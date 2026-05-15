package fixture

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/asolgan/lattice/internal/refractor/lens"
)

// FixtureRule holds the rule configuration embedded in a fixture file.
// YAML fields mirror lens.Rule. Validated via engine.Parse + engine.Compile
// at RunFixture time (not at Load time) so Load stays infrastructure-free.
type FixtureRule struct {
	ID    string          `yaml:"id"`
	Team  string          `yaml:"team"`
	Match string          `yaml:"match"`
	Into  lens.IntoConfig `yaml:"into"`
}

// InputEntry is one Core KV entry to seed and evaluate.
// Entries are delivered in list order (index 0 first).
type InputEntry struct {
	Key     string         `yaml:"key"`     // Core KV key — must be node_<label>_<id> format
	Payload map[string]any `yaml:"payload"` // node properties including isDeleted
}

// AdjEdge describes one edge in an adjacency list seed entry.
// Field names match adjacency.EdgeEntry JSON field names.
type AdjEdge struct {
	CoreKvKey   string `yaml:"coreKvKey"`
	EdgeID      string `yaml:"edgeId"`
	Name        string `yaml:"name"`
	Direction   string `yaml:"direction"`   // "outbound" | "inbound"
	OtherNodeID string `yaml:"otherNodeId"`
}

// AdjEntry seeds adjacency KV for one node before inputs are delivered.
type AdjEntry struct {
	NodeID string    `yaml:"node_id"` // node to index under (e.g. "node_agreement_abc123")
	Edges  []AdjEdge `yaml:"edges"`
}

// NatsKVExpectEntry is one assertion against the NATS KV target bucket.
type NatsKVExpectEntry struct {
	Key     string         `yaml:"key"`     // expected KV key (built from into.key values)
	Value   map[string]any `yaml:"value"`   // expected JSON value; ignored when Deleted is true
	Deleted bool           `yaml:"deleted"` // true = assert key is absent or tombstoned
}

// ExpectBlock holds all post-processing assertions.
type ExpectBlock struct {
	NatsKV []NatsKVExpectEntry `yaml:"nats_kv"`
}

// Fixture is the parsed, validated representation of a YAML fixture file.
type Fixture struct {
	Description string       `yaml:"description"`
	Rule        FixtureRule  `yaml:"rule"`
	Inputs      []InputEntry `yaml:"inputs"`
	Adjacency   []AdjEntry   `yaml:"adjacency"`
	Expect      ExpectBlock  `yaml:"expect"`
}

// Load reads and parses the YAML fixture at path.
// Returns an error if the file cannot be read, fails YAML parsing,
// or is missing required fields (lens.id, lens.team, lens.match,
// lens.into.target, lens.into.key).
func Load(path string) (*Fixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fixture: read %s: %w", path, err)
	}
	var fix Fixture
	if err := yaml.Unmarshal(data, &fix); err != nil {
		return nil, fmt.Errorf("fixture: parse %s: %w", path, err)
	}
	if fix.Rule.ID == "" {
		return nil, fmt.Errorf("fixture: lens.id is required")
	}
	if fix.Rule.Team == "" {
		return nil, fmt.Errorf("fixture: lens.team is required")
	}
	if fix.Rule.Match == "" {
		return nil, fmt.Errorf("fixture: lens.match is required")
	}
	if fix.Rule.Into.Target == "" {
		return nil, fmt.Errorf("fixture: lens.into.target is required")
	}
	if len(fix.Rule.Into.Key) == 0 {
		return nil, fmt.Errorf("fixture: lens.into.key is required")
	}
	return &fix, nil
}
