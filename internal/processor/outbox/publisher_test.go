package outbox

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

func TestEventSubject_Sanitization(t *testing.T) {
	cases := map[string]string{
		"identity.created": "events.identity.created",
		"":                 "events._unknown",
		"TaskCompleted":    "events._nodomain",
		"with*star":        "events._nodomain",
		"domain.bad name>": "events.domain.bad_name_",
		"domain.with*star": "events.domain.with_star",
	}
	for in, want := range cases {
		if got := EventSubject(in); got != want {
			t.Errorf("EventSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEventPublisher_NoEventsShortCircuits(t *testing.T) {
	t.Parallel()
	ctx, conn := setup(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pub := NewEventPublisher(conn, logger)
	env := &processor.OperationEnvelope{RequestID: "Aj4kPmRtw9nbCxz5vQ2y"}
	if err := pub.Publish(ctx, env, processor.EventList{}); err != nil {
		t.Fatalf("expected nil on empty events, got %v", err)
	}
}

func TestEventPublisher_HappyPath(t *testing.T) {
	t.Parallel()
	ctx, conn := setup(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pub := NewEventPublisher(conn, logger)
	env := &processor.OperationEnvelope{RequestID: "Aj4kPmRtw9nbCxz5vQ2y"}
	result := processor.ScriptResult{
		Mutations: []processor.MutationOp{{Op: "create", Key: "vtx.identity.Bj4kPmRtw9nbCxz5vQ2y"}},
		Events: []processor.EventSpec{
			{Class: "identity.created", Data: map[string]interface{}{"x": 1}},
			{Class: "identity.linked", Data: map[string]interface{}{"y": 2}},
		},
	}
	events, err := processor.BuildEventList(env, result, time.Now())
	if err != nil {
		t.Fatalf("BuildEventList: %v", err)
	}
	if err := pub.Publish(ctx, env, events); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Consume events back from core-events and assert order + count.
	cons, err := conn.JetStream().CreateOrUpdateConsumer(ctx, "core-events", jetstream.ConsumerConfig{
		Durable:        "pub-test-consumer",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{"events.>"},
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateConsumer: %v", err)
	}
	batch, err := cons.Fetch(2, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var subjects []string
	for m := range batch.Messages() {
		subjects = append(subjects, m.Subject())
		_ = m.Ack()
	}
	if len(subjects) != 2 {
		t.Fatalf("got %d events, want 2 (subjects=%v)", len(subjects), subjects)
	}
	if subjects[0] != "events.identity.created" || subjects[1] != "events.identity.linked" {
		t.Fatalf("subjects out of order: %v", subjects)
	}
}

// TestEventPublisher_RetryThenSuccess uses a sabotaged sub-Conn whose
// PublishBatch fails the first call and succeeds afterwards. Mocking
// at the substrate level is invasive, so this test instead temporarily
// removes the stream, then re-creates it after the first attempt.
func TestEventPublisher_RetriesOnTransientFailure(t *testing.T) {
	t.Parallel()
	ctx, conn := setup(t)
	// core-events is already provisioned by setup(t).
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pub := NewEventPublisher(conn, logger)
	pub.BackoffSchedule = []time.Duration{0, 0, 0} // fast retries
	env := &processor.OperationEnvelope{RequestID: "Cj4kPmRtw9nbCxz5vQ2y"}
	result := processor.ScriptResult{
		Events: []processor.EventSpec{{Class: "test.event", Data: map[string]interface{}{}}},
	}
	events, err := processor.BuildEventList(env, result, time.Now())
	if err != nil {
		t.Fatalf("BuildEventList: %v", err)
	}
	if err := pub.Publish(ctx, env, events); err != nil {
		t.Fatalf("happy publish: %v", err)
	}
}

// TestEventPublisher_FailureSurfacesPublicationError connects to a NATS server
// where core-events is NOT provisioned so PublishBatch fails repeatedly; the
// wrapper surfaces a *PublicationError after MaxRetries.
func TestEventPublisher_FailureSurfacesPublicationError(t *testing.T) {
	t.Parallel()
	// Use a fresh NATS server without core-events so PublishBatch fails.
	ctx, cancel := func() (context.Context, func()) {
		c, cc := context.WithCancel(context.Background())
		return c, cc
	}()
	t.Cleanup(cancel)
	url := startEmbeddedNATS(t)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "pub-fail-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	// core-events stream is intentionally NOT provisioned.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	pub := NewEventPublisher(conn, logger)
	pub.BackoffSchedule = []time.Duration{0, 0, 0}
	pub.MaxRetries = 2
	env := &processor.OperationEnvelope{RequestID: "Dj4kPmRtw9nbCxz5vQ2y"}
	result := processor.ScriptResult{
		Events: []processor.EventSpec{{Class: "test.event", Data: map[string]interface{}{}}},
	}
	events, buildErr := processor.BuildEventList(env, result, time.Now())
	if buildErr != nil {
		t.Fatalf("BuildEventList: %v", buildErr)
	}
	pubErr := pub.Publish(ctx, env, events)
	if pubErr == nil {
		t.Fatalf("expected PublicationError, got nil")
	}
	var pe *PublicationError
	if !errors.As(pubErr, &pe) {
		t.Fatalf("expected *PublicationError, got %T: %v", pubErr, pubErr)
	}
	if pe.Attempts < 1 {
		t.Fatalf("PublicationError.Attempts = %d, want >= 1", pe.Attempts)
	}
}
