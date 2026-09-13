package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// timeOffConflictDecl lifts the shipped timeOffConflict declaration out of the
// embedded app.js. It is the one predicate deciding whether a booked
// appointment falls inside a provider's time off — self-contained by
// construction (no reference to state/DOM/other app functions), so extracting
// and running the REAL source is what makes the assertions below a statement
// about what ships rather than about a copy in this file. Mirrors
// resetLoginOfferableDecl / reset_login_offer_test.go.
var timeOffConflictDecl = regexp.MustCompile(`(?s)\nfunction timeOffConflict\(a, ranges\) \{\n.*?\n\}\n`)

func timeOffConflictVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := timeOffConflictDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function timeOffConflict(a, ranges) {…}` declaration found — the extraction regex no longer matches this file")
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped timeOffConflict: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("timeOffConflict"))
	if !ok {
		t.Fatal("timeOffConflict is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestTimeOffConflict pins the half-open overlap rule the time-off op's own
// time_off_overlap conjunct enforces (packages/clinic-domain/ddls.go): a
// range's from must be strictly before the appointment's endsAt AND the
// appointment's startsAt strictly before the range's to. An appointment
// ending exactly at a range's from, or starting exactly at its to, is
// back-to-back and does NOT conflict. Only a non-terminal appointment can
// conflict — the auto no-show sweep's own posture (it no-ops rather than
// mark a visit inside a provider's time off) only makes sense because a
// missed visit there was never actually bookable against a live provider.
func TestTimeOffConflict(t *testing.T) {
	appt := func(startsAt, endsAt, status string) map[string]interface{} {
		return map[string]interface{}{"startsAt": startsAt, "endsAt": endsAt, "status": status}
	}
	rng := func(from, to string) map[string]interface{} {
		return map[string]interface{}{"from": from, "to": to}
	}

	run := func(t *testing.T, a map[string]interface{}, ranges interface{}) (goja.Value, error) {
		t.Helper()
		vm, fn := timeOffConflictVM(t)
		var rangesArg goja.Value
		if ranges == nil {
			rangesArg = goja.Undefined()
		} else {
			rangesArg = vm.ToValue(ranges)
		}
		var aArg goja.Value
		if a == nil {
			aArg = goja.Undefined()
		} else {
			aArg = vm.ToValue(a)
		}
		return fn(goja.Undefined(), aArg, rangesArg)
	}

	// wantRange checks the result names the range by its "from"; wantNull checks null.
	wantFrom := func(t *testing.T, res goja.Value, err error, from string) {
		t.Helper()
		if err != nil {
			t.Fatalf("timeOffConflict threw: %v", err)
		}
		if goja.IsNull(res) || goja.IsUndefined(res) {
			t.Fatalf("timeOffConflict = null, want range from=%q", from)
		}
		obj := res.ToObject(nil)
		got := obj.Get("from").String()
		if got != from {
			t.Errorf("timeOffConflict returned range from=%q, want %q", got, from)
		}
	}
	wantNull := func(t *testing.T, res goja.Value, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("timeOffConflict threw: %v", err)
		}
		if !goja.IsNull(res) && !goja.IsUndefined(res) {
			t.Errorf("timeOffConflict = %v, want null", res)
		}
	}

	t.Run("appointment fully inside a range", func(t *testing.T) {
		a := appt("2026-06-02T00:00:00Z", "2026-06-02T01:00:00Z", "scheduled")
		ranges := []interface{}{rng("2026-06-01T00:00:00Z", "2026-06-05T00:00:00Z")}
		res, err := run(t, a, ranges)
		wantFrom(t, res, err, "2026-06-01T00:00:00Z")
	})

	t.Run("overlap at the start of the range", func(t *testing.T) {
		a := appt("2026-06-01T23:00:00Z", "2026-06-02T01:00:00Z", "scheduled")
		ranges := []interface{}{rng("2026-06-02T00:00:00Z", "2026-06-05T00:00:00Z")}
		res, err := run(t, a, ranges)
		wantFrom(t, res, err, "2026-06-02T00:00:00Z")
	})

	t.Run("overlap at the end of the range", func(t *testing.T) {
		a := appt("2026-06-04T23:00:00Z", "2026-06-05T01:00:00Z", "scheduled")
		ranges := []interface{}{rng("2026-06-01T00:00:00Z", "2026-06-05T00:00:00Z")}
		res, err := run(t, a, ranges)
		wantFrom(t, res, err, "2026-06-01T00:00:00Z")
	})

	t.Run("range fully inside the appointment", func(t *testing.T) {
		a := appt("2026-06-01T00:00:00Z", "2026-06-10T00:00:00Z", "scheduled")
		ranges := []interface{}{rng("2026-06-03T00:00:00Z", "2026-06-04T00:00:00Z")}
		res, err := run(t, a, ranges)
		wantFrom(t, res, err, "2026-06-03T00:00:00Z")
	})

	t.Run("back-to-back: appointment ends exactly at the range's from", func(t *testing.T) {
		a := appt("2026-06-01T00:00:00Z", "2026-06-02T00:00:00Z", "scheduled")
		ranges := []interface{}{rng("2026-06-02T00:00:00Z", "2026-06-05T00:00:00Z")}
		res, err := run(t, a, ranges)
		wantNull(t, res, err)
	})

	t.Run("back-to-back: appointment starts exactly at the range's to", func(t *testing.T) {
		a := appt("2026-06-05T00:00:00Z", "2026-06-05T01:00:00Z", "scheduled")
		ranges := []interface{}{rng("2026-06-01T00:00:00Z", "2026-06-05T00:00:00Z")}
		res, err := run(t, a, ranges)
		wantNull(t, res, err)
	})

	t.Run("first overlapping range wins", func(t *testing.T) {
		a := appt("2026-06-02T00:00:00Z", "2026-06-02T01:00:00Z", "scheduled")
		ranges := []interface{}{
			rng("2026-06-01T00:00:00Z", "2026-06-03T00:00:00Z"),
			rng("2026-06-01T12:00:00Z", "2026-06-04T00:00:00Z"),
		}
		res, err := run(t, a, ranges)
		wantFrom(t, res, err, "2026-06-01T00:00:00Z")
	})

	overlapping := []interface{}{rng("2026-06-01T00:00:00Z", "2026-06-05T00:00:00Z")}
	for _, status := range []string{"completed", "cancelled", "noShow"} {
		status := status
		t.Run("terminal status never conflicts: "+status, func(t *testing.T) {
			a := appt("2026-06-02T00:00:00Z", "2026-06-02T01:00:00Z", status)
			res, err := run(t, a, overlapping)
			wantNull(t, res, err)
		})
	}
	for _, status := range []string{"scheduled", "confirmed", "checkedIn"} {
		status := status
		t.Run("active status conflicts: "+status, func(t *testing.T) {
			a := appt("2026-06-02T00:00:00Z", "2026-06-02T01:00:00Z", status)
			res, err := run(t, a, overlapping)
			wantFrom(t, res, err, "2026-06-01T00:00:00Z")
		})
	}

	t.Run("ranges undefined", func(t *testing.T) {
		a := appt("2026-06-02T00:00:00Z", "2026-06-02T01:00:00Z", "scheduled")
		res, err := run(t, a, nil)
		wantNull(t, res, err)
	})

	t.Run("ranges null", func(t *testing.T) {
		vm, fn := timeOffConflictVM(t)
		a := appt("2026-06-02T00:00:00Z", "2026-06-02T01:00:00Z", "scheduled")
		res, err := fn(goja.Undefined(), vm.ToValue(a), goja.Null())
		wantNull(t, res, err)
	})

	t.Run("ranges empty array", func(t *testing.T) {
		a := appt("2026-06-02T00:00:00Z", "2026-06-02T01:00:00Z", "scheduled")
		res, err := run(t, a, []interface{}{})
		wantNull(t, res, err)
	})

	t.Run("malformed range is skipped, a later valid one still matches", func(t *testing.T) {
		a := appt("2026-06-02T00:00:00Z", "2026-06-02T01:00:00Z", "scheduled")
		ranges := []interface{}{
			"not an object",
			map[string]interface{}{"from": "2026-06-01T00:00:00Z"}, // missing "to"
			rng("2026-06-01T00:00:00Z", "2026-06-05T00:00:00Z"),
		}
		res, err := run(t, a, ranges)
		wantFrom(t, res, err, "2026-06-01T00:00:00Z")
	})

	t.Run("unparsable startsAt", func(t *testing.T) {
		a := appt("not-a-date", "2026-06-02T01:00:00Z", "scheduled")
		res, err := run(t, a, overlapping)
		wantNull(t, res, err)
	})
}
