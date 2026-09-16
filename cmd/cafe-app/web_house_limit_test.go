package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// The house tab limit's FE rules, lifted out of the embedded app.js the way
// web_hold_test.go lifts openTabGate: tabLimitOf / tabLimitRemaining /
// houseLimitLine are pure functions of a residents row and a total,
// menuOptions' bound is a pure function of the items and the room left, and
// housePolicyCurrentLine of a policy row. The assertions below are about the
// rule that ships, and the picker's "over the limit" test is the script's
// own TabLimitExceeded conjunct (totalCents + priceCents > limit,
// packages/cafe-domain/ddls.go) on the same item price the op derives.
var tabLimitOfDecl = regexp.MustCompile(`(?s)\nfunction tabLimitOf\(row\) \{\n.*?\n\}\n`)
var tabLimitRemainingDecl = regexp.MustCompile(`(?s)\nfunction tabLimitRemaining\(limitCents, totalCents\) \{\n.*?\n\}\n`)
var houseLimitLineDecl = regexp.MustCompile(`(?s)\nfunction houseLimitLine\(limitCents, totalCents\) \{\n.*?\n\}\n`)
var housePolicyCurrentLineDecl = regexp.MustCompile(`(?s)\nfunction housePolicyCurrentLine\(policy, locationKey, policies\) \{\n.*?\n\}\n`)
var parseDollarsOrZeroDecl = regexp.MustCompile(`(?s)\nfunction parseDollarsOrZero\(s\) \{\n.*?\n\}\n`)
var shortKeyDecl = regexp.MustCompile(`(?s)\nfunction shortKey\(key\) \{\n.*?\n\}\n`)

func houseLimitVM(t *testing.T) *goja.Runtime {
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
		{"shortKey", shortKeyDecl},
		{"tabLimitOf", tabLimitOfDecl},
		{"tabLimitRemaining", tabLimitRemainingDecl},
		{"houseLimitLine", houseLimitLineDecl},
		{"housePolicyCurrentLine", housePolicyCurrentLineDecl},
		{"parseDollarsOrZero", parseDollarsOrZeroDecl},
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
	return vm
}

func evalString(t *testing.T, vm *goja.Runtime, expr string) string {
	t.Helper()
	v, err := vm.RunString(expr)
	if err != nil {
		t.Fatalf("%s threw: %v", expr, err)
	}
	return v.String()
}

// TestTabLimitOf_NullIsNoLimitZeroIsClosed pins the null/0 distinction the
// residents join carries: a row with no policy on its chain reads as no
// limit (null), a $0 policy reads as 0 (self-service closed), never the
// other way round.
func TestTabLimitOf_NullIsNoLimitZeroIsClosed(t *testing.T) {
	vm := houseLimitVM(t)
	for expr, want := range map[string]string{
		`String(tabLimitOf(null))`:                    "null",
		`String(tabLimitOf(undefined))`:               "null",
		`String(tabLimitOf({ tabLimitCents: null }))`: "null",
		`String(tabLimitOf({}))`:                      "null",
		`String(tabLimitOf({ tabLimitCents: 0 }))`:    "0",
		`String(tabLimitOf({ tabLimitCents: 5000 }))`: "5000",
		`String(tabLimitRemaining(null, 450))`:        "null",
		`String(tabLimitRemaining(900, 450))`:         "450",
		`String(tabLimitRemaining(900, 900))`:         "0",
		`String(tabLimitRemaining(900, 1400))`:        "0",
		`String(parseDollarsOrZero("0"))`:             "0",
		`String(parseDollarsOrZero("50"))`:            "5000",
		`String(parseDollarsOrZero("12.34"))`:         "1234",
		`String(parseDollarsOrZero("12.345"))`:        "null",
		`String(parseDollarsOrZero("0.005"))`:         "null",
		`String(parseDollarsOrZero(""))`:              "null",
		`String(parseDollarsOrZero("-1"))`:            "null",
		`String(parseDollarsOrZero("abc"))`:           "null",
	} {
		if got := evalString(t, vm, expr); got != want {
			t.Errorf("%s = %s, want %s", expr, got, want)
		}
	}
}

// TestHouseLimitLine_UnderAtOver pins the card sentence in its three
// states, and that a house with no limit says nothing at all.
func TestHouseLimitLine_UnderAtOver(t *testing.T) {
	vm := houseLimitVM(t)
	for expr, want := range map[string]string{
		`houseLimitLine(null, 450)`: "",
		`houseLimitLine(900, 0)`:    "House limit $9.00 · $9.00 left",
		`houseLimitLine(900, 450)`:  "House limit $9.00 · $4.50 left",
		`houseLimitLine(900, 900)`:  "At the house limit of $9.00 — self-order is closed",
		`houseLimitLine(900, 1400)`: "Over the house limit of $9.00 — self-order is closed",
		`houseLimitLine(0, 0)`:      "At the house limit of $0.00 — self-order is closed",
	} {
		if got := evalString(t, vm, expr); got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}

// TestMenuOptions_OverLimitDisabled pins the picker courtesy for
// TabLimitExceeded: with $4.50 of room, a $4.50 item stays offered (equal
// is allowed, the op's own conjunct), a $4.75 one is disabled inside the
// "Over your tab limit" optgroup, a sold-out one stays in its own optgroup,
// and with no bound (null / undefined) nothing is disabled for price.
func TestMenuOptions_OverLimitDisabled(t *testing.T) {
	vm := houseLimitVM(t)
	if _, err := vm.RunString(`
const items = [
  { menuItemKey: "vtx.menuitem.a", name: "Latte", priceCents: 450, available: true },
  { menuItemKey: "vtx.menuitem.b", name: "Croissant", priceCents: 475, available: true },
  { menuItemKey: "vtx.menuitem.c", name: "Muffin", priceCents: 300, available: false },
];`); err != nil {
		t.Fatal(err)
	}
	bounded := evalString(t, vm, `menuOptions(items, 450)`)
	if !strings.Contains(bounded, `<option value="vtx.menuitem.a">Latte — $4.50</option>`) {
		t.Errorf("an item priced exactly at the room left must stay offered:\n%s", bounded)
	}
	if !strings.Contains(bounded, `<optgroup label="Over your tab limit"><option value="vtx.menuitem.b" disabled>Croissant — $4.75 — over the limit</option></optgroup>`) {
		t.Errorf("an item priced above the room left must be disabled inside the over-limit optgroup:\n%s", bounded)
	}
	if !strings.Contains(bounded, `<optgroup label="Sold out today">`) {
		t.Errorf("the sold-out optgroup must still render beside the over-limit one:\n%s", bounded)
	}
	zero := evalString(t, vm, `menuOptions(items, 0)`)
	if strings.Contains(zero, `<option value="vtx.menuitem.a">`) || strings.Contains(zero, `<option value="vtx.menuitem.b">`) {
		t.Errorf("with no room left every priced item must be disabled:\n%s", zero)
	}
	for _, expr := range []string{`menuOptions(items, null)`, `menuOptions(items)`} {
		unbounded := evalString(t, vm, expr)
		if strings.Contains(unbounded, "Over your tab limit") || !strings.Contains(unbounded, `<option value="vtx.menuitem.b">Croissant — $4.75</option>`) {
			t.Errorf("%s: with no bound nothing is disabled for price:\n%s", expr, unbounded)
		}
	}
}

// TestHousePolicyCurrentLine pins the Manage Menu panel's sentence: no
// policy, a closed house, a recorded limit, named by the location's
// presentation name when it has one — and the second clause naming a
// tighter policy elsewhere on the covered chains (the op applies the
// tightest, so "any total" under a property cap would be a false promise),
// silent when no other policy binds tighter.
func TestHousePolicyCurrentLine(t *testing.T) {
	vm := houseLimitVM(t)
	if _, err := vm.RunString(`
const estateCap = { locationKey: "vtx.property.p", tabLimitCents: 900, name: "The Estate" };
const estateClosed = { locationKey: "vtx.property.p", tabLimitCents: 0, name: "The Estate" };
const looser = { locationKey: "vtx.property.p", tabLimitCents: 9000, name: "The Estate" };
const own = { locationKey: "vtx.building.x", tabLimitCents: 5000, name: "Riverside" };
`); err != nil {
		t.Fatal(err)
	}
	for expr, want := range map[string]string{
		`housePolicyCurrentLine(null, "")`:                                                               "",
		`housePolicyCurrentLine(null, "vtx.building.BBCAFEHPQLBLDGHJKMNP")`:                              "No house tab limit recorded at BBCAFE…KMNP — residents may self-order any total.",
		`housePolicyCurrentLine({ tabLimitCents: 0, name: "Riverside" }, "vtx.building.x")`:              "Self-service tabs are closed at Riverside (limit $0.00).",
		`housePolicyCurrentLine({ tabLimitCents: 5000, name: "Riverside" }, "vtx.building.x")`:           "House tab limit at Riverside: $50.00 per tab.",
		`housePolicyCurrentLine({ tabLimitCents: 5000, name: "" }, "vtx.building.BBCAFEHPQLBLDGHJKMNP")`: "House tab limit at BBCAFE…KMNP: $50.00 per tab.",
		`housePolicyCurrentLine(null, "vtx.building.x", [estateCap])`:                                    "No house tab limit recorded at x — residents may self-order any total. A tighter limit of $9.00 at The Estate binds here.",
		`housePolicyCurrentLine(own, "vtx.building.x", [own, estateCap])`:                                "House tab limit at Riverside: $50.00 per tab. A tighter limit of $9.00 at The Estate binds here.",
		`housePolicyCurrentLine(own, "vtx.building.x", [own, estateClosed])`:                             "House tab limit at Riverside: $50.00 per tab. Self-service tabs are closed by the policy at The Estate (limit $0.00), which binds here.",
		`housePolicyCurrentLine(own, "vtx.building.x", [own, looser])`:                                   "House tab limit at Riverside: $50.00 per tab.",
	} {
		if got := evalString(t, vm, expr); got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}
