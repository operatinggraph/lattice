// The claim / credential-link guard equalization
// (nfr-s6-release-quantum-payload-design.md §4.1): both branches accumulate an
// outcome and fail once at the bottom, so no rejection cause exits before
// another and the causes stay indistinguishable in the time domain as well as
// on the wire.
//
// Two tests, and they pin different halves:
//
//   - TestClaimScript_GuardsFailOnceRatherThanCascading reads the shipped
//     script text and pins the SHAPE — one terminal fail per branch.
//   - TestCompleteCredentialLink_RejectionOutcomeWords drives one vector per
//     outcome word the link branch can render and pins the BEHAVIOUR — the
//     Health-KV word each cause is named by, and the single generic wire shape
//     they all collapse to.
package identitydomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	identitydomain "github.com/operatinggraph/lattice/packages/identity-domain"
)

// identityExecuteSource returns the body of the `identity` DDL script's
// execute() function, as shipped in the package Definition — the same text the
// Processor loads. Read from identitydomain.DDLs() rather than from the file so
// a template substitution or a second script definition cannot make the
// assertions below inspect something other than what runs.
func identityExecuteSource(t *testing.T) string {
	t.Helper()
	var script string
	for _, d := range identitydomain.DDLs() {
		if d.CanonicalName == "identity" {
			script = d.Script
		}
	}
	if script == "" {
		t.Fatal("no `identity` DDL script found")
	}
	idx := strings.Index(script, "\ndef execute(state, op):")
	if idx < 0 {
		t.Fatal("cannot locate execute() in the identity script")
	}
	return script[idx:]
}

// operationBranchSource slices one `if ot == "<opType>":` arm out of execute(),
// ending at whichever arm follows it. Both markers carry execute()'s own
// four-space body indent, which is what keeps this from matching the
// same-named arm inside derive_reads.
func operationBranchSource(t *testing.T, execute, opType string) string {
	t.Helper()
	marker := "\n    if ot == \"" + opType + "\":"
	start := strings.Index(execute, marker)
	if start < 0 {
		t.Fatalf("cannot locate the %s branch in execute()", opType)
	}
	rest := execute[start+len(marker):]
	end := strings.Index(rest, "\n    if ot == ")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// TestClaimScript_GuardsFailOnceRatherThanCascading pins the single-exit shape
// of the two secret-redemption branches.
//
// ClaimIdentity and CompleteCredentialLink each adjudicate several different
// facts about the graph — no such identity, one that exists but is in the wrong
// state, one whose secret the caller does not hold — and answer all of them
// with one wire shape (Contract #9 §9.3). A cascade of early returns answers
// them differently in TIME: each exit does strictly less work than the next,
// and monotone bias of a few tenths of a millisecond is recoverable by
// averaging against an endpoint nothing rate-limits. Both branches therefore
// accumulate an outcome (ddls.go's first_outcome) and call their fail helper
// exactly once, at the bottom.
//
// Four assertions per branch, and each one catches a mutation the others let
// through:
//
//   - the fail helper appears twice (its `def` plus one terminal call) — the
//     ordinary reintroduced early return;
//   - no bare `fail(` beyond the helper's own body — an early exit spelled
//     `fail("ClaimKeyInvalid: no-target")` never touches the helper's name;
//   - exactly one `return {` — an early `return {...}` leaves the branch just
//     as effectively as a fail and mentions neither;
//   - `crypto.sha256(` and `crypto.constant_time_equal(` appear once each, at
//     the branch body's own indent — this is the one that catches the mutation
//     the other three miss. Wrapping both crypto calls in
//     `if <secret usable> and stored_hash != None:` and setting the outcome in
//     the else keeps the fail count at 2, keeps every outcome word identical
//     and keeps the whole behavioural suite green, while re-opening the
//     script-half oracle outright: the cheap causes stop paying for a digest
//     and a compare. Design §10 T2 is the requirement; unnested is what "runs
//     on all causes" looks like in the text.
//
// What this proves and what it does not: it pins the SHAPE, and nothing more.
// That the outcome WORDS are still the ones each cause is named by is pinned
// behaviourally, by TestClaimIdentity_RejectionCausesIndistinguishable and
// TestCompleteCredentialLink_RejectionOutcomeWords, which drive one vector per
// word and assert the Health-KV counter each increments. Both halves are
// needed: the shape alone would survive a reordering that renamed every
// outcome, and the vectors alone would survive a reintroduced early return
// that answered the same words sooner.
func TestClaimScript_GuardsFailOnceRatherThanCascading(t *testing.T) {
	t.Parallel()
	execute := identityExecuteSource(t)

	for _, tc := range []struct{ opType, helper string }{
		{"ClaimIdentity", "fail_claim("},
		{"CompleteCredentialLink", "fail_link("},
	} {
		branch := operationBranchSource(t, execute, tc.opType)

		// Two occurrences: the `def` that names the helper, and the one
		// terminal call the accumulated outcome reaches.
		if n := strings.Count(branch, tc.helper); n != 2 {
			t.Fatalf("%s branch mentions %s %d time(s), want 2 (its `def` plus ONE terminal call) — "+
				"an added early return re-opens the timing separation between rejection causes: "+
				"a cause that exits early does strictly less work than one that runs on, and that "+
				"difference is an identity-existence oracle on a scope=self endpoint nothing "+
				"rate-limits (nfr-s6-release-quantum-payload-design.md §4.1). Accumulate the "+
				"outcome with first_outcome instead and let the single fail at the bottom render it.",
				tc.opType, tc.helper, n)
		}

		// One occurrence: the helper's own body. Counting the helper's NAME
		// cannot see an exit that calls the builtin directly.
		if n := strings.Count(branch, "fail("); n != 1 {
			t.Fatalf("%s branch calls the bare `fail(` builtin %d time(s), want 1 (only inside %s's own body) — "+
				"an early exit spelled fail(\"ClaimKeyInvalid: <word>\") leaves the branch without ever "+
				"naming the helper, so it passes the helper-count check above while separating that "+
				"cause from every other one in the time domain. Record the word with first_outcome and "+
				"let the terminal %s render it.",
				tc.opType, n, tc.helper, tc.helper)
		}

		// One occurrence: the success path's own result.
		if n := strings.Count(branch, "return {"); n != 1 {
			t.Fatalf("%s branch has %d `return {` statement(s), want 1 (the success result) — "+
				"an early `return {...}` exits the branch exactly as a fail does and mentions neither "+
				"the helper nor the builtin, so it slips past both checks above while making its "+
				"cause measurably cheaper than the ones that run on.",
				tc.opType, n)
		}

		assertUnnestedOnce(t, tc.opType, branch, "crypto.sha256(")
		assertUnnestedOnce(t, tc.opType, branch, "crypto.constant_time_equal(")
	}
}

// claimBranchBodyIndent is the column execute()'s per-operation branch bodies
// sit at: four spaces for execute()'s own body, four more for the `if ot ==`
// arm. A crypto call nested inside any further `if` is deeper than this.
const claimBranchBodyIndent = 8

// assertUnnestedOnce fails unless call appears on exactly one line of branch,
// at the branch body's own indent — i.e. evaluated once, on every cause,
// conditioned on nothing.
func assertUnnestedOnce(t *testing.T, opType, branch, call string) {
	t.Helper()
	var hits []string
	for _, line := range strings.Split(branch, "\n") {
		if strings.Contains(line, call) {
			hits = append(hits, line)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("%s branch contains %d %s call(s), want exactly 1 — every rejection cause must pay "+
			"the same crypto work, so this call is made once and unconditionally "+
			"(nfr-s6-release-quantum-payload-design.md §10 T2).",
			opType, len(hits), call)
	}
	indent := len(hits[0]) - len(strings.TrimLeft(hits[0], " "))
	if indent != claimBranchBodyIndent {
		t.Fatalf("%s branch makes its %s call at indent %d, want %d (the branch body's own level) — "+
			"the call is nested inside a condition, so the causes that do not enter it skip a digest "+
			"or a compare and answer sooner than the ones that do. That is the script-half timing "+
			"oracle this branch exists to have removed, and it survives every count-based check: "+
			"guarding both crypto calls with `if <secret usable> and stored_hash != None:` and setting "+
			"the outcome in the else keeps the fail count, the return count and every outcome word "+
			"exactly as they are (nfr-s6-release-quantum-payload-design.md §10 T2). Call it "+
			"unconditionally against PLACEHOLDER_SECRET / PLACEHOLDER_STORED_HASH instead.\n\tline: %s",
			opType, call, indent, claimBranchBodyIndent, strings.TrimRight(hits[0], " "))
	}
}

// TestCompleteCredentialLink_RejectionOutcomeWords drives one vector per
// outcome word the link branch renders and asserts two things per vector: the
// Health-KV counter that names the cause increments, and the reply carries the
// one generic shape every cause collapses to.
//
// The counter delta is what makes the wire-shape half non-vacuous. Several
// causes share a reply, so a vector that silently took some other branch would
// still match on the wire; only a strict increase on ITS OWN
// claim-attempts.<word> says the cause under test is the one that answered.
//
// Two words are covered by dedicated tests elsewhere and are not repeated here:
// `credential-not-provisioned` by credential_endpoint_guard_test.go's
// CompleteCredentialLink arms, and `erased` on the actor position by
// erasure_gate_test.go.
func TestCompleteCredentialLink_RejectionOutcomeWords(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)

	instance := claimInstance + "-cmpl-words"
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:      "cmpl-words",
		Instance:     instance,
		ClaimEmitter: processor.NewClaimAttemptEmitter(conn, testutil.HarnessHealthBucket, instance, testutil.TestLogger()),
	})

	// A2 is the submitting credential for every vector but the already-bound
	// one, and stays unbound throughout: every submission below is a rejection,
	// so nothing here binds it.
	const armedSecret = "link-secret-outcome-words"

	// A live, claimed identity with a live `.linkKey` — the fixture the
	// invalid-key vector needs, and the one the already-bound vector aims at so
	// its refusal is the dedup guard rather than a target fault. Seeded rather
	// than driven through the claim + arm ceremony because every fixture here
	// is a target, not a ceremony under test, and the ceremony would bind the
	// harness's own consumer credential to the first of them.
	armedTarget := "vtx.identity." + testutil.GenReqID("CmplWordsArmd0")
	seedDirectIdentity(t, ctx, conn, armedTarget, "claimed", "")
	seedSensitiveAspect(t, ctx, conn, armedTarget, "linkKey",
		map[string]any{"hash": sha256HexOf(armedSecret), "algo": "sha256"})

	// A target that was never provisioned at all.
	absentTarget := "vtx.identity." + testutil.GenReqID("CmplWordsGone0")

	// An armed target that never claimed: the link ceremony requires
	// state=claimed, so this is the wrong-state cause with the secret held.
	unclaimedTarget := "vtx.identity." + testutil.GenReqID("CmplWordsUncl0")
	seedDirectIdentity(t, ctx, conn, unclaimedTarget, "unclaimed", "")
	seedSensitiveAspect(t, ctx, conn, unclaimedTarget, "linkKey",
		map[string]any{"hash": sha256HexOf(armedSecret), "algo": "sha256"})

	// An armed target that was merged away. `merged` is its own word, ranked
	// above wrong-state, so a survivor's key is the answer a caller would need
	// and never gets.
	mergedTarget := "vtx.identity." + testutil.GenReqID("CmplWordsMrgd0")
	seedDirectIdentity(t, ctx, conn, mergedTarget, "merged", "vtx.identity.SurvivorVtxNPQRSTUVW")
	seedSensitiveAspect(t, ctx, conn, mergedTarget, "linkKey",
		map[string]any{"hash": sha256HexOf(armedSecret), "algo": "sha256"})

	// A claimed target whose `.linkKey` body carries a hash of the wrong TYPE.
	// crypto.constant_time_equal refuses a non-string operand with a builtin
	// argument error, so the branch substitutes its fixed-length stand-in
	// comparand and this renders the ordinary invalid-key rather than a
	// distinguishable script fault.
	badHashTarget := "vtx.identity." + testutil.GenReqID("CmplWordsBadH0")
	seedDirectIdentity(t, ctx, conn, badHashTarget, "claimed", "")
	seedSensitiveAspect(t, ctx, conn, badHashTarget, "linkKey",
		map[string]any{"hash": 42, "algo": "sha256"})

	// An armed, claimed target sealed for erasure. The gate sits BELOW the
	// secret comparison, so this vector submits the CORRECT secret — a wrong
	// one would render invalid-key and never reach the gate, which is exactly
	// the counter-attribution the placement buys.
	sealedTarget := "vtx.identity." + testutil.GenReqID("CmplWordsSeal0")
	seedDirectIdentity(t, ctx, conn, sealedTarget, "claimed", "")
	seedSensitiveAspect(t, ctx, conn, sealedTarget, "linkKey",
		map[string]any{"hash": sha256HexOf(armedSecret), "algo": "sha256"})
	sealForErasure(t, ctx, conn, sealedTarget)

	// A3 is a provisioned credential that already carries a credentialindex, so
	// the one-credential-≤-one-identity guard refuses it whatever the target
	// says. Its secret is deliberately WRONG: credential-already-bound ranks
	// above invalid-key, so the vector proves the ranking as well as the word,
	// and cannot accidentally succeed.
	testutil.SeedCapDoc(t, ctx, conn, thirdCredCapDoc())
	testutil.SeedCredentialActor(t, ctx, conn, thirdCredActorKey, consumerRoleKey(t))
	seedBoundCredentialIndex(t, ctx, conn, thirdCredActorKey, "vtx.identity."+testutil.GenReqID("CmplWordsPrior0"))

	var shapes []string
	for _, tc := range []struct {
		name    string
		reqID   string
		actor   string
		target  string
		secret  string
		outcome string
		// omitSecret drops linkKey from the payload entirely. The op's
		// InputSchema requires only targetIdentityKey, so this reaches the
		// script with no secret to hash — the vector that proves the branch
		// still runs both crypto calls, against PLACEHOLDER_SECRET and
		// PLACEHOLDER_STORED_HASH, rather than skipping them.
		omitSecret bool
	}{
		{"no-target", testutil.GenReqID("CmplWordsNoTgt"), secondCredActorKey, absentTarget, armedSecret, "no-target", false},
		{"unclaimed-target", testutil.GenReqID("CmplWordsUnclS"), secondCredActorKey, unclaimedTarget, armedSecret, "wrong-state", false},
		{"merged-target", testutil.GenReqID("CmplWordsMrgdS"), secondCredActorKey, mergedTarget, armedSecret, "merged", false},
		{"wrong-key", testutil.GenReqID("CmplWordsWrong"), secondCredActorKey, armedTarget, "a-guessed-wrong-secret", "invalid-key", false},
		{"absent-key", testutil.GenReqID("CmplWordsNoKey"), secondCredActorKey, armedTarget, "", "invalid-key", true},
		{"stored-hash-wrong-type", testutil.GenReqID("CmplWordsBadHs"), secondCredActorKey, badHashTarget, armedSecret, "invalid-key", false},
		{"sealed-target", testutil.GenReqID("CmplWordsSealS"), secondCredActorKey, sealedTarget, armedSecret, "erased", false},
		{"already-bound-credential", testutil.GenReqID("CmplWordsBound"), thirdCredActorKey, armedTarget, "a-guessed-wrong-secret", "credential-already-bound", false},
	} {
		before, _ := readClaimHealthCounter(t, ctx, conn, instance, tc.outcome)

		env := completeLinkEnv(tc.reqID, tc.actor, tc.target, tc.secret)
		if tc.omitSecret {
			env.Payload = json.RawMessage(`{"targetIdentityKey":"` + tc.target + `"}`)
		}
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		if outcome != processor.OutcomeRejected {
			t.Fatalf("%s: outcome = %q, want rejected", tc.name, outcome)
		}
		assertGenericClaimRejection(t, reply)
		shapes = append(shapes, string(reply.Error.Code)+"|"+strings.Repeat("d", len(reply.Error.Details)))

		if count, ok := readClaimHealthCounter(t, ctx, conn, instance, tc.outcome); !ok || count <= before {
			t.Fatalf("%s: claim-attempts.%s = %d (found=%v), was %d before this submission — "+
				"THIS cause never reached the branch that records it, so the wire-shape match is vacuous",
				tc.name, tc.outcome, count, ok, before)
		}
	}
	for i := 1; i < len(shapes); i++ {
		if shapes[i] != shapes[0] {
			t.Fatalf("rejection shapes differ: %q vs %q (%v)", shapes[0], shapes[i], shapes)
		}
	}
}

// seedBoundCredentialIndex writes the credentialindex vertex a bound credential
// carries, at the key credential_index_key derives for actorKey.
func seedBoundCredentialIndex(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey, ownerKey string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"class":     "credentialindex",
		"isDeleted": false,
		"data": map[string]any{
			"actorKey":    actorKey,
			"identityKey": ownerKey,
			"boundAt":     "2026-08-01T09:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("marshal credentialindex: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, credentialIndexKey(actorKey), raw); err != nil {
		t.Fatalf("seed credentialindex for %s: %v", actorKey, err)
	}
}
