package processor

import (
	"errors"
	"strings"
	"testing"
)

// The trusted-engine identity keys this package's INTERNAL tests submit egress
// declarations under. They are fixed rather than bootstrap-derived because
// internal/processor's own tests cannot import internal/testutil (import
// cycle), and the predicate compares an envelope's actor against the map's
// values — nothing about the values' provenance is load-bearing to it.
const (
	testLoomActorID   = "Lm7Kn2Pq8Rs4Tv6Wx9Yz"
	testWeaverActorID = "Wv3Xy5Zb7Cd9Ef2Gh4Jk"
)

// testPrimordialActors is the admitted set an in-package test wires onto a
// Hydrator that must hydrate a class-(f) egress read. It mirrors the shape
// cmd/processor's AuthWiring builds: engine name → the engine's identity key.
var testPrimordialActors = map[string]string{
	"loom":   "vtx.identity." + testLoomActorID,
	"weaver": "vtx.identity." + testWeaverActorID,
}

// withPrimordialActors wires the admitted set onto a Hydrator, which is what a
// harness driving an egress declaration must do — the predicate has no
// test-only arm, so a test that skips this is refused exactly as production
// would refuse an unwired deployment.
func withPrimordialActors(h *HydratorImpl) *HydratorImpl {
	h.PrimordialActors = testPrimordialActors
	return h
}

// asPrimordialEngine stamps Loom's identity key as the envelope's actor, the
// submitter every live egress declaration in the tree carries.
func asPrimordialEngine(env *OperationEnvelope) *OperationEnvelope {
	env.Actor = testPrimordialActors["loom"]
	return env
}

// egressAuthSubjectID is the subject whose sensitive aspect the refusal tests
// declare. It is distinct from the envelope's actor id so "the fault names no
// key" can be asserted against the id itself, not merely the whole key string.
const egressAuthSubjectID = "Ss5Tt6Uu7Vv8Ww9Xx1Yz"

// TestEgressDeclaration_NonEngineActor_RefusedWithoutNamingTheKey is the
// negative: an ordinary submitter naming a key under egressReads is refused
// terminally, and neither the fault nor the operator's log line repeats the key
// it named. WHERE the refusal happens is pinned separately, by
// TestEgressDeclaration_RefusedBeforeAnyKeyIsResolved.
func TestEgressDeclaration_NonEngineActor_RefusedWithoutNamingTheKey(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)

	logger, logs := capturingLogger()
	seedSensitiveAspectClassDDL(t, ctx, conn, "ssn", true)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	h := withPrimordialActors(NewHydratorWithCache(conn, testCoreBucket, cache, logger))

	aspectKey := "vtx.identity." + egressAuthSubjectID + ".ssn"
	seedCiphertextAspect(t, ctx, conn, aspectKey, "ssn")

	// newTestEnvelope's actor is an ordinary identity, not an engine — the
	// whole population this predicate removes from the class.
	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}

	_, err := h.Hydrate(ctx, env)
	var hErr *HydrationError
	if !errors.As(err, &hErr) {
		t.Fatalf("Hydrate = %v (%T), want a *HydrationError — an egress declaration from a non-engine submitter must refuse the operation", err, err)
	}
	if hErr.Code != "EgressDeclarationUnauthorized" {
		t.Fatalf("Code = %q, want EgressDeclarationUnauthorized", hErr.Code)
	}
	if hErr.MissingKey != "" {
		t.Fatalf("MissingKey = %q, want empty — classifyStepError copies it into the reply's details verbatim", hErr.MissingKey)
	}
	if strings.Contains(hErr.Error(), aspectKey) || strings.Contains(hErr.Error(), egressAuthSubjectID) {
		t.Fatalf("fault text = %q, want it to name neither the declared key nor its subject: the refused key is the submitter's own probe", hErr.Error())
	}

	// The operator's only copy: enough to attribute the refusal, still no key.
	line := logs.String()
	if !strings.Contains(line, env.Actor) {
		t.Fatalf("Warn = %q, want it to name the refused actor — an operator who cannot attribute a refusal cannot act on it", line)
	}
	if !strings.Contains(line, "declaredCount=1") {
		t.Fatalf("Warn = %q, want the size of the refused declaration", line)
	}
	if strings.Contains(line, aspectKey) || strings.Contains(line, egressAuthSubjectID) {
		t.Fatalf("Warn = %q, want no declared key in it", line)
	}
}

// TestEgressDeclaration_RefusedBeforeAnyKeyIsResolved pins WHEN the refusal
// happens, which is the half the returned error cannot show: Hydrate answers
// HydratedState{} on every error path, so inspecting the returned state proves
// nothing about whether the declared key was resolved first.
//
// The declared key is seeded as a TOMBSTONED sensitive aspect instead, because
// that state has its own distinct refusal one layer down — decryptSensitiveDoc
// refuses a deleted aspect outright under the egress disposition
// (sensitive_decrypt.go, "read deleted sensitive aspect"), since a
// $sensitiveRef over a dead aspect is a capability that must not leave the
// Processor. So the two refusals are DISTINGUISHABLE, and which one comes back
// says which ran: an admission predicate that resolved the key first would
// answer the tombstone refusal, and only one that runs before any key is
// resolved can answer EgressDeclarationUnauthorized.
//
// That is the property the design rests on — refused before the mint, so the
// decrypt and the MAC never happen and no audit record lands against an
// uninvolved identity — and it is a claim about ORDER, so it needs an
// order-sensitive observation.
func TestEgressDeclaration_RefusedBeforeAnyKeyIsResolved(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := withPrimordialActors(newEgressTestHydrator(t, ctx, conn, nil))

	aspectKey := "vtx.identity." + egressAuthSubjectID + ".ssn"
	seedDeletedCiphertextAspect(t, ctx, conn, aspectKey, "ssn")

	// The control first: under an ENGINE actor the predicate admits, the key IS
	// resolved, and the tombstone refusal is what comes back. Without this arm
	// the assertion below could pass on a fixture that never seeded anything.
	engineEnv := asPrimordialEngine(newTestEnvelope(testNanoID1))
	engineEnv.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}
	_, engineErr := h.Hydrate(ctx, engineEnv)
	if engineErr == nil || !strings.Contains(engineErr.Error(), "read deleted sensitive aspect") {
		t.Fatalf("admitted hydration = %v, want the tombstone refusal — this arm is what proves resolving the key is observable at all", engineErr)
	}

	// The same key, the same hydrator, an ordinary submitter: the admission
	// refusal, which the tombstone refusal would have preempted had the key
	// been resolved first.
	env := newTestEnvelope(testNanoID1)
	env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}
	_, err := h.Hydrate(ctx, env)
	var hErr *HydrationError
	if !errors.As(err, &hErr) || hErr.Code != "EgressDeclarationUnauthorized" {
		t.Fatalf("Hydrate = %v, want EgressDeclarationUnauthorized: the tombstone refusal here would mean the declared key was read, decrypted and judged BEFORE the submitter's authority to name it was", err)
	}

	// The same question one layer further out: the operation's own CLASS is
	// also resolved during step 4, and resolving it costs a cache lookup or two
	// Core KV GETs and answers NoDDLForClass — a fault that echoes the class
	// name. A submitter with no standing to declare an egress read must not be
	// able to reach that answer either, so the refusal precedes class
	// resolution too, and an unresolvable class does not preempt it.
	unknownClass := newTestEnvelope(testNanoID1)
	unknownClass.Class = "neverseeded"
	unknownClass.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}
	_, err = h.Hydrate(ctx, unknownClass)
	if !errors.As(err, &hErr) || hErr.Code != "EgressDeclarationUnauthorized" {
		t.Fatalf("Hydrate = %v, want EgressDeclarationUnauthorized: a NoDDLForClass here would mean the operation's class was resolved — and its name echoed — before the submitter's authority to declare was asked about", err)
	}
	if strings.Contains(hErr.Error(), "neverseeded") {
		t.Fatalf("fault text = %q, want it to name no class either", hErr.Error())
	}
}

// TestEgressDeclaration_EngineActor_HydratesTheRef is the negative's positive
// vector: the SAME envelope, the SAME hydrator, submitted by an engine, reaches
// the mint and authors the $sensitiveRef. Without it the refusal above could
// pass for any reason at all — a seeding mistake included.
func TestEgressDeclaration_EngineActor_HydratesTheRef(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	h := withPrimordialActors(newEgressTestHydrator(t, ctx, conn, nil))

	aspectKey := "vtx.identity." + egressAuthSubjectID + ".ssn"
	seedCiphertextAspect(t, ctx, conn, aspectKey, "ssn")

	assertRef := func(t *testing.T, actor string) {
		t.Helper()
		env := newTestEnvelope(testNanoID1)
		env.Actor = actor
		env.ContextHint = &ContextHint{EgressReads: []string{aspectKey}}

		state, err := h.Hydrate(ctx, env)
		if err != nil {
			t.Fatalf("Hydrate: %v", err)
		}
		sref, ok := state.Context.Hydrated[aspectKey].Data["$sensitiveRef"].(map[string]interface{})
		if !ok {
			t.Fatalf("data = %+v, want a $sensitiveRef marker", state.Context.Hydrated[aspectKey].Data)
		}
		if sref["ref"] != aspectKey {
			t.Fatalf("$sensitiveRef.ref = %v, want %q", sref["ref"], aspectKey)
		}
	}

	t.Run("loom", func(t *testing.T) { assertRef(t, testPrimordialActors["loom"]) })
	// The admitted set is every platform engine, not one hardcoded name: the
	// shipped guard corpus already pins external-emitting ops to Loom OR Weaver.
	t.Run("weaver", func(t *testing.T) { assertRef(t, testPrimordialActors["weaver"]) })
}

// TestRefuseEgressDeclarationFromNonEngine_AdmissionArms drives the predicate
// itself over the states no end-to-end hydration reaches: an unwired admitted
// set, an empty actor against an empty map value, and the two states the rule
// has no subject for.
func TestRefuseEgressDeclarationFromNonEngine_AdmissionArms(t *testing.T) {
	t.Parallel()
	const key = "vtx.identity." + egressAuthSubjectID + ".ssn"

	envWith := func(actor string, hint *ContextHint) *OperationEnvelope {
		env := newTestEnvelope(testNanoID1)
		env.Actor = actor
		env.ContextHint = hint
		return env
	}

	cases := []struct {
		name    string
		env     *OperationEnvelope
		actors  map[string]string
		refused bool
	}{
		{
			// An unwired deployment admits nobody. Fail-closed is the field's
			// own documented direction, so a pipeline driving an egress op must
			// wire the map exactly as one driving a primordial-pinned op must.
			name:    "a nil admitted set refuses every actor",
			env:     envWith("vtx.identity."+testLoomActorID, &ContextHint{EgressReads: []string{key}}),
			actors:  nil,
			refused: true,
		},
		{
			// The empty string is not a member. An engine name a deployment
			// left unwired binds "", and an envelope can carry an empty actor —
			// the two must not meet in the middle.
			name:    "an empty actor never matches an empty map value",
			env:     envWith("", &ContextHint{EgressReads: []string{key}}),
			actors:  map[string]string{"loom": ""},
			refused: true,
		},
		{
			name:    "no contextHint is admitted whatever the actor",
			env:     envWith("vtx.identity."+testNanoID2, nil),
			actors:  nil,
			refused: false,
		},
		{
			name:    "an empty egressReads list is admitted whatever the actor",
			env:     envWith("vtx.identity."+testNanoID2, &ContextHint{Reads: []string{key}}),
			actors:  nil,
			refused: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseEgressDeclarationFromNonEngine(tc.env, tc.actors, testLogger())
			if tc.refused {
				var hErr *HydrationError
				if !errors.As(err, &hErr) || hErr.Code != "EgressDeclarationUnauthorized" {
					t.Fatalf("err = %v, want the egress admission refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want admission — the rule has no subject here", err)
			}
		})
	}
}

// TestPrimordialEngineActor_MembershipIsOverValues pins the comparison's own
// shape: an envelope's actor carries an identity KEY, so a lookup by engine
// NAME would admit nothing and a lookup that ignored blanks would admit too
// much.
func TestPrimordialEngineActor_MembershipIsOverValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		actor  string
		actors map[string]string
		want   bool
	}{
		{"the engine's identity key is a member", "vtx.identity." + testLoomActorID, testPrimordialActors, true},
		{"another engine's identity key is a member too", "vtx.identity." + testWeaverActorID, testPrimordialActors, true},
		{"the engine NAME is not a member", "loom", testPrimordialActors, false},
		{"an ordinary identity is not a member", "vtx.identity." + testNanoID2, testPrimordialActors, false},
		{"a nil map has no members", "vtx.identity." + testLoomActorID, nil, false},
		{"an empty actor is never a member", "", testPrimordialActors, false},
		{"an empty actor does not match an empty map value", "", map[string]string{"loom": ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := primordialEngineActor(tc.actor, tc.actors); got != tc.want {
				t.Fatalf("primordialEngineActor(%q) = %v, want %v", tc.actor, got, tc.want)
			}
		})
	}
}

// TestEgressDeclaration_NFRS6Operation_CollapsesToTheGenericReply is the
// masking pin. The admission refusal runs BEFORE the closed-declared-set
// refusal that answers every other over-declaration on an NFR-S6 operation, so
// an egress declaration meets this refusal and never the closed-set one — and
// the two channels Contract #9 §9.3 equalizes must not tell them apart. Those
// channels are the REPLY and the claim-attempts COUNTER, and the pin is on
// exactly those two, never on the internal code, which is precisely what the
// caller must not learn.
//
// The operator's LOG is deliberately outside the pin, because it deliberately
// differs: the closed-set refusal logs the refused key (`declared=<key>`),
// while this one logs `declaredCount` alone. That asymmetry is the design's,
// not an oversight — the closed set refuses a key its own descriptor could have
// admitted, whereas an egress declaration's key is the submitter's own probe.
//
// The second arm is the refusal this one displaces, driven through the same
// pipeline: asserting the two answer IDENTICALLY is what makes "unchanged" a
// measurement rather than a claim about a constant.
//
// This is a NON-CHANGE assertion, so it is NOT revert-proof by design: it
// passes with the predicate deleted, because the refusal it displaces produces
// the same reply and the same bucket. That invariance IS the property pinned.
// The predicate's own existence is held by the negative and the ordering pin
// above; this test exists to catch a future edit that makes the two diverge.
func TestEgressDeclaration_NFRS6Operation_CollapsesToTheGenericReply(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, _, _ := setupTestPipeline(t)

	target := "vtx.identity." + egressAuthSubjectID
	probe := "vtx.identity." + testNanoIDAbsent

	drive := func(t *testing.T, hint *ContextHint) (*OperationReply, []string) {
		t.Helper()
		rec := &recordingClaimEmitter{}
		cp.deps.ClaimEmitter = rec
		env := nfrS6Envelope(t, "ClaimIdentity")
		env.ContextHint = hint

		outcome, reply := dispatchAndReply(t, ctx, conn, cp, env)
		if outcome != OutcomeRejected {
			t.Fatalf("outcome = %q, want Rejected — this arm must reach the collapse to mean anything", outcome)
		}
		if reply.Error == nil {
			t.Fatalf("reply = %+v, want a rejection error", reply)
		}
		if reply.Error.Code != ErrCodeClaimKeyInvalid {
			t.Fatalf("reply code = %q, want %q — moving the refusal one step earlier must not change the wire shape",
				reply.Error.Code, ErrCodeClaimKeyInvalid)
		}
		if reply.Error.Message != claimRejectionMessage {
			t.Fatalf("reply message = %q, want %q", reply.Error.Message, claimRejectionMessage)
		}
		if reply.Error.Details != nil {
			t.Fatalf("reply details = %+v, want nil", reply.Error.Details)
		}
		return reply, rec.seen()
	}

	// The egress declaration, refused by the admission predicate.
	egressReply, egressSeen := drive(t, &ContextHint{EgressReads: []string{target + ".ssn"}})
	if len(egressSeen) != 1 {
		t.Fatalf("claim attempts = %v, want exactly one — a collapsed rejection that records nothing is "+
			"invisible to the caller AND the operator at once, and one that records twice double-counts "+
			"the plane an operator reads for a brute-force signature", egressSeen)
	}
	if strings.Contains(egressReply.Error.Message, egressAuthSubjectID) {
		t.Fatalf("reply message = %q, want it to name no part of the declared key", egressReply.Error.Message)
	}

	// The displaced refusal: an over-declaration of any other class on the same
	// operation, refused by the closed declared-read set one step later.
	_, closedSetSeen := drive(t, &ContextHint{OptionalReads: []string{probe}})
	if len(closedSetSeen) != 1 || closedSetSeen[0] != egressSeen[0] {
		t.Fatalf("claim attempts: egress declaration recorded %v, the closed-set refusal it runs ahead of recorded %v — "+
			"an earlier refusal that lands in a different bucket moves a counter an operator watches",
			egressSeen, closedSetSeen)
	}
}

// TestEgressDeclaration_UncollapsedOperation_RepliesTheBlankMissingKey is the
// control, and the reply-shape pin for every operation outside the NFR-S6 set:
// the real code reaches the caller, and the details map carries a missingKey
// field that names nothing.
func TestEgressDeclaration_UncollapsedOperation_RepliesTheBlankMissingKey(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, _, _ := setupTestPipeline(t)

	target := "vtx.identity." + egressAuthSubjectID
	env := nfrS6Envelope(t, "CreateIdentity")
	env.ContextHint = &ContextHint{EgressReads: []string{target + ".ssn"}}

	outcome, reply := dispatchAndReply(t, ctx, conn, cp, env)
	if outcome != OutcomeRejected {
		t.Fatalf("outcome = %q, want Rejected", outcome)
	}
	if reply.Error == nil || reply.Error.Code != ErrCodeHydrationFailed {
		t.Fatalf("reply = %+v, want %q", reply.Error, ErrCodeHydrationFailed)
	}
	if got := reply.Error.Details["code"]; got != "EgressDeclarationUnauthorized" {
		t.Fatalf("details.code = %v, want EgressDeclarationUnauthorized", got)
	}
	if got, ok := reply.Error.Details["missingKey"]; !ok || got != "" {
		t.Fatalf("details.missingKey = %v (present=%v), want an empty string: the field is copied unconditionally and must name nothing", got, ok)
	}
	if strings.Contains(reply.Error.Message, egressAuthSubjectID) {
		t.Fatalf("reply message = %q, want it to name no part of the declared key", reply.Error.Message)
	}
}
