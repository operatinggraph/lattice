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
		{-1.0, "unknown"},  // the absent-sentinel is not a low-confidence verdict
		{1.5, "unknown"},   // nor is an out-of-range score a high-confidence one
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

// TestSourceLabel pins F25.3b's origin badge: "operator" is the only value a
// human's direct SubmitCapabilityProposal ever stamps; the bridge-recorded
// 'ai' lane AND the null the lens projects (a proposal recorded before the
// field existed, or one still reasoning) all read as "ai" — there is no
// third badge state.
func TestSourceLabel(t *testing.T) {
	vm := logicVM(t, "review.js")
	if got := call(t, vm, "sourceLabel", "operator"); got != "operator" {
		t.Errorf("sourceLabel(operator) = %v", got)
	}
	if got := call(t, vm, "sourceLabel", "ai"); got != "ai" {
		t.Errorf("sourceLabel(ai) = %v", got)
	}
	if got := call(t, vm, "sourceLabel", nil); got != "ai" {
		t.Errorf("sourceLabel(null) = %v, want ai (legacy/in-flight row)", got)
	}
	if got := call(t, vm, "sourceLabel", "bogus"); got != "ai" {
		t.Errorf("sourceLabel(bogus) = %v, want ai (unrecognized never reads as operator)", got)
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

func TestAugurDisplayState(t *testing.T) {
	vm := logicVM(t, "review.js")

	if got := call(t, vm, "augurDisplayState", map[string]any{}); got != "authoring" {
		t.Errorf("no reviewState (claim in flight) = %v, want authoring", got)
	}
	if got := call(t, vm, "augurDisplayState", map[string]any{"reviewState": "pending"}); got != "pending" {
		t.Errorf("pending = %v", got)
	}
	if got := call(t, vm, "augurDisplayState", map[string]any{"reviewState": "invalid"}); got != "invalid" {
		t.Errorf("invalid = %v", got)
	}
	if got := call(t, vm, "augurDisplayState", map[string]any{"reviewState": "approved"}); got != "approved" {
		t.Errorf("approved, no dispatchedAt = %v, want approved", got)
	}
	if got := call(t, vm, "augurDisplayState", map[string]any{
		"reviewState": "approved", "dispatchedAt": "2026-07-18T00:00:00Z",
	}); got != "dispatched" {
		t.Errorf("approved + dispatchedAt = %v, want dispatched", got)
	}
	if got := call(t, vm, "augurDisplayState", map[string]any{"reviewState": "rejected"}); got != "rejected" {
		t.Errorf("rejected = %v", got)
	}
}

func TestAugurProposalRows(t *testing.T) {
	vm := logicVM(t, "review.js")

	raw := []any{
		map[string]any{"proposalId": "low-conf-pending", "reviewState": "pending", "confidence": 0.2, "reasonedAt": "2026-07-18T00:00:00Z"},
		map[string]any{"proposalId": "high-conf-pending", "reviewState": "pending", "confidence": 0.9, "reasonedAt": "2026-07-01T00:00:00Z"},
		map[string]any{"proposalId": "dispatched-newest", "reviewState": "approved", "dispatchedAt": "2026-07-19T00:00:00Z", "reasonedAt": "2026-07-19T00:00:00Z"},
		map[string]any{"proposalId": "authoring", "reasonedAt": "2026-07-20T00:00:00Z"},
	}
	got, ok := call(t, vm, "augurProposalRows", raw).([]any)
	if !ok {
		t.Fatalf("augurProposalRows did not return an array")
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
	// pending group sorts by confidence DESCENDING (§8.4 — high before low,
	// never hidden), ahead of the non-pending group, which sorts newest first.
	want := []string{"high-conf-pending", "low-conf-pending", "authoring", "dispatched-newest"}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q (full order: %v)", i, order[i], want[i], order)
		}
	}
	dispatched := byID["dispatched-newest"]
	if dispatched["displayState"] != "dispatched" || dispatched["actionable"] != false {
		t.Errorf("dispatched-newest row shape = %v", dispatched)
	}
	authoring := byID["authoring"]
	if authoring["displayState"] != "authoring" || authoring["actionable"] != false {
		t.Errorf("authoring row shape = %v", authoring)
	}
	highConf := byID["high-conf-pending"]
	if highConf["displayState"] != "pending" || highConf["actionable"] != true {
		t.Errorf("high-conf-pending row shape = %v", highConf)
	}
}

func TestProposalRows(t *testing.T) {
	vm := logicVM(t, "review.js")

	raw := []any{
		map[string]any{"proposalId": "old-pending", "reviewState": "pending", "kind": "lens", "reasonedAt": "2026-07-01T00:00:00Z"},
		map[string]any{"proposalId": "new-pending", "reviewState": "pending", "kind": "lens", "reasonedAt": "2026-07-18T00:00:00Z", "source": "operator"},
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
	if first["source"] != "operator" {
		t.Errorf("new-pending source = %v, want operator carried through from the raw row", first["source"])
	}
	if byID["old-pending"]["source"] != "ai" {
		t.Errorf("old-pending source = %v, want ai (no source field on the raw row)", byID["old-pending"]["source"])
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

// applyOutcome is the fork between "the apply failed, try again" and "the
// apply half-committed, and only recovery can finish it" — the branch that
// decides whether re-arming "Apply now" would send the operator round a loop
// with no exit.
func TestApplyOutcome(t *testing.T) {
	vm := logicVM(t, "review.js")

	ordinary, _ := call(t, vm, "applyOutcome", map[string]any{"error": "NATS is not connected"}).(map[string]any)
	if ordinary["retryable"] != true || ordinary["resumable"] != false {
		t.Errorf("an ordinary failure = %v, want retryable + not resumable", ordinary)
	}
	if msg, _ := ordinary["message"].(string); msg != "apply failed: NATS is not connected" {
		t.Errorf("message = %q", msg)
	}
	if hint, _ := ordinary["hint"].(string); hint != "" {
		t.Errorf("an ordinary failure carries a recovery hint (%q); only the resumable branch should", hint)
	}

	half, _ := call(t, vm, "applyOutcome", map[string]any{
		"error": "apply succeeded … but MarkCapabilityProposalApplied failed", "resumable": true,
	}).(map[string]any)
	if half["resumable"] != true || half["retryable"] != false {
		t.Errorf("a half-committed apply = %v, want resumable + NOT retryable", half)
	}
	if hint, _ := half["hint"].(string); hint == "" {
		t.Error("a half-committed apply gives the operator no hint about the recovery control")
	}

	// A reply with neither field still has to produce a legible message rather
	// than "apply failed: undefined".
	empty, _ := call(t, vm, "applyOutcome", map[string]any{}).(map[string]any)
	if msg, _ := empty["message"].(string); msg != "apply failed: unknown error" {
		t.Errorf("empty body message = %q", msg)
	}

	// An op reply's error is an OBJECT, not a string, and both shapes reach
	// these status lines — concatenating the object prints "[object Object]"
	// where the reason belongs.
	obj, _ := call(t, vm, "applyOutcome", map[string]any{
		"error": map[string]any{"code": "Denied", "message": "no grant for this op"},
	}).(map[string]any)
	if msg, _ := obj["message"].(string); msg != "apply failed: no grant for this op" {
		t.Errorf("object-shaped error message = %q", msg)
	}
}

func TestErrorText(t *testing.T) {
	vm := logicVM(t, "review.js")

	cases := []struct {
		in   any
		want string
	}{
		{"plain", "plain"},
		{map[string]any{"code": "Denied", "message": "no grant"}, "no grant"},
		{map[string]any{"code": "Denied"}, "Denied"},
		{map[string]any{}, "see reply"},
		{nil, ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := call(t, vm, "errorText", c.in); got != c.want {
			t.Errorf("errorText(%v) = %v, want %q", c.in, got, c.want)
		}
	}
}

// A Processor rejection arrives as a well-formed reply with HTTP 200 and no
// error field, so a handler testing only for an error reports the platform's
// refusal to the operator as a success.
func TestOpRejected(t *testing.T) {
	vm := logicVM(t, "review.js")

	if got := call(t, vm, "opRejected", map[string]any{"status": "rejected"}); got != true {
		t.Errorf("rejected reply = %v", got)
	}
	if got := call(t, vm, "opRejected", map[string]any{"status": "accepted"}); got != false {
		t.Errorf("accepted reply = %v", got)
	}
	if got := call(t, vm, "opRejected", map[string]any{"status": "duplicate"}); got != false {
		t.Errorf("duplicate reply = %v", got)
	}
	if got := call(t, vm, "opRejected", nil); got != false {
		t.Errorf("absent reply = %v, want false (a reply the console never got is not a rejection)", got)
	}
}

// A reviewer must never be asked to approve an in-place change to an installed
// artifact under a label that reads like a fresh install. The two acts differ,
// and only one of them can overwrite something that is already there.
func TestInstallTargetLabel(t *testing.T) {
	vm := logicVM(t, "review.js")

	edit := call(t, vm, "installTargetLabel", map[string]any{
		"targetMode":        "upgradeExisting",
		"targetPackageName": "weaver-target-leasecomplete-k3f9",
		"targetBaseVersion": "0.1.0",
		"targetNewVersion":  "0.1.1",
		"content":           `{"targetId":"leaseComplete","lensRef":"leaseViolations","gaps":{}}`,
	}).(string)
	for _, want := range []string{"edits", "leaseComplete", "weaver-target-leasecomplete-k3f9", "0.1.0", "0.1.1"} {
		if !containsSub(edit, want) {
			t.Errorf("label %q does not name %q", edit, want)
		}
	}

	// The artifact content is the only place the targetId lives — an unparsable
	// one still yields a label naming the package and the versions, never a
	// half-built sentence with an empty subject.
	noID := call(t, vm, "installTargetLabel", map[string]any{
		"targetMode": "upgradeExisting", "targetPackageName": "weaver-target-x-1",
		"targetBaseVersion": "0.1.0", "targetNewVersion": "0.1.1", "content": "{not json",
	}).(string)
	if containsSub(noID, "undefined") || !containsSub(noID, "edits weaver-target-x-1") {
		t.Errorf("label = %q, want a package-only edit sentence", noID)
	}

	// An upgrade missing a version is refused at apply. The label shows the gap
	// rather than dropping it and reading like a well-formed edit.
	gap := call(t, vm, "installTargetLabel", map[string]any{
		"targetMode": "upgradeExisting", "targetPackageName": "p", "content": "",
	}).(string)
	if !containsSub(gap, "?") {
		t.Errorf("label = %q, want the missing versions visible", gap)
	}

	// The from-scratch line is unchanged.
	fresh := call(t, vm, "installTargetLabel", map[string]any{
		"targetMode": "newPackage", "targetPackageName": "weaver-target-x-abc", "targetNewVersion": "0.1.0",
	}).(string)
	if fresh != "newPackage weaver-target-x-abc@0.1.0" {
		t.Errorf("label = %q, want the newPackage line unchanged", fresh)
	}

	// A row with no target yet (reasoning still in flight) labels nothing,
	// which is what lets the card omit the line entirely.
	for _, row := range []any{nil, map[string]any{}, map[string]any{"targetMode": "newPackage"}} {
		if got := call(t, vm, "installTargetLabel", row); got != "" {
			t.Errorf("installTargetLabel(%v) = %q, want empty", row, got)
		}
	}
}

func TestArtifactTargetId(t *testing.T) {
	vm := logicVM(t, "review.js")
	if got := call(t, vm, "artifactTargetId", `{"targetId":"leaseComplete"}`); got != "leaseComplete" {
		t.Errorf("artifactTargetId = %v, want leaseComplete", got)
	}
	// An AI-authored artifact is not guaranteed well-formed at record time, and
	// a non-string targetId is not a targetId.
	for _, content := range []any{nil, "", "{not json", "[]", `{"targetId":42}`, `{"canonicalName":"x"}`} {
		if got := call(t, vm, "artifactTargetId", content); got != "" {
			t.Errorf("artifactTargetId(%v) = %v, want empty", content, got)
		}
	}
}

// The queue card reads its label off the shaped row, so proposalRows has to
// carry it (and the baseVersion it is built from) rather than leaving the view
// to re-derive them.
func TestProposalRowsCarryTheInstallLabel(t *testing.T) {
	vm := logicVM(t, "review.js")
	rows := call(t, vm, "proposalRows", []any{map[string]any{
		"key": "vtx.capabilityproposal.p1", "proposalId": "p1", "kind": "weaverTarget",
		"targetMode": "upgradeExisting", "targetPackageName": "weaver-target-leasecomplete-k3f9",
		"targetBaseVersion": "0.1.0", "targetNewVersion": "0.1.1",
		"content": `{"targetId":"leaseComplete"}`,
	}}).([]any)
	row := rows[0].(map[string]any)
	if row["targetBaseVersion"] != "0.1.0" {
		t.Errorf("targetBaseVersion = %v, want it carried onto the row", row["targetBaseVersion"])
	}
	label, _ := row["installLabel"].(string)
	if !containsSub(label, "edits leaseComplete") {
		t.Errorf("installLabel = %q, want the edit spelled out on the queue card", label)
	}
}
