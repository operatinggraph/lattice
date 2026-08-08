// ShredIdentityKey integration tests for the privacy-base Capability
// Package (design §2.2/§2.4/§9 Fire 3): the real installed DDL, driven
// through the real Processor commit path — not a hand-seeded fixture.
package privacybase_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/privacyworker"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/processor/outbox"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	"github.com/operatinggraph/lattice/internal/vault"
)

const (
	pbStaffActorID  = "BBshredStfHJKMNPQRST"
	pbStaffActorKey = "vtx.identity." + pbStaffActorID
	pbStaffCapKey   = "cap.identity." + pbStaffActorID
)

// staffCapDoc grants CreateUnclaimedIdentity/RecordIdentityPII (default lane)
// and ShredIdentityKey (urgent lane, per design §2.2's "ops.urgent.>" —
// Contract #2 names urgent for emergency revocations), plus the
// retention-class destruction verbs the sibling suite drives. The grant is a
// TEST posture only: privacy-base ships no ShredRetentionClassKey grant for
// the reason permissions.go records.
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
			{OperationType: "ShredRetentionClassKey", Scope: "any"},
			{OperationType: "RecordRetentionClassShredFinalization", Scope: "any"},
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

	const nonOpActorID = "BBshrdNonopHJKMNPQRS"
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

// --- The dedup-hygiene footprint (design §3.5): what the key shred leaves
// standing, and who erases it. ---

// pbContactIndexKey mirrors identity-domain's contactIndexKey / the script's
// own crypto.sha256NanoID(contactType + ":" + normalized) derivation.
func pbContactIndexKey(contactType, normalized string) string {
	// derived-key: the identityindex key an assertion names. A test has to
	// derive it independently of the script that writes it — reading the key
	// back out of the package under test would assert nothing.
	return "vtx.identityindex." + substrate.SHA256NanoID(contactType+":"+normalized)
}

// pbIndexesLinkKey mirrors the script's `indexes` link key: the identityindex
// vertex is the source, the identity vertex is the target.
func pbIndexesLinkKey(indexKey, identityID string) string {
	return "lnk." + strings.TrimPrefix(indexKey, "vtx.") + ".indexes.identity." + identityID
}

// pbDuplicateOfLinkKey mirrors the script's `duplicateOf` link key: the
// newer (source) identity to the incumbent (target) identity.
func pbDuplicateOfLinkKey(newID, incumbentKey string) string {
	return "lnk.identity." + newID + ".duplicateOf." + strings.TrimPrefix(incumbentKey, "vtx.")
}

func readIsDeleted(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	doc := readDoc(t, ctx, conn, key)
	del, _ := doc["isDeleted"].(bool)
	return del
}

// createUnclaimedWithProbe submits CreateUnclaimedIdentity with the given
// name/email, declaring probeKeys as OptionalReads (the Fire-1 dispatcher
// fix) so a real collision is detected and flagged.
func createUnclaimedWithProbe(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, name, email, claimHash, reqLabel string,
	probeKeys []string) (identityKey, identityID string) {
	t.Helper()
	reqID := testutil.GenReqID(reqLabel)
	identityID = identityIDFromRequestID(reqID)
	identityKey = "vtx.identity." + identityID
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-07-11T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"` + name + `","email":"` + email + `","claimKeyHash":"` + claimHash + `"}`),
		ContextHint:   &processor.ContextHint{OptionalReads: probeKeys},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return identityKey, identityID
}

// TestShredIdentityKey_LeavesOwnedIndexesLive proves ShredIdentityKey does not
// touch the identityindex vertices an identity owns (email + name, both created
// fresh — no collision) or their `indexes` links: that arm is swept by
// PurgeIdentityDedupFootprint, staged off a live erasureRequested marker
// (identityErasure pattern step 4), never by the key shred itself.
func TestShredIdentityKey_LeavesOwnedIndexesLive(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-idx-erase", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "IdxErase")
	identityID := strings.TrimPrefix(identityKey, "vtx.identity.")

	emailIdxKey := pbContactIndexKey("email", "shred-idxerase@example.com")
	nameIdxKey := pbContactIndexKey("name", "shred target")
	emailLinkKey := pbIndexesLinkKey(emailIdxKey, identityID)
	nameLinkKey := pbIndexesLinkKey(nameIdxKey, identityID)

	if readIsDeleted(t, ctx, conn, emailIdxKey) {
		t.Fatalf("precondition: email index already tombstoned")
	}
	if readIsDeleted(t, ctx, conn, nameLinkKey) {
		t.Fatalf("precondition: name indexes link already tombstoned")
	}

	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-idx-erase-urgent", v)
	submitShred(t, ctx, conn, urgentCP, urgentCons, identityKey, "IdxEraseShred", processor.OutcomeAccepted)

	if readIsDeleted(t, ctx, conn, emailIdxKey) {
		t.Fatalf("email identityindex vertex tombstoned by ShredIdentityKey — the dedup footprint belongs to PurgeIdentityDedupFootprint")
	}
	if readIsDeleted(t, ctx, conn, emailLinkKey) {
		t.Fatalf("email indexes link tombstoned by ShredIdentityKey — the dedup footprint belongs to PurgeIdentityDedupFootprint")
	}
	if readIsDeleted(t, ctx, conn, nameIdxKey) {
		t.Fatalf("name identityindex vertex tombstoned by ShredIdentityKey — the dedup footprint belongs to PurgeIdentityDedupFootprint")
	}
	if readIsDeleted(t, ctx, conn, nameLinkKey) {
		t.Fatalf("name indexes link tombstoned by ShredIdentityKey — the dedup footprint belongs to PurgeIdentityDedupFootprint")
	}
}

// TestShredIdentityKey_LeavesDuplicateOfLinkLive_SourceSide shreds the NEWER
// (source) side of a duplicateOf pair and proves the link survives: that arm
// belongs to PurgeIdentityDedupFootprint, staged off a live erasureRequested
// marker (identityErasure pattern step 4).
func TestShredIdentityKey_LeavesDuplicateOfLinkLive_SourceSide(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-dup-src", v)

	claimA := strings.Repeat("1", 64)
	claimB := strings.Repeat("2", 64)
	incumbentKey, _ := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Incumbent One", "collide-src@example.com", claimA, "DupSrcIncumbent", nil)

	emailIdxKey := pbContactIndexKey("email", "collide-src@example.com")
	newcomerKey, newcomerID := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Newcomer Two", "collide-src@example.com", claimB, "DupSrcNewcomer", []string{emailIdxKey})

	dupLinkKey := pbDuplicateOfLinkKey(newcomerID, incumbentKey)
	if readIsDeleted(t, ctx, conn, dupLinkKey) {
		t.Fatalf("precondition: duplicateOf link already tombstoned")
	}

	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-dup-src-urgent", v)
	submitShred(t, ctx, conn, urgentCP, urgentCons, newcomerKey, "DupSrcShred", processor.OutcomeAccepted)

	if readIsDeleted(t, ctx, conn, dupLinkKey) {
		t.Fatalf("duplicateOf link tombstoned by shredding its source side — the dedup footprint belongs to PurgeIdentityDedupFootprint")
	}
}

// TestShredIdentityKey_LeavesDuplicateOfLinkLive_TargetSide shreds the
// INCUMBENT (target) side of a duplicateOf pair and proves the link
// survives, for the same reason as the source side.
func TestShredIdentityKey_LeavesDuplicateOfLinkLive_TargetSide(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-dup-tgt", v)

	claimA := strings.Repeat("3", 64)
	claimB := strings.Repeat("4", 64)
	incumbentKey, _ := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Incumbent Three", "collide-tgt@example.com", claimA, "DupTgtIncumbent", nil)

	emailIdxKey := pbContactIndexKey("email", "collide-tgt@example.com")
	newcomerKey, newcomerID := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Newcomer Four", "collide-tgt@example.com", claimB, "DupTgtNewcomer", []string{emailIdxKey})

	dupLinkKey := pbDuplicateOfLinkKey(newcomerID, incumbentKey)
	if readIsDeleted(t, ctx, conn, dupLinkKey) {
		t.Fatalf("precondition: duplicateOf link already tombstoned")
	}

	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-dup-tgt-urgent", v)
	submitShred(t, ctx, conn, urgentCP, urgentCons, incumbentKey, "DupTgtShred", processor.OutcomeAccepted)

	if readIsDeleted(t, ctx, conn, dupLinkKey) {
		t.Fatalf("duplicateOf link tombstoned by shredding its target (incumbent) side — the dedup footprint belongs to PurgeIdentityDedupFootprint")
	}
	// The newer side is untouched — still a live identity, just not linked to
	// the shredded incumbent by anything this op does.
	if readIsDeleted(t, ctx, conn, newcomerKey) {
		t.Fatalf("shredding the incumbent must not tombstone the newer identity's own root vertex")
	}
}

// TestShredIdentityKey_Reshred_Idempotent proves a second ShredIdentityKey on
// an already-shredded identity is still Accepted, and that its envelope
// afterward carries shredded=true with the SECOND submit's shreddedAt (not
// stale from the first) and none of a prior finalization cycle's booleans —
// the finalization-cycle reset (RecordShredFinalization's own
// TestRecordShredFinalization_ReShredResetsCycle pins the general case; this
// pins ShredIdentityKey's own accept-twice contract, which is the whole of what
// a second shred has to prove).
func TestShredIdentityKey_Reshred_Idempotent(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-reshred", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-reshred-urgent", v)
	testutil.SeedCapDoc(t, ctx, conn, privacyCapDoc())
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "shred-reshred-system", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "Reshred")

	submitShred(t, ctx, conn, urgentCP, urgentCons, identityKey, "ReshredOne", processor.OutcomeAccepted)
	submitFinalization(t, ctx, conn, sysCP, sysCons, identityKey, "vaultKeyDestroyed", "ReshredVKD", processor.OutcomeAccepted)

	const secondSubmittedAt = "2026-07-02T11:45:00Z"
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("ReshredTwo"),
		Lane:          processor.LaneUrgent,
		OperationType: "ShredIdentityKey",
		Actor:         pbStaffActorKey,
		SubmittedAt:   secondSubmittedAt,
		Class:         "shredIdentityKey",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	})
	testutil.DriveOne(t, ctx, urgentCP, urgentCons, processor.OutcomeAccepted)

	data := piiKeyData(t, ctx, conn, identityKey)
	if data["shredded"] != true {
		t.Fatalf("piiKey.shredded = %v after re-shred, want true", data["shredded"])
	}
	if got, _ := data["shreddedAt"].(string); got != secondSubmittedAt {
		t.Fatalf("piiKey.shreddedAt = %q after re-shred, want the second submit's %q", got, secondSubmittedAt)
	}
	for _, stale := range []string{"vaultKeyDestroyed", "vaultKeyDestroyedAt", "projectionsNullified", "projectionsNullifiedAt"} {
		if _, present := data[stale]; present {
			t.Errorf("re-shred must clear prior-cycle %s; still present: %v", stale, data[stale])
		}
	}
}

// TestShredIdentityKey_PostShredCreate_FreshIndexNoLinkToShredded is the
// Gate-3-style vector design §3.5/§7 names explicitly: once the dedup sweep has
// tombstoned an erased identity's owned index, a LATER create for the same
// contact must revive a fresh, live index pointed at the new identity — not
// silently skip indexing (the tombstone would otherwise look "present" to the
// create script's not-in-state gate) — and must NOT flag a duplicateOf against
// the erased identity.
//
// The revive is what this test carries alone. The duplicateOf half is a shape
// assertion, not a proof of the §6 gate: the sweep has tombstoned the index, so
// the create's live_hit check drops the candidate before match_is_erased is
// consulted at all, and the erasure marker would close the write path even
// tombstoned (identity-domain's marker_closes_write_path). The gate's
// piiKey.shredded arm — the bare-shredded incumbent an ordinary walk-in would
// otherwise correlate to — is proven where it can fail, against a live index
// and with a positive control:
// identity-domain's TestErasureGate_CreateUnclaimedIdentity_SkipsBareShreddedIncumbent.
func TestShredIdentityKey_PostShredCreate_FreshIndexNoLinkToShredded(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-postcreate", v)
	testutil.SeedCapDoc(t, ctx, conn, purgeCapDoc())
	sysCP, sysCons := newPurgePipeline(t, ctx, conn, "shred-postcreate-system")

	claimA := strings.Repeat("5", 64)
	claimC := strings.Repeat("6", 64)
	ownerKey, _ := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Reuse Owner", "reuse@example.com", claimA, "ReuseOwner", nil)

	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-postcreate-urgent", v)
	submitShred(t, ctx, conn, urgentCP, urgentCons, ownerKey, "ReuseOwnerShred", processor.OutcomeAccepted)

	// PurgeIdentityDedupFootprint owns the tombstoning: seal the owner for
	// erasure and run one sweep. One invocation is enough for the indexes arm
	// — the op sweeps one class per pass, in the order indexes ->
	// duplicateOf-out -> duplicateOf-in, and the owner's footprint here is
	// only an indexes hit.
	pbSealForErasure(t, ctx, conn, ownerKey)
	submitPurge(t, ctx, conn, sysCP, sysCons, ownerKey, "ReuseOwnerPurge", processor.OutcomeAccepted)

	emailIdxKey := pbContactIndexKey("email", "reuse@example.com")
	if !readIsDeleted(t, ctx, conn, emailIdxKey) {
		t.Fatalf("precondition: owner's email index not tombstoned by the sweep")
	}

	newKey, newID := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Reuse New", "reuse@example.com", claimC, "ReuseNew", []string{emailIdxKey})

	if readIsDeleted(t, ctx, conn, emailIdxKey) {
		t.Fatalf("email index still tombstoned after a fresh create for the same contact — the revive did not run")
	}
	postDoc := readDoc(t, ctx, conn, emailIdxKey)
	postData, _ := postDoc["data"].(map[string]any)
	if postData["identityKey"] != newKey {
		t.Fatalf("email index owner = %v, want the new identity %s (revived, not left pointing at the shredded owner)", postData["identityKey"], newKey)
	}

	newLinkKey := pbIndexesLinkKey(emailIdxKey, newID)
	if readIsDeleted(t, ctx, conn, newLinkKey) {
		t.Fatalf("new identity's indexes link not live")
	}

	if kvExists(t, ctx, conn, pbDuplicateOfLinkKey(newID, ownerKey)) {
		t.Fatalf("a duplicateOf link was created against the shredded owner — the revived index must not be treated as a live duplicate")
	}
}

// pbBoundToLinkKey mirrors identity-domain's boundTo key: the credential
// (source) to the identity it is bound to (target).
func pbBoundToLinkKey(credentialActorKey, ownerIdentityKey string) string {
	return "lnk.identity." + strings.TrimPrefix(credentialActorKey, "vtx.identity.") +
		".boundTo.identity." + strings.TrimPrefix(ownerIdentityKey, "vtx.identity.")
}

func seedBoundToLink(t *testing.T, ctx context.Context, conn *substrate.Conn, credentialActorKey, ownerIdentityKey string) string {
	t.Helper()
	key := pbBoundToLinkKey(credentialActorKey, ownerIdentityKey)
	doc := map[string]any{
		"class": "boundTo", "isDeleted": false,
		"sourceVertex": credentialActorKey, "targetVertex": ownerIdentityKey,
		"localName": "boundTo", "data": map[string]any{"boundAt": "2026-01-01T00:00:00Z"},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal boundTo doc: %v", err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed boundTo link %s: %v", key, err)
	}
	return key
}

// TestShredIdentityKey_LeavesBoundToLinksLive_BothDirections — the boundTo
// link names, in plaintext and in the key itself, which sign-in credential
// belonged to which person, and a live link answers that question decrypt-free
// regardless of what ShredIdentityKey does to the DEK. Retiring it belongs to
// UnbindIdentityCredentials (identityErasure pattern step 3), which also
// retires the credential's own credentialindex vertex — a live index must not
// be allowed to authorize republishing. ShredIdentityKey itself leaves every
// boundTo link, both directions, exactly as it found them.
func TestShredIdentityKey_LeavesBoundToLinksLive_BothDirections(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-boundto", v)

	subjectKey, _ := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Bound Subject", "boundto-shred@example.com", strings.Repeat("7", 64), "BoundToShredSub", nil)
	otherKey, _ := createUnclaimedWithProbe(t, ctx, conn, cp, cons,
		"Bound Other", "boundto-other@example.com", strings.Repeat("8", 64), "BoundToShredOth", nil)

	credKey := "vtx.identity." + testutil.GenReqID("BoundToShredCred")

	// The subject as OWNER (in) and as CREDENTIAL of someone else (out).
	inbound := seedBoundToLink(t, ctx, conn, credKey, subjectKey)
	outbound := seedBoundToLink(t, ctx, conn, subjectKey, otherKey)
	// A third link touching neither side of the subject. Against an op that
	// tombstones nothing this only shows the fixture is a real corpus rather
	// than an empty one; the scoping assertion that can fail belongs to the
	// sweep that does the tombstoning
	// (identity-domain's TestUnbindIdentityCredentials_InboundSweep_*).
	bystander := seedBoundToLink(t, ctx, conn, credKey, otherKey)

	for _, k := range []string{inbound, outbound, bystander} {
		if readIsDeleted(t, ctx, conn, k) {
			t.Fatalf("precondition: %s already tombstoned", k)
		}
	}

	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-boundto-urgent", v)
	submitShred(t, ctx, conn, urgentCP, urgentCons, subjectKey, "BoundToShredDo", processor.OutcomeAccepted)

	if readIsDeleted(t, ctx, conn, inbound) {
		t.Fatalf("boundTo naming the shredded identity as OWNER was tombstoned by ShredIdentityKey — credential retirement belongs to UnbindIdentityCredentials")
	}
	if readIsDeleted(t, ctx, conn, outbound) {
		t.Fatalf("boundTo naming the shredded identity as CREDENTIAL was tombstoned by ShredIdentityKey — credential retirement belongs to UnbindIdentityCredentials")
	}
	if readIsDeleted(t, ctx, conn, bystander) {
		t.Fatalf("a boundTo link touching neither side of the shredded identity was tombstoned")
	}
}

// submitShredPayload publishes one ShredIdentityKey with a caller-chosen
// payload, so a test can exercise the subject-naming surface rather than the
// single spelling submitShred hardcodes.
func submitShredPayload(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, identityKey, payload, reqLabel string, wantOutcome processor.MessageOutcome) {
	t.Helper()
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(reqLabel),
		Lane:          processor.LaneUrgent,
		OperationType: "ShredIdentityKey",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-08-07T10:10:00Z",
		Class:         "shredIdentityKey",
		Payload:       json.RawMessage(payload),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	})
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
}

// TestShredIdentityKey_AcceptsSubjectKeyAlias is what makes step 1 of the
// identityErasure pattern able to run at all.
//
// This op names its subject `identityKey`; every other op on the erasure path
// names it `subjectKey`, and a Loom systemOp step submits a payload the ENGINE
// builds — `{"subjectKey": inst.SubjectKey}` (internal/loom/engine.go), with no
// field-name knob on StepSpec. So the pattern's first step sends a payload this
// op would otherwise reject `InvalidArgument: identityKey: required`, on every
// instance, forever: no shred, no seal, and therefore no erasureRequested marker
// — which is the residue lens's anchor predicate, so the Weaver's convergent
// tail never gets a row either. The spine and the guarantee both die at cursor 0.
//
// Nothing else catches it. Install validation checks step STRUCTURE, not payload
// compatibility (internal/pkgmgr/orchestrationguard.go), the Processor does not
// enforce InputSchema, and any test that pre-shreds its fixture skips the step
// on its guard and never sends the payload at all.
func TestShredIdentityKey_AcceptsSubjectKeyAlias(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-alias", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-alias-urgent", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "ShredAlias")
	recordPII(t, ctx, conn, cp, cons, identityKey, "ShredAliasPII")

	submitShredPayload(t, ctx, conn, urgentCP, urgentCons, identityKey,
		`{"subjectKey":"`+identityKey+`"}`, "ShredAliasOp", processor.OutcomeAccepted)

	data, _ := readDoc(t, ctx, conn, identityKey+".piiKey")["data"].(map[string]any)
	if data["shredded"] != true {
		t.Fatalf("the envelope was not shredded under the subjectKey alias: %v — the identityErasure pattern's first step submits exactly this payload", data)
	}
}

// TestShredIdentityKey_DisagreeingSubjectNames_Rejected pins the refusal rather
// than a precedence rule. Resolving a disagreement here means picking which of
// two named people to irreversibly destroy a key for, which is not a choice this
// op gets to make on a caller's behalf.
func TestShredIdentityKey_DisagreeingSubjectNames_Rejected(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "shred-disagree", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "shred-disagree-urgent", v)

	a := createIdentity(t, ctx, conn, cp, cons, "ShredDisA")
	recordPII(t, ctx, conn, cp, cons, a, "ShredDisAPII")
	b := createIdentity(t, ctx, conn, cp, cons, "ShredDisB")
	recordPII(t, ctx, conn, cp, cons, b, "ShredDisBPII")

	submitShredPayload(t, ctx, conn, urgentCP, urgentCons, a,
		`{"identityKey":"`+a+`","subjectKey":"`+b+`"}`, "ShredDisagreeOp", processor.OutcomeRejected)

	for _, k := range []string{a, b} {
		data, _ := readDoc(t, ctx, conn, k+".piiKey")["data"].(map[string]any)
		if data["shredded"] == true {
			t.Errorf("%s was shredded despite the disagreeing payload — the op picked a person the caller did not unambiguously name", k)
		}
	}
}
