package pipeline

import (
	"context"
	"fmt"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Hydrate performs a cold bulk projection of one identity's full authorized +
// interested slice (personal-secure-lens-design.md §3.5, Fire PL.4 — the
// "personal.hydrate" control RPC's cold path). It re-executes the personal
// cypher for that one actor via the same reprojectActors machinery the live
// cross-vertex fan-out uses (§2.1's ActorEnumerator-driven reprojection —
// here run for a single actor rather than a fan-out set discovered from a
// CDC event), publishes each resulting row through the active adapter
// exactly as the live path does, then — if the adapter supports it —
// publishes a terminal hydrationComplete marker carrying the high-water
// revision. Returns that revision.
//
// The high-water revision is this pipeline's own CDC forward-progress
// (Progress().LastAppliedSeq), captured BEFORE reprojection runs: any live
// incremental delta the pipeline applies concurrently with or after this call
// necessarily carries a revision >= this snapshot's, so the Edge's
// last-writer-wins-by-revision resolution can never let a bulk hydration
// snapshot regress a fresher incremental delta that raced it.
//
// identityID is the bare NanoID (the same value a "personal: true" lens
// publishes as keys[adapter.PersonalActorKeyField] and the control plane's
// register/deregister ops key the Interest Set by) — Hydrate builds the full
// vtx.identity.<id> key internally. Personal Lens actors are always
// identities (ActorEnumerator's actorType, Fire PL.2); Hydrate does not take
// an actor type.
// Hydrate holds the same keyed (lens, actor) publish lock
// ReprojectPersonalActor does, across its own revision capture and its frame.
// Both publish an authoritative keyset frame for one (lens, actor) from off the
// consumer goroutine, and the client keeps whichever frame ARRIVED carrying the
// higher revision — so two of them interleaving would let the stale one win by
// arriving second. Hydrate captures its revision BEFORE reprojecting and the
// reprojection path captures AFTER (each correct for its own job,
// reproject_personal.go), which makes the ordering matter more, not less: the
// lock is what stops the two captures from being interleaved with the two
// publishes.
//
// That under-claiming capture is also what puts a cold hydrate BEHIND the live
// consumer, which does not take this lock: a scoped live publish landing
// mid-hydrate carries a frame above this one's, and the device would then drop
// this hydrate's frame and every row it does not already hold. The two guards
// of personal-lens-delta-publication-design.md §4.6 close it — the mark this
// call sets, which makes a live event publish the whole actor, and the wait
// below for the one event that was already past that decision. Neither touches
// the capture itself: capturing after, as the reprojection path does, would let
// an over-claiming hydrate frame prune a row a concurrent live event wrote.
func (p *Pipeline) Hydrate(ctx context.Context, identityID string) (uint64, error) {
	// Abandonable on ctx: this call answers a device-attach RPC with a deadline
	// of its own, and the slot it wants can be held across a drain worker's
	// whole evaluate-write-publish for the same actor.
	unlock, err := p.lockPersonalActor(ctx, identityID)
	if err != nil {
		return 0, fmt.Errorf("pipeline: hydrate %q: awaiting the publish slot: %w", identityID, err)
	}
	defer unlock()

	// The mark is taken after the slot and dropped before it, so it stands for
	// every moment this call could publish anything. From here on, an event
	// reaching its scope decision sees a hydrate in flight for this actor and
	// publishes that actor WHOLE — guard 1 of §4.6.
	unmark := p.markHydrating(identityID)
	defer unmark()

	// Guard 2 (personal-lens-delta-publication-design.md §4.6). An event that
	// made its scope decision BEFORE the mark was set is already past guard 1:
	// it can publish a scoped row set and a frame at its own, HIGHER sequence
	// while this hydrate is still evaluating, and the client would then drop
	// this hydrate's frame and every row it carries for a key the device does
	// not yet hold — leaving a cold device holding one row. Waiting for that
	// one event's handler to return before capturing the high-water closes it:
	// afterwards the applied cursor is a complete statement about that event,
	// and every LATER event sees the mark.
	//
	// At most one event can be in that position. The consumer goroutine stamps
	// the sequence it is entering under the same lock this reads, so an event
	// that started before this read is named here, and one that starts after it
	// finds the mark already set. The wait is bounded by the attach RPC's own
	// context, and it waits for the handler to LEAVE rather than for the cursor
	// to reach the sequence, because a Naked event never advances the cursor.
	if inFlight := p.handlingSequence(); inFlight != 0 {
		if werr := p.awaitHandlerLeft(ctx, inFlight); werr != nil {
			return 0, fmt.Errorf("pipeline: hydrate %q: awaiting the event in flight at seq %d: %w", identityID, inFlight, werr)
		}
	}

	highWater := p.Progress().LastAppliedSeq
	actorKey := substrate.VertexKey("identity", identityID)

	// Fail with a clear "no such identity" rather than letting a nonexistent
	// actor fall through to reprojectActors' capability-pipeline-shaped
	// missing-actor Delete (Keys lacks PersonalActorKeyField for a personal
	// lens, since InstallPersonalLens installs no SetActorDeleteKey override —
	// that path would surface as an opaque "__actor absent from keys" write
	// error instead of a clean not-found).
	props, err := p.fetchVertexProps(ctx, actorKey)
	if err != nil {
		return 0, fmt.Errorf("pipeline: hydrate %q: %w", identityID, err)
	}
	if props == nil {
		return 0, fmt.Errorf("pipeline: hydrate %q: no such identity", identityID)
	}

	// This entry point runs off the consumer goroutine (Hydrate is called by the
	// personal-lens attach path), so it takes its own rule snapshot — see ruleMu.
	rs := p.ruleState()

	results, err := p.reprojectActors(ctx, rs, []string{actorKey})
	if err != nil {
		return 0, fmt.Errorf("pipeline: hydrate %q: %w", identityID, err)
	}

	adpt := p.currentAdapter()
	// A cold hydrate is the widest write loop the personal path runs — every
	// row of an actor at once — so it pipelines its publishes and awaits the
	// acks once, below, before the frame. writeCtx carries the pipeline; ctx
	// does not, so the frame and the marker publish synchronously.
	writeCtx := ctx
	var rowPipeline *substrate.PublishPipeline
	if opener, ok := adpt.(adapter.PublishPipelineOpener); ok {
		rowPipeline = opener.NewPublishPipeline()
		writeCtx = adapter.WithPublishPipeline(ctx, rowPipeline)
	}
	var frameKeys []map[string]any
	for _, result := range results {
		var writeErr error
		if result.Delete {
			writeErr = adpt.Delete(writeCtx, result.Keys, highWater)
		} else {
			writeErr = adpt.Upsert(writeCtx, result.Keys, result.Row, highWater)
			frameKeys = append(frameKeys, result.Keys)
		}
		p.recordProjectionWrite()
		if writeErr != nil {
			return 0, fmt.Errorf("pipeline: hydrate %q: write: %w", identityID, writeErr)
		}
	}

	// Every row is known stored before the frame that claims to describe them
	// is published: the pipeline's flush is where the loop's acks are awaited.
	if rowPipeline != nil {
		if err := rowPipeline.Flush(ctx); err != nil {
			return 0, fmt.Errorf("pipeline: hydrate %q: write: %w", identityID, err)
		}
	}

	// A keyset frame at highWater — the complete authoritative set this cold
	// bulk projection just published — lets the cold reconnect prune
	// whatever dropped out since the device's last mirror, exactly like a
	// live retraction (personal-lens-retraction-design.md §3.4). Published
	// before the terminal marker so a client observing the marker has
	// already seen the frame.
	if publisher, ok := adpt.(adapter.KeySetPublisher); ok {
		if err := publisher.PublishKeySet(ctx, identityID, frameKeys, highWater); err != nil {
			return 0, fmt.Errorf("pipeline: hydrate %q: keyset: %w", identityID, err)
		}
	}

	// A hydrate is real output — a device asked for this actor and the rows and
	// frame it asked for have landed — so it stamps the read-model's last-touch
	// clock, the same posture a signalled ReprojectPersonalActor's frame takes.
	// Stamped outside the keyset assertion because the stamp is about the
	// hydrate, not the adapter's shape: an actor whose whole set is empty writes
	// no row, and a clock left frozen through such a hydrate reads as though
	// nothing had been published.
	p.recordProjected()

	if marker, ok := adpt.(adapter.HydrationMarkerPublisher); ok {
		if err := marker.PublishHydrationComplete(ctx, identityID, highWater); err != nil {
			return 0, fmt.Errorf("pipeline: hydrate %q: marker: %w", identityID, err)
		}
	}

	return highWater, nil
}
