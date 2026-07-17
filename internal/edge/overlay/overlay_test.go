package overlay

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/edge/store"
)

func openTestOverlay(t *testing.T) (*Overlay, store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "edge.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return New(st), st
}

const testKey = "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T"

func TestRead_AbsentEverywhereReturnsNotOK(t *testing.T) {
	o, _ := openTestOverlay(t)

	_, ok, err := o.Read(testKey)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRead_ConfirmedOnly(t *testing.T) {
	o, st := openTestOverlay(t)
	_, err := st.ApplyUpsert(testKey, 3, []byte(`{"rent":100}`))
	require.NoError(t, err)

	v, ok, err := o.Read(testKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, v.Pending)
	require.JSONEq(t, `{"rent":100}`, string(v.Data))
}

func TestApply_ShowsPendingImmediately(t *testing.T) {
	o, st := openTestOverlay(t)
	_, err := st.ApplyUpsert(testKey, 3, []byte(`{"rent":100}`))
	require.NoError(t, err)

	require.NoError(t, o.Apply(testKey, "req1", []byte(`{"rent":150}`), false))

	v, ok, err := o.Read(testKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, v.Pending, "the overlay must be visible before any confirmation")
	require.JSONEq(t, `{"rent":150}`, string(v.Data))
}

func TestApply_OnNeverConfirmedKey(t *testing.T) {
	o, _ := openTestOverlay(t)

	require.NoError(t, o.Apply(testKey, "req1", []byte(`{"rent":150}`), false))

	v, ok, err := o.Read(testKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, v.Pending)
	require.JSONEq(t, `{"rent":150}`, string(v.Data))
}

func TestRead_RetiresOverlayOnceConfirmedAdvancesPastBaseline(t *testing.T) {
	o, st := openTestOverlay(t)
	_, err := st.ApplyUpsert(testKey, 3, []byte(`{"rent":100}`))
	require.NoError(t, err)
	require.NoError(t, o.Apply(testKey, "req1", []byte(`{"rent":150}`), false))

	// The intent's own commit lands via the ordinary SYNC stream (or an
	// unrelated concurrent write does) — either way, once the confirmed
	// revision moves past the overlay's baseline, R3 says the overlay is
	// cleared by the authoritative value, never by local success alone.
	_, err = st.ApplyUpsert(testKey, 4, []byte(`{"rent":175}`))
	require.NoError(t, err)

	v, ok, err := o.Read(testKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, v.Pending, "a fresher confirmed value must supersede the stale overlay")
	require.JSONEq(t, `{"rent":175}`, string(v.Data))

	// The lazy retire in Read must have pruned the pending entry.
	keys, err := o.PendingKeys()
	require.NoError(t, err)
	require.Empty(t, keys)
}

func TestRead_KeepsOverlayWhileConfirmedStillAtBaseline(t *testing.T) {
	o, st := openTestOverlay(t)
	_, err := st.ApplyUpsert(testKey, 3, []byte(`{"rent":100}`))
	require.NoError(t, err)
	require.NoError(t, o.Apply(testKey, "req1", []byte(`{"rent":150}`), false))

	// An unrelated redelivery at the SAME revision (JetStream at-least-once)
	// must not falsely retire the overlay.
	_, err = st.ApplyUpsert(testKey, 3, []byte(`{"rent":100}`))
	require.NoError(t, err)

	v, ok, err := o.Read(testKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, v.Pending)
}

func TestDiscard_DropsOverlayAndFallsBackToConfirmed(t *testing.T) {
	o, st := openTestOverlay(t)
	_, err := st.ApplyUpsert(testKey, 3, []byte(`{"rent":100}`))
	require.NoError(t, err)
	require.NoError(t, o.Apply(testKey, "req1", []byte(`{"rent":150}`), false))

	require.NoError(t, o.Discard(testKey))

	v, ok, err := o.Read(testKey)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, v.Pending)
	require.JSONEq(t, `{"rent":100}`, string(v.Data))
}

func TestDiscard_OnNeverConfirmedKeyLeavesNothing(t *testing.T) {
	o, _ := openTestOverlay(t)
	require.NoError(t, o.Apply(testKey, "req1", []byte(`{"rent":150}`), false))

	require.NoError(t, o.Discard(testKey))

	_, ok, err := o.Read(testKey)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestPendingKeys_ListsActiveOverlaysOnly(t *testing.T) {
	o, _ := openTestOverlay(t)
	require.NoError(t, o.Apply(testKey, "req1", []byte(`{"a":1}`), false))

	keys, err := o.PendingKeys()
	require.NoError(t, err)
	require.Equal(t, []string{testKey}, keys)

	require.NoError(t, o.Discard(testKey))
	keys, err = o.PendingKeys()
	require.NoError(t, err)
	require.Empty(t, keys)
}

func TestLinks_OutDirection_ConfirmedAndPending(t *testing.T) {
	o, st := openTestOverlay(t)
	hub := "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T"
	confirmedLink := "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.hasBooking.booking.Bk2Pn6mQrtwzKbcXvP3T"
	pendingLink := "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.hasBooking.booking.Ck2Pn6mQrtwzKbcXvP3T"
	otherRelation := "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.hasTenant.identity.Dk2Pn6mQrtwzKbcXvP3T"
	otherHub := "lnk.lease.Zk2Pn6mQrtwzKbcXvP3T.hasBooking.booking.Bk2Pn6mQrtwzKbcXvP3T"

	_, err := st.ApplyUpsert(confirmedLink, 1, []byte(`{}`))
	require.NoError(t, err)
	_, err = st.ApplyUpsert(otherRelation, 1, []byte(`{}`))
	require.NoError(t, err)
	_, err = st.ApplyUpsert(otherHub, 1, []byte(`{}`))
	require.NoError(t, err)
	require.NoError(t, o.Apply(pendingLink, "req1", []byte(`{}`), false))

	links, err := o.Links(hub, "hasBooking", "out")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{confirmedLink, pendingLink}, links)
}

func TestLinks_OutDirection_ExcludesPendingDeletedLink(t *testing.T) {
	o, st := openTestOverlay(t)
	hub := "vtx.lease.Lk2Pn6mQrtwzKbcXvP3T"
	link := "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.hasBooking.booking.Bk2Pn6mQrtwzKbcXvP3T"

	_, err := st.ApplyUpsert(link, 1, []byte(`{}`))
	require.NoError(t, err)
	// A local intent tombstones the link before the cloud confirms it.
	require.NoError(t, o.Apply(link, "req1", nil, true))

	links, err := o.Links(hub, "hasBooking", "out")
	require.NoError(t, err)
	require.Empty(t, links, "a pending delete overlay must hide the link from UI discovery")
}

func TestLinks_InDirection(t *testing.T) {
	o, st := openTestOverlay(t)
	hub := "vtx.booking.Bk2Pn6mQrtwzKbcXvP3T"
	link := "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.hasBooking.booking.Bk2Pn6mQrtwzKbcXvP3T"
	unrelated := "lnk.lease.Lk2Pn6mQrtwzKbcXvP3T.hasBooking.booking.Ck2Pn6mQrtwzKbcXvP3T"

	_, err := st.ApplyUpsert(link, 1, []byte(`{}`))
	require.NoError(t, err)
	_, err = st.ApplyUpsert(unrelated, 1, []byte(`{}`))
	require.NoError(t, err)

	links, err := o.Links(hub, "hasBooking", "in")
	require.NoError(t, err)
	require.Equal(t, []string{link}, links)
}

func TestLinks_RejectsBadInput(t *testing.T) {
	o, _ := openTestOverlay(t)

	_, err := o.Links("not-a-vertex-key", "hasBooking", "out")
	require.Error(t, err)

	_, err = o.Links("vtx.lease.Lk2Pn6mQrtwzKbcXvP3T", "hasBooking", "sideways")
	require.Error(t, err)
}
