// Unit tests for aiagent.Traverser.
//
// These tests use an embedded NATS server + substrate.Conn so we exercise
// the real KV path without Docker. They do NOT exercise the full Processor
// commit path — that is covered by fr19_northstar_test.go.
package aiagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/aiagent"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

const (
	unitCoreBucket = "core-kv"
	unitCapBucket  = "capability-kv"
)

// setupUnitEnv starts embedded NATS, provisions the two KV buckets used
// by the traversal unit tests, and returns a ready Traverser + context.
func setupUnitEnv(t *testing.T) (context.Context, *substrate.Conn, *aiagent.Traverser) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "aiagent-unit-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)

	js := conn.JetStream()
	for _, bucket := range []string{unitCoreBucket, unitCapBucket} {
		if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket: bucket,
		}); err != nil {
			t.Fatalf("create KV %q: %v", bucket, err)
		}
	}

	tr := aiagent.NewTraverser(conn, unitCoreBucket, unitCapBucket)
	return ctx, conn, tr
}

// putKV marshals val as JSON and writes it to bucket/key.
func putKV(t *testing.T, ctx context.Context, conn *substrate.Conn, bucket, key string, val any) {
	t.Helper()
	b, err := json.Marshal(val)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, bucket, key, b); err != nil {
		t.Fatalf("KVPut %s/%s: %v", bucket, key, err)
	}
}

// seedDDLAspectsFull seeds the vertex key, all seven self-description aspects,
// accepting explicit script and permittedCommands values. The vertex itself is
// seeded as a live (isDeleted: false) meta.ddl.vertexType entry so
// ReadDDLAspects' liveness pre-check passes.
func seedDDLAspectsFull(t *testing.T, ctx context.Context, conn *substrate.Conn, ddlKey, description, inputSchema, outputSchema string, fieldDescs map[string]string, examples []aiagent.ExampleEntry, script string, permittedCommands []string) {
	t.Helper()

	// Seed the vertex itself (required for F-007 liveness pre-check).
	putKV(t, ctx, conn, unitCoreBucket, ddlKey, map[string]any{
		"class": "meta.ddl.vertexType", "isDeleted": false, "data": map[string]any{},
	})

	putKV(t, ctx, conn, unitCoreBucket, ddlKey+".description", map[string]any{
		"class": "description", "isDeleted": false,
		"data": map[string]any{"text": description},
	})
	putKV(t, ctx, conn, unitCoreBucket, ddlKey+".inputSchema", map[string]any{
		"class": "inputSchema", "isDeleted": false,
		"data": map[string]any{"schema": inputSchema},
	})
	putKV(t, ctx, conn, unitCoreBucket, ddlKey+".outputSchema", map[string]any{
		"class": "outputSchema", "isDeleted": false,
		"data": map[string]any{"schema": outputSchema},
	})

	fdMap := make(map[string]any, len(fieldDescs))
	for k, v := range fieldDescs {
		fdMap[k] = v
	}
	putKV(t, ctx, conn, unitCoreBucket, ddlKey+".fieldDescription", map[string]any{
		"class": "fieldDescription", "isDeleted": false,
		"data": map[string]any{"fieldDescriptions": fdMap},
	})

	exSlice := make([]any, len(examples))
	for i, ex := range examples {
		exSlice[i] = map[string]any{
			"name":            ex.Name,
			"payload":         ex.Payload,
			"expectedOutcome": ex.ExpectedOutcome,
		}
	}
	putKV(t, ctx, conn, unitCoreBucket, ddlKey+".examples", map[string]any{
		"class": "examples", "isDeleted": false,
		"data": map[string]any{"examples": exSlice},
	})
	putKV(t, ctx, conn, unitCoreBucket, ddlKey+".script", map[string]any{
		"class": "script", "isDeleted": false,
		"data": map[string]any{"source": script},
	})
	cmds := make([]any, len(permittedCommands))
	for i, c := range permittedCommands {
		cmds[i] = c
	}
	putKV(t, ctx, conn, unitCoreBucket, ddlKey+".permittedCommands", map[string]any{
		"class": "permittedCommands", "isDeleted": false,
		"data": map[string]any{"commands": cmds},
	})
}

// --- ReadCapability tests ---

// TestReadCapability_HappyPath seeds a capability doc and asserts it is
// returned correctly.
func TestReadCapability_HappyPath(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	actorID := "ReadCapTestActrID00001"
	capKey := "cap.roles.identity." + actorID
	putKV(t, ctx, conn, unitCapBucket, capKey, map[string]any{
		"key":                    capKey,
		"actor":                  "vtx.identity." + actorID,
		"version":                "1.0",
		"projectedAt":            "2026-05-23T00:00:00Z",
		"projectedFromRevisions": map[string]any{},
		"lanes":                  []string{"default"},
		"platformPermissions": []any{
			map[string]any{"operationType": "CreateRole", "scope": "any"},
		},
		"serviceAccess":   []any{},
		"ephemeralGrants": []any{},
		"roles":           []string{},
	})

	doc, err := tr.ReadCapability(ctx, actorID)
	if err != nil {
		t.Fatalf("ReadCapability: %v", err)
	}
	if doc.Actor != "vtx.identity."+actorID {
		t.Errorf("actor mismatch: got %q want %q", doc.Actor, "vtx.identity."+actorID)
	}
	if len(doc.PlatformPermissions) != 1 || doc.PlatformPermissions[0].OperationType != "CreateRole" {
		t.Errorf("unexpected platformPermissions: %+v", doc.PlatformPermissions)
	}
}

// TestReadCapability_MergesCoreAndRoles reproduces the
// root-designation-topology-reconverge regression: an actor holding the
// primordial operator role (so cap.identity.<actor>, the kernel-literal
// anchor, is projected) ALSO holds an rbac-granted permission that only
// lives in cap.roles.identity.<actor>. Neither producer is a subset of the
// other, so ReadCapability must return the union of both, not just one.
func TestReadCapability_MergesCoreAndRoles(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	actorID := "MergeCapTestActorID001"
	coreKey := "cap.identity." + actorID
	putKV(t, ctx, conn, unitCapBucket, coreKey, map[string]any{
		"key":                    coreKey,
		"actor":                  "vtx.identity." + actorID,
		"version":                "1.0",
		"projectedAt":            "2026-05-23T00:00:00Z",
		"projectedFromRevisions": map[string]any{},
		"lanes":                  []string{"default", "meta", "urgent", "system"},
		"platformPermissions": []any{
			map[string]any{"operationType": "CreateMetaVertex", "scope": "any"},
			map[string]any{"operationType": "InstallPackage", "scope": "any"},
		},
		"serviceAccess":   []any{},
		"ephemeralGrants": []any{},
		"roles":           []string{},
	})

	rolesKey := "cap.roles.identity." + actorID
	putKV(t, ctx, conn, unitCapBucket, rolesKey, map[string]any{
		"key":                    rolesKey,
		"actor":                  "vtx.identity." + actorID,
		"version":                "1.0",
		"projectedAt":            "2026-05-23T00:00:00Z",
		"projectedFromRevisions": map[string]any{},
		"lanes":                  []string{"default"},
		"platformPermissions": []any{
			map[string]any{"operationType": "CreateBook", "scope": "any"},
		},
		"serviceAccess":   []any{},
		"ephemeralGrants": []any{},
		"roles":           []string{"vtx.role.OperatorRoleID000001"},
	})

	doc, err := tr.ReadCapability(ctx, actorID)
	if err != nil {
		t.Fatalf("ReadCapability: %v", err)
	}
	has := func(op string) bool {
		for _, p := range doc.PlatformPermissions {
			if p.OperationType == op {
				return true
			}
		}
		return false
	}
	for _, op := range []string{"CreateMetaVertex", "InstallPackage", "CreateBook"} {
		if !has(op) {
			t.Errorf("merged capability doc missing %q (platformPermissions: %+v)", op, doc.PlatformPermissions)
		}
	}
	if len(doc.Roles) != 1 || doc.Roles[0] != "vtx.role.OperatorRoleID000001" {
		t.Errorf("merged capability doc lost roles: %+v", doc.Roles)
	}
	wantLanes := map[string]bool{"default": true, "meta": true, "urgent": true, "system": true}
	if len(doc.Lanes) != len(wantLanes) {
		t.Errorf("merged capability doc lanes mismatch: %+v", doc.Lanes)
	}
	for _, l := range doc.Lanes {
		if !wantLanes[l] {
			t.Errorf("unexpected lane %q in merged doc", l)
		}
	}
}

// TestReadCapability_NotFound asserts an error is returned when the key
// is absent from Capability KV.
func TestReadCapability_NotFound(t *testing.T) {
	ctx, _, tr := setupUnitEnv(t)
	_, err := tr.ReadCapability(ctx, "NoSuchActorXXXX000001")
	if err == nil {
		t.Fatal("expected error for missing capability entry, got nil")
	}
	if !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound in error chain, got: %v", err)
	}
}

// --- DiscoverDDL tests ---

// TestDiscoverDDL_HappyPath seeds a meta-vertex + canonicalName aspect and
// asserts DiscoverDDL returns the correct key.
func TestDiscoverDDL_HappyPath(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	metaID := "DiscoverDDLTestMeta0001"
	metaKey := "vtx.meta." + metaID
	putKV(t, ctx, conn, unitCoreBucket, metaKey, map[string]any{
		"class": "meta.ddl.vertexType", "isDeleted": false, "data": map[string]any{},
	})
	putKV(t, ctx, conn, unitCoreBucket, metaKey+".canonicalName", map[string]any{
		"class": "canonicalName", "isDeleted": false,
		"data": map[string]any{"value": "DiscoverTestOp"},
	})

	// Seed a second meta-vertex that should NOT match.
	otherKey := "vtx.meta." + "OtherMetaVertexID00001"
	putKV(t, ctx, conn, unitCoreBucket, otherKey, map[string]any{
		"class": "meta.ddl.vertexType", "isDeleted": false, "data": map[string]any{},
	})
	putKV(t, ctx, conn, unitCoreBucket, otherKey+".canonicalName", map[string]any{
		"class": "canonicalName", "isDeleted": false,
		"data": map[string]any{"value": "OtherOp"},
	})

	got, err := tr.DiscoverDDL(ctx, "DiscoverTestOp")
	if err != nil {
		t.Fatalf("DiscoverDDL: %v", err)
	}
	if got != metaKey {
		t.Errorf("got %q want %q", got, metaKey)
	}
}

// TestDiscoverDDL_NotFound asserts ErrDDLNotFound is returned when no
// matching DDL exists.
func TestDiscoverDDL_NotFound(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	// Seed an unrelated meta-vertex.
	metaKey := "vtx.meta." + "DiscoverNotFoundID0001"
	putKV(t, ctx, conn, unitCoreBucket, metaKey, map[string]any{
		"class": "meta.ddl.vertexType", "isDeleted": false, "data": map[string]any{},
	})
	putKV(t, ctx, conn, unitCoreBucket, metaKey+".canonicalName", map[string]any{
		"class": "canonicalName", "isDeleted": false,
		"data": map[string]any{"value": "SomeOtherOp"},
	})

	_, err := tr.DiscoverDDL(ctx, "NonExistentOpType0001")
	if err == nil {
		t.Fatal("expected ErrDDLNotFound, got nil")
	}
	if !errors.Is(err, aiagent.ErrDDLNotFound) {
		t.Errorf("expected ErrDDLNotFound, got: %v", err)
	}
}

// TestDiscoverDDL_SkipsTombstonedCanonicalName asserts that a tombstoned
// canonicalName aspect is skipped.
func TestDiscoverDDL_SkipsTombstonedCanonicalName(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	metaKey := "vtx.meta." + "TombstonedCanonID00001"
	putKV(t, ctx, conn, unitCoreBucket, metaKey, map[string]any{
		"class": "meta.ddl.vertexType", "isDeleted": false, "data": map[string]any{},
	})
	// isDeleted: true → should be skipped
	putKV(t, ctx, conn, unitCoreBucket, metaKey+".canonicalName", map[string]any{
		"class": "canonicalName", "isDeleted": true,
		"data": map[string]any{"value": "TombstonedOp"},
	})

	_, err := tr.DiscoverDDL(ctx, "TombstonedOp")
	if err == nil {
		t.Fatal("expected ErrDDLNotFound for tombstoned canonicalName, got nil")
	}
	if !errors.Is(err, aiagent.ErrDDLNotFound) {
		t.Errorf("expected ErrDDLNotFound, got: %v", err)
	}
}

// --- ReadDDLAspects tests ---

// TestReadDDLAspects_HappyPath seeds all seven self-description aspects and
// asserts ReadDDLAspects parses them correctly.
func TestReadDDLAspects_HappyPath(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	ddlKey := "vtx.meta." + "ReadDDLAspectsID00001"
	seedDDLAspectsFull(t, ctx, conn, ddlKey,
		"Test description.",
		`{"type":"object","properties":{"note":{"type":"string"}}}`,
		`{"type":"object","properties":{}}`,
		map[string]string{"note": "A test note field."},
		[]aiagent.ExampleEntry{{Name: "ex1", Payload: map[string]any{"note": "hello"}, ExpectedOutcome: "ok"}},
		"def execute(state, op):\n    return {\"mutations\": [], \"events\": []}\n",
		[]string{"TestOp", "AltOp"},
	)

	got, err := tr.ReadDDLAspects(ctx, ddlKey)
	if err != nil {
		t.Fatalf("ReadDDLAspects: %v", err)
	}
	if got.Description != "Test description." {
		t.Errorf("Description: got %q want %q", got.Description, "Test description.")
	}
	if got.InputSchema == "" {
		t.Error("InputSchema is empty")
	}
	if got.OutputSchema == "" {
		t.Error("OutputSchema is empty")
	}
	if got.FieldDescriptions["note"] != "A test note field." {
		t.Errorf("FieldDescriptions[note]: got %q", got.FieldDescriptions["note"])
	}
	if len(got.Examples) != 1 || got.Examples[0].Name != "ex1" {
		t.Errorf("Examples: got %v", got.Examples)
	}
	if got.Script == "" {
		t.Error("Script is empty")
	}
	if len(got.PermittedCommands) != 2 || got.PermittedCommands[0] != "TestOp" || got.PermittedCommands[1] != "AltOp" {
		t.Errorf("PermittedCommands: got %v want [TestOp AltOp]", got.PermittedCommands)
	}
}

// TestReadDDLAspects_MissingAspect asserts ErrAspectMissing is returned when
// a required aspect is absent. The vertex itself is seeded as live; only the
// description aspect is written — all other six are missing.
func TestReadDDLAspects_MissingAspect(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	ddlKey := "vtx.meta." + "MissingAspectDDLID0001"
	// Seed the vertex (so liveness pre-check passes) and description only.
	putKV(t, ctx, conn, unitCoreBucket, ddlKey, map[string]any{
		"class": "meta.ddl.vertexType", "isDeleted": false, "data": map[string]any{},
	})
	putKV(t, ctx, conn, unitCoreBucket, ddlKey+".description", map[string]any{
		"class": "description", "isDeleted": false,
		"data": map[string]any{"text": "Only description, nothing else."},
	})

	_, err := tr.ReadDDLAspects(ctx, ddlKey)
	if err == nil {
		t.Fatal("expected ErrAspectMissing, got nil")
	}
	if !errors.Is(err, aiagent.ErrAspectMissing) {
		t.Errorf("expected ErrAspectMissing, got: %v", err)
	}
}

// TestDiscoverDDL_DuplicateCanonicalName asserts that DiscoverDDL returns an
// error (not ErrDDLNotFound) when two live meta-vertices share the same
// canonicalName — indicating inconsistent cell state.
func TestDiscoverDDL_DuplicateCanonicalName(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	for _, id := range []string{"DupCanonMetaID000001", "DupCanonMetaID000002"} {
		metaKey := "vtx.meta." + id
		putKV(t, ctx, conn, unitCoreBucket, metaKey, map[string]any{
			"class": "meta.ddl.vertexType", "isDeleted": false, "data": map[string]any{},
		})
		putKV(t, ctx, conn, unitCoreBucket, metaKey+".canonicalName", map[string]any{
			"class": "canonicalName", "isDeleted": false,
			"data": map[string]any{"value": "DuplicateOp"},
		})
	}

	_, err := tr.DiscoverDDL(ctx, "DuplicateOp")
	if err == nil {
		t.Fatal("expected error for duplicate canonicalName, got nil")
	}
	// Must NOT be ErrDDLNotFound — it should be a distinct inconsistency error.
	if errors.Is(err, aiagent.ErrDDLNotFound) {
		t.Errorf("expected distinct inconsistency error, got ErrDDLNotFound: %v", err)
	}
	if !strings.Contains(err.Error(), "inconsistent") {
		t.Errorf("expected error message to mention inconsistent state, got: %v", err)
	}
}

// --- ReadCompensation tests ---

// TestTraverser_ReadCompensation_HappyPath seeds a .compensation aspect and
// asserts ReadCompensation returns the expected map with inverseOperationType.
func TestTraverser_ReadCompensation_HappyPath(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	metaKey := "vtx.meta." + "CompensationTestID0001"
	// Seed the .compensation aspect with canonical shape.
	putKV(t, ctx, conn, unitCoreBucket, metaKey+".compensation", map[string]any{
		"class": "compensation", "isDeleted": false,
		"data": map[string]any{
			"inverseOperationType": "TombstoneMetaVertex",
			"payloadTemplate":      map[string]any{"metaKey": "{{detail.metaKey}}"},
			"revisionTemplate":     map[string]any{"metaKey": "{{revisions[detail.metaKey]}}"},
		},
	})

	got, err := tr.ReadCompensation(ctx, metaKey)
	if err != nil {
		t.Fatalf("ReadCompensation: %v", err)
	}
	if got == nil {
		t.Fatal("ReadCompensation returned nil map")
	}
	if got["inverseOperationType"] != "TombstoneMetaVertex" {
		t.Errorf("inverseOperationType: got %v want TombstoneMetaVertex", got["inverseOperationType"])
	}
	// Verify payloadTemplate is present and has expected key.
	pt, ok := got["payloadTemplate"].(map[string]any)
	if !ok || pt == nil {
		t.Fatalf("payloadTemplate is not a map: %v", got["payloadTemplate"])
	}
	if pt["metaKey"] != "{{detail.metaKey}}" {
		t.Errorf("payloadTemplate.metaKey: got %v want {{detail.metaKey}}", pt["metaKey"])
	}
}

// TestTraverser_ReadCompensation_MissingAspect asserts ErrCompensationAspectMissing
// is returned when the .compensation aspect key is absent.
func TestTraverser_ReadCompensation_MissingAspect(t *testing.T) {
	ctx, _, tr := setupUnitEnv(t)

	_, err := tr.ReadCompensation(ctx, "vtx.meta."+"NoCompensationID00001")
	if err == nil {
		t.Fatal("expected error for missing compensation aspect, got nil")
	}
	if !errors.Is(err, aiagent.ErrCompensationAspectMissing) {
		t.Errorf("expected ErrCompensationAspectMissing in error chain, got: %v", err)
	}
}

// TestTraverser_ReadCompensation_TombstonedAspect asserts that a tombstoned
// .compensation aspect returns ErrCompensationAspectMissing.
func TestTraverser_ReadCompensation_TombstonedAspect(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	metaKey := "vtx.meta." + "TombstonedCompID00001"
	putKV(t, ctx, conn, unitCoreBucket, metaKey+".compensation", map[string]any{
		"class": "compensation", "isDeleted": true,
		"data": map[string]any{
			"inverseOperationType": "TombstoneMetaVertex",
		},
	})

	_, err := tr.ReadCompensation(ctx, metaKey)
	if err == nil {
		t.Fatal("expected ErrCompensationAspectMissing for tombstoned aspect, got nil")
	}
	if !errors.Is(err, aiagent.ErrCompensationAspectMissing) {
		t.Errorf("expected ErrCompensationAspectMissing, got: %v", err)
	}
}

// TestDiscoverDDL_SkipsTombstonedVertex asserts that DiscoverDDL skips a
// meta-vertex whose vertex key is tombstoned (isDeleted: true) even when
// its .canonicalName aspect is still intact.
//
// This test validates the Story 5.3 tombstone guard added to DiscoverDDL:
// TombstoneMetaVertex only tombstones the vertex key, not the aspects.
func TestDiscoverDDL_SkipsTombstonedVertex(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	metaKey := "vtx.meta." + "TombstonedVertexID00001"
	// Meta-vertex itself is tombstoned.
	putKV(t, ctx, conn, unitCoreBucket, metaKey, map[string]any{
		"class": "meta.ddl.vertexType", "isDeleted": true, "data": map[string]any{},
	})
	// But its .canonicalName aspect is still intact (not tombstoned).
	putKV(t, ctx, conn, unitCoreBucket, metaKey+".canonicalName", map[string]any{
		"class": "canonicalName", "isDeleted": false,
		"data": map[string]any{"value": "TombstonedVertexOp"},
	})

	// DiscoverDDL should NOT return this vertex — it is tombstoned.
	_, err := tr.DiscoverDDL(ctx, "TombstonedVertexOp")
	if err == nil {
		t.Fatal("expected ErrDDLNotFound for tombstoned vertex, got nil")
	}
	if !errors.Is(err, aiagent.ErrDDLNotFound) {
		t.Errorf("expected ErrDDLNotFound, got: %v", err)
	}
}
