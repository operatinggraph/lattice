package main

import "testing"

// The F16.1 AI-review-console logic tier: row shaping/sort, display state,
// confidence banding, actionability, and the ago formatter — asserted
// against the shipped embedded asset via the goja harness.

func TestProposalDisplayState(t *testing.T) {
	vm := logicVM(t, "review.js")

	if got := call(t, vm, "proposalDisplayState", map[string]any{}); got != "authoring" {
		t.Errorf("no kind (reasoning in flight) = %v, want authoring", got)
	}
	if got := call(t, vm, "proposalDisplayState", map[string]any{"kind": "lens"}); got != "pending" {
		t.Errorf("kind set, no reviewState = %v, want pending (default)", got)
	}
	if got := call(t, vm, "proposalDisplayState", map[string]any{"kind": "lens", "reviewState": "pending"}); got != "pending" {
		t.Errorf("explicit pending = %v", got)
	}
	if got := call(t, vm, "proposalDisplayState", map[string]any{"kind": "lens", "reviewState": "approved"}); got != "approved" {
		t.Errorf("approved, no appliedAt = %v, want approved", got)
	}
	if got := call(t, vm, "proposalDisplayState", map[string]any{
		"kind": "lens", "reviewState": "approved", "appliedAt": "2026-07-18T00:00:00Z",
	}); got != "applied" {
		t.Errorf("approved + appliedAt = %v, want applied", got)
	}
	if got := call(t, vm, "proposalDisplayState", map[string]any{"kind": "lens", "reviewState": "rejected"}); got != "rejected" {
		t.Errorf("rejected = %v", got)
	}
	if got := call(t, vm, "proposalDisplayState", map[string]any{"kind": "lens", "reviewState": "invalid"}); got != "invalid" {
		t.Errorf("invalid = %v", got)
	}
}

func TestReviewStateClass(t *testing.T) {
	vm := logicVM(t, "review.js")

	cases := map[string]string{
		"authoring": "review-state authoring",
		"pending":   "review-state pending",
		"approved":  "review-state approved",
		"applied":   "review-state applied",
		"rejected":  "review-state rejected",
		"invalid":   "review-state invalid",
		"bogus":     "review-state unknown",
	}
	for state, want := range cases {
		if got := call(t, vm, "reviewStateClass", state); got != want {
			t.Errorf("reviewStateClass(%q) = %v, want %v", state, got, want)
		}
	}
}

func TestConfidenceBand(t *testing.T) {
	vm := logicVM(t, "review.js")

	cases := []struct {
		score any
		want  string
	}{
		{nil, "unknown"},
		{"0.9", "unknown"}, // a non-number never bands as a real confidence
		{0.0, "low"},
		{0.49, "low"},
		{0.5, "med"},
		{0.79, "med"},
		{0.8, "high"},
		{1.0, "high"},
	}
	for _, c := range cases {
		if got := call(t, vm, "confidenceBand", c.score); got != c.want {
			t.Errorf("confidenceBand(%v) = %v, want %v", c.score, got, c.want)
		}
	}
}

func TestIsActionable(t *testing.T) {
	vm := logicVM(t, "review.js")

	if call(t, vm, "isActionable", nil) != false {
		t.Error("nil row = not actionable")
	}
	if call(t, vm, "isActionable", map[string]any{}) != false {
		t.Error("authoring-in-flight row (no reviewState) = not actionable")
	}
	if call(t, vm, "isActionable", map[string]any{"reviewState": "pending"}) != true {
		t.Error("reviewState=pending = actionable")
	}
	if call(t, vm, "isActionable", map[string]any{"reviewState": "approved"}) != false {
		t.Error("reviewState=approved = not actionable")
	}
}

func TestAgoFrom(t *testing.T) {
	vm := logicVM(t, "review.js")
	// 2026-07-18T12:00:00Z in epoch ms.
	now := int64(1784376000000)

	if got := call(t, vm, "agoFrom", "", now); got != "" {
		t.Errorf("empty iso = %v, want empty", got)
	}
	if got := call(t, vm, "agoFrom", "not-a-timestamp", now); got != "" {
		t.Errorf("unparsable iso = %v, want empty", got)
	}
	if got := call(t, vm, "agoFrom", "2026-07-18T11:59:30Z", now); got != "30s ago" {
		t.Errorf("30s ago = %v", got)
	}
	if got := call(t, vm, "agoFrom", "2026-07-18T11:55:00Z", now); got != "5m ago" {
		t.Errorf("5m ago = %v", got)
	}
	if got := call(t, vm, "agoFrom", "2026-07-18T09:00:00Z", now); got != "3h ago" {
		t.Errorf("3h ago = %v", got)
	}
	if got := call(t, vm, "agoFrom", "2026-07-15T12:00:00Z", now); got != "3d ago" {
		t.Errorf("3d ago = %v", got)
	}
	// A future timestamp (clock skew) clamps to "0s ago", never negative.
	if got := call(t, vm, "agoFrom", "2026-07-18T12:00:30Z", now); got != "0s ago" {
		t.Errorf("future timestamp = %v, want 0s ago", got)
	}
}

func TestPendingCount(t *testing.T) {
	vm := logicVM(t, "review.js")

	if got := call(t, vm, "pendingCount", nil); got != int64(0) {
		t.Errorf("nil list = %v, want 0", got)
	}
	rows := []any{
		map[string]any{"reviewState": "pending"},
		map[string]any{"reviewState": "approved"},
		map[string]any{"reviewState": "pending"},
		map[string]any{},
	}
	if got := call(t, vm, "pendingCount", rows); got != int64(2) {
		t.Errorf("mixed list = %v, want 2", got)
	}
}

func TestProposalRows(t *testing.T) {
	vm := logicVM(t, "review.js")

	raw := []any{
		map[string]any{"proposalId": "old-pending", "reviewState": "pending", "kind": "lens", "reasonedAt": "2026-07-01T00:00:00Z"},
		map[string]any{"proposalId": "new-pending", "reviewState": "pending", "kind": "lens", "reasonedAt": "2026-07-18T00:00:00Z"},
		map[string]any{"proposalId": "approved-newest", "reviewState": "approved", "kind": "lens", "reasonedAt": "2026-07-19T00:00:00Z"},
		map[string]any{"proposalId": "authoring", "reasonedAt": "2026-07-20T00:00:00Z"},
	}
	got, ok := call(t, vm, "proposalRows", raw).([]any)
	if !ok {
		t.Fatalf("proposalRows did not return an array")
	}
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	order := make([]string, len(got))
	byID := make(map[string]map[string]any, len(got))
	for i, r := range got {
		row := r.(map[string]any)
		id := row["proposalId"].(string)
		order[i] = id
		byID[id] = row
	}
	// actionable (pending) rows first, newest reasonedAt within each group —
	// authoring's reasonedAt (07-20) outranks approved-newest's (07-19), so it
	// sorts ahead even though neither is actionable.
	want := []string{"new-pending", "old-pending", "authoring", "approved-newest"}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q (full order: %v)", i, order[i], want[i], order)
		}
	}
	first := byID["new-pending"]
	if first["displayState"] != "pending" || first["actionable"] != true {
		t.Errorf("new-pending row shape = %v", first)
	}
	authoring := byID["authoring"]
	if authoring["displayState"] != "authoring" || authoring["actionable"] != false {
		t.Errorf("authoring row shape = %v", authoring)
	}
	approved := byID["approved-newest"]
	if approved["displayState"] != "approved" || approved["actionable"] != false {
		t.Errorf("approved-newest row shape = %v", approved)
	}
}
