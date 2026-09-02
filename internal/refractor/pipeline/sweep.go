package pipeline

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// DefaultSweepInterval and DefaultSweepBatch bound the convergence sweep
// (capability-projection-reconciliation-design.md §3.2). "Auth-plane" describes
// the only lens family enrolled today, NOT the condition for enrolment: that is
// sweepEnrolment (driver.go), three structural conjuncts — the output key
// pattern must yield a prefix, it must round-trip through AnchorFromKey so the
// orphan direction can claim a row, and the target adapter must be able to
// enumerate keys under that prefix. Reading the old wording as a policy is what
// makes a Secure Lens look deliberately excluded when it simply never reaches
// InstallActorAggregate. The deep pass
// re-executes at most DefaultSweepBatch anchors per tick, so a 10k-actor cell
// fully re-verifies in roughly seven hours while costing one bounded batch of
// cypher evaluations a minute. Both are deployment-overridable, like the
// capability-lag threshold.
const (
	DefaultSweepInterval = 60 * time.Second
	DefaultSweepBatch    = 25
)

// SweepPlan is the per-lens data the convergence sweep needs, supplied by the
// projection driver from the compiled Output descriptor. Installing a plan is
// what opts a pipeline into sweeping: the driver installs one only for an
// auth-plane actor-aggregate lens, so a personal, plain, convergence, or
// operation-aggregate lens is excluded structurally rather than by a name list.
type SweepPlan struct {
	// AnchorType is the actor vertex type whose Core KV population defines
	// the lens's expected coverage.
	AnchorType string
	// AnchorFromKey recovers the anchor a target key was built for, reporting
	// false for a key this lens does not own (OutputDescriptor.AnchorFromKey).
	AnchorFromKey func(targetKey string) (string, bool)
	// KeyPrefix scopes the target listing to this lens's own rows
	// (OutputDescriptor.KeyPrefix). It is what makes a sweep affordable on a
	// bucket several lenses share, and the driver refuses to install a plan
	// without one — an unscoped listing costs the whole bucket per lens per
	// tick, and the fact that a lens cannot name its own keys is itself the
	// reason not to sweep it.
	KeyPrefix string
	// Interval and Batch override the sweep defaults; zero selects them.
	Interval time.Duration
	Batch    int
}

// SweepStatus is the sweeper's snapshot for the heartbeat and for cursor
// persistence. Reconciled is cumulative across the lens's lifetime (restored
// from Health KV at startup); DivergentStreak counts consecutive sweep passes
// that healed at least one divergence, so the heartbeat can escalate a
// recurring divergence from warning to error without holding debounce state of
// its own.
//
// The repair fields are the sweep's second, independent verdict, and the worse
// of the two: a divergence the sweep HEALED leaves a correct row behind, while
// one it could not write leaves the row wrong. They are separate because a
// failed repair heals nothing — folding it into the heal count would clear
// DivergentStreak and publish a converged lens over a projection that is still
// broken, the more thoroughly broken the healthier it reads.
type SweepStatus struct {
	Reconciled      uint64
	DivergentStreak int
	Cursor          string
	// LastPassAt is when a pass last reached a verdict — a completed tick or a
	// pass-level fault, never a suppressed one. It is the sweep's liveness
	// clock: every counter below describes the last pass that ran, so a sweep
	// that stops running keeps publishing its final verdict forever, and only
	// this timestamp says the verdict is old. Zero until the first verdict.
	LastPassAt time.Time
	// Suppression names why the most recent tick was skipped without verifying
	// anything ("" when the last tick ran), and SuppressionAt when that was
	// recorded. A suppressed tick is legitimate — a rebuild supersedes the sweep,
	// an operator pause outranks it — but it is indefinite: whatever holds the
	// condition holds the sweep, so the reason travels to the heartbeat rather
	// than being logged and dropped.
	//
	// The timestamp is what makes the reason trustworthy. It describes the last
	// tick, so a tick that never returns (a read wedged inside the suppression
	// check itself) leaves the previous tick's reason standing, and a reader
	// with no clock would take a wedged sweep for a merely-suppressed one. A
	// consumer must treat a reason older than a couple of intervals as absent.
	Suppression   string
	SuppressionAt time.Time
	// FailingActors is how many anchors currently carry an unrepaired
	// reprojection failure, and FailedStreak how many consecutive passes ended
	// with at least one failure (per-actor, or a pass-level fault that stopped
	// the tick before it could verify anything). LastFailure is the governing
	// error's text, so the heartbeat issue names a cause and not just a count.
	FailingActors int
	FailedStreak  int
	LastFailure   string
	// Unverified is how many anchors in the last pass reached no verdict at
	// all, UnverifiedStreak how many consecutive passes carried at least one,
	// and LastUnverified the governing reason.
	//
	// It is the third outcome the heal/fail pair cannot express. A pass that
	// examines an anchor and can conclude nothing about it writes nothing —
	// and a signal derived from writes reads that as convergence: no heal
	// counted, streak reset, green. Counting it separately is what keeps an
	// anchor whose divergence has no repair transport from making the lens
	// read HEALTHIER the more thoroughly it is broken.
	Unverified       int
	UnverifiedStreak int
	LastUnverified   string
	// Blocked is how many anchors in the last pass held a divergence the
	// ordering guard refused to let the sweep repair (Contract #6 §6.2 —
	// stored watermark >= the reconciliation's token), BlockedStreak how many
	// consecutive passes carried at least one, and LastBlocked the governing
	// reason.
	//
	// This is the worst of the four: unlike an unverified anchor the sweep
	// knows exactly what is wrong, and unlike a failing one there is no error
	// to retry — the write returned success having changed nothing. A row here
	// stays wrong until a real CDC event above that watermark reprojects it.
	Blocked       int
	BlockedStreak int
	LastBlocked   string
	// BlockedByClass counts the blocked set by the class of condition the guard
	// refused to let the sweep repair, and WorstBlockedClass names the most
	// severe class present ("" when nothing is blocked). LastBlocked is the
	// governing reason from that same worst class, so the text and the class
	// never name different conditions.
	//
	// Both are DERIVED from the standing set at publish time rather than
	// accumulated as passes run, so a class count can never disagree with the
	// Blocked total it is part of. The map is rebuilt on every publish and never
	// mutated afterwards, so a snapshot taken by an earlier Status() call keeps
	// describing the pass it was taken from.
	//
	// BlockedStreak counts passes carrying at least one blocked row of ANY
	// class: it is the standing set's liveness, not a per-class ladder, and the
	// class is what decides how loudly each of those passes is reported.
	BlockedByClass    map[BlockedClass]int
	WorstBlockedClass string
}

// blockedVerdict is one anchor's standing blocked state: the class of condition
// the ordering guard refused to let the sweep repair, and the reason text that
// names it. The two are stored together because they are one verdict — a reason
// carried apart from its class is a text an aggregator can pick independently of
// the class it belongs to, which is how a content divergence ends up reported
// under a provenance heading.
type blockedVerdict struct {
	class  BlockedClass
	reason string
}

// actorFailure is one anchor's unrepaired-reprojection state. It lives from the
// failure that created it until a reprojection of that actor succeeds —
// deliberately spanning the passes backoff skips, so suppressing the retry WORK
// never suppresses the health SIGNAL.
type actorFailure struct {
	consecutive int
	// retryAfter is the last pass number this actor sits out; the sweep skips
	// it while passNo <= retryAfter.
	retryAfter uint64
	err        string
}

// Sweeper is one auth-plane lens's periodic self-audit: it detects graph ↔
// projection divergence and repairs it through the same per-actor Reproject
// path the control verb uses, so reconciliation has exactly one write path and
// one ordering token.
//
// Two detectors compose. The coverage prefilter is two key listings — the
// lens's anchor-type vertices from Core KV and the target's live keys — and
// points at both directions: an anchor with no target key (the observed
// first-projection loss) and a target key whose anchor is gone (an over-grant).
// The round-robin deep verify then walks all anchors a bounded batch at a time,
// re-executing the projection; that is what catches a row which is present but
// stale — the over-grant direction the prefilter cannot see, since a revoked
// actor keeps both its vertex and its key.
//
// Neither prefilter direction is a decisive divergence, so both are PRIORITY
// HINTS whose share of the batch is earned from whether their candidates
// actually heal anything (see candidates and hintState), never an assumption
// about the lens:
//
//   - An anchor with no row is definite only for a lens that projects a row per
//     anchor. For a lens whose match filters — the common shape, and true on the
//     auth plane itself, where an identity holding no unexpired task grant
//     correctly has no capabilityEphemeral row and one holding no role correctly
//     has no capabilityRoles row — it is the steady state.
//   - A target key with no live anchor is not proof of a row to retract either.
//     Core KV deletes are logical, so a departed identity KEEPS its vertex key
//     and stays in the anchor listing; its stale row is therefore not an orphan,
//     and the deep verify is what detects that over-grant. This direction's real
//     triggers are a physically purged anchor, a row written for an anchor that
//     never existed, and a transiently short anchor listing — and because a
//     guarded retraction leaves a soft tombstone that stays listed, a key it has
//     already retracted keeps presenting as an orphan.
//
// A converged pass writes nothing: Reproject's skip-if-identical drops the
// write when the recomputed body equals the stored one, so a healthy bucket
// costs reads only.
type Sweeper struct {
	p    *Pipeline
	plan SweepPlan

	interval time.Duration
	batch    int

	mu     sync.Mutex
	status SweepStatus
	// failing is the per-actor unrepaired-failure set, and passNo the monotonic
	// tick counter the per-actor backoff schedules against.
	failing map[string]*actorFailure
	// blocked and unverified are the standing sets of anchors whose last
	// verdict was VerdictBlocked / VerdictUnverified, keyed by actor. An
	// unverified entry holds its governing reason; a blocked one holds the
	// class of condition the guard declined together with that class's reason.
	//
	// They are SETS, not per-pass counters, for the same reason `failing` is:
	// a pass examines at most `batch` anchors chosen by cursor round-robin, so
	// a per-pass tally reports whichever verdicts this batch happened to
	// contain and reads zero on every pass that did not re-examine them. On a
	// corpus larger than one batch that makes the streak — and therefore the
	// escalation to error — structurally unreachable, and publishes a clean
	// verdict over a row the sweep has proven it cannot repair for all but one
	// pass in every rotation. An entry lives from the verdict that created it
	// until a later verdict on the SAME anchor supersedes it, or the anchor
	// leaves the corpus.
	blocked    map[string]blockedVerdict
	unverified map[string]string
	passNo     uint64
	// coverage and orphan are the two prefilter hints' rotation + earned-share
	// state (see hintState and candidates).
	coverage hintState
	orphan   hintState

	// surveyValid, surveyAnchors, surveyTargets, surveyAppliedSeq and
	// surveyWrites are the cached result of the last real survey() call and
	// the pipeline's two invalidation signals as of that call — see
	// surveyCached. surveyValid is false until the first real survey runs, so
	// a brand-new Pipeline (lastAppliedSeq and ProjectionWrites both zero)
	// can never read as a cache hit against a Sweeper that has never actually
	// surveyed.
	surveyValid      bool
	surveyAnchors    []string
	surveyTargets    map[string]struct{}
	surveyAppliedSeq uint64
	surveyWrites     uint64

	// idleEligible is sticky across ticks: true once a round-robin deep-verify
	// lap (cycle) completes having healed nothing and found no divergence or
	// failure, cleared the instant either survey-cache signal moves — even
	// mid-lap — or a later lap finds work. idleSkipped counts consecutive
	// ticks the idle back-off has skipped since idleEligible last went true or
	// a skip last gave way to a real tick. See idleTick and updateIdleCycle.
	idleEligible bool
	idleSkipped  int
	cycle        idleCycle
}

// idleCycle accumulates the round-robin deep verify's current lap: visited
// counts how many anchors direction 3 of candidates has examined since the
// lap began (a lap spans as many ticks as the population needs), and dirty
// latches true the moment any tick within it heals something, finds a
// Blocked/Unverified verdict, or leaves an actor failing. dirty is latched,
// never cleared mid-lap — one divergent or failing tick condemns the whole
// lap, because the point of the back-off is a lap that PROVED nothing needs
// attention, not one that merely ended on a clean tick.
type idleCycle struct {
	visited int
	dirty   bool
}

// hintState is one prefilter hint's rotation position and its standing record
// of whether its premise holds for this lens.
//
// Both hints propose candidates from a set that can legitimately stay populated
// forever, so neither may spend every tick on the same head of its list:
//
//   - The coverage hint's set is the row-less anchors. For a lens whose match
//     filters, that set is the steady state.
//   - The orphan hint's set is the target keys with no live anchor. An auth-plane
//     target is always guarded, so retracting a row writes a SOFT tombstone that
//     stays a live NATS-KV key and keeps appearing in the target listing while
//     reading as absent. A retracted orphan therefore does NOT leave the set — it
//     becomes a permanent, zero-value occupant of the hint's budget.
//
// cursor rotates the walk across ticks so every member of the set is reached in
// bounded time. Neither cursor is persisted: the deep verify's cursor is the
// lens's completeness guarantee, while these only order hints. A restart
// re-walks from the head, so on a cell that redeploys faster than a full
// rotation the rotation never completes and the hint degrades to its head —
// bounded and never wrong, but the benefit is restart-scoped.
//
// misses counts consecutive passes in which the hint selected candidates and
// none of them wrote. Reaching hintMissesBeforeFloor caps the hint at the same
// reserved floor the other directions hold, so it stops spending the batch on
// re-projections that heal nothing; one write clears it outright.
type hintState struct {
	cursor string
	misses int
}

// hintMissesBeforeFloor is how many consecutive unproductive passes demote a
// hint to its floor. It is two rather than one because a single pass can be
// unproductive for reasons that say nothing about the lens: either key listing
// can come back short, and a row that lands between the target listing and the
// reprojection reads as missing and then as correct.
//
// hintRetestPasses periodically restores the full share regardless of the
// standing record, so a verdict formed from a transient cannot hold for the life
// of the process — on a converged lens the hint selects nothing, gathers no
// evidence, and would otherwise never get the chance to clear itself.
const (
	hintMissesBeforeFloor = 2
	hintRetestPasses      = 8
)

// IdleSweepBackoffEvery is how many ticks separate one deep verify from the
// next once a full round-robin lap of it has healed nothing and found no
// divergence or failure: the other IdleSweepBackoffEvery-1 ticks are skipped
// exactly like a suppressed one (see idleTick). Any movement in either
// survey-cache signal ends the back-off on the very next tick, so this only
// ever holds for a lens that has proven, on its own last full lap, that
// nothing needs closer attention.
//
// Exported, and pinned deliberately small relative to the sweep-stall alert
// window (health.DefaultCapabilitySweepStallCycles,
// internal/refractor/health/lattice_heartbeater.go): a skipped idle tick
// never advances the sweep's liveness clock (SweepStatus.LastPassAt) — only a
// REAL pass does — so once back-off engages, real passes recur every
// IdleSweepBackoffEvery ticks, and that recurrence must land well inside the
// stall window or an idle, perfectly healthy lens ages toward its own
// CapabilitySweepStalled/LensSweepStalled alert. At the 60s auth-plane
// interval, 5 means a real pass every 5 minutes against a 10-minute stall
// window (at the defaults); at the 5-minute BusinessSweepInterval it is 25
// minutes against 50. health_test's
// TestIdleSweepBackoffEvery_StaysInsideTheSweepStallWindow pins
// IdleSweepBackoffEvery*2 <= DefaultCapabilitySweepStallCycles so the two
// cannot drift back into collision unnoticed.
const IdleSweepBackoffEvery = 5

// surveyForceEvery bounds how long the survey cache (surveyCached) may go
// un-refreshed: insurance against a target mutation neither invalidation
// signal saw — see projectionWrites' doc for what "this pipeline's own
// writes" covers, and what it does not.
const surveyForceEvery = 30

// idleSuppressionReason is the sweep-status suppression text an idle-skipped
// tick publishes, distinct from every other suppression cause (rebuild in
// flight, paused, unreadable) so an operator — or a log grep — can tell a
// lens that is quiet because it is converged from one that is stalled.
const idleSuppressionReason = "idle: no change since last verified cycle"

// coverageExamineMultiple bounds how many anchors the coverage direction may
// INSPECT per tick, as a multiple of how many it may select. A row-less
// tombstoned anchor costs a liveness read and yields no slot, so a selection cap
// alone does not bound the reads — see the walk in candidates. Four keeps a
// converged lens's cost near the batch it was sized for while leaving room to
// step over a run of tombstones and still fill the batch with real divergences
// in the same tick.
const coverageExamineMultiple = 4

// floored reports whether this hint currently keeps only its reserved floor.
func (h hintState) floored(passNo uint64) bool {
	if h.misses < hintMissesBeforeFloor {
		return false
	}
	return passNo%hintRetestPasses != 0
}

// newSweeper builds the sweeper for an installed plan, applying the defaults.
func newSweeper(p *Pipeline, plan SweepPlan) *Sweeper {
	iv := plan.Interval
	if iv <= 0 {
		iv = DefaultSweepInterval
	}
	batch := plan.Batch
	if batch <= 0 {
		batch = DefaultSweepBatch
	}
	return &Sweeper{
		p:          p,
		plan:       plan,
		interval:   iv,
		batch:      batch,
		failing:    map[string]*actorFailure{},
		blocked:    map[string]blockedVerdict{},
		unverified: map[string]string{},
	}
}

// Status returns the current sweep snapshot. Thread-safe; read by the
// heartbeat's capability-lens provider every beat.
func (s *Sweeper) Status() SweepStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// SetSweepPlan installs the convergence-sweep plan for this pipeline
// (capability-projection-reconciliation-design.md §3.2). Called by the
// projection driver for an auth-plane actor-aggregate lens only. Must be called
// before RunSweep.
func (p *Pipeline) SetSweepPlan(plan SweepPlan) {
	p.sweeper = newSweeper(p, plan)
}

// Sweeper returns this pipeline's convergence sweeper, or nil when no plan is
// installed. That is not "every non-auth-plane lens": since the enrolment gate
// landed, any actor-aggregate lens that can scope a key listing to its own
// rows, round-trip its own keys, and whose adapter enumerates under a prefix
// gets a plan — business lenses included. What has no sweeper is a lens that
// fails one of those conjuncts, or is not actor-aggregate at all.
func (p *Pipeline) Sweeper() *Sweeper { return p.sweeper }

// RunSweep runs the convergence sweep until ctx is cancelled. It returns
// immediately for a pipeline with no sweep plan, so the caller can start it
// unconditionally beside Run.
//
// The cursor and the cumulative reconciled count are restored from the lens's
// existing Health KV entry before the first tick, so a restart resumes the walk
// rather than restarting it — no new bucket, no new stream.
func (p *Pipeline) RunSweep(ctx context.Context) {
	s := p.sweeper
	if s == nil {
		return
	}
	s.restore(ctx)

	// Every lens's sweeper starts inside the same activation loop, so without a
	// stagger they all tick together: one instant per interval in which the
	// whole lens set enumerates its anchors and reprojects a batch each, and an
	// otherwise idle interval around it. The offset is derived from the lens ID
	// rather than drawn randomly so a given lens keeps its slot across restarts
	// and a test can predict it.
	select {
	case <-ctx.Done():
		return
	case <-time.After(s.startOffset()):
	}

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pass(ctx)
		}
	}
}

// startOffset spreads this lens's ticks across the interval, in [0, interval).
func (s *Sweeper) startOffset() time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s.p.ruleID))
	return time.Duration(h.Sum64() % uint64(s.interval))
}

// restore seeds the in-memory status from Health KV so a restarted process
// resumes its round-robin position and keeps its cumulative heal count.
func (s *Sweeper) restore(ctx context.Context) {
	if s.p.reporter == nil {
		return
	}
	entry, err := s.p.reporter.GetStatus(ctx)
	if err != nil {
		slog.Warn("pipeline: sweep: could not restore cursor; walking from the start",
			"ruleId", s.p.ruleID, "err", err)
		return
	}
	s.mu.Lock()
	s.status.Cursor = entry.SweepCursor
	s.status.Reconciled = entry.SweepReconciled
	s.mu.Unlock()
}

// suppressed reports why this tick must be skipped, or "" to run it. A rebuild
// is a superset of the sweep (truncate + full rescan), and a paused pipeline is
// operator intent that reconciliation must not quietly override.
//
// The reason is returned rather than a bare bool because every suppression cause
// is held by something external to the sweep — a rebuild that never finishes, a
// pause nobody lifts, a health entry that stays unreadable — so "skipped" is not
// a transient state the next tick clears, and the operator needs the cause.
func (s *Sweeper) suppressed(ctx context.Context) string {
	if s.p.RebuildInFlight() {
		return "rebuild in flight"
	}
	if s.p.reporter == nil {
		return ""
	}
	entry, err := s.p.reporter.GetStatus(ctx)
	if err != nil {
		// Fail closed: an unreadable health entry means the pause state is
		// unknown, and skipping one tick costs a minute of latency where
		// sweeping through an operator pause costs correctness of intent.
		return "lens status unreadable: " + err.Error()
	}
	if entry.Status != "active" {
		return "lens status is " + entry.Status
	}
	return ""
}

// pass runs one bounded sweep tick: prefilter, deep verify, heal, then publish
// the resulting status.
func (s *Sweeper) pass(ctx context.Context) {
	if reason := s.suppressed(ctx); reason != "" {
		s.noteSuppressed(reason)
		return
	}
	if s.idleTick() {
		// Exactly like any other suppressed tick: no passNo, no survey, no
		// record — the cursor and every streak stand exactly as the last real
		// pass left them (see idleTick and updateIdleCycle).
		s.noteSuppressed(idleSuppressionReason)
		return
	}
	s.noteSuppressed("")

	s.mu.Lock()
	s.passNo++
	passNo := s.passNo
	s.mu.Unlock()

	anchors, targets, err := s.surveyCached(ctx, passNo)
	if err != nil {
		// A pass that could not read both sides of the comparison verified
		// nothing, so it must not read as a converged tick.
		slog.Warn("pipeline: sweep: survey failed; retrying next tick",
			"ruleId", s.p.ruleID, "err", err)
		s.clearIdleEligible()
		s.record(ctx, 0, err)
		return
	}
	s.reapDepartedFailures(anchors, targets)

	sel := s.candidates(ctx, anchors, targets)
	healed := 0
	// Each prefilter hint's premise is a hypothesis, and Reproject's verdict on
	// its candidates is the experiment that tests it.
	coverageTried, coverageHits := 0, 0
	orphanTried, orphanHits := 0, 0
	for _, actor := range sel.actors {
		res, rerr := s.p.Reproject(ctx, actor)
		if errors.Is(rerr, ErrRuleSuperseded) {
			// A hot-reload replaced the rule while this pass was mid-loop, so
			// every result still in hand derives from a retired rule. Abandon
			// the pass, mirroring the ordering-token arm below: the condition
			// is per-pipeline, not per-actor.
			//
			// Deliberately NOT noteActorFailure — no actor is at fault, and a
			// consecutive-failure strike would push it into backoffPasses and
			// delay the genuine post-rebuild heal. Info, not Warn: this is the
			// expected consequence of a reload, and the rebuild the same reload
			// kicked off is about to re-derive the corpus anyway.
			//
			// Recorded as an ABANDONED pass, not a completed one. A reload is
			// nobody's fault, so it raises no repair failure — but a pass that
			// stopped partway verified almost nothing, and stamping it as a
			// clean fault-free tick would reset the liveness clock and publish
			// a converged lens on the strength of a pass that examined one
			// anchor. That is precisely the collapse toward health this verdict
			// model exists to end.
			slog.Info("pipeline: sweep: pass abandoned — rule swapped mid-pass",
				"ruleId", s.p.ruleID, "actor", actor)
			s.clearIdleEligible()
			s.recordAbandoned(ctx, healed, "rule swapped mid-pass")
			return
		}
		if errors.Is(rerr, ErrNoOrderingToken) {
			// The pipeline has applied nothing this process, so every write
			// that would correct an existing row loses to its stored
			// watermark. Abandon the whole pass rather than repeat the refusal
			// per actor: the condition is per-pipeline, and it clears on its
			// own the moment the consumer acks anything (which it does for
			// every Core KV event, including ack-and-skip).
			slog.Warn("pipeline: sweep: pass abandoned — no ordering token yet",
				"ruleId", s.p.ruleID, "actor", actor)
			s.clearIdleEligible()
			s.record(ctx, healed, rerr)
			return
		}
		if rerr != nil {
			slog.Warn("pipeline: sweep: reproject failed",
				"ruleId", s.p.ruleID, "actor", actor, "err", rerr)
			s.noteActorFailure(actor, rerr, passNo)
			continue
		}
		s.clearActorFailure(actor)
		// A hint's premise is "this anchor is divergent", and a BLOCKED verdict
		// confirms that premise exactly as a heal does — the divergence was
		// real, the guard simply refused the repair. Scoring only writes would
		// demote the orphan hint precisely when it keeps finding rows the guard
		// will not let the sweep retract, throttling the detector for the
		// over-grant direction.
		premiseHeld := res.Wrote || res.Verdict == VerdictBlocked
		if _, viaCoverage := sel.fromCoverage[actor]; viaCoverage {
			coverageTried++
			if premiseHeld {
				coverageHits++
			}
		}
		if _, viaOrphan := sel.fromOrphan[actor]; viaOrphan {
			orphanTried++
			if premiseHeld {
				orphanHits++
			}
		}
		s.noteVerdict(actor, res.Verdict, res.BlockedClass, res.VerdictReason)
		switch res.Verdict {
		case VerdictBlocked:
			// Warn, not Info: the sweep has PROVEN it cannot repair this row,
			// and a quieter level would sit beside the heal lines it is not.
			slog.Warn("pipeline: sweep: divergence could not be repaired — the ordering guard declined the write",
				"ruleId", s.p.ruleID, "actor", actor, "class", res.BlockedClass.String(),
				"reason", res.VerdictReason, "projectionSeq", res.ProjectionSeq)
		case VerdictUnverified:
			slog.Warn("pipeline: sweep: anchor reached no verdict",
				"ruleId", s.p.ruleID, "actor", actor, "reason", res.VerdictReason)
		}
		if res.Wrote {
			healed++
			slog.Info("pipeline: sweep: healed a divergent projection",
				"ruleId", s.p.ruleID, "actor", actor, "deleted", res.Deleted,
				"projectionSeq", res.ProjectionSeq)
		}
	}
	s.noteHintOutcome("coverage", &s.coverage, coverageTried, coverageHits)
	s.noteHintOutcome("orphan", &s.orphan, orphanTried, orphanHits)

	s.updateIdleCycle(sel.deepVerifyExamined, len(anchors), healed)
	s.record(ctx, healed, nil)
}

// governingReasonLocked picks the reason a heartbeat message names when several
// anchors share a verdict: the lexicographically first, so the published text
// is stable across passes instead of flapping with map iteration order. The
// count beside it is what conveys breadth. Caller holds s.mu.
func governingReasonLocked(set map[string]string) string {
	governing := ""
	for _, reason := range set {
		if governing == "" || reason < governing {
			governing = reason
		}
	}
	return governing
}

// governingBlockedLocked picks the blocked verdict the heartbeat message names:
// the worst CLASS in the set, and within that class the lexicographically first
// reason so the published text stays stable across passes instead of flapping
// with map iteration order.
//
// The class is what decides, not the text. Ordering the reasons alphabetically
// makes the winner an accident of spelling — that "content divergence" sorts
// ahead of "provenance-only divergence" is the letter c, not an order anyone
// chose — and only ONE reason survives for the whole lens, so a single content
// divergence among sixteen provenance ones is exactly the row that must not
// lose. Caller holds s.mu.
func governingBlockedLocked(set map[string]blockedVerdict) (BlockedClass, string) {
	class, governing, chosen := BlockedUnknown, "", false
	for _, bv := range set {
		switch {
		case !chosen, bv.class.severity() > class.severity():
			class, governing, chosen = bv.class, bv.reason, true
		case bv.class == class && bv.reason < governing:
			governing = bv.reason
		}
	}
	return class, governing
}

// blockedCensusLocked counts the standing blocked set by class. It is derived
// here, at publish, rather than tallied as verdicts arrive: one source of truth
// means a class count cannot disagree with the total it is part of, and a pass
// that re-examined none of these anchors cannot silently drop one from the
// census. Caller holds s.mu.
func blockedCensusLocked(set map[string]blockedVerdict) map[BlockedClass]int {
	census := make(map[BlockedClass]int, len(set))
	for _, bv := range set {
		census[bv.class]++
	}
	return census
}

// noteSuppressed records (or clears) the reason the last tick verified nothing.
// It deliberately does NOT touch LastPassAt: a suppressed tick reached no
// verdict, so the liveness clock must keep aging — that ageing is the only thing
// that distinguishes a sweep held indefinitely from one that is simply converged
// and quiet, since both publish identical counters.
func (s *Sweeper) noteSuppressed(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Suppression = truncateFailure(reason)
	if reason == "" {
		s.status.SuppressionAt = time.Time{}
		return
	}
	s.status.SuppressionAt = time.Now()
}

// Interval is the sweep's tick period after defaults are applied. The heartbeat
// reads it to scale its staleness window off the sweep's own cadence instead of
// a second, independently-tuned constant.
func (s *Sweeper) Interval() time.Duration { return s.interval }

// backoffPasses is how many passes an actor sits out after its Nth consecutive
// repair failure: none after the first (a transient write error deserves the
// next tick), then doubling to a sixteen-pass ceiling — about a quarter hour at
// the 60s default. The ceiling is the part that matters: a permanently
// unwritable row must stop consuming a batch slot every tick, but must never
// back off so far that a fixed target waits hours to be re-attempted.
func backoffPasses(consecutive int) uint64 {
	if consecutive <= 1 {
		return 0
	}
	shift := consecutive - 2
	if shift > 4 {
		shift = 4
	}
	return 1 << shift
}

// backedOff reports whether this actor is serving a retry delay. A skipped
// actor keeps its entry in the failing set, so it still counts toward the
// lens's health verdict for every pass it is not attempted.
func (s *Sweeper) backedOff(actor string, passNo uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.failing[actor]
	return f != nil && passNo <= f.retryAfter
}

// reapDepartedFailures drops failure state for an actor that has left the
// sweep's universe entirely — no anchor vertex, and no target key this lens
// owns. Selection draws only from those two listings, so such an actor is never
// reached again and its entry would hold the lens at CapabilityRepairFailing
// for the life of the process (the case is ordinary: an orphan retraction that
// errors, then lands via the CDC path before the retry).
//
// Both listings are bounded feeds that can come back short, so a truncation
// clears a live failure for one pass at most — the actor reappears in the next
// listing and is re-selected. That is the same self-correcting trade the
// prefilter already makes, and the opposite error (never reaping) is not
// self-correcting at all.
func (s *Sweeper) reapDepartedFailures(anchors []string, targets map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.failing) == 0 && len(s.blocked) == 0 && len(s.unverified) == 0 {
		return
	}
	present := make(map[string]struct{}, len(anchors)+len(targets))
	for _, actor := range anchors {
		present[actor] = struct{}{}
	}
	for key := range targets {
		if actor, owned := s.plan.AnchorFromKey(key); owned {
			present[actor] = struct{}{}
		}
	}
	for actor := range s.failing {
		if _, ok := present[actor]; !ok {
			delete(s.failing, actor)
		}
	}
	// The verdict sets depart on the same terms: an anchor that has left the
	// corpus carries no live claim about a row, so holding its verdict would
	// report a permanent issue for something that no longer exists.
	for actor := range s.blocked {
		if _, ok := present[actor]; !ok {
			delete(s.blocked, actor)
		}
	}
	for actor := range s.unverified {
		if _, ok := present[actor]; !ok {
			delete(s.unverified, actor)
		}
	}
}

// noteVerdict records an anchor's non-clean verdict, and clearVerdicts retires
// both entries once a later pass reaches a clean one for that anchor. A verdict
// is per-anchor standing state: only a NEW verdict on the SAME anchor may
// supersede it, never the mere fact that some other batch ran.
func (s *Sweeper) noteVerdict(actor string, v Verdict, class BlockedClass, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch v {
	case VerdictBlocked:
		s.blocked[actor] = blockedVerdict{class: class, reason: reason}
		delete(s.unverified, actor)
	case VerdictUnverified:
		s.unverified[actor] = reason
		delete(s.blocked, actor)
	default:
		delete(s.blocked, actor)
		delete(s.unverified, actor)
	}
}

// noteActorFailure records an unrepaired reprojection and schedules its retry.
func (s *Sweeper) noteActorFailure(actor string, err error, passNo uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.failing[actor]
	if f == nil {
		f = &actorFailure{}
		s.failing[actor] = f
	}
	f.consecutive++
	f.retryAfter = passNo + backoffPasses(f.consecutive)
	f.err = err.Error()
}

// clearActorFailure retires an actor's failure state once its reprojection
// succeeds — including the converged no-write case, which proves the row is
// right just as well as a write does.
func (s *Sweeper) clearActorFailure(actor string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failing, actor)
}

// governingFailureLocked picks the failure the heartbeat message names: the
// longest-running one, ties broken by actor key so a steady-state fault reports
// the same cause every beat instead of flapping over map order.
func (s *Sweeper) governingFailureLocked() string {
	worst, worstActor := 0, ""
	for actor, f := range s.failing {
		if f.consecutive > worst || (f.consecutive == worst && actor < worstActor) {
			worst, worstActor = f.consecutive, actor
		}
	}
	if worstActor == "" {
		return ""
	}
	return s.failing[worstActor].err
}

// maxFailureText bounds the error text carried into the heartbeat, which is a
// Health-KV document with a NATS payload limit.
const maxFailureText = 200

// truncateFailure bounds the text and drops the partial rune a byte-offset cut
// can leave behind, which json.Marshal would otherwise coerce to U+FFFD.
func truncateFailure(s string) string {
	if len(s) <= maxFailureText {
		return s
	}
	return strings.ToValidUTF8(s[:maxFailureText], "") + "…"
}

// survey reads both sides of the coverage comparison: the lens's live anchors
// from Core KV and the target keys this lens owns.
//
// The anchor listing selects root vertex keys at the substrate: `vtx.<type>.*`
// matches exactly one trailing token, so the four-segment aspect keys sharing
// the prefix never cross the wire. A vertex carries several aspects, and five
// shipped lenses share anchorType `identity` with no sharing between them, so
// filtering them in Go instead meant listing the same discarded keys once per
// lens per tick. ParseVertexKey still runs on what comes back: the filter is a
// cost mechanism, and the parse is what decides the key is really a root of this
// anchor type — the same posture the target listing takes with AnchorFromKey.
//
// The listing still includes logically-deleted vertices (a tombstone is a live
// NATS-KV key with isDeleted set). They are not filtered here — that would cost
// one Core KV read per anchor per tick. The prefilter instead defers the
// liveness check to the point where it changes a decision (see candidates), and
// the deep verify reprojects a tombstoned anchor to the envelope's own delete
// semantics anyway.
//
// The target listing is scoped to the lens's own key prefix, so a bucket shared
// by a dozen lenses is not streamed in full once per lens per tick. Scoping
// narrows only: the prefix is the same literal AnchorFromKey matches first, so
// every key it removes is one candidates would have rejected as unowned. It is
// not a substitute for that test — one lens's prefix can be another's ancestor
// (cap. and cap.roles.) — and candidates still applies it.
func (s *Sweeper) survey(ctx context.Context) (anchors []string, targets map[string]struct{}, err error) {
	// limit<=0 returns every match in one page: the caller needs the whole anchor
	// set to build the expected-key map, and a page boundary there would hand the
	// orphan direction live actors to retract.
	rootFilter := substrate.VertexPrefix + "." + s.plan.AnchorType + ".*"
	keys, _, err := s.p.coreKV.ListKeysFilter(ctx, rootFilter, "", 0)
	if err != nil {
		return nil, nil, err
	}
	anchors = make([]string, 0, len(keys))
	for _, k := range keys {
		// The filter already excludes the four-segment aspect keys; this admits
		// only a well-formed root of the anchor type the plan names.
		vtxType, _, ok := substrate.ParseVertexKey(k)
		if !ok || vtxType != s.plan.AnchorType {
			continue
		}
		anchors = append(anchors, k)
	}
	sort.Strings(anchors)

	lister, ok := s.p.currentAdapter().(adapter.PrefixKeyLister)
	if !ok {
		// Every actor-aggregate target is NATS-KV (the §6.2 guard demands it,
		// and every such lens is guarded), so this is unreachable in
		// production; report it rather than sweeping with half the comparison
		// silently missing.
		return nil, nil, errSweepNoKeyLister
	}
	rows, err := lister.ListKeysPrefix(ctx, s.plan.KeyPrefix)
	if err != nil {
		return nil, nil, err
	}
	targets = make(map[string]struct{}, len(rows))
	for _, row := range rows {
		key, _ := row["key"].(string)
		if key == "" {
			continue
		}
		targets[key] = struct{}{}
	}
	return anchors, targets, nil
}

// surveyCached returns this tick's anchor/target listing, reusing the
// previous survey() result when NEITHER invalidation signal has moved since
// it was taken: the pipeline's applied sequence (Progress().LastAppliedSeq)
// and its projectionWrites counter (ProjectionWrites()). The anchor set can
// only move on a Core KV write this consumer has applied — the anchor label
// is always in a narrowed filter — and the target set only through a write
// this pipeline itself made (projectionWrites' doc). Reading both signals is
// cheap, in-memory, and safe to do every tick; it is survey()'s own two KV
// listings this cache exists to avoid repeating. Every surveyForceEvery'th
// pass re-lists regardless, as insurance against a target mutation neither
// signal saw.
func (s *Sweeper) surveyCached(ctx context.Context, passNo uint64) ([]string, map[string]struct{}, error) {
	appliedSeq := s.p.Progress().LastAppliedSeq
	writes := s.p.ProjectionWrites()

	s.mu.Lock()
	reuse := s.surveyValid &&
		appliedSeq == s.surveyAppliedSeq &&
		writes == s.surveyWrites &&
		passNo%surveyForceEvery != 0
	anchors, targets := s.surveyAnchors, s.surveyTargets
	s.mu.Unlock()
	if reuse {
		return anchors, targets, nil
	}

	anchors, targets, err := s.survey(ctx)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	s.surveyValid = true
	s.surveyAnchors, s.surveyTargets = anchors, targets
	s.surveyAppliedSeq, s.surveyWrites = appliedSeq, writes
	s.mu.Unlock()
	return anchors, targets, nil
}

// idleTick reports whether this tick should be skipped by the idle back-off,
// exactly like a suppressed one (see pass). It also owns clearing
// idleEligible the instant either survey-cache signal has moved — "any
// change... resets to every tick" — independent of whether the current skip
// streak has run out, so a change is acted on the very next tick rather than
// waited out for up to IdleSweepBackoffEvery-1 more ticks.
func (s *Sweeper) idleTick() bool {
	appliedSeq := s.p.Progress().LastAppliedSeq
	writes := s.p.ProjectionWrites()

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.surveyValid || appliedSeq != s.surveyAppliedSeq || writes != s.surveyWrites {
		s.idleEligible = false
		s.idleSkipped = 0
		return false
	}
	if !s.idleEligible {
		s.idleSkipped = 0
		return false
	}
	if s.idleSkipped >= IdleSweepBackoffEvery-1 {
		s.idleSkipped = 0
		return false
	}
	s.idleSkipped++
	return true
}

// clearIdleEligible forces the idle back-off off immediately and discards the
// current lap's progress. Called on a pass that faulted or was abandoned
// before it reached a verdict: such a pass proves nothing about the corpus,
// so it must neither be read as evidence of a clean lap nor let a later lap's
// completion be credited with visits it never actually made.
func (s *Sweeper) clearIdleEligible() {
	s.mu.Lock()
	s.idleEligible = false
	s.cycle = idleCycle{}
	s.mu.Unlock()
}

// updateIdleCycle folds one normally-completed pass's deep-verify progress
// and outcome into the current lap, and evaluates idleEligible once the lap
// completes — anchorCount anchors examined in total since the lap began (a
// lens with zero anchors has no lap to walk, so every such pass evaluates
// immediately and vacuously). Read the failing/blocked/unverified sets here
// rather than from anything the caller tallied, for the same reason record
// does: a pass that re-examined only some of them must judge the lap on the
// STANDING sets, not on this tick's slice.
func (s *Sweeper) updateIdleCycle(examined, anchorCount, healed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cycle.visited += examined
	if healed > 0 || len(s.blocked) > 0 || len(s.unverified) > 0 || len(s.failing) > 0 {
		s.cycle.dirty = true
	}
	if anchorCount > 0 && s.cycle.visited < anchorCount {
		return
	}
	s.idleEligible = !s.cycle.dirty
	s.cycle = idleCycle{}
}

// candidates picks this tick's bounded actor set: the two prefilter hints, then
// the round-robin deep verify continuing from the persisted cursor. The result
// is deduplicated and capped at the batch size.
//
// Each direction holds a reserved share the others cannot consume, because two
// of them are the ONLY detector for what they see: the deep verify for a row
// that is present but stale, and the orphan hint for a row whose anchor is gone
// (the deep verify walks anchors, and an orphan's anchor is by definition not
// among them). A direction that could be starved indefinitely is a detector that
// can be silently switched off.
//
// A reserved share is necessary but NOT sufficient: a hint whose set stays
// populated forever must also reach every member of it, or an individual
// divergence starves while the direction's slot count looks healthy. That is
// what each hint's cursor is for (hintState) — a hint that always walked its
// sorted head would retract the same already-retracted tombstones every tick
// while a genuinely stale row further down the order was never reached.
//
// Each share is sized to the work that exists, so a direction with nothing to do
// hands its slots on: a lens with no orphans leaves the coverage hint the whole
// prefilter, and a listing with no anchors leaves the orphan hint the whole
// batch. Below three slots a batch cannot seat three directions at all; the
// default is 25 and nothing in production overrides it.
//
// The prefilter only SELECTS; it never decides. Every candidate is answered by
// Reproject, which re-derives the row from live Core KV — so a key listing that
// came back short (both listings are bounded feeds) costs a wasted
// re-verification at worst, never a wrong write. Retracting an "orphan" straight
// from the listing instead of through Reproject would trade that property for a
// mass over-revocation on the auth plane the first time a listing truncated.
func (s *Sweeper) candidates(ctx context.Context, anchors []string, targets map[string]struct{}) selection {
	quota := s.batch / 5
	if quota < 1 {
		quota = 1
	}

	s.mu.Lock()
	passNo := s.passNo
	coverage, orphanHint := s.coverage, s.orphan
	s.mu.Unlock()
	coverageFloored := coverage.floored(passNo)
	orphanFloored := orphanHint.floored(passNo)

	fromCoverage := make(map[string]struct{}, s.batch)
	fromOrphan := make(map[string]struct{}, s.batch)
	out := make([]string, 0, s.batch)
	seen := make(map[string]struct{}, s.batch)
	addUpTo := func(actor string, limit int) bool {
		if len(out) >= limit {
			return false
		}
		if _, dup := seen[actor]; dup {
			return true
		}
		seen[actor] = struct{}{}
		// An actor serving a retry delay yields its slot to the next divergent
		// one rather than spending it on a skip — the point of backing off is
		// to stop a permanently unwritable row from consuming a batch slot
		// every tick. It stays in the failing set, so declining to retry it
		// does not change the lens's health verdict.
		if s.backedOff(actor, passNo) {
			return true
		}
		out = append(out, actor)
		return true
	}

	// listedAnchors is the set of anchor vertex keys survey() found in Core KV
	// — present, whether tombstoned or not; anchorLive below is the real
	// liveness test — built over EVERY anchor before and independently of any
	// budget. A set truncated by a selection budget would hand the orphan
	// direction below a tombstoned-but-still-listed actor's row to retract as
	// if its anchor had vanished outright, which is the deep verify's call to
	// make, not the orphan hint's.
	listedAnchors := make(map[string]struct{}, len(anchors))
	for _, actor := range anchors {
		listedAnchors[actor] = struct{}{}
	}

	// coveredAnchors and orphans are both grouped from the SAME single pass
	// over the target listing, by the anchor AnchorFromKey recovers for each
	// key — not by testing key equality against one BuildKey(actor) probe per
	// anchor. That distinction is load-bearing for a perEntry lens (§4.4 of
	// cap-read-per-anchor-grant-keys-design.md): no single real key ever
	// equals BuildKey(actor) alone (every real key carries one more trailing
	// entry token), so an equality probe would find every live actor's every
	// child key "not expected" and flood the orphan set with actors that are
	// very much alive. Grouping by recovered anchor instead answers both
	// directions correctly for a doc-mode lens too (its one document per actor
	// recovers to that actor exactly, so the two computations coincide) — one
	// mechanism, not two per lens shape.
	//
	// Keys this lens does not own are rejected by AnchorFromKey, so a shared
	// bucket's sibling rows are never touched, in either set. Resolving both
	// sets up front costs no I/O beyond the listing already in hand.
	coveredAnchors := make(map[string]struct{}, len(anchors))
	orphans := make([]string, 0)
	for key := range targets {
		actor, owned := s.plan.AnchorFromKey(key)
		if !owned {
			continue
		}
		if _, listed := listedAnchors[actor]; listed {
			coveredAnchors[actor] = struct{}{}
			continue
		}
		orphans = append(orphans, actor)
	}
	sort.Strings(orphans)
	orphans = slices.Compact(orphans)

	// The deep verify has no work with nothing to walk, and the orphan hint none
	// with nothing to retract; an idle reservation would just shrink the batch.
	deepQuota := quota
	if len(anchors) == 0 {
		deepQuota = 0
	}
	orphanQuota := quota
	if orphanQuota > len(orphans) {
		orphanQuota = len(orphans)
	}

	coverageCap := s.batch - deepQuota - orphanQuota
	if coverageFloored && coverageCap > quota {
		coverageCap = quota
	}
	if coverageCap < 1 {
		coverageCap = 1
	}

	// Direction 1 — an anchor with no target key, walked from its own cursor. A
	// tombstoned anchor has the same signature legitimately: its row was
	// retracted when the tombstone projected, so it keeps a listed vertex key
	// and no target key forever. The liveness check runs here rather than over
	// every anchor — one Core KV read per candidate examined, not one per
	// anchor.
	//
	// Examinations are budgeted, not just selections. A tombstoned row-less
	// anchor costs an anchorLive read and then yields no slot, so a population
	// of them never fills the batch: bounding only selections let the walk read
	// EVERY row-less anchor on every tick while selecting nothing, against a
	// cost model of one bounded batch a minute. The budget bounds the reads; the
	// cursor is what keeps that from starving the tail, since it advances over
	// examined-and-skipped anchors too and the next tick resumes past them. A
	// budget without the cursor would re-read the same head forever — the
	// fairness failure §11 decision 4 already made once on this walk.
	if len(anchors) > 0 {
		examineCap := coverageCap * coverageExamineMultiple
		start := resumeAt(anchors, coverage.cursor)
		last := coverage.cursor
		for i := 0; i < len(anchors); i++ {
			if len(out) >= coverageCap || i >= examineCap {
				break
			}
			actor := anchors[(start+i)%len(anchors)]
			if _, present := coveredAnchors[actor]; present {
				last = actor
				continue
			}
			if !s.anchorLive(ctx, actor) {
				last = actor
				continue
			}
			before := len(out)
			addUpTo(actor, coverageCap)
			if len(out) > before {
				// Only an actor that actually took a slot is evidence: a
				// backed-off one is never re-projected, so it can neither
				// confirm nor refute the hint's premise.
				fromCoverage[actor] = struct{}{}
			}
			last = actor
		}
		s.mu.Lock()
		s.coverage.cursor = last
		s.mu.Unlock()
	}

	// Direction 2 — the orphans, walked from their own cursor so an
	// already-retracted tombstone that keeps presenting as an orphan cannot hold
	// a slot against the rest of the set. Its allowance is whatever the coverage
	// hint left, less the deep verify's reservation.
	if len(orphans) > 0 {
		allowance := s.batch - deepQuota - len(out)
		if orphanFloored && allowance > quota {
			allowance = quota
		}
		orphanCap := len(out) + allowance
		start := resumeAt(orphans, orphanHint.cursor)
		last := orphanHint.cursor
		for i := 0; i < len(orphans); i++ {
			if len(out) >= orphanCap {
				break
			}
			actor := orphans[(start+i)%len(orphans)]
			before := len(out)
			addUpTo(actor, orphanCap)
			if len(out) > before {
				fromOrphan[actor] = struct{}{}
			}
			last = actor
		}
		s.mu.Lock()
		s.orphan.cursor = last
		s.mu.Unlock()
	}

	// Direction 3 — the bounded round-robin deep verify. Re-executing the
	// projection is the only detector for a row that is present but stale, the
	// over-grant case neither prefilter direction can see.
	if len(anchors) == 0 {
		return selection{actors: out, fromCoverage: fromCoverage, fromOrphan: fromOrphan}
	}
	s.mu.Lock()
	cursor := s.status.Cursor
	s.mu.Unlock()
	start := resumeAt(anchors, cursor)
	last := cursor
	examined := 0
	for i := 0; i < len(anchors) && len(out) < s.batch; i++ {
		examined++
		actor := anchors[(start+i)%len(anchors)]
		last = actor
		// The full batch, not a hint's share: this is the reserved remainder the
		// walk is guaranteed.
		if !addUpTo(actor, s.batch) {
			break
		}
	}
	s.mu.Lock()
	s.status.Cursor = last
	s.mu.Unlock()
	return selection{actors: out, fromCoverage: fromCoverage, fromOrphan: fromOrphan, deepVerifyExamined: examined}
}

// selection is one tick's candidate set plus which prefilter hint proposed each
// actor, so the pass can test each hint's premise against what Reproject found.
// The two hint sets are disjoint by construction: an orphan's recovered anchor
// is absent from listedAnchors, which is exactly what makes it not an anchor.
//
// deepVerifyExamined is how many anchors direction 3's round-robin walk looked
// at this tick (whether or not each was actually selected — a dup or a
// backed-off actor is still examined), so the idle back-off can tell how much
// of a full lap this tick advanced (updateIdleCycle). Zero when there were no
// anchors to walk at all.
type selection struct {
	actors             []string
	fromCoverage       map[string]struct{}
	fromOrphan         map[string]struct{}
	deepVerifyExamined int
}

// resumeAt is where a cursored walk over a sorted key list continues: the first
// entry after the cursor, wrapping to the start when the cursor has fallen off
// the end (the entry it named may have left the list, which is why it is a
// binary search for the position rather than a lookup of the key).
func resumeAt(sorted []string, cursor string) int {
	if cursor == "" {
		return 0
	}
	start := sort.SearchStrings(sorted, cursor)
	if start < len(sorted) && sorted[start] == cursor {
		start++
	}
	if start >= len(sorted) {
		return 0
	}
	return start
}

// noteHintOutcome folds one pass's evidence about a prefilter hint's premise.
// tried counts the hint's candidates that Reproject actually answered, and hits
// how many of those wrote.
//
// A pass that selected nothing through the hint, or whose candidates all
// errored, is NO evidence: it leaves the standing record alone rather than
// counting against the hint, so an erroring lens is never mistaken for one whose
// premise is false. A single write clears the record outright, so a lens that
// covers its anchors and then loses a batch of rows is back to the full share on
// the pass after the one that discovers the loss.
func (s *Sweeper) noteHintOutcome(name string, h *hintState, tried, hits int) {
	if tried <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if hits > 0 {
		if h.misses >= hintMissesBeforeFloor {
			slog.Info("pipeline: sweep: prefilter hint healed again; restoring its full share",
				"ruleId", s.p.ruleID, "hint", name)
		}
		h.misses = 0
		return
	}
	h.misses++
	if h.misses == hintMissesBeforeFloor {
		// The demotion is the sweep quietly changing how it spends its batch, so
		// it is said out loud once: an operator chasing why a detector is slow
		// otherwise has no way to see that it is running at its floor.
		slog.Info("pipeline: sweep: prefilter hint healed nothing; capping it at its reserved floor",
			"ruleId", s.p.ruleID, "hint", name, "passes", h.misses)
	}
}

// anchorLive reports whether an anchor vertex is present and not tombstoned.
func (s *Sweeper) anchorLive(ctx context.Context, actorKey string) bool {
	props, err := s.p.fetchVertexProps(ctx, actorKey)
	if err != nil {
		// Treat an unreadable vertex as live so a transient Core KV error
		// cannot silently drop a real divergence; Reproject re-reads it and
		// converges on its own if the read was wrong.
		return true
	}
	return props != nil
}

// record folds this pass's outcome into the status and persists the cursor +
// cumulative heal count to the lens's Health KV entry. The divergent streak is
// one escalation input: one divergent pass is a warning, a second consecutive
// one is an error, and a clean pass clears it. The failed streak is the other,
// and answers the case the heal count cannot see — a pass that repaired nothing
// because its repairs ERRORED looks identical, on the heal count alone, to a
// pass that repaired nothing because everything was already converged.
//
// fault is a pass-level failure (the survey, or a tick abandoned before it
// could verify anything); a nil fault with an empty failing set is the only
// combination that clears the repair verdict.
//
// The blocked and unverified verdicts are read from their standing SETS rather
// than from anything this pass tallied, so a pass that re-examined none of
// those anchors — or abandoned before it could — leaves them intact. A pass
// must never clear a verdict it did not re-derive; that is the same collapse
// toward health this whole verdict model exists to end, one layer up.
// recordAbandoned books a pass that stopped before it could verify its batch.
// It banks the heals that genuinely landed and publishes the reason, but leaves
// every streak and the liveness clock alone: the pass reached no verdict, so it
// may neither clear one nor claim the freshness of one. This is the disposition
// noteSuppressed already takes for a tick that never ran — an abandoned pass is
// far closer to that than to a completed one.
func (s *Sweeper) recordAbandoned(ctx context.Context, healed int, reason string) {
	s.mu.Lock()
	s.status.Reconciled += uint64(healed)
	s.status.Suppression = reason
	s.status.SuppressionAt = time.Now()
	snapshot := s.status
	s.mu.Unlock()

	if s.p.reporter == nil {
		return
	}
	if err := s.p.reporter.SetSweepProgress(ctx, snapshot.Cursor, snapshot.Reconciled); err != nil {
		slog.Warn("pipeline: sweep: could not persist cursor",
			"ruleId", s.p.ruleID, "err", err)
	}
}

func (s *Sweeper) record(ctx context.Context, healed int, fault error) {
	s.mu.Lock()
	s.status.Reconciled += uint64(healed)
	if healed > 0 {
		s.status.DivergentStreak++
	} else {
		s.status.DivergentStreak = 0
	}
	s.status.Unverified = len(s.unverified)
	if len(s.unverified) > 0 {
		s.status.UnverifiedStreak++
		s.status.LastUnverified = truncateFailure(governingReasonLocked(s.unverified))
	} else {
		s.status.UnverifiedStreak = 0
		s.status.LastUnverified = ""
	}
	s.status.Blocked = len(s.blocked)
	s.status.BlockedByClass = blockedCensusLocked(s.blocked)
	if len(s.blocked) > 0 {
		s.status.BlockedStreak++
		worst, reason := governingBlockedLocked(s.blocked)
		s.status.WorstBlockedClass = worst.String()
		s.status.LastBlocked = truncateFailure(reason)
	} else {
		s.status.BlockedStreak = 0
		s.status.WorstBlockedClass = ""
		s.status.LastBlocked = ""
	}
	s.status.FailingActors = len(s.failing)
	switch {
	case fault != nil:
		s.status.LastFailure = truncateFailure(fault.Error())
	case s.status.FailingActors > 0:
		s.status.LastFailure = truncateFailure(s.governingFailureLocked())
	default:
		s.status.LastFailure = ""
	}
	if s.status.FailingActors > 0 || fault != nil {
		s.status.FailedStreak++
	} else {
		s.status.FailedStreak = 0
	}
	s.status.LastPassAt = time.Now()
	snapshot := s.status
	s.mu.Unlock()

	if s.p.reporter == nil {
		return
	}
	if err := s.p.reporter.SetSweepProgress(ctx, snapshot.Cursor, snapshot.Reconciled); err != nil {
		slog.Warn("pipeline: sweep: could not persist cursor",
			"ruleId", s.p.ruleID, "err", err)
	}
}

// errSweepNoKeyLister marks a target store that cannot enumerate the keys under
// this lens's own prefix — the coverage prefilter's precondition. An unscoped
// enumeration is not a fallback: on a shared target it returns rows this lens
// does not own.
var errSweepNoKeyLister = errors.New("pipeline: sweep: target adapter cannot enumerate this lens's keys (not a PrefixKeyLister)")
