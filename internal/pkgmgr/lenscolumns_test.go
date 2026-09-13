package pkgmgr

import (
	"encoding/json"
	"testing"

	"github.com/operatinggraph/lattice/internal/lenscolumns"
)

// pkgmgr's own actorAggregate spelling must never drift from lenscolumns' —
// the installer's lens-shape dispatch and the shared column derivation would
// otherwise silently disagree on which lenses are actorAggregate-shaped.
func TestActorAggregateProjectionKind_MatchesLensColumns(t *testing.T) {
	if ActorAggregateProjectionKind != lenscolumns.ActorAggregateKind {
		t.Fatalf("pkgmgr.ActorAggregateProjectionKind = %q, want lenscolumns.ActorAggregateKind %q",
			ActorAggregateProjectionKind, lenscolumns.ActorAggregateKind)
	}
}

// declaredRowBodyColumns's result must be byte-identical to routing the same
// Output through lenscolumns.Projected directly — proving the delegation, not
// merely that both happen to return a non-empty map.
func TestDeclaredRowBodyColumns_MatchesLensColumnsProjected(t *testing.T) {
	out := &OutputDescriptorSpec{
		BodyColumns:        []string{"missing_x", "y"},
		StaticEmptyColumns: []string{"missing_z", "y"},
	}

	got := declaredRowBodyColumns(out)

	want, err := lenscolumns.Projected(lenscolumns.Spec{
		ProjectionKind: lenscolumns.ActorAggregateKind,
		Output: &lenscolumns.Output{
			BodyColumns:        out.BodyColumns,
			StaticEmptyColumns: out.StaticEmptyColumns,
		},
	}, nil)
	if err != nil {
		t.Fatalf("lenscolumns.Projected: %v", err)
	}

	if len(got) != len(want.Columns) {
		t.Fatalf("declaredRowBodyColumns = %v, want %v", got, want.Columns)
	}
	for k, v := range want.Columns {
		if got[k] != v {
			t.Fatalf("declaredRowBodyColumns[%q] = %q, want %q", k, got[k], v)
		}
	}
	// y is declared in both lists; BodyColumns must win the provenance.
	if got["y"] != lenscolumns.ProvenanceBodyColumns {
		t.Fatalf(`declaredRowBodyColumns["y"] = %q, want %q (BodyColumns wins on overlap)`,
			got["y"], lenscolumns.ProvenanceBodyColumns)
	}
}

// declaredRowBodyColumns must still answer (never panic) for an entry-keyed
// Output, the one shape lenscolumns marks unreadable for this branch — zero
// such lenses exist in the corpus today, but the function must fail closed
// rather than crash if one arrives.
func TestDeclaredRowBodyColumns_EntryKeyedIsEmptyNotPanic(t *testing.T) {
	got := declaredRowBodyColumns(&OutputDescriptorSpec{
		BodyColumns:    []string{"x"},
		EntryKeyColumn: "id",
	})
	if len(got) != 0 {
		t.Fatalf("declaredRowBodyColumns = %v, want empty for an entry-keyed Output", got)
	}
}

// lensColumnsSpec must convert LensSpec's Output/Source fields into
// lenscolumns' types by their shared JSON shape, not a hand-copied subset —
// proven by round-tripping a body through both types' JSON tags and comparing.
func TestLensColumnsSpec_JSONRoundTripsWithPkgmgrTypes(t *testing.T) {
	l := LensSpec{
		ProjectionKind: ActorAggregateProjectionKind,
		Output: &OutputDescriptorSpec{
			AnchorType:         "identity",
			OutputKeyPattern:   "cap-read.x.{actorSuffix}",
			BodyColumns:        []string{"a", "b"},
			EmptyBehavior:      "delete",
			StaticEmptyColumns: []string{"c"},
		},
	}

	got := lensColumnsSpec(l)

	if got.ProjectionKind != l.ProjectionKind {
		t.Fatalf("ProjectionKind = %q, want %q", got.ProjectionKind, l.ProjectionKind)
	}
	if got.Output == nil {
		t.Fatal("Output = nil")
	}

	// The conversion must be lossless for every field lenscolumns.Output
	// declares — proven by marshalling LensSpec.Output and unmarshalling
	// directly into lenscolumns.Output, then comparing to lensColumnsSpec's
	// own answer.
	wireBody, err := json.Marshal(l.Output)
	if err != nil {
		t.Fatalf("marshal LensSpec.Output: %v", err)
	}
	var want lenscolumns.Output
	if err := json.Unmarshal(wireBody, &want); err != nil {
		t.Fatalf("unmarshal into lenscolumns.Output: %v", err)
	}
	if got.Output.EntryKeyColumn != want.EntryKeyColumn ||
		len(got.Output.BodyColumns) != len(want.BodyColumns) ||
		len(got.Output.StaticEmptyColumns) != len(want.StaticEmptyColumns) {
		t.Fatalf("lensColumnsSpec(l).Output = %+v, want %+v", got.Output, want)
	}
}

// A body marshalled from pkgmgr.SourceConfig must decode into lenscolumns.Source
// carrying the same Kind and the same Project.Columns key set — lenscolumns
// must not import pkgmgr, so this pin runs from pkgmgr's side.
func TestLensColumnsSpec_SourceConfigJSONRoundTrips(t *testing.T) {
	l := LensSpec{
		Source: &SourceConfig{
			Kind:     "eventStream",
			Subjects: []string{"core-events.>"},
			Project: &EventProjection{
				Key: "id",
				Columns: map[string]ColumnMapping{
					"missing_x": {Path: "some.path"},
					"y":         {From: "other"},
				},
			},
		},
	}

	got := lensColumnsSpec(l)
	if got.Source == nil {
		t.Fatal("Source = nil")
	}
	if got.Source.Kind != "eventStream" {
		t.Fatalf("Source.Kind = %q, want eventStream", got.Source.Kind)
	}
	if got.Source.Project == nil {
		t.Fatal("Source.Project = nil")
	}
	if len(got.Source.Project.Columns) != 2 {
		t.Fatalf("Source.Project.Columns = %v, want 2 keys", got.Source.Project.Columns)
	}
	for _, k := range []string{"missing_x", "y"} {
		if _, ok := got.Source.Project.Columns[k]; !ok {
			t.Fatalf("Source.Project.Columns missing key %q: %v", k, got.Source.Project.Columns)
		}
	}
}

// A plain lens's Spec/SpecBranches must land as CypherRule/CypherBranches.
func TestLensColumnsSpec_PlainLensFields(t *testing.T) {
	l := LensSpec{
		Spec:         "MATCH (n) RETURN n.k AS k",
		SpecBranches: []string{"branch0", "branch1"},
	}
	got := lensColumnsSpec(l)
	if got.CypherRule != l.Spec {
		t.Fatalf("CypherRule = %q, want %q", got.CypherRule, l.Spec)
	}
	if len(got.CypherBranches) != 2 || got.CypherBranches[0] != "branch0" || got.CypherBranches[1] != "branch1" {
		t.Fatalf("CypherBranches = %v, want %v", got.CypherBranches, l.SpecBranches)
	}
}
