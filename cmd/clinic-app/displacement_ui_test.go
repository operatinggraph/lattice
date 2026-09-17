package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// displacementUIDecls lifts the shipped displacementLabel declaration out of
// the embedded app.js — the card line both hats render when the provider's
// time-off covers the visit (docs/reviews/clinic-time-off-displacement-2026-09-17.md
// verdict 7). Self-contained by construction (no DOM/state/clock), so the
// REAL shipped source runs here; mirrors status_clock_ui_test.go's
// extraction.
var displacementUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction displacementLabel\(a\) \{\n.*?\n\}\n`),
}

func displacementUIVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	vm := goja.New()
	for _, re := range displacementUIDecls {
		decl := re.FindString(string(src))
		if decl == "" {
			t.Fatalf("app.js: no top-level declaration matching %s — the extraction regex no longer matches this file", re)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped declaration %s: %v", re, err)
		}
	}
	return vm
}

// TestDisplacementLabel pins the card's displacement line over every shape
// the lens can project: displaced = true renders the line with the covering
// range and the reschedule promise; displaced = false (recorded clear) and a
// null verdict (no .displacement on the visit) both render nothing; a
// displaced row missing or carrying an unparseable range still renders the
// line, without the range. goja has no ICU: assert the prefix, the separator
// and the suffix, not the locale-rendered date/time itself
// (status_clock_ui_test.go's own note on this).
func TestDisplacementLabel(t *testing.T) {
	vm := displacementUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("displacementLabel"))
	if !ok {
		t.Fatal("displacementLabel is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, row map[string]interface{}) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(row))
		if err != nil {
			t.Fatalf("displacementLabel(%v) threw: %v", row, err)
		}
		return res.String()
	}
	const prefix = "⛔ Provider unavailable"
	const suffix = " — the clinic will reschedule"

	t.Run("displaced with range: the full line", func(t *testing.T) {
		row := map[string]interface{}{"status": "scheduled", "displaced": true, "displacedFrom": "2026-07-20T00:00:00Z", "displacedTo": "2026-07-21T00:00:00Z"}
		got := run(t, row)
		if !strings.HasPrefix(got, prefix+" · ") {
			t.Fatalf("displacementLabel(%v) = %q, want prefix %q", row, got, prefix+" · ")
		}
		if !strings.Contains(got, " – ") {
			t.Fatalf("displacementLabel(%v) = %q, want the from – to separator", row, got)
		}
		if !strings.HasSuffix(got, suffix) {
			t.Fatalf("displacementLabel(%v) = %q, want suffix %q", row, got, suffix)
		}
	})
	t.Run("displaced without a range: the line, no range", func(t *testing.T) {
		row := map[string]interface{}{"status": "scheduled", "displaced": true}
		if got, want := run(t, row), prefix+suffix; got != want {
			t.Fatalf("displacementLabel(%v) = %q, want %q", row, got, want)
		}
	})
	t.Run("displaced with an unparseable range: the line, no range", func(t *testing.T) {
		row := map[string]interface{}{"status": "scheduled", "displaced": true, "displacedFrom": "not-a-date", "displacedTo": "2026-07-21T00:00:00Z"}
		if got, want := run(t, row), prefix+suffix; got != want {
			t.Fatalf("displacementLabel(%v) = %q, want %q (never an Invalid Date)", row, got, want)
		}
	})
	t.Run("recorded clear: empty", func(t *testing.T) {
		row := map[string]interface{}{"status": "scheduled", "displaced": false}
		if got := run(t, row); got != "" {
			t.Fatalf("displacementLabel(%v) = %q, want empty", row, got)
		}
	})
	t.Run("no verdict (null): empty", func(t *testing.T) {
		row := map[string]interface{}{"status": "scheduled", "displaced": nil}
		if got := run(t, row); got != "" {
			t.Fatalf("displacementLabel(%v) = %q, want empty", row, got)
		}
	})
	t.Run("field absent: empty", func(t *testing.T) {
		row := map[string]interface{}{"status": "scheduled"}
		if got := run(t, row); got != "" {
			t.Fatalf("displacementLabel(%v) = %q, want empty", row, got)
		}
	})
	t.Run("a truthy non-boolean is not displaced", func(t *testing.T) {
		row := map[string]interface{}{"status": "scheduled", "displaced": "true"}
		if got := run(t, row); got != "" {
			t.Fatalf("displacementLabel(%v) = %q, want empty — only the boolean true is the recorded fact", row, got)
		}
	})
}

// TestRenderApptCardAppendsDisplacementLine pins that renderApptCard builds
// the line off displacementLabel and appends it for every hat: the card
// source computes it unconditionally (no opts.asSelf gate, unlike the desk's
// client-side conflict badge) and appends it when non-empty.
func TestRenderApptCardAppendsDisplacementLine(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	card := regexp.MustCompile(`(?s)\nfunction renderApptCard\(a, opts\) \{\n.*?\n\}\n`).FindString(string(src))
	if card == "" {
		t.Fatal("app.js: renderApptCard declaration not found")
	}
	if !strings.Contains(card, `displacement.textContent = displacementLabel(a);`) {
		t.Fatal("renderApptCard must compute the displacement line off displacementLabel(a), for every hat")
	}
	if !strings.Contains(card, `if (displacement.textContent) card.append(displacement);`) {
		t.Fatal("renderApptCard must append the displacement line when it renders")
	}
	if strings.Contains(card, `if (!opts.asSelf) displacement`) {
		t.Fatal("the recorded displacement line is the patient's too — it must not be gated on opts.asSelf")
	}
}
