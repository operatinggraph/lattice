package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// seriesFloorReasonDecl lifts the shipped seriesFloorReason declaration out of
// the embedded app.js — the pure predicate the Book calendar uses to refuse a
// day that falls before a recurring series' next due date. Self-contained by
// construction (no state/DOM), so the REAL source runs here; mirrors
// lifecycle_transitions_test.go's extraction.
var seriesFloorReasonDecl = regexp.MustCompile(`(?s)\nfunction seriesFloorReason\(dayStart, floor\) \{\n.*?\n\}\n`)

func seriesFloorReasonVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := seriesFloorReasonDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function seriesFloorReason(dayStart, floor) {…}` declaration found — the extraction regex no longer matches this file")
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped seriesFloorReason: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("seriesFloorReason"))
	if !ok {
		t.Fatal("seriesFloorReason is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestSeriesFloorReason_BlocksOnlyDaysEndingBeforeTheFloor pins the FE calendar
// guard that keeps the desk from picking a day that would not credit a
// recurring series' occurrence: the booking op only counts a visit whose
// startsAt is on or after the series' nextDueAt, so a day that ends before
// that instant is refused outright, named by its own due date.
func TestSeriesFloorReason_BlocksOnlyDaysEndingBeforeTheFloor(t *testing.T) {
	vm, fn := seriesFloorReasonVM(t)
	run := func(dayStart float64, floor string) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(dayStart), vm.ToValue(floor))
		if err != nil {
			t.Fatalf("seriesFloorReason(%v, %q) threw: %v", dayStart, floor, err)
		}
		return res.String()
	}
	// An arbitrary UTC day: 2026-09-15T00:00:00Z.
	const dayStart = 1789430400000

	if got := run(dayStart, ""); got != "" {
		t.Errorf("no floor: got %q, want \"\"", got)
	}
	if got := run(dayStart, "not-a-date"); got != "" {
		t.Errorf("invalid floor: got %q, want \"\"", got)
	}
	// The floor lands the next UTC midnight — the whole of dayStart ends before it.
	if got := run(dayStart, "2026-09-16T00:00:00Z"); got == "" {
		t.Error("day fully before the floor: got \"\", want a non-empty reason")
	} else if !strings.Contains(got, "2026-09-16") {
		t.Errorf("reason %q does not contain the floor's date (2026-09-16)", got)
	}
	// The floor's own day (same UTC midnight as dayStart) is bookable — the
	// day does not end before its own start.
	if got := run(dayStart, "2026-09-15T00:00:00Z"); got != "" {
		t.Errorf("the floor's own day: got %q, want \"\"", got)
	}
	// A later day is bookable.
	if got := run(dayStart, "2026-09-10T00:00:00Z"); got != "" {
		t.Errorf("a later day: got %q, want \"\"", got)
	}
	// The floor sits exactly at dayStart's end (00:00:00Z the next day) — the
	// "day fully before the floor" and "exact boundary" vectors are the same
	// instant (dayStart + 86400000 <= floorMs holds with equality), pinned
	// again here so the boundary itself is named, not incidental.
	if got := run(dayStart, "2026-09-16T00:00:00Z"); got == "" {
		t.Error("day ending exactly at the floor: got \"\", want blocked")
	}
}

// beforeSeriesFloorDecl lifts the shipped beforeSeriesFloor declaration out of
// the embedded app.js — the minute-grained predicate the slot list and the
// booking op's FE backstop use to refuse a specific instant that would not
// credit a recurring series' occurrence. Self-contained by construction (no
// state/DOM), so the REAL source runs here; mirrors seriesFloorReasonVM above.
var beforeSeriesFloorDecl = regexp.MustCompile(`(?s)\nfunction beforeSeriesFloor\(startsAt, floor\) \{\n.*?\n\}\n`)

func beforeSeriesFloorVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := beforeSeriesFloorDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function beforeSeriesFloor(startsAt, floor) {…}` declaration found — the extraction regex no longer matches this file")
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped beforeSeriesFloor: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("beforeSeriesFloor"))
	if !ok {
		t.Fatal("beforeSeriesFloor is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestBeforeSeriesFloor_StrictlyBeforeTheInstant pins the exact-instant guard
// behind both the slot list's filtering and the booking op's FE backstop: a
// visit is credited to a series occurrence only when its start is on or after
// nextDueAt, so anything strictly earlier — down to the second — is refused.
func TestBeforeSeriesFloor_StrictlyBeforeTheInstant(t *testing.T) {
	vm, fn := beforeSeriesFloorVM(t)
	run := func(startsAt, floor string) bool {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(startsAt), vm.ToValue(floor))
		if err != nil {
			t.Fatalf("beforeSeriesFloor(%q, %q) threw: %v", startsAt, floor, err)
		}
		return res.ToBoolean()
	}
	const floor = "2026-09-16T14:00:00Z"

	if got := run("2026-09-10T09:00:00Z", ""); got != false {
		t.Errorf("no floor: got %v, want false", got)
	}
	if got := run(floor, floor); got != false {
		t.Errorf("equal instant: got %v, want false", got)
	}
	if got := run("2026-09-16T13:59:59Z", floor); got != true {
		t.Errorf("one second before: got %v, want true", got)
	}
	if got := run("2026-09-10T09:00:00Z", "not-a-date"); got != false {
		t.Errorf("invalid floor: got %v, want false", got)
	}
}
