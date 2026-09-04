package substrate

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// resolveThenGetProfile runs fn under a hook that captures how the NoSnapshot
// past-the-cap read was served, so a test can assert the strategy and not
// merely the answer: a complete map comes back either way.
func resolveThenGetProfile(t *testing.T, ctx context.Context, fn func(context.Context)) (infos, requests, calls int) {
	t.Helper()
	hooked := WithKVResolveThenGetHook(ctx, func(i, r int) {
		infos += i
		requests += r
		calls++
	})
	fn(hooked)
	return infos, requests, calls
}

// seedKeys writes n vertex entries of the given type under one salt and
// returns their keys in seeding order.
func seedKeys(t *testing.T, ctx context.Context, c *Conn, bucket, vtxType, salt string, n int) []string {
	t.Helper()
	keys := make([]string, n)
	for i := 0; i < n; i++ {
		key := VertexKey(vtxType, DeriveNanoID(salt, fmt.Sprintf("%d", i)))
		if _, err := c.KVPut(ctx, bucket, key, []byte(fmt.Sprintf(`{"isDeleted":false,"data":{"i":%d}}`, i))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		keys[i] = key
	}
	return keys
}

// TestKVGetMultiNoSnapshot_Over1024_ExactKeys_ChunkedNotDrained pins the whole
// point of the strategy on a request of exact keys: past the fast path's cap
// the read is a handful of atomic requests over the keys it was already given
// — no listing, because nothing needed resolving, and no per-key drain.
func TestKVGetMultiNoSnapshot_Over1024_ExactKeys_ChunkedNotDrained(t *testing.T) {
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const n = 1100
	keys := seedKeys(t, ctx, c, bucket, "identity", "ltg-exact", n)

	var got map[string]*KVEntry
	infos, requests, calls := resolveThenGetProfile(t, ctx, func(hooked context.Context) {
		var err error
		got, err = c.KVGetMultiNoSnapshot(hooked, bucket, keys)
		if err != nil {
			t.Fatalf("KVGetMultiNoSnapshot over 1024 exact keys: %v", err)
		}
	})

	if calls != 1 {
		t.Fatalf("the past-the-cap path ran %d times, want exactly 1", calls)
	}
	if infos != 0 {
		t.Errorf("stream-info requests = %d, want 0 — an exact key needs no resolving, not even a stream handle", infos)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2 — 1,100 keys is a full 1,024 chunk and a remainder", requests)
	}
	if len(got) != n {
		t.Fatalf("got %d entries, want %d", len(got), n)
	}
	for i, k := range keys {
		entry, ok := got[k]
		if !ok {
			t.Fatalf("key %d (%s) missing", i, k)
		}
		want := fmt.Sprintf(`{"isDeleted":false,"data":{"i":%d}}`, i)
		if string(entry.Value) != want {
			t.Errorf("key %d value = %s, want %s", i, entry.Value, want)
		}
		if entry.Revision == 0 || entry.Key != k || entry.Bucket != bucket {
			t.Errorf("key %d entry = %+v, want bucket=%s key=%s and a nonzero revision", i, entry, bucket, k)
		}
	}
}

// TestKVGetMultiNoSnapshot_Over1024_Wildcards_ResolveThenChunk is the same
// pinning for the shape the strategy exists for: two wildcard filters over two
// key shapes, whose matches together exceed the cap. Each filter is enumerated
// once and the union is read in fast-path chunks.
func TestKVGetMultiNoSnapshot_Over1024_Wildcards_ResolveThenChunk(t *testing.T) {
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const perShape = 550
	identities := seedKeys(t, ctx, c, bucket, "identity", "ltg-wild-identity", perShape)
	services := seedKeys(t, ctx, c, bucket, "service", "ltg-wild-service", perShape)

	var got map[string]*KVEntry
	infos, requests, calls := resolveThenGetProfile(t, ctx, func(hooked context.Context) {
		var err error
		got, err = c.KVGetMultiNoSnapshot(hooked, bucket,
			[]string{"vtx.identity.*", "vtx.service.*"})
		if err != nil {
			t.Fatalf("KVGetMultiNoSnapshot over two wildcards: %v", err)
		}
	})

	if calls != 1 {
		t.Fatalf("the past-the-cap path ran %d times, want exactly 1", calls)
	}
	if infos != 3 {
		t.Errorf("stream-info requests = %d, want 3 — ONE stream handle for the resolution, then one per distinct wildcard", infos)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2 — 1,100 resolved keys is a full chunk and a remainder", requests)
	}
	if len(got) != 2*perShape {
		t.Fatalf("got %d entries, want %d", len(got), 2*perShape)
	}
	for _, k := range append(append([]string{}, identities...), services...) {
		if _, ok := got[k]; !ok {
			t.Fatalf("key %s missing from the resolved read", k)
		}
	}
}

// TestKVGetMultiNoSnapshot_Over1024_TombstoneAndPurgeAndAbsent pins that
// resolving to exact keys observes exactly what the fast path observes: a
// soft-tombstoned envelope is a live JetStream entry and IS returned, a
// NATS-purged key is not (the listing's IgnoreDeletes and the read's own marker
// handling agree), and a key that never existed is simply absent.
func TestKVGetMultiNoSnapshot_Over1024_TombstoneAndPurgeAndAbsent(t *testing.T) {
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const n = 1100
	keys := seedKeys(t, ctx, c, bucket, "identity", "ltg-lifecycle", n)

	tombstoned := keys[3]
	if _, err := c.KVPut(ctx, bucket, tombstoned, []byte(`{"isDeleted":true,"data":{"i":3}}`)); err != nil {
		t.Fatalf("soft tombstone: %v", err)
	}
	purged := keys[7]
	if err := c.KVPurge(ctx, bucket, purged); err != nil {
		t.Fatalf("purge: %v", err)
	}
	absent := VertexKey("identity", DeriveNanoID("ltg-lifecycle-absent", "0"))

	requested := append(append([]string{}, keys...), absent)
	got, err := c.KVGetMultiNoSnapshot(ctx, bucket, requested)
	if err != nil {
		t.Fatalf("KVGetMultiNoSnapshot: %v", err)
	}

	entry, ok := got[tombstoned]
	if !ok {
		t.Fatalf("soft-tombstoned key %s must still be returned", tombstoned)
	}
	if string(entry.Value) != `{"isDeleted":true,"data":{"i":3}}` {
		t.Errorf("tombstoned value = %s", entry.Value)
	}
	if _, ok := got[purged]; ok {
		t.Errorf("purged key %s must be absent", purged)
	}
	if _, ok := got[absent]; ok {
		t.Errorf("never-written key %s must be absent", absent)
	}
	if len(got) != n-1 {
		t.Fatalf("got %d entries, want %d (every key but the purged one)", len(got), n-1)
	}
}

// TestKVGetMultiNoSnapshot_Over1024_AgreesWithTheVerifiedDrain is the
// equivalence pin: over one quiescent dataset, resolving-then-chunking and the
// stability-verified drain return the same map — the same keys, the same
// values, the same revisions. What the two variants differ in is what they
// promise under a concurrent WRITER, not what they observe of a settled store.
func TestKVGetMultiNoSnapshot_Over1024_AgreesWithTheVerifiedDrain(t *testing.T) {
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const n = 1100
	keys := seedKeys(t, ctx, c, bucket, "identity", "ltg-equivalence", n)
	if _, err := c.KVPut(ctx, bucket, keys[5], []byte(`{"isDeleted":true,"data":{"i":5}}`)); err != nil {
		t.Fatalf("soft tombstone: %v", err)
	}

	drained, err := c.KVGetMulti(ctx, bucket, keys)
	if err != nil {
		t.Fatalf("KVGetMulti (verified drain): %v", err)
	}
	resolved, err := c.KVGetMultiNoSnapshot(ctx, bucket, keys)
	if err != nil {
		t.Fatalf("KVGetMultiNoSnapshot (list-then-get): %v", err)
	}

	if len(drained) != len(resolved) {
		t.Fatalf("drain returned %d entries, resolved read %d", len(drained), len(resolved))
	}
	for key, want := range drained {
		got, ok := resolved[key]
		if !ok {
			t.Fatalf("key %s returned by the drain is missing from the resolved read", key)
		}
		if got.Revision != want.Revision || string(got.Value) != string(want.Value) || got.Key != want.Key {
			t.Errorf("key %s: resolved = %+v, drained = %+v", key, got, want)
		}
	}
}

// TestKVGetMultiNoSnapshot_Over1024_EmptyListingIsAnEmptyMap pins the boundary
// where resolution finds nothing: a wildcard matching no key contributes no
// key, and a request that resolves to nothing reads nothing and answers with an
// empty map rather than an error or a nil.
func TestKVGetMultiNoSnapshot_Over1024_EmptyListingIsAnEmptyMap(t *testing.T) {
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	// Seed past the cap under one type so the request is refused by count, and
	// pair that wildcard with a DIFFERENT type's, which matches nothing.
	seedKeys(t, ctx, c, bucket, "identity", "ltg-empty", 1100)

	var got map[string]*KVEntry
	infos, requests, calls := resolveThenGetProfile(t, ctx, func(hooked context.Context) {
		var err error
		got, err = c.KVGetMultiNoSnapshot(hooked, bucket, []string{"vtx.identity.>", "vtx.nosuchtype.*"})
		if err != nil {
			t.Fatalf("KVGetMultiNoSnapshot: %v", err)
		}
	})

	if calls != 1 {
		t.Fatalf("the past-the-cap path ran %d times, want exactly 1", calls)
	}
	if infos != 3 {
		t.Errorf("stream-info requests = %d, want 3 — one handle plus one per wildcard, the one matching nothing included", infos)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2 — the empty filter adds no key and so no request", requests)
	}
	if len(got) != 1100 {
		t.Fatalf("got %d entries, want 1100 — an empty filter contributes nothing, not a failure", len(got))
	}

	// A request that matches nothing at all never reaches this path: the fast
	// path answers it directly, with the same non-nil empty map.
	empty, err := c.KVGetMultiNoSnapshot(ctx, bucket, []string{"vtx.nosuchtype.*", "vtx.othertype.*"})
	if err != nil {
		t.Fatalf("KVGetMultiNoSnapshot over filters matching nothing: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("got %+v, want a non-nil empty map", empty)
	}
}

// TestDrainDirectGetFallback_RetractsAKeyHardDeletedMidDrain pins the drain's
// own mid-round retraction, which is what the stability-verified KVGetMulti
// still runs past the fast path's cap.
//
// The drain accumulates entries across rounds, so a key hard-deleted while it
// is in flight arrives as a tombstone in a LATER round than the round that
// collected it live. Merely skipping that tombstone would hand the caller a
// revoked entry as live — an over-grant wherever the result is read as a live
// set. The window is unreachable from the public entry points: a single-round
// drain always sees the tombstone as its subject's last message, so only a
// delete landing between two rounds exercises the retraction, and the hook is
// what opens it.
func TestDrainDirectGetFallback_RetractsAKeyHardDeletedMidDrain(t *testing.T) {
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const n = 1100
	keys := seedKeys(t, ctx, c, bucket, "identity", "drain-retract", n)
	doomed := keys[0]
	survivors := keys[1:]

	pre := "$KV." + bucket + "."
	subjects := make([]string, len(keys))
	for i, k := range keys {
		subjects[i] = pre + k
	}

	deleted := false
	hooked := WithKVDrainRoundHook(ctx, func(int) {
		if deleted {
			return
		}
		deleted = true
		if err := c.KVDelete(context.Background(), bucket, doomed); err != nil {
			t.Errorf("mid-drain delete: %v", err)
			return
		}
		// More writes, so the drain has a further round to deliver the
		// tombstone in.
		for _, k := range survivors[:50] {
			if _, err := c.KVPut(context.Background(), bucket, k, []byte(`{"isDeleted":false}`)); err != nil {
				t.Errorf("mid-drain put: %v", err)
				return
			}
		}
	})

	got, err := c.drainDirectGetFallback(hooked, bucket, "KV_"+bucket, subjects, pre)
	if err != nil {
		t.Fatalf("drainDirectGetFallback: %v", err)
	}
	if !deleted {
		t.Fatal("the mid-drain delete must actually have run")
	}
	if _, ok := got[doomed]; ok {
		t.Errorf("key %s hard-deleted mid-drain must not come back as live", doomed)
	}
	for _, k := range survivors {
		if _, ok := got[k]; !ok {
			t.Fatalf("surviving key %s missing from the drain", k)
		}
	}
	if len(got) != n-1 {
		t.Errorf("got %d entries, want %d", len(got), n-1)
	}
}

// TestKVGetMultiNoSnapshot_Over1024_HotSubsetIsNeverResolvedAway is the
// adversarial vector, and the one that decides the resolution strategy.
//
// A consumer-backed key enumeration stops on `received >= initPending ||
// delta == 0`, with initPending captured when the consumer was created
// (nats.go v1.52.0, jetstream/kv.go). Rewriting a key erases the message that
// count was counting and appends a new one at the tail, so the count can be
// satisfied while the rewritten subject is still ahead of the cursor — the
// enumeration stops SHORT, silently. Two such enumerations do not fix it:
// the loss is CAUSED by the rewrite, so when the rewrites are correlated on a
// hot subset both drop the same key, agree, and the read returns short with no
// error. The fixture that hides this is the round-robin writer; this one is
// the correlated writer that exposes it.
//
// Resolving from the stream's own subject state has no such stop condition, so
// the hot key is in every resolution and every answer.
func TestKVGetMultiNoSnapshot_Over1024_HotSubsetIsNeverResolvedAway(t *testing.T) {
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const n = 1100
	keys := seedKeys(t, ctx, c, bucket, "identity", "ltg-hot", n)
	hot := keys[0]

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := c.KVPut(context.Background(), bucket, hot, []byte(`{"isDeleted":false,"data":{"i":0}}`)); err != nil {
				return
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	missingFromResolution, missingFromAnswer, errs := 0, 0, 0
	for attempt := 0; attempt < 20; attempt++ {
		sawHot := false
		hooked := WithKVResolvedKeysHook(ctx, func(resolved []string) {
			for _, k := range resolved {
				if k == hot {
					sawHot = true
					return
				}
			}
		})
		got, err := c.KVGetMultiNoSnapshot(hooked, bucket, []string{"vtx.identity.*"})
		if err != nil {
			errs++
			continue
		}
		if !sawHot {
			missingFromResolution++
		}
		if _, ok := got[hot]; !ok {
			missingFromAnswer++
		}
		if len(got) != n {
			t.Errorf("attempt %d returned %d entries, want %d", attempt, len(got), n)
		}
	}

	if missingFromResolution != 0 {
		t.Errorf("the rewritten key was resolved away on %d of 20 reads — the resolution is not complete under a correlated writer", missingFromResolution)
	}
	if missingFromAnswer != 0 {
		t.Errorf("the rewritten key was missing from %d of 20 answers", missingFromAnswer)
	}
	if errs != 0 {
		t.Errorf("%d of 20 reads errored; a rewrite must cost neither correctness nor availability here", errs)
	}
}

// TestKVGetMultiNoSnapshot_Over1024_LiteralAndWildcardOverlapAskedOnce pins the
// de-duplication the contract claims in both directions: a repeated filter is
// resolved once, and a literal a filter also matches is asked for once. Both
// are shapes a consumer's FilterSubjects rejects outright, and here they are
// just a set union.
func TestKVGetMultiNoSnapshot_Over1024_LiteralAndWildcardOverlapAskedOnce(t *testing.T) {
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const n = 1100
	keys := seedKeys(t, ctx, c, bucket, "identity", "ltg-overlap", n)

	var resolved []string
	var got map[string]*KVEntry
	infos, requests, calls := resolveThenGetProfile(t, ctx, func(hooked context.Context) {
		hooked = WithKVResolvedKeysHook(hooked, func(k []string) { resolved = append([]string{}, k...) })
		var err error
		got, err = c.KVGetMultiNoSnapshot(hooked, bucket,
			// The same wildcard twice, plus two literals it already matches.
			[]string{"vtx.identity.*", "vtx.identity.*", keys[0], keys[1]})
		if err != nil {
			t.Fatalf("KVGetMultiNoSnapshot: %v", err)
		}
	})

	if calls != 1 {
		t.Fatalf("the past-the-cap path ran %d times, want exactly 1", calls)
	}
	if infos != 2 {
		t.Errorf("stream-info requests = %d, want 2 — one handle plus ONE resolution, a repeated filter costing nothing extra", infos)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
	if len(resolved) != n {
		t.Errorf("resolved %d keys, want %d — a literal a filter also matches must not be asked for twice", len(resolved), n)
	}
	seen := make(map[string]struct{}, len(resolved))
	for _, k := range resolved {
		if _, dup := seen[k]; dup {
			t.Fatalf("key %s resolved twice", k)
		}
		seen[k] = struct{}{}
	}
	if len(got) != n {
		t.Fatalf("got %d entries, want %d", len(got), n)
	}
}
