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
func (p *Pipeline) Hydrate(ctx context.Context, identityID string) (uint64, error) {
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

	results, err := p.reprojectActors(ctx, []string{actorKey})
	if err != nil {
		return 0, fmt.Errorf("pipeline: hydrate %q: %w", identityID, err)
	}

	adpt := p.currentAdapter()
	var frameKeys []map[string]any
	for _, result := range results {
		var writeErr error
		if result.Delete {
			writeErr = adpt.Delete(ctx, result.Keys, highWater)
		} else {
			writeErr = adpt.Upsert(ctx, result.Keys, result.Row, highWater)
			frameKeys = append(frameKeys, result.Keys)
		}
		if writeErr != nil {
			return 0, fmt.Errorf("pipeline: hydrate %q: write: %w", identityID, writeErr)
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

	if marker, ok := adpt.(adapter.HydrationMarkerPublisher); ok {
		if err := marker.PublishHydrationComplete(ctx, identityID, highWater); err != nil {
			return 0, fmt.Errorf("pipeline: hydrate %q: marker: %w", identityID, err)
		}
	}

	return highWater, nil
}
