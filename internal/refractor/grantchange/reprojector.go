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
	"strconv"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// PersonalActorType is the vertex type a personal reprojection is keyed by.
// Personal Lens actors are always identities (the ActorEnumerator's configured
// actorType), which is why the dirty set holds bare NanoIDs rather than full
// vertex keys — and why a transition naming any other anchor type is dropped
// rather than guessed at.
const PersonalActorType = "identity"

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
	// reprojection that did not happen.
	RecordGrantReprojectIssue(ctx context.Context, kind, detail string)
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
	// dropped counts signals refused since the last drain tick reported them.
	// It is a counter rather than a flag so the Health issue can say how much
	// was lost, and it is reported from the drain worker rather than from the
	// producer's write path so raising it never blocks a write.
	dropped int
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
	}
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
	actorType, actorID, ok := substrate.ParseVertexKey(actorKey)
	if !ok || actorType != PersonalActorType || actorID == "" {
		// A read-grant producer anchored on something other than an identity
		// has no personal lens keyed off it, so there is nothing to re-drive.
		// Dropping is the fail-slow direction and it is silent by design: a
		// deployment that adds such a producer would otherwise log per write.
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
	r.reportDropped(ctx)
	for {
		if ctx.Err() != nil {
			return
		}
		actorID, ok := r.take()
		if !ok {
			return
		}
		r.reprojectActor(ctx, actorID)
	}
}

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
	for ruleID, p := range r.snapshotPersonal() {
		if err := p.ReprojectPersonalActor(ctx, actorID); err != nil {
			slog.Warn("grantchange: reprojection failed for one lens — continuing with this actor's remaining lenses; the convergence sweep is its healer",
				"ruleId", ruleID, "actorId", actorID, "err", err)
			p.RecordGrantReprojectIssue(ctx, "reproject", "actor "+actorID+": "+err.Error())
		}
	}
}

// snapshotPersonal copies the registry so a reprojection pass never holds the
// lock across a cypher evaluation — a producer's GrantChanged call must not
// block behind the drain it feeds.
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
	dropped := r.dropped
	r.dropped = 0
	bound := r.maxDirty
	r.mu.Unlock()
	if dropped == 0 {
		return
	}

	slog.Warn("grantchange: dirty-actor set is at its bound — grant-change signals were dropped; those actors converge only on the sweep",
		"dropped", dropped, "bound", bound)
	detail := "dropped " + strconv.Itoa(dropped) + " signal(s) at the " + strconv.Itoa(bound) + "-actor bound"
	for _, p := range r.snapshotPersonal() {
		p.RecordGrantReprojectIssue(ctx, "overflow", detail)
	}
}
