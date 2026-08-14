package full

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAnchorDeleteResult pins the AST-only retraction-key derivation that lets
// the pipeline emit a Delete when a plain-projection lens's anchor is
// soft-deleted (the non-actor twin of the actor-aware tombstone shortcut). It
// needs no KV — it parses a rule body and resolves the delete key from the AST
// + the tombstoned anchor's root props.
func TestAnchorDeleteResult(t *testing.T) {
	const vtxKey = "vtx.provider.AnchrDe1eteTestAAAAA"

	cases := []struct {
		name      string
		rule      string
		eventType string
		props     map[string]any
		wantOK    bool
		wantKeys  map[string]any
	}{
		{
			name:      "anchor root tombstone, first RETURN is .key (auto-alias)",
			rule:      `MATCH (p:provider {key: $k}) RETURN p.key, p.profile.data.fullName AS fullName`,
			eventType: "provider",
			props:     map[string]any{"isDeleted": true},
			wantOK:    true,
			wantKeys:  map[string]any{"key": vtxKey},
		},
		{
			name:      "anchor root tombstone, first RETURN is .key AS alias",
			rule:      `MATCH (p:provider {key: $k}) RETURN p.key AS providerKey, p.profile.data.fullName AS fullName`,
			eventType: "provider",
			props:     map[string]any{"isDeleted": true},
			wantOK:    true,
			wantKeys:  map[string]any{"providerKey": vtxKey},
		},
		{
			name:      "secondary-node tombstone (event type != anchor label) falls through",
			rule:      `MATCH (a:appointment {key: $k})-[:forPatient]->(pt:patient) RETURN a.key AS apptKey, pt.name AS patientName`,
			eventType: "patient",
			props:     map[string]any{"isDeleted": true},
			wantOK:    false,
		},
		{
			name:      "anchor tombstone, first RETURN is a root-body field (resolved from props)",
			rule:      `MATCH (p:provider {key: $k}) RETURN p.canonicalName AS name`,
			eventType: "provider",
			props:     map[string]any{"isDeleted": true, "canonicalName": "Dr. Who"},
			wantOK:    true,
			wantKeys:  map[string]any{"name": "Dr. Who"},
		},
		{
			name:      "anchor tombstone, first RETURN is an aspect field (anti-pattern) falls through",
			rule:      `MATCH (p:provider {key: $k}) RETURN p.profile.data.fullName AS fullName`,
			eventType: "provider",
			props:     map[string]any{"isDeleted": true},
			wantOK:    false,
		},
		{
			name:      "anchor tombstone, first RETURN is a bare node variable falls through",
			rule:      `MATCH (p:provider {key: $k}) RETURN p`,
			eventType: "provider",
			props:     map[string]any{"isDeleted": true},
			wantOK:    false,
		},
		{
			name:      "anchor tombstone, root-field key absent from props falls through",
			rule:      `MATCH (p:provider {key: $k}) RETURN p.canonicalName AS name`,
			eventType: "provider",
			props:     map[string]any{"isDeleted": true},
			wantOK:    false,
		},
	}

	eng := New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr, err := eng.Parse(tc.rule)
			require.NoError(t, err)

			keys, ok := eng.AnchorDeleteResult(cr, vtxKey, tc.eventType, tc.props)
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, tc.wantKeys, keys)
			} else {
				require.Nil(t, keys)
			}
		})
	}
}

// TestAnchorDeleteResult_CompositeGrantKey pins the Fire-2 retraction: a
// GrantTable lens keyed on the (actor_id, anchor_id, grant_source) composite —
// whose key columns are function calls + a literal, NOT simple property
// accesses — retracts its self-grant on an identity tombstone, resolving every
// key column read-free against the tombstoned anchor exactly as the upsert path
// does. The single-column path is covered by TestAnchorDeleteResult above.
func TestAnchorDeleteResult_CompositeGrantKey(t *testing.T) {
	const idKey = "vtx.identity.Anchrdent1tyTestAAAA"
	const grantRule = `
MATCH (identity:identity)
RETURN
  nanoIdFromKey(identity.key) AS actor_id,
  nanoIdFromKey(identity.key) AS anchor_id,
  'cap-read'                  AS grant_source
`
	eng := New()
	parse := func(body string, cols []string) *CompiledRule {
		cr, err := eng.Parse(body)
		require.NoError(t, err)
		fcr, ok := cr.(*CompiledRule)
		require.True(t, ok)
		fcr.KeyColumns = cols
		return fcr
	}

	t.Run("identity tombstone retracts the composite self-grant", func(t *testing.T) {
		cr := parse(grantRule, []string{"actor_id", "anchor_id", "grant_source"})
		keys, ok := eng.AnchorDeleteResult(cr, idKey, "identity", map[string]any{"isDeleted": true})
		require.True(t, ok)
		require.Equal(t, map[string]any{
			"actor_id":     "Anchrdent1tyTestAAAA", // nanoIdFromKey(identity.key), read-free
			"anchor_id":    "Anchrdent1tyTestAAAA", // the column the single-key path could never produce
			"grant_source": "cap-read",
		}, keys)
	})

	t.Run("secondary-type tombstone falls through (re-execute)", func(t *testing.T) {
		cr := parse(grantRule, []string{"actor_id", "anchor_id", "grant_source"})
		keys, ok := eng.AnchorDeleteResult(cr, "vtx.role.r1", "role", map[string]any{"isDeleted": true})
		require.False(t, ok)
		require.Nil(t, keys)
	})

	t.Run("a composite column needing an aspect read falls through", func(t *testing.T) {
		// actor_id keys on an aspect field — a Core-KV point-read the read-free
		// delete path cannot satisfy from a root-tombstone payload, so no Delete
		// is emitted (never a wrong or partial key).
		const rule = `
MATCH (identity:identity)
RETURN
  identity.profile.data.handle AS actor_id,
  'cap-read'                   AS grant_source
`
		cr := parse(rule, []string{"actor_id", "grant_source"})
		keys, ok := eng.AnchorDeleteResult(cr, idKey, "identity", map[string]any{"isDeleted": true})
		require.False(t, ok)
		require.Nil(t, keys)
	})

	t.Run("a key column absent from RETURN falls through", func(t *testing.T) {
		cr := parse(grantRule, []string{"actor_id", "anchor_id", "grant_source", "phantom"})
		keys, ok := eng.AnchorDeleteResult(cr, idKey, "identity", map[string]any{"isDeleted": true})
		require.False(t, ok)
		require.Nil(t, keys)
	})
}

// TestHasAnchorOnlyKeyColumns pins the structural half of the ok contract on
// its own — the question a caller holding a compiled rule and NO event has to
// ask (the plain arm's narrowing licence,
// plain-lens-neighbour-anchor-derivation-design.md §5.1). Each refusal is
// paired with a shape that holds, so a green refusal can never come from a
// predicate that refuses everything.
func TestHasAnchorOnlyKeyColumns(t *testing.T) {
	eng := New()
	parse := func(t *testing.T, body string, cols []string) *CompiledRule {
		t.Helper()
		cr, err := eng.Parse(body)
		require.NoError(t, err)
		fcr, isFull := cr.(*CompiledRule)
		require.True(t, isFull)
		fcr.KeyColumns = cols
		return fcr
	}

	t.Run("the anchor's own key column is closed", func(t *testing.T) {
		cr := parse(t, `MATCH (p:provider) RETURN p.key AS key, p.canonicalName AS name`, []string{"key"})
		require.True(t, cr.HasAnchorOnlyKeyColumns())
	})

	t.Run("a composite key over the anchor alone is closed", func(t *testing.T) {
		cr := parse(t, `
MATCH (identity:identity)
RETURN
  nanoIdFromKey(identity.key) AS actor_id,
  nanoIdFromKey(identity.key) AS anchor_id,
  'cap-read'                  AS grant_source
`, []string{"actor_id", "anchor_id", "grant_source"})
		require.True(t, cr.HasAnchorOnlyKeyColumns())
	})

	t.Run("a key column bound to a neighbour variable is not closed", func(t *testing.T) {
		cr := parse(t, `
MATCH (u:unit)
OPTIONAL MATCH (u)-[:managedBy]->(l:landlord)
RETURN l.key AS key, u.name AS name
`, []string{"key"})
		require.False(t, cr.HasAnchorOnlyKeyColumns(),
			"a neighbour-keyed lens projects one row per LANDLORD, which a per-anchor evaluation truncates")
	})

	t.Run("an aggregate key column is not closed", func(t *testing.T) {
		cr := parse(t, `
MATCH (u:unit)
OPTIONAL MATCH (u)-[:managedBy]->(l:landlord)
RETURN collect(DISTINCT l.key) AS key, u.name AS name
`, []string{"key"})
		require.False(t, cr.HasAnchorOnlyKeyColumns(),
			"an aggregate's value depends on the grouped row set, which one anchor's binding cannot stand for")
	})

	t.Run("a WITH clause is not closed", func(t *testing.T) {
		cr := parse(t, `
MATCH (u:unit)
WITH u AS renamed
RETURN renamed.key AS key
`, []string{"key"})
		require.False(t, cr.HasAnchorOnlyKeyColumns(),
			"WITH can re-bind, so a RETURN expression's variable NAME stops proving it binds the anchor")
	})

	t.Run("an unlabeled anchor is not closed", func(t *testing.T) {
		cr := parse(t, `MATCH (u) RETURN u.key AS key`, []string{"key"})
		require.False(t, cr.HasAnchorOnlyKeyColumns())
	})

	t.Run("a key column that is not a RETURN alias is not closed", func(t *testing.T) {
		cr := parse(t, `MATCH (p:provider) RETURN p.key AS key`, []string{"key", "phantom"})
		require.False(t, cr.HasAnchorOnlyKeyColumns())
	})

	t.Run("an expanding anchor with no resolved set is not closed", func(t *testing.T) {
		cr := parse(t, `MATCH (u:unit*) RETURN u.key AS key`, []string{"key"})
		require.False(t, cr.HasAnchorOnlyKeyColumns(),
			"a `*` anchor whose downward closure is unresolved fails closed, exactly as AnchorProjectionKey does")

		cr.LabelExpansion = map[string]map[string]struct{}{"unit": {"studio": {}}}
		require.True(t, cr.HasAnchorOnlyKeyColumns(),
			"resolved, the same rule is closed — otherwise the refusal above proves nothing")
	})

	t.Run("no key columns threaded falls back to the first RETURN item", func(t *testing.T) {
		cr := parse(t, `MATCH (p:provider) RETURN p.key, p.canonicalName AS name`, nil)
		require.True(t, cr.HasAnchorOnlyKeyColumns())

		neighbourKeyed := parse(t, `
MATCH (u:unit)
OPTIONAL MATCH (u)-[:managedBy]->(l:landlord)
RETURN l.key, u.name AS name
`, nil)
		require.False(t, neighbourKeyed.HasAnchorOnlyKeyColumns())
	})

	t.Run("a nil rule and a rule with no query are not closed", func(t *testing.T) {
		var nilCR *CompiledRule
		require.False(t, nilCR.HasAnchorOnlyKeyColumns())
		require.False(t, (&CompiledRule{}).HasAnchorOnlyKeyColumns())
	})
}

// TestProjectsOneRowPerAnchor pins the WRITE licence's own closure predicate —
// closure PLUS the conjunct closure alone cannot carry: that a key column says
// WHICH anchor the row is for. The literal-key case is what motivates it. A
// literal references no variable, so it is anchor-only by vacuity; every root
// then lands in one group, and the aggregate in that row is computed from every
// anchor's matches at once — which a per-anchor evaluation truncates.
func TestProjectsOneRowPerAnchor(t *testing.T) {
	eng := New()
	parse := func(t *testing.T, body string, cols []string) *CompiledRule {
		t.Helper()
		cr, err := eng.Parse(body)
		require.NoError(t, err)
		fcr, isFull := cr.(*CompiledRule)
		require.True(t, isFull)
		fcr.KeyColumns = cols
		return fcr
	}

	t.Run("keyed on the anchor's own key", func(t *testing.T) {
		cr := parse(t, `
MATCH (u:unit)
OPTIONAL MATCH (u)-[:managedBy]->(l:landlord)
RETURN u.key AS key, collect(l.name) AS landlords
`, []string{"key"})
		require.True(t, cr.ProjectsOneRowPerAnchor(),
			"the anchor's key is in every row's grouping key, so the collect spans that unit's own matches alone")
	})

	t.Run("keyed on nanoIdFromKey of the anchor's key", func(t *testing.T) {
		cr := parse(t, `
MATCH (identity:identity)
RETURN
  nanoIdFromKey(identity.key) AS actor_id,
  'cap-read'                  AS grant_source
`, []string{"actor_id", "grant_source"})
		require.True(t, cr.ProjectsOneRowPerAnchor(),
			"nanoIdFromKey is the engine's one key-preserving derivation, and one identifying column is enough")
	})

	t.Run("a literal key column with an aggregate refuses", func(t *testing.T) {
		cr := parse(t, `
MATCH (u:unit)
MATCH (u)-[:managedBy]->(l:landlord)
RETURN 'all' AS key, collect(u.name) AS names
`, []string{"key"})
		require.True(t, cr.HasAnchorOnlyKeyColumns(),
			"the key column references no non-anchor variable, so closure alone admits it")
		require.False(t, cr.ProjectsOneRowPerAnchor(),
			"but every unit groups into one row, which a per-anchor evaluation would truncate")
	})

	t.Run("a non-identifying anchor property refuses", func(t *testing.T) {
		cr := parse(t, `
MATCH (u:unit)
OPTIONAL MATCH (u)-[:managedBy]->(l:landlord)
RETURN u.name AS key, collect(l.name) AS landlords
`, []string{"key"})
		require.True(t, cr.HasAnchorOnlyKeyColumns())
		require.False(t, cr.ProjectsOneRowPerAnchor(),
			"two units sharing a name group together, so the row is not one anchor's")
	})

	t.Run("a lossy function over the anchor's key refuses", func(t *testing.T) {
		cr := parse(t, `
MATCH (u:unit)
OPTIONAL MATCH (u)-[:managedBy]->(l:landlord)
RETURN levenshteinDist(u.key, 'vtx.unit.x') AS key, collect(l.name) AS landlords
`, []string{"key"})
		require.False(t, cr.ProjectsOneRowPerAnchor(),
			"a distance over the key is not the key — identity is recognized by name, never inferred from an argument")
	})

	t.Run("a neighbour-keyed lens refuses on the closure half", func(t *testing.T) {
		cr := parse(t, `
MATCH (u:unit)
OPTIONAL MATCH (u)-[:managedBy]->(l:landlord)
RETURN l.key AS key, u.name AS name
`, []string{"key"})
		require.False(t, cr.HasAnchorOnlyKeyColumns())
		require.False(t, cr.ProjectsOneRowPerAnchor())
	})

	t.Run("a nested path merely ending in .key refuses", func(t *testing.T) {
		cr := parse(t, `MATCH (u:unit) RETURN u.listing.key AS key`, []string{"key"})
		require.False(t, cr.ProjectsOneRowPerAnchor(),
			"an aspect's own key field is not the anchor vertex's Contract #1 key")
	})

	t.Run("a nil rule and a rule with no query refuse", func(t *testing.T) {
		var nilCR *CompiledRule
		require.False(t, nilCR.ProjectsOneRowPerAnchor())
		require.False(t, (&CompiledRule{}).ProjectsOneRowPerAnchor())
	})
}

// TestHasAnchorOnlyKeyColumns_AgreesWithAnchorProjectionKey is the pin that
// keeps the two from drifting: the structural predicate is exactly the half of
// AnchorProjectionKey's ok contract that needs no event, so
// AnchorProjectionKey can never say ok on a rule the structural predicate
// refuses. The converse does NOT hold, and the last case below is why — whether
// a particular event's key columns EVALUATE read-free to non-nil scalars is a
// property of that event's stored body, not of the lens.
func TestHasAnchorOnlyKeyColumns_AgreesWithAnchorProjectionKey(t *testing.T) {
	eng := New()
	const vtxKey = "vtx.provider.AnchrDe1eteTestAAAAA"

	cases := []struct {
		name       string
		rule       string
		cols       []string
		eventType  string
		props      map[string]any
		wantClosed bool
		wantKeyOK  bool
	}{
		{
			name:       "anchor-keyed lens, resolvable event",
			rule:       `MATCH (p:provider {key: $k}) RETURN p.key AS key`,
			cols:       []string{"key"},
			eventType:  "provider",
			props:      map[string]any{"isDeleted": true},
			wantClosed: true,
			wantKeyOK:  true,
		},
		{
			name:       "anchor-keyed lens, secondary-node event",
			rule:       `MATCH (a:appointment)-[:forPatient]->(pt:patient) RETURN a.key AS key`,
			cols:       []string{"key"},
			eventType:  "patient",
			props:      map[string]any{"isDeleted": true},
			wantClosed: true,
			wantKeyOK:  false,
		},
		{
			name:       "neighbour-keyed lens",
			rule:       `MATCH (a:appointment)-[:forPatient]->(pt:patient) RETURN pt.key AS key`,
			cols:       []string{"key"},
			eventType:  "appointment",
			props:      map[string]any{"isDeleted": true},
			wantClosed: false,
			wantKeyOK:  false,
		},
		{
			name:       "anchor-keyed on an aspect field, which no root body carries",
			rule:       `MATCH (p:provider) RETURN p.profile.data.fullName AS key`,
			cols:       []string{"key"},
			eventType:  "provider",
			props:      map[string]any{"isDeleted": true},
			wantClosed: true,
			wantKeyOK:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr, err := eng.Parse(tc.rule)
			require.NoError(t, err)
			fcr, isFull := cr.(*CompiledRule)
			require.True(t, isFull)
			fcr.KeyColumns = tc.cols

			require.Equal(t, tc.wantClosed, fcr.HasAnchorOnlyKeyColumns())
			_, keyOK := eng.AnchorProjectionKey(cr, vtxKey, tc.eventType, tc.props)
			require.Equal(t, tc.wantKeyOK, keyOK)
			if keyOK {
				require.True(t, fcr.HasAnchorOnlyKeyColumns(),
					"a resolved projection key implies the structural half held")
			}
		})
	}
}

// TestAnchorDeleteResult_NilGuards covers the defensive fall-throughs: a nil or
// wrong-engine CompiledRule never panics and never emits a Delete.
func TestAnchorDeleteResult_NilGuards(t *testing.T) {
	eng := New()

	keys, ok := eng.AnchorDeleteResult(nil, "vtx.provider.x", "provider", nil)
	require.False(t, ok)
	require.Nil(t, keys)

	keys, ok = eng.AnchorDeleteResult(&CompiledRule{Query: nil}, "vtx.provider.x", "provider", nil)
	require.False(t, ok)
	require.Nil(t, keys)
}
