package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// TestArrearsBadgeText_RendersOwedAndOverdue proves arrearsBadgeText's four
// shapes against the shipped source: nothing owed says nothing, a balance
// alone says only what is owed, and an overdue balance appends the
// day/days-overdue clause in the singular exactly at daysOverdue == 1 — the
// same singular/plural rule arrearsLine already applies to a due-date banner,
// now on the booking pickers and roster cards that flag a debtor before the
// desk books them.
func TestArrearsBadgeText_RendersOwedAndOverdue(t *testing.T) {
	vm := webHelperVM(t, "money", "arrearsBadgeText")
	fn, ok := goja.AssertFunction(vm.Get("arrearsBadgeText"))
	if !ok {
		t.Fatal("arrearsBadgeText is not a function after evaluating its declaration")
	}

	call := func(t *testing.T, row map[string]any) string {
		t.Helper()
		var arg goja.Value
		if row == nil {
			arg = goja.Undefined()
		} else {
			arg = vm.ToValue(row)
		}
		res, err := fn(goja.Undefined(), arg)
		if err != nil {
			t.Fatalf("arrearsBadgeText threw: %v", err)
		}
		return res.String()
	}

	for _, tc := range []struct {
		name string
		row  map[string]any
		want string
	}{
		{"no row at all", nil, ""},
		{"zero balance", map[string]any{"balanceCents": 0}, ""},
		{"negative balance", map[string]any{"balanceCents": -500}, ""},
		{"balance owed, not overdue", map[string]any{"balanceCents": 6000}, "owes $60.00"},
		{
			"balance owed, overdue, plural days",
			map[string]any{"balanceCents": 6000, "isOverdue": true, "daysOverdue": 3},
			"owes $60.00 · 3 days overdue",
		},
		{
			"balance owed, overdue, singular day",
			map[string]any{"balanceCents": 6000, "isOverdue": true, "daysOverdue": 1},
			"owes $60.00 · 1 day overdue",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(t, tc.row); got != tc.want {
				t.Errorf("arrearsBadgeText(%v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}

// TestBookingProjectionPollMs_OutlastsTheMeasuredLensLag pins
// BOOKING_PROJECTION_POLL_MS, lifted out of the shipped source the same way
// TestIsLateCancel_ForfeitedAndWaitlistedNeverForfeitAgain lifts
// LATE_CANCEL_WINDOW_MS (web_status_test.go): the wellnessBookings lens has
// been measured minting a fresh booking row at ~24s on this stack, so the
// schedule must sum to at least that with headroom, and each wait must never
// be shorter than the one before it — a poll loop that got FASTER over time
// would burn its budget hammering the lens in the early seconds and starve
// the tail end where the real lag lives.
func TestBookingProjectionPollMs_OutlastsTheMeasuredLensLag(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := regexp.MustCompile(`const BOOKING_PROJECTION_POLL_MS = [^;]+;`).FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no `const BOOKING_PROJECTION_POLL_MS = […];` declaration found — awaitProjectedBooking reads it")
	}

	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped BOOKING_PROJECTION_POLL_MS: %v", err)
	}
	raw, ok := vm.Get("BOOKING_PROJECTION_POLL_MS").Export().([]any)
	if !ok {
		t.Fatalf("BOOKING_PROJECTION_POLL_MS did not evaluate to an array: %v", vm.Get("BOOKING_PROJECTION_POLL_MS"))
	}
	if len(raw) == 0 {
		t.Fatal("BOOKING_PROJECTION_POLL_MS is empty — awaitProjectedBooking would never poll")
	}

	const measuredLensLagMs = 25000
	var sum int64
	var prev int64 = -1
	for i, v := range raw {
		n, ok := v.(int64)
		if !ok {
			// goja exports a numeric literal as int64 when it fits, float64
			// otherwise — the schedule is all whole milliseconds, but fall
			// back rather than fail on the export shape itself.
			if f, fok := v.(float64); fok {
				n = int64(f)
			} else {
				t.Fatalf("BOOKING_PROJECTION_POLL_MS[%d] is not numeric: %v (%T)", i, v, v)
			}
		}
		if n < prev {
			t.Errorf("BOOKING_PROJECTION_POLL_MS[%d] = %d is shorter than BOOKING_PROJECTION_POLL_MS[%d] = %d — the schedule must never get faster", i, n, i-1, prev)
		}
		prev = n
		sum += n
	}
	if sum < measuredLensLagMs {
		t.Errorf("BOOKING_PROJECTION_POLL_MS sums to %dms, want at least %dms (the measured ~24s wellnessBookings mint lag, with headroom)", sum, measuredLensLagMs)
	}
}
