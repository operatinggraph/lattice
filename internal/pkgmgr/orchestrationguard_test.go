package pkgmgr

import (
	"strings"
	"testing"
)

func TestValidateWeaverTargets_Valid(t *testing.T) {
	def := Definition{
		WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			LensRef:  "leaseSigningCandidates",
			Gaps: map[string]GapActionSpec{
				"missing_signature": {Action: "assignTask", Operation: "SignLease", Assignee: "row.tenant", Target: "row.lease"},
			},
		}},
	}
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("expected valid weaver target to pass, got: %v", err)
	}
}

func TestValidateWeaverTargets_MissingTargetID(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{TargetID: ""}}}
	if err := def.validateWeaverTargets(); err == nil {
		t.Fatal("expected error for empty TargetID, got nil")
	}
}

func TestValidateWeaverTargets_BadTargetIDKeyShape(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{TargetID: "lease.signing"}}}
	err := def.validateWeaverTargets()
	if err == nil {
		t.Fatal("expected error for dotted TargetID, got nil")
	}
	if !strings.Contains(err.Error(), "lease.signing") {
		t.Errorf("error should name the offending targetId; got %q", err)
	}
}

func TestValidateWeaverTargets_DuplicateTargetID(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{
		{TargetID: "leaseSigning"},
		{TargetID: "leaseSigning"},
	}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-targetId error, got %v", err)
	}
}

func TestValidateWeaverTargets_NonMissingGapKeyRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"signature": {Action: "directOp", Operation: "SignLease"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "missing_<gap>") {
		t.Fatalf("expected missing_<gap> convention error, got %v", err)
	}
}

func TestValidateWeaverTargets_BareMissingPrefixRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_": {Action: "directOp", Operation: "SignLease"}},
	}}}
	if err := def.validateWeaverTargets(); err == nil {
		t.Fatal("expected error for bare missing_ gap key, got nil")
	}
}

func TestValidateWeaverTargets_ReservedExpectedRevisionParamRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps: map[string]GapActionSpec{
			"missing_signature": {Action: "assignTask", Params: map[string]string{"expectedRevision": "3"}},
		},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "expectedRevision") {
		t.Fatalf("expected reserved-param error, got %v", err)
	}
}

func TestValidateWeaverTargets_UnknownActionRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "teleport", Operation: "X"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("expected unknown-action error naming the action, got %v", err)
	}
}

func TestValidateWeaverTargets_EmptyActionRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: ""}},
	}}}
	if err := def.validateWeaverTargets(); err == nil {
		t.Fatal("expected error for empty action, got nil")
	}
}

func TestValidateWeaverTargets_TriggerLoomMissingPattern(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "triggerLoom", Subject: "row.lease"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "Pattern") {
		t.Fatalf("expected triggerLoom missing-Pattern error, got %v", err)
	}
}

func TestValidateWeaverTargets_TriggerLoomMissingSubject(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "triggerLoom", Pattern: "leaseSigning"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "Subject") {
		t.Fatalf("expected triggerLoom missing-Subject error, got %v", err)
	}
}

func TestValidateWeaverTargets_AssignTaskMissingAssignee(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "assignTask", Operation: "SignLease", Target: "row.lease"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "Assignee") {
		t.Fatalf("expected assignTask missing-Assignee error, got %v", err)
	}
}

func TestValidateWeaverTargets_AssignTaskMissingTarget(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "assignTask", Operation: "SignLease", Assignee: "row.tenant"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "Target") {
		t.Fatalf("expected assignTask missing-Target error, got %v", err)
	}
}

func TestValidateWeaverTargets_DirectOpMissingOperation(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "leaseSigning",
		Gaps:     map[string]GapActionSpec{"missing_signature": {Action: "directOp"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "Operation") {
		t.Fatalf("expected directOp missing-Operation error, got %v", err)
	}
}

func TestValidateWeaverTargets_SurfaceMissingIssueCode(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "unroutedTasks",
		Gaps:     map[string]GapActionSpec{"missing_claim": {Action: "surface"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "IssueCode") {
		t.Fatalf("expected surface missing-IssueCode error, got %v", err)
	}
}

func TestValidateWeaverTargets_SurfaceBadIssueSeverityRejected(t *testing.T) {
	def := Definition{WeaverTargets: []WeaverTargetSpec{{
		TargetID: "unroutedTasks",
		Gaps:     map[string]GapActionSpec{"missing_claim": {Action: "surface", IssueCode: "UnroutedTasks", IssueSeverity: "critical"}},
	}}}
	err := def.validateWeaverTargets()
	if err == nil || !strings.Contains(err.Error(), "issueSeverity") {
		t.Fatalf("expected surface bad-issueSeverity error, got %v", err)
	}
}

func TestValidateWeaverTargets_EachActionWellFormedAccepted(t *testing.T) {
	cases := map[string]GapActionSpec{
		"missing_a": {Action: "triggerLoom", Pattern: "leaseSigning", Subject: "row.lease"},
		"missing_c": {Action: "assignTask", Operation: "SignLease", Assignee: "row.tenant", Target: "row.lease"},
		"missing_d": {Action: "directOp", Operation: "MarkExpired"},
		"missing_e": {Action: "surface", IssueCode: "UnroutedTasks", IssueSeverity: "warning"},
	}
	for col, ga := range cases {
		def := Definition{WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			Gaps:     map[string]GapActionSpec{col: ga},
		}}}
		if err := def.validateWeaverTargets(); err != nil {
			t.Fatalf("expected well-formed %s action to pass, got: %v", ga.Action, err)
		}
	}
}

// companionPairDef builds a one-target/one-lens Definition whose lens declares
// exactly the given BodyColumns and whose single gap carries the given action,
// so a case varies only the two things the §10.3 companion-pair gate reads: the
// action's class and what the feeding lens's row body declares.
func companionPairDef(action string, bodyColumns []string) Definition {
	return Definition{
		WeaverTargets: []WeaverTargetSpec{{
			TargetID: "erasureResidue",
			LensRef:  "identityErasureResidue",
			Gaps: map[string]GapActionSpec{
				"missing_dedupResidue": gapActionForCompanionPair(action),
			},
		}},
		Lenses: []LensSpec{{
			CanonicalName:  "identityErasureResidue",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			ProjectionKind: "actorAggregate",
			Spec:           `MATCH (n:identity) RETURN n.key AS key`,
			Output: &OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: "residue.{key}",
				BodyColumns:      bodyColumns,
				EmptyBehavior:    "omit",
			},
		}},
	}
}

// gapActionForCompanionPair returns a well-formed gap spec for the action, so a
// companion-pair case is never decided by an unrelated required-field refusal.
func gapActionForCompanionPair(action string) GapActionSpec {
	switch action {
	case "triggerLoom":
		return GapActionSpec{Action: action, Pattern: "sweepResidue", Subject: "row.identity"}
	case "assignTask":
		return GapActionSpec{Action: action, Operation: "PurgeResidue", Assignee: "row.steward", Target: "row.identity"}
	case "surface":
		return GapActionSpec{Action: action, IssueCode: "ResidueStuck"}
	case "proposedOp":
		return GapActionSpec{Action: action}
	default:
		return GapActionSpec{Action: action, Operation: "PurgeResidue"}
	}
}

// requireCompanionPairRefusal runs the gate and returns the refusal, failing
// rather than panicking when the gate does not fire — a nil dereference here
// would abort the whole package's test binary and swallow every other result.
func requireCompanionPairRefusal(t *testing.T, def Definition) string {
	t.Helper()
	err := def.validateWeaverTargets()
	if err == nil {
		t.Fatal("expected the §10.3 companion-pair gate to refuse, got nil")
	}
	return err.Error()
}

func TestValidateWeaverTargets_ExternalGapInflightWithoutRetryCapRejected(t *testing.T) {
	msg := requireCompanionPairRefusal(t, companionPairDef("directOp", []string{"violating", "inflight_dedupResidue"}))
	for _, want := range []string{"inflight_dedupResidue", "maxretries_dedupResidue", "directOp", "§10.3"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must name %q; got %q", want, msg)
		}
	}
}

// The remedy a refusal states must not be a move that defeats the gate:
// declaring the cap is the advice, and where dropping the marker is named as an
// alternative the message must say WHY that is a real bound — it hands the gap
// back to the engine's default retry budget.
func TestValidateWeaverTargets_CompanionPairRemedyDoesNotDefeatTheGate(t *testing.T) {
	msg := requireCompanionPairRefusal(t, companionPairDef("directOp", []string{"inflight_dedupResidue"}))
	if !strings.Contains(msg, "Declare \"maxretries_dedupResidue\"") {
		t.Errorf("the refusal must advise declaring the cap; got %q", msg)
	}
	if strings.Contains(msg, "Dropping") && !strings.Contains(msg, "default retry budget") {
		t.Errorf("naming the drop as a fix requires saying it restores the engine's default budget; got %q", msg)
	}
}

// The harm the refusal names must be the one that actually occurs. A directOp's
// episode request-id is derived from the mark revision, not from a claimId, so
// the refusal must not attribute the damage to a fresh-claimId reclaim; the real
// consequence is that the dispatch count can never reach a cap and
// GapBudgetExhausted therefore never fires.
func TestValidateWeaverTargets_CompanionPairRefusalNamesTheRealHarm(t *testing.T) {
	msg := requireCompanionPairRefusal(t, companionPairDef("directOp", []string{"inflight_dedupResidue"}))
	if strings.Contains(msg, "claimId") {
		t.Errorf("a directOp episode is markRevision-scoped, not claimId-scoped — the refusal must not blame a fresh claimId; got %q", msg)
	}
	if !strings.Contains(msg, "GapBudgetExhausted") {
		t.Errorf("the refusal must name the §10.8 escalation the missing cap makes unreachable; got %q", msg)
	}
}

func TestValidateWeaverTargets_ExternalGapWithBothCompanionsAccepted(t *testing.T) {
	def := companionPairDef("directOp", []string{"violating", "inflight_dedupResidue", "maxretries_dedupResidue"})
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("a directOp gap declaring both companions must pass: %v", err)
	}
}

// The commonest accepting shape, named so that inverting the gate into "refuse
// every uncapped external gap" — an install-blocking false refusal on the whole
// shipped corpus — fails here and not only incidentally somewhere else. A gap
// declaring NEITHER companion keeps the engine's default retry budget, so it is
// already bounded and §10.3 has nothing to say about it.
func TestValidateWeaverTargets_ExternalGapWithNeitherCompanionAccepted(t *testing.T) {
	def := companionPairDef("directOp", []string{"violating", "missing_dedupResidue", "entityKey"})
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("a directOp gap declaring neither companion must pass — the engine default bounds it: %v", err)
	}
}

// proposedOp is external-class to the engine's classifier, but its gaps get no
// default-budget fallback: "neither companion" and "inflight_<g> only" are
// equally uncapped there, so refusing the second would refuse the
// better-documented of two identical outcomes — and the shipped augur package
// occupies the admitted one.
func TestValidateWeaverTargets_ProposedOpInflightWithoutRetryCapNotRefused(t *testing.T) {
	def := companionPairDef("proposedOp", []string{"violating", "inflight_dedupResidue"})
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("a proposedOp gap has no default budget to decline, so an uncapped inflight_ must not be refused: %v", err)
	}
}

// Refractor's projection driver writes StaticEmptyColumns into the same envelope
// as BodyColumns, so a marker declared there is a PRESENT inflight_<g> key in
// the row the engine reads — the shape that declines the default budget. Reading
// BodyColumns alone would let it through the gate untouched.
func TestValidateWeaverTargets_InflightViaStaticEmptyColumnsRejected(t *testing.T) {
	def := companionPairDef("directOp", []string{"violating", "missing_dedupResidue"})
	def.Lenses[0].Output.StaticEmptyColumns = []string{"inflight_dedupResidue"}
	msg := requireCompanionPairRefusal(t, def)
	if !strings.Contains(msg, "StaticEmptyColumns") {
		t.Errorf("the refusal must say which descriptor list declares the marker, or the author hunts the wrong one; got %q", msg)
	}
	if !strings.Contains(msg, "Output.BodyColumns") {
		t.Errorf("the remedy must point at BodyColumns, where a usable cap can live; got %q", msg)
	}
}

// A cap named in StaticEmptyColumns satisfies the declaration rule the same way,
// since it lands in the row body too — but it projects an empty array, which the
// engine's intColumn reads as no usable cap. The refusal's remedy must therefore
// steer an author to BodyColumns rather than to the list that would silence the
// gate without bounding anything.
func TestValidateWeaverTargets_RetryCapViaStaticEmptyColumnsSatisfiesTheDeclaration(t *testing.T) {
	def := companionPairDef("directOp", []string{"violating", "inflight_dedupResidue"})
	def.Lenses[0].Output.StaticEmptyColumns = []string{"maxretries_dedupResidue"}
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("a cap declared in StaticEmptyColumns is still a declared row-body column: %v", err)
	}
	msg := requireCompanionPairRefusal(t, companionPairDef("directOp", []string{"inflight_dedupResidue"}))
	if !strings.Contains(msg, "empty array") {
		t.Errorf("the remedy must warn that a StaticEmptyColumns cap is not a usable one; got %q", msg)
	}
}

// A cap with no in-flight marker is the ordinary bounded shape — §10.3 binds the
// implication one way only.
func TestValidateWeaverTargets_RetryCapWithoutInflightAccepted(t *testing.T) {
	def := companionPairDef("directOp", []string{"violating", "maxretries_dedupResidue"})
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("maxretries_ without inflight_ must pass: %v", err)
	}
}

// The gate reads only the gap it is validating: a companion pair belonging to a
// DIFFERENT gap name neither satisfies nor triggers this gap's check.
func TestValidateWeaverTargets_CompanionPairIsPerGap(t *testing.T) {
	msg := requireCompanionPairRefusal(t, companionPairDef("directOp",
		[]string{"inflight_dedupResidue", "maxretries_erasureSeal"}))
	if !strings.Contains(msg, "maxretries_dedupResidue") {
		t.Errorf("refusal must name this gap's own companion; got %q", msg)
	}
}

// A target's Gaps is a Go map, so an unsorted range would report a different one
// of two offending gaps per run. The refusal must be reproducible: sorted
// iteration makes the lexicographically first offender the one always named.
func TestValidateWeaverTargets_MultipleOffendingGapsRefuseDeterministically(t *testing.T) {
	def := companionPairDef("directOp", []string{"inflight_aResidue", "inflight_zResidue"})
	def.WeaverTargets[0].Gaps = map[string]GapActionSpec{
		"missing_aResidue": gapActionForCompanionPair("directOp"),
		"missing_zResidue": gapActionForCompanionPair("directOp"),
	}
	first := requireCompanionPairRefusal(t, def)
	if !strings.Contains(first, "missing_aResidue") {
		t.Fatalf("the lexicographically first offending gap must be the one named; got %q", first)
	}
	for i := 0; i < 32; i++ {
		if again := requireCompanionPairRefusal(t, def); again != first {
			t.Fatalf("refusal is not reproducible across runs:\n first: %s\n later: %s", first, again)
		}
	}
}

// Fail-open boundary: the classifier decides triggerLoom from the referenced
// pattern's step kinds, not from the action name, and that reference may be a
// row.<column> template — undecidable at install, so the gate must not refuse.
// assignTask and surface never make an external call at all.
func TestValidateWeaverTargets_NonStaticallyExternalActionsNotRefused(t *testing.T) {
	for _, action := range []string{"triggerLoom", "assignTask", "surface"} {
		t.Run(action, func(t *testing.T) {
			def := companionPairDef(action, []string{"violating", "inflight_dedupResidue"})
			if err := def.validateWeaverTargets(); err != nil {
				t.Fatalf("%s is not statically external-class, so an uncapped inflight_ must not be refused: %v", action, err)
			}
		})
	}
}

// Fail-open boundary: a LensRef that resolves to no lens in this batch — a
// NanoID naming an already-installed lens another package owns — leaves the
// declaration unreadable here. Refusing on absence would fail every
// cross-package target.
func TestValidateWeaverTargets_UnresolvableLensRefNotRefused(t *testing.T) {
	def := companionPairDef("directOp", []string{"inflight_dedupResidue"})
	def.WeaverTargets[0].LensRef = "V1StGXR8Z5jdHi6BmyT0"
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("a LensRef naming no lens in this batch must be skipped, not refused: %v", err)
	}
}

// Fail-open boundary: a lens with no Output descriptor is not an actor-aggregate
// and declares no row body for the gate to read.
func TestValidateWeaverTargets_NilLensOutputNotRefused(t *testing.T) {
	def := companionPairDef("directOp", []string{"inflight_dedupResidue"})
	def.Lenses[0].Output = nil
	def.Lenses[0].ProjectionKind = ""
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("a lens with a nil Output must be skipped, not refused: %v", err)
	}
}

// An empty LensRef binds no lens at all: resolveLensRef returns early on it
// rather than consulting the canonicalName map, so the gate must too. Without
// that early return a lens carrying an empty CanonicalName — which nothing
// forbids — would be matched and validated against a target that never
// referenced it.
func TestValidateWeaverTargets_EmptyLensRefBindsNoLens(t *testing.T) {
	def := companionPairDef("directOp", []string{"inflight_dedupResidue"})
	def.WeaverTargets[0].LensRef = ""
	def.Lenses[0].CanonicalName = ""
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("an empty LensRef binds no lens, so the gate has nothing to read: %v", err)
	}
}

// The gate must resolve a LensRef the way the installer does. Two lenses sharing
// a canonicalName is refused by validateCanonicalNameUniqueness — but that runs
// AFTER validateWeaverTargets, so a first-wins gate here would mask the real
// error with a companion-pair refusal about a lens the build (last-wins, via
// resolveLensRef's overwritten map) would never have bound.
func TestValidateWeaverTargets_DuplicateLensCanonicalNameResolvesLastWins(t *testing.T) {
	def := companionPairDef("directOp", []string{"violating", "inflight_dedupResidue"})
	capped := companionPairDef("directOp", []string{"violating", "inflight_dedupResidue", "maxretries_dedupResidue"})
	def.Lenses = append(def.Lenses, capped.Lenses[0])
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("the LAST lens declaring the canonicalName is the one the installer binds, and it is capped: %v", err)
	}
	if err := def.validateAll(); err == nil || !strings.Contains(err.Error(), "canonicalName") {
		t.Fatalf("the duplicate canonicalName must still be refused by its own validator, not masked: %v", err)
	}
}

// TestValidateGapCompanionPair_ReachedThroughValidateAll drives the whole
// validateAll chain over a Definition legal in every other respect, asserting
// wording only this member emits — so a short-circuit from an earlier validator
// cannot pass for it, and removing validateWeaverTargets from validateAll's list
// breaks this test rather than silently disarming the gate.
func TestValidateGapCompanionPair_ReachedThroughValidateAll(t *testing.T) {
	def := sampleDef("0.1.0")
	companion := companionPairDef("directOp", []string{"violating", "inflight_dedupResidue"})
	def.Lenses = append(def.Lenses, companion.Lenses...)
	def.WeaverTargets = companion.WeaverTargets

	// The same Definition minus the missing companion must pass validateAll
	// outright, so the refusal below can only be this gate's.
	capped := def
	capped.Lenses = append([]LensSpec(nil), def.Lenses...)
	capped.Lenses[1] = *companionPairDef("directOp",
		[]string{"violating", "inflight_dedupResidue", "maxretries_dedupResidue"}).lensByCanonicalName("identityErasureResidue")
	if err := capped.validateAll(); err != nil {
		t.Fatalf("the fixture must be legal in every other respect; got: %v", err)
	}

	err := def.validateAll()
	if err == nil {
		t.Fatal("validateAll() must refuse an external gap declaring inflight_ with no maxretries_")
	}
	if !strings.Contains(err.Error(), "Contract #10 §10.3 requires the companion pair") {
		t.Fatalf("validateAll() failed on something other than the companion-pair gate: %v", err)
	}
}

func TestValidateLoomPatterns_Valid(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "leaseSigning",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "systemOp", Operation: "SignLease"}},
	}}}
	if err := def.validateLoomPatterns(); err != nil {
		t.Fatalf("expected valid pattern to pass, got: %v", err)
	}
}

func TestValidateLoomPatterns_MissingPatternID(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{SubjectType: "lease", Steps: []StepSpec{{Kind: "systemOp", Operation: "X"}}}}}
	if err := def.validateLoomPatterns(); err == nil {
		t.Fatal("expected error for empty PatternID, got nil")
	}
}

func TestValidateLoomPatterns_MissingSubjectType(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{PatternID: "p", Steps: []StepSpec{{Kind: "systemOp", Operation: "X"}}}}}
	if err := def.validateLoomPatterns(); err == nil {
		t.Fatal("expected error for empty SubjectType, got nil")
	}
}

func TestValidateLoomPatterns_NoSteps(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{PatternID: "p", SubjectType: "lease"}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "at least one step") {
		t.Fatalf("expected no-steps error, got %v", err)
	}
}

func TestValidateLoomPatterns_BadStepKind(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "magic", Operation: "X"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected bad-step-kind error, got %v", err)
	}
}

func TestValidateLoomPatterns_EmptyStepOperation(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "systemOp", Operation: ""}},
	}}}
	if err := def.validateLoomPatterns(); err == nil {
		t.Fatal("expected error for empty step operation, got nil")
	}
}

func TestValidateLoomPatterns_ExternalTaskValid(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "leaseSigning",
		SubjectType: "lease",
		Steps: []StepSpec{{
			Kind:       "externalTask",
			Adapter:    "docusign",
			InstanceOp: "CreateSigningInstance",
			ReplyOp:    "ResolveSigning",
			Params:     map[string]any{"template": "lease"},
		}},
	}}}
	if err := def.validateLoomPatterns(); err != nil {
		t.Fatalf("expected valid externalTask pattern to pass, got: %v", err)
	}
}

func TestValidateLoomPatterns_ExternalTaskNoOperationRequired(t *testing.T) {
	// An externalTask must NOT require `operation` — its op vocabulary is
	// instanceOp/replyOp.
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "externalTask", Adapter: "docusign", InstanceOp: "CreateSigningInstance", ReplyOp: "ResolveSigning"}},
	}}}
	if err := def.validateLoomPatterns(); err != nil {
		t.Fatalf("externalTask must not require operation, got: %v", err)
	}
}

func TestValidateLoomPatterns_ExternalTaskMissingAdapter(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "externalTask", InstanceOp: "CreateSigningInstance", ReplyOp: "ResolveSigning"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("expected externalTask missing-adapter error, got %v", err)
	}
}

func TestValidateLoomPatterns_ExternalTaskMissingInstanceOp(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "externalTask", Adapter: "docusign", ReplyOp: "ResolveSigning"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "instanceOp") {
		t.Fatalf("expected externalTask missing-instanceOp error, got %v", err)
	}
}

func TestValidateLoomPatterns_ExternalTaskMissingReplyOp(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "externalTask", Adapter: "docusign", InstanceOp: "CreateSigningInstance"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "replyOp") {
		t.Fatalf("expected externalTask missing-replyOp error, got %v", err)
	}
}

func TestValidateLoomPatterns_SystemOpStillRequiresOperation(t *testing.T) {
	// The externalTask branch must not relax the systemOp/userTask operation
	// requirement.
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "systemOp", Operation: ""}},
	}}}
	if err := def.validateLoomPatterns(); err == nil {
		t.Fatal("expected systemOp without operation to still be rejected, got nil")
	}
}

func TestValidateLoomPatterns_SystemOpWithStrayInstanceOpRejected(t *testing.T) {
	// A systemOp carrying an externalTask-only field must be rejected fail-closed
	// rather than validating clean with the foreign field silently ignored.
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "systemOp", Operation: "SignLease", InstanceOp: "CreateSigningInstance"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "instanceOp") {
		t.Fatalf("expected systemOp stray-instanceOp error, got %v", err)
	}
}

func TestValidateLoomPatterns_UserTaskWithStrayAdapterRejected(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps:       []StepSpec{{Kind: "userTask", Operation: "SignLease", Adapter: "docusign"}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "adapter") {
		t.Fatalf("expected userTask stray-adapter error, got %v", err)
	}
}

func TestValidateLoomPatterns_ExternalTaskWithStrayOperationRejected(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{{
		PatternID:   "p",
		SubjectType: "lease",
		Steps: []StepSpec{{
			Kind:       "externalTask",
			Adapter:    "docusign",
			InstanceOp: "CreateSigningInstance",
			ReplyOp:    "ResolveSigning",
			Operation:  "SignLease",
		}},
	}}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "operation") {
		t.Fatalf("expected externalTask stray-operation error, got %v", err)
	}
}

func TestValidateLoomPatterns_DuplicatePatternID(t *testing.T) {
	def := Definition{LoomPatterns: []LoomPatternSpec{
		{PatternID: "leaseSigning", SubjectType: "lease", Steps: []StepSpec{{Kind: "systemOp", Operation: "X"}}},
		{PatternID: "leaseSigning", SubjectType: "lease", Steps: []StepSpec{{Kind: "systemOp", Operation: "Y"}}},
	}}
	err := def.validateLoomPatterns()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-patternId error, got %v", err)
	}
}

func TestValidateOpMetas_Valid(t *testing.T) {
	def := Definition{OpMetas: []OpMetaSpec{{OperationType: "SignLease"}}}
	if err := def.validateOpMetas(); err != nil {
		t.Fatalf("expected valid op-meta to pass, got: %v", err)
	}
}

func TestValidateOpMetas_EmptyOperationType(t *testing.T) {
	def := Definition{OpMetas: []OpMetaSpec{{OperationType: ""}}}
	if err := def.validateOpMetas(); err == nil {
		t.Fatal("expected error for empty OperationType, got nil")
	}
}

func TestValidateOpMetas_BadToken(t *testing.T) {
	def := Definition{OpMetas: []OpMetaSpec{{OperationType: "Sign.Lease"}}}
	if err := def.validateOpMetas(); err == nil {
		t.Fatal("expected error for dotted OperationType, got nil")
	}
}

func TestValidateOpMetas_DuplicateOperationType(t *testing.T) {
	def := Definition{OpMetas: []OpMetaSpec{
		{OperationType: "SignLease"},
		{OperationType: "SignLease"},
	}}
	err := def.validateOpMetas()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-operationType error, got %v", err)
	}
}

func augurTargetDef(a *AugurSpec) Definition {
	return Definition{
		WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			LensRef:  "leaseSigningCandidates",
			Gaps: map[string]GapActionSpec{
				"missing_approval": {Action: "assignTask", Operation: "Approve", Assignee: "row.a", Target: "row.t"},
			},
			Augur: a,
		}},
	}
}

func TestValidateWeaverTargets_AugurNilOK(t *testing.T) {
	if err := augurTargetDef(nil).validateWeaverTargets(); err != nil {
		t.Fatalf("a target with no augur block must pass: %v", err)
	}
}

func TestValidateWeaverTargets_AugurValid(t *testing.T) {
	// Option F: no loom pattern — Weaver dispatches the reasoning op directly as a
	// directOp, so the block is just escalate + the optional op/adapter/replyOp
	// overrides (defaulted at dispatch when omitted).
	def := augurTargetDef(&AugurSpec{
		Escalate: []string{"unplannable", "exhausted"},
		Op:       "CreateAugurReasoningClaim",
		Adapter:  "augur",
		ReplyOp:  "RecordProposal",
		Model:    "claude-opus-4-8",
		AutoApply: &AugurAutoApplySpec{
			Actions: []string{"triggerLoom", "directOp"}, MinConfidence: 0.9,
		},
	})
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("a fully-populated valid augur block must pass: %v", err)
	}
}

func TestValidateWeaverTargets_AugurMinimalOK(t *testing.T) {
	// The minimal block: one trigger, no overrides (op/adapter/replyOp default at
	// dispatch). No pattern is required anymore (Option F).
	if err := augurTargetDef(&AugurSpec{Escalate: []string{"unplannable"}}).validateWeaverTargets(); err != nil {
		t.Fatalf("a minimal augur block (no overrides) must pass: %v", err)
	}
}

func TestValidateWeaverTargets_AugurRejections(t *testing.T) {
	cases := []struct {
		name    string
		spec    *AugurSpec
		wantSub string
	}{
		{"empty escalate", &AugurSpec{}, "escalate is empty"},
		{"unknown trigger", &AugurSpec{Escalate: []string{"someday"}}, "not a known trigger"},
		{"bad op token", &AugurSpec{Escalate: []string{"unplannable"}, Op: "bad.op"}, "single token"},
		{"bad adapter token", &AugurSpec{Escalate: []string{"unplannable"}, Adapter: "a b"}, "single token"},
		{"bad model token", &AugurSpec{Escalate: []string{"unplannable"}, Model: "claude opus 4.8"}, "single token"},
		{"bad autoApply action", &AugurSpec{Escalate: []string{"unplannable"},
			AutoApply: &AugurAutoApplySpec{Actions: []string{"DropTable"}}}, "not a known action"},
		{"minConfidence too high", &AugurSpec{Escalate: []string{"unplannable"},
			AutoApply: &AugurAutoApplySpec{MinConfidence: 1.5}}, "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := augurTargetDef(tc.spec).validateWeaverTargets()
			if err == nil {
				t.Fatalf("%s: must be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("%s: unexpected reason: %v", tc.name, err)
			}
		})
	}
}

func admissionTargetDef(a *AdmissionSpec) Definition {
	return Definition{
		WeaverTargets: []WeaverTargetSpec{{
			TargetID: "leaseSigning",
			LensRef:  "leaseSigningCandidates",
			Gaps: map[string]GapActionSpec{
				"missing_approval": {Action: "assignTask", Operation: "Approve", Assignee: "row.a", Target: "row.t"},
			},
			Admission: a,
		}},
	}
}

func TestValidateWeaverTargets_AdmissionNilOK(t *testing.T) {
	if err := admissionTargetDef(nil).validateWeaverTargets(); err != nil {
		t.Fatalf("a target with no admission block must pass: %v", err)
	}
}

func TestValidateWeaverTargets_AdmissionValid(t *testing.T) {
	def := admissionTargetDef(&AdmissionSpec{
		GlobalRate:   10,
		AdapterRates: map[string]float64{"backgroundCheck": 2, "stripe": 5},
	})
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("a fully-populated valid admission block must pass: %v", err)
	}
}

func TestValidateWeaverTargets_AdmissionAdapterOnlyOK(t *testing.T) {
	def := admissionTargetDef(&AdmissionSpec{AdapterRates: map[string]float64{"stripe": 5}})
	if err := def.validateWeaverTargets(); err != nil {
		t.Fatalf("adapterRates alone (no globalRate) must pass: %v", err)
	}
}

func TestValidateWeaverTargets_AdmissionRejections(t *testing.T) {
	cases := []struct {
		name    string
		spec    *AdmissionSpec
		wantSub string
	}{
		{"empty block", &AdmissionSpec{}, "declares no positive rate"},
		{"negative globalRate", &AdmissionSpec{GlobalRate: -1}, "must be >= 0"},
		{"zero adapter rate", &AdmissionSpec{AdapterRates: map[string]float64{"stripe": 0}}, "must be > 0"},
		{"negative adapter rate", &AdmissionSpec{AdapterRates: map[string]float64{"stripe": -5}}, "must be > 0"},
		{"empty adapter key", &AdmissionSpec{AdapterRates: map[string]float64{"": 5}}, "empty adapter key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := admissionTargetDef(tc.spec).validateWeaverTargets()
			if err == nil {
				t.Fatalf("%s: must be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("%s: unexpected reason: %v", tc.name, err)
			}
		})
	}
}
