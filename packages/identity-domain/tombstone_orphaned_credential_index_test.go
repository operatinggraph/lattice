// TombstoneOrphanedCredentialIndex (erasure-orchestration-design.md §7) — the
// cleanup verb for a credentialindex vertex a pre-narrowing ShredIdentityKey
// left standing where no boundTo walk can reach it. Driven through the real
// Processor commit path, so what these prove is what production does.
//
// Two of these carry the weight:
//
//   - TestTombstoneOrphanedCredentialIndex_LiveOwner_Rejected is the safety
//     property. This op holds a scope:any grant and issues a document-less
//     tombstone, so nothing downstream of the script re-checks what it names:
//     the NotErased gate is the only thing standing between the verb and a LIVE
//     person's sign-in method. It is asserted as a pair over one corpus — the
//     identical envelope against the identical credential, with only the
//     owner's piiKey changing.
//   - TestTombstoneOrphanedCredentialIndex_LiveLink_Rejected is the narrowness
//     property. A live boundTo link means UnbindIdentityCredentials' ordinary
//     sweep still enumerates the pair and retires the index and the link
//     together; this op running there would leave the link pointing at an index
//     that no longer resolves. Same pair construction, with only the link's
//     tombstone flag changing.
//
// Every accepted case also proves the reply-constraint: the script names
// index_key as response.primaryKey, and a primaryKey outside the committed write
// footprint is rejected BEFORE commit (internal/processor/commit_path.go), so an
// accepted outcome is itself the assertion that the named key is the one written.
package identitydomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	tombOrphanActorID  = "BBtmbrphanHJKMNPQRST"
	tombOrphanActorKey = "vtx.identity." + tombOrphanActorID

	tombOrphanUngrantedActorID  = "BBtmbNoGrantHJKMNPQR"
	tombOrphanUngrantedActorKey = "vtx.identity." + tombOrphanUngrantedActorID

	// A credential that exists only as an index vertex this harness seeds by
	// hand — the shape the OwnerMismatch case needs, where the vertex's own
	// actorKey disagrees with the key that hashes to it.
	tombOrphanBogusCredKey = "vtx.identity.BBtmbBogusCredHJKMNP"
	tombOrphanOtherCredKey = "vtx.identity.BBtmbthrCredHJKMNPQR"

	// Filler credentials for the owner-array vectors. They exist only as
	// entries inside a live owner's credentialBinding array — the rewrite reads
	// the array and never dereferences an entry, so no vertex is needed behind
	// them, and their survival is exactly what proves the filter is a filter and
	// not a truncation.
	tombOrphanCredAKey = "vtx.identity.BBtmbCredAHJKMNPQRST"
	tombOrphanCredBKey = "vtx.identity.BBtmbCredBHJKMNPQRST"
	tombOrphanCredCKey = "vtx.identity.BBtmbCredCHJKMNPQRST"
)

// tombOrphanCapDoc grants TombstoneOrphanedCredentialIndex on the DEFAULT lane
// — the shape the operator running `lattice identity sweep-credential-residue`
// carries, which is the op's only dispatcher.
func tombOrphanCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + tombOrphanActorID,
		Actor:                  tombOrphanActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{tombOrphanActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "TombstoneOrphanedCredentialIndex", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// tombOrphanCapDocMissingGrant is tombOrphanCapDoc's control: the same lane and
// the same operator role, granting a DIFFERENT identity-domain op. Without it a
// denial test would be satisfied by the lane check alone and would stay green
// if this op's grant were deleted outright.
func tombOrphanCapDocMissingGrant() *processor.CapabilityDoc {
	doc := tombOrphanCapDoc()
	doc.Key = "cap.identity." + tombOrphanUngrantedActorID
	doc.Actor = tombOrphanUngrantedActorKey
	doc.ProjectedFromRevisions = map[string]uint64{tombOrphanUngrantedActorKey: 1}
	doc.PlatformPermissions = []processor.PlatformPermission{
		{OperationType: "ReconcileCredentialBinding", Scope: "any"},
	}
	return doc
}

func setupTombOrphanEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := setupTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, tombOrphanCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, tombOrphanCapDocMissingGrant())
	return ctx, conn
}

// tombOrphanBoundToKey mirrors the script's credential_bound_to_key: the
// credential is the source, the owner is the target (Contract #1 §1.1).
func tombOrphanBoundToKey(credentialActorKey, ownerIdentityKey string) string {
	const prefix = "vtx.identity."
	return "lnk.identity." + credentialActorKey[len(prefix):] +
		".boundTo.identity." + ownerIdentityKey[len(prefix):]
}

// tombOrphanEnv builds one TombstoneOrphanedCredentialIndex envelope. The
// declared read-set mirrors what the CLI driver declares
// (cmd/lattice/identity/credential_residue.go): the index vertex fail-closed in
// reads — its absence IS a correctness error for a driver that scanned the key
// to get here — and BOTH endpoints' erasure-discriminator keys plus the boundTo
// link in optionalReads, where absence is a legitimate branch. Both endpoints,
// because the op's erasure gate is symmetric in them: the erased subject is the
// row's owner in the inbound residue shape and the row's credential in the
// outbound one.
//
// The gates hold with no help from this declaration: both read through kv.Read,
// so an undeclared key falls through to a live Core KV GET rather than reading
// absent. Declaring them only buys the step-4 snapshot.
func tombOrphanEnv(t *testing.T, actorKey, credKey, ownerKey, reqID string, extraOptionalReads ...string) *processor.OperationEnvelope {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"credentialActorKey": credKey,
		"identityKey":        ownerKey,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "TombstoneOrphanedCredentialIndex",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-21T12:00:00Z",
		Class:         "tombstoneOrphanedCredentialIndex",
		Payload:       payload,
		AuthContext:   &processor.AuthContext{Target: ownerKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{credentialIndexKey(credKey)},
			OptionalReads: append([]string{
				ownerKey + ".erasureRequested",
				ownerKey + ".piiKey",
				credKey + ".erasureRequested",
				credKey + ".piiKey",
				tombOrphanBoundToKey(credKey, ownerKey),
			}, extraOptionalReads...),
		},
	}
}

// submitTombOrphanDeclaringOwnerArray is the OUTBOUND dispatcher shape: the
// driver classified the owner as live, so it also declares the owner's
// credentialBinding array (cmd/lattice/identity/credential_residue.go's
// optionalReadsFor). Under that declaration the array is served from the step-4
// snapshot and the rewrite carries the snapshot's own revision, which is the
// posture production runs; the bare submitTombOrphan above is the other
// dispatcher shape, where the same read falls through to a live Core KV GET.
//
// The declaration is scoped to this arm and cannot be hoisted into
// tombOrphanEnv: credentialBinding is sensitive, so step 4 decrypts every
// declared instance of it under the named owner's key, and the inbound vectors
// name an owner whose key is destroyed.
func submitTombOrphanDeclaringOwnerArray(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, credKey, ownerKey, reqLabel string,
	wantOutcome processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(reqLabel)
	testutil.PublishOp(t, conn, tombOrphanEnv(t, tombOrphanActorKey, credKey, ownerKey, reqID,
		ownerKey+".credentialBinding"))
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
	return reqID
}

// seedOwnerBindingArray writes a live owner's credentialBinding aspect with the
// given singular fallback pair and array, encrypted exactly as the Processor's
// step-6.5 hook would have. The singular pair is passed separately from the
// array because the two disagree in the real corpus — a pre-array record has
// only the pair, and the promote-or-omit branch turns on whether the pair names
// the credential being removed.
func seedOwnerBindingArray(t *testing.T, ctx context.Context, conn *substrate.Conn,
	ownerKey, singularActor, singularBoundAt string, credentials []map[string]any) {
	t.Helper()
	data := map[string]any{}
	if singularActor != "" {
		data["actorKey"] = singularActor
		data["boundAt"] = singularBoundAt
	}
	if credentials != nil {
		entries := make([]any, 0, len(credentials))
		for _, c := range credentials {
			entries = append(entries, c)
		}
		data["credentials"] = entries
	}
	seedSensitiveAspect(t, ctx, conn, ownerKey, "credentialBinding", data)
}

// bindingEntry is the array element shape (ddls.go's credentialBinding).
func bindingEntry(actorKey, boundAt string) map[string]any {
	return map[string]any{"actorKey": actorKey, "boundAt": boundAt}
}

// ownerArrayActorKeys returns the owner's credentials array as the ordered list
// of actor keys it names. Order is asserted, not just membership: the array is
// a rewrite of an existing list, and a filter that reorders it is a different
// operation from the one this op claims to perform.
func ownerArrayActorKeys(t *testing.T, ctx context.Context, conn *substrate.Conn, ownerKey string) []string {
	t.Helper()
	data := readDecryptedAspectData(t, ctx, conn, ownerKey, "credentialBinding")
	raw, ok := data["credentials"].([]any)
	if !ok {
		t.Fatalf("%s.credentialBinding has no credentials array: %+v", ownerKey, data)
	}
	out := make([]string, 0, len(raw))
	for i, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("%s.credentialBinding.credentials[%d] is not an object: %+v", ownerKey, i, e)
		}
		k, _ := m["actorKey"].(string)
		out = append(out, k)
	}
	return out
}

// aspectRevision is the only readable evidence for an aspect the test cannot
// decrypt — an erased owner's array, whose key is destroyed. An unchanged
// revision is the assertion that nothing wrote it; reading its body is not an
// option, which is the whole reason the inbound arm declines to rewrite it.
func aspectRevision(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) uint64 {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	return entry.Revision
}

// submitTombOrphanAs publishes one envelope and drives it to wantOutcome.
func submitTombOrphanAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, actorKey, credKey, ownerKey, reqLabel string,
	wantOutcome processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(reqLabel)
	testutil.PublishOp(t, conn, tombOrphanEnv(t, actorKey, credKey, ownerKey, reqID))
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
	return reqID
}

func submitTombOrphan(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, credKey, ownerKey, reqLabel string,
	wantOutcome processor.MessageOutcome) string {
	t.Helper()
	return submitTombOrphanAs(t, ctx, conn, cp, cons, tombOrphanActorKey, credKey, ownerKey, reqLabel, wantOutcome)
}

// rejectTombOrphanAs pins WHICH refusal fired, not merely that one did. `want`
// is the word the script's own fail() leads with, which reaches the reply inside
// Error.Message (internal/processor/commit_path.go's classifyStepError wraps a
// script fail under the wire code ScriptFailed and carries the script's text
// through). Mirrors credential_reconcile_test.go's assertReconcileRejected.
//
// Without this a rejection test passes on ANY of the op's five refusals, so a
// test aimed at one guard would stay green with that guard deleted as long as
// some earlier one happened to catch the fixture.
func rejectTombOrphanAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, actorKey, credKey, ownerKey, reqLabel, want string) {
	t.Helper()
	env := tombOrphanEnv(t, actorKey, credKey, ownerKey, testutil.GenReqID(reqLabel))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("%s: outcome = %q, want rejected", reqLabel, outcome)
	}
	if reply.Error == nil {
		t.Fatalf("%s: rejected with no error detail; want %s", reqLabel, want)
	}
	if !strings.Contains(reply.Error.Message, want) {
		t.Fatalf("%s: rejected with %q, want the %s guard — a rejection from anywhere else means the guard under test never ran",
			reqLabel, reply.Error.Message, want)
	}
}

func rejectTombOrphan(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, credKey, ownerKey, reqLabel, want string) {
	t.Helper()
	rejectTombOrphanAs(t, ctx, conn, cp, cons, tombOrphanActorKey, credKey, ownerKey, reqLabel, want)
}

// retractBoundToLink reproduces what a pre-narrowing ShredIdentityKey did: the
// boundTo link is tombstoned and the credential's index vertex is deliberately
// left standing. Written straight to Core KV rather than by submitting the
// shred — that op lives in privacy-base, which this package's harness does not
// install, and what the residue shape consists of is exactly these two facts.
func retractBoundToLink(t *testing.T, ctx context.Context, conn *substrate.Conn, credKey, ownerKey string) {
	t.Helper()
	key := tombOrphanBoundToKey(credKey, ownerKey)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v — the fixture assumes a real boundTo link the claim already wrote", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	doc["isDeleted"] = true
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, raw); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
}

// seedCredentialIndexAs writes a credentialindex vertex at the hash of hashOf
// while recording recordedActor/recordedOwner in its body — so the two can be
// made to DISAGREE, which is the whole of the OwnerMismatch case.
func seedCredentialIndexAs(t *testing.T, ctx context.Context, conn *substrate.Conn, hashOf, recordedActor, recordedOwner string) {
	t.Helper()
	key := credentialIndexKey(hashOf)
	raw, err := json.Marshal(map[string]any{
		"class":     "credentialindex",
		"isDeleted": false,
		"data": map[string]any{
			"actorKey":    recordedActor,
			"identityKey": recordedOwner,
			"boundAt":     "2026-05-01T00:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("marshal credentialindex: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, raw); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

// TestTombstoneOrphanedCredentialIndex_BareShredResidue_Tombstoned is the
// historical shape, and the one the whole fire exists for: the owner was erased
// by a BARE ShredIdentityKey submit — piiKey.shredded and no marker at all,
// which is what the operator Shred button has always sent — and the
// pre-narrowing cascade tombstoned the boundTo link while leaving the
// credential's plaintext index vertex live.
//
// UnbindIdentityCredentials cannot reach this: its sweep enumerates boundTo
// links, both directions are already tombstoned, and its hit-gated arms emit no
// mutations at all. So the subject earns a violating=false attestation over a
// live sha256(sign-in id) → erased-person row. This is the op that clears it.
func TestTombstoneOrphanedCredentialIndex_BareShredResidue_Tombstoned(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-bare")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbBare")

	// A bystander pair, erased and orphaned exactly like the subject. This op
	// names ONE index vertex per submit and issues a document-less tombstone, so
	// a derivation that took the wrong key would land silently; this is what
	// makes "it retired the one it was asked for" mean something.
	bystanderOwner, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbBareBy"))
	seedCredentialIndexAs(t, ctx, conn, tombOrphanOtherCredKey, tombOrphanOtherCredKey, bystanderOwner)
	mutatePiiKey(t, ctx, conn, bystanderOwner, "piiKey", true, false)

	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"the fixture is the pre-narrowing residue: the link is retracted and the index is deliberately still live")
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", true, false)

	reqID := submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbBareDo", processor.OutcomeAccepted)

	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"an erased person's plaintext credential row must stop resolving")
	assertTrackerEvent(t, ctx, conn, reqID, "identity.unbound")
	assertDocLive(t, ctx, conn, credentialIndexKey(tombOrphanOtherCredKey),
		"a bystander's index vertex was named by nothing in this submit and must be untouched")
	assertDocLive(t, ctx, conn, uKey,
		"the inbound arm writes exactly one key — the index vertex — and must not have touched the erased owner's own vertex")
}

// TestTombstoneOrphanedCredentialIndex_SealedOwner_Tombstoned covers the
// full-pattern erasure shape for completeness: the owner carries a live
// erasureRequested marker rather than a shredded key. UnbindIdentityCredentials
// would normally have swept this subject already, so the residue is not expected
// here — but the gate is a disjunction and both arms must arm the verb, or a
// subject sealed-then-orphaned by any other route would be unreachable.
func TestTombstoneOrphanedCredentialIndex_SealedOwner_Tombstoned(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-sealed")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbSeal")
	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)

	// No piiKey shred at all — the marker is the only erasure fact present, so
	// only the marker arm can be what accepts this.
	assertPiiKeyUnshredded(t, ctx, conn, uKey)
	sealForErasure(t, ctx, conn, uKey)

	reqID := submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbSealDo", processor.OutcomeAccepted)
	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"a live erasureRequested marker closes the write path on its own")
	assertTrackerEvent(t, ctx, conn, reqID, "identity.unbound")
}

// TestTombstoneOrphanedCredentialIndex_OutboundResidue_Tombstoned is the OTHER
// half of the residue class, and the half a discriminator gated on identityKey
// alone silently skips.
//
// The pre-narrowing shred tombstoned boundTo in BOTH directions (54b3c8c7^'s
// collect_bound_to_links: "in" + "out"), so an erased subject leaves an orphaned
// index behind in two shapes. This is the OUTBOUND one: the erased subject is
// the link's SOURCE — itself a credential of somebody else — so the surviving
// row lives at the hash of the ERASED subject's own key and reads
// {actorKey: erased, identityKey: live owner}. That is a merged-away identity
// folded into its survivor as an implicit self-credential, or a Scenario-B
// identity later linked to another.
//
// The leak is identical: a live plaintext row, keyed by a derivative of the
// destroyed identity, naming the erased person and answering "who are they now"
// with no decrypt. The owner here is deliberately a LIVE, unerased person and is
// asserted so — the acceptance can only come from the credential arm of the
// gate.
//
// And because that owner is live, retiring the index is only half of it: their
// credentialBinding array — the sign-in-methods list the account page renders —
// still names the erased credential, and an entry there resolves to nothing and
// belongs to nobody. The batch rewrites it, so this asserts BOTH keys: the index
// stops resolving, and the owner's array stops naming the erased person while
// their own surviving credential is left exactly where it was.
func TestTombstoneOrphanedCredentialIndex_OutboundResidue_Tombstoned(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-outbound")

	// The live owner the erased subject is a credential OF.
	liveOwner, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbOutO"))
	assertPiiKeyUnshredded(t, ctx, conn, liveOwner)

	// The erased subject, standing here as the CREDENTIAL.
	erasedCred, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbOutC"))
	mutatePiiKey(t, ctx, conn, erasedCred, "piiKey", true, false)

	// The owner's sign-in methods: their own credential, plus the erased one.
	// The singular fallback pair names the OWN credential, so this vector
	// exercises the filter without the promotion branch.
	seedOwnerBindingArray(t, ctx, conn, liveOwner, tombOrphanCredAKey, "2026-05-01T00:00:00Z",
		[]map[string]any{
			bindingEntry(tombOrphanCredAKey, "2026-05-01T00:00:00Z"),
			bindingEntry(erasedCred, "2026-05-02T00:00:00Z"),
		})

	// The residue itself: the index at the hash of the erased subject's own key,
	// recording them as the live owner's credential. No boundTo link exists —
	// the pre-narrowing shred's outbound arm tombstoned it, and a hard-removed
	// link reads absent, which the op treats as the same answer.
	seedCredentialIndexAs(t, ctx, conn, erasedCred, erasedCred, liveOwner)

	reqID := submitTombOrphanDeclaringOwnerArray(t, ctx, conn, cp, cons, erasedCred, liveOwner, "TmbOutDo", processor.OutcomeAccepted)

	assertTombstoned(t, ctx, conn, credentialIndexKey(erasedCred),
		"the outbound residue names the erased person in the clear exactly as the inbound shape does, and must stop resolving too")
	assertTrackerEvent(t, ctx, conn, reqID, "identity.unbound")
	assertDocLive(t, ctx, conn, liveOwner,
		"the owner's own identity vertex is not this op's to touch — only their credentialBinding aspect is")
	assertDocLive(t, ctx, conn, liveOwner+".credentialBinding",
		"the array is rewritten, never retired: the owner still has sign-in methods")

	got := ownerArrayActorKeys(t, ctx, conn, liveOwner)
	want := []string{tombOrphanCredAKey}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("live owner's credentials = %v, want %v — the erased credential must be gone from the "+
			"owner's sign-in methods and their own must survive", got, want)
	}
	after := readDecryptedAspectData(t, ctx, conn, liveOwner, "credentialBinding")
	if singular, _ := after["actorKey"].(string); singular != tombOrphanCredAKey {
		t.Fatalf("live owner's singular actorKey = %q, want %q untouched — the removed credential was not the "+
			"one the pre-array fallback named, so nothing about it should have moved", singular, tombOrphanCredAKey)
	}
}

// TestTombstoneOrphanedCredentialIndex_OutboundResidue_KeepsTheOtherCredentials
// is the filter's own control. An owner with several sign-in methods must lose
// exactly the erased one, and the survivors must come back in the order they
// were in: this is a rewrite of an existing list, and a body that truncated,
// reordered, or kept only the head would satisfy a membership-only assertion
// while silently destroying the rest of a live person's account access.
//
// The erased credential sits in the MIDDLE deliberately — a filter that dropped
// the first or last element would pass a two-element fixture by luck.
func TestTombstoneOrphanedCredentialIndex_OutboundResidue_KeepsTheOtherCredentials(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-outbound-multi")

	liveOwner, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbMultO"))
	assertPiiKeyUnshredded(t, ctx, conn, liveOwner)
	erasedCred, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbMultC"))
	mutatePiiKey(t, ctx, conn, erasedCred, "piiKey", true, false)

	seedOwnerBindingArray(t, ctx, conn, liveOwner, tombOrphanCredAKey, "2026-05-01T00:00:00Z",
		[]map[string]any{
			bindingEntry(tombOrphanCredAKey, "2026-05-01T00:00:00Z"),
			bindingEntry(tombOrphanCredBKey, "2026-05-02T00:00:00Z"),
			bindingEntry(erasedCred, "2026-05-03T00:00:00Z"),
			bindingEntry(tombOrphanCredCKey, "2026-05-04T00:00:00Z"),
		})
	seedCredentialIndexAs(t, ctx, conn, erasedCred, erasedCred, liveOwner)

	submitTombOrphanDeclaringOwnerArray(t, ctx, conn, cp, cons, erasedCred, liveOwner, "TmbMultDo", processor.OutcomeAccepted)

	assertTombstoned(t, ctx, conn, credentialIndexKey(erasedCred),
		"the residue row is retired on this vector exactly as on the two-credential one")

	got := ownerArrayActorKeys(t, ctx, conn, liveOwner)
	want := []string{tombOrphanCredAKey, tombOrphanCredBKey, tombOrphanCredCKey}
	if len(got) != len(want) {
		t.Fatalf("live owner's credentials = %v, want %v — one entry removed, three kept", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("live owner's credentials = %v, want %v (differs at index %d) — the survivors must keep "+
				"their order; a reordering rewrite is a different operation from a filter", got, want, i)
		}
	}
	after := readDecryptedAspectData(t, ctx, conn, liveOwner, "credentialBinding")
	for _, field := range []string{"actorKey", "boundAt"} {
		if _, present := after[field]; !present {
			t.Fatalf("live owner's singular %s went missing — the removed credential was not the one it named, "+
				"so the pre-array fallback must be left exactly as it was", field)
		}
	}
}

// TestTombstoneOrphanedCredentialIndex_OutboundResidue_PromotesSingularHolder
// covers the branch the array alone does not reach. The credentialBinding aspect
// carries a pre-array singular actorKey/boundAt pair that readers still fall
// back to, and when the credential being removed IS that pair, leaving it in
// place would keep the fallback pointing at a sign-in method that no longer
// resolves and belongs to an erased person.
//
// The promotion target is the first survivor, and its boundAt must travel with
// it: a promoted actorKey carrying the erased credential's timestamp would say
// the survivor was bound on a day it was not.
func TestTombstoneOrphanedCredentialIndex_OutboundResidue_PromotesSingularHolder(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-outbound-promote")

	liveOwner, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbPromO"))
	assertPiiKeyUnshredded(t, ctx, conn, liveOwner)
	erasedCred, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbPromC"))
	mutatePiiKey(t, ctx, conn, erasedCred, "piiKey", true, false)

	// The singular pair names the ERASED credential — it was the owner's first.
	seedOwnerBindingArray(t, ctx, conn, liveOwner, erasedCred, "2026-05-01T00:00:00Z",
		[]map[string]any{
			bindingEntry(erasedCred, "2026-05-01T00:00:00Z"),
			bindingEntry(tombOrphanCredBKey, "2026-05-02T00:00:00Z"),
		})
	seedCredentialIndexAs(t, ctx, conn, erasedCred, erasedCred, liveOwner)

	submitTombOrphanDeclaringOwnerArray(t, ctx, conn, cp, cons, erasedCred, liveOwner, "TmbPromDo", processor.OutcomeAccepted)

	assertTombstoned(t, ctx, conn, credentialIndexKey(erasedCred),
		"the residue row is retired on this vector too")

	after := readDecryptedAspectData(t, ctx, conn, liveOwner, "credentialBinding")
	if singular, _ := after["actorKey"].(string); singular != tombOrphanCredBKey {
		t.Fatalf("live owner's singular actorKey = %q, want the promoted survivor %q — a fallback still naming "+
			"the erased credential resolves to nothing", singular, tombOrphanCredBKey)
	}
	if boundAt, _ := after["boundAt"].(string); boundAt != "2026-05-02T00:00:00Z" {
		t.Fatalf("live owner's singular boundAt = %q, want the promoted survivor's own %q — the timestamp must "+
			"travel with the key it describes", boundAt, "2026-05-02T00:00:00Z")
	}
	got := ownerArrayActorKeys(t, ctx, conn, liveOwner)
	if len(got) != 1 || got[0] != tombOrphanCredBKey {
		t.Fatalf("live owner's credentials = %v, want [%s]", got, tombOrphanCredBKey)
	}
}

// TestTombstoneOrphanedCredentialIndex_OutboundResidue_OmitsSingularWhenNoneRemain
// is the promotion branch with nothing to promote to, and the assertion is about
// what is NOT written.
//
// The removed credential was both the array's only entry and the singular
// fallback. Writing null there would be worse than leaving it: the aspect's
// schema types actorKey as a string, and every fallback reader tests the field's
// PRESENCE, so a null passes the test and hands them a non-key. Both fields are
// omitted instead.
//
// This shape is reachable because this op has no last-credential guard, by
// design — a credential belonging to an erased person must stop authenticating
// whether or not it was the owner's last, exactly as UnbindIdentityCredentials
// treats the same case.
func TestTombstoneOrphanedCredentialIndex_OutboundResidue_OmitsSingularWhenNoneRemain(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-outbound-omit")

	liveOwner, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbOmitO"))
	assertPiiKeyUnshredded(t, ctx, conn, liveOwner)
	erasedCred, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbOmitC"))
	mutatePiiKey(t, ctx, conn, erasedCred, "piiKey", true, false)

	seedOwnerBindingArray(t, ctx, conn, liveOwner, erasedCred, "2026-05-01T00:00:00Z",
		[]map[string]any{bindingEntry(erasedCred, "2026-05-01T00:00:00Z")})
	seedCredentialIndexAs(t, ctx, conn, erasedCred, erasedCred, liveOwner)

	submitTombOrphanDeclaringOwnerArray(t, ctx, conn, cp, cons, erasedCred, liveOwner, "TmbOmitDo", processor.OutcomeAccepted)

	assertTombstoned(t, ctx, conn, credentialIndexKey(erasedCred), "the residue row is retired")

	after := readDecryptedAspectData(t, ctx, conn, liveOwner, "credentialBinding")
	for _, field := range []string{"actorKey", "boundAt"} {
		v, present := after[field]
		if present {
			t.Fatalf("live owner's singular %s is present as %#v with nothing left to promote — it must be "+
				"OMITTED, and a null in particular would satisfy every presence-testing fallback reader "+
				"while handing them a non-key", field, v)
		}
	}
	if got := ownerArrayActorKeys(t, ctx, conn, liveOwner); len(got) != 0 {
		t.Fatalf("live owner's credentials = %v, want empty", got)
	}
}

// TestTombstoneOrphanedCredentialIndex_OutboundResidue_ArrayAlreadyClear is the
// rewrite arm's idempotence, and it is separate from the op's own
// CredentialIndexAlreadyClear refusal: here the INDEX is genuinely residue and
// must be retired, while the owner's array already fails to name the erased
// credential — an UnlinkCredential got there first, or the row was residue the
// array never named.
//
// The array must then not be written at all. Re-emitting an unchanged body would
// burn a mutation, bump the aspect's revision on every pass, and — because the
// rewrite is revision-pinned — turn a harmless re-run into a source of conflicts
// for every other writer of that array.
func TestTombstoneOrphanedCredentialIndex_OutboundResidue_ArrayAlreadyClear(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-outbound-idem")

	liveOwner, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbClrO"))
	assertPiiKeyUnshredded(t, ctx, conn, liveOwner)
	erasedCred, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbClrC"))
	mutatePiiKey(t, ctx, conn, erasedCred, "piiKey", true, false)

	// The owner's array names their own credentials and nothing else.
	seedOwnerBindingArray(t, ctx, conn, liveOwner, tombOrphanCredAKey, "2026-05-01T00:00:00Z",
		[]map[string]any{
			bindingEntry(tombOrphanCredAKey, "2026-05-01T00:00:00Z"),
			bindingEntry(tombOrphanCredBKey, "2026-05-02T00:00:00Z"),
		})
	before := aspectRevision(t, ctx, conn, liveOwner+".credentialBinding")

	seedCredentialIndexAs(t, ctx, conn, erasedCred, erasedCred, liveOwner)
	submitTombOrphanDeclaringOwnerArray(t, ctx, conn, cp, cons, erasedCred, liveOwner, "TmbClrDo", processor.OutcomeAccepted)

	assertTombstoned(t, ctx, conn, credentialIndexKey(erasedCred),
		"the index is residue whatever the owner's array says, and retiring it is unconditional on this arm")
	if after := aspectRevision(t, ctx, conn, liveOwner+".credentialBinding"); after != before {
		t.Fatalf("the owner's array was rewritten (revision %d -> %d) with nothing to remove from it; "+
			"an unchanged array must produce no mutation at all", before, after)
	}
	got := ownerArrayActorKeys(t, ctx, conn, liveOwner)
	want := []string{tombOrphanCredAKey, tombOrphanCredBKey}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("live owner's credentials = %v, want %v untouched", got, want)
	}
}

// TestTombstoneOrphanedCredentialIndex_InboundResidue_OwnerArrayUntouched is the
// OPPOSITE guarantee, and the reason the rewrite is gated on the owner rather
// than applied wherever an array exists.
//
// Here the erased subject is the OWNER. Their credentialBinding array is
// sensitive and their PII key is destroyed, so it is already erased by key
// destruction — there is nothing to tidy and nothing that could read it. A
// rewrite arm that fired on "the array exists" would try to decrypt it, and the
// Vault answers ErrKeyShredded: the operation would fail outright rather than
// retire the index, taking down the exact population this op was built for.
//
// The assertion is the aspect's REVISION, because its body is unreadable by
// construction. That is the honest evidence available for a key nobody can open.
func TestTombstoneOrphanedCredentialIndex_InboundResidue_OwnerArrayUntouched(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-inbound-array")

	// A claimed identity has a real credentialBinding array naming the claiming
	// actor, written by ClaimIdentity through the real encrypt hook.
	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbInArr")
	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	before := aspectRevision(t, ctx, conn, uKey+".credentialBinding")
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", true, false)

	submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbInArrDo", processor.OutcomeAccepted)

	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"the inbound residue is retired exactly as before — the owner's array plays no part in it")
	if after := aspectRevision(t, ctx, conn, uKey+".credentialBinding"); after != before {
		t.Fatalf("the ERASED owner's credentialBinding was written (revision %d -> %d); an erased person's "+
			"sensitive aspect is theirs to lose to key destruction, and this op must not write to it", before, after)
	}
}

// TestTombstoneOrphanedCredentialIndex_MarkerClosedOwner_ArrayUntouched is the
// case that separates the arm discriminator from the precedent's.
//
// UnbindIdentityCredentials guards its own rewrite on the owner's piiKey being
// shredded, which is the only closure it can meet. This op's gate is wider: an
// owner can also be closed by a live erasureRequested marker with their key
// still intact — sealed for erasure, array still perfectly readable and
// writable. A rewrite guarded on shredded-ness alone would fire there.
//
// It must not. That owner is not a third party being tidied up after; they are a
// subject mid-erasure, and this op holds a scope:any operator grant, so writing
// a fresh sensitive aspect on them would drive a write straight through the gate
// whose entire purpose is to refuse one. The array is decryptable here, so this
// asserts the body and not merely the revision.
func TestTombstoneOrphanedCredentialIndex_MarkerClosedOwner_ArrayUntouched(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-marker-array")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbMkArr")
	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)

	// The marker alone — the key is intact and asserted so, which is what makes
	// this vector different from the shredded one and what makes a rewrite here
	// mechanically possible rather than merely wrong.
	assertPiiKeyUnshredded(t, ctx, conn, uKey)
	sealForErasure(t, ctx, conn, uKey)

	before := aspectRevision(t, ctx, conn, uKey+".credentialBinding")
	submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbMkArrDo", processor.OutcomeAccepted)

	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"a live erasureRequested marker closes the write path on its own, so the index is still retired")
	if after := aspectRevision(t, ctx, conn, uKey+".credentialBinding"); after != before {
		t.Fatalf("a marker-sealed owner's credentialBinding was written (revision %d -> %d); the erasure "+
			"write-path gate refuses a fresh erasable representation of a sealed subject, and this op may "+
			"not be the one verb that walks through it", before, after)
	}
	if got := ownerArrayActorKeys(t, ctx, conn, uKey); len(got) != 1 || got[0] != consumerActorKey {
		t.Fatalf("sealed owner's credentials = %v, want [%s] untouched", got, consumerActorKey)
	}
}

// TestTombstoneOrphanedCredentialIndex_OutboundResidue_ConcurrentOwnerWriteConflicts
// is the shared-vertex property, and it is the one thing the precedent this
// rewrite copies does not have.
//
// The owner's credentialBinding array has three other writers —
// CompleteCredentialLink appends, UnlinkCredential removes, MergeIdentity unions
// — and none of them is excluded by anything this op checks: they act on the
// LIVE owner, whose write path is open by construction on the outbound arm. The
// script reads the array, filters it, and hands step 8 a whole replacement body,
// so a writer landing between the read and the commit has its entry silently
// erased by a body that never saw it. A credential the person just linked would
// simply cease to exist, with nothing left to notice.
//
// The op is submitted WITHOUT declaring the array, which is the shape where the
// hazard is real: an undeclared key is not in the step-4 snapshot, so
// commit_path.go's applyHydratedRevisions supplies no condition, and step 8's own
// prior-document read happens AFTER the script decided — it would read the
// racing writer's revision and condition on it, satisfying the CAS and
// committing the clobber. The script's own expectedRevision, taken from the read
// it actually filtered, is the only thing that turns that into a conflict.
//
// The competing write is a bare Core KV Put rather than a real
// CompleteCredentialLink: what the CAS compares is a revision, so any writer of
// that key reproduces it, and driving a second op through a commit path already
// mid-commit is not something the harness can do.
func TestTombstoneOrphanedCredentialIndex_OutboundResidue_ConcurrentOwnerWriteConflicts(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)

	// The competing writer, firing once, inside the window: it appends a third
	// credential the way a CompleteCredentialLink would. The owner and credential
	// keys are not known until the fixture ops run, so the hook closes over
	// pointers the setup below fills in.
	var liveOwner, erasedCred string
	raced := make(chan struct{})
	cp, cons := newRacingPipeline(t, ctx, conn, racingPipelineConfig{
		Durable:       "tomborphan-race",
		FilterSubject: "ops.default",
		OperationType: "TombstoneOrphanedCredentialIndex",
	}, func() {
		seedOwnerBindingArray(t, ctx, conn, liveOwner, tombOrphanCredAKey, "2026-05-01T00:00:00Z",
			[]map[string]any{
				bindingEntry(tombOrphanCredAKey, "2026-05-01T00:00:00Z"),
				bindingEntry(erasedCred, "2026-05-02T00:00:00Z"),
				bindingEntry(tombOrphanCredCKey, "2026-05-03T00:00:00Z"),
			})
		close(raced)
	})

	liveOwner, _ = createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbRaceO"))
	assertPiiKeyUnshredded(t, ctx, conn, liveOwner)
	erasedCred, _ = createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbRaceC"))
	mutatePiiKey(t, ctx, conn, erasedCred, "piiKey", true, false)

	seedOwnerBindingArray(t, ctx, conn, liveOwner, tombOrphanCredAKey, "2026-05-01T00:00:00Z",
		[]map[string]any{
			bindingEntry(tombOrphanCredAKey, "2026-05-01T00:00:00Z"),
			bindingEntry(erasedCred, "2026-05-02T00:00:00Z"),
		})
	seedCredentialIndexAs(t, ctx, conn, erasedCred, erasedCred, liveOwner)

	env := tombOrphanEnv(t, tombOrphanActorKey, erasedCred, liveOwner, testutil.GenReqID("TmbRaceDo"))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	<-raced

	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected — a write landing on the owner's array between this op's read "+
			"and its commit must conflict the batch, not be overwritten by a body that never saw it", outcome)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeRevisionConflict {
		t.Fatalf("rejected with %+v, want %s — the rewrite must carry the revision it read, so the CAS is "+
			"against the state the filter was computed from and not against whatever step 8 finds later",
			reply.Error, processor.ErrCodeRevisionConflict)
	}

	// The racing writer's entry survived, and so did the index: a conflicted
	// batch commits nothing, which is what makes the operator's re-run correct.
	got := ownerArrayActorKeys(t, ctx, conn, liveOwner)
	want := []string{tombOrphanCredAKey, erasedCred, tombOrphanCredCKey}
	if len(got) != len(want) {
		t.Fatalf("live owner's credentials = %v, want %v — the conflicted batch must have written nothing", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("live owner's credentials = %v, want %v (differs at index %d)", got, want, i)
		}
	}
	assertDocLive(t, ctx, conn, credentialIndexKey(erasedCred),
		"the whole batch is atomic: a conflict on the owner's array must leave the index standing too, so the "+
			"operator's re-run meets a residue that has not moved")
}

// TestTombstoneOrphanedCredentialIndex_SelfLoop_Rejected pins the script's OWN
// self-loop guard through the real commit path.
//
// The CLI driver skips this shape before submitting, which is why it exists at
// all — a merge writes an index vertex for the primary's own implicit
// self-credential, so the shape is on any merged corpus. But the driver's skip
// is an optimization, not the guard: any other caller holding the scope:any
// grant reaches the op directly. Here the subject is genuinely erased and the
// row genuinely self-referential, so nothing but the self-loop refusal can be
// what stops it.
func TestTombstoneOrphanedCredentialIndex_SelfLoop_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-selfloop")

	erased, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbSelfE"))
	mutatePiiKey(t, ctx, conn, erased, "piiKey", true, false)
	seedCredentialIndexAs(t, ctx, conn, erased, erased, erased)

	rejectTombOrphan(t, ctx, conn, cp, cons, erased, erased, "TmbSelfNo", "SelfLoop")
	assertDocLive(t, ctx, conn, credentialIndexKey(erased),
		"a merge's implicit self-credential is not this op's residue class and must be refused, not retired")

	// The positive vector over the same corpus, changing exactly one fact: the
	// row names a DIFFERENT credential, so the payload is no longer a self-loop.
	// Without it the refusal above could be any of the op's other guards.
	seedCredentialIndexAs(t, ctx, conn, tombOrphanOtherCredKey, tombOrphanOtherCredKey, erased)
	submitTombOrphan(t, ctx, conn, cp, cons, tombOrphanOtherCredKey, erased, "TmbSelfYes", processor.OutcomeAccepted)
	assertTombstoned(t, ctx, conn, credentialIndexKey(tombOrphanOtherCredKey),
		"the same erased owner, named by a row that is not self-referential, is the accepted shape")
}

// TestTombstoneOrphanedCredentialIndex_ForeignPiiKeyClass_Rejected is the
// envelope arm's false-positive control, and the symmetric twin of
// _ForeignMarkerClass_Rejected.
//
// key_shredded_closes_write_path checks the piiKey envelope's CLASS for exactly
// the reason the marker helper checks the marker's: privacy-base's aspect-type
// DDL gates the class rather than the key, so a document declaring some other
// class at the same key falls to resolveGoverningDDL's permissive default and
// any package script could write one. Both guards are structurally identical and
// both unlock a tombstone rather than block a write, so both need the control.
func TestTombstoneOrphanedCredentialIndex_ForeignPiiKeyClass_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-piiclass")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbPiiCls")
	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)

	// shredded=true, but under a FOREIGN class — the write any package script
	// could make at this key. A presence-and-flag check would read it as a real
	// key destruction and arm the verb against a person nobody erased.
	mutatePiiKey(t, ctx, conn, uKey, "someOtherClass", true, false)
	rejectTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbPiiClsNo", "NotErased")
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"a foreign class at the piiKey key must not read as a destroyed key")

	// The positive vector: the same key, the same flag, the real class.
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", true, false)
	submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbPiiClsYes", processor.OutcomeAccepted)
	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"the real class on the same key arms the verb")
}

// TestTombstoneOrphanedCredentialIndex_LiveOwner_Rejected is the safety
// property, and the reason this op's grant is defensible at all.
//
// The owner is a live, unerased person whose credential's boundTo link has gone
// missing — the shape ReconcileCredentialBinding exists to REPAIR. This op must
// refuse it outright: with a scope:any grant and a document-less tombstone, the
// NotErased gate is the only thing between the verb and a live person's sign-in
// method. Paired with its own positive vector over the same corpus so the
// rejection cannot be satisfied by one of the op's other four refusals.
//
// The gate is symmetric in the two endpoints, so this fixture needs BOTH live:
// the owner is the freshly-claimed identity, unshredded and asserted so, and the
// credential is the harness's raw consumer actor, which carries neither an
// erasureRequested marker nor a piiKey envelope of any kind. A refusal here is
// therefore about the row, not about one arm of the disjunction.
func TestTombstoneOrphanedCredentialIndex_LiveOwner_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-live")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbLive")
	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	assertPiiKeyUnshredded(t, ctx, conn, uKey)

	rejectTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbLiveNo", "NotErased")
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"a live person's credential index must survive: this op reaches only residue an erasure left behind")

	// Change exactly one fact.
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", true, false)
	submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbLiveYes", processor.OutcomeAccepted)
	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"the same envelope against the same corpus, with only the owner's erasure changing")
}

// TestTombstoneOrphanedCredentialIndex_ForeignMarkerClass_Rejected — the
// marker's CLASS is what arms this half of the gate, not its key. privacy-base's
// aspect-type DDL gates the class rather than the key, so any package script can
// write some other class there; a presence-only check would hand an operator the
// right to retire a live person's credential by way of a foreign write.
func TestTombstoneOrphanedCredentialIndex_ForeignMarkerClass_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-class")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbCls")
	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	assertPiiKeyUnshredded(t, ctx, conn, uKey)

	sealForErasureAs(t, ctx, conn, uKey, "someOtherClass", false)
	rejectTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbClsNo", "NotErased")
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"a foreign-class marker must not arm this verb")

	// The positive vector: the same key, the real class.
	sealForErasureAs(t, ctx, conn, uKey, "erasureRequested", false)
	submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbClsYes", processor.OutcomeAccepted)
	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"the real class on the same key arms the verb")
}

// TestTombstoneOrphanedCredentialIndex_LiveLink_Rejected is the narrowness
// property. A live boundTo link means UnbindIdentityCredentials' ordinary sweep
// still enumerates this pair and retires the index and the link together, in one
// batch, with the owner's array rewrite where that applies. Running here instead
// would leave the link standing and pointing at an index that no longer
// resolves — the diverged shape reconcile-bindings already refuses to repair,
// manufactured by the repair tool itself.
func TestTombstoneOrphanedCredentialIndex_LiveLink_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-bound")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbBound")
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", true, false)

	// The link the claim wrote is still LIVE — everything else is the accepted
	// shape, so only StillBound can be what refuses.
	assertDocLive(t, ctx, conn, tombOrphanBoundToKey(consumerActorKey, uKey),
		"the fixture needs a live link for this refusal to be about the link")
	rejectTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbBoundNo", "StillBound")
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"a still-bound credential belongs to the ordinary sweep, which retires the index and the link together")

	// Change exactly one fact.
	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbBoundYes", processor.OutcomeAccepted)
	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"with the link retracted the pair is genuinely orphaned and this op is the only path to it")
}

// TestTombstoneOrphanedCredentialIndex_OwnerMismatch_Rejected pins
// author-declares-intent, and the first half is a live security vector rather
// than a hygiene check: the payload names an ERASED third party as the owner of
// a LIVE person's credential. Without the identityKey check the op would read
// the erased party's write path, find it closed, find no link between that pair,
// and tombstone the live person's index — the NotErased gate answered about
// somebody else entirely.
func TestTombstoneOrphanedCredentialIndex_OwnerMismatch_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-mismatch")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbMism")
	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	assertPiiKeyUnshredded(t, ctx, conn, uKey)

	// A DIFFERENT, genuinely erased identity — so the refusal below cannot be
	// NotErased arriving first.
	erasedOther, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("TmbMismO"))
	mutatePiiKey(t, ctx, conn, erasedOther, "piiKey", true, false)

	rejectTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, erasedOther, "TmbMismNo", "OwnerMismatch")
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"naming an erased third party as the owner must not retire a live person's credential index")

	// The other half of the pair: the vertex's own actorKey must agree too. The
	// index is seeded at the hash of tombOrphanBogusCredKey while recording a
	// DIFFERENT credential in its body, which no writer produces but a key-only
	// delete would happily act on.
	seedCredentialIndexAs(t, ctx, conn, tombOrphanBogusCredKey, tombOrphanOtherCredKey, erasedOther)
	rejectTombOrphan(t, ctx, conn, cp, cons, tombOrphanBogusCredKey, erasedOther, "TmbMismAct", "OwnerMismatch")
	assertDocLive(t, ctx, conn, credentialIndexKey(tombOrphanBogusCredKey),
		"an index whose recorded actorKey is not the one that hashes to it must be refused, not deleted")

	// The positive vector: both halves named truthfully, over the same corpus.
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", true, false)
	submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbMismYes", processor.OutcomeAccepted)
	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"the same credential, with the owner named as the index actually records it")
}

// TestTombstoneOrphanedCredentialIndex_SecondRunRefuses — idempotence BY
// REFUSAL, not by re-writing. An operator re-driving the CLI sweep meets an
// already-cleared index on every pass after the first; accepting there would
// re-tombstone what is already gone and bump its revision forever, and a sweep
// that can never report clean is a sweep nobody finishes.
func TestTombstoneOrphanedCredentialIndex_SecondRunRefuses(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-idem")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbIdem")
	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", true, false)

	submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbIdem1", processor.OutcomeAccepted)
	firstEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(consumerActorKey))
	if err != nil {
		t.Fatalf("KVGet credentialindex: %v", err)
	}

	rejectTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbIdem2", "CredentialIndexAlreadyClear")
	secondEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(consumerActorKey))
	if err != nil {
		t.Fatalf("KVGet credentialindex after re-run: %v", err)
	}
	if secondEntry.Revision != firstEntry.Revision {
		t.Fatalf("a re-run rewrote an already-tombstoned credentialindex (revision %d -> %d); "+
			"CredentialIndexAlreadyClear must refuse rather than commit", firstEntry.Revision, secondEntry.Revision)
	}
}

// TestTombstoneOrphanedCredentialIndex_UngrantedActor_Denied proves the grant is
// what authorizes this, not the lane: the control actor holds the same operator
// role on the same default lane, granting a different identity-domain op.
func TestTombstoneOrphanedCredentialIndex_UngrantedActor_Denied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTombOrphanEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "tomborphan-auth")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "TmbAuth")
	retractBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	mutatePiiKey(t, ctx, conn, uKey, "piiKey", true, false)

	// AuthDenied specifically, not merely "rejected": the control actor's
	// envelope is otherwise the accepted shape, so a rejection carrying a SCRIPT
	// refusal would mean the script ran for an actor that should never have
	// reached it, and this test would have proved the wrong thing.
	env := tombOrphanEnv(t, tombOrphanUngrantedActorKey, consumerActorKey, uKey, testutil.GenReqID("TmbAuthNo"))
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", outcome)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeAuthDenied {
		t.Fatalf("rejected with %+v, want code %s — the ungranted actor must be stopped by the capability gate, not by a script refusal",
			reply.Error, processor.ErrCodeAuthDenied)
	}
	assertDocLive(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"a denied submit must change nothing")

	submitTombOrphan(t, ctx, conn, cp, cons, consumerActorKey, uKey, "TmbAuthYes", processor.OutcomeAccepted)
	assertTombstoned(t, ctx, conn, credentialIndexKey(consumerActorKey),
		"the identical envelope from the granted actor is accepted, so the denial was the grant and not the shape")
}
