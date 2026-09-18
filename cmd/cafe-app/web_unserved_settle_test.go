package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// unservedLineCount and chargeLinesBlock, lifted out of the embedded app.js
// the same way web_orders_queue_test.go lifts ordersQueue: pure functions of
// their arguments, so the assertions below are statements about what ships.
// chargeLinesBlock's own dependencies (money/escapeHtml/itemsMemoLine/
// orderedByLabel/idOf/shortKey/nameForIdentity) ride along the same way
// web_hold_test.go rides money along openTabGate — nameForIdentity reaches
// state.identities, so the fixture below stubs a bare `state`.
var unservedLineCountDecl = regexp.MustCompile(`(?s)\nfunction unservedLineCount\(tab\) \{\n.*?\n\}\n`)
var chargeLinesBlockDecl = regexp.MustCompile(`(?s)\nfunction chargeLinesBlock\(lines, memo, voidableTabKey, tabOpen\) \{\n.*?\n\}\n`)
var moneyDeclUnserved = regexp.MustCompile(`(?s)\nfunction money\(cents\) \{\n.*?\n\}\n`)
var escapeHtmlDecl = regexp.MustCompile(`(?s)\nfunction escapeHtml\(s\) \{\n.*?\n\}\n`)
var itemsMemoLineDecl = regexp.MustCompile(`(?s)\nfunction itemsMemoLine\(memo\) \{\n.*?\n\}\n`)
var orderedByLabelDecl = regexp.MustCompile(`(?s)\nfunction orderedByLabel\(orderedBy\) \{\n.*?\n\}\n`)
var idOfDeclUnserved = regexp.MustCompile(`(?s)\nfunction idOf\(key\) \{\n.*?\n\}\n`)
var shortKeyDeclUnserved = regexp.MustCompile(`(?s)\nfunction shortKey\(key\) \{\n.*?\n\}\n`)
var nameForIdentityDecl = regexp.MustCompile(`(?s)\nfunction nameForIdentity\(key\) \{\n.*?\n\}\n`)
var settleButtonAttrsDecl = regexp.MustCompile(`(?s)\nfunction settleButtonAttrs\(count, title\) \{\n.*?\n\}\n`)
var residentSettlePanelMarkupDecl = regexp.MustCompile(`(?s)\nfunction residentSettlePanelMarkup\(count\) \{\n.*?\n\}\n`)
var renderOpenTabCardDecl = regexp.MustCompile(`(?s)\nfunction renderOpenTabCard\(tab, items, limitCents\) \{\n.*?\n\}\n`)
var frontDeskCardDecl = regexp.MustCompile(`(?s)\nfunction frontDeskCard\(t, booking, lease, visit, bookerKey, balance\) \{\n.*?\n\}\n`)
var frontDeskBalanceBadgeDecl = regexp.MustCompile(`(?s)\nfunction frontDeskBalanceBadge\(balance\) \{\n.*?\n\}\n`)
var unservedSettleHintDecl = regexp.MustCompile(`(?s)\nfunction unservedSettleHint\(count\) \{\n.*?\n\}\n`)

// mustEvalDecl extracts and evaluates one top-level `function <name>(...) {…}`
// declaration out of the embedded app.js text, failing the test with a clear
// message if the declaration is gone (a rename) or fails to evaluate.
func mustEvalDecl(t *testing.T, vm *goja.Runtime, text, name string, re *regexp.Regexp) {
	t.Helper()
	decl := re.FindString(text)
	if decl == "" {
		t.Fatalf("app.js: no top-level `function %s(…) {…}` declaration found — the extraction regex no longer matches this file", name)
	}
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped %s: %v", name, err)
	}
}

func unservedLineCountVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)
	decl := unservedLineCountDecl.FindString(text)
	if decl == "" {
		t.Fatal("app.js: no top-level `function unservedLineCount(tab) {…}` declaration found — the extraction regex no longer matches this file")
	}
	if strings.Contains(decl, "document") || strings.Contains(decl, "window") {
		t.Fatalf("unservedLineCount reaches the DOM; it must stay a pure function of its argument:\n%s", decl)
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped unservedLineCount: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("unservedLineCount"))
	if !ok {
		t.Fatal("unservedLineCount is not a function after evaluating its declaration")
	}
	return vm, fn
}

func chargeLinesBlockVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)
	vm := goja.New()
	if _, err := vm.RunString(`const state = { identities: [] };`); err != nil {
		t.Fatalf("state stub eval: %v", err)
	}
	for _, d := range []struct {
		name string
		re   *regexp.Regexp
	}{
		{"money", moneyDeclUnserved},
		{"escapeHtml", escapeHtmlDecl},
		{"itemsMemoLine", itemsMemoLineDecl},
		{"idOf", idOfDeclUnserved},
		{"shortKey", shortKeyDeclUnserved},
		{"nameForIdentity", nameForIdentityDecl},
		{"orderedByLabel", orderedByLabelDecl},
		{"chargeLinesBlock", chargeLinesBlockDecl},
	} {
		decl := d.re.FindString(text)
		if decl == "" {
			t.Fatalf("app.js: no top-level `function %s(…) {…}` declaration found — the extraction regex no longer matches this file", d.name)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped %s: %v", d.name, err)
		}
	}
	fn, ok := goja.AssertFunction(vm.Get("chargeLinesBlock"))
	if !ok {
		t.Fatal("chargeLinesBlock is not a function after evaluating its declaration")
	}
	return vm, fn
}

// tabChargeLinesJS marshals real tabChargeLine values (the exact Go struct
// /api/tabs serializes, tabs.go) into a goja array — so a fixture here is a
// statement about the wire shape the FE actually receives, not a free-form
// object a field rename could silently drift from.
func tabChargeLinesJS(t *testing.T, vm *goja.Runtime, lines []tabChargeLine) goja.Value {
	t.Helper()
	b, err := json.Marshal(lines)
	if err != nil {
		t.Fatalf("marshal tabChargeLine fixture: %v", err)
	}
	v, err := vm.RunString("(" + string(b) + ")")
	if err != nil {
		t.Fatalf("parse tabChargeLine fixture into goja: %v", err)
	}
	return v
}

// TestUnservedLineCount_AppliesSettlesOwnPredicate pins the exact predicate
// Settle's own refusal applies (unserved_line_ids, packages/cafe-domain): a
// legacy line (neither field), a served line and a voided to-make line all
// count for nothing; two real to-make lines and one synthetic pending line
// (a self-order this session just submitted, not yet reflected in the tab's
// own lines) count as unserved — 3 total.
func TestUnservedLineCount_AppliesSettlesOwnPredicate(t *testing.T) {
	vm, fn := unservedLineCountVM(t)
	tab := map[string]any{
		"tabKey": "vtx.tab.a",
		"lines": []map[string]any{
			{"id": "line-1", "description": "Legacy", "amountCents": 300},
			{"id": "line-2", "description": "Cortado", "amountCents": 400, "voided": true, "orderedAt": "2026-09-18T12:00:00Z"},
			{"id": "line-3", "description": "Latte", "amountCents": 450, "orderedAt": "2026-09-18T12:01:00Z", "servedAt": "2026-09-18T12:05:00Z"},
			{"id": "pending-0", "description": "Muffin", "amountCents": 300, "pending": true},
			{"id": "line-4", "description": "Espresso", "amountCents": 250, "orderedAt": "2026-09-18T12:02:00Z"},
			{"id": "line-5", "description": "Bagel", "amountCents": 300, "orderedAt": "2026-09-18T12:03:00Z"},
		},
	}
	res, err := fn(goja.Undefined(), vm.ToValue(tab))
	if err != nil {
		t.Fatalf("unservedLineCount threw: %v", err)
	}
	if got := res.ToInteger(); got != 3 {
		t.Fatalf("unservedLineCount = %d, want 3 (2 to-make lines + the pending overlay)", got)
	}
}

// TestUnservedLineCount_SettledTabStillCounts proves the helper is a pure
// read of the tab's own lines — the open-tab gate lives in the callers
// (renderOpenTabCard/frontDeskCard/renderResident only call it on an open
// tab), not in the helper itself.
func TestUnservedLineCount_SettledTabStillCounts(t *testing.T) {
	vm, fn := unservedLineCountVM(t)
	tab := map[string]any{
		"tabKey": "vtx.tab.b",
		"status": "settled",
		"lines": []map[string]any{
			{"id": "line-1", "description": "Latte", "amountCents": 450, "orderedAt": "2026-09-18T12:00:00Z"},
		},
	}
	res, err := fn(goja.Undefined(), vm.ToValue(tab))
	if err != nil {
		t.Fatalf("unservedLineCount threw: %v", err)
	}
	if got := res.ToInteger(); got != 1 {
		t.Fatalf("unservedLineCount = %d, want 1 (a settled tab still counts; the gate is the caller's)", got)
	}
}

// TestChargeLinesBlock_MarkServedOnlyOnOpenVoidableToMakeLine pins the Mark
// served button's exact gate: rendered on a to-make line when voidableTabKey
// is given and the tab is open, absent when voidableTabKey is null (a
// resident's own card, VoidCharge grants no self-service scope).
func TestChargeLinesBlock_MarkServedOnlyOnOpenVoidableToMakeLine(t *testing.T) {
	vm, fn := chargeLinesBlockVM(t)
	lines := []tabChargeLine{
		{ID: "line-1", Description: "Latte", AmountCents: 450, OrderedAt: "2026-09-18T12:00:00Z"},
	}
	linesJS := tabChargeLinesJS(t, vm, lines)
	res, err := fn(goja.Undefined(), linesJS, vm.ToValue(""), vm.ToValue("vtx.tab.a"), vm.ToValue(true))
	if err != nil {
		t.Fatalf("chargeLinesBlock threw: %v", err)
	}
	html := res.String()
	if !strings.Contains(html, `data-serve-line="line-1"`) {
		t.Errorf("voidable open tab, to-make line: want a data-serve-line button, got:\n%s", html)
	}

	res, err = fn(goja.Undefined(), linesJS, vm.ToValue(""), goja.Null(), vm.ToValue(true))
	if err != nil {
		t.Fatalf("chargeLinesBlock threw: %v", err)
	}
	html = res.String()
	if strings.Contains(html, "data-serve-line") {
		t.Errorf("voidableTabKey=null (a resident's own card): want no data-serve-line button, got:\n%s", html)
	}
}

// TestChargeLinesBlock_VoidedUnservedReadsNeverMade pins the sweep's own
// void tag: voidedReason "unserved" reads "(voided — never made)"; a desk
// void (no voidedReason) still reads plain "(voided)".
func TestChargeLinesBlock_VoidedUnservedReadsNeverMade(t *testing.T) {
	vm, fn := chargeLinesBlockVM(t)
	lines := []tabChargeLine{
		{ID: "line-1", Description: "Cortado", AmountCents: 400, Voided: true, VoidedReason: "unserved", OrderedAt: "2026-09-18T12:00:00Z"},
		{ID: "line-2", Description: "Muffin", AmountCents: 300, Voided: true, OrderedAt: "2026-09-18T12:00:00Z"},
	}
	linesJS := tabChargeLinesJS(t, vm, lines)
	res, err := fn(goja.Undefined(), linesJS, vm.ToValue(""), vm.ToValue("vtx.tab.a"), vm.ToValue(true))
	if err != nil {
		t.Fatalf("chargeLinesBlock threw: %v", err)
	}
	html := res.String()
	if !strings.Contains(html, "never made") {
		t.Errorf("voidedReason=unserved: want \"never made\" in the rendered line, got:\n%s", html)
	}
	if !strings.Contains(html, "(voided)") {
		t.Errorf("a plain desk void (no voidedReason): want \"(voided)\" preserved, got:\n%s", html)
	}
}

func settleButtonAttrsVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)
	vm := goja.New()
	mustEvalDecl(t, vm, text, "escapeHtml", escapeHtmlDecl)
	mustEvalDecl(t, vm, text, "settleButtonAttrs", settleButtonAttrsDecl)
	fn, ok := goja.AssertFunction(vm.Get("settleButtonAttrs"))
	if !ok {
		t.Fatal("settleButtonAttrs is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestSettleButtonAttrs_DisabledWithTitleWhenPositiveElseEmpty pins the one
// function every settle button (POS, Front Desk, resident) shares: a
// positive count renders `disabled title="<escaped title>"`, zero renders
// nothing.
func TestSettleButtonAttrs_DisabledWithTitleWhenPositiveElseEmpty(t *testing.T) {
	vm, fn := settleButtonAttrsVM(t)
	res, err := fn(goja.Undefined(), vm.ToValue(0), vm.ToValue("Mark served or void every unserved order first"))
	if err != nil {
		t.Fatalf("settleButtonAttrs threw: %v", err)
	}
	if got := res.String(); got != "" {
		t.Errorf("count=0: want \"\", got %q", got)
	}
	res, err = fn(goja.Undefined(), vm.ToValue(2), vm.ToValue(`A "quoted" title`))
	if err != nil {
		t.Fatalf("settleButtonAttrs threw: %v", err)
	}
	want := ` disabled title="A &quot;quoted&quot; title"`
	if got := res.String(); got != want {
		t.Errorf("count=2: got %q, want %q", got, want)
	}
}

func renderOpenTabCardVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)
	vm := goja.New()
	if _, err := vm.RunString(`const state = { identities: [] };`); err != nil {
		t.Fatalf("state stub eval: %v", err)
	}
	for _, d := range []struct {
		name string
		re   *regexp.Regexp
	}{
		{"money", moneyDeclUnserved},
		{"escapeHtml", escapeHtmlDecl},
		{"itemsMemoLine", itemsMemoLineDecl},
		{"idOf", idOfDeclUnserved},
		{"shortKey", shortKeyDeclUnserved},
		{"nameForIdentity", nameForIdentityDecl},
		{"orderedByLabel", orderedByLabelDecl},
		{"chargeLinesBlock", chargeLinesBlockDecl},
		{"houseLimitLine", houseLimitLineDecl},
		{"localDateTime", localDateTimeDecl},
		{"unservedLineCount", unservedLineCountDecl},
		{"unservedSettleHint", unservedSettleHintDecl},
		{"settleButtonAttrs", settleButtonAttrsDecl},
		{"renderOpenTabCard", renderOpenTabCardDecl},
	} {
		mustEvalDecl(t, vm, text, d.name, d.re)
	}
	fn, ok := goja.AssertFunction(vm.Get("renderOpenTabCard"))
	if !ok {
		t.Fatal("renderOpenTabCard is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestRenderOpenTabCard_SettleDisabledOnUnservedLineElseEnabled evaluates the
// POS card's ACTUAL rendered markup: one to-make line disables settle-btn/
// settle-pay-btn with the hint paragraph present; the same line marked
// served renders both enabled with no hint.
func TestRenderOpenTabCard_SettleDisabledOnUnservedLineElseEnabled(t *testing.T) {
	vm, fn := renderOpenTabCardVM(t)
	toMake := map[string]any{
		"tabKey": "vtx.tab.a", "totalCents": 450.0, "openedAt": "2026-09-18T10:00:00Z",
		"lines": []map[string]any{
			{"id": "line-1", "description": "Latte", "amountCents": 450, "orderedAt": "2026-09-18T12:00:00Z"},
		},
	}
	res, err := fn(goja.Undefined(), vm.ToValue(toMake), goja.Undefined(), goja.Undefined())
	if err != nil {
		t.Fatalf("renderOpenTabCard threw: %v", err)
	}
	html := res.String()
	if !strings.Contains(html, `id="settle-btn" class="danger" disabled title="Mark served or void every unserved order first"`) {
		t.Errorf("one to-make line: want settle-btn disabled, got:\n%s", html)
	}
	if !strings.Contains(html, `id="settle-pay-btn" class="danger" disabled title="Mark served or void every unserved order first"`) {
		t.Errorf("one to-make line: want settle-pay-btn disabled, got:\n%s", html)
	}
	if !strings.Contains(html, "1 order to make — mark served or void first") {
		t.Errorf("one to-make line: want the singular hint paragraph, got:\n%s", html)
	}

	served := map[string]any{
		"tabKey": "vtx.tab.a", "totalCents": 450.0, "openedAt": "2026-09-18T10:00:00Z",
		"lines": []map[string]any{
			{"id": "line-1", "description": "Latte", "amountCents": 450, "orderedAt": "2026-09-18T12:00:00Z", "servedAt": "2026-09-18T12:05:00Z"},
		},
	}
	res, err = fn(goja.Undefined(), vm.ToValue(served), goja.Undefined(), goja.Undefined())
	if err != nil {
		t.Fatalf("renderOpenTabCard threw: %v", err)
	}
	html = res.String()
	if !strings.Contains(html, `id="settle-btn" class="danger">Settle Tab</button>`) {
		t.Errorf("line served: want settle-btn enabled, got:\n%s", html)
	}
	if strings.Contains(html, "disabled") {
		t.Errorf("line served: want no disabled attribute anywhere, got:\n%s", html)
	}
	if strings.Contains(html, "to make — mark served or void first") {
		t.Errorf("line served: want no hint paragraph, got:\n%s", html)
	}
}

func frontDeskCardVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)
	vm := goja.New()
	if _, err := vm.RunString(`const state = { identities: [] };`); err != nil {
		t.Fatalf("state stub eval: %v", err)
	}
	for _, d := range []struct {
		name string
		re   *regexp.Regexp
	}{
		{"money", moneyDeclUnserved},
		{"escapeHtml", escapeHtmlDecl},
		{"itemsMemoLine", itemsMemoLineDecl},
		{"idOf", idOfDeclUnserved},
		{"shortKey", shortKeyDeclUnserved},
		{"nameForIdentity", nameForIdentityDecl},
		{"orderedByLabel", orderedByLabelDecl},
		{"chargeLinesBlock", chargeLinesBlockDecl},
		{"localDateTime", localDateTimeDecl},
		{"frontDeskBalanceBadge", frontDeskBalanceBadgeDecl},
		{"unservedLineCount", unservedLineCountDecl},
		{"unservedSettleHint", unservedSettleHintDecl},
		{"settleButtonAttrs", settleButtonAttrsDecl},
		{"frontDeskCard", frontDeskCardDecl},
	} {
		mustEvalDecl(t, vm, text, d.name, d.re)
	}
	fn, ok := goja.AssertFunction(vm.Get("frontDeskCard"))
	if !ok {
		t.Fatal("frontDeskCard is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestFrontDeskCard_SettleDisabledOnUnservedLineElseEnabled is
// TestRenderOpenTabCard_SettleDisabledOnUnservedLineElseEnabled's Front Desk
// twin, over the actual frontDeskCard markup.
func TestFrontDeskCard_SettleDisabledOnUnservedLineElseEnabled(t *testing.T) {
	vm, fn := frontDeskCardVM(t)
	toMake := map[string]any{
		"tabKey": "vtx.tab.a", "leaseAppKey": "vtx.leaseapp.a", "totalCents": 450.0, "openedAt": "2026-09-18T10:00:00Z",
		"lines": []map[string]any{
			{"id": "line-1", "description": "Latte", "amountCents": 450, "orderedAt": "2026-09-18T12:00:00Z"},
		},
	}
	res, err := fn(goja.Undefined(), vm.ToValue(toMake), goja.Null(), goja.Null(), goja.Null(), goja.Null(), goja.Null())
	if err != nil {
		t.Fatalf("frontDeskCard threw: %v", err)
	}
	html := res.String()
	if !strings.Contains(html, `class="danger" disabled title="Mark served or void every unserved order first">Settle</button>`) {
		t.Errorf("one to-make line: want Settle disabled, got:\n%s", html)
	}
	if !strings.Contains(html, "1 order to make — mark served or void first") {
		t.Errorf("one to-make line: want the singular hint paragraph, got:\n%s", html)
	}

	served := map[string]any{
		"tabKey": "vtx.tab.a", "leaseAppKey": "vtx.leaseapp.a", "totalCents": 450.0, "openedAt": "2026-09-18T10:00:00Z",
		"lines": []map[string]any{
			{"id": "line-1", "description": "Latte", "amountCents": 450, "orderedAt": "2026-09-18T12:00:00Z", "servedAt": "2026-09-18T12:05:00Z"},
		},
	}
	res, err = fn(goja.Undefined(), vm.ToValue(served), goja.Null(), goja.Null(), goja.Null(), goja.Null(), goja.Null())
	if err != nil {
		t.Fatalf("frontDeskCard threw: %v", err)
	}
	html = res.String()
	if !strings.Contains(html, `class="danger">Settle</button>`) {
		t.Errorf("line served: want Settle enabled, got:\n%s", html)
	}
	if strings.Contains(html, "disabled") {
		t.Errorf("line served: want no disabled attribute anywhere, got:\n%s", html)
	}
}

func residentSettlePanelMarkupVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)
	vm := goja.New()
	mustEvalDecl(t, vm, text, "escapeHtml", escapeHtmlDecl)
	mustEvalDecl(t, vm, text, "settleButtonAttrs", settleButtonAttrsDecl)
	mustEvalDecl(t, vm, text, "residentSettlePanelMarkup", residentSettlePanelMarkupDecl)
	fn, ok := goja.AssertFunction(vm.Get("residentSettlePanelMarkup"))
	if !ok {
		t.Fatal("residentSettlePanelMarkup is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestResidentSettlePanelMarkup_DisabledOnUnservedLineElseEnabled is the
// resident panel's own twin: renderResident calls this pure function
// directly for its "Settle My Tab" action, so pinning it IS pinning what the
// resident's card renders.
func TestResidentSettlePanelMarkup_DisabledOnUnservedLineElseEnabled(t *testing.T) {
	vm, fn := residentSettlePanelMarkupVM(t)
	res, err := fn(goja.Undefined(), vm.ToValue(1))
	if err != nil {
		t.Fatalf("residentSettlePanelMarkup threw: %v", err)
	}
	html := res.String()
	if !strings.Contains(html, `id="resident-settle-btn" class="danger" disabled title="Your order is still being made — the desk can settle or cancel it"`) {
		t.Errorf("count=1: want resident-settle-btn disabled, got:\n%s", html)
	}
	if !strings.Contains(html, "Your order is still being made — the desk can settle or cancel it</p>") {
		t.Errorf("count=1: want the hint paragraph, got:\n%s", html)
	}

	res, err = fn(goja.Undefined(), vm.ToValue(0))
	if err != nil {
		t.Fatalf("residentSettlePanelMarkup threw: %v", err)
	}
	html = res.String()
	if !strings.Contains(html, `id="resident-settle-btn" class="danger">Settle My Tab</button>`) {
		t.Errorf("count=0: want resident-settle-btn enabled, got:\n%s", html)
	}
	if strings.Contains(html, "disabled") || strings.Contains(html, "<p class=\"meta\">") {
		t.Errorf("count=0: want no disabled attribute and no hint paragraph, got:\n%s", html)
	}
}
