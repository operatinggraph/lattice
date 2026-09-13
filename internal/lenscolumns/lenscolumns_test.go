package lenscolumns

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func stubReturnColumns(cols []string, err error) func(string) ([]string, error) {
	return func(rule string) ([]string, error) {
		return cols, err
	}
}

// Table test over every §2 row of the design, including the three unreadable
// shapes, plus branches-over-rule precedence and the nil-parser case.
func TestProjected(t *testing.T) {
	tests := []struct {
		name        string
		spec        Spec
		returnCols  func(string) ([]string, error)
		wantColumns map[string]string
		wantSource  string
		wantErr     bool
	}{
		{
			name: "eventStream reads Source.Project.Columns, no cypher",
			spec: Spec{
				Source: &Source{Kind: EventStreamKind, Project: &Project{
					Columns: map[string]json.RawMessage{"a": nil, "b": nil},
				}},
				CypherRule: "MATCH (n) RETURN n.x", // must be ignored: no cypher read
			},
			returnCols: func(string) ([]string, error) {
				t.Fatal("eventStream must not call returnColumns")
				return nil, nil
			},
			wantColumns: map[string]string{"a": ProvenanceProjectColumns, "b": ProvenanceProjectColumns},
			wantSource:  ProvenanceProjectColumns,
		},
		{
			// Unreadable, not empty: an eventStream lens's rows ARE its
			// project.columns, so with none declared nothing states what a row
			// carries — and an empty answer would read as "this lens projects
			// no gap column", which is a claim about a lens nobody described.
			name:    "eventStream with nil Project is unreadable, never empty",
			spec:    Spec{Source: &Source{Kind: EventStreamKind}},
			wantErr: true,
		},
		{
			name: "eventStream with an empty project.columns is unreadable too",
			spec: Spec{Source: &Source{Kind: EventStreamKind, Project: &Project{
				Columns: map[string]json.RawMessage{},
			}}},
			wantErr: true,
		},
		{
			name: "actorAggregate with Output, no EntryKeyColumn: BodyColumns ∪ StaticEmptyColumns",
			spec: Spec{
				ProjectionKind: ActorAggregateKind,
				Output: &Output{
					BodyColumns:        []string{"missing_x", "y"},
					StaticEmptyColumns: []string{"missing_z"},
				},
			},
			wantColumns: map[string]string{
				"missing_x": ProvenanceBodyColumns,
				"y":         ProvenanceBodyColumns,
				"missing_z": ProvenanceStaticEmptyColumns,
			},
			wantSource: resultSourceOutput,
		},
		{
			name: "actorAggregate BodyColumns wins provenance on overlap with StaticEmptyColumns",
			spec: Spec{
				ProjectionKind: ActorAggregateKind,
				Output: &Output{
					BodyColumns:        []string{"dup"},
					StaticEmptyColumns: []string{"dup"},
				},
			},
			wantColumns: map[string]string{"dup": ProvenanceBodyColumns},
			wantSource:  resultSourceOutput,
		},
		{
			name:    "actorAggregate with Output == nil is unreadable",
			spec:    Spec{ProjectionKind: ActorAggregateKind},
			wantErr: true,
		},
		{
			name: "actorAggregate with EntryKeyColumn set is unreadable (runtime shape)",
			spec: Spec{
				ProjectionKind: ActorAggregateKind,
				Output: &Output{
					BodyColumns:    []string{"x"},
					EntryKeyColumn: "id",
				},
			},
			wantErr: true,
		},
		{
			name:        "plain lens reads RETURN via CypherRule",
			spec:        Spec{CypherRule: "MATCH (n:identity) RETURN n.name AS name"},
			returnCols:  stubReturnColumns([]string{"name"}, nil),
			wantColumns: map[string]string{"name": ProvenanceReturn},
			wantSource:  ProvenanceReturn,
		},
		{
			// The runtime refuses a spec carrying both (lens/corekv_source.go
			// at activation), so such a lens never projects at all — reading
			// branch 0 would describe row keys that never arrive.
			name: "a spec declaring BOTH cypherRule and cypherBranches is unreadable",
			spec: Spec{
				CypherRule:     "MATCH (n) RETURN n.k AS k",
				CypherBranches: []string{"MATCH (m) RETURN m.k AS k"},
			},
			returnCols: stubReturnColumns([]string{"k"}, nil),
			wantErr:    true,
		},
		{
			name: "plain lens with CypherBranches reads branch 0",
			spec: Spec{
				CypherBranches: []string{"branch0 rule", "branch1 rule"},
			},
			returnCols: func(rule string) ([]string, error) {
				if rule != "branch0 rule" {
					t.Fatalf("expected branch 0's rule, got %q", rule)
				}
				return []string{"col0"}, nil
			},
			wantColumns: map[string]string{"col0": ProvenanceReturn},
			wantSource:  ProvenanceReturn,
		},
		{
			name:       "plain lens with nil returnColumns is unreadable, never empty",
			spec:       Spec{CypherRule: "MATCH (n) RETURN n.x"},
			returnCols: nil,
			wantErr:    true,
		},
		{
			name:       "plain lens with a parse error is unreadable",
			spec:       Spec{CypherRule: "not cypher"},
			returnCols: stubReturnColumns(nil, errors.New("boom")),
			wantErr:    true,
		},
		{
			name:       "plain lens with an empty RETURN is unreadable",
			spec:       Spec{CypherRule: "MATCH (n)"},
			returnCols: stubReturnColumns(nil, nil),
			wantErr:    true,
		},
		{
			name:        "an unrecognized projectionKind falls to the plain branch",
			spec:        Spec{ProjectionKind: "somethingElse", CypherRule: "MATCH (n) RETURN n.k AS k"},
			returnCols:  stubReturnColumns([]string{"k"}, nil),
			wantColumns: map[string]string{"k": ProvenanceReturn},
			wantSource:  ProvenanceReturn,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Projected(tc.spec, tc.returnCols)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Projected() = %+v, nil; want an error", got)
				}
				if !errors.Is(err, ErrUnreadable) {
					t.Fatalf("error = %v, want errors.Is(err, ErrUnreadable)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Projected() error = %v", err)
			}
			if len(got.Columns) != len(tc.wantColumns) {
				t.Fatalf("Columns = %v, want %v", got.Columns, tc.wantColumns)
			}
			for k, v := range tc.wantColumns {
				if got.Columns[k] != v {
					t.Fatalf("Columns[%q] = %q, want %q (full: %v)", k, got.Columns[k], v, got.Columns)
				}
			}
			if got.Source != tc.wantSource {
				t.Fatalf("Source = %q, want %q", got.Source, tc.wantSource)
			}
		})
	}
}

func TestGaps(t *testing.T) {
	r := Result{Columns: map[string]string{
		"missing_x": ProvenanceBodyColumns,
		"y":         ProvenanceBodyColumns,
		"missing_z": ProvenanceReturn,
	}}
	gaps := Gaps(r)
	want := map[string]string{"missing_x": ProvenanceBodyColumns, "missing_z": ProvenanceReturn}
	if len(gaps) != len(want) {
		t.Fatalf("Gaps() = %v, want %v", gaps, want)
	}
	for k, v := range want {
		if gaps[k] != v {
			t.Fatalf("Gaps()[%q] = %q, want %q", k, gaps[k], v)
		}
	}
}

// Gaps must never return nil, even over a zero-value Result — a caller that
// ranges over it must never need a nil check.
func TestGaps_NeverNil(t *testing.T) {
	if g := Gaps(Result{}); g == nil {
		t.Fatal("Gaps(Result{}) = nil, want a non-nil empty map")
	}
}

// A body marshalled from pkgmgr's OutputDescriptorSpec/SourceConfig-shaped
// JSON (the actual wire shape build.go's lensSpecBody writes) must decode
// into these types unchanged — proven from the pkgmgr side in
// TestLensColumnsSpec_JSONRoundTripsWithPkgmgrTypes, since lenscolumns must
// not import pkgmgr. This is the reverse half: lenscolumns' own JSON tags
// match the on-wire spec body shape byte for byte.
func TestSpec_JSONTagsMatchStoredBody(t *testing.T) {
	body := []byte(`{
		"projectionKind": "actorAggregate",
		"output": {
			"bodyColumns": ["a", "b"],
			"staticEmptyColumns": ["c"],
			"entryKeyColumn": ""
		},
		"cypherRule": "",
		"cypherBranches": null
	}`)
	var s Spec
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if s.ProjectionKind != ActorAggregateKind {
		t.Fatalf("ProjectionKind = %q", s.ProjectionKind)
	}
	if s.Output == nil || fmt.Sprint(s.Output.BodyColumns) != "[a b]" || fmt.Sprint(s.Output.StaticEmptyColumns) != "[c]" {
		t.Fatalf("Output = %+v", s.Output)
	}
}
