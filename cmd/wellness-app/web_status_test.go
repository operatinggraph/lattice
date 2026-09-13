package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"
)

// TestIsLateCancel_ForfeitedAndWaitlistedNeverForfeitAgain proves
// isLateCancel treats a forfeited booking exactly like a waitlisted one — no
// seat and no posted charge left to lose a second time — while a booked
// seat inside the 2-hour window still reads as a late cancel.
func TestIsLateCancel_ForfeitedAndWaitlistedNeverForfeitAgain(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	windowDecl := regexp.MustCompile(`const LATE_CANCEL_WINDOW_MS = [^;]+;`).FindString(string(src))
	if windowDecl == "" {
		t.Fatal("app.js: no `const LATE_CANCEL_WINDOW_MS = …;` declaration found — isLateCancel reads it")
	}

	vm := webHelperVM(t, "isLateCancel")
	if _, err := vm.RunString(windowDecl); err != nil {
		t.Fatalf("goja eval of the shipped LATE_CANCEL_WINDOW_MS: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("isLateCancel"))
	if !ok {
		t.Fatal("isLateCancel is not a function after evaluating its declaration")
	}

	startsAt := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)

	call := func(t *testing.T, status string) bool {
		t.Helper()
		b := map[string]any{"status": status, "priceCents": 1500, "startsAt": startsAt}
		res, err := fn(goja.Undefined(), vm.ToValue(b))
		if err != nil {
			t.Fatalf("isLateCancel threw: %v", err)
		}
		return res.ToBoolean()
	}

	require.False(t, call(t, "forfeited"), "an already-forfeited booking has nothing left to forfeit")
	require.False(t, call(t, "waitlisted"), "a waitlisted booking holds no posted charge")
	require.True(t, call(t, "booked"), "a booked seat inside the late-cancel window still forfeits")
}

// TestAttendanceMarks_CoversForfeited proves the ATTENDANCE_MARKS literal
// carries a forfeited entry — the map is a `const` object literal, not a
// function, so webDeclRe's function-shaped extraction cannot lift it the way
// isLateCancel is lifted above; asserting directly on the shipped source is
// the cheapest test that still fails if the entry is ever removed or
// misspelled.
func TestAttendanceMarks_CoversForfeited(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	re := regexp.MustCompile(`(?s)const ATTENDANCE_MARKS = \{.*?\n\};`)
	m := re.FindString(string(src))
	if m == "" {
		t.Fatal("app.js: no `const ATTENDANCE_MARKS = {…};` literal found — it may have moved or been renamed")
	}
	require.True(t, strings.Contains(m, "forfeited:"), "ATTENDANCE_MARKS must carry a forfeited: entry\n%s", m)
}
