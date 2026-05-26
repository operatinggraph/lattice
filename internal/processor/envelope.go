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
//
// Graph topology that a write-path op needs is delivered as command
// parameters: a Lens projects the topology into its own output bucket,
// the *client* reads the lens, and the discovered keys travel back
// through the operation envelope as command parameters declared in
// `Reads`. The script validates each declared key against Core KV
// (envelope class, endpoint touch, not tombstoned) before acting on it.
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
	// Class is the DDL hint carrying the canonical class name for the
	// operation (e.g., "identity", "org"). Clients must populate it so the
	// Hydrator and Validator can resolve the correct DDL schema. It stays
	// `omitempty` so existing wire payloads without it are unaffected.
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
	// Steps 4/5 typed failure codes.
	ErrCodeHydrationFailed ErrorCode = "HydrationFailed"
	ErrCodeScriptFailed    ErrorCode = "ScriptFailed"
	// Steps 6/8 typed failure codes.
	ErrCodeDDLViolation     ErrorCode = "DDLViolation"
	ErrCodeRevisionConflict ErrorCode = "RevisionConflict"
	// Step-3 Capability KV auth codes.
	// ErrCodeAuthFreshnessExceeded fires when the Capability KV projection
	// for the actor is staler than the freshness ceiling (5 × NFR-P3 by
	// default). Hard denial — operation rejected so the actor doesn't see
	// auth state from an arbitrarily-out-of-date projection (Contract #6 §6.8).
	ErrCodeAuthFreshnessExceeded ErrorCode = "AuthFreshnessExceeded"
	// ErrCodeAuthInfrastructureFailure surfaces a NATS / Capability KV outage
	// to the reply path. The CapabilityAuthorizer returns an error (not a
	// Decision) when the KV GET fails; the commit path maps that to this code
	// so callers can distinguish "auth-plane broken" from other internal errors.
	ErrCodeAuthInfrastructureFailure ErrorCode = "AuthInfrastructureFailure"
	// ErrCodeClaimKeyInvalid is the generic rejection code for all ClaimIdentity
	// failure modes (NFR-S6 anti-enumeration). Callers cannot distinguish
	// wrong-key / wrong-state / already-bound / merged — all map to this single
	// code. Specific outcomes surface only via Health KV.
	ErrCodeClaimKeyInvalid ErrorCode = "ClaimKeyInvalid"
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
type OperationReply struct {
	RequestID           string      `json:"requestId"`
	OpTrackerKey        string      `json:"opTrackerKey"`
	Status              ReplyStatus `json:"status"`
	CommittedAt         string      `json:"committedAt,omitempty"`
	OriginalCommittedAt string      `json:"originalCommittedAt,omitempty"`
	Error               *ReplyError `json:"error,omitempty"`
	// Decision is "committed" on accepted replies; "duplicate" when
	// step 2 short-circuits.
	Decision string `json:"decision,omitempty"`
	// Revisions: per-key revision map returned by the substrate after a
	// successful atomic batch. Useful for client RYOW polling.
	Revisions map[string]uint64 `json:"revisions,omitempty"`
	// Detail: optional structured data the script surfaced via its
	// "response" return key. May contain sensitive tokens (e.g.
	// plaintext claim keys). MUST NOT be logged by any middleware or
	// interceptor (NFR-S6/S7 compliance). Only present on accepted
	// replies when the script populated it.
	//
	// Convention-enforced semantics:
	//
	//   ALLOWED  (commit-trace shape):
	//     - Identifiers that already exist in the commit (linkKey,
	//       primary/secondary keys, requestId echoes).
	//     - Counts and ratios computed from the commit batch
	//       (mutationCount, linksMigrated, linkCollisionsMerged,
	//       eventCount, etc.).
	//     - One-time-use tokens delivered *only* in the reply
	//       (plaintext claimKey on CreateUnclaimedIdentity is the
	//       canonical exception — it is created server-side, has no
	//       Core-KV plaintext counterpart, and the reply is the
	//       single delivery channel).
	//
	//   FORBIDDEN (business data leak):
	//     - Identity .name / .email / .phone aspect values.
	//     - mergedInto target keys when the caller did not author the
	//       merge (NFR-S6 anti-enumeration).
	//     - Any aspect value the actor would otherwise need a
	//       Capability-Lens-authorized read to obtain.
	//
	// Reviewers (Winston + brief authors) maintain compliance; the
	// processor does not enforce in code. New op authors: cite this
	// block in your script's Detail-building section.
	Detail map[string]any `json:"detail,omitempty"`
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
