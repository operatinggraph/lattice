package pkgmgr

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/lenscolumns"
)

// stubInstalledLenses is an InstalledLensResolver over a fixed catalog, so the
// validator's rule chain is driven through ValidateCapabilityArtifact — the
// entry point every caller uses — with no substrate at all. The three answers
// it can give are the three the contract distinguishes: a lens with columns
// (found, no error), a lens whose columns are not derivable (found, an
// ErrUnreadable-wrapped error), and no lens (not found). readErr overrides all
// three with a failed read, which is not a verdict.
type stubInstalledLenses struct {
	lenses     map[string]lenscolumns.Result
	unreadable map[string]string
	// roots records ids that exist in the kernel under a class OTHER than
	// meta.lens — a weaver target, a Loom pattern, a DDL, an op-meta. They are
	// present and still answer found=false, which is what makes a wrong-class
	// vector mean something: without the class test such an id resolves, since
	// those metas carry a `spec` aspect of their own.
	roots   map[string]string
	readErr error
}

func (s stubInstalledLenses) ResolveLensColumns(lensRef string) (lenscolumns.Result, bool, error) {
	if s.readErr != nil {
		return lenscolumns.Result{}, false, s.readErr
	}
	if class, ok := s.roots[lensRef]; ok && class != MetaLensClass {
		return lenscolumns.Result{}, false, nil
	}
	if why, ok := s.unreadable[lensRef]; ok {
		return lenscolumns.Result{}, true, fmt.Errorf("%w: %s", lenscolumns.ErrUnreadable, why)
	}
	cols, ok := s.lenses[lensRef]
	if !ok {
		return lenscolumns.Result{}, false, nil
	}
	return cols, true, nil
}

// projecting builds a resolver answer: the columns a row of the lens carries,
// attributed to the declaration named by provenance.
func projecting(provenance string, cols ...string) lenscolumns.Result {
	out := lenscolumns.Result{Columns: map[string]string{}, Source: provenance}
	for _, c := range cols {
		out.Columns[c] = provenance
	}
	return out
}

// weaverTargetLensStub is the catalog the pre-existing weaverTarget artifact
// vectors resolve against: the lens they all name, projecting a row with no
// gap column at all, so those vectors keep testing exactly what they were
// written for (shape checks) rather than the binding rule below.
var weaverTargetLensStub = stubInstalledLenses{
	lenses: map[string]lenscolumns.Result{
		"someExistingLens": projecting(lenscolumns.ProvenanceReturn, "key", "entityKey"),
	},
}

// weaverTargetArtifact renders a weaverTarget artifact's content for the
// validator, with one gap column declared per name in declared.
func weaverTargetArtifact(t *testing.T, lensRef string, declared ...string) json.RawMessage {
	t.Helper()
	gaps := map[string]GapActionArtifact{}
	for _, col := range declared {
		gaps[col] = GapActionArtifact{Action: "directOp", Operation: "SendReminder"}
	}
	return weaverTargetContent(t, WeaverTargetArtifactContent{
		TargetID: "aiTargetDispatch",
		LensRef:  lensRef,
		Gaps:     gaps,
	})
}

// THE NIL POSTURE. A weaverTarget artifact validated with no installed-lens
// catalog binds a lens nothing verified, and there is no shape of artifact for
// which that is acceptable — every other injected dependency has a kind that
// ignores it, this one does not. Pinned on the exact sentence, because the
// sentence is what tells an operator staring at a queue of invalid proposals
// that the caller is unwired rather than the proposals bad.
func TestValidateWeaverTarget_NilResolver_Invalid(t *testing.T) {
	content := weaverTargetArtifact(t, "someExistingLens", "missing_followUp")
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("a nil resolver is a verdict on the artifact, never a caller error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid with no installed-lens catalog supplied")
	}
	want := `no installed-lens catalog was supplied to resolve lensRef "someExistingLens" — a weaverTarget artifact may not bind an unverified lens`
	if joined := strings.Join(report.Errors, " "); !strings.Contains(joined, want) {
		t.Fatalf("report = %q, want it to contain %q", joined, want)
	}
}

// THE POSITIVE VECTOR for every refusal below: the same artifact, against a
// catalog that answers, validates. Each negative differs from this in exactly
// one input.
func TestValidateWeaverTarget_DeclaredAgainstInstalledLens_Valid(t *testing.T) {
	content := weaverTargetArtifact(t, "someExistingLens", "missing_followUp")
	resolver := stubInstalledLenses{lenses: map[string]lenscolumns.Result{
		"someExistingLens": projecting(lenscolumns.ProvenanceReturn, "key", "missing_followUp"),
	}}
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("a target declaring every gap column its lens projects must validate: %v", report.Errors)
	}
}

func TestValidateWeaverTarget_LensNotInstalled_Invalid(t *testing.T) {
	content := weaverTargetArtifact(t, "someExistingLens", "missing_followUp")
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil,
		stubInstalledLenses{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid for a lensRef naming no installed lens")
	}
	want := `lensRef "someExistingLens" names no installed lens; install the lens first`
	if joined := strings.Join(report.Errors, " "); !strings.Contains(joined, want) {
		t.Fatalf("report = %q, want it to contain %q", joined, want)
	}
}

// THE LOOKALIKE (§1's hole, census 3): a canonicalName that happens to be 20
// characters of the NanoID alphabet reads as an id everywhere downstream. The
// validator answers on the catalog alone — no installed lens carries that id —
// so the target that would have installed bound to nothing is invalid here.
func TestValidateWeaverTarget_NanoIDLookalikeCanonicalName_Invalid(t *testing.T) {
	const lookalike = "appointmentReminders"
	if len(lookalike) != 20 {
		t.Fatalf("fixture precondition: %q must be exactly 20 characters to read as an id", lookalike)
	}
	content := weaverTargetArtifact(t, lookalike, "missing_followUp")
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil,
		stubInstalledLenses{lenses: map[string]lenscolumns.Result{
			"someOtherLens": projecting(lenscolumns.ProvenanceReturn, "key"),
		}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("a lookalike canonicalName naming no installed id must not validate")
	}
	if joined := strings.Join(report.Errors, " "); !strings.Contains(joined, "names no installed lens") {
		t.Fatalf("report = %q, want the not-installed verdict", joined)
	}
}

// A wrong-class id — an id that EXISTS, under a class that is not meta.lens —
// answers found=false through the resolver contract, so it lands on the same
// verdict as an absent one: the two holders agree, and the author is told the
// same thing either way. The fixture records the id as present under
// meta.weaverTarget, so a resolver that dropped the class test would resolve it
// (a weaverTarget meta carries a spec aspect too) and this vector would red.
// (CoreKVLensResolver's own class test is proven against a live kernel in
// lensresolver_test.go.)
func TestValidateWeaverTarget_WrongClassID_Invalid(t *testing.T) {
	const targetMetaID = "weaverTargetMetaXYZa"
	content := weaverTargetArtifact(t, targetMetaID, "missing_followUp")
	resolver := stubInstalledLenses{
		roots:  map[string]string{targetMetaID: "meta.weaverTarget"},
		lenses: map[string]lenscolumns.Result{targetMetaID: projecting(lenscolumns.ProvenanceReturn, "key")},
	}
	if _, found, _ := resolver.ResolveLensColumns(targetMetaID); found {
		t.Fatalf("fixture precondition: the class test is what must answer false here")
	}
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid for an id whose root is not a meta.lens")
	}
	if joined := strings.Join(report.Errors, " "); !strings.Contains(joined, "names no installed lens") {
		t.Fatalf("report = %q, want the not-installed verdict", joined)
	}
}

func TestValidateWeaverTarget_UnreadableLens_Invalid(t *testing.T) {
	content := weaverTargetArtifact(t, "someExistingLens", "missing_followUp")
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil,
		stubInstalledLenses{unreadable: map[string]string{"someExistingLens": "per-entry list lens: a runtime shape"}})
	if err != nil {
		t.Fatalf("an unreadable lens is a verdict on the artifact, not a caller error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid for a lens whose columns are not derivable")
	}
	joined := strings.Join(report.Errors, " ")
	if !strings.Contains(joined, `lensRef "someExistingLens" names a lens whose projected columns cannot be derived`) {
		t.Fatalf("report = %q, want the unreadable verdict", joined)
	}
	if !strings.Contains(joined, "per-entry list lens") {
		t.Fatalf("report = %q, want it to name the REASON the columns are not derivable", joined)
	}
}

// A live read that FAILED is not a verdict: the artifact is neither valid nor
// invalid, and the caller — who can retry, or report the substrate — is the
// one told. Reporting it as invalid would record a sound proposal as a bad one
// on a transient.
func TestValidateWeaverTarget_ResolverReadFailure_IsCallerError(t *testing.T) {
	content := weaverTargetArtifact(t, "someExistingLens", "missing_followUp")
	_, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil,
		stubInstalledLenses{readErr: fmt.Errorf("core-kv unreachable")})
	if err == nil {
		t.Fatalf("a failed catalog read must reach the caller as an error, never as an invalid verdict")
	}
	if !strings.Contains(err.Error(), "core-kv unreachable") {
		t.Fatalf("err = %v, want the read failure carried through", err)
	}
}

// The aggregate shape: an actor-aggregate lens's row columns come from its
// Output descriptor, and an undeclared one is refused with the SAME sentence
// the CI lint prints for a package-authored target.
func TestValidateWeaverTarget_AggregateLensUndeclaredColumn_Invalid(t *testing.T) {
	content := weaverTargetArtifact(t, "someExistingLens", "missing_followUp")
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil,
		stubInstalledLenses{lenses: map[string]lenscolumns.Result{
			"someExistingLens": {
				Columns: map[string]string{
					"key":              lenscolumns.ProvenanceBodyColumns,
					"missing_followUp": lenscolumns.ProvenanceBodyColumns,
					"missing_escalate": lenscolumns.ProvenanceStaticEmptyColumns,
				},
				Source: "Output",
			},
		}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid for an undeclared missing_escalate column")
	}
	joined := strings.Join(report.Errors, " ")
	for _, want := range []string{
		`projects gap column "missing_escalate"`,
		lenscolumns.ProvenanceStaticEmptyColumns,
		"declared: missing_followUp",
		"declare it `surface`",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("report = %q, want it to contain %q", joined, want)
		}
	}
}

// The positive twin of the vector above, differing in the gaps map alone.
func TestValidateWeaverTarget_AggregateLensDeclaredColumns_Valid(t *testing.T) {
	content := weaverTargetArtifact(t, "someExistingLens", "missing_followUp", "missing_escalate")
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil,
		stubInstalledLenses{lenses: map[string]lenscolumns.Result{
			"someExistingLens": {
				Columns: map[string]string{
					"key":              lenscolumns.ProvenanceBodyColumns,
					"missing_followUp": lenscolumns.ProvenanceBodyColumns,
					"missing_escalate": lenscolumns.ProvenanceStaticEmptyColumns,
				},
				Source: "Output",
			},
		}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("every projected gap column is declared; want valid, got %v", report.Errors)
	}
}

// The plain shape — the one EVERY artifact lens has: its row columns are its
// RETURN names, so an undeclared gap column there is refused identically.
func TestValidateWeaverTarget_PlainLensUndeclaredColumn_Invalid(t *testing.T) {
	content := weaverTargetArtifact(t, "someExistingLens", "missing_followUp")
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil,
		stubInstalledLenses{lenses: map[string]lenscolumns.Result{
			"someExistingLens": projecting(lenscolumns.ProvenanceReturn, "key", "missing_followUp", "missing_reminder"),
		}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("expected invalid for an undeclared missing_reminder RETURN column")
	}
	joined := strings.Join(report.Errors, " ")
	if !strings.Contains(joined, `projects gap column "missing_reminder" (in RETURN)`) {
		t.Fatalf("report = %q, want the column attributed to the RETURN clause", joined)
	}
}

func TestValidateWeaverTarget_PlainLensDeclaredColumns_Valid(t *testing.T) {
	content := weaverTargetArtifact(t, "someExistingLens", "missing_followUp", "missing_reminder")
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil,
		stubInstalledLenses{lenses: map[string]lenscolumns.Result{
			"someExistingLens": projecting(lenscolumns.ProvenanceReturn, "key", "missing_followUp", "missing_reminder"),
		}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("want valid, got %v", report.Errors)
	}
}

// A `surface` entry IS a declaration: the §10.8 answer for a column that is
// deliberately not remediated. The refusal's own remedy sentence offers it, so
// following that remedy has to actually satisfy the rule.
func TestValidateWeaverTarget_SurfaceEntrySatisfiesTheRule(t *testing.T) {
	content := weaverTargetContent(t, WeaverTargetArtifactContent{
		TargetID: "aiTargetDispatch",
		LensRef:  "someExistingLens",
		Gaps: map[string]GapActionArtifact{
			"missing_followUp": {Action: "surface", IssueCode: "coldFollowUp", IssueSeverity: "warning"},
		},
	})
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil,
		stubInstalledLenses{lenses: map[string]lenscolumns.Result{
			"someExistingLens": projecting(lenscolumns.ProvenanceReturn, "key", "missing_followUp"),
		}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Valid {
		t.Fatalf("a surface entry declares the column; want valid, got %v", report.Errors)
	}
}

// An artifact cannot declare an augur block at all (the unknown-field scan
// refuses one), so the package path's `unplannable` exemption has no way in
// here — an undeclared column is invalid whatever the author writes.
func TestValidateWeaverTarget_NoUnplannableExemptionForArtifacts(t *testing.T) {
	content := json.RawMessage(`{"targetId":"aiTargetDispatch","lensRef":"someExistingLens",` +
		`"gaps":{"missing_followUp":{"action":"directOp","operation":"SendReminder"}},` +
		`"augur":{"escalate":["unplannable"]}}`)
	report, err := ValidateCapabilityArtifact("weaverTarget", content, fullCypherParser{}, nil, nil,
		stubInstalledLenses{lenses: map[string]lenscolumns.Result{
			"someExistingLens": projecting(lenscolumns.ProvenanceReturn, "key", "missing_reminder"),
		}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatalf("an artifact declaring augur is invalid outright; it may not buy the exemption")
	}
}
