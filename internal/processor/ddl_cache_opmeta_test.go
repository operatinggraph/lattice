package processor

import (
	"context"
	"slices"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedOpMeta writes an op-meta vertex the way pkgmgr's install does
// (internal/pkgmgr/build.go's OpMetas loop): a `vtx.meta.<NanoID>` root whose
// class is the same `meta.ddl.vertexType` a real DDL uses and whose data
// carries operationType, plus an optional `.dispatch` aspect. dispatch nil
// seeds no aspect at all.
func seedOpMeta(t *testing.T, ctx context.Context, conn *substrate.Conn, metaID, opType string, dispatch []byte, tombstoned bool) string {
	t.Helper()
	root := "vtx.meta." + metaID
	del := "false"
	if tombstoned {
		del = "true"
	}
	doc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":` + del + `,"data":{"operationType":"` + opType + `"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, doc); err != nil {
		t.Fatalf("seed op-meta %s: %v", root, err)
	}
	if dispatch != nil {
		if _, err := conn.KVPut(ctx, testCoreBucket, root+".dispatch", dispatch); err != nil {
			t.Fatalf("seed dispatch %s: %v", root, err)
		}
	}
	return root
}

func dispatchAspect(root string, optionalReads string, tombstoned bool) []byte {
	del := "false"
	if tombstoned {
		del = "true"
	}
	return []byte(`{"class":"dispatch","vertexKey":"` + root + `","localName":"dispatch","isDeleted":` + del +
		`,"data":{"class":"identity","authContext":"self","optionalReads":` + optionalReads + `}}`)
}

// TestDDLCache_LoadsOpMetaDispatch: the descriptor is readable SERVER-SIDE.
// Before this it was not — an op-meta carries no `.canonicalName`, so
// loadMetaVertex skipped it and the whole descriptor was write-only from the
// Processor's point of view, consumed by clients alone.
func TestDDLCache_LoadsOpMetaDispatch(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(root, `["{payload.targetIdentityKey}","{payload.targetIdentityKey}.claimKey"]`, false), false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, ok := cache.DispatchOptionalReads("ClaimIdentity")
	if !ok {
		t.Fatalf("DispatchOptionalReads = (_, false); the descriptor must be reachable at merge time")
	}
	want := []string{"{payload.targetIdentityKey}", "{payload.targetIdentityKey}.claimKey"}
	if !slices.Equal(got, want) {
		t.Fatalf("templates = %v, want %v", got, want)
	}
}

// TestDDLCache_OpMetaIsNotIndexedAsADDL: an op-meta's root class is
// byte-identical to a real vertexType DDL's, so the two are distinguished by
// `data.operationType` alone. An op-meta must never land in byName — that
// namespace is DDL canonical names, and an operationType colliding with a type
// name there would shadow that type's DDL.
func TestDDLCache_OpMetaIsNotIndexedAsADDL(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, tplID, "identity", dispatchAspect(root, `["{actor}"]`, false), false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if ref, ok := cache.Lookup("identity"); ok && ref.MetaVertexKey == root {
		t.Fatalf("the op-meta was indexed as the DDL for class %q", "identity")
	}
	if _, ok := cache.DispatchOptionalReads("identity"); !ok {
		t.Fatalf("the op-meta should still be reachable as a DESCRIPTOR")
	}
}

// TestDDLCache_TombstonedDispatchWithdrawsTheFloor: an in-place upgrade keeps
// the meta-vertex's NanoID (Contract #8 §8.1), so a package that stops
// emitting `.dispatch` gets it TOMBSTONED rather than removed. Reading a
// tombstoned dispatch as live would keep applying a floor its owner withdrew.
func TestDDLCache_TombstonedDispatchWithdrawsTheFloor(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(root, `["{payload.targetIdentityKey}"]`, true), false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	templates, ok := cache.DispatchOptionalReads("ClaimIdentity")
	if !ok {
		t.Fatalf("the descriptor itself is still there; only its dispatch was withdrawn")
	}
	if len(templates) != 0 {
		t.Fatalf("templates = %v, want none — a tombstoned dispatch reads as absent", templates)
	}
}

// TestDDLCache_InvalidateMaintainsTheDescriptorIndex: a meta-commit invalidates
// one root synchronously (step 8), so the floor must follow without waiting for
// a full Refresh — in both directions.
func TestDDLCache_InvalidateMaintainsTheDescriptorIndex(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root := "vtx.meta." + tplID

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok := cache.DispatchOptionalReads("ClaimIdentity"); ok {
		t.Fatalf("no descriptor is seeded yet")
	}

	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(root, `["{payload.targetIdentityKey}"]`, false), false)
	if err := cache.Invalidate(ctx, root+".dispatch"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if got, ok := cache.DispatchOptionalReads("ClaimIdentity"); !ok || len(got) != 1 {
		t.Fatalf("after invalidate: got (%v, %v), want the freshly committed floor", got, ok)
	}

	// Tombstone the whole op-meta: the floor must be dropped by KEY, since a
	// tombstoned root can no longer say which operationType it carried.
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(root, `["{payload.targetIdentityKey}"]`, false), true)
	if err := cache.Invalidate(ctx, root); err != nil {
		t.Fatalf("Invalidate (tombstoned): %v", err)
	}
	if got, ok := cache.DispatchOptionalReads("ClaimIdentity"); ok {
		t.Fatalf("after the op-meta was tombstoned the floor is still applied: %v", got)
	}
}

// TestDDLCache_DispatchReadsAreNotAFloor is the negative control against the
// wrong list being read. A descriptor carrying BOTH lists must surface only its
// optionalReads: its `reads` entries are not a floor, so an envelope hardening
// one of those keys is left alone (hardening is already that key's default,
// and the contract says a reads-declaring descriptor is unaffected).
func TestDDLCache_DispatchReadsAreNotAFloor(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root := "vtx.meta." + tplID
	body := []byte(`{"class":"dispatch","vertexKey":"` + root + `","localName":"dispatch","isDeleted":false,` +
		`"data":{"reads":["{payload.hardKey}"],"optionalReads":["{payload.softKey}"]}}`)
	seedOpMeta(t, ctx, conn, tplID, "MixedDispatchOp", body, false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	templates, ok := cache.DispatchOptionalReads("MixedDispatchOp")
	if !ok {
		t.Fatalf("descriptor not found")
	}
	if !slices.Equal(templates, []string{"{payload.softKey}"}) {
		t.Fatalf("templates = %v, want the optionalReads half ALONE", templates)
	}

	hard := "vtx.identity." + instID
	soft := "vtx.identity." + testNanoID2
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "MixedDispatchOp"
	env.Payload = []byte(`{"hardKey":"` + hard + `","softKey":"` + soft + `"}`)
	base := declaredReads{Reads: []string{hard, soft}}

	got := applyDescriptorFloor(base, templates, env, testLogger())
	if !slices.Contains(got.Reads, hard) {
		t.Fatalf("Reads = %v, want %q still fail-closed — a descriptor `reads` key is not a floor", got.Reads, hard)
	}
	if slices.Contains(got.Reads, soft) || !slices.Contains(got.OptionalReads, soft) {
		t.Fatalf("the descriptor-optional key was not demoted: %+v", got)
	}
}

// TestHydrate_DescriptorFloorDemotesIntoKnownAbsent is the end-to-end shape at
// the seam that matters: an envelope hardening a floored key must land in
// KnownAbsent (the script reads None and renders its own outcome), never in
// RequiredAbsent (the script's first touch faults HydrationMiss and the reply
// echoes the key back).
func TestHydrate_DescriptorFloorDemotesIntoKnownAbsent(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, tplID, "CreateIdentity",
		dispatchAspect(root, `["{payload.targetIdentityKey}"]`, false), false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h := NewHydratorWithCache(conn, testCoreBucket, cache, testLogger())

	absent := "vtx.identity." + instID
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "CreateIdentity"
	env.Payload = []byte(`{"targetIdentityKey":"` + absent + `"}`)
	env.ContextHint = &ContextHint{Reads: []string{absent}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if _, ok := state.Context.RequiredAbsent[absent]; ok {
		t.Fatalf("%s is required-absent; the floor did not demote it and the oracle is open", absent)
	}
	if _, ok := state.Context.KnownAbsent[absent]; !ok {
		t.Fatalf("KnownAbsent = %v, want %q — a demoted key resolves to None, not a fault", state.Context.KnownAbsent, absent)
	}
}
