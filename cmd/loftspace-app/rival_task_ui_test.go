package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// rivalTaskUIDecls lifts the shipped taskExpired / taskDisposition /
// tasksSummaryFor declarations out of the embedded app.js (the
// lease_term_ui_test.go pattern: the REAL source runs here, not a copy).
// fmtDate is stubbed to the identity — taskDisposition only reaches it in the
// expired branch's title, and its locale formatting is not what these pins
// are about.
var rivalTaskUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction taskExpired\(t, nowMs\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction taskDisposition\(t, nowMs, canComplete, profileTask\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction tasksSummaryFor\(tasks, nowMs\) \{\n.*?\n\}\n`),
}

func rivalTaskUIVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	vm := goja.New()
	if _, err := vm.RunString(`function fmtDate(s) { return s; }`); err != nil {
		t.Fatalf("fmtDate stub: %v", err)
	}
	for _, re := range rivalTaskUIDecls {
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

// The clock the pins run against: 2026-09-14T12:00:00Z as epoch millis.
const rivalNowMs = 1789387200000.0

// TestApplicationBannerFor_LostToRivalReadsWentToAnotherApplicant pins the
// new branch's place in the precedence: a losing rival (lostToRival, every gap
// closed, no decision) reads "went to another applicant" — never the default
// "In review" — while every terminal-or-approved branch above it still wins.
func TestApplicationBannerFor_LostToRivalReadsWentToAnotherApplicant(t *testing.T) {
	vm := leaseTermUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("applicationBannerFor"))
	if !ok {
		t.Fatal("applicationBannerFor is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, row map[string]interface{}) (string, string) {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(row))
		if err != nil {
			t.Fatalf("applicationBannerFor(%v) threw: %v", row, err)
		}
		obj := res.ToObject(vm)
		return obj.Get("cls").String(), obj.Get("text").String()
	}

	t.Run("lostToRival with every gap closed", func(t *testing.T) {
		cls, text := run(t, map[string]interface{}{
			"lostToRival": true, "unitStatus": "leased",
			"missing_onboarding": false, "missing_bgcheck": false, "missing_payment": false,
			"missing_signature": false, "missing_decision": false,
		})
		if cls != "decision declined" || text != "This unit went to another applicant." {
			t.Errorf("cls=%q text=%q, want the lost-to-rival banner", cls, text)
		}
	})

	t.Run("without lostToRival the same closed-gap row still reads in review", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{
			"unitStatus":         "leased",
			"missing_onboarding": false, "missing_bgcheck": false, "missing_payment": false,
			"missing_signature": false, "missing_decision": false,
		})
		if text != "In review — complete the open steps below." {
			t.Errorf("text = %q, want the default banner when the column is absent", text)
		}
	})

	t.Run("a decline outranks lostToRival", func(t *testing.T) {
		cls, _ := run(t, map[string]interface{}{"lostToRival": true, "declined": true})
		if cls != "decision declined" {
			t.Errorf("cls = %q", cls)
		}
		_, text := run(t, map[string]interface{}{"lostToRival": true, "declined": true})
		if text != "Application declined." {
			t.Errorf("text = %q, want the decline banner", text)
		}
	})

	t.Run("approval outranks lostToRival", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{"lostToRival": true, "landlordApproved": true, "unitStatus": "leased"})
		if text != "Application complete — all steps done." {
			t.Errorf("text = %q, want the complete banner", text)
		}
	})

	t.Run("an ended tenancy outranks lostToRival", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{"lostToRival": true, "tenancyEndedAt": "2026-09-15T00:00:00Z"})
		if text != "Lease ended Sep 15, 2026" {
			t.Errorf("text = %q, want the ended banner", text)
		}
	})
}

// TestTaskDisposition_ExpiredIsReadOnly pins the inbox's read-only expired
// task: the Complete control is disabled and labelled Expired whatever the
// op's completability, while an open task keeps the three prior shapes.
func TestTaskDisposition_ExpiredIsReadOnly(t *testing.T) {
	vm := rivalTaskUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("taskDisposition"))
	if !ok {
		t.Fatal("taskDisposition is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, task map[string]interface{}, canComplete, profile bool) (badge, label string, disabled bool) {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(task), vm.ToValue(rivalNowMs), vm.ToValue(canComplete), vm.ToValue(profile))
		if err != nil {
			t.Fatalf("taskDisposition threw: %v", err)
		}
		obj := res.ToObject(vm)
		return obj.Get("badge").String(), obj.Get("label").String(), obj.Get("disabled").ToBoolean()
	}
	expiredTask := map[string]interface{}{"operationName": "SignLease", "expiresAt": "2026-08-31T00:00:00Z"}
	openTask := map[string]interface{}{"operationName": "SignLease", "expiresAt": "2026-10-01T00:00:00Z"}
	noDeadline := map[string]interface{}{"operationName": "SignLease"}

	t.Run("expired + completable op is still disabled", func(t *testing.T) {
		badge, label, disabled := run(t, expiredTask, true, false)
		if badge != "expired" || label != "Expired" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want expired/Expired/true", badge, label, disabled)
		}
	})
	t.Run("expired profile task is disabled too", func(t *testing.T) {
		badge, label, disabled := run(t, expiredTask, true, true)
		if badge != "expired" || label != "Expired" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v", badge, label, disabled)
		}
	})
	t.Run("open + completable", func(t *testing.T) {
		badge, label, disabled := run(t, openTask, true, false)
		if badge != "open" || label != "Complete" || disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want open/Complete/false", badge, label, disabled)
		}
	})
	t.Run("open + not completable here", func(t *testing.T) {
		badge, label, disabled := run(t, openTask, false, false)
		if badge != "open" || label != "Complete in Loupe" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v", badge, label, disabled)
		}
	})
	t.Run("open profile task with its application not loaded", func(t *testing.T) {
		badge, label, disabled := run(t, openTask, false, true)
		if badge != "open" || label != "Complete" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v", badge, label, disabled)
		}
	})
	t.Run("no deadline is never expired", func(t *testing.T) {
		badge, _, disabled := run(t, noDeadline, true, false)
		if badge != "open" || disabled {
			t.Errorf("badge=%q disabled=%v", badge, disabled)
		}
	})
	t.Run("a deadline exactly now is not yet expired", func(t *testing.T) {
		badge, _, _ := run(t, map[string]interface{}{"expiresAt": "2026-09-14T12:00:00Z"}, true, false)
		if badge != "open" {
			t.Errorf("badge = %q, want open at the boundary (expired is strictly before now)", badge)
		}
	})
}

// TestTasksSummaryFor_CountsExpiredApart pins the inbox summary: expired
// tasks stay listed but are not counted as open.
func TestTasksSummaryFor_CountsExpiredApart(t *testing.T) {
	vm := rivalTaskUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("tasksSummaryFor"))
	if !ok {
		t.Fatal("tasksSummaryFor is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, tasks []map[string]interface{}) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(tasks), vm.ToValue(rivalNowMs))
		if err != nil {
			t.Fatalf("tasksSummaryFor threw: %v", err)
		}
		return res.String()
	}
	for _, tc := range []struct {
		name  string
		tasks []map[string]interface{}
		want  string
	}{
		{"two open", []map[string]interface{}{{"expiresAt": "2026-10-01T00:00:00Z"}, {}}, "2 open tasks"},
		{"one open one expired", []map[string]interface{}{{"expiresAt": "2026-10-01T00:00:00Z"}, {"expiresAt": "2026-08-31T00:00:00Z"}}, "1 open task · 1 expired"},
		{"all expired", []map[string]interface{}{{"expiresAt": "2026-08-31T00:00:00Z"}, {"expiresAt": "2026-08-30T00:00:00Z"}}, "0 open tasks · 2 expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.tasks); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
