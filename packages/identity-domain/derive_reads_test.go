// derive_reads adoption tests (client-ceremony-op-descriptors-design.md Inc 1b,
// Contract #2 §2.5 class (g)) — the package's own derivation is now the only
// one, so what has to be pinned is that it agrees with everything that used to
// re-derive the same key, and that a submitter declaring NOTHING still gets the
// dedup probes hydrated.
//
// Coverage:
//  1. TestDeriveReads_NormalizationVectors — each raw contact driven through a
//     real CreateUnclaimedIdentity lands its index vertex at the key derived
//     from the expected NORMALIZED form, across a table exercising every rule
//     (case, whitespace collapse, phone punctuation). The one derivation that
//     deliberately does NOT normalize — the actor-keyed credentialindex — is
//     pinned in gateway_agreement_test.go, where the same assertion also holds
//     the gateway's Go copy of it to the script.
//  2. TestCreateUnclaimed_UndeclaredSubmitter_StillDedupes — the §7 e2e: a
//     second create sharing a contact, whose envelope declares no contextHint
//     at all, still probes the index and emits duplicateOf instead of hard-
//     failing RevisionConflict. This is the failure mode Inc 1 exists to close
//     at the platform layer rather than by a well-behaved client.
//  3. TestCompleteCredentialLink_UndeclaredSubmitter_StillGuards — the same for
//     the actor-derived credentialindex probe, whose every pre-existing test
//     declares the key itself and so cannot see the derivation at all.
//  4. TestClaimIdentity_RebindsAfterUnlink — the behaviour this increment
//     CHANGES: ClaimIdentity never had a submitter that could declare the
//     probe, so the tombstoned-index revive branch was dead until now.
package identitydomain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestDeriveReads_NormalizationVectors drives each vector's RAW contact through
// a real CreateUnclaimedIdentity — declaring nothing — and asserts the script
// indexed it under the key derived from the EXPECTED normalized form.
//
// It has to go through the pipeline to be worth anything. A table that computed
// both sides in Go would only prove `sha256NanoID(x) == substrate.SHA256NanoID(x)`
// — two Go functions agreeing about a string neither one normalized — and would
// still pass if every Starlark normalizer were replaced by `return raw`. The
// normalization is the part that was hand-ported four times and the part that
// silently drifted, so it is the part that has to be executed, in Starlark, to
// be pinned at all.
//
// The vectors exercise each rule rather than sampling: email lowercases and
// trims, phone keeps only digits and '+', name lowercases and collapses
// interior whitespace.
func TestDeriveReads_NormalizationVectors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		contactType string
		raw         string
		normalized  string
	}{
		{"email lowercases", "email", "Dup.Flow@Example.COM", "dup.flow@example.com"},
		{"email trims", "email", "  spaced@example.com  ", "spaced@example.com"},
		{"phone strips punctuation", "phone", "+1 (555) 010-9999", "+15550109999"},
		{"phone bare digits", "phone", "5550109999", "5550109999"},
		{"name lowercases", "name", "Ada LOVELACE", "ada lovelace"},
		{"name collapses interior whitespace", "name", "Ada\t  Byron   Lovelace", "ada byron lovelace"},
		{"name trims", "name", "   Ada Lovelace   ", "ada lovelace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, conn := setupTestEnv(t)
			cp, cons := newCreatePipeline(t, ctx, conn, "ici-nv-"+strings.NewReplacer(" ", "", "'", "").Replace(tc.name))

			// Every vector needs a name and one contact. When the vector under
			// test IS the name, pair it with a unique email so the create is
			// valid; otherwise supply a fixed name.
			payload := map[string]string{"claimKeyHash": strings.Repeat("e", 64)}
			switch tc.contactType {
			case "name":
				payload["name"] = tc.raw
				payload["email"] = "nv-" + tc.normalized + "@example.com"
			case "email":
				payload["name"] = "NV " + tc.normalized
				payload["email"] = tc.raw
			case "phone":
				payload["name"] = "NV " + tc.normalized
				payload["phone"] = tc.raw
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}

			reqID := testutil.GenReqID("NormVec")
			testutil.PublishOp(t, conn, &processor.OperationEnvelope{
				RequestID:     reqID,
				Lane:          processor.LaneDefault,
				OperationType: "CreateUnclaimedIdentity",
				Actor:         staffActorKey,
				SubmittedAt:   "2026-08-03T12:00:00Z",
				Class:         "identity",
				Payload:       body,
			})
			testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

			// The index vertex the script wrote must sit at the key derived
			// from the NORMALIZED form. If a normalizer stopped normalizing,
			// the key moves and this read misses.
			wantKey := "vtx.identityindex." + substrate.SHA256NanoID(tc.contactType+":"+tc.normalized)
			if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, wantKey); err != nil {
				t.Fatalf("no identityindex vertex at %s (raw %q → expected normalized %q): %v",
					wantKey, tc.raw, tc.normalized, err)
			}
		})
	}
}

// TestCreateUnclaimed_UndeclaredSubmitter_StillDedupes is the design's §7 e2e.
//
// The first create seeds the email index. The second shares that email and
// declares NO contextHint whatsoever — the state a submitter is in once its
// hand-ported derivation is deleted. Before class (g), that envelope hydrated
// nothing, the script's `email_index_key in state` probe answered False, and
// the blind index create collided with the incumbent's write history:
// RevisionConflict, a hard failure on a legitimate duplicate.
//
// The assertion is deliberately the ACCEPT plus the duplicateOf link, not just
// the accept: an accept alone would also be produced by a script that stopped
// probing entirely, which is the regression this test would otherwise miss.
func TestCreateUnclaimed_UndeclaredSubmitter_StillDedupes(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-nodecl")

	email := "nodecl@example.com"

	reqID1 := testutil.GenReqID("NoDeclFirst")
	firstID := identityIDFromRequestID(reqID1)
	firstKey := "vtx.identity." + firstID

	// The incumbent declares nothing either — the derivation serves both.
	env1 := &processor.OperationEnvelope{
		RequestID:     reqID1,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-08-03T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"No Decl First","email":"` + email + `","claimKeyHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
	}
	testutil.PublishOp(t, conn, env1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	reqID2 := testutil.GenReqID("NoDeclSecond")
	secondID := identityIDFromRequestID(reqID2)

	env2 := &processor.OperationEnvelope{
		RequestID:     reqID2,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-08-03T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"No Decl Second","email":"` + email + `","claimKeyHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`),
	}
	testutil.PublishOp(t, conn, env2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	data := readLinkData(t, ctx, conn, duplicateOfLinkKey(secondID, firstKey))
	criteria := criteriaStrings(t, data)
	if len(criteria) != 1 || criteria[0] != "exact-email" {
		t.Fatalf("criteria = %v, want [exact-email] — the derived probe did not hydrate", criteria)
	}
}

// TestCreateUnclaimed_UndeclaredSubmitter_MixedCaseContactStillDedupes proves
// the derivation carries the package's NORMALIZATION, not merely its hash. A
// client that had to re-implement this is exactly where the two would drift:
// the incumbent registers a lowercase email, the duplicate submits it
// shouting, and only a shared normalizer makes those the same index key.
func TestCreateUnclaimed_UndeclaredSubmitter_MixedCaseContactStillDedupes(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-nodeclcase")

	reqID1 := testutil.GenReqID("NoDeclCaseFirst")
	firstID := identityIDFromRequestID(reqID1)
	firstKey := "vtx.identity." + firstID

	env1 := &processor.OperationEnvelope{
		RequestID:     reqID1,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-08-03T11:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Ada Lovelace","email":"ada@example.com","claimKeyHash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`),
	}
	testutil.PublishOp(t, conn, env1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Same person, shouted, spaced, and phone-punctuated differently.
	reqID2 := testutil.GenReqID("NoDeclCaseSecond")
	secondID := identityIDFromRequestID(reqID2)

	env2 := &processor.OperationEnvelope{
		RequestID:     reqID2,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-08-03T11:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"  ADA   LOVELACE ","email":"  Ada@Example.COM ","claimKeyHash":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}`),
	}
	testutil.PublishOp(t, conn, env2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	data := readLinkData(t, ctx, conn, duplicateOfLinkKey(secondID, firstKey))
	criteria := criteriaStrings(t, data)
	joined := strings.Join(criteria, ",")
	if !strings.Contains(joined, "exact-email") || !strings.Contains(joined, "exact-name") {
		t.Fatalf("criteria = %v, want both exact-email and exact-name — normalization did not travel with the derivation", criteria)
	}
}

// TestCompleteCredentialLink_UndeclaredSubmitter_StillGuards covers the
// actor-derived half. Every pre-existing CompleteCredentialLink test declares
// credentialIndexKey(actor) in its own envelope, so under weakest-wins the
// derived key is a no-op in all of them — deleting derive_reads' credentialindex
// branch outright would leave those green. This one declares nothing.
func TestCompleteCredentialLink_UndeclaredSubmitter_StillGuards(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icl-nodecl")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "LinkNoDecl")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")

	const secret = "link-secret-nodecl"
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("LinkNoDeclArm"), uKey, sha256HexOf(secret)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The submitter declares only what it can name; the credentialindex probe
	// is left to derive_reads. Everything else stays as the shipped dispatchers
	// send it — absence-tolerant, per the Contract #2 §2.5 floor this op's
	// descriptor now declares.
	env := completeLinkEnv(testutil.GenReqID("LinkNoDeclCmpl"), secondCredActorKey, uKey, secret)
	env.ContextHint.OptionalReads = []string{
		uKey, uKey + ".state", uKey + ".linkKey", uKey + ".credentialBinding",
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The index vertex proves the derived key hydrated: without it the script's
	// read-before-create probe reads absent and takes the plain create branch.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(secondCredActorKey)); err != nil {
		t.Fatalf("no credentialindex vertex for the bound credential: %v", err)
	}
}

// TestClaimIdentity_RebindsAfterUnlink is the behaviour change this increment
// makes, asserted rather than discovered later.
//
// No ClaimIdentity submitter ever declared the credentialindex probe — opmetas'
// dispatch template substitutes, it does not hash — so the script's
// read-before-create branch was DORMANT on this path: the probe always read
// absent and `credential_index_mutation` always emitted a plain CreateOnly
// create. A credential whose index UnlinkCredential had tombstoned therefore
// could not be re-bound at all; the create asserted revision 0 against a key
// that already had write history and died on RevisionConflict.
//
// derive_reads supplies that key, so the tombstone is now visible and the
// CAS-guarded revive branch — which the multi-credential design wrote for
// exactly this case — is reachable. That is the intended behaviour, and
// this test pins it as reachable.
func TestClaimIdentity_RebindsAfterUnlink(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icl-rebind")

	// Bind secondCredActorKey to U, then unlink it — tombstoning its index.
	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "Rebind")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "RebindLink", "link-secret-rebind")
	seedIdentityCapDoc(t, ctx, conn, uKey, "UnlinkCredential")
	testutil.PublishOp(t, conn, unlinkEnv(testutil.GenReqID("RebindUnlnk"), uKey, secondCredActorKey))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The same credential now claims a DIFFERENT, fresh unclaimed identity.
	// Pre-derivation this failed RevisionConflict on the index re-create.
	targetKey, claimPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, testutil.GenReqID("RebindTarget"))
	seedIdentityCapDoc(t, ctx, conn, secondCredActorKey, "ClaimIdentity")

	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RebindClaim"),
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         secondCredActorKey,
		SubmittedAt:   "2026-08-03T13:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"targetIdentityKey":"` + targetKey + `","claimKey":"` + claimPlaintext + `"}`),
		AuthContext:   &processor.AuthContext{Target: secondCredActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{targetKey, targetKey + ".state", targetKey + ".claimKey"},
		},
	})
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(secondCredActorKey))
	if err != nil {
		t.Fatalf("credentialindex absent after re-bind: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal credentialindex: %v", err)
	}
	if deleted, _ := doc["isDeleted"].(bool); deleted {
		t.Fatalf("credentialindex still tombstoned after a successful re-bind")
	}
	data, _ := doc["data"].(map[string]any)
	if got, _ := data["identityKey"].(string); got != targetKey {
		t.Fatalf("credentialindex points at %q, want the newly claimed %q", got, targetKey)
	}
}
