package main

import "testing"

// The Flows logic tier: how a row's kind is read off the history/engine pair,
// how the wall is ordered, and how it collapses into per-pattern groups.

func TestFlowKindAndLabel(t *testing.T) {
	vm := logicVM(t, "flows.js")

	cases := []struct {
		name string
		row  map[string]any
		kind string
	}{
		{"terminal row is its own status", map[string]any{"status": "complete"}, "complete"},
		{"a failure is a failure", map[string]any{"status": "failed"}, "failed"},
		{"running + live", map[string]any{"status": "running", "liveness": "live"}, "live"},
		{"running + stale history", map[string]any{"status": "running", "liveness": "stale-history"}, "stale-history"},
		{"running + orphaned", map[string]any{"status": "running", "liveness": "orphaned"}, "orphaned"},
		// No verdict means the control read failed; the row stays plainly
		// running rather than borrowing a verdict it never got.
		{"running with no verdict", map[string]any{"status": "running"}, "running"},
	}
	for _, c := range cases {
		if got := call(t, vm, "flowKind", c.row); got != c.kind {
			t.Errorf("%s: flowKind = %v, want %q", c.name, got, c.kind)
		}
	}

	// "stale-history" alone is an accusation with no object — the operator
	// needs both voices to tell a broken flow from a lagging projection.
	label := call(t, vm, "flowLabel", map[string]any{
		"status": "running", "liveness": "stale-history", "engineStatus": "complete",
	})
	if s, _ := label.(string); s != "history stale · Loom says complete" {
		t.Errorf("stale-history label = %q, want both voices named", s)
	}
	if s, _ := call(t, vm, "flowLabel", map[string]any{"status": "running", "liveness": "orphaned"}).(string); s != "orphaned · Loom has no record" {
		t.Errorf("orphaned label = %q", s)
	}
}

// The card title is the pattern an operator recognizes, not the NanoID the
// read model happens to store.
func TestPatternLabel(t *testing.T) {
	vm := logicVM(t, "flows.js")

	if got := call(t, vm, "patternLabel", map[string]any{
		"patternName": "identityOnboarding", "patternRef": "vtx.meta.abc", "instanceId": "i1",
	}); got != "identityOnboarding" {
		t.Errorf("resolved name should win, got %v", got)
	}
	if got := call(t, vm, "patternLabel", map[string]any{"patternRef": "vtx.meta.abc", "instanceId": "i1"}); got != "vtx.meta.abc" {
		t.Errorf("unresolved should fall back to the ref, got %v", got)
	}
	if got := call(t, vm, "patternLabel", map[string]any{"instanceId": "i1"}); got != "i1" {
		t.Errorf("with neither, fall back to the instance rather than a blank title, got %v", got)
	}
}

// Exception-first: every liveness disagreement outranks a plain running row,
// because each is a finding and a running flow is not.
func TestFlowRows_ExceptionFirst(t *testing.T) {
	vm := logicVM(t, "flows.js")

	out, _ := call(t, vm, "flowRows", []any{
		map[string]any{"instanceId": "c1", "status": "complete", "startedAt": "2026-07-05T12:00:00Z"},
		map[string]any{"instanceId": "r1", "status": "running", "liveness": "live", "startedAt": "2026-07-05T11:00:00Z"},
		map[string]any{"instanceId": "o1", "status": "running", "liveness": "orphaned", "startedAt": "2026-07-05T10:00:00Z"},
		map[string]any{"instanceId": "s1", "status": "running", "liveness": "stale-history", "startedAt": "2026-07-05T09:00:00Z"},
		map[string]any{"instanceId": "f1", "status": "failed", "startedAt": "2026-07-05T08:00:00Z"},
	}).([]any)

	order := make([]string, 0, len(out))
	for _, r := range out {
		row, _ := r.(map[string]any)
		order = append(order, row["instanceId"].(string))
	}
	want := []string{"f1", "s1", "o1", "r1", "c1"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v (failed · stale · orphaned · running · complete)", order, want)
		}
	}

	// A stale row must not render in the calm colour its own claim earns.
	stale, _ := out[1].(map[string]any)
	if stale["cls"] != "red" {
		t.Errorf("stale-history class = %v, want red — its own status says running", stale["cls"])
	}
}

func TestFlowRows_NewestFirstWithinASeverity(t *testing.T) {
	vm := logicVM(t, "flows.js")

	out, _ := call(t, vm, "flowRows", []any{
		map[string]any{"instanceId": "old", "status": "complete", "startedAt": "2026-07-05T08:00:00Z"},
		map[string]any{"instanceId": "new", "status": "complete", "startedAt": "2026-07-05T12:00:00Z"},
	}).([]any)
	first, _ := out[0].(map[string]any)
	if first["instanceId"] != "new" {
		t.Errorf("within one severity the newest start sorts first, got %v", first["instanceId"])
	}
}

// A group holding the only failure has to sort to the top however few rows it
// holds — otherwise grouping buries the finding that grouping was meant to
// surface.
func TestGroupFlowsByPattern(t *testing.T) {
	vm := logicVM(t, "flows.js")

	rows := call(t, vm, "flowRows", []any{
		map[string]any{"instanceId": "a1", "patternRef": "vtx.meta.aaa", "patternName": "bulk", "status": "complete", "startedAt": "2026-07-05T12:00:00Z"},
		map[string]any{"instanceId": "a2", "patternRef": "vtx.meta.aaa", "patternName": "bulk", "status": "complete", "startedAt": "2026-07-05T11:00:00Z"},
		map[string]any{"instanceId": "a3", "patternRef": "vtx.meta.aaa", "patternName": "bulk", "status": "complete", "startedAt": "2026-07-05T10:00:00Z"},
		map[string]any{"instanceId": "b1", "patternRef": "vtx.meta.bbb", "patternName": "rare", "status": "failed", "startedAt": "2026-07-05T09:00:00Z"},
	})
	groups, _ := call(t, vm, "groupFlowsByPattern", rows).([]any)

	if len(groups) != 2 {
		t.Fatalf("want 2 pattern groups, got %d", len(groups))
	}
	first, _ := groups[0].(map[string]any)
	if first["pattern"] != "rare" {
		t.Errorf("the group holding the failure sorts first, got %v (a 3-row healthy group must not outrank it)", first["pattern"])
	}
	second, _ := groups[1].(map[string]any)
	secondRows, _ := second["rows"].([]any)
	if len(secondRows) != 3 {
		t.Errorf("second group rows = %d, want 3", len(secondRows))
	}

	if s, _ := call(t, vm, "groupSummary", first).(string); s != "1 failed" {
		t.Errorf("group summary = %q", s)
	}
}

// The headline leads with what needs attention; "26 flows" answers a question
// nobody asked.
func TestFlowsHeadline(t *testing.T) {
	vm := logicVM(t, "flows.js")

	healthy := call(t, vm, "flowRows", []any{
		map[string]any{"instanceId": "c1", "status": "complete"},
		map[string]any{"instanceId": "r1", "status": "running", "liveness": "live"},
	})
	if s, _ := call(t, vm, "flowsHeadline", healthy).(string); s != "2 flows · all healthy" {
		t.Errorf("healthy headline = %q", s)
	}

	mixed := call(t, vm, "flowRows", []any{
		map[string]any{"instanceId": "c1", "status": "complete"},
		map[string]any{"instanceId": "s1", "status": "running", "liveness": "stale-history"},
		map[string]any{"instanceId": "f1", "status": "failed"},
	})
	if s, _ := call(t, vm, "flowsHeadline", mixed).(string); s != "2 of 3 flows need attention" {
		t.Errorf("mixed headline = %q", s)
	}

	if s, _ := call(t, vm, "flowsHeadline", []any{}).(string); s != "no flows" {
		t.Errorf("empty headline = %q", s)
	}
}
