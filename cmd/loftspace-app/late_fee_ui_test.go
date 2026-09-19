package main

import (
	"regexp"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"
)

// Goja proof of the late-fee helpers (docs/reviews/
// loftspace-ledger-reversal-and-late-fee-2026-09-18.md decisions 5 and 8):
// the fee-term line both cards render (lateFeeTermText), the Rent-owed
// row's "late fee billed <when>" clause (lateFeeBilledSuffix), the Set
// late fee control's cents bound (lateFeeCentsFromInput), and the tenant's
// combined statement carrying the reversal label off one-bill's rows. The
// reversal_ui_test.go harness; fixtures are json.Marshal-ed from the app's
// own wire structs (protectedApplicationRow / protectedLandlordRow /
// landlordLeaseBalance / oneBillEntryRow), so a field renamed on the Go side
// breaks this test instead of the fixture silently drifting from what the
// app serves.
var lateFeeUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction fmtMoney\(amount, currency\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction localDateTime\(iso\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction lateFeeTermText\(lateFeeCents, currency, graceDays\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction lateFeeBilledSuffix\(row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`\nconst LATE_FEE_MAX_CENTS = [0-9]+;\n`),
	regexp.MustCompile(`(?s)\nfunction lateFeeCentsFromInput\(value\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction reversalRowSuffix\(t, byKey\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction reversedCents\(t, rows\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction reversibleCents\(t, rows\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction billedForRowSuffix\(t, byKey\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction billedForReversedHint\(t, rows, byKey\) \{\n.*?\n\}\n`),
}

func lateFeeUIVM(t *testing.T) *goja.Runtime {
	t.Helper()
	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	prev := time.Local
	time.Local = la
	t.Cleanup(func() { time.Local = prev })
	src, err := webFS.ReadFile("web/app.js")
	require.NoError(t, err)
	vm := goja.New()
	for _, re := range lateFeeUIDecls {
		decl := re.FindString(string(src))
		require.NotEmptyf(t, decl, "app.js: no top-level declaration matching %s — the extraction regex no longer matches this file", re)
		_, err := vm.RunString(decl)
		require.NoErrorf(t, err, "goja eval of the shipped declaration %s", re)
	}
	return vm
}

func callString(t *testing.T, vm *goja.Runtime, fn string, args ...any) string {
	t.Helper()
	f, ok := goja.AssertFunction(vm.Get(fn))
	require.Truef(t, ok, "%s is not a function after evaluating its declaration", fn)
	vals := make([]goja.Value, 0, len(args))
	for _, a := range args {
		vals = append(vals, toGojaValue(t, vm, a))
	}
	res, err := f(goja.Undefined(), vals...)
	require.NoError(t, err)
	if goja.IsNull(res) || goja.IsUndefined(res) {
		return ""
	}
	return res.String()
}

// TestLateFeeTermText_ReadsTheTermOffTheWireRow pins the fee line off the
// two application wire structs: "$50 after 5 days" from a 5000-cent term
// with the ledger's grace known, "after the grace period" before the config
// has loaded (never a number the browser made up), the listing's currency
// carried, and nothing at all for a lease with no term (the null column).
func TestLateFeeTermText_ReadsTheTermOffTheWireRow(t *testing.T) {
	vm := lateFeeUIVM(t)
	fee := int64(5000)
	usd := "USD"
	eur := "EUR"

	tenant := protectedApplicationRow{EntityKey: "vtx.leaseapp.a", LateFeeCents: &fee, UnitCurrency: &usd}
	landlord := protectedLandlordRow{EntityKey: "vtx.leaseapp.a", LateFeeCents: &fee, UnitCurrency: &usd}
	for _, row := range []any{tenant, landlord} {
		m := toGojaValue(t, vm, row).Export().(map[string]any)
		got := callString(t, vm, "lateFeeTermText", m["lateFeeCents"], m["unitCurrency"], 5)
		require.Equal(t, "$50 after 5 days", got)
		require.Equal(t, "$50 after the grace period", callString(t, vm, "lateFeeTermText", m["lateFeeCents"], m["unitCurrency"], nil),
			"before the config reports the grace the line names the grace period, never a literal")
	}
	require.Equal(t, "$50 after 1 day", callString(t, vm, "lateFeeTermText", 5000, "USD", 1))
	require.Equal(t, "50 EUR after 5 days", callString(t, vm, "lateFeeTermText", 5000, eur, 5), "the listing's currency is the deposit line's rule too")
	require.Equal(t, "$12.5 after 5 days", callString(t, vm, "lateFeeTermText", 1250, "USD", 5))

	none := protectedApplicationRow{EntityKey: "vtx.leaseapp.b", UnitCurrency: &usd}
	m := toGojaValue(t, vm, none).Export().(map[string]any)
	require.Equal(t, "", callString(t, vm, "lateFeeTermText", m["lateFeeCents"], m["unitCurrency"], 5), "no term, no line — the null column renders nothing")
	require.Equal(t, "", callString(t, vm, "lateFeeTermText", 0, "USD", 5))
}

// TestLateFeeBilledSuffix_RendersTheInstantInLocalTime pins the Rent-owed
// row's clause off the landlordLeaseBalance wire struct: a billed episode
// reads " · late fee billed <lateFeeBilledAt in the viewer's zone>", an
// episode never charged one reads "", and the same clause serves the ledger
// / one-bill responses (the lateFeeBilledAt field they carry).
func TestLateFeeBilledSuffix_RendersTheInstantInLocalTime(t *testing.T) {
	vm := lateFeeUIVM(t)
	billed := landlordLeaseBalance{LeaseAppKey: "vtx.leaseapp.a", BalanceCents: 245000, DueDate: "2026-09-01T09:00:00Z", IsOverdue: true, DaysOverdue: 11,
		ReminderSentAt: "2026-09-12T17:01:51Z", LateFeeBilledAt: "2026-09-12T17:01:51Z"}
	// 2026-09-12T17:01:51Z = 10:01 Pacific; goja's toLocaleString honours the
	// zone but not the dateStyle/timeStyle options.
	got := callString(t, vm, "lateFeeBilledSuffix", billed)
	require.Contains(t, got, " · late fee billed ")
	require.Contains(t, got, "2026")
	require.Contains(t, got, "10:01")
	require.NotContains(t, got, "17:01", "an instant renders in the viewer's zone, never its UTC slice")

	unbilled := landlordLeaseBalance{LeaseAppKey: "vtx.leaseapp.b", BalanceCents: 100000, ReminderSentAt: "2026-09-12T17:01:51Z"}
	require.Equal(t, "", callString(t, vm, "lateFeeBilledSuffix", unbilled), "a reminder without a fee never reads as billed")
	require.Equal(t, "", callString(t, vm, "lateFeeBilledSuffix", nil))
}

// TestLateFeeCentsFromInput_IsTheOpsOwnBound pins the Set late fee control's
// client-side bound against SetLateFee's InvalidArgument: dollars become
// whole cents, a fraction of a cent, zero, a negative, blank and prose are
// refused before submit.
func TestLateFeeCentsFromInput_IsTheOpsOwnBound(t *testing.T) {
	vm := lateFeeUIVM(t)
	f, ok := goja.AssertFunction(vm.Get("lateFeeCentsFromInput"))
	require.True(t, ok)
	call := func(v any) goja.Value {
		res, err := f(goja.Undefined(), vm.ToValue(v))
		require.NoError(t, err)
		return res
	}
	require.Equal(t, int64(5000), call("50").ToInteger())
	require.Equal(t, int64(5000), call("50.00").ToInteger())
	require.Equal(t, int64(1250), call(" 12.50 ").ToInteger())
	require.Equal(t, int64(1), call("0.01").ToInteger())
	require.Equal(t, int64(100000000), call("1000000").ToInteger(), "one million dollars is the op's own ceiling, admitted")
	for _, bad := range []any{"0", "-5", "", "abc", "12.345", "0.001", "1000000.01", "5000000", nil} {
		require.Truef(t, goja.IsNull(call(bad)), "%v must be refused before submit", bad)
	}
}

// lateFeeLedgerRows is the landlord ledger as /api/ledger serves it: rent
// charged 09-01, the late fee billed for it 09-12 (billedFor the rent), a
// second rent 10-01 with its own fee 10-12, and a payment.
func lateFeeLedgerRows() []ledgerEntryRow {
	return []ledgerEntryRow{
		{TransactionKey: "vtx.transaction.rent1", Type: "debit", AmountCents: 240000, PostedAt: "2026-09-01T17:01:51Z", DueAt: "2026-09-01T17:01:51Z"},
		{TransactionKey: "vtx.transaction.fee1", Type: "debit", AmountCents: 5000, PostedAt: "2026-09-12T09:00:00Z", DueAt: "2026-09-12T09:00:00Z", Memo: "Late fee — rent due 2026-09-01", BilledForKey: "vtx.transaction.rent1"},
		{TransactionKey: "vtx.transaction.pay", Type: "credit", AmountCents: 100000, PostedAt: "2026-09-13T09:00:00Z"},
		{TransactionKey: "vtx.transaction.rent2", Type: "debit", AmountCents: 240000, PostedAt: "2026-10-01T17:01:51Z", DueAt: "2026-10-01T17:01:51Z"},
		{TransactionKey: "vtx.transaction.fee2", Type: "debit", AmountCents: 5000, PostedAt: "2026-10-12T09:00:00Z", DueAt: "2026-10-12T09:00:00Z", Memo: "Late fee — rent due 2026-10-01", BilledForKey: "vtx.transaction.rent2"},
	}
}

func rowsIndex(t *testing.T, vm *goja.Runtime, rows any) goja.Value {
	t.Helper()
	byKey, err := vm.RunString(`(function (rows) { return new Map(rows.map((t) => [t.transactionKey, t])); })`)
	require.NoError(t, err)
	mkMap, ok := goja.AssertFunction(byKey)
	require.True(t, ok)
	index, err := mkMap(goja.Undefined(), toGojaValue(t, vm, rows))
	require.NoError(t, err)
	return index
}

// TestBilledForRowSuffix_NamesTheChargeInLocalTime pins the fee row's label
// on both hats' rows (ledgerEntryRow and oneBillEntryRow carry billedForKey
// alike): a fee reads "late fee for the charge of <the charge's postedAt in
// the viewer's zone>"; a fee whose charge is not among the rows still says
// it is for a charge; a charge, a payment and a reversal read nothing.
func TestBilledForRowSuffix_NamesTheChargeInLocalTime(t *testing.T) {
	vm := lateFeeUIVM(t)
	rows := lateFeeLedgerRows()
	index := rowsIndex(t, vm, rows)
	fn, ok := goja.AssertFunction(vm.Get("billedForRowSuffix"))
	require.True(t, ok)
	call := func(row any) string {
		res, err := fn(goja.Undefined(), toGojaValue(t, vm, row), index)
		require.NoError(t, err)
		return res.String()
	}
	// The charge posted 2026-09-01T17:01:51Z = 10:01 Pacific.
	label := call(rows[1])
	require.Contains(t, label, " · late fee for the charge of ")
	require.Contains(t, label, "10:01")
	require.NotContains(t, label, "17:01", "an instant renders in the viewer's zone, never its UTC slice")
	require.Equal(t, "", call(rows[0]), "a charge is billed for nothing")
	require.Equal(t, "", call(rows[2]), "a payment is billed for nothing")
	orphan := ledgerEntryRow{TransactionKey: "vtx.transaction.orphanfee", Type: "debit", AmountCents: 5000, PostedAt: "2026-09-12T09:00:00Z", BilledForKey: "vtx.transaction.gone"}
	require.Equal(t, " · late fee for a charge", call(orphan))

	tenant := oneBillEntryRow{TransactionKey: "vtx.transaction.fee1", Type: "debit", AmountCents: 5000, PostedAt: "2026-09-12T09:00:00Z", Source: "rent", BilledForKey: "vtx.transaction.rent1"}
	tenantRows := []oneBillEntryRow{{TransactionKey: "vtx.transaction.rent1", Type: "debit", AmountCents: 240000, PostedAt: "2026-09-01T17:01:51Z", Source: "rent"}, tenant}
	tenantIndex := rowsIndex(t, vm, tenantRows)
	res, err := fn(goja.Undefined(), toGojaValue(t, vm, tenant), tenantIndex)
	require.NoError(t, err)
	require.Contains(t, res.String(), " · late fee for the charge of ")
	require.Contains(t, res.String(), "10:01")
}

// TestBilledForReversedHint_OnlyWhenTheChargeIsReversedOut pins the hint the
// landlord's Reverse control carries on a fee row: present exactly when the
// charge the fee was billed for has reversals covering its whole face
// (reversibleCents(charge) === 0) and the fee itself is still unreversed;
// absent while the charge stands, once the fee is reversed too, on a
// partially reversed charge, and on every non-fee row.
func TestBilledForReversedHint_OnlyWhenTheChargeIsReversedOut(t *testing.T) {
	vm := lateFeeUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("billedForReversedHint"))
	require.True(t, ok)
	call := func(row ledgerEntryRow, rows []ledgerEntryRow) string {
		res, err := fn(goja.Undefined(), toGojaValue(t, vm, row), toGojaValue(t, vm, rows), rowsIndex(t, vm, rows))
		require.NoError(t, err)
		return res.String()
	}
	const hint = "The charge this fee was billed for has been reversed."
	standing := lateFeeLedgerRows()
	require.Equal(t, "", call(standing[1], standing), "the charge stands: no hint")

	reversed := append(lateFeeLedgerRows(), ledgerEntryRow{TransactionKey: "vtx.transaction.rev1", Type: "credit", AmountCents: 240000, PostedAt: "2026-09-14T09:00:00Z", ReversesKey: "vtx.transaction.rent1"})
	require.Equal(t, hint, call(reversed[1], reversed), "the charge is reversed in full: the fee's control carries the hint")
	require.Equal(t, "", call(reversed[4], reversed), "the OTHER episode's fee, whose charge stands, carries none")
	require.Equal(t, "", call(reversed[0], reversed), "the reversed charge itself carries none (it has no Reverse to carry it on)")
	require.Equal(t, "", call(reversed[5], reversed), "a credit carries none")

	partial := append(lateFeeLedgerRows(), ledgerEntryRow{TransactionKey: "vtx.transaction.half", Type: "credit", AmountCents: 100000, PostedAt: "2026-09-14T09:00:00Z", ReversesKey: "vtx.transaction.rent1"})
	require.Equal(t, "", call(partial[1], partial), "a partial reversal leaves the charge standing — the op's own cap, not a coarser key")

	feeReversedToo := append(reversed, ledgerEntryRow{TransactionKey: "vtx.transaction.revfee", Type: "credit", AmountCents: 5000, PostedAt: "2026-09-15T09:00:00Z", ReversesKey: "vtx.transaction.fee1"})
	require.Equal(t, "", call(feeReversedToo[1], feeReversedToo), "once the fee is reversed too there is nothing left to hint about")
}

// TestOneBillRows_CarryTheReversalLabel pins the tenant's combined statement
// on one-bill's own wire row: a reversing credit (reversesKey off
// rentEntries' reverses hop) reads "reverses the charge of <when>" exactly
// as the landlord's ledger does, resolved against the statement's rows.
func TestOneBillRows_CarryTheReversalLabel(t *testing.T) {
	vm := lateFeeUIVM(t)
	rows := []oneBillEntryRow{
		{TransactionKey: "vtx.transaction.sep", Type: "debit", AmountCents: 205000, PostedAt: "2026-09-05T17:01:51Z", Source: "rent"},
		{TransactionKey: "vtx.transaction.rev", Type: "credit", AmountCents: 205000, PostedAt: "2026-09-13T09:00:00Z", Source: "rent", ReversesKey: "vtx.transaction.sep"},
		{TransactionKey: "vtx.transaction.pay", Type: "credit", AmountCents: 50000, PostedAt: "2026-09-14T09:00:00Z", Source: "rent"},
	}
	byKey, err := vm.RunString(`(function (rows) { return new Map(rows.map((t) => [t.transactionKey, t])); })`)
	require.NoError(t, err)
	mkMap, ok := goja.AssertFunction(byKey)
	require.True(t, ok)
	index, err := mkMap(goja.Undefined(), toGojaValue(t, vm, rows))
	require.NoError(t, err)
	fn, ok := goja.AssertFunction(vm.Get("reversalRowSuffix"))
	require.True(t, ok)
	call := func(row oneBillEntryRow) string {
		res, err := fn(goja.Undefined(), toGojaValue(t, vm, row), index)
		require.NoError(t, err)
		return res.String()
	}
	label := call(rows[1])
	require.Contains(t, label, " · reverses the charge of ")
	require.Contains(t, label, "10:01")
	require.Equal(t, "", call(rows[2]))
	require.Equal(t, "", call(rows[0]))
}
