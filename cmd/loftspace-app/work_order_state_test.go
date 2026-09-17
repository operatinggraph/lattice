package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// workOrderStateDecl lifts the shipped workOrderState declaration out of the
// embedded app.js — the leaseTermUIDecls / rotate_offer_test.go pattern: the
// REAL shipped source runs here, not a copy. Self-contained (no DOM/state),
// so goja can evaluate it directly.
var workOrderStateDecl = regexp.MustCompile(`(?s)\nfunction workOrderState\(row\) \{\n.*?\n\}\n`)

func workOrderStateVM(t *testing.T) *goja.Runtime {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := workOrderStateDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level workOrderState declaration — the extraction regex no longer matches this file")
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped declaration: %v", err)
	}
	return vm
}

// TestWorkOrderState pins the tri-state precedence (docs/reviews/loftspace-maintenance-loop-2026-09-17.md
// decision 5): resolved wins over queued (a resolved order with a straggling
// open task still reads resolved), queued wins over unqueued, and a bare
// reported order with no task and no resolution reads unqueued.
func TestWorkOrderState(t *testing.T) {
	vm := workOrderStateVM(t)
	fn, ok := goja.AssertFunction(vm.Get("workOrderState"))
	if !ok {
		t.Fatal("workOrderState is not a function after evaluating its declaration")
	}
	for _, tc := range []struct {
		name string
		row  map[string]interface{}
		want string
	}{
		{"resolved, no open task", map[string]interface{}{"resolvedAt": "2026-07-30T11:00:00Z", "openTaskCount": 0}, "resolved"},
		{"resolved wins over a straggling open task", map[string]interface{}{"resolvedAt": "2026-07-30T11:00:00Z", "openTaskCount": 1}, "resolved"},
		{"queued: an open task, no resolution", map[string]interface{}{"openTaskCount": 1}, "queued"},
		{"unqueued: reported, no task, no resolution", map[string]interface{}{"openTaskCount": 0}, "unqueued"},
		{"unqueued: no fields at all", map[string]interface{}{}, "unqueued"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := fn(goja.Undefined(), vm.ToValue(tc.row))
			if err != nil {
				t.Fatalf("workOrderState: %v", err)
			}
			if got := v.String(); got != tc.want {
				t.Fatalf("workOrderState(%+v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}
