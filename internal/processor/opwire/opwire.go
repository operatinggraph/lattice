// Package opwire holds the operation wire format — the envelope a client
// publishes to `core-operations` and the reply the Processor sends back
// (Contract #2 §2.1–§2.6) — plus the parse that validates it. It depends on
// nothing but the standard library and internal/substrate/keys.
//
// It exists because internal/processor bundles the whole commit path beside
// these types, so importing an OperationEnvelope links a NATS client (and a
// Starlark interpreter). An Edge node constructs envelopes and reads replies
// but runs none of that; a browser-hosted engine must not carry it at all
// (edge-browser-node-design.md §3.2) — the transport it is allowed is the
// Gateway door, and a linked NATS client is both dead weight in the artifact
// and a bypass waiting to be wired. internal/processor re-exports every name
// here, so platform call sites read as processor vocabulary and do not import
// this package directly.
package opwire

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate/keys"
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
//
// The read posture (Contract #2 §2.5 "Read posture"): `Reads` is fail-closed
// (a missing key faults HydrationMiss — class (a)); `OptionalReads` is
// absence-tolerant (a missing key is recorded known-absent and kv.Read serves
// None from the step-4 snapshot — class (d), the read-before-create / dedup
// pattern); `Enumerations` declares kv.Links link-enumerations (§2.5.1) as
// METADATA only (class (e)) — the enumeration stays a bounded paged live read,
// never hydrated; the declaration feeds the Edge mirror-coverage gate and the
// static read-classification lint; `EgressReads` declares reads for external
// egress (§2.5 class (f)) — fail-closed like `Reads`, except a sensitive-DDL
// key hydrates as a `$sensitiveRef` marker (ciphertext, never plaintext)
// rather than decrypted plaintext; a non-sensitive key hydrates identically to
// a plain read. A key may not appear in `EgressReads` AND EITHER `Reads` or
// `OptionalReads` (parse error — ambiguous disposition).
type ContextHint struct {
	Reads         []string          `json:"reads,omitempty"`
	OptionalReads []string          `json:"optionalReads,omitempty"`
	Enumerations  []EnumerationHint `json:"enumerations,omitempty"`
	EgressReads   []string          `json:"egressReads,omitempty"`
}

// EnumerationHint is one declared kv.Links enumeration (Contract #2 §2.5 —
// `contextHint.enumerations`): the hub vertex key, the link relation, and the
// direction the hub sits in the link ("out" = hub is source, "in" = hub is
// target). Metadata, not a hydration directive — the Processor validates the
// shape at parse and otherwise ignores it (the script's kv.Links call is what
// executes, paged and live).
type EnumerationHint struct {
	Hub       string `json:"hub"`
	Relation  string `json:"relation"`
	Direction string `json:"direction"`
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
	// ErrCodeProtectedKey is the step-8 authoritative backstop: any update or
	// tombstone whose root document carries data.protected == true is rejected
	// at commit time, path-independent (InstallPackage, UninstallPackage,
	// meta-root, or any future DDL). It is the kernel/auth bricking guard —
	// the script-level checks are best-effort defense-in-depth only.
	ErrCodeProtectedKey ErrorCode = "ProtectedKey"
	// ErrCodeBatchTooLarge is the step-8 rejection when a single operation's
	// atomic batch exceeds the message-count ceiling (>998 business mutations)
	// or a value exceeds the payload ceiling (Contract #2 §2.6 / #3 §3.9.1).
	// Terminal — a redelivery reproduces the identical over-limit batch.
	ErrCodeBatchTooLarge ErrorCode = "BatchTooLarge"
	// Step-3 Capability KV auth codes.
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
	// successful atomic batch. Useful for client RYOW polling. The full
	// committed key set is the key set of this map.
	Revisions map[string]uint64 `json:"revisions,omitempty"`
	// PrimaryKey: the single principal Core KV key an operation wrote,
	// surfaced by the script via the closed `response` return dict
	// (`{"primaryKey": <key>}`). The Processor validates that the value
	// is a member of the committed mutation key set before it reaches the
	// reply — a script can only point at a key it actually committed; it
	// cannot smuggle arbitrary data. Multi-key operations with no single
	// principal entity (InstallPackage / UninstallPackage) omit it; their
	// clients read the full key set from Revisions.
	PrimaryKey string `json:"primaryKey,omitempty"`
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
	if !keys.IsValidNanoID(env.RequestID) {
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
	if env.ContextHint != nil {
		for i, e := range env.ContextHint.Enumerations {
			if e.Hub == "" || e.Relation == "" {
				return nil, fmt.Errorf("envelope: contextHint.enumerations[%d] requires hub and relation", i)
			}
			if e.Direction != "out" && e.Direction != "in" {
				return nil, fmt.Errorf("envelope: contextHint.enumerations[%d] direction must be \"out\" or \"in\", got %q", i, e.Direction)
			}
		}
		if len(env.ContextHint.EgressReads) > 0 {
			// A key must carry exactly one disposition — reject it declared
			// under egressReads AND EITHER of the other two read classes,
			// not just reads. optionalReads⊓egressReads is the same
			// ambiguity: without this check the optionalReads hydration
			// loop (which runs first) would win and cache the key as
			// PLAINTEXT, silently demoting its egressReads disposition
			// instead of refusing loudly.
			other := make(map[string]struct{}, len(env.ContextHint.Reads)+len(env.ContextHint.OptionalReads))
			for _, k := range env.ContextHint.Reads {
				other[k] = struct{}{}
			}
			for _, k := range env.ContextHint.OptionalReads {
				other[k] = struct{}{}
			}
			for _, k := range env.ContextHint.EgressReads {
				if _, dup := other[k]; dup {
					return nil, fmt.Errorf("envelope: contextHint key %q declared in egressReads and also in reads/optionalReads (ambiguous disposition)", k)
				}
			}
		}
	}
	return &env, nil
}
