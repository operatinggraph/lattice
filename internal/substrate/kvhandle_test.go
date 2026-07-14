package substrate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOpenKV_MissingBucket(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	if _, err := c.OpenKV(ctx, "no-such-bucket"); err == nil {
		t.Fatal("expected error opening a non-existent bucket, got nil")
	}
}

func TestKVHandle_RoundTrip(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	kv, err := c.OpenKV(ctx, bucket)
	if err != nil {
		t.Fatalf("OpenKV: %v", err)
	}
	if kv.Bucket() != bucket {
		t.Fatalf("Bucket() = %q, want %q", kv.Bucket(), bucket)
	}

	key := VertexKey("identity", testNanoID1)

	// Get missing → ErrKeyNotFound (delegates to Conn.KVGet).
	if _, err := kv.Get(ctx, key); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Get missing: want ErrKeyNotFound, got %v", err)
	}

	// Create, then Create again → ErrRevisionConflict.
	rev1, err := kv.Create(ctx, key, []byte(`{"v":1}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := kv.Create(ctx, key, []byte(`{"v":1}`)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("duplicate Create: want ErrRevisionConflict, got %v", err)
	}

	// Get back what we wrote.
	entry, err := kv.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.Revision != rev1 || string(entry.Value) != `{"v":1}` {
		t.Fatalf("Get mismatch: %+v", entry)
	}

	// Update wrong-rev → conflict; correct-rev → advances.
	if _, err := kv.Update(ctx, key, []byte(`{"v":2}`), rev1+9); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("Update wrong-rev: want ErrRevisionConflict, got %v", err)
	}
	if _, err := kv.Update(ctx, key, []byte(`{"v":2}`), rev1); err != nil {
		t.Fatalf("Update ok: %v", err)
	}

	// Put is unconditional.
	if _, err := kv.Put(ctx, key, []byte(`{"v":3}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// ListKeys sees the key.
	keys, err := kv.ListKeys(ctx)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != key {
		t.Fatalf("ListKeys = %v, want [%s]", keys, key)
	}

	// Delete → subsequent Get is ErrKeyNotFound.
	if err := kv.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := kv.Get(ctx, key); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Get after Delete: want ErrKeyNotFound, got %v", err)
	}
}

func TestKVHandle_PurgeAndStatus(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	kv, err := c.OpenKV(ctx, bucket)
	if err != nil {
		t.Fatalf("OpenKV: %v", err)
	}

	// Status on a live bucket → nil.
	if err := kv.Status(ctx); err != nil {
		t.Fatalf("Status on live bucket: %v", err)
	}
	// Status on a missing bucket → ErrBucketNotFound (via Conn.KVStatus).
	if err := c.KVStatus(ctx, "ghost-bucket"); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("Status missing bucket: want ErrBucketNotFound, got %v", err)
	}

	key := VertexKey("identity", testNanoID1)
	if _, err := kv.Create(ctx, key, []byte(`{"v":1}`)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Purge removes the key; a subsequent Get is ErrKeyNotFound.
	if err := kv.Purge(ctx, key); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := kv.Get(ctx, key); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Get after Purge: want ErrKeyNotFound, got %v", err)
	}
	// Purge is idempotent: purging an already-absent key is a no-op.
	if err := kv.Purge(ctx, key); err != nil {
		t.Fatalf("Purge absent: want nil (idempotent), got %v", err)
	}
}

func TestKVHandle_WatchUpdates(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	kv, err := c.OpenKV(ctx, bucket)
	if err != nil {
		t.Fatalf("OpenKV: %v", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates, err := kv.WatchUpdates(watchCtx)
	if err != nil {
		t.Fatalf("WatchUpdates: %v", err)
	}

	key := VertexKey("identity", testNanoID1)
	// Put repeatedly until the event is observed — absorbs the watcher-
	// establishment race (UpdatesOnly drops anything before the watch is live).
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_, _ = kv.Put(ctx, key, []byte(`{"v":1}`))
			}
		}
	}()

	select {
	case evt, ok := <-updates:
		close(done)
		if !ok {
			t.Fatal("updates channel closed before any event")
		}
		if evt.Key != key {
			t.Fatalf("event key = %q, want %q", evt.Key, key)
		}
	case <-time.After(5 * time.Second):
		close(done)
		t.Fatal("timed out waiting for a watch update")
	}

	// Cancelling the watch ctx closes the channel.
	cancel()
	select {
	case _, ok := <-updates:
		// Drain any buffered event, then expect close.
		if ok {
			select {
			case _, ok2 := <-updates:
				if ok2 {
					t.Fatal("expected channel close after cancel")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("channel did not close after cancel")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after cancel")
	}
}
