package main

import (
	"testing"
	"time"
)

func nodesByID(m systemMap) map[string]mapNode {
	out := make(map[string]mapNode, len(m.Nodes))
	for _, n := range m.Nodes {
		out[n.ID] = n
	}
	return out
}

func TestComputeSystemMapOverlay(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	stale := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{
		"health.processor.proc-1":              {"component": "processor", "instance": "proc-1", "heartbeatAt": now},
		"health.refractor.rfx-1":               {"component": "refractor", "instance": "rfx-1", "heartbeatAt": now},
		"health.loom.loom-1":                   {"component": "loom", "instance": "loom-1", "heartbeatAt": stale},
		"health.object-store-manager.objmgr-1": {"component": "object-store-manager", "instance": "objmgr-1", "updatedAt": now},
		"AbcLensId0000000000":                  {"status": "active", "activeSequence": float64(41)},
		// weaver + bridge intentionally absent.
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	resolve := func(id string) (string, string) {
		if id == "AbcLensId0000000000" {
			return "objectLiveness", "object→owner liveness"
		}
		return "", ""
	}

	m := computeSystemMap(keys, read, resolve, nil, 60*time.Second, nil, nil)
	byID := nodesByID(m)

	// Infra spine is always present.
	for _, id := range []string{"core-operations", "core-events", "core-kv"} {
		if n, ok := byID[id]; !ok || n.Kind != nodeInfra || n.Status != "present" {
			t.Errorf("infra node %q = %+v, want kind=infra status=present", id, n)
		}
	}

	// Present + fresh component is green with its instance.
	if proc := byID["processor"]; proc.Status != "green" || proc.Detail != "proc-1" || proc.Kind != nodeComponent {
		t.Errorf("processor = %+v, want green/proc-1/component", proc)
	}
	// Stale heartbeat → stale + an issue.
	if loom := byID["loom"]; loom.Status != "stale" || len(loom.Issues) == 0 {
		t.Errorf("loom = %+v, want stale with an issue", loom)
	}
	// Declared but no heartbeat → absent.
	if weaver := byID["weaver"]; weaver.Status != "absent" {
		t.Errorf("weaver = %+v, want absent", weaver)
	}
	if bridge := byID["bridge"]; bridge.Status != "absent" {
		t.Errorf("bridge = %+v, want absent", bridge)
	}
	// Lens node is parented to refractor with a resolved label.
	lens := byID["AbcLensId0000000000"]
	if lens.Kind != nodeLens || lens.Parent != refractorID || lens.Label != "objectLiveness" || lens.Status != "projecting" {
		t.Errorf("lens = %+v, want lens/refractor/objectLiveness/projecting", lens)
	}
	// The reporter's activeSequence (rule version) rides the lens node — the
	// pulse feed diffs it between polls.
	if lens.ActiveSequence != 41 {
		t.Errorf("lens activeSequence = %d, want 41", lens.ActiveSequence)
	}
	// A lens claimed by no manifest (nil pkgIndex here) falls to "kernel" —
	// per-lens edges are retired (F14); the client clusters and draws its own
	// synthetic refractor→cluster edge from this field.
	if lens.Pkg != kernelGroup {
		t.Errorf("lens.Pkg = %q, want kernel fallback", lens.Pkg)
	}
	for _, e := range m.Edges {
		if e.To == "AbcLensId0000000000" {
			t.Errorf("per-lens edges are retired (F14), got %+v", e)
		}
	}

	// An absent declared component forces red.
	if m.Overall != "red" {
		t.Errorf("overall = %q, want red (weaver+bridge absent)", m.Overall)
	}
}

func TestComputeSystemMapAllPresentGreen(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{}
	for _, dc := range declaredComponents {
		docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
	}
	docs["health.bootstrap.complete"] = map[string]any{}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	m := computeSystemMap(keys, read, nil, nil, 60*time.Second, nil, nil)
	if m.Overall != "green" {
		t.Errorf("overall = %q, want green (every component fresh)", m.Overall)
	}
	for _, n := range m.Nodes {
		if n.Kind == nodeComponent && n.Status != "green" {
			t.Errorf("component %q = %q, want green", n.ID, n.Status)
		}
	}
}

// A component self-reporting unhealthy (Contract #5 §5.4) drives the map node to
// "unhealthy" with its issues, and rolls the overall up to red.
func TestComputeSystemMapUnhealthyComponentRed(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{}
	for _, dc := range declaredComponents {
		docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
	}
	docs["health.bootstrap.complete"] = map[string]any{}
	docs["health."+refractorID+".inst"] = map[string]any{
		"component": refractorID, "instance": "inst", "heartbeatAt": now,
		"status": "unhealthy",
		"issues": []any{map[string]any{"severity": "error", "code": "CapabilityLensPaused", "message": "auth lens paused"}},
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	m := computeSystemMap(keys, read, nil, nil, time.Minute, nil, nil)
	if m.Overall != "red" {
		t.Errorf("overall = %q, want red (unhealthy component)", m.Overall)
	}
	refr := nodesByID(m)[refractorID]
	if refr.Status != "unhealthy" {
		t.Errorf("refractor status = %q, want unhealthy", refr.Status)
	}
	if len(refr.Issues) != 1 || refr.Issues[0] != "[error] CapabilityLensPaused: auth lens paused" {
		t.Errorf("refractor issues = %v, want the §5.5 line", refr.Issues)
	}
}

func TestComputeSystemMapStaleLensYellow(t *testing.T) {
	docs := map[string]map[string]any{"LensId00000000000000": {"status": "paused"}}
	// All declared components present + fresh so only the lens can move the rollup.
	now := time.Now().UTC().Format(time.RFC3339)
	for _, dc := range declaredComponents {
		docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
	}
	docs["health.bootstrap.complete"] = map[string]any{}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	m := computeSystemMap(keys, read, func(string) (string, string) { return "", "" }, nil, time.Minute, nil, nil)
	if m.Overall != "yellow" {
		t.Errorf("overall = %q, want yellow (paused lens)", m.Overall)
	}
	lens := nodesByID(m)["LensId00000000000000"]
	if lens.Status != "paused" {
		t.Errorf("lens status = %q, want paused", lens.Status)
	}
}

// Two heartbeats of the same component must BOTH survive onto the node (no
// last-write-wins collapse): worst instance drives status/detail, freshest
// drives freshness, and Instances itemizes every beat.
func TestComputeSystemMapPluralInstances(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	stale := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{}
	for _, dc := range declaredComponents {
		docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
	}
	docs["health.bootstrap.complete"] = map[string]any{}
	docs["health.processor.proc-a"] = map[string]any{"component": "processor", "instance": "proc-a", "heartbeatAt": now}
	docs["health.processor.proc-b"] = map[string]any{"component": "processor", "instance": "proc-b", "heartbeatAt": stale}
	delete(docs, "health.processor.inst")
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	m := computeSystemMap(keys, read, nil, nil, time.Minute, nil, nil)
	proc := nodesByID(m)["processor"]
	if len(proc.Instances) != 2 {
		t.Fatalf("processor instances = %+v, want 2 (no LWW collapse)", proc.Instances)
	}
	if proc.Status != "stale" || proc.Detail != "proc-b" {
		t.Errorf("processor rollup = %q/%q, want stale/proc-b (worst instance)", proc.Status, proc.Detail)
	}
	// Freshness comes from the freshest beat (proc-a, just now — sub-second
	// truncation in the RFC3339 fixture makes the exact digit wall-clock
	// dependent, so assert "fresh", not an exact value).
	if proc.Freshness != "0s ago" && proc.Freshness != "1s ago" {
		t.Errorf("processor freshness = %q, want the freshest instance's (~0s ago)", proc.Freshness)
	}
	if m.Overall != "yellow" {
		t.Errorf("overall = %q, want yellow (one stale instance)", m.Overall)
	}
	if proc.Instances[0].Instance != "proc-a" || proc.Instances[1].Instance != "proc-b" {
		t.Errorf("instances not sorted by id: %+v", proc.Instances)
	}
}

// An undeclared heartbeat group (some other reporter — the verticals are now
// F14 declared apps, not this discovery path) becomes a "client" node with
// the same per-instance overlay and no skeleton edges.
func TestComputeSystemMapClientNodes(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{}
	for _, dc := range declaredComponents {
		docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
	}
	docs["health.bootstrap.complete"] = map[string]any{}
	docs["health.custom-reporter.web-1"] = map[string]any{"component": "custom-reporter", "instance": "web-1", "heartbeatAt": now}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	m := computeSystemMap(keys, read, nil, nil, time.Minute, nil, nil)
	app := nodesByID(m)["custom-reporter"]
	if app.Kind != nodeClient || app.Status != "green" || len(app.Instances) != 1 {
		t.Errorf("client node = %+v, want kind=client green with 1 instance", app)
	}
	for _, e := range m.Edges {
		if e.From == "custom-reporter" || e.To == "custom-reporter" {
			t.Errorf("client node must not gain skeleton edges, got %+v", e)
		}
	}
	if m.Overall != "green" {
		t.Errorf("overall = %q, want green (client is healthy, declared apps offline is not degrading)", m.Overall)
	}
}

// A declared app (F14 door band) with no heartbeat renders "offline" — dim,
// zero rollup contribution, never absent-red (verticals are optional
// workloads). It heartbeats and overlays like any component once live, and
// gains the door-band edges (solid direct + dashed design-ahead via-Gateway)
// with the label carried once across the declaredApps pair.
func TestComputeSystemMapDeclaredAppsDoorBand(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{"health.bootstrap.complete": {}}
	for _, dc := range declaredComponents {
		docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
	}
	docs["health.clinic-app.web-1"] = map[string]any{"component": "clinic-app", "instance": "web-1", "heartbeatAt": now}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	m := computeSystemMap(keys, read, nil, nil, time.Minute, nil, nil)
	byID := nodesByID(m)

	clinic := byID["clinic-app"]
	if clinic.Kind != nodeApp || clinic.Status != "green" || clinic.Detail != "web-1" {
		t.Errorf("clinic-app = %+v, want app/green/web-1 (heartbeating)", clinic)
	}
	loft := byID["loftspace-app"]
	if loft.Kind != nodeApp || loft.Status != "offline" {
		t.Errorf("loftspace-app = %+v, want app/offline (no heartbeat)", loft)
	}
	if m.Overall != "green" {
		t.Errorf("overall = %q, want green (an offline declared app never degrades the rollup)", m.Overall)
	}

	var direct, gateway []mapEdge
	labelled := 0
	for _, e := range m.Edges {
		if e.To == "core-operations" && (e.From == "clinic-app" || e.From == "loftspace-app") {
			direct = append(direct, e)
			if e.Label != "" {
				labelled++
			}
		}
		if e.To == "gateway" && (e.From == "clinic-app" || e.From == "loftspace-app") {
			gateway = append(gateway, e)
			if !e.DesignAhead {
				t.Errorf("app→gateway edge %+v must carry designAhead (end-state route not yet adopted)", e)
			}
		}
	}
	if len(direct) != 2 || labelled != 1 {
		t.Errorf("direct app→core-operations edges = %+v, want 2 edges with exactly 1 labelled", direct)
	}
	if len(gateway) != 2 {
		t.Errorf("dashed app→gateway edges = %+v, want 2", gateway)
	}
}

// A designAhead declared component with no heartbeat renders the informational
// "design-ahead" state (loupe-platform-edges-ux.md §1.4) — never absent-red,
// never a rollup contribution; the instant it heartbeats it goes live like any
// component. The F10 topology nodes (ingress marker, object-store plane,
// Vault's lateral flag) and edges ride the same map.
func TestComputeSystemMapDesignAhead(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{"health.bootstrap.complete": {}}
	for _, dc := range declaredComponents {
		if dc.designAhead {
			continue // gateway / vault / chronicler: no heartbeat yet
		}
		docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	m := computeSystemMap(keys, read, nil, nil, time.Minute, nil, nil)
	byID := nodesByID(m)

	if m.Overall != "green" {
		t.Errorf("overall = %q, want green (design-ahead never degrades the rollup)", m.Overall)
	}
	for _, id := range []string{"gateway", "vault", "chronicler"} {
		n := byID[id]
		if n.Kind != nodeComponent || n.Status != "design-ahead" || n.Detail != "not yet deployed" {
			t.Errorf("%s = %+v, want component/design-ahead/not yet deployed", id, n)
		}
	}
	if !byID["vault"].Lateral {
		t.Errorf("vault node not marked lateral (beside-Core-KV placement)")
	}
	if byID["gateway"].Lateral || byID["chronicler"].Lateral {
		t.Errorf("lateral must mark vault only")
	}
	if n := byID["external"]; n.Kind != nodeIngress || n.Status != "present" {
		t.Errorf("external ingress marker = %+v, want kind=ingress status=present", n)
	}
	if n := byID["object-store"]; n.Kind != nodeInfra || n.Status != "present" {
		t.Errorf("object-store plane = %+v, want kind=infra status=present", n)
	}
	wantEdges := []mapEdge{
		{From: "external", To: "gateway", Label: ""},
		{From: "gateway", To: "core-operations", Label: "stamp + publish"},
		{From: "processor", To: "vault", Label: "encrypt / decrypt"},
		{From: "core-operations", To: "chronicler", Label: "archive"},
		{From: "core-events", To: "chronicler", Label: "history"},
		{From: "core-kv", To: "chronicler", Label: "CDC"},
		{From: "chronicler", To: "object-store", Label: "archive segments"},
	}
	for _, want := range wantEdges {
		found := false
		for _, e := range m.Edges {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing skeleton edge %s → %s (%q)", want.From, want.To, want.Label)
		}
	}

	// First heartbeat moots the flag — the component goes live normally.
	docs["health.gateway.gw-1"] = map[string]any{"component": "gateway", "instance": "gw-1", "heartbeatAt": now}
	keys = append(keys, "health.gateway.gw-1")
	m = computeSystemMap(keys, read, nil, nil, time.Minute, nil, nil)
	if gw := nodesByID(m)["gateway"]; gw.Status != "green" || gw.Detail != "gw-1" {
		t.Errorf("heartbeating gateway = %+v, want green/gw-1 (designAhead moot once live)", gw)
	}
}

// A design-ahead component this process HAS seen alive must not revert to
// "not yet deployed" once its heartbeats TTL out of Health KV — that is a
// deployed-then-crashed component, and it reads honest absent-red.
func TestComputeSystemMapDesignAheadEverLive(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{"health.bootstrap.complete": {}}
	for _, dc := range declaredComponents {
		if dc.designAhead {
			continue
		}
		docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	m := computeSystemMap(keys, read, nil, nil, time.Minute, map[string]bool{"gateway": true}, nil)
	if gw := nodesByID(m)["gateway"]; gw.Status != "absent" {
		t.Errorf("ever-live gateway with no heartbeat = %q, want absent (deployed-then-crashed)", gw.Status)
	}
	if m.Overall != "red" {
		t.Errorf("overall = %q, want red (a crashed ever-live component degrades the rollup)", m.Overall)
	}
	// Vault was never seen alive — it stays design-ahead.
	if v := nodesByID(m)["vault"]; v.Status != "design-ahead" {
		t.Errorf("never-live vault = %q, want design-ahead", v.Status)
	}
}
