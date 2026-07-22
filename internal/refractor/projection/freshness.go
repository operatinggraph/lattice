package projection

import "github.com/operatinggraph/lattice/internal/substrate"

// ContributingSources derives the contributing-binding source set for an
// actor's projection (Contract #6 §6.3, freshness: auto). It is the set of Core
// KV keys the plan actually bound and read into the winning rows: the actor's
// own identity vertex, the lens-definition vertex, and every roles / tasks /
// services / links key that contributed a binding (surfaced as a Contract #1
// vtx.* or lnk.* key value somewhere in the projected rows).
//
// v1 covers contributing-binding sources only. Sources that were read then
// excluded (e.g. a now-closed task the executor matched then dropped via a
// WHERE / realness filter) require full-executor touched-then-dropped
// instrumentation and are DEFERRED to a 12.3-follow-up (see
// docs/decisions/12.3-projected-from-revisions-followup.md). This datum is the
// coherence/debug provenance, NOT the write-ordering guard (that is
// projectionSeq, §6.2) — so the deferral carries no auth risk.
//
// actorKey is the bound actor vertex key; lensDefKey is the meta-lens vertex
// key; rows are the projected RETURN rows for the actor; revisionOf returns the
// current Core KV revision of a key (0 = unknown/absent, omitted).
func ContributingSources(actorKey, lensDefKey string, rows []map[string]any, revisionOf func(key string) uint64) map[string]uint64 {
	keys := map[string]struct{}{}
	if actorKey != "" {
		keys[actorKey] = struct{}{}
	}
	if lensDefKey != "" {
		keys[lensDefKey] = struct{}{}
	}
	for _, row := range rows {
		collectGraphKeys(row, keys)
	}

	out := map[string]uint64{}
	if revisionOf == nil {
		for k := range keys {
			out[k] = 0
		}
		return out
	}
	for k := range keys {
		if rev := revisionOf(k); rev != 0 {
			out[k] = rev
		}
	}
	return out
}

// collectGraphKeys walks an arbitrary projected-row value tree and records every
// string that is a Contract #1 graph key (vtx.* vertex or lnk.* link). These are
// the neighbor keys the executor fetched into the winning row — the
// contributing-binding set.
func collectGraphKeys(v any, into map[string]struct{}) {
	switch x := v.(type) {
	case string:
		if isGraphKey(x) {
			into[x] = struct{}{}
		}
	case map[string]any:
		for _, vv := range x {
			collectGraphKeys(vv, into)
		}
	case []any:
		for _, el := range x {
			collectGraphKeys(el, into)
		}
	}
}

func isGraphKey(s string) bool {
	if _, _, ok := substrate.ParseVertexKey(s); ok {
		return true
	}
	return substrate.ClassifyKey(s) == substrate.KindLink
}
