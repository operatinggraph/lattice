package adapter_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestNatsKVAdapter_GetRows_ChunksStripsAndSurvivesACorruptMember is
// GetRows' whole contract on one bucket, at a size that forces the batched
// read past the substrate primitive's 1,024-subject fast path — the boundary
// where a caller that assumed one request silently becomes several.
//
// Four properties, all in one fixture because they are one read:
//
//   - CHUNKING, pinned as a REQUEST PROFILE and not merely as a complete map.
//     1,100 live keys — deliberately just past the substrate's fast-path
//     subject cap — come back whole, AND the read is served by exactly
//     ceil(N/cap) fast-path requests, each carrying at most the cap. That is
//     the bound GetRows' doc states, and a complete map is no evidence for it:
//     the same map comes back from one refused request plus a resolution plus
//     the same chunks, which is what the primitive does when a caller hands it
//     the whole set. Only the per-request count tells the two apart.
//   - TOMBSTONES ARE ABSENT. A soft-deleted entry is missing from the result,
//     the same answer GetRow gives for one, so the caller reads it as "no
//     stored body" and writes the entry.
//   - projectionSeq IS STRIPPED. The guard's bookkeeping field never reaches a
//     caller that is about to compare the body against a freshly computed one
//     which has no such field.
//   - A CORRUPT MEMBER FAILS ONLY WHERE IT IS USED. One unparseable value
//     among the 1,100 is dropped from the result and the batch still returns
//     every sibling — the dossier's mandated shape for any set read
//     (TestPrefetch_CorruptBodyFailsOnlyWhereItIsUsed).
func TestNatsKVAdapter_GetRows_ChunksStripsAndSurvivesACorruptMember(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})
	ctx := context.Background()

	const total = 1100
	keys := make([]string, 0, total)
	for i := range total {
		key := fmt.Sprintf("cap.rows.identity.A.%04d", i)
		keys = append(keys, key)
		require.NoError(t, a.Upsert(ctx, map[string]any{"key": key},
			map[string]any{"anchorType": "operation", "via": "role"}, uint64(i+1)))
	}

	// One member soft-tombstoned, one member's stored value replaced with
	// something no JSON decoder will accept.
	tombstoned := keys[7]
	require.NoError(t, a.Delete(ctx, map[string]any{"key": tombstoned}, total+1))
	corrupt := keys[13]
	_, err := kv.Put(ctx, corrupt, []byte("{not json"))
	require.NoError(t, err)

	// One key that was never written: absent, like the tombstoned one.
	absent := "cap.rows.identity.A.9999"

	requested := append(append([]string{}, keys...), absent)

	// Every fast-path request this read issues, in order, by subject count.
	var perRequest []int
	var mu sync.Mutex
	hooked := substrate.WithKVFastPathRequestHook(ctx, func(subjects int) {
		mu.Lock()
		perRequest = append(perRequest, subjects)
		mu.Unlock()
	})

	rows, err := a.GetRows(hooked, requested)
	require.NoError(t, err, "a per-member problem must never fail the batch")

	subjectCap := substrate.KVDirectGetSubjectCap
	require.Equal(t, 1024, subjectCap,
		"the fast path's subject cap is the server's number and GetRows' bound is stated in terms of it; "+
			"a change here changes that bound and must be read, not absorbed")
	wantRequests := (len(requested) + subjectCap - 1) / subjectCap
	require.Equal(t, 2, wantRequests, "1,101 keys past a 1,024 cap is two requests — the vector is on the right side of the boundary")
	require.Len(t, perRequest, wantRequests,
		"GetRows must issue exactly ceil(N/%d) requests; got %v — more than that means it fell off the fast path "+
			"and paid a refused request plus a resolution", subjectCap, perRequest)
	for i, subjects := range perRequest {
		require.LessOrEqual(t, subjects, subjectCap, "request %d carried %d subjects, past the fast path's cap", i, subjects)
	}
	asked := 0
	for _, subjects := range perRequest {
		asked += subjects
	}
	require.Equal(t, len(requested), asked, "every requested key is asked for exactly once across the chunks")

	require.Len(t, rows, total-2,
		"every live, parseable key comes back; the tombstoned and the corrupt one do not")
	require.NotContains(t, rows, tombstoned, "a tombstone reads as absent, exactly as GetRow reports one")
	require.NotContains(t, rows, corrupt, "an unparseable member is dropped, not raised")
	require.NotContains(t, rows, absent, "a key that was never written is simply missing")

	sample := rows[keys[0]]
	require.NotNil(t, sample)
	require.NotContains(t, sample, "projectionSeq",
		"the guard's bookkeeping field must never reach a caller comparing against a freshly computed body")
	require.Equal(t, "operation", sample["anchorType"])
	require.Equal(t, "role", sample["via"])

	// The empty request is a request nobody makes: it must cost no round trip
	// and answer with an empty map, not nil, so a caller can range over it.
	empty, err := a.GetRows(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, empty)
	require.Empty(t, empty)
}

// TestNatsKVAdapter_SatisfiesRowsReader pins the seam the pipeline's arming
// conjunct reads: withholding is armed only for an adapter that is BOTH a
// RowsReader and a guarded SeqGuarded, and the type assertion that asks is
// silent when it fails — a NatsKVAdapter that stopped satisfying the
// interface would disarm every perEntry lens with nothing failing anywhere.
func TestNatsKVAdapter_SatisfiesRowsReader(t *testing.T) {
	kv := startKV(t)
	a := guardedAdapter(t, kv, []string{"key"})

	var _ adapter.RowsReader = a
	rr, ok := any(a).(adapter.RowsReader)
	require.True(t, ok, "NatsKVAdapter must satisfy adapter.RowsReader")
	require.NotNil(t, rr)

	guard, ok := any(a).(adapter.SeqGuarded)
	require.True(t, ok)
	require.True(t, guard.Guarded())
}
