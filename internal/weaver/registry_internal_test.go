package weaver

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestSource(t *testing.T) *targetSource {
	t.Helper()
	return newTargetSource(nil, "core-kv", "test", newIssueCache(), discardLogger())
}

func testNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	return id
}

func vertexEvent(t *testing.T, id, class string) substrate.KVEvent {
	t.Helper()
	body, err := json.Marshal(map[string]any{"class": class, "data": map[string]any{}})
	if err != nil {
		t.Fatalf("marshal vertex: %v", err)
	}
	return substrate.KVEvent{Key: "vtx.meta." + id, Value: body}
}

func specEvent(t *testing.T, id string, spec map[string]any) substrate.KVEvent {
	t.Helper()
	body, err := json.Marshal(map[string]any{"class": "spec", "data": spec})
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	return substrate.KVEvent{Key: "vtx.meta." + id + ".spec", Value: body}
}

func targetSpecFixture(targetID string) map[string]any {
	return map[string]any{
		"targetId": targetID,
		"lensRef":  "lensFixture",
		"gaps": map[string]any{
			"missing_a": map[string]any{"action": "directOp", "operation": "FixA"},
		},
	}
}

func hasIssueCode(issues []healthIssue, code string) bool {
	for _, i := range issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

// TestRegistry_SpecAspectDeleteThenRecreate proves the spec-aspect lifecycle:
// deleting a spec ASPECT unregisters the target but keeps the vertex's class
// entry (the vertex still exists), so a re-created spec registers immediately
// instead of buffering forever.
func TestRegistry_SpecAspectDeleteThenRecreate(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	id := testNanoID(t)
	const targetID = "fixtureLifecycle"

	s.handle(vertexEvent(t, id, weaverTargetClass))
	s.handle(specEvent(t, id, targetSpecFixture(targetID)))
	if _, ok := s.target(targetID); !ok {
		t.Fatalf("target %q must register after vertex+spec", targetID)
	}

	// Delete the spec ASPECT (not the vertex).
	s.handle(substrate.KVEvent{Key: "vtx.meta." + id + ".spec", IsDeleted: true})
	if _, ok := s.target(targetID); ok {
		t.Fatalf("target %q must unregister when its spec aspect is deleted", targetID)
	}
	s.mu.Lock()
	class, classKept := s.classes[id]
	pending := len(s.pendingSpecs)
	s.mu.Unlock()
	if !classKept || class != weaverTargetClass {
		t.Fatalf("a spec-aspect delete must keep the vertex's class entry (got %q, kept=%v)", class, classKept)
	}
	if pending != 0 {
		t.Fatalf("a spec-aspect delete must evict any pending buffer, got %d entries", pending)
	}

	// Re-create the spec: it must register immediately (no pending buffer).
	s.handle(specEvent(t, id, targetSpecFixture(targetID)))
	if _, ok := s.target(targetID); !ok {
		t.Fatalf("a re-created spec under a live vertex must register, not buffer")
	}
	s.mu.Lock()
	pending = len(s.pendingSpecs)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("re-created spec must not buffer, got %d pending entries", pending)
	}
}

// TestRegistry_PendingSpecBounds proves the pending-spec buffer is bounded: a
// spec buffered ahead of its vertex is dropped once the class is learned to be
// non-routed, a spec for a known non-routed class is never buffered, and a
// vertex delete evicts the buffer.
func TestRegistry_PendingSpecBounds(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)

	// Spec arrives before its vertex → buffered.
	id := testNanoID(t)
	s.handle(specEvent(t, id, map[string]any{"some": "lensSpec"}))
	s.mu.Lock()
	pending := len(s.pendingSpecs)
	s.mu.Unlock()
	if pending != 1 {
		t.Fatalf("spec-before-vertex must buffer, got %d pending entries", pending)
	}

	// The vertex turns out non-routed → the buffer drops.
	s.handle(vertexEvent(t, id, "meta.lens"))
	s.mu.Lock()
	pending = len(s.pendingSpecs)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("learning a non-routed class must drop the pending spec, got %d entries", pending)
	}

	// A later spec write for the known non-routed vertex is never buffered.
	s.handle(specEvent(t, id, map[string]any{"some": "lensSpec2"}))
	s.mu.Lock()
	pending = len(s.pendingSpecs)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("a spec for a known non-routed class must not buffer, got %d entries", pending)
	}

	// Vertex delete evicts a pending spec.
	id2 := testNanoID(t)
	s.handle(specEvent(t, id2, map[string]any{"some": "spec"}))
	s.handle(substrate.KVEvent{Key: "vtx.meta." + id2, IsDeleted: true})
	s.mu.Lock()
	pending = len(s.pendingSpecs)
	s.mu.Unlock()
	if pending != 0 {
		t.Fatalf("a vertex delete must evict its pending spec, got %d entries", pending)
	}
}

// TestRegistry_OrphanedSpecHealthIssue proves a spec stuck pending past the
// bound surfaces as a Health issue (never silent) and that the issue clears
// once the parent vertex's class arrives.
func TestRegistry_OrphanedSpecHealthIssue(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	id := testNanoID(t)
	s.handle(specEvent(t, id, targetSpecFixture("fixtureOrphan")))

	// Not yet past the bound: no issue.
	s.flagOrphanedSpecs()
	if hasIssueCode(s.issues.snapshot(), "OrphanedSpec") {
		t.Fatalf("a freshly-buffered spec must not be flagged as orphaned")
	}

	// Backdate the pending entry past the bound.
	s.mu.Lock()
	s.pendingSince[id] = time.Now().Add(-pendingSpecWarnAfter - time.Minute)
	s.mu.Unlock()
	s.flagOrphanedSpecs()
	if !hasIssueCode(s.issues.snapshot(), "OrphanedSpec") {
		t.Fatalf("a spec pending past the bound must surface an OrphanedSpec Health issue")
	}

	// The vertex finally arrives: the spec drains and the issue clears.
	s.handle(vertexEvent(t, id, weaverTargetClass))
	if hasIssueCode(s.issues.snapshot(), "OrphanedSpec") {
		t.Fatalf("the OrphanedSpec issue must clear once the spec drains")
	}
	if _, ok := s.target("fixtureOrphan"); !ok {
		t.Fatalf("the drained spec must register its target")
	}
}

// TestValidateTarget_GapColumnCharsetAndReservedParam proves the install-time
// validations: a gaps key with characters invalid in a KV key segment is
// rejected (it becomes a mark-key segment), and a playbook param named
// expectedRevision (the engine-owned payload field) is rejected instead of
// silently clobbered at dispatch.
func TestValidateTarget_GapColumnCharsetAndReservedParam(t *testing.T) {
	t.Parallel()
	valid := &Target{
		TargetID: "fixtureValid",
		Gaps: map[string]GapAction{
			"missing_a": {Action: actionDirectOp, Operation: "FixA", Params: map[string]string{"note": "x"}},
		},
	}
	if err := validateTarget(valid); err != nil {
		t.Fatalf("valid target must pass: %v", err)
	}

	for _, col := range []string{"missing_bg check", "missing_bg.check", "missing_bg*", "missing_bg>"} {
		bad := &Target{
			TargetID: "fixtureBadCol",
			Gaps:     map[string]GapAction{col: {Action: actionDirectOp, Operation: "Fix"}},
		}
		err := validateTarget(bad)
		if err == nil {
			t.Fatalf("gaps key %q must be rejected (invalid KV key segment)", col)
		}
		if !strings.Contains(err.Error(), "invalid in a KV key segment") {
			t.Fatalf("gaps key %q: unexpected rejection reason: %v", col, err)
		}
	}

	reserved := &Target{
		TargetID: "fixtureReserved",
		Gaps: map[string]GapAction{
			"missing_a": {Action: actionDirectOp, Operation: "FixA",
				Params: map[string]string{"expectedRevision": "row.someRev"}},
		},
	}
	err := validateTarget(reserved)
	if err == nil {
		t.Fatalf("a param named expectedRevision must be rejected at install time")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("unexpected rejection reason for reserved param: %v", err)
	}
}

func TestValidateTarget_AugurPolicy(t *testing.T) {
	t.Parallel()

	withAugur := func(a *AugurPolicy) *Target {
		return &Target{
			TargetID: "fixtureAugur",
			Gaps:     map[string]GapAction{"missing_a": {Action: actionDirectOp, Operation: "FixA"}},
			Augur:    a,
		}
	}

	// A nil augur block is the frozen-contract default — always valid.
	if err := validateTarget(withAugur(nil)); err != nil {
		t.Fatalf("a target with no augur block must pass: %v", err)
	}

	// The minimal valid block: one known trigger, no overrides (Op/Adapter/ReplyOp
	// default at dispatch). autoApply absent.
	if err := validateTarget(withAugur(&AugurPolicy{
		Escalate: []string{escalateUnplannable},
	})); err != nil {
		t.Fatalf("a minimal valid augur block must pass: %v", err)
	}

	// A fully-populated valid block: both triggers, the op/adapter/replyOp
	// overrides, a model override, and a well-formed autoApply allow-list (parsed +
	// validated even though no escalation path consumes it yet).
	if err := validateTarget(withAugur(&AugurPolicy{
		Escalate: []string{escalateUnplannable, escalateExhausted},
		Op:       "CreateAugurReasoningClaim",
		Adapter:  "augur",
		ReplyOp:  "RecordProposal",
		Model:    "claude-opus-4-8",
		AutoApply: &AugurAutoApply{
			Actions: []string{actionTriggerLoom, actionDirectOp}, MinConfidence: 0.9,
		},
	})); err != nil {
		t.Fatalf("a fully-populated valid augur block must pass: %v", err)
	}

	cases := []struct {
		name    string
		policy  *AugurPolicy
		wantSub string
	}{
		{"empty escalate", &AugurPolicy{}, "escalate is empty"},
		{"unknown trigger", &AugurPolicy{Escalate: []string{"someday"}}, "not a known trigger"},
		{"bad op token", &AugurPolicy{Escalate: []string{escalateUnplannable}, Op: "bad.op"}, "single token"},
		{"bad adapter token", &AugurPolicy{Escalate: []string{escalateUnplannable}, Adapter: "a b"}, "single token"},
		{"bad autoApply action", &AugurPolicy{Escalate: []string{escalateUnplannable},
			AutoApply: &AugurAutoApply{Actions: []string{"DropTable"}}}, "not a known action"},
		{"minConfidence too high", &AugurPolicy{Escalate: []string{escalateUnplannable},
			AutoApply: &AugurAutoApply{MinConfidence: 1.5}}, "out of range"},
		{"minConfidence negative", &AugurPolicy{Escalate: []string{escalateUnplannable},
			AutoApply: &AugurAutoApply{MinConfidence: -0.1}}, "out of range"},
	}
	for _, tc := range cases {
		err := validateTarget(withAugur(tc.policy))
		if err == nil {
			t.Fatalf("%s: must be rejected", tc.name)
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Fatalf("%s: unexpected rejection reason: %v", tc.name, err)
		}
	}
}

// TestRegistry_AugurBlockRoundTrips proves the augur block survives the
// spec-aspect unwrap + JSON unmarshal the CDC source runs (the path a
// pkgmgr-emitted body takes into a runtime Target).
func TestRegistry_AugurBlockRoundTrips(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	id := testNanoID(t)
	spec := targetSpecFixture("augurRoundTrip")
	spec["augur"] = map[string]any{
		"escalate":  []any{"unplannable"},
		"op":        "CreateAugurReasoningClaim",
		"adapter":   "augur",
		"replyOp":   "RecordProposal",
		"model":     "claude-opus-4-8",
		"autoApply": map[string]any{"actions": []any{"triggerLoom"}, "minConfidence": 0.8},
	}
	s.handle(vertexEvent(t, id, weaverTargetClass))
	s.handle(specEvent(t, id, spec))

	tgt, ok := s.target("augurRoundTrip")
	if !ok {
		t.Fatalf("target augurRoundTrip not registered after augur-bearing spec")
	}
	if tgt.Augur == nil {
		t.Fatalf("augur block dropped on unmarshal")
	}
	if len(tgt.Augur.Escalate) != 1 || tgt.Augur.Escalate[0] != escalateUnplannable {
		t.Fatalf("escalate not round-tripped: %+v", tgt.Augur.Escalate)
	}
	if tgt.Augur.Op != "CreateAugurReasoningClaim" || tgt.Augur.Adapter != "augur" ||
		tgt.Augur.ReplyOp != "RecordProposal" || tgt.Augur.Model != "claude-opus-4-8" {
		t.Fatalf("op/adapter/replyOp/model not round-tripped: %+v", tgt.Augur)
	}
	if tgt.Augur.AutoApply == nil || tgt.Augur.AutoApply.MinConfidence != 0.8 {
		t.Fatalf("autoApply not round-tripped: %+v", tgt.Augur.AutoApply)
	}
}

// TestValidateTarget_Mode proves the §10.8 Planner-extension `mode` field is
// install-validated: absent and the two known values pass; anything else
// rejects the whole target.
func TestValidateTarget_Mode(t *testing.T) {
	t.Parallel()
	base := func(mode string) *Target {
		return &Target{
			TargetID: "fixtureMode",
			Mode:     mode,
			Gaps:     map[string]GapAction{"missing_a": {Action: actionDirectOp, Operation: "FixA"}},
		}
	}
	for _, mode := range []string{"", targetModeShadow, targetModePlanned} {
		if err := validateTarget(base(mode)); err != nil {
			t.Fatalf("mode %q must pass: %v", mode, err)
		}
	}
	err := validateTarget(base("eager"))
	if err == nil {
		t.Fatalf("an unknown mode must be rejected")
	}
	if !strings.Contains(err.Error(), "not a known planner mode") {
		t.Fatalf("unexpected rejection reason: %v", err)
	}
}

// TestValidateTarget_Candidates proves a gap's `candidates` list is
// install-validated: each entry needs a non-empty action and a non-negative
// cost, and a well-formed `pre` guard parses (rejecting a malformed one) —
// mirroring the op-DDL effects fail-wholesale doctrine.
func TestValidateTarget_Candidates(t *testing.T) {
	t.Parallel()
	valid := &Target{
		TargetID: "fixtureCandidates",
		Mode:     targetModeShadow,
		Gaps: map[string]GapAction{
			"missing_a": {Candidates: []GapCandidate{
				{Action: "FixA", Cost: 1},
				{Action: "FixB", Cost: 2, Pre: json.RawMessage(`{"present":"subject.data.applicant"}`)},
			}},
		},
	}
	if err := validateTarget(valid); err != nil {
		t.Fatalf("valid candidates must pass: %v", err)
	}
	if got := valid.Gaps["missing_a"].Candidates[1].preGuard; got == nil {
		t.Fatalf("a well-formed pre guard must be parsed and cached")
	}

	noAction := &Target{TargetID: "fixtureCandNoAction", Gaps: map[string]GapAction{
		"missing_a": {Candidates: []GapCandidate{{Cost: 1}}},
	}}
	err := validateTarget(noAction)
	if err == nil || !strings.Contains(err.Error(), "has no action") {
		t.Fatalf("a candidate with no action must be rejected: %v", err)
	}

	negCost := &Target{TargetID: "fixtureCandNegCost", Gaps: map[string]GapAction{
		"missing_a": {Candidates: []GapCandidate{{Action: "FixA", Cost: -1}}},
	}}
	err = validateTarget(negCost)
	if err == nil || !strings.Contains(err.Error(), "must be >= 0") {
		t.Fatalf("a negative cost must be rejected: %v", err)
	}

	badPre := &Target{TargetID: "fixtureCandBadPre", Gaps: map[string]GapAction{
		"missing_a": {Candidates: []GapCandidate{
			{Action: "FixA", Pre: json.RawMessage(`{"bogus":"x"}`)},
		}},
	}}
	err = validateTarget(badPre)
	if err == nil {
		t.Fatalf("a malformed pre guard must reject the whole target")
	}
}

// TestValidateTarget_Goal proves a gap's `goal` parses as a well-formed §10.5
// guard (rejecting a malformed one) and is cached on the parsed target — not
// yet consumed anywhere (Fire 6).
func TestValidateTarget_Goal(t *testing.T) {
	t.Parallel()
	valid := &Target{
		TargetID: "fixtureGoal",
		Gaps: map[string]GapAction{
			"missing_a": {Goal: json.RawMessage(`{"present":"subject.data.signature"}`)},
		},
	}
	if err := validateTarget(valid); err != nil {
		t.Fatalf("a well-formed goal must pass: %v", err)
	}
	if valid.Gaps["missing_a"].goalGuard == nil {
		t.Fatalf("a well-formed goal must be parsed and cached")
	}

	bad := &Target{TargetID: "fixtureBadGoal", Gaps: map[string]GapAction{
		"missing_a": {Goal: json.RawMessage(`{"bogus":"x"}`)},
	}}
	if err := validateTarget(bad); err == nil {
		t.Fatalf("a malformed goal must reject the whole target")
	}
}
