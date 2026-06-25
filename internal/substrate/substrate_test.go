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

func TestAtomicBatch_Commits(t *testing.T) {
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
