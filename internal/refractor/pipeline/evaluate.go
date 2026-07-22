package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
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

// evaluateForEntry runs the full-engine evaluate path against entry and
// returns the normalised []ruleengine.EvalResult shape the write loop expects.
// It binds `$actorKey`, `$now`, `$projectedAt` from the event/provenance and
// calls full.Engine.ExecuteWith. When an EnvelopeFn is installed, each row
// is rewritten before being handed to the adapter. When a SecureDecryptor is
// installed (a Secure Lens), each row's declared secure columns are decrypted
// before the results reach any write path — this wrapper is the single choke
// point both the stream consumer (handle) and the adjacency watch
// (handleAdjUpdate) flow through, so no plain-lens evaluation path can bypass
// it.
func (p *Pipeline) evaluateForEntry(ctx context.Context, entry ruleengine.NodeEntry) ([]ruleengine.EvalResult, error) {
	results, err := p.evaluateForEntryRaw(ctx, entry)
	if err != nil {
		return nil, err
	}
	if err := p.applySecureDecrypt(ctx, results); err != nil {
		return nil, err
	}
	return results, nil
}

// applySecureDecrypt runs the installed SecureDecryptor over results; a no-op
// when none is installed. Every evaluation path that can reach a write must
// call this (evaluateForEntry covers the stream consumer + adjacency watch;
// the actor fan-out handlers call it explicitly) — a validated Secure Lens is
// always a plain projection lens, so the fan-out coverage is defense in depth
// against a future wiring that combines an enumerator with a decryptor.
func (p *Pipeline) applySecureDecrypt(ctx context.Context, results []ruleengine.EvalResult) error {
	if p.secureDecryptor == nil {
		return nil
	}
	return p.secureDecryptor.Apply(ctx, results)
}

// evaluateForEntryRaw is evaluateForEntry's core, pre-decrypt.
func (p *Pipeline) evaluateForEntryRaw(ctx context.Context, entry ruleengine.NodeEntry) ([]ruleengine.EvalResult, error) {
	if p.fullEngine == nil || p.fullCR == nil {
		return nil, fmt.Errorf("pipeline: full engine/compiled rule unset for rule %q", p.ruleID)
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
		return []ruleengine.EvalResult{{
			Delete: true,
			Keys:   map[string]any{"key": delKey},
			Row:    nil,
		}}, nil
	}

	// Plain-projection anchor tombstone: retract the row the deleted anchor
	// projected. The non-actor twin of the actor-aware shortcut above. The
	// upsert-only re-scan path returns zero rows for a tombstoned anchor but
	// never a Delete, so the prior row would linger forever. A secondary-node
	// tombstone (event type != the anchor label) returns ok=false and falls
	// through to a normal re-execute so dependent rows refresh (e.g. a
	// deleted patient nulls an appointment's patientName without deleting
	// the appointment row).
	if entry.IsDeleted && p.actorEnumerator == nil {
		eventType, _, _ := substrate.ParseVertexKey(entry.CoreKVKey)
		if keys, ok := p.fullEngine.AnchorDeleteResult(
			p.fullCR, entry.CoreKVKey, eventType, entry.Properties); ok {
			return []ruleengine.EvalResult{{Delete: true, Keys: keys, Row: nil}}, nil
		}
	}

	results, err := p.executeFullForActor(ctx, entry.CoreKVKey, entry.Properties)
	if err != nil {
		return nil, err
	}
	// Filter-retraction presence check (plain projection lenses): when a
	// live event anchor no longer appears in the re-derived row set — a
	// WHERE predicate flipped, a keyed aspect was deleted, a required
	// link was removed — its previously-projected row must be retracted,
	// which the upsert-only re-scan never does. The anchor's projection
	// key is derived read-free (AnchorProjectionKey succeeds only for a
	// one-row-per-anchor, anchor-keyed lens — see its ok contract), so a
	// multi-row or neighbor-keyed lens falls through to today's behaviour
	// and never risks a wrong Delete. A never-matched anchor emits an
	// idempotent Delete against an absent key — a harmless no-op, pinned
	// by test. The tombstoned-anchor shortcut above returns before this
	// check; a tombstone it could not derive keys for cannot derive them
	// here either (same derivation).
	if p.actorEnumerator == nil && p.envelopeFn == nil {
		if keys, ok := p.fullEngine.AnchorProjectionKey(
			p.fullCR, entry.CoreKVKey, entry.NodeLabel, entry.Properties); ok &&
			!resultsContainKeys(results, keys) {
			results = append(results, ruleengine.EvalResult{Delete: true, Keys: keys})
		} else if !ok && p.diffRetraction {
			// Fire 3 (build-deferred in the design until a real consumer
			// arrived): AnchorProjectionKey could not derive a single
			// anchor-keyed row, so this lens's own opt-in target-diff picks
			// up what Fire 2 structurally cannot reach.
			var derr error
			results, derr = p.applyDiffRetraction(ctx, results)
			if derr != nil {
				return nil, derr
			}
		}
	}
	return results, nil
}

// executeFullForActor runs the full-engine cypher against a single
// actor key and wraps each row through envelopeFn (when installed).
// nodeProps is the actor vertex's stored Core KV body; it's passed
// through to the engine's EventContext so the executor can resolve
// the anchor without an extra Core KV read.
func (p *Pipeline) executeFullForActor(ctx context.Context, actorKey string, nodeProps map[string]any) ([]ruleengine.EvalResult, error) {
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
	results := make([]ruleengine.EvalResult, 0, len(out))
	for _, r := range out {
		row := r.Values
		keys := r.Key
		if p.envelopeFn != nil {
			newRow, newKeys, envErr := p.envelopeFn(row, keys, params)
			if errors.Is(envErr, ErrSkipProjection) {
				continue
			}
			if errors.Is(envErr, ErrDeleteProjection) {
				results = append(results, ruleengine.EvalResult{
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
		results = append(results, ruleengine.EvalResult{
			Delete: r.Delete,
			Keys:   keys,
			Row:    row,
		})
	}
	// An actor-aggregate lens (envelope installed) derives its output key from the
	// anchor, not the row, so every non-delete row for one actor carries the same
	// key. If the cypher returns 2+ such rows, the write loop would overwrite them
	// in turn (last-writer-wins) and silently drop the rest — an FR29 violation.
	// The aggregation belongs in the cypher (collect → one row per anchor); when it
	// is missing, surface the authoring defect and fail the actor's projection
	// closed rather than write a half-result.
	if p.envelopeFn != nil {
		if err := p.guardOutputKeyCollision(ctx, actorKey, results); err != nil {
			return nil, err
		}
	}
	// Record per-event projection latency for the heartbeat aggregator.
	// The buffer is cheap (single atomic-protected ring slot per insert)
	// so calling it on every fan-out actor is fine.
	if p.latencyBuf != nil {
		p.latencyBuf.Record(time.Since(start))
	}
	return results, nil
}

// guardOutputKeyCollision enforces the one-row-per-anchor invariant of an
// actor-aggregate projection. When 2+ non-delete results for a single actor map
// to the same anchor-derived output key, writing them in turn would overwrite
// last-writer-wins and silently drop the earlier rows (FR29 — Refractor must
// never silently drop). It records the defect on the Health-KV surface
// (errorCount + lastError, the same surface a terminal write failure uses) and
// logs a WARN, then returns a Terminal-classified error so the actor's
// projection fails closed: the colliding rows are never written, and the
// disposition path routes the event to the DLQ + Health rather than wedging the
// rule. The correct authoring fix is to aggregate in the cypher
// (collect(DISTINCT …) → one row per anchor); this guard catches the case where
// that aggregation is missing. A delete result paired with a write, or rows for
// different actors, are not collisions and pass through untouched.
func (p *Pipeline) guardOutputKeyCollision(ctx context.Context, actorKey string, results []ruleengine.EvalResult) error {
	collidingKey, count, found := detectOutputKeyCollision(results)
	if !found {
		return nil
	}
	msg := fmt.Sprintf(
		"actor-aggregate projection produced %d non-delete rows for actor %q sharing output key %q; "+
			"the cypher must aggregate to one row per anchor (collect)",
		count, actorKey, collidingKey)
	slog.Warn("pipeline: actor-aggregate output-key collision — defect signal",
		"ruleId", p.ruleID, "actorKey", actorKey,
		"outputKey", collidingKey, "rowCount", count)
	if p.reporter != nil {
		if recErr := p.reporter.RecordError(ctx, msg); recErr != nil {
			slog.Error("pipeline: record output-key collision on health KV",
				"ruleId", p.ruleID, "err", recErr)
		}
	}
	return failure.Terminal(fmt.Errorf("pipeline: %s", msg))
}

// detectOutputKeyCollision reports the first output key carried by 2+ non-delete
// results in a single actor's result set, along with the total number of results
// that share it. Delete results are excluded: a delete + a write for the same key
// is the normal retract-then-write shape, not a collision. found is false when
// every non-delete result has a distinct output key (the overwhelmingly common
// one-row-per-anchor path).
func detectOutputKeyCollision(results []ruleengine.EvalResult) (collidingKey string, count int, found bool) {
	counts := make(map[string]int, len(results))
	var firstRepeated string
	for i := range results {
		if results[i].Delete {
			continue
		}
		key, _ := results[i].Keys["key"].(string)
		if key == "" {
			continue
		}
		counts[key]++
		if counts[key] == 2 && firstRepeated == "" {
			firstRepeated = key
		}
	}
	if firstRepeated == "" {
		return "", 0, false
	}
	return firstRepeated, counts[firstRepeated], true
}

// evaluateFanOut handles the cross-vertex fan-out path: the CDC event arrived
// on a non-actor vertex; enumerate affected actors and re-execute the cypher
// per actor. Each actor's result set is appended to the returned []EvalResult
// — the pipeline write loop handles each result row independently.
func (p *Pipeline) evaluateFanOut(ctx context.Context, entry ruleengine.NodeEntry) ([]ruleengine.EvalResult, error) {
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
func (p *Pipeline) evaluateLinkFanOut(ctx context.Context, linkKey string, isDeleted bool) ([]ruleengine.EvalResult, error) {
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
func (p *Pipeline) evaluateAspectFanOut(ctx context.Context, aspectKey string) ([]ruleengine.EvalResult, error) {
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
func (p *Pipeline) reprojectActors(ctx context.Context, actorKeys []string) ([]ruleengine.EvalResult, error) {
	var all []ruleengine.EvalResult
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
			all = append(all, ruleengine.EvalResult{
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

// resultsContainKeys reports whether any non-delete result carries the given
// target-key map — the filter-retraction presence test: present ⇒ the anchor
// still projects, absent ⇒ its row must be retracted. Keys compare by their
// canonical JSON rendering (the identity the adapters key on), so a
// same-valued key differing only in in-memory numeric type reads as PRESENT —
// erring toward linger (safe), never toward deleting a row the adapter would
// address identically.
func resultsContainKeys(results []ruleengine.EvalResult, keys map[string]any) bool {
	want, err := json.Marshal(keys)
	if err != nil {
		return true // unmarshalable keys: treat as present → no Delete (fail safe)
	}
	for i := range results {
		if results[i].Delete {
			continue
		}
		got, gerr := json.Marshal(results[i].Keys)
		if gerr == nil && bytes.Equal(got, want) {
			return true
		}
	}
	return false
}

// applyDiffRetraction closes the neighbor-driven / multi-row retraction gap
// Fire 2's anchor-self presence check cannot reach by construction (a
// composite output key with a column bound to a non-anchor variable, so
// AnchorProjectionKey returns ok=false for every event on the lens, not just
// some). It reads the target's full live key set via adapter.KeyLister,
// diffs it against this re-execute's freshly-derived row set, and appends a
// Delete for every key the target still carries but the fresh computation no
// longer produces.
//
// Correctness rests on the lens itself being a genuinely unanchored
// whole-scan (no `{key: $actorKey}` seed anywhere in its MATCH clauses, the
// shape every live diffRetraction-opted-in lens has): because the query
// re-derives the COMPLETE current truth on every re-execute regardless of
// which vertex seeded it, comparing that complete truth against the target's
// complete existing key set is exact — not an approximation scoped to
// "whichever vertex happened to trigger this event," which would risk
// misattributing an identity vertex's role (e.g. applicant vs. managing
// landlord) and deriving the wrong scope. Only called when p.diffRetraction
// is set (SetDiffRetraction) — a convergence (`violating`-flag) lens never
// opts in, so its deliberate never-retract contract is untouched.
//
// An adapter that doesn't implement KeyLister is a configuration defect — a
// lens opted into DiffRetraction against a target that cannot enumerate its
// keys, so no row can ever be retracted. It fails the projection rather than
// passing results through: for the retraction-bearing lenses this mechanism
// exists to serve (a grant producer above all), silence would present a
// permanently inert path as a working security control. Activation refuses the
// lens up front (cmd/refractor's DiffRetraction guard), so reaching here means
// the adapter was swapped underneath a running pipeline — loud is correct.
func (p *Pipeline) applyDiffRetraction(ctx context.Context, results []ruleengine.EvalResult) ([]ruleengine.EvalResult, error) {
	lister, ok := p.currentAdapter().(adapter.KeyLister)
	if !ok {
		return nil, fmt.Errorf("pipeline: diff retraction: adapter %T does not implement adapter.KeyLister — the lens cannot retract anything", p.currentAdapter())
	}
	existing, err := lister.ListKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("pipeline: diff retraction: list keys: %w", err)
	}
	for _, exKeys := range existing {
		if resultsContainKeys(results, exKeys) {
			continue
		}
		results = append(results, ruleengine.EvalResult{Delete: true, Keys: exKeys})
	}
	return results, nil
}

// fetchVertexProps point-reads a vertex from Core KV and returns its
// properties (or nil if missing / soft-deleted).
func (p *Pipeline) fetchVertexProps(ctx context.Context, vtxKey string) (map[string]any, error) {
	entry, err := p.coreKV.Get(ctx, vtxKey)
	if err != nil {
		// A genuinely-absent key is "missing" (nil, nil); any other error
		// surfaces so the caller can decide retry/structural handling.
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if entry == nil || len(entry.Value) == 0 {
		return nil, nil
	}
	var props map[string]any
	if jerr := json.Unmarshal(entry.Value, &props); jerr != nil {
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
