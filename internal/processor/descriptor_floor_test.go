package processor

import (
	"encoding/json"
	"slices"
	"testing"
)

func floorEnv(payload string) *OperationEnvelope {
	return &OperationEnvelope{
		RequestID:     testNanoID1,
		Lane:          LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         "vtx.identity." + testNanoID2,
		Payload:       json.RawMessage(payload),
	}
}

const floorTarget = "vtx.identity.Aa1Bb2Cc3Dd4Ee5Ff6Gg"

func floorPayload() string {
	b, _ := json.Marshal(map[string]string{"targetIdentityKey": floorTarget})
	return string(b)
}

// TestDescriptorFloor_DemotesEnvelopeReads is the mechanism the contract asks
// for: a key the descriptor declares optional cannot be hardened by the
// envelope. The key MOVES — asserting it left Reads matters as much as
// asserting it reached OptionalReads, because step4_hydrate's both-lists rule
// re-hardens anything left in Reads.
func TestDescriptorFloor_DemotesEnvelopeReads(t *testing.T) {
	base := declaredReads{
		Reads: []string{floorTarget, floorTarget + ".state", "vtx.task." + testNanoID2},
	}
	templates := []string{"{payload.targetIdentityKey}", "{payload.targetIdentityKey}.state"}

	got := applyDescriptorFloor(base, templates, floorEnv(floorPayload()), testLogger())

	if slices.Contains(got.Reads, floorTarget) || slices.Contains(got.Reads, floorTarget+".state") {
		t.Fatalf("Reads = %v, want the floored keys REMOVED — a key left in both lists is re-hardened by step 4", got.Reads)
	}
	if !slices.Contains(got.Reads, "vtx.task."+testNanoID2) {
		t.Fatalf("Reads = %v, want the unfloored key kept — the floor demotes only what the descriptor names", got.Reads)
	}
	for _, want := range []string{floorTarget, floorTarget + ".state"} {
		if !slices.Contains(got.OptionalReads, want) {
			t.Fatalf("OptionalReads = %v, want %q", got.OptionalReads, want)
		}
	}
}

// TestDescriptorFloor_NoDescriptorTemplatesLeavesEnvelopeAlone: an operation
// whose descriptor declares no optionalReads — the ordinary case, most of the
// corpus — is untouched. The rule has no subject, and demoting on doubt would
// soften reads the operation genuinely depends on.
func TestDescriptorFloor_NoDescriptorTemplatesLeavesEnvelopeAlone(t *testing.T) {
	base := declaredReads{Reads: []string{floorTarget, floorTarget + ".state"}}
	got := applyDescriptorFloor(base, nil, floorEnv(floorPayload()), testLogger())
	if !slices.Equal(got.Reads, base.Reads) || len(got.OptionalReads) != 0 {
		t.Fatalf("got %+v, want the envelope's declaration verbatim", got)
	}
}

// TestDescriptorFloor_DescriptorReadsAreNotAFloor is the negative control the
// contract names explicitly: "a descriptor that declares a key under `reads` is
// unaffected". Only the descriptor's optionalReads list is a floor, so a
// descriptor-`reads` key handed here as a template it does not own must not be
// demoted. Modelled by passing the empty optional list a reads-only descriptor
// produces.
func TestDescriptorFloor_DescriptorReadsAreNotAFloor(t *testing.T) {
	base := declaredReads{Reads: []string{floorTarget}}
	got := applyDescriptorFloor(base, []string{}, floorEnv(floorPayload()), testLogger())
	if !slices.Equal(got.Reads, []string{floorTarget}) {
		t.Fatalf("Reads = %v, want the key still fail-closed", got.Reads)
	}
	if len(got.OptionalReads) != 0 {
		t.Fatalf("OptionalReads = %v, want empty", got.OptionalReads)
	}
}

// TestDescriptorFloor_KeyInBothEnvelopeListsIsRemovedFromReads: an envelope
// naming the same key in BOTH lists keeps fail-closed semantics today
// (step4_hydrate's duplicate rule). Under the floor that reading must not
// survive, so the key has to leave Reads rather than merely appear in
// OptionalReads — where it already was.
func TestDescriptorFloor_KeyInBothEnvelopeListsIsRemovedFromReads(t *testing.T) {
	base := declaredReads{
		Reads:         []string{floorTarget},
		OptionalReads: []string{floorTarget},
	}
	got := applyDescriptorFloor(base, []string{"{payload.targetIdentityKey}"}, floorEnv(floorPayload()), testLogger())
	if slices.Contains(got.Reads, floorTarget) {
		t.Fatalf("Reads = %v, want %q gone — leaving it lets the both-lists rule keep the fail-closed reading", got.Reads, floorTarget)
	}
	if n := len(got.OptionalReads); n != 1 {
		t.Fatalf("OptionalReads = %v, want the single existing entry, not a duplicate", got.OptionalReads)
	}
}

// TestDescriptorFloor_EgressIsMarkedNotMoved: the floor reaches egressReads,
// but by marking rather than relocating.
//
// Moving an egress key into optionalReads would hand the script the decrypted
// body where the submitter asked for a `$sensitiveRef` the bridge opens — a
// disposition change in the dangerous direction, and a worse outcome than the
// fault it would avoid. So the key stays in egressReads and only its ABSENCE
// becomes tolerant. The list membership itself is what keeps ParseEnvelope's
// egress/read mutual exclusion and mergeDerivedReads' re-check seeing exactly
// the sets they would have seen.
func TestDescriptorFloor_EgressIsMarkedNotMoved(t *testing.T) {
	base := declaredReads{
		Reads:       []string{floorTarget},
		EgressReads: []string{floorTarget + ".ssn"},
	}
	templates := []string{"{payload.targetIdentityKey}", "{payload.targetIdentityKey}.ssn"}
	got := applyDescriptorFloor(base, templates, floorEnv(floorPayload()), testLogger())

	if !slices.Equal(got.EgressReads, []string{floorTarget + ".ssn"}) {
		t.Fatalf("EgressReads = %v, want the key still declared egress", got.EgressReads)
	}
	if slices.Contains(got.Reads, floorTarget+".ssn") || slices.Contains(got.OptionalReads, floorTarget+".ssn") {
		t.Fatalf("the egress key was pulled into a read list — that is a plaintext leak, not a demotion: %+v", got)
	}
	if _, tolerant := got.EgressAbsenceTolerant[floorTarget+".ssn"]; !tolerant {
		t.Fatalf("EgressAbsenceTolerant = %v, want the floored egress key — otherwise its absence still faults HydrationMiss and echoes the key", got.EgressAbsenceTolerant)
	}
	// The unfloored half of the same envelope keeps ordinary fail-closed
	// egress semantics.
	if _, tolerant := got.EgressAbsenceTolerant["vtx.identity.Zz9Yy8Xx7Ww6Vv5Uu4Tt.ssn"]; tolerant {
		t.Fatalf("an unfloored key must not become tolerant")
	}
}

// TestHydrate_FlooredEgressAbsenceIsKnownNotRequired is the same statement at
// the seam that decides it: RequiredAbsent is a flat map with no memory of
// which list named a key, so an absent egress key is the same oracle reached
// through the third list.
//
// It also pins how far the change reaches. Those two maps are read past step 4,
// so moving a key between them switches it between two different sets of
// downstream rules — the write-side dependence fault and the absent-conditioned
// create. Asserting the map alone would leave both readers unstated, and they
// are the difference between "the reply says less" and "the write behaves
// differently".
func TestHydrate_FlooredEgressAbsenceIsKnownNotRequired(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, tplID, "CreateIdentity",
		dispatchAspect(root, `["{payload.targetIdentityKey}.ssn"]`, false), false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h := NewHydratorWithCache(conn, testCoreBucket, cache, testLogger())

	absent := "vtx.identity." + instID
	egressKey := absent + ".ssn"
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "CreateIdentity"
	env.Payload = []byte(`{"targetIdentityKey":"` + absent + `"}`)
	env.ContextHint = &ContextHint{EgressReads: []string{egressKey}}

	state, err := h.Hydrate(ctx, env)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if _, ok := state.Context.RequiredAbsent[egressKey]; ok {
		t.Fatalf("%s is required-absent; egressReads reproduces the oracle past the floor", egressKey)
	}
	if _, ok := state.Context.KnownAbsent[egressKey]; !ok {
		t.Fatalf("KnownAbsent = %v, want the floored egress key", state.Context.KnownAbsent)
	}

	// Both readers of those maps, at one mutation. A `create` on the floored
	// key no longer trips the write-side dependence fault, and it joins the
	// absent-conditioned set the commit path re-probes on a conflict.
	create := []MutationOp{{Op: "create", Key: egressKey, Document: map[string]interface{}{"class": "ssn"}}}
	if key := firstRequiredAbsentMutation(create, state.Context.RequiredAbsent); key != "" {
		t.Fatalf("firstRequiredAbsentMutation = %q, want none — a floored key is not a dependence the write side faults on", key)
	}
	if got := absentConditionedCreates(create, state.Context.KnownAbsent); !slices.Equal(got, []string{egressKey}) {
		t.Fatalf("absentConditionedCreates = %v, want [%s] — a create on a known-absent key is conditioned on that absence and so is retry-eligible", got, egressKey)
	}

	// The control: the same envelope against an operation with NO descriptor.
	// Both readers answer the other way, which is what shows the two answers
	// above are the floor's doing rather than the shape of the mutation.
	unfloored := newTestEnvelope(testNanoID1)
	unfloored.OperationType = "UndescribedOp"
	unfloored.Payload = env.Payload
	unfloored.ContextHint = &ContextHint{EgressReads: []string{egressKey}}
	plain, err := h.Hydrate(ctx, unfloored)
	if err != nil {
		t.Fatalf("Hydrate (no descriptor): %v", err)
	}
	if _, ok := plain.Context.RequiredAbsent[egressKey]; !ok {
		t.Fatalf("RequiredAbsent = %v, want the egress key — with no descriptor the envelope's own disposition is the last word", plain.Context.RequiredAbsent)
	}
	if key := firstRequiredAbsentMutation(create, plain.Context.RequiredAbsent); key != egressKey {
		t.Fatalf("firstRequiredAbsentMutation = %q, want %q", key, egressKey)
	}
	if got := absentConditionedCreates(create, plain.Context.KnownAbsent); len(got) != 0 {
		t.Fatalf("absentConditionedCreates = %v, want none", got)
	}
}

// TestDescriptorFloor_UnresolvableTemplateDemotesNothing: `{me.<type>}` names
// a vertex out of the caller's own projected view, which the Processor does
// not have. It resolves to no key, so it floors nothing — and specifically it
// must not become a wildcard that softens whatever the envelope happened to
// declare.
func TestDescriptorFloor_UnresolvableTemplateDemotesNothing(t *testing.T) {
	base := declaredReads{Reads: []string{floorTarget, "vtx.leaseapp." + testNanoID2}}
	templates := []string{"{me.leaseapp}", "{entity.unitKey}", "{payload.notInPayload}"}
	got := applyDescriptorFloor(base, templates, floorEnv(floorPayload()), testLogger())
	if !slices.Equal(got.Reads, base.Reads) {
		t.Fatalf("Reads = %v, want unchanged — an unresolvable template names no key", got.Reads)
	}
	if len(got.OptionalReads) != 0 {
		t.Fatalf("OptionalReads = %v, want empty", got.OptionalReads)
	}
}

// TestSubstituteDescriptorTemplate covers the server-resolvable vocabulary and
// every way a substitution is refused. A refusal is always "no key", never a
// partial one: a hole leaves a DIFFERENT key, not a shorter one.
func TestSubstituteDescriptorTemplate(t *testing.T) {
	actor := "vtx.identity." + testNanoID2
	env := floorEnv(floorPayload())
	env.Actor = actor
	env.AuthContext = &AuthContext{Service: "vtx.service." + testNanoID1}

	ok := map[string]string{
		"{actor}":                              actor,
		"{actor}.state":                        actor + ".state",
		"{payload.targetIdentityKey}":          floorTarget,
		"{payload.targetIdentityKey}.claimKey": floorTarget + ".claimKey",
		"{service}":                            "vtx.service." + testNanoID1,
		"lnk.task." + testNanoID1 + ".assignedTo.identity.{actor:id}": "lnk.task." + testNanoID1 + ".assignedTo.identity." + testNanoID2,
		"vtx.session.{payload.targetIdentityKey:id}.bkr{actor:id}":    "vtx.session.Aa1Bb2Cc3Dd4Ee5Ff6Gg.bkr" + testNanoID2,
	}
	for tpl, want := range ok {
		got, resolved := substituteDescriptorTemplate(tpl, env, mustPayload(t, env))
		if !resolved || got != want {
			t.Fatalf("substitute(%q) = (%q, %v), want (%q, true)", tpl, got, resolved, want)
		}
	}

	refused := []string{
		"{me.leaseapp}",              // the caller's projected view, not the Processor's
		"{entity.unitKey}",           // ditto
		"{scopedTo}",                 // a task's target, resolved client-side
		"{payload.missing}",          // no such payload field
		"{payload.targetIdentityKey", // unterminated
		"{actor:id}.{me.thing:id}",   // one resolvable half is not enough
	}
	for _, tpl := range refused {
		if got, resolved := substituteDescriptorTemplate(tpl, env, mustPayload(t, env)); resolved {
			t.Fatalf("substitute(%q) = (%q, true), want refused", tpl, got)
		}
	}

	// A `:id` modifier over a value that is not a vertex key is refused rather
	// than truncated — a bare id taken off a malformed key addresses something
	// else.
	badIDEnv := floorEnv(`{"targetIdentityKey":"notakey"}`)
	if got, resolved := substituteDescriptorTemplate("lnk.task.x.assignedTo.identity.{payload.targetIdentityKey:id}", badIDEnv, mustPayload(t, badIDEnv)); resolved {
		t.Fatalf("substitute over a malformed :id source = (%q, true), want refused", got)
	}
}

func mustPayload(t *testing.T, env *OperationEnvelope) map[string]interface{} {
	t.Helper()
	m, _ := jsonToGenericMap(env.Payload)
	return m
}
