//go:build js

package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"syscall/js"

	"github.com/operatinggraph/lattice/internal/edge/agent"
	"github.com/operatinggraph/lattice/internal/processor/opwire"
)

// fetchSubmitter is the browser host's write door: the same Gateway POST
// /v1/operations contract agent.GatewaySubmitter speaks, issued over the
// browser's native fetch instead of net/http.
//
// It exists because net/http, in a wasm build, is a reimplementation of fetch
// — and it drags crypto/tls in with it, though the browser terminates TLS
// itself and the wasm code never sees a certificate. Linking that whole stack
// into the artifact only to have it call back out to fetch roughly doubles the
// gzipped size (measured this fire: ~1.4 MB → ~3.0 MB gz, past the FORK-W
// tripwire), and every byte of it is dead: net/http is reachable in the js
// engine graph ONLY through GatewaySubmitter. Talking to fetch directly is
// both smaller and the more honest realization of the design's "GatewaySubmitter
// (Fetch)" — so the browser host wires this, and the Go host keeps net/http.
//
// The wire contract is GatewaySubmitter's, reproduced rather than shared
// because the two now differ only in transport and sharing would drag net/http
// back into this package's graph. A drift between them is caught by both hosts
// exercising the same Gateway in the cross-machine e2e (W4).
type fetchSubmitter struct {
	url string
	// getToken returns the current bearer token, called fresh on every
	// Submit rather than captured once — the credential is exp-bounded (the
	// same JWT the WS shell's own getToken re-supplies on reconnect,
	// internal/edge/browser/shell/shell.mjs), so a long-lived submitter that
	// only ever read a token captured at Start() would start failing every
	// submit once it expired (agent.ErrCredentialRejected, sticky sign-out)
	// even after a refresh kept the shell's own connection alive.
	getToken func() string
}

// gatewayOperationRequest mirrors agent's unexported wire shape for POST
// /v1/operations. There is deliberately no actor/submittedAt field: the
// Gateway stamps both from the verified Bearer token, ignoring anything a
// caller sends. AuthContext forwards (it selects the auth path, not the actor).
type gatewayOperationRequest struct {
	RequestID     string                   `json:"requestId,omitempty"`
	Lane          string                   `json:"lane,omitempty"`
	OperationType string                   `json:"operationType"`
	Class         string                   `json:"class,omitempty"`
	Payload       json.RawMessage          `json:"payload,omitempty"`
	Reads         []string                 `json:"reads,omitempty"`
	OptionalReads []string                 `json:"optionalReads,omitempty"`
	Enumerations  []opwire.EnumerationHint `json:"enumerations,omitempty"`
	AuthContext   *opwire.AuthContext      `json:"authContext,omitempty"`
}

var _ agent.Submitter = (*fetchSubmitter)(nil)

// Submit implements agent.Submitter over fetch. Its outcome taxonomy is
// GatewaySubmitter's exactly, including agent.ErrCredentialRejected on a 401/403
// so the drain loop's errors.Is check (host.runDrainLoop) still fires the
// sign-out flow.
func (s *fetchSubmitter) Submit(ctx context.Context, env *opwire.OperationEnvelope) (*opwire.OperationReply, error) {
	token := s.getToken()
	if token == "" {
		return nil, fmt.Errorf("edge/browser: no gateway credential available to submit with")
	}
	req := gatewayOperationRequest{
		RequestID:     env.RequestID,
		Lane:          string(env.Lane),
		OperationType: env.OperationType,
		Class:         env.Class,
		Payload:       env.Payload,
		AuthContext:   env.AuthContext,
	}
	if env.ContextHint != nil {
		req.Reads = env.ContextHint.Reads
		req.OptionalReads = env.ContextHint.OptionalReads
		req.Enumerations = env.ContextHint.Enumerations
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("edge/browser: marshal gateway request: %w", err)
	}

	status, raw, err := s.fetch(ctx, body, token)
	if err != nil {
		return nil, fmt.Errorf("edge/browser: call gateway: %w", err)
	}

	var reply opwire.OperationReply
	if err := json.Unmarshal(raw, &reply); err == nil && reply.Status != "" {
		return &reply, nil
	}

	// 202 with a requestId is the Gateway timing out while waiting for the
	// Processor — the same async-timeout GatewaySubmitter reports (the outcome
	// lands in Core KV eventually; this drain does not know it yet).
	if status == 202 {
		var pending struct {
			RequestID string `json:"requestId"`
		}
		if err := json.Unmarshal(raw, &pending); err == nil && pending.RequestID != "" {
			return nil, fmt.Errorf("edge/browser: gateway submission timed out waiting for the Processor (requestId=%s)", pending.RequestID)
		}
	}

	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &errBody); err == nil && errBody.Error != "" {
		if isCredentialRejection(status) {
			return nil, fmt.Errorf("%w: %s (HTTP %d)", agent.ErrCredentialRejected, errBody.Error, status)
		}
		return nil, fmt.Errorf("edge/browser: gateway: %s (HTTP %d)", errBody.Error, status)
	}
	if isCredentialRejection(status) {
		return nil, fmt.Errorf("%w (HTTP %d)", agent.ErrCredentialRejected, status)
	}
	const snippetCap = 500
	snippet := raw
	if len(snippet) > snippetCap {
		snippet = snippet[:snippetCap]
	}
	return nil, fmt.Errorf("edge/browser: gateway: unrecognized response (HTTP %d): %s", status, string(snippet))
}

// isCredentialRejection matches agent's: the two codes gateway.go's
// authFailureStatus returns (401 authentication failed / 403 token revoked).
func isCredentialRejection(status int) bool {
	return status == 401 || status == 403
}

// fetch issues the POST and returns the HTTP status plus the response body. It
// runs on the caller's goroutine (never a js.Func callback), so the two awaits
// — one for the Response, one for its text — are safe.
func (s *fetchSubmitter) fetch(ctx context.Context, body []byte, token string) (int, []byte, error) {
	headers := js.Global().Get("Object").New()
	headers.Set("Content-Type", "application/json")
	headers.Set("Authorization", "Bearer "+token)

	init := js.Global().Get("Object").New()
	init.Set("method", "POST")
	init.Set("headers", headers)
	init.Set("body", toUint8Array(body))

	respV, err := await(ctx, js.Global().Call("fetch", s.url+"/v1/operations", init))
	if err != nil {
		return 0, nil, err
	}
	status := respV.Get("status").Int()

	textV, err := await(ctx, respV.Call("text"))
	if err != nil {
		return status, nil, fmt.Errorf("read gateway response: %w", err)
	}
	return status, []byte(textV.String()), nil
}
