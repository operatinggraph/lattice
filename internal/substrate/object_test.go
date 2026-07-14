package substrate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
)

// provisionObjectStore mirrors bootstrap's core-objects provisioning: a
// file-backed JetStream Object Store.
func provisionObjectStore(ctx context.Context, t *testing.T, c *Conn, bucket string) {
	t.Helper()
	if _, err := c.JetStream().CreateOrUpdateObjectStore(ctx, jetstream.ObjectStoreConfig{
		Bucket:  bucket,
		Storage: jetstream.FileStorage,
	}); err != nil {
		t.Fatalf("create object store %q: %v", bucket, err)
	}
}

func TestObject_PutGetRoundTrip(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionObjectStore(ctx, t, c, "core-objects")

	payload := []byte("hello lattice off-graph blob plane")
	info, err := c.ObjectPut(ctx, "core-objects", "obj-1", bytes.NewReader(payload), 1<<20)
	if err != nil {
		t.Fatalf("ObjectPut: %v", err)
	}
	if info.Size != uint64(len(payload)) {
		t.Fatalf("size = %d want %d", info.Size, len(payload))
	}
	if !strings.HasPrefix(info.Digest, "SHA-256=") {
		t.Fatalf("digest %q missing SHA-256= prefix", info.Digest)
	}

	gi, err := c.ObjectGetInfo(ctx, "core-objects", "obj-1")
	if err != nil {
		t.Fatalf("ObjectGetInfo: %v", err)
	}
	if gi.Digest != info.Digest {
		t.Fatalf("getinfo digest %q != put %q", gi.Digest, info.Digest)
	}

	rc, gotInfo, err := c.ObjectGet(ctx, "core-objects", "obj-1")
	if err != nil {
		t.Fatalf("ObjectGet: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read object stream: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("bytes mismatch: got %q want %q", got, payload)
	}
	if gotInfo.Digest != info.Digest {
		t.Fatalf("get digest %q != put %q", gotInfo.Digest, info.Digest)
	}
}

func TestObject_DeleteIdempotent(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionObjectStore(ctx, t, c, "core-objects")

	if _, err := c.ObjectPut(ctx, "core-objects", "obj-del", bytes.NewReader([]byte("bytes")), 1<<20); err != nil {
		t.Fatalf("ObjectPut: %v", err)
	}
	if err := c.ObjectDelete(ctx, "core-objects", "obj-del"); err != nil {
		t.Fatalf("ObjectDelete: %v", err)
	}
	// Second delete of an absent object is a no-op (idempotent — the GC byte
	// reclaim path must not race a concurrent delete).
	if err := c.ObjectDelete(ctx, "core-objects", "obj-del"); err != nil {
		t.Fatalf("ObjectDelete (idempotent second): %v", err)
	}
	if _, err := c.ObjectGetInfo(ctx, "core-objects", "obj-del"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("after delete: err = %v want ErrObjectNotFound", err)
	}
}

func TestObject_PutCapRejectsOversize(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionObjectStore(ctx, t, c, "core-objects")

	big := bytes.Repeat([]byte("x"), 1024)
	_, err := c.ObjectPut(ctx, "core-objects", "too-big", bytes.NewReader(big), 100)
	if !errors.Is(err, ErrObjectTooLarge) {
		t.Fatalf("oversize put: err = %v want ErrObjectTooLarge", err)
	}
	// A rejected oversized Put leaves no bytes behind.
	if _, err := c.ObjectGetInfo(ctx, "core-objects", "too-big"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("after oversize reject, object should be absent: err = %v", err)
	}
	// Exactly-cap is allowed; cap <= 0 disables the cap.
	if _, err := c.ObjectPut(ctx, "core-objects", "exact", bytes.NewReader(bytes.Repeat([]byte("y"), 100)), 100); err != nil {
		t.Fatalf("exact-cap put: %v", err)
	}
	if _, err := c.ObjectPut(ctx, "core-objects", "uncapped", bytes.NewReader(big), 0); err != nil {
		t.Fatalf("uncapped put: %v", err)
	}
}

func TestObject_StoreExists(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	if err := c.ObjectStoreExists(ctx, "core-objects"); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("missing store: err = %v want ErrBucketNotFound", err)
	}
	provisionObjectStore(ctx, t, c, "core-objects")
	if err := c.ObjectStoreExists(ctx, "core-objects"); err != nil {
		t.Fatalf("present store: %v", err)
	}
}

func TestObject_List(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionObjectStore(ctx, t, c, "core-objects")

	// Empty store → empty slice, not an error.
	infos, err := c.ObjectList(ctx, "core-objects")
	if err != nil {
		t.Fatalf("ObjectList (empty): %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("empty store should list 0 objects, got %d", len(infos))
	}

	for _, n := range []string{"a", "b", "c"} {
		if _, err := c.ObjectPut(ctx, "core-objects", n, bytes.NewReader([]byte("bytes-"+n)), 1<<20); err != nil {
			t.Fatalf("ObjectPut %s: %v", n, err)
		}
	}
	infos, err = c.ObjectList(ctx, "core-objects")
	if err != nil {
		t.Fatalf("ObjectList: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(infos))
	}
	names := map[string]bool{}
	for _, in := range infos {
		names[in.Name] = true
		if in.Digest == "" || in.ModTime.IsZero() {
			t.Fatalf("object %s missing digest/modtime: %+v", in.Name, in)
		}
	}
	for _, n := range []string{"a", "b", "c"} {
		if !names[n] {
			t.Fatalf("ObjectList missing %q (got %v)", n, names)
		}
	}

	// A deleted object drops out of the listing.
	if err := c.ObjectDelete(ctx, "core-objects", "b"); err != nil {
		t.Fatalf("ObjectDelete: %v", err)
	}
	infos, _ = c.ObjectList(ctx, "core-objects")
	if len(infos) != 2 {
		t.Fatalf("after delete, expected 2 objects, got %d", len(infos))
	}
}

func TestObject_GetMissingReturnsNotFound(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionObjectStore(ctx, t, c, "core-objects")
	if _, _, err := c.ObjectGet(ctx, "core-objects", "nope"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("get absent: err = %v want ErrObjectNotFound", err)
	}
}
