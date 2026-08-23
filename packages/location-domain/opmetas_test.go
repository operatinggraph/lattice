package locationdomain

import (
	"sort"
	"testing"
)

// TestOpMetas_DispatchClassMatchesOwningDDL is location-domain's variant of
// clinic-domain's opmetas_test.go test of the same name, adapted for the one
// op this package's op-metas describe that is NOT owned by a single DDL:
// CreateLocation is declared on all three concrete leaves (unit, building,
// property — ddls.go locationLeafDDL), so it has no single Dispatch.Class to
// match — it carries Dispatch.ClassChoices instead. This test asserts the
// no-static-Class shape directly, and cross-checks ClassChoices against
// DDLs()'s own PermittedCommands LIVE rather than a copied literal list, so a
// future fourth leaf type can't silently go undescribed.
func TestOpMetas_DispatchClassMatchesOwningDDL(t *testing.T) {
	// owningClasses collects every vertexType DDL's CanonicalName that admits
	// each operationType in its PermittedCommands — the live census a
	// classChoices op must equal exactly, never a hand-maintained copy.
	owningClasses := map[string][]string{}
	for _, d := range DDLs() {
		if d.Class != "meta.ddl.vertexType" {
			continue // only a vertexType DDL carries the Script step4 resolves by class
		}
		for _, op := range d.PermittedCommands {
			owningClasses[op] = append(owningClasses[op], d.CanonicalName)
		}
	}

	for _, m := range OpMetas() {
		if m.Dispatch == nil {
			continue
		}
		owners := owningClasses[m.OperationType]
		if len(owners) == 0 {
			t.Fatalf("%s: no owning DDL found in PermittedCommands", m.OperationType)
		}

		switch m.OperationType {
		case "CreateLocation":
			if m.Dispatch.Class != "" {
				t.Errorf("CreateLocation: Dispatch.Class = %q, want \"\" — legitimately declared on %d DDLs, "+
					"so no single static class can name it; must use ClassChoices", m.Dispatch.Class, len(owners))
			}
			got := append([]string{}, m.Dispatch.ClassChoices...)
			sort.Strings(got)
			want := append([]string{}, owners...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("CreateLocation: Dispatch.ClassChoices = %v, want exactly the live DDL owners %v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("CreateLocation: Dispatch.ClassChoices = %v, want exactly the live DDL owners %v", got, want)
					break
				}
			}
			// LocationTypes is the package's own declared vocabulary
			// (ddls.go:88) — ClassChoices must be it, not a fresh literal that
			// could drift from it independently.
			wantLT := append([]string{}, LocationTypes...)
			sort.Strings(wantLT)
			if len(got) != len(wantLT) {
				t.Fatalf("CreateLocation: Dispatch.ClassChoices = %v, want exactly LocationTypes %v", got, wantLT)
			}
			for i := range got {
				if got[i] != wantLT[i] {
					t.Errorf("CreateLocation: Dispatch.ClassChoices = %v, want exactly LocationTypes %v", got, wantLT)
					break
				}
			}
		default:
			if len(owners) != 1 {
				t.Fatalf("%s: declared on %d DDLs (%v) but this test only knows the single-owner shape for it; "+
					"give it a ClassChoices branch above like CreateLocation", m.OperationType, len(owners), owners)
			}
			if m.Dispatch.Class != owners[0] {
				t.Errorf("%s: Dispatch.Class = %q, want %q (owning DDL's CanonicalName)", m.OperationType, m.Dispatch.Class, owners[0])
			}
		}
	}
}
