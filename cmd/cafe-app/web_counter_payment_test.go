package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// counterPaymentLineDecl lifts the shipped counterPaymentLine declaration out
// of the embedded app.js — the pure decision every surface showing a settled
// tab's counter payment (the resident's Pending posting panel, the Front
// Desk Today panel) shares, the same extract-and-run-the-real-source posture
// as receiptLinesVM (web_receipt_test.go). money() rides along (moneyDecl,
// web_hold_test.go) because counterPaymentLine formats the amount through it.
var counterPaymentLineDecl = regexp.MustCompile(`(?s)\nfunction counterPaymentLine\(tab\) \{\n.*?\n\}\n`)

func counterPaymentLineVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)
	vm := goja.New()
	for _, d := range []struct {
		name string
		re   *regexp.Regexp
	}{
		{"money", moneyDecl},
		{"counterPaymentLine", counterPaymentLineDecl},
	} {
		decl := d.re.FindString(text)
		if decl == "" {
			t.Fatalf("app.js: no top-level `function %s(…) {…}` declaration found — the extraction regex no longer matches this file", d.name)
		}
		if strings.Contains(decl, "document") || strings.Contains(decl, "window") {
			t.Fatalf("%s reaches the DOM; it must stay a pure function of its argument:\n%s", d.name, decl)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped %s: %v", d.name, err)
		}
	}
	fn, ok := goja.AssertFunction(vm.Get("counterPaymentLine"))
	if !ok {
		t.Fatal("counterPaymentLine is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestCounterPaymentLine_AbsentIsEmpty proves a tab carrying no
// paidAtSettleCents at all — a settle with no counter payment, or a tab
// settled before the field existed — renders no tag.
func TestCounterPaymentLine_AbsentIsEmpty(t *testing.T) {
	vm, fn := counterPaymentLineVM(t)
	res, err := fn(goja.Undefined(), vm.ToValue(map[string]any{"tabKey": "vtx.tab.a", "totalCents": 800}))
	if err != nil {
		t.Fatalf("counterPaymentLine threw: %v", err)
	}
	if got := res.String(); got != "" {
		t.Errorf(`counterPaymentLine(absent) = %q, want ""`, got)
	}
}

// TestCounterPaymentLine_ZeroIsEmpty proves a defensively-zero
// paidAtSettleCents (never minted live — the op requires a positive amount)
// also renders nothing, matching the "absent" reading rather than a
// misleading "paid $0.00 at the counter".
func TestCounterPaymentLine_ZeroIsEmpty(t *testing.T) {
	vm, fn := counterPaymentLineVM(t)
	res, err := fn(goja.Undefined(), vm.ToValue(map[string]any{"tabKey": "vtx.tab.a", "paidAtSettleCents": 0}))
	if err != nil {
		t.Fatalf("counterPaymentLine threw: %v", err)
	}
	if got := res.String(); got != "" {
		t.Errorf(`counterPaymentLine(0) = %q, want ""`, got)
	}
}

// TestCounterPaymentLine_RendersTheAmount pins the exact phrase every
// surface tags a paid settlement with.
func TestCounterPaymentLine_RendersTheAmount(t *testing.T) {
	vm, fn := counterPaymentLineVM(t)
	res, err := fn(goja.Undefined(), vm.ToValue(map[string]any{"tabKey": "vtx.tab.a", "paidAtSettleCents": 1949}))
	if err != nil {
		t.Fatalf("counterPaymentLine threw: %v", err)
	}
	if got, want := res.String(), "paid $19.49 at the counter"; got != want {
		t.Errorf("counterPaymentLine(1949) = %q, want %q", got, want)
	}
}

// settlePayEnvelopeDecl lifts the shipped settlePayEnvelope declaration out
// of the embedded app.js — the single envelope both desk "Settle & pay"
// buttons (POS and Front Desk) submit through opOrThrow, so a drift between
// the two sites is impossible by construction. idOf/chargedToOptionalRead
// ride along since settlePayEnvelope's optionalReads calls through them.
var settlePayEnvelopeDecl = regexp.MustCompile(`(?s)\nfunction settlePayEnvelope\(tabKey, leaseAppKey, totalCents\) \{\n.*?\n\}\n`)
var idOfDecl = regexp.MustCompile(`(?s)\nfunction idOf\(key\) \{\n.*?\n\}\n`)
var chargedToOptionalReadDecl = regexp.MustCompile(`(?s)\nfunction chargedToOptionalRead\(tabKey, leaseAppKey\) \{\n.*?\n\}\n`)

func settlePayEnvelopeVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)
	vm := goja.New()
	for _, d := range []struct {
		name string
		re   *regexp.Regexp
	}{
		{"idOf", idOfDecl},
		{"chargedToOptionalRead", chargedToOptionalReadDecl},
		{"settlePayEnvelope", settlePayEnvelopeDecl},
	} {
		decl := d.re.FindString(text)
		if decl == "" {
			t.Fatalf("app.js: no top-level `function %s(…) {…}` declaration found — the extraction regex no longer matches this file", d.name)
		}
		if strings.Contains(decl, "document") || strings.Contains(decl, "window") {
			t.Fatalf("%s reaches the DOM; it must stay a pure function of its arguments:\n%s", d.name, decl)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped %s: %v", d.name, err)
		}
	}
	fn, ok := goja.AssertFunction(vm.Get("settlePayEnvelope"))
	if !ok {
		t.Fatal("settlePayEnvelope is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestSettlePayEnvelope_PaidCentsIsTheTabTotal pins the shape both desk
// buttons submit: paidCents always equals the totalCents argument (the
// card's own total, never a typed amount — the courtesy that keeps
// PaidMismatchesTab a stale-card refusal rather than something a visitor
// could construct), and reads names only the tab + its status, mirroring
// every other Settle dispatch in this file.
func TestSettlePayEnvelope_PaidCentsIsTheTabTotal(t *testing.T) {
	vm, fn := settlePayEnvelopeVM(t)
	res, err := fn(goja.Undefined(), vm.ToValue("vtx.tab.a"), vm.ToValue("vtx.leaseapp.a"), vm.ToValue(1949))
	if err != nil {
		t.Fatalf("settlePayEnvelope threw: %v", err)
	}
	obj := res.ToObject(vm)
	if got := obj.Get("operationType").String(); got != "Settle" {
		t.Errorf("operationType = %q, want %q", got, "Settle")
	}
	if got := obj.Get("class").String(); got != "tab" {
		t.Errorf("class = %q, want %q", got, "tab")
	}
	payload := obj.Get("payload").ToObject(vm)
	if got, want := payload.Get("tabKey").String(), "vtx.tab.a"; got != want {
		t.Errorf("payload.tabKey = %q, want %q", got, want)
	}
	if got, want := payload.Get("paidCents").ToInteger(), int64(1949); got != want {
		t.Errorf("payload.paidCents = %d, want %d (the tab's own totalCents)", got, want)
	}
	reads := obj.Get("reads")
	if got, want := reads.ToObject(vm).Get("length").ToInteger(), int64(2); got != want {
		t.Fatalf("reads.length = %d, want %d", got, want)
	}
	readsJoined := vm.Get("JSON").ToObject(vm)
	stringify, _ := goja.AssertFunction(readsJoined.Get("stringify"))
	readsJSON, err := stringify(goja.Undefined(), reads)
	if err != nil {
		t.Fatalf("JSON.stringify(reads): %v", err)
	}
	if got, want := readsJSON.String(), `["vtx.tab.a","vtx.tab.a.status"]`; got != want {
		t.Errorf("reads = %s, want %s", got, want)
	}
}
