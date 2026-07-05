// Package eventlens runs the Chronicler's `eventStream` lens-source primitive
// (orchestration-history-read-model-design.md §2.2/§2.3): a lens whose rows
// are derived from a durable event stream instead of Core-KV CDC. An event
// lens has no Core-KV vertex to MATCH — the event payload is the only data —
// so there is no cypher engine here, only a declarative event→row mapper
// (project.go) and a durable consumer that applies it (manager.go).
//
// Fire 1 (this package) ships the primitive dark: cmd/refractor wires it into
// startPipeline so any eventStream-kind lens loaded from Core-KV activates it,
// but no production lens is installed yet — that is Fire 2
// (orchestration-base's loomFlowHistory lens).
package eventlens

import (
	"fmt"
	"strings"

	"github.com/asolgan/lattice/internal/refractor/lens"
)

// Event mirrors the canonical Contract #3 §3.4 event envelope published onto
// core-events (internal/processor/step7_events.go's Event) — a JSON contract
// this package decodes independently, matching the platform's existing
// consumer idiom (internal/refractor/keyshredded, object-store-manager, Weaver
// all decode core-events JSON without importing internal/processor).
type Event struct {
	EventID   string         `json:"eventId"`
	RequestID string         `json:"requestId"`
	EventType string         `json:"eventType"`
	Domain    string         `json:"domain"`
	TargetKey string         `json:"targetKey,omitempty"`
	Payload   map[string]any `json:"payload"`
	Timestamp string         `json:"timestamp"`
}

// resolvePath resolves a lens.validatePath-shaped dot-path against ev + the
// transport-supplied backing-stream sequence. ok is false only for a
// "payload.<field>" path absent from THIS event's payload — expected (a
// lifecycle event carries a subset of a row's columns), never an error. Every
// other recognized root always resolves (they're always present on the
// canonical Event envelope).
func resolvePath(path string, ev Event, seq uint64) (value any, ok bool) {
	switch {
	case path == "message.sequence":
		return seq, true
	case path == "eventType":
		return ev.EventType, true
	case path == "domain":
		return ev.Domain, true
	case path == "targetKey":
		return ev.TargetKey, true
	case path == "requestId":
		return ev.RequestID, true
	case path == "eventId":
		return ev.EventID, true
	case path == "timestamp":
		return ev.Timestamp, true
	case strings.HasPrefix(path, "payload."):
		field := path[len("payload."):]
		v, present := ev.Payload[field]
		return v, present
	default:
		// Unreachable against a load-time-validated EventProjection
		// (lens.validatePath already rejects any other shape).
		return nil, false
	}
}

// resolveColumn applies one column mapping against ev. set is false when the
// column is not produced by this event (a bare path absent from the payload,
// or a conditional mapping whose `when` doesn't match this event's type) —
// the caller (eventlens.Manager) carries forward the previously stored value
// for an unset column, since a single lifecycle event only ever carries a
// subset of a row's full column set.
func resolveColumn(cm lens.ColumnMapping, ev Event, seq uint64) (value any, set bool, err error) {
	switch {
	case cm.Path != "":
		v, ok := resolvePath(cm.Path, ev, seq)
		return v, ok, nil
	case cm.From != "":
		v, ok := resolvePath(cm.From, ev, seq)
		if !ok {
			return nil, false, nil
		}
		key := fmt.Sprintf("%v", v)
		mapped, ok := cm.Map[key]
		if !ok {
			// Fail-closed (eventsource.go's doctrine): every value `from` can
			// take must be a declared map key. An unmapped value is a
			// producer/lens-definition mismatch, surfaced as a poison event
			// by the caller — never a silent default.
			return nil, false, fmt.Errorf("value %q from %q has no entry in map (event type %q)", key, cm.From, ev.EventType)
		}
		return mapped, true, nil
	default: // conditional {when,value}
		matches := false
		for _, w := range cm.When {
			if w == ev.EventType {
				matches = true
				break
			}
		}
		if !matches {
			return nil, false, nil
		}
		v, ok := resolvePath(cm.Value, ev, seq)
		return v, ok, nil
	}
}

// ProjectEvent computes the row key and the PARTIAL column set this one event
// contributes (design §2.2/§2.4). A column absent from row is not produced by
// this event (e.g. pattern_ref on a patternCompleted event, which carries no
// patternRef) — internal/refractor/eventlens's Manager merges this partial row
// onto the previously stored one, carrying the last-known value forward for
// every unset column. The key must always resolve (every event on a lens's
// configured subject is expected to carry the key field); a key that fails to
// resolve is a poison event, never silently dropped.
func ProjectEvent(proj *lens.EventProjection, ev Event, seq uint64) (key string, row map[string]any, err error) {
	keyVal, ok := resolvePath(proj.Key, ev, seq)
	if !ok {
		return "", nil, fmt.Errorf("key path %q did not resolve against event type %q", proj.Key, ev.EventType)
	}
	keyStr, ok := keyVal.(string)
	if !ok || keyStr == "" {
		return "", nil, fmt.Errorf("key path %q resolved to %v, not a non-empty string", proj.Key, keyVal)
	}
	row = make(map[string]any, len(proj.Columns))
	for col, cm := range proj.Columns {
		v, set, err := resolveColumn(cm, ev, seq)
		if err != nil {
			return "", nil, fmt.Errorf("column %q: %w", col, err)
		}
		if set {
			row[col] = v
		}
	}
	return keyStr, row, nil
}
