package main

import (
	"regexp"
	"testing"

	"github.com/dop251/goja"
)

// renewalReadyDecls lifts the shipped profileOnFile + renewalReady +
// renewalCardOffersLandlordActions declarations out of the embedded app.js
// — the predicates deciding whether a tenant's renewal card offers "Sign
// renewal" and whether a landlord's offers Set terms/Verify guarantor/
// Decline at all. All three are self-contained (no state/DOM), so the REAL
// source runs here rather than a copy, the rotate_offer_test.go pattern.
var renewalReadyDecls = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\nfunction profileOnFile\(row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction renewalReady\(row\) \{\n.*?\n\}\n`),
	regexp.MustCompile(`(?s)\nfunction renewalCardOffersLandlordActions\(row\) \{\n.*?\n\}\n`),
}

// renewalDeclsVM evaluates renewalReadyDecls into a fresh VM — the shared
// setup behind both renewalReadyVM and renewalCardOffersLandlordActionsVM.
func renewalDeclsVM(t *testing.T) *goja.Runtime {
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
	return vm
}

func renewalReadyVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	vm := renewalDeclsVM(t)
	fn, ok := goja.AssertFunction(vm.Get("renewalReady"))
	if !ok {
		t.Fatal("renewalReady is not a function after evaluating its declaration")
	}
	return vm, fn
}

func renewalCardOffersLandlordActionsVM(t *testing.T) (*goja.Runtime, goja.Callable) {
	t.Helper()
	vm := renewalDeclsVM(t)
	fn, ok := goja.AssertFunction(vm.Get("renewalCardOffersLandlordActions"))
	if !ok {
		t.Fatal("renewalCardOffersLandlordActions is not a function after evaluating its declaration")
	}
	return vm, fn
}

// TestRenewalReady_MirrorsSignRenewalsWriteGuard pins the card's readiness
// against every refusal SignRenewal's own script raises before it writes:
// ApplicationSignalsMissing (no profile — renewalsRead projects hasGuarantor
// null), NotReadyToSign (no terms), GuarantorNotVerified (a guarantor on file,
// unverified), TenancyEnded (the renewed application's .tenancy.endedAt
// recorded — renewalsRead's own tenancy_ended_at column, lease-signing
// renewal_lenses.go), and NoticeGiven (a recorded .notice.moveOutAt —
// renewalsRead's own notice_move_out_at column). A card that offered the
// button on any of these would only ever earn that refusal.
func TestRenewalReady_MirrorsSignRenewalsWriteGuard(t *testing.T) {
	const termsAt = "2026-09-05T22:57:14Z"
	const verifiedAt = "2026-09-06T00:00:00Z"
	const endedAt = "2026-09-10T00:00:00Z"
	const noticeAt = "2026-10-01T00:00:00Z"
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
		{"otherwise ready but tenancy ended", map[string]interface{}{"hasGuarantor": false, "termsSetAt": termsAt, "tenancyEndedAt": endedAt}, false},
		{"otherwise ready, no tenancyEndedAt", map[string]interface{}{"hasGuarantor": false, "termsSetAt": termsAt, "tenancyEndedAt": nil}, true},
		{"otherwise ready but notice given", map[string]interface{}{"hasGuarantor": false, "termsSetAt": termsAt, "noticeMoveOutAt": noticeAt}, false},
		{"otherwise ready, no noticeMoveOutAt", map[string]interface{}{"hasGuarantor": false, "termsSetAt": termsAt, "noticeMoveOutAt": nil}, true},
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

// TestRenewalCardOffersLandlordActions_ClosesOnEndedOrNoticed pins Winston's
// product call (tenancy-notice-2026-09-15 fix round): once the renewed
// leaseapp carries tenancyEndedAt or noticeMoveOutAt, renewalComplete's own
// `open` conjunct (renewal_lenses.go: `(status = 'open') AND (tenancyEndedAt
// = null) AND (noticeMoveOutAt = null)`) is false and the planner stops
// driving the cycle, so the landlord's renewal card must offer NO
// Set terms / Verify guarantor / Decline button — a card that still offered
// one would be a dead end nobody will ever action again.
func TestRenewalCardOffersLandlordActions_ClosesOnEndedOrNoticed(t *testing.T) {
	vm, fn := renewalCardOffersLandlordActionsVM(t)
	run := func(t *testing.T, row map[string]interface{}) bool {
		t.Helper()
		res, err := fn(goja.Undefined(), vm.ToValue(row))
		if err != nil {
			t.Fatalf("renewalCardOffersLandlordActions(%v) threw: %v", row, err)
		}
		return res.ToBoolean()
	}
	for _, tc := range []struct {
		name string
		row  map[string]interface{}
		want bool
	}{
		{"positive vector: an open, live cycle offers actions", map[string]interface{}{"status": "open"}, true},
		{"tenancy ended closes the card", map[string]interface{}{"status": "open", "tenancyEndedAt": "2026-09-10T00:00:00Z"}, false},
		{"notice given closes the card", map[string]interface{}{"status": "open", "noticeMoveOutAt": "2026-10-01T00:00:00Z"}, false},
		{"both set closes the card", map[string]interface{}{"status": "open", "tenancyEndedAt": "2026-09-10T00:00:00Z", "noticeMoveOutAt": "2026-10-01T00:00:00Z"}, false},
		{"a signed/complete cycle with neither set still offers (unrelated to this gate)", map[string]interface{}{"status": "complete", "signedAt": "2026-09-01T00:00:00Z"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.row); got != tc.want {
				t.Errorf("renewalCardOffersLandlordActions(%v) = %v, want %v", tc.row, got, tc.want)
			}
		})
	}
}
