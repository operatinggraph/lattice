package eventlens

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/lens"
)

func testProjection() *lens.EventProjection {
	return &lens.EventProjection{
		Key: "payload.instanceId",
		Columns: map[string]lens.ColumnMapping{
			"instance_id": {Path: "payload.instanceId"},
			"pattern_ref": {Path: "payload.patternRef"},
			"status": {
				From: "eventType",
				Map: map[string]string{
					"loom.patternStarted":   "running",
					"loom.patternCompleted": "complete",
					"loom.patternFailed":    "failed",
				},
			},
			"failure_reason": {Path: "payload.reason"},
			"started_at":     {When: []string{"loom.patternStarted"}, Value: "timestamp"},
			"ended_at":       {When: []string{"loom.patternCompleted", "loom.patternFailed"}, Value: "timestamp"},
			"last_event_seq": {Path: "message.sequence"},
		},
	}
}

func TestProjectEvent_PatternStarted_SetsRunningAndStartedAt(t *testing.T) {
	ev := Event{
		EventType: "loom.patternStarted",
		Payload: map[string]any{
			"instanceId": "inst-1",
			"patternRef": "onboarding-v1",
			"subjectKey": "identity.1",
		},
		Timestamp: "2026-07-05T10:00:00Z",
	}
	key, row, err := ProjectEvent(testProjection(), ev, 42)
	require.NoError(t, err)
	assert.Equal(t, "inst-1", key)
	assert.Equal(t, "inst-1", row["instance_id"])
	assert.Equal(t, "onboarding-v1", row["pattern_ref"])
	assert.Equal(t, "running", row["status"])
	assert.Equal(t, "2026-07-05T10:00:00Z", row["started_at"])
	assert.Equal(t, uint64(42), row["last_event_seq"])
	_, hasEnded := row["ended_at"]
	assert.False(t, hasEnded, "started event must not set ended_at")
	_, hasReason := row["failure_reason"]
	assert.False(t, hasReason, "started event carries no reason field")
}

func TestProjectEvent_PatternCompleted_OmitsColumnsItDoesNotCarry(t *testing.T) {
	ev := Event{
		EventType: "loom.patternCompleted",
		Payload: map[string]any{
			"instanceId": "inst-1",
		},
		Timestamp: "2026-07-05T10:05:00Z",
	}
	key, row, err := ProjectEvent(testProjection(), ev, 43)
	require.NoError(t, err)
	assert.Equal(t, "inst-1", key)
	assert.Equal(t, "complete", row["status"])
	assert.Equal(t, "2026-07-05T10:05:00Z", row["ended_at"])
	// patternCompleted's payload carries no patternRef/subjectKey — these
	// columns must be OMITTED (unset), not written as null/empty, so the
	// caller's carry-forward merge preserves the previously stored value.
	_, hasPatternRef := row["pattern_ref"]
	assert.False(t, hasPatternRef, "pattern_ref must be omitted, not nulled, on an event that doesn't carry it")
	_, hasStartedAt := row["started_at"]
	assert.False(t, hasStartedAt, "a completed event must not (re)set started_at")
}

func TestProjectEvent_PatternFailed_SetsReason(t *testing.T) {
	ev := Event{
		EventType: "loom.patternFailed",
		Payload: map[string]any{
			"instanceId": "inst-1",
			"reason":     "vendor timeout",
		},
		Timestamp: "2026-07-05T10:05:00Z",
	}
	_, row, err := ProjectEvent(testProjection(), ev, 44)
	require.NoError(t, err)
	assert.Equal(t, "failed", row["status"])
	assert.Equal(t, "vendor timeout", row["failure_reason"])
	assert.Equal(t, "2026-07-05T10:05:00Z", row["ended_at"])
}

func TestProjectEvent_UnmappedEventType_IsError(t *testing.T) {
	ev := Event{
		EventType: "loom.somethingElse",
		Payload:   map[string]any{"instanceId": "inst-1"},
	}
	_, _, err := ProjectEvent(testProjection(), ev, 1)
	require.Error(t, err, "an eventType with no entry in the status map must be a poison event, never a silent default")
}

func TestProjectEvent_MissingKeyField_IsError(t *testing.T) {
	ev := Event{
		EventType: "loom.patternStarted",
		Payload:   map[string]any{"patternRef": "x"}, // no instanceId
	}
	_, _, err := ProjectEvent(testProjection(), ev, 1)
	require.Error(t, err)
}

func TestProjectEvent_OutOfOrderReplay_CallerCarriesSeqForward(t *testing.T) {
	// ProjectEvent itself is stateless per-call; monotonic convergence under
	// replay is the adapter's guard (natskv_test.go's Guarded_RejectsLowerSeqUpsert)
	// plus the eventlens.Manager's read-merge-write. This test only pins that
	// last_event_seq always reflects THIS call's seq, independent of ordering.
	ev := Event{EventType: "loom.patternStarted", Payload: map[string]any{"instanceId": "inst-1"}}
	_, row, err := ProjectEvent(testProjection(), ev, 5)
	require.NoError(t, err)
	assert.Equal(t, uint64(5), row["last_event_seq"])
}
