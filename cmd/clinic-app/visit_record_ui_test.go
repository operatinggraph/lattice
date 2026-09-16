package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// visitRecordUIDecls lifts the shipped encounterSummary/selfConfirmOffered
// declarations out of the embedded app.js — the clinic-visit-record-integrity
// fire's two FE predicates (the amended-note summary line, and the self card's
// Confirm gate). Self-contained by construction (no DOM/state), so the REAL
// shipped source runs here; mirrors lifecycle_transitions_test.go's extraction.
var visitRecordUIDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction encounterSummary\(a\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction selfConfirmOffered\(status, clock\) \{\n.*?\n\}\n`),
}

func visitRecordUIVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	vm := goja.New()
	for _, re := range visitRecordUIDecls {
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

// TestEncounterSummary pins the amended-visit half of the FE's operational
// summary line (clinic-visit-record-integrity-2026-09-16.md verdict 3):
// undocumented renders nothing, a first documented visit renders no "amended"
// clause, and a later amendment appends " · amended <date>" — the LOCALE date
// of amendedAt, the same isNaN-guarded shape documentedAt already renders in
// —  alongside an existing follow-up clause, in order. Node's ICU (which goja
// defers to for Date#toLocaleDateString) renders the default locale as
// M/D/YYYY zero-padded to MM/DD/YYYY, matching lifecycle_transitions_test.go's
// sibling pins; a garbage amendedAt drops the whole clause rather than
// rendering raw text.
func TestEncounterSummary(t *testing.T) {
	vm := visitRecordUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("encounterSummary"))
	if !ok {
		t.Fatal("encounterSummary is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, row map[string]interface{}) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(row))
		if err != nil {
			t.Fatalf("encounterSummary(%v) threw: %v", row, err)
		}
		return res.String()
	}
	for _, tc := range []struct {
		name string
		row  map[string]interface{}
		want string
	}{
		{
			"undocumented visit",
			map[string]interface{}{},
			"",
		},
		{
			"documented, never amended",
			map[string]interface{}{"documentedAt": "2026-09-10T15:00:00Z"},
			"✓ Visit documented · 09/10/2026",
		},
		{
			"documented and amended",
			map[string]interface{}{"documentedAt": "2026-09-10T15:00:00Z", "amendedAt": "2026-09-16T09:30:00Z"},
			"✓ Visit documented · 09/10/2026 · amended 09/16/2026",
		},
		{
			"amended visit with a follow-up — amended clause comes last",
			map[string]interface{}{
				"documentedAt":      "2026-09-10T15:00:00Z",
				"amendedAt":         "2026-09-16T09:30:00Z",
				"followUpRequested": true,
				"followUpDate":      "2026-10-01T00:00:00Z",
			},
			"✓ Visit documented · 09/10/2026 · follow-up 2026-10-01 · amended 09/16/2026",
		},
		{
			"garbage amendedAt drops the clause, not rendered raw",
			map[string]interface{}{"documentedAt": "2026-09-10T15:00:00Z", "amendedAt": "not-a-date"},
			"✓ Visit documented · 09/10/2026",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.row); got != tc.want {
				t.Errorf("encounterSummary(%v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}

// TestSelfConfirmOffered pins the self card's Confirm gate (verdict 7): a
// patient may confirm their own visit only while it is still at "scheduled"
// (or the never-actually-projected absent value, treated the same) and the
// visit has not started — "late" (inside the 24h reminder window) still
// offers it, matching the server's own self_visit_clock gate (VisitStarted
// only fires at "started"). Any other status (confirmed, checkedIn, and every
// terminal value) withholds it regardless of clock.
func TestSelfConfirmOffered(t *testing.T) {
	vm := visitRecordUIVM(t)
	fn, ok := goja.AssertFunction(vm.Get("selfConfirmOffered"))
	if !ok {
		t.Fatal("selfConfirmOffered is not a function after evaluating its declaration")
	}
	run := func(t *testing.T, status, clock string) bool {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(status), vm.ToValue(clock))
		if err != nil {
			t.Fatalf("selfConfirmOffered(%q, %q) threw: %v", status, clock, err)
		}
		return res.ToBoolean()
	}
	for _, tc := range []struct {
		status string
		clock  string
		want   bool
	}{
		{"scheduled", "open", true},
		{"scheduled", "late", true},
		{"scheduled", "started", false},
		{"confirmed", "open", false},
		{"confirmed", "late", false},
		{"confirmed", "started", false},
		{"checkedIn", "open", false},
		{"checkedIn", "late", false},
		{"checkedIn", "started", false},
		{"", "open", true},
	} {
		name := tc.status + "/" + tc.clock
		if tc.status == "" {
			name = "(absent)/" + tc.clock
		}
		t.Run(name, func(t *testing.T) {
			if got := run(t, tc.status, tc.clock); got != tc.want {
				t.Errorf("selfConfirmOffered(%q, %q) = %v, want %v", tc.status, tc.clock, got, tc.want)
			}
		})
	}
}
