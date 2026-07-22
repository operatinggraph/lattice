package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sort"
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
type SweepStatus struct {
	Reconciled      uint64
	DivergentStreak int
	Cursor          string
	LastPassAt      time.Time
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
	return &Sweeper{p: p, plan: plan, interval: iv, batch: batch}
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

	anchors, targets, err := s.survey(ctx)
	if err != nil {
		slog.Warn("pipeline: sweep: survey failed; retrying next tick",
			"ruleId", s.p.ruleID, "err", err)
		return
	}

	candidates := s.candidates(ctx, anchors, targets)
	healed := 0
	for _, actor := range candidates {
		res, rerr := s.p.Reproject(ctx, actor)
		if rerr != nil {
			slog.Warn("pipeline: sweep: reproject failed",
				"ruleId", s.p.ruleID, "actor", actor, "err", rerr)
			continue
		}
		if res.Wrote {
			healed++
			slog.Info("pipeline: sweep: healed a divergent projection",
				"ruleId", s.p.ruleID, "actor", actor, "deleted", res.Deleted,
				"projectionSeq", res.ProjectionSeq)
		}
	}

	s.record(ctx, healed)
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
// the escalation input: one divergent pass is a warning, a second consecutive
// one is an error, and a clean pass clears it.
func (s *Sweeper) record(ctx context.Context, healed int) {
	s.mu.Lock()
	s.status.Reconciled += uint64(healed)
	if healed > 0 {
		s.status.DivergentStreak++
	} else {
		s.status.DivergentStreak = 0
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
