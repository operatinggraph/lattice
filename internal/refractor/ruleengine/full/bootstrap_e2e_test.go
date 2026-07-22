package full

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

// ephemeralLensSpec returns the orchestration-base capabilityEphemeral lens
// cypher, selected by CanonicalName so the package may declare additional
// lenses without these conformance tests silently exercising the wrong one.
func ephemeralLensSpec(t *testing.T) string {
	t.Helper()
	for _, l := range orchestrationbase.Lenses() {
		if l.CanonicalName == "capabilityEphemeral" {
			return l.Spec
		}
	}
	t.Fatal("orchestration-base must declare a capabilityEphemeral lens")
	return ""
}

// TestRbacCapabilityRolesLens_E2E exercises rbac-domain's capabilityRoles lens
// (the role-derived grant projection the god-cypher's role branch used to
// produce, now decomposed into the package). It uses the LITERAL
// capabilityRoles lens spec from packages/rbac-domain and asserts Contract #6
// §6.10 item 4 / §6.2 platform + roles output for an ordinary role-holding
// actor.
//
// Representative graph seeded:
//
//	alice (identity)
//	  ─[holdsRole]─> admin (role) <─[grantedBy]─ permRead (permission)
//	                              <─[grantedBy]─ permWrite (permission)
//
// Service access is no longer projected by any core or rbac lens (Path B —
// retired with the service/location remnants); it is deferred to a future
// service package, so this test does not seed services and asserts no
// serviceAccess column.
func TestRbacCapabilityRolesLens_E2E(t *testing.T) {
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
	putEdge(t, reg, adjKV, "grantedBy", "permread", "admin")
	putEdge(t, reg, adjKV, "grantedBy", "permwrite", "admin")

	body := rolesLensSpec(t)
	eng := New()
	cr, err := eng.Parse(body)
	require.NoError(t, err, "capabilityRoles cypher must parse")

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
		require.NoError(t, err, "capabilityRoles query must execute without error")
		durations = append(durations, dur)
		results = out
	}

	require.Len(t, results, 1, "capabilityRoles query should produce exactly one row per actor")
	row := results[0].Values

	// actorKey
	require.Equal(t, aliceKey, row["actorKey"])

	// platformPermissions: collect of {operationType, scope} for permread+permwrite.
	pp, ok := row["platformPermissions"].([]any)
	require.True(t, ok, "platformPermissions must be a list, got %T", row["platformPermissions"])
	require.Len(t, pp, 2, "platformPermissions should have 2 entries")

	// capabilityRoles projects no serviceAccess column (Path B).
	require.NotContains(t, row, "serviceAccess",
		"capabilityRoles must NOT project serviceAccess (Path B — service projection retired)")

	// roles
	roles, ok := row["roles"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, roles, "expected at least one role")

	// Latency: log mean / p95 / p99 (records, doesn't halt).
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	mean := sum / time.Duration(len(durations))
	p95 := durations[int(float64(len(durations))*0.95)]
	p99 := durations[len(durations)-1]
	t.Logf("capabilityRoles lens latency over %d runs: mean=%v p95=%v p99=%v",
		runs, mean, p95, p99)
}

// rolesLensSpec returns the rbac-domain capabilityRoles lens cypher, selected
// by CanonicalName so the package may declare additional lenses without this
// conformance test silently exercising the wrong one.
func rolesLensSpec(t *testing.T) string {
	t.Helper()
	for _, l := range rbacdomain.Lenses() {
		if l.CanonicalName == "capabilityRoles" {
			return l.Spec
		}
	}
	t.Fatal("rbac-domain must declare a capabilityRoles lens")
	return ""
}

// TestCapabilityEphemeralLens_E2E exercises the link-sourced ephemeral-grant
// behaviors of the orchestration-base capabilityEphemeral lens:
//
//	task1 ─[assignedTo]─> alice (future expiry)          → granted (direct, alice)
//	taskexpired ─[assignedTo]─> alice (past expiry)       → filtered (expired)
//	task2 ─[assignedTo]─> bob (future expiry)             → granted (direct, bob)
//	alice ─[reportsTo]─> bob   → bob (manager) inherits alice's task1 (downward
//	                            delegation); alice does NOT inherit bob's task2.
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

	eng := New()
	cr, err := eng.Parse(ephemeralLensSpec(t))
	require.NoError(t, err, "capabilityEphemeral cypher must parse")

	type grant struct{ op, target string }
	projectGrants := func(actor string) map[string]grant {
		actorKey := vtxKey(reg, actor)
		params := map[string]any{
			"actorKey":    actorKey,
			"now":         time.Now().UTC().Format(time.RFC3339),
			"projectedAt": time.Now().UTC().Format(time.RFC3339),
		}
		out, err := eng.ExecuteWith(context.Background(), cr,
			ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
		require.NoError(t, err, "capabilityEphemeral query must execute")
		require.Len(t, out, 1, "should produce exactly one row per actor")
		row := out[0].Values
		require.Equal(t, actorKey, row["actorKey"])

		eg, ok := row["ephemeralGrants"].([]any)
		require.True(t, ok, "ephemeralGrants must be a list")
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
		return byTask
	}

	// --- alice (the report) ---
	aliceGrants := projectGrants("alice")

	// task1 (direct) present + link-sourced fields.
	g1, ok := aliceGrants[vtxKey(reg, "task1")]
	require.True(t, ok, "alice ephemeralGrants must include task1 (direct)")
	require.Equal(t, "delete", g1.op, "task1 operationType must be link-sourced from opDelete")
	require.Equal(t, vtxKey(reg, "doc1"), g1.target, "task1 target must be link-sourced from scopedTo")

	// taskexpired filtered out.
	_, expiredPresent := aliceGrants[vtxKey(reg, "taskexpired")]
	require.False(t, expiredPresent, "alice ephemeralGrants must NOT include the expired task")

	// alice (subordinate) must NOT inherit bob's (manager's) task — no upward
	// privilege escalation (Contract #6 §6.6, Contract #1 §1.1).
	_, aliceHasTask2 := aliceGrants[vtxKey(reg, "task2")]
	require.False(t, aliceHasTask2,
		"alice (subordinate) must NOT inherit bob's task2 — reportsTo is downward delegation")

	// --- bob (the manager) ---
	bobGrants := projectGrants("bob")

	// task2 (bob's direct assignment) present + link-sourced fields.
	g2, ok := bobGrants[vtxKey(reg, "task2")]
	require.True(t, ok, "bob ephemeralGrants must include task2 (direct)")
	require.Equal(t, "approve", g2.op, "task2 operationType must be link-sourced from opApprove")
	require.Equal(t, vtxKey(reg, "doc3"), g2.target, "task2 target must be link-sourced from scopedTo")

	// bob (manager) inherits alice's (report's) task1 via reportsTo — downward
	// delegation.
	gInherited, ok := bobGrants[vtxKey(reg, "task1")]
	require.True(t, ok, "bob (manager) must inherit alice's task1 via reportsTo (downward delegation)")
	require.Equal(t, "delete", gInherited.op, "inherited task1 operationType must be link-sourced from opDelete")
	require.Equal(t, vtxKey(reg, "doc1"), gInherited.target, "inherited task1 target must be link-sourced from scopedTo")
}

// TestCapabilityEphemeralLens_NoLiveGrants_NoRealRow proves the A3 absence
// mechanism at the cypher level: an actor with NO live task (no task at all,
// or only expired tasks) produces a row whose ephemeralGrants collect carries
// only degenerate (null-taskKey) artifacts — zero REAL grants. The envelope
// wrapper turns that into a delete (covered by the capabilityenv unit tests),
// so cap.ephemeral.<actor> is hard-deleted → step-3 reads absent →
// AuthContextMismatch.
func TestCapabilityEphemeralLens_NoLiveGrants_NoRealRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()

	// Two grant-less actors: carol (no tasks at all) and dave (only an
	// expired task). Neither must yield a real grant.
	putVertex(t, reg, coreKV, "carol", "identity", map[string]any{"name": "carol"})
	putVertex(t, reg, coreKV, "dave", "identity", map[string]any{"name": "dave"})
	putVertex(t, reg, coreKV, "opAdmin", "meta", map[string]any{"data": map[string]any{"operationType": "admin"}})
	putVertex(t, reg, coreKV, "doc1", "doc", nil)

	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	putVertex(t, reg, coreKV, "daveexpired", "task", map[string]any{
		"data": map[string]any{"status": "open", "expiresAt": past},
	})
	putEdge(t, reg, adjKV, "assignedTo", "daveexpired", "dave")
	putEdge(t, reg, adjKV, "forOperation", "daveexpired", "opAdmin")
	putEdge(t, reg, adjKV, "scopedTo", "daveexpired", "doc1")

	eng := New()
	cr, err := eng.Parse(ephemeralLensSpec(t))
	require.NoError(t, err)

	for _, actor := range []string{"carol", "dave"} {
		actorKey := vtxKey(reg, actor)
		params := map[string]any{
			"actorKey":    actorKey,
			"now":         time.Now().UTC().Format(time.RFC3339),
			"projectedAt": time.Now().UTC().Format(time.RFC3339),
		}
		out, err := eng.ExecuteWith(context.Background(), cr,
			ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
		require.NoError(t, err)
		require.Len(t, out, 1, "actor %s should still produce exactly one (anchor) row", actor)
		eg, _ := out[0].Values["ephemeralGrants"].([]any)
		realGrants := 0
		for _, e := range eg {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if tk, ok := m["taskKey"].(string); ok && tk != "" {
				realGrants++
			}
		}
		require.Zero(t, realGrants, "actor %s must have ZERO real grants (no live task)", actor)
	}
}
