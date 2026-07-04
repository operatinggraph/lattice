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
		map[string]any{"status": "design-ahead"},
	}
	got, ok := call(t, vm, "sysmapSummary", nodes).(map[string]any)
	if !ok {
		t.Fatal("sysmapSummary did not return an object")
	}
	// goja exports JS numbers as int64 when integral. A design-ahead component
	// gets its own informational bucket — never degraded (§1.4).
	want := map[string]int64{"pending": 2, "degraded": 3, "absent": 1, "unhealthy": 1, "designAhead": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("summary[%q] = %v, want %d", k, got[k], v)
		}
	}
}

// The F10 tier placements: the ingress band (tier -1) holds the external
// marker + the Gateway; the object-store plane sits on the tier-4 band with
// the read-models; everything else keeps its 2.0 tier.
func TestSysmapTierJS(t *testing.T) {
	vm := logicVM(t, "status.js")
	cases := []struct {
		node map[string]any
		want int64
	}{
		{map[string]any{"kind": "ingress", "id": "external"}, -1},
		{map[string]any{"kind": "component", "id": "gateway"}, -1},
		{map[string]any{"kind": "infra", "id": "core-operations"}, 0},
		{map[string]any{"kind": "component", "id": "processor"}, 1},
		{map[string]any{"kind": "infra", "id": "core-kv"}, 2},
		{map[string]any{"kind": "infra", "id": "core-events"}, 2},
		{map[string]any{"kind": "component", "id": "weaver"}, 3},
		{map[string]any{"kind": "component", "id": "vault"}, 3},
		{map[string]any{"kind": "component", "id": "chronicler"}, 3},
		{map[string]any{"kind": "lens", "id": "SomeLensId"}, 4},
		{map[string]any{"kind": "infra", "id": "object-store"}, 4},
		// F14: declared apps join the door band beside the Gateway.
		{map[string]any{"kind": "app", "id": "clinic-app"}, -1},
		{map[string]any{"kind": "app", "id": "loftspace-app"}, -1},
	}
	for _, c := range cases {
		if got := call(t, vm, "sysmapTier", c.node); got != c.want {
			t.Errorf("sysmapTier(%v) = %v, want %d", c.node, got, c.want)
		}
	}
}

// F14: componentStatusClass carries the offline (dim) family for a declared
// app with no heartbeat.
func TestComponentStatusClassOfflineJS(t *testing.T) {
	vm := logicVM(t, "status.js")
	cls, ok := vm.Get("componentStatusClass").Export().(map[string]any)
	if !ok {
		t.Fatal("componentStatusClass is not an object")
	}
	if cls["offline"] != "dim" {
		t.Errorf(`componentStatusClass["offline"] = %v, want "dim"`, cls["offline"])
	}
}

// F14: an offline declared app never counts toward the yellow "degraded"
// line — verticals are optional workloads, symmetric with design-ahead.
func TestSysmapSummaryOfflineJS(t *testing.T) {
	vm := logicVM(t, "status.js")
	nodes := []any{
		map[string]any{"status": "green"},
		map[string]any{"status": "offline"},
		map[string]any{"status": "offline"},
	}
	got, ok := call(t, vm, "sysmapSummary", nodes).(map[string]any)
	if !ok {
		t.Fatal("sysmapSummary did not return an object")
	}
	if got["degraded"] != int64(0) {
		t.Errorf("summary[degraded] = %v, want 0 (offline apps never degrade)", got["degraded"])
	}
}

// groupLenses buckets lens nodes by their server-stamped pkg field, sorted by
// group name: worst-of status, count, protected count, and member chips in
// input order — the exception-first density rule reads this without a fresh
// DOM pass.
func TestGroupLensesJS(t *testing.T) {
	vm := logicVM(t, "status.js")
	nodes := []any{
		map[string]any{"kind": "component", "id": "processor", "status": "green"}, // non-lens, ignored
		map[string]any{"kind": "lens", "id": "L1", "pkg": "clinic", "pkgKey": "vtx.package.P2", "status": "projecting", "label": "aaa"},
		map[string]any{"kind": "lens", "id": "L2", "pkg": "clinic", "pkgKey": "vtx.package.P2", "status": "fault", "label": "bbb", "protected": true},
		map[string]any{"kind": "lens", "id": "L3", "status": "projecting", "label": "ccc"}, // no pkg → kernel
	}
	groups, ok := call(t, vm, "groupLenses", nodes).([]any)
	if !ok {
		t.Fatalf("groupLenses did not return an array: %T", call(t, vm, "groupLenses", nodes))
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %+v, want 2 (clinic, kernel)", groups)
	}
	// Sorted by group name: "clinic" before "kernel".
	clinic, ok := groups[0].(map[string]any)
	if !ok || clinic["group"] != "clinic" {
		t.Fatalf("groups[0] = %+v, want clinic first (alphabetical)", groups[0])
	}
	if clinic["count"] != int64(2) || clinic["protected"] != int64(1) || clinic["worst"] != "fault" {
		t.Errorf("clinic group = %+v, want count=2 protected=1 worst=fault", clinic)
	}
	if clinic["pkgKey"] != "vtx.package.P2" {
		t.Errorf("clinic pkgKey = %v, want vtx.package.P2", clinic["pkgKey"])
	}
	chips, ok := clinic["chips"].([]any)
	if !ok || len(chips) != 2 {
		t.Fatalf("clinic chips = %+v, want 2 members", clinic["chips"])
	}
	kernel, ok := groups[1].(map[string]any)
	if !ok || kernel["group"] != "kernel" || kernel["count"] != int64(1) || kernel["worst"] != "projecting" {
		t.Errorf("groups[1] = %+v, want kernel/count=1/worst=projecting", groups[1])
	}
}

// design-ahead maps to the accent-family class, and the hover copy + pointers
// exist for every designAhead component the server declares.
func TestDesignAheadRenderTablesJS(t *testing.T) {
	vm := logicVM(t, "status.js")
	cls, ok := vm.Get("componentStatusClass").Export().(map[string]any)
	if !ok {
		t.Fatal("componentStatusClass is not an object")
	}
	if cls["design-ahead"] != "designahead" {
		t.Errorf(`componentStatusClass["design-ahead"] = %v, want "designahead"`, cls["design-ahead"])
	}
	ptr, ok := vm.Get("designAheadPointer").Export().(map[string]any)
	if !ok {
		t.Fatal("designAheadPointer is not an object")
	}
	for _, dc := range declaredComponents {
		if !dc.designAhead {
			continue
		}
		if s, _ := ptr[dc.id].(string); s == "" {
			t.Errorf("designAheadPointer[%q] missing — every design-ahead node teaches its roadmap", dc.id)
		}
	}
}
