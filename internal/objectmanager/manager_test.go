package objectmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func testConn(t *testing.T) (*substrate.Conn, context.Context) {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = jsstore.Dir(t) // unique per test — avoid a shared-JetStream collision
	s := natsserver.RunServer(&opts)
	t.Cleanup(func() {
		if jsCfg := s.JetStreamConfig(); jsCfg != nil {
			defer os.RemoveAll(jsCfg.StoreDir)
		}
		s.Shutdown()
	})
	ctx := context.Background()
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: s.ClientURL()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	js := conn.JetStream()
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"}); err != nil {
		t.Fatalf("create core-kv: %v", err)
	}
	if _, err := js.CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket: "core-objects", Storage: jetstream.FileStorage}); err != nil {
		t.Fatalf("create core-objects: %v", err)
	}
	return conn, ctx
}

func newManager(conn *substrate.Conn) *Manager {
	return New(Config{
		Conn:           conn,
		CoreKVBucket:   "core-kv",
		ObjectsBucket:  "core-objects",
		EventsStream:   "core-events",
		ReconcileGrace: time.Hour,
	})
}

func seedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, isDeleted bool, data map[string]any) {
	t.Helper()
	if data == nil {
		data = map[string]any{}
	}
	doc := map[string]any{"key": key, "class": "object", "isDeleted": isDeleted, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, "core-kv", key, b); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

func tombstonedMsg(objectKey, storeName string) substrate.Message {
	body, _ := json.Marshal(map[string]any{"payload": map[string]any{"objectKey": objectKey, "storeName": storeName}})
	return substrate.Message{Body: body, Subject: "events.object.tombstoned"}
}

func putBytes(t *testing.T, ctx context.Context, conn *substrate.Conn, name string) {
	t.Helper()
	if _, err := conn.ObjectPut(ctx, "core-objects", name, bytes.NewReader([]byte("bytes-"+name)), 1<<20); err != nil {
		t.Fatalf("put bytes %s: %v", name, err)
	}
}

func objectAbsent(ctx context.Context, conn *substrate.Conn, name string) bool {
	_, err := conn.ObjectGetInfo(ctx, "core-objects", name)
	return errors.Is(err, substrate.ErrObjectNotFound)
}

// Loop B: a still-tombstoned vertex ⇒ the bytes are reclaimed.
func TestManager_HandleTombstoned_DeletesWhenTombstoned(t *testing.T) {
	conn, ctx := testConn(t)
	m := newManager(conn)
	putBytes(t, ctx, conn, "store1")
	seedVertex(t, ctx, conn, "vtx.object.AAobjHJKMNPQRSTUVWX", true, nil) // tombstoned

	if got := m.handleTombstoned(ctx, tombstonedMsg("vtx.object.AAobjHJKMNPQRSTUVWX", "store1")); got != substrate.Ack {
		t.Fatalf("decision = %v want Ack", got)
	}
	if !objectAbsent(ctx, conn, "store1") {
		t.Fatalf("bytes should be reclaimed for a still-tombstoned object")
	}
}

// Loop B: a REVIVED (alive) vertex ⇒ the bytes are kept (the tombstone was superseded).
func TestManager_HandleTombstoned_KeepsWhenRevived(t *testing.T) {
	conn, ctx := testConn(t)
	m := newManager(conn)
	putBytes(t, ctx, conn, "store2")
	seedVertex(t, ctx, conn, "vtx.object.BBobjHJKMNPQRSTUVWX", false, nil) // alive (revived)

	if got := m.handleTombstoned(ctx, tombstonedMsg("vtx.object.BBobjHJKMNPQRSTUVWX", "store2")); got != substrate.Ack {
		t.Fatalf("decision = %v want Ack", got)
	}
	if objectAbsent(ctx, conn, "store2") {
		t.Fatalf("bytes must NOT be deleted when the object was revived")
	}
}

// Loop B: a vertex that is hard-gone (NotFound) ⇒ the bytes are reclaimed.
func TestManager_HandleTombstoned_DeletesWhenGone(t *testing.T) {
	conn, ctx := testConn(t)
	m := newManager(conn)
	putBytes(t, ctx, conn, "store3")

	if got := m.handleTombstoned(ctx, tombstonedMsg("vtx.object.CCobjHJKMNPQRSTUVWX", "store3")); got != substrate.Ack {
		t.Fatalf("decision = %v want Ack", got)
	}
	if !objectAbsent(ctx, conn, "store3") {
		t.Fatalf("bytes should be reclaimed when the object vertex is gone")
	}
}

func TestManager_HandleTombstoned_Malformed(t *testing.T) {
	conn, ctx := testConn(t)
	m := newManager(conn)
	if got := m.handleTombstoned(ctx, substrate.Message{Body: []byte("{not json")}); got != substrate.Term {
		t.Fatalf("malformed body decision = %v want Term", got)
	}
	if got := m.handleTombstoned(ctx, tombstonedMsg("", "")); got != substrate.Term {
		t.Fatalf("missing fields decision = %v want Term", got)
	}
	if got := m.handleTombstoned(ctx, substrate.Message{}); got != substrate.Ack {
		t.Fatalf("empty body decision = %v want Ack", got)
	}
}

// The reconcile: a referenced object (live vertex names exactly its storeName) is
// kept; a dedup-duplicate (vertex names a DIFFERENT storeName) and a never-attached
// blob are reclaimed; a too-young blob is spared (orphan window).
func TestManager_Reconcile(t *testing.T) {
	conn, ctx := testConn(t)
	m := newManager(conn)
	// Advance the clock 48h so every just-uploaded blob is past the 1h grace,
	// except the explicitly-young one (which we re-stamp via a second store).
	m.cfg.now = func() time.Time { return time.Now().Add(48 * time.Hour) }

	// canonical: a live vertex names exactly this storeName → kept.
	putBytes(t, ctx, conn, "canon")
	digCanon, _ := conn.ObjectGetInfo(ctx, "core-objects", "canon")
	oidCanon := substrate.SHA256NanoID("object:" + digCanon.Digest)
	seedVertex(t, ctx, conn, "vtx.object."+oidCanon, false, nil)
	contentDoc := func(storeName string, deleted bool) []byte {
		b, _ := json.Marshal(map[string]any{"key": "vtx.object." + oidCanon + ".content", "class": "content",
			"vertexKey": "vtx.object." + oidCanon, "localName": "content", "isDeleted": deleted,
			"data": map[string]any{"storeName": storeName, "digest": digCanon.Digest}})
		return b
	}
	if _, err := conn.KVPut(ctx, "core-kv", "vtx.object."+oidCanon+".content", contentDoc("canon", false)); err != nil {
		t.Fatalf("seed content: %v", err)
	}

	// dedup-dup: identical bytes uploaded again under a different storeName; the
	// canonical vertex names "canon", NOT "dup" → "dup" must be reclaimed.
	putBytes(t, ctx, conn, "dup") // same content "bytes-..."? no — different name → different content
	// Re-upload the SAME content as canon under a different store name to make a
	// true dedup duplicate (same digest → same oid as canon).
	if _, err := conn.ObjectPut(ctx, "core-objects", "dup2", bytes.NewReader([]byte("bytes-canon")), 1<<20); err != nil {
		t.Fatalf("put dup2: %v", err)
	}

	// never-attached: a blob with no referring vertex at all → reclaimed.
	putBytes(t, ctx, conn, "orphan")

	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if objectAbsent(ctx, conn, "canon") {
		t.Fatalf("canonical bytes (named by a live vertex) must be spared")
	}
	if !objectAbsent(ctx, conn, "dup2") {
		t.Fatalf("a dedup-duplicate storeName (not named by the canonical vertex) must be reclaimed")
	}
	if !objectAbsent(ctx, conn, "orphan") {
		t.Fatalf("a never-attached blob must be reclaimed")
	}
	if !objectAbsent(ctx, conn, "dup") {
		t.Fatalf("an unreferenced blob must be reclaimed")
	}
}

// The reconcile spares a blob within the grace window (an AttachObject may still
// be in flight — the orphan window).
func TestManager_Reconcile_SparesYoungBytes(t *testing.T) {
	conn, ctx := testConn(t)
	m := newManager(conn) // now == real now; grace 1h
	putBytes(t, ctx, conn, "fresh")
	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if objectAbsent(ctx, conn, "fresh") {
		t.Fatalf("a blob younger than the grace window must be spared (orphan window)")
	}
}
