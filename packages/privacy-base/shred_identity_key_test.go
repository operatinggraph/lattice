// ShredIdentityKey integration tests for the privacy-base Capability
// Package (design §2.2/§2.4/§9 Fire 3): the real installed DDL, driven
// through the real Processor commit path — not a hand-seeded fixture.
package privacybase_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/privacyworker"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/processor/outbox"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	"github.com/asolgan/lattice/internal/vault"
)

const (
	pbStaffActorID  = "BBshredStfHJKMNPQRST"
	pbStaffActorKey = "vtx.identity." + pbStaffActorID
	pbStaffCapKey   = "cap.identity." + pbStaffActorID
)

// staffCapDoc grants CreateUnclaimedIdentity/RecordIdentityPII (default lane)
// and ShredIdentityKey (urgent lane, per design §2.2's "ops.urgent.>" —
// Contract #2 names urgent for emergency revocations).
func staffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    pbStaffCapKey,
		Actor:                  pbStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{pbStaffActorKey: 1},
		Lanes:                  []string{"default", "urgent"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateUnclaimedIdentity", Scope: "any"},
			{OperationType: "RecordIdentityPII", Scope: "any"},
			{OperationType: "ShredIdentityKey", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

func setupShredEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // installs rbac+privacy-base+identity+hygiene
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	return ctx, conn
}

func newDefaultPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string, v vault.Vault) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "shred-" + durable,
		Vault:    v,
	})
}

func newUrgentPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string, v vault.Vault) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        durable,
		Instance:       "shred-" + durable,
		Vault:          v,
		FilterSubjects: []string{"ops.urgent"},
	})
}

func identityIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

func createIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, reqLabel string) string {
	t.Helper()
	reqID := testutil.GenReqID(reqLabel)
	identityKey := "vtx.identity." + identityIDFromRequestID(reqID)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-07-02T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Shred Target","email":"shred-` + reqLabel + `@example.com","claimKeyHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return identityKey
}

func recordPII(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, identityKey, reqLabel string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(reqLabel),
		Lane:          processor.LaneDefault,
		OperationType: "RecordIdentityPII",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-07-02T10:05:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","ssn":"123-45-6789","dob":"1990-01-15"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey, identityKey + ".state"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

func submitShred(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, identityKey, reqLabel string, wantOutcome processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(reqLabel),
		Lane:          processor.LaneUrgent,
		OperationType: "ShredIdentityKey",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-07-02T10:10:00Z",
		Class:         "shredIdentityKey",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
}

func readDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return doc
}

func kvExists(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	_, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err == nil {
		return true
	}
	if errors.Is(err, substrate.ErrKeyNotFound) {
		return false
	}
	t.Fatalf("kvExists %s: unexpected error: %v", key, err)
	return false
}

func seedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string, data map[string]any, isDeleted bool) {
	t.Helper()
	if data == nil {
		data = map[string]any{}
	}
	doc := map[string]any{"class": class, "isDeleted": isDeleted, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed vertex %s: %v", key, err)
	}
}

// assertOutboxEventClass reads reqID's outbox aspect and asserts it carries
// an event of wantClass.
func assertOutboxEventClass(t *testing.T, ctx context.Context, conn *substrate.Conn, reqID, wantClass string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(reqID))
	if err != nil {
		t.Fatalf("outbox aspect missing for %s: %v", reqID, err)
	}
	aspect, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect: %v", err)
	}
	for _, ev := range aspect.Data.Events {
		if ev.EventType == wantClass {
			return
		}
	}
	t.Fatalf("no %s event in outbox aspect for %s (got %+v)", wantClass, reqID, aspect.Data.Events)
}

// TestShredIdentityKey_MarksExistingPiiKeyShredded — the C1 case: an identity
// that already received a sensitive write has its piiKey.shredded flipped to
// true (an update, all other envelope fields preserved) and emits
// privacy.keyShredded.
func TestShredIdentityKey_MarksExistingPiiKeyShredded(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-mark", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "ShredMark")
	recordPII(t, ctx, conn, cp, cons, identityKey, "ShredMarkPII")

	preEnvelope := readDoc(t, ctx, conn, identityKey+".piiKey")
	preData, _ := preEnvelope["data"].(map[string]any)
	if shredded, _ := preData["shredded"].(bool); shredded {
		t.Fatalf("precondition: piiKey already shredded before the op")
	}
	wrappedDEKBefore := preData["wrappedDEK"]

	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-mark-urgent", v)
	shredReqID := testutil.GenReqID("ShredMarkOp")
	env := &processor.OperationEnvelope{
		RequestID:     shredReqID,
		Lane:          processor.LaneUrgent,
		OperationType: "ShredIdentityKey",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-07-02T10:10:00Z",
		Class:         "shredIdentityKey",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, urgentCP, urgentCons, processor.OutcomeAccepted)

	postEnvelope := readDoc(t, ctx, conn, identityKey+".piiKey")
	postData, _ := postEnvelope["data"].(map[string]any)
	if shredded, _ := postData["shredded"].(bool); !shredded {
		t.Fatalf("piiKey.shredded = %v after ShredIdentityKey, want true", postData["shredded"])
	}
	if postData["wrappedDEK"] != wrappedDEKBefore {
		t.Fatalf("wrappedDEK changed by ShredIdentityKey: before=%v after=%v", wrappedDEKBefore, postData["wrappedDEK"])
	}

	assertOutboxEventClass(t, ctx, conn, shredReqID, "privacy.keyShredded")
}

// TestShredIdentityKey_NoPiiKeyYet_WritesDurablePlaceholder — the C2 case: an
// identity that never received a sensitive write has no piiKey aspect.
// ShredIdentityKey writes a DURABLE placeholder (empty wrappedDEK,
// shredded=true) rather than skipping the mutation — LocalBackend's
// shredded-identity deny-list is in-memory only, so without a Core-KV record
// a sensitive write arriving after a Processor restart would mint a fresh,
// unshredded key and silently reopen the identity to PII. A directly-seeded
// vertex (not CreateUnclaimedIdentity, which itself writes sensitive
// name/email/claimKey aspects and so always mints a real piiKey) is the only
// way to reach the never-had-PII state through this DDL.
func TestShredIdentityKey_NoPiiKeyYet_WritesDurablePlaceholder(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)

	identityKey := "vtx.identity." + testutil.GenReqID("ShredNoPIITgt")
	seedVertex(t, ctx, conn, identityKey, "identity", map[string]any{}, false)
	if kvExists(t, ctx, conn, identityKey+".piiKey") {
		t.Fatalf("precondition: identity already has a piiKey")
	}

	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-nopii-urgent", v)
	reqID := testutil.GenReqID("ShredNoPIIOp")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneUrgent,
		OperationType: "ShredIdentityKey",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-07-02T10:10:00Z",
		Class:         "shredIdentityKey",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, urgentCP, urgentCons, processor.OutcomeAccepted)

	postEnvelope := readDoc(t, ctx, conn, identityKey+".piiKey")
	postData, _ := postEnvelope["data"].(map[string]any)
	if shredded, _ := postData["shredded"].(bool); !shredded {
		t.Fatalf("placeholder piiKey.shredded = %v, want true", postData["shredded"])
	}
	if wrappedDEK, _ := postData["wrappedDEK"].(string); wrappedDEK != "" {
		t.Fatalf("placeholder piiKey.wrappedDEK = %q, want empty (no real key was ever minted)", wrappedDEK)
	}
	assertOutboxEventClass(t, ctx, conn, reqID, "privacy.keyShredded")

	// The durability proof: a NEW vault.Envelope decoded straight off this
	// placeholder must be rejected by a fresh (simulating post-restart)
	// LocalBackend instance sharing only the master KEK, not any in-memory
	// state — proving the shred survives a process restart.
	restarted := testutil.TestVault(t)
	envelope := readPiiKeyEnvelopeForTest(t, ctx, conn, identityKey)
	if _, err := restarted.Encrypt(ctx, identityKey, envelope, []byte(`{"value":"reopened"}`)); !errors.Is(err, vault.ErrKeyShredded) {
		t.Fatalf("post-restart Encrypt error = %v, want vault.ErrKeyShredded", err)
	}
}

// TestShredIdentityKey_AbsentIdentity_Rejected — the target-existence guard,
// mirroring MarkExpired's C1: no marker/mutation for an identity that does
// not exist.
func TestShredIdentityKey_AbsentIdentity_Rejected(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "shred-absent", v)

	absentKey := "vtx.identity." + testutil.GenReqID("ShredAbsentTgt")
	submitShred(t, ctx, conn, cp, cons, absentKey, "ShredAbsentOp", processor.OutcomeRejected)

	if kvExists(t, ctx, conn, absentKey+".piiKey") {
		t.Fatalf("a piiKey was written for an absent identity")
	}
}

// TestShredIdentityKey_TombstonedIdentity_Rejected — the tombstoned-parent
// guard, mirroring MarkExpired's C1 tombstoned case.
func TestShredIdentityKey_TombstonedIdentity_Rejected(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "shred-tomb", v)

	identityKey := "vtx.identity." + testutil.GenReqID("ShredTombTgt")
	seedVertex(t, ctx, conn, identityKey, "identity", map[string]any{}, true)

	submitShred(t, ctx, conn, cp, cons, identityKey, "ShredTombOp", processor.OutcomeRejected)
}

// TestShredIdentityKey_NonOperatorActor_Denied drives the real capability
// auth path: an actor with no ShredIdentityKey grant is DENIED at step 3.
func TestShredIdentityKey_NonOperatorActor_Denied(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-denied", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "ShredDeny")

	const nonOpActorID = "BBshrdNonOpHJKMNPQRS"
	const nonOpActorKey = "vtx.identity." + nonOpActorID
	const nonOpCapKey = "cap.identity." + nonOpActorID
	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    nonOpCapKey,
		Actor:                  nonOpActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{nonOpActorKey: 1},
		Lanes:                  []string{"default", "urgent"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateUnclaimedIdentity", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{},
	})

	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-denied-urgent", v)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("ShredDenyOp"),
		Lane:          processor.LaneUrgent,
		OperationType: "ShredIdentityKey",
		Actor:         nonOpActorKey,
		SubmittedAt:   "2026-07-02T10:10:00Z",
		Class:         "shredIdentityKey",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, urgentCP, urgentCons, processor.OutcomeRejected)

	// CreateUnclaimedIdentity already minted a piiKey (it writes sensitive
	// name/email/claimKey aspects); the denial must leave it UNTOUCHED — still
	// not shredded.
	postEnvelope := readDoc(t, ctx, conn, identityKey+".piiKey")
	postData, _ := postEnvelope["data"].(map[string]any)
	if shredded, _ := postData["shredded"].(bool); shredded {
		t.Fatalf("a denied ShredIdentityKey marked piiKey.shredded = true")
	}
}

// TestShredIdentityKey_EndToEnd_VaultDecryptFails is the full-chain proof
// (design §9 Fire 3's "shred -> decrypt fails"): submit RecordIdentityPII
// (mints piiKey + encrypts ssn/dob), submit ShredIdentityKey, let the outbox
// + privacy-worker consumers (both driven against the SAME Vault instance the
// commit path used) process the resulting event chain, and assert
// Vault.Decrypt subsequently fails with ErrKeyShredded for that identity —
// the ciphertext already in Core KV becomes permanently unrecoverable.
func TestShredIdentityKey_EndToEnd_VaultDecryptFails(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-e2e", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-e2e-urgent", v)

	workerCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = outbox.New(conn, testutil.HarnessCoreBucket, testutil.TestLogger()).Run(workerCtx) }()
	worker := privacyworker.New(privacyworker.Config{
		Conn:         conn,
		EventsStream: testutil.HarnessEventsStream,
		Vault:        v,
		Logger:       testutil.TestLogger(),
	})
	go func() { _ = worker.Run(workerCtx) }()

	identityKey := createIdentity(t, ctx, conn, cp, cons, "ShredE2E")
	recordPII(t, ctx, conn, cp, cons, identityKey, "ShredE2EPII")

	envelope := readPiiKeyEnvelopeForTest(t, ctx, conn, identityKey)
	ssnCT := readCiphertextForTest(t, ctx, conn, identityKey+".ssn")

	// Sanity: decrypt succeeds BEFORE the shred.
	if _, err := v.Decrypt(ctx, identityKey, envelope, ssnCT); err != nil {
		t.Fatalf("precondition: Decrypt before shred failed: %v", err)
	}

	shredReqID := testutil.GenReqID("ShredE2EOp")
	env := &processor.OperationEnvelope{
		RequestID:     shredReqID,
		Lane:          processor.LaneUrgent,
		OperationType: "ShredIdentityKey",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-07-02T10:10:00Z",
		Class:         "shredIdentityKey",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, urgentCP, urgentCons, processor.OutcomeAccepted)

	// Poll for the async privacy-worker to process the outbox-published
	// event and call Vault.ShredKey — the two consumer hops (outbox publish,
	// then worker consume) run on their own goroutines.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := v.Decrypt(ctx, identityKey, envelope, ssnCT)
		if errors.Is(err, vault.ErrKeyShredded) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Decrypt did not fail with ErrKeyShredded within 10s of ShredIdentityKey committing (got err=%v)", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func readPiiKeyEnvelopeForTest(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) vault.Envelope {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, identityKey+".piiKey")
	if err != nil {
		t.Fatalf("KVGet piiKey for %s: %v", identityKey, err)
	}
	var doc struct {
		Data vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal piiKey for %s: %v", identityKey, err)
	}
	return doc.Data
}

func readCiphertextForTest(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) vault.Ciphertext {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc struct {
		Data vault.Ciphertext `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return doc.Data
}
