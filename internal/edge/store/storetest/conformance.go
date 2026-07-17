// Package storetest is the conformance suite for the Edge node's Local VAL
// Store (internal/edge/store): the executable definition of what a Store
// implementation must do, run against every backing engine.
//
// The semantics packages (internal/edge/{overlay,sync,agent,vault}) are
// written against store.Store's behaviour rather than any one engine, so that
// behaviour has to be pinned somewhere both engines answer to. Run is that
// gate: the bbolt store passes it today, and a browser host's IndexedDB store
// passes the same suite before the semantics are pointed at it — the check
// that a port preserved the semantics rather than merely compiling.
package storetest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/edge/store"
)

const testVtxKey = "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T"

// Factory builds stores for the conformance suite.
type Factory interface {
	// New opens a fresh, empty store. It must be usable until the test ends;
	// the implementation is responsible for cleanup (t.Cleanup).
	New(t *testing.T) store.Store
	// Reopen closes s and reopens a store over the same underlying storage,
	// modelling a process restart. Everything durably written through s must
	// still be readable through the returned store.
	Reopen(t *testing.T, s store.Store) store.Store
}

// Run executes the full Store conformance suite against f's implementation.
func Run(t *testing.T, f Factory) {
	t.Helper()
	t.Run("ApplyUpsert_AppliesNewerRevision", func(t *testing.T) { applyUpsertAppliesNewerRevision(t, f) })
	t.Run("ApplyUpsert_DropsStaleRevision", func(t *testing.T) { applyUpsertDropsStaleRevision(t, f) })
	t.Run("ApplyUpsert_DropsDuplicateRevision", func(t *testing.T) { applyUpsertAppliesDuplicateRevision(t, f) })
	t.Run("ApplyDelete_TombstonesOnDelete", func(t *testing.T) { applyDeleteTombstones(t, f) })
	t.Run("ApplyDelete_DropsStaleRevision", func(t *testing.T) { applyDeleteDropsStaleRevision(t, f) })
	t.Run("ApplyUpsertAndDelete_RejectNonContract1Keys", func(t *testing.T) { rejectNonContract1Keys(t, f) })
	t.Run("ApplyUpsertAndDelete_AcceptManifestKeys", func(t *testing.T) { acceptManifestKeys(t, f) })
	t.Run("Get_AbsentKeyReturnsNotOK", func(t *testing.T) { getAbsentKey(t, f) })
	t.Run("Cursor_PersistsAcrossReopen", func(t *testing.T) { cursorPersistsAcrossReopen(t, f) })
	t.Run("LocalNamespace_NeverVisibleThroughGet", func(t *testing.T) { localNamespaceNeverVisible(t, f) })
	t.Run("GetLocal_AbsentNameReturnsNotOK", func(t *testing.T) { getLocalAbsentName(t, f) })
	t.Run("Pending_RoundTripsAndDeletes", func(t *testing.T) { pendingRoundTrips(t, f) })
	t.Run("ScanPrefix_ReturnsOnlyMatchingConfirmedEntries", func(t *testing.T) { scanPrefixMatchesOnly(t, f) })
	t.Run("IntentQueue_FIFOAndDelete", func(t *testing.T) { intentQueueFIFO(t, f) })
	t.Run("IntentQueue_PersistsAcrossReopen", func(t *testing.T) { intentQueuePersistsAcrossReopen(t, f) })
}

func applyUpsertAppliesNewerRevision(t *testing.T, f Factory) {
	s := f.New(t)

	applied, err := s.ApplyUpsert(testVtxKey, 5, []byte(`{"monthlyRent":2400}`))
	require.NoError(t, err)
	require.True(t, applied)

	entry, ok, err := s.Get(testVtxKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(5), entry.Revision)
	require.JSONEq(t, `{"monthlyRent":2400}`, string(entry.Data))
	require.False(t, entry.Deleted)

	applied, err = s.ApplyUpsert(testVtxKey, 9, []byte(`{"monthlyRent":2500}`))
	require.NoError(t, err)
	require.True(t, applied, "a strictly newer revision must apply")

	entry, ok, err = s.Get(testVtxKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(9), entry.Revision)
	require.JSONEq(t, `{"monthlyRent":2500}`, string(entry.Data))
}

func applyUpsertDropsStaleRevision(t *testing.T, f Factory) {
	s := f.New(t)

	_, err := s.ApplyUpsert(testVtxKey, 9, []byte(`{"monthlyRent":2500}`))
	require.NoError(t, err)

	applied, err := s.ApplyUpsert(testVtxKey, 5, []byte(`{"monthlyRent":2400}`))
	require.NoError(t, err)
	require.False(t, applied, "an older revision must be dropped, not applied")

	entry, ok, err := s.Get(testVtxKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(9), entry.Revision, "the stale write must not overwrite the newer entry")
}

func applyUpsertAppliesDuplicateRevision(t *testing.T, f Factory) {
	s := f.New(t)

	applied, err := s.ApplyUpsert(testVtxKey, 7, []byte(`{"a":1}`))
	require.NoError(t, err)
	require.True(t, applied)

	// revision == stored revision is treated as an at-least-once redelivery of
	// the same delta, not a newer write — LWW's "≥" gate still lets it
	// re-apply the same content, exercising the boundary (revision < stored is
	// the only drop condition).
	applied, err = s.ApplyUpsert(testVtxKey, 7, []byte(`{"a":1}`))
	require.NoError(t, err)
	require.True(t, applied, "a duplicate (equal) revision redelivery must be idempotent-applied, not error")
}

func applyDeleteTombstones(t *testing.T, f Factory) {
	s := f.New(t)

	_, err := s.ApplyUpsert(testVtxKey, 3, []byte(`{"a":1}`))
	require.NoError(t, err)

	applied, err := s.ApplyDelete(testVtxKey, 4)
	require.NoError(t, err)
	require.True(t, applied)

	entry, ok, err := s.Get(testVtxKey)
	require.NoError(t, err)
	require.True(t, ok, "a tombstone is a retained entry, not an absent one")
	require.True(t, entry.Deleted)
	require.Equal(t, uint64(4), entry.Revision)
}

func applyDeleteDropsStaleRevision(t *testing.T, f Factory) {
	s := f.New(t)

	_, err := s.ApplyUpsert(testVtxKey, 10, []byte(`{"a":1}`))
	require.NoError(t, err)

	applied, err := s.ApplyDelete(testVtxKey, 2)
	require.NoError(t, err)
	require.False(t, applied, "a delete older than the stored revision must be dropped")

	entry, ok, err := s.Get(testVtxKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, entry.Deleted, "the stale delete must not tombstone the newer entry")
}

func rejectNonContract1Keys(t *testing.T, f Factory) {
	s := f.New(t)

	_, err := s.ApplyUpsert("not-a-valid-key", 1, []byte(`{}`))
	require.Error(t, err)

	_, err = s.ApplyDelete("vtx.badtype", 1)
	require.Error(t, err)
}

func acceptManifestKeys(t *testing.T, f Factory) {
	s := f.New(t)

	applied, err := s.ApplyUpsert("manifest.me", 1, []byte(`{"displayName":"Riley"}`))
	require.NoError(t, err)
	require.True(t, applied)

	entry, ok, err := s.Get("manifest.me")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(1), entry.Revision)

	applied, err = s.ApplyDelete("manifest.me", 2)
	require.NoError(t, err)
	require.True(t, applied)
}

func getAbsentKey(t *testing.T, f Factory) {
	s := f.New(t)

	entry, ok, err := s.Get(testVtxKey)
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, entry)
}

func cursorPersistsAcrossReopen(t *testing.T, f Factory) {
	s := f.New(t)

	_, ok, err := s.Cursor()
	require.NoError(t, err)
	require.False(t, ok, "a fresh store has no cursor — the node must hydrate")

	require.NoError(t, s.SetCursor(42))
	seq, ok, err := s.Cursor()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(42), seq)

	reopened := f.Reopen(t, s)
	seq, ok, err = reopened.Cursor()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(42), seq, "the resume cursor must survive a process restart")
}

func localNamespaceNeverVisible(t *testing.T, f Factory) {
	s := f.New(t)

	require.NoError(t, s.PutLocal("draft-note-1", []byte(`{"text":"private"}`)))

	data, ok, err := s.GetLocal("draft-note-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.JSONEq(t, `{"text":"private"}`, string(data))

	// A local-only name is never mirrored-key shaped, so it can never collide
	// with — or be surfaced through — the Contract #1 Get path.
	_, ok, err = s.Get("draft-note-1")
	require.NoError(t, err)
	require.False(t, ok)
}

func getLocalAbsentName(t *testing.T, f Factory) {
	s := f.New(t)

	data, ok, err := s.GetLocal("nope")
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, data)
}

func pendingRoundTrips(t *testing.T, f Factory) {
	s := f.New(t)

	_, ok, err := s.GetPending(testVtxKey)
	require.NoError(t, err)
	require.False(t, ok)

	entry := store.PendingEntry{Key: testVtxKey, RequestID: "req1", Data: []byte(`{"a":1}`), BaseRevision: 3}
	require.NoError(t, s.PutPending(entry))

	got, ok, err := s.GetPending(testVtxKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, entry.RequestID, got.RequestID)
	require.Equal(t, entry.BaseRevision, got.BaseRevision)
	require.JSONEq(t, `{"a":1}`, string(got.Data))

	list, err := s.ListPending()
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, s.DeletePending(testVtxKey))
	_, ok, err = s.GetPending(testVtxKey)
	require.NoError(t, err)
	require.False(t, ok)

	list, err = s.ListPending()
	require.NoError(t, err)
	require.Empty(t, list)
}

func scanPrefixMatchesOnly(t *testing.T, f Factory) {
	s := f.New(t)

	linkA := "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.hasBooking.booking.Bk2Pn6mQrtwzKbcXvP3T"
	linkB := "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.hasBooking.booking.Ck2Pn6mQrtwzKbcXvP3T"
	other := "lnk.lease.Zk2Pn6mQrtwzKbcXvP3T.hasBooking.booking.Bk2Pn6mQrtwzKbcXvP3T"

	_, err := s.ApplyUpsert(linkA, 1, []byte(`{}`))
	require.NoError(t, err)
	_, err = s.ApplyUpsert(linkB, 1, []byte(`{}`))
	require.NoError(t, err)
	_, err = s.ApplyUpsert(other, 1, []byte(`{}`))
	require.NoError(t, err)

	entries, err := s.ScanPrefix("lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.hasBooking.")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	keys := []string{entries[0].Key, entries[1].Key}
	require.ElementsMatch(t, []string{linkA, linkB}, keys)
}

func intentQueueFIFO(t *testing.T, f Factory) {
	s := f.New(t)

	seq1, err := s.EnqueueIntent([]byte(`{"requestId":"one"}`))
	require.NoError(t, err)
	seq2, err := s.EnqueueIntent([]byte(`{"requestId":"two"}`))
	require.NoError(t, err)
	require.Less(t, seq1, seq2)

	list, err := s.ListIntents()
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, seq1, list[0].Seq)
	require.Equal(t, seq2, list[1].Seq)
	require.JSONEq(t, `{"requestId":"one"}`, string(list[0].Envelope))

	require.NoError(t, s.DeleteIntent(seq1))
	list, err = s.ListIntents()
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, seq2, list[0].Seq)
}

func intentQueuePersistsAcrossReopen(t *testing.T, f Factory) {
	s := f.New(t)
	_, err := s.EnqueueIntent([]byte(`{"requestId":"one"}`))
	require.NoError(t, err)

	reopened := f.Reopen(t, s)
	list, err := reopened.ListIntents()
	require.NoError(t, err)
	require.Len(t, list, 1, "a queued intent must survive a process restart")
}
