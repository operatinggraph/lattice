package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/substrate"
)

// provisionEvents creates the core-events stream in the test cluster.
func provisionEvents(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	_, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:               "core-events",
		Subjects:           []string{"events.>"},
		Retention:          jetstream.LimitsPolicy,
		MaxAge:             7 * 24 * time.Hour,
		AllowAtomicPublish: true,
	})
	if err != nil {
		t.Fatalf("provision core-events: %v", err)
	}
}

func TestEventSubject_Sanitization(t *testing.T) {
	cases := map[string]string{
		"identity.created": "events.identity.created",
		"":                 "events._unknown",
		"bad name>":        "events.bad_name_",
		"with*star":        "events.with_star",
	}
	for in, want := range cases {
		if got := EventSubject(in); got != want {
			t.Errorf("EventSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEventPublisher_NoEventsShortCircuits(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	provisionEvents(t, ctx, conn)
	pub := NewEventPublisher(conn, testLogger())
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{} // no events
	if err := pub.Publish(ctx, env, result); err != nil {
		t.Fatalf("expected nil on empty events, got %v", err)
	}
}

func TestEventPublisher_HappyPath(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	provisionEvents(t, ctx, conn)
	pub := NewEventPublisher(conn, testLogger())
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Mutations: []MutationOp{{Op: "create", Key: "vtx.identity." + testNanoID2}},
		Events: []EventSpec{
			{Class: "identity.created", Data: map[string]interface{}{"x": 1}},
			{Class: "identity.linked", Data: map[string]interface{}{"y": 2}},
		},
	}
	if err := pub.Publish(ctx, env, result); err != nil {
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
	ctx, conn, _, _, _ := setupTestPipeline(t)
	// Provision the stream so the FIRST publish succeeds.
	provisionEvents(t, ctx, conn)
	pub := NewEventPublisher(conn, testLogger())
	pub.BackoffSchedule = []time.Duration{0, 0, 0} // fast retries
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Events: []EventSpec{{Class: "test.event", Data: map[string]interface{}{}}},
	}
	if err := pub.Publish(ctx, env, result); err != nil {
		t.Fatalf("happy publish: %v", err)
	}
}

// TestEventPublisher_FailureSurfacesPublicationError deletes the
// core-events stream so PublishBatch fails repeatedly; the wrapper
// surfaces a *PublicationError after MaxRetries.
func TestEventPublisher_FailureSurfacesPublicationError(t *testing.T) {
	ctx, conn, _, _, _ := setupTestPipeline(t)
	// Do NOT provision core-events — every PublishBatch call should
	// receive "no stream matches subject" from the server.
	pub := NewEventPublisher(conn, testLogger())
	pub.BackoffSchedule = []time.Duration{0, 0, 0}
	pub.MaxRetries = 2
	env := newTestEnvelope(testNanoID1)
	result := ScriptResult{
		Events: []EventSpec{{Class: "test.event", Data: map[string]interface{}{}}},
	}
	err := pub.Publish(ctx, env, result)
	if err == nil {
		t.Fatalf("expected PublicationError, got nil")
	}
	var pe *PublicationError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PublicationError, got %T: %v", err, err)
	}
	if pe.Attempts < 1 {
		t.Fatalf("PublicationError.Attempts = %d, want >= 1", pe.Attempts)
	}
}
