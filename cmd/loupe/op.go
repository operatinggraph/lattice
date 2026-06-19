package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/asolgan/lattice/internal/processor"
)

// opRequest is the POST /api/op body. operationType is required; lane defaults
// to "default"; class is the optional DDL hint; payload is the raw operation
// payload forwarded verbatim into the envelope (defaults to {} when omitted so
// the Processor's "payload is required" check passes).
type opRequest struct {
	OperationType string          `json:"operationType"`
	Lane          string          `json:"lane,omitempty"`
	Class         string          `json:"class,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
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
	return &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          lane,
		OperationType: req.OperationType,
		Actor:         actor,
		SubmittedAt:   now.UTC().Format(time.RFC3339),
		Class:         req.Class,
		Payload:       payload,
	}, nil
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
