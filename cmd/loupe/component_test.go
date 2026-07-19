package main

import (
	"testing"
	"time"
)

func TestComputeComponentPluralInstancesAndEvents(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	stale := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	older := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{
		"health.processor.proc-a": {"component": "processor", "instance": "proc-a", "heartbeatAt": now,
			"metrics": map[string]any{"ops_consumed_total": float64(12)}},
		"health.processor.proc-b": {"component": "processor", "instance": "proc-b", "heartbeatAt": stale},
		// Component-scoped events — deeper keys under the same group.
		"health.processor.proc-a.malformed-operation.r1": {"at": older, "reason": "bad payload"},
		"health.processor.proc-a.step3-latency":          {"at": now, "p95": float64(3)},
		// Other groups + non-component keys must not leak in.
		"health.loom.loom-1":        {"component": "loom", "instance": "loom-1", "heartbeatAt": now},
		"health.alerts.security.x":  {"severity": "warning", "message": "stub"},
		"health.bootstrap.complete": {"completedAt": now},
		"SomeLensId0000000000":      {"status": "active"},
	}
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }

	page := computeComponent("processor", keys, read, time.Minute, nil)
	if !page.Declared || page.Label != "Processor" {
		t.Errorf("page identity = %+v, want declared Processor", page)
	}
	if len(page.Instances) != 2 {
		t.Fatalf("instances = %+v, want 2", page.Instances)
	}
	if page.Instances[0].Instance != "proc-a" || page.Instances[1].Instance != "proc-b" {
		t.Errorf("instances not sorted: %+v", page.Instances)
	}
	// Worst instance (stale proc-b) drives the page status.
	if page.Status != "stale" {
		t.Errorf("page status = %q, want stale (worst-of)", page.Status)
	}
	// The raw doc rides along for the metrics line.
	if m, ok := page.Instances[0].Doc["metrics"].(map[string]any); !ok || m["ops_consumed_total"] != float64(12) {
		t.Errorf("instance doc not carried verbatim: %+v", page.Instances[0].Doc)
	}

	if len(page.Events) != 2 {
		t.Fatalf("events = %+v, want 2", page.Events)
	}
	// Newest-first: step3-latency (now) before malformed-operation (older).
	if page.Events[0].Kind != "step3-latency" || page.Events[1].Kind != "malformed-operation" {
		t.Errorf("event order/kinds = %q,%q, want step3-latency,malformed-operation",
			page.Events[0].Kind, page.Events[1].Kind)
	}
	if page.Events[1].Tail != "proc-a.malformed-operation.r1" {
		t.Errorf("event tail = %q", page.Events[1].Tail)
	}
}

func TestComputeComponentAbsentAndUndeclared(t *testing.T) {
	page := computeComponent("weaver", nil, func(string) (map[string]any, bool) { return nil, false }, time.Minute, nil)
	if !page.Declared || page.Status != "absent" || len(page.Instances) != 0 {
		t.Errorf("absent declared component = %+v, want declared/absent/no instances", page)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	docs := map[string]map[string]any{
		"health.clinic-app.c1": {"component": "clinic-app", "instance": "c1", "heartbeatAt": now},
	}
	read := func(k string) (map[string]any, bool) { d, ok := docs[k]; return d, ok }
	page = computeComponent("clinic-app", []string{"health.clinic-app.c1"}, read, time.Minute, nil)
	if page.Declared || page.Label != "clinic-app" || page.Status != "green" || len(page.Instances) != 1 {
		t.Errorf("undeclared client page = %+v, want undeclared green with 1 instance", page)
	}
}

// An optional declared component's page agrees with its map node: with no
// heartbeat and never-seen-alive the header pill reads "offline" (informational,
// up-full only), and a live instance overwrites it with the normal worst-of.
func TestComputeComponentOptionalOffline(t *testing.T) {
	page := computeComponent("vault", nil, func(string) (map[string]any, bool) { return nil, false }, time.Minute, nil)
	if !page.Declared || page.Status != "offline" {
		t.Errorf("vault page = declared=%v status=%q, want declared/offline", page.Declared, page.Status)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	read := func(k string) (map[string]any, bool) {
		if k == "health.vault.v1" {
			return map[string]any{"component": "vault", "instance": "v1", "heartbeatAt": now}, true
		}
		return nil, false
	}
	page = computeComponent("vault", []string{"health.vault.v1"}, read, time.Minute, nil)
	if page.Status != "green" {
		t.Errorf("heartbeating vault page status = %q, want green (optional flag moot once live)", page.Status)
	}
}

// The component page applies the same ever-live gate as the map: an optional
// component seen alive earlier this process reports absent, not offline.
func TestComputeComponentOptionalEverLive(t *testing.T) {
	page := computeComponent("vault", nil, func(string) (map[string]any, bool) { return nil, false },
		time.Minute, map[string]bool{"vault": true})
	if page.Status != "absent" {
		t.Errorf("ever-live vault page status = %q, want absent", page.Status)
	}
}
