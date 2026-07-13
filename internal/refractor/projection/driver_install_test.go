package projection_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/projection"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
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
