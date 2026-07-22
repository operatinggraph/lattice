// Script-level unit tests for the loomLifecycle DDL's StartLoomPattern branch —
// specifically the optional stable instanceId seam (Contract #10 §10.3) that
// makes Weaver's triggerLoom re-dispatch collapse on Loom's existing instance.
package orchestrationbase_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

const llReqID = "BBloomreqHJKMNPQRSTU"

func loomLifecycleScript(t *testing.T) string {
	t.Helper()
	for _, d := range orchestrationbase.DDLs() {
		if d.CanonicalName == "loomLifecycle" {
			return d.Script
		}
	}
	t.Fatal("loomLifecycle DDL not found")
	return ""
}

func runStartLoomPattern(t *testing.T, payload map[string]any) processor.ScriptResult {
	t.Helper()
	pb, _ := json.Marshal(payload)
	sc := processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     llReqID,
			Lane:          processor.LaneDefault,
			OperationType: "StartLoomPattern",
			Actor:         "vtx.identity.BBweaverHJKMNPQRSTUV",
			SubmittedAt:   "2026-06-04T00:00:00Z",
			Payload:       pb,
		},
		ScriptSource: loomLifecycleScript(t),
		ScriptClass:  "loomLifecycle",
	}
	res, err := processor.NewStarlarkRunner(0, 0).Run(context.Background(), sc)
	if err != nil {
		t.Fatalf("StartLoomPattern: unexpected error: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].Class != "loom.patternStarted" {
		t.Fatalf("expected one loom.patternStarted event, got %+v", res.Events)
	}
	return res
}

// TestStartLoomPattern_SuppliedInstanceId_UsedVerbatim: a caller-supplied stable
// instanceId (Weaver's claimId-seeded id, §10.3) is emitted verbatim on
// loom.patternStarted, so a re-dispatch collapses on Loom's existing instance.
func TestStartLoomPattern_SuppliedInstanceId_UsedVerbatim(t *testing.T) {
	const stableID = "BBstableInstHJKMNPQR"
	res := runStartLoomPattern(t, map[string]any{
		"patternRef": "vtx.meta.BBonboardHJKMNPQRST",
		"subjectKey": "vtx.identity.BBsubjectHJKMNPQRS",
		"instanceId": stableID,
	})
	if got := res.Events[0].Data["instanceId"]; got != stableID {
		t.Fatalf("event instanceId = %v, want the supplied stable id %q", got, stableID)
	}
}

// TestStartLoomPattern_NoInstanceId_DefaultsToRequestId: absent the optional
// field, the instanceId defaults to the op's requestId (the prior behavior —
// clients/fixtures that do not dedup across episodes are unaffected).
func TestStartLoomPattern_NoInstanceId_DefaultsToRequestId(t *testing.T) {
	res := runStartLoomPattern(t, map[string]any{
		"patternRef": "vtx.meta.BBonboardHJKMNPQRST",
		"subjectKey": "vtx.identity.BBsubjectHJKMNPQRS",
	})
	if got := res.Events[0].Data["instanceId"]; got != llReqID {
		t.Fatalf("event instanceId = %v, want the op requestId %q", got, llReqID)
	}
}
