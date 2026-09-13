// Crosses the retraction transport for capabilityEphemeral end to end: an
// actor holding a projected ephemeral grant, a REAL MarkExpired op recording
// the task's lapse through the real Processor, and the grant leaving
// cap.ephemeral.<actor> — with no clock anywhere in the loop.
//
// The transport being proved is the one the design cites rather than measures:
// MarkExpired writes an ASPECT on the TASK, and capabilityEphemeral is anchored
// on the IDENTITY. Nothing links the two but deriveAnchorsForAspect, which
// seeds the aspect's parent vertex at every pattern position binding that type
// and walks back to the actors whose rows read it. A census of the live stack
// can show grants are absent; only this can show the write is what removes
// them.
package refractor_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// TestRefractor_MarkExpired_RetractsEphemeralGrant_E2E stands up the real
// Processor + a real orchestration-base install + the live capabilityEphemeral
// pipeline on the CDC pump, projects one grant, then submits the real
// MarkExpired op that records the task's lapse and asserts the grant goes.
func TestRefractor_MarkExpired_RetractsEphemeralGrant_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping MarkExpired-retracts-ephemeral-grant e2e test in -short mode")
	}

	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	// orchestration-base has to be installed FOR REAL, not just have its Lens
	// pulled: MarkExpired's handler is the freshnessMarker DDL and the marker's
	// shape is the freshnessExpiry aspect-type DDL, both of which live in Core
	// KV only after an install. Its identity-domain dependency comes from the
	// phase-1 set.
	testutil.InstallPhase1Packages(t, ctx, conn)
	stopMetaInstall := testutil.RunMetaInstallPipeline(t, ctx, conn)
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = testutil.StandardRoleIDs()
	_, err = inst.Install(ctx, orchestrationbase.Package)
	require.NoError(t, err, "orchestration-base must install")
	stopMetaInstall()

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
	case <-time.After(15 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 15s")
	}

	// --- the live capabilityEphemeral pipeline on the CDC pump ---
	fullEngine := full.New()
	ephSpec := ephemeralLensSpecForTest(t)
	ephCR, err := fullEngine.Parse(ephSpec.Spec)
	require.NoError(t, err, "capabilityEphemeral spec must parse")
	ephDesc := descFromPkgSpec(t, ephSpec)
	ephAdpt, err := adapter.New(capabilityKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)

	const ephLensID = "MarkExpRetractEphLns"
	ephP, err := pipeline.New(ephLensID, "nats_kv",
		bootstrap.CoreKVBucket, adjKV, coreKV, ephAdpt, nil)
	require.NoError(t, err)
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}
	ephP.UseFullEngine(fullEngine, ephCR)
	ephP.SetEnvelopeFn(ephDesc.EnvelopeFn("vtx.meta."+ephLensID, projectionRevision))
	ephP.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, ephDesc.AnchorType))
	ephP.SetActorDeleteKey(ephDesc.BuildKey)
	ephP.RunOn(conn, e2eSpec(ephLensID, bootstrap.CoreKVBucket))

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	ephDone := make(chan struct{})
	go func() { defer close(ephDone); ephP.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-ephDone })

	// --- the real op-driving CommitPath (default lane) ---
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable: "markexpiredretractseph",
	})

	// --- graph: one grantee identity, one open task assigned to it. The task's
	// deadline is already in the past, so the grant is exactly the population
	// the clock-reading form of this lens would have dropped and this one keeps
	// until the lapse is recorded. ---
	const provenanceAt = "2026-05-15T10:00:00Z"
	granteeID := stableNanoID("markexpired-eph-grantee")
	taskID := stableNanoID("markexpired-eph-task")
	opID := stableNanoID("markexpired-eph-op")
	targetID := stableNanoID("markexpired-eph-target")

	granteeKey := substrate.VertexKey("identity", granteeID)
	taskKey := substrate.VertexKey("task", taskID)
	opKey := substrate.VertexKey("meta", opID)
	targetKey := substrate.VertexKey("leaseapp", targetID)
	taskExpiresAt := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)

	writeVertex := func(key, class string, data map[string]any) {
		body := map[string]any{
			"key": key, "class": class, "isDeleted": false,
			"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
			"data": data,
		}
		raw, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, key, raw)
		require.NoError(t, perr)
	}
	writeLink := func(srcType, srcID, rel, dstType, dstID string) {
		linkKey := substrate.LinkKey(srcType, srcID, rel, dstType, dstID)
		body := map[string]any{
			"key": linkKey, "class": "link", "isDeleted": false,
			"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
			"sourceVertex": substrate.VertexKey(srcType, srcID),
			"targetVertex": substrate.VertexKey(dstType, dstID),
			"localName":    rel,
		}
		raw, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, linkKey, raw)
		require.NoError(t, perr)
	}

	writeVertex(granteeKey, "identity", map[string]any{"name": "grantee"})
	writeVertex(opKey, "meta", map[string]any{"operationType": "ApproveLeaseApplication"})
	writeVertex(targetKey, "leaseapp", map[string]any{"state": "pending"})
	writeVertex(taskKey, "task", map[string]any{"status": "open", "expiresAt": taskExpiresAt})
	writeLink("task", taskID, "assignedTo", "identity", granteeID)
	writeLink("task", taskID, "forOperation", "meta", opID)
	writeLink("task", taskID, "scopedTo", "leaseapp", targetID)

	// --- arrange: the grant projects. ---
	ephKey := ephDesc.BuildKey(granteeKey)
	grantsTask := func() bool {
		entry, gErr := capabilityKV.Get(ctx, ephKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false
		}
		var env map[string]any
		if json.Unmarshal(entry.Value, &env) != nil {
			return false
		}
		if isDel, _ := env["isDeleted"].(bool); isDel {
			return false
		}
		grants, _ := env["ephemeralGrants"].([]any)
		for _, g := range grants {
			m, _ := g.(map[string]any)
			if m["taskKey"] == taskKey {
				return true
			}
		}
		return false
	}
	require.Eventually(t, grantsTask, 40*time.Second, 100*time.Millisecond,
		"the open task's grant must project into cap.ephemeral.<grantee> before the retraction can mean anything — "+
			"note its deadline is already PAST and no lapse is recorded, which is the listed-but-denied population")

	// --- act: the REAL MarkExpired op, in the envelope Weaver's temporal lane
	// dispatches — the payload triple, no authContext (MarkExpired is the
	// target-less directOp posture), the entity root in contextHint.reads and
	// the marker in optionalReads (the merge is a read-modify-write whose
	// declared read is also what serializes it). Class is left for the
	// Processor's reverse index to resolve, exactly as the lane leaves it.
	//
	// The submitter holds `operator`, which is who MarkExpired is granted to
	// (permissions.go — Weaver's service actor is operator-equivalent). The
	// lane is `default` rather than `system` only because that is the subject
	// this harness's consumer is bound to; the lane is not what is under test.
	operatorActorKey := "vtx.identity." + stableNanoID("markexpired-eph-operator")
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key:                    "cap.identity." + operatorActorKey[len("vtx.identity."):],
		Actor:                  operatorActorKey,
		Version:                "1.0",
		ProjectedAt:            time.Now().UTC().Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{operatorActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "MarkExpired", Scope: "any"},
		},
		Roles: []string{bootstrap.RoleOperatorKey},
	})
	testutil.SeedHoldsRole(t, ctx, conn, operatorActorKey, bootstrap.RoleOperatorKey)

	markEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("MarkExpRetrEph01"),
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Actor:         operatorActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload: json.RawMessage(`{"entityKey":"` + taskKey +
			`","targetId":"` + orchestrationbase.StaleAssignedTasksTarget +
			`","expiredAt":"` + taskExpiresAt + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{taskKey},
			OptionalReads: []string{taskKey + ".freshnessExpiry"},
		},
	}
	testutil.PublishOp(t, conn, markEnv)
	require.Equal(t, processor.OutcomeAccepted,
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted),
		"MarkExpired must be accepted under the operator grant")

	// The marker really landed, at or after the task's own deadline. Asserted
	// separately so a retraction that happened for some other reason cannot
	// pass for this one.
	markerEntry, err := coreKV.Get(ctx, taskKey+".freshnessExpiry")
	require.NoError(t, err, "MarkExpired must have committed the freshnessExpiry marker on the task")
	var marker map[string]any
	require.NoError(t, json.Unmarshal(markerEntry.Value, &marker))
	markerData, _ := marker["data"].(map[string]any)
	require.GreaterOrEqual(t, markerData["expiredAt"], taskExpiresAt,
		"the recorded expiredAt must be at or after the task's deadline — that comparison IS the lens's predicate")

	// --- assert: the grant is gone. The aspect write on the TASK is the only
	// event; the lens is anchored on the IDENTITY, so every hop from that write
	// to this key is the transport under test. ---
	require.Eventually(t, func() bool { return !grantsTask() }, 40*time.Second, 100*time.Millisecond,
		"the grant must leave cap.ephemeral.<grantee> once MarkExpired records the task's lapse — "+
			"the marker write bumps the task's adjacency revision, deriveAnchorsForAspect walks from the "+
			"task to the actors whose rows bind it, and the reprojected row reads the recorded fact")
}
