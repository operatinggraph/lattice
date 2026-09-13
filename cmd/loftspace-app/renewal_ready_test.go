package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// renewalReadyDecls lifts the shipped profileOnFile + renewalReady declarations
// out of the embedded app.js — the predicate deciding whether a tenant's
// renewal card offers "Sign renewal" at all. Both are self-contained (no
// state/DOM), so the REAL source runs here rather than a copy, the
// rotate_offer_test.go pattern.
var renewalReadyDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction profileOnFile\(row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction renewalReady\(row\) \{\n.*?\n\}\n`),
}

func renewalReadyVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	vm := goja.New()
	for _, re := range renewalReadyDecls {
		decl := re.FindString(string(src))
		if decl == "" {
			t.Fatalf("app.js: no top-level declaration matching %s — the extraction regex no longer matches this file", re)
		}
		if _, err := vm.RunString(decl); err != nil {
			t.Fatalf("goja eval of the shipped declaration: %v", err)
		}
	}
	fn, ok := goja.AssertFunction(vm.Get("renewalReady"))
	if !ok {
		t.Fatal("renewalReady is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestRenewalReady_MirrorsSignRenewalsWriteGuard pins the card's readiness
// against every refusal SignRenewal's own script raises before it writes:
// ApplicationSignalsMissing (no profile — renewalsRead projects hasGuarantor
// null), NotReadyToSign (no terms), GuarantorNotVerified (a guarantor on file,
// unverified). A card that offered the button on any of these would only ever
// earn that refusal.
func TestRenewalReady_MirrorsSignRenewalsWriteGuard(t *testing.T) {
	const termsAt = "2026-09-05T22:57:14Z"
	const verifiedAt = "2026-09-06T00:00:00Z"
	for _, tc := range []struct {
		name string
		row  map[string]interface{}
		want bool
	}{
		{"no profile, terms set", map[string]interface{}{"hasGuarantor": nil, "termsSetAt": termsAt}, false},
		{"no profile key at all, terms set", map[string]interface{}{"termsSetAt": termsAt}, false},
		{"profile without guarantor, terms set", map[string]interface{}{"hasGuarantor": false, "termsSetAt": termsAt}, true},
		{"profile without guarantor, no terms", map[string]interface{}{"hasGuarantor": false, "termsSetAt": nil}, false},
		{"guarantor on file, unverified", map[string]interface{}{"hasGuarantor": true, "termsSetAt": termsAt, "guarantorVerifiedAt": nil}, false},
		{"guarantor on file, verified", map[string]interface{}{"hasGuarantor": true, "termsSetAt": termsAt, "guarantorVerifiedAt": verifiedAt}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm, fn := renewalReadyVM(t)
			res, err := fn(goja.Undefined(), vm.ToValue(tc.row))
			if err != nil {
				t.Fatalf("renewalReady(%v) threw: %v", tc.row, err)
			}
			if got := res.ToBoolean(); got != tc.want {
				t.Errorf("renewalReady(%v) = %v, want %v", tc.row, got, tc.want)
			}
		})
	}
}
