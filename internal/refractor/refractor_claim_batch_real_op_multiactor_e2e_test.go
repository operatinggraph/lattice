// Reproduces facet-staff-worlds-design.md §13.6's live finding: the real
// two-actor claim ceremony (a fresh device D auto-provisions itself via
// ProvisionConsumerIdentity, then immediately claims a staff-minted
// identity U) drops U's own cap.roles projection 2/3 runs on the demo box,
// while D's — written moments earlier by the same request — always lands.
// Every prior local harness in this design's §13 series wrote only ONE
// actor's holdsRole grant per test; this is the first to write TWO
// DIFFERENT actors' holdsRole grants back to back (no wall-clock gap, no
// artificial delay) against a SINGLE live, CDC-driven capabilityRoles
// pipeline, mirroring cmd/facet/claim.go's own two-sequential-op handler.
package refractor_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func multiActorSHA256HexOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func multiActorIdentityIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

// TestRefractor_CapabilityLens_TwoActorClaimCeremony_MultiActorRace_E2E is
// facet-staff-worlds-design.md §13.6's checkpoint: drive the SAME two-op
// sequence the live Gateway ceremony drives — device D's own
// ProvisionConsumerIdentity, then D's ClaimIdentity targeting a DIFFERENT
// identity U — back to back, against a single live capabilityRoles
// pipeline, and check whether BOTH cap.roles.D and cap.roles.U converge.
func TestRefractor_CapabilityLens_TwoActorClaimCeremony_MultiActorRace_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping two-actor claim ceremony e2e test in -short mode")
	}

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

	testutil.InstallPhase1Packages(t, ctx, conn)

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capabilityKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

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

	const rolesLensID = "MuLtiActorRaceLensid"
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

	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "multiactorraceops",
	})

	// --- staff actor: creates the unclaimed identity U. ---
	staffActorKey := "vtx.identity." + stableClaimBatchID("multiactor-staff-actor")
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
			{OperationType: "ProvisionConsumerIdentity", Scope: "any"},
		},
		Roles: []string{bootstrap.RoleOperatorKey},
	})
	testutil.SeedHoldsRole(t, ctx, conn, staffActorKey, bootstrap.RoleOperatorKey)

	// --- U: a real CreateUnclaimedIdentity op. ---
	createReqID := testutil.GenReqID("MultiActorU00")
	identityID := multiActorIdentityIDFromRequestID(createReqID)
	identityKey := "vtx.identity." + identityID
	claimKeyPlaintext := "claim-secret-for-" + createReqID
	claimKeyHash := multiActorSHA256HexOf(claimKeyPlaintext)

	createEnv := &processor.OperationEnvelope{
		RequestID:     createReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateUnclaimedIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-30T10:00:00Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"name":"MultiActor Race U","email":"multiactor-race-u@claim.example","claimKeyHash":"` + claimKeyHash + `"}`),
	}
	testutil.PublishOp(t, conn, createEnv)
	outcome := testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	require.Equal(t, processor.OutcomeAccepted, outcome, "CreateUnclaimedIdentity must be accepted")

	// --- D: a brand-new device identity, self-provisioned by an operator on
	// D's behalf — mirrors the Gateway's own pre-flight
	// (provisionActorIfNeeded), which uses the platform-seeded operator
	// grant, not a caller-supplied one. ---
	deviceID, err := substrate.NewNanoID()
	require.NoError(t, err)
	deviceKey := "vtx.identity." + deviceID
	consumerRoleKey := "vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")

	provisionReqID := testutil.GenReqID("MultiActorD00")
	provisionEnv := &processor.OperationEnvelope{
		RequestID:     provisionReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ProvisionConsumerIdentity",
		Actor:         staffActorKey,
		SubmittedAt:   "2026-07-30T10:00:01Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"targetActorKey":"` + deviceKey + `","consumerRoleKey":"` + consumerRoleKey + `"}`),
	}

	// D's own scope=self ClaimIdentity permission is seeded directly (like
	// every prior harness in this series seeds its callers' permissions) —
	// testutil.CapabilityPipeline's authorizer isn't wired RbacRolesActive,
	// so step 3 reads cap.identity.<actor> (capabilitykv.CapabilityKeyFromActor),
	// never the rbac-derived cap.roles.<actor> a live-projecting lens writes.
	// This sidesteps D's OWN async-projection auth race (cmd/facet/claim.go's
	// isTransientAuthLag — already understood, a different mechanism) so the
	// two ops below can fire with NO artificial gap: what's under test is
	// whether U's grant, projected by the very next CDC event on this same
	// live pipeline, survives right after D's own holdsRole write. ---
	now2 := time.Now().UTC()
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + deviceID,
		Actor:                  deviceKey,
		Version:                "1.0",
		ProjectedAt:            now2.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{deviceKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ClaimIdentity", Scope: "self"},
		},
		Roles: []string{consumerRoleKey},
	})

	// --- The two-op sequence: submit D's pre-flight, drive it to accepted
	// (as the Gateway's own handler awaits it synchronously), THEN
	// IMMEDIATELY — no sleep, no artificial gap — submit U's claim as D.
	// capP's live CDC pump reacts to both holdsRole writes (D's from this
	// op, U's from the claim below) on its own schedule, exactly as it does
	// in production; this is the one variable no prior harness in this
	// series varied. ---
	testutil.PublishOp(t, conn, provisionEnv)
	outcome = testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	require.Equal(t, processor.OutcomeAccepted, outcome, "ProvisionConsumerIdentity must be accepted")

	claimReqID := testutil.GenReqID("MultiActorClm0")
	claimEnv := &processor.OperationEnvelope{
		RequestID:     claimReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClaimIdentity",
		Actor:         deviceKey,
		SubmittedAt:   "2026-07-30T10:00:02Z",
		Class:         "identity",
		Payload:       json.RawMessage(`{"claimKey":"` + claimKeyPlaintext + `","targetIdentityKey":"` + identityKey + `"}`),
		AuthContext:   &processor.AuthContext{Target: deviceKey},
		ContextHint: &processor.ContextHint{
			Reads: []string{
				identityKey,
				identityKey + ".state",
				identityKey + ".claimKey",
			},
		},
	}
	var claimReply *processor.OperationReply
	outcome, claimReply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, claimEnv)
	if outcome != processor.OutcomeAccepted {
		t.Logf("claim rejected: %+v (error: %+v)", claimReply, claimReply.Error)
	}
	require.Equal(t, processor.OutcomeAccepted, outcome, "ClaimIdentity must be accepted")

	// --- assert: BOTH D's own consumer grant AND U's post-claim consumer
	// grant must project — the live ceremony drops U's while D's lands. ---
	hasConsumerRole := func(env map[string]any) bool {
		pp, _ := env["platformPermissions"].([]any)
		for _, e := range pp {
			m, ok := e.(map[string]any)
			if ok && m["operationType"] == "InitiateCredentialLink" && m["scope"] == "self" {
				return true
			}
		}
		return false
	}
	getEnv := func(capKey string) (map[string]any, bool) {
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
	waitForConsumerRole := func(capKey string) bool {
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			if env, ok := getEnv(capKey); ok && hasConsumerRole(env) {
				return true
			}
			time.Sleep(200 * time.Millisecond)
		}
		return false
	}

	dCapKey := "cap.roles.identity." + deviceID
	uCapKey := "cap.roles.identity." + identityID

	dProjected := waitForConsumerRole(dCapKey)
	uProjected := waitForConsumerRole(uCapKey)

	if !dProjected {
		env, _ := getEnv(dCapKey)
		t.Logf("D's own consumer grant never projected into %s (last read: %+v)", dCapKey, env)
	}
	if !uProjected {
		env, _ := getEnv(uCapKey)
		t.Logf("U's post-claim consumer grant never projected into %s (last read: %+v) — "+
			"this is the live-observed defect (facet-staff-worlds-design.md §13.6): U's grant "+
			"drops while D's (written moments earlier by the same two-op sequence) lands.", uCapKey, env)
	}

	require.True(t, dProjected, "D's own consumer grant (%s) must project", dCapKey)
	require.True(t, uProjected, "U's post-claim consumer grant (%s) must project", uCapKey)
}
