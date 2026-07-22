// UpgradePackage kernel-script tests — exercise the UpgradePackageDDLScript
// guardrails + mixed-op emission directly via the Starlark runner (Contract #8
// §8.6).
//
// Coverage:
//   - a mixed create/update/tombstone batch passes through verbatim and the
//     PackageUpgraded event carries the per-op counts;
//   - a same-version (fromVersion == toVersion) update-only batch is legal;
//   - an unknown op is rejected;
//   - an illegal key shape is rejected;
//   - an underscore-prefixed aspect is rejected;
//   - an empty mutation list is rejected;
//   - missing name/fromVersion/toVersion/mutations are each rejected.
package bootstrap_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
)

// makeUpgradeCtx builds a ScriptContext for an UpgradePackage op with the
// supplied payload. The upgrade script reads only op.payload (empty hydrated
// state), mirroring install/uninstall.
func makeUpgradeCtx(payloadJSON string) processor.ScriptContext {
	return processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     "UpgradePkgOp00000001",
			Lane:          processor.LaneMeta,
			OperationType: "UpgradePackage",
			Actor:         "vtx.identity.UpgradeActorXzBbCdEf",
			SubmittedAt:   "2026-06-28T10:00:00Z",
			Payload:       json.RawMessage(payloadJSON),
		},
		Hydrated:     map[string]processor.VertexDoc{},
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: bootstrap.UpgradePackageDDLScript,
		ScriptClass:  "meta.ddl.vertexType",
	}
}

func TestUpgradePackage_MixedBatchPassesThroughWithCounts(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	const (
		updKey  = "vtx.meta.UpgLensUpdate000001"
		crtKey  = "vtx.meta.UpgDDLCreate0000001"
		tombKey = "vtx.permission.UpgPermDrop00001"
	)
	payload := `{
		"name": "demo-domain",
		"fromVersion": "0.1.0",
		"toVersion": "0.2.0",
		"mutations": [
			{"op": "update",    "key": "` + updKey + `",  "document": {"class": "meta.lens", "isDeleted": false, "data": {}}},
			{"op": "create",    "key": "` + crtKey + `",  "document": {"class": "meta.ddl.vertexType", "isDeleted": false, "data": {}}},
			{"op": "tombstone", "key": "` + tombKey + `", "document": {"isDeleted": true, "data": {}}}
		]
	}`

	res, err := runner.Run(ctx, makeUpgradeCtx(payload))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(res.Mutations) != 3 {
		t.Fatalf("got %d mutations, want 3: %+v", len(res.Mutations), res.Mutations)
	}
	got := mutationKeys(res.Mutations)
	for key, wantOp := range map[string]string{updKey: "update", crtKey: "create", tombKey: "tombstone"} {
		m, ok := got[key]
		if !ok {
			t.Fatalf("missing mutation for %q", key)
		}
		if m.Op != wantOp {
			t.Errorf("mutation %q op = %q, want %q", key, m.Op, wantOp)
		}
	}

	// PackageUpgraded event with per-op counts.
	if len(res.Events) != 1 || res.Events[0].Class != "package.upgraded" {
		t.Fatalf("events = %+v, want one package.upgraded", res.Events)
	}
	ev := res.Events[0].Data
	if ev["name"] != "demo-domain" {
		t.Errorf("event name = %v, want demo-domain", ev["name"])
	}
	if ev["fromVersion"] != "0.1.0" || ev["toVersion"] != "0.2.0" {
		t.Errorf("event versions = %v→%v, want 0.1.0→0.2.0", ev["fromVersion"], ev["toVersion"])
	}
	// Starlark integers surface as int64 through the runner's conversion.
	assertCount(t, ev, "createdCount", 1)
	assertCount(t, ev, "updatedCount", 1)
	assertCount(t, ev, "tombstonedCount", 1)
}

// assertCount compares an event count field tolerantly across the numeric
// types JSON/Starlark conversion may yield (int64 / float64 / int).
func assertCount(t *testing.T, data map[string]any, field string, want int64) {
	t.Helper()
	var got int64
	switch v := data[field].(type) {
	case int64:
		got = v
	case int:
		got = int64(v)
	case float64:
		got = int64(v)
	default:
		t.Fatalf("%s = %v (%T), want a numeric %d", field, data[field], data[field], want)
	}
	if got != want {
		t.Errorf("%s = %d, want %d", field, got, want)
	}
}

func TestUpgradePackage_SameVersionUpdateOnlyIsLegal(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	payload := `{
		"name": "demo-domain",
		"fromVersion": "0.2.0",
		"toVersion": "0.2.0",
		"mutations": [
			{"op": "update", "key": "vtx.meta.UpgSameVerLens00001", "document": {"class": "meta.lens", "isDeleted": false, "data": {}}}
		]
	}`
	res, err := runner.Run(ctx, makeUpgradeCtx(payload))
	if err != nil {
		t.Fatalf("Run (same-version re-apply): %v", err)
	}
	if len(res.Mutations) != 1 || res.Mutations[0].Op != "update" {
		t.Fatalf("want one update mutation, got %+v", res.Mutations)
	}
	assertCount(t, res.Events[0].Data, "updatedCount", 1)
	assertCount(t, res.Events[0].Data, "createdCount", 0)
}

func TestUpgradePackage_GuardrailsReject(t *testing.T) {
	runner := processor.NewStarlarkRunner(0, 0)
	ctx := context.Background()

	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{
			name: "unknown op",
			payload: `{"name":"d","fromVersion":"1","toVersion":"2","mutations":[
				{"op":"delete","key":"vtx.meta.UpgBadOpKey00000001","document":{"data":{}}}]}`,
			wantErr: "create/update/tombstone",
		},
		{
			name: "illegal key shape",
			payload: `{"name":"d","fromVersion":"1","toVersion":"2","mutations":[
				{"op":"create","key":"not-a-key","document":{"data":{}}}]}`,
			wantErr: "illegal key shape",
		},
		{
			name: "underscore aspect",
			payload: `{"name":"d","fromVersion":"1","toVersion":"2","mutations":[
				{"op":"update","key":"vtx.meta.UpgUnderscore00001._secret","document":{"data":{}}}]}`,
			wantErr: "underscore-prefixed aspect not allowed",
		},
		{
			name:    "empty mutations",
			payload: `{"name":"d","fromVersion":"1","toVersion":"2","mutations":[]}`,
			wantErr: "upgrade produced no mutations",
		},
		{
			name:    "missing name",
			payload: `{"fromVersion":"1","toVersion":"2","mutations":[]}`,
			wantErr: "name: required",
		},
		{
			name:    "missing fromVersion",
			payload: `{"name":"d","toVersion":"2","mutations":[]}`,
			wantErr: "fromVersion: required",
		},
		{
			name:    "missing toVersion",
			payload: `{"name":"d","fromVersion":"1","mutations":[]}`,
			wantErr: "toVersion: required",
		},
		{
			name:    "missing mutations",
			payload: `{"name":"d","fromVersion":"1","toVersion":"2"}`,
			wantErr: "mutations: required",
		},
		{
			name: "mutation missing document",
			payload: `{"name":"d","fromVersion":"1","toVersion":"2","mutations":[
				{"op":"create","key":"vtx.meta.UpgNoDoc0000000001"}]}`,
			wantErr: "requires a document dict",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runner.Run(ctx, makeUpgradeCtx(tc.payload))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}
