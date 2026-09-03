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
//     has not yet framed. That direction is UNDER-display, the next event or
//     sweep pass recovers it, and it is the correct side to fail on for a
//     security filter.
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
// The whole of it runs under the keyed (lens, actor) publish lock, revision
// capture included — see lockPersonalActor.
func (p *Pipeline) ReprojectPersonalActor(ctx context.Context, identityID string) error {
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
		err := adpt.Upsert(writeCtx, result.Keys, result.Row, revision)
		p.recordProjectionWrite()
		if err != nil {
			return fmt.Errorf("pipeline: reproject personal actor %q: write: %w", identityID, err)
		}
		frameKeys = append(frameKeys, result.Keys)
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
	return nil
}
