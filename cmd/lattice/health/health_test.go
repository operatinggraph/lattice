package health

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

// noneDeleted is the isLensDeleted stub for tests with no deactivated lens.
func noneDeleted(string) bool { return false }

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

	rollup, overallLevel := computeSummaryRollup(allKeys, readFn, 60*time.Second, noneDeleted)

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

// TestLagStalled pins the cold-bring-up-replay-debt distinction: a lens with
// no evidence of active draining (absent/unparseable lagProgressAt, or one
// past lagStallWindow) reads as stalled; one whose lag is still falling
// within the window does not.
func TestLagStalled(t *testing.T) {
	cases := []struct {
		name string
		doc  map[string]any
		want bool
	}{
		{"absent clock", map[string]any{}, true},
		{"unparseable clock", map[string]any{"lagProgressAt": "garbage"}, true},
		{"within the window", map[string]any{"lagProgressAt": time.Now().Add(-time.Second).Format(time.RFC3339)}, false},
		{"past the window", map[string]any{"lagProgressAt": time.Now().Add(-lagStallWindow - 5*time.Second).Format(time.RFC3339)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lagStalled(tc.doc); got != tc.want {
				t.Errorf("lagStalled(%v) = %v, want %v", tc.doc, got, tc.want)
			}
		})
	}
}

// TestHealthSummary_Rollup_DrainingLensNotYellow pins the cold-bring-up
// replay-debt fix at the rollup level: a lens with a large but actively
// falling consumerLag (lagProgressAt within the window) must not turn the
// summary yellow, while one whose lag has stopped falling still does.
func TestHealthSummary_Rollup_DrainingLensNotYellow(t *testing.T) {
	now := time.Now().UTC()
	drainingLens := "lensdraining00000"
	stalledLens := "lensstalled000000"

	bootstrapDoc := map[string]any{"status": "complete", "completedAt": now.Format(time.RFC3339)}

	docs := map[string]map[string]any{
		drainingLens: {
			"ruleId":        drainingLens,
			"status":        "active",
			"consumerLag":   float64(2483),
			"lagProgressAt": now.Add(-5 * time.Second).Format(time.RFC3339),
			"lastUpdated":   now.Format(time.RFC3339),
		},
		"health.bootstrap.complete": bootstrapDoc,
	}
	readFn := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	rollup, overallLevel := computeSummaryRollup([]string{drainingLens, "health.bootstrap.complete"}, readFn, 60*time.Second, noneDeleted)
	if overallLevel != rollupGreen {
		t.Errorf("draining lens: overall = %v, want GREEN", overallLevel)
	}
	if rollup.Components[0].Status != "active" {
		t.Errorf("draining lens: status = %q, want \"active\"", rollup.Components[0].Status)
	}

	docs = map[string]map[string]any{
		stalledLens: {
			"ruleId":        stalledLens,
			"status":        "active",
			"consumerLag":   float64(2483),
			"lagProgressAt": now.Add(-5 * time.Minute).Format(time.RFC3339),
			"lastUpdated":   now.Format(time.RFC3339),
		},
		"health.bootstrap.complete": bootstrapDoc,
	}
	rollup, overallLevel = computeSummaryRollup([]string{stalledLens, "health.bootstrap.complete"}, readFn, 60*time.Second, noneDeleted)
	if overallLevel != rollupYellow {
		t.Errorf("stalled lens: overall = %v, want YELLOW", overallLevel)
	}
	if rollup.Components[0].Status != "yellow" {
		t.Errorf("stalled lens: status = %q, want \"yellow\"", rollup.Components[0].Status)
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

	rollup, overallLevel := computeSummaryRollup(allKeys, readFn, 60*time.Second, noneDeleted)

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

// TestHealthSummary_Rollup_StaleLensRow is the regression test for
// lens-registry-restart-integrity-design.md §4 Fire B step 3: a per-lens
// reporter entry an unregistered pipeline stopped updating freezes
// status="active"/consumerLag=0 forever — exactly what looked "green" before
// this fix (Freshness rendered "-", no age evaluated at all). A frozen entry
// past staleThreshold must now read Status "stale" and a non-"-" Freshness,
// escalating the rollup, even though its own status/consumerLag fields still
// claim "active".
func TestHealthSummary_Rollup_StaleLensRow(t *testing.T) {
	lensID := "StaleLensRowTestId1"
	staleLastUpdated := time.Now().UTC().Add(-14 * time.Hour).Format(time.RFC3339)

	docs := map[string]map[string]any{
		lensID: {
			"ruleId":      lensID,
			"status":      "active",
			"consumerLag": float64(0),
			"errorCount":  float64(0),
			"lastUpdated": staleLastUpdated,
		},
		"health.bootstrap.complete": {
			"status":      "complete",
			"completedAt": staleLastUpdated,
		},
	}
	allKeys := []string{lensID, "health.bootstrap.complete"}
	readFn := func(k string) (map[string]any, bool) {
		d, ok := docs[k]
		return d, ok
	}

	rollup, overallLevel := computeSummaryRollup(allKeys, readFn, 60*time.Second, noneDeleted)

	if overallLevel != rollupYellow {
		t.Errorf("overall = %v, want YELLOW (a frozen lens row must escalate the rollup)", overallLevel)
	}

	found := false
	for _, row := range rollup.Components {
		if strings.Contains(row.Component, lensID) {
			found = true
			if row.Status != "stale" {
				t.Errorf("lens row status = %q, want \"stale\" (status=active/consumerLag=0 alone must not mask a frozen entry)", row.Status)
			}
			if row.Freshness == "-" || row.Freshness == "" {
				t.Errorf("lens row Freshness = %q, want a real age (e.g. \"...s ago\"), not \"-\"", row.Freshness)
			}
		}
	}
	if !found {
		t.Error("lens row not found in rollup components")
	}
}

// TestHealthSummary_Rollup_DeactivatedLensSuppressed pins the fix for a
// deactivated lens's frozen Health KV entry permanently pinning the rollup
// yellow (component-maintenance board row): `lens deactivate` tombstones the
// meta vertex but nothing deletes the per-lens reporter's last-written
// snapshot, so it ages past staleThreshold forever. A row whose lens the
// isLensDeleted probe reports tombstoned must be dropped entirely rather than
// scored stale, while an otherwise-identical frozen row for a still-live lens
// must still escalate (TestHealthSummary_Rollup_StaleLensRow).
func TestHealthSummary_Rollup_DeactivatedLensSuppressed(t *testing.T) {
	deactivatedLens := "DeactivatedLensTestId"
	staleLastUpdated := time.Now().UTC().Add(-14 * time.Hour).Format(time.RFC3339)

	docs := map[string]map[string]any{
		deactivatedLens: {
			"ruleId":      deactivatedLens,
			"status":      "active",
			"consumerLag": float64(0),
			"errorCount":  float64(0),
			"lastUpdated": staleLastUpdated,
		},
		"health.bootstrap.complete": {
			"status":      "complete",
			"completedAt": staleLastUpdated,
		},
	}
	allKeys := []string{deactivatedLens, "health.bootstrap.complete"}
	readFn := func(k string) (map[string]any, bool) {
		d, ok := docs[k]
		return d, ok
	}
	isLensDeleted := func(lensID string) bool { return lensID == deactivatedLens }

	rollup, overallLevel := computeSummaryRollup(allKeys, readFn, 60*time.Second, isLensDeleted)

	if overallLevel != rollupGreen {
		t.Errorf("overall = %v, want GREEN (a deactivated lens's frozen entry must not pin the rollup)", overallLevel)
	}
	for _, row := range rollup.Components {
		if strings.Contains(row.Component, deactivatedLens) {
			t.Errorf("deactivated lens row %+v present in rollup, want suppressed entirely", row)
		}
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

// TestHealthSummary_Rollup_RefractorProcessorIssues pins that the bespoke
// refractor-heartbeat / processor-heartbeat branches escalate on an inline
// issues[] error/warning exactly like the generic component-heartbeat branch
// does (TestHealthSummary_Rollup_WeaverLoom) — both components emit the same
// uniform {status, heartbeatAt, issues[]} body (e.g. refractor's
// LensRegistryIncomplete, lens-registry-restart-integrity-design.md §4 Fire
// B) through their own case, added only for the lensLags / ops_consumed
// Details formatting, and that case previously never read issues[] at all —
// a live example (2026-07-30, dev stack) sat "unhealthy" with an open error
// issue for 2.5h while `lattice health summary` printed the row "green".
func TestHealthSummary_Rollup_RefractorProcessorIssues(t *testing.T) {
	now := time.Now().UTC()
	fresh := now.Add(-5 * time.Second).Format(time.RFC3339)

	t.Run("RefractorErrorRed", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.refractor.rfx-01": {"heartbeatAt": fresh, "status": "unhealthy", "issues": []any{
				map[string]any{"severity": "error", "code": "LensRegistryIncomplete", "message": "boom"},
			}},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupRed {
			t.Errorf("overall = %v, want RED (refractor error issue)", level)
		}
	})

	t.Run("RefractorWarningYellow", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.refractor.rfx-02": {"heartbeatAt": fresh, "status": "degraded", "issues": []any{
				map[string]any{"severity": "warning", "code": "SomeWarning", "message": "hmm"},
			}},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupYellow {
			t.Errorf("overall = %v, want YELLOW (refractor warning issue)", level)
		}
	})

	t.Run("ProcessorErrorRed", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.processor.proc-01": {"heartbeatAt": fresh, "status": "unhealthy", "issues": []any{
				map[string]any{"severity": "error", "code": "SomeIssue", "message": "boom"},
			}},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupRed {
			t.Errorf("overall = %v, want RED (processor error issue)", level)
		}
	})

	t.Run("RefractorNoIssuesStaysGreen", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.refractor.rfx-03":   {"heartbeatAt": fresh, "status": "healthy", "issues": []any{}},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupGreen {
			t.Errorf("overall = %v, want GREEN (no issues)", level)
		}
	})
}

// rollupOf computes the overall rollup level for a doc set (test helper).
// sharedReporterComponents are components that emit through the shared healthkv
// reporter and carry no bespoke case in classifyKey — the set the structural
// default has to cover.
var sharedReporterComponents = []string{
	"gateway", "bridge", "object-store-manager", "chronicler", "vault",
	"cafe-app", "clinic-app", "loftspace-app", "wellness-app",
}

// TestClassifyKey_UnenumeratedComponent pins the structural classification: a
// component with no bespoke case is still read as a heartbeat, and its deeper
// keys as its event stream. Without this a heartbeat lands in the "lens" bucket,
// whose status vocabulary ({active,paused,rebuilding} + lastUpdated) shares no
// field with a real heartbeat doc — so it reads "unknown" forever, healthy or
// dead, and a frozen instance is indistinguishable from a live one.
func TestClassifyKey_UnenumeratedComponent(t *testing.T) {
	for _, c := range sharedReporterComponents {
		key := "health." + c + ".inst-abc"
		if got := classifyKey(key); got != "component-heartbeat" {
			t.Errorf("classifyKey(%q) = %q, want %q", key, got, "component-heartbeat")
		}
		evt := key + ".detail"
		if got := classifyKey(evt); got != "component-event" {
			t.Errorf("classifyKey(%q) = %q, want %q", evt, got, "component-event")
		}
	}
}

// TestClassifyKey_StructuralBoundaries pins what the structural default rests
// on: a bare NanoID (no health. prefix) is a per-lens reporter entry, and the
// key families with bespoke handling still win over the generic rule — each of
// these would otherwise be swept up as a component heartbeat or event.
func TestClassifyKey_StructuralBoundaries(t *testing.T) {
	cases := []struct{ key, want string }{
		{"someBareLensNanoID", "lens"},
		{"health.bootstrap.complete", "bootstrap"},
		{"health.gates.phase1.gate2", "gate"},
		{"health.alerts.refractor-lag", "alert"},
		{"health.weaver.wvr-01", "weaver-heartbeat"},
	}
	for _, c := range cases {
		if got := classifyKey(c.key); got != c.want {
			t.Errorf("classifyKey(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestHealthSummary_Rollup_UnenumeratedComponent verifies the structural
// classification reaches the rollup: an unenumerated component's heartbeat is
// staleness-checked and issue-checked exactly like Weaver's, rather than
// contributing a permanent "unknown". The stale case is the one that matters
// operationally — it is the difference between seeing a dead gateway and not.
func TestHealthSummary_Rollup_UnenumeratedComponent(t *testing.T) {
	now := time.Now().UTC()
	fresh := now.Add(-5 * time.Second).Format(time.RFC3339)
	stale := now.Add(-120 * time.Second).Format(time.RFC3339)

	t.Run("FreshIsGreen", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.gateway.gw-01":      {"heartbeatAt": fresh, "status": "healthy", "issues": []any{}},
			"health.vault.vlt-01":       {"heartbeatAt": fresh, "status": "healthy", "issues": []any{}},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupGreen {
			t.Errorf("overall = %v, want GREEN (fresh unenumerated heartbeats)", level)
		}
	})

	t.Run("StaleIsYellow", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.gateway.gw-02":      {"heartbeatAt": stale, "status": "healthy"},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupYellow {
			t.Errorf("overall = %v, want YELLOW (stale gateway heartbeat)", level)
		}
	})

	t.Run("ErrorIssueIsRed", func(t *testing.T) {
		level := rollupOf(t, map[string]map[string]any{
			"health.bridge.br-01": {"heartbeatAt": fresh, "status": "healthy", "issues": []any{
				map[string]any{"severity": "error", "code": "X", "message": "boom"},
			}},
			"health.bootstrap.complete": {"status": "complete"},
		})
		if level != rollupRed {
			t.Errorf("overall = %v, want RED (bridge error issue)", level)
		}
	})
}

func rollupOf(t *testing.T, docs map[string]map[string]any) rollupLevel {
	t.Helper()
	allKeys := make([]string, 0, len(docs))
	for k := range docs {
		allKeys = append(allKeys, k)
	}
	readFn := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	_, level := computeSummaryRollup(allKeys, readFn, 60*time.Second, noneDeleted)
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
		"health.bootstrap.complete":       map[string]interface{}{"ok": true},
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

// TestHealthSummary_Rollup_WedgedAckFloorYellow pins the case consumerLag
// structurally cannot see. A consumer that has been delivered its whole backlog
// and cannot finish it reports NumPending 0, so consumerLag is 0 and the
// lagStalled test is never consulted — the exact shape measured live on
// clinicProviders (678 un-acked, ack floor pinned, status "active",
// consumerLag 0), which rendered green for as long as it was wedged.
//
// Three vectors: wedged (un-acked work, floor frozen) is yellow; a consumer
// with un-acked work whose floor is still advancing is the normal
// mid-processing state and stays green; and an entry with no
// ackFloorProgressAt at all — a Refractor that predates the field — stays green
// rather than turning every in-flight message into a yellow.
func TestHealthSummary_Rollup_WedgedAckFloorYellow(t *testing.T) {
	now := time.Now().UTC()
	bootstrapDoc := map[string]any{"status": "complete", "completedAt": now.Format(time.RFC3339)}

	cases := []struct {
		name       string
		lens       string
		doc        map[string]any
		wantStatus string
		wantLevel  rollupLevel
	}{
		{
			name: "wedged: delivered work, floor frozen",
			lens: "lenswedged0000000",
			doc: map[string]any{
				"status":             "active",
				"consumerLag":        float64(0),
				"ackPending":         float64(678),
				"ackFloorProgressAt": now.Add(-20 * time.Minute).Format(time.RFC3339),
			},
			wantStatus: "yellow",
			wantLevel:  rollupYellow,
		},
		{
			name: "healthy: delivered work, floor still advancing",
			lens: "lensadvancing0000",
			doc: map[string]any{
				"status":             "active",
				"consumerLag":        float64(0),
				"ackPending":         float64(12),
				"ackFloorProgressAt": now.Add(-5 * time.Second).Format(time.RFC3339),
			},
			wantStatus: "active",
			wantLevel:  rollupGreen,
		},
		{
			name: "older Refractor: no ackFloorProgressAt at all",
			lens: "lensnoclock000000",
			doc: map[string]any{
				"status":      "active",
				"consumerLag": float64(0),
				"ackPending":  float64(3),
			},
			wantStatus: "active",
			wantLevel:  rollupGreen,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := tc.doc
			doc["ruleId"] = tc.lens
			doc["lastUpdated"] = now.Format(time.RFC3339)
			docs := map[string]map[string]any{
				tc.lens:                     doc,
				"health.bootstrap.complete": bootstrapDoc,
			}
			readFn := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
			rollup, level := computeSummaryRollup(
				[]string{tc.lens, "health.bootstrap.complete"}, readFn, 60*time.Second, noneDeleted)
			if level != tc.wantLevel {
				t.Errorf("overall = %v, want %v", level, tc.wantLevel)
			}
			if rollup.Components[0].Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", rollup.Components[0].Status, tc.wantStatus)
			}
		})
	}
}
