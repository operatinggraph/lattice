package substrate

import (
	"context"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// reopenBackoff is the short delay before re-opening a consumer iterator after a
// transient open failure (e.g. the brief window of an in-flight Reset).
const reopenBackoff = 100 * time.Millisecond

// pumpState is the live pause/probe/reopen machinery for one supervised
// consumer. Pause reasons are tracked as a composable SET: probe-success clears
// ONLY the infra reason; an operator Resume clears manual + structural and
// force-exits an in-flight probe loop; the pump runs only when the set is empty.
type pumpState struct {
	mu      sync.Mutex
	reasons map[PauseReason]struct{}
	spec    ConsumerSpec

	// resumeCh is signalled when a structural/manual pause should re-evaluate
	// (an operator Resume). Buffered so a Resume that races the select is not lost.
	resumeCh chan struct{}
	// forceResumeCh overrides an in-flight infra probe loop (operator Resume).
	forceResumeCh chan struct{}
	// pauseTrigger interrupts a running drain so the pump re-checks its reason
	// set promptly (a manual Pause arriving mid-drain).
	pauseTrigger chan struct{}
	// reopenTrigger signals the pump to drop its current iterator and re-open
	// against a recreated durable (Reset).
	reopenTrigger chan struct{}
}

func newPumpState() *pumpState {
	return &pumpState{
		reasons:       make(map[PauseReason]struct{}),
		resumeCh:      make(chan struct{}, 1),
		forceResumeCh: make(chan struct{}, 1),
		pauseTrigger:  make(chan struct{}, 1),
		reopenTrigger: make(chan struct{}, 1),
	}
}

func (st *pumpState) updateSpec(spec ConsumerSpec) {
	st.mu.Lock()
	st.spec = spec
	st.mu.Unlock()
}

func (st *pumpState) currentSpec(fallback ConsumerSpec) ConsumerSpec {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.spec.Handler != nil {
		return st.spec
	}
	return fallback
}

// addReason adds reason to the pause set and, for a manual pause, interrupts a
// running drain so the pump halts promptly.
func (st *pumpState) addReason(reason PauseReason) {
	st.mu.Lock()
	st.reasons[reason] = struct{}{}
	st.mu.Unlock()
	if reason == PauseManual {
		nonBlockingSend(st.pauseTrigger)
	}
}

func (st *pumpState) clearReason(reason PauseReason) {
	st.mu.Lock()
	delete(st.reasons, reason)
	st.mu.Unlock()
}

// operatorResume clears manual + structural reasons, wakes a blocked
// structural/manual select, and force-exits an in-flight infra probe loop.
func (st *pumpState) operatorResume() {
	st.mu.Lock()
	delete(st.reasons, PauseManual)
	delete(st.reasons, PauseStructural)
	st.mu.Unlock()
	nonBlockingSend(st.resumeCh)
	nonBlockingSend(st.forceResumeCh)
}

func (st *pumpState) requestReopen() { nonBlockingSend(st.reopenTrigger) }

func (st *pumpState) hasReason(reason PauseReason) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.reasons[reason]
	return ok
}

func (st *pumpState) anyReason() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.reasons) != 0
}

// dominantReason returns the highest operator-relevance reason currently held,
// for HealthSink persistence (manual > structural > infra). Refractor's health
// Entry persists one reason at a time; this defines the composable machine's
// tie-break. The lost lower-precedence reason re-presents on the next pump
// failure (self-healing — e.g. a dropped infra pause re-enters the probe loop
// on the next failing message).
func (st *pumpState) dominantReason() (PauseReason, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.reasons[PauseManual]; ok {
		return PauseManual, true
	}
	if _, ok := st.reasons[PauseStructural]; ok {
		return PauseStructural, true
	}
	if _, ok := st.reasons[PauseInfra]; ok {
		return PauseInfra, true
	}
	return "", false
}

func nonBlockingSend(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func drainSignal(ch chan struct{}) {
	select {
	case <-ch:
	default:
	}
}

// runPump is the supervised pump for one consumer: restore persisted state →
// loop { wait-while-paused → open iterator → drain → classify failure →
// pause/probe/resume }. It generalises pipeline.Run's skeleton.
func (s *ConsumerSupervisor) runPump(ctx context.Context, spec ConsumerSpec, st *pumpState) {
	st.updateSpec(spec)
	logger := specLogger(spec)

	if done := s.restoreState(ctx, spec, st); done {
		return
	}

	for {
		if ctx.Err() != nil {
			return
		}

		// Block while any pause reason is held (structural/manual await Resume;
		// infra is handled by the probe loop after a drain failure).
		if st.anyReason() {
			if done := s.waitWhilePaused(ctx, spec, st); done {
				return
			}
			continue
		}

		cons, err := s.conn.js.Consumer(ctx, spec.Stream, spec.Name)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// A not-found here is usually the brief window of an in-flight Reset
			// (delete-then-recreate); retry on a short backoff so the pump picks
			// up the recreated durable promptly.
			logger.Warn("substrate: ConsumerSupervisor: open consumer, retrying",
				"consumer", spec.Name, "error", err)
			if waitOrDone(ctx, reopenBackoff) {
				return
			}
			continue
		}

		mc, err := cons.Messages(jetstream.PullHeartbeat(5 * time.Second))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("substrate: ConsumerSupervisor: open messages iterator",
				"consumer", spec.Name, "error", err)
			if waitOrDone(ctx, reopenBackoff) {
				return
			}
			continue
		}

		class, drainErr := s.drain(ctx, spec, st, mc)
		s.handleDrainOutcome(ctx, spec, st, class, drainErr)
	}
}

// waitWhilePaused blocks until an operator Resume clears the structural/manual
// reasons or ctx is done. Returns true when the pump should exit (ctx cancelled).
// Infra-only pauses are not handled here — they run the probe loop after a drain.
func (s *ConsumerSupervisor) waitWhilePaused(ctx context.Context, spec ConsumerSpec, st *pumpState) bool {
	// If the only reason is infra, run the probe loop directly (e.g. restored
	// infra pause with no failing message yet).
	if st.hasReason(PauseInfra) && !st.hasReason(PauseManual) && !st.hasReason(PauseStructural) {
		return s.runProbeLoop(ctx, spec, st)
	}
	drainSignal(st.resumeCh)
	select {
	case <-ctx.Done():
		return true
	case <-st.resumeCh:
		if !st.anyReason() {
			s.persistActive(context.WithoutCancel(ctx), spec)
		}
		return false
	}
}

// drain reads and processes messages until ctx is done, the iterator errors, a
// manual pause is triggered, a Reset reopen is requested, or the handler signals
// an infra/structural failure. Returns the failure class + error to drive the
// pause decision (ClassTransient/nil on a clean reconnect or pause/reopen).
func (s *ConsumerSupervisor) drain(ctx context.Context, spec ConsumerSpec, st *pumpState, mc jetstream.MessagesContext) (FailureClass, error) {
	stopCtx, stopDone := context.WithCancel(ctx)
	defer stopDone()
	go func() {
		select {
		case <-stopCtx.Done():
			mc.Stop()
		case <-st.pauseTrigger:
			mc.Stop()
		case <-st.reopenTrigger:
			mc.Stop()
		}
	}()

	for {
		// A manual pause requested mid-drain: stop and let the pump re-check.
		if st.hasReason(PauseManual) {
			return ClassTransient, nil
		}

		msg, err := mc.Next()
		if err != nil {
			if ctx.Err() != nil {
				return ClassTransient, nil
			}
			return ClassTransient, err
		}

		current := st.currentSpec(spec)
		class, herr, disposed := s.processMsg(ctx, current, msg)
		if !disposed {
			// Infra/Structural: message left un-acked for redelivery on resume.
			return class, herr
		}
	}
}

// processMsg invokes the spec's handler and applies its verdict. Returns the
// FailureClass, the handler error (when infra/structural), and whether the
// message was disposed (acked/naked). A false "disposed" means an
// infra/structural failure: the message is intentionally left pending and the
// caller must pause.
func (s *ConsumerSupervisor) processMsg(ctx context.Context, spec ConsumerSpec, msg jetstream.Msg) (FailureClass, error, bool) {
	decision, herr := spec.Handler(ctx, newMessage(msg))
	if herr != nil {
		class := classify(spec, herr)
		if class == ClassInfra || class == ClassStructural {
			return class, herr, false
		}
		// Transient/Terminal handler error: fall back to the returned Decision.
	}
	applyDecision(decision, msg, spec.Name, spec.RedeliveryDelay, specLogger(spec))
	return ClassTransient, nil, true
}

// handleDrainOutcome maps a drain result to the pause state machine. On infra it
// pauses + probes; on structural it pauses awaiting Resume; otherwise it loops.
func (s *ConsumerSupervisor) handleDrainOutcome(ctx context.Context, spec ConsumerSpec, st *pumpState, class FailureClass, drainErr error) {
	logger := specLogger(spec)
	switch class {
	case ClassInfra:
		st.addReason(PauseInfra)
		logger.Warn("substrate: ConsumerSupervisor: infra failure, pausing",
			"consumer", spec.Name, "error", drainErr)
		s.persistDominant(ctx, spec, st, errString(drainErr))
	case ClassStructural:
		st.addReason(PauseStructural)
		logger.Error("substrate: ConsumerSupervisor: structural failure, pausing until resume",
			"consumer", spec.Name, "error", drainErr)
		s.persistDominant(ctx, spec, st, errString(drainErr))
	default:
		// Transient reconnect, manual pause, or reopen: drain stale triggers and
		// loop. The pump's top re-checks the reason set.
		drainSignal(st.reopenTrigger)
		drainSignal(st.pauseTrigger)
	}
}

// runProbeLoop polls the spec's Probe hook at the configured interval until it
// passes (clears the infra reason), an operator Resume force-exits, ctx is
// cancelled, or a probe error classified Structural escalates the pause. Returns
// true when the pump should exit (ctx cancelled).
func (s *ConsumerSupervisor) runProbeLoop(ctx context.Context, spec ConsumerSpec, st *pumpState) bool {
	logger := specLogger(spec)
	drainSignal(st.forceResumeCh)
	interval := effectiveProbeInterval(spec)
	logger.Info("substrate: ConsumerSupervisor: entering probe loop", "consumer", spec.Name)
	for {
		select {
		case <-ctx.Done():
			return true
		case <-st.forceResumeCh:
			logger.Info("substrate: ConsumerSupervisor: probe loop overridden by resume",
				"consumer", spec.Name)
			st.clearReason(PauseInfra)
			if !st.anyReason() {
				s.persistActive(context.WithoutCancel(ctx), spec)
			}
			return false
		case <-time.After(interval):
			if spec.Probe == nil {
				continue
			}
			err := spec.Probe(ctx)
			if err == nil {
				logger.Info("substrate: ConsumerSupervisor: dependency recovered, resuming",
					"consumer", spec.Name)
				st.clearReason(PauseInfra)
				if !st.anyReason() {
					s.persistActive(context.WithoutCancel(ctx), spec)
				}
				return false
			}
			if classify(spec, err) == ClassStructural {
				logger.Error("substrate: ConsumerSupervisor: structural error during probe, escalating",
					"consumer", spec.Name, "error", err)
				st.clearReason(PauseInfra)
				st.addReason(PauseStructural)
				s.persistDominant(context.WithoutCancel(ctx), spec, st, errString(err))
				return false
			}
			logger.Warn("substrate: ConsumerSupervisor: dependency not yet available, probing again",
				"consumer", spec.Name)
		}
	}
}

// restoreState reads the spec's HealthSink at startup and enters the matching
// state, generalising pipeline.restoreHealthState. Returns true when the pump
// should exit immediately (ctx cancelled during restore).
func (s *ConsumerSupervisor) restoreState(ctx context.Context, spec ConsumerSpec, st *pumpState) bool {
	if spec.Health == nil {
		return false
	}
	status, reason, err := spec.Health.Load(ctx)
	if err != nil {
		specLogger(spec).Warn("substrate: ConsumerSupervisor: health load failed, assuming active",
			"consumer", spec.Name, "error", err)
		return false
	}
	if status != StatusPaused {
		return false
	}
	switch reason {
	case PauseInfra:
		st.addReason(PauseInfra)
		return s.runProbeLoop(ctx, spec, st)
	case PauseStructural:
		st.addReason(PauseStructural)
		return s.waitWhilePaused(ctx, spec, st)
	case PauseManual:
		st.addReason(PauseManual)
		return s.waitWhilePaused(ctx, spec, st)
	default:
		specLogger(spec).Warn("substrate: ConsumerSupervisor: unrecognised pause reason, assuming active",
			"consumer", spec.Name, "reason", reason)
		return false
	}
}

func (s *ConsumerSupervisor) persistDominant(ctx context.Context, spec ConsumerSpec, st *pumpState, lastErr string) {
	reason, ok := st.dominantReason()
	if !ok {
		s.persistActive(ctx, spec)
		return
	}
	s.persistPaused(ctx, spec, reason, lastErr)
}

func classify(spec ConsumerSpec, err error) FailureClass {
	if spec.Classify == nil {
		return ClassTransient
	}
	return spec.Classify(err)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func waitOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}
