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
func tombOrphanEnv(t *testing.T, actorKey, credKey, ownerKey, reqID string) *processor.OperationEnvelope {
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
			OptionalReads: []string{
				ownerKey + ".erasureRequested",
				ownerKey + ".piiKey",
				credKey + ".erasureRequested",
				credKey + ".piiKey",
				tombOrphanBoundToKey(credKey, ownerKey),
			},
		},
	}
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
		"the op writes exactly one key — the index vertex — and must not have touched the owner's own vertex")
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
// gate, and the owner's own vertex must survive untouched.
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

	// The residue itself: the index at the hash of the erased subject's own key,
	// recording them as the live owner's credential. No boundTo link exists —
	// the pre-narrowing shred's outbound arm tombstoned it, and a hard-removed
	// link reads absent, which the op treats as the same answer.
	seedCredentialIndexAs(t, ctx, conn, erasedCred, erasedCred, liveOwner)

	reqID := submitTombOrphan(t, ctx, conn, cp, cons, erasedCred, liveOwner, "TmbOutDo", processor.OutcomeAccepted)

	assertTombstoned(t, ctx, conn, credentialIndexKey(erasedCred),
		"the outbound residue names the erased person in the clear exactly as the inbound shape does, and must stop resolving too")
	assertTrackerEvent(t, ctx, conn, reqID, "identity.unbound")
	assertDocLive(t, ctx, conn, liveOwner,
		"the op writes exactly one key — the index vertex — and the live owner's own vertex is not it")
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
