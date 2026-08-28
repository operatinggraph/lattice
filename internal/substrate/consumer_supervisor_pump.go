package substrate

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// reopenBackoff is the base delay before re-opening a consumer iterator after
// a transient open failure (e.g. the brief window of an in-flight Reset).
// pumpState.nextReopenDelay grows this exponentially — capped at
// reopenBackoffCap, with jitter — across consecutive failures on the same
// consumer, so a sustained outage backs off instead of retrying at a fixed
// interval indefinitely.
const reopenBackoff = 100 * time.Millisecond

// reopenBackoffCap bounds that growth: a sustained outage settles into a
// slow, steady retry rate instead of climbing forever — important when many
// consumers on one connection are retrying concurrently, where a fixed short
// interval multiplies into sustained heavy load on the server they're all
// trying to reach.
const reopenBackoffCap = 5 * time.Second

// pumpPullMaxMessages is how many messages one worker's pull iterator may hold.
//
// A worker's drain loop is strictly serial: it takes one message from the
// iterator, runs the handler to completion, applies the ack, and only then asks
// for the next. Anything the iterator buffers beyond that one message is work
// the worker has not started — but JetStream starts the AckWait clock at
// DELIVERY, so a buffered message is burning its window while the worker is N
// messages away from looking at it. With prefetch N and handler latency L, the
// back of the buffer waits N*L; once N*L exceeds AckWait the server redelivers
// it into the same buffer and the worker owes strictly more work than arrived.
// The ack floor then freezes, redeliveries climb, and — because nothing errors —
// no pause is entered and no signal is published.
//
// L is unbounded for a projection handler, so no static value above 1 satisfies
// "never hold a message you cannot start within AckWait". One per worker makes
// forward progress a property of the pump rather than of handler speed, and it
// is also the fairest split across a fan-out lane's workers (the pull-consumer
// equivalent of a queue group). The cost is that nats.go issues its next pull
// from inside Next, so the round trip no longer overlaps the handler — a
// loopback request/reply against work that is a Starlark execution or a
// full-engine projection.
const pumpPullMaxMessages = 1

// messagesOpts builds the pull-iterator options for a worker: a heartbeat, and a
// prefetch bounded to what a serial worker can honor within AckWait.
func messagesOpts() []jetstream.PullMessagesOpt {
	return []jetstream.PullMessagesOpt{
		jetstream.PullHeartbeat(5 * time.Second),
		jetstream.PullMaxMessages(pumpPullMaxMessages),
	}
}

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
	// reopenedCh is the acknowledgement to reopenTrigger's request: the pump
	// CLOSES it once the consumer AND its messages iterator are both open, and
	// installs a fresh one in the same critical section. A waiter snapshots the
	// current channel and waits for that close.
	//
	// A closed channel rather than a delivered value, because the
	// acknowledgement has an unbounded audience. There is no bound on how many
	// callers may reset one consumer at once — the reset paths do not exclude
	// each other — and a signal that can be RECEIVED is a signal one waiter can
	// take from another: with a token, two overlapping waiters both arm, the
	// pump reopens once, one of them consumes the token and the other waits out
	// its whole budget and reports a pump that had in fact reopened. A close
	// releases every holder of that snapshot at once and cannot be stolen.
	//
	// Guarded by mu, like every other field here, because the close and the
	// replacement must be one step: a waiter that read the field between them
	// would hold a channel nothing will ever close.
	reopenedCh chan struct{}

	// reopenFailures counts consecutive open failures since the last
	// successful open, driving nextReopenDelay's exponential growth. Reset to
	// 0 once the consumer and its message iterator both open successfully.
	reopenFailures int

	// probeResumedStructural marks that the structural reason this worker last
	// cleared was cleared by a PASSING PROBE rather than by an operator. It arms
	// the relapse counter: the next structural pause is then a self-heal that did
	// not hold, not a fresh fault. Consumed by that pause (noteStructuralPause),
	// and cleared by an operator Resume.
	probeResumedStructural bool

	// structuralRelapses counts consecutive structural pauses that each followed
	// a probe-driven structural resume. A structural pause that did NOT follow one
	// starts a new chain and resets it to 0; an operator Resume resets it too. It
	// is NOT reset by a successful drain: a probe-driven resume that projects for
	// an hour and then hits a different structural fault still counts, which bounds
	// self-healing slightly sooner than a per-episode count would — the direction
	// that hands the operator control, never the one that withholds it.
	structuralRelapses int

	// structuralLatched makes this worker treat ConsumerSpec.StructuralProbe as
	// false for the rest of its life: the pause is operator-only again, because
	// the probe has now adjudicated recovery structuralRelapseLimit times and been
	// wrong every time. It is in-process and per-worker, never persisted, so a
	// restart re-arms it — deliberately: a restart is a deploy or an operator act,
	// which bounds self-healing at structuralRelapseLimit per restart, and the
	// state whose whole purpose is to expire with the process has no business in a
	// health schema. Cleared by an operator Resume.
	structuralLatched bool

	// pendingAutoRecovered records that a probe-driven structural clear has not
	// yet been announced to the operator: a structural pause that healed itself is
	// exactly the event a health entry reading "active" would otherwise hide. The
	// pump takes it immediately before its next drain, where every gate it had to
	// pass — the structural probe AND any InitialPause re-verification — has
	// cleared and it is about to project. It is cleared without announcement by an
	// operator Resume (the human already knows) and by a latch (the recovery did
	// not hold, so there is none to announce).
	//
	// autoRecoveredCause and autoRecoveredAttempt are captured WITH it, at the
	// clear, so the announcement carries the cause the pause actually held and the
	// attempt that lifted it rather than whatever the state has drifted to by the
	// time the pump reaches its next open.
	pendingAutoRecovered bool
	autoRecoveredCause   string
	autoRecoveredAttempt int

	// lastStructuralCause is the cause currently persisted for a held structural
	// pause, empty when none is held. It exists so a probe that keeps reporting an
	// unchanged fault does not rewrite the health entry every ProbeInterval: every
	// setter stamps a fresh timestamp, so re-persisting an unchanged cause would
	// make a permanently dead consumer publish a permanently fresh entry. It is
	// set at each structural persist, cleared whenever the structural reason
	// clears, and read at the clear to fill autoRecoveredCause.
	lastStructuralCause string

	// resumeEpoch counts operator Resumes. A Probe is a round trip — tens of
	// milliseconds of catalog queries for a protected lens — and it runs with no
	// lock held, so an operator can lift the pause while the probe that is
	// adjudicating it is still in flight. The verdict that comes back is then
	// about a pause nobody holds any more: applying it would credit the platform
	// with a recovery a human performed, and would re-arm the relapse counter
	// across the very Resume that is supposed to clear it. The probe arm samples
	// this before calling Probe and drops its verdict if it has moved.
	resumeEpoch uint64
}

// structuralRelapseLimit is how many probe-driven structural resumes may fail to
// hold before a worker latches into operator-only pausing. With the failing
// message Nak'd one probe interval out (processMsg), each attempt costs about a
// probe interval, so the bound is seconds of churn rather than an unbounded
// resume/re-pause cycle — and the operator gets both the cause and the fact that
// the platform tried.
const structuralRelapseLimit = 3

func newPumpState() *pumpState {
	return &pumpState{
		reasons:       make(map[PauseReason]struct{}),
		resumeCh:      make(chan struct{}, 1),
		forceResumeCh: make(chan struct{}, 1),
		pauseTrigger:  make(chan struct{}, 1),
		reopenTrigger: make(chan struct{}, 1),
		reopenedCh:    make(chan struct{}),
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
//
// It also returns the worker's structural self-healing to its starting state:
// the latch, the relapse count, the arming flag and the un-announced recovery
// all clear together. An operator Resume is a human asserting the condition is
// fixed, so it earns a full fresh set of attempts — and a latch that survived
// one would be unclearable short of a restart.
func (st *pumpState) operatorResume() {
	st.mu.Lock()
	delete(st.reasons, PauseManual)
	delete(st.reasons, PauseStructural)
	st.structuralLatched = false
	st.structuralRelapses = 0
	st.probeResumedStructural = false
	st.pendingAutoRecovered = false
	st.lastStructuralCause = ""
	st.resumeEpoch++
	st.mu.Unlock()
	nonBlockingSend(st.resumeCh)
	nonBlockingSend(st.forceResumeCh)
}

// currentEpoch samples the operator-resume generation. A probe arm takes it
// BEFORE calling Probe, so the verdict it applies afterwards can be checked
// against the state the pause was in when the question was asked.
func (st *pumpState) currentEpoch() uint64 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.resumeEpoch
}

// probePassed applies a passing probe's verdict in ONE critical section: it
// clears the reason the loop was adjudicating and, for a structural pause,
// captures the recovery (its cause, and which self-heal attempt lifted it) for
// the pump to announce before it next drains.
//
// It is a no-op returning false when an operator Resume landed while the probe
// was in flight. That verdict answers a question about a pause the operator has
// already lifted: applying it would stamp the health entry "recovered without
// operator action" for a recovery a human performed, and would leave the relapse
// counter armed across the Resume that exists to disarm it.
func (st *pumpState) probePassed(reason PauseReason, epoch uint64) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.resumeEpoch != epoch {
		return false
	}
	delete(st.reasons, reason)
	if reason == PauseStructural {
		st.probeResumedStructural = true
		st.pendingAutoRecovered = true
		st.autoRecoveredCause = st.lastStructuralCause
		// The counter holds the relapses BEFORE this attempt, so the first
		// self-heal of a chain is attempt 1.
		st.autoRecoveredAttempt = st.structuralRelapses + 1
		st.lastStructuralCause = ""
	}
	return true
}

// recordStructuralCause remembers the cause persisted for the structural pause
// now held and reports whether it differs from the one already recorded — the
// test a repeating probe uses to decide whether it has anything new to say.
func (st *pumpState) recordStructuralCause(cause string) (changed bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.lastStructuralCause == cause {
		return false
	}
	st.lastStructuralCause = cause
	return true
}

// forgetStructuralCause drops the recorded cause when the structural reason
// clears, so the same fault returning later is persisted afresh rather than
// mistaken for the one already on the entry.
func (st *pumpState) forgetStructuralCause() {
	st.mu.Lock()
	st.lastStructuralCause = ""
	st.mu.Unlock()
}

// enterStructuralPause holds the structural reason and records it against the
// relapse machinery, reporting whether the worker is now latched and on which
// attempt. Adding the reason and deciding the latch are ONE critical section: an
// operator Resume landing between them would clear a latch that the caller then
// persists anyway, telling the operator no further attempt will be made on a
// worker whose attempts they had just reset.
func (st *pumpState) enterStructuralPause() (latched bool, attempts int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.probeResumedStructural {
		st.probeResumedStructural = false
		st.structuralRelapses++
	} else {
		// A structural pause that no self-heal preceded starts a new chain.
		st.structuralRelapses = 0
	}
	if st.structuralRelapses >= structuralRelapseLimit {
		st.structuralLatched = true
	}
	st.reasons[PauseStructural] = struct{}{}
	return st.structuralLatched, st.structuralRelapses
}

// structuralProbeAllowed reports whether a structural pause on this worker may
// probe its own way out: the spec must opt in and supply the Probe that
// adjudicates the condition, and the worker must not have latched.
func (st *pumpState) structuralProbeAllowed(spec ConsumerSpec) bool {
	if !spec.StructuralProbe || spec.Probe == nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return !st.structuralLatched
}

// peekPendingAutoRecovered returns the probe-driven structural clear waiting to
// be announced WITHOUT consuming it: the announcement can fail, and a recovery
// dropped because the health store was briefly unreachable — plausibly the same
// blip that paused the consumer — is exactly the silent self-heal the
// announcement exists to prevent.
func (st *pumpState) peekPendingAutoRecovered() (cause string, attempt int, pending bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.pendingAutoRecovered {
		return "", 0, false
	}
	return st.autoRecoveredCause, st.autoRecoveredAttempt, true
}

// clearPendingAutoRecovered retires the pending recovery once it has been
// delivered — or once it is established that nothing can receive it.
func (st *pumpState) clearPendingAutoRecovered() {
	st.mu.Lock()
	st.pendingAutoRecovered = false
	st.autoRecoveredCause, st.autoRecoveredAttempt = "", 0
	st.mu.Unlock()
}

// latchedCause prefixes a structural pause's cause with the fact that the
// platform tried to heal it and failed, so the operator reading the health
// entry gets the diagnosis AND the reason no further attempt will be made.
func latchedCause(attempts int, cause string) string {
	return fmt.Sprintf("structural pause latched after %d self-heal attempts: %s", attempts, cause)
}

func (st *pumpState) requestReopen() { nonBlockingSend(st.reopenTrigger) }

// reopenSnapshot returns the channel the NEXT successful open will close. A
// waiter takes this BEFORE requesting the reopen it intends to wait for; taking
// it afterwards could hand back a channel installed by an open that had already
// happened, which nothing further would close.
func (st *pumpState) reopenSnapshot() <-chan struct{} {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.reopenedCh
}

// signalReopened announces that this pump's consumer and messages iterator are
// both open: it releases every waiter holding the current snapshot and installs
// the channel the next open will close.
//
// Called on every successful open, not only after a reset. The pump has no way
// to tell which opens someone is waiting on, and an announcement nobody is
// waiting for costs one channel allocation.
func (st *pumpState) signalReopened() {
	st.mu.Lock()
	released := st.reopenedCh
	st.reopenedCh = make(chan struct{})
	st.mu.Unlock()
	close(released)
}

// nextReopenDelay returns the delay before the next open attempt and records
// the attempt: reopenBackoff on the first failure since the last successful
// open, doubling on each consecutive failure up to reopenBackoffCap, with
// full jitter (uniform over [0, delay]) so consumers that failed together —
// e.g. every consumer on one connection, after a single connection drop —
// spread out across the window instead of retrying in lockstep.
func (st *pumpState) nextReopenDelay() time.Duration {
	st.mu.Lock()
	shift := st.reopenFailures
	st.reopenFailures++
	st.mu.Unlock()

	const maxShift = 6 // reopenBackoff<<6 = 6.4s already exceeds reopenBackoffCap
	if shift > maxShift {
		shift = maxShift
	}
	d := reopenBackoff * (1 << uint(shift))
	if d > reopenBackoffCap {
		d = reopenBackoffCap
	}
	return time.Duration(rand.Int64N(int64(d) + 1))
}

// resetReopenBackoff clears the consecutive-failure count once the consumer
// and its message iterator are open again.
func (st *pumpState) resetReopenBackoff() {
	st.mu.Lock()
	st.reopenFailures = 0
	st.mu.Unlock()
}

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

	// A fresh start (no persisted paused state) with a non-empty InitialPause
	// seeds the pause set before the first drain, so an InitialPause: PauseInfra
	// pump enters the probe loop and projects nothing until Probe passes — the
	// fail-closed precondition gate. restoreState ran first, so a persisted pause
	// (a restart) already populated the reason set and takes precedence here.
	if spec.InitialPause != "" && !st.anyReason() {
		st.addReason(spec.InitialPause)
		s.persistDominant(context.WithoutCancel(ctx), spec, st, "")
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
			// up the recreated durable promptly. A sustained failure (e.g. NATS
			// itself unreachable) grows that backoff instead of retrying at a
			// fixed interval indefinitely.
			logger.Warn("substrate: ConsumerSupervisor: open consumer, retrying",
				"consumer", spec.Name, "error", err)
			if waitOrDone(ctx, st.nextReopenDelay()) {
				return
			}
			continue
		}

		mc, err := cons.Messages(messagesOpts()...)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("substrate: ConsumerSupervisor: open messages iterator",
				"consumer", spec.Name, "error", err)
			if waitOrDone(ctx, st.nextReopenDelay()) {
				return
			}
			continue
		}

		// Both halves of the open have succeeded — the consumer handle AND the
		// messages iterator. Announcing the reopen any earlier would be a lie a
		// waiter cannot detect: a consumer that opened and an iterator that then
		// failed goes back round this loop on nextReopenDelay, and a waiter
		// released at the first half would have stopped waiting for a transition
		// that had not happened.
		st.resetReopenBackoff()
		st.signalReopened()
		s.announceStructuralRecovery(ctx, spec, st)
		class, drainErr := s.drain(ctx, spec, st, mc)
		s.handleDrainOutcome(ctx, spec, st, class, drainErr)
	}
}

// waitWhilePaused blocks until an operator Resume clears the structural/manual
// reasons or ctx is done. Returns true when the pump should exit (ctx cancelled).
// Infra-only pauses are not handled here — they run the probe loop after a drain.
//
// A structural pause is the one reason whose route depends on the spec: a
// consumer whose Probe adjudicates the structural condition (StructuralProbe)
// probes its own way out, bounded by the relapse latch; every other consumer
// waits for an operator, which is the default.
func (s *ConsumerSupervisor) waitWhilePaused(ctx context.Context, spec ConsumerSpec, st *pumpState) bool {
	// If the only reason is infra, run the probe loop directly (e.g. restored
	// infra pause with no failing message yet).
	if st.hasReason(PauseInfra) && !st.hasReason(PauseManual) && !st.hasReason(PauseStructural) {
		return s.runProbeLoop(ctx, spec, st, PauseInfra)
	}
	// A manual pause is an operator holding the pump down and outranks any
	// verdict a probe could reach, so the whole reason set is tested here, not
	// just the structural bit.
	if st.hasReason(PauseStructural) && !st.hasReason(PauseManual) && st.structuralProbeAllowed(spec) {
		return s.runProbeLoop(ctx, spec, st, PauseStructural)
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

// announceStructuralRecovery tells the operator that a structural pause cleared
// because the consumer's own Probe adjudicated the condition, with nobody
// involved. It runs immediately before a drain, which is the only point that is
// after EVERY gate the pump had to pass: a pump that clears a restored
// structural pause falls back through runPump's InitialPause seeding and
// re-verifies its precondition before its first projection, so announcing at the
// structural clear would tell the operator something the pump does not yet know.
//
// A sink that does not implement StructuralRecoveryAnnouncer is told nothing.
//
// The recovery is retired only once it has been delivered. An announcement that
// fails — the health store briefly unreachable, plausibly the same blip that
// paused the consumer — stays armed and is retried at the pump's NEXT open: a
// reconnect, a Reset, or the next pause and resume. There is no timer, because
// the pump has no other rendezvous and a second scheduler for one health write
// would cost more than it is worth; the alternative, dropping it, leaves an
// auth-plane lens self-healing with nothing but a log line behind it.
// Announcement errors are logged and never fatal, like every other sink call.
func (s *ConsumerSupervisor) announceStructuralRecovery(ctx context.Context, spec ConsumerSpec, st *pumpState) {
	cause, attempt, pending := st.peekPendingAutoRecovered()
	if !pending {
		return
	}
	logger := specLogger(spec)
	if announcer, ok := spec.Health.(StructuralRecoveryAnnouncer); ok {
		if err := announcer.RecordStructuralAutoRecovery(ctx, cause, attempt); err != nil {
			logger.Error("substrate: ConsumerSupervisor: health record structural auto-recovery, retrying at the next open",
				"consumer", spec.Name, "error", err)
			return
		}
	}
	st.clearPendingAutoRecovered()
	logger.Info("substrate: ConsumerSupervisor: structural pause recovered without operator action",
		"consumer", spec.Name, "attempt", attempt, "cause", cause)
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
		class, herr, keepDraining := s.processMsg(ctx, current, st, msg)
		if !keepDraining {
			// Infra/Structural: message un-acked, redelivered on resume.
			return class, herr
		}
	}
}

// processMsg invokes the spec's handler and applies its verdict. Returns the
// FailureClass, the handler error (when infra/structural), and whether the
// drain may continue. A false "continue" means an infra/structural failure and
// the caller must pause; the message is left pending for redelivery on resume,
// except for the structural failure of a consumer that will probe its own way
// out, which is Nak'd with a delay (below) — still un-acked, but asked for
// sooner.
func (s *ConsumerSupervisor) processMsg(ctx context.Context, spec ConsumerSpec, st *pumpState, msg jetstream.Msg) (FailureClass, error, bool) {
	stopHeartbeat := keepAckAlive(msg, spec)
	decision, herr := spec.Handler(ctx, newMessage(msg))
	stopHeartbeat()
	if herr != nil {
		class := classify(spec, herr)
		if class == ClassInfra || class == ClassStructural {
			// One predicate for "this consumer heals itself", shared with
			// waitWhilePaused: bringing the retest forward is only useful for a
			// pump that is going to retest, and a latched worker (or one with no
			// Probe) waits for an operator with the message simply pending.
			if class == ClassStructural && st.structuralProbeAllowed(spec) {
				// This pump is about to probe its own way out of the pause, and
				// this message is the only thing that can test whether the
				// recovery actually took. Left pending it returns only when
				// AckWait expires — five minutes for a lens — so the health
				// entry would read "active" for that whole window while the
				// consumer is still broken. A Nak carrying a delay does not ack
				// and does not advance the ack floor; it asks the server for
				// redelivery one probe interval out, and the pump is paused
				// until then, so nothing is consumed early. (An undelayed Nak
				// would ignore AckWait and redeliver instantly —
				// nats.go@v1.52.0 jetstream/message.go:60-70.)
				//
				// KNOWN WINDOW, accepted: stopHeartbeat closes the heartbeat's
				// done channel but does not wait for the goroutine, so a +WPI
				// already past its select can still land AFTER this Nak. The
				// server implements the delay by BACKDATING the pending entry to
				// now-AckWait+delay (nats-server@v2.14.0 server/consumer.go:
				// 3173-3176), and progressUpdate re-stamps that entry to
				// time.Now() (:2762-2771) — so a straggler erases the backdate
				// and redelivery reverts to the full AckWait. The window is one
				// scheduling gap wide and only reachable for a handler whose
				// latency exceeds AckWait/2; closing it means making
				// stopHeartbeat synchronous, which changes the ack path of every
				// consumer in the system for a slow-path optimisation.
				// The Decision here is always NakWithDelay, never
				// NakWithLongDelay, so the long floor is inert on this call —
				// but applyDecision still reads it against a live config
				// field rather than a bare zero, which would read as "no long
				// floor configured" to a future caller of this function.
				applyDecision(NakWithDelay, msg, spec.Name, effectiveProbeInterval(spec), spec.LongRedeliveryDelay, specLogger(spec))
			}
			return class, herr, false
		}
		// Transient/Terminal handler error: fall back to the returned Decision.
	}
	applyDecision(decision, msg, spec.Name, spec.RedeliveryDelay, spec.LongRedeliveryDelay, specLogger(spec))
	return ClassTransient, nil, true
}

// jetStreamDefaultAckWait is JetStream's AckWait when a consumer does not set
// one. It is not exported by nats.go, so the effective window a spec with no
// AckWait runs under has to be named here to derive a heartbeat interval from.
const jetStreamDefaultAckWait = 30 * time.Second

// keepAckAlive holds the in-flight message's ack window open for as long as the
// handler runs, and returns the function that releases it.
//
// Bounding the prefetch (pumpPullMaxMessages) stops UN-STARTED work from aging
// out; this stops STARTED work from aging out. Without it a handler slower than
// AckWait loses its own window and the server redelivers the message underneath
// it — bounded duplicate work, but forever, for any handler whose latency sits
// above the wait. A projection handler's latency is unbounded by design, so the
// pump must not let AckWait bound it.
//
// InProgress sends +WPI, which is the one ack type that does NOT mark the
// message acked, so it may be sent repeatedly; a heartbeat that races the
// handler's own Ack fails with ErrMsgAlreadyAckd and is dropped. The returned
// stop is idempotent and must be called before the decision is applied.
func keepAckAlive(msg jetstream.Msg, spec ConsumerSpec) func() {
	wait := spec.AckWait
	if wait <= 0 {
		wait = jetStreamDefaultAckWait
	}
	// Half the window: one missed tick still leaves a full interval of margin.
	interval := wait / 2
	if interval <= 0 {
		return func() {}
	}

	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := msg.InProgress(); err != nil {
					// The message is already disposed, or the connection is
					// gone; either way there is no window left to hold open.
					return
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
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
		latched, attempts := st.enterStructuralPause()
		if latched {
			logger.Error("substrate: ConsumerSupervisor: structural failure latched, pausing until resume",
				"consumer", spec.Name, "attempts", attempts, "error", drainErr)
		} else {
			logger.Error("substrate: ConsumerSupervisor: structural failure, pausing until resume",
				"consumer", spec.Name, "error", drainErr)
		}
		// A structural pause is always published on entry — the status transition
		// is itself the news — and its cause is recorded so a probe repeating that
		// same fault has nothing to rewrite.
		cause := structuralCause(latched, attempts, drainErr)
		st.recordStructuralCause(cause)
		s.persistDominant(ctx, spec, st, cause)
	default:
		// Transient reconnect, manual pause, or reopen: drain stale triggers and
		// loop. The pump's top re-checks the reason set.
		drainSignal(st.reopenTrigger)
		drainSignal(st.pauseTrigger)
	}
}

// runProbeLoop polls the spec's Probe hook at the configured interval until it
// passes (clearing the reason it is probing), an operator Resume force-exits, ctx
// is cancelled, or a probe error classified Structural escalates the pause.
// Returns true when the pump should exit (ctx cancelled).
//
// reason is the pause the loop is adjudicating — PauseInfra for a dependency
// that is expected back, PauseStructural for a consumer whose Probe verifies the
// very condition its Classify calls structural. It is the reason a passing probe
// clears; every other held reason survives the loop.
func (s *ConsumerSupervisor) runProbeLoop(ctx context.Context, spec ConsumerSpec, st *pumpState, reason PauseReason) bool {
	logger := specLogger(spec)
	drainSignal(st.forceResumeCh)
	interval := effectiveProbeInterval(spec)
	logger.Info("substrate: ConsumerSupervisor: entering probe loop", "consumer", spec.Name, "reason", reason)
	for {
		select {
		case <-ctx.Done():
			return true
		case <-st.forceResumeCh:
			logger.Info("substrate: ConsumerSupervisor: probe loop overridden by resume",
				"consumer", spec.Name)
			st.clearReason(reason)
			st.forgetStructuralCause()
			if !st.anyReason() {
				s.persistActive(context.WithoutCancel(ctx), spec)
			}
			return false
		case <-time.After(interval):
			if spec.Probe == nil {
				continue
			}
			// Sampled BEFORE the probe: a Probe is a round trip, and the pause it
			// is adjudicating can be lifted by an operator while it runs.
			epoch := st.currentEpoch()
			err := spec.Probe(ctx)
			if err == nil {
				if !st.probePassed(reason, epoch) {
					logger.Info("substrate: ConsumerSupervisor: probe passed after an operator resume, verdict discarded",
						"consumer", spec.Name, "reason", reason)
					return false
				}
				logger.Info("substrate: ConsumerSupervisor: dependency recovered, resuming",
					"consumer", spec.Name, "reason", reason)
				if !st.anyReason() {
					s.persistActive(context.WithoutCancel(ctx), spec)
				}
				return false
			}
			if classify(spec, err) == ClassStructural {
				if reason == PauseStructural {
					// Already probing a structural pause: there is no tier to
					// escalate to, and the pause the loop would re-enter is the
					// one it is standing in. Clearing and re-adding the reason
					// would spend a relapse on a probe that never passed —
					// latching on failed PROBES rather than on failed
					// RECOVERIES — and would flicker the health entry through
					// active. So keep the pause and keep probing.
					//
					// The entry is rewritten only when the fault CHANGES. A
					// probe repeating a fault has told the operator nothing
					// new, and every health setter stamps a fresh timestamp —
					// so persisting on every attempt would leave a permanently
					// dead consumer publishing a permanently FRESH entry, which
					// is exactly the shape a freshness check exists to catch. A
					// fault that changes is news: that string is the operator's
					// whole diagnosis.
					logger.Warn("substrate: ConsumerSupervisor: structural condition still unmet, probing again",
						"consumer", spec.Name, "error", err)
					if st.recordStructuralCause(errString(err)) {
						s.persistDominant(context.WithoutCancel(ctx), spec, st, errString(err))
					}
					continue
				}
				logger.Error("substrate: ConsumerSupervisor: structural error during probe, escalating",
					"consumer", spec.Name, "error", err)
				st.clearReason(reason)
				latched, attempts := st.enterStructuralPause()
				cause := structuralCause(latched, attempts, err)
				st.recordStructuralCause(cause)
				s.persistDominant(context.WithoutCancel(ctx), spec, st, cause)
				return false
			}
			logger.Warn("substrate: ConsumerSupervisor: dependency not yet available, probing again",
				"consumer", spec.Name)
		}
	}
}

// structuralCause renders the cause persisted for a structural pause: the
// handler's or probe's own error, prefixed once the worker has latched so the
// operator reads why nothing further will happen on its own.
func structuralCause(latched bool, attempts int, err error) string {
	if latched {
		return latchedCause(attempts, errString(err))
	}
	return errString(err)
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
		return s.runProbeLoop(ctx, spec, st, PauseInfra)
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
