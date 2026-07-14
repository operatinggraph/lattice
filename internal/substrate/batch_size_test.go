package substrate

import (
	"errors"
	"testing"
)

// TestAtomicBatch_MessageCountCeiling proves an over-limit op count is
// rejected pre-flight (ErrBatchTooLarge, never a raw NATS 10199) and that
// nothing is published — the batch is provably all-or-nothing at the guard,
// not just at the server.
func TestAtomicBatch_MessageCountCeiling(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	ops := make([]BatchOp, MaxBatchMessages+1)
	for i := range ops {
		id, err := NewNanoID()
		if err != nil {
			t.Fatalf("NewNanoID: %v", err)
		}
		ops[i] = BatchOp{Bucket: bucket, Key: VertexKey("identity", id), Value: []byte(`{}`), CreateOnly: true}
	}
	_, err := c.AtomicBatch(ctx, ops)
	if !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("expected ErrBatchTooLarge, got %v", err)
	}
	if errors.Is(err, ErrAtomicBatchRejected) {
		t.Fatalf("ErrBatchTooLarge must NOT be wrapped in ErrAtomicBatchRejected (pre-flight, not a NATS rejection)")
	}

	// Nothing landed — check the first op's key is absent.
	if _, err := c.KVGet(ctx, bucket, ops[0].Key); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("op[0] key must not exist after a rejected over-limit batch: %v", err)
	}
}

// TestAtomicBatch_ValueSizeCeiling proves a single oversized value is
// rejected pre-flight with ErrValueTooLarge, and that the boundary value
// (exactly at the derived ceiling) is accepted.
func TestAtomicBatch_ValueSizeCeiling(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	limit := c.valueSizeLimit()

	// Over the ceiling by one byte.
	overKey := VertexKey("identity", testNanoID1)
	overOps := []BatchOp{{Bucket: bucket, Key: overKey, Value: make([]byte, limit+1), CreateOnly: true}}
	_, err := c.AtomicBatch(ctx, overOps)
	if !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("expected ErrValueTooLarge, got %v", err)
	}
	if _, err := c.KVGet(ctx, bucket, overKey); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("oversized key must not exist after rejection: %v", err)
	}

	// Exactly at the ceiling — must pass (boundary).
	atKey := VertexKey("identity", testNanoID2)
	atOps := []BatchOp{{Bucket: bucket, Key: atKey, Value: make([]byte, limit), CreateOnly: true}}
	if _, err := c.AtomicBatch(ctx, atOps); err != nil {
		t.Fatalf("boundary-sized value must be accepted: %v", err)
	}
	if _, err := c.KVGet(ctx, bucket, atKey); err != nil {
		t.Fatalf("boundary key must exist after accepted batch: %v", err)
	}
}

// TestAtomicBatch_DeleteSkipsValueCheck proves a Delete op (no body) is
// exempt from the value-size guard regardless of the ceiling.
func TestAtomicBatch_DeleteSkipsValueCheck(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	if _, err := c.KVCreate(ctx, bucket, key, []byte(`{}`)); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	// A Delete op carries Value == nil; the guard must not consult len(Value)
	// against the ceiling (it would trivially pass anyway, but this pins the
	// exemption explicitly per the guard's Delete skip).
	ops := []BatchOp{{Bucket: bucket, Key: key, Delete: true}}
	if _, err := c.AtomicBatch(ctx, ops); err != nil {
		t.Fatalf("delete-only batch must not be rejected by the value guard: %v", err)
	}
	if _, err := c.KVGet(ctx, bucket, key); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("key must be deleted: %v", err)
	}
}

// TestPublishBatch_MessageCountCeiling mirrors the AtomicBatch count guard
// for the outbox publisher.
func TestPublishBatch_MessageCountCeiling(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionEventsStream(ctx, t, c)

	ops := make([]PublishOp, MaxBatchMessages+1)
	for i := range ops {
		ops[i] = PublishOp{Subject: "events.identity.created", Data: []byte(`{}`)}
	}
	if _, err := c.PublishBatch(ctx, ops); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("expected ErrBatchTooLarge, got %v", err)
	}
}

// TestPublishBatch_ValueSizeCeiling mirrors the AtomicBatch value-size guard
// for the outbox publisher.
func TestPublishBatch_ValueSizeCeiling(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionEventsStream(ctx, t, c)

	limit := c.valueSizeLimit()
	ops := []PublishOp{{Subject: "events.identity.created", Data: make([]byte, limit+1)}}
	if _, err := c.PublishBatch(ctx, ops); !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("expected ErrValueTooLarge, got %v", err)
	}
}
