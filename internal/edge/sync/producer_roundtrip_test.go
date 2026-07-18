package sync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/edge/transport"
	"github.com/asolgan/lattice/internal/edge/transport/natstransport"
	refadapter "github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/subjects"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// TestProducerConsumerEnvelope_RoundTrip is the RR-4 guard
// (edge-lattice-full-design.md §8.1): the Edge deliberately re-declares the
// wire struct (deltaEnvelope) rather than importing the producer's unexported
// type, so a producer-side field rename or a key-shape the consumer rejects
// would otherwise pass CI. This publishes a delta through the REAL
// NatsSubjectAdapter and applies the captured bytes through the consumer's
// deltaEnvelope decode (Manager.handle) + edge/store — end-to-end, no
// hand-built envelope on either side.
func TestProducerConsumerEnvelope_RoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := newSyncTestConn(t, ctx)
	st := openTestStore(t)

	// The real producer: a NatsSubjectAdapter over the SYNC stream, keyed by
	// the reserved actor field + a business "anchor" column.
	keyOrder := []string{refadapter.PersonalActorKeyField, "anchor"}
	adpt, err := refadapter.NewNatsSubjectAdapter(ctx, conn, defaultSubjectPrefix, defaultStream, keyOrder)
	require.NoError(t, err)

	actor, err := substrate.NewNanoID()
	require.NoError(t, err)
	leaseID, err := substrate.NewNanoID()
	require.NoError(t, err)
	anchorKey := substrate.VertexKey("lease", leaseID) // the envelope's `key`

	// Capture the exact bytes the adapter publishes to the actor's subject.
	subject := subjects.PersonalSync(defaultSubjectPrefix, actor)
	sub, err := conn.NATS().SubscribeSync(subject)
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	keys := map[string]any{refadapter.PersonalActorKeyField: actor, "anchor": anchorKey}
	row := map[string]any{"anchor": anchorKey, "kind": "lease", "amount": float64(42)}
	require.NoError(t, adpt.Upsert(ctx, keys, row, 5 /*projectionSeq*/))

	upsertMsg, err := sub.NextMsg(5 * time.Second)
	require.NoError(t, err, "adapter must publish the upsert delta")

	// The real consumer: decode via the re-declared deltaEnvelope + apply
	// through edge/store, exactly as the live Sync Manager does.
	mgr, err := New(natstransport.New(conn), st, Config{IdentityID: "identityA", DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)

	decision := mgr.handle(ctx, transport.Delta{Sequence: 1, Subject: subject, Body: upsertMsg.Data})
	require.Equal(t, transport.Ack, decision, "a well-formed producer envelope must apply cleanly")

	entry, ok, err := st.Get(anchorKey)
	require.NoError(t, err)
	require.True(t, ok, "the upsert must land under the envelope's key")
	require.Equal(t, uint64(5), entry.Revision, "the consumer must carry the producer's projectionSeq as revision")
	require.False(t, entry.Deleted)
	// The reserved anchor/kind columns are lifted to envelope metadata; only
	// the business columns reach Data.
	var data map[string]any
	require.NoError(t, json.Unmarshal(entry.Data, &data))
	require.Equal(t, float64(42), data["amount"])
	require.NotContains(t, data, "kind", "reserved metadata fields must not leak into Data")

	// A delete round-trips through the same decode + LWW gate.
	require.NoError(t, adpt.Delete(ctx, keys, 6))
	delMsg, err := sub.NextMsg(5 * time.Second)
	require.NoError(t, err)
	delDecision := mgr.handle(ctx, transport.Delta{Sequence: 2, Subject: subject, Body: delMsg.Data})
	require.Equal(t, transport.Ack, delDecision)

	entry, ok, err = st.Get(anchorKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, entry.Deleted, "the delete delta must tombstone the local key")
}
