package lens

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// seedMetaVertex writes a meta-vertex root in the shape the Processor
// actually commits: an envelope whose `data` is empty, with the canonical
// name on its own `.canonicalName` aspect carrying `data.value`
// (Contract #1). A fixture that puts the name on the root instead proves only
// itself — no such document exists in Core KV.
func seedMetaVertex(ctx context.Context, t *testing.T, conn *substrate.Conn, id, class, name string, deleted bool) string {
	t.Helper()
	key := "vtx.meta." + id
	root, _ := json.Marshal(map[string]interface{}{
		"key":       key,
		"class":     class,
		"isDeleted": deleted,
		"data":      map[string]interface{}{},
	})
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, key, root); err != nil {
		t.Fatalf("KVPut %s: %v", key, err)
	}
	if name == "" {
		return key
	}
	aspect, _ := json.Marshal(map[string]interface{}{
		"key":       key + ".canonicalName",
		"class":     "canonicalName",
		"isDeleted": false,
		"data":      map[string]interface{}{"value": name},
	})
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, key+".canonicalName", aspect); err != nil {
		t.Fatalf("KVPut %s.canonicalName: %v", key, err)
	}
	return key
}

func findLens(lenses []lensEntry, key string) *lensEntry {
	for i := range lenses {
		if lenses[i].Key == key {
			return &lenses[i]
		}
	}
	return nil
}

// TestLensList_NamesEachLensFromItsAspect exercises the production listing
// path, not a copy of it: a lens is identified by envelope class and named
// from its canonicalName aspect.
func TestLensList_NamesEachLensFromItsAspect(t *testing.T) {
	ctx, conn := setupLensEnv(t)

	lensKey := seedMetaVertex(ctx, t, conn, "AbCdEfGhJkMnPqRsTuVw", "meta.lens", "lens.contract-view", false)
	ddlKey := seedMetaVertex(ctx, t, conn, "BcDeFgHjKmNpQrStUvWx", "meta.ddl", "ddl.contract", false)

	// A non-meta vertex — excluded by prefix, not by class.
	roleDoc, _ := json.Marshal(map[string]interface{}{"key": "vtx.role.CdEfGhJkMnPqRsTuVwXy", "class": "role"})
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, "vtx.role.CdEfGhJkMnPqRsTuVwXy", roleDoc); err != nil {
		t.Fatalf("KVPut role: %v", err)
	}

	lenses, err := collectLenses(ctx, conn)
	if err != nil {
		t.Fatalf("collectLenses: %v", err)
	}

	match := findLens(lenses, lensKey)
	if match == nil {
		t.Fatalf("test-created lens %q not found among %d lenses", lensKey, len(lenses))
	}
	if match.CanonicalName != "lens.contract-view" {
		t.Errorf("canonicalName = %q, want lens.contract-view", match.CanonicalName)
	}
	if match.IsDeleted {
		t.Error("lens must not report as deleted")
	}
	if findLens(lenses, ddlKey) != nil {
		t.Error("a meta.ddl vertex must not be listed as a lens")
	}
}

// TestLensList_UnnamedLensStillListed pins the fallback: the canonical name
// is display metadata, so a lens whose aspect is missing or tombstoned is
// still listed — just unnamed.
func TestLensList_UnnamedLensStillListed(t *testing.T) {
	ctx, conn := setupLensEnv(t)

	noAspect := seedMetaVertex(ctx, t, conn, "AbCdEfGhJkMnPqRsTuVw", "meta.lens", "", false)
	tombstoned := seedMetaVertex(ctx, t, conn, "BcDeFgHjKmNpQrStUvWx", "meta.lens", "lens.retired", false)
	deadAspect, _ := json.Marshal(map[string]interface{}{
		"key":       tombstoned + ".canonicalName",
		"class":     "canonicalName",
		"isDeleted": true,
		"data":      map[string]interface{}{"value": "lens.retired"},
	})
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, tombstoned+".canonicalName", deadAspect); err != nil {
		t.Fatalf("KVPut tombstoned aspect: %v", err)
	}

	lenses, err := collectLenses(ctx, conn)
	if err != nil {
		t.Fatalf("collectLenses: %v", err)
	}

	for _, key := range []string{noAspect, tombstoned} {
		match := findLens(lenses, key)
		if match == nil {
			t.Fatalf("lens %q must still be listed without a name", key)
		}
		if match.CanonicalName != "" {
			t.Errorf("%s: canonicalName = %q, want empty", key, match.CanonicalName)
		}
	}
}

// TestLensLag_HappyPath verifies that lag entries are correctly filtered
// by the health.refractor.* prefix.
func TestLensLag_HappyPath(t *testing.T) {
	ctx, conn := setupLensEnv(t)

	// Seed a refractor health entry.
	lagKey := "health.refractor.lens.testLens.lag"
	lagDoc := map[string]interface{}{
		"lens": "testLens",
		"lagMs": 42,
	}
	data, _ := json.Marshal(lagDoc)
	if _, err := conn.KVPut(ctx, bootstrap.HealthKVBucket, lagKey, data); err != nil {
		t.Fatalf("KVPut lag: %v", err)
	}

	// Seed a non-refractor health entry (should be excluded).
	otherKey := "health.processor.test.heartbeat"
	otherDoc := map[string]interface{}{"ping": true}
	otherData, _ := json.Marshal(otherDoc)
	if _, err := conn.KVPut(ctx, bootstrap.HealthKVBucket, otherKey, otherData); err != nil {
		t.Fatalf("KVPut other: %v", err)
	}

	allKeys, err := conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}

	refractorCount := 0
	for _, k := range allKeys {
		if strings.HasPrefix(k, "health.refractor.") {
			refractorCount++
		}
	}
	if refractorCount != 1 {
		t.Errorf("expected 1 refractor health entry, got %d", refractorCount)
	}
}

func setupLensEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "lens-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)
	return ctx, conn
}
