package pipeline

import (
	"context"
	"fmt"
	"sync"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// actorPublishLock is one (lens, actor) publish slot. waiters is what keeps the
// map bounded: the slot is dropped as soon as nobody holds or wants it, so a
// cell with a large identity population never accumulates a mutex per identity.
// It is guarded by the pipeline's own personalPublishMu, never by itself.
type actorPublishLock struct {
	mu      sync.Mutex
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
func (p *Pipeline) lockPersonalActor(actorID string) func() {
	p.personalPublishMu.Lock()
	if p.personalPublishLocks == nil {
		p.personalPublishLocks = make(map[string]*actorPublishLock)
	}
	l, ok := p.personalPublishLocks[actorID]
	if !ok {
		l = &actorPublishLock{}
		p.personalPublishLocks[actorID] = l
	}
	l.waiters++
	p.personalPublishMu.Unlock()

	l.mu.Lock()

	return func() {
		l.mu.Unlock()
		p.personalPublishMu.Lock()
		l.waiters--
		if l.waiters == 0 {
			delete(p.personalPublishLocks, actorID)
		}
		p.personalPublishMu.Unlock()
	}
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

	unlock := p.lockPersonalActor(identityID)
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

	adpt := p.currentAdapter()
	var frameKeys []map[string]any
	for _, result := range results {
		if result.Delete {
			if err := adpt.Delete(ctx, result.Keys, revision); err != nil {
				return fmt.Errorf("pipeline: reproject personal actor %q: write: %w", identityID, err)
			}
			continue
		}
		if err := adpt.Upsert(ctx, result.Keys, result.Row, revision); err != nil {
			return fmt.Errorf("pipeline: reproject personal actor %q: write: %w", identityID, err)
		}
		frameKeys = append(frameKeys, result.Keys)
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
