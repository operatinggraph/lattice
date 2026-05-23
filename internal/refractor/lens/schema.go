package lens

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
)

// defaultRegistry is the package-level engine registry used by Parse() to
// resolve the engine that owns a given Rule's body. Story 3.1a wires only
// the simple engine (real) and full engine (stub); 3.1b will replace the
// full stub with the visitor + executor implementation.
//
// Tests may override via SetRegistry to inject alternative engines (e.g.
// always-failing simple to exercise the absent-fallback path).
var defaultRegistry ruleengine.Registry = ruleengine.NewRegistry(simple.New(), full.New())

// SetRegistry replaces the package-level engine registry used by Parse().
// It returns the previous registry so tests can restore it. Test-only.
func SetRegistry(r ruleengine.Registry) ruleengine.Registry {
	prev := defaultRegistry
	defaultRegistry = r
	return prev
}

// KeyField holds one or more field names that form the rule output key.
// In YAML it can be a single string ("agreement_id") or an array (["team_id", "agreement_id"]).
type KeyField []string

// UnmarshalYAML implements yaml.Unmarshaler so KeyField accepts both scalar and sequence nodes.
func (k *KeyField) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value == "" {
			return fmt.Errorf("key must not be empty")
		}
		*k = KeyField{value.Value}
	case yaml.SequenceNode:
		var vals []string
		if err := value.Decode(&vals); err != nil {
			return err
		}
		if len(vals) == 0 {
			return fmt.Errorf("key array must not be empty")
		}
		for i, v := range vals {
			if v == "" {
				return fmt.Errorf("key array element %d must not be empty", i)
			}
		}
		*k = KeyField(vals)
	default:
		return fmt.Errorf("key must be a string or array of strings")
	}
	return nil
}

// IntoConfig describes the target store for a rule's output.
type IntoConfig struct {
	Target          string        `yaml:"target"`        // "nats_kv" or "postgres"
	Bucket          string        `yaml:"bucket"`        // NATS KV bucket name (nats_kv only)
	DSN             string        `yaml:"dsn"`           // Postgres connection string (postgres only)
	Table           string        `yaml:"table"`         // Postgres table name (postgres only)
	Key             KeyField      `yaml:"key"`           // Output key field(s) — required
	QueryTimeoutRaw string        `yaml:"query_timeout"` // e.g. "5s", "30s" — parsed into QueryTimeout by Parse()
	QueryTimeout    time.Duration `yaml:"-"`             // populated by Parse(); defaults to 30s for postgres rules
}

// RetryConfig describes retry behaviour for transient write failures.
// All fields are optional; zero value means no retry.
type RetryConfig struct {
	MaxAttempts int    `yaml:"max_attempts"` // 0 = no retry
	Backoff     string `yaml:"backoff"`      // ISO 8601 duration, e.g. "PT5S"
}

// Rule is the parsed, validated representation of a Lens (formerly Materializer-domain "Rule").
type Rule struct {
	ID            string      `yaml:"id"`
	Match         string      `yaml:"match"`
	Into          IntoConfig  `yaml:"into"`
	Retry         RetryConfig `yaml:"retry"`
	// CanonicalName mirrors the LensSpec.canonicalName field — used by
	// downstream wiring (e.g. the cmd/refractor startPipeline path) to
	// select target-specific envelopes (Story 3.2a Phase C). Not
	// authoritative for routing; pipeline behaviour stays canonical-
	// name-agnostic except for envelope selection.
	CanonicalName string `yaml:"-"`

	// RuleEngine is the explicit engine selector (Story 3.1a). Valid values:
	//   "simple"  — v1 Materializer-derived parser (only engine functional in 3.1a).
	//   "full"    — v2 openCypher engine (stub in 3.1a — always rejects).
	//   ""        — absent; selection falls back simple-then-full.
	RuleEngine string `yaml:"ruleEngine"`

	// ResolvedEngine is the engine name that successfully parsed Match during
	// validation. Populated by Parse(); blank until Parse() returns successfully.
	// Not from YAML.
	ResolvedEngine string `yaml:"-"`

	// CompiledRule is the engine-specific compiled artifact produced by
	// Parse() via the registry's SelectForLens. Story 3.2a wires the full
	// engine through the pipeline; callers route on ResolvedEngine and
	// pass this back to the matching engine's Execute path. May be nil
	// for the simple engine (which re-compiles via simple.Compile against
	// the Into.Key list) — see startPipeline in cmd/refractor for the
	// per-engine routing.
	CompiledRule ruleengine.CompiledRule `yaml:"-"`

	// AttemptedEngines is the ordered list of engines consulted during
	// selection. Populated by Parse() for log/health surfaces.
	AttemptedEngines []string `yaml:"-"`

	// Sequence is the NATS JetStream stream sequence number of the message that
	// activated this rule version. Set at load time by the Loader from message
	// metadata; zero until the rule is received from the stream.
	// Not from YAML — the yaml:"-" tag prevents accidental unmarshalling.
	Sequence uint64 `yaml:"-"`
}

// Parse decodes a rule YAML payload and validates required fields.
// Unknown fields are silently ignored for forward/backward compatibility (NFR22).
func Parse(data []byte) (*Rule, error) {
	var r Rule
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse rule YAML: %w", err)
	}
	if r.ID == "" {
		return nil, fmt.Errorf("rule validation: id is required")
	}
	if r.Match == "" {
		return nil, fmt.Errorf("rule validation: match is required")
	}
	// Validate ruleEngine field shape (the registry will surface unknown
	// values via SelectionError, but we catch obviously-bogus inputs here
	// so callers see a stable error message).
	switch r.RuleEngine {
	case "", ruleengine.EngineSimple, ruleengine.EngineFull:
		// ok
	default:
		return nil, fmt.Errorf("rule validation: ruleEngine must be %q, %q, or empty; got %q",
			ruleengine.EngineSimple, ruleengine.EngineFull, r.RuleEngine)
	}

	// Resolve the engine per Decision #3 (explicit-simple, explicit-full,
	// absent-fallback). Selection failure is surfaced as InvalidRule.
	_, compiled, attempted, selErr := defaultRegistry.SelectForLens(ruleengine.LensDefinition{
		ID:         r.ID,
		RuleBody:   r.Match,
		RuleEngine: r.RuleEngine,
	})
	r.AttemptedEngines = attempted
	if selErr != nil {
		var se *ruleengine.SelectionError
		if errors.As(selErr, &se) {
			return nil, fmt.Errorf("rule validation: invalid match query: %w", se)
		}
		return nil, fmt.Errorf("rule validation: invalid match query: %w", selErr)
	}
	// On success the resolved engine is the LAST attempted name (simple if
	// it succeeded directly; full if simple failed and full succeeded).
	r.ResolvedEngine = attempted[len(attempted)-1]
	r.CompiledRule = compiled

	// Engine-specific post-parse validation (Story 3.1b-ii C2 convergence).
	// We reuse the *simple.CompiledRule's parsed AST returned from the
	// registry instead of re-parsing — eliminating the duplicate parse call.
	// The Compile step still runs because it needs the key fields, which
	// the engine-neutral SelectForLens contract doesn't carry.
	if r.ResolvedEngine == ruleengine.EngineSimple {
		sc, ok := compiled.(*simple.CompiledRule)
		if !ok {
			return nil, fmt.Errorf("rule validation: simple engine returned unexpected compiled type %T", compiled)
		}
		if _, err := simple.Compile(sc.Query, r.Into.Key); err != nil {
			return nil, fmt.Errorf("rule validation: invalid query plan: %w", err)
		}
	}

	if len(r.Into.Key) == 0 {
		return nil, fmt.Errorf("rule validation: into.key is required")
	}
	switch r.Into.Target {
	case "nats_kv":
		if r.Into.Bucket == "" {
			return nil, fmt.Errorf("rule validation: into.bucket is required when target is \"nats_kv\"")
		}
	case "postgres":
		if r.Into.DSN == "" {
			return nil, fmt.Errorf("rule validation: into.dsn is required when target is \"postgres\"")
		}
		if r.Into.Table == "" {
			return nil, fmt.Errorf("rule validation: into.table is required when target is \"postgres\"")
		}
	default:
		return nil, fmt.Errorf("rule validation: into.target must be \"nats_kv\" or \"postgres\", got %q", r.Into.Target)
	}

	// Parse per-rule query timeout (used by Postgres adapter; harmless for nats_kv).
	if r.Into.QueryTimeoutRaw != "" {
		d, err := time.ParseDuration(r.Into.QueryTimeoutRaw)
		if err != nil {
			return nil, fmt.Errorf("rule validation: invalid query_timeout %q: %w", r.Into.QueryTimeoutRaw, err)
		}
		r.Into.QueryTimeout = d
	} else {
		r.Into.QueryTimeout = 30 * time.Second
	}

	slog.Info("lens: rule engine resolved",
		"lensId", r.ID,
		"resolvedEngine", r.ResolvedEngine,
		"attemptedEngines", r.AttemptedEngines)

	return &r, nil
}
