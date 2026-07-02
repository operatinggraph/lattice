package main

import (
	"testing"
)

// The §4.2 render tables: every renderedState the server can emit must have a
// deliberate dot class — pending-readpath in the accent (informational)
// family, never yellow.
func TestLensStateDotJS(t *testing.T) {
	vm := logicVM(t, "status.js")
	dots, ok := vm.Get("lensStateDot").Export().(map[string]any)
	if !ok {
		t.Fatal("lensStateDot is not an object")
	}
	want := map[string]string{
		lensStateFault:      "red",
		lensStatePaused:     "yellow",
		lensStatePendingRP:  "accent",
		lensStateRebuilding: "yellow",
		lensStateLagging:    "yellow",
		lensStateProjecting: "green",
		lensStateUnknown:    "dim",
	}
	for state, cls := range want {
		if dots[state] != cls {
			t.Errorf("lensStateDot[%q] = %v, want %q", state, dots[state], cls)
		}
	}
}

func TestShapeAlertLinesJS(t *testing.T) {
	vm := logicVM(t, "status.js")

	asLines := func(v any) []map[string]any {
		raw, ok := v.([]any)
		if !ok {
			t.Fatalf("shapeAlertLines returned %T, want array", v)
		}
		out := make([]map[string]any, len(raw))
		for i, e := range raw {
			out[i], ok = e.(map[string]any)
			if !ok {
				t.Fatalf("line %d is %T, want object", i, e)
			}
		}
		return out
	}

	// bootstrap missing → red first line; errors sort before warnings.
	got := asLines(call(t, vm, "shapeAlertLines", map[string]any{
		"bootstrap": false,
		"alerts": []any{
			"[warning] health.alerts.security.stub-auth-active: stub auth",
			"[error] health.alerts.x: boom",
		},
	}))
	if len(got) != 3 {
		t.Fatalf("lines = %v, want 3", got)
	}
	if got[0]["cls"] != "alertstrip-line bad" || got[0]["text"] == "" {
		t.Errorf("line 0 = %v, want the red bootstrap line", got[0])
	}
	if got[1]["text"] != "[error] health.alerts.x: boom" {
		t.Errorf("line 1 = %v, want the error alert before the warning", got[1])
	}
	// The live stub-auth alert renders verbatim, warning-classed.
	if got[2]["text"] != "[warning] health.alerts.security.stub-auth-active: stub auth" ||
		got[2]["cls"] != "alertstrip-line warn" {
		t.Errorf("line 2 = %v, want the verbatim warning", got[2])
	}

	// Healthy body → no lines (strip hides).
	if got := asLines(call(t, vm, "shapeAlertLines", map[string]any{"bootstrap": true, "alerts": []any{}})); len(got) != 0 {
		t.Errorf("healthy lines = %v, want none", got)
	}
}

func TestSysmapSummaryJS(t *testing.T) {
	vm := logicVM(t, "status.js")
	nodes := []any{
		map[string]any{"status": "green"},
		map[string]any{"status": "projecting"},
		map[string]any{"status": "present"},
		map[string]any{"status": "pending-readpath"},
		map[string]any{"status": "pending-readpath"},
		map[string]any{"status": "lagging"},
		map[string]any{"status": "absent"},
		map[string]any{"status": "unhealthy"},
	}
	got, ok := call(t, vm, "sysmapSummary", nodes).(map[string]any)
	if !ok {
		t.Fatal("sysmapSummary did not return an object")
	}
	// goja exports JS numbers as int64 when integral.
	want := map[string]int64{"pending": 2, "degraded": 3, "absent": 1, "unhealthy": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("summary[%q] = %v, want %d", k, got[k], v)
		}
	}
}
