package substrate

import (
	"errors"
	"fmt"
	"testing"
)

func TestKVGetMulti_ExactKeys_MixedPresentAbsent(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	present1 := VertexKey("identity", testNanoID1)
	present2 := VertexKey("identity", testNanoID2)
	absent := VertexKey("identity", testNanoID3)

	rev1, err := c.KVPut(ctx, bucket, present1, []byte(`{"isDeleted":false,"data":{"n":1}}`))
	if err != nil {
		t.Fatalf("put present1: %v", err)
	}
	rev2, err := c.KVPut(ctx, bucket, present2, []byte(`{"isDeleted":false,"data":{"n":2}}`))
	if err != nil {
		t.Fatalf("put present2: %v", err)
	}

	got, err := c.KVGetMulti(ctx, bucket, []string{present1, present2, absent})
	if err != nil {
		t.Fatalf("KVGetMulti: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	e1, ok := got[present1]
	if !ok {
		t.Fatalf("missing present1 (%s) in result: %+v", present1, got)
	}
	if e1.Bucket != bucket || e1.Key != present1 || e1.Revision != rev1 {
		t.Errorf("present1 entry = %+v, want bucket=%s key=%s revision=%d", e1, bucket, present1, rev1)
	}
	if string(e1.Value) != `{"isDeleted":false,"data":{"n":1}}` {
		t.Errorf("present1 value = %s", e1.Value)
	}
	e2, ok := got[present2]
	if !ok || e2.Revision != rev2 {
		t.Fatalf("present2 entry = %+v, ok=%v, want revision=%d", e2, ok, rev2)
	}
	if _, ok := got[absent]; ok {
		t.Errorf("absent key %q unexpectedly present in result", absent)
	}
}

func TestKVGetMulti_EmptyKeys_ReturnsEmptyMap(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	got, err := c.KVGetMulti(ctx, bucket, nil)
	if err != nil {
		t.Fatalf("KVGetMulti(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries for empty keys, want 0: %+v", len(got), got)
	}
}

func TestKVGetMulti_NoMatches_ReturnsEmptyMap(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	// Bucket has nothing in it at all: the server's 404 "No Results" path.
	got, err := c.KVGetMulti(ctx, bucket, []string{VertexKey("identity", testNanoID1)})
	if err != nil {
		t.Fatalf("KVGetMulti on empty bucket: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0: %+v", len(got), got)
	}
}

// TestKVGetMulti_SoftTombstone_StillLive mirrors KVGet's documented
// semantics (kv.go:29-34): an envelope written with "isDeleted": true
// remains a live JetStream message and is returned normally — only a NATS
// hard delete/purge is excluded.
func TestKVGetMulti_SoftTombstone_StillLive(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	if _, err := c.KVPut(ctx, bucket, key, []byte(`{"isDeleted":true}`)); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := c.KVGetMulti(ctx, bucket, []string{key})
	if err != nil {
		t.Fatalf("KVGetMulti: %v", err)
	}
	entry, ok := got[key]
	if !ok {
		t.Fatalf("soft-tombstoned key missing from result: %+v", got)
	}
	if string(entry.Value) != `{"isDeleted":true}` {
		t.Errorf("value = %s", entry.Value)
	}
}

// TestKVGetMulti_HardDeleteAndPurge_Absent covers both NATS-level tombstone
// shapes (KV-Operation: DEL from KVDelete, KV-Operation: PURGE from
// KVPurge) — each must exclude the key from the result, matching KVGet's
// ErrKeyNotFound-after-hard-delete behavior.
func TestKVGetMulti_HardDeleteAndPurge_Absent(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	deletedKey := VertexKey("identity", testNanoID1)
	purgedKey := VertexKey("identity", testNanoID2)
	liveKey := VertexKey("identity", testNanoID3)

	for _, k := range []string{deletedKey, purgedKey, liveKey} {
		if _, err := c.KVPut(ctx, bucket, k, []byte(`{"isDeleted":false}`)); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	if err := c.KVDelete(ctx, bucket, deletedKey); err != nil {
		t.Fatalf("KVDelete: %v", err)
	}
	if err := c.KVPurge(ctx, bucket, purgedKey); err != nil {
		t.Fatalf("KVPurge: %v", err)
	}

	got, err := c.KVGetMulti(ctx, bucket, []string{deletedKey, purgedKey, liveKey})
	if err != nil {
		t.Fatalf("KVGetMulti: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 (only liveKey): %+v", len(got), got)
	}
	if _, ok := got[liveKey]; !ok {
		t.Errorf("liveKey missing from result: %+v", got)
	}
}

// TestKVGetMulti_Filter exercises a wildcard filter subject alongside an
// exact key in the same request — the "exact lists + filters" combined
// capability the ratified primitive names.
func TestKVGetMulti_Filter(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	link1 := "lnk.object." + testNanoID1 + ".photoOf.identity." + testNanoID2
	link2 := "lnk.object." + testNanoID2 + ".signedLeaseOf.leaseapp." + testNanoID1
	other := VertexKey("identity", testNanoID1)

	for _, k := range []string{link1, link2, other} {
		if _, err := c.KVPut(ctx, bucket, k, []byte(`{"isDeleted":false}`)); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}

	got, err := c.KVGetMulti(ctx, bucket, []string{"lnk.object.>"})
	if err != nil {
		t.Fatalf("KVGetMulti: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if _, ok := got[link1]; !ok {
		t.Errorf("link1 missing: %+v", got)
	}
	if _, ok := got[link2]; !ok {
		t.Errorf("link2 missing: %+v", got)
	}
	if _, ok := got[other]; ok {
		t.Errorf("out-of-filter key %q unexpectedly matched", other)
	}
}

// TestKVGetMulti_Over1024_FallbackServesCorrectData exercises the 413 ->
// stability-verified fallback end to end: seed past the server's
// 1,024-matched-subject cap and confirm the fallback returns the complete,
// correct set (not a truncated fast-path response, not an error).
func TestKVGetMulti_Over1024_FallbackServesCorrectData(t *testing.T) {
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const n = 1100
	keys := make([]string, n)
	for i := 0; i < n; i++ {
		id := DeriveNanoID("kv-multi-fallback-test", fmt.Sprintf("%d", i))
		key := VertexKey("identity", id)
		if _, err := c.KVPut(ctx, bucket, key, []byte(fmt.Sprintf(`{"isDeleted":false,"data":{"i":%d}}`, i))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
		keys[i] = key
	}

	got, err := c.KVGetMulti(ctx, bucket, keys)
	if err != nil {
		t.Fatalf("KVGetMulti over 1024 keys: %v", err)
	}
	if len(got) != n {
		t.Fatalf("got %d entries, want %d", len(got), n)
	}
	for i, k := range keys {
		entry, ok := got[k]
		if !ok {
			t.Fatalf("key %d (%s) missing from fallback result", i, k)
		}
		want := fmt.Sprintf(`{"isDeleted":false,"data":{"i":%d}}`, i)
		if string(entry.Value) != want {
			t.Errorf("key %d value = %s, want %s", i, entry.Value, want)
		}
	}
}

func TestKVGetMulti_UnprovisionedBucket_Errors(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)

	_, err := c.KVGetMulti(ctx, "no-such-bucket", []string{VertexKey("identity", testNanoID1)})
	if err == nil {
		t.Fatal("expected an error for an unprovisioned bucket, got nil")
	}
	if errors.Is(err, errDirectGetTooManyResults) || errors.Is(err, errDirectGetShortRead) {
		t.Errorf("unprovisioned-bucket error should not be classified as a retry/fallback signal: %v", err)
	}
}

func TestKV_GetMulti_Delegate(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	key := VertexKey("identity", testNanoID1)
	if _, err := c.KVPut(ctx, bucket, key, []byte(`{"isDeleted":false}`)); err != nil {
		t.Fatalf("put: %v", err)
	}

	kv, err := c.OpenKV(ctx, bucket)
	if err != nil {
		t.Fatalf("OpenKV: %v", err)
	}
	got, err := kv.GetMulti(ctx, []string{key})
	if err != nil {
		t.Fatalf("GetMulti: %v", err)
	}
	if _, ok := got[key]; !ok {
		t.Errorf("delegate did not return %q: %+v", key, got)
	}
}
