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
		"AbcLensId0000000000":                  {"status": "active"},
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

	m := computeSystemMap(keys, read, resolve, nil, 60*time.Second)
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

	// A refractor→lens project edge exists.
	foundLensEdge := false
	for _, e := range m.Edges {
		if e.From == refractorID && e.To == "AbcLensId0000000000" && e.Label == "project" {
			foundLensEdge = true
		}
	}
	if !foundLensEdge {
		t.Errorf("missing refractor→lens project edge; edges=%+v", m.Edges)
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

	m := computeSystemMap(keys, read, nil, nil, 60*time.Second)
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

	m := computeSystemMap(keys, read, nil, nil, time.Minute)
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

	m := computeSystemMap(keys, read, func(string) (string, string) { return "", "" }, nil, time.Minute)
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

	m := computeSystemMap(keys, read, nil, nil, time.Minute)
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

// An undeclared heartbeat group (a vertical app's reporter) becomes a "client"
// node with the same per-instance overlay and no skeleton edges.
func TestComputeSystemMapClientNodes(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{}
	for _, dc := range declaredComponents {
		docs["health."+dc.id+".inst"] = map[string]any{"component": dc.id, "instance": "inst", "heartbeatAt": now}
	}
	docs["health.bootstrap.complete"] = map[string]any{}
	docs["health.loftspace-app.web-1"] = map[string]any{"component": "loftspace-app", "instance": "web-1", "heartbeatAt": now}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	m := computeSystemMap(keys, read, nil, nil, time.Minute)
	app := nodesByID(m)["loftspace-app"]
	if app.Kind != nodeClient || app.Status != "green" || len(app.Instances) != 1 {
		t.Errorf("client node = %+v, want kind=client green with 1 instance", app)
	}
	for _, e := range m.Edges {
		if e.From == "loftspace-app" || e.To == "loftspace-app" {
			t.Errorf("client node must not gain skeleton edges, got %+v", e)
		}
	}
	if m.Overall != "green" {
		t.Errorf("overall = %q, want green (client is healthy)", m.Overall)
	}
}
