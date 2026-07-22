package leasesigning

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestLeaseSigning_PlaybookColumnsMatchLens (test 6 — the §10.2↔§10.8 seam, §4
// trap #3). A static assertion (no pipeline): every row.<col> token the playbook
// templates is a member of the lens Output.BodyColumns, and every gaps key is a
// missing_* column the lens projects. Catches a drift between the playbook and
// the lens cheaply.
func TestLeaseSigning_PlaybookColumnsMatchLens(t *testing.T) {
	lensCols := map[string]bool{}
	var lens *struct{ cols []string }
	for _, l := range Lenses() {
		if l.CanonicalName == "leaseApplicationComplete" {
			for _, c := range l.Output.BodyColumns {
				lensCols[c] = true
			}
			lens = &struct{ cols []string }{l.Output.BodyColumns}
		}
	}
	if lens == nil {
		t.Fatal("leaseApplicationComplete lens not declared")
	}

	// The package now declares three weaverTargets (leaseApplicationComplete,
	// plus the renewal chain's leaseExpiry/renewalComplete, design
	// loftspace-lease-renewal-goal-authored-target-design.md §9 R2) — select
	// the one this test is actually about by TargetID rather than assuming
	// it is the only one.
	var target pkgmgr.WeaverTargetSpec
	var found bool
	for _, wt := range WeaverTargets() {
		if wt.TargetID == "leaseApplicationComplete" {
			target = wt
			found = true
			break
		}
	}
	if !found {
		t.Fatal("leaseApplicationComplete weaverTarget not declared")
	}

	// TargetID == the lens OutputKeyPattern prefix (the §10.2↔§10.8 binding).
	for _, l := range Lenses() {
		if l.CanonicalName == "leaseApplicationComplete" {
			prefix := strings.TrimSuffix(l.Output.OutputKeyPattern, ".{actorSuffix}")
			if prefix != target.TargetID {
				t.Fatalf("TargetID %q != lens OutputKeyPattern prefix %q", target.TargetID, prefix)
			}
		}
	}

	for col, ga := range target.Gaps {
		// every gaps key is a missing_* column the lens projects.
		if !strings.HasPrefix(col, "missing_") {
			t.Fatalf("gaps key %q is not a missing_<gap> column", col)
		}
		if !lensCols[col] {
			t.Fatalf("gaps key %q is not a lens BodyColumn (lens has %v)", col, lens.cols)
		}
		// every row.<col> template the action names — across the scalar fields
		// (Subject / Assignee / Target) AND a directOp's Params values + Reads —
		// is a lens BodyColumn. Literals (e.g. status=leased) carry no row.
		// prefix and are skipped.
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
			t.Fatalf("gap %q action references row.%s, which is not a lens BodyColumn (lens has %v)", col, refCol, lens.cols)
		}
	}
}

// TestLeaseAppType_AbsentFromCore (test 7 — invariant a, mirrors
// service-domain/type_agnostic_test.go). The concrete types/ops this package
// introduces live ONLY in the package; they must not leak into internal/* engine
// code. A narrow grep (the leaseapp class string + the package's op names) over
// internal/ asserts the boundary.
func TestLeaseAppType_AbsentFromCore(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	internalDir := filepath.Join(repoRoot, "internal")

	// The concrete tokens that must not appear in engine code. Narrowly chosen to
	// avoid false positives on the English word "lease": the vertex-key prefix
	// and the package's distinctive op names.
	forbidden := []string{
		"vtx.leaseapp.",
		"CreateLeaseApplication",
		"CreateLeaseServiceInstance",
		"RecordLeaseServiceOutcome",
		"leaseApplicationComplete",
	}

	var violations []string
	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Tests may legitimately reference these tokens (e.g. a fixture). The
		// invariant is about ENGINE code, so skip _test.go files.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		content := string(b)
		for _, tok := range forbidden {
			if strings.Contains(content, tok) {
				rel, _ := filepath.Rel(repoRoot, path)
				violations = append(violations, rel+": "+tok)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("lease-signing concrete types/ops leaked into internal/ engine code:\n  %s", strings.Join(violations, "\n  "))
	}
}
