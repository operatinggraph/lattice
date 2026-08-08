package keyshredded

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// fakeNullifier is a control.RowNullifier test double. Delete returns err
// (nil for success) and records every call.
type fakeNullifier struct {
	err   error
	calls []map[string]any
}

func (f *fakeNullifier) Delete(_ context.Context, keys map[string]any, _ uint64) error {
	f.calls = append(f.calls, keys)
	return f.err
}

// fakeRowSetNullifier is a control.RowSetNullifier test double.
// DeleteAllForActor returns err (nil for success) and records every call.
type fakeRowSetNullifier struct {
	err   error
	calls []string
}

func (f *fakeRowSetNullifier) DeleteAllForActor(_ context.Context, actorKey string, _ uint64) error {
	f.calls = append(f.calls, actorKey)
	return f.err
}

// fakeGrantRevoker is a GrantTableRevoker test double. RevokeAllGrantsForActor
// returns err (nil for success) and records every call.
type fakeGrantRevoker struct {
	err   error
	calls []string
}

func (f *fakeGrantRevoker) RevokeAllGrantsForActor(_ context.Context, actorID string, _ uint64) error {
	f.calls = append(f.calls, actorID)
	return f.err
}

// fakePauser is a control.Pauser test double recording whether Pause was called.
type fakePauser struct {
	paused bool
}

func (f *fakePauser) Pause(_ context.Context) { f.paused = true }

func newTestManager(t *testing.T, svc *control.Service, targets []NullifyTarget) *Manager {
	t.Helper()
	return New(Config{Control: svc, Targets: targets})
}

func keyShreddedMsg(t *testing.T, identityKey string) substrate.Message {
	t.Helper()
	body := []byte(`{"payload":{"identityKey":"` + identityKey + `"}}`)
	return substrate.Message{Body: body}
}

func TestHandleKeyShredded_NoTargets_AcksAndCounts(t *testing.T) {
	svc := control.NewService()
	m := newTestManager(t, svc, nil)

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, uint64(1), m.HandledTotal())
}

func TestHandleKeyShredded_TargetSucceeds_DeletesAndAcks(t *testing.T) {
	svc := control.NewService()
	nullifier := &fakeNullifier{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, uint64(1), m.HandledTotal())
	require.Len(t, nullifier.calls, 1)
	require.Equal(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA", nullifier.calls[0]["identityKey"])
}

func TestHandleKeyShredded_MultipleTargets_AllAttempted(t *testing.T) {
	svc := control.NewService()
	nullifierA := &fakeNullifier{}
	nullifierB := &fakeNullifier{}
	svc.RegisterRowNullifier("lens-a", nullifierA)
	svc.RegisterRowNullifier("lens-b", nullifierB)
	m := newTestManager(t, svc, []NullifyTarget{
		{RuleID: "lens-a", KeyField: "identityKey"},
		{RuleID: "lens-b", KeyField: "identityKey"},
	})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Len(t, nullifierA.calls, 1)
	require.Len(t, nullifierB.calls, 1)
}

// TestHandleKeyShredded_TargetNotRegistered_NaksForRedelivery covers the
// still-starting-up case: a configured target whose lens hasn't registered
// yet is treated as transient (redeliver), not privacy-critical.
func TestHandleKeyShredded_TargetNotRegistered_NaksForRedelivery(t *testing.T) {
	svc := control.NewService() // lens-a never registered
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.NakWithDelay, decision)
	require.Equal(t, uint64(0), m.HandledTotal(), "not-yet-registered must not count as handled")
}

// TestHandleKeyShredded_TargetNeverRegisters_GivesUpAfterMaxDeliveries proves
// a permanently-misconfigured RuleID (a typo'd/decommissioned target) stops
// nak-looping once NumDelivered reaches maxNotRegisteredDeliveries, instead
// of retrying forever.
func TestHandleKeyShredded_TargetNeverRegisters_GivesUpAfterMaxDeliveries(t *testing.T) {
	svc := control.NewService() // lens-a never registered
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}})

	msg := keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA")
	msg.NumDelivered = maxNotRegisteredDeliveries

	decision := m.handleKeyShredded(context.Background(), msg)

	require.Equal(t, substrate.Ack, decision, "must give up (Ack) rather than nak forever once the threshold is reached")
	require.Equal(t, uint64(1), m.HandledTotal())
}

// TestNew_NilControl_Panics proves a misconfigured Manager fails at
// construction (fail fast) rather than mid-stream on the first real event.
func TestNew_NilControl_Panics(t *testing.T) {
	require.Panics(t, func() {
		New(Config{Control: nil})
	})
}

// TestHandleKeyShredded_TargetLister_DynamicTargetNullified proves a target
// supplied only by SetTargetLister (nothing in the static Targets config) is
// attempted exactly like a configured one — the discovery path a
// package-generated cap-read.* producer's install-time NanoID RuleID needs,
// since it cannot be hand-listed at Manager construction.
func TestHandleKeyShredded_TargetLister_DynamicTargetNullified(t *testing.T) {
	svc := control.NewService()
	rowSetNullifier := &fakeRowSetNullifier{}
	svc.RegisterRowSetNullifier("lens-dynamic", rowSetNullifier)
	m := newTestManager(t, svc, nil)
	m.SetTargetLister(func() []NullifyTarget {
		return []NullifyTarget{{RuleID: "lens-dynamic", PerEntry: true}}
	})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, []string{"vtx.identity.AAAAAAAAAAAAAAAAAAAA"}, rowSetNullifier.calls)
}

// TestHandleKeyShredded_TargetLister_AdditiveToStaticTargets proves the
// dynamic list supplements, never replaces, the static Targets config — a
// plain lens's explicit entry is still attempted alongside whatever the
// lister supplies.
func TestHandleKeyShredded_TargetLister_AdditiveToStaticTargets(t *testing.T) {
	svc := control.NewService()
	staticNullifier := &fakeNullifier{}
	dynamicNullifier := &fakeRowSetNullifier{}
	svc.RegisterRowNullifier("lens-static", staticNullifier)
	svc.RegisterRowSetNullifier("lens-dynamic", dynamicNullifier)
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-static", KeyField: "identityKey"}})
	m.SetTargetLister(func() []NullifyTarget {
		return []NullifyTarget{{RuleID: "lens-dynamic", PerEntry: true}}
	})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Len(t, staticNullifier.calls, 1, "the static target must still be attempted")
	require.Len(t, dynamicNullifier.calls, 1, "the TargetLister target must also be attempted")
}

// TestHandleKeyShredded_TargetLister_ReEvaluatedPerEvent proves the lister is
// called fresh on every event rather than cached at SetTargetLister time —
// the property that lets a package installed AFTER Manager construction
// start getting nullified without any Refractor restart.
func TestHandleKeyShredded_TargetLister_ReEvaluatedPerEvent(t *testing.T) {
	svc := control.NewService()
	nullifier := &fakeRowSetNullifier{}
	m := newTestManager(t, svc, nil)
	installed := false
	m.SetTargetLister(func() []NullifyTarget {
		if !installed {
			return nil
		}
		return []NullifyTarget{{RuleID: "lens-late", PerEntry: true}}
	})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))
	require.Equal(t, substrate.Ack, decision, "an empty lister result is a vacuous no-op, not a failure")

	svc.RegisterRowSetNullifier("lens-late", nullifier)
	installed = true
	decision = m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.BBBBBBBBBBBBBBBBBBBB"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, []string{"vtx.identity.BBBBBBBBBBBBBBBBBBBB"}, nullifier.calls, "the second event must reach the just-installed lens")
}

// TestHandleKeyShredded_TargetLister_EmptyDuringBoot_StaticFloorStillNaks
// pins a regression an adversarial review caught: cmd/refractor keeps a
// static base-lens Targets entry ALONGSIDE the dynamic TargetLister
// specifically because the lens registry a TargetLister reads is empty for a
// window at process start (before the lens source's initial catch-up). If a
// deployment relied on the lister alone, a shred event delivered in that
// window would see zero targets and Ack + record finalization having
// nullified nothing. With the static entry present, the effective target
// list can never be empty, so an unregistered lens correctly forces
// NakWithDelay instead.
func TestHandleKeyShredded_TargetLister_EmptyDuringBoot_StaticFloorStillNaks(t *testing.T) {
	svc := control.NewService() // the base lens has not registered yet — boot window
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "base-lens", PerEntry: true}})
	m.SetTargetLister(func() []NullifyTarget { return nil }) // registry still empty

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.NakWithDelay, decision, "an empty dynamic result must not let the static floor's own not-yet-registered target Ack as clean")
	require.Equal(t, uint64(0), m.HandledTotal(), "nothing was actually nullified; this must not count as handled")
}

// TestHandleKeyShredded_NullifyFails_RaisesPrivacyCriticalPausesNoRetry is the
// failure-tier proof (vault-crypto-shredding-design.md §6 "a forced
// nullification failure raises the privacy-critical tier — lens halts, no
// retry, alert emitted"): a real Delete failure must pause the affected lens
// and Ack (never retry) rather than Nak.
func TestHandleKeyShredded_NullifyFails_RaisesPrivacyCriticalPausesNoRetry(t *testing.T) {
	svc := control.NewService()
	boom := errors.New("adapter: boom")
	nullifier := &fakeNullifier{err: boom}
	pauser := &fakePauser{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	svc.RegisterPauser("lens-a", pauser)
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision, "a privacy-critical failure must never be retried")
	require.True(t, pauser.paused, "the affected lens must be paused")
	require.Equal(t, uint64(1), m.HandledTotal())
}

// TestHandleKeyShredded_OneTargetFailsAnotherSucceeds_BothAttempted proves a
// privacy-critical failure on one target does not skip the remaining ones.
func TestHandleKeyShredded_OneTargetFailsAnotherSucceeds_BothAttempted(t *testing.T) {
	svc := control.NewService()
	failing := &fakeNullifier{err: errors.New("boom")}
	ok := &fakeNullifier{}
	pauser := &fakePauser{}
	svc.RegisterRowNullifier("lens-fail", failing)
	svc.RegisterPauser("lens-fail", pauser)
	svc.RegisterRowNullifier("lens-ok", ok)
	m := newTestManager(t, svc, []NullifyTarget{
		{RuleID: "lens-fail", KeyField: "identityKey"},
		{RuleID: "lens-ok", KeyField: "identityKey"},
	})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.True(t, pauser.paused)
	require.Len(t, failing.calls, 1)
	require.Len(t, ok.calls, 1, "the second target must still be attempted after the first fails")
}

// TestHandleKeyShredded_PerEntryTarget_RoutesThroughNullifyActor proves a
// PerEntry target calls Control.NullifyActor (enumerate-then-delete-all)
// instead of NullifyRow's single explicit-key delete, and that KeyField is
// unused for a PerEntry target.
func TestHandleKeyShredded_PerEntryTarget_RoutesThroughNullifyActor(t *testing.T) {
	svc := control.NewService()
	rowNullifier := &fakeNullifier{}
	rowSetNullifier := &fakeRowSetNullifier{}
	svc.RegisterRowNullifier("lens-a", rowNullifier)
	svc.RegisterRowSetNullifier("lens-a", rowSetNullifier)
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", PerEntry: true}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, uint64(1), m.HandledTotal())
	require.Empty(t, rowNullifier.calls, "a PerEntry target must never call the single-key NullifyRow path")
	require.Equal(t, []string{"vtx.identity.AAAAAAAAAAAAAAAAAAAA"}, rowSetNullifier.calls)
}

// TestHandleKeyShredded_PerEntryTarget_NotRegistered_NaksForRedelivery proves
// the PerEntry path shares the same not-yet-registered retry behavior as the
// doc-mode path (ErrRuleNotRegistered from NullifyActor naks for redelivery,
// bounded by maxNotRegisteredDeliveries).
func TestHandleKeyShredded_PerEntryTarget_NotRegistered_NaksForRedelivery(t *testing.T) {
	svc := control.NewService() // lens-a never registered as a RowSetNullifier
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", PerEntry: true}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.NakWithDelay, decision)
	require.Equal(t, uint64(0), m.HandledTotal())
}

// TestHandleKeyShredded_PerEntryTarget_DeleteFails_RaisesPrivacyCritical
// proves a real DeleteAllForActor failure on a PerEntry target pauses the
// lens and Acks (never retries), mirroring the doc-mode failure tier.
func TestHandleKeyShredded_PerEntryTarget_DeleteFails_RaisesPrivacyCritical(t *testing.T) {
	svc := control.NewService()
	boom := errors.New("adapter: boom")
	rowSetNullifier := &fakeRowSetNullifier{err: boom}
	pauser := &fakePauser{}
	svc.RegisterRowSetNullifier("lens-a", rowSetNullifier)
	svc.RegisterPauser("lens-a", pauser)
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", PerEntry: true}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision, "a privacy-critical failure must never be retried")
	require.True(t, pauser.paused)
	require.Equal(t, uint64(1), m.HandledTotal())
}

// TestHandleKeyShredded_GrantRevoker_Succeeds proves a configured
// GrantTableRevoker is invoked with the shredded identityKey and Acks.
func TestHandleKeyShredded_GrantRevoker_Succeeds(t *testing.T) {
	svc := control.NewService()
	revoker := &fakeGrantRevoker{}
	m := New(Config{Control: svc, GrantRevokers: []GrantTableRevoker{revoker}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, []string{"vtx.identity.AAAAAAAAAAAAAAAAAAAA"}, revoker.calls)
}

// TestHandleKeyShredded_GrantRevokerLister_DynamicAndAdditive proves a
// GrantTableRevoker supplied only by SetGrantRevokerLister is attempted
// alongside a statically configured one — the discovery path a Postgres
// GrantTable producer's runtime-acquired DSN needs, mirroring
// SetTargetLister's additive contract for the NATS-KV side.
func TestHandleKeyShredded_GrantRevokerLister_DynamicAndAdditive(t *testing.T) {
	svc := control.NewService()
	static := &fakeGrantRevoker{}
	dynamic := &fakeGrantRevoker{}
	m := New(Config{Control: svc, GrantRevokers: []GrantTableRevoker{static}})
	m.SetGrantRevokerLister(func() []GrantTableRevoker {
		return []GrantTableRevoker{dynamic}
	})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Len(t, static.calls, 1, "the static revoker must still be attempted")
	require.Len(t, dynamic.calls, 1, "the GrantRevokerLister revoker must also be attempted")
}

// TestHandleKeyShredded_GrantRevokerLister_ReEvaluatedPerEvent proves the
// lister is called fresh on every event, not cached at registration time —
// the property that lets a Postgres GrantTable producer installed AFTER
// Manager construction start getting revoked without a Refractor restart.
func TestHandleKeyShredded_GrantRevokerLister_ReEvaluatedPerEvent(t *testing.T) {
	svc := control.NewService()
	m := New(Config{Control: svc})
	revoker := &fakeGrantRevoker{}
	installed := false
	m.SetGrantRevokerLister(func() []GrantTableRevoker {
		if !installed {
			return nil
		}
		return []GrantTableRevoker{revoker}
	})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))
	require.Equal(t, substrate.Ack, decision, "an empty lister result is a vacuous no-op, not a failure")

	installed = true
	decision = m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.BBBBBBBBBBBBBBBBBBBB"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, []string{"vtx.identity.BBBBBBBBBBBBBBBBBBBB"}, revoker.calls, "the second event must reach the just-installed revoker")
}

// TestHandleKeyShredded_GrantRevokerFails_PrivacyCriticalNoRetry proves a
// real RevokeAllGrantsForActor failure Acks (never retries) — there is no
// single lens's RuleID to pause (actor_read_grants is shared across every
// grant-source producer), so the privacy-critical tier here is alert-only,
// distinct from the per-lens pause a NullifyTarget failure gets.
func TestHandleKeyShredded_GrantRevokerFails_PrivacyCriticalNoRetry(t *testing.T) {
	svc := control.NewService()
	boom := errors.New("grant writer: revoke all: boom")
	revoker := &fakeGrantRevoker{err: boom}
	m := New(Config{Control: svc, GrantRevokers: []GrantTableRevoker{revoker}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision, "a privacy-critical failure must never be retried")
	require.Equal(t, uint64(1), m.HandledTotal())
	require.Len(t, revoker.calls, 1)
}

// TestHandleKeyShredded_GrantRevokerFails_OtherTargetsStillAttempted proves a
// failing GrantTableRevoker does not skip the remaining NullifyTarget
// entries — every target (KV and Postgres alike) is always attempted.
func TestHandleKeyShredded_GrantRevokerFails_OtherTargetsStillAttempted(t *testing.T) {
	svc := control.NewService()
	nullifier := &fakeNullifier{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	revoker := &fakeGrantRevoker{err: errors.New("boom")}
	m := New(Config{
		Control:       svc,
		Targets:       []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}},
		GrantRevokers: []GrantTableRevoker{revoker},
	})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Len(t, nullifier.calls, 1, "the NullifyTarget must still be attempted despite the revoker failure")
	require.Len(t, revoker.calls, 1)
}

func TestHandleKeyShredded_EmptyBody_Acks(t *testing.T) {
	svc := control.NewService()
	m := newTestManager(t, svc, nil)

	decision := m.handleKeyShredded(context.Background(), substrate.Message{})

	require.Equal(t, substrate.Ack, decision)
}

func TestHandleKeyShredded_UnparseableBody_Terms(t *testing.T) {
	svc := control.NewService()
	m := newTestManager(t, svc, nil)

	decision := m.handleKeyShredded(context.Background(), substrate.Message{Body: []byte("not json")})

	require.Equal(t, substrate.Term, decision)
}

func TestHandleKeyShredded_MissingIdentityKey_Terms(t *testing.T) {
	svc := control.NewService()
	m := newTestManager(t, svc, nil)

	decision := m.handleKeyShredded(context.Background(), substrate.Message{Body: []byte(`{"payload":{}}`)})

	require.Equal(t, substrate.Term, decision)
}

// TestFailurePrivacyCritical_Classify proves the new failure.PrivacyCritical
// tier round-trips through failure.Classify (mirrors the pattern each of the
// other three tiers already covers).
func TestFailurePrivacyCritical_Classify(t *testing.T) {
	err := failure.PrivacyCritical(errors.New("row nullify failed"))
	require.Equal(t, failure.CatPrivacyCritical, failure.Classify(err))
}

// newSubmitTestConn starts an embedded NATS + JetStream with a
// core-operations-shaped stream, for the Fire-4b finalization-submit tests.
// Mirrors internal/privacyworker's harness (jsstore.Dir StoreDir convention).
func newSubmitTestConn(t *testing.T) (*substrate.Conn, context.Context, jetstream.Consumer) {
	t.Helper()
	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	stream, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "core-operations", Subjects: []string{"ops.>"},
	})
	require.NoError(t, err)
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{Durable: "ops-observer"})
	require.NoError(t, err)
	return conn, ctx, cons
}

func fetchOneOp(t *testing.T, cons jetstream.Consumer) []byte {
	t.Helper()
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(3*time.Second))
	require.NoError(t, err)
	for m := range msgs.Messages() {
		require.NoError(t, m.Ack())
		return m.Data()
	}
	return nil
}

// TestHandleKeyShredded_CleanPath_SubmitsFinalization proves Fire 4b: with an
// ActorKey configured and every target nullifying cleanly, the listener
// publishes exactly one RecordShredFinalization{projectionsNullified} to
// ops.system before Acking.
func TestHandleKeyShredded_CleanPath_SubmitsFinalization(t *testing.T) {
	conn, ctx, opsCons := newSubmitTestConn(t)
	svc := control.NewService()
	nullifier := &fakeNullifier{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	const actorKey = "vtx.identity.PrivacyActorKMNPQRST"
	m := New(Config{
		Conn: conn, Control: svc, ActorKey: actorKey,
		Targets: []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}},
	})

	decision := m.handleKeyShredded(ctx, keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	data := fetchOneOp(t, opsCons)
	require.NotNil(t, data, "expected a RecordShredFinalization op on ops.system")
	var env struct {
		RequestID     string `json:"requestId"`
		Lane          string `json:"lane"`
		OperationType string `json:"operationType"`
		Actor         string `json:"actor"`
		Payload       struct {
			IdentityKey string `json:"identityKey"`
			Step        string `json:"step"`
		} `json:"payload"`
		ContextHint struct {
			Reads []string `json:"reads"`
		} `json:"contextHint"`
	}
	require.NoError(t, json.Unmarshal(data, &env))
	require.Equal(t, "RecordShredFinalization", env.OperationType)
	require.Equal(t, "system", env.Lane)
	require.Equal(t, actorKey, env.Actor)
	require.Equal(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA", env.Payload.IdentityKey)
	require.Equal(t, StepProjectionsNullified, env.Payload.Step)
	require.True(t, substrate.IsValidNanoID(env.RequestID))
	// Two declared reads, each load-bearing for a different reason: the piiKey is
	// the OCC condition against the sibling record racing this one on the system
	// lane, and the ACTOR's own vertex is what the finalization script reads to
	// refuse an attestation written by anyone but the privacy service actor. An
	// undeclared actor fails the op closed, so dropping either is a live outage,
	// not a test failure — which is exactly why it is asserted here.
	require.Equal(t, []string{"vtx.identity.AAAAAAAAAAAAAAAAAAAA.piiKey", actorKey},
		env.ContextHint.Reads)
}

// TestHandleKeyShredded_PrivacyCritical_SkipsFinalization proves a
// privacy-critical nullification failure still Acks (never retries) but does
// NOT record projectionsNullified — the shredStatus row stays visibly stuck.
func TestHandleKeyShredded_PrivacyCritical_SkipsFinalization(t *testing.T) {
	conn, ctx, opsCons := newSubmitTestConn(t)
	svc := control.NewService()
	nullifier := &fakeNullifier{err: errors.New("injected delete failure")}
	pauser := &fakePauser{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	svc.RegisterPauser("lens-a", pauser)
	m := New(Config{
		Conn: conn, Control: svc, ActorKey: "vtx.identity.PrivacyActorKMNPQRST",
		Targets: []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}},
	})

	decision := m.handleKeyShredded(ctx, keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision, "privacy-critical is Acked, never retried")
	require.True(t, pauser.paused)
	msgs, err := opsCons.Fetch(1, jetstream.FetchMaxWait(1500*time.Millisecond))
	require.NoError(t, err)
	for range msgs.Messages() {
		t.Fatal("no RecordShredFinalization must be published after a privacy-critical failure")
	}
}

// TestHandleKeyShredded_NoActorKey_NoSubmit proves the disabled posture (a
// pre-v15 kernel): the clean path still Acks + counts with no op published.
func TestHandleKeyShredded_NoActorKey_NoSubmit(t *testing.T) {
	conn, ctx, opsCons := newSubmitTestConn(t)
	svc := control.NewService()
	nullifier := &fakeNullifier{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	m := New(Config{
		Conn: conn, Control: svc,
		Targets: []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}},
	})

	decision := m.handleKeyShredded(ctx, keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, uint64(1), m.HandledTotal())
	msgs, err := opsCons.Fetch(1, jetstream.FetchMaxWait(1500*time.Millisecond))
	require.NoError(t, err)
	for range msgs.Messages() {
		t.Fatal("no op must be published without an ActorKey")
	}
}
