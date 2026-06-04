package full

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
)

// TestBootstrap_CapabilityLensE2E is the Story 3.1b-ii acceptance test.
//
// Representative graph seeded:
//
//	alice (identity)
//	  ─[holdsRole]─> admin (role) ─[grantsPermission]─> permRead (permission)
//	                              ─[grantsPermission]─> permWrite (permission)
//	  ─[containedIn]─> hq (location) ─[availableAt]─> svcOK
//	                                  ─[availableAt]─> svcBlocked
//	                                  ─[unavailableAt]─> svcBlocked  // exclusion
//	  svcOK ─[permitsOperation]─> opRead
//
// The bootstrap god-cypher does NOT produce ephemeralGrants. FR56 ephemeral
// grant behaviors (task / reportsTo / expiry-filtering) are exercised by
// TestCapabilityEphemeralLens_E2E below via the orchestration-base
// capabilityEphemeral lens. This test does not seed tasks or assert
// ephemeralGrants.
//
// It uses the LITERAL CapabilityLensDefinition.RuleBody (parent brief
// Decision #8) and asserts Contract #6 §6.10 / §6.2 output (platform +
// service + roles).
func TestBootstrap_CapabilityLensE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	// Identities
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})

	// Roles + permissions
	putVertex(t, reg, coreKV, "admin", "role", map[string]any{"canonicalName": "admin"})
	putVertex(t, reg, coreKV, "permread", "permission", map[string]any{
		"data": map[string]any{"operationType": "read", "scope": "any"},
	})
	putVertex(t, reg, coreKV, "permwrite", "permission", map[string]any{
		"data": map[string]any{"operationType": "write", "scope": "owned"},
	})
	putEdge(t, reg, adjKV, "holdsRole", "alice", "admin")
	// Story 4.7 rename: grantsPermission(role→permission) became
	// grantedBy(permission→role).
	putEdge(t, reg, adjKV, "grantedBy", "permread", "admin")
	putEdge(t, reg, adjKV, "grantedBy", "permwrite", "admin")

	// Locations + services
	putVertex(t, reg, coreKV, "hq", "location", nil)
	putVertex(t, reg, coreKV, "svcok", "service", map[string]any{"class": "service"})
	putVertex(t, reg, coreKV, "svcblocked", "service", map[string]any{"class": "service"})
	putEdge(t, reg, adjKV, "containedIn", "alice", "hq")
	putEdge(t, reg, adjKV, "availableAt", "hq", "svcok")
	putEdge(t, reg, adjKV, "availableAt", "hq", "svcblocked")
	putEdge(t, reg, adjKV, "unavailableAt", "hq", "svcblocked")
	putVertex(t, reg, coreKV, "opread", "operation", map[string]any{"data": map[string]any{"operationType": "read"}})
	putEdge(t, reg, adjKV, "permitsOperation", "svcok", "opread")

	body := bootstrap.CapabilityLensDefinition().CypherRule
	eng := New()
	cr, err := eng.Parse(body)
	require.NoError(t, err, "bootstrap cypher must parse")

	now := time.Now().Unix()
	aliceKey := vtxKey(reg, "alice")
	params := map[string]any{
		"actorKey":    aliceKey,
		"now":         float64(now),
		"projectedAt": time.Now().UTC().Format(time.RFC3339),
	}

	// Latency: warm up + measure across N runs.
	const runs = 5
	durations := make([]time.Duration, 0, runs)
	var results []ruleengine.ProjectionResult
	for i := 0; i < runs; i++ {
		start := time.Now()
		out, err := eng.ExecuteWith(context.Background(), cr,
			ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
		dur := time.Since(start)
		require.NoError(t, err, "bootstrap query must execute without error")
		durations = append(durations, dur)
		results = out
	}

	// Three-section output assertion (Contract #6 §6.10 / §6.2).
	require.Len(t, results, 1, "bootstrap query should produce exactly one row per actor")
	row := results[0].Values

	// actorKey
	require.Equal(t, aliceKey, row["actorKey"])

	// platformPermissions: collect of {operationType, scope} for permread+permwrite.
	pp, ok := row["platformPermissions"].([]any)
	require.True(t, ok, "platformPermissions must be a list, got %T", row["platformPermissions"])
	require.Len(t, pp, 2, "platformPermissions should have 2 entries")

	// serviceAccess: svcok only (svcblocked excluded by anti-pattern).
	sa, ok := row["serviceAccess"].([]any)
	require.True(t, ok, "serviceAccess must be a list")
	// At minimum, svcok must be present; svcblocked must NOT be present.
	require.NotEmpty(t, sa, "serviceAccess must include svcok")
	foundSvcOk, foundSvcBlocked := false, false
	for _, e := range sa {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if m["service"] == vtxKey(reg, "svcok") {
			foundSvcOk = true
		}
		if m["service"] == vtxKey(reg, "svcblocked") {
			foundSvcBlocked = true
		}
	}
	require.True(t, foundSvcOk, "serviceAccess must include svcok")
	require.False(t, foundSvcBlocked, "serviceAccess must NOT include svcblocked (anti-pattern)")

	// The bootstrap cypher does not RETURN ephemeralGrants.
	require.NotContains(t, row, "ephemeralGrants",
		"bootstrap cypher must NOT produce ephemeralGrants (owned by capabilityEphemeral lens)")

	// roles
	roles, ok := row["roles"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, roles, "expected at least one role")

	// Latency: log mean / p95 / p99 (records, doesn't halt — Decision #11).
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	mean := sum / time.Duration(len(durations))
	p95 := durations[int(float64(len(durations))*0.95)]
	p99 := durations[len(durations)-1]
	t.Logf("bootstrap CapabilityLens latency over %d runs: mean=%v p95=%v p99=%v",
		runs, mean, p95, p99)
}

// TestCapabilityEphemeralLens_E2E exercises the link-sourced ephemeral-grant
// behaviors of the orchestration-base capabilityEphemeral lens:
//
//	task1 ─[assignedTo]─> alice (future expiry)          → granted (direct)
//	taskexpired ─[assignedTo]─> alice (past expiry)       → filtered (expired)
//	task2 ─[assignedTo]─> bob; alice ─[reportsTo]─> bob   → granted (2-hop delegation)
//
// Each grant is LINK-sourced: operationType ← forOperation→op,
// target ← scopedTo→target, expiresAt ← task root scalar (Contract #10
// §10.1) — NOT the old task.data.grantedOperationType/targetKey fields.
func TestCapabilityEphemeralLens_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})
	putVertex(t, reg, coreKV, "bob", "identity", map[string]any{"name": "bob"})

	// op meta-vertices (forOperation endpoints) + scopedTo targets.
	putVertex(t, reg, coreKV, "opDelete", "meta", map[string]any{"data": map[string]any{"operationType": "delete"}})
	putVertex(t, reg, coreKV, "opAdmin", "meta", map[string]any{"data": map[string]any{"operationType": "admin"}})
	putVertex(t, reg, coreKV, "opApprove", "meta", map[string]any{"data": map[string]any{"operationType": "approve"}})
	putVertex(t, reg, coreKV, "doc1", "doc", nil)
	putVertex(t, reg, coreKV, "doc2", "doc", nil)
	putVertex(t, reg, coreKV, "doc3", "doc", nil)

	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)

	// task1 — direct assignment, future expiry.
	putVertex(t, reg, coreKV, "task1", "task", map[string]any{
		"data": map[string]any{"status": "open", "expiresAt": future},
	})
	putEdge(t, reg, adjKV, "assignedTo", "task1", "alice")
	putEdge(t, reg, adjKV, "forOperation", "task1", "opDelete")
	putEdge(t, reg, adjKV, "scopedTo", "task1", "doc1")

	// taskexpired — direct assignment, PAST expiry (must be filtered).
	putVertex(t, reg, coreKV, "taskexpired", "task", map[string]any{
		"data": map[string]any{"status": "open", "expiresAt": past},
	})
	putEdge(t, reg, adjKV, "assignedTo", "taskexpired", "alice")
	putEdge(t, reg, adjKV, "forOperation", "taskexpired", "opAdmin")
	putEdge(t, reg, adjKV, "scopedTo", "taskexpired", "doc2")

	// task2 — assigned to bob; alice reports to bob (2-hop delegation).
	putVertex(t, reg, coreKV, "task2", "task", map[string]any{
		"data": map[string]any{"status": "open", "expiresAt": future},
	})
	putEdge(t, reg, adjKV, "assignedTo", "task2", "bob")
	putEdge(t, reg, adjKV, "forOperation", "task2", "opApprove")
	putEdge(t, reg, adjKV, "scopedTo", "task2", "doc3")
	putEdge(t, reg, adjKV, "reportsTo", "alice", "bob")

	specs := orchestrationbase.Lenses()
	require.Len(t, specs, 1)
	eng := New()
	cr, err := eng.Parse(specs[0].Spec)
	require.NoError(t, err, "capabilityEphemeral cypher must parse")

	aliceKey := vtxKey(reg, "alice")
	params := map[string]any{
		"actorKey":    aliceKey,
		"now":         time.Now().UTC().Format(time.RFC3339),
		"projectedAt": time.Now().UTC().Format(time.RFC3339),
	}
	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
	require.NoError(t, err, "capabilityEphemeral query must execute")
	require.Len(t, out, 1, "should produce exactly one row per actor")
	row := out[0].Values
	require.Equal(t, aliceKey, row["actorKey"])

	eg, ok := row["ephemeralGrants"].([]any)
	require.True(t, ok, "ephemeralGrants must be a list")
	type grant struct{ op, target string }
	byTask := map[string]grant{}
	for _, e := range eg {
		m, ok := e.(map[string]any)
		if !ok || m["taskKey"] == nil {
			continue
		}
		tk, _ := m["taskKey"].(string)
		op, _ := m["operationType"].(string)
		tgt, _ := m["target"].(string)
		byTask[tk] = grant{op: op, target: tgt}
	}

	// task1 (direct) present + link-sourced fields.
	g1, ok := byTask[vtxKey(reg, "task1")]
	require.True(t, ok, "ephemeralGrants must include task1 (direct)")
	require.Equal(t, "delete", g1.op, "task1 operationType must be link-sourced from opDelete")
	require.Equal(t, vtxKey(reg, "doc1"), g1.target, "task1 target must be link-sourced from scopedTo")

	// task2 (via reportsTo) present + link-sourced fields.
	g2, ok := byTask[vtxKey(reg, "task2")]
	require.True(t, ok, "ephemeralGrants must include task2 via reportsTo")
	require.Equal(t, "approve", g2.op, "task2 operationType must be link-sourced from opApprove")
	require.Equal(t, vtxKey(reg, "doc3"), g2.target, "task2 target must be link-sourced from scopedTo")

	// taskexpired filtered out.
	_, expiredPresent := byTask[vtxKey(reg, "taskexpired")]
	require.False(t, expiredPresent, "ephemeralGrants must NOT include the expired task")
}
