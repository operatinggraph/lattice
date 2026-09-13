package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// escDecl lifts the shipped esc declaration out of the embedded app.js. The
// whole file cannot be evaluated in goja — it is a browser script whose top
// level touches `document` — but this one function is self-contained by
// construction (its escape map is a local), so extracting it and running the
// REAL source is what makes the assertion below a statement about what ships
// rather than about a copy in this file.
var escDecl = regexp.MustCompile(`(?s)\nfunction esc\(s\) \{\n.*?\n\}\n`)

func escVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := escDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function esc(s) {…}` declaration found — the extraction regex no longer matches this file")
	}
	// A DOM-delegating helper (`d.textContent = s; return d.innerHTML`) is the
	// specific wrong answer this file guards, and it is unfalsifiable from Go:
	// innerHTML serializes a text node, which escapes `&`, `<` and `>` and
	// NEVER the quote characters, so such a helper is safe for text content and
	// broken for every attribute it is interpolated into. Refusing it here says
	// so, instead of skipping the assertions for want of a browser.
	if strings.Contains(decl, "document") {
		t.Fatalf("esc delegates to the DOM: a text node's innerHTML escapes & < > and never the quote characters, so a name containing `\"` closes the attribute it sits in:\n%s", decl)
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped esc: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("esc"))
	if !ok {
		t.Fatal("esc is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestEsc_EscapesBothQuoteCharacters is the stored-XSS gate on the schedule
// and roster. `<option value="…">` and `data-…="…"` attributes in the studio
// picker, instructor picker and roster markup are built by string
// concatenation around esc; today every one of them carries a server-minted
// key, but the same helper renders class, studio and instructor names — free
// text a staffer typed — and the first attribute a name reaches would be an
// attribute breakout under a helper that escaped only the text-node
// characters (`&`, `<`, `>`): a value containing `"` closes the attribute it
// sits in and opens an event handler beside it, with no `<` anywhere in the
// payload.
func TestEsc_EscapesBothQuoteCharacters(t *testing.T) {
	vm, fn := escVM(t)

	call := func(t *testing.T, in goja.Value) string {
		t.Helper()
		res, err := fn(goja.Undefined(), in)
		if err != nil {
			t.Fatalf("esc(%v) threw: %v", in, err)
		}
		return res.String()
	}

	for _, tc := range []struct{ in, want string }{
		{`a"b'c<d>&`, `a&quot;b&#39;c&lt;d&gt;&amp;`},
		{"", ""},
		{"Morning flow", "Morning flow"},
		// The attribute-breakout vector itself: no angle bracket anywhere, so
		// a text-node-only escape leaves it byte-for-byte intact.
		{`x" onmouseover="alert(1)`, `x&quot; onmouseover=&quot;alert(1)`},
		// `&` first, so a naive sequential replace that escaped it last would
		// double-escape the entities it had just written.
		{`&amp;`, `&amp;amp;`},
	} {
		if got := call(t, vm.ToValue(tc.in)); got != tc.want {
			t.Errorf("esc(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// The previous helper rendered null and undefined as "" (`s == null`);
	// the templates lean on that for optional fields, so the map shape keeps it.
	for _, in := range []goja.Value{goja.Null(), goja.Undefined()} {
		if got := call(t, in); got != "" {
			t.Errorf("esc(%v) = %q, want \"\"", in, got)
		}
	}

	for _, in := range []string{`" onload="x`, `' onload='x`, `<img src=x onerror=y>`} {
		got := call(t, vm.ToValue(in))
		if strings.ContainsAny(got, `"'<>`) {
			t.Errorf("esc(%q) = %q: a raw quote or angle bracket survived, so the value can break out of the attribute it is interpolated into", in, got)
		}
	}
}
