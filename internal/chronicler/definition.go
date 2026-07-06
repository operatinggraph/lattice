package chronicler

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/asolgan/lattice/internal/refractor/adapter"
)

// LensSpec mirrors the JSON aspect body stored at `vtx.meta.<NanoID>.spec`
// (parent vertex class `meta.lens`) — the same wire shape
// internal/refractor/lens.LensSpec parses, restricted to the fields an
// eventStream-kind definition needs. Chronicler owns no `coreKv`-kind lens
// (that stays Refractor's, unchanged); every other kind is ignored.
type LensSpec struct {
	ID            string          `json:"id"`
	CanonicalName string          `json:"canonicalName"`
	TargetType    string          `json:"targetType"`
	TargetConfig  json.RawMessage `json:"targetConfig"`
	CypherRule    string          `json:"cypherRule"`
	Source        *SourceConfig   `json:"source,omitempty"`
}

// SourceConfig is the `source` descriptor on a LensSpec. Chronicler only
// ever activates `kind: "eventStream"` definitions — a `coreKv` (absent
// source) or any other kind is silently skipped by the discovery loop
// (source.go), same as Refractor skips a non-`meta.lens` vertex.
type SourceConfig struct {
	Kind string `json:"kind"`

	// Subjects are the core-events JetStream subjects to consume. v1
	// supports exactly one subject — substrate.RunDurableConsumer takes a
	// single FilterSubject; a definition needing more than one subject is a
	// load-time reject until the substrate primitive grows multi-subject
	// support.
	Subjects []string `json:"subjects,omitempty"`

	// Project is the declarative event-body → row mapping.
	Project *EventProjection `json:"project,omitempty"`
}

// targetNATSKVConfig mirrors internal/refractor/lens.TargetNATSKVConfig's
// JSON shape — the only targetType an eventStream definition may declare
// (v1). Protected/GrantTable/SecureColumns are parsed (not simply omitted)
// for the same reason the sibling type parses them: encoding/json silently
// drops unrecognized fields on Unmarshal, so a targetConfig body carrying
// one of these Postgres-only concepts would otherwise load and run with no
// error — a lens author's declared protection/decryption intent silently
// discarded, exactly the "world-publish a model the author believed was
// protected" failure the sibling type's own doctrine exists to prevent
// (internal/refractor/lens/corekv_source.go's TargetNATSKVConfig comment).
// translateDefinition rejects any of the three at load time.
type targetNATSKVConfig struct {
	Bucket        string            `json:"bucket"`
	Key           []string          `json:"key"`
	DeleteMode    string            `json:"deleteMode,omitempty"`
	Protected     bool              `json:"protected,omitempty"`
	GrantTable    bool              `json:"grantTable,omitempty"`
	SecureColumns []json.RawMessage `json:"secureColumns,omitempty"`
}

// Definition is one eventStream lens's fully-parsed, ready-to-run
// configuration — the chronicler-side counterpart of
// internal/refractor/lens.Rule for the eventStream shape only.
type Definition struct {
	ID            string
	CanonicalName string
	Subject       string
	Bucket        string
	KeyField      string
	Project       *EventProjection
	DeleteMode    adapter.DeleteMode
}

// unwrapSpecBody mirrors internal/refractor/lens's tolerant probe: the body
// may be a bare LensSpec JSON object, or a substrate aspect envelope whose
// `data` field carries the LensSpec. Probe for the `source` field (present on
// every eventStream spec, unlike `cypherRule` which the Refractor mirror
// checks and an eventStream spec always leaves empty) — if absent at the top
// level, look under `data`.
func unwrapSpecBody(body []byte) ([]byte, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("probe spec body: %w", err)
	}
	if _, ok := probe["source"]; ok {
		return body, nil
	}
	if data, ok := probe["data"]; ok {
		return data, nil
	}
	return body, nil
}

// isEventStreamSpec reports whether body (an unwrapped LensSpec JSON body)
// declares an eventStream source — the cheap pre-check the discovery loop
// runs before the full translateDefinition validation, so a coreKv lens
// (the overwhelming majority) is skipped without constructing an error.
func isEventStreamSpec(spec *LensSpec) bool {
	return spec.Source != nil && spec.Source.Kind == "eventStream"
}

// translateDefinition validates + translates a LensSpec already known to
// declare source.kind == "eventStream" into a ready-to-run Definition.
// Mirrors internal/refractor/lens.translateEventStreamSpec's validations
// exactly (the wire contract is unchanged by the extraction): no cypher
// engine is resolved — an event lens has no Core-KV vertex to MATCH; v1
// targets nats_kv only and requires exactly one key column and one subject
// (substrate.RunDurableConsumer takes a single FilterSubject).
func translateDefinition(spec *LensSpec) (*Definition, error) {
	if strings.TrimSpace(spec.CypherRule) != "" {
		return nil, fmt.Errorf("lens %q: cypherRule must be empty for an eventStream source (no cypher — the event payload is the only data)", spec.ID)
	}
	if spec.Source == nil {
		return nil, fmt.Errorf("lens %q: source required", spec.ID)
	}
	if len(spec.Source.Subjects) == 0 {
		return nil, fmt.Errorf("lens %q: source.subjects is required for an eventStream lens", spec.ID)
	}
	if len(spec.Source.Subjects) != 1 {
		return nil, fmt.Errorf("lens %q: source.subjects must have exactly one entry (v1: substrate.RunDurableConsumer supports one FilterSubject)", spec.ID)
	}
	if err := validateEventProjection(spec.Source.Project); err != nil {
		return nil, fmt.Errorf("lens %q: %w", spec.ID, err)
	}
	if spec.TargetType != "nats_kv" {
		return nil, fmt.Errorf("lens %q: an eventStream source supports targetType \"nats_kv\" only (got %q)", spec.ID, spec.TargetType)
	}

	var cfg targetNATSKVConfig
	if err := json.Unmarshal(spec.TargetConfig, &cfg); err != nil {
		return nil, fmt.Errorf("lens %q: targetConfig unmarshal: %w", spec.ID, err)
	}
	if cfg.Bucket == "" || len(cfg.Key) == 0 {
		return nil, fmt.Errorf("lens %q: targetConfig.{bucket,key} required for nats_kv", spec.ID)
	}
	if len(cfg.Key) != 1 {
		return nil, fmt.Errorf("lens %q: an eventStream lens targets exactly one key column (got %d)", spec.ID, len(cfg.Key))
	}
	if cfg.Protected || cfg.GrantTable || len(cfg.SecureColumns) > 0 {
		return nil, fmt.Errorf("lens %q: an eventStream lens may not declare protected/grantTable/secureColumns (Postgres-only concepts; NATS-KV has no row-level enforcement)", spec.ID)
	}
	// The target key column and the projection's declared columns are two
	// independent shapes (targetConfig.key vs. source.project.columns) — a
	// lens author can set the key correctly but forget (or typo) that
	// column's own mapping, producing a lens that loads and stores rows
	// correctly keyed, but whose stored document never carries its own
	// identifying field. Reject that mismatch at load time rather than
	// silently accepting it.
	if _, ok := spec.Source.Project.Columns[cfg.Key[0]]; !ok {
		return nil, fmt.Errorf("lens %q: targetConfig.key %q has no matching entry in source.project.columns (the key value must also be projected as a row column)", spec.ID, cfg.Key[0])
	}
	dm, err := adapter.ParseDeleteMode(cfg.DeleteMode)
	if err != nil {
		return nil, fmt.Errorf("lens %q: targetConfig.deleteMode: %w", spec.ID, err)
	}

	return &Definition{
		ID:            spec.ID,
		CanonicalName: spec.CanonicalName,
		Subject:       spec.Source.Subjects[0],
		Bucket:        cfg.Bucket,
		KeyField:      cfg.Key[0],
		Project:       spec.Source.Project,
		DeleteMode:    dm,
	}, nil
}
