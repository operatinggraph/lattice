package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// recordingReporter records the sequence of status writes the sink issues.
// entry, when set, is what GetStatus returns — the persisted state a restarting
// sink restores from.
type recordingReporter struct {
	writes []string
	entry  *health.Entry
}

func (r *recordingReporter) SetActive(context.Context) error {
	r.writes = append(r.writes, "active")
	return nil
}

func (r *recordingReporter) SetPaused(_ context.Context, reason, _ string) error {
	r.writes = append(r.writes, "paused:"+reason)
	return nil
}

func (r *recordingReporter) SetRebuilding(context.Context) error {
	r.writes = append(r.writes, "rebuilding")
	return nil
}

func (r *recordingReporter) GetStatus(context.Context) (health.Entry, error) {
	if r.entry != nil {
		return *r.entry, nil
	}
	return health.Entry{Status: "active"}, nil
}

// structurallyPaused is the persisted shape a lens that paused before a restart
// leaves behind: paused, structural, and still carrying its diagnosis (which it
// keeps for the life of the pause).
func structurallyPaused(cause string) *health.Entry {
	reason := health.PauseReasonStructural
	return &health.Entry{Status: health.StatusPaused, PauseReason: &reason, LastError: &cause}
}

func (r *recordingReporter) RecordStructuralAutoRecovery(_ context.Context, cause string, attempts int) error {
	r.writes = append(r.writes, fmt.Sprintf("auto-recovered:%d:%s", attempts, cause))
	return nil
}

// The sink must satisfy the OPTIONAL half of substrate.HealthSink: the
// supervisor asserts StructuralRecoveryAnnouncer on spec.Health and tells a sink
// that does not implement it nothing at all, so a dropped method would silence
// the whole self-heal signal with everything still compiling and green.
func TestHealthSink_AnnouncesStructuralAutoRecovery(t *testing.T) {
	rec := &recordingReporter{}
	sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return false }}

	announcer, ok := any(sink).(substrate.StructuralRecoveryAnnouncer)
	require.True(t, ok, "healthSink must satisfy substrate.StructuralRecoveryAnnouncer")

	require.NoError(t, announcer.RecordStructuralAutoRecovery(context.Background(), "column absent", 2))
	assert.Equal(t, []string{"auto-recovered:2:column absent"}, rec.writes)
}

// The restart path, and the one the design cares most about: a lens paused
// before a restart is restored by Load, healed by the probe on the way back up,
// and announced with an EMPTY cause — the supervisor never saw a diagnosis,
// because HealthSink.Load reports status and reason and nothing else. Without
// the stash the likeliest real recovery of all announces with nothing to act on.
func TestHealthSink_RestoredStructuralCauseFillsAnEmptyAnnouncement(t *testing.T) {
	ctx := context.Background()
	rec := &recordingReporter{entry: structurallyPaused(`column "discharged_at" does not exist`)}
	sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return false }}

	status, reason, err := sink.Load(ctx)
	require.NoError(t, err)
	require.Equal(t, substrate.StatusPaused, status)
	require.Equal(t, substrate.PauseStructural, reason)

	// The clear runs BEFORE the announcement and nils LastError on the entry,
	// which is exactly why the capture has to happen at Load.
	require.NoError(t, sink.SetActive(ctx))
	require.NoError(t, sink.RecordStructuralAutoRecovery(ctx, "", 1))

	assert.Equal(t, []string{"active", `auto-recovered:1:column "discharged_at" does not exist`}, rec.writes)
}

// A cause the supervisor DID supply describes the pause that just cleared; the
// stashed one describes whatever was on the entry at boot. The live one wins.
func TestHealthSink_SupervisorCauseBeatsTheRestoredOne(t *testing.T) {
	ctx := context.Background()
	rec := &recordingReporter{entry: structurallyPaused("stale boot diagnosis")}
	sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return false }}

	_, _, err := sink.Load(ctx)
	require.NoError(t, err)
	require.NoError(t, sink.RecordStructuralAutoRecovery(ctx, "constraint absent", 1))

	assert.Equal(t, []string{"auto-recovered:1:constraint absent"}, rec.writes)
}

// The stash is consumed once and superseded by any pause this process persists.
// A cached diagnosis with no boundary is how a later, unrelated recovery comes
// to announce an older lens fault as its own.
func TestHealthSink_RestoredStructuralCauseIsNotInherited(t *testing.T) {
	ctx := context.Background()

	t.Run("consumed once", func(t *testing.T) {
		rec := &recordingReporter{entry: structurallyPaused("boot diagnosis")}
		sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return false }}
		_, _, err := sink.Load(ctx)
		require.NoError(t, err)

		require.NoError(t, sink.RecordStructuralAutoRecovery(ctx, "", 1))
		require.NoError(t, sink.RecordStructuralAutoRecovery(ctx, "", 2))

		assert.Equal(t, []string{"auto-recovered:1:boot diagnosis", "auto-recovered:2:"}, rec.writes)
	})

	t.Run("superseded by a STRUCTURAL pause this process persists", func(t *testing.T) {
		rec := &recordingReporter{entry: structurallyPaused("boot diagnosis")}
		sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return false }}
		_, _, err := sink.Load(ctx)
		require.NoError(t, err)

		require.NoError(t, sink.SetPaused(ctx, substrate.PauseStructural, "a newer fault"))
		require.NoError(t, sink.RecordStructuralAutoRecovery(ctx, "", 1))

		assert.Equal(t, []string{"paused:structural", "auto-recovered:1:"}, rec.writes)
	})
}

// The REAL restart lifecycle, which every test above skips. Any lens opted into
// the structural probe is opted in by the same predicate that gives it
// InitialPause, so its way back up is: restore structural → structural probe
// passes → the pump re-seeds the infra gate → SetPaused(infra, "") → infra probe
// passes → announce. A stash discarded by ANY pause is eaten by that infra write
// one step before the announcement that needs it, and the operator gets an
// attempt count with no diagnosis — on the recovery the design calls the
// likeliest of all. Neither layer's own tests can see this: the substrate side
// pins the SetPaused(infra, "") in isolation, and the sink side went
// Load → SetActive → Record without ever interposing it.
func TestHealthSink_RestoredStructuralCauseSurvivesTheReseededInfraGate(t *testing.T) {
	ctx := context.Background()

	t.Run("infra — the gate the restart path re-seeds", func(t *testing.T) {
		rec := &recordingReporter{entry: structurallyPaused(`column "discharged_at" does not exist`)}
		sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return false }}
		_, _, err := sink.Load(ctx)
		require.NoError(t, err)

		require.NoError(t, sink.SetPaused(ctx, substrate.PauseInfra, ""))
		require.NoError(t, sink.SetActive(ctx))
		require.NoError(t, sink.RecordStructuralAutoRecovery(ctx, "", 1))

		assert.Equal(t, []string{
			"paused:infra", "active", `auto-recovered:1:column "discharged_at" does not exist`,
		}, rec.writes)
	})

	// A manual pause carries no structural diagnosis either, and never will, so
	// discarding on it would drop the restored cause with nothing to replace it.
	t.Run("manual", func(t *testing.T) {
		rec := &recordingReporter{entry: structurallyPaused("boot diagnosis")}
		sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return false }}
		_, _, err := sink.Load(ctx)
		require.NoError(t, err)

		require.NoError(t, sink.SetPaused(ctx, substrate.PauseManual, ""))
		require.NoError(t, sink.RecordStructuralAutoRecovery(ctx, "", 1))

		assert.Equal(t, []string{"paused:manual", "auto-recovered:1:boot diagnosis"}, rec.writes)
	})
}

// An infra or manual restore stashes nothing: neither is a structural pause, and
// neither can be lifted by the structural probe, so a cause carried over from
// one could only ever be attributed to the wrong recovery.
func TestHealthSink_OnlyAStructuralRestoreStashesItsCause(t *testing.T) {
	ctx := context.Background()
	for _, reason := range []string{health.PauseReasonInfra, health.PauseReasonManual} {
		t.Run(reason, func(t *testing.T) {
			r := reason
			cause := "some other tier's fault"
			rec := &recordingReporter{entry: &health.Entry{
				Status: health.StatusPaused, PauseReason: &r, LastError: &cause,
			}}
			sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return false }}
			_, _, err := sink.Load(ctx)
			require.NoError(t, err)

			require.NoError(t, sink.RecordStructuralAutoRecovery(ctx, "", 1))
			assert.Equal(t, []string{"auto-recovered:1:"}, rec.writes)
		})
	}
}

// TestHealthSink_SetActive_NoRebuild verifies the plain path: no rebuild in
// flight → SetActive writes "active".
func TestHealthSink_SetActive_NoRebuild(t *testing.T) {
	rec := &recordingReporter{}
	sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return false }}

	require.NoError(t, sink.SetActive(context.Background()))
	assert.Equal(t, []string{"active"}, rec.writes)
}

// TestHealthSink_SetActive_RebuildInFlight verifies that a supervisor
// active-persist during an in-flight rebuild (probe recovery mid-rescan)
// re-persists "rebuilding" and never writes a premature "active".
func TestHealthSink_SetActive_RebuildInFlight(t *testing.T) {
	rec := &recordingReporter{}
	sink := &healthSink{reporter: rec, rebuildInFlight: func() bool { return true }}

	require.NoError(t, sink.SetActive(context.Background()))
	assert.Equal(t, []string{"rebuilding"}, rec.writes)
}

// TestHealthSink_SetActive_RebuildCompletesDuringWrite verifies the
// double-check: when the rebuild watcher clears the flag concurrently with the
// sink's rebuilding write, the sink falls through to "active" so the entry is
// not left "rebuilding" with no watcher remaining to clear it.
func TestHealthSink_SetActive_RebuildCompletesDuringWrite(t *testing.T) {
	rec := &recordingReporter{}
	calls := 0
	sink := &healthSink{reporter: rec, rebuildInFlight: func() bool {
		calls++
		return calls == 1 // in flight on the first check, cleared on the re-check
	}}

	require.NoError(t, sink.SetActive(context.Background()))
	assert.Equal(t, []string{"rebuilding", "active"}, rec.writes)
}

// TestClassifyForSupervisor_CatPrivacyCritical_MapsToTerminal pins the explicit
// CatPrivacyCritical case (G12): it must not fall through the default arm to
// ClassTransient (an infinite auto-redeliver loop — the opposite of "never
// auto-retry", docs/components/refractor-failure-tiers.md), and it must not
// map to ClassStructural or ClassInfra either, since both carry probe-driven
// auto-recovery machinery, and neither probe verifies that a
// privacy-critical condition was actually remediated.
func TestClassifyForSupervisor_CatPrivacyCritical_MapsToTerminal(t *testing.T) {
	err := failure.PrivacyCritical(errors.New("could not nullify shredded row"))
	require.Equal(t, failure.CatPrivacyCritical, failure.Classify(err), "sanity: PrivacyCritical must classify CatPrivacyCritical")
	assert.Equal(t, substrate.ClassTerminal, classifyForSupervisor(err),
		"CatPrivacyCritical must map explicitly, and never to a class carrying probe-driven auto-recovery")
}

// keyedAdapter fails Upsert/Delete with the error configured for the result's
// "k" key value; keys without an entry succeed.
type keyedAdapter struct {
	errs map[string]error
}

func (a *keyedAdapter) write(keys map[string]any) error {
	k, _ := keys["k"].(string)
	return a.errs[k]
}

func (a *keyedAdapter) Upsert(_ context.Context, keys map[string]any, _ map[string]any, _ uint64) error {
	return a.write(keys)
}
func (a *keyedAdapter) Delete(_ context.Context, keys map[string]any, _ uint64) error {
	return a.write(keys)
}
func (a *keyedAdapter) Probe(context.Context) error { return nil }
func (a *keyedAdapter) Close() error                { return nil }

// TestWriteResults_NoRetryEnqueueWhileBatchLeftPending verifies that a batch
// whose early result fails transient and whose later result fails infra leaves
// the message pending WITHOUT enqueuing the transient result: redelivery
// re-runs the whole batch, so an eager enqueue would add a duplicate
// retry-queue entry on every pause/resume cycle.
func TestWriteResults_NoRetryEnqueueWhileBatchLeftPending(t *testing.T) {
	ad := &keyedAdapter{errs: map[string]error{
		"a": errors.New("transient write failure"), // CatTransient
		"b": nats.ErrConnectionClosed,              // CatInfra
	}}
	rq := failure.NewRetryQueue()

	p, err := New("rule-dedup", "nats_kv", "CORE", nil, nil, ad, nil)
	require.NoError(t, err)
	p.SetRetryQueue(rq, nil, 3, time.Millisecond)

	ctx := context.Background()
	msg := substrate.Message{Subject: "$KV.CORE.vtx.agreement.x", Body: []byte(`{"id":"x"}`)}
	results := []ruleengine.EvalResult{
		{Keys: map[string]any{"k": "a"}, Row: map[string]any{"k": "a"}},
		{Keys: map[string]any{"k": "b"}, Row: map[string]any{"k": "b"}},
	}

	// First delivery: infra on "b" leaves the message pending; "a" must NOT
	// have been enqueued.
	dec, werr := p.writeResults(ctx, p.ruleState(), msg, "vtx.agreement.x", results, nil, ScopeAll())
	assert.Equal(t, substrate.Nak, dec)
	require.Error(t, werr)
	assert.Equal(t, 0, rq.Len(), "no retry entry while the message is left pending")

	// Redelivery while the infra failure persists (each pause/resume cycle):
	// still no accumulation.
	dec, werr = p.writeResults(ctx, p.ruleState(), msg, "vtx.agreement.x", results, nil, ScopeAll())
	assert.Equal(t, substrate.Nak, dec)
	require.Error(t, werr)
	assert.Equal(t, 0, rq.Len(), "redelivery must not accumulate retry entries")

	// Infra recovers; the transient failure persists → the batch disposes via
	// the retry queue exactly once and the message is acked.
	delete(ad.errs, "b")
	dec, werr = p.writeResults(ctx, p.ruleState(), msg, "vtx.agreement.x", results, nil, ScopeAll())
	assert.Equal(t, substrate.Ack, dec)
	require.NoError(t, werr)
	assert.Equal(t, 1, rq.Len(), "exactly one retry entry once the batch disposes")
}
