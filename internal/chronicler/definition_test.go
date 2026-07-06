package chronicler

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func validEventStreamLensSpec() *LensSpec {
	return &LensSpec{
		ID:         "AAvalidLensAAAAAAAAA",
		TargetType: "nats_kv",
		TargetConfig: mustJSON(map[string]any{
			"bucket": "orchestration-history",
			"key":    []string{"instance_id"},
		}),
		Source: &SourceConfig{
			Kind:     "eventStream",
			Subjects: []string{"events.loom.>"},
			Project: &EventProjection{
				Key: "payload.instanceId",
				Columns: map[string]ColumnMapping{
					"instance_id": {Path: "payload.instanceId"},
				},
			},
		},
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestTranslateDefinition_Valid_Succeeds(t *testing.T) {
	def, err := translateDefinition(validEventStreamLensSpec())
	require.NoError(t, err)
	require.Equal(t, "orchestration-history", def.Bucket)
	require.Equal(t, "instance_id", def.KeyField)
}

// TestTranslateDefinition_RejectsProtectedGrantTableSecureColumns proves the
// fail-closed doctrine internal/refractor/lens.translateEventStreamSpec
// enforces (Postgres-only concepts on a nats_kv target world-publish a model
// the author believed was protected) carries over to this package: since
// encoding/json silently drops unrecognized fields on Unmarshal, targetConfig
// declaring protected/grantTable/secureColumns must be an explicit load-time
// reject, not silently ignored.
func TestTranslateDefinition_RejectsProtectedGrantTableSecureColumns(t *testing.T) {
	cases := []struct {
		name          string
		targetConfig  map[string]any
		wantSubstring string
	}{
		{"protected", map[string]any{"bucket": "orchestration-history", "key": []string{"instance_id"}, "protected": true}, "protected/grantTable/secureColumns"},
		{"grantTable", map[string]any{"bucket": "orchestration-history", "key": []string{"instance_id"}, "grantTable": true}, "protected/grantTable/secureColumns"},
		{"secureColumns", map[string]any{"bucket": "orchestration-history", "key": []string{"instance_id"}, "secureColumns": []map[string]any{{"column": "ssn"}}}, "protected/grantTable/secureColumns"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := validEventStreamLensSpec()
			spec.TargetConfig = mustJSON(tc.targetConfig)
			_, err := translateDefinition(spec)
			require.Error(t, err, "%s must be rejected at load time, not silently dropped", tc.name)
			require.Contains(t, err.Error(), tc.wantSubstring)
		})
	}
}

func TestTranslateDefinition_RejectsKeyColumnNotProjected(t *testing.T) {
	spec := validEventStreamLensSpec()
	spec.TargetConfig = mustJSON(map[string]any{
		"bucket": "orchestration-history",
		"key":    []string{"missing_column"},
	})
	_, err := translateDefinition(spec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no matching entry in source.project.columns")
}

func TestTranslateDefinition_RejectsMultipleSubjects(t *testing.T) {
	spec := validEventStreamLensSpec()
	spec.Source.Subjects = []string{"events.loom.>", "events.weaver.>"}
	_, err := translateDefinition(spec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exactly one entry")
}

func TestTranslateDefinition_RejectsNonEmptyCypherRule(t *testing.T) {
	spec := validEventStreamLensSpec()
	spec.CypherRule = "MATCH (i:identity) RETURN i.key AS key"
	_, err := translateDefinition(spec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cypherRule must be empty")
}
