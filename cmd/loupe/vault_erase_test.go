package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// vaultEraseFixture wires an embedded NATS Core-KV bucket, optionally seeded
// with an installed identityErasure Loom pattern meta-vertex (the shape
// buildWeaverMetaIndex reads — TestBuildWeaverMetaIndexKeysOffTheSpecBody
// pins the resolver itself; this pins handleVaultErase's use of it end to
// end), plus a server wired to a stub Gateway.
func vaultEraseFixture(t *testing.T, seedPattern bool) (srv *server, captured *gatewayOperationRequest) {
	t.Helper()
	ns := natsfixture.StartServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: ns.ClientURL(), Name: "loupe-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bootstrap.CoreKVBucket}); err != nil {
		t.Fatalf("create core-kv bucket: %v", err)
	}
	if seedPattern {
		spec := []byte(`{"data":{"patternId":"identityErasure","subjectType":"identity","steps":[]}}`)
		if _, err := conn.KVPut(ctx, bootstrap.CoreKVBucket, "vtx.meta.pErasureAAAAAAAAAAA.spec", spec); err != nil {
			t.Fatalf("put pattern spec: %v", err)
		}
	}

	url, cap := stubGateway(t, processor.OperationReply{
		RequestID: "req1", Status: processor.ReplyStatusAccepted, Decision: "committed",
	})
	srv = &server{conn: conn, gatewayURL: url, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), natsTimeout: 5 * time.Second}
	return srv, cap
}

// callErase drives handleVaultErase directly (mirroring callMarkApplied /
// callPropose) with an operator token in the request context — the token
// requireOperator normally puts there, which is not part of this fixture
// (registerRoutes alone, no auth middleware).
func callErase(t *testing.T, srv *server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/vault/erase", r)
	req = req.WithContext(context.WithValue(req.Context(), operatorTokenContextKey{}, "test-operator-token"))
	rec := httptest.NewRecorder()
	srv.handleVaultErase(rec, req)
	return rec
}

// TestVaultErase_ResolvesPatternAndSubmitsStartLoomPattern pins the happy
// path: the canonical name "identityErasure" resolves to its installed
// vtx.meta.<id> key, and the relayed op carries that resolved key as
// patternRef (never the bare name — StartLoomPattern looks its `.spec` up by
// key, packages/orchestration-base/loom_lifecycle.go) plus the identity as
// subjectKey.
func TestVaultErase_ResolvesPatternAndSubmitsStartLoomPattern(t *testing.T) {
	srv, captured := vaultEraseFixture(t, true)

	rec := callErase(t, srv, http.MethodPost, `{"identityKey":"vtx.identity.LoupeEraseMeAAAAAAAA"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var reply processor.OperationReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply.Status != processor.ReplyStatusAccepted {
		t.Errorf("reply.Status = %q, want accepted", reply.Status)
	}

	if captured.OperationType != "StartLoomPattern" {
		t.Errorf("operationType = %q, want StartLoomPattern", captured.OperationType)
	}
	var payload struct {
		PatternRef string `json:"patternRef"`
		SubjectKey string `json:"subjectKey"`
	}
	if err := json.Unmarshal(captured.Payload, &payload); err != nil {
		t.Fatalf("decode relayed payload: %v", err)
	}
	if payload.PatternRef != "vtx.meta.pErasureAAAAAAAAAAA" {
		t.Errorf("patternRef = %q, want the resolved meta-vertex key, not the bare canonical name", payload.PatternRef)
	}
	if payload.SubjectKey != "vtx.identity.LoupeEraseMeAAAAAAAA" {
		t.Errorf("subjectKey = %q", payload.SubjectKey)
	}
}

// TestVaultErase_PatternNotInstalled pins the degraded shape: a stack whose
// privacy-base package isn't installed has no identityErasure pattern meta,
// and the endpoint reports that as an upstream error rather than silently
// submitting an op the engine cannot resolve.
func TestVaultErase_PatternNotInstalled(t *testing.T) {
	srv, captured := vaultEraseFixture(t, false)

	rec := callErase(t, srv, http.MethodPost, `{"identityKey":"vtx.identity.LoupeEraseMeAAAAAAAA"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s, want 502", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Fatalf("want an {error} body, got err=%v body=%+v", err, body)
	}
	if captured.OperationType != "" {
		t.Errorf("no op should have been relayed, got %+v", captured)
	}
}

// TestVaultErase_RejectsMalformedRequests pins the guard rails: a non-identity
// key and a non-POST method both fail closed before any pattern resolution or
// relay.
func TestVaultErase_RejectsMalformedRequests(t *testing.T) {
	srv, captured := vaultEraseFixture(t, true)

	// A vertex of the wrong type.
	rec1 := callErase(t, srv, http.MethodPost, `{"identityKey":"vtx.task.LoupeNotAnIdentityAAA"}`)
	if rec1.Code != http.StatusBadRequest {
		t.Errorf("wrong-type key status = %d, want 400", rec1.Code)
	}

	// An aspect key, not a vertex root.
	rec2 := callErase(t, srv, http.MethodPost, `{"identityKey":"vtx.identity.LoupeEraseMeAAAAAAAA.ssn"}`)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("aspect key status = %d, want 400", rec2.Code)
	}

	rec3 := callErase(t, srv, http.MethodGet, "")
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("GET status = %d, want 400", rec3.Code)
	}

	if captured.OperationType != "" {
		t.Errorf("no op should have been relayed for any rejected request, got %+v", captured)
	}
}
