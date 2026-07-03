package lens

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/adapter"
)

// These tests cover the LensSpec → Rule conversion (translateSpec) for the
// "nats_subject" target — the Personal Lens transport
// (personal-secure-lens-design.md Fire 1: PL.1).

func TestTranslateSpec_NatsSubject_Valid(t *testing.T) {
	spec := &LensSpec{
		ID:         "personal-lens",
		TargetType: "nats_subject",
		CypherRule: "MATCH (i:identity)-[:owns]->(l:lease) RETURN i.id AS __actor, l.id AS entityId",
		TargetConfig: mustJSON(t, map[string]any{
			"subjectPrefix": "lattice.sync.user",
			"stream":        "SYNC",
			"key":           []string{adapter.PersonalActorKeyField, "entityId"},
		}),
	}
	r, err := translateSpec(spec)
	require.NoError(t, err)
	assert.Equal(t, "nats_subject", r.Into.Target)
	assert.Equal(t, "lattice.sync.user", r.Into.SubjectPrefix)
	assert.Equal(t, "SYNC", r.Into.Stream)
	assert.Equal(t, KeyField{adapter.PersonalActorKeyField, "entityId"}, r.Into.Key)
}

func TestTranslateSpec_NatsSubject_MissingSubjectPrefix(t *testing.T) {
	spec := &LensSpec{
		ID:         "personal-lens",
		TargetType: "nats_subject",
		CypherRule: "MATCH (i:identity)-[:owns]->(l:lease) RETURN i.id AS __actor, l.id AS entityId",
		TargetConfig: mustJSON(t, map[string]any{
			"stream": "SYNC",
			"key":    []string{adapter.PersonalActorKeyField, "entityId"},
		}),
	}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subjectPrefix")
}

func TestTranslateSpec_NatsSubject_MissingStream(t *testing.T) {
	spec := &LensSpec{
		ID:         "personal-lens",
		TargetType: "nats_subject",
		CypherRule: "MATCH (i:identity)-[:owns]->(l:lease) RETURN i.id AS __actor, l.id AS entityId",
		TargetConfig: mustJSON(t, map[string]any{
			"subjectPrefix": "lattice.sync.user",
			"key":           []string{adapter.PersonalActorKeyField, "entityId"},
		}),
	}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream")
}

func TestTranslateSpec_NatsSubject_MissingActorKeyField(t *testing.T) {
	spec := &LensSpec{
		ID:         "personal-lens",
		TargetType: "nats_subject",
		CypherRule: "MATCH (i:identity)-[:owns]->(l:lease) RETURN i.id AS actorId, l.id AS entityId",
		TargetConfig: mustJSON(t, map[string]any{
			"subjectPrefix": "lattice.sync.user",
			"stream":        "SYNC",
			"key":           []string{"actorId", "entityId"},
		}),
	}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), adapter.PersonalActorKeyField)
}
