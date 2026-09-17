package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// rentArrearsUIDecls lifts the shipped UTC_MONTH_ABBR/fmtUTCDate/moneyAmount/
// rentBalanceLine/rentAgeText/depositLine/listingDepositLine/
// entrySignAndClass/groupOneBillEntriesByPeriod declarations out of the
// embedded app.js — the rotate_offer_test.go / renewal_ready_test.go /
// lease_term_ui_test.go pattern: the REAL shipped source runs here, not a
// copy, so these pins are a statement about what ships. All nine are
// self-contained (no DOM/state), so goja can evaluate them directly.
var rentArrearsUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nconst UTC_MONTH_ABBR = \[.*?\];\n`),
	regexp.MustCompile(`(?s)\nfunction fmtUTCDate\(s\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction moneyAmount\(n\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction rentBalanceLine\(data\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction rentAgeText\(row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction depositLine\(data\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction listingDepositLine\(listing\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction entrySignAndClass\(t\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction groupOneBillEntriesByPeriod\(entries\) \{\n.*?\n\}\n`),
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

func callEntrySignAndClass(t *testing.T, vm *goja.Runtime, entry map[string]interface{}) (sign, cls string) {
	t.Helper()
	fn, ok := goja.AssertFunction(vm.Get("entrySignAndClass"))
	if !ok {
		t.Fatal("entrySignAndClass is not a function after evaluating its declaration")
	}
	res, err := fn(goja.Undefined(), vm.ToValue(entry))
	if err != nil {
		t.Fatalf("entrySignAndClass(%v) threw: %v", entry, err)
	}
	obj := res.ToObject(vm)
	return obj.Get("sign").String(), obj.Get("cls").String()
}

func callGroupOneBillEntriesByPeriod(t *testing.T, vm *goja.Runtime, entries []map[string]interface{}) []map[string]interface{} {
	t.Helper()
	fn, ok := goja.AssertFunction(vm.Get("groupOneBillEntriesByPeriod"))
	if !ok {
		t.Fatal("groupOneBillEntriesByPeriod is not a function after evaluating its declaration")
	}
	res, err := fn(goja.Undefined(), vm.ToValue(entries))
	if err != nil {
		t.Fatalf("groupOneBillEntriesByPeriod(%v) threw: %v", entries, err)
	}
	var groups []map[string]interface{}
	if err := vm.ExportTo(res, &groups); err != nil {
		t.Fatalf("export groupOneBillEntriesByPeriod result: %v", err)
	}
	return groups
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
			// depositHeldCents is CUSTODY (charged minus returned), never an
			// unpaid figure — a $1500 deposit held against a $50 open balance
			// means at most $50 of the OPEN balance could be the deposit
			// (min(balanceCents, depositHeldCents)), never the full $1500: the
			// rest of the deposit was already retired by an earlier payment.
			"a deposit larger than the open balance is capped BY the balance",
			map[string]interface{}{"balanceCents": 5000.0, "depositHeldCents": 150000.0},
			"Balance owed: $50 · of which up to $50 is the security deposit",
		},
		{
			// The fire brief's own partial-payment vector: $1500 charged, $1000
			// paid down, $500 still open — the suffix must read "up to $500",
			// never the full $1500 the deposit was charged at.
			"partial payment: the open balance is smaller than the deposit",
			map[string]interface{}{"balanceCents": 50000.0, "depositHeldCents": 150000.0},
			"Balance owed: $500 · of which up to $500 is the security deposit",
		},
		{
			"the open balance exceeds the deposit — capped BY the deposit instead",
			map[string]interface{}{"balanceCents": 300000.0, "depositHeldCents": 150000.0},
			"Balance owed: $3000 · of which up to $1500 is the security deposit",
		},
		{
			"a returned (zero-held) deposit adds no suffix at all",
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

// TestDepositLine_HeldReturnedNone pins depositLine's branches — held (the
// charged figure, "held since" the charge date), held-with-a-deduction ("·
// $X deducted" inserted before any return clause), returned (the NET figure
// — charged less any deduction — "returned" the credit date), fully
// deducted with no return row yet ("· fully deducted, nothing to return",
// the zero-net ReturnDeposit shape that mints no transaction to read a
// returnedAt off), and none at all (empty string — a deposit-less unit, or a
// lease whose deposit has not yet billed) — against the ledger.go
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
			"held with a deduction",
			map[string]interface{}{"depositHeldCents": 100000.0, "depositChargedCents": 150000.0, "depositChargedAt": "2026-06-01T00:00:00Z", "depositDeductedCents": 50000.0},
			"Security deposit $1500 · held since Jun 1, 2026 · $500 deducted",
		},
		{
			"returned, no deduction",
			map[string]interface{}{"depositHeldCents": 0.0, "depositChargedCents": 150000.0, "depositChargedAt": "2026-06-01T00:00:00Z", "depositReturnedAt": "2027-06-01T00:00:00Z"},
			"Security deposit $1500 · held since Jun 1, 2026 · $1500 returned Jun 1, 2027",
		},
		{
			"returned net of a deduction",
			map[string]interface{}{"depositHeldCents": 0.0, "depositChargedCents": 150000.0, "depositChargedAt": "2026-06-01T00:00:00Z", "depositDeductedCents": 50000.0, "depositReturnedAt": "2027-06-01T00:00:00Z"},
			"Security deposit $1500 · held since Jun 1, 2026 · $500 deducted · $1000 returned Jun 1, 2027",
		},
		{
			"fully deducted, not yet returned (or a zero-net return that minted no transaction)",
			map[string]interface{}{"depositHeldCents": 0.0, "depositChargedCents": 150000.0, "depositChargedAt": "2026-06-01T00:00:00Z", "depositDeductedCents": 150000.0},
			"Security deposit $1500 · held since Jun 1, 2026 · $1500 deducted · fully deducted, nothing to return",
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

// TestRenderUnitCard_RentAndDepositAreCurrencyAware is a grep-style pin
// (renderUnitCard builds real DOM elements — document.createElement — so it
// cannot run headless in goja the way the pure formatters above do; the
// source text itself is what ships). It isolates the function body and
// checks BOTH money figures — the rent span and the deposit span — go
// through fmtMoney(amount, currency), never the USD-only moneyAmount, so a
// non-USD unit's rent and deposit both render in the listing's own
// rentCurrency instead of a misleading bare "$" figure.
func TestRenderUnitCard_RentAndDepositAreCurrencyAware(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)

	fnRe := regexp.MustCompile(`(?s)\nfunction renderUnitCard\(u\) \{\n.*?\n\}\n`)
	body := fnRe.FindString(text)
	if body == "" {
		t.Fatal("app.js: no top-level renderUnitCard(u) declaration found — the extraction regex no longer matches this file")
	}

	for _, want := range []string{
		"fmtMoney(u.unitRent, currency)",
		"fmtMoney(u.listing.depositAmount, currency)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("renderUnitCard: want the currency-aware call %q, not found", want)
		}
	}
	if strings.Contains(body, "moneyAmount(u.unitRent)") {
		t.Error("renderUnitCard: the rent span still calls the USD-only moneyAmount(u.unitRent)")
	}
	if strings.Contains(body, "moneyAmount(u.listing.depositAmount)") {
		t.Error("renderUnitCard: the deposit span still calls the USD-only moneyAmount(u.listing.depositAmount)")
	}
}

// TestRenderLedgerRecordForm_DeductAndPayoutGates is a grep-style pin
// (renderLedgerRecordForm builds real DOM elements, so it cannot run
// headless in goja the way the pure formatters above do; the source text
// itself is what ships — the renderUnitCard precedent above). It checks
// that "Deduct from deposit" is offered only while depositHeldCents > 0 AND
// depositClauseKey is known (RecordDepositDeduction's own custody proof
// needs the clause key the form supplies), and "Pay out" only while a
// credit balance is owed (balanceCents < 0) AND the tenancy has ended
// (tenancyEndedAt) — PayOutBalance's own TenancyNotEnded/NoCreditBalance
// refusals — so the reachable-code census in each op's opmetas.go
// refusal-courtesy(hide) declaration stays true. Both controls use native
// confirm() before submitting, matching the withdraw/detach/remove-photo
// precedent for an irreversible financial action.
func TestRenderLedgerRecordForm_DeductAndPayoutGates(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)

	fnRe := regexp.MustCompile(`(?s)\nfunction renderLedgerRecordForm\(leaseAppKey, accountKey, body, canRecord, data, tenancyEndedAt\) \{\n.*?\n\}\n`)
	body := fnRe.FindString(text)
	if body == "" {
		t.Fatal("app.js: no top-level renderLedgerRecordForm(leaseAppKey, accountKey, body, canRecord, data, tenancyEndedAt) declaration found — the extraction regex no longer matches this file")
	}

	for _, want := range []string{
		`data && Number(data.depositHeldCents) > 0 && data.depositClauseKey`,
		`operationType: "RecordDepositDeduction"`,
		`data && Number(data.balanceCents) < 0 && tenancyEndedAt`,
		`operationType: "PayOutBalance"`,
		`if (cents > Number(data.depositHeldCents)) {`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("renderLedgerRecordForm: want %q, not found", want)
		}
	}
	if n := strings.Count(body, "confirm("); n < 2 {
		t.Errorf("renderLedgerRecordForm: want a native confirm() before both the deduction and the payout submit, found %d confirm( call(s)", n)
	}
}

// TestRenderLedgerRecordForm_DeductionCapEnforcedInClickHandler pins the
// RecordDepositDeduction/DeductionExceedsDeposit refusal-courtesy's actual
// mechanism: deductAmount.max is a bare <input> attribute outside a <form>,
// which the DOM never enforces on its own (no submit event validates it), so
// the real cap has to be the click handler's own comparison against
// data.depositHeldCents, checked and toasted BEFORE the native confirm() —
// never relying on the input's max alone.
func TestRenderLedgerRecordForm_DeductionCapEnforcedInClickHandler(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)

	fnRe := regexp.MustCompile(`(?s)\nfunction renderLedgerRecordForm\(leaseAppKey, accountKey, body, canRecord, data, tenancyEndedAt\) \{\n.*?\n\}\n`)
	body := fnRe.FindString(text)
	if body == "" {
		t.Fatal("app.js: no top-level renderLedgerRecordForm(leaseAppKey, accountKey, body, canRecord, data, tenancyEndedAt) declaration found — the extraction regex no longer matches this file")
	}

	capIdx := strings.Index(body, "if (cents > Number(data.depositHeldCents)) {")
	if capIdx < 0 {
		t.Fatal(`renderLedgerRecordForm: want "if (cents > Number(data.depositHeldCents)) {", not found`)
	}
	toastIdx := strings.Index(body[capIdx:], `toast("The deduction cannot exceed the `)
	if toastIdx < 0 || toastIdx > 200 {
		t.Error(`renderLedgerRecordForm: the deposit-cap check's body must toast "The deduction cannot exceed the ..." immediately`)
	}
	confirmIdx := strings.Index(body, `confirm("Deduct " + moneyAmount(dollars)`)
	if confirmIdx < 0 {
		t.Fatal("renderLedgerRecordForm: no deduction confirm() call found")
	}
	if confirmIdx < capIdx {
		t.Error("renderLedgerRecordForm: the deposit-cap check must run BEFORE the native confirm(), so an over-cap deduction never reaches the confirmation dialog")
	}
}

// TestEntrySignAndClass_DeductionGetsNoSignNoClass pins entrySignAndClass —
// the shared helper the landlord ledger, the tenant statement and the
// combined one-bill statement all call to render a transaction row — proving
// a "deduction" row (custody moved off an already-paid deposit, never money
// charged or paid) renders with NEITHER a +/− sign NOR a debit/credit CSS
// class, unlike an ordinary debit or credit.
func TestEntrySignAndClass_DeductionGetsNoSignNoClass(t *testing.T) {
	vm := rentArrearsUIVM(t)

	sign, cls := callEntrySignAndClass(t, vm, map[string]interface{}{"type": "deduction", "amountCents": 5000})
	if sign != "" || cls != "" {
		t.Errorf("entrySignAndClass(deduction) = (%q, %q), want (\"\", \"\") — a deduction moves custody, it is not a payment", sign, cls)
	}

	if sign, cls := callEntrySignAndClass(t, vm, map[string]interface{}{"type": "debit", "amountCents": 5000}); sign != "+" || cls != "debit" {
		t.Errorf("entrySignAndClass(debit) = (%q, %q), want (\"+\", \"debit\")", sign, cls)
	}
	if sign, cls := callEntrySignAndClass(t, vm, map[string]interface{}{"type": "credit", "amountCents": 5000}); sign != "−" || cls != "credit" {
		t.Errorf("entrySignAndClass(credit) = (%q, %q), want (\"−\", \"credit\")", sign, cls)
	}
}

// TestGroupOneBillEntriesByPeriod_DeductionDoesNotMoveNet pins the one-bill
// statement's per-period net computation: a "deduction" row sitting in the
// same calendar month as a debit and a credit must not move netCents in
// either direction (Decision 1 — every balance reader ignores a deduction —
// mirrored client-side). Removing the type-guard and falling through to an
// else-branch that folds a deduction in as a credit (or an if that folds it
// in as a debit) would move this number; this test fails either way.
func TestGroupOneBillEntriesByPeriod_DeductionDoesNotMoveNet(t *testing.T) {
	vm := rentArrearsUIVM(t)

	entries := []map[string]interface{}{
		{"type": "debit", "amountCents": 100000, "postedAt": "2027-03-10T12:00:00Z"},
		{"type": "credit", "amountCents": 30000, "postedAt": "2027-03-15T12:00:00Z"},
		{"type": "deduction", "amountCents": 999999999, "postedAt": "2027-03-20T12:00:00Z"},
	}
	groups := callGroupOneBillEntriesByPeriod(t, vm, entries)
	if len(groups) != 1 {
		t.Fatalf("groupOneBillEntriesByPeriod: got %d period group(s), want 1 (all three entries fall in March 2027)", len(groups))
	}
	net, _ := groups[0]["netCents"].(int64)
	if net == 0 {
		if netF, ok := groups[0]["netCents"].(float64); ok {
			net = int64(netF)
		}
	}
	if want := int64(70000); net != want {
		t.Errorf("groupOneBillEntriesByPeriod: netCents = %v, want %v (100000 debit − 30000 credit; the 999999999 deduction must not move it)", net, want)
	}
	if entryCount := len(groups[0]["entries"].([]interface{})); entryCount != 3 {
		t.Errorf("groupOneBillEntriesByPeriod: period carries %d entries, want 3 — the deduction is still LISTED, only excluded from netCents", entryCount)
	}
}
