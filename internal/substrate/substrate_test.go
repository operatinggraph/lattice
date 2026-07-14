package substrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"
)

// startEmbeddedNATS runs a JetStream-enabled NATS server in-process and
// returns the connection URL. Cleanup is registered via t.Cleanup.
func startEmbeddedNATS(t *testing.T) (url string) {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = jsstore.Dir(t)
	s := natsserver.RunServer(&opts)
	t.Cleanup(func() {
		if jsCfg := s.JetStreamConfig(); jsCfg != nil {
			defer os.RemoveAll(jsCfg.StoreDir)
		}
		s.Shutdown()
		_ = server.VERSION // silence unused
	})
	return s.ClientURL()
}

// provisionCoreBucket mirrors the bootstrap's Core KV provisioning:
// LimitMarkerTTL (=> AllowMsgTTL) and AllowAtomicPublish on the underlying
// stream.
func provisionCoreBucket(ctx context.Context, t *testing.T, c *Conn, bucket string) {
	t.Helper()
	js := c.JetStream()
	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         bucket,
		LimitMarkerTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("create KV bucket %q: %v", bucket, err)
	}
	streamName := "KV_" + bucket
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("get stream %q: %v", streamName, err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("enable AllowAtomicPublish: %v", err)
	}
}

func newTestConn(t *testing.T) (*Conn, context.Context) {
	t.Helper()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c, err := Connect(ctx, ConnectOpts{URL: url, Name: "substrate-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.Close)
	return c, ctx
}

func TestKV_PutGetCreateUpdateDelete(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	val := []byte(`{"hello":"world"}`)

	// Get missing → ErrKeyNotFound.
	if _, err := c.KVGet(ctx, bucket, key); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}

	// Create.
	rev1, err := c.KVCreate(ctx, bucket, key, val)
	if err != nil {
		t.Fatalf("KVCreate: %v", err)
	}
	if rev1 == 0 {
		t.Fatalf("Create returned revision 0")
	}

	// Create same key again → ErrRevisionConflict.
	if _, err := c.KVCreate(ctx, bucket, key, val); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on duplicate Create, got %v", err)
	}

	// Get back the entry.
	entry, err := c.KVGet(ctx, bucket, key)
	if err != nil {
		t.Fatalf("KVGet: %v", err)
	}
	if entry.Key != key || string(entry.Value) != string(val) || entry.Revision != rev1 {
		t.Fatalf("KVGet mismatch: %+v", entry)
	}

	// Update with wrong revision.
	if _, err := c.KVUpdate(ctx, bucket, key, []byte(`{"v":2}`), rev1+9); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on wrong-rev Update, got %v", err)
	}

	// Update with correct revision.
	rev2, err := c.KVUpdate(ctx, bucket, key, []byte(`{"v":2}`), rev1)
	if err != nil {
		t.Fatalf("KVUpdate ok-path: %v", err)
	}
	if rev2 <= rev1 {
		t.Fatalf("revision did not advance: %d -> %d", rev1, rev2)
	}

	// Plain Put.
	rev3, err := c.KVPut(ctx, bucket, key, []byte(`{"v":3}`))
	if err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	if rev3 <= rev2 {
		t.Fatalf("Put revision did not advance: %d -> %d", rev2, rev3)
	}

	// Delete -> subsequent get returns ErrKeyNotFound.
	if err := c.KVDelete(ctx, bucket, key); err != nil {
		t.Fatalf("KVDelete: %v", err)
	}
	if _, err := c.KVGet(ctx, bucket, key); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after Delete, got %v", err)
	}
}

// TestKVCreateWithTTL proves the per-key-TTL create: CAS semantics hold (a
// second create conflicts while the key lives), the key self-expires at the
// TTL, and the CAS stays tombstone-aware (create-after-soft-delete succeeds).
// The bucket is LimitMarkerTTL-provisioned (provisionCoreBucket), the
// per-key-TTL prerequisite.
func TestKVCreateWithTTL(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	val := []byte(`{"hello":"ttl"}`)

	// Create with the NATS-floor TTL.
	rev, err := c.KVCreateWithTTL(ctx, bucket, key, val, time.Second)
	if err != nil {
		t.Fatalf("KVCreateWithTTL: %v", err)
	}
	if rev == 0 {
		t.Fatalf("Create returned revision 0")
	}

	// CAS holds while the key lives: a duplicate create conflicts.
	if _, err := c.KVCreateWithTTL(ctx, bucket, key, val, time.Second); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on duplicate create, got %v", err)
	}

	// The key self-expires: poll until the server's TTL deletion lands.
	deadline := time.Now().Add(10 * time.Second)
	expired := false
	for time.Now().Before(deadline) {
		if _, err := c.KVGet(ctx, bucket, key); errors.Is(err, ErrKeyNotFound) {
			expired = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !expired {
		t.Fatalf("key %q never expired via its per-key TTL", key)
	}

	// Tombstone-aware CAS: a soft-deleted key accepts a fresh TTL create.
	key2 := VertexKey("identity", testNanoID2)
	if _, err := c.KVCreate(ctx, bucket, key2, val); err != nil {
		t.Fatalf("KVCreate %q: %v", key2, err)
	}
	if err := c.KVDelete(ctx, bucket, key2); err != nil {
		t.Fatalf("KVDelete %q: %v", key2, err)
	}
	if _, err := c.KVCreateWithTTL(ctx, bucket, key2, val, time.Second); err != nil {
		t.Fatalf("create-after-soft-delete with TTL must succeed, got %v", err)
	}

	// ttl <= 0 falls back to a plain create (no expiry header, CAS preserved).
	key3 := VertexKey("identity", testNanoID3)
	if _, err := c.KVCreateWithTTL(ctx, bucket, key3, val, 0); err != nil {
		t.Fatalf("KVCreateWithTTL(ttl=0): %v", err)
	}
	if _, err := c.KVCreateWithTTL(ctx, bucket, key3, val, 0); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on duplicate ttl=0 create, got %v", err)
	}
}

// TestKVUpdateWithTTL proves the revision-conditioned TTL update: the write
// lands only at the expected revision (a stale revision conflicts and leaves
// the entry intact), the update RE-ARMS the per-key TTL (the new entry's TTL
// governs, superseding the prior entry's), and ttl <= 0 falls back to a plain
// revision-conditioned update.
func TestKVUpdateWithTTL(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	rev, err := c.KVCreateWithTTL(ctx, bucket, key, []byte(`{"n":1}`), time.Hour)
	if err != nil {
		t.Fatalf("KVCreateWithTTL: %v", err)
	}

	// A stale revision conflicts and leaves the live entry untouched.
	if _, err := c.KVUpdateWithTTL(ctx, bucket, key, []byte(`{"n":0}`), rev+1, time.Second); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on stale update, got %v", err)
	}
	entry, err := c.KVGet(ctx, bucket, key)
	if err != nil || string(entry.Value) != `{"n":1}` {
		t.Fatalf("a conflicted update must leave the entry intact (err=%v value=%s)", err, entry.Value)
	}

	// At the live revision the update lands and re-arms the TTL: the entry
	// was created with a 1h TTL but expires at the UPDATE's 1s TTL.
	rev2, err := c.KVUpdateWithTTL(ctx, bucket, key, []byte(`{"n":2}`), rev, time.Second)
	if err != nil {
		t.Fatalf("KVUpdateWithTTL: %v", err)
	}
	if rev2 <= rev {
		t.Fatalf("update revision %d must exceed the prior revision %d", rev2, rev)
	}
	entry, err = c.KVGet(ctx, bucket, key)
	if err != nil || string(entry.Value) != `{"n":2}` {
		t.Fatalf("updated value not visible (err=%v value=%s)", err, entry.Value)
	}
	deadline := time.Now().Add(10 * time.Second)
	expired := false
	for time.Now().Before(deadline) {
		if _, err := c.KVGet(ctx, bucket, key); errors.Is(err, ErrKeyNotFound) {
			expired = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !expired {
		t.Fatalf("key %q never expired via the update's re-armed TTL", key)
	}

	// ttl <= 0 falls back to a plain revision-conditioned update (no expiry).
	key2 := VertexKey("identity", testNanoID2)
	rev, err = c.KVCreate(ctx, bucket, key2, []byte(`{"n":1}`))
	if err != nil {
		t.Fatalf("KVCreate %q: %v", key2, err)
	}
	if _, err := c.KVUpdateWithTTL(ctx, bucket, key2, []byte(`{"n":2}`), rev+1, 0); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on stale ttl=0 update, got %v", err)
	}
	if _, err := c.KVUpdateWithTTL(ctx, bucket, key2, []byte(`{"n":2}`), rev, 0); err != nil {
		t.Fatalf("KVUpdateWithTTL(ttl=0): %v", err)
	}
}

// TestKVPutWithTTL proves the raw-header TTL path: the entry self-expires at
// the TTL and is visible (and re-writable) before then, while ttl<=0 falls
// back to a plain unconditional KVPut (no expiry, preserved by CAS on a
// subsequent KVCreate of the same key only after it actually expires).
func TestKVPutWithTTL(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	seq, err := c.KVPutWithTTL(ctx, bucket, key, []byte(`{"n":1}`), time.Second)
	if err != nil {
		t.Fatalf("KVPutWithTTL: %v", err)
	}
	if seq == 0 {
		t.Fatalf("KVPutWithTTL returned sequence 0")
	}
	entry, err := c.KVGet(ctx, bucket, key)
	if err != nil || string(entry.Value) != `{"n":1}` {
		t.Fatalf("KVGet after KVPutWithTTL: entry=%v err=%v", entry, err)
	}

	deadline := time.Now().Add(10 * time.Second)
	expired := false
	for time.Now().Before(deadline) {
		if _, err := c.KVGet(ctx, bucket, key); errors.Is(err, ErrKeyNotFound) {
			expired = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !expired {
		t.Fatalf("key %q never expired via KVPutWithTTL's TTL", key)
	}

	// ttl <= 0 falls back to a plain KVPut (no expiry header).
	key2 := VertexKey("identity", testNanoID2)
	if _, err := c.KVPutWithTTL(ctx, bucket, key2, []byte(`{"n":1}`), 0); err != nil {
		t.Fatalf("KVPutWithTTL(ttl=0): %v", err)
	}
	entry2, err := c.KVGet(ctx, bucket, key2)
	if err != nil || string(entry2.Value) != `{"n":1}` {
		t.Fatalf("KVGet after KVPutWithTTL(ttl=0): entry=%v err=%v", entry2, err)
	}
}

// TestKVDeleteRevision proves the optimistic-concurrency delete: a stale
// expectedRevision conflicts and leaves the entry live, while the current
// revision deletes it (subsequent KVGet returns ErrKeyNotFound).
func TestKVDeleteRevision(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	rev, err := c.KVCreate(ctx, bucket, key, []byte(`{"n":1}`))
	if err != nil {
		t.Fatalf("KVCreate: %v", err)
	}

	// A stale expected revision conflicts and leaves the entry live.
	if err := c.KVDeleteRevision(ctx, bucket, key, rev+1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict on stale delete-revision, got %v", err)
	}
	if _, err := c.KVGet(ctx, bucket, key); err != nil {
		t.Fatalf("a conflicted delete-revision must leave the entry intact, got %v", err)
	}

	// The matching revision deletes.
	if err := c.KVDeleteRevision(ctx, bucket, key, rev); err != nil {
		t.Fatalf("KVDeleteRevision at the live revision: %v", err)
	}
	if _, err := c.KVGet(ctx, bucket, key); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("expected ErrKeyNotFound after KVDeleteRevision, got %v", err)
	}
}

func TestAtomicBatch_Commits(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	keyVtx := VertexKey("identity", testNanoID1)
	keyAsp := AspectKey(keyVtx, "email")
	keyOp := VertexKey("op", testNanoID3)

	ops := []BatchOp{
		{Bucket: bucket, Key: keyVtx, Value: []byte(`{"class":"identity"}`), CreateOnly: true},
		{Bucket: bucket, Key: keyAsp, Value: []byte(`{"class":"email"}`), CreateOnly: true},
		{Bucket: bucket, Key: keyOp, Value: []byte(`{"class":"op"}`), CreateOnly: true, TTL: 3 * time.Second},
	}
	ack, err := c.AtomicBatch(ctx, ops)
	if err != nil {
		t.Fatalf("AtomicBatch: %v", err)
	}
	if ack.Count != 3 {
		t.Fatalf("ack.Count = %d, want 3", ack.Count)
	}

	// All three present, and the derived per-key revision must match the
	// revision the KV API reports on read-back. This proves the
	// contiguous-sequence + revision==stream-sequence premise behind
	// BatchAck.Revisions on live NATS.
	if ack.Revisions == nil {
		t.Fatalf("ack.Revisions is nil; expected derived per-key revisions")
	}
	for _, k := range []string{keyVtx, keyAsp, keyOp} {
		entry, err := c.KVGet(ctx, bucket, k)
		if err != nil {
			t.Fatalf("post-batch KVGet %q: %v", k, err)
		}
		got, ok := ack.Revisions[k]
		if !ok {
			t.Fatalf("ack.Revisions missing key %q", k)
		}
		if entry.Revision != got {
			t.Fatalf("revision mismatch for %q: KV API=%d ack.Revisions=%d", k, entry.Revision, got)
		}
	}
}

// TestAtomicBatch_DeleteMarkerInBatch proves a BatchOp{Delete:true} removes a
// key within the same atomic batch as other puts (Contract #10 §10.3 Loom step
// transition: update instance.<id> + write the new token + delete the prior
// token, all-or-nothing).
func TestAtomicBatch_DeleteMarkerInBatch(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	oldKey := VertexKey("op", testNanoID1)
	newKey := VertexKey("op", testNanoID2)
	if _, err := c.KVCreate(ctx, bucket, oldKey, []byte(`{"class":"op"}`)); err != nil {
		t.Fatalf("seed oldKey: %v", err)
	}

	ops := []BatchOp{
		{Bucket: bucket, Key: newKey, Value: []byte(`{"class":"op"}`), CreateOnly: true},
		{Bucket: bucket, Key: oldKey, Delete: true},
	}
	if _, err := c.AtomicBatch(ctx, ops); err != nil {
		t.Fatalf("AtomicBatch with delete: %v", err)
	}

	if _, err := c.KVGet(ctx, bucket, newKey); err != nil {
		t.Fatalf("newKey must be present after batch: %v", err)
	}
	if _, err := c.KVGet(ctx, bucket, oldKey); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("oldKey must be deleted by the batch: got err=%v", err)
	}
}

func TestAtomicBatch_AllOrNothing(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	keyA := VertexKey("identity", testNanoID1)
	keyB := VertexKey("identity", testNanoID2)

	// Seed keyA so its revision is known.
	revA, err := c.KVCreate(ctx, bucket, keyA, []byte(`{"v":"initial"}`))
	if err != nil {
		t.Fatalf("seed keyA: %v", err)
	}

	// Submit a batch that updates keyA with a deliberately wrong revision
	// (revA+9) and creates keyB. Whole batch must be rejected.
	ops := []BatchOp{
		{Bucket: bucket, Key: keyA, Value: []byte(`{"v":"updated"}`), HasRevision: true, Revision: revA + 9},
		{Bucket: bucket, Key: keyB, Value: []byte(`{"v":"new"}`), CreateOnly: true},
	}
	_, err = c.AtomicBatch(ctx, ops)
	if err == nil {
		t.Fatalf("expected AtomicBatch rejection")
	}
	if !errors.Is(err, ErrAtomicBatchRejected) {
		t.Fatalf("expected ErrAtomicBatchRejected, got %v", err)
	}

	// keyB must NOT exist (no partial commit).
	if _, err := c.KVGet(ctx, bucket, keyB); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("partial commit detected — keyB present after rejected batch: %v", err)
	}
	// keyA must still be at original revision.
	entry, err := c.KVGet(ctx, bucket, keyA)
	if err != nil {
		t.Fatalf("post-reject KVGet keyA: %v", err)
	}
	if entry.Revision != revA {
		t.Fatalf("keyA revision changed despite rejection: %d -> %d", revA, entry.Revision)
	}
}

func TestAtomicBatch_RejectsCrossBucket(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	ops := []BatchOp{
		{Bucket: "a", Key: "k1", Value: []byte(`x`)},
		{Bucket: "b", Key: "k2", Value: []byte(`y`)},
	}
	_, err := c.AtomicBatch(ctx, ops)
	if err == nil || !contains(err.Error(), "cross-bucket") {
		t.Fatalf("expected cross-bucket error, got %v", err)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// fmt.Println sentinel — keeps unused import gone if fmt is dropped.
var _ = fmt.Println

func TestKVListKeysPrefix(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	// Two live links + one SOFT-tombstoned link under lnk.object., plus an
	// unrelated vertex key under a different prefix.
	live1 := "lnk.object." + testNanoID1 + ".photoOf.identity." + testNanoID2
	live2 := "lnk.object." + testNanoID2 + ".signedLeaseOf.leaseapp." + testNanoID1
	soft := "lnk.object." + testNanoID2 + ".photoOf.identity." + testNanoID1
	other := VertexKey("identity", testNanoID1)

	if _, err := c.KVPut(ctx, bucket, live1, []byte(`{"isDeleted":false}`)); err != nil {
		t.Fatalf("put live1: %v", err)
	}
	if _, err := c.KVPut(ctx, bucket, live2, []byte(`{"isDeleted":false}`)); err != nil {
		t.Fatalf("put live2: %v", err)
	}
	// Soft tombstone = a normal KV entry whose body carries isDeleted:true (the
	// Processor commit shape). It must STILL be listed (the prefix list is a
	// JetStream-level enumeration; the caller filters on body isDeleted).
	if _, err := c.KVPut(ctx, bucket, soft, []byte(`{"isDeleted":true}`)); err != nil {
		t.Fatalf("put soft: %v", err)
	}
	if _, err := c.KVPut(ctx, bucket, other, []byte(`{"isDeleted":false}`)); err != nil {
		t.Fatalf("put other: %v", err)
	}

	keys, err := c.KVListKeysPrefix(ctx, bucket, "lnk.object.")
	if err != nil {
		t.Fatalf("KVListKeysPrefix: %v", err)
	}
	got := map[string]bool{}
	for _, k := range keys {
		got[k] = true
	}
	for _, want := range []string{live1, live2, soft} {
		if !got[want] {
			t.Errorf("KVListKeysPrefix missing %q; got %v", want, keys)
		}
	}
	if got[other] {
		t.Errorf("KVListKeysPrefix returned out-of-prefix key %q", other)
	}
	if len(keys) != 3 {
		t.Errorf("KVListKeysPrefix returned %d keys, want 3: %v", len(keys), keys)
	}
}

// TestKVListKeysFilter exercises the generalized, paged subject-filter seam that
// backs kv.Links: a source-bounded prefix filter, a TARGET-bounded mid-subject
// wildcard filter (the load-bearing "in"-direction case), the token boundary,
// and deterministic cursor paging.
func TestKVListKeysFilter(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	hub := testNanoID1 // the provider id, fixed in both directions
	// Outbound: provider hasBooking appointment (hub is the source).
	out1 := "lnk.provider." + hub + ".hasBooking.appointment." + testNanoID2
	out2 := "lnk.provider." + hub + ".hasBooking.appointment." + testNanoID3
	// A sibling relation sharing the hasBooking prefix — must NOT match (the
	// trailing-dot token boundary distinguishes hasBooking from hasBookingExtra).
	sibling := "lnk.provider." + hub + ".hasBookingExtra.appointment." + testNanoID2
	// Inbound: appointment withProvider provider (hub is the suffix).
	in1 := "lnk.appointment." + testNanoID2 + ".withProvider.provider." + hub
	in2 := "lnk.appointment." + testNanoID3 + ".withProvider.provider." + hub

	for _, k := range []string{out1, out2, sibling, in1, in2} {
		if _, err := c.KVPut(ctx, bucket, k, []byte(`{"isDeleted":false}`)); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}

	// Source-bounded filter: token boundary excludes hasBookingExtra.
	outKeys, next, err := c.KVListKeysFilter(ctx, bucket, "lnk.provider."+hub+".hasBooking.>", "", 10)
	if err != nil {
		t.Fatalf("filter out: %v", err)
	}
	if next != "" {
		t.Errorf("out: nextCursor = %q, want empty", next)
	}
	gotOut := map[string]bool{}
	for _, k := range outKeys {
		gotOut[k] = true
	}
	if !gotOut[out1] || !gotOut[out2] {
		t.Errorf("out filter missing a hasBooking key; got %v", outKeys)
	}
	if gotOut[sibling] {
		t.Errorf("out filter leaked hasBookingExtra past the token boundary; got %v", outKeys)
	}
	if len(outKeys) != 2 {
		t.Errorf("out filter returned %d keys, want 2: %v", len(outKeys), outKeys)
	}

	// Target-bounded mid-subject wildcard filter (the "in" direction).
	inKeys, _, err := c.KVListKeysFilter(ctx, bucket, "lnk.*.*.withProvider.provider."+hub, "", 10)
	if err != nil {
		t.Fatalf("filter in (mid-subject wildcard): %v", err)
	}
	gotIn := map[string]bool{}
	for _, k := range inKeys {
		gotIn[k] = true
	}
	if !gotIn[in1] || !gotIn[in2] || len(inKeys) != 2 {
		t.Errorf("in filter wrong set; got %v, want %q + %q", inKeys, in1, in2)
	}

	// Deterministic paging over the 2 outbound keys, one per page.
	p1, c1, err := c.KVListKeysFilter(ctx, bucket, "lnk.provider."+hub+".hasBooking.>", "", 1)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(p1) != 1 || c1 == "" {
		t.Fatalf("page1: %d keys, cursor=%q, want 1 + non-empty cursor", len(p1), c1)
	}
	p2, c2, err := c.KVListKeysFilter(ctx, bucket, "lnk.provider."+hub+".hasBooking.>", c1, 1)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(p2) != 1 || c2 != "" {
		t.Fatalf("page2: %d keys, cursor=%q, want 1 + exhausted", len(p2), c2)
	}
	if p1[0] == p2[0] {
		t.Errorf("paging returned the same key twice: %q", p1[0])
	}
	// The cursor is the first page's last (and only) key.
	if c1 != p1[0] {
		t.Errorf("nextCursor = %q, want the page's last key %q", c1, p1[0])
	}
}

// TestPageFilteredKeys exercises the pure paging core directly — the de-dup,
// strict-cursor, and page-boundary invariants KVListKeysFilter depends on,
// without a live KV. The de-dup + boundary-duplicate cases guard the
// membership-loss bug a duplicate-reporting lister would otherwise cause.
func TestPageFilteredKeys(t *testing.T) {
	cases := []struct {
		name    string
		keys    []string
		cursor  string
		limit   int
		want    []string
		wantNxt string
	}{
		{"empty", nil, "", 10, nil, ""},
		{"sorts and returns all under limit", []string{"c", "a", "b"}, "", 10, []string{"a", "b", "c"}, ""},
		{"exactly limit yields no spurious next", []string{"a", "b"}, "", 2, []string{"a", "b"}, ""},
		{"limit+1 pages with boundary cursor", []string{"a", "b", "c"}, "", 2, []string{"a", "b"}, "b"},
		{"cursor exclusion is strict-greater", []string{"a", "b", "c", "d"}, "b", 10, []string{"c", "d"}, ""},
		{"adjacent duplicates collapse", []string{"a", "a", "b", "b", "c"}, "", 10, []string{"a", "b", "c"}, ""},
		{"boundary duplicate does not skip a distinct key", []string{"a", "a", "b", "c"}, "", 2, []string{"a", "b"}, "b"},
		{"non-positive limit returns all in one page", []string{"a", "b", "c"}, "", 0, []string{"a", "b", "c"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, nxt := pageFilteredKeys(append([]string(nil), tc.keys...), tc.cursor, tc.limit)
			if nxt != tc.wantNxt {
				t.Errorf("nextCursor = %q, want %q", nxt, tc.wantNxt)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("page = %v, want %v", got, tc.want)
			}
		})
	}

	// Paging across a boundary duplicate must cover {a,b,c} with no loss — the
	// exact membership-loss the de-dup guards (a naive impl would set the cursor
	// to a duplicate 'a' and skip 'b' on the next page).
	p1, c1 := pageFilteredKeys([]string{"a", "a", "b", "c"}, "", 2)
	p2, c2 := pageFilteredKeys([]string{"a", "a", "b", "c"}, c1, 2)
	all := append(append([]string{}, p1...), p2...)
	if c2 != "" || fmt.Sprint(all) != fmt.Sprint([]string{"a", "b", "c"}) {
		t.Fatalf("paged sequence = %v (c2=%q), want [a b c] exhausted", all, c2)
	}
}

// TestKVListKeysFilter_CancelledContext proves a cancelled context yields an
// ERROR, never a silently-truncated page. The keyLister has no error channel:
// on cancellation it just closes the keys channel, so without the post-range
// ctx.Err() check a timed-out enumeration would return a partial set as
// success — and a set guard would evaluate a constraint over an incomplete
// neighbor set.
func TestKVListKeysFilter_CancelledContext(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)
	key := "lnk.provider." + testNanoID1 + ".hasBooking.appointment." + testNanoID2
	if _, err := c.KVPut(ctx, bucket, key, []byte(`{"isDeleted":false}`)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel() // cancelled before the call
	_, _, err := c.KVListKeysFilter(cctx, bucket, "lnk.provider."+testNanoID1+".hasBooking.>", "", 10)
	if err == nil {
		t.Fatal("cancelled context must yield an error, not a silent (possibly partial) success")
	}
}
