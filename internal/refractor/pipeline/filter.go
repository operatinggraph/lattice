package pipeline

import (
	"context"
	"log/slog"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
)

// NarrowedFilterEligible reports whether this pipeline's Core KV consumer may
// be scoped to a narrowed, server-side FilterSubjects set instead of the
// broad $KV.<bucket>.> filter, and if so the exhaustive set of vertex-type
// labels to derive it from — the SAME set useFullEngineBranches already
// computed from every compiled branch's ReferencedLabels(), not a second
// derivation.
//
// The invariant is one sentence: a narrowed consumer is never denied an event
// this pipeline's own CLIENT-side gate would have kept. Server-side filtering is
// therefore strictly more conservative than that gate, derived from the exact
// data the gate already trusts — never a second, independently-fallible
// judgment. Each pipeline shape brings its own client gate, so each brings its
// own eligibility:
//
//   - PLAIN — engineKind == EngineFull (plainReactsTo/plainVertexRelevant have
//     no label data for any other engine) and an EXHAUSTIVE referenced-label set
//     (!reprojectAll): the two conditions those gates already require.
//   - ACTOR-AWARE — the §4.2 conjunction, actorAwareNarrowingLabels. Whether a
//     fan-out arm may skip an event and whether the server may withhold it are
//     the same question (auth-plane-projection-latency-design.md §4.6), so there
//     is one answer to it.
//
// An actor-aware pipeline's FAN-OUT breadth is not bounded by its own MATCH
// labels — the enumerator walks adjacency, so one relevant event can reach
// actors no label names. That is a different question from RELEVANCE: once §4.2
// holds, whether any actor can be affected at all IS bounded by the pattern's
// label set, and only that second question decides delivery. The enumerator
// itself is unaffected by a narrower filter because adjacency is written by its
// own dedicated whole-stream consumer (refractor/consumer/bootstrap.go), not by
// this pipeline's deliveries.
//
// Label-set-to-subject alignment, which is what makes "the exact data" exact
// rather than nearly so: CoreKVNarrowedFilters emits a vertex form per label,
// and a Contract #1 aspect key is the 4-segment vtx.<type>.<id>.<localName>, so
// a label's vertex form already covers its aspects — which is what the aspect
// arm judges by parent type. It emits a source-pinned AND a target-pinned link
// form, so a link is admitted when EITHER endpoint type is in the set — which is
// what the link arm judges, skipping only when NEITHER is. The RELATION segment
// of those link forms is a separate dimension with its own client conjunct and
// its own degradation, decided at ConsumerFilter rather than here: this
// accessor answers the label question alone.
//
// A pipeline whose install has not finished answers (nil, false) here, not the
// plain branch's verdict — see narrowedFilterEligible. An eligibility answered
// off components that are not installed yet is a claim about a lens that does
// not exist, and this accessor is exactly where an activation-path caller would
// otherwise pick one up.
func (p *Pipeline) NarrowedFilterEligible() (labels map[string]struct{}, ok bool) {
	return p.narrowedFilterEligible(p.ruleState())
}

// narrowedFilterEligible is NarrowedFilterEligible against a snapshot the
// caller already holds — see actorAwareNarrowingLabels for why the in-pipeline
// callers must not take their own.
//
// It carries the install-completeness guard itself, rather than leaving it to
// each caller, so eligibility is unanswerable-by-construction on a pipeline
// whose install has not finished and a fourth entry point inherits the refusal
// instead of having to remember it. ConsumerFilter still tests the same
// condition ahead of this call: it owes the health entry a REASON and an
// operator a log line, and neither survives being flattened into a bare
// not-eligible here.
func (p *Pipeline) narrowedFilterEligible(rs ruleState) (labels map[string]struct{}, ok bool) {
	if p.actorEnumerator == nil && rs.declaresActorAnchor {
		return nil, false
	}
	if p.actorEnumerator != nil {
		return p.actorAwareNarrowingLabels(rs)
	}
	if rs.engineKind != ruleengine.EngineFull || rs.reprojectAll {
		return nil, false
	}
	return rs.reprojectLabels, true
}

// ConsumerFilter derives this lens's Core KV consumer filter from its CURRENT
// compiled rule: a narrowed, server-side FilterSubjects set when the lens
// qualifies (NarrowedFilterEligible, and the deduped label count at or under
// maxNarrowedFilterLabels), or the broad $KV.<bucket>.> FilterSubject
// otherwise. Exactly one of the two filter return values is non-empty.
//
// The third return, FilterDecision, is a REPORT of the choice this function
// just made, for the lens's health entry. It is derived on the same pass, from
// the same conditions, so an operator's view of the footprint cannot disagree
// with the footprint. It has no effect on either filter value.
//
// Pure over the pipeline's current state, not cached, so every caller
// recomputes the identical value from the identical inputs with nothing to
// keep in sync: the initial activation (RunOn's caller builds the spec from
// this) and a later Rebuild both call it. Rebuild MUST call it again rather
// than reuse whatever filter activation chose — a MATCH hot-reload's
// UseFullEngineBranches call can widen or narrow the referenced-label set
// before the next rebuild, and a stale filter left in place would silently
// under-deliver forever (a JetStream filter update never resets the
// consumer's cursor — nats-server v2.14.0).
//
// This is also the one place a per-event predicate gets SNAPSHOTTED, because a
// consumer's filter is fixed at registration. actorAwareNarrowingLabels is
// deliberately lazy — the host installs its inputs in stages, so a snapshot
// taken mid-installation reads a later stage's component as absent — and the
// snapshot here is sound only when every one of those stages has already run.
// The order that satisfies it, verified live: UseFullEngineBranches →
// InstallActorAggregate (enumerator, pattern-closure, sweep plan) →
// SetSecureDecryptor → ConsumerFilter → RunOn (cmd/refractor/main.go).
//
// GETTING THAT ORDER WRONG WOULD COST CORRECTNESS, NOT MERELY NARROWING, and
// the failure is counter-intuitive enough to state outright: a missing stage
// does not naturally fall back to the broad filter. With the enumerator not yet
// installed this pipeline is indistinguishable from a plain one, so
// narrowedFilterEligible would take the PLAIN branch — whose two conditions
// (full engine, exhaustive labels) UseFullEngineBranches has already satisfied.
// The result would be a narrowed filter granted with NONE of §4.2's conjuncts
// evaluated: no pattern-closure, no sweep plan, no anchor-type-in-labels, no
// decryptor check — with the relation dimension riding along on top, since the
// rule's relation set is exhaustive whatever the host has installed. An early
// call therefore reaches for the MOST aggressive filter, not the broadest, and a
// missed anchor soft-delete is an over-grant that (per the paragraph below) no
// revert recovers.
//
// The install-completeness guard is what closes that off for every lens whose
// declaration this pipeline can see. It compares the lens's DECLARED kind
// against what is installed: a rule whose cypher pins `{key: $actorKey}`
// (rs.declaresActorAnchor) with no enumerator on the pipeline is an install
// caught mid-flight, and this function then refuses to NARROW — broad filter,
// health.FilterBroadReasonInstallIncomplete, logged at Error, louder than the
// label cap's Warn because that arm reports a lens whose footprint regressed
// while this one reports a HOST that wired the pipeline in the wrong order,
// which no lens author can fix and no data will clear. It does not refuse
// activation: the hazard is asymmetric (an over-narrow filter is unrecoverable,
// a broad one is merely wasteful and heals on the next rebuild), so a
// caller-ordering bug must not take a healthy lens down. Any new install stage
// still belongs ABOVE this call — the guard reports the mistake, it does not
// make the ordering free.
//
// "Whose declaration this pipeline can see" is the guard's one gap, and it is
// stated here rather than left on the predicate because this is where the
// soundness claim gets made: full.DeclaresActorAnchor cannot see a
// `{key: $actorKey}` node buried inside an expression shape the hop-index
// builder does not model, because that arm default-denies without descending. It
// would report such a lens plain, and the guard would stay silent on it. No
// shipped cypher is in that shape, and the same blind spot already costs every
// other consumer of that index its answer — but a new one written that way gets
// the pre-install narrowing this guard otherwise refuses.
//
// Two SIBLING surfaces share the guard rather than reimplementing it:
// narrowedFilterEligible carries it (so ConsumerFilterLabels and the exported
// NarrowedFilterEligible probe inherit the refusal), and this function repeats
// the condition only to own the REASON and the log line.
//
// The secure-decryptor conjunct is the one stage the guard does NOT cover, for
// want of a declared signal: a decryptor installed after this call would narrow
// a secure lens whose decryptor conjunct was never evaluated, and nothing in the
// cypher declares that a lens is secure. Secure columns are refused on any
// non-empty projectionKind at spec load, so secure ∧ actorAggregate cannot exist
// today; whoever lifts that ban owns this ordering, and owes the guard a
// declared signal for it.
//
// RECOVERING FROM A WRONG NARROW IS NOT A CODE REVERT, and this is the site that
// has to say so. A JetStream filter update never rewinds the consumer's cursor,
// so widening the filter back — by any means, reverting the code that narrowed
// it included — leaves every event the narrow filter already excluded
// permanently undelivered. The recovery is Pipeline.Rebuild (consumer reset plus
// re-projection from the DeliverLastPerSubject snapshot) or the convergence
// sweep, which is why a sweep plan is one of §4.2's conjuncts rather than a
// nice-to-have: a narrowed lens must always have a standing healer.
// FilterDecision is ConsumerFilter's account of the filter it just derived, in
// the health entry's vocabulary (health.FilterMode* / health.FilterBroadReason*).
//
// It is returned BY the derivation rather than reconstructed from its result so
// the report and the filter cannot drift: there is one traversal of the
// conditions, and every arm that returns a filter returns the decision that
// produced it. Reading it changes nothing — a caller that discards it gets
// byte-identical filter subjects.
type FilterDecision struct {
	// Mode is which filter was chosen: health.FilterModeNarrowedRelation,
	// health.FilterModeNarrowedLabel, or health.FilterModeBroad.
	Mode string
	// LabelCount is how many labels the narrowed filter carries, 0 when broad.
	LabelCount int
	// BroadReason is why the broad filter was chosen, "" when narrowed. Never
	// "" when Mode is broad — see broadFilterReason.
	BroadReason string
}

// broadFilterReason names WHY a lens that reached one of ConsumerFilter's
// not-narrowed arms takes the broad filter. It is total over those states, with
// no default arm to hide a shape nobody enumerated:
//
//   - the rule itself could not produce an exhaustive label set — the snapshot
//     carries the site's own reason (non-exhaustive or taxonomy-unarmed), which
//     is available precisely because reprojectAll and that reason are published
//     together;
//   - everything else is not-eligible: no rule compiled at all, a non-full
//     engine, or an actor-aware lens missing one of §4.2's INSTALLED conjuncts
//     (pattern-closure, sweep plan, anchor type, secure holder types). Those
//     four are properties of how the lens was wired, not of its cypher, and a
//     narrowing that never begins for one of them has no per-site cause to
//     carry.
//
// It deliberately does not cover the label cap: that arm is reached only after
// the derivation SUCCEEDED, so it names its own reason at the site.
func broadFilterReason(rs ruleState) string {
	if rs.narrowingBlocked != "" {
		return rs.narrowingBlocked
	}
	return health.FilterBroadReasonNotEligible
}

// registrationFailedDecision is the footprint a lens ends up with when its
// derived narrowed filter was refused and it fell back to the broad one. It has
// one definition because two paths must agree on it byte-for-byte:
// registerWithFilterFallback writes it the moment the fallback fires, and a
// caller that also reports its own derivation rewrites the SAME value rather
// than skipping its write. Making the two idempotent is what removes the
// ordering question entirely — there is no branch left in which a derivation
// can overwrite a refusal that came after it.
func registrationFailedDecision() FilterDecision {
	return FilterDecision{
		Mode:        health.FilterModeBroad,
		BroadReason: health.FilterBroadReasonRegistrationFailed,
	}
}

// RecordFilterDecision reports a ConsumerFilter decision on this lens's health
// entry. It never returns an error and never propagates one: a health write is
// an observation of a filter that is already registered (or about to be), so
// failing an activation or a rebuild over it would trade a working lens for a
// missing metric. Mirrors the posture every neighbouring health call on those
// two paths already takes — log and carry on.
func (p *Pipeline) RecordFilterDecision(ctx context.Context, dec FilterDecision) {
	if p.reporter == nil {
		return
	}
	if err := p.reporter.SetFilterState(ctx, dec.Mode, dec.LabelCount, dec.BroadReason); err != nil {
		slog.Error("pipeline: record consumer-filter footprint state", "ruleId", p.ruleID, "err", err)
	}
}

func (p *Pipeline) ConsumerFilter() (filterSubjects []string, filterSubject string, decision FilterDecision) {
	// One snapshot for both dimensions: the label set and the relation set must
	// come from the SAME compiled rule, or a hot-reload landing between the two
	// reads would build a filter no rule ever asked for.
	rs := p.ruleState()
	// The ACTOR dimension is read more than once on this path — the guard just
	// below, then again inside narrowedFilterEligible and actorAwareNarrowingLabels
	// — and it deliberately is not hoisted into a local, because a hoist here
	// would cover only the first of those and read as though it covered them all.
	// What makes the repeated reads safe is the field's LIFETIME, not a snapshot:
	// p.actorEnumerator is install-time-only state whose sole writers
	// (projection.InstallActorAggregate, projection.InstallPersonalLens) each
	// store a freshly-built enumerator on the activation goroutine before RunOn,
	// and nothing anywhere clears it. The transition is monotone nil → non-nil and
	// happens once, so the reads can only straddle it in that direction: a later
	// read seeing an enumerator the guard did not is an install still in flight,
	// which the §4.2 conjuncts then evaluate against components not yet supplied
	// — every one of them defaulting to its unsafe-side value, i.e. broad. The
	// reverse straddle would be the dangerous one (the guard satisfied, then the
	// plain branch taken with none of §4.2 evaluated), and it requires a writer
	// that sets the field back to nil. An edit that adds one owes this site a
	// single read.
	//
	// The install-completeness guard, evaluated before any verdict because a
	// verdict derived from an unfinished install is not a verdict about this
	// lens at all: the plain branch it would take answers a question about a
	// DIFFERENT pipeline shape. Declared actor-anchored (the cypher pins
	// `{key: $actorKey}`) with no enumerator installed is the one input whose
	// absence is otherwise indistinguishable from a genuinely plain lens, and
	// it is the only combination this arm claims — a lens the host really did
	// install as plain declares no anchor, so nothing that narrows today stops.
	//
	// Refusing to NARROW rather than refusing to activate is the asymmetry: the
	// broad filter costs delivered-then-skipped events and heals on the next
	// rebuild, while an over-narrow one is unrecoverable, so a caller-ordering
	// bug must not take a healthy lens down.
	if p.actorEnumerator == nil && rs.declaresActorAnchor {
		slog.Error("pipeline: consumer filter derived before the lens's install stages completed — refusing to narrow, falling back to the broad filter",
			"ruleId", p.ruleID)
		return nil, subjects.CoreKVFilter(p.coreKVBucket), FilterDecision{
			Mode:        health.FilterModeBroad,
			BroadReason: health.FilterBroadReasonInstallIncomplete,
		}
	}
	labels, ok := p.narrowedFilterEligible(rs)
	if !ok || len(labels) == 0 {
		// An eligible lens with an EMPTY label set lands here too, and reports
		// not-eligible along with every other shape broadFilterReason covers:
		// its rule is exhaustive, so it carries no per-site cause, and a
		// narrowed filter with no subject in it is not a narrower filter — it
		// is no consumer at all.
		return nil, subjects.CoreKVFilter(p.coreKVBucket), FilterDecision{
			Mode:        health.FilterModeBroad,
			BroadReason: broadFilterReason(rs),
		}
	}
	if len(labels) > maxNarrowedFilterLabels {
		// Unlike the not-eligible/empty arm above (an ordinary, frequent
		// shape — most lenses are not full-engine or not exhaustive, and
		// logging every one of them would make this signal noise), crossing
		// the label cap is a footprint REGRESSION worth an operator's
		// attention (§10.1): a lens author wrote one label and a DIFFERENT
		// package's install pushed the resolved count over the cap, with no
		// other signal anywhere today (registerWithFilterFallback logs a
		// registration FAULT; this is a silent, correct-but-broader
		// derivation, the gap named at design time).
		slog.Warn("pipeline: narrowed filter label count exceeds the cap — falling back to the broad filter",
			"ruleId", p.ruleID, "labelCount", len(labels), "cap", maxNarrowedFilterLabels)
		return nil, subjects.CoreKVFilter(p.coreKVBucket), FilterDecision{
			Mode:        health.FilterModeBroad,
			BroadReason: health.FilterBroadReasonLabelCap,
		}
	}
	labelList := make([]string, 0, len(labels))
	for l := range labels {
		labelList = append(labelList, l)
	}
	// Relation-narrowed when the relation set is exhaustive too and the
	// resulting subject count fits the budget; otherwise the relation-blind
	// narrowed set. Degrading here rather than at NarrowedFilterEligible keeps
	// the label narrowing's own eligibility untouched — the two dimensions fail
	// back independently, and a lens that narrows by label today never stops
	// because its relations do not qualify.
	//
	// The relation dimension is a CORRECTNESS gate before it is a budget one,
	// and what entitles it is that both pipeline shapes carry the matching
	// client-side conjunct off this same published relation set:
	// linkRelationReactsTo, consulted by the plain link arm
	// (evalPlainLinkReprojection) and by the actor-aware one
	// (actorAwareLinkRelevant). A relation-pinned subject therefore withholds
	// exactly the links whose relation an arm skips anyway — strictly more
	// conservative than a gate that ran regardless, never the
	// second, independently-fallible judgment NarrowedFilterEligible's invariant
	// forbids.
	//
	// The pairing holds arm by arm because each arm's relation conjunct arms on
	// the SAME condition that got the lens here. The plain arm's is the one this
	// branch is reached under; the actor-aware arm's is §4.2's conjunction,
	// which is the answer narrowedFilterEligible returned above. A lens failing
	// either keeps the unconditional fan-out AND takes the broad filter — one
	// decision, not two.
	//
	// Over the subject budget the filter falls back to the relation-blind set
	// while the client conjunct keeps skipping, and that is the only asymmetry
	// left. It is the safe direction: the server delivers links the arm then
	// acks and skips, which costs a queue slot and never an event.
	if rs.relationsExhaustive {
		relationList := make([]string, 0, len(rs.reprojectRelations))
		for r := range rs.reprojectRelations {
			relationList = append(relationList, r)
		}
		if len(labelList)*(1+2*len(relationList)) <= maxNarrowedFilterSubjects {
			return subjects.CoreKVRelationNarrowedFilters(p.coreKVBucket, labelList, relationList), "", FilterDecision{
				Mode:       health.FilterModeNarrowedRelation,
				LabelCount: len(labelList),
			}
		}
	}
	return subjects.CoreKVNarrowedFilters(p.coreKVBucket, labelList), "", FilterDecision{
		Mode:       health.FilterModeNarrowedLabel,
		LabelCount: len(labelList),
	}
}

// ConsumerFilterLabels reports the label set the lens's Core KV consumer
// currently ADMITS, and whether it is narrowed to that set at all. A broad
// filter answers (nil, false): it admits every label in the bucket, which is
// not a set that can be compared with another.
//
// It answers the LABEL dimension of what ConsumerFilter derives, and it applies
// the same eligibility and the same label cap, so the two never disagree about
// whether the lens is narrowed. Callers comparing what a lens admitted BEFORE a
// rule swap against what it admits after (cmd/refractor's hot-reload retraction
// decision) need exactly this: the filter SUBJECTS encode the relation
// dimension too, so diffing their strings reports a relation-narrowed filter
// widening to a label-narrowed one — strictly more admitted — as though labels
// had been dropped.
//
// It inherits the install-completeness guard from narrowedFilterEligible, which
// is what keeps "narrowed" meaning the same thing in both answers: an unfinished
// install makes the label verdict unsafe here exactly as it does there.
// Not-narrowed is the answer that keeps the shrink comparison honest — a broad
// filter admits everything, so no caller can read a dropped label out of it.
func (p *Pipeline) ConsumerFilterLabels() (map[string]struct{}, bool) {
	rs := p.ruleState()
	labels, ok := p.narrowedFilterEligible(rs)
	if !ok || len(labels) == 0 || len(labels) > maxNarrowedFilterLabels {
		return nil, false
	}
	out := make(map[string]struct{}, len(labels))
	for l := range labels {
		out[l] = struct{}{}
	}
	return out, true
}
