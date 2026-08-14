package grantchange

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// DefaultPersonalSweepInterval and DefaultPersonalSweepBatch bound the personal
// convergence sweep (personal-lens-grant-change-trigger-design.md §4.3).
//
// The arithmetic behind them: one batch costs Batch × (registered personal
// lenses) cypher evaluations, and the shipped corpus carries fifteen personal
// lenses, so 5 identities a minute is 75 evaluations a minute — the same order
// as the auth plane's own sweep (25 anchors × 4 producers). A full cycle is
// N/5 minutes: minutes at showcase scale, roughly 33 hours for a
// 10k-identity cell.
//
// That cycle time bounds only the UN-SIGNALLED worst case — a signal lost with
// the process, a lens that registered after the transition, an overflowed
// coalescing set. Every transition the process observes is handled by the
// Reprojector's drain in about a second, so this is not the revocation latency;
// it is the latency of the cases nothing else covers.
const (
	DefaultPersonalSweepInterval = 60 * time.Second
	DefaultPersonalSweepBatch    = 5
)

// CoreKVLister is the one Core KV capability the sweep needs: enumerate the
// identity population. Narrow on purpose — the sweeper reads nothing else, and
// a one-method surface is what lets its unit tests hand it a scripted
// population instead of a live bucket.
type CoreKVLister interface {
	ListKeysFilter(ctx context.Context, filter, cursor string, limit int) ([]string, string, error)
}

// PersonalSweeper is the personal plane's standing healer: one shared ticker
// that walks the identity population in bounded batches and re-drives each
// identity across every registered personal lens.
//
// It exists because the fast path cannot be trusted alone. The grant-change
// edge is an in-process, best-effort signal — a crash between the producer's
// write and the drain loses it, the coalescing set is bounded, a lens that
// registers late never hears about a transition that landed first, and a
// multi-instance deployment can land producer and consumer on different
// processes. Prevention is best-effort; detect-and-recover is authoritative,
// and this is the recover half.
//
// It is a NEW sweeper rather than a reuse of pipeline.Sweeper. Exactly two
// things transfer: the Core-KV anchor population walk and the round-robin
// cursor. The auth-plane sweeper's own machinery does not — it hard-fails
// without a target key lister, and its candidate selection is built almost
// entirely out of the target listing, to find ORPHAN rows. There is no orphan
// direction here and none is needed: the authoritative keyset frame is the
// stray-killer, evaluated on the device, where every key absent from the frame
// is pruned.
//
// It holds no registry of its own. The Reprojector already keeps exactly the
// list of personal pipelines this needs, and re-drives one actor across all of
// them with a per-lens error posture that is right for this caller too — so
// the sweeper's candidates go through ReprojectNow rather than duplicating
// that walk beside it.
type PersonalSweeper struct {
	r      *Reprojector
	coreKV CoreKVLister

	mu sync.Mutex
	// population is the cached identity census for the CURRENT cycle: bare
	// NanoIDs (not vertex keys — that is what ReprojectPersonalActor takes),
	// sorted, so the cursor names a stable position in it.
	//
	// Cached rather than re-listed per tick because the listing is deliberately
	// unpaged: it asks for the whole population in one page, so re-listing per
	// tick would cost a 10k-key enumeration a minute on top of the batch the
	// interval is meant to bound. An EMPTY answer is never cached — see
	// ensurePopulation — because a cycle over nothing never wraps, and a cache
	// only the wrap invalidates would leave a cell that boots with no
	// identities permanently unswept.
	population []string
	loaded     bool
	// cursor is the last identity swept, the round-robin resume point. It is
	// NOT persisted: a restart re-starts the cycle from the top, which is the
	// safe direction (re-work, never a skipped segment), and mirrors what the
	// auth-plane sweeper's own in-memory cursor does.
	cursor string
	// cycleCompletedAt is when the walk last reached the END of the population.
	// It is the coverage claim: a tick covers at most Batch identities, so
	// "the sweep is running" says nothing about the whole plane until a cycle
	// has closed over it.
	cycleCompletedAt time.Time

	batch    int
	interval time.Duration
}

// NewPersonalSweeper builds the sweeper over the Reprojector whose registry it
// shares and the Core KV handle it enumerates identities from.
func NewPersonalSweeper(r *Reprojector, coreKV CoreKVLister) *PersonalSweeper {
	return &PersonalSweeper{
		r:        r,
		coreKV:   coreKV,
		batch:    DefaultPersonalSweepBatch,
		interval: DefaultPersonalSweepInterval,
	}
}

// SetBounds overrides the per-tick batch and the tick interval. Zero or
// negative leaves the default in place, the same convention
// Reprojector.SetBounds takes. It exists for tests, which need a cycle they can
// complete without waiting out production minutes, and for a deployment that
// wants a different cycle time.
func (s *PersonalSweeper) SetBounds(batch int, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch > 0 {
		s.batch = batch
	}
	if interval > 0 {
		s.interval = interval
	}
}

// Run sweeps on a ticker until ctx is cancelled.
func (s *PersonalSweeper) Run(ctx context.Context) {
	s.mu.Lock()
	interval := s.interval
	s.mu.Unlock()

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep(ctx)
		}
	}
}

// Sweep runs exactly one tick: load the population if this is the start of a
// cycle, re-drive the next batch of identities, and publish the walk's position
// to every registered personal lens's health entry. Exported so a test can
// drive a deterministic pass instead of waiting on a ticker.
func (s *PersonalSweeper) Sweep(ctx context.Context) {
	if !s.r.hasPersonal() {
		// Nothing registered: every reprojection would be a no-op and every
		// health write would have nobody to land on, so the tick would be a
		// whole-population Core-KV listing bought for nothing. Checked per tick
		// rather than latched, because a lens registering later — during boot,
		// or on a hot install — must simply resume the walk. Deliberately NOT
		// the drain's registry-COMPLETENESS gate: this sweep is the healer for
		// the lens that registers late, so holding it until every lens is
		// present would be the mechanism waiting on the thing it repairs.
		return
	}
	if !s.ensurePopulation(ctx) {
		// Either the listing failed — already logged, and the next tick retries
		// — or this cell has no identities. Neither publishes progress: a cursor
		// written over a population nobody could read would claim coverage the
		// sweep does not have.
		return
	}
	ids, cycleCompletedAt, ok := s.claim()
	if !ok {
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			// A cancelled context abandons the rest of the batch. The cursor has
			// already advanced past it, so the abandoned identities are covered
			// on the NEXT cycle rather than this one — the same "re-work, never
			// a skipped segment" direction the unpersisted cursor takes, since
			// the only thing that cancels this context is the process going away.
			return
		}
		// One actor at a time, all its lenses in sequence, through the
		// Reprojector's own per-lens walk: no new concurrency, and a per-lens
		// failure logs, raises that lens's Health fault, and continues.
		s.r.ReprojectNow(ctx, id)
	}
	s.publishProgress(ctx, ids[len(ids)-1], cycleCompletedAt)
}

// ensurePopulation lists the identity census when a cycle is starting, and
// reports whether there is anything to sweep.
//
// The listing is the same shape the auth-plane sweep takes for its anchors: a
// filtered Core-KV key listing with limit 0, so the whole population arrives in
// one page (a page boundary would hand the walk a partial census that reads
// like a complete one), and every returned key is re-parsed rather than
// trusted — the filter is a cost mechanism, the parse is what decides a key is
// really an identity root.
//
// Tombstoned identities are NOT filtered out. Doing so would cost one Core KV
// read per identity per cycle, and it would filter out precisely the case that
// most needs sweeping: a deleted identity's personal frames must go empty, and
// reprojecting it is what publishes that retraction.
func (s *PersonalSweeper) ensurePopulation(ctx context.Context) bool {
	s.mu.Lock()
	if s.loaded {
		s.mu.Unlock()
		return true
	}
	s.mu.Unlock()

	filter := substrate.VertexPrefix + "." + projection.PersonalActorType + ".*"
	keys, _, err := s.coreKV.ListKeysFilter(ctx, filter, "", 0)
	if err != nil {
		slog.Warn("grantchange: personal sweep could not list the identity population — this cycle is skipped and the next tick retries",
			"filter", filter, "err", err)
		return false
	}
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		vtxType, id, ok := substrate.ParseVertexKey(k)
		if !ok || vtxType != projection.PersonalActorType || id == "" {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		// Deliberately NOT cached. The cache is invalidated by a cycle wrapping,
		// and a walk over nothing never wraps — so caching this answer would
		// make a cell that boots before its first identity exists stay unswept
		// for the life of the process. Re-listing an empty population per tick
		// is the cheapest listing there is.
		return false
	}

	s.mu.Lock()
	s.population = ids
	s.loaded = true
	s.cursor = ""
	s.mu.Unlock()
	return true
}

// claim takes this tick's identities and advances the cursor past them. The
// returned time is non-zero only on the tick that closes a cycle.
func (s *PersonalSweeper) claim() (ids []string, cycleCompletedAt time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.population) == 0 {
		return nil, time.Time{}, false
	}
	start := resumeAt(s.population, s.cursor)
	end := start + s.batch
	wrapped := false
	if end >= len(s.population) {
		end = len(s.population)
		wrapped = true
	}
	ids = append([]string(nil), s.population[start:end]...)
	if !wrapped {
		s.cursor = ids[len(ids)-1]
		return ids, time.Time{}, true
	}
	// The walk reached the end: the cycle is closed, so stamp it, drop the
	// cached census — the next tick re-lists, which is how an identity created
	// mid-cycle enters the walk — and reset the cursor to the top.
	s.cycleCompletedAt = time.Now()
	s.cursor = ""
	s.population = nil
	s.loaded = false
	return ids, s.cycleCompletedAt, true
}

// publishProgress fans the walk's position out to every registered personal
// lens's own health entry, mirroring how the drain reports its faults.
//
// Per-lens rather than to one process-level aggregate for the same reason: an
// operator reading one personal lens's health is asking whether that lens's
// rows are converging, and the answer depends on a backstop that lives outside
// the lens. A cursor only a process-wide entry carried would leave every lens
// looking like it has no standing healer at all.
//
// The queue depth rides along because it is the gauge Increment 1's drain
// exposes and nothing read: a mass grant change shows up as a depth that stops
// falling, and this is the only place that number reaches an operator.
func (s *PersonalSweeper) publishProgress(ctx context.Context, cursor string, cycleCompletedAt time.Time) {
	depth := uint64(s.r.QueueDepth())
	for _, ruleID := range s.r.registeredRuleIDs() {
		// Re-read the registry immediately before each write, the same posture
		// reprojectActor takes and for the same reason: the batch this reports on
		// spans a real window, and a lens deleted inside it has had its Health
		// entry removed already. The progress write is a read-modify-PUT, so
		// writing to a deleted lens would RE-CREATE that entry — an orphan row
		// claiming an active lens that no longer exists, with no TTL behind it to
		// reap it. Unlike the reprojection, a single progress write is short
		// enough that this one check closes the window; there is no second check
		// after it.
		p, live := s.r.registered(ruleID)
		if !live {
			continue
		}
		if err := p.SetPersonalSweepProgress(ctx, cursor, cycleCompletedAt, depth); err != nil {
			slog.Warn("grantchange: personal sweep could not record its progress on a lens's health entry — the sweep itself is unaffected, its observability is not",
				"ruleId", ruleID, "cursor", cursor, "err", err)
		}
	}
}

// Cursor and CycleCompletedAt report the walk's position for a test or a
// caller that wants it without reading Health KV back.
func (s *PersonalSweeper) Cursor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

// CycleCompletedAt is when the walk last closed a full cycle over the identity
// population — the zero time until the first one closes.
func (s *PersonalSweeper) CycleCompletedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cycleCompletedAt
}

// resumeAt is where a cursored walk over a sorted list continues: the first
// entry after the cursor, wrapping to the start when the cursor has fallen off
// the end. It is a binary search for the POSITION rather than a lookup of the
// key, so a cursor whose entry has left the list still resumes in the right
// place instead of restarting the cycle.
//
// The same shape the auth-plane sweeper's own round-robin uses. It is copied
// rather than shared because it is six lines and sharing it would mean either
// exporting an internal from a package this one has no other reason to import,
// or minting a helper package for one function.
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
