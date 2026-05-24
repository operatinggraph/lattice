package graph

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// TestGraphRead_HappyPath verifies that KVGet correctly retrieves a key
// written to Core KV — the same underlying operation as lattice graph read.
func TestGraphRead_HappyPath(t *testing.T) {
	ctx, conn := setupGraphEnv(t)

	testKey := "vtx.identity.testGraphReadKey0001"
	testVal := map[string]interface{}{
		"key":       testKey,
		"class":     "identity",
		"isDeleted": false,
	}
	data, _ := json.Marshal(testVal)
	if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, testKey, data); err != nil {
		t.Fatalf("KVPut: %v", err)
	}

	entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, testKey)
	if err != nil {
		t.Fatalf("KVGet: %v", err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(entry.Value, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["key"] != testKey {
		t.Errorf("key = %q, want %q", got["key"], testKey)
	}
}

// TestGraphKeys_HappyPath verifies that KVListKeys returns keys filtered
// by prefix — the same underlying operation as lattice graph keys.
func TestGraphKeys_HappyPath(t *testing.T) {
	ctx, conn := setupGraphEnv(t)

	prefix := "vtx.identity."
	keys := []string{
		prefix + "graphKeyTest00001",
		prefix + "graphKeyTest00002",
		"vtx.role.graphRoleKey000001",
	}
	for _, k := range keys {
		val := map[string]interface{}{"key": k, "class": "test", "isDeleted": false}
		data, _ := json.Marshal(val)
		if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, k, data); err != nil {
			t.Fatalf("KVPut %s: %v", k, err)
		}
	}

	allKeys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}

	count := 0
	for _, k := range allKeys {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			count++
		}
	}
	if count < 2 {
		t.Errorf("expected at least 2 identity keys, got %d", count)
	}
}

func setupGraphEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "graph-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)
	return ctx, conn
}
