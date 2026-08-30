package testutil_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	testActorKey = "vtx.identity.ACTRAAAAAAAAAAAAAAAA"
	testHubKey   = "vtx.building.BLDGAAAAAAAAAAAAAAAA"
)

// metaWith builds a one-op meta set carrying the given dispatch enumerations.
func metaWith(op string, enums ...pkgmgr.EnumerationSpec) []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{{
		OperationType: op,
		Dispatch:      &pkgmgr.OpDispatchSpec{Enumerations: enums},
	}}
}

func actorRole() pkgmgr.EnumerationSpec {
	return pkgmgr.EnumerationSpec{Hub: "{actor}", Relation: "holdsRole", Direction: "out"}
}

// TestDeclaredEnumerations_SubstitutesActorHub is the property every fixture
// retiring a baseline row depends on: the spec's `{actor}` template becomes the
// submitting actor's own key, so the hint the envelope carries normalizes to the
// same shape the script's walk records.
func TestDeclaredEnumerations_SubstitutesActorHub(t *testing.T) {
	got := testutil.DeclaredEnumerations("OpenTab", testActorKey, metaWith("OpenTab", actorRole()))
	if len(got) != 1 {
		t.Fatalf("want 1 hint, got %d (%+v)", len(got), got)
	}
	if got[0].Hub != testActorKey {
		t.Errorf("hub: want %q, got %q", testActorKey, got[0].Hub)
	}
	if got[0].Relation != "holdsRole" || got[0].Direction != "out" {
		t.Errorf("relation/direction not carried through: %+v", got[0])
	}
}

// TestDeclaredEnumerations_LiteralHubPassesThrough proves a hub with no
// placeholder is already concrete and is not mistaken for an unresolvable one.
func TestDeclaredEnumerations_LiteralHubPassesThrough(t *testing.T) {
	spec := pkgmgr.EnumerationSpec{Hub: testHubKey, Relation: "containedIn", Direction: "out"}
	got, skipped := testutil.DeclaredEnumerationsWithSkips("Move", testActorKey, metaWith("Move", spec))
	if len(skipped) != 0 {
		t.Fatalf("a literal hub must not be skipped, got %v", skipped)
	}
	if len(got) != 1 || got[0].Hub != testHubKey {
		t.Fatalf("want the literal hub carried through, got %+v", got)
	}
}

// TestDeclaredEnumerations_PayloadHubIsReportedNotDropped is the honesty
// property: a hub this helper cannot resolve without the payload must surface as
// a skip, so a fixture cannot read an empty result as "this op declares nothing"
// and retire a row against a declaration it never sent.
func TestDeclaredEnumerations_PayloadHubIsReportedNotDropped(t *testing.T) {
	spec := pkgmgr.EnumerationSpec{Hub: "{payload.unitKey}", Relation: "containedIn", Direction: "out"}
	got, skipped := testutil.DeclaredEnumerationsWithSkips("Move", testActorKey, metaWith("Move", spec))
	if len(got) != 0 {
		t.Errorf("an unresolvable hub must not produce a hint, got %+v", got)
	}
	if len(skipped) != 1 || skipped[0] != "{payload.unitKey}" {
		t.Fatalf("want the unresolved hub reported, got %v", skipped)
	}
}

// TestDeclaredEnumerations_SpansMetaSets covers the cross-package case that
// broke three clinic fixtures: the op's meta is owned by a package other than
// the one under test, so a caller naming both sets must still find it.
func TestDeclaredEnumerations_SpansMetaSets(t *testing.T) {
	own := metaWith("StartVisitSeries", actorRole())
	other := metaWith("CreateAppointment", actorRole())
	got := testutil.DeclaredEnumerations("CreateAppointment", testActorKey, own, other)
	if len(got) != 1 || got[0].Hub != testActorKey {
		t.Fatalf("want the other package's declaration resolved, got %+v", got)
	}
}

// TestDeclaredEnumerations_UndeclaredOpYieldsNothing pins the negative: an op
// with no dispatch spec, or one declaring no enumeration, contributes no hint —
// which is what keeps a fixture from declaring a walk its script never makes.
func TestDeclaredEnumerations_UndeclaredOpYieldsNothing(t *testing.T) {
	metas := []pkgmgr.OpMetaSpec{
		{OperationType: "NoDispatch"},
		{OperationType: "EmptyDispatch", Dispatch: &pkgmgr.OpDispatchSpec{}},
	}
	for _, op := range []string{"NoDispatch", "EmptyDispatch", "NotPresent"} {
		if got := testutil.DeclaredEnumerations(op, testActorKey, metas); len(got) != 0 {
			t.Errorf("%s: want no hints, got %+v", op, got)
		}
	}
}
