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
//  4. TestClaimIdentity_RebindsAfterUnlink — pins the tombstoned-index revive
//     branch. No ClaimIdentity submitter declares the credentialindex probe,
//     so derive_reads is what supplies that key and makes the branch
//     reachable at all.
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

// TestClaimIdentity_RebindsAfterUnlink pins that a credential whose index was
// tombstoned by UnlinkCredential can be bound to a fresh identity.
//
// No ClaimIdentity submitter declares the credentialindex probe — opmetas'
// dispatch template substitutes, it does not hash — so the key reaches the
// script only because derive_reads supplies it. Without that, the script's
// read-before-create branch is dormant on this path: the probe reads absent,
// `credential_index_mutation` emits a plain CreateOnly create, and the create
// asserts revision 0 against a key that already has write history and dies on
// RevisionConflict, leaving the credential unbindable.
//
// With the key supplied, the tombstone is visible and the CAS-guarded revive
// branch the multi-credential design wrote for exactly this case is reachable.
// That is what this test holds the platform to.
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
	// Without the derived read declared, this fails RevisionConflict on the
	// index re-create.
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

// TestClaimIdentity_UndeclaredSubmitter_Claims: a ClaimIdentity declaring NO
// ContextHint at all claims the identity. derive_reads' own optionalReads
// carries the target root, its .state and its .claimKey, so
// vertex_alive(state, lookup_key) / read_state / state[...] see the live,
// unclaimed target and its claim-key hash rather than misreading each
// undeclared key as absent (which would misattribute the claim-attempts
// Health-KV counter to no-target instead of the genuine accept).
func TestClaimIdentity_UndeclaredSubmitter_Claims(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "claim-nodecl")

	createReqID := testutil.GenReqID("ClaimNoDeclCreate")
	identityKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("ClaimNoDeclDo000"),
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + claimKeyPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed — the derivation must hydrate the target root/.state/.claimKey for the claim to run at all", got)
	}
}

// TestCompleteCredentialLink_UndeclaredSubmitter_BindsCredential_ClaimedTarget:
// a CompleteCredentialLink declaring NO ContextHint at all binds a SECOND
// credential to a target already claimed via ClaimIdentity — the population
// this op exists for (target_state must be "claimed", execute()'s own guard).
// derive_reads' own optionalReads carries the target root, its .state, its
// .linkKey AND its .credentialBinding (this DDL's own derive_reads comment:
// CompleteCredentialLink derives .credentialBinding, unlike ClaimIdentity,
// because every real dispatch of this op is against an already-claimed
// target, so there is no claimed-vs-unclaimed NFR-S6 timing separation left
// to leak — only a claimed-vs-claimed constant), so
// credential_binding_first_write sees the LIVE aspect and appends to it
// rather than misreading an undeclared key as absent and attempting a CREATE
// against a document that already exists (RevisionConflict).
func TestCompleteCredentialLink_UndeclaredSubmitter_BindsCredential_ClaimedTarget(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "cmpl-nodecl-claimed")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "CmplNoDeclClmd")
	seedIdentityCapDoc(t, ctx, conn, uKey, "InitiateCredentialLink")

	const secret = "link-secret-nodecl-claimed"
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("CmplNdClArm"), uKey, sha256HexOf(secret)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CmplNdClDo0000"),
		Lane:          processor.LaneDefault,
		OperationType: "CompleteCredentialLink",
		Actor:         secondCredActorKey,
		SubmittedAt:   "2026-07-11T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"targetIdentityKey":"` + uKey + `","linkKey":"` + secret + `"}`),
		AuthContext:   &processor.AuthContext{Target: secondCredActorKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(secondCredActorKey)); err != nil {
		t.Fatalf("no credentialindex vertex for the bound credential — the derivation must hydrate the target root/.state/.linkKey for the bind to run at all: %v", err)
	}
	bindData := readDecryptedAspectData(t, ctx, conn, uKey, "credentialBinding")
	creds, _ := bindData["credentials"].([]interface{})
	found := false
	for _, c := range creds {
		m, _ := c.(map[string]interface{})
		if got, _ := m["actorKey"].(string); got == secondCredActorKey {
			found = true
		}
	}
	if !found {
		t.Fatalf("credentialBinding.credentials = %+v, want an entry for %q — the derivation must hydrate the LIVE .credentialBinding for the second bind to append rather than collide", creds, secondCredActorKey)
	}
}

// TestCompleteCredentialLink_UndeclaredSubmitter_BindsCredential_ScenarioB: the
// same bare envelope against a Scenario-B target (ProvisionConsumerIdentity,
// never claimed via ClaimIdentity) — the other population CompleteCredentialLink
// serves. Its .credentialBinding is genuinely absent on the server too, so
// credential_binding_first_write's binding_absent branch creates the aspect
// fresh, exercising the derivation's OTHER branch from the claimed-target
// vector above.
func TestCompleteCredentialLink_UndeclaredSubmitter_BindsCredential_ScenarioB(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "cmpl-nodecl-scenb")

	scenarioBKey := "vtx.identity.SCNBUNDCLHJKMNPQRST1"
	roleKey := consumerRoleKey(t)
	testutil.PublishOp(t, conn, provisionEnvelope(t, testutil.GenReqID("CmplNoDeclProv"), scenarioBKey, roleKey, "2026-07-11T09:00:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	seedIdentityCapDoc(t, ctx, conn, scenarioBKey, "InitiateCredentialLink")
	const secret = "link-secret-nodecl-bare"
	testutil.PublishOp(t, conn, initiateLinkEnv(testutil.GenReqID("CmplNoDeclArm"), scenarioBKey, sha256HexOf(secret)))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CmplNoDeclDo000"),
		Lane:          processor.LaneDefault,
		OperationType: "CompleteCredentialLink",
		Actor:         secondCredActorKey,
		SubmittedAt:   "2026-07-11T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"targetIdentityKey":"` + scenarioBKey + `","linkKey":"` + secret + `"}`),
		AuthContext:   &processor.AuthContext{Target: secondCredActorKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(secondCredActorKey)); err != nil {
		t.Fatalf("no credentialindex vertex for the bound credential — the derivation must hydrate the target root/.state/.linkKey for the bind to run at all: %v", err)
	}
}

// TestUnlinkCredential_UndeclaredSubmitter_Unlinks: an UnlinkCredential
// declaring NO ContextHint at all unlinks the second credential. derive_reads'
// own optionalReads carries U's own root/.state/.mergedInto/.credentialBinding
// (op.actor is always known — it is the authenticated caller, never
// payload-supplied), so state[u_key] / read_state / read_merged_into see U's
// live identity rather than misreading an undeclared key as absent (which
// would fail no-target/wrong-state on a legitimate self-unlink).
func TestUnlinkCredential_UndeclaredSubmitter_Unlinks(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "unlnk-nodecl")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "UnlnkNoDecl")
	linkSecondCredential(t, ctx, conn, cp, cons, uKey, secondCredActorKey, "UnlnkNoDeclLink", "link-secret-unlnk-nodecl")
	seedIdentityCapDoc(t, ctx, conn, uKey, "UnlinkCredential")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("UnlnkNoDeclDo00"),
		Lane:          processor.LaneDefault,
		OperationType: "UnlinkCredential",
		Actor:         uKey,
		SubmittedAt:   "2026-07-12T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"credentialActorKey":"` + secondCredActorKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: uKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	// The derivation must hydrate U's own root/.state/.mergedInto/
	// .credentialBinding for the unlink to run at all: absent that, the
	// bare submission above would have failed no-target/wrong-state instead
	// of reaching this tombstone.
	assertTombstonedBoundTo(t, ctx, conn, secondCredActorKey, uKey)
}

// TestRevokeIdentityClaim_UndeclaredSubmitter_RefusedLiteral: a bare
// RevokeIdentityClaim (ContextHint: nil) against a live, claimed identity is
// refused no-target, and nothing commits. RevokeIdentityClaim's own
// derive_reads arm does not derive the vertex/.state/.credentialBinding its
// descriptor lists as REQUIRED Reads (ddls.go), so an undeclared submitter
// hydrates none of them and execute()'s own `identity_key not in state` check
// reads the live identity as absent — the operator-only standing this verb
// requires makes an under-declaring caller's refusal the correct outcome, not
// a derivation gap to close. (revoke_identity_claim_test.go's own
// TestRevokeIdentityClaim_UndeclaredSubmitter_Refused pins the same property
// through the package's revokeEnv helper, whose composite literal sits inside
// that helper's body and so is invisible to a literal-only static scan; this
// vector restates it as a literal envelope for the census to see.)
func TestRevokeIdentityClaim_UndeclaredSubmitter_RefusedLiteral(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "rvk-nodecl-lit")

	identityKey := claimFreshIdentity(t, ctx, conn, cp, cons, "RvkNoDeclLit")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RvkNoDeclLitDo0"),
		Lane:          processor.LaneDefault,
		OperationType: "RevokeIdentityClaim",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-13T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","newClaimKeyHash":"` + sha256HexOf("whatever") + `"}`),
		ContextHint:   nil,
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want Rejected (reply=%+v)", outcome, reply)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "no-target") {
		t.Fatalf("want a refusal naming no-target, got %+v", reply.Error)
	}
	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed — an undeclared submit must change nothing", got)
	}
}

// TestReconcileCredentialBinding_UndeclaredSubmitter_RestoresLink: a
// ReconcileCredentialBinding declaring NO ContextHint at all restores a
// boundTo link the link plane dropped. derive_reads' own optionalReads
// carries credentialIndexKey(credentialActorKey), the credentialActorKey
// root, the boundTo link, and both ends' erasure-gate keys — the op's only
// dispatcher declares no contextHint at all (this DDL's own derive_reads
// comment), so class (g) is the sole source of every key it reads. The
// package's own reconcileEnv helper (credential_reconcile_test.go) builds the
// identical literal shape, but the composite sits inside that helper's body
// and so is invisible to a literal-only static scan; this vector restates it
// directly so the census can see it.
func TestReconcileCredentialBinding_UndeclaredSubmitter_RestoresLink(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newLinkPipeline(t, ctx, conn, "icr-nodecl-lit")

	uKey := claimFreshIdentity(t, ctx, conn, cp, cons, "ReconNoDeclLit")
	wantBoundAt := credentialIndexBoundAt(t, ctx, conn, consumerActorKey)
	dropBoundToLink(t, ctx, conn, consumerActorKey, uKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("ReconNoDeclLitDo"),
		Lane:          processor.LaneDefault,
		OperationType: "ReconcileCredentialBinding",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-08-03T12:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"credentialActorKey":"` + consumerActorKey + `","identityKey":"` + uKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: uKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("outcome = %v, want Accepted (reply=%+v)", outcome, reply)
	}
	data := assertLiveBoundTo(t, ctx, conn, consumerActorKey, uKey)
	if got, _ := data["boundAt"].(string); got != wantBoundAt {
		t.Fatalf("reconciled boundAt = %q, want the index's %q — the derivation must hydrate the index for the repair to carry the original binding instant", got, wantBoundAt)
	}
}
