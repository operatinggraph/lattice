package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// menuOptions is the <option>/<optgroup> markup both the self-order and POS
// pickers render from (renderResident, renderOpenTabCard) — lifted out of
// the embedded app.js the same way web_hold_test.go lifts openTabGate, so
// the assertions below are about the markup that ships. money/escapeHtml
// ride along because menuOptions calls both.
var menuOptionsDecl = regexp.MustCompile(`(?s)\nfunction menuOptions\(items, remainingCents\) \{\n.*?\n\}\n`)

func menuOptionsVM(t *testing.T) (*goja.Runtime, goja.Callable) {
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
		{"escapeHtml", escapeHTMLDecl},
		{"menuOptions", menuOptionsDecl},
	} {
		decl := d.re.FindString(text)
		if decl == "" {
			t.Fatalf("app.js: no top-level `function %s(…) {…}` declaration found — the extraction regex no longer matches this file", d.name)
		}
		if strings.Contains(decl, "document") || strings.Contains(decl, "window") {
			t.Fatalf("%s reaches the DOM; it must stay a pure function of its arguments", d.name)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped %s: %v", d.name, err)
		}
	}
	fn, ok := goja.AssertFunction(vm.Get("menuOptions"))
	if !ok {
		t.Fatal("menuOptions is not a function after evaluating its declaration")
	}
	return vm, fn
}

func callMenuOptions(t *testing.T, vm *goja.Runtime, fn goja.Callable, items []map[string]any) string {
	t.Helper()
	res, err := fn(goja.Undefined(), vm.ToValue(items))
	if err != nil {
		t.Fatalf("menuOptions(%+v) threw: %v", items, err)
	}
	return res.String()
}

// TestMenuOptions_AvailableFirstSoldOutDisabledInOptgroup pins the picker
// order the desk's sold-out toggle depends on: available items render first
// (plain, enabled options — so a browser's default selection always lands on
// one that can actually be ordered), sold-out items render after inside one
// "Sold out today" optgroup, each disabled and suffixed " — sold out".
func TestMenuOptions_AvailableFirstSoldOutDisabledInOptgroup(t *testing.T) {
	vm, fn := menuOptionsVM(t)
	items := []map[string]any{
		{"menuItemKey": "vtx.menuitem.b", "name": "Latte", "priceCents": 450, "available": false},
		{"menuItemKey": "vtx.menuitem.a", "name": "Croissant", "priceCents": 350, "available": true},
	}
	got := callMenuOptions(t, vm, fn, items)

	croissantIdx := strings.Index(got, "vtx.menuitem.a")
	optgroupIdx := strings.Index(got, "<optgroup")
	latteIdx := strings.Index(got, "vtx.menuitem.b")
	if croissantIdx < 0 || optgroupIdx < 0 || latteIdx < 0 {
		t.Fatalf("menuOptions output missing an expected item or optgroup: %s", got)
	}
	if !(croissantIdx < optgroupIdx && optgroupIdx < latteIdx) {
		t.Errorf("want the available item before the optgroup before the sold-out item, got order croissant=%d optgroup=%d latte=%d in %s", croissantIdx, optgroupIdx, latteIdx, got)
	}
	if !strings.Contains(got, `<optgroup label="Sold out today">`) {
		t.Errorf("want a \"Sold out today\" optgroup, got %s", got)
	}
	if !strings.Contains(got, `value="vtx.menuitem.b" disabled`) {
		t.Errorf("want the sold-out option marked disabled, got %s", got)
	}
	if !strings.Contains(got, "Latte — $4.50 — sold out") {
		t.Errorf("want the sold-out option suffixed \" — sold out\", got %s", got)
	}
	if strings.Contains(got, `value="vtx.menuitem.a" disabled`) {
		t.Errorf("the available item must not be disabled, got %s", got)
	}
}

// TestMenuOptions_AllSoldOutStillRendersOptgroupNoPlainOptions proves the
// all-sold-out case: no plain option renders, but the disabled group still
// does — the caller (renderResident/renderOpenTabCard) reads this shape to
// decide whether to drop the submit button, not an empty-string special case
// from menuOptions itself.
func TestMenuOptions_AllSoldOutStillRendersOptgroupNoPlainOptions(t *testing.T) {
	vm, fn := menuOptionsVM(t)
	items := []map[string]any{
		{"menuItemKey": "vtx.menuitem.a", "name": "Croissant", "priceCents": 350, "available": false},
	}
	got := callMenuOptions(t, vm, fn, items)
	if !strings.Contains(got, `<optgroup label="Sold out today">`) {
		t.Errorf("want the sold-out optgroup even when every item is sold out, got %s", got)
	}
	if strings.Contains(got, `value="vtx.menuitem.a">`) {
		t.Errorf("want no plain (enabled, non-optgroup) option when every item is sold out, got %s", got)
	}
}

// TestMenuOptions_MissingAvailableFieldTreatedAsAvailable proves an item
// with no available field at all (a row carrying no such column) renders
// as an ordinary available option, not a sold-out one —
// the same "never toggled means available" default the package and the
// lens both carry.
func TestMenuOptions_MissingAvailableFieldTreatedAsAvailable(t *testing.T) {
	vm, fn := menuOptionsVM(t)
	items := []map[string]any{
		{"menuItemKey": "vtx.menuitem.a", "name": "Croissant", "priceCents": 350},
	}
	got := callMenuOptions(t, vm, fn, items)
	if strings.Contains(got, "optgroup") || strings.Contains(got, "disabled") {
		t.Errorf("want an item with no available field treated as available, got %s", got)
	}
}

// TestMenuOptions_EscapesItemName proves a menu item name containing markup
// is escaped in both the plain-option and optgroup branches — the same
// stored-XSS gate escapeHTMLVM's own test guards, exercised through
// menuOptions specifically since it builds its own option markup rather than
// delegating wholesale to chargeLinesBlock.
func TestMenuOptions_EscapesItemName(t *testing.T) {
	vm, fn := menuOptionsVM(t)
	items := []map[string]any{
		{"menuItemKey": "vtx.menuitem.a", "name": "<script>alert(1)</script>", "priceCents": 100, "available": true},
		{"menuItemKey": "vtx.menuitem.b", "name": "<img src=x onerror=y>", "priceCents": 200, "available": false},
	}
	got := callMenuOptions(t, vm, fn, items)
	if strings.Contains(got, "<script>") || strings.Contains(got, "onerror=y>") {
		t.Errorf("menuOptions must escape item names in both branches, got %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("want the available-branch name escaped, got %s", got)
	}
	if !strings.Contains(got, "&lt;img src=x onerror=y&gt;") {
		t.Errorf("want the sold-out-branch name escaped, got %s", got)
	}
}
