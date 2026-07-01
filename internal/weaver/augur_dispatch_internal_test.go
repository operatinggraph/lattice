package weaver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
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
	want := deriveProposalDispatchRequestID(handle)
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
	if first["requestId"] != deriveProposalDispatchRequestID(handle) {
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
