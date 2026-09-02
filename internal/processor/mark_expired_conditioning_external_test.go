package processor_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// The freshnessMarker script's whole serialization argument is that its two
// mutation KINDS select the two conditionings this commit path applies — the
// §3.2 hydrated-revision default on the update, the observed-absence condition
// on the create. A test that only inspects the script's output asserts the
// premise; a test that only feeds hand-built mutations to the helpers asserts
// the conclusion. These run the SHIPPED script and hand its real output to the
// real helpers, so a script edit that stopped selecting the conditioning — a
// create where an update belongs, an explicit expectedRevision, a key the
// hydrated set does not contain — is caught at the joint.

const (
	mecEntityKey = "vtx.leaseapp.BBmarkcondHJKMNPQRS"
	mecMarkerKey = mecEntityKey + ".freshnessExpiry"
	mecRevision  = uint64(41)
)

// TestMarkExpiredUpdate_TakesTheSection32HydratedRevisionDefault: on a hydrated
// marker the script proposes an `update` naming no expectedRevision, and
// applyHydratedRevisions therefore conditions it on the revision the key was
// read at and reports it as a key WE conditioned — which is what makes a
// concurrent sibling target's conflict retry-eligible rather than a rejection
// the caller sees.
func TestMarkExpiredUpdate_TakesTheSection32HydratedRevisionDefault(t *testing.T) {
	hydrated := map[string]processor.VertexDoc{
		mecEntityKey: {Key: mecEntityKey, Class: "leaseapp", Data: map[string]any{}, Revision: 3},
		mecMarkerKey: {
			Key: mecMarkerKey, Class: "freshnessExpiry", VertexKey: mecEntityKey,
			LocalName: "freshnessExpiry", Revision: mecRevision,
			Data: map[string]any{
				"expiredAt": "2026-06-18T09:00:00Z",
				"byTarget":  map[string]any{"leaseApplicationComplete": "2026-06-18T09:00:00Z"},
			},
		},
	}
	result := runShippedMarkExpired(t, hydrated, nil, "leaseExpiry", "2026-06-18T17:00:00Z")
	if len(result.Mutations) != 1 || result.Mutations[0].Op != "update" {
		t.Fatalf("a hydrated marker must propose exactly one `update`, got %+v", result.Mutations)
	}
	if result.Mutations[0].ExpectedRevision != nil {
		t.Fatalf("the script must name no explicit expectedRevision — an explicit one is excluded from "+
			"the retry set and surfaces the race to the caller; got %d", *result.Mutations[0].ExpectedRevision)
	}

	defaulted := processor.ApplyHydratedRevisionsForTest(result.Mutations, hydrated)
	got, ok := defaulted[mecMarkerKey]
	if !ok {
		t.Fatalf("the marker update must land in the §3.2-defaulted set (keys %v) — only a condition the "+
			"commit path supplied is attributed structurally on a conflict, and only those are retried", defaulted)
	}
	if got != mecRevision {
		t.Fatalf("marker conditioned at revision %d, want the revision it was hydrated at (%d)", got, mecRevision)
	}
	if result.Mutations[0].ExpectedRevision == nil || *result.Mutations[0].ExpectedRevision != mecRevision {
		t.Fatalf("applyHydratedRevisions must stamp the mutation itself with %d, got %v",
			mecRevision, result.Mutations[0].ExpectedRevision)
	}
}

// TestMarkExpiredCreate_IsConditionedOnTheObservedAbsence: on a marker step 4
// recorded known-absent the script proposes a `create`, which
// absentConditionedCreates recognises — so a first-fire race between two targets
// resolves as the benign declared-dedup case (the loser re-hydrates and takes
// the merge branch) instead of a create-once collision nobody retries.
func TestMarkExpiredCreate_IsConditionedOnTheObservedAbsence(t *testing.T) {
	hydrated := map[string]processor.VertexDoc{
		mecEntityKey: {Key: mecEntityKey, Class: "leaseapp", Data: map[string]any{}, Revision: 3},
	}
	knownAbsent := map[string]struct{}{mecMarkerKey: {}}
	result := runShippedMarkExpired(t, hydrated, knownAbsent, "leaseApplicationComplete", "2026-06-18T09:00:00Z")
	if len(result.Mutations) != 1 || result.Mutations[0].Op != "create" {
		t.Fatalf("a known-absent marker must propose exactly one `create`, got %+v", result.Mutations)
	}

	absent := processor.AbsentConditionedCreatesForTest(result.Mutations, knownAbsent)
	if len(absent) != 1 || absent[0] != mecMarkerKey {
		t.Fatalf("the marker create must be reported absent-conditioned, got %v — an unrecognised create "+
			"collision is surfaced without a retry, and the loser never reaches the merge branch", absent)
	}
	// And it takes no §3.2 revision condition: there is no revision to condition
	// on, which is exactly why the create branch exists.
	if defaulted := processor.ApplyHydratedRevisionsForTest(result.Mutations, hydrated); len(defaulted) != 0 {
		t.Fatalf("a create must take no hydrated-revision condition, got %v", defaulted)
	}
}

// runShippedMarkExpired executes the real freshnessMarker DDL script against the
// hydrated state step 4 would build.
func runShippedMarkExpired(t *testing.T, hydrated map[string]processor.VertexDoc, knownAbsent map[string]struct{}, targetID, expiredAt string) processor.ScriptResult {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     "MEcondAAAAAAAAAAAAAA",
		Lane:          processor.LaneDefault,
		OperationType: "MarkExpired",
		Class:         "freshnessMarker",
		SubmittedAt:   expiredAt,
		Payload: json.RawMessage(`{"entityKey":"` + mecEntityKey + `","targetId":"` + targetID +
			`","expiredAt":"` + expiredAt + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{mecEntityKey},
			OptionalReads: []string{mecMarkerKey},
		},
	}
	res, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), processor.ScriptContext{
		Operation:    env,
		Hydrated:     hydrated,
		KnownAbsent:  knownAbsent,
		ScriptSource: orchestrationbase.MarkExpiredDDL().Script,
		ScriptClass:  "freshnessMarker",
	})
	if err != nil {
		t.Fatalf("run the shipped MarkExpired script: %v", err)
	}
	return res
}
