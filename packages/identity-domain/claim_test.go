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
//  12. TestNFRS6Ops_DescriptorFloorSetPinned                — the exact Contract
//     #2 §2.5 floor each NFR-S6 op declares, plus the census that says which
//     ops are in that class
package identitydomain_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/identityceremony"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	"github.com/operatinggraph/lattice/internal/vault"
	identitydomain "github.com/operatinggraph/lattice/packages/identity-domain"
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

// flooredTargetKeys resolves an op descriptor's Dispatch.OptionalReads — the
// Contract #2 §2.5 floor the Processor applies over a submitter's envelope
// (internal/processor/descriptor_floor.go) — against one concrete target key.
//
// The hostile-envelope tests below declare these keys under `reads`, the
// fail-closed disposition, and the floor is the only thing that can demote
// them. Reading the list from the descriptor rather than restating it is what
// keeps the probe set and the enforcement point from drifting apart: a key
// added to the floor is probed on the next run, and a key removed from it
// fails these tests instead of silently narrowing them.
func flooredTargetKeys(t *testing.T, opType, targetKey string) []string {
	t.Helper()
	const param = "{payload.targetIdentityKey}"
	for _, m := range identitydomain.OpMetas() {
		if m.OperationType != opType {
			continue
		}
		if m.Dispatch == nil || len(m.Dispatch.OptionalReads) == 0 {
			t.Fatalf("%s: descriptor declares no Dispatch.OptionalReads — the §2.5 floor these tests probe does not exist", opType)
		}
		out := make([]string, 0, len(m.Dispatch.OptionalReads))
		for _, tmpl := range m.Dispatch.OptionalReads {
			if !strings.HasPrefix(tmpl, param) {
				// Fail closed: an unrecognized template would silently drop a
				// floored key out of the probe set, which is the exact gap
				// this helper exists to close.
				t.Fatalf("%s: unhandled floor template %q — teach this helper the shape before declaring it", opType, tmpl)
			}
			out = append(out, targetKey+strings.TrimPrefix(tmpl, param))
		}
		return out
	}
	t.Fatalf("%s: no descriptor in OpMetas()", opType)
	return nil
}

// nfrS6FlooredKeys pins the exact Contract #2 §2.5 floor each NFR-S6 op
// declares, independently of the descriptor it is checked against.
//
// flooredTargetKeys READS the descriptor, so it proves every declared key is
// really demoted but moves in lockstep with the declaration — it cannot notice
// the declaration shrinking. Shrinking it is precisely the edit opmetas.go
// warns about: removing an entry, or moving one to Dispatch.Reads, re-opens the
// identity-keyspace oracle for every caller, not just for clients that read the
// descriptor. Changing this table is therefore a deliberate act that has to be
// argued in the commit, never re-pinned to match a descriptor someone narrowed.
var nfrS6FlooredKeys = map[string][]string{
	"ClaimIdentity": {
		"{payload.targetIdentityKey}",
		"{payload.targetIdentityKey}.state",
		"{payload.targetIdentityKey}.claimKey",
	},
	"CompleteCredentialLink": {
		"{payload.targetIdentityKey}",
		"{payload.targetIdentityKey}.state",
		"{payload.targetIdentityKey}.linkKey",
		"{payload.targetIdentityKey}.credentialBinding",
	},
}

// TestNFRS6Ops_DescriptorFloorSetPinned guards what flooredTargetKeys cannot:
// that the floor each NFR-S6 op declares has not been narrowed. The hostile-
// envelope tests prove every key the descriptor floors is really demoted, but
// they derive their probe FROM the descriptor, so a shrunk or relocated
// declaration shrinks the probe right along with it and nothing fails. This
// test compares the descriptor against the independent list above instead.
//
// The comparison is EXACT — element for element, in order, the same line
// package_test.go takes for the sibling op. A count plus set-membership is not
// equality: a duplicated entry keeps the count while a real key silently drops
// out of the floor, which is precisely the edit this test has to catch.
//
// It also asserts Dispatch.Reads is empty for both ops. Not because a required
// template would out-rank the optional one — for THESE ops it would not. Every
// template here resolves from `{payload.targetIdentityKey}`, and
// resolveDescriptorRequired (internal/processor/descriptor_floor.go) refuses to
// build an exclusion out of a submitter-derived template, so a key named by
// both lists is still demoted; an exclusion set the attacker can address is a
// bypass, not a precedence rule. The reason the list must stay empty is the
// ceremony itself: it adjudicates an absent target with its own generic
// outcome, and a fail-closed read is the opposite of that — it faults
// HydrationMiss carrying the probed key before the script can answer at all.
func TestNFRS6Ops_DescriptorFloorSetPinned(t *testing.T) {
	enrolled := make([]string, 0, len(nfrS6FlooredKeys))
	for opType := range nfrS6FlooredKeys {
		enrolled = append(enrolled, opType)
	}
	// Sorted so a run that breaks both ops names both, in the same order every
	// time; ranging the map directly reports an arbitrary one.
	slices.Sort(enrolled)

	for _, opType := range enrolled {
		want := nfrS6FlooredKeys[opType]
		t.Run(opType, func(t *testing.T) {
			var found *pkgmgr.OpDispatchSpec
			for _, m := range identitydomain.OpMetas() {
				if m.OperationType == opType {
					found = m.Dispatch
					break
				}
			}
			if found == nil {
				t.Fatalf("%s: no descriptor in OpMetas(), or its Dispatch spec is nil", opType)
			}
			if !slices.Equal(found.OptionalReads, want) {
				t.Fatalf("%s: Dispatch.OptionalReads = %v, want exactly %v — narrowing this set re-opens the identity-keyspace oracle for every caller, not just for clients that read the descriptor",
					opType, found.OptionalReads, want)
			}
			if len(found.Reads) != 0 {
				t.Fatalf("%s: Dispatch.Reads = %v, want empty — this ceremony adjudicates an absent target with its own generic outcome, and a fail-closed read faults HydrationMiss carrying the probed key before it can",
					opType, found.Reads)
			}
		})
	}

	// The census. nfrS6FlooredKeys is a hand list and nothing else ties it to
	// "the ops that answer with NFR-S6's one generic code". Membership in that
	// class is a property of the SCRIPT: an op joins by calling
	// fail("ClaimKeyInvalid: " + outcome), the message shape the Processor's
	// classifier collapses to ErrCodeClaimKeyInvalid. Counting those call sites
	// in the package's own script source is what makes a third op audible here
	// instead of shipping with no floor, no pin, and no failure.
	t.Run("enrolment-census", func(t *testing.T) {
		src, err := os.ReadFile("ddls.go")
		if err != nil {
			t.Fatalf("read the package's script source: %v", err)
		}
		const failCall = `fail("ClaimKeyInvalid: "`
		got := strings.Count(string(src), failCall)
		if got != len(nfrS6FlooredKeys) {
			t.Fatalf("ddls.go has %d `%s` call sites but %d ops are enrolled in nfrS6FlooredKeys (%v) — an op that answers with NFR-S6's generic code must be given a Contract #2 §2.5 descriptor floor and enrolled here, or its absent-target case is an identity-keyspace oracle",
				got, failCall, len(nfrS6FlooredKeys), enrolled)
		}
	})
}

// hardenedClaimHint is the declaration a HOSTILE submitter sends: the
// target-derived keys the descriptor floors, under `reads`, the fail-closed
// disposition. It bypasses the shipped client builder
// (identityceremony.ClaimContextHint), which declares the same keys under the
// absence-tolerant disposition; nothing in the transport can refuse the
// hardened form. It exists so the tests can send the exact probe the floor has
// to answer, and it must never be wired into a dispatcher.
func hardenedClaimHint(t *testing.T, targetKey string) *processor.ContextHint {
	return &processor.ContextHint{Reads: flooredTargetKeys(t, "ClaimIdentity", targetKey)}
}

// hostileUnflooredReadsHint is the probe the DECLARATION-side checks cannot
// answer: the floored target-derived keys PLUS two the descriptor does not
// floor — `.erasureRequested` and `.mergedInto`, both derived from the same
// submitter-named target — all under `reads`.
//
// It pins the SCRIPT-side surface rather than the declaration. An unfloored
// `reads` key stays required-absent, and internal/processor/starlark_kv.go
// defers its HydrationMiss until the script TOUCHES the key, so what a hostile
// submitter can actually observe is {target-derived keys the script reads
// before its generic rejection} — which the descriptor's floor covers only for
// as long as those two sets agree. Both keys are absent for every target these
// arms use, live or not, so a touch anywhere above the rejection renders
// HydrationFailed with the probed key instead of the one generic code.
//
// ClaimIdentity's script reaches neither key before it answers: it carries no
// `.mergedInto` guard (state "merged" is the branch it takes), and its
// `write_path_closed` erasure gate sits BELOW the secret comparison, which a
// caller holding no valid secret never passes. Lift that gate above the
// comparison, or re-add a `mergedInto` guard, and the surface is open again
// with every descriptor-vs-descriptor check still green; these arms are what
// fail then.
func hostileUnflooredReadsHint(t *testing.T, targetKey string) *processor.ContextHint {
	t.Helper()
	reads := flooredTargetKeys(t, "ClaimIdentity", targetKey)
	reads = append(reads, targetKey+".erasureRequested", targetKey+".mergedInto")
	return &processor.ContextHint{Reads: reads}
}

// assertGenericClaimRejection pins NFR-S6's wire shape: the generic code, no
// details map, and no outcome word anywhere in the message. Every claim
// rejection cause must be indistinguishable through this surface.
// submitAndTimeRejection submits env through the real pipeline and returns the
// reply plus the interval from just before submission to the reply's arrival.
//
// This is the ONLY place the release quantizer
// (internal/processor/claim_reply_floor.go) is exercised on the path production
// actually takes. Every unit-tier test of the mechanism injects its failure
// through a stub Hydrator — step 4 — but no real ClaimKeyInvalid is ever minted
// there: it comes from classifyScriptError parsing the script's
// fail("ClaimKeyInvalid: " + outcome) (identity-domain/ddls.go), which runs
// inside the Executor at step 5. A regression that un-anchored the step-5
// callsite alone would leave every one of those unit tests green.
//
// Measuring from before submission is the conservative direction: t0 precedes
// the receipt instant dispatch captures, so an observed interval that clears the
// quantum bounds the real receipt-to-publish offset from below.
func submitAndTimeRejection(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, env *processor.OperationEnvelope) (processor.MessageOutcome, *processor.OperationReply, time.Duration) {
	t.Helper()
	t0 := time.Now()
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	return outcome, reply, time.Since(t0)
}

// assertRejectionQuantized fails when a rejection of an NFR-S6 operation came
// back before its first release boundary. `elapsed` must come from
// submitAndTimeRejection.
func assertRejectionQuantized(t *testing.T, label string, elapsed time.Duration) {
	t.Helper()
	if elapsed < processor.DefaultClaimRejectionFloor {
		t.Fatalf("%s: rejection answered after %s, before the %s NFR-S6 release quantum — "+
			"the reply is being published at its own service time, which is what makes the "+
			"rejection causes separable in the time domain",
			label, elapsed, processor.DefaultClaimRejectionFloor)
	}
}

// slowDecryptVault wraps a real Vault and spends a fixed interval on every
// Decrypt, so a test can make one rejection cause do substantially more work
// than another WITHOUT leaving the deployed code path — the script still runs,
// the aspect is still real ciphertext, and the decrypt still really happens.
//
// The interval is not synchronising anything; its duration is the quantity
// under test.
type slowDecryptVault struct {
	vault.Vault
	work time.Duration
}

func (v slowDecryptVault) Decrypt(ctx context.Context, keyHolderKey string, env vault.Envelope, ct vault.Ciphertext) ([]byte, error) {
	time.Sleep(v.work)
	return v.Vault.Decrypt(ctx, keyHolderKey, env, ct)
}

// TestClaimIdentity_RejectionReleaseIsAnchoredAtReceipt is the end-to-end proof
// that the NFR-S6 release quantizer is anchored at message ARRIVAL, on the path
// production takes.
//
// It compares two real causes whose work genuinely differs: `wrong-key` reads a
// LIVE `.claimKey` aspect, so step 4 runs the envelope read and the AEAD decrypt
// (inflated here by slowDecryptVault), while `absent-target` finds nothing to
// decrypt at all. Anchored at receipt, both are answered on the same lattice
// point and the difference collapses to publish jitter. Anchored anywhere later
// — at the step-5 callsite, or at a receipt captured below dispatch's prologue —
// the decrypt time reappears in the gap, which is precisely the oracle.
//
// A lower-bound "not before the quantum" assertion cannot see this: anchoring
// late makes a reply LATER, never earlier. Only the comparison catches it.
func TestClaimIdentity_RejectionReleaseIsAnchoredAtReceipt(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)

	// The quantum is set well above the injected work so both causes land in the
	// FIRST quantum however many times the path decrypts — the assertion must
	// not depend on that count.
	const quantum = 400 * time.Millisecond
	const decryptWork = 150 * time.Millisecond
	// Far below decryptWork, far above goroutine wakeup plus a NATS round trip.
	const tolerance = 60 * time.Millisecond

	instance := claimInstance + "-anchor"
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:             "clm-anchor",
		Instance:            instance,
		ClaimEmitter:        processor.NewClaimAttemptEmitter(conn, testutil.HarnessHealthBucket, instance, testutil.TestLogger()),
		Vault:               slowDecryptVault{Vault: testutil.TestVault(t), work: decryptWork},
		ClaimRejectionFloor: quantum,
	})

	// A live, unclaimed identity with a LIVE claimKey aspect: the cause that
	// really decrypts.
	wrongKeyTarget := "vtx.identity." + testutil.GenReqID("ClmAnchorLive0")
	seedDirectIdentity(t, ctx, conn, wrongKeyTarget, "unclaimed", "")
	seedClaimKeyAspect(t, ctx, conn, wrongKeyTarget, sha256HexOf("the-real-anchor-secret"))

	// A target that was never provisioned: nothing to read, nothing to decrypt.
	absentTarget := "vtx.identity." + testutil.GenReqID("ClmAnchorGone0")

	claimAt := func(reqID, target, secret string) time.Duration {
		t.Helper()
		env := &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "ClaimIdentity",
			Actor:         consumerActorKey,
			SubmittedAt:   "2026-05-22T10:05:00Z",
			Class:         "identity",
			Payload:       json.RawMessage(`{"claimKey":"` + secret + `","targetIdentityKey":"` + target + `"}`),
			AuthContext:   &processor.AuthContext{Target: consumerActorKey},
			ContextHint:   identityceremony.ClaimContextHint(target),
		}
		outcome, reply, elapsed := submitAndTimeRejection(t, ctx, conn, cp, cons, env)
		if outcome != processor.OutcomeRejected {
			t.Fatalf("%s: outcome = %q, want rejected", reqID, outcome)
		}
		assertGenericClaimRejection(t, reply)
		if elapsed < quantum {
			t.Fatalf("%s: answered after %s, before the %s quantum", reqID, elapsed, quantum)
		}
		return elapsed
	}

	wrongKeyElapsed := claimAt(testutil.GenReqID("ClmAnchorWrong"), wrongKeyTarget, "not-the-real-secret")
	absentElapsed := claimAt(testutil.GenReqID("ClmAnchorAbsnt"), absentTarget, "irrelevant-secret")

	gap := wrongKeyElapsed - absentElapsed
	if gap < 0 {
		gap = -gap
	}
	if gap > tolerance {
		t.Fatalf("the two causes are separable in the time domain: wrong-key answered in %s, "+
			"absent-target in %s, gap %s > %s — %s of injected decrypt work is visible in the reply "+
			"timing, so the release is anchored after that work rather than at message receipt",
			wrongKeyElapsed, absentElapsed, gap, tolerance, decryptWork)
	}
	// Both must also have landed in the FIRST quantum; if the injected work had
	// pushed one into the second, the comparison above would be measuring the
	// wrong thing.
	if wrongKeyElapsed >= 2*quantum || absentElapsed >= 2*quantum {
		t.Fatalf("a cause overran the first quantum (wrong-key %s, absent %s, quantum %s) — "+
			"raise the quantum or lower the injected work so this test keeps testing anchoring",
			wrongKeyElapsed, absentElapsed, quantum)
	}
}

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
		hint func(t *testing.T, target string) *processor.ContextHint
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
		// The two arms that survive a descriptor edit: they declare
		// target-derived keys the floor does NOT cover, so what comes back
		// depends on which keys the SCRIPT touches, not on which keys the
		// descriptor happens to list. Both target positions are probed because
		// the script answers them at different depths — the absent target is
		// refused at the existence check, the live one runs all the way to the
		// secret comparison — and a key touched before EITHER answer is a
		// surface. See hostileUnflooredReadsHint.
		{"hand-rolled-unfloored-reads-absent", testutil.GenReqID("IndstHostUnfAb"), absentKey, "irrelevant-secret", "no-target", hostileUnflooredReadsHint},
		{"hand-rolled-unfloored-reads-wrong-key", testutil.GenReqID("IndstHostUnfWr"), wrongKeyKey, "not-the-real-secret", "invalid-key", hostileUnflooredReadsHint},
	}

	instance := claimInstance + "-indst"
	var shapes []string
	for _, tc := range cases {
		// The counter is a RUNNING total shared by every arm, and several arms
		// share an outcome — so only the delta across this one submission says
		// anything about this one arm. Snapshot before, require a strict
		// increase after.
		before, _ := readClaimHealthCounter(t, ctx, conn, instance, tc.outcome)

		// Default: the identical shipped builder every live dispatcher calls,
		// including for the absent and malformed targets. Hand-tailoring a
		// conforming arm's declaration would hide the failure this test exists
		// to catch — a submitter cannot know in advance which of its keys exist
		// or parse, so the declaration it ALWAYS sends is the one worth testing.
		hint := identityceremony.ClaimContextHint(tc.target)
		if tc.hint != nil {
			hint = tc.hint(t, tc.target)
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
		outcome, reply, elapsed := submitAndTimeRejection(t, ctx, conn, cp, cons, env)
		if outcome != processor.OutcomeRejected {
			t.Fatalf("%s: outcome = %q, want rejected", tc.name, outcome)
		}
		assertGenericClaimRejection(t, reply)
		// The timing half of NFR-S6, on the deployed step-5 path. Every arm here
		// is a real cause reaching a real script branch, so this is what proves
		// the quantizer is anchored at receipt for the callsite production uses.
		assertRejectionQuantized(t, tc.name, elapsed)
		shapes = append(shapes, fmt.Sprintf("%s|%d", reply.Error.Code, len(reply.Error.Details)))

		if count, ok := readClaimHealthCounter(t, ctx, conn, instance, tc.outcome); !ok || count <= before {
			t.Fatalf("%s: claim-attempts.%s = %d (found=%v), was %d before this submission — THIS cause never reached the script's own gate, so the wire-shape match is vacuous",
				tc.name, tc.outcome, count, ok, before)
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
