package pipeline

import (
	"context"
	"errors"
	"log/slog"
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
	LastPassAt      time.Time
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
// catches the definite cases in both directions: an anchor with no target key
// (the observed first-projection loss) and a target key whose anchor is gone
// (an over-grant). The round-robin deep verify then walks all anchors a bounded
// batch at a time, re-executing the projection; that is what catches a row
// which is present but stale — the over-grant direction the prefilter cannot
// see, since a revoked actor keeps both its vertex and its key.
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

// suppressed reports whether this tick must be skipped. A rebuild is a superset
// of the sweep (truncate + full rescan), and a paused pipeline is operator
// intent that reconciliation must not quietly override.
func (s *Sweeper) suppressed(ctx context.Context) bool {
	if s.p.RebuildInFlight() {
		return true
	}
	if s.p.reporter == nil {
		return false
	}
	entry, err := s.p.reporter.GetStatus(ctx)
	if err != nil {
		// Fail closed: an unreadable health entry means the pause state is
		// unknown, and skipping one tick costs a minute of latency where
		// sweeping through an operator pause costs correctness of intent.
		return true
	}
	return entry.Status != "active"
}

// pass runs one bounded sweep tick: prefilter, deep verify, heal, then publish
// the resulting status.
func (s *Sweeper) pass(ctx context.Context) {
	if s.suppressed(ctx) {
		return
	}

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

	candidates := s.candidates(ctx, anchors, targets)
	healed := 0
	for _, actor := range candidates {
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
		if res.Wrote {
			healed++
			slog.Info("pipeline: sweep: healed a divergent projection",
				"ruleId", s.p.ruleID, "actor", actor, "deleted", res.Deleted,
				"projectionSeq", res.ProjectionSeq)
		}
	}

	s.record(ctx, healed, nil)
}

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

// candidates picks this tick's bounded actor set: the prefilter's definite
// divergences first (they are known-wrong right now), then the round-robin deep
// verify continuing from the persisted cursor. The result is deduplicated and
// capped at the batch size, and the cursor advances only over the anchors the
// deep pass actually reached.
//
// The prefilter only SELECTS; it never decides. Every candidate is answered by
// Reproject, which re-derives the row from live Core KV — so a key listing that
// came back short (both listings are bounded feeds) costs a wasted
// re-verification at worst, never a wrong write. Retracting an "orphan" straight
// from the listing instead of through Reproject would trade that property for a
// mass over-revocation on the auth plane the first time a listing truncated.
func (s *Sweeper) candidates(ctx context.Context, anchors []string, targets map[string]struct{}) []string {
	// The deep verify keeps a reserved slice of every batch. Without it, any
	// prefilter candidate that recurs indefinitely — an actor whose heal keeps
	// erroring, a soft-delete target key that stays listed after retraction —
	// would refill the batch each tick and starve the round-robin walk forever,
	// silently disabling the only detector for a stale-but-present row.
	deepQuota := s.batch / 5
	if deepQuota < 1 {
		deepQuota = 1
	}
	prefilterCap := s.batch - deepQuota
	if prefilterCap < 1 {
		// A batch of one cannot honor both reservations; the definite
		// divergence wins the slot, since it is known-wrong right now.
		prefilterCap = 1
	}

	s.mu.Lock()
	passNo := s.passNo
	s.mu.Unlock()

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
	add := func(actor string) bool { return addUpTo(actor, prefilterCap) }

	// Direction 1 — an anchor with no target key. This is the observed
	// first-projection loss. A tombstoned anchor has the same signature
	// legitimately: its row was retracted when the tombstone projected, so it
	// keeps a listed vertex key and no target key forever. Without the liveness
	// check the accumulated tombstone set would refill the batch on every tick
	// and permanently starve the deep verify below, so the check runs here —
	// one Core KV read per candidate rather than one per anchor.
	expected := make(map[string]string, len(anchors))
	for _, actor := range anchors {
		key := s.plan.BuildKey(actor)
		expected[key] = actor
		if _, present := targets[key]; present {
			continue
		}
		if !s.anchorLive(ctx, actor) {
			continue
		}
		if !add(actor) {
			break
		}
	}

	// Direction 2 — a target key whose anchor is gone from Core KV entirely: a
	// row that should have been retracted. Keys this lens does not own are
	// rejected by AnchorFromKey, so a shared bucket's sibling rows are never
	// touched.
	for key := range targets {
		if _, isExpected := expected[key]; isExpected {
			continue
		}
		actor, owned := s.plan.AnchorFromKey(key)
		if !owned {
			continue
		}
		if !add(actor) {
			break
		}
	}

	// Direction 3 — the bounded round-robin deep verify. Re-executing the
	// projection is the only detector for a row that is present but stale, the
	// over-grant case neither prefilter direction can see.
	if len(anchors) == 0 {
		return out
	}
	start := 0
	s.mu.Lock()
	cursor := s.status.Cursor
	s.mu.Unlock()
	if cursor != "" {
		start = sort.SearchStrings(anchors, cursor)
		if start < len(anchors) && anchors[start] == cursor {
			start++
		}
		if start >= len(anchors) {
			start = 0
		}
	}
	last := cursor
	for i := 0; i < len(anchors) && len(out) < s.batch; i++ {
		actor := anchors[(start+i)%len(anchors)]
		last = actor
		// The full batch, not the prefilter's share: this is the reserved
		// remainder the walk is guaranteed.
		if !addUpTo(actor, s.batch) {
			break
		}
	}
	s.mu.Lock()
	s.status.Cursor = last
	s.mu.Unlock()
	return out
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
