package substrate

import (
	"context"
	"sort"
	"testing"
)

// TestSupervisor_IsManaged_ManagedNames proves the authoritative managed-consumer
// registry accessors: IsManaged reports membership and ManagedNames returns the
// full current set, both reflecting Add and Remove. These back the operator
// control surface's name validation (Pause/Resume are silent no-ops on an unknown
// name, so the caller validates against this registry first).
func TestSupervisor_IsManaged_ManagedNames(t *testing.T) {
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	spec := func(name string) ConsumerSpec {
		return ConsumerSpec{
			Name:          name,
			Stream:        "KV_" + bucket,
			FilterSubject: "$KV." + bucket + ".vtx.meta.>",
			Handler:       func(_ context.Context, _ Message) (Decision, error) { return Ack, nil },
		}
	}

	// Empty registry: nothing managed.
	if sup.IsManaged("loom-trigger") {
		t.Fatal("IsManaged should be false on an empty registry")
	}
	if names := sup.ManagedNames(); len(names) != 0 {
		t.Fatalf("ManagedNames should be empty on an empty registry, got %v", names)
	}

	if err := sup.Add(ctx, spec("c-a")); err != nil {
		t.Fatalf("Add c-a: %v", err)
	}
	if err := sup.Add(ctx, spec("c-b")); err != nil {
		t.Fatalf("Add c-b: %v", err)
	}

	if !sup.IsManaged("c-a") {
		t.Fatal("c-a should be managed after Add")
	}
	if !sup.IsManaged("c-b") {
		t.Fatal("c-b should be managed after Add")
	}
	if sup.IsManaged("c-missing") {
		t.Fatal("an un-added name must not report managed")
	}

	// Pause/Resume report whether the name was managed and the op applied — the
	// atomic managed-check + act a control surface fuses into one lock acquisition
	// (no IsManaged-then-act gap). A managed name → true; an unmanaged name → false
	// (a silent no-op the caller turns into an explicit not-managed error).
	if !sup.Pause(ctx, "c-a") {
		t.Fatal("Pause of a managed consumer should return true")
	}
	if !sup.Resume(ctx, "c-a") {
		t.Fatal("Resume of a managed consumer should return true")
	}
	if sup.Pause(ctx, "c-missing") {
		t.Fatal("Pause of an unmanaged name should return false (no-op)")
	}
	if sup.Resume(ctx, "c-missing") {
		t.Fatal("Resume of an unmanaged name should return false (no-op)")
	}

	got := sup.ManagedNames()
	sort.Strings(got)
	if len(got) != 2 || got[0] != "c-a" || got[1] != "c-b" {
		t.Fatalf("ManagedNames = %v, want [c-a c-b]", got)
	}

	// Removing a consumer drops it from the registry.
	if err := sup.Remove(ctx, "c-a"); err != nil {
		t.Fatalf("Remove c-a: %v", err)
	}
	if sup.IsManaged("c-a") {
		t.Fatal("c-a must not be managed after Remove")
	}
	got = sup.ManagedNames()
	if len(got) != 1 || got[0] != "c-b" {
		t.Fatalf("ManagedNames after Remove = %v, want [c-b]", got)
	}

	// ManagedNames returns a fresh copy the caller owns — mutating it must not
	// corrupt the registry's view.
	got[0] = "mutated"
	if !sup.IsManaged("c-b") {
		t.Fatal("mutating the ManagedNames result must not affect the registry")
	}
}
