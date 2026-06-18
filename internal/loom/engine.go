package loom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// Operation types the engine submits for the lifecycle event-only ops
// (Contract #10 §10.9). The trigger op (StartLoomPattern) is submitted by the
// caller, never by the engine.
const (
	opCompletePattern = "CompletePattern"
	opFailPattern     = "FailPattern"
	// opCreateTask is the op a userTask step submits to assign its bound op to
	// the instance subject (Contract #10 §10.5).
	opCreateTask = "CreateTask"
)

// userTaskGrantTTL is the expiry horizon set on a userTask's task grant. A
// userTask wait is unbounded by design (§10.6), so the grant outlives any
// realistic human response window; the grant authorizes the user's bound op,
// whose commit auto-completes the task and advances the cursor.
const userTaskGrantTTL = 30 * 24 * time.Hour

// triggerDurable is the fixed always-on trigger consumer's durable name
// (Contract #10 §10.9). It is independent of completionDomains.
const triggerDurable = "loom-trigger"

// triggerSubject is the single subject the trigger consumer filters on.
const triggerSubject = "events.loom.patternStarted"

// Config parameterizes the engine. Bucket/stream names default to the
// platform-standard values; callers (cmd/loom, tests) override only what they
// need.
type Config struct {
	// CoreKVBucket backs the pattern source (vtx.meta.> CDC). Default "core-kv".
	CoreKVBucket string
	// LoomStateBucket holds the per-instance cursors + token index. Default "loom-state".
	LoomStateBucket string
	// EventsStream is the core-events stream the trigger + per-domain completion
	// consumers attach to. Default "core-events".
	EventsStream string
	// HealthKVBucket holds the Contract #5 heartbeat (health.loom.<instance>)
	// and the per-consumer pause-state entries. Default "health-kv" — matches
	// internal/bootstrap.HealthKVBucket; cmd/loom may override from there.
	HealthKVBucket string
	// ActorKey is the identity:loom service-actor vertex key the Actuator
	// submits under (vtx.identity.<id>, the primordial loom service actor).
	ActorKey string
	// Lane is the ops lane systemOps + lifecycle ops are submitted on. Default "system".
	Lane string
	// StepTimeout is the per-step deadline: a step whose committed event is not
	// seen within this window trips the step-deadline-exceeded handler (the
	// off-stream failed/rejected backstop, §10.6). Must be >= 1s (NATS per-key
	// TTL floor). Default 60s.
	StepTimeout time.Duration
	// CreateTaskTimeout is the bounded creation-deadline a userTask step arms
	// while it waits for its CreateTask to commit (the §10.6 deadline+probe
	// applied to the task-creation path). It backstops a CreateTask that is
	// rejected or lost — without it a userTask whose CreateTask never commits
	// parks forever. It is sized ≫ any CreateTask commit latency (NOT a human
	// response window): once the probe confirms the task vertex exists, the
	// deadline is disarmed and the wait for the human becomes unbounded
	// (§10.6). Must be >= 1s (NATS per-key TTL floor). Default 60s.
	CreateTaskTimeout time.Duration
	// HeartbeatEvery is the Contract #5 heartbeat cadence. The 10s default is
	// the §5.6/NFR-O1 production cadence; a shorter value lets a test observe
	// heartbeat-driven state without waiting out production timing. Values <= 0
	// take the default.
	HeartbeatEvery time.Duration
	// Instance distinguishes this engine process; it suffixes the per-boot
	// pattern-source durable so each boot replays the installed pattern set,
	// and it is the key segment for this process's Contract #5 heartbeat
	// (health.loom.<instance>) and per-consumer pause-state entries
	// (health.loom.<instance>.consumer.<name>). MUST be unique per Loom
	// process sharing a health-kv bucket — see docs/components/loom.md for the
	// shared-bucket collision consequences. Defaults to
	// "<hostname>-<pid>-<NanoID>" (sanitized) when empty.
	Instance string
	// Logger is the diagnostics sink. Defaults to slog.Default().
	Logger *slog.Logger
}

// instanceSegmentReplacer sanitizes a hostname for use as a KV key segment
// (Contract #5 health.loom.<instance> / health.loom.<instance>.consumer.<name>):
// '.' would be read as a key-segment separator and is replaced with '-'.
var instanceSegmentReplacer = strings.NewReplacer(".", "-")

// defaultInstance returns a host/pid-attributable, per-construction-unique
// instance id ("<hostname>-<pid>-<NanoID>", sanitized for KV key segments) used
// when Config.Instance is empty. The hostname+pid prefix makes an
// auto-generated health.loom.<instance> document attributable to the process
// that wrote it (Contract #5); the NanoID suffix preserves the existing
// per-boot uniqueness the pattern-source durable relies on (multiple Engine
// constructions in one process — e.g. a restart in the same test/host process —
// must not collide on the same patternSourceDurable name, see source.go). See
// docs/components/loom.md for why an explicit, cluster-unique Instance is
// preferred for production multi-replica deployments.
func defaultInstance() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "loom"
	}
	host = instanceSegmentReplacer.Replace(host)
	suffix, err := substrate.NewNanoID()
	if err != nil {
		suffix = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return host + "-" + strconv.Itoa(os.Getpid()) + "-" + suffix
}

func (c *Config) withDefaults() {
	if c.CoreKVBucket == "" {
		c.CoreKVBucket = "core-kv"
	}
	if c.LoomStateBucket == "" {
		c.LoomStateBucket = "loom-state"
	}
	if c.EventsStream == "" {
		c.EventsStream = "core-events"
	}
	if c.HealthKVBucket == "" {
		// Literal default mirrors internal/bootstrap.HealthKVBucket; kept literal
		// (like CoreKVBucket/LoomStateBucket/EventsStream) so internal/loom does
		// not import internal/bootstrap.
		c.HealthKVBucket = "health-kv"
	}
	if c.Lane == "" {
		c.Lane = "system"
	}
	if c.StepTimeout <= 0 {
		c.StepTimeout = 60 * time.Second
	}
	if c.StepTimeout < time.Second {
		// NATS per-key TTL floor: loom-state is provisioned LimitMarkerTTL >= 1s,
		// so a sub-second deadline would not arm a marker and the off-stream
		// failed terminal would never fire. Clamp up rather than silently degrade.
		c.StepTimeout = time.Second
	}
	if c.CreateTaskTimeout <= 0 {
		c.CreateTaskTimeout = 60 * time.Second
	}
	if c.CreateTaskTimeout < time.Second {
		c.CreateTaskTimeout = time.Second
	}
	if c.Instance == "" {
		c.Instance = defaultInstance()
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Engine is the Loom orchestration engine: pattern loader (Sensorium's
// definition source), the fixed trigger consumer, per-domain completion
// consumers (Sensorium), Transition Engine, and Actuator. It holds NO in-memory
// correlation index — completions correlate by a durable token.<token> GET on
// loom-state (Contract #10 §10.6), so any replica resolves any token.
type Engine struct {
	cfg        Config
	conn       *substrate.Conn
	logger     *slog.Logger
	source     *patternSource
	state      *stateStore
	relay      *relay
	supervisor *substrate.ConsumerSupervisor
	states     *consumerStateCache

	mu sync.Mutex
	// domains is the last-applied desired per-domain consumer set, diffed on
	// each reconcile against the live binding registry. The value is the
	// comparable config fingerprint of the applied spec, so a future spec-shape
	// change is detected and drives a Reset; the supervisor owns the pump
	// lifecycle, so the engine tracks only what the diff needs.
	domains map[string]specFingerprint

	ctx context.Context
}

// specFingerprint is the subset of a ConsumerSpec's config that, if it changes,
// requires the durable to be recreated (Reset). Hooks (Handler/Classify/Probe/
// Health) are intentionally excluded — they are refreshed via UpdateSpec without
// recreating the durable.
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
	e := &Engine{
		cfg:        cfg,
		conn:       conn,
		logger:     cfg.Logger,
		state:      newStateStore(conn, cfg.LoomStateBucket),
		relay:      newRelay(conn, cfg.LoomStateBucket, cfg.Logger),
		supervisor: substrate.NewConsumerSupervisor(conn),
		states:     newConsumerStateCache(),
		domains:    make(map[string]specFingerprint),
	}
	e.source = newPatternSource(conn, cfg.CoreKVBucket, cfg.Instance, cfg.Logger)
	return e
}

// Start runs the engine until ctx is cancelled. It (1) starts the fixed trigger
// consumer on events.loom.patternStarted; (2) starts the pattern source whose
// load/update callbacks reconcile the per-domain completion consumers; (3)
// blocks on ctx. There is no startup index rebuild and no watch-suspend gate
// (Crash-safety invariant 3 removed, §10.6): a redelivered completion resolves
// against the durable token.<token> pointer regardless of engine age.
func (e *Engine) Start(ctx context.Context) (err error) {
	e.ctx = ctx

	defer func() {
		if err != nil {
			e.supervisor.Stop()
		}
	}()

	for _, spec := range []substrate.ConsumerSpec{e.triggerSpec(), e.relaySpec(), e.deadlineSpec()} {
		if err := e.supervisor.Add(ctx, spec); err != nil {
			return fmt.Errorf("loom: add %s consumer: %w", spec.Name, err)
		}
	}

	hb := newHeartbeater(e.conn, e.cfg.HealthKVBucket, e.cfg.LoomStateBucket, e.cfg.Instance, e.cfg.HeartbeatEvery, e.states, e.logger)
	go hb.run(ctx)

	e.source.setLoadCallback(func(p *Pattern) { e.reconcileConsumers() })
	e.source.setUpdateCallback(func(_, _ *Pattern) { e.reconcileConsumers() })
	if err := e.source.start(ctx); err != nil {
		return fmt.Errorf("loom: start pattern source: %w", err)
	}
	// Seed one reconcile at startup. The pinned-instance leg of the desired set
	// is independent of the source callbacks: a restart with live pinned
	// instances but ZERO loaded patterns fires no load/update callback, and
	// without this seed no loom-<domain> pump would ever attach — permanently
	// wedging an in-flight wait (e.g. a userTask whose human acts after the
	// restart). When patterns did load, this is a cheap no-op diff.
	e.reconcileConsumers()

	e.logger.Info("loom engine started",
		"coreKV", e.cfg.CoreKVBucket, "loomState", e.cfg.LoomStateBucket, "lane", e.cfg.Lane)
	<-ctx.Done()
	e.supervisor.Stop()
	return nil
}

// supervisedHandler adapts an existing Decision-returning handler to the
// supervisor's SupervisedHandler signature. The wrapped handlers already encode
// every outcome as a Decision, so the error channel is always nil and Classify
// is never exercised on these paths.
func supervisedHandler(h func(context.Context, substrate.Message) substrate.Decision) substrate.SupervisedHandler {
	return func(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
		return h(ctx, msg), nil
	}
}

// healthSinkFor builds a per-consumer HealthSink that persists pause-state to
// health-kv and feeds the engine's consumer-state cache.
func (e *Engine) healthSinkFor(name string) substrate.HealthSink {
	return newConsumerHealthSink(e.conn, e.cfg.HealthKVBucket, e.cfg.Instance, name, e.states)
}

// triggerSpec describes the fixed trigger consumer (loom-trigger) on
// events.loom.patternStarted.
func (e *Engine) triggerSpec() substrate.ConsumerSpec {
	return substrate.ConsumerSpec{
		Name:          triggerDurable,
		Stream:        e.cfg.EventsStream,
		FilterSubject: triggerSubject,
		DeliverPolicy: substrate.DeliverAll,
		Handler:       supervisedHandler(e.handleTrigger),
		Health:        e.healthSinkFor(triggerDurable),
		Logger:        e.logger,
	}
}

// domainSpec describes a per-domain completion consumer (loom-<domain>) on
// events.<domain>.>.
func (e *Engine) domainSpec(domain string) substrate.ConsumerSpec {
	name := "loom-" + domain
	return substrate.ConsumerSpec{
		Name:          name,
		Stream:        e.cfg.EventsStream,
		FilterSubject: "events." + domain + ".>",
		DeliverPolicy: substrate.DeliverAll,
		Handler:       supervisedHandler(e.handleCompletion),
		Health:        e.healthSinkFor(name),
		Logger:        e.logger,
	}
}

// relaySpec describes the command-outbox relay (loom-outbox-relay), a KV-CDC
// consumer on the loom-state backing stream filtered to outbox.>. Its
// publish/delete-failure paths return NakWithDelay (bounded cadence), so
// RedeliveryDelay is left zero to take substrate.DefaultRedeliveryDelay (5s).
func (e *Engine) relaySpec() substrate.ConsumerSpec {
	return substrate.ConsumerSpec{
		Name:          relayDurable,
		Stream:        "KV_" + e.cfg.LoomStateBucket,
		FilterSubject: "$KV." + e.cfg.LoomStateBucket + "." + outboxPrefix + ">",
		DeliverPolicy: substrate.DeliverAll,
		Handler:       e.relay.handle,
		Health:        e.healthSinkFor(relayDurable),
		Logger:        e.logger,
	}
}

// deadlineSpec describes the deadline watcher (loom-deadline), a KV-CDC consumer
// on the loom-state backing stream filtered to deadline.>.
func (e *Engine) deadlineSpec() substrate.ConsumerSpec {
	subjPrefix := "$KV." + e.cfg.LoomStateBucket + "."
	return substrate.ConsumerSpec{
		Name:          deadlineDurable,
		Stream:        "KV_" + e.cfg.LoomStateBucket,
		FilterSubject: subjPrefix + deadlinePrefix + ">",
		DeliverPolicy: substrate.DeliverAll,
		Handler: supervisedHandler(func(ctx context.Context, msg substrate.Message) substrate.Decision {
			return e.handleDeadline(ctx, subjPrefix, msg)
		}),
		Health: e.healthSinkFor(deadlineDurable),
		Logger: e.logger,
	}
}

// --- Trigger consumer (Contract #10 §10.9) ---------------------------------

// triggerBody is the patternStarted event Loom reads (Contract #10 §10.9:
// instanceId = the StartLoomPattern requestId). The business fields ride the
// Event's `payload` object (the outbox publishes the full Event envelope); they
// are read from the body, never from the subject.
type triggerBody struct {
	Payload struct {
		InstanceID string `json:"instanceId"`
		PatternRef string `json:"patternRef"`
		SubjectKey string `json:"subjectKey"`
	} `json:"payload"`
}

func (e *Engine) handleTrigger(ctx context.Context, msg substrate.Message) substrate.Decision {
	if len(msg.Body) == 0 {
		return substrate.Ack
	}
	var tb triggerBody
	if err := json.Unmarshal(msg.Body, &tb); err != nil {
		e.logger.Warn("loom: patternStarted body unparseable; dropping", "err", err)
		return substrate.Ack
	}
	t := tb.Payload
	if t.InstanceID == "" || t.PatternRef == "" || t.SubjectKey == "" {
		e.logger.Warn("loom: patternStarted body incomplete; dropping",
			"instanceId", t.InstanceID, "patternRef", t.PatternRef, "subjectKey", t.SubjectKey)
		return substrate.Ack
	}
	// instanceId is the StartLoomPattern requestId — a NanoID (§10.9), which is
	// dot-free by construction. A dotted id (e.g. "X.pattern") would collide with
	// the instance.<id>.pattern pin namespace and corrupt the health counter and
	// the pinned-domain enumeration. Redelivery cannot fix a malformed id, so
	// drop it (Ack), loudly.
	if !substrate.IsValidNanoID(t.InstanceID) {
		e.logger.Warn("loom: patternStarted instanceId is not a NanoID; dropping",
			"instanceId", t.InstanceID, "patternRef", t.PatternRef)
		return substrate.Ack
	}

	// Idempotency on instanceId: a redelivered trigger finds the cursor present
	// and skips (Contract #10 §10.9) — unless the prior delivery crashed between
	// createInstance and the step-0 submission, in which case it resumes.
	existing, err := e.state.getInstance(ctx, t.InstanceID)
	if err != nil {
		e.logger.Error("loom: trigger instance read failed; nak", "instanceId", t.InstanceID, "err", err)
		return substrate.Nak
	}
	if existing != nil {
		if existing.Status == StatusRunning && existing.PendingToken == "" {
			// The instance exists but no step was ever submitted: a prior handler
			// crashed/Nak'd after the createInstance batch committed but before the
			// step-0 transition. Plain-Acking here would wedge the instance forever
			// (its pin would sit in the reconcile union with no step in flight), so
			// resume step 0 from the PINNED pattern instead.
			return e.resumeStepZero(ctx, existing)
		}
		// Terminal, or running with a step in flight — a true duplicate.
		return substrate.Ack
	}

	patternID := patternIDFromRef(t.PatternRef)
	pattern, ok := e.source.get(patternID)
	if !ok {
		// The pattern is not loaded yet (the CDC source replays asynchronously).
		// Nak so the trigger is redelivered once the pattern registers.
		e.logger.Warn("loom: patternStarted for unloaded pattern; nak for redelivery",
			"patternRef", t.PatternRef, "instanceId", t.InstanceID)
		return substrate.Nak
	}

	inst := &Instance{
		InstanceID: t.InstanceID,
		PatternRef: t.PatternRef,
		SubjectKey: t.SubjectKey,
		Cursor:     0,
		Status:     StatusRunning,
	}
	// The definition binds at instance start: createInstance pins a full copy of
	// the pattern (instance.<id>.pattern) atomically with the cursor. Every
	// subsequent step resolution reads the pin, so a pattern update mid-flight
	// affects NEW instances only.
	if err := e.state.createInstance(ctx, inst, pattern); err != nil {
		e.logger.Error("loom: create instance failed; nak", "instanceId", t.InstanceID, "err", err)
		return substrate.Nak
	}
	// The pin is committed; make sure its completion domains have running
	// consumers before the step is submitted. This closes the trigger-vs-
	// reconcile race where a concurrent reconcile listed the pins BEFORE this
	// trigger's pin landed and removed a consumer this instance needs.
	e.ensureDomainConsumers(pattern)
	return e.runStepZero(ctx, inst, pattern)
}

// resumeStepZero finishes a trigger whose prior delivery committed the
// createInstance batch (instance + pin) but never submitted step 0
// (status=running, pendingToken empty). It re-runs the step-0 sequence against
// the PINNED pattern — the pin necessarily exists, written atomically with the
// instance. Race-safe under concurrent redeliveries: both racers derive the
// same deterministic step-0 token, and the transition batch's CreateOnly token
// guard rejects the loser, whose next redelivery then sees a non-empty
// pendingToken and plain-Acks.
func (e *Engine) resumeStepZero(ctx context.Context, inst *Instance) substrate.Decision {
	pattern, err := e.state.getPinnedPattern(ctx, inst.InstanceID)
	if err != nil {
		if errors.Is(err, errPatternPinMissing) {
			// Same posture as advance: an operator-visible failed terminal, never a
			// Nak loop on an unrecoverable invariant break.
			if ferr := e.fail(ctx, inst, "", "pattern pin missing"); ferr != nil {
				e.logger.Error("loom: fail on missing pin failed; nak", "instanceId", inst.InstanceID, "err", ferr)
				return substrate.Nak
			}
			return substrate.Ack
		}
		e.logger.Error("loom: resume pin read failed; nak", "instanceId", inst.InstanceID, "err", err)
		return substrate.Nak
	}
	e.logger.Info("loom: resuming step 0 for instance with no submitted step",
		"instanceId", inst.InstanceID, "patternId", pattern.PatternID)
	e.ensureDomainConsumers(pattern)
	return e.runStepZero(ctx, inst, pattern)
}

// runStepZero evaluates guards forward from the instance's cursor and either
// completes immediately (every remaining guard false) or submits the first
// runnable step, mapping each outcome to the trigger consumer's Decision.
func (e *Engine) runStepZero(ctx context.Context, inst *Instance, pattern *Pattern) substrate.Decision {
	runCursor, completed, err := e.advanceToRunnableStep(ctx, inst, pattern)
	if err != nil {
		e.logger.Error("loom: step-0 guard evaluation failed; nak", "instanceId", inst.InstanceID, "err", err)
		return substrate.Nak
	}
	if completed {
		// Step 0's guard — and every subsequent guard — is false: there is nothing
		// to do (a legitimate "verify-info, already satisfied" run). Complete
		// immediately instead of submitting a step. runCursor == len(pattern.Steps)
		// here (advanceToRunnableStep's exhaustion return), matching the cursor a
		// normal completion would persist.
		inst.Cursor = runCursor
		if err := e.complete(ctx, inst, pattern, ""); err != nil {
			e.logger.Error("loom: complete-on-trigger failed; nak", "instanceId", inst.InstanceID, "err", err)
			return substrate.Nak
		}
		e.logger.Info("loom instance completed on trigger (all guards skipped)",
			"instanceId", inst.InstanceID, "patternId", pattern.PatternID)
		return substrate.Ack
	}
	inst.Cursor = runCursor
	if err := e.submitStep(ctx, inst, pattern, ""); err != nil {
		e.logger.Error("loom: submit step 0 failed; nak", "instanceId", inst.InstanceID, "err", err)
		return substrate.Nak
	}
	e.logger.Info("loom instance started", "instanceId", inst.InstanceID, "patternId", pattern.PatternID)
	return substrate.Ack
}

// --- Per-domain completion consumers (D2) ----------------------------------

// reconcileConsumers diffs the desired per-domain completion-consumer set
// against the last-applied set, driving the supervisor's Add / Remove / Reset.
// The desired set is the UNION of two legs:
//
//   - the binding registry aggregated across the current pattern snapshot
//     (which domains today's definitions reference); and
//   - the domains of the pinned patterns of LIVE instances (instance.*.pattern
//     in loom-state — pins are deleted in the terminal batch, so the listing is
//     exactly the live set).
//
// The union is what makes an in-flight instance survive its pattern being
// removed/updated-away: the instance completes under its pinned definition, and
// its completion domain's consumer stays up until the last live instance
// pinning that domain reaches terminal (complete/fail trigger a reconcile, so
// the drained consumer is then torn down). Diff outcomes:
//
//   - a domain newly in the union → Add loom-<domain>;
//   - a domain in neither leg (no current pattern references it AND no live
//     instance pins it) → Remove (the supervisor stops the pump AND deletes
//     the JetStream durable — an un-pumped server-side durable IS the leak F6
//     forbids);
//   - a domain in both whose desired spec differs from the running one → Reset
//     (delete-and-recreate), never silently unchanged.
//
// The per-domain filter (events.<domain>.>) is name-derived and stable, so the
// Reset branch is mechanically reachable only if a future spec field changes;
// the diff is written generically so such a change is caught.
//
// If the pinned-domain enumeration fails, the diff proceeds with the snapshot
// leg only for Adds but SKIPS the Remove phase: tearing down a consumer a live
// instance still needs would orphan its completion, while deferring a teardown
// to the next reconcile is harmless.
//
// The WHOLE pass — desired-set computation AND diff application — runs under
// e.mu, so concurrent passes serialize: a pass can never apply a desired set
// computed before another pass's teardown (which would resurrect a just-removed
// domain from stale data). pinnedDomains does KV I/O under the mutex; that is
// an accepted tradeoff at current scale (the live-instance set is small and
// reconciles fire on callbacks/terminals, not per-message).
func (e *Engine) reconcileConsumers() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx == nil || e.ctx.Err() != nil {
		return
	}
	desired := bindingRegistry(e.source.snapshot())
	pinned, pinErr := e.state.pinnedDomains(e.ctx, e.logger)
	if pinErr != nil {
		if e.ctx.Err() != nil || errors.Is(pinErr, context.Canceled) || errors.Is(pinErr, context.DeadlineExceeded) {
			// Shutdown, not a fault: the engine is going away, so there is nothing
			// to reconcile and nothing alarming to report.
			e.logger.Info("loom: reconcile aborted by shutdown", "err", pinErr)
			return
		}
		e.logger.Error("loom: pinned-domain enumeration failed; deferring consumer teardown", "err", pinErr)
	} else {
		for d := range pinned {
			desired[d] = struct{}{}
		}
	}
	for d := range desired {
		spec := e.domainSpec(d)
		fp := fingerprintOf(spec)
		applied, running := e.domains[d]
		if !running {
			if err := e.supervisor.Add(e.ctx, spec); err != nil {
				e.logger.Error("loom domain consumer add failed", "domain", d, "err", err)
				continue
			}
			e.domains[d] = fp
			e.logger.Info("loom domain consumer added", "domain", d, "durable", spec.Name)
			continue
		}
		if applied == fp {
			continue
		}
		// Desired config diverged from the running durable — recreate cleanly.
		if err := e.supervisor.UpdateSpec(spec.Name, func(s *substrate.ConsumerSpec) { *s = spec }); err != nil {
			e.logger.Error("loom domain consumer update-spec failed", "domain", d, "err", err)
			continue
		}
		if err := e.supervisor.Reset(e.ctx, spec.Name); err != nil {
			e.logger.Error("loom domain consumer reset failed", "domain", d, "err", err)
			continue
		}
		e.domains[d] = fp
		e.logger.Info("loom domain consumer reset", "domain", d, "durable", spec.Name)
	}
	if pinErr != nil {
		// Without the pinned leg the union is incomplete — a Remove here could
		// tear down a consumer a live instance still needs. Defer teardown to the
		// next reconcile.
		return
	}
	for d := range e.domains {
		if _, want := desired[d]; want {
			continue
		}
		name := "loom-" + d
		if err := e.supervisor.Remove(e.ctx, name); err != nil {
			e.logger.Error("loom domain consumer remove failed", "domain", d, "err", err)
			continue
		}
		delete(e.domains, d)
		sink := newConsumerHealthSink(e.conn, e.cfg.HealthKVBucket, e.cfg.Instance, name, e.states)
		if err := sink.delete(e.ctx); err != nil {
			e.logger.Error("loom domain consumer health-state cleanup failed", "domain", d, "durable", name, "err", err)
		}
		e.logger.Info("loom domain consumer removed", "domain", d, "durable", name)
	}
}

// ensureDomainConsumers guarantees the pattern's completion domains have
// running consumers, running a full reconcile only on a miss. The fast path is
// a map check under e.mu (no KV list per trigger when the domains are already
// up). Because reconcile passes are serialized whole under e.mu, a reconcile
// entered after the caller's pin write is committed necessarily sees that pin —
// so the slow path cannot itself be raced into staleness.
func (e *Engine) ensureDomainConsumers(pattern *Pattern) {
	e.mu.Lock()
	missing := false
	for _, d := range pattern.Domains() {
		if _, ok := e.domains[d]; !ok {
			missing = true
			break
		}
	}
	e.mu.Unlock()
	if missing {
		e.reconcileConsumers()
	}
}

// eventBody is the minimal view of a core-events message Loom reads. It carries
// the three structural correlation keys the contract defines (Contract #10
// §10.6), all from the Event envelope body (read-from-body, never from the
// subject):
//   - requestId — the top-level field; the systemOp token (the op's own
//     requestId Loom chose).
//   - payload.taskKey — the userTask token (a vtx.task.<id> the
//     orchestration.taskCompleted event carries under the Event envelope's
//     payload object, Contract #3 §3.4; the top-level requestId on that event is
//     the user's bound-op requestId, which Loom does not know, so it cannot be
//     the correlation key).
//   - payload.externalRef — the externalTask token (the bare instance handle
//     Loom minted and parked on; the bridge's replyOp event echoes it back. The
//     top-level requestId on that event is the bridge's result-op requestId,
//     which Loom does not own, so it cannot be the correlation key).
//
// Loom stays domain-ignorant: it does not know which event is which, it tries
// each key against the durable token store and the pointer decides.
type eventBody struct {
	RequestID string `json:"requestId"`
	Payload   struct {
		TaskKey     string `json:"taskKey"`
		ExternalRef string `json:"externalRef"`
	} `json:"payload"`
}

// handleCompletion correlates a committed business event to its instance by a
// direct token.<token> GET on loom-state and advances the cursor. It tries each
// structural correlation key (requestId for systemOp, payload.taskKey for
// userTask, payload.externalRef for externalTask); at most one resolves a live
// pointer (tokens are unique). There is no in-memory index; the pointer's
// presence is the correlation + idempotency guard (Contract #10 §10.6).
func (e *Engine) handleCompletion(ctx context.Context, msg substrate.Message) substrate.Decision {
	if len(msg.Body) == 0 {
		return substrate.Ack
	}
	var ev eventBody
	if err := json.Unmarshal(msg.Body, &ev); err != nil {
		// A core-events body Loom cannot parse is not its concern; ack + skip.
		return substrate.Ack
	}

	for _, token := range correlationKeys(ev) {
		instanceID, live, err := e.state.resolveToken(ctx, token)
		if err != nil {
			e.logger.Error("loom: token resolve failed; nak", "token", token, "err", err)
			return substrate.Nak
		}
		if !live {
			continue
		}
		if err := e.advance(ctx, instanceID, token); err != nil {
			e.logger.Error("loom advance failed; nak for redelivery",
				"instanceId", instanceID, "token", token, "err", err)
			return substrate.Nak
		}
		return substrate.Ack
	}
	// Not a token Loom is awaiting (another component's event, or a redelivered
	// completion for an already-advanced instance). Drop.
	return substrate.Ack
}

// correlationKeys returns the distinct, non-empty structural correlation keys to
// try for a completion event, in order (systemOp requestId first, userTask
// taskKey second, externalTask externalRef third), de-duplicated against the
// keys already chosen. At most one resolves a live pointer — tokens are unique
// handles, so only the current pending step's token is live; the single
// live-pointer invariant holds regardless of order. Trying requestId first is
// safe because the keys are unguessable NanoIDs: a completion event's top-level
// requestId (the bound-op or bridge result-op id) cannot collide with a live
// Loom token (Loom's own op requestId / taskKey / instance handle), so the wrong
// key never resolves a live pointer.
func correlationKeys(ev eventBody) []string {
	keys := make([]string, 0, 3)
	if ev.RequestID != "" {
		keys = append(keys, ev.RequestID)
	}
	if ev.Payload.TaskKey != "" && ev.Payload.TaskKey != ev.RequestID {
		keys = append(keys, ev.Payload.TaskKey)
	}
	if ref := ev.Payload.ExternalRef; ref != "" && ref != ev.RequestID && ref != ev.Payload.TaskKey {
		keys = append(keys, ref)
	}
	return keys
}

// --- Transition Engine -----------------------------------------------------

// advance moves an instance to its next step on a committed terminal. It
// re-reads loom-state and verifies the pendingToken still matches (idempotent:
// a redelivery whose token no longer matches clears the stale pointer and
// drops). On exhaustion it submits CompletePattern (event-only).
func (e *Engine) advance(ctx context.Context, instanceID, token string) error {
	inst, err := e.state.getInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	if inst == nil || inst.Status != StatusRunning || inst.PendingToken != token {
		// Already advanced (redelivery) — clear any stale pointer and drop.
		return e.state.deleteToken(ctx, token)
	}

	// Step resolution reads the instance's PINNED definition (bound at trigger
	// time), never the live pattern source: a pattern update mid-flight must not
	// re-index the durable cursor against reordered/changed steps. The pin is
	// written atomically with the instance, so a missing pin for a running
	// instance is an invariant break — and an unrecoverable one (no retry can
	// restore it), so it becomes an operator-visible failed terminal (§10.6:
	// never a silent wedge) rather than an error → Nak → infinite redelivery.
	// Transient pin-read errors stay errors → Nak → retry.
	pattern, err := e.state.getPinnedPattern(ctx, inst.InstanceID)
	if err != nil {
		if errors.Is(err, errPatternPinMissing) {
			return e.fail(ctx, inst, token, "pattern pin missing")
		}
		return err
	}

	inst.Cursor++
	runCursor, completed, err := e.advanceToRunnableStep(ctx, inst, pattern)
	if err != nil {
		return err
	}
	if completed {
		// runCursor == len(pattern.Steps) (advanceToRunnableStep's exhaustion
		// return) — set it before complete so the durable terminal record shows
		// the true off-the-end position, matching a normal completion's cursor.
		inst.Cursor = runCursor
		return e.complete(ctx, inst, pattern, token)
	}
	inst.Cursor = runCursor
	return e.submitStep(ctx, inst, pattern, token)
}

// advanceToRunnableStep evaluates step guards forward from inst.Cursor against
// the subject's CURRENT Core KV state, skipping every step whose guard is false,
// and returns the cursor of the first step that should RUN (absent guard = true
// = run) — or completed=true if the cursor runs off the end (every remaining
// guard was false). It does NOT mutate inst.Cursor; the caller sets it before
// submitStep so submitStep reads pattern.Steps[inst.Cursor].
//
// Guard replay is a forward-skip mechanism only: it answers "is this step's
// precondition already satisfied, so skip it?", never "did this step already
// run?" (that is the durable token's job, §10.6). A guardless step's run/skip is
// NOT derivable from Core KV (it has no guard-replay signal, §10.6 invariant 2),
// so replay always LANDS ON a guardless step (no guard = run), never skips past
// one on inferred completion. This is what makes the cursor crash-rebuildable: a
// fresh instance over a partially-populated subject lands on the same effective
// step a surviving instance would occupy.
func (e *Engine) advanceToRunnableStep(ctx context.Context, inst *Instance, pattern *Pattern) (runCursor int, completed bool, err error) {
	cursor := inst.Cursor
	for {
		if cursor >= len(pattern.Steps) {
			return cursor, true, nil
		}
		step := pattern.Steps[cursor]
		if len(step.Guard) == 0 {
			// No guard → always runs.
			return cursor, false, nil
		}
		g, perr := parseGuard(step.Guard)
		if perr != nil {
			// A loaded pattern passed validate(), so its guards parse — a parse
			// failure here is an unexpected invariant break, surfaced as an error.
			return 0, false, fmt.Errorf("loom: step %d guard parse: %w", cursor, perr)
		}
		run, eerr := evalGuard(ctx, e.conn, e.cfg.CoreKVBucket, inst.SubjectKey, g)
		if eerr != nil {
			return 0, false, eerr
		}
		if run {
			return cursor, false, nil
		}
		// Guard false → skip this step (no task, no op, no token, no outbox) and
		// re-evaluate the next one within this same transition.
		cursor++
	}
}

// submitStep write-aheads the next step in a single AtomicBatch (update
// instance.<id>, write token.<newToken>, delete the prior token.<oldToken>,
// write the outbox.<opRequestId> op record, arm or disarm deadline.<instanceId>).
// The relay publishes the op off that batch — submission is part of the atomic
// fact, not a dual write (Crash-safety invariant 1, §10.6). oldToken == "" for
// step 0. The step is dispatched by Kind: a systemOp submits its bound op
// directly with a bounded deadline; a userTask submits CreateTask and parks for
// a human (the human wait is unbounded; the bounded deadline backstops only the
// task creation, §10.6).
func (e *Engine) submitStep(ctx context.Context, inst *Instance, pattern *Pattern, oldToken string) error {
	step := pattern.Steps[inst.Cursor]
	switch step.Kind {
	case StepKindUserTask:
		return e.submitUserTask(ctx, inst, pattern, step, oldToken)
	case StepKindExternalTask:
		return e.submitExternalTask(ctx, inst, pattern, step, oldToken)
	default:
		return e.submitSystemOp(ctx, inst, pattern, step, oldToken)
	}
}

// submitSystemOp submits a step's bound op directly. The write-ahead token is
// the op's own requestId; the step arms the bounded deadline (the off-stream
// rejected/lost backstop, §10.6).
func (e *Engine) submitSystemOp(ctx context.Context, inst *Instance, pattern *Pattern, step Step, oldToken string) error {
	token := deriveRequestID(inst.InstanceID, inst.Cursor)
	inst.PendingToken = token

	target := "vtx.meta." + pattern.PatternID
	payload := map[string]any{"subjectKey": inst.SubjectKey}
	ob, err := buildOutbox(token, step.Operation, payload, target, e.cfg.Lane, e.cfg.ActorKey)
	if err != nil {
		return err
	}
	if err := e.state.transition(ctx, inst, token, oldToken, ob, e.cfg.StepTimeout); err != nil {
		return err
	}
	e.logger.Info("loom step write-ahead",
		"instanceId", inst.InstanceID, "cursor", inst.Cursor,
		"kind", step.Kind, "operation", step.Operation, "requestId", token)
	return nil
}

// submitUserTask submits a CreateTask assigning the step's bound op to the
// instance subject (§10.5: assignedTo/scopedTo = the subject, forOperation = the
// bound op's meta-vertex). The write-ahead token is the taskKey — the
// completion-correlation handle the orchestration.taskCompleted event will carry — derived
// deterministically so a crash-retry re-supplies the SAME taskId and collapses
// on the Contract #4 tracker (no duplicate task). The CreateTask op's own
// requestId is a disjoint deterministic id (the submission idempotency handle).
//
// A bounded CreateTaskTimeout creation-deadline IS armed (the §10.6 deadline+
// probe applied to the task-creation path): waiting for the task vertex to be
// CREATED is a machine action with a tight latency bound, so a rejected/lost
// CreateTask must not park the token forever. The deadline backstops only the
// creation; once onDeadline's probe confirms the task vertex exists, it disarms
// the deadline and the wait for the human becomes unbounded (§10.6) — a
// human may take days, and false-failing that wait would be a correctness bug.
func (e *Engine) submitUserTask(ctx context.Context, inst *Instance, pattern *Pattern, step Step, oldToken string) error {
	// opMetaKey resolves LIVE (not pinned): it maps the step's operationType to
	// the op's CURRENT meta-vertex key, which becomes the task's forOperation
	// grant endpoint. The user executes the op as it exists when the task is
	// created, so the grant must reference the live op definition — a pinned
	// (possibly deleted/replaced) meta-vertex key would mint a task whose grant
	// points at a dead vertex. The pin governs WHICH steps run in WHAT order;
	// op-name → meta-vertex identity is deliberately today's truth.
	forOperation, ok := e.source.opMetaKey(step.Operation)
	if !ok {
		return fmt.Errorf("loom: userTask step %d: no op meta-vertex for operation %q (forOperation unresolved)",
			inst.Cursor, step.Operation)
	}

	taskID := deriveTaskID(inst.InstanceID, inst.Cursor)
	taskKey := "vtx.task." + taskID
	token := taskKey
	inst.PendingToken = token

	opRequestID := deriveRequestID(inst.InstanceID, inst.Cursor)
	payload := map[string]any{
		"assignee":     inst.SubjectKey,
		"forOperation": forOperation,
		"scopedTo":     inst.SubjectKey,
		"expiresAt":    substrate.FormatTimestamp(time.Now().Add(userTaskGrantTTL)),
		"taskId":       taskID,
	}
	ob, err := buildOutbox(opRequestID, opCreateTask, payload, inst.SubjectKey, e.cfg.Lane, e.cfg.ActorKey)
	if err != nil {
		return err
	}
	if err := e.state.transition(ctx, inst, token, oldToken, ob, e.cfg.CreateTaskTimeout); err != nil {
		return err
	}
	e.logger.Info("loom userTask write-ahead",
		"instanceId", inst.InstanceID, "cursor", inst.Cursor,
		"operation", step.Operation, "forOperation", forOperation,
		"taskKey", taskKey, "createTaskRequestId", opRequestID)
	return nil
}

// submitExternalTask submits the step's instanceOp and parks for the bridge's
// replyOp (§10.5/§10.6). The write-ahead pendingToken is the bare instance
// handle Loom mints — NOT the full vtx.<type>.<id> claim-vertex key: the handle
// is type-free, and the instanceOp DDL prepends its own package-chosen type to
// form the key (the engine never names a claim-vertex type — invariant a). The
// handle is passed to instanceOp as the caller-supplied `instanceKey` field
// (the verbatim-id seam, exactly like CreateTask's taskId), so a crash-retry
// re-mints the same handle and the re-submitted instanceOp collapses on the
// Contract #4 tracker. The bridge echoes the handle back as payload.externalRef;
// Loom's third correlation key resolves token.<handle> → instance and advances.
//
// The instanceOp's own submission requestId is a disjoint deterministic id
// (deriveRequestID), keeping the submission idempotency handle separate from the
// parked handle — exactly as submitUserTask keeps the CreateTask requestId
// disjoint from the taskId.
//
// A bounded StepTimeout deadline IS armed (an externalTask is a machine wait —
// the bridge is the completer — so a never-arriving reply must trip the FR29
// backstop, exactly like a systemOp; it is NOT the unbounded human wait of a
// userTask, so CreateTaskTimeout does not apply). Loom stays substrate-only: the
// external.<adapter> event is emitted by the instanceOp DDL's transactional
// outbox, never by Loom — the relay just submits the instanceOp like any op.
func (e *Engine) submitExternalTask(ctx context.Context, inst *Instance, pattern *Pattern, step Step, oldToken string) error {
	handle := deriveInstanceID(inst.InstanceID, inst.Cursor)
	token := handle
	inst.PendingToken = token

	opRequestID := deriveRequestID(inst.InstanceID, inst.Cursor)
	payload := map[string]any{
		// The caller-supplied id: the BARE instance handle, type-free. The
		// instanceOp DDL prepends its package-chosen type → vtx.<type>.<handle>.
		"instanceKey": handle,
		"subjectKey":  inst.SubjectKey,
		"adapter":     step.Adapter,
		"replyOp":     step.ReplyOp,
	}
	// params is opaque pass-through (no engine-side template resolution — a Loom
	// step's params are concrete pattern data). Omitted when empty.
	if len(step.Params) != 0 {
		payload["params"] = step.Params
	}

	target := "vtx.meta." + pattern.PatternID
	ob, err := buildOutbox(opRequestID, step.InstanceOp, payload, target, e.cfg.Lane, e.cfg.ActorKey)
	if err != nil {
		return err
	}
	if err := e.state.transition(ctx, inst, token, oldToken, ob, e.cfg.StepTimeout); err != nil {
		return err
	}
	e.logger.Info("loom externalTask write-ahead",
		"instanceId", inst.InstanceID, "cursor", inst.Cursor,
		"adapter", step.Adapter, "instanceOp", step.InstanceOp, "replyOp", step.ReplyOp,
		"instanceKey", handle, "instanceOpRequestId", opRequestID)
	return nil
}

// complete flips the instance to status=complete (the operational terminal) and
// writes the CompletePattern lifecycle op into the outbox — all in one
// AtomicBatch that also deletes the last pending pointer and disarms the
// deadline. The relay publishes CompletePattern, whose commit emits
// events.loom.patternCompleted through the Processor → outbox → core-events
// (never a direct publish, P2). Because the announcement rides the durable
// outbox in the same atomic fact as the terminal, it is delivered exactly like a
// step op — not best-effort — so a nested parent waiting on it is safe.
func (e *Engine) complete(ctx context.Context, inst *Instance, pattern *Pattern, oldToken string) error {
	inst.Status = StatusComplete
	inst.PendingToken = ""
	requestID := deriveRequestID(inst.InstanceID, lifecycleCursor)
	ob, err := buildOutbox(requestID, opCompletePattern,
		map[string]any{"instanceId": inst.InstanceID}, "", e.cfg.Lane, e.cfg.ActorKey)
	if err != nil {
		return err
	}
	if err := e.state.transition(ctx, inst, "", oldToken, ob, 0); err != nil {
		return err
	}
	e.logger.Info("loom pattern complete", "instanceId", inst.InstanceID, "patternId", pattern.PatternID)
	// The terminal batch deleted this instance's pattern pin; re-derive the
	// desired domain set so a consumer kept alive only by this instance drains.
	// Run async — reconcile takes e.mu and supervisor calls, and this path runs
	// inside a consumer handler.
	go e.reconcileConsumers()
	return nil
}

// fail flips the instance to status=failed (the off-stream rejected/timeout
// terminal, §10.6) and writes the FailPattern lifecycle op into the outbox in
// the same AtomicBatch (which also deletes the pending pointer and disarms the
// deadline). Delivery of the announcement is durable, like complete.
func (e *Engine) fail(ctx context.Context, inst *Instance, oldToken, reason string) error {
	inst.Status = StatusFailed
	inst.PendingToken = ""
	inst.RetryCount++
	requestID := deriveRequestID(inst.InstanceID, lifecycleCursor)
	payload := map[string]any{"instanceId": inst.InstanceID}
	if reason != "" {
		payload["reason"] = reason
	}
	ob, err := buildOutbox(requestID, opFailPattern, payload, "", e.cfg.Lane, e.cfg.ActorKey)
	if err != nil {
		return err
	}
	if err := e.state.transition(ctx, inst, "", oldToken, ob, 0); err != nil {
		return err
	}
	e.logger.Warn("loom instance failed",
		"instanceId", inst.InstanceID, "cursor", inst.Cursor, "reason", reason)
	// Same drain trigger as complete: the terminal batch deleted the pattern pin.
	go e.reconcileConsumers()
	return nil
}

// userTaskTokenPrefix is the key prefix of a userTask write-ahead token (the
// taskKey, vtx.task.<id>). A token with this prefix is an unbounded human wait,
// distinguishing it from a systemOp token (a bare requestId).
const userTaskTokenPrefix = "vtx.task."

// isUserTaskToken reports whether a pending token is a userTask taskKey.
func isUserTaskToken(token string) bool {
	return strings.HasPrefix(token, userTaskTokenPrefix)
}

// lifecycleCursor is the deterministic cursor sentinel used to derive the
// requestId of the terminal lifecycle op (CompletePattern/FailPattern). It is
// distinct from any step cursor (steps are 0..len-1), so the lifecycle op's
// requestId never collides with a step's; redelivery of the operational
// terminal collapses on the Contract #4 tracker.
const lifecycleCursor = -1

// --- Step-deadline-exceeded handler (Contract #10 §10.6) -------------------

// deadlineDurable is the deadline watcher's durable consumer name.
const deadlineDurable = "loom-deadline"

// handleDeadline reacts to a deadline.<instanceId> delete/expiry marker (empty
// body). A value write (the re-arm PUT) carries a body and is ignored.
func (e *Engine) handleDeadline(ctx context.Context, subjPrefix string, msg substrate.Message) substrate.Decision {
	if len(msg.Body) != 0 {
		// A re-arm PUT, not an expiry/delete — nothing to do.
		return substrate.Ack
	}
	instanceID := strings.TrimPrefix(strings.TrimPrefix(msg.Subject, subjPrefix), deadlinePrefix)
	if instanceID == "" {
		return substrate.Ack
	}
	if err := e.onDeadline(ctx, instanceID); err != nil {
		e.logger.Error("loom: deadline handler failed; nak", "instanceId", instanceID, "err", err)
		return substrate.Nak
	}
	return substrate.Ack
}

// onDeadline runs the read-before-act probe for an instance whose step deadline
// fired (Contract #10 §10.6): GET the Contract #4 op tracker for the pending
// token — present → the op committed but its event was missed → advance + alert;
// absent but the outbox record still present → the relay has not delivered →
// re-arm; absent and no outbox record → rejected → fail. Every branch re-reads
// instance state and is CAS-on-running (the advance/fail paths verify the
// pending token), so a redelivered marker / second replica is a no-op.
//
// The pending step's kind selects the probe:
//   - userTask (vtx.task.<id> token) → onUserTaskDeadline: the deadline is
//     bounded on the task-CREATION only, so the probe reads the task vertex and
//     the CreateTask op's tracker/outbox to decide created-vs-rejected.
//   - externalTask → onExternalTaskDeadline: the pending token is the bare
//     instance handle, but the instanceOp's tracker/outbox are keyed by the
//     instanceOp's own requestId (not the handle), so the probe MUST re-derive
//     that requestId — it cannot reuse the systemOp branch (which probes the
//     pending token directly).
//   - systemOp → the inline probe below: the pending token IS the op requestId,
//     so trackerExists(token)/outboxExists(token) probe the right keys.
//
// A userTask token is distinguishable by its vtx.task.<id> shape, but systemOp
// and externalTask tokens are both bare NanoIDs — so the systemOp/externalTask
// split reads the step kind from the instance's PINNED pattern at inst.Cursor
// (authoritative; the pin is written atomically with the instance and so is
// always present for a running instance — a missing pin is the same
// unrecoverable invariant break advance handles, turned into a failed terminal
// rather than an infinite Nak loop).
func (e *Engine) onDeadline(ctx context.Context, instanceID string) error {
	inst, err := e.state.getInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	if inst == nil || inst.Status != StatusRunning {
		// Already terminal, or a stale marker (e.g. the disarm delete from a
		// normal advance/terminal). No-op.
		return nil
	}
	token := inst.PendingToken
	if token == "" {
		return nil
	}
	if isUserTaskToken(token) {
		return e.onUserTaskDeadline(ctx, inst)
	}

	pattern, err := e.state.getPinnedPattern(ctx, inst.InstanceID)
	if err != nil {
		if errors.Is(err, errPatternPinMissing) {
			return e.fail(ctx, inst, token, "pattern pin missing")
		}
		return err
	}
	if inst.Cursor >= 0 && inst.Cursor < len(pattern.Steps) &&
		pattern.Steps[inst.Cursor].Kind == StepKindExternalTask {
		return e.onExternalTaskDeadline(ctx, inst)
	}

	committed, err := e.trackerExists(ctx, token)
	if err != nil {
		return err
	}
	if committed {
		// The op committed; its completion event was missed (mis-declared
		// completionDomains / lost). Advance off the durable tracker (§10.6).
		e.logger.Warn("loom: completion recovered via deadline probe; check completionDomains",
			"instanceId", instanceID, "requestId", token, "patternRef", inst.PatternRef)
		return e.advance(ctx, instanceID, token)
	}

	outboxPending, err := e.state.outboxExists(ctx, token)
	if err != nil {
		return err
	}
	if outboxPending {
		// The relay has not delivered the op yet — extend the deadline rather
		// than fail.
		e.logger.Info("loom: deadline fired before relay delivered; re-arming",
			"instanceId", instanceID, "requestId", token)
		return e.state.rearmDeadline(ctx, instanceID, e.cfg.StepTimeout)
	}

	// Tracker absent and the op was relayed (no outbox record) → rejected/lost.
	return e.fail(ctx, inst, token, fmt.Sprintf("step %d deadline exceeded; op rejected or lost", inst.Cursor))
}

// onUserTaskDeadline runs the read-before-act probe for a userTask whose bounded
// creation-deadline fired (the §10.6 deadline+probe applied to task creation). It
// distinguishes "still waiting for the task to be CREATED" (a bounded machine
// action) from "the task exists, now waiting for the HUMAN" (unbounded):
//
//  1. GET the task vertex vtx.task.<taskId> from Core KV. Present → the
//     CreateTask committed and the flow is now in the legitimate unbounded human
//     wait → disarm the creation-deadline (the cursor/token are untouched) and
//     stop; the human may take days.
//  2. Absent → probe the CreateTask op like a systemOp deadline: its tracker
//     present → CreateTask committed but the task-vertex read raced/missed →
//     re-arm; else its outbox record still present → the relay has not delivered
//     → re-arm; else (no task, no tracker, no outbox) → CreateTask rejected/lost
//     → fail.
//
// Every branch re-reads instance state via the caller and is CAS-on-running (the
// fail path verifies the pending token), so a redelivered marker / second replica
// is a no-op.
func (e *Engine) onUserTaskDeadline(ctx context.Context, inst *Instance) error {
	taskID := deriveTaskID(inst.InstanceID, inst.Cursor)
	created, err := e.taskVertexExists(ctx, taskID)
	if err != nil {
		return err
	}
	if created {
		// The task vertex exists: the bounded creation wait is over and the
		// unbounded human wait begins. Disarm the deadline without touching the
		// cursor/token — the instance stays running until the human acts.
		e.logger.Info("loom: userTask created; disarming creation-deadline for unbounded human wait",
			"instanceId", inst.InstanceID, "cursor", inst.Cursor, "taskId", taskID)
		return e.state.disarmDeadline(ctx, inst.InstanceID)
	}

	opRequestID := deriveRequestID(inst.InstanceID, inst.Cursor)
	committed, err := e.trackerExists(ctx, opRequestID)
	if err != nil {
		return err
	}
	if committed {
		// CreateTask committed but the task-vertex read raced the commit; the next
		// probe will see the vertex. Extend the creation-deadline rather than fail.
		e.logger.Info("loom: CreateTask committed but task vertex not yet visible; re-arming",
			"instanceId", inst.InstanceID, "cursor", inst.Cursor, "createTaskRequestId", opRequestID)
		return e.state.rearmDeadline(ctx, inst.InstanceID, e.cfg.CreateTaskTimeout)
	}

	outboxPending, err := e.state.outboxExists(ctx, opRequestID)
	if err != nil {
		return err
	}
	if outboxPending {
		// The relay has not delivered the CreateTask yet — extend rather than fail.
		e.logger.Info("loom: creation-deadline fired before relay delivered CreateTask; re-arming",
			"instanceId", inst.InstanceID, "cursor", inst.Cursor, "createTaskRequestId", opRequestID)
		return e.state.rearmDeadline(ctx, inst.InstanceID, e.cfg.CreateTaskTimeout)
	}

	// No task vertex, no tracker, no outbox record → the CreateTask was rejected
	// or lost. Fail the instance rather than park the token forever (§10.6: never
	// a silent wedge).
	return e.fail(ctx, inst, inst.PendingToken,
		fmt.Sprintf("step %d CreateTask rejected", inst.Cursor))
}

// onExternalTaskDeadline runs the read-before-act probe for an externalTask
// whose bounded StepTimeout deadline fired. It is the systemOp ladder
// (committed-but-missed → advance+alert; not-yet-relayed → re-arm; rejected/lost
// → fail) with one critical difference: the pending token is the bare instance
// HANDLE Loom parked on, but the instanceOp's Contract #4 tracker and its outbox
// record are keyed by the instanceOp's OWN requestId (deriveRequestID), NOT the
// handle. Probing the handle would read keys that never exist (vtx.op.<handle> /
// outbox.<handle>) and ALWAYS wrongly fail a healthy instance — so the probe
// re-derives the instanceOp requestId (exactly as onUserTaskDeadline re-derives
// the CreateTask requestId) while the advance/fail act on the pending handle
// token. Unlike a userTask there is no created-vs-human-wait split: an
// externalTask park is a pure bounded machine wait (the bridge completes it), so
// there is no disarm-and-go-unbounded branch.
//
// Every branch re-reads instance state via the caller and is CAS-on-running (the
// advance/fail paths verify the pending token), so a redelivered marker / second
// replica is a no-op.
func (e *Engine) onExternalTaskDeadline(ctx context.Context, inst *Instance) error {
	token := inst.PendingToken
	opRequestID := deriveRequestID(inst.InstanceID, inst.Cursor)

	committed, err := e.trackerExists(ctx, opRequestID)
	if err != nil {
		return err
	}
	if committed {
		// The instanceOp committed; the external/replyOp completion event was
		// missed (mis-declared completionDomains / lost reply). Advance off the
		// durable tracker on the pending handle token (§10.6).
		e.logger.Warn("loom: completion recovered via deadline probe; check completionDomains",
			"instanceId", inst.InstanceID, "instanceKey", token, "instanceOpRequestId", opRequestID,
			"patternRef", inst.PatternRef)
		return e.advance(ctx, inst.InstanceID, token)
	}

	outboxPending, err := e.state.outboxExists(ctx, opRequestID)
	if err != nil {
		return err
	}
	if outboxPending {
		// The relay has not delivered the instanceOp yet — extend the deadline
		// rather than fail.
		e.logger.Info("loom: deadline fired before relay delivered instanceOp; re-arming",
			"instanceId", inst.InstanceID, "instanceOpRequestId", opRequestID)
		return e.state.rearmDeadline(ctx, inst.InstanceID, e.cfg.StepTimeout)
	}

	// Tracker absent and the instanceOp was relayed (no outbox record) →
	// rejected/lost. Fail on the pending handle token.
	return e.fail(ctx, inst, token,
		fmt.Sprintf("step %d deadline exceeded; instanceOp rejected or lost", inst.Cursor))
}

// trackerExists reports whether the Contract #4 op tracker vtx.op.<requestId>
// exists in Core KV (a read — Loom never writes Core KV). A committed op writes
// the tracker; a rejected op (denied before commit step 8) writes none.
func (e *Engine) trackerExists(ctx context.Context, requestID string) (bool, error) {
	_, err := e.conn.KVGet(ctx, e.cfg.CoreKVBucket, "vtx.op."+requestID)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("loom: probe tracker %q: %w", requestID, err)
	}
	return true, nil
}

// taskVertexExists reports whether the task vertex vtx.task.<taskId> exists in
// Core KV (a read — Loom never writes Core KV). A committed CreateTask mints it;
// a rejected CreateTask mints none. It is the signal that a userTask's bounded
// creation wait is over and the unbounded human wait may begin (§10.6).
func (e *Engine) taskVertexExists(ctx context.Context, taskID string) (bool, error) {
	_, err := e.conn.KVGet(ctx, e.cfg.CoreKVBucket, "vtx.task."+taskID)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("loom: probe task vertex %q: %w", taskID, err)
	}
	return true, nil
}
