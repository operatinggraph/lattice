package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
	"github.com/asolgan/lattice/internal/refractor/failure"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/substrate"
)

const reconnectDelay = 5 * time.Second

// ProbeInterval is the delay between consecutive probe attempts during an infrastructure pause.
// Exported so tests can override it to a short value for fast recovery detection.
var ProbeInterval = 10 * time.Second

// RebuildPollInterval is the interval between consumer lag checks during a rebuild.
// Exported so tests can override it to a short value without real sleeps.
// The interval is captured into the Pipeline at construction time via New.
var RebuildPollInterval = 500 * time.Millisecond

// Pipeline processes Core KV messages for a single rule: evaluate → project → write.
// Each rule runs its own Pipeline in an independent goroutine (NFR13).
type Pipeline struct {
	ruleID       string
	adapterName  string // "nats_kv" or "postgres" — used for logging only
	plan         *simple.QueryPlan
	coreKVBucket string // Core KV bucket name; used to strip the $KV prefix from subjects
	adjKV        jetstream.KeyValue
	coreKV       jetstream.KeyValue

	// engineKind selects the evaluate code path; "simple" (default) drives
	// the plan-based simple.Evaluate; "full" drives full.Engine.ExecuteWith
	// against fullCR and the live event context. envelopeFn (when non-nil)
	// rewrites each projection row into the on-wire envelope expected by
	// the adapter target (e.g. Contract #6 §6.2 Capability KV shape).
	engineKind string
	fullEngine *full.Engine
	fullCR     ruleengine.CompiledRule
	envelopeFn EnvelopeFn

	// actorEnumerator enables cross-vertex fan-out. When non-nil and
	// engineKind == Full, evaluateForEntry expands every CDC event on a
	// non-actor vertex into the set of affected actors and re-executes
	// the cypher per actor. Nil uses the single-execute path.
	actorEnumerator *ActorEnumerator

	// actorDeleteKey derives the Capability KV target key to delete when an
	// actor disappears (tombstone shortcut and reprojectActors missing-actor
	// path). It maps an actor vertex key to the key this lens's envelope
	// projects to, so each lens removes the key it actually owns. Nil falls
	// back to capabilityKeyForActor (cap.<actor>), the primary lens's shape.
	actorDeleteKey func(actorKey string) string

	// latencyBuf captures the (CDC → projection-write) latency per event
	// so the heartbeat can compute mean/p95/p99 per Lens. Nil disables.
	latencyBuf *LatencyRingBuffer
	adapterMu    sync.RWMutex    // protects adpt for concurrent hot-reload
	adpt         adapter.Adapter // access via currentAdapter(); swap via HotReloadInto
	planMu       sync.RWMutex   // protects plan for concurrent hot-reload

	reporter     *health.Reporter // nil → skip health KV operations (optional)

	// Retry queue (optional). When non-nil and retryMaxAttempts > 0, transient write
	// failures are enqueued for exponential-backoff retry instead of Nak'd.
	// Set via SetRetryQueue before calling Run.
	retryQueue       *failure.RetryQueue
	retryMaxAttempts int
	retryBaseBackoff time.Duration
	retryJS          jetstream.JetStream // for DLQ escalation after retry exhaustion

	// Lag poller (optional). When non-nil, publishes per-lens consumer lag metrics
	// to lattice.refractor.metrics.<lensId> at health.MetricsInterval.
	// Set via SetLagPoller before calling Run.
	lagPoller *health.LagPoller

	// Audit writer (optional). When non-nil, appends an audit entry to the
	// per-rule JetStream stream on every successful write.
	// Set via SetAuditWriter before calling Run.
	auditWriter *health.AuditWriter

	// Rebuild support. rebuildPollInterval is captured from RebuildPollInterval
	// at construction time; watchRebuildCompletion polls the supervisor for
	// pending count. rebuildInFlight is true from the start of Rebuild until
	// watchRebuildCompletion observes zero consumer lag (or the rebuild aborts);
	// the health sink consults it so a supervisor-driven active-persist (e.g.
	// probe recovery mid-rebuild) re-persists "rebuilding" instead of
	// prematurely reporting "active" while the rescan is still draining.
	rebuildPollInterval time.Duration
	rebuildInFlight     atomic.Bool

	// Supervised runtime. The supervisor hosts the pump skeleton (restore →
	// pump → classify → pause/probe/resume); the pipeline supplies the handler
	// + Classify + Probe + HealthSink policy. Configured via RunOn before Run.
	supervisor  *substrate.ConsumerSupervisor
	consumerCfg substrate.ConsumerSpec // stream/filter/durable/deliver-policy/queue-group
	// started is closed once Run has registered the consumer with the
	// supervisor, so a control-plane Pause/Resume issued immediately after Run
	// (which runs in a goroutine) acts on a live consumer.
	started chan struct{}
}

// EnvelopeFn rewrites a projection-row map into the on-wire shape the
// adapter writes (e.g. Contract #6 §6.2 Capability KV envelope). The
// function receives the raw RETURN-row map produced by the engine plus the
// EventContext.Parameters (so it can derive `projectedAt`, `$actorKey`, etc.)
// and returns the wrapped row + a possibly-rewritten Key map.
// A nil EnvelopeFn writes the row verbatim.
type EnvelopeFn func(row map[string]any, keys map[string]any, params map[string]any) (newRow, newKeys map[string]any, err error)

// New creates a Pipeline for the given rule.
// adapterName is a display label for slog ("nats_kv" or "postgres").
// reporter may be nil — health KV reads/writes are skipped when nil.
// Returns an error if adpt is nil.
func New(
	ruleID, adapterName string,
	plan *simple.QueryPlan,
	coreKVBucket string,
	adjKV, coreKV jetstream.KeyValue,
	adpt adapter.Adapter,
	reporter *health.Reporter,
) (*Pipeline, error) {
	if adpt == nil {
		return nil, errors.New("pipeline: adapter must not be nil")
	}
	iv := RebuildPollInterval
	if iv <= 0 {
		iv = 500 * time.Millisecond
	}
	p := &Pipeline{
		ruleID:              ruleID,
		adapterName:         adapterName,
		plan:                plan,
		coreKVBucket:        coreKVBucket,
		adjKV:               adjKV,
		coreKV:              coreKV,
		reporter:            reporter,
		rebuildPollInterval: iv,
		engineKind:          ruleengine.EngineSimple,
		started:             make(chan struct{}),
	}
	p.adpt = adpt
	return p, nil
}

// UseFullEngine switches this pipeline's evaluate path to the full
// openCypher engine. cr must be the *full.CompiledRule that lens.Parse /
// corekv_source produced for this rule. Must be called before Run.
func (p *Pipeline) UseFullEngine(eng *full.Engine, cr ruleengine.CompiledRule) {
	p.engineKind = ruleengine.EngineFull
	p.fullEngine = eng
	p.fullCR = cr
}

// SetEnvelopeFn installs the on-wire envelope wrapper. Pass nil to clear.
// Must be called before Run.
func (p *Pipeline) SetEnvelopeFn(fn EnvelopeFn) {
	p.envelopeFn = fn
}

// SetActorEnumerator installs the cross-vertex fan-out enumerator for the
// full-engine path. When set, evaluateForEntry expands every non-actor CDC
// event into the set of affected actors and re-executes the cypher per actor.
// Pass nil to disable. Must be called before Run.
func (p *Pipeline) SetActorEnumerator(en *ActorEnumerator) {
	p.actorEnumerator = en
}

// SetActorDeleteKey installs the actor-deletion delete-key derivation used by
// both actor-disappearance paths (the tombstone shortcut and the
// reprojectActors missing-actor path). It lets a lens delete the key its own
// envelope projects to. Pass nil to keep the default cap.<actor> derivation.
// Must be called before Run.
func (p *Pipeline) SetActorDeleteKey(fn func(actorKey string) string) {
	p.actorDeleteKey = fn
}

// SetLatencyBuffer installs the per-Lens latency ring buffer.
// Pass nil to disable. Must be called before Run.
func (p *Pipeline) SetLatencyBuffer(buf *LatencyRingBuffer) {
	p.latencyBuf = buf
}

// LatencyBuffer returns the installed ring buffer (or nil). Used by
// the heartbeater to summarise per-Lens latency at tick.
func (p *Pipeline) LatencyBuffer() *LatencyRingBuffer {
	return p.latencyBuf
}

// currentAdapter returns the active adapter under a read lock.
// All internal code must use this instead of accessing adpt directly.
func (p *Pipeline) currentAdapter() adapter.Adapter {
	p.adapterMu.RLock()
	defer p.adapterMu.RUnlock()
	return p.adpt
}

func (p *Pipeline) currentPlan() *simple.QueryPlan {
	p.planMu.RLock()
	defer p.planMu.RUnlock()
	return p.plan
}

// HotReloadPlan atomically replaces the compiled query plan. Any message already in
// processMsg continues with the plan it captured at the start of that call; the next
// message will use newPlan. Returns an error if newPlan is nil.
// Used by the orchestrator when a MATCH change is detected, so that the subsequent
// operator-triggered rebuild re-scans Core KV with the updated query.
func (p *Pipeline) HotReloadPlan(newPlan *simple.QueryPlan) error {
	if newPlan == nil {
		return errors.New("pipeline: HotReloadPlan: newPlan must not be nil")
	}
	p.planMu.Lock()
	p.plan = newPlan
	p.planMu.Unlock()
	return nil
}

// SetRetryQueue configures the pipeline to use q for transient write failure retry.
// maxAttempts is the maximum number of retry attempts before DLQ escalation (0 = no retry).
// baseBackoff is the base exponential-backoff duration (doubles each attempt).
// js is the JetStream handle used to publish DLQ messages on exhaustion (may be nil if DLQ is not needed).
// Must be called before Run.
func (p *Pipeline) SetRetryQueue(q *failure.RetryQueue, js jetstream.JetStream, maxAttempts int, baseBackoff time.Duration) {
	p.retryQueue = q
	p.retryJS = js
	p.retryMaxAttempts = maxAttempts
	p.retryBaseBackoff = baseBackoff
}

// SetLagPoller attaches a LagPoller that publishes per-rule consumer lag metrics.
// Must be called before Run.
func (p *Pipeline) SetLagPoller(lp *health.LagPoller) {
	p.lagPoller = lp
}

// SetAuditWriter attaches an AuditWriter that appends entries to the per-rule
// JetStream audit stream on every successful write.
// Must be called before Run. EnsureStream must have been called on aw before Run.
func (p *Pipeline) SetAuditWriter(aw *health.AuditWriter) {
	p.auditWriter = aw
}

// RunOn configures the supervised runtime for this pipeline: a substrate
// connection (from which the pipeline builds its own ConsumerSupervisor — one
// supervisor per pipeline, one consumer per supervisor) and the consumer spec
// config (stream, filter, durable name, delivery policy, queue group,
// redelivery floor). The handler + Classify + Probe + HealthSink hooks are
// filled in by Run. Must be called before Run.
func (p *Pipeline) RunOn(conn *substrate.Conn, cfg substrate.ConsumerSpec) {
	if p.supervisor != nil {
		slog.Error("pipeline: RunOn called more than once, ignoring", "ruleId", p.ruleID)
		return
	}
	p.supervisor = substrate.NewConsumerSupervisor(conn)
	p.consumerCfg = cfg
}

// Supervisor returns the pipeline's ConsumerSupervisor (nil before RunOn).
// Exposed so the rebuild lag-watch and control plane can drive Reset / Pause /
// Resume / pending-count through the same supervised consumer.
func (p *Pipeline) Supervisor() *substrate.ConsumerSupervisor {
	return p.supervisor
}

// HotReloadInto atomically replaces the adapter. Any message already in processMsg
// continues with the adapter it captured at the start of that call; the next message
// will use newAdpt. Returns an error if newAdpt is nil.
// Used by the orchestrator for INTO-only rule updates (FR4).
func (p *Pipeline) HotReloadInto(newAdpt adapter.Adapter) error {
	if newAdpt == nil {
		return errors.New("pipeline: HotReloadInto: newAdpt must not be nil")
	}
	p.adapterMu.Lock()
	p.adpt = newAdpt
	p.adapterMu.Unlock()
	return nil
}

// Run starts the supervised consumer for this rule on the configured supervisor
// (via RunOn) and blocks until ctx is cancelled. The supervisor owns the pump
// skeleton — restore persisted paused state on startup (NFR4), pump, classify,
// pause/probe/resume (FR16, FR17, FR19a). The pipeline supplies the processing
// policy (handler), error classification, recovery probe, and HealthSink.
// Callers must use a sync.WaitGroup to track completion for graceful shutdown.
func (p *Pipeline) Run(ctx context.Context) {
	if p.supervisor == nil {
		slog.Error("pipeline: Run called before RunOn — no supervisor configured", "ruleId", p.ruleID)
		return
	}

	// Start per-rule consumer lag metric publisher (Story 4.2).
	// Runs for the full lifetime of ctx — continues even during infra pauses (AC4).
	if p.lagPoller != nil {
		go p.lagPoller.Start(ctx)
	}

	// Start adjacency KV watcher (ADR-16). When adj.<nodeId> is written by the
	// bootstrapper, re-evaluate the corresponding Core KV node so the pipeline
	// produces output with up-to-date adjacency without writing back to Core KV.
	go p.runAdjWatch(ctx)

	spec := p.consumerCfg
	spec.Handler = p.handle
	spec.Classify = classifyForSupervisor
	spec.Probe = func(pctx context.Context) error { return p.currentAdapter().Probe(pctx) }
	spec.Health = newHealthSink(p.reporter, p.rebuildInFlight.Load)
	// ProbeInterval is exported so tests can shrink it for fast recovery detection.
	if spec.ProbeInterval <= 0 {
		spec.ProbeInterval = ProbeInterval
	}

	if err := p.supervisor.Add(ctx, spec); err != nil {
		slog.Error("pipeline: supervisor add", "ruleId", p.ruleID, "err", err)
		return
	}
	// Signal that the supervised consumer is registered so Pause/Resume issued
	// immediately after Run starts (in a goroutine) act on a live consumer.
	close(p.started)

	<-ctx.Done()
	// Stop the pump without deleting the durable — its persisted position is the
	// point of durability (substrate doctrine, Winston Q3). Refractor's
	// delete-on-rule-removal path goes through the supervisor's Remove from the
	// orchestrator (control Deleter), not here.
	p.supervisor.Stop()
}

// Rebuild performs an in-place rebuild of the rule's target store. It:
//  1. Sets health KV status to "rebuilding" (AC4).
//  2. If truncate is true and the adapter implements adapter.Truncater, truncates
//     the target store before the rescan (FR29, AC2).
//  3. Resets the durable consumer via the supervisor (delete-and-recreate
//     preserving DeliverLastPerSubjectPolicy), so all current Core KV entries are
//     rescanned from the beginning (FR28, AC1). The supervised pump swaps onto
//     the recreated durable automatically.
//  4. Launches a background goroutine (watchRebuildCompletion) that transitions
//     health KV to "active" when consumer lag reaches zero (AC5).
//
// Returns nil immediately — the rebuild runs asynchronously. The caller (control
// service) MUST call Rebuild in its own goroutine and return an async ack to the
// operator before Rebuild returns.
func (p *Pipeline) Rebuild(ctx context.Context, truncate bool) error {
	// Mark the rebuild in flight before the status write so a concurrent
	// supervisor health persist (probe recovery, operator resume) cannot
	// publish "active" while the rescan is still draining.
	p.rebuildInFlight.Store(true)

	// 1. Set health status to "rebuilding".
	if p.reporter != nil {
		if err := p.reporter.SetRebuilding(ctx); err != nil {
			slog.Warn("pipeline: rebuild: could not set rebuilding status", "ruleId", p.ruleID, "err", err)
		}
	}

	// 2. Optional target-store truncation.
	if truncate {
		adpt := p.currentAdapter()
		if t, ok := adpt.(adapter.Truncater); ok {
			if err := t.Truncate(ctx); err != nil {
				p.rebuildInFlight.Store(false)
				return fmt.Errorf("pipeline: rebuild: truncate: %w", err)
			}
		} else {
			slog.Warn("pipeline: rebuild: truncate=true but adapter does not implement Truncater; skipping",
				"ruleId", p.ruleID)
		}
	}

	// 3. Reset the durable via the supervisor (delete-recreate-swap).
	if p.supervisor == nil {
		p.rebuildInFlight.Store(false)
		return fmt.Errorf("pipeline: rebuild: no supervisor configured")
	}
	if err := p.supervisor.Reset(ctx, p.consumerCfg.Name); err != nil {
		p.rebuildInFlight.Store(false)
		return fmt.Errorf("pipeline: rebuild: reset consumer: %w", err)
	}

	// 4. Launch background goroutine to transition to "active" when lag reaches zero.
	if p.reporter != nil {
		go p.watchRebuildCompletion(ctx)
	} else {
		// No reporter → no completion watcher will ever clear the flag.
		p.rebuildInFlight.Store(false)
	}

	return nil
}

// watchRebuildCompletion polls the supervised consumer's pending count at
// rebuildPollInterval. When it reaches zero, it transitions health KV from
// "rebuilding" back to "active" (AC5).
func (p *Pipeline) watchRebuildCompletion(ctx context.Context) {
	// The rebuild window ends when this watcher exits for any reason; the
	// deferred clear keeps the health sink from pinning "rebuilding" forever
	// after a cancelled watch.
	defer p.rebuildInFlight.Store(false)
	ticker := time.NewTicker(p.rebuildPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pending, err := p.supervisor.PendingForConsumer(ctx, p.consumerCfg.Name)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// Consumer may still be initializing or context cancelled; retry.
				continue
			}
			if pending == 0 {
				// Clear the flag before the status write so a concurrent health
				// sink SetActive that re-checks the flag converges on "active".
				p.rebuildInFlight.Store(false)
				if p.reporter != nil {
					if serr := p.reporter.SetActive(ctx); serr != nil {
						slog.Error("pipeline: rebuild: set active", "ruleId", p.ruleID, "err", serr)
					}
				}
				return
			}
		}
	}
}

// handle is the supervised message handler (substrate.SupervisedHandler). It
// runs Refractor's full per-message policy — decode → classify key shape →
// evaluate → write, with terminal-DLQ and retry-queue disposition — and returns
// the substrate Decision the supervisor applies. A non-nil returned error is an
// Infra/Structural failure: the message is left UN-acked so JetStream redelivers
// it when the supervised pump resumes (the supervisor classifies the error and
// pauses). Transient and Terminal outcomes are disposed here and reported via the
// Decision (Nak for transient redelivery, Ack after DLQ/retry-enqueue).
func (p *Pipeline) handle(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
	// Extract Core KV key from subject: "$KV.<bucket>.<key>" → "<key>".
	// Done before the empty-body short-circuit so a link TOMBSTONE (which has
	// an empty body) can still be classified and fanned out on the actor-aware
	// pipeline (revocation must shrink capability docs).
	prefix := "$KV." + p.coreKVBucket + "."
	key := strings.TrimPrefix(msg.Subject, prefix)
	tombstone := len(msg.Body) == 0

	// Classify the key by Lattice Contract #1 §1.5 shape.
	switch substrate.ClassifyKey(key) {
	case substrate.KindAspect:
		// On the actor-aware (capability) pipeline an aspect-only mutation
		// (e.g. identity .state, role .description) changes a vertex's
		// projected state with no vertex-root event, so it must drive a
		// fan-out reprojection seeded from the parent vertex. Other lenses
		// keep the legacy ack-and-skip behaviour.
		if p.actorEnumerator != nil {
			return p.evalAspectFanOut(ctx, msg, key)
		}
		slog.Info("pipeline: aspect mutation observed but no handler registered",
			"ruleId", p.ruleID, "key", key,
			"parentVertexKey", key[:strings.LastIndex(key, ".")])
		return substrate.Ack, nil
	case substrate.KindLink:
		// On the actor-aware (capability) pipeline a pure link mutation
		// (holdsRole/grantedBy/...) changes actors' topology with no vertex
		// event, so it must drive a fan-out reprojection from both endpoints.
		// Other lenses keep the legacy ack-and-skip behaviour.
		if p.actorEnumerator != nil {
			return p.evalLinkFanOut(ctx, msg, key, tombstone)
		}
		slog.Info("pipeline: link mutation observed but no handler registered",
			"ruleId", p.ruleID, "key", key)
		return substrate.Ack, nil
	case substrate.KindUnknown:
		slog.Warn("pipeline: unknown key shape — defect signal",
			"ruleId", p.ruleID, "key", key)
		return substrate.Ack, nil
	}

	// KindVertex. A vertex tombstone (empty body) is handled below by the
	// normal evaluate path (the actor-aware pipeline emits a cap Delete);
	// for other lenses an empty body carries no props, so ack and skip.
	if tombstone {
		return substrate.Ack, nil
	}

	// KindVertex: parse type and id.
	label, _, ok := substrate.ParseVertexKey(key)
	if !ok {
		// Should not occur after ClassifyKey == KindVertex, but guard defensively.
		return substrate.Ack, nil
	}

	// Unmarshal payload.
	var props map[string]any
	if err := json.Unmarshal(msg.Body, &props); err != nil {
		slog.Error("pipeline: unmarshal payload",
			"ruleId", p.ruleID, "entityId", key,
			"stage", "pipeline", "adapter", p.adapterName, "err", err)
		return substrate.Nak, nil
	}

	// Edge events carry a non-empty "nodeId" field — adjacency builder handles these.
	if nodeID, _ := props["nodeId"].(string); nodeID != "" {
		return substrate.Ack, nil
	}

	isDeleted, _ := props["isDeleted"].(bool)
	entry := simple.NodeEntry{
		CoreKVKey:  key,
		NodeLabel:  label,
		IsDeleted:  isDeleted,
		Properties: props,
	}

	// Route to the appropriate engine. The simple engine returns
	// []EvalResult{Delete,Keys,Row}; the full engine returns
	// []ProjectionResult{Key,Values,Delete}. evaluateForEntry normalises
	// and applies the envelope so the downstream write path sees a single
	// []simple.EvalResult shape.
	results, err := p.evaluateForEntry(ctx, entry)
	if err != nil {
		slog.Error("pipeline: evaluate",
			"ruleId", p.ruleID, "entityId", key,
			"stage", "traversal", "adapter", p.adapterName, "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err)
	}

	// Write each result through the shared write path (failure classification,
	// terminal DLQ, retry enqueue, ack discipline). The adapter is captured
	// once inside writeResults so all results in this message use a consistent
	// instance even if HotReloadInto swaps it between messages.
	return p.writeResults(ctx, msg, key, results)
}

// evalLinkFanOut handles a KindLink CDC event on the actor-aware pipeline.
// It determines whether the link is a create or a tombstone, drives the
// endpoint-seeded fan-out reprojection (evaluateLinkFanOut), and writes the
// resulting capability projections through the normal write path.
//
// A link tombstone arrives with an empty body (NATS DEL/PURGE). A link create
// or update arrives with a body whose `isDeleted` field distinguishes a
// soft-delete (revocation) from an active link.
func (p *Pipeline) evalLinkFanOut(ctx context.Context, msg substrate.Message, key string, tombstone bool) (substrate.Decision, error) {
	isDeleted := tombstone
	if !tombstone {
		var props map[string]any
		if err := json.Unmarshal(msg.Body, &props); err != nil {
			slog.Error("pipeline: link fan-out: unmarshal payload",
				"ruleId", p.ruleID, "entityId", key, "err", err)
			return substrate.Nak, nil
		}
		isDeleted, _ = props["isDeleted"].(bool)
	}

	results, err := p.evaluateLinkFanOut(ctx, key, isDeleted)
	if err != nil {
		slog.Error("pipeline: link fan-out: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err)
	}

	return p.writeResults(ctx, msg, key, results)
}

// evalAspectFanOut handles a KindAspect CDC event on the actor-aware
// pipeline. An aspect-only mutation (e.g. identity .state, role .description)
// carries no vertex-root event, so the parent vertex's projection is re-derived
// by seeding the fan-out from the parent vertex (evaluateAspectFanOut) and
// writing the resulting capability projections through the normal write path.
//
// Unlike a link, an aspect mutation does not change graph topology, so no
// adjacency update is required; the aspect body is irrelevant to the fan-out
// (the reprojection cypher re-reads current Core KV state), so a tombstone
// (empty body) and a value change take the same path.
func (p *Pipeline) evalAspectFanOut(ctx context.Context, msg substrate.Message, key string) (substrate.Decision, error) {
	results, err := p.evaluateAspectFanOut(ctx, key)
	if err != nil {
		slog.Error("pipeline: aspect fan-out: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err)
	}

	return p.writeResults(ctx, msg, key, results)
}

// dispositionEvalErr maps an evaluate-stage error to a Decision (+ error for the
// pause path), mirroring the inline ack/nak discipline the pre-supervisor
// pipeline applied: Infra/Structural leave the message pending (return the error
// so the supervisor pauses); Terminal publishes a DLQ entry and acks; Transient
// naks for redelivery.
func (p *Pipeline) dispositionEvalErr(ctx context.Context, msg substrate.Message, key, stage string, err error) (substrate.Decision, error) {
	cat := failure.Classify(err)
	if cat == failure.CatInfra || cat == failure.CatStructural {
		// Do NOT dispose — leave pending for redelivery when the pipeline resumes.
		return substrate.Nak, err
	}
	if cat == failure.CatTerminal {
		p.publishTerminalDLQ(ctx, msg.Body, key, stage, err)
		return substrate.Ack, nil
	}
	return substrate.Nak, nil
}

// writeResults writes a slice of evaluation results through the active adapter,
// applying the same failure-classification, terminal-DLQ, retry-enqueue, and
// ack discipline as the inline vertex write loop. Returns the Decision the
// supervisor applies (plus a non-nil error on an infra/structural write failure,
// which leaves the message pending and pauses the pump).
//
// Retry enqueues and terminal DLQ publishes are buffered and flushed only when
// the whole batch is known free of infra/structural failures. Any path that
// leaves the message pending (Nak) makes redelivery re-run every result, so
// flushing eagerly would enqueue/publish a duplicate for the already-disposed
// results on every redelivery (e.g. each pause/resume cycle).
func (p *Pipeline) writeResults(ctx context.Context, msg substrate.Message, key string, results []simple.EvalResult) (substrate.Decision, error) {
	adpt := p.currentAdapter()
	var retryResults []simple.EvalResult
	var terminalErrs []error
	for i := range results {
		// Stamp the triggering CDC message's stream sequence as the monotonic
		// ordering token before any write. The retry-queue capture copies the
		// stamped result, so a replay carries this same (original, lower) seq,
		// which is exactly what must lose to a later real reprojection.
		results[i].ProjectionSeq = msg.Sequence
	}
	for _, result := range results {
		var writeErr error
		if result.Delete {
			writeErr = adpt.Delete(ctx, result.Keys, result.ProjectionSeq)
		} else {
			writeErr = adpt.Upsert(ctx, result.Keys, result.Row, result.ProjectionSeq)
		}

		if writeErr != nil {
			cat := failure.Classify(writeErr)
			op := "upsert"
			if result.Delete {
				op = "delete"
			}
			slog.Error("pipeline: "+op,
				"ruleId", p.ruleID, "entityId", key,
				"stage", "write", "adapter", p.adapterName, "err", writeErr)

			if cat == failure.CatInfra || cat == failure.CatStructural {
				// Buffered dispositions are dropped — redelivery re-evaluates
				// every result after the pause resolves.
				return substrate.Nak, writeErr
			}
			if cat == failure.CatTerminal {
				terminalErrs = append(terminalErrs, writeErr)
				continue
			}
			if cat == failure.CatTransient && p.retryQueue != nil && p.retryMaxAttempts > 0 {
				retryResults = append(retryResults, result)
				continue
			}
			return substrate.Nak, nil
		}

		p.writeAudit(ctx, key, result)
	}

	for _, terr := range terminalErrs {
		p.publishTerminalDLQ(ctx, msg.Body, key, "write", terr)
	}
	for _, r := range retryResults {
		p.enqueueRetry(key, msg.Body, r)
	}

	if len(retryResults) > 0 || len(terminalErrs) > 0 {
		// Transient enqueue / terminal DLQ: the message is fully disposed —
		// ack to prevent redelivery (the retry queue owns the eventual write).
		return substrate.Ack, nil
	}

	slog.Info("pipeline: processed",
		"ruleId", p.ruleID, "entityId", key,
		"stage", "pipeline", "adapter", p.adapterName)
	return substrate.Ack, nil
}

// enqueueRetry constructs and enqueues a RetryEntry for a transient write
// failure, mirroring the inline retry-enqueue path in processMsg.
func (p *Pipeline) enqueueRetry(key string, rawPayload []byte, result simple.EvalResult) {
	capturedResult := result
	capturedReporter := p.reporter
	capturedSeq := ""
	if p.reporter != nil {
		if seq := p.reporter.ActiveSequence(); seq != 0 {
			capturedSeq = fmt.Sprintf("%d", seq)
		}
	}
	e := &failure.RetryEntry{
		RuleID:       p.ruleID,
		EntityID:     key,
		Stage:        "write",
		RawPayload:   rawPayload,
		RuleSequence: capturedSeq,
		WriteFn: func(rctx context.Context) error {
			a := p.currentAdapter()
			if capturedResult.Delete {
				return a.Delete(rctx, capturedResult.Keys, capturedResult.ProjectionSeq)
			}
			return a.Upsert(rctx, capturedResult.Keys, capturedResult.Row, capturedResult.ProjectionSeq)
		},
		Attempt:     0,
		MaxAttempts: p.retryMaxAttempts,
		BaseBackoff: p.retryBaseBackoff,
		JS:          p.retryJS,
		OnDLQPublished: func(rctx context.Context, errMsg string) {
			if capturedReporter != nil {
				if recErr := capturedReporter.RecordError(rctx, errMsg); recErr != nil {
					slog.Error("pipeline: update health errorCount after retry DLQ",
						"ruleId", p.ruleID, "err", recErr)
				}
			}
		},
	}
	p.retryQueue.Enqueue(e)
}

// Pause manually pauses this rule's supervised consumer (FR30 control surface).
// The supervisor sets health KV to paused/manual and halts the pump; processing
// blocks until Resume. Safe to call from any goroutine; idempotent.
func (p *Pipeline) Pause(ctx context.Context) {
	if p.supervisor == nil {
		return
	}
	if !p.awaitStarted(ctx) {
		slog.Warn("pipeline: Pause: consumer never started, dropping", "ruleId", p.ruleID)
		return
	}
	p.supervisor.Pause(ctx, p.consumerCfg.Name)
}

// awaitStarted blocks (briefly) until Run has registered the supervised consumer
// so a control-plane Pause/Resume issued right after Run starts is not lost.
// Returns false if p.started was never closed within the wait window (Run
// exited early, e.g. supervisor nil or Add failed) — callers should treat this
// as "no live consumer to act on" rather than issuing a no-op against the
// supervisor.
func (p *Pipeline) awaitStarted(ctx context.Context) bool {
	select {
	case <-p.started:
		return true
	case <-ctx.Done():
		return false
	case <-time.After(2 * time.Second):
		return false
	}
}

// Resume clears a manual or structural pause and force-exits an in-flight infra
// probe loop (FR31, AC4), so processing resumes without waiting for the next
// probe; the supervisor sets health KV active. Safe to call from any goroutine;
// no-op if the consumer is not paused.
func (p *Pipeline) Resume(ctx context.Context) {
	if p.supervisor == nil {
		return
	}
	if !p.awaitStarted(ctx) {
		slog.Warn("pipeline: Resume: consumer never started, dropping", "ruleId", p.ruleID)
		return
	}
	p.supervisor.Resume(ctx, p.consumerCfg.Name)
}

// publishTerminalDLQ publishes a DLQ message for an entity whose data is permanently
// unrecoverable (failure.CatTerminal). Uses p.retryJS — the same JetStream handle set via
// SetRetryQueue. If p.retryJS == nil (no JetStream configured), logs and returns without
// panicking, mirroring RetryQueue.escalateToDLQ. rawBody is the message body
// stored as the DLQ rawPayload.
func (p *Pipeline) publishTerminalDLQ(ctx context.Context, rawBody []byte, entityID, stage string, origErr error) {
	if p.retryJS == nil {
		slog.Error("pipeline: terminal failure, no JetStream for DLQ — entity dropped",
			"ruleId", p.ruleID, "entityId", entityID,
			"stage", stage, "err", origErr)
		return
	}
	// Fill RuleSequence from the reporter's cached active sequence.
	// Only format when non-zero; zero means SetRuleSequence was never called (keeps "" sentinel).
	ruleSeq := ""
	if p.reporter != nil {
		if seq := p.reporter.ActiveSequence(); seq != 0 {
			ruleSeq = fmt.Sprintf("%d", seq)
		}
	}
	dlqMsg := failure.DLQMessage{
		RuleID:       p.ruleID,
		EntityID:     entityID,
		FailedStage:  stage,
		ErrorClass:   "TERMINAL",
		ErrorMessage: origErr.Error(),
		RetryCount:   0,
		RuleSequence: ruleSeq,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		RawPayload:   string(rawBody),
	}
	// Use WithoutCancel so a DLQ publish triggered during shutdown still completes.
	pubCtx := context.WithoutCancel(ctx)
	if err := failure.Publish(pubCtx, p.retryJS, p.ruleID, dlqMsg); err != nil {
		slog.Error("pipeline: terminal DLQ publish failed",
			"ruleId", p.ruleID, "entityId", entityID,
			"stage", stage, "err", err)
	} else if p.reporter != nil {
		// AC3: increment health KV error count after each DLQ write.
		if recErr := p.reporter.RecordError(pubCtx, origErr.Error()); recErr != nil {
			slog.Error("pipeline: update health errorCount after terminal DLQ",
				"ruleId", p.ruleID, "err", recErr)
		}
	}
}

// writeAudit appends an audit entry after a successful write. It is a no-op when
// auditWriter is nil (optional feature, AC6). Errors are logged as Warn — a failed
// audit entry must never interrupt message processing (the write already succeeded).
func (p *Pipeline) writeAudit(ctx context.Context, entityID string, result simple.EvalResult) {
	if p.auditWriter == nil {
		return
	}
	op := "upsert"
	var row map[string]any
	if result.Delete {
		op = "delete"
	} else {
		row = result.Row
	}
	if err := p.auditWriter.WriteAudit(ctx, entityID, op, row); err != nil {
		if ctx.Err() == nil {
			slog.Warn("pipeline: audit write failed",
				"ruleId", p.ruleID, "entityId", entityID, "op", op, "err", err)
		}
	}
}

// runAdjWatch watches the Refractor adjacency KV for new or updated
// entries. When adj.<nodeId> is committed by the bootstrapper, the pipeline
// fetches the corresponding Core KV node (read-only) and re-evaluates it so
// that output reflects the now-current adjacency without any write to Core KV
// (ADR-16). The watcher reconnects automatically on transient failures.
// Runs for the full lifetime of ctx — cancelled when the pipeline stops.
func (p *Pipeline) runAdjWatch(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		watcher, err := p.adjKV.WatchAll(ctx, jetstream.UpdatesOnly())
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("pipeline: adj watch: create watcher", "ruleId", p.ruleID, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(reconnectDelay):
			}
			continue
		}
		p.drainAdjWatch(ctx, watcher)
		watcher.Stop() //nolint:errcheck
	}
}

// drainAdjWatch reads entries from watcher until the channel closes or ctx is done.
func (p *Pipeline) drainAdjWatch(ctx context.Context, watcher jetstream.KeyWatcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-watcher.Updates():
			if !ok {
				// Channel closed — watcher stopped; runAdjWatch will reconnect.
				return
			}
			if entry == nil {
				// Sentinel value; should not occur with UpdatesOnly but guard defensively.
				continue
			}
			p.handleAdjUpdate(ctx, entry)
		}
	}
}

// handleAdjUpdate processes one adjacency KV change. It strips the "adj." prefix
// to recover the Core KV node key, fetches the node value directly (read-only),
// and re-evaluates it through the normal evaluate-and-write path.
func (p *Pipeline) handleAdjUpdate(ctx context.Context, adjEntry jetstream.KeyValueEntry) {
	const adjPrefix = "adj."
	nodeKey := strings.TrimPrefix(adjEntry.Key(), adjPrefix)
	if nodeKey == adjEntry.Key() {
		// Key does not start with "adj." — unexpected format, skip.
		return
	}

	// Point-read the Core KV node. Refractor is a pure consumer of Core KV —
	// this is a read, not a write (ADR-16).
	coreEntry, err := p.coreKV.Get(ctx, nodeKey)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			// Node hasn't arrived in Core KV yet. When it does, the normal stream
			// consumer will evaluate it with adjacency already in place.
			return
		}
		if ctx.Err() != nil {
			return
		}
		slog.Warn("pipeline: adj watch: core KV get",
			"ruleId", p.ruleID, "key", nodeKey, "err", err)
		return
	}

	data := coreEntry.Value()
	if len(data) == 0 {
		// Tombstone — node was deleted; skip (the normal consumer handles deletes).
		return
	}

	label, _, ok := substrate.ParseVertexKey(nodeKey)
	if !ok {
		return
	}

	var props map[string]any
	if err := json.Unmarshal(data, &props); err != nil {
		slog.Warn("pipeline: adj watch: unmarshal",
			"ruleId", p.ruleID, "key", nodeKey, "err", err)
		return
	}

	// Skip edge events — the bootstrapper processes these; pipelines handle nodes only.
	if nodeID, _ := props["nodeId"].(string); nodeID != "" {
		return
	}

	isDeleted, _ := props["isDeleted"].(bool)
	entry := simple.NodeEntry{
		CoreKVKey:  nodeKey,
		NodeLabel:  label,
		IsDeleted:  isDeleted,
		Properties: props,
	}

	results, err := p.evaluateForEntry(ctx, entry)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Warn("pipeline: adj watch: evaluate",
			"ruleId", p.ruleID, "key", nodeKey, "err", err)
		return
	}

	adpt := p.currentAdapter()
	// A guarded watermark may be advanced or cleared ONLY by a stream-sequenced
	// write (Contract #6 §6.2). The adjacency-watch path is not message-driven —
	// it carries no JetStream stream sequence — and it is redundant for the
	// guarded lenses: every link/aspect CDC event that an adjacency update
	// reflects also flows through the stream consumer's evalLinkFanOut /
	// evalAspectFanOut, which reproject the same affected actors with the
	// triggering message's stream sequence. Writing here with a sentinel seq of 0
	// would either be unconditionally dropped or could resurrect a tombstoned /
	// absent guarded key. So skip guarded-key writes on this path entirely and
	// log, leaving the watermark to the stream-sequenced reprojection.
	if g, ok := adpt.(interface{ Guarded() bool }); ok && g.Guarded() {
		if len(results) > 0 {
			slog.Info("pipeline: adj watch: skipping guarded-key write (stream consumer owns the watermark)",
				"ruleId", p.ruleID, "key", nodeKey, "results", len(results))
		}
		return
	}
	for _, result := range results {
		var writeErr error
		if result.Delete {
			writeErr = adpt.Delete(ctx, result.Keys, 0)
		} else {
			writeErr = adpt.Upsert(ctx, result.Keys, result.Row, 0)
		}
		if writeErr != nil {
			if ctx.Err() != nil {
				return
			}
			cat := failure.Classify(writeErr)
			slog.Error("pipeline: adj watch: write",
				"ruleId", p.ruleID, "key", nodeKey, "err", writeErr, "category", cat)
			if p.reporter != nil {
				if recErr := p.reporter.RecordError(ctx, writeErr.Error()); recErr != nil {
					slog.Error("pipeline: adj watch: update health errorCount",
						"ruleId", p.ruleID, "err", recErr)
				}
			}
			// Continue — remaining results are independent; adapter writes are idempotent.
			// Adj-watch events are not replayable from JetStream, so we do not pause here,
			// but the error is recorded in health KV for operator visibility.
			continue
		}
		slog.Info("pipeline: adj watch: re-evaluated",
			"ruleId", p.ruleID, "entityId", nodeKey,
			"stage", "pipeline", "adapter", p.adapterName)
	}
}

