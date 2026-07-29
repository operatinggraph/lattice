package wellnessdomain

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition keeps manifest.yaml and the Go
// Definition in lockstep (the cafe-domain/clinic-domain precedent): the
// install reads the Definition, but the manifest is the human-facing
// declaration, and a drift between the two is a silent install hazard.
func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

// TestPackage_StructurePins pins every declared element by count and canonical
// name (Vertical Package Standard S6, loftspace-domain/package_test.go idiom). A
// declaration added or dropped without a deliberate edit here reds this test
// rather than reaching an install, where the same change is a silent capability
// or read-model shift.
//
// Five of the thirteen DDLs are CreateOnly claim aspects (S4) — the slot, seat,
// booker, and the two instructor-binding claims. Each is the sole thing making
// its uniqueness constraint hold, and none is reachable from a lens, so dropping
// one would not break a read: it would silently re-admit double-booking. That is
// why they are pinned by name rather than counted.
func TestPackage_StructurePins(t *testing.T) {
	if got, want := len(Package.DDLs), 13; got != want {
		t.Errorf("DDLs: got %d, want %d", got, want)
	}
	if got, want := len(Package.Lenses), 7; got != want {
		t.Errorf("Lenses: got %d, want %d", got, want)
	}
	if got, want := len(Package.Permissions), 14; got != want {
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
	if got, want := len(Package.LoomPatterns), 0; got != want {
		t.Errorf("LoomPatterns: got %d, want %d", got, want)
	}
	if got, want := len(Package.Depends), 0; got != want {
		t.Errorf("Depends: got %d, want %d — wellness stands alone", got, want)
	}

	wantDDLs := []struct{ name, class string }{
		{"studio", "meta.ddl.vertexType"},
		{"session", "meta.ddl.vertexType"},
		{"booking", "meta.ddl.vertexType"},
		{"instructor", "meta.ddl.vertexType"},
		{"studioProfile", "meta.ddl.aspectType"},
		{"sessionSchedule", "meta.ddl.aspectType"},
		{"studioSlotClaim", "meta.ddl.aspectType"},
		{"sessionSeatClaim", "meta.ddl.aspectType"},
		{"sessionBookerClaim", "meta.ddl.aspectType"},
		{"bookingStatus", "meta.ddl.aspectType"},
		{"instructorProfile", "meta.ddl.aspectType"},
		{"instructorIdentityClaim", "meta.ddl.aspectType"},
		{"identityInstructorClaim", "meta.ddl.aspectType"},
	}
	for i, want := range wantDDLs {
		if i >= len(Package.DDLs) {
			break
		}
		got := Package.DDLs[i]
		if got.CanonicalName != want.name || got.Class != want.class {
			t.Errorf("DDLs[%d]: got %s/%s, want %s/%s", i, got.CanonicalName, got.Class, want.name, want.class)
		}
	}
	for i, want := range []string{"wellnessStudios", "wellnessSessions", "wellnessBookings", "wellnessInstructors", "wellnessMembers", "wellnessIdentitiesRead", "wellnessOrphanedBookingSettlement"} {
		if i >= len(Package.Lenses) {
			break
		}
		if got := Package.Lenses[i].CanonicalName; got != want {
			t.Errorf("Lenses[%d]: got %q, want %q", i, got, want)
		}
	}
	// Pinned as (operationType, scope, grantsTo) triples: a permission's
	// identity is its (operationType, scope) pair (Contract #8 §8.1), and the
	// grantee list is the security-bearing half — widening a row is how a
	// front-desk hat appears, so the pin has to see it (clinic-domain's grant
	// matrix is the precedent). The staff-widened rows are confined inside the
	// Starlark scripts by the workplace walk; see workplace_confinement_test.go.
	staff := []string{"operator", "frontOfHouse"}
	operatorOnly := []string{"operator"}
	wantPerms := []struct {
		op, scope string
		grantsTo  []string
	}{
		{"CreateStudio", "any", staff}, {"TombstoneStudio", "any", operatorOnly},
		{"CreateSession", "any", staff}, {"TombstoneSession", "any", []string{"operator", "provider"}},
		{"CreateBooking", "any", staff}, {"CreateBooking", "self", []string{"consumer"}},
		{"CancelBooking", "any", staff}, {"CancelBooking", "self", []string{"consumer"}},
		{"SetBookingAttendance", "any", []string{"operator", "provider"}},
		{"CreateInstructor", "any", operatorOnly}, {"TombstoneInstructor", "any", operatorOnly},
		{"SetInstructorProfile", "any", []string{"operator", "provider"}},
		{"BindInstructorIdentity", "any", operatorOnly},
		{"ReleaseOrphanedBooking", "any", operatorOnly},
	}
	if got := len(Package.Permissions); got != len(wantPerms) {
		t.Errorf("Permissions: got %d, want %d", got, len(wantPerms))
	}
	for i, want := range wantPerms {
		if i >= len(Package.Permissions) {
			break
		}
		got := Package.Permissions[i]
		if got.OperationType != want.op || got.Scope != want.scope {
			t.Errorf("Permissions[%d]: got %s/%s, want %s/%s", i, got.OperationType, got.Scope, want.op, want.scope)
		}
		if !slices.Equal(got.GrantsTo, want.grantsTo) {
			t.Errorf("Permissions[%d] (%s/%s): grantsTo %v, want %v", i, want.op, want.scope, got.GrantsTo, want.grantsTo)
		}
	}
}
