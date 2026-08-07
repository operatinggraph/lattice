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
// The measurement §4.7 asks for is the ratio between the two sets, and the two
// directions of difference mean opposite things — which is the whole reason
// they are counted apart:
//
//   - NARROWED (the derivation returned FEWER anchors) is the increment's
//     entire point, not an alarm. Both sets are supersets of the truly-affected
//     anchors; a tighter superset is the win, and its anchor delta is the
//     latency saving being measured.
//   - DIVERGENT (the derivation returned an anchor the BFS did not) is the
//     anomaly. The BFS is the trusted superset, so an anchor outside it means
//     the two disagree about reachability — logged loudly, with the anchors, on
//     every occurrence.
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
// live in memory behind an accessor nothing calls — so the measurement that is
// this increment's whole justification would never reach an operator. The
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
}

type derivationShadow struct {
	mu       sync.Mutex
	events   atomic.Int64
	sampling atomic.Int64
	stats    DerivationShadowStats
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
	if _, ready := p.derivationIndex(rs); !ready {
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
		// The BFS is the trusted superset, so an anchor outside it means the
		// two disagree about reachability. Logged with the anchors, capped, so
		// the flip decision is made against evidence rather than a count.
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

func cappedList(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
