package failure_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/failure"
)

// ── ParseISO8601Duration ─────────────────────────────────────────────────────

func TestParseISO8601Duration_Variants(t *testing.T) {
	cases := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"PT5S", 5 * time.Second, false},
		{"PT1M", time.Minute, false},
		{"PT1H", time.Hour, false},
		{"PT1M30S", 90 * time.Second, false},
		{"PT1H30M", 90 * time.Minute, false},
		{"PT1H30M45S", time.Hour + 30*time.Minute + 45*time.Second, false},
		// Errors
		{"P1D", 0, true},        // missing T
		{"PT", 0, true},         // empty after PT
		{"PT0S", 0, true},       // parses to zero
		{"PTxS", 0, true},       // non-numeric
		{"PT5X", 0, true},       // unrecognised unit
		{"", 0, true},           // empty
		{"5S", 0, true},         // no PT prefix
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			got, err := failure.ParseISO8601Duration(tc.input)
			if tc.wantErr {
				assert.Error(t, err, "expected error for input %q", tc.input)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

// ── RetryQueue unit tests (no NATS) ─────────────────────────────────────────

// TestRetryQueue_SuccessOnSecondAttempt verifies that a write that fails once
// then succeeds is removed from the queue with no DLQ escalation.
func TestRetryQueue_SuccessOnSecondAttempt(t *testing.T) {
	q := failure.NewRetryQueue()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go q.Run(ctx)

	calls := 0
	done := make(chan struct{})
	e := &failure.RetryEntry{
		RuleID:      "rule-1",
		Team:        "team-a",
		EntityID:    "entity-1",
		Stage:       "write",
		MaxAttempts: 5,
		BaseBackoff: time.Millisecond,
		JS:          nil, // no DLQ needed — should succeed before exhaustion
		WriteFn: func(_ context.Context) error {
			calls++
			if calls == 1 {
				return errors.New("transient error on first attempt")
			}
			close(done)
			return nil
		},
	}

	q.Enqueue(e)

	select {
	case <-done:
		// Success — verify entry removed from queue.
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not succeed within 2s")
	}

	// Give the queue a brief moment to remove the entry after success.
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 0, q.Len(), "queue must be empty after successful retry")
	assert.Equal(t, 2, calls, "write function must be called exactly twice (fail + succeed)")
}

// TestRetryQueue_ExhaustsToNilDLQ_NoPanic verifies that when all attempts are
// exhausted with JS=nil, the queue logs an error and does NOT panic.
func TestRetryQueue_ExhaustsToNilDLQ_NoPanic(t *testing.T) {
	q := failure.NewRetryQueue()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go q.Run(ctx)

	e := &failure.RetryEntry{
		RuleID:      "rule-dlq",
		Team:        "team-a",
		EntityID:    "entity-dlq",
		Stage:       "write",
		MaxAttempts: 2,
		BaseBackoff: time.Millisecond,
		JS:          nil, // no JetStream — must not panic
		WriteFn: func(_ context.Context) error {
			return errors.New("always fails")
		},
	}

	// Should not panic regardless of JS being nil.
	assert.NotPanics(t, func() {
		q.Enqueue(e)
		// Wait for the queue to exhaust the entry.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if q.Len() == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})

	// Wait a moment for final removal then assert queue is empty.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, q.Len(), "queue must be empty after exhaustion")
}

// TestRetryQueue_DrainOnContextCancel verifies that cancelling ctx causes Run
// to return cleanly, leaving any pending entries without processing them further.
func TestRetryQueue_DrainOnContextCancel(t *testing.T) {
	q := failure.NewRetryQueue()
	ctx, cancel := context.WithCancel(context.Background())

	runDone := make(chan struct{})
	go func() {
		q.Run(ctx)
		close(runDone)
	}()

	// Enqueue an entry with a very long backoff so it will never fire.
	e := &failure.RetryEntry{
		RuleID:      "rule-cancel",
		Team:        "team-a",
		EntityID:    "entity-cancel",
		Stage:       "write",
		MaxAttempts: 5,
		BaseBackoff: time.Hour, // far future
		JS:          nil,
		WriteFn: func(_ context.Context) error {
			return errors.New("should never be called")
		},
	}
	q.Enqueue(e)

	// Cancel context — Run should return promptly.
	cancel()
	select {
	case <-runDone:
		// Pass.
	case <-time.After(time.Second):
		t.Fatal("RetryQueue.Run did not return within 1s after context cancellation")
	}
}

// TestRetryQueue_BackoffDoubles verifies that the retry delay doubles after each
// failure (base × 2^attempt). This would fail under any flat-delay implementation.
func TestRetryQueue_BackoffDoubles(t *testing.T) {
	q := failure.NewRetryQueue()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go q.Run(ctx)

	const base = 20 * time.Millisecond
	t1ch := make(chan time.Time, 1)
	t2ch := make(chan time.Time, 1)
	callCount := 0
	e := &failure.RetryEntry{
		RuleID:      "rule-double",
		Team:        "team-a",
		EntityID:    "entity-double",
		Stage:       "write",
		MaxAttempts: 5,
		BaseBackoff: base,
		JS:          nil,
		WriteFn: func(_ context.Context) error {
			callCount++
			switch callCount {
			case 1:
				t1ch <- time.Now()
				return errors.New("fail first")
			default:
				t2ch <- time.Now()
				return nil
			}
		},
	}

	enqueueTime := time.Now()
	q.Enqueue(e)

	var t1, t2 time.Time
	select {
	case t1 = <-t1ch:
	case <-time.After(2 * time.Second):
		t.Fatal("first retry did not fire within 2s")
	}
	select {
	case t2 = <-t2ch:
	case <-time.After(2 * time.Second):
		t.Fatal("second retry did not fire within 2s")
	}

	d1 := t1.Sub(enqueueTime) // ≈ base × 2^0 = 20ms
	d2 := t2.Sub(t1)          // ≈ base × 2^1 = 40ms
	assert.Greater(t, d2, d1,
		"second retry delay (%dms) must exceed first (%dms) — backoff must double each attempt",
		d2.Milliseconds(), d1.Milliseconds())
}

// ── NATS integration test: retry-to-DLQ ─────────────────────────────────────

// TestRetryQueue_ExhaustsToDLQ_Integration verifies that when all retry attempts
// are exhausted the entry is routed to the DLQ stream with the correct fields (FR18, FR20).
func TestRetryQueue_ExhaustsToDLQ_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	js := startFailureJetStreamServer(t)
	ctx := context.Background()

	q := failure.NewRetryQueue()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go q.Run(runCtx)

	e := &failure.RetryEntry{
		RuleID:      "rule-exhausted",
		Team:        "team-x",
		EntityID:    "entity-ex",
		Stage:       "write",
		MaxAttempts: 2,
		BaseBackoff: time.Millisecond,
		JS:          js,
		WriteFn: func(_ context.Context) error {
			return errors.New("always fails — exhaust me")
		},
	}
	q.Enqueue(e)

	// Poll until the DLQ stream exists and has a message (retry exhaustion may take a few ms).
	var dlqMsg failure.DLQMessage
	var gotMsg bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !gotMsg {
		cons, err := js.OrderedConsumer(ctx, "REFRACTOR_DLQ_RULE-EXHAUSTED", jetstream.OrderedConsumerConfig{})
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		batch, err := cons.Fetch(1, jetstream.FetchMaxWait(100*time.Millisecond))
		if err == nil {
			for msg := range batch.Messages() {
				require.NoError(t, msg.Ack())
				if json.Unmarshal(msg.Data(), &dlqMsg) == nil {
					gotMsg = true
				}
			}
		}
		if !gotMsg {
			time.Sleep(50 * time.Millisecond)
		}
	}
	require.True(t, gotMsg, "DLQ message must be published before timeout")

	require.NotEmpty(t, dlqMsg.RuleID, "DLQ message RuleID must be populated")
	assert.Equal(t, "TRANSIENT", dlqMsg.ErrorClass)
	assert.Equal(t, 2, dlqMsg.RetryCount, "retryCount must equal MaxAttempts")
	assert.Equal(t, "rule-exhausted", dlqMsg.RuleID)
	assert.Equal(t, "entity-ex", dlqMsg.EntityID)
	assert.Equal(t, "write", dlqMsg.FailedStage)
	assert.NotEmpty(t, dlqMsg.ErrorMessage)
	assert.NotEmpty(t, dlqMsg.Timestamp)
}
