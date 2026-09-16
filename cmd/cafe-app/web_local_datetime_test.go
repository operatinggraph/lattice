package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// localDateTime is the one formatter every card's openedAt / settledAt /
// startsAt runs through — lifted out of the embedded app.js the same way
// web_receipt_test.go lifts receiptLines.
var localDateTimeDecl = regexp.MustCompile(`(?s)\nfunction localDateTime\(iso\) \{\n.*?\n\}\n`)

func localDateTimeVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := localDateTimeDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function localDateTime(iso) {…}` declaration found — the extraction regex no longer matches this file")
	}
	if strings.Contains(decl, "document") || strings.Contains(decl, "window") {
		t.Fatalf("localDateTime reaches the DOM; it must stay a pure function of its argument:\n%s", decl)
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped localDateTime: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("localDateTime"))
	if !ok {
		t.Fatal("localDateTime is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestLocalDateTime_FormatsAnInstantAndDegradesToQuestionMark: a parseable
// RFC3339 stamp comes back as a rendered local date-time (never the raw
// ISO string), and an absent or malformed stamp renders "?".
func TestLocalDateTime_FormatsAnInstantAndDegradesToQuestionMark(t *testing.T) {
	vm, fn := localDateTimeVM(t)
	call := func(v any) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(v))
		if err != nil {
			t.Fatalf("localDateTime(%v) threw: %v", v, err)
		}
		return res.String()
	}
	const iso = "2026-09-16T12:37:47Z"
	got := call(iso)
	if got == iso || got == "?" || !strings.Contains(got, "2026") {
		t.Fatalf("localDateTime(%q) = %q, want a rendered local date-time carrying the year, never the raw stamp", iso, got)
	}
	for _, v := range []any{"", nil, "not-a-date"} {
		if got := call(v); got != "?" {
			t.Fatalf("localDateTime(%v) = %q, want \"?\"", v, got)
		}
	}
}

// TestCardTimestamps_AllRunThroughLocalDateTime pins that no card template
// renders openedAt / settledAt / startsAt raw: every one of those stamps on
// a POS, Front Desk or Resident card passes through localDateTime, the
// same way the due dates on the same cards pass through toLocaleDateString.
func TestCardTimestamps_AllRunThroughLocalDateTime(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	raw := regexp.MustCompile(`escapeHtml\((\w+\.)?(openedAt|settledAt|startsAt) \|\| "\?"\)`)
	if m := raw.FindAllString(string(src), -1); len(m) != 0 {
		t.Fatalf("app.js renders a card timestamp raw: %v — route it through localDateTime", m)
	}
	for _, want := range []string{
		"localDateTime(tab.openedAt)",
		"localDateTime(t.openedAt)",
		"localDateTime(open.openedAt)",
		"localDateTime(pendingSettled.settledAt)",
		"localDateTime(booking.startsAt)",
		"localDateTime(visit.startsAt)",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("app.js: %s is no longer rendered through localDateTime", want)
		}
	}
}
