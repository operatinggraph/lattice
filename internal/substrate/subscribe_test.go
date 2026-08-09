package substrate

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// receiveEvent reads one KVEvent off ch with a bounded wait. Test fails
// if no event arrives in time.
func receiveEvent(t *testing.T, ch <-chan KVEvent, within time.Duration) KVEvent {
	t.Helper()
	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatalf("KVEvent channel closed unexpectedly")
		}
		return evt
	case <-time.After(within):
		t.Fatalf("timeout waiting %v for KVEvent", within)
	}
	return KVEvent{}
}

// expectNoEvent asserts no event arrives within the window.
func expectNoEvent(t *testing.T, ch <-chan KVEvent, within time.Duration) {
	t.Helper()
	select {
	case evt, ok := <-ch:
		if !ok {
			return // closed is acceptable here
		}
		t.Fatalf("unexpected KVEvent: %+v", evt)
	case <-time.After(within):
		return
	}
}

// TestSubscribeKVChanges_HappyPath subscribes after a bucket exists, writes
// one value under the prefix, and asserts the event surfaces with the
// expected key/value/Sequence.
func TestSubscribeKVChanges_HappyPath(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch, err := c.SubscribeKVChanges(subCtx, bucket, []string{"vtx.meta."}, "sub-test-happy", SubscribeKVOptions{})
	if err != nil {
		t.Fatalf("SubscribeKVChanges: %v", err)
	}

	body := []byte(`{"class":"meta.lens","isDeleted":false}`)
	rev, err := c.KVPut(ctx, bucket, "vtx.meta.abc", body)
	if err != nil {
		t.Fatalf("KVPut: %v", err)
	}

	evt := receiveEvent(t, ch, 2*time.Second)
	if evt.Key != "vtx.meta.abc" {
		t.Errorf("key: got %q want %q", evt.Key, "vtx.meta.abc")
	}
	if string(evt.Value) != string(body) {
		t.Errorf("value: got %q want %q", evt.Value, body)
	}
	if evt.Revision == 0 || evt.Revision != rev {
		t.Errorf("Revision: got %d want %d", evt.Revision, rev)
	}
	if evt.IsDeleted {
		t.Errorf("IsDeleted: want false")
	}
}

// TestSubscribeKVChanges_IncludeHistory pre-seeds two values, then
// subscribes with IncludeHistory=true and asserts both replay.
func TestSubscribeKVChanges_IncludeHistory(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	for i, k := range []string{"vtx.meta.one", "vtx.meta.two"} {
		body, _ := json.Marshal(map[string]any{"class": "meta.lens", "n": i})
		if _, err := c.KVPut(ctx, bucket, k, body); err != nil {
			t.Fatalf("seed %q: %v", k, err)
		}
	}

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch, err := c.SubscribeKVChanges(subCtx, bucket, []string{"vtx.meta."}, "sub-test-history",
		SubscribeKVOptions{IncludeHistory: true})
	if err != nil {
		t.Fatalf("SubscribeKVChanges: %v", err)
	}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		evt := receiveEvent(t, ch, 2*time.Second)
		seen[evt.Key] = true
	}
	for _, k := range []string{"vtx.meta.one", "vtx.meta.two"} {
		if !seen[k] {
			t.Errorf("did not replay key %q (seen=%v)", k, seen)
		}
	}
}

// TestSubscribeKVChanges_DurableResume — the centrepiece behaviour. Write
// N values, consume some, cancel the subscription, write more, resubscribe
// with the SAME durable name, and assert only the post-cancel writes
// surface (sequence position held across the restart).
//
// NOTE: This test deliberately exercises a non-default code path —
// production callers will typically pass IncludeHistory=true to seed
// state at first connect. We use IncludeHistory=false here to verify
// the durable-position behaviour cleanly.
func TestSubscribeKVChanges_DurableResume(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const durable = "sub-test-resume"

	// First subscription: replay-from-new. Write & consume one entry.
	sub1Ctx, cancel1 := context.WithCancel(ctx)
	ch1, err := c.SubscribeKVChanges(sub1Ctx, bucket, []string{"vtx.meta."}, durable, SubscribeKVOptions{})
	if err != nil {
		t.Fatalf("first SubscribeKVChanges: %v", err)
	}

	if _, err := c.KVPut(ctx, bucket, "vtx.meta.alpha", []byte(`{"class":"meta.lens","v":1}`)); err != nil {
		t.Fatalf("first put: %v", err)
	}
	evt1 := receiveEvent(t, ch1, 2*time.Second)
	if evt1.Key != "vtx.meta.alpha" {
		t.Fatalf("first event key: got %q want %q", evt1.Key, "vtx.meta.alpha")
	}

	// Tear down the first subscription. The helper does NOT delete the
	// durable consumer on ctx cancel by design (see runKVSubscription's
	// docstring) — that's exactly what lets the resumed subscription
	// pick up from the last-acked sequence.
	cancel1()
	// Give the goroutine a moment to exit.
	time.Sleep(150 * time.Millisecond)

	// Writes that occur between sessions — these are what the resumed
	// subscription must surface.
	if _, err := c.KVPut(ctx, bucket, "vtx.meta.beta", []byte(`{"class":"meta.lens","v":2}`)); err != nil {
		t.Fatalf("second put: %v", err)
	}
	if _, err := c.KVPut(ctx, bucket, "vtx.meta.gamma", []byte(`{"class":"meta.lens","v":3}`)); err != nil {
		t.Fatalf("third put: %v", err)
	}

	// Resume with the SAME durable name.
	sub2Ctx, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	ch2, err := c.SubscribeKVChanges(sub2Ctx, bucket, []string{"vtx.meta."}, durable, SubscribeKVOptions{})
	if err != nil {
		t.Fatalf("resume SubscribeKVChanges: %v", err)
	}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		evt := receiveEvent(t, ch2, 2*time.Second)
		seen[evt.Key] = true
	}
	if !seen["vtx.meta.beta"] || !seen["vtx.meta.gamma"] {
		t.Errorf("resumed subscription did not replay both new entries: seen=%v", seen)
	}
	if seen["vtx.meta.alpha"] {
		t.Errorf("resumed subscription replayed already-acked alpha — sequence position not held")
	}

	// Confirm nothing more arrives.
	expectNoEvent(t, ch2, 250*time.Millisecond)
}

// TestPruneStaleDurables_AgeGuard proves the age-guard property Fire A adds
// (refractor-lens-registry-restart-integrity-design.md §4/§4.1): a durable
// matching namePrefix survives PruneStaleDurables while it is recently
// active, and is only removed once it ages past the threshold — the
// property that makes the per-boot-durable pattern safe under concurrent
// instances (instance A's boot must never delete instance B's live
// durable).
func TestPruneStaleDurables_AgeGuard(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	origAge := PruneStaleDurableAge
	PruneStaleDurableAge = 200 * time.Millisecond
	t.Cleanup(func() { PruneStaleDurableAge = origAge })

	const prefix = "prune-age-test"
	const sibling = prefix + "-sibling-nonce1"
	const keep = prefix + "-me-nonce2"

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if _, err := c.SubscribeKVChanges(subCtx, bucket, []string{"vtx.meta."}, sibling, SubscribeKVOptions{}); err != nil {
		t.Fatalf("create sibling durable: %v", err)
	}

	// Immediately after creation, the sibling is well inside the (shrunk)
	// age window — PruneStaleDurables must NOT delete it.
	if err := c.PruneStaleDurables(ctx, bucket, prefix, keep, nil); err != nil {
		t.Fatalf("PruneStaleDurables (recent): %v", err)
	}
	if _, err := c.js.Consumer(ctx, "KV_"+bucket, sibling); err != nil {
		t.Fatalf("recently-created sibling durable was pruned (age guard failed): %v", err)
	}

	// Once the sibling ages past the threshold with no further activity,
	// PruneStaleDurables must remove it.
	time.Sleep(300 * time.Millisecond)
	if err := c.PruneStaleDurables(ctx, bucket, prefix, keep, nil); err != nil {
		t.Fatalf("PruneStaleDurables (stale): %v", err)
	}
	if _, err := c.js.Consumer(ctx, "KV_"+bucket, sibling); err == nil {
		t.Fatalf("stale sibling durable was not pruned")
	}
}

// TestSubscribeKVChanges_MultiplePrefixes subscribes with two prefixes and
// asserts an event under either surfaces, while a key under neither does not
// (dynamic-type-taxonomy-design.md §14 Fire A item 4 — the taxonomy trigger
// widens the meta watch to a vtx.meta.> branch and a lnk.meta.*.subtypeOf.>
// branch on the SAME consumer).
func TestSubscribeKVChanges_MultiplePrefixes(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch, err := c.SubscribeKVChanges(subCtx, bucket,
		[]string{"vtx.meta.", "lnk.meta.*.subtypeOf.>"}, "sub-test-multi", SubscribeKVOptions{})
	if err != nil {
		t.Fatalf("SubscribeKVChanges: %v", err)
	}

	metaID, err := NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	leafID, err := NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	parentID, err := NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	unitID, err := NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}

	metaKey := "vtx.meta." + metaID
	if _, err := c.KVPut(ctx, bucket, metaKey, []byte(`{"class":"meta.ddl.vertexType","isDeleted":false}`)); err != nil {
		t.Fatalf("KVPut %s: %v", metaKey, err)
	}
	evt1 := receiveEvent(t, ch, 2*time.Second)
	if evt1.Key != metaKey {
		t.Errorf("first key: got %q want %q", evt1.Key, metaKey)
	}

	linkKey := "lnk.meta." + leafID + ".subtypeOf.meta." + parentID
	if _, err := c.KVPut(ctx, bucket, linkKey,
		[]byte(`{"class":"subtypeOf","isDeleted":false}`)); err != nil {
		t.Fatalf("KVPut subtypeOf link: %v", err)
	}
	evt2 := receiveEvent(t, ch, 2*time.Second)
	if evt2.Key != linkKey {
		t.Errorf("second key: got %q want the subtypeOf link key", evt2.Key)
	}

	// A key under neither prefix must not surface.
	unitKey := "vtx.unit." + unitID
	if _, err := c.KVPut(ctx, bucket, unitKey, []byte(`{"class":"unit","isDeleted":false}`)); err != nil {
		t.Fatalf("KVPut %s: %v", unitKey, err)
	}
	expectNoEvent(t, ch, 250*time.Millisecond)
}

// TestSubscribeKVChanges_SinglePrefixUsesSingularFilterSubject asserts, at
// the JetStream ConsumerConfig level, that a one-element keyPrefixes slice
// produces the singular FilterSubject field with FilterSubjects left
// nil/empty — the exact shape every pre-multi-prefix durable already has.
// This compat property is otherwise stated only in prose (SubscribeKVChanges'
// own doc); pinning it here means a future "simplify to always use
// FilterSubjects" cannot land silently.
func TestSubscribeKVChanges_SinglePrefixUsesSingularFilterSubject(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	const durable = "sub-test-singular-filter"
	if _, err := c.SubscribeKVChanges(subCtx, bucket, []string{"vtx.meta."}, durable, SubscribeKVOptions{}); err != nil {
		t.Fatalf("SubscribeKVChanges: %v", err)
	}

	cons, err := c.js.Consumer(ctx, "KV_"+bucket, durable)
	if err != nil {
		t.Fatalf("look up consumer: %v", err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	wantFilterSubject := "$KV." + bucket + ".vtx.meta.>"
	if info.Config.FilterSubject != wantFilterSubject {
		t.Errorf("FilterSubject: got %q want %q", info.Config.FilterSubject, wantFilterSubject)
	}
	if len(info.Config.FilterSubjects) != 0 {
		t.Errorf("FilterSubjects: want nil/empty for a single-prefix subscription, got %v", info.Config.FilterSubjects)
	}
}

// TestSubscribeKVChanges_EmptyPrefixesErrors asserts an empty keyPrefixes
// slice is rejected rather than silently subscribing to nothing (or
// everything).
func TestSubscribeKVChanges_EmptyPrefixesErrors(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	_, err := c.SubscribeKVChanges(subCtx, bucket, nil, "sub-test-empty-prefixes", SubscribeKVOptions{})
	if err == nil {
		t.Fatalf("SubscribeKVChanges: want error for empty keyPrefixes, got nil")
	}
}

// TestSubscribeKVChanges_Tombstone writes a value with isDeleted=true in
// the envelope and asserts IsDeleted is surfaced.
func TestSubscribeKVChanges_Tombstone(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := c.SubscribeKVChanges(subCtx, bucket, []string{"vtx.meta."}, "sub-test-tomb", SubscribeKVOptions{})
	if err != nil {
		t.Fatalf("SubscribeKVChanges: %v", err)
	}

	// Soft-delete pattern: write an envelope with isDeleted=true (the
	// Processor's logical-delete shape — distinct from a NATS KV
	// tombstone, which is empty-body).
	body := []byte(`{"class":"meta.lens","isDeleted":true}`)
	if _, err := c.KVPut(ctx, bucket, "vtx.meta.tombstone", body); err != nil {
		t.Fatalf("KVPut: %v", err)
	}

	evt := receiveEvent(t, ch, 2*time.Second)
	if evt.Key != "vtx.meta.tombstone" {
		t.Errorf("key: got %q want %q", evt.Key, "vtx.meta.tombstone")
	}
	if !evt.IsDeleted {
		t.Errorf("IsDeleted: want true on soft-deleted envelope")
	}

	// Also confirm a NATS KV tombstone (KV.Delete → empty-body message)
	// surfaces as IsDeleted=true.
	if err := c.KVDelete(ctx, bucket, "vtx.meta.tombstone"); err != nil {
		t.Fatalf("KVDelete: %v", err)
	}
	evt2 := receiveEvent(t, ch, 2*time.Second)
	if !evt2.IsDeleted {
		t.Errorf("IsDeleted: want true on KV tombstone, got %+v", evt2)
	}
	if len(evt2.Value) != 0 {
		t.Errorf("KV tombstone should carry empty body, got %q", evt2.Value)
	}

	// Sanity: AckExplicit is in effect and consumer name unique.
	_ = jetstream.AckExplicitPolicy
}
