package main

import (
	"regexp"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// leaseTermUIDecls lifts the shipped fmtDate/fmtUTCDate/applicationBannerFor/
// relistOffered/decisionOffered/entryPeriodLabel/depositRowTag/customerMemo/
// entryMemoSuffix declarations (plus fmtUTCDate's UTC_MONTH_ABBR dependency)
// out of the embedded app.js — the rotate_offer_test.go / renewal_ready_test.go
// pattern: the REAL shipped source runs here, not a copy, so these pins are
// a statement about what ships. All nine are self-contained (no DOM/state),
// so goja can evaluate them directly.
var leaseTermUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nconst UTC_MONTH_ABBR = \[.*?\];\n`),
	regexp.MustCompile(`(?s)\nfunction fmtDate\(s\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction fmtUTCDate\(s\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction applicationBannerFor\(row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction decisionOffered\(a, unit\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction relistOffered\(apps\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction entryPeriodLabel\(e\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction depositRowTag\(e\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction customerMemo\(memo\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction entryMemoSuffix\(e\) \{\n.*?\n\}\n`),
}

// leaseTermUIVM evaluates the declarations WEST OF GREENWICH: goja's Date
// reads Go's time.Local, so the process zone is pinned to America/Los_Angeles
// for the test's lifetime. Every pin below therefore runs where a midnight-UTC
// stamp parsed as a local Date reads the day before — the defect the UTC-slice
// helpers exist to avoid — and TestFmtUTCDate_DoesNotShiftWestOfGreenwich
// proves the zone actually bites in this harness.
func leaseTermUIVM(t *testing.T) *goja.Runtime {
	t.Helper()
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load America/Los_Angeles: %v", err)
	}
	prev := time.Local
	time.Local = la
	t.Cleanup(func() { time.Local = prev })
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	vm := goja.New()
	for _, re := range leaseTermUIDecls {
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

// TestFmtUTCDate_ReadsTheUTCCalendarDateSlice pins fmtUTCDate against the
// design's own worked example ("2026-09-15T00:00:00Z" -> "Sep 15, 2026") — the
// midnight-UTC stamp EndTenancy's own NotYetEnded refusal names by the same
// YYYY-MM-DD slice. It must never go through a timezone-sensitive Date parse
// (a west-of-Greenwich reader would otherwise see Sep 14), and leaseTermUIVM
// runs it under America/Los_Angeles so process-local time.Local is proven
// not to leak into it.
func TestFmtUTCDate_ReadsTheUTCCalendarDateSlice(t *testing.T) {
	vm := leaseTermUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("fmtUTCDate"))
	if !ok {
		t.Fatal("fmtUTCDate is not a function after evaluating its declaration")
	}
	for _, tc := range []struct {
		name string
		in   interface{}
		want string
	}{
		{"midnight UTC instant", "2026-09-15T00:00:00Z", "Sep 15, 2026"},
		{"a different month/day, no zero-pad on day", "2026-01-05T00:00:00Z", "Jan 5, 2026"},
		{"bare calendar date (no time component)", "2026-12-31", "Dec 31, 2026"},
		{"end of year rolls the month table correctly", "2026-12-01T00:00:00Z", "Dec 1, 2026"},
		{"empty string", "", ""},
		{"null", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var arg goja.Value
			if tc.in == nil {
				arg = goja.Null()
			} else {
				arg = vm.ToValue(tc.in)
			}
			res, err := fn(goja.Undefined(), arg)
			if err != nil {
				t.Fatalf("fmtUTCDate(%v) threw: %v", tc.in, err)
			}
			if got := res.String(); got != tc.want {
				t.Errorf("fmtUTCDate(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRelistOffered_TruthTable pins the landlord unit card's Relist gate
// against Winston's §2.2 tie-break adjudication: the manual Relist must be
// reachable even while another approved application still holds a live
// lease, whenever SOME approved application on the unit has ended — the
// double-approval case (A ended, B approved-and-live) that the OLDER
// "nothing live at all" predicate made unreachable. False only when every
// approved application is live and none has ever ended.
func TestRelistOffered_TruthTable(t *testing.T) {
	vm := leaseTermUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("relistOffered"))
	if !ok {
		t.Fatal("relistOffered is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, apps []map[string]interface{}) bool {
		t.Helper()
		var arg goja.Value
		if apps == nil {
			arg = goja.Undefined()
		} else {
			arr := make([]interface{}, len(apps))
			for i, a := range apps {
				arr[i] = a
			}
			arg = vm.ToValue(arr)
		}
		res, err := fn(goja.Undefined(), arg)
		if err != nil {
			t.Fatalf("relistOffered(%v) threw: %v", apps, err)
		}
		return res.ToBoolean()
	}

	approvedLive := map[string]interface{}{"landlordApproved": true, "tenancyEndedAt": nil}
	approvedEnded := map[string]interface{}{"landlordApproved": true, "tenancyEndedAt": "2026-09-15T00:00:00Z"}
	unapproved := map[string]interface{}{"landlordApproved": false, "tenancyEndedAt": nil}

	for _, tc := range []struct {
		name string
		apps []map[string]interface{}
		want bool
	}{
		{"no apps", nil, true},
		{"no apps (empty slice)", []map[string]interface{}{}, true},
		{"live only", []map[string]interface{}{approvedLive}, false},
		{"ended only", []map[string]interface{}{approvedEnded}, true},
		{"ended + live (§2.2 double-approval tie-break) — TRUE now", []map[string]interface{}{approvedEnded, approvedLive}, true},
		{"unapproved only", []map[string]interface{}{unapproved, unapproved}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.apps); got != tc.want {
				t.Errorf("relistOffered(%v) = %v, want %v", tc.apps, got, tc.want)
			}
		})
	}
}

// TestDecisionOffered pins the landlord decide surface's gate: Approve/Decline
// render only for a qualified, undecided application on a not-yet-leased unit.
// The two revert-proof cases sit on a unit that reads available again, where
// `a.qualified && !unitLeased` alone would re-offer a decision DecisionFinal
// already closed: the ended-and-relisted tenant (Approve would only ever be a
// silent same-value no-op) and the losing rival whose recorded loss holds
// across the relist (Approve would earn a DecisionFinal refusal).
func TestDecisionOffered(t *testing.T) {
	vm := leaseTermUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("decisionOffered"))
	if !ok {
		t.Fatal("decisionOffered is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, a map[string]interface{}, unit map[string]interface{}) bool {
		t.Helper()
		unitArg := goja.Value(goja.Undefined())
		if unit != nil {
			unitArg = vm.ToValue(unit)
		}
		res, err := fn(goja.Undefined(), vm.ToValue(a), unitArg)
		if err != nil {
			t.Fatalf("decisionOffered(%v, %v) threw: %v", a, unit, err)
		}
		return res.ToBoolean()
	}

	available := map[string]interface{}{"unitStatus": "available"}
	leased := map[string]interface{}{"unitStatus": "leased"}

	for _, tc := range []struct {
		name string
		a    map[string]interface{}
		unit map[string]interface{}
		want bool
	}{
		{"qualified, undecided, unit not leased", map[string]interface{}{"qualified": true}, available, true},
		{"qualified but unit already leased (to someone else)", map[string]interface{}{"qualified": true}, leased, false},
		{"not qualified yet", map[string]interface{}{"qualified": false}, available, false},
		{"already approved", map[string]interface{}{"qualified": true, "landlordApproved": true}, available, false},
		{"already declined", map[string]interface{}{"qualified": true, "landlordDeclined": true}, available, false},
		{"ended tenancy, unit relisted (available again) — the revert-proof case", map[string]interface{}{"qualified": true, "landlordApproved": true, "tenancyEndedAt": "2026-09-15T00:00:00Z"}, available, false},
		{"lost to a rival, unit relisted (available again) — the recorded loss holds", map[string]interface{}{"qualified": true, "lostToRival": true}, available, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.a, tc.unit); got != tc.want {
				t.Errorf("decisionOffered(%v, %v) = %v, want %v", tc.a, tc.unit, got, tc.want)
			}
		})
	}
}

// TestApplicationBannerFor_TenancyEndedWinsOverEveryOtherBranch pins the
// banner-selection precedence (design §2.4 item 2): tenancyEndedAt is a
// TERMINAL fact and must win over landlordApproved/unitStatus, missing_decision
// and even a standing decline — evaluated before every other branch.
func TestApplicationBannerFor_TenancyEndedWinsOverEveryOtherBranch(t *testing.T) {
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

	t.Run("tenancyEndedAt wins over landlordApproved+leased", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{
			"tenancyEndedAt": "2026-09-15T00:00:00Z", "landlordApproved": true, "unitStatus": "leased",
		})
		if text != "Lease ended Sep 15, 2026" {
			t.Errorf("text = %q, want the ended banner", text)
		}
	})

	t.Run("tenancyEndedAt wins over a standing decline", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{
			"tenancyEndedAt": "2026-09-15T00:00:00Z", "declined": true, "landlordDeclined": true, "declineReason": "moved",
		})
		if text != "Lease ended Sep 15, 2026" {
			t.Errorf("an ended tenancy must win over a standing decline, got %q", text)
		}
	})

	t.Run("declined (no tenancyEndedAt) still reads declined", func(t *testing.T) {
		cls, text := run(t, map[string]interface{}{"declined": true})
		if cls != "decision declined" || text != "Application declined." {
			t.Errorf("cls=%q text=%q, want declined banner", cls, text)
		}
	})

	t.Run("landlordApproved + leased (no tenancy end) reads complete", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{"landlordApproved": true, "unitStatus": "leased"})
		if text != "Application complete — all steps done." {
			t.Errorf("text = %q, want the complete banner", text)
		}
	})

	t.Run("missing_decision reads awaiting review", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{"missing_decision": true})
		if text != "Qualified — awaiting landlord review." {
			t.Errorf("text = %q, want the awaiting-review banner", text)
		}
	})

	t.Run("bare in-review default", func(t *testing.T) {
		_, text := run(t, map[string]interface{}{})
		if text != "In review — complete the open steps below." {
			t.Errorf("text = %q, want the in-review default", text)
		}
	})
}

// TestEntryPeriodLabel_NamesThePeriodAndDueDate pins the statement's period
// suffix: a recurring charge reads "covers <start> – <end> · due <due>" by the
// stamps' UTC calendar dates; a row with no period renders nothing.
func TestEntryPeriodLabel_NamesThePeriodAndDueDate(t *testing.T) {
	vm := leaseTermUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("entryPeriodLabel"))
	if !ok {
		t.Fatal("entryPeriodLabel is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, e map[string]interface{}) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(e))
		if err != nil {
			t.Fatalf("entryPeriodLabel threw: %v", err)
		}
		return res.String()
	}
	for _, tc := range []struct {
		name string
		e    map[string]interface{}
		want string
	}{
		{"rent period with due date", map[string]interface{}{"periodStart": "2026-09-06T00:00:00Z", "periodEnd": "2026-10-06T00:00:00Z", "dueAt": "2026-09-06T00:00:00Z"},
			" · covers Sep 6, 2026 – Oct 6, 2026 · due Sep 6, 2026"},
		{"period without a due date", map[string]interface{}{"periodStart": "2026-09-06T00:00:00Z", "periodEnd": "2026-10-06T00:00:00Z"},
			" · covers Sep 6, 2026 – Oct 6, 2026"},
		{"a payment has no period", map[string]interface{}{"type": "credit", "postedAt": "2026-09-14T00:00:00Z"}, ""},
		{"a half-stamped row renders nothing", map[string]interface{}{"periodStart": "2026-09-06T00:00:00Z"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.e); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDepositRowTag_ChargeReturnAndFallthrough pins depositRowTag: a
// clausePurpose="deposit" row tags the charge/return by TYPE regardless of
// what period fields it carries (a deposit clause is oneTime — never a
// period, so entryPeriodLabel would otherwise read ""); any other row
// (rent, a plain charge, a purpose-less one-time fee) falls straight
// through to entryPeriodLabel unchanged.
func TestDepositRowTag_ChargeReturnAndFallthrough(t *testing.T) {
	vm := leaseTermUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("depositRowTag"))
	if !ok {
		t.Fatal("depositRowTag is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, e map[string]interface{}) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(e))
		if err != nil {
			t.Fatalf("depositRowTag threw: %v", err)
		}
		return res.String()
	}
	for _, tc := range []struct {
		name string
		e    map[string]interface{}
		want string
	}{
		{"deposit charge (debit)", map[string]interface{}{"type": "debit", "clausePurpose": "deposit"}, " · Security deposit"},
		{"deposit return (credit)", map[string]interface{}{"type": "credit", "clausePurpose": "deposit"}, " · Security deposit returned"},
		{"deposit deduction", map[string]interface{}{"type": "deduction", "clausePurpose": "deposit"}, " · Deposit deduction"},
		{"balance paid out (no clause at all)", map[string]interface{}{"type": "debit", "kind": "payout"}, " · Balance paid out"},
		{"payout checked before clausePurpose, though a payout carries none anyway", map[string]interface{}{"type": "debit", "kind": "payout", "clausePurpose": "deposit"}, " · Balance paid out"},
		{"rent row with a period falls through", map[string]interface{}{"type": "debit", "periodStart": "2026-09-06T00:00:00Z", "periodEnd": "2026-10-06T00:00:00Z"}, " · covers Sep 6, 2026 – Oct 6, 2026"},
		{"plain charge, no purpose, no period falls through to empty", map[string]interface{}{"type": "debit"}, ""},
		{"non-deposit purpose falls through", map[string]interface{}{"type": "debit", "clausePurpose": "lockoutFee"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.e); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEntryMemoSuffix_DepositRowDropsItsOwnMemo pins the double-statement
// fix: ReturnDeposit posts a fixed "Security deposit returned" memo — the
// exact phrase depositRowTag already renders as the row's own tag — so a
// deposit row (clausePurpose === "deposit") must render NO memo suffix at
// all, whatever the memo says, on either the charge or the return; any
// other row's memo passes through customerMemo unchanged.
func TestEntryMemoSuffix_DepositRowDropsItsOwnMemo(t *testing.T) {
	vm := leaseTermUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("entryMemoSuffix"))
	if !ok {
		t.Fatal("entryMemoSuffix is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, e map[string]interface{}) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(e))
		if err != nil {
			t.Fatalf("entryMemoSuffix threw: %v", err)
		}
		return res.String()
	}
	for _, tc := range []struct {
		name string
		e    map[string]interface{}
		want string
	}{
		{"deposit return: the tag already says it, the memo is dropped",
			map[string]interface{}{"type": "credit", "clausePurpose": "deposit", "memo": "Security deposit returned"}, ""},
		{"deposit charge with no memo of its own",
			map[string]interface{}{"type": "debit", "clausePurpose": "deposit"}, ""},
		{"deposit charge that somehow carries a memo anyway — still dropped",
			map[string]interface{}{"type": "debit", "clausePurpose": "deposit", "memo": "whatever"}, ""},
		{"a deduction's memo IS the reason — kept, unlike the charge/return",
			map[string]interface{}{"type": "deduction", "clausePurpose": "deposit", "memo": "Carpet cleaning"}, " — Carpet cleaning"},
		{"a payout's fixed memo is dropped — the tag already says it",
			map[string]interface{}{"type": "debit", "kind": "payout", "memo": "Balance paid out to the tenant"}, ""},
		{"a plain rent charge's memo passes through",
			map[string]interface{}{"type": "debit", "memo": "June rent"}, " — June rent"},
		{"no memo at all",
			map[string]interface{}{"type": "debit"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.e); got != tc.want {
				t.Errorf("entryMemoSuffix(%v) = %q, want %q", tc.e, got, tc.want)
			}
		})
	}
}

// TestFmtUTCDate_DoesNotShiftWestOfGreenwich is the positive vector for the
// harness's zone: under America/Los_Angeles the local-parse helper (fmtDate,
// the right rendering for a real instant such as postedAt or expiresAt) reads
// a midnight-UTC stamp as the DAY BEFORE, while fmtUTCDate and every label
// built on it name the recorded day. The first assertion is what proves the
// pinned zone reaches goja's Date at all — were the swap ever lost, the two
// helpers would agree and the UTC pins would pass for the wrong reason. A
// date-only fact (leaseStart / leaseEnd / endedAt / termStart / availableFrom /
// moveInDate / periodStart / periodEnd / dueAt) renders through fmtUTCDate;
// this is the mandated pin shape for any card that gains one.
func TestFmtUTCDate_DoesNotShiftWestOfGreenwich(t *testing.T) {
	vm := leaseTermUIVM(t)
	const midnightUTC = "2026-09-15T00:00:00Z"
	call := func(t *testing.T, name string, arg interface{}) string {
		t.Helper()
		fn, ok := goja.AssertFunction(vm.Get(name))
		if !ok {
			t.Fatalf("%s is not a function after evaluating its declaration", name)
		}
		res, err := fn(goja.Undefined(), vm.ToValue(arg))
		if err != nil {
			t.Fatalf("%s(%v) threw: %v", name, arg, err)
		}
		return res.String()
	}
	if got := call(t, "fmtDate", midnightUTC); got != "09/14/2026" {
		t.Fatalf("fmtDate(%s) under America/Los_Angeles = %q, want the local day before (09/14/2026) — the harness zone is not reaching goja's Date, so every UTC pin in this file passes vacuously", midnightUTC, got)
	}
	if got := call(t, "fmtUTCDate", midnightUTC); got != "Sep 15, 2026" {
		t.Errorf("fmtUTCDate(%s) under America/Los_Angeles = %q, want the recorded day (Sep 15, 2026)", midnightUTC, got)
	}
	entry := map[string]interface{}{"periodStart": "2026-09-06T00:00:00Z", "periodEnd": "2026-10-06T00:00:00Z", "dueAt": "2026-09-06T00:00:00Z"}
	if got, want := call(t, "entryPeriodLabel", entry), " · covers Sep 6, 2026 – Oct 6, 2026 · due Sep 6, 2026"; got != want {
		t.Errorf("entryPeriodLabel under America/Los_Angeles = %q, want %q", got, want)
	}
}
