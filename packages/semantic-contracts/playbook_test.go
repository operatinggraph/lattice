package semanticcontracts

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// checkPlaybookColumnsMatchLens is the §10.2↔§10.8 seam assertion (mirroring
// TestLeaseSigning_PlaybookColumnsMatchLens): every row.<col> token the named
// weaverTarget's playbook templates is a member of its own lens's
// BodyColumns, and every gaps key is a missing_* column that lens projects.
// Shared by every per-target test below — the package now declares two
// targets (clauseSatisfaction, leaseRentSettlement), so each test selects its
// own by TargetID rather than assuming there is exactly one (the trap a
// single-target package can get away with, but this one no longer can).
func checkPlaybookColumnsMatchLens(t *testing.T, targetID string) {
	t.Helper()
	lensCols := map[string]bool{}
	var cols []string
	for _, l := range Lenses() {
		if l.CanonicalName == targetID {
			for _, c := range l.Output.BodyColumns {
				lensCols[c] = true
			}
			cols = l.Output.BodyColumns
		}
	}
	if cols == nil {
		t.Fatalf("%s lens not declared", targetID)
	}

	var target pkgmgr.WeaverTargetSpec
	var found bool
	for _, wt := range WeaverTargets() {
		if wt.TargetID == targetID {
			target = wt
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%s weaverTarget not declared", targetID)
	}

	for _, l := range Lenses() {
		if l.CanonicalName == targetID {
			prefix := strings.TrimSuffix(l.Output.OutputKeyPattern, ".{actorSuffix}")
			if prefix != target.TargetID {
				t.Fatalf("TargetID %q != lens OutputKeyPattern prefix %q", target.TargetID, prefix)
			}
		}
	}

	for col, ga := range target.Gaps {
		if !strings.HasPrefix(col, "missing_") {
			t.Fatalf("gaps key %q is not a missing_<gap> column", col)
		}
		if !lensCols[col] {
			t.Fatalf("gaps key %q is not a lens BodyColumn (lens has %v)", col, cols)
		}
		templated := []string{ga.Subject, ga.Assignee, ga.Target}
		for _, v := range ga.Params {
			templated = append(templated, v)
		}
		templated = append(templated, ga.Reads...)
		for _, v := range templated {
			if !strings.HasPrefix(v, "row.") {
				continue
			}
			refCol := strings.TrimPrefix(v, "row.")
			if lensCols[refCol] {
				continue
			}
			// A Reads-only derived-aspect form row.<col>.<aspect> (§13 hard
			// case 4, strategist.go resolveReadKey): the BASE column must
			// still be a lens BodyColumn even though the full dotted string
			// isn't one.
			base, _, isSuffixed := strings.Cut(refCol, ".")
			if isSuffixed && lensCols[base] {
				continue
			}
			t.Fatalf("gap %q action references row.%s, which is not a lens BodyColumn (lens has %v)", col, refCol, cols)
		}
	}
}

func TestSemanticContracts_PlaybookColumnsMatchLens(t *testing.T) {
	checkPlaybookColumnsMatchLens(t, ClauseSatisfactionTarget)
}

func TestSemanticContracts_LeaseRentSettlementColumnsMatchLens(t *testing.T) {
	checkPlaybookColumnsMatchLens(t, LeaseRentSettlementTarget)
}
