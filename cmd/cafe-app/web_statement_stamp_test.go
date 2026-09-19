package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestStatementPostedAt_RunsThroughLocalDateTime pins that the café
// statement's posted-charge/refund rows never render postedAt raw — the
// same guard web_local_datetime_test.go's TestCardTimestamps_AllRunThrough
// LocalDateTime applies to a tab's openedAt/settledAt/startsAt, extended to
// the ledger list's own instant. A raw `escapeHtml(r.postedAt)` or
// `escapeHtml(reversed.postedAt)` would print a bare "2026-09-17T18:04:22Z"
// beside the due date and "Opened Sep 17, 2026, 1:40 PM", both already
// localized.
//
// The `data-posted` attribute (built from `escapeHtml(r.postedAt || "")`)
// is exempted from the raw-render check below, but it is NOT a "never
// display" field: the refund dialog's own click handler reads it back with
// `btn.getAttribute("data-posted")` and shows it in "Refunding the charge
// of …" — that display site is checked separately here (through
// localDateTime), since the attribute itself must stay the raw, parseable
// instant for that handler to localize.
func TestStatementPostedAt_RunsThroughLocalDateTime(t *testing.T) {
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	text := string(src)

	raw := regexp.MustCompile(`escapeHtml\((r|reversed)\.postedAt(\s*\|\|\s*"")?\)`)
	dataPosted := regexp.MustCompile(`data-posted=`)
	for _, loc := range raw.FindAllStringIndex(text, -1) {
		start := loc[0]
		m := text[loc[0]:loc[1]]
		// data-posted="..." is the one sanctioned raw render — a data
		// attribute the refund dialog's click handler reads and localizes
		// on display, checked below. Checked at THIS match's own position,
		// not the first occurrence of the matched text anywhere in the
		// file, since `escapeHtml(r.postedAt || "")` itself repeats.
		ctxStart := max(0, start-20)
		if dataPosted.MatchString(text[ctxStart:start]) {
			continue
		}
		t.Fatalf("app.js renders a statement postedAt raw at byte %d: %q — route it through localDateTime", start, m)
	}

	for _, want := range []string{
		"localDateTime(r.postedAt)",
		"localDateTime(reversed.postedAt)",
		`localDateTime(btn.getAttribute("data-posted"))`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("app.js: %s is no longer rendered through localDateTime", want)
		}
	}
}
