package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/asolgan/lattice/internal/processor"
)

// The op-submission half of the operator-auth lift
// (loupe-operator-auth-lift-design.md §3.2). Loupe no longer stamps its own
// actor onto an operation — it relays the requesting operator's own verified
// Bearer token to the Gateway's POST /v1/operations, which re-verifies it
// and stamps the verified subject itself. A compromised Loupe process can
// only act as operators whose tokens currently pass through it; it can never
// forge an arbitrary actor the way direct-NATS stamping of s.adminActor did.

// gatewayHTTPClient carries NO client-level Timeout deliberately — every
// call site passes a context already bounded by s.gatewaySubmitContext (see
// server.go), which is the one deadline that matters. A second, fixed
// client-level timeout raced that context in an earlier version of this
// file and, being equal to or shorter than it, silently won: it fired before
// the Gateway's own longer-lived wait-for-Processor timeout could ever
// produce its designed 202-with-requestId fallback, so a slow-but-live
// Processor surfaced as a bare "context deadline exceeded" with no requestId
// a caller could poll Core KV with. One deadline, set where the caller's
// actual budget is known, avoids that.
var gatewayHTTPClient = &http.Client{}

// gatewayOperationRequest mirrors internal/gateway's operationRequest — the
// wire shape POST /v1/operations accepts. There is deliberately no actor
// field: the Gateway stamps the Bearer token's verified subject, ignoring
// anything a caller might send.
type gatewayOperationRequest struct {
	RequestID     string          `json:"requestId,omitempty"`
	Lane          string          `json:"lane,omitempty"`
	OperationType string          `json:"operationType"`
	Class         string          `json:"class,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Reads         []string        `json:"reads,omitempty"`
	OptionalReads []string        `json:"optionalReads,omitempty"`
}

// gatewayErrorBody is the JSON shape the Gateway answers with for a failure
// it never gets far enough to turn into a processor.OperationReply (a bad
// bearer token, a malformed request body, an oversized payload).
type gatewayErrorBody struct {
	Error string `json:"error"`
}

// submitOpViaGateway relays req to the Gateway's POST /v1/operations with
// bearerToken as the caller's own credential. A response that decodes as a
// processor.OperationReply with a non-empty Status is returned as-is
// regardless of HTTP status — accepted, duplicate, and a Processor-level
// rejection (e.g. a denied meta-lane op) all arrive this way, so every
// existing caller's reply.Status / reply.Error handling keeps working
// unchanged. A response the Gateway never got far enough to shape as a reply
// (a rejected token, a bad request body, a timeout still awaiting the
// Processor) becomes a plain Go error instead.
func submitOpViaGateway(ctx context.Context, gatewayURL, bearerToken string, req gatewayOperationRequest) (*processor.OperationReply, error) {
	if bearerToken == "" {
		return nil, fmt.Errorf("no operator credential available to relay")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal gateway request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/v1/operations", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build gateway request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+bearerToken)

	resp, err := gatewayHTTPClient.Do(httpReq)
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
	// A response shape none of the above recognize (e.g. LOUPE_GATEWAY_URL
	// misconfigured to something that isn't the Gateway at all, or a
	// reverse-proxy error page) — bounded well below the 1MiB read cap so an
	// operator-visible error can't balloon into an accidental dump of
	// whatever answered.
	const snippetCap = 500
	snippet := raw
	if len(snippet) > snippetCap {
		snippet = snippet[:snippetCap]
	}
	return nil, fmt.Errorf("gateway: unrecognized response (HTTP %d): %s", resp.StatusCode, string(snippet))
}

// pkgmgrSubmit satisfies internal/pkgmgr.Installer.Submit, relaying every
// package-lifecycle op (InstallPackage/UpgradePackage/UninstallPackage)
// through the Gateway under whatever operator token ctx carries, in place of
// the installer's default direct-NATS submission stamping AdminActor. Always
// meta-lane and read-declaration-free, matching the installer's own
// direct-NATS submitOp exactly.
func (s *server) pkgmgrSubmit(ctx context.Context, operationType, class, requestID string, payload map[string]any) (*processor.OperationReply, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return submitOpViaGateway(ctx, s.gatewayURL, operatorToken(ctx), gatewayOperationRequest{
		RequestID:     requestID,
		Lane:          string(processor.LaneMeta),
		OperationType: operationType,
		Class:         class,
		Payload:       payloadJSON,
	})
}
