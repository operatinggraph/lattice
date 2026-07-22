package processor

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// maxInstanceOfHops bounds the instanceOf-chain walk in the step-6 write-gate's
// governing-DDL resolution (Contract #1 §1.5). The deepest real domain chain is
// two hops (instance → template → type); the bound + a visited-set cycle guard
// keep the walk terminating and abuse-proof for crafted link cycles. Exceeding
// the bound yields "no governing DDL" → the §1.5 step-5 permissive default
// (fail-open to today's behavior, never into a wrong DDL).
const maxInstanceOfHops = 4

const instanceOfRelation = "instanceOf"

// instanceOfEdge is one resolved instanceOf link: its 6-segment link key and the
// 3-segment vertex key it points at. A vertex is expected to carry at most one
// live instanceOf (Contract #1 §1.5 design assumption); more than one is
// ambiguous and resolves to the permissive default (design §9 F1), never a
// guessed pick. Edges are kept link-key-sorted for stable, retry-identical logs.
type instanceOfEdge struct {
	linkKey string
	target  string
}

// instanceOfTargetReader enumerates a vertex's live instanceOf-link targets from
// committed Core KV — the on-demand fallback used only when the link is in
// neither the in-flight batch nor the hydrated working set. A single bounded
// `lnk.<root>.instanceOf.>` prefix list (source-anchored, so the read is bounded
// by construction: the source segments sit in the prefix). Optional on the
// validator (nil ⇒ on-demand discovery is skipped; batch + working-set paths
// still resolve).
type instanceOfTargetReader interface {
	// LiveInstanceOfTargets returns every live instanceOf edge sourced at
	// vtxRoot, sorted by link key for a deterministic selection. Tombstoned,
	// unparseable, or (between list and get) hard-deleted links are skipped.
	LiveInstanceOfTargets(ctx context.Context, vtxRoot string) ([]instanceOfEdge, error)
}

// connInstanceOfReader is the production instanceOfTargetReader, backed by the
// Processor's substrate connection. It performs one bounded prefix list over the
// source-anchored `lnk.<root>.instanceOf.` keyspace plus a per-key GET to honor
// tombstones — never an unbounded scan.
type connInstanceOfReader struct {
	conn       *substrate.Conn
	coreBucket string
}

func (r *connInstanceOfReader) LiveInstanceOfTargets(ctx context.Context, vtxRoot string) ([]instanceOfEdge, error) {
	vt, id, ok := substrate.ParseVertexKey(vtxRoot)
	if !ok {
		return nil, nil
	}
	prefix := "lnk." + vt + "." + id + "." + instanceOfRelation + "."
	keys, err := r.conn.KVListKeysPrefix(ctx, r.coreBucket, prefix)
	if err != nil {
		return nil, err
	}
	var edges []instanceOfEdge
	for _, k := range keys {
		_, _, _, t2, id2, ok := substrate.ParseLinkKey(k)
		if !ok {
			continue
		}
		entry, err := r.conn.KVGet(ctx, r.coreBucket, k)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue // hard-tombstoned between list and get → no link
			}
			return nil, err
		}
		var d struct {
			IsDeleted bool `json:"isDeleted"`
		}
		// A malformed/unparseable link envelope is skipped (treated as no link),
		// not trusted as live — fail-open to the permissive default, never
		// resolve a corrupt edge into a governing DDL.
		if uerr := json.Unmarshal(entry.Value, &d); uerr != nil || d.IsDeleted {
			continue
		}
		edges = append(edges, instanceOfEdge{linkKey: k, target: substrate.VertexKey(t2, id2)})
	}
	sortEdges(edges)
	return edges, nil
}

// resolveGoverningDDL resolves the DDL that gates a mutation's write
// (Contract #1 §1.5). It first tries the exact class→DDL lookup (today's fast
// path, unchanged); on a miss it walks the mutation's vertex root up its
// instanceOf chain to the nearest type-authority DDL, so a fine-grained
// dotted discriminator class is governed by its shared type DDL (the type
// authority the chain reaches) with zero per-subtype DDLs. A miss on both →
// (zero, false) → the caller applies the §1.5 step-5 permissive default.
//
// The walk is read-lazy: in-flight batch first, then the hydrated working set,
// then (only if both miss) a single bounded on-demand Core KV read. For every
// coarse-class vertex shipping today the exact lookup wins first, so the walk —
// and any read — never runs.
func (v *ValidatorImpl) resolveGoverningDDL(ctx context.Context, class, key string, kind substrate.KeyKind, result ScriptResult, state HydratedState) (MetaVertexRef, bool) {
	if ref, ok := v.DDLs.Lookup(class); ok {
		return ref, true // exact match — unchanged Contract #1 §1.5 step-3 path
	}

	root := vertexRootForResolve(key, kind)
	if root == "" {
		return MetaVertexRef{}, false // links / unparseable → permissive default (today's behavior)
	}

	visited := map[string]bool{root: true}
	cur := root
	for hop := 0; hop < maxInstanceOfHops; hop++ {
		target, ok := v.instanceOfTargetOf(ctx, cur, result, state)
		if !ok {
			break // no instanceOf link → no type authority
		}
		if visited[target] {
			break // cycle guard → permissive default
		}
		visited[target] = true

		if isMetaVertexKey(target) {
			// Terminal: the target IS a DDL meta-vertex. Only a vertexType DDL
			// is a legitimate governing authority (an aspect/link/event DDL is
			// not a write-gate type).
			if ref, ok := v.DDLs.LookupByMetaKey(target); ok && ref.Kind == "vertexType" {
				return ref, true
			}
			break
		}

		// A business vertex whose own class is itself a registered DDL is also a
		// terminal (the one-hop instance→type domain shape).
		if tclass, ok := v.classOf(ctx, target, result, state); ok {
			if ref, ok := v.DDLs.Lookup(tclass); ok {
				return ref, true
			}
		}

		cur = target // keep walking (instance → template → type)
	}

	return MetaVertexRef{}, false
}

// instanceOfTargetOf returns the live instanceOf target of vtxRoot. A vertex is
// expected to carry exactly one live instanceOf (Contract #1 §1.5 design
// assumption); the resolving layer therefore resolves only when it holds
// **exactly one** live edge. Multiple live edges are **ambiguous → no
// resolution** (the caller applies the permissive default) — mirroring the
// `ClassForCommand` ambiguity guard (design §9 F1: never pick the admitting DDL
// when the type authority is ambiguous, so an extra instanceOf link cannot steer
// the gate). The in-flight batch is the authoritative layer (last op per link
// key wins; a tombstone in the batch suppresses the same link committed below);
// then the hydrated working set, then a single bounded on-demand Core KV read.
func (v *ValidatorImpl) instanceOfTargetOf(ctx context.Context, vtxRoot string, result ScriptResult, state HydratedState) (string, bool) {
	batchLive, batchDead := reconcileBatchInstanceOf(vtxRoot, result.Mutations)
	if len(batchLive) > 0 {
		return soleTarget(batchLive)
	}
	if edges := workingSetInstanceOfEdges(vtxRoot, state.Context.Hydrated, batchDead); len(edges) > 0 {
		return soleTarget(edges)
	}
	if v.linkReader != nil {
		edges, err := v.linkReader.LiveInstanceOfTargets(ctx, vtxRoot)
		if err != nil {
			// Fail-open to the permissive default — never resolve into a wrong
			// DDL on a read fault. A read error degrades to today's behavior.
			v.Logger.Warn("step 6: instanceOf on-demand read failed; resolving to permissive default",
				"vtxRoot", vtxRoot, "error", err)
			return "", false
		}
		if edges = excludeDead(edges, batchDead); len(edges) > 0 {
			return soleTarget(edges)
		}
	}
	return "", false
}

// soleTarget returns the single edge's target, or no resolution when the layer
// carries more than one live instanceOf (ambiguous type authority → permissive
// default per design §9 F1).
func soleTarget(edges []instanceOfEdge) (string, bool) {
	if len(edges) == 1 {
		return edges[0].target, true
	}
	return "", false
}

// reconcileBatchInstanceOf folds the in-flight mutations into the net instanceOf
// state of vtxRoot: last op per link key wins, so a create-then-tombstone (or a
// tombstone alone) of a link leaves it dead. Returns the net-live edges and the
// set of link keys the batch tombstoned (which must be suppressed in the
// committed/working-set layers — the batch is the in-flight truth).
func reconcileBatchInstanceOf(vtxRoot string, muts []MutationOp) (live []instanceOfEdge, dead map[string]bool) {
	vt, id, ok := substrate.ParseVertexKey(vtxRoot)
	if !ok {
		return nil, nil
	}
	liveByKey := map[string]instanceOfEdge{}
	dead = map[string]bool{}
	for _, m := range muts {
		t1, id1, name, t2, id2, ok := substrate.ParseLinkKey(m.Key)
		if !ok || name != instanceOfRelation || t1 != vt || id1 != id {
			continue
		}
		if mutationTombstoned(m) {
			delete(liveByKey, m.Key)
			dead[m.Key] = true
			continue
		}
		delete(dead, m.Key)
		liveByKey[m.Key] = instanceOfEdge{linkKey: m.Key, target: substrate.VertexKey(t2, id2)}
	}
	for _, e := range liveByKey {
		live = append(live, e)
	}
	sortEdges(live)
	return live, dead
}

// workingSetInstanceOfEdges collects the live instanceOf edges sourced at
// vtxRoot among the hydrated reads, excluding any link the batch tombstoned.
func workingSetInstanceOfEdges(vtxRoot string, hydrated map[string]VertexDoc, dead map[string]bool) []instanceOfEdge {
	vt, id, ok := substrate.ParseVertexKey(vtxRoot)
	if !ok {
		return nil
	}
	prefix := "lnk." + vt + "." + id + "." + instanceOfRelation + "."
	var edges []instanceOfEdge
	for k, doc := range hydrated {
		if !strings.HasPrefix(k, prefix) || doc.IsDeleted || dead[k] {
			continue
		}
		if _, _, _, t2, id2, ok := substrate.ParseLinkKey(k); ok {
			edges = append(edges, instanceOfEdge{linkKey: k, target: substrate.VertexKey(t2, id2)})
		}
	}
	sortEdges(edges)
	return edges
}

// excludeDead drops edges whose link key the batch tombstoned.
func excludeDead(edges []instanceOfEdge, dead map[string]bool) []instanceOfEdge {
	if len(dead) == 0 {
		return edges
	}
	out := edges[:0]
	for _, e := range edges {
		if !dead[e.linkKey] {
			out = append(out, e)
		}
	}
	return out
}

// sortEdges orders edges by link key for deterministic, retry-stable selection.
func sortEdges(edges []instanceOfEdge) {
	sort.Slice(edges, func(i, j int) bool { return edges[i].linkKey < edges[j].linkKey })
}

// classOf resolves the class of the vertex at targetKey, preferring the batch,
// then the working set, then a single on-demand Core KV read. Used to detect a
// terminal whose own class is itself a registered DDL.
func (v *ValidatorImpl) classOf(ctx context.Context, targetKey string, result ScriptResult, state HydratedState) (string, bool) {
	for _, m := range result.Mutations {
		if m.Key != targetKey || m.Document == nil || mutationTombstoned(m) {
			continue
		}
		if m.Op != "create" && m.Op != "update" {
			continue // only a create/update establishes the vertex's class
		}
		if c, ok := m.Document["class"].(string); ok {
			return c, true
		}
	}
	if doc, ok := state.Context.Hydrated[targetKey]; ok && !doc.IsDeleted {
		return doc.Class, true
	}
	if state.Context.KVReader != nil {
		doc, err := state.Context.KVReader.ReadVertex(ctx, targetKey)
		if err == nil && doc != nil && !doc.IsDeleted {
			return doc.Class, true
		}
	}
	return "", false
}

// vertexRootForResolve derives the 3-segment vertex root whose instanceOf chain
// governs a mutation. A vertex mutation roots at itself; an aspect mutation at
// its parent vertex. Link/unknown mutations have no instanceOf-governed root —
// they fall through to the permissive default exactly as today (a link carries
// its own link class, never a fine-grained vertex discriminator).
func vertexRootForResolve(key string, kind substrate.KeyKind) string {
	switch kind {
	case substrate.KindVertex:
		return key
	case substrate.KindAspect:
		if vk, _, _, _, ok := substrate.ParseAspectKey(key); ok {
			return vk
		}
	}
	return ""
}

// isMetaVertexKey reports whether key is a DDL meta-vertex (vtx.meta.<NanoID>).
func isMetaVertexKey(key string) bool {
	return strings.HasPrefix(key, "vtx.meta.")
}

// mutationTombstoned reports whether a mutation removes (or carries a removed)
// document — a tombstoned link/vertex is no link/vertex for resolution.
func mutationTombstoned(m MutationOp) bool {
	if m.Op == "tombstone" {
		return true
	}
	if m.Document != nil {
		if del, ok := m.Document["isDeleted"].(bool); ok && del {
			return true
		}
	}
	return false
}
