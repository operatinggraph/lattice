package substrate

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// markerCollector drains a KV subject's messages through the same newMessage
// view a consumer handler gets, so a test reads provenance exactly as a handler
// does — Message.Header, not a raw jetstream.Msg.
type markerCollector struct {
	mu   sync.Mutex
	msgs []Message
}

func (mc *markerCollector) add(m Message) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.msgs = append(mc.msgs, m)
}

// forSubject returns every message delivered on subject, oldest first.
func (mc *markerCollector) forSubject(subject string) []Message {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	var out []Message
	for _, m := range mc.msgs {
		if m.Subject == subject {
			out = append(out, m)
		}
	}
	return out
}

// startMarkerCollector attaches an ordered consumer to filter on the bucket's
// backing stream and collects every delivered message. It is started BEFORE the
// writes it observes, so nothing depends on how fast the server sweeps: a
// marker the server never emits is one the collector never sees, no matter how
// long the test waits.
func startMarkerCollector(ctx context.Context, t *testing.T, c *Conn, bucket, filter string) *markerCollector {
	t.Helper()
	stream, err := c.JetStream().Stream(ctx, "KV_"+bucket)
	require.NoError(t, err)
	cons, err := stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{FilterSubjects: []string{filter}})
	require.NoError(t, err)

	mc := &markerCollector{}
	cc, err := cons.Consume(func(msg jetstream.Msg) { mc.add(newMessage(msg)) })
	require.NoError(t, err)
	t.Cleanup(cc.Stop)
	return mc
}

// awaitMarker polls the collector for the n-th message on subject, so a wait is
// on a DELIVERED message rather than on a duration.
func awaitMarker(t *testing.T, mc *markerCollector, subject string, n int, why string) Message {
	t.Helper()
	var got Message
	require.Eventually(t, func() bool {
		msgs := mc.forSubject(subject)
		if len(msgs) < n {
			return false
		}
		got = msgs[n-1]
		return true
	}, 30*time.Second, 50*time.Millisecond, why)
	return got
}

// requireNoFurtherMarker asserts that subject never grows past want messages
// within window — the shape "no marker is emitted", which only a bounded
// negative wait can establish. window must outlast whatever TTL would have
// produced the marker.
func requireNoFurtherMarker(t *testing.T, mc *markerCollector, subject string, want int, window time.Duration, why string) {
	t.Helper()
	require.Never(t, func() bool {
		return len(mc.forSubject(subject)) > want
	}, window, 50*time.Millisecond, why)
}

// TestKVMarkerProvenance_ExpiryIsTheOnlyMaxAgeMarker is the mechanism pin under
// the deadline watcher's classifier: on a bucket provisioned like loom-state
// (LimitMarkerTTL 1s), the four ways a KV subject can lose its value are told
// apart by the headers the substrate delivers, and only ONE of them is the
// server's expiry.
//
//   - a TTL'd value that expires: no KV-Operation, Nats-Marker-Reason: MaxAge;
//   - a client delete: KV-Operation: DEL and no reason at all;
//   - a client purge carrying a TTL: KV-Operation: PURGE, and its own expiry
//     does NOT mint a later MaxAge marker over it;
//   - an arm evicted by a re-arm on a history-1 subject: nothing at all, so the
//     superseded TTL cannot fire late.
//
// The last two are why the watcher may key on MaxAge alone: a removal is never
// re-marked as an expiry, and a superseded arm never expires. Grounded against
// nats-server v2.14.0 (server/filestore.go:6883-6893, :6948-6963;
// server/sdm.go:41-43; ADR-43 Limit Markers); a vendor bump that changes any of
// it fails here.
func TestKVMarkerProvenance_ExpiryIsTheOnlyMaxAgeMarker(t *testing.T) {
	t.Parallel()
	c, ctx := newPurgeTestConn(t)
	const bucket = "loom-state"
	provisionCoreBucket(ctx, t, c, bucket)

	const (
		armedKey   = "deadline.armExpires"
		deletedKey = "deadline.clientDeletes"
		purgedKey  = "deadline.clientPurges"
		evictedKey = "deadline.rearmEvicts"
	)
	mc := startMarkerCollector(ctx, t, c, bucket, kvBucketSubject(bucket, "deadline.>"))

	// 1. An arm with a per-key TTL, left to expire.
	_, err := c.KVPutWithTTL(ctx, bucket, armedKey, []byte(`{"setAt":"t0"}`), time.Second)
	require.NoError(t, err)

	// 2. An arm the client then deletes.
	_, err = c.KVPutWithTTL(ctx, bucket, deletedKey, []byte(`{"setAt":"t0"}`), 2*time.Second)
	require.NoError(t, err)
	require.NoError(t, c.KVDelete(ctx, bucket, deletedKey))

	// 3. An arm the client then purges with a TTL of its own.
	_, err = c.KVPutWithTTL(ctx, bucket, purgedKey, []byte(`{"setAt":"t0"}`), 2*time.Second)
	require.NoError(t, err)
	require.NoError(t, c.KVPurgeWithTTL(ctx, bucket, purgedKey, time.Second, 0))

	// 4. An arm superseded by a re-arm before its own TTL runs out.
	_, err = c.KVPutWithTTL(ctx, bucket, evictedKey, []byte(`{"setAt":"t0"}`), time.Second)
	require.NoError(t, err)
	_, err = c.KVPutWithTTL(ctx, bucket, evictedKey, []byte(`{"setAt":"t1"}`), 30*time.Second)
	require.NoError(t, err)

	armedSubj := kvBucketSubject(bucket, armedKey)
	deletedSubj := kvBucketSubject(bucket, deletedKey)
	purgedSubj := kvBucketSubject(bucket, purgedKey)
	evictedSubj := kvBucketSubject(bucket, evictedKey)

	// The expiry: an empty body carrying the server's reason and no
	// KV-Operation. This is the deadline watcher's entire admission test.
	expiry := awaitMarker(t, mc, armedSubj, 2, "the expired arm must deliver a marker after its own value")
	require.Empty(t, expiry.Body, "an expiry marker carries no body")
	require.Equal(t, MarkerReasonMaxAge, expiry.Header(MarkerReasonHeader),
		"the server's expiry marker names MaxAge as its reason")
	require.Empty(t, expiry.Header(KVOperationHeader),
		"a server-minted marker carries no KV-Operation — that header means a client asked")

	// The client delete: the mirror image.
	del := awaitMarker(t, mc, deletedSubj, 2, "the client delete must deliver a marker")
	require.Empty(t, del.Body)
	require.Equal(t, KVOperationDelete, del.Header(KVOperationHeader))
	require.Empty(t, del.Header(MarkerReasonHeader),
		"a client delete is not an expiry and must carry no marker reason")

	// The client purge, and — the load-bearing half — no MaxAge marker minted
	// over it when its own TTL runs out. The window outlasts both the purge
	// marker's 1s TTL and the arm's original 2s TTL.
	purge := awaitMarker(t, mc, purgedSubj, 2, "the client purge must deliver a marker")
	require.Empty(t, purge.Body)
	require.Equal(t, KVOperationPurge, purge.Header(KVOperationHeader))
	require.Empty(t, purge.Header(MarkerReasonHeader),
		"a client purge is not an expiry and must carry no marker reason")
	requireNoFurtherMarker(t, mc, purgedSubj, 2, 4*time.Second,
		"an expiring purge marker must not be re-marked as a MaxAge expiry")

	// The evicted arm: the re-arm's own value is the only thing after it. The
	// superseded 1s TTL must produce nothing, or an earlier step's deadline
	// could fire after the cursor advanced.
	awaitMarker(t, mc, evictedSubj, 2, "the re-arm's value must be delivered")
	require.Len(t, mc.forSubject(evictedSubj), 2, "the re-arm's value follows the arm's")
	requireNoFurtherMarker(t, mc, evictedSubj, 2, 4*time.Second,
		"an arm evicted by a re-arm must never emit its own expiry marker")

	for _, m := range mc.forSubject(evictedSubj) {
		require.NotEqual(t, MarkerReasonMaxAge, m.Header(MarkerReasonHeader))
	}
}
