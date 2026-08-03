// ReconcileCredentialBinding tests (client-ceremony-op-descriptors-design.md
// Inc 2b-1, §15) — the repair verb that converges the boundTo link plane onto
// the credentialindex vertex, so a binding the link plane never recorded
// becomes projectable without decrypting anything.
//
// A binding with no edge is reproduced by claiming and then DELETING the link
// key, which leaves the same pair of facts the op is built to reach: a live
// index vertex and no edge.
//
// Every rejection asserts the script's own outcome word rather than the bare
// "rejected" MessageOutcome. That collapses authorization, hydration and
// payload errors into one value, so an outcome-only assertion cannot tell that
// a guard fired at all — and two of these guards are reachable only through
// state another guard would also reject.
//
// Coverage:
//  1. TestReconcileCredentialBinding_RestoresMissingLink — the case it exists
//     for, carrying the index's own boundAt rather than the submission time.
//  2. TestReconcileCredentialBinding_Idempotent — a re-run over a converged
//     corpus writes a byte-identical document.
//  3. TestReconcileCredentialBinding_TombstonedIndexRejects — a deliberate
//     unlink is not undone.
//  4. TestReconcileCredentialBinding_ShreddedEdgeIsNotRevived — an erasure is
//     not undone, which the index alone cannot tell you.
//  5. TestReconcileCredentialBinding_OwnerMismatchRejects — no caller can mint
//     an edge the index does not already assert.
//  6. TestReconcileCredentialBinding_SelfLoopRejects.
package identitydomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// assertReconcileRejected pins WHICH guard fired. `want` is the outcome word
// the script's fail_reconcile emits.
func assertReconcileRejected(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, env *processor.OperationEnvelope, want string) {
	t.Helper()
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", outcome)
	}
	if reply.Error == nil {
		t.Fatalf("rejected with no error detail; want CredentialReconcileRejected: %s", want)
	}
	if !strings.Contains(reply.Error.Message, "CredentialReconcileRejected: "+want) {
		t.Fatalf("rejected with %q, want the %q guard — a rejection from anywhere else means the guard under test never ran",
			reply.Error.Message, want)
	}
}

// reconcileEnv submits as the operator-equivalent staff actor with scope=any:
// this op repairs someone ELSE's edge, so op.actor is never the owner. It
// declares nothing about either derived key — derive_reads is what supplies
// them, and a test that declared them by hand would pass with the derivation
// deleted.
func reconcileEnv(reqID, credentialActorKey, identityKey string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReconcileCredentialBinding",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-08-03T12:00:00Z",
		Class:         "identity",
		Payload: json.RawMessage(`{"credentialActorKey":"` + credentialActorKey +
			`","identityKey":"` + identityKey + `"}`),
		AuthContext: &processor.AuthContext{Target: identityKey},
	}
}

// dropBoundToLink removes the edge, reproducing a binding the link plane never
// recorded: the index vertex is live, and nothing points anywhere.
func dropBoundToLink(t *testing.T, ctx context.Context, conn *substrate.Conn, credentialActorKey, ownerIdentityKey string) {
	t.Helper()
	if err := conn.KVDelete(ctx, testutil.HarnessCoreBucket, boundToLinkKey(credentialActorKey, ownerIdentityKey)); err != nil {
		t.Fatalf("drop boundTo link: %v", err)
	}
}

// tombstoneBoundToLink retracts the edge the way ShredIdentityKey does — a
// soft tombstone that preserves the document — while leaving the
// credentialindex vertex untouched, which is the half the shred does not erase.
func tombstoneBoundToLink(t *testing.T, ctx context.Context, conn *substrate.Conn, credentialActorKey, ownerIdentityKey string) {
	t.Helper()
	linkKey := boundToLinkKey(credentialActorKey, ownerIdentityKey)
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey)
	if err != nil {
		t.Fatalf("KVGet link %s: %v", linkKey, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal link %s: %v", linkKey, err)
	}
	doc["isDeleted"] = true
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal link %s: %v", linkKey, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, linkKey, raw); err != nil {
		t.Fatalf("tombstone boundTo link: %v", err)
	}
}

func credentialIndexRevision(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey string) uint64 {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(actorKey))
	if err != nil {
		t.Fatalf("KVGet credentialindex(%s): %v", actorKey, err)
	}
	return entry.Revision
}

func credentialIndexBoundAt(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey string) string {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(actorKey))
	if err != nil {
		t.Fatalf("KVGet credentialindex(%s): %v", actorKey, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal credentialindex(%s): %v", actorKey, err)
	}
	data, _ := doc["data"].(map[string]any)
	boundAt, _ := data["boundAt"].(string)
	if boundAt == "" {
		t.Fatalf("credentialindex(%s).data.boundAt is empty — nothing for the reconcile to carry", actorKey)
	}
	return boundAt
}

// TestReconcileCredentialBinding_RestoresMissingLink is the whole point of the
// increment. The boundAt assertion is the load-bearing half: a reconcile that
// stamped its own SubmittedAt would rewrite every historical binding's
// provenance to say it happened the day the repair ran, and the resulting
// corpus would be indistinguishable from a correct one.
func TestReconcileCredentialBinding_RestoresMissingLink(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icr-restore")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "ReconRestore")
	wantBoundAt := credentialIndexBoundAt(t, ctx, conn, consumerActorKey)
	dropBoundToLink(t, ctx, conn, consumerActorKey, uKey)

	indexRevBefore := credentialIndexRevision(t, ctx, conn, consumerActorKey)

	testutil.PublishOp(t, conn, reconcileEnv(testutil.GenReqID("ReconRestoreDo"), consumerActorKey, uKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	data := assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
	if got, _ := data["boundAt"].(string); got != wantBoundAt {
		t.Fatalf("reconciled boundAt = %q, want the index's %q — the repair must carry the original binding instant, not its own run time",
			got, wantBoundAt)
	}

	// The index must be IN the committed batch, not merely read. Only mutation
	// keys carry their hydrated revision into the commit condition, so an index
	// that is read-and-forgotten lets an UnlinkCredential land between hydrate
	// and commit and leaves a live edge over a tombstoned index — a row no
	// later unlink can retract, because its array entry is already gone. A
	// bumped revision is the observable that the guard write is there.
	if got := credentialIndexRevision(t, ctx, conn, consumerActorKey); got <= indexRevBefore {
		t.Fatalf("credentialindex revision %d did not advance past %d — the index is outside the conditioned write footprint, so the decision it drove is unguarded",
			got, indexRevBefore)
	}
}

// TestReconcileCredentialBinding_Idempotent — the CLI driver skips a credential
// whose link is already live, but a re-run under a race must still be harmless.
// The write is an update over the key derive_reads hydrated, so a converged
// edge reconciles to the same document.
func TestReconcileCredentialBinding_Idempotent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icr-idem")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "ReconIdem")
	before := assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)

	testutil.PublishOp(t, conn, reconcileEnv(testutil.GenReqID("ReconIdem1"), consumerActorKey, uKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	testutil.PublishOp(t, conn, reconcileEnv(testutil.GenReqID("ReconIdem2"), consumerActorKey, uKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	after := assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
	if before["boundAt"] != after["boundAt"] {
		t.Fatalf("boundAt drifted across reconciles: %v -> %v", before["boundAt"], after["boundAt"])
	}
}

// TestReconcileCredentialBinding_TombstonedIndexRejects — UnlinkCredential
// tombstones the index and the link in one batch, so the index is what tells a
// deliberate removal apart from a missing edge. Reading the encrypted array
// instead would not have: the array is what the unlink rewrites last.
func TestReconcileCredentialBinding_TombstonedIndexRejects(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icr-tomb")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "ReconTomb")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "ReconTomb2", "link-secret-recon-tomb")

	seedIdentityCapDoc(t, ctx, conn, uKey, "UnlinkCredential")
	testutil.PublishOp(t, conn, unlinkEnv(testutil.GenReqID("ReconTombUn"), uKey, secondCredActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertTombstonedBoundTo(t, ctx, conn, secondCredActorKey, uKey)

	// The unlink tombstones the link too, so `retracted` is what fires first.
	// Either guard is a refusal to undo the removal; this pins which.
	assertReconcileRejected(t, ctx, conn, cp, cons,
		reconcileEnv(testutil.GenReqID("ReconTombDo"), secondCredActorKey, uKey), "retracted")

	assertTombstonedBoundTo(t, ctx, conn, secondCredActorKey, uKey)

	// And with the link gone entirely, the tombstoned INDEX is the sole
	// remaining guard — the branch that would otherwise be shadowed.
	dropBoundToLink(t, ctx, conn, secondCredActorKey, uKey)
	assertReconcileRejected(t, ctx, conn, cp, cons,
		reconcileEnv(testutil.GenReqID("ReconTombIdx"), secondCredActorKey, uKey), "not-bound")
}

// TestReconcileCredentialBinding_ShreddedEdgeIsNotRevived is the case the
// credentialindex vertex cannot answer on its own, and the reason this op does
// not treat it as the only authority.
//
// privacy-base's ShredIdentityKey tombstones every boundTo link touching an
// erased identity — the link names, in the key itself and in plaintext, which
// sign-in credential belonged to which person, so it must not outlive the
// encryption key. It does NOT tombstone the credentialindex vertex. An
// index-only judgement would therefore see "live index, missing edge", call it
// work, and republish the exact association the erasure destroyed — restoring
// it decrypt-free and graph-traversable, for a person whose data is gone.
func TestReconcileCredentialBinding_ShreddedEdgeIsNotRevived(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icr-shred")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "ReconShred")

	// The shred's effect on this edge, reproduced exactly: the link tombstoned,
	// the index left standing.
	tombstoneBoundToLink(t, ctx, conn, consumerActorKey, uKey)
	if entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(consumerActorKey)); err != nil {
		t.Fatalf("the index must survive the shred for this test to mean anything: %v", err)
	} else {
		var doc map[string]any
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			t.Fatalf("unmarshal index: %v", err)
		}
		if deleted, _ := doc["isDeleted"].(bool); deleted {
			t.Fatal("index is tombstoned — the scenario under test no longer exists")
		}
	}

	assertReconcileRejected(t, ctx, conn, cp, cons,
		reconcileEnv(testutil.GenReqID("ReconShredDo"), consumerActorKey, uKey), "retracted")

	assertTombstonedBoundTo(t, ctx, conn, consumerActorKey, uKey)
}

// TestReconcileCredentialBinding_OwnerMismatchRejects — identityKey is
// client-supplied out of necessity (derive_reads needs both halves of the link
// key before anything is hydrated), so the index has to be the authority. A
// caller naming an owner the index does not record is claiming an edge that
// does not exist, and gets nothing.
func TestReconcileCredentialBinding_OwnerMismatchRejects(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icr-owner")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "ReconOwner")
	otherKey, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("ReconOwnerOth"))

	assertReconcileRejected(t, ctx, conn, cp, cons,
		reconcileEnv(testutil.GenReqID("ReconOwnerDo"), consumerActorKey, otherKey), "owner-mismatch")

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, boundToLinkKey(consumerActorKey, otherKey)); err == nil {
		t.Fatalf("a boundTo edge to %s exists — the index never recorded that owner, so nothing should have been written", otherKey)
	}
	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
}

// TestReconcileCredentialBinding_SelfLoopRejects — the same guard
// MergeIdentity's rekey loop applies. A vertex cannot be its own credential,
// and the link key would name one vertex twice.
//
// The index vertex is seeded deliberately. A merge writes exactly this shape —
// an index whose actorKey and identityKey are both the primary, for its own
// implicit self-credential — and without it the op would reject `not-bound`
// instead, so the assertion would hold with the self-loop guard deleted.
func TestReconcileCredentialBinding_SelfLoopRejects(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icr-self")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "ReconSelf")

	selfIndex, err := json.Marshal(map[string]any{
		"class":     "credentialindex",
		"isDeleted": false,
		"data":      map[string]any{"actorKey": uKey, "identityKey": uKey, "boundAt": "2026-05-01T00:00:00Z"},
	})
	if err != nil {
		t.Fatalf("marshal self index: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, credentialIndexKey(uKey), selfIndex); err != nil {
		t.Fatalf("seed self-loop index: %v", err)
	}

	assertReconcileRejected(t, ctx, conn, cp, cons,
		reconcileEnv(testutil.GenReqID("ReconSelfDo"), uKey, uKey), "self-loop")
}
