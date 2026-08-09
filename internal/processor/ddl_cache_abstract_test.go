package processor

import (
	"testing"
)

// TestDDLCache_Abstract_PopulatedFromDataAbstract pins dynamic-type-taxonomy-
// design.md §3.2: MetaVertexRef.Abstract is populated straight off the root
// document's `data.abstract`, the explicit marker — never derived from "a
// vertexType with no script".
func TestDDLCache_Abstract_PopulatedFromDataAbstract(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	key := "vtx.meta.abstractlocation"
	doc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"abstractlocation","abstract":true}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	ref, ok := cache.Lookup("abstractlocation")
	if !ok {
		t.Fatalf("abstractlocation DDL not in cache")
	}
	if !ref.Abstract {
		t.Errorf("Abstract = false, want true")
	}
}

// TestDDLCache_Abstract_NonBoolValueTreatedAsAbstract pins the fail-closed
// rule: a `data.abstract` value that is PRESENT but not a JSON bool (a
// malformed or unexpected write) resolves to Abstract == true, never false —
// false is the permissive direction for the two step-6 gates this field
// feeds, so an ambiguous marker must not silently take the permissive
// reading.
func TestDDLCache_Abstract_NonBoolValueTreatedAsAbstract(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	key := "vtx.meta.weirdabstract"
	doc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"weirdabstract","abstract":"true"}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := cache.Invalidate(ctx, key); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	ref, ok := cache.Lookup("weirdabstract")
	if !ok {
		t.Fatalf("weirdabstract DDL not in cache")
	}
	if !ref.Abstract {
		t.Errorf("Abstract = false for a non-bool data.abstract value, want true (fail-closed)")
	}
}

// TestDDLCache_Abstract_RealInstallShape pins the same Abstract-population
// behavior against the shape pkgmgr actually writes (build.go's
// abstractDDLRootData): a NanoID root document carrying data.abstract, a
// SEPARATE .canonicalName aspect, and no .script aspect at all — not the
// shadow-key, root-only fixture every other test in this file uses. The
// writer (internal/pkgmgr) and the reader (here) are joined by this test
// rather than by inspection alone.
func TestDDLCache_Abstract_RealInstallShape(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	const nanoID = "Bb2Cc3Dd4Ee5Ff6Gg7Hh"
	root := "vtx.meta." + nanoID
	rootDoc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"abstract":true}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, rootDoc); err != nil {
		t.Fatalf("seed root: %v", err)
	}
	cnDoc := []byte(`{"class":"canonicalName","isDeleted":false,"data":{"value":"reallocation"},"vertexKey":"` + root + `","localName":"canonicalName"}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root+".canonicalName", cnDoc); err != nil {
		t.Fatalf("seed canonicalName aspect: %v", err)
	}
	if err := cache.Invalidate(ctx, root); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	ref, ok := cache.Lookup("reallocation")
	if !ok {
		t.Fatalf("reallocation DDL not in cache")
	}
	if !ref.Abstract {
		t.Errorf("Abstract = false, want true (real install shape)")
	}
	if ref.ScriptSource != "" {
		t.Errorf("ScriptSource = %q, want empty (no .script aspect written for an abstract DDL)", ref.ScriptSource)
	}
}

// TestDDLCache_Abstract_AbsentDefaultsFalse pins the common case: a DDL that
// declares no `data.abstract` at all resolves to Abstract == false — an
// ordinary vertexType DDL is unaffected by this field's existence.
func TestDDLCache_Abstract_AbsentDefaultsFalse(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	// setupTestPipeline seeds vtx.meta.identity with no data.abstract.
	ref, ok := cache.Lookup("identity")
	if !ok {
		t.Fatalf("identity DDL not in cache")
	}
	if ref.Abstract {
		t.Errorf("identity: Abstract = true, want false (no data.abstract declared)")
	}
}
