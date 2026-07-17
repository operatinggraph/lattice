// CreateUnclaimedIdentity (FR1) integration tests for the identity-domain
// Capability Package.
//
// Pipeline: real Capability authorizer, real DDL cache (resolves the
// identity DDL installed by testutil.InstallPhase1Packages), real
// Hydrator + Executor + Committer.
//
// Coverage:
//  1. TestCreateUnclaimed_Success                  — name+email+phone, full commit
//  2. TestCreateUnclaimed_MissingName_Rejected     — payload missing name
//  3. TestCreateUnclaimed_MissingBothContacts      — name only, no email/phone
//  4. TestCreateUnclaimed_NormalizesEmailCase      — email uppercased -> lowercase
//  5. TestCreateUnclaimed_DuplicateEmail_RemainsUnclaimed — duplicate email walk-back
//  6. TestCreateUnclaimed_NonStaffActor_Denied     — consumer role denied at step 3
//  7. TestCreateUnclaimed_Idempotent               — same requestId twice
//  8. TestCreateUnclaimed_ClaimKeyHashOnly         — claimKey aspect = hash only (NFR-S6)
package identitydomain_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

func newCreatePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ici-" + durable,
	})
}

// TestCreateUnclaimed_Success: staff submits name+email+phone; asserts
// step-8 commit, all aspects exist, index vertices exist, IdentityCreated
// recorded in the tracker.
func TestCreateUnclaimed_Success(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-success")

	reqID := testutil.GenReqID("CUISuccess")
	identityID := identityIDFromRequestID(reqID)
	identityKey := "vtx.identity." + identityID

	claimKeyPlaintext := "andrew-test-claim-secret-0001"
	claimKeyHash := sha256HexOf(claimKeyPlaintext)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Andrew Test","email":"andrew@example.com","phone":"+15551234567","claimKeyHash":"` + claimKeyHash + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey); err != nil {
		t.Fatalf("identity vertex not found: %v", err)
	}
	name := readDecryptedAspectData(t, ctx, conn, identityKey, "name")
	if got, _ := name["value"].(string); got != "Andrew Test" {
		t.Fatalf("name = %q", got)
	}
	email := readDecryptedAspectData(t, ctx, conn, identityKey, "email")
	if got, _ := email["value"].(string); got != "andrew@example.com" {
		t.Fatalf("email = %q", got)
	}
	phone := readDecryptedAspectData(t, ctx, conn, identityKey, "phone")
	if got, _ := phone["value"].(string); got != "+15551234567" {
		t.Fatalf("phone = %q", got)
	}
	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("state = %q, want unclaimed", got)
	}
	claim := readDecryptedAspectData(t, ctx, conn, identityKey, "claimKey")
	if h, _ := claim["hash"].(string); len(h) != 64 {
		t.Fatalf("claimKey hash len = %d", len(h))
	}
	if a, _ := claim["algo"].(string); a != "sha256" {
		t.Fatalf("claimKey algo = %q", a)
	}
	// Verify stored hash == sha256(claimKeyPlaintext).
	storedHash, _ := claim["hash"].(string)
	sum := sha256.Sum256([]byte(claimKeyPlaintext))
	wantHash := hexLower(sum[:])
	if storedHash != wantHash {
		t.Fatalf("claimKey hash mismatch: stored=%q, sha256(plaintext)=%q", storedHash, wantHash)
	}

	// Index vertices exist.
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, contactIndexKey("email", "andrew@example.com")); err != nil {
		t.Fatalf("email index vertex not found: %v", err)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, contactIndexKey("phone", "+15551234567")); err != nil {
		t.Fatalf("phone index vertex not found: %v", err)
	}

	// Tracker records IdentityCreated.
	assertTrackerEvent(t, ctx, conn, reqID, "identity.created")
}

func TestCreateUnclaimed_MissingName_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-missname")
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CUIMissName"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"email":"foo@bar.com"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

func TestCreateUnclaimed_MissingBothContacts(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-missctct")
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CUIMissCtct"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Alice"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

func TestCreateUnclaimed_NormalizesEmailCase(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-normalize")

	reqID := testutil.GenReqID("CUINormEmail")
	identityID := identityIDFromRequestID(reqID)
	identityKey := "vtx.identity." + identityID

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Bob","email":"Foo@BAR.com","claimKeyHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	email := readDecryptedAspectData(t, ctx, conn, identityKey, "email")
	if got, _ := email["value"].(string); got != "foo@bar.com" {
		t.Fatalf("email = %q, want lowercase", got)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, contactIndexKey("email", "foo@bar.com")); err != nil {
		t.Fatalf("normalized email index not found: %v", err)
	}
}

// TestCreateUnclaimed_DuplicateEmail_RemainsUnclaimed: duplicate emails do
// NOT change state. State stays unclaimed; only the IdentityCreated event's
// `duplicate` flag is set.
func TestCreateUnclaimed_DuplicateEmail_RemainsUnclaimed(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-dupemail")

	dupEmail := "x@y.com"
	emailIdxKey := contactIndexKey("email", dupEmail)

	// Pre-seed the index vertex as if a prior identity were created.
	priorIdentityKey := "vtx.identity." + testutil.GenReqID("PriorIdentity")
	idxDoc := map[string]any{
		"class":     "identityindex",
		"isDeleted": false,
		"data": map[string]any{
			"contactType": "email",
			"identityKey": priorIdentityKey,
		},
	}
	idxBytes, _ := json.Marshal(idxDoc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, emailIdxKey, idxBytes); err != nil {
		t.Fatalf("seed index: %v", err)
	}

	reqID := testutil.GenReqID("CUIDupEmail")
	identityID := identityIDFromRequestID(reqID)
	identityKey := "vtx.identity." + identityID

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Charlie","email":"x@y.com","claimKeyHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{emailIdxKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	stateAspect := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateAspect["value"].(string); got != "unclaimed" {
		t.Fatalf("state = %q, want unclaimed (duplicate does not change state)", got)
	}
	// IdentityFlaggedForReview must NOT be emitted — duplicate detection rides
	// the IdentityCreated event's data.duplicate, not the reply or the state.
	assertTrackerNotEvent(t, ctx, conn, reqID, "IdentityFlaggedForReview")
	assertTrackerEvent(t, ctx, conn, reqID, "identity.created")
}

// TestCreateUnclaimed_NonStaffActor_Denied: consumer actor (ClaimIdentity
// only) submits CreateUnclaimedIdentity -> step-3 denies.
func TestCreateUnclaimed_NonStaffActor_Denied(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-nonstaff")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("CUINonStaff"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Eve","email":"eve@example.com"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateUnclaimed_Idempotent: same requestId twice; the second
// short-circuits at step 2 (tracker present).
func TestCreateUnclaimed_Idempotent(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons1 := newCreatePipeline(t, ctx, conn, "ici-idem-1")

	reqID := testutil.GenReqID("CUIIdempotnt")
	identityID := identityIDFromRequestID(reqID)
	identityKey := "vtx.identity." + identityID

	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Dana","email":"dana@example.com","claimKeyHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons1, processor.OutcomeAccepted)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey); err != nil {
		t.Fatalf("identity vertex absent after first create: %v", err)
	}

	cp2, cons2 := newCreatePipeline(t, ctx, conn, "ici-idem-2")
	_ = cp2
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons2, processor.OutcomeDuplicate)

	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey); err != nil {
		t.Fatalf("identity vertex absent after second create attempt: %v", err)
	}
}

// TestCreateUnclaimed_ClaimKeyHashOnly: the client supplies claimKeyHash;
// the claimKey aspect must store only that hash. The plaintext is never
// submitted to Lattice (Option C), so it must appear nowhere in Core KV.
func TestCreateUnclaimed_ClaimKeyHashOnly(t *testing.T) {
	t.Parallel()
	ctx, conn := setupTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-hashonly")

	reqID := testutil.GenReqID("CUIHashOnly")
	identityID := identityIDFromRequestID(reqID)
	identityKey := "vtx.identity." + identityID

	claimKeyPlaintext := "frank-claim-secret-plaintext-9999"
	claimKeyHash := sha256HexOf(claimKeyPlaintext)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-05-22T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Frank","email":"frank@test.com","claimKeyHash":"` + claimKeyHash + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".claimKey")
	if err != nil {
		t.Fatalf("claimKey aspect missing: %v", err)
	}
	rawJSON := string(entry.Value)
	if strings.Contains(rawJSON, claimKeyPlaintext) {
		t.Fatalf("NFR-S6 violation: plaintext leaked into claimKey aspect: %q in %q", claimKeyPlaintext, rawJSON)
	}

	data := readDecryptedAspectData(t, ctx, conn, identityKey, "claimKey")
	hash, _ := data["hash"].(string)
	if len(hash) != 64 {
		t.Fatalf("hash len = %d", len(hash))
	}
	algo, _ := data["algo"].(string)
	if algo != "sha256" {
		t.Fatalf("algo = %q", algo)
	}
	sum := sha256.Sum256([]byte(claimKeyPlaintext))
	if hash != hexLower(sum[:]) {
		t.Fatalf("hash != sha256(plaintext)")
	}
}

// --- shared helpers ---

func assertTrackerEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, eventClass string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID))
	if err != nil {
		t.Fatalf("tracker not found: %v", err)
	}
	tr, err := processor.ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	for _, ec := range ecs {
		if ec == eventClass {
			return
		}
	}
	t.Fatalf("%s not in tracker eventClasses: %v", eventClass, ecs)
}

func assertTrackerNotEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, eventClass string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID))
	if err != nil {
		// no tracker -> trivially not present
		return
	}
	tr, _ := processor.ParseTracker(entry.Value)
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	for _, ec := range ecs {
		if ec == eventClass {
			t.Fatalf("%s should NOT be in eventClasses: %v", eventClass, ecs)
		}
	}
}

func hexLower(b []byte) string {
	const hexChars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexChars[v>>4]
		out[i*2+1] = hexChars[v&0xf]
	}
	return string(out)
}
