package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// RebuildEventListFromClasses constructs a minimal EventList from the class
// names and mutation keys stored in the idempotency tracker. Used on the
// dedup-hit re-publish path (P1-002) when the original EventList is
// unavailable. Each event receives a fresh NanoID (the original IDs are not
// stored in the tracker), so this is a best-effort re-publish rather than
// an exact replay.
func RebuildEventListFromClasses(requestID string, classes []string, mutationKeys []string, at time.Time) (EventList, error) {
	stamp := substrate.FormatTimestamp(at)
	out := make(EventList, 0, len(classes))
	for i, class := range classes {
		if class == "" {
			continue
		}
		id, err := substrate.NewNanoID()
		if err != nil {
			return nil, fmt.Errorf("rebuild event list: event %d: NanoID: %w", i, err)
		}
		target := ""
		if i < len(mutationKeys) {
			target = mutationKeys[i]
		}
		out = append(out, Event{
			EventID:   id,
			RequestID: requestID,
			EventType: class,
			TargetKey: target,
			Payload:   map[string]interface{}{},
			Timestamp: stamp,
		})
	}
	return out, nil
}

// PublicationError is the typed step-9 failure surfaced when the batch
// publish to `core-events` fails after all retries. Step 8 has already
// committed durably. The commit path naks and JetStream redelivers; the
// dedup short-circuit at step 2 detects the committed tracker and attempts
// a best-effort re-publish before acking (Contract #4 §4.4 + NFR-R1).
type PublicationError struct {
	EventClass string
	Subject    string
	Attempts   int
	LastErr    error
}

func (e *PublicationError) Error() string {
	return fmt.Sprintf("PublicationError: class=%s subject=%s attempts=%d: %v",
		e.EventClass, e.Subject, e.Attempts, e.LastErr)
}

func (e *PublicationError) Unwrap() error { return e.LastErr }

// EventPublisherImpl is the step-9 implementation. It batch-publishes every
// event in the pre-built EventList to `core-events` in order. Behavior:
//
//  1. Skip step entirely if the EventList is empty (no events to publish).
//  2. Build one PublishOp per event with subject `events.<class>` and
//     a JSON-serialized Event body (see step7_events.go for shape).
//  3. Submit via Conn.PublishBatch — either all events land or none do.
//  4. Retry up to MaxRetries with exponential backoff (50ms, 200ms, 800ms)
//     on transient failures. Surface PublicationError after final attempt.
//
// Per Architecture Decision #3: cross-stream atomicity is not required
// here — events go to `core-events` while mutations went to Core KV.
// `substrate.PublishBatch` gives single-stream atomicity for the event
// fan-out (NATS atomic batch with sequential `Nats-Batch-Sequence`
// headers), so "partial event publication is not possible" per the AC.
type EventPublisherImpl struct {
	Conn       *substrate.Conn
	Logger     *slog.Logger
	Clock      func() time.Time
	MaxRetries int
	Timeout    time.Duration
	// BackoffSchedule is the slice of sleep durations between attempts;
	// must have length >= MaxRetries. Tests can override with zeros to
	// run synchronously.
	BackoffSchedule []time.Duration
}

// NewEventPublisher constructs the real EventPublisher.
func NewEventPublisher(conn *substrate.Conn, logger *slog.Logger) *EventPublisherImpl {
	if conn == nil {
		panic("processor: NewEventPublisher requires Conn")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &EventPublisherImpl{
		Conn:            conn,
		Logger:          logger,
		Clock:           time.Now,
		MaxRetries:      3,
		Timeout:         5 * time.Second,
		BackoffSchedule: []time.Duration{50 * time.Millisecond, 200 * time.Millisecond, 800 * time.Millisecond},
	}
}

// Publish implements EventPublisher (step 9). It batch-publishes the
// pre-built EventList to `core-events`. The EventList is built exactly once
// at step 8 and passed here so event IDs are identical to those recorded in
// the idempotency tracker.
func (p *EventPublisherImpl) Publish(ctx context.Context, env *OperationEnvelope, events EventList) error {
	if len(events) == 0 {
		p.Logger.Info("step 9: no events to publish", "requestId", env.RequestID)
		return nil
	}

	ops := make([]substrate.PublishOp, 0, len(events))
	for _, ev := range events {
		body, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("step 9: marshal event %s: %w", ev.EventID, err)
		}
		ops = append(ops, substrate.PublishOp{
			Subject: EventSubject(ev.EventType),
			Data:    body,
			Header: map[string]string{
				"X-Lattice-RequestId": env.RequestID,
				"X-Lattice-EventId":   ev.EventID,
				"X-Lattice-EventType": ev.EventType,
			},
		})
	}

	attempt := 0
	var lastErr error
	for attempt < p.MaxRetries {
		ack, perr := p.Conn.PublishBatch(ops, p.Timeout)
		if perr == nil {
			p.Logger.Info("step 9: events published",
				"requestId", env.RequestID,
				"count", ack.Count,
				"stream", ack.Stream,
				"seq", ack.Sequence,
				"batchID", ack.BatchID,
				"classes", events.EventClasses())
			return nil
		}
		lastErr = perr
		p.Logger.Warn("step 9: batch publish failed; retrying",
			"requestId", env.RequestID, "attempt", attempt+1, "error", perr)
		attempt++
		// Break immediately after the final attempt — no sleep on the last
		// failure since no further retry will follow.
		if attempt >= p.MaxRetries {
			break
		}
		// Honor context cancellation between attempts.
		if ctx.Err() != nil {
			break
		}
		if attempt-1 < len(p.BackoffSchedule) {
			select {
			case <-ctx.Done():
				lastErr = ctx.Err()
			case <-time.After(p.BackoffSchedule[attempt-1]):
			}
		}
	}

	firstClass := ""
	firstSubject := ""
	if len(events) > 0 {
		firstClass = events[0].EventType
		firstSubject = EventSubject(events[0].EventType)
	}
	return &PublicationError{
		EventClass: firstClass,
		Subject:    firstSubject,
		Attempts:   attempt,
		LastErr:    lastErr,
	}
}

// EventSubject derives the JetStream subject for an event class.
// Architecture Decision #2: `events.<class>` where class is sanitized
// to replace non-subject-token chars (whitespace, `>`, `*`, `.` at
// boundaries) with `_`. DDL class names already conform; this is a
// belt-and-braces guard so a typo cannot inject wildcard routing.
func EventSubject(class string) string {
	if class == "" {
		return "events._unknown"
	}
	// Replace dangerous tokens. Keep dots — class names like
	// `identity.created` legally segment the subject (matches `events.>`).
	safe := class
	for _, bad := range []string{" ", "\t", "\n", ">", "*"} {
		safe = strings.ReplaceAll(safe, bad, "_")
	}
	return "events." + safe
}

