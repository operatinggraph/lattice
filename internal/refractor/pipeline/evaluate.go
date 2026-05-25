package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
	"github.com/asolgan/lattice/internal/substrate"
)

// ErrSkipProjection signals that an EnvelopeFn declined a row — the
// pipeline drops it without writing or erroring. Used by the Capability
// envelope to suppress projections the cypher produced over zero
// MATCH-bindings (no real actor).
var ErrSkipProjection = errors.New("pipeline: envelope: skip projection")

// evaluateForEntry runs the per-engine evaluate path against entry and
// returns the normalised []simple.EvalResult shape the write loop expects.
// The simple engine delegates to simple.Evaluate; the full engine binds
// `$actorKey`, `$now`, `$projectedAt` from the event/clock and calls
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
			delKey := capabilityKeyForActor(entry.CoreKVKey)
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
		params := map[string]any{
			"actorKey":    entry.CoreKVKey,
			"projectedAt": time.Now().UTC().Format(time.RFC3339),
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
	params := map[string]any{
		"actorKey":    actorKey,
		"now":         now.Format(time.RFC3339),
		"projectedAt": now.Format(time.RFC3339),
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
			delKey := capabilityKeyForActor(actorKey)
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
