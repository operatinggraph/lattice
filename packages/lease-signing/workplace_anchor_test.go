package leasesigning

import (
	"strings"
	"testing"
)

// TestWorkplaceAnchor_UsesComprehensionNotLiteralElement is the vector behind
// the most dangerous way to write this anchor.
//
// The obvious form is a second array element:
//
//	[nanoIdFromKey(landlordKey), nanoIdFromKey(bldgKey)] AS authz_anchors
//
// which is WRONG. When a unit is not containedIn a building the walk yields
// null, so the array gets a NULL element — and the Protected adapter's
// toStringSlice REJECTS a non-string element, failing the row's entire upsert.
// The row then disappears for its LANDLORD too. A missing building must cost
// the row its staff visibility, never its existence.
//
// A pattern comprehension yields [] instead of a null element, so the array
// stays all-strings and a building-less unit simply carries no workplace token.
func TestWorkplaceAnchor_UsesComprehensionNotLiteralElement(t *testing.T) {
	spec := landlordLeaseApplicationsReadSpec

	if !strings.Contains(spec, "[(u)-[:containedIn]->(b:building) | nanoIdFromKey(b.key)]") {
		t.Fatal("the workplace anchor must be a pattern comprehension (yields [] when absent), not an array element (yields a null element)")
	}
	// The landlord anchor stays a required, always-present token.
	if !strings.Contains(spec, "[nanoIdFromKey(landlordKey)] +") {
		t.Error("the landlord anchor must remain the first, unconditional element — staff visibility is additive to it, never a replacement")
	}
	// A comma between two nanoIdFromKey calls inside one bracket is the
	// null-element shape this test exists to forbid.
	if strings.Contains(spec, "[nanoIdFromKey(landlordKey), nanoIdFromKey(") {
		t.Error("two-element array literal reintroduces the null-element hazard: a building-less unit would fail the whole row's upsert")
	}
}

// TestWorkplaceAnchor_ApplicantLensUnchanged pins the blast radius. The
// applicant-facing lens anchors on the applicant's own id; a building token
// there would let front-desk staff read applicants' rows through a table that
// was never meant to be workplace-scoped.
func TestWorkplaceAnchor_ApplicantLensUnchanged(t *testing.T) {
	if strings.Contains(leaseApplicationsReadSpec, "containedIn") {
		t.Error("the applicant-facing lens must not gain a workplace anchor — only the landlord worklist is workplace-scoped in v1")
	}
	if !strings.Contains(leaseApplicationsReadSpec, "[nanoIdFromKey(applicantKey)]") {
		t.Error("the applicant lens must keep its self-anchor exactly as shipped")
	}
}
