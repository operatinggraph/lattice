package loom

import (
	"reflect"
	"testing"
)

// TestCorrelationKeys proves the three structural correlation keys are returned
// in order (requestId, taskKey, externalRef), de-duplicated, and that empty
// fields are skipped — the property that lets Loom try each against the durable
// token store with at most one live pointer resolving (Contract #10 §10.6).
func TestCorrelationKeys(t *testing.T) {
	mk := func(requestID, taskKey, externalRef string) eventBody {
		var ev eventBody
		ev.RequestID = requestID
		ev.Payload.TaskKey = taskKey
		ev.Payload.ExternalRef = externalRef
		return ev
	}
	cases := []struct {
		name string
		ev   eventBody
		want []string
	}{
		{
			name: "externalRef yields the externalTask key, ordered third",
			ev:   mk("req1", "vtx.task.t1", "handle1"),
			want: []string{"req1", "vtx.task.t1", "handle1"},
		},
		{
			name: "externalRef alone",
			ev:   mk("", "", "handle1"),
			want: []string{"handle1"},
		},
		{
			name: "all empty yields no keys",
			ev:   mk("", "", ""),
			want: []string{},
		},
		{
			name: "externalRef equal to requestId is de-duplicated",
			ev:   mk("same", "", "same"),
			want: []string{"same"},
		},
		{
			name: "externalRef equal to taskKey is de-duplicated",
			ev:   mk("", "shared", "shared"),
			want: []string{"shared"},
		},
		{
			name: "ordering is requestId, taskKey, externalRef",
			ev:   mk("r", "k", "x"),
			want: []string{"r", "k", "x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := correlationKeys(tc.ev)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("correlationKeys() = %v, want %v", got, tc.want)
			}
		})
	}
}
