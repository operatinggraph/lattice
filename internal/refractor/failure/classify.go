package failure

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/asolgan/lattice/internal/substrate"
)

// Category classifies an error into one of four routing tiers.
// All error classification in the Refractor routes through Classify — no other
// package should perform local error classification (architecture.md rule).
type Category uint8

const (
	// CatTransient errors are recoverable: Nak the message and let NATS redeliver.
	// Routed to an exponential-backoff retry queue (FR18).
	CatTransient Category = iota
	// CatTerminal errors indicate bad data that can never succeed: route to DLQ immediately.
	// Created by calling failure.Terminal(err) in adapters or the evaluator.
	CatTerminal
	// CatInfra errors indicate the target store is temporarily unavailable.
	// The pipeline pauses the fetch loop and probes for recovery (FR16, FR17).
	CatInfra
	// CatStructural errors indicate a permanent misconfiguration in the target store
	// (bucket/table missing, schema incompatible). The rule is paused immediately;
	// no DLQ entries accumulate (FR19a, NFR3).
	CatStructural
	// CatPrivacyCritical errors indicate a shredded identity's projected row could not
	// be nullified — a confidentiality-relevant failure (refractor-failure-tiers.md
	// "Privacy / security supersession tiers"). The rule is paused immediately, alerted,
	// and never auto-retried: a row that should have been scrubbed on shred must not be
	// silently left in place while the pipeline keeps running.
	CatPrivacyCritical
)

// ── Private wrapper types ─────────────────────────────────────────────────────
// Each wrapper carries the cause and is detected by Classify via errors.As.
// Explicit wrappers always take priority over heuristic raw-type detection.

type infraError struct{ cause error }

func (e *infraError) Error() string { return e.cause.Error() }
func (e *infraError) Unwrap() error { return e.cause }

type structuralError struct{ cause error }

func (e *structuralError) Error() string { return e.cause.Error() }
func (e *structuralError) Unwrap() error { return e.cause }

type terminalError struct{ cause error }

func (e *terminalError) Error() string { return e.cause.Error() }
func (e *terminalError) Unwrap() error { return e.cause }

type transientError struct{ cause error }

func (e *transientError) Error() string { return e.cause.Error() }
func (e *transientError) Unwrap() error { return e.cause }

type privacyCriticalError struct{ cause error }

func (e *privacyCriticalError) Error() string { return e.cause.Error() }
func (e *privacyCriticalError) Unwrap() error { return e.cause }

// ── Public constructors ───────────────────────────────────────────────────────
// Use these to explicitly tag an error with its failure category. Classify
// detects these wrappers before raw-type heuristics, so explicit tagging always wins.
// All constructors panic on nil — wrapping nil is always a caller bug.

// Infrastructure wraps err to signal an infrastructure-tier failure (target store
// temporarily unavailable). Classify routes it to the fetch-loop pause path.
// Panics if err is nil.
func Infrastructure(err error) error {
	if err == nil {
		panic("failure: Infrastructure called with nil error")
	}
	return &infraError{cause: err}
}

// Structural wraps err to signal a structural misconfiguration (missing table,
// schema mismatch). Classify routes it to the rule-pause path; no DLQ written.
// Panics if err is nil.
func Structural(err error) error {
	if err == nil {
		panic("failure: Structural called with nil error")
	}
	return &structuralError{cause: err}
}

// Terminal wraps err to signal permanently bad data for a specific entity.
// Classify routes it to immediate DLQ publish with no retry.
// Call this in adapters or the evaluator when the data cannot produce a valid row.
// Panics if err is nil.
func Terminal(err error) error {
	if err == nil {
		panic("failure: Terminal called with nil error")
	}
	return &terminalError{cause: err}
}

// Transient wraps err to explicitly tag it as a recoverable transient failure.
// Usually unnecessary — Classify defaults to CatTransient for unrecognised errors.
// Use when you need to force transient routing for an error that might otherwise
// be misclassified (e.g., a wrapped infrastructure error you want retried).
// Panics if err is nil.
func Transient(err error) error {
	if err == nil {
		panic("failure: Transient called with nil error")
	}
	return &transientError{cause: err}
}

// PrivacyCritical wraps err to signal a shredded identity's projected row could not be
// nullified. Classify routes it to the rule-pause path (no DLQ, no retry) — halting the
// affected lens is the correct response to a possible confidentiality breach, not
// something an automatic retry can fix. Panics if err is nil.
func PrivacyCritical(err error) error {
	if err == nil {
		panic("failure: PrivacyCritical called with nil error")
	}
	return &privacyCriticalError{cause: err}
}

// ── Classify ──────────────────────────────────────────────────────────────────

// Classify returns the failure category for err.
// Explicit wrappers (Infrastructure, Structural, Terminal, Transient) take priority
// over raw-type heuristics. Panics if err is nil.
func Classify(err error) Category {
	if err == nil {
		panic("failure: Classify called with nil error")
	}

	// 1. Explicit wrapper detection — always wins over heuristics.
	if errors.As(err, new(*infraError)) {
		return CatInfra
	}
	if errors.As(err, new(*structuralError)) {
		return CatStructural
	}
	if errors.As(err, new(*terminalError)) {
		return CatTerminal
	}
	if errors.As(err, new(*transientError)) {
		return CatTransient
	}
	if errors.As(err, new(*privacyCriticalError)) {
		return CatPrivacyCritical
	}

	// 2. Infrastructure: NATS transport-level failures (server unreachable, connection lost).
	if substrate.IsConnectionError(err) {
		return CatInfra
	}

	// 3. Structural: target store configuration errors (bucket or stream absent).
	if errors.Is(err, substrate.ErrBucketNotFound) {
		return CatStructural
	}

	// 4. Structural: Postgres schema contract violations.
	// These represent permanent misconfigurations that cannot be resolved by retrying
	// the same message — the rule must be paused until the schema is corrected (FR37, FR38).
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42P01", // undefined_table — relation does not exist (FR37)
			"23502", // not_null_violation — NULL into NOT NULL column (FR38)
			"42804", // datatype_mismatch — column type incompatible with value (FR38)
			"22P02": // invalid_text_representation — e.g. non-numeric string → INTEGER (FR38)
			return CatStructural
		}
	}

	// 5. Default: transient — redeliver via Nak.
	return CatTransient
}
