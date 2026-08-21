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
	"unicode/utf8"

	"github.com/operatinggraph/lattice/internal/bootstrap"
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
		{"kind":"weaverTarget","content":"{\"targetId\":\"t1\",\"description\":\"Every tab settles.\"}","target":{"mode":"install"},"rationale":"r","validation":{"state":"valid"}},
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

// TestWeaverAuthorPropose_UndescribedTargetRejected pins Loupe's own
// queue-readability rule: pkgmgr keeps `description` optional, but a target
// entering the human review queue must say what it ensures. The refusal is a
// whole-request 400 (nothing is submitted) rather than a per-artifact result —
// the bundle's two artifacts are one operator action, and half-submitting it
// would leave an unlabelled lens in the queue with no target to explain it.
func TestWeaverAuthorPropose_UndescribedTargetRejected(t *testing.T) {
	calls := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	}))
	t.Cleanup(hs.Close)
	srv := newTestProposeServer(t, hs.URL)

	for _, content := range []string{
		`{\"targetId\":\"t1\"}`,
		`{\"targetId\":\"t1\",\"description\":\"\"}`,
		`{\"targetId\":\"t1\",\"description\":\"   \\n \"}`,
	} {
		body := `{"artifacts":[
			{"kind":"weaverTarget","content":"` + content + `","target":{"mode":"install"},"rationale":"r","validation":{"state":"valid"}},
			{"kind":"lens","content":"{}","target":{"mode":"install"},"rationale":"r","validation":{"state":"valid"}}
		]}`
		rec, _ := callPropose(t, srv, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("content %s: status = %d, want 400", content, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "description") {
			t.Errorf("content %s: refusal must name the missing field; got %s", content, rec.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("gateway calls = %d, want 0 — a refused bundle submits nothing", calls)
	}
}

// TestWeaverAuthorPropose_IntentLabelsBothArtifacts pins the queue row label:
// SubmitCapabilityProposal defaults `intent` to the whole rationale text
// (packages/capability-author/ddls.go), which reads as a wall of prose in the
// list. The target's description is the label of record, and BOTH artifacts
// carry the same one so the pair reads as one submission.
func TestWeaverAuthorPropose_IntentLabelsBothArtifacts(t *testing.T) {
	var intents []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gatewayOperationRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		var payload map[string]any
		_ = json.Unmarshal(req.Payload, &payload)
		s, _ := payload["intent"].(string)
		intents = append(intents, s)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	}))
	t.Cleanup(hs.Close)
	srv := newTestProposeServer(t, hs.URL)

	body := `{"artifacts":[
		{"kind":"weaverTarget","content":"{\"targetId\":\"t1\",\"description\":\"Every settled tab is charged.\\nA second line the row label drops.\"}","target":{"mode":"install"},"rationale":"a much longer rationale nobody wants as a row label","validation":{"state":"valid"}},
		{"kind":"lens","content":"{}","target":{"mode":"install"},"rationale":"a much longer rationale nobody wants as a row label","validation":{"state":"valid"}}
	]}`
	rec, _ := callPropose(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(intents) != 2 {
		t.Fatalf("relayed intents = %v, want one per artifact", intents)
	}
	for i, got := range intents {
		if got != "Every settled tab is charged." {
			t.Errorf("intent[%d] = %q, want the description's first line", i, got)
		}
	}
}

// TestProposeIntentFallsBackToRationale: a bundle with no weaverTarget artifact
// never reaches the description gate, so its label comes from the first
// rationale present — still a first line, still capped.
func TestProposeIntentFallsBackToRationale(t *testing.T) {
	t.Parallel()
	got := proposeIntent("", []weaverAuthorProposeArtifact{
		{Kind: "lens", Rationale: "  \n"},
		{Kind: "lens", Rationale: "Projects the open tabs.\nwith more detail below"},
	})
	if got != "Projects the open tabs." {
		t.Errorf("proposeIntent = %q, want the first non-blank rationale's first line", got)
	}
	if got := proposeIntent("", nil); got != "" {
		t.Errorf("proposeIntent(nothing) = %q, want empty so the op's own default stands", got)
	}
}

// TestFirstLineCappedIsRuneSafe: the cap is in RUNES. A byte-slice cut through
// a multi-byte rune would put invalid UTF-8 into the proposal vertex.
func TestFirstLineCappedIsRuneSafe(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 200)
	got := firstLineCapped(long, proposeIntentCap)
	if n := len([]rune(got)); n != proposeIntentCap {
		t.Errorf("capped length = %d runes, want %d", n, proposeIntentCap)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated label must be marked elided; got %q", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("capped label is not valid UTF-8: %q", got)
	}
	if got := firstLineCapped("short", proposeIntentCap); got != "short" {
		t.Errorf("an under-cap line must be returned whole; got %q", got)
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

// --- NL-2 — Describe (weaverAuthorRequest) --------------------------------

// callAuthorRequest drives the handler directly with an operator token in the
// request context, mirroring callPropose above.
func callAuthorRequest(t *testing.T, srv *server, body string) (*httptest.ResponseRecorder, weaverAuthorRequestResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/weaver/author/request", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), operatorTokenContextKey{}, "test-operator-token"))
	rec := httptest.NewRecorder()
	srv.weaverAuthorRequest(rec, req)
	var out weaverAuthorRequestResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// TestWeaverAuthorRequest_BlankIntentRejected pins the same-shape refusal as
// propose's undescribed-target gate: a blank (or whitespace-only) intent is a
// 400 naming the missing field, and nothing is relayed to the Gateway.
func TestWeaverAuthorRequest_BlankIntentRejected(t *testing.T) {
	calls := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	t.Cleanup(hs.Close)
	srv := newTestProposeServer(t, hs.URL)

	for _, body := range []string{`{"intent":""}`, `{"intent":"   \n "}`, `{}`} {
		rec, _ := callAuthorRequest(t, srv, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "intent") {
			t.Errorf("body %s: refusal must name the missing field; got %s", body, rec.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("gateway calls = %d, want 0 — a blank intent submits nothing", calls)
	}
}

// TestWeaverAuthorRequest_HappyPath pins the relay shape: a minted bare
// NanoID, RequestCapabilityAuthoring as the operation type, no Reads (the
// DDL declares its own posture), and the relayed reply carried through.
func TestWeaverAuthorRequest_HappyPath(t *testing.T) {
	url, captured := stubGateway(t, processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	srv := newTestProposeServer(t, url)

	rec, out := callAuthorRequest(t, srv, `{"intent":"a lens listing active providers by specialty"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(out.ProposalID) != 20 {
		t.Errorf("proposalId %q is not a 20-char NanoID", out.ProposalID)
	}
	if out.Reply == nil || out.Reply.Status != processor.ReplyStatusAccepted {
		t.Errorf("reply = %+v, want the relayed accepted reply carried through", out.Reply)
	}

	if captured.OperationType != "RequestCapabilityAuthoring" {
		t.Errorf("operationType = %q, want RequestCapabilityAuthoring", captured.OperationType)
	}
	if len(captured.Reads) != 0 {
		t.Errorf("reads = %v, want none — the DDL declares its own posture", captured.Reads)
	}
	var payload map[string]any
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode relayed payload: %v", err)
	}
	if payload["intent"] != "a lens listing active providers by specialty" {
		t.Errorf("relayed intent = %+v", payload["intent"])
	}
	if pid, _ := payload["proposalId"].(string); pid != out.ProposalID {
		t.Errorf("relayed proposalId = %q, want the minted id %q", pid, out.ProposalID)
	}
	if _, has := payload["contextRef"]; has {
		t.Errorf("payload = %+v, an omitted contextRef must not appear at all", payload)
	}
}

// TestWeaverAuthorRequest_ContextRefPassthroughAndOmission pins both halves:
// a typed contextRef rides the relayed payload trimmed, and an absent one is
// omitted from the payload entirely rather than carried as "".
func TestWeaverAuthorRequest_ContextRefPassthroughAndOmission(t *testing.T) {
	url, captured := stubGateway(t, processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	srv := newTestProposeServer(t, url)

	if _, out := callAuthorRequest(t, srv, `{"intent":"x","contextRef":"  vtx.meta.abc  "}`); out.ProposalID == "" {
		t.Fatal("expected a minted proposalId")
	}
	var payload map[string]any
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode relayed payload: %v", err)
	}
	if payload["contextRef"] != "vtx.meta.abc" {
		t.Errorf("contextRef = %+v, want the trimmed value carried through", payload["contextRef"])
	}

	callAuthorRequest(t, srv, `{"intent":"x"}`)
	payload = nil
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode relayed payload: %v", err)
	}
	if _, has := payload["contextRef"]; has {
		t.Errorf("payload = %+v, a request with no contextRef must omit the key entirely", payload)
	}
}

func TestWeaverAuthorRequest_GetMethodRejected(t *testing.T) {
	srv := newTestProposeServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/api/weaver/author/request", nil)
	rec := httptest.NewRecorder()
	srv.weaverAuthorRequest(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (GET not allowed)", rec.Code)
	}
}

func TestWeaverAuthorRequest_MalformedBodyRejected(t *testing.T) {
	srv := newTestProposeServer(t, "")
	rec, _ := callAuthorRequest(t, srv, `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// --- Cold-review fix: Studio apply path -----------------------------------
//
// L2 (target-only check) + L3 (lensRef canonicalName resolution). Verified
// live against internal/pkgmgr: capabilityapply.go:156-197 (packageName/mode
// requirements), build.go:566-590 (resolveLensRef — an installed target's
// LensRef is always a bare NanoID; a canonicalName only resolves within the
// SAME Definition's own declared Lenses, which a Studio proposal — always a
// single-artifact Definition — never carries), capabilitymaterializer.go:601
// (validateWeaverTargetArtifact requires lensRef non-empty but never resolves
// it — resolution is apply-time only, so Check-time validity is unaffected by
// whether a lensRef is a canonicalName or a NanoID).

// lensMetaSpecValue is the Core KV value classifyKey+metaData expect at
// vtx.meta.<id>.spec for an installed lens — the {"data":{...}} envelope
// metaData's own decode struct requires (cmd/loupe/ops.go).
func lensMetaSpecValue(canonicalName string) string {
	return `{"data":{"canonicalName":"` + canonicalName + `","targetType":"nats-kv","targetConfig":{},"cypherRule":"MATCH (e) RETURN e","engine":"full"}}`
}

func TestWeaverAuthorCheck_TargetOnlyOmitsLensValidation(t *testing.T) {
	srv, _, _, _ := newTestReviewServerWithSrv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/weaver/author/check", strings.NewReader(
		`{"target":{"targetId":"t1","lensRef":"aaaaaaaaaaaaaaaaaaaa","gaps":{}}}`))
	rec := httptest.NewRecorder()
	srv.weaverAuthorCheck(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "lensValidation") {
		t.Errorf("response body carries a lensValidation key at all when no lens was submitted: %s", rec.Body.String())
	}
	var resp weaverAuthorCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.LensValidation != nil {
		t.Errorf("lensValidation = %+v, want nil", resp.LensValidation)
	}
	if !resp.TargetValidation.Valid {
		t.Errorf("targetValidation = %+v, want valid — Check-time validation never resolves lensRef (apply-time only)", resp.TargetValidation)
	}
}

func TestWeaverAuthorCheck_WithLensComputesLensValidation(t *testing.T) {
	srv, _, _, _ := newTestReviewServerWithSrv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/weaver/author/check", strings.NewReader(
		`{"target":{"targetId":"t1","lensRef":"l1","gaps":{}},`+
			`"lens":{"canonicalName":"l1","adapter":"nats-kv","bucket":"weaver-targets","spec":"MATCH (e) RETURN e"}}`))
	rec := httptest.NewRecorder()
	srv.weaverAuthorCheck(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	var resp weaverAuthorCheckResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.LensValidation == nil {
		t.Fatal("lensValidation = nil, want a computed verdict — a lens WAS submitted")
	}
	if !resp.LensValidation.Valid {
		t.Errorf("lensValidation = %+v, want valid", resp.LensValidation)
	}
}

func TestWeaverTargetNeedsLensResolution(t *testing.T) {
	cases := []struct {
		name      string
		artifacts []weaverAuthorProposeArtifact
		want      bool
	}{
		{"co-authored bundle never needs resolution, even with a canonicalName lensRef",
			[]weaverAuthorProposeArtifact{
				{Kind: "weaverTarget", Content: `{"targetId":"t1","lensRef":"leaseViolations"}`},
				{Kind: "lens", Content: `{"canonicalName":"leaseViolations"}`},
			}, false},
		{"target-only, empty lensRef",
			[]weaverAuthorProposeArtifact{{Kind: "weaverTarget", Content: `{"targetId":"t1","lensRef":""}`}}, false},
		{"target-only, already a bare NanoID",
			[]weaverAuthorProposeArtifact{{Kind: "weaverTarget", Content: `{"targetId":"t1","lensRef":"aaaaaaaaaaaaaaaaaaaa"}`}}, false},
		{"target-only, canonicalName lensRef",
			[]weaverAuthorProposeArtifact{{Kind: "weaverTarget", Content: `{"targetId":"t1","lensRef":"leaseViolations"}`}}, true},
		{"malformed content never blocks — resolveWeaverTargetLensRefs reports it properly",
			[]weaverAuthorProposeArtifact{{Kind: "weaverTarget", Content: `not json`}}, false},
		{"no weaverTarget artifact at all",
			[]weaverAuthorProposeArtifact{{Kind: "lens", Content: `{"canonicalName":"l1"}`}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := weaverTargetNeedsLensResolution(tc.artifacts); got != tc.want {
				t.Errorf("weaverTargetNeedsLensResolution = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWeaverAuthorPropose_TargetOnlyResolvesLensRefCanonicalName is L3's
// core proof: an installed lens's canonicalName, authored into a
// target-only bundle's lensRef, is rewritten to that lens's bare NanoID
// before the op relays — the exact resolution internal/pkgmgr/build.go's
// resolveLensRef needs at apply time and can never perform itself (a Studio
// proposal's Definition never carries a Lenses list of its own).
func TestWeaverAuthorPropose_TargetOnlyResolvesLensRefCanonicalName(t *testing.T) {
	srv, _, _, put := newTestReviewServerWithSrv(t)
	const lensID = "aaaaaaaaaaaaaaaaaaaa"
	put(bootstrap.CoreKVBucket, "vtx.meta."+lensID+".spec", lensMetaSpecValue("leaseViolations"))

	url, captured := stubGateway(t, processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	srv.gatewayURL = url

	body := `{"artifacts":[
		{"kind":"weaverTarget","content":"{\"targetId\":\"t1\",\"lensRef\":\"leaseViolations\",\"description\":\"Every tab settles.\"}","target":{"mode":"newPackage","packageName":"weaver-target-t1-abc","newVersion":"0.1.0"},"rationale":"r","validation":{"state":"valid"}}
	]}`
	rec, out := callPropose(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if len(out.Results) != 1 {
		t.Fatalf("results = %+v, want 1 (target-only)", out.Results)
	}
	if out.Results[0].Error != "" {
		t.Fatalf("result = %+v, want no error", out.Results[0])
	}

	var payload map[string]any
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode relayed payload: %v", err)
	}
	content, _ := payload["content"].(string)
	var wc pkgmgr.WeaverTargetArtifactContent
	if err := json.Unmarshal([]byte(content), &wc); err != nil {
		t.Fatalf("decode relayed weaverTarget content: %v", err)
	}
	if wc.LensRef != lensID {
		t.Errorf("relayed lensRef = %q, want the resolved NanoID %q", wc.LensRef, lensID)
	}
}

// TestWeaverAuthorPropose_TargetOnlyUnresolvableLensRefRejected pins the
// fail-closed side: a canonicalName that matches no installed lens is
// refused synchronously (400, naming the lensRef) rather than minting a
// proposal that can only fail later, at the final apply click.
func TestWeaverAuthorPropose_TargetOnlyUnresolvableLensRefRejected(t *testing.T) {
	srv, _, _, _ := newTestReviewServerWithSrv(t)
	calls := 0
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	t.Cleanup(hs.Close)
	srv.gatewayURL = hs.URL

	body := `{"artifacts":[
		{"kind":"weaverTarget","content":"{\"targetId\":\"t1\",\"lensRef\":\"noSuchLens\",\"description\":\"Every tab settles.\"}","target":{"mode":"newPackage","packageName":"weaver-target-t1-abc","newVersion":"0.1.0"},"rationale":"r","validation":{"state":"valid"}}
	]}`
	rec, _ := callPropose(t, srv, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "noSuchLens") {
		t.Errorf("refusal must name the unresolvable lensRef; got %s", rec.Body.String())
	}
	if calls != 0 {
		t.Fatalf("gateway calls = %d, want 0 — an unresolvable bundle submits nothing", calls)
	}
}

// TestWeaverAuthorPropose_TargetOnlyAlreadyNanoIDLensRefNeedsNoConn is the
// coordinator's round-trip proof: hydrateFromProposal loads content.lensRef
// verbatim, and after the bridge adapter fix that value is already a bare
// NanoID — so a re-proposed unedited "Load into Author" draft needs NO Core
// KV lookup at all. Driven against the conn-FREE fixture (newTestProposeServer,
// s.conn is nil) specifically to prove that: had resolution been attempted
// here, s.requireConn would have failed the whole request closed.
func TestWeaverAuthorPropose_TargetOnlyAlreadyNanoIDLensRefNeedsNoConn(t *testing.T) {
	url, captured := stubGateway(t, processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	srv := newTestProposeServer(t, url)
	const lensID = "aaaaaaaaaaaaaaaaaaaa"

	body := `{"artifacts":[
		{"kind":"weaverTarget","content":"{\"targetId\":\"t1\",\"lensRef\":\"` + lensID + `\",\"description\":\"Every tab settles.\"}","target":{"mode":"newPackage","packageName":"weaver-target-t1-abc","newVersion":"0.1.0"},"rationale":"r","validation":{"state":"valid"}}
	]}`
	rec, out := callPropose(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200 (an already-NanoID lensRef needs no Core KV read, so a nil conn must not block it)", rec.Code, rec.Body.String())
	}
	if len(out.Results) != 1 || out.Results[0].Error != "" {
		t.Fatalf("results = %+v, want 1 clean result", out.Results)
	}
	var payload map[string]any
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode relayed payload: %v", err)
	}
	content, _ := payload["content"].(string)
	var wc pkgmgr.WeaverTargetArtifactContent
	if err := json.Unmarshal([]byte(content), &wc); err != nil {
		t.Fatalf("decode relayed weaverTarget content: %v", err)
	}
	if wc.LensRef != lensID {
		t.Errorf("lensRef = %q, want unchanged %q (already resolved)", wc.LensRef, lensID)
	}
}

// TestWeaverAuthorPropose_CoAuthoredBundleNeedsNoConn pins that the existing
// {target+lens} co-authoring path is left completely alone: a canonicalName
// lensRef is never rewritten (there is nothing installed yet to resolve it
// against — the paired lens gets its NanoID only when ITS OWN proposal is
// applied), and this path needs no Core KV connection at all — proven the
// same way, against the conn-free fixture.
func TestWeaverAuthorPropose_CoAuthoredBundleNeedsNoConn(t *testing.T) {
	url, _ := stubGateway(t, processor.OperationReply{Status: processor.ReplyStatusAccepted, Decision: "committed"})
	srv := newTestProposeServer(t, url)

	body := `{"artifacts":[
		{"kind":"weaverTarget","content":"{\"targetId\":\"t1\",\"lensRef\":\"leaseViolations\",\"description\":\"Every tab settles.\"}","target":{"mode":"newPackage","packageName":"weaver-target-t1-abc","newVersion":"0.1.0"},"rationale":"r","validation":{"state":"valid"}},
		{"kind":"lens","content":"{\"canonicalName\":\"leaseViolations\"}","target":{"mode":"newPackage","packageName":"weaver-lens-t1-abc","newVersion":"0.1.0"},"rationale":"r","validation":{"state":"valid"}}
	]}`
	rec, out := callPropose(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200 (co-authored path needs no Core KV read, so a nil conn must not block it)", rec.Code, rec.Body.String())
	}
	if len(out.Results) != 2 {
		t.Fatalf("results = %+v, want 2", out.Results)
	}
	for _, r := range out.Results {
		if r.Error != "" {
			t.Errorf("result %+v carries an unexpected error", r)
		}
	}
}
