package bespokecontracts

import (
	"strings"
	"testing"
)

// TestBespokeContracts_PlaybookColumnsMatchLens (the §10.2↔§10.8 seam,
// mirroring TestLeaseSigning_PlaybookColumnsMatchLens). A static assertion
// (no pipeline): every row.<col> token the playbook templates is a member of
// the clauseSatisfaction lens's BodyColumns, and the single gaps key is a
// missing_* column the lens projects. Catches a drift between the playbook
// and the lens cheaply.
func TestBespokeContracts_PlaybookColumnsMatchLens(t *testing.T) {
	lensCols := map[string]bool{}
	var cols []string
	for _, l := range Lenses() {
		if l.CanonicalName == ClauseSatisfactionTarget {
			for _, c := range l.Output.BodyColumns {
				lensCols[c] = true
			}
			cols = l.Output.BodyColumns
		}
	}
	if cols == nil {
		t.Fatal("clauseSatisfaction lens not declared")
	}

	targets := WeaverTargets()
	if len(targets) != 1 {
		t.Fatalf("expected exactly 1 weaverTarget, got %d", len(targets))
	}
	target := targets[0]

	for _, l := range Lenses() {
		if l.CanonicalName == ClauseSatisfactionTarget {
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
			if !lensCols[refCol] {
				t.Fatalf("gap %q action references row.%s, which is not a lens BodyColumn (lens has %v)", col, refCol, cols)
			}
		}
	}
}
