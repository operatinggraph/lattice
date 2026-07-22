package weaver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// laneConsumerPrefix prefixes a lane-1 durable name: weaver-target-<targetId>.
const laneConsumerPrefix = "weaver-target-"

// Config parameterizes the engine. Bucket/stream names default to the
// platform-standard values; callers (cmd/weaver, tests) override only what
// they need.
type Config struct {
	// CoreKVBucket backs the registry source (vtx.meta.> CDC). Default "core-kv".
	CoreKVBucket string
	// WeaverTargetsBucket is the shared target-Lens projection bucket lane-1
	// consumes (Contract #10 §10.2). Default "weaver-targets".
	WeaverTargetsBucket string
	// WeaverStateBucket holds the §10.3 in-flight marks. Default "weaver-state".
	WeaverStateBucket string
	// HealthKVBucket holds the Contract #5 heartbeat (health.weaver.<instance>)
	// and the per-consumer pause-state entries. Default "health-kv" — matches
	// internal/bootstrap.HealthKVBucket; cmd/weaver may override from there.
	HealthKVBucket string
	// CoreSchedulesStream is the platform message-scheduling stream the lane-3
	// temporal consumer binds to and the Actuator publishes @at schedules on
	// (Contract #10 §10.4). Default "core-schedules" — kept literal, like the
	// bucket defaults, so internal/weaver does not import internal/bootstrap.
	CoreSchedulesStream string
	// ActorKey is the identity:weaver service-actor vertex key the Actuator
	// submits under (vtx.identity.<id>, the primordial weaver service actor).
	ActorKey string
	// Lane is the ops lane remediation ops are submitted on (the ops.<lane>
	// subject token — a single dot-free token, validated at Start). Default
	// "system".
	Lane string
	// HeartbeatEvery is the Contract #5 heartbeat cadence. The 10s default is
	// the §5.6/NFR-O1 production cadence; a shorter value lets a test observe
	// heartbeat-driven state without waiting out production timing. Values <= 0
	// take the default.
	HeartbeatEvery time.Duration
	// MarkLease is the §10.3 in-flight mark lease: an episode whose mark
	// outlives this window is presumed dead and reclaimed by the reconciler
	// sweep (the mark's per-key TTL backstops at markTTLBackstopFactor × this
	// value). Sized ≫ expected remediation latency so expiry is rare. Must be
	// >= 1s (NATS per-key TTL floor). Default 30m; values <= 0 take the
	// default.
	MarkLease time.Duration
	// SweepInterval is the reconciler sweep cadence: each pass level-reconciles
	// every weaver-state mark against its current row and reclaims expired
	// leases and orphaned marks. Values <= 0 take the 1m default; values above
	// MarkLease are clamped down to it (with a Warn), so the sweep always
	// observes an expired lease while the key still exists — before the
	// markTTLBackstopFactor × MarkLease per-key TTL deletes it unseen.
	SweepInterval time.Duration
	// SweepOrphanWarmup gates the sweep's orphan legs (target not installed;
	// playbook lacks the gap column) for this long after engine start. It is a
	// registry-replay-readiness proxy: the registry source replays
	// meta.weaverTarget history asynchronously and exposes no replay-done
	// signal, so an early "uninstalled"/"column dropped" verdict may be replay
	// lag — deleting on it would orphan a live gap. Expired-lease reclaim and
	// level clearing are never gated. Values <= 0 take the 5m default; values
	// below SweepInterval are clamped up to it (a warm-up shorter than one
	// tick gates nothing).
	SweepOrphanWarmup time.Duration
	// ReclaimBackoffBase is the base interval for the reclaim backoff applied to
	// the collapse-only actions (assignTask/triggerLoom and the Augur's
	// proposedOp): the sweep paces repeat reclaims of the SAME still-open
	// episode at base × 2^(count-1), capped at ReclaimBackoffCap, instead of
	// re-firing every pass. The FIRST reclaim still fires at lease-expiry
	// (count 0→1 ⇒ base), so lost-dispatch recovery is unchanged; ordinary
	// (non-Augur) directOp/external gaps are never backed off (their reclaim
	// re-dispatch is the intended bounded retry). Values <= 0 default to
	// MarkLease (the first repeat then paces at the same cadence as the lease).
	ReclaimBackoffBase time.Duration
	// ReclaimBackoffCap caps the reclaim backoff interval. Defaults to 24h — the
	// Contract #4 §4.3 op-tracker TTL horizon, beyond which a duplicate re-dispatch
	// would no longer collapse on the tracker anyway. Values <= 0 take the default.
	ReclaimBackoffCap time.Duration
	// Instance distinguishes this engine process; it is one segment of the
	// per-boot registry-source durable name (a separate per-boot nonce is
	// what actually guarantees full-replay uniqueness — see registry.go — so
	// Instance MAY be stable across restarts), and it is the key segment for
	// this process's Contract #5 heartbeat (health.weaver.<instance>).
	// Per-consumer pause-state entries (health.weaver.consumer-state.<name>)
	// are consumer-scoped, not instance-scoped, so they survive a restart
	// under a new Instance. MUST be unique per Weaver process sharing a
	// health-kv bucket, and MUST be a single dot-free token (validated at
	// Start — a dot would fragment the health key space and break the
	// durable name). Defaults to "<hostname>-<pid>-<NanoID>" (sanitized) when
	// empty.
	Instance string
	// Logger is the diagnostics sink. Defaults to slog.Default().
	Logger *slog.Logger
}

// instanceSegmentReplacer sanitizes a hostname for use as a KV key segment
// (Contract #5 health.weaver.<instance>): '.' would be read as a key-segment
// separator and is replaced with '-'.
var instanceSegmentReplacer = strings.NewReplacer(".", "-")

// defaultInstance returns a host/pid-attributable, per-construction-unique
// instance id ("<hostname>-<pid>-<NanoID>", sanitized for KV key segments)
// used when Config.Instance is empty. The hostname+pid prefix makes an
// auto-generated health.weaver.<instance> document attributable to the
// process that wrote it (Contract #5); the registry-source durable's own
// per-boot uniqueness comes from a separate nonce (see registry.go), not from
// this value, so an operator-set stable Instance is equally safe.
func defaultInstance() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "weaver"
	}
	host = instanceSegmentReplacer.Replace(host)
	suffix, err := substrate.NewNanoID()
	if err != nil {
		suffix = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return host + "-" + strconv.Itoa(os.Getpid()) + "-" + suffix
}

func (c *Config) withDefaults() {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.CoreKVBucket == "" {
		c.CoreKVBucket = "core-kv"
	}
	if c.WeaverTargetsBucket == "" {
		c.WeaverTargetsBucket = "weaver-targets"
	}
	if c.WeaverStateBucket == "" {
		c.WeaverStateBucket = "weaver-state"
	}
	if c.HealthKVBucket == "" {
		// Literal default mirrors internal/bootstrap.HealthKVBucket; kept literal
		// (like the other bucket defaults) so internal/weaver does not import
		// internal/bootstrap.
		c.HealthKVBucket = "health-kv"
	}
	if c.CoreSchedulesStream == "" {
		c.CoreSchedulesStream = "core-schedules"
	}
	if c.Lane == "" {
		c.Lane = "system"
	}
	if c.MarkLease <= 0 {
		c.MarkLease = defaultMarkLease
	}
	if c.MarkLease < time.Second {
		// NATS per-key TTL floor: weaver-state is provisioned LimitMarkerTTL >= 1s,
		// and the server rejects per-key TTLs under a second; the mark's TTL is
		// derived from the lease, so the lease shares the floor.
		c.MarkLease = time.Second
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = defaultSweepInterval
	}
	if c.SweepInterval > c.MarkLease {
		// The sweep is the only actor that can re-attempt an expired mark, and
		// it can only act while the key still exists — the per-key TTL deletes
		// it at markTTLBackstopFactor × MarkLease. A sweep slower than the
		// lease could let every expired mark be TTL-deleted unobserved,
		// degrading every recovery to the unwedge-without-re-attempt backstop.
		c.Logger.Warn("weaver: SweepInterval exceeds MarkLease; clamping",
			"sweepInterval", c.SweepInterval, "markLease", c.MarkLease)
		c.SweepInterval = c.MarkLease
	}
	if c.SweepOrphanWarmup <= 0 {
		c.SweepOrphanWarmup = defaultSweepOrphanWarmup
	}
	if c.SweepOrphanWarmup < c.SweepInterval {
		c.SweepOrphanWarmup = c.SweepInterval
	}
	if c.ReclaimBackoffBase <= 0 {
		c.ReclaimBackoffBase = c.MarkLease
	}
	if c.ReclaimBackoffCap <= 0 {
		c.ReclaimBackoffCap = defaultReclaimBackoffCap
	}
	if c.ReclaimBackoffCap < c.ReclaimBackoffBase {
		// A cap below the base would invert the backoff (the first repeat would be
		// clamped below the lease). Clamp the cap up so the floor is always >= base.
		c.ReclaimBackoffCap = c.ReclaimBackoffBase
	}
	if c.Instance == "" {
		c.Instance = defaultInstance()
	}
}

// Engine is the Weaver convergence engine: the meta.weaverTarget registry
// source (Sensorium's definition source), per-target lane-1 KV-CDC consumers
// (Sensorium), Evaluator L1/L2 + Strategist, and the fire-and-forget Actuator.
// Durable dispatch state lives ONLY in weaver-state (the §10.3 mark — the
// in-flight check is a KV read, never an in-memory map); the engine's
// in-memory caches hold derived/registry state rebuilt by CDC replay (the
// registry source, the consumer-state cache).
type Engine struct {
	cfg              Config
	conn             *substrate.Conn
	logger           *slog.Logger
	source           *targetSource
	marks            *markStore
	sweep            *sweeper
	temporal         *temporalStats
	act              *actuator
	supervisor       *substrate.ConsumerSupervisor
	states           *healthkv.ConsumerStateCache
	issues           *issueCache
	rowSubjectPrefix string
	disabled         *disabledTargetSet
	shadow           *shadowStats
	contraction      *contractionStats
	oscillation      *oscillationStats
	admission        *admissionScheduler

	mu sync.Mutex
	// targets is the last-applied desired lane-1 consumer set (targetId →
	// applied spec fingerprint), diffed on each reconcile against the live
	// registry.
	targets map[string]specFingerprint

	ctx context.Context
}

// disabledTargetSet is the engine's in-memory cache of currently-disabled
// targetIds: the per-row/per-firing dispatch-skip guards
// (handleRow, scheduleFreshness, handleFiredTimer) read this set rather than
// KV-Get the `<targetId>.__control` marker on every message — the same
// "in-memory cache rebuilt from durable backing" line the registry source and
// healthkv.ConsumerStateCache already draw. The `<targetId>.__control` weaver-state
// marker is the durable truth (it is what survives a restart, seeding this
// set at Start via seedDisabledTargets); Disable/Enable/Revoke update both the
// marker and this set synchronously so the two never disagree mid-process.
type disabledTargetSet struct {
	mu  sync.RWMutex
	ids map[string]struct{}
}

func newDisabledTargetSet() *disabledTargetSet {
	return &disabledTargetSet{ids: make(map[string]struct{})}
}

func (d *disabledTargetSet) has(targetID string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.ids[targetID]
	return ok
}

func (d *disabledTargetSet) set(targetID string, disabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if disabled {
		d.ids[targetID] = struct{}{}
	} else {
		delete(d.ids, targetID)
	}
}

// isTargetDisabled reports whether targetID currently carries the
// `<targetId>.__control` dispatch-skip marker, per the in-memory
// disabled-set — no per-message KV read.
func (e *Engine) isTargetDisabled(targetID string) bool {
	return e.disabled.has(targetID)
}

// specFingerprint is the subset of a ConsumerSpec's config that, if it
// changes, requires the durable to be recreated (Reset). Hooks (Handler/
// Classify/Probe/Health) are intentionally excluded — they are refreshed via
// UpdateSpec without recreating the durable.
type specFingerprint struct {
	stream        string
	filterSubject string
	deliverPolicy substrate.DeliverPolicy
	deliverGroup  string
}

func fingerprintOf(spec substrate.ConsumerSpec) specFingerprint {
	return specFingerprint{
		stream:        spec.Stream,
		filterSubject: spec.FilterSubject,
		deliverPolicy: spec.DeliverPolicy,
		deliverGroup:  spec.DeliverGroup,
	}
}

// NewEngine constructs an Engine over conn.
func NewEngine(conn *substrate.Conn, cfg Config) *Engine {
	cfg.withDefaults()
	issues := newIssueCache()
	e := &Engine{
		cfg:              cfg,
		conn:             conn,
		logger:           cfg.Logger,
		marks:            newMarkStore(conn, cfg.WeaverStateBucket, cfg.MarkLease, cfg.Instance),
		temporal:         &temporalStats{},
		act:              newActuator(conn, cfg.Lane, cfg.ActorKey, cfg.Logger),
		supervisor:       substrate.NewConsumerSupervisor(conn),
		states:           healthkv.NewConsumerStateCache(),
		issues:           issues,
		rowSubjectPrefix: "$KV." + cfg.WeaverTargetsBucket + ".",
		disabled:         newDisabledTargetSet(),
		shadow:           newShadowStats(),
		contraction:      newContractionStats(),
		oscillation:      newOscillationStats(),
		admission:        newAdmissionScheduler(),
		targets:          make(map[string]specFingerprint),
	}
	e.source = newTargetSource(conn, cfg.CoreKVBucket, cfg.Instance, issues, cfg.Logger)
	e.sweep = newSweeper(e, cfg.SweepInterval, cfg.SweepOrphanWarmup, cfg.ReclaimBackoffBase, cfg.ReclaimBackoffCap)
	return e
}

// Start runs the engine until ctx is cancelled. It (1) validates the config
// tokens that feed KV keys, subjects, and durable names; (2) starts the
// Contract #5 heartbeater and the reconciler sweep; (3) starts the static
// lane-3 temporal consumer (the fixed weaver-temporal durable on
// core-schedules); (4) starts the registry source whose load/update callbacks
// reconcile the per-target lane-1 consumers; (5) seeds one reconcile (a
// restart must not depend solely on source callbacks to bring consumers up);
// (6) blocks on ctx.
func (e *Engine) Start(ctx context.Context) (err error) {
	if !singleTokenPattern.MatchString(e.cfg.Instance) {
		return fmt.Errorf("weaver: Instance %q must be a single dot-free token (it is a Contract #5 health key segment and a durable-name segment; must match %s)",
			e.cfg.Instance, singleTokenPattern.String())
	}
	if !singleTokenPattern.MatchString(e.cfg.Lane) {
		return fmt.Errorf("weaver: Lane %q must be a single dot-free subject token (ops are published to ops.<lane>; must match %s)",
			e.cfg.Lane, singleTokenPattern.String())
	}
	// An empty ActorKey would publish remediation ops under actor:"" — the
	// Processor rejects those off-stream with no signal. Fail loud here; there
	// is no sensible default identity key.
	if e.cfg.ActorKey == "" {
		return fmt.Errorf("weaver: ActorKey required")
	}
	e.ctx = ctx

	defer func() {
		if err != nil {
			e.supervisor.Stop()
		}
	}()

	if err := e.seedDisabledTargets(ctx); err != nil {
		return fmt.Errorf("weaver: seed disabled-target set: %w", err)
	}

	hb := newHeartbeater(e.conn, e.cfg.HealthKVBucket, e.cfg.Instance, e.cfg.HeartbeatEvery,
		e.states, e.issues, e.source, e.marks, e.sweep, e.temporal, e.shadow, e.contraction, e.admission, e.logger)
	go hb.run(ctx)
	// A startup warm sweep runs once so a cold start does not wait a full
	// interval; the recurring cadence is the durable @every sweep schedule
	// (armed below) picked up by the weaver-sweep durable, replacing the
	// in-process ticker (§10.4 recurring temporal lane, brainstorm #47).
	go e.sweep.warmPass(ctx)

	if err := e.supervisor.Add(ctx, e.temporalSpec()); err != nil {
		return fmt.Errorf("weaver: start temporal consumer: %w", err)
	}
	if err := e.supervisor.Add(ctx, e.sweepSpec()); err != nil {
		return fmt.Errorf("weaver: start recurring-sweep consumer: %w", err)
	}
	if err := e.armSweepSchedule(ctx); err != nil {
		return fmt.Errorf("weaver: %w", err)
	}

	e.source.setLoadCallback(func(*Target) { e.reconcileConsumers() })
	e.source.setUpdateCallback(func(_, _ *Target) { e.reconcileConsumers() })
	if err := e.source.start(ctx); err != nil {
		return fmt.Errorf("weaver: start target source: %w", err)
	}
	e.reconcileConsumers()

	e.logger.Info("weaver engine started",
		"coreKV", e.cfg.CoreKVBucket, "targets", e.cfg.WeaverTargetsBucket,
		"state", e.cfg.WeaverStateBucket, "lane", e.cfg.Lane)
	<-ctx.Done()
	e.supervisor.Stop()
	return nil
}

// supervisedHandler adapts a Decision-returning handler to the supervisor's
// SupervisedHandler signature. The handler already encodes every outcome as a
// Decision, so the error channel is always nil and Classify is never exercised
// on this path (a nil Classify = always transient is the accepted posture).
func supervisedHandler(h func(context.Context, substrate.Message) substrate.Decision) substrate.SupervisedHandler {
	return func(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
		return h(ctx, msg), nil
	}
}

// targetSpec describes one lane-1 consumer: a per-target supervised KV-CDC
// durable on the weaver-targets backing stream, filtered to the target's key
// prefix, DeliverLastPerSubject (the Refractor CDC pattern — never a raw KV
// watcher).
func (e *Engine) targetSpec(targetID string) substrate.ConsumerSpec {
	name := laneConsumerPrefix + targetID
	return substrate.ConsumerSpec{
		Name:          name,
		Stream:        "KV_" + e.cfg.WeaverTargetsBucket,
		FilterSubject: e.rowSubjectPrefix + targetID + ".>",
		DeliverPolicy: substrate.DeliverLastPerSubject,
		Handler:       supervisedHandler(e.handleRow),
		Health:        healthkv.NewConsumerSink(e.conn, e.cfg.HealthKVBucket, "weaver", name, e.states),
		Logger:        e.logger,
	}
}

// reconcileConsumers diffs the desired lane-1 consumer set (the registered
// targets) against the last-applied set, driving the supervisor:
//
//   - a target newly registered → Add weaver-target-<targetId>;
//   - a target removed/revoked → Remove (the supervisor stops the pump AND
//     deletes the JetStream durable — an un-pumped server-side durable IS a
//     leak; re-add replays via DeliverLastPerSubject, safe because the mark
//     CAS + level-reconcile make the handler idempotent) and its health-sink
//     entry is deleted;
//   - a registered target whose desired spec differs from the running one →
//     UpdateSpec + Reset (delete-and-recreate), never silently unchanged.
//
// The per-target filter is name-derived and stable, so the Reset branch is
// mechanically reachable only if a future spec field changes; the diff is
// written generically so such a change is caught. The WHOLE pass runs under
// e.mu so concurrent passes serialize.
//
// A failed Add/Remove/Reset raises a Health KV issue (cleared on a later
// success for the same target) — the discrepancy never rides silently on the
// heartbeat. The retry is the next reconcile pass (the next registry event);
// there is no retry ticker here.
func (e *Engine) reconcileConsumers() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx == nil || e.ctx.Err() != nil {
		return
	}
	desired := make(map[string]struct{})
	for _, id := range e.source.targetIDs() {
		desired[id] = struct{}{}
	}
	for id := range desired {
		spec := e.targetSpec(id)
		fp := fingerprintOf(spec)
		applied, running := e.targets[id]
		if !running {
			if err := e.supervisor.Add(e.ctx, spec); err != nil {
				e.logger.Error("weaver target consumer add failed", "targetId", id, "err", err)
				e.issues.set(issueKeyConsumer(id), "error", "ConsumerReconcileError",
					"target "+id+": lane-1 consumer add failed: "+err.Error())
				continue
			}
			e.issues.clear(issueKeyConsumer(id))
			e.targets[id] = fp
			e.logger.Info("weaver target consumer added", "targetId", id, "durable", spec.Name)
			continue
		}
		if applied == fp {
			continue
		}
		if err := e.supervisor.UpdateSpec(spec.Name, func(s *substrate.ConsumerSpec) { *s = spec }); err != nil {
			e.logger.Error("weaver target consumer update-spec failed", "targetId", id, "err", err)
			e.issues.set(issueKeyConsumer(id), "error", "ConsumerReconcileError",
				"target "+id+": lane-1 consumer update-spec failed: "+err.Error())
			continue
		}
		if err := e.supervisor.Reset(e.ctx, spec.Name); err != nil {
			e.logger.Error("weaver target consumer reset failed", "targetId", id, "err", err)
			e.issues.set(issueKeyConsumer(id), "error", "ConsumerReconcileError",
				"target "+id+": lane-1 consumer reset failed: "+err.Error())
			continue
		}
		e.issues.clear(issueKeyConsumer(id))
		e.targets[id] = fp
		e.logger.Info("weaver target consumer reset", "targetId", id, "durable", spec.Name)
	}
	for id := range e.targets {
		if _, want := desired[id]; want {
			continue
		}
		name := laneConsumerPrefix + id
		if err := e.supervisor.Remove(e.ctx, name); err != nil {
			e.logger.Error("weaver target consumer remove failed", "targetId", id, "err", err)
			e.issues.set(issueKeyConsumer(id), "error", "ConsumerReconcileError",
				"target "+id+": lane-1 consumer remove failed (durable may leak until the next reconcile): "+err.Error())
			continue
		}
		e.issues.clear(issueKeyConsumer(id))
		delete(e.targets, id)
		sink := healthkv.NewConsumerSink(e.conn, e.cfg.HealthKVBucket, "weaver", name, e.states)
		if err := sink.Delete(e.ctx); err != nil {
			e.logger.Error("weaver target consumer health-state cleanup failed", "targetId", id, "durable", name, "err", err)
		}
		// A target leaving the registry is a genuine uninstall — delete its
		// `<targetId>.__control` dispatch-skip marker and prune the in-memory
		// disabled-set, so a re-install of the same targetId does not silently
		// come up disabled (the marker would otherwise be re-seeded at the next
		// Start and orphan-leak in weaver-state).
		if err := e.marks.setDisabled(e.ctx, id, false); err != nil {
			e.logger.Error("weaver target control-marker cleanup failed", "targetId", id, "err", err)
		}
		e.disabled.set(id, false)
		e.logger.Info("weaver target consumer removed", "targetId", id, "durable", name)
	}
}

func issueKeyConsumer(targetID string) string { return "consumer:" + targetID }
