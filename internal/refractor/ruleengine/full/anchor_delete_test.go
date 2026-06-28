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
	const vtxKey = "vtx.provider.abc123"

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
