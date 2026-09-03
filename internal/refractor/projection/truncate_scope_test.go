package projection_test

import (
	"context"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	servicelocation "github.com/operatinggraph/lattice/packages/service-location"
)

// shippedServiceAccessSpec returns packages/service-location's own
// capabilityServiceAccess LensSpec — the shipped declaration, not a copy of it.
// A hand-written duplicate of the output key pattern would keep this test green
// while the real lens drifted to a pattern that scopes nothing.
func shippedServiceAccessSpec(t *testing.T) pkgmgr.LensSpec {
	t.Helper()
	for _, spec := range servicelocation.Lenses() {
		if spec.CanonicalName == "capabilityServiceAccess" {
			return spec
		}
	}
	t.Fatal("packages/service-location no longer declares capabilityServiceAccess — this test is about the shipped shared-bucket lens, so it must be re-aimed rather than deleted")
	return pkgmgr.LensSpec{}
}

// serviceAccessRule is the Refractor-side lens.Rule for that shipped spec: the
// same actorAggregate projection kind and the same Output descriptor, so the
// prefix under test is the one the package actually ships.
func serviceAccessRule(t *testing.T) *lens.Rule {
	t.Helper()
	spec := shippedServiceAccessSpec(t)
	if spec.Output == nil {
		t.Fatal("the shipped capabilityServiceAccess spec declares no Output descriptor — nothing scopes its truncate")
	}
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc:location)
RETURN identity.key AS actorKey, collect(loc.key) AS serviceAccess
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if spec.ProjectionKind != projection.ActorAggregateKind {
		t.Fatalf("the shipped spec is %q, not an actor-aggregate — ApplyTruncateScope would decline it", spec.ProjectionKind)
	}
	return &lens.Rule{
		ID:             "lens-capability-service-access",
		CanonicalName:  spec.CanonicalName,
		ProjectionKind: spec.ProjectionKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into:           lens.IntoConfig{Target: "nats_kv", Bucket: spec.Bucket, Key: lens.KeyField{"key"}},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:       spec.Output.AnchorType,
			OutputKeyPattern: spec.Output.OutputKeyPattern,
			BodyColumns:      spec.Output.BodyColumns,
			EmptyBehavior:    spec.Output.EmptyBehavior,
			Freshness:        spec.Output.Freshness,
		},
	}
}

// seedSharedBucket writes one row for the service-access lens and two rows that
// belong to OTHER producers of the same bucket — the core `cap.<actor>` surface
// and its `cap.roles.<actor>` sibling.
func seedSharedBucket(t *testing.T, kv *substrate.KV, ownPrefix string) (own string, siblings []string) {
	t.Helper()
	ctx := context.Background()
	own = ownPrefix + "identity.ZwqPmRtw9nbCxz5vQ2yH"
	siblings = []string{
		"cap.identity.ZwqPmRtw9nbCxz5vQ2yH",
		"cap.roles.identity.ZwqPmRtw9nbCxz5vQ2yH",
	}
	for _, key := range append([]string{own}, siblings...) {
		if _, err := kv.Put(ctx, key, []byte(`{"key":"`+key+`"}`)); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	return own, siblings
}

func liveKeys(t *testing.T, kv *substrate.KV, keys ...string) []string {
	t.Helper()
	ctx := context.Background()
	var live []string
	for _, key := range keys {
		entry, err := kv.Get(ctx, key)
		if err == nil && entry != nil {
			live = append(live, key)
		}
	}
	return live
}

// TestTruncateScope_ServiceAccessLensPurgesOnlyItsOwnKeys is the end-to-end
// guard on the one operation in Refractor that can delete rows it does not own.
//
// A rebuild truncates through whatever adapter the lens is running on, and
// NatsKVAdapter.Truncate with no key prefix purges the WHOLE bucket. For a lens
// sharing capability-kv with the platform's core authorization surfaces that is
// not a rebuild, it is a platform-wide authorization wipe — every sibling
// producer's grants gone, healed only at sweep pace. What prevents it is
// projection.ApplyTruncateScope binding the lens's own declared key prefix onto
// the adapter, and nothing else: the prefix is a property of the RULE, so every
// adapter built for that rule (activation and INTO-hot-reload replacement
// alike) has to acquire it.
//
// The negative half runs the same truncate through an UNSCOPED adapter over the
// same seeded bucket, so this test fails — rather than passing vacuously — if
// ApplyTruncateScope ever stops being applied on the path to a rebuild.
func TestTruncateScope_ServiceAccessLensPurgesOnlyItsOwnKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	kv := startTargetKV(t)
	r := serviceAccessRule(t)
	scoped, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	projection.ApplyTruncateScope(scoped, r)
	prefix := scoped.KeyPrefix()
	if prefix == "" {
		t.Fatalf("the shipped lens's output pattern %q yields no truncate scope — an unconfined purge of its shared bucket",
			r.Output.OutputKeyPattern)
	}
	own, siblings := seedSharedBucket(t, kv, prefix)

	if err := scoped.Truncate(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if live := liveKeys(t, kv, own); len(live) != 0 {
		t.Fatalf("a scoped truncate must still clear the lens's OWN rows, %v survived", live)
	}
	if live := liveKeys(t, kv, siblings...); len(live) != len(siblings) {
		t.Fatalf("a scoped truncate must not touch another producer's rows in the shared bucket; only %v survived of %v", live, siblings)
	}

	// The same truncate without the scope wipes the siblings — which is what
	// makes the assertion above a real one.
	unscoped, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if err := unscoped.Truncate(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if live := liveKeys(t, kv, siblings...); len(live) != 0 {
		t.Fatalf("precondition for this whole test: an unscoped truncate purges the bucket whole, %v survived", live)
	}
}

// kernelCapabilityRule is the Refractor-side lens.Rule for the SHIPPED kernel
// `capability` lens definition — the shipped Output descriptor, not a copy, so
// the prefix under test is the one the platform actually writes.
func kernelCapabilityRule(t *testing.T) *lens.Rule {
	t.Helper()
	def := bootstrap.CapabilityLensDefinition()
	if def.Output == nil {
		t.Fatal("the kernel capability lens declares no Output descriptor — nothing scopes its truncate")
	}
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
RETURN identity.key AS actorKey, collect(role.key) AS platformPermissions
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return &lens.Rule{
		ID:             "lens-capability",
		CanonicalName:  def.CanonicalName,
		ProjectionKind: projection.ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into:           lens.IntoConfig{Target: "nats_kv", Bucket: def.TargetBucket, Key: lens.KeyField{"key"}},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:         def.Output.AnchorType,
			OutputKeyPattern:   def.Output.OutputKeyPattern,
			BodyColumns:        def.Output.BodyColumns,
			EmptyBehavior:      def.Output.EmptyBehavior,
			Freshness:          def.Output.Freshness,
			Lanes:              def.Output.Lanes,
			StaticEmptyColumns: def.Output.StaticEmptyColumns,
		},
	}
}

// TestTruncateScope_KernelCapabilityLensPurgesOnlyTheKeysItsOwnInverseClaims is
// the case a prefix cannot decide. The kernel `capability` lens writes
// `cap.{actorSuffix}`, so its truncate scope is the literal `cap.` — which also
// covers `cap.ephemeral.`, `cap.svc.`, `cap.roles.` and
// `cap.role-by-operation.`, the four sibling producers of the same bucket. A
// prefix-scoped purge takes all four with it and re-derives none of them, and it
// is not an operator-only path any more: the lens is guarded (auth plane, and a
// tombstone empty behavior), so a package upgrade's bodyColumns-only Output edit
// FORCES the purge on its way through re-activation.
//
// What confines it is the descriptor's own key inverse, bound beside the prefix
// by ApplyTruncateScope. The negative half runs the identical purge through an
// adapter carrying only the prefix, so this fails rather than passing vacuously
// if the ownership binding is ever dropped.
func TestTruncateScope_KernelCapabilityLensPurgesOnlyTheKeysItsOwnInverseClaims(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	kv := startTargetKV(t)
	r := kernelCapabilityRule(t)

	owned, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	projection.ApplyTruncateScope(owned, r)
	if owned.KeyPrefix() != "cap." {
		t.Fatalf("the kernel lens's pattern %q must scope to %q, got %q",
			r.Output.OutputKeyPattern, "cap.", owned.KeyPrefix())
	}

	const actor = "ZwqPmRtw9nbCxz5vQ2yH"
	own := "cap.identity." + actor
	// Every sibling shape the `cap.` prefix admits, and one row of each. The
	// first three are `<literal>.<type>.<id>` — four segments once the prefix is
	// cut, which ParseVertexKey rejects; the fourth is keyed by operation type,
	// not by an anchor at all.
	siblings := []string{
		"cap.ephemeral.identity." + actor,
		"cap.svc.identity." + actor,
		"cap.roles.identity." + actor,
		"cap.role-by-operation.CreatePatient",
	}
	for _, key := range append([]string{own}, siblings...) {
		if _, err := kv.Put(ctx, key, []byte(`{"key":"`+key+`"}`)); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	if err := owned.Truncate(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if live := liveKeys(t, kv, own); len(live) != 0 {
		t.Fatalf("the lens's own row must still be purged, %v survived", live)
	}
	if live := liveKeys(t, kv, siblings...); len(live) != len(siblings) {
		t.Fatalf("a purge confined by the lens's own key inverse must leave every sibling producer's row: only %v survived of %v", live, siblings)
	}

	// The same purge with the prefix but no ownership test wipes all four —
	// which is what makes the assertion above a real one.
	prefixOnly, err := adapter.New(kv, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	prefixOnly.SetKeyPrefix("cap.")
	if err := prefixOnly.Truncate(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if live := liveKeys(t, kv, siblings...); len(live) != 0 {
		t.Fatalf("precondition for this whole test: a prefix-only purge takes every sibling under the prefix, %v survived", live)
	}
}

// A descriptor whose key inverse does not recover the anchor BuildKey rendered
// for cannot be the ownership test: bound anyway it would reject the lens's own
// rows and the purge would clear nothing. Such a lens keeps the prefix-only
// scoping — the behaviour it has when no owner is bound at all.
func TestTruncateScope_BrokenKeyInverseKeepsThePrefixOnlyScoping(t *testing.T) {
	r := kernelCapabilityRule(t)
	// Two placeholders: BuildKey substitutes both, the inverse brackets the
	// first, so the round trip fails.
	r.Output.OutputKeyPattern = "cap.{actorSuffix}.{actorSuffix}"

	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	projection.ApplyTruncateScope(adpt, r)

	desc, err := projection.ParseOutputDescriptor(r.Output)
	if err != nil {
		t.Fatalf("parse descriptor: %v", err)
	}
	if desc.KeyOwnershipRoundTrips() {
		t.Fatal("precondition: this pattern's inverse must NOT round-trip, or the test proves nothing")
	}
	if adpt.KeyPrefix() != "cap." {
		t.Fatalf("the prefix scoping still applies, got %q", adpt.KeyPrefix())
	}
	if adpt.OwnsKeysExactly() {
		t.Fatal("an inverse that cannot recover the lens's own anchor must not be bound as the ownership test — every one of the lens's own rows would be skipped")
	}
}
