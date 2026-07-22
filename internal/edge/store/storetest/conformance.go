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

	"github.com/operatinggraph/lattice/internal/edge/store"
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
	t.Run("ApplyKeySet_PrunesOmittedAttributedKey", func(t *testing.T) { applyKeySetPrunesOmittedKey(t, f) })
	t.Run("ApplyKeySet_EmptyFrameRetractsLastRow", func(t *testing.T) { applyKeySetEmptyFrameRetractsLastRow(t, f) })
	t.Run("ApplyKeySet_DropsStaleFrame", func(t *testing.T) { applyKeySetDropsStaleFrame(t, f) })
	t.Run("ApplyUpsert_AttributionSurvivesBodyLWWLoss", func(t *testing.T) { applyUpsertAttributionSurvivesBodyLoss(t, f) })
	t.Run("ApplyUpsert_FrameHWGuardDropsResurrectingRedelivery", func(t *testing.T) { applyUpsertFrameHWGuardDropsResurrection(t, f) })
	t.Run("ApplyKeySet_SameKeyTwoLensOverlapRefcountSurvives", func(t *testing.T) { applyKeySetTwoLensRefcountSurvives(t, f) })
	t.Run("PruneDeadLensAttributions_TombstonesWhenLastLensDies", func(t *testing.T) { pruneDeadLensTombstonesLastLens(t, f) })
	t.Run("PruneDeadLensAttributions_KeepsKeyWithSurvivingLens", func(t *testing.T) { pruneDeadLensKeepsSurvivingLens(t, f) })
	t.Run("ApplyUpsert_UnattributedWritePreservesExistingSources", func(t *testing.T) { applyUpsertUnattributedPreservesSources(t, f) })
}

func applyUpsertAppliesNewerRevision(t *testing.T, f Factory) {
	s := f.New(t)

	applied, err := s.ApplyUpsert(testVtxKey, "", 5, []byte(`{"monthlyRent":2400}`))
	require.NoError(t, err)
	require.True(t, applied)

	entry, ok, err := s.Get(testVtxKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(5), entry.Revision)
	require.JSONEq(t, `{"monthlyRent":2400}`, string(entry.Data))
	require.False(t, entry.Deleted)

	applied, err = s.ApplyUpsert(testVtxKey, "", 9, []byte(`{"monthlyRent":2500}`))
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

	_, err := s.ApplyUpsert(testVtxKey, "", 9, []byte(`{"monthlyRent":2500}`))
	require.NoError(t, err)

	applied, err := s.ApplyUpsert(testVtxKey, "", 5, []byte(`{"monthlyRent":2400}`))
	require.NoError(t, err)
	require.False(t, applied, "an older revision must be dropped, not applied")

	entry, ok, err := s.Get(testVtxKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(9), entry.Revision, "the stale write must not overwrite the newer entry")
}

func applyUpsertAppliesDuplicateRevision(t *testing.T, f Factory) {
	s := f.New(t)

	applied, err := s.ApplyUpsert(testVtxKey, "", 7, []byte(`{"a":1}`))
	require.NoError(t, err)
	require.True(t, applied)

	// revision == stored revision is treated as an at-least-once redelivery of
	// the same delta, not a newer write — LWW's "≥" gate still lets it
	// re-apply the same content, exercising the boundary (revision < stored is
	// the only drop condition).
	applied, err = s.ApplyUpsert(testVtxKey, "", 7, []byte(`{"a":1}`))
	require.NoError(t, err)
	require.True(t, applied, "a duplicate (equal) revision redelivery must be idempotent-applied, not error")
}

func applyDeleteTombstones(t *testing.T, f Factory) {
	s := f.New(t)

	_, err := s.ApplyUpsert(testVtxKey, "", 3, []byte(`{"a":1}`))
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

	_, err := s.ApplyUpsert(testVtxKey, "", 10, []byte(`{"a":1}`))
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

	_, err := s.ApplyUpsert("not-a-valid-key", "", 1, []byte(`{}`))
	require.Error(t, err)

	_, err = s.ApplyDelete("vtx.badtype", 1)
	require.Error(t, err)
}

func acceptManifestKeys(t *testing.T, f Factory) {
	s := f.New(t)

	applied, err := s.ApplyUpsert("manifest.me", "", 1, []byte(`{"displayName":"Riley"}`))
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

	_, err := s.ApplyUpsert(linkA, "", 1, []byte(`{}`))
	require.NoError(t, err)
	_, err = s.ApplyUpsert(linkB, "", 1, []byte(`{}`))
	require.NoError(t, err)
	_, err = s.ApplyUpsert(other, "", 1, []byte(`{}`))
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

const (
	testLensQueued   = "lensQueued00000000AB"
	testLensAssigned = "lensAssigned0000000CD"
)

func applyKeySetPrunesOmittedKey(t *testing.T, f Factory) {
	s := f.New(t)

	keyA := "manifest.task.aaaaaaaaaaaaaaaaaaaa"
	keyB := "manifest.task.bbbbbbbbbbbbbbbbbbbb"
	_, err := s.ApplyUpsert(keyA, testLensQueued, 1, []byte(`{"a":1}`))
	require.NoError(t, err)
	_, err = s.ApplyUpsert(keyB, testLensQueued, 1, []byte(`{"a":1}`))
	require.NoError(t, err)

	pruned, applied, err := s.ApplyKeySet(testLensQueued, 5, []string{keyB})
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, []string{keyA}, pruned, "keyA dropped out of the frame, keyB stayed in it")

	entry, ok, err := s.Get(keyA)
	require.NoError(t, err)
	require.True(t, ok, "a retracted key is a tombstone, not an absent entry")
	require.True(t, entry.Deleted)
	require.Empty(t, entry.Data, "a tombstone must not retain the last-known body — exactly as ApplyDelete clears it")

	entry, ok, err = s.Get(keyB)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, entry.Deleted, "a key still named in the frame must survive")
}

func applyKeySetEmptyFrameRetractsLastRow(t *testing.T, f Factory) {
	s := f.New(t)

	key := "manifest.task.cccccccccccccccccccc"
	_, err := s.ApplyUpsert(key, testLensQueued, 1, []byte(`{"a":1}`))
	require.NoError(t, err)

	pruned, applied, err := s.ApplyKeySet(testLensQueued, 5, nil)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, []string{key}, pruned, "an empty frame is the last-row-retraction case")

	entry, ok, err := s.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, entry.Deleted)
}

func applyKeySetDropsStaleFrame(t *testing.T, f Factory) {
	s := f.New(t)

	key := "manifest.task.dddddddddddddddddddd"
	_, err := s.ApplyUpsert(key, testLensQueued, 1, []byte(`{"a":1}`))
	require.NoError(t, err)

	_, applied, err := s.ApplyKeySet(testLensQueued, 10, []string{key})
	require.NoError(t, err)
	require.True(t, applied)

	// A frame at an older revision than the last one applied for this lens
	// must be dropped whole — a redelivered/reordered stale frame must not
	// regress the lens's known-current keyset.
	pruned, applied, err := s.ApplyKeySet(testLensQueued, 3, nil)
	require.NoError(t, err)
	require.False(t, applied, "a frame older than the applied high-water mark must be dropped")
	require.Empty(t, pruned)

	entry, ok, err := s.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, entry.Deleted, "the stale frame must not retract a key the newer frame kept")
}

func applyUpsertAttributionSurvivesBodyLoss(t *testing.T, f Factory) {
	s := f.New(t)

	key := "manifest.task.eeeeeeeeeeeeeeeeeeee"
	_, err := s.ApplyUpsert(key, testLensAssigned, 10, []byte(`{"body":"fromAssigned"}`))
	require.NoError(t, err)

	// A same-key upsert from a different, lower-numbered lens loses the body
	// LWW race (10 > 3), but its own source's attribution must still record —
	// otherwise this lens's refcount undercounts and its next frame prunes a
	// key it still legitimately asserts.
	applied, err := s.ApplyUpsert(key, testLensQueued, 3, []byte(`{"body":"fromQueued"}`))
	require.NoError(t, err)
	require.False(t, applied, "the body write itself lost the LWW race")

	entry, ok, err := s.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(10), entry.Revision, "the winning lens's body must be unchanged")
	require.JSONEq(t, `{"body":"fromAssigned"}`, string(entry.Data))
	require.Equal(t, uint64(3), entry.Sources[testLensQueued], "the losing lens's attribution must have recorded despite losing the body race")
	require.Equal(t, uint64(10), entry.Sources[testLensAssigned])

	// The losing lens's own empty-keyset frame retracts only ITS attribution
	// — the winning lens still asserts the key, so the refcount keeps the row
	// alive.
	pruned, applied, err := s.ApplyKeySet(testLensQueued, 3, nil)
	require.NoError(t, err)
	require.True(t, applied)
	require.Empty(t, pruned, "the winning lens still asserts the key")

	entry, ok, err = s.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, entry.Deleted)

	// Now the winning lens retracts too — the last source drops and the row
	// finally tombstones.
	pruned, applied, err = s.ApplyKeySet(testLensAssigned, 10, nil)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, []string{key}, pruned)
}

func applyUpsertFrameHWGuardDropsResurrection(t *testing.T, f Factory) {
	s := f.New(t)

	key := "manifest.task.ffffffffffffffffffff"
	_, err := s.ApplyUpsert(key, testLensQueued, 5, []byte(`{"a":1}`))
	require.NoError(t, err)

	pruned, _, err := s.ApplyKeySet(testLensQueued, 10, nil)
	require.NoError(t, err)
	require.Equal(t, []string{key}, pruned)

	// A Nak'd-then-redelivered stale upsert (revision 7, between the original
	// assertion and the frame that retracted it) arrives after the frame. The
	// key is no longer attributed to this lens, so there is no tombstone to
	// lose against — without the frameHW guard this would silently resurrect
	// a key the frame already retracted by omission.
	applied, err := s.ApplyUpsert(key, testLensQueued, 7, []byte(`{"a":1}`))
	require.NoError(t, err)
	require.False(t, applied, "a stale redelivery behind the frame high-water mark must not resurrect a retracted key")

	entry, ok, err := s.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, entry.Deleted, "the retraction must hold")
}

func applyKeySetTwoLensRefcountSurvives(t *testing.T, f Factory) {
	s := f.New(t)

	key := "manifest.task.gggggggggggggggggggg"
	_, err := s.ApplyUpsert(key, testLensQueued, 1, []byte(`{"a":1}`))
	require.NoError(t, err)
	_, err = s.ApplyUpsert(key, testLensAssigned, 1, []byte(`{"a":1}`))
	require.NoError(t, err)

	// The queued lens retracts (the task was claimed); the assigned lens
	// still asserts it, so the key must survive under the refcount.
	pruned, applied, err := s.ApplyKeySet(testLensQueued, 5, nil)
	require.NoError(t, err)
	require.True(t, applied)
	require.Empty(t, pruned, "the assigned lens still asserts the key")

	entry, ok, err := s.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, entry.Deleted)

	// Now the assigned lens retracts too — the last source drops, and the
	// key finally tombstones.
	pruned, applied, err = s.ApplyKeySet(testLensAssigned, 5, nil)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, []string{key}, pruned)
}

func pruneDeadLensTombstonesLastLens(t *testing.T, f Factory) {
	s := f.New(t)

	key := "manifest.task.hhhhhhhhhhhhhhhhhhhh"
	_, err := s.ApplyUpsert(key, testLensQueued, 1, []byte(`{"a":1}`))
	require.NoError(t, err)

	pruned, err := s.PruneDeadLensAttributions([]string{testLensAssigned})
	require.NoError(t, err)
	require.Equal(t, []string{key}, pruned, "testLensQueued is not in the live set, so its only key must tombstone")

	entry, ok, err := s.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, entry.Deleted)
	require.Empty(t, entry.Data, "a dead-lens tombstone must not retain the last-known body")
}

func pruneDeadLensKeepsSurvivingLens(t *testing.T, f Factory) {
	s := f.New(t)

	key := "manifest.task.iiiiiiiiiiiiiiiiiiii"
	_, err := s.ApplyUpsert(key, testLensQueued, 1, []byte(`{"a":1}`))
	require.NoError(t, err)
	_, err = s.ApplyUpsert(key, testLensAssigned, 1, []byte(`{"a":1}`))
	require.NoError(t, err)

	pruned, err := s.PruneDeadLensAttributions([]string{testLensAssigned})
	require.NoError(t, err)
	require.Empty(t, pruned, "testLensAssigned is live, so the key must survive")

	entry, ok, err := s.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, entry.Deleted)
}

// applyUpsertUnattributedPreservesSources proves an upsert with lens=""
// (no attribution to report — e.g. a pre-R2 wire producer) never clobbers a
// key's existing attribution from other lenses: it wins the body LWW race
// exactly as before this design, but must not silently drop the refcount an
// already-attributed key depends on.
func applyUpsertUnattributedPreservesSources(t *testing.T, f Factory) {
	s := f.New(t)

	key := "manifest.task.jjjjjjjjjjjjjjjjjjjj"
	_, err := s.ApplyUpsert(key, testLensQueued, 1, []byte(`{"a":1}`))
	require.NoError(t, err)

	applied, err := s.ApplyUpsert(key, "", 2, []byte(`{"a":2}`))
	require.NoError(t, err)
	require.True(t, applied, "an unattributed upsert still wins the body LWW race on a newer revision")

	entry, ok, err := s.Get(key)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(2), entry.Revision)
	require.JSONEq(t, `{"a":2}`, string(entry.Data))
	require.Equal(t, uint64(1), entry.Sources[testLensQueued], "the lens's prior attribution must survive an unattributed write over it")

	// Proves the attribution is still live, not merely present: the lens's
	// own frame can still retract the key afterward.
	pruned, applied, err := s.ApplyKeySet(testLensQueued, 5, nil)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, []string{key}, pruned)
}
