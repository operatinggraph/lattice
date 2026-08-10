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

// expansionShrank reports whether next drops anything prevResolved carried —
// a label gone entirely, or a label that kept fewer members than it had.
//
// That is the question a retraction turns on. A GROW is self-repairing: the
// widened filter delivers the newly-admitted type's events and the replay
// projects them. A SHRINK is not: the narrowed filter admits no event for the
// dropped subtype, so nothing will ever arrive to retract the rows already
// projected for it, and they orphan in the target (on a grant-producing lens,
// as live grants). The rebuild behind a shrink therefore has to clear the
// target and re-derive from what the new gate admits.
//
// Members ADDED, and labels added, are not a shrink — a set can grow and
// shrink at once, and the "or" of the two is still a shrink, which is what the
// per-label scan over prevResolved answers.
//
// prevResolved is the last RESOLVED answer (pipelineEntry.taxExpansionResolved),
// never the raw last answer: comparing against a (nil, StatusUnknown) degrade
// would read every label as dropped and truncate a live lens over a transient
// resolver fault. An empty or nil prevResolved is never a shrink — there is
// nothing recorded to have lost.
func expansionShrank(prevResolved, next map[string]map[string]struct{}) bool {
	for label, prevMembers := range prevResolved {
		nextMembers, ok := next[label]
		if !ok {
			return true
		}
		for member := range prevMembers {
			if _, ok := nextMembers[member]; !ok {
				return true
			}
		}
	}
	return false
}

// consumerFilterShrank reports whether the label set a lens's Core KV consumer
// ADMITS got smaller across a rule swap — the MATCH-edit counterpart of
// expansionShrank, and the same retraction question.
//
// It works on Pipeline.ConsumerFilterLabels' answer rather than on the filter
// SUBJECTS, because the subjects encode the relation dimension as well as the
// label one: a lens narrowing its relation set publishes
// `...lnk.<label>.*.<relation>.>` where a label-narrowed one publishes
// `...lnk.<label>.>`, and those two strings share no prefix even though the
// second admits strictly MORE. Diffing subject strings therefore reports a
// widening — a relation-narrowed filter falling back to label-narrowed, or the
// subject-budget degrade — as though labels had been dropped, which would ask
// for a truncate on a lens that lost nothing.
//
// A lens that is not narrowed admits every label in the bucket, so
// not-narrowed → narrowed is the largest shrink there is, and narrowed →
// not-narrowed is a grow.
//
// What it does NOT answer is a narrowing WITHIN the label dimension it
// compares: a same-labels edit that only tightens the relation set or a WHERE
// predicate reads as no change here. Those rows are retracted by the
// anchor-presence check on the replayed event instead
// (Pipeline.evaluatePlain) — the event that would re-derive the row is still
// delivered, which is exactly what a dropped LABEL takes away.
func consumerFilterShrank(prevLabels map[string]struct{}, prevNarrowed bool, nextLabels map[string]struct{}, nextNarrowed bool) bool {
	if !prevNarrowed {
		return nextNarrowed
	}
	if !nextNarrowed {
		return false
	}
	for label := range prevLabels {
		if _, ok := nextLabels[label]; !ok {
			return true
		}
	}
	return false
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
//
// The loop below is the fan-out one taxonomy event produces over the whole live
// corpus. Each entry's Rebuild is a durable JetStream consumer delete-recreate,
// so it is queued on the bounded rebuild scheduler rather than started here —
// see taxonomyRebuildConcurrency. The enqueue never blocks, which is what keeps
// the sweep off the dispatch goroutine every other lens's events arrive on.
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
// reset) off this goroutine, on the bounded rebuild scheduler
// (taxonomy_rebuild_scheduler.go) — publishing the client gate widens or
// narrows what the pipeline ADMITS immediately, and only the Rebuild
// afterward can move a JetStream consumer's cursor, so the ordering is
// load-bearing in both directions (§6.2's asymmetric-consequence argument:
// a too-broad server filter merely costs extra delivered-then-skipped
// events, while a too-narrow client gate silently acks-and-drops with no
// other write path). A shrink goes through Rebuild exactly like a grow —
// never an in-place filter narrowing (§6.4) — but TRUNCATING, which is the
// whole of the retraction: the narrowed filter admits no event for the dropped
// subtype, so clearing the target and re-deriving from what the new gate
// admits is the only thing that removes its already-projected rows
// (entry.taxRebuildTruncate). UseFullEngineBranchesForReDerivation
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
	// A shrink is only ever computed from a RESOLVED answer. StatusUnknown
	// degrades to the broad filter and keeps the last known good matcher
	// (§6.5), so it drops nothing — reading it as a shrink would blank a live
	// lens's target over a transient resolver fault.
	shrank := status != taxonomy.StatusUnknown && expansionShrank(entry.taxExpansionResolved, expanded)
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
		if status != taxonomy.StatusUnknown {
			entry.taxExpansionResolved = expanded
		}
		entry.taxMu.Unlock()
	}
	// An unchanged answer republishes nothing — the gate already in force IS
	// the answer — and only re-drives the rebuild it is still owed. The
	// truncate the shrink owes is committed by the SAME critical section that
	// advances taxGen: a rebuild worker reading the two apart would compute
	// "not superseded" against the new generation and clear a pending flag for
	// a truncate it never performed.
	markTaxonomyRebuildPending(entry, !unchanged && shrank && rl.truncateIsSafe(entry, ruleID))
	rl.startTaxonomyRebuild(entry, ruleID,
		"taxonomy re-derivation: rebuild failed — the Core KV consumer filter may still carry the pre-change label set; a later taxonomy event will retry")
}

// markTaxonomyRebuildPending records that entry's client gate has just been
// (re)published and owes a Rebuild, truncating when truncate is set. Advancing
// taxGen here is what retires any rebuild goroutine still working for an older
// gate: its answer describes a gate no longer in force, so it must not clear
// the flag this sets.
//
// The truncate is committed HERE, in the same critical section as the taxGen
// bump, and never by the caller in a section of its own. A worker that read the
// flag between the two would compare its captured generation against the
// already-advanced one, find itself NOT superseded, and clear taxRebuildPending
// and taxRebuildTruncate together for a truncate it never performed — the
// shrink silently not retracting, which is the whole defect the flag exists to
// prevent. It is only ever raised, never lowered: a grow arriving while a
// shrink's rebuild is still owed must not cancel the truncate that rebuild owes.
func markTaxonomyRebuildPending(entry *pipelineEntry, truncate bool) {
	entry.taxMu.Lock()
	entry.taxRebuildPending = true
	if truncate {
		entry.taxRebuildTruncate = true
	}
	entry.taxGen++
	entry.taxMu.Unlock()
}

// truncateIsSafe reports whether the rebuild entry owes may clear its target,
// and says so on the lens's log when it may not.
//
// A shrink retracts by truncating, but only a target whose truncate is CONFINED
// to this lens's rows may be cleared: NatsKVAdapter.Truncate with no bound key
// prefix purges the whole bucket, and several shipped lenses share a bucket
// with other producers (see Pipeline.RebuildTruncateIsScoped, and
// pipelineEntry.taxRebuildTruncate for what a refused truncate means for the
// dropped rows). Wiping every sibling producer's keys is not a smaller harm
// than leaving this lens's dropped rows behind; it is a much larger one.
func (rl *reloader) truncateIsSafe(entry *pipelineEntry, ruleID string) bool {
	if entry.pipeline == nil || !entry.pipeline.RebuildTruncateIsScoped() {
		rl.logger.Warn("lens admitted-set shrank but its target truncate is not confined to this lens's rows — "+
			"the rebuild will NOT clear the target, so rows for the dropped labels are not retracted by it",
			"lensId", ruleID)
		return false
	}
	return true
}

// startTaxonomyRebuild queues the Rebuild owed to entry's current client gate,
// off CoreKVSource's single dispatch goroutine — Rebuild is a network round
// trip and that goroutine is shared by every other lens's events
// (reloader.update's MatchChange arm queues for the same reason). The enqueue
// itself never blocks, whatever the queue depth: blocking here would stall the
// source, not merely the sweep.
//
// The work does NOT get a goroutine of its own. One taxonomy event re-derives
// the whole live corpus, and a goroutine per entry would ask the NATS server
// for one durable delete-recreate per lens all at once; the scheduler holds
// that fan-out to taxonomyRebuildConcurrency at a time (see its doc).
//
// At most one job runs per entry, and it re-reads the gate generation after
// each attempt: a publication that lands while a rebuild is in flight is picked
// up by the SAME job on its next pass instead of racing a second one, so the
// entry converges on the newest gate rather than on whichever rebuild happened
// to finish last. That latch — taxRebuildRunning, taken here — is also what
// coalesces the queue: an entry already queued or already running is never
// queued twice. A failure leaves taxRebuildPending (and taxRebuildTruncate) set
// and stops — the next taxonomy event is what retries, exactly as this file's
// rederiveEntry doc describes — and is recorded on the lens's health entry
// under failureReason.
func (rl *reloader) startTaxonomyRebuild(entry *pipelineEntry, ruleID, failureReason string) {
	entry.taxMu.Lock()
	if entry.taxRebuildRunning {
		// The queued (or running) job re-reads taxGen on its way out and will
		// make another pass for the gate just published.
		entry.taxMu.Unlock()
		return
	}
	entry.taxRebuildRunning = true
	entry.taxMu.Unlock()

	rl.taxRebuild.enqueue(rl.ctx, taxonomyRebuildJob{
		entry:   entry,
		key:     ruleID,
		run:     func() { rl.driveTaxonomyRebuild(entry, ruleID, failureReason) },
		abandon: func() { releaseTaxonomyRebuild(entry) },
	})
}

// releaseTaxonomyRebuild hands back the single-flight latch without touching
// taxRebuildPending or taxRebuildTruncate: the rebuild those two describe is
// still owed, and leaving them set is what lets the next taxonomy event drive
// it.
func releaseTaxonomyRebuild(entry *pipelineEntry) {
	entry.taxMu.Lock()
	entry.taxRebuildRunning = false
	entry.taxMu.Unlock()
}

// driveTaxonomyRebuild is the body of one queued job: rebuild for the gate in
// force, and loop while a newer gate lands underneath it. It runs on a
// scheduler worker, so it occupies one of the taxonomyRebuildConcurrency slots
// for its whole life — including across the loop, which is what keeps a lens
// being re-derived repeatedly from taking a second slot.
//
// That loop is also the one starvation shape here: a lens whose gate is
// republished faster than its rebuilds finish holds its slot indefinitely, and
// with the bound at 2 a pair of such lenses would stall every other lens's
// rebuild behind them. It is bounded in practice by taxonomy events being
// operator/package-driven rather than data-driven, and by the settle window
// (taxonomyRebuildSettle) collapsing a burst into one pass. No backoff is
// applied; if this is ever observed, that is the mechanism to add.
//
// A rebuild for a lens deleted while the job sat queued is not special-cased
// here. Pipeline.rebuild establishes that the supervisor still manages the
// consumer BEFORE it truncates anything, so a deleted lens's rebuild fails
// without clearing a target nothing would re-derive.
func (rl *reloader) driveTaxonomyRebuild(entry *pipelineEntry, ruleID, failureReason string) {
	for {
		entry.taxMu.Lock()
		gen, pending, truncate := entry.taxGen, entry.taxRebuildPending, entry.taxRebuildTruncate
		if !pending {
			entry.taxRebuildRunning = false
			entry.taxMu.Unlock()
			return
		}
		entry.taxMu.Unlock()

		err := rl.runRebuild(entry, truncate)

		entry.taxMu.Lock()
		superseded := entry.taxGen != gen
		if err == nil && !superseded {
			// Cleared together, under one critical section: a pending rebuild
			// that has lost the truncate it owes would replay a shrink without
			// retracting anything.
			entry.taxRebuildPending = false
			entry.taxRebuildTruncate = false
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
}

// runRebuild performs entry's rebuild, through rl.rebuildPipeline when a test
// injected one and against the real pipeline otherwise.
func (rl *reloader) runRebuild(entry *pipelineEntry, truncate bool) error {
	if rl.rebuildPipeline != nil {
		return rl.rebuildPipeline(entry, truncate)
	}
	return entry.pipeline.Rebuild(rl.ctx, truncate)
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
