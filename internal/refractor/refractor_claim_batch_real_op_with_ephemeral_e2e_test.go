// Drives the REAL ClaimIdentity Starlark op alongside TWO live, CDC-driven
// Refractor pipelines in the SAME test process — capabilityRoles (rbac-domain)
// AND capabilityEphemeral (orchestration-base), both anchored on "identity"
// and both reacting to the same claimed identity's holdsRole write.
// facet-staff-worlds-design.md §13.4's checkpoint, candidate (3): the four
// prior harnesses (bare NATS delivery, sequential Put, hand-built atomic
// batch, and the real op script itself) all project cap.roles.<target>
// cleanly with capabilityRoles running alone. This test adds a second live
// consumer/lens competing for the same adjacency bootstrapper + Core KV read
// path on the same identity/role graph, mirroring
// refractor_capability_faninstress_e2e_test.go's multi-lens wiring but
// keeping the live CDC pump (RunOn/Run) rather than that test's
// Reproject-driven determinism, since the live pump is what §13.3/§13.4
// proved reproduces the claim batch's real write shape.
package refractor_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// ephemeralLensSpecForTest returns orchestration-base's capabilityEphemeral
// LensSpec, selected by canonical name — the second live consumer §13.4's
// checkpoint names as candidate (3).
func ephemeralLensSpecForTest(t *testing.T) pkgmgr.LensSpec {
	t.Helper()
	for _, l := range orchestrationbase.Lenses() {
		if l.CanonicalName == "capabilityEphemeral" {
			return l
		}
	}
	require.FailNow(t, "orchestration-base must declare a capabilityEphemeral lens")
	return pkgmgr.LensSpec{}
}

// TestRefractor_CapabilityLens_RealClaimIdentityOp_WithEphemeralConsumer_E2E
// is facet-staff-worlds-design.md §13.4's checkpoint, candidate (3): does a
// second live consumer/lens (capabilityEphemeral) reacting to the same
// identity/role graph interfere with capabilityRoles' own projection of the
// real ClaimIdentity op's holdsRole grant? A pass narrows the still-open live
// gap to candidate (1) alone (production load/timing, needing direct
// instrumentation of the demo box); a failure captures the bug's minimal
// repro at last.
func TestRefractor_CapabilityLens_RealClaimIdentityOp_WithEphemeralConsumer_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-ClaimIdentity-op-with-ephemeral-consumer e2e test in -short mode")
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
	// identity-hygiene for real (the ClaimIdentity op script + its op-meta,
	// and the capabilityRoles lens spec, all landing in Core KV via real
	// InstallPackage ops). orchestration-base's capabilityEphemeral lens spec
	// is pulled directly (mirroring refractor_capability_faninstress_e2e_test.go
	// — that package need not be pkgmgr-installed for its Rule to be compiled
	// and driven as a second live pipeline). ---
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

	fullEngine := full.New()
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	// --- capabilityRoles: the real rbac-domain lens, live-CDC-driven,
	// wired identically to §13.4's TestRefractor_CapabilityLens_RealClaimIdentityOp_E2E. ---
	rolesSpec := capabilityRolesSpecForTest(t)
	rolesCR, err := fullEngine.Parse(rolesSpec.Spec)
	require.NoError(t, err, "capabilityRoles spec must parse")
	rolesDesc := descFromPkgSpec(t, rolesSpec)
	capAdpt, err := adapter.New(capabilityKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)

	const rolesLensID = "ReaLCLmEphRoLesLnsid"
	capP, err := pipeline.New(rolesLensID, "nats_kv",
		bootstrap.CoreKVBucket, adjKV, coreKV, capAdpt, nil)
	require.NoError(t, err)
	capP.UseFullEngine(fullEngine, rolesCR)
	capP.SetEnvelopeFn(rolesDesc.EnvelopeFn("vtx.meta."+rolesLensID, projectionRevision))
	capP.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, rolesDesc.AnchorType))
	capP.SetActorDeleteKey(rolesDesc.BuildKey)
	capP.RunOn(conn, e2eSpec(rolesLensID, bootstrap.CoreKVBucket))

	// --- capabilityEphemeral: the real orchestration-base lens, ALSO
	// live-CDC-driven, anchored on "identity" like capabilityRoles — the
	// second live consumer competing for the same adjacency bootstrapper +
	// Core KV read path on the same claimed identity. ---
	ephSpec := ephemeralLensSpecForTest(t)
	ephCR, err := fullEngine.Parse(ephSpec.Spec)
	require.NoError(t, err, "capabilityEphemeral spec must parse")
	ephDesc := descFromPkgSpec(t, ephSpec)
	ephAdpt, err := adapter.New(capabilityKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)

	const ephLensID = "ReaLCLmEphEphemLnsid"
	ephP, err := pipeline.New(ephLensID, "nats_kv",
		bootstrap.CoreKVBucket, adjKV, coreKV, ephAdpt, nil)
	require.NoError(t, err)
	ephP.UseFullEngine(fullEngine, ephCR)
	ephP.SetEnvelopeFn(ephDesc.EnvelopeFn("vtx.meta."+ephLensID, projectionRevision))
	ephP.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, ephDesc.AnchorType))
	ephP.SetActorDeleteKey(ephDesc.BuildKey)
	ephP.RunOn(conn, e2eSpec(ephLensID, bootstrap.CoreKVBucket))

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	capDone := make(chan struct{})
	go func() { defer close(capDone); capP.Run(pipelineCtx) }()
	ephDone := make(chan struct{})
	go func() { defer close(ephDone); ephP.Run(pipelineCtx) }()
	t.Cleanup(func() {
		pipelineCancel()
		<-capDone
		<-ephDone
	})

	// --- real op-driving CommitPath (default lane) ---
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "realclaimopswitheph",
	})

	// --- actors: staff (creates the unclaimed identity), consumer (claims
	// it). Both cap docs are seeded directly, bypassing live projection —
	// mirrors packages/identity-domain/claim_test.go's own pattern; what's
	// under test is whether the CLAIMED identity's OWN post-claim holdsRole
	// grant projects with a second live consumer/lens in play, not whether
	// the caller's pre-existing permissions do. ---
	staffActorKey := "vtx.identity." + stableClaimBatchID("real-op-eph-staff-actor")
	consumerActorKey := "vtx.identity." + stableClaimBatchID("real-op-eph-consumer-actor")
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
	// The credential actor ClaimIdentity submits as. The op refuses an actor
	// with no live identity vertex: the boundTo edge it emits names that
	// vertex as its source, and the bindings projection anchors on it.
	testutil.SeedCredentialActor(t, ctx, conn, consumerActorKey, "")

	// --- arrange: a real CreateUnclaimedIdentity op ---
	createReqID := testutil.GenReqID("RealClmEphCreat0")
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
		Payload:       json.RawMessage(`{"name":"Real Claim Eph Test","email":"realclaimeph@claim.example","claimKeyHash":"` + claimKeyHash + `"}`),
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
	claimReqID := testutil.GenReqID("RealClmEphClaim0")
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
	// cap.roles.<claimedIdentity> WITH capabilityEphemeral running as a
	// second live consumer on the same identity/role graph? ---
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
		t.Logf("MINIMAL REPRO CAPTURED (real op + ephemeral consumer): cap.roles.%s never gained "+
			"the consumer-role grant from the REAL ClaimIdentity op's own holdsRole write within 25s "+
			"(last read: %+v) while capabilityEphemeral ran as a second live consumer on the same "+
			"identity/role graph. Candidate (3) confirmed: a competing consumer/lens IS the gap.", identityID, env)
	} else {
		t.Logf("NOT REPRODUCED IN THIS HARNESS: cap.roles.%s projected the consumer-role grant "+
			"from the real ClaimIdentity op even with capabilityEphemeral running as a second live "+
			"consumer. Candidate (3) is cleared — the remaining live-stack-only candidate (1) needs "+
			"direct instrumentation of the demo box's Refractor instance during a real claim.", identityID)
	}
	require.True(t, projected,
		"cap.roles.<target> must project the role-derived grant from the REAL ClaimIdentity op's "+
			"own holdsRole write even with a second live consumer/lens in play — see the preceding "+
			"t.Logf for repro details")
}
