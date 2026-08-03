// boundTo link tests (client-ceremony-op-descriptors-design.md Inc 2a, §4.2) —
// the credential-to-owner edge that makes the bound-credential set projectable
// one row per credential without decrypting the sensitive credentialBinding
// aspect.
//
// Every assertion here goes through the real pipeline, because the link is
// emitted by Starlark from keys derive_reads supplies: a table computing both
// sides in Go would pass with the emit deleted.
//
// Coverage:
//  1. TestBoundTo_ClaimIdentity_EmitsLink — the first credential.
//  2. TestBoundTo_CompleteCredentialLink_EmitsLink — the Nth.
//  3. TestBoundTo_UnlinkCredential_TombstonesLink — the unbind.
//  4. TestBoundTo_RelinkRevivesTombstonedLink — the revive branch the CAS
//     posture exists for, and the one a create-only writer would brick.
//  5. TestBoundTo_UndeclaredSubmitter_StillEmits — the derivation is what
//     supplies the key; a submitter declaring nothing about it still gets a
//     correct link.
package identitydomain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// boundToLinkKey mirrors the script's `boundTo` key: the credential (source,
// the later-arriving vertex per Contract #1 §1.1) to the identity it binds to.
func boundToLinkKey(credentialActorKey, ownerIdentityKey string) string {
	return "lnk.identity." + strings.TrimPrefix(credentialActorKey, "vtx.identity.") +
		".boundTo.identity." + strings.TrimPrefix(ownerIdentityKey, "vtx.identity.")
}

// readLinkDoc returns the whole link document, not just its data — the
// endpoints and isDeleted are what these tests are about.
func readLinkDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, linkKey string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, linkKey)
	if err != nil {
		t.Fatalf("KVGet link %s: %v", linkKey, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal link %s: %v", linkKey, err)
	}
	return doc
}

func assertLiveBoundTo(t *testing.T, ctx context.Context, conn *substrate.Conn, credentialActorKey, ownerIdentityKey string) map[string]any {
	t.Helper()
	doc := readLinkDoc(t, ctx, conn, boundToLinkKey(credentialActorKey, ownerIdentityKey))
	if deleted, _ := doc["isDeleted"].(bool); deleted {
		t.Fatalf("boundTo %s -> %s is tombstoned, want live", credentialActorKey, ownerIdentityKey)
	}
	if got, _ := doc["class"].(string); got != "boundTo" {
		t.Fatalf("boundTo class = %q, want boundTo", got)
	}
	if got, _ := doc["sourceVertex"].(string); got != credentialActorKey {
		t.Fatalf("sourceVertex = %q, want the credential %q — the direction is what the lens walks", got, credentialActorKey)
	}
	if got, _ := doc["targetVertex"].(string); got != ownerIdentityKey {
		t.Fatalf("targetVertex = %q, want the owner %q", got, ownerIdentityKey)
	}
	data, _ := doc["data"].(map[string]any)
	if got, _ := data["boundAt"].(string); got == "" {
		t.Fatalf("data.boundAt is empty — the provenance a projection will read once the engine binds a relationship variable")
	}
	return data
}

func assertTombstonedBoundTo(t *testing.T, ctx context.Context, conn *substrate.Conn, credentialActorKey, ownerIdentityKey string) {
	t.Helper()
	doc := readLinkDoc(t, ctx, conn, boundToLinkKey(credentialActorKey, ownerIdentityKey))
	if deleted, _ := doc["isDeleted"].(bool); !deleted {
		t.Fatalf("boundTo %s -> %s is still live after the unbind — the lens would keep projecting the row",
			credentialActorKey, ownerIdentityKey)
	}
}

// TestBoundTo_ClaimIdentity_EmitsLink — the claim binds the FIRST credential,
// the one that only ever existed as the singular actorKey inside the encrypted
// aspect. Its boundAt must be the same instant the aspect records, or the two
// records of one binding disagree about when it happened.
func TestBoundTo_ClaimIdentity_EmitsLink(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icb-claim")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "BoundToClaim")

	data := assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)

	binding := readDecryptedAspectData(t, ctx, conn, uKey, "credentialBinding")
	wantBoundAt, _ := binding["boundAt"].(string)
	if got, _ := data["boundAt"].(string); got != wantBoundAt {
		t.Fatalf("link boundAt = %q, aspect boundAt = %q — one binding, two disagreeing records", got, wantBoundAt)
	}
}

// TestBoundTo_CompleteCredentialLink_EmitsLink — the Nth credential. Both
// links must be live at once: the whole point of the per-credential row is
// that the set fans out, and an owner with two credentials that projects one
// row is the failure this replaces.
func TestBoundTo_CompleteCredentialLink_EmitsLink(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icb-link")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "BoundToLink")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "BoundToLink2", "link-secret-boundto")

	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
	assertLiveBoundTo(t, ctx, conn, secondCredActorKey, uKey)
}

// TestBoundTo_UnlinkCredential_TombstonesLink — the unbind retracts the edge,
// and only the named one. A tombstone that took the whole owner's set down
// would leave them with a credential they still sign in with and no row for it.
func TestBoundTo_UnlinkCredential_TombstonesLink(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icb-unlink")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "BoundToUnl")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "BoundToUnl2", "link-secret-unl")

	seedIdentityCapDoc(t, ctx, conn, uKey, "UnlinkCredential")
	testutil.PublishOp(t, conn, unlinkEnv(testutil.GenReqID("BoundToUnlDo"), uKey, secondCredActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	assertTombstonedBoundTo(t, ctx, conn, secondCredActorKey, uKey)
	assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
}

// TestBoundTo_RelinkRevivesTombstonedLink is why the writer is an update and
// not a create. Unlink tombstones the key; re-binding the same credential to
// the same identity has to bring it back. A CreateOnly writer would assert
// revision 0 against a key that already has write history and take the whole
// atomic batch down with a RevisionConflict — the op would be accepted-shaped
// nowhere and the person would simply be unable to re-add a credential they
// removed. The key is declared in derive_reads, so the revive commits
// conditioned on the revision it was read at.
func TestBoundTo_RelinkRevivesTombstonedLink(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icb-revive")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "BoundToRev")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "BoundToRev2", "link-secret-rev1")

	seedIdentityCapDoc(t, ctx, conn, uKey, "UnlinkCredential")
	testutil.PublishOp(t, conn, unlinkEnv(testutil.GenReqID("BoundToRevUn"), uKey, secondCredActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	assertTombstonedBoundTo(t, ctx, conn, secondCredActorKey, uKey)

	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "BoundToRev3", "link-secret-rev2")

	assertLiveBoundTo(t, ctx, conn, secondCredActorKey, uKey)
}

// TestBoundTo_UndeclaredSubmitter_StillEmits pins the derivation as the source
// of the key. Every other test here rides helpers whose envelopes declare the
// credentialindex probe by hand; none of them declares the boundTo key, but
// this one declares nothing optional at all, so the emit depends entirely on
// what derive_reads returned.
func TestBoundTo_UndeclaredSubmitter_StillEmits(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icb-nodecl")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "BoundToND")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")

	const secret = "link-secret-nodecl-boundto"
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("BoundToNDArm"), uKey, sha256HexOf(secret)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	env := completeLinkEnv(testutil.GenReqID("BoundToNDCmpl"), secondCredActorKey, uKey, secret)
	env.ContextHint.OptionalReads = []string{uKey + ".linkKey", uKey + ".credentialBinding"}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	assertLiveBoundTo(t, ctx, conn, secondCredActorKey, uKey)
}
