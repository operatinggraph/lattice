package privacybase_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	"github.com/operatinggraph/lattice/internal/vault"
)

const (
	pbPrivacyActorID  = "BBshredFinHJKMNPQRST"
	pbPrivacyActorKey = "vtx.identity." + pbPrivacyActorID
	pbPrivacyCapKey   = "cap.identity." + pbPrivacyActorID
)

// privacyCapDoc grants RecordShredFinalization on the system lane — the
// grant shape the identity.system.privacy service actor carries in
// production (operator-equivalent; the Fire-4b finalization listeners submit
// under it on ops.system).
func privacyCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    pbPrivacyCapKey,
		Actor:                  pbPrivacyActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{pbPrivacyActorKey: 1},
		Lanes:                  []string{"system"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "RecordShredFinalization", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

func newSystemPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string, v vault.Vault) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:        durable,
		Instance:       "shredfin-" + durable,
		Vault:          v,
		FilterSubjects: []string{"ops.system"},
	})
}

// submitFinalization publishes one RecordShredFinalization and drives it to
// wantOutcome. Class-less, with the piiKey declared in ContextHint.Reads —
// exactly the envelope the real listeners (internal/privacyworker,
// internal/refractor/keyshredded) publish, so the operationType→class
// reverse-index inference AND the hydrated OCC conditioning are exercised.
func submitFinalization(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, identityKey, step, reqLabel string, wantOutcome processor.MessageOutcome) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(reqLabel),
		Lane:          processor.LaneSystem,
		OperationType: "RecordShredFinalization",
		Actor:         pbPrivacyActorKey,
		SubmittedAt:   "2026-07-02T10:20:00Z",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `","step":"` + step + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey + ".piiKey"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, wantOutcome)
}

func setupFinalizationEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, staffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, privacyCapDoc())
	return ctx, conn
}

func piiKeyData(t *testing.T, ctx context.Context, conn *substrate.Conn, identityKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, identityKey+".piiKey")
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("piiKey doc for %s has no data map: %v", identityKey, doc)
	}
	return data
}

// TestRecordShredFinalization_RecordsBothSteps drives the full Fire-4b record
// sequence against a real shredded envelope: vaultKeyDestroyed then
// projectionsNullified each flip their boolean (+ At stamp) while preserving
// the rest of the envelope (wrappedDEK, shredded), and a duplicate record of
// an already-recorded step is idempotent (accepted, value unchanged).
func TestRecordShredFinalization_RecordsBothSteps(t *testing.T) {
	ctx, conn := setupFinalizationEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "fin-both-default", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "fin-both-urgent", v)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "fin-both-system", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "FinBothIdent")
	recordPII(t, ctx, conn, cp, cons, identityKey, "FinBothPII")
	submitShred(t, ctx, conn, urgentCP, urgentCons, identityKey, "FinBothShred", processor.OutcomeAccepted)

	before := piiKeyData(t, ctx, conn, identityKey)
	if before["shredded"] != true {
		t.Fatalf("precondition: piiKey.shredded = %v, want true", before["shredded"])
	}
	wrappedDEK, _ := before["wrappedDEK"].(string)
	if wrappedDEK == "" {
		t.Fatal("precondition: piiKey.wrappedDEK empty — recordPII should have minted a real envelope")
	}

	submitFinalization(t, ctx, conn, sysCP, sysCons, identityKey, "vaultKeyDestroyed", "FinBothVKD", processor.OutcomeAccepted)
	after := piiKeyData(t, ctx, conn, identityKey)
	if after["vaultKeyDestroyed"] != true {
		t.Errorf("vaultKeyDestroyed = %v, want true", after["vaultKeyDestroyed"])
	}
	if at, _ := after["vaultKeyDestroyedAt"].(string); at == "" {
		t.Error("vaultKeyDestroyedAt not stamped")
	}
	if after["projectionsNullified"] == true {
		t.Error("projectionsNullified must not be set by the vaultKeyDestroyed record")
	}
	if after["shredded"] != true || after["wrappedDEK"] != wrappedDEK {
		t.Errorf("record must preserve the envelope: shredded=%v wrappedDEK-preserved=%v",
			after["shredded"], after["wrappedDEK"] == wrappedDEK)
	}

	submitFinalization(t, ctx, conn, sysCP, sysCons, identityKey, "projectionsNullified", "FinBothPN", processor.OutcomeAccepted)
	final := piiKeyData(t, ctx, conn, identityKey)
	if final["vaultKeyDestroyed"] != true || final["projectionsNullified"] != true {
		t.Errorf("both steps must be recorded: vaultKeyDestroyed=%v projectionsNullified=%v",
			final["vaultKeyDestroyed"], final["projectionsNullified"])
	}

	// Idempotent re-record (a redelivered event with a NEW requestId).
	submitFinalization(t, ctx, conn, sysCP, sysCons, identityKey, "vaultKeyDestroyed", "FinBothVKD2", processor.OutcomeAccepted)
	again := piiKeyData(t, ctx, conn, identityKey)
	if again["vaultKeyDestroyed"] != true || again["projectionsNullified"] != true {
		t.Errorf("re-record must be idempotent: %v", again)
	}
}

// TestRecordShredFinalization_UnshreddedPiiKey_Rejected — a finalization for
// an identity whose piiKey exists but was never shredded is a
// FailedPrecondition rejection: there is no shred to record progress for.
func TestRecordShredFinalization_UnshreddedPiiKey_Rejected(t *testing.T) {
	ctx, conn := setupFinalizationEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "fin-unshred-default", v)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "fin-unshred-system", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "FinUnshredId")
	recordPII(t, ctx, conn, cp, cons, identityKey, "FinUnshredPII") // mints an UNSHREDDED piiKey

	submitFinalization(t, ctx, conn, sysCP, sysCons, identityKey, "vaultKeyDestroyed", "FinUnshredRec", processor.OutcomeRejected)

	data := piiKeyData(t, ctx, conn, identityKey)
	if data["vaultKeyDestroyed"] == true {
		t.Error("rejected record must not have written vaultKeyDestroyed")
	}
}

// TestRecordShredFinalization_NoPiiKey_Rejected — a finalization for an
// identity with NO piiKey at all is a NotFound rejection (ShredIdentityKey
// always durably writes an envelope before its event exists). The identity is
// seeded directly: CreateUnclaimedIdentity would mint a piiKey via its
// sensitive name/email writes, which is exactly what this case must avoid.
func TestRecordShredFinalization_NoPiiKey_Rejected(t *testing.T) {
	ctx, conn := setupFinalizationEnv(t)
	v := testutil.TestVault(t)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "fin-nopii-system", v)

	const identityKey = "vtx.identity.BBfinNoPiiHJKMNPQRST"
	seedVertex(t, ctx, conn, identityKey, "identity", map[string]any{}, false)

	submitFinalization(t, ctx, conn, sysCP, sysCons, identityKey, "vaultKeyDestroyed", "FinNoPiiRec", processor.OutcomeRejected)

	if kvExists(t, ctx, conn, identityKey+".piiKey") {
		t.Error("rejected record must not have created a piiKey")
	}
}

// TestRecordShredFinalization_UnknownStep_Rejected — the step enum is closed;
// anything else is an InvalidArgument rejection.
func TestRecordShredFinalization_UnknownStep_Rejected(t *testing.T) {
	ctx, conn := setupFinalizationEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "fin-badstep-default", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "fin-badstep-urgent", v)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "fin-badstep-system", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "FinBadStepId")
	recordPII(t, ctx, conn, cp, cons, identityKey, "FinBadStepPII")
	submitShred(t, ctx, conn, urgentCP, urgentCons, identityKey, "FinBadStepShred", processor.OutcomeAccepted)

	submitFinalization(t, ctx, conn, sysCP, sysCons, identityKey, "shredded", "FinBadStepRec", processor.OutcomeRejected)

	data := piiKeyData(t, ctx, conn, identityKey)
	if data["shreddedAt"] == nil {
		t.Error("precondition drift: shred should have stamped shreddedAt")
	}
}

// TestRecordShredFinalization_ReShredResetsCycle — a re-shred of an
// already-finalized identity starts a NEW finalization cycle: the prior
// cycle's recorded booleans (+ stamps) are cleared by the ShredIdentityKey
// commit, so the shredStatus lens shows the new shred as in-flight until its
// own async records land.
func TestRecordShredFinalization_ReShredResetsCycle(t *testing.T) {
	ctx, conn := setupFinalizationEnv(t)
	v := testutil.TestVault(t)
	cp, cons := newDefaultPipeline(t, ctx, conn, "fin-reshred-default", v)
	urgentCP, urgentCons := newUrgentPipeline(t, ctx, conn, "fin-reshred-urgent", v)
	sysCP, sysCons := newSystemPipeline(t, ctx, conn, "fin-reshred-system", v)

	identityKey := createIdentity(t, ctx, conn, cp, cons, "FinReshredId")
	recordPII(t, ctx, conn, cp, cons, identityKey, "FinReshredPII")
	submitShred(t, ctx, conn, urgentCP, urgentCons, identityKey, "FinReshredOne", processor.OutcomeAccepted)
	submitFinalization(t, ctx, conn, sysCP, sysCons, identityKey, "vaultKeyDestroyed", "FinReshredVKD", processor.OutcomeAccepted)
	submitFinalization(t, ctx, conn, sysCP, sysCons, identityKey, "projectionsNullified", "FinReshredNul", processor.OutcomeAccepted)

	submitShred(t, ctx, conn, urgentCP, urgentCons, identityKey, "FinReshredTwo", processor.OutcomeAccepted)

	data := piiKeyData(t, ctx, conn, identityKey)
	if data["shredded"] != true {
		t.Errorf("re-shred must keep shredded=true; got %v", data["shredded"])
	}
	for _, stale := range []string{"vaultKeyDestroyed", "vaultKeyDestroyedAt", "projectionsNullified", "projectionsNullifiedAt"} {
		if _, present := data[stale]; present {
			t.Errorf("re-shred must clear prior-cycle %s; still present: %v", stale, data[stale])
		}
	}
}
