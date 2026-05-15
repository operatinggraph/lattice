package consumer_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/consumer"
)

func TestManager_AddCreatesConsumer(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-mgr1"})
	require.NoError(t, err)

	m := consumer.NewManager(js, "core-mgr1")

	require.NoError(t, m.Add(ctx, "rule-1"))
	assert.NotNil(t, m.Consumer("rule-1"))
}

func TestManager_AddIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-mgr2"})
	require.NoError(t, err)

	m := consumer.NewManager(js, "core-mgr2")

	require.NoError(t, m.Add(ctx, "rule-1"))
	require.NoError(t, m.Add(ctx, "rule-1")) // second call must not error
	assert.NotNil(t, m.Consumer("rule-1"))
}

func TestManager_RemoveDeletesConsumer(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-mgr3"})
	require.NoError(t, err)

	m := consumer.NewManager(js, "core-mgr3")

	require.NoError(t, m.Add(ctx, "rule-1"))
	require.NoError(t, m.Remove(ctx, "rule-1"))
	assert.Nil(t, m.Consumer("rule-1"))
}

func TestManager_RemoveNonexistentIsNoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-mgr4"})
	require.NoError(t, err)

	m := consumer.NewManager(js, "core-mgr4")
	require.NoError(t, m.Remove(ctx, "nonexistent"))
}

func TestManager_StopRemovesAll(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-mgr5"})
	require.NoError(t, err)

	m := consumer.NewManager(js, "core-mgr5")

	require.NoError(t, m.Add(ctx, "rule-a"))
	require.NoError(t, m.Add(ctx, "rule-b"))

	m.Stop(ctx)

	assert.Nil(t, m.Consumer("rule-a"))
	assert.Nil(t, m.Consumer("rule-b"))
}

func TestManager_ConsumerNilForUnknownRule(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-mgr6"})
	require.NoError(t, err)

	m := consumer.NewManager(js, "core-mgr6")
	assert.Nil(t, m.Consumer("unknown-rule"))
}

// TestManager_Reset_ReturnsNewConsumerWithLastPerSubjectPolicy verifies that
// Reset deletes the existing consumer and creates a new one with
// DeliverLastPerSubjectPolicy; the returned consumer is registered in the manager (AC1).
func TestManager_Reset_ReturnsNewConsumerWithLastPerSubjectPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-mgr7"})
	require.NoError(t, err)

	m := consumer.NewManager(js, "core-mgr7")

	// Create initial consumer with DeliverAllPolicy.
	require.NoError(t, m.Add(ctx, "rule-reset"))
	originalCons := m.Consumer("rule-reset")
	require.NotNil(t, originalCons)

	// Reset to DeliverLastPerSubjectPolicy.
	newCons, err := m.Reset(ctx, "rule-reset")
	require.NoError(t, err)
	require.NotNil(t, newCons)

	// The manager's consumer reference must be updated.
	assert.Equal(t, newCons, m.Consumer("rule-reset"), "manager must return new consumer after reset")

	// The new consumer config must use DeliverLastPerSubjectPolicy.
	info, err := newCons.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, jetstream.DeliverLastPerSubjectPolicy, info.Config.DeliverPolicy,
		"reset consumer must use DeliverLastPerSubjectPolicy")
}

// ── Zero-downtime migration (two-consumer) tests ──────────────────────────────

// TestManager_TwoConsumers_Independent verifies that two rules with different IDs
// each get their own independent durable consumer and do not interfere with each other (AC1, FR32).
func TestManager_TwoConsumers_Independent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-two-cons"})
	require.NoError(t, err)

	m := consumer.NewManager(js, "core-two-cons")

	require.NoError(t, m.Add(ctx, "agreement-summary-v1"))
	require.NoError(t, m.Add(ctx, "agreement-summary-v2"))

	consV1 := m.Consumer("agreement-summary-v1")
	consV2 := m.Consumer("agreement-summary-v2")
	assert.NotNil(t, consV1, "v1 consumer must exist")
	assert.NotNil(t, consV2, "v2 consumer must exist")

	// Verify distinct durable names via consumer info.
	infoV1, err := consV1.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, "materializer-agreement-summary-v1", infoV1.Config.Durable)

	infoV2, err := consV2.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, "materializer-agreement-summary-v2", infoV2.Config.Durable)
}

// TestManager_RemoveV1_LeavesV2 verifies that removing v1's consumer does not affect
// v2's consumer — v2 remains registered and accessible (AC3, FR32).
func TestManager_RemoveV1_LeavesV2(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-remove-v1"})
	require.NoError(t, err)

	m := consumer.NewManager(js, "core-remove-v1")

	require.NoError(t, m.Add(ctx, "agreement-summary-v1"))
	require.NoError(t, m.Add(ctx, "agreement-summary-v2"))

	// Capture v2 consumer handle before removing v1.
	consV2Before := m.Consumer("agreement-summary-v2")
	require.NotNil(t, consV2Before, "v2 consumer must exist before Remove")

	// Remove v1 consumer.
	require.NoError(t, m.Remove(ctx, "agreement-summary-v1"))

	// v1 must be gone; v2 must still be registered in the local map.
	assert.Nil(t, m.Consumer("agreement-summary-v1"), "v1 consumer must be nil after Remove")
	assert.NotNil(t, m.Consumer("agreement-summary-v2"), "v2 consumer must still exist after v1 Remove")

	// v2 must also still exist in NATS JetStream (not just in the local map).
	_, err = consV2Before.Info(ctx)
	require.NoError(t, err, "v2 durable consumer must still exist in NATS after v1 is removed")
}

// TestManager_Reset_WithoutPriorAdd verifies that Reset succeeds even when
// no consumer exists for the rule (no prior Add call).
func TestManager_Reset_WithoutPriorAdd(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, _ := startJS(t)
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-mgr8"})
	require.NoError(t, err)

	m := consumer.NewManager(js, "core-mgr8")

	// Reset without prior Add — should succeed.
	newCons, err := m.Reset(ctx, "rule-fresh")
	require.NoError(t, err)
	require.NotNil(t, newCons)

	info, err := newCons.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, jetstream.DeliverLastPerSubjectPolicy, info.Config.DeliverPolicy)
}
