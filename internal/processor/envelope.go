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
// ScanPrefixes (Story 4.4): when non-empty, the hydrator performs a prefix scan
// over Core KV for each prefix and bulk-loads all matching entries into the
// script's state global. Phase 1 only supports "vtx.identity." and
// "lnk.identity." as prefixes; other prefixes return
// HydrationError("scan-prefix-not-supported"). For "vtx.identity." the hydrator
// also loads 4 hard-coded aspects (.name/.email/.phone/.state) per vertex.
// For "lnk.identity." 6-segment link keys are loaded as-is.
// Soft cap: > 1000 keys per prefix returns HydrationError("scan-too-large").
// NFR-SC1: operator cells target ≤500 identities.
type ContextHint struct {
	Reads        []string `json:"reads,omitempty"`
	ScanPrefixes []string `json:"scanPrefixes,omitempty"`
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
	// Class is the Phase-1 transitional DDL hint. Story 1.6 added it as
	// a stop-gap; Story 1.7 brought the DDL cache forward so the
	// Hydrator + Validator can resolve class via the cache once the
	// operationType→class derivation is wired (Story 1.10+). Until
	// then `class` remains the supported way for clients to tell the
	// Processor which DDL their operation targets — it stays `omitempty`
	// so existing wire payloads are unaffected. See
	// cmd/processor/CONTRACT-AMENDMENT-REQUEST.md (1.6 entry, resolved
	// in 1.7) and data-contracts.md Contract #2 §2.1 addendum.
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
	// Story 1.7 error codes for steps 6/8 typed failures.
	ErrCodeDDLViolation     ErrorCode = "DDLViolation"
	ErrCodeRevisionConflict ErrorCode = "RevisionConflict"
	// Story 3.3 error codes for step-3 Capability KV auth.
	// ErrCodeAuthFreshnessExceeded fires when the Capability KV projection
	// for the actor is staler than the freshness ceiling (5 × NFR-P3 by
	// default). Hard denial — operation rejected so the actor doesn't see
	// auth state from an arbitrarily-out-of-date projection. See Contract
	// #6 §6.8 spirit + Decision #6 in the Story 3.3 brief.
	ErrCodeAuthFreshnessExceeded ErrorCode = "AuthFreshnessExceeded"
	// ErrCodeAuthInfrastructureFailure is reserved for surfacing a real
	// NATS / Capability KV outage to the reply path. The Story 3.3
	// CapabilityAuthorizer returns an `error` (not a Decision) when the
	// KV GET fails for any reason other than ErrKeyNotFound; the commit
	// path's existing authorizer-error branch maps that to InternalError
	// today. This code is the typed alternative for callers that want to
	// distinguish "auth-plane broken" from "any other internal error" —
	// wired through if/when 3.5 (traceability) needs it.
	ErrCodeAuthInfrastructureFailure ErrorCode = "AuthInfrastructureFailure"
	// ErrCodeClaimKeyInvalid is the generic rejection code for all
	// ClaimIdentity failure modes (Story 4.3 / NFR-S6 anti-enumeration).
	// Callers cannot distinguish wrong-key / wrong-state / already-bound /
	// merged — all map to this single code. Specific outcomes surface only
	// via Health KV at health.processor.<instance>.claim-attempts.<outcome>.
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
//
// Story 1.7 swaps the Story-1.5 `decision: accepted-stub` marker for the
// canonical `decision: committed` semantic — alongside a `revisions`
// map that lets clients perform read-your-own-writes against the
// committed keys' revisions. The Decision field is preserved as a
// transitional disambiguator until the meta-lane request-reply contract
// stabilises (Story 1.9+).
type OperationReply struct {
	RequestID           string      `json:"requestId"`
	OpTrackerKey        string      `json:"opTrackerKey"`
	Status              ReplyStatus `json:"status"`
	CommittedAt         string      `json:"committedAt,omitempty"`
	OriginalCommittedAt string      `json:"originalCommittedAt,omitempty"`
	Error               *ReplyError `json:"error,omitempty"`
	// Decision: "committed" (Story 1.7+ success), "accepted-stub"
	// (Story 1.5 transitional — no longer emitted), "duplicate" when
	// step 2 short-circuits.
	Decision string `json:"decision,omitempty"`
	// Revisions: per-key revision map returned by the substrate after
	// a successful atomic batch. Populated on `accepted` replies in
	// Story 1.7+. Useful for client RYOW polling.
	Revisions map[string]uint64 `json:"revisions,omitempty"`
	// Detail: optional structured data the script surfaced via its
	// "response" return key (Story 4.2 extension). May contain
	// sensitive tokens (e.g. plaintext claim keys). MUST NOT be logged
	// by any middleware or interceptor (NFR-S6/S7 compliance).
	// Only present on accepted replies when the script populated it.
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

