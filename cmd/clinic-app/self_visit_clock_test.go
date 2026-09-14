package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// selfVisitClockConstDecl and selfVisitClockDecl lift the shipped
// LATE_CANCEL_WINDOW_MS constant and selfVisitClock declaration out of the
// embedded app.js — the pure predicate deciding whether a patient
// self-service cancel/reschedule sits before, inside, or past the clinic's
// no-show-fee window. Self-contained by construction (no state/DOM), so the
// REAL source runs here; mirrors lifecycle_transitions_test.go's extraction.
var selfVisitClockConstDecl = regexp.MustCompile(`(?m)^const LATE_CANCEL_WINDOW_MS = .*?;$`)
var selfVisitClockDecl = regexp.MustCompile(`(?s)\nfunction selfVisitClock\(startsAt, nowMs\) \{\n.*?\n\}\n`)

func selfVisitClockVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	constDecl := selfVisitClockConstDecl.FindString(string(src))
	if constDecl == "" {
		t.Fatal("app.js: no top-level `const LATE_CANCEL_WINDOW_MS = …;` declaration found — the extraction regex no longer matches this file")
	}
	fnDecl := selfVisitClockDecl.FindString(string(src))
	if fnDecl == "" {
		t.Fatal("app.js: no top-level `function selfVisitClock(startsAt, nowMs) {…}` declaration found — the extraction regex no longer matches this file")
	}
	vm := goja.New()
	if _, err := vm.RunString(constDecl + "\n" + fnDecl); err != nil {
		t.Fatalf("goja eval of the shipped selfVisitClock: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("selfVisitClock"))
	if !ok {
		t.Fatal("selfVisitClock is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestSelfVisitClockWindow pins the FE mirror of the self-path clock
// (packages/clinic-domain/ddls.go's LATE_CANCEL_WINDOW_OFFSET): at and after
// a visit's own start the visit has "started"; from 24h before start up to
// (not including) start it is "late"; earlier than that, or given no usable
// startsAt, it is "open".
func TestSelfVisitClockWindow(t *testing.T) {
	vm, fn := selfVisitClockVM(t)
	const startsAt = "2026-09-20T15:00:00Z"
	startsMs := float64(1789916400000) // 2026-09-20T15:00:00Z in epoch ms

	run := func(nowMs float64) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(startsAt), vm.ToValue(nowMs))
		if err != nil {
			t.Fatalf("selfVisitClock(%q, %v) threw: %v", startsAt, nowMs, err)
		}
		return res.String()
	}

	dayMs := float64(24 * 60 * 60 * 1000)

	for _, tc := range []struct {
		name string
		now  float64
		want string
	}{
		{"at startsAt", startsMs, "started"},
		{"1ms after startsAt", startsMs + 1, "started"},
		{"1ms before startsAt", startsMs - 1, "late"},
		{"exactly 24h before startsAt", startsMs - dayMs, "late"},
		{"24h and 1ms before startsAt", startsMs - dayMs - 1, "open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.now); got != tc.want {
				t.Errorf("selfVisitClock(%q, now=%v) = %q, want %q", startsAt, tc.now, got, tc.want)
			}
		})
	}

	for _, bad := range []goja.Value{vm.ToValue(""), vm.ToValue("not-a-date"), goja.Undefined()} {
		t.Run("missing or garbage startsAt", func(t *testing.T) {
			res, err := fn(goja.Undefined(), bad, vm.ToValue(startsMs))
			if err != nil {
				t.Fatalf("selfVisitClock(%v, %v) threw: %v", bad, startsMs, err)
			}
			if got := res.String(); got != "open" {
				t.Errorf("selfVisitClock(%v, now=startsAt) = %q, want %q", bad, got, "open")
			}
		})
	}
}

// TestSelfVisitClockWindowIsADay pins LATE_CANCEL_WINDOW_MS itself at 24
// hours — the reminder-lead window the design ties the self late-cancel
// clock to.
func TestSelfVisitClockWindowIsADay(t *testing.T) {
	vm, _ := selfVisitClockVM(t)
	got := vm.Get("LATE_CANCEL_WINDOW_MS").ToInteger()
	want := int64(24 * 60 * 60 * 1000)
	if got != want {
		t.Errorf("LATE_CANCEL_WINDOW_MS = %d, want %d", got, want)
	}
}
