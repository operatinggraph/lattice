package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/subjects"
)

// Manager creates and tracks per-rule durable consumers on the Core KV stream.
// Each consumer uses a NATS queue group so that multiple Refractor instances
// distribute load without duplicate message processing (NFR12).
type Manager struct {
	js         jetstream.JetStream
	streamName string
	filterSubj string
	mu         sync.Mutex
	active     map[string]jetstream.Consumer // ruleID → consumer
}

// NewManager returns a Manager for the given Core KV bucket.
func NewManager(js jetstream.JetStream, coreKVBucket string) *Manager {
	return &Manager{
		js:         js,
		streamName: subjects.CoreKVStream(coreKVBucket),
		filterSubj: subjects.CoreKVFilter(coreKVBucket),
		active:     make(map[string]jetstream.Consumer),
	}
}

// Add creates a durable NATS consumer named "refractor-<ruleID>" with a
// matching queue group for the given rule and registers it. If a consumer for
// ruleID already exists locally, Add is a no-op. Multiple Refractor instances
// calling Add with the same ruleID converge to the same durable consumer in
// NATS — CreateOrUpdateConsumer is idempotent and the queue group ensures
// exactly-once delivery across instances (NFR12).
//
// DeliverLastPerSubjectPolicy is required (ADR-15): the Core KV stream has
// history=1 (MaxMsgsPerSubject=1), so every key has exactly one message.
// This policy is semantically correct ("give me the current value of every key")
// and consistent with Reset(), so delivery-policy conflicts on restart are impossible.
func (m *Manager) Add(ctx context.Context, ruleID string) error {
	name := ruleConsumerName(ruleID)

	m.mu.Lock()
	if _, exists := m.active[ruleID]; exists {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	cons, err := m.js.CreateOrUpdateConsumer(ctx, m.streamName, jetstream.ConsumerConfig{
		Durable:       name,
		DeliverGroup:  name,
		FilterSubject: m.filterSubj,
		DeliverPolicy: jetstream.DeliverLastPerSubjectPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return fmt.Errorf("consumer manager: add %q: %w", ruleID, err)
	}

	m.mu.Lock()
	m.active[ruleID] = cons
	m.mu.Unlock()

	slog.Info("consumer manager: consumer created", "ruleId", ruleID)
	return nil
}

// Consumer returns the active consumer handle for ruleID, or nil if not found.
func (m *Manager) Consumer(ruleID string) jetstream.Consumer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[ruleID]
}

// Remove stops tracking and deletes the durable NATS consumer for ruleID.
// If no consumer exists for ruleID, Remove is a no-op. The mutex is held
// through the NATS DeleteConsumer call to prevent a concurrent Add() from
// re-creating the consumer and having it immediately deleted by this call.
func (m *Manager) Remove(ctx context.Context, ruleID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.active[ruleID]; !exists {
		return nil
	}

	name := ruleConsumerName(ruleID)
	if err := m.js.DeleteConsumer(ctx, m.streamName, name); err != nil {
		return fmt.Errorf("consumer manager: remove %q: %w", ruleID, err)
	}
	delete(m.active, ruleID)

	slog.Info("consumer manager: consumer deleted", "ruleId", ruleID)
	return nil
}

// Reset deletes the existing durable consumer for ruleID (in NATS and locally)
// and creates a new one with DeliverLastPerSubjectPolicy so that all current
// Core KV entries are rescanned from the beginning of the subject log.
// All other consumer config (name, queue group, filter, AckPolicy) is
// identical to Add. Returns the new jetstream.Consumer on success.
// Used by the "rebuild" control operation (FR28).
//
// DeleteConsumer is always called unconditionally (not just when present in the
// local map) to handle the case where a consumer exists in NATS but not locally.
// ErrConsumerNotFound is silently ignored. This also eliminates the TOCTOU race
// between the local-map check and the NATS delete call.
func (m *Manager) Reset(ctx context.Context, ruleID string) (jetstream.Consumer, error) {
	name := ruleConsumerName(ruleID)

	// Remove from local map first so no goroutine can observe a stale handle
	// while the NATS delete is in flight.
	m.mu.Lock()
	delete(m.active, ruleID)
	m.mu.Unlock()

	if err := m.js.DeleteConsumer(ctx, m.streamName, name); err != nil &&
		!errors.Is(err, jetstream.ErrConsumerNotFound) {
		return nil, fmt.Errorf("consumer manager: reset %q: delete: %w", ruleID, err)
	}

	cons, err := m.js.CreateOrUpdateConsumer(ctx, m.streamName, jetstream.ConsumerConfig{
		Durable:       name,
		DeliverGroup:  name,
		FilterSubject: m.filterSubj,
		DeliverPolicy: jetstream.DeliverLastPerSubjectPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("consumer manager: reset %q: create: %w", ruleID, err)
	}

	m.mu.Lock()
	m.active[ruleID] = cons
	m.mu.Unlock()

	slog.Info("consumer manager: consumer reset to DeliverLastPerSubjectPolicy", "ruleId", ruleID)
	return cons, nil
}

// Stop removes all active consumers. Individual removal errors are logged but
// do not prevent the remaining consumers from being removed.
func (m *Manager) Stop(ctx context.Context) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.active))
	for id := range m.active {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		if err := m.Remove(ctx, id); err != nil {
			slog.Error("consumer manager: stop", "ruleId", id, "err", err)
		}
	}
}

// ruleConsumerName returns the durable name and queue group name for a rule consumer.
// The format "refractor-<ruleID>" (Story 2.4a rename from "materializer-<ruleID>").
// Note: ruleID must not be "adjacency" as that would collide with the adjacency
// consumer name used by the Bootstrapper ("refractor-adjacency").
func ruleConsumerName(ruleID string) string {
	return "refractor-" + ruleID
}
