package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asolgan/lattice/internal/processor"
)

// fakeGateway records the last request it received (method, path, headers,
// decoded body) and answers with a pre-set status + raw JSON body — enough
// to drive submitOpViaGateway through every response shape the real Gateway
// can produce without needing a live Processor.
type fakeGateway struct {
	srv *httptest.Server

	called     bool
	gotMethod  string
	gotPath    string
	gotAuth    string
	gotBody    []byte
	respStatus int
	respBody   string
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	fg := &fakeGateway{respStatus: http.StatusOK, respBody: `{"status":"accepted","requestId":"r1","opTrackerKey":"t1"}`}
	fg.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fg.called = true
		fg.gotMethod = r.Method
		fg.gotPath = r.URL.Path
		fg.gotAuth = r.Header.Get("Authorization")
		fg.gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fg.respStatus)
		_, _ = w.Write([]byte(fg.respBody))
	}))
	t.Cleanup(fg.srv.Close)
	return fg
}

func (fg *fakeGateway) url() string { return fg.srv.URL }

func TestSubmitOpViaGateway_Accepted(t *testing.T) {
	fg := newFakeGateway(t)
	reply, err := submitOpViaGateway(t.Context(), fg.url(), "operator-token", gatewayOperationRequest{
		RequestID: "r1", Lane: "default", OperationType: "ShredIdentityKey",
		Class: "identity", Payload: json.RawMessage(`{"x":1}`), Reads: []string{"vtx.identity.a"},
	})
	if err != nil {
		t.Fatalf("submitOpViaGateway: %v", err)
	}
	if reply.Status != processor.ReplyStatusAccepted {
		t.Errorf("Status = %q, want accepted", reply.Status)
	}
	if !fg.called {
		t.Fatal("gateway never received a request")
	}
	if fg.gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", fg.gotMethod)
	}
	if fg.gotPath != "/v1/operations" {
		t.Errorf("path = %q, want /v1/operations", fg.gotPath)
	}
	if fg.gotAuth != "Bearer operator-token" {
		t.Errorf("Authorization = %q, want %q", fg.gotAuth, "Bearer operator-token")
	}
}

// TestSubmitOpViaGateway_RequestShape proves the outgoing body carries
// exactly what was asked and — critically — no actor field at all, matching
// the Gateway's own "there is no actor field by construction" contract
// (Loupe relays a credential, it never asserts one).
func TestSubmitOpViaGateway_RequestShape(t *testing.T) {
	fg := newFakeGateway(t)
	_, err := submitOpViaGateway(t.Context(), fg.url(), "tok", gatewayOperationRequest{
		RequestID: "req-123", Lane: "meta", OperationType: "InstallPackage",
		Class: "InstallPackage", Payload: json.RawMessage(`{"a":"b"}`), Reads: []string{"vtx.x.1", "vtx.y.2"},
	})
	if err != nil {
		t.Fatalf("submitOpViaGateway: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(fg.gotBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v; raw=%s", err, fg.gotBody)
	}
	if _, hasActor := sent["actor"]; hasActor {
		t.Error("outgoing request must never carry an actor field")
	}
	if sent["requestId"] != "req-123" || sent["lane"] != "meta" || sent["operationType"] != "InstallPackage" || sent["class"] != "InstallPackage" {
		t.Errorf("unexpected request shape: %+v", sent)
	}
	reads, _ := sent["reads"].([]any)
	if len(reads) != 2 || reads[0] != "vtx.x.1" || reads[1] != "vtx.y.2" {
		t.Errorf("reads = %v, want [vtx.x.1 vtx.y.2]", sent["reads"])
	}
}

// TestSubmitOpViaGateway_DeniedRejection proves a real capability denial
// (§8's "deny must be real") arrives as a normal reply, not swallowed into a
// generic error — every existing caller's reply.Status/reply.Error.Code
// inspection (objects.go's retry check, handlers' 400-vs-200 branch) keeps
// working unchanged against a Gateway-relayed reply.
func TestSubmitOpViaGateway_DeniedRejection(t *testing.T) {
	fg := newFakeGateway(t)
	fg.respStatus = http.StatusForbidden
	fg.respBody = `{"requestId":"r2","opTrackerKey":"t2","status":"rejected","error":{"code":"AuthDenied","message":"operator lacks this grant"}}`

	reply, err := submitOpViaGateway(t.Context(), fg.url(), "tok", gatewayOperationRequest{OperationType: "InstallPackage"})
	if err != nil {
		t.Fatalf("submitOpViaGateway: %v (a denial must be a reply, not an error)", err)
	}
	if reply.Status != processor.ReplyStatusRejected {
		t.Errorf("Status = %q, want rejected", reply.Status)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeAuthDenied {
		t.Errorf("Error = %+v, want code AuthDenied", reply.Error)
	}
}

// TestSubmitOpViaGateway_RevisionConflictSurvivesRelay proves the specific
// error code objects.go's retry loop keys on survives the relay unchanged.
func TestSubmitOpViaGateway_RevisionConflictSurvivesRelay(t *testing.T) {
	fg := newFakeGateway(t)
	fg.respStatus = http.StatusConflict
	fg.respBody = `{"requestId":"r3","opTrackerKey":"t3","status":"rejected","error":{"code":"RevisionConflict","message":"concurrent write"}}`

	reply, err := submitOpViaGateway(t.Context(), fg.url(), "tok", gatewayOperationRequest{OperationType: "AttachObject"})
	if err != nil {
		t.Fatalf("submitOpViaGateway: %v", err)
	}
	if reply.Status != processor.ReplyStatusRejected || reply.Error == nil || reply.Error.Code != processor.ErrCodeRevisionConflict {
		t.Errorf("reply = %+v, want rejected/RevisionConflict", reply)
	}
}

func TestSubmitOpViaGateway_Unauthorized401(t *testing.T) {
	fg := newFakeGateway(t)
	fg.respStatus = http.StatusUnauthorized
	fg.respBody = `{"error":"authentication failed"}`

	_, err := submitOpViaGateway(t.Context(), fg.url(), "bad-tok", gatewayOperationRequest{OperationType: "ShredIdentityKey"})
	if err == nil {
		t.Fatal("expected an error for a 401 from the gateway")
	}
}

func TestSubmitOpViaGateway_BadRequest400(t *testing.T) {
	fg := newFakeGateway(t)
	fg.respStatus = http.StatusBadRequest
	fg.respBody = `{"error":"operationType is required"}`

	_, err := submitOpViaGateway(t.Context(), fg.url(), "tok", gatewayOperationRequest{})
	if err == nil {
		t.Fatal("expected an error for a 400 from the gateway")
	}
}

// TestSubmitOpViaGateway_TimeoutAccepted202 covers the Gateway's
// DeadlineExceeded stand-in reply — {"requestId":...} with no status/error —
// which the caller cannot productively treat as a normal reply (no
// Status to branch on), so it surfaces as an error naming the requestId.
func TestSubmitOpViaGateway_TimeoutAccepted202(t *testing.T) {
	fg := newFakeGateway(t)
	fg.respStatus = http.StatusAccepted
	fg.respBody = `{"requestId":"pending-abc"}`

	_, err := submitOpViaGateway(t.Context(), fg.url(), "tok", gatewayOperationRequest{OperationType: "ShredIdentityKey"})
	if err == nil {
		t.Fatal("expected an error for a 202 pending response")
	}
}

// TestSubmitOpViaGateway_UnrecognizedResponse covers the final catch-all —
// a response that matches none of the three known shapes (a status-bearing
// reply, a 202 pending-with-requestId body, or an {"error":...} body). A
// misconfigured LOUPE_GATEWAY_URL pointing at an unrelated HTTP server (or
// its reverse proxy's own error page) is the realistic trigger.
func TestSubmitOpViaGateway_UnrecognizedResponse(t *testing.T) {
	fg := newFakeGateway(t)
	fg.respStatus = http.StatusInternalServerError
	fg.respBody = "<html><body>502 Bad Gateway</body></html>"

	_, err := submitOpViaGateway(t.Context(), fg.url(), "tok", gatewayOperationRequest{OperationType: "ShredIdentityKey"})
	if err == nil {
		t.Fatal("expected an error for an unrecognized response shape")
	}
	if !strings.Contains(err.Error(), "502 Bad Gateway") {
		t.Errorf("error = %q, want it to surface the unrecognized body for diagnostics", err.Error())
	}
}

// TestSubmitOpViaGateway_UnrecognizedResponse_TruncatesLargeBody proves an
// oversized or malicious unrecognized body doesn't balloon the operator-
// visible error message.
func TestSubmitOpViaGateway_UnrecognizedResponse_TruncatesLargeBody(t *testing.T) {
	fg := newFakeGateway(t)
	fg.respStatus = http.StatusInternalServerError
	fg.respBody = strings.Repeat("x", 10_000)

	_, err := submitOpViaGateway(t.Context(), fg.url(), "tok", gatewayOperationRequest{OperationType: "ShredIdentityKey"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(err.Error()) > 1000 {
		t.Errorf("error message is %d bytes, want it bounded well below the raw 10000-byte body", len(err.Error()))
	}
}

func TestSubmitOpViaGateway_EmptyToken_NeverCallsGateway(t *testing.T) {
	fg := newFakeGateway(t)
	_, err := submitOpViaGateway(t.Context(), fg.url(), "", gatewayOperationRequest{OperationType: "ShredIdentityKey"})
	if err == nil {
		t.Fatal("expected an error for an empty bearer token")
	}
	if fg.called {
		t.Error("must not call the gateway at all with no credential to present")
	}
}

func TestSubmitOpViaGateway_NetworkFailure(t *testing.T) {
	fg := newFakeGateway(t)
	fg.srv.Close() // guarantees connection refused
	_, err := submitOpViaGateway(t.Context(), fg.url(), "tok", gatewayOperationRequest{OperationType: "ShredIdentityKey"})
	if err == nil {
		t.Fatal("expected an error when the gateway is unreachable")
	}
}

func TestGatewayRequestFromEnvelope(t *testing.T) {
	env := &processor.OperationEnvelope{
		RequestID:     "req-1",
		Lane:          processor.LaneMeta,
		OperationType: "InstallPackage",
		Actor:         "vtx.identity.should-be-dropped",
		SubmittedAt:   "2026-01-01T00:00:00Z",
		Class:         "InstallPackage",
		Payload:       json.RawMessage(`{"a":1}`),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.x.1"}},
	}
	greq := gatewayRequestFromEnvelope(env)
	if greq.RequestID != "req-1" || greq.Lane != "meta" || greq.OperationType != "InstallPackage" || greq.Class != "InstallPackage" {
		t.Errorf("unexpected adapted request: %+v", greq)
	}
	if string(greq.Payload) != `{"a":1}` {
		t.Errorf("Payload = %s", greq.Payload)
	}
	if len(greq.Reads) != 1 || greq.Reads[0] != "vtx.x.1" {
		t.Errorf("Reads = %v", greq.Reads)
	}
	// gatewayOperationRequest has no Actor/SubmittedAt field at all — a
	// compile-time guarantee, not just an unasserted runtime one.
}

func TestPkgmgrSubmit_RelaysWithMetaLaneAndContextToken(t *testing.T) {
	fg := newFakeGateway(t)
	s := &server{gatewayURL: fg.url()}
	ctx := context.WithValue(t.Context(), operatorTokenContextKey{}, "operator-tok")

	reply, err := s.pkgmgrSubmit(ctx, "InstallPackage", "InstallPackage", "req-9", map[string]any{"name": "foo"})
	if err != nil {
		t.Fatalf("pkgmgrSubmit: %v", err)
	}
	if reply.Status != processor.ReplyStatusAccepted {
		t.Errorf("Status = %q", reply.Status)
	}
	if fg.gotAuth != "Bearer operator-tok" {
		t.Errorf("Authorization = %q, want the context's operator token", fg.gotAuth)
	}
	var sent map[string]any
	_ = json.Unmarshal(fg.gotBody, &sent)
	if sent["lane"] != "meta" {
		t.Errorf("lane = %v, want meta (package ops are always meta-lane)", sent["lane"])
	}
	if sent["requestId"] != "req-9" || sent["operationType"] != "InstallPackage" {
		t.Errorf("unexpected request: %+v", sent)
	}
}

func TestPkgmgrSubmit_NoTokenInContext_Errors(t *testing.T) {
	fg := newFakeGateway(t)
	s := &server{gatewayURL: fg.url()}
	if _, err := s.pkgmgrSubmit(t.Context(), "InstallPackage", "InstallPackage", "req-1", map[string]any{}); err == nil {
		t.Fatal("expected an error with no operator token in context")
	}
	if fg.called {
		t.Error("must not call the gateway with no credential")
	}
}

func TestOperatorToken_EmptyOutsideContext(t *testing.T) {
	if tok := operatorToken(context.Background()); tok != "" {
		t.Errorf("operatorToken on a bare context = %q, want empty", tok)
	}
}

// TestRequireOperator_AttachesTokenToContext proves the plumbing task itself:
// once requireOperator authenticates a request, the exact winning token is
// retrievable downstream via operatorToken(r.Context()) — what every relay
// call site depends on.
func TestRequireOperator_AttachesTokenToContext(t *testing.T) {
	s := devAuthServer(t)
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	var gotInHandler string
	gate := s.requireOperator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotInHandler = operatorToken(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/systemmap", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	gate.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotInHandler != tok {
		t.Errorf("operatorToken in handler = %q, want %q", gotInHandler, tok)
	}
}

// TestHandleOp_RelaysToGateway proves the plumbing end-to-end against a
// fake Gateway (see the package comment on fakeGateway): a fully
// authenticated POST /api/op reaches it carrying the logged-in operator's
// own token (via requireOperator's context plumbing), and its reply flows
// back to the HTTP caller unchanged. Proving this against the real Gateway
// under real capability enforcement is item 6's e2e (deferred).
func TestHandleOp_RelaysToGateway(t *testing.T) {
	fg := newFakeGateway(t)
	s := devAuthServer(t)
	s.gatewayURL = fg.url()
	tok, _, err := s.devSigner.mint("Hj4kPmRtw9nbCxz5vQ2y")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)
	gated := s.requireOperator(mux)

	body := `{"operationType":"ShredIdentityKey","payload":{"identityKey":"vtx.identity.x"}}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/op", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+tok)
	gated.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !fg.called {
		t.Fatal("the gateway never received the relayed op")
	}
	if fg.gotAuth != "Bearer "+tok {
		t.Errorf("relayed Authorization = %q, want the logged-in operator's own token", fg.gotAuth)
	}
	var sent map[string]any
	_ = json.Unmarshal(fg.gotBody, &sent)
	if sent["operationType"] != "ShredIdentityKey" {
		t.Errorf("relayed operationType = %v", sent["operationType"])
	}
	if _, hasActor := sent["actor"]; hasActor {
		t.Error("relayed request must never carry an actor field")
	}
}
