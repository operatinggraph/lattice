package cafedomain

import (
	"strings"
	"testing"
)

// TestCafeDomain_PlaybookColumnsMatchLens (the §10.2↔§10.8 seam, mirroring
// TestSemanticContracts_PlaybookColumnsMatchLens). A static assertion (no
// pipeline), run once per weaverTarget: every row.<col> (or
// row.<col>.<aspectSuffix>) token the playbook templates names a column its
// own lens (LensRef) projects, and every gap key is a missing_* column that
// same lens projects. Catches a drift between the playbook and the lens
// cheaply.
func TestCafeDomain_PlaybookColumnsMatchLens(t *testing.T) {
	lensBodyCols := map[string][]string{}
	for _, l := range Lenses() {
		if l.Output != nil {
			lensBodyCols[l.CanonicalName] = l.Output.BodyColumns
		}
	}

	targets := WeaverTargets()
	wantGapCount := map[string]int{TabSettlementTarget: 2, StaleTabSettlementTarget: 1}
	if len(targets) != len(wantGapCount) {
		t.Fatalf("expected exactly %d weaverTargets, got %d", len(wantGapCount), len(targets))
	}

	for _, target := range targets {
		cols, ok := lensBodyCols[target.LensRef]
		if !ok {
			t.Fatalf("target %q: LensRef %q not declared among Lenses()", target.TargetID, target.LensRef)
		}
		lensCols := map[string]bool{}
		for _, c := range cols {
			lensCols[c] = true
		}

		for _, l := range Lenses() {
			if l.CanonicalName == target.LensRef {
				prefix := strings.TrimSuffix(l.Output.OutputKeyPattern, ".{actorSuffix}")
				if prefix != target.TargetID {
					t.Fatalf("target %q: TargetID != lens OutputKeyPattern prefix %q", target.TargetID, prefix)
				}
			}
		}

		if want := wantGapCount[target.TargetID]; len(target.Gaps) != want {
			t.Fatalf("target %q: expected exactly %d gaps, got %d", target.TargetID, want, len(target.Gaps))
		}

		for col, ga := range target.Gaps {
			if !strings.HasPrefix(col, "missing_") {
				t.Fatalf("target %q: gaps key %q is not a missing_<gap> column", target.TargetID, col)
			}
			if !lensCols[col] {
				t.Fatalf("target %q: gaps key %q is not a lens BodyColumn (lens has %v)", target.TargetID, col, cols)
			}
			if ga.Operation == "" {
				t.Fatalf("target %q: gap %q: directOp requires a non-empty Operation", target.TargetID, col)
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
				refCol = strings.SplitN(refCol, ".", 2)[0]
				if !lensCols[refCol] {
					t.Fatalf("target %q: gap %q action references row.%s, which is not a lens BodyColumn (lens has %v)", target.TargetID, col, refCol, cols)
				}
			}
		}
	}
}
