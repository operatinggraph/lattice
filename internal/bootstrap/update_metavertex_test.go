// UpdateMetaVertex expansion tests — exercise the MetaRootDDLScript Update
// branch directly via the Starlark runner.
//
// Coverage:
//   - per-field updates emit exactly the targeted aspect mutation plus the
//     .compensation mutation, leaving untouched aspects alone;
//   - multi-field updates;
//   - empty-update rejection;
//   - canonicalName is ignored (immutable);
//   - metaKey is preserved (no fresh NanoID; root key untouched);
//   - .compensation payloadTemplate captures prior values of the changed
//     fields read from hydrated state;
//   - expectedRevision happy path (applied to first present field) + conflict
//     (non-integer rejected);
//   - lens spec update reuses Create-branch validation.
package bootstrap_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
)

// ddlMetaKey is a stable meta-vertex key used across the Update tests.
const ddlMetaKey = "vtx.meta.UpdMetaVtxTest000001"

// makeUpdateCtx builds a ScriptContext for an UpdateMetaVertex op with the
// supplied payload and hydrated prior state.
func makeUpdateCtx(payloadJSON string, hydrated map[string]processor.VertexDoc) processor.ScriptContext {
	return processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "UpdMetaVtxOp00000001",
			Lane:          processor.LaneMeta,
			OperationType: "UpdateMetaVertex",
			Actor:         "vtx.identity.UpdMetaActrXzBbCdEfG",
			SubmittedAt:   "2026-05-29T10:00:00Z",
			Payload:       json.RawMessage(payloadJSON),
		},
		Hydrated:     hydrated,
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: bootstrap.MetaRootDDLScript,
		ScriptClass:  "root",
	}
}

// ddlState returns a hydrated state for a live DDL meta-vertex with the given
// prior aspect data fields, suitable as the `state` global for the script.
func ddlState() map[string]processor.VertexDoc {
	return map[string]processor.VertexDoc{
		ddlMetaKey: {
			Key:   ddlMetaKey,
			Class: "meta.ddl.vertexType",
			Data:  map[string]interface{}{},
		},
		ddlMetaKey + ".description": {
			Key: ddlMetaKey + ".description", Class: "description",
			VertexKey: ddlMetaKey, LocalName: "description",
			Data: map[string]interface{}{"text": "prior-desc"},
		},
		ddlMetaKey + ".script": {
			Key: ddlMetaKey + ".script", Class: "script",
			VertexKey: ddlMetaKey, LocalName: "script",
			Data: map[string]interface{}{"source": "prior-source"},
		},
		ddlMetaKey + ".permittedCommands": {
			Key: ddlMetaKey + ".permittedCommands", Class: "permittedCommands",
			VertexKey: ddlMetaKey, LocalName: "permittedCommands",
			Data: map[string]interface{}{"commands": []interface{}{"PriorCmd"}},
		},
		ddlMetaKey + ".inputSchema": {
			Key: ddlMetaKey + ".inputSchema", Class: "inputSchema",
			VertexKey: ddlMetaKey, LocalName: "inputSchema",
			Data: map[string]interface{}{"schema": "prior-input"},
		},
		ddlMetaKey + ".outputSchema": {
			Key: ddlMetaKey + ".outputSchema", Class: "outputSchema",
			VertexKey: ddlMetaKey, LocalName: "outputSchema",
			Data: map[string]interface{}{"schema": "prior-output"},
		},
		ddlMetaKey + ".fieldDescription": {
			Key: ddlMetaKey + ".fieldDescription", Class: "fieldDescription",
			VertexKey: ddlMetaKey, LocalName: "fieldDescription",
			Data: map[string]interface{}{"fieldDescriptions": map[string]interface{}{"title": "prior"}},
		},
		ddlMetaKey + ".examples": {
			Key: ddlMetaKey + ".examples", Class: "examples",
			VertexKey: ddlMetaKey, LocalName: "examples",
			Data: map[string]interface{}{"examples": []interface{}{map[string]interface{}{"name": "prior"}}},
		},
	}
}

// findMutation returns the mutation whose key has the given suffix, or nil.
func findMutation(muts []processor.MutationOp, suffix string) *processor.MutationOp {
	for i := range muts {
		if strings.HasSuffix(muts[i].Key, suffix) {
			return &muts[i]
		}
	}
	return nil
}

// dataField extracts document["data"][field] from a mutation.
func dataField(t *testing.T, m *processor.MutationOp, field string) interface{} {
	t.Helper()
	data, ok := m.Document["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("mutation %s document.data not a map: %+v", m.Key, m.Document)
	}
	return data[field]
}

func TestUpdateMetaVertex_PerFieldPreservesUntouchedAspects(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	cases := []struct {
		name      string
		payload   string
		suffix    string
		dataKey   string
		wantValue string
		priorKey  string // payloadTemplate field holding the prior value
		priorWant string
	}{
		{"description", `{"metaKey":"` + ddlMetaKey + `","description":"new-desc"}`,
			".description", "text", "new-desc", "description", "prior-desc"},
		{"script", `{"metaKey":"` + ddlMetaKey + `","script":"new-source"}`,
			".script", "source", "new-source", "script", "prior-source"},
		{"inputSchema", `{"metaKey":"` + ddlMetaKey + `","inputSchema":"new-input"}`,
			".inputSchema", "schema", "new-input", "inputSchema", "prior-input"},
		{"outputSchema", `{"metaKey":"` + ddlMetaKey + `","outputSchema":"new-output"}`,
			".outputSchema", "schema", "new-output", "outputSchema", "prior-output"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := runner.Run(ctx, makeUpdateCtx(tc.payload, ddlState()))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			// Exactly the targeted aspect + .compensation = 2 mutations.
			if len(res.Mutations) != 2 {
				t.Fatalf("got %d mutations, want 2 (field + compensation): %+v",
					len(res.Mutations), res.Mutations)
			}
			m := findMutation(res.Mutations, tc.suffix)
			if m == nil {
				t.Fatalf("no mutation with suffix %s", tc.suffix)
			}
			if got := dataField(t, m, tc.dataKey); got != tc.wantValue {
				t.Errorf("%s.%s = %v, want %q", tc.suffix, tc.dataKey, got, tc.wantValue)
			}
			// metaKey preserved: mutation key is rooted at ddlMetaKey.
			if !strings.HasPrefix(m.Key, ddlMetaKey+".") {
				t.Errorf("mutation key %q not rooted at %q", m.Key, ddlMetaKey)
			}
			// .compensation captures the prior value of the changed field only.
			comp := findMutation(res.Mutations, ".compensation")
			if comp == nil {
				t.Fatal("no .compensation mutation")
			}
			cdata := comp.Document["data"].(map[string]interface{})
			if cdata["inverseOperationType"] != "UpdateMetaVertex" {
				t.Errorf("inverseOperationType = %v, want UpdateMetaVertex", cdata["inverseOperationType"])
			}
			pt := cdata["payloadTemplate"].(map[string]interface{})
			if pt["metaKey"] != ddlMetaKey {
				t.Errorf("payloadTemplate.metaKey = %v, want %q", pt["metaKey"], ddlMetaKey)
			}
			if pt[tc.priorKey] != tc.priorWant {
				t.Errorf("payloadTemplate.%s = %v, want %q", tc.priorKey, pt[tc.priorKey], tc.priorWant)
			}
			// Compensation should NOT carry fields that weren't changed.
			for _, other := range []string{"description", "script", "inputSchema", "outputSchema"} {
				if other == tc.priorKey {
					continue
				}
				if _, present := pt[other]; present {
					t.Errorf("payloadTemplate unexpectedly carries unchanged field %q", other)
				}
			}
		})
	}
}

func TestUpdateMetaVertex_ListAndDictFields(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	t.Run("permittedCommands", func(t *testing.T) {
		res, err := runner.Run(ctx, makeUpdateCtx(
			`{"metaKey":"`+ddlMetaKey+`","permittedCommands":["A","B"]}`, ddlState()))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		m := findMutation(res.Mutations, ".permittedCommands")
		if m == nil {
			t.Fatal("no .permittedCommands mutation")
		}
		cmds, ok := dataField(t, m, "commands").([]interface{})
		if !ok || len(cmds) != 2 || cmds[0] != "A" || cmds[1] != "B" {
			t.Errorf("commands = %v, want [A B]", dataField(t, m, "commands"))
		}
	})

	t.Run("permittedCommands_non_string_element_rejected", func(t *testing.T) {
		_, err := runner.Run(ctx, makeUpdateCtx(
			`{"metaKey":"`+ddlMetaKey+`","permittedCommands":["A",7]}`, ddlState()))
		if err == nil || !strings.Contains(err.Error(), "each entry must be a string") {
			t.Fatalf("want 'each entry must be a string', got: %v", err)
		}
	})

	t.Run("fieldDescription", func(t *testing.T) {
		res, err := runner.Run(ctx, makeUpdateCtx(
			`{"metaKey":"`+ddlMetaKey+`","fieldDescription":{"x":"y"}}`, ddlState()))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		m := findMutation(res.Mutations, ".fieldDescription")
		if m == nil {
			t.Fatal("no .fieldDescription mutation")
		}
		fd, ok := dataField(t, m, "fieldDescriptions").(map[string]interface{})
		if !ok || fd["x"] != "y" {
			t.Errorf("fieldDescriptions = %v, want {x:y}", dataField(t, m, "fieldDescriptions"))
		}
	})

	t.Run("examples", func(t *testing.T) {
		res, err := runner.Run(ctx, makeUpdateCtx(
			`{"metaKey":"`+ddlMetaKey+`","examples":[{"name":"e1"}]}`, ddlState()))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		m := findMutation(res.Mutations, ".examples")
		if m == nil {
			t.Fatal("no .examples mutation")
		}
		ex, ok := dataField(t, m, "examples").([]interface{})
		if !ok || len(ex) != 1 {
			t.Errorf("examples = %v, want one entry", dataField(t, m, "examples"))
		}
	})
}

func TestUpdateMetaVertex_MultiField(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	res, err := runner.Run(ctx, makeUpdateCtx(
		`{"metaKey":"`+ddlMetaKey+`","description":"d2","script":"s2","examples":[{"name":"e"}]}`,
		ddlState()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 3 field mutations + 1 compensation.
	if len(res.Mutations) != 4 {
		t.Fatalf("got %d mutations, want 4: %+v", len(res.Mutations), res.Mutations)
	}
	for _, suffix := range []string{".description", ".script", ".examples", ".compensation"} {
		if findMutation(res.Mutations, suffix) == nil {
			t.Errorf("missing mutation %s", suffix)
		}
	}
	comp := findMutation(res.Mutations, ".compensation")
	pt := comp.Document["data"].(map[string]interface{})["payloadTemplate"].(map[string]interface{})
	if pt["description"] != "prior-desc" {
		t.Errorf("compensation description = %v, want prior-desc", pt["description"])
	}
	if pt["script"] != "prior-source" {
		t.Errorf("compensation script = %v, want prior-source", pt["script"])
	}
	if _, ok := pt["examples"]; !ok {
		t.Error("compensation missing examples prior value")
	}
	// Untouched fields must not appear in compensation.
	if _, ok := pt["inputSchema"]; ok {
		t.Error("compensation should not carry untouched inputSchema")
	}
}

func TestUpdateMetaVertex_EmptyUpdateRejected(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	res, err := runner.Run(ctx, makeUpdateCtx(
		`{"metaKey":"`+ddlMetaKey+`"}`, ddlState()))
	if err == nil {
		t.Fatalf("expected error for empty update, got: %+v", res)
	}
	if !strings.Contains(err.Error(), "no updatable fields provided") {
		t.Fatalf("error = %q, want 'no updatable fields provided'", err.Error())
	}
}

func TestUpdateMetaVertex_CanonicalNameIgnored(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	// canonicalName supplied alongside a real change: ignored, not mutated,
	// and does not fail.
	res, err := runner.Run(ctx, makeUpdateCtx(
		`{"metaKey":"`+ddlMetaKey+`","canonicalName":"renamed","description":"d3"}`,
		ddlState()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if findMutation(res.Mutations, ".canonicalName") != nil {
		t.Error("canonicalName must be immutable — no .canonicalName mutation expected")
	}
	// description still applied.
	if findMutation(res.Mutations, ".description") == nil {
		t.Error("description mutation missing")
	}

	// canonicalName as the ONLY field is an empty update → rejected.
	_, err = runner.Run(ctx, makeUpdateCtx(
		`{"metaKey":"`+ddlMetaKey+`","canonicalName":"renamed"}`, ddlState()))
	if err == nil || !strings.Contains(err.Error(), "no updatable fields provided") {
		t.Fatalf("canonicalName-only update: want empty-update rejection, got: %v", err)
	}
}

func TestUpdateMetaVertex_MetaKeyPreserved(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	res, err := runner.Run(ctx, makeUpdateCtx(
		`{"metaKey":"`+ddlMetaKey+`","description":"d4"}`, ddlState()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Every mutation key is rooted at the original metaKey (no new NanoID).
	for _, m := range res.Mutations {
		if !strings.HasPrefix(m.Key, ddlMetaKey+".") {
			t.Errorf("mutation key %q not rooted at original metaKey %q", m.Key, ddlMetaKey)
		}
	}
	// UpdateMetaVertex mutates aspects, not the root vertex; primaryKey names the
	// principal entity (the meta-vertex), accepted by the Processor as the
	// 3-segment root of the committed aspects.
	if res.PrimaryKey != ddlMetaKey {
		t.Errorf("response primaryKey = %q, want %q", res.PrimaryKey, ddlMetaKey)
	}
}

func TestUpdateMetaVertex_PriorValueMissingFails(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	// State with only the live vertex root, no prior .description aspect — a
	// null prior would bake an un-submittable rollback, so the forward op must
	// fail rather than capture nil into the compensation payloadTemplate.
	state := map[string]processor.VertexDoc{
		ddlMetaKey: {Key: ddlMetaKey, Class: "meta.ddl.vertexType", Data: map[string]interface{}{}},
	}
	_, err := runner.Run(ctx, makeUpdateCtx(
		`{"metaKey":"`+ddlMetaKey+`","description":"d5"}`, state))
	if err == nil || !strings.Contains(err.Error(), "prior value unavailable for compensation") {
		t.Fatalf("want 'prior value unavailable for compensation', got: %v", err)
	}
}

func TestUpdateMetaVertex_ExpectedRevision(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	t.Run("happy_applied_to_first_field", func(t *testing.T) {
		// Two fields; expectedRevision must land on description (canonical
		// order first), not on script or compensation.
		res, err := runner.Run(ctx, makeUpdateCtx(
			`{"metaKey":"`+ddlMetaKey+`","script":"s","description":"d","expectedRevision":7}`,
			ddlState()))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		desc := findMutation(res.Mutations, ".description")
		if desc.ExpectedRevision == nil || *desc.ExpectedRevision != 7 {
			t.Errorf("description ExpectedRevision = %v, want 7", desc.ExpectedRevision)
		}
		if scr := findMutation(res.Mutations, ".script"); scr.ExpectedRevision != nil {
			t.Errorf("script ExpectedRevision = %v, want nil", scr.ExpectedRevision)
		}
		if comp := findMutation(res.Mutations, ".compensation"); comp.ExpectedRevision != nil {
			t.Errorf("compensation ExpectedRevision = %v, want nil", comp.ExpectedRevision)
		}
	})

	t.Run("force_bypasses", func(t *testing.T) {
		res, err := runner.Run(ctx, makeUpdateCtx(
			`{"metaKey":"`+ddlMetaKey+`","description":"d","expectedRevision":7,"force":true}`,
			ddlState()))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if desc := findMutation(res.Mutations, ".description"); desc.ExpectedRevision != nil {
			t.Errorf("force=true: ExpectedRevision = %v, want nil", desc.ExpectedRevision)
		}
	})

	t.Run("non_integer_rejected", func(t *testing.T) {
		_, err := runner.Run(ctx, makeUpdateCtx(
			`{"metaKey":"`+ddlMetaKey+`","description":"d","expectedRevision":"oops"}`,
			ddlState()))
		if err == nil || !strings.Contains(err.Error(), "expectedRevision must be an integer") {
			t.Fatalf("want integer-validation error, got: %v", err)
		}
	})
}

func TestUpdateMetaVertex_UnknownVertexRejected(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	_, err := runner.Run(ctx, makeUpdateCtx(
		`{"metaKey":"`+ddlMetaKey+`","description":"d"}`,
		map[string]processor.VertexDoc{}))
	if err == nil || !strings.Contains(err.Error(), "UnknownMetaVertex") {
		t.Fatalf("want UnknownMetaVertex, got: %v", err)
	}
}

func TestUpdateMetaVertex_LensSpecAndDescription(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	const lensKey = "vtx.meta.UpdLensVtxTest00001"
	priorSpec := map[string]interface{}{
		"cypherRule":   "MATCH (n) RETURN n",
		"targetType":   "nats_kv",
		"targetConfig": map[string]interface{}{"bucket": "b", "key": []interface{}{"k"}},
	}
	lensState := map[string]processor.VertexDoc{
		lensKey: {Key: lensKey, Class: "meta.lens", Data: map[string]interface{}{}},
		lensKey + ".description": {
			Key: lensKey + ".description", Class: "description",
			VertexKey: lensKey, LocalName: "description",
			Data: map[string]interface{}{"text": "prior-lens-desc"},
		},
		lensKey + ".spec": {
			Key: lensKey + ".spec", Class: "lensSpec",
			VertexKey: lensKey, LocalName: "spec",
			Data: priorSpec,
		},
	}

	newSpec := `{"cypherRule":"MATCH (m) RETURN m","targetType":"postgres","targetConfig":{"table":"t","key":["id"]}}`
	payload := `{"metaKey":"` + lensKey + `","description":"new-lens-desc","spec":` +
		mustJSONString(t, newSpec) + `}`

	res, err := runner.Run(ctx, makeUpdateCtx(payload, lensState))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// .spec aspect stores the decoded dict verbatim (cypherRule key, not source).
	specMut := findMutation(res.Mutations, ".spec")
	if specMut == nil {
		t.Fatal("no .spec mutation")
	}
	sdata := specMut.Document["data"].(map[string]interface{})
	if sdata["cypherRule"] != "MATCH (m) RETURN m" {
		t.Errorf(".spec data cypherRule = %v", sdata["cypherRule"])
	}
	if _, hasSource := sdata["source"]; hasSource {
		t.Error(".spec data must not wrap in a 'source' field")
	}

	// description applied.
	descMut := findMutation(res.Mutations, ".description")
	if descMut == nil || dataField(t, descMut, "text") != "new-lens-desc" {
		t.Errorf("lens description not applied: %+v", descMut)
	}

	// compensation: spec prior value is a re-encoded JSON string of priorSpec.
	comp := findMutation(res.Mutations, ".compensation")
	pt := comp.Document["data"].(map[string]interface{})["payloadTemplate"].(map[string]interface{})
	specStr, ok := pt["spec"].(string)
	if !ok {
		t.Fatalf("compensation spec not a string: %T %v", pt["spec"], pt["spec"])
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(specStr), &decoded); err != nil {
		t.Fatalf("compensation spec not valid JSON: %v", err)
	}
	if decoded["cypherRule"] != "MATCH (n) RETURN n" {
		t.Errorf("compensation prior spec cypherRule = %v, want prior", decoded["cypherRule"])
	}
	if pt["description"] != "prior-lens-desc" {
		t.Errorf("compensation description = %v, want prior-lens-desc", pt["description"])
	}
}

func TestUpdateMetaVertex_LensSpecMissingCypherRuleRejected(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	const lensKey = "vtx.meta.UpdLensBad00000001"
	lensState := map[string]processor.VertexDoc{
		lensKey: {Key: lensKey, Class: "meta.lens", Data: map[string]interface{}{}},
	}
	badSpec := `{"targetType":"postgres","targetConfig":{"table":"t","key":["id"]}}`
	payload := `{"metaKey":"` + lensKey + `","spec":` + mustJSONString(t, badSpec) + `}`

	_, err := runner.Run(ctx, makeUpdateCtx(payload, lensState))
	if err == nil || !strings.Contains(err.Error(), "spec.cypherRule: required") {
		t.Fatalf("want spec.cypherRule validation error, got: %v", err)
	}
}

// mustJSONString JSON-encodes s as a quoted JSON string literal for embedding
// inside a larger JSON payload.
func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal string: %v", err)
	}
	return string(b)
}
