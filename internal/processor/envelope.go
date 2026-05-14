package processor

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/asolgan/lattice/internal/substrate"
)

// Lane is the JetStream lane enum per Contract #2 §2.3.
type Lane string

const (
	LaneDefault Lane = "default"
	LaneMeta    Lane = "meta"
	LaneUrgent  Lane = "urgent"
	LaneSystem  Lane = "system"
)

func (l Lane) valid() bool {
	switch l {
	case LaneDefault, LaneMeta, LaneUrgent, LaneSystem:
		return true
	}
	return false
}

// ContextHint mirrors Contract #2 §2.5 — declared read set.
type ContextHint struct {
	Reads []string `json:"reads,omitempty"`
}

// AuthContext mirrors Contract #2 §2.8 — auth path declaration.
type AuthContext struct {
	Service string `json:"service,omitempty"`
	Task    string `json:"task,omitempty"`
	Target  string `json:"target,omitempty"`
}

// OperationEnvelope is the wire format a client publishes to
// `core-operations`. See Contract #2 §2.1 for full semantics.
type OperationEnvelope struct {
	RequestID     string          `json:"requestId"`
	Lane          Lane            `json:"lane"`
	OperationType string          `json:"operationType"`
	Actor         string          `json:"actor"`
	SubmittedAt   string          `json:"submittedAt"`
	Payload       json.RawMessage `json:"payload"`
	// Class is the optional class hint for Story 1.6 DDL lookup. Once
	// the DDL cache lands (Story 1.10) this becomes derivable from
	// operationType and the field can be removed. See
	// cmd/processor/CONTRACT-AMENDMENT-REQUEST.md (1.6 entry).
	Class       string       `json:"class,omitempty"`
	ContextHint *ContextHint `json:"contextHint,omitempty"`
	AuthContext *AuthContext `json:"authContext,omitempty"`
}

// ErrorCode is the closed enumeration of operation-reply error codes per
// Contract #2 §2.6.
type ErrorCode string

const (
	ErrCodeEnvelopeMalformed   ErrorCode = "EnvelopeMalformed"
	ErrCodeLaneUnauthorized    ErrorCode = "LaneUnauthorized"
	ErrCodeAuthDenied          ErrorCode = "AuthDenied"
	ErrCodeAuthContextMismatch ErrorCode = "AuthContextMismatch"
	ErrCodeInternalError       ErrorCode = "InternalError"
	// Story 1.6 error codes for steps 4/5 typed failures.
	ErrCodeHydrationFailed ErrorCode = "HydrationFailed"
	ErrCodeScriptFailed    ErrorCode = "ScriptFailed"
)

// Status is the reply envelope status enum per Contract #2 §2.4.
type ReplyStatus string

const (
	ReplyStatusAccepted  ReplyStatus = "accepted"
	ReplyStatusDuplicate ReplyStatus = "duplicate"
	ReplyStatusRejected  ReplyStatus = "rejected"
)

// ReplyError is the structured error payload on a rejected reply.
type ReplyError struct {
	Code    ErrorCode      `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// OperationReply is the request-reply response the Processor sends back
// to the operation submitter per Contract #2 §2.4.
//
// For Story 1.5 with steps 4-10 stubbed, `accepted` replies carry the
// `decision: accepted-stub` marker in Details so callers can disambiguate
// stubbed-success from fully-committed success (Story 1.7 swaps to real
// commit semantics with `decision` removed).
type OperationReply struct {
	RequestID           string      `json:"requestId"`
	OpTrackerKey        string      `json:"opTrackerKey"`
	Status              ReplyStatus `json:"status"`
	CommittedAt         string      `json:"committedAt,omitempty"`
	OriginalCommittedAt string      `json:"originalCommittedAt,omitempty"`
	Error               *ReplyError `json:"error,omitempty"`
	// Decision is a Story 1.5 marker that the reply was produced by the
	// stubbed commit path. Removed in Story 1.7 once real commit lands.
	Decision string `json:"decision,omitempty"`
}

// ParseEnvelope unmarshals raw bytes and validates required fields per
// Contract #2 §2.2. On any failure returns an error suitable for logging
// + nack-with-term at step 1.
func ParseEnvelope(data []byte) (*OperationEnvelope, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("envelope: empty payload")
	}
	var env OperationEnvelope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return nil, fmt.Errorf("envelope: json decode: %w", err)
	}
	if env.RequestID == "" {
		return nil, fmt.Errorf("envelope: requestId is required")
	}
	if !substrate.IsValidNanoID(env.RequestID) {
		return nil, fmt.Errorf("envelope: requestId %q is not a valid Contract #1 NanoID", env.RequestID)
	}
	if env.Lane == "" {
		return nil, fmt.Errorf("envelope: lane is required")
	}
	if !env.Lane.valid() {
		return nil, fmt.Errorf("envelope: lane %q is not a recognized enum value", env.Lane)
	}
	if env.OperationType == "" {
		return nil, fmt.Errorf("envelope: operationType is required")
	}
	if env.Actor == "" {
		return nil, fmt.Errorf("envelope: actor is required")
	}
	if env.SubmittedAt == "" {
		return nil, fmt.Errorf("envelope: submittedAt is required")
	}
	if len(env.Payload) == 0 {
		return nil, fmt.Errorf("envelope: payload is required (use {} for empty)")
	}
	return &env, nil
}

