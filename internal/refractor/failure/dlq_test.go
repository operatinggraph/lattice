package failure_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/failure"
)

// startFailureJetStreamServer starts an in-memory NATS server with JetStream enabled
// and returns a connected JetStream handle. The server and connection are shut down
// via t.Cleanup at the end of the test.
func startFailureJetStreamServer(t *testing.T) jetstream.JetStream {
	t.Helper()
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err, "create test NATS server")
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second), "NATS server not ready within 5s")

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err, "connect to test NATS server")

	t.Cleanup(func() {
		nc.Close()
		s.Shutdown()
	})

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	return js
}

// TestPublish_WritesCorrectFields publishes a DLQMessage and reads it back from the
// JetStream stream, asserting all fields are present with camelCase JSON keys (FR20).
func TestPublish_WritesCorrectFields(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	js := startFailureJetStreamServer(t)
	ctx := context.Background()

	msg := failure.DLQMessage{
		RuleID:       "test-rule",
		EntityID:     "entity-1",
		FailedStage:  "write",
		ErrorClass:   "TRANSIENT",
		ErrorMessage: "something went wrong",
		RetryCount:   3,
		RuleSequence: "",
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		RawPayload:   `{"id":"entity-1"}`,
	}

	err := failure.Publish(ctx, js, "team-a", "test-rule", msg)
	require.NoError(t, err)

	// Read back the message via an ordered consumer on the DLQ stream.
	cons, err := js.OrderedConsumer(ctx, "MATERIALIZER_DLQ_TEST-RULE", jetstream.OrderedConsumerConfig{})
	require.NoError(t, err)

	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	require.NoError(t, err)

	var received jetstream.Msg
	for m := range batch.Messages() {
		received = m
	}
	require.NotNil(t, received, "expected one message in DLQ stream")
	require.NoError(t, received.Ack())

	// Verify struct fields round-trip correctly.
	var got failure.DLQMessage
	require.NoError(t, json.Unmarshal(received.Data(), &got))
	assert.Equal(t, "test-rule", got.RuleID)
	assert.Equal(t, "entity-1", got.EntityID)
	assert.Equal(t, "write", got.FailedStage)
	assert.Equal(t, "TRANSIENT", got.ErrorClass)
	assert.Equal(t, "something went wrong", got.ErrorMessage)
	assert.Equal(t, 3, got.RetryCount)
	assert.Equal(t, `{"id":"entity-1"}`, got.RawPayload)

	// Verify JSON field names are camelCase per architecture.md.
	var rawJSON map[string]any
	require.NoError(t, json.Unmarshal(received.Data(), &rawJSON))
	for _, field := range []string{"ruleId", "entityId", "failedStage", "errorClass", "errorMessage", "retryCount", "ruleSequence", "timestamp", "rawPayload"} {
		assert.Contains(t, rawJSON, field, "expected camelCase field %q in DLQ message JSON", field)
	}
}
