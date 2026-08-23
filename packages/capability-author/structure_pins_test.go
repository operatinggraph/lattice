package capabilityauthor

import "testing"

// TestPackage_StructurePins pins every declared element by count and canonical
// name (Vertical Package Standard S6, loftspace-domain/package_test.go idiom). A
// declaration added or dropped without a deliberate edit here reds this test
// rather than reaching an install, where the same change is a silent capability
// or read-model shift.
//
// It lives in its own INTERNAL test file because this package's package_test.go
// is an external `capabilityauthor_test` package, where the pin would have to
// read `capabilityauthor.Package.…` — and the S6 gate greps for the literal
// `len(Package.`, so an externally-qualified pin would not register as one.
//
// Every permission is operator-only on purpose: this package authors NEW package
// artifacts, so a consumer-reachable grant here would let a caller mint platform
// capability. That is the human gate the whole design rests on.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 4; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 4; got != want {
		t.Errorf("Lenses: got %d, want %d", got, want)
	}
	if got, want := len(Package.Permissions), 7; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 7; got != want {
		t.Errorf("OpMetas: got %d, want %d", got, want)
	}
	if got, want := len(Package.Roles), 0; got != want {
		t.Errorf("Roles: got %d, want %d", got, want)
	}
	if got, want := len(Package.WeaverTargets), 1; got != want {
		t.Errorf("WeaverTargets: got %d, want %d", got, want)
	}
	if got, want := len(Package.LoomPatterns), 1; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	if len(Package.Depends) != 1 || Package.Depends[0] != "orchestration-base" {
		t.Errorf("Depends: got %v, want [orchestration-base]", Package.Depends)
	}

	for i, want := range []string{"capabilityproposal", "capabilityauthorclaim", "capabilityAuthorClaimDispatch", "capabilityAuthorDispatchMarker"} {
		if i >= len(Package.DDLs) {
			break
		}
		if got := Package.DDLs[i].CanonicalName; got != want {
			t.Errorf("DDLs[%d]: got %q, want %q", i, got, want)
		}
	}
	for i, want := range []string{"capabilityAuthorPending", "capabilityProposals", "capabilityAuthorContext", "capabilityAuthorPackages"} {
		if i >= len(Package.Lenses) {
			break
		}
		if got := Package.Lenses[i].CanonicalName; got != want {
			t.Errorf("Lenses[%d]: got %q, want %q", i, got, want)
		}
	}
	wantPerms := []struct{ op, scope string }{
		{"RequestCapabilityAuthoring", "any"}, {"CreateAuthoringClaim", "any"},
		{"SubmitCapabilityProposal", "any"}, {"RecordCapabilityProposal", "any"},
		{"RecordAuthoringDispatch", "any"},
		{"ReviewCapabilityProposal", "any"}, {"MarkCapabilityProposalApplied", "any"},
	}
	for i, want := range wantPerms {
		if i >= len(Package.Permissions) {
			break
		}
		got := Package.Permissions[i]
		if got.OperationType != want.op || got.Scope != want.scope {
			t.Errorf("Permissions[%d]: got %s/%s, want %s/%s", i, got.OperationType, got.Scope, want.op, want.scope)
		}
	}
}
