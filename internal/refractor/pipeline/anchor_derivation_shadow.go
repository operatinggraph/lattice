package pipeline

import (
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
)

// Shadow mode for the affected-anchor derivation
// (auth-plane-projection-latency-design.md §16, the build order inside
// Increment 3). On a sampled fraction of actor-aware fan-out events the
// derivation runs alongside the ActorEnumerator BFS, its answer is compared
// with the BFS's, and the comparison is counted — while the BFS's answer is
// what the pipeline acts on, unchanged.
//
// The BFS this arm runs is the RELATION-BLIND walk, by construction:
// affectedAnchors' shadow branch calls enumerate(false), which forces the
// unscoped walk whatever the lens's own walkScope says (walkscope.go's
// pattern-scoped walk is what `act` and `off` run). That is not an accident of
// wiring but the measurement's premise — both counters below are defined
// against the WIDEST answer this pipeline can give, and a baseline that was
// itself narrowed would understate NarrowedAnchors and would fire
// DivergentEvents on anchors the scope pruned rather than on a real
// disagreement about reachability.
//
// The measurement §4.7 asks for is the ratio between the two sets, and the two
// directions of difference mean opposite things — which is the whole reason
// they are counted apart:
//
//   - NARROWED (the derivation returned FEWER anchors) is the increment's
//     entire point, not an alarm. Both sets are supersets of the truly-affected
//     anchors; a tighter superset is the win, and its anchor delta is the
//     latency saving being measured.
//   - DIVERGENT (the derivation returned an anchor the relation-blind BFS did
//     not) is the anomaly. That BFS is the trusted superset, so an anchor
//     outside it means the two disagree about reachability — logged loudly,
//     with the anchors, on every occurrence.
//
// Neither direction can prove the superset invariant itself: "derived ⊇ truly
// affected" is a claim about the projection, not about the BFS, and §9's
// differential test over pre/post recomputes is what settles it. What the
// shadow settles is whether the derivation behaves on the real graph at all,
// and by how much it would narrow if flipped.

// defaultDerivationShadowSampling runs the shadow on one event in N. The walk
// is cheaper than the BFS it is compared against, so the sampling is not there
// to protect the path so much as to keep the instrumentation's own adjacency
// reads a bounded fraction of it (§4.7).
const defaultDerivationShadowSampling = 8

// derivationShadowSummaryEvery emits the running tally to the log every N
// sampled events. Without it the shadow is unobservable in production: the
// per-event line is DEBUG (which no deployed component prints) and the counters
// live in memory behind an accessor nothing calls — so the measurement that
// justifies the shadow's cost would never reach an operator. The
// summary is INFO, per lens, and cheap at this interval.
const derivationShadowSummaryEvery = 50

// DerivationShadowStats is a snapshot of what the shadow has observed for one
// lens. Counts are per sampled event except where a field names anchors.
type DerivationShadowStats struct {
	Sampled  int64
	Declined int64 // the derivation refused — an incomplete index or a read cap
	Agreed   int64 // identical sets

	// NarrowedEvents counts events where the derivation returned a strict
	// subset of the BFS's answer, and NarrowedAnchors the anchors it spared.
	// This is the win being measured, not a defect.
	NarrowedEvents  int64
	NarrowedAnchors int64

	// DivergentEvents counts events where the derivation returned an anchor the
	// BFS did not — the two disagreeing about reachability, which is the
	// direction worth investigating.
	DivergentEvents  int64
	DivergentAnchors int64

	BFSAnchors     int64 // total anchors the BFS returned over sampled events
	DerivedAnchors int64 // total the derivation returned over the same events

	// The act-mode counters. They are a different measurement from the shadow's
	// and cannot be collected alongside it: acting means the BFS is never run,
	// so there is no second set to compare against. What is worth knowing once
	// the derivation decides is how often it answers at all — a lens that falls
	// back on every event carries the flip's cost and none of its benefit, and
	// only this ratio says so.
	//
	// Acted counts events reprojected against the derived set, ActedAnchors the
	// anchors those events reprojected, and FellBack the events that ran the
	// enumerator because a §17.2 conjunct, the hop index, or the read cap
	// refused.
	Acted        int64
	ActedAnchors int64
	FellBack     int64

	// LastOverCapSize is the size of the most recent derived set an ACTING
	// event declined for exceeding the derived-anchor cap, and zero until one
	// does. It is a last value rather than a sum because the question it
	// answers is a sizing one — how far past the cap this lens's
	// neighbourhoods actually reach — which a running total over an unknown
	// number of events cannot be read for. FellBack beside it says how often;
	// PlainOverCapSize below is the SHADOW arm's own distribution and is not
	// written here, so acting and measuring never share a number.
	LastOverCapSize int64

	// The plain arm's own shadow counters (plain-lens-neighbour-anchor-
	// derivation-design.md §11's measurement), populated by
	// shadowPlainDerivation (anchor_derivation_plain.go) instead of
	// shadowAnchorDerivation. A plain lens has no enumerated anchor-key list
	// to diff against, so Agreed/Narrowed*/Divergent*/BFSAnchors above stay
	// at their zero value for one — DerivedAnchors is shared (it is exactly
	// the derived-set-size total both arms want), but "declined" is split
	// into three causes rather than collapsed into one Declined, because an
	// operator sizing DefaultPlainDerivedAnchorCap needs to tell "the index
	// was never ready" apart from "the walk hit its own read cap" apart from
	// "the derived set was ready but too big" — and folding the last of
	// those into a plain Declined with no size recorded would make the
	// derived-set-size distribution circular: truncated exactly at the cap
	// it exists to justify.
	PlainNotReady     int64 // plainDerivationIndex was not ready (a §4.2 conjunct refused)
	PlainWalkDeclined int64 // the walk itself declined (ok == false) or errored — includes DefaultDerivationReadCap exhaustion
	PlainOverCap      int64 // the derived set was ready but exceeded DefaultPlainDerivedAnchorCap
	PlainOverCapSize  int64 // sum of derived-set sizes that triggered PlainOverCap — the tail §11's distribution needs

	// PlainProbeUnreadable counts §6 presence probes the target could not answer
	// (derivedRowIsLive's failed-read arm), each of which failed its whole event
	// rather than deciding a derived anchor's Delete either way. Unlike the four
	// above it is an ACT-mode counter, not a sampled one, and it is the only
	// standing record that the target could not be asked at all: the event is
	// redelivered and eventually succeeds, so without this a target's bad minute
	// would leave no trace in the lens's own numbers.
	PlainProbeUnreadable int64

	// RangedClosureReads is the adjacency documents the walk's bounded ranged
	// closures read, summed over every walk that ran one. It is an ACT-mode
	// counter like PlainProbeUnreadable, tallied once per walk however that
	// walk exits — the read-cap exit included.
	//
	// It is the one number that makes DefaultDerivationReadCap's firing rate
	// legible. A ranged hop is the only shape whose cost is not a handful of
	// reads per event: `(work)<-[:containedIn*0..]-(place)` walks DOWN a
	// containment tree, so its read count is the descendant set rather than an
	// ancestor chain. Read against FellBack, this says whether a lens is
	// falling back because its ranged closures are wide, or for some other
	// reason entirely.
	RangedClosureReads int64
}

type derivationShadow struct {
	mu       sync.Mutex
	events   atomic.Int64
	sampling atomic.Int64
	stats    DerivationShadowStats

	// staticRefusal is the last reason this lens was found unable to act, so
	// the reason is logged on change rather than on every event.
	// staticRefusalSet distinguishes "no reason has been reported yet" from
	// "the reported reason was the empty string": without it an empty reason
	// reads as a repeat of a report that never happened, and is swallowed.
	staticRefusal    string
	staticRefusalSet bool

	// personalLicensed latches the OTHER direction of the same transition: a
	// personal lens whose narrowing licence was granted. It lives here beside
	// staticRefusal, under the same mutex, because the two must move together —
	// a lens granted, revoked and granted again owes all three log lines, and a
	// grant that only cleared the refusal (or a refusal that only cleared the
	// grant) would swallow one of them.
	personalLicensed bool
}

// AnchorDerivationShadow returns this lens's shadow tally.
func (p *Pipeline) AnchorDerivationShadow() DerivationShadowStats {
	p.derivShadow.mu.Lock()
	defer p.derivShadow.mu.Unlock()
	return p.derivShadow.stats
}

// SetAnchorDerivationSampling overrides the 1-in-N shadow rate. n == 1 runs it
// on every event, which is what a test wants and what a live auth plane does
// not; a NEGATIVE n switches the shadow off entirely. n == 0 restores the
// default rather than disabling — zero is the atomic's own unset value, so it
// has to mean "unset", and a caller wanting off must say so with -1.
func (p *Pipeline) SetAnchorDerivationSampling(n int) {
	p.derivShadow.sampling.Store(int64(n))
}

func (s *derivationShadow) shouldSample() bool {
	n := s.sampling.Load()
	if n == 0 {
		n = defaultDerivationShadowSampling
	}
	if n < 0 {
		return false
	}
	return s.events.Add(1)%n == 0
}

// shadowAnchorDerivation runs derive on a sampled fraction of events and records
// how its answer compares with the BFS's, which the caller has already computed
// and will act on regardless. It never returns an error and never changes the
// event's outcome: a derivation failure is an observation about the derivation,
// not about the event.
func (p *Pipeline) shadowAnchorDerivation(rs ruleState, eventKey string, bfsAnchors []string, derive func() ([]string, bool, error)) {
	if p.actorEnumerator == nil || !p.derivShadow.shouldSample() {
		return
	}
	if _, ready := p.derivationIndexes(rs); !ready {
		p.recordShadowDeclined()
		return
	}
	derived, ok, err := derive()
	if err != nil {
		slog.Warn("pipeline: anchor-derivation shadow: walk failed",
			"ruleId", p.ruleID, "eventKey", eventKey, "err", err)
		p.recordShadowDeclined()
		return
	}
	if !ok {
		p.recordShadowDeclined()
		return
	}
	p.recordShadowComparison(eventKey, bfsAnchors, derived)
}

func (p *Pipeline) recordShadowDeclined() {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.Sampled++
	p.derivShadow.stats.Declined++
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logSummaryIfDue(snapshot)
}

func (p *Pipeline) recordShadowComparison(eventKey string, bfsAnchors, derived []string) {
	inDerived := make(map[string]struct{}, len(derived))
	for _, a := range derived {
		inDerived[a] = struct{}{}
	}
	inBFS := make(map[string]struct{}, len(bfsAnchors))
	for _, a := range bfsAnchors {
		inBFS[a] = struct{}{}
	}

	var narrowed, divergent []string
	for _, a := range bfsAnchors {
		if _, has := inDerived[a]; !has {
			narrowed = append(narrowed, a)
		}
	}
	for _, a := range derived {
		if _, has := inBFS[a]; !has {
			divergent = append(divergent, a)
		}
	}

	p.derivShadow.mu.Lock()
	st := &p.derivShadow.stats
	st.Sampled++
	st.BFSAnchors += int64(len(inBFS))
	st.DerivedAnchors += int64(len(inDerived))
	// The directions are tallied independently rather than as a switch: one
	// event can be narrower in one direction and wider in the other, and
	// collapsing that into a single verdict is what would hide it.
	if len(narrowed) > 0 {
		st.NarrowedEvents++
		st.NarrowedAnchors += int64(len(narrowed))
	}
	if len(divergent) > 0 {
		st.DivergentEvents++
		st.DivergentAnchors += int64(len(divergent))
	}
	if len(narrowed) == 0 && len(divergent) == 0 {
		st.Agreed++
	}
	snapshot := *st
	p.derivShadow.mu.Unlock()
	p.logSummaryIfDue(snapshot)

	if len(divergent) > 0 {
		// The relation-blind BFS is the trusted superset (this arm runs the
		// unscoped walk — see the package doc), so an anchor outside it means
		// the two disagree about reachability. Logged with the anchors, capped,
		// so the flip decision is made against evidence rather than a count.
		sort.Strings(divergent)
		slog.Warn("pipeline: anchor-derivation shadow: derived set holds anchors the enumerator did not reach",
			"ruleId", p.ruleID, "eventKey", eventKey,
			"bfsCount", len(inBFS), "derivedCount", len(inDerived),
			"divergent", cappedList(divergent, 10))
		return
	}
	slog.Debug("pipeline: anchor-derivation shadow",
		"ruleId", p.ruleID, "eventKey", eventKey,
		"bfsCount", len(inBFS), "derivedCount", len(inDerived), "narrowedBy", len(narrowed))
}

// logSummaryIfDue emits the running tally every derivationShadowSummaryEvery
// sampled events. It takes a snapshot taken under the lock rather than reading
// the live struct, so the line it prints is one coherent observation and the
// logging itself never happens with the lock held.
func (p *Pipeline) logSummaryIfDue(st DerivationShadowStats) {
	if st.Sampled == 0 || st.Sampled%derivationShadowSummaryEvery != 0 {
		return
	}
	slog.Info("pipeline: anchor-derivation shadow tally",
		"ruleId", p.ruleID,
		"sampled", st.Sampled, "declined", st.Declined, "agreed", st.Agreed,
		"narrowedEvents", st.NarrowedEvents, "narrowedAnchors", st.NarrowedAnchors,
		"divergentEvents", st.DivergentEvents, "divergentAnchors", st.DivergentAnchors,
		"bfsAnchors", st.BFSAnchors, "derivedAnchors", st.DerivedAnchors)
}

// recordDerivationActed and recordDerivationFellBack are the act-mode tally.
// The interval is against acting EVENTS rather than sampled ones: acting runs
// the derivation on every event, so there is no sampling to divide by, and a
// lens that falls back every time still reaches the interval and reports that
// it is doing so — which is precisely the case an operator needs told.
//
// walkScoped is this lens's ENUMERATOR posture, read from the same rule
// snapshot the event ran under — whether the fall-back walk is pattern-scoped
// or relation-blind. It rides both recorders rather than the fall-back one
// alone because the line is printed on whichever of them reaches the interval,
// and a lens's posture does not depend on which event that was.
func (p *Pipeline) recordDerivationActed(anchors int, walkScoped bool) {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.Acted++
	p.derivShadow.stats.ActedAnchors += int64(anchors)
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logActSummaryIfDue(snapshot, walkScoped)
}

func (p *Pipeline) recordDerivationFellBack(walkScoped bool) {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.FellBack++
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logActSummaryIfDue(snapshot, walkScoped)
}

// recordDerivationOverCap is recordDerivationFellBack for the one fall-back
// cause whose SIZE is worth carrying: a derived set that was ready and correct
// and simply too large. LastOverCapSize is the MOST RECENT over-cap event's
// size, not a running total keyed to FellBack — a lens whose fall-backs mix
// walk failures (recordDerivationFellBack, which never touches this field)
// with over-cap events publishes a FellBack that outcounts the one size beside
// it, and an operator reading the pair must read the size as "how big the last
// refused derived set was", never as "the size behind every count in
// FellBack".
func (p *Pipeline) recordDerivationOverCap(derivedCount int, walkScoped bool) {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.FellBack++
	p.derivShadow.stats.LastOverCapSize = int64(derivedCount)
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logActSummaryIfDue(snapshot, walkScoped)
}

// recordDerivationRangedReads adds one walk's ranged-closure adjacency reads to
// the act-mode tally. It does not touch Acted/FellBack — the walk's outcome is
// recorded by its own caller — so it does not move logActSummaryIfDue's
// interval either; the count is simply there when the next acted or fell-back
// event prints the line.
func (p *Pipeline) recordDerivationRangedReads(n int) {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.RangedClosureReads += int64(n)
	p.derivShadow.mu.Unlock()
}

func (p *Pipeline) logActSummaryIfDue(st DerivationShadowStats, walkScoped bool) {
	total := st.Acted + st.FellBack
	if total == 0 || total%derivationShadowSummaryEvery != 0 {
		return
	}
	attrs := []any{"ruleId", p.ruleID,
		"acted", st.Acted, "actedAnchors", st.ActedAnchors, "fellBack", st.FellBack}
	// Carried for every actor-aware lens and for no other, in BOTH its states:
	// on this arm `false` is the operator-relevant answer — the lens falls back
	// to a relation-blind walk — so suppressing it the way the counters above
	// are suppressed would hide exactly the lenses worth naming. A plain lens
	// holds no enumerator and no walk, so the attribute would mean nothing.
	if p.actorEnumerator != nil {
		attrs = append(attrs, "walkScoped", walkScoped)
	}
	// Carried only when it has fired, so the actor-aware arm's tally — which can
	// never reach the plain probe — is not padded with a permanent zero.
	if st.PlainProbeUnreadable > 0 {
		attrs = append(attrs, "probeUnreadable", st.PlainProbeUnreadable)
	}
	// Likewise carried only once it has fired, so a lens whose pattern holds no
	// ranged hop — the overwhelming majority — is not padded with a permanent
	// zero.
	if st.RangedClosureReads > 0 {
		attrs = append(attrs, "rangedClosureReads", st.RangedClosureReads)
	}
	// Same rule: a lens that has never exceeded the cap has no size to report,
	// and a permanent zero beside a non-zero fellBack would read as "the
	// fall-backs were over-cap ones of size nothing".
	if st.LastOverCapSize > 0 {
		attrs = append(attrs, "lastOverCapSize", st.LastOverCapSize)
	}
	slog.Info("pipeline: anchor-derivation tally", attrs...)
}

func cappedList(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
