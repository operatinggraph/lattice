package main

import (
	"regexp"
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
