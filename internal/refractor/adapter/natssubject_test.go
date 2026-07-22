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

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// startSyncServer starts an in-memory NATS server with JetStream and returns
// a wrapped substrate.Conn plus the raw jetstream.JetStream handle (for
// asserting published envelopes off the backing stream).
func startSyncServer(t *testing.T) (*substrate.Conn, jetstream.JetStream) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  jsstore.Dir(t),
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
	t.Cleanup(func() { nc.Close(); s.Shutdown() })

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	return conn, js
}

// readSyncMsg reads one message off the SYNC stream for the given actor
// subject, timing out after 2s.
func readSyncMsg(t *testing.T, js jetstream.JetStream, stream, subject string) map[string]any {
	t.Helper()
	cons, err := js.CreateOrUpdateConsumer(context.Background(), stream, jetstream.ConsumerConfig{
		FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	msg, err := cons.Next(jetstream.FetchMaxWait(2 * time.Second))
	require.NoError(t, err, "must receive one message on %s", subject)

	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	return env
}

func TestNewNatsSubjectAdapter_EnsuresStream(t *testing.T) {
	conn, js := startSyncServer(t)
	_, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	s, err := js.Stream(context.Background(), "SYNC")
	assert.NoError(t, err, "SYNC stream must exist after construction")
	assert.Equal(t, 24*time.Hour, s.CachedInfo().Config.MaxAge, "SYNC stream must retain the designed 24h MaxAge (personal-secure-lens-design.md §3.2)")
}

func TestNewNatsSubjectAdapter_RejectsMissingConfig(t *testing.T) {
	conn, _ := startSyncServer(t)
	_, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField})
	assert.Error(t, err, "empty ruleID must be rejected")

	_, err = adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "", "SYNC", []string{adapter.PersonalActorKeyField})
	assert.Error(t, err)

	_, err = adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "", []string{adapter.PersonalActorKeyField})
	assert.Error(t, err)

	_, err = adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{"entityId"})
	assert.Error(t, err, "keyOrder without PersonalActorKeyField must be rejected")
}

func TestNatsSubjectAdapter_Upsert_PublishesEnvelopeToActorSubject(t *testing.T) {
	conn, js := startSyncServer(t)
	a, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	keys := map[string]any{adapter.PersonalActorKeyField: "identityA", "entityId": "lease.abc123"}
	row := map[string]any{
		"anchor":      "lease.abc123",
		"kind":        "aspect",
		"class":       "lease.terms",
		"monthlyRent": float64(2400),
	}
	require.NoError(t, a.Upsert(context.Background(), keys, row, 10481))

	env := readSyncMsg(t, js, "SYNC", "lattice.sync.user.identityA")
	assert.Equal(t, "upsert", env["op"])
	assert.Equal(t, "lease.abc123", env["key"])
	assert.Equal(t, "lease.abc123", env["anchor"])
	assert.Equal(t, "aspect", env["kind"])
	assert.Equal(t, "lease.terms", env["class"])
	assert.Equal(t, float64(10481), env["revision"])
	assert.Equal(t, float64(10481), env["projectionSeq"])
	assert.Equal(t, false, env["encrypted"])
	data, ok := env["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(2400), data["monthlyRent"])
	// Reserved envelope fields must not leak into data.
	assert.NotContains(t, data, "anchor")
	assert.NotContains(t, data, "kind")
	assert.NotContains(t, data, "class")
}

func TestNatsSubjectAdapter_Upsert_DisjointActorsGetDisjointSubjects(t *testing.T) {
	conn, js := startSyncServer(t)
	a, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	require.NoError(t, a.Upsert(context.Background(), map[string]any{adapter.PersonalActorKeyField: "identityA", "entityId": "e1"}, map[string]any{"v": 1.0}, 1))
	require.NoError(t, a.Upsert(context.Background(), map[string]any{adapter.PersonalActorKeyField: "identityB", "entityId": "e2"}, map[string]any{"v": 2.0}, 2))

	envA := readSyncMsg(t, js, "SYNC", "lattice.sync.user.identityA")
	envB := readSyncMsg(t, js, "SYNC", "lattice.sync.user.identityB")
	assert.Equal(t, "e1", envA["key"])
	assert.Equal(t, "e2", envB["key"])
}

// TestNatsSubjectAdapter_Upsert_CiphertextFieldSetsEncrypted proves Fire 5's
// passthrough marking (personal-secure-lens-design.md §3.6): a row carrying a
// Vault ciphertext-envelope-shaped field publishes with encrypted:true, and
// the field itself is forwarded byte-for-byte (never decoded/decrypted).
func TestNatsSubjectAdapter_Upsert_CiphertextFieldSetsEncrypted(t *testing.T) {
	conn, js := startSyncServer(t)
	a, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	keys := map[string]any{adapter.PersonalActorKeyField: "identityA", "entityId": "identity.ssn"}
	ciphertextEnvelope := map[string]any{"ct": "c2VhbGVkLWJ5dGVz", "nonce": "b25jZS1ieXRlcw==", "keyId": "identity-A"}
	row := map[string]any{
		"anchor": "identity.A",
		"kind":   "aspect",
		"class":  "identity.ssn",
		"ssn":    ciphertextEnvelope,
	}
	require.NoError(t, a.Upsert(context.Background(), keys, row, 20481))

	env := readSyncMsg(t, js, "SYNC", "lattice.sync.user.identityA")
	assert.Equal(t, true, env["encrypted"], "a ciphertext-shaped field must set encrypted:true")
	data, ok := env["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, ciphertextEnvelope, data["ssn"], "the ciphertext envelope must forward unchanged, never decoded")
}

// TestNatsSubjectAdapter_Upsert_FieldNamedCtAloneNotFlaggedEncrypted proves
// rowHasCiphertext requires the full envelope shape (ct+nonce+keyId all
// non-empty), not just a field that happens to be named "ct" — a business
// column named "ct" with no nonce/keyId must not false-positive as ciphertext.
func TestNatsSubjectAdapter_Upsert_FieldNamedCtAloneNotFlaggedEncrypted(t *testing.T) {
	conn, js := startSyncServer(t)
	a, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	keys := map[string]any{adapter.PersonalActorKeyField: "identityA", "entityId": "e1"}
	row := map[string]any{"stats": map[string]any{"ct": "aGVsbG8="}} // a nested field merely named "ct", no nonce/keyId
	require.NoError(t, a.Upsert(context.Background(), keys, row, 1))

	env := readSyncMsg(t, js, "SYNC", "lattice.sync.user.identityA")
	assert.Equal(t, false, env["encrypted"], "a bare 'ct'-named field with no nonce/keyId must not be flagged as ciphertext")
}

func TestNatsSubjectAdapter_Delete_PublishesTombstoneEnvelope(t *testing.T) {
	conn, js := startSyncServer(t)
	a, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	keys := map[string]any{adapter.PersonalActorKeyField: "identityA", "entityId": "lease.abc123"}
	require.NoError(t, a.Delete(context.Background(), keys, 10482))

	env := readSyncMsg(t, js, "SYNC", "lattice.sync.user.identityA")
	assert.Equal(t, "delete", env["op"])
	assert.Equal(t, "lease.abc123", env["key"])
	assert.Equal(t, float64(10482), env["projectionSeq"])
	assert.Nil(t, env["data"])
}

func TestNatsSubjectAdapter_Upsert_MissingActorKeyErrors(t *testing.T) {
	conn, _ := startSyncServer(t)
	a, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	err = a.Upsert(context.Background(), map[string]any{"entityId": "lease.abc123"}, map[string]any{}, 1)
	assert.Error(t, err)
}

func TestNatsSubjectAdapter_Upsert_MalformedActorFailsClosedNotPanic(t *testing.T) {
	conn, _ := startSyncServer(t)
	a, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	// A non-string __actor (e.g. a malformed cypher projection) must return an
	// error, not panic the whole process via subjects.PersonalSync's
	// validateToken.
	assert.NotPanics(t, func() {
		err = a.Upsert(context.Background(), map[string]any{adapter.PersonalActorKeyField: map[string]any{"bad": "shape"}, "entityId": "e1"}, map[string]any{}, 1)
	})
	assert.Error(t, err)

	// A string __actor containing a subject-unsafe character (e.g. a stray
	// dot) must also fail closed rather than reach the panic path.
	assert.NotPanics(t, func() {
		err = a.Upsert(context.Background(), map[string]any{adapter.PersonalActorKeyField: "actor.with.dots", "entityId": "e1"}, map[string]any{}, 1)
	})
	assert.Error(t, err)
}

func TestNatsSubjectAdapter_PublishHydrationComplete_PublishesMarker(t *testing.T) {
	conn, js := startSyncServer(t)
	a, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField, "entityId"})
	require.NoError(t, err)

	require.NoError(t, a.PublishHydrationComplete(context.Background(), "identityA", 10500))

	env := readSyncMsg(t, js, "SYNC", "lattice.sync.user.identityA")
	assert.Equal(t, "hydrationComplete", env["op"])
	assert.Equal(t, float64(10500), env["revision"])
	assert.Equal(t, float64(10500), env["projectionSeq"])
	assert.Nil(t, env["data"])
	assert.Equal(t, "", env["key"], "the marker carries no row key")
}

func TestNatsSubjectAdapter_SatisfiesHydrationMarkerPublisher(t *testing.T) {
	var _ adapter.HydrationMarkerPublisher = (*adapter.NatsSubjectAdapter)(nil)
}

func TestNatsSubjectAdapter_Probe(t *testing.T) {
	conn, _ := startSyncServer(t)
	a, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField})
	require.NoError(t, err)
	assert.NoError(t, a.Probe(context.Background()))
}

func TestNatsSubjectAdapter_Close(t *testing.T) {
	conn, _ := startSyncServer(t)
	a, err := adapter.NewNatsSubjectAdapter(context.Background(), conn, "rule-1", "lattice.sync.user", "SYNC", []string{adapter.PersonalActorKeyField})
	require.NoError(t, err)
	assert.NoError(t, a.Close())
}
