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

// RebuildCompleteSink is the standing personal healer, as a rebuilding personal
// lens sees it (personal-lens-delta-publication-design.md §4.5).
//
// A personal lens publishes NOTHING for as long as its rebuild window is open
// (ScopeSilent), so at the instant that window closes, every connected device
// still holds the shape it held when the window opened. Nothing else would
// correct that within a bounded time: the CDC path only republishes a row an
// event touches, and the healer's own content cycle is a day away. So the end of
// the window asks for one.
//
// The interface is one method for the reason SetGrantChangeSink's is: the
// pipeline package cannot import grantchange (grantchange imports pipeline), so
// the healer arrives injected rather than named.
type RebuildCompleteSink interface {
	// RequestContentCycle asks for the next full sweep cycle to republish
	// every swept actor's rows, not the authoritative frame alone.
	//
	// Implementations must not block: it is called inline wherever a rebuild
	// window ends — the completion watcher's goroutine, or the caller's own on
	// a rebuild abandoned before it ever reset its consumer.
	RequestContentCycle()
}

// SetRebuildCompleteSink installs the healer this lens tells when its rebuild
// window closes. Like SetGrantChangeSink it is called at construction time,
// before the pipeline starts writing.
//
// A nil sink is a no-op and a deployment with no standing personal healer at
// all — every harness, and any embedder that wired none. The rebuild still
// completes and the lens still returns to "active"; what is missing is the one
// prompt republication, so its devices carry the pre-rebuild shape until their
// next hydrate. Fail-SLOW, the same posture and for the same reason as a missing
// grant-change sink.
func (p *Pipeline) SetRebuildCompleteSink(sink RebuildCompleteSink) {
	if sink == nil {
		return
	}
	p.rebuildCompleteSink = sink
}

// HasRebuildCompleteSink reports whether this lens can ask a standing healer for
// a content cycle when its rebuild window closes. Its reader is the host's own wiring
// test: the call that installs the sink is one line in an activation sequence,
// and a deployment that dropped it would rebuild silently and never republish —
// a difference nothing else on the plane can be asked about.
func (p *Pipeline) HasRebuildCompleteSink() bool { return p.rebuildCompleteSink != nil }

// announceRebuildComplete asks the standing personal healer for a content cycle
// on behalf of a personal lens whose silent window has just ended.
//
// Personal targets only, and the restriction turns on what a rebuild REPAIRS.
// A business or auth-plane rebuild's output is a stored read model, and its own
// replay is what rewrites it: nothing that replay produces was ever pushed to a
// device, so there is nothing left for a content cycle to deliver. A personal
// lens is the one shape whose replay publishes nothing at all, which is exactly
// why its devices still hold the pre-rebuild shape when the window closes.
//
// What the cycle costs is the same whoever asks: it republishes every registered
// personal lens's rows for every swept actor, not the asking lens's alone. So an
// ask made on a stored target's behalf would buy a whole-population republish
// for a rebuild that had already repaired itself.
func (p *Pipeline) announceRebuildComplete() {
	if p.rebuildCompleteSink == nil {
		return
	}
	if !p.PublishesToDevices() {
		return
	}
	p.rebuildCompleteSink.RequestContentCycle()
	slog.Info("pipeline: rebuild window closed on a personal lens — it published nothing while it was open, so the standing healer's next cycle is a CONTENT cycle that hands every actor the rebuilt shape once, at a live revision",
		"ruleId", p.ruleID)
}

// PublishesToDevices reports whether this lens's RUNNING target is a personal
// one — a per-actor subject stream connected devices hold a live subscription
// to, rather than a stored read model something reads back.
//
// The running ADAPTER answers it, not the spec: what a device receives is
// decided by what the pipeline is writing through right now.
func (p *Pipeline) PublishesToDevices() bool {
	_, personal := p.currentAdapter().(adapter.KeySetPublisher)
	return personal
}

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
	// endRebuild closes this rebuild's window, asks the standing healer for the
	// content cycle a personal lens's silence owes its devices once the last
	// window on the lens has closed however it ended, and reports whether this
	// rebuild was still the installed one. The status write below belongs to
	// whichever rebuild is current, and this one may no longer be it: announcing
	// "active" under a NEWER rebuild's live rescan would report a lens that is
	// still draining. An older rebuild's failure is still worth recording, but it
	// does not get to retire a newer one's status.
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
// actually observed the rescan reach zero outstanding; `ended` is the
// once-latch that makes the window this rebuild opened close exactly once,
// however many enders reach it.
//
// The first two are separate because a closed channel alone cannot answer the
// question an attesting caller is asking. Every exit closes `done`, including a
// watcher cancelled at shutdown — and a waiter selecting on a closed channel and
// an expired context takes either arm at random, so a close read as completion
// makes "the rescan drained" and "the process is going down mid-rescan" the same
// observation about half the time. The flag is what the waiter reads; the
// channel only says "stop waiting".
type rebuildSignal struct {
	done    chan struct{}
	drained atomic.Bool
	ended   atomic.Bool
}

// beginRebuild OPENS a rebuild window: it raises the pipeline's live-window
// count, installs a fresh completion signal for the rebuild about to start, and
// starts that rebuild's progress clock. The returned signal belongs to that
// rebuild: only the goroutine that ends it (its watcher, or the error path that
// abandons it) ends it, via endRebuild — and whichever of those runs first
// lowers the count again, exactly once.
//
// The count rises HERE, before the health write, the registration wait, the
// truncate and the consumer reset — so nothing that follows can publish "active"
// over a rescan about to start, and a personal lens is already silent by the
// time the first replayed event could reach its write loop.
//
// The progress clock is stamped here too, and reset rather than carried: a fresh
// rebuild judged on the timestamp a finished one left behind is judged on a
// clock it never started. Stamping at the open rather than at the first poll is
// what keeps a rebuild whose watcher only ever ERRORS judgeable — that path
// records no progress on purpose, and a zero timestamp reads as "unknown, not
// wedged" (health.evalRebuildWedged), so a lens could stay silent indefinitely
// with nothing surfacing it.
func (p *Pipeline) beginRebuild() *rebuildSignal {
	p.rebuildWatchMu.Lock()
	sig := p.openRebuildWindowLocked()
	p.rebuildWatchMu.Unlock()
	p.baselineRebuildProgress()
	return sig
}

// beginRebuildIfIdle opens a window only when this pipeline has none open,
// returning nil when one is already in flight. It is how a rebuild interrupted
// by a restart re-arms its watcher without opening a second window for a rescan
// this process is already watching.
//
// The test and the open share beginRebuild's critical section rather than
// asking and then calling it: between an unlocked answer and the open, a
// control-plane Rebuild arriving while Run is still starting would give the same
// rescan two windows, and the second would never be closed by anything.
func (p *Pipeline) beginRebuildIfIdle() *rebuildSignal {
	p.rebuildWatchMu.Lock()
	if p.rebuildWindows.Load() > 0 {
		p.rebuildWatchMu.Unlock()
		return nil
	}
	sig := p.openRebuildWindowLocked()
	p.rebuildWatchMu.Unlock()
	p.baselineRebuildProgress()
	return sig
}

// openRebuildWindowLocked raises the live-window count and installs the new
// rebuild's signal. Callers hold rebuildWatchMu, which is what makes the two
// one step: releaseRebuildSignal takes the same lock, so no ender can lower a
// count between the raise and the install.
func (p *Pipeline) openRebuildWindowLocked() *rebuildSignal {
	sig := &rebuildSignal{done: make(chan struct{})}
	p.rebuildWatch = sig
	p.rebuildWindows.Add(1)
	return sig
}

// baselineRebuildProgress starts an opening rebuild's progress clock, discarding
// the previous rebuild's record. Outside rebuildWatchMu because the two locks
// guard unrelated state and nothing reads the pair atomically.
func (p *Pipeline) baselineRebuildProgress() {
	p.rebuildMu.Lock()
	p.rebuildOutstanding, p.rebuildProgressAt = 0, time.Now()
	p.rebuildMu.Unlock()
}

// endRebuild ends sig: it closes that rebuild's window, and — when the window
// closed was the LAST one open on this pipeline — asks the standing personal
// healer for one content cycle. It reports whether sig was still the installed
// rebuild, which is a narrower fact the health status write turns on.
//
// The ask lives here because EVERY way a window ends passes through it: a
// drained rescan, a rebuild abandoned before its consumer was ever reset, and a
// rebuild on a pipeline with no reporter, which has no watcher to end it later.
// A personal lens scopes every CDC event to ScopeSilent for as long as any
// window is open, so what the ask answers is the SILENCE, not the success: a
// window that lasted twelve seconds and then failed left exactly as many rows
// stale on exactly as many devices as one that drained. A personal lens that was
// silent for any span asks for one content cycle when that span ends.
//
// Once per span of silence, in both directions. The 1 → 0 transition is what
// asks, so a rebuild that begins and abandons under an earlier live rescan stays
// silent — the lens is still not publishing, and the surviving rescan's own end
// is what will ask — while sig.ended keeps the deferred endRebuild on the way
// out of watchRebuildCompletion from closing a second window after the drained
// branch has already closed this one.
//
// The ask is made after releaseRebuildSignal has lowered the count, so an event
// racing it publishes normally rather than silently: the content cycle is the
// backstop for a window that published nothing, not a substitute for the live
// path. It is also made outside that function's mutex, because the sink is
// another component's lock and the window's state is already settled.
func (p *Pipeline) endRebuild(sig *rebuildSignal) bool {
	owned, last := p.releaseRebuildSignal(sig)
	if last {
		p.announceRebuildComplete()
	}
	return owned
}

// releaseRebuildSignal closes sig's rebuild window: it lowers the live-window
// count, retracts the installed signal, and releases every waiter. It reports
// two different facts — whether sig was still the INSTALLED rebuild, and whether
// the window it closed was the LAST one open.
//
// Ownership keeps a slow finisher from retracting a newer rebuild's signal: a
// rebuild that started after this one has already replaced rebuildWatch, and its
// own waiters must keep waiting. It is the health STATUS write's question,
// because a status describes the rebuild the lens is currently running.
//
// `last` is the SILENCE's question, and it is the one the count answers that a
// single installed signal cannot. rebuildWatch names the most recently BEGUN
// rebuild, not the set of rescans still running: `Rebuild` is fire-and-forget
// and does not take rebuildSerial, so a second caller can begin — and then
// abandon — a rebuild while an earlier watcher is still polling a live rescan.
// That abandon owns the signal; what it must not do is declare the lens live,
// because the earlier rescan is still replaying and a personal lens is still
// publishing nothing. So each rebuild lowers its OWN window (sig.ended makes
// that exactly once per rebuild however many enders reach it), and only the
// transition to zero windows both un-suppresses the convergence sweep
// (Sweeper.suppressed reads RebuildInFlight) and asks for the content cycle.
//
// The count, ownership and the release all move inside ONE critical section
// because they are one decision. Checking ownership through a separate call
// leaves a window between the answer and the mutation it gates, and lowering the
// count after the release leaves a wider one: a waiter freed by the close can
// begin the next rebuild — beginRebuild takes this same lock, so it cannot — and
// see its own window closed out from under it by the goroutine that just ended.
//
// The health STATUS write stays with the caller, after this returns: it is
// remote I/O and does not belong under a mutex every watcher takes. Two residues
// follow, both bounded. An older goroutine's "active" can land after a newer
// rebuild's "rebuilding", which the newer rebuild corrects when it drains; and
// waiters are released BEFORE that write, so a caller can return from
// RebuildAndWait while the entry still reads "rebuilding" — no consumer reads
// status after a rebuild, and the window count, which is the load-bearing half,
// is ordered here.
//
// It does NOT set `drained` — only the watcher that saw the rescan finish does
// that — so every other exit (abandoned, cancelled, never watched) releases
// waiters with an honest "this did not drain".
func (p *Pipeline) releaseRebuildSignal(sig *rebuildSignal) (owned, last bool) {
	p.rebuildWatchMu.Lock()
	defer p.rebuildWatchMu.Unlock()
	owned = p.rebuildWatch == sig
	if owned {
		p.rebuildWatch = nil
	}
	if sig.ended.CompareAndSwap(false, true) {
		last = p.rebuildWindows.Add(-1) == 0
	}
	// Closing under the lock rather than beside it: two enders racing on the
	// select/default form would both see it open and double-close, which panics.
	select {
	case <-sig.done:
	default:
		close(sig.done)
	}
	return owned, last
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

// TruncateForReactivation clears the lens's rows on the seam between a stopped
// pipeline and the same lens ID being activated again from an edited spec, and
// reports whether it purged.
//
// requested says the replay the activation runs cannot overwrite what is already
// stored — the caller's question, not this one's (cmd/refractor's
// replayCannotOverwrite). A guarded target purges whatever requested says, for
// resolveTruncate's reason: the §6.2 watermark declines a replayed write at or
// below the seq already stored, so on a guarded target the new shape never lands
// on top of the old rows at all.
//
// Two things have to hold before it purges, and they are different questions.
// The target must be truncatable at all — the grant family shares one table
// across every producer and implements no Truncater, so a purge there is
// declined rather than announced. And the purge must be CONFINED
// (RebuildTruncateIsScoped): an unscoped NATS-KV truncate clears the whole
// bucket, and several lenses share one with producers this seam knows nothing
// about. Either way it warns and declines; the replay still overwrites in place
// every row it re-derives, and what stays behind is only what the new spec no
// longer addresses.
//
// Confinement here means what ApplyTruncateScope bound onto the adapter: the
// lens's key prefix AND its own key inverse as the exact ownership test over
// what that prefix lists, because one lens's prefix contains another's (`cap.`
// covers `cap.roles.`). Without the second, a guarded lens's forced purge takes
// its siblings' rows and re-derives none of them.
//
// The purge runs through truncateTarget, so a cap-read producer's grant sink
// hears every actor whose grants it withdrew.
//
// Call it once Run has returned — an in-flight write of the old shape landing
// after the purge would outlive the whole re-activation — and before the
// activation that follows, which is what re-derives every row this removes.
func (p *Pipeline) TruncateForReactivation(ctx context.Context, requested bool) (bool, error) {
	adpt := p.currentAdapter()
	if !resolveTruncate(adpt, p.ruleID, requested) {
		return false, nil
	}
	if _, truncatable := adpt.(adapter.Truncater); !truncatable {
		slog.Warn("pipeline: re-activation: target cannot be truncated — shared with other producers; skipping the purge (the replay re-derives rows that are ABSENT and leaves rows already present at or above their stored watermark unchanged)",
			"ruleId", p.ruleID)
		return false, nil
	}
	if !p.RebuildTruncateIsScoped() {
		slog.Warn("pipeline: re-activation: target is not confined to this lens's own keys — skipping the purge (the replay overwrites in place; rows the new spec no longer addresses stay behind)",
			"ruleId", p.ruleID)
		return false, nil
	}
	if err := p.truncateTarget(ctx, adpt); err != nil {
		return false, err
	}
	return true, nil
}

func (p *Pipeline) rebuild(ctx context.Context, truncate bool, sig *rebuildSignal) error {
	// sig's window is already open (beginRebuild) and its progress clock already
	// started, so none of the steps below can publish "active" over a rescan
	// about to start.

	// 1. Set health status to "rebuilding".
	if p.reporter != nil {
		if err := p.reporter.SetRebuilding(ctx); err != nil {
			slog.Warn("pipeline: rebuild: could not set rebuilding status", "ruleId", p.ruleID, "err", err)
		}
	}

	// Run registers the durable with the supervisor only after creating it
	// server-side, so a rebuild arriving in that window — a MATCH hot-reload at
	// boot, a control-plane Rebuild right after Run starts — would be told "not
	// managed" by the reset below and abandon a rescan the lens needs. The same
	// guard Pause/Resume use: wait, briefly, for Run to publish the consumer. A
	// consumer that is not managed AFTER Run has registered is a different case
	// — the durable removed from under a live pipeline, which the deleter does
	// before cancelling the run — and that one must reach the reset below so the
	// refusal it meets there is the one recorded.
	if p.supervisor != nil && !p.supervisor.IsManaged(p.consumerCfg.Name) && !p.awaitStarted(ctx) {
		return p.abandonRebuild(ctx, sig, fmt.Errorf(
			"pipeline: rebuild: the supervised consumer %q is not registered yet", p.consumerCfg.Name))
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
		//
		// This rebuild's silent window ends here too — its own and only end, so
		// the count falls by exactly one — which is where a personal lens's
		// devices are owed the content cycle endRebuild asks for once the last
		// window on the lens has closed.
		p.endRebuild(sig)
	}

	return nil
}

// resumeInterruptedRebuild re-opens the rebuild window of a lens whose
// persisted health entry still reads "rebuilding" when this process starts, and
// returns that window's signal — nil when there is nothing to resume. The watch
// that ends the window is started separately, by watchResumedRebuild, because
// the two halves belong on opposite sides of the consumer's registration.
//
// The watcher Rebuild launches lives only as long as the process that armed it;
// the rescan it watches outlives that process, because the rebuild IS the
// durable's reset cursor and JetStream keeps it. So after a crash or an
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
//
// ORDERING INVARIANT — Run calls this BEFORE it registers the consumer, and the
// health read it pays for is on that critical path deliberately. The window is
// the whole of a personal lens's silence: eventPublishScope reads
// RebuildInFlight once per event, and the rescan being resumed is a durable
// cursor JetStream has already rewound, so the pump delivers its first replayed
// event the instant the supervisor is handed the spec. A window opened after
// that registration is opened after the event whose scope it governs, and that
// one event fans out over every actor of the lens and publishes the whole
// replayed shape at a revision below every device's frame high-water mark —
// the flood the silence exists to remove, on the one path where the lens has
// the most left to replay.
func (p *Pipeline) resumeInterruptedRebuild(ctx context.Context) *rebuildSignal {
	if p.reporter == nil {
		return nil
	}
	entry, err := p.reporter.GetStatus(ctx)
	if err != nil {
		slog.Warn("pipeline: could not read health status to resume an interrupted rebuild",
			"ruleId", p.ruleID, "err", err)
		return nil
	}
	if entry.Status != health.StatusRebuilding {
		return nil
	}
	// A window already open means a rebuild is in flight in THIS process — a
	// control-plane Rebuild that arrived while Run was still starting — and it
	// already owns a watcher.
	sig := p.beginRebuildIfIdle()
	if sig == nil {
		return nil
	}
	slog.Info("pipeline: resuming the watch for a rebuild interrupted by a restart", "ruleId", p.ruleID)
	return sig
}

// watchResumedRebuild starts the completion watch for the window
// resumeInterruptedRebuild opened, and does nothing when it opened none.
//
// ORDERING INVARIANT — Run calls this AFTER it has registered the consumer, and
// unlike the open it is not free to run earlier. watchRebuildCompletion polls
// OutstandingForConsumer, and the supervisor answers "not managed" for a
// consumer it has not been given yet. That answer is an error, and the
// watcher's error branch retries at the same delay without recording progress,
// so an early poll cannot end the window — but it also cannot observe an
// outstanding count, and every poll spent before the consumer exists is one the
// rebuild's progress clock (recordRebuildProgress, read by
// health.evalRebuildWedged) does not get. Starting the watch once the consumer
// is registered keeps every poll a real observation of the rescan.
func (p *Pipeline) watchResumedRebuild(ctx context.Context, sig *rebuildSignal) {
	if sig == nil {
		return
	}
	go p.watchRebuildCompletion(ctx, sig)
}

// watchRebuildCompletion polls the supervised consumer's outstanding count,
// starting at rebuildPollInterval. When it reaches zero, it transitions health
// KV from "rebuilding" back to "active" (AC5).
//
// Outstanding counts the un-delivered backlog *and* the delivered-but-unacked
// messages: a message the pump has fetched leaves the backlog the instant it is
// delivered, so a backlog-only check reads zero mid-flight and would publish
// "active" over a rescan that has not drained — and that is not a transient
// mislabel when the in-flight message then fails and is redelivered.
//
// The poll delay backs off (nextRebuildPollDelay) while outstanding holds
// steady or climbs against racing writes, and resets to the floor the instant
// it strictly decreases: a rebuild racing 10k–128k unprocessed events at one
// message a minute (the PO's finding) no longer costs a consumer-info call
// every rebuildPollInterval for the whole drain, but a rebuild that starts
// actually moving is checked promptly again right when it might finish.
func (p *Pipeline) watchRebuildCompletion(ctx context.Context, sig *rebuildSignal) {
	// The rebuild window ends when this watcher exits for any reason, so the
	// deferred endRebuild both releases the waiters on THIS rebuild and lowers
	// the live-window count by one — sig's own window, never another rebuild's.
	// A watcher cancelled at shutdown, or one returning after a newer rebuild has
	// begun, therefore leaves that newer rescan's window open and the convergence
	// sweep suppressed under it.
	defer p.endRebuild(sig)
	delay := p.rebuildPollInterval
	timer := time.NewTimer(delay)
	defer timer.Stop()
	// previousOutstanding and havePolled let a poll tell a genuine drain
	// (outstanding fell) from one that merely held steady or grew — the same
	// distinction recordRebuildProgress's own clock draws. havePolled gates the
	// very first observation, which has nothing yet to compare against.
	var previousOutstanding uint64
	havePolled := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			outstanding, err := p.supervisor.OutstandingForConsumer(ctx, p.consumerCfg.Name)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// Consumer may still be initializing or context cancelled; retry
				// at the same delay — an error observes no outstanding count, so
				// it is neither progress nor its absence. No progress is
				// recorded: this retries forever, so an error that never clears
				// must read as wedged rather than as quietly fine.
				timer.Reset(delay)
				continue
			}
			p.recordRebuildProgress(outstanding)
			if outstanding == 0 {
				// The ONE place a rebuild is recorded as genuinely finished. Set
				// before the release below, so no waiter can observe the closed
				// channel without it.
				sig.drained.Store(true)
				// endRebuild closes this rebuild's window before releasing
				// waiters, so a concurrent health sink re-checking the count
				// converges on "active" — and reports whether this rebuild is
				// still the current one. Announcing "active" when it is not would
				// retire a newer rescan's status mid-drain. The deferred
				// endRebuild runs again on the way out and is a no-op by then,
				// this rebuild's content-cycle ask included.
				if owned := p.endRebuild(sig); owned && p.reporter != nil {
					if serr := p.reporter.SetActive(ctx); serr != nil {
						slog.Error("pipeline: rebuild: set active", "ruleId", p.ruleID, "err", serr)
					}
				}
				return
			}
			decreased := havePolled && outstanding < previousOutstanding
			delay = nextRebuildPollDelay(delay, p.rebuildPollInterval, RebuildPollBackoffCap, !havePolled, decreased)
			previousOutstanding = outstanding
			havePolled = true
			timer.Reset(delay)
		}
	}
}

// nextRebuildPollDelay computes watchRebuildCompletion's next poll delay from
// the outcome of the poll just taken. first is true only for the very first
// observation, which has no prior count to compare against and so holds at
// the floor rather than guessing progress either way. Thereafter a strict
// decrease resets to the floor; anything else — steady, or grown against
// racing writes — doubles the delay, clamped at cap.
func nextRebuildPollDelay(current, floor, capDelay time.Duration, first, decreased bool) time.Duration {
	if first || decreased {
		return floor
	}
	doubled := current * 2
	if doubled > capDelay {
		return capDelay
	}
	return doubled
}

// recordRebuildProgress folds one poll of the un-drained count into the rebuild's
// progress record. The timestamp advances only on a STRICT decrease, so it
// answers "when did this rebuild last actually drain something" rather than
// "when was it last observed" — the second is true of a wedged rebuild every
// poll interval and would report it as healthy forever. The window's OPEN is the
// clock's baseline (beginRebuild), so a rebuild that has drained nothing since it
// started ages from the moment it started.
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
// decreased — or, until a poll observes a decrease, when its window opened.
// progressAt is zero only when this process has started no rebuild on the lens
// at all, which a consumer must read as "unknown", never as "stalled since the
// epoch".
func (p *Pipeline) RebuildProgress() (outstanding uint64, progressAt time.Time) {
	p.rebuildMu.Lock()
	defer p.rebuildMu.Unlock()
	return p.rebuildOutstanding, p.rebuildProgressAt
}
