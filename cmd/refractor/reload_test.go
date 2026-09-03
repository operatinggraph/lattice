package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/reloadpin"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// authPlaneRule is an actor-aggregate lens projecting the capability-kv
// authorization surface — the shape whose adapter must carry the §6.2 guard.
func authPlaneRule(t *testing.T) *lens.Rule {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
RETURN identity.key AS actorKey, collect(task.key) AS tasks
`)
	require.NoError(t, err)
	return &lens.Rule{
		ID:             "lens-reload-test",
		CanonicalName:  "reloadTest",
		ProjectionKind: projection.ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into:           lens.IntoConfig{Target: "nats_kv", Bucket: "capability-kv", Key: lens.KeyField{"key"}},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap.{actorSuffix}",
			BodyColumns:      []string{"tasks"},
			EmptyBehavior:    "delete",
			Freshness:        "auto",
		},
	}
}

func newKVAdapter(t *testing.T) *adapter.NatsKVAdapter {
	t.Helper()
	a, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	return a
}

// ---------------------------------------------------------------------------
// buildAdapter — the guard belongs to every adapter built for the rule
// ---------------------------------------------------------------------------

func TestBuildAdapter_AuthPlaneRule_BindsTheGuard(t *testing.T) {
	r := authPlaneRule(t)
	adpt, err := buildAdapter(r, func(*lens.Rule) (adapter.Adapter, error) {
		return newKVAdapter(t), nil
	})
	require.NoError(t, err)
	assert.True(t, adapterIsGuarded(adpt),
		"an auth-plane rule's adapter must carry the §6.2 projection-write guard")
}

func TestBuildAdapter_PlainRule_LeavesTheAdapterOpen(t *testing.T) {
	r := authPlaneRule(t)
	// Neither driver of the guard: an ordinary read-model bucket, and an empty
	// behavior that writes a document rather than a §6.2 tombstone.
	r.Into.Bucket = "weaver-targets"
	r.Output.EmptyBehavior = "emptyDoc"
	adpt, err := buildAdapter(r, func(*lens.Rule) (adapter.Adapter, error) {
		return newKVAdapter(t), nil
	})
	require.NoError(t, err)
	assert.False(t, adapterIsGuarded(adpt), "a plain lens is not guarded")
}

func TestBuildAdapter_GuardRequiredButTargetCannotEnforce_FailsClosed(t *testing.T) {
	r := authPlaneRule(t)
	// A Postgres adapter cannot carry the ordering token, so a rule that
	// requires it must fail to build rather than run the lens open.
	_, err := buildAdapter(r, func(*lens.Rule) (adapter.Adapter, error) {
		return unguardableAdapter{}, nil
	})
	require.Error(t, err)
}

// TestBuildAdapter_SharedBucketRule_BindsTheTruncateScope pins the OTHER rule
// property every adapter built for a lens must acquire. A rebuild truncates
// through whatever adapter the lens is running on, and an unscoped NATS-KV
// truncate purges the whole bucket — for a lens sharing capability-kv with the
// platform's core authorization surfaces, a platform-wide grant wipe rather
// than a rebuild. buildAdapter is the single point every adapter passes
// through, activation and INTO-hot-reload replacement alike, so the binding is
// pinned here rather than at either call site.
//
// projection.ApplyTruncateScope's own semantics (which keys the prefix covers,
// and that a sibling producer's rows survive a real purge) are pinned in
// internal/refractor/projection.
func TestBuildAdapter_SharedBucketRule_BindsTheTruncateScope(t *testing.T) {
	r := authPlaneRule(t)
	r.Output.OutputKeyPattern = "cap.svc.{actorSuffix}"

	adpt, err := buildAdapter(r, func(*lens.Rule) (adapter.Adapter, error) {
		return newKVAdapter(t), nil
	})
	require.NoError(t, err)

	kvAdapter, ok := adpt.(*adapter.NatsKVAdapter)
	require.True(t, ok)
	assert.Equal(t, "cap.svc.", kvAdapter.KeyPrefix(),
		"a lens sharing its bucket must truncate only the keys it owns")
}

func TestBuildAdapter_TargetError_Propagates(t *testing.T) {
	sentinel := errors.New("target unavailable")
	_, err := buildAdapter(authPlaneRule(t), func(*lens.Rule) (adapter.Adapter, error) {
		return nil, sentinel
	})
	assert.ErrorIs(t, err, sentinel)
}

// unguardableAdapter satisfies adapter.Adapter but is deliberately not a
// *adapter.NatsKVAdapter, so the guard cannot be enabled on it.
type unguardableAdapter struct{}

func (unguardableAdapter) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	return nil
}
func (unguardableAdapter) Delete(context.Context, map[string]any, uint64) error { return nil }
func (unguardableAdapter) Probe(context.Context) error                          { return nil }
func (unguardableAdapter) Close() error                                         { return nil }

// ---------------------------------------------------------------------------
// hotReloadRefusal — every change an adapter swap cannot carry
// ---------------------------------------------------------------------------

// runningEntry is the baseline a lens was activated with: a guarded NATS-KV
// auth-plane lens.
func runningEntry() *pipelineEntry {
	return &pipelineEntry{
		guarded: true,
		target:  "nats_kv",
		bucket:  "capability-kv",
		// The capability bucket IS the auth plane, so this is what
		// newPipelineEntry records for such a lens; an entry saying otherwise
		// would be a shape activation never produces.
		authPlane: true,
		output: &lens.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "cap.{actorSuffix}",
			BodyColumns:      []string{"tasks"},
			EmptyBehavior:    "delete",
			Freshness:        "auto",
		},
	}
}

// runningGrantEntry is a grant lens: Postgres, sharing actor_read_grants,
// confined to its own rows by grant_source. Its adapter's guard is structural,
// so `guarded` is true even though it is not an actor-aggregate.
func runningGrantEntry() *pipelineEntry {
	return &pipelineEntry{
		guarded:     true,
		target:      "postgres",
		table:       "actor_read_grants",
		grantSource: "loftspace.residence",
		grantTable:  true,
		// A grant table is the auth plane's other arm, and newPipelineEntry
		// records it as such.
		authPlane: true,
	}
}

func grantRule() *lens.Rule {
	return &lens.Rule{
		ID: "lens-reload-test",
		Into: lens.IntoConfig{
			Target:      "postgres",
			Table:       "actor_read_grants",
			GrantSource: "loftspace.residence",
			GrantTable:  true,
		},
	}
}

func TestHotReloadRefusal_AcceptsAnIntoOnlyEdit(t *testing.T) {
	entry := runningEntry()
	newLens := authPlaneRule(t)
	newLens.Into.Key = lens.KeyField{"key", "actorKey"}
	assert.Empty(t, hotReloadRefusal(entry, newLens),
		"an INTO edit the swap CAN carry must be accepted")
}

func TestHotReloadRefusal_SecureColumnsChange(t *testing.T) {
	entry := runningEntry()
	newLens := authPlaneRule(t)
	newLens.Into.SecureColumns = []lens.SecureColumn{{Column: "ssn", HolderTypes: []string{"identity"}}}
	assert.Contains(t, hotReloadRefusal(entry, newLens), "secureColumns")
}

// A live decryptor is fixed at activation, so a holder-type edit is exactly as
// unswappable as a column edit: widening the list changes which ciphertexts the
// running decrypt set will open, and a swap would leave the old, narrower set
// deciding. Same column, same field, different holder types must still refuse.
func TestHotReloadRefusal_HolderTypesAloneStillRefuses(t *testing.T) {
	entry := secureEntry()
	newLens := secureRule()
	newLens.Into.SecureColumns = []lens.SecureColumn{
		{Column: "ssn", HolderTypes: []string{"identity", "retentionclass"}},
	}
	assert.Contains(t, hotReloadRefusal(entry, newLens), "secureColumns")
}

// The comparison is order-sensitive because the declaration is authored, not
// computed — a reordered list is a spec edit, and over-refusing a reload costs
// a reactivation while under-refusing one leaves a stale decrypt set live.
func TestHotReloadRefusal_ReorderedHolderTypesRefuses(t *testing.T) {
	entry := &pipelineEntry{
		guarded: true, target: "postgres", table: "patients", dsn: "postgres://one",
		secureColumns: []lens.SecureColumn{{Column: "ssn", HolderTypes: []string{"identity", "retentionclass"}}},
	}
	newLens := secureRule()
	newLens.Into.SecureColumns = []lens.SecureColumn{
		{Column: "ssn", HolderTypes: []string{"retentionclass", "identity"}},
	}
	assert.Contains(t, hotReloadRefusal(entry, newLens), "secureColumns")
}

// An identical declaration is not a secureColumns change, so the refusal must
// come from elsewhere or not at all — otherwise every reload of a secure lens
// would be refused for the wrong reason.
func TestHotReloadRefusal_IdenticalHolderTypesIsNotASecureColumnsChange(t *testing.T) {
	assert.NotContains(t, hotReloadRefusal(secureEntry(), secureRule()), "secureColumns")
}

func secureEntry() *pipelineEntry {
	cols := []lens.SecureColumn{{Column: "ssn", HolderTypes: []string{"identity"}}}
	return &pipelineEntry{
		guarded:       true,
		target:        "postgres",
		table:         "patients",
		dsn:           "postgres://one",
		secureColumns: cols,
	}
}

func secureRule() *lens.Rule {
	return &lens.Rule{
		ID: "lens-reload-test",
		Into: lens.IntoConfig{
			Target:        "postgres",
			Table:         "patients",
			DSN:           "postgres://one",
			SecureColumns: []lens.SecureColumn{{Column: "ssn", HolderTypes: []string{"identity"}}},
		},
	}
}

func TestHotReloadRefusal_SecureLensTargetChange(t *testing.T) {
	newLens := secureRule()
	newLens.Into.Table = "patients_v2"
	assert.Contains(t, hotReloadRefusal(secureEntry(), newLens), "table/dsn")
}

// A secure lens's DSN is the one surface field with no other pin behind it: the
// guarded-surface check does not read it, so if this compared against anything
// but the RUNNING pipeline's value, a refused DSN edit followed by any second
// edit would ride in — and the swap has no verify-and-pause, so the new
// database's RLS posture would go unprobed while the rows carry decrypted PII.
func TestHotReloadRefusal_SecureLensDSNChange(t *testing.T) {
	newLens := secureRule()
	newLens.Into.DSN = "postgres://two"
	assert.Contains(t, hotReloadRefusal(secureEntry(), newLens), "table/dsn")
}

// The refused edit must not become the baseline for the next one. The source
// records every revision it sees, applied or not, so only the entry can answer
// "what is this pipeline actually running".
func TestHotReloadRefusal_ARefusedEditIsNotTheNextBaseline(t *testing.T) {
	entry := secureEntry()

	first := secureRule()
	first.Into.DSN = "postgres://two"
	require.NotEmpty(t, hotReloadRefusal(entry, first), "precondition: the DSN move is refused")

	// A second edit carrying the refused DSN plus an unrelated change.
	second := secureRule()
	second.Into.DSN = "postgres://two"
	second.Into.QueryTimeout = 5
	assert.Contains(t, hotReloadRefusal(entry, second), "table/dsn",
		"the DSN the pipeline never adopted must still be refused")
}

// An Output edit reaches the INTO-only path with the Match clause untouched —
// `output` is its own aspect. The swap rebuilds the adapter and re-runs none of
// the envelope / delete-key / sweep-plan installation, so it must be refused.
func TestHotReloadRefusal_OutputDescriptorChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*lens.OutputDescriptorSpec)
	}{
		{"emptyBehavior", func(o *lens.OutputDescriptorSpec) { o.EmptyBehavior = "softDelete" }},
		{"anchorType", func(o *lens.OutputDescriptorSpec) { o.AnchorType = "provider" }},
		{"outputKeyPattern", func(o *lens.OutputDescriptorSpec) { o.OutputKeyPattern = "cap.other.{actorSuffix}" }},
		{"bodyColumns", func(o *lens.OutputDescriptorSpec) { o.BodyColumns = []string{"tasks", "grants"} }},
		{"realnessFilter", func(o *lens.OutputDescriptorSpec) { o.RealnessFilter = "taskKey" }},
		{"keyColumn", func(o *lens.OutputDescriptorSpec) { o.KeyColumn = "entityId" }},
		{"entryKeyColumn", func(o *lens.OutputDescriptorSpec) { o.BodyColumns = []string{"tasks"}; o.EntryKeyColumn = "anchorId" }},
		{"actorField", func(o *lens.OutputDescriptorSpec) { o.ActorField = "assignee" }},
		{"lanes", func(o *lens.OutputDescriptorSpec) { o.Lanes = []string{"write"} }},
		{"staticEmptyColumns", func(o *lens.OutputDescriptorSpec) { o.StaticEmptyColumns = []string{"ephemeralGrants"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := runningEntry()
			newLens := authPlaneRule(t)
			tc.mutate(newLens.Output)
			assert.Contains(t, hotReloadRefusal(entry, newLens), "Output descriptor")
		})
	}
}

func TestHotReloadRefusal_OutputDropped(t *testing.T) {
	entry := runningEntry()
	newLens := authPlaneRule(t)
	newLens.Output = nil
	assert.Contains(t, hotReloadRefusal(entry, newLens), "Output descriptor")
}

// The row this closes: flipping a lens OFF the shared grant table strands every
// row it wrote — no producer addresses that grant_source afterwards, so diff
// retraction can never revoke them, and they stay live in the table every
// protected read consults.
func TestHotReloadRefusal_GrantTableFlippedOff(t *testing.T) {
	entry := runningGrantEntry()
	newLens := grantRule()
	newLens.Into.GrantTable = false
	newLens.Into.Table = "residence_read_model"
	assert.Contains(t, hotReloadRefusal(entry, newLens), "grantTable")
}

func TestHotReloadRefusal_GrantTableFlippedOn(t *testing.T) {
	entry := &pipelineEntry{guarded: true, target: "postgres", table: "residence_read_model"}
	newLens := grantRule()
	assert.Contains(t, hotReloadRefusal(entry, newLens), "grantTable")
}

// grant_source is part of a grant lens's write surface: it is what confines the
// lens's writes and its retraction enumeration to its own rows in a table it
// shares with five other producers.
func TestHotReloadRefusal_GrantSourceChange(t *testing.T) {
	entry := runningGrantEntry()
	newLens := grantRule()
	newLens.Into.GrantSource = "loftspace.tenancy"
	assert.Contains(t, hotReloadRefusal(entry, newLens), "write surface")
}

// bucket is empty for every Postgres target, so a pin that reads only
// target+bucket is vacuous for exactly the grant family.
func TestHotReloadRefusal_GuardedPostgresTableChange(t *testing.T) {
	entry := runningGrantEntry()
	newLens := grantRule()
	newLens.Into.Table = "actor_read_grants_v2"
	assert.Contains(t, hotReloadRefusal(entry, newLens), "write surface")
}

func TestHotReloadRefusal_GuardedBucketChange(t *testing.T) {
	entry := runningEntry()
	newLens := authPlaneRule(t)
	newLens.Into.Bucket = "weaver-targets"
	assert.Contains(t, hotReloadRefusal(entry, newLens), "write surface")
}

func TestHotReloadRefusal_UnguardedLensMayMoveItsTarget(t *testing.T) {
	entry := runningEntry()
	entry.guarded = false
	// An ordinary business bucket, on both sides: this entry is the shape
	// activation records for a weaver-targets lens, plane included, and the
	// move below stays inside that plane.
	entry.bucket = "weaver-targets"
	entry.authPlane = false
	newLens := authPlaneRule(t)
	newLens.Into.Bucket = "other-targets"
	newLens.Output = entry.output
	assert.Empty(t, hotReloadRefusal(entry, newLens),
		"an unguarded lens strands nothing it cannot re-derive")
}

// The move the pin above lets through by design is the one that changes the
// lens's PLANE: an unguarded business lens edited onto the capability bucket
// keeps three activation-time records — the pipeline's own authPlane, this
// entry's (which picks the heartbeat's severity tier), and the auditor's
// captured copy — all reading "business read model" for what is now an
// authorization surface.
func TestHotReloadRefusal_UnguardedLensMayNotMoveOntoTheAuthPlane(t *testing.T) {
	entry := runningEntry()
	entry.guarded = false
	entry.bucket = "weaver-targets"
	entry.authPlane = false

	newLens := authPlaneRule(t)
	newLens.Into.Bucket = projection.AuthPlaneBucket
	newLens.Output = entry.output

	assert.Contains(t, hotReloadRefusal(entry, newLens), "authorization plane")
}

// And the reverse: moving OFF the plane strands every capability row the lens
// wrote, since no producer addresses them afterwards — the same argument the
// grantTable pin makes for its own family.
func TestHotReloadRefusal_UnguardedLensMayNotMoveOffTheAuthPlane(t *testing.T) {
	entry := runningEntry()
	entry.guarded = false

	newLens := authPlaneRule(t)
	newLens.Into.Bucket = "weaver-targets"
	newLens.Output = entry.output

	assert.Contains(t, hotReloadRefusal(entry, newLens), "authorization plane")
}

// The entry's own record is what the pin reads, and newPipelineEntry is what
// writes it — so the two must agree, or the pin compares against a plane the
// lens was never activated with.
func TestNewPipelineEntry_RecordsThePlaneThePinReads(t *testing.T) {
	onPlane := newPipelineEntry(grantRule(), unguardableAdapter{}, nil, nil, nil, nil, nil)
	assert.True(t, onPlane.authPlane, "a grant table is the auth plane")

	business := grantRule()
	business.Into.GrantTable = false
	business.Into.Table = "residence_read_model"
	offPlane := newPipelineEntry(business, unguardableAdapter{}, nil, nil, nil, nil, nil)
	assert.False(t, offPlane.authPlane)

	assert.Contains(t, hotReloadRefusal(onPlane, business), "grantTable",
		"the grantTable pin answers this family first; the plane pin is what covers the nats_kv arm")
}

// The baseline is the RUNNING pipeline's activated value, never the last-seen
// spec: a refused update must not move it, or a second edit compares against
// something that was never live.
func TestHotReloadRefusal_ComparesAgainstTheRunningEntryNotTheOldSpec(t *testing.T) {
	entry := runningEntry()
	// `old` already carries the refused edit; the entry still carries what is live.
	old := authPlaneRule(t)
	old.Into.Bucket = "weaver-targets"
	newLens := authPlaneRule(t)
	newLens.Into.Bucket = "weaver-targets"
	assert.Contains(t, hotReloadRefusal(entry, newLens), "write surface",
		"a refused update must not become the baseline for the next one")
}

// ---------------------------------------------------------------------------
// reloader.update — a refused reload is visible where an operator looks
// ---------------------------------------------------------------------------

func startHealthKV(t *testing.T) *substrate.KV {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "HEALTH"})
	require.NoError(t, err)
	kv, err := conn.OpenKV(context.Background(), "HEALTH")
	require.NoError(t, err)
	return kv
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestReloaderUpdate_RefusalRecordsAnErrorOnHealthAndDoesNotBuild(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	p, err := pipeline.New("lens-reload-test", "nats_kv", "CORE", nil, nil, newKVAdapter(t), nil)
	require.NoError(t, err)

	entry := runningEntry()
	entry.reporter = reporter
	entry.pipeline = p

	built := 0
	rl := &reloader{
		ctx:    context.Background(),
		logger: discardLogger(),
		lookup: func(string) (*pipelineEntry, bool) { return entry, true },
		buildAdapter: func(*lens.Rule) (adapter.Adapter, error) {
			built++
			return newKVAdapter(t), nil
		},
		fullEngine: full.New(),
	}

	newLens := authPlaneRule(t)
	newLens.Output.EmptyBehavior = "softDelete"
	rl.update(authPlaneRule(t), newLens, lens.IntoOnly)

	assert.Zero(t, built,
		"a refused reload must decide before opening its target — building leaves an auto-created bucket behind on every redelivery")

	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), status.ErrorCount, "a refused reload must not leave health unbroken")
	require.NotNil(t, status.LastError)
	assert.Contains(t, *status.LastError, "Output descriptor",
		"the operator is told the same reason in health as in the log")
}

func TestReloaderUpdate_AcceptedReloadSwapsTheAdapterAndReportsNoError(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	p, err := pipeline.New("lens-reload-test", "nats_kv", "CORE", nil, nil, newKVAdapter(t), nil)
	require.NoError(t, err)

	entry := runningEntry()
	entry.reporter = reporter
	entry.pipeline = p

	built := 0
	rl := &reloader{
		ctx:    context.Background(),
		logger: discardLogger(),
		lookup: func(string) (*pipelineEntry, bool) { return entry, true },
		buildAdapter: func(r *lens.Rule) (adapter.Adapter, error) {
			built++
			return buildAdapter(r, func(*lens.Rule) (adapter.Adapter, error) { return newKVAdapter(t), nil })
		},
		fullEngine: full.New(),
	}

	newLens := authPlaneRule(t)
	newLens.Sequence = 42
	rl.update(authPlaneRule(t), newLens, lens.IntoOnly)

	assert.Equal(t, 1, built, "an accepted reload builds the replacement adapter")
	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Zero(t, status.ErrorCount)
	assert.Equal(t, uint64(42), reporter.ActiveSequence(),
		"an accepted reload advances the reported rule sequence")
}

func TestReloaderUpdate_UnknownLensIsIgnored(t *testing.T) {
	rl := &reloader{
		ctx:    context.Background(),
		logger: discardLogger(),
		lookup: func(string) (*pipelineEntry, bool) { return nil, false },
		buildAdapter: func(*lens.Rule) (adapter.Adapter, error) {
			t.Fatal("must not build an adapter for an unregistered lens")
			return nil, nil
		},
		fullEngine: full.New(),
	}
	rl.update(authPlaneRule(t), authPlaneRule(t), lens.IntoOnly)
}

// TestReloaderUpdate_UnknownButRefusedLens_ReplacesQueuedRule covers A2: an
// "unknown lens" update is not always a genuinely-unregistered one — it can
// also be an operator's spec edit landing while the lens is refused and
// queued in rl.refused pending a taxonomy event. That edit must replace the
// queued rule, not be dropped in favor of the pre-edit one the eventual
// retry would otherwise activate — this file's header states the principle
// for the opposite direction ("a refused edit must not become the baseline
// for the next one"); this is that same principle applied to the queue.
func TestReloaderUpdate_UnknownButRefusedLens_ReplacesQueuedRule(t *testing.T) {
	rl := &reloader{
		ctx:    context.Background(),
		logger: discardLogger(),
		lookup: func(string) (*pipelineEntry, bool) { return nil, false },
	}
	original := authPlaneRule(t)
	rl.recordRefusedForTaxonomy(original)

	edited := authPlaneRule(t)
	edited.Sequence = 99

	rl.update(original, edited, lens.MatchChange)

	rl.refusedMu.Lock()
	queued := rl.refused[edited.ID]
	rl.refusedMu.Unlock()
	require.NotNil(t, queued)
	assert.Same(t, edited, queued, "the queued rule must be replaced by the edit, not left as the pre-edit rule")
}

func TestReloaderUpdate_MatchChangeSecureColumnsRefusalIsRecorded(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	entry := runningEntry()
	entry.reporter = reporter

	rl := &reloader{
		ctx:        context.Background(),
		logger:     discardLogger(),
		lookup:     func(string) (*pipelineEntry, bool) { return entry, true },
		fullEngine: full.New(),
	}

	newLens := authPlaneRule(t)
	newLens.Into.SecureColumns = []lens.SecureColumn{{Column: "ssn", HolderTypes: []string{"identity"}}}
	rl.update(authPlaneRule(t), newLens, lens.MatchChange)

	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), status.ErrorCount)
	require.NotNil(t, status.LastError)
	assert.Contains(t, *status.LastError, "secureColumns")
}

// ---------------------------------------------------------------------------
// newPipelineEntry — the activated baseline
// ---------------------------------------------------------------------------

// structurallyGuardedAdapter mirrors the grant writer: its guard is a property
// of the SQL it always issues, so it reports guarded with no flag to set.
type structurallyGuardedAdapter struct{ unguardableAdapter }

func (structurallyGuardedAdapter) Guarded() bool { return true }

// A grant lens is not an actor-aggregate, so the rule-level predicate answers
// "unguarded" for it — while its adapter's guard is unconditional. The entry
// must follow the adapter, or the surface pin stays disarmed for the one family
// that shares a table.
func TestNewPipelineEntry_GuardedFollowsTheAdapterNotTheRulePredicate(t *testing.T) {
	r := grantRule()
	requiresGuard, err := projection.RequiresGuard(r)
	require.NoError(t, err)
	require.False(t, requiresGuard, "precondition: the rule predicate does not see the grant family")

	entry := newPipelineEntry(r, structurallyGuardedAdapter{}, nil, nil, nil, nil, nil)
	assert.True(t, entry.guarded, "the activated adapter's guard is what the entry records")

	// And the pin it arms refuses the edit that would strand the lens's rows.
	moved := grantRule()
	moved.Into.GrantSource = "loftspace.tenancy"
	assert.Contains(t, hotReloadRefusal(entry, moved), "write surface")
}

func TestNewPipelineEntry_SnapshotsTheWholeSurface(t *testing.T) {
	entry := newPipelineEntry(grantRule(), unguardableAdapter{}, nil, nil, nil, nil, nil)
	assert.False(t, entry.guarded)
	assert.Equal(t, "postgres", entry.target)
	assert.Equal(t, "actor_read_grants", entry.table)
	assert.Equal(t, "loftspace.residence", entry.grantSource)
	assert.True(t, entry.grantTable)
}

// A reorder of columns the envelope writes into a map is not a change. Refusing
// it would wedge a lens on a cosmetic re-authoring, with delete-and-re-create
// the only way out.
func TestOutputDescriptorsEqual_BodyColumnOrderIsNotContent(t *testing.T) {
	entry := runningEntry()
	entry.output.BodyColumns = []string{"tasks", "grants"}
	newLens := authPlaneRule(t)
	newLens.Output.BodyColumns = []string{"grants", "tasks"}
	assert.Empty(t, hotReloadRefusal(entry, newLens))
}

// Lanes IS emitted verbatim as the document's `lanes` array, so its order is
// content and a reorder is a real edit.
func TestOutputDescriptorsEqual_LaneOrderIsContent(t *testing.T) {
	entry := runningEntry()
	entry.output.Lanes = []string{"default", "urgent"}
	newLens := authPlaneRule(t)
	newLens.Output.Lanes = []string{"urgent", "default"}
	assert.Contains(t, hotReloadRefusal(entry, newLens), "Output descriptor")
}

func TestOutputDescriptorsEqual_ColumnSetStillComparesMultiplicity(t *testing.T) {
	assert.False(t, sameColumnSet([]string{"a", "a", "b"}, []string{"a", "b", "b"}))
	assert.True(t, sameColumnSet([]string{"a", "a", "b"}, []string{"b", "a", "a"}))
	assert.True(t, sameColumnSet(nil, []string{}))
}

// An unguarded lens strands nothing it cannot re-derive, so the widened pin
// must stay off for it — the whole pin, not just the fields it used to cover.
func TestHotReloadRefusal_UnguardedLensMayMoveEveryPinnedField(t *testing.T) {
	entry := runningGrantEntry()
	entry.guarded = false
	newLens := grantRule()
	newLens.Into.Table = "somewhere_else"
	newLens.Into.GrantSource = "another.source"
	assert.Empty(t, hotReloadRefusal(entry, newLens))
}

// A MATCH change re-installs the envelope exactly as little as an INTO-only one
// does, so it cannot be the way around the pins. Without this, an operator
// refused on an Output edit can apply it by touching the cypher in the same
// version.
func TestReloaderUpdate_MatchChangeCannotSmuggleAnOutputEdit(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	p, err := pipeline.New("lens-reload-test", "nats_kv", "CORE", nil, nil, newKVAdapter(t), nil)
	require.NoError(t, err)

	entry := runningEntry()
	entry.reporter = reporter
	entry.pipeline = p

	rl := &reloader{
		ctx:        context.Background(),
		logger:     discardLogger(),
		lookup:     func(string) (*pipelineEntry, bool) { return entry, true },
		fullEngine: full.New(),
	}

	newLens := authPlaneRule(t)
	newLens.Output.EmptyBehavior = "softDelete"
	newLens.Sequence = 99
	rl.update(authPlaneRule(t), newLens, lens.MatchChange)

	assert.NotEqual(t, uint64(99), reporter.ActiveSequence(),
		"a refused MATCH update must not swap the compiled rule")
	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status.LastError)
	assert.Contains(t, *status.LastError, "Output descriptor")
}

// The same bypass on the identity pin: a grant lens flipped off the shared
// table in the same version as a cypher edit.
func TestReloaderUpdate_MatchChangeCannotSmuggleAGrantTableFlip(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	entry := runningGrantEntry()
	entry.reporter = reporter

	rl := &reloader{
		ctx:        context.Background(),
		logger:     discardLogger(),
		lookup:     func(string) (*pipelineEntry, bool) { return entry, true },
		fullEngine: full.New(),
	}

	newLens := grantRule()
	newLens.Into.GrantTable = false
	rl.update(grantRule(), newLens, lens.MatchChange)

	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status.LastError)
	assert.Contains(t, *status.LastError, "grantTable")
}

// ---------------------------------------------------------------------------
// The guard sources, all four of them
// ---------------------------------------------------------------------------

// protectedEntry is an RLS-locked business read model: Postgres, guarded
// because NewProtectedAdapter forces the guard on its inner adapter, and NOT an
// actor-aggregate — so nothing ever calls RequireGuardedAdapter for it and
// HotReloadInto has no requirement to refuse against.
func protectedEntry() *pipelineEntry {
	return &pipelineEntry{
		guarded:   true,
		target:    "postgres",
		table:     "read_landlord_units",
		dsn:       "postgres://one",
		protected: true,
	}
}

func protectedRule() *lens.Rule {
	return &lens.Rule{
		ID: "lens-reload-test",
		Into: lens.IntoConfig{
			Target:    "postgres",
			Table:     "read_landlord_units",
			DSN:       "postgres://one",
			Protected: true,
		},
	}
}

// Retiring `protected` swaps the ProtectedAdapter for a bare PostgresAdapter,
// which drops the monotonic projection_seq predicate — reopening the stale-
// replay resurrection window on the very read model that carries read-path
// authorization. No pipeline-level requirement is armed for this family, so
// this refusal is the only thing standing there.
func TestHotReloadRefusal_ProtectedRetired(t *testing.T) {
	newLens := protectedRule()
	newLens.Into.Protected = false
	newLens.Into.Public = true
	assert.Contains(t, hotReloadRefusal(protectedEntry(), newLens), "protected")
}

func TestHotReloadRefusal_ProtectedAdopted(t *testing.T) {
	entry := protectedEntry()
	entry.protected = false
	entry.guarded = false
	assert.Contains(t, hotReloadRefusal(entry, protectedRule()), "protected")
}

// dsn is part of a guarded lens's surface even when the lens is not a secure
// one — the rows it wrote live in that database and nothing else addresses them.
func TestHotReloadRefusal_GuardedDSNChange(t *testing.T) {
	newLens := protectedRule()
	newLens.Into.DSN = "postgres://two"
	assert.Contains(t, hotReloadRefusal(protectedEntry(), newLens), "write surface")
}

// Every source of the §6.2 guard must be pinned by something, or a lens can be
// edited from guarded to unguarded while it runs. The four sources are the
// auth-plane bucket, the Output tombstone empty-behavior, grantTable, and
// protected.
func TestHotReloadRefusal_NoGuardSourceIsUnpinned(t *testing.T) {
	t.Run("auth-plane bucket", func(t *testing.T) {
		newLens := authPlaneRule(t)
		newLens.Into.Bucket = "weaver-targets"
		assert.NotEmpty(t, hotReloadRefusal(runningEntry(), newLens))
	})
	t.Run("tombstone empty behavior", func(t *testing.T) {
		newLens := authPlaneRule(t)
		newLens.Output.EmptyBehavior = "emptyDoc"
		assert.NotEmpty(t, hotReloadRefusal(runningEntry(), newLens))
	})
	t.Run("grantTable", func(t *testing.T) {
		newLens := grantRule()
		newLens.Into.GrantTable = false
		assert.NotEmpty(t, hotReloadRefusal(runningGrantEntry(), newLens))
	})
	t.Run("protected", func(t *testing.T) {
		newLens := protectedRule()
		newLens.Into.Protected = false
		assert.NotEmpty(t, hotReloadRefusal(protectedEntry(), newLens))
	})
}

// Spelling out a default that parsing would have supplied anyway is not an edit.
func TestOutputDescriptorsEqual_ActorFieldDefaultIsNotAnEdit(t *testing.T) {
	entry := runningEntry()
	entry.output.ActorField = ""
	newLens := authPlaneRule(t)
	newLens.Output.ActorField = "actor"
	assert.Empty(t, hotReloadRefusal(entry, newLens))

	newLens.Output.ActorField = "assignee"
	assert.Contains(t, hotReloadRefusal(entry, newLens), "Output descriptor")
}

// A refusal must name a remedy an operator can actually carry out. A
// package-installed lens is re-authored by an upgrade, not by hand, so
// "delete and re-create" alone is not reachable advice.
func TestHotReloadRefusal_NamesAReachableRemedy(t *testing.T) {
	newLens := authPlaneRule(t)
	newLens.Output.EmptyBehavior = "softDelete"
	assert.Contains(t, hotReloadRefusal(runningEntry(), newLens), "restart Refractor")
}

// ---------------------------------------------------------------------------
// reloadpin drift guard
// ---------------------------------------------------------------------------

// TestPinnedFieldsMatchTheRefusalSet is what keeps pkgmgr's apply-time warning
// honest. pkgmgr cannot import internal/refractor/lens, so reloadpin restates
// the spec-derived half of hotReloadRefusal over the stored document — and a
// restatement that drifts stops warning about an edit that is still refused,
// which is the exact silence the warning exists to break.
//
// This asserts the direction that matters: everything reloadpin predicts, this
// package really does refuse. The reverse is deliberately not asserted — the
// write-surface pins are runtime-conditional and reloadpin correctly declines to
// predict them.
func TestPinnedFieldsMatchTheRefusalSet(t *testing.T) {
	edits := map[string]func(*lens.Rule){
		"output":                  func(r *lens.Rule) { r.Output.BodyColumns = append(r.Output.BodyColumns, "extra") },
		"targetConfig.grantTable": func(r *lens.Rule) { r.Into.GrantTable = !r.Into.GrantTable },
		"targetConfig.protected":  func(r *lens.Rule) { r.Into.Protected = !r.Into.Protected },
		"targetConfig.secureColumns": func(r *lens.Rule) {
			r.Into.SecureColumns = []lens.SecureColumn{{Column: "ssn", HolderTypes: []string{"identity"}}}
		},
	}
	for _, f := range reloadpin.PinnedFields {
		name := strings.Join(f.Path, ".")
		edit, known := edits[name]
		if !known {
			t.Fatalf("reloadpin pins %q with no matching refusal check here — either this package must refuse it too, or it does not belong in PinnedFields", name)
		}
		newLens := authPlaneRule(t)
		edit(newLens)
		assert.NotEmpty(t, hotReloadRefusal(runningEntry(), newLens),
			"reloadpin warns that %q is not hot-reloadable, so this package must actually refuse it", name)
	}
}

// ---------------------------------------------------------------------------
// MatchChange — an accepted edit reprojects the existing corpus, not just
// future events (Component maintenance: "a lens spec change re-compiles but
// never re-projects").
// ---------------------------------------------------------------------------

// matchChangeEnv wires a real running pipeline against embedded NATS — Core
// KV, a target NATS-KV bucket and a health bucket — so an accepted MATCH
// hot-reload can be proven to actually rescan the pre-existing corpus, not
// just log that it swapped the compiled rule.
type matchChangeEnv struct {
	conn   *substrate.Conn
	adjKV  *substrate.KV
	coreKV *substrate.KV
	target *substrate.KV
}

func startMatchChangeEnv(t *testing.T) *matchChangeEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()

	for _, bucket := range []string{"CORE", "ADJ", "TARGET-mc", "HEALTH-lens-mc"} {
		_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
		require.NoError(t, err)
	}

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "ADJ")
	require.NoError(t, err)
	coreKV, err := conn.OpenKV(ctx, "CORE")
	require.NoError(t, err)
	target, err := conn.OpenKV(ctx, "TARGET-mc")
	require.NoError(t, err)

	return &matchChangeEnv{conn: conn, adjKV: adjKV, coreKV: coreKV, target: target}
}

// mcCompile parses query against a fresh full engine and threads keyFields
// onto the compiled rule, mirroring internal/refractor/pipeline's
// compileFullRule (unexported to that package's own test, so restated here).
func mcCompile(t *testing.T, eng *full.Engine, query string, keyFields []string) *full.CompiledRule {
	t.Helper()
	cr, err := eng.Parse(query)
	require.NoError(t, err)
	fullCR, ok := cr.(*full.CompiledRule)
	require.True(t, ok)
	fullCR.KeyColumns = keyFields
	require.NoError(t, fullCR.ValidateKeyColumns())
	return fullCR
}

// mcPollUntil retries check every 20ms until it returns true or timeout
// expires — the package's one deterministic wait (CLAUDE.md: polling with a
// condition, never a fixed sleep standing in for synchronisation). msg, when
// given, says what the caller was waiting for.
func mcPollUntil(t *testing.T, timeout time.Duration, check func() bool, msg ...string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(msg) > 0 {
		t.Fatalf("%s (within %s)", msg[0], timeout)
	}
	t.Fatal("condition not met within timeout")
}

func TestReloaderUpdate_AcceptedMatchChangeReprojectsExistingEntries(t *testing.T) {
	env := startMatchChangeEnv(t)
	ctx := context.Background()

	fullEngine := full.New()
	oldCR := mcCompile(t, fullEngine,
		"MATCH (a:agreement {key: $actorKey}) RETURN a.stableId AS agreementId, a.id AS marker",
		[]string{"agreementId"})

	adpt, err := adapter.New(env.target, []string{"agreementId"}, adapter.DeleteModeHard)
	require.NoError(t, err)

	healthKV, err := env.conn.OpenKV(ctx, "HEALTH-lens-mc")
	require.NoError(t, err)
	reporter := health.New(healthKV, "lens-mc")

	p, err := pipeline.New("lens-mc", "nats_kv", "CORE", env.adjKV, env.coreKV, adpt, reporter)
	require.NoError(t, err)
	p.UseFullEngine(fullEngine, oldCR)

	p.RunOn(env.conn, substrate.ConsumerSpec{
		Name:          "refractor-lens-mc",
		Stream:        subjects.CoreKVStream("CORE"),
		FilterSubject: subjects.CoreKVFilter("CORE"),
		DeliverPolicy: substrate.DeliverLastPerSubject,
		DeliverGroup:  "refractor-lens-mc",
		AckWait:       2 * time.Second,
	})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Seed one agreement — the corpus a hot-reload must reach, not just
	// events arriving after it.
	putProps := map[string]any{
		"stableId":       "STABLE1",
		"id":             "OLD-VALUE",
		"altId":          "NEW-VALUE",
		"isDeleted":      false,
		"createdAt":      "2026-01-01T00:00:00Z",
		"lastModifiedAt": "2026-01-01T00:00:00Z",
	}
	body, err := json.Marshal(putProps)
	require.NoError(t, err)
	_, err = env.coreKV.Put(ctx, "vtx.agreement.Tsnt1McAgreementAaaa", body)
	require.NoError(t, err)

	// The old rule projects the pre-existing entry under its OLD marker.
	mcPollUntil(t, 3*time.Second, func() bool {
		entry, err := env.target.Get(ctx, "STABLE1")
		if err != nil {
			return false
		}
		var got map[string]any
		if json.Unmarshal(entry.Value, &got) != nil {
			return false
		}
		return got["marker"] == "OLD-VALUE"
	})

	// A MATCH-only edit: same key, different source column for `marker`.
	newCR := mcCompile(t, fullEngine,
		"MATCH (a:agreement {key: $actorKey}) RETURN a.stableId AS agreementId, a.altId AS marker",
		[]string{"agreementId"})
	newLens := &lens.Rule{
		ID:             "lens-mc",
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   newCR,
		Sequence:       7,
		Into:           lens.IntoConfig{Target: "nats_kv", Key: lens.KeyField{"agreementId"}},
	}

	entry := &pipelineEntry{
		target:   "nats_kv",
		pipeline: p,
		reporter: reporter,
	}
	rl := &reloader{
		ctx:        ctx,
		logger:     discardLogger(),
		lookup:     func(string) (*pipelineEntry, bool) { return entry, true },
		fullEngine: fullEngine,
	}

	oldLens := &lens.Rule{ID: "lens-mc", Into: lens.IntoConfig{Target: "nats_kv", Key: lens.KeyField{"agreementId"}}}
	rl.update(oldLens, newLens, lens.MatchChange)

	status, err := reporter.GetStatus(ctx)
	require.NoError(t, err)
	assert.Zero(t, status.ErrorCount, "an accepted MATCH change must not record a refusal")

	// The already-stored entry must reproject under the NEW rule without any
	// new Core KV event — proving the accepted swap triggered a rescan of the
	// existing corpus, not just a change in behavior for future events.
	mcPollUntil(t, 3*time.Second, func() bool {
		entry, err := env.target.Get(ctx, "STABLE1")
		if err != nil {
			return false
		}
		var got map[string]any
		if json.Unmarshal(entry.Value, &got) != nil {
			return false
		}
		return got["marker"] == "NEW-VALUE"
	})
}

// TestReloaderUpdate_NarrowingMatchChangeRetractsTheDroppedLabelsRows pins the
// MATCH-edit half of the retraction: an accepted MATCH change that NARROWS the
// admitted label set must retract the rows the old rule projected for the
// labels it dropped, not leave them in the target forever.
//
// The mechanism is identical to a taxonomy shrink's, and so is the harm: from
// the swap onward the consumer filter admits no event for the dropped label, so
// no delivery, no sweep and no anchor tombstone can reach those rows again. The
// existing MATCH-change test asserts a REPROJECTION (a row whose value changes);
// this one asserts a RETRACTION (a row that must cease to exist), which only the
// truncate ahead of the replay produces.
func TestReloaderUpdate_NarrowingMatchChangeRetractsTheDroppedLabelsRows(t *testing.T) {
	env := startMatchChangeEnv(t)
	ctx := context.Background()

	fullEngine := full.New()
	oldCR := mcCompile(t, fullEngine, "MATCH (l:location*) RETURN l.key AS locKey", []string{"locKey"})

	adpt, err := adapter.New(env.target, []string{"locKey"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	// The lens's own truncate scope (projection.ApplyTruncateScope binds this
	// from the declared output prefix): without it the rebuild refuses to
	// truncate at all rather than purge a bucket the lens may share.
	adpt.SetKeyPrefix("vtx.")
	healthKV, err := env.conn.OpenKV(ctx, "HEALTH-lens-mc")
	require.NoError(t, err)
	reporter := health.New(healthKV, "lens-mc-narrow")

	p, err := pipeline.New("lens-mc-narrow", "nats_kv", "CORE", env.adjKV, env.coreKV, adpt, reporter)
	require.NoError(t, err)

	resolver := taxonomy.New()
	installLocationTaxonomy(resolver, "room", "desk")
	p.SetTaxonomyResolver(resolver)
	require.NoError(t, p.UseFullEngineBranches(fullEngine, oldCR, nil))

	p.RunOn(env.conn, substrate.ConsumerSpec{
		Name:          "refractor-lens-mc-narrow",
		Stream:        subjects.CoreKVStream("CORE"),
		FilterSubject: subjects.CoreKVFilter("CORE"),
		DeliverPolicy: substrate.DeliverLastPerSubject,
		DeliverGroup:  "refractor-lens-mc-narrow",
		AckWait:       2 * time.Second,
	})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	mcPollUntil(t, 5*time.Second, func() bool {
		_, err := p.Pending(ctx)
		return err == nil
	})

	const (
		roomKey = "vtx.room.Tsnt1McNarrowRoomAaa"
		deskKey = "vtx.desk.Tsnt1McNarrowDeskAaa"
	)
	for _, key := range []string{roomKey, deskKey} {
		body, err := json.Marshal(map[string]any{
			"isDeleted":      false,
			"createdAt":      "2026-01-01T00:00:00Z",
			"lastModifiedAt": "2026-01-01T00:00:00Z",
		})
		require.NoError(t, err)
		_, err = env.coreKV.Put(ctx, key, body)
		require.NoError(t, err)
	}
	projected := func(key string) bool {
		_, err := env.target.Get(ctx, key)
		return err == nil
	}
	// revisionOf returns the target row's current revision, or 0 when absent.
	// NATS KV revisions are the bucket's stream sequence, so a purge followed
	// by a re-write always lands STRICTLY ABOVE whatever the row carried
	// before — which is what lets the wait below tell a re-derived row from a
	// surviving one.
	revisionOf := func(key string) uint64 {
		entry, err := env.target.Get(ctx, key)
		if err != nil {
			return 0
		}
		return entry.Revision
	}
	mcPollUntil(t, 5*time.Second, func() bool { return projected(roomKey) && projected(deskKey) })
	roomRevisionBeforeEdit := revisionOf(roomKey)
	require.NotZero(t, roomRevisionBeforeEdit, "precondition: the room row is projected before the edit")

	// A row belonging to another producer of the same bucket: the retraction
	// must be confined to this lens's own prefix.
	const siblingKey = "sibling.producer.row"
	_, err = env.target.Put(ctx, siblingKey, []byte(`{"owner":"another lens"}`))
	require.NoError(t, err)

	// The edit: the lens stops covering the whole `location` subtree and
	// projects rooms only. `desk` is dropped from the admitted set.
	newCR := mcCompile(t, fullEngine, "MATCH (l:room) RETURN l.key AS locKey", []string{"locKey"})
	newLens := &lens.Rule{
		ID:             "lens-mc-narrow",
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   newCR,
		Sequence:       9,
		Into:           lens.IntoConfig{Target: "nats_kv", Key: lens.KeyField{"locKey"}},
	}
	oldLens := &lens.Rule{ID: "lens-mc-narrow", Into: lens.IntoConfig{Target: "nats_kv", Key: lens.KeyField{"locKey"}}}

	entry := &pipelineEntry{
		target:   "nats_kv",
		pipeline: p,
		reporter: reporter,
	}
	rl := &reloader{
		ctx:        ctx,
		logger:     discardLogger(),
		lookup:     func(string) (*pipelineEntry, bool) { return entry, true },
		fullEngine: fullEngine,
		resolver:   resolver,
	}

	rl.update(oldLens, newLens, lens.MatchChange)

	filterSubjects, _, _ := p.ConsumerFilter()
	require.NotContains(t, filterSubjects, subjects.CoreKVVertexFilter("CORE", "desk"),
		"precondition: the edit narrowed the admitted set, so nothing will deliver a desk event again")

	// Room survives the truncate via the replay; desk does not come back.
	//
	// The wait is on the room row's REVISION rising above what it carried
	// before the edit, not on its mere presence, and the difference is
	// load-bearing. Truncate purges the lens's rows ONE KEY AT A TIME in list
	// order (adapter.NatsKVAdapter.Truncate), and `vtx.desk.*` sorts ahead of
	// `vtx.room.*`, so mid-truncate there is a real window where desk is
	// already gone and room is still its STALE pre-truncate row. A presence
	// check is satisfied by that window and returns before the replay has
	// re-derived anything — after which the truncate reaches room and every
	// assertion below reads a row that is legitimately, momentarily absent.
	// A revision strictly above the pre-edit one can only come from the
	// replay's own write.
	mcPollUntil(t, 10*time.Second, func() bool {
		return revisionOf(roomKey) > roomRevisionBeforeEdit && !projected(deskKey)
	}, "the surviving label's row must be re-derived by the replay and the dropped label's retracted")
	assert.Greater(t, revisionOf(roomKey), roomRevisionBeforeEdit,
		"the surviving label's row must be re-derived by the replay")
	assert.False(t, projected(deskKey), "the dropped label's row must be retracted — nothing else can ever reach it")
	assert.True(t, projected(siblingKey), "the truncate must be confined to the lens's own key prefix — another producer's row in the same bucket must survive")
	assert.Zero(t, errorCount(t, reporter), "an accepted narrowing MATCH change must not record a refusal")
}

// readGrantProducerRule is the shape whose adapter must carry the D1 read-grant
// namespace licence: an actor-aggregate, per-entry, auth-plane lens whose key
// pattern round-trips — i.e. what projection.IsReadGrantProducer admits.
func readGrantProducerRule(t *testing.T) *lens.Rule {
	t.Helper()
	r := authPlaneRule(t)
	r.Output.OutputKeyPattern = "cap-read.reloadtest.{actorSuffix}"
	r.Output.BodyColumns = []string{"readableAnchors"}
	r.Output.EntryKeyColumn = "anchorId"
	r.Output.RealnessFilter = "anchorId"
	return r
}

// adapterIsReadGrantLicensed reports whether a built adapter carries the D1
// read-grant namespace licence — the same question adapterIsGuarded asks about
// the §6.2 guard, and asked of the ADAPTER for the same reason: what a
// replacement actually acquired is the only thing this path can get wrong, and
// re-deriving it from the rule here would assert the predicate against itself.
//
// What the licence then DOES to a write (refuse, terminally, fail-closed) is
// pinned in internal/refractor/adapter's own tests, against a real KV.
func adapterIsReadGrantLicensed(adpt adapter.Adapter) bool {
	licensed, reports := adpt.(interface{ ReadGrantWriter() bool })
	return reports && licensed.ReadGrantWriter()
}

// TestBuildAdapter_ReadGrantProducer_KeepsItsLicenceAcrossAnIntoReload is the
// third rule property every adapter built for a lens must acquire, and the one
// whose absence is silent.
//
// An INTO-only hot reload builds a FRESH adapter and swaps it into the running
// pipeline. CoreKVSource fires its update callback on every CDC revision of a
// known lens spec — a package reinstall with an UNCHANGED cypher classifies as
// IntoOnly and nothing in hotReloadRefusal stops it — so a `lattice pkg
// install` of a package owning a cap-read producer, or a kernel re-seed of
// capabilityRead, runs this path routinely.
//
// An unlicensed replacement would then refuse every cap-read key the lens
// renders: its retractions fail while the grants they meant to withdraw stay
// live, in the over-grant direction, with no notifyGrantChange because the
// write errors before the announcement. Binding in buildAdapter — the single
// point activation and the reload replacement both pass through — is what
// makes the licence a property of the rule rather than of one call site.
func TestBuildAdapter_ReadGrantProducer_KeepsItsLicenceAcrossAnIntoReload(t *testing.T) {
	r := readGrantProducerRule(t)
	adpt, err := buildAdapter(r, func(*lens.Rule) (adapter.Adapter, error) {
		return newKVAdapter(t), nil
	})
	require.NoError(t, err)
	assert.True(t, adapterIsReadGrantLicensed(adpt),
		"a read-grant producer's replacement adapter must carry the namespace licence, or a package reinstall silently unlicenses the lens")
}

// TestBuildAdapter_PlainRule_GainsNoReadGrantLicence is the other direction,
// and it carries the security half: the reload path must not hand the licence
// to a lens the installer would refuse. A binding that armed every adapter
// would close the hole by opening the namespace.
func TestBuildAdapter_PlainRule_GainsNoReadGrantLicence(t *testing.T) {
	r := readGrantProducerRule(t)
	// Doc mode: no entryKeyColumn, so IsReadGrantProducer refuses it.
	r.Output.EntryKeyColumn = ""
	r.Output.RealnessFilter = ""
	adpt, err := buildAdapter(r, func(*lens.Rule) (adapter.Adapter, error) {
		return newKVAdapter(t), nil
	})
	require.NoError(t, err)
	assert.False(t, adapterIsReadGrantLicensed(adpt),
		"a lens that does not qualify as a read-grant producer must not acquire the namespace licence from the reload path")

	// And a lens with no output descriptor at all — the descriptor-less plain
	// shape the runtime guard exists for — likewise.
	plain := authPlaneRule(t)
	plain.ProjectionKind = ""
	plain.Output = nil
	plainAdpt, err := buildAdapter(plain, func(*lens.Rule) (adapter.Adapter, error) {
		return newKVAdapter(t), nil
	})
	require.NoError(t, err)
	assert.False(t, adapterIsReadGrantLicensed(plainAdpt),
		"a descriptor-less plain lens declares no key space and must never be licensed")
}
