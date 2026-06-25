// Self-description end-to-end tests.
//
// Coverage:
//  1. TestSelfDescription_KernelDDLsHaveAllFiveAspects — verifies that
//     after SeedPrimordial each of the 5 primordial aspect-type
//     meta-vertices carries all 5 self-description aspects in Core KV.
//  2. TestSelfDescription_CreateMetaVertexRequiresAllFiveAspects —
//     exercises the MetaRootDDLScript Starlark validation: missing any
//     of the 4 new required fields produces a MissingSelfDescription
//     error; a complete payload produces a valid mutation batch.
package bootstrap_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/testutil"
)

// TestSelfDescription_KernelDDLsHaveAllFiveAspects verifies the primordial
// seeder writes all 5 self-description aspects on each of the 5 aspect-type
// meta-vertices (description, inputSchema, outputSchema, fieldDescription,
// examples).
func TestSelfDescription_KernelDDLsHaveAllFiveAspects(t *testing.T) {
	ctx, conn := testutil.SetupPackageTestEnv(t)

	aspectTypeKeys := []string{
		bootstrap.AspectTypeDescriptionKey,
		bootstrap.AspectTypeInputSchemaKey,
		bootstrap.AspectTypeOutputSchemaKey,
		bootstrap.AspectTypeFieldDescriptionKey,
		bootstrap.AspectTypeExamplesKey,
	}
	selfDescAspects := []string{
		"description",
		"inputSchema",
		"outputSchema",
		"fieldDescription",
		"examples",
	}

	for _, vtxKey := range aspectTypeKeys {
		// Vertex itself must exist with class=meta.ddl.aspectType.
		entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, vtxKey)
		if err != nil {
			t.Errorf("aspect-type vertex missing: %s: %v", vtxKey, err)
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			t.Errorf("unmarshal %s: %v", vtxKey, err)
			continue
		}
		if doc["class"] != "meta.ddl.aspectType" {
			t.Errorf("%s class = %v, want meta.ddl.aspectType", vtxKey, doc["class"])
		}

		// Each of the 5 self-description aspects must be present.
		for _, asp := range selfDescAspects {
			aspKey := vtxKey + "." + asp
			aspEntry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, aspKey)
			if err != nil {
				t.Errorf("aspect %s missing on %s: %v", asp, vtxKey, err)
				continue
			}
			var aspDoc map[string]any
			if err := json.Unmarshal(aspEntry.Value, &aspDoc); err != nil {
				t.Errorf("unmarshal aspect %s: %v", aspKey, err)
			}
		}
	}
}

// metaRootScriptSource returns the MetaRootDDLScript for Starlark unit tests.
func metaRootScriptSource() string {
	return bootstrap.MetaRootDDLScript
}

// makeMetaRootCtx builds a ScriptContext for a CreateMetaVertex operation
// against the MetaRootDDLScript.
func makeMetaRootCtx(payloadJSON string) processor.ScriptContext {
	return processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "SdE2eMetaV0000000001",
			Lane:          processor.LaneDefault,
			OperationType: "CreateMetaVertex",
			Actor:         "vtx.identity.SdE2eActrXzBbCdEfGHJ",
			SubmittedAt:   "2026-05-23T10:00:00Z",
			Payload:       json.RawMessage(payloadJSON),
		},
		Hydrated:     map[string]processor.VertexDoc{},
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: metaRootScriptSource(),
		ScriptClass:  "root",
	}
}

// fullDDLPayload returns a JSON payload with all required DDL fields.
func fullDDLPayload() string {
	return `{
		"targetClass":     "meta.ddl.vertexType",
		"canonicalName":   "book",
		"permittedCommands": ["CreateBook"],
		"description":     "Book vertex DDL.",
		"script":          "def execute(state, op): pass",
		"inputSchema":     "{\"type\":\"object\",\"properties\":{\"title\":{\"type\":\"string\"}},\"required\":[\"title\"]}",
		"outputSchema":    "{\"type\":\"object\",\"properties\":{\"bookKey\":{\"type\":\"string\"}},\"required\":[\"bookKey\"]}",
		"fieldDescription": {"title": "The book title."},
		"examples":        [{"name": "CreateBook example", "payload": {"title": "Dune"}, "expectedOutcome": "Creates vtx.meta.<NanoID>."}]
	}`
}

// TestSelfDescription_CreateMetaVertexRequiresAllFiveAspects verifies that
// the MetaRootDDLScript rejects CreateMetaVertex ops for DDL-class vertices
// when any of the 4 new self-description fields is missing, and accepts ops
// that include all of them.
func TestSelfDescription_CreateMetaVertexRequiresAllFiveAspects(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	cases := []struct {
		name        string
		payloadJSON string
		wantErr     string // substring expected in error, or "" for success
	}{
		{
			name: "missing inputSchema",
			payloadJSON: `{
				"targetClass": "meta.ddl.vertexType",
				"canonicalName": "book",
				"permittedCommands": ["CreateBook"],
				"description": "desc",
				"script": "def execute(state, op): pass",
				"outputSchema": "{\"type\":\"object\"}",
				"fieldDescription": {"title": "The title."},
				"examples": [{"name":"ex","payload":{},"expectedOutcome":"ok"}]
			}`,
			wantErr: "MissingSelfDescription: inputSchema",
		},
		{
			name: "missing outputSchema",
			payloadJSON: `{
				"targetClass": "meta.ddl.vertexType",
				"canonicalName": "book",
				"permittedCommands": ["CreateBook"],
				"description": "desc",
				"script": "def execute(state, op): pass",
				"inputSchema": "{\"type\":\"object\"}",
				"fieldDescription": {"title": "The title."},
				"examples": [{"name":"ex","payload":{},"expectedOutcome":"ok"}]
			}`,
			wantErr: "MissingSelfDescription: outputSchema",
		},
		{
			name: "missing fieldDescription",
			payloadJSON: `{
				"targetClass": "meta.ddl.vertexType",
				"canonicalName": "book",
				"permittedCommands": ["CreateBook"],
				"description": "desc",
				"script": "def execute(state, op): pass",
				"inputSchema": "{\"type\":\"object\"}",
				"outputSchema": "{\"type\":\"object\"}",
				"examples": [{"name":"ex","payload":{},"expectedOutcome":"ok"}]
			}`,
			wantErr: "MissingSelfDescription: fieldDescription",
		},
		{
			name: "missing examples",
			payloadJSON: `{
				"targetClass": "meta.ddl.vertexType",
				"canonicalName": "book",
				"permittedCommands": ["CreateBook"],
				"description": "desc",
				"script": "def execute(state, op): pass",
				"inputSchema": "{\"type\":\"object\"}",
				"outputSchema": "{\"type\":\"object\"}",
				"fieldDescription": {"title": "The title."}
			}`,
			wantErr: "MissingSelfDescription: examples",
		},
		{
			name:        "all five present — accepted",
			payloadJSON: fullDDLPayload(),
			wantErr:     "",
		},
		{
			name: "meta.lens class — valid LensSpec JSON string accepted",
			payloadJSON: `{
				"targetClass": "meta.lens",
				"canonicalName": "myLens",
				"description": "A lens.",
				"spec": "{\"cypherRule\":\"MATCH (b:book) RETURN b.id AS book_id\",\"targetType\":\"postgres\",\"targetConfig\":{\"dsn\":\"postgres://localhost/test\",\"table\":\"books\",\"key\":[\"book_id\"]}}"
			}`,
			wantErr: "",
		},
		{
			name: "meta.lens class — spec missing cypherRule rejected",
			payloadJSON: `{
				"targetClass": "meta.lens",
				"canonicalName": "myLens",
				"description": "A lens.",
				"spec": "{\"targetType\":\"postgres\",\"targetConfig\":{\"dsn\":\"postgres://localhost/test\",\"table\":\"books\",\"key\":[\"book_id\"]}}"
			}`,
			wantErr: "spec.cypherRule: required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := makeMetaRootCtx(tc.payloadJSON)
			result, err := runner.Run(ctx, sc)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got result: %+v", tc.wantErr, result)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(result.Mutations) == 0 {
					t.Fatalf("expected mutations, got none")
				}
			}
		})
	}
}

// TestMetaLensSpec_CypherRuleInAspectData verifies that submitting a
// CreateMetaVertex with targetClass=meta.lens and a valid spec JSON string
// produces a .spec aspect mutation whose document data contains the
// cypherRule key (not "source"). This is the SD-1 fix assertion.
func TestMetaLensSpec_CypherRuleInAspectData(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	// specJSON is the raw LensSpec object string that becomes the "spec" field value.
	// The JSON escaping is handled by encoding/json.Marshal so the payload is valid JSON.
	specObj := map[string]any{
		"cypherRule": "MATCH (b:book) RETURN b.id AS book_id, b.title AS title",
		"targetType": "postgres",
		"targetConfig": map[string]any{
			"dsn":   "postgres://localhost/test",
			"table": "books",
			"key":   []string{"book_id"},
		},
	}
	specBytes, err := json.Marshal(specObj)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	payload := map[string]any{
		"targetClass":  "meta.lens",
		"canonicalName": "books",
		"description":  "Projects book vertices to the books Postgres table.",
		"spec":         string(specBytes),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	payloadJSON := string(payloadBytes)

	sc := makeMetaRootCtx(payloadJSON)
	result, err := runner.Run(ctx, sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var specMutation *processor.MutationOp
	for i := range result.Mutations {
		m := &result.Mutations[i]
		if strings.HasSuffix(m.Key, ".spec") {
			specMutation = m
			break
		}
	}
	if specMutation == nil {
		t.Fatalf("no .spec mutation found in result; mutations: %+v", result.Mutations)
	}

	doc := specMutation.Document
	data, ok := doc["data"]
	if !ok {
		t.Fatalf(".spec mutation document missing 'data' key; document: %+v", doc)
	}
	dataMap, ok := data.(map[string]any)
	if !ok {
		t.Fatalf(".spec mutation document 'data' is not a map; got %T: %+v", data, data)
	}

	if _, hasCypherRule := dataMap["cypherRule"]; !hasCypherRule {
		keys := make([]string, 0, len(dataMap))
		for k := range dataMap {
			keys = append(keys, k)
		}
		t.Errorf(".spec aspect data missing 'cypherRule'; keys present: %v", keys)
	}
	if _, hasSource := dataMap["source"]; hasSource {
		t.Errorf(".spec aspect data contains old 'source' key — SD-1 fix not applied correctly")
	}
	if _, hasTargetType := dataMap["targetType"]; !hasTargetType {
		t.Errorf(".spec aspect data missing 'targetType'")
	}
	if _, hasTargetConfig := dataMap["targetConfig"]; !hasTargetConfig {
		t.Errorf(".spec aspect data missing 'targetConfig'")
	}
}
