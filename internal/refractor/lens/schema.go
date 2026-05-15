package lens

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asolgan/lattice/internal/refractor/engine"
)

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

// Rule is the parsed, validated representation of a Materializer rule.
type Rule struct {
	ID    string      `yaml:"id"`
	Team  string      `yaml:"team"`
	Match string      `yaml:"match"`
	Into  IntoConfig  `yaml:"into"`
	Retry RetryConfig `yaml:"retry"`

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
	if r.Team == "" {
		return nil, fmt.Errorf("rule validation: team is required")
	}
	if r.Match == "" {
		return nil, fmt.Errorf("rule validation: match is required")
	}
	query, err := engine.Parse(r.Match)
	if err != nil {
		return nil, fmt.Errorf("rule validation: invalid match query: %w", err)
	}
	if _, err := engine.Compile(query, r.Into.Key); err != nil {
		return nil, fmt.Errorf("rule validation: invalid query plan: %w", err)
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

	return &r, nil
}
