package main

import (
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"
)

// TestDeskCanSeat_MirrorsTheDeskLegsEndsAtGuard pins the roster's book-a-member
// gate against CreateBooking's desk-leg past-class guard
// (prepare_booking_common, packages/wellness-domain/ddls.go): a staff or
// operator submission is admitted until the class ENDS and refused
// SessionInPast from endsAt on. So the form stays up before the start and
// while the class is under way — the walk-in at the door — and comes down at
// endsAt exactly, with a row this page cannot place in time reading as not
// seatable.
func TestDeskCanSeat_MirrorsTheDeskLegsEndsAtGuard(t *testing.T) {
	vm := webHelperVM(t, "deskCanSeat")
	fn, ok := goja.AssertFunction(vm.Get("deskCanSeat"))
	if !ok {
		t.Fatal("deskCanSeat is not a function after evaluating its declaration")
	}
	startsAt := time.Date(2026, 7, 8, 9, 0, 0, 0, time.UTC)
	endsAt := startsAt.Add(30 * time.Minute)
	se := map[string]any{"sessionKey": "vtx.session.x", "startsAt": startsAt.Format(time.RFC3339), "endsAt": endsAt.Format(time.RFC3339)}

	call := func(t *testing.T, se any, now time.Time) bool {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(se), vm.ToValue(now.UnixMilli()))
		if err != nil {
			t.Fatalf("deskCanSeat threw: %v", err)
		}
		return res.ToBoolean()
	}

	require.True(t, call(t, se, startsAt.Add(-time.Hour)), "before the start the desk seats as it always did")
	require.True(t, call(t, se, startsAt), "at the start the class is under way and the walk-in is still seatable")
	require.True(t, call(t, se, startsAt.Add(10*time.Minute)), "ten minutes in — the row itself")
	require.True(t, call(t, se, endsAt.Add(-time.Second)), "a second before the end")
	require.False(t, call(t, se, endsAt), "at endsAt the class has ended — the op refuses from here (the script's `submitted < ends_at`)")
	require.False(t, call(t, se, endsAt.Add(time.Hour)), "an hour after the end")
	require.False(t, call(t, map[string]any{"sessionKey": "vtx.session.y", "startsAt": se["startsAt"]}, startsAt.Add(-time.Hour)), "no endsAt — cannot be placed in time, not seatable")
	require.False(t, call(t, nil, startsAt.Add(-time.Hour)), "no session at all")
	require.False(t, call(t, map[string]any{"endsAt": "not-a-time"}, startsAt), "an unparseable end never admits")
}

// TestReassignCapacityFloor_IsTheSeatedCount pins the reassign form's
// Capacity lower bound — the courtesy for ReassignSession's
// CapacityBelowSeated refusal (packages/wellness-domain/ddls.go): the row's
// seated count (se.bookedCount), never below 1, and 1 when the row carries no
// count at all.
func TestReassignCapacityFloor_IsTheSeatedCount(t *testing.T) {
	vm := webHelperVM(t, "reassignCapacityFloor")
	fn, ok := goja.AssertFunction(vm.Get("reassignCapacityFloor"))
	if !ok {
		t.Fatal("reassignCapacityFloor is not a function after evaluating its declaration")
	}
	call := func(t *testing.T, se any) int64 {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(se))
		if err != nil {
			t.Fatalf("reassignCapacityFloor threw: %v", err)
		}
		return res.ToInteger()
	}

	require.Equal(t, int64(2), call(t, map[string]any{"capacity": 5, "bookedCount": 2}), "two seated — the floor is 2")
	require.Equal(t, int64(5), call(t, map[string]any{"capacity": 5, "bookedCount": 5}), "a full class cannot shrink at all")
	require.Equal(t, int64(1), call(t, map[string]any{"capacity": 5, "bookedCount": 0}), "nobody seated — the op's own minimum of 1")
	require.Equal(t, int64(1), call(t, map[string]any{"capacity": 5}), "no count on the row — the op's own minimum")
	require.Equal(t, int64(1), call(t, nil), "no row at all")
}
