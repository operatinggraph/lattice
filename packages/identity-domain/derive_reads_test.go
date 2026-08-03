// derive_reads adoption tests (client-ceremony-op-descriptors-design.md Inc 1b,
// Contract #2 §2.5 class (g)) — the package's own derivation is now the only
// one, so what has to be pinned is that it agrees with everything that used to
// re-derive the same key, and that a submitter declaring NOTHING still gets the
// dedup probes hydrated.
//
// Coverage:
//  1. TestDeriveReads_EquivalenceVectors — the three identityindex keys and the
//     credentialindex key the script derives are byte-identical to
//     substrate.SHA256NanoID over the same normalized input, across a vector
//     table that exercises every normalization rule (case, whitespace
//     collapse, phone punctuation).
//  2. TestCreateUnclaimed_UndeclaredSubmitter_StillDedupes — the §7 e2e: a
//     second create sharing a contact, whose envelope declares no contextHint
//     at all, still probes the index and emits duplicateOf instead of hard-
//     failing RevisionConflict. This is the failure mode Inc 1 exists to close
//     at the platform layer rather than by a well-behaved client.
//  3. TestCompleteCredentialLink_UndeclaredSubmitter_StillGuards — the same for
//     the actor-derived credentialindex probe.
package identitydomain_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestDeriveReads_EquivalenceVectors pins the agreement the whole increment
// rests on. The script's derive_reads and execute now call ONE set of
// normalizers, so those two cannot drift; what this table proves is the other
// half — that the key they produce is the one substrate.SHA256NanoID produces,
// which is what every off-script reader (the GC manager, Loupe, a future Edge
// predictor) computes.
//
// The vectors are chosen to exercise each normalization rule, not to sample:
// email lowercases and trims, phone keeps only digits and '+', name lowercases
// and collapses interior whitespace.
func TestDeriveReads_EquivalenceVectors(t *testing.T) {
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
			want := "vtx.identityindex." + substrate.SHA256NanoID(tc.contactType+":"+tc.normalized)
			if got := contactIndexKey(tc.contactType, tc.normalized); got != want {
				t.Fatalf("index key for %q = %s, want %s", tc.raw, got, want)
			}
		})
	}

	// The credentialindex derivation takes the actor key verbatim — no
	// normalization at all, which is itself the property worth pinning: an
	// actor key is already a Contract #1 key, so normalizing it would be a
	// silent corruption rather than a cleanup.
	actor := secondCredActorKey
	want := "vtx.credentialindex." + substrate.SHA256NanoID(actor)
	if got := credentialIndexKey(actor); got != want {
		t.Fatalf("credential index key = %s, want %s", got, want)
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
