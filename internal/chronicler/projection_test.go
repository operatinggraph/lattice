package chronicler

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestColumnMapping_ClearOn_RoundTrips proves ClearOn survives the
// Marshal/Unmarshal round trip for all three base shapes — the wire path a
// package's Go-literal ColumnMapping (pkgmgr) actually travels through the
// installed lens definition's aspect data before Chronicler reads it back.
func TestColumnMapping_ClearOn_RoundTrips(t *testing.T) {
	cases := map[string]ColumnMapping{
		"bare path + clearOn": {
			Path:    "payload.reason",
			ClearOn: []string{"loom.patternStarted"},
		},
		"from/map + clearOn": {
			From:    "eventType",
			Map:     map[string]string{"loom.patternStarted": "running"},
			ClearOn: []string{"loom.somethingElse"},
		},
		"when/value + clearOn": {
			When:    []string{"loom.patternCompleted", "loom.patternFailed"},
			Value:   "timestamp",
			ClearOn: []string{"loom.patternStarted"},
		},
	}
	for name, cm := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(cm)
			require.NoError(t, err)
			var decoded ColumnMapping
			require.NoError(t, json.Unmarshal(data, &decoded))
			require.Equal(t, cm.ClearOn, decoded.ClearOn)
			require.Equal(t, cm.Path, decoded.Path)
			require.Equal(t, cm.From, decoded.From)
			require.Equal(t, cm.Map, decoded.Map)
			require.Equal(t, cm.When, decoded.When)
			require.Equal(t, cm.Value, decoded.Value)
		})
	}
}

// TestColumnMapping_MarshalJSON_BarePathNoClearOn_StaysBareString pins
// backward compatibility: a Path-only mapping with no ClearOn must still
// encode as a bare JSON string, not an object — every already-installed
// lens definition's wire shape is unchanged by adding ClearOn.
func TestColumnMapping_MarshalJSON_BarePathNoClearOn_StaysBareString(t *testing.T) {
	data, err := json.Marshal(ColumnMapping{Path: "payload.instanceId"})
	require.NoError(t, err)
	require.Equal(t, `"payload.instanceId"`, string(data))
}

func TestColumnMapping_Validate_RejectsEmptyClearOnEntry(t *testing.T) {
	cm := ColumnMapping{Path: "payload.reason", ClearOn: []string{""}}
	err := cm.validate("failure_reason")
	require.Error(t, err)
	require.Contains(t, err.Error(), "clearOn entries must not be empty")
}

func TestColumnMapping_Validate_RejectsClearOnOverlappingWhen(t *testing.T) {
	cm := ColumnMapping{
		When:    []string{"loom.patternStarted"},
		Value:   "timestamp",
		ClearOn: []string{"loom.patternStarted"},
	}
	err := cm.validate("ended_at")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot appear in both when and clearOn")
}

func TestColumnMapping_Validate_ClearOnAlongsidePath_Accepted(t *testing.T) {
	cm := ColumnMapping{Path: "payload.reason", ClearOn: []string{"loom.patternStarted"}}
	require.NoError(t, cm.validate("failure_reason"))
}
