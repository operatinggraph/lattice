package adapter_test

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

	"github.com/asolgan/lattice/internal/refractor/adapter"
)

// startKV starts an in-memory NATS server with JetStream and returns a KV bucket for testing.
func startKV(t *testing.T) jetstream.KeyValue {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
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

	kv, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "test-target"})
	require.NoError(t, err)
	return kv
}

// newAdapter is a test helper that requires New to succeed.
func newAdapter(t *testing.T, kv jetstream.KeyValue, keyOrder []string) *adapter.NatsKVAdapter {
	t.Helper()
	a, err := adapter.New(kv, keyOrder)
	require.NoError(t, err)
	return a
}

func TestNatsKVAdapter_New_EmptyKeyOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	kv := startKV(t)
	_, err := adapter.New(kv, nil)
	require.Error(t, err)
	_, err = adapter.New(kv, []string{})
	require.Error(t, err)
}

func TestNatsKVAdapter_Upsert_SingleKey(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"agreement_id"})

	keys := map[string]any{"agreement_id": "abc123"}
	row := map[string]any{"party_name": "Acme Corp", "status": "active"}

	err := a.Upsert(context.Background(), keys, row)
	require.NoError(t, err)

	entry, err := kv.Get(context.Background(), "abc123")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	assert.Equal(t, "Acme Corp", got["party_name"])
	assert.Equal(t, "active", got["status"])
}

func TestNatsKVAdapter_Upsert_CompositeKey(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"account_id", "agreement_id"})

	keys := map[string]any{"account_id": "acct-001", "agreement_id": "abc123"}
	row := map[string]any{"name": "Widget Agreement"}

	err := a.Upsert(context.Background(), keys, row)
	require.NoError(t, err)

	entry, err := kv.Get(context.Background(), "acct-001.abc123")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	assert.Equal(t, "Widget Agreement", got["name"])
}

func TestNatsKVAdapter_Upsert_Idempotent(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"id"})

	keys := map[string]any{"id": "e1"}

	err := a.Upsert(context.Background(), keys, map[string]any{"value": "first"})
	require.NoError(t, err)

	err = a.Upsert(context.Background(), keys, map[string]any{"value": "second"})
	require.NoError(t, err)

	entry, err := kv.Get(context.Background(), "e1")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &got))
	assert.Equal(t, "second", got["value"], "expected latest value after two upserts")
}

func TestNatsKVAdapter_Upsert_AbsentKeyField(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"id", "account_id"})

	// "account_id" is absent from the keys map.
	err := a.Upsert(context.Background(), map[string]any{"id": "e1"}, map[string]any{"x": 1})
	require.Error(t, err)
}

func TestNatsKVAdapter_Delete(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"id"})

	keys := map[string]any{"id": "e1"}

	require.NoError(t, a.Upsert(context.Background(), keys, map[string]any{"x": 1}))

	require.NoError(t, a.Delete(context.Background(), keys))

	// Story 2.1 AC #4: NATS-KV Delete writes a tombstone document
	// {"isDeleted": true}, NOT a physical KV delete.
	entry, err := kv.Get(context.Background(), "e1")
	require.NoError(t, err)
	require.Contains(t, string(entry.Value()), `"isDeleted":true`)
}

func TestNatsKVAdapter_Delete_NeverExisted(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"id"})

	// Deleting a key that was never upserted must succeed (no-op / idempotent).
	err := a.Delete(context.Background(), map[string]any{"id": "ghost"})
	require.NoError(t, err)
}

func TestNatsKVAdapter_Probe_Success(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"id"})
	err := a.Probe(context.Background())
	assert.NoError(t, err, "Probe should succeed on a live bucket")
}

func TestNatsKVAdapter_Close(t *testing.T) {
	kv := startKV(t)
	a := newAdapter(t, kv, []string{"id"})
	assert.NoError(t, a.Close())
}
