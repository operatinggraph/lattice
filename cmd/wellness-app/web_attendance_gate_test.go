package main

import (
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// TestRosterCard_WaitlistedBookingNeverOffersAttendance pins rosterCard
// against SetBookingAttendance's own InvalidState guard
// (packages/wellness-domain/ddls.go): a waitlisted booking never held a
// seat, so the op refuses recording attendance for one ("is waitlisted,
// never held a seat; nothing to record attendance for"). attendanceActions
// must not render for a waitlisted row even when the roster's own
// started+leader/staff gate (canMark) would otherwise offer it — mirroring
// the forfeited exclusion already beside it. The release action
// (seatCancelAction) is unaffected: releasing a waitlist slot is exactly
// what CancelBooking does for one.
func TestRosterCard_WaitlistedBookingNeverOffersAttendance(t *testing.T) {
	vm := webHelperVM(t, "esc", "shortKey", "idOf", "nameForIdentity", "arrearsBadgeText", "fmtTime", "fmtDay", "promotedBadge", "reminderBadge", "attendanceActions", "seatCancelAction", "rosterCard")
	if _, err := vm.RunString(`var rosterArrears = new Map(); var state = { identities: [] };`); err != nil {
		t.Fatalf("goja eval of the rosterArrears/state stub: %v", err)
	}
	// ATTENDANCE_MARKS is a top-level `const` object literal, not a function,
	// so webDeclRe's function-shaped extraction cannot lift it — mirrors
	// TestAttendanceMarks_CoversForfeited's own reasoning (web_status_test.go).
	const attendanceMarks = `const ATTENDANCE_MARKS = {attended:{badge:"posted",label:"attended"},noShow:{badge:"settled",label:"no-show"},forfeited:{badge:"settled",label:"forfeited"}};`
	if _, err := vm.RunString(attendanceMarks); err != nil {
		t.Fatalf("goja eval of the ATTENDANCE_MARKS stub: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("rosterCard"))
	if !ok {
		t.Fatal("rosterCard is not a function after evaluating its declaration")
	}

	call := func(t *testing.T, status string) string {
		t.Helper()
		b := map[string]any{"bookingKey": "vtx.booking.X", "bookerKey": "vtx.identity.X", "status": status}
		res, err := fn(goja.Undefined(), vm.ToValue(b), vm.ToValue(true), vm.ToValue(true))
		if err != nil {
			t.Fatalf("rosterCard threw: %v", err)
		}
		return res.String()
	}

	waitlisted := call(t, "waitlisted")
	if strings.Contains(waitlisted, "data-attend=") {
		t.Errorf("rosterCard(waitlisted) offers an attendance action, want none:\n%s", waitlisted)
	}
	if !strings.Contains(waitlisted, "data-cancel-seat=") {
		t.Errorf("rosterCard(waitlisted) drops the release action too, want it kept:\n%s", waitlisted)
	}

	booked := call(t, "booked")
	if !strings.Contains(booked, "data-attend=") {
		t.Errorf("rosterCard(booked) offers no attendance action, want one:\n%s", booked)
	}
}

// TestRosterCard_BadgesASeatFromTheWaitlist pins the desk's own seating badge:
// a row carrying promotedAt says so, a directly booked row does not — the
// front desk otherwise sees a Booked row with no trace of how the member got
// there.
func TestRosterCard_BadgesASeatFromTheWaitlist(t *testing.T) {
	vm := webHelperVM(t, "esc", "shortKey", "idOf", "nameForIdentity", "arrearsBadgeText", "fmtTime", "fmtDay", "promotedBadge", "reminderBadge", "attendanceActions", "seatCancelAction", "rosterCard")
	if _, err := vm.RunString(`var rosterArrears = new Map(); var state = { identities: [] }; const ATTENDANCE_MARKS = {};`); err != nil {
		t.Fatalf("goja eval of the rosterArrears/state stub: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("rosterCard"))
	if !ok {
		t.Fatal("rosterCard is not a function after evaluating its declaration")
	}
	call := func(t *testing.T, b map[string]any) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(b), vm.ToValue(true), vm.ToValue(true))
		if err != nil {
			t.Fatalf("rosterCard threw: %v", err)
		}
		return res.String()
	}
	promoted := call(t, map[string]any{"bookingKey": "vtx.booking.X", "bookerKey": "vtx.identity.X", "status": "booked", "promotedAt": "2026-07-08T07:57:00Z"})
	if !strings.Contains(promoted, "Seated from the waitlist") {
		t.Errorf("rosterCard(promoted) carries no seating badge, want one:\n%s", promoted)
	}
	direct := call(t, map[string]any{"bookingKey": "vtx.booking.Y", "bookerKey": "vtx.identity.Y", "status": "booked"})
	if strings.Contains(direct, "Seated from the waitlist") {
		t.Errorf("rosterCard(direct) carries a seating badge, want none:\n%s", direct)
	}
}
