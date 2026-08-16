package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Rebuild performs an in-place rebuild of the rule's target store. It:
//  1. Sets health KV status to "rebuilding" (AC4).
//  2. If truncate is true and the adapter implements adapter.Truncater, truncates
//     the target store before the rescan (FR29, AC2).
//  3. Resets the durable consumer via the supervisor (delete-and-recreate
//     preserving DeliverLastPerSubjectPolicy), so all current Core KV entries are
//     rescanned from the beginning (FR28, AC1). The supervised pump swaps onto
//     the recreated durable automatically.
//  4. Launches a background goroutine (watchRebuildCompletion) that transitions
//     health KV to "active" when consumer lag reaches zero (AC5).
//
// Returns nil immediately — the rebuild runs asynchronously. The caller (control
// service) MUST call Rebuild in its own goroutine and return an async ack to the
// operator before Rebuild returns.
// abandonRebuild is every Rebuild error return's exit: it clears the in-flight
// flag, records the cause on the lens's health entry, and takes the status back
// out of "rebuilding". It returns the error unchanged so a caller reads exactly
// what went wrong.
//
// Leaving the status alone is what made this necessary. SetRebuilding is written
// before any of the work, but the only writer of the rebuilding → active
// transition is watchRebuildCompletion, which Rebuild launches on the SUCCESS
// path only. So a rebuild that failed left "rebuilding" latched with no watcher
// remaining to clear it, and Sweeper.suppressed refuses every tick whose status
// is not "active" — for the life of the process, since resumeInterruptedRebuild
// runs at startup only. RebuildInFlight() reads false by then, so the sweep's
// first suppression check does not catch it either.
//
// That is not a cosmetic status bug for a NARROWED lens. ActorAwareNarrowingLabels
// authorizes narrowing partly on the strength of a standing healer (the sweeper
// conjunct, and see ConsumerFilter's doc: Rebuild or the sweep are the only two
// recoveries from a wrong narrow). A failed Rebuild is the one event that could
// switch BOTH off at once — the rebuild did not happen, and the status it left
// behind suppresses the sweep that would have covered for it.
//
// "active" plus a recorded LastError is the honest pair, not a contradiction: the
// rebuild did not run, so the lens is still consuming under exactly the filter
// and cursor it had before the call — it is active, and it also has a fault worth
// an operator's attention. The status carries liveness; LastError carries the
// verdict. Loupe's fault conjunct keys off a live LastError precisely so the two
// can be read together.
func (p *Pipeline) abandonRebuild(ctx context.Context, sig *rebuildSignal, cause error) error {
	// Release every waiter on THIS rebuild's signal, always. An abandoned
	// rebuild is a finished one as far as a waiter is concerned — it is not
	// draining and nothing further will end it — but `drained` is never set, so
	// the waiter reads it as the failure it is.
	//
	// endRebuild clears the in-flight flag when this rebuild still owns it, and
	// reports that ownership. The status write below belongs to whichever rebuild
	// is current, and this one may no longer be it: announcing "active" under a
	// NEWER rebuild's live rescan would report a lens that is still draining. An
	// older rebuild's failure is still worth recording, but it does not get to
	// retire a newer one's status.
	if owned := p.endRebuild(sig); !owned {
		if p.reporter != nil {
			if recErr := p.reporter.RecordError(ctx, cause.Error()); recErr != nil {
				slog.Error("pipeline: rebuild: record abandoned-rebuild health signal",
					"ruleId", p.ruleID, "err", recErr)
			}
		}
		return cause
	}

	if p.reporter == nil {
		return cause
	}
	// Status FIRST, cause SECOND, and the order is load-bearing: SetActive writes
	// LastError back as an explicit JSON null (Reporter.SetActive), so recording
	// the cause before restoring the status would erase the very thing that makes
	// "active" honest and leave a clean-looking entry behind.
	if setErr := p.reporter.SetActive(ctx); setErr != nil {
		// Louder than the usual health-write warning: this is the path that
		// leaves the convergence sweep suppressed with nothing to un-latch it.
		slog.Error("pipeline: rebuild: could not clear rebuilding status after a failed rebuild — the convergence sweep stays suppressed until this lens is rebuilt or restarted",
			"ruleId", p.ruleID, "err", setErr)
	}
	if recErr := p.reporter.RecordError(ctx, cause.Error()); recErr != nil {
		slog.Error("pipeline: rebuild: record abandoned-rebuild health signal",
			"ruleId", p.ruleID, "err", recErr)
	}
	return cause
}

// rebuildSignal is one rebuild's completion record. `done` is closed when that
// rebuild ends for ANY reason; `drained` is set only by the watcher that
// actually observed the rescan reach zero outstanding.
//
// The two are separate because a closed channel alone cannot answer the
// question an attesting caller is asking. Every exit closes `done`, including a
// watcher cancelled at shutdown — and a waiter selecting on a closed channel and
// an expired context takes either arm at random, so a close read as completion
// makes "the rescan drained" and "the process is going down mid-rescan" the same
// observation about half the time. The flag is what the waiter reads; the
// channel only says "stop waiting".
type rebuildSignal struct {
	done    chan struct{}
	drained atomic.Bool
}

// beginRebuild installs a fresh completion signal for a rebuild about to start
// and returns it. The returned signal belongs to that rebuild: only the
// goroutine that ends it (its watcher, or the error path that abandons it)
// ends it, via endRebuild.
func (p *Pipeline) beginRebuild() *rebuildSignal {
	sig := &rebuildSignal{done: make(chan struct{})}
	p.rebuildWatchMu.Lock()
	p.rebuildWatch = sig
	p.rebuildWatchMu.Unlock()
	return sig
}

// endRebuild ends sig: it clears the pipeline-wide in-flight flag, retracts the
// installed signal and releases every waiter, and reports whether sig was still
// the installed rebuild. The equality check keeps a slow finisher from
// retracting a newer rebuild's signal: a rebuild that started after this one has
// already replaced rebuildWatch, and its own waiters must keep waiting.
//
// Ownership, the flag and the release all move inside ONE critical section
// because they are one decision. Checking ownership through a separate call
// leaves a window between the answer and the mutation it gates, and clearing the
// flag after the release leaves a wider one: a waiter freed by the close can
// begin the next rebuild — beginRebuild takes this same lock, so it cannot — and
// have its `rebuildInFlight` cleared out from under it by the goroutine that
// just ended. That un-suppresses the convergence sweep (Sweeper.suppressed reads
// RebuildInFlight) while the newer rescan is still draining.
//
// What ownership means here is narrow, and the narrowness is why this is not the
// whole story: rebuildWatch tracks the most recently BEGUN rebuild, not the set
// of rescans still running. `Rebuild` is fire-and-forget and does not take
// rebuildSerial, so a second caller can begin — and then abandon — a rebuild
// while an earlier watcher is still polling a live rescan; that abandon owns the
// signal, clears the flag, and un-suppresses the sweep under the earlier rescan.
// The ordering below closes the successor race, NOT that one. Closing it too
// needs the flag to count live watchers rather than name one signal, and the
// consequence meanwhile is bounded: the sweep is a healer, and the attestation
// path reads `drained`, never this flag.
//
// The health STATUS write also stays with the caller, after this returns: it is
// remote I/O and does not belong under a mutex every watcher takes. Two residues
// follow, both bounded. An older goroutine's "active" can land after a newer
// rebuild's "rebuilding", which the newer rebuild corrects when it drains; and
// waiters are now released BEFORE that write, so a caller can return from
// RebuildAndWait while the entry still reads "rebuilding" — no consumer reads
// status after a rebuild, and the flag, which is the load-bearing half, is
// ordered here.
//
// It does NOT set `drained` — only the watcher that saw the rescan finish does
// that — so every other exit (abandoned, cancelled, never watched) releases
// waiters with an honest "this did not drain".
func (p *Pipeline) endRebuild(sig *rebuildSignal) bool {
	p.rebuildWatchMu.Lock()
	defer p.rebuildWatchMu.Unlock()
	owned := p.rebuildWatch == sig
	if owned {
		p.rebuildWatch = nil
		p.rebuildInFlight.Store(false)
	}
	// Closing under the lock rather than beside it: two enders racing on the
	// select/default form would both see it open and double-close, which panics.
	select {
	case <-sig.done:
	default:
		close(sig.done)
	}
	return owned
}

// currentRebuildSignal returns the in-flight rebuild's completion signal, or
// nil when none is running.
func (p *Pipeline) currentRebuildSignal() *rebuildSignal {
	p.rebuildWatchMu.Lock()
	defer p.rebuildWatchMu.Unlock()
	return p.rebuildWatch
}

// RebuildAndWait rebuilds this pipeline and blocks until the rescan has
// drained, serialized against any other RebuildAndWait caller on the same
// pipeline. Rebuild itself is fire-and-forget — it returns as soon as the
// durable has been reset, leaving a background watcher to notice the drain — so
// a caller that must ATTEST to a completed rebuild (the retention-class key
// destruction consumer, retention-class-key-custody-design.md §6.3 step 4)
// cannot use it directly.
//
// It waits out an already-in-flight rebuild BEFORE starting its own rather than
// adopting it, and that ordering is the whole point. A rebuild in progress may
// have been started by another path — the MATCH hot-reloader
// (cmd/refractor/reload.go) or the operator "rebuild" control op
// (control.Service.rebuildRule), neither of which passes through
// rebuildSerial — and it may have begun BEFORE the key destruction this rebuild
// is answering to. Its rows can therefore still carry plaintext the destruction
// was supposed to erase. Counting it as this destruction's rebuild would attest
// to an erasure that never happened, so the wait is fail-closed where a
// CAS-and-skip would fail open.
//
// rebuildSerial serializes RebuildAndWait callers against EACH OTHER and
// nothing more; it does not exclude those two paths, which is exactly why each
// wait is on a per-rebuild signal rather than on shared in-flight state.
//
// wait bounds the two waits, not the rebuild: a deadline-bearing context passed
// into Rebuild would also cancel watchRebuildCompletion, which would leave the
// health entry latched on "rebuilding" and, through Sweeper.suppressed, retire
// the convergence sweep for the life of the process. A rebuild that outlives
// the budget therefore keeps running and keeps its watcher; only this caller
// gives up, with control.ErrRebuildWaitTimeout. A wait <= 0 means no bound, which no
// attesting caller should use — an unbounded wait inside a serial durable
// handler stops every subsequent message on that consumer.
func (p *Pipeline) RebuildAndWait(ctx context.Context, truncate bool, wait time.Duration) error {
	select {
	case p.rebuildSerial <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.rebuildSerial }()

	waitCtx := ctx
	if wait > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}

	// Waiting OUT a pre-existing rebuild asks a weaker question than waiting on
	// our own: all that matters is that it has ENDED, so this rescan cannot be
	// mistaken for it. How it ended is that rebuild's business — an abandoned or
	// unwatched one leaves the coast just as clear as a drained one, and
	// refusing to proceed on it would make an unrelated failure permanently
	// block every attesting caller.
	if err := p.waitRebuildSignal(waitCtx, ctx, p.currentRebuildSignal()); err != nil &&
		!errors.Is(err, control.ErrRebuildNotDrained) {
		return fmt.Errorf("pipeline: rebuild: waiting out an in-flight rebuild: %w", err)
	}
	sig, err := p.rebuildWithSignal(ctx, truncate)
	if err != nil {
		return err
	}
	return p.waitRebuildSignal(waitCtx, ctx, sig)
}

// waitRebuildSignal blocks until sig ends, waitCtx expires, or the caller's own
// ctx is cancelled. A nil sig means no rebuild was in flight.
//
// Success is `sig.drained`, never the closed channel: every other way a rebuild
// ends closes the channel too, so reading the close as completion is how a
// shutdown, an abandoned rescan, or a rebuild nothing ever watched all came
// back as "rebuilt". The three error classes are kept distinct because the
// caller's response differs — a cancelled caller retries on the next process, a
// budget expiry means the rescan is still running and healthy, and neither is a
// fault of the LENS, so neither may be answered by pausing it.
func (p *Pipeline) waitRebuildSignal(waitCtx, ctx context.Context, sig *rebuildSignal) error {
	if sig == nil {
		return nil
	}
	select {
	case <-sig.done:
		if sig.drained.Load() {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return control.ErrRebuildNotDrained
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return control.ErrRebuildWaitTimeout
	}
}

// Rebuild starts a rescan and returns immediately, discarding the completion
// signal — the fire-and-forget form every caller that does not attest uses.
func (p *Pipeline) Rebuild(ctx context.Context, truncate bool) error {
	_, err := p.rebuildWithSignal(ctx, truncate)
	return err
}

// rebuildWithSignal is Rebuild plus the completion channel of the rebuild it
// started, closed when that rescan drains (or when it is abandoned). On error
// the signal is already closed and nil is returned: there is nothing to wait
// for.
func (p *Pipeline) rebuildWithSignal(ctx context.Context, truncate bool) (*rebuildSignal, error) {
	sig := p.beginRebuild()
	if err := p.rebuild(ctx, truncate, sig); err != nil {
		return nil, err
	}
	return sig, nil
}

// resolveTruncate applies the guarded-target force rule to a rebuild's
// requested truncate. A guarded bucket forces it: the target's monotonic
// watermarks would reject a lower-seq historical replay, leaving rejected-write
// holes, and truncating clears the watermarks with the data so the stream
// replays from empty and the highest-seq write wins — a steady state identical
// to a from-scratch projection (Contract #6 §6.2). The force keys off Guarded()
// so the pipeline never learns lens canonical names.
//
// The force applies only to a target that can actually be truncated. A guarded
// target that cannot (the grant family, which shares one table across every
// producer and so must never TRUNCATE it) gets the honest account instead:
// forcing there would announce a repair the Truncater branch then silently
// declines, leaving the operator to believe the watermarks were cleared when a
// replay is still about to be rejected against them.
//
// "Rejected against them" is only half of what happens, and the half an
// operator reaching for a rebuild usually does not want. The §6.14 guard lives
// in the ON CONFLICT arm, so a row still PRESENT replays against its own stored
// seq and is a no-op, while a row ABSENT — the out-of-band restore or partial
// wipe a rebuild is the response to — takes the plain INSERT and is re-derived.
// So an un-truncatable guarded rebuild is a repair for exactly the divergence it
// looks powerless against, and saying only that rows "survive" discourages the
// action that fixes it.
func resolveTruncate(adpt adapter.Adapter, ruleID string, truncate bool) bool {
	if truncate {
		return true
	}
	g, ok := adpt.(interface{ Guarded() bool })
	if !ok || !g.Guarded() {
		return false
	}
	if _, truncatable := adpt.(adapter.Truncater); !truncatable {
		slog.Info("pipeline: rebuild: guarded target cannot be truncated (shared with other producers) — the replay re-derives rows that are ABSENT and leaves rows already present at or above their stored watermark unchanged; this repairs an out-of-band wipe but does not rewrite live rows",
			"ruleId", ruleID)
		return false
	}
	slog.Info("pipeline: rebuild: guarded bucket forces truncate (avoids rejected-write holes)", "ruleId", ruleID)
	return true
}

func (p *Pipeline) rebuild(ctx context.Context, truncate bool, sig *rebuildSignal) error {
	// Mark the rebuild in flight before the status write so a concurrent
	// supervisor health persist (probe recovery, operator resume) cannot
	// publish "active" while the rescan is still draining.
	p.rebuildInFlight.Store(true)

	// Clear the previous rebuild's progress record so this one is judged on its
	// own draining, not on a stale timestamp a finished rebuild left behind.
	p.rebuildMu.Lock()
	p.rebuildOutstanding, p.rebuildProgressAt = 0, time.Time{}
	p.rebuildMu.Unlock()

	// 1. Set health status to "rebuilding".
	if p.reporter != nil {
		if err := p.reporter.SetRebuilding(ctx); err != nil {
			slog.Warn("pipeline: rebuild: could not set rebuilding status", "ruleId", p.ruleID, "err", err)
		}
	}

	// 2. Optional target-store truncation, after the guarded-target force rule
	// (resolveTruncate) has had its say about what the request really means.
	truncate = resolveTruncate(p.currentAdapter(), p.ruleID, truncate)
	if truncate {
		// A target you cannot reset is a target you must not clear. The truncate
		// below is irreversible, while the only thing that re-derives the rows it
		// removes is the reset further down — a supervisor call that answers "not
		// managed" for a consumer already removed. Establishing that here rather
		// than discovering it at the reset is what keeps a lens deletion from
		// leaving a purged target behind: pipelineDeleter.Delete removes the
		// durable BEFORE it cancels the run context, so a rebuild racing a delete
		// finds the consumer gone while the pipeline still looks alive.
		if p.supervisor == nil || !p.supervisor.IsManaged(p.consumerCfg.Name) {
			return p.abandonRebuild(ctx, sig, fmt.Errorf(
				"pipeline: rebuild: consumer %q is not managed — refusing to truncate a target that cannot be re-derived",
				p.consumerCfg.Name))
		}
		if err := p.truncateTarget(ctx, p.currentAdapter()); err != nil {
			return p.abandonRebuild(ctx, sig, fmt.Errorf("pipeline: rebuild: truncate: %w", err))
		}
	}

	// 3. Recompute the Core KV filter from the CURRENT compiled rule (see
	// ConsumerFilter's doc: a MATCH hot-reload's UseFullEngineBranches call may
	// have already changed the referenced-label set by the time a rebuild
	// reaches here — activation's filter must not ride forward unexamined) and
	// reset the durable via the supervisor (delete-recreate-swap) with it.
	if p.supervisor == nil {
		return p.abandonRebuild(ctx, sig, fmt.Errorf("pipeline: rebuild: no supervisor configured"))
	}
	filterSubjects, filterSubject, filterDecision := p.ConsumerFilter()
	resetWithFilter := func() error {
		if err := p.supervisor.UpdateSpec(p.consumerCfg.Name, func(spec *substrate.ConsumerSpec) {
			spec.FilterSubjects = filterSubjects
			spec.FilterSubject = filterSubject
		}); err != nil {
			return err
		}
		return p.supervisor.ResetAwaitReopen(ctx, p.consumerCfg.Name, rebuildReopenWait)
	}
	if err := p.registerWithFilterFallback(ctx, filterSubjects, func() {
		filterSubjects = nil
		filterSubject = subjects.CoreKVFilter(p.coreKVBucket)
		// The fallback re-decides the footprint as well as the filter, so the
		// report below describes what was ADOPTED rather than what was derived.
		// It is the same value registerWithFilterFallback has already written,
		// deliberately: rewriting it is idempotent, where skipping the write
		// would need a flag and a branch.
		//
		// It is load-bearing on exactly one transition — the fallback fires and
		// the RETRY SUCCEEDS, where the derived (narrowed) decision would
		// otherwise overwrite the refusal. No fixture reaches it: making a
		// rebuild's first reset fail and its second succeed needs a
		// fault-injection seam the supervisor does not offer. What IS pinned is
		// the contract this closure depends on —
		// TestRegisterWithFilterFallback_ApplyBroadRunsBeforeASucceedingRetry.
		filterDecision = registrationFailedDecision()
	}, resetWithFilter); err != nil {
		return p.abandonRebuild(ctx, sig, fmt.Errorf("pipeline: rebuild: reset consumer: %w", err))
	}
	// Reported only once the consumer has actually ADOPTED this filter.
	// Recording before the reset left an abandoned rebuild advertising a
	// footprint the consumer never got — on the one path where the lens's
	// PREVIOUS filter is still the live one, so the entry became wrong about a
	// lens that is otherwise fine.
	p.RecordFilterDecision(ctx, filterDecision)

	// 4. Launch background goroutine to transition to "active" when lag reaches zero.
	if p.reporter != nil {
		go p.watchRebuildCompletion(ctx, sig)
	} else {
		// No reporter → no completion watcher, so this rebuild has no observable
		// end. Ending the signal keeps a waiter from blocking forever on a lens
		// that will never report, and leaving `drained` unset is what keeps that
		// waiter from reading the release as a completed rescan: the rescan is
		// still running, it simply cannot be watched. The Rebuilder contract
		// exists so an attesting caller cannot be handed a completion nobody
		// observed, and this is the branch that would otherwise hand it one.
		p.endRebuild(sig)
	}

	return nil
}

// resumeInterruptedRebuild re-arms the rebuilding → active transition for a
// lens whose persisted health entry still reads "rebuilding" when this process
// starts. The watcher Rebuild launches lives only as long as the process that
// armed it; the rescan it watches outlives that process, because the rebuild
// IS the durable's reset cursor and JetStream keeps it. So after a crash or an
// ordinary cycle the drain carries on with nothing left to declare it
// finished, and the status reads "rebuilding" for the rest of the lens's life.
//
// That is not a cosmetic mislabel: the capability convergence sweep suppresses
// itself on a rebuilding lens, so a latched status quietly retires the auth
// plane's only projection-convergence check while every liveness signal stays
// green.
//
// Re-arming rather than clearing is the point. An interrupted rebuild whose
// backlog has not drained is genuinely still rebuilding, and writing "active"
// over it would be exactly the premature transition watchRebuildCompletion's
// outstanding-not-backlog check exists to prevent. One poll answers both
// cases: it clears on the first tick when the drain already finished, and
// holds an honest "rebuilding" while it has not.
func (p *Pipeline) resumeInterruptedRebuild(ctx context.Context) {
	if p.reporter == nil {
		return
	}
	entry, err := p.reporter.GetStatus(ctx)
	if err != nil {
		slog.Warn("pipeline: could not read health status to resume an interrupted rebuild",
			"ruleId", p.ruleID, "err", err)
		return
	}
	if entry.Status != health.StatusRebuilding {
		return
	}
	// Losing this swap means a rebuild is already in flight in THIS process —
	// a control-plane Rebuild that arrived while Run was still starting — and
	// it already owns a watcher.
	if !p.rebuildInFlight.CompareAndSwap(false, true) {
		return
	}
	slog.Info("pipeline: resuming the watch for a rebuild interrupted by a restart", "ruleId", p.ruleID)
	go p.watchRebuildCompletion(ctx, p.beginRebuild())
}

// watchRebuildCompletion polls the supervised consumer's outstanding count at
// rebuildPollInterval. When it reaches zero, it transitions health KV from
// "rebuilding" back to "active" (AC5).
//
// Outstanding counts the un-delivered backlog *and* the delivered-but-unacked
// messages: a message the pump has fetched leaves the backlog the instant it is
// delivered, so a backlog-only check reads zero mid-flight and would publish
// "active" over a rescan that has not drained — and that is not a transient
// mislabel when the in-flight message then fails and is redelivered.
func (p *Pipeline) watchRebuildCompletion(ctx context.Context, sig *rebuildSignal) {
	// The rebuild window ends when this watcher exits for any reason, so the
	// deferred endRebuild both releases the waiters on THIS rebuild and clears
	// the in-flight flag — but only while this rebuild still owns it. Clearing it
	// unconditionally is the same hazard the abandon path guards against, on the
	// path that runs every time: a watcher cancelled at shutdown, or one that
	// returns after a newer rebuild has already begun, would otherwise
	// un-suppress the convergence sweep under a live rescan.
	defer p.endRebuild(sig)
	ticker := time.NewTicker(p.rebuildPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			outstanding, err := p.supervisor.OutstandingForConsumer(ctx, p.consumerCfg.Name)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// Consumer may still be initializing or context cancelled; retry.
				// No progress is recorded: this retries forever, so an error that
				// never clears must read as wedged rather than as quietly fine.
				continue
			}
			p.recordRebuildProgress(outstanding)
			if outstanding == 0 {
				// The ONE place a rebuild is recorded as genuinely finished. Set
				// before the release below, so no waiter can observe the closed
				// channel without it.
				sig.drained.Store(true)
				// endRebuild clears the in-flight flag before releasing waiters,
				// so a concurrent health sink re-checking the flag converges on
				// "active" — and reports whether this rebuild is still the current
				// one. Announcing "active" when it is not would retire a newer
				// rescan's status mid-drain. The deferred endRebuild runs again on
				// the way out and is a no-op by then.
				if owned := p.endRebuild(sig); owned && p.reporter != nil {
					if serr := p.reporter.SetActive(ctx); serr != nil {
						slog.Error("pipeline: rebuild: set active", "ruleId", p.ruleID, "err", serr)
					}
				}
				return
			}
		}
	}
}

// recordRebuildProgress folds one poll of the un-drained count into the rebuild's
// progress record. The timestamp advances only on a STRICT decrease, so it
// answers "when did this rebuild last actually drain something" rather than
// "when was it last observed" — the second is true of a wedged rebuild every
// poll interval and would report it as healthy forever.
//
// A count that goes UP is still progress in the sense that matters here: the
// consumer is live and the backlog is real, and a rebuild racing new writes can
// legitimately grow. What it must not do is reset the clock, so an oscillating
// count cannot mask a rebuild that never gets closer to done.
func (p *Pipeline) recordRebuildProgress(outstanding uint64) {
	p.rebuildMu.Lock()
	defer p.rebuildMu.Unlock()
	if p.rebuildProgressAt.IsZero() || outstanding < p.rebuildOutstanding {
		p.rebuildProgressAt = time.Now()
	}
	p.rebuildOutstanding = outstanding
}

// RebuildProgress reports the rebuild's un-drained count and when it last
// decreased. progressAt is zero when no rebuild has polled yet, which a consumer
// must read as "unknown", never as "stalled since the epoch".
func (p *Pipeline) RebuildProgress() (outstanding uint64, progressAt time.Time) {
	p.rebuildMu.Lock()
	defer p.rebuildMu.Unlock()
	return p.rebuildOutstanding, p.rebuildProgressAt
}
