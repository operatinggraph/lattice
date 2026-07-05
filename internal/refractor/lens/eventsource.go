package lens

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SourceConfig is the optional `source` descriptor on a LensSpec
// (orchestration-history-read-model-design.md §2.2 — "the Chronicler"). Absent
// ⇒ {kind: "coreKv"}, preserving every existing lens byte-for-byte: the lens
// re-executes CypherRule over Core-KV CDC exactly as before. `kind:
// "eventStream"` is the new primitive: the lens has no Core-KV vertex to
// MATCH — it sources a durable event stream and maps each event's body
// straight into a row via Project, no cypher engine involved.
type SourceConfig struct {
	Kind string `json:"kind"`

	// Subjects are the core-events JetStream subjects to consume (eventStream
	// only), e.g. ["events.loom.>"]. v1 supports exactly one subject —
	// substrate.RunDurableConsumer takes a single FilterSubject; a lens
	// needing more than one subject is a load-time reject until the substrate
	// primitive grows multi-subject support.
	Subjects []string `json:"subjects,omitempty"`

	// Project is the declarative event-body → row mapping (eventStream only).
	Project *EventProjection `json:"project,omitempty"`
}

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
//     column UNSET for this event — the caller (the eventlens runtime) merges
//     onto the previously stored row, carrying the last-known value forward.
//     A missing path is never an error; only a malformed/unrecognized path is
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
		From  string          `json:"from"`
		Map   map[string]string `json:"map"`
		When  json.RawMessage `json:"when"`
		Value string          `json:"value"`
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

// isFromMap reports whether this mapping is the {from,map} shape.
func (c ColumnMapping) isFromMap() bool { return c.From != "" || len(c.Map) > 0 }

// isConditional reports whether this mapping is the {when,value} shape.
func (c ColumnMapping) isConditional() bool { return len(c.When) > 0 || c.Value != "" }

// validEventPaths are the only dot-path roots an eventStream mapping may
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
