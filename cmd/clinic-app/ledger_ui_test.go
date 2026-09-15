package main

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// ledgerUIDecls lifts the shipped moneyAmount/customerMemo/shortKey/localDate/
// arrearsBadgeText/overdueBookingPrompt/ledgerLineLabel/visitPickerOptions/
// openChargeOptions/defaultWaiveTarget declarations out of the embedded
// app.js — the followup_addressed_test.go / lease_term_ui_test.go pattern:
// the REAL shipped source runs here, not a copy, so these pins are a
// statement about what ships. moneyAmount/customerMemo/shortKey/localDate
// are dependencies the other seven call; all are self-contained (no
// DOM/state), so goja can evaluate them directly.
var ledgerUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction moneyAmount\(cents\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction customerMemo\(memo\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction shortKey\(key\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction localDate\(instant\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction arrearsBadgeText\(row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction overdueBookingPrompt\(name, row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction ledgerLineLabel\(t, byKey\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction visitPickerOptions\(appts\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction openChargeOptions\(transactions\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction defaultWaiveTarget\(options\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction selfPayCapMessage\(owedCents, cents\) \{\n.*?\n\}\n`),
}

// ledgerUITestHarness is test scaffolding only (never extracted from app.js):
// __callLedgerLineLabel builds the real js Map ledgerLineLabel's byKey param
// expects (a plain Go map converted via vm.ToValue has no .get method, so
// ledgerLineLabel would treat it as byKey-less); __jsonVisitPickerOptions and
// __jsonOpenChargeOptions JSON-encode their array results so the Go side can
// unmarshal them instead of walking goja's Value API by hand.
const ledgerUITestHarness = `
function __callLedgerLineLabel(t, entries) {
  var m = new Map();
  for (var i = 0; i < entries.length; i++) m.set(entries[i][0], entries[i][1]);
  return ledgerLineLabel(t, m);
}
function __jsonVisitPickerOptions(appts) {
  return JSON.stringify(visitPickerOptions(appts));
}
function __jsonOpenChargeOptions(transactions) {
  return JSON.stringify(openChargeOptions(transactions));
}
`

// ledgerUIVM evaluates the declarations WEST OF GREENWICH: goja's Date reads
// Go's time.Local, so the process zone is pinned to America/Los_Angeles for
// the test's lifetime, mirroring lease_term_ui_test.go's leaseTermUIVM. Every
// instant fixture in this file is stamped at noon UTC specifically so a
// -7h/-8h local offset never crosses a calendar-day boundary — the pins below
// are about the annotation LOGIC (open/due/overdue/settled/reverses), not
// about re-deriving a day-boundary shift lease_term_ui_test.go already owns.
func ledgerUIVM(t *testing.T) *goja.Runtime {
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
	for _, re := range ledgerUIDecls {
		decl := re.FindString(string(src))
		if decl == "" {
			t.Fatalf("app.js: no top-level declaration matching %s — the extraction regex no longer matches this file", re)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped declaration %s: %v", re, err)
		}
	}
	if _, err := vm.RunString(ledgerUITestHarness); err != nil {
		t.Fatalf("goja eval of the test harness: %v", err)
	}
	return vm
}

func TestArrearsBadgeText(t *testing.T) {
	vm := ledgerUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("arrearsBadgeText"))
	if !ok {
		t.Fatal("arrearsBadgeText is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, row map[string]interface{}) string {
		t.Helper()
		arg := goja.Value(goja.Undefined())
		if row != nil {
			arg = vm.ToValue(row)
		}
		res, err := fn(goja.Undefined(), arg)
		if err != nil {
			t.Fatalf("arrearsBadgeText(%v) threw: %v", row, err)
		}
		return res.String()
	}
	for _, tc := range []struct {
		name string
		row  map[string]interface{}
		want string
	}{
		{"no row", nil, ""},
		{"zero balance", map[string]interface{}{"balanceCents": 0}, ""},
		{"negative balance (a credit, never a debtor)", map[string]interface{}{"balanceCents": -500}, ""},
		{"positive, not overdue", map[string]interface{}{"balanceCents": 2500}, "owes $25"},
		{"overdue, 1 day (singular)", map[string]interface{}{"balanceCents": 6000, "isOverdue": true, "daysOverdue": 1}, "owes $60 · 1 day overdue"},
		{"overdue, 3 days (plural)", map[string]interface{}{"balanceCents": 6000, "isOverdue": true, "daysOverdue": 3}, "owes $60 · 3 days overdue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.row); got != tc.want {
				t.Errorf("arrearsBadgeText(%v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}

func TestOverdueBookingPrompt(t *testing.T) {
	vm := ledgerUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("overdueBookingPrompt"))
	if !ok {
		t.Fatal("overdueBookingPrompt is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, name string, row map[string]interface{}) string {
		t.Helper()
		arg := goja.Value(goja.Undefined())
		if row != nil {
			arg = vm.ToValue(row)
		}
		res, err := fn(goja.Undefined(), vm.ToValue(name), arg)
		if err != nil {
			t.Fatalf("overdueBookingPrompt(%q, %v) threw: %v", name, row, err)
		}
		return res.String()
	}
	t.Run("no row", func(t *testing.T) {
		if got := run(t, "Riley Chen", nil); got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})
	t.Run("not overdue", func(t *testing.T) {
		if got := run(t, "Riley Chen", map[string]interface{}{"isOverdue": false, "balanceCents": 2500}); got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})
	t.Run("overdue, 3 days", func(t *testing.T) {
		want := "Riley Chen owes $60, 3 days overdue. Book them anyway?"
		if got := run(t, "Riley Chen", map[string]interface{}{"balanceCents": 6000, "isOverdue": true, "daysOverdue": 3}); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("overdue, 1 day (singular)", func(t *testing.T) {
		want := "Riley Chen owes $15, 1 day overdue. Book them anyway?"
		if got := run(t, "Riley Chen", map[string]interface{}{"balanceCents": 1500, "isOverdue": true, "daysOverdue": 1}); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestLedgerLineLabel(t *testing.T) {
	vm := ledgerUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("__callLedgerLineLabel"))
	if !ok {
		t.Fatal("__callLedgerLineLabel is not a function after evaluating the test harness")
	}
	run := func(t *testing.T, row map[string]interface{}, byKey [][2]interface{}) string {
		t.Helper()
		entries := make([]interface{}, len(byKey))
		for i, e := range byKey {
			entries[i] = []interface{}{e[0], e[1]}
		}
		res, err := fn(goja.Undefined(), vm.ToValue(row), vm.ToValue(entries))
		if err != nil {
			t.Fatalf("ledgerLineLabel(%v) threw: %v", row, err)
		}
		return res.String()
	}

	t.Run("debit, open and due (not overdue)", func(t *testing.T) {
		row := map[string]interface{}{
			"transactionKey": "tx-a", "type": "debit", "amountCents": 5000,
			"postedAt": "2026-09-01T12:00:00Z", "openCents": 5000, "dueAt": "2026-09-16T12:00:00Z",
		}
		want := "09/01/2026 · +$50 · open $50 · due 09/16/2026"
		if got := run(t, row, nil); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("debit, overdue", func(t *testing.T) {
		row := map[string]interface{}{
			"transactionKey": "tx-b", "type": "debit", "amountCents": 6000,
			"postedAt": "2026-08-01T12:00:00Z", "openCents": 6000, "dueAt": "2026-08-16T12:00:00Z",
			"isOverdue": true, "daysOverdue": 3,
		}
		want := "08/01/2026 · +$60 · open $60 · 3 days overdue"
		if got := run(t, row, nil); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("debit, fully settled (openCents 0 — paid, waived, or reversed alike)", func(t *testing.T) {
		row := map[string]interface{}{
			"transactionKey": "tx-c", "type": "debit", "amountCents": 5000,
			"postedAt": "2026-09-01T12:00:00Z", "openCents": 0,
		}
		want := "09/01/2026 · +$50 · settled"
		if got := run(t, row, nil); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("credit reversing a known charge names its memo and date", func(t *testing.T) {
		fee := map[string]interface{}{
			"transactionKey": "tx-fee", "type": "debit", "memo": "No-show fee",
			"postedAt": "2026-08-01T12:00:00Z", "amountCents": 6000,
		}
		credit := map[string]interface{}{
			"transactionKey": "tx-credit", "type": "credit", "amountCents": 6000,
			"postedAt": "2026-08-05T12:00:00Z", "reversesKey": "tx-fee",
		}
		want := "08/05/2026 · −$60 · reverses No-show fee of 08/01/2026"
		if got := run(t, credit, [][2]interface{}{{"tx-fee", fee}}); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("credit reversing an unresolvable key still says charge", func(t *testing.T) {
		credit := map[string]interface{}{
			"transactionKey": "tx-credit2", "type": "credit", "amountCents": 1000,
			"postedAt": "2026-09-01T12:00:00Z", "reversesKey": "nonexistent",
		}
		want := "09/01/2026 · −$10 · reverses charge"
		if got := run(t, credit, nil); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("a settles line keeps its (visit …) suffix", func(t *testing.T) {
		row := map[string]interface{}{
			"transactionKey": "tx-settle", "type": "debit", "amountCents": 6000,
			"postedAt": "2026-08-01T12:00:00Z", "memo": "No-show fee",
			"visitStartsAt": "2026-07-30T12:00:00Z", "settlesFee": true, "openCents": 0,
		}
		want := "08/01/2026 · +$60 — No-show fee (visit 07/30/2026) · settled"
		if got := run(t, row, nil); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

type pickerOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func TestVisitPickerOptions(t *testing.T) {
	vm := ledgerUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("__jsonVisitPickerOptions"))
	if !ok {
		t.Fatal("__jsonVisitPickerOptions is not a function after evaluating the test harness")
	}
	run := func(t *testing.T, appts []map[string]interface{}) []pickerOption {
		t.Helper()
		arr := make([]interface{}, len(appts))
		for i, a := range appts {
			arr[i] = a
		}
		res, err := fn(goja.Undefined(), vm.ToValue(arr))
		if err != nil {
			t.Fatalf("visitPickerOptions threw: %v", err)
		}
		var out []pickerOption
		if err := json.Unmarshal([]byte(res.String()), &out); err != nil {
			t.Fatalf("unmarshal visitPickerOptions result %q: %v", res.String(), err)
		}
		return out
	}
	visit := func(key, status, startsAt, provider string) map[string]interface{} {
		return map[string]interface{}{"appointmentKey": key, "status": status, "startsAt": startsAt, "providerName": provider}
	}

	t.Run("excludes cancelled/noShow only — a checked-in or future visit is offered", func(t *testing.T) {
		appts := []map[string]interface{}{
			visit("vtx.appointment.a1", "scheduled", "2026-09-10T12:00:00Z", "Dr. Osei"),
			visit("vtx.appointment.a2", "cancelled", "2026-09-11T12:00:00Z", "Dr. Osei"),
			visit("vtx.appointment.a3", "noShow", "2026-09-12T12:00:00Z", "Dr. Osei"),
			// checkedIn, not yet at its scheduled start — the primary copay
			// moment this picker exists for (S2): a clock filter on startsAt
			// would refuse exactly this visit.
			visit("vtx.appointment.a4", "checkedIn", "2026-09-20T12:00:00Z", "Dr. Osei"),
			visit("vtx.appointment.a5", "completed", "2026-09-13T12:00:00Z", "Dr. Osei"),
		}
		got := run(t, appts)
		if len(got) != 3 {
			t.Fatalf("got %d options, want 3 (%v)", len(got), got)
		}
		if got[0].Value != "vtx.appointment.a4" || got[1].Value != "vtx.appointment.a5" || got[2].Value != "vtx.appointment.a1" {
			t.Errorf("order/membership = %v, want [a4, a5, a1] (newest startsAt first, cancelled/noShow excluded)", got)
		}
	})

	t.Run("capped at 20, newest first", func(t *testing.T) {
		appts := make([]map[string]interface{}, 25)
		for i := 0; i < 25; i++ {
			day := 1 + i
			appts[i] = visit("vtx.appointment.v"+string(rune('a'+i)), "completed",
				dateAtDay(t, 2026, 1, day), "Dr. Osei")
		}
		got := run(t, appts)
		if len(got) != 20 {
			t.Fatalf("got %d options, want cap of 20", len(got))
		}
		if got[0].Value != "vtx.appointment.v"+string(rune('a'+24)) {
			t.Errorf("newest-first head = %v, want the day-25 visit", got[0])
		}
	})
}

func TestDefaultWaiveTarget(t *testing.T) {
	vm := ledgerUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("defaultWaiveTarget"))
	if !ok {
		t.Fatal("defaultWaiveTarget is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, options []map[string]interface{}) string {
		t.Helper()
		arr := make([]interface{}, len(options))
		for i, o := range options {
			arr[i] = o
		}
		res, err := fn(goja.Undefined(), vm.ToValue(arr))
		if err != nil {
			t.Fatalf("defaultWaiveTarget threw: %v", err)
		}
		return res.String()
	}
	t.Run("no options — whole balance", func(t *testing.T) {
		if got := run(t, nil); got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})
	t.Run("exactly one open debit — defaults to it (S5: an untouched waiver must still carry reversesRef)", func(t *testing.T) {
		options := []map[string]interface{}{{"value": "tx-only", "label": "08/01/2026 · $60 remaining"}}
		if got := run(t, options); got != "tx-only" {
			t.Errorf("got %q, want %q", got, "tx-only")
		}
	})
	t.Run("several open debits — whole balance stays the default, the desk must pick", func(t *testing.T) {
		options := []map[string]interface{}{
			{"value": "tx-1", "label": "08/01/2026 · $60 remaining"},
			{"value": "tx-2", "label": "09/01/2026 · $25 remaining"},
		}
		if got := run(t, options); got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})
}

func TestOpenChargeOptions(t *testing.T) {
	vm := ledgerUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("__jsonOpenChargeOptions"))
	if !ok {
		t.Fatal("__jsonOpenChargeOptions is not a function after evaluating the test harness")
	}
	run := func(t *testing.T, txs []map[string]interface{}) []pickerOption {
		t.Helper()
		arr := make([]interface{}, len(txs))
		for i, tx := range txs {
			arr[i] = tx
		}
		res, err := fn(goja.Undefined(), vm.ToValue(arr))
		if err != nil {
			t.Fatalf("openChargeOptions threw: %v", err)
		}
		var out []pickerOption
		if err := json.Unmarshal([]byte(res.String()), &out); err != nil {
			t.Fatalf("unmarshal openChargeOptions result %q: %v", res.String(), err)
		}
		return out
	}

	t.Run("only openCents > 0, oldest first", func(t *testing.T) {
		txs := []map[string]interface{}{
			{"transactionKey": "t1", "type": "debit", "openCents": 0, "postedAt": "2026-09-01T12:00:00Z"},
			{"transactionKey": "t2", "type": "debit", "openCents": 2000, "postedAt": "2026-08-01T12:00:00Z"},
			{"transactionKey": "t3", "type": "credit", "openCents": 0, "postedAt": "2026-08-10T12:00:00Z"},
			{"transactionKey": "t4", "type": "debit", "openCents": 1500, "postedAt": "2026-07-01T12:00:00Z"},
		}
		got := run(t, txs)
		if len(got) != 2 {
			t.Fatalf("got %d options, want 2 (%v)", len(got), got)
		}
		if got[0].Value != "t4" || got[1].Value != "t2" {
			t.Errorf("order/membership = %v, want [t4, t2] (oldest open debit first)", got)
		}
	})
}

func dateAtDay(t *testing.T, year, month, day int) string {
	t.Helper()
	return time.Date(year, time.Month(month), day, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// TestSelfPayCapMessage pins the patient's own-payment courtesy to the two
// refusals ClinicCreditAccount's self-scope leg raises: nothing owed
// (NoBalanceToPay) and more than owed (PaymentExceedsBalance). An unknown
// balance never blocks — the script is the authority when the panel has no
// figure to compare against.
func TestSelfPayCapMessage(t *testing.T) {
	vm := ledgerUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("selfPayCapMessage"))
	if !ok {
		t.Fatal("selfPayCapMessage is not a function after evaluating its declaration")
	}
	run := func(owed, cents interface{}) string {
		t.Helper()
		v, err := fn(goja.Undefined(), vm.ToValue(owed), vm.ToValue(cents))
		if err != nil {
			t.Fatalf("selfPayCapMessage(%v, %v): %v", owed, cents, err)
		}
		return v.String()
	}
	if got := run(5000, 2500); got != "" {
		t.Fatalf("within the balance must pass, got %q", got)
	}
	if got := run(5000, 5000); got != "" {
		t.Fatalf("exactly the balance must pass, got %q", got)
	}
	if got := run(5000, 5001); got == "" {
		t.Fatal("one cent over the balance must be refused")
	}
	if got := run(0, 100); got == "" {
		t.Fatal("a payment against nothing owed must be refused")
	}
	if got := run(-1500, 100); got == "" {
		t.Fatal("a payment against a credit balance must be refused")
	}
	if got := run(nil, 100); got != "" {
		t.Fatalf("an unknown balance never blocks, got %q", got)
	}
}
