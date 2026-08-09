package leasesigning

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/weaver/planner"
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

// TestRenewalComplete_MissingGoalAgreement proves renewal_lenses.go's own
// claim (renewalCompleteSpec's doc comment): the lens's missing_renewalComplete
// predicate and the renewalComplete WeaverTarget's planner Goal
// (renewal_targets.go) are two independent evaluations over the SAME facts
// that must agree. It runs the planner's REAL guard evaluator
// (internal/weaver/planner.EvalGuard) against the parsed Goal, and a literal
// Go mirror of the lens's own RETURN expression, across every combination of
// the five underlying facts (32 cases) — static, no NATS/Postgres required.
//
// A regression this catches: projecting hasGuarantor as the raw (possibly
// null) .applicationSignals field, rather than coalesced to a real boolean
// via (app.applicationSignals.data.hasGuarantor = True), would disagree with
// the goal on every hasGuarantor=false, .applicationSignals-absent case — the
// bug MAJOR 5 fixed.
func TestRenewalComplete_MissingGoalAgreement(t *testing.T) {
	var target pkgmgr.WeaverTargetSpec
	var found bool
	for _, wt := range RenewalTargets() {
		if wt.TargetID == "renewalComplete" {
			target = wt
			found = true
			break
		}
	}
	if !found {
		t.Fatal("renewalComplete weaverTarget not declared")
	}
	ga, ok := target.Gaps["missing_renewalComplete"]
	if !ok {
		t.Fatal("renewalComplete target has no missing_renewalComplete gap")
	}
	goal, err := guardgrammar.Parse(ga.Goal)
	if err != nil {
		t.Fatalf("parse renewalComplete goal: %v", err)
	}

	bgcheckPath := guardgrammar.Path{Field: "bgcheckValidUntil"}
	hasGuarantorPath := guardgrammar.Path{Field: "hasGuarantor"}
	guarantorVerifiedPath := guardgrammar.Path{Aspect: "guarantorVerification", Field: "verifiedAt"}
	termsSetPath := guardgrammar.Path{Aspect: "terms", Field: "setAt"}
	signedPath := guardgrammar.Path{Aspect: "renewalSignature", Field: "signedAt"}

	for _, hasGuarantor := range []bool{false, true} {
		for _, bgcheck := range []bool{false, true} {
			for _, guarantorVerified := range []bool{false, true} {
				for _, termsSet := range []bool{false, true} {
					for _, signed := range []bool{false, true} {
						// hasGuarantor is ALWAYS a concrete boolean here — the
						// post-fix lens projection (app.applicationSignals.data.
						// hasGuarantor = True) never leaves it null, so the
						// state matrix does not exercise an absent key for it.
						state := planner.State{hasGuarantorPath: hasGuarantor}
						if bgcheck {
							state[bgcheckPath] = "2027-01-01T00:00:00Z"
						}
						if guarantorVerified {
							state[guarantorVerifiedPath] = "2027-01-01T00:00:00Z"
						}
						if termsSet {
							state[termsSetPath] = "2027-01-01T00:00:00Z"
						}
						if signed {
							state[signedPath] = "2027-01-01T00:00:00Z"
						}

						goalMet := planner.EvalGuard(goal, state)
						// The lens's own RETURN expression (renewal_lenses.go
						// renewalCompleteSpec), mirrored literally:
						//   missing_renewalComplete = (status='open') AND leaseappAlive AND NOT (
						//     bgcheckValidUntil <> null AND
						//     (hasGuarantor = False OR guarantorVerifiedAt <> null) AND
						//     termsSetAt <> null AND signedAt <> null)
						// status/leaseappAlive are orthogonal to the goal, so this
						// asserts only the NOT(...) remainder — the goal must be
						// met exactly when that remainder is false.
						lensGoalPortion := bgcheck && (!hasGuarantor || guarantorVerified) && termsSet && signed

						if goalMet != lensGoalPortion {
							t.Fatalf("hasGuarantor=%v bgcheck=%v guarantorVerified=%v termsSet=%v signed=%v: "+
								"planner Goal met=%v, lens's missing_renewalComplete remainder met=%v — DISAGREE",
								hasGuarantor, bgcheck, guarantorVerified, termsSet, signed, goalMet, lensGoalPortion)
						}
					}
				}
			}
		}
	}
}
