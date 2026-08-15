// Two-Phase Identity Claim (FR2, FR5) integration tests for the
// identity-domain Capability Package.
//
// Tests chain a CreateUnclaimedIdentity op (arrange) with a
// ClaimIdentity op (act) so both ops are exercised together. All
// rejections collapse to ErrCodeClaimKeyInvalid per NFR-S6
// anti-enumeration; specific outcomes surface only via Health KV
// counters (`claim-attempts.<outcome>`).
//
// Coverage:
//  1. TestClaimIdentity_Success                            — full happy path
//  2. TestClaimIdentity_WrongKey_GenericError              — wrong plaintext
//  3. TestClaimIdentity_AlreadyClaimed_GenericError        — state=claimed
//  4. TestClaimIdentity_Flagged_GenericError               — state=flagged-for-review
//  5. TestClaimIdentity_Merged_GenericError                — state=merged
//  6. TestClaimIdentity_CredentialAlreadyBound_GenericError
//  7. TestClaimIdentity_FR5_GrandfatheredFlow              — historical import
//  8. TestClaimIdentity_FR5_ImmediateAccess                — second claim blocked
//  9. TestClaimIdentity_ReClaimAfterRealClaim_GenericError — spent claimKey
//  10. TestClaimIdentity_RejectionCausesIndistinguishable   — one wire shape
//  11. TestClaimIdentity_SpentClaimKeyRefusedWithoutTheScrub — the script's own
//     tombstone guard, isolated from the Processor's scrub
package identitydomain_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/identityceremony"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const claimInstance = "icl-test"

// claimedConsumerGrantKey returns the lnk.identity.<id>.holdsRole.role.<id>
// key ClaimIdentity's R2 refinement grants the claimed identity
// (gateway-claim-flow-identity-provisioning-design.md §11.5).
func claimedConsumerGrantKey(t *testing.T, identityKey string) string {
	t.Helper()
	roleKey := consumerRoleKey(t)
	identityID := identityKey[len("vtx.identity."):]
	roleID := roleKey[len("vtx.role."):]
	return "lnk.identity." + identityID + ".holdsRole.role." + roleID
}

func newClaimPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	logger := testutil.TestLogger()
	emitter := processor.NewClaimAttemptEmitter(conn, testutil.HarnessHealthBucket, claimInstance+"-"+durable, logger)
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:      durable,
		Instance:     claimInstance + "-" + durable,
		ClaimEmitter: emitter,
	})
}

// createIdentityAndGetKeys runs CreateUnclaimedIdentity as staff and
// returns identityKey + plaintext claim key for use by a subsequent
// claim.
func createIdentityAndGetKeys(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, reqID string) (identityKey, claimKey string) {
	t.Helper()
	identityID := identityIDFromRequestID(reqID)
	identityKey = "vtx.identity." + identityID

	// Option C: the client mints the claim secret and submits only its hash.
	claimKeyPlaintext := "claim-secret-for-" + reqID
	claimKeyHash := sha256HexOf(claimKeyPlaintext)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Test Identity","email":"test@claim.example","claimKeyHash":"` + claimKeyHash + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("create: state = %q, want unclaimed", got)
	}
	return identityKey, claimKeyPlaintext
}

// readClaimHealthCounter reads a claim-attempts counter for the given outcome.
func readClaimHealthCounter(t *testing.T, ctx context.Context, conn *substrate.Conn, instance, outcome string) (count int64, found bool) {
	t.Helper()
	key := "health.processor." + instance + ".claim-attempts." + outcome
	entry, err := conn.KVGet(ctx, testutil.HarnessHealthBucket, key)
	if err != nil {
		return 0, false
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return 0, false
	}
	c, _ := doc["count"].(float64)
	return int64(c), true
}

func TestClaimIdentity_Success(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "succ")

	createReqID := testutil.GenReqID("ClmSuccCreate")
	identityKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	credIndexKey := credentialIndexKey(consumerActorKey)

	claimReqID := testutil.GenReqID("ClmSuccClaim0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + claimKeyPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed", got)
	}
	bindData := readDecryptedAspectData(t, ctx, conn, identityKey, "credentialBinding")
	if got, _ := bindData["actorKey"].(string); got != consumerActorKey {
		t.Fatalf("credentialBinding.actorKey = %q", got)
	}

	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".claimKey")
	if err != nil {
		t.Fatalf("claimKey aspect missing: %v", err)
	}
	var claimKeyDoc map[string]any
	_ = json.Unmarshal(entry.Value, &claimKeyDoc)
	if isDeleted, _ := claimKeyDoc["isDeleted"].(bool); !isDeleted {
		t.Fatalf("claimKey aspect should be tombstoned")
	}

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credIndexKey); err != nil {
		t.Fatalf("credentialindex vertex not found at %s: %v", credIndexKey, err)
	}

	grantKey := claimedConsumerGrantKey(t, identityKey)
	grantEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, grantKey)
	if err != nil {
		t.Fatalf("R2: holdsRole grant not found at %s: %v", grantKey, err)
	}
	var grantDoc struct {
		SourceVertex string `json:"sourceVertex"`
		TargetVertex string `json:"targetVertex"`
	}
	if err := json.Unmarshal(grantEntry.Value, &grantDoc); err != nil {
		t.Fatalf("unmarshal holdsRole grant: %v", err)
	}
	if grantDoc.SourceVertex != identityKey || grantDoc.TargetVertex != consumerRoleKey(t) {
		t.Fatalf("R2: holdsRole grant source/target = %q/%q, want %q/%q",
			grantDoc.SourceVertex, grantDoc.TargetVertex, identityKey, consumerRoleKey(t))
	}

	assertTrackerEvent(t, ctx, conn, claimReqID, "identity.claimed")

	instance := claimInstance + "-succ"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "success")
	if !ok {
		t.Fatalf("claim-attempts.success not found for %s", instance)
	}
	if count < 1 {
		t.Fatalf("claim-attempts.success count = %d", count)
	}
}

// TestClaimIdentity_ConsumerGrantAlreadyHeld_Succeeds covers the identity that
// was reached before it was claimed: any authenticated touch runs the Gateway's
// ProvisionConsumerIdentity pre-flight, and a seeder may grant `consumer`
// outright, so the grant link can already exist when the ceremony runs. The
// claim's grant is an upsert for that reason — as a create it asserts revision 0
// and takes the whole atomic batch with it, leaving the person permanently
// unable to claim the identity they hold the secret for.
func TestClaimIdentity_ConsumerGrantAlreadyHeld_Succeeds(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "pregrant")

	createReqID := testutil.GenReqID("ClmPreGrantCrt")
	identityKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	// The state the pre-flight leaves behind on an unclaimed identity.
	testutil.SeedHoldsRole(t, ctx, conn, identityKey, consumerRoleKey(t))

	claimReqID := testutil.GenReqID("ClmPreGrantClm")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-07-27T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + claimKeyPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The whole batch had to land, not just the grant: the ceremony's real work
	// is the state transition + the credential binding.
	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("state = %q, want claimed", got)
	}
	bindData := readDecryptedAspectData(t, ctx, conn, identityKey, "credentialBinding")
	if got, _ := bindData["actorKey"].(string); got != consumerActorKey {
		t.Fatalf("credentialBinding.actorKey = %q, want %q", got, consumerActorKey)
	}
	if grantIsDeleted(t, ctx, conn, claimedConsumerGrantKey(t, identityKey)) {
		t.Fatalf("the claimed identity must still hold a live consumer grant")
	}
}

func TestClaimIdentity_WrongKey_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "wrkey")

	createReqID := testutil.GenReqID("ClmWrKeyCreate")
	identityKey, _ := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	credIndexKey := credentialIndexKey(consumerActorKey)

	claimReqID := testutil.GenReqID("ClmWrKeyClaim0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"garbage-wrong-key-12345","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("state mutated: %q, want unclaimed", got)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credIndexKey); err == nil {
		t.Fatalf("credentialindex should NOT exist after wrong-key")
	}
	instance := claimInstance + "-wrkey"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "invalid-key")
	if !ok {
		t.Fatalf("claim-attempts.invalid-key not found")
	}
	if count < 1 {
		t.Fatalf("invalid-key count = %d", count)
	}
}

// TestClaimIdentity_AlreadyClaimed_GenericError seeds the shape a completed
// claim really leaves: state=claimed AND a TOMBSTONED `.claimKey`. Both halves
// matter — the aspect is sensitive-classed, so a read path that refuses a
// tombstoned sensitive doc fails the operation in step 4 hydrate and renders
// an internal error, never the script's own generic rejection, which makes
// "already claimed" distinguishable from "wrong key" on the wire.
func TestClaimIdentity_AlreadyClaimed_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "alrcl")

	identityID := testutil.GenReqID("PreClaimedIdnt")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "claimed", "")
	seedSpentClaimKeyAspect(t, ctx, conn, identityKey, "dummy-hash-value")

	claimReqID := testutil.GenReqID("ClmAlrClClaim0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"any-key","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint:   identityceremony.ClaimContextHint(identityKey),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, claimEnv)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", outcome)
	}
	assertGenericClaimRejection(t, reply)

	// The counter is what proves the script actually RAN and took its own
	// wrong-state branch. Without it a hydrate fault satisfies "rejected" just
	// as well, and the assertion above would be the only thing standing
	// between this test and a vacuous pass.
	instance := claimInstance + "-alrcl"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "wrong-state")
	if !ok {
		t.Fatalf("claim-attempts.wrong-state not found — the script never reached its own gate")
	}
	if count < 1 {
		t.Fatalf("wrong-state count = %d", count)
	}
}

// hardenedClaimHint is the declaration a HOSTILE submitter sends: the three
// target-derived keys under `reads`, the fail-closed disposition, hand-rolled
// rather than built. No shipped client produces this; nothing in the transport
// can refuse it. It exists so the tests can send the exact probe the fix has to
// answer, and it must never be wired into a dispatcher.
func hardenedClaimHint(targetKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads: []string{targetKey, targetKey + ".state", targetKey + ".claimKey"},
	}
}

// assertGenericClaimRejection pins NFR-S6's wire shape: the generic code, no
// details map, and no outcome word anywhere in the message. Every claim
// rejection cause must be indistinguishable through this surface.
func assertGenericClaimRejection(t *testing.T, reply *processor.OperationReply) {
	t.Helper()
	if reply == nil || reply.Error == nil {
		t.Fatalf("reply = %+v, want a rejection error", reply)
	}
	if reply.Error.Code != processor.ErrCodeClaimKeyInvalid {
		// Details are printed because they are the payload that makes a
		// divergent code an oracle: HydrationFailed carries `missingKey`, and
		// the probed key coming back is the whole enumeration primitive.
		t.Fatalf("error code = %q (details %+v), want %q — every claim rejection collapses to the one generic code",
			reply.Error.Code, reply.Error.Details, processor.ErrCodeClaimKeyInvalid)
	}
	if len(reply.Error.Details) != 0 {
		t.Fatalf("error details = %+v, want empty", reply.Error.Details)
	}
	for _, outcome := range []string{
		"invalid-key", "no-target", "wrong-state", "flagged", "merged", "erased",
		"credential-already-bound", "credential-not-provisioned",
	} {
		if strings.Contains(reply.Error.Message, outcome) {
			t.Fatalf("error message %q leaks the outcome word %q", reply.Error.Message, outcome)
		}
	}
}

// TestClaimIdentity_ReClaimAfterRealClaim_GenericError drives the whole
// ceremony — create, claim, re-claim — because the tombstone the re-claim
// trips over is written by the first claim itself, not by a fixture. The
// re-claim declares `.claimKey`, the first claim tombstoned it, and the aspect
// is sensitive-classed. That triple must clear step 4 hydrate: the ceremony
// owes the generic rejection its own script produces, never an internal error
// raised before the script runs.
func TestClaimIdentity_ReClaimAfterRealClaim_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "reclm")

	createReqID := testutil.GenReqID("ClmReclmCreate")
	identityKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)
	hint := identityceremony.ClaimContextHint(identityKey)
	hint.OptionalReads = append(hint.OptionalReads, credentialIndexKey(consumerActorKey))

	claimEnv := func(reqID, submittedAt string) *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "ClaimIdentity",
			Actor:         consumerActorKey,
			SubmittedAt:   submittedAt,
			Class:         "identity",
			Payload:       json.RawMessage(`{"claimKey":"` + claimKeyPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
			AuthContext:   &processor.AuthContext{Target: consumerActorKey},
			ContextHint:   hint,
		}
	}

	testutil.PublishOp(t, conn, claimEnv(testutil.GenReqID("ClmReclmFirst0"), "2026-05-22T10:01:00Z"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// The tombstone RETAINS the prior document, so the spent hash is still
	// sitting at the key the re-claim is about to declare. Assert the hazard
	// rather than assume it: if the claim ever hard-deleted the aspect, the
	// re-claim below would prove nothing about the tombstoned-read path.
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".claimKey")
	if err != nil {
		t.Fatalf("read %s.claimKey after the first claim: %v", identityKey, err)
	}
	var spent map[string]any
	if err := json.Unmarshal(entry.Value, &spent); err != nil {
		t.Fatalf("unmarshal spent claimKey: %v", err)
	}
	if deleted, _ := spent["isDeleted"].(bool); !deleted {
		t.Fatalf("claimKey isDeleted = false after a successful claim; doc = %+v", spent)
	}
	if body, _ := spent["data"].(map[string]any); len(body) == 0 {
		t.Fatalf("claimKey tombstone carries no body; the retained-document hazard this test rides is gone: %+v", spent)
	}

	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		claimEnv(testutil.GenReqID("ClmReclmSecond"), "2026-05-22T10:02:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("re-claim outcome = %q, want rejected", outcome)
	}
	assertGenericClaimRejection(t, reply)

	instance := claimInstance + "-reclm"
	if count, ok := readClaimHealthCounter(t, ctx, conn, instance, "wrong-state"); !ok || count < 1 {
		t.Fatalf("claim-attempts.wrong-state = %d (found=%v) — the re-claim never reached the script's own gate", count, ok)
	}
}

// TestClaimIdentity_RejectionCausesIndistinguishable is the anti-enumeration
// residual: three structurally different rejection causes, one of them a
// tombstoned-`.claimKey` read, answered with the identical wire shape. The
// failure mode it guards is a cause that faults EARLIER in the pipeline than
// its siblings and so renders a different code — which tells a caller which
// cause it hit.
func TestClaimIdentity_RejectionCausesIndistinguishable(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "indst")

	// A target that was never provisioned at all.
	absentKey := "vtx.identity." + testutil.GenReqID("IndstAbsent000")

	// A live unclaimed identity whose secret the caller does not hold.
	wrongKeyKey := "vtx.identity." + testutil.GenReqID("IndstWrongKey0")
	seedDirectIdentity(t, ctx, conn, wrongKeyKey, "unclaimed", "")
	seedClaimKeyAspect(t, ctx, conn, wrongKeyKey, sha256HexOf("indistinguishable-real-secret"))

	// A live unclaimed identity whose claimKey has already been spent.
	spentKey := "vtx.identity." + testutil.GenReqID("IndstSpentKey0")
	seedDirectIdentity(t, ctx, conn, spentKey, "unclaimed", "")
	seedSpentClaimKeyAspect(t, ctx, conn, spentKey, sha256HexOf("indistinguishable-spent-secret"))

	cases := []struct {
		name    string
		reqID   string
		target  string
		secret  string
		outcome string
		// hint overrides the shipped builder for the hostile arms. nil means
		// "submit exactly what a conforming dispatcher sends".
		hint func(target string) *processor.ContextHint
	}{
		{"no-target", testutil.GenReqID("IndstNoTarget0"), absentKey, "irrelevant-secret", "no-target", nil},
		{"wrong-key", testutil.GenReqID("IndstWrongSbmt"), wrongKeyKey, "not-the-real-secret", "invalid-key", nil},
		{"spent-key", testutil.GenReqID("IndstSpentSbmt"), spentKey, "indistinguishable-spent-secret", "invalid-key", nil},
		// A target that fails the Contract #1 grammar. Right prefix, so the
		// script's own `startswith` guard would accept it and its `no-target`
		// branch is what should render — but a MALFORMED key declared in a
		// ContextHint is rejected by step 4 as InvalidReadKey before the
		// script runs at all. It reaches the script's branch only because the
		// shared builder declines to declare a key the grammar rejects.
		{"malformed-target", testutil.GenReqID("IndstMalformed"), "vtx.identity.x", "irrelevant-secret", "no-target", nil},
		// The hostile arms. These bypass the shared builder entirely and
		// hand-roll the declaration a conforming client no longer sends —
		// `reads`, the fail-closed disposition, on the three target-derived
		// keys. That is the exact probe that reproduced the oracle: under
		// `reads` an absent target is required-absent, the script's first
		// `target in state` faults HydrationMiss, and the reply comes back
		// HydrationFailed with the probed key in details.missingKey.
		//
		// Nothing in the transport can stop this envelope being sent —
		// contextHint is client-supplied and step 3 never inspects it — so the
		// only thing that can make it answer like every other cause is the
		// descriptor floor (Contract #2 §2.5) demoting it Processor-side.
		{"hand-rolled-reads-absent", testutil.GenReqID("IndstHostAbsnt"), absentKey, "irrelevant-secret", "no-target", hardenedClaimHint},
		{"hand-rolled-reads-wrong-key", testutil.GenReqID("IndstHostWrong"), wrongKeyKey, "not-the-real-secret", "invalid-key", hardenedClaimHint},
	}

	var shapes []string
	for _, tc := range cases {
		// Default: the identical shipped builder every live dispatcher calls,
		// including for the absent and malformed targets. Hand-tailoring a
		// conforming arm's declaration would hide the failure this test exists
		// to catch — a submitter cannot know in advance which of its keys exist
		// or parse, so the declaration it ALWAYS sends is the one worth testing.
		hint := identityceremony.ClaimContextHint(tc.target)
		if tc.hint != nil {
			hint = tc.hint(tc.target)
		}
		env := &processor.OperationEnvelope{
			RequestID:     tc.reqID,
			Lane:          processor.LaneDefault,
			OperationType: "ClaimIdentity",
			Actor:         consumerActorKey,
			SubmittedAt:   "2026-05-22T10:03:00Z",
			Class:         "identity",
			Payload:       json.RawMessage(`{"claimKey":"` + tc.secret + `","targetIdentityKey":"` + tc.target + `"}`),
			AuthContext:   &processor.AuthContext{Target: consumerActorKey},
			ContextHint:   hint,
		}
		outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
		if outcome != processor.OutcomeRejected {
			t.Fatalf("%s: outcome = %q, want rejected", tc.name, outcome)
		}
		assertGenericClaimRejection(t, reply)
		shapes = append(shapes, fmt.Sprintf("%s|%d", reply.Error.Code, len(reply.Error.Details)))

		instance := claimInstance + "-indst"
		if count, ok := readClaimHealthCounter(t, ctx, conn, instance, tc.outcome); !ok || count < 1 {
			t.Fatalf("%s: claim-attempts.%s = %d (found=%v) — the cause never reached the script's own gate, so the wire-shape match is vacuous",
				tc.name, tc.outcome, count, ok)
		}
	}
	for i := 1; i < len(shapes); i++ {
		if shapes[i] != shapes[0] {
			t.Fatalf("rejection shapes differ: %q vs %q (%v)", shapes[0], shapes[i], shapes)
		}
	}
}

// TestClaimIdentity_SpentClaimKeyRefusedWithoutTheScrub isolates the script's
// OWN tombstone guard (`claim_key_aspect ... isDeleted` → invalid-key) from the
// Processor's scrub of a deleted sensitive body.
//
// The two defences are independent and both are load-bearing, but every other
// spent-key vector conflates them: the scrub empties the body, so the guard
// below it ("hash" not in data) refuses anyway and the tombstone clause could
// be deleted with no test noticing. Here the aspect's class resolves to no
// sensitive DDL, so the scrub does not run and the retained {hash} body
// survives into the script intact — the shape a DDL-cache miss produces, the
// same race step 6.5 refuses to guess at. The submitted secret is the CORRECT
// one for that retained hash, so if the script consulted a spent key's body
// the claim would be ACCEPTED.
func TestClaimIdentity_SpentClaimKeyRefusedWithoutTheScrub(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "spentg")

	identityKey := "vtx.identity." + testutil.GenReqID("SpentGuardIdnt")
	seedDirectIdentity(t, ctx, conn, identityKey, "unclaimed", "")

	const secret = "spent-guard-plaintext-secret-001"
	tombstone := map[string]any{
		// A class no aspectType DDL registers, so decryptSensitiveDoc resolves
		// it to nothing and leaves the body untouched.
		"class":     "claimKeyUnresolvedClass",
		"vertexKey": identityKey,
		"localName": "claimKey",
		"isDeleted": true,
		"data":      map[string]any{"hash": sha256HexOf(secret), "algo": "sha256"},
	}
	b, _ := json.Marshal(tombstone)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".claimKey", b); err != nil {
		t.Fatalf("seed unscrubbed spent claimKey: %v", err)
	}

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("SpentGuardClm0"),
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:04:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + secret + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint:   identityceremony.ClaimContextHint(identityKey),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected — a spent claim key must not be honoured even when its body survives", outcome)
	}
	assertGenericClaimRejection(t, reply)

	// invalid-key, and specifically NOT wrong-state: the identity is still
	// unclaimed, so the run genuinely descended past the state gates and the
	// claimKey guard is the branch that fired.
	instance := claimInstance + "-spentg"
	if count, ok := readClaimHealthCounter(t, ctx, conn, instance, "invalid-key"); !ok || count < 1 {
		t.Fatalf("claim-attempts.invalid-key = %d (found=%v) — the run never reached the claimKey guard", count, ok)
	}
	if count, ok := readClaimHealthCounter(t, ctx, conn, instance, "wrong-state"); ok && count > 0 {
		t.Fatalf("claim-attempts.wrong-state = %d — the run stopped at a state gate, so it never exercised the claimKey guard", count)
	}
	// Nothing was bound: the refusal is a refusal, not a rejection reply over
	// a committed batch.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credentialIndexKey(consumerActorKey)); err == nil {
		t.Fatalf("a credentialindex exists — the spent key was honoured")
	}
	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("state = %q, want unclaimed", got)
	}
}

func TestClaimIdentity_Flagged_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "flagged")

	identityID := testutil.GenReqID("FlaggedIdentit")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "flagged-for-review", "")
	seedClaimKeyAspect(t, ctx, conn, identityKey, "dummy-hash-value")

	claimReqID := testutil.GenReqID("ClmFlagClaim00")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"any-key","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	instance := claimInstance + "-flagged"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "flagged")
	if !ok {
		t.Fatalf("claim-attempts.flagged not found")
	}
	if count < 1 {
		t.Fatalf("flagged count = %d", count)
	}
}

func TestClaimIdentity_Merged_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "merged")

	identityID := testutil.GenReqID("MergedIdentity")
	identityKey := "vtx.identity." + identityID
	mergedIntoKey := "vtx.identity." + testutil.GenReqID("MergedIntoIdnt")
	seedDirectIdentity(t, ctx, conn, identityKey, "merged", mergedIntoKey)
	seedClaimKeyAspect(t, ctx, conn, identityKey, "dummy-hash-value")

	claimReqID := testutil.GenReqID("ClmMergdClaim0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"any-key","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
				identityKey + ".mergedInto",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	outcome := testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("NFR-S6: merged identity must surface as generic Rejected, got %q", outcome)
	}

	instance := claimInstance + "-merged"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "merged")
	if !ok {
		t.Fatalf("claim-attempts.merged not found")
	}
	if count < 1 {
		t.Fatalf("merged count = %d", count)
	}
	_ = time.Now
}

func TestClaimIdentity_CredentialAlreadyBound_GenericError(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "credbnd")

	credIndexKey := credentialIndexKey(consumerActorKey)
	priorIdentityKey := "vtx.identity." + testutil.GenReqID("PriorBoundIdnt")
	credIdxDoc := map[string]any{
		"class":     "credentialindex",
		"isDeleted": false,
		"data": map[string]any{
			"actorKey":    consumerActorKey,
			"identityKey": priorIdentityKey,
			"boundAt":     "2026-05-22T09:00:00Z",
		},
	}
	b, _ := json.Marshal(credIdxDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, credIndexKey, b); err != nil {
		t.Fatalf("seed credentialindex: %v", err)
	}

	identityID := testutil.GenReqID("SecndIdentity0")
	identityKey := "vtx.identity." + identityID
	seedDirectIdentity(t, ctx, conn, identityKey, "unclaimed", "")
	claimKeyHash := sha256HexOf("the-real-key-12345678901234567890")
	seedClaimKeyAspect(t, ctx, conn, identityKey, claimKeyHash)

	claimReqID := testutil.GenReqID("ClmCredBndClm0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"the-real-key-12345678901234567890","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
				credIndexKey,
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	instance := claimInstance + "-credbnd"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "credential-already-bound")
	if !ok {
		t.Fatalf("claim-attempts.credential-already-bound not found")
	}
	if count < 1 {
		t.Fatalf("credential-already-bound count = %d", count)
	}
}

func TestClaimIdentity_FR5_GrandfatheredFlow(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "fr5gf")

	identityID := testutil.GenReqID("FR5GrandFathrd")
	identityKey := "vtx.identity." + identityID

	grandPlaintext := "grandfathered-claim-key-1234567"
	grandHash := sha256HexOf(grandPlaintext)

	// Historical-import shape: minimal vertex + state aspect + claimKey aspect.
	vtxDoc := map[string]any{
		"class":     "identity",
		"isDeleted": false,
		"createdAt": "2024-01-01T00:00:00Z",
		"createdBy": "system.legacy-import",
		"data":      map[string]any{},
	}
	vb, _ := json.Marshal(vtxDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey, vb); err != nil {
		t.Fatalf("seed grandfathered vertex: %v", err)
	}
	stateDoc := map[string]any{
		"class": "state", "vertexKey": identityKey, "localName": "state",
		"isDeleted": false, "data": map[string]any{"value": "unclaimed"},
	}
	sb, _ := json.Marshal(stateDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, identityKey+".state", sb); err != nil {
		t.Fatalf("seed state aspect: %v", err)
	}
	seedClaimKeyAspect(t, ctx, conn, identityKey, grandHash)

	credIndexKey := credentialIndexKey(consumerActorKey)
	claimReqID := testutil.GenReqID("ClmFR5GFClaim0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + grandPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("FR5 GF: state = %q, want claimed", got)
	}
	bindData := readDecryptedAspectData(t, ctx, conn, identityKey, "credentialBinding")
	if got, _ := bindData["actorKey"].(string); got != consumerActorKey {
		t.Fatalf("FR5 GF: credentialBinding.actorKey = %q", got)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, credIndexKey); err != nil {
		t.Fatalf("FR5 GF: credentialindex missing: %v", err)
	}

	// Confirm provenance: createdBy on the grandfathered vertex is the
	// legacy-import marker (proves the createdByOp on the .credentialBinding
	// + .state mutations is the new ClaimIdentity op's tracker — not the
	// grandfathered vertex's).
	vtxEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey)
	if err != nil {
		t.Fatalf("read identity vertex: %v", err)
	}
	var rawVtx map[string]any
	_ = json.Unmarshal(vtxEntry.Value, &rawVtx)
	if got, _ := rawVtx["createdBy"].(string); got != "system.legacy-import" {
		t.Fatalf("FR5: expected createdBy=system.legacy-import, got %q", got)
	}
}

func TestClaimIdentity_FR5_ImmediateAccess(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newClaimPipeline(t, ctx, conn, "fr5ia")

	createReqID := testutil.GenReqID("FR5IACreate00")
	identityKey, claimKeyPlaintext := createIdentityAndGetKeys(t, ctx, conn, cp, cons, createReqID)

	credIndexKey := credentialIndexKey(consumerActorKey)
	claimReqID1 := testutil.GenReqID("FR5IAClaim0001")
	claimEnv1 := &processor.OperationEnvelope{
		RequestID:     claimReqID1,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:01:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + claimKeyPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "claimed" {
		t.Fatalf("FR5 IA: first claim state = %q", got)
	}

	// Second claim against a different unclaimed identity by same consumer:
	// must fail with credential-already-bound.
	identity2ID := testutil.GenReqID("FR5IAIdent2000")
	identity2Key := "vtx.identity." + identity2ID
	secondPlaintext := "fr5-second-claim-key-12345678901"
	secondHash := sha256HexOf(secondPlaintext)
	seedDirectIdentity(t, ctx, conn, identity2Key, "unclaimed", "")
	seedClaimKeyAspect(t, ctx, conn, identity2Key, secondHash)

	claimReqID2 := testutil.GenReqID("FR5IAClaim0002")
	claimEnv2 := &processor.OperationEnvelope{
		RequestID:     claimReqID2,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:02:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + secondPlaintext + `","targetIdentityKey":"` + identity2Key + `"}`),
		AuthContext:   &processor.AuthContext{Target: consumerActorKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identity2Key,
				identity2Key + ".state",
				identity2Key + ".claimKey",
				credIndexKey,
			},
		},
	}
	testutil.PublishOp(t, conn, claimEnv2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	instance := claimInstance + "-fr5ia"
	count, ok := readClaimHealthCounter(t, ctx, conn, instance, "credential-already-bound")
	if !ok {
		t.Fatalf("FR5 IA: credential-already-bound not found")
	}
	if count < 1 {
		t.Fatalf("FR5 IA: credential-already-bound count = %d", count)
	}
}
