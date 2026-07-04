package lens

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
)

// defaultRegistry is the package-level engine registry used by Parse() to
// resolve the engine that owns a given Rule's body.
//
// Tests may override via SetRegistry to inject an alternative engine.
var defaultRegistry ruleengine.Registry = ruleengine.NewRegistry(full.New())

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
	Target          string        `yaml:"target"`        // "nats_kv", "postgres", or "nats_subject"
	Bucket          string        `yaml:"bucket"`        // NATS KV bucket name (nats_kv only)
	DSN             string        `yaml:"dsn"`           // Postgres connection string (postgres only)
	Table           string        `yaml:"table"`         // Postgres table name (postgres only)
	Key             KeyField      `yaml:"key"`           // Output key field(s) — required
	QueryTimeoutRaw string        `yaml:"query_timeout"` // e.g. "5s", "30s" — parsed into QueryTimeout by Parse()
	QueryTimeout    time.Duration `yaml:"-"`             // populated by Parse(); defaults to 30s for postgres rules
	DeleteMode      string        `yaml:"delete_mode"`   // "hard" (default) or "soft"; defaulted+validated by Parse()

	// Read-path authorization (Contract #6 §6.14, D1.3) — populated from the
	// LensSpec postgres targetConfig by translateSpec; not from YAML.
	Protected    bool                `yaml:"-"` // provision an RLS table at activation + project authz_anchors
	Public       bool                `yaml:"-"` // explicit public opt-out (no RLS)
	GrantTable   bool                `yaml:"-"` // project to actor_read_grants via the seq-guarded writer
	Columns      []adapter.ColumnDef `yaml:"-"` // declared business columns to provision (protected only)
	ArrayColumns []string            `yaml:"-"` // columns to encode as Postgres arrays (authz_anchors + text[] body cols)

	// SecureColumns marks this lens as a Secure Lens (Contract #3 §3.10):
	// decrypt-at-projection columns, validated protected-postgres-only by
	// translateSpec. Not from YAML.
	SecureColumns []SecureColumn `yaml:"-"`

	// DiffRetraction opts a plain (non-actor-aware) postgres lens into Fire 3's
	// neighbor-driven / multi-row target-diff retraction
	// (negative-filter-retraction-projection-design.md §2.4) — for a lens whose
	// output key is not derivable read-free from its own anchor (a composite
	// key with a column bound to a non-anchor variable), the pipeline diffs the
	// target's live key set against each re-execute instead of relying on the
	// anchor-self presence check (which structurally cannot reach this shape).
	// Populated from the LensSpec targetConfig by translateSpec; not from YAML.
	DiffRetraction bool `yaml:"-"`

	// SubjectPrefix and Stream configure a "nats_subject" target — the
	// Personal Lens transport (personal-secure-lens-design.md Fire 1).
	// Populated from the LensSpec targetConfig by translateSpec; not from YAML.
	SubjectPrefix string `yaml:"-"`
	Stream        string `yaml:"-"`

	// Personal opts a "nats_subject" lens into the Fire 2 cross-vertex fan-out:
	// the projection.InstallPersonalLens path installs an ActorEnumerator
	// (actorType "identity") and re-executes the lens cypher once per
	// enumerated recipient, injecting the recipient into the reserved
	// "__actor" key field (personal-secure-lens-design.md §3.3). Absent, a
	// "nats_subject" lens is PL.1's direct shape: its own cypher RETURN
	// supplies "__actor" and no fan-out is installed. Populated from the
	// LensSpec targetConfig by translateSpec; not from YAML.
	Personal bool `yaml:"-"`
}

// RetryConfig describes retry behaviour for transient write failures.
// All fields are optional; zero value means no retry.
type RetryConfig struct {
	MaxAttempts int    `yaml:"max_attempts"` // 0 = no retry
	Backoff     string `yaml:"backoff"`      // ISO 8601 duration, e.g. "PT5S"
}

// Rule is the parsed, validated representation of a Lens (formerly Materializer-domain "Rule").
type Rule struct {
	ID    string      `yaml:"id"`
	Match string      `yaml:"match"`
	Into  IntoConfig  `yaml:"into"`
	Retry RetryConfig `yaml:"retry"`
	// CanonicalName mirrors the LensSpec.canonicalName field — used by
	// downstream wiring (e.g. the cmd/refractor startPipeline path) to
	// select target-specific envelopes. Not authoritative for routing;
	// pipeline behaviour stays canonical-name-agnostic except for envelope
	// selection.
	CanonicalName string `yaml:"-"`

	// RuleEngine is the explicit engine selector. Valid values:
	//   "full" — v2 openCypher engine.
	//   ""     — absent; resolves to "full".
	RuleEngine string `yaml:"ruleEngine"`

	// ProjectionKind mirrors LensSpec.ProjectionKind. "actorAggregate" routes
	// the lens to the projection plan compiler; absence/any other value leaves
	// the lens untouched by actor-aggregate machinery. Plumbed from the spec the
	// same way RuleEngine is. Not from YAML.
	ProjectionKind string `yaml:"-"`

	// Output mirrors LensSpec.Output: the §6.13 Output descriptor for an
	// actor-aggregate lens, surfaced onto the Rule so the projection layer can
	// compile a ProjectionPlan without re-reading the spec. Nil for non-
	// actor-aggregate lenses. Not from YAML.
	Output *OutputDescriptorSpec `yaml:"-"`

	// ResolvedEngine is the engine name that successfully parsed Match during
	// validation. Populated by Parse(); blank until Parse() returns successfully.
	// Not from YAML.
	ResolvedEngine string `yaml:"-"`

	// CompiledRule is the engine-specific compiled artifact produced by
	// Parse() via the registry's SelectForLens. Passed to the full engine's
	// Execute path — see startPipeline in cmd/refractor.
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
	case "", ruleengine.EngineFull:
		// ok
	default:
		return nil, fmt.Errorf("rule validation: ruleEngine must be %q or empty; got %q",
			ruleengine.EngineFull, r.RuleEngine)
	}

	// Resolve the engine ("full" or absent → full; anything else already
	// rejected above). Selection failure is surfaced as InvalidRule.
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
	r.ResolvedEngine = attempted[len(attempted)-1]
	r.CompiledRule = compiled

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
	case "nats_subject":
		// SubjectPrefix/Stream are translateSpec-only (JSON targetConfig), the
		// same as postgres's Protected/Columns — a YAML rule declaring this
		// target fails later at adapter construction, not here.
	default:
		return nil, fmt.Errorf("rule validation: into.target must be \"nats_kv\", \"postgres\", or \"nats_subject\", got %q", r.Into.Target)
	}

	// Validate + default delete_mode: absent → "hard"; reject values outside
	// {hard,soft} (adapter.ParseDeleteMode is the single source of truth for the
	// allowed set). Normalize the stored value to the canonical mode string.
	dm, err := adapter.ParseDeleteMode(r.Into.DeleteMode)
	if err != nil {
		return nil, fmt.Errorf("rule validation: into.delete_mode: %w", err)
	}
	r.Into.DeleteMode = string(dm)

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
