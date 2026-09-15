package substrate

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
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

// awaitRemoval polls the collector for the first REMOVAL delivered on subject,
// selected by shape — an empty body, which every removal has and no value the
// tests here write does — and says nothing about which kind of removal it is.
// Provenance is then a separate assertion on the headers it carries, which is
// the only thing that tells an expiry from a delete or a purge.
//
// Selecting by shape rather than by position is what makes it usable where a
// value may or may not still be replayed ahead of its own removal: a consumer
// attached around the instant the server sweeps sees one or two messages on the
// subject depending on the timing, but exactly one of them is empty-bodied.
func awaitRemoval(t *testing.T, mc *markerCollector, subject, why string) Message {
	t.Helper()
	var got Message
	require.Eventually(t, func() bool {
		for _, m := range mc.forSubject(subject) {
			if len(m.Body) == 0 {
				got = m
				return true
			}
		}
		return false
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
// the deadline watcher's classifier: on a per-key-TTL bucket at the server's
// floor marker TTL (1s — long enough for every removal here to mint its marker,
// short enough for the negative waits to be cheap), the four ways a KV subject
// can lose its value are told apart by the headers the substrate delivers, and
// only ONE of them is the server's expiry.
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

	// 3. An arm the client then purges with a TTL of its own. The purge marker
	// has to outlive the collector's delivery lag: the ordered consumer is
	// started before the writes, but a marker the server has already swept by
	// MaxAge before the consumer fetches it is a marker the collector never
	// sees, and on a loaded runner the two awaits ahead of this one in the
	// assertion order can take longer than a one-second TTL.
	const purgeMarkerTTL = 3 * time.Second
	_, err = c.KVPutWithTTL(ctx, bucket, purgedKey, []byte(`{"setAt":"t0"}`), 2*time.Second)
	require.NoError(t, err)
	require.NoError(t, c.KVPurgeWithTTL(ctx, bucket, purgedKey, purgeMarkerTTL, 0))

	// 4. An arm superseded by a re-arm before its own TTL runs out.
	//
	// This step's proposition is intrinsically timed: proving a superseded TTL
	// cannot fire late means landing the re-arm inside the first arm's TTL AND
	// still watching when that TTL would have come due. Both were previously
	// hoped for — the re-arm implicitly, the observation via a fixed 4 s window
	// that might or might not have reached the instant — and a loaded runner
	// stalled a round trip past the arm's TTL, at which point the marker the arm
	// mints is a legitimate expiry rather than the eviction this pins. Both are
	// now asserted: the arm is timed from before its own put, the re-arm's
	// landing inside the TTL is a checked precondition, and the closing window is
	// computed to outlast the TTL instant rather than assumed to.
	const evictedArmTTL = 10 * time.Second
	evictedArmedAt := time.Now()
	_, err = c.KVPutWithTTL(ctx, bucket, evictedKey, []byte(`{"setAt":"t0"}`), evictedArmTTL)
	require.NoError(t, err)
	_, err = c.KVPutWithTTL(ctx, bucket, evictedKey, []byte(`{"setAt":"t1"}`), 30*time.Second)
	require.NoError(t, err)
	require.Less(t, time.Since(evictedArmedAt), evictedArmTTL,
		"the re-arm has to land inside the arm's own TTL or the arm expires for real and this step "+
			"pins nothing — a stall this long is host contention, not the marker mechanism")

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
	// marker's TTL and the arm's original 2s TTL.
	purge := awaitMarker(t, mc, purgedSubj, 2, "the client purge must deliver a marker")
	require.Empty(t, purge.Body)
	require.Equal(t, KVOperationPurge, purge.Header(KVOperationHeader))
	require.Empty(t, purge.Header(MarkerReasonHeader),
		"a client purge is not an expiry and must carry no marker reason")
	requireNoFurtherMarker(t, mc, purgedSubj, 2, purgeMarkerTTL+2*time.Second,
		"an expiring purge marker must not be re-marked as a MaxAge expiry")

	// The evicted arm: the re-arm's own value is the only thing after it. The
	// superseded 3s TTL must produce nothing, or an earlier step's deadline
	// could fire after the cursor advanced.
	awaitMarker(t, mc, evictedSubj, 2, "the re-arm's value must be delivered")
	require.Len(t, mc.forSubject(evictedSubj), 2, "the re-arm's value follows the arm's")
	// The window has to still be open when the superseded TTL would have come
	// due, or a quiet subject proves only that the instant had not arrived. Hold
	// it open to two seconds past that instant, and never shorten it below the
	// original fixed period.
	evictedWindow := time.Until(evictedArmedAt.Add(evictedArmTTL + 2*time.Second))
	if evictedWindow < 4*time.Second {
		evictedWindow = 4 * time.Second
	}
	requireNoFurtherMarker(t, mc, evictedSubj, 2, evictedWindow,
		"an arm evicted by a re-arm must never emit its own expiry marker")

	for _, m := range mc.forSubject(evictedSubj) {
		require.NotEqual(t, MarkerReasonMaxAge, m.Header(MarkerReasonHeader))
	}
}

// provisionMarkerTTLBucket creates a KV bucket whose removal markers live
// markerTTL, the shape ProvisionBuckets gives a platform bucket from its
// registry row, and pins the two stream properties the tests below reason
// about: the marker lifetime the bucket asked for, and History 1
// (MaxMsgsPerSubject, the nats.go KV default — jetstream/kv.go:619-625, 672).
func provisionMarkerTTLBucket(ctx context.Context, t *testing.T, c *Conn, bucket string, markerTTL time.Duration) {
	t.Helper()
	js := c.JetStream()
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket, LimitMarkerTTL: markerTTL})
	require.NoError(t, err)

	stream, err := js.Stream(ctx, "KV_"+bucket)
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.Equal(t, markerTTL, info.Config.SubjectDeleteMarkerTTL,
		"the bucket's marker lifetime is what these tests measure")
	require.EqualValues(t, 1, info.Config.MaxMsgsPerSubject,
		"these tests read the History-1 regime; above 1 the server's per-message TTL floor applies")
}

// maxAgeMarkerStands reports whether the newest message on a key's KV subject
// is the server's expiry marker, keyed on the reason header rather than on the
// message's shape — an expiry, a delete and a purge are all empty-bodied, and
// only the header tells them apart.
//
// It asserts nothing and takes no *testing.T so it can be polled from inside a
// require.Eventually predicate: a read that fails concludes nothing and
// reports false, so a wait keeps waiting and fails on its own timeout message
// rather than aborting a goroutine testify does not own.
func maxAgeMarkerStands(ctx context.Context, c *Conn, bucket, key string) bool {
	stream, err := c.JetStream().Stream(ctx, "KV_"+bucket)
	if err != nil {
		return false
	}
	msg, err := stream.GetLastMsgForSubject(ctx, kvBucketSubject(bucket, key))
	if err != nil {
		return false
	}
	return msg.Header.Get(MarkerReasonHeader) == MarkerReasonMaxAge
}

// newMarkerWindowConn is newPurgeTestConn with a budget sized for a test that
// waits out a whole marker lifetime end to end — a wait for the expiry, a live
// observation window, and a wait for the marker's own expiry, each of which
// carries a slow-host budget of its own. The connection's context must outlast
// their sum rather than force any leg to be trimmed to fit.
func newMarkerWindowConn(t *testing.T) (*Conn, context.Context) {
	t.Helper()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	c, err := Connect(ctx, ConnectOpts{URL: url, Name: "substrate-marker-window-test"})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c, ctx
}

// TestKVMarkerProvenance_MarkerLifetimeIsTheBucketsMarkerTTL pins the fact
// loom-state's delivery window rests on: a MaxAge marker's lifetime IS the
// bucket's LimitMarkerTTL (nats-server v2.14.0 server/stream.go:109-111), so a
// consumer that is not connected at the instant a key expires can still be
// handed the expiry for as long as the bucket says — and no longer. A bucket
// that declares nothing gets the server's floor, one second
// (server/stream.go:1768-1771), and gives its expiry consumer one second to be
// alive in; a bucket that asks for more gets exactly what it asked for, which
// is the whole of what bootstrap.LoomStateMarkerTTL buys Loom's deadline
// watcher.
//
// The lifetime is read off the marker rather than measured: the server stamps
// the marker it mints with the bucket's own SubjectDeleteMarkerTTL in the
// Nats-TTL header (filestore.go:6948-6963), so the marker states its window
// itself and the assertion holds however long the host takes to deliver it.
func TestKVMarkerProvenance_MarkerLifetimeIsTheBucketsMarkerTTL(t *testing.T) {
	t.Parallel()
	c, ctx := newMarkerWindowConn(t)
	const (
		bucket = "loom-state"
		key    = "deadline.windowedArm"
		// A whole number of seconds: the server carries the marker TTL as
		// seconds, so the header it stamps is this value exactly.
		markerTTL = 60 * time.Second
		armTTL    = time.Second
		// Observed live, and a small fraction of markerTTL — a host that takes
		// tens of seconds to deliver the marker still leaves most of the
		// marker's life for the window to sit inside.
		observed = 2 * time.Second
	)
	provisionMarkerTTLBucket(ctx, t, c, bucket, markerTTL)

	_, err := c.KVPutWithTTL(ctx, bucket, key, []byte(`{"setAt":"t0"}`), armTTL)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return maxAgeMarkerStands(ctx, c, bucket, key) },
		30*time.Second, 50*time.Millisecond,
		"the arm's own TTL must expire it into a MaxAge marker")

	// What the marker says about itself: a MaxAge expiry that will stand for
	// the bucket's marker TTL — sixty times the floor a bucket inherits when
	// it states no value of its own.
	hdr, _, ok := lastMsgHeaders(ctx, t, c, bucket, key)
	require.True(t, ok, "the expiry leaves a marker on the subject")
	require.Equal(t, MarkerReasonMaxAge, hdr.Get(MarkerReasonHeader),
		"what stands on the subject is the server's expiry, named by its reason header")
	require.Equal(t, markerTTL.String(), hdr.Get("Nats-TTL"),
		"the marker's own lifetime is the bucket's marker TTL — the window in which an expiry stays observable")

	// Corroboration on the live stream: the subject really does keep standing
	// once the value is gone, so the window is retention a consumer can come
	// back into and not merely a header.
	require.Never(t, func() bool { return !subjectPresent(ctx, c, bucket, key) },
		observed, 50*time.Millisecond,
		"the expiry marker must stand for the bucket's marker TTL, not vanish with the value")

	// And the window closes: a marker is not durable, so a bucket buys exactly
	// the lifetime it declares. Waiting one out belongs on a short-marker
	// bucket, where the wait is for an absence — a slow host can only delay
	// that, never falsify it, and the budget sits many times above the TTL it
	// waits on.
	const (
		expiring = "loom-state-brief"
		briefTTL = 3 * time.Second
	)
	provisionMarkerTTLBucket(ctx, t, c, expiring, briefTTL)
	_, err = c.KVPutWithTTL(ctx, expiring, key, []byte(`{"setAt":"t0"}`), armTTL)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !subjectPresent(ctx, c, expiring, key) },
		40*time.Second, 100*time.Millisecond,
		"the subject must drop once the marker's own TTL elapses")
}

// newRecoveryTestConn starts an embedded server on a JetStream store the test
// owns for its whole life — not the server's — and connects to it, returning the
// restart handle so the test can bring that server down and a fresh one up on the
// same store. The budget outlasts a whole down-and-up cycle: an arm's lifetime
// spent with nothing serving, plus the slow-host budget of every wait after
// recovery.
//
// Each server has its own client URL, so the returned *Conn is only valid until
// the handle is stopped; after a restart the test connects again through
// connectRecoveryConn against the new server.
func newRecoveryTestConn(t *testing.T) (*natsfixture.RestartableServer, *Conn, context.Context) {
	t.Helper()
	r := natsfixture.StartRestartableServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	return r, connectRecoveryConn(ctx, t, r.Server().ClientURL()), ctx
}

// connectRecoveryConn opens a substrate connection to one particular embedded
// server, closing it at test end.
func connectRecoveryConn(ctx context.Context, t *testing.T, url string) *Conn {
	t.Helper()
	c, err := Connect(ctx, ConnectOpts{URL: url, Name: "substrate-recovery-test"})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

// TestKVMarkerProvenance_TTLPastDueAtRecoveryStillMintsAMaxAgeMarker pins the
// premise Loom's deadline watcher rests on across a server restart: an arm whose
// per-key TTL falls due while NOTHING IS SERVING is not quietly dropped and not
// silently kept — it is expired with a MaxAge marker once the server comes back,
// and that marker is delivered to a watcher that attaches after recovery. A
// deadline is therefore never lost to a substrate outage; it arrives late, on the
// recovery clock, rather than never.
//
// Three pieces of nats-server v2.14.0 compose to that, and a vendor bump that
// changes any of them reds here:
//
//   - fileStore.expireMsgsOnRecover returns early when the stream configures
//     subject-delete markers (server/filestore.go:2515-2523), so recovery itself
//     expires nothing — the arm is still on the stream when the server opens;
//   - fileStore.recoverTTLState rebuilds the timed hash wheel from the store and
//     schedules the age check immediately, `defer fs.resetAgeChk(0)`
//     (:2148-2173) — so the past-due deadline is looked at, not left until some
//     later write;
//   - fileStore.expireMsgs drains every past-due wheel entry and
//     handleRemovalOrSdm stamps each emptied subject's marker with
//     JSMarkerReasonMaxAge (:6790-6905, :6948-6966) — so what the watcher
//     receives is an expiry by provenance, indistinguishable from one that fired
//     while the server was up.
//
// Were the first of those to change, the arm would be expired during recovery
// with no marker minted and the deadline would vanish; were the second to
// change, the marker would wait for unrelated traffic on the stream.
func TestKVMarkerProvenance_TTLPastDueAtRecoveryStillMintsAMaxAgeMarker(t *testing.T) {
	t.Parallel()
	r, c, ctx := newRecoveryTestConn(t)
	const (
		bucket = "loom-state"
		key    = "deadline.armedAcrossRestart"
		// Long enough that the marker minted at recovery is still standing
		// when the late watcher attaches, however slow the host.
		markerTTL = 60 * time.Second
		// Above the server's one-second floor, and generous enough that the
		// put and the shutdown cannot plausibly consume it — the arm must
		// still be live when the server goes down, or the expiry would be a
		// live one and the test would pin nothing.
		armTTL = 4 * time.Second
		// How far past armedAt+armTTL the server stays down. It covers the put's
		// own round trip (the server's deadline is stamped that much after
		// armedAt) and leaves recovery an unambiguously past-due wheel entry.
		downOvershoot = time.Second
	)
	provisionMarkerTTLBucket(ctx, t, c, bucket, markerTTL)

	// Read before the put, so armedAt is provably no later than the instant the
	// server stamped the arm: the deadline the server holds is then at or after
	// armedAt+armTTL, which makes the liveness guard below sound rather than
	// approximate.
	armedAt := time.Now()
	_, err := c.KVPutWithTTL(ctx, bucket, key, []byte(`{"setAt":"t0"}`), armTTL)
	require.NoError(t, err)
	_, err = c.KVGet(ctx, bucket, key)
	require.NoError(t, err, "the arm must be live before the server goes down")

	// Down, and fully down: Stop does not return until the server has finished
	// shutting down and released the store.
	r.Stop()
	require.WithinDuration(t, armedAt, time.Now(), armTTL,
		"the arm's TTL must still be in the future when the server stops — otherwise the "+
			"expiry happened live and nothing here is about recovery")

	// The only wait in this test that is a DURATION rather than a condition,
	// and necessarily so: the deadline is absolute, and there is no server to
	// poll while it passes. Nothing is being synchronised on — every wait for
	// an event below polls a condition.
	deadline := armedAt.Add(armTTL + downOvershoot)
	time.Sleep(time.Until(deadline))

	// Up again, on the same store, at a new URL — so the connection above is
	// finished and the test needs one of its own.
	second := r.Start()
	c2 := connectRecoveryConn(ctx, t, second.ClientURL())

	// The watcher exists only now, after recovery — Loom's shape when the
	// substrate restarts under it.
	subj := kvBucketSubject(bucket, key)
	mc := startMarkerCollector(ctx, t, c2, bucket, kvBucketSubject(bucket, "deadline.>"))

	// The removal arrives at all: a server that carried the arm forward
	// unexpired, or that dropped it during recovery without minting anything,
	// delivers nothing here and is caught by this wait.
	got := awaitRemoval(t, mc, subj,
		"the arm whose TTL fell due while the server was down must be removed after recovery")
	require.Equal(t, subj, got.Subject, "the removal stands on the armed key's own subject")

	// And the removal is an EXPIRY by provenance — minted on the recovery clock,
	// but indistinguishable from one that fired while the server was up. This is
	// the whole of what the deadline watcher admits on.
	require.Equal(t, MarkerReasonMaxAge, got.Header(MarkerReasonHeader),
		"an expiry that fell due during downtime is minted at recovery as a MaxAge marker, not as a bare removal")
	require.Empty(t, got.Header(KVOperationHeader),
		"a server-minted expiry carries no KV-Operation — that header means a client asked")

	_, err = c2.KVGet(ctx, bucket, key)
	require.ErrorIs(t, err, ErrKeyNotFound, "the arm reads absent after recovery")
}

// TestKVMarkerProvenance_ShortPerKeyTTLNotRaisedToMarkerTTL pins the exception
// every per-key TTL in the platform rides on: the server raises a per-message
// TTL below the stream's SubjectDeleteMarkerTTL up to it, EXCEPT when
// MaxMsgsPer == 1 (nats-server v2.14.0 server/stream.go:6890-6897). KV buckets
// get MaxMsgsPerSubject = History = 1, so a short arm on a bucket with a long
// marker TTL expires on its own schedule.
//
// Without the exception, loom-state's 60s step deadline would fire only after
// the bucket's marker TTL — an hour — and the tombstone sweep's short TTL'd
// purges would linger just as long. This proves it against the pinned server
// rather than reading it from the server's source.
func TestKVMarkerProvenance_ShortPerKeyTTLNotRaisedToMarkerTTL(t *testing.T) {
	t.Parallel()
	c, ctx := newPurgeTestConn(t)
	const (
		bucket    = "loom-state"
		key       = "deadline.shortArm"
		markerTTL = 60 * time.Second
		armTTL    = time.Second
		// Three times the arm it waits on and half the marker TTL it
		// discriminates against: a host stall inside this budget cannot fail a
		// correct server, and a raised TTL cannot pass inside it.
		budget = 30 * time.Second
	)
	provisionMarkerTTLBucket(ctx, t, c, bucket, markerTTL)

	_, err := c.KVPutWithTTL(ctx, bucket, key, []byte(`{"setAt":"t0"}`), armTTL)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return maxAgeMarkerStands(ctx, c, bucket, key) },
		budget, 50*time.Millisecond,
		"a %v arm must expire on its own schedule: were it raised to the bucket's %v marker TTL, "+
			"nothing would happen inside this budget", armTTL, markerTTL)

	_, err = c.KVGet(ctx, bucket, key)
	require.ErrorIs(t, err, ErrKeyNotFound, "the expired arm must read absent")

	hdr, _, ok := lastMsgHeaders(ctx, t, c, bucket, key)
	require.True(t, ok, "the expiry leaves a marker on the subject")
	require.Equal(t, MarkerReasonMaxAge, hdr.Get(MarkerReasonHeader),
		"what stands on the subject is the server's expiry, named by its reason header")
	require.Empty(t, hdr.Get(KVOperationHeader),
		"a server-minted expiry carries no KV-Operation — that header means a client asked")
}

// TestKVMarkerProvenance_ExpiryReachesAWatcherAttachedAfterIt is the
// composition the marker TTL exists for: the key expires with NOTHING attached
// to the bucket's stream, and a loom-deadline-shaped durable created only
// afterwards is still handed the MaxAge marker. Delivery of an expiry is a
// replay out of retention, not an event a listener has to be present for — so
// a watcher that was restarting, deploying or reconnecting at the instant a
// deadline fired still learns the step was rejected or lost.
//
// The window that composition gets is the bucket's marker TTL, pinned by
// TestKVMarkerProvenance_MarkerLifetimeIsTheBucketsMarkerTTL; this test pins
// the replay itself, and is deliberately sized so no leg races that window: the
// marker outlives every budget here by a wide margin, so a slow host delays the
// test rather than failing it.
func TestKVMarkerProvenance_ExpiryReachesAWatcherAttachedAfterIt(t *testing.T) {
	t.Parallel()
	c, ctx := newMarkerWindowConn(t)
	const (
		bucket    = "loom-state"
		key       = "deadline.armedWithNoWatcher"
		markerTTL = 60 * time.Second
		armTTL    = time.Second
	)
	provisionMarkerTTLBucket(ctx, t, c, bucket, markerTTL)

	_, err := c.KVPutWithTTL(ctx, bucket, key, []byte(`{"setAt":"t0"}`), armTTL)
	require.NoError(t, err)

	// Nothing consumes this stream yet: the expiry happens unwitnessed.
	require.Eventually(t, func() bool { return maxAgeMarkerStands(ctx, c, bucket, key) },
		30*time.Second, 50*time.Millisecond,
		"the arm must expire into a marker while no consumer exists")

	// Only now does the watcher exist — a durable in loom-deadline's shape:
	// DeliverAll over the bucket's deadline.> subjects, explicit ack.
	stream, err := c.JetStream().Stream(ctx, "KV_"+bucket)
	require.NoError(t, err)
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:        "loom-deadline",
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{kvBucketSubject(bucket, "deadline.>")},
	})
	require.NoError(t, err)

	raw, err := cons.Next(jetstream.FetchMaxWait(30 * time.Second))
	require.NoError(t, err, "a durable created after the expiry must still be handed the marker")
	require.NoError(t, raw.Ack())

	// Read through the same Message view a handler gets, and admit it the way
	// the deadline watcher does — on the reason header, never on the shape.
	got := newMessage(raw)
	require.Equal(t, kvBucketSubject(bucket, key), got.Subject,
		"the replayed message is the armed key's own subject")
	require.Equal(t, MarkerReasonMaxAge, got.Header(MarkerReasonHeader),
		"what the late watcher receives is the server's expiry, named by its reason header")
	require.Empty(t, got.Header(KVOperationHeader),
		"a server-minted expiry carries no KV-Operation — that header means a client asked")
	require.Empty(t, got.Body, "an expiry marker carries no body")
}
