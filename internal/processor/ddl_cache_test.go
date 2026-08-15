package processor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

func TestDDLCache_RefreshAndLookup_ShadowKey(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestDDLCache_LoadMetaVertex_SensitiveTombstoneNotHonored pins the sensitive
// reader's deliberate asymmetry with its permittedCommands/custody/script
// siblings: a tombstoned sensitive aspect that still declares true is NOT
// read as absent. Step 8's tombstone arm copies the prior document whole and
// only flips isDeleted (the same shape TestDDLCache_TombstonedCanonicalNameRetiresTheEntry
// exercises for canonicalName), so this fixture tombstones a sensitive
// declaration that was true and confirms the class stays sensitive — the
// posture ddl_cache.go's sensitive-aspect comment states.
func TestDDLCache_LoadMetaVertex_SensitiveTombstoneNotHonored(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	const nanoID = "Aa1Bb2Cc3Dd4Ee5Ff6Gg"
	root := "vtx.meta." + nanoID
	rootDoc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, rootDoc); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	cn := []byte(`{"class":"canonicalName","isDeleted":false,"data":{"value":"withdrawnsensitive"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".canonicalName", cn); err != nil {
		t.Fatalf("seed canonicalName: %v", err)
	}
	dead := []byte(`{"class":"sensitive","isDeleted":true,"data":{"value":true}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".sensitive", dead); err != nil {
		t.Fatalf("seed tombstoned sensitive: %v", err)
	}

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	ref, ok, err := cache.loadMetaVertex(ctx, root, nil)
	if err != nil {
		t.Fatalf("loadMetaVertex: %v", err)
	}
	if !ok {
		t.Fatalf("meta-vertex must still load (only the sensitive aspect is tombstoned)")
	}
	if !ref.Sensitive {
		t.Fatalf("a tombstoned sensitive aspect that declared true must NOT be honored as a withdrawal; got Sensitive=false")
	}
}

// TestDDLCache_LoadMetaVertex_SensitiveFalseWhenLive is the positive vector
// for the tombstone test above: a LIVE sensitive:false aspect must read as
// false, proving the fixture and reader actually reach the aspect (rather
// than the tombstone test passing because nothing was ever read at all).
func TestDDLCache_LoadMetaVertex_SensitiveFalseWhenLive(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	const nanoID = "Hh7Jj8Kk9Mm1Nn2Pp3Qq"
	root := "vtx.meta." + nanoID
	rootDoc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, rootDoc); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	cn := []byte(`{"class":"canonicalName","isDeleted":false,"data":{"value":"liveinsensitive"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".canonicalName", cn); err != nil {
		t.Fatalf("seed canonicalName: %v", err)
	}
	live := []byte(`{"class":"sensitive","isDeleted":false,"data":{"value":false}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".sensitive", live); err != nil {
		t.Fatalf("seed live sensitive: %v", err)
	}

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	ref, ok, err := cache.loadMetaVertex(ctx, root, nil)
	if err != nil {
		t.Fatalf("loadMetaVertex: %v", err)
	}
	if !ok {
		t.Fatalf("meta-vertex must load")
	}
	if ref.Sensitive {
		t.Fatalf("a live sensitive:false aspect must read as false; got Sensitive=true")
	}
}

// TestDDLCache_LoadMetaVertex_SensitiveUnparseableFailsClosed pins the third
// outcome the reader's asymmetry comment (ddl_cache.go, above the read)
// argues for but the code did not yet enforce: a data.value that decodes to
// the wrong JSON type (here a string, not a bool) must NOT silently leave
// Sensitive at its zero value — that would skip step 6.5 encryption for a
// class whose declaration exists but could not be read, the same fail-open
// shape §17's mutation-time gate closes for future writes, but for a
// pre-existing or write-path-bypassing malformed record already at rest.
// The read site now poisons toward true instead, mirroring the custody
// reader's poison-on-unparseable posture a few lines below in this file.
func TestDDLCache_LoadMetaVertex_SensitiveUnparseableFailsClosed(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	const nanoID = "Rr4Ss5Tt6Uu7Vv8Ww9Xx"
	root := "vtx.meta." + nanoID
	rootDoc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, rootDoc); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	cn := []byte(`{"class":"canonicalName","isDeleted":false,"data":{"value":"unparseablesensitive"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".canonicalName", cn); err != nil {
		t.Fatalf("seed canonicalName: %v", err)
	}
	malformed := []byte(`{"class":"sensitive","isDeleted":false,"data":{"value":"true"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".sensitive", malformed); err != nil {
		t.Fatalf("seed malformed sensitive: %v", err)
	}

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	ref, ok, err := cache.loadMetaVertex(ctx, root, nil)
	if err != nil {
		t.Fatalf("loadMetaVertex: %v", err)
	}
	if !ok {
		t.Fatalf("meta-vertex must still load (only the sensitive aspect is malformed)")
	}
	if !ref.Sensitive {
		t.Fatalf("an unparseable sensitive aspect must fail closed to Sensitive=true; got false")
	}
}

// TestDDLCache_LoadMetaVertex_SensitiveMissingValueFailsClosed is the second-
// review finding: a well-typed aspect with NO data.value (or an explicit
// JSON null) decodes without error, so the unparseable-only gate above would
// leave it at the zero value (false) — but pkgmgr's only legitimate writer
// (build.go) never creates this aspect for the false case at all (absence of
// the whole aspect IS how "not sensitive" is encoded), so a present aspect
// with no value is exactly as undecidable as a malformed one. Must also fail
// closed.
func TestDDLCache_LoadMetaVertex_SensitiveMissingValueFailsClosed(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	const nanoID = "Yy1Zz2Aa3Bb4Cc5Dd6Ee"
	root := "vtx.meta." + nanoID
	rootDoc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, rootDoc); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	cn := []byte(`{"class":"canonicalName","isDeleted":false,"data":{"value":"missingvaluesensitive"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".canonicalName", cn); err != nil {
		t.Fatalf("seed canonicalName: %v", err)
	}
	noValue := []byte(`{"class":"sensitive","isDeleted":false,"data":{}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".sensitive", noValue); err != nil {
		t.Fatalf("seed valueless sensitive: %v", err)
	}

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	ref, ok, err := cache.loadMetaVertex(ctx, root, nil)
	if err != nil {
		t.Fatalf("loadMetaVertex: %v", err)
	}
	if !ok {
		t.Fatalf("meta-vertex must still load (only the sensitive aspect is missing its value)")
	}
	if !ref.Sensitive {
		t.Fatalf("a sensitive aspect present with no data.value must fail closed to Sensitive=true; got false")
	}
}

// TestDDLCache_Invalidate_OpMetaFailureLatchesDegraded covers the first of
// Invalidate's two error returns: a root whose bytes no decoder can read fails
// in the op-meta load, before the DDL load is ever reached. The cache has then
// missed a durable commit, and Degraded is the only record of it — the caller
// (step 8) has already committed and cannot retry.
func TestDDLCache_Invalidate_OpMetaFailureLatchesDegraded(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if degraded, _, _, _ := cache.Degraded(); degraded {
		t.Fatalf("a freshly refreshed cache must not be degraded")
	}

	key := "vtx.meta.Dd4Ee5Ff6Gg7Hh8Jj9Kk"
	if _, err := conn.KVPut(ctx, testCoreBucket, key, []byte(`{"class":`)); err != nil {
		t.Fatalf("seed unparseable root: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err == nil {
		t.Fatalf("Invalidate must fail on an unparseable op-meta root")
	}

	degraded, since, failedKey, err := cache.Degraded()
	if !degraded {
		t.Fatalf("a failed Invalidate must latch degraded")
	}
	if since.IsZero() {
		t.Fatalf("degraded since = zero, want the moment of the failure")
	}
	if failedKey != key {
		t.Fatalf("degraded key = %q, want %q", failedKey, key)
	}
	if err == nil {
		t.Fatalf("degraded err = nil, want the invalidation failure")
	}
}

// TestDDLCache_Invalidate_MetaVertexFailureLatchesDegraded covers Invalidate's
// SECOND error return. The fixture is the one loadMetaVertex's own decoder
// comment names: a non-string `class` decodes cleanly in the op-meta loader
// (which never binds the field) and fails only in the DDL loader, so this
// reaches the second return without tripping the first.
func TestDDLCache_Invalidate_MetaVertexFailureLatchesDegraded(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	key := "vtx.meta.Mm1Nn2Pp3Qq4Rr5Ss6T"
	if _, err := conn.KVPut(ctx, testCoreBucket, key, []byte(`{"class":123,"isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed root with a non-string class: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err == nil {
		t.Fatalf("Invalidate must fail on a meta-vertex root it cannot parse")
	}

	degraded, _, failedKey, err := cache.Degraded()
	if !degraded {
		t.Fatalf("a failed meta-vertex load must latch degraded")
	}
	if failedKey != key {
		t.Fatalf("degraded key = %q, want %q", failedKey, key)
	}
	if err == nil {
		t.Fatalf("degraded err = nil, want the invalidation failure")
	}
}

// TestDDLCache_Invalidate_SuccessLeavesDegradedUnset is the positive vector for
// the two tests above: the same Invalidate call shape, against a root that
// loads, must leave the latch alone — proving they fail on the failure and not
// merely on having called Invalidate at all.
func TestDDLCache_Invalidate_SuccessLeavesDegradedUnset(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	key := "vtx.meta.healthyclass"
	doc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"healthyclass","permittedCommands":["DoHealthy"]}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if degraded, _, _, _ := cache.Degraded(); degraded {
		t.Fatalf("a successful Invalidate must not latch degraded")
	}
}

// TestDDLCache_Degraded_NeverClearsOnLaterSuccess pins the deliberate stickiness.
// A later root's successful Invalidate proves Core KV answers again — it does
// NOT load the root that was missed, whose canonicalName is exactly what the
// cache still does not know — so trust in the whole projection is not restored
// and the latch must survive it. Only a restart (a fresh Refresh) clears it.
func TestDDLCache_Degraded_NeverClearsOnLaterSuccess(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	badKey := "vtx.meta.Uu7Vv8Ww9Xx1Yy2Zz3A"
	if _, err := conn.KVPut(ctx, testCoreBucket, badKey, []byte(`{"class":`)); err != nil {
		t.Fatalf("seed unparseable root: %v", err)
	}
	if err := cache.Invalidate(ctx, badKey); err == nil {
		t.Fatalf("Invalidate must fail on an unparseable root")
	}
	_, sinceBefore, _, _ := cache.Degraded()

	// An unrelated, healthy root invalidates successfully afterwards.
	goodKey := "vtx.meta.laterclass"
	doc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"laterclass","permittedCommands":["DoLater"]}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, goodKey, doc); err != nil {
		t.Fatalf("seed later root: %v", err)
	}
	if err := cache.Invalidate(ctx, goodKey); err != nil {
		t.Fatalf("Invalidate (later, healthy root): %v", err)
	}
	if _, ok := cache.Lookup("laterclass"); !ok {
		t.Fatalf("the later root must be served — the fixture never proved a success happened")
	}

	degraded, since, failedKey, _ := cache.Degraded()
	if !degraded {
		t.Fatalf("degraded must survive a later unrelated success")
	}
	if failedKey != badKey {
		t.Fatalf("degraded key = %q, want the root that actually failed (%q)", failedKey, badKey)
	}
	if !since.Equal(sinceBefore) {
		t.Fatalf("degraded since moved from %v to %v; the onset must not be re-dated", sinceBefore, since)
	}
}

// TestDDLCache_Invalidate_NonMetaKeyLatchesDegraded covers Invalidate's third
// error return. Step 8 filters on the `vtx.meta.>` prefix, so nothing reaches it
// today; it latches for the same reason as the other two, so a future caller
// with a wrong filter cannot reopen the silent divergence.
func TestDDLCache_Invalidate_NonMetaKeyLatchesDegraded(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := cache.Invalidate(ctx, "vtx.identity."+testNanoID2); err == nil {
		t.Fatalf("Invalidate must reject a non-meta key")
	}
	if degraded, _, _, _ := cache.Degraded(); !degraded {
		t.Fatalf("a rejected invalidation must latch degraded")
	}
}

// TestDDLCache_Invalidate_FailureFlagsPriorEntryStale is the UPDATE half of the
// latch. A failing Invalidate returns before it touches byRoot, so the root's
// prior entry keeps being served — under a canonicalName whose declaration Core
// KV has already changed. The name is flagged so a consumer can tell that HIT
// apart from a trustworthy one; every other name stays clean, which is what
// keeps the fail-closed reaction narrow.
func TestDDLCache_Invalidate_FailureFlagsPriorEntryStale(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())

	key := "vtx.meta.staleclass"
	live := []byte(`{"class":"meta.ddl.aspectType","isDeleted":false,"data":{"canonicalName":"staleclass","sensitive":false}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, live); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if cache.PossiblyStale("staleclass") {
		t.Fatalf("a freshly refreshed entry must not be flagged stale")
	}

	// The root's next invalidation cannot be loaded.
	if _, err := conn.KVPut(ctx, testCoreBucket, key, []byte(`{"class":123,"isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed unreadable root: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err == nil {
		t.Fatalf("Invalidate must fail on a root it cannot parse")
	}

	if ref, ok := cache.Lookup("staleclass"); !ok || ref.Sensitive {
		t.Fatalf("the prior entry must survive the failure (ok=%v sensitive=%v) — that is why the name is flagged", ok, ref.Sensitive)
	}
	if !cache.PossiblyStale("staleclass") {
		t.Fatalf("the failed root's canonicalName must be flagged possibly stale")
	}
	if cache.PossiblyStale("identity") {
		t.Fatalf("an unrelated class must not be flagged by another root's failure")
	}
	if cache.PossiblyStale("") {
		t.Fatalf("the empty name must never be flagged")
	}
}

// TestDDLCache_Invalidate_FailureOnUnknownRootFlagsNothing is the complement: a
// root the cache never held strands no entry, so the failure raises the latch —
// the class it would have declared is now missing — without flagging any name as
// stale.
func TestDDLCache_Invalidate_FailureOnUnknownRootFlagsNothing(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	key := "vtx.meta.Ee5Ff6Gg7Hh8Jj9Kk1Mm"
	if _, err := conn.KVPut(ctx, testCoreBucket, key, []byte(`{"class":123,"isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed unreadable root: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err == nil {
		t.Fatalf("Invalidate must fail")
	}
	if degraded, _, _, _ := cache.Degraded(); !degraded {
		t.Fatalf("the latch must still be raised")
	}
	if cache.PossiblyStale("identity") {
		t.Fatalf("a root with no prior entry must flag no canonicalName")
	}
}

// TestDDLCache_Invalidate_FailuresAccumulateAcrossRoots: two roots failing must
// leave BOTH names flagged. The guard depends on the recording sitting above
// markDegraded's already-degraded early return, and a reorder would strand the
// second root's entry with every other test still green.
func TestDDLCache_Invalidate_FailuresAccumulateAcrossRoots(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())

	rootA, rootB := "vtx.meta.accumfirst", "vtx.meta.accumsecond"
	for _, seed := range []struct{ key, name string }{{rootA, "accumfirst"}, {rootB, "accumsecond"}} {
		doc := []byte(`{"class":"meta.ddl.aspectType","isDeleted":false,"data":{"canonicalName":"` + seed.name + `","sensitive":false}}`)
		if _, err := conn.KVPut(ctx, testCoreBucket, seed.key, doc); err != nil {
			t.Fatalf("seed %s: %v", seed.key, err)
		}
	}
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	unreadable := []byte(`{"class":123,"isDeleted":false,"data":{}}`)
	for _, root := range []string{rootA, rootB} {
		if _, err := conn.KVPut(ctx, testCoreBucket, root, unreadable); err != nil {
			t.Fatalf("seed unreadable %s: %v", root, err)
		}
		if err := cache.Invalidate(ctx, root); err == nil {
			t.Fatalf("Invalidate %s must fail", root)
		}
	}

	if !cache.PossiblyStale("accumfirst") {
		t.Fatalf("the FIRST failure's name must stay flagged after a second failure")
	}
	if !cache.PossiblyStale("accumsecond") {
		t.Fatalf("the second failure's name must be flagged too")
	}
}

// TestDDLCache_Invalidate_SuccessClearsItsOwnStaleFlag: a root's own successful
// reload proves its entry current again, so its stale flag is withdrawn — one
// transient blip must not refuse that class's sensitive writes until a restart.
// The process-wide latch is untouched: some OTHER root may still be unresolved,
// and only a full Refresh can speak for the whole projection.
func TestDDLCache_Invalidate_SuccessClearsItsOwnStaleFlag(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())

	key := "vtx.meta.recoverclass"
	live := []byte(`{"class":"meta.ddl.aspectType","isDeleted":false,"data":{"canonicalName":"recoverclass","sensitive":false}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, live); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, key, []byte(`{"class":123,"isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed unreadable root: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err == nil {
		t.Fatalf("Invalidate must fail")
	}
	if !cache.PossiblyStale("recoverclass") {
		t.Fatalf("the failed root's name must be flagged first — the fixture proves nothing otherwise")
	}

	// The same root loads on a later invalidation.
	if _, err := conn.KVPut(ctx, testCoreBucket, key, live); err != nil {
		t.Fatalf("restore live root: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err != nil {
		t.Fatalf("Invalidate (recovered): %v", err)
	}
	if cache.PossiblyStale("recoverclass") {
		t.Fatalf("the flag must clear once THIS root's own reload succeeded")
	}
	if degraded, _, _, _ := cache.Degraded(); !degraded {
		t.Fatalf("the process-wide latch must stay set — only a Refresh can speak for the whole cache")
	}
}

// TestDDLCache_Invalidate_SuccessClearsTheFlaggedNameAfterRename is why the flag
// is keyed by ROOT. A root whose canonicalName changed between the failed and the
// successful load must withdraw the name that was FLAGGED, not the one it now
// serves — under name-keying the old name would stay refused forever, with
// nothing left in the cache able to name it.
func TestDDLCache_Invalidate_SuccessClearsTheFlaggedNameAfterRename(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())

	// A NanoID root, the real install shape: the canonicalName comes from the
	// document, so the rename is expressible (a shadow key would pin the name).
	key := "vtx.meta.Ff6Gg7Hh8Jj9Kk1Mm2Nn"
	before := []byte(`{"class":"meta.ddl.aspectType","isDeleted":false,"data":{"canonicalName":"oldname","sensitive":false}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, before); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, key, []byte(`{"class":123,"isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed unreadable root: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err == nil {
		t.Fatalf("Invalidate must fail")
	}
	if !cache.PossiblyStale("oldname") {
		t.Fatalf("the pre-failure name must be flagged")
	}

	after := []byte(`{"class":"meta.ddl.aspectType","isDeleted":false,"data":{"canonicalName":"newname","sensitive":false}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, after); err != nil {
		t.Fatalf("seed renamed root: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err != nil {
		t.Fatalf("Invalidate (renamed): %v", err)
	}
	if cache.PossiblyStale("oldname") {
		t.Fatalf("the flagged OLD name must be withdrawn by its own root's reload")
	}
	if cache.PossiblyStale("newname") {
		t.Fatalf("the name the root now serves was never flagged")
	}
}

// TestDDLCache_Refresh_ClearsDegradedLatch proves the field doc's own claim —
// "the only event that re-establishes full trust is a fresh Refresh" — is
// actually implemented, not just asserted in a comment. A full rescan that
// loads every root cleanly is exactly the trust a degraded latch withholds.
func TestDDLCache_Refresh_ClearsDegradedLatch(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())

	key := "vtx.meta.refreshclears"
	live := []byte(`{"class":"meta.ddl.aspectType","isDeleted":false,"data":{"canonicalName":"refreshclears","sensitive":false}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, live); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, key, []byte(`{"class":123,"isDeleted":false,"data":{}}`)); err != nil {
		t.Fatalf("seed unreadable root: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err == nil {
		t.Fatalf("Invalidate must fail")
	}
	if degraded, _, _, _ := cache.Degraded(); !degraded {
		t.Fatalf("the fixture must actually degrade the cache, or this test proves nothing")
	}
	if !cache.PossiblyStale("refreshclears") {
		t.Fatalf("the failed root's name must be flagged, or this test proves nothing")
	}

	// A later full Refresh loads every root cleanly (the fixture is repaired
	// first — Refresh itself refuses the whole rebuild on any load error, so a
	// Refresh that returns nil IS the "every root loaded" claim).
	if _, err := conn.KVPut(ctx, testCoreBucket, key, live); err != nil {
		t.Fatalf("restore live root: %v", err)
	}
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh (recovered): %v", err)
	}
	if degraded, _, _, _ := cache.Degraded(); degraded {
		t.Fatalf("a clean full Refresh must clear the process-wide latch")
	}
	if cache.PossiblyStale("refreshclears") {
		t.Fatalf("a clean full Refresh must clear every stale-name flag too")
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
	t.Parallel()
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

// TestDDLCache_TombstonedCanonicalNameRetiresTheEntry pins that a tombstoned
// canonicalName aspect stops being served. The name is the ONLY handle any
// consumer has on a meta-vertex — DDLs.Lookup is keyed by it — and step 8's
// tombstone arm retains the prior document whole rather than removing the key,
// so reading a deleted aspect as live would make a registration permanent: no
// second write exists that could ever retire the name. The root here carries a
// NanoID and no data.canonicalName, the real install shape, so once the aspect
// is gone the meta-vertex has no name from any source and drops out entirely.
func TestDDLCache_TombstonedCanonicalNameRetiresTheEntry(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	const nanoID = "Nn7Pp8Qq9Rr1Ss2Tt3Uu"
	root := "vtx.meta." + nanoID
	rootDoc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, rootDoc); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	live := []byte(`{"class":"canonicalName","isDeleted":false,"vertexKey":"` + root +
		`","localName":"canonicalName","data":{"value":"retireme"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".canonicalName", live); err != nil {
		t.Fatalf("seed canonicalName: %v", err)
	}

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok := cache.Lookup("retireme"); !ok {
		t.Fatalf("a live canonicalName aspect must be served — the fixture never registered")
	}

	// Step 8's tombstone arm copies the prior document and flips isDeleted.
	dead := []byte(`{"class":"canonicalName","isDeleted":true,"vertexKey":"` + root +
		`","localName":"canonicalName","data":{"value":"retireme"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".canonicalName", dead); err != nil {
		t.Fatalf("tombstone canonicalName: %v", err)
	}
	if err := cache.Invalidate(ctx, root+".canonicalName"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if ref, ok := cache.Lookup("retireme"); ok {
		t.Fatalf("a tombstoned canonicalName aspect must retire the entry, still serving %+v", ref)
	}
	if _, ok := cache.LookupByMetaKey(root); ok {
		t.Fatalf("the meta-vertex has no name from any source and must leave the index")
	}
}
