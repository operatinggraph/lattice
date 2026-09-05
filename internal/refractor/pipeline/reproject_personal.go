package pipeline

import (
	"context"
	"fmt"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// actorPublishLock is one (lens, actor) publish slot. ch is a 1-buffered
// channel rather than a sync.Mutex so an acquire can be abandoned on context
// cancellation — a control-plane Hydrate answering a device attach must not
// block past its own RPC deadline behind a stalled drain-worker reprojection
// for the same actor. waiters is what keeps the map bounded: the slot is
// dropped as soon as nobody holds or wants it. The map that holds these is
// guarded by the pipeline's own personalPublishMu, never by the slot itself.
type actorPublishLock struct {
	ch      chan struct{}
	waiters int
	// hydrating counts the Hydrate calls currently holding this slot — 0 or 1
	// in practice, since the slot admits one holder, and a counter rather than
	// a flag so a second hydrate path could not clear the mark out from under
	// the first. It is what tells the CDC write loop that this actor's rows are
	// being republished WHOLE at a LOWER revision by a cold hydrate, which is
	// the one case a scoped publish leaves the device short
	// (personal-lens-delta-publication-design.md §4.6, guard 1). Guarded by the
	// pipeline's personalPublishMu, like waiters.
	hydrating int
}

// lockPersonalActor serializes everything one reprojection does for one actor —
// evaluate, write, publish, and the revision capture in the middle of it —
// against every other reprojection of the same actor on this lens.
//
// Three publishers can reach one (lens, actor): the grant-change drain worker,
// a live personal.hydrate, and (once it exists) the convergence sweeper.
// Nothing ordered them before. Each captures its own revision and publishes its
// own authoritative frame, and the client keeps whichever frame ARRIVED
// carrying the highest revision (edge/store/bolt.go's frameHW guard) — which is
// not the same thing as the freshest. Two publishers interleaving therefore let
// a stale frame win by arriving second.
//
// Holding the lock ACROSS the revision capture is what fixes it, not merely
// holding it across the publish: frames from these paths are then totally
// ordered per (lens, actor), so a later-captured revision is also a
// later-published one. Live CDC frames stay outside this lock deliberately —
// they are emitted from the single consumer goroutine at the message's own
// sequence, so they are already monotone and strictly fresher, and "freshest
// wins" is then the correct outcome of the client's guard rather than an
// accident of arrival order.
//
// The lock is per-pipeline, which IS per-lens, so the key is the actor alone.
// It lives here rather than in the reprojector on purpose: a lock a caller has
// to remember to take is one a caller can forget, and this one is load-bearing
// for a security filter's retraction.
//
// The acquire is abandonable. A reprojection holds this slot across a cypher
// evaluation, a write and a publish, which under load is a window seconds wide
// — and one of the three publishers is a control-plane RPC answering a device
// attach, with a deadline of its own. Waiting on ctx as well as on the slot
// lets that caller give up and report a timeout rather than block
// un-cancellably behind a drain worker. Returns a non-nil release func only
// when the slot was actually acquired.
func (p *Pipeline) lockPersonalActor(ctx context.Context, actorID string) (func(), error) {
	p.personalPublishMu.Lock()
	if p.personalPublishLocks == nil {
		p.personalPublishLocks = make(map[string]*actorPublishLock)
	}
	l, ok := p.personalPublishLocks[actorID]
	if !ok {
		l = &actorPublishLock{ch: make(chan struct{}, 1)}
		p.personalPublishLocks[actorID] = l
	}
	l.waiters++
	p.personalPublishMu.Unlock()

	// drop retires this caller's interest in the slot, reclaiming it once
	// nobody holds or wants it. Called on both the acquired and abandoned
	// paths, so an abandoned acquire cannot leak the entry.
	drop := func() {
		p.personalPublishMu.Lock()
		l.waiters--
		if l.waiters == 0 {
			delete(p.personalPublishLocks, actorID)
		}
		p.personalPublishMu.Unlock()
	}

	select {
	case l.ch <- struct{}{}:
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}

	return func() {
		<-l.ch
		drop()
	}, nil
}

// markHydrating records that the caller — which must already HOLD actorID's
// publish slot — is a Hydrate, and returns the func that clears the mark. The
// slot's entry cannot be reclaimed while it is held, so the mark always finds
// one; a caller that has not taken the slot would create an entry nobody ever
// drops, which is why this is unexported and called only from Hydrate.
func (p *Pipeline) markHydrating(actorID string) func() {
	p.personalPublishMu.Lock()
	l, ok := p.personalPublishLocks[actorID]
	if ok {
		l.hydrating++
	}
	p.personalPublishMu.Unlock()
	return func() {
		if !ok {
			return
		}
		p.personalPublishMu.Lock()
		l.hydrating--
		p.personalPublishMu.Unlock()
	}
}

// hydrateInFlight reports whether a Hydrate currently holds actorID's publish
// slot — the CDC write loop's guard 1
// (personal-lens-delta-publication-design.md §4.6).
//
// A hydrate publishes this actor's whole row set at a high-water it captured
// BEFORE evaluating, so its rows and its frame both sit BELOW a live event's
// sequence. A scoped live publish landing in that window would advance the
// device's frame high-water past the hydrate's, and every hydrate row for a key
// the device does not yet hold would then be dropped by the client's
// resurrection guard — leaving a cold device holding only the one row the scope
// admitted. Publishing that actor whole instead makes the live event's own
// output the complete set at its own, higher revision, which is what the client
// keeps.
//
// actorID is the bare NanoID the slot is keyed by, the same value a personal
// lens publishes as keys[adapter.PersonalActorKeyField].
func (p *Pipeline) hydrateInFlight(actorID string) bool {
	p.personalPublishMu.Lock()
	defer p.personalPublishMu.Unlock()
	l, ok := p.personalPublishLocks[actorID]
	return ok && l.hydrating > 0
}

// ReprojectPersonalActor re-evaluates this personal pipeline for one actor and
// publishes the resulting deltas plus an authoritative keyset frame. It is
// Hydrate without the terminal hydrationComplete marker
// (personal-lens-grant-change-trigger-design.md §4.1).
//
// It exists because a Personal Lens's output is a function of two inputs — its
// own Core-KV subgraph, which drives it through CDC, and the D1 read-grant
// projection, which drove nothing. This is the entry point that re-asks the
// security filter when that second input changes, for exactly the one actor it
// changed for.
//
// identityID is the bare NanoID, the same value Hydrate takes and the same one
// a "personal: true" lens publishes as keys[adapter.PersonalActorKeyField]; the
// full vtx.identity.<id> key is built internally.
//
// Three deliberate divergences from Hydrate, each load-bearing:
//
//   - The revision is captured AFTER the reprojection, not before. Hydrate
//     captures before so it UNDER-claims, which is right for a cold bulk
//     snapshot: the worst case is a row that arrives again anyway. It is fatal
//     for a retraction. The client drops a frame whose revision is below its
//     last applied high-water for the lens, and exempts from pruning any key
//     whose stored attribution revision exceeds the frame's
//     (edge/store/bolt.go). Both guards fail in the OVER-GRANT direction for an
//     under-claiming frame: the revoked key is either never examined or the
//     whole frame is discarded, and the stale row survives. A grant-triggered
//     frame that under-claims by construction is a retraction that cannot
//     retract.
//
//     Capturing after has a mirror-image cost — an over-claiming frame could
//     prune a row a concurrent live evaluation wrote at a higher sequence but
//     has not yet framed. That direction is UNDER-display, and it is the
//     correct side to fail on for a security filter. A pruned row is CONTENT,
//     so what restores it is the next event on that row, the standing healer's
//     content cycle (within grantchange.PersonalContentHealInterval) or a
//     hydrate — never the healer's ordinary frames-only pass, which converges
//     the key set and republishes no row.
//
//   - A missing actor publishes the EMPTY frame instead of erroring. A
//     tombstoned identity is the expected companion of a grant retraction, and
//     the empty frame is precisely the "you may now read nothing" signal;
//     erroring would drop the one case that most needs retracting. Hydrate errors
//     there for its own good reason — it answers a device's attach, and a device
//     asking to hydrate an identity that does not exist deserves to be told.
//
//   - No terminal hydrationComplete marker is published. That marker releases
//     the client's first-paint gate; emitting it mid-session would release a
//     gate that is not being held.
//
// scope decides which of the evaluated rows are WRITTEN; every non-delete
// result is framed whatever the scope says (personal-lens-delta-publication-
// design.md §4.3). A row the scope withholds is one the device already holds
// unchanged, and the frame naming it is what keeps it from being pruned. The
// zero value is ScopeAll, which publishes the whole actor. The one scope that
// frames nothing — ScopeSilent, this lens's rebuild replaying — publishes
// nothing at all and stamps nothing.
//
// The whole of it runs under the keyed (lens, actor) publish lock, revision
// capture included — see lockPersonalActor.
func (p *Pipeline) ReprojectPersonalActor(ctx context.Context, identityID string, scope PublishScope) error {
	actorKey := substrate.VertexKey("identity", identityID)

	unlock, err := p.lockPersonalActor(ctx, identityID)
	if err != nil {
		return fmt.Errorf("pipeline: reproject personal actor %q: awaiting the publish slot: %w", identityID, err)
	}
	defer unlock()

	// This entry point runs off the consumer goroutine, so it takes its own
	// rule snapshot — the same posture Hydrate takes, and no weaker. A rule
	// replaced mid-reprojection means this actor is reprojected under whichever
	// snapshot it took; the next transition or sweep pass corrects it.
	rs := p.ruleState()

	var results []ruleengine.EvalResult
	props, err := p.fetchVertexProps(ctx, actorKey)
	if err != nil {
		return fmt.Errorf("pipeline: reproject personal actor %q: %w", identityID, err)
	}
	if props != nil {
		results, err = p.reprojectActors(ctx, rs, []string{actorKey})
		if err != nil {
			return fmt.Errorf("pipeline: reproject personal actor %q: %w", identityID, err)
		}
	}
	// props == nil leaves results empty, which publishes the empty frame below.
	// Deliberately not routed through reprojectActors' own missing-actor arm:
	// that produces a capability-pipeline-shaped Delete whose Keys lack
	// PersonalActorKeyField, which a personal target rejects as a write error.

	// Captured here, after the evaluation and inside the lock — see the doc
	// above for why "after" is the whole point, and lockPersonalActor for why
	// the lock has to span it.
	revision := p.Progress().LastAppliedSeq
	if revision == 0 {
		// The same refusal Reproject makes with ErrNoOrderingToken, for the
		// same reason and in the same direction. A frame at revision 0 is not a
		// weaker frame, it is a frame the client discards: frameHW drops any
		// frame below the last one applied for the lens, and collectAttributed
		// exempts from pruning every key whose attribution revision exceeds the
		// frame's — which at 0 is all of them. Publishing one would report a
		// retraction that provably cannot retract, silently, in the over-grant
		// direction this whole mechanism exists to close.
		//
		// Reachable, not theoretical: the drain worker's ticker runs before
		// every personal pipeline's consumer has seeded its ack floor, and
		// seedAppliedSeqFromAckFloor leaves the token at zero when that read
		// fails outright. Refusing hands it to the caller, which logs it and
		// raises the lens's Health fault instead of serving a doomed frame.
		return fmt.Errorf("pipeline: reproject personal actor %q: %w", identityID, ErrNoOrderingToken)
	}

	if !scope.Frames() {
		// ScopeSilent, the one scope that withholds the frame as well as the
		// rows: this lens's rebuild is replaying, and every message a
		// reprojection would send — row, Delete or frame — sits below the frame
		// high-water a connected device already holds and is dropped there. So
		// nothing goes out and, because nothing went out, nothing stamps the
		// read model's last-touch clock either; a stamp for a publication that
		// never happened is exactly what LensProjectionStalled reads as health.
		//
		// No caller hands one here today — the sweeper passes All or None and
		// the drain passes Anchors or None — but this is the entry point every
		// non-CDC republish flows through, so it answers the scope rather than
		// assuming its callers.
		return nil
	}

	adpt := p.currentAdapter()
	// The reprojection rewrites the whole actor, so its publishes are pipelined
	// and their acks awaited once, below, before the frame. writeCtx carries
	// the pipeline; ctx does not, so the frame publishes synchronously.
	writeCtx := ctx
	var rowPipeline *substrate.PublishPipeline
	if opener, ok := adpt.(adapter.PublishPipelineOpener); ok {
		rowPipeline = opener.NewPublishPipeline()
		writeCtx = adapter.WithPublishPipeline(ctx, rowPipeline)
	}
	var frameKeys []map[string]any
	for _, result := range results {
		if result.Delete {
			err := adpt.Delete(writeCtx, result.Keys, revision)
			p.recordProjectionWrite()
			if err != nil {
				return fmt.Errorf("pipeline: reproject personal actor %q: write: %w", identityID, err)
			}
			continue
		}
		// Framed unconditionally, written only when the scope admits it. The
		// frame is the complete authoritative key set for this (lens, actor) at
		// this revision, so a key the scope withheld a row for must still be
		// named or the client prunes the copy it holds.
		frameKeys = append(frameKeys, result.Keys)
		if !scope.Admits(result) {
			continue
		}
		err := adpt.Upsert(writeCtx, result.Keys, result.Row, revision)
		p.recordProjectionWrite()
		if err != nil {
			return fmt.Errorf("pipeline: reproject personal actor %q: write: %w", identityID, err)
		}
	}

	// Every row is known stored before the frame describing them goes out: the
	// pipeline's flush is where the loop's acks are awaited.
	if rowPipeline != nil {
		if err := rowPipeline.Flush(ctx); err != nil {
			return fmt.Errorf("pipeline: reproject personal actor %q: write: %w", identityID, err)
		}
	}

	// The authoritative frame is the retraction transport: the client prunes
	// every key it holds for this lens that the frame omits. An empty frame is
	// a full retraction and is published as readily as any other.
	publisher, ok := adpt.(adapter.KeySetPublisher)
	if !ok {
		return fmt.Errorf("pipeline: reproject personal actor %q: target adapter %T publishes no keyset frame, so a re-evaluation could not retract anything", identityID, adpt)
	}
	if err := publisher.PublishKeySet(ctx, identityID, frameKeys, revision); err != nil {
		return fmt.Errorf("pipeline: reproject personal actor %q: keyset: %w", identityID, err)
	}
	// A SIGNALLED reprojection is real output, so its frame stamps the
	// read-model's last-touch clock like any landed write: a drain signal, an
	// interest change or the healer's content cycle each asked this lens a
	// question, and the frame is the whole answer whenever the admitted row set
	// is empty — an actor who may now read nothing is retracted BY the frame.
	//
	// A ScopeNone pass is not output. That is the standing healer turning over
	// the population on its own clock, re-asking the inclusion gates rather than
	// reacting to anything, and it reaches every registered personal lens every
	// pass. Stamping here would advance all of them forever, which is exactly
	// the signal LensProjectionStalled reads — lag sustained AND lastProjectedAt
	// not advancing (lens-projection-liveness-design.md) — so a personal lens
	// diverging silently could never be detected again.
	if scope.Kind() != ScopeKindNone {
		p.recordProjected()
	}
	return nil
}
