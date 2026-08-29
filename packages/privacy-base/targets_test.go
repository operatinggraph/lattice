package privacybase

import (
	"regexp"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	identitydomain "github.com/operatinggraph/lattice/packages/identity-domain"
)

// findErasureLens resolves the identityErasureComplete target's LensRef to its
// declared LensSpec. Unlike lease-signing's TestLeaseSigning_PlaybookColumnsMatchLens
// (packages/lease-signing/lens_unit_test.go), this package's lens CanonicalName
// (identityErasureResidue) and TargetID (identityErasureComplete) deliberately
// differ (lenses.go's ErasureCompleteTarget doc), so the lookup goes through
// LensRef, never CanonicalName == targetID.
func findErasureLens(t *testing.T) *pkgmgr.LensSpec {
	t.Helper()
	targets := WeaverTargets()
	if len(targets) != 1 {
		t.Fatalf("expected exactly one weaverTarget, got %d", len(targets))
	}
	lenses := Lenses()
	for i := range lenses {
		if lenses[i].CanonicalName == targets[0].LensRef {
			return &lenses[i]
		}
	}
	t.Fatalf("weaverTarget LensRef %q resolves to no declared lens", targets[0].LensRef)
	return nil
}

// TestWeaverTargets_PlaybookColumnsMatchLens (the §10.2↔§10.8 seam, forward
// direction). Catches a build where a Gaps key or a row.<column> template
// drifts from the lens's actual BodyColumns — e.g. a typo'd gap column, or a
// Params/Reads template naming a column the cypher never projects, which
// would resolve to nothing at dispatch time (resolveStringParam's errData)
// and silently drop the gap into the config-error alert path instead of
// dispatching. Also pins TargetID == the lens's OutputKeyPattern prefix — the
// only binding the Weaver resolves a target through (registry.go's Target
// doc) — so neither side can drift alone.
func TestWeaverTargets_PlaybookColumnsMatchLens(t *testing.T) {
	target := WeaverTargets()[0]
	lens := findErasureLens(t)
	if lens.Output == nil {
		t.Fatalf("lens %q has no Output descriptor", lens.CanonicalName)
	}

	prefix := strings.TrimSuffix(lens.Output.OutputKeyPattern, ".{actorSuffix}")
	if prefix != target.TargetID {
		t.Fatalf("TargetID %q != lens OutputKeyPattern prefix %q", target.TargetID, prefix)
	}

	lensCols := make(map[string]bool, len(lens.Output.BodyColumns))
	for _, c := range lens.Output.BodyColumns {
		lensCols[c] = true
	}

	for col, ga := range target.Gaps {
		if !strings.HasPrefix(col, "missing_") {
			t.Fatalf("gaps key %q is not a missing_<gap> column", col)
		}
		if !lensCols[col] {
			t.Fatalf("gaps key %q is not a lens BodyColumn (lens has %v)", col, lens.Output.BodyColumns)
		}
		templated := []string{ga.Target}
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
			base, _, isSuffixed := strings.Cut(refCol, ".")
			if isSuffixed && lensCols[base] {
				continue
			}
			t.Fatalf("gap %q action references row.%s, which is not a lens BodyColumn (lens has %v)",
				col, refCol, lens.Output.BodyColumns)
		}
	}
}

// TestWeaverTargets_EveryLensGapHasPlaybookEntry is the reverse direction, and
// the one that matters most: a `missing_<g>` column the lens projects with NO
// matching Gaps entry is never remediated. Weaver acks the row with a standing
// warning-severity GapWithoutPlaybook issue (internal/weaver/evaluator.go's
// dispatchGap) — Weaver degrades, the obligation goes undispatched, and the only
// surface saying so is one Health entry per (target, gap) that an operator has to
// be looking at. Deleting any one of this package's five Gaps entries must red
// this test.
func TestWeaverTargets_EveryLensGapHasPlaybookEntry(t *testing.T) {
	target := WeaverTargets()[0]
	lens := findErasureLens(t)
	if lens.Output == nil {
		t.Fatalf("lens %q has no Output descriptor", lens.CanonicalName)
	}
	for _, col := range lens.Output.BodyColumns {
		if !strings.HasPrefix(col, "missing_") {
			continue
		}
		if _, ok := target.Gaps[col]; !ok {
			t.Errorf("lens projects %q with no playbook entry — this is the GapWithoutPlaybook failure mode (evaluator.go dispatchGap): the row column is true, the Weaver finds no Gaps[%q], and it acks the row with a standing warning instead of remediating, so the obligation is never dispatched for any subject", col, col)
		}
	}
}

// pascalCaseIssueCode mirrors Contract #5 §5.5's `code` field convention
// (docs/contracts/05-health-kv.md:109-124): every Health issue code raised
// anywhere in the system is PascalCase.
var pascalCaseIssueCode = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)

// TestWeaverTargets_SurfaceGapsCarryWarningSeverityAndPascalCaseIssueCode pins
// the two async-half gaps' severity and code shape. IssueSeverity must be
// "warning", not "error": the row exists from the instant erasureRequested is
// written, and both async halves are ALWAYS outstanding at that instant, so
// every ordinary erasure sits in this state for its whole in-flight window —
// the routine case, not a failure to fulfil the responsibility (Contract #5
// §5.2's line for "error"). aggregateStatus returns "unhealthy" for the whole
// Weaver component on any open "error" issue (internal/weaver/health.go), so
// an "error" severity here would hold the component unhealthy for as long as
// any erasure is in flight — effectively always, in any deployment that
// processes one. orchestration-base's unroutedTasks, the only other shipped
// surface gap, is "warning" for the same reason
// (packages/orchestration-base/targets.go). IssueCode must be non-empty and
// PascalCase, matching every other code raised in the system (the twelve
// engine codes plus orchestration-base's shipped UnroutedTasks) — nothing
// validates the shape at install or CDC load, so a dotted or lowercase code
// would install green and just be wrong.
func TestWeaverTargets_SurfaceGapsCarryWarningSeverityAndPascalCaseIssueCode(t *testing.T) {
	target := WeaverTargets()[0]
	for _, col := range []string{"missing_vaultDestruction", "missing_projectionNullify"} {
		ga, ok := target.Gaps[col]
		if !ok {
			t.Fatalf("gap %q missing from playbook", col)
		}
		if ga.Action != "surface" {
			t.Fatalf("gap %q: want action \"surface\", got %q", col, ga.Action)
		}
		if ga.IssueSeverity != "warning" {
			t.Fatalf("gap %q: IssueSeverity %q must be \"warning\"", col, ga.IssueSeverity)
		}
		if ga.IssueCode == "" {
			t.Fatalf("gap %q: IssueCode is required for a surface action", col)
		}
		if !pascalCaseIssueCode.MatchString(ga.IssueCode) {
			t.Fatalf("gap %q: IssueCode %q is not PascalCase", col, ga.IssueCode)
		}
	}
}

// TestWeaverTargets_DispatchedOpsAreUnambiguous pins the non-ambiguity every
// omitted Class relies on (Contract #10 §10.8 build note's decision 3):
// plan.class exists only to disambiguate the Processor's
// operationType->class reverse index (internal/processor/ddl_cache.go's
// commandIndexEligible — vertexType DDLs only; an aspectType DDL listing the
// same operationType as a step-6 write gate, e.g. this package's own erasure
// aspectType DDL on SealIdentityForErasureComplete, does not count) when TWO
// OR MORE installed vertexType DDLs admit the same operationType. Omitting
// Class on all three of this target's directOp gaps is only correct while
// each stays admitted by exactly one such DDL; the day a second one claims
// any of them, dispatch fails closed (MissingClass) rather than guessing, and
// this test is what catches the corpus drift before that happens rather than
// after. Scoped to privacy-base and identity-domain — the two packages
// UnbindIdentityCredentials, PurgeIdentityDedupFootprint and
// SealIdentityForErasureComplete are declared across, and the only two this
// package can import without a cycle (identity-domain declares no Go
// dependency on privacy-base) — not a full-corpus census.
func TestWeaverTargets_DispatchedOpsAreUnambiguous(t *testing.T) {
	type source struct {
		pkg  string
		ddls []pkgmgr.DDLSpec
	}
	sources := []source{
		{"privacy-base", DDLs()},
		{"identity-domain", identitydomain.DDLs()},
	}
	ops := []string{"UnbindIdentityCredentials", "PurgeIdentityDedupFootprint", "SealIdentityForErasureComplete"}

	for _, op := range ops {
		t.Run(op, func(t *testing.T) {
			var admitters []string
			for _, src := range sources {
				for _, d := range src.ddls {
					if d.Class != "meta.ddl.vertexType" {
						continue
					}
					for _, cmd := range d.PermittedCommands {
						if cmd == op {
							admitters = append(admitters, src.pkg+"/"+d.CanonicalName)
						}
					}
				}
			}
			if len(admitters) != 1 {
				t.Fatalf("%s must be admitted by exactly one meta.ddl.vertexType DDL across privacy-base+identity-domain so the omitted Class stays unambiguous; got %v", op, admitters)
			}
		})
	}
}
