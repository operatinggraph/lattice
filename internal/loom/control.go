package loom

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// InstanceSummary is the operator-facing snapshot of one Loom instance, returned
// by ListInstances: the durable cursor record's identity and lifecycle fields. It
// covers running instances and retained terminals alike (a terminal instance's
// record persists — only its pattern pin is deleted).
type InstanceSummary struct {
	InstanceID string `json:"instanceId"`
	PatternRef string `json:"patternRef"`
	SubjectKey string `json:"subjectKey"`
	Cursor     int    `json:"cursor"`
	Status     string `json:"status"`
	RetryCount int    `json:"retryCount"`
}

// ConsumerStatus is the operator-facing snapshot of one managed consumer: its
// durable name and current pause state ("running" / "pausedManual" /
// "pausedStructural" / "pausedInfra"). The name set is authoritative (the
// supervisor's managed registry); the state is joined from the engine's
// consumer-state cache, which is eventually-consistent with the supervisor's
// last transition — a name with no cached state yet reports "running".
type ConsumerStatus struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// InstanceDetail is the operator-facing detail of one Loom instance, returned by
// InspectInstance: the summary plus the resolved current step. CurrentStep is the
// pinned pattern's step at the instance's cursor for a RUNNING instance; it is nil
// for a terminal instance (the pin is deleted at terminal and there is no current
// step). Terminal marks a complete/failed instance. Step resolution is
// best-effort / eventually-consistent: the instance record and the pinned pattern
// are two reads, so a concurrent terminal transition is reconciled by re-reading
// the instance and reporting terminal (never an invariant-break error for a race).
type InstanceDetail struct {
	Instance    InstanceSummary `json:"instance"`
	CurrentStep *Step           `json:"currentStep"`
	Terminal    bool            `json:"terminal"`
}

// errConsumerNotManaged reports that a Pause/Resume target is not a
// currently-managed consumer. The supervisor's Pause/Resume are silent no-ops on
// an unknown name, so the control surface validates against the authoritative
// managed registry first and returns this typed error rather than acknowledging a
// command that did nothing.
var errConsumerNotManaged = errors.New("consumer not managed")

// errConsumerPauseForbidden reports that a Pause target is a dispatch/crash-safety
// critical consumer the control surface refuses to pause. Pausing the outbox relay
// halts ALL op dispatch (in-flight steps stall; deadlines re-arm forever); pausing
// the deadline watcher disables the only stuck-instance failure backstop. These are
// engine-wide hazards, not safe toggles — they are excluded from the pause op.
var errConsumerPauseForbidden = errors.New("consumer is dispatch/crash-safety critical and cannot be paused")

// pauseRestartNote is carried on every successful pause: a manual pause is
// persisted to health-kv and restored on engine restart, so it stays in effect
// until an explicit resume.
const pauseRestartNote = "manual pause persists across restart until resume"

// pauseDomainStallNote is appended to a per-domain completion consumer's pause
// (loom-<domain>): with that consumer paused, completions for the domain are no
// longer drained, so every in-flight instance currently awaiting a step in that
// domain holds at its cursor until the consumer is resumed. The trigger consumer
// carries no such warning (pausing it only stops NEW instances from starting; it
// does not stall instances already in flight).
const pauseDomainStallNote = "in-flight instances awaiting this domain will stall until resume"

// ListInstances returns a snapshot of every Loom instance's cursor record in
// loom-state — running instances and retained terminals alike (only the pattern
// pin is deleted at terminal). The .pattern pin sub-keys are filtered out; an
// unreadable/unparseable record is skipped, not fatal. Results are sorted by
// instanceId for a stable operator view. Read-only.
//
// It relies on instance cursor records never being soft-deleted: a terminal is
// recorded by flipping Status in place, so no isDeleted envelope is ever written
// for an instance.<id> key (symmetric to runningInstanceCounter). Every key that
// lists is therefore a live record, decoded directly with no tombstone check.
func (e *Engine) ListInstances(ctx context.Context) ([]InstanceSummary, error) {
	insts, err := e.state.listInstances(ctx, e.logger)
	if err != nil {
		return nil, err
	}
	out := make([]InstanceSummary, 0, len(insts))
	for _, inst := range insts {
		out = append(out, InstanceSummary{
			InstanceID: inst.InstanceID,
			PatternRef: inst.PatternRef,
			SubjectKey: inst.SubjectKey,
			Cursor:     inst.Cursor,
			Status:     inst.Status,
			RetryCount: inst.RetryCount,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out, nil
}

// ListConsumers returns every managed consumer and its pause state. The name set
// is the supervisor's authoritative managed registry (current + complete — not
// the lazily-populated state cache, which can miss names); each name's state is
// joined from the engine's consumer-state cache snapshot, defaulting to "running"
// when the cache has no entry yet. Results are sorted by name. Read-only.
func (e *Engine) ListConsumers(_ context.Context) ([]ConsumerStatus, error) {
	names := e.supervisor.ManagedNames()
	states := e.states.Snapshot()
	out := make([]ConsumerStatus, 0, len(names))
	for _, name := range names {
		state, ok := states[name]
		if !ok {
			state = "running"
		}
		out = append(out, ConsumerStatus{Name: name, State: state})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// InspectInstance returns one instance plus its resolved current step. It is
// terminal-safe and never panics on a missing/corrupt pin:
//
//   - A missing instance returns a not-found error.
//   - A TERMINAL instance (complete/failed) returns the summary with a nil
//     CurrentStep and Terminal=true — the pin is deleted at terminal, so the
//     absent pin is expected, never an error.
//   - A RUNNING instance reads its pinned pattern and resolves the step at the
//     cursor, bounds-checking cursor < len(steps) first (a cursor out of range
//     is a typed error, not a panic). A missing pin on a still-running instance
//     is the errPatternPinMissing invariant break — but only after a re-read
//     confirms the instance is still running (read-tearing guard, C5): the
//     instance and the pin are two reads, so a concurrent terminal transition
//     that deleted the pin between them is reconciled by re-reading the instance
//     and reporting terminal, not an invariant break.
func (e *Engine) InspectInstance(ctx context.Context, instanceID string) (InstanceDetail, error) {
	inst, err := e.state.getInstance(ctx, instanceID)
	if err != nil {
		return InstanceDetail{}, err
	}
	if inst == nil {
		return InstanceDetail{}, fmt.Errorf("loom: instance %q not found", instanceID)
	}
	return e.inspectResolved(ctx, inst)
}

// inspectResolved resolves the current step for inst, branching on status first.
func (e *Engine) inspectResolved(ctx context.Context, inst *Instance) (InstanceDetail, error) {
	summary := InstanceSummary{
		InstanceID: inst.InstanceID,
		PatternRef: inst.PatternRef,
		SubjectKey: inst.SubjectKey,
		Cursor:     inst.Cursor,
		Status:     inst.Status,
		RetryCount: inst.RetryCount,
	}
	if inst.Status != StatusRunning {
		// Terminal: the pin is gone by design and there is no current step.
		return InstanceDetail{Instance: summary, CurrentStep: nil, Terminal: true}, nil
	}

	pattern, err := e.state.getPinnedPattern(ctx, inst.InstanceID)
	if err != nil {
		if errors.Is(err, errPatternPinMissing) {
			// Read-tearing guard: the instance read said running but the pin is
			// absent. A concurrent terminal transition deletes the pin in the same
			// batch that flips status, so re-read the instance once — if it is now
			// terminal, this was a race, not an invariant break.
			reread, rerr := e.state.getInstance(ctx, inst.InstanceID)
			if rerr != nil {
				return InstanceDetail{}, rerr
			}
			if reread == nil || reread.Status != StatusRunning {
				// The re-read confirms the race: report terminal with the fresh
				// record. When the record was deleted out from under us
				// (reread == nil), the original summary's Status is still "running"
				// — echoing it would emit an inconsistent terminal:true /
				// status:running, so clear the status to the empty sentinel rather
				// than carry the now-untrue running value.
				rsummary := summary
				if reread != nil {
					rsummary = InstanceSummary{
						InstanceID: reread.InstanceID,
						PatternRef: reread.PatternRef,
						SubjectKey: reread.SubjectKey,
						Cursor:     reread.Cursor,
						Status:     reread.Status,
						RetryCount: reread.RetryCount,
					}
				} else {
					rsummary.Status = ""
				}
				return InstanceDetail{Instance: rsummary, CurrentStep: nil, Terminal: true}, nil
			}
			// Still running after the re-read, pin still absent: the genuine
			// invariant break (surfaced, never panicked).
			return InstanceDetail{}, err
		}
		return InstanceDetail{}, err
	}
	if inst.Cursor < 0 || inst.Cursor >= len(pattern.Steps) {
		// A running instance's cursor must index a real step; off-the-end is the
		// terminal-cursor position, so a running record there is a corrupt cursor
		// (surfaced as a typed error, never an index panic).
		return InstanceDetail{}, fmt.Errorf("loom: instance %q cursor %d out of range (pattern has %d steps)",
			inst.InstanceID, inst.Cursor, len(pattern.Steps))
	}
	step := pattern.Steps[inst.Cursor]
	return InstanceDetail{Instance: summary, CurrentStep: &step, Terminal: false}, nil
}

// PauseConsumer manually pauses a managed completion/trigger consumer
// (substrate.ConsumerSupervisor.Pause — PauseManual, idempotent, persisted to
// health-kv so it survives a restart until an explicit Resume). It first refuses
// the dispatch/crash-safety critical consumers — the outbox relay and the
// deadline watcher (errConsumerPauseForbidden): pausing the relay halts all op
// dispatch and pausing the deadline watcher disables the stuck-instance backstop,
// both engine-wide hazards rather than safe toggles (the relay/deadline names are
// compile-time constants, so this guard is race-free without touching the
// registry). It then pauses via the supervisor, which atomically checks the name
// is managed and applies the pause under one lock — an unmanaged name (e.g. one a
// concurrent reconcile Removed) returns errConsumerNotManaged rather than a
// silently-dropped no-op acknowledged as success. The read-only ListConsumers
// still reports the relay/deadline consumers and their state.
//
// On success it returns an advisory note for the operator: always the
// persist-across-restart contract, plus — for a per-domain completion consumer
// (any managed name that is not the trigger; relay/deadline are already rejected
// above) — a warning that in-flight instances awaiting that domain will stall
// until resume.
func (e *Engine) PauseConsumer(ctx context.Context, name string) (string, error) {
	if name == relayDurable || name == deadlineDurable {
		return "", fmt.Errorf("loom: %w: %q", errConsumerPauseForbidden, name)
	}
	if !e.supervisor.Pause(ctx, name) {
		return "", fmt.Errorf("loom: %w: %q", errConsumerNotManaged, name)
	}
	note := pauseRestartNote
	if name != triggerDurable {
		note = note + "; " + pauseDomainStallNote
	}
	e.logger.Info("loom: consumer paused", "consumer", name)
	return note, nil
}

// ResumeConsumer clears a managed consumer's pause (manual/structural)
// (substrate.ConsumerSupervisor.Resume — idempotent). It resumes via the
// supervisor, which atomically checks the name is managed and applies the resume
// under one lock — an unknown name is errConsumerNotManaged. Resume is
// unrestricted over managed names — it is always safe and can recover any pause
// state, including one set out-of-band (so unlike Pause it does not exclude the
// relay/deadline consumers).
func (e *Engine) ResumeConsumer(ctx context.Context, name string) error {
	if !e.supervisor.Resume(ctx, name) {
		return fmt.Errorf("loom: %w: %q", errConsumerNotManaged, name)
	}
	e.logger.Info("loom: consumer resumed", "consumer", name)
	return nil
}
