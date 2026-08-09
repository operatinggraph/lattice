package full

// Coverage for dynamic-type-taxonomy-design.md §14 Fire A item 3's engine
// half: WithLabelExpansion's copy-on-write contract, ExpansionLabels'
// AST walk, and the four label-equality sites (§5.1) generalized to set
// membership, keyed on each pattern's OWN LabelExpand flag rather than the
// label string. None of these need KV — nodeMatches/seedAnchorBinds parse
// only the key STRING, and AnchorProjectionKey resolves read-free from the
// tombstoned anchor's stored props, exactly like the existing
// anchor_delete_test.go suite this file sits beside.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithLabelExpansion_ReturnsCopy_OriginalUntouched(t *testing.T) {
	cr, err := New().Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)
	original := cr.(*CompiledRule)
	require.Nil(t, original.LabelExpansion)

	exp := map[string]map[string]struct{}{"location": {"unit": {}, "building": {}}}
	next := WithLabelExpansion(original, exp)

	require.NotSame(t, original, next, "must be a distinct copy, not the same pointer")
	require.Nil(t, original.LabelExpansion, "the original must be left untouched")
	require.Equal(t, exp, next.LabelExpansion)
	require.Same(t, original.Query, next.Query, "the Query AST is shared read-only, not copied")
}

func TestWithLabelExpansion_NilRule(t *testing.T) {
	require.Nil(t, WithLabelExpansion(nil, nil))
}

func TestExpansionLabels(t *testing.T) {
	cases := []struct {
		name string
		body string
		want map[string]struct{}
	}{
		{
			name: "no sigil anywhere returns empty, non-nil",
			body: `MATCH (u:unit) RETURN u.key AS key`,
			want: map[string]struct{}{},
		},
		{
			name: "anchor sigil",
			body: `MATCH (l:location*) RETURN l.key AS key`,
			want: map[string]struct{}{"location": {}},
		},
		{
			name: "mixed bare label and sigil label in one query",
			body: `MATCH (a:location)-[:managedBy]->(b:location*) RETURN a.key AS a1, b.key AS b1`,
			want: map[string]struct{}{"location": {}},
		},
		{
			name: "two distinct expand labels",
			body: `MATCH (a:location*)-[:managedBy]->(b:owner*) RETURN a.key AS a1, b.key AS b1`,
			want: map[string]struct{}{"location": {}, "owner": {}},
		},
		{
			name: "sigil inside a WHERE existence pattern",
			body: `MATCH (u:unit) WHERE NOT (u)-[:managedBy]->(:location*) RETURN u.key AS key`,
			want: map[string]struct{}{"location": {}},
		},
		{
			name: "sigil inside a pattern comprehension",
			body: `MATCH (u:unit) RETURN [(u)-[:managedBy]->(o:location*) | o.key] AS locs`,
			want: map[string]struct{}{"location": {}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr, err := New().Parse(tc.body)
			require.NoError(t, err)
			got := cr.(*CompiledRule).ExpansionLabels()
			require.Equal(t, tc.want, got)
		})
	}
}

func TestExpansionLabels_NilAndEmptyRule(t *testing.T) {
	var nilCR *CompiledRule
	require.Equal(t, map[string]struct{}{}, nilCR.ExpansionLabels())
	require.Equal(t, map[string]struct{}{}, (&CompiledRule{}).ExpansionLabels())
}

// --- Site 1: executor.nodeMatches ---

func TestNodeMatches_LabelExpandSite(t *testing.T) {
	exp := map[string]map[string]struct{}{"location": {"unit": {}, "building": {}}}

	cases := []struct {
		name string
		ex   *executor
		ref  *nodeRef
		n    NodePattern
		want bool
	}{
		{
			name: "bare label equality still holds when LabelExpand is false",
			ex:   &executor{},
			ref:  &nodeRef{key: "vtx.unit.AAAAAAAAAAAAAAAAAAAA"},
			n:    NodePattern{Label: "unit"},
			want: true,
		},
		{
			name: "bare label equality rejects a different type when LabelExpand is false",
			ex:   &executor{},
			ref:  &nodeRef{key: "vtx.room.AAAAAAAAAAAAAAAAAAAA"},
			n:    NodePattern{Label: "unit"},
			want: false,
		},
		{
			name: "LabelExpand true matches a member of the resolved set",
			ex:   &executor{labelExpansion: exp},
			ref:  &nodeRef{key: "vtx.unit.AAAAAAAAAAAAAAAAAAAA"},
			n:    NodePattern{Label: "location", LabelExpand: true},
			want: true,
		},
		{
			name: "LabelExpand true rejects a type outside the resolved set",
			ex:   &executor{labelExpansion: exp},
			ref:  &nodeRef{key: "vtx.identity.AAAAAAAAAAAAAAAAAAAA"},
			n:    NodePattern{Label: "location", LabelExpand: true},
			want: false,
		},
		{
			name: "LabelExpand true with no entry in LabelExpansion fails closed",
			ex:   &executor{labelExpansion: exp},
			ref:  &nodeRef{key: "vtx.unit.AAAAAAAAAAAAAAAAAAAA"},
			n:    NodePattern{Label: "owner", LabelExpand: true},
			want: false,
		},
		{
			name: "empty label always matches",
			ex:   &executor{},
			ref:  &nodeRef{key: "vtx.unit.AAAAAAAAAAAAAAAAAAAA"},
			n:    NodePattern{},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.ex.nodeMatches(tc.ref, tc.n))
		})
	}
}

// TestNodeMatches_MixedExpandAndBareNeverShareADecision pins that a query
// carrying BOTH `(a:location)` and `(b:location*)` decides each pattern
// independently: the lookup keys on the pattern's own LabelExpand flag, not
// the label text — the two patterns share a label string but must never
// share a match verdict.
func TestNodeMatches_MixedExpandAndBareNeverShareADecision(t *testing.T) {
	exp := map[string]map[string]struct{}{"location": {"unit": {}}}
	ex := &executor{labelExpansion: exp}
	buildingRef := &nodeRef{key: "vtx.building.AAAAAAAAAAAAAAAAAAAA"}

	bare := NodePattern{Label: "location"}
	expand := NodePattern{Label: "location", LabelExpand: true}

	require.False(t, ex.nodeMatches(buildingRef, bare),
		"bare (a:location) must not match a building key")
	require.False(t, ex.nodeMatches(buildingRef, expand),
		"building is not in location's resolved set ({unit}) either")

	unitRef := &nodeRef{key: "vtx.unit.AAAAAAAAAAAAAAAAAAAA"}
	require.False(t, ex.nodeMatches(unitRef, bare),
		"bare (a:location) never matches a unit key — location names no instance directly")
	require.True(t, ex.nodeMatches(unitRef, expand),
		"(b:location*) matches unit — it is in the resolved set")
}

// --- Site 2: seedAnchorBinds / seedAnchorFor ---

func TestSeedAnchorBinds_LabelExpandSite(t *testing.T) {
	exp := map[string]map[string]struct{}{"location": {"unit": {}}}

	cases := []struct {
		name    string
		n       NodePattern
		seedKey string
		exp     map[string]map[string]struct{}
		want    bool
	}{
		{
			name:    "bare label equality still holds",
			n:       NodePattern{Label: "unit"},
			seedKey: "vtx.unit.AAAAAAAAAAAAAAAAAAAA",
			want:    true,
		},
		{
			name:    "bare label equality rejects a different type",
			n:       NodePattern{Label: "unit"},
			seedKey: "vtx.room.AAAAAAAAAAAAAAAAAAAA",
			want:    false,
		},
		{
			name:    "LabelExpand true matches a member of the resolved set",
			n:       NodePattern{Label: "location", LabelExpand: true},
			seedKey: "vtx.unit.AAAAAAAAAAAAAAAAAAAA",
			exp:     exp,
			want:    true,
		},
		{
			name:    "LabelExpand true rejects a type outside the resolved set",
			n:       NodePattern{Label: "location", LabelExpand: true},
			seedKey: "vtx.building.AAAAAAAAAAAAAAAAAAAA",
			exp:     exp,
			want:    false,
		},
		{
			name:    "LabelExpand true with no entry fails closed",
			n:       NodePattern{Label: "location", LabelExpand: true},
			seedKey: "vtx.unit.AAAAAAAAAAAAAAAAAAAA",
			exp:     nil,
			want:    false,
		},
		{
			name:    "a `key` property still displaces the seed regardless of LabelExpand",
			n:       NodePattern{Label: "location", LabelExpand: true, Properties: map[string]Expr{"key": &Literal{Value: "x"}}},
			seedKey: "vtx.unit.AAAAAAAAAAAAAAAAAAAA",
			exp:     exp,
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, seedAnchorBinds(tc.n, tc.seedKey, tc.exp))
		})
	}
}

func TestSeedAnchorFor_ThreadsExpansionThroughToTheAnchorPattern(t *testing.T) {
	q := &Query{Clauses: []Clause{
		&Match{Patterns: []PathPattern{{Nodes: []NodePattern{{Variable: "l", Label: "location", LabelExpand: true}}}}},
		&Return{Items: []ProjectionItem{{Expr: &PropertyAccess{Target: &VariableRef{Name: "l"}, Key: "key"}, Alias: "key"}}},
	}}
	exp := map[string]map[string]struct{}{"location": {"unit": {}}}

	require.Equal(t, "vtx.unit.AAAAAAAAAAAAAAAAAAAA",
		seedAnchorFor(q, "vtx.unit.AAAAAAAAAAAAAAAAAAAA", exp))
	require.Empty(t, seedAnchorFor(q, "vtx.building.AAAAAAAAAAAAAAAAAAAA", exp),
		"building is outside location's resolved set")
	require.Empty(t, seedAnchorFor(q, "vtx.unit.AAAAAAAAAAAAAAAAAAAA", nil),
		"no expansion entry for a LabelExpand anchor fails closed")
}

// --- Site 3: AnchorProjectionKey / anchor retraction ---

func TestAnchorProjectionKey_LabelExpandAnchor(t *testing.T) {
	eng := New()
	cr, err := eng.Parse(`MATCH (l:location* {key: $k}) RETURN l.key AS key`)
	require.NoError(t, err)
	compiled := cr.(*CompiledRule)

	exp := map[string]map[string]struct{}{"location": {"unit": {}, "building": {}}}
	withExp := WithLabelExpansion(compiled, exp)

	cases := []struct {
		name      string
		cr        *CompiledRule
		eventType string
		wantOK    bool
	}{
		{
			name:      "unresolved LabelExpansion refuses (fail closed)",
			cr:        compiled,
			eventType: "unit",
			wantOK:    false,
		},
		{
			name:      "a member of the resolved set retracts",
			cr:        withExp,
			eventType: "unit",
			wantOK:    true,
		},
		{
			name:      "a type outside the resolved set does not retract (falls through to re-execute)",
			cr:        withExp,
			eventType: "identity",
			wantOK:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys, ok := eng.AnchorProjectionKey(tc.cr, "vtx.unit.AAAAAAAAAAAAAAAAAAAA", tc.eventType,
				map[string]any{"isDeleted": true})
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, map[string]any{"key": "vtx.unit.AAAAAAAAAAAAAAAAAAAA"}, keys)
			}
		})
	}
}

// TestAnchorProjectionKey_BareAnchorUnaffectedByLabelExpansion pins that a
// bare (non-sigil) anchor's retraction is decided by plain equality even
// when the CompiledRule happens to carry an (unrelated) LabelExpansion —
// the two decisions never mix.
func TestAnchorProjectionKey_BareAnchorUnaffectedByLabelExpansion(t *testing.T) {
	eng := New()
	cr, err := eng.Parse(`MATCH (p:provider {key: $k}) RETURN p.key AS key`)
	require.NoError(t, err)
	compiled := cr.(*CompiledRule)
	withExp := WithLabelExpansion(compiled, map[string]map[string]struct{}{"location": {"unit": {}}})

	_, ok := eng.AnchorProjectionKey(withExp, "vtx.provider.AAAAAAAAAAAAAAAAAAAA", "provider",
		map[string]any{"isDeleted": true})
	require.True(t, ok)

	_, ok = eng.AnchorProjectionKey(withExp, "vtx.unit.AAAAAAAAAAAAAAAAAAAA", "unit",
		map[string]any{"isDeleted": true})
	require.False(t, ok, "unit is not this rule's bare anchor label 'provider'")
}

func TestAnchorLabelExpand(t *testing.T) {
	cr, err := New().Parse(`MATCH (l:location*) RETURN l.key AS key`)
	require.NoError(t, err)
	require.True(t, cr.(*CompiledRule).AnchorLabelExpand())

	cr2, err := New().Parse(`MATCH (p:provider) RETURN p.key AS key`)
	require.NoError(t, err)
	require.False(t, cr2.(*CompiledRule).AnchorLabelExpand())

	var nilCR *CompiledRule
	require.False(t, nilCR.AnchorLabelExpand())
}
