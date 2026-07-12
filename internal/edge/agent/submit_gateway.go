package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/asolgan/lattice/internal/processor"
)

// gatewayOperationRequest mirrors internal/gateway's operationRequest (the
// wire shape POST /v1/operations accepts) and cmd/loupe/gatewayrelay.go's
// copy of it — reproduced here rather than imported for the same reason
// replyInboxHeader is: this internal package does not depend on a cmd/
// package, and internal/gateway's shape is unexported. There is
// deliberately no actor/submittedAt field: the Gateway stamps both itself
// from the caller's verified Bearer token, ignoring anything a caller might
// send.
type gatewayOperationRequest struct {
	RequestID     string                      `json:"requestId,omitempty"`
	Lane          string                      `json:"lane,omitempty"`
	OperationType string                      `json:"operationType"`
	Class         string                      `json:"class,omitempty"`
	Payload       json.RawMessage             `json:"payload,omitempty"`
	Reads         []string                    `json:"reads,omitempty"`
	OptionalReads []string                    `json:"optionalReads,omitempty"`
	Enumerations  []processor.EnumerationHint `json:"enumerations,omitempty"`
}

type gatewayErrorBody struct {
	Error string `json:"error"`
}

// GatewaySubmitter submits an intent through the Gateway's POST
// /v1/operations, presenting Token as the caller's own Bearer credential
// (edge-lattice-full-design.md EDGE.3: "the node ... submits intents
// through the Gateway (verify-and-stamp env.Actor)"). The Gateway
// re-verifies the token and stamps the verified subject as env.Actor itself
// — this is the untrusted multi-identity posture; a revoked or invalid
// token is denied by the Gateway before any envelope ever reaches
// core-operations.
type GatewaySubmitter struct {
	// URL is the Gateway's base URL, e.g. "http://localhost:8080".
	URL string
	// Token is the bearer JWT (Contract #11) presented on every submit —
	// the same EDGE_TOKEN the node's NATS connection authenticates with.
	Token string
	// Client defaults to http.DefaultClient when nil.
	Client *http.Client
}

// Submit implements Submitter.
func (g *GatewaySubmitter) Submit(ctx context.Context, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
	if g.Token == "" {
		return nil, fmt.Errorf("edge/agent: no gateway credential available to submit with")
	}
	req := gatewayOperationRequest{
		RequestID:     env.RequestID,
		Lane:          string(env.Lane),
		OperationType: env.OperationType,
		Class:         env.Class,
		Payload:       env.Payload,
	}
	if env.ContextHint != nil {
		req.Reads = env.ContextHint.Reads
		req.OptionalReads = env.ContextHint.OptionalReads
		req.Enumerations = env.ContextHint.Enumerations
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal gateway request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.URL+"/v1/operations", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build gateway request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+g.Token)

	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call gateway: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read gateway response: %w", err)
	}

	var reply processor.OperationReply
	if err := json.Unmarshal(raw, &reply); err == nil && reply.Status != "" {
		return &reply, nil
	}

	if resp.StatusCode == http.StatusAccepted {
		var pending struct {
			RequestID string `json:"requestId"`
		}
		if err := json.Unmarshal(raw, &pending); err == nil && pending.RequestID != "" {
			return nil, fmt.Errorf("gateway: submission timed out waiting for the Processor (requestId=%s); check Core KV for the eventual outcome", pending.RequestID)
		}
	}

	var errBody gatewayErrorBody
	if err := json.Unmarshal(raw, &errBody); err == nil && errBody.Error != "" {
		return nil, fmt.Errorf("gateway: %s (HTTP %d)", errBody.Error, resp.StatusCode)
	}
	const snippetCap = 500
	snippet := raw
	if len(snippet) > snippetCap {
		snippet = snippet[:snippetCap]
	}
	return nil, fmt.Errorf("gateway: unrecognized response (HTTP %d): %s", resp.StatusCode, string(snippet))
}
