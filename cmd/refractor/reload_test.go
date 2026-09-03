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
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
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
		// The kind activation's install switch dispatched on, recorded exactly as
		// newPipelineEntry records it for this rule — an entry saying otherwise
		// would read every reload of an actor-aggregate lens as a kind flip.
		projectionKind: projection.ActorAggregateKind,
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
	newLens.Into.Bucket = "weaver-targets"
	rl.update(authPlaneRule(t), newLens, lens.IntoOnly)

	assert.Zero(t, built,
		"a refused reload must decide before opening its target — building leaves an auto-created bucket behind on every redelivery")

	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), status.ErrorCount, "a refused reload must not leave health unbroken")
	require.NotNil(t, status.LastError)
	assert.Contains(t, *status.LastError, "write surface",
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

// A reorder of columns the envelope writes into a map is not a change. Reading
// it as one would cost a lens a full stop-purge-replay cycle on a cosmetic
// re-authoring.
func TestOutputDescriptorsEqual_BodyColumnOrderIsNotContent(t *testing.T) {
	entry := runningEntry()
	entry.output.BodyColumns = []string{"tasks", "grants"}
	newLens := authPlaneRule(t)
	newLens.Output.BodyColumns = []string{"grants", "tasks"}
	assert.True(t, outputDescriptorsEqual(entry.output, newLens.Output))
}

// Lanes IS emitted verbatim as the document's `lanes` array, so its order is
// content and a reorder is a real edit.
func TestOutputDescriptorsEqual_LaneOrderIsContent(t *testing.T) {
	entry := runningEntry()
	entry.output.Lanes = []string{"default", "urgent"}
	newLens := authPlaneRule(t)
	newLens.Output.Lanes = []string{"urgent", "default"}
	assert.False(t, outputDescriptorsEqual(entry.output, newLens.Output))
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

// ---------------------------------------------------------------------------
// reloader.reactivate — an Output edit is carried by stopping the lens and
// starting it again, not by either swap
// ---------------------------------------------------------------------------

// scopedTruncAdapter is an unguarded target confined to the lens's own key
// prefix — what projection.ApplyTruncateScope binds for a lens sharing a bucket.
// On this shape one question alone decides the purge: does the new Output still
// address the keys the lens already wrote?
type scopedTruncAdapter struct {
	unguardableAdapter
	truncated bool
}

func (a *scopedTruncAdapter) Truncate(context.Context) error { a.truncated = true; return nil }
func (a *scopedTruncAdapter) KeyPrefix() string              { return "reloadTest." }

// businessLensRule is an ordinary read-model actor-aggregate lens: an unguarded
// shared bucket, a literal key prefix of its own, no auth plane — the shape a
// package upgrade's bodyColumns addition actually lands on.
func businessLensRule(t *testing.T) *lens.Rule {
	t.Helper()
	r := authPlaneRule(t)
	r.Into.Bucket = "weaver-targets"
	r.Output = &lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: "reloadTest.{actorSuffix}",
		BodyColumns:      []string{"tasks"},
		EmptyBehavior:    "emptyDoc",
		Freshness:        "auto",
	}
	return r
}

// businessLensEntry is the registry entry businessLensRule activates to, over a
// pipeline whose adapter records whether the purge ran. done is the channel
// Run's goroutine closes on its way out — open here, because the entry models a
// RUNNING lens; the rig's deactivate closes it, exactly as pipelineDeleter.Delete
// waits for the real one.
func businessLensEntry(t *testing.T, adpt adapter.Adapter, reporter *health.Reporter) *pipelineEntry {
	t.Helper()
	r := businessLensRule(t)
	p, err := pipeline.New(r.ID, r.Into.Target, "CORE", nil, nil, adpt, reporter)
	require.NoError(t, err)
	return newPipelineEntry(r, adpt, p, reporter, nil, make(chan struct{}), nil)
}

// reactivationRig stands in for the two registry seams reloader.reactivate
// composes: main.go wires deactivate to remover.remove (which TAKES the entry and
// tears the pipeline down) and activateForTaxonomy to activateIfNotRegistered
// (which INSERTS one). It records the order the two ran in, because that order is
// the safety argument — the registry is keyed by lens ID, so activating before
// the removal would leave two pipelines, two durables and two writers on one lens.
type reactivationRig struct {
	entry   *pipelineEntry
	order   []string
	oldSeen *lens.Rule
	newSeen *lens.Rule
	live    bool
	// activateOK models whether the activation actually registered the lens.
	activateOK bool
	// deactivateErr models a teardown that failed — pipelineDeleter.Delete
	// returns its "remove durable" failure BEFORE it cancels the run context, so
	// the old pump is still alive when this is non-nil.
	deactivateErr error
	// stopsRun models whether the teardown waited for Run to return. A
	// deactivation that reports success without closing done is a mis-wiring, not
	// a stopped lens.
	stopsRun bool
}

func newReactivationRig(t *testing.T, entry *pipelineEntry) (*reloader, *reactivationRig) {
	t.Helper()
	rig := &reactivationRig{entry: entry, live: true, activateOK: true, stopsRun: true}
	rl := &reloader{
		ctx:    context.Background(),
		logger: discardLogger(),
		lookup: func(string) (*pipelineEntry, bool) {
			if !rig.live {
				return nil, false
			}
			return rig.entry, true
		},
		// An Output edit must reach neither swap, and the INTO swap's first act is
		// to build the replacement adapter it would HotReloadInto.
		buildAdapter: func(*lens.Rule) (adapter.Adapter, error) {
			t.Fatal("an Output edit must re-activate the lens, never build a replacement adapter to hot-reload into")
			return nil, nil
		},
		fullEngine: full.New(),
	}
	rl.deactivate = func(old *lens.Rule) error {
		rig.order = append(rig.order, "deactivate")
		rig.oldSeen = old
		if rig.deactivateErr != nil {
			return rig.deactivateErr
		}
		rig.live = false
		if rig.stopsRun {
			close(entry.done)
		}
		return nil
	}
	rl.activateForTaxonomy = func(r *lens.Rule) {
		rig.order = append(rig.order, "activate")
		rig.newSeen = r
		rig.live = rig.activateOK
	}
	return rl, rig
}

// TestReloaderUpdate_OutputEditReactivatesTheLens walks every field of the §6.13
// descriptor. Each changes something installed only at activation, so each must
// stop the lens and start it again — and the purge ahead of the replay is owed by
// exactly the edits the replay cannot overwrite: the four fields that move the
// KEYS, and an empty behavior edited to `skip`, which stops the lens writing for
// an emptied actor at all.
func TestReloaderUpdate_OutputEditReactivatesTheLens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*lens.OutputDescriptorSpec)
		purges bool
	}{
		{"emptyBehavior", func(o *lens.OutputDescriptorSpec) { o.EmptyBehavior = "softDelete" }, false},
		// `skip` writes NOTHING for an actor whose result empties out, where every
		// other behavior writes a document or a tombstone — so the replay cannot
		// reach what the old descriptor last left at those keys.
		{"emptyBehavior to skip", func(o *lens.OutputDescriptorSpec) { o.EmptyBehavior = "skip" }, true},
		{"anchorType", func(o *lens.OutputDescriptorSpec) { o.AnchorType = "provider" }, true},
		{"outputKeyPattern", func(o *lens.OutputDescriptorSpec) { o.OutputKeyPattern = "reloadTest.other.{actorSuffix}" }, true},
		{"bodyColumns", func(o *lens.OutputDescriptorSpec) { o.BodyColumns = []string{"tasks", "grants"} }, false},
		{"realnessFilter", func(o *lens.OutputDescriptorSpec) { o.RealnessFilter = "taskKey" }, false},
		{"keyColumn", func(o *lens.OutputDescriptorSpec) { o.KeyColumn = "entityId" }, true},
		{"entryKeyColumn", func(o *lens.OutputDescriptorSpec) {
			o.EntryKeyColumn, o.RealnessFilter = "anchorId", "anchorId"
		}, true},
		{"actorField", func(o *lens.OutputDescriptorSpec) { o.ActorField = "assignee" }, false},
		{"lanes", func(o *lens.OutputDescriptorSpec) { o.Lanes = []string{"write"} }, false},
		{"staticEmptyColumns", func(o *lens.OutputDescriptorSpec) { o.StaticEmptyColumns = []string{"ephemeralGrants"} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adpt := &scopedTruncAdapter{}
			entry := businessLensEntry(t, adpt, nil)
			rl, rig := newReactivationRig(t, entry)

			newLens := businessLensRule(t)
			tc.mutate(newLens.Output)
			require.Empty(t, hotReloadRefusal(entry, newLens),
				"precondition: this arm is what happens once the refusal set has passed")

			old := businessLensRule(t)
			rl.update(old, newLens, lens.IntoOnly)

			assert.Equal(t, []string{"deactivate", "activate"}, rig.order,
				"the running pipeline must be gone before its replacement is registered")
			assert.Same(t, old, rig.oldSeen, "the removal is driven from the rule the lens is actually running")
			assert.Same(t, newLens, rig.newSeen, "activation reads the edited spec")
			assert.Equal(t, tc.purges, adpt.truncated,
				"the target is purged exactly when the new Output stops addressing the keys already written")
		})
	}
}

// A cypher edit landing in the same version as an Output edit reaches update as a
// MATCH change, which re-installs the envelope exactly as little as an INTO-only
// one does. So it takes the same re-activation, and the compiled-rule swap never
// runs — otherwise the lens would evaluate the new cypher through an envelope
// built for the old descriptor.
func TestReloaderUpdate_MatchChangeCarriesAnOutputEditThroughReactivation(t *testing.T) {
	adpt := &scopedTruncAdapter{}
	entry := businessLensEntry(t, adpt, nil)
	rl, rig := newReactivationRig(t, entry)

	newLens := businessLensRule(t)
	newLens.Output.BodyColumns = []string{"tasks", "grants"}
	newLens.Sequence = 99

	rl.update(businessLensRule(t), newLens, lens.MatchChange)

	assert.Equal(t, []string{"deactivate", "activate"}, rig.order,
		"a MATCH edit is no way around the re-activation an Output edit needs")
	assert.Same(t, newLens, rig.newSeen)
	assert.False(t, adpt.truncated, "the keys did not move, so the replay overwrites in place")
}

// unscopedTruncAdapter is the shape a PLAIN lens's adapter has:
// projection.ApplyTruncateScope derives a key prefix only for an actor-aggregate
// rule, so nothing ever confines this one, and RebuildTruncateIsScoped answers
// false for a NATS-KV target carrying no prefix.
type unscopedTruncAdapter struct {
	unguardableAdapter
	truncated bool
}

func (a *unscopedTruncAdapter) Truncate(context.Context) error { a.truncated = true; return nil }
func (a *unscopedTruncAdapter) KeyPrefix() string              { return "" }

// A descriptor arriving or departing is the largest Output change there is — the
// lens switches between the actor-aggregate envelope and the plain path — and it
// moves every key. The two directions are NOT symmetric, though, and the
// asymmetry is a property of the OLD adapter rather than a choice:
//
//   - DROPPED: the old adapter is the actor-aggregate one, confined to the
//     lens's own prefix, so the purge runs and the rows it wrote are retracted.
//   - ADDED: the old adapter is the PLAIN lens's, which
//     projection.ApplyTruncateScope never scoped, so the purge is declined as
//     unconfined and the plain lens's rows stay where they are — the same outcome
//     a tombstone or a restart leaves. Scoping the purge off the NEW descriptor
//     would not reach them either: they were written under a key shape it does
//     not claim.
func TestReloaderUpdate_OutputDroppedOrAddedReactivates(t *testing.T) {
	t.Run("dropped", func(t *testing.T) {
		adpt := &scopedTruncAdapter{}
		entry := businessLensEntry(t, adpt, nil)
		rl, rig := newReactivationRig(t, entry)

		newLens := businessLensRule(t)
		newLens.Output, newLens.ProjectionKind = nil, ""

		rl.update(businessLensRule(t), newLens, lens.IntoOnly)

		assert.Equal(t, []string{"deactivate", "activate"}, rig.order)
		assert.True(t, adpt.truncated, "a lens with no descriptor derives its keys another way entirely")
	})

	t.Run("added", func(t *testing.T) {
		adpt := &unscopedTruncAdapter{}
		plain := businessLensRule(t)
		plain.Output, plain.ProjectionKind = nil, ""
		p, err := pipeline.New(plain.ID, plain.Into.Target, "CORE", nil, nil, adpt, nil)
		require.NoError(t, err)
		entry := newPipelineEntry(plain, adpt, p, nil, nil, make(chan struct{}), nil)
		require.False(t, p.RebuildTruncateIsScoped(),
			"precondition: a plain lens's adapter is never scoped, which is what production hands this seam")
		rl, rig := newReactivationRig(t, entry)

		newLens := businessLensRule(t)
		rl.update(plain, newLens, lens.IntoOnly)

		assert.Equal(t, []string{"deactivate", "activate"}, rig.order, "the edit still re-activates")
		assert.False(t, adpt.truncated,
			"an unconfined purge would clear every producer of the bucket; the plain lens's own rows are left, unretracted, and the Warn says so")
	})
}

// TestReloaderUpdate_OutputEditOnAnAlreadyRemovedLensIsIgnored: a concurrent
// operator delete can take the registry entry between this reloader's lookup and
// its teardown. Whoever removed it decided the lens ID is going away, so there is
// nothing to re-activate and nothing to write to health — re-creating a record
// for a lens a delete has just taken away would resurrect it in every operator
// view.
// update's own unknown-lens arm takes exactly this view.
func TestReloaderUpdate_OutputEditOnAnAlreadyRemovedLensIsIgnored(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	adpt := &scopedTruncAdapter{}
	rl, rig := newReactivationRig(t, businessLensEntry(t, adpt, reporter))
	rig.deactivateErr = errLensNotRunning

	newLens := businessLensRule(t)
	newLens.Output.BodyColumns = []string{"tasks", "grants"}
	rl.update(businessLensRule(t), newLens, lens.IntoOnly)

	assert.Equal(t, []string{"deactivate"}, rig.order, "nothing may be activated for a lens that is being deleted")
	assert.False(t, adpt.truncated)
	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Zero(t, status.ErrorCount, "a lens that is going away owes no fault")
	assert.Nil(t, status.LastError)
}

// The refusal set decides first, and that ordering is what keeps a lens the
// refusal protects from being torn down anyway: a guarded lens moving its write
// surface strands every key it wrote there, and re-activating it would not give
// those keys back.
func TestReloaderUpdate_GuardedSurfaceMoveIsRefusedAlongsideAnOutputEdit(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	p, err := pipeline.New("lens-reload-test", "nats_kv", "CORE", nil, nil, newKVAdapter(t), nil)
	require.NoError(t, err)

	entry := runningEntry()
	entry.reporter = reporter
	entry.pipeline = p
	rl, rig := newReactivationRig(t, entry)

	newLens := authPlaneRule(t)
	newLens.Into.Bucket = "weaver-targets"
	newLens.Output.BodyColumns = []string{"tasks", "grants"}

	rl.update(authPlaneRule(t), newLens, lens.IntoOnly)

	assert.Empty(t, rig.order, "a refused update must not stop the lens it is protecting")
	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status.LastError)
	assert.Contains(t, *status.LastError, "write surface")
}

// The one property of a refusal worth keeping for an Output edit: a malformed
// descriptor must leave the OLD lens running. Pre-flighting the new rule's own
// activation checks is what buys that — without it the lens would be stopped for
// an activation that was always going to refuse, so the edit would take the
// projection down instead of merely not landing.
func TestReloaderUpdate_MalformedOutputKeepsTheOldLensRunning(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	adpt := &scopedTruncAdapter{}
	rl, rig := newReactivationRig(t, businessLensEntry(t, adpt, reporter))

	newLens := businessLensRule(t)
	// entryKeyColumn splits ONE list column into per-entry keys, so naming two
	// body columns is a descriptor ParseOutputDescriptor rejects.
	newLens.Output.EntryKeyColumn, newLens.Output.RealnessFilter = "anchorId", "anchorId"
	newLens.Output.BodyColumns = []string{"tasks", "grants"}

	rl.update(businessLensRule(t), newLens, lens.IntoOnly)

	assert.Empty(t, rig.order, "a descriptor that cannot compile must not cost the lens its pipeline")
	assert.False(t, adpt.truncated, "nor its rows")
	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status.LastError)
	assert.Contains(t, *status.LastError, "does not compile")
}

// A re-activation that does not take leaves the lens dark: its pipeline is gone
// and nothing replaced it. The old reporter is the one handle that outlives the
// teardown, and its health entry went with the pipeline — recording there is what
// puts the failure where an operator looks for a lens's state, not only in the
// log.
//
// Two writes, because the two say different things and both are read. The error
// is the diagnosis on the latch Loupe's fault conjunct consumes; the pause is
// the STATE, and without it RecordError re-creates the deleted entry at its
// `active` default — a lens with no pipeline behind it reading healthy. The
// reason is INFRA so the supervisor's probe loop can bring the entry back on the
// next activation; structural would hold it until an operator issued `resume`.
func TestReloaderUpdate_ReactivationThatDoesNotTakeIsRecordedOnHealth(t *testing.T) {
	t.Run("dark", func(t *testing.T) {
		kv := startHealthKV(t)
		reporter := health.New(kv, "lens-reload-test")

		rl, rig := newReactivationRig(t, businessLensEntry(t, &scopedTruncAdapter{}, reporter))
		rig.activateOK = false

		newLens := businessLensRule(t)
		newLens.Output.BodyColumns = []string{"tasks", "grants"}
		rl.update(businessLensRule(t), newLens, lens.IntoOnly)

		require.Equal(t, []string{"deactivate", "activate"}, rig.order)
		status, err := reporter.GetStatus(context.Background())
		require.NoError(t, err)
		require.NotNil(t, status.LastError)
		assert.Contains(t, *status.LastError, "the lens is dark")
		assert.Equal(t, health.StatusPaused, status.Status,
			"a lens with no pipeline behind it must not read `active`")
		require.NotNil(t, status.PauseReason)
		assert.Equal(t, health.PauseReasonInfra, *status.PauseReason,
			"infra is the pause the supervisor's probe loop serves, so the next activation resumes the entry on its own; a structural one is held until an operator issues resume, which no restart does")
	})

	// A lens whose activation was refused for an UNKNOWN taxonomy expansion is not
	// dark in that sense: it is queued, and retryRefused drives it on the next
	// taxonomy event exactly as it would a first load. Reporting it as dark would
	// put a permanent error on a lens that is waiting, correctly, for the
	// taxonomy.
	t.Run("queued for a taxonomy retry", func(t *testing.T) {
		kv := startHealthKV(t)
		reporter := health.New(kv, "lens-reload-test")

		rl, rig := newReactivationRig(t, businessLensEntry(t, &scopedTruncAdapter{}, reporter))
		rig.activateOK = false
		rl.activateForTaxonomy = func(r *lens.Rule) {
			rig.order = append(rig.order, "activate")
			rig.live = false
			rl.recordRefusedForTaxonomy(r)
		}

		newLens := businessLensRule(t)
		newLens.Output.BodyColumns = []string{"tasks", "grants"}
		rl.update(businessLensRule(t), newLens, lens.IntoOnly)

		require.Equal(t, []string{"deactivate", "activate"}, rig.order)
		assert.Zero(t, errorCount(t, reporter),
			"a lens the taxonomy will retry is not a lens that failed to re-activate")
	})
}

// protectedTableAdapter is adapter.ProtectedAdapter's shape as every question
// TruncateForReactivation asks sees it: guarded (NewProtectedAdapter forces the
// §6.2 write guard on the inner PostgresAdapter), truncatable, and — carrying no
// key prefix — counting as owning its target outright. It answers yes to all
// three, so nothing downstream of the pre-flight would stop a purge of a live
// RLS table.
type protectedTableAdapter struct {
	unguardableAdapter
	truncated bool
}

func (a *protectedTableAdapter) Truncate(context.Context) error { a.truncated = true; return nil }
func (a *protectedTableAdapter) Guarded() bool                  { return true }

// TestReloaderUpdate_ReactivationAbortsWhenTheLensDidNotStop is the ordering the
// teardown's own return value exists for. pipelineDeleter.Delete reports its
// "remove durable" failure BEFORE it cancels the run context, so a teardown that
// failed leaves the old pump alive — purging under it and activating a second
// pipeline would put two writers, two durables and two health writers on one
// lens ID. Both ways the stop can fail to happen are covered: it says so, and it
// says nothing but did not wait for Run.
func TestReloaderUpdate_ReactivationAbortsWhenTheLensDidNotStop(t *testing.T) {
	t.Run("the teardown reported a failure", func(t *testing.T) {
		kv := startHealthKV(t)
		reporter := health.New(kv, "lens-reload-test")

		adpt := &scopedTruncAdapter{}
		rl, rig := newReactivationRig(t, businessLensEntry(t, adpt, reporter))
		rig.deactivateErr = errors.New("injected: remove durable failed")

		newLens := businessLensRule(t)
		newLens.Output.BodyColumns = []string{"tasks", "grants"}
		rl.update(businessLensRule(t), newLens, lens.IntoOnly)

		assert.Equal(t, []string{"deactivate"}, rig.order,
			"nothing may be activated over a pipeline that is still running")
		assert.False(t, adpt.truncated, "and nothing may be purged out from under it")
		status, err := reporter.GetStatus(context.Background())
		require.NoError(t, err)
		require.NotNil(t, status.LastError)
		assert.Contains(t, *status.LastError, "could not be stopped")
	})

	t.Run("the teardown reported success without waiting for Run", func(t *testing.T) {
		kv := startHealthKV(t)
		reporter := health.New(kv, "lens-reload-test")

		adpt := &scopedTruncAdapter{}
		rl, rig := newReactivationRig(t, businessLensEntry(t, adpt, reporter))
		rig.stopsRun = false

		newLens := businessLensRule(t)
		newLens.Output.BodyColumns = []string{"tasks", "grants"}
		rl.update(businessLensRule(t), newLens, lens.IntoOnly)

		assert.Equal(t, []string{"deactivate"}, rig.order)
		assert.False(t, adpt.truncated)
		status, err := reporter.GetStatus(context.Background())
		require.NoError(t, err)
		require.NotNil(t, status.LastError)
		assert.Contains(t, *status.LastError, "has not stopped")
		assert.Equal(t, health.StatusPaused, status.Status,
			"a lens out of the registry with no replacement is not active")
		require.NotNil(t, status.PauseReason)
		assert.Equal(t, health.PauseReasonInfra, *status.PauseReason,
			"infra is the pause the pump probes back to life — a structural one would need an operator resume, so a restart would no longer recover the lens")
	})
}

// TestReloaderUpdate_ActorAggregateNeedsANatsKVTarget pre-flights the refusals
// that would otherwise fire AFTER the teardown. An actor-aggregate descriptor's
// write guard (EnableProjectionGuard), its per-entry key listing
// (adapter.PrefixKeyLister) and its row read-back (adapter.RowReader) are all
// NATS-KV capabilities, so such a lens on any other target has no activation
// that can succeed — and discovering that after the old pipeline is gone would
// turn an unapplyable edit into a dark lens.
func TestReloaderUpdate_ActorAggregateNeedsANatsKVTarget(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	adpt := &scopedTruncAdapter{}
	rl, rig := newReactivationRig(t, businessLensEntry(t, adpt, reporter))

	newLens := businessLensRule(t)
	newLens.Into.Target = "postgres"
	newLens.Into.Bucket = ""
	newLens.Into.Table = "read_reload_test"
	newLens.Output.BodyColumns = []string{"tasks", "grants"}
	require.Empty(t, hotReloadRefusal(rig.entry, newLens),
		"precondition: an unguarded lens may move its surface, so only the pre-flight stands here")

	rl.update(businessLensRule(t), newLens, lens.IntoOnly)

	assert.Empty(t, rig.order, "a lens that cannot activate must keep the pipeline it has")
	assert.False(t, adpt.truncated)
	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status.LastError)
	assert.Contains(t, *status.LastError, "lives only on a nats_kv target")
}

// TestReloaderUpdate_ProtectedPostgresLensIsNeverReactivated closes the arm no
// downstream flag could: the purge runs on the OLD adapter, and a protected
// Postgres one answers yes to every question TruncateForReactivation asks — the
// guard NewProtectedAdapter forces makes resolveTruncate FORCE the purge
// whatever the caller requested, Truncater is implemented, and with no key
// prefix the table counts as owned outright. So a "requested = false" decision
// changes nothing, and only refusing the re-activation outright keeps a live RLS
// table intact.
//
// The reachable trigger is a projectionKind flip OUT of actorAggregate: every
// descriptor check keys on the NEW rule, which is no longer an actor-aggregate,
// so nothing else on this path looks at the target at all.
func TestReloaderUpdate_ProtectedPostgresLensIsNeverReactivated(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	adpt := &protectedTableAdapter{}
	entry := businessLensEntry(t, adpt, reporter)
	entry.target = "postgres"
	entry.bucket = ""
	entry.table = "read_landlord_units"
	rl, rig := newReactivationRig(t, entry)

	newLens := businessLensRule(t)
	newLens.Into.Target = "postgres"
	newLens.Into.Bucket = ""
	newLens.Into.Table = "read_landlord_units"
	newLens.ProjectionKind = ""
	require.Empty(t, hotReloadRefusal(entry, newLens),
		"precondition: nothing in the refusal set covers a kind flip, so only the pre-flight stands here")

	rl.update(businessLensRule(t), newLens, lens.IntoOnly)

	assert.Empty(t, rig.order, "the running lens must not be stopped")
	assert.False(t, adpt.truncated, "and its RLS table must not be cleared")
	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status.LastError)
	assert.Contains(t, *status.LastError, "lives only on a nats_kv target")
}

// TestReloaderUpdate_ProjectionKindFlipReactivates: projectionKind decides
// whether the Output descriptor is installed AT ALL — activation's install
// switch dispatches on projection.IsActorAggregate — so a lens edited into or
// out of the actor-aggregate kind needs the same re-installation an Output edit
// does, with a byte-identical descriptor on both sides and nothing else on the
// reload path examining it.
func TestReloaderUpdate_ProjectionKindFlipReactivates(t *testing.T) {
	t.Run("out of actorAggregate", func(t *testing.T) {
		adpt := &scopedTruncAdapter{}
		entry := businessLensEntry(t, adpt, nil)
		rl, rig := newReactivationRig(t, entry)

		newLens := businessLensRule(t)
		newLens.ProjectionKind = ""
		require.True(t, outputDescriptorsEqual(entry.output, newLens.Output),
			"precondition: the descriptor is unchanged, so only the kind can trigger this")

		rl.update(businessLensRule(t), newLens, lens.IntoOnly)

		assert.Equal(t, []string{"deactivate", "activate"}, rig.order)
		assert.False(t, adpt.truncated, "the keys did not move and the lens still writes for every actor")
	})

	t.Run("into actorAggregate, and the pre-flight still applies", func(t *testing.T) {
		kv := startHealthKV(t)
		reporter := health.New(kv, "lens-reload-test")

		plain := businessLensRule(t)
		plain.ProjectionKind = ""
		adpt := &scopedTruncAdapter{}
		p, err := pipeline.New(plain.ID, plain.Into.Target, "CORE", nil, nil, adpt, reporter)
		require.NoError(t, err)
		rl, rig := newReactivationRig(t, newPipelineEntry(plain, adpt, p, reporter, nil, make(chan struct{}), nil))

		// The same descriptor, now declared actorAggregate — on a target that
		// cannot carry one.
		newLens := businessLensRule(t)
		newLens.Into.Target = "postgres"
		newLens.Into.Bucket = ""

		rl.update(plain, newLens, lens.IntoOnly)

		assert.Empty(t, rig.order)
		status, err := reporter.GetStatus(context.Background())
		require.NoError(t, err)
		require.NotNil(t, status.LastError)
		assert.Contains(t, *status.LastError, "lives only on a nats_kv target")
	})
}

// An edit whose removal cannot be driven must REFUSE rather than fall through to
// an activation it cannot pair with one: the registry is keyed by lens ID, so a
// second pipeline for one lens would race the running one over its durable, its
// rows and its health entry. Both ways the removal can be undrivable are covered,
// because remover.remove is a no-op for a nil rule exactly as an unwired
// deactivate is.
func TestReloaderUpdate_OutputEditWithNoDrivableRemovalIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire func(rl *reloader, rig *reactivationRig)
		old  func(t *testing.T) *lens.Rule
	}{
		{
			name: "no registry halves wired",
			wire: func(rl *reloader, _ *reactivationRig) { rl.deactivate, rl.activateForTaxonomy = nil, nil },
			old:  businessLensRule,
		},
		{
			name: "no running rule to remove",
			wire: func(*reloader, *reactivationRig) {},
			old:  func(*testing.T) *lens.Rule { return nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kv := startHealthKV(t)
			reporter := health.New(kv, "lens-reload-test")

			adpt := &scopedTruncAdapter{}
			rl, rig := newReactivationRig(t, businessLensEntry(t, adpt, reporter))
			tc.wire(rl, rig)

			newLens := businessLensRule(t)
			newLens.Output.BodyColumns = []string{"tasks", "grants"}
			rl.update(tc.old(t), newLens, lens.IntoOnly)

			assert.Empty(t, rig.order, "nothing may be started when the removal cannot be driven")
			assert.False(t, adpt.truncated, "nor torn down")
			status, err := reporter.GetStatus(context.Background())
			require.NoError(t, err)
			require.NotNil(t, status.LastError)
			assert.Contains(t, *status.LastError, "re-activation cannot be driven")
		})
	}
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

// No source of the §6.2 guard may reach a LIVE pipeline through a swap, or a
// lens can be edited from guarded to unguarded while it runs. The four sources
// are the auth-plane bucket, the Output tombstone empty-behavior, grantTable and
// protected. Three are refused outright; the fourth is routed to re-activation,
// which rebuilds the adapter from the new rule and so acquires the guard the
// edited spec calls for — what neither may do is ride in on a HotReloadInto.
func TestHotReloadRefusal_NoGuardSourceIsUnpinned(t *testing.T) {
	t.Run("auth-plane bucket", func(t *testing.T) {
		newLens := authPlaneRule(t)
		newLens.Into.Bucket = "weaver-targets"
		assert.NotEmpty(t, hotReloadRefusal(runningEntry(), newLens))
	})
	t.Run("tombstone empty behavior", func(t *testing.T) {
		entry := runningEntry()
		newLens := authPlaneRule(t)
		newLens.Output.EmptyBehavior = "emptyDoc"
		assert.Empty(t, hotReloadRefusal(entry, newLens),
			"the guard flip is carried by a full re-activation, not by a refusal")
		assert.False(t, outputDescriptorsEqual(entry.output, newLens.Output),
			"and update must SEE it as an Output change, or the swap would carry it after all")
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
	assert.True(t, outputDescriptorsEqual(entry.output, newLens.Output))

	newLens.Output.ActorField = "assignee"
	assert.False(t, outputDescriptorsEqual(entry.output, newLens.Output))
}

// A refusal must name a remedy an operator can actually carry out. A
// package-installed lens is re-authored by an upgrade, not by hand, so
// "delete and re-create" alone is not reachable advice.
func TestHotReloadRefusal_NamesAReachableRemedy(t *testing.T) {
	newLens := authPlaneRule(t)
	newLens.Into.SecureColumns = []lens.SecureColumn{{Column: "ssn", HolderTypes: []string{"identity"}}}
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

// TestOutputIsInNeitherTheRefusalSetNorThePinnedSet holds the two halves of one
// decision together. An Output edit is applied — by re-activation — so this
// package must not refuse it AND reloadpin must not predict a refusal of it: a
// warning telling an operator to restart Refractor for a change that has already
// landed is worse than silence, because it trains them past the warnings that
// still mean something. Re-introducing the refusal has to fail HERE, loudly,
// rather than quietly stop being predicted on the installer's side.
func TestOutputIsInNeitherTheRefusalSetNorThePinnedSet(t *testing.T) {
	for _, f := range reloadpin.PinnedFields {
		assert.NotEqual(t, "output", f.Path[0],
			"reloadpin must not predict a refusal for an edit Refractor applies")
	}

	newLens := authPlaneRule(t)
	newLens.Output.BodyColumns = append(newLens.Output.BodyColumns, "grants")
	assert.Empty(t, hotReloadRefusal(runningEntry(), newLens),
		"an Output-only edit is carried by re-activation, not refused")
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

// ---------------------------------------------------------------------------
// Output re-activation end to end: the real installer, the real deleter, a real
// durable, over embedded NATS
// ---------------------------------------------------------------------------

// reactivationHarness activates a lens the way cmd/refractor's startPipeline
// does — the guard, truncate-scope and read-grant bindings buildAdapter applies,
// projection.InstallActorAggregate, a health reporter, and a supervised
// DeliverLastPerSubject durable — and removes one through the same
// pipelineDeleter a tombstone takes. Driving rl.update against THESE closures is
// what makes the seam honest at both layers: the re-activation runs the real
// activation, not a stand-in that could differ from it.
type reactivationHarness struct {
	env      *matchChangeEnv
	healthKV *substrate.KV
	engine   *full.Engine
	registry map[string]*pipelineEntry
}

func newReactivationHarness(t *testing.T) *reactivationHarness {
	t.Helper()
	env := startMatchChangeEnv(t)
	healthKV, err := env.conn.OpenKV(context.Background(), "HEALTH-lens-mc")
	require.NoError(t, err)
	return &reactivationHarness{
		env:      env,
		healthKV: healthKV,
		engine:   full.New(),
		registry: map[string]*pipelineEntry{},
	}
}

func (h *reactivationHarness) activate(t *testing.T, r *lens.Rule) {
	t.Helper()
	h.activateOn(t, r, nil)
}

// activateOn is activate over a caller-supplied adapter. Passing nil builds the
// real one; a test supplies its own to make one of the target's operations fail
// where no fault-injection seam otherwise exists.
func (h *reactivationHarness) activateOn(t *testing.T, r *lens.Rule, override adapter.Adapter) {
	t.Helper()
	adpt, err := buildAdapter(r, func(*lens.Rule) (adapter.Adapter, error) {
		if override != nil {
			return override, nil
		}
		return adapter.New(h.env.target, r.Into.Key, adapter.DeleteModeHard)
	})
	require.NoError(t, err)

	reporter := health.New(h.healthKV, r.ID)
	reporter.SetRuleSequence(r.Sequence)

	p, err := pipeline.New(r.ID, r.Into.Target, "CORE", h.env.adjKV, h.env.coreKV, adpt, reporter)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngineBranches(h.engine, r.CompiledRule, r.CompiledBranches))
	require.True(t, projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 },
		h.env.adjKV, h.env.coreKV, discardLogger()))

	p.RunOn(h.env.conn, substrate.ConsumerSpec{
		Name:          "refractor-" + r.ID,
		Stream:        subjects.CoreKVStream("CORE"),
		FilterSubject: subjects.CoreKVFilter("CORE"),
		DeliverPolicy: substrate.DeliverLastPerSubject,
		DeliverGroup:  "refractor-" + r.ID,
		AckWait:       2 * time.Second,
	})
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	h.registry[r.ID] = newPipelineEntry(r, adpt, p, reporter, cancel, done, nil)
}

// remover is the REAL remover a tombstone goes through, over this harness's
// registry — the same type, the same pipelineDeleter, the same take-then-stop
// ordering main.go builds. reactivate drives it through its `stop` half so a
// teardown failure reaches the caller instead of a log line.
func (h *reactivationHarness) remover() *remover {
	return &remover{
		logger: discardLogger(),
		take: func(lensID string) (*pipelineEntry, bool) {
			entry, ok := h.registry[lensID]
			if ok {
				delete(h.registry, lensID)
			}
			return entry, ok
		},
		unregister: func(string) {},
	}
}

// reloader wires the two halves main.go wires: deactivate to the tombstone
// path's own remover, activateForTaxonomy to the existence-checked activation
// above.
func (h *reactivationHarness) reloader(t *testing.T) *reloader {
	t.Helper()
	return &reloader{
		ctx:    context.Background(),
		logger: discardLogger(),
		lookup: func(id string) (*pipelineEntry, bool) {
			entry, ok := h.registry[id]
			return entry, ok
		},
		buildAdapter: func(*lens.Rule) (adapter.Adapter, error) {
			t.Fatal("an Output edit must re-activate the lens, never build a replacement adapter to hot-reload into")
			return nil, nil
		},
		fullEngine: h.engine,
		deactivate: func(old *lens.Rule) error { return h.remover().stop(old, "reactivation") },
		activateForTaxonomy: func(r *lens.Rule) {
			if _, exists := h.registry[r.ID]; exists {
				return
			}
			h.activate(t, r)
		},
	}
}

// reactivationLensRule is an actor-aggregate lens over the harness's target
// bucket, with a key prefix of its own so its purge is confined to its own rows.
func reactivationLensRule(t *testing.T, engine *full.Engine, id, cypher string, output *lens.OutputDescriptorSpec) *lens.Rule {
	t.Helper()
	cr, err := engine.Parse(cypher)
	require.NoError(t, err)
	return &lens.Rule{
		ID:             id,
		CanonicalName:  "reloadTest",
		ProjectionKind: projection.ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Match:          cypher,
		Into:           lens.IntoConfig{Target: "nats_kv", Key: lens.KeyField{"key"}},
		Output:         output,
	}
}

// reactivationOutput is the descriptor every variant edits, parameterised by the
// key pattern (which decides the truncate scope and the siblings it can reach),
// the empty behavior (which decides whether the target carries the §6.2 guard),
// and the body columns.
func reactivationOutput(keyPattern, emptyBehavior string, bodyColumns ...string) *lens.OutputDescriptorSpec {
	return &lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: keyPattern,
		BodyColumns:      bodyColumns,
		EmptyBehavior:    emptyBehavior,
		Freshness:        "auto",
	}
}

// reloadTestKeyPattern is the ordinary, unshared key space the doc-mode and
// guarded variants project into.
const reloadTestKeyPattern = "reloadTest.{actorSuffix}"

// seedVertex writes one Contract #1 vertex into Core KV in the envelope shape the
// pipeline decodes.
func seedVertex(t *testing.T, coreKV *substrate.KV, key string, props map[string]any) {
	t.Helper()
	doc := map[string]any{
		"isDeleted":      false,
		"createdAt":      "2026-01-01T00:00:00Z",
		"lastModifiedAt": "2026-01-01T00:00:00Z",
	}
	for k, v := range props {
		doc[k] = v
	}
	body, err := json.Marshal(doc)
	require.NoError(t, err)
	_, err = coreKV.Put(context.Background(), key, body)
	require.NoError(t, err)
}

// seedAssignedTo writes both adjacency directions for one `task assignedTo
// identity` link — the same idempotent pair the Bootstrapper's link fan-out
// builds — so the lens's OPTIONAL MATCH has an edge to traverse.
func seedAssignedTo(t *testing.T, adjKV *substrate.KV, taskID, identityID string) {
	t.Helper()
	ctx := context.Background()
	key := "lnk.task." + taskID + ".assignedTo.identity." + identityID
	edgeID := "assignedTo_" + taskID + "_" + identityID
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: key, EdgeID: edgeID, Name: "assignedTo",
		Direction: "outbound", NodeID: taskID, OtherNodeID: identityID, OtherType: "identity",
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: key, EdgeID: edgeID, Name: "assignedTo",
		Direction: "inbound", NodeID: identityID, OtherNodeID: taskID, OtherType: "task",
	}))
}

// targetDoc reads a projected document, reporting absence rather than failing —
// it is polled.
func targetDoc(kv *substrate.KV, key string) (map[string]any, bool) {
	entry, err := kv.Get(context.Background(), key)
	if err != nil {
		return nil, false
	}
	var doc map[string]any
	if json.Unmarshal(entry.Value, &doc) != nil {
		return nil, false
	}
	return doc, true
}

// coreRevision returns a Core KV entry's current revision, so a test can prove a
// re-projection came from the replay rather than from a fresh mutation.
func coreRevision(t *testing.T, kv *substrate.KV, key string) uint64 {
	t.Helper()
	entry, err := kv.Get(context.Background(), key)
	require.NoError(t, err)
	return entry.Revision
}

const (
	reactivateIdentityID = "Tsnt1ReactidentityAa"
	reactivateTaskID     = "Tsnt1ReacttaskAaaaaa"
)

const (
	reactivateCypherOld = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
RETURN identity.key AS actorKey, collect(task.key) AS tasks
`
	reactivateCypherNew = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
RETURN identity.key AS actorKey, collect(task.key) AS tasks, collect(task.stableId) AS grants
`
)

// seedReactivationCorpus writes the identity, the task assigned to it and the
// adjacency edge between them, then waits for the lens to project the identity's
// document — the pre-existing corpus a re-activation has to reach, not just the
// events that arrive after it.
func seedReactivationCorpus(t *testing.T, h *reactivationHarness, docKey string) {
	t.Helper()
	seedVertex(t, h.env.coreKV, "vtx.task."+reactivateTaskID, map[string]any{"stableId": "TASK-1"})
	seedAssignedTo(t, h.env.adjKV, reactivateTaskID, reactivateIdentityID)
	seedVertex(t, h.env.coreKV, "vtx.identity."+reactivateIdentityID, nil)

	mcPollUntil(t, 10*time.Second, func() bool {
		doc, ok := targetDoc(h.env.target, docKey)
		return ok && doc["tasks"] != nil
	}, "the lens must project the identity's document before the edit")
}

// TestReloaderUpdate_OutputBodyColumnAddedReprojectsTheStoredCorpus is the shipped
// harm, end to end: the commonest package-lens edit there is — one more entry in
// bodyColumns — must reach the rows a running lens already wrote, with no operator
// action and no new Core KV mutation to re-drive them.
func TestReloaderUpdate_OutputBodyColumnAddedReprojectsTheStoredCorpus(t *testing.T) {
	h := newReactivationHarness(t)
	ctx := context.Background()
	docKey := "reloadTest.identity." + reactivateIdentityID

	oldLens := reactivationLensRule(t, h.engine, "lens-reactivate-doc", reactivateCypherOld,
		reactivationOutput(reloadTestKeyPattern, "emptyDoc", "tasks"))
	h.activate(t, oldLens)
	seedReactivationCorpus(t, h, docKey)

	// The replay must be what rewrites the row, so nothing may touch the anchor's
	// Core KV entry across the edit.
	identityKey := "vtx.identity." + reactivateIdentityID
	revisionBeforeEdit := coreRevision(t, h.env.coreKV, identityKey)
	oldDone := h.registry[oldLens.ID].done

	newLens := reactivationLensRule(t, h.engine, oldLens.ID, reactivateCypherNew,
		reactivationOutput(reloadTestKeyPattern, "emptyDoc", "tasks", "grants"))
	newLens.Sequence = 7

	h.reloader(t).update(oldLens, newLens, lens.MatchChange)

	select {
	case <-oldDone:
	default:
		t.Fatal("the old pipeline's Run must have returned before its replacement was activated — an in-flight old-shape write would outlive the whole re-activation")
	}

	mcPollUntil(t, 15*time.Second, func() bool {
		doc, ok := targetDoc(h.env.target, docKey)
		return ok && doc["grants"] != nil
	}, "the already-stored document must be re-projected in the new shape")

	doc, ok := targetDoc(h.env.target, docKey)
	require.True(t, ok)
	assert.Equal(t, []any{"TASK-1"}, doc["grants"], "the new body column carries the new cypher's value")
	assert.Equal(t, revisionBeforeEdit, coreRevision(t, h.env.coreKV, identityKey),
		"the re-projection must come from the replay, not from a fresh Core KV write")

	// The NEW entry's health, read once its pipeline has registered — the old
	// entry's record was deleted with the old pipeline, so reading that one would
	// assert about nothing. A live fault on this path has its own test
	// (TestReloaderUpdate_TruncateFailureSurvivesTheActivationThatFollows), which
	// is what makes this clean read a claim rather than a default.
	live := h.registry[newLens.ID]
	require.NotNil(t, live)
	mcPollUntil(t, 15*time.Second, func() bool {
		_, err := live.pipeline.Pending(ctx)
		return err == nil
	}, "the replacement pipeline must register its consumer")
	status, err := live.reporter.GetStatus(ctx)
	require.NoError(t, err)
	assert.Zero(t, status.ErrorCount, "an Output edit that lands is not a fault")
	assert.Nil(t, status.LastError)
}

// TestReloaderUpdate_GuardedOutputEditLandsTheNewShape is the other half, and the
// one that needs the purge. A tombstone empty-behavior arms the §6.2 guard, whose
// watermark declines a replayed write at or below the seq already stored — so
// without the forced truncate the replay would be refused row by row and the edit
// would look applied while changing nothing.
func TestReloaderUpdate_GuardedOutputEditLandsTheNewShape(t *testing.T) {
	h := newReactivationHarness(t)
	docKey := "reloadTest.identity." + reactivateIdentityID

	oldLens := reactivationLensRule(t, h.engine, "lens-reactivate-guarded", reactivateCypherOld,
		reactivationOutput(reloadTestKeyPattern, "delete", "tasks"))
	h.activate(t, oldLens)
	require.True(t, h.registry[oldLens.ID].guarded,
		"precondition: a tombstone empty-behavior arms the §6.2 guard on the target")
	seedReactivationCorpus(t, h, docKey)

	newLens := reactivationLensRule(t, h.engine, oldLens.ID, reactivateCypherNew,
		reactivationOutput(reloadTestKeyPattern, "delete", "tasks", "grants"))
	newLens.Sequence = 7

	h.reloader(t).update(oldLens, newLens, lens.MatchChange)

	mcPollUntil(t, 15*time.Second, func() bool {
		doc, ok := targetDoc(h.env.target, docKey)
		return ok && doc["grants"] != nil
	}, "the guarded target's new shape lands only because the purge cleared the watermark it would have been declined against")

	live := h.registry[newLens.ID]
	require.NotNil(t, live)
	status, err := live.reporter.GetStatus(context.Background())
	require.NoError(t, err)
	assert.Zero(t, status.ErrorCount)
	assert.Nil(t, status.LastError)
}

// TestReloaderUpdate_GuardedOutputEditLeavesSiblingProducersRows is the shipped
// shape of the platform's most-shared key space, driven end to end. The kernel
// `capability` lens writes `cap.{actorSuffix}`, so its truncate scope is the
// literal `cap.` — which also covers `cap.ephemeral.`, `cap.svc.`, `cap.roles.`
// and `cap.role-by-operation.`. The lens is guarded, so its purge is FORCED, and
// a bodyColumns-only Output edit from a package upgrade now drives that purge
// unattended.
//
// What confines it to the lens's own rows is the descriptor's key inverse, bound
// beside the prefix by projection.ApplyTruncateScope and applied to the listing
// by NatsKVAdapter.Truncate. This asserts both directions at once: the sibling
// survives, and the lens's own row still comes back in the NEW shape — which
// only the purge makes possible on a guarded target.
func TestReloaderUpdate_GuardedOutputEditLeavesSiblingProducersRows(t *testing.T) {
	h := newReactivationHarness(t)
	ctx := context.Background()
	const capPattern = "cap.{actorSuffix}"
	docKey := "cap.identity." + reactivateIdentityID
	siblingKey := "cap.roles.identity." + reactivateIdentityID

	oldLens := reactivationLensRule(t, h.engine, "lens-reactivate-shared", reactivateCypherOld,
		reactivationOutput(capPattern, "delete", "tasks"))
	h.activate(t, oldLens)
	require.True(t, h.registry[oldLens.ID].guarded,
		"precondition: a tombstone empty-behavior arms the §6.2 guard, which FORCES the purge")
	seedReactivationCorpus(t, h, docKey)

	// Another producer's row, under the same prefix this lens's purge lists.
	_, err := h.env.target.Put(ctx, siblingKey, []byte(`{"owner":"the rbac roles lens"}`))
	require.NoError(t, err)

	newLens := reactivationLensRule(t, h.engine, oldLens.ID, reactivateCypherNew,
		reactivationOutput(capPattern, "delete", "tasks", "grants"))
	newLens.Sequence = 7

	h.reloader(t).update(oldLens, newLens, lens.MatchChange)

	mcPollUntil(t, 15*time.Second, func() bool {
		doc, ok := targetDoc(h.env.target, docKey)
		return ok && doc["grants"] != nil
	}, "the lens's own row must come back in the new shape, which only the forced purge allows on a guarded target")

	_, siblingLives := targetDoc(h.env.target, siblingKey)
	assert.True(t, siblingLives,
		"a purge confined by the lens's own key inverse must leave every sibling producer's row — `cap.` contains `cap.roles.`, and nothing re-derives what this lens removes there")
	assert.Zero(t, errorCount(t, h.registry[newLens.ID].reporter))
}

// failingTruncateAdapter is a scoped, truncatable target whose purge always
// fails. There is no other fault-injection seam on the truncate: the real
// adapter's Purge only fails when the bucket does, and a bucket taken away would
// fail the activation this test needs to run afterwards.
type failingTruncateAdapter struct{ unguardableAdapter }

func (failingTruncateAdapter) Truncate(context.Context) error {
	return errors.New("injected: purge failed")
}
func (failingTruncateAdapter) KeyPrefix() string { return "reloadTest." }

// TestReloaderUpdate_TruncateFailureSurvivesTheActivationThatFollows is the
// other half of recording that failure at all. The activation runs anyway — the
// replay re-derives whatever a partial purge removed, where abandoning would
// leave the lens dark AND its rows half gone — and the fresh pipeline's first
// clean consumer registration lands seconds later, on the same health entry. An
// unscoped clear there erases the diagnosis; Loupe's fault conjunct reads a LIVE
// LastError, so a fault the next registration retires is a fault nobody ever
// sees.
func TestReloaderUpdate_TruncateFailureSurvivesTheActivationThatFollows(t *testing.T) {
	h := newReactivationHarness(t)

	oldLens := reactivationLensRule(t, h.engine, "lens-reactivate-purgefail", reactivateCypherOld,
		reactivationOutput(reloadTestKeyPattern, "emptyDoc", "tasks"))
	h.activateOn(t, oldLens, failingTruncateAdapter{})

	// A key-shape move, so the purge is asked for on the way through.
	newLens := reactivationLensRule(t, h.engine, oldLens.ID, reactivateCypherNew,
		reactivationOutput("reloadTest.moved.{actorSuffix}", "emptyDoc", "tasks", "grants"))
	newLens.Sequence = 7

	h.reloader(t).update(oldLens, newLens, lens.MatchChange)

	entry, live := h.registry[newLens.ID]
	require.True(t, live, "a failed purge must not stop the activation — the replay is what re-derives the rows")

	// Wait for the replacement to have REGISTERED its consumer, which is the
	// moment registerWithFilterFallback's clean-registration clear runs.
	mcPollUntil(t, 15*time.Second, func() bool {
		_, err := entry.pipeline.Pending(context.Background())
		return err == nil
	}, "the replacement pipeline must register its consumer")

	status, err := entry.reporter.GetStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status.LastError,
		"the diagnosis must outlive the registration that follows it — only the writer of a message may retire it")
	assert.Contains(t, *status.LastError, "could not clear the target before the replay")
}

// TestRemoverStop_FailedTeardownIsRecordedOnTheLensHealthEntry: the health entry
// SURVIVES a failed teardown, because pipelineDeleter.Delete removes the durable
// first and only reaches reporter.Delete once the pump has stopped. So there is
// somewhere to put the diagnosis, and it has to go there: the lens has left the
// registry while its pump may still be running, and a log line is the only other
// account anyone would get.
func TestRemoverStop_FailedTeardownIsRecordedOnTheLensHealthEntry(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	entry := businessLensEntry(t, &scopedTruncAdapter{}, reporter)
	// A pipeline whose supervised consumer cannot be removed: RemoveConsumer
	// answers before the run context is ever cancelled, which is the window this
	// records for.
	entry.cancel = func() {}

	rm := &remover{
		logger: discardLogger(),
		take:   func(string) (*pipelineEntry, bool) { return entry, true },
		unregister: func(string) {
			t.Fatal("a lens whose pump may still be running must stay addressable by the operator delete op")
		},
		// The teardown's own bound, shortened. Its expiry is the only failure a
		// pipeline with no supervisor can produce, and it is exactly the window
		// the record exists for — so the branch is reached by the mechanism that
		// reaches it in production, rather than by waiting out the production
		// value.
		timeout: 200 * time.Millisecond,
	}

	err := rm.stop(businessLensRule(t), "tombstone")
	require.Error(t, err, "a teardown that never waited for Run must not report success")

	status, gerr := reporter.GetStatus(context.Background())
	require.NoError(t, gerr)
	require.NotNil(t, status.LastError)
	assert.Contains(t, *status.LastError, "lens teardown failed")
	assert.Contains(t, *status.LastError, "retry with the operator delete op")
}

// And the no-op arm is not a failure to report: nothing was running, so there is
// no pump to warn about and no entry this call is entitled to write.
func TestRemoverStop_NothingRunningIsASentinelNotAFault(t *testing.T) {
	rm := &remover{
		logger:     discardLogger(),
		take:       func(string) (*pipelineEntry, bool) { return nil, false },
		unregister: func(string) { t.Fatal("nothing to unregister") },
	}
	err := rm.stop(businessLensRule(t), "tombstone")
	assert.ErrorIs(t, err, errLensNotRunning)
}

// TestReloaderRefuse_RecordsUnderTheHotReloadClass pins the marker the clear on
// the other side reads. A refusal says only that a SWAP could not carry the
// edit — activation reads the current spec and settles it either way — so the
// verdict is retired by the next clean consumer registration, and the prefix is
// what licenses that. Without it a refusal recorded before a restart outlives
// the restart that resolved it and the lens renders faulted for an edit that
// landed.
func TestReloaderRefuse_RecordsUnderTheHotReloadClass(t *testing.T) {
	kv := startHealthKV(t)
	reporter := health.New(kv, "lens-reload-test")

	entry := runningEntry()
	entry.reporter = reporter
	rl := &reloader{ctx: context.Background(), logger: discardLogger()}

	rl.refuse(entry, "lens-reload-test", "lens update changes grantTable — not hot-reloadable")

	status, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)
	require.NotNil(t, status.LastError)
	assert.True(t, strings.HasPrefix(*status.LastError, health.HotReloadRefusalPrefix),
		"every reloader verdict carries the class marker its retirement is keyed on, got %q", *status.LastError)
	assert.Contains(t, *status.LastError, "changes grantTable",
		"and the operator still reads the reason, not just the marker")
}

// TestReloaderReactivation_UnsettledFailuresCarryNoHotReloadClass is the other
// half. Neither of these is settled by an activation: a restart un-strands no
// rows, and a dark lens is brought back by its own infra pause's
// probe-and-resume. Marking them would have the next registration retire a fault
// that is still true.
func TestReloaderReactivation_UnsettledFailuresCarryNoHotReloadClass(t *testing.T) {
	t.Run("a purge that could not clear the target", func(t *testing.T) {
		kv := startHealthKV(t)
		reporter := health.New(kv, "lens-reload-test")

		rl, rig := newReactivationRig(t, businessLensEntry(t, failingTruncateAdapter{}, reporter))

		newLens := businessLensRule(t)
		newLens.Output.OutputKeyPattern = "reloadTest.moved.{actorSuffix}"
		rl.update(businessLensRule(t), newLens, lens.IntoOnly)
		require.Equal(t, []string{"deactivate", "activate"}, rig.order,
			"precondition: a failed purge does not stop the activation")

		status, err := reporter.GetStatus(context.Background())
		require.NoError(t, err)
		require.NotNil(t, status.LastError)
		assert.Contains(t, *status.LastError, "could not clear the target")
		assert.False(t, strings.HasPrefix(*status.LastError, health.HotReloadRefusalPrefix),
			"stranded rows survive every restart, so no registration may retire this")
	})

	t.Run("a lens left dark", func(t *testing.T) {
		kv := startHealthKV(t)
		reporter := health.New(kv, "lens-reload-test")

		rl, rig := newReactivationRig(t, businessLensEntry(t, &scopedTruncAdapter{}, reporter))
		rig.activateOK = false

		newLens := businessLensRule(t)
		newLens.Output.BodyColumns = []string{"tasks", "grants"}
		rl.update(businessLensRule(t), newLens, lens.IntoOnly)

		status, err := reporter.GetStatus(context.Background())
		require.NoError(t, err)
		require.NotNil(t, status.LastError)
		assert.Contains(t, *status.LastError, "the lens is dark")
		assert.False(t, strings.HasPrefix(*status.LastError, health.HotReloadRefusalPrefix),
			"the dark marker is retired by the infra pause's own resume, not by a registration")
	})
}

// TestActivation_RetiresAHotReloadRefusalTheRestartResolved is the shipped
// symptom, end to end and through the real activation path: a lens whose health
// entry still carries a refusal from before the process came up must not keep
// rendering faulted once it activates. Activation reads the current spec and
// installs all of it, so by the time its consumer registers cleanly the edit the
// refusal named has been settled — and the entry has to say so.
//
// The re-activation path does not reach this: it DELETES the health entry with
// the pipeline, so a verdict recorded there is gone whether or not anything
// clears it. A restart is where the latch actually survives, which is what this
// drives.
func TestActivation_RetiresAHotReloadRefusalTheRestartResolved(t *testing.T) {
	h := newReactivationHarness(t)
	ctx := context.Background()

	r := reactivationLensRule(t, h.engine, "lens-reactivate-refusal", reactivateCypherOld,
		reactivationOutput(reloadTestKeyPattern, "emptyDoc", "tasks"))

	// The verdict the previous process left behind, on this lens's own key.
	stale := health.New(h.healthKV, r.ID)
	require.NoError(t, stale.RecordError(ctx,
		health.HotReloadRefusalPrefix+"lens Output descriptor changed — not hot-reloadable"))
	// And one nobody's registration settles, on a second lens, to keep the clear
	// from reading as "a fresh activation blanks the entry".
	stranded := health.New(h.healthKV, "lens-reactivate-refusal-stranded")
	const strandedMsg = "re-activation could not clear the target before the replay — rows the new Output no longer addresses may survive"
	require.NoError(t, stranded.RecordError(ctx, strandedMsg))

	h.activate(t, r)

	mcPollUntil(t, 15*time.Second, func() bool {
		status, err := stale.GetStatus(ctx)
		return err == nil && status.LastError == nil
	}, "activation settles the edit a hot-reload verdict was about, so the verdict must not outlive it")

	status, err := stale.GetStatus(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), status.ErrorCount,
		"the cumulative count is the record that the refusal happened; only the live latch is retired")

	strandedStatus, err := stranded.GetStatus(ctx)
	require.NoError(t, err)
	require.NotNil(t, strandedStatus.LastError, "an unsettled fault on another lens must be untouched")
	assert.Equal(t, strandedMsg, *strandedStatus.LastError)
}
