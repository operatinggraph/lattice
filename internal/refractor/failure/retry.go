package failure

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// RetryEntry holds all data needed to retry a write or escalate to the DLQ on exhaustion.
// WriteFn is a closure that captures the adapter and write arguments at enqueue time,
// keeping the failure package free of adapter imports.
type RetryEntry struct {
	// Identification
	RuleID     string
	EntityID   string
	Stage      string // "write"
	RawPayload []byte // original NATS message body stored for DLQ diagnostic context

	// Write closure — adapter + write arguments captured at enqueue time.
	// Returns nil on success, a non-nil error on failure.
	WriteFn func(ctx context.Context) error

	// Retry configuration
	Attempt     int           // write attempts executed so far (0 before the first retry)
	MaxAttempts int           // from rule.RetryConfig.MaxAttempts
	BaseBackoff time.Duration // parsed from rule.RetryConfig.Backoff ISO 8601 string

	// Scheduled execution time (set by RetryQueue.Enqueue and updated on reschedule)
	NextAt time.Time

	// Conn is the substrate connection for DLQ publish on exhaustion — may be nil
	// (logs an error instead of publishing).
	Conn *substrate.Conn

	// RuleSequence is the active rule version sequence stamp included in the DLQ message.
	// Set at enqueue time from reporter.ActiveSequence() so the correct version is recorded
	// even if the rule is updated before retry exhaustion.
	RuleSequence string

	// OnDLQPublished is an optional callback invoked after a successful DLQ publish
	// on retry exhaustion. Used by pipeline to update the health KV error count (Story 4.1).
	// Called with context.WithoutCancel(ctx) — safe to call even at shutdown.
	// Nil means skip.
	OnDLQPublished func(ctx context.Context, errMsg string)
}

// RetryQueue is a deferred exponential-backoff retry queue for Transient write failures.
// Call Run in a goroutine before calling Enqueue.
//
// Single-caller invariant: Run MUST be called from exactly one goroutine. Multiple
// concurrent calls to Run would race on processDue execution — entries could be
// executed more than once and the due-list collected under the lock could be stale
// by the time it is executed. The running flag below enforces this at runtime.
type RetryQueue struct {
	mu      sync.Mutex
	entries []*RetryEntry
	trigger chan struct{} // buffered(1); wakes Run when a new entry is added
	running bool          // guards against multiple concurrent Run callers
}

// NewRetryQueue returns a ready-to-use RetryQueue. Start Run in a goroutine before Enqueue.
func NewRetryQueue() *RetryQueue {
	return &RetryQueue{trigger: make(chan struct{}, 1)}
}

// Len returns the number of entries currently in the queue.
func (q *RetryQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries)
}

// Enqueue adds e to the queue and schedules its first retry.
// The retry delay is: BaseBackoff × 2^Attempt.
func (q *RetryQueue) Enqueue(e *RetryEntry) {
	shift := e.Attempt
	if shift < 0 {
		shift = 0
	}
	if shift > 30 { // cap at 2^30 (~1 billion × base) to prevent int overflow
		shift = 30
	}
	delay := e.BaseBackoff * (1 << uint(shift))
	e.NextAt = time.Now().Add(delay)

	q.mu.Lock()
	q.entries = append(q.entries, e)
	q.mu.Unlock()

	// Wake the run loop; non-blocking if already awake.
	select {
	case q.trigger <- struct{}{}:
	default:
	}
}

// Run processes retry entries until ctx is cancelled. Run in a dedicated goroutine.
// Panics if called concurrently from more than one goroutine (single-caller invariant).
func (q *RetryQueue) Run(ctx context.Context) {
	q.mu.Lock()
	if q.running {
		q.mu.Unlock()
		panic("failure.RetryQueue: Run called from more than one goroutine")
	}
	q.running = true
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.running = false
		q.mu.Unlock()
	}()

	for {
		delay := q.nextDelay()

		if delay < 0 {
			// No entries — park until a new entry arrives or ctx is cancelled.
			select {
			case <-ctx.Done():
				return
			case <-q.trigger:
				// New entry added; re-evaluate.
			}
			continue
		}

		if delay == 0 {
			q.processDue(ctx)
			continue
		}

		// Wait until the earliest entry is due, a new (potentially earlier) entry arrives,
		// or ctx is cancelled. Use NewTimer so the timer can be stopped when the trigger
		// fires first, preventing goroutine leaks from abandoned time.After channels.
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			q.processDue(ctx)
		case <-q.trigger:
			timer.Stop()
			// New entry may be scheduled earlier than current timer — re-evaluate.
		}
	}
}

// nextDelay returns the duration until the earliest due entry, 0 if already due, or -1 if empty.
func (q *RetryQueue) nextDelay() time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.entries) == 0 {
		return -1
	}
	earliest := q.entries[0].NextAt
	for _, e := range q.entries[1:] {
		if e.NextAt.Before(earliest) {
			earliest = e.NextAt
		}
	}
	d := time.Until(earliest)
	if d < 0 {
		return 0
	}
	return d
}

// processDue executes all entries whose NextAt is now or past.
func (q *RetryQueue) processDue(ctx context.Context) {
	q.mu.Lock()
	now := time.Now()
	var due []*RetryEntry
	for _, e := range q.entries {
		if !e.NextAt.After(now) {
			due = append(due, e)
		}
	}
	q.mu.Unlock()

	for _, e := range due {
		if err := e.WriteFn(ctx); err == nil {
			// Success — remove from queue, no DLQ entry needed.
			q.remove(e)
			slog.Info("failure: retry succeeded",
				"ruleId", e.RuleID, "entityId", e.EntityID, "attempt", e.Attempt+1)
			continue
		} else {
			e.Attempt++
			if e.MaxAttempts > 0 && e.Attempt >= e.MaxAttempts {
				q.remove(e)
				q.escalateToDLQ(ctx, e, err)
				continue
			}
			// Reschedule with doubled backoff.
			reschedShift := e.Attempt
			if reschedShift > 30 {
				reschedShift = 30
			}
			e.NextAt = time.Now().Add(e.BaseBackoff * (1 << uint(reschedShift)))
			slog.Warn("failure: retry failed, rescheduled",
				"ruleId", e.RuleID, "entityId", e.EntityID,
				"attempt", e.Attempt, "nextRetry", e.NextAt)
		}
	}
}

// remove deletes e from the entries slice.
func (q *RetryQueue) remove(e *RetryEntry) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, entry := range q.entries {
		if entry == e {
			q.entries = append(q.entries[:i], q.entries[i+1:]...)
			return
		}
	}
}

// escalateToDLQ publishes a DLQ message after all retry attempts are exhausted.
func (q *RetryQueue) escalateToDLQ(ctx context.Context, e *RetryEntry, lastErr error) {
	slog.Error("failure: retry exhausted, routing to DLQ",
		"ruleId", e.RuleID, "entityId", e.EntityID, "attempts", e.Attempt, "err", lastErr)
	if e.Conn == nil {
		slog.Error("failure: no connection configured for DLQ publish — exhausted entry dropped",
			"ruleId", e.RuleID, "entityId", e.EntityID)
		return
	}
	msg := DLQMessage{
		RuleID:       e.RuleID,
		EntityID:     e.EntityID,
		FailedStage:  e.Stage,
		ErrorClass:   "TRANSIENT",
		ErrorMessage: lastErr.Error(),
		RetryCount:   e.Attempt,
		RuleSequence: e.RuleSequence, // set at enqueue time from reporter.ActiveSequence()
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		RawPayload:   string(e.RawPayload),
	}
	// Use WithoutCancel so a DLQ publish triggered at shutdown (when ctx may already
	// be cancelled) still completes rather than being silently discarded.
	pubCtx := context.WithoutCancel(ctx)
	if err := Publish(pubCtx, e.Conn, e.RuleID, msg); err != nil {
		slog.Error("failure: DLQ publish failed after retry exhaustion",
			"ruleId", e.RuleID, "entityId", e.EntityID, "err", err)
	} else if e.OnDLQPublished != nil {
		e.OnDLQPublished(pubCtx, lastErr.Error())
	}
}

// ParseISO8601Duration parses an ISO 8601 duration string of the form PT[nH][nM][nS].
// At least one of H, M, or S must be present and produce a non-zero total.
// Examples: "PT5S", "PT1M", "PT1H", "PT1M30S", "PT1H30M", "PT1H30M45S".
func ParseISO8601Duration(s string) (time.Duration, error) {
	if !strings.HasPrefix(s, "PT") {
		return 0, fmt.Errorf("failure: ISO 8601 duration must start with PT, got %q", s)
	}
	rest := s[2:] // strip leading "PT"
	if rest == "" {
		return 0, fmt.Errorf("failure: ISO 8601 duration is empty after PT in %q", s)
	}

	var total time.Duration
	for _, u := range []struct {
		suffix string
		mult   time.Duration
	}{
		{"H", time.Hour},
		{"M", time.Minute},
		{"S", time.Second},
	} {
		idx := strings.Index(rest, u.suffix)
		if idx < 0 {
			continue
		}
		if idx == 0 {
			return 0, fmt.Errorf("failure: missing number before %q in ISO 8601 duration %q", u.suffix, s)
		}
		n, err := strconv.Atoi(rest[:idx])
		if err != nil || n < 0 {
			return 0, fmt.Errorf("failure: invalid number %q before %q in ISO 8601 duration %q", rest[:idx], u.suffix, s)
		}
		total += time.Duration(n) * u.mult
		rest = rest[idx+1:]
	}

	if rest != "" {
		return 0, fmt.Errorf("failure: unrecognised trailing characters %q in ISO 8601 duration %q", rest, s)
	}
	if total == 0 {
		return 0, fmt.Errorf("failure: ISO 8601 duration %q parses to zero", s)
	}
	return total, nil
}
