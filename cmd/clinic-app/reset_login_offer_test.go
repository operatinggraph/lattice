package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// resetLoginOfferableDecl lifts the shipped resetLoginOfferable declaration
// out of the embedded app.js. It is the one predicate deciding whether the
// topbar offers a "Reset login" button for the patient currently in context —
// self-contained by construction (no reference to state/DOM), so extracting
// and running the REAL source is what makes the assertions below a statement
// about what ships rather than about a copy in this file. Mirrors
// cmd/loftspace-app's own rotateOfferableDecl / rotate_offer_test.go.
var resetLoginOfferableDecl = regexp.MustCompile(`(?s)\nfunction resetLoginOfferable\(row, isOperator\) \{\n.*?\n\}\n`)

func resetLoginOfferableVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := resetLoginOfferableDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function resetLoginOfferable(row, isOperator) {…}` declaration found — the extraction regex no longer matches this file")
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped resetLoginOfferable: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("resetLoginOfferable"))
	if !ok {
		t.Fatal("resetLoginOfferable is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestResetLoginOfferable_OperatorHatWithAnIdentityOnly is the gate on the
// "Reset login" offer: RevokeIdentityClaim is operator-only
// (packages/identity-domain/permissions.go) — the registrar who may have kept
// the claim secret is exactly the staff role this verb has to sit above, so
// offering the button to a front-desk (non-operator) session would only ever
// earn an AuthDenied — and a patient row with no identityKey has nothing to
// reset at all.
func TestResetLoginOfferable_OperatorHatWithAnIdentityOnly(t *testing.T) {
	run := func(t *testing.T, vm *goja.Runtime, fn goja.Callable, row map[string]interface{}, isOperator bool) bool {
		t.Helper()
		var rowArg goja.Value
		if row == nil {
			rowArg = goja.Undefined()
		} else {
			rowArg = vm.ToValue(row)
		}
		res, err := fn(goja.Undefined(), rowArg, vm.ToValue(isOperator))
		if err != nil {
			t.Fatalf("resetLoginOfferable(%v, %v) threw: %v", row, isOperator, err)
		}
		return res.ToBoolean()
	}

	for _, tc := range []struct {
		name       string
		row        map[string]interface{}
		isOperator bool
		want       bool
	}{
		{"operator + identity bound", map[string]interface{}{
			"patientKey":  "vtx.patient.AAAAAAAAAAAAAAAAAAAA",
			"name":        "Jordan Ellis",
			"identityKey": "vtx.identity.BBBBBBBBBBBBBBBBBBBB",
		}, true, true},
		{"operator + no identity bound", map[string]interface{}{
			"patientKey": "vtx.patient.AAAAAAAAAAAAAAAAAAAA",
			"name":       "Jordan Ellis",
		}, true, false},
		{"non-operator + identity bound", map[string]interface{}{
			"patientKey":  "vtx.patient.AAAAAAAAAAAAAAAAAAAA",
			"name":        "Jordan Ellis",
			"identityKey": "vtx.identity.BBBBBBBBBBBBBBBBBBBB",
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm, fn := resetLoginOfferableVM(t)
			got := run(t, vm, fn, tc.row, tc.isOperator)
			if got != tc.want {
				t.Errorf("resetLoginOfferable(row=%v, isOperator=%v) = %v, want %v", tc.row, tc.isOperator, got, tc.want)
			}
		})
	}

	t.Run("no row", func(t *testing.T) {
		vm, fn := resetLoginOfferableVM(t)
		if got := run(t, vm, fn, nil, true); got != false {
			t.Errorf("resetLoginOfferable(undefined, true) = %v, want false", got)
		}
	})
}
