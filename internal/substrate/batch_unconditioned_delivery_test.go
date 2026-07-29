package substrate

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestAtomicBatch_UnconditionedMemberIsDeliveredToDurableConsumer grounds the
// lattice.md "[Refractor] A live claim's own consumer grant never projects
// into Capability KV" investigation. ClaimIdentity's consumer-role grant
// commits as a genuinely UNCONDITIONED batch member (step8_commit.go:191-215
// — an "update" mutation whose prior key is not found gets neither
// HasRevision nor CreateOnly) riding inside the same atomic batch as
// conditioned members (create/update-with-revision). This test reproduces
// that exact shape against an embedded NATS 2.14 server and asserts whether a
// subject-filtered durable consumer (mirroring Refractor's adjacency
// bootstrapper, internal/refractor/consumer/bootstrap.go) receives the
// unconditioned member identically to the conditioned ones.
func TestAtomicBatch_UnconditionedMemberIsDeliveredToDurableConsumer(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	conditionedKey := VertexKey("identity", testNanoID1)
	unconditionedKey := LinkKey("identity", testNanoID1, "holdsRole", "role", testNanoID2)

	ops := []BatchOp{
		{
			Bucket:     bucket,
			Key:        conditionedKey,
			Value:      []byte(`{"class":"state","data":{"value":"claimed"}}`),
			CreateOnly: true,
		},
		{
			Bucket: bucket,
			Key:    unconditionedKey,
			Value:  []byte(`{"class":"link","isDeleted":false}`),
			// Deliberately no CreateOnly, no HasRevision — mirrors the
			// "update" mutation whose prior[key].Found is false at
			// step8_commit.go:207-214.
		},
	}

	ack, err := c.AtomicBatch(ctx, ops)
	if err != nil {
		t.Fatalf("AtomicBatch: %v", err)
	}
	if ack.Count != 2 {
		t.Fatalf("expected batch count 2, got %d", ack.Count)
	}

	// Confirm both keys are durably visible via a direct Get — the design
	// doc's own live repro already established this holds live; assert it
	// here too so a failure below is isolated to consumer delivery.
	if _, err := c.KVGet(ctx, bucket, conditionedKey); err != nil {
		t.Fatalf("KVGet conditioned key: %v", err)
	}
	if _, err := c.KVGet(ctx, bucket, unconditionedKey); err != nil {
		t.Fatalf("KVGet unconditioned key: %v", err)
	}

	// Now mirror Refractor's adjacency bootstrapper exactly: a durable
	// JetStream consumer on KV_<bucket> filtered to "$KV.<bucket>.>".
	streamName := CoreKVStreamForTest(bucket)
	filterSubj := "$KV." + bucket + ".>"

	var mu sync.Mutex
	seen := map[string]bool{}
	done := make(chan struct{})

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		_ = c.RunDurableConsumer(runCtx, DurableConsumerConfig{
			Stream:        streamName,
			FilterSubject: filterSubj,
			Durable:       "test-adjacency-repro",
		}, func(_ context.Context, msg Message) Decision {
			mu.Lock()
			key := msg.Subject[len("$KV."+bucket+"."):]
			seen[key] = true
			gotBoth := seen[conditionedKey] && seen[unconditionedKey]
			mu.Unlock()
			if gotBoth {
				select {
				case <-done:
				default:
					close(done)
				}
			}
			return Ack
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("timed out waiting for both keys to be delivered to the durable consumer; seen=%v (conditioned=%v unconditioned=%v)",
			seen, seen[conditionedKey], seen[unconditionedKey])
	}
}

// CoreKVStreamForTest mirrors internal/refractor/subjects.CoreKVStream
// without importing internal/refractor (which would create an import cycle
// risk / unwanted dependency from internal/substrate). Kept identical:
// NATS convention "KV_<bucket>".
func CoreKVStreamForTest(bucket string) string {
	return "KV_" + bucket
}
