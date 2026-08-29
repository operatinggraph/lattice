package capabilityauthor

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

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
	if got, want := len(Package.Permissions), 8; got != want {
		t.Errorf("Permissions: got %d, want %d", got, want)
	}
	if got, want := len(Package.OpMetas), 8; got != want {
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
		{"RecordCapabilityInstallReceipt", "any"},
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

	// OpMetas and the capabilityproposal DDL's PermittedCommands are the two
	// other places an op name must appear for it to be dispatchable at all: the
	// op-meta vertex makes it forOperation-resolvable, PermittedCommands is what
	// the step-6 write gate consults before letting the script touch a
	// vtx.capabilityproposal.* key. Pinning them by name, not just by count,
	// keeps a renamed or dropped op from reaching an install.
	wantOpMetas := []string{
		"RequestCapabilityAuthoring", "CreateAuthoringClaim", "SubmitCapabilityProposal",
		"RecordCapabilityProposal", "RecordAuthoringDispatch", "ReviewCapabilityProposal",
		"MarkCapabilityProposalApplied", "RecordCapabilityInstallReceipt",
	}
	for i, want := range wantOpMetas {
		if i >= len(Package.OpMetas) {
			break
		}
		if got := Package.OpMetas[i].OperationType; got != want {
			t.Errorf("OpMetas[%d]: got %q, want %q", i, got, want)
		}
	}

	// Resolved by CanonicalName, never by position, and never behind a
	// length guard: a pin that can skip itself when the slice it indexes is
	// empty or reordered reports green for exactly the shape it exists to
	// catch.
	wantPermitted := []string{
		"RequestCapabilityAuthoring", "SubmitCapabilityProposal", "RecordCapabilityProposal",
		"ReviewCapabilityProposal", "MarkCapabilityProposalApplied", "RecordCapabilityInstallReceipt",
	}
	var proposalDDL *pkgmgr.DDLSpec
	for i := range Package.DDLs {
		if Package.DDLs[i].CanonicalName == "capabilityproposal" {
			proposalDDL = &Package.DDLs[i]
			break
		}
	}
	if proposalDDL == nil {
		t.Fatal("no DDL with CanonicalName \"capabilityproposal\" — every capability-proposal op is undispatchable without it")
	}
	got := proposalDDL.PermittedCommands
	if len(got) != len(wantPermitted) {
		t.Fatalf("capabilityproposal PermittedCommands: got %v, want %v", got, wantPermitted)
	}
	for i, want := range wantPermitted {
		if got[i] != want {
			t.Errorf("capabilityproposal PermittedCommands[%d]: got %q, want %q", i, got[i], want)
		}
	}
}
