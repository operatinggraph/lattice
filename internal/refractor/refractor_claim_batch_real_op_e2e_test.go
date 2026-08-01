// Drives the REAL ClaimIdentity Starlark op — through a real Processor
// CommitPath, not a hand-built atomic batch — alongside a live, CDC-driven
// capabilityRoles Refractor pipeline in the SAME test process.
// facet-staff-worlds-design.md §13.3's checkpoint: three independent
// harnesses already project this exact write shape cleanly (bare NATS
// delivery, sequential Put through the live pipeline, and a hand-built
// atomic-batch commit through the live pipeline via
// TestRefractor_CapabilityLens_ClaimBatchAtomicUnconditionedLink_E2E, this
// package). The one variable none of them covers is the real
// ClaimIdentity script's exact mutation encoding — this test drives that,
// via testutil.CapabilityPipeline + InstallPhase1Packages, the exact
// machinery packages/identity-domain/claim_test.go already uses.
package refractor_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// realOpSHA256HexOf returns the hex-encoded SHA-256 hash of s, mirroring
// identity-domain's own claim-key hashing (packages/identity-domain's
// sha256HexOf), reproduced here since that helper is private to another
// package's test files.
func realOpSHA256HexOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// realOpIdentityIDFromRequestID mirrors identity-domain's
// identityIDFromRequestID: the identity DDL's Starlark derives the created
// identity's NanoID deterministically from the requestId, so a test can
// predict the created identity's key without reading a reply.
func realOpIdentityIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

// TestRefractor_CapabilityLens_RealClaimIdentityOp_E2E is
// facet-staff-worlds-design.md §13.3's checkpoint, candidate (2): drive the
// real ClaimIdentity op alongside a live capabilityRoles pipeline in the
// same test. A pass narrows the still-open live gap to something the live
// stack has that this harness doesn't (production load/timing, or another
// consumer/lens also reacting to the same holdsRole link); a failure
// captures the bug's minimal repro at last.
func TestRefractor_CapabilityLens_RealClaimIdentityOp_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-ClaimIdentity-op e2e test in -short mode")
	}

	// --- embedded NATS + full platform bucket/stream provisioning (incl.
	// RefractorAdjacencyKV + AllowAtomicPublish on Core KV) ---
	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	// --- install rbac-domain + privacy-base + identity-domain +
	// identity-hygiene for real: the ClaimIdentity op script + its op-meta,
	// and the capabilityRoles lens spec, all land in Core KV via real
	// InstallPackage ops (step-6 validation + step-8 atomic commit), not a
	// test stand-in. ---
	testutil.InstallPhase1Packages(t, ctx, conn)

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capabilityKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	// --- adjacency bootstrapper ---
	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	// --- the real rbac-domain capabilityRoles lens, live-CDC-driven, wired
	// identically to TestRefractor_CapabilityLens_ClaimBatchAtomicUnconditionedLink_E2E
	// (this package) so the ONLY variable against that already-green harness
	// is the write path: the real ClaimIdentity Starlark script driven
	// through a real Processor CommitPath, not a hand-built atomic batch. ---
	rolesSpec := capabilityRolesSpecForTest(t)
	fullEngine := full.New()
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	rolesCR, err := fullEngine.Parse(rolesSpec.Spec)
	require.NoError(t, err, "capabilityRoles spec must parse")
	rolesDesc := descFromPkgSpec(t, rolesSpec)
	capAdpt, err := adapter.New(capabilityKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)

	const rolesLensID = "ReaLCLaimRoLesLensid"
	capP, err := pipeline.New(rolesLensID, "nats_kv",
		bootstrap.CoreKVBucket, adjKV, coreKV, capAdpt, nil)
	require.NoError(t, err)
	capP.UseFullEngine(fullEngine, rolesCR)
	capP.SetEnvelopeFn(rolesDesc.EnvelopeFn("vtx.meta."+rolesLensID, projectionRevision))
	capP.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, rolesDesc.AnchorType))
	capP.SetActorDeleteKey(rolesDesc.BuildKey)

	capP.RunOn(conn, e2eSpec(rolesLensID, bootstrap.CoreKVBucket))

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	capDone := make(chan struct{})
	go func() { defer close(capDone); capP.Run(pipelineCtx) }()
	t.Cleanup(func() {
		pipelineCancel()
		<-capDone
	})

	// --- real op-driving CommitPath (default lane) ---
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "realclaimops",
	})

	// --- actors: staff (creates the unclaimed identity), consumer (claims
	// it). Both cap docs are seeded directly, bypassing live projection —
	// mirrors packages/identity-domain/claim_test.go's own pattern, and is
	// deliberately NOT the thing under test: what's under test is whether
	// the CLAIMED identity's OWN post-claim holdsRole grant (the R2
	// refinement, a target of the op, not the caller) projects, not
	// whether the caller's pre-existing permissions do. ---
	staffActorKey := "vtx.identity." + stableClaimBatchID("real-op-staff-actor")
	consumerActorKey := "vtx.identity." + stableClaimBatchID("real-op-consumer-actor")
	now := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + staffActorKey[len("vtx.identity."):],
		Actor:                  staffActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{staffActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateUnclaimedIdentity", Scope: "any"},
		},
		Roles: []string{bootstrap.RoleOperatorKey},
	})
	testutil.SeedHoldsRole(t, ctx, conn, staffActorKey, bootstrap.RoleOperatorKey)
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + consumerActorKey[len("vtx.identity."):],
		Actor:                  consumerActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{consumerActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ClaimIdentity", Scope: "self"},
		},
		Roles: []string{"vtx.role.consumer"},
	})

	// --- arrange: a real CreateUnclaimedIdentity op ---
	createReqID := testutil.GenReqID("RealClmCreate0")
	identityID := realOpIdentityIDFromRequestID(createReqID)
	identityKey := "vtx.identity." + identityID
	claimKeyPlaintext := "claim-secret-for-" + createReqID
	claimKeyHash := realOpSHA256HexOf(claimKeyPlaintext)

	createEnv := &processor.OperationEnvelope{
		RequestID:     createReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-29T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"Real Claim Test","email":"realclaim@claim.example","claimKeyHash":"` + claimKeyHash + `"}`),
	}
	testutil.PublishOp(t, conn, createEnv)
	outcome := testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	require.Equal(t, processor.OutcomeAccepted, outcome, "CreateUnclaimedIdentity must be accepted")

	// --- Sanity: no role held yet, cap doc absent for the claimed identity. ---
	capKey := "cap.roles.identity." + identityID
	capDocAbsent := func() bool {
		_, gErr := capabilityKV.Get(ctx, capKey)
		return errors.Is(gErr, substrate.ErrKeyNotFound)
	}
	require.Eventually(t, capDocAbsent, 20*time.Second, 100*time.Millisecond,
		"identity cap doc must stay absent before the claim")

	// --- act: the REAL ClaimIdentity op — same envelope shape production's
	// cmd/facet/claim.go and packages/identity-domain/claim_test.go use. ---
	claimReqID := testutil.GenReqID("RealClmClaim00")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         consumerActorKey,
		SubmittedAt:   "2026-07-29T10:01:00Z",
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
	outcome = testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	require.Equal(t, processor.OutcomeAccepted, outcome, "ClaimIdentity must be accepted")

	// --- assert: does the real op's own holdsRole grant fan out into
	// cap.roles.<claimedIdentity>? ---
	hasConsumerRole := func(env map[string]any) bool {
		roles, _ := env["roles"].([]any)
		for _, r := range roles {
			if s, ok := r.(string); ok && s == "vtx.role."+pkgmgr.RoleID("identity-domain", "consumer") {
				return true
			}
		}
		pp, _ := env["platformPermissions"].([]any)
		for _, e := range pp {
			m, ok := e.(map[string]any)
			if ok && m["operationType"] == "InitiateCredentialLink" && m["scope"] == "self" {
				return true
			}
		}
		return false
	}
	getEnv := func() (map[string]any, bool) {
		entry, gErr := capabilityKV.Get(ctx, capKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return nil, false
		}
		var env map[string]any
		if jerr := json.Unmarshal(entry.Value, &env); jerr != nil {
			return nil, false
		}
		return env, true
	}

	projected := false
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if env, ok := getEnv(); ok && hasConsumerRole(env) {
			projected = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !projected {
		env, _ := getEnv()
		t.Logf("MINIMAL REPRO CAPTURED (real op): cap.roles.%s never gained the consumer-role "+
			"grant from the REAL ClaimIdentity op's own holdsRole write within 25s (last read: "+
			"%+v). Three other harnesses (bare NATS delivery, sequential Put, hand-built atomic "+
			"batch) all project this write shape cleanly — the defect is specific to the real "+
			"script's own mutation encoding.", identityID, env)
	} else {
		t.Logf("NOT REPRODUCED IN THIS HARNESS: cap.roles.%s projected the consumer-role grant "+
			"from the real ClaimIdentity op. Candidate (2) is cleared — escalate to candidate (3) "+
			"(a second live consumer/lens on the same holdsRole link, e.g. capabilityEphemeral) "+
			"or instrument the live stack directly (candidate 1: production load/timing).", identityID)
	}
	require.True(t, projected,
		"cap.roles.<target> must project the role-derived grant from the REAL ClaimIdentity "+
			"op's own holdsRole write — see the preceding t.Logf for repro details")
}
