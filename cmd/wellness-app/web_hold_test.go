package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// jsString evaluates expr in vm and returns its string form — the shared
// idiom cafe-app's web_hold_test.go uses for its openTabGate/arrearsBadge
// fixture-driven table tests.
func jsString(t *testing.T, vm *goja.Runtime, expr string) string {
	t.Helper()
	v, err := vm.RunString(expr)
	if err != nil {
		t.Fatalf("eval %q: %v", expr, err)
	}
	return v.String()
}

// jsSessionStatusMap builds a real JS Map (scheduleCard reads
// myStatusBySession via .get(), not bracket access) with at most one entry —
// the only shape these tests need.
func jsSessionStatusMap(t *testing.T, vm *goja.Runtime, sessionKey, status string) goja.Value {
	t.Helper()
	v, err := vm.RunString(fmt.Sprintf(`new Map([[%q, %q]])`, sessionKey, status))
	if err != nil {
		t.Fatalf("build session-status Map: %v", err)
	}
	return v
}

// TestStatementLine_RendersOwedDueOverdueAndReminderSuffixes pins the
// member's own statement line (cafe-app's web_escape_test.go pin shape,
// TestStatementLineAndFrontDeskArrearsLine_RenderReminderSuffix) against the
// shipped source: the owed/credit/paid-in-full split stays exactly
// ledgerBalanceLine's, and — once something is owed — the due date, the
// overdue count (singular at exactly one day), and the reminder clause each
// layer on independently. The reminder clause is proven to stand on its own,
// not gated on isOverdue: a reminded balance sitting back inside its term
// still says so, because CreditHold binds on reminderSentAt alone.
func TestStatementLine_RendersOwedDueOverdueAndReminderSuffixes(t *testing.T) {
	vm := webHelperVM(t, "money", "statementLine")
	fn, ok := goja.AssertFunction(vm.Get("statementLine"))
	if !ok {
		t.Fatal("statementLine is not a function after evaluating its declaration")
	}

	call := func(t *testing.T, data map[string]any) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(data))
		if err != nil {
			t.Fatalf("statementLine threw: %v", err)
		}
		return res.String()
	}

	for _, tc := range []struct {
		name string
		data map[string]any
		want string
	}{
		{"credit balance", map[string]any{"balanceCents": -1200}, "Credit balance: $12.00"},
		{"paid in full", map[string]any{"balanceCents": 0}, "Balance: $0.00 (paid in full)"},
		{"owed, no due date yet", map[string]any{"balanceCents": 4750}, "Balance owed: $47.50"},
		{
			"owed, due but not overdue",
			map[string]any{"balanceCents": 4750, "dueDate": "2026-09-20T00:00:00Z"},
			"Balance owed: $47.50 · due 2026-09-20",
		},
		{
			"owed, overdue, plural days",
			map[string]any{"balanceCents": 4750, "dueDate": "2026-08-14T00:00:00Z", "isOverdue": true, "daysOverdue": 31},
			"Balance owed: $47.50 · due 2026-08-14 · 31 days overdue",
		},
		{
			"owed, overdue, singular day",
			map[string]any{"balanceCents": 450, "dueDate": "2026-09-13T00:00:00Z", "isOverdue": true, "daysOverdue": 1},
			"Balance owed: $4.50 · due 2026-09-13 · 1 day overdue",
		},
		{
			"owed, overdue, reminded — the full chain",
			map[string]any{
				"balanceCents": 4750, "dueDate": "2026-08-14T00:00:00Z", "isOverdue": true, "daysOverdue": 31,
				"reminderSentAt": "2026-08-15T03:00:00Z",
			},
			"Balance owed: $47.50 · due 2026-08-14 · 31 days overdue · a reminder was sent 2026-08-15 — booking is on hold; paying it off lifts the hold once the next arrears check runs (usually within a minute)",
		},
		{
			"owed, reminded but back inside its term — the reminder clause stands alone",
			map[string]any{
				"balanceCents": 975, "dueDate": "2026-09-20T00:00:00Z", "isOverdue": false,
				"reminderSentAt": "2026-08-15T03:00:00Z",
			},
			"Balance owed: $9.75 · due 2026-09-20 · a reminder was sent 2026-08-15 — booking is on hold; paying it off lifts the hold once the next arrears check runs (usually within a minute)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(t, tc.data); got != tc.want {
				t.Errorf("statementLine(%v) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}

// TestStatementLine_NeverPromisesImmediateRelief proves the reminder clause
// never claims the hold lifts the instant the balance is paid — the
// recorded .arrears.sentAt is carried forward (stale) by post_entry until
// the NEXT EvaluateWellnessArrears evaluation clears it, a dispatch-latency
// window (packages/wellness-ledger/scripts.go). "immediately" would be a
// wrong promise; the copy must name the mechanism (a re-check) instead.
func TestStatementLine_NeverPromisesImmediateRelief(t *testing.T) {
	vm := webHelperVM(t, "money", "statementLine")
	fn, ok := goja.AssertFunction(vm.Get("statementLine"))
	if !ok {
		t.Fatal("statementLine is not a function after evaluating its declaration")
	}
	res, err := fn(goja.Undefined(), vm.ToValue(map[string]any{
		"balanceCents": 4750, "dueDate": "2026-08-14T00:00:00Z", "isOverdue": true, "daysOverdue": 31,
		"reminderSentAt": "2026-08-15T03:00:00Z",
	}))
	if err != nil {
		t.Fatalf("statementLine threw: %v", err)
	}
	got := res.String()
	if strings.Contains(strings.ToLower(got), "immediately") {
		t.Errorf("statementLine(reminded) = %q, must not promise the hold lifts immediately — the recorded reminder is stale until the next evaluation", got)
	}
	if !strings.Contains(got, "next arrears check") {
		t.Errorf("statementLine(reminded) = %q, want it to name the re-check mechanism", got)
	}
}

// TestBookingGate_HoldConfirmOpenPrecedence pins the three-way rule
// CreateBooking/JoinWaitlist's CreditHold refusal mirrors — mirrors café's
// TestOpenTabGate_HoldOnReminderConfirmOnOverdue (cmd/cafe-app/
// web_hold_test.go): a reminded episode is a hold whatever isOverdue says
// (the op refuses CreditHold on reminderSentAt alone), an overdue balance
// nobody has reminded yet is a confirm, and everything else — no row, in
// term, in credit — opens. Hold takes precedence over confirm whenever both
// fields are set together (the common case: overdue AND reminded).
func TestBookingGate_HoldConfirmOpenPrecedence(t *testing.T) {
	vm := webHelperVM(t, "bookingGate")
	if _, err := vm.RunString(`
const none = undefined;
const overdueUnreminded = { balanceCents: 4750, isOverdue: true, daysOverdue: 31 };
const overdueReminded = { balanceCents: 4750, isOverdue: true, daysOverdue: 31, reminderSentAt: "2026-08-15T03:00:00Z" };
const inTermReminded = { balanceCents: 975, isOverdue: false, reminderSentAt: "2026-08-15T03:00:00Z" };
const inTerm = { balanceCents: 975, isOverdue: false };
const credit = { balanceCents: -1200, isOverdue: false };
`); err != nil {
		t.Fatalf("fixture eval: %v", err)
	}

	for expr, want := range map[string]string{
		"bookingGate(none)":              "open",
		"bookingGate(null)":              "open",
		"bookingGate(overdueUnreminded)": "confirm",
		"bookingGate(overdueReminded)":   "hold",
		"bookingGate(inTermReminded)":    "hold",
		"bookingGate(inTerm)":            "open",
		"bookingGate(credit)":            "open",
	} {
		if got := jsString(t, vm, expr); got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}

// TestScheduleCard_HeldReplacesBookAndJoinWaitlistWithANote proves the
// schedule card offers no button at all under a hold — mirrors café's own
// openTabGate-driven panel swap — for both the plain "Book" case and the
// full-class "Join waitlist" case, and that the note carries the balance. A
// pre-existing claim (already booked) is untouched by the hold: promotion of
// a claim that predates the hold is not a leg CreditHold gates.
func TestScheduleCard_HeldReplacesBookAndJoinWaitlistWithANote(t *testing.T) {
	vm := webHelperVM(t, "esc", "money", "shortKey", "fmtTime", "fmtDay", "domId", "priceLabel", "cardPriceCents", "cardPriceLabel", "seriesCountKey", "scheduleCard")
	fn, ok := goja.AssertFunction(vm.Get("scheduleCard"))
	if !ok {
		t.Fatal("scheduleCard is not a function after evaluating its declaration")
	}

	const future = "2026-09-20T18:00:00Z"
	ledger := map[string]any{"balanceCents": 4750, "reminderSentAt": "2026-08-15T03:00:00Z"}
	openSession := map[string]any{
		"sessionKey": "vtx.wellnesssession.aaaaaaaaaaaaaaaaaaaa", "startsAt": future, "endsAt": future,
		"bookedCount": 1.0, "capacity": 10.0, "name": "Vinyasa Flow", "priceCents": 1500.0,
	}
	fullSession := map[string]any{
		"sessionKey": "vtx.wellnesssession.bbbbbbbbbbbbbbbbbbbb", "startsAt": future, "endsAt": future,
		"bookedCount": 10.0, "capacity": 10.0, "name": "Vinyasa Flow", "priceCents": 1500.0,
	}

	call := func(t *testing.T, se map[string]any, myStatus goja.Value, gate string, ledger map[string]any) string {
		t.Helper()
		if myStatus == nil {
			myStatus = goja.Undefined()
		}
		ledgerArg := goja.Value(goja.Undefined())
		if ledger != nil {
			ledgerArg = vm.ToValue(ledger)
		}
		res, err := fn(goja.Undefined(), vm.ToValue(se), myStatus, goja.Undefined(), vm.ToValue(true), vm.ToValue(gate), ledgerArg)
		if err != nil {
			t.Fatalf("scheduleCard threw: %v", err)
		}
		return res.String()
	}

	t.Run("held, open seats — no Book button, note present", func(t *testing.T) {
		got := call(t, openSession, nil, "hold", ledger)
		if strings.Contains(got, "<button") {
			t.Errorf("scheduleCard(open session, held) = %q, want no <button> at all", got)
		}
		if !strings.Contains(got, "On hold") || !strings.Contains(got, "$47.50") {
			t.Errorf("scheduleCard(open session, held) = %q, want an on-hold note naming the balance", got)
		}
	})

	t.Run("held, full class — no Join waitlist button either", func(t *testing.T) {
		got := call(t, fullSession, nil, "hold", ledger)
		if strings.Contains(got, "<button") || strings.Contains(got, "Join waitlist") {
			t.Errorf("scheduleCard(full session, held) = %q, want no button — a hold blocks the waitlist leg too", got)
		}
	})

	t.Run("not held — the ordinary Book button renders", func(t *testing.T) {
		got := call(t, openSession, nil, "open", nil)
		if !strings.Contains(got, `<button id="book-`) || !strings.Contains(got, ">Book<") {
			t.Errorf("scheduleCard(open session, open gate) = %q, want the ordinary Book button", got)
		}
	})

	t.Run("already booked — a hold never touches a claim already held", func(t *testing.T) {
		myStatus := jsSessionStatusMap(t, vm, openSession["sessionKey"].(string), "booked")
		got := call(t, openSession, myStatus, "hold", ledger)
		if !strings.Contains(got, ">Booked<") {
			t.Errorf("scheduleCard(already booked, held) = %q, want the existing Booked label, untouched by the hold", got)
		}
	})
}

// TestBookHoldNote_RendersOwedReminderDateAndMechanism pins the front
// desk's picker on-hold note (bookMemberIn's CreditHold declaration) —
// mirrors café's renderCreditHoldPanel: who, what they owe, whether they're
// overdue, and the reminder date rendered from the same UTC calendar-day
// slice the op's own refusal text uses. It never promises the hold lifts
// the instant the balance is paid.
func TestBookHoldNote_RendersOwedReminderDateAndMechanism(t *testing.T) {
	vm := webHelperVM(t, "esc", "money", "bookHoldNote")
	fn, ok := goja.AssertFunction(vm.Get("bookHoldNote"))
	if !ok {
		t.Fatal("bookHoldNote is not a function after evaluating its declaration")
	}

	res, err := fn(goja.Undefined(), vm.ToValue("Alex Kim"), vm.ToValue(map[string]any{
		"balanceCents": 2000, "isOverdue": true, "daysOverdue": 3, "reminderSentAt": "2026-08-15T03:00:00Z",
	}))
	if err != nil {
		t.Fatalf("bookHoldNote threw: %v", err)
	}
	got := res.String()
	for _, want := range []string{"Alex Kim", "$20.00", "3 days overdue", "2026-08-15"} {
		if !strings.Contains(got, want) {
			t.Errorf("bookHoldNote(...) = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(strings.ToLower(got), "immediately") {
		t.Errorf("bookHoldNote(...) = %q, must not promise the hold lifts immediately", got)
	}
}

// TestArrearsLine_OverdueGainsTheReminderSuffix pins the desk arrears
// grid's overdue banner gaining the reminder clause — "no reminder sent
// yet" for an overdue-but-unreminded row (the desk's existing confirm
// case), and the reminder's own UTC calendar-day slice for a reminded one.
func TestArrearsLine_OverdueGainsTheReminderSuffix(t *testing.T) {
	vm := webHelperVM(t, "esc", "arrearsLine")
	fn, ok := goja.AssertFunction(vm.Get("arrearsLine"))
	if !ok {
		t.Fatal("arrearsLine is not a function after evaluating its declaration")
	}

	call := func(t *testing.T, row map[string]any) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(row))
		if err != nil {
			t.Fatalf("arrearsLine threw: %v", err)
		}
		return res.String()
	}

	unsent := call(t, map[string]any{"dueDate": "2026-08-14T00:00:00Z", "isOverdue": true, "daysOverdue": 5})
	if !strings.Contains(unsent, "no reminder sent yet") {
		t.Errorf("arrearsLine(overdue, no reminderSentAt) = %q, want it to say no reminder sent yet", unsent)
	}

	sent := call(t, map[string]any{
		"dueDate": "2026-08-14T00:00:00Z", "isOverdue": true, "daysOverdue": 5, "reminderSentAt": "2026-08-15T03:00:00Z",
	})
	if !strings.Contains(sent, "reminder sent 2026-08-15") {
		t.Errorf("arrearsLine(overdue, reminderSentAt) = %q, want it to render the row's own reminderSentAt (UTC slice)", sent)
	}

	notOverdue := call(t, map[string]any{"dueDate": "2026-09-20T00:00:00Z", "isOverdue": false})
	if strings.Contains(notOverdue, "reminder") {
		t.Errorf("arrearsLine(not overdue) = %q, want no reminder text at all", notOverdue)
	}
}
