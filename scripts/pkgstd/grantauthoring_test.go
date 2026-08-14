package pkgstd

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// mkSpec mirrors packages/rbac-domain/permissions.go's own declaration idiom: a
// closure that builds each PermissionSpec from an operationType argument.
//
// The fixture is built this way on purpose. Roughly a fifth of the corpus
// declares its grants through a helper like this, a loop over components, or a
// named constant, so a rule implemented as a source scan for
// `PermissionSpec{OperationType: "UpdatePermission"}` would pass a
// struct-literal fixture while missing every package that matters. A test whose
// fixture is a literal cannot tell the two implementations apart.
func mkSpec(op, note string) pkgmgr.PermissionSpec {
	return pkgmgr.PermissionSpec{
		OperationType: op,
		Scope:         "any",
		Note:          note,
		GrantsTo:      []string{"operator"},
	}
}

// bodyRewritingOpName assembles the reserved operationType from parts, so the
// token `UpdatePermission` never appears as one string in this file. If the
// gate ever regressed to matching source text, the fixture below would stop
// resembling anything it could find.
func bodyRewritingOpName() string {
	verb, class := "Update", "Permission"
	return verb + class
}

func defWith(specs ...pkgmgr.PermissionSpec) pkgmgr.Definition {
	return pkgmgr.Definition{Name: "fixture-pkg", Version: "0.0.1", Permissions: specs}
}

// TestLintPackageStandard_GrantAuthoring walks the gate's outcomes in the order
// that makes each one mean something.
//
// The sanctioned case runs FIRST as the positive vector: it proves a
// well-formed declaration is admitted, so the denial that follows is
// attributable to the missing declaration rather than to a rule that refuses
// everything it is handed. The unsanctioned case then runs the SAME
// helper-built spec with only the Note changed — which is also what stops the
// positive vector passing vacuously, since a rule that never fired would fail
// that step.
func TestLintPackageStandard_GrantAuthoring(t *testing.T) {
	op := bodyRewritingOpName()

	t.Run("an ordinary grant is untouched", func(t *testing.T) {
		got := GrantAuthoringIssues("fixture-pkg", defWith(
			mkSpec("CreateRole", "Grants the operator the right to submit CreateRole operations."),
			mkSpec("TombstonePermission", "Narrows a grant; never rewrites a body."),
		))
		if len(got) != 0 {
			t.Fatalf("the gate fired on ops that rewrite nothing: %v", got)
		}
	})

	sanctioned := mkSpec(op, "Needed while the class has no remint route. "+
		"[grant-authoring-sanctioned: no-remint-path — the permission class ships no tombstone-and-remint route for this fixture]")

	t.Run("a declared sanction passes", func(t *testing.T) {
		if got := GrantAuthoringIssues("fixture-pkg", defWith(sanctioned)); len(got) != 0 {
			t.Fatalf("a well-formed sanction was refused: %v", got)
		}
	})

	t.Run("the same spec without the sanction fails", func(t *testing.T) {
		unsanctioned := mkSpec(op, sanctioned.Note[:strings.Index(sanctioned.Note, "[")])
		if unsanctioned.OperationType != sanctioned.OperationType || unsanctioned.Scope != sanctioned.Scope {
			t.Fatalf("the two fixtures differ by more than the Note — the comparison proves nothing")
		}
		got := GrantAuthoringIssues("fixture-pkg", defWith(unsanctioned))
		if len(got) != 1 {
			t.Fatalf("expected exactly 1 issue for an undeclared body-rewriting grant, got %d: %v", len(got), got)
		}
		for _, want := range []string{"fixture-pkg", op, "write-once", sanctionMarker} {
			if !strings.Contains(got[0], want) {
				t.Errorf("the failure does not mention %q, so the author is not told what to do: %s", want, got[0])
			}
		}
	})

	t.Run("a malformed sanction fails rather than passing", func(t *testing.T) {
		for name, note := range map[string]string{
			"no code":         "[grant-authoring-sanctioned: the class has no remint route]",
			"unknown code":    "[grant-authoring-sanctioned: because-i-said-so — it is needed]",
			"no prose":        "[grant-authoring-sanctioned: no-remint-path — ]",
			"hyphen not dash": "[grant-authoring-sanctioned: no-remint-path - the class has no remint route]",

			// The marker is never closed. Its prose must not run forward and
			// borrow the `]` of an unrelated bracketed reference further down
			// the Note: that would turn an unterminated declaration into a
			// valid one, completed by brackets sitting a paragraph away, past a
			// newline, in text no reader takes for part of the sanction.
			"unterminated, closed by a later unrelated bracket": "[grant-authoring-sanctioned: no-remint-path — see the ADR\nRefs: [contract-6]",

			// Two declarations, the second bogus. Only the first is parsed, so
			// admitting this validates a Note whose other half claims a code the
			// vocabulary does not carry.
			"a second, bogus marker": "[grant-authoring-sanctioned: no-remint-path — the class has no remint route] " +
				"[grant-authoring-sanctioned: because-i-said-so — and this one is never read]",

			// Both markers well-formed and valid: still ambiguous, because
			// nobody auditing the grant can tell which one the gate honoured.
			"two valid markers": "[grant-authoring-sanctioned: no-remint-path — the class has no remint route] " +
				"[grant-authoring-sanctioned: pre-provenance-migration — a different reason here]",
		} {
			got := GrantAuthoringIssues("fixture-pkg", defWith(mkSpec(op, note)))
			if len(got) != 1 {
				t.Errorf("%s: expected the malformed sanction to fail, got %v", name, got)
			}
		}
	})

	t.Run("a spec granting nothing is still denied", func(t *testing.T) {
		// A PermissionSpec with an empty GrantsTo still mints the permission
		// vertex, which leaves a runtime GrantPermission one link away from
		// conferring it. The declaration is what the gate denies.
		spec := mkSpec(op, "no roles listed")
		spec.GrantsTo = nil
		if got := GrantAuthoringIssues("fixture-pkg", defWith(spec)); len(got) != 1 {
			t.Fatalf("a body-rewriting spec with no GrantsTo was admitted: %v", got)
		}
	})
}

// TestLintPackageStandard_GrantAuthoringVocabularyIsClosed pins the two halves
// of the escape hatch that make it re-checkable: every code carries prose
// explaining what it claims (a code with none cannot be re-checked when its
// mechanism ships), and the marker does not collide with the `[no-op-meta: …]`
// exemption the sibling rules parse — two markers that contained one another
// would each answer for the other's declaration.
func TestLintPackageStandard_GrantAuthoringVocabularyIsClosed(t *testing.T) {
	if len(sanctionCodes) == 0 {
		t.Fatal("the vocabulary is empty, so the hatch cannot be declared against — a gate with no usable declaration gets deleted rather than declared against")
	}
	for code, meaning := range sanctionCodes {
		if strings.TrimSpace(meaning) == "" {
			t.Errorf("sanction code %q claims nothing, so nothing can retire it", code)
		}
	}
	if strings.Contains(sanctionMarker, "[no-op-meta:") || strings.Contains("[no-op-meta:", sanctionMarker) {
		t.Errorf("the sanction marker %q collides with the S1 exemption marker", sanctionMarker)
	}
}
