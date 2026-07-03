package healthkv

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/asolgan/lattice/internal/substrate"
)

// consumerHealthEntry is an engine's minimal per-consumer pause-state
// document, stored under health.<component>.<instance>.consumer.<name> in
// the health-kv bucket — a SEPARATE, smaller shape from the Contract #5
// heartbeat document. It carries only the fields ConsumerSink.Load needs to
// restore pause state across a restart.
type consumerHealthEntry struct {
	Status      string `json:"status"`                // "active" | "paused"
	PauseReason string `json:"pauseReason,omitempty"` // "infra" | "structural" | "manual"
	LastError   string `json:"lastError,omitempty"`
}

// ConsumerSink implements substrate.HealthSink for one managed consumer.
// Each consumer gets its own sink instance keyed by name. Every supervisor
// transition is funnelled through this sink: it persists to health-kv AND
// updates the engine's in-memory consumer-state cache, which the Contract #5
// heartbeater reads to populate metrics.consumers.
type ConsumerSink struct {
	conn   *substrate.Conn
	bucket string
	key    string
	name   string
	states *ConsumerStateCache
}

// NewConsumerSink builds a ConsumerSink for one named consumer of the given
// component (e.g. "loom", "weaver", "bridge"), keyed
// health.<component>.<instance>.consumer.<name>.
func NewConsumerSink(conn *substrate.Conn, bucket, component, instance, name string, states *ConsumerStateCache) *ConsumerSink {
	return &ConsumerSink{
		conn:   conn,
		bucket: bucket,
		key:    "health." + component + "." + instance + ".consumer." + name,
		name:   name,
		states: states,
	}
}

func (s *ConsumerSink) SetActive(ctx context.Context) error {
	s.states.set(s.name, consumerState(false, ""))
	return s.put(ctx, consumerHealthEntry{Status: "active"})
}

func (s *ConsumerSink) SetPaused(ctx context.Context, reason substrate.PauseReason, lastErr string) error {
	s.states.set(s.name, consumerState(true, reason))
	return s.put(ctx, consumerHealthEntry{
		Status:      "paused",
		PauseReason: string(reason),
		LastError:   lastErr,
	})
}

// Load restores the persisted pause state at supervisor Add time. A missing or
// malformed entry resolves to (StatusActive, "", nil) per the HealthSink
// contract. It also seeds the in-memory state cache with the restored state.
func (s *ConsumerSink) Load(ctx context.Context) (substrate.HealthStatus, substrate.PauseReason, error) {
	entry, err := s.conn.KVGet(ctx, s.bucket, s.key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			s.states.set(s.name, consumerState(false, ""))
			return substrate.StatusActive, "", nil
		}
		return substrate.StatusActive, "", err
	}
	var doc consumerHealthEntry
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		s.states.set(s.name, consumerState(false, ""))
		return substrate.StatusActive, "", nil
	}
	if doc.Status != "paused" {
		s.states.set(s.name, consumerState(false, ""))
		return substrate.StatusActive, "", nil
	}
	reason := pauseReasonFromString(doc.PauseReason)
	s.states.set(s.name, consumerState(true, reason))
	return substrate.StatusPaused, reason, nil
}

// Delete removes the persisted pause-state entry and the in-memory
// consumer-state cache entry for this consumer. Called when the consumer is
// torn down (supervisor.Remove) so a future re-add of the same name does not
// restore a stale pause and the heartbeat does not report a phantom consumer.
func (s *ConsumerSink) Delete(ctx context.Context) error {
	s.states.delete(s.name)
	if err := s.conn.KVDelete(ctx, s.bucket, s.key); err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
		return err
	}
	return nil
}

func (s *ConsumerSink) put(ctx context.Context, entry consumerHealthEntry) error {
	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = s.conn.KVPut(ctx, s.bucket, s.key, body)
	return err
}

func pauseReasonFromString(s string) substrate.PauseReason {
	switch s {
	case string(substrate.PauseManual):
		return substrate.PauseManual
	case string(substrate.PauseStructural):
		return substrate.PauseStructural
	default:
		return substrate.PauseInfra
	}
}

// ConsumerStateCache holds the last-known pause/active state of every managed
// consumer, fed from the per-consumer ConsumerSink writes the supervisor
// drives. The supervisor persists state through the caller's sink but exposes
// no read-back accessor, so the engine caches each transition and its
// heartbeater reads this cache to populate metrics.consumers (no supervisor
// re-query, no per-message KV scan).
type ConsumerStateCache struct {
	mu     sync.Mutex
	states map[string]string
}

func NewConsumerStateCache() *ConsumerStateCache {
	return &ConsumerStateCache{states: make(map[string]string)}
}

func (c *ConsumerStateCache) set(name, state string) {
	c.mu.Lock()
	c.states[name] = state
	c.mu.Unlock()
}

func (c *ConsumerStateCache) delete(name string) {
	c.mu.Lock()
	delete(c.states, name)
	c.mu.Unlock()
}

// Snapshot returns a copy of the current name→state map, safe for the caller
// to read without further locking.
func (c *ConsumerStateCache) Snapshot() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.states))
	for k, v := range c.states {
		out[k] = v
	}
	return out
}

// consumerState renders a pause reason to the metrics.consumers state string.
func consumerState(paused bool, reason substrate.PauseReason) string {
	if !paused {
		return "running"
	}
	switch reason {
	case substrate.PauseManual:
		return "pausedManual"
	case substrate.PauseStructural:
		return "pausedStructural"
	case substrate.PauseInfra:
		return "pausedInfra"
	default:
		return "paused"
	}
}
