// Package chronicler is the event→row materializer host
// (orchestration-history-read-model-design.md §2.2/§2.3, Fork C
// re-ratified 2026-07-06): a lens whose rows are derived from a durable
// event stream instead of Core-KV CDC. An event lens has no Core-KV vertex
// to MATCH — the event payload is the only data — so there is no cypher
// engine here, only a declarative event→row mapper (this file) and a
// durable consumer that applies it (manager.go), discovered from
// `vtx.meta.>` eventStream-kind lens definitions (source.go).
//
// This package is the standalone-binary home of what previously ran inside
// cmd/refractor (internal/refractor/eventlens + the eventStream branch of
// internal/refractor/lens) — Refractor's charter is Core-KV CDC only and
// never included lattice-events; only the host moves here, byte-for-byte:
// the projection model, lens definitions, and read models are unchanged.
package chronicler

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EventProjection is a pure, total `event → row` mapping — no cypher, no
// Adjacency, no Core-KV read (an event lens's only data is the event body).
type EventProjection struct {
	// Key is a dot-path into the event yielding the row's key value. Exactly
	// one key column (v1): an event-sourced history row is keyed by the one
	// entity the event stream is about (e.g. the Loom instanceId).
	Key string `json:"key"`

	// Columns maps each target row column to how it's derived from the event.
	Columns map[string]ColumnMapping `json:"columns"`
}

// ColumnMapping is one column's derivation rule. Three shapes, matched by
// which fields are populated (see UnmarshalJSON):
//
//   - A bare dot-path string (Path set, everything else zero): resolve the
//     path against the event; a path absent from THIS event's body (e.g.
//     patternRef on a patternCompleted event, which carries none) leaves the
//     column UNSET for this event — the caller (the Manager) merges onto the
//     previously stored row, carrying the last-known value forward. A
//     missing path is never an error; only a malformed/unrecognized path is
//     (validate, below).
//   - {"from": <path>, "map": {rawValue: mappedValue, …}}: resolve `from`,
//     look it up in `map`. Every value `from` can take MUST be a `map` key
//     (fail-closed — an unmapped value is a load-time-uncaught producer
//     contract violation, handled at runtime as a poison event, never a
//     silent default).
//   - {"when": <eventType> | [<eventType>, …], "value": <path>}: set the
//     column from `value` only when the event's `eventType` is `when` (or is
//     in the `when` list); otherwise the column is UNSET for this event
//     (carried forward, same as a missing bare path).
type ColumnMapping struct {
	Path string // populated when the JSON value was a bare string

	From string            `json:"from,omitempty"`
	Map  map[string]string `json:"map,omitempty"`

	When  []string `json:"when,omitempty"`
	Value string   `json:"value,omitempty"`
}

// UnmarshalJSON accepts either a bare JSON string (a dot-path) or an object
// carrying exactly one of the two structured shapes.
func (c *ColumnMapping) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		c.Path = asString
		return nil
	}
	// The object form's `when` may be a bare string or an array; decode into
	// a tolerant shape then normalize to []string.
	var obj struct {
		From  string            `json:"from"`
		Map   map[string]string `json:"map"`
		When  json.RawMessage   `json:"when"`
		Value string            `json:"value"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("column mapping: must be a string or an object: %w", err)
	}
	c.From = obj.From
	c.Map = obj.Map
	c.Value = obj.Value
	if len(obj.When) > 0 {
		var single string
		if err := json.Unmarshal(obj.When, &single); err == nil {
			c.When = []string{single}
		} else {
			var multi []string
			if err := json.Unmarshal(obj.When, &multi); err != nil {
				return fmt.Errorf("column mapping: when must be a string or array of strings: %w", err)
			}
			c.When = multi
		}
	}
	return nil
}

// MarshalJSON is the mirror image of UnmarshalJSON: a bare-path mapping
// encodes as a JSON string, the two structured shapes as objects. Needed so
// a ColumnMapping constructed as a Go literal (a package's LensSpec, e.g.
// orchestration-base's loomFlowHistory lens) round-trips correctly through
// the aspect-data JSON the installed lens definition is stored as — without
// this, the default reflection-based encoding would serialize the untagged
// `Path` field verbatim (as key "Path"), which UnmarshalJSON's object arm
// does not recognize, silently losing every bare-path column on install.
func (c ColumnMapping) MarshalJSON() ([]byte, error) {
	switch {
	case c.Path != "":
		if c.isFromMap() || c.isConditional() {
			return nil, fmt.Errorf("column mapping: a bare path cannot also carry from/map/when/value")
		}
		return json.Marshal(c.Path)
	case c.isFromMap():
		if c.isConditional() {
			return nil, fmt.Errorf("column mapping: from/map and when/value are mutually exclusive")
		}
		return json.Marshal(struct {
			From string            `json:"from"`
			Map  map[string]string `json:"map"`
		}{From: c.From, Map: c.Map})
	case c.isConditional():
		return json.Marshal(struct {
			When  []string `json:"when"`
			Value string   `json:"value"`
		}{When: c.When, Value: c.Value})
	default:
		return nil, fmt.Errorf("column mapping: empty mapping cannot be marshaled")
	}
}

// isFromMap reports whether this mapping is the {from,map} shape.
func (c ColumnMapping) isFromMap() bool { return c.From != "" || len(c.Map) > 0 }

// isConditional reports whether this mapping is the {when,value} shape.
func (c ColumnMapping) isConditional() bool { return len(c.When) > 0 || c.Value != "" }

// validEventPathRoots are the only dot-path roots an eventStream mapping may
// reference — the canonical Contract #3 §3.4 Event envelope
// (internal/processor/step7_events.go) plus the pipeline-supplied transport
// metadata pseudo-path. Anything else is a load-time reject: an event lens
// has no other data source (no Core-KV read, no cypher).
var validEventPathRoots = map[string]bool{
	"eventType": true,
	"domain":    true,
	"targetKey": true,
	"requestId": true,
	"eventId":   true,
	"timestamp": true,
}

// validatePath rejects a dot-path that cannot possibly resolve against the
// Event envelope — recognize-and-reject wholesale at load time, the same
// doctrine the guard-grammar parser and Refractor's translateSpec already
// apply elsewhere. "payload.<field>" is always accepted (payload is a free-
// form map the event's DDL author controls); every other root must be an
// exact, known top-level Event field; "message.sequence" is the one
// transport-metadata path (JetStream stream sequence, not part of the JSON
// body).
func validatePath(path string) error {
	if path == "" {
		return fmt.Errorf("path must not be empty")
	}
	if path == "message.sequence" {
		return nil
	}
	if strings.HasPrefix(path, "payload.") && len(path) > len("payload.") {
		return nil
	}
	if validEventPathRoots[path] {
		return nil
	}
	return fmt.Errorf("unrecognized event path %q (expected payload.<field>, message.sequence, or one of eventType/domain/targetKey/requestId/eventId/timestamp)", path)
}

// validate checks one column mapping's shape + path syntax at load time.
func (c ColumnMapping) validate(column string) error {
	switch {
	case c.Path != "":
		if c.isFromMap() || c.isConditional() {
			return fmt.Errorf("column %q: a bare path mapping cannot also carry from/map/when/value", column)
		}
		return validatePath(c.Path)
	case c.isFromMap():
		if c.isConditional() {
			return fmt.Errorf("column %q: from/map and when/value are mutually exclusive", column)
		}
		if c.From == "" {
			return fmt.Errorf("column %q: from is required with map", column)
		}
		if len(c.Map) == 0 {
			return fmt.Errorf("column %q: map must not be empty", column)
		}
		return validatePath(c.From)
	case c.isConditional():
		if len(c.When) == 0 {
			return fmt.Errorf("column %q: when is required with value", column)
		}
		for _, w := range c.When {
			if w == "" {
				return fmt.Errorf("column %q: when entries must not be empty", column)
			}
		}
		if c.Value == "" {
			return fmt.Errorf("column %q: value is required with when", column)
		}
		return validatePath(c.Value)
	default:
		return fmt.Errorf("column %q: empty mapping (expected a path string, {from,map}, or {when,value})", column)
	}
}

// validateEventProjection load-time-validates an eventStream lens's Project
// descriptor: the key path + every column mapping must be well-formed and
// reference only recognized event paths (fail-closed — an unknown source
// kind, a cypher body on an event lens, or a mapping referencing a
// non-existent envelope field is a load-time reject, never a silent runtime
// fallthrough).
func validateEventProjection(proj *EventProjection) error {
	if proj == nil {
		return fmt.Errorf("source.project is required for an eventStream lens")
	}
	if strings.TrimSpace(proj.Key) == "" {
		return fmt.Errorf("source.project.key is required")
	}
	if err := validatePath(proj.Key); err != nil {
		return fmt.Errorf("source.project.key: %w", err)
	}
	if len(proj.Columns) == 0 {
		return fmt.Errorf("source.project.columns must not be empty")
	}
	for col, cm := range proj.Columns {
		if err := cm.validate(col); err != nil {
			return fmt.Errorf("source.project.columns: %w", err)
		}
	}
	return nil
}

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

// resolvePath resolves a validatePath-shaped dot-path against ev + the
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
		// (validatePath already rejects any other shape).
		return nil, false
	}
}

// resolveColumn applies one column mapping against ev. set is false when the
// column is not produced by this event (a bare path absent from the payload,
// or a conditional mapping whose `when` doesn't match this event's type) —
// the caller (Manager) carries forward the previously stored value for an
// unset column, since a single lifecycle event only ever carries a subset of
// a row's full column set.
func resolveColumn(cm ColumnMapping, ev Event, seq uint64) (value any, set bool, err error) {
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
			// Fail-closed: every value `from` can take must be a declared map
			// key. An unmapped value is a producer/lens-definition mismatch,
			// surfaced as a poison event by the caller — never a silent
			// default.
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
// patternRef) — Manager merges this partial row onto the previously stored
// one, carrying the last-known value forward for every unset column. The key
// must always resolve (every event on a lens's configured subject is expected
// to carry the key field); a key that fails to resolve is a poison event,
// never silently dropped.
func ProjectEvent(proj *EventProjection, ev Event, seq uint64) (key string, row map[string]any, err error) {
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
