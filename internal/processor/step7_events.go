package processor

import (
	"fmt"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Event is one item in an EventList per Contract #3 §3.4: each event has
// eventId (NanoID), requestId (from envelope), eventType (canonical event
// class of the shape `<domain>.<eventName>`), domain (the class's first
// segment, single source of truth), targetKey (the mutation key the event
// corresponds to — or empty for events without a direct mutation counterpart),
// payload, and timestamp. Events are published to `core-events` by the outbox
// consumer via substrate.PublishBatch.
type Event struct {
	EventID   string                 `json:"eventId"`
	RequestID string                 `json:"requestId"`
	EventType string                 `json:"eventType"`
	Domain    string                 `json:"domain"`
	TargetKey string                 `json:"targetKey,omitempty"`
	Payload   map[string]interface{} `json:"payload"`
	Timestamp string                 `json:"timestamp"`
}

// eventDomain returns the domain segment (the part before the first dot) of an
// event class. A class of the shape `<domain>.<eventName>` yields `<domain>`.
func eventDomain(class string) string {
	if i := strings.IndexByte(class, '.'); i >= 0 {
		return class[:i]
	}
	return ""
}

// EventList is the ordered list of events constructed from a validated
// ScriptResult at step 7 (BuildEventList) and published by the outbox consumer.
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
		domain := eventDomain(spec.Class)
		if domain == "" || domain == spec.Class {
			return nil, fmt.Errorf("event %d: class %q is not <domain>.<eventName>: a domain segment is required", i, spec.Class)
		}
		if name := spec.Class[len(domain)+1:]; name == "" {
			return nil, fmt.Errorf("event %d: class %q is not <domain>.<eventName>: the event name is empty", i, spec.Class)
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
			Domain:    domain,
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
