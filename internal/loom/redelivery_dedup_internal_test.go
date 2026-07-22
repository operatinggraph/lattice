package loom

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// newLoomConn brings up an embedded JetStream server and returns a substrate
// connection to it (no buckets provisioned).
func newLoomConn(t *testing.T) *substrate.Conn {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	return conn
}

// newLoomStateStore returns a stateStore bound to a provisioned loom-state KV
// bucket. Used by the redelivery-dedup path tests below.
func newLoomStateStore(ctx context.Context, t *testing.T) *stateStore {
	t.Helper()
	conn := newLoomConn(t)
	const bucket = "loom-state"
	_, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
	require.NoError(t, err)
	return newStateStore(conn, bucket)
}

// TestDeleteToken_ClearsAdvancedPointerIdempotently pins the
// idempotency-on-redelivery guard (engine.go redelivered-completion path): when
// a completion is redelivered after the cursor already advanced, deleteToken
// clears the stale token.<token> reverse pointer so resolveToken no longer maps
// it to the (now past) instance, and a SECOND clear of the same token — the
// shape redelivery actually produces — is a no-op, never an error.
func TestDeleteToken_ClearsAdvancedPointerIdempotently(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStore(ctx, t)
	const token = "stale-token"

	// Seed a reverse pointer exactly as transition() would write it.
	ptrBody, err := json.Marshal(tokenPointer{InstanceID: "inst-1"})
	require.NoError(t, err)
	_, err = s.conn.KVPut(ctx, s.bucket, tokenKey(token), ptrBody)
	require.NoError(t, err)

	// Precondition: the pointer resolves.
	id, ok, err := s.resolveToken(ctx, token)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "inst-1", id)

	// deleteToken clears it.
	require.NoError(t, s.deleteToken(ctx, token))

	_, ok, err = s.resolveToken(ctx, token)
	require.NoError(t, err)
	require.False(t, ok, "pointer should be gone after deleteToken")

	// Redelivery: a second clear of the already-removed pointer is idempotent.
	require.NoError(t, s.deleteToken(ctx, token), "double-delete must not error (missing pointer is not an error)")
}

// TestDeleteToken_MissingPointerIsNoError pins that clearing a token that was
// never written returns nil — the guard runs unconditionally on the redelivery
// path and must not fail when the pointer is already absent.
func TestDeleteToken_MissingPointerIsNoError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStore(ctx, t)
	require.NoError(t, s.deleteToken(ctx, "never-written-token"))
}

// TestDeleteToken_PropagatesGenuineFailure pins that a real substrate failure
// (here: the loom-state bucket does not exist) is returned, not swallowed — the
// redelivery guard tolerates a missing pointer but must surface an actual KV
// error so the completion is redelivered rather than silently dropped.
func TestDeleteToken_PropagatesGenuineFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newStateStore(newLoomConn(t), "loom-state-never-provisioned")
	require.Error(t, s.deleteToken(ctx, "any-token"))
}

// TestRemoveOpMeta_DeregistersTombstonedOpMeta pins the live op-meta deregister
// (source.go handle → IsDeleted path): when an op meta-vertex is tombstoned, the
// operationType → vtx.meta.<id> mapping built by indexOpMeta must be dropped so
// a stale operationType no longer resolves to a deleted op meta-vertex.
func TestRemoveOpMeta_DeregistersTombstonedOpMeta(t *testing.T) {
	t.Parallel()
	src := newPatternSource(nil, "core-kv", "test", slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Index two distinct op metas via the same CDC path the source uses live.
	src.indexOpMeta("vtx.meta.opA", opMetaEnvelope(t, "createLease"))
	src.indexOpMeta("vtx.meta.opB", opMetaEnvelope(t, "signLease"))

	if _, ok := src.opMetaKey("createLease"); !ok {
		t.Fatal("indexOpMeta should have registered createLease")
	}

	// Tombstone opA: its operationType must deregister, opB untouched.
	src.removeOpMeta("opA")

	if _, ok := src.opMetaKey("createLease"); ok {
		t.Fatal("removeOpMeta should have dropped the tombstoned op's operationType")
	}
	k, ok := src.opMetaKey("signLease")
	if !ok || k != "vtx.meta.opB" {
		t.Fatalf("removeOpMeta dropped an unrelated mapping: got (%q, %v)", k, ok)
	}
}

// TestRemoveOpMeta_DropsEveryTypePointingAtID pins that removeOpMeta clears ALL
// operationType entries that resolve to the tombstoned id — the deregister loop
// keys on the target key, not a single operationType, so a meta-vertex reachable
// under more than one operationType is fully deregistered.
func TestRemoveOpMeta_DropsEveryTypePointingAtID(t *testing.T) {
	t.Parallel()
	src := newPatternSource(nil, "core-kv", "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	src.opMetaByType["typeOne"] = "vtx.meta.shared"
	src.opMetaByType["typeTwo"] = "vtx.meta.shared"
	src.opMetaByType["other"] = "vtx.meta.kept"

	src.removeOpMeta("shared")

	if _, ok := src.opMetaKey("typeOne"); ok {
		t.Fatal("typeOne pointing at vtx.meta.shared should be dropped")
	}
	if _, ok := src.opMetaKey("typeTwo"); ok {
		t.Fatal("typeTwo pointing at vtx.meta.shared should be dropped")
	}
	if k, ok := src.opMetaKey("other"); !ok || k != "vtx.meta.kept" {
		t.Fatalf("unrelated mapping should survive: got (%q, %v)", k, ok)
	}
}

// TestRemoveOpMeta_UnknownIDIsNoOp pins that deregistering an id with no indexed
// operationType leaves the index intact (CDC can redeliver a tombstone for a
// meta-vertex this source never indexed).
func TestRemoveOpMeta_UnknownIDIsNoOp(t *testing.T) {
	t.Parallel()
	src := newPatternSource(nil, "core-kv", "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	src.indexOpMeta("vtx.meta.opA", opMetaEnvelope(t, "createLease"))

	src.removeOpMeta("ghost")

	if k, ok := src.opMetaKey("createLease"); !ok || k != "vtx.meta.opA" {
		t.Fatalf("removeOpMeta of an unknown id must not disturb the index: got (%q, %v)", k, ok)
	}
}

// opMetaEnvelope builds the substrate aspect envelope shape indexOpMeta reads
// the operationType off of (data.operationType).
func opMetaEnvelope(t *testing.T, operationType string) []byte {
	t.Helper()
	body, err := json.Marshal(struct {
		Data struct {
			OperationType string `json:"operationType"`
		} `json:"data"`
	}{Data: struct {
		OperationType string `json:"operationType"`
	}{OperationType: operationType}})
	require.NoError(t, err)
	return body
}
