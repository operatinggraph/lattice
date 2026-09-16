package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// rivalTaskUIDecls lifts the shipped taskExpired / taskLostToRival /
// renewalCardTaskOps / renewalTaskStale / taskDisposition / tasksSummaryFor
// declarations out of the embedded app.js (the lease_term_ui_test.go
// pattern: the REAL source runs here, not a copy). fmtDate is stubbed to the
// identity — the only fmtDate call left in this slice is taskExpired's own
// title text (a live instant, correctly local); UTC_MONTH_ABBR + fmtUTCDate
// are the REAL shipped declarations, needed because taskDisposition's
// ended/notice titles render tenancyEndedAt/noticeMoveOutAt — date-only
// facts — through fmtUTCDate, never fmtDate (lease_term_ui_test.go's own
// mandated pin shape; a bare identity stub here would hide a west-of-
// Greenwich reader seeing the day before).
var rivalTaskUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nconst UTC_MONTH_ABBR = \[.*?\];\n`),
	regexp.MustCompile(`(?s)\nfunction fmtUTCDate\(s\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction taskExpired\(t, nowMs\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction taskLostToRival\(t, applications\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nconst renewalCardTaskOps = \[.*?\];\n`),
	regexp.MustCompile(`(?s)\nfunction renewalTaskStale\(operationName, row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction taskDisposition\(t, nowMs, canComplete, profileTask, applications, renewals\) \{\n.*?\n\}\n`),
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
	t.Run("a deadline exactly now is expired — the grant holds only while expiresAt > now", func(t *testing.T) {
		badge, _, _ := run(t, map[string]interface{}{"expiresAt": "2026-09-14T12:00:00Z"}, true, false)
		if badge != "expired" {
			t.Errorf("badge = %q, want expired at the boundary (the Processor refuses once now >= expiresAt)", badge)
		}
	})
}

// TestTaskDisposition_LostApplicationTaskIsClosed pins the second read-only
// case: a live-grant task scoped to an application the applicant has lost
// (its row's lostToRival) is closed with the reason, whatever the op; a task
// scoped to a live application, or to no loaded application, is untouched.
func TestTaskDisposition_LostApplicationTaskIsClosed(t *testing.T) {
	vm := rivalTaskUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("taskDisposition"))
	if !ok {
		t.Fatal("taskDisposition is not a function after evaluating its declaration")
	}
	apps := []map[string]interface{}{
		{"entityKey": "vtx.leaseapp.lost", "lostToRival": true},
		{"entityKey": "vtx.leaseapp.live", "lostToRival": false},
	}
	run := func(t *testing.T, task map[string]interface{}) (badge, label string, disabled bool) {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(task), vm.ToValue(rivalNowMs), vm.ToValue(true), vm.ToValue(false), vm.ToValue(apps))
		if err != nil {
			t.Fatalf("taskDisposition threw: %v", err)
		}
		obj := res.ToObject(vm)
		return obj.Get("badge").String(), obj.Get("label").String(), obj.Get("disabled").ToBoolean()
	}
	t.Run("live grant, lost application", func(t *testing.T) {
		badge, label, disabled := run(t, map[string]interface{}{"operationName": "SignLease", "scopedTo": "vtx.leaseapp.lost", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "closed" || label != "Unit no longer available" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want closed/Unit no longer available/true", badge, label, disabled)
		}
	})
	t.Run("expired still wins over lost", func(t *testing.T) {
		badge, _, _ := run(t, map[string]interface{}{"operationName": "SignLease", "scopedTo": "vtx.leaseapp.lost", "expiresAt": "2026-08-31T00:00:00Z"})
		if badge != "expired" {
			t.Errorf("badge = %q, want expired", badge)
		}
	})
	t.Run("live application stays open", func(t *testing.T) {
		badge, label, disabled := run(t, map[string]interface{}{"operationName": "SignLease", "scopedTo": "vtx.leaseapp.live", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "open" || label != "Complete" || disabled {
			t.Errorf("badge=%q label=%q disabled=%v", badge, label, disabled)
		}
	})
	t.Run("a task scoped to no loaded application is not judged", func(t *testing.T) {
		badge, _, disabled := run(t, map[string]interface{}{"operationName": "RecordIdentityPII", "scopedTo": "vtx.identity.bob"})
		if badge != "open" || disabled {
			t.Errorf("badge=%q disabled=%v, want open", badge, disabled)
		}
	})
}

// TestTaskDisposition_RenewalTaskStaleIsClosed pins the inbox's degrade for a
// renewal-chain task (SetRenewalTerms/VerifyGuarantor/SignRenewal) whose own
// renewal cycle moved past it after the task was assigned — the
// refusal-courtesy fix: before this, canCompleteOp + taskDisposition's
// expired/lostToRival were the ONLY gates, so a SignRenewal task assigned
// while a cycle was open still offered a live Complete after the operator
// ended the tenancy (raw TenancyEnded) or after CancelRenewal closed the
// cycle (raw RenewalNotOpen) — exactly what renderRenewalCard already hides
// on its own Sign button. Positive: an ended tenancy is not offered.
// Negative: an open, unsigned renewal keeps offering it.
func TestTaskDisposition_RenewalTaskStaleIsClosed(t *testing.T) {
	vm := rivalTaskUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("taskDisposition"))
	if !ok {
		t.Fatal("taskDisposition is not a function after evaluating its declaration")
	}
	renewals := []map[string]interface{}{
		{"entityKey": "vtx.renewal.ended", "status": "open", "tenancyEndedAt": "2026-09-10T00:00:00Z"},
		{"entityKey": "vtx.renewal.noticed", "status": "open", "noticeMoveOutAt": "2026-10-01T00:00:00Z"},
		{"entityKey": "vtx.renewal.cancelled", "status": "cancelled"},
		{"entityKey": "vtx.renewal.open", "status": "open"},
	}
	run := func(t *testing.T, task map[string]interface{}) (badge, label string, disabled bool) {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(task), vm.ToValue(rivalNowMs), vm.ToValue(true), vm.ToValue(false), vm.ToValue([]map[string]interface{}{}), vm.ToValue(renewals))
		if err != nil {
			t.Fatalf("taskDisposition threw: %v", err)
		}
		obj := res.ToObject(vm)
		return obj.Get("badge").String(), obj.Get("label").String(), obj.Get("disabled").ToBoolean()
	}
	t.Run("SignRenewal on an ended tenancy is not offered", func(t *testing.T) {
		badge, label, disabled := run(t, map[string]interface{}{"operationName": "SignRenewal", "scopedTo": "vtx.renewal.ended", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "closed" || label != "Lease ended" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want closed/Lease ended/true", badge, label, disabled)
		}
	})
	t.Run("SignRenewal on a noticed lease is not offered", func(t *testing.T) {
		badge, label, disabled := run(t, map[string]interface{}{"operationName": "SignRenewal", "scopedTo": "vtx.renewal.noticed", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "closed" || label != "Notice given" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want closed/Notice given/true", badge, label, disabled)
		}
	})
	// A notice closes renewalCompleteSpec's OWN `open` conjunct (renewal_lenses.go:
	// `(status = 'open') AND (tenancyEndedAt = null) AND (noticeMoveOutAt = null)`),
	// which gates missing_renewalComplete for EVERY leg — not only SignRenewal's
	// own script refusal — so the landlord's SetRenewalTerms/VerifyGuarantor tasks,
	// dispatched before the notice landed, must close here too even though
	// neither op's own script reads .notice.
	t.Run("SetRenewalTerms on a noticed lease is not offered", func(t *testing.T) {
		badge, label, disabled := run(t, map[string]interface{}{"operationName": "SetRenewalTerms", "scopedTo": "vtx.renewal.noticed", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "closed" || label != "Notice given" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want closed/Notice given/true", badge, label, disabled)
		}
	})
	t.Run("VerifyGuarantor on a noticed lease is not offered", func(t *testing.T) {
		badge, label, disabled := run(t, map[string]interface{}{"operationName": "VerifyGuarantor", "scopedTo": "vtx.renewal.noticed", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "closed" || label != "Notice given" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want closed/Notice given/true", badge, label, disabled)
		}
	})
	t.Run("SignRenewal on a cancelled renewal is not offered", func(t *testing.T) {
		badge, label, disabled := run(t, map[string]interface{}{"operationName": "SignRenewal", "scopedTo": "vtx.renewal.cancelled", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "closed" || label != "Renewal no longer open" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want closed/Renewal no longer open/true", badge, label, disabled)
		}
	})
	t.Run("SetRenewalTerms on a cancelled renewal is not offered", func(t *testing.T) {
		badge, label, disabled := run(t, map[string]interface{}{"operationName": "SetRenewalTerms", "scopedTo": "vtx.renewal.cancelled", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "closed" || label != "Renewal no longer open" || !disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want closed/Renewal no longer open/true", badge, label, disabled)
		}
	})
	t.Run("expired still wins over a stale renewal", func(t *testing.T) {
		badge, _, _ := run(t, map[string]interface{}{"operationName": "SignRenewal", "scopedTo": "vtx.renewal.ended", "expiresAt": "2026-08-31T00:00:00Z"})
		if badge != "expired" {
			t.Errorf("badge = %q, want expired", badge)
		}
	})
	t.Run("SignRenewal on an open, unsigned renewal keeps offering Complete", func(t *testing.T) {
		badge, label, disabled := run(t, map[string]interface{}{"operationName": "SignRenewal", "scopedTo": "vtx.renewal.open", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "open" || label != "Complete" || disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want open/Complete/false", badge, label, disabled)
		}
	})
	t.Run("VerifyGuarantor is never judged against tenancyEndedAt/status", func(t *testing.T) {
		badge, label, disabled := run(t, map[string]interface{}{"operationName": "VerifyGuarantor", "scopedTo": "vtx.renewal.ended", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "open" || label != "Complete" || disabled {
			t.Errorf("badge=%q label=%q disabled=%v, want open/Complete/false", badge, label, disabled)
		}
	})
	t.Run("a task scoped to no loaded renewal is not judged", func(t *testing.T) {
		badge, _, disabled := run(t, map[string]interface{}{"operationName": "SignRenewal", "scopedTo": "vtx.renewal.unloaded", "expiresAt": "2026-10-01T00:00:00Z"})
		if badge != "open" || disabled {
			t.Errorf("badge=%q disabled=%v, want open", badge, disabled)
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
