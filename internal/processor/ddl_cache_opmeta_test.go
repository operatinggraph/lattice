package processor

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

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
	return dispatchAspectLists(root, `[]`, optionalReads, tombstoned)
}

// dispatchAspectLists seeds a `.dispatch` carrying BOTH read-template lists,
// the way pkgmgr emits it (internal/pkgmgr/build.go's OpMetas loop writes
// `reads` and `optionalReads` side by side). reads and optionalReads are JSON
// array literals.
func dispatchAspectLists(root, reads, optionalReads string, tombstoned bool) []byte {
	del := "false"
	if tombstoned {
		del = "true"
	}
	return []byte(`{"class":"dispatch","vertexKey":"` + root + `","localName":"dispatch","isDeleted":` + del +
		`,"data":{"class":"identity","authContext":"self","reads":` + reads +
		`,"optionalReads":` + optionalReads + `}}`)
}

// floorTemplates reads the descriptor index's floor half through the real
// accessor, for the assertions whose subject is the floor alone. The pair the
// accessor returns is asserted whole wherever the `reads` half is the point.
func floorTemplates(c *DDLCache, operationType string) ([]string, bool) {
	templates, ok := c.DispatchReadTemplates(operationType)
	return templates.OptionalReads, ok
}

// TestDDLCache_LoadsOpMetaDispatch: the descriptor is readable SERVER-SIDE.
// An op-meta carries no `.canonicalName`, so loadMetaVertex declines it; the
// separate op-meta loader is what keeps the descriptor from being write-only
// from the Processor's point of view, consumed by clients alone.
func TestDDLCache_LoadsOpMetaDispatch(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspectLists(root, `["{payload.claimantKey}"]`,
			`["{payload.targetIdentityKey}","{payload.targetIdentityKey}.claimKey"]`, false), false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, ok := cache.DispatchReadTemplates("ClaimIdentity")
	if !ok {
		t.Fatalf("DispatchReadTemplates = (_, false); the descriptor must be reachable at merge time")
	}
	want := []string{"{payload.targetIdentityKey}", "{payload.targetIdentityKey}.claimKey"}
	if !slices.Equal(got.OptionalReads, want) {
		t.Fatalf("optionalReads = %v, want %v", got.OptionalReads, want)
	}
	// The `reads` half rides the same aspect and the same accessor. Without it
	// the required-wins precedence has no subject at all: every key would be
	// demotable by whatever the optional side happens to match.
	if !slices.Equal(got.Reads, []string{"{payload.claimantKey}"}) {
		t.Fatalf("reads = %v, want the descriptor's required templates — they are on the wire and must reach step 4", got.Reads)
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
	if _, ok := floorTemplates(cache, "identity"); !ok {
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
	templates, ok := floorTemplates(cache, "ClaimIdentity")
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
	if _, ok := floorTemplates(cache, "ClaimIdentity"); ok {
		t.Fatalf("no descriptor is seeded yet")
	}

	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(root, `["{payload.targetIdentityKey}"]`, false), false)
	if err := cache.Invalidate(ctx, root+".dispatch"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if got, ok := floorTemplates(cache, "ClaimIdentity"); !ok || len(got) != 1 {
		t.Fatalf("after invalidate: got (%v, %v), want the freshly committed floor", got, ok)
	}

	// Tombstone the whole op-meta: the floor must be dropped by KEY, since a
	// tombstoned root can no longer say which operationType it carried.
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(root, `["{payload.targetIdentityKey}"]`, false), true)
	if err := cache.Invalidate(ctx, root); err != nil {
		t.Fatalf("Invalidate (tombstoned): %v", err)
	}
	if got, ok := floorTemplates(cache, "ClaimIdentity"); ok {
		t.Fatalf("after the op-meta was tombstoned the floor is still applied: %v", got)
	}
}

// TestDDLCache_DispatchReadsAreNotAFloor is the negative control against the
// wrong list being read. A descriptor carrying BOTH lists floors only what its
// optionalReads names: its `reads` entries are not a floor, so an envelope
// hardening one of those keys is left alone (hardening is already that key's
// default, and the contract says a reads-declaring descriptor is unaffected).
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
	templates, ok := cache.DispatchReadTemplates("MixedDispatchOp")
	if !ok {
		t.Fatalf("descriptor not found")
	}
	if !slices.Equal(templates.OptionalReads, []string{"{payload.softKey}"}) {
		t.Fatalf("optionalReads = %v, want the floor half ALONE", templates.OptionalReads)
	}
	if !slices.Equal(templates.Reads, []string{"{payload.hardKey}"}) {
		t.Fatalf("reads = %v, want the required half kept apart from the floor", templates.Reads)
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

// cacheIndexes copies ALL SIX of the cache's maps out from under the read lock,
// so a comparison is against a value the cache cannot mutate underneath it.
//
// All six, not the two the descriptor work touches, because the guarantee being
// asserted is about Invalidate as a whole: its op-meta arms return early, so a
// comparison covering only the descriptor indexes would say nothing about the
// DDL views those returns skip, and nothing about byCommand, which is derived
// from a third.
type cacheIndexes struct {
	byRoot       map[string]MetaVertexRef
	byName       map[string]MetaVertexRef
	byMetaPK     map[string]string
	byCommand    map[string]string
	byOpType     map[string]DispatchTemplates
	byOpMetaRoot map[string]opMetaDescriptor
}

func cloneTemplates(t DispatchTemplates) DispatchTemplates {
	return DispatchTemplates{Reads: slices.Clone(t.Reads), OptionalReads: slices.Clone(t.OptionalReads)}
}

func sameTemplates(a, b DispatchTemplates) bool {
	return slices.Equal(a.Reads, b.Reads) && slices.Equal(a.OptionalReads, b.OptionalReads)
}

func snapshotIndexes(c *DDLCache) cacheIndexes {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := cacheIndexes{
		byRoot:       maps.Clone(c.byRoot),
		byName:       maps.Clone(c.byName),
		byMetaPK:     maps.Clone(c.byMetaPK),
		byCommand:    maps.Clone(c.byCommand),
		byOpType:     make(map[string]DispatchTemplates, len(c.byOpType)),
		byOpMetaRoot: make(map[string]opMetaDescriptor, len(c.byOpMetaRoot)),
	}
	for op, tpl := range c.byOpType {
		out.byOpType[op] = cloneTemplates(tpl)
	}
	for k, d := range c.byOpMetaRoot {
		out.byOpMetaRoot[k] = opMetaDescriptor{operationType: d.operationType, templates: cloneTemplates(d.templates)}
	}
	return out
}

// sameMetaVertexRef compares two projections of one meta-vertex field by field.
// Script is compared by PRESENCE rather than by pointer: each load compiles its
// own program, so two caches built from identical bytes hold different pointers
// — but one holding a program where the other holds none is a real difference,
// and the source both compiled from is already covered by ScriptSource.
func sameMetaVertexRef(a, b MetaVertexRef) bool {
	return a.MetaVertexKey == b.MetaVertexKey &&
		a.CanonicalName == b.CanonicalName &&
		a.Kind == b.Kind &&
		slices.Equal(a.PermittedCommands, b.PermittedCommands) &&
		a.Sensitive == b.Sensitive &&
		a.Abstract == b.Abstract &&
		a.CustodyKind == b.CustodyKind &&
		a.CustodyHolderKey == b.CustodyHolderKey &&
		a.ScriptSource == b.ScriptSource &&
		(a.Script == nil) == (b.Script == nil)
}

// refreshedFromScratch builds a SECOND cache over the same Core KV state and
// full-scans it. It is the oracle every assertion below compares against: a
// full Refresh is by definition the right answer for the state in the bucket,
// so "Invalidate agrees with it" is the whole property, and a spot-check on a
// length or a single key is not — an index that keeps a withdrawn claimant's
// templates has the right length and the wrong contents.
func refreshedFromScratch(t *testing.T, ctx context.Context, conn *substrate.Conn) *DDLCache {
	t.Helper()
	fresh := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := fresh.Refresh(ctx); err != nil {
		t.Fatalf("reference Refresh: %v", err)
	}
	return fresh
}

func assertMatchesFullRefresh(t *testing.T, ctx context.Context, conn *substrate.Conn, invalidated *DDLCache, stage string) {
	t.Helper()
	got := snapshotIndexes(invalidated)
	want := snapshotIndexes(refreshedFromScratch(t, ctx, conn))
	sameDescriptor := func(a, b opMetaDescriptor) bool {
		return a.operationType == b.operationType && sameTemplates(a.templates, b.templates)
	}
	if !maps.EqualFunc(got.byOpType, want.byOpType, sameTemplates) {
		t.Fatalf("%s: byOpType after Invalidate = %v, want %v — a single-root invalidate must land where a full Refresh over the same KV state lands",
			stage, got.byOpType, want.byOpType)
	}
	if !maps.EqualFunc(got.byOpMetaRoot, want.byOpMetaRoot, sameDescriptor) {
		t.Fatalf("%s: byOpMetaRoot after Invalidate = %v, want %v", stage, got.byOpMetaRoot, want.byOpMetaRoot)
	}
	if !maps.EqualFunc(got.byRoot, want.byRoot, sameMetaVertexRef) {
		t.Fatalf("%s: byRoot after Invalidate = %v, want %v", stage, got.byRoot, want.byRoot)
	}
	if !maps.EqualFunc(got.byName, want.byName, sameMetaVertexRef) {
		t.Fatalf("%s: byName after Invalidate = %v, want %v", stage, got.byName, want.byName)
	}
	if !maps.Equal(got.byMetaPK, want.byMetaPK) {
		t.Fatalf("%s: byMetaPK after Invalidate = %v, want %v", stage, got.byMetaPK, want.byMetaPK)
	}
	if !maps.Equal(got.byCommand, want.byCommand) {
		t.Fatalf("%s: byCommand after Invalidate = %v, want %v", stage, got.byCommand, want.byCommand)
	}
}

// TestDDLCache_TwoClaimantsWithdrawOneFloor is the case a single-claimant test
// cannot see. Two op-meta roots claiming ONE operationType make that
// operationType's floor an aggregate, and an aggregate can be added to by a
// per-root invalidate but not subtracted from unless the cache still knows
// which root contributed what.
//
// Both directions of the subtraction are covered, because they are different
// code paths reaching the same hazard: withdrawing a whole descriptor
// (tombstoning its root) and withdrawing one template from a descriptor that
// stays (editing its `.dispatch`). Either one silently retaining the withdrawn
// template floors a key its owner no longer declares optional, and the floor
// demotes: a key the operation genuinely depends on stops faulting
// HydrationMiss and reads None instead, which descriptor_floor.go's "Direction
// of failure" names as the dangerous one.
func TestDDLCache_TwoClaimantsWithdrawOneFloor(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	rootA := "vtx.meta." + testNanoID3
	rootB := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, testNanoID3, "ClaimIdentity",
		dispatchAspect(rootA, `["{payload.aOnly}"]`, false), false)
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(rootB, `["{payload.bOnly}","{payload.shared}"]`, false), false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got, _ := floorTemplates(cache, "ClaimIdentity"); !slices.Equal(got, []string{"{payload.aOnly}", "{payload.bOnly}", "{payload.shared}"}) {
		t.Fatalf("union floor = %v, want both claimants' templates in root-key order", got)
	}

	// (1) Withdraw A entirely. B's templates must survive; A's must not.
	seedOpMeta(t, ctx, conn, testNanoID3, "ClaimIdentity",
		dispatchAspect(rootA, `["{payload.aOnly}"]`, false), true)
	if err := cache.Invalidate(ctx, rootA); err != nil {
		t.Fatalf("Invalidate (tombstoned A): %v", err)
	}
	assertMatchesFullRefresh(t, ctx, conn, cache, "after tombstoning one of two claimants")
	got, ok := floorTemplates(cache, "ClaimIdentity")
	if !ok {
		t.Fatalf("B still claims ClaimIdentity; its floor must not be dropped with A's")
	}
	if slices.Contains(got, "{payload.aOnly}") {
		t.Fatalf("floor = %v, still carries A's template after A was tombstoned — the withdrawal was not subtracted", got)
	}
	if !slices.Equal(got, []string{"{payload.bOnly}", "{payload.shared}"}) {
		t.Fatalf("floor = %v, want exactly B's own templates", got)
	}

	// (2) Edit B to drop a template while it is still the only claimant of
	// record — the arm where a stale peer union would keep re-adding it.
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(rootB, `["{payload.bOnly}"]`, false), false)
	if err := cache.Invalidate(ctx, rootB+".dispatch"); err != nil {
		t.Fatalf("Invalidate (edited B): %v", err)
	}
	if got, _ := floorTemplates(cache, "ClaimIdentity"); !slices.Equal(got, []string{"{payload.bOnly}"}) {
		t.Fatalf("floor = %v, want the edited descriptor's own templates — a removed template must shrink the floor", got)
	}
	assertMatchesFullRefresh(t, ctx, conn, cache, "after editing a descriptor to remove a template")

	// (3) The last claimant leaves: the operationType leaves the index too, so
	// an operation with no descriptor is distinguishable from one whose
	// descriptor declares nothing.
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(rootB, `["{payload.bOnly}"]`, false), true)
	if err := cache.Invalidate(ctx, rootB); err != nil {
		t.Fatalf("Invalidate (tombstoned B): %v", err)
	}
	if got, ok := floorTemplates(cache, "ClaimIdentity"); ok {
		t.Fatalf("floor = %v, want no descriptor at all once every claimant is withdrawn", got)
	}
	assertMatchesFullRefresh(t, ctx, conn, cache, "after withdrawing the last claimant")
}

// TestDDLCache_TwoClaimantsEditWhilePeerPresent pins the edit arm from the
// other side: the peer is the one that STAYS, so a rebuild that reads the
// aggregate instead of the peer's own templates would re-add exactly what the
// edit removed.
func TestDDLCache_TwoClaimantsEditWhilePeerPresent(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	rootA := "vtx.meta." + testNanoID3
	rootB := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, testNanoID3, "ClaimIdentity",
		dispatchAspect(rootA, `["{payload.aOnly}","{payload.doomed}"]`, false), false)
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(rootB, `["{payload.bOnly}"]`, false), false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	seedOpMeta(t, ctx, conn, testNanoID3, "ClaimIdentity",
		dispatchAspect(rootA, `["{payload.aOnly}"]`, false), false)
	if err := cache.Invalidate(ctx, rootA+".dispatch"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	got, _ := floorTemplates(cache, "ClaimIdentity")
	if slices.Contains(got, "{payload.doomed}") {
		t.Fatalf("floor = %v, still carries the removed template — the rebuild unioned the stale aggregate rather than the peers' own declarations", got)
	}
	if !slices.Equal(got, []string{"{payload.aOnly}", "{payload.bOnly}"}) {
		t.Fatalf("floor = %v, want the surviving declarations of both claimants", got)
	}
	assertMatchesFullRefresh(t, ctx, conn, cache, "after editing one of two live claimants")
}

// TestDDLCache_TwoClaimantsUnionBothTemplateLists is the aggregate shape over
// the list the floor does not demote from. `reads` decides which keys the floor
// must LEAVE ALONE, so a rebuild that unioned only the optional half would let
// one claimant's optional template demote a key the other claimant declares
// required — the silent-None direction — and a withdrawal that subtracted from
// the aggregate instead of re-deriving would keep excluding keys nobody
// requires any more.
//
// Both claimants declare one template the other also declares, in DIFFERENT
// positions within their own lists, and every assertion below is over the whole
// merged list. That is what pins the union's two halves together: membership
// and order come from the claimant set alone, and a template two claimants
// declare appears ONCE. A merge that concatenated instead would double it, and
// the doubling is not cosmetic — the merged list is walked per declared key on
// the step-4 hot path, and a duplicated template is a second full pass over
// every declared key for an answer the first pass already gave.
func TestDDLCache_TwoClaimantsUnionBothTemplateLists(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	rootA := "vtx.meta." + testNanoID3
	rootB := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, testNanoID3, "ClaimIdentity",
		dispatchAspectLists(rootA,
			`["{payload.aRequired}","{payload.sharedRequired}"]`,
			`["{payload.aOptional}","{payload.sharedOptional}"]`, false), false)
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspectLists(rootB,
			`["{payload.sharedRequired}","{payload.bRequired}"]`,
			`["{payload.sharedOptional}","{payload.bOptional}"]`, false), false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// Root-key order, then each claimant's own order, each template once: the
	// shared one lands where the lowest-keyed claimant declares it.
	wantReads := []string{"{payload.aRequired}", "{payload.sharedRequired}", "{payload.bRequired}"}
	wantOptional := []string{"{payload.aOptional}", "{payload.sharedOptional}", "{payload.bOptional}"}

	got, ok := cache.DispatchReadTemplates("ClaimIdentity")
	if !ok {
		t.Fatalf("descriptor not found")
	}
	assertUnionedTemplates(t, got, wantReads, wantOptional, "two live claimants")

	// Edit A to drop its EXCLUSIVE required template while keeping the shared
	// one: the arm where a rebuild reading the stale aggregate re-adds exactly
	// what the edit removed, and where a merge that deduplicated by dropping
	// the later claimant's copy would lose a template the survivor declares.
	seedOpMeta(t, ctx, conn, testNanoID3, "ClaimIdentity",
		dispatchAspectLists(rootA,
			`["{payload.sharedRequired}"]`,
			`["{payload.aOptional}","{payload.sharedOptional}"]`, false), false)
	if err := cache.Invalidate(ctx, rootA+".dispatch"); err != nil {
		t.Fatalf("Invalidate (edited A): %v", err)
	}
	got, _ = cache.DispatchReadTemplates("ClaimIdentity")
	if slices.Contains(got.Reads, "{payload.aRequired}") {
		t.Fatalf("reads = %v, still carries the withdrawn required template — the rebuild unioned the stale aggregate rather than the claimants' own declarations", got.Reads)
	}
	assertUnionedTemplates(t, got,
		[]string{"{payload.sharedRequired}", "{payload.bRequired}"}, wantOptional,
		"after editing one of two claimants' required list")
	assertMatchesFullRefresh(t, ctx, conn, cache, "after editing one of two claimants' required list")

	// Withdraw A entirely: A's exclusive templates leave, and the shared one
	// stays — exactly once — because B still declares it. Dropping it with A
	// would floor a key B's descriptor still names optional.
	seedOpMeta(t, ctx, conn, testNanoID3, "ClaimIdentity",
		dispatchAspectLists(rootA,
			`["{payload.sharedRequired}"]`,
			`["{payload.aOptional}","{payload.sharedOptional}"]`, false), true)
	if err := cache.Invalidate(ctx, rootA); err != nil {
		t.Fatalf("Invalidate (tombstoned A): %v", err)
	}
	got, ok = cache.DispatchReadTemplates("ClaimIdentity")
	if !ok {
		t.Fatalf("B still claims ClaimIdentity; its descriptor must not be dropped with A's")
	}
	for _, gone := range []string{"{payload.aRequired}", "{payload.aOptional}"} {
		if slices.Contains(got.Reads, gone) || slices.Contains(got.OptionalReads, gone) {
			t.Fatalf("templates = %+v, still carry %q after its only claimant was withdrawn", got, gone)
		}
	}
	assertUnionedTemplates(t, got,
		[]string{"{payload.sharedRequired}", "{payload.bRequired}"},
		[]string{"{payload.sharedOptional}", "{payload.bOptional}"},
		"after withdrawing one of two claimants")
	assertMatchesFullRefresh(t, ctx, conn, cache, "after withdrawing one of two claimants")
}

// assertUnionedTemplates compares both merged lists against the exact sequence
// the claimant set implies. Equality over the whole list is what states the
// union's guarantee in one assertion — membership, order, and each template
// appearing once — and the count check that follows names the duplicate case
// directly, because "the list is longer than expected" is the report a reader
// most needs spelled out.
func assertUnionedTemplates(t *testing.T, got DispatchTemplates, wantReads, wantOptional []string, stage string) {
	t.Helper()
	for _, list := range []struct {
		name string
		got  []string
		want []string
	}{
		{"reads", got.Reads, wantReads},
		{"optionalReads", got.OptionalReads, wantOptional},
	} {
		if !slices.Equal(list.got, list.want) {
			t.Fatalf("%s: %s = %v, want %v", stage, list.name, list.got, list.want)
		}
		for _, tpl := range list.want {
			if n := countTemplate(list.got, tpl); n != 1 {
				t.Fatalf("%s: %s carries %q %d times, want once — a template two claimants declare is unioned, not concatenated", stage, list.name, tpl, n)
			}
		}
	}
}

func countTemplate(list []string, want string) int {
	n := 0
	for _, tpl := range list {
		if tpl == want {
			n++
		}
	}
	return n
}

// TestDDLCache_DuplicateCanonicalNameSurvivesTheOtherClaimantsWithdrawal is the
// same aliasing shape one index over: byName is keyed by canonicalName, which
// two meta-vertex roots can claim, so withdrawing one root must not delete a
// name the other still carries. The direction of failure is the DDL vanishing
// — and an absent DDL is Contract #1 §1.5's permissive default, so every gate
// keyed off it stops enforcing.
func TestDDLCache_DuplicateCanonicalNameSurvivesTheOtherClaimantsWithdrawal(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	// THREE contesting roots, and every arbitration assertion below is repeated
	// over independently-built caches. Both are aimed at the same weakness in
	// the assertion rather than in the code: with two claimants an unsorted
	// rebuild picks the intended winner half the time, so a single run of a
	// two-root vector passes on a coin flip. Three claimants and eight rebuilds
	// put a fail-by-luck outcome out of reach.
	twinDoc := func(deleted bool) []byte {
		return []byte(`{"class":"meta.ddl.vertexType","isDeleted":` + map[bool]string{true: "true", false: "false"}[deleted] +
			`,"data":{"canonicalName":"twinned","permittedCommands":["TwinOp"]}}`)
	}
	rootA := "vtx.meta." + testNanoID3 // lowest-keyed of the three
	rootB := "vtx.meta." + tplID
	rootC := "vtx.meta." + instID
	for _, root := range []string{rootA, rootB, rootC} {
		if _, err := conn.KVPut(ctx, testCoreBucket, root, twinDoc(false)); err != nil {
			t.Fatalf("seed %s: %v", root, err)
		}
	}

	const rebuilds = 8
	assertWinner := func(stage, want string) {
		t.Helper()
		for i := 0; i < rebuilds; i++ {
			fresh := NewDDLCache(conn, testCoreBucket, testLogger())
			if err := fresh.Refresh(ctx); err != nil {
				t.Fatalf("%s rebuild %d: Refresh: %v", stage, i, err)
			}
			ref, ok := fresh.Lookup("twinned")
			if !ok {
				t.Fatalf("%s rebuild %d: the contested canonicalName is not indexed at all", stage, i)
			}
			if ref.MetaVertexKey != want {
				t.Fatalf("%s rebuild %d: winner = %s, want the lowest-keyed live root %s — the arbitration must be a property of the SET, not of map iteration order",
					stage, i, ref.MetaVertexKey, want)
			}
		}
	}
	assertWinner("three live claimants", rootA)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, rootA, twinDoc(true)); err != nil {
		t.Fatalf("tombstone %s: %v", rootA, err)
	}
	if err := cache.Invalidate(ctx, rootA); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	ref, ok := cache.Lookup("twinned")
	if !ok {
		t.Fatalf("the name was deleted when its first claimant was withdrawn, though two roots still declare it — the class now resolves to NO DDL and every gate keyed off it goes permissive")
	}
	if ref.MetaVertexKey != rootB {
		t.Fatalf("winner = %s, want the surviving lowest-keyed root %s", ref.MetaVertexKey, rootB)
	}
	if _, stillThere := cache.LookupByMetaKey(rootA); stillThere {
		t.Fatalf("the withdrawn root is still reachable by key")
	}
	if name, _ := cache.ClassForCommand("TwinOp"); name != "twinned" {
		t.Fatalf("ClassForCommand(TwinOp) = %q, want %q — the reverse index is derived from byName and must follow it", name, "twinned")
	}
	assertMatchesFullRefresh(t, ctx, conn, cache, "after withdrawing the lowest-keyed of three claimants")
	assertWinner("two live claimants", rootB)
}

// TestDDLCache_UnparseableMetaDocumentRefusesTheRefresh: a document the cache
// can READ and cannot parse is a durable fact about the bucket — neither a
// retry nor a restart changes those bytes — and whatever the root declares is
// unknowable while it sits there. The refresh refuses, which is what stops a
// Processor from coming up around a meta-vertex nobody can read.
func TestDDLCache_UnparseableMetaDocumentRefusesTheRefresh(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	healthy := "vtx.meta." + testNanoID3
	seedOpMeta(t, ctx, conn, testNanoID3, "ClaimIdentity",
		dispatchAspect(healthy, `["{payload.targetIdentityKey}"]`, false), false)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta."+tplID, []byte(`{"class":"meta.ddl.vertexType",`)); err != nil {
		t.Fatalf("seed corrupt root: %v", err)
	}

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	err := cache.Refresh(ctx)
	if err == nil {
		t.Fatalf("Refresh succeeded over a meta document nothing can parse")
	}
	if !errors.Is(err, errMetaDocumentUntrusted) {
		t.Fatalf("Refresh error = %v, want one that classifies as an untrusted document so callers can tell it from a read that did not answer", err)
	}
	if _, ok := floorTemplates(cache, "ClaimIdentity"); ok {
		t.Fatalf("the refused refresh still published a partial index")
	}
}

// TestDDLCache_ReadThatNeverAnswersSpendsTheBudgetThenRefuses is the other side
// of that line. Both failures refuse — what the classification decides is what
// gets RETRIED — so this pins three things about a read that does not answer:
// the budget is actually spent, the failure still reaches the caller, and it is
// not filed as an unparseable document (which would skip the retry entirely).
//
// The refusal matters more than it looks. A read that gave up quietly would
// drop the root as a DDL as well as a descriptor, and an absent aspect DDL is
// not a rejected write — step 6.5 resolves the class, misses, and commits the
// aspect as plaintext, permanently, for a process whose Refresh has no second
// caller.
func TestDDLCache_ReadThatNeverAnswersSpendsTheBudgetThenRefuses(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	cache := NewDDLCache(conn, "core-kv-that-was-never-provisioned", testLogger())

	started := time.Now()
	_, _, err := cache.loadOpMetaDispatch(ctx, "vtx.meta."+tplID)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatalf("a read against a bucket that does not exist reported success")
	}
	if errors.Is(err, errMetaDocumentUntrusted) {
		t.Fatalf("a failed read classified as an unparseable document (%v) — it would never be retried", err)
	}
	// A LITERAL lower bound, deliberately not derived from metaReadAttempts and
	// metaReadRetryDelay. Deriving it would make the assertion move with the
	// very thing it guards: setting the budget to one attempt, or the delay to
	// zero, would leave a derived bound satisfied and this package green while
	// the mechanism was gone. 100ms is the floor a 3-attempt / 50ms budget must
	// clear — shrinking the budget is allowed, but only by changing this line
	// too, deliberately.
	const wantAtLeast = 100 * time.Millisecond
	if elapsed < wantAtLeast {
		t.Fatalf("the read returned after %s, before the retry budget could be spent (>= %s) — one dropped request would decide whether the Processor starts", elapsed, wantAtLeast)
	}
}

// TestDDLCache_KeyListingIsOnTheReadBudget: the listing is the one read a
// refresh cannot survive — it returns before any per-root read runs, so a single
// dropped request there costs the whole scan rather than one meta-vertex. It was
// the last read exempt from the budget the rest of the file spends.
func TestDDLCache_KeyListingIsOnTheReadBudget(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	cache := NewDDLCache(conn, "core-kv-that-was-never-provisioned", testLogger())

	started := time.Now()
	_, err := cache.listCoreKeys(ctx)
	elapsed := time.Since(started)

	if err == nil {
		t.Fatalf("listing a bucket that does not exist reported success")
	}
	const wantAtLeast = 100 * time.Millisecond
	if elapsed < wantAtLeast {
		t.Fatalf("the listing returned after %s, before the retry budget could be spent (>= %s)", elapsed, wantAtLeast)
	}
	if rerr := cache.Refresh(ctx); rerr == nil {
		t.Fatalf("Refresh succeeded without a key listing — a scan that read nothing must never publish an index")
	}
}

// TestRetryMetaRead covers the budget itself: what it spends, what it refuses to
// spend, and what it reports.
func TestRetryMetaRead(t *testing.T) {
	t.Parallel()
	boom := errors.New("kv did not answer")

	t.Run("a read that never answers spends every attempt and reports the failure", func(t *testing.T) {
		t.Parallel()
		calls := 0
		_, attempts, err := retryMetaRead(context.Background(), 3, time.Millisecond, func() (*substrate.KVEntry, error) {
			calls++
			return nil, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the read's own failure", err)
		}
		if calls != 3 || attempts != 3 {
			t.Fatalf("calls=%d attempts=%d, want the whole budget spent before the failure is taken at its word", calls, attempts)
		}
	})

	t.Run("a read that answers on a later attempt succeeds", func(t *testing.T) {
		t.Parallel()
		calls := 0
		entry, attempts, err := retryMetaRead(context.Background(), 3, time.Millisecond, func() (*substrate.KVEntry, error) {
			calls++
			if calls < 3 {
				return nil, boom
			}
			return &substrate.KVEntry{Value: []byte(`{}`)}, nil
		})
		if err != nil {
			t.Fatalf("err = %v, want the third attempt's success", err)
		}
		if entry == nil || attempts != 3 {
			t.Fatalf("entry=%v attempts=%d, want the answer the budget bought", entry, attempts)
		}
	})

	t.Run("an absent key is an answer, not a failure to retry", func(t *testing.T) {
		t.Parallel()
		calls := 0
		_, attempts, err := retryMetaRead(context.Background(), 3, time.Second, func() (*substrate.KVEntry, error) {
			calls++
			return nil, substrate.ErrKeyNotFound
		})
		if !errors.Is(err, substrate.ErrKeyNotFound) {
			t.Fatalf("err = %v, want ErrKeyNotFound surfaced unchanged", err)
		}
		if calls != 1 || attempts != 1 {
			t.Fatalf("calls=%d attempts=%d, want one — every optional aspect in the scan reports absent, and retrying that would spend the budget on the common case", calls, attempts)
		}
	})

	t.Run("a dead context is not outwaited", func(t *testing.T) {
		t.Parallel()
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		_, attempts, err := retryMetaRead(cancelled, 3, time.Hour, func() (*substrate.KVEntry, error) {
			calls++
			return nil, boom
		})
		if err == nil {
			t.Fatalf("want the read's failure")
		}
		if calls != 1 || attempts != 1 {
			t.Fatalf("calls=%d attempts=%d, want one — the next attempt would fail on the context, not on the bucket", calls, attempts)
		}
	})
}
