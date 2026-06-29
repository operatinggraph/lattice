package substrate

import (
	"context"
	"log/slog"
	"time"
)

// DeliverPolicy is the substrate-owned delivery-policy enum for a supervised
// consumer. It deliberately mirrors only the policies Lattice consumers need so
// that no jetstream.DeliverPolicy escapes the supervisor's exported surface.
type DeliverPolicy int

const (
	// DeliverAll starts delivery at the beginning of the durable's history
	// (jetstream.DeliverAllPolicy). The default zero value.
	DeliverAll DeliverPolicy = iota
	// DeliverLastPerSubject delivers only the latest message per subject
	// (jetstream.DeliverLastPerSubjectPolicy). Required for Core KV CDC, whose
	// backing stream keeps history=1 (ADR-15).
	DeliverLastPerSubject
)

// FailureClass is the substrate-owned routing tier returned by a spec's
// Classify hook. It mirrors the caller's own failure taxonomy (e.g. Refractor's
// failure.Category) without the supervisor importing any caller package — the
// caller adapts its classifier to this enum.
type FailureClass int

const (
	// ClassTransient is recoverable: the message is redelivered. The default
	// zero value, so an unrecognised error is treated as transient.
	ClassTransient FailureClass = iota
	// ClassTerminal is permanently bad data: the message is disposed by the
	// handler (the supervisor does not Term it; policy stays with the caller).
	ClassTerminal
	// ClassInfra signals the downstream dependency is temporarily unavailable:
	// the pump pauses and enters the probe loop.
	ClassInfra
	// ClassStructural signals a permanent misconfiguration: the pump pauses
	// awaiting an operator Resume.
	ClassStructural
)

// PauseReason identifies why a supervised pump is paused. Reasons are composable
// (a pump may hold several at once); the pump runs only when no reason is held.
type PauseReason string

const (
	// PauseInfra is cleared automatically by a passing Probe.
	PauseInfra PauseReason = "infra"
	// PauseStructural is cleared only by an operator Resume.
	PauseStructural PauseReason = "structural"
	// PauseManual is cleared only by an operator Resume.
	PauseManual PauseReason = "manual"
)

// HealthStatus is the restored lifecycle status read from a HealthSink at
// startup.
type HealthStatus int

const (
	// StatusActive means the consumer should pump immediately. Returned for an
	// active entry, a missing entry, a malformed entry, or any unrecognised
	// status (including an interrupted "rebuilding").
	StatusActive HealthStatus = iota
	// StatusPaused means the consumer was paused when last persisted; the
	// accompanying PauseReason decides infra (probe loop) vs structural/manual
	// (block awaiting Resume).
	StatusPaused
)

// HealthSink persists and restores a supervised consumer's lifecycle state. It
// is keyed entirely by the CALLER — the supervisor never invents or namespaces
// health keys. A nil sink disables all health I/O (the supervisor still runs).
//
// Implementations must tolerate being called from the pump goroutine; sink
// errors are logged by the supervisor and never fatal.
type HealthSink interface {
	// SetActive records that the consumer is running.
	SetActive(ctx context.Context) error
	// SetPaused records that the consumer is paused for reason, with an
	// optional last-error string (empty when none).
	SetPaused(ctx context.Context, reason PauseReason, lastErr string) error
	// Load reads the persisted status and (when paused) the pause reason. A
	// missing or malformed entry must resolve to (StatusActive, "", nil).
	Load(ctx context.Context) (HealthStatus, PauseReason, error)
}

// SupervisedHandler processes one delivered message and returns an ack Decision
// plus an optional error. The two channels are orthogonal:
//
//   - A nil error means the handler disposed of the message itself (success,
//     skip, terminal-to-DLQ, or retry-queue enqueue); the returned Decision is
//     applied to the JetStream message (Ack / Nak / NakWithDelay / Term).
//     Deferred disposition (the retry-queue case) is modelled by returning Ack
//     after the handler has taken ownership of the eventual write.
//   - A non-nil error is routed through the spec's Classify hook. ClassInfra and
//     ClassStructural pause the pump and leave the message UN-acked / UN-naked so
//     JetStream redelivers it when the pump resumes (mirrors the pipeline's "do
//     NOT ack/nak on infra/structural" contract). ClassTransient and ClassTerminal
//     fall back to the returned Decision.
//
// The handler MUST be idempotent: at-least-once delivery means the same message
// can arrive again after a Nak, a pause/resume, or a crash-before-ack.
type SupervisedHandler func(ctx context.Context, msg Message) (Decision, error)

// ClassifyFunc maps a handler error to a substrate FailureClass. Supplied by the
// caller (policy). A nil ClassifyFunc treats every error as ClassTransient.
type ClassifyFunc func(err error) FailureClass

// ProbeFunc checks whether a paused-on-infra dependency has recovered. It
// returns nil when healthy. A probe error that Classify maps to ClassStructural
// escalates the pump from PauseInfra to PauseStructural. A nil ProbeFunc makes
// an infra pause behave like a structural one (no automatic recovery).
type ProbeFunc func(ctx context.Context) error

// ConsumerSpec is the full, caller-supplied description of one supervised
// consumer. Every field is data — the supervisor hard-codes nothing about the
// stream, subjects, durable, delivery policy, queue group, or backoff floor, so
// it is agnostic between event-stream durables (events.<domain>.>) and KV-CDC
// durables ($KV.<bucket>.>).
type ConsumerSpec struct {
	// Name is the supervisor-local identity AND the JetStream durable name. It
	// is also used as the registry key for Remove / Reset / Pause / Resume / Stop.
	Name string
	// Stream is the JetStream stream the durable binds to.
	Stream string
	// FilterSubject restricts delivery to matching subjects. Empty delivers all
	// subjects on the stream. Mutually exclusive with FilterSubjects — set one or
	// the other, never both.
	FilterSubject string
	// FilterSubjects restricts delivery to a SET of subjects (the multi-filter
	// form JetStream supports natively). Use it when one durable must cover
	// several discrete subjects that no single wildcard captures exactly — e.g.
	// the Processor's `processor-main` over [ops.default, ops.urgent, ops.system,
	// ops.meta]. When non-empty it takes precedence; FilterSubject must then be
	// empty (JetStream rejects a consumer config that sets both).
	FilterSubjects []string
	// DeliverPolicy selects where delivery starts (DeliverAll by default).
	DeliverPolicy DeliverPolicy
	// DeliverGroup sets the JetStream queue group (NFR12 fan-out across
	// instances). Empty omits the queue group.
	DeliverGroup string
	// RedeliveryDelay is the floor applied when the handler returns
	// NakWithDelay. Zero falls back to DefaultRedeliveryDelay.
	RedeliveryDelay time.Duration
	// ProbeInterval is the delay between Probe attempts during an infra pause.
	// Zero falls back to DefaultProbeInterval.
	ProbeInterval time.Duration
	// AckWait bounds how long JetStream waits for an ack before redelivering a
	// message. Zero leaves the JetStream default. A short AckWait makes the
	// un-acked message that triggered an infra pause redeliver promptly once the
	// pump resumes.
	AckWait time.Duration

	// Handler is the message-processing policy (required).
	Handler SupervisedHandler
	// Classify maps a handler/probe error to a FailureClass (optional; nil =
	// always ClassTransient).
	Classify ClassifyFunc
	// Probe checks infra-recovery during a probe loop (optional; nil = no
	// automatic recovery from an infra pause).
	Probe ProbeFunc
	// Health persists and restores lifecycle state (optional; nil = no health
	// I/O).
	Health HealthSink
	// Logger is the diagnostics sink. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// DefaultProbeInterval is the gap between Probe attempts when a spec leaves
// ProbeInterval at its zero value.
const DefaultProbeInterval = 10 * time.Second
