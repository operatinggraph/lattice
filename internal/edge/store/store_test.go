package store

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "edge.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

const testVtxKey = "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T"

func TestApplyUpsert_AppliesNewerRevision(t *testing.T) {
	s := openTestStore(t)

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

func TestApplyUpsert_DropsStaleRevision(t *testing.T) {
	s := openTestStore(t)

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

func TestApplyUpsert_DropsDuplicateRevision(t *testing.T) {
	s := openTestStore(t)

	applied, err := s.ApplyUpsert(testVtxKey, 7, []byte(`{"a":1}`))
	require.NoError(t, err)
	require.True(t, applied)

	// revision == stored revision is treated as an at-least-once redelivery
	// (JetStream) of the same delta, not a newer write — LWW's "≥" gate
	// still lets it re-apply the same content, exercising the boundary
	// (revision < stored is the only drop condition).
	applied, err = s.ApplyUpsert(testVtxKey, 7, []byte(`{"a":1}`))
	require.NoError(t, err)
	require.True(t, applied, "a duplicate (equal) revision redelivery must be idempotent-applied, not error")
}

func TestApplyDelete_TombstonesOnDelete(t *testing.T) {
	s := openTestStore(t)

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

func TestApplyDelete_DropsStaleRevision(t *testing.T) {
	s := openTestStore(t)

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

func TestApplyUpsertAndDelete_RejectNonContract1Keys(t *testing.T) {
	s := openTestStore(t)

	_, err := s.ApplyUpsert("not-a-valid-key", 1, []byte(`{}`))
	require.Error(t, err)

	_, err = s.ApplyDelete("vtx.badtype", 1)
	require.Error(t, err)
}

func TestApplyUpsertAndDelete_AcceptManifestKeys(t *testing.T) {
	s := openTestStore(t)

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

func TestGet_AbsentKeyReturnsNotOK(t *testing.T) {
	s := openTestStore(t)

	entry, ok, err := s.Get(testVtxKey)
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, entry)
}

func TestCursor_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.db")

	s, err := Open(path)
	require.NoError(t, err)

	_, ok, err := s.Cursor()
	require.NoError(t, err)
	require.False(t, ok, "a fresh store has no cursor — the node must hydrate")

	require.NoError(t, s.SetCursor(42))
	seq, ok, err := s.Cursor()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(42), seq)
	require.NoError(t, s.Close())

	reopened, err := Open(path)
	require.NoError(t, err)
	defer func() { _ = reopened.Close() }()

	seq, ok, err = reopened.Cursor()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(42), seq, "the resume cursor must survive a process restart")
}

func TestLocalNamespace_NeverVisibleThroughGet(t *testing.T) {
	s := openTestStore(t)

	require.NoError(t, s.PutLocal("draft-note-1", []byte(`{"text":"private"}`)))

	data, ok, err := s.GetLocal("draft-note-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.JSONEq(t, `{"text":"private"}`, string(data))

	// A local-only name is never mirrored-key shaped, so it can never
	// collide with — or be surfaced through — the Contract #1 Get path.
	_, ok, err = s.Get("draft-note-1")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestGetLocal_AbsentNameReturnsNotOK(t *testing.T) {
	s := openTestStore(t)

	data, ok, err := s.GetLocal("nope")
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, data)
}

func TestPending_RoundTripsAndDeletes(t *testing.T) {
	s := openTestStore(t)

	_, ok, err := s.GetPending(testVtxKey)
	require.NoError(t, err)
	require.False(t, ok)

	entry := PendingEntry{Key: testVtxKey, RequestID: "req1", Data: []byte(`{"a":1}`), BaseRevision: 3}
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

func TestScanPrefix_ReturnsOnlyMatchingConfirmedEntries(t *testing.T) {
	s := openTestStore(t)

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

func TestIntentQueue_FIFOAndDelete(t *testing.T) {
	s := openTestStore(t)

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

func TestIntentQueue_PersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.db")
	s, err := Open(path)
	require.NoError(t, err)
	_, err = s.EnqueueIntent([]byte(`{"requestId":"one"}`))
	require.NoError(t, err)
	require.NoError(t, s.Close())

	reopened, err := Open(path)
	require.NoError(t, err)
	defer func() { _ = reopened.Close() }()

	list, err := reopened.ListIntents()
	require.NoError(t, err)
	require.Len(t, list, 1, "a queued intent must survive a process restart")
}
