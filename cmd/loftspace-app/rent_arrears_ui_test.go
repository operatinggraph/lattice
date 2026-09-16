package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// rentArrearsUIDecls lifts the shipped UTC_MONTH_ABBR/fmtUTCDate/moneyAmount/
// rentBalanceLine/rentAgeText/depositLine/listingDepositLine declarations out
// of the embedded app.js — the rotate_offer_test.go / renewal_ready_test.go /
// lease_term_ui_test.go pattern: the REAL shipped source runs here, not a
// copy, so these pins are a statement about what ships. All seven are
// self-contained (no DOM/state), so goja can evaluate them directly.
var rentArrearsUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nconst UTC_MONTH_ABBR = \[.*?\];\n`),
	regexp.MustCompile(`(?s)\nfunction fmtUTCDate\(s\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction moneyAmount\(n\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction rentBalanceLine\(data\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction rentAgeText\(row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction depositLine\(data\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction listingDepositLine\(listing\) \{\n.*?\n\}\n`),
}

// rentArrearsUIVM evaluates the declarations WEST OF GREENWICH — goja's Date
// reads Go's time.Local, so the process zone is pinned to
// America/Los_Angeles for the test's lifetime, the lease_term_ui_test.go
// harness shape. Every fmtUTCDate-derived pin below therefore runs where a
// midnight-UTC stamp parsed through a locale Date would read the day
// before — the defect the UTC-slice helper exists to avoid, and the
// vertical-apps dossier's "two courtesy surfaces name the same instant"
// class this file guards.
func rentArrearsUIVM(t *testing.T) *goja.Runtime {
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
	for _, re := range rentArrearsUIDecls {
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

func callRentBalanceLine(t *testing.T, vm *goja.Runtime, data map[string]interface{}) string {
	t.Helper()
	fn, ok := goja.AssertFunction(vm.Get("rentBalanceLine"))
	if !ok {
		t.Fatal("rentBalanceLine is not a function after evaluating its declaration")
	}
	res, err := fn(goja.Undefined(), vm.ToValue(data))
	if err != nil {
		t.Fatalf("rentBalanceLine(%v) threw: %v", data, err)
	}
	return res.String()
}

func callRentAgeText(t *testing.T, vm *goja.Runtime, row map[string]interface{}) string {
	t.Helper()
	fn, ok := goja.AssertFunction(vm.Get("rentAgeText"))
	if !ok {
		t.Fatal("rentAgeText is not a function after evaluating its declaration")
	}
	res, err := fn(goja.Undefined(), vm.ToValue(row))
	if err != nil {
		t.Fatalf("rentAgeText(%v) threw: %v", row, err)
	}
	return res.String()
}

func callDepositLine(t *testing.T, vm *goja.Runtime, data map[string]interface{}) string {
	t.Helper()
	fn, ok := goja.AssertFunction(vm.Get("depositLine"))
	if !ok {
		t.Fatal("depositLine is not a function after evaluating its declaration")
	}
	res, err := fn(goja.Undefined(), vm.ToValue(data))
	if err != nil {
		t.Fatalf("depositLine(%v) threw: %v", data, err)
	}
	return res.String()
}

func callListingDepositLine(t *testing.T, vm *goja.Runtime, listing map[string]interface{}) string {
	t.Helper()
	fn, ok := goja.AssertFunction(vm.Get("listingDepositLine"))
	if !ok {
		t.Fatal("listingDepositLine is not a function after evaluating its declaration")
	}
	res, err := fn(goja.Undefined(), vm.ToValue(listing))
	if err != nil {
		t.Fatalf("listingDepositLine(%v) threw: %v", listing, err)
	}
	return res.String()
}

// TestRentBalanceLine_EverySuffix pins rentBalanceLine's every branch
// against the design verdict §5 wording: owed-only (no age at all), the
// base "rent due" clause with today's due-today branch, singular/plural
// overdue, singular/plural due-in, the reminder clause (which stands on its
// own, appended whatever the due-date branch said), the credit and
// paid-in-full splits, and the one-bill combined-balance naming rule (a rent
// balance that differs from the total names itself explicitly so the age is
// never misread as describing the café portion too).
func TestRentBalanceLine_EverySuffix(t *testing.T) {
	vm := rentArrearsUIVM(t)
	for _, tc := range []struct {
		name string
		data map[string]interface{}
		want string
	}{
		{
			"owed-only, no due date at all",
			map[string]interface{}{"balanceCents": 5000.0},
			"Balance owed: $50",
		},
		{
			"due today (not overdue, no days-until)",
			map[string]interface{}{"balanceCents": 5000.0, "dueDate": "2026-09-15T00:00:00Z", "isOverdue": false, "daysOverdue": 0.0, "daysUntilDue": 0.0},
			"Balance owed: $50 · rent due Sep 15, 2026 · due today",
		},
		{
			"overdue, singular day",
			map[string]interface{}{"balanceCents": 5000.0, "dueDate": "2026-09-08T00:00:00Z", "isOverdue": true, "daysOverdue": 1.0},
			"Balance owed: $50 · rent due Sep 8, 2026 · 1 day overdue",
		},
		{
			"overdue, plural days",
			map[string]interface{}{"balanceCents": 240000.0, "dueDate": "2026-09-08T00:00:00Z", "isOverdue": true, "daysOverdue": 7.0},
			"Balance owed: $2400 · rent due Sep 8, 2026 · 7 days overdue",
		},
		{
			"due in, singular day",
			map[string]interface{}{"balanceCents": 330000.0, "dueDate": "2026-09-16T00:00:00Z", "isOverdue": false, "daysUntilDue": 1.0},
			"Balance owed: $3300 · rent due Sep 16, 2026 · due in 1 day",
		},
		{
			"due in, plural days",
			map[string]interface{}{"balanceCents": 330000.0, "dueDate": "2026-09-22T00:00:00Z", "isOverdue": false, "daysUntilDue": 7.0},
			"Balance owed: $3300 · rent due Sep 22, 2026 · due in 7 days",
		},
		{
			"reminder clause appended after overdue",
			map[string]interface{}{"balanceCents": 5000.0, "dueDate": "2026-09-08T00:00:00Z", "isOverdue": true, "daysOverdue": 7.0, "reminderSentAt": "2026-09-13T00:00:00Z"},
			"Balance owed: $50 · rent due Sep 8, 2026 · 7 days overdue · a reminder was sent Sep 13, 2026",
		},
		{
			"credit balance",
			map[string]interface{}{"balanceCents": -1500.0},
			"Credit balance: $15",
		},
		{
			"paid in full",
			map[string]interface{}{"balanceCents": 0.0},
			"Balance: $0.00 (paid in full)",
		},
		{
			"one-bill combined: rent differs from the total, names itself",
			map[string]interface{}{"balanceCents": 7000.0, "rentBalanceCents": 5000.0, "dueDate": "2026-09-08T00:00:00Z", "isOverdue": true, "daysOverdue": 7.0},
			"Balance owed: $70 · rent owed $50, due Sep 8, 2026 · 7 days overdue",
		},
		{
			"one-bill: rentBalanceCents equal to the total is NOT combined wording",
			map[string]interface{}{"balanceCents": 5000.0, "rentBalanceCents": 5000.0, "dueDate": "2026-09-08T00:00:00Z", "isOverdue": true, "daysOverdue": 7.0},
			"Balance owed: $50 · rent due Sep 8, 2026 · 7 days overdue",
		},
		{
			"a held deposit appends its own inclusion clause, last",
			map[string]interface{}{"balanceCents": 5000.0, "depositHeldCents": 150000.0},
			"Balance owed: $50 · incl. security deposit $1500",
		},
		{
			"a returned (zero-held) deposit adds no inclusion clause",
			map[string]interface{}{"balanceCents": 5000.0, "depositHeldCents": 0.0},
			"Balance owed: $50",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := callRentBalanceLine(t, vm, tc.data); got != tc.want {
				t.Errorf("rentBalanceLine(%v) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}

// TestRentAgeText_EveryBranch pins the portfolio row's age fragment: empty
// for a row with no due date at all, singular/plural overdue, singular/
// plural due-in, and due-today.
func TestRentAgeText_EveryBranch(t *testing.T) {
	vm := rentArrearsUIVM(t)
	for _, tc := range []struct {
		name string
		row  map[string]interface{}
		want string
	}{
		{"no due date at all", map[string]interface{}{"balanceCents": 5000.0}, ""},
		{"overdue, singular day", map[string]interface{}{"dueDate": "2026-09-08T00:00:00Z", "isOverdue": true, "daysOverdue": 1.0}, "1 day overdue"},
		{"overdue, plural days", map[string]interface{}{"dueDate": "2026-09-08T00:00:00Z", "isOverdue": true, "daysOverdue": 7.0}, "7 days overdue"},
		{"due in, singular day", map[string]interface{}{"dueDate": "2026-09-16T00:00:00Z", "isOverdue": false, "daysUntilDue": 1.0}, "due in 1 day"},
		{"due in, plural days", map[string]interface{}{"dueDate": "2026-09-22T00:00:00Z", "isOverdue": false, "daysUntilDue": 7.0}, "due in 7 days"},
		{"due today", map[string]interface{}{"dueDate": "2026-09-15T00:00:00Z", "isOverdue": false, "daysUntilDue": 0.0}, "due today"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := callRentAgeText(t, vm, tc.row); got != tc.want {
				t.Errorf("rentAgeText(%v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}

// TestDepositLine_HeldReturnedNone pins depositLine's three branches — held
// (the charged figure, "held since" the charge date), returned (the SAME
// charged figure, not the net-zero held amount, "returned" the credit
// date), and none at all (empty string — a deposit-less unit, or a lease
// whose deposit has not yet billed) — against the ledger.go
// computeDepositSummary shapes /api/ledger and /api/one-bill actually send.
func TestDepositLine_HeldReturnedNone(t *testing.T) {
	vm := rentArrearsUIVM(t)
	for _, tc := range []struct {
		name string
		data map[string]interface{}
		want string
	}{
		{
			"held",
			map[string]interface{}{"depositHeldCents": 150000.0, "depositChargedCents": 150000.0, "depositChargedAt": "2026-06-01T00:00:00Z"},
			"Security deposit $1500 · held since Jun 1, 2026",
		},
		{
			"returned",
			map[string]interface{}{"depositHeldCents": 0.0, "depositChargedCents": 150000.0, "depositChargedAt": "2026-06-01T00:00:00Z", "depositReturnedAt": "2027-06-01T00:00:00Z"},
			"Security deposit $1500 · returned Jun 1, 2027",
		},
		{
			"no deposit row at all",
			map[string]interface{}{"balanceCents": 230000.0},
			"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := callDepositLine(t, vm, tc.data); got != tc.want {
				t.Errorf("depositLine(%v) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}

// TestListingDepositLine_PresentAbsent pins the browse-card deposit line: a
// listing carrying a depositAmount renders it in the same currency shape
// money() uses, and a listing with none (the absent-field "no deposit"
// state) renders "" so the caller skips the element entirely.
func TestListingDepositLine_PresentAbsent(t *testing.T) {
	vm := rentArrearsUIVM(t)
	for _, tc := range []struct {
		name    string
		listing map[string]interface{}
		want    string
	}{
		{"present, USD", map[string]interface{}{"depositAmount": 1500.0, "rentCurrency": "USD"}, "Security deposit $1500"},
		{"present, non-USD", map[string]interface{}{"depositAmount": 1500.0, "rentCurrency": "CAD"}, "Security deposit 1500 CAD"},
		{"absent", map[string]interface{}{"rentAmount": 2400.0, "rentCurrency": "USD"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := callListingDepositLine(t, vm, tc.listing); got != tc.want {
				t.Errorf("listingDepositLine(%v) = %q, want %q", tc.listing, got, tc.want)
			}
		})
	}
}

// TestRentArrearsUI_DoesNotShiftWestOfGreenwich is the positive vector for
// the harness's zone (lease_term_ui_test.go's own
// TestFmtUTCDate_DoesNotShiftWestOfGreenwich pattern): under
// America/Los_Angeles, a dueDate of 2026-09-08T00:00:00Z must still render
// "Sep 8, 2026" — the recorded UTC calendar day — through both
// rentBalanceLine and rentAgeText, never "Sep 7, 2026" (the local-parse
// day-before a locale Date read would produce). This is the exact instant
// the fire brief's "Priya's $2,400 due 09-08" example names.
func TestRentArrearsUI_DoesNotShiftWestOfGreenwich(t *testing.T) {
	vm := rentArrearsUIVM(t)
	data := map[string]interface{}{
		"balanceCents": 240000.0, "dueDate": "2026-09-08T00:00:00Z", "isOverdue": true, "daysOverdue": 7.0,
	}
	if got, want := callRentBalanceLine(t, vm, data), "Balance owed: $2400 · rent due Sep 8, 2026 · 7 days overdue"; got != want {
		t.Errorf("rentBalanceLine under America/Los_Angeles = %q, want %q (the recorded UTC day, not the day before)", got, want)
	}
	if got, want := callRentAgeText(t, vm, data), "7 days overdue"; got != want {
		t.Errorf("rentAgeText under America/Los_Angeles = %q, want %q", got, want)
	}
	// fmtUTCDate itself, isolated, on the exact stamp named above.
	fn, ok := goja.AssertFunction(vm.Get("fmtUTCDate"))
	if !ok {
		t.Fatal("fmtUTCDate is not a function after evaluating its declaration")
	}
	res, err := fn(goja.Undefined(), vm.ToValue("2026-09-08T00:00:00Z"))
	if err != nil {
		t.Fatalf("fmtUTCDate threw: %v", err)
	}
	if got := res.String(); got != "Sep 8, 2026" {
		t.Fatalf("fmtUTCDate(2026-09-08T00:00:00Z) under America/Los_Angeles = %q, want Sep 8, 2026", got)
	}
}

// TestRentArrearsUI_ReachesDOMViaTextContentOnly is a grep-style pin (this
// app carries no prior markup-escaping self-test to extend, per
// lint-markup-escaping.go's own scope — innerHTML/outerHTML sinks only, and
// rentBalanceLine/rentAgeText never touch either): every call site that
// renders one of these two functions' output assigns it to .textContent,
// and neither function name ever appears on the right-hand side of an
// innerHTML/outerHTML assignment anywhere in the shipped file. Both
// functions return plain strings built from a landlord/tenant-entered
// figure that has already passed through moneyAmount/fmtUTCDate (numeric-
// safe formatters, lint-markup-escaping.go's own derivation rules), so
// nothing here can carry attacker-controlled markup — but the DOM sink
// discipline is what makes that true, and this pins it against a future
// call site regressing to innerHTML.
func TestRentArrearsUI_ReachesDOMViaTextContentOnly(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)

	for _, want := range []string{
		"balance.textContent = rentBalanceLine(data);",
		"age.textContent = ageText;",
		"deposit.textContent = depositText;",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("app.js: want the literal call site %q — a rendering path changed shape", want)
		}
	}

	unsafeSinks := regexp.MustCompile(`\.(?:innerHTML|outerHTML)\s*(?:\+?=)[^;\n]*\b(?:rentBalanceLine|rentAgeText|ageText|depositLine|depositText)\b`)
	if m := unsafeSinks.FindString(text); m != "" {
		t.Errorf("app.js: %q reaches an innerHTML/outerHTML sink — must be .textContent", m)
	}
}
