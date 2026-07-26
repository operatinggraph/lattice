package projection_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// installRule builds an actorAggregate lens.Rule ready for InstallActorAggregate,
// with the given bucket (auth-plane driver) and empty behavior (guard driver).
func installRule(t *testing.T, bucket, emptyBehavior string) *lens.Rule {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
RETURN identity.key AS actorKey, collect(task.key) AS tasks
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return &lens.Rule{
		ID:             "lens-install-test",
		CanonicalName:  "installTest",
		ProjectionKind: projection.ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into:           lens.IntoConfig{Target: "nats_kv", Bucket: bucket, Key: lens.KeyField{"key"}},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:       "identity",
			OutputKeyPattern: "installTest.{actorSuffix}",
			BodyColumns:      []string{"tasks"},
			EmptyBehavior:    emptyBehavior,
			Freshness:        "auto",
		},
	}
}

func newTestPipeline(t *testing.T, adpt adapter.Adapter) *pipeline.Pipeline {
	t.Helper()
	p, err := pipeline.New("lens-install-test", "nats_kv", "CORE", nil, nil, adpt, nil)
	if err != nil {
		t.Fatalf("pipeline.New: %v", err)
	}
	return p
}

func newUnguardedAdapter(t *testing.T) *adapter.NatsKVAdapter {
	t.Helper()
	a, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("adapter.New: %v", err)
	}
	return a
}

// nonKVAdapter satisfies adapter.Adapter but is deliberately not *adapter.NatsKVAdapter,
// exercising EnableProjectionGuard's fail-closed branch.
type nonKVAdapter struct{}

func (nonKVAdapter) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	return nil
}
func (nonKVAdapter) Delete(context.Context, map[string]any, uint64) error { return nil }
func (nonKVAdapter) Probe(context.Context) error                         { return nil }
func (nonKVAdapter) Close() error                                        { return nil }

func TestInstallActorAggregate_InvalidOutputDescriptor_Refuses(t *testing.T) {
	r := installRule(t, "my-tasks", string(projection.EmptySkip))
	r.Output.BodyColumns = nil // ParseOutputDescriptor rejects empty bodyColumns
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger())
	if ok {
		t.Fatalf("expected refusal for an invalid output descriptor")
	}
}

func TestInstallActorAggregate_NotActorAggregate_Refuses(t *testing.T) {
	r := installRule(t, "my-tasks", string(projection.EmptySkip))
	r.ProjectionKind = "" // Compile requires IsActorAggregate
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger())
	if ok {
		t.Fatalf("expected refusal when the rule does not opt into actorAggregate")
	}
}

func TestInstallActorAggregate_Unguarded_Installs(t *testing.T) {
	r := installRule(t, "my-tasks", string(projection.EmptySkip))
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger())
	if !ok {
		t.Fatalf("expected a well-formed non-auth-plane, non-tombstone lens to install")
	}
	if adpt.Guarded() {
		t.Fatalf("an unguarded lens must not enable the projection-write guard")
	}
}

func TestInstallActorAggregate_AuthPlane_EnablesGuard(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger())
	if !ok {
		t.Fatalf("expected an auth-plane lens to install")
	}
	if !adpt.Guarded() {
		t.Fatalf("an auth-plane lens must enable the projection-write guard")
	}
}

func TestInstallActorAggregate_GuardRequiredButAdapterCannotEnforce_Refuses(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
	adpt := nonKVAdapter{}
	p := newTestPipeline(t, adpt)

	ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger())
	if ok {
		t.Fatalf("a guard-required lens on a non-NATS-KV adapter must refuse to install (fail-closed)")
	}
}

func TestInstallActorAggregate_TombstoneEmptyBehavior_EnablesGuard(t *testing.T) {
	r := installRule(t, "my-tasks", string(projection.EmptyDelete))
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger())
	if !ok {
		t.Fatalf("expected a delete-empty-behavior lens to install")
	}
	if !adpt.Guarded() {
		t.Fatalf("an empty-behavior=delete lens must enable the projection-write guard even off the auth plane")
	}
}

func TestEnableProjectionGuard_NatsKVAdapter_SetsGuarded(t *testing.T) {
	adpt := newUnguardedAdapter(t)
	if err := projection.EnableProjectionGuard(adpt, "lens-x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !adpt.Guarded() {
		t.Fatalf("expected Guarded() true after EnableProjectionGuard")
	}
}

func TestEnableProjectionGuard_NonNatsKVAdapter_Errors(t *testing.T) {
	err := projection.EnableProjectionGuard(nonKVAdapter{}, "lens-x")
	if err == nil {
		t.Fatalf("expected an error for a non-NATS-KV adapter")
	}
}

// ── ApplyGuard: the rule-bound guard requirement, re-appliable off the
// activation path (an INTO-only hot reload builds a fresh adapter). ──────────

func TestApplyGuard_AuthPlaneRule_GuardsFreshAdapter(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
	adpt := newUnguardedAdapter(t)

	if err := projection.ApplyGuard(adpt, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !adpt.Guarded() {
		t.Fatalf("an auth-plane rule must guard every adapter built for it")
	}
}

func TestApplyGuard_TombstoneEmptyBehaviorRule_GuardsFreshAdapter(t *testing.T) {
	r := installRule(t, "my-tasks", string(projection.EmptyDelete))
	adpt := newUnguardedAdapter(t)

	if err := projection.ApplyGuard(adpt, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !adpt.Guarded() {
		t.Fatalf("an empty-behavior=delete rule must guard every adapter built for it")
	}
}

func TestApplyGuard_UnguardedRule_LeavesAdapterOpen(t *testing.T) {
	r := installRule(t, "my-tasks", string(projection.EmptySkip))
	adpt := newUnguardedAdapter(t)

	if err := projection.ApplyGuard(adpt, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adpt.Guarded() {
		t.Fatalf("a rule that requires no guard must not silently enable one")
	}
}

func TestApplyGuard_NotActorAggregate_NoOp(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
	r.ProjectionKind = "" // a plain lens has no projection plan and no guard
	adpt := newUnguardedAdapter(t)

	if err := projection.ApplyGuard(adpt, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adpt.Guarded() {
		t.Fatalf("a plain lens must not be guarded by the actor-aggregate predicate")
	}
}

func TestApplyTruncateScope_BindsTheLensOwnPrefix(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
	adpt := newUnguardedAdapter(t)

	projection.ApplyTruncateScope(adpt, r)

	if got := adpt.KeyPrefix(); got != "installTest." {
		t.Fatalf("truncate must be scoped to the lens's own key prefix, got %q", got)
	}
}

// A replacement adapter built by an INTO-only hot reload must carry the scoping
// too. One that lost it would purge a shared bucket whole on its next rebuild —
// the wipe reached through the swap instead of through activation.
func TestApplyTruncateScope_ScopesAFreshAdapter(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptyDelete))

	for _, adpt := range []*adapter.NatsKVAdapter{newUnguardedAdapter(t), newUnguardedAdapter(t)} {
		projection.ApplyTruncateScope(adpt, r)
		if adpt.KeyPrefix() == "" {
			t.Fatalf("every adapter a lens writes through must carry its truncate scope")
		}
	}
}

func TestApplyTruncateScope_NotActorAggregate_NoOp(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
	r.ProjectionKind = "" // a plain lens has no output descriptor to scope by
	adpt := newUnguardedAdapter(t)

	projection.ApplyTruncateScope(adpt, r)

	if adpt.KeyPrefix() != "" {
		t.Fatalf("a plain lens must not acquire a truncate scope from the actor-aggregate predicate")
	}
}

// An unscopable pattern leaves the adapter truncating its whole bucket. That is
// the same refusal sweepEnrolment makes, with the opposite default: no scope is
// safe for a rebuild (it clears everything, which a dedicated target needs),
// where no scope is NOT safe for a sweep (it would enumerate the bucket).
func TestApplyTruncateScope_UnscopableKeyPattern_LeavesBucketWideTruncate(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
	r.Output.OutputKeyPattern = "{actorSuffix}.installTest"
	adpt := newUnguardedAdapter(t)

	projection.ApplyTruncateScope(adpt, r)

	if adpt.KeyPrefix() != "" {
		t.Fatalf("a pattern yielding no literal prefix must not produce a bogus scope")
	}
}

func TestInstallActorAggregate_ScopesTheTruncate(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	if !projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger()) {
		t.Fatalf("install must succeed")
	}
	if adpt.KeyPrefix() != "installTest." {
		t.Fatalf("activation must scope the lens's rebuild truncate to its own rows")
	}
}

func TestApplyGuard_GuardRequiredButAdapterCannotEnforce_Errors(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))

	if err := projection.ApplyGuard(nonKVAdapter{}, r); err == nil {
		t.Fatalf("a guard-required rule on a target that cannot enforce it must error, not downgrade silently")
	}
}

// ── The requirement outlives the adapter instance: an INTO-only hot reload
// must not swap a guarded lens onto an unguarded replacement. ───────────────

func TestInstallActorAggregate_AuthPlane_HotReloadRefusesUnguardedAdapter(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	if !projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger()) {
		t.Fatalf("expected the auth-plane lens to install")
	}

	// A freshly built adapter reaching the swap directly: it starts open,
	// because the guard is a flag something sets on that instance.
	replacement := newUnguardedAdapter(t)
	if err := p.HotReloadInto(replacement); err == nil {
		t.Fatalf("a guarded lens must refuse an unguarded replacement adapter")
	}
}

func TestInstallActorAggregate_AuthPlane_HotReloadAcceptsGuardedAdapter(t *testing.T) {
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	if !projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger()) {
		t.Fatalf("expected the auth-plane lens to install")
	}

	// The build path a replacement arrives through: the rule's guard
	// requirement applied to the freshly built adapter.
	replacement := newUnguardedAdapter(t)
	if err := projection.ApplyGuard(replacement, r); err != nil {
		t.Fatalf("ApplyGuard: %v", err)
	}
	if err := p.HotReloadInto(replacement); err != nil {
		t.Fatalf("a guarded replacement must be accepted: %v", err)
	}
	if !replacement.Guarded() {
		t.Fatalf("the live adapter after a hot reload must still enforce the guard")
	}
}

func TestInstallActorAggregate_Unguarded_HotReloadAcceptsAnyAdapter(t *testing.T) {
	r := installRule(t, "my-tasks", string(projection.EmptySkip))
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	if !projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger()) {
		t.Fatalf("expected the unguarded lens to install")
	}

	if err := p.HotReloadInto(newUnguardedAdapter(t)); err != nil {
		t.Fatalf("a lens that requires no guard must hot-reload freely: %v", err)
	}
}

func TestRequiresGuard_AnswersFromTheRuleAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rule   func() *lens.Rule
		expect bool
	}{
		{"auth-plane bucket", func() *lens.Rule {
			return installRule(t, projection.AuthPlaneBucket, string(projection.EmptySkip))
		}, true},
		{"tombstone empty behavior", func() *lens.Rule {
			return installRule(t, "my-tasks", string(projection.EmptyDelete))
		}, true},
		{"neither", func() *lens.Rule {
			return installRule(t, "my-tasks", string(projection.EmptySkip))
		}, false},
		{"not an actor-aggregate", func() *lens.Rule {
			r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptyDelete))
			r.ProjectionKind = ""
			return r
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := projection.RequiresGuard(tc.rule())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expect {
				t.Fatalf("RequiresGuard = %v, want %v", got, tc.expect)
			}
		})
	}
}

// ── The convergence sweep's install gate: a lens is enrolled when it can name
// its own rows, on a clock scaled to what its staleness costs. ───────────────

func TestInstallActorAggregate_BusinessLens_IsEnrolledOnTheSlowerClock(t *testing.T) {
	// The healer a business actorAggregate lens had no access to: without a
	// plan, a row added by a lens edit converges only when a CDC event next
	// happens to touch that actor.
	r := installRule(t, "weaver-targets", string(projection.EmptyDelete))
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	if ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger()); !ok {
		t.Fatalf("expected a business actor-aggregate lens to install")
	}
	sw := p.Sweeper()
	if sw == nil {
		t.Fatalf("a business actor-aggregate lens must be enrolled in the convergence sweep")
	}
	if got := sw.Interval(); got != projection.BusinessSweepInterval {
		t.Fatalf("business sweep interval = %v, want %v", got, projection.BusinessSweepInterval)
	}
}

func TestInstallActorAggregate_AuthPlaneLens_KeepsTheAuthPlaneClock(t *testing.T) {
	// Widening enrolment must not slow the plane the sweep was built for: an
	// unhealed capability document is an authorization failure, not a stale view.
	r := installRule(t, projection.AuthPlaneBucket, string(projection.EmptyDelete))
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	if ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger()); !ok {
		t.Fatalf("expected an auth-plane lens to install")
	}
	sw := p.Sweeper()
	if sw == nil {
		t.Fatalf("an auth-plane lens must keep its convergence sweep")
	}
	if got := sw.Interval(); got != pipeline.DefaultSweepInterval {
		t.Fatalf("auth-plane sweep interval = %v, want the default %v", got, pipeline.DefaultSweepInterval)
	}
}

func TestInstallActorAggregate_UnscopableKeyPattern_InstallsWithoutASweep(t *testing.T) {
	// A pattern whose keys start at the actor suffix cannot scope a listing, so
	// sweeping it would mean enumerating a target it shares with other lenses to
	// find rows it may not own. The lens still runs; it just gets no sweep.
	r := installRule(t, "weaver-targets", string(projection.EmptyDelete))
	r.Output.OutputKeyPattern = "{actorSuffix}"
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	if ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger()); !ok {
		t.Fatalf("an unscopable key pattern must not refuse the lens itself")
	}
	if p.Sweeper() != nil {
		t.Fatalf("a lens that cannot scope a listing must not be enrolled in the sweep")
	}
}

func TestInstallActorAggregate_AdapterThatCannotScopeAListing_GetsNoSweep(t *testing.T) {
	// The refusal has to be structural, not incidental. A sweep on an adapter
	// that cannot enumerate under a prefix is not a degraded sweep: survey
	// faults on every tick, the streak raises a repair-failing verdict forever,
	// and the healer never runs once — a lens reported unrepairable rather than
	// unswept.
	r := installRule(t, "weaver-targets", string(projection.EmptySkip)) // skip ⇒ no guard ⇒ any adapter installs
	adpt := nonKVAdapter{}
	p := newTestPipeline(t, adpt)

	if ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger()); !ok {
		t.Fatalf("an adapter that cannot scope a listing must not refuse the lens itself")
	}
	if p.Sweeper() != nil {
		t.Fatalf("a lens whose adapter cannot enumerate under a prefix must not be enrolled")
	}
}

func TestInstallActorAggregate_KeyPatternThatDoesNotRoundTrip_GetsNoSweep(t *testing.T) {
	// The orphan direction is the only detector for a row whose anchor is gone,
	// and it fails silently when BuildKey and AnchorFromKey disagree: claiming
	// nothing looks exactly like having nothing to claim. A repeated
	// placeholder is the shape the pattern grammar allows and the inverse does
	// not survive.
	r := installRule(t, "weaver-targets", string(projection.EmptyDelete))
	r.Output.OutputKeyPattern = "installTest.{actorSuffix}.x.{actorSuffix}"
	adpt := newUnguardedAdapter(t)
	p := newTestPipeline(t, adpt)

	if ok := projection.InstallActorAggregate(p, adpt, r, func(string) uint64 { return 0 }, nil, nil, discardLogger()); !ok {
		t.Fatalf("a non-round-tripping key pattern must not refuse the lens itself")
	}
	if p.Sweeper() != nil {
		t.Fatalf("a lens whose key pattern has no working inverse must not be enrolled")
	}
}
