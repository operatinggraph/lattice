package main

import (
	"reflect"
	"sort"
	"testing"
)

// F25.2 Verify: the same honesty rule recurs here as F25.1 — a check's
// Evidence label states exactly how much the finding is worth (static /
// observed / advisory), never more.

func TestRankActionsByCostThenRef(t *testing.T) {
	actions := []weaverAction{
		{Ref: "b", Cost: 1},
		{Ref: "a", Cost: 1},
		{Ref: "z", Cost: 0},
	}
	rankActionsByCostThenRef(actions)
	got := []string{actions[0].Ref, actions[1].Ref, actions[2].Ref}
	want := []string{"z", "a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rank order = %v, want %v (cost ascending, ref lexicographic on ties)", got, want)
	}
}

func TestVertexTypeOf(t *testing.T) {
	if vt, ok := vertexTypeOf("vtx.identity.aaaaaaaaaaaaaaaaaaaa"); !ok || vt != "identity" {
		t.Errorf("vertexTypeOf(vertex) = %q, %v, want identity, true", vt, ok)
	}
	if _, ok := vertexTypeOf("vtx.meta.aaaaaaaaaaaaaaaaaaaa"); ok {
		t.Error("a meta vertex must not read as a plain vertex type")
	}
	if _, ok := vertexTypeOf("not-a-key"); ok {
		t.Error("a malformed key must not resolve a type")
	}
}

func TestComputeTargetChecksSurfacesFindings(t *testing.T) {
	body := parseLeaseTarget(t)
	// missing_bgcheck's triggerLoom binds row.applicant, resolved against a
	// pattern declaring subjectType "identity"; a sampled row disagrees.
	patterns := map[string]string{"backgroundCheck": "vtx.meta.pAAAAAAAAAAAAAAAAAAA"}
	patternSubject := map[string]string{"vtx.meta.pAAAAAAAAAAAAAAAAAAA": "identity"}

	docs := map[string]map[string]any{
		"aaaaaaaaaaaaaaaaaaaa": {
			"applicant":       "vtx.listing.bbbbbbbbbbbbbbbbbbbb", // wrong type: identity wanted
			"missing_bgcheck": true,
			// missing_signature and missing_notify never observed at all.
		},
	}
	scan := scanWeaverRows(docs, []string{"aaaaaaaaaaaaaaaaaaaa"}, false)
	state := weaverStateKeys{}
	summary := &weaverTargetSummary{TargetID: "leaseComplete", State: "active",
		Gaps: []string{"missing_bgcheck", "missing_signature", "missing_notify"}}

	d := buildTargetDetail("leaseComplete", body, "vtx.meta.tAAAAAAAAAAAAAAAAAAA", "leaseViolations",
		summary, scan, state, nil, patterns, patternSubject, nil)

	byCode := map[string]int{}
	for _, c := range d.Checks {
		byCode[c.Code]++
	}
	if byCode["SubjectTypeMismatch"] != 1 {
		t.Errorf("checks = %+v, want exactly one SubjectTypeMismatch", d.Checks)
	}
	if byCode["ColumnNeverObserved"] != 2 {
		t.Errorf("checks = %+v, want two ColumnNeverObserved (signature, notify)", d.Checks)
	}
	if byCode["UnboundBinding"] == 0 {
		t.Errorf("checks = %+v, want at least one UnboundBinding (assignee/target never observed)", d.Checks)
	}
}

func TestComputeTargetChecksPatternNotInstalled(t *testing.T) {
	body := parseLeaseTarget(t)
	scan := scanWeaverRows(nil, nil, false)
	summary := &weaverTargetSummary{TargetID: "leaseComplete", State: "active",
		Gaps: []string{"missing_bgcheck", "missing_signature", "missing_notify"}}
	// No patterns map entry at all — backgroundCheck resolves to nothing.
	d := buildTargetDetail("leaseComplete", body, "vtx.meta.tAAAAAAAAAAAAAAAAAAA", "leaseViolations",
		summary, scan, weaverStateKeys{}, nil, nil, nil, nil)
	found := false
	for _, c := range d.Checks {
		if c.Code == "PatternNotInstalled" {
			found = true
		}
	}
	if !found {
		t.Errorf("checks = %+v, want PatternNotInstalled for the unresolved backgroundCheck pattern", d.Checks)
	}
}

func TestComputeTargetChecksRejectedIssueAttribution(t *testing.T) {
	hbs := []weaverHeartbeat{{
		Instance: "i1",
		Issues: []weaverIssue{
			{Code: "TargetRejected", Message: "meta.weaverTarget vtx.meta.tAAAAAAAAAAAAAAAAAAA rejected: targetId already registered"},
			// A different vertex's rejection must not attribute here.
			{Code: "TargetRejected", Message: "meta.weaverTarget vtx.meta.qBBBBBBBBBBBBBBBBBBB rejected: bad mode"},
		},
	}}
	d := weaverTargetDetail{MetaKey: "vtx.meta.tAAAAAAAAAAAAAAAAAAA"}
	checks := computeTargetChecks(d, weaverRowScan{}, nil, hbs)
	if len(checks) != 1 || checks[0].Code != "TargetRejected" || checks[0].Tier != "v2" {
		t.Fatalf("checks = %+v, want exactly one v2 TargetRejected naming this vertex", checks)
	}
}

func TestComputeLaneChecksReservedParamAndGapBindsNothing(t *testing.T) {
	body := &weaverTargetBody{
		TargetID: "bad target id", // fails the single-token pattern
		Gaps: map[string]weaverGapAction{
			"missing_x": {
				weaverActionContract: weaverActionContract{
					Action: "directOp", Operation: "Foo",
					Params: map[string]string{"expectedRevision": "row.rev"},
				},
			},
			"missing_y": {}, // dispatchKind "none"
		},
	}
	checks := computeLaneChecks(body)
	byCode := map[string]int{}
	for _, c := range checks {
		byCode[c.Code]++
	}
	for _, want := range []string{"InvalidTargetID", "ReservedParamCollision", "GapBindsNothing"} {
		if byCode[want] == 0 {
			t.Errorf("checks = %+v, missing %s", checks, want)
		}
	}
}

func TestCheckGoalColumns(t *testing.T) {
	goal := []byte(`{"present":"subject.signature.data.signedAt"}`)
	cases := []struct {
		name string
		g    weaverGapAction
		want string // code expected to appear
	}{
		{
			name: "goalColumns without goal",
			g:    weaverGapAction{GoalColumns: map[string]string{"signedAt": "subject.signature.data.signedAt"}},
			want: "GoalColumnsWithoutGoal",
		},
		{
			name: "root-shaped entry is redundant",
			g: weaverGapAction{Goal: goal,
				GoalColumns: map[string]string{"signedAt": "subject.data.signedAt"}},
			want: "GoalColumnNotAspectQualified",
		},
		{
			name: "unreferenced aspect path",
			g: weaverGapAction{Goal: goal,
				GoalColumns: map[string]string{"other": "subject.other.data.field"}},
			want: "GoalColumnUnreferenced",
		},
		{
			name: "clean aspect-qualified + referenced",
			g: weaverGapAction{Goal: goal,
				GoalColumns: map[string]string{"signedAt": "subject.signature.data.signedAt"}},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			checks := checkGoalColumns("missing_x", c.g)
			if c.want == "" {
				if len(checks) != 0 {
					t.Errorf("checks = %+v, want none", checks)
				}
				return
			}
			found := false
			for _, chk := range checks {
				if chk.Code == c.want {
					found = true
				}
			}
			if !found {
				t.Errorf("checks = %+v, want %s", checks, c.want)
			}
		})
	}
}

func TestCheckGoalColumnsAliasedPaths(t *testing.T) {
	goal := []byte(`{"allOf":[{"present":"subject.signature.data.signedAt"},{"present":"subject.other.data.x"}]}`)
	g := weaverGapAction{Goal: goal, GoalColumns: map[string]string{
		"a": "subject.signature.data.signedAt",
		"b": "subject.signature.data.signedAt", // same path, different column
	}}
	checks := checkGoalColumns("missing_x", g)
	found := false
	for _, c := range checks {
		if c.Code == "GoalColumnAliased" {
			found = true
		}
	}
	if !found {
		t.Errorf("checks = %+v, want GoalColumnAliased", checks)
	}
}

func TestBuildOpEffectsIndex(t *testing.T) {
	envelopes := map[string]string{
		"vtx.meta.oAAAAAAAAAAAAAAAAAAA":         `{"data":{"operationType":"SignLease"}}`,
		"vtx.meta.oAAAAAAAAAAAAAAAAAAA.effects": `{"data":{"guards":[{"present":"subject.signature.data.signedAt"}]}}`,
		// An op with an operationType but no effects aspect must not appear.
		"vtx.meta.oBBBBBBBBBBBBBBBBBBB": `{"data":{"operationType":"Notify"}}`,
		// A malformed effects body is dropped defensively, never partially indexed.
		"vtx.meta.oCCCCCCCCCCCCCCCCCCC":         `{"data":{"operationType":"Broken"}}`,
		"vtx.meta.oCCCCCCCCCCCCCCCCCCC.effects": `{"data":{"guards":[{"anyOf":[]}]}}`,
	}
	get := func(k string) ([]byte, bool) {
		v, ok := envelopes[k]
		return []byte(v), ok
	}
	keys := make([]string, 0, len(envelopes))
	for k := range envelopes {
		keys = append(keys, k)
	}
	idx := buildOpEffectsIndex(keys, get)
	if len(idx["SignLease"]) != 1 || idx["SignLease"][0].Field != "signedAt" {
		t.Errorf("SignLease effects = %v, want one signedAt leaf", idx["SignLease"])
	}
	if _, ok := idx["Notify"]; ok {
		t.Error("Notify has no effects aspect and must not appear")
	}
	if _, ok := idx["Broken"]; ok {
		t.Error("a malformed effects body must not be indexed")
	}
}

func TestComputeInterferenceAndOpCoverage(t *testing.T) {
	bodies := map[string]*weaverTargetBody{
		"targetA": {TargetID: "targetA", Gaps: map[string]weaverGapAction{
			"missing_x": {weaverActionContract: weaverActionContract{Action: "directOp", Operation: "SignLease"}},
		}},
		"targetB": {TargetID: "targetB", Gaps: map[string]weaverGapAction{
			"missing_y": {weaverActionContract: weaverActionContract{Action: "assignTask", Operation: "SignLease"}},
		}},
		"targetC": {TargetID: "targetC", Gaps: map[string]weaverGapAction{
			"missing_z": {weaverActionContract: weaverActionContract{Action: "directOp", Operation: "Unanalyzed"}},
		}},
	}
	opPaths := buildOpEffectsIndex([]string{
		"vtx.meta.oAAAAAAAAAAAAAAAAAAA", "vtx.meta.oAAAAAAAAAAAAAAAAAAA.effects",
	}, func(k string) ([]byte, bool) {
		m := map[string]string{
			"vtx.meta.oAAAAAAAAAAAAAAAAAAA":         `{"data":{"operationType":"SignLease"}}`,
			"vtx.meta.oAAAAAAAAAAAAAAAAAAA.effects": `{"data":{"guards":[{"present":"subject.signature.data.signedAt"}]}}`,
		}
		v, ok := m[k]
		return []byte(v), ok
	})

	interference := computeInterference(bodies, opPaths)
	if len(interference) != 1 {
		t.Fatalf("interference = %+v, want one shared path", interference)
	}
	targets := append([]string(nil), interference[0].Targets...)
	sort.Strings(targets)
	if !reflect.DeepEqual(targets, []string{"targetA", "targetB"}) {
		t.Errorf("interference targets = %v, want [targetA targetB]", targets)
	}

	cov := computeOpCoverage(bodies, opPaths)
	if cov.ReferencedOps != 2 || cov.DeclaredOps != 1 {
		t.Errorf("coverage = %+v, want 2 referenced / 1 declared", cov)
	}
	if len(cov.UnanalyzableOps) != 1 || cov.UnanalyzableOps[0] != "Unanalyzed" {
		t.Errorf("unanalyzable = %v, want [Unanalyzed]", cov.UnanalyzableOps)
	}
}

func TestExtractRejectedMetaID(t *testing.T) {
	msg := "meta.weaverTarget vtx.meta.tAAAAAAAAAAAAAAAAAAA rejected: targetId already registered"
	if got := extractRejectedMetaID(msg); got != "tAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("extractRejectedMetaID = %q", got)
	}
	if got := extractRejectedMetaID("unrelated message"); got != "" {
		t.Errorf("extractRejectedMetaID(no match) = %q, want empty", got)
	}
}

func TestLaneRejectedIssuesResolvesCurrentTarget(t *testing.T) {
	hbs := []weaverHeartbeat{{Instance: "i1", Issues: []weaverIssue{
		{Code: "TargetRejected", Message: "meta.weaverTarget vtx.meta.tAAAAAAAAAAAAAAAAAAA rejected: bad mode"},
	}}}
	// The vertex was rejected once but a later spec fixed it and it is now
	// registered as "leaseComplete" — the roundup must say so.
	index := weaverMetaIndex{Targets: map[string]string{"leaseComplete": "vtx.meta.tAAAAAAAAAAAAAAAAAAA"}}
	out := laneRejectedIssues(hbs, index)
	if len(out) != 1 || out[0].TargetID != "leaseComplete" || out[0].MetaID != "tAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("laneRejectedIssues = %+v", out)
	}
}
