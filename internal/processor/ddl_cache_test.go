package processor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asolgan/lattice/internal/substrate"
)

func TestDDLCache_RefreshAndLookup_ShadowKey(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// setupTestPipeline seeds vtx.meta.identity + .script.
	ref, ok := cache.Lookup("identity")
	if !ok {
		t.Fatalf("identity DDL not in cache")
	}
	if ref.MetaVertexKey != "vtx.meta.identity" {
		t.Fatalf("MetaVertexKey = %q", ref.MetaVertexKey)
	}
	if ref.ScriptSource == "" {
		t.Fatalf("ScriptSource empty")
	}
	if len(ref.PermittedCommands) == 0 || ref.PermittedCommands[0] != "CreateIdentity" {
		t.Fatalf("PermittedCommands = %v", ref.PermittedCommands)
	}
}

func TestDDLCache_Invalidate_AfterPut(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// Seed a new meta-vertex.
	newKey := "vtx.meta.newclass"
	doc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"newclass","permittedCommands":["DoNew"]}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, newKey, doc); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := cache.Invalidate(ctx, newKey); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	ref, ok := cache.Lookup("newclass")
	if !ok || ref.MetaVertexKey != newKey {
		t.Fatalf("after invalidate, Lookup got ok=%v ref=%+v", ok, ref)
	}
}

func TestDDLCache_Lookup_MissReturnsFalse(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok := cache.Lookup("nonexistent"); ok {
		t.Fatalf("expected miss for nonexistent class")
	}
}

func TestDDLCache_Invalidate_EvictsTombstonedRoot(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// Seed a live meta-vertex and pull it into the cache.
	key := "vtx.meta.tombclass"
	live := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"tombclass","permittedCommands":["DoTomb"]}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, live); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err != nil {
		t.Fatalf("Invalidate (live): %v", err)
	}
	if _, ok := cache.Lookup("tombclass"); !ok {
		t.Fatalf("tombclass should be present after live invalidate")
	}
	if _, ok := cache.LookupByMetaKey(key); !ok {
		t.Fatalf("LookupByMetaKey should resolve before tombstone")
	}

	// Tombstone the root (isDeleted=true) and re-invalidate. The entry must
	// be evicted from both indexes and not re-inserted.
	dead := []byte(`{"class":"meta.ddl.vertexType","isDeleted":true,"data":{}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, dead); err != nil {
		t.Fatalf("seed tombstone: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err != nil {
		t.Fatalf("Invalidate (tombstoned): %v", err)
	}
	if ref, ok := cache.Lookup("tombclass"); ok {
		t.Fatalf("tombclass must be evicted after tombstone, got %+v", ref)
	}
	if _, ok := cache.LookupByMetaKey(key); ok {
		t.Fatalf("LookupByMetaKey must report absent after tombstone")
	}
}

func TestDDLCache_LoadMetaVertex_TombstonedRootAbsent(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	key := "vtx.meta.deadload"
	// A tombstoned root with a still-present canonicalName aspect must report
	// absent before any aspect read — eviction precedes name resolution.
	dead := []byte(`{"class":"meta.ddl.vertexType","isDeleted":true,"data":{}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, dead); err != nil {
		t.Fatalf("seed tombstone: %v", err)
	}
	cn := []byte(`{"class":"canonicalName","isDeleted":false,"data":{"value":"deadload"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key+".canonicalName", cn); err != nil {
		t.Fatalf("seed canonicalName: %v", err)
	}
	ref, ok, err := cache.loadMetaVertex(ctx, key, nil)
	if err != nil {
		t.Fatalf("loadMetaVertex: %v", err)
	}
	if ok {
		t.Fatalf("tombstoned root must load as absent, got %+v", ref)
	}
}

// TestDDLCache_ClassForCommand_VertexTypeOnly is the H1 correction's load-bearing
// case: an op admitted by ONE vertexType DDL plus TWO aspectType DDLs (the
// multi-key-write pattern — RecordIdentityPII is in identity + ssn + dob) must
// resolve to the vertexType owner (identity), never to an aspectType. Only the
// vertexType DDL carries the executing script; the aspectType entries are step-6
// write gates and must not be class-inference targets.
func TestDDLCache_ClassForCommand_VertexTypeOnly(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())

	// identityx: the vertexType DDL that EXECUTES RecordIdentityPII. (Op names
	// are unique to this fixture so they don't collide with the meta-vertices
	// setupTestPipeline already seeds — a collision would itself be ambiguous.)
	seedMetaDDL(t, ctx, conn, "vtx.meta.identityx", "meta.ddl.vertexType", "identityx",
		[]string{"CreateIdentityX", "RecordIdentityPII"})
	// ssn + dob: aspectType DDLs that list RecordIdentityPII only as a step-6
	// write gate (declaration-only scripts).
	seedMetaDDL(t, ctx, conn, "vtx.meta.ssnx", "meta.ddl.aspectType", "ssnx",
		[]string{"RecordIdentityPII"})
	seedMetaDDL(t, ctx, conn, "vtx.meta.dobx", "meta.ddl.aspectType", "dobx",
		[]string{"RecordIdentityPII"})

	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	class, ok := cache.ClassForCommand("RecordIdentityPII")
	if !ok {
		t.Fatalf("RecordIdentityPII must resolve (the single vertexType owner identityx)")
	}
	if class != "identityx" {
		t.Fatalf("RecordIdentityPII resolved to %q, want identityx (NOT ssnx/dobx)", class)
	}
	// CreateIdentityX is admitted by exactly one vertexType DDL → indexed.
	if class, ok := cache.ClassForCommand("CreateIdentityX"); !ok || class != "identityx" {
		t.Fatalf("CreateIdentityX resolved ok=%v class=%q, want identityx", ok, class)
	}
}

// TestDDLCache_ClassForCommand_AmbiguityGuard is the RED-GREEN of the global
// ambiguity guard: an op admitted by TWO vertexType DDLs must NOT be indexed
// (the caller falls through to the explicit-class requirement) — inferring a
// class for an ambiguous op could run the wrong script, so it fails closed.
func TestDDLCache_ClassForCommand_AmbiguityGuard(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())

	// Two vertexType DDLs both admit DoShared — ambiguous.
	seedMetaDDL(t, ctx, conn, "vtx.meta.alpha", "meta.ddl.vertexType", "alpha",
		[]string{"DoAlpha", "DoShared"})
	seedMetaDDL(t, ctx, conn, "vtx.meta.beta", "meta.ddl.vertexType", "beta",
		[]string{"DoBeta", "DoShared"})

	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// GREEN: each unambiguous op resolves to its sole owner.
	if class, ok := cache.ClassForCommand("DoAlpha"); !ok || class != "alpha" {
		t.Fatalf("DoAlpha resolved ok=%v class=%q, want alpha", ok, class)
	}
	if class, ok := cache.ClassForCommand("DoBeta"); !ok || class != "beta" {
		t.Fatalf("DoBeta resolved ok=%v class=%q, want beta", ok, class)
	}
	// RED→guarded: the ambiguous op must miss (NOT resolve to alpha-or-beta).
	if class, ok := cache.ClassForCommand("DoShared"); ok {
		t.Fatalf("DoShared must NOT be indexed (ambiguous across alpha+beta); got %q", class)
	}
}

// TestDDLCache_ClassForCommand_Unindexed confirms an unknown / empty op misses
// (the explicit-class requirement then stands — unchanged behavior).
func TestDDLCache_ClassForCommand_Unindexed(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if class, ok := cache.ClassForCommand("NoSuchOp"); ok {
		t.Fatalf("unknown op must miss; got %q", class)
	}
	if _, ok := cache.ClassForCommand(""); ok {
		t.Fatalf("empty op must miss")
	}
}

// TestDDLCache_Invalidate_AddingSecondAdmitterEvictsFromIndex is the U1 RED-GREEN
// for the dynamic ambiguity transition on ADD: an op admitted by ONE vertexType
// DDL is indexed (resolves); after an Invalidate brings a SECOND vertexType DDL
// that also admits it into the cache, the global ambiguity guard must evict the
// op from the index (ClassForCommand now MISSES). The whole-index rebuild on
// Invalidate makes this work — this locks it.
func TestDDLCache_Invalidate_AddingSecondAdmitterEvictsFromIndex(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())

	// One vertexType DDL admits DoDynamic — initially unambiguous.
	firstKey := "vtx.meta.dynfirst"
	seedMetaDDL(t, ctx, conn, firstKey, "meta.ddl.vertexType", "dynfirst",
		[]string{"DoDynFirst", "DoDynamic"})
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// GREEN before: the op resolves to its sole owner.
	if class, ok := cache.ClassForCommand("DoDynamic"); !ok || class != "dynfirst" {
		t.Fatalf("before second admitter, DoDynamic resolved ok=%v class=%q, want dynfirst", ok, class)
	}

	// A second vertexType DDL that ALSO admits DoDynamic arrives + is invalidated in.
	secondKey := "vtx.meta.dynsecond"
	seedMetaDDL(t, ctx, conn, secondKey, "meta.ddl.vertexType", "dynsecond",
		[]string{"DoDynSecond", "DoDynamic"})
	if err := cache.Invalidate(ctx, secondKey); err != nil {
		t.Fatalf("Invalidate (add second admitter): %v", err)
	}

	// RED→guarded: DoDynamic is now admitted by TWO vertexType DDLs → evicted from
	// the index (the explicit-class requirement stands). The disjoint ops still
	// resolve.
	if class, ok := cache.ClassForCommand("DoDynamic"); ok {
		t.Fatalf("after second admitter, DoDynamic must MISS (ambiguous); got %q", class)
	}
	if class, ok := cache.ClassForCommand("DoDynFirst"); !ok || class != "dynfirst" {
		t.Fatalf("DoDynFirst resolved ok=%v class=%q, want dynfirst", ok, class)
	}
	if class, ok := cache.ClassForCommand("DoDynSecond"); !ok || class != "dynsecond" {
		t.Fatalf("DoDynSecond resolved ok=%v class=%q, want dynsecond", ok, class)
	}
}

// TestDDLCache_Invalidate_RemovingOneAdmitterReindexes is the U1 RED-GREEN for the
// dynamic ambiguity transition on REMOVE: an op admitted by TWO vertexType DDLs is
// NOT indexed (ambiguous); after an Invalidate tombstones one of the two admitters,
// the remaining single owner makes the op unambiguous again and ClassForCommand
// resolves. The complement of the ADD case — both rely on the whole-index rebuild.
func TestDDLCache_Invalidate_RemovingOneAdmitterReindexes(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())

	// Two vertexType DDLs both admit DoContested — ambiguous from the start.
	keepKey := "vtx.meta.keepowner"
	dropKey := "vtx.meta.dropowner"
	seedMetaDDL(t, ctx, conn, keepKey, "meta.ddl.vertexType", "keepowner",
		[]string{"DoKeep", "DoContested"})
	seedMetaDDL(t, ctx, conn, dropKey, "meta.ddl.vertexType", "dropowner",
		[]string{"DoDrop", "DoContested"})
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// RED before: the contested op misses (two admitters).
	if class, ok := cache.ClassForCommand("DoContested"); ok {
		t.Fatalf("before removal, DoContested must MISS (ambiguous); got %q", class)
	}

	// Tombstone one admitter and invalidate it out.
	dead := []byte(`{"class":"meta.ddl.vertexType","isDeleted":true,"data":{}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, dropKey, dead); err != nil {
		t.Fatalf("tombstone dropowner: %v", err)
	}
	if err := cache.Invalidate(ctx, dropKey); err != nil {
		t.Fatalf("Invalidate (remove one admitter): %v", err)
	}

	// GREEN→re-indexed: only keepowner admits DoContested now → it resolves again.
	if class, ok := cache.ClassForCommand("DoContested"); !ok || class != "keepowner" {
		t.Fatalf("after removal, DoContested resolved ok=%v class=%q, want keepowner", ok, class)
	}
}

// seedMetaDDL writes a shadow-keyed meta-vertex DDL fixture (root carries class
// + data.canonicalName + data.permittedCommands).
func seedMetaDDL(t *testing.T, ctx context.Context, conn *substrate.Conn, key, metaClass, canonicalName string, permittedCommands []string) {
	t.Helper()
	doc := map[string]any{
		"class":     metaClass,
		"isDeleted": false,
		"data": map[string]any{
			"canonicalName":     canonicalName,
			"permittedCommands": permittedCommands,
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal meta ddl %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, key, b); err != nil {
		t.Fatalf("seed meta ddl %s: %v", key, err)
	}
}

func TestDDLCache_Invalidate_AspectKeyResolvesToRoot(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// Invalidate via an aspect key — should derive root.
	if err := cache.Invalidate(ctx, "vtx.meta.identity.permittedCommands"); err != nil {
		t.Fatalf("Invalidate via aspect: %v", err)
	}
	if _, ok := cache.Lookup("identity"); !ok {
		t.Fatalf("identity should still be present after aspect invalidate")
	}
}
