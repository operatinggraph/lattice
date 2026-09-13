package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// lifecycleTransitionsDecl lifts the shipped lifecycleTransitions declaration out
// of the embedded app.js — the one predicate deciding which SetAppointmentStatus
// buttons a card offers. Self-contained by construction (no state/DOM), so the
// REAL source runs here; mirrors reset_login_offer_test.go's extraction.
var lifecycleTransitionsDecl = regexp.MustCompile(`(?s)\nfunction lifecycleTransitions\(status, started\) \{\n.*?\n\}\n`)

func lifecycleTransitionsVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := lifecycleTransitionsDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function lifecycleTransitions(status, started) {…}` declaration found — the extraction regex no longer matches this file")
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped lifecycleTransitions: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("lifecycleTransitions"))
	if !ok {
		t.Fatal("lifecycleTransitions is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestLifecycleTransitions_CompleteAndNoShowWaitForStartsAt pins the FE mirror of
// SetAppointmentStatus's NotYetStarted guard (packages/clinic-domain/ddls.go): before
// the visit starts a card offers confirm / check-in only; from startsAt onward it
// offers complete / no-show too; a terminal status offers nothing either way.
func TestLifecycleTransitions_CompleteAndNoShowWaitForStartsAt(t *testing.T) {
	vm, fn := lifecycleTransitionsVM(t)
	run := func(status string, started bool) string {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(status), vm.ToValue(started))
		if err != nil {
			t.Fatalf("lifecycleTransitions(%q, %v) threw: %v", status, started, err)
		}
		var out []string
		if err := vm.ExportTo(res, &out); err != nil {
			t.Fatalf("lifecycleTransitions(%q, %v) did not return an array: %v", status, started, err)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct {
		status  string
		started bool
		want    string
	}{
		{"scheduled", false, "confirmed,checkedIn"},
		{"scheduled", true, "confirmed,checkedIn,completed,noShow"},
		{"confirmed", false, "checkedIn"},
		{"confirmed", true, "checkedIn,completed,noShow"},
		{"checkedIn", false, ""},
		{"checkedIn", true, "completed,noShow"},
		{"completed", true, ""},
		{"cancelled", true, ""},
		{"noShow", true, ""},
		{"", true, ""},
	} {
		if got := run(tc.status, tc.started); got != tc.want {
			t.Errorf("lifecycleTransitions(%q, started=%v) = [%s], want [%s]", tc.status, tc.started, got, tc.want)
		}
	}
}
