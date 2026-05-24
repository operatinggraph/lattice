package health

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// TestHealthGates_HappyPath verifies that phase gate entries are correctly
// read from Health KV.
func TestHealthGates_HappyPath(t *testing.T) {
	ctx, conn := setupHealthEnv(t)

	gateKey := "health.gates.phase1.gate2"
	gateDoc := map[string]interface{}{
		"key":         gateKey,
		"passed":      true,
		"completedAt": "2026-05-01T10:00:00Z",
	}
	data, _ := json.Marshal(gateDoc)
	if _, err := conn.KVPut(ctx, bootstrap.HealthKVBucket, gateKey, data); err != nil {
		t.Fatalf("KVPut gate: %v", err)
	}

	allKeys, err := conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}

	gatePrefix := "health.gates.phase1."
	var gateKeys []string
	for _, k := range allKeys {
		if strings.HasPrefix(k, gatePrefix) {
			gateKeys = append(gateKeys, k)
		}
	}
	if len(gateKeys) == 0 {
		t.Fatal("expected at least 1 gate key")
	}

	entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, gateKey)
	if err != nil {
		t.Fatalf("KVGet gate: %v", err)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if doc["passed"] != true {
		t.Errorf("passed = %v, want true", doc["passed"])
	}
}

// TestHealthSummary_HappyPath verifies that health entries can be listed
// from Health KV.
func TestHealthSummary_HappyPath(t *testing.T) {
	ctx, conn := setupHealthEnv(t)

	// Seed several health entries.
	entries := map[string]interface{}{
		"health.processor.test.heartbeat": map[string]interface{}{"ping": true},
		"health.refractor.test.lag":       map[string]interface{}{"lagMs": 10},
		"health.bootstrap.complete":        map[string]interface{}{"ok": true},
	}
	for k, v := range entries {
		data, _ := json.Marshal(v)
		if _, err := conn.KVPut(ctx, bootstrap.HealthKVBucket, k, data); err != nil {
			t.Fatalf("KVPut %s: %v", k, err)
		}
	}

	allKeys, err := conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}
	if len(allKeys) < len(entries) {
		t.Errorf("expected at least %d keys, got %d", len(entries), len(allKeys))
	}
}

func setupHealthEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "health-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)
	return ctx, conn
}
