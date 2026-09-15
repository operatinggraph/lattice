package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// receiptLines is the pure join renderResident's ledger list uses to attach
// a settled tab's itemized lines onto the row that named it (A1: "a posted
// charge has no receipt") — lifted out of the embedded app.js the same way
// web_hold_test.go lifts openTabGate.
var receiptLinesDecl = regexp.MustCompile(`(?s)\nfunction receiptLines\(row, tabByKey\) \{\n.*?\n\}\n`)

func receiptLinesVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := receiptLinesDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function receiptLines(row, tabByKey) {…}` declaration found — the extraction regex no longer matches this file")
	}
	if strings.Contains(decl, "document") || strings.Contains(decl, "window") {
		t.Fatalf("receiptLines reaches the DOM; it must stay a pure function of its arguments:\n%s", decl)
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped receiptLines: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("receiptLines"))
	if !ok {
		t.Fatal("receiptLines is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestReceiptLines_JoinsSettledTabByTabKey proves the successful join: a
// ledger row naming a tabKey that resolves in tabByKey returns that tab's
// own lines + itemsMemo.
func TestReceiptLines_JoinsSettledTabByTabKey(t *testing.T) {
	vm, fn := receiptLinesVM(t)
	row := map[string]any{"transactionKey": "vtx.cafetransaction.a", "type": "debit", "amountCents": 800, "tabKey": "vtx.tab.a"}
	tabByKey := map[string]any{
		"vtx.tab.a": map[string]any{
			"tabKey": "vtx.tab.a", "itemsMemo": "Latte, Croissant",
			"lines": []map[string]any{
				{"id": "line-1", "description": "Latte", "amountCents": 450, "voided": false},
				{"id": "line-2", "description": "Croissant", "amountCents": 350, "voided": false},
			},
		},
	}
	res, err := fn(goja.Undefined(), vm.ToValue(row), vm.ToValue(tabByKey))
	if err != nil {
		t.Fatalf("receiptLines threw: %v", err)
	}
	if goja.IsNull(res) || goja.IsUndefined(res) {
		t.Fatalf("want a joined result for a tabKey that resolves, got %v", res)
	}
	obj := res.ToObject(vm)
	if got := obj.Get("memo").String(); got != "Latte, Croissant" {
		t.Errorf("memo = %q, want %q", got, "Latte, Croissant")
	}
	lines := obj.Get("lines").ToObject(vm)
	if got := lines.Get("length").ToInteger(); got != 2 {
		t.Errorf("lines.length = %d, want 2", got)
	}
}

// TestReceiptLines_NoTabKeyReturnsNull proves an entry that carries no
// tabKey at all (a hand-posted debit, a payment credit) joins to nothing —
// it renders exactly as it does today, with no receipt.
func TestReceiptLines_NoTabKeyReturnsNull(t *testing.T) {
	vm, fn := receiptLinesVM(t)
	row := map[string]any{"transactionKey": "vtx.cafetransaction.b", "type": "credit", "amountCents": 500}
	res, err := fn(goja.Undefined(), vm.ToValue(row), vm.ToValue(map[string]any{}))
	if err != nil {
		t.Fatalf("receiptLines threw: %v", err)
	}
	if !goja.IsNull(res) {
		t.Errorf("want null for a row with no tabKey, got %v", res)
	}
}

// TestReceiptLines_TabKeyWithNoResolvingTabReturnsNull proves a tabKey that
// does not resolve in tabByKey (a projection still catching up, or a tab row
// that was pruned) degrades to no receipt rather than throwing or joining a
// wrong tab.
func TestReceiptLines_TabKeyWithNoResolvingTabReturnsNull(t *testing.T) {
	vm, fn := receiptLinesVM(t)
	row := map[string]any{"transactionKey": "vtx.cafetransaction.c", "type": "debit", "amountCents": 800, "tabKey": "vtx.tab.missing"}
	res, err := fn(goja.Undefined(), vm.ToValue(row), vm.ToValue(map[string]any{}))
	if err != nil {
		t.Fatalf("receiptLines threw: %v", err)
	}
	if !goja.IsNull(res) {
		t.Errorf("want null for a tabKey that does not resolve, got %v", res)
	}
}

// TestReceiptLines_JoinsByKeyRegardlessOfRowType proves the join is by KEY
// alone, never by the row's own type — a credit row carrying a tabKey (never
// happens today, but if it did) still joins, because receiptLines never
// inspects row.type.
func TestReceiptLines_JoinsByKeyRegardlessOfRowType(t *testing.T) {
	vm, fn := receiptLinesVM(t)
	row := map[string]any{"transactionKey": "vtx.cafetransaction.d", "type": "credit", "amountCents": 300, "tabKey": "vtx.tab.a"}
	tabByKey := map[string]any{
		"vtx.tab.a": map[string]any{
			"tabKey": "vtx.tab.a", "itemsMemo": "Muffin",
			"lines": []map[string]any{{"id": "line-1", "description": "Muffin", "amountCents": 300, "voided": false}},
		},
	}
	res, err := fn(goja.Undefined(), vm.ToValue(row), vm.ToValue(tabByKey))
	if err != nil {
		t.Fatalf("receiptLines threw: %v", err)
	}
	if goja.IsNull(res) {
		t.Errorf("want the join to succeed by tabKey regardless of row.type=credit, got null")
	}
}
