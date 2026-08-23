// Fan-in stress vector — evaluation-consistency-design.md §13.8/§13.9 step 1.
//
// This is the tier the da1b4641 revert needed and did not have: every unit
// gate passed the reverted Increment 2, and only a stack-gate-shaped
// assertion (make verify-package-service-location) caught the regression.
// This test reproduces the SAME mechanism at the Go test tier so the
// regression has a fast, always-run guard instead of only a live-stack one.
//
// It seeds ONE identity holding ONE role, with a LIVE queued task so
// orchestration-base's capabilityEphemeral role-queue branch
// ((identity)-[:holdsRole]->(role)<-[:queuedFor]-(task3)) is non-trivially
// projecting — the census row the whole mechanism exists for (§13 ledger,
// row 1) — grants N=10 permissions to that SAME role (mirroring
// packages/service-location's install-time seeding pattern that originally
// broke make verify-package-service-location: 10 grantedBy links to one
// role in a tight loop) and asserts both cap.roles.<actor> and
// cap.ephemeral.<actor> converge to the fully-granted state, then proves the
// MECHANISM itself deterministically: an eleventh grantedBy write, injected
// via full.WithFootprintCapturedHook to land exactly inside an evaluation's
// footprint-capture-to-validate gap, must register zero eval-drift retries
// on EITHER lens once both fixes have landed.
//
// Driving mechanism: the live consumer pump (pipeline.RunOn/Run) turned out
// to be the wrong tool here. Its internal message-pump context is derived
// from context.Background() (substrate's ConsumerSupervisor.Add), not from
// the context passed to Run — a deliberate isolation so pump lifetime
// isn't tied to a caller's cancellation, but it also means
// full.WithFootprintCapturedHook's value never reaches an evaluation
// triggered by the live pump, so a hook attached to Run's context is
// silently inert there. It also does not reliably reproduce the regression
// even without a hook: embedded, in-process NATS redelivers a Nak'd
// message near-instantly (substrate.Nak carries no backoff — only
// NakWithDelay does), and neither package lens declares a Retry block
// (mirroring production, so no pipeline retry-queue backs the drift path
// either), so a raw rapid write loop converges in well under a second even
// against the unmodified, unclassified, whole-document-footprint seam.
//
// pipeline.Reproject is the fix: it is a real, exported, synchronous
// per-actor entry point (the same one the sweep's deep-verify, the
// operator control-plane RPC, and the drift retry-queue itself all use) that
// runs the identical executeFullForActor/footprintValid path, driven with
// whatever context the caller passes — so a hook attached to THIS call's
// context reaches the evaluation deterministically, no wall-clock guessing,
// no live pump needed (CLAUDE.md's "channels, polling with condition" sync
// discipline, not a fixed sleep).
//
// Both rbac-domain's capabilityRoles and orchestration-base's
// capabilityEphemeral are wired through the SAME projection.Compile path
// production activation uses (projection.InstallActorAggregate derives from
// it), so this exercises the real §13.3 classifier verdict and the real
// §13.4 selector-scoped footprint end to end, not a stand-in.
//
// Acceptance (§13.9): red against the freshly-cherry-picked da1b4641 seam
// alone (no classifier narrows the predicate, so capabilityRoles pays
// pointless validation on its own legitimate churn — defect 1); still red
// with §13.3 alone (capabilityEphemeral remains whole-document-footprinted
// on the shared role node — defect 2); green only once §13.3 and §13.4 have
// both landed.
package refractor_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

// faninstressGrantCount mirrors packages/service-location's Permissions()
// count (ten grantedBy links to one role installed in a tight loop) — the
// exact shape that broke make verify-package-service-location under the
// reverted Increment 2 predicate.
const faninstressGrantCount = 10

// ruleFromPkgSpec builds a minimal *lens.Rule for a real package LensSpec so
// the test can drive it through the SAME projection.Compile path production
// activation (projection.InstallActorAggregate) uses — the real §13.3
// classifier verdict and §13.4 selector-scoped footprint, not a stand-in.
func ruleFromPkgSpec(t *testing.T, l pkgmgr.LensSpec, cr ruleengine.CompiledRule, id string) *lens.Rule {
	t.Helper()
	require.NotNil(t, l.Output, "package lens %q must declare an Output descriptor", l.CanonicalName)
	return &lens.Rule{
		ID:             id,
		CanonicalName:  l.CanonicalName,
		ResolvedEngine: "full",
		CompiledRule:   cr,
		ProjectionKind: l.ProjectionKind,
		Into: lens.IntoConfig{
			Target: "nats_kv",
			Bucket: l.Bucket,
			Key:    lens.KeyField{"key"},
		},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:         l.Output.AnchorType,
			OutputKeyPattern:   l.Output.OutputKeyPattern,
			BodyColumns:        l.Output.BodyColumns,
			EmptyBehavior:      l.Output.EmptyBehavior,
			RealnessFilter:     l.Output.RealnessFilter,
			Freshness:          l.Output.Freshness,
			ActorField:         l.Output.ActorField,
			Lanes:              l.Output.Lanes,
			StaticEmptyColumns: l.Output.StaticEmptyColumns,
		},
	}
}

func TestRefractor_FanInStress_CapabilityRolesAndEphemeral_ConvergeUnderRoleGrantChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping fan-in stress e2e test in -short mode")
	}

	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capabilityKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)
	healthKV, err := conn.OpenKV(ctx, bootstrap.HealthKVBucket)
	require.NoError(t, err)

	// --- real package specs, compiled + wired the same way production does ---
	var rolesLensSpec pkgmgr.LensSpec
	for _, l := range rbacdomain.Lenses() {
		if l.CanonicalName == "capabilityRoles" {
			rolesLensSpec = l
		}
	}
	require.NotEmpty(t, rolesLensSpec.Spec, "rbac-domain must declare a capabilityRoles lens")

	var ephLensSpec pkgmgr.LensSpec
	for _, l := range orchestrationbase.Lenses() {
		if l.CanonicalName == "capabilityEphemeral" {
			ephLensSpec = l
		}
	}
	require.NotEmpty(t, ephLensSpec.Spec, "orchestration-base must declare a capabilityEphemeral lens")

	fullEngine := full.New()
	rolesCR, err := fullEngine.Parse(rolesLensSpec.Spec)
	require.NoError(t, err, "capabilityRoles spec must parse")
	ephCR, err := fullEngine.Parse(ephLensSpec.Spec)
	require.NoError(t, err, "capabilityEphemeral spec must parse")

	const rolesLensID = "FaninRoLesLensid9991" // 20-char synthetic id
	const ephLensID = "FaninEphLensid999991"   // 20-char synthetic id
	rolesRule := ruleFromPkgSpec(t, rolesLensSpec, rolesCR, rolesLensID)
	ephRule := ruleFromPkgSpec(t, ephLensSpec, ephCR, ephLensID)

	rolesPlan, err := projection.Compile(rolesRule)
	require.NoError(t, err, "capabilityRoles must compile through the actor-aggregate plan")
	ephPlan, err := projection.Compile(ephRule)
	require.NoError(t, err, "capabilityEphemeral must compile through the actor-aggregate plan")
	// §13.3's census reproduction, pinned here too: capabilityRoles is
	// single-binding (exempt), capabilityEphemeral is multi-binding
	// (validated) — see footprint_classifier_test.go for the full table.
	require.False(t, rolesPlan.RequiresFootprintValidation, "capabilityRoles must classify as exempt (single-binding entries)")
	require.True(t, ephPlan.RequiresFootprintValidation, "capabilityEphemeral must classify as validated (multi-binding entries)")

	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	rolesTargetKV, err := conn.OpenKV(ctx, rolesRule.Into.Bucket)
	require.NoError(t, err)
	rolesAdpt, err := adapter.New(rolesTargetKV, []string(rolesRule.Into.Key), adapter.DeleteModeHard)
	require.NoError(t, err)
	rolesReporter := health.New(healthKV, rolesLensID)
	rolesP, err := pipeline.New(rolesLensID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, rolesAdpt, rolesReporter)
	require.NoError(t, err)
	rolesP.UseFullEngine(fullEngine, rolesCR)
	rolesP.SetEnvelopeFn(rolesPlan.Output.EnvelopeFn("vtx.meta."+rolesLensID, projectionRevision))
	rolesP.SetActorDeleteKey(rolesPlan.Output.BuildKey)
	rolesP.SetAuthPlane(rolesPlan.AuthPlane)
	rolesP.SetRequiresFootprintValidation(rolesPlan.RequiresFootprintValidation)

	ephTargetKV, err := conn.OpenKV(ctx, ephRule.Into.Bucket)
	require.NoError(t, err)
	ephAdpt, err := adapter.New(ephTargetKV, []string(ephRule.Into.Key), adapter.DeleteModeHard)
	require.NoError(t, err)
	ephReporter := health.New(healthKV, ephLensID)
	ephP, err := pipeline.New(ephLensID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, ephAdpt, ephReporter)
	require.NoError(t, err)
	ephP.UseFullEngine(fullEngine, ephCR)
	ephP.SetEnvelopeFn(ephPlan.Output.EnvelopeFn("vtx.meta."+ephLensID, projectionRevision))
	ephP.SetActorDeleteKey(ephPlan.Output.BuildKey)
	ephP.SetAuthPlane(ephPlan.AuthPlane)
	ephP.SetRequiresFootprintValidation(ephPlan.RequiresFootprintValidation)

	// --- fixture: identity + role + a LIVE queued task (role-queue branch) ---
	identityID := stableMultiID("faninstress-actor")
	roleID := stableMultiID("faninstress-role")
	task3ID := stableMultiID("faninstress-task3")
	op3ID := stableMultiID("faninstress-op3")
	target3ID := stableMultiID("faninstress-target3")

	identityKey := substrate.VertexKey("identity", identityID)
	roleKey := substrate.VertexKey("role", roleID)
	task3Key := substrate.VertexKey("task", task3ID)
	op3Key := substrate.VertexKey("meta", op3ID)
	target3Key := substrate.VertexKey("leaseapp", target3ID)

	const provenanceAt = "2026-05-15T10:00:00Z"
	writeVertex := func(key, class string, extra map[string]any) {
		body := map[string]any{
			"key": key, "class": class, "isDeleted": false,
			"createdAt": provenanceAt, "lastModifiedAt": provenanceAt, "data": extra,
		}
		raw, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, key, raw)
		require.NoError(t, perr)
	}
	// buildEdge writes both adjacency directions directly (no live link-
	// bridge bootstrapper is running in this test — pipeline.Reproject reads
	// adjKV/coreKV fresh on every call and needs no CDC pump to drive it).
	buildEdge := func(name, fromType, fromID, toType, toID string) {
		edgeID := name + ":" + fromID + ":" + toID
		require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
			CoreKvKey: substrate.LinkKey(fromType, fromID, name, toType, toID),
			EdgeID:    edgeID, Name: name,
			Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType,
		}))
		require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
			CoreKvKey: substrate.LinkKey(fromType, fromID, name, toType, toID),
			EdgeID:    edgeID, Name: name,
			Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType,
		}))
	}

	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)

	writeVertex(roleKey, "role", nil)
	writeVertex(op3Key, "meta", map[string]any{"operationType": "FanInStressApproveInitial"})
	writeVertex(target3Key, "leaseapp", map[string]any{"state": "pending"})
	writeVertex(task3Key, "task", map[string]any{"status": "open", "expiresAt": future})
	buildEdge("queuedFor", "task", task3ID, "role", roleID)
	buildEdge("forOperation", "task", task3ID, "meta", op3ID)
	buildEdge("scopedTo", "task", task3ID, "leaseapp", target3ID)
	buildEdge("holdsRole", "identity", identityID, "role", roleID)
	writeVertex(identityKey, "identity", map[string]any{"name": "faninstress-actor"})

	rolesKey := "cap.roles.identity." + identityID
	ephKey := "cap.ephemeral.identity." + identityID

	readEnv := func(key string) (map[string]any, bool) {
		entry, gErr := capabilityKV.Get(ctx, key)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return nil, false
		}
		var env map[string]any
		if jerr := json.Unmarshal(entry.Value, &env); jerr != nil {
			return nil, false
		}
		return env, true
	}
	hasEphemeralForTask := func(env map[string]any, taskKey string) bool {
		eg, _ := env["ephemeralGrants"].([]any)
		for _, e := range eg {
			m, ok := e.(map[string]any)
			if ok && m["taskKey"] == taskKey {
				return true
			}
		}
		return false
	}

	// --- Sanity: the role-queue branch must be non-trivially projecting
	// BEFORE the grant loop starts — otherwise the stress vector proves
	// nothing about capabilityEphemeral (§13.1's row-1 mechanism). No churn
	// is in flight yet, so a single clean Reproject call must converge. ---
	_, rerr := ephP.Reproject(ctx, identityKey)
	require.NoError(t, rerr, "capabilityEphemeral's initial role-queue projection must succeed with no churn in flight")
	ephEnv, ok := readEnv(ephKey)
	require.True(t, ok, "cap.ephemeral.<actor> must exist after the initial Reproject")
	require.True(t, hasEphemeralForTask(ephEnv, task3Key), "capabilityEphemeral's role-queue branch must converge BEFORE the grant loop starts")

	// --- N grantedBy permissions to the SAME role — mirrors
	// packages/service-location's install-time seeding pattern that
	// originally broke make verify-package-service-location (10 grantedBy
	// links to one role in a tight loop). No churn is interleaved with this
	// write burst itself; interleaving is exercised deterministically below
	// via the footprint-captured hook, which is the actual race the design
	// names — a raw write loop with nothing else running does not race
	// anything. ---
	wantOps := make([]string, faninstressGrantCount)
	for i := 0; i < faninstressGrantCount; i++ {
		permID := stableMultiID("faninstress-perm-" + string(rune('0'+i)))
		opType := "FanInStressGrant" + string(rune('0'+i))
		wantOps[i] = opType
		writeVertex(substrate.VertexKey("permission", permID), "permission", map[string]any{"operationType": opType, "scope": "any"})
		buildEdge("grantedBy", "permission", permID, "role", roleID)
	}

	_, rerr = rolesP.Reproject(ctx, identityKey)
	require.NoError(t, rerr, "capabilityRoles must converge cleanly with no churn in flight")
	_, rerr = ephP.Reproject(ctx, identityKey)
	require.NoError(t, rerr, "capabilityEphemeral must converge cleanly with no churn in flight")

	rolesEnv, ok := readEnv(rolesKey)
	require.True(t, ok, "cap.roles.<actor> must exist after the grant loop")
	seenOps := map[string]bool{}
	if pp, _ := rolesEnv["platformPermissions"].([]any); pp != nil {
		for _, e := range pp {
			if m, ok := e.(map[string]any); ok {
				if op, _ := m["operationType"].(string); op != "" {
					seenOps[op] = true
				}
			}
		}
	}
	for _, op := range wantOps {
		require.Truef(t, seenOps[op], "cap.roles.<actor> must carry every one of the %d grantedBy permissions; missing %q; got %v",
			faninstressGrantCount, op, seenOps)
	}
	ephEnv, ok = readEnv(ephKey)
	require.True(t, ok, "cap.ephemeral.<actor> must still exist after the unrelated grant loop")
	require.True(t, hasEphemeralForTask(ephEnv, task3Key), "cap.ephemeral.<actor> must retain the role-queue grant after the unrelated grant loop")

	// --- The mechanism proof: an eleventh grantedBy write, injected via the
	// footprint-captured hook to land inside ONE Reproject call's footprint-
	// capture-to-validate gap. §13.3/§13.4's acceptance criterion: zero
	// eval-drift retries on EITHER lens once both fixes have landed —
	// capabilityRoles because it never validates at all (exempt by
	// construction), capabilityEphemeral because its footprint no longer
	// includes the role's grantedBy edges (selector-scoped to queuedFor). ---
	injectOneMoreGrant := func(suffix string) func() {
		fired := false
		return func() {
			if fired {
				return
			}
			fired = true
			permID := stableMultiID("faninstress-interleaved-perm-" + suffix)
			writeVertex(substrate.VertexKey("permission", permID), "permission",
				map[string]any{"operationType": "FanInStressInterleaved" + suffix, "scope": "any"})
			buildEdge("grantedBy", "permission", permID, "role", roleID)
		}
	}

	rolesHookCtx := full.WithFootprintCapturedHook(ctx, injectOneMoreGrant("Roles"))
	_, _ = rolesP.Reproject(rolesHookCtx, identityKey) // return value ignored — the retry-count checks below are the assertion

	ephHookCtx := full.WithFootprintCapturedHook(ctx, injectOneMoreGrant("Eph"))
	_, _ = ephP.Reproject(ephHookCtx, identityKey)

	rolesEntry, herr := rolesReporter.GetStatus(ctx)
	require.NoError(t, herr)
	ephEntry, herr := ephReporter.GetStatus(ctx)
	require.NoError(t, herr)

	require.Zerof(t, rolesEntry.EvalDriftRetries,
		"capabilityRoles must be exempt from footprint validation (§13.3 — single-binding entries) and so must never even attempt a drift-retry on its own legitimate grantedBy churn; got %d retries, %d requeues",
		rolesEntry.EvalDriftRetries, rolesEntry.EvalDriftRequeues)
	require.Zerof(t, ephEntry.EvalDriftRetries,
		"capabilityEphemeral's footprint must be selector-scoped to queuedFor (§13.4) — an unrelated grantedBy write to the shared role node must not register as drift; got %d retries, %d requeues",
		ephEntry.EvalDriftRetries, ephEntry.EvalDriftRequeues)
}
