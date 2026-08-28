package weaver

import "sync"

// republishCapPerTarget bounds how many keys one target may owe a re-publish at
// once.
//
// The live population is self-limiting and far below this: an entry is added
// only by a `fire` publish FAILURE, and that failure returns substrate.Nak,
// which asks the server for the same message back immediately (an undelayed Nak
// queues the message for redelivery ahead of every new one, V2). Lane 1 is a
// single serial worker per target prefetching one message, so during a publish
// outage the worker cycles that one row rather than draining into the rest — the
// set holds that row's open gaps and little else. Entries are added at the Nak
// rate, not the row rate.
//
// The cap exists anyway because that argument rests on substrate BEHAVIOUR
// (Nak's immediate redelivery, one worker, prefetch 1) rather than on anything
// this package controls, and because the failure mode of being wrong is an
// unbounded per-row map in the one component whose per-row maps have twice
// needed capping. Refusing an insertion past the cap is safe by construction: it
// degrades that key to exactly the behaviour a process restart already gives it
// — no prompt re-publish, and the sweep's lease-expiry reclaim as the backstop —
// which is a documented, bounded outcome rather than a lost obligation.
const republishCapPerTarget = 256

// republishSet records the gaps whose last op publish FAILED and are therefore
// owed an immediate re-publish of the SAME episode.
//
// `fire`'s publish failure returns Nak, which brings the row straight back. On
// that redelivery dispatchGap finds the mark present with a live lease, and
// every such delivery but this one is an anti-storm drop — so without a record
// of which gap owes a publish, the failure would be swallowed until the mark's
// lease expired into the sweep's reclaim. This set is that record, and it has to
// be its own structure because the obligation is per-GAP while a message's
// redelivery signal is per-MESSAGE: one row's gaps Nak independently, so
// "a publish of mine failed" and "a sibling gap Nak'd the row" are
// indistinguishable from the message alone.
//
// Deliberately NOT compensation-by-mark-delete. A publish failure can be
// AMBIGUOUS (the op landed, the ack was lost), and deleting the mark would let
// the redelivery mint a SECOND episode with a fresh claimId. Re-publishing the
// existing episode with its preserved claimId derives the same requestId and
// collapses on the Contract #4 tracker, which is the only idempotent shape under
// that ambiguity.
//
// Lifetime:
//
//	created                | lazily, per target, at that target's first publish failure
//	added                  | in `fire`, on a primary-op publish failure — the SOLE writer that adds.
//	                       | A followUp publish failure does not add: it does not Nak the episode,
//	                       | and its loss self-heals on the sweep (see fire)
//	removed (repaired)     | in `fire`, on that same key's next successful primary publish
//	removed (gap ended)    | by clearClosedMarks' mark clear — the episode is over, so nothing is owed
//	removed (subject gone) | with the target, on Revoke and on the reconcileConsumers removal, beside
//	                       | the weaver-state and issue-cache teardowns
//	refused                | past republishCapPerTarget for the target: the key keeps no entry and
//	                       | degrades to the restart behaviour (reclaim at lease expiry)
//	restart                | EMPTY. A pending obligation is lost and the key degrades to the reclaim
//	                       | ladder, bounded by one MarkLease for the first re-attempt. This is the
//	                       | design's accepted backstop, not a gap
//	stranded (inert)       | a mark deleted by a route that does not clear the set (the sweep's own
//	                       | deleteMark legs) leaves an entry behind. It cannot cause a wrong action:
//	                       | the only reader runs behind `found && !stale`, so an entry whose mark is
//	                       | gone is never consulted. It occupies a slot until the key's next
//	                       | successful publish, the target's teardown, or a restart
//
// ARBITRATION with clearClosedMarks, stated rather than assumed. Both `fire` and
// clearClosedMarks remove; removal is an idempotent delete of the same key, so
// neither "wins" and order does not matter. What would matter is a remove racing
// an ADD for one key, and that cannot happen on lane 1: clearClosedMarks runs in
// handleRow's preamble and only ever touches a column the row is NOT reporting
// open, while the dispatch leg below it only ever fires columns the row IS
// reporting open — so within a pass the two never name the same key, and lane 1
// is one serial worker per target, so two passes never overlap. The one genuine
// concurrency is lane-1's `fire` against the sweep's `fire` for the same key;
// both write the outcome of their own publish, last writer wins, and either
// verdict is safe — a spurious entry costs at most one idempotent re-publish
// (same requestId, collapses on the tracker), and a spuriously cleared entry
// degrades to the reclaim ladder.
type republishSet struct {
	mu sync.Mutex
	// owed is keyed by target so a teardown and the cap are both O(1) to reason
	// about; the inner set holds the mark-key-shaped (target, entity, gap)
	// tuple, the same identity the §10.3 mark itself owns.
	owed map[string]map[string]struct{}
}

func newRepublishSet() *republishSet {
	return &republishSet{owed: make(map[string]map[string]struct{})}
}

// add records that this gap's episode published unsuccessfully and is owed a
// re-publish. Reports whether the entry is now held — false when the target is
// at its cap, so the caller can say so once rather than silently.
func (r *republishSet) add(targetID, entityID, col string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys, ok := r.owed[targetID]
	if !ok {
		keys = make(map[string]struct{})
		r.owed[targetID] = keys
	}
	key := markKey(targetID, entityID, col)
	if _, held := keys[key]; held {
		return true
	}
	if len(keys) >= republishCapPerTarget {
		return false
	}
	keys[key] = struct{}{}
	return true
}

// clear retires the obligation at one key — the publish succeeded, or the gap
// ended. Idempotent.
func (r *republishSet) clear(targetID, entityID, col string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keys, ok := r.owed[targetID]
	if !ok {
		return
	}
	delete(keys, markKey(targetID, entityID, col))
	if len(keys) == 0 {
		delete(r.owed, targetID)
	}
}

// owes reports whether this gap's last publish failed and has not been made
// good since.
func (r *republishSet) owes(targetID, entityID, col string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, held := r.owed[targetID][markKey(targetID, entityID, col)]
	return held
}

// clearTarget drops every obligation for a target that is leaving — revoked, or
// unregistered by the reconcile. A target with no consumer delivers no rows, so
// nothing could ever consult or retire these again.
func (r *republishSet) clearTarget(targetID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.owed, targetID)
}
