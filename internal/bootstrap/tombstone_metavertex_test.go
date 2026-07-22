// TombstoneMetaVertex cascade tests — exercise the MetaRootDDLScript
// Tombstone branch directly via the Starlark runner.
//
// Coverage:
//   - DDL classes cascade make_tombstone to the root + all 9 aspect keys
//     (including .compensation), with no leftover make_update;
//   - meta.lens cascades to the root + the union of DDL-created and
//     primordial lens aspect keys;
//   - expectedRevision lands on the root tombstone only (mutations[0]);
//   - force bypasses the revision assertion; non-integer rejected;
//   - the live-vertex guard rejects absent/dead vertices (UnknownMetaVertex);
//   - the MetaVertexTombstoned event is emitted.
package bootstrap_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
)

// makeTombstoneCtx builds a ScriptContext for a TombstoneMetaVertex op with
// the supplied payload and hydrated prior state.
func makeTombstoneCtx(payloadJSON string, hydrated map[string]processor.VertexDoc) processor.ScriptContext {
	return processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "TombMetaVtxOp0000001",
			Lane:          processor.LaneMeta,
			OperationType: "TombstoneMetaVertex",
			Actor:         "vtx.identity.TombMetaActrXzBbCdEf",
			SubmittedAt:   "2026-05-29T10:00:00Z",
			Payload:       json.RawMessage(payloadJSON),
		},
		Hydrated:     hydrated,
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: bootstrap.MetaRootDDLScript,
		ScriptClass:  "root",
	}
}

// liveDDLRoot returns hydrated state for a live DDL meta-vertex root.
func liveDDLRoot(key string) map[string]processor.VertexDoc {
	return map[string]processor.VertexDoc{
		key: {Key: key, Class: "meta.ddl.vertexType", Data: map[string]interface{}{}},
	}
}

// mutationKeys returns the set of mutation keys for assertions.
func mutationKeys(muts []processor.MutationOp) map[string]processor.MutationOp {
	m := map[string]processor.MutationOp{}
	for _, mu := range muts {
		m[mu.Key] = mu
	}
	return m
}

func TestTombstoneMetaVertex_DDLCascadesAllAspects(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()
	const key = "vtx.meta.TombDDLTest00000001"

	res, err := runner.Run(ctx, makeTombstoneCtx(
		`{"metaKey":"`+key+`"}`, liveDDLRoot(key)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{
		key,
		key + ".canonicalName",
		key + ".permittedCommands",
		key + ".description",
		key + ".script",
		key + ".inputSchema",
		key + ".outputSchema",
		key + ".fieldDescription",
		key + ".examples",
		key + ".compensation",
	}
	if len(res.Mutations) != len(want) {
		t.Fatalf("got %d mutations, want %d: %+v", len(res.Mutations), len(want), res.Mutations)
	}
	got := mutationKeys(res.Mutations)
	for _, k := range want {
		m, ok := got[k]
		if !ok {
			t.Fatalf("missing tombstone mutation for %q", k)
		}
		if m.Op != "tombstone" {
			t.Errorf("mutation %q op = %q, want tombstone", k, m.Op)
		}
	}
	// No make_update leftover (the old irreversible-note path is gone).
	for _, m := range res.Mutations {
		if m.Op == "update" {
			t.Errorf("unexpected update mutation %q; tombstone must only emit tombstones", m.Key)
		}
	}
	// MetaVertexTombstoned event emitted with the metaKey.
	if len(res.Events) != 1 || res.Events[0].Class != "meta.vertexTombstoned" {
		t.Fatalf("events = %+v, want one MetaVertexTombstoned", res.Events)
	}
	if res.Events[0].Data["metaKey"] != key {
		t.Errorf("event metaKey = %v, want %q", res.Events[0].Data["metaKey"], key)
	}
}

func TestTombstoneMetaVertex_LensCascadesLensAspects(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()
	const key = "vtx.meta.TombLensTest0000001"

	state := map[string]processor.VertexDoc{
		key: {Key: key, Class: "meta.lens", Data: map[string]interface{}{}},
	}
	res, err := runner.Run(ctx, makeTombstoneCtx(`{"metaKey":"`+key+`"}`, state))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{
		key,
		key + ".canonicalName",
		key + ".description",
		key + ".spec",
		key + ".compensation",
		key + ".targetBucket",
		key + ".cypherRule",
		key + ".outputSchema",
	}
	if len(res.Mutations) != len(want) {
		t.Fatalf("got %d mutations, want %d: %+v", len(res.Mutations), len(want), res.Mutations)
	}
	got := mutationKeys(res.Mutations)
	for _, k := range want {
		if m, ok := got[k]; !ok || m.Op != "tombstone" {
			t.Errorf("missing/incorrect tombstone for %q: ok=%v %+v", k, ok, m)
		}
	}
	// Lens must NOT tombstone DDL-only aspect keys.
	for _, k := range []string{key + ".script", key + ".permittedCommands", key + ".inputSchema"} {
		if _, ok := got[k]; ok {
			t.Errorf("lens cascade unexpectedly tombstoned DDL-only aspect %q", k)
		}
	}
}

func TestTombstoneMetaVertex_ExpectedRevisionOnRootOnly(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()
	const key = "vtx.meta.TombRevTest00000001"

	t.Run("applied_to_root", func(t *testing.T) {
		res, err := runner.Run(ctx, makeTombstoneCtx(
			`{"metaKey":"`+key+`","expectedRevision":11}`, liveDDLRoot(key)))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		// mutations[0] is the root tombstone and carries the revision.
		if res.Mutations[0].Key != key {
			t.Fatalf("mutations[0] key = %q, want root %q", res.Mutations[0].Key, key)
		}
		if res.Mutations[0].ExpectedRevision == nil || *res.Mutations[0].ExpectedRevision != 11 {
			t.Errorf("root ExpectedRevision = %v, want 11", res.Mutations[0].ExpectedRevision)
		}
		// Every aspect tombstone is unconditional.
		for _, m := range res.Mutations[1:] {
			if m.ExpectedRevision != nil {
				t.Errorf("aspect %q carries ExpectedRevision %v, want nil", m.Key, m.ExpectedRevision)
			}
		}
	})

	t.Run("force_bypasses", func(t *testing.T) {
		res, err := runner.Run(ctx, makeTombstoneCtx(
			`{"metaKey":"`+key+`","expectedRevision":11,"force":true}`, liveDDLRoot(key)))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Mutations[0].ExpectedRevision != nil {
			t.Errorf("force=true: root ExpectedRevision = %v, want nil", res.Mutations[0].ExpectedRevision)
		}
	})

	t.Run("non_integer_rejected", func(t *testing.T) {
		_, err := runner.Run(ctx, makeTombstoneCtx(
			`{"metaKey":"`+key+`","expectedRevision":"oops"}`, liveDDLRoot(key)))
		if err == nil || !strings.Contains(err.Error(), "expectedRevision must be an integer") {
			t.Fatalf("want integer-validation error, got: %v", err)
		}
	})
}

func TestTombstoneMetaVertex_UnknownVertexRejected(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()
	const key = "vtx.meta.TombMissingTest0001"

	t.Run("absent", func(t *testing.T) {
		_, err := runner.Run(ctx, makeTombstoneCtx(
			`{"metaKey":"`+key+`"}`, map[string]processor.VertexDoc{}))
		if err == nil || !strings.Contains(err.Error(), "UnknownMetaVertex") {
			t.Fatalf("want UnknownMetaVertex, got: %v", err)
		}
	})

	t.Run("already_dead", func(t *testing.T) {
		state := map[string]processor.VertexDoc{
			key: {Key: key, Class: "meta.ddl.vertexType", IsDeleted: true,
				Data: map[string]interface{}{}},
		}
		_, err := runner.Run(ctx, makeTombstoneCtx(`{"metaKey":"`+key+`"}`, state))
		if err == nil || !strings.Contains(err.Error(), "UnknownMetaVertex") {
			t.Fatalf("want UnknownMetaVertex for dead vertex, got: %v", err)
		}
	})
}
