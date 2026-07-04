package main

import (
	"testing"
	"time"
)

// The §4.2 derivation table, top→bottom precedence.
func TestLensRenderedState(t *testing.T) {
	protectedPG := lensSpecInfo{TargetType: "postgres", Protected: true}
	grantPG := lensSpecInfo{TargetType: "postgres", GrantTable: true}
	plainKV := lensSpecInfo{TargetType: "nats_kv"}

	cases := []struct {
		name      string
		doc       map[string]any
		spec      lensSpecInfo
		wantState string
		wantLevel int
	}{
		{"active clean → projecting", map[string]any{"status": "active"}, plainKV, lensStateProjecting, sevGreen},
		{"active with lag → lagging", map[string]any{"status": "active", "consumerLag": float64(3)}, plainKV, lensStateLagging, sevYellow},
		{"errors → fault even while active", map[string]any{"status": "active", "errorCount": float64(2), "lastError": "boom"}, plainKV, lensStateFault, sevYellow},
		{"structural pause → fault", map[string]any{"status": "paused", "pauseReason": "structural"}, plainKV, lensStateFault, sevYellow},
		{"manual pause → paused (operator), even on a protected lens", map[string]any{"status": "paused", "pauseReason": "manual"}, protectedPG, lensStatePaused, sevYellow},
		{"fail-closed protected pause → pending-readpath, rollup-neutral", map[string]any{"status": "paused"}, protectedPG, lensStatePendingRP, sevGreen},
		{"fail-closed grant-table pause → pending-readpath", map[string]any{"status": "paused", "pauseReason": "infra"}, grantPG, lensStatePendingRP, sevGreen},
		{"unattributed pause on a plain lens → paused", map[string]any{"status": "paused"}, plainKV, lensStatePaused, sevYellow},
		{"infra pause on a plain lens → paused (not unknown)", map[string]any{"status": "paused", "pauseReason": "infra"}, plainKV, lensStatePaused, sevYellow},
		{"rebuilding passes through", map[string]any{"status": "rebuilding"}, plainKV, lensStateRebuilding, sevYellow},
		{"unparseable → unknown", map[string]any{}, plainKV, lensStateUnknown, sevYellow},
		// errorCount is cumulative and never reset, while SetActive nulls
		// lastError — a recovered lens must NOT latch fault forever.
		{"historical errors on a recovered lens → projecting", map[string]any{"status": "active", "errorCount": float64(3)}, plainKV, lensStateProjecting, sevGreen},
		{"structural pause needs no errorCount to fault", map[string]any{"status": "paused", "pauseReason": "structural", "lastError": "bad shape"}, plainKV, lensStateFault, sevYellow},
		// The spec join alone must not mask a real fault on a verified-then-
		// failing protected lens.
		{"protected lens with errors → fault", map[string]any{"status": "active", "errorCount": float64(1), "lastError": "constraint"}, protectedPG, lensStateFault, sevYellow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, _, level := lensRenderedState(tc.doc, tc.spec)
			if state != tc.wantState || level != tc.wantLevel {
				t.Errorf("lensRenderedState = (%q, level %d), want (%q, %d)", state, level, tc.wantState, tc.wantLevel)
			}
		})
	}
}

func TestLensRenderedStateIssues(t *testing.T) {
	doc := map[string]any{"status": "active", "consumerLag": float64(7), "errorCount": float64(2), "lastError": "pg down"}
	_, issues, _ := lensRenderedState(doc, lensSpecInfo{})
	want := []string{"consumerLag=7", "errorCount=2", "lastError: pg down"}
	if len(issues) != len(want) {
		t.Fatalf("issues = %v, want %v", issues, want)
	}
	for i := range want {
		if issues[i] != want[i] {
			t.Errorf("issues[%d] = %q, want %q", i, issues[i], want[i])
		}
	}

	// lastError surfaces independently of errorCount — a paused lens's
	// explanation must not sit unread in the doc.
	paused := map[string]any{"status": "paused", "pauseReason": "infra", "lastError": "connect refused"}
	_, issues, _ = lensRenderedState(paused, lensSpecInfo{})
	if len(issues) != 1 || issues[0] != "lastError: connect refused" {
		t.Errorf("paused issues = %v, want the lastError line", issues)
	}
}

func TestComputeGates(t *testing.T) {
	docs := map[string]map[string]any{
		"health.gates.phase1.gate2": {"passed": true, "timestamp": "2026-07-01T00:00:00Z", "commit": "abc1234"},
		"health.gates.phase1.gate3": {"passed": true},
		// gate4/gate5 stamp "completedAt", not "timestamp" — the fallback read.
		"health.gates.phase1.gate4": {"passed": true, "completedAt": "2026-07-02T00:00:00Z"},
		"health.gates.phase1.gate9": {"passed": false},
		// Non-gate keys are ignored.
		"health.processor.p1":  {"component": "processor"},
		"LensId0000000000000x": {"status": "active"},
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	gates := computeGates(keys, read)
	if len(gates) != 5 {
		t.Fatalf("gates = %+v, want the 4 declared + 1 extra", gates)
	}
	// Declared order first.
	if gates[0].Gate != "gate2" || !gates[0].Present || !gates[0].Passed || gates[0].Commit != "abc1234" {
		t.Errorf("gate2 = %+v, want present+passed with commit", gates[0])
	}
	if gates[1].Gate != "gate3" || !gates[1].Passed {
		t.Errorf("gate3 = %+v, want present+passed", gates[1])
	}
	// gate4's completion time comes from its "completedAt" field.
	if gates[2].Gate != "gate4" || !gates[2].Present || gates[2].Timestamp != "2026-07-02T00:00:00Z" {
		t.Errorf("gate4 = %+v, want present with the completedAt fallback", gates[2])
	}
	// A declared but absent gate still renders (dim chip).
	if gates[3].Gate != "gate5" || gates[3].Present {
		t.Errorf("absent declared gate = %+v, want present=false", gates[3])
	}
	// Undeclared markers append after, and a written-but-failed marker is honest.
	if gates[4].Gate != "gate9" || !gates[4].Present || gates[4].Passed {
		t.Errorf("extra gate = %+v, want present, not passed", gates[4])
	}
}

// A fail-closed protected lens must not yellow the map: the "7 degraded" fix.
func TestComputeSystemMapPendingReadpathExcludedFromRollup(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{}
	for _, dc := range declaredComponents {
		docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
	}
	docs["health.bootstrap.complete"] = map[string]any{}
	docs["ProtLensId0000000000"] = map[string]any{"status": "paused"}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	spec := func(id string) lensSpecInfo {
		if id == "ProtLensId0000000000" {
			return lensSpecInfo{TargetType: "postgres", Protected: true}
		}
		return lensSpecInfo{}
	}

	m := computeSystemMap(keys, read, nil, spec, time.Minute, nil, nil)
	if m.Overall != "green" {
		t.Errorf("overall = %q, want green (pending-readpath is not degradation)", m.Overall)
	}
	lens := nodesByID(m)["ProtLensId0000000000"]
	if lens.Status != lensStatePendingRP || !lens.Protected {
		t.Errorf("lens = %+v, want pending-readpath + protected", lens)
	}
}

// Alert severity and the bootstrap marker fold into the map's overall — the
// topbar pill (from /api/health) and the map banner must agree on one screen.
func TestComputeSystemMapAlertsAndBootstrapFoldIntoOverall(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	base := func() map[string]map[string]any {
		docs := map[string]map[string]any{"health.bootstrap.complete": {}}
		for _, dc := range declaredComponents {
			docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
		}
		return docs
	}
	keysOf := func(docs map[string]map[string]any) []string {
		keys := make([]string, 0, len(docs))
		for k := range docs {
			keys = append(keys, k)
		}
		return keys
	}

	withAlert := base()
	withAlert["health.alerts.security.stub-auth-active"] = map[string]any{"severity": "warning", "message": "stub"}
	read := func(k string) (map[string]any, bool) { d, ok := withAlert[k]; return d, ok }
	if m := computeSystemMap(keysOf(withAlert), read, nil, nil, time.Minute, nil, nil); m.Overall != "yellow" {
		t.Errorf("overall = %q, want yellow (warning alert folds in)", m.Overall)
	}

	noBoot := base()
	delete(noBoot, "health.bootstrap.complete")
	read = func(k string) (map[string]any, bool) { d, ok := noBoot[k]; return d, ok }
	if m := computeSystemMap(keysOf(noBoot), read, nil, nil, time.Minute, nil, nil); m.Overall != "red" {
		t.Errorf("overall = %q, want red (bootstrap marker missing)", m.Overall)
	}
}

// computeHealth mirrors the exclusion so the topbar pill agrees with the map.
func TestComputeHealthPendingReadpathExcludedFromRollup(t *testing.T) {
	docs := map[string]map[string]any{
		"health.bootstrap.complete": {},
		"ProtLensId0000000000":      {"status": "paused"},
	}
	keys := []string{"health.bootstrap.complete", "ProtLensId0000000000"}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	spec := func(id string) lensSpecInfo { return lensSpecInfo{TargetType: "postgres", GrantTable: true} }

	got := computeHealth(keys, read, nil, spec, time.Minute)
	if got.Overall != "green" {
		t.Errorf("overall = %q, want green (pending-readpath excluded)", got.Overall)
	}
}
