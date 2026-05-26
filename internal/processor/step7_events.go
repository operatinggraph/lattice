package processor

import (
	"fmt"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// Event is one item in an EventList per Contract #3 §3.4: each event has
// eventId (NanoID), requestId (from envelope), eventType (canonical event
// class), targetKey (the mutation key the event corresponds to — or empty
// for events without a direct mutation counterpart), payload, and timestamp.
// Events are published to `core-events` at step 9 via substrate.PublishBatch.
type Event struct {
	EventID   string                 `json:"eventId"`
	RequestID string                 `json:"requestId"`
	EventType string                 `json:"eventType"`
	TargetKey string                 `json:"targetKey,omitempty"`
	Payload   map[string]interface{} `json:"payload"`
	Timestamp string                 `json:"timestamp"`
}

// EventList is the ordered list of events constructed from a validated
// ScriptResult at step 7 (BuildEventList) and published at step 9.
type EventList []Event

// BuildEventList constructs the EventList for a validated MutationBatch
// + EventSpec list. Order matches result.Events order. Each event gets
// a fresh NanoID via substrate.NewNanoID; failures bubble up as errors.
//
// The `targetKey` on each event is derived as follows:
//   - If the event payload carries a "targetKey" string field, it is
//     used verbatim.
//   - Else if the i'th event has an i'th mutation, that mutation's key
//     is used as a best-effort default.
//   - Else empty.
//
// `at` is the wall-clock timestamp the Processor stamps onto every
// event in the batch (commit time).
func BuildEventList(env *OperationEnvelope, result ScriptResult, at time.Time) (EventList, error) {
	stamp := substrate.FormatTimestamp(at)
	out := make(EventList, 0, len(result.Events))
	for i, spec := range result.Events {
		if spec.Class == "" {
			return nil, fmt.Errorf("event %d: missing class", i)
		}
		id, err := substrate.NewNanoID()
		if err != nil {
			return nil, fmt.Errorf("event %d: NanoID: %w", i, err)
		}
		target := ""
		if spec.Data != nil {
			if v, ok := spec.Data["targetKey"].(string); ok {
				target = v
			}
		}
		if target == "" && i < len(result.Mutations) {
			target = result.Mutations[i].Key
		}
		out = append(out, Event{
			EventID:   id,
			RequestID: env.RequestID,
			EventType: spec.Class,
			TargetKey: target,
			Payload:   spec.Data,
			Timestamp: stamp,
		})
	}
	return out, nil
}

// EventClasses returns the ordered list of event class names. Used by
// the tracker's data.eventClasses field at step 8.
func (l EventList) EventClasses() []string {
	out := make([]string, 0, len(l))
	for _, e := range l {
		out = append(out, e.EventType)
	}
	return out
}
