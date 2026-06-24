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

// TestHealthSummary_Rollup_AllGreen exercises the rollup logic when all
// components are healthy and within the stale threshold.
func TestHealthSummary_Rollup_AllGreen(t *testing.T) {
	now := time.Now().UTC()
	processorInstance := "proc-test01"
	refractorInstance := "rfx-test01"
	lensID := "lens0000000000000"

	heartbeatAt := now.Add(-5 * time.Second).Format(time.RFC3339)

	docs := map[string]map[string]any{
		"health.processor." + processorInstance: {
			"key":         "health.processor." + processorInstance,
			"component":   "processor",
			"instance":    processorInstance,
			"status":      "healthy",
			"heartbeatAt": heartbeatAt,
			"metrics": map[string]any{
				"ops_consumed_total":  float64(100),
				"ops_committed_total": float64(99),
			},
		},
		"health.refractor." + refractorInstance: {
			"key":         "health.refractor." + refractorInstance,
			"component":   "refractor",
			"instance":    refractorInstance,
			"status":      "healthy",
			"heartbeatAt": heartbeatAt,
			"metrics": map[string]any{
				"lensLags": map[string]any{"capability": float64(0)},
			},
		},
		lensID: {
			"ruleId":      lensID,
			"status":      "active",
			"consumerLag": float64(0),
			"errorCount":  float64(0),
		},
		"health.bootstrap.complete": {
			"status":      "complete",
			"completedAt": heartbeatAt,
		},
	}

	allKeys := make([]string, 0, len(docs))
	for k := range docs {
		allKeys = append(allKeys, k)
	}

	readFn := func(k string) (map[string]any, bool) {
		d, ok := docs[k]
		return d, ok
	}

	rollup, overallLevel := computeSummaryRollup(allKeys, readFn, 60*time.Second)

	if overallLevel != rollupGreen {
		t.Errorf("overall = %v, want GREEN", overallLevel)
	}
	if rollup.Overall != "green" {
		t.Errorf("rollup.Overall = %q, want \"green\"", rollup.Overall)
	}

	// Every component row should be green or active.
	for _, row := range rollup.Components {
		if row.level == rollupRed {
			t.Errorf("component %q has red status; want green or active", row.Component)
		}
		if row.level == rollupYellow {
			t.Errorf("component %q has yellow status; want green or active", row.Component)
		}
	}

	if len(rollup.Alerts) != 0 {
		t.Errorf("expected no alerts, got %v", rollup.Alerts)
	}
}

// TestHealthSummary_Rollup_StaleYellow exercises the rollup logic when a
// processor heartbeat is older than the stale threshold.
func TestHealthSummary_Rollup_StaleYellow(t *testing.T) {
	processorInstance := "proc-stale01"
	staleHeartbeat := time.Now().UTC().Add(-120 * time.Second).Format(time.RFC3339)

	docs := map[string]map[string]any{
		"health.processor." + processorInstance: {
			"key":         "health.processor." + processorInstance,
			"component":   "processor",
			"instance":    processorInstance,
			"status":      "healthy",
			"heartbeatAt": staleHeartbeat,
			"metrics": map[string]any{
				"ops_consumed_total":  float64(50),
				"ops_committed_total": float64(50),
			},
		},
		"health.bootstrap.complete": {
			"status":      "complete",
			"completedAt": staleHeartbeat,
		},
	}

	allKeys := []string{
		"health.processor." + processorInstance,
		"health.bootstrap.complete",
	}

	readFn := func(k string) (map[string]any, bool) {
		d, ok := docs[k]
		return d, ok
	}

	rollup, overallLevel := computeSummaryRollup(allKeys, readFn, 60*time.Second)

	if overallLevel != rollupYellow {
		t.Errorf("overall = %v, want YELLOW (stale heartbeat)", overallLevel)
	}
	if rollup.Overall != "yellow" {
		t.Errorf("rollup.Overall = %q, want \"yellow\"", rollup.Overall)
	}

	// The processor row should be stale.
	found := false
	for _, row := range rollup.Components {
		if strings.Contains(row.Component, processorInstance) {
			found = true
			if row.Status != "stale" {
				t.Errorf("processor row status = %q, want \"stale\"", row.Status)
			}
		}
	}
	if !found {
		t.Error("processor heartbeat row not found in rollup components")
	}
}

// TestClassifyKey_WeaverLoom verifies Weaver/Loom heartbeat and event keys are
// classified distinctly. Regression: they previously fell through to "lens" and
// were never staleness-checked in the rollup.
func TestClassifyKey_WeaverLoom(t *testing.T) {
	cases := []struct{ key, want string }{
		{"health.weaver.wvr-abc", "weaver-heartbeat"},
		{"health.weaver.wvr-abc.detail", "weaver-event"},
		{"health.loom.lm-abc", "loom-heartbeat"},
		{"health.loom.lm-abc.detail", "loom-event"},
		{"health.processor.proc-abc", "processor-heartbeat"},
		{"someBareLensNanoID", "lens"},
	}
	for _, c := range cases {
		if got := classifyKey(c.key); got != c.want {
			t.Errorf("classifyKey(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestHealthSummary_Rollup_WeaverLoom verifies Weaver/Loom heartbeats drive the
// rollup: a stale heartbeat → yellow, an inline error issue → red, an inline
// warning issue (fresh) → yellow, and a healthy pair → green.
func TestHealthSummary_Rollup_WeaverLoom(t *testing.T) {
	now := time.Now().UTC()
	fresh := now.Add(-5 * time.Second).Format(time.RFC3339)
	stale := now.Add(-120 * time.Second).Format(time.RFC3339)

	t.Run("AllGreen", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.weaver.wvr-01":      {"heartbeatAt": fresh, "metrics": map[string]any{"targets": float64(3)}, "issues": []any{}},
			"health.loom.lm-01":         {"heartbeatAt": fresh, "metrics": map[string]any{"runningInstances": float64(2)}, "issues": []any{}},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupGreen {
			t.Errorf("overall = %v, want GREEN", level)
		}
	})

	t.Run("StaleLoomYellow", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.loom.lm-02":         {"heartbeatAt": stale},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupYellow {
			t.Errorf("overall = %v, want YELLOW (stale loom)", level)
		}
	})

	t.Run("WeaverErrorRed", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.weaver.wvr-03": {"heartbeatAt": fresh, "issues": []any{
				map[string]any{"severity": "error", "code": "X", "message": "boom"},
			}},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupRed {
			t.Errorf("overall = %v, want RED (weaver error issue)", level)
		}
	})

	t.Run("WeaverWarningYellow", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.weaver.wvr-04": {"heartbeatAt": fresh, "issues": []any{
				map[string]any{"severity": "warning", "code": "ConsumerPaused", "message": "paused"},
			}},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupYellow {
			t.Errorf("overall = %v, want YELLOW (weaver warning issue)", level)
		}
	})
}

// rollupOf computes the overall rollup level for a doc set (test helper).
func rollupOf(t *testing.T, docs map[string]map[string]any) rollupLevel {
	t.Helper()
	allKeys := make([]string, 0, len(docs))
	for k := range docs {
		allKeys = append(allKeys, k)
	}
	readFn := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	_, level := computeSummaryRollup(allKeys, readFn, 60*time.Second)
	return level
}

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
