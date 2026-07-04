package weaver

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/guardgrammar"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/weaver/planner"
)

// shadowDivergenceHistory bounds how many recent divergences a target's
// shadow-stats entry keeps (design weaver-planner-mandate-design.md §7
// resolved question 2: "a per-target Health doc carrying the last N
// divergences"). Small and fixed — this is an operator diagnostic, not an
// audit trail.
const shadowDivergenceHistory = 10

// shadowDivergence is one recorded disagreement between the planner's
// candidate pick and the frozen table's actual dispatch for a gap episode.
type shadowDivergence struct {
	GapColumn string `json:"gapColumn"`
	EntityID  string `json:"entityId"`
	PickedRef string `json:"pickedRef"`
	ActualRef string `json:"actualRef"`
	At        string `json:"at"`
}

// targetShadowStats is one target's cumulative shadow-comparison counters
// plus a bounded ring of its most recent divergences.
type targetShadowStats struct {
	Agree   int64              `json:"agree"`
	Diverge int64              `json:"diverge"`
	Recent  []shadowDivergence `json:"recentDivergences,omitempty"`
}

// shadowStats is the engine-wide Fire-4 shadow-comparison tracker: per-target
// agree/diverge counters plus each target's bounded divergence ring. Purely
// in-memory and diagnostic (design §7 resolved question 2: "NOT a lens/event
// stream — shadow is diagnostic, not business truth") — it never gates or
// alters dispatch, and a process restart resets it (a restart also resets the
// comparison population, so nothing is lost that matters).
type shadowStats struct {
	mu      sync.Mutex
	targets map[string]*targetShadowStats
}

func newShadowStats() *shadowStats {
	return &shadowStats{targets: make(map[string]*targetShadowStats)}
}

func (s *shadowStats) recordAgree(targetID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entryLocked(targetID).Agree++
}

func (s *shadowStats) recordDiverge(targetID string, d shadowDivergence) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.entryLocked(targetID)
	t.Diverge++
	t.Recent = append(t.Recent, d)
	if len(t.Recent) > shadowDivergenceHistory {
		t.Recent = t.Recent[len(t.Recent)-shadowDivergenceHistory:]
	}
}

func (s *shadowStats) entryLocked(targetID string) *targetShadowStats {
	t, ok := s.targets[targetID]
	if !ok {
		t = &targetShadowStats{}
		s.targets[targetID] = t
	}
	return t
}

// snapshot returns a per-target copy of the current counters/history, in
// deterministic targetId order, for the heartbeat to serialize.
func (s *shadowStats) snapshot() map[string]targetShadowStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]targetShadowStats, len(s.targets))
	for id, t := range s.targets {
		recent := make([]shadowDivergence, len(t.Recent))
		copy(recent, t.Recent)
		out[id] = targetShadowStats{Agree: t.Agree, Diverge: t.Diverge, Recent: recent}
	}
	return out
}

// shadowCompare is Fire 4's diagnostic-only planner comparison (Contract #10
// §10.8 Planner extension, design §7 resolved question 2). For a target in
// mode:"shadow" whose gap declares candidates, it independently ranks those
// candidates by the SAME criteria Fire 5 will dispatch under — precondition
// satisfaction, then windowed close-rate, then declared cost, then
// lexicographic actionRef — and compares the pick to the actionRef the
// frozen table just dispatched (ga.Action; empty when the gap carries no
// explicit action). The result is recorded (agree/diverge counters + a
// bounded divergence log) and NEVER changes what was dispatched — table
// dispatch is computed and fired entirely independently of this call.
//
// A no-op when the target isn't in shadow mode or the gap declares no
// candidates (goal-only shadow comparison needs the effects catalog Fire 6
// introduces; not run here — see the design checkpoint).
//
// Called once per lane-1 delivery for every open gap the caller did not skip
// as suppressed — the SAME cadence dispatchGap itself runs at, which is
// coarser than "once per episode" whenever a gap's lens omits inflight_<g>/
// maxretries_<g> (so a still-in-flight gap keeps calling dispatchGap on every
// row delivery even though the real table's anti-storm mark drops the
// redundant fire). The counters below over-count proportionally in that case
// rather than staying exactly-once — acceptable because the ranking is
// deterministic per (row, candidates) so a repeated call reproduces the same
// judgment, and the design pins this as diagnostic bookkeeping, not business
// truth (§7 resolved question 2).
func (e *Engine) shadowCompare(ctx context.Context, target *Target, targetID, entityID, gapColumn string, ga GapAction, row map[string]any) {
	if target.Mode != targetModeShadow || len(ga.Candidates) == 0 {
		return
	}
	picked, ok := e.rankCandidates(ctx, targetID, gapColumn, ga.Candidates, row)
	if !ok {
		return
	}
	if picked == ga.Action {
		e.shadow.recordAgree(targetID)
		return
	}
	e.shadow.recordDiverge(targetID, shadowDivergence{
		GapColumn: gapColumn,
		EntityID:  entityID,
		PickedRef: picked,
		ActualRef: ga.Action,
		At:        substrate.FormatTimestamp(time.Now()),
	})
}

// candidateRank is one candidate's ranking key (rankCandidates' sort input):
// eligible descending (true first), closeRate descending, cost ascending,
// actionRef lexicographic — the exact tuple design §3.3 specifies for Fire
// 5's dispatch-time selection, reused here read-only for comparison.
type candidateRank struct {
	ref       string
	eligible  bool
	hasRate   bool
	closeRate float64
	cost      int
}

func lessCandidateRank(a, b candidateRank) bool {
	if a.eligible != b.eligible {
		return a.eligible // eligible sorts first
	}
	// A candidate with no dispatch history yet is neither favored nor
	// penalized against one with data on this term — it ties and falls
	// through to cost/lexicographic (a documented Fire-4 simplification;
	// Fire 5's brief may refine how a no-data candidate ranks against a
	// scored one).
	if a.hasRate && b.hasRate && a.closeRate != b.closeRate {
		return a.closeRate > b.closeRate
	}
	if a.cost != b.cost {
		return a.cost < b.cost
	}
	return a.ref < b.ref
}

// rankCandidates picks ONE candidate deterministically from candidates
// (design §3.3): preconditions evaluate against row (each column addressed
// as subject.data.<column>, the row-is-State convention internal/weaver/planner
// documents), then windowed close-rate (from the §10.3 `__effect` bookkeeping
// Fire 2 built), then declared cost, then lexicographic actionRef. ok=false
// means no candidate is currently eligible (every Pre evaluated false) — the
// planner has no pick to compare this episode, same as Fire 5's "no eligible
// candidate" outcome will be.
func (e *Engine) rankCandidates(ctx context.Context, targetID, gapColumn string, candidates []GapCandidate, row map[string]any) (string, bool) {
	state := rowState(row)
	ranks := make([]candidateRank, 0, len(candidates))
	for _, c := range candidates {
		eligible := c.preGuard == nil || planner.EvalGuard(c.preGuard, state)
		if !eligible {
			continue
		}
		rate, _, hasRate, err := e.marks.effectCloseRate(ctx, targetID, gapColumn, c.Action)
		if err != nil {
			e.logger.Warn("weaver: shadow-compare close-rate read failed; ranking without it",
				"targetId", targetID, "gap", gapColumn, "action", c.Action, "err", err)
			hasRate = false
		}
		ranks = append(ranks, candidateRank{ref: c.Action, eligible: true, hasRate: hasRate, closeRate: rate, cost: c.Cost})
	}
	if len(ranks) == 0 {
		return "", false
	}
	sort.Slice(ranks, func(i, j int) bool { return lessCandidateRank(ranks[i], ranks[j]) })
	return ranks[0].ref, true
}

// rowState maps a §10.2 lens row onto a planner.State: each column addresses
// as a root path (subject.data.<column>) — the convention
// internal/weaver/planner.State documents ("stands in for whatever a real
// dispatch would read — a lens row today").
func rowState(row map[string]any) planner.State {
	s := make(planner.State, len(row))
	for col, v := range row {
		s[guardgrammar.Path{Field: col}] = v
	}
	return s
}
