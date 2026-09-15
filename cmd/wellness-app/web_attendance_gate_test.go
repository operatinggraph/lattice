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
	vm := webHelperVM(t, "esc", "shortKey", "idOf", "nameForIdentity", "arrearsBadgeText", "reminderBadge", "attendanceActions", "seatCancelAction", "rosterCard")
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
