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

	vm := webHelperVM(t, "promotedInsideWindow", "isLateCancel")
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

// lateCancelVM lifts isLateCancel and promotedInsideWindow with the shipped
// LATE_CANCEL_WINDOW_MS beside them — the two predicates share the constant,
// so the pins below read the same window the page does.
func lateCancelVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	windowDecl := regexp.MustCompile(`const LATE_CANCEL_WINDOW_MS = [^;]+;`).FindString(string(src))
	if windowDecl == "" {
		t.Fatal("app.js: no `const LATE_CANCEL_WINDOW_MS = …;` declaration found")
	}
	vm := webHelperVM(t, "promotedInsideWindow", "isLateCancel")
	if _, err := vm.RunString(windowDecl); err != nil {
		t.Fatalf("goja eval of the shipped LATE_CANCEL_WINDOW_MS: %v", err)
	}
	return vm
}

// TestPromotedInsideWindow_MirrorsTheScriptsInequality pins the FE predicate
// to CancelBooking's own `promotedAt >= late_cancel_cutoff`
// (packages/wellness-domain/ddls.go): a promotion exactly two hours before
// the start is exempt, one minute earlier is not, and a seat with no stamp
// (booked directly) or an unparseable one never is.
func TestPromotedInsideWindow_MirrorsTheScriptsInequality(t *testing.T) {
	vm := lateCancelVM(t)
	fn, ok := goja.AssertFunction(vm.Get("promotedInsideWindow"))
	if !ok {
		t.Fatal("promotedInsideWindow is not a function after evaluating its declaration")
	}
	call := func(t *testing.T, promotedAt any) bool {
		t.Helper()
		b := map[string]any{"status": "booked", "priceCents": 1500, "startsAt": "2026-07-08T09:00:00Z"}
		if promotedAt != nil {
			b["promotedAt"] = promotedAt
		}
		res, err := fn(goja.Undefined(), vm.ToValue(b))
		if err != nil {
			t.Fatalf("promotedInsideWindow threw: %v", err)
		}
		return res.ToBoolean()
	}

	require.True(t, call(t, "2026-07-08T07:00:00Z"), "promoted exactly on the two-hour mark is exempt (the script's >=)")
	require.True(t, call(t, "2026-07-08T08:57:00Z"), "promoted three minutes out is exempt")
	require.False(t, call(t, "2026-07-08T06:59:00Z"), "promoted one minute before the cutoff is an ordinary seat")
	require.False(t, call(t, nil), "a seat booked directly carries no promotedAt")
	require.False(t, call(t, "not-a-time"), "an unparseable stamp never exempts")
}

// TestIsLateCancel_PromotedInsideWindowIsExempt is the revert-proof vector for
// the exemption conjunct: the SAME booked, priced seat inside the window reads
// as a late cancel without a promotedAt and with one before the cutoff, and
// does not with one inside it — so the cancel confirm never tells a member
// the op is about to refund them that it will forfeit.
func TestIsLateCancel_PromotedInsideWindowIsExempt(t *testing.T) {
	vm := lateCancelVM(t)
	fn, ok := goja.AssertFunction(vm.Get("isLateCancel"))
	if !ok {
		t.Fatal("isLateCancel is not a function after evaluating its declaration")
	}
	startsAt := time.Now().Add(10 * time.Minute).UTC()
	call := func(t *testing.T, promotedAt any) bool {
		t.Helper()
		b := map[string]any{"status": "booked", "priceCents": 1500, "startsAt": startsAt.Format(time.RFC3339)}
		if promotedAt != nil {
			b["promotedAt"] = promotedAt
		}
		res, err := fn(goja.Undefined(), vm.ToValue(b))
		if err != nil {
			t.Fatalf("isLateCancel threw: %v", err)
		}
		return res.ToBoolean()
	}

	require.True(t, call(t, nil), "a directly booked seat inside the window forfeits")
	require.True(t, call(t, startsAt.Add(-3*time.Hour).Format(time.RFC3339)), "a seat promoted before the cutoff forfeits like any other")
	require.False(t, call(t, startsAt.Add(-30*time.Minute).Format(time.RFC3339)), "a seat promoted inside the window cancels free")
}

// TestPromotedBadge_NamesTheSeating pins the badge both My Classes and the
// roster render off promotedAt, and its absence on a directly booked seat.
func TestPromotedBadge_NamesTheSeating(t *testing.T) {
	vm := webHelperVM(t, "esc", "fmtTime", "fmtDay", "promotedBadge")
	fn, ok := goja.AssertFunction(vm.Get("promotedBadge"))
	if !ok {
		t.Fatal("promotedBadge is not a function after evaluating its declaration")
	}
	call := func(t *testing.T, b map[string]any) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(b))
		if err != nil {
			t.Fatalf("promotedBadge threw: %v", err)
		}
		return res.String()
	}
	promoted := call(t, map[string]any{"status": "booked", "promotedAt": "2026-07-08T07:57:00Z"})
	require.Contains(t, promoted, `class="badge promoted"`)
	require.Contains(t, promoted, "Seated from the waitlist · ")
	require.Equal(t, "", call(t, map[string]any{"status": "booked"}), "a directly booked seat carries no seating badge")
}

// TestPromotedCancelNote_OnlyForALiveSeatPromotedInsideTheWindow pins the
// card line that tells a member the exemption applies: rendered for a booked
// seat promoted inside the window on a class yet to start, and for nothing
// else — not a seat promoted before the cutoff, not a started class, not a
// settled record.
func TestPromotedCancelNote_OnlyForALiveSeatPromotedInsideTheWindow(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	windowDecl := regexp.MustCompile(`const LATE_CANCEL_WINDOW_MS = [^;]+;`).FindString(string(src))
	vm := webHelperVM(t, "promotedInsideWindow", "promotedCancelNote")
	if _, err := vm.RunString(windowDecl); err != nil {
		t.Fatalf("goja eval of the shipped LATE_CANCEL_WINDOW_MS: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("promotedCancelNote"))
	if !ok {
		t.Fatal("promotedCancelNote is not a function after evaluating its declaration")
	}
	call := func(t *testing.T, status string, startsAt time.Time, promotedAt time.Time) string {
		t.Helper()
		b := map[string]any{"status": status, "priceCents": 1000, "startsAt": startsAt.Format(time.RFC3339), "promotedAt": promotedAt.Format(time.RFC3339)}
		res, err := fn(goja.Undefined(), vm.ToValue(b))
		if err != nil {
			t.Fatalf("promotedCancelNote threw: %v", err)
		}
		return res.String()
	}
	upcoming := time.Now().Add(90 * time.Minute).UTC()
	started := time.Now().Add(-10 * time.Minute).UTC()

	require.Contains(t, call(t, "booked", upcoming, upcoming.Add(-30*time.Minute)), "cancelling is free until the class begins")
	require.Equal(t, "", call(t, "booked", upcoming, upcoming.Add(-3*time.Hour)), "promoted before the cutoff: the ordinary rule applies, nothing to say")
	require.Equal(t, "", call(t, "booked", started, started.Add(-30*time.Minute)), "a started class cannot be cancelled at all")
	require.Equal(t, "", call(t, "attended", upcoming, upcoming.Add(-30*time.Minute)), "a settled record is not cancellable")

	free := map[string]any{"status": "booked", "priceCents": 0, "startsAt": upcoming.Format(time.RFC3339), "promotedAt": upcoming.Add(-30 * time.Minute).Format(time.RFC3339)}
	res, err := fn(goja.Undefined(), vm.ToValue(free))
	require.NoError(t, err)
	require.Equal(t, "", res.String(), "a free class has no forfeit rule to be exempt from")
}
