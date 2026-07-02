package main

import (
	"testing"
	"time"
)

func TestClassifyHealthKey(t *testing.T) {
	cases := []struct {
		key       string
		wantGroup string
		wantKind  string
	}{
		{"health.processor.proc-1", "processor", kindComponent},
		{"health.refractor.rfx-1", "refractor", kindComponent},
		{"health.loom.loom-1", "loom", kindComponent},
		{"health.weaver.weaver-1", "weaver", kindComponent},
		{"health.bridge.bridge-1", "bridge", kindComponent},
		{"health.object-store-manager.objmgr-1", "object-store-manager", kindComponent},
		{"health.bootstrap.complete", "bootstrap", kindBootstrap},
		{"health.gates.phase1.gate1", "gate", kindGate},
		{"health.alerts.security.stub-auth-active", "alert", kindAlert},
		{"health.processor.proc-1.event.deep", "processor", kindEvent},
		{"5BNztfjCmcyLcu9Js9XT", "lens", kindLens},
	}
	for _, c := range cases {
		g, k := classifyHealthKey(c.key)
		if g != c.wantGroup || k != c.wantKind {
			t.Errorf("classifyHealthKey(%q) = (%q,%q), want (%q,%q)", c.key, g, k, c.wantGroup, c.wantKind)
		}
	}
}

func TestComputeHealthComponentAndLens(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{
		"health.processor.proc-1":              {"component": "processor", "instance": "proc-1", "heartbeatAt": now},
		"health.loom.loom-1":                   {"component": "loom", "instance": "loom-1", "heartbeatAt": now},
		"health.object-store-manager.objmgr-1": {"component": "object-store-manager", "instance": "objmgr-1", "updatedAt": now},
		"uhBwnSgiVAtRTswWuhBw":                 {"status": "active"},
		"health.bootstrap.complete":            {},
		"health.alerts.security.stub":          {"severity": "warning", "message": "stub auth"},
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	resolve := func(id string) (string, string) {
		if id == "uhBwnSgiVAtRTswWuhBw" {
			return "objectLiveness", "object→owner liveness"
		}
		return "", ""
	}

	got := computeHealth(keys, read, resolve, nil, 60*time.Second)

	byName := map[string]healthComponent{}
	for _, c := range got.Components {
		byName[c.Name] = c
	}

	// loom must now be its own component (not "lens"/"unknown"), green via heartbeat.
	loom, ok := byName["loom"]
	if !ok {
		t.Fatalf("loom component missing; components: %+v", got.Components)
	}
	if loom.Group != "loom" || loom.Status != "green" || loom.Detail != "loom-1" {
		t.Errorf("loom card = %+v, want group=loom status=green detail=loom-1", loom)
	}

	// object-store-manager uses updatedAt (not heartbeatAt) — still green.
	if objmgr := byName["object-store-manager"]; objmgr.Status != "green" {
		t.Errorf("object-store-manager status = %q, want green (via updatedAt)", objmgr.Status)
	}

	// the lens card resolves to a descriptive name + detail.
	lens, ok := byName["objectLiveness"]
	if !ok {
		t.Fatalf("resolved lens card missing; components: %+v", got.Components)
	}
	if lens.Group != "lens" || lens.Status != "projecting" || lens.Key != "uhBwnSgiVAtRTswWuhBw" {
		t.Errorf("lens card = %+v, want group=lens status=projecting key=<id>", lens)
	}
	if lens.Detail != "lens · object→owner liveness" {
		t.Errorf("lens detail = %q", lens.Detail)
	}

	// bootstrap present → not red; the warning alert → yellow rollup.
	if got.Overall != "yellow" {
		t.Errorf("overall = %q, want yellow (warning alert present)", got.Overall)
	}
	if !got.Bootstrap {
		t.Error("bootstrap = false, want true (marker present)")
	}
	if len(got.Alerts) != 1 {
		t.Errorf("alerts = %v, want 1", got.Alerts)
	}
}

func TestComputeHealthLensFallsBackToID(t *testing.T) {
	docs := map[string]map[string]any{"AbcLensId0000000000": {"status": "active"}}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	// resolve returns "" → card keeps the id as its name.
	got := computeHealth([]string{"AbcLensId0000000000"}, read, func(string) (string, string) { return "", "" }, nil, time.Minute)
	if len(got.Components) != 1 || got.Components[0].Name != "AbcLensId0000000000" {
		t.Errorf("expected id fallback name; got %+v", got.Components)
	}
}

func TestComputeHealthMissingBootstrapIsRed(t *testing.T) {
	got := computeHealth(nil, func(string) (map[string]any, bool) { return nil, false }, nil, nil, time.Minute)
	if got.Overall != "red" {
		t.Errorf("overall = %q, want red (no bootstrap marker)", got.Overall)
	}
	if got.Bootstrap {
		t.Error("bootstrap = true, want false (no marker)")
	}
}

// componentLiveness fuses Loupe-computed heartbeat freshness with the
// Contract #5 §5.4 status + §5.5 issues[] anomaly channel.
func TestComponentLiveness(t *testing.T) {
	fresh := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	issue := func(sev, code, msg string) map[string]any {
		return map[string]any{"severity": sev, "code": code, "message": msg, "since": old}
	}

	cases := []struct {
		name       string
		doc        map[string]any
		wantStatus string
		wantLevel  int
		wantIssues []string
	}{
		{
			name:       "fresh healthy → green, no issues",
			doc:        map[string]any{"heartbeatAt": fresh, "status": "healthy", "issues": []any{}},
			wantStatus: "green", wantLevel: sevGreen, wantIssues: nil,
		},
		{
			name:       "missing status on fresh heartbeat stays green (back-compat)",
			doc:        map[string]any{"heartbeatAt": fresh},
			wantStatus: "green", wantLevel: sevGreen, wantIssues: nil,
		},
		{
			name:       "degraded → yellow, warning issue surfaced",
			doc:        map[string]any{"heartbeatAt": fresh, "status": "degraded", "issues": []any{issue("warning", "CapabilityLensLagging", "lag over threshold")}},
			wantStatus: "degraded", wantLevel: sevYellow,
			wantIssues: []string{"[warning] CapabilityLensLagging: lag over threshold"},
		},
		{
			name:       "unhealthy → red, error issue surfaced",
			doc:        map[string]any{"heartbeatAt": fresh, "status": "unhealthy", "issues": []any{issue("error", "CapabilityLensPaused", "auth lens paused")}},
			wantStatus: "unhealthy", wantLevel: sevRed,
			wantIssues: []string{"[error] CapabilityLensPaused: auth lens paused"},
		},
		{
			name:       "reported healthy but error issue escalates to unhealthy (Weaver self-report inconsistency)",
			doc:        map[string]any{"heartbeatAt": fresh, "status": "healthy", "issues": []any{issue("error", "TemplateDataError", "missing subject")}},
			wantStatus: "unhealthy", wantLevel: sevRed,
			wantIssues: []string{"[error] TemplateDataError: missing subject"},
		},
		{
			name:       "reported healthy but warning issue escalates to degraded",
			doc:        map[string]any{"heartbeatAt": fresh, "status": "healthy", "issues": []any{issue("warning", "SlowProjection", "lag rising")}},
			wantStatus: "degraded", wantLevel: sevYellow,
			wantIssues: []string{"[warning] SlowProjection: lag rising"},
		},
		{
			name:       "stale heartbeat overrides reported healthy, still surfaces last-known issues",
			doc:        map[string]any{"heartbeatAt": old, "status": "healthy", "issues": []any{}},
			wantStatus: "stale", wantLevel: sevYellow,
			wantIssues: []string{"heartbeat older than 1m0s"},
		},
		{
			name:       "stale + reported unhealthy keeps red label and notes staleness",
			doc:        map[string]any{"heartbeatAt": old, "status": "unhealthy", "issues": []any{issue("error", "X", "boom")}},
			wantStatus: "unhealthy", wantLevel: sevRed,
			wantIssues: []string{"heartbeat older than 1m0s", "[error] X: boom"},
		},
		{
			name:       "no heartbeat → unknown",
			doc:        map[string]any{"status": "healthy"},
			wantStatus: "unknown", wantLevel: sevYellow, wantIssues: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, issues, level := componentLiveness(tc.doc, time.Minute)
			if status != tc.wantStatus {
				t.Errorf("status = %q, want %q", status, tc.wantStatus)
			}
			if level != tc.wantLevel {
				t.Errorf("level = %d, want %d", level, tc.wantLevel)
			}
			if len(issues) != len(tc.wantIssues) {
				t.Fatalf("issues = %v, want %v", issues, tc.wantIssues)
			}
			for i := range issues {
				if issues[i] != tc.wantIssues[i] {
					t.Errorf("issues[%d] = %q, want %q", i, issues[i], tc.wantIssues[i])
				}
			}
		})
	}
}

// computeHealth must roll the overall up to red when a component self-reports
// unhealthy (previously it only ever reached yellow on staleness).
func TestComputeHealthUnhealthyComponentRollsToRed(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{
		"health.bootstrap.complete": {},
		"health.refractor.refr-1": {
			"component": "refractor", "instance": "refr-1", "heartbeatAt": now,
			"status": "unhealthy",
			"issues": []any{map[string]any{"severity": "error", "code": "CapabilityLensPaused", "message": "auth lens paused"}},
		},
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	got := computeHealth(keys, read, nil, nil, time.Minute)
	if got.Overall != "red" {
		t.Errorf("overall = %q, want red (unhealthy component)", got.Overall)
	}
	var refr *healthComponent
	for i := range got.Components {
		if got.Components[i].Name == "refractor" {
			refr = &got.Components[i]
		}
	}
	if refr == nil {
		t.Fatalf("refractor card missing: %+v", got.Components)
	}
	if refr.Status != "unhealthy" {
		t.Errorf("refractor status = %q, want unhealthy", refr.Status)
	}
	if len(refr.Issues) != 1 || refr.Issues[0] != "[error] CapabilityLensPaused: auth lens paused" {
		t.Errorf("refractor issues = %v, want the §5.5 line", refr.Issues)
	}
}
