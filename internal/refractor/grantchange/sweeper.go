package grantchange

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
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

// PersonalContentHealInterval is how often a sweep CYCLE republishes rows
// rather than the frame alone (personal-lens-delta-publication-design.md §4.4).
//
// Every pass publishes the authoritative keyset frame, which is the product of
// both inclusion gates and therefore re-asks exactly what the healer was put
// there to re-ask. Rows are content, and content converges on the CDC path; a
// content cycle is the bounded backstop for the row whose upsert was dropped or
// whose event the derivation missed on a device that stays connected and never
// re-hydrates.
//
// A day is that backstop's window, at one whole-actor republish per actor per
// day.
//
// It is a bound on the heal, not a period: rows are republished by a whole
// CYCLE, so a cycle is a content cycle when the one after it would close past
// this window. The heal therefore lands AT LEAST once per interval and at most
// once per cycle. A deployment whose cycle is already longer than the interval
// — population / batch × tick interval ≥ 24 h, roughly 7,200 identities at the
// default bounds — makes every cycle a content cycle, and the frames-only
// saving there is nil.
const PersonalContentHealInterval = 24 * time.Hour

// CoreKVLister is the one Core KV capability the sweep needs: enumerate the
// identity population. Narrow on purpose — the sweeper reads nothing else, and
// a one-method surface is what lets its unit tests hand it a scripted
// population instead of a live bucket.
type CoreKVLister interface {
	ListKeysFilter(ctx context.Context, filter, cursor string, limit int) ([]string, string, error)
}

// HealthKVLister is the one Health KV capability the sweep needs: enumerate the
// live Refractor instance heartbeats, so a pass can say how many processes are
// running.
//
// A separate named type from CoreKVLister despite the identical method set,
// because the two are different buckets answering different questions and a
// single name would let a caller thread one where the other belongs — a mistake
// whose symptom is an instance count derived from the identity population.
//
// NIL IS A REFUSAL, not a default: a sweeper with no health lister reports its
// instance count UNREADABLE, and the personal derivation licence refuses on
// that. The grant-change edge is process-local, so a consumer that cannot tell
// how many processes exist must not narrow.
type HealthKVLister interface {
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
	r        *Reprojector
	coreKV   CoreKVLister
	healthKV HealthKVLister

	// nudge asks Run for a pass now rather than at the next tick. Buffered at
	// ONE and sent to non-blockingly, which is the whole coalescing rule: a
	// hundred lenses activating in a burst cost one extra pass, and a nudge
	// arriving while a pass is already running is kept rather than dropped, so
	// the loop's next iteration runs one MORE pass whose StartedAt is later than
	// the registration that sent it. That is the property the licence's conjunct
	// 3 needs (StartedAt after the lens's RegisteredAt) and the reason the
	// buffer is a slot rather than a boolean the running pass could clear.
	//
	// Never closed: Run's exit is ctx.Done, and a closed channel in the select
	// would spin.
	nudge chan struct{}

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

	// lastContentCycleStart is when a CONTENT cycle last began, and contentCycle
	// is whether the cycle now in progress is one. Both are latched together at
	// ensurePopulation's re-list — the site that actually starts a cycle — and
	// read by every pass of that cycle, so a cycle's kind cannot change under
	// the batches that make it up.
	//
	// The latch compares this against where the cycle now starting will END
	// (elapsed + the projected cycle length), so the heal lands at least once
	// per PersonalContentHealInterval and at most once per cycle.
	//
	// LIFETIME: the zero lastContentCycleStart makes the FIRST cycle after boot
	// a content cycle, so a process that just started republishes rows once
	// before settling into frames. Neither survives the process: a restart costs
	// one content pass per actor over the next cycle, which is the same
	// re-work-never-skip direction the unpersisted cursor takes.
	lastContentCycleStart time.Time
	contentCycle          bool

	// contentCycleRequested is a standing ask for the NEXT cycle to carry
	// content, whatever the clock says — RequestContentCycle's whole state.
	//
	// LIFETIME: set by a caller, CONSUMED by the latch at ensurePopulation's
	// re-list (cleared there whether or not the clock had already decided the
	// same way), so one request buys exactly one content cycle. It does not
	// survive the process, and does not need to: a restart makes the first
	// cycle a content cycle on the zero lastContentCycleStart anyway, which is
	// strictly more than the request was asking for.
	contentCycleRequested bool

	// now is the clock the content-cycle latch reads. Defaulted to time.Now by
	// NewPersonalSweeper; a test replaces it to cross a day's interval without
	// waiting one out.
	now func() time.Time

	// verdict is what the LAST PASS achieved, replaced wholesale under this
	// same lock at the end of every pass and read live by the personal
	// derivation licence (pipeline.PersonalHealerVerdict).
	//
	// LIFETIME: created by the first completed pass — which a lens's own
	// registration kicks off (Nudge), rather than the first tick, or the whole
	// personal plane would run on the enumerator for a full interval after every
	// restart, precisely when the backlog is deepest. Zero at process start, and zero
	// REFUSES the licence. Not persisted: a restart re-earns the narrowing on
	// its first pass. Replaced wholesale rather than field-by-field so a reader
	// can never see one pass's failure count beside another pass's clock.
	verdict pipeline.PersonalHealerVerdict

	batch    int
	interval time.Duration
}

// NewPersonalSweeper builds the sweeper over the Reprojector whose registry it
// shares, the Core KV handle it enumerates identities from, and the Health KV
// handle it counts live Refractor instances on.
//
// healthKV is a positional parameter rather than an option precisely so every
// caller has to decide: nil is legal and means "this deployment's cardinality is
// unknown", which the personal derivation licence reads as a refusal. A harness
// exercising the sweep alone passes nil and gets exactly today's behaviour plus
// a narrowing it was never going to earn.
func NewPersonalSweeper(r *Reprojector, coreKV CoreKVLister, healthKV HealthKVLister) *PersonalSweeper {
	s := &PersonalSweeper{
		r:        r,
		coreKV:   coreKV,
		healthKV: healthKV,
		nudge:    make(chan struct{}, 1),
		batch:    DefaultPersonalSweepBatch,
		interval: DefaultPersonalSweepInterval,
		now:      time.Now,
	}
	if r != nil {
		// A registration is what turns a pass from a no-op into the verdict the
		// licence reads, so the registry tells the sweep when one lands. Wired
		// here rather than by the host: the pair is the Reprojector this
		// sweeper was built over, and a second wiring step in cmd/refractor
		// would be one more thing that can be forgotten with no symptom but a
		// slower plane.
		r.setPersonalRegisteredHook(s.Nudge)
	}
	return s
}

// Nudge asks the run loop for a pass as soon as it can take one, coalescing
// with any nudge already pending. Safe on a nil sweeper and safe before Run
// starts — a nudge sent early is simply the first thing the loop sees.
//
// Two callers, for two halves of one registration. The Reprojector fires it
// when a lens lands in the registry this sweep walks; cmd/refractor fires it
// again once that lens's derivation licence has been asserted, because the
// licence's RegisteredAt is stamped AFTER the registry insert and a pass that
// began in between would be refused by conjunct 3 for the very lens it was
// kicked off for. The second nudge cannot be lost to the first: the buffered
// slot is drained before a pass runs, so a nudge arriving during one is kept.
func (s *PersonalSweeper) Nudge() {
	if s == nil || s.nudge == nil {
		return
	}
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// RequestContentCycle asks for the next cycle this sweeper starts to republish
// every swept actor's rows rather than the authoritative frame alone. It
// satisfies pipeline.RebuildCompleteSink.
//
// One request buys exactly ONE content cycle: the flag is consumed by the latch
// at ensurePopulation's re-list, so a burst of requests — fifteen personal
// lenses rebuilt by one package install — costs one cycle between them, and the
// cycle after it returns to frames-only on the ordinary clock.
//
// It does NOT interrupt the cycle in progress. A cycle's kind is latched where
// the cycle starts and read unchanged by every pass of it, precisely so the
// population cannot be half republished; a request arriving mid-cycle is
// therefore honoured by the next one, within one cycle length rather than within
// PersonalContentHealInterval. That is the whole gain being asked for.
//
// Nil-safe: a deployment with no standing healer has nowhere to record the ask,
// and a caller must not have to know whether it wired one.
func (s *PersonalSweeper) RequestContentCycle() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.contentCycleRequested = true
	s.mu.Unlock()
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

// Run sweeps once immediately, then on every nudge and every tick until ctx is
// cancelled.
//
// The NUDGE is what makes the promptness real, and the immediate pass alone is
// not. The pass verdict is what the personal derivation licence rests on and its
// zero value refuses — but Sweep returns without recording one while no personal
// lens is registered, and the host starts this loop before any lens activates.
// So an immediate pass on its own records nothing, and the first verdict would
// land a full interval later: every personal lens on the relation-blind
// enumerator for that window, which is exactly when the backlog is deepest and
// the enumerator most expensive. A registration therefore kicks a pass (Nudge),
// and the immediate pass covers the other wiring order — a host that registers
// its lenses before starting the loop, which is what every unit fixture in this
// package does.
//
// The nudged and ticked arms run the same pass. Only the ticked arm re-reads the
// cadence, because only it schedules by it.
func (s *PersonalSweeper) Run(ctx context.Context) {
	interval := s.tickInterval()

	s.Sweep(ctx)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.nudge:
			// Received BEFORE the pass runs, which is what lets a registration
			// arriving mid-pass keep the slot and earn a further pass of its
			// own — see Nudge. Draining it after the pass would coalesce a
			// registration this pass could not have covered into a pass that
			// started before it.
			s.Sweep(ctx)
		case <-t.C:
			s.Sweep(ctx)
			// The ticker FOLLOWS SetBounds. Every verdict advertises
			// tickInterval() as the cadence the licence measures staleness
			// against, so a bound changed after Run started would otherwise
			// leave the advertised window describing a clock the loop no longer
			// keeps — and the direction of that drift is a licence that stays
			// granted through a cadence it is not actually running at. Reset
			// after the pass rather than in a second goroutine: the read is one
			// mutex acquisition per interval, and there is exactly one owner of
			// this ticker.
			if live := s.tickInterval(); live != interval {
				interval = live
				t.Reset(interval)
			}
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
		// the drain's registry-COMPLETENESS gate: this sweep is what covers the
		// lens that registers late — its next pass frames that lens's actors,
		// and its content cycle republishes their rows within
		// PersonalContentHealInterval — so holding it until every lens is
		// present would be the mechanism waiting on the thing it repairs.
		//
		// No verdict is recorded, and none is owed: a plane with no personal
		// lens has nothing for a licence to speak about. A lens that registers
		// LATER earns its licence on the first pass after it, and until then
		// reads the standing zero verdict, which refuses.
		return
	}

	// The deployment's cardinality is read ONCE here, on the healer's own clock,
	// and never on the event path — the licence's conjunct 5 asks a question
	// about the deployment, and asking it per CDC event would put a Health-KV
	// listing on the path this whole narrowing exists to shorten.
	// Stamped BEFORE any work, and carried on the verdict, because a lens that
	// registers into an already-swept plane must not inherit a pass that began
	// before it joined the registry: the licence compares this against the
	// lens's own registration time.
	startedAt := time.Now()

	instances, instancesReadable := s.countInstances(ctx)

	populated, readable := s.ensurePopulation(ctx)
	if !readable {
		// The listing failed — already logged, and the next tick retries. This
		// is the case the verdict exists for: a healer that cannot enumerate its
		// own population is not covering it, whatever it did to the actors it
		// last saw, and the licence must read that as "nothing is standing
		// behind these rows" rather than inheriting the previous pass's clean
		// answer. Published, too: an operator reading one personal lens must be
		// able to see why fifteen of them stopped narrowing.
		s.recordVerdict(ctx, pipeline.PersonalHealerVerdict{
			StartedAt:             startedAt,
			CompletedAt:           time.Now(),
			Interval:              s.tickInterval(),
			PopulationReadable:    false,
			InstanceCount:         instances,
			InstanceCountReadable: instancesReadable,
			EdgeSpansDeployment:   GrantChangeEdgeSpansDeployment,
		}, "", time.Time{})
		return
	}

	verdict := pipeline.PersonalHealerVerdict{
		StartedAt:             startedAt,
		Interval:              s.tickInterval(),
		PopulationReadable:    true,
		InstanceCount:         instances,
		InstanceCountReadable: instancesReadable,
		EdgeSpansDeployment:   GrantChangeEdgeSpansDeployment,
	}

	// Decided ONCE for the whole pass, from the kind the cycle latched at its
	// start. A frames-only pass publishes each swept actor's authoritative
	// keyset frame and no row; a content cycle republishes the rows too. Read
	// here rather than per identity so one pass cannot straddle two answers.
	scope := s.passScope()

	var cursor string
	var cycleCompletedAt time.Time
	if populated {
		var ids []string
		var ok bool
		ids, cycleCompletedAt, ok = s.claim()
		if ok {
			for _, id := range ids {
				if ctx.Err() != nil {
					// A cancelled context abandons the rest of the batch. The
					// cursor has already advanced past it, so the abandoned
					// identities are covered on the NEXT cycle rather than this
					// one — the same "re-work, never a skipped segment"
					// direction the unpersisted cursor takes, since the only
					// thing that cancels this context is the process going away.
					//
					// No verdict is recorded on this path either: the process is
					// going away, and a partial pass stamped as a completed one
					// would hand the licence a clean answer over work that did
					// not happen.
					return
				}
				// One actor at a time, all its lenses in sequence, through the
				// Reprojector's own per-lens walk: no new concurrency, and a
				// per-lens failure logs, raises that lens's Health fault, and
				// continues — while COUNTING, which is what makes this a
				// verdict rather than a stamp.
				verdict.Attempted++
				verdict.Failed += s.r.ReprojectNow(ctx, id, scope)
			}
			cursor = ids[len(ids)-1]
		}
	}
	// An EMPTY population is a clean pass, not a failed one: a cell with no
	// identities has no personal rows for anything to leave stale, and refusing
	// the licence there would put fifteen lenses on the enumerator to heal
	// nothing.
	verdict.CompletedAt = time.Now()
	s.recordVerdict(ctx, verdict, cursor, cycleCompletedAt)
}

// passScope reports what this pass publishes: every row on a content cycle,
// the frame alone otherwise.
//
// Nothing reads what a pass published. The derivation licence's healer conjunct
// is a liveness statement, the verdict counts reprojections that failed rather
// than rows that landed, and no health surface distinguishes a row from a
// frame — so a frames-only pass is the same pass to every consumer of it.
func (s *PersonalSweeper) passScope() pipeline.PublishScope {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contentCycle {
		return pipeline.ScopeAll()
	}
	return pipeline.ScopeNone()
}

// tickInterval reports the cadence this sweeper runs on, which rides on every
// verdict so the licence's staleness window is measured against the clock the
// healer actually keeps rather than against a constant the consumer would have
// to hold in step with a bound SetBounds can move.
func (s *PersonalSweeper) tickInterval() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interval
}

// recordVerdict replaces the pass verdict wholesale under the sweeper's own
// lock and fans it out, with the walk's position, to every registered personal
// lens's health entry.
//
// The in-memory store happens FIRST and unconditionally. The health write is
// observability and can fail against a KV blip; the verdict is what the
// narrowing licence reads, and a pass whose verdict was lost to a failed health
// write would leave the licence on the PREVIOUS pass's answer — which is the
// stale-clean direction this whole mechanism exists to refuse.
func (s *PersonalSweeper) recordVerdict(ctx context.Context, v pipeline.PersonalHealerVerdict, cursor string, cycleCompletedAt time.Time) {
	s.mu.Lock()
	s.verdict = v
	s.mu.Unlock()
	s.publishProgress(ctx, cursor, cycleCompletedAt, v.Summary())
}

// Verdict reports what this sweeper's last pass achieved. It is the accessor
// cmd/refractor injects into every personal pipeline as the licence's live
// conjuncts, and the zero value — no pass has completed — refuses.
//
// The cadence is stamped HERE, at read time, rather than carried from the pass
// that produced the verdict. The licence measures staleness as K × Interval
// against a pass that has not happened, so the interval it needs is the one the
// loop is running on NOW — and a bound changed after the last pass would
// otherwise leave the standing verdict advertising the old one until a pass it
// is waiting for arrives to correct it. The unsafe direction is a cadence
// SHORTENED after a pass: the stale longer interval would hold the licence open
// past the window the sweeper is actually keeping. Run's ticker reads the same
// value, so the advertised window and the loop's cadence are one number.
func (s *PersonalSweeper) Verdict() pipeline.PersonalHealerVerdict {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.verdict
	v.Interval = s.interval
	return v
}

// countInstances reports how many Refractor instances are live in Health KV,
// and whether the count could be read at all.
//
// One filtered key listing per pass, no value reads: the question is how many
// heartbeats exist, and health.InstanceKeyFilter's single-token wildcard matches
// exactly one key per instance. The prefix comes from the package that WRITES it
// rather than being respelled here, because a reader that spelled its own would
// drift silently into counting zero and refusing forever.
//
// It fails CLOSED — a listing error, or no health handle at all, reports
// unreadable — and the two staleness directions are asymmetric in a way worth
// stating where the read happens. A CRASHED instance whose entry has not yet
// expired over-counts, so the licence refuses: pessimisation, safe, and pinned
// as correct by a test so a later change that trusts freshness has something
// standing in front of it. A newly started instance that has not yet written its
// first heartbeat under-counts, and the licence stays on for that window: this
// is why the count is a backstop and the build-time gate
// (scripts/lint-refractor-single-instance.go) is the primary defence.
func (s *PersonalSweeper) countInstances(ctx context.Context) (count int, readable bool) {
	if s.healthKV == nil {
		return 0, false
	}
	keys, _, err := s.healthKV.ListKeysFilter(ctx, health.InstanceKeyFilter, "", 0)
	if err != nil {
		slog.Warn("grantchange: personal sweep could not count the live Refractor instances — the personal derivation licence refuses while the deployment's cardinality is unknown",
			"filter", health.InstanceKeyFilter, "err", err)
		return 0, false
	}
	if len(keys) == 0 {
		// AN EMPTY LISTING IS NOT AN EMPTY DEPLOYMENT. This code is running
		// inside a live Refractor, so a census that finds no Refractor has
		// contradicted itself: the answer zero can only mean the census is
		// broken — the Health bucket purged or re-provisioned under a running
		// process, heartbeat writes failing while listings succeed, a
		// permission change, a drift in the key shape this filter matches.
		//
		// The direction matters. Two instances whose heartbeats are not landing
		// produce exactly this reading on BOTH of them, and a zero treated as
		// readable would license the narrowing on both while the grant-change
		// edge reaches neither — the precise fail-open conjunct 5 exists to
		// close. So an empty listing is UNREADABLE, not empty; the licence
		// asserts the same thing again on its own side, so an edit here cannot
		// reopen it alone.
		slog.Warn("grantchange: the live Refractor instance census returned NO instances, which cannot be true of a process performing the census — treating the count as unreadable and refusing the personal derivation licence",
			"filter", health.InstanceKeyFilter)
		return 0, false
	}
	return len(keys), true
}

// ensurePopulation lists the identity census when a cycle is starting, and
// reports both whether there is anything to sweep and whether the population
// could be READ at all.
//
// Two answers rather than one because they are different facts with opposite
// consequences for the narrowing licence, and the single boolean this returned
// before conflated them: a cell with no identities is a clean pass over nothing,
// while a listing this sweep could not perform means nothing is standing behind
// any personal row on the plane.
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
func (s *PersonalSweeper) ensurePopulation(ctx context.Context) (populated, readable bool) {
	s.mu.Lock()
	if s.loaded {
		s.mu.Unlock()
		return true, true
	}
	s.mu.Unlock()

	filter := substrate.VertexPrefix + "." + projection.PersonalActorType + ".*"
	keys, _, err := s.coreKV.ListKeysFilter(ctx, filter, "", 0)
	if err != nil {
		slog.Warn("grantchange: personal sweep could not list the identity population — this cycle is skipped and the next tick retries",
			"filter", filter, "err", err)
		return false, false
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
		return false, true
	}

	s.mu.Lock()
	s.population = ids
	s.loaded = true
	s.cursor = ""
	// The cycle's kind is latched HERE, where a cycle actually starts, and read
	// unchanged by every pass of it — claim()'s wrap stamps a cycle's END, and a
	// kind decided per pass would flip mid-cycle the moment the latch was
	// stamped, leaving the rest of the population frames-only on the very cycle
	// meant to carry its content.
	//
	// The decision is made against where this cycle ENDS, not where it starts.
	// Rows are only republished by a whole cycle, so asking "has the interval
	// already elapsed?" bought a heal every cycleLength × ceil(interval /
	// cycleLength) — 46 h for a 23 h cycle. Asking instead whether the NEXT
	// cycle would close past the window makes the heal AT LEAST once per
	// PersonalContentHealInterval and at most once per cycle. Batch and interval
	// are read in this same locked section as the latch they feed, so a
	// concurrent SetBounds cannot land between the projection and the decision.
	//
	// Three ways in, one latch. The CLOCK is the standing backstop; the zero
	// lastContentCycleStart is boot, which has sent nothing yet; and an explicit
	// REQUEST is a caller that knows the devices are behind and cannot wait a
	// day for the clock to say so — a rebuild whose replay published nothing.
	// The request is consumed here, at the same site and under the same lock,
	// so it buys exactly one cycle whether or not the clock had already agreed.
	batch, interval := s.batch, s.interval
	cycleLength := projectedCycleLength(len(ids), batch, interval)
	firstSinceBoot := s.lastContentCycleStart.IsZero()
	requested := s.contentCycleRequested
	s.contentCycleRequested = false
	var elapsed time.Duration
	if !firstSinceBoot {
		elapsed = s.now().Sub(s.lastContentCycleStart)
	}
	contentCycle := firstSinceBoot || requested || elapsed+cycleLength >= PersonalContentHealInterval
	s.contentCycle = contentCycle
	if contentCycle {
		s.lastContentCycleStart = s.now()
	}
	s.mu.Unlock()
	if contentCycle {
		// Said out loud with the numbers the decision was made on, because the
		// content heal's window is a claim about a full cycle: a population
		// large enough that a cycle outruns the interval makes EVERY cycle a
		// content cycle, and nothing else on the plane would say so.
		since := elapsed.String()
		if firstSinceBoot {
			since = "first since boot"
		}
		if requested {
			since += " (requested)"
		}
		slog.Info("grantchange: personal sweep is starting a CONTENT cycle — this cycle republishes every swept actor's rows; the cycles between it publish the authoritative frame alone",
			"elapsedSinceLastContentCycle", since,
			"projectedCycleLength", cycleLength.String(),
			"population", len(ids),
			"batch", batch,
			"interval", interval.String(),
			"contentHealInterval", PersonalContentHealInterval.String())
	}
	return true, true
}

// projectedCycleLength is how long the cycle now starting will take to walk the
// whole population: one pass per batch of identities, one tick interval between
// passes. It is what the content latch measures against, so that the decision is
// about where the cycle ENDS.
//
// A zero or negative input reports zero, which reduces the latch to "has the
// interval already elapsed" — the honest answer when the cadence is unknown.
func projectedCycleLength(population, batch int, interval time.Duration) time.Duration {
	if population <= 0 || batch <= 0 || interval <= 0 {
		return 0
	}
	passes := (population + batch - 1) / batch
	return time.Duration(passes) * interval
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
// falling, and this is the only place that number reaches an operator. The
// VERDICT rides along for the sharper version of the same reason: the cursor
// advances on a pass in which every reprojection failed, so an entry carrying a
// moving cursor and nothing else reads healthy through the exact condition an
// operator is looking for.
//
// An EMPTY cursor means "this pass reached no batch" — an unreadable population
// — and the reporter leaves the stored position alone rather than erasing the
// one the last real pass earned. The verdict always overwrites.
func (s *PersonalSweeper) publishProgress(ctx context.Context, cursor string, cycleCompletedAt time.Time, verdict string) {
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
		if err := p.SetPersonalSweepProgress(ctx, cursor, cycleCompletedAt, depth, verdict); err != nil {
			slog.Warn("grantchange: personal sweep could not record its progress on a lens's health entry — the sweep itself is unaffected, its observability is not",
				"ruleId", ruleID, "cursor", cursor, "verdict", verdict, "err", err)
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
