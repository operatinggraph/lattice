// Package healthwire holds the health KV value schema — the Entry a Reporter
// writes and a control response embeds. It depends on nothing but the standard
// library.
//
// It exists because internal/refractor/health bundles the Reporter and its
// pollers beside the schema, so importing an Entry links a NATS client through
// internal/substrate. A control response carries an Entry, and an Edge node
// decodes control responses (edge-browser-node-design.md §3.2) without ever
// reporting health. internal/refractor/health re-exports every name here, so
// platform call sites read as health vocabulary and do not import this package
// directly.
package healthwire

// PauseReason values used in health KV entries.
const (
	PauseReasonInfra      = "infra"
	PauseReasonStructural = "structural"
	PauseReasonManual     = "manual"
)

// Entry is the full health KV value schema. All field names are camelCase per
// architecture convention. The KV key is the ruleID; the KV bucket is
// configured via config.HealthKVBucket.
//
// PauseReason and LastError are *string so they marshal as JSON null when
// inactive, satisfying the FR21 requirement for null (not empty string) in
// active entries.
type Entry struct {
	RuleID         string  `json:"ruleId"`
	Status         string  `json:"status"`         // "active" | "paused" | "rebuilding"
	PauseReason    *string `json:"pauseReason"`    // null when active; "infra", "structural", or "manual" when paused
	ActiveSequence uint64  `json:"activeSequence"` // NATS sequence of the active rule version
	ConsumerLag    uint64  `json:"consumerLag"`    // current consumer lag; updated by Story 4.2
	ErrorCount     uint64  `json:"errorCount"`     // cumulative DLQ writes; preserved across restarts
	LastError      *string `json:"lastError"`      // null when no error; non-nil with latest error message
	LastUpdated    string  `json:"lastUpdated"`    // RFC3339 UTC
	// RuleEngine is the engine name that successfully parsed this rule's match
	// body (Story 3.1a). Cached via SetRuleEngine and re-emitted on every
	// status transition. Empty string when not yet set (forward-compat).
	RuleEngine string `json:"ruleEngine,omitempty"`
	// LastProjectedAt is the wall-clock of the last successful target write
	// (lens-projection-liveness-design.md §3.2) — RFC3339 UTC; "" until the
	// lens's first projection. A freshness signal, never an alert input on its
	// own (a quiet, no-match lens naturally has an old value).
	LastProjectedAt string `json:"lastProjectedAt,omitempty"`
	// ProjectionLag is the operator-facing alias of ConsumerLag (same NumPending
	// value, named for what it means to an operator: events behind).
	ProjectionLag uint64 `json:"projectionLag"`
}
