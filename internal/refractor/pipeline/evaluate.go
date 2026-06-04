package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
	"github.com/asolgan/lattice/internal/substrate"
)

// ErrSkipProjection signals that an EnvelopeFn declined a row — the
// pipeline drops it without writing or erroring. Used by the Capability
// envelope to suppress projections the cypher produced over zero
// MATCH-bindings (no real actor).
var ErrSkipProjection = errors.New("pipeline: envelope: skip projection")

// ErrDeleteProjection signals that an EnvelopeFn declined to project a row
// AND that the row's target key must be deleted (not merely skipped). The
// pipeline synthesizes a Delete against the keys the envelope returned. Used
// by the ephemeral-grant envelope: a live actor whose grants have all
// expired/been removed produces no real grant, so its `cap.ephemeral.<actor>`
// key must be hard-deleted (absence = denial, Contract #6 §6.8). Unlike
// ErrSkipProjection (drop silently, leave any existing key untouched), this
// actively removes the target.
var ErrDeleteProjection = errors.New("pipeline: envelope: delete projection")

// ErrNoProvenanceTimestamp signals that an anchor vertex body carried no
// usable commit-provenance timestamp (neither lastModifiedAt nor createdAt),
// so a deterministic projectedAt cannot be derived. The pipeline surfaces
// this rather than substituting a wall-clock value.
var ErrNoProvenanceTimestamp = errors.New("pipeline: anchor vertex carries no commit-provenance timestamp")

// projectedAtFromProvenance derives the deterministic projectedAt value for a
// capability projection from the anchor vertex body's commit provenance. The
// universal Core KV envelope (Contract #1 §1.3) records the committing op's
// timestamp as lastModifiedAt (updated on every commit; equal to createdAt on
// creation). Using it makes projectedAt a pure function of the input state, so
// replay/rebuild over the same vertex yields an identical value — it is
// provenance ("as-of input state"), never a wall-clock read.
func projectedAtFromProvenance(nodeProps map[string]any) (string, error) {
	if nodeProps != nil {
		if v, ok := nodeProps["lastModifiedAt"].(string); ok && v != "" {
			return v, nil
		}
		if v, ok := nodeProps["createdAt"].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", ErrNoProvenanceTimestamp
}

// evaluateForEntry runs the per-engine evaluate path against entry and
// returns the normalised []simple.EvalResult shape the write loop expects.
// The simple engine delegates to simple.Evaluate; the full engine binds
// `$actorKey`, `$now`, `$projectedAt` from the event/provenance and calls
// full.Engine.ExecuteWith. When an EnvelopeFn is installed, each row
// is rewritten before being handed to the adapter.
func (p *Pipeline) evaluateForEntry(ctx context.Context, entry simple.NodeEntry) ([]simple.EvalResult, error) {
	switch p.engineKind {
	case ruleengine.EngineFull:
		if p.fullEngine == nil || p.fullCR == nil {
			return nil, fmt.Errorf("pipeline: full engine selected but engine/compiled rule unset for rule %q", p.ruleID)
		}

		// Cross-vertex fan-out: on a non-actor event with an ActorEnumerator
		// installed, expand the event into the set of affected actors and
		// re-execute the cypher per actor so their capability set is
		// re-projected with the updated topology.
		if p.actorEnumerator != nil {
			eventType, _, _ := substrate.ParseVertexKey(entry.CoreKVKey)
			if eventType != p.actorEnumerator.actorType {
				return p.evaluateFanOut(ctx, entry)
			}
		}

		// Actor tombstone shortcut: emit a Delete against the Capability KV
		// target key so the cap entry is removed when an identity vertex is
		// soft-deleted. Only the actor-aware pipeline (ActorEnumerator installed)
		// takes this path — other lenses let the cypher re-execute normally.
		if entry.IsDeleted && p.actorEnumerator != nil {
			delKey := p.actorDeleteKeyFor(entry.CoreKVKey)
			return []simple.EvalResult{{
				Delete: true,
				Keys:   map[string]any{"key": delKey},
				Row:    nil,
			}}, nil
		}

		results, err := p.executeFullForActor(ctx, entry.CoreKVKey, entry.Properties)
		if err != nil {
			return nil, err
		}
		return results, nil

	default:
		// Simple engine — unchanged behaviour modulo optional envelope
		// wrap (Phase C may install one for capability lenses authored
		// against the simple engine; in practice the seeded capability
		// lenses use the full engine, so this path stays a no-op for
		// 3.2a).
		results, err := simple.Evaluate(ctx, p.currentPlan(), entry, p.adjKV, p.coreKV)
		if err != nil {
			return nil, err
		}
		if p.envelopeFn == nil {
			return results, nil
		}
		projectedAt, perr := projectedAtFromProvenance(entry.Properties)
		if perr != nil {
			return nil, fmt.Errorf("pipeline: projectedAt for %q: %w", entry.CoreKVKey, perr)
		}
		params := map[string]any{
			"actorKey":    entry.CoreKVKey,
			"projectedAt": projectedAt,
		}
		filtered := results[:0]
		for i := range results {
			if results[i].Delete {
				filtered = append(filtered, results[i])
				continue
			}
			newRow, newKeys, envErr := p.envelopeFn(results[i].Row, results[i].Keys, params)
			if errors.Is(envErr, ErrSkipProjection) {
				continue
			}
			if errors.Is(envErr, ErrDeleteProjection) {
				results[i].Delete = true
				results[i].Keys = newKeys
				results[i].Row = nil
				filtered = append(filtered, results[i])
				continue
			}
			if envErr != nil {
				return nil, fmt.Errorf("pipeline: envelope: %w", envErr)
			}
			results[i].Row = newRow
			results[i].Keys = newKeys
			filtered = append(filtered, results[i])
		}
		return filtered, nil
	}
}

// executeFullForActor runs the full-engine cypher against a single
// actor key and wraps each row through envelopeFn (when installed).
// nodeProps is the actor vertex's stored Core KV body; it's passed
// through to the engine's EventContext so the executor can resolve
// the anchor without an extra Core KV read.
func (p *Pipeline) executeFullForActor(ctx context.Context, actorKey string, nodeProps map[string]any) ([]simple.EvalResult, error) {
	start := time.Now()
	now := start.UTC()
	projectedAt, perr := projectedAtFromProvenance(nodeProps)
	if perr != nil {
		return nil, fmt.Errorf("pipeline: projectedAt for %q: %w", actorKey, perr)
	}
	params := map[string]any{
		"actorKey":    actorKey,
		"now":         now.Format(time.RFC3339),
		"projectedAt": projectedAt,
	}
	out, err := p.fullEngine.ExecuteWith(ctx, p.fullCR,
		ruleengine.EventContext{
			NodeKey:    actorKey,
			NodeProps:  nodeProps,
			Parameters: params,
		}, p.adjKV, p.coreKV)
	if err != nil {
		return nil, err
	}
	results := make([]simple.EvalResult, 0, len(out))
	for _, r := range out {
		row := r.Values
		keys := r.Key
		if p.envelopeFn != nil {
			newRow, newKeys, envErr := p.envelopeFn(row, keys, params)
			if errors.Is(envErr, ErrSkipProjection) {
				continue
			}
			if errors.Is(envErr, ErrDeleteProjection) {
				results = append(results, simple.EvalResult{
					Delete: true,
					Keys:   newKeys,
					Row:    nil,
				})
				continue
			}
			if envErr != nil {
				return nil, fmt.Errorf("pipeline: envelope: %w", envErr)
			}
			row = newRow
			keys = newKeys
		}
		results = append(results, simple.EvalResult{
			Delete: r.Delete,
			Keys:   keys,
			Row:    row,
		})
	}
	// Record per-event projection latency for the heartbeat aggregator.
	// The buffer is cheap (single atomic-protected ring slot per insert)
	// so calling it on every fan-out actor is fine.
	if p.latencyBuf != nil {
		p.latencyBuf.Record(time.Since(start))
	}
	return results, nil
}

// evaluateFanOut handles the cross-vertex fan-out path: the CDC event arrived
// on a non-actor vertex; enumerate affected actors and re-execute the cypher
// per actor. Each actor's result set is appended to the returned []EvalResult
// — the pipeline write loop handles each result row independently.
func (p *Pipeline) evaluateFanOut(ctx context.Context, entry simple.NodeEntry) ([]simple.EvalResult, error) {
	eventType, _, _ := substrate.ParseVertexKey(entry.CoreKVKey)
	actorKeys, err := p.actorEnumerator.Enumerate(ctx, entry.CoreKVKey, eventType)
	if err != nil {
		return nil, fmt.Errorf("pipeline: fan-out enumerate: %w", err)
	}
	// No affected actors → no projection to write. This is a valid
	// outcome (e.g. a role with no assignments yet, or a service in a
	// location no actor sits inside).
	if len(actorKeys) == 0 {
		return nil, nil
	}
	return p.reprojectActors(ctx, actorKeys)
}

// evaluateLinkFanOut handles a link CDC event (create or tombstone) on the
// actor-aware pipeline. A pure link mutation (e.g. holdsRole, grantedBy)
// carries no vertex change, so the only way affected actors are reprojected
// is to seed the fan-out from BOTH link endpoints.
//
// Adjacency consistency: the dedicated adjacency consumer
// (internal/refractor/consumer/bootstrap.go) and this pipeline both react to
// the same link event with no cross-consumer ordering guarantee. Before
// enumerating, we idempotently apply the link to adjKV ourselves (mirroring
// processLinkEnvelope) so the reprojection cypher sees a consistent edge set
// regardless of which consumer reached the link first. adjacency.Build upserts
// (create) / removes (tombstone) by EdgeID, so the dedicated consumer's later
// Build for the same edge is a no-op. This guarantees the reprojection never
// races ahead of the edge that triggered it.
func (p *Pipeline) evaluateLinkFanOut(ctx context.Context, linkKey string, isDeleted bool) ([]simple.EvalResult, error) {
	srcType, srcID, linkName, dstType, dstID, ok := substrate.ParseLinkKey(linkKey)
	if !ok {
		// ClassifyKey already gated KindLink; unreachable in practice.
		return nil, fmt.Errorf("pipeline: link fan-out: not a Contract #1 link key: %q", linkKey)
	}

	// Idempotently reflect this link in adjKV before enumerating. The link key
	// is its own EdgeID (Contract #1 link keys are globally unique), so a
	// create upserts and a tombstone removes by that EdgeID — matching the
	// dedicated consumer's directional events exactly.
	outbound := adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: linkKey, Name: linkName, Direction: "outbound",
		NodeID: srcID, OtherNodeID: dstID, OtherType: dstType, IsDeleted: isDeleted,
	}
	inbound := adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: linkKey, Name: linkName, Direction: "inbound",
		NodeID: dstID, OtherNodeID: srcID, OtherType: srcType, IsDeleted: isDeleted,
	}
	for _, evt := range []adjacency.CoreKVEvent{outbound, inbound} {
		if err := adjacency.Build(ctx, p.adjKV, evt); err != nil {
			return nil, fmt.Errorf("pipeline: link fan-out: adjacency build for %q: %w", linkKey, err)
		}
	}

	// Seed the actor enumeration from BOTH endpoint vertices and union the
	// results. Either endpoint may be (or reach) an actor.
	srcVtx := substrate.VertexKey(srcType, srcID)
	dstVtx := substrate.VertexKey(dstType, dstID)

	actorSet := map[string]struct{}{}
	for _, ep := range []struct{ key, typ string }{{srcVtx, srcType}, {dstVtx, dstType}} {
		actors, err := p.actorEnumerator.Enumerate(ctx, ep.key, ep.typ)
		if err != nil {
			return nil, fmt.Errorf("pipeline: link fan-out enumerate from %q: %w", ep.key, err)
		}
		for _, a := range actors {
			actorSet[a] = struct{}{}
		}
	}
	if len(actorSet) == 0 {
		// A link whose endpoints reach no actors (e.g. a book→author link)
		// is a correct no-op.
		return nil, nil
	}
	actorKeys := make([]string, 0, len(actorSet))
	for a := range actorSet {
		actorKeys = append(actorKeys, a)
	}
	return p.reprojectActors(ctx, actorKeys)
}

// evaluateAspectFanOut handles an aspect CDC event (mutation or tombstone) on
// the actor-aware pipeline. An aspect-only mutation (e.g. identity .state,
// role .description) carries no vertex-root change, so affected actors are
// reprojected by seeding the fan-out from the aspect's parent vertex.
//
// When the parent vertex is itself an actor (e.g. an identity .state flip), the
// enumerator returns it as a singleton and only that actor is reprojected. When
// the parent is a non-actor vertex (e.g. a role .description), the enumerator
// walks adjacency to the actors that reach it. Adjacency is untouched — an
// aspect change never alters graph topology — so, unlike the link fan-out, no
// adjacency.Build is performed here.
func (p *Pipeline) evaluateAspectFanOut(ctx context.Context, aspectKey string) ([]simple.EvalResult, error) {
	parentVtx, parentType, _, _, ok := substrate.ParseAspectKey(aspectKey)
	if !ok {
		// ClassifyKey already gated KindAspect; unreachable in practice.
		return nil, fmt.Errorf("pipeline: aspect fan-out: not a Contract #1 aspect key: %q", aspectKey)
	}

	actorKeys, err := p.actorEnumerator.Enumerate(ctx, parentVtx, parentType)
	if err != nil {
		return nil, fmt.Errorf("pipeline: aspect fan-out enumerate from %q: %w", parentVtx, err)
	}
	// No affected actors → no projection to write (e.g. a meta-vertex aspect,
	// or a vertex no actor reaches). A correct no-op.
	if len(actorKeys) == 0 {
		return nil, nil
	}
	return p.reprojectActors(ctx, actorKeys)
}

// reprojectActors re-executes the capability cypher for each actor key and
// returns the concatenated result set. A missing (tombstoned) actor yields a
// Delete against its Capability KV key. Shared by the vertex fan-out
// (evaluateFanOut) and the link fan-out (evaluateLinkFanOut).
func (p *Pipeline) reprojectActors(ctx context.Context, actorKeys []string) ([]simple.EvalResult, error) {
	var all []simple.EvalResult
	for _, actorKey := range actorKeys {
		// Fetch the actor's properties via Core KV so the engine can
		// resolve the anchor `MATCH (identity {key: $actorKey})`
		// without scanning. Missing actors are skipped — they may have
		// been tombstoned out from under a stale adjacency edge.
		entryProps, err := p.fetchVertexProps(ctx, actorKey)
		if err != nil {
			return nil, fmt.Errorf("pipeline: fan-out fetch %q: %w", actorKey, err)
		}
		if entryProps == nil {
			// Actor missing → emit a Delete (cap key) so the Capability
			// KV reflects the disappearance. This case can occur if the
			// actor was tombstoned but its adjacency hasn't been
			// pruned yet.
			delKey := p.actorDeleteKeyFor(actorKey)
			all = append(all, simple.EvalResult{
				Delete: true,
				Keys:   map[string]any{"key": delKey},
				Row:    nil,
			})
			continue
		}
		res, err := p.executeFullForActor(ctx, actorKey, entryProps)
		if err != nil {
			return nil, err
		}
		all = append(all, res...)
	}
	return all, nil
}

// fetchVertexProps point-reads a vertex from Core KV and returns its
// properties (or nil if missing / soft-deleted).
func (p *Pipeline) fetchVertexProps(ctx context.Context, vtxKey string) (map[string]any, error) {
	entry, err := p.coreKV.Get(ctx, vtxKey)
	if err != nil {
		// Use the JetStream-typed error path indirectly via Classify:
		// if the key isn't found, return (nil, nil). For other errors,
		// surface so the caller can decide retry/structural handling.
		// The substrate doesn't export the ErrKeyNotFound type from
		// here without an import; instead we accept any error as
		// "missing" only when the data is genuinely absent. To stay
		// type-safe we use a soft check.
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if entry == nil || len(entry.Value()) == 0 {
		return nil, nil
	}
	var props map[string]any
	if jerr := json.Unmarshal(entry.Value(), &props); jerr != nil {
		return nil, jerr
	}
	if isDel, _ := props["isDeleted"].(bool); isDel {
		return nil, nil
	}
	return props, nil
}

// actorDeleteKeyFor derives the Capability KV key to delete when actorKey
// disappears, using the lens-specific derivation when one is installed and
// falling back to the primary cap.<actor> shape otherwise.
func (p *Pipeline) actorDeleteKeyFor(actorKey string) string {
	if p.actorDeleteKey != nil {
		return p.actorDeleteKey(actorKey)
	}
	return capabilityKeyForActor(actorKey)
}

// capabilityKeyForActor derives the Capability KV target key
// (cap.<type>.<id>) from an actor vertex key (vtx.<type>.<id>).
// Mirrors capabilityenv.capabilityKey but lives here to avoid a
// circular import (capabilityenv imports pipeline for EnvelopeFn).
func capabilityKeyForActor(actorKey string) string {
	if rest, ok := strings.CutPrefix(actorKey, substrate.VertexPrefix+"."); ok {
		return "cap." + rest
	}
	return "cap." + actorKey
}
