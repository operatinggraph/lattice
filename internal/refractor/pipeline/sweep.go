package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// DefaultSweepInterval and DefaultSweepBatch bound the auth-plane convergence
// sweep (capability-projection-reconciliation-design.md §3.2). The deep pass
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
	// BuildKey renders one anchor's target key (OutputDescriptor.BuildKey).
	BuildKey func(actorKey string) string
	// AnchorFromKey recovers the anchor a target key was built for, reporting
	// false for a key this lens does not own (OutputDescriptor.AnchorFromKey).
	AnchorFromKey func(targetKey string) (string, bool)
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
	passNo  uint64
	// coverage and orphan are the two prefilter hints' rotation + earned-share
	// state (see hintState and candidates).
	coverage hintState
	orphan   hintState
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
		p:        p,
		plan:     plan,
		interval: iv,
		batch:    batch,
		failing:  map[string]*actorFailure{},
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
// installed (every non-auth-plane lens).
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
	s.noteSuppressed("")

	s.mu.Lock()
	s.passNo++
	passNo := s.passNo
	s.mu.Unlock()

	anchors, targets, err := s.survey(ctx)
	if err != nil {
		// A pass that could not read both sides of the comparison verified
		// nothing, so it must not read as a converged tick.
		slog.Warn("pipeline: sweep: survey failed; retrying next tick",
			"ruleId", s.p.ruleID, "err", err)
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
		if errors.Is(rerr, ErrNoOrderingToken) {
			// The pipeline has applied nothing this process, so every write
			// that would correct an existing row loses to its stored
			// watermark. Abandon the whole pass rather than repeat the refusal
			// per actor: the condition is per-pipeline, and it clears on its
			// own the moment the consumer acks anything (which it does for
			// every Core KV event, including ack-and-skip).
			slog.Warn("pipeline: sweep: pass abandoned — no ordering token yet",
				"ruleId", s.p.ruleID, "actor", actor)
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
		if _, viaCoverage := sel.fromCoverage[actor]; viaCoverage {
			coverageTried++
			if res.Wrote {
				coverageHits++
			}
		}
		if _, viaOrphan := sel.fromOrphan[actor]; viaOrphan {
			orphanTried++
			if res.Wrote {
				orphanHits++
			}
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

	s.record(ctx, healed, nil)
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
	if len(s.failing) == 0 {
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
// The anchor listing is by key prefix and therefore includes logically-deleted
// vertices (a tombstone is a live NATS-KV key with isDeleted set). They are not
// filtered here — that would cost one Core KV read per anchor per tick. The
// prefilter instead defers the liveness check to the point where it changes a
// decision (see candidates), and the deep verify reprojects a tombstoned anchor
// to the envelope's own delete semantics anyway.
func (s *Sweeper) survey(ctx context.Context) (anchors []string, targets map[string]struct{}, err error) {
	prefix := substrate.VertexPrefix + "." + s.plan.AnchorType + "."
	keys, err := s.p.coreKV.ListKeysPrefix(ctx, prefix)
	if err != nil {
		return nil, nil, err
	}
	anchors = make([]string, 0, len(keys))
	for _, k := range keys {
		// The prefix also matches this vertex's aspects (four segments);
		// ParseVertexKey admits only the three-segment root.
		vtxType, _, ok := substrate.ParseVertexKey(k)
		if !ok || vtxType != s.plan.AnchorType {
			continue
		}
		anchors = append(anchors, k)
	}
	sort.Strings(anchors)

	lister, ok := s.p.currentAdapter().(adapter.KeyLister)
	if !ok {
		// Every auth-plane target is NATS-KV (the §6.2 guard demands it), so
		// this is unreachable in production; report it rather than sweeping
		// with half the comparison silently missing.
		return nil, nil, errSweepNoKeyLister
	}
	rows, err := lister.ListKeys(ctx)
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

	// The expected-key map is built over EVERY anchor, before and independently
	// of any budget. It is what tells the orphan direction which target keys
	// have a live anchor behind them, so a map truncated by a selection budget
	// would hand that direction healthy actors to re-project as orphans.
	expected := make(map[string]string, len(anchors))
	for _, actor := range anchors {
		expected[s.plan.BuildKey(actor)] = actor
	}

	// The orphan hint's candidate set — a target key with no live anchor behind
	// it. Keys this lens does not own are rejected by AnchorFromKey, so a shared
	// bucket's sibling rows are never touched. Resolving it up front costs no
	// I/O and is what lets its share be sized to the work that exists. It is
	// sorted so the cursor can resume in it, and deduplicated so a lens with
	// several keys per anchor sizes the share by actors to reproject rather than
	// by keys.
	orphans := make([]string, 0)
	for key := range targets {
		if _, isExpected := expected[key]; isExpected {
			continue
		}
		if actor, owned := s.plan.AnchorFromKey(key); owned {
			orphans = append(orphans, actor)
		}
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
	if len(anchors) > 0 {
		start := resumeAt(anchors, coverage.cursor)
		last := coverage.cursor
		for i := 0; i < len(anchors); i++ {
			if len(out) >= coverageCap {
				break
			}
			actor := anchors[(start+i)%len(anchors)]
			key := s.plan.BuildKey(actor)
			if _, present := targets[key]; present {
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
	for i := 0; i < len(anchors) && len(out) < s.batch; i++ {
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
	return selection{actors: out, fromCoverage: fromCoverage, fromOrphan: fromOrphan}
}

// selection is one tick's candidate set plus which prefilter hint proposed each
// actor, so the pass can test each hint's premise against what Reproject found.
// The two hint sets are disjoint by construction: an orphan's key is absent from
// the expected map, which is exactly what makes it not an anchor.
type selection struct {
	actors       []string
	fromCoverage map[string]struct{}
	fromOrphan   map[string]struct{}
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
func (s *Sweeper) record(ctx context.Context, healed int, fault error) {
	s.mu.Lock()
	s.status.Reconciled += uint64(healed)
	if healed > 0 {
		s.status.DivergentStreak++
	} else {
		s.status.DivergentStreak = 0
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

// errSweepNoKeyLister marks a target store that cannot enumerate its keys —
// the coverage prefilter's precondition.
var errSweepNoKeyLister = errors.New("pipeline: sweep: target adapter cannot enumerate keys (not a KeyLister)")
