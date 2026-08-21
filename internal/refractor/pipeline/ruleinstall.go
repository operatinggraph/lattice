package pipeline

import (
	"fmt"
	"log/slog"
	"sort"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
)

// UseFullEngine switches this pipeline's evaluate path to the full
// openCypher engine. cr must be the *full.CompiledRule that lens.Parse /
// corekv_source produced for this rule. Must be called before Run.
//
// Returns an error — and leaves the pipeline's previously published rule
// state untouched — when a `*` label pattern's taxonomy expansion cannot be
// trusted at all (taxonomy.StatusUnknown: no resolver installed, no
// snapshot loaded, an unresolvable label, or a cycle/depth fault). See
// useFullEngineBranches.
func (p *Pipeline) UseFullEngine(eng *full.Engine, cr ruleengine.CompiledRule) error {
	return p.useFullEngineBranches(eng, cr, nil, false)
}

// UseFullEngineBranches is UseFullEngine's multi-walk sibling
// (refractor-shared-keyspace-arbitration-design.md §13.2): branches carries
// a Personal lens's N independently-compiled query branches (lens.Rule.
// CompiledBranches), cr must be branches[0]. Nil/single-element branches
// behaves exactly like UseFullEngine. Must be called before Run.
func (p *Pipeline) UseFullEngineBranches(eng *full.Engine, cr ruleengine.CompiledRule, branches []ruleengine.CompiledRule) error {
	return p.useFullEngineBranches(eng, cr, branches, false)
}

// narrowingBlockRank orders the causes that can clear a rule's exhaustiveness
// by how much SURVIVES fixing the others — lowest rank reported. It is a table
// rather than call order because one derivation can trip several sites and the
// site order is not the actionability order: the unarmed-resolver site runs
// before the zero-concrete-leaves one, so a positional rule would report a
// cause that clears on its own for a rule that also carries one that never
// does.
//
//	0  non-exhaustive        the cypher itself can bind a type no label names,
//	                         or a `*` resolved to no concrete type at all.
//	                         Survives BOTH arming the resolver and repairing
//	                         the taxonomy, so it is always the true verdict.
//	1  taxonomy-unresolvable the taxonomy cannot answer at all. Needs a package
//	                         fix; waiting never clears it.
//	2  taxonomy-unarmed      the answer is known but not guaranteed current.
//	                         The one cause that clears with no edit anywhere,
//	                         so it is reported only when it is the ONLY thing
//	                         blocking narrowing.
var narrowingBlockRank = map[string]int{
	health.FilterBroadReasonNonExhaustive:        0,
	health.FilterBroadReasonTaxonomyUnresolvable: 1,
	health.FilterBroadReasonTaxonomyUnarmed:      2,
}

// narrowingBlockRankOf ranks an UNREGISTERED reason last rather than first.
// Reading the map directly would give one the zero value, which silently
// outranks every real cause — precisely the failure a written-down precedence
// exists to remove, and precisely what a new site added without a table row
// would hit.
func narrowingBlockRankOf(reason string) int {
	if rank, ok := narrowingBlockRank[reason]; ok {
		return rank
	}
	return len(narrowingBlockRank)
}

// UseFullEngineBranchesForReDerivation is UseFullEngineBranches' LIVE
// re-derivation sibling (dynamic-type-taxonomy-design.md §14 Fire A item 4).
// Byte-identical to UseFullEngineBranches except for the taxonomy.
// StatusUnknown branch, where the two entry points are governed by different
// sections of the design: an ACTIVATION (UseFullEngineBranches) refuses
// outright per §4.2 — nothing published, stay dark, which is safe because
// the pipeline was never running against this rule. A LIVE pipeline
// re-deriving cannot take that same refusal without violating §6.5's "never
// keep a stale narrow set": it is already projecting against a set the
// resolver has just proven it cannot currently trust, so this entry point
// degrades to the broad filter (the existing exhaustive=false /
// reprojectAll=true machinery every other non-StatusArmed answer already goes
// through) and PUBLISHES that, instead of refusing.
//
// The degradation is on the DELIVERY axis alone. The matcher keeps this
// pipeline's last known good expansion (Pipeline.labelExpansion), because a
// broad filter over a blank matcher is not "slower", it is zero rows — and a
// Rebuild against zero rows retracts every row the lens has ever written.
// Correct-but-slower means the row set stays right and only the narrowing is
// lost. The one state that cannot degrade is a rule with a label the carried
// expansion does not cover, which refuses like activation does and which no
// RUNNING `*` lens can be in (see the StatusUnknown arm's own comment). Never wrong, only un-narrowed, until the next successful
// re-derivation — the caller (reloader.rederiveEntry) still drives the
// Rebuild that re-registers the widened server-side filter; this call only
// updates the client gate.
func (p *Pipeline) UseFullEngineBranchesForReDerivation(eng *full.Engine, cr ruleengine.CompiledRule, branches []ruleengine.CompiledRule) error {
	return p.useFullEngineBranches(eng, cr, branches, true)
}

func (p *Pipeline) useFullEngineBranches(eng *full.Engine, cr ruleengine.CompiledRule, branches []ruleengine.CompiledRule, liveReDerivation bool) error {
	// Everything is derived into locals first and published under one Lock at
	// the end (see ruleMu): a reader must never observe a half-rewritten rule.
	// Nothing is published at all if this function returns an error part way
	// through — the taxonomy-expansion refusal below is an activation
	// failure, not a rule swap, so the pipeline keeps evaluating whatever
	// rule (if any) it already had.
	next := ruleState{
		engineKind: ruleengine.EngineFull,
		engine:     eng,
		cr:         cr,
	}
	// Unconditional, not just the len>1 arm: a reload (cmd/refractor/reload.go)
	// calls this on an EXISTING pipeline, so a lens edited from 2+ Walks down
	// to a single Walk must clear both fields — leaving them set would keep
	// evaluating a Walk the new spec no longer has.
	if len(branches) > 1 {
		next.branches = branches
		next.walkOwnedColumns = walkOwnedColumns(branches)
	}
	// Pin the vertex types this lens's patterns can bind, so the plain
	// aspect/link reprojection arms skip events on types the lens cannot
	// read (an unbounded label set — unlabeled node pattern or var-length
	// relationship — disables the skip; every event reprojects). Union
	// across every branch for a multi-walk lens: each branch's own clauses
	// bind only that walk's types, but the plain-reprojection arms reason
	// about the lens as a whole.
	next.reprojectAll = true
	all := []ruleengine.CompiledRule{cr}
	if len(branches) > 1 {
		all = branches
	}
	labels := map[string]struct{}{}
	relations := map[string]struct{}{}
	exhaustive := true
	relationsExhaustive := true
	// Why this rule's label set ends up non-exhaustive, in the health entry's
	// own vocabulary — carried onto the published ruleState so ConsumerFilter
	// can REPORT the cause it already acts on, rather than re-deriving it from
	// a snapshot that no longer knows which site fired. Paired with every
	// `exhaustive = false` below and set nowhere else, which is what makes
	// `narrowingBlocked != ""` and `!exhaustive` the same condition.
	//
	// The highest-RANKED cause wins (narrowingBlockRank), not the first one
	// written: the sites do not fire in actionability order, so positional
	// precedence would report a transient cause for a rule that also carries a
	// permanent one and leave an operator waiting for an arming that changes
	// nothing.
	narrowingBlocked := ""
	blockNarrowing := func(reason string) {
		if narrowingBlocked == "" || narrowingBlockRankOf(reason) < narrowingBlockRankOf(narrowingBlocked) {
			narrowingBlocked = reason
		}
	}
	expansionNeeded := map[string]struct{}{}
	for _, c := range all {
		fullCR, isFull := c.(*full.CompiledRule)
		if !isFull {
			exhaustive = false
			blockNarrowing(health.FilterBroadReasonNonExhaustive)
			relationsExhaustive = false
			// continue, not break: a LATER branch may still be a
			// *full.CompiledRule carrying `*`, and expansionNeeded below
			// must see it regardless of exhaustive already being lost —
			// otherwise an unresolvable label on that branch would be
			// silently dropped instead of refusing activation loudly
			// through the same path every other `*` branch goes through.
			continue
		}
		// The lens's declared projection kind, collected in the same lockstep
		// walk as the label and relation sets so the three come from one
		// traversal of one rule and cannot disagree about it. One branch
		// declaring the anchor makes the lens actor-anchored.
		if fullCR.DeclaresActorAnchor() {
			next.declaresActorAnchor = true
		}
		ls, ok := fullCR.ReferencedLabels()
		if !ok {
			exhaustive = false
			blockNarrowing(health.FilterBroadReasonNonExhaustive)
		} else {
			for l := range ls {
				labels[l] = struct{}{}
			}
		}
		for l := range fullCR.ExpansionLabels() {
			expansionNeeded[l] = struct{}{}
		}
		// The relation set is derived independently of the label set: a lens
		// can name every label exhaustively while traversing an untyped
		// relationship, or the reverse. Collecting both to the end (rather
		// than breaking on the first non-exhaustive answer) is what lets the
		// two narrowings degrade separately.
		rs, rok := fullCR.ReferencedRelations()
		if !rok {
			relationsExhaustive = false
		} else {
			for r := range rs {
				relations[r] = struct{}{}
			}
		}
	}

	// Taxonomy expansion (dynamic-type-taxonomy-design.md §4). Consulted only
	// when at least one pattern in the query carries the `*` sigil: a
	// sigil-free query's labels are derived from ReferencedLabels() alone,
	// above, and the resolver is never called for it (§14 Fire A item 3's
	// inertness guarantee).
	var expandedLabels map[string]map[string]struct{}
	if len(expansionNeeded) > 0 {
		status := taxonomy.StatusUnknown
		reason := "no taxonomy resolver is installed on this pipeline"
		var inert map[string]struct{}
		if p.taxonomyResolver != nil {
			expandedLabels, inert, status, reason = p.taxonomyResolver.Expand(expansionNeeded)
		}
		if status == taxonomy.StatusUnknown {
			if !liveReDerivation {
				// §4.2's two-tier fork: a set-KNOWN-but-possibly-stale answer
				// may still activate broad (below); a set UNKNOWN answer must
				// never activate at all, because no filter width rescues a
				// MATCHER evaluating against a wrong expansion. Nothing has
				// been published — the pipeline's previous rule
				// state (if any) is untouched.
				return fmt.Errorf(
					"pipeline: taxonomy expansion unknown for label(s) %s — %s; refusing activation rather than risk projecting the wrong row set",
					sortedLabelList(expansionNeeded), reason)
			}
			// A live re-derivation is governed by §6.5, not §4.2 (see
			// UseFullEngineBranchesForReDerivation's doc): degrade rather
			// than refuse — but degrade on the DELIVERY axis only. Expand
			// answered nil (its own StatusUnknown contract), and publishing
			// that nil would carry the degradation onto the PROJECTION axis
			// too: executor.go's nodeMatches finds no entry for the `*`
			// label and binds nothing, so the lens matches zero rows, and
			// the Rebuild the caller drives next replays every anchor
			// against that blank matcher — on a lens with filter/diff/
			// presence retraction, a mass Delete; on a grant lens, a mass
			// revoke. §6.5 promises a broad FILTER, which costs delivered-
			// then-skipped events; it never promises an empty matcher.
			//
			// So the matcher keeps this pipeline's last known good
			// expansion (carriedLabelExpansion — the set the currently
			// published rule state is already matching against) while
			// exhaustive=false below takes the filter broad and reports
			// taxonomy-unresolvable. Correct-but-slower, which is what §6.5
			// asks for: the rows stay right, only the narrowing is lost,
			// until a later re-derivation finds a trustworthy answer.
			//
			// With nothing carried there is nothing to degrade TO, and this
			// arm refuses like activation does — the asymmetry between the
			// two entry points is "activation refuses; a LIVE lens degrades
			// and keeps serving", and a lens with no published expansion is
			// not a live one: UseFullEngineBranches refuses any `*` lens
			// whose expansion is unknown, so every running `*` lens reached
			// its RunOn with an expansion published. This is the belt to
			// that brace, and it fails the way the brace does.
			//
			// What makes the carry safe is COVERAGE, not mere presence: every
			// label this rule expands must have an entry, because
			// full.WithLabelExpansion threads one map into all of them and
			// executor.go's nodeMatches binds nothing for a `*` label the map
			// does not mention. A map covering some labels and not others
			// would publish a partly-blank matcher, which is the same zero-row
			// Rebuild this arm exists to prevent, reached by a narrower door.
			// Tested here against expansionNeeded rather than argued from what
			// the caller's rule can be: the guard has to hold for the rule in
			// hand, not for the ordering of the reload that produced it.
			expandedLabels = p.carriedLabelExpansion()
			if missing := labelsWithoutExpansion(expansionNeeded, expandedLabels); len(missing) > 0 {
				return fmt.Errorf(
					"pipeline: live taxonomy re-derivation found the expansion unknown for label(s) %s — %s; this pipeline has no previously-resolved expansion for label(s) %s to keep matching against, so it refuses rather than publish a matcher that binds nothing for them",
					sortedLabelList(expansionNeeded), reason, sortedLabelList(missing))
			}
			slog.Warn("pipeline: live taxonomy re-derivation found the expansion unknown — degrading to the broad filter and keeping the last resolved expansion for matching, rather than keeping a stale narrow set or blanking the matcher",
				"ruleId", p.ruleID, "labels", sortedLabelList(expansionNeeded), "reason", reason)
		} else if !liveReDerivation && len(inert) > 0 {
			// ACTIVATION-only (taxonomy.Resolver.Expand's doc has the full
			// split): a `*` whose resolved closure is exactly {itself}
			// asserts a polymorphism the taxonomy does not currently
			// declare — refused here as an authoring mistake, never a
			// silent no-op. A LIVE re-derivation must NOT take this
			// branch: a concrete type's LAST subtypeOf child can be
			// uninstalled by a DIFFERENT package while this lens is
			// running and correct, and {itself} is the truthful, merely
			// un-widened answer for it right now (§6.5) — refusing here
			// would take the lens's own still-resolvable instances dark
			// along with the widening it lost. liveReDerivation's branch
			// below (via exhaustive/expandedLabels) accepts inert answers
			// exactly like any other resolved label.
			return fmt.Errorf(
				"pipeline: taxonomy expansion for label(s) %s resolves to exactly itself — the `*` sigil asserts a polymorphism the taxonomy does not currently declare; refusing activation rather than accept a no-op sigil",
				sortedLabelList(inert))
		}
		if status != taxonomy.StatusArmed {
			// Known but not guaranteed current: correct-but-slower, never
			// wrong-but-fast. Forces the broad filter even though every
			// referenced label above resolved exhaustively.
			exhaustive = false
			// Two very different states reach here, and only one of them is
			// waiting for something. StatusStale is a loaded snapshot with a
			// dead invalidation consumer — it clears the moment the resolver
			// arms, with no edit anywhere. StatusUnknown is the resolver
			// unable to answer at all (a cycle, an over-depth chain, an
			// ambiguous canonicalName, a vanished abstract, no snapshot ever
			// loaded), reachable here only on a LIVE re-derivation because
			// activation refuses outright above — and it never clears until a
			// package is fixed. Reporting them under one word tells an
			// operator to wait out a state that will not end.
			if status == taxonomy.StatusUnknown {
				blockNarrowing(health.FilterBroadReasonTaxonomyUnresolvable)
			} else {
				blockNarrowing(health.FilterBroadReasonTaxonomyUnarmed)
			}
		}
		// An abstract label with no concrete descendants (or whose
		// descendants are all themselves abstract) resolves to a KNOWN, empty
		// set — Expand reports ok/StatusArmed for it, not StatusUnknown, per
		// §3.4's expanded-set row: "genuinely zero leaves" is a real answer,
		// not a resolver fault. But publishing exhaustive=true on it would
		// make reprojectLabels lose that label's contribution entirely
		// (nothing is unioned in below) while every OTHER gate keyed off
		// reprojectLabels — plainVertexRelevant chief among them, whose false
		// branch acks-and-drops with no fallback — reads the narrowed set as
		// authoritative. That is the "stale narrow set" §6.5 calls the only
		// unacceptable state: the lens goes silently dark on that type while
		// presenting as narrowed and health-green. (Actor-aware lenses do not
		// share this hazard — an unseeded fan-out re-executes rather than
		// relying on the plain reprojection gate — so this is plain-lens
		// specific.) Forces the broad filter instead, exactly like a
		// not-yet-armed resolver.
		// Judged on every path, a carried expansion included: a label whose
		// concrete set is empty is non-exhaustive DURABLY — arming the
		// resolver and repairing the taxonomy both leave it so — and rank 0 is
		// the true verdict for exactly that reason (narrowingBlockRank). A
		// degrade that reported taxonomy-unresolvable over it would send an
		// operator to fix a taxonomy that, once fixed, leaves the lens broad
		// anyway.
		for l, set := range expandedLabels {
			if len(set) == 0 {
				exhaustive = false
				blockNarrowing(health.FilterBroadReasonNonExhaustive)
				slog.Warn("pipeline: taxonomy label resolved to zero concrete types — degrading to the broad filter",
					"ruleId", p.ruleID, "label", l)
			}
		}
		// Every LabelExpand label's own raw string was already added to
		// labels by ReferencedLabels() above (it collects the AST label text
		// unconditionally, blind to the `*` sigil). That string must not
		// survive into reprojectLabels on its own: for an ABSTRACT label it
		// names no instance at all (§3.4's expanded-set row — including it
		// would add a filter subject that can never match, and would let
		// plainVertexRelevant admit a type that cannot exist), and for a
		// CONCRETE label the resolved set already contains it via Expand's
		// reflexivity. So each expanded label is deleted and replaced
		// wholesale by its resolved concrete member set.
		for l := range expandedLabels {
			delete(labels, l)
		}
		for _, set := range expandedLabels {
			for vt := range set {
				labels[vt] = struct{}{}
			}
		}
	}

	if exhaustive {
		next.reprojectLabels = labels
		next.reprojectAll = false
	}
	// Published unconditionally, so the reason is derived fresh from THIS rule
	// alongside the label set it explains and nothing survives a swap: "" here
	// is exactly the exhaustive case, since every site that clears exhaustive
	// sets it.
	next.narrowingBlocked = narrowingBlocked
	if relationsExhaustive {
		next.reprojectRelations = relations
		next.relationsExhaustive = true
	}

	// Publish the taxonomy-resolved compiled rule(s) — a copy carrying
	// LabelExpansion (full.WithLabelExpansion, §4.3), never a mutation of the
	// caller's cr/branches — but only when expansion actually ran; a lens
	// with no `*` keeps the exact rule object it was given, preserving
	// identity as well as behaviour.
	if len(expansionNeeded) > 0 {
		// Recorded on the same snapshot the wrapped rules ride, so the
		// pipeline's last-known-good is by construction the set its matcher is
		// using — the two cannot drift, because one publication writes both.
		next.labelExpansion = expandedLabels
		wrapped := make([]ruleengine.CompiledRule, len(all))
		for i, c := range all {
			if fullCR, isFull := c.(*full.CompiledRule); isFull {
				wrapped[i] = full.WithLabelExpansion(fullCR, expandedLabels)
			} else {
				wrapped[i] = c
			}
		}
		next.cr = wrapped[0]
		if len(branches) > 1 {
			next.branches = wrapped
		}
	}

	// Pin the anchor label(s) an anchor-labeled event can seed the evaluation
	// with. Derived unconditionally like the label set above, and for the same
	// reason: a reload must never leave a previous rule body's anchor armed. A
	// multi-walk lens is excluded outright — branch merging evaluates N
	// independent queries, each with its own anchor, and one seed cannot speak
	// for all of them.
	if len(branches) <= 1 {
		if fullCR, isFull := next.cr.(*full.CompiledRule); isFull {
			if label, ok := fullCR.AnchorLabel(); ok {
				if fullCR.AnchorLabelExpand() {
					if set, ok := expandedLabels[label]; ok {
						next.seedAnchorLabels = set
					}
				} else {
					next.seedAnchorLabels = map[string]struct{}{label: {}}
				}
			}
			// The pattern graph the affected-anchor derivation walks under
			// (auth-plane-projection-latency-design.md §4.7). Derived here for
			// the same two reasons the anchor label is, and excluded on the
			// same multi-walk arm: each branch carries its own anchor, and one
			// graph cannot speak for all of them. WithLabelExpansion threads
			// the SAME resolved sets into every `*` position the graph
			// carries — PositionsBinding, AnchorSideSeeds and the walk's
			// far-end prune all read them (dynamic-type-taxonomy-design.md
			// §5.1's HopIndex-shaped sixth mechanism) — mirroring
			// full.WithLabelExpansion exactly: a no-op when expandedLabels is
			// nil (no `*` anywhere in this query).
			next.anchorHops = fullCR.AnchorHopIndex().WithLabelExpansion(expandedLabels)
			// The plain arm's own terminus (plain-lens-neighbour-anchor-
			// derivation-design.md §4.1/§10) — built alongside anchorHops, on
			// the SAME multi-walk exclusion and the SAME label-expansion
			// threading, so a hot reload can never leave one of the pair
			// stale against the other. Unconditional within this branch: a
			// plain lens (AnchorLabel not ok) still gets a rootHops, since
			// ScanRootHopIndex's terminus is the anchor PATTERN, never
			// `{key: $actorKey}` — the two termini are independent questions
			// answered by the same builder.
			next.rootHops = fullCR.ScanRootHopIndex().WithLabelExpansion(expandedLabels)
		}
	}
	p.publishRuleState(next)
	return nil
}

// sortedLabelList returns labels' keys sorted, for a deterministic error
// message.
func sortedLabelList(labels map[string]struct{}) []string {
	out := make([]string, 0, len(labels))
	for l := range labels {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// labelsWithoutExpansion returns the labels in needed that exp has no entry
// for — the coverage test the live-re-derivation carry-forward turns on.
//
// A PRESENT but empty set is covered: an abstract with no concrete
// descendants is a real, resolved answer (§3.4), and the pattern binding
// nothing is then the truth rather than a blank matcher. Only a MISSING key
// is the un-carryable case, because that is the one executor.go's nodeMatches
// cannot tell apart from "never resolved".
func labelsWithoutExpansion(needed map[string]struct{}, exp map[string]map[string]struct{}) map[string]struct{} {
	missing := map[string]struct{}{}
	for l := range needed {
		if _, ok := exp[l]; !ok {
			missing[l] = struct{}{}
		}
	}
	return missing
}
