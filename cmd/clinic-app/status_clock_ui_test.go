package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// statusClockUIDecls lifts the shipped relativeAgo/statusClockLabel
// declarations, plus the STATUS_PAST map statusClockLabel reads, out of the
// embedded app.js — the clinic-status-clock fire's card line (verdict item
// 7). Self-contained by construction (no DOM/state), so the REAL shipped
// source runs here; mirrors visit_record_ui_test.go's extraction.
var statusClockUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction relativeAgo\(isoAt, nowMs\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nconst STATUS_PAST = \{.*?\};\n`),
	regexp.MustCompile(`(?s)\nfunction statusClockLabel\(a, nowMs\) \{\n.*?\n\}\n`),
}

func statusClockUIVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	vm := goja.New()
	for _, re := range statusClockUIDecls {
		decl := re.FindString(string(src))
		if decl == "" {
			t.Fatalf("app.js: no top-level declaration matching %s — the extraction regex no longer matches this file", re)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped declaration %s: %v", re, err)
		}
	}
	return vm
}

// msToISO renders a ms-since-epoch instant as the RFC3339 shape relativeAgo
// and statusClockLabel parse via `new Date(...)` — UTC, no fractional
// seconds, matching time.rfc3339_utc's own shape.
func msToISO(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05Z")
}

// TestRelativeAgo pins the card's short-form age (clinic-status-clock-and-
// change-notices-2026-09-17.md verdict 7): under a minute is "just now",
// under an hour is "N min ago", under a day is "N h M min ago", else "N d
// ago" — floored, never negative, and "" for a missing or unparseable
// isoAt. Pure arithmetic (no ICU involved), so exact strings are asserted.
func TestRelativeAgo(t *testing.T) {
	vm := statusClockUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("relativeAgo"))
	if !ok {
		t.Fatal("relativeAgo is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, isoAt string, nowMs int64) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(isoAt), vm.ToValue(nowMs))
		if err != nil {
			t.Fatalf("relativeAgo(%q, %d) threw: %v", isoAt, nowMs, err)
		}
		return res.String()
	}
	const now = int64(1_800_000_000_000) // an arbitrary fixed instant, ms since epoch
	ago := func(deltaMs int64) string { return msToISO(now - deltaMs) }
	for _, tc := range []struct {
		name  string
		isoAt string
		want  string
	}{
		{"0s", ago(0), "just now"},
		{"59s", ago(59_000), "just now"},
		{"60s", ago(60_000), "1 min ago"},
		{"12 min", ago(12 * 60_000), "12 min ago"},
		{"61 min", ago(61 * 60_000), "1 h 1 min ago"},
		{"3h5min", ago(3*3_600_000 + 5*60_000), "3 h 5 min ago"},
		{"25h", ago(25 * 3_600_000), "1 d ago"},
		{"missing", "", ""},
		{"garbage", "not-a-date", ""},
		{"future", msToISO(now + 60_000), "just now"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.isoAt, now); got != tc.want {
				t.Errorf("relativeAgo(%q, now=%d) = %q, want %q", tc.isoAt, now, got, tc.want)
			}
		})
	}
}

// TestStatusClockLabel pins the card's status-clock line over every status
// value the lens can project (verdict 7): checkedIn renders the wait as
// relativeAgo; a terminal status (cancelled/completed/noShow) renders the
// capitalized STATUS_PAST word plus the local moment, with a courtesy
// suffix naming a patient's own cancel or the past-due sweep's no-show;
// scheduled/confirmed, and any status missing statusAt, render nothing.
// goja has no ICU: assert the label's prefix (up to and including the "·")
// and any by-suffix, not the locale-rendered date/time itself
// (visit_record_ui_test.go's own note on this).
func TestStatusClockLabel(t *testing.T) {
	vm := statusClockUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("statusClockLabel"))
	if !ok {
		t.Fatal("statusClockLabel is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, row map[string]interface{}, nowMs int64) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(row), vm.ToValue(nowMs))
		if err != nil {
			t.Fatalf("statusClockLabel(%v) threw: %v", row, err)
		}
		return res.String()
	}
	const now = int64(1_800_000_000_000)
	checkedInAt := msToISO(now - 12*60_000) // 12 min ago

	t.Run("scheduled, no statusAt: empty", func(t *testing.T) {
		if got := run(t, map[string]interface{}{"status": "scheduled"}, now); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("confirmed with statusAt: still empty (not terminal, not checkedIn)", func(t *testing.T) {
		row := map[string]interface{}{"status": "confirmed", "statusAt": checkedInAt, "statusBy": "staff"}
		if got := run(t, row, now); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("checkedIn with statusAt: the wait, exact", func(t *testing.T) {
		row := map[string]interface{}{"status": "checkedIn", "statusAt": checkedInAt, "statusBy": "staff"}
		want := "⏱ Checked in 12 min ago"
		if got := run(t, row, now); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("checkedIn, no statusAt: empty", func(t *testing.T) {
		if got := run(t, map[string]interface{}{"status": "checkedIn"}, now); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("terminal status, no statusAt: empty", func(t *testing.T) {
		if got := run(t, map[string]interface{}{"status": "cancelled"}, now); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	for _, tc := range []struct {
		status     string
		by         string
		wantPrefix string
		wantSuffix string
	}{
		{"cancelled", "staff", "Cancelled · ", ""},
		{"cancelled", "patient", "Cancelled · ", " (by the patient)"},
		{"completed", "staff", "Completed · ", ""},
		{"completed", "patient", "Completed · ", " (by the patient)"},
		{"noShow", "staff", "Marked no-show · ", ""},
		{"noShow", "sweep", "Marked no-show · ", " (past-due sweep)"},
	} {
		t.Run(tc.status+"/"+tc.by, func(t *testing.T) {
			row := map[string]interface{}{"status": tc.status, "statusAt": checkedInAt, "statusBy": tc.by}
			got := run(t, row, now)
			if !strings.HasPrefix(got, tc.wantPrefix) {
				t.Fatalf("statusClockLabel(%v) = %q, want prefix %q", row, got, tc.wantPrefix)
			}
			if !strings.HasSuffix(got, tc.wantSuffix) {
				t.Fatalf("statusClockLabel(%v) = %q, want suffix %q", row, got, tc.wantSuffix)
			}
			if tc.wantSuffix == "" && (strings.HasSuffix(got, " (by the patient)") || strings.HasSuffix(got, " (past-due sweep)")) {
				t.Fatalf("statusClockLabel(%v) = %q, want no by-suffix", row, got)
			}
		})
	}
}
