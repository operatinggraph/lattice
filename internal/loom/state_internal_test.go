package loom

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// newLoomStateStoreTTL is newLoomStateStore's TTL-capable counterpart —
// rearmDeadline (KVPutWithTTL) requires the backing bucket provisioned with
// LimitMarkerTTL (Contract #4 §4.3, mirroring bootstrap/primordial.go's real
// loom-state provisioning), which the redelivery-dedup tests' plain bucket
// does not need.
func newLoomStateStoreTTL(ctx context.Context, t *testing.T) *stateStore {
	t.Helper()
	conn := newLoomConn(t)
	const bucket = "loom-state"
	_, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket, LimitMarkerTTL: time.Second})
	require.NoError(t, err)
	return newStateStore(conn, bucket)
}

// TestDisarmDeadline_MissingKeyIsNoOp pins the already-disarmed no-op: probing
// a deadline key that was never armed (or was already disarmed) finds
// ErrKeyNotFound on the KVGet and returns nil without attempting a delete.
// This is what breaks the deadline-watcher re-entry loop (state.go doc
// comment on disarmDeadline) — the watcher's own DEL marker re-fires itself,
// and a second disarm must be a true no-op rather than erroring.
func TestDisarmDeadline_MissingKeyIsNoOp(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStore(ctx, t)
	require.NoError(t, s.disarmDeadline(ctx, "never-armed-instance"))
}

// TestDisarmDeadline_ReentryAfterDisarmIsIdempotent proves the full re-entry
// loop this guard exists to break: arm, disarm (deletes the key), then disarm
// again (the watcher's re-fire) — the second call must also be a no-op, not
// an error.
func TestDisarmDeadline_ReentryAfterDisarmIsIdempotent(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newLoomStateStoreTTL(ctx, t)
	const instanceID = "inst-reentry"

	require.NoError(t, s.rearmDeadline(ctx, instanceID, time.Minute))
	_, err := s.conn.KVGet(ctx, s.bucket, deadlineKey(instanceID))
	require.NoError(t, err, "precondition: deadline key must be armed")

	require.NoError(t, s.disarmDeadline(ctx, instanceID))
	_, err = s.conn.KVGet(ctx, s.bucket, deadlineKey(instanceID))
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "first disarm must delete the key")

	require.NoError(t, s.disarmDeadline(ctx, instanceID), "re-fired disarm on an already-gone key must not error")
}

// TestDisarmDeadline_PropagatesGenuineGetFailure pins that a real substrate
// failure on the probe (here: the bucket does not exist) is returned, not
// swallowed as the tolerated "missing key" case.
func TestDisarmDeadline_PropagatesGenuineGetFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s := newStateStore(newLoomConn(t), "loom-state-never-provisioned")
	require.Error(t, s.disarmDeadline(ctx, "any-instance"))
}
