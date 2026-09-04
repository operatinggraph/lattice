package substrate

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// newPurgeTestConn is newTestConn with a context long enough to outlive a
// marker TTL plus the server's expiry sweep. The purge mechanism can only be
// observed by waiting for a marker to expire, which newTestConn's 10s budget
// leaves no headroom for.
func newPurgeTestConn(t *testing.T) (*Conn, context.Context) {
	t.Helper()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	c, err := Connect(ctx, ConnectOpts{URL: url, Name: "substrate-purge-test"})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c, ctx
}

// lastMsgHeaders returns the headers of the newest message on a key's KV
// subject, or ok=false when the subject holds nothing.
func lastMsgHeaders(ctx context.Context, t *testing.T, c *Conn, bucket, key string) (hdr nats.Header, seq uint64, ok bool) {
	t.Helper()
	stream, err := c.JetStream().Stream(ctx, "KV_"+bucket)
	require.NoError(t, err)
	msg, err := stream.GetLastMsgForSubject(ctx, kvBucketSubject(bucket, key))
	if err != nil {
		return nil, 0, false
	}
	return msg.Header, msg.Sequence, true
}

// subjectPresent reports whether the stream still carries any message on a
// key's KV subject, read from STREAM.INFO's subject details (the same surface
// `nats stream subjects` and Loupe's stream views show an operator).
//
// It is polled from inside require.Eventually predicates, so it asserts
// nothing and takes no *testing.T: a read that fails cannot conclude the
// subject is gone, so it reports the subject still present. A predicate
// waiting for absence keeps polling and, if the reads never recover, fails on
// its own timeout message instead of aborting a goroutine testify does not
// own.
func subjectPresent(ctx context.Context, c *Conn, bucket, key string) bool {
	stream, err := c.JetStream().Stream(ctx, "KV_"+bucket)
	if err != nil {
		return true
	}
	info, err := stream.Info(ctx, jetstream.WithSubjectFilter(kvBucketSubject(bucket, key)))
	if err != nil {
		return true
	}
	_, present := info.State.Subjects[kvBucketSubject(bucket, key)]
	return present
}

// TestAtomicBatch_PurgeWithTTLLeavesNoSubject is the mechanism pin for the
// TTL'd purge: a BatchOp{Purge, TTL} renders the PURGE + rollup headers, the
// key reads absent at once, and once the marker's TTL passes the SUBJECT
// itself is gone from the stream — the server does not write a fresh
// end-of-life marker over an expiring purge marker (nats-server v2.14.0
// server/sdm.go:42-44, server/filestore.go:6827), which is the whole reason
// this shape replaces a DEL on a history-1 bucket.
func TestAtomicBatch_PurgeWithTTLLeavesNoSubject(t *testing.T) {
	t.Parallel()
	c, ctx := newPurgeTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	_, err := c.KVCreate(ctx, bucket, key, []byte(`{"seed":true}`))
	require.NoError(t, err)

	_, err = c.AtomicBatch(ctx, []BatchOp{{Bucket: bucket, Key: key, Purge: true, TTL: time.Second}})
	require.NoError(t, err)

	// Absent to every reader, immediately.
	_, err = c.KVGet(ctx, bucket, key)
	require.ErrorIs(t, err, ErrKeyNotFound, "a purged key must read absent at once")
	keys, _, err := c.KVListKeysFilter(ctx, bucket, key, "", 0)
	require.NoError(t, err)
	require.Empty(t, keys, "a purged key must not be listed")

	// The marker that stands in its place is a PURGE with a subject rollup —
	// the header shape the server needs to treat it as a subject-delete
	// marker, and the reason its expiry ends the subject.
	hdr, _, ok := lastMsgHeaders(ctx, t, c, bucket, key)
	require.True(t, ok, "a marker must stand on the subject while the TTL runs")
	require.Equal(t, "PURGE", hdr.Get("KV-Operation"), "the marker must decode as a purge, not a delete")
	require.Equal(t, "sub", hdr.Get("Nats-Rollup"), "the purge must roll up its subject")
	require.NotEmpty(t, hdr.Get("Nats-TTL"), "the marker must carry the TTL that expires it")

	// And it decodes as KeyValuePurge to a KV reader while it stands.
	tombstones, err := c.KVListTombstones(ctx, bucket, key)
	require.NoError(t, err)
	require.Empty(t, tombstones, "a purge marker is not a DELETE tombstone")

	// The subject leaves the stream when the marker expires, with no
	// replacement marker written over it.
	require.Eventually(t, func() bool {
		return !subjectPresent(ctx, c, bucket, key)
	}, 30*time.Second, 250*time.Millisecond,
		"the subject must disappear once the purge marker's TTL expires")
}

// TestKVPurgeWithTTL_SinglePublishLeavesNoSubject is the same mechanism
// through the single-publish method rather than a batch, unconditioned.
func TestKVPurgeWithTTL_SinglePublishLeavesNoSubject(t *testing.T) {
	t.Parallel()
	c, ctx := newPurgeTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	_, err := c.KVCreate(ctx, bucket, key, []byte(`{"seed":true}`))
	require.NoError(t, err)

	require.NoError(t, c.KVPurgeWithTTL(ctx, bucket, key, time.Second, 0))

	_, err = c.KVGet(ctx, bucket, key)
	require.ErrorIs(t, err, ErrKeyNotFound)

	hdr, _, ok := lastMsgHeaders(ctx, t, c, bucket, key)
	require.True(t, ok)
	require.Equal(t, "PURGE", hdr.Get("KV-Operation"))
	require.Equal(t, "sub", hdr.Get("Nats-Rollup"))

	require.Eventually(t, func() bool {
		return !subjectPresent(ctx, c, bucket, key)
	}, 30*time.Second, 250*time.Millisecond,
		"the subject must disappear once the purge marker's TTL expires")
}

// TestKVPurgeWithTTL_RevisionConditioned pins the outcome table of a
// revision-conditioned purge: the marker's own revision commits, a stale
// revision is a conflict, and a revision aimed at a key that is already gone
// is the SAME conflict class (the server answers 10071 for an absent subject
// too) rather than a success or a distinct not-found.
func TestKVPurgeWithTTL_RevisionConditioned(t *testing.T) {
	t.Parallel()
	c, ctx := newPurgeTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	stale, err := c.KVCreate(ctx, bucket, key, []byte(`{"v":1}`))
	require.NoError(t, err)
	rev, err := c.KVPut(ctx, bucket, key, []byte(`{"v":2}`))
	require.NoError(t, err)
	require.Greater(t, rev, stale)

	// A stale expectation is refused and the value survives.
	err = c.KVPurgeWithTTL(ctx, bucket, key, time.Minute, stale)
	require.ErrorIs(t, err, ErrRevisionConflict, "a stale expected revision must conflict")
	entry, err := c.KVGet(ctx, bucket, key)
	require.NoError(t, err, "a refused purge must leave the value intact")
	require.Equal(t, rev, entry.Revision)

	// The current revision commits.
	require.NoError(t, c.KVPurgeWithTTL(ctx, bucket, key, time.Minute, rev))
	_, err = c.KVGet(ctx, bucket, key)
	require.ErrorIs(t, err, ErrKeyNotFound)

	// A second conditioned purge at the same revision now conflicts: the
	// subject's last sequence moved to the marker's.
	err = c.KVPurgeWithTTL(ctx, bucket, key, time.Minute, rev)
	require.ErrorIs(t, err, ErrRevisionConflict)

	// A conditioned purge of a key that never existed is the same class —
	// an absent subject's last sequence is 0 and can never match a non-zero
	// expectation.
	err = c.KVPurgeWithTTL(ctx, bucket, VertexKey("identity", testNanoID3), time.Minute, 7)
	require.ErrorIs(t, err, ErrRevisionConflict,
		"a conditioned purge of an already-gone subject must surface as a revision conflict")
}

// TestKVPurgeWithTTL_UnconditionedOnNeverWrittenKey pins the behaviour the
// callers that tolerate an absent key depend on: an UNCONDITIONED purge of a
// key that was never written is accepted (a rollup over an empty subject),
// leaving only the TTL'd marker — it is not an error and not ErrKeyNotFound.
func TestKVPurgeWithTTL_UnconditionedOnNeverWrittenKey(t *testing.T) {
	t.Parallel()
	c, ctx := newPurgeTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID2)
	require.NoError(t, c.KVPurgeWithTTL(ctx, bucket, key, time.Second, 0),
		"an unconditioned purge of a never-written key must be accepted")
	_, err := c.KVGet(ctx, bucket, key)
	require.ErrorIs(t, err, ErrKeyNotFound)

	require.Eventually(t, func() bool {
		return !subjectPresent(ctx, c, bucket, key)
	}, 30*time.Second, 250*time.Millisecond,
		"the marker such a purge leaves must expire like any other")
}

// TestKVPurgeWithTTL_RefusesTTLBelowTheServerFloor proves the method refuses
// every TTL the server would not honor rather than publishing one: zero and
// negative leave the permanent marker this shape exists to avoid, and a
// sub-second value is rejected outright by parseMessageTTL (nats-server
// v2.14.0 server/stream.go), so 999ms is not a shorter-lived marker but a
// failed publish. The refusal is pre-flight — the seeded value is untouched.
func TestKVPurgeWithTTL_RefusesTTLBelowTheServerFloor(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	rev, err := c.KVCreate(ctx, bucket, key, []byte(`{"v":1}`))
	require.NoError(t, err)

	for _, ttl := range []time.Duration{0, -time.Second, 999 * time.Millisecond} {
		require.Error(t, c.KVPurgeWithTTL(ctx, bucket, key, ttl, 0),
			"ttl=%s must be refused", ttl)
	}
	entry, err := c.KVGet(ctx, bucket, key)
	require.NoError(t, err, "a refused purge must not have published anything")
	require.Equal(t, rev, entry.Revision, "nothing may have landed on the subject")
	_, _, ok := lastMsgHeaders(ctx, t, c, bucket, key)
	require.True(t, ok)
}

// TestAtomicBatch_RefusesPurgeBelowTheTTLFloor is the batch half of the same
// guard: a purge op whose TTL is under the server's one-second floor is a
// caller error caught before publish, not a NATS rejection and not a
// permanent marker.
func TestAtomicBatch_RefusesPurgeBelowTheTTLFloor(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	rev, err := c.KVCreate(ctx, bucket, key, []byte(`{"v":1}`))
	require.NoError(t, err)

	for _, ttl := range []time.Duration{0, -time.Second, 999 * time.Millisecond} {
		_, err := c.AtomicBatch(ctx, []BatchOp{{Bucket: bucket, Key: key, Purge: true, TTL: ttl}})
		require.Error(t, err, "TTL=%s must be refused", ttl)
		require.NotErrorIs(t, err, ErrAtomicBatchRejected,
			"the refusal is pre-flight, not a NATS-reported rejection (TTL=%s)", ttl)
	}

	entry, err := c.KVGet(ctx, bucket, key)
	require.NoError(t, err, "nothing may be published by a refused batch")
	require.Equal(t, rev, entry.Revision)

	// A DELETE op in the same batch shape carries no TTL requirement — the
	// floor is a property of the purge marker, not of every removal.
	_, err = c.AtomicBatch(ctx, []BatchOp{{Bucket: bucket, Key: key, Delete: true}})
	require.NoError(t, err, "a plain delete op is unaffected by the purge TTL floor")
}

// TestKVListTombstones_CancelledMidListingParksNoWatcher pins the release
// path. nats.go's watcher sends each entry on a 256-slot channel from a
// callback that holds the watcher's mutex, and the closed handler that closes
// that channel takes the same mutex (jetstream/kv.go:1278-1290, :1335-1338):
// abandoning a listing with the buffer full would leave the client's
// dispatcher blocked in that send for the life of the process. So the bucket
// here carries more markers than the buffer holds, and a cancellation lands
// while they are queued.
func TestKVListTombstones_CancelledMidListingParksNoWatcher(t *testing.T) {
	t.Parallel()
	c, ctx := newPurgeTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	// 2000 delete markers: far past the 256-slot channel, so entries are
	// certain to be queued behind the caller when it walks away. Seeded in
	// batches rather than 4000 round trips.
	const (
		markers   = 2000
		chunkSize = 500
	)
	for base := 0; base < markers; base += chunkSize {
		puts := make([]BatchOp, 0, chunkSize)
		dels := make([]BatchOp, 0, chunkSize)
		for i := base; i < base+chunkSize; i++ {
			key := fmt.Sprintf("sweep.k%04d", i)
			puts = append(puts, BatchOp{Bucket: bucket, Key: key, Value: []byte(`{}`)})
			dels = append(dels, BatchOp{Bucket: bucket, Key: key, Delete: true})
		}
		_, err := c.AtomicBatch(ctx, puts)
		require.NoError(t, err)
		_, err = c.AtomicBatch(ctx, dels)
		require.NoError(t, err)
	}

	full, err := c.KVListTombstones(ctx, bucket, "sweep.>")
	require.NoError(t, err)
	require.Len(t, full, markers, "seed precondition: every marker is listed")

	stream, err := c.JetStream().Stream(ctx, "KV_"+bucket)
	require.NoError(t, err)

	// The cancellation has to land INSIDE the receive loop: one that beats the
	// watcher's create-consumer round trip returns before any entry is queued
	// and exercises nothing. So an attempt waits on a condition rather than a
	// clock — the watcher's ordered consumer appearing on KV_<bucket> means
	// the subscription exists and entries are flowing — and an attempt that
	// still finished first is retried. The interrupted-in-the-loop error
	// message is the proof of where it landed.
	var listErr error
	require.Eventually(t, func() bool {
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		defer cancelAttempt()
		done := make(chan error, 1)
		go func() {
			_, e := c.KVListTombstones(attemptCtx, bucket, "sweep.>")
			done <- e
		}()
		for {
			select {
			case listErr = <-done:
				return false // finished before the cancel could land; retry
			default:
			}
			if ctx.Err() != nil {
				return false
			}
			if info, ierr := stream.Info(ctx); ierr == nil && info.State.Consumers > 0 {
				break
			}
		}
		cancelAttempt()
		select {
		case listErr = <-done:
		case <-time.After(10 * time.Second):
			return false
		}
		return listErr != nil &&
			strings.Contains(listErr.Error(), "interrupted (partial result discarded)")
	}, 60*time.Second, time.Millisecond,
		"a listing cancelled inside its receive loop must return an error, never a short list")
	require.ErrorIs(t, listErr, context.Canceled)

	// And nothing is left parked. The assertion names the exact goroutine
	// state the drain exists to prevent — a nats.go watcher callback blocked
	// on a channel send — rather than a whole-process goroutine count, which
	// is too noisy to distinguish one parked dispatcher from ordinary churn.
	var parked string
	require.Eventually(t, func() bool {
		var found bool
		parked, found = watcherParkedInChanSend()
		return !found
	}, 30*time.Second, 100*time.Millisecond,
		"an abandoned listing must not leave a goroutine blocked in the watcher")
	require.Empty(t, parked)
}

// watcherParkedInChanSend returns the stack of a goroutine blocked on a
// channel send inside nats.go's KV watcher, if one exists. That is the leak
// shape KVListTombstones' drain prevents: the watcher's update callback sends
// each entry on a 256-slot channel while holding the watcher's mutex, so a
// caller that abandons a full listing without draining parks that goroutine
// for the life of the process.
func watcherParkedInChanSend() (string, bool) {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	for _, g := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(g, "[chan send") && strings.Contains(g, "nats.go/jetstream") {
			return g, true
		}
	}
	return "", false
}

// TestAtomicBatch_RefusesDeleteAndPurge proves the mutual exclusion is a
// pre-flight guard: nothing is published, and the failure is not a NATS
// rejection the caller has to decode.
func TestAtomicBatch_RefusesDeleteAndPurge(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	rev, err := c.KVCreate(ctx, bucket, key, []byte(`{"v":1}`))
	require.NoError(t, err)

	_, err = c.AtomicBatch(ctx, []BatchOp{{Bucket: bucket, Key: key, Delete: true, Purge: true}})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAtomicBatchRejected,
		"the refusal is pre-flight, not a NATS-reported rejection")

	entry, err := c.KVGet(ctx, bucket, key)
	require.NoError(t, err, "nothing may be published by a refused batch")
	require.Equal(t, rev, entry.Revision)
}

// TestAtomicBatch_PurgeSkipsValueCheck mirrors the Delete exemption: a purge
// op carries no body and must not be measured against the value ceiling.
func TestAtomicBatch_PurgeSkipsValueCheck(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	_, err := c.KVCreate(ctx, bucket, key, []byte(`{}`))
	require.NoError(t, err)

	ops := []BatchOp{{Bucket: bucket, Key: key, Purge: true, TTL: time.Second, Value: make([]byte, c.valueSizeLimit()+1)}}
	_, err = c.AtomicBatch(ctx, ops)
	require.NoError(t, err, "a purge op's ignored Value must not be measured against the ceiling")
	_, err = c.KVGet(ctx, bucket, key)
	require.ErrorIs(t, err, ErrKeyNotFound)
}

// TestKVListTombstones_ReturnsOnlyDeleteMarkers pins the enumeration a sweep
// reads: DELETE markers with the revision of the marker message, never a live
// key and never a PURGE marker (which is already expiring, so returning it
// would make a sweep re-purge its own output every pass).
func TestKVListTombstones_ReturnsOnlyDeleteMarkers(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	liveKey := VertexKey("identity", testNanoID1)
	delKeyA := VertexKey("identity", testNanoID2)
	delKeyB := VertexKey("identity", testNanoID3)
	purgedKey := VertexKey("role", testNanoID1)

	for _, k := range []string{liveKey, delKeyA, delKeyB, purgedKey} {
		_, err := c.KVCreate(ctx, bucket, k, []byte(`{"seed":true}`))
		require.NoError(t, err)
	}
	require.NoError(t, c.KVDelete(ctx, bucket, delKeyA))
	require.NoError(t, c.KVDelete(ctx, bucket, delKeyB))
	// A long TTL so the purge marker is still standing when the listing runs
	// — its exclusion must come from its operation, not from having expired.
	require.NoError(t, c.KVPurgeWithTTL(ctx, bucket, purgedKey, time.Hour, 0))

	got, err := c.KVListTombstones(ctx, bucket, "vtx.>")
	require.NoError(t, err)

	byKey := map[string]uint64{}
	for _, ts := range got {
		byKey[ts.Key] = ts.Revision
	}
	require.Len(t, byKey, 2, "exactly the two DELETE markers: got %v", byKey)
	for _, k := range []string{delKeyA, delKeyB} {
		hdr, markerSeq, ok := lastMsgHeaders(ctx, t, c, bucket, k)
		require.True(t, ok)
		require.Equal(t, "DEL", hdr.Get("KV-Operation"))
		require.Equal(t, markerSeq, byKey[k],
			"the reported revision must be the marker's own sequence for %s", k)
	}
	require.NotContains(t, byKey, liveKey, "a live key is never a tombstone")
	require.NotContains(t, byKey, purgedKey, "a PURGE marker is not a DELETE tombstone")

	// A conditioned purge at the reported revision converts the marker — the
	// revision is usable, not merely reported.
	require.NoError(t, c.KVPurgeWithTTL(ctx, bucket, delKeyA, time.Minute, byKey[delKeyA]))
	after, err := c.KVListTombstones(ctx, bucket, "vtx.>")
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, delKeyB, after[0].Key)
}

// TestKVListTombstones_EmptyResultIsNotAnError pins that a filter matching
// nothing yields an empty slice, so a caller reads "clean" rather than having
// to classify an error.
func TestKVListTombstones_EmptyResultIsNotAnError(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	_, err := c.KVCreate(ctx, bucket, VertexKey("identity", testNanoID1), []byte(`{}`))
	require.NoError(t, err)

	got, err := c.KVListTombstones(ctx, bucket, "lnk.>")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)

	// A filter that matches only a LIVE key is equally empty.
	got, err = c.KVListTombstones(ctx, bucket, "vtx.>")
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestAtomicBatch_PurgeLeavesSiblingPendingDelivery pins the cost boundary of
// the rollup a purge runs: the server's per-subject purge path walks every
// consumer's pending map and drops entries whose message is gone, but it is
// SUBJECT-scoped — an unacked delivery on a sibling subject under the same
// consumer filter must still be redelivered. Loom's two durables sit on this
// bucket, so a purge that swallowed a sibling's pending message would lose a
// deadline probe or an outbox relay.
func TestAtomicBatch_PurgeLeavesSiblingPendingDelivery(t *testing.T) {
	t.Parallel()
	c, ctx := newPurgeTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const (
		siblingKey = "deadline.keep"
		purgedKey  = "deadline.go"
	)
	_, err := c.KVPut(ctx, bucket, siblingKey, []byte(`{"sibling":true}`))
	require.NoError(t, err)

	stream, err := c.JetStream().Stream(ctx, "KV_"+bucket)
	require.NoError(t, err)
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       "sibling-survival",
		FilterSubject: kvBucketSubject(bucket, "deadline.>"),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Second,
	})
	require.NoError(t, err)

	// Take the sibling's delivery and hold it UNACKED, so it sits in the
	// consumer's pending map while the purge runs.
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
	require.NoError(t, err)
	var held int
	for msg := range batch.Messages() {
		require.Equal(t, kvBucketSubject(bucket, siblingKey), msg.Subject())
		held++
	}
	require.NoError(t, batch.Error())
	require.Equal(t, 1, held, "the sibling's message must be delivered and left unacked")

	_, err = c.KVPut(ctx, bucket, purgedKey, []byte(`{"doomed":true}`))
	require.NoError(t, err)
	_, err = c.AtomicBatch(ctx, []BatchOp{{Bucket: bucket, Key: purgedKey, Purge: true, TTL: time.Second}})
	require.NoError(t, err)

	// The sibling's unacked delivery must come back on the AckWait timer. The
	// predicate only collects it — the ack is an assertion and runs on the
	// test's own goroutine once Eventually has returned.
	var redelivered jetstream.Msg
	require.Eventually(t, func() bool {
		b, ferr := cons.Fetch(5, jetstream.FetchMaxWait(time.Second))
		if ferr != nil {
			return false
		}
		for msg := range b.Messages() {
			if msg.Subject() == kvBucketSubject(bucket, siblingKey) {
				redelivered = msg
			}
		}
		return redelivered != nil
	}, 30*time.Second, 500*time.Millisecond,
		"a purge on one subject must not drop a sibling subject's pending delivery")
	require.NoError(t, redelivered.Ack())
}

// TestAtomicBatch_PurgeHonorsRevisionCondition proves the batch form carries
// the same expected-last-subject-sequence condition a single publish does —
// the guard a conversion pass leans on to never purge a key that was
// re-created between listing and write.
func TestAtomicBatch_PurgeHonorsRevisionCondition(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	stale, err := c.KVCreate(ctx, bucket, key, []byte(`{"v":1}`))
	require.NoError(t, err)
	rev, err := c.KVPut(ctx, bucket, key, []byte(`{"v":2}`))
	require.NoError(t, err)

	_, err = c.AtomicBatch(ctx, []BatchOp{
		{Bucket: bucket, Key: key, Purge: true, TTL: time.Second, HasRevision: true, Revision: stale},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrAtomicBatchRejected)
	entry, err := c.KVGet(ctx, bucket, key)
	require.NoError(t, err, "a stale-revision purge must leave the value")
	require.Equal(t, rev, entry.Revision)

	_, err = c.AtomicBatch(ctx, []BatchOp{
		{Bucket: bucket, Key: key, Purge: true, TTL: time.Second, HasRevision: true, Revision: rev},
	})
	require.NoError(t, err)
	_, err = c.KVGet(ctx, bucket, key)
	require.ErrorIs(t, err, ErrKeyNotFound)
}
