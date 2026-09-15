package main

import (
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// TestAttendanceActions_NoShowLabelPerFeeState pins the roster's No-show
// button against every shape SetStudioProfile/CreateStudio's noShowFeeCents
// policy can take (resolveNoShowFeeCents/noShowLabel, app.js) — the same fee
// SetBookingAttendance itself resolves server-side
// (studio_no_show_fee, packages/wellness-domain/ddls.go) when the payload
// names none. The waiver button ("No-show, waive fee") is offered whenever
// the amount is positive OR unknown (a studio row that failed to load), and
// withheld only when the resolved fee is already 0 — waiving a free no-show
// has nothing to waive.
func TestAttendanceActions_NoShowLabelPerFeeState(t *testing.T) {
	vm := webHelperVM(t, "esc", "money", "resolveNoShowFeeCents", "noShowLabel", "attendanceActions")
	fn, ok := goja.AssertFunction(vm.Get("attendanceActions"))
	if !ok {
		t.Fatal("attendanceActions is not a function after evaluating its declaration")
	}
	call := func(t *testing.T, studio any) string {
		t.Helper()
		b := map[string]any{"bookingKey": "vtx.booking.X", "status": "booked"}
		var res goja.Value
		var err error
		if studio == nil {
			res, err = fn(goja.Undefined(), vm.ToValue(b), goja.Undefined())
		} else {
			res, err = fn(goja.Undefined(), vm.ToValue(b), vm.ToValue(studio))
		}
		if err != nil {
			t.Fatalf("attendanceActions threw: %v", err)
		}
		return res.String()
	}

	for _, tc := range []struct {
		name       string
		studio     any
		wantLabel  string
		wantWaiver bool
	}{
		{"studio policy $25 (2500 cents)", map[string]any{"studioKey": "vtx.studio.A", "noShowFeeCents": 2500.0}, "No-show ($25.00)", true},
		{"studio policy $10 (1000 cents)", map[string]any{"studioKey": "vtx.studio.A", "noShowFeeCents": 1000.0}, "No-show ($10.00)", true},
		{"studio fee-free policy (0)", map[string]any{"studioKey": "vtx.studio.A", "noShowFeeCents": 0.0}, "No-show (no fee)", false},
		{"studio with no policy recorded — the $25 default", map[string]any{"studioKey": "vtx.studio.A"}, "No-show ($25.00)", true},
		{"studio row not loaded", nil, "No-show", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := call(t, tc.studio)
			if !strings.Contains(out, ">"+tc.wantLabel+"<") {
				t.Errorf("attendanceActions label = %q, want %q in:\n%s", tc.wantLabel, tc.wantLabel, out)
			}
			gotWaiver := strings.Contains(out, "data-waive=")
			if gotWaiver != tc.wantWaiver {
				t.Errorf("attendanceActions waiver button present = %v, want %v:\n%s", gotWaiver, tc.wantWaiver, out)
			}
		})
	}
}

// TestAttendancePayload_WaiverSendsExplicitZero pins markAttendance's own
// payload builder (attendancePayload, app.js): the waiver button's click
// sends an explicit noShowFeeCents: 0, while the plain No-show mark sends
// none at all — the field must be ABSENT, not merely falsy, so
// SetBookingAttendance takes its own studio_no_show_fee resolution path
// (ddls.go's optional_number(p, "noShowFeeCents") reads absence as "resolve
// the studio's policy", not as a caller-supplied zero).
func TestAttendancePayload_WaiverSendsExplicitZero(t *testing.T) {
	vm := webHelperVM(t, "idOf", "attendancePayload")
	fn, ok := goja.AssertFunction(vm.Get("attendancePayload"))
	if !ok {
		t.Fatal("attendancePayload is not a function after evaluating its declaration")
	}
	call := func(t *testing.T, waive bool) map[string]any {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue("vtx.booking.X"), vm.ToValue("vtx.session.Y"), vm.ToValue("noShow"), goja.Null(), vm.ToValue(waive))
		if err != nil {
			t.Fatalf("attendancePayload threw: %v", err)
		}
		return res.Export().(map[string]any)
	}

	waived := call(t, true)
	payload := waived["payload"].(map[string]any)
	fee, present := payload["noShowFeeCents"]
	if !present {
		t.Fatalf("waived payload carries no noShowFeeCents at all, want an explicit 0: %+v", payload)
	}
	if n, ok := fee.(int64); !ok || n != 0 {
		if f, ok := fee.(float64); !ok || f != 0 {
			t.Errorf("waived payload noShowFeeCents = %v, want 0", fee)
		}
	}

	plain := call(t, false)
	plainPayload := plain["payload"].(map[string]any)
	if _, present := plainPayload["noShowFeeCents"]; present {
		t.Errorf("plain no-show payload carries noShowFeeCents = %v, want the key entirely absent", plainPayload["noShowFeeCents"])
	}
}

// TestNoShowFeeCardLine_PerState pins the Studios admin card's own fee line
// (noShowFeeCardLine, app.js) — the same three policy shapes
// TestAttendanceActions_NoShowLabelPerFeeState covers for the roster button,
// stated in the card's own longer sentence rather than the roster's compact
// parenthetical.
func TestNoShowFeeCardLine_PerState(t *testing.T) {
	vm := webHelperVM(t, "money", "noShowFeeCardLine")
	fn, ok := goja.AssertFunction(vm.Get("noShowFeeCardLine"))
	if !ok {
		t.Fatal("noShowFeeCardLine is not a function after evaluating its declaration")
	}
	call := func(t *testing.T, s map[string]any) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(s))
		if err != nil {
			t.Fatalf("noShowFeeCardLine threw: %v", err)
		}
		return res.String()
	}

	for _, tc := range []struct {
		name string
		s    map[string]any
		want string
	}{
		{"policy $10 (1000 cents)", map[string]any{"noShowFeeCents": 1000.0}, "No-show fee: $10.00"},
		{"fee-free policy (0)", map[string]any{"noShowFeeCents": 0.0}, "No-show fee: $0.00"},
		{"no policy recorded", map[string]any{}, "No-show fee: none (bills $25)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(t, tc.s); got != tc.want {
				t.Errorf("noShowFeeCardLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDollarsToCents pins the shared dollars-in-the-UI-to-cents-on-the-wire
// conversion the studio create form and the fee-edit form both submit
// through: a fractional dollar amount rounds to the whole cent, and a blank
// or malformed entry converts to `undefined` (stays OMITTED from the
// payload) rather than a coerced 0 — the "no policy" vs "explicit $0"
// distinction SetStudioProfile/CreateStudio's own noShowFeeCents both
// preserve (packages/wellness-domain/ddls.go's optional_fee_cents).
func TestDollarsToCents(t *testing.T) {
	vm := webHelperVM(t, "dollarsToCents")
	fn, ok := goja.AssertFunction(vm.Get("dollarsToCents"))
	if !ok {
		t.Fatal("dollarsToCents is not a function after evaluating its declaration")
	}
	call := func(t *testing.T, raw string) goja.Value {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(raw))
		if err != nil {
			t.Fatalf("dollarsToCents threw: %v", err)
		}
		return res
	}

	for _, tc := range []struct {
		name    string
		raw     string
		wantDef bool
		want    int64
	}{
		{"a fractional dollar amount rounds to the whole cent", "12.5", true, 1250},
		{"a whole dollar amount", "25", true, 2500},
		{"zero is a real, defined value", "0", true, 0},
		{"blank stays omitted", "", false, 0},
		{"whitespace-only stays omitted", "   ", false, 0},
		{"a negative amount stays omitted", "-5", false, 0},
		{"a non-numeric amount stays omitted", "abc", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := call(t, tc.raw)
			if goja.IsUndefined(got) {
				if tc.wantDef {
					t.Fatalf("dollarsToCents(%q) = undefined, want %d", tc.raw, tc.want)
				}
				return
			}
			if !tc.wantDef {
				t.Fatalf("dollarsToCents(%q) = %v, want undefined", tc.raw, got.Export())
			}
			if got.ToInteger() != tc.want {
				t.Errorf("dollarsToCents(%q) = %v, want %d", tc.raw, got.Export(), tc.want)
			}
		})
	}
}
