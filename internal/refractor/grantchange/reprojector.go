// Package grantchange carries the consumer half of the D1 read-grant change
// edge (personal-lens-grant-change-trigger-design.md §4.2).
//
// A Personal Lens decides every row it publishes against the read-grant
// projection — a projection produced by a DIFFERENT Refractor pipeline, read
// live through capabilityread.IsReadable, with no change notification and no
// ordering between the two. So a grant that lands or is withdrawn changed
// nothing until some unrelated Core-KV event happened to re-drive that actor,
// which makes the staleness window bounded by unrelated traffic rather than by
// CDC lag.
//
// The producer announces a grant LIVENESS transition (adapter.GrantTransition,
// derived where both the stored and outgoing bodies are in hand). This package
// receives those announcements, coalesces them per actor, and re-drives that
// one actor's personal pipelines.
//
// The signal is best-effort by construction: it is in-process, so a crash
// between the grant write and the drain loses it, and the set is bounded, so a
// mass grant change can overflow it. That is deliberate — prevention
// best-effort, detect-and-recover authoritative. The durability story is the
// convergence sweep, not a persisted queue here; what this package owes in
// exchange is that every way it drops a signal is LOUD.
package grantchange

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// DefaultDrainInterval is how often the drain worker looks for dirty actors.
// Short on purpose: this is the LATENCY path — the whole reason it exists
// instead of leaving the job to the standing healer is that a newly-granted
// staff member should see their queue now, not on the next sweep cycle. The
// cost of a short tick is one map check, since a tick with an empty set does no
// work at all.
const DefaultDrainInterval = time.Second

// DefaultMaxDirtyActors bounds the coalescing set. A package upgrade that
// re-derives every actor's grants produces one transition per actor, each
// costing one cypher evaluation per personal lens, so the set is what stands
// between a mass grant change and unbounded memory.
//
// On overflow the NEW entry is dropped, never an entry already queued: an actor
// already in the set has a reprojection owed to it and dropping that would
// convert a bounded queue into a lost signal for an actor nobody is tracking.
const DefaultMaxDirtyActors = 10000

// PersonalPipeline is the reprojector's view of one personal lens.
//
// It is deliberately NOT control.Hydrator. That interface is one method,
// unexported behind the control service's own mutex with no iterator, and it is
// shaped that way so internal/control need not import the pipeline package at
// all. Reusing it would drag a control-plane boundary into a Refractor-internal
// reaction. This registry is a second list, it is Refractor-internal, and it
// crosses nothing.
type PersonalPipeline interface {
	// ReprojectPersonalActor re-evaluates this lens for one actor and
	// publishes the authoritative keyset frame.
	ReprojectPersonalActor(ctx context.Context, identityID string) error
	// RecordGrantReprojectIssue raises this lens's Health fault for a
	// reprojection that did not happen. It returns the write's own error so a
	// caller tracking what has actually been REPORTED (rather than what it
	// tried to report) can tell a landed issue from a lost one.
	RecordGrantReprojectIssue(ctx context.Context, kind, detail string) error
}

// Reprojector is the process-level consumer of read-grant transitions: one
// coalescing dirty-actor set, one drain worker, and its own registry of the
// personal pipelines to re-drive.
//
// It satisfies pipeline.GrantChangeSink, so the producers hand it transitions
// directly with no intermediate queue.
type Reprojector struct {
	mu sync.Mutex
	// dirty is the coalescing set: actors owed a reprojection. Created at boot
	// with the Reprojector, emptied per actor as the drain takes each one, and
	// deliberately NOT carried across a restart — the sweep is the recovery for
	// a process that died holding signals.
	dirty map[string]struct{}
	// dropped counts signals refused, CUMULATIVELY for the process's life, and
	// droppedReported is the high-water of that count which has actually been
	// written to Health. The two are separate so a failed Health write loses
	// nothing: the delta stays visible to the next attempt instead of being
	// cleared into a silence the bound's contract forbids. Reported from the
	// drain worker rather than the producer's write path, so raising an issue
	// never blocks a write.
	dropped         int
	droppedReported int

	// registryReady, when set, reports nil once the in-process lens registry is
	// complete against Core KV; registryLatched records that it has said so at
	// least once. See registryIsReady for what this gates and why it latches.
	registryReady   func(context.Context) error
	registryLatched bool
	holdSince       time.Time
	holdLogged      bool
	holdMax         time.Duration
	// personal is the per-ruleID registry, populated at boot from the same
	// site that registers the control-plane hydrator.
	personal map[string]PersonalPipeline

	maxDirty int
	interval time.Duration
}

// New builds a Reprojector with the default bound and drain interval.
func New() *Reprojector {
	return &Reprojector{
		dirty:    make(map[string]struct{}),
		personal: make(map[string]PersonalPipeline),
		maxDirty: DefaultMaxDirtyActors,
		interval: DefaultDrainInterval,
		holdMax:  RegistryHoldMax,
	}
}

// RegistryHoldMax bounds how long the drain will wait for the lens registry to
// finish loading before draining anyway.
//
// The wait is prevention, not correctness, and prevention must never become a
// permanent stall: a lens that fails activation outright never registers, and
// without a bound one such lens would hold the whole grant-change edge closed
// forever. Past the bound the drain proceeds and says loudly that it is doing so.
//
// This bound is doing more work than a fallback normally does — see
// SetRegistryReady on why the corpus-global check makes it load-bearing rather
// than theoretical.
const RegistryHoldMax = 2 * time.Minute

// SetRegistryHoldMax overrides how long the drain waits for the lens registry
// to finish loading before proceeding anyway. Zero or negative leaves the
// default. Like SetBounds it exists so a test can reach the bounded-fallback
// branch without waiting out the production window.
func (r *Reprojector) SetRegistryHoldMax(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d > 0 {
		r.holdMax = d
	}
}

// SetRegistryReady installs the completeness check the drain waits on. nil
// (tests, harnesses) means no gate.
//
// A signal is consumed exactly once: Drain takes an actor off the dirty set and
// reprojects it across whatever lenses are registered AT THAT MOMENT. Personal
// lenses register one at a time as their rules activate, while the drain ticker
// and the read-grant producers are already live — so an actor dirtied before the
// last personal lens registers would be reprojected against a short registry,
// and the frames its unregistered lenses owed it would be gone with no log, no
// Health issue, and (until the convergence sweep lands) no healer.
//
// It answers the hazard cmd/refractor's own wiring already names for a
// different consumer: over a registry still loading, a SHORT answer is
// indistinguishable from a complete one, and Core KV — the persistent lens
// registry — is what separates the two.
//
// It is NOT, however, the same check that consumer runs, and the difference
// costs something. The retention-class consumer deliberately narrows to
// ReconcileNowForHolderType, precisely so one permanently-unactivatable lens
// somewhere else cannot withhold every attestation forever. No such narrowing
// exists for "the lenses a personal reprojection needs" — the probe narrows by
// declared Secure-Lens holder type, which is a different question — so this gate
// passes the corpus-global ReconcileNow and inherits exactly the failure that
// narrowing was introduced to avoid: in a deployment where any unrelated lens
// never registers, readiness is false forever, for everyone.
//
// RegistryHoldMax is therefore not a theoretical backstop here, it is the thing
// that makes the gate survivable, and the practical effect deserves saying
// plainly: on such a deployment EVERY process restart eats the full hold on the
// FIRST grant-change signal after boot, not merely during a genuinely-loading
// window. Nothing is lost when that happens — the signals are held in the
// coalescing set, not dropped, and the hold latches open once — but the fast
// path's first reaction is that much later than the sweep-free design implies.
// If that becomes real, the fix is a narrowing the probe does not offer today:
// reconcile only the lenses this reprojector has been asked to drive.
func (r *Reprojector) SetRegistryReady(fn func(context.Context) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registryReady = fn
}

// registryIsReady reports whether the drain may consume signals yet.
//
// It latches: once the registry has been observed complete, the check stops
// running. That bounds its cost — it is a Core KV enumeration, and the drain
// ticks every second — and it is sound for what the gate is FOR, which is the
// boot window.
//
// A lens hot-added later reopens a smaller window, and "smaller" is the honest
// word rather than "brief": it runs from the lens's Core-KV declaration being
// dispatched to its RegisterPersonal call, which is the whole of
// cmd/refractor's startPipeline in between — adapter construction, engine
// wiring, durable registration. That is not a sub-second gap, and a grant
// change landing inside it is missed for the new lens. Closing it is the
// convergence sweep's job, not this gate's.
//
// It is also skipped entirely while nothing is queued: an idle boot has no
// signal to lose, so it should not spend an enumeration per tick to say so.
func (r *Reprojector) registryIsReady(ctx context.Context) bool {
	r.mu.Lock()
	if r.registryLatched || r.registryReady == nil {
		r.mu.Unlock()
		return true
	}
	if len(r.dirty) == 0 {
		r.mu.Unlock()
		return false
	}
	check := r.registryReady
	if r.holdSince.IsZero() {
		r.holdSince = time.Now()
	}
	held, logged, holdMax := time.Since(r.holdSince), r.holdLogged, r.holdMax
	r.holdLogged = true
	r.mu.Unlock()

	err := check(ctx)
	if err == nil {
		r.latchRegistry()
		slog.Info("grantchange: lens registry is complete — draining the grant changes queued during boot",
			"heldFor", held.String())
		return true
	}
	if held > holdMax {
		r.latchRegistry()
		slog.Warn("grantchange: lens registry never reported complete within the hold bound — draining anyway; signals for a lens that is still missing will be lost until the convergence sweep covers them",
			"heldFor", held.String(), "bound", holdMax.String(), "reason", err)
		// Raised, not merely logged, for the same reason the overflow path is:
		// this is a degradation an operator has to be able to SEE, and every
		// other way this package can silently do less already raises one. It
		// fires once per process — the latch above guarantees it — so it costs
		// one Health write per registered lens, ever.
		detail := "drained against an incomplete lens registry after " + held.Round(time.Second).String() + ": " + err.Error()
		for _, p := range r.snapshotPersonal() {
			_ = p.RecordGrantReprojectIssue(ctx, "registry-incomplete", detail)
		}
		return true
	}
	if !logged {
		slog.Info("grantchange: holding grant changes until the lens registry finishes loading — reprojecting now would silently skip the lenses that have not registered yet",
			"reason", err)
	}
	return false
}

func (r *Reprojector) latchRegistry() {
	r.mu.Lock()
	r.registryLatched = true
	r.mu.Unlock()
}

// SetBounds overrides the dirty-set bound and the drain interval. Zero or
// negative leaves the default in place. It exists for tests, which need a drain
// they can drive deterministically and a bound they can reach.
func (r *Reprojector) SetBounds(maxDirty int, interval time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if maxDirty > 0 {
		r.maxDirty = maxDirty
	}
	if interval > 0 {
		r.interval = interval
	}
}

// RegisterPersonal adds one personal lens's pipeline to the registry the drain
// re-drives. Called at boot, from the same site that registers the lens's
// control-plane hydrator, where the concrete pipeline is in hand.
func (r *Reprojector) RegisterPersonal(ruleID string, p PersonalPipeline) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.personal[ruleID] = p
}

// DeregisterPersonal drops a lens from the registry — a deleted or replaced
// lens must stop being re-driven, and a pipeline that no longer runs cannot
// publish a frame for anyone.
func (r *Reprojector) DeregisterPersonal(ruleID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.personal, ruleID)
}

// GrantChanged records that one actor's read grants flipped
// (pipeline.GrantChangeSink). actorKey is the full Contract #1 vertex key the
// producer's own key-pattern inverse recovered.
//
// It never blocks and never does I/O: it runs inline on the producing
// pipeline's consumer goroutine, synchronous with the write it describes.
func (r *Reprojector) GrantChanged(actorKey string) {
	// projection.PersonalActorType is the SAME symbol InstallPersonalLens
	// configures the ActorEnumerator with, not a copy of the literal: the type
	// this drops on and the type a personal lens actually enumerates cannot be
	// allowed to drift, since the drift's failure mode is a reprojector that
	// silently accepts nothing.
	actorType, actorID, ok := substrate.ParseVertexKey(actorKey)
	if !ok || actorType != projection.PersonalActorType || actorID == "" {
		// A read-grant producer anchored on something other than an identity has
		// no personal lens keyed off it, so there is nothing to re-drive.
		// Dropping is the fail-slow direction, but it must not be SILENT: a
		// deployment that adds such a producer would otherwise have a wired,
		// permanently-dead edge with nothing anywhere to say so. Debug rather
		// than Warn because a correct deployment never reaches it, and one that
		// does reaches it on every write.
		slog.Debug("grantchange: grant change names a non-identity anchor — no personal lens is keyed off it, so no reprojection is owed",
			"actorKey", actorKey, "actorType", actorType, "want", projection.PersonalActorType)
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, already := r.dirty[actorID]; already {
		// The coalescing: N transitions for one actor between two drain ticks
		// cost one reprojection, not N.
		return
	}
	if len(r.dirty) >= r.maxDirty {
		r.dropped++
		return
	}
	r.dirty[actorID] = struct{}{}
}

// QueueDepth reports how many actors are waiting for a reprojection — the
// drain-queue gauge a mass grant change shows up in.
func (r *Reprojector) QueueDepth() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.dirty)
}

// Run drains the dirty set on a ticker until ctx is cancelled. One worker, one
// actor at a time, all that actor's pipelines in sequence — no new concurrency
// against the consumer goroutine beyond what Hydrate already established, and
// none at all between two reprojections.
func (r *Reprojector) Run(ctx context.Context) {
	r.mu.Lock()
	interval := r.interval
	r.mu.Unlock()

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Drain(ctx)
		}
	}
}

// Drain reprojects every actor currently dirty, then returns. Exported so a
// test can drive one pass deterministically instead of waiting on a ticker.
//
// Actors are taken one at a time rather than snapshotted as a batch, so a
// transition that lands mid-drain for an actor already processed re-dirties it
// and is picked up rather than silently folded into the pass that already ran.
func (r *Reprojector) Drain(ctx context.Context) {
	if !r.registryIsReady(ctx) {
		return
	}
	drained := 0
	for {
		if ctx.Err() != nil {
			return
		}
		// Reported from INSIDE the loop, not once at the top. The scenario the
		// bound exists for is a mass grant change that outpaces the drain — and
		// in exactly that scenario this loop does not return for a long time, so
		// a report fired only on entry would leave an operator with no evidence
		// for the whole storm. Every dropReportEvery actors is frequent enough
		// to be visible and rare enough not to write Health KV per actor.
		if drained%dropReportEvery == 0 {
			r.reportDropped(ctx)
		}
		actorID, ok := r.take()
		if !ok {
			// One last report on the way out, so a drop that landed after the
			// final in-loop check is not held until the next tick.
			r.reportDropped(ctx)
			return
		}
		r.reprojectActor(ctx, actorID)
		drained++
	}
}

// dropReportEvery is how many actors the drain processes between overflow
// reports while a storm is in progress.
const dropReportEvery = 50

// take removes and returns one dirty actor. Map iteration order makes the
// choice arbitrary, which is the right property here: every dirty actor is owed
// the same work, and an arbitrary order cannot starve one behind another the
// way a stable order could.
func (r *Reprojector) take() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for actorID := range r.dirty {
		delete(r.dirty, actorID)
		return actorID, true
	}
	return "", false
}

// reprojectActor re-drives one actor across every registered personal lens.
//
// A per-lens failure — a Capability KV read fault surfacing through the D1 gate
// is the live case — logs at Warn, raises the lens's own Health fault, and
// CONTINUES to the actor's remaining lenses. It does not re-enqueue: a
// persistent fault would spin the drain forever, starving every other actor,
// and the actor has a standing healer behind it. The Health fault is what keeps
// that from being a silent handoff.
func (r *Reprojector) reprojectActor(ctx context.Context, actorID string) {
	for _, ruleID := range r.registeredRuleIDs() {
		// Re-read the registry immediately before each call rather than trusting
		// the snapshot. Walking one actor across every personal lens is a window
		// seconds wide under load, and a lens deleted inside it is gone: its run
		// context is cancelled and its Health entry already removed. Calling it
		// anyway would fail, and the failure path does a read-modify-PUT that
		// RE-CREATES the entry the deleter just deleted — leaving an orphan
		// "degraded lens" row in Health KV with no lens behind it.
		p, live := r.registered(ruleID)
		if !live {
			continue
		}
		if err := p.ReprojectPersonalActor(ctx, actorID); err != nil {
			slog.Warn("grantchange: reprojection failed for one lens — continuing with this actor's remaining lenses; the convergence sweep is its healer",
				"ruleId", ruleID, "actorId", actorID, "err", err)
			// Checked again after the call, for the same reason: the
			// reprojection itself spans the window, and a lens deregistered
			// DURING it fails precisely because it was torn down.
			if _, stillLive := r.registered(ruleID); stillLive {
				p.RecordGrantReprojectIssue(ctx, "reproject", "actor "+actorID+": "+err.Error())
			}
		}
	}
}

// registered reports the pipeline currently registered under ruleID, if any.
func (r *Reprojector) registered(ruleID string) (PersonalPipeline, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.personal[ruleID]
	return p, ok
}

// registeredRuleIDs lists the registry's lens IDs, sorted, without holding the
// lock across the walk that follows — a producer's GrantChanged call must not
// block behind the drain it feeds.
//
// Sorted rather than map order so one actor's lenses are always visited in the
// same sequence: a partial pass (a cancelled context, a lens that failed
// halfway) then covers a reproducible prefix, which is the difference between a
// bug that reproduces and one that does not.
func (r *Reprojector) registeredRuleIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.personal))
	for id := range r.personal {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// snapshotPersonal copies the registry for callers that need the pipelines
// themselves rather than a walk order.
func (r *Reprojector) snapshotPersonal() map[string]PersonalPipeline {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]PersonalPipeline, len(r.personal))
	for id, p := range r.personal {
		out[id] = p
	}
	return out
}

// reportDropped raises the overflow fault, once per drain tick that follows one
// or more drops, on every registered personal lens.
//
// Every one of them IS degraded — the dropped actors are actors none of their
// pipelines will re-drive — so this is not fan-out for its own sake. It is
// reported here rather than at the drop so the producer's write path never
// waits on a Health KV write, and it is edge-triggered on the counter so a
// storm costs one report per tick rather than one per dropped signal.
func (r *Reprojector) reportDropped(ctx context.Context) {
	r.mu.Lock()
	dropped, reported, bound := r.dropped, r.droppedReported, r.maxDirty
	r.mu.Unlock()
	if dropped == reported {
		return
	}

	slog.Warn("grantchange: dirty-actor set is at its bound — grant-change signals were dropped; those actors converge only on the sweep",
		"dropped", dropped, "sinceLastReport", dropped-reported, "bound", bound)
	detail := "dropped " + strconv.Itoa(dropped) + " signal(s) cumulative at the " + strconv.Itoa(bound) + "-actor bound"

	// The counter is CUMULATIVE and is never reset; what advances is a separate
	// high-water of what has been reported, and only after a report actually
	// landed. Clearing a delta before the Health write — the shape this
	// replaced — loses the count outright whenever that write fails, and the
	// next pass then reports nothing at all: a silent overflow, which is the
	// one outcome the bound's whole contract forbids.
	landed := false
	for _, p := range r.snapshotPersonal() {
		if p.RecordGrantReprojectIssue(ctx, "overflow", detail) == nil {
			landed = true
		}
	}
	if !landed {
		// Nothing recorded it — either there are no personal lenses registered
		// yet or every Health write failed. Leave the high-water where it is so
		// the next pass tries again with the full count.
		return
	}
	r.mu.Lock()
	if dropped > r.droppedReported {
		r.droppedReported = dropped
	}
	r.mu.Unlock()
}
