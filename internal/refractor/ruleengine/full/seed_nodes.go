package full

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedNodes returns all Core KV vertices matching n's label + properties.
// Three ways to build that candidate set, in order:
//
//   - a "key" property with a resolvable expression → a point lookup;
//   - an armed anchor seed (EventContext.SeedAnchor) on the anchor pattern →
//     a point lookup on the event vertex, since the caller has proved the
//     event is a mutation of that anchor and no other anchor's rows can have
//     changed with it;
//   - otherwise a listing — a labeled pattern via a server-side "vtx.<label>."
//     prefix filter bounded by that type's own vertex count, an unlabeled one
//     via the whole bucket since any type may bind it — filtered by shape,
//     label, and properties.
func (ex *executor) seedNodes(b binding, n NodePattern) ([]*nodeRef, error) {
	// Fast path: property "key" with a resolvable expression → point read.
	if keyExpr, ok := n.Properties["key"]; ok {
		val, err := ex.evalExpr(b, keyExpr)
		if err != nil {
			return nil, err
		}
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("full engine: node property 'key' must resolve to string, got %T", val)
		}
		return ex.pointCandidate(b, n, s)
	}

	// Anchor seeding: take (and clear) the armed seed at the first candidate
	// set built by scan — the anchor pattern. The seedAnchorBinds re-check is
	// defensive: arming already proved this exact pattern point-seedable, and
	// a seed that somehow reached a pattern it cannot bind falls back to the
	// scan below rather than narrowing the wrong pattern.
	if seed := ex.takeSeedAnchor(); seed != "" && seedAnchorBinds(n, seed, ex.labelExpansion) {
		return ex.pointCandidate(b, n, seed)
	}

	// Generic path: list candidates, then filter by shape, label, and
	// properties. A labeled pattern only ever binds vertices of that exact
	// type, so listing is scoped to the "vtx.<label>." prefix — a
	// server-side JetStream subject filter (ListKeysPrefix) bounded by that
	// type's own vertex count — instead of every key in the bucket; an
	// unlabeled pattern can bind any type and still lists everything. A list
	// failure MUST surface, not degrade to zero seeds: downstream, the
	// filter-retraction presence check treats an anchor's absence from the
	// derived row set as authoritative and emits a Delete, so a swallowed
	// error here would retract live rows on a transient substrate blip. An
	// empty bucket/prefix returns an empty slice with no error and seeds
	// nothing.
	//
	// A LabelExpand pattern cannot be expressed as ONE prefix — an abstract
	// label's own "vtx.<label>." prefix has no instances at all (§3.2), and
	// the label's resolved closure is a SET of concrete types, not a single
	// string. So this lists once per concrete member instead. An unresolved
	// expansion (no entry in ex.labelExpansion) binds nothing, matching the
	// fail-closed posture the other three label-equality sites already take
	// — never fall back to scanning the bare (abstract) prefix, which would
	// silently seed zero candidates from every unseeded evaluation of a
	// `*`-anchored lens (boot, Rebuild, a neighbour-triggered re-execute) and
	// disagree with a seeded evaluation of the SAME lens that reaches the
	// leaf via pointCandidate above — the disagreement filter-retraction
	// reads as a mass revoke.
	var keys []string
	var err error
	switch {
	case n.LabelExpand:
		set, hasSet := ex.labelExpansion[n.Label]
		if !hasSet {
			return nil, nil
		}
		for vt := range set {
			vtKeys, lerr := ex.coreKV.ListKeysPrefix(ex.ctx, substrate.VertexPrefix+"."+vt+".")
			if lerr != nil {
				err = lerr
				break
			}
			keys = append(keys, vtKeys...)
		}
	case n.Label != "":
		keys, err = ex.coreKV.ListKeysPrefix(ex.ctx, substrate.VertexPrefix+"."+n.Label+".")
	default:
		keys, err = ex.coreKV.ListKeys(ex.ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("full engine: seed scan: %w", err)
	}
	var refs []*nodeRef
	for _, k := range keys {
		cls := substrate.ClassifyKey(k)
		if n.Label != "" {
			// The label-scoped prefix also matches 4-segment aspect keys
			// sharing the same string prefix (vtx.<label>.<id>.<localName>)
			// and, for a malformed id, a KindUnknown key that merely starts
			// with the right tokens — only a true vertex key is a candidate.
			if cls != substrate.KindVertex {
				continue
			}
		} else if cls != substrate.KindVertex && cls != substrate.KindUnknown {
			// The whole-bucket scan additionally admits KindUnknown: an
			// unlabeled pattern imposes no label constraint at all, so a key
			// whose shape is not a Contract #1 vertex key is still a
			// candidate and only the property predicates can exclude it.
			continue
		}
		ref, err := ex.fetchNode(k)
		if err != nil {
			return nil, err
		}
		if ref == nil {
			continue
		}
		if !ex.nodeMatches(ref, n) {
			continue
		}
		ok, err := ex.propsAllMatch(b, ref, n)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// pointCandidate builds the candidate set for a pattern narrowed to ONE Core
// KV key — shared by the `{key: …}` fast path and the anchor-seeded path. A
// missing or soft-deleted vertex, a key whose vertex does not carry the
// pattern's label, or a property predicate the vertex fails all yield zero
// candidates: exactly what a scan that matched nothing yields, so a narrowed
// pattern is never distinguishable from a scanned one by its result.
func (ex *executor) pointCandidate(b binding, n NodePattern, key string) ([]*nodeRef, error) {
	ref, err := ex.fetchNode(key)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, nil
	}
	if !ex.nodeMatches(ref, n) {
		return nil, nil
	}
	ok, err := ex.propsAllMatch(b, ref, n)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return []*nodeRef{ref}, nil
}

// takeSeedAnchor returns the armed anchor seed and clears it, so exactly one
// pattern can consume it. Taken unconditionally by the first scan-seeded
// pattern (see executor.seedAnchor) rather than only when it applies: a seed
// that outlived its own pattern must be spent, never left armed for the next
// one.
func (ex *executor) takeSeedAnchor() string {
	seed := ex.seedAnchor
	ex.seedAnchor = ""
	return seed
}

// seedAnchorFor reports the vertex key this evaluation's anchor pattern may
// narrow to, or "" when the seed does not apply and every pattern scans as
// usual. Only the query's ANCHOR — the first MATCH clause's first node
// pattern, the same derivation AnchorProjectionKey uses — is ever seedable;
// later MATCH clauses, OPTIONAL MATCH clauses, and sibling comma-separated
// patterns keep their own candidate sets whatever the event was.
func seedAnchorFor(q *Query, seedKey string, exp map[string]map[string]struct{}) string {
	if seedKey == "" || q == nil {
		return ""
	}
	n, ok := anchorPattern(q)
	if !ok || !seedAnchorBinds(n, seedKey, exp) {
		return ""
	}
	return seedKey
}

// seedAnchorBinds reports whether one vertex key can stand in for pattern n's
// entire candidate set. Two structural conditions:
//
//   - n is labeled with the key's own Contract #1 vertex type — or, when n
//     carries the `*` taxonomy-expansion sigil, the key's type is a member
//     of the label's resolved downward closure (exp[n.Label]) — §5.1 site 2,
//     the engine half of event seeding. An unlabeled pattern binds any type
//     (so one vertex is not its candidate set), and a pattern labeled with a
//     type outside what n admits is not the event's pattern at all — a
//     neighbor-type event says nothing about which anchors changed. A
//     LabelExpand pattern whose label has no entry in exp matches nothing —
//     fail closed, never the bare-label reading.
//   - n carries no `key` property. Such a pattern already resolves to a point
//     read of its own key, which the seed must not silently displace.
func seedAnchorBinds(n NodePattern, seedKey string, exp map[string]map[string]struct{}) bool {
	if n.Label == "" {
		return false
	}
	if _, keyed := n.Properties["key"]; keyed {
		return false
	}
	vtype, _, ok := substrate.ParseVertexKey(seedKey)
	if !ok {
		return false
	}
	if n.LabelExpand {
		set, hasSet := exp[n.Label]
		if !hasSet {
			return false
		}
		_, hit := set[vtype]
		return hit
	}
	return vtype == n.Label
}
