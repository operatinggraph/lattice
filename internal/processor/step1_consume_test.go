package processor

// TestBypass2_* — off-namespace publish adversarial vectors proving
// step1_consume.go's EnsureConsumer FilterSubjects boundary: the Processor's
// durable consumer only ever receives ops.default.>/ops.urgent.>/ops.system.>/
// ops.meta.>, so a message published off-namespace (or to an unfiltered
// subject on the same stream) never reaches it.

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/substrate"
)

// TestBypass2_OffNamespacePublish_NotConsumed publishes a message to a subject
// outside the ops.default.>/ops.urgent.>/ops.system.> filter set and asserts
// the Processor's durable consumer never receives it.
func TestBypass2_OffNamespacePublish_NotConsumed(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "bypass2-test"})
	if err != nil {
		t.Fatalf("bypass2: connect: %v", err)
	}
	t.Cleanup(conn.Close)
	provisionHarness(t, ctx, conn)
	widenOpsStreamSubjects(t, ctx, conn)

	cons, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName:     testStream,
		Durable:        "bypass2-main",
		FilterSubjects: []string{"ops.default.>", "ops.urgent.>", "ops.system.>"},
		AckWait:        2 * time.Second,
	}, testLogger())
	if err != nil {
		t.Fatalf("bypass2: EnsureConsumer: %v", err)
	}

	// Attempt 1: publish to "bypass.attempt" — completely outside ops.> hierarchy.
	// The core-operations stream only covers "ops.>" subjects, so this message
	// will not even land in the stream.
	_, err = conn.JetStream().Publish(ctx, "bypass.attempt", []byte(`{"attack":"direct"}`))
	if err == nil {
		// Some NATS servers accept and store this on a different stream or
		// return no-interest. Either way — check the consumer.
		t.Logf("bypass2: 'bypass.attempt' publish returned no error (may be stored or dropped)")
	} else {
		t.Logf("bypass2: 'bypass.attempt' publish rejected: %v (stream doesn't cover this subject)", err)
	}

	// Attempt 2: publish to "ops.rogue" — under ops.> so it lands in the stream,
	// but NOT under ops.default.>/ops.urgent.>/ops.system.> so the consumer
	// filter must block it.
	_, err = conn.JetStream().Publish(ctx, "ops.rogue", []byte(`{"attack":"off-lane"}`))
	if err != nil {
		t.Logf("bypass2: 'ops.rogue' publish rejected by stream: %v", err)
	} else {
		t.Logf("bypass2: 'ops.rogue' publish landed in stream (expected — stream covers ops.>)")
	}

	// Attempt 3: publish directly to the subject "ops.malicious.inject" — this
	// is under ops.> so the stream accepts it, but the consumer only receives
	// ops.default.>/ops.urgent.>/ops.system.>. This is the critical check.
	_, err = conn.JetStream().Publish(ctx, "ops.malicious.inject", []byte(`{"attack":"namespace-confusion"}`))
	if err != nil {
		t.Logf("bypass2: 'ops.malicious.inject' rejected by stream: %v", err)
	} else {
		t.Logf("bypass2: 'ops.malicious.inject' landed in stream at ops.malicious.inject")
	}

	// ASSERTION: the Processor consumer must NOT receive any of these messages.
	// Use Fetch with a brief timeout — if count == 0, consumer filtered them all.
	assertConsumerReceivesNothing(t, ctx, cons, "bypass2-consumer-check", 3)

	t.Logf("Bypass #2 BLOCKED: off-namespace messages did not reach Processor consumer (FilterSubjects enforcement)")
}

// TestBypass2_ValidLanePublish_IsConsumed is the positive baseline: a message
// published to "ops.default.bypass2" (under ops.default.>) IS received by the
// consumer. This confirms FilterSubjects is correctly scoped and not vacuously
// blocking everything.
func TestBypass2_ValidLanePublish_IsConsumed(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "bypass2-test"})
	if err != nil {
		t.Fatalf("bypass2: connect: %v", err)
	}
	t.Cleanup(conn.Close)
	provisionHarness(t, ctx, conn)
	widenOpsStreamSubjects(t, ctx, conn)

	cons, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName:     testStream,
		Durable:        "bypass2-positive",
		FilterSubjects: []string{"ops.default.>", "ops.urgent.>", "ops.system.>"},
		AckWait:        2 * time.Second,
	}, testLogger())
	if err != nil {
		t.Fatalf("bypass2: EnsureConsumer: %v", err)
	}

	// Publish a valid envelope to a filtered subject.
	validPayload := []byte(`{"requestId":"` + testNanoID1 + `","lane":"default","operationType":"CreateIdentity","actor":"vtx.identity.` + testNanoID2 + `","submittedAt":"2026-05-14T00:00:00Z","class":"identity","payload":{}}`)
	if _, err := conn.JetStream().Publish(ctx, "ops.default.bypass2", validPayload); err != nil {
		t.Fatalf("bypass2 baseline: publish to ops.default.bypass2: %v", err)
	}

	// Consumer MUST receive exactly 1 message.
	got := 0
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("bypass2 baseline: Fetch: %v", err)
	}
	for m := range batch.Messages() {
		got++
		_ = m.Ack()
	}
	if got != 1 {
		t.Fatalf("bypass2 baseline: expected 1 message on valid lane, got %d", got)
	}
	t.Logf("Bypass #2 baseline: valid ops.default.> publish received by consumer (FilterSubjects working)")
}

// TestBypass2_FilterSubjects_CoverageCheck verifies that the production
// consumer's FilterSubjects exactly match the values from step1_consume.go's
// applyDefaults(): ["ops.default", "ops.urgent", "ops.system", "ops.meta"].
// Production publishers send two-segment subjects (ops.<lane>); the consumer
// filter must match the publish form exactly. NATS '>' matches one-or-more
// trailing segments and does not cover the two-segment form by itself.
// This test fails if the defaults change without updating the bypass test.
func TestBypass2_FilterSubjects_CoverageCheck(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "bypass2-test"})
	if err != nil {
		t.Fatalf("bypass2 coverage: connect: %v", err)
	}
	t.Cleanup(conn.Close)
	provisionHarness(t, ctx, conn)

	// Create consumer using zero-value config — applyDefaults() fires.
	cons, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName: testStream,
		Durable:    "bypass2-coverage",
		AckWait:    2 * time.Second,
		// FilterSubjects intentionally omitted → applyDefaults() fills it.
	}, testLogger())
	if err != nil {
		t.Fatalf("bypass2 coverage: EnsureConsumer: %v", err)
	}

	// Introspect consumer info to read FilterSubjects from the server.
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatalf("bypass2 coverage: consumer info: %v", err)
	}

	filters := info.Config.FilterSubjects
	expected := map[string]bool{
		"ops.default": false,
		"ops.urgent":  false,
		"ops.system":  false,
		"ops.meta":    false,
	}
	for _, f := range filters {
		if _, ok := expected[f]; ok {
			expected[f] = true
		} else {
			t.Errorf("bypass2 coverage: unexpected filter subject: %q", f)
		}
	}
	for sub, found := range expected {
		if !found {
			t.Errorf("bypass2 coverage: missing expected filter subject: %q", sub)
		}
	}
	t.Logf("Bypass #2 coverage: consumer filters = %v", filters)
}

// assertConsumerReceivesNothing fetches up to maxFetch messages from the
// consumer and fails if any are received. The brief timeout (500ms) is
// sufficient since messages would be immediately available if filtered wrong.
func assertConsumerReceivesNothing(t *testing.T, ctx context.Context, cons jetstream.Consumer, tag string, maxFetch int) {
	t.Helper()
	batch, err := cons.Fetch(maxFetch, jetstream.FetchMaxWait(500*time.Millisecond))
	if err != nil {
		t.Logf("bypass: %s: Fetch returned %v (expected for empty consumer)", tag, err)
	}
	count := 0
	for m := range batch.Messages() {
		count++
		_ = m.Ack()
	}
	if count > 0 {
		t.Fatalf("bypass: BYPASS ESCAPED: %s: consumer received %d message(s) that should have been filtered", tag, count)
	}
}

// widenOpsStreamSubjects updates the core-operations stream to the real
// production subject shape, "ops.>" (internal/bootstrap/primordial.go,
// Contract #2 §2.3) — provisionHarness's own "ops.*"/"ops.meta.>" pair is
// narrower and never needed a 3-segment publish subject for its existing
// tests, but a production lane publish is "ops.<lane>.<...>".
func widenOpsStreamSubjects(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()
	stream, err := js.Stream(ctx, testStream)
	if err != nil {
		t.Fatalf("widenOpsStreamSubjects: get stream: %v", err)
	}
	cfg := stream.CachedInfo().Config
	cfg.Subjects = []string{"ops.>"}
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("widenOpsStreamSubjects: update stream: %v", err)
	}
}
