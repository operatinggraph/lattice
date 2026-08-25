package processor

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
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

// The ids a pattern's wildcard segment is asked to cover. floorLeaseVictim is
// deliberately never derivable from the envelope: it stands for an entity the
// submitter names but the caller's own projected view would not.
const (
	floorLeaseMine   = "Mm4Nn5Pp6Qq7Rr8Ss9Tt"
	floorLeaseVictim = "Vv7Ww8Xx9Yy1Zz2Aa3Bb"
	floorTabID       = "Cc3Dd4Ee5Ff6Gg7Hh8Jj"
)

func floorPayload() string {
	b, _ := json.Marshal(map[string]string{"targetIdentityKey": floorTarget})
	return string(b)
}

// floorFor is the descriptor shape most of this file exercises: an
// optionalReads floor and no required templates.
func floorFor(templates ...string) DispatchTemplates {
	return DispatchTemplates{OptionalReads: templates}
}

// applyFloor runs the envelope arm the way step 4 does: one resolver built
// from the descriptor's templates and this envelope, then the demotion pass
// over it. Going through the resolver is what keeps these tests exercising the
// production path — the same value the merge arm consults.
func applyFloor(base declaredReads, templates DispatchTemplates, env *OperationEnvelope, logger *slog.Logger) declaredReads {
	return applyDescriptorFloor(base, newDescriptorFloorResolver(templates, env, logger))
}

// capturingLogger records what the floor SAYS as well as what it does. A
// template that contributes nothing is only observable through its Warn, so a
// test asserting the no-contribution posture has to read the log to tell it
// from a template that silently never reached the compiler.
func capturingLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})), buf
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
	templates := floorFor("{payload.targetIdentityKey}", "{payload.targetIdentityKey}.state")

	got := applyFloor(base, templates, floorEnv(floorPayload()), testLogger())

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
	got := applyFloor(base, DispatchTemplates{}, floorEnv(floorPayload()), testLogger())
	if !slices.Equal(got.Reads, base.Reads) || len(got.OptionalReads) != 0 {
		t.Fatalf("got %+v, want the envelope's declaration verbatim", got)
	}
}

// TestDescriptorFloor_DescriptorReadsAreNotAFloor is the negative control the
// contract names explicitly: "a descriptor that declares a key under `reads` is
// unaffected". A descriptor whose only templates are required ones floors
// nothing at all.
func TestDescriptorFloor_DescriptorReadsAreNotAFloor(t *testing.T) {
	base := declaredReads{Reads: []string{floorTarget}}
	templates := DispatchTemplates{Reads: []string{"{payload.targetIdentityKey}"}}
	got := applyFloor(base, templates, floorEnv(floorPayload()), testLogger())
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
	got := applyFloor(base, floorFor("{payload.targetIdentityKey}"), floorEnv(floorPayload()), testLogger())
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
	templates := floorFor("{payload.targetIdentityKey}", "{payload.targetIdentityKey}.ssn")
	got := applyFloor(base, templates, floorEnv(floorPayload()), testLogger())

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

// TestDescriptorFloor_UncompilableTemplateDemotesNothing: a template outside
// the vocabulary, or one whose server-side placeholder this envelope does not
// satisfy, names neither a key nor a shape — so it floors nothing, and
// specifically it must not become a wildcard that softens whatever the envelope
// happened to declare.
//
// The resolvable sibling in the same descriptor is the positive vector: it
// proves the envelope's keys reach the floor at all, so the untouched keys
// below are the refusals' doing rather than a floor that never ran.
func TestDescriptorFloor_UncompilableTemplateDemotesNothing(t *testing.T) {
	reachable := "vtx.leaseapp." + floorLeaseMine
	unnamed := "vtx.instructor." + floorLeaseVictim
	base := declaredReads{Reads: []string{floorTarget, reachable, unnamed}}
	templates := floorFor(
		"{entity.unitKey}",       // the row the client is displaying, not a server value
		"{payload.notInPayload}", // no such payload field on this envelope
		"{scopedTo}",             // a target step 3 did not validate
		"bkr{me.instructor:id}",  // a client-only placeholder sharing a segment
		"{me.instructor?}",       // `?` is ContextParams vocabulary, not read-template
		"{me.}",                  // a self-anchor naming no type
		"{payload.targetIdentityKey}",
	)
	logger, log := capturingLogger()
	got := applyFloor(base, templates, floorEnv(floorPayload()), logger)

	if slices.Contains(got.Reads, floorTarget) {
		t.Fatalf("Reads = %v, want the resolvable template's key demoted — the positive vector must reach the floor", got.Reads)
	}
	for _, want := range []string{reachable, unnamed} {
		if !slices.Contains(got.Reads, want) {
			t.Fatalf("Reads = %v, want %q untouched — an uncompilable template names no key", got.Reads, want)
		}
	}
	for _, tpl := range []string{"{entity.unitKey}", "{payload.notInPayload}", "{scopedTo}", "bkr{me.instructor:id}", "{me.instructor?}", "{me.}"} {
		if !strings.Contains(log.String(), tpl) {
			t.Fatalf("log = %q, want a Warn naming %q — a control that did not apply must be as visible as one that fired", log.String(), tpl)
		}
	}
}

// TestCompileDescriptorTemplate walks the vocabulary one row at a time: what
// substitutes concretely, what compiles to a key SHAPE, and every way a
// template is refused. A refusal is always "no key", never a partial one: a
// hole leaves a DIFFERENT key, not a shorter one, and an over-wide wildcard
// leaves a floor over keys no descriptor named.
func TestCompileDescriptorTemplate(t *testing.T) {
	actor := "vtx.identity." + testNanoID2
	patient := "vtx.patient." + floorTabID
	env := floorEnv(`{"targetIdentityKey":"` + floorTarget + `","session":"vtx.session.` + tplID +
		`","patientKey":"` + patient + `","providerKey":"vtx.provider.` + instID + `"}`)
	env.Actor = actor
	env.AuthContext = &AuthContext{Service: "vtx.service." + testNanoID1, Target: floorTarget}
	payload := mustPayload(t, env)

	concrete := map[string]string{
		"vtx.identity." + testNanoID3:          "vtx.identity." + testNanoID3,
		"{actor}":                              actor,
		"{actor}.state":                        actor + ".state",
		"{payload.targetIdentityKey}":          floorTarget,
		"{payload.targetIdentityKey}.claimKey": floorTarget + ".claimKey",
		"{service}":                            "vtx.service." + testNanoID1,
		"lnk.task." + testNanoID1 + ".assignedTo.identity.{actor:id}": "lnk.task." + testNanoID1 + ".assignedTo.identity." + testNanoID2,
		// The mid-segment `:id` fragment idiom, live in two packages. With
		// server-resolvable placeholders it substitutes concretely and needs no
		// wildcard at all — the two live shapes the matcher must not disturb.
		"vtx.session.{payload.session:id}.bkr{actor:id}":                     "vtx.session." + tplID + ".bkr" + testNanoID2,
		"{payload.patientKey}.activeVisitSeriesWith{payload.providerKey:id}": patient + ".activeVisitSeriesWith" + instID,
	}
	for tpl, want := range concrete {
		pattern, ok := compileDescriptorTemplate(tpl, env, payload)
		if !ok {
			t.Fatalf("compile(%q) refused, want the concrete key %q", tpl, want)
		}
		key, isConcrete := pattern.concrete()
		if !isConcrete {
			t.Fatalf("compile(%q) = the pattern %q, want the concrete key %q — a template with no client-only vocabulary must take the exact-match path", tpl, patternString(pattern), want)
		}
		if key != want {
			t.Fatalf("compile(%q) = %q, want %q", tpl, key, want)
		}
	}

	// `{me.<type>}` is the client-only half: the Processor has no value for it,
	// so it compiles to a whole-segment NanoID wildcard. `*` below is that
	// wildcard, never a fragment of a segment.
	patterns := map[string]string{
		"{me.leaseapp:id}":    "*",
		"{me.leaseapp}":       "vtx.leaseapp.*",
		"{me.leaseapp}.terms": "vtx.leaseapp.*.terms",
		"lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}": "lnk.leaseapp.*.applicationFor.identity." + testNanoID2,
		"lnk.tab.{payload.session:id}.chargedTo.leaseapp.{me.leaseapp:id}": "lnk.tab." + tplID + ".chargedTo.leaseapp.*",
	}
	for tpl, want := range patterns {
		pattern, ok := compileDescriptorTemplate(tpl, env, payload)
		if !ok {
			t.Fatalf("compile(%q) refused, want the pattern %q", tpl, want)
		}
		if _, isConcrete := pattern.concrete(); isConcrete {
			t.Fatalf("compile(%q) resolved concretely; the Processor has no value for a client-only placeholder", tpl)
		}
		if got := patternString(pattern); got != want {
			t.Fatalf("compile(%q) = %q, want %q", tpl, got, want)
		}
	}

	refused := []string{
		"{entity.unitKey}",           // the row being displayed, not a server value
		"{ancestor.orgKey}",          // outside the vocabulary entirely
		"{payload.missing}",          // no such payload field
		"{payload.targetIdentityKey", // unterminated
		"{actor}..state",             // a hole is a different key, not a shorter one
		"",                           // nothing at all
		"bkr{me.instructor:id}",      // a client-only placeholder sharing a segment
		"{me.instructor:id}bkr",      // the same, on the other side
		"{me.a}{me.b}",               // two shapes with no segment boundary between them
		"{me.}",                      // a self-anchor naming no type
		"{me.a.b:id}",                // a type name is one segment
		// The `?` optional marker is live ContextParams vocabulary and is not
		// part of the read-template vocabulary. It must be refused audibly
		// rather than swallowed into a literal segment, where it would compile
		// to a pattern that matches nothing and warns about nothing —
		// §5.5's residual argument rests on the Warn being the backstop.
		"{me.instructor?}",
		"{me.leaseapp:id?}",
		"{actor?}",
		"{payload.targetIdentityKey?}",
		"{me.Leaseapp}",  // a type token is [a-z][a-z0-9]*
		"{me.lease_app}", // ditto
	}
	for _, tpl := range refused {
		if pattern, ok := compileDescriptorTemplate(tpl, env, payload); ok {
			t.Fatalf("compile(%q) = %q, want refused", tpl, patternString(pattern))
		}
	}

	// A `:id` modifier over a value that is not a vertex key is refused rather
	// than truncated — a bare id taken off a malformed key addresses something
	// else.
	badIDEnv := floorEnv(`{"targetIdentityKey":"notakey"}`)
	if pattern, ok := compileDescriptorTemplate("lnk.task.x.assignedTo.identity.{payload.targetIdentityKey:id}", badIDEnv, mustPayload(t, badIDEnv)); ok {
		t.Fatalf("compile over a malformed :id source = %q, want refused", patternString(pattern))
	}
}

// TestCompileDescriptorTemplate_ScopedToNeedsAValidatedTarget: `authContext.
// target` is client-supplied except where step 3 proved it, so resolving an
// unvalidated one would let a lying submitter steer the floor away from the key
// they are probing. It never patterns either: the target's TYPE is unknown, and
// `vtx.<any>.<nanoid>` would demote every declared vertex root.
//
// The validated arm is the positive vector — without it, "refused" would be
// satisfied by a `{scopedTo}` nothing ever resolves.
func TestCompileDescriptorTemplate_ScopedToNeedsAValidatedTarget(t *testing.T) {
	env := floorEnv(floorPayload())
	env.AuthContext = &AuthContext{Target: floorTarget}

	env.AuthTargetValidated = false
	for _, tpl := range []string{"{scopedTo}", "{scopedTo:id}", "{scopedTo}.state"} {
		if pattern, ok := compileDescriptorTemplate(tpl, env, mustPayload(t, env)); ok {
			t.Fatalf("compile(%q) = %q over an unvalidated target, want refused", tpl, patternString(pattern))
		}
	}

	env.AuthTargetValidated = true
	want := map[string]string{
		"{scopedTo}":       floorTarget,
		"{scopedTo}.state": floorTarget + ".state",
		"lnk.task." + testNanoID1 + ".scopedTo.identity.{scopedTo:id}": "lnk.task." + testNanoID1 + ".scopedTo.identity." + testNanoID3,
	}
	for tpl, key := range want {
		pattern, ok := compileDescriptorTemplate(tpl, env, mustPayload(t, env))
		if !ok {
			t.Fatalf("compile(%q) refused over a target step 3 validated, want %q", tpl, key)
		}
		got, isConcrete := pattern.concrete()
		if !isConcrete || got != key {
			t.Fatalf("compile(%q) = (%q, concrete=%v), want (%q, true)", tpl, patternString(pattern), isConcrete, key)
		}
	}

	// A validated flag with no target still names nothing.
	env.AuthContext = nil
	if _, ok := compileDescriptorTemplate("{scopedTo}", env, mustPayload(t, env)); ok {
		t.Fatalf("compile({scopedTo}) resolved with no authContext at all")
	}
}

// TestDescriptorFloor_PatternDemotesEveryMatch pins the matcher's three
// answers at the demotion site: every same-shaped key the envelope declares is
// demoted (not the first), a same-shaped key whose wildcard segment is not a
// NanoID is not, and neither is a key of a different segment count.
//
// "Every match" is the load-bearing half. A submitter probing fifty ids in one
// envelope is exactly the enumeration the floor exists to close, and a matcher
// that stopped at the first hit would leave forty-nine oracles open.
func TestDescriptorFloor_PatternDemotesEveryMatch(t *testing.T) {
	mine := "vtx.leaseapp." + floorLeaseMine
	victim := "vtx.leaseapp." + floorLeaseVictim
	notAnID := "vtx.leaseapp.not-a-nanoid"
	deeper := "vtx.leaseapp." + floorLeaseMine + ".terms"
	otherType := "vtx.tab." + floorTabID
	base := declaredReads{Reads: []string{mine, victim, notAnID, deeper, otherType}}

	got := applyFloor(base, floorFor("{me.leaseapp}"), floorEnv(floorPayload()), testLogger())

	for _, want := range []string{mine, victim} {
		if slices.Contains(got.Reads, want) {
			t.Fatalf("Reads = %v, want %q demoted — a pattern demotes EVERY match, not the first", got.Reads, want)
		}
		if !slices.Contains(got.OptionalReads, want) {
			t.Fatalf("OptionalReads = %v, want %q", got.OptionalReads, want)
		}
	}
	for _, keep := range []string{notAnID, deeper, otherType} {
		if !slices.Contains(got.Reads, keep) {
			t.Fatalf("Reads = %v, want %q still fail-closed — a wildcard segment matches a NanoID and nothing else, and a pattern never matches across segment counts", got.Reads, keep)
		}
	}
}

// TestDescriptorFloor_RequiredTemplateWinsOverOptional: where a descriptor's
// two lists name one key, the required reading stands. Wrong-demotion is the
// dangerous direction — it turns the HydrationMiss the script's author expected
// into a silent None.
//
// The required templates here are the only kind that can carry an exclusion:
// `{actor}` is the step-3-authenticated identity and the literal is the
// descriptor's own text, so the excluded key is a function of the descriptor
// and the caller — nothing a request body can address.
//
// The peer key demoted by the same pattern in the same call is the positive
// vector: it proves the optional template really does cover this shape, so the
// surviving keys are the exclusion's doing.
func TestDescriptorFloor_RequiredTemplateWinsOverOptional(t *testing.T) {
	env := floorEnv(floorPayload())
	self := "vtx.identity." + testNanoID2 // env.Actor
	literal := "vtx.identity." + testNanoID1
	other := "vtx.identity." + floorLeaseVictim
	base := declaredReads{
		Reads:       []string{self, literal, other},
		EgressReads: []string{self + ".ssn"},
	}
	templates := DispatchTemplates{
		Reads:         []string{"{actor}", "{actor}.ssn", literal},
		OptionalReads: []string{"{me.identity}", "{me.identity}.ssn"},
	}

	got := applyFloor(base, templates, env, testLogger())

	for _, kept := range []string{self, literal} {
		if !slices.Contains(got.Reads, kept) {
			t.Fatalf("Reads = %v, want %q kept — the descriptor's own required template excludes it from demotion", got.Reads, kept)
		}
	}
	if slices.Contains(got.Reads, other) {
		t.Fatalf("Reads = %v, want %q demoted — without it the exclusions above prove nothing", got.Reads, other)
	}
	if _, tolerant := got.EgressAbsenceTolerant[self+".ssn"]; tolerant {
		t.Fatalf("EgressAbsenceTolerant = %v, want the required egress key untouched — required-wins binds both arms", got.EgressAbsenceTolerant)
	}
}

// TestDescriptorFloor_PayloadSteeredRequiredTemplateCannotSuppressTheFloor is
// the control's integrity property: the exclusion set is a function of the
// descriptor and the authenticated identity, never of the request body.
//
// The descriptor is cafe-domain Charge's, verbatim: its required templates are
// all `{payload.<field>}`-rooted, and its floor is the lease-ownership link the
// caller must not be able to probe. If a payload-derived required template
// contributed an exclusion, the submitter would only have to spell their probe
// key into `menuItemKey` — a field the op reads for something else entirely —
// to lift the floor off it for that request and get HydrationMiss +
// details.missingKey back. The honest payload in the same shape is the positive
// vector: it proves the floor covers this key, so the hostile arm is testing
// the steering and not a floor that never applied.
func TestDescriptorFloor_PayloadSteeredRequiredTemplateCannotSuppressTheFloor(t *testing.T) {
	templates := DispatchTemplates{
		Reads: []string{
			"{payload.tabKey}", "{payload.tabKey}.status",
			"{payload.menuItemKey}", "{payload.menuItemKey}.price",
		},
		OptionalReads: []string{"lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}"},
	}
	probe := "lnk.leaseapp." + floorLeaseVictim + ".applicationFor.identity." + testNanoID2

	honest := floorEnv(`{"tabKey":"vtx.tab.` + floorTabID + `","menuItemKey":"vtx.menuitem.` + floorLeaseMine + `"}`)
	control := applyFloor(declaredReads{Reads: []string{probe}}, templates, honest, testLogger())
	if slices.Contains(control.Reads, probe) {
		t.Fatalf("Reads = %v, want %q demoted under an honest payload — the floor must cover this key before the steering means anything", control.Reads, probe)
	}

	// The same descriptor, the same probe, one payload field aimed at it.
	hostile := floorEnv(`{"tabKey":"vtx.tab.` + floorTabID + `","menuItemKey":"` + probe + `"}`)
	logger, log := capturingLogger()
	got := applyFloor(
		declaredReads{Reads: []string{probe}, EgressReads: []string{probe + ".terms"}},
		DispatchTemplates{
			Reads: templates.Reads,
			OptionalReads: append(slices.Clone(templates.OptionalReads),
				"lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}.terms"),
		},
		hostile, logger)

	if slices.Contains(got.Reads, probe) {
		t.Fatalf("Reads = %v, want %q still demoted — a payload field must not be able to buy an exclusion from the floor", got.Reads, probe)
	}
	if !slices.Contains(got.OptionalReads, probe) {
		t.Fatalf("OptionalReads = %v, want %q", got.OptionalReads, probe)
	}
	if _, tolerant := got.EgressAbsenceTolerant[probe+".terms"]; !tolerant {
		t.Fatalf("EgressAbsenceTolerant = %v, want the egress probe marked — the steering reaches this arm the same way", got.EgressAbsenceTolerant)
	}
	if !strings.Contains(log.String(), "{payload.menuItemKey}") || !strings.Contains(log.String(), "excludes no key") {
		t.Fatalf("log = %q, want a Warn naming the payload-derived required template it refused to honour", log.String())
	}
}

// TestDescriptorFloor_RequiredPatternExcludesNothing: a required template
// carrying client-only vocabulary compiles to a SHAPE, and a shape excluded
// from demotion blankets every key of that shape — one `{me.<type>}` on the
// required side would preserve the oracle for every declared root of the type.
// So it contributes no exclusion at all and says so at Warn: the same posture
// an uncompilable optional template gets, failing toward the floor still
// applying rather than toward the oracle.
func TestDescriptorFloor_RequiredPatternExcludesNothing(t *testing.T) {
	self := "vtx.identity." + testNanoID2 // env.Actor
	base := declaredReads{Reads: []string{self}}
	templates := DispatchTemplates{
		Reads:         []string{"{me.identity}"},
		OptionalReads: []string{"{me.identity}"},
	}
	logger, log := capturingLogger()

	got := applyFloor(base, templates, floorEnv(floorPayload()), logger)

	if slices.Contains(got.Reads, self) {
		t.Fatalf("Reads = %v, want %q demoted — a required SHAPE excludes nothing", got.Reads, self)
	}
	if !slices.Contains(got.OptionalReads, self) {
		t.Fatalf("OptionalReads = %v, want %q", got.OptionalReads, self)
	}
	if !strings.Contains(log.String(), "{me.identity}") || !strings.Contains(log.String(), "excludes no key") {
		t.Fatalf("log = %q, want a Warn naming the required template it could not honour", log.String())
	}

	// The same call with a required template that resolves to ONE key from the
	// authenticated identity excludes it, which is what shows the Warn above is
	// about the shape and not about the required list being ignored wholesale.
	concrete := DispatchTemplates{Reads: []string{"{actor}"}, OptionalReads: []string{"{me.identity}"}}
	if kept := applyFloor(declaredReads{Reads: []string{self}}, concrete, floorEnv(floorPayload()), testLogger()); !slices.Contains(kept.Reads, self) {
		t.Fatalf("Reads = %v, want %q kept by the actor-rooted required template", kept.Reads, self)
	}
}

// TestDescriptorFloor_EgressPatternIsMarkedInPlace: a pattern decides WHICH
// egress keys are floored and changes nothing about what happens to one. The
// key stays in EgressReads — presence still authors the `$sensitiveRef` the
// bridge opens — and only its absence becomes tolerant.
func TestDescriptorFloor_EgressPatternIsMarkedInPlace(t *testing.T) {
	matched := "vtx.leaseapp." + floorLeaseVictim + ".ssn"
	unmatched := "vtx.tab." + floorTabID + ".ssn"
	base := declaredReads{EgressReads: []string{matched, unmatched}}

	got := applyFloor(base, floorFor("vtx.leaseapp.{me.leaseapp:id}.ssn"), floorEnv(floorPayload()), testLogger())

	if !slices.Equal(got.EgressReads, []string{matched, unmatched}) {
		t.Fatalf("EgressReads = %v, want both keys still declared egress — moving one swaps a bridge-opened ref for plaintext", got.EgressReads)
	}
	if slices.Contains(got.Reads, matched) || slices.Contains(got.OptionalReads, matched) {
		t.Fatalf("the pattern-matched egress key was pulled into a read list: %+v", got)
	}
	if _, tolerant := got.EgressAbsenceTolerant[matched]; !tolerant {
		t.Fatalf("EgressAbsenceTolerant = %v, want %q — its absence otherwise faults HydrationMiss and echoes the key back", got.EgressAbsenceTolerant, matched)
	}
	if _, tolerant := got.EgressAbsenceTolerant[unmatched]; tolerant {
		t.Fatalf("EgressAbsenceTolerant = %v, want %q untouched — it is a different key shape", got.EgressAbsenceTolerant, unmatched)
	}
}

// TestDescriptorFloor_DecoupledProbeIsDemoted is the vector that keeps the
// negative claim honest, over cafe-domain's real template strings.
//
// A client-faithful resolution of `{me.leaseapp}` would name the CALLER's own
// lease. It is the wrong key to floor: the script recovers the lease from the
// tab's own `.status`, never from anything the caller supplied, so a submitter
// who names someone else's tab makes the script touch the VICTIM's lease link —
// a key the caller's own projected view would never produce. The pattern covers
// the honest key and the decoupled one alike, which is why shape-matching is
// the correct semantics here rather than an approximation of resolution.
func TestDescriptorFloor_DecoupledProbeIsDemoted(t *testing.T) {
	env := floorEnv(`{"tabKey":"vtx.tab.` + floorTabID + `"}`)
	templates := floorFor(
		"lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}",
		"lnk.tab.{payload.tabKey:id}.chargedTo.leaseapp.{me.leaseapp:id}",
	)

	victimOwnership := "lnk.leaseapp." + floorLeaseVictim + ".applicationFor.identity." + testNanoID2
	victimCharge := "lnk.tab." + floorTabID + ".chargedTo.leaseapp." + floorLeaseVictim
	// Anchored on someone else's actor: the pattern fixes that segment
	// concretely from the step-3-authenticated actor, so it is out of reach.
	someoneElsesProbe := "lnk.leaseapp." + floorLeaseVictim + ".applicationFor.identity." + testNanoID3
	base := declaredReads{Reads: []string{victimOwnership, victimCharge, someoneElsesProbe}}

	got := applyFloor(base, templates, env, testLogger())

	for _, demoted := range []string{victimOwnership, victimCharge} {
		if slices.Contains(got.Reads, demoted) {
			t.Fatalf("Reads = %v, want %q demoted — a probe the caller's own view would never name is exactly the key the script touches", got.Reads, demoted)
		}
		if !slices.Contains(got.OptionalReads, demoted) {
			t.Fatalf("OptionalReads = %v, want %q", got.OptionalReads, demoted)
		}
	}
	if !slices.Contains(got.Reads, someoneElsesProbe) {
		t.Fatalf("Reads = %v, want %q untouched — every server-resolvable placeholder stays CONCRETE inside a pattern", got.Reads, someoneElsesProbe)
	}
}

// patternString renders a compiled template for a failure message: literal
// segments as themselves, wildcard segments as `*`.
func patternString(p keyPattern) string {
	out := make([]string, len(p.segments))
	for i, seg := range p.segments {
		if seg.wildcard {
			out[i] = "*"
			continue
		}
		out[i] = seg.literal
	}
	return strings.Join(out, ".")
}

func mustPayload(t *testing.T, env *OperationEnvelope) map[string]interface{} {
	t.Helper()
	m, _ := jsonToGenericMap(env.Payload)
	return m
}

// TestDescriptorFloorResolver_ResolvesOnceAndOnlyWhenAsked pins the two
// properties the resolver's own doc claims, both of which are observable ONLY
// through the Warn an uncompilable template emits.
//
// Deferral: the Warn asserts that the floor did not apply TO A KEY. An envelope
// that declares nothing and a derivation that returns nothing give it no key to
// be about, so compiling on construction would put that claim in the log of
// every operation carrying a descriptor — the claim would still be there, and
// still be false, when the operation had nothing to floor.
//
// Memoization: the Warn belongs to the DESCRIPTOR, not to whichever arm asked
// first. Two arms consulting one resolver must not double the operator's log
// for one template, and must not make the count depend on which arms happened
// to have keys.
func TestDescriptorFloorResolver_ResolvesOnceAndOnlyWhenAsked(t *testing.T) {
	env := floorEnv(floorPayload())
	// One template that cannot compile against this envelope (the field is
	// absent from the payload) beside one that can, so the floor is non-empty
	// and the Warn has a subject.
	templates := DispatchTemplates{OptionalReads: []string{"{payload.absentField}", floorTarget}}
	const uncompilable = "{payload.absentField}"

	t.Run("neither arm has a key, so nothing resolves", func(t *testing.T) {
		logger, log := capturingLogger()
		r := newDescriptorFloorResolver(templates, env, logger)
		_ = applyDescriptorFloor(declaredReads{}, r)
		if _, err := mergeDerivedReads(declaredReads{}, derivedReads{}, r, testNanoID1); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if log.Len() != 0 {
			t.Fatalf("log = %q, want silence — the templates were never asked about a key", log.String())
		}
	})

	t.Run("both arms ask, the descriptor is warned about once", func(t *testing.T) {
		logger, log := capturingLogger()
		r := newDescriptorFloorResolver(templates, env, logger)
		_ = applyDescriptorFloor(declaredReads{Reads: []string{floorTarget}}, r)
		// A derived REQUIRED key reaches floored(); it is not covered by this
		// floor, so it is admitted and the only trace is whether the resolver
		// compiled the templates a second time.
		if _, err := mergeDerivedReads(declaredReads{}, derivedReads{Reads: []string{deriveTestKeyB}}, r, testNanoID1); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if n := strings.Count(log.String(), uncompilable); n != 1 {
			t.Fatalf("the template was warned about %d times, want exactly 1: %q", n, log.String())
		}
	})
}

// ---- the closed declared-read set (NFR-S6) ----

// The two NFR-S6 descriptors, as packages/identity-domain ships them. They are
// literals here because that package imports this one; the correspondence
// between these templates, the package's own descriptor and the four shipped
// dispatchers is pinned where it can be read from both sides —
// identity-domain's package_test and internal/identityceremony's builder test.
var (
	claimDescriptor = DispatchTemplates{OptionalReads: []string{
		"{payload.targetIdentityKey}",
		"{payload.targetIdentityKey}.state",
		"{payload.targetIdentityKey}.claimKey",
	}}
	linkDescriptor = DispatchTemplates{OptionalReads: []string{
		"{payload.targetIdentityKey}",
		"{payload.targetIdentityKey}.state",
		"{payload.targetIdentityKey}.linkKey",
		"{payload.targetIdentityKey}.credentialBinding",
	}}
)

// shippedClaimHint / shippedLinkHint mirror identityceremony's builders — the
// declaration every live dispatcher sends. Written out rather than derived from
// the templates above, so an envelope and a descriptor cannot drift together.
func shippedClaimHint(target string) *ContextHint {
	return &ContextHint{OptionalReads: []string{target, target + ".state", target + ".claimKey"}}
}

func shippedLinkHint(target string) *ContextHint {
	return &ContextHint{OptionalReads: []string{
		target, target + ".state", target + ".linkKey", target + ".credentialBinding",
	}}
}

func nfrS6Env(operationType, target string) *OperationEnvelope {
	env := floorEnv(`{"targetIdentityKey":"` + target + `"}`)
	env.OperationType = operationType
	return env
}

// TestRefuseUndeclaredContextHint walks the closed set one declaration at a
// time. The admitted rows come FIRST and are the ones that matter: this gate's
// negatives are worthless unless the envelope every shipped dispatcher actually
// sends passes through it untouched.
func TestRefuseUndeclaredContextHint(t *testing.T) {
	// A key no descriptor names: a well-formed identity key the submitter
	// chose, which is exactly what a probe looks like.
	probe := "vtx.identity." + floorLeaseVictim

	withOptional := func(h *ContextHint, extra ...string) *ContextHint {
		out := *h
		out.OptionalReads = append(append([]string{}, h.OptionalReads...), extra...)
		return &out
	}

	cases := []struct {
		name         string
		op           string
		templates    DispatchTemplates
		noDescriptor bool
		// target overrides the payload's targetIdentityKey, for the rows whose
		// subject is what a template COMPILES to rather than what is declared.
		// Empty means floorTarget.
		target      string
		hint        *ContextHint
		wantRefused bool
	}{
		{
			name: "the shipped claim envelope is admitted",
			op:   "ClaimIdentity", templates: claimDescriptor, hint: shippedClaimHint(floorTarget),
		},
		{
			name: "the shipped credential-link envelope is admitted",
			op:   "CompleteCredentialLink", templates: linkDescriptor, hint: shippedLinkHint(floorTarget),
		},
		{
			name: "a descriptor `reads` template admits its key too",
			op:   "ClaimIdentity",
			templates: DispatchTemplates{
				Reads:         []string{"{payload.targetIdentityKey}.claimKey"},
				OptionalReads: []string{"{payload.targetIdentityKey}"},
			},
			hint: &ContextHint{
				Reads:         []string{floorTarget + ".claimKey"},
				OptionalReads: []string{floorTarget},
			},
		},
		{
			name: "repetition of an admitted key is not the property this closes",
			op:   "ClaimIdentity", templates: claimDescriptor,
			hint: withOptional(shippedClaimHint(floorTarget), floorTarget, floorTarget),
		},
		{
			name: "an envelope declaring nothing at all",
			op:   "ClaimIdentity", templates: claimDescriptor, hint: nil,
		},
		{
			name: "one extra optionalReads key",
			op:   "ClaimIdentity", templates: claimDescriptor,
			hint:        withOptional(shippedClaimHint(floorTarget), probe),
			wantRefused: true,
		},
		{
			name: "one extra reads key",
			op:   "CompleteCredentialLink", templates: linkDescriptor,
			hint: &ContextHint{
				Reads:         []string{probe},
				OptionalReads: shippedLinkHint(floorTarget).OptionalReads,
			},
			wantRefused: true,
		},
		{
			name: "an egressReads key, which no descriptor can name",
			op:   "ClaimIdentity", templates: claimDescriptor,
			hint: &ContextHint{
				OptionalReads: shippedClaimHint(floorTarget).OptionalReads,
				EgressReads:   []string{floorTarget + ".ssn"},
			},
			wantRefused: true,
		},
		{
			name: "an enumeration, which no descriptor can name either",
			op:   "ClaimIdentity", templates: claimDescriptor,
			hint: &ContextHint{
				OptionalReads: shippedClaimHint(floorTarget).OptionalReads,
				Enumerations:  []EnumerationHint{{Hub: floorTarget, Relation: "holdsRole", Direction: "out"}},
			},
			wantRefused: true,
		},
		{
			name: "a template compiling to a key SHAPE admits nothing",
			op:   "ClaimIdentity", templates: DispatchTemplates{OptionalReads: []string{"{me.identity}"}},
			hint:        &ContextHint{OptionalReads: []string{floorTarget}},
			wantRefused: true,
		},
		{
			name: "a template that does not compile admits nothing",
			op:   "ClaimIdentity", templates: DispatchTemplates{OptionalReads: []string{"{payload.absentField}"}},
			hint:        shippedClaimHint(floorTarget),
			wantRefused: true,
		},
		{
			name: "no descriptor at all admits nothing",
			op:   "ClaimIdentity", noDescriptor: true, hint: shippedClaimHint(floorTarget),
			wantRefused: true,
		},
		{
			// loadOpMetaDispatch reports an op-meta whose `.dispatch` is absent
			// or tombstoned as found-with-empty-lists, so this is the shape a
			// descriptor withdrawn by an upgrade leaves behind: a descriptor
			// that exists and names nothing.
			name: "a descriptor naming no read template at all admits nothing",
			op:   "ClaimIdentity", templates: DispatchTemplates{},
			hint:        shippedClaimHint(floorTarget),
			wantRefused: true,
		},
		{
			// floorsByOpType UNIONS two claimants of one operationType, which
			// widens a floor safely and widens an admitted set dangerously — a
			// second claimant's thousand padding templates would be admitted as
			// declarations. The closure refuses the whole union instead.
			name: "an operationType two op-metas claim admits nothing, union or not",
			op:   "ClaimIdentity",
			templates: DispatchTemplates{
				OptionalReads: claimDescriptor.OptionalReads, Claimants: 2,
			},
			hint:        shippedClaimHint(floorTarget),
			wantRefused: true,
		},
		{
			// The grammar rule. Three literal segments compile from a payload
			// value of `vtx.identity.*`, so the segment matcher calls it
			// concrete — and admitting it would let the declaration name a NATS
			// subject filter rather than a key.
			name: "a template compiling to a wildcard is not a key, so it admits nothing",
			op:   "ClaimIdentity", templates: claimDescriptor,
			target:      "vtx.identity.*",
			hint:        &ContextHint{OptionalReads: []string{"vtx.identity.*"}},
			wantRefused: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.target
			if target == "" {
				target = floorTarget
			}
			env := nfrS6Env(tc.op, target)
			env.ContextHint = tc.hint
			var resolver *descriptorFloorResolver
			if !tc.noDescriptor {
				resolver = newDescriptorFloorResolver(tc.templates, env, testLogger())
			}
			err := refuseUndeclaredContextHint(env, resolver, testLogger())
			if !tc.wantRefused {
				if err != nil {
					t.Fatalf("refused %+v: %v — the declaration is the descriptor's own set, so this operation is now unsubmittable", tc.hint, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("admitted %+v; the declared set for an NFR-S6 operation is closed", tc.hint)
			}
			var hErr *HydrationError
			if !errors.As(err, &hErr) {
				t.Fatalf("err = %T (%v), want a *HydrationError — anything else classifies as InternalError and answers with a different wire code", err, err)
			}
			if hErr.Code != "UndeclaredContextHintKey" {
				t.Fatalf("code = %q, want UndeclaredContextHintKey", hErr.Code)
			}
			// The refused key is the submitter's own probe. details.missingKey
			// is what the reply carries, so a key here IS the oracle.
			if hErr.MissingKey != "" {
				t.Fatalf("MissingKey = %q, want empty — the reply carries it straight back to the prober", hErr.MissingKey)
			}
			if strings.Contains(err.Error(), probe) || strings.Contains(err.Error(), floorTarget) {
				t.Fatalf("the fault text quotes a declared key: %q", err.Error())
			}
		})
	}
}

// TestRefuseUndeclaredContextHint_RefusalNamesTheKeyInTheLogOnly: the operator
// gets exactly one copy of what was refused, and it is the log's. Same posture
// as warnContradiction — the fault the caller sees is deliberately mute, so a
// silent refusal would leave nothing to diagnose an over-deny with.
func TestRefuseUndeclaredContextHint_RefusalNamesTheKeyInTheLogOnly(t *testing.T) {
	probe := "vtx.identity." + floorLeaseVictim
	env := nfrS6Env("ClaimIdentity", floorTarget)
	env.ContextHint = &ContextHint{OptionalReads: []string{probe}}

	logger, log := capturingLogger()
	err := refuseUndeclaredContextHint(env, newDescriptorFloorResolver(claimDescriptor, env, logger), logger)
	if err == nil {
		t.Fatal("admitted a key the descriptor does not name")
	}
	if !strings.Contains(log.String(), probe) {
		t.Fatalf("log = %q, want the refused key — the fault carries none, so an over-deny is otherwise undiagnosable", log.String())
	}
	if !strings.Contains(log.String(), env.RequestID) {
		t.Fatalf("log = %q, want the requestId that ties the refusal to a submission", log.String())
	}
	if !strings.Contains(log.String(), "admitted=3") {
		t.Fatalf("log = %q, want the admitted-set size — without it an over-deny reads exactly like an over-declaring submitter", log.String())
	}
}

// TestRefuseUndeclaredContextHint_OverDenyIsAttributable: the three ways this
// rule refuses a submission the shipped dispatcher actually sends are the ways
// an operator has to be able to tell apart, because the fault carries no key
// and the refusal is collapsed into the same generic reply as every other
// cause. Each states its own condition, and none of them is a per-template Warn
// — the descriptor never reaches template compilation in any of the three.
func TestRefuseUndeclaredContextHint_OverDenyIsAttributable(t *testing.T) {
	shipped := shippedClaimHint(floorTarget)

	cases := []struct {
		name      string
		templates DispatchTemplates
		// noDescriptor drops the resolver entirely, the case an op-meta the
		// cache never saw produces.
		noDescriptor bool
		want         string
	}{
		{
			name:      "a descriptor naming no read template says so",
			templates: DispatchTemplates{},
			want:      "names NO read template",
		},
		{
			name:      "a multi-claimant operationType says so",
			templates: DispatchTemplates{OptionalReads: claimDescriptor.OptionalReads, Claimants: 2},
			want:      "claims this operationType",
		},
		{
			name: "an over-declaring submitter is not confused with either",
			// The control: a descriptor that admits its three keys, refusing
			// one the submitter added. Nothing about the descriptor is at
			// fault, so neither whole-descriptor line may appear.
			templates: claimDescriptor,
			want:      "admitted=3",
		},
		{
			name: "no descriptor at all reaches the operator as an empty admitted set",
			// A nil resolver has no Warn of its own to give — admittedCount is
			// what carries the condition.
			noDescriptor: true,
			want:         "admitted=0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := nfrS6Env("ClaimIdentity", floorTarget)
			env.ContextHint = &ContextHint{
				OptionalReads: append(append([]string{}, shipped.OptionalReads...), "vtx.identity."+floorLeaseVictim),
			}
			logger, log := capturingLogger()
			var resolver *descriptorFloorResolver
			if !tc.noDescriptor {
				resolver = newDescriptorFloorResolver(tc.templates, env, logger)
			}
			if err := refuseUndeclaredContextHint(env, resolver, logger); err == nil {
				t.Fatal("admitted a declaration the descriptor does not name")
			}
			if !strings.Contains(log.String(), tc.want) {
				t.Fatalf("log = %q, want it to carry %q", log.String(), tc.want)
			}
			if tc.want == "admitted=3" {
				for _, unwanted := range []string{"names NO read template", "claims this operationType"} {
					if strings.Contains(log.String(), unwanted) {
						t.Fatalf("log = %q, must not blame the descriptor for a submitter's extra key", log.String())
					}
				}
			}
		})
	}
}

// TestDescriptorFloorResolver_MultiClaimantStillFloors: the union is refused as
// an ADMISSION and kept as a FLOOR, and the asymmetry is the point. Demotion is
// the weaker disposition, so inheriting a second claimant's optional template
// costs nothing an attacker wants; admission is the stronger one, and a second
// claimant's templates would enlarge the set the closure exists to pin.
func TestDescriptorFloorResolver_MultiClaimantStillFloors(t *testing.T) {
	env := nfrS6Env("ClaimIdentity", floorTarget)
	templates := DispatchTemplates{OptionalReads: claimDescriptor.OptionalReads, Claimants: 2}
	r := newDescriptorFloorResolver(templates, env, testLogger())

	got := applyDescriptorFloor(declaredReads{Reads: []string{floorTarget}}, r)
	if len(got.Reads) != 0 || !slices.Contains(got.OptionalReads, floorTarget) {
		t.Fatalf("reads=%v optionalReads=%v, want the key demoted — a union is still a floor", got.Reads, got.OptionalReads)
	}
	if r.admits(floorTarget) {
		t.Fatal("a union admitted a key into the closed declared-read set")
	}
}

// TestHydrate_NFRS6ClosedDeclaredSet is the same rule at the seam that applies
// it, with the real descriptors read out of the real DDL cache.
//
// The two properties the unit table cannot reach are here: the check runs
// against the SUBMITTER's declaration at the head of step 4 (an admitted
// envelope still hydrates, absence-tolerantly), and it is scoped to the NFR-S6
// operations alone — an operation outside the set carrying the very same
// descriptor is demoted and hydrated, never refused.
func TestHydrate_NFRS6ClosedDeclaredSet(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	claimRoot := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(claimRoot, templatesJSON(claimDescriptor.OptionalReads), false), false)
	linkRoot := "vtx.meta." + instID
	seedOpMeta(t, ctx, conn, instID, "CompleteCredentialLink",
		dispatchAspect(linkRoot, templatesJSON(linkDescriptor.OptionalReads), false), false)
	// The control: the SAME descriptor on an operation outside the set.
	otherRoot := "vtx.meta." + svcTypeID
	seedOpMeta(t, ctx, conn, svcTypeID, "CreateIdentity",
		dispatchAspect(otherRoot, templatesJSON(claimDescriptor.OptionalReads), false), false)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h := NewHydratorWithCache(conn, testCoreBucket, cache, testLogger())

	target := "vtx.identity." + testNanoIDAbsent
	probe := "vtx.identity." + testNanoIDAbsent2
	envFor := func(op string, hint *ContextHint) *OperationEnvelope {
		env := newTestEnvelope(testNanoID1)
		env.OperationType = op
		env.Payload = []byte(`{"targetIdentityKey":"` + target + `"}`)
		env.ContextHint = hint
		return env
	}

	admitted := map[string]*ContextHint{
		"ClaimIdentity":          shippedClaimHint(target),
		"CompleteCredentialLink": shippedLinkHint(target),
	}
	for op, hint := range admitted {
		t.Run("the shipped "+op+" envelope hydrates", func(t *testing.T) {
			state, err := h.Hydrate(ctx, envFor(op, hint))
			if err != nil {
				t.Fatalf("Hydrate: %v — the shipped dispatcher's own declaration must reach hydration", err)
			}
			for _, key := range hint.OptionalReads {
				if _, ok := state.Context.KnownAbsent[key]; !ok {
					t.Fatalf("KnownAbsent = %v, want %q — the declared key never reached the snapshot",
						state.Context.KnownAbsent, key)
				}
			}
			if len(state.Context.RequiredAbsent) != 0 {
				t.Fatalf("RequiredAbsent = %v, want none — every key the ceremony declares is absence-tolerant",
					state.Context.RequiredAbsent)
			}
		})
	}

	refused := []struct {
		name string
		op   string
		hint *ContextHint
	}{
		{"an extra optionalReads key on the claim", "ClaimIdentity",
			&ContextHint{OptionalReads: append(append([]string{}, admitted["ClaimIdentity"].OptionalReads...), probe)}},
		{"an extra reads key on the claim", "ClaimIdentity",
			&ContextHint{Reads: []string{probe}, OptionalReads: admitted["ClaimIdentity"].OptionalReads}},
		{"an extra optionalReads key on the credential link", "CompleteCredentialLink",
			&ContextHint{OptionalReads: append(append([]string{}, admitted["CompleteCredentialLink"].OptionalReads...), probe)}},
		{"an egressReads key", "ClaimIdentity",
			&ContextHint{OptionalReads: admitted["ClaimIdentity"].OptionalReads, EgressReads: []string{target + ".ssn"}}},
		{"an enumeration", "ClaimIdentity",
			&ContextHint{OptionalReads: admitted["ClaimIdentity"].OptionalReads,
				Enumerations: []EnumerationHint{{Hub: target, Relation: "holdsRole", Direction: "out"}}}},
	}
	for _, tc := range refused {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			_, err := h.Hydrate(ctx, envFor(tc.op, tc.hint))
			var hErr *HydrationError
			if !errors.As(err, &hErr) || hErr.Code != "UndeclaredContextHintKey" {
				t.Fatalf("Hydrate = %v, want the closed-set refusal — an admitted extra key is hydrated INSIDE the rejection quantum", err)
			}
			if hErr.MissingKey != "" {
				t.Fatalf("MissingKey = %q, want empty", hErr.MissingKey)
			}
		})
	}

	t.Run("an operation outside the NFR-S6 set is unaffected", func(t *testing.T) {
		// The same out-of-set key, the same descriptor, the fail-closed
		// disposition: demoted where the descriptor covers it and hydrated
		// either way. Closing every operation's declared set
		// would break every submitter that declares a key its descriptor does
		// not name, which is most of the corpus.
		hint := &ContextHint{Reads: []string{target, probe}}
		state, err := h.Hydrate(ctx, envFor("CreateIdentity", hint))
		if err != nil {
			t.Fatalf("Hydrate: %v", err)
		}
		if _, ok := state.Context.KnownAbsent[target]; !ok {
			t.Fatalf("KnownAbsent = %v, want the floored key %q", state.Context.KnownAbsent, target)
		}
		if _, ok := state.Context.RequiredAbsent[probe]; !ok {
			t.Fatalf("RequiredAbsent = %v, want the undescribed key %q still fail-closed", state.Context.RequiredAbsent, probe)
		}
	})

	t.Run("an NFR-S6 operation with no descriptor refuses every declared key", func(t *testing.T) {
		// A cache that never saw an op-meta is the same condition as a
		// descriptor withdrawn by an upgrade: nothing is admitted, so the
		// operation over-denies where it is visible instead of quietly
		// reverting to the open envelope surface.
		bare := NewHydratorWithCache(conn, testCoreBucket, nil, testLogger())
		_, err := bare.Hydrate(ctx, envFor("ClaimIdentity", admitted["ClaimIdentity"]))
		var hErr *HydrationError
		if !errors.As(err, &hErr) || hErr.Code != "UndeclaredContextHintKey" {
			t.Fatalf("Hydrate = %v, want the closed-set refusal", err)
		}
	})
}

// TestHydrate_NFRS6ClosureRunsOnTheSubmittersDeclarationAlone pins the ORDER
// the whole rule rests on: the closure's subject is env.ContextHint, resolved
// at the head of step 4, BEFORE `derive_reads` runs.
//
// The ordering is not cosmetic in either direction. Derived keys are the DDL's
// own — authored by the package, not priced by the caller — so a closure that
// saw the merged set would refuse the package's own class-(g) probes and make
// every conforming submission of these two operations unsubmittable. And a
// refusal that happened after the derivation would have already run a Starlark
// program on the submitter's payload inside the rejection quantum, which is the
// work the closure exists to keep out of it.
//
// Two arms, because one alone does not pin the order. The first moves the
// subject (a derived key must survive); the second moves the POINT (the
// refusal must beat a derivation that faults).
func TestHydrate_NFRS6ClosureRunsOnTheSubmittersDeclarationAlone(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	claimRoot := "vtx.meta." + tplID
	seedOpMeta(t, ctx, conn, tplID, "ClaimIdentity",
		dispatchAspect(claimRoot, templatesJSON(claimDescriptor.OptionalReads), false), false)

	target := "vtx.identity." + testNanoIDAbsent
	// A key no descriptor template can compile to, so the closure would refuse
	// it if it ever became the closure's subject. It is exactly the shape of
	// identity-domain's own credentialindex dedup probe: derived from the
	// actor by a hash the template vocabulary cannot express.
	derivedKey := "vtx.credentialindex." + testNanoID2
	seedDeriveDDL(t, ctx, conn, `
def derive_reads(op):
    return {"optionalReads": ["`+derivedKey+`"]}
`)

	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h := NewHydratorWithCache(conn, testCoreBucket, cache, testLogger())

	envFor := func(hint *ContextHint) *OperationEnvelope {
		env := newTestEnvelope(testNanoID1)
		env.OperationType = "ClaimIdentity"
		env.Payload = []byte(`{"targetIdentityKey":"` + target + `"}`)
		env.ContextHint = hint
		return env
	}

	t.Run("a derived key the descriptor does not name still joins the set", func(t *testing.T) {
		state, err := h.Hydrate(ctx, envFor(shippedClaimHint(target)))
		if err != nil {
			t.Fatalf("Hydrate: %v — the closure refused the package's OWN derived key, so no conforming submission of this operation can succeed", err)
		}
		if _, ok := state.Context.KnownAbsent[derivedKey]; !ok {
			t.Fatalf("KnownAbsent = %v, want the derived key %q — a class-(g) key is outside the closure",
				state.Context.KnownAbsent, derivedKey)
		}
	})

	t.Run("the refusal beats a derivation that faults", func(t *testing.T) {
		// The derivation for THIS envelope raises, so the fault code names
		// which of the two ran first. UndeclaredContextHintKey means the
		// closure answered before any Starlark ran on the payload;
		// DeriveReadsFailed means the check moved past it.
		faulting, faultConn, _, _, _ := setupTestPipeline(t)
		seedOpMeta(t, faulting, faultConn, tplID, "ClaimIdentity",
			dispatchAspect(claimRoot, templatesJSON(claimDescriptor.OptionalReads), false), false)
		seedDeriveDDL(t, faulting, faultConn, `
def derive_reads(op):
    fail("this derivation must never be reached for a refused envelope")
`)
		faultCache := NewDDLCache(faultConn, testCoreBucket, testLogger())
		if err := faultCache.Refresh(faulting); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		fh := NewHydratorWithCache(faultConn, testCoreBucket, faultCache, testLogger())

		probe := "vtx.identity." + testNanoIDAbsent2
		hint := &ContextHint{OptionalReads: append(append([]string{}, shippedClaimHint(target).OptionalReads...), probe)}
		_, err := fh.Hydrate(faulting, envFor(hint))
		var hErr *HydrationError
		if !errors.As(err, &hErr) {
			t.Fatalf("Hydrate = %v, want a *HydrationError", err)
		}
		if hErr.Code != "UndeclaredContextHintKey" {
			t.Fatalf("code = %q, want UndeclaredContextHintKey — the derivation ran on a payload the closure had already refused, i.e. inside the rejection quantum", hErr.Code)
		}
	})
}

// templatesJSON renders read templates as the JSON array the `.dispatch` aspect
// carries.
func templatesJSON(templates []string) string {
	quoted := make([]string, len(templates))
	for i, tpl := range templates {
		quoted[i] = `"` + tpl + `"`
	}
	return "[" + strings.Join(quoted, ",") + "]"
}
