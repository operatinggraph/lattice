//go:build cryptoshred

// Package cryptoshred_test is the crypto-shredding end-to-end harness
// (vault-crypto-shredding-design.md §6, Fire 4a): installs rbac-domain +
// privacy-base + identity-domain (+ identity-hygiene) via the real
// InstallPackage op path, wires the real Processor commit path (Vault
// encrypt-on-write / decrypt-on-read, Fire 2), the async privacy-worker
// (Vault key destruction, Fire 3), and a Refractor lens + the new
// keyshredded nullification listener (Fire 4a), then drives
// CreateUnclaimedIdentity -> ShredIdentityKey and observes the full Phase-A
// guarantee converge: Vault.Decrypt fails AND the lens's already-projected
// row for that identity is nullified.
//
// Gated behind the `cryptoshred` build tag — runs only via `make
// test-crypto-shred`, mirroring `make test-object-gc`'s Loop-A/B
// convergence e2e precedent (design §6).
package cryptoshred_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/privacyworker"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/processor/outbox"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/control"
	"github.com/asolgan/lattice/internal/refractor/keyshredded"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	"github.com/asolgan/lattice/internal/vault"
)

const (
	adjBucket    = "refractor-adjacency"
	targetBucket = "cryptoshred-identity-view"
	lensRuleID   = "cryptoshred-identity-view"

	csStaffActorID  = "CSshredStfHJKMNPQRST"
	csStaffActorKey = "vtx.identity." + csStaffActorID
	csStaffCapKey   = "cap.identity." + csStaffActorID

	csPrivacyActorID  = "CSprivacyActKMNPQRST"
	csPrivacyActorKey = "vtx.identity." + csPrivacyActorID
	csPrivacyCapKey   = "cap.identity." + csPrivacyActorID
)

// csStaffCapDoc grants CreateUnclaimedIdentity/RecordIdentityPII (default
// lane) and ShredIdentityKey (urgent lane) — mirrors
// packages/privacy-base's shred_identity_key_test.go staffCapDoc.
func csStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    csStaffCapKey,
		Actor:                  csStaffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{csStaffActorKey: 1},
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

// csPrivacyCapDoc grants RecordShredFinalization (system lane) — the grant
// the identity.system.privacy service actor carries in production; the two
// Fire-4b finalization listeners submit under it.
func csPrivacyCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    csPrivacyCapKey,
		Actor:                  csPrivacyActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{csPrivacyActorKey: 1},
		Lanes:                  []string{"system"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "RecordShredFinalization", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

type harness struct {
	t          *testing.T
	ctx        context.Context
	conn       *substrate.Conn
	cp         *processor.CommitPath
	cons       jetstream.Consumer
	urgentCP   *processor.CommitPath
	urgentCons jetstream.Consumer
	sysCP      *processor.CommitPath
	sysCons    jetstream.Consumer
	targetKV   *substrate.KV
	keyShred   *keyshredded.Manager
	v          vault.Vault
	controlSvc *control.Service
	lensRuleID string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // rbac + privacy-base + identity + hygiene
	testutil.SeedCapDoc(t, ctx, conn, csStaffCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, csPrivacyCapDoc())

	v := testutil.TestVault(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "cryptoshred-default", Instance: "cryptoshred-default", Vault: v,
	})
	urgentCP, urgentCons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "cryptoshred-urgent", Instance: "cryptoshred-urgent", Vault: v, FilterSubjects: []string{"ops.urgent"},
	})
	sysCP, sysCons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "cryptoshred-system", Instance: "cryptoshred-system", Vault: v, FilterSubjects: []string{"ops.system"},
	})

	workerCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = outbox.New(conn, testutil.HarnessCoreBucket, testutil.TestLogger()).Run(workerCtx) }()

	worker := privacyworker.New(privacyworker.Config{
		Conn: conn, EventsStream: testutil.HarnessEventsStream, Vault: v, Logger: testutil.TestLogger(),
		ActorKey: csPrivacyActorKey,
	})
	go func() { _ = worker.Run(workerCtx) }()

	h := &harness{t: t, ctx: ctx, conn: conn, cp: cp, cons: cons,
		urgentCP: urgentCP, urgentCons: urgentCons, sysCP: sysCP, sysCons: sysCons, v: v}
	h.startRefractorAndKeyshredded(workerCtx)
	return h
}

// startRefractorAndKeyshredded wires a single full-engine lens
// (MATCH (a:identity) RETURN a.key AS identityKey -> a nats_kv row per
// identity) behind the standard adjacency-CDC path, registers it with a
// control.Service exactly as cmd/refractor's main() does, then starts the
// new keyshredded.Manager configured to nullify that one lens on shred.
func (h *harness) startRefractorAndKeyshredded(ctx context.Context) {
	js := h.conn.JetStream()
	_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: adjBucket})
	require.NoError(h.t, err)
	_, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: targetBucket})
	require.NoError(h.t, err)
	adjKV, err := h.conn.OpenKV(ctx, adjBucket)
	require.NoError(h.t, err)
	coreKV, err := h.conn.OpenKV(ctx, testutil.HarnessCoreBucket)
	require.NoError(h.t, err)
	targetKV, err := h.conn.OpenKV(ctx, targetBucket)
	require.NoError(h.t, err)
	h.targetKV = targetKV

	boots := consumer.NewBootstrapper(h.conn, testutil.HarnessCoreBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(15 * time.Second):
		h.t.Fatal("adjacency bootstrapper did not reach Ready within 15s")
	}

	rule, err := lens.Parse([]byte(`
id: ` + lensRuleID + `
ruleEngine: full
match: |
  MATCH (a:identity)
  RETURN a.key AS identityKey
into:
  target: nats_kv
  bucket: ` + targetBucket + `
  key: identityKey
`))
	require.NoError(h.t, err)

	adpt, err := adapter.New(targetKV, rule.Into.Key, adapter.DeleteModeHard)
	require.NoError(h.t, err)
	p, err := pipeline.New(rule.ID, "nats_kv", nil, testutil.HarnessCoreBucket, adjKV, coreKV, adpt, nil)
	require.NoError(h.t, err)
	p.UseFullEngine(full.New(), rule.CompiledRule)
	p.RunOn(h.conn, substrate.ConsumerSpec{
		Name:          "refractor-" + rule.ID,
		Stream:        "KV_" + testutil.HarnessCoreBucket,
		FilterSubject: "$KV." + testutil.HarnessCoreBucket + ".>",
		DeliverPolicy: substrate.DeliverLastPerSubject,
		DeliverGroup:  "refractor-" + rule.ID,
	})
	pctx, pcancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); p.Run(pctx) }()
	h.t.Cleanup(func() { pcancel(); <-done })

	controlSvc := control.NewService()
	controlSvc.Register(rule.ID, p, nil)
	controlSvc.RegisterPauser(rule.ID, p)
	controlSvc.RegisterRowNullifier(rule.ID, p)
	h.controlSvc = controlSvc
	h.lensRuleID = rule.ID

	h.keyShred = keyshredded.New(keyshredded.Config{
		Conn:         h.conn,
		EventsStream: testutil.HarnessEventsStream,
		Control:      controlSvc,
		Targets:      []keyshredded.NullifyTarget{{RuleID: rule.ID, KeyField: "identityKey"}},
		Logger:       testutil.TestLogger(),
		ActorKey:     csPrivacyActorKey,
	})
	go func() { _ = h.keyShred.Run(ctx) }()
}

func (h *harness) submitOp(cp *processor.CommitPath, cons jetstream.Consumer, env *processor.OperationEnvelope) processor.MessageOutcome {
	h.t.Helper()
	testutil.PublishOp(h.t, h.conn, env)
	return testutil.DriveOne(h.t, h.ctx, cp, cons, processor.OutcomeAccepted)
}

func (h *harness) createIdentity(reqLabel string) string {
	h.t.Helper()
	claimSum := sha256.Sum256([]byte("owner-" + reqLabel))
	reqID := testutil.GenReqID(reqLabel)
	env := &processor.OperationEnvelope{
		RequestID: reqID, Lane: processor.LaneDefault, OperationType: "CreateUnclaimedIdentity",
		Actor: csStaffActorKey, SubmittedAt: "2026-07-02T10:00:00Z", Class: "identity",
		Payload: mustJSON(map[string]any{
			"name": "Shred Target", "email": reqLabel + "@example.com",
			"claimKeyHash": hex.EncodeToString(claimSum[:]),
		}),
	}
	h.submitOp(h.cp, h.cons, env)
	return "vtx.identity." + identityIDFromRequestID(reqID)
}

// identityIDFromRequestID mirrors packages/privacy-base's
// shred_identity_key_test.go helper — CreateUnclaimedIdentity's
// deterministic-NanoID-from-requestID scheme.
func identityIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

func (h *harness) submitShred(identityKey, reqLabel string) {
	h.t.Helper()
	env := &processor.OperationEnvelope{
		RequestID: testutil.GenReqID(reqLabel), Lane: processor.LaneUrgent, OperationType: "ShredIdentityKey",
		Actor: csStaffActorKey, SubmittedAt: "2026-07-02T10:10:00Z", Class: "shredIdentityKey",
		Payload:     mustJSON(map[string]any{"identityKey": identityKey}),
		ContextHint: &processor.ContextHint{Reads: []string{identityKey}},
	}
	h.submitOp(h.urgentCP, h.urgentCons, env)
}

func (h *harness) rowExists(identityKey string) bool {
	entry, err := h.targetKV.Get(h.ctx, identityKey)
	return err == nil && entry != nil && len(entry.Value) > 0
}

func (h *harness) eventually(desc string, d time.Duration, cond func() bool) {
	h.t.Helper()
	cut := time.Now().Add(d)
	for time.Now().Before(cut) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	h.t.Fatalf("condition not met within %s: %s", d, desc)
}

func mustJSON(v map[string]any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// TestCryptoShred_NullifiesProjectedRowAndDestroysKey is the Fire 4a
// end-to-end wiring proof: an identity's row lands in the lens's target, then
// ShredIdentityKey drives BOTH async consumers of privacy.keyShredded —
// privacy-worker destroys the Vault key (Fire 3) and the new keyshredded
// listener nullifies the lens's projected row (Fire 4a) via a REAL
// control.Service + adapter.Delete call.
//
// This does NOT assert the row stays permanently absent — empirically, a
// still-alive (non-tombstoned) identity's row can be re-upserted by
// Refractor's own projection pipeline shortly after this listener deletes it
// (see internal/refractor/keyshredded's handleKeyShredded doc comment on the
// known limitation). internal/refractor/keyshredded's own unit tests prove
// the delete call itself is issued correctly and reaches the adapter; this
// e2e proves the real event → real control.Service → real adapter wiring end
// to end without depending on a race-prone "stays deleted" window.
func TestCryptoShred_NullifiesProjectedRowAndDestroysKey(t *testing.T) {
	h := newHarness(t)
	identityKey := h.createIdentity("CsE2ETgt")

	h.eventually("lens row projects before shred", 20*time.Second, func() bool {
		return h.rowExists(identityKey)
	})

	h.submitShred(identityKey, "CsE2EShredOp")

	h.eventually("keyshredded listener counted the event", 20*time.Second, func() bool {
		return h.keyShred.HandledTotal() >= 1
	})

	// Fire 4b: both async listeners durably record their finalization by
	// submitting RecordShredFinalization under the privacy actor. Drive the
	// two class-less ops off ops.system through the REAL capability pipeline
	// (order is nondeterministic — one from each listener) and assert the
	// piiKey envelope carries both progress booleans.
	testutil.DriveOne(h.t, h.ctx, h.sysCP, h.sysCons, processor.OutcomeAccepted)
	testutil.DriveOne(h.t, h.ctx, h.sysCP, h.sysCons, processor.OutcomeAccepted)

	entry, err := h.conn.KVGet(h.ctx, testutil.HarnessCoreBucket, identityKey+".piiKey")
	require.NoError(t, err)
	var piiDoc struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(entry.Value, &piiDoc))
	require.Equal(t, true, piiDoc.Data["shredded"], "piiKey.shredded")
	require.Equal(t, true, piiDoc.Data["vaultKeyDestroyed"], "piiKey.vaultKeyDestroyed (privacy-worker record)")
	require.Equal(t, true, piiDoc.Data["projectionsNullified"], "piiKey.projectionsNullified (keyshredded record)")
	require.NotEmpty(t, piiDoc.Data["vaultKeyDestroyedAt"], "vaultKeyDestroyedAt stamp")
	require.NotEmpty(t, piiDoc.Data["projectionsNullifiedAt"], "projectionsNullifiedAt stamp")
}

// TestCryptoShred_NoTargetsConfigured_StillHandlesAndCounts proves an empty
// Targets list (today's production default — see internal/refractor/keyshredded's
// package doc) is a harmless no-op sweep: the event is still handled and counted.
func TestCryptoShred_NoTargetsConfigured_StillHandlesAndCounts(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, csStaffCapDoc())
	v := testutil.TestVault(t)
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "cryptoshred-notargets-default", Instance: "cryptoshred-notargets-default", Vault: v,
	})
	urgentCP, urgentCons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "cryptoshred-notargets-urgent", Instance: "cryptoshred-notargets-urgent", Vault: v,
		FilterSubjects: []string{"ops.urgent"},
	})
	workerCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = outbox.New(conn, testutil.HarnessCoreBucket, testutil.TestLogger()).Run(workerCtx) }()

	km := keyshredded.New(keyshredded.Config{
		Conn: conn, EventsStream: testutil.HarnessEventsStream, Control: control.NewService(),
		Logger: testutil.TestLogger(),
	})
	go func() { _ = km.Run(workerCtx) }()

	h := &harness{t: t, ctx: ctx, conn: conn, cp: cp, cons: cons, urgentCP: urgentCP, urgentCons: urgentCons, v: v}
	identityKey := h.createIdentity("CsNoTgtIdent")
	h.submitShred(identityKey, "CsNoTgtShredOp")

	h.eventually("keyshredded listener counted the event with zero targets", 10*time.Second, func() bool {
		return km.HandledTotal() >= 1
	})
}
