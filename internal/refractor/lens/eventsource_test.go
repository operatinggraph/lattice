package lens

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the LensSpec → Rule translation for the eventStream
// source kind (the Chronicler's dark primitive, Fire 1) — load-time
// validation must reject wholesale, never fall through at runtime
// (orchestration-history-read-model-design.md §2.2).

func validEventStreamSpec(t *testing.T) *LensSpec {
	return &LensSpec{
		ID: "test-event-lens",
		Source: &SourceConfig{
			Kind:     "eventStream",
			Subjects: []string{"events.loom.>"},
			Project: &EventProjection{
				Key: "payload.instanceId",
				Columns: map[string]ColumnMapping{
					"instance_id": {Path: "payload.instanceId"},
					"status": {
						From: "eventType",
						Map: map[string]string{
							"loom.patternStarted":   "running",
							"loom.patternCompleted": "complete",
							"loom.patternFailed":    "failed",
						},
					},
					"ended_at": {
						When:  []string{"loom.patternCompleted", "loom.patternFailed"},
						Value: "timestamp",
					},
					"last_event_seq": {Path: "message.sequence"},
				},
			},
		},
		TargetType: "nats_kv",
		TargetConfig: mustJSON(t, map[string]any{
			"bucket": "orchestration-history",
			"key":    []string{"instance_id"},
		}),
	}
}

func TestTranslateSpec_EventStream_Valid(t *testing.T) {
	r, err := translateSpec(validEventStreamSpec(t))
	require.NoError(t, err)
	require.NotNil(t, r.Source)
	assert.Equal(t, "eventStream", r.Source.Kind)
	assert.Equal(t, "nats_kv", r.Into.Target)
	assert.Equal(t, "orchestration-history", r.Into.Bucket)
	assert.Equal(t, KeyField{"instance_id"}, r.Into.Key)
	assert.Empty(t, r.ResolvedEngine, "an event lens resolves no cypher engine")
}

func TestTranslateSpec_EventStream_RejectsCypherBody(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.CypherRule = "MATCH (n) RETURN n"
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cypherRule must be empty")
}

// TestColumnMapping_MarshalJSON_RoundTrip pins ColumnMapping.MarshalJSON
// against its UnmarshalJSON counterpart for all three shapes. Without a
// MarshalJSON method, Go's default reflection-based encoding serializes the
// untagged `Path` field verbatim (key "Path"), which UnmarshalJSON's object
// arm does not recognize — silently losing a bare-path column's value on
// any encode→decode round-trip (exactly what installing a Go-literal
// LensSpec as aspect-data JSON does).
func TestColumnMapping_MarshalJSON_RoundTrip(t *testing.T) {
	cases := map[string]ColumnMapping{
		"bare path": {Path: "payload.instanceId"},
		"from/map": {From: "eventType", Map: map[string]string{
			"loom.patternStarted": "running", "loom.patternCompleted": "complete",
		}},
		"when/value": {When: []string{"loom.patternStarted", "loom.patternCompleted"}, Value: "timestamp"},
	}
	for name, cm := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(cm)
			require.NoError(t, err)
			var got ColumnMapping
			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, cm, got)
		})
	}
}

// TestTranslateSpec_EventStream_SurvivesJSONRoundTrip proves the install
// path end-to-end: a LensSpec constructed as a Go literal (a package's
// LensSpec, e.g. orchestration-base's loomFlowHistory lens) is marshaled to
// JSON exactly as pkgmgr.lensSpecBody emits the aspect data an installed
// lens is stored as, then re-parsed exactly as CoreKVSource loads it back
// from Core KV. Before ColumnMapping.MarshalJSON existed, this round-trip
// silently dropped every bare-path column.
func TestTranslateSpec_EventStream_SurvivesJSONRoundTrip(t *testing.T) {
	spec := validEventStreamSpec(t)
	data, err := json.Marshal(spec.Source)
	require.NoError(t, err)
	var roundTripped SourceConfig
	require.NoError(t, json.Unmarshal(data, &roundTripped))
	spec.Source = &roundTripped

	r, err := translateSpec(spec)
	require.NoError(t, err)
	require.NotNil(t, r.Source)
	assert.Equal(t, "payload.instanceId", r.Source.Project.Columns["instance_id"].Path)
	assert.Equal(t, "message.sequence", r.Source.Project.Columns["last_event_seq"].Path)
	assert.Equal(t, "eventType", r.Source.Project.Columns["status"].From)
	assert.Equal(t, "running", r.Source.Project.Columns["status"].Map["loom.patternStarted"])
}

func TestTranslateSpec_UnknownSourceKind(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.Source.Kind = "bogus"
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source.kind must be")
}

func TestTranslateSpec_EventStream_MissingSubjects(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.Source.Subjects = nil
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source.subjects")
}

func TestTranslateSpec_EventStream_RejectsMultipleSubjects(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.Source.Subjects = []string{"events.loom.>", "events.weaver.>"}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one entry")
}

func TestTranslateSpec_EventStream_MissingProject(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.Source.Project = nil
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source.project is required")
}

func TestTranslateSpec_EventStream_RejectsUnrecognizedPath(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.Source.Project.Columns["bad"] = ColumnMapping{Path: "envelope.committedAt"}
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized event path")
}

func TestTranslateSpec_EventStream_RejectsBadKeyPath(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.Source.Project.Key = "data.instanceId"
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized event path")
}

func TestTranslateSpec_EventStream_RejectsNonNatsKVTarget(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.TargetType = "postgres"
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "targetType \"nats_kv\" only")
}

func TestTranslateSpec_EventStream_RejectsCompositeKey(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.TargetConfig = mustJSON(t, map[string]any{
		"bucket": "orchestration-history",
		"key":    []string{"a", "b"},
	})
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one key column")
}

func TestTranslateSpec_EventStream_RejectsKeyColumnWithNoMapping(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.TargetConfig = mustJSON(t, map[string]any{
		"bucket": "orchestration-history",
		"key":    []string{"flow_id"}, // not a key in spec.Source.Project.Columns
	})
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no matching entry in source.project.columns")
}

func TestTranslateSpec_EventStream_RejectsProtected(t *testing.T) {
	spec := validEventStreamSpec(t)
	spec.TargetConfig = mustJSON(t, map[string]any{
		"bucket":    "orchestration-history",
		"key":       []string{"instance_id"},
		"protected": true,
	})
	_, err := translateSpec(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "protected/grantTable/secureColumns")
}

func TestColumnMapping_UnmarshalJSON_BareString(t *testing.T) {
	var cm ColumnMapping
	require.NoError(t, cm.UnmarshalJSON([]byte(`"payload.instanceId"`)))
	assert.Equal(t, "payload.instanceId", cm.Path)
}

func TestColumnMapping_UnmarshalJSON_FromMap(t *testing.T) {
	var cm ColumnMapping
	require.NoError(t, cm.UnmarshalJSON([]byte(`{"from":"eventType","map":{"a":"b"}}`)))
	assert.Equal(t, "eventType", cm.From)
	assert.Equal(t, map[string]string{"a": "b"}, cm.Map)
}

func TestColumnMapping_UnmarshalJSON_WhenSingleString(t *testing.T) {
	var cm ColumnMapping
	require.NoError(t, cm.UnmarshalJSON([]byte(`{"when":"loom.patternStarted","value":"timestamp"}`)))
	assert.Equal(t, []string{"loom.patternStarted"}, cm.When)
	assert.Equal(t, "timestamp", cm.Value)
}

func TestColumnMapping_UnmarshalJSON_WhenArray(t *testing.T) {
	var cm ColumnMapping
	require.NoError(t, cm.UnmarshalJSON([]byte(`{"when":["a","b"],"value":"timestamp"}`)))
	assert.Equal(t, []string{"a", "b"}, cm.When)
}

func TestValidateEventProjection_RejectsEmptyColumns(t *testing.T) {
	err := validateEventProjection(&EventProjection{Key: "payload.instanceId"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "columns must not be empty")
}

func TestValidateEventProjection_RejectsMixedShapeMapping(t *testing.T) {
	err := validateEventProjection(&EventProjection{
		Key: "payload.instanceId",
		Columns: map[string]ColumnMapping{
			"bad": {Path: "payload.x", From: "eventType"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot also carry")
}
