package processor

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// TrackerKey returns the Core KV key for an operation's idempotency
// tracker (Contract #4 §4.1).
func TrackerKey(requestID string) string {
	return "vtx.op." + requestID
}

// TrackerTTL is the per-key TTL applied to every tracker write (Contract
// #4 §4.3). 24h is the architecture-locked default.
const TrackerTTL = 24 * time.Hour

// Tracker is the Contract #4 §4.1 idempotency-tracker entry written at step 8.
// Shape: `class`, `isDeleted`, `requestId`, `committed`, `observedAt` plus the
// universal provenance triplet (self-referential, per Contract #4 §4.1).
// The Committer enriches `data` with `mutationKeys`, `eventClasses`, and
// (after step 9) `eventsPublishedAt` per Contract #4 §4.2.
type Tracker struct {
	Key              string         `json:"key"`
	Class            string         `json:"class"`
	IsDeleted        bool           `json:"isDeleted"`
	CreatedAt        string         `json:"createdAt"`
	CreatedBy        string         `json:"createdBy"`
	CreatedByOp      string         `json:"createdByOp"`
	LastModifiedAt   string         `json:"lastModifiedAt"`
	LastModifiedBy   string         `json:"lastModifiedBy"`
	LastModifiedByOp string         `json:"lastModifiedByOp"`
	Data             map[string]any `json:"data"`
}

// NewTracker builds a tracker entry for the given envelope.
// committedAt is supplied so tests can fix the timestamp.
func NewTracker(env *OperationEnvelope, committedAt time.Time) Tracker {
	stamp := substrate.FormatTimestamp(committedAt)
	key := TrackerKey(env.RequestID)
	return Tracker{
		Key:              key,
		Class:            "op-tracker",
		IsDeleted:        false,
		CreatedAt:        stamp,
		CreatedBy:        env.Actor,
		CreatedByOp:      key,
		LastModifiedAt:   stamp,
		LastModifiedBy:   env.Actor,
		LastModifiedByOp: key,
		Data: map[string]any{
			"requestId":     env.RequestID,
			"operationType": env.OperationType,
			"lane":          string(env.Lane),
			"submittedAt":   env.SubmittedAt,
			"committedAt":   stamp,
			"committed":     true,
			"observedAt":    stamp,
			"status":        "committed",
		},
	}
}

// Marshal returns the JSON encoding of the tracker (Core KV value).
func (t Tracker) Marshal() ([]byte, error) { return json.Marshal(t) }

// ParseTracker decodes a tracker payload read back from Core KV.
func ParseTracker(b []byte) (*Tracker, error) {
	var t Tracker
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("tracker: json decode: %w", err)
	}
	return &t, nil
}

// CommittedAt extracts the tracker's committedAt timestamp from data.
// Falls back to lastModifiedAt if data.committedAt is absent.
func (t Tracker) CommittedAt() string {
	if t.Data != nil {
		if v, ok := t.Data["committedAt"].(string); ok && v != "" {
			return v
		}
	}
	return t.LastModifiedAt
}
