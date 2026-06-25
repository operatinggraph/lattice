package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/asolgan/lattice/cmd/lattice/output"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// opRequest is the POST /api/op body. operationType is required; lane defaults
// to "default"; class is the optional DDL hint; payload is forwarded verbatim
// into the envelope (defaults to {} when omitted); reads is the optional
// Contract #2 §2.5 declared read set a read-dependent op (CreateLeaseApplication
// validates its applicant + unit by key, SignLease its application) must carry.
type opRequest struct {
	OperationType string          `json:"operationType"`
	Lane          string          `json:"lane,omitempty"`
	Class         string          `json:"class,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Reads         []string        `json:"reads,omitempty"`
}

// buildEnvelope turns a parsed opRequest into a processor.OperationEnvelope,
// stamping the request id, actor, and submit time the caller supplies. It
// validates operationType is present and the lane is a recognized enum, and
// fills empty fields with safe defaults (lane→default, payload→{}). It does not
// touch NATS — the request→envelope seam the handler wraps.
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
	if reads := cleanReads(req.Reads); len(reads) > 0 {
		env.ContextHint = &processor.ContextHint{Reads: reads}
	}
	return env, nil
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
func laneValid(lane processor.Lane) bool {
	switch lane {
	case processor.LaneDefault, processor.LaneMeta, processor.LaneUrgent, processor.LaneSystem:
		return true
	}
	return false
}

// handleOp implements POST /api/op. It parses the body into an opRequest, builds
// an envelope (stamping a fresh request id + the admin actor), submits it via
// output.SubmitOp, and returns the OperationReply. The applicant app submits
// CreateLeaseApplication (apply) and SignLease (sign) through this path.
func (s *server) handleOp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusBadRequest, "POST required")
		return
	}
	conn, ok := s.requireConn(w)
	if !ok {
		return
	}
	if s.adminActor == "" {
		s.writeError(w, http.StatusBadGateway,
			"admin actor not loaded; a valid bootstrap file (BOOTSTRAP_JSON_PATH) is required to submit ops")
		return
	}

	body, err := requireBody(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req opRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeError(w, http.StatusBadRequest, "parse body: "+err.Error())
		return
	}

	requestID, err := substrate.NewNanoID()
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "generate request id: "+err.Error())
		return
	}
	env, err := buildEnvelope(req, requestID, s.adminActor, time.Now())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := s.reqContext(r)
	defer cancel()

	reply, err := output.SubmitOp(ctx, conn, env)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "submit op: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, reply)
}
