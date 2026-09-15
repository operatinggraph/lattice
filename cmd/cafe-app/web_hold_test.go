package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// The Open Tab gate and the picker badge, lifted out of the embedded app.js
// the same way web_today_test.go lifts summarizeToday: each is a pure
// function of a balance row (no DOM), so the assertions below are about the
// rule that ships, not a copy of it. money() rides along because
// arrearsBadge formats the amount through it.
var openTabGateDecl = regexp.MustCompile(`(?s)\nfunction openTabGate\(balance\) \{\n.*?\n\}\n`)
var overdueDaysPhraseDecl = regexp.MustCompile(`(?s)\nfunction overdueDaysPhrase\(balance\) \{\n.*?\n\}\n`)
var arrearsBadgeDecl = regexp.MustCompile(`(?s)\nfunction arrearsBadge\(balance\) \{\n.*?\n\}\n`)
var moneyDecl = regexp.MustCompile(`(?s)\nfunction money\(cents\) \{\n.*?\n\}\n`)

func openTabGateVM(t *testing.T) *goja.Runtime {
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
		{"openTabGate", openTabGateDecl},
		{"overdueDaysPhrase", overdueDaysPhraseDecl},
		{"arrearsBadge", arrearsBadgeDecl},
	} {
		decl := d.re.FindString(text)
		if decl == "" {
			t.Fatalf("app.js: no top-level `function %s(…) {…}` declaration found — the extraction regex no longer matches this file", d.name)
		}
		if strings.Contains(decl, "document") || strings.Contains(decl, "window") {
			t.Fatalf("%s reaches the DOM; it must stay a pure function of the balance row so the gate is testable:\n%s", d.name, decl)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped %s: %v", d.name, err)
		}
	}
	if _, err := vm.RunString(`
const none = undefined;
const overdueUnreminded = { balanceCents: 4750, dueDate: "2026-08-14T00:00:00Z", isOverdue: true, daysOverdue: 31 };
const overdueReminded = { balanceCents: 4750, dueDate: "2026-08-14T00:00:00Z", isOverdue: true, daysOverdue: 31, reminderSentAt: "2026-08-15T03:00:00Z" };
const inTermReminded = { balanceCents: 975, dueDate: "2026-09-20T00:00:00Z", isOverdue: false, daysOverdue: 0, reminderSentAt: "2026-08-15T03:00:00Z" };
const inTerm = { balanceCents: 975, dueDate: "2026-09-20T00:00:00Z", isOverdue: false, daysOverdue: 0 };
const credit = { balanceCents: -1200, dueDate: "", isOverdue: false, daysOverdue: 0 };
const oneDay = { balanceCents: 450, dueDate: "2026-09-13T00:00:00Z", isOverdue: true, daysOverdue: 1 };
`); err != nil {
		t.Fatalf("fixture eval: %v", err)
	}
	return vm
}

// TestOpenTabGate_HoldOnReminderConfirmOnOverdue pins the three-way rule the
// POS and the resident view share: a reminded episode is a hold whatever
// isOverdue says (the op refuses CreditHold on sentAt alone), an overdue
// balance nobody has reminded yet is a confirm, and everything else — no
// row, inside the term, in credit — opens.
func TestOpenTabGate_HoldOnReminderConfirmOnOverdue(t *testing.T) {
	vm := openTabGateVM(t)
	for expr, want := range map[string]string{
		"openTabGate(none)":              "open",
		"openTabGate(null)":              "open",
		"openTabGate(overdueUnreminded)": "confirm",
		"openTabGate(overdueReminded)":   "hold",
		"openTabGate(inTermReminded)":    "hold",
		"openTabGate(inTerm)":            "open",
		"openTabGate(credit)":            "open",
		"openTabGate(oneDay)":            "confirm",
	} {
		if got := jsString(t, vm, expr); got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}

// TestArrearsBadge_PickerSuffix pins the picker option's suffix: nothing
// when the gate is open, the amount plus the overdue count (singular at one
// day) on a confirm, and " · credit hold" appended on a hold — without a
// days phrase when the reminded balance sits inside its term.
func TestArrearsBadge_PickerSuffix(t *testing.T) {
	vm := openTabGateVM(t)
	for expr, want := range map[string]string{
		"arrearsBadge(none)":              "",
		"arrearsBadge(inTerm)":            "",
		"arrearsBadge(credit)":            "",
		"arrearsBadge(overdueUnreminded)": " · owes $47.50 · 31 days overdue",
		"arrearsBadge(overdueReminded)":   " · owes $47.50 · 31 days overdue · credit hold",
		"arrearsBadge(inTermReminded)":    " · owes $9.75 · credit hold",
		"arrearsBadge(oneDay)":            " · owes $4.50 · 1 day overdue",
	} {
		if got := jsString(t, vm, expr); got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}
