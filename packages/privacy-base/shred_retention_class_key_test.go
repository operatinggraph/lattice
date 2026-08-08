// ShredRetentionClassKey integration tests for the privacy-base Capability
// Package (retention-class-key-custody-design.md §4.3): the real installed
// DDL, driven through the real Processor commit path — not a hand-seeded
// fixture.
//
// The holder vertices are seeded directly rather than installed, because
// pkgmgr still REFUSES a retentionClass custody declaration at install
// (internal/pkgmgr/custodyscope.go rule 5, lifted by item 3b once the
// destruction can reach a read model). That gate constrains what a PACKAGE may
// declare; it does not constrain the op, which requires only a live holder
// vertex — which is exactly what lets this fire's acceptance criterion be
// proven with zero installed classes and zero consumers.
package privacybase_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/privacyworker"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/processor/outbox"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	"github.com/operatinggraph/lattice/internal/vault"
)

const (
	rcHolderExisting    = "vtx.retentionclass.RCkeyHLDRabcdefghijk"
	rcHolderNoKey       = "vtx.retentionclass.RCkeyHLDRmnpqrstuvwx"
	rcHolderTombstoned  = "vtx.retentionclass.RCkeyHLDR123456789ab"
	rcHolderAbsent      = "vtx.retentionclass.RCkeyHLDRZYXWVUTSRQP"
	rcHolderReshred     = "vtx.retentionclass.RCkeyHLDRJKLMNPQRSTU"
	rcHolderFinalize    = "vtx.retentionclass.RCkeyHLDRcdefghijkmn"
	rcHolderEndToEnd    = "vtx.retentionclass.RCkeyHLDRefghijkmnpq"
	rcRetentionClassDDL = "shredRetentionClassKey"
)

// seedRetentionClassHolder writes a live holder vertex and its
// `.retentionPolicy` aspect — the pair internal/pkgmgr/build.go writes at
// install for a declared class.
func seedRetentionClassHolder(t *testing.T, ctx context.Context, conn *substrate.Conn, holderKey, canonicalName string) {
	t.Helper()
	seedVertex(t, ctx, conn, holderKey, "retentionclass", nil, false)
	seedAspectDoc(t, ctx, conn, holderKey+".retentionPolicy", holderKey, "retentionPolicy", map[string]any{
		"canonicalName":   canonicalName,
		"policy":          "eraseOnExpiry",
		"retentionPeriod": "P7Y",
		"description":     "test retention class",
	}, false)
}

// seedAspectDoc writes one aspect document in the Contract #1 envelope shape.
func seedAspectDoc(t *testing.T, ctx context.Context, conn *substrate.Conn,
	key, vertexKey, localName string, data map[string]any, isDeleted bool) {
	t.Helper()
	doc := map[string]any{
		"class":     localName,
		"vertexKey": vertexKey,
		"localName": localName,
		"isDeleted": isDeleted,
		"data":      data,
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal aspect %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed aspect %s: %v", key, err)
	}
}

// submitRetentionShred publishes one ShredRetentionClassKey and drives it.
func submitRetentionShred(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, holderKey, reqLabel string,
	wantOutcome processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(reqLabel)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneUrgent,
		OperationType: "ShredRetentionClassKey",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-08-08T10:10:00Z",
		Class:         rcRetentionClassDDL,
		Payload:       json.RawMessage(`{"retentionClassKey":"` + holderKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{holderKey},
			OptionalReads: []string{holderKey + ".piiKey"},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
	return reqID
}

// submitRetentionFinalization publishes one RecordRetentionClassShredFinalization.
func submitRetentionFinalization(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, holderKey, step, reqLabel string,
	wantOutcome processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(reqLabel),
		Lane:          processor.LaneUrgent,
		OperationType: "RecordRetentionClassShredFinalization",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-08-08T10:20:00Z",
		Class:         rcRetentionClassDDL,
		Payload:       json.RawMessage(`{"retentionClassKey":"` + holderKey + `","step":"` + step + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{holderKey + ".piiKey"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
}

// rcStoredEnvelope re-parses the holder's stored piiKey `data` as the typed
// vault.Envelope, so byte fields compare as bytes rather than as their base64
// rendering.
func rcStoredEnvelope(t *testing.T, ctx context.Context, conn *substrate.Conn, holderKey string) vault.Envelope {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, holderKey+".piiKey")
	if err != nil {
		t.Fatalf("KVGet piiKey for %s: %v", holderKey, err)
	}
	var doc struct {
		Data vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal piiKey for %s: %v", holderKey, err)
	}
	return doc.Data
}

func rcPiiKeyData(t *testing.T, ctx context.Context, conn *substrate.Conn, holderKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, holderKey+".piiKey")
	data, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("piiKey for %s has no data map: %+v", holderKey, doc)
	}
	return data
}

// TestShredRetentionClassKey_MarksExistingPiiKeyShredded — a class that has
// received a sensitive write has its piiKey.shredded flipped true, every other
// envelope field preserved, and emits privacy.retentionClassKeyShredded.
func TestShredRetentionClassKey_MarksExistingPiiKeyShredded(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "rcshred-mark", v)

	seedRetentionClassHolder(t, ctx, conn, rcHolderExisting, "clinicalRecord")
	envelope, err := v.CreateIdentityKey(ctx, rcHolderExisting)
	if err != nil {
		t.Fatalf("mint class DEK: %v", err)
	}
	seedAspectDoc(t, ctx, conn, rcHolderExisting+".piiKey", rcHolderExisting, "piiKey", map[string]any{
		"wrappedDEK": envelope.WrappedDEK,
		"keyId":      envelope.KeyID,
		"kekVersion": envelope.KEKVersion,
		"alg":        envelope.Alg,
		"createdAt":  "2026-08-01T00:00:00Z",
		"shredded":   false,
	}, false)

	reqID := submitRetentionShred(t, ctx, conn, cp, cons, rcHolderExisting, "RCShredMark", processor.OutcomeAccepted)

	data := rcPiiKeyData(t, ctx, conn, rcHolderExisting)
	if data["shredded"] != true {
		t.Fatalf("shredded not flipped true: %+v", data)
	}
	if data["shreddedAt"] == "" || data["shreddedAt"] == nil {
		t.Fatalf("shreddedAt not stamped: %+v", data)
	}
	// The real wrapped DEK is PRESERVED, not zeroed — the same posture
	// ShredIdentityKey has: the durable flag is what denies, and the bytes stay
	// so an operator can still see an envelope existed.
	if got := rcStoredEnvelope(t, ctx, conn, rcHolderExisting); !bytes.Equal(got.WrappedDEK, envelope.WrappedDEK) {
		t.Fatalf("wrappedDEK not preserved: got %x want %x", got.WrappedDEK, envelope.WrappedDEK)
	}
	if data["keyId"] != rcHolderExisting {
		t.Fatalf("keyId not preserved: got %v want %s", data["keyId"], rcHolderExisting)
	}
	assertOutboxEventClass(t, ctx, conn, reqID, "privacy.retentionClassKeyShredded")
}

// TestShredRetentionClassKey_NoPiiKeyYet_WritesDurablePlaceholder — a class
// that never received a sensitive write still gets a durable empty-wrappedDEK
// envelope, so a write arriving after a Processor restart cannot mint a fresh
// unshredded class DEK against an in-memory-only deny-list.
func TestShredRetentionClassKey_NoPiiKeyYet_WritesDurablePlaceholder(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "rcshred-placeholder", v)

	seedRetentionClassHolder(t, ctx, conn, rcHolderNoKey, "underwritingRecord")
	if kvExists(t, ctx, conn, rcHolderNoKey+".piiKey") {
		t.Fatalf("precondition: %s.piiKey should not exist yet", rcHolderNoKey)
	}

	reqID := submitRetentionShred(t, ctx, conn, cp, cons, rcHolderNoKey, "RCShredPlaceholder", processor.OutcomeAccepted)

	data := rcPiiKeyData(t, ctx, conn, rcHolderNoKey)
	if data["shredded"] != true {
		t.Fatalf("placeholder not shredded: %+v", data)
	}
	if data["wrappedDEK"] != "" {
		t.Fatalf("placeholder wrappedDEK should be empty, got %v", data["wrappedDEK"])
	}
	if data["keyId"] != rcHolderNoKey {
		t.Fatalf("placeholder keyId: got %v want %s", data["keyId"], rcHolderNoKey)
	}
	assertOutboxEventClass(t, ctx, conn, reqID, "privacy.retentionClassKeyShredded")
}

// TestShredRetentionClassKey_AbsentHolder_Rejected — an absent holder is
// refused, and no piiKey is written for it.
//
// The refusal is a HYDRATION failure, not the script's own `vertex_alive`
// guard — measured, not assumed. A compliant dispatcher declares the holder in
// ContextHint.Reads (which is what makes the liveness check meaningful at all),
// and step 4 records an absent declared read as required-absent, so the script
// faults the moment it touches the key. The script's `NotFound: … is absent or
// tombstoned` is therefore reachable only via a dispatcher that omitted the
// declaration — where it would fire for a perfectly LIVE class. Both paths
// refuse, which is the property asserted here; the tombstoned case below is
// what exercises the script guard directly, since step 4 hydrates a
// logically-deleted doc rather than faulting on it.
func TestShredRetentionClassKey_AbsentHolder_Rejected(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "rcshred-absent", testutil.TestVault(t))
	submitRetentionShred(t, ctx, conn, cp, cons, rcHolderAbsent, "RCShredAbsent", processor.OutcomeRejected)
	if kvExists(t, ctx, conn, rcHolderAbsent+".piiKey") {
		t.Fatalf("a rejected shred must not write a piiKey for %s", rcHolderAbsent)
	}
}

// TestShredRetentionClassKey_TombstonedHolder_Rejected — a tombstoned holder
// is refused, so a retired class cannot have its key destroyed by name after
// the fact.
func TestShredRetentionClassKey_TombstonedHolder_Rejected(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "rcshred-tombstoned", testutil.TestVault(t))
	seedVertex(t, ctx, conn, rcHolderTombstoned, "retentionclass", nil, true)
	submitRetentionShred(t, ctx, conn, cp, cons, rcHolderTombstoned, "RCShredTombstoned", processor.OutcomeRejected)
}

// TestShredRetentionClassKey_WrongHolderType_Rejected — an identity key is
// refused by the type guard. This is the negative that keeps the two
// destruction verbs from being interchangeable: routing an identity through
// the class verb would destroy the right key while recording it in the wrong
// vocabulary, and nothing downstream would notice.
func TestShredRetentionClassKey_WrongHolderType_Rejected(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "rcshred-wrongtype", testutil.TestVault(t))
	identityKey := "vtx.identity." + "RCwrngTYPEabcdefghij"
	seedVertex(t, ctx, conn, identityKey, "identity", nil, false)
	submitRetentionShred(t, ctx, conn, cp, cons, identityKey, "RCShredWrongType", processor.OutcomeRejected)
	if kvExists(t, ctx, conn, identityKey+".piiKey") {
		t.Fatalf("a type-rejected shred must not write a piiKey for %s", identityKey)
	}
}

// TestShredRetentionClassKey_Reshred_ClearsPriorFinalization — a re-shred
// starts a NEW finalization cycle, so the operator lens shows it in flight
// rather than inheriting the previous cycle's completed flags.
func TestShredRetentionClassKey_Reshred_ClearsPriorFinalization(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "rcshred-reshred", v)

	seedRetentionClassHolder(t, ctx, conn, rcHolderReshred, "clinicalRecord")
	seedAspectDoc(t, ctx, conn, rcHolderReshred+".piiKey", rcHolderReshred, "piiKey", map[string]any{
		"wrappedDEK":           "AAAA",
		"keyId":                rcHolderReshred,
		"kekVersion":           "v1",
		"alg":                  "AES-256-GCM",
		"createdAt":            "2026-08-01T00:00:00Z",
		"shredded":             true,
		"shreddedAt":           "2026-08-02T00:00:00Z",
		"vaultKeyDestroyed":    true,
		"vaultKeyDestroyedAt":  "2026-08-02T00:01:00Z",
		"projectionsRebuilt":   true,
		"projectionsRebuiltAt": "2026-08-02T00:02:00Z",
	}, false)

	submitRetentionShred(t, ctx, conn, cp, cons, rcHolderReshred, "RCShredReshred", processor.OutcomeAccepted)

	data := rcPiiKeyData(t, ctx, conn, rcHolderReshred)
	for _, stale := range []string{"vaultKeyDestroyed", "vaultKeyDestroyedAt", "projectionsRebuilt", "projectionsRebuiltAt"} {
		if _, present := data[stale]; present {
			t.Fatalf("re-shred left prior cycle's %s in place: %+v", stale, data)
		}
	}
	if data["shredded"] != true {
		t.Fatalf("re-shred did not keep shredded=true: %+v", data)
	}
}

// TestRecordRetentionClassShredFinalization_RequiresPriorShred — the
// finalization fail-closes on an envelope that is absent, and on one that
// exists but is not shredded. A finalization can only ever follow a commit.
//
// The two absences are refused by two DIFFERENT mechanisms, and the test
// asserts the outcome rather than the mechanism deliberately. Case (a) never
// reaches the script: the submitter declares the piiKey in ContextHint.Reads
// (which is what makes the record OCC-conditioned), so a genuinely absent key
// is a HydrationMiss — measured, not assumed. Case (b) reaches the script and
// hits its own FailedPrecondition. Both are the same refusal from the caller's
// side, which is the property that matters; a reader who needs the script's
// guard exercised directly should look at case (b).
func TestRecordRetentionClassShredFinalization_RequiresPriorShred(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "rcfinal-guards", v)

	seedRetentionClassHolder(t, ctx, conn, rcHolderFinalize, "clinicalRecord")

	// (a) no envelope at all.
	submitRetentionFinalization(t, ctx, conn, cp, cons, rcHolderFinalize,
		"vaultKeyDestroyed", "RCFinalNoEnvelope", processor.OutcomeRejected)

	// (b) an envelope that exists but was never shredded.
	seedAspectDoc(t, ctx, conn, rcHolderFinalize+".piiKey", rcHolderFinalize, "piiKey", map[string]any{
		"wrappedDEK": "AAAA", "keyId": rcHolderFinalize, "kekVersion": "v1",
		"alg": "AES-256-GCM", "createdAt": "2026-08-01T00:00:00Z", "shredded": false,
	}, false)
	submitRetentionFinalization(t, ctx, conn, cp, cons, rcHolderFinalize,
		"vaultKeyDestroyed", "RCFinalNotShredded", processor.OutcomeRejected)

	// (c) an unknown step is refused even once the shred has landed.
	submitRetentionShred(t, ctx, conn, cp, cons, rcHolderFinalize, "RCFinalShred", processor.OutcomeAccepted)
	submitRetentionFinalization(t, ctx, conn, cp, cons, rcHolderFinalize,
		"projectionsNullified", "RCFinalWrongStep", processor.OutcomeRejected)

	// (c2) projectionsRebuilt is DECLARED but refused: its Refractor producer
	// does not exist, so any value it could hold today would attest a rebuild
	// that never ran — and this verb is granted to operator at scope:any, so
	// "nothing submits it" is not a guarantee.
	submitRetentionFinalization(t, ctx, conn, cp, cons, rcHolderFinalize,
		"projectionsRebuilt", "RCFinalNoProducer", processor.OutcomeRejected)
	if data := rcPiiKeyData(t, ctx, conn, rcHolderFinalize); data["projectionsRebuilt"] != nil {
		t.Fatalf("a refused step must write nothing: %+v", data)
	}

	// (d) the happy path flips exactly the named flag.
	submitRetentionFinalization(t, ctx, conn, cp, cons, rcHolderFinalize,
		"vaultKeyDestroyed", "RCFinalOK", processor.OutcomeAccepted)
	data := rcPiiKeyData(t, ctx, conn, rcHolderFinalize)
	if data["vaultKeyDestroyed"] != true {
		t.Fatalf("vaultKeyDestroyed not recorded: %+v", data)
	}
	if _, present := data["projectionsRebuilt"]; present {
		t.Fatalf("recording one step must not touch the other: %+v", data)
	}
}

// TestShredRetentionClassKey_EndToEnd_VaultDecryptFails — the fire's
// acceptance criterion, end to end: a record custodied on a retention class is
// readable, and stops being readable once the class's key is destroyed. The
// privacy-worker's own consumer is what carries the op's recorded intent into
// the Vault, so this exercises the whole spine rather than the script alone.
func TestShredRetentionClassKey_EndToEnd_VaultDecryptFails(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "rcshred-e2e", v)

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

	seedRetentionClassHolder(t, ctx, conn, rcHolderEndToEnd, "clinicalRecord")
	envelope, err := v.CreateIdentityKey(ctx, rcHolderEndToEnd)
	if err != nil {
		t.Fatalf("mint class DEK: %v", err)
	}
	seedAspectDoc(t, ctx, conn, rcHolderEndToEnd+".piiKey", rcHolderEndToEnd, "piiKey", map[string]any{
		"wrappedDEK": envelope.WrappedDEK,
		"keyId":      envelope.KeyID,
		"kekVersion": envelope.KEKVersion,
		"alg":        envelope.Alg,
		"createdAt":  "2026-08-01T00:00:00Z",
		"shredded":   false,
	}, false)

	ct, err := v.Encrypt(ctx, rcHolderEndToEnd, envelope, []byte(`{"summary":"a retained clinical note"}`))
	if err != nil {
		t.Fatalf("encrypt under the class holder: %v", err)
	}
	if _, err := v.Decrypt(ctx, rcHolderEndToEnd, envelope, ct); err != nil {
		t.Fatalf("precondition: Decrypt before the shred failed: %v", err)
	}

	submitRetentionShred(t, ctx, conn, cp, cons, rcHolderEndToEnd, "RCShredE2E", processor.OutcomeAccepted)

	// Poll for the two async hops (outbox publish, then the worker's consume +
	// Vault.ShredKey) to land.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := v.Decrypt(ctx, rcHolderEndToEnd, envelope, ct)
		if errors.Is(err, vault.ErrKeyShredded) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Decrypt did not fail with ErrKeyShredded within 10s of ShredRetentionClassKey committing (got err=%v)", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestShredRetentionClassKey_NonOperatorActor_Denied drives the real
// capability auth path against the widest-blast-radius verb in the package: an
// actor with no ShredRetentionClassKey grant is DENIED at step 3, and the
// class's key aspect is untouched.
//
// This matters more here than for its identity sibling. privacy-base ships no
// grant for this verb precisely because one call destroys every record a class
// holds, for subjects who never asked for anything — so the claim that an
// ungranted actor cannot reach it needs a test, not an argument.
func TestShredRetentionClassKey_NonOperatorActor_Denied(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "rcshred-denied", v)

	const holderKey = "vtx.retentionclass.RCkeyDENYabcdefghijk"
	seedRetentionClassHolder(t, ctx, conn, holderKey, "clinicalRecord")
	seedAspectDoc(t, ctx, conn, holderKey+".piiKey", holderKey, "piiKey", map[string]any{
		"wrappedDEK": "AAAA", "keyId": holderKey, "kekVersion": "v1",
		"alg": "AES-256-GCM", "createdAt": "2026-08-01T00:00:00Z", "shredded": false,
	}, false)

	const nonOpActorID = "RCnnpActrHJKMNPQRSTU"
	const nonOpActorKey = "vtx.identity." + nonOpActorID
	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + nonOpActorID,
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

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RCShredDenyOp"),
		Lane:          processor.LaneUrgent,
		OperationType: "ShredRetentionClassKey",
		Actor:         nonOpActorKey,
		SubmittedAt:   "2026-08-08T10:10:00Z",
		Class:         rcRetentionClassDDL,
		Payload:       json.RawMessage(`{"retentionClassKey":"` + holderKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{holderKey},
			OptionalReads: []string{holderKey + ".piiKey"},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	data := rcPiiKeyData(t, ctx, conn, holderKey)
	if shredded, _ := data["shredded"].(bool); shredded {
		t.Fatalf("a denied ShredRetentionClassKey marked piiKey.shredded = true")
	}
}

// TestRecordRetentionClassShredFinalization_ClassLessResolves proves the shape
// the PRODUCTION submitter actually publishes: internal/privacyworker sends the
// op with NO `class`, relying on the Processor's operationType→class reverse
// index to resolve it to the shredRetentionClassKey DDL.
//
// Every other test here sets Class explicitly, which would mask exactly the
// failure that matters: buildByCommand DROPS an ambiguous command with only a
// Warn, after which a class-less submit fails the explicit-class requirement
// and the worker's finalization record silently never lands — leaving every
// destruction reading as in-flight forever on the operator surface.
func TestRecordRetentionClassShredFinalization_ClassLessResolves(t *testing.T) {
	ctx, conn := setupShredEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newUrgentPipeline(t, ctx, conn, "rcfinal-classless", v)

	const holderKey = "vtx.retentionclass.RCkeyCLSSabcdefghijk"
	seedRetentionClassHolder(t, ctx, conn, holderKey, "clinicalRecord")
	submitRetentionShred(t, ctx, conn, cp, cons, holderKey, "RCClassLessShred", processor.OutcomeAccepted)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("RCClassLessFin"),
		Lane:          processor.LaneUrgent,
		OperationType: "RecordRetentionClassShredFinalization",
		Actor:         pbStaffActorKey,
		SubmittedAt:   "2026-08-08T10:20:00Z",
		// No Class — the reverse index must resolve it.
		Payload:     json.RawMessage(`{"retentionClassKey":"` + holderKey + `","step":"vaultKeyDestroyed"}`),
		ContextHint: &processor.ContextHint{Reads: []string{holderKey + ".piiKey"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if data := rcPiiKeyData(t, ctx, conn, holderKey); data["vaultKeyDestroyed"] != true {
		t.Fatalf("class-less finalization did not record vaultKeyDestroyed: %+v", data)
	}
}
