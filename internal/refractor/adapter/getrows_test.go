package adapter_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// TestNatsKVAdapter_GetRows_ChunksStripsAndSurvivesACorruptMember is
// GetRows' whole contract on one bucket, at a size that forces the batched
// read past the substrate primitive's 1,024-subject fast path — the boundary
// where a caller that assumed one request silently becomes several.
//
// Four properties, all in one fixture because they are one read:
//
//   - CHUNKING. 1,100 live keys are requested in one call and every one comes
//     back. Below the cap this proves nothing; above it, a resolve-then-chunk
//     that dropped a chunk would show up as a short map.
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

	rows, err := a.GetRows(ctx, append(append([]string{}, keys...), absent))
	require.NoError(t, err, "a per-member problem must never fail the batch")

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
