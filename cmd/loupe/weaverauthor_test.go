package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

func TestDraftTargetBodyFieldMapping(t *testing.T) {
	t.Parallel()
	in := pkgmgr.WeaverTargetArtifactContent{
		TargetID: "leaseComplete",
		LensRef:  "leaseViolations",
		Gaps: map[string]pkgmgr.GapActionArtifact{
			"missing_x": {
				Action: "directOp", Operation: "SignLease", Subject: "row.entityKey",
				Params: map[string]string{"k": "v"}, Reads: []string{"row.a"},
				IssueCode: "X", IssueSeverity: "warn",
			},
		},
	}
	body := draftTargetBody(in)
	if body.TargetID != "leaseComplete" || body.LensRef != "leaseViolations" {
		t.Fatalf("body = %+v, want targetId/lensRef carried through", body)
	}
	g, ok := body.Gaps["missing_x"]
	if !ok {
		t.Fatalf("body.Gaps = %+v, missing missing_x", body.Gaps)
	}
	if g.Action != "directOp" || g.Operation != "SignLease" || g.Subject != "row.entityKey" ||
		g.IssueCode != "X" || g.IssueSeverity != "warn" {
		t.Errorf("gap action fields not carried through: %+v", g.weaverActionContract)
	}
	if !reflect.DeepEqual(g.Params, map[string]string{"k": "v"}) {
		t.Errorf("params = %v, want {k:v}", g.Params)
	}
	if !reflect.DeepEqual(g.Reads, []string{"row.a"}) {
		t.Errorf("reads = %v, want [row.a]", g.Reads)
	}
	// The restricted artifact carries no candidates/goal/actions catalog —
	// dispatchKind must still resolve via the explicit action alone.
	if g.dispatchKind() != "action" {
		t.Errorf("dispatchKind = %q, want action", g.dispatchKind())
	}
}

func TestDraftTargetBodyEmptyGapsNeverNil(t *testing.T) {
	t.Parallel()
	body := draftTargetBody(pkgmgr.WeaverTargetArtifactContent{TargetID: "t"})
	if body.Gaps == nil {
		t.Error("Gaps must be a non-nil empty map, not nil — computeLaneChecks ranges over it")
	}
}

func TestContainsTargetBinarySearchOnSortedInput(t *testing.T) {
	t.Parallel()
	sorted := []string{"__draft", "targetA", "targetB"}
	if !containsTarget(sorted, "__draft") {
		t.Error("containsTarget missed the first element")
	}
	if !containsTarget(sorted, "targetB") {
		t.Error("containsTarget missed the last element")
	}
	if containsTarget(sorted, "targetZ") {
		t.Error("containsTarget false-positived on an absent id")
	}
	if containsTarget(nil, "__draft") {
		t.Error("containsTarget on a nil slice must report false, not panic")
	}
}

func TestNonNilStrings(t *testing.T) {
	t.Parallel()
	if got := nonNilStrings(nil); got == nil || len(got) != 0 {
		t.Errorf("nonNilStrings(nil) = %#v, want empty non-nil slice (JSON [] not null)", got)
	}
	if got := nonNilStrings([]string{"a"}); len(got) != 1 || got[0] != "a" {
		t.Errorf("nonNilStrings([a]) = %v", got)
	}
}

// TestWeaverAuthorCheckRequestDecodesPkgmgrTypes pins the wire shape: the
// request body's target/lens fields decode directly as the pkgmgr artifact
// content types, with no Loupe-side re-mapping of the JSON tags.
func TestWeaverAuthorCheckRequestDecodesPkgmgrTypes(t *testing.T) {
	t.Parallel()
	raw := `{
		"target": {"targetId":"t1","lensRef":"l1","gaps":{"missing_x":{"action":"directOp","operation":"Foo"}}},
		"lens": {"canonicalName":"l1","adapter":"nats-kv","bucket":"weaver-targets","spec":"MATCH (e) RETURN e"}
	}`
	var req weaverAuthorCheckRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Target.TargetID != "t1" || req.Target.LensRef != "l1" {
		t.Errorf("target = %+v", req.Target)
	}
	if req.Target.Gaps["missing_x"].Operation != "Foo" {
		t.Errorf("gap = %+v", req.Target.Gaps["missing_x"])
	}
	if req.Lens.CanonicalName != "l1" || req.Lens.Bucket != "weaver-targets" {
		t.Errorf("lens = %+v", req.Lens)
	}
}
