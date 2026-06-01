package full

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
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
//	  task1 ─[assignedTo]─> alice (future expiry)
//	  taskexpired ─[assignedTo]─> alice (past expiry; must be filtered)
//	  task2 ─[assignedTo]─> bob; bob ─[reportsTo]─> alice (future expiry)
// It uses the LITERAL CapabilityLensDefinition.RuleBody (parent brief
// Decision #8) and asserts Contract #6 §6.10 / §6.2 three-section output.
func TestBootstrap_CapabilityLensE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	// Identities
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})
	putVertex(t, reg, coreKV, "bob", "identity", map[string]any{"name": "bob"})

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

	// Tasks (ephemeral grants)
	future := time.Now().Add(24 * time.Hour).Unix()
	past := time.Now().Add(-24 * time.Hour).Unix()
	putVertex(t, reg, coreKV, "task1", "task", map[string]any{
		"data": map[string]any{
			"expiresAt":            float64(future),
			"grantedOperationType": "delete",
			"targetKey":            "doc1",
		},
	})
	putVertex(t, reg, coreKV, "taskexpired", "task", map[string]any{
		"data": map[string]any{
			"expiresAt":            float64(past),
			"grantedOperationType": "admin",
			"targetKey":            "doc2",
		},
	})
	putEdge(t, reg, adjKV, "assignedTo", "task1", "alice")
	putEdge(t, reg, adjKV, "assignedTo", "taskexpired", "alice")

	// Reports-to chain
	putVertex(t, reg, coreKV, "task2", "task", map[string]any{
		"data": map[string]any{
			"expiresAt":            float64(future),
			"grantedOperationType": "approve",
			"targetKey":            "doc3",
		},
	})
	putEdge(t, reg, adjKV, "assignedTo", "task2", "bob")
	// alice reports to bob: identity -[:reportsTo]-> report; bob is the manager.
	putEdge(t, reg, adjKV, "reportsTo", "alice", "bob")

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

	// ephemeralGrants: list of {source, taskKey, ...}. Must include task1 (direct)
	// and task2 (via reportsTo). Must NOT include taskexpired.
	eg, ok := row["ephemeralGrants"].([]any)
	require.True(t, ok, "ephemeralGrants must be a list")
	foundTask1, foundTask2, foundExpired := false, false, false
	for _, e := range eg {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if m["taskKey"] == vtxKey(reg, "task1") {
			foundTask1 = true
		}
		if m["taskKey"] == vtxKey(reg, "task2") {
			foundTask2 = true
		}
		if m["taskKey"] == vtxKey(reg, "taskexpired") {
			foundExpired = true
		}
	}
	require.True(t, foundTask1, "ephemeralGrants must include task1")
	require.True(t, foundTask2, "ephemeralGrants must include task2 via reportsTo")
	require.False(t, foundExpired, "ephemeralGrants must NOT include expired task")

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
