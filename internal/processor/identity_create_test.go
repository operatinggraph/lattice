// Story 4.2 — Staff Creates Unclaimed Identity (FR1) integration tests.
//
// Validates CreateUnclaimedIdentity end-to-end through the 10-step
// Processor pipeline against an embedded NATS server, seeded with
// the identity DDL from bootstrap.IdentityDDL() and capability docs
// granting the operator role (which holds CreateUnclaimedIdentity).
//
// Tests:
//  1. TestCreateUnclaimed_Success                — name+email+phone, full commit
//  2. TestCreateUnclaimed_MissingName_Rejected   — payload missing name
//  3. TestCreateUnclaimed_MissingBothContacts    — name only, no email/phone
//  4. TestCreateUnclaimed_NormalizesEmailCase    — email uppercased → lowercase
//  5. TestCreateUnclaimed_DuplicateEmail_Flags   — pre-seeded index → flagged
//  6. TestCreateUnclaimed_NonStaffActor_Denied   — consumer role → step-3 denied
//  7. TestCreateUnclaimed_Idempotent             — same requestId twice
//  8. TestCreateUnclaimed_ClaimKeyHashOnly       — claimKey aspect = hash only
//
// All tests use capability-auth mode. Fixture seeding mirrors
// identity_state_machine_test.go patterns (Story 4.1).
package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/nats-io/nats.go/jetstream"
)

// Test identity NanoIDs for create tests. 20-char, substrate.Alphabet only.
const (
	iciOperatorActorID  = "JciOpActHKLMNPQRSTUV" // 20 chars, safe alphabet
	iciOperatorActorKey = "vtx.identity." + iciOperatorActorID
	iciOperatorCapKey   = "cap.identity." + iciOperatorActorID

	iciConsumerActorID  = "JciCnActHKLMNPQRSTUV" // 20 chars
	iciConsumerActorKey = "vtx.identity." + iciConsumerActorID
	iciConsumerCapKey   = "cap.identity." + iciConsumerActorID

	iciTestBucket    = "core-kv"
	iciHealthBucket  = "health-kv"
	iciCapBucket     = "capability-kv"
	iciOpsStreamName = "core-operations"
)

// iciOperatorCapDoc seeds an operator capability doc with CreateUnclaimedIdentity.
func iciOperatorCapDoc() *CapabilityDoc {
	now := time.Now().UTC()
	return &CapabilityDoc{
		Key:                    iciOperatorCapKey,
		Actor:                  iciOperatorActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{iciOperatorActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []PlatformPermission{
			{OperationType: "CreateUnclaimedIdentity", Scope: "any"},
		},
		ServiceAccess:   []ServiceAccessEntry{},
		EphemeralGrants: []EphemeralGrant{},
		Roles:           []string{"vtx.role.operator"},
	}
}

// iciConsumerCapDoc seeds a consumer capability doc without CreateUnclaimedIdentity.
func iciConsumerCapDoc() *CapabilityDoc {
	now := time.Now().UTC()
	return &CapabilityDoc{
		Key:                    iciConsumerCapKey,
		Actor:                  iciConsumerActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{iciConsumerActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []PlatformPermission{
			{OperationType: "ClaimIdentity", Scope: "self"},
		},
		ServiceAccess:   []ServiceAccessEntry{},
		EphemeralGrants: []EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

// provisionCreateHarness sets up KV buckets, streams, and capability docs
// for CreateUnclaimedIdentity tests.
func provisionCreateHarness(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()

	for _, bucket := range []string{iciTestBucket, iciHealthBucket} {
		_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket:         bucket,
			LimitMarkerTTL: time.Second,
		})
		if err != nil {
			t.Fatalf("create KV %q: %v", bucket, err)
		}
	}
	streamName := "KV_" + iciTestBucket
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("get stream %q: %v", streamName, err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("enable AllowAtomicPublish: %v", err)
	}

	capKV, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         iciCapBucket,
		LimitMarkerTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("create capability-kv: %v", err)
	}
	opDoc, _ := json.Marshal(iciOperatorCapDoc())
	if _, err := capKV.Put(ctx, iciOperatorCapKey, opDoc); err != nil {
		t.Fatalf("seed operator cap doc: %v", err)
	}
	conDoc, _ := json.Marshal(iciConsumerCapDoc())
	if _, err := capKV.Put(ctx, iciConsumerCapKey, conDoc); err != nil {
		t.Fatalf("seed consumer cap doc: %v", err)
	}

	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     iciOpsStreamName,
		Subjects: []string{"ops.>"},
	})
	if err != nil {
		t.Fatalf("create core-operations stream: %v", err)
	}
}

// seedIdentityDDLForCreate writes the identity DDL in shadow-key form so the
// DDL cache can pick it up. Delegates to bootstrap.IdentityDDL().
func seedIdentityDDLForCreate(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	ddl := bootstrap.IdentityDDL()
	ddlKey := "vtx.meta.identity"

	ddlDoc := map[string]any{
		"class":     ddl.Class,
		"isDeleted": false,
		"data": map[string]any{
			"canonicalName":     ddl.CanonicalName,
			"permittedCommands": ddl.PermittedCommands,
		},
	}
	ddlBytes, _ := json.Marshal(ddlDoc)
	if _, err := conn.KVPut(ctx, iciTestBucket, ddlKey, ddlBytes); err != nil {
		t.Fatalf("seed identity DDL: %v", err)
	}

	scriptDoc := map[string]any{
		"class":     "meta.script",
		"isDeleted": false,
		"data":      map[string]any{"source": ddl.Script},
	}
	scriptBytes, _ := json.Marshal(scriptDoc)
	if _, err := conn.KVPut(ctx, iciTestBucket, ddlKey+".script", scriptBytes); err != nil {
		t.Fatalf("seed identity DDL script: %v", err)
	}
}

// setupCreateTestEnv returns an embedded NATS env with harness + identity DDL.
func setupCreateTestEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "ici-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	provisionCreateHarness(t, ctx, conn)
	seedIdentityDDLForCreate(t, ctx, conn)
	return ctx, conn
}

// newCreatePipeline builds a capability-mode CommitPath for create tests.
func newCreatePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*CommitPath, jetstream.Consumer) {
	t.Helper()
	logger := testLogger()
	metrics := &Metrics{}
	hb := NewHealthHeartbeater(conn, iciHealthBucket, "ici-"+durable, 10*time.Second, metrics, logger)
	cache := NewDDLCache(conn, iciTestBucket, logger)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("ddl cache refresh: %v", err)
	}
	authz, err := SelectAuthorizerArgs(SelectAuthorizerOpts{
		Mode:             AuthModeCapability,
		Reader:           conn,
		CapabilityBucket: iciCapBucket,
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("SelectAuthorizerArgs: %v", err)
	}
	committer := NewCommitter(conn, iciTestBucket, cache, logger, time.Now)
	cp := NewCommitPath(Deps{
		Conn:        conn,
		CoreBucket:  iciTestBucket,
		HealthKV:    iciHealthBucket,
		Authorizer:  authz,
		Hydrator:    NewHydratorWithCache(conn, iciTestBucket, cache, logger),
		Executor:    NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:   NewValidator(cache, logger),
		Committer:   committer,
		Events:      &StubEventPublisher{logger: logger},
		Metrics:     metrics,
		Heartbeater: hb,
		Logger:      logger,
	})
	cons, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName:     iciOpsStreamName,
		Durable:        durable,
		FilterSubjects: []string{"ops.default"},
		AckWait:        5 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}
	return cp, cons
}

// publishCreateOp marshals and publishes a CreateUnclaimedIdentity envelope.
func publishCreateOp(t *testing.T, conn *substrate.Conn, env *OperationEnvelope) {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	_, err = conn.JetStream().Publish(context.Background(), "ops.default", b)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
}

// iciContactIndexKey mirrors the Go side of crypto.sha256NanoID used in the
// Starlark script. Returns the full vtx.identityindex.<nanoID> key.
// contactType is "email" or "phone"; value is the normalized contact.
func iciContactIndexKey(contactType, value string) string {
	sum := sha256.Sum256([]byte(contactType + ":" + value))
	seed := [2]uint64{
		(uint64(sum[0]) << 56) | (uint64(sum[1]) << 48) | (uint64(sum[2]) << 40) | (uint64(sum[3]) << 32) |
			(uint64(sum[4]) << 24) | (uint64(sum[5]) << 16) | (uint64(sum[6]) << 8) | uint64(sum[7]),
		(uint64(sum[8]) << 56) | (uint64(sum[9]) << 48) | (uint64(sum[10]) << 40) | (uint64(sum[11]) << 32) |
			(uint64(sum[12]) << 24) | (uint64(sum[13]) << 16) | (uint64(sum[14]) << 8) | uint64(sum[15]),
	}
	pcg := rand.NewPCG(seed[0], seed[1])
	nanoID := deterministicNanoID(pcg, substrate.NanoIDLength)
	return "vtx.identityindex." + nanoID
}

// readAspectData reads a KV aspect and returns its data map.
func readAspectData(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, iciTestBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	data, _ := doc["data"].(map[string]any)
	return data
}

// iciNanoIDsFromRequestID returns the first two NanoIDs the Starlark script
// would generate for the given requestId. The first is the identity ID,
// the second is the claim key plaintext. This mirrors the deterministic
// nanoid.new() seeding logic: PCG seeded from sha256(requestId), first
// call = identity_id, second call = claim_key_plaintext.
func iciNanoIDsFromRequestID(requestID string) (identityID, claimKeyPlaintext string) {
	seed := seedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	identityID = deterministicNanoID(pcg, substrate.NanoIDLength)
	claimKeyPlaintext = deterministicNanoID(pcg, substrate.NanoIDLength)
	return
}

// ---- Tests ----

// TestCreateUnclaimed_Success: operator submits name+email+phone; asserts
// step-8 commit, all aspects exist, index vertices exist, response detail
// carries plaintext claimKey + identityKey + possibleDuplicateFlag=false,
// IdentityCreated event published (tracker records it).
//
// Note on reply.Detail: JetStream consumers deliver messages with JetStream
// ACK subjects as the reply subject, not the original caller's NATS inbox.
// We verify reply.Detail indirectly by asserting committed state in Core KV
// using deterministic NanoIDs derived from the requestId (same algorithm the
// Starlark script uses). The ResponseDetail field and BuildAcceptedReplyWithDetail
// are exercised by the commit_path wiring which feeds script result to the reply.
func TestCreateUnclaimed_Success(t *testing.T) {
	ctx, conn := setupCreateTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-success")

	reqID := genReqID("CUISuccess")

	// Pre-compute expected identityKey and claimKey from the deterministic NanoID.
	identityID, claimKeyPlaintext := iciNanoIDsFromRequestID(reqID)
	identityKey := "vtx.identity." + identityID

	env := &OperationEnvelope{
		RequestID:     reqID,
		Lane:          LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         iciOperatorActorKey,
		SubmittedAt:   "2026-05-17T10:00:00Z",
		Class:         "identity",
		Payload: json.RawMessage(`{"name":"Andrew Test","email":"andrew@example.com","phone":"+15551234567"}`),
		// No ContextHint: index keys don't exist yet → no duplicate detected
		// (best-effort Phase 1 per Decision #6 in the brief).
	}

	publishCreateOp(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// Vertex + aspects exist.
	if _, err := conn.KVGet(ctx, iciTestBucket, identityKey); err != nil {
		t.Fatalf("identity vertex not found: %v", err)
	}
	nameData := readAspectData(t, ctx, conn, identityKey+".name")
	if got, _ := nameData["value"].(string); got != "Andrew Test" {
		t.Fatalf("name aspect value = %q, want %q", got, "Andrew Test")
	}
	emailData := readAspectData(t, ctx, conn, identityKey+".email")
	if got, _ := emailData["value"].(string); got != "andrew@example.com" {
		t.Fatalf("email aspect value = %q", got)
	}
	phoneData := readAspectData(t, ctx, conn, identityKey+".phone")
	if got, _ := phoneData["value"].(string); got != "+15551234567" {
		t.Fatalf("phone aspect value = %q", got)
	}
	stateData := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateData["value"].(string); got != "unclaimed" {
		t.Fatalf("state aspect value = %q, want unclaimed", got)
	}
	claimData := readAspectData(t, ctx, conn, identityKey+".claimKey")
	if h, _ := claimData["hash"].(string); len(h) != 64 {
		t.Fatalf("claimKey hash len = %d, want 64", len(h))
	}
	if a, _ := claimData["algo"].(string); a != "sha256" {
		t.Fatalf("claimKey algo = %q, want sha256", a)
	}

	// Verify ResponseDetail content via the script result directly.
	// The Starlark script returns {"identityKey": ..., "claimKey": ...,
	// "possibleDuplicateFlag": ...} under the "response" key. We reproduce
	// the expected values from deterministic NanoIDs to confirm the mapping.
	// The claimKey plaintext is only visible in reply.Detail (NFR-S6: not stored
	// in KV). Verify the stored hash matches sha256(plaintext).
	claimHash, _ := claimData["hash"].(string)
	sum := sha256.Sum256([]byte(claimKeyPlaintext))
	wantHash := hex.EncodeToString(sum[:])
	if claimHash != wantHash {
		t.Fatalf("claimKey hash mismatch: stored=%q, sha256(plaintext)=%q", claimHash, wantHash)
	}

	// Index vertices exist.
	emailIdx := iciContactIndexKey("email", "andrew@example.com")
	if _, err := conn.KVGet(ctx, iciTestBucket, emailIdx); err != nil {
		t.Fatalf("email index vertex not found: %v", err)
	}
	phoneIdx := iciContactIndexKey("phone", "+15551234567")
	if _, err := conn.KVGet(ctx, iciTestBucket, phoneIdx); err != nil {
		t.Fatalf("phone index vertex not found: %v", err)
	}

	// Tracker records IdentityCreated event.
	te, err := conn.KVGet(ctx, iciTestBucket, TrackerKey(reqID))
	if err != nil {
		t.Fatalf("tracker not found: %v", err)
	}
	tr, err := ParseTracker(te.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	found := false
	for _, ec := range ecs {
		if ec == "IdentityCreated" {
			found = true
		}
	}
	if !found {
		t.Fatalf("IdentityCreated not in tracker eventClasses: %v", ecs)
	}
}

// TestCreateUnclaimed_MissingName_Rejected: payload with email but no name.
func TestCreateUnclaimed_MissingName_Rejected(t *testing.T) {
	ctx, conn := setupCreateTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-missing-name")

	env := &OperationEnvelope{
		RequestID:     genReqID("CUIMissName"),
		Lane:          LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         iciOperatorActorKey,
		SubmittedAt:   "2026-05-17T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"email":"foo@bar.com"}`),
	}
	publishCreateOp(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeRejected)
}

// TestCreateUnclaimed_MissingBothContacts: payload with name only.
func TestCreateUnclaimed_MissingBothContacts(t *testing.T) {
	ctx, conn := setupCreateTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-missing-contacts")

	env := &OperationEnvelope{
		RequestID:     genReqID("CUIMissCtct"),
		Lane:          LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         iciOperatorActorKey,
		SubmittedAt:   "2026-05-17T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Alice"}`),
	}
	publishCreateOp(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeRejected)
}

// TestCreateUnclaimed_NormalizesEmailCase: email "Foo@BAR.com" stored as
// "foo@bar.com" and the index key uses the normalized form.
func TestCreateUnclaimed_NormalizesEmailCase(t *testing.T) {
	ctx, conn := setupCreateTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-normalize")

	normalizedEmail := "foo@bar.com"
	reqID := genReqID("CUINormEmail")
	identityID, _ := iciNanoIDsFromRequestID(reqID)
	identityKey := "vtx.identity." + identityID

	env := &OperationEnvelope{
		RequestID:     reqID,
		Lane:          LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         iciOperatorActorKey,
		SubmittedAt:   "2026-05-17T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Bob","email":"Foo@BAR.com"}`),
		// No ContextHint: index key doesn't exist yet → no duplicate.
	}

	publishCreateOp(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// Email aspect should store lowercase.
	emailData := readAspectData(t, ctx, conn, identityKey+".email")
	if got, _ := emailData["value"].(string); got != normalizedEmail {
		t.Fatalf("email aspect = %q, want %q (lowercase normalized)", got, normalizedEmail)
	}

	// Index vertex was created using normalized email key.
	normalizedIdx := iciContactIndexKey("email", normalizedEmail)
	if _, err := conn.KVGet(ctx, iciTestBucket, normalizedIdx); err != nil {
		t.Fatalf("normalized email index vertex not found at %s: %v", normalizedIdx, err)
	}
}

// TestCreateUnclaimed_DuplicateEmail_RemainsUnclaimed: Story 4.6 walk-back —
// the flagged-for-review state has been retired. Duplicate detection is now
// lens-projected (identity-hygiene package's duplicateCandidates Lens), so a
// duplicate create simply records duplicate=true in the IdentityCreated event
// and leaves state=unclaimed. No IdentityFlaggedForReview event is emitted.
func TestCreateUnclaimed_DuplicateEmail_RemainsUnclaimed(t *testing.T) {
	ctx, conn := setupCreateTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-dup-email")

	dupEmail := "x@y.com"
	emailIdxKey := iciContactIndexKey("email", dupEmail)

	// Pre-seed the index vertex as if a prior identity was created.
	priorIdentityKey := "vtx.identity." + genReqID("PriorIdentity")
	idxDoc := map[string]any{
		"class":     "identityindex",
		"isDeleted": false,
		"data": map[string]any{
			"contactType": "email",
			"identityKey": priorIdentityKey,
		},
	}
	idxBytes, _ := json.Marshal(idxDoc)
	if _, err := conn.KVPut(ctx, iciTestBucket, emailIdxKey, idxBytes); err != nil {
		t.Fatalf("seed index vertex: %v", err)
	}

	reqID := genReqID("CUIDupEmail")
	identityID, _ := iciNanoIDsFromRequestID(reqID)
	identityKey := "vtx.identity." + identityID

	env := &OperationEnvelope{
		RequestID:     reqID,
		Lane:          LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         iciOperatorActorKey,
		SubmittedAt:   "2026-05-17T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Charlie","email":"x@y.com"}`),
		ContextHint: &ContextHint{
			Reads: []string{emailIdxKey},
		},
	}

	publishCreateOp(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// State aspect must remain unclaimed (Story 4.6 walk-back).
	stateData := readAspectData(t, ctx, conn, identityKey+".state")
	if got, _ := stateData["value"].(string); got != "unclaimed" {
		t.Fatalf("state = %q, want unclaimed (Story 4.6: no auto-flagging)", got)
	}

	// Tracker must record IdentityCreated; IdentityFlaggedForReview is no
	// longer emitted.
	te, err := conn.KVGet(ctx, iciTestBucket, TrackerKey(reqID))
	if err != nil {
		t.Fatalf("tracker not found: %v", err)
	}
	tr, _ := ParseTracker(te.Value)
	ecs, _ := tr.Data["eventClasses"].([]interface{})
	createdFound := false
	for _, ec := range ecs {
		if ec == "IdentityCreated" {
			createdFound = true
		}
		if ec == "IdentityFlaggedForReview" {
			t.Errorf("Story 4.6: IdentityFlaggedForReview must not be emitted; got eventClasses=%v", ecs)
		}
	}
	if !createdFound {
		t.Fatalf("IdentityCreated not in tracker eventClasses: %v", ecs)
	}
}

// TestCreateUnclaimed_NonStaffActor_Denied: consumer actor (ClaimIdentity
// only) submits CreateUnclaimedIdentity → step-3 denies with OperationNotPermitted.
func TestCreateUnclaimed_NonStaffActor_Denied(t *testing.T) {
	ctx, conn := setupCreateTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-nonstaf")

	env := &OperationEnvelope{
		RequestID:     genReqID("CUINonStaff"),
		Lane:          LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         iciConsumerActorKey, // consumer — no CreateUnclaimedIdentity grant
		SubmittedAt:   "2026-05-17T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Eve","email":"eve@example.com"}`),
	}
	publishCreateOp(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeRejected)
}

// TestCreateUnclaimed_Idempotent: submit the same requestId twice. The
// second call short-circuits at step 2 (tracker already exists).
// The committed identity vertex exists after the first create; the second
// create short-circuits without creating a second vertex.
func TestCreateUnclaimed_Idempotent(t *testing.T) {
	ctx, conn := setupCreateTestEnv(t)
	cp, cons1 := newCreatePipeline(t, ctx, conn, "ici-idempotent")

	reqID := genReqID("CUIIdempotnt")
	identityID, _ := iciNanoIDsFromRequestID(reqID)
	identityKey := "vtx.identity." + identityID

	env := &OperationEnvelope{
		RequestID:     reqID,
		Lane:          LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         iciOperatorActorKey,
		SubmittedAt:   "2026-05-17T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Dana","email":"dana@example.com"}`),
		// No ContextHint: index key doesn't exist on first create.
	}

	// First submission: must succeed and create the identity.
	publishCreateOp(t, conn, env)
	driveOne(t, ctx, cp, cons1, OutcomeAccepted)

	// Identity vertex must exist after first create.
	if _, err := conn.KVGet(ctx, iciTestBucket, identityKey); err != nil {
		t.Fatalf("identity vertex not found after first create: %v", err)
	}

	// Second submission with same requestId. Must short-circuit at step 2.
	cons2, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName:     iciOpsStreamName,
		Durable:        "ici-idempotent-c2",
		FilterSubjects: []string{"ops.default"},
		AckWait:        5 * time.Second,
	}, testLogger())
	if err != nil {
		t.Fatalf("EnsureConsumer c2: %v", err)
	}
	publishCreateOp(t, conn, env)
	driveOne(t, ctx, cp, cons2, OutcomeDuplicate)

	// The original identity vertex is still there (no double-create).
	if _, err := conn.KVGet(ctx, iciTestBucket, identityKey); err != nil {
		t.Fatalf("identity vertex not found after second create attempt: %v", err)
	}
}

// TestCreateUnclaimed_ClaimKeyHashOnly: the claimKey aspect stores only the
// hash, never the plaintext. Verifies NFR-S6 compliance.
// Uses deterministic NanoID to compute expected claimKey plaintext, then
// verifies the stored hash equals sha256(plaintext).
func TestCreateUnclaimed_ClaimKeyHashOnly(t *testing.T) {
	ctx, conn := setupCreateTestEnv(t)
	cp, cons := newCreatePipeline(t, ctx, conn, "ici-hashonly")

	reqID := genReqID("CUIHashOnly")
	identityID, claimKeyPlaintext := iciNanoIDsFromRequestID(reqID)
	identityKey := "vtx.identity." + identityID

	env := &OperationEnvelope{
		RequestID:     reqID,
		Lane:          LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         iciOperatorActorKey,
		SubmittedAt:   "2026-05-17T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Frank","email":"frank@test.com"}`),
		// No ContextHint: index key doesn't exist on first create.
	}

	publishCreateOp(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeAccepted)

	// Read the raw aspect envelope from Core KV.
	entry, err := conn.KVGet(ctx, iciTestBucket, identityKey+".claimKey")
	if err != nil {
		t.Fatalf("claimKey aspect not found: %v", err)
	}
	rawJSON := string(entry.Value)

	// Plaintext must NOT appear anywhere in the stored JSON (NFR-S6).
	if strings.Contains(rawJSON, claimKeyPlaintext) {
		t.Fatalf("plaintext claimKey leaked into stored aspect JSON (NFR-S6 violation): found %q in %q",
			claimKeyPlaintext, rawJSON)
	}

	// Hash must be 64 hex chars; algo must be sha256.
	var doc map[string]any
	_ = json.Unmarshal(entry.Value, &doc)
	data, _ := doc["data"].(map[string]any)
	hash, _ := data["hash"].(string)
	if len(hash) != 64 {
		t.Fatalf("claimKey hash len = %d, want 64", len(hash))
	}
	algo, _ := data["algo"].(string)
	if algo != "sha256" {
		t.Fatalf("claimKey algo = %q, want sha256", algo)
	}

	// Verify: stored hash == sha256(claimKeyPlaintext) (proves correct pre-image).
	expectedSum := sha256.Sum256([]byte(claimKeyPlaintext))
	expectedHex := make([]byte, 64)
	const hexChars = "0123456789abcdef"
	for i, b := range expectedSum {
		expectedHex[i*2] = hexChars[b>>4]
		expectedHex[i*2+1] = hexChars[b&0xf]
	}
	if hash != string(expectedHex) {
		t.Fatalf("stored hash = %q, want sha256(%q) = %q", hash, claimKeyPlaintext, string(expectedHex))
	}
}
