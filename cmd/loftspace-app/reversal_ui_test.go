package main

import (
	"regexp"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/require"
)

// Goja proof of the ledger panel's reversal helpers (docs/reviews/
// loftspace-ledger-reversal-and-late-fee-2026-09-18.md decision 4): the
// netting the Reverse control applies before it offers itself
// (reversibleCents — the op's own cap, face less what earlier reversals
// absorbed) and the credit row's "reverses the charge of …" label
// (reversalRowSuffix). The same lift-the-shipped-declaration-out-of-app.js
// harness reporter_label_test.go uses; fixtures are json.Marshal-ed from the
// app's own wire struct (ledgerEntryRow), so a field renamed on the Go side
// breaks this test instead of the fixture silently drifting from what
// /api/ledger serves.
var reversalUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction moneyAmount\(n\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction localDateTime\(iso\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction reversedCents\(t, rows\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction reversibleCents\(t, rows\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction reversalRowSuffix\(t, byKey\) \{\n.*?\n\}\n`),
}

// reversalUIVM evaluates the declarations under a pinned America/Los_Angeles
// process zone (the lease_term_ui_test.go harness shape), so the label's
// localDateTime provably renders the charge's instant in the viewer's zone —
// an instant renders locale everywhere on this panel.
func reversalUIVM(t *testing.T) *goja.Runtime {
	t.Helper()
	la, err := time.LoadLocation("America/Los_Angeles")
	require.NoError(t, err)
	prev := time.Local
	time.Local = la
	t.Cleanup(func() { time.Local = prev })
	src, err := webFS.ReadFile("web/app.js")
	require.NoError(t, err)
	vm := goja.New()
	for _, re := range reversalUIDecls {
		decl := re.FindString(string(src))
		require.NotEmptyf(t, decl, "app.js: no top-level declaration matching %s — the extraction regex no longer matches this file", re)
		_, err := vm.RunString(decl)
		require.NoErrorf(t, err, "goja eval of the shipped declaration %s", re)
	}
	return vm
}

// reversalFixtureRows is the live shape that minted the rule, as
// /api/ledger serves it: rent charged 08-06, a partial payment, rent charged
// 09-05, that charge reversed in full by a credit naming it, the renewed rent.
func reversalFixtureRows() []ledgerEntryRow {
	return []ledgerEntryRow{
		{TransactionKey: "vtx.transaction.aug", Type: "debit", AmountCents: 205000, PostedAt: "2026-08-06T17:01:51Z", DueAt: "2026-08-06T17:01:51Z"},
		{TransactionKey: "vtx.transaction.pay", Type: "credit", AmountCents: 50000, PostedAt: "2026-09-04T09:00:00Z"},
		{TransactionKey: "vtx.transaction.sep", Type: "debit", AmountCents: 205000, PostedAt: "2026-09-05T17:01:51Z", DueAt: "2026-09-05T17:01:51Z"},
		{TransactionKey: "vtx.transaction.rev", Type: "credit", AmountCents: 205000, PostedAt: "2026-09-13T09:00:00Z", Memo: "Reversal", ReversesKey: "vtx.transaction.sep"},
		{TransactionKey: "vtx.transaction.renewed", Type: "debit", AmountCents: 212500, PostedAt: "2026-09-13T09:30:00Z", DueAt: "2026-09-13T09:30:00Z"},
	}
}

func callCents(t *testing.T, vm *goja.Runtime, fn string, row ledgerEntryRow, rows []ledgerEntryRow) int64 {
	t.Helper()
	f, ok := goja.AssertFunction(vm.Get(fn))
	require.Truef(t, ok, "%s is not a function after evaluating its declaration", fn)
	res, err := f(goja.Undefined(), toGojaValue(t, vm, row), toGojaValue(t, vm, rows))
	require.NoError(t, err)
	return res.ToInteger()
}

// TestReversibleCents_NetsEarlierReversalsAtTheFace pins the Reverse
// control's own predicate against the op's: a fully reversed charge offers
// nothing (0), an untouched charge offers its whole face, a partially
// reversed one the remainder, and the sum of reversals is capped at the face
// (a second full reversal already landed leaves nothing, never a negative);
// a credit row and a deduction row are never reversible.
func TestReversibleCents_NetsEarlierReversalsAtTheFace(t *testing.T) {
	vm := reversalUIVM(t)
	rows := reversalFixtureRows()

	require.Equal(t, int64(205000), callCents(t, vm, "reversedCents", rows[2], rows), "the 09-05 charge is named by one 205000 reversal")
	require.Equal(t, int64(0), callCents(t, vm, "reversibleCents", rows[2], rows), "a fully reversed charge offers no Reverse")
	require.Equal(t, int64(205000), callCents(t, vm, "reversibleCents", rows[0], rows), "the 08-06 charge nothing names offers its whole face")
	require.Equal(t, int64(212500), callCents(t, vm, "reversibleCents", rows[4], rows))
	require.Equal(t, int64(0), callCents(t, vm, "reversibleCents", rows[3], rows), "a credit is never reversible")

	partial := append(rows, ledgerEntryRow{TransactionKey: "vtx.transaction.half", Type: "credit", AmountCents: 100000, PostedAt: "2026-09-14T09:00:00Z", ReversesKey: "vtx.transaction.aug"})
	require.Equal(t, int64(105000), callCents(t, vm, "reversibleCents", partial[0], partial), "a partial reversal leaves the remainder")

	over := append(partial, ledgerEntryRow{TransactionKey: "vtx.transaction.over", Type: "credit", AmountCents: 205000, PostedAt: "2026-09-15T09:00:00Z", ReversesKey: "vtx.transaction.aug"})
	require.Equal(t, int64(0), callCents(t, vm, "reversibleCents", over[0], over), "reversals past the face floor at zero, never negative")

	deduction := ledgerEntryRow{TransactionKey: "vtx.transaction.ded", Type: "deduction", AmountCents: 5000, PostedAt: "2026-09-16T09:00:00Z", ClausePurpose: "deposit"}
	require.Equal(t, int64(0), callCents(t, vm, "reversibleCents", deduction, rows), "a deduction is neither a charge nor reversible")

	// The security deposit's own charge is never offered a Reverse: the op
	// refuses it DepositNotReversible (the deposit is deducted from or
	// returned), and a reversal would leave it "held" on the statement.
	depositCharge := ledgerEntryRow{TransactionKey: "vtx.transaction.dep", Type: "debit", AmountCents: 250000, PostedAt: "2026-09-02T09:00:00Z", ClauseKey: "vtx.clause.dep", ClausePurpose: "deposit"}
	require.Equal(t, int64(0), callCents(t, vm, "reversibleCents", depositCharge, rows), "the deposit charge offers no Reverse")
}

// TestReversalRowSuffix_NamesTheChargeInLocalTime pins the credit row's
// label: a reversing credit reads "reverses the charge of <the named
// charge's postedAt, in the viewer's zone>", resolved through the same
// response's rows; a reversal whose charge is not among the rows still says
// it reverses a charge; a plain payment and a debit read nothing.
func TestReversalRowSuffix_NamesTheChargeInLocalTime(t *testing.T) {
	vm := reversalUIVM(t)
	rows := reversalFixtureRows()
	fn, ok := goja.AssertFunction(vm.Get("reversalRowSuffix"))
	require.True(t, ok)
	byKey, err := vm.RunString(`(function (rows) { return new Map(rows.map((t) => [t.transactionKey, t])); })`)
	require.NoError(t, err)
	mkMap, ok := goja.AssertFunction(byKey)
	require.True(t, ok)
	index, err := mkMap(goja.Undefined(), toGojaValue(t, vm, rows))
	require.NoError(t, err)
	call := func(row ledgerEntryRow) string {
		res, err := fn(goja.Undefined(), toGojaValue(t, vm, row), index)
		require.NoError(t, err)
		return res.String()
	}

	// The named charge posted 2026-09-05T17:01:51Z = 10:01 Pacific. goja's
	// toLocaleString honours the zone but not the dateStyle/timeStyle
	// options, so the pin holds the zone-shifted clock and the day, not the
	// browser's exact spelling.
	label := call(rows[3])
	require.Contains(t, label, " · reverses the charge of ")
	require.Contains(t, label, "2026")
	require.Contains(t, label, "10:01")
	require.NotContains(t, label, "17:01", "an instant renders in the viewer's zone, never its UTC slice")

	require.Equal(t, "", call(rows[1]), "a plain payment names no charge")
	require.Equal(t, "", call(rows[2]), "a debit reverses nothing")
	orphan := ledgerEntryRow{TransactionKey: "vtx.transaction.orphan", Type: "credit", AmountCents: 1000, PostedAt: "2026-09-14T09:00:00Z", ReversesKey: "vtx.transaction.gone"}
	require.Equal(t, " · reverses a charge", call(orphan), "a charge outside the rows still reads as reversed, never silently dropped")
}
