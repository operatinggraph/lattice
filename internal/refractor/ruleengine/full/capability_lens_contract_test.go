// Story 3.2b §7 — Contract #6 §6.13 byte-shape conformance test.
//
// Per Contract #6 §6.13: "Story 3.2's contract-conformance test runs
// the bootstrap cypher query against a deterministically seeded graph
// and asserts the output structure matches the shape below. This test
// catches schema drift if anyone modifies the Capability Lens cypher
// query without updating this contract (or vice versa)."
//
// The test:
//   - Uses the LITERAL `CapabilityLensDefinition().CypherRule` from the
//     bootstrap package — NO hand-copied simplified rule (Decision #6).
//   - Seeds a deterministic graph that exercises all three sections
//     (platformPermissions, serviceAccess, ephemeralGrants) and the
//     `roles` projection.
//   - Wraps the executor's RETURN row through capabilityenv.NewWrapper
//     so the assertion targets the on-wire envelope, not the raw
//     RETURN-row map.
//   - Asserts the envelope's structure field-by-field with descriptive
//     failure messages (NOT raw byte diff per Decision #6). Timestamps
//     and revisions are checked for presence + type only — their values
//     are non-deterministic by design.
package full_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/capabilityenv"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
)

// --- local test helpers (mirror the package-internal test scaffolding;
// kept here so the contract test can live in an external test package
// to avoid a capabilityenv→pipeline→full import cycle). ---

func contractStartKVs(t *testing.T) (jetstream.JetStream, jetstream.KeyValue, jetstream.KeyValue) {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	adj, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "contract-adj"})
	require.NoError(t, err)
	core, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "contract-core"})
	require.NoError(t, err)
	return js, adj, core
}

// contractStableID returns a deterministic NanoID for fixture names.
func contractStableID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte("contract:" + name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

func contractPutVertex(t *testing.T, kv jetstream.KeyValue, typ, name string, extra map[string]any) string {
	t.Helper()
	id := contractStableID(typ + ":" + name)
	key := "vtx." + typ + "." + id
	// Domain fields live under the `data` envelope (key/class/provenance stay
	// top-level), mirroring the Processor's vertex shape; the seeded capability
	// cypher reads them as node.data.<field>.
	body := map[string]any{"key": key, "class": typ, "data": extra}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = kv.Put(ctx, key, data)
	require.NoError(t, err)
	return key
}

func contractPutEdge(t *testing.T, adjKV jetstream.KeyValue, name, fromType, fromName, toType, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID := contractStableID(fromType + ":" + fromName)
	toID := contractStableID(toType + ":" + toName)
	edgeID := name + ":" + fromID + ":" + toID
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey:   edgeID,
		EdgeID:      edgeID,
		Name:        name,
		Direction:   "outbound",
		NodeID:      fromID,
		OtherNodeID: toID,
		OtherType:   toType,
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey:   edgeID,
		EdgeID:      edgeID,
		Name:        name,
		Direction:   "inbound",
		NodeID:      toID,
		OtherNodeID: fromID,
		OtherType:   fromType,
	}))
}

// TestCapabilityLens_ContractConformance asserts the Contract #6 §6.2
// envelope shape against the LITERAL bootstrap cypher rule.
func TestCapabilityLens_ContractConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	_, adjKV, coreKV := contractStartKVs(t)

	// --- deterministic graph fixture ---
	// One actor exercising all three sections + roles.
	aliceKey := contractPutVertex(t, coreKV, "identity", "alice", map[string]any{"name": "alice"})
	contractPutVertex(t, coreKV, "role", "admin", map[string]any{"canonicalName": "admin"})
	contractPutVertex(t, coreKV, "permission", "permread", map[string]any{
		"operationType": "read", "scope": "any",
	})
	contractPutEdge(t, adjKV, "holdsRole", "identity", "alice", "role", "admin")
	// Story 4.7 rename: grantsPermission(role→permission) became
	// grantedBy(permission→role); direction reverses.
	contractPutEdge(t, adjKV, "grantedBy", "permission", "permread", "role", "admin")

	contractPutVertex(t, coreKV, "location", "hq", nil)
	contractPutVertex(t, coreKV, "service", "svc", map[string]any{"class": "service"})
	contractPutEdge(t, adjKV, "containedIn", "identity", "alice", "location", "hq")
	contractPutEdge(t, adjKV, "availableAt", "location", "hq", "service", "svc")

	// The bootstrap `capability` cypher does not produce ephemeralGrants —
	// those are owned by the orchestration-base `capabilityEphemeral` lens
	// (key cap.ephemeral.<actor>). The link-sourced ephemeral conformance
	// lives in TestCapabilityEphemeralLens_ContractConformance below. No
	// task fixture is seeded here.

	// --- run the LITERAL bootstrap cypher ---
	body := bootstrap.CapabilityLensDefinition().CypherRule
	eng := full.New()
	cr, err := eng.Parse(body)
	require.NoError(t, err, "literal bootstrap cypher must parse")

	now := time.Now().Unix()
	projectedAt := time.Now().UTC().Format(time.RFC3339)
	params := map[string]any{
		"actorKey":    aliceKey,
		"now":         float64(now),
		"projectedAt": projectedAt,
	}
	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
	require.NoError(t, err, "literal bootstrap cypher must execute")
	require.Len(t, out, 1, "bootstrap query should produce exactly one row")
	row := out[0].Values
	keys := out[0].Key

	// --- wrap through the production envelope (Story 3.2a / Contract #6 §6.2) ---
	wrapper := capabilityenv.NewWrapper("vtx.meta.test-lens",
		func(k string) uint64 { return 42 })
	envRow, envKeys, envErr := wrapper(row, keys, params)
	require.NoError(t, envErr, "envelope wrapping must succeed")
	require.NotNil(t, envRow)
	require.NotNil(t, envKeys)

	// --- Contract #6 §6.2 field-by-field assertions ---

	// `key`: must be "cap.identity.<NanoID>" derived from actor vertex key.
	keyVal, ok := envRow["key"].(string)
	require.True(t, ok, "envelope.key must be a string")
	require.Truef(t, len(keyVal) > len("cap.identity."),
		"envelope.key must include actor NanoID; got %q", keyVal)
	require.Equalf(t, "cap.identity.", keyVal[:len("cap.identity.")],
		"envelope.key must start with 'cap.identity.'; got %q", keyVal)
	require.Equal(t, keyVal, envKeys["key"], "Keys map must mirror envelope.key")

	// `actor`: must equal the full actor vertex key passed in $actorKey.
	require.Equalf(t, aliceKey, envRow["actor"],
		"envelope.actor must equal $actorKey; got %v", envRow["actor"])

	// `version`: must be "1.0" (Contract #6 §6.3 Phase 1 pin).
	require.Equal(t, "1.0", envRow["version"], "envelope.version must be '1.0'")

	// `projectedAt`: must be the params projectedAt (RFC3339 string).
	pa, ok := envRow["projectedAt"].(string)
	require.True(t, ok, "envelope.projectedAt must be a string")
	require.Equalf(t, projectedAt, pa, "envelope.projectedAt must equal params.projectedAt")

	// `projectedFromRevisions`: must be a map; presence-checked only —
	// the test stub returns 42 for every key, so anchor + lens-def
	// revisions must both be present per Story 3.2a Decision #7.
	revs, ok := envRow["projectedFromRevisions"].(map[string]any)
	require.True(t, ok, "envelope.projectedFromRevisions must be a map")
	require.Containsf(t, revs, aliceKey,
		"projectedFromRevisions must include anchor revision; got %v", revs)
	require.Containsf(t, revs, "vtx.meta.test-lens",
		"projectedFromRevisions must include lens-def revision; got %v", revs)
	for k, v := range revs {
		_, isFloat := v.(float64)
		_, isUint64 := v.(uint64)
		require.Truef(t, isFloat || isUint64,
			"projectedFromRevisions[%q] must be numeric; got %T", k, v)
	}

	// `lanes`: must be a non-empty string array including "default".
	lanes, ok := envRow["lanes"].([]string)
	if !ok {
		// Allow []any as well for JSON round-trip safety.
		la, okAny := envRow["lanes"].([]any)
		require.Truef(t, okAny, "envelope.lanes must be a string array; got %T", envRow["lanes"])
		require.NotEmpty(t, la, "envelope.lanes must not be empty")
		require.Equal(t, "default", la[0])
	} else {
		require.NotEmpty(t, lanes, "envelope.lanes must not be empty")
		require.Equal(t, "default", lanes[0])
	}

	// `platformPermissions`: array of {operationType, scope}.
	pp, ok := envRow["platformPermissions"].([]any)
	require.True(t, ok, "envelope.platformPermissions must be an array")
	require.NotEmpty(t, pp, "platformPermissions must include at least one entry")
	foundRead := false
	for _, e := range pp {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if m["operationType"] == "read" && m["scope"] == "any" {
			foundRead = true
		}
		// Every non-null entry must carry both fields (Contract #6 §6.2).
		if m["operationType"] != nil {
			require.Contains(t, m, "operationType",
				"platformPermissions entry must carry operationType")
			require.Contains(t, m, "scope",
				"platformPermissions entry must carry scope")
		}
	}
	require.True(t, foundRead,
		"platformPermissions must include the seeded read/any entry")

	// `serviceAccess`: array of {service, serviceClass, resolvedVia, allowedOperations}.
	sa, ok := envRow["serviceAccess"].([]any)
	require.True(t, ok, "envelope.serviceAccess must be an array")
	require.NotEmpty(t, sa, "serviceAccess must include the seeded service entry")
	svcEntryOK := false
	for _, e := range sa {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if m["service"] == nil {
			continue // aggregation null row
		}
		require.Contains(t, m, "service")
		require.Contains(t, m, "serviceClass")
		require.Contains(t, m, "resolvedVia")
		require.Contains(t, m, "allowedOperations")
		svcEntryOK = true
	}
	require.True(t, svcEntryOK,
		"serviceAccess must include a real (non-null) entry per Contract #6 §6.2")

	// `ephemeralGrants`: the bootstrap envelope still carries the field for
	// shape stability (the wrapper hardcodes it), but post-7.1 the bootstrap
	// cypher RETURNs no ephemeral rows, so it must be EMPTY here. The
	// link-sourced grant projection is asserted against the orchestration-base
	// lens in TestCapabilityEphemeralLens_ContractConformance.
	eg, ok := envRow["ephemeralGrants"].([]any)
	require.True(t, ok, "envelope.ephemeralGrants must be an array")
	for _, e := range eg {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		require.Nil(t, m["taskKey"],
			"bootstrap cypher must produce NO real ephemeral grant post-7.1 (moved to capabilityEphemeral lens)")
	}

	// `roles`: array of role vertex keys.
	roles, ok := envRow["roles"].([]any)
	require.True(t, ok, "envelope.roles must be an array")
	require.NotEmpty(t, roles, "roles must include the seeded admin role")
}

// TestCapabilityEphemeralLens_ContractConformance asserts the Contract #6
// §6.6 (Phase-2 amendment) cap.ephemeral.<actor> envelope shape against the
// LITERAL orchestration-base `capabilityEphemeral` lens spec. The grant is
// LINK-SOURCED: the lens walks assignedTo/forOperation/scopedTo (Contract
// #10 §10.1) — NOT the old task.data.grantedOperationType/targetKey fields.
func TestCapabilityEphemeralLens_ContractConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	_, adjKV, coreKV := contractStartKVs(t)

	// --- deterministic graph fixture: a manager with one link-shaped task ---
	managerKey := contractPutVertex(t, coreKV, "identity", "manager", map[string]any{"name": "manager"})
	// The operation meta-vertex the task grants (operationType under data).
	opKey := contractPutVertex(t, coreKV, "meta", "approveOp", map[string]any{
		"operationType": "ApproveLeaseApplication",
	})
	// The scopedTo target (the specific lease application).
	targetKey := contractPutVertex(t, coreKV, "leaseApp", "applicant", map[string]any{"state": "pending"})
	// The task vertex — root data is scalars only {status, expiresAt}; NO
	// grantedOperationType / targetKey fields (Contract #10 §10.1).
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	contractPutVertex(t, coreKV, "task", "task1", map[string]any{
		"status":    "open",
		"expiresAt": future,
	})

	// Links (task = source, the other vertex = target).
	contractPutEdge(t, adjKV, "assignedTo", "task", "task1", "identity", "manager")
	contractPutEdge(t, adjKV, "forOperation", "task", "task1", "meta", "approveOp")
	contractPutEdge(t, adjKV, "scopedTo", "task", "task1", "leaseApp", "applicant")

	// --- run the LITERAL orchestration-base capabilityEphemeral cypher ---
	lensSpecs := orchestrationbase.Lenses()
	require.Len(t, lensSpecs, 1, "orchestration-base must declare exactly one lens")
	require.Equal(t, "capabilityEphemeral", lensSpecs[0].CanonicalName)
	body := lensSpecs[0].Spec

	eng := full.New()
	cr, err := eng.Parse(body)
	require.NoError(t, err, "literal capabilityEphemeral cypher must parse")

	now := time.Now().UTC().Format(time.RFC3339)
	projectedAt := time.Now().UTC().Format(time.RFC3339)
	params := map[string]any{
		"actorKey":    managerKey,
		"now":         now,
		"projectedAt": projectedAt,
	}
	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
	require.NoError(t, err, "literal capabilityEphemeral cypher must execute")
	require.Len(t, out, 1, "ephemeral query should produce exactly one row")
	row := out[0].Values
	keys := out[0].Key

	// --- wrap through the production ephemeral envelope ---
	wrapper := capabilityenv.NewEphemeralWrapper("vtx.meta.test-eph-lens",
		func(k string) uint64 { return 7 })
	envRow, envKeys, envErr := wrapper(row, keys, params)
	require.NoError(t, envErr, "ephemeral envelope wrapping must succeed")
	require.NotNil(t, envRow)
	require.NotNil(t, envKeys)

	// `key`: must be "cap.ephemeral.identity.<NanoID>".
	keyVal, ok := envRow["key"].(string)
	require.True(t, ok, "envelope.key must be a string")
	require.Truef(t, len(keyVal) > len("cap.ephemeral.identity."),
		"envelope.key must include actor NanoID; got %q", keyVal)
	require.Equalf(t, "cap.ephemeral.identity.", keyVal[:len("cap.ephemeral.identity.")],
		"envelope.key must start with 'cap.ephemeral.identity.'; got %q", keyVal)
	require.Equal(t, keyVal, envKeys["key"], "Keys map must mirror envelope.key")

	// `actor` / `version`.
	require.Equal(t, managerKey, envRow["actor"], "envelope.actor must equal $actorKey")
	require.Equal(t, "1.0", envRow["version"], "envelope.version must be '1.0'")

	// `ephemeralGrants`: link-sourced {source, taskKey, operationType, target, expiresAt}.
	eg, ok := envRow["ephemeralGrants"].([]any)
	require.True(t, ok, "envelope.ephemeralGrants must be an array")
	require.NotEmpty(t, eg, "ephemeralGrants must include the seeded link-sourced grant")
	grantFound := false
	for _, e := range eg {
		m, ok := e.(map[string]any)
		if !ok || m["taskKey"] == nil {
			continue
		}
		grantFound = true
		require.Equal(t, "task", m["source"], "grant source must be 'task'")
		// operationType is LINK-sourced from forOperation→op.data.operationType.
		require.Equalf(t, "ApproveLeaseApplication", m["operationType"],
			"operationType must be link-sourced from the forOperation op; got %v", m["operationType"])
		// target is LINK-sourced from scopedTo→target.key.
		require.Equalf(t, targetKey, m["target"],
			"target must be link-sourced from scopedTo; got %v", m["target"])
		require.Equalf(t, future, m["expiresAt"],
			"expiresAt must be the task root scalar; got %v", m["expiresAt"])
		_ = opKey
	}
	require.True(t, grantFound,
		"ephemeralGrants must include a real (non-null) link-sourced grant")
}
