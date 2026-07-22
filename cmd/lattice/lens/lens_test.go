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

// TestLensList_HappyPath verifies that the list logic correctly identifies
// meta.lens class vertices among Core KV keys.
func TestLensList_HappyPath(t *testing.T) {
	ctx, conn := setupLensEnv(t)

	// Seed a meta.lens vertex.
	lensKey := "vtx.meta.testLensNanoID0001234"
	lensDoc := map[string]interface{}{
		"key":       lensKey,
		"class":     "meta.lens",
		"isDeleted": false,
		"data": map[string]interface{}{
			"canonicalName": "testLens",
		},
	}
	data, _ := json.Marshal(lensDoc)
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, lensKey, data); err != nil {
		t.Fatalf("KVPut lens: %v", err)
	}

	// Seed a non-lens vertex (should be excluded).
	roleKey := "vtx.role.testRoleNanoID000001"
	roleDoc := map[string]interface{}{
		"key":   roleKey,
		"class": "role",
	}
	roleData, _ := json.Marshal(roleDoc)
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, roleKey, roleData); err != nil {
		t.Fatalf("KVPut role: %v", err)
	}

	allKeys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}

	// Replicate the list filter logic.
	var found []lensEntry
	for _, k := range allKeys {
		if !strings.HasPrefix(k, "vtx.meta.") {
			continue
		}
		parts := strings.Split(k, ".")
		if len(parts) != 3 {
			continue
		}
		entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, k)
		if err != nil {
			continue
		}
		var doc map[string]interface{}
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			continue
		}
		class, _ := doc["class"].(string)
		if class != "meta.lens" {
			continue
		}
		isDeleted, _ := doc["isDeleted"].(bool)
		found = append(found, lensEntry{
			Key:           k,
			CanonicalName: canonicalNameFromDoc(doc),
			IsDeleted:     isDeleted,
		})
	}

	var match *lensEntry
	for i := range found {
		if found[i].Key == lensKey {
			match = &found[i]
			break
		}
	}
	if match == nil {
		t.Fatalf("test-created lens %q not found among %d lenses", lensKey, len(found))
	}
	if match.CanonicalName != "testLens" {
		t.Errorf("canonicalName = %q, want testLens", match.CanonicalName)
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
