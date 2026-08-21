// Package wire is the model-runner's transport contract: the request/ack/
// result shapes, the subject and queue-group names, the result bucket name,
// and a thin request client over a *nats.Conn.
//
// It is deliberately a leaf — standard library plus nats.go, nothing else.
// Both sides of the call import it: the runner (internal/modelrunner) and
// every caller (the bridge's model-backed adapters). A caller must be able to
// speak to the runner without pulling in the vendor SDK, the package manager,
// or any Lattice engine, so nothing heavier ever belongs here.
//
// The contract is domain-free. A request names a model, a system prompt, a
// user prompt, and exactly ONE tool; the runner forces the model to answer
// through that tool and hands back the tool's input JSON verbatim. Meaning
// lives entirely with the caller: the runner never inspects the schema or the
// output.
package wire

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

const (
	// ServiceName is the NATS micro service name the runner registers under
	// ($SRV.PING/INFO/STATS discovery).
	ServiceName = "model-runner"

	// GenerateSubject is the request subject for a single model call.
	GenerateSubject = "svc.model.generate"

	// SubjectPrefix is the runner's whole subject space — the natsperm grant
	// unit ("svc.model.>"), kept next to the subject it must cover so a new
	// endpoint cannot silently land outside the permission matrix.
	SubjectPrefix = "svc.model."

	// QueueGroup load-balances requests across the runner fleet: N instances
	// share the subject, one receives each request, and an instance that dies
	// simply stops being picked.
	QueueGroup = "model-runners"

	// ResultsBucket is the KV bucket the runner writes results to, keyed by
	// the request's Ref. Callers poll `<ref>`; they never receive the model
	// output on the reply subject.
	ResultsBucket = "model-results"
)

// AckStatus is the runner's immediate answer to a request. It reports whether
// the work was taken on — never whether the model succeeded, which lands in
// the result bucket minutes later.
type AckStatus string

const (
	// AckAccepted means the ref is now in flight (or was already in flight or
	// finished from an earlier delivery — either way the caller should poll
	// the result key and must not resubmit as a new ref).
	AckAccepted AckStatus = "accepted"

	// AckBusy is transient back-pressure: no worker slot, the daily call cap
	// is reached, or the result bucket is unreachable. Nothing was written and
	// no vendor call was made; the caller should retry the same ref later.
	AckBusy AckStatus = "busy"

	// AckInvalid is terminal: the request could not be understood. Retrying it
	// unchanged will fail identically.
	AckInvalid AckStatus = "invalid"
)

// Errors a caller can branch on. AckBusy is retryable, AckInvalid is not — the
// distinction decides whether a dispatcher Naks with delay or records a
// terminal failure.
var (
	ErrBusy    = errors.New("modelrunner: runner busy (no capacity or daily cap reached)")
	ErrInvalid = errors.New("modelrunner: request rejected as invalid")
)

// Ack is the reply payload on the request subject.
type Ack struct {
	Status AckStatus `json:"status"`
	Ref    string    `json:"ref,omitempty"`
	// Reason is a short operator-facing explanation for a busy/invalid ack.
	// It is produced before any vendor call, so it can never carry vendor
	// error text or credentials.
	Reason string `json:"reason,omitempty"`
}

// Err maps a non-accepted status onto a sentinel error, so callers can use
// errors.Is instead of comparing strings. Accepted returns nil.
func (a Ack) Err() error {
	switch a.Status {
	case AckAccepted:
		return nil
	case AckBusy:
		return ErrBusy
	case AckInvalid:
		return ErrInvalid
	default:
		return errors.New("modelrunner: unknown ack status " + string(a.Status))
	}
}

// ToolSchema is the JSON-Schema body of the single tool the model must answer
// through. The runner supplies `"type": "object"` and forces
// `"additionalProperties": false` itself, so a caller declares only the shape
// it wants: strictness is the runner's guarantee, not the caller's option.
type ToolSchema struct {
	Properties map[string]any `json:"properties"`
	Required   []string       `json:"required,omitempty"`
}

// Tool is the one tool a request declares. Its input schema IS the caller's
// output contract: the model is forced to call it, and its input JSON is what
// comes back as Result.Output.
type Tool struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema ToolSchema `json:"inputSchema"`
}

// Request is one model call.
type Request struct {
	// Ref is the caller-chosen idempotency key. It is the result bucket's key,
	// so it must be a valid KV key (see ValidRef); two requests carrying the
	// same Ref cost exactly one vendor call.
	Ref string `json:"ref"`
	// Model is the vendor model id. Empty selects the runner's default.
	Model string `json:"model,omitempty"`
	// MaxTokens is the per-call output ceiling. Zero, or anything above the
	// runner's own ceiling, is clamped by the runner.
	MaxTokens int64 `json:"maxTokens,omitempty"`
	// System is the system prompt. Optional.
	System string `json:"system,omitempty"`
	// Prompt is the user turn. Required.
	Prompt string `json:"prompt"`
	// Tool is the single forced tool. Required.
	Tool Tool `json:"tool"`
}

// ResultState is the lifecycle of a result row.
type ResultState string

const (
	// StateInflight is the CAS-created marker written before the vendor call.
	// Its presence is the double-spend guard: a second request for the same
	// ref finds it and returns without spending.
	StateInflight ResultState = "inflight"
	// StateCompleted carries Output — the tool input JSON, verbatim.
	StateCompleted ResultState = "completed"
	// StateRefused means the vendor declined on policy grounds. Terminal, and
	// distinct from a failure: nothing about the request will make it work.
	StateRefused ResultState = "refused"
	// StateFailed is any other terminal outcome (transport, timeout, a
	// response carrying no tool call). Error says which.
	StateFailed ResultState = "failed"
)

// Usage is the vendor's token accounting for the call, recorded so spend is
// attributable per ref rather than only in aggregate.
type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// Result is the value at ResultsBucket/<ref>. The runner is its only writer.
type Result struct {
	State ResultState `json:"state"`
	Ref   string      `json:"ref"`
	// Output is the forced tool's input JSON, passed through untouched. Set
	// only on StateCompleted.
	Output json.RawMessage `json:"output,omitempty"`
	// Model is the model id the vendor reports having answered with — the
	// provenance record, not the id that was asked for.
	Model string `json:"model,omitempty"`
	Usage Usage  `json:"usage"`
	// Error is a redacted operator-facing message on StateFailed.
	Error string `json:"error,omitempty"`
	// RefusalCategory is the vendor's policy category on StateRefused.
	RefusalCategory string `json:"refusalCategory,omitempty"`
	StartedAt       string `json:"startedAt,omitempty"`
	CompletedAt     string `json:"completedAt,omitempty"`
}

// Terminal reports whether the result will never change again.
func (r Result) Terminal() bool {
	switch r.State {
	case StateCompleted, StateRefused, StateFailed:
		return true
	default:
		return false
	}
}

// refPattern is nats.go's KV key charset. A ref that fails it would ack
// "accepted" and then fail unwritably at the KV layer, stranding the caller on
// a key that never appears — so the runner rejects it up front instead.
var refPattern = regexp.MustCompile(`^[-/=\.a-zA-Z0-9]+$`)

// maxRefLen bounds a ref to a subject-token-sized identifier. NanoIDs and
// composed `<id>.<attempt>` chains sit far below it.
const maxRefLen = 128

// ValidRef reports whether ref is usable as a result-bucket key.
//
// The underscore is excluded even though NATS permits it: the runner's own
// bookkeeping keys live under a leading "__" in the same bucket, and a ref
// able to reach them could overwrite the daily-spend counter. Excluding the
// character outright is a smaller rule than a prefix ban and leaves no
// encoding trick — a dotted or dashed id is unaffected.
func ValidRef(ref string) bool {
	if ref == "" || len(ref) > maxRefLen {
		return false
	}
	if strings.HasPrefix(ref, ".") || strings.HasSuffix(ref, ".") {
		return false
	}
	return refPattern.MatchString(ref)
}
