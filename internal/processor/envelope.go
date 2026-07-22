package processor

import "github.com/operatinggraph/lattice/internal/processor/opwire"

// The operation wire format (Contract #2 §2.1–§2.6) is defined in
// internal/processor/opwire, a leaf package that depends on nothing but the
// standard library and internal/substrate/keys. It is re-exported here because
// it reads as processor vocabulary at every call site in the platform, and
// because importing this package means linking a NATS client and a Starlark
// interpreter — which a browser-hosted Edge engine (edge-browser-node-design.md
// §3.2) must not do for the sake of an envelope struct. Code that needs only
// the wire format imports internal/processor/opwire directly.
//
// These are aliases, not new types: an opwire.OperationEnvelope IS a
// processor.OperationEnvelope.

// Lane is the JetStream lane enum per Contract #2 §2.3.
type Lane = opwire.Lane

const (
	LaneDefault = opwire.LaneDefault
	LaneMeta    = opwire.LaneMeta
	LaneUrgent  = opwire.LaneUrgent
	LaneSystem  = opwire.LaneSystem
)

// ContextHint mirrors Contract #2 §2.5 — the declared read set.
type ContextHint = opwire.ContextHint

// EnumerationHint is one declared kv.Links enumeration (Contract #2 §2.5.1).
type EnumerationHint = opwire.EnumerationHint

// AuthContext mirrors Contract #2 §2.8 — auth path declaration.
type AuthContext = opwire.AuthContext

// OperationEnvelope is the wire format a client publishes to `core-operations`.
type OperationEnvelope = opwire.OperationEnvelope

// ErrorCode is the closed enumeration of operation-reply error codes per
// Contract #2 §2.6.
type ErrorCode = opwire.ErrorCode

const (
	ErrCodeEnvelopeMalformed         = opwire.ErrCodeEnvelopeMalformed
	ErrCodeLaneUnauthorized          = opwire.ErrCodeLaneUnauthorized
	ErrCodeAuthDenied                = opwire.ErrCodeAuthDenied
	ErrCodeAuthContextMismatch       = opwire.ErrCodeAuthContextMismatch
	ErrCodeInternalError             = opwire.ErrCodeInternalError
	ErrCodeHydrationFailed           = opwire.ErrCodeHydrationFailed
	ErrCodeScriptFailed              = opwire.ErrCodeScriptFailed
	ErrCodeDDLViolation              = opwire.ErrCodeDDLViolation
	ErrCodeRevisionConflict          = opwire.ErrCodeRevisionConflict
	ErrCodeProtectedKey              = opwire.ErrCodeProtectedKey
	ErrCodeBatchTooLarge             = opwire.ErrCodeBatchTooLarge
	ErrCodeAuthInfrastructureFailure = opwire.ErrCodeAuthInfrastructureFailure
	ErrCodeClaimKeyInvalid           = opwire.ErrCodeClaimKeyInvalid
)

// ReplyStatus is the reply envelope status enum per Contract #2 §2.4.
type ReplyStatus = opwire.ReplyStatus

const (
	ReplyStatusAccepted  = opwire.ReplyStatusAccepted
	ReplyStatusDuplicate = opwire.ReplyStatusDuplicate
	ReplyStatusRejected  = opwire.ReplyStatusRejected
)

// ReplyError is the structured error payload on a rejected reply.
type ReplyError = opwire.ReplyError

// OperationReply is the request-reply response the Processor sends back to the
// operation submitter per Contract #2 §2.4.
type OperationReply = opwire.OperationReply

// ParseEnvelope unmarshals raw bytes and validates required fields per
// Contract #2 §2.2.
func ParseEnvelope(data []byte) (*OperationEnvelope, error) { return opwire.ParseEnvelope(data) }
