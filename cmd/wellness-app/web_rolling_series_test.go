package main

import (
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// TestStudioGridWarning_RollingRunNeverRunsOut pins the studio card's
// "runs out in N days" guard against the rolling series: a run whose horizon
// is live (seriesRolling on any upcoming row at the studio) mints its next
// class as each one starts, so the studio's schedule cannot run out and the
// warning is withheld — while the same rows without the flag still warn
// inside GRID_HORIZON_DAYS, and an empty studio still reads as empty.
func TestStudioGridWarning_RollingRunNeverRunsOut(t *testing.T) {
	vm := webHelperVM(t, "upcomingSessionsAt", "studioGridWarning")
	if _, err := vm.RunString(`const GRID_HORIZON_DAYS = 7;`); err != nil {
		t.Fatalf("goja eval of the GRID_HORIZON_DAYS stub: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("studioGridWarning"))
	if !ok {
		t.Fatal("studioGridWarning is not a function after evaluating its declaration")
	}
	studio := map[string]any{"studioKey": "vtx.studio.BBWELLRLLNGSTUDHJKMN", "name": "Flow Room"}
	call := func(t *testing.T, sessions []map[string]any) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(studio), vm.ToValue(sessions))
		if err != nil {
			t.Fatalf("studioGridWarning threw: %v", err)
		}
		return res.String()
	}
	// Two days ahead — inside the horizon the warning measures.
	soon := "2026-09-18T09:00:00Z"
	if _, err := vm.RunString(`Date.now = () => Date.parse("2026-09-16T09:00:00Z");`); err != nil {
		t.Fatalf("pinning Date.now: %v", err)
	}
	row := func(rolling bool) map[string]any {
		m := map[string]any{"studioKey": "vtx.studio.BBWELLRLLNGSTUDHJKMN", "startsAt": soon, "endsAt": soon}
		if rolling {
			m["seriesRolling"] = true
		}
		return m
	}

	if got := call(t, []map[string]any{row(false)}); !strings.Contains(got, "Schedule runs out in") {
		t.Errorf("a non-rolling class two days out must warn the schedule runs out; got %q", got)
	}
	if got := call(t, []map[string]any{row(true)}); got != "" {
		t.Errorf("a rolling run on the books must withhold the warning; got %q", got)
	}
	if got := call(t, []map[string]any{row(false), row(true)}); got != "" {
		t.Errorf("one rolling run among the studio's classes withholds the warning; got %q", got)
	}
	if got := call(t, nil); !strings.Contains(got, "Schedule is empty") {
		t.Errorf("an empty studio still reads as empty; got %q", got)
	}
}
