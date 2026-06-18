package leasesigning

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

	targets := WeaverTargets()
	if len(targets) != 1 {
		t.Fatalf("expected exactly 1 weaverTarget, got %d", len(targets))
	}
	target := targets[0]

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
		// every row.<col> template the action names is a lens BodyColumn.
		for _, v := range []string{ga.Subject, ga.Assignee, ga.Target} {
			if !strings.HasPrefix(v, "row.") {
				continue
			}
			refCol := strings.TrimPrefix(v, "row.")
			if !lensCols[refCol] {
				t.Fatalf("gap %q action references row.%s, which is not a lens BodyColumn (lens has %v)", col, refCol, lens.cols)
			}
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
