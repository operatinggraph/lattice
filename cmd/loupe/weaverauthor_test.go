package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
)

func TestDraftTargetBodyFieldMapping(t *testing.T) {
	t.Parallel()
	in := pkgmgr.WeaverTargetArtifactContent{
		TargetID: "leaseComplete",
		LensRef:  "leaseViolations",
		Gaps: map[string]pkgmgr.GapActionArtifact{
			"missing_x": {
				Action: "directOp", Operation: "SignLease", Subject: "row.entityKey",
				Params: map[string]string{"k": "v"}, Reads: []string{"row.a"},
				IssueCode: "X", IssueSeverity: "warn",
			},
		},
	}
	body := draftTargetBody(in)
	if body.TargetID != "leaseComplete" || body.LensRef != "leaseViolations" {
		t.Fatalf("body = %+v, want targetId/lensRef carried through", body)
	}
	g, ok := body.Gaps["missing_x"]
	if !ok {
		t.Fatalf("body.Gaps = %+v, missing missing_x", body.Gaps)
	}
	if g.Action != "directOp" || g.Operation != "SignLease" || g.Subject != "row.entityKey" ||
		g.IssueCode != "X" || g.IssueSeverity != "warn" {
		t.Errorf("gap action fields not carried through: %+v", g.weaverActionContract)
	}
	if !reflect.DeepEqual(g.Params, map[string]string{"k": "v"}) {
		t.Errorf("params = %v, want {k:v}", g.Params)
	}
	if !reflect.DeepEqual(g.Reads, []string{"row.a"}) {
		t.Errorf("reads = %v, want [row.a]", g.Reads)
	}
	// The restricted artifact carries no candidates/goal/actions catalog —
	// dispatchKind must still resolve via the explicit action alone.
	if g.dispatchKind() != "action" {
		t.Errorf("dispatchKind = %q, want action", g.dispatchKind())
	}
}

func TestDraftTargetBodyEmptyGapsNeverNil(t *testing.T) {
	t.Parallel()
	body := draftTargetBody(pkgmgr.WeaverTargetArtifactContent{TargetID: "t"})
	if body.Gaps == nil {
		t.Error("Gaps must be a non-nil empty map, not nil — computeLaneChecks ranges over it")
	}
}

func TestContainsTargetBinarySearchOnSortedInput(t *testing.T) {
	t.Parallel()
	sorted := []string{"__draft", "targetA", "targetB"}
	if !containsTarget(sorted, "__draft") {
		t.Error("containsTarget missed the first element")
	}
	if !containsTarget(sorted, "targetB") {
		t.Error("containsTarget missed the last element")
	}
	if containsTarget(sorted, "targetZ") {
		t.Error("containsTarget false-positived on an absent id")
	}
	if containsTarget(nil, "__draft") {
		t.Error("containsTarget on a nil slice must report false, not panic")
	}
}

func TestNonNilStrings(t *testing.T) {
	t.Parallel()
	if got := nonNilStrings(nil); got == nil || len(got) != 0 {
		t.Errorf("nonNilStrings(nil) = %#v, want empty non-nil slice (JSON [] not null)", got)
	}
	if got := nonNilStrings([]string{"a"}); len(got) != 1 || got[0] != "a" {
		t.Errorf("nonNilStrings([a]) = %v", got)
	}
}

// TestWeaverAuthorCheckRequestDecodesPkgmgrTypes pins the wire shape: the
// request body's target/lens fields decode directly as the pkgmgr artifact
// content types, with no Loupe-side re-mapping of the JSON tags.
func TestWeaverAuthorCheckRequestDecodesPkgmgrTypes(t *testing.T) {
	t.Parallel()
	raw := `{
		"target": {"targetId":"t1","lensRef":"l1","gaps":{"missing_x":{"action":"directOp","operation":"Foo"}}},
		"lens": {"canonicalName":"l1","adapter":"nats-kv","bucket":"weaver-targets","spec":"MATCH (e) RETURN e"}
	}`
	var req weaverAuthorCheckRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Target.TargetID != "t1" || req.Target.LensRef != "l1" {
		t.Errorf("target = %+v", req.Target)
	}
	if req.Target.Gaps["missing_x"].Operation != "Foo" {
		t.Errorf("gap = %+v", req.Target.Gaps["missing_x"])
	}
	if req.Lens.CanonicalName != "l1" || req.Lens.Bucket != "weaver-targets" {
		t.Errorf("lens = %+v", req.Lens)
	}
}

// --- F25.3b — propose (weaverAuthorPropose) --------------------------------

// callPropose drives the handler directly with an operator token in the
// request context, mirroring review_test.go's callMarkApplied — propose
// needs no conn at all (SubmitCapabilityProposal reads nothing), so the
// fixture server carries only what the handler touches: gatewayURL + logger.
func callPropose(t *testing.T, srv *server, body string) (*httptest.ResponseRecorder, weaverAuthorProposeResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/weaver/author/propose", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), operatorTokenContextKey{}, "test-operator-token"))
	rec := httptest.NewRecorder()
	srv.weaverAuthorPropose(rec, req)
	var out weaverAuthorProposeResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func newTestProposeServer(t *testing.T, gatewayURL string) *server {
	t.Helper()
	return &server{gatewayURL: gatewayURL, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// TestWeaverAuthorPropose_MintsOneProposalIdPerArtifact pins the shape:
// each artifact gets its OWN minted NanoID and its own relayed op, and the
// mint is a real 20-char Lattice NanoID — the DDL's step-6 keyPattern gate
// refuses anything else (submit_test.go's own framing).
func TestWeaverAuthorPropose_MintsOneProposalIdPerArtifact(t *testing.T) {
	url, captured := stubGateway(t, processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	srv := newTestProposeServer(t, url)

	body := `{"artifacts":[
		{"kind":"weaverTarget","content":"{\"targetId\":\"t1\"}","target":{"mode":"install"},"rationale":"r","validation":{"state":"valid"}},
		{"kind":"lens","content":"{\"canonicalName\":\"l1\"}","target":{"mode":"install"},"rationale":"r","validation":{"state":"valid"}}
	]}`
	rec, out := callPropose(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(out.Results) != 2 {
		t.Fatalf("results = %+v, want 2", out.Results)
	}
	seen := map[string]bool{}
	for _, r := range out.Results {
		if r.Error != "" {
			t.Errorf("result %+v carries an unexpected error", r)
		}
		if len(r.ProposalID) != 20 {
			t.Errorf("proposalId %q is not a 20-char NanoID", r.ProposalID)
		}
		if seen[r.ProposalID] {
			t.Errorf("proposalId %q reused across artifacts", r.ProposalID)
		}
		seen[r.ProposalID] = true
	}

	// The relayed op is the LAST one captured (both share the stub); confirm
	// the operation type and that the payload carries the minted id + the
	// artifact fields verbatim, never a client-supplied proposalId.
	if captured.OperationType != "SubmitCapabilityProposal" {
		t.Errorf("operationType = %q, want SubmitCapabilityProposal", captured.OperationType)
	}
	var payload map[string]any
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode relayed payload: %v", err)
	}
	if payload["kind"] != "lens" || payload["rationale"] != "r" {
		t.Errorf("relayed payload = %+v", payload)
	}
	if pid, _ := payload["proposalId"].(string); len(pid) != 20 {
		t.Errorf("relayed proposalId = %q, want a minted 20-char id", pid)
	}
}

// TestWeaverAuthorPropose_OneArtifactFailureDoesNotBlockTheOther: the two
// artifacts are independent proposals — a Processor rejection on one must
// not stop the other from being submitted or reported.
func TestWeaverAuthorPropose_OneArtifactFailureDoesNotBlockTheOther(t *testing.T) {
	calls := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(processor.OperationReply{Status: processor.ReplyStatusRejected,
				Error: &processor.ReplyError{Message: "kind disabled"}})
			return
		}
		_ = json.NewEncoder(w).Encode(processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	}))
	t.Cleanup(hs.Close)
	srv := newTestProposeServer(t, hs.URL)

	body := `{"artifacts":[
		{"kind":"weaverPlaybook","content":"{}","target":{"mode":"install"},"rationale":"r","validation":{"state":"valid"}},
		{"kind":"lens","content":"{}","target":{"mode":"install"},"rationale":"r","validation":{"state":"valid"}}
	]}`
	rec, out := callPropose(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the handler reports per-artifact outcomes, never fails the whole request)", rec.Code)
	}
	if len(out.Results) != 2 {
		t.Fatalf("results = %+v, want 2", out.Results)
	}
	if out.Results[0].Reply == nil || out.Results[0].Reply.Status != processor.ReplyStatusRejected {
		t.Errorf("result[0] = %+v, want a rejected reply carried through (not an Error)", out.Results[0])
	}
	if out.Results[1].Reply == nil || out.Results[1].Reply.Status != processor.ReplyStatusAccepted {
		t.Errorf("result[1] = %+v, want the second artifact to still succeed", out.Results[1])
	}
	if calls != 2 {
		t.Fatalf("gateway calls = %d, want 2 (both artifacts submitted)", calls)
	}
}

func TestWeaverAuthorPropose_EmptyArtifactsRejected(t *testing.T) {
	srv := newTestProposeServer(t, "")
	rec, _ := callPropose(t, srv, `{"artifacts":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestWeaverAuthorPropose_MalformedBodyRejected(t *testing.T) {
	srv := newTestProposeServer(t, "")
	rec, _ := callPropose(t, srv, `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestWeaverAuthorPropose_GetMethodRejected(t *testing.T) {
	srv := newTestProposeServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/api/weaver/author/propose", nil)
	rec := httptest.NewRecorder()
	srv.weaverAuthorPropose(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (GET not allowed)", rec.Code)
	}
}
