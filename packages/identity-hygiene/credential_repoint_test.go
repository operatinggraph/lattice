// MergeIdentity credential-repoint tests
// (multi-credential-identity-linking-design.md §3.3, Fire 1).
//
// Coverage:
//  1. TestMergeCredentialRepoint_SingularBinding — secondary's pre-Fire-2
//     singular credentialBinding repoints; implicit self-credential also folds.
//  2. TestMergeCredentialRepoint_ArrayBinding_MultipleCredentials — N
//     credentials in secondary's array all repoint, plus the self-credential.
//  3. TestMergeCredentialRepoint_NeverClaimedSecondary — no credentialBinding
//     aspect at all: only the implicit self-credential folds, and no
//     tombstone mutation fires (nothing to tombstone).
//  4. TestMergeCredentialRepoint_PrimaryPreservesExistingSingular — primary's
//     own first-bound credential (the singular actorKey/boundAt fields)
//     survives the union untouched.
//  5. TestMergeCredentialRepoint_ChainMerge — U3→U2→U1: U3's original
//     credential and both U2/U3 self-credentials all end up pointing at U1.
//  6. TestMergeCredentialRepoint_OverwritesExistingIndexVertex — a
//     credentialindex vertex that already exists (pointing at the pre-merge
//     identity) is overwritten to point at primary, proving the unconditioned
//     `update` blind-Put semantics work whether the key pre-exists or not.
package identityhygiene_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// --- 1. TestMergeCredentialRepoint_SingularBinding ---

func TestMergeCredentialRepoint_SingularBinding(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mcrsing")

	primaryID := testutil.GenReqID("PrimCredSing00")
	secondaryID := testutil.GenReqID("SecCredSing000")
	credActorID := testutil.GenReqID("CredActorSing0")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID
	credActorKey := "vtx.identity." + credActorID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "claimed", "")
	seedCredentialBindingSingular(t, ctx, conn, secondaryKey, credActorKey, "2026-01-01T00:00:00Z")
	seedCredentialIndexVertex(t, ctx, conn, credActorKey, secondaryKey, "2026-01-01T00:00:00Z")

	edges := []string{}
	reqID := testutil.GenReqID("MrgCredSing000")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T11:00:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint: &processor.ContextHint{
			Reads:         mergeReads(primaryKey, secondaryKey, edges),
			OptionalReads: mergeCredentialOptionalReads(primaryKey, secondaryKey),
			Enumerations: []processor.EnumerationHint{
				{Hub: secondaryKey, Relation: "assignedTo", Direction: "in"},
				{Hub: secondaryKey, Relation: "indexes", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The bound credential and the implicit self-credential both repoint.
	assertCredentialIndexPointsTo(t, ctx, conn, credActorKey, primaryKey)
	assertCredentialIndexPointsTo(t, ctx, conn, secondaryKey, primaryKey)

	// Primary's unioned array carries both.
	got := primaryCredentialActorKeys(t, ctx, conn, primaryKey)
	if !got[credActorKey] || !got[secondaryKey] {
		t.Fatalf("primary credentials = %v, want both %s and %s", got, credActorKey, secondaryKey)
	}

	// Secondary's own binding is tombstoned.
	assertLinkTombstoned(t, ctx, conn, secondaryKey+".credentialBinding")

	// One identity.rebound per credential (2).
	if n := countTrackerEventClass(t, ctx, conn, reqID, "identity.rebound"); n != 2 {
		t.Fatalf("identity.rebound count = %d, want 2", n)
	}
	assertTrackerEvent(t, ctx, conn, reqID, "identity.merged")
}

// --- 2. TestMergeCredentialRepoint_ArrayBinding_MultipleCredentials ---

func TestMergeCredentialRepoint_ArrayBinding_MultipleCredentials(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mcrarr")

	primaryID := testutil.GenReqID("PrimCredArr000")
	secondaryID := testutil.GenReqID("SecCredArr0000")
	cred1ID := testutil.GenReqID("CredArrOne0000")
	cred2ID := testutil.GenReqID("CredArrTwo0000")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID
	cred1Key := "vtx.identity." + cred1ID
	cred2Key := "vtx.identity." + cred2ID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "claimed", "")
	seedCredentialBindingArray(t, ctx, conn, secondaryKey, []map[string]any{
		{"actorKey": cred1Key, "boundAt": "2026-01-01T00:00:00Z"},
		{"actorKey": cred2Key, "boundAt": "2026-02-01T00:00:00Z"},
	})
	seedCredentialIndexVertex(t, ctx, conn, cred1Key, secondaryKey, "2026-01-01T00:00:00Z")
	seedCredentialIndexVertex(t, ctx, conn, cred2Key, secondaryKey, "2026-02-01T00:00:00Z")

	edges := []string{}
	reqID := testutil.GenReqID("MrgCredArr0000")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T11:01:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint: &processor.ContextHint{
			Reads:         mergeReads(primaryKey, secondaryKey, edges),
			OptionalReads: mergeCredentialOptionalReads(primaryKey, secondaryKey),
			Enumerations: []processor.EnumerationHint{
				{Hub: secondaryKey, Relation: "assignedTo", Direction: "in"},
				{Hub: secondaryKey, Relation: "indexes", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	assertCredentialIndexPointsTo(t, ctx, conn, cred1Key, primaryKey)
	assertCredentialIndexPointsTo(t, ctx, conn, cred2Key, primaryKey)
	assertCredentialIndexPointsTo(t, ctx, conn, secondaryKey, primaryKey)

	got := primaryCredentialActorKeys(t, ctx, conn, primaryKey)
	for _, want := range []string{cred1Key, cred2Key, secondaryKey} {
		if !got[want] {
			t.Fatalf("primary credentials = %v, missing %s", got, want)
		}
	}
	if len(got) != 3 {
		t.Fatalf("primary credentials = %v, want exactly 3 entries", got)
	}

	assertLinkTombstoned(t, ctx, conn, secondaryKey+".credentialBinding")

	if n := countTrackerEventClass(t, ctx, conn, reqID, "identity.rebound"); n != 3 {
		t.Fatalf("identity.rebound count = %d, want 3", n)
	}
}

// --- 3. TestMergeCredentialRepoint_NeverClaimedSecondary ---

// A secondary with no credentialBinding aspect at all (staff-created via
// CreateUnclaimedIdentity, never claimed) still folds its own implicit
// self-credential — inert-but-correct (design §3.3) — but there is nothing
// to tombstone, so that mutation is skipped.
func TestMergeCredentialRepoint_NeverClaimedSecondary(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mcrnever")

	primaryID := testutil.GenReqID("PrimCredNvr000")
	secondaryID := testutil.GenReqID("SecCredNvr0000")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "unclaimed", "")
	// No credentialBinding seeded for either side.

	edges := []string{}
	reqID := testutil.GenReqID("MrgCredNvr0000")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T11:02:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint: &processor.ContextHint{
			Reads:         mergeReads(primaryKey, secondaryKey, edges),
			OptionalReads: mergeCredentialOptionalReads(primaryKey, secondaryKey),
			Enumerations: []processor.EnumerationHint{
				{Hub: secondaryKey, Relation: "assignedTo", Direction: "in"},
				{Hub: secondaryKey, Relation: "indexes", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Only the implicit self-credential folds.
	assertCredentialIndexPointsTo(t, ctx, conn, secondaryKey, primaryKey)
	got := primaryCredentialActorKeys(t, ctx, conn, primaryKey)
	if len(got) != 1 || !got[secondaryKey] {
		t.Fatalf("primary credentials = %v, want exactly {%s}", got, secondaryKey)
	}

	// Secondary never had a credentialBinding aspect; still doesn't.
	assertCredentialBindingAbsent(t, ctx, conn, secondaryKey)

	if n := countTrackerEventClass(t, ctx, conn, reqID, "identity.rebound"); n != 1 {
		t.Fatalf("identity.rebound count = %d, want 1", n)
	}
}

// --- 4. TestMergeCredentialRepoint_PrimaryPreservesExistingSingular ---

// When primary already has its own first-bound credential, the merge's
// union must not clobber it — the singular actorKey/boundAt fields keep
// meaning "primary's own first-bound credential" (design §3.3).
func TestMergeCredentialRepoint_PrimaryPreservesExistingSingular(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mcrpres")

	primaryID := testutil.GenReqID("PrimCredPres00")
	secondaryID := testutil.GenReqID("SecCredPres000")
	primaryCredID := testutil.GenReqID("PrimCredOwn000")
	secCredID := testutil.GenReqID("SecCredOwn0000")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID
	primaryCredKey := "vtx.identity." + primaryCredID
	secCredKey := "vtx.identity." + secCredID

	seedIdentityVertex(t, ctx, conn, primaryKey, "claimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "claimed", "")
	seedCredentialBindingSingular(t, ctx, conn, primaryKey, primaryCredKey, "2025-12-01T00:00:00Z")
	seedCredentialIndexVertex(t, ctx, conn, primaryCredKey, primaryKey, "2025-12-01T00:00:00Z")
	seedCredentialBindingSingular(t, ctx, conn, secondaryKey, secCredKey, "2026-01-01T00:00:00Z")
	seedCredentialIndexVertex(t, ctx, conn, secCredKey, secondaryKey, "2026-01-01T00:00:00Z")

	edges := []string{}
	reqID := testutil.GenReqID("MrgCredPres000")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T11:03:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint: &processor.ContextHint{
			Reads:         mergeReads(primaryKey, secondaryKey, edges),
			OptionalReads: mergeCredentialOptionalReads(primaryKey, secondaryKey),
			Enumerations: []processor.EnumerationHint{
				{Hub: secondaryKey, Relation: "assignedTo", Direction: "in"},
				{Hub: secondaryKey, Relation: "indexes", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// Primary's own credential is preserved, untouched (still points at primary).
	assertCredentialIndexPointsTo(t, ctx, conn, primaryCredKey, primaryKey)
	// Secondary's credential + self-credential repoint.
	assertCredentialIndexPointsTo(t, ctx, conn, secCredKey, primaryKey)
	assertCredentialIndexPointsTo(t, ctx, conn, secondaryKey, primaryKey)

	got := primaryCredentialActorKeys(t, ctx, conn, primaryKey)
	for _, want := range []string{primaryCredKey, secCredKey, secondaryKey} {
		if !got[want] {
			t.Fatalf("primary credentials = %v, missing %s", got, want)
		}
	}

	// The singular actorKey/boundAt fields keep primary's own first-bound
	// credential -- not overwritten by the merge.
	data := readDecryptedCredentialBinding(t, ctx, conn, primaryKey)
	if a, _ := data["actorKey"].(string); a != primaryCredKey {
		t.Fatalf("primary.credentialBinding.actorKey = %q, want %s (preserved)", a, primaryCredKey)
	}
}

// --- 5. TestMergeCredentialRepoint_ChainMerge ---

// U3 -> U2 -> U1: U3's original credential and both U2/U3's self-credentials
// must all end up resolving to U1 (design §3.1: "the array is the full
// 'resolves to me' set, so no entry is ever orphaned by a second-generation
// merge").
func TestMergeCredentialRepoint_ChainMerge(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mcrchain")

	u1ID := testutil.GenReqID("ChainU1000000")
	u2ID := testutil.GenReqID("ChainU2000000")
	u3ID := testutil.GenReqID("ChainU3000000")
	c3ID := testutil.GenReqID("ChainCred3000")
	u1Key := "vtx.identity." + u1ID
	u2Key := "vtx.identity." + u2ID
	u3Key := "vtx.identity." + u3ID
	c3Key := "vtx.identity." + c3ID

	seedIdentityVertex(t, ctx, conn, u1Key, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, u2Key, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, u3Key, "claimed", "")
	seedCredentialBindingSingular(t, ctx, conn, u3Key, c3Key, "2026-01-01T00:00:00Z")
	seedCredentialIndexVertex(t, ctx, conn, c3Key, u3Key, "2026-01-01T00:00:00Z")

	// --- Merge 1: U3 (secondary) into U2 (primary) ---
	edges1 := []string{}
	reqID1 := testutil.GenReqID("MrgChain1U3U2")
	env1 := &processor.OperationEnvelope{
		RequestID:     reqID1,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T11:04:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(u2Key, u3Key, edges1),
		ContextHint: &processor.ContextHint{
			Reads:         mergeReads(u2Key, u3Key, edges1),
			OptionalReads: mergeCredentialOptionalReads(u2Key, u3Key),
			Enumerations: []processor.EnumerationHint{
				{Hub: u3Key, Relation: "assignedTo", Direction: "in"},
				{Hub: u3Key, Relation: "indexes", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	assertCredentialIndexPointsTo(t, ctx, conn, c3Key, u2Key)
	assertCredentialIndexPointsTo(t, ctx, conn, u3Key, u2Key)
	u2Got := primaryCredentialActorKeys(t, ctx, conn, u2Key)
	if !u2Got[c3Key] || !u2Got[u3Key] {
		t.Fatalf("U2 credentials = %v, want both %s and %s", u2Got, c3Key, u3Key)
	}

	// --- Merge 2: U2 (secondary) into U1 (primary) ---
	edges2 := []string{}
	reqID2 := testutil.GenReqID("MrgChain2U2U1")
	env2 := &processor.OperationEnvelope{
		RequestID:     reqID2,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T11:05:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(u1Key, u2Key, edges2),
		ContextHint: &processor.ContextHint{
			Reads:         mergeReads(u1Key, u2Key, edges2),
			OptionalReads: mergeCredentialOptionalReads(u1Key, u2Key),
			Enumerations: []processor.EnumerationHint{
				{Hub: u2Key, Relation: "assignedTo", Direction: "in"},
				{Hub: u2Key, Relation: "indexes", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// U3's original credential, chained through U2, now lands on U1.
	assertCredentialIndexPointsTo(t, ctx, conn, c3Key, u1Key)
	assertCredentialIndexPointsTo(t, ctx, conn, u3Key, u1Key)
	assertCredentialIndexPointsTo(t, ctx, conn, u2Key, u1Key)

	u1Got := primaryCredentialActorKeys(t, ctx, conn, u1Key)
	for _, want := range []string{c3Key, u3Key, u2Key} {
		if !u1Got[want] {
			t.Fatalf("U1 credentials = %v, missing %s", u1Got, want)
		}
	}
}

// --- 6. TestMergeCredentialRepoint_OverwritesExistingIndexVertex ---

// The self-credential's credentialindex key equals hash(secondary), which
// never pre-exists (a credential vertex is never itself indexed before this
// mechanism). This test instead proves the general unconditioned-update
// overwrite semantics using an ALREADY-BOUND credential: seed the
// credentialindex vertex pointing at secondary (as ClaimIdentity would have
// written it), and confirm the merge's blind Put overwrites it to primary
// rather than erroring on the pre-existing key.
func TestMergeCredentialRepoint_OverwritesExistingIndexVertex(t *testing.T) {
	ctx, conn := setupTestEnv(t)
	cp, cons := newMergePipeline(t, ctx, conn, "mcrover")

	primaryID := testutil.GenReqID("PrimCredOvr000")
	secondaryID := testutil.GenReqID("SecCredOvr0000")
	credActorID := testutil.GenReqID("CredActorOvr00")
	primaryKey := "vtx.identity." + primaryID
	secondaryKey := "vtx.identity." + secondaryID
	credActorKey := "vtx.identity." + credActorID

	seedIdentityVertex(t, ctx, conn, primaryKey, "unclaimed", "")
	seedIdentityVertex(t, ctx, conn, secondaryKey, "claimed", "")
	seedCredentialBindingSingular(t, ctx, conn, secondaryKey, credActorKey, "2026-01-01T00:00:00Z")
	// Pre-existing credentialindex vertex, still pointing at secondary.
	seedCredentialIndexVertex(t, ctx, conn, credActorKey, secondaryKey, "2026-01-01T00:00:00Z")

	edges := []string{}
	reqID := testutil.GenReqID("MrgCredOvr0000")

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "MergeIdentity",
		Actor:         operatorActorKey,
		SubmittedAt:   "2026-05-23T11:06:00Z",
		Class:         "identityHygiene",
		Payload:       mergePayload(primaryKey, secondaryKey, edges),
		ContextHint: &processor.ContextHint{
			Reads:         mergeReads(primaryKey, secondaryKey, edges),
			OptionalReads: mergeCredentialOptionalReads(primaryKey, secondaryKey),
			Enumerations: []processor.EnumerationHint{
				{Hub: secondaryKey, Relation: "assignedTo", Direction: "in"},
				{Hub: secondaryKey, Relation: "indexes", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	assertCredentialIndexPointsTo(t, ctx, conn, credActorKey, primaryKey)
}
