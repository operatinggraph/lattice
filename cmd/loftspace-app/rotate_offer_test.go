package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// rotateOfferableDecl lifts the shipped rotateOfferable declaration out of the
// embedded app.js. It is the one predicate deciding whether the Applicants &
// tenants panel offers a "Re-issue secret" button on a roster row — self-
// contained by construction (no reference to state/DOM), so extracting and
// running the REAL source is what makes the assertions below a statement
// about what ships rather than about a copy in this file.
var rotateOfferableDecl = regexp.MustCompile(`(?s)\nfunction rotateOfferable\(identity, rotateWorks\) \{\n.*?\n\}\n`)

func rotateOfferableVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := rotateOfferableDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no top-level `function rotateOfferable(identity, rotateWorks) {…}` declaration found — the extraction regex no longer matches this file")
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped rotateOfferable: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("rotateOfferable"))
	if !ok {
		t.Fatal("rotateOfferable is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestRotateOfferable_OnlyUnclaimedWithAWorkingDescriptor is the gate on the
// "Re-issue secret" offer: RotateClaimKey's own dispatch.visibleWhen (the
// unclaimed-only guard the Processor script enforces too) means offering the
// button on a claimed or merged identity would only ever earn a rejection,
// and offering it before the catalog + shared descriptorform module can
// actually render the op would open a broken form.
func TestRotateOfferable_OnlyUnclaimedWithAWorkingDescriptor(t *testing.T) {
	run := func(t *testing.T, vm *goja.Runtime, fn goja.Callable, identity map[string]interface{}, rotateWorks bool) bool {
		t.Helper()
		var identityArg goja.Value
		if identity == nil {
			identityArg = goja.Undefined()
		} else {
			identityArg = vm.ToValue(identity)
		}
		res, err := fn(goja.Undefined(), identityArg, vm.ToValue(rotateWorks))
		if err != nil {
			t.Fatalf("rotateOfferable(%v, %v) threw: %v", identity, rotateWorks, err)
		}
		return res.ToBoolean()
	}

	for _, tc := range []struct {
		name       string
		state      string
		rotateWork bool
		want       bool
	}{
		{"unclaimed + working descriptor", "unclaimed", true, true},
		{"unclaimed + no descriptor yet", "unclaimed", false, false},
		{"claimed + working descriptor", "claimed", true, false},
		{"merged + working descriptor", "merged", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm, fn := rotateOfferableVM(t)
			got := run(t, vm, fn, map[string]interface{}{
				"identityKey": "vtx.identity.AAAAAAAAAAAAAAAAAAAA",
				"name":        "Jordan Ellis",
				"state":       tc.state,
			}, tc.rotateWork)
			if got != tc.want {
				t.Errorf("rotateOfferable(state=%q, rotateWorks=%v) = %v, want %v", tc.state, tc.rotateWork, got, tc.want)
			}
		})
	}

	t.Run("no identity", func(t *testing.T) {
		vm, fn := rotateOfferableVM(t)
		if got := run(t, vm, fn, nil, true); got != false {
			t.Errorf("rotateOfferable(undefined, true) = %v, want false", got)
		}
	})
}

// identityStateClassDecl lifts identityStateClass — the whitelist standing
// between a roster row's raw `state` field and an element's class list — the
// same extract-and-run way as rotateOfferable above, plus the `const
// identityStateKnown = …` line right before it that the function's closure
// depends on.
var identityStateClassDecl = regexp.MustCompile(`(?s)\nconst identityStateKnown = .*?\nfunction identityStateClass\(rawState\) \{\n.*?\n\}\n`)

func identityStateClassVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	src, err := webFS.ReadFile("web/app.js")
	if err != nil {
		t.Fatalf("read embedded app.js: %v", err)
	}
	decl := identityStateClassDecl.FindString(string(src))
	if decl == "" {
		t.Fatal("app.js: no `const identityStateKnown = …` / `function identityStateClass(rawState) {…}` pair found — the extraction regex no longer matches this file")
	}
	vm := goja.New()
	if _, err := vm.RunString(decl); err != nil {
		t.Fatalf("goja eval of the shipped identityStateClass: %v", err)
	}
	fn, ok := goja.AssertFunction(vm.Get("identityStateClass"))
	if !ok {
		t.Fatal("identityStateClass is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestIdentityStateClass_WhitelistsTheThreeKnownStates is the gate on the
// roster badge's CSS class: only the three states identity-domain's lens
// ever projects (style.css's badge.unclaimed/claimed/merged) may reach the
// class list unchanged — anything else, including the values a state field
// can take when it is absent or malformed, must fall back to "unknown"
// rather than being written into `className` as-is.
func TestIdentityStateClass_WhitelistsTheThreeKnownStates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rawState interface{}
		want     string
	}{
		{"unclaimed", "unclaimed", "unclaimed"},
		{"claimed", "claimed", "claimed"},
		{"merged", "merged", "merged"},
		{"empty string", "", "unknown"},
		{"unrecognized value", "revoked", "unknown"},
		{"undefined", nil, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vm, fn := identityStateClassVM(t)
			var arg goja.Value
			if tc.rawState == nil {
				arg = goja.Undefined()
			} else {
				arg = vm.ToValue(tc.rawState)
			}
			res, err := fn(goja.Undefined(), arg)
			if err != nil {
				t.Fatalf("identityStateClass(%v) threw: %v", tc.rawState, err)
			}
			if got := res.String(); got != tc.want {
				t.Errorf("identityStateClass(%v) = %q, want %q", tc.rawState, got, tc.want)
			}
		})
	}
}
