package lens

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// These tests cover the LensSpec → Rule conversion (translateSpec) for a
// multi-branch Personal lens (refractor-shared-keyspace-arbitration-
// design.md §13.2) — cypherBranches replacing the single cypherRule string.

func TestTranslateSpec_CypherBranches_CompilesEachIndependently(t *testing.T) {
	spec := &LensSpec{
		ID:         "personal-lens",
		TargetType: "nats_subject",
		CypherBranches: []string{
			"MATCH (i:identity {key: $actorKey})-[:hasService]->(svc:service)-[:for]->(o:op) RETURN o.key AS anchor, svc.name AS viaServices, o.title AS title",
			"MATCH (i:identity {key: $actorKey})-[:hasRole]->(role:role)-[:for]->(o:op) RETURN o.key AS anchor, role.name AS viaServices, o.title AS title",
		},
		TargetConfig: mustJSON(t, map[string]any{
			"subjectPrefix": "lattice.sync.user",
			"stream":        "SYNC",
			"key":           []string{adapter.PersonalActorKeyField, "anchor"},
			"personal":      true,
		}),
	}
	r, err := translateSpec(spec)
	require.NoError(t, err)
	require.Len(t, r.CompiledBranches, 2)
	assert.Equal(t, r.CompiledBranches[0], r.CompiledRule, "CompiledRule stays branch 0 for single-field consumers")
	for i, cr := range r.CompiledBranches {
		fcr, ok := cr.(*full.CompiledRule)
		require.Truef(t, ok, "branch %d: expected *full.CompiledRule, got %T", i, cr)
		require.NotNil(t, fcr.Query)
	}
}

func TestTranslateSpec_CypherBranches_AndCypherRuleMutuallyExclusive(t *testing.T) {
	spec := &LensSpec{
		ID:         "personal-lens",
		TargetType: "nats_subject",
		CypherRule: "MATCH (i:identity {key: $actorKey}) RETURN i.key AS anchor",
		CypherBranches: []string{
			"MATCH (i:identity {key: $actorKey}) RETURN i.key AS anchor",
			"MATCH (i:identity {key: $actorKey}) RETURN i.key AS anchor",
		},
		TargetConfig: mustJSON(t, map[string]any{
			"subjectPrefix": "lattice.sync.user",
			"stream":        "SYNC",
			"key":           []string{adapter.PersonalActorKeyField, "anchor"},
		}),
	}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestTranslateSpec_CypherBranches_MixedWalkColumnRefused(t *testing.T) {
	spec := &LensSpec{
		ID:         "personal-lens",
		TargetType: "nats_subject",
		CypherBranches: []string{
			"MATCH (i:identity {key: $actorKey})-[:inTeam]->(g:team)-[:for]->(o:op) RETURN o.key AS anchor, g.name AS gname, w.name AS wname, g.name + w.name AS mixed",
			"MATCH (i:identity {key: $actorKey})-[:worksAt]->(w:location)-[:for]->(o:op) RETURN o.key AS anchor, g.name AS gname, w.name AS wname, g.name + w.name AS mixed",
		},
		TargetConfig: mustJSON(t, map[string]any{
			"subjectPrefix": "lattice.sync.user",
			"stream":        "SYNC",
			"key":           []string{adapter.PersonalActorKeyField, "anchor"},
		}),
	}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"mixed"`)
	assert.Contains(t, err.Error(), "more than one walk")
}

func TestTranslateSpec_CypherBranches_BranchParseErrorSurfaces(t *testing.T) {
	spec := &LensSpec{
		ID:         "personal-lens",
		TargetType: "nats_subject",
		CypherBranches: []string{
			"MATCH (i:identity {key: $actorKey}) RETURN i.key AS anchor",
			"NOT VALID CYPHER (((",
		},
		TargetConfig: mustJSON(t, map[string]any{
			"subjectPrefix": "lattice.sync.user",
			"stream":        "SYNC",
			"key":           []string{adapter.PersonalActorKeyField, "anchor"},
		}),
	}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "branch 1")
}
