// Erasure write-path gates (erasure-orchestration-design.md §6) — the guards
// that make the erased set CLOSED, so the residue count the completion seal
// rests on means "erased" rather than "zero at projection time".
//
// Most cases are a PAIR over ONE corpus: the identical envelope submitted
// against the identical identity and actor twice, with only the marker
// changing. That construction is what makes a bare rejection assertion mean
// "the erasure gate refused" — each op has four or five other refusals in
// front of this one, and on the claim and link paths NFR-S6's anti-enumeration
// reclassification strips the script's outcome word before it reaches the
// reply, so the text cannot name the guard that fired.
//
// Where the reply cannot carry the outcome, HEALTH KV can, and the tests read
// it. Without that, swapping fail_claim("erased") for fail_claim("wrong-state")
// would leave every behavioural assertion green while silently retiring the
// only channel an operator can see this on.
//
// The marker is written straight to Core KV rather than by submitting
// SealIdentityForErasure: that op lives in privacy-base, which this package's
// harness does not install. What the gate consumes is the key's presence and
// class, which is exactly what these fixtures reproduce.
package identitydomain_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	identitydomain "github.com/operatinggraph/lattice/packages/identity-domain"
)

// sealForErasureAs writes a document at the marker key with a caller-chosen
// class and tombstone flag. The class is a parameter because the gate checks
// it: privacy-base records that its aspect-type DDL gates the CLASS and not
// the key, so any package script can write some other class there, and a gate
// keyed on mere presence would let such a write shut a person's account
// permanently with nothing able to reopen it.
func sealForErasureAs(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey, class string, tombstoned bool) {
	t.Helper()
	doc := map[string]any{
		"class":     class,
		"vertexKey": identityKey,
		"localName": "erasureRequested",
		"isDeleted": tombstoned,
		"data": map[string]any{
			"requestedAt": "2026-08-07T09:00:00Z",
			"shreddedAt":  "2026-08-07T08:59:00Z",
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal erasureRequested for %s: %v", identityKey, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".erasureRequested", raw); err != nil {
		t.Fatalf("seed erasureRequested on %s: %v", identityKey, err)
	}
}

// sealForErasure writes the marker exactly as SealIdentityForErasure does.
func sealForErasure(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) {
	t.Helper()
	sealForErasureAs(t, ctx, conn, identityKey, "erasureRequested", false)
}

// unsealForErasure hard-removes the marker. Nothing in production does this —
// its non-removal is the convention §7's convergence rests on — but it is what
// lets a test change exactly one fact and re-run.
func unsealForErasure(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) {
	t.Helper()
	if err := conn.KVDelete(ctx, testutil.HarnessCoreBucket, identityKey+".erasureRequested"); err != nil {
		t.Fatalf("remove erasureRequested from %s: %v", identityKey, err)
	}
}

// assertKeyAbsent distinguishes a genuinely absent key from a failed read: any
// bucket or connection fault would otherwise read as "absent" and pass.
func assertKeyAbsent(t *testing.T, ctx context.Context, conn *substrate.Conn, key, why string) {
	t.Helper()
	_, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err == nil {
		t.Fatalf("%s still exists — %s", key, why)
	}
	if !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("KVGet %s: %v — cannot conclude the key is absent from this error", key, err)
	}
}

func assertKeyPresent(t *testing.T, ctx context.Context, conn *substrate.Conn, key, why string) {
	t.Helper()
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key); err != nil {
		t.Fatalf("KVGet %s: %v — %s", key, err, why)
	}
}

func erasureClaimEnv(reqID, actorKey, identityKey, claimKeyPlaintext string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-07T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + claimKeyPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: actorKey},
		// No erasureRequested declaration: derive_reads supplies it, and a test
		// that named it by hand would stay green with the derivation deleted.
		ContextHint: &processor.ContextHint{
			Reads: []string{identityKey, identityKey + ".state", identityKey + ".claimKey"},
		},
	}
}

// TestErasureGate_ClaimIdentity_RejectsSealedTarget is the resurrection test
// for the claim path: a sealed identity must not acquire a new credential, and
// so must not gain the credentialindex vertex and boundTo link a claim writes.
func TestErasureGate_ClaimIdentity_RejectsSealedTarget(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "erase-claim")

	uKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("EraseClaim"))
	sealForErasure(t, ctx, conn, uKey)

	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("EraseClaimSealed"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	assertKeyAbsent(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"the gate refused the claim but a credentialindex still landed")
	assertKeyAbsent(t, ctx, conn, boundToLinkKey(consumerActorKey, uKey),
		"the gate refused the claim but a boundTo link still landed")

	// The outcome word is stripped from the reply, so Health KV is the only
	// place it survives — and without this assertion the gate could refuse
	// under ANY existing outcome and every other check here would still pass.
	instance := claimInstance + "-erase-claim"
	if count, ok := readClaimHealthCounter(t, ctx, conn, instance, "erased"); !ok || count < 1 {
		t.Fatalf("claim-attempts.erased = (%d, found=%v), want >=1 — the refusal did not record the erasure outcome, so an operator has no signal that a sealed identity is being claimed",
			count, ok)
	}

	// One fact changed, nothing else. If this does not now succeed, the
	// rejection above belonged to some other guard and proved nothing.
	unsealForErasure(t, ctx, conn, uKey)
	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("EraseClaimOpen"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
	assertKeyPresent(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"the unsealed claim must write the credentialindex the sealed one was refused")
}

// TestErasureGate_ClaimIdentity_RejectsSealedActor covers the position the
// first draft of this gate missed. UnbindIdentityCredentials erases boundTo in
// BOTH directions — its own comment says why: the identity is the target of
// every credential bound to it and the SOURCE when it is itself someone
// else's credential. So an erased identity used AS a credential writes two
// records inside that sweep's own erasure set, and must be refused here too.
func TestErasureGate_ClaimIdentity_RejectsSealedActor(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-actor")

	uKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("EraseActor"))

	// The TARGET is clean; the submitting credential is the sealed one.
	sealForErasure(t, ctx, conn, consumerActorKey)
	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("EraseActorSealed"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	assertKeyAbsent(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"a sealed identity was allowed to become someone else's credential")
	assertKeyAbsent(t, ctx, conn, boundToLinkKey(consumerActorKey, uKey),
		"a sealed identity was allowed to become the SOURCE of a fresh boundTo")

	unsealForErasure(t, ctx, conn, consumerActorKey)
	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("EraseActorOpen"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
}

// TestErasureGate_WrongSecretStillCountsAsInvalidKey pins the gate's POSITION,
// which is a property no other test here can see. The gate sits below the
// claim-secret comparison so that a brute-force attempt against a sealed
// identity keeps landing in claim-attempts.invalid-key — the counter an
// operator watches — instead of being diverted into claim-attempts.erased,
// and so that a caller holding no valid secret learns nothing from a shorter
// code path.
func TestErasureGate_WrongSecretStillCountsAsInvalidKey(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "erase-order")

	uKey, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("EraseOrder"))
	sealForErasure(t, ctx, conn, uKey)

	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("EraseOrderWrong"), consumerActorKey, uKey, "not-the-secret"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	instance := claimInstance + "-erase-order"
	if count, ok := readClaimHealthCounter(t, ctx, conn, instance, "invalid-key"); !ok || count < 1 {
		t.Fatalf("claim-attempts.invalid-key = (%d, found=%v), want >=1 — a wrong-secret attempt against a sealed identity must still count as a failed secret, or the brute-force counter goes quiet exactly where it matters",
			count, ok)
	}
	if count, ok := readClaimHealthCounter(t, ctx, conn, instance, "erased"); ok && count > 0 {
		t.Fatalf("claim-attempts.erased = %d — the erasure gate fired ahead of the secret comparison, diverting a brute-force attempt out of invalid-key", count)
	}
}

// TestErasureGate_CompleteCredentialLink_RejectsSealedIdentity — the second
// door onto the same two representations, reachable only through an armed link
// secret. The secret survives a refusal (it is tombstoned on success only), so
// the sealed and unsealed runs share one arming.
func TestErasureGate_CompleteCredentialLink_RejectsSealedIdentity(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-link")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "EraseLink")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")

	secret := "link-secret-erase-gate"
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("EraseLinkArm"), uKey, sha256HexOf(secret)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	sealForErasure(t, ctx, conn, uKey)
	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("EraseLinkSealed"), secondCredActorKey, uKey, secret))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	assertKeyAbsent(t, ctx, conn, credentialIndexKey(secondCredActorKey),
		"the gate refused the link but a credentialindex still landed")
	assertKeyAbsent(t, ctx, conn, boundToLinkKey(secondCredActorKey, uKey),
		"the gate refused the link but a boundTo link still landed")

	unsealForErasure(t, ctx, conn, uKey)
	testutil.PublishOp(t, conn, completeLinkEnv(testutil.GenReqID("EraseLinkOpen"), secondCredActorKey, uKey, secret))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertLiveBoundTo(t, ctx, conn, secondCredActorKey, uKey)
	assertKeyPresent(t, ctx, conn, credentialIndexKey(secondCredActorKey),
		"the unsealed link must write the credentialindex the sealed one was refused")
}

// TestErasureGate_ReconcileCredentialBinding_RejectsSealedIdentity — the
// republish path. This op restores a boundTo edge from a credentialindex the
// shred deliberately left standing, which makes it the most direct way to undo
// an erasure in progress. Its outcome word DOES survive to the reply
// (CredentialReconcileRejected is not reclassified), so this one names the
// guard as well as pairing the runs.
func TestErasureGate_ReconcileCredentialBinding_RejectsSealedIdentity(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-recon")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "EraseRecon")
	dropBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	sealForErasure(t, ctx, conn, uKey)

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		reconcileEnv(testutil.GenReqID("EraseReconSealed"), consumerActorKey, uKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected — the erasure gate did not fire", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "CredentialReconcileRejected: erased") {
		t.Fatalf("rejected with %+v, want the `erased` guard — a rejection from anywhere else means the gate never ran", reply.Error)
	}
	assertKeyAbsent(t, ctx, conn, boundToLinkKey(consumerActorKey, uKey),
		"the gate refused the reconcile but the edge was republished anyway")

	unsealForErasure(t, ctx, conn, uKey)
	testutil.PublishOp(t, conn, reconcileEnv(testutil.GenReqID("EraseReconOpen"), consumerActorKey, uKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
}

// TestErasureGate_Reconcile_UnknownIdentityIsNotBound pins the reconcile
// gate's POSITION. It sits below not-bound and owner-mismatch, so this op
// cannot be used to ask "is this identity sealed for erasure?" about an
// arbitrary key — which it could if the gate ran first, and which would
// contradict its own permission Note that it reaches nothing the index does
// not already assert. Unlike the claim path, this outcome word IS on the wire.
func TestErasureGate_Reconcile_UnknownIdentityIsNotBound(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-recon-oracle")

	strangerKey := "vtx.identity." + testutil.GenReqID("EraseStranger")
	sealForErasure(t, ctx, conn, strangerKey)

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		reconcileEnv(testutil.GenReqID("EraseOracleDo"), secondCredActorKey, strangerKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", outcome)
	}
	if reply.Error == nil || strings.Contains(reply.Error.Message, "erased") {
		t.Fatalf("rejected with %+v, want not-bound — leaking `erased` for an identity with no credentialindex turns this op into an erasure oracle over arbitrary keys", reply.Error)
	}
}

// TestErasureGate_CreateUnclaimedIdentity_SkipsSealedIncumbent is §6's fifth
// row, and the one an earlier draft of this fire argued away. A live contact
// index hit is what turns a new registration into a duplicateOf naming the
// incumbent — a link key plus its match criteria, in plaintext. If that
// incumbent is sealed, the link is a BRAND-NEW correlation to a person who
// asked to be forgotten, written after the seal. It needs no exotic path: the
// name index always matches, so an ordinary same-named walk-in during the
// convergence window does it.
func TestErasureGate_CreateUnclaimedIdentity_SkipsSealedIncumbent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-erase-dedup")

	const sharedEmail = "erasure.dedup@example.com"
	emailIdxKey := contactIndexKey("email", sharedEmail)

	createContact := func(label, name string) string {
		t.Helper()
		reqID := testutil.GenReqID(label)
		env := &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "CreateUnclaimedIdentity",
			Actor:         staffActorKey,
			SubmittedAt:   "2026-08-07T10:00:00Z",
			Class:         "identity",
			Payload: json.RawMessage(`{"name":"` + name + `","email":"` + sharedEmail +
				`","claimKeyHash":"` + sha256HexOf("claim-"+label) + `"}`),
			ContextHint: &processor.ContextHint{OptionalReads: []string{emailIdxKey}},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
		return identityIDFromRequestID(reqID)
	}

	incumbentID := createContact("EraseDedupInc", "Dedup Erasure One")
	incumbentKey := "vtx.identity." + incumbentID

	// Positive vector FIRST: with no marker, the same contact really does
	// produce a duplicateOf. Without it the negative case below could pass
	// simply because the dedup never matched at all.
	dupID := createContact("EraseDedupDup", "Dedup Erasure Two")
	assertKeyPresent(t, ctx, conn, duplicateOfLinkKey(dupID, incumbentKey),
		"an unsealed incumbent must still be deduped against, or the sealed case below proves nothing")

	// Now seal the incumbent and register the same contact again.
	sealForErasure(t, ctx, conn, incumbentKey)
	afterID := createContact("EraseDedupAftr", "Dedup Erasure Three")

	assertKeyAbsent(t, ctx, conn, duplicateOfLinkKey(afterID, incumbentKey),
		"a fresh duplicateOf named an identity sealed for erasure — the erased set grew after the seal, which is exactly what §6 exists to forbid")
}

// TestErasureGate_TombstonedMarkerStillCloses — presence of the right class is
// the signal, not liveness. Nothing removes the marker (no aspect-type DDL can
// refuse a tombstone, so its non-removal is a review-held convention), and a
// gate that reopened on one would let the erased set grow again exactly when
// that was least observable.
func TestErasureGate_TombstonedMarkerStillCloses(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-tomb")

	uKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("EraseTomb"))
	sealForErasureAs(t, ctx, conn, uKey, "erasureRequested", true)

	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("EraseTombSealed"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	unsealForErasure(t, ctx, conn, uKey)
	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("EraseTombOpen"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestErasureGate_WrongClassAtMarkerKeyDoesNotClose — the gate checks the
// CLASS, not just the key. privacy-base records that its aspect-type DDL gates
// the class rather than the key, so a mutation at this key declaring some
// other class falls to the permissive default and any package script could
// write one. A presence-only gate would let such a write shut a person's
// claim, link, reconcile and merge paths permanently, with no op able to
// remove the marker and reopen them.
func TestErasureGate_WrongClassAtMarkerKeyDoesNotClose(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-class")

	uKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("EraseClass"))
	sealForErasureAs(t, ctx, conn, uKey, "note", false)

	testutil.PublishOp(t, conn, erasureClaimEnv(testutil.GenReqID("EraseClassDo"), consumerActorKey, uKey, claimKeyPlaintext))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestErasureGate_MalformedIdentityKeyIsACleanRejection — a derivation must
// never fail. The Processor validates every key derive_reads returns against
// the Contract #1 grammar and answers a malformed one with DeriveReadsInvalid,
// a hydration fault raised BEFORE the operation's own validation. Deriving
// straight off an unvalidated payload would therefore turn this clean
// ClaimKeyInvalid into an opaque HydrationFailed — a new, distinguishable wire
// code on an NFR-S6-protected path that any caller could reach.
func TestErasureGate_MalformedIdentityKeyIsACleanRejection(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-malformed")

	for _, bad := range []string{"vtx.identity.x", "vtx.identity.a.b", "vtx.identity."} {
		env := erasureClaimEnv(testutil.GenReqID("EraseMalf"+strings.NewReplacer(".", "", "-", "").Replace(bad)), consumerActorKey, bad, "irrelevant")
		// The target is malformed, so nothing about it can be declared.
		env.ContextHint = nil
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		if outcome != processor.OutcomeRejected {
			t.Fatalf("%q: outcome = %q, want rejected", bad, outcome)
		}
		if reply.Error != nil && strings.Contains(string(reply.Error.Code), "Hydration") {
			t.Fatalf("%q: rejected with %s — a malformed payload must reach the script's own clean rejection, not fault during hydration",
				bad, reply.Error.Code)
		}
	}
}

// TestErasureGate_DeriveReadsCoversBothPositions pins the class-(g) derivation
// as TEXT, because its effect is invisible behaviourally: declared or not, the
// gate refuses identically, since the script reads through kv.Read and an
// undeclared read falls through to a live Core KV GET. The behavioural tests
// above prove the gate holds with no help from any submitter; only this can
// show the derivation still exists, and losing it would silently add a Core KV
// round trip to every claim, link and reconcile.
func TestErasureGate_DeriveReadsCoversBothPositions(t *testing.T) {
	t.Parallel()
	var script string
	for _, d := range identitydomain.DDLs() {
		if d.CanonicalName == "identity" {
			script = d.Script
		}
	}
	if script == "" {
		t.Fatal("no `identity` DDL script found")
	}
	deriveIdx := strings.Index(script, "def derive_reads(op):")
	executeIdx := strings.Index(script, "\ndef execute(state, op):")
	if deriveIdx < 0 || executeIdx <= deriveIdx {
		t.Fatalf("cannot locate derive_reads in the identity script (derive=%d execute=%d)", deriveIdx, executeIdx)
	}
	derive := script[deriveIdx:executeIdx]
	// Two call sites: the ClaimIdentity/CompleteCredentialLink arm and the
	// ReconcileCredentialBinding arm. Each passes BOTH link positions, so a
	// derivation that silently dropped the actor half would have to delete an
	// argument this assertion also reads.
	if n := strings.Count(derive, "erasure_gate_keys("); n != 2 {
		t.Fatalf("derive_reads calls erasure_gate_keys %d time(s), want 2 (the claim/link arm and the reconcile arm)", n)
	}
	for _, want := range []string{"erasure_gate_keys([target, op.actor])", "erasure_gate_keys([identity_key, credential_actor_key])"} {
		if !strings.Contains(derive, want) {
			t.Fatalf("derive_reads is missing %q — one of the two gated link positions would fall back to a live Core KV read on every call", want)
		}
	}
	// Both of the gate's conditions are hydrated, not just the marker. The
	// piiKey half is what closes the write path for a subject shredded by a
	// bare ShredIdentityKey submit — no marker was ever written for those — so
	// a derivation carrying only the marker would leave the commonest erased
	// population reading as a live dedup incumbent.
	gateIdx := strings.Index(script, "def erasure_gate_keys(identity_keys):")
	if gateIdx < 0 {
		t.Fatal("no erasure_gate_keys helper found in the identity script")
	}
	gate := script[gateIdx:]
	if end := strings.Index(gate, "\ndef "); end > 0 {
		gate = gate[:end]
	}
	for _, want := range []string{`".erasureRequested"`, `".piiKey"`} {
		if !strings.Contains(gate, want) {
			t.Fatalf("erasure_gate_keys does not derive %s — that half of the §6 gate would cost a live Core KV read on every claim, link and reconcile", want)
		}
	}
}

// The second condition's fixtures MUTATE the identity's real piiKey envelope
// rather than writing a fresh one. Every identity created by this harness
// already carries an envelope — its claimKey is a sensitive aspect — and
// replacing it with a hand-built placeholder makes the Vault refuse to decrypt
// that aspect, so the op dies in hydrate and any assertion below the gate
// would pass for the wrong reason.
//
// They also use the RECONCILE path rather than claim. Reconcile names its
// outcome word on the wire (NFR-S6 reclassification does not touch it), and it
// does not decrypt the claimKey — so the refusal it asserts is provably the
// gate's and not the Vault's own shredded-key refusal arriving first.

// mutatePiiKey read-modify-writes the identity's existing piiKey envelope,
// applying a caller-chosen class, shredded flag and tombstone flag. The
// wrappedDEK is preserved: the fixture changes only the facts the gate reads.
func mutatePiiKey(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey, class string, shredded, tombstoned bool) {
	t.Helper()
	key := identityKey + ".piiKey"
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v — the fixture assumes a real envelope already exists (the claimKey is sensitive)", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	doc["class"] = class
	doc["isDeleted"] = tombstoned
	data, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no data map — the envelope shape this gate reads has changed", key)
	}
	if shredded {
		data["shredded"] = true
		data["shreddedAt"] = "2026-08-07T08:59:00Z"
	} else {
		delete(data, "shredded")
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, raw); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
}

// assertPiiKeyUnshredded proves the fixture's starting state, so a test that
// asserts the path stays OPEN cannot pass merely because the envelope was
// missing or already carried the flag.
func assertPiiKeyUnshredded(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) {
	t.Helper()
	key := identityKey + ".piiKey"
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v — this control needs a real, live envelope to be meaningful", key, err)
	}
	var doc struct {
		Class string         `json:"class"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	if doc.Class != "piiKey" {
		t.Fatalf("%s has class %q, want piiKey", key, doc.Class)
	}
	if v, ok := doc.Data["shredded"]; ok && v == true {
		t.Fatalf("%s is already shredded — this control asserts an UNSHREDDED envelope leaves the path open", key)
	}
}

// TestErasureGate_Reconcile_ShreddedKeyClosesWithNoMarker is the gate's second
// condition, and it exists because the marker alone does not cover the
// population that matters most. The marker is written by the erasure PATTERN's
// seal; a bare ShredIdentityKey submit — what the operator Shred button has
// always sent — writes only piiKey.shredded. Gated on the marker alone, every
// one of those subjects reads as a live, unerased identity.
//
// NO marker is seeded. If the gate still consulted only the marker, this
// reconcile would be accepted.
func TestErasureGate_Reconcile_ShreddedKeyClosesWithNoMarker(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-shred-recon")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "ShredRecon")
	dropBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	assertKeyAbsent(t, ctx, conn, uKey+".erasureRequested",
		"the fixture must carry NO marker, or this proves only what the marker half already proved")
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", true, false)

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		reconcileEnv(testutil.GenReqID("ShredReconShr"), consumerActorKey, uKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected — a key-shredded identity with no marker kept its write path open", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "CredentialReconcileRejected: erased") {
		t.Fatalf("rejected with %+v, want the `erased` guard — a rejection from anywhere else means the gate never ran", reply.Error)
	}
	assertKeyAbsent(t, ctx, conn, boundToLinkKey(consumerActorKey, uKey),
		"the gate refused the reconcile but the edge was republished anyway")

	// One fact changed, nothing else. If this does not now succeed, the
	// rejection above belonged to some other guard and proved nothing.
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", false, false)
	testutil.PublishOp(t, conn, reconcileEnv(testutil.GenReqID("ShredReconOpn"), consumerActorKey, uKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
}

// TestErasureGate_UnshreddedPiiKeyDoesNotClose is the false-positive control,
// and it is what makes the second condition safe to ship. Every identity that
// has taken a sensitive write carries a piiKey envelope — in this package,
// every identity at all, since the claimKey is sensitive — so a gate keyed on
// the envelope's PRESENCE rather than its shredded flag would refuse the
// claim, link, reconcile and merge paths of the entire population,
// permanently, with no op able to reopen them.
func TestErasureGate_UnshreddedPiiKeyDoesNotClose(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-live-key")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "LiveKey")
	dropBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	assertPiiKeyUnshredded(t, ctx, conn, uKey)

	testutil.PublishOp(t, conn, reconcileEnv(testutil.GenReqID("LiveKeyRecon"), consumerActorKey, uKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
}

// TestErasureGate_WrongClassAtPiiKeyDoesNotClose — the same class check the
// marker half carries, for the same reason. privacy-base owns the piiKey
// aspect-type DDL, so a document declaring some other class at this key falls
// to resolveGoverningDDL's permissive default and any package script could
// write one. A class-blind gate would let such a write shut a person's write
// path permanently, with nothing able to reopen it.
func TestErasureGate_WrongClassAtPiiKeyDoesNotClose(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-key-class")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "KeyClass")
	dropBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	mutatePiiKey(t, ctx, conn, uKey, "note", true, false)

	testutil.PublishOp(t, conn, reconcileEnv(testutil.GenReqID("KeyClassDo"), consumerActorKey, uKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestErasureGate_TombstonedShreddedKeyStillCloses — destruction does not
// become untrue when the aspect is deleted, and a gate that reopened on a
// tombstone would let the erased set grow again exactly when that was least
// observable. Same posture as the marker half.
func TestErasureGate_TombstonedShreddedKeyStillCloses(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "erase-key-tomb")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "KeyTomb")
	dropBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", true, true)

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		reconcileEnv(testutil.GenReqID("KeyTombShr"), consumerActorKey, uKey))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected — a tombstoned shredded envelope reopened the write path", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "CredentialReconcileRejected: erased") {
		t.Fatalf("rejected with %+v, want the `erased` guard", reply.Error)
	}

	mutatePiiKey(t, ctx, conn, uKey, "piiKey", false, false)
	testutil.PublishOp(t, conn, reconcileEnv(testutil.GenReqID("KeyTombOpen"), consumerActorKey, uKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestErasureGate_CreateUnclaimedIdentity_SkipsBareShreddedIncumbent is the
// vector the second condition was built for. A bare-shredded subject keeps a
// live identityindex and no marker, so an ordinary same-contact walk-in mints
// lnk.identity.<new>.duplicateOf.identity.<shredded> and an identity.created
// carrying matchedIdentityKeys — a brand-new, durable, decrypt-free
// correlation to a person whose key is already destroyed. Nothing exotic
// reaches it: the front desk registering a namesake does.
func TestErasureGate_CreateUnclaimedIdentity_SkipsBareShreddedIncumbent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-shred-dedup")

	const sharedEmail = "shredded.dedup@example.com"
	emailIdxKey := contactIndexKey("email", sharedEmail)

	createContact := func(label, name string) string {
		t.Helper()
		reqID := testutil.GenReqID(label)
		env := &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "CreateUnclaimedIdentity",
			Actor:         staffActorKey,
			SubmittedAt:   "2026-08-07T10:00:00Z",
			Class:         "identity",
			Payload: json.RawMessage(`{"name":"` + name + `","email":"` + sharedEmail +
				`","claimKeyHash":"` + sha256HexOf("claim-"+label) + `"}`),
			ContextHint: &processor.ContextHint{OptionalReads: []string{emailIdxKey}},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
		return identityIDFromRequestID(reqID)
	}

	incumbentID := createContact("ShredDedupInc", "Shred Dedup One")
	incumbentKey := "vtx.identity." + incumbentID

	// Positive vector FIRST: without it the negative below could pass simply
	// because the dedup never matched at all.
	dupID := createContact("ShredDedupDup", "Shred Dedup Two")
	assertKeyPresent(t, ctx, conn, duplicateOfLinkKey(dupID, incumbentKey),
		"an un-shredded incumbent must still be deduped against, or the shredded case below proves nothing")

	// Shred the incumbent's key with NO marker — the bare-submit state — and
	// register the same contact again.
	mutatePiiKey(t, ctx, conn, incumbentKey, "piiKey", true, false)
	assertKeyAbsent(t, ctx, conn, incumbentKey+".erasureRequested",
		"the fixture must carry NO marker, or this proves only what the marker half already proved")
	afterID := createContact("ShredDedupAft", "Shred Dedup Three")

	assertKeyAbsent(t, ctx, conn, duplicateOfLinkKey(afterID, incumbentKey),
		"a fresh duplicateOf named an identity whose PII key is already destroyed — the erased set grew through the path the operator button has always used")
}
