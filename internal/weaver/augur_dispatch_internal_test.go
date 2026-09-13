package weaver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- materializeGapAction ----------------------------------------------------

func TestMaterializeGapAction_AssignTask(t *testing.T) {
	t.Parallel()
	ga, err := materializeGapAction("assignTask", map[string]any{
		"operation": "ApproveLeaseApplication",
		"assignee":  "vtx.identity.SomeStaffHJKMNPQR1",
		"target":    "vtx.leaseapp.CandidateHJKMNPQR1",
	})
	if err != nil {
		t.Fatalf("materializeGapAction: %v", err)
	}
	if ga.Action != actionAssignTask || ga.Operation != "ApproveLeaseApplication" ||
		ga.Assignee != "vtx.identity.SomeStaffHJKMNPQR1" || ga.Target != "vtx.leaseapp.CandidateHJKMNPQR1" {
		t.Fatalf("materialized assignTask = %+v", ga)
	}
}

func TestMaterializeGapAction_TriggerLoom(t *testing.T) {
	t.Parallel()
	ga, err := materializeGapAction("triggerLoom", map[string]any{
		"pattern": "backgroundCheck",
		"subject": "vtx.leaseapp.CandidateHJKMNPQR1",
	})
	if err != nil {
		t.Fatalf("materializeGapAction: %v", err)
	}
	if ga.Action != actionTriggerLoom || ga.Pattern != "backgroundCheck" || ga.Subject != "vtx.leaseapp.CandidateHJKMNPQR1" {
		t.Fatalf("materialized triggerLoom = %+v", ga)
	}
}

func TestMaterializeGapAction_UnknownAction(t *testing.T) {
	t.Parallel()
	if _, err := materializeGapAction("dropDatabase", map[string]any{}); err == nil {
		t.Fatal("expected an error for an out-of-vocabulary action")
	}
	// directOp is deliberately NOT handled here (see buildProposedDirectOpPlan).
	if _, err := materializeGapAction("directOp", map[string]any{"operation": "X"}); err == nil {
		t.Fatal("directOp must not be materialized via materializeGapAction")
	}
}

func TestMaterializeGapAction_MissingFields(t *testing.T) {
	t.Parallel()
	if _, err := materializeGapAction("assignTask", map[string]any{"operation": "X"}); err == nil {
		t.Fatal("assignTask missing assignee/target must error")
	}
	if _, err := materializeGapAction("triggerLoom", map[string]any{"pattern": "X"}); err == nil {
		t.Fatal("triggerLoom missing subject must error")
	}
}

// --- buildProposedDirectOpPlan: directOp's type-preserving materialisation --

func TestBuildProposedDirectOpPlan_PreservesNonStringValues(t *testing.T) {
	t.Parallel()
	pl, err := buildProposedDirectOpPlan(map[string]any{
		"operation": "SetAvailability",
		"target":    dpCandidate,
		"params":    map[string]any{"identity": dpCandidate, "available": true, "priority": 3.0},
		"reads":     []any{dpCandidate},
	}, 42)
	if err != nil {
		t.Fatalf("buildProposedDirectOpPlan: %v", err)
	}
	if pl.operationType != "SetAvailability" || pl.authTarget != dpCandidate {
		t.Fatalf("op/authTarget = %q/%q", pl.operationType, pl.authTarget)
	}
	payload := pl.payload("ignored")
	if payload["identity"] != dpCandidate {
		t.Fatalf("payload identity = %v, want %q", payload["identity"], dpCandidate)
	}
	if b, ok := payload["available"].(bool); !ok || !b {
		t.Fatalf("payload available = %v (%T), want bool true (type must survive verbatim, not stringify)", payload["available"], payload["available"])
	}
	if n, ok := payload["priority"].(float64); !ok || n != 3.0 {
		t.Fatalf("payload priority = %v (%T), want float64 3", payload["priority"], payload["priority"])
	}
	if payload["expectedRevision"] != uint64(42) {
		t.Fatalf("payload expectedRevision = %v, want 42", payload["expectedRevision"])
	}
	if len(pl.reads) != 1 || pl.reads[0] != dpCandidate {
		t.Fatalf("reads = %v", pl.reads)
	}
}

func TestBuildProposedDirectOpPlan_MissingOperation(t *testing.T) {
	t.Parallel()
	if _, err := buildProposedDirectOpPlan(map[string]any{}, 1); err == nil {
		t.Fatal("directOp with no operation must error")
	}
}

// --- validateProposedDispatch (the dispatch-time §5 leg) --------------------

const dpCandidate = "vtx.leaseapp.BBcandidateHJKMNPQRS"

func TestValidateProposedDispatch_InScopeValid(t *testing.T) {
	t.Parallel()
	reason := validateProposedDispatch("directOp", map[string]any{
		"operation": "SetListingStatus",
		"target":    dpCandidate,
	}, dpCandidate)
	if reason != "" {
		t.Fatalf("in-scope proposal rejected: %q", reason)
	}
}

func TestValidateProposedDispatch_UnknownAction(t *testing.T) {
	t.Parallel()
	reason := validateProposedDispatch("dropDatabase", map[string]any{"target": dpCandidate}, dpCandidate)
	if reason == "" {
		t.Fatal("expected a rejection for an out-of-vocabulary action")
	}
}

func TestValidateProposedDispatch_ScopeEscape(t *testing.T) {
	t.Parallel()
	reason := validateProposedDispatch("assignTask", map[string]any{
		"operation": "ApproveLeaseApplication",
		"assignee":  "vtx.identity.SomeForeignHJKMNPQ1",
		"target":    dpCandidate,
	}, dpCandidate)
	if reason == "" {
		t.Fatal("a foreign vtx-key under an unlisted param name must be rejected")
	}
}

func TestValidateProposedDispatch_NoScopeReference(t *testing.T) {
	t.Parallel()
	reason := validateProposedDispatch("directOp", map[string]any{
		"operation": "SomeOp",
	}, dpCandidate)
	if reason == "" {
		t.Fatal("a proposal that never references the candidate must be rejected")
	}
}

func TestValidateProposedDispatch_TooDeepRejected(t *testing.T) {
	t.Parallel()
	reason := validateProposedDispatch("directOp", map[string]any{
		"operation": "SomeOp",
		"target":    dpCandidate, // the anchor is present+valid — too-deep is what must trigger the rejection
		"params": map[string]any{
			"nested": map[string]any{"deeper": dpCandidate},
		},
	}, dpCandidate)
	if reason == "" {
		t.Fatal("a value nested deeper than one level must be conservatively rejected")
	}
	if !strings.Contains(reason, "nested deeper") {
		t.Fatalf("reason = %q, want the too-deep class specifically (the anchor check must not have fired first)", reason)
	}
}

// TestValidateProposedDispatch_AnchorFieldMissing_Rejected: the anchor field
// itself (subject for triggerLoom, target for assignTask/directOp) must be
// present — a proposal that only references the candidate via an unrelated
// field (e.g. `reads`) must not pass.
func TestValidateProposedDispatch_AnchorFieldMissing_Rejected(t *testing.T) {
	t.Parallel()
	reason := validateProposedDispatch("directOp", map[string]any{
		"operation": "SomeOp",
		"reads":     []any{dpCandidate}, // candidate mentioned, but NOT via the anchor field
	}, dpCandidate)
	if reason == "" {
		t.Fatal("a directOp with no target anchor field must be rejected even if the candidate is mentioned elsewhere")
	}
}

// TestValidateProposedDispatch_AnchorFieldWrongValue_Rejected: the anchor
// field must equal candidateKey EXACTLY — a mismatched (but non-vtx-shaped,
// so the generic scan alone would miss it) anchor value must still reject.
func TestValidateProposedDispatch_AnchorFieldWrongValue_Rejected(t *testing.T) {
	t.Parallel()
	reason := validateProposedDispatch("directOp", map[string]any{
		"operation": "SomeOp",
		"target":    "not-a-vtx-key-at-all",
	}, dpCandidate)
	if reason == "" {
		t.Fatal("an anchor field that does not equal candidateKey must be rejected, even when it is not vtx-shaped")
	}
}

// TestValidateProposedDispatch_NoTrimming_PaddedValueRejected proves the
// scope check compares RAW values (no TrimSpace): a padded candidateKey in
// the anchor field must NOT be treated as equal — validation and the value
// that would be dispatched must always be byte-identical.
func TestValidateProposedDispatch_NoTrimming_PaddedValueRejected(t *testing.T) {
	t.Parallel()
	reason := validateProposedDispatch("directOp", map[string]any{
		"operation": "SomeOp",
		"target":    dpCandidate + " ",
	}, dpCandidate)
	if reason == "" {
		t.Fatal("a whitespace-padded anchor value must be rejected, not trimmed-and-accepted")
	}
}

// TestValidateProposedDispatch_RowTemplateInjectionRejected: a model-proposed
// literal that happens to use the reserved row.<column> prefix must be
// rejected outright — otherwise buildPlan's resolveParam would re-interpret it
// as a template against WEAVER'S OWN internal augurDispatch row (a distinct
// scope-escape vector the plain vtx-key check does not catch, since
// "row.targetMetaKey" is not itself vtx-shaped).
func TestValidateProposedDispatch_RowTemplateInjectionRejected(t *testing.T) {
	t.Parallel()
	reason := validateProposedDispatch("assignTask", map[string]any{
		"operation": "ApproveLeaseApplication",
		"assignee":  "row.targetMetaKey",
		"target":    dpCandidate,
	}, dpCandidate)
	if reason == "" {
		t.Fatal("a param value using the reserved row.<column> template prefix must be rejected")
	}
}

// --- buildProposedOpPlan: the end-to-end two-op dispatch resolution --------

func dispatchRow(candidateKey, targetMetaKey, action string, params map[string]any) map[string]any {
	return map[string]any{
		"entityKey":        "vtx.augurproposal.AProposalHandle0001",
		"violating":        true,
		"missing_dispatch": true,
		"proposedAction":   action,
		"proposedParams":   params,
		"candidateKey":     candidateKey,
		"targetMetaKey":    targetMetaKey,
	}
}

func TestBuildProposedOpPlan_Valid_DirectOp(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	row := dispatchRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", "directOp", map[string]any{
		"operation": "SetListingStatus",
		"target":    dpCandidate,
		"params":    map[string]any{"status": "leased"},
	})
	const handle = "AProposalHandle0001"

	pl, perr := buildProposedOpPlan(s, handle, row, 7)
	if perr != nil {
		t.Fatalf("buildProposedOpPlan: %v", perr)
	}
	if pl.operationType != "SetListingStatus" {
		t.Fatalf("primary op = %q, want SetListingStatus", pl.operationType)
	}
	if pl.requestID == nil {
		t.Fatal("a valid dispatch must carry a proposal-scoped requestID override")
	}
	got := pl.requestID("ignored-claim")
	want := deriveProposalDispatchRequestID(handle, 0)
	if got != want {
		t.Fatalf("requestID = %q, want the proposal-scoped %q", got, want)
	}
	if pl.followUp == nil {
		t.Fatal("a valid dispatch must carry the RecordProposalDispatch followUp")
	}
	if pl.followUp.operationType != opRecordProposalDispatch {
		t.Fatalf("followUp op = %q, want %q", pl.followUp.operationType, opRecordProposalDispatch)
	}
	fuPayload := pl.followUp.payload("ignored")
	if fuPayload["outcome"] != "dispatched" || fuPayload["externalRef"] != handle {
		t.Fatalf("followUp payload = %+v", fuPayload)
	}
}

// TestBuildProposedOpPlan_RequestIDStableAcrossReclaim proves the requestID is
// PROPOSAL-scoped, not mark/episode-scoped: two calls with different
// expectedRevision (simulating a sweep reclaim's fresh mark revision) still
// derive the identical requestId, so a re-dispatch collapses on the Contract
// #4 tracker instead of double-applying (design §3.3/§3.4).
func TestBuildProposedOpPlan_RequestIDStableAcrossReclaim(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	row := dispatchRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", "directOp", map[string]any{
		"operation": "SetListingStatus",
		"target":    dpCandidate,
	})
	const handle = "AProposalHandle0002"

	pl1, perr := buildProposedOpPlan(s, handle, row, 7)
	if perr != nil {
		t.Fatalf("buildProposedOpPlan (rev 7): %v", perr)
	}
	pl2, perr := buildProposedOpPlan(s, handle, row, 999)
	if perr != nil {
		t.Fatalf("buildProposedOpPlan (rev 999): %v", perr)
	}
	if pl1.requestID("a") != pl2.requestID("b") {
		t.Fatal("requestID must be stable across a reclaim's fresh revision/claimId")
	}
}

func TestBuildProposedOpPlan_Invalid_ScopeEscape_FlipOnly(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	row := dispatchRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", "assignTask", map[string]any{
		"operation": "ApproveLeaseApplication",
		"assignee":  "vtx.identity.SomeForeignHJKMNPQ1",
		"target":    dpCandidate,
	})
	const handle = "AProposalHandle0003"

	pl, perr := buildProposedOpPlan(s, handle, row, 7)
	if perr != nil {
		t.Fatalf("an invalid proposal must plan a flip, not error: %v", perr)
	}
	if pl.operationType != opRecordProposalDispatch {
		t.Fatalf("an invalid proposal must dispatch ONLY the flip, got op %q", pl.operationType)
	}
	if pl.followUp != nil {
		t.Fatal("the invalid-outcome flip carries no followUp (nothing else to fire)")
	}
	payload := pl.payload("ignored")
	if payload["outcome"] != "invalid" || payload["externalRef"] != handle {
		t.Fatalf("flip payload = %+v", payload)
	}
	if reason, _ := payload["reason"].(string); reason == "" {
		t.Fatal("the invalid flip must carry an auditable reason")
	}
}

func TestBuildProposedOpPlan_Invalid_NoCandidateKey_FlipOnly(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	row := map[string]any{"entityKey": "vtx.augurproposal.AProposalHandle0004"}
	pl, perr := buildProposedOpPlan(s, "AProposalHandle0004", row, 1)
	if perr != nil {
		t.Fatalf("a malformed row must plan a flip, not error: %v", perr)
	}
	if pl.operationType != opRecordProposalDispatch {
		t.Fatalf("op = %q, want the invalid flip", pl.operationType)
	}
}

// TestBuildProposedOpPlan_Transient_DefersNoFlip proves an unresolved
// live-registry reference (a triggerLoom pattern not yet loaded) defers
// (errTransient) with NO flip — nothing was dispatched yet, so nothing to
// record; the next redelivery/reclaim retries the same resolution.
func TestBuildProposedOpPlan_Transient_DefersNoFlip(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	row := dispatchRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", "triggerLoom", map[string]any{
		"pattern": "notInstalledPattern",
		"subject": dpCandidate,
	})
	pl, perr := buildProposedOpPlan(s, "AProposalHandle0005", row, 1)
	if pl != nil {
		t.Fatalf("expected no plan for an unresolved pattern reference, got %+v", pl)
	}
	if perr == nil || perr.kind != errTransient {
		t.Fatalf("expected errTransient, got %+v", perr)
	}
}

// --- end-to-end via the real engine: the two-op fire + the flip-only path --

func TestHandleRow_AugurDispatch_ValidProposal_FiresTwoOps(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "augurDispatch"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_dispatch": {Action: actionProposedOp}},
	})
	const handle = "BBdispatchAHJKMNPQRS"
	row := dispatchRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", "directOp", map[string]any{
		"operation": "SetListingStatus",
		"target":    dpCandidate,
		"params":    map[string]any{"status": "leased"},
	})

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, handle, row, 3, 1))
	if dec != substrate.Ack {
		t.Fatalf("valid dispatch must Ack, got %v", dec)
	}

	first := h.nextOp(t)
	if first["operationType"] != "SetListingStatus" {
		t.Fatalf("first op = %v, want SetListingStatus", first["operationType"])
	}
	if first["requestId"] != deriveProposalDispatchRequestID(handle, 0) {
		t.Fatalf("first op requestId = %v, want the proposal-scoped id", first["requestId"])
	}

	second := h.nextOp(t)
	if second["operationType"] != "RecordProposalDispatch" {
		t.Fatalf("second op = %v, want RecordProposalDispatch", second["operationType"])
	}
}

// TestHandleRow_AugurDispatch_InvalidProposal_FiresFlipOnly proves a
// dispatch-time-invalid proposal (here: a scope-escaping assignTask.assignee)
// fires ONLY the RecordProposalDispatch{invalid} flip — no remediation op ever
// reaches ops.system.
func TestHandleRow_AugurDispatch_InvalidProposal_FiresFlipOnly(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "augurDispatch"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_dispatch": {Action: actionProposedOp}},
	})
	const handle = "BBdispatchBHJKMNPQRS"
	row := dispatchRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", "assignTask", map[string]any{
		"operation": "ApproveLeaseApplication",
		"assignee":  "vtx.identity.SomeForeignHJKMNPQ1",
		"target":    dpCandidate,
	})

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, handle, row, 3, 1))
	if dec != substrate.Ack {
		t.Fatalf("an invalid dispatch still Acks (the flip commits) — got %v", dec)
	}

	only := h.nextOp(t)
	if only["operationType"] != "RecordProposalDispatch" {
		t.Fatalf("only op = %v, want RecordProposalDispatch (no remediation ever fires)", only["operationType"])
	}
	h.requireNoOp(t)
}

// --- plan-shaped proposals: per-leg dispatch --------------------------------

// planRow is dispatchRow with a recorded PLAN: the ordered steps and the leg
// counter the augurDispatchPending lens projects.
func planRow(candidateKey, targetMetaKey string, leg int, steps ...map[string]any) map[string]any {
	row := dispatchRow(candidateKey, targetMetaKey,
		steps[0]["action"].(string), steps[0]["params"].(map[string]any))
	asAny := make([]any, 0, len(steps))
	for _, step := range steps {
		asAny = append(asAny, any(step))
	}
	row["proposedSteps"] = asAny
	// The lens row arrives decoded from JSON, where every number is a float64.
	row["dispatchLeg"] = float64(leg)
	return row
}

// directStep is one plan leg as a directOp — the arm with no live-registry
// resolution, so a leg's materialisation is pinned without seeding a catalog.
func directStep(operation, target string) map[string]any {
	return map[string]any{
		"action": actionDirectOp,
		"params": map[string]any{"operation": operation, "target": target},
	}
}

// TestDeriveProposalDispatchRequestID_LegZeroUnchanged pins the leg-0
// derivation to the literal it produced before the leg became part of it: every
// proposal already in flight — and every single-step proposal forever — keeps
// the SAME requestId, so the Contract #4 tracker still collapses its
// re-dispatch. Leg 1 is a genuinely different op.
func TestDeriveProposalDispatchRequestID_LegZeroUnchanged(t *testing.T) {
	t.Parallel()
	const handle = "BBdispatchAHJKMNPQRS"
	if got := deriveProposalDispatchRequestID(handle, 0); got != "mxeLQDvHxqQnNRcWzHNR" {
		t.Fatalf("leg 0 requestId = %q, want the pre-plan literal mxeLQDvHxqQnNRcWzHNR", got)
	}
	if got := deriveProposalDispatchFlipRequestID(handle, "dispatched", 0); got != "EG1DoSJF9pzMntehaUZt" {
		t.Fatalf("leg 0 flip requestId = %q, want the pre-plan literal EG1DoSJF9pzMntehaUZt", got)
	}
	if deriveProposalDispatchRequestID(handle, 1) == deriveProposalDispatchRequestID(handle, 0) {
		t.Fatal("leg 1 must derive its own requestId, or the next leg would collapse onto the previous one")
	}
	if deriveProposalDispatchFlipRequestID(handle, "dispatched", 1) == deriveProposalDispatchFlipRequestID(handle, "dispatched", 0) {
		t.Fatal("leg 1's flip must derive its own requestId")
	}
}

// TestBuildProposedOpPlan_DispatchesTheRowsLeg proves the plan is dispatched one
// leg at a time: the row's dispatchLeg selects the step materialised, the
// requestId is that leg's, and the followUp flip carries the leg it records.
func TestBuildProposedOpPlan_DispatchesTheRowsLeg(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	const handle = "AProposalHandle0010"
	steps := []map[string]any{
		directStep("SetListingStatus", dpCandidate),
		directStep("RecordLeaseDecision", dpCandidate),
	}

	for leg, wantOp := range map[int]string{0: "SetListingStatus", 1: "RecordLeaseDecision"} {
		row := planRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", leg, steps...)
		pl, perr := buildProposedOpPlan(s, handle, row, 7)
		if perr != nil {
			t.Fatalf("leg %d: buildProposedOpPlan: %v", leg, perr)
		}
		if pl.operationType != wantOp {
			t.Fatalf("leg %d dispatched %q, want the step's own operation %q", leg, pl.operationType, wantOp)
		}
		if got, want := pl.requestID("ignored"), deriveProposalDispatchRequestID(handle, leg); got != want {
			t.Fatalf("leg %d requestID = %q, want the leg-scoped %q", leg, got, want)
		}
		if pl.proposalLeg != leg {
			t.Fatalf("leg %d: plan.proposalLeg = %d — the mark must record the leg it stands over", leg, pl.proposalLeg)
		}
		if pl.followUp == nil {
			t.Fatalf("leg %d must carry the flip", leg)
		}
		fu := pl.followUp.payload("ignored")
		if got, _ := fu["leg"].(int); got != leg {
			t.Fatalf("leg %d flip payload leg = %v, want %d", leg, fu["leg"], leg)
		}
		if got, want := pl.followUp.requestID("ignored"), deriveProposalDispatchFlipRequestID(handle, "dispatched", leg); got != want {
			t.Fatalf("leg %d flip requestID = %q, want %q", leg, got, want)
		}
		if !slicesContain(pl.followUp.reads, "vtx.augurproposal."+handle+".proposed") {
			t.Fatalf("leg %d flip must declare the .proposed read it counts legs against: %v", leg, pl.followUp.reads)
		}
	}
}

// TestBuildProposedOpPlan_LegPastTheEnd_FlipOnly: a counter past the plan's last
// step is a row and a counter that disagree — the dispatch records the proposal
// invalid rather than indexing out of range.
func TestBuildProposedOpPlan_LegPastTheEnd_FlipOnly(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	row := planRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", 2,
		directStep("SetListingStatus", dpCandidate),
		directStep("RecordLeaseDecision", dpCandidate))

	pl, perr := buildProposedOpPlan(s, "AProposalHandle0011", row, 7)
	if perr != nil {
		t.Fatalf("a leg past the end must plan a flip, not error: %v", perr)
	}
	if pl.operationType != opRecordProposalDispatch {
		t.Fatalf("op = %q, want the invalid flip", pl.operationType)
	}
	payload := pl.payload("ignored")
	if payload["outcome"] != "invalid" {
		t.Fatalf("flip outcome = %v, want invalid", payload["outcome"])
	}
	if reason, _ := payload["reason"].(string); !strings.Contains(reason, "past the end") {
		t.Fatalf("reason = %q, want it to explain the out-of-range leg", reason)
	}
}

// TestBuildProposedOpPlan_LegacyRowUnchanged is the optional-field-absent
// vector: a row with no proposedSteps and no dispatchLeg — the shape projected
// for a proposal recorded before the plan — dispatches its single remediation at
// leg 0 under the pre-plan requestId, and its flip carries leg 0.
func TestBuildProposedOpPlan_LegacyRowUnchanged(t *testing.T) {
	t.Parallel()
	s := newTestSource(t)
	row := dispatchRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", "directOp", map[string]any{
		"operation": "SetListingStatus",
		"target":    dpCandidate,
	})
	const handle = "AProposalHandle0012"

	pl, perr := buildProposedOpPlan(s, handle, row, 7)
	if perr != nil {
		t.Fatalf("buildProposedOpPlan: %v", perr)
	}
	if pl.operationType != "SetListingStatus" {
		t.Fatalf("op = %q, want the single recorded remediation", pl.operationType)
	}
	if pl.proposalLeg != 0 {
		t.Fatalf("plan.proposalLeg = %d, want 0", pl.proposalLeg)
	}
	if got, want := pl.requestID("x"), deriveProposalDispatchRequestID(handle, 0); got != want {
		t.Fatalf("requestID = %q, want %q", got, want)
	}
	if got, _ := pl.followUp.payload("x")["leg"].(int); got != 0 {
		t.Fatalf("flip leg = %v, want 0", pl.followUp.payload("x")["leg"])
	}
}

func slicesContain(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// --- the per-leg release, at both dispatch seams ----------------------------

// augurPlanTarget seeds the augurDispatch convergence target — one proposedOp
// gap, the shape packages/augur's weaver target installs.
func augurPlanTarget(targetID string) *Target {
	return &Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_dispatch": {Action: actionProposedOp}},
	}
}

// twoLegRow is the augurDispatchPending row for a two-leg plan standing at leg.
func twoLegRow(handle string, leg int) map[string]any {
	row := planRow(dpCandidate, "vtx.meta.SomeTargetHJKMNPQRS1", leg,
		directStep("SetListingStatus", dpCandidate),
		directStep("RecordLeaseDecision", dpCandidate))
	row["entityKey"] = "vtx.augurproposal." + handle
	return row
}

// TestHandleRow_AugurDispatch_AdvancedLegReleasesAndFiresNext: lane 1. A LIVE
// mark for leg 0 over a row whose counter has reached leg 1 stands over a leg
// already dispatched and recorded — it is released, and the delivery goes on to
// fire leg 1 as a genuinely fresh episode under a fresh mark that records the
// new leg. Without the release the live mark would take the anti-storm drop and
// the plan would never reach its second leg.
func TestHandleRow_AugurDispatch_AdvancedLegReleasesAndFiresNext(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "augurDispatch"
	const handle = "BBdispatchCHJKMNPQRS"
	h.seedTarget(augurPlanTarget(targetID))

	_, _, lost, err := h.engine.marks.create(ctx, targetID, handle, "missing_dispatch",
		"vtx.augurproposal."+handle, actionProposedOp, "", "", 0)
	if err != nil || lost {
		t.Fatalf("seed leg-0 mark: err=%v lost=%v", err, lost)
	}

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, handle, twoLegRow(handle, 1), 3, 1))
	if dec != substrate.Ack {
		t.Fatalf("the advanced-leg delivery must Ack, got %v", dec)
	}

	first := h.nextOp(t)
	if first["operationType"] != "RecordLeaseDecision" {
		t.Fatalf("first op = %v, want the SECOND leg's remediation", first["operationType"])
	}
	if first["requestId"] != deriveProposalDispatchRequestID(handle, 1) {
		t.Fatalf("first op requestId = %v, want leg 1's own id", first["requestId"])
	}
	second := h.nextOp(t)
	if second["operationType"] != opRecordProposalDispatch {
		t.Fatalf("second op = %v, want the flip", second["operationType"])
	}

	rec, _, found, err := h.engine.marks.get(ctx, targetID, handle, "missing_dispatch")
	if err != nil || !found {
		t.Fatalf("expected a FRESH mark for the advanced leg (err=%v found=%v)", err, found)
	}
	if rec.ProposalLeg != 1 {
		t.Fatalf("the fresh mark must record the leg it stands over: proposalLeg = %d, want 1", rec.ProposalLeg)
	}
}

// TestHandleRow_AugurDispatch_SameLegIsNotReleased: the release is conditioned
// on the counter having MOVED. A mark standing over the leg the row is still on
// is a live episode, and the delivery takes the ordinary anti-storm drop — it
// must not re-fire the leg already in flight.
func TestHandleRow_AugurDispatch_SameLegIsNotReleased(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "augurDispatch"
	const handle = "BBdispatchDHJKMNPQRS"
	h.seedTarget(augurPlanTarget(targetID))

	rev, _, lost, err := h.engine.marks.create(ctx, targetID, handle, "missing_dispatch",
		"vtx.augurproposal."+handle, actionProposedOp, "", "", 1)
	if err != nil || lost {
		t.Fatalf("seed leg-1 mark: err=%v lost=%v", err, lost)
	}

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, handle, twoLegRow(handle, 1), 3, 1))
	if dec != substrate.Ack {
		t.Fatalf("a live episode's delivery Acks, got %v", dec)
	}
	h.requireNoOp(t)

	rec, gotRev, found, err := h.engine.marks.get(ctx, targetID, handle, "missing_dispatch")
	if err != nil || !found {
		t.Fatalf("the live mark must stand (err=%v found=%v)", err, found)
	}
	if gotRev != rev || rec.ProposalLeg != 1 {
		t.Fatalf("the live mark must be untouched: rev %d→%d, proposalLeg %d", rev, gotRev, rec.ProposalLeg)
	}
}

// TestReclaim_AugurDispatch_AdvancedLegReleasesAndAdvances: the sweep seam. The
// sweep enumerates MARKS, so an expired mark over a leg the plan has recorded is
// the only thing that will look at this gap again — released and advanced in the
// same pass, or the plan stalls on the completed leg forever.
func TestReclaim_AugurDispatch_AdvancedLegReleasesAndAdvances(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "augurDispatch"
	const handle = "BBdispatchEHJKMNPQRS"
	h.seedTarget(augurPlanTarget(targetID))

	key := markKey(targetID, handle, "missing_dispatch")
	rec := fixtureMark(targetID, handle, "missing_dispatch", actionProposedOp, pastLease())
	rec.EntityKey = "vtx.augurproposal." + handle
	h.putMark(t, ctx, key, rec)
	h.putRow(t, ctx, targetID, handle, twoLegRow(handle, 1))

	h.pass(ctx)

	op := h.nextOp(t)
	if op["operationType"] != "RecordLeaseDecision" {
		t.Fatalf("expected the released leg to advance to the SECOND leg in the same pass, got %v", op["operationType"])
	}
	flip := h.nextOp(t)
	if flip["operationType"] != opRecordProposalDispatch {
		t.Fatalf("expected the advanced leg's flip, got %v", flip["operationType"])
	}

	fresh, _, found, err := h.engine.marks.get(ctx, targetID, handle, "missing_dispatch")
	if err != nil || !found {
		t.Fatalf("expected a FRESH mark for the advanced leg (err=%v found=%v)", err, found)
	}
	if fresh.ProposalLeg != 1 {
		t.Fatalf("the advanced mark must record leg 1, got %d", fresh.ProposalLeg)
	}
	h.requireNoOp(t)
}

// TestReclaim_AugurDispatch_SameLegReArmsKeepingItsLeg is the third mark-writer
// pin: the sweep's reclaim rewrites the whole mark value, so the leg it stands
// over survives only by being threaded through. A re-armed mark that read back
// as leg 0 would be released on the next pass and re-dispatch a leg already in
// flight.
func TestReclaim_AugurDispatch_SameLegReArmsKeepingItsLeg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "augurDispatch"
	const handle = "BBdispatchFHJKMNPQRS"
	h.seedTarget(augurPlanTarget(targetID))

	key := markKey(targetID, handle, "missing_dispatch")
	rec := fixtureMark(targetID, handle, "missing_dispatch", actionProposedOp, pastLease())
	rec.EntityKey = "vtx.augurproposal." + handle
	rec.ProposalLeg = 1
	h.putMark(t, ctx, key, rec)
	h.putRow(t, ctx, targetID, handle, twoLegRow(handle, 1))

	h.pass(ctx)

	op := h.nextOp(t)
	if op["operationType"] != "RecordLeaseDecision" {
		t.Fatalf("the reclaim re-fires the SAME leg, got %v", op["operationType"])
	}
	rearmed, _, found, err := h.engine.marks.get(ctx, targetID, handle, "missing_dispatch")
	if err != nil || !found {
		t.Fatalf("the reclaim must leave a mark standing (err=%v found=%v)", err, found)
	}
	if rearmed.ProposalLeg != 1 {
		t.Fatalf("the re-armed mark must keep its leg: proposalLeg = %d, want 1", rearmed.ProposalLeg)
	}
}

// TestFireEpisode_AugurDispatch_StaleReclaimKeepsItsLeg is the second mark-writer
// pin: lane 1's own in-place re-arm of an expired external mark (fireEpisode's
// stale branch) rewrites the whole mark value, so the leg it stands over must be
// written back. It comes off the PLAN being re-fired, which for a re-arm is the
// same leg the row still stands on.
func TestFireEpisode_AugurDispatch_StaleReclaimKeepsItsLeg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "augurDispatch"
	const handle = "BBdispatchGHJKMNPQRS"
	h.seedTarget(augurPlanTarget(targetID))

	key := markKey(targetID, handle, "missing_dispatch")
	rec := fixtureMark(targetID, handle, "missing_dispatch", actionProposedOp, pastLease())
	rec.EntityKey = "vtx.augurproposal." + handle
	rec.ProposalLeg = 1
	staleRev := h.putMark(t, ctx, key, rec)

	// inflight_<g>, declared and false, is what nominates the gap for the
	// external stale-reconcile class staleMark gates the in-place re-arm on.
	row := twoLegRow(handle, 1)
	row["inflight_dispatch"] = false
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	msg := substrate.Message{
		Subject:      h.engine.rowSubjectPrefix + targetID + "." + handle,
		Body:         body,
		Sequence:     9,
		NumDelivered: 1,
	}
	if dec := h.engine.handleRow(ctx, msg); dec != substrate.Ack {
		t.Fatalf("the stale-mark re-arm must Ack, got %v", dec)
	}
	if op := h.nextOp(t); op["operationType"] != "RecordLeaseDecision" {
		t.Fatalf("the re-arm re-fires the SAME leg, got %v", op["operationType"])
	}

	rearmed, gotRev, found, err := h.engine.marks.get(ctx, targetID, handle, "missing_dispatch")
	if err != nil || !found {
		t.Fatalf("the re-arm must leave a mark standing (err=%v found=%v)", err, found)
	}
	if gotRev == staleRev {
		t.Fatal("the re-arm must replace the mark in place with a fresh revision")
	}
	if rearmed.ProposalLeg != 1 {
		t.Fatalf("the re-armed mark must keep its leg: proposalLeg = %d, want 1", rearmed.ProposalLeg)
	}
}

// TestReleaseAdvancedProposalLeg_ResetsTheGapsDispatchCount: the retry budget is
// PER LEG — the attempts charged to it were spent reaching the leg the proposal
// has now recorded, so the release clears them. Carrying them forward would spend
// one leg's budget on the next leg's first try.
func TestReleaseAdvancedProposalLeg_ResetsTheGapsDispatchCount(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "augurDispatch"
	const handle = "BBdispatchHHJKMNPQRS"
	h.seedTarget(augurPlanTarget(targetID))

	markRev, _, lost, err := h.engine.marks.create(ctx, targetID, handle, "missing_dispatch",
		"vtx.augurproposal."+handle, actionProposedOp, "", "", 0)
	if err != nil || lost {
		t.Fatalf("seed leg-0 mark: err=%v lost=%v", err, lost)
	}
	putStateValue(t, ctx, h.conn, countKey(targetID, handle, "missing_dispatch"),
		dispatchCount{Count: 3, Leg: actionProposedOp})
	_, countRev, err := h.engine.marks.getDispatchCount(ctx, targetID, handle, "missing_dispatch")
	if err != nil {
		t.Fatalf("read the seeded count's revision: %v", err)
	}

	ga := GapAction{Action: actionProposedOp}
	rec, _, found, err := h.engine.marks.get(ctx, targetID, handle, "missing_dispatch")
	if err != nil || !found {
		t.Fatalf("read the seeded mark: err=%v found=%v", err, found)
	}
	if !h.engine.releaseAdvancedProposalLeg(ctx, targetID, handle, "missing_dispatch", ga, rec,
		twoLegRow(handle, 1), markRev, countRev) {
		t.Fatal("the advanced leg must release")
	}

	// getDispatchCount, not a raw read: a clean reset DELETES the document, and
	// absence reads as the zero one.
	doc, _, err := h.engine.marks.getDispatchCount(ctx, targetID, handle, "missing_dispatch")
	if err != nil {
		t.Fatalf("read the count document back: %v", err)
	}
	if doc.Count != 0 {
		t.Fatalf("count document = %+v, want it reset: the next leg starts on a fresh per-leg budget", doc)
	}
}
