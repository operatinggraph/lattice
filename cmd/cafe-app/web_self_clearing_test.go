package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// The self-clearing courtesy — nobody clears their own debt from the desk.
// packages/cafe-ledger refuses a staffer's write-off, refund or payout of
// their own account SelfClearing; the Front Desk arrears grid withholds the
// Write off / Pay out buttons on the viewer's own row and says why. Both
// halves the grid leans on are pure and are lifted out of the embedded app.js
// so the pins run the REAL source: isOwnAccount (the row-is-mine predicate)
// and arrearsRowMarkup (the row renderer), plus the helpers the renderer
// calls (money, escapeHtml, frontDeskArrearsLine) and idOf (idOfDecl,
// web_counter_payment_test.go).
var (
	isOwnAccountDecl     = regexp.MustCompile(`(?s)\nfunction isOwnAccount\(bookerKey\) \{\n.*?\n\}\n`)
	arrearsRowMarkupDecl = regexp.MustCompile(`(?s)\nfunction arrearsRowMarkup\(row, who, own\) \{\n.*?\n\}\n`)
)

// selfClearingVM evaluates the shipped declarations into a fresh goja runtime
// with a minimal `state` global (isOwnAccount reads state.identityId and
// nothing else) and returns the runtime.
func selfClearingVM(t *testing.T, identityID string) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)
	vm := goja.New()
	if _, err := vm.RunString(`var state = { identityId: ` + identityLiteral(identityID) + ` };`); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	for _, d := range []struct {
		name string
		re   *regexp.Regexp
	}{
		{"idOf", idOfDecl},
		{"isOwnAccount", isOwnAccountDecl},
		{"arrearsRowMarkup", arrearsRowMarkupDecl},
		{"money", moneyDecl},
		{"escapeHtml", escapeHTMLDecl},
		{"frontDeskArrearsLine", frontDeskArrearsLineDecl},
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
	return vm
}

// identityLiteral renders s as a JS string literal, or `null` for the empty
// string, so a test can seed state.identityId as a signed-out page would leave
// it.
func identityLiteral(s string) string {
	if s == "" {
		return "null"
	}
	return `"` + s + `"`
}

func callBool(t *testing.T, vm *goja.Runtime, expr string) bool {
	t.Helper()
	v, err := vm.RunString(expr)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return v.ToBoolean()
}

// TestIsOwnAccount pins the predicate: true only when the roster row's
// bookerKey names the signed-in identity; false for another resident, for a
// row with no bookerKey (an unresolved roster join), and for a page whose
// whoami never answered — an absent side must never withhold a button.
func TestIsOwnAccount(t *testing.T) {
	vm := selfClearingVM(t, "BBCAFEVEWERHJKMNPQRS")
	if !callBool(t, vm, `isOwnAccount("vtx.identity.BBCAFEVEWERHJKMNPQRS")`) {
		t.Fatal("isOwnAccount(the viewer's own key) = false, want true")
	}
	if callBool(t, vm, `isOwnAccount("vtx.identity.BBCAFEQTHERSHJKMNPQR")`) {
		t.Fatal("isOwnAccount(another resident's key) = true, want false")
	}
	if callBool(t, vm, `isOwnAccount(undefined)`) || callBool(t, vm, `isOwnAccount("")`) {
		t.Fatal("isOwnAccount(no bookerKey) = true, want false — an unresolved roster row is not the viewer's")
	}

	signedOut := selfClearingVM(t, "")
	if callBool(t, signedOut, `isOwnAccount("vtx.identity.BBCAFEVEWERHJKMNPQRS")`) {
		t.Fatal("isOwnAccount with state.identityId null = true, want false — an unanswered whoami must not hide a button")
	}
}

func rowMarkup(t *testing.T, vm *goja.Runtime, rowJSON string, own bool) string {
	t.Helper()
	ownLit := "false"
	if own {
		ownLit = "true"
	}
	v, err := vm.RunString(`arrearsRowMarkup(` + rowJSON + `, "Sam Okafor", ` + ownLit + `)`)
	if err != nil {
		t.Fatalf("arrearsRowMarkup: %v", err)
	}
	return v.String()
}

const (
	debtorRow = `{"leaseAppKey":"vtx.leaseapp.a","accountKey":"vtx.cafeaccount.a","balanceCents":1850,"dueDate":"2026-08-01","isOverdue":true,"daysOverdue":32}`
	creditRow = `{"leaseAppKey":"vtx.leaseapp.b","accountKey":"vtx.cafeaccount.b","balanceCents":-900}`
	ownNote   = "your own account — another staffer clears it"
)

// TestArrearsRowMarkup_OwnRowWithholdsClearingVerbs pins the courtesy on the
// viewer's own row: a debtor row keeps Take payment (a payment is not a
// clearing verb) but draws no Write off; a credit row draws no Pay out; both
// carry the note naming the rule.
func TestArrearsRowMarkup_OwnRowWithholdsClearingVerbs(t *testing.T) {
	vm := selfClearingVM(t, "BBCAFEVEWERHJKMNPQRS")

	debtor := rowMarkup(t, vm, debtorRow, true)
	if !strings.Contains(debtor, `class="take-payment-btn"`) {
		t.Fatalf("own debtor row lost its Take payment button:\n%s", debtor)
	}
	if strings.Contains(debtor, "writeoff-debt-btn") || strings.Contains(debtor, "Write off") {
		t.Fatalf("own debtor row still draws Write off:\n%s", debtor)
	}
	if !strings.Contains(debtor, ownNote) {
		t.Fatalf("own debtor row carries no self-clearing note:\n%s", debtor)
	}

	credit := rowMarkup(t, vm, creditRow, true)
	if strings.Contains(credit, "payout-credit-btn") || strings.Contains(credit, "Pay out") {
		t.Fatalf("own credit row still draws Pay out:\n%s", credit)
	}
	if !strings.Contains(credit, ownNote) {
		t.Fatalf("own credit row carries no self-clearing note:\n%s", credit)
	}
}

// TestArrearsRowMarkup_OtherRowKeepsBothButtons is the pair the pin above is
// measured against: another resident's debtor row draws Take payment AND
// Write off, their credit row draws Pay out, and neither carries the note —
// a renderer that withheld the verbs everywhere would pass the own-row pin.
func TestArrearsRowMarkup_OtherRowKeepsBothButtons(t *testing.T) {
	vm := selfClearingVM(t, "BBCAFEVEWERHJKMNPQRS")

	debtor := rowMarkup(t, vm, debtorRow, false)
	for _, want := range []string{`class="take-payment-btn"`, `class="writeoff-debt-btn"`, `data-account="vtx.cafeaccount.a"`, `data-amount="1850"`} {
		if !strings.Contains(debtor, want) {
			t.Fatalf("other debtor row lacks %q:\n%s", want, debtor)
		}
	}
	if strings.Contains(debtor, ownNote) {
		t.Fatalf("other debtor row carries the self-clearing note:\n%s", debtor)
	}

	credit := rowMarkup(t, vm, creditRow, false)
	for _, want := range []string{`class="payout-credit-btn"`, `data-account="vtx.cafeaccount.b"`, `data-amount="900"`} {
		if !strings.Contains(credit, want) {
			t.Fatalf("other credit row lacks %q:\n%s", want, credit)
		}
	}
	if strings.Contains(credit, ownNote) {
		t.Fatalf("other credit row carries the self-clearing note:\n%s", credit)
	}
}
