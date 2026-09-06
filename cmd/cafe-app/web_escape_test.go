package main

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// escapeHtmlSource lifts the shipped escapeHtml declaration out of the
// embedded app.js. The whole file cannot be evaluated in goja — it is a
// browser script whose top level touches `document` — but this one function is
// self-contained by construction (its escape map is a local), so extracting it
// and running the REAL source is what makes the assertion below a statement
// about what ships rather than about a copy in this file.
var escapeHTMLDecl = regexp.MustCompile(`(?s)\nfunction escapeHtml\(s\) \{\n.*?\n\}\n`)

func escapeHTMLVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := escapeHTMLDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function escapeHtml(s) {…}` declaration found — the extraction regex no longer matches this file")
	}
	// A DOM-delegating helper (`d.textContent = s; return d.innerHTML`) is the
	// specific wrong answer this file guards, and it is unfalsifiable from Go:
	// innerHTML serializes a text node, which escapes `&`, `<` and `>` and
	// NEVER the quote characters, so such a helper is safe for text content and
	// broken for every attribute it is interpolated into. Refusing it here says
	// so, instead of skipping the assertions for want of a browser.
	if strings.Contains(decl, "document") {
		t.Fatalf("escapeHtml delegates to the DOM: a text node's innerHTML escapes & < > and never the quote characters, so a memo containing `\"` closes the data- attribute it sits in:\n%s", decl)
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped escapeHtml: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("escapeHtml"))
	if !ok {
		t.Fatal("escapeHtml is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestEscapeHtml_EscapesBothQuoteCharacters is the stored-XSS gate on the
// front desk. Every `data-…="…"` attribute in the ledger and menu markup is
// built by string concatenation around escapeHtml, and the values are free
// text a person typed — an off-menu charge description, a menu item name, a
// refund memo. A helper that escaped only the text-node characters (`&`, `<`,
// `>`) would let a value containing `"` close the attribute it sits in and
// open an event handler beside it, with no `<` anywhere in the payload.
func TestEscapeHtml_EscapesBothQuoteCharacters(t *testing.T) {
	vm, fn := escapeHTMLVM(t)

	call := func(t *testing.T, in string) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(in))
		if err != nil {
			t.Fatalf("escapeHtml(%q) threw: %v", in, err)
		}
		return res.String()
	}

	for _, tc := range []struct{ in, want string }{
		{`a"b'c<d>&`, `a&quot;b&#39;c&lt;d&gt;&amp;`},
		{"", ""},
		{"Flat white", "Flat white"},
		// The attribute-breakout vector itself: no angle bracket anywhere, so
		// a text-node-only escape leaves it byte-for-byte intact.
		{`x" onmouseover="alert(1)`, `x&quot; onmouseover=&quot;alert(1)`},
		// `&` first, so a naive sequential replace that escaped it last would
		// double-escape the entities it had just written.
		{`&amp;`, `&amp;amp;`},
	} {
		if got := call(t, tc.in); got != tc.want {
			t.Errorf("escapeHtml(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	for _, in := range []string{`" onload="x`, `' onload='x`, `<img src=x onerror=y>`} {
		got := call(t, in)
		if strings.ContainsAny(got, `"'<>`) {
			t.Errorf("escapeHtml(%q) = %q: a raw quote or angle bracket survived, so the value can break out of the attribute it is interpolated into", in, got)
		}
	}
}

// statementLineDecl and frontDeskArrearsLineDecl lift the shipped
// statementLine/frontDeskArrearsLine declarations out of the embedded
// app.js — both are self-contained (Date + string concatenation, no DOM),
// so running the REAL source is what makes the assertions below statements
// about what ships rather than about a copy in this file.
var statementLineDecl = regexp.MustCompile(`(?s)\nfunction statementLine\(ledger\) \{\n.*?\n\}\n`)
var frontDeskArrearsLineDecl = regexp.MustCompile(`(?s)\nfunction frontDeskArrearsLine\(row\) \{\n.*?\n\}\n`)

func reminderLinesVM(t *testing.T) (*goja.Runtime, goja.Callable, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)

	stmtDecl := statementLineDecl.FindString(text)
	if stmtDecl == "" {
		t.Fatal("app.js: no top-level `function statementLine(ledger) {…}` declaration found — the extraction regex no longer matches this file")
	}
	arrearsDecl := frontDeskArrearsLineDecl.FindString(text)
	if arrearsDecl == "" {
		t.Fatal("app.js: no top-level `function frontDeskArrearsLine(row) {…}` declaration found — the extraction regex no longer matches this file")
	}

	vm := goja.New()
	if _, err := vm.RunString(stmtDecl); err != nil {
		t.Fatalf("goja eval of the shipped statementLine: %v", err)
	}
	if _, err := vm.RunString(arrearsDecl); err != nil {
		t.Fatalf("goja eval of the shipped frontDeskArrearsLine: %v", err)
	}
	stmtFn, ok := goja.AssertFunction(vm.Get("statementLine"))
	if !ok {
		t.Fatal("statementLine is not a function after evaluating its declaration")
	}
	arrearsFn, ok := goja.AssertFunction(vm.Get("frontDeskArrearsLine"))
	if !ok {
		t.Fatal("frontDeskArrearsLine is not a function after evaluating its declaration")
	}
	return vm, stmtFn, arrearsFn
}

// localeDate renders isoDate through the VM's own toLocaleDateString, so the
// expectation below is pinned to whatever this runtime's date formatting
// actually is (goja's is not the same as a browser's) instead of a guessed
// literal — the assertion is that the rendered suffix carries THIS date, not
// that it matches one hardcoded string.
func localeDate(t *testing.T, vm *goja.Runtime, isoDate string) string {
	t.Helper()
	v, err := vm.RunString(`new Date(` + strconv.Quote(isoDate) + `).toLocaleDateString()`)
	if err != nil {
		t.Fatalf("format %q via the VM's own Date: %v", isoDate, err)
	}
	return v.String()
}

// TestStatementLineAndFrontDeskArrearsLine_RenderReminderSuffix proves both
// overdue banners say whether the café arrears reminder has gone out —
// "reminder sent <date>" rendering the ROW'S OWN reminderSentAt (not
// dueDate, not merely "some date"), and "no reminder sent yet" when the
// field is absent. A lease that isn't overdue gets neither: the reminder
// state has nothing to say until there is a due date to be late against.
func TestStatementLineAndFrontDeskArrearsLine_RenderReminderSuffix(t *testing.T) {
	vm, stmtFn, arrearsFn := reminderLinesVM(t)

	const dueDate = "2026-08-07T00:00:00Z"
	const reminderSentAt = "2026-08-12T00:00:00Z"
	sentDate := localeDate(t, vm, reminderSentAt)
	dueDateRendered := localeDate(t, vm, dueDate)
	if sentDate == dueDateRendered {
		t.Fatalf("fixture bug: reminderSentAt and dueDate render to the same locale date (%q) — the vector can't tell the two apart", sentDate)
	}

	call := func(t *testing.T, fn goja.Callable, obj map[string]any) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(obj))
		if err != nil {
			t.Fatalf("call(%+v) threw: %v", obj, err)
		}
		return res.String()
	}

	sent := map[string]any{"dueDate": dueDate, "isOverdue": true, "daysOverdue": 5, "reminderSentAt": reminderSentAt}
	if got := call(t, stmtFn, sent); !strings.Contains(got, "reminder sent "+sentDate) {
		t.Errorf("statementLine(overdue, reminderSentAt=%s) = %q, want it to render the row's own reminderSentAt (%s), not dueDate or a placeholder", reminderSentAt, got, sentDate)
	}

	unsent := map[string]any{"dueDate": dueDate, "isOverdue": true, "daysOverdue": 5}
	if got := call(t, stmtFn, unsent); !strings.Contains(got, "no reminder sent yet") {
		t.Errorf("statementLine(overdue, no reminderSentAt) = %q, want it to say no reminder sent yet", got)
	}

	notOverdue := map[string]any{"dueDate": "2026-09-11T00:00:00Z", "isOverdue": false}
	if got := call(t, stmtFn, notOverdue); strings.Contains(got, "reminder") {
		t.Errorf("statementLine(not overdue) = %q, want no reminder text at all (nothing is late yet)", got)
	}

	if got := call(t, arrearsFn, sent); !strings.Contains(got, "reminder sent "+sentDate) {
		t.Errorf("frontDeskArrearsLine(overdue, reminderSentAt=%s) = %q, want it to render the row's own reminderSentAt (%s)", reminderSentAt, got, sentDate)
	}
	if got := call(t, arrearsFn, unsent); !strings.Contains(got, "no reminder sent yet") {
		t.Errorf("frontDeskArrearsLine(overdue, no reminderSentAt) = %q, want it to say no reminder sent yet", got)
	}
}
