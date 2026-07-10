package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/processor"
)

// opRequest is the POST /api/op body. operationType is required; lane defaults
// to "default"; class is the optional DDL hint; payload is the raw operation
// payload forwarded verbatim into the envelope (defaults to {} when omitted so
// the Processor's "payload is required" check passes). reads is the optional
// Contract #2 §2.5 declared read set — a read-dependent op (Tombstone/Update/
// Assign/Grant…) must declare the keys it reads or its script sees no state and
// fails (e.g. UnknownRole). optionalReads is the class-(d) absence-tolerant
// counterpart (§2.5) — a key the op's own read-before-create/dedup branch may
// legitimately find absent; unlike reads, an absent optionalReads key never
// faults hydration. Empty/blank entries are dropped from both.
type opRequest struct {
	OperationType string          `json:"operationType"`
	Lane          string          `json:"lane,omitempty"`
	Class         string          `json:"class,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Reads         []string        `json:"reads,omitempty"`
	OptionalReads []string        `json:"optionalReads,omitempty"`
}

// buildEnvelope turns a parsed opRequest into a processor.OperationEnvelope,
// stamping the request id, actor, and submit time the caller supplies. It
// validates operationType is present and the lane is a recognized enum, and
// fills empty/whitespace fields with safe defaults (lane→default, payload→{}).
// It does not touch NATS — this is the request→envelope seam the handler wraps.
func buildEnvelope(req opRequest, requestID, actor string, now time.Time) (*processor.OperationEnvelope, error) {
	if req.OperationType == "" {
		return nil, fmt.Errorf("operationType is required")
	}
	lane := processor.Lane(req.Lane)
	if req.Lane == "" {
		lane = processor.LaneDefault
	}
	if !laneValid(lane) {
		return nil, fmt.Errorf("lane %q is not a recognized enum value (default|meta|urgent|system)", req.Lane)
	}
	payload := req.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	if !json.Valid(payload) {
		return nil, fmt.Errorf("payload is not valid JSON")
	}
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          lane,
		OperationType: req.OperationType,
		Actor:         actor,
		SubmittedAt:   now.UTC().Format(time.RFC3339),
		Class:         req.Class,
		Payload:       payload,
	}
	reads := cleanReads(req.Reads)
	optionalReads := cleanReads(req.OptionalReads)
	if len(reads) > 0 || len(optionalReads) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: reads, OptionalReads: optionalReads}
	}
	return env, nil
}

// gatewayRequestFromEnvelope narrows a validated OperationEnvelope down to
// the fields the Gateway's POST /v1/operations wire shape accepts. Actor and
// SubmittedAt are deliberately dropped — the Gateway stamps both itself, the
// former from the caller's verified Bearer token (never from anything Loupe
// asserts) and the latter from its own clock.
func gatewayRequestFromEnvelope(env *processor.OperationEnvelope) gatewayOperationRequest {
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
	}
	return req
}

// cleanReads trims and drops empty entries from a declared read set, preserving
// order and removing duplicates.
func cleanReads(reads []string) []string {
	if len(reads) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(reads))
	out := make([]string, 0, len(reads))
	for _, r := range reads {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// laneValid reports whether lane is one of the Contract #2 §2.3 lane enums.
// processor.Lane.valid is unexported, so this mirrors the closed set Loupe
// accepts from the UI.
func laneValid(lane processor.Lane) bool {
	switch lane {
	case processor.LaneDefault, processor.LaneMeta, processor.LaneUrgent, processor.LaneSystem:
		return true
	}
	return false
}
