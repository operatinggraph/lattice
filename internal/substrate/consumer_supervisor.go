package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// ConsumerSupervisor owns the supervised-pump mechanism for a set of durable
// consumers: a registry of desired ConsumerSpecs, desired-vs-running reconcile
// (Add / Remove / Reset / Stop), and per-consumer lifecycle — a composable
// pause state machine (infra / structural / manual), a NakWithDelay backoff
// floor, and HealthSink persist/restore. Policy (the message handler, error
// classification, recovery probe, and health key) stays with the caller via the
// ConsumerSpec hooks, so Loom and Weaver reuse one supervised pump instead of
// each hand-rolling lifecycle, backoff, and health.
//
// No jetstream (or nats.go) type appears on the exported surface: callers import
// only substrate. The supervisor never sets MaxDeliver on any consumer it
// creates — retry cadence is bounded (NakWithDelay) but retry count is not.
type ConsumerSupervisor struct {
	conn *Conn

	mu      sync.Mutex
	managed map[string]*managedConsumer
	stopped bool
}

// managedConsumer holds the per-consumer runtime: the desired spec, its pump
// goroutine cancel + done, and the live pause state machine.
type managedConsumer struct {
	spec   ConsumerSpec
	cancel context.CancelFunc
	done   chan struct{}
	state  *pumpState
}

// NewConsumerSupervisor constructs a supervisor over conn. The supervisor uses
// conn's package-internal JetStream handle; nothing jetstream-typed is exposed.
func NewConsumerSupervisor(conn *Conn) *ConsumerSupervisor {
	return &ConsumerSupervisor{
		conn:    conn,
		managed: make(map[string]*managedConsumer),
	}
}

// Add registers spec, creates (idempotently) its durable consumer, and starts
// the supervised pump goroutine. Calling Add with a Name that is already managed
// is a no-op (the existing pump keeps running) — use Reset to recreate a durable
// whose config changed. Returns an error if the spec is invalid or the durable
// cannot be created.
func (s *ConsumerSupervisor) Add(ctx context.Context, spec ConsumerSpec) error {
	if err := validateSpec(spec); err != nil {
		return err
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return fmt.Errorf("substrate: ConsumerSupervisor: Add after Stop")
	}
	if _, exists := s.managed[spec.Name]; exists {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if _, err := s.createConsumer(ctx, spec); err != nil {
		return err
	}

	pumpCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	st := newPumpState()
	mc := &managedConsumer{spec: spec, cancel: cancel, done: done, state: st}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		cancel()
		return fmt.Errorf("substrate: ConsumerSupervisor: Add after Stop")
	}
	if _, exists := s.managed[spec.Name]; exists {
		s.mu.Unlock()
		cancel()
		return nil
	}
	s.managed[spec.Name] = mc
	s.mu.Unlock()

	go func() {
		defer close(done)
		s.runPump(pumpCtx, spec, st)
	}()
	return nil
}

// Remove stops the pump for name and deletes its server-side durable. If name is
// not managed, Remove is a no-op. Deleting the durable is the caller's explicit
// intent (operator retiring a consumer), distinct from Stop, which preserves
// durables.
func (s *ConsumerSupervisor) Remove(ctx context.Context, name string) error {
	s.mu.Lock()
	mc, exists := s.managed[name]
	if !exists {
		s.mu.Unlock()
		return nil
	}
	delete(s.managed, name)
	s.mu.Unlock()

	mc.cancel()
	<-mc.done

	if err := s.conn.js.DeleteConsumer(ctx, mc.spec.Stream, name); err != nil &&
		!errors.Is(err, jetstream.ErrConsumerNotFound) {
		return fmt.Errorf("substrate: ConsumerSupervisor: remove %q: %w", name, err)
	}
	return nil
}

// Reset deletes and recreates the durable for name (preserving the spec's
// delivery policy and all other config) and points the pump at the new durable.
// The delete is unconditional and ErrConsumerNotFound-tolerant (TOCTOU
// hardening): it runs whether or not the durable is locally known, so a durable
// that exists in NATS but not in the registry is still recreated cleanly. If name
// is not managed, an optional spec override may be supplied via UpdateSpec before
// Reset; otherwise Reset on an unmanaged name returns an error.
//
// Reset is the migration target for Refractor's rebuild delete-recreate-swap.
func (s *ConsumerSupervisor) Reset(ctx context.Context, name string) error {
	s.mu.Lock()
	mc, exists := s.managed[name]
	s.mu.Unlock()
	if !exists {
		return fmt.Errorf("substrate: ConsumerSupervisor: reset %q: not managed", name)
	}

	// Unconditional delete (TOCTOU-safe): tolerate ErrConsumerNotFound.
	if err := s.conn.js.DeleteConsumer(ctx, mc.spec.Stream, name); err != nil &&
		!errors.Is(err, jetstream.ErrConsumerNotFound) {
		return fmt.Errorf("substrate: ConsumerSupervisor: reset %q: delete: %w", name, err)
	}

	if _, err := s.createConsumer(ctx, mc.spec); err != nil {
		return fmt.Errorf("substrate: ConsumerSupervisor: reset %q: create: %w", name, err)
	}

	// Signal the pump to re-open its iterator against the recreated durable.
	mc.state.requestReopen()
	return nil
}

// UpdateSpec replaces the desired spec for an already-managed consumer without
// recreating the durable. Used to change a spec's FilterSubject (etc.) before a
// Reset recreates the durable with the new config. Returns an error if name is
// not managed. Hooks and config captured by the running pump are refreshed; the
// pump picks up the new handler/classify/probe atomically.
func (s *ConsumerSupervisor) UpdateSpec(name string, mutate func(*ConsumerSpec)) error {
	s.mu.Lock()
	mc, exists := s.managed[name]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("substrate: ConsumerSupervisor: update %q: not managed", name)
	}
	mutate(&mc.spec)
	updated := mc.spec
	s.mu.Unlock()
	mc.state.updateSpec(updated)
	return nil
}

// Stop stops every pump but does NOT delete any durable: a durable's persisted
// position is the point of its durability (substrate doctrine). Callers that
// want delete-on-shutdown invoke Remove per consumer from their own adapter
// layer. After Stop the supervisor rejects further Add calls.
func (s *ConsumerSupervisor) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	managed := make([]*managedConsumer, 0, len(s.managed))
	for _, mc := range s.managed {
		managed = append(managed, mc)
	}
	s.managed = make(map[string]*managedConsumer)
	s.mu.Unlock()

	for _, mc := range managed {
		mc.cancel()
	}
	for _, mc := range managed {
		<-mc.done
	}
}

// Pause manually pauses the pump for name (operator control surface; FR30 / 9.4
// disable). Idempotent. A manual pause is cleared only by Resume, never by a
// passing probe. Returns true iff name was managed and the pause was applied;
// false (no-op) if name is not managed. The bool lets a control surface fuse the
// managed-check and the act into one lock acquisition, with no check-then-act
// gap a concurrent Remove could slip through.
func (s *ConsumerSupervisor) Pause(ctx context.Context, name string) bool {
	s.mu.Lock()
	mc, exists := s.managed[name]
	s.mu.Unlock()
	if !exists {
		return false
	}
	mc.state.addReason(PauseManual)
	s.persistPaused(ctx, mc.spec, PauseManual, "")
	return true
}

// Resume clears manual + structural pauses for name and force-exits an in-flight
// infra probe loop, so processing resumes without waiting for the next probe
// (FR31). No-op if name is not managed.
//
// Resume only clears pause reasons that were active at the moment it was
// called. A pause reason added AFTER a Resume — e.g. a structural escalation
// discovered by the probe loop, or a fresh infra failure on the next pump
// iteration — is NOT retroactively cleared by that earlier Resume; the new
// failure re-enters its own pause state and requires its own Resume.
//
// Returns true iff name was managed and the resume was applied; false (no-op)
// if name is not managed — the bool lets a control surface fuse the
// managed-check and the act into one lock acquisition.
func (s *ConsumerSupervisor) Resume(ctx context.Context, name string) bool {
	s.mu.Lock()
	mc, exists := s.managed[name]
	s.mu.Unlock()
	if !exists {
		return false
	}
	mc.state.operatorResume()
	s.persistActive(context.WithoutCancel(ctx), mc.spec)
	return true
}

// IsManaged reports whether name is currently in the supervisor's managed set.
// Read under the same lock that guards Add/Remove, so it is a consistent,
// race-free view at the call instant. It is the authoritative allow-list for an
// operator control surface: Pause/Resume are silent no-ops on an unmanaged name,
// so a caller validates IsManaged first to turn an unknown name into an explicit
// error rather than a silently-dropped command.
func (s *ConsumerSupervisor) IsManaged(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.managed[name]
	return ok
}

// ManagedNames returns the names of every currently-managed consumer, read under
// the registry lock. The returned slice is a fresh copy the caller owns; order is
// unspecified (it is the Go map-iteration order). It is the authoritative name
// set for an operator control surface — the lazily-populated health/state caches
// elsewhere are not a reliable allow-list.
func (s *ConsumerSupervisor) ManagedNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.managed))
	for name := range s.managed {
		names = append(names, name)
	}
	return names
}

// PendingForConsumer returns the number of pending (un-delivered) messages for
// the named consumer's durable, queried by durable name. Returns an error if the
// consumer info cannot be read. Exposed as a substrate-typed accessor so callers
// (e.g. Refractor's rebuild lag-watch) need no jetstream.Consumer handle.
func (s *ConsumerSupervisor) PendingForConsumer(ctx context.Context, name string) (uint64, error) {
	s.mu.Lock()
	mc, exists := s.managed[name]
	s.mu.Unlock()
	if !exists {
		return 0, fmt.Errorf("substrate: ConsumerSupervisor: pending %q: not managed", name)
	}
	cons, err := s.conn.js.Consumer(ctx, mc.spec.Stream, name)
	if err != nil {
		return 0, fmt.Errorf("substrate: ConsumerSupervisor: pending %q: consumer: %w", name, err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("substrate: ConsumerSupervisor: pending %q: info: %w", name, err)
	}
	return info.NumPending, nil
}

// createConsumer creates (idempotently) the durable described by spec. The
// supervisor never sets MaxDeliver — retry count stays unbounded.
func (s *ConsumerSupervisor) createConsumer(ctx context.Context, spec ConsumerSpec) (jetstream.Consumer, error) {
	cfg := jetstream.ConsumerConfig{
		Durable:       spec.Name,
		DeliverPolicy: toJetstreamDeliverPolicy(spec.DeliverPolicy),
		AckPolicy:     jetstream.AckExplicitPolicy,
	}
	// FilterSubjects (the multi-filter set) and FilterSubject (the single filter)
	// are mutually exclusive on a JetStream consumer config — setting both is
	// rejected by the server. The set form takes precedence when supplied.
	if len(spec.FilterSubjects) > 0 {
		cfg.FilterSubjects = spec.FilterSubjects
	} else {
		cfg.FilterSubject = spec.FilterSubject
	}
	if spec.DeliverGroup != "" {
		cfg.DeliverGroup = spec.DeliverGroup
	}
	if spec.AckWait > 0 {
		cfg.AckWait = spec.AckWait
	}
	cons, err := s.conn.js.CreateOrUpdateConsumer(ctx, spec.Stream, cfg)
	if err != nil {
		return nil, fmt.Errorf("substrate: ConsumerSupervisor: create consumer %q on %q: %w",
			spec.Name, spec.Stream, err)
	}
	return cons, nil
}

func toJetstreamDeliverPolicy(p DeliverPolicy) jetstream.DeliverPolicy {
	if p == DeliverLastPerSubject {
		return jetstream.DeliverLastPerSubjectPolicy
	}
	return jetstream.DeliverAllPolicy
}

func validateSpec(spec ConsumerSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("substrate: ConsumerSupervisor: spec.Name required")
	}
	if spec.Stream == "" {
		return fmt.Errorf("substrate: ConsumerSupervisor: spec %q: Stream required", spec.Name)
	}
	if spec.Handler == nil {
		return fmt.Errorf("substrate: ConsumerSupervisor: spec %q: Handler required", spec.Name)
	}
	if spec.FilterSubject != "" && len(spec.FilterSubjects) > 0 {
		return fmt.Errorf("substrate: ConsumerSupervisor: spec %q: FilterSubject and FilterSubjects are mutually exclusive", spec.Name)
	}
	return nil
}

func specLogger(spec ConsumerSpec) *slog.Logger {
	if spec.Logger != nil {
		return spec.Logger
	}
	return slog.Default()
}

// persistActive / persistPaused funnel every supervisor state transition through
// the spec's HealthSink. A nil sink skips the I/O; sink errors are logged, never
// fatal (mirrors the pipeline's setHealthActive/setHealthPaused guards).
func (s *ConsumerSupervisor) persistActive(ctx context.Context, spec ConsumerSpec) {
	if spec.Health == nil {
		return
	}
	if err := spec.Health.SetActive(ctx); err != nil {
		specLogger(spec).Error("substrate: ConsumerSupervisor: health set active",
			"consumer", spec.Name, "error", err)
	}
}

func (s *ConsumerSupervisor) persistPaused(ctx context.Context, spec ConsumerSpec, reason PauseReason, lastErr string) {
	if spec.Health == nil {
		return
	}
	if err := spec.Health.SetPaused(ctx, reason, lastErr); err != nil {
		specLogger(spec).Error("substrate: ConsumerSupervisor: health set paused",
			"consumer", spec.Name, "reason", reason, "error", err)
	}
}

// effectiveProbeInterval / effectiveRedeliveryDelay resolve the spec's tunables
// against their package defaults.
func effectiveProbeInterval(spec ConsumerSpec) time.Duration {
	if spec.ProbeInterval > 0 {
		return spec.ProbeInterval
	}
	return DefaultProbeInterval
}
