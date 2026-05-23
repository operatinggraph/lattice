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
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/aiagent"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
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

// seedDDLAspects seeds all five self-description aspects for a DDL key.
func seedDDLAspects(t *testing.T, ctx context.Context, conn *substrate.Conn, ddlKey, description, inputSchema, outputSchema string, fieldDescs map[string]string, examples []aiagent.ExampleEntry) {
	t.Helper()

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
}

// --- ReadCapability tests ---

// TestReadCapability_HappyPath seeds a capability doc and asserts it is
// returned correctly.
func TestReadCapability_HappyPath(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	actorID := "ReadCapTestActrID00001"
	capKey := "cap.identity." + actorID
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

// TestReadDDLAspects_HappyPath seeds all five self-description aspects and
// asserts ReadDDLAspects parses them correctly.
func TestReadDDLAspects_HappyPath(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	ddlKey := "vtx.meta." + "ReadDDLAspectsID00001"
	seedDDLAspects(t, ctx, conn, ddlKey,
		"Test description.",
		`{"type":"object","properties":{"note":{"type":"string"}}}`,
		`{"type":"object","properties":{}}`,
		map[string]string{"note": "A test note field."},
		[]aiagent.ExampleEntry{{Name: "ex1", Payload: map[string]any{"note": "hello"}, ExpectedOutcome: "ok"}},
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
}

// TestReadDDLAspects_MissingAspect asserts ErrAspectMissing is returned when
// a required aspect is absent.
func TestReadDDLAspects_MissingAspect(t *testing.T) {
	ctx, conn, tr := setupUnitEnv(t)

	ddlKey := "vtx.meta." + "MissingAspectDDLID0001"
	// Seed only description — omit the other four.
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
