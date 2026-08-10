package main

import (
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
)

// unionExpansionLabels unions (*full.CompiledRule).ExpansionLabels() across
// r's CompiledRule and, for a multi-walk lens, every CompiledBranches entry
// instead of just branches[0] — mirroring pipeline.useFullEngineBranches'
// own `all := []ruleengine.CompiledRule{cr}; if len(branches) > 1 { all =
// branches }` selection (internal/refractor/pipeline/pipeline.go), so the
// re-derivation candidate set is chosen by the identical rule
// useFullEngineBranches itself compiles against, never a narrower one that
// missed a branch.
//
// Returns an empty, non-nil map for a nil rule, a non-full-engine rule, or a
// rule with no `*` anywhere — every one of which is correctly "never a
// taxonomy re-derivation candidate": both newPipelineEntry (pipelineEntry.
// taxExpansionLabels) and reloader.rederiveEntry gate on len(...) == 0.
func unionExpansionLabels(r *lens.Rule) map[string]struct{} {
	out := map[string]struct{}{}
	if r == nil {
		return out
	}
	all := []ruleengine.CompiledRule{r.CompiledRule}
	if len(r.CompiledBranches) > 1 {
		all = r.CompiledBranches
	}
	for _, c := range all {
		fullCR, ok := c.(*full.CompiledRule)
		if !ok {
			continue
		}
		for l := range fullCR.ExpansionLabels() {
			out[l] = struct{}{}
		}
	}
	return out
}

// taxonomyExpansionEqual reports whether two (Expand result, Status) pairs
// describe the same answer — what reloader.rederiveEntry uses to tell a
// real taxonomy change from a no-op (dynamic-type-taxonomy-design.md §14
// Fire A item 4). Status is compared first: StatusStale and StatusArmed are
// different guarantees about the SAME set (§4.2 — one may run a broad
// filter, the other may treat itself as exhaustive), so a resolver arming or
// disarming with the label sets otherwise unchanged still counts as a real
// change.
func taxonomyExpansionEqual(
	aSet map[string]map[string]struct{}, aStatus taxonomy.Status,
	bSet map[string]map[string]struct{}, bStatus taxonomy.Status,
) bool {
	if aStatus != bStatus {
		return false
	}
	if len(aSet) != len(bSet) {
		return false
	}
	for label, aMembers := range aSet {
		bMembers, ok := bSet[label]
		if !ok || len(aMembers) != len(bMembers) {
			return false
		}
		for m := range aMembers {
			if _, ok := bMembers[m]; !ok {
				return false
			}
		}
	}
	return true
}

// taxonomyChanged re-derives every live lens whose expansion-label union is
// non-empty against the current taxonomy resolver state, and retries
// activation for every lens previously refused because its expansion was
// unknown (dynamic-type-taxonomy-design.md §14 Fire A item 4; §17.6's "a
// boot refusal is never retried" precondition). Called synchronously, on
// CoreKVSource's single dispatch goroutine, by the taxonomy snapshot/dead
// callbacks main.go wires onto the lens source — see
// lens.CoreKVSource.SetTaxonomyCallback's doc for why nothing here needs its
// own goroutine (rederiveEntry's Rebuild call is the one exception,
// mirroring reloader.update's MatchChange arm exactly).
func (rl *reloader) taxonomyChanged() {
	for _, entry := range rl.liveEntries() {
		rl.rederiveEntry(entry)
	}
	rl.retryRefused()
}

// rederiveEntry re-derives one live entry's taxonomy expansion. A no-op
// unless the lens carries `*` (taxExpansionLabels non-empty) and the
// resolver's current answer actually differs from what was last COMMITTED
// for it (entry.taxExpansion/taxExpansionStatus, guarded by entry.taxMu —
// see its doc) — comparing against that cached baseline, not against the
// running pipeline's already-published rule state, is what makes "did this
// change" answerable at all.
//
// A real change re-runs exactly the §6.2-§6.4 sequence reloader.update's
// MatchChange arm does: UseFullEngineBranchesForReDerivation (the client
// gate) synchronously first, THEN Rebuild (the server filter + cursor
// reset) in its own goroutine — publishing the client gate widens or
// narrows what the pipeline ADMITS immediately, and only the Rebuild
// afterward can move a JetStream consumer's cursor, so the ordering is
// load-bearing in both directions (§6.2's asymmetric-consequence argument:
// a too-broad server filter merely costs extra delivered-then-skipped
// events, while a too-narrow client gate silently acks-and-drops with no
// other write path). A shrink goes through Rebuild exactly like a grow —
// never an in-place filter narrowing (§6.4). UseFullEngineBranchesForReDerivation
// (not UseFullEngineBranches) is used deliberately: a LIVE re-derivation
// that finds the expansion StatusUnknown must degrade to the broad filter,
// never keep projecting against a set just proven untrustworthy (§6.5) —
// unlike ACTIVATION, which correctly refuses outright (§4.2). See that
// method's doc.
//
// The baseline (entry.taxExpansion/taxExpansionStatus) is committed
// immediately after the publish it describes, on this same goroutine, which
// is what makes "unchanged" answerable at all: it means "the gate the
// pipeline is publishing right now came out of exactly this resolver answer".
// It records the ANSWER, not the set the gate matches against — on the
// degrade path those differ, and entry.taxExpansion's own doc has the full
// statement, including why the two writes are ordered by this goroutine
// rather than by a lock. Whether the consumer's registered filter has caught
// up is the separate question entry.taxRebuildPending answers, and the fast
// path returns early only when BOTH hold — the answer is unchanged AND the
// rebuild behind it succeeded.
//
// One latch for both facts is what the A→B→A sequence breaks. Baseline E0;
// the taxonomy moves to E1, so the gate is published as E1 and the Rebuild
// fails on an ordinary NATS blip; the taxonomy moves back to E0. With the
// baseline advanced only on rebuild success it would still read E0, compare
// EQUAL to the fresh answer, and return early — leaving a client gate on E1
// over a consumer filter on E0 with no other write path to either. Splitting
// them makes the second event find (unchanged, pending) and drive the
// outstanding rebuild, which is the whole of what is still owed.
func (rl *reloader) rederiveEntry(entry *pipelineEntry) {
	if len(entry.taxExpansionLabels) == 0 {
		return
	}
	expanded, _, status, _ := rl.resolver.Expand(entry.taxExpansionLabels)
	entry.taxMu.Lock()
	unchanged := taxonomyExpansionEqual(expanded, status, entry.taxExpansion, entry.taxExpansionStatus)
	pending := entry.taxRebuildPending
	entry.taxMu.Unlock()
	if unchanged && !pending {
		return
	}
	ruleID := entry.rule.ID
	if !unchanged {
		if err := entry.pipeline.UseFullEngineBranchesForReDerivation(rl.fullEngine, entry.rule.CompiledRule, entry.rule.CompiledBranches); err != nil {
			// The pipeline keeps its previous rule state — useFullEngineBranches
			// publishes nothing on error (pipeline.go's own doc). The baseline
			// is left describing that still-published gate, so the NEXT taxonomy
			// event tries again rather than latching onto a failed answer as if
			// it were the current one.
			rl.refuse(entry, ruleID,
				"taxonomy re-derivation: label expansion refused — the pipeline keeps its previous rule state",
				"err", err)
			return
		}
		// The answer the gate came out of — which on the StatusUnknown
		// degrade is (nil, Unknown) while the gate itself matches against the
		// pipeline's carried expansion. Recording the answer is what makes the
		// resolver's return to a resolvable state compare UNEQUAL and
		// republish (taxonomyExpansionEqual compares Status first).
		entry.taxMu.Lock()
		entry.taxExpansion = expanded
		entry.taxExpansionStatus = status
		entry.taxMu.Unlock()
	}
	// An unchanged answer republishes nothing — the gate already in force IS
	// the answer — and only re-drives the rebuild it is still owed.
	markTaxonomyRebuildPending(entry)
	rl.startTaxonomyRebuild(entry, ruleID,
		"taxonomy re-derivation: rebuild failed — the Core KV consumer filter may still carry the pre-change label set; a later taxonomy event will retry")
}

// markTaxonomyRebuildPending records that entry's client gate has just been
// (re)published and owes a Rebuild. Advancing taxGen here is what retires any
// rebuild goroutine still working for an older gate: its answer describes a
// gate no longer in force, so it must not clear the flag this sets.
func markTaxonomyRebuildPending(entry *pipelineEntry) {
	entry.taxMu.Lock()
	entry.taxRebuildPending = true
	entry.taxGen++
	entry.taxMu.Unlock()
}

// startTaxonomyRebuild drives the Rebuild owed to entry's current client gate,
// in its own goroutine — Rebuild is a network round trip and this runs on
// CoreKVSource's single dispatch goroutine, which every other lens's events
// share (reloader.update's MatchChange arm spawns for the same reason).
//
// At most one such goroutine runs per entry, and it re-reads the gate
// generation after each attempt: a publication that lands while a rebuild is
// in flight is picked up by the SAME goroutine on its next pass instead of
// racing a second one, so the entry converges on the newest gate rather than
// on whichever rebuild happened to finish last. A failure leaves
// taxRebuildPending set and stops — the next taxonomy event is what retries,
// exactly as this file's rederiveEntry doc describes — and is recorded on the
// lens's health entry under failureReason.
func (rl *reloader) startTaxonomyRebuild(entry *pipelineEntry, ruleID, failureReason string) {
	entry.taxMu.Lock()
	if entry.taxRebuildRunning {
		// The running goroutine re-reads taxGen on its way out and will make
		// another pass for the gate just published.
		entry.taxMu.Unlock()
		return
	}
	entry.taxRebuildRunning = true
	entry.taxMu.Unlock()

	go func() {
		for {
			entry.taxMu.Lock()
			gen, pending := entry.taxGen, entry.taxRebuildPending
			if !pending {
				entry.taxRebuildRunning = false
				entry.taxMu.Unlock()
				return
			}
			entry.taxMu.Unlock()

			err := entry.pipeline.Rebuild(rl.ctx, false)

			entry.taxMu.Lock()
			superseded := entry.taxGen != gen
			if err == nil && !superseded {
				entry.taxRebuildPending = false
			}
			if !superseded {
				entry.taxRebuildRunning = false
			}
			entry.taxMu.Unlock()

			if err != nil {
				rl.refuse(entry, ruleID, failureReason, "err", err)
			}
			if !superseded {
				return
			}
		}
	}()
}

// retryRefused re-attempts activation for every lens recorded in refused —
// the mechanism that keeps a lens refused for an unknown taxonomy expansion
// from staying dark forever: without it, only a lens-definition edit or a
// process restart would ever re-attempt activation. A lens is removed from
// refused by main.go's startPipeline the instant a retry succeeds; one that
// fails again (for the same or a different reason) stays queued for the
// NEXT taxonomy event, which is the only thing that will ever retry it
// again.
//
// A rule the source (CoreKVSource) no longer knows at all — its definition
// was tombstoned while queued here — is evicted rather than retried.
// remover.remove and pipelineDeleter.Delete already evict unconditionally on
// their own triggers; this is the belt to that braces, catching any path
// that reaches a deletion without going through either. ruleKnown is nil in
// tests that do not wire one, in which case every queued rule is retried
// unconditionally.
func (rl *reloader) retryRefused() {
	rl.refusedMu.Lock()
	pending := make([]*lens.Rule, 0, len(rl.refused))
	for _, r := range rl.refused {
		pending = append(pending, r)
	}
	rl.refusedMu.Unlock()
	for _, r := range pending {
		if rl.ruleKnown != nil && !rl.ruleKnown(r.ID) {
			rl.clearRefusedForTaxonomy(r.ID)
			continue
		}
		rl.activateForTaxonomy(r)
	}
}

// recordRefusedForTaxonomy registers r as refused for an unknown taxonomy
// expansion. Its only caller, main.go's startPipeline, calls it on the
// UseFullEngineBranches failure path, and only when r's expansion-label
// union is non-empty — an activation failure for any OTHER reason must
// never reach here, or a lens broken for an unrelated cause would be
// retried forever on every taxonomy event instead of staying refused until
// an operator fixes it.
func (rl *reloader) recordRefusedForTaxonomy(r *lens.Rule) {
	rl.refusedMu.Lock()
	defer rl.refusedMu.Unlock()
	if rl.refused == nil {
		rl.refused = make(map[string]*lens.Rule)
	}
	rl.refused[r.ID] = r
}

// updateRefusedForTaxonomy replaces the queued rule for newLens.ID with
// newLens if one is currently queued in refused, reporting whether it was.
// The caller (reloader.update, on its unknown-lens arm) uses this so a spec
// edit landing while a `*` lens is refused-and-queued is not silently
// dropped and superseded by the pre-edit rule once a retry finally succeeds
// — this file's own header states the governing principle for the opposite
// direction ("a refused edit must not become the baseline for the next
// one"); this is the same principle applied to the update itself, not the
// entry it targets.
func (rl *reloader) updateRefusedForTaxonomy(newLens *lens.Rule) bool {
	rl.refusedMu.Lock()
	defer rl.refusedMu.Unlock()
	if _, queued := rl.refused[newLens.ID]; !queued {
		return false
	}
	rl.refused[newLens.ID] = newLens
	return true
}

// clearRefusedForTaxonomy removes ruleID from the refused set. Called
// unconditionally by every path that means "this lens ID is gone or fully
// activated" — main.go's startPipeline on the successful registry insert
// (not merely a successful UseFullEngineBranches, which can still be
// followed by a later-stage activation failure with no registry entry to
// show for it), remover.remove on a tombstone, and pipelineDeleter.Delete on
// an operator-triggered delete — regardless of whether ruleID happens to be
// queued right now. Idempotent: deleting an absent map key is a no-op.
func (rl *reloader) clearRefusedForTaxonomy(ruleID string) {
	rl.refusedMu.Lock()
	defer rl.refusedMu.Unlock()
	delete(rl.refused, ruleID)
}
