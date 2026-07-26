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
		map[string]any{"status": "offline"},
	}
	got, ok := call(t, vm, "sysmapSummary", nodes).(map[string]any)
	if !ok {
		t.Fatal("sysmapSummary did not return an object")
	}
	// goja exports JS numbers as int64 when integral. An offline node (declared
	// app or optional up-full-only component) contributes to no bucket.
	want := map[string]int64{"pending": 2, "degraded": 3, "absent": 1, "unhealthy": 1}
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

// An offline node (declared app or optional up-full-only component) never
// counts toward the yellow "degraded" line — both are expected-absent.
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

// offline maps to the dim-family class, and the hover copy + pointers exist for
// every optional component the server declares (up-full only).
func TestOptionalComponentRenderTablesJS(t *testing.T) {
	vm := logicVM(t, "status.js")
	cls, ok := vm.Get("componentStatusClass").Export().(map[string]any)
	if !ok {
		t.Fatal("componentStatusClass is not an object")
	}
	if cls["offline"] != "dim" {
		t.Errorf(`componentStatusClass["offline"] = %v, want "dim"`, cls["offline"])
	}
	ptr, ok := vm.Get("offlineComponentPointer").Export().(map[string]any)
	if !ok {
		t.Fatal("offlineComponentPointer is not an object")
	}
	for _, dc := range declaredComponents {
		if !dc.optional {
			continue
		}
		if s, _ := ptr[dc.id].(string); s == "" {
			t.Errorf("offlineComponentPointer[%q] missing — every optional node names where it runs", dc.id)
		}
	}
}

// The door band is three rows now, and the split is what makes the map read as
// the request path: who calls, what they call, and the one door those calls go
// through. sysmapTier alone cannot express it — every one of these is tier -1.
func TestSysmapDoorLine(t *testing.T) {
	vm := logicVM(t, "status.js")

	cases := map[string]map[string]any{
		"ingress": {"kind": "ingress", "id": "external"},
		"apps":    {"kind": "app", "id": "facet"},
		"gateway": {"kind": "component", "id": "gateway"},
	}
	for want, node := range cases {
		if got := call(t, vm, "sysmapDoorLine", node); got != want {
			t.Errorf("sysmapDoorLine(%v) = %v, want %q", node, got, want)
		}
	}
}

// An app is ABOVE the Gateway, so an app→gateway edge must route downward.
// Read off sysmapTier the two are equal (both -1) and the edge would draw as a
// sideways same-row arc instead of the hop it is. Depth has to order the whole
// door band against the spine below it, too.
func TestSysmapDepth_OrdersTheDoorBandTopToBottom(t *testing.T) {
	vm := logicVM(t, "status.js")

	// goja exports a whole number as int64 and a fractional one as float64,
	// and this function deliberately returns both.
	depth := func(node map[string]any) float64 {
		switch v := call(t, vm, "sysmapDepth", node).(type) {
		case float64:
			return v
		case int64:
			return float64(v)
		default:
			t.Fatalf("sysmapDepth(%v) = %T, not a number", node, v)
			return 0
		}
	}
	ingress := depth(map[string]any{"kind": "ingress", "id": "external"})
	app := depth(map[string]any{"kind": "app", "id": "facet"})
	gw := depth(map[string]any{"kind": "component", "id": "gateway"})
	ops := depth(map[string]any{"kind": "infra", "id": "core-operations"})

	if !(ingress < app && app < gw && gw < ops) {
		t.Errorf("depths ingress=%v app=%v gateway=%v core-operations=%v; want strictly increasing "+
			"(the band reads browser → app → Gateway → core-operations)", ingress, app, gw, ops)
	}
}

func TestGroupApps(t *testing.T) {
	vm := logicVM(t, "status.js")

	out, _ := call(t, vm, "groupApps", []any{
		map[string]any{"id": "facet", "label": "Facet", "status": "green"},
		map[string]any{"id": "clinic-app", "label": "Clinic", "group": "apps", "status": "green"},
		map[string]any{"id": "cafe-app", "label": "Café", "group": "apps", "status": "offline"},
	}).(map[string]any)

	standalone, _ := out["standalone"].([]any)
	if len(standalone) != 1 {
		t.Fatalf("standalone = %v, want just Facet", standalone)
	}
	if s0, _ := standalone[0].(map[string]any); s0["id"] != "facet" {
		t.Errorf("standalone[0] = %v, want facet", standalone[0])
	}
	boxes, _ := out["boxes"].([]any)
	if len(boxes) != 1 {
		t.Fatalf("boxes = %v, want one apps box", boxes)
	}
	b, _ := boxes[0].(map[string]any)
	if b["id"] != "group:apps" {
		t.Errorf("box id = %v, want group:apps (what the edges attach to)", b["id"])
	}
	members, _ := b["members"].([]any)
	if len(members) != 2 {
		t.Errorf("box members = %v, want the two grouped apps", members)
	}
	// offline outranks green: an app that is not running must surface on the box.
	if b["status"] != "offline" {
		t.Errorf("box status = %v, want offline (worst-of its members)", b["status"])
	}
}

// A box that renders healthy while a member is down hides exactly what the map
// exists to show, so worst-of has to prefer the real failures over "offline".
func TestGroupApps_BoxTakesTheWorstMemberStatus(t *testing.T) {
	vm := logicVM(t, "status.js")

	out, _ := call(t, vm, "groupApps", []any{
		map[string]any{"id": "a", "group": "apps", "status": "offline"},
		map[string]any{"id": "b", "group": "apps", "status": "unhealthy"},
		map[string]any{"id": "c", "group": "apps", "status": "green"},
	}).(map[string]any)
	boxes, _ := out["boxes"].([]any)
	b, _ := boxes[0].(map[string]any)
	if b["status"] != "unhealthy" {
		t.Errorf("box status = %v, want unhealthy (a down app outranks an offline one)", b["status"])
	}
}

func TestInfraTarget(t *testing.T) {
	vm := logicVM(t, "status.js")

	if got := call(t, vm, "infraTarget", "core-kv"); got != "#/graph" {
		t.Errorf("core-kv = %v, want the Graph tab — the tile's contents ARE the graph", got)
	}
	for _, id := range []string{"core-operations", "core-events", "object-store", "external"} {
		if got := call(t, vm, "infraTarget", id); got != "" {
			t.Errorf("%s = %v, want no drill target", id, got)
		}
	}
}

// The edge pass runs separately from the layout pass, so the boxed-app mapping
// has to be derivable from the node set alone — a mapping the layout owns is a
// mapping the edge pass cannot see, and every edge on the map dies with it.
func TestAppBoxIndex(t *testing.T) {
	vm := logicVM(t, "status.js")

	out, _ := call(t, vm, "appBoxIndex", []any{
		map[string]any{"id": "facet", "kind": "app"},
		map[string]any{"id": "clinic-app", "kind": "app", "group": "apps"},
		map[string]any{"id": "cafe-app", "kind": "app", "group": "apps"},
		map[string]any{"id": "gateway", "kind": "component", "group": "apps"},
		map[string]any{"id": "core-kv", "kind": "infra"},
	}).(map[string]any)

	if out["clinic-app"] != "group:apps" || out["cafe-app"] != "group:apps" {
		t.Errorf("boxed apps = %v, want both re-pointed at group:apps", out)
	}
	if _, ok := out["facet"]; ok {
		t.Error("facet is indexed, but a standalone app keeps its own edges")
	}
	if _, ok := out["gateway"]; ok {
		t.Error("a non-app node with a group was indexed; only apps box up")
	}
	if _, ok := out["core-kv"]; ok {
		t.Error("infra was indexed")
	}
}
