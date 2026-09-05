// The plain arm's own affected-anchor derivation
// (plain-lens-neighbour-anchor-derivation-design.md, Increment 2). A plain
// (non-actor-anchored) lens's neighbour event — a vertex, aspect owner, or
// link endpoint of a type that is not the lens's own anchor pattern — reaches
// seedAnchorFor's empty-seed branch (evaluate.go) and today re-derives the
// lens's WHOLE row set. This file gives that branch a second producer,
// mirroring anchor_derivation.go's actor-aware trio (deriveAnchorsFor
// {Vertex,Aspect,Link}) but reading rs.rootHops (Increment 1's
// ScanRootHopIndex) instead of rs.anchorHops, and re-entering
// evaluatePlainFromVertexRaw — the anchor-typed arms' own entry point, minus
// the decrypt wrapper the re-entry already runs inside — once per derived
// anchor instead of running the unseeded evaluation.
//
// The narrowing LICENCE (§5: per-anchor closure, an Auditor that is enrolled,
// unsuppressed and not stale, no $now/$projectedAt, the auth-plane exclusion)
// lives here too, as plainDerivationLicence, and
// plainDerivationIndexForAct is what consults it: in `act` mode a derived
// anchor set decides a plain lens's neighbour event only on a lens the licence
// admits, and every lens it refuses keeps today's whole-corpus rescan. Both
// arms run through the SAME three-way mode switch and the SAME derivationShadow
// counters the actor-aware arm's affectedAnchors already uses
// (anchor_derivation_mode.go, anchor_derivation_shadow.go). A pipeline is one
// lens and is either plain or actor-anchored, never both, so sharing that
// counter state introduces no new state and cannot let the two measurements
// collide.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// DefaultPlainDerivedAnchorCap bounds the number of anchor vertices the plain
// arm's derivation may return before the caller falls back to today's
// unseeded evaluation — a FALLBACK trigger, not a truncation, mirroring
// DefaultDerivationReadCap's own contract (anchor_derivation.go): a derived
// set this large costs more in K seeded evaluations than one whole-corpus
// rescan saves, so acting on it would cost more than it wins.
//
// Its unit is DERIVED ROOT VERTICES, never projected rows: for a lens keyed
// on a neighbour variable, K root bindings can produce a single output row,
// so this bound is a bound on WORK. §4.2's own obligation (iii) is that the
// measurement report both distributions rather than conflate the two.
const DefaultPlainDerivedAnchorCap = 64

// defaultPlainDerivedAnchorCap is the package-wide override, mirroring
// defaultDerivationMode's shape (anchor_derivation_mode.go): one process-wide
// knob because pipelines are built in more than one place, and a startup flag
// threaded through only one of them could be missed.
var defaultPlainDerivedAnchorCap atomic.Int64

// SetDefaultPlainDerivedAnchorCap sets the cap every pipeline without its own
// override uses. n <= 0 restores DefaultPlainDerivedAnchorCap.
func SetDefaultPlainDerivedAnchorCap(n int) {
	defaultPlainDerivedAnchorCap.Store(int64(n))
}

// SetPlainDerivedAnchorCap overrides the derived-anchor cap for this pipeline
// alone. n <= 0 returns it to the package default. Mirrors
// SetAnchorDerivationReadCap's shape (anchor_derivation.go).
func (p *Pipeline) SetPlainDerivedAnchorCap(n int) {
	p.plainDerivedAnchorCapOverride.Store(int64(n))
}

// plainDerivedAnchorCap resolves this pipeline's effective cap: its own
// override, else the package default, else DefaultPlainDerivedAnchorCap.
func (p *Pipeline) plainDerivedAnchorCap() int {
	if n := p.plainDerivedAnchorCapOverride.Load(); n > 0 {
		return int(n)
	}
	if n := defaultPlainDerivedAnchorCap.Load(); n > 0 {
		return int(n)
	}
	return DefaultPlainDerivedAnchorCap
}

// PlainDerivationStatus is one plain lens's act-mode derivation posture, as an
// operator reads it off the heartbeat.
//
// Eligible and Armed answer two different questions, and an operator needs
// both. Eligible is the STATIC half — act mode, plus plainDerivationIndex's
// own AST-derived conjuncts (single branch, a complete resolved rootHops, no
// diffRetraction) — and says nothing about whether a derived set may be ACTED
// on: it is the property a fixed lens shape carries, independent of the
// licence's live auditor-health conjuncts. Armed is Eligible AND
// plainDerivationLicence: the WHOLE gate, so a lens the licence turns back —
// an unenrolled, suppressed or stale auditor, among other conjuncts — is
// Eligible but not Armed, and its heartbeat says exactly that ("declared,
// currently off") rather than omitting the lens as if its shape could never
// support the transport at all.
//
// FellBack and OverCapSize are read whenever Eligible, never gated on Armed:
// plainDerivationDecide's tally increments on a per-event walk failure or an
// over-cap derived set regardless of whether the licence is what is currently
// refusing the lens — a count that has already accrued must stay on the wire,
// not disappear the moment an unrelated licence conjunct (say, the auditor
// going stale) flips. Reading only "act mode and plain" would still be wrong
// for a different reason: a static licence refusal never increments the tally
// at all (plainDerivationDecide's own noteStaticPlainDerivationRefusal path),
// so a permanently-refused lens would publish a permanent zero indistinguishable
// from an armed lens that has never fallen back — which is why Eligible, not
// the mode alone, is the presence gate.
//
// It is answered per heartbeat, off one rule-state snapshot, so a lens whose
// audit goes stale keeps publishing Eligible and the counters but flips Armed
// to false for as long as that holds — the honest reading: the shape is still
// there, the transport is off, and the tally is neither hidden nor frozen.
type PlainDerivationStatus struct {
	Eligible    bool
	Armed       bool
	FellBack    uint64
	OverCapSize int
}

// PlainDerivationStatus reports whether this pipeline's neighbour events COULD
// be decided by a derived anchor set (Eligible), whether they currently ARE
// (Armed), and the act-mode fall-back tally.
//
// The licence is the dearer read — TWO independent auditor status snapshots
// (Status() for enrolment/suppression, and a second inside Stale() for the
// verdict clock) and THREE compiled-rule walks (ReferencesParam for "now" and
// for "projectedAt", plus ProjectsOneRowPerAnchor) — so it is paid only once
// Eligible has already held, and only once per lens per beat, the same order
// as the health-entry read the caller already makes for every lens.
//
// copyLensAuditStatus (cmd/refractor) takes its OWN independent Status()
// snapshot of the same auditor, on the same heartbeat pass but not under any
// shared lock with this call's two reads — so a suppression that starts or
// clears in the instant between them can show on one published field and not
// the other for one beat. Accepted skew: both settle to the same answer on the
// next pass, and neither ever narrows a write on the strength of the other's
// snapshot.
func (p *Pipeline) PlainDerivationStatus() PlainDerivationStatus {
	if p.derivationMode() != DerivationModeAct {
		return PlainDerivationStatus{}
	}
	rs := p.ruleState()
	if _, ready := p.plainDerivationIndex(rs); !ready {
		return PlainDerivationStatus{}
	}
	st := p.AnchorDerivationShadow()
	licensed, _ := p.plainDerivationLicence(rs)
	return PlainDerivationStatus{
		Eligible:    true,
		Armed:       licensed,
		FellBack:    uint64(st.FellBack),
		OverCapSize: int(st.LastOverCapSize),
	}
}

// plainDerivationIndex returns rs.rootHops and whether this pipeline may
// derive from it at all — the plain arm's mirror of derivationIndex
// (anchor_derivation.go), with the plain pipeline's own conjuncts (§4.2 of
// the design):
//
//   - this IS a plain pipeline (no ActorEnumerator, no envelope of either
//     shape) — an actor-aware/personal evaluation's "anchor" is the actor
//     $actorKey names, not a vertex this walk could seed from, and seeding
//     it here would evaluate the wrong entity (mirrors seedAnchorFor's own
//     first conjunct, pipeline.go);
//   - a single-branch lens — a multi-walk lens has N independent queries,
//     each with its own scan root, and one graph cannot speak for all of
//     them (mirrors anchorHops' own multi-walk exclusion, useFullEngineBranches);
//   - rs.rootHops.Complete — every shape ScanRootHopIndex itself refuses
//     (hopindex.go) is not this derivation's to second-guess;
//   - no unresolved `*` position — pruning a far end the taxonomy cannot yet
//     confirm is the unsound direction for a derivation (HopIndex's own doc
//     on UnresolvedExpansionPosition);
//   - the ANCHOR position itself does not carry the `*` sigil
//     (HopIndex.AnchorIsExpanding) — resolved or not. evaluatePlainDerivedAnchors
//     hands the re-entry idx.Labels[idx.Anchor], the AST label, never a derived
//     anchor's own concrete type; seedAnchorFor's re-entrant dispatch tests
//     membership in the RESOLVED closure (rs.seedAnchorLabels), which an
//     abstract AST label is not a member of, so the re-entry would miss its own
//     seed and fall through to a whole-corpus rescan PER derived anchor — the
//     opposite of what deriving was for. No corpus lens has a `*`-sigil anchor
//     today (hopindex.go's Expanded/LabelExpand pair), so this refuses a shape
//     nothing shipped yet relies on;
//   - !p.diffRetraction — a per-anchor seeded row set would read to
//     applyDiffRetraction as "every OTHER anchor's rows are gone" (§3's
//     grounding ledger; the same conjunct seedAnchorFor already enforces at
//     pipeline.go's seedAnchorFor, inherited here rather than re-derived).
//
// There is no "anchor label == enumerator's actor type" conjunct to carry
// from derivationIndex: the terminus's OWN label IS the anchor label the
// caller seeds from — one derivation, so the two cannot disagree.
func (p *Pipeline) plainDerivationIndex(rs ruleState) (full.HopIndex, bool) {
	if p.plainDerivationIndexRefusal(rs) != "" {
		return full.HopIndex{}, false
	}
	return rs.rootHops, true
}

// plainDerivationIndexRefusal is the predicate above, carrying the reason each
// conjunct refuses on. The two are one function so a caller that needs the
// reason — the activation gate's message, the static-eligibility predicate
// below — and a caller that needs only the answer can never be told different
// things about the same lens.
func (p *Pipeline) plainDerivationIndexRefusal(rs ruleState) string {
	if p.actorEnumerator != nil || p.envelopeFn != nil || p.multiEnvelopeFn != nil {
		return "it is actor-aware or personal, so its anchor is the actor rather than a vertex this walk could seed from"
	}
	if len(rs.branches) > 1 {
		return "it is a multi-walk lens, and one scan-root graph cannot speak for N independent queries"
	}
	if !rs.rootHops.Complete {
		return "its scan-root pattern graph is incomplete (" + rs.rootHops.Incomplete + "), so which anchors a neighbour event affects cannot be derived"
	}
	if rs.rootHops.UnresolvedExpansionPosition() >= 0 {
		return "its pattern graph carries a taxonomy-expansion position the resolver has not confirmed, and pruning an unconfirmed far end is the unsound direction"
	}
	if rs.rootHops.AnchorIsExpanding() {
		return "its anchor position carries the taxonomy-expansion sigil, so a derived anchor's re-entry would miss its own seed"
	}
	if p.diffRetraction {
		return "it uses target-diff retraction, whose whole-key-set semantics a per-anchor seeded row set would misread"
	}
	return ""
}

// seedMultiPosition reports whether a plain lens's anchor label — the type a
// seeded event narrows to (seedAnchorFor's contract, pipeline.go) — ALSO
// binds a pattern position other than the anchor itself (§4.4 of the
// design, §1.1's gap). seedAnchorFor's engine-level seed only ever narrows
// the query's ANCHOR pattern (full/executor.go's anchorPattern: the first
// MATCH clause's first node), never a second position the SAME label binds —
// so a lens shaped like `(b:identity)-[:duplicateOf]->(a:identity)`, with a
// column reading `a`'s DATA, silently drops every row where the event
// vertex sits at the OTHER position, because the seeded call only ever asks
// "does this vertex, AS the anchor, satisfy the pattern", never "as the
// other position". identity-domain's identityCredentialBindingsRead is a
// live example of the STRUCTURAL shape (identity bound at two positions),
// though not of the consequence: every one of its columns is key-derived,
// so this predicate reports true for it but no property change at the far
// position can ever move its row (the design doc's build note).
//
// False — including when plainDerivationIndex itself is not ready, meaning
// this derivation has nothing authoritative to say about the lens at all —
// means the narrow seeded call is exactly right and stays untouched: this
// predicate only WIDENS which events route through the derivation, never
// narrows it, so a lens this derivation cannot reason about keeps its
// pre-existing (already correct, for a single-position anchor label)
// behaviour.
func (p *Pipeline) seedMultiPosition(rs ruleState, vertexType string) bool {
	idx, ready := p.plainDerivationIndex(rs)
	if !ready {
		return false
	}
	return len(idx.PositionsBinding(vertexType)) > 1
}

// plainDerivedAnchorReentryKey marks ctx, for the duration of
// evaluatePlainDerivedAnchors' per-anchor loop, as a re-entrant call rather
// than a fresh top-level event — see that function's own "THE REENTRANCY
// SEAM" note for why a lens with a multi-position anchor label would
// otherwise recurse forever through evaluateForEntryRaw's seeded-
// multi-position dispatch (evaluate.go).
type plainDerivedAnchorReentryKey struct{}

// isPlainDerivedAnchorReentry reports whether ctx was marked by
// evaluatePlainDerivedAnchors.
func isPlainDerivedAnchorReentry(ctx context.Context) bool {
	v, _ := ctx.Value(plainDerivedAnchorReentryKey{}).(bool)
	return v
}

// plainDerivationLicence reports whether this plain lens may let a derived
// anchor set DECIDE a neighbour event's outcome — §5 of the design — and, when
// it may not, the reason. Every conjunct is fail-closed, and the refusal is
// returned rather than logged here so the caller decides whether it is a
// once-per-lens note or a per-event fact.
//
// It is evaluated per event off LIVE pipeline fields rather than snapshotted at
// install, for the reason seedAnchorFor and actorAwareNarrowingLabels already
// state (pipeline.go): activation installs components in stages, so a snapshot
// taken during installation reads a later stage's component as absent — and for
// a licence, absent reads as satisfied, which is the fail-open direction.
//
// The conjuncts, in the order the body asks them (auditEnrolment's own ordering
// discipline — the field reads before the two expression walks):
//
//   - not auth-plane. An auth-plane lens projects an authorization surface, so
//     a stale row is an over-grant in one direction or the other. The
//     actor-aware precedent required a repair-capable healer proven end to end
//     before narrowing that plane; this mechanism gives detection only, so the
//     plane is excluded. p.authPlane is the activation path's record of
//     projection.IsAuthPlane (cmd/refractor's startPipeline), which is what
//     makes this conjunct able to fire on a plain pipeline at all.
//   - an enrolled Auditor that is RUNNING. The ratified actor-aware licence
//     requires a Sweeper — "something standing will re-test this row". For a
//     plain lens the only standing thing that can is the divergence Auditor, so
//     that is the conjunct: after narrowing, a diverged row is detected, named
//     and alarmed rather than maybe-silently rewritten by the next neighbour
//     event. Enrolled alone is not enough, and reading only it would be
//     fail-open: it is the INSTALL-TIME verdict, fixed by InstallAudit and never
//     revised, while every pass re-runs the enrolment conjuncts and the pause /
//     rebuild checks (Auditor.suppressed) and publishes the outcome as
//     AuditStatus.Suppression. A lens whose audit is suppressed indefinitely —
//     an operator pause, a hot-reload that moved its anchor to an expanded set,
//     the deployment kill switch thrown after activation — still reads
//     Enrolled, and nothing is re-testing its rows. So both are read, from ONE
//     status snapshot: no auditor at all, a refused one, and a suppressed one
//     all refuse. Suppression is "" before the first pass, which is the honest
//     reading of a freshly activated auditor: enrolled, running, nothing
//     reported.
//   - an audit whose last verdict is RECENT — Auditor.Stale, i.e. less than
//     auditorStaleCycles of the audit's own Interval since AuditStatus.LastPassAt.
//     Enrolled and Suppression together still leave one fail-open state, and it is
//     the worst one: both fields are written BY the tick loop, so a loop that has
//     stopped running at all — crashed, wedged, blocked forever inside a pass —
//     leaves Enrolled true and Suppression empty for the life of the process. A
//     licence reading only those two would take a dead audit for a healthy quiet
//     one indefinitely, and narrow every write behind it. LastPassAt is the one
//     field that ages without anyone writing it, so it is the one that can answer
//     "is something re-testing this lens NOW" rather than "was something once".
//     The window is scaled off the lens's own cadence rather than a second
//     duration, and a zero LastPassAt — an auditor that has never completed a pass
//     — reads as stale, which is the correct fail-closed answer for a write
//     licence: not yet proven is not licensed. That is deliberately the OPPOSITE
//     disposition to the heartbeat's own audit-stall detector, which must not
//     alarm on a freshly activated lens and so rebases its clock at first sight;
//     the two mechanisms share no state and no constant, only a default value
//     chosen to keep them legible together (see auditorStaleCycles).
//   - everything that is a property of the lens's shape, declaration and
//     target: plainDerivationStaticallyEligible below, which the activation
//     gate calls too. Its own doc carries that half's conjunct list, and the
//     tail of this function is literally the call — the two consumers cannot
//     be told different things about one lens.
//
// The auth plane is asked FIRST because it is a single bool and because it is
// the one exclusion no other conjunct can stand in for — a lens on that plane
// is refused whatever its audit is doing, and reading any other reason for it
// would misdirect an operator. The auditor's own health — enrolled,
// unsuppressed, not stale — follows, ahead of everything derived from the rule,
// because it is the next-cheapest (field reads off one status snapshot and a
// clock) and is the conjunct most likely to MOVE under a lens that is otherwise
// permanently eligible. The static prefix, which cannot move at all while the
// rule snapshot stands, is asked last.
func (p *Pipeline) plainDerivationLicence(rs ruleState) (licensed bool, refusal string) {
	if p.authPlane {
		return false, "it projects onto the auth plane, which narrows only behind a repair-capable healer proven end to end"
	}
	auditor := p.Auditor()
	if auditor == nil {
		return false, "no divergence audit is enrolled on it, so nothing standing would re-test a row a narrowed reprojection left behind"
	}
	st := auditor.Status()
	switch {
	case !st.Enrolled:
		reason := "no divergence audit is enrolled on it, so nothing standing would re-test a row a narrowed reprojection left behind"
		if st.Refusal != "" {
			reason += " (" + st.Refusal + ")"
		}
		return false, reason
	case st.Suppression != "":
		return false, "its divergence audit is suppressed (" + st.Suppression + "), so nothing is re-testing its rows while that holds"
	}
	// Every refusal string below is STABLE for as long as the state producing it
	// holds — no elapsed duration is interpolated into one. The rule is stated for
	// what follows because the two arms above interpolate the AUDITOR's own
	// published reason (st.Refusal, st.Suppression) rather than a string this
	// function composes: those are only as stable as the auditor makes them, and a
	// suppression reason carrying a live err.Error() is the auditor's to keep
	// steady (audit.go's noteSuppressed). The caller latches on
	// the reason string to log a refusal at most once (noteStaticPlainDerivationRefusal),
	// and a staleness window lasts hours, so a reason carrying a per-second
	// elapsed would defeat that latch precisely where it matters most and emit a
	// line per neighbour event. The duration is the LOG's to carry as its own
	// field, never the reason's. A never-passed auditor gets its own sentence
	// rather than an elapsed measured from the zero time, which is not a duration
	// anyone can read.
	if stale, _ := auditor.Stale(time.Now()); stale {
		if st.LastPassAt.IsZero() {
			return false, "its divergence audit has not reached a verdict since it was installed, and an audit that has proven nothing yet licenses nothing yet"
		}
		return false, fmt.Sprintf("its divergence audit has not reached a verdict in %d of its own cadence intervals, so nothing is proven to be re-testing its rows",
			auditorStaleCycles)
	}
	return p.plainDerivationStaticallyEligible(rs)
}

// plainDerivationStaticallyEligible is the licence's STATIC prefix: every
// conjunct that is a property of the lens's declaration, its compiled rule and
// its target — with none of the live state the two conjuncts above read (the
// auditor's health, the deployment kill switch) and none of the plane
// exclusion, which is a fact about what the lens projects rather than about
// whether a derived set can be computed for it.
//
// The activation gate and the licence ask the same question
// (secure-plain-lens-retraction-and-audit-design.md §4.4's T1), so they call ONE
// function and the licence's own tail IS that call. A gate restating the
// conjuncts by hand admits whichever ones its copy is missing — a lens the gate
// waves through and the licence will never license, dark in exactly the way the
// gate exists to prevent — and the copy drifts silently, because both sides keep
// passing their own tests. Here the gate cannot drift from the licence without
// failing the licence's.
//
// The conjuncts, in the order the body asks them:
//
//   - the derivation index (plainDerivationIndexRefusal above). Asked first
//     because it answers "is there a derived set at all", which every conjunct
//     after it presumes. In act mode plainDerivationIndexForAct has already
//     asked it, so it never fires from inside the licence — the gate is the
//     caller that needs it asked here.
//   - a full-engine compiled rule, without which neither closure nor the
//     parameter walk can be asked at all.
//   - a target that can read a row back (adapter.RowReader). Required twice
//     over: the Auditor's own enrolment needs it, and so does the zero-row
//     Delete probe the derived retraction class rests on.
//   - exactly one derivable seed anchor label — the audit's own conjunct,
//     carried here rather than left implicit in the auditor's enrolment. A
//     licence reached through an enrolled audit inherits it; the GATE has no
//     auditor to inherit it from, and a lens whose anchor pattern expands to
//     several concrete types has no single seed a per-anchor evaluation could
//     narrow to.
//   - no $now / $projectedAt. Both are reproduced differently by a per-anchor
//     evaluation than by the whole-corpus rescan it replaces. A non-exhaustive
//     walk is a REFUSAL, never a pass.
//   - per-anchor closure, full.CompiledRule.ProjectsOneRowPerAnchor. Asked LAST
//     because it is a fixed property of the query: reporting "not keyed by its
//     anchor alone" only for a lens that is otherwise fully eligible is the
//     reading an operator can act on.
func (p *Pipeline) plainDerivationStaticallyEligible(rs ruleState) (eligible bool, refusal string) {
	if reason := p.plainDerivationIndexRefusal(rs); reason != "" {
		return false, reason
	}
	fullCR, isFull := rs.cr.(*full.CompiledRule)
	if !isFull || fullCR == nil {
		return false, "its compiled rule is not a full-engine rule, so its closure and parameters cannot be derived"
	}
	if _, ok := p.currentAdapter().(adapter.RowReader); !ok {
		return false, "its target adapter cannot read a row back, which both the audit and the derived path's own presence probe require"
	}
	switch len(rs.seedAnchorLabels) {
	case 1:
	case 0:
		return false, "it has no single derivable anchor pattern to seed an evaluation from (multi-walk, unlabeled, or not full-engine)"
	default:
		return false, "its anchor pattern expands to several concrete types, which one seeded evaluation cannot narrow to"
	}
	for _, param := range []string{"now", "projectedAt"} {
		referenced, exhaustive := fullCR.ReferencesParam(param)
		if !exhaustive {
			return false, "its query shape could not be proven free of $" + param + ", which a per-anchor evaluation reproduces differently"
		}
		if referenced {
			return false, "it returns $" + param + ", which a per-anchor evaluation reproduces differently from the whole-corpus rescan it replaces"
		}
	}
	if !fullCR.ProjectsOneRowPerAnchor() {
		return false, "its rows do not partition by anchor (no key column both resolves from the anchor alone and identifies it), so a per-anchor evaluation would compute a truncated row"
	}
	return true, ""
}

// PlainDerivationStaticallyEligible reports whether this lens's shape,
// declaration and target admit the neighbour-anchor derivation — the licence's
// static prefix, asked of the pipeline's current rule snapshot.
//
// It is the activation gate's T1 test (secure-plain-lens-retraction-and-audit-
// design.md §4.4) and the corpus census's, both of which run outside this
// package. What it deliberately does NOT answer is whether the transport is
// currently ON: the licence's live conjuncts (the auth plane, an enrolled,
// unsuppressed and fresh auditor) are read per event, and PlainDerivationStatus
// above is where that answer lives.
func (p *Pipeline) PlainDerivationStaticallyEligible() (eligible bool, refusal string) {
	return p.plainDerivationStaticallyEligible(p.ruleState())
}

// plainDerivationIndexForAct is the gate `act` mode consults before letting a
// derived anchor set decide a plain lens's neighbour event. It carries both
// halves of the question: plainDerivationIndex answers "is there a derived set
// at all" (§4.2), plainDerivationLicence answers "may a derived set be acted
// on" (§5). Either refusing keeps today's unseeded whole-corpus rescan, so a
// lens is narrowed only where both hold.
//
// The licence is asked SECOND, and only once the index is ready, for two
// reasons: it is the dearer of the two — one auditor-status snapshot, a clock
// read, and two walks of the compiled rule (its parameters and its key columns)
// — and a lens whose index refuses has no derived set for a licence to speak
// about.
//
// `shadow` mode never reaches here: shadowPlainDerivation asks
// plainDerivationIndex directly, deliberately without the licence, because how
// far the derivation WOULD have narrowed on a lens acting refuses is exactly
// what the measurement is for.
//
// licenceRefusal is the licence's own reason, handed back rather than discarded
// so the caller's refusal note does not have to re-derive it: recomputing it
// would run a second auditor snapshot and a second pair of compiled-rule walks
// (ReferencesParam twice, ProjectsOneRowPerAnchor) on EVERY neighbour event of
// every lens without an enrolled auditor — which today is most of the corpus. It
// is empty in both of the cases where the licence has nothing to say: the index
// refused first, so the licence was never asked, and the lens was admitted.
func (p *Pipeline) plainDerivationIndexForAct(rs ruleState) (idx full.HopIndex, ready bool, licenceRefusal string) {
	idx, ready = p.plainDerivationIndex(rs)
	if !ready {
		return full.HopIndex{}, false, ""
	}
	licensed, refusal := p.plainDerivationLicence(rs)
	if !licensed {
		return full.HopIndex{}, false, refusal
	}
	return idx, true, ""
}

// deriveAnchorsForPlainVertex returns the anchor keys whose projection a
// mutation of (vertexType, vertexKey) can change, under the plain arm's own
// scan-root graph. ok == false means the derivation declined and the caller
// must fall back to today's unseeded evaluation. Mirrors
// deriveAnchorsForVertex (anchor_derivation.go) exactly, substituting
// plainDerivationIndex for derivationIndexes; the walk is reused
// unchanged (§4.1: one index type, two termini, zero duplicated consumers).
func (p *Pipeline) deriveAnchorsForPlainVertex(ctx context.Context, rs ruleState, vertexKey, vertexType string) ([]string, bool, error) {
	idx, ready := p.plainDerivationIndex(rs)
	if !ready {
		return nil, false, nil
	}
	_, id, parsed := substrate.ParseVertexKey(vertexKey)
	if !parsed {
		return nil, false, nil
	}
	var seeds []seededNode
	for _, pos := range idx.PositionsBinding(vertexType) {
		seeds = append(seeds, seededNode{pos: pos, id: id})
	}
	return p.walkOneIndex(ctx, idx, seeds)
}

// deriveAnchorsForPlainAspect is deriveAnchorsForPlainVertex seeded by the
// aspect's PARENT vertex — an aspect mutation changes what the parent's node
// properties render, and the pattern binds the parent, never the aspect key
// itself. Mirrors deriveAnchorsForAspect (anchor_derivation.go).
func (p *Pipeline) deriveAnchorsForPlainAspect(ctx context.Context, rs ruleState, aspectKey string) ([]string, bool, error) {
	parentVtx, parentType, _, _, ok := substrate.ParseAspectKey(aspectKey)
	if !ok {
		return nil, false, nil
	}
	return p.deriveAnchorsForPlainVertex(ctx, rs, parentVtx, parentType)
}

// deriveAnchorsForPlainLink returns the anchor keys a link create or
// tombstone can affect, seeded at the ANCHOR-SIDE endpoint of every pattern
// hop the link can bind (mirrors deriveAnchorsForLink, anchor_derivation.go).
// For the clinicProviders shape this is what collapses the neighbour
// endpoint's derivation into a duplicate of the anchor endpoint's own seed,
// with zero adjacency reads (§4.2's worked payoff trace).
//
// It takes a LINK KEY, and no live path holds one by the time derivation is
// asked. evalPlainLinkReprojection splits a link event into its two endpoint
// vertices and evaluates each through evaluatePlainFromVertex, so what reaches
// the derivation seam is already "evaluate from this one vertex" and is
// indistinguishable from a genuine vertex-root event —
// deriveAnchorsForPlainVertex serves every plain neighbour event, link endpoints
// included. This function is the whole-link entry point that answer costs two
// endpoint evaluations to reach: a caller that kept the link key could seed the
// anchor side once and skip the neighbour endpoint's redundant evaluation
// entirely (§4.2's own payoff trace), and it is exercised by the derivation's
// unit and differential tests against that shape.
func (p *Pipeline) deriveAnchorsForPlainLink(ctx context.Context, rs ruleState, linkKey string) ([]string, bool, error) {
	idx, ready := p.plainDerivationIndex(rs)
	if !ready {
		return nil, false, nil
	}
	srcType, srcID, rel, dstType, dstID, ok := substrate.ParseLinkKey(linkKey)
	if !ok {
		return nil, false, nil
	}
	var seeds []seededNode
	for _, s := range idx.AnchorSideSeeds(srcType, rel, dstType) {
		id := dstID
		if s.SrcIsAnchorSide {
			id = srcID
		}
		seeds = append(seeds, seededNode{pos: s.Pos, id: id})
	}
	return p.walkOneIndex(ctx, idx, seeds)
}

// evaluatePlainDerivedAnchors re-enters evaluatePlainFromVertex once per
// derived anchor — the same entry point the anchor-typed arms already use —
// and returns the combined, deduplicated result set. It is what `act` mode
// substitutes for the unseeded whole-corpus rescan on a licensed lens.
//
// THE ZERO-ROW DELETE PROBE (§6). Each re-entry reaches evaluateForEntryRaw's
// filter-retraction presence check (evaluate.go), which emits a Delete for an
// anchor whose re-derived row set no longer contains it. For a genuine
// anchor-typed event that is exactly right. For a WALK-derived anchor it is
// not: walkToAnchors models the pattern's hops, never its WHERE, so it can
// derive an anchor whose row the lens has never once projected — and an
// unconditional Delete for one is a spurious tombstone, durable under
// soft-delete. So every derived anchor's Delete is asked of the target first
// (derivedRowIsLive) and dropped when the row is CONFIRMED absent. Dropping on
// that answer is silent rather than an error: absence is the answer, not a
// failure.
//
// A probe the target could not ANSWER is the opposite disposition, and the
// difference is the event's second chance. This probe fires on a non-anchor-
// incident neighbour event — for the derived anchor it names, a one-shot event
// that will never recur — so a Delete dropped here has nothing behind it: the
// row becomes an orphan only the detect-only audit will ever name, and only if
// the audit is not itself blind to the same outage. So an unreadable probe ends
// the WHOLE event with its error, exactly as derive()'s own failed walk hands
// the event's outcome to the call it falls back to (evaluatePlainNeighbourEvent),
// and exactly as the upsert half of this same evaluation already behaves on a
// write failure. evaluateForEntryRaw propagates it (evaluate.go) and
// dispositionEvalErr Naks it (pipeline.go), so the event is redelivered rather
// than acked with its retraction silently missing.
//
// Discarding the results already collected from earlier anchors is correct, not
// a loss: redelivery re-runs the whole evaluation from scratch — every derived
// anchor's seeded evaluation again — and each of those is idempotent (an upsert,
// or the probed Delete). Returning a partial set would be the lossy option,
// because it would ack.
//
// Dedupe is hoisted here (§4.2 obligation i): today only the link arm dedupes
// across its two endpoint evaluations (evalPlainLinkReprojection's own
// dedupeKeyFor loop); with K derived anchors every arm now carries K anchors'
// results and needs the same treatment.
//
// Error disposition (§4.2 obligation ii): the FIRST error aborts the WHOLE
// event, matching the link arm's shipped behaviour — a widening for the
// vertex/aspect call sites, which previously ran exactly one evaluation and
// so had no "some derived anchors succeeded, one didn't" case to decide.
// Redelivery re-runs all K, which is idempotent: each of the K is itself a
// plain evaluation through the pipeline's normal write path (upsert, or the
// presence-check Delete, itself idempotent against an already-absent key).
//
// THE DECRYPT SEAM: this loop runs inside evaluateForEntryRaw's
// neighbour-event path, itself inside the outer evaluateForEntry wrapper that
// runs applySecureDecrypt on everything returned to it. So the invariant the
// re-entry must hold is that it NEVER decrypts on its own account — which is
// why it goes through evaluatePlainFromVertexRaw (pipeline.go) rather than
// evaluatePlainFromVertex: the outer wrapper is the single choke point, and a
// Secure Lens's declared columns are decrypted exactly once per derived
// anchor's row, by it. A re-entry that decrypted here too would hand that
// wrapper a decrypted string where a ciphertext envelope map is declared —
// Terminal, redacted to null (secure.go) — so the failure is silent data loss
// rather than an error, and TestSecureDecryptor_DecryptCallsPerEvaluation is
// what holds the wiring. It detects that failure by the stored plaintext and
// the zero-redaction assertion, never by the decrypt COUNT: swapping
// evaluatePlainFromVertexRaw back for evaluatePlainFromVertex re-introduces the
// double decrypt without moving the count at all — the inner call now decrypts
// and succeeds, and the outer wrapper's own call then fails secure.go's
// ciphertext-envelope type assertion before it would have incremented, so one
// success and one no-op failure land on the same total.
//
// THE REENTRANCY SEAM (§4.4). Every anchor in anchors is, by walkToAnchors'
// own construction, of the SAME anchorLabel type — so seedAnchorFor
// (pipeline.go) always finds a seed for the re-entrant evaluateForEntryRaw
// call evaluatePlainFromVertexRaw makes below. On a lens whose
// anchorLabel ALSO binds a second pattern position, that re-entry would
// itself qualify for the seeded-multi-position derivation
// (seedMultiPosition) — deriving the identical anchor set and recursing
// forever. plainDerivedAnchorReentry marks ctx for exactly the duration of
// this loop so evaluateForEntryRaw treats the re-entry as the narrow
// single-seed case it already is: this changes only how many times the
// question is asked, never the answer.
func (p *Pipeline) evaluatePlainDerivedAnchors(ctx context.Context, rs ruleState, anchors []string, anchorLabel string) ([]ruleengine.EvalResult, error) {
	ctx = context.WithValue(ctx, plainDerivedAnchorReentryKey{}, true)
	var combined []ruleengine.EvalResult
	seen := make(map[string]bool, len(anchors))
	for _, anchorKey := range anchors {
		results, err := p.evaluatePlainFromVertexRaw(ctx, rs, anchorKey, anchorLabel)
		if err != nil {
			return nil, err
		}
		for _, r := range results {
			if r.Delete {
				live, perr := p.derivedRowIsLive(ctx, r.Keys)
				if perr != nil {
					return nil, perr
				}
				if !live {
					continue
				}
			}
			id := dedupeKeyFor(r)
			if seen[id] {
				continue
			}
			seen[id] = true
			combined = append(combined, r)
		}
	}
	return combined, nil
}

// derivedRowIsLive reports whether the target presently holds the row keys
// names — the §6 presence probe a walk-derived anchor's Delete must pass
// before it is written.
//
// Only a positively confirmed live row earns the Delete, so the two answers
// that are FACTS ABOUT THE ROW — a confirmed absence, and an adapter that
// cannot be asked at all — return (false, nil) and the caller drops the Delete
// silently, mirroring zeroRowDeleteKey's own disposition (evaluate.go). The
// second is unreachable on a licensed lens (plainDerivationLicence requires
// adapter.RowReader, and this path runs only behind that licence) but is still
// answered fail-safe rather than asserted away.
//
// The FAILED READ is neither, and it is why this returns an error at all. It is
// not a fact about the row: the probe could not tell whether the retraction it
// is gating is a real one, and dropping it would ack an event that will never
// come back for this anchor (see evaluatePlainDerivedAnchors on why the
// zeroRowDeleteKey precedent stops here — that one fires on an evaluation the
// actor gets again). So it is returned for the caller to make the event's
// outcome, and it is ALSO logged and counted (PlainProbeUnreadable): the
// telemetry says a target had a bad minute, which no amount of redelivery makes
// visible on its own.
func (p *Pipeline) derivedRowIsLive(ctx context.Context, keys map[string]any) (bool, error) {
	reader, ok := p.currentAdapter().(adapter.RowReader)
	if !ok {
		return false, nil
	}
	_, present, err := reader.GetRow(ctx, keys)
	if err != nil {
		slog.Warn("pipeline: plain derived anchor: could not read the row back; failing the event rather than dropping its retraction",
			"ruleId", p.ruleID, "keys", keys, "err", err)
		p.recordPlainProbeUnreadable()
		return false, fmt.Errorf("plain derived anchor: presence probe could not read the row back: %w", err)
	}
	return present, nil
}

// recordPlainProbeUnreadable tallies a §6 presence probe the target could not
// answer, which fails its event rather than deciding a retraction.
func (p *Pipeline) recordPlainProbeUnreadable() {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.PlainProbeUnreadable++
	p.derivShadow.mu.Unlock()
}

// evaluatePlainNeighbourEvent decides how to answer a genuine neighbour
// event — the vertex / aspect-owner / link-endpoint is not the lens's own
// anchor type, so seedAnchorFor returned "". Its declined answer, at every
// exit below, is the unseeded whole-corpus re-scan: that IS today's shipped
// behaviour for a neighbour event, which never had a narrower option before
// Increment 1 existed at all. See plainDerivationDecide for the shared
// three-way mode switch + §4.2/§5 gate both this and
// evaluateSeededMultiPosition run through.
func (p *Pipeline) evaluatePlainNeighbourEvent(ctx context.Context, rs ruleState, entry ruleengine.NodeEntry) ([]ruleengine.EvalResult, error) {
	unseeded := func() ([]ruleengine.EvalResult, error) {
		return p.executeFullForActor(ctx, rs, entry.CoreKVKey, entry.Properties, "")
	}
	return p.plainDerivationDecide(ctx, rs, entry, unseeded)
}

// evaluateSeededMultiPosition decides how to answer a SEEDED event whose own
// type ALSO binds a second pattern position (seedMultiPosition, §4.4): a
// single engine-level seed can only ever narrow to the ANCHOR pattern
// position, so it silently misses every row where this vertex sits at the
// OTHER position. Its declined answer is the NARROW single-seed call — NOT
// the unseeded rescan evaluatePlainNeighbourEvent declines to — because
// that narrow call IS today's shipped answer for a seeded event (the very
// thing seedAnchorFor already computes); an operator running `off` mode, or
// any lens without a fresh licence, must pay exactly what it pays today,
// never a whole-corpus rescan it never asked for. Only `act` mode on a
// licensed lens substitutes the derived K-seeded answer, matching
// evaluatePlainNeighbourEvent's own act path exactly (same
// plainDerivationDecide call, different declined closure).
//
// On EVERY path here, entry.NodeLabel is the lens's own anchor label (that
// is what made it seed in the first place) — so evaluateForEntryRaw's outer
// filter-retraction check, which runs unconditionally after this dispatch
// returns, always finds an AnchorProjectionKey answer and may append its own
// Delete for entry's own key. That Delete is NOT run through §6's
// derivedRowIsLive probe (evaluatePlainDerivedAnchors' own zero-row-probe
// note) — it never has been, for any seeded-typed entry, on the narrow path
// this branch replaces just as much as on this one. Harmless in practice (a
// vertex genuinely at the far position has no row of its own to begin with,
// so the Delete lands on an already-absent or already-tombstoned key), but
// worth stating rather than leaving implicit.
func (p *Pipeline) evaluateSeededMultiPosition(ctx context.Context, rs ruleState, entry ruleengine.NodeEntry) ([]ruleengine.EvalResult, error) {
	narrowSeed := func() ([]ruleengine.EvalResult, error) {
		return p.executeFullForActor(ctx, rs, entry.CoreKVKey, entry.Properties, entry.CoreKVKey)
	}
	return p.plainDerivationDecide(ctx, rs, entry, narrowSeed)
}

// plainDerivationDecide is the three-way derivation-mode switch shared by
// evaluatePlainNeighbourEvent and evaluateSeededMultiPosition — the plain
// arm's own producer into the SAME off/shadow/act switch the actor-aware
// arm's affectedAnchors already uses (anchor_derivation_mode.go), and the
// SAME derivationShadow counters (a pipeline is one lens and is either
// plain or actor-anchored, never both, so the fields cannot collide between
// the two measurements).
//
// declined is the caller's OWN "today's shipped answer" — what runs
// unconditionally under `off`, is measured-but-not-decided-by under
// `shadow`, and is every `act`-path refusal's fallback. The two callers
// differ ONLY in what that answer is (a neighbour event's unseeded rescan,
// a seeded event's narrow single-seed call); every other decision — the
// mode switch itself, the §4.2/§5 gate, the derived-anchor cap, the
// K-seeded evaluation — is identical and shared here so the two cannot
// silently drift apart.
func (p *Pipeline) plainDerivationDecide(ctx context.Context, rs ruleState, entry ruleengine.NodeEntry,
	declined func() ([]ruleengine.EvalResult, error)) ([]ruleengine.EvalResult, error) {
	derive := func() ([]string, bool, error) {
		return p.deriveAnchorsForPlainVertex(ctx, rs, entry.CoreKVKey, entry.NodeLabel)
	}

	switch p.derivationMode() {
	case DerivationModeOff:
		return declined()
	case DerivationModeShadow:
		results, err := declined()
		if err != nil {
			return nil, err
		}
		p.shadowPlainDerivation(rs, derive)
		return results, nil
	case DerivationModeAct:
		// fall through to the act path below
	default:
		slog.Warn("pipeline: unknown anchor-derivation mode; using today's declined evaluation",
			"ruleId", p.ruleID, "mode", int(p.derivationMode()))
		return declined()
	}

	idx, ready, licenceRefusal := p.plainDerivationIndexForAct(rs)
	if !ready {
		// NOT counted as a fall-back, mirroring affectedAnchors' own static-
		// refusal treatment: this refusal is a property of the LENS, not of the
		// event, and counting it every event would drown the ratio the tally
		// exists to report — the per-event walk failures and cap overflows.
		p.noteStaticPlainDerivationRefusal(rs, licenceRefusal)
		return declined()
	}
	anchorLabel := idx.Labels[idx.Anchor]
	anchors, ok, err := derive()
	if err != nil {
		// A walk that errored says nothing about the event: adjacency is the
		// same store the declined evaluation is about to read, so the honest
		// response is to run it and let ITS error, if any, be the event's
		// outcome — mirroring affectedAnchors' own disposition.
		slog.Warn("pipeline: plain anchor derivation failed; falling back to today's declined evaluation",
			"ruleId", p.ruleID, "eventKey", entry.CoreKVKey, "err", err)
		p.recordDerivationFellBack(p.walkIsScoped(rs))
		return declined()
	}
	if !ok {
		p.recordDerivationFellBack(p.walkIsScoped(rs))
		return declined()
	}
	if cap := p.plainDerivedAnchorCap(); len(anchors) > cap {
		// A fallback, not a truncation (§4.2's caps, plural): the derived set
		// is real and correct, but K seeded evaluations this large cost more
		// than the declined answer they would replace.
		slog.Warn("pipeline: plain anchor derivation exceeded the derived-anchor cap; using today's declined evaluation",
			"ruleId", p.ruleID, "eventKey", entry.CoreKVKey, "derivedCount", len(anchors), "cap", cap)
		p.recordDerivationOverCap(len(anchors), p.walkIsScoped(rs))
		return declined()
	}
	p.recordDerivationActed(len(anchors), p.walkIsScoped(rs))
	return p.evaluatePlainDerivedAnchors(ctx, rs, anchors, anchorLabel)
}

// shadowPlainDerivation runs derive on a sampled fraction of events (the SAME
// 1-in-N sampler shadowAnchorDerivation uses, derivShadow.shouldSample) and
// records the derived-set SIZE — the measurement §11 of the design asks for —
// into the SAME derivationShadow counters the actor-aware arm's shadow uses.
// Unlike shadowAnchorDerivation there is no second, independently-computed
// set to diff against (a plain lens's shipped behaviour is a whole-corpus
// re-scan, not an enumerated anchor-key list), so only Sampled/DerivedAnchors
// and the Plain* fields below are meaningful for a plain pipeline; the
// actor-aware-only fields (Agreed, Narrowed*, Divergent*, BFSAnchors) simply
// stay at their zero value, which is safe because a pipeline is one lens and
// never reports both kinds of measurement.
//
// The three ways this can fail to answer are recorded under DISTINCT causes
// rather than one shared Declined, and the derived-set SIZE is recorded even
// when it is the reason for the refusal (PlainOverCapSize): folding "ready
// but too big" into a bare declined-with-no-size would make the derived-set
// -size distribution circular — truncated exactly at the cap it exists to
// justify, which is precisely the number §11 asks the fire to report.
//
// It never returns anything and never changes the event's outcome: a
// derivation failure here is an observation about the derivation, not about
// the event — the caller has already computed and will return the unseeded
// evaluation's results regardless.
func (p *Pipeline) shadowPlainDerivation(rs ruleState, derive func() ([]string, bool, error)) {
	if !p.derivShadow.shouldSample() {
		return
	}
	if _, ready := p.plainDerivationIndex(rs); !ready {
		p.recordPlainShadowNotReady()
		return
	}
	anchors, ok, err := derive()
	if err != nil {
		slog.Warn("pipeline: plain anchor-derivation shadow: walk failed",
			"ruleId", p.ruleID, "err", err)
		p.recordPlainShadowWalkDeclined()
		return
	}
	if !ok {
		p.recordPlainShadowWalkDeclined()
		return
	}
	if cap := p.plainDerivedAnchorCap(); len(anchors) > cap {
		// The derived set is real and correct — recorded in PlainOverCapSize,
		// never dropped — but K seeded evaluations this large would cost more
		// than the whole-corpus rescan they would replace, so it is still a
		// fallback (act mode would fall back here too; see
		// evaluatePlainNeighbourEvent's own cap check).
		slog.Warn("pipeline: plain anchor-derivation shadow: derived set exceeds the cap",
			"ruleId", p.ruleID, "derivedCount", len(anchors), "cap", cap)
		p.recordPlainShadowOverCap(len(anchors))
		return
	}
	p.recordPlainShadowAnswered(len(anchors))
}

// recordPlainShadowNotReady tallies a sampled event where plainDerivationIndex
// itself was not ready — a §4.2 conjunct refused before the walk ever ran.
func (p *Pipeline) recordPlainShadowNotReady() {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.Sampled++
	p.derivShadow.stats.PlainNotReady++
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logPlainSummaryIfDue(snapshot)
}

// recordPlainShadowWalkDeclined tallies a sampled event where the walk itself
// declined (ok == false, including DefaultDerivationReadCap exhaustion) or
// errored.
func (p *Pipeline) recordPlainShadowWalkDeclined() {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.Sampled++
	p.derivShadow.stats.PlainWalkDeclined++
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logPlainSummaryIfDue(snapshot)
}

// recordPlainShadowOverCap tallies a sampled event where the walk answered
// but the derived set exceeded DefaultPlainDerivedAnchorCap. derivedCount is
// added to PlainOverCapSize (never DerivedAnchors), so the two distributions
// — "sizes that fit under the cap" and "sizes that didn't" — stay separable.
func (p *Pipeline) recordPlainShadowOverCap(derivedCount int) {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.Sampled++
	p.derivShadow.stats.PlainOverCap++
	p.derivShadow.stats.PlainOverCapSize += int64(derivedCount)
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logPlainSummaryIfDue(snapshot)
}

// recordPlainShadowAnswered tallies a sampled event where the derivation
// answered within the cap, adding derivedCount to the shared DerivedAnchors
// total (DerivedAnchors / answered-count is the mean derived-set size §11's
// measurement asks for; the answered count is Sampled - PlainNotReady -
// PlainWalkDeclined - PlainOverCap).
func (p *Pipeline) recordPlainShadowAnswered(derivedCount int) {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.Sampled++
	p.derivShadow.stats.DerivedAnchors += int64(derivedCount)
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logPlainSummaryIfDue(snapshot)
}

// logPlainSummaryIfDue emits the running plain-lens tally at the same
// interval logSummaryIfDue uses, reporting only the fields meaningful for a
// plain pipeline rather than the actor-aware fields that stay zero for one.
func (p *Pipeline) logPlainSummaryIfDue(st DerivationShadowStats) {
	if st.Sampled == 0 || st.Sampled%derivationShadowSummaryEvery != 0 {
		return
	}
	slog.Info("pipeline: plain anchor-derivation shadow tally",
		"ruleId", p.ruleID,
		"sampled", st.Sampled, "notReady", st.PlainNotReady, "walkDeclined", st.PlainWalkDeclined,
		"overCap", st.PlainOverCap, "overCapSize", st.PlainOverCapSize, "derivedAnchors", st.DerivedAnchors)
}

// noteStaticPlainDerivationRefusal logs, at most once per distinct reason, why
// plainDerivationIndexForAct will not let this plain lens act — mirroring
// noteStaticDerivationRefusal (anchor_derivation_mode.go) and sharing its
// keyed-on-change latch (derivShadow.staticRefusal), safe for the same reason
// every other shared field is: a pipeline is one lens, never both.
//
// The gate's index half is re-asked here one conjunct at a time, in the gate's own
// order, so the reason names the conjunct that actually governs. The licence half is
// not re-asked: licenceRefusal is what plainDerivationIndexForAct already computed
// and handed back, and it is non-empty exactly when the licence is what refused.
//
// The licence's refusals sit in the SAME once-per-reason, uncounted bucket as the
// index's, even though its auditor-health conjuncts are live per-event facts rather
// than fixed properties of the lens. An audit that is stale or suppressed stays that
// way for hours, and counting every event under one through recordDerivationFellBack
// would drown the ratio that tally exists to report with a repeated lens-level fact —
// the same reason noteStaticDerivationRefusal's own doc gives. Keying the latch on the
// reason STRING rather than latching outright is what keeps this honest: the moment
// the governing conjunct changes, the new verdict is logged. It is also why every
// reason here is stable while its cause holds — see plainDerivationLicence, which
// keeps varying quantities out of its refusals for this reason.
//
// The audit's last verdict rides along as its own log field rather than inside the
// reason, so an operator reading a refusal can see how old the lens's audit is
// without that timestamp making every event's reason a new one.
func (p *Pipeline) noteStaticPlainDerivationRefusal(rs ruleState, licenceRefusal string) {
	var reason string
	switch {
	case p.actorEnumerator != nil || p.envelopeFn != nil || p.multiEnvelopeFn != nil:
		reason = "it is actor-aggregate or personal, so its anchor is the actor $actorKey names rather than a vertex the walk could seed from"
	case len(rs.branches) > 1:
		reason = "it is a multi-walk lens, and one scan-root graph cannot speak for N independent queries"
	case !rs.rootHops.Complete:
		reason = rs.rootHops.Incomplete
	case rs.rootHops.UnresolvedExpansionPosition() >= 0:
		reason = fmt.Sprintf("pattern position %d carries the `*` taxonomy-expansion sigil with no resolved concrete set — the walk would prune far ends it cannot confirm, which under-approximates",
			rs.rootHops.UnresolvedExpansionPosition())
	case rs.rootHops.AnchorIsExpanding():
		reason = "the anchor pattern position carries the `*` taxonomy-expansion sigil — a derived anchor's re-entry would be seeded by the AST label rather than its own resolved type, and would miss its seed and fall through to a whole-corpus rescan"
	case p.diffRetraction:
		reason = "it uses target-diff retraction, which would read a per-anchor row set as every OTHER anchor's rows being gone"
	case licenceRefusal != "":
		reason = licenceRefusal
	default:
		// Reachable only through the INDEX half, never the licence's. Every
		// plainDerivationLicence refusal returns a non-empty reason, so an empty
		// licenceRefusal on a gate that answered "not ready" means the index
		// refused first and the licence was never asked. The index conjuncts read
		// off the ruleState (branches, rootHops) cannot have moved — it is the
		// same value the gate was handed — so what moved is one of the ones read
		// live off the pipeline: its actor/envelope shape, or target-diff
		// retraction.
		reason = "an index conjunct it reads live — its actor or envelope shape, or target-diff retraction — moved between the act gate's answer and this one"
	}

	p.derivShadow.mu.Lock()
	repeat := p.derivShadow.staticRefusal == reason
	p.derivShadow.staticRefusal = reason
	p.derivShadow.mu.Unlock()
	if repeat {
		return
	}
	attrs := []any{"ruleId", p.ruleID, "reason", reason}
	if a := p.Auditor(); a != nil {
		if last := a.Status().LastPassAt; !last.IsZero() {
			attrs = append(attrs, "lastAuditVerdictAt", last)
		}
	}
	slog.Info("pipeline: plain anchor derivation cannot act on this lens; using today's unseeded evaluation", attrs...)
}
