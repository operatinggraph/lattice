package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/adapter"
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

// ConsumerResetter resets a rule's durable consumer to DeliverLastPerSubjectPolicy.
// *consumer.Manager satisfies this via its Reset method.
// Defined here to keep pipeline free of an import cycle with internal/consumer.
type ConsumerResetter interface {
	Reset(ctx context.Context, ruleID string) (jetstream.Consumer, error)
}

// Pipeline processes Core KV messages for a single rule: evaluate → project → write.
// Each rule runs its own Pipeline in an independent goroutine (NFR13).
type Pipeline struct {
	ruleID       string
	team         string
	adapterName  string // "nats_kv" or "postgres" — used for logging only
	plan         *simple.QueryPlan
	coreKVBucket string // Core KV bucket name; used to strip the $KV prefix from subjects
	adjKV        jetstream.KeyValue
	coreKV       jetstream.KeyValue
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

	// Lag poller (optional). When non-nil, publishes per-rule consumer lag metrics
	// to materializer.metrics.<ruleId> at health.MetricsInterval.
	// Set via SetLagPoller before calling Run.
	lagPoller *health.LagPoller

	// Audit writer (optional). When non-nil, appends an audit entry to the
	// per-rule JetStream stream on every successful write.
	// Set via SetAuditWriter before calling Run.
	auditWriter *health.AuditWriter

	// Resume support for structural and manual pauses.
	// resumeMu protects resumeCh; initResumeCh creates a fresh channel per pause cycle.
	resumeMu sync.Mutex
	resumeCh chan struct{} // non-nil only while a structural/manual pause select is in progress

	// Manual pause support (FR30, FR31).
	// manualPauseTrigger is sent to by Pause() to interrupt the running drain loop.
	// manualPauseRequested is read by the Run loop at the top of each iteration.
	manualPauseMu       sync.Mutex
	manualPauseRequested bool
	manualPauseTrigger  chan struct{} // buffered 1; initialized in New()

	// forceResumeCh allows Resume() to override an infra probe loop immediately (AC4).
	// Buffered 1; initialized in New().
	forceResumeCh chan struct{}

	// Rebuild support (optional). consumerResetter is set via SetConsumerResetter before Run.
	// pendingCons is set by Rebuild and consumed (swapped in) by Run on the next loop iteration.
	consumerResetter   ConsumerResetter // nil → rebuild unavailable
	rebuildMu          sync.Mutex
	pendingCons        jetstream.Consumer // non-nil while a rebuild-triggered swap is pending
	rebuildPollInterval time.Duration     // captured from RebuildPollInterval at construction time
}

// New creates a Pipeline for the given rule.
// adapterName is a display label for slog ("nats_kv" or "postgres").
// reporter may be nil — health KV reads/writes are skipped when nil.
// Returns an error if adpt is nil.
func New(
	ruleID, team, adapterName string,
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
		team:                team,
		adapterName:         adapterName,
		plan:                plan,
		coreKVBucket:        coreKVBucket,
		adjKV:               adjKV,
		coreKV:              coreKV,
		reporter:            reporter,
		rebuildPollInterval: iv,
		manualPauseTrigger:  make(chan struct{}, 1),
		forceResumeCh:       make(chan struct{}, 1),
	}
	p.adpt = adpt
	return p, nil
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

// SetConsumerResetter attaches a ConsumerResetter that the Rebuild method uses
// to delete and recreate the rule's durable consumer with DeliverLastPerSubjectPolicy.
// Must be called before Run. *consumer.Manager satisfies this interface.
func (p *Pipeline) SetConsumerResetter(cr ConsumerResetter) {
	p.consumerResetter = cr
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

// Run continuously processes messages from cons until ctx is cancelled.
// On startup it reads health KV to restore any previous paused state (NFR4).
// On infrastructure failure: pauses, probes for recovery, resumes (FR16, FR17).
// On structural failure: pauses until ctx is cancelled OR Resume() is called (FR19a).
// Callers must use a sync.WaitGroup to track completion for graceful shutdown.
func (p *Pipeline) Run(ctx context.Context, cons jetstream.Consumer) {
	// Restore persisted paused state on process restart (NFR4).
	if p.reporter != nil {
		if done := p.restoreHealthState(ctx); done {
			return
		}
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

	currentCons := cons
	for {
		if ctx.Err() != nil {
			return
		}

		// Check for a pending manual pause (set by Pause()).
		// Drain exits because Pause() sends to manualPauseTrigger causing mc.Stop(),
		// which makes mc.Next() error; Run falls into the default case and loops here.
		p.manualPauseMu.Lock()
		if p.manualPauseRequested {
			p.manualPauseRequested = false
			// Drain any stale trigger token. If drain() exited for an unrelated reason
			// (CatInfra / CatStructural) before the watcher goroutine could consume the
			// token, the token remains in the channel. Without this drain the next
			// drain() call would stop immediately on the stale signal.
			select {
			case <-p.manualPauseTrigger:
			default:
			}
			p.manualPauseMu.Unlock()
			resumeCh := p.initResumeCh()
			select {
			case <-ctx.Done():
				p.clearResumeCh()
				return
			case <-resumeCh:
				p.clearResumeCh()
				slog.Info("pipeline: manual pause cleared by resume",
					"ruleId", p.ruleID, "team", p.team)
			}
			// Resumed — fall through to pendingCons check and normal loop.
			continue
		}
		p.manualPauseMu.Unlock()

		// Pick up a rebuild-triggered consumer replacement (set by Rebuild).
		// This check runs before each Messages() call so the new consumer is
		// used as soon as the previous drain exits (e.g. due to consumer deletion).
		p.rebuildMu.Lock()
		if p.pendingCons != nil {
			currentCons = p.pendingCons
			p.pendingCons = nil
		}
		p.rebuildMu.Unlock()

		mc, err := currentCons.Messages(jetstream.PullHeartbeat(5 * time.Second))
		if err != nil {
			slog.Error("pipeline: open messages iterator", "ruleId", p.ruleID, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(reconnectDelay):
			}
			continue
		}

		cat, drainErr := p.drain(ctx, mc)

		switch cat {
		case failure.CatInfra:
			slog.Warn("pipeline: infrastructure failure, pausing",
				"ruleId", p.ruleID, "team", p.team, "err", drainErr)
			p.setHealthPaused(ctx, health.PauseReasonInfra, drainErr)
			cat, probeErr := p.runInfraProbeLoop(ctx)
			if cat == failure.CatStructural {
				slog.Error("pipeline: structural error during probe, pausing until resume or shutdown",
					"ruleId", p.ruleID, "team", p.team, "err", probeErr)
				p.setHealthPaused(ctx, health.PauseReasonStructural, probeErr)
				resumeCh := p.initResumeCh()
				select {
				case <-ctx.Done():
					p.clearResumeCh()
					return
				case <-resumeCh:
					p.clearResumeCh()
					slog.Info("pipeline: structural pause (probe) cleared by resume",
						"ruleId", p.ruleID, "team", p.team)
				}
				// Resumed — fall through to setHealthActive and continue the Run loop.
			}
			if probeErr != nil {
				return // ctx cancelled
			}
			p.setHealthActive(ctx)

		case failure.CatStructural:
			slog.Error("pipeline: structural failure, pausing until resume or shutdown",
				"ruleId", p.ruleID, "team", p.team, "err", drainErr)
			p.setHealthPaused(ctx, health.PauseReasonStructural, drainErr)
			resumeCh := p.initResumeCh()
			select {
			case <-ctx.Done():
				p.clearResumeCh()
				return
			case <-resumeCh:
				p.clearResumeCh()
				slog.Info("pipeline: structural pause cleared by resume",
					"ruleId", p.ruleID, "team", p.team)
			}

		default:
			// Transient or clean ctx-cancellation reconnect — continue normal loop.
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// Rebuild performs an in-place rebuild of the rule's target store. It:
//  1. Sets health KV status to "rebuilding" (AC4).
//  2. If truncate is true and the adapter implements adapter.Truncater, truncates
//     the target store before the rescan (FR29, AC2).
//  3. Resets the durable consumer to DeliverLastPerSubjectPolicy via consumerResetter,
//     so all current Core KV entries are rescanned from the beginning (FR28, AC1).
//  4. Updates the LagPoller to use the new consumer so it does not query the deleted one.
//  5. Stores the new consumer in pendingCons so Run picks it up on its next iteration.
//  6. Launches a background goroutine (watchRebuildCompletion) that transitions
//     health KV to "active" when consumer lag reaches zero (AC5).
//
// Returns nil immediately — the rebuild runs asynchronously in the Run loop.
// The caller (control service) MUST call Rebuild in its own goroutine and return
// an async ack to the operator before Rebuild returns.
func (p *Pipeline) Rebuild(ctx context.Context, truncate bool) error {
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
				return fmt.Errorf("pipeline: rebuild: truncate: %w", err)
			}
		} else {
			slog.Warn("pipeline: rebuild: truncate=true but adapter does not implement Truncater; skipping",
				"ruleId", p.ruleID)
		}
	}

	// 3. Reset consumer.
	if p.consumerResetter == nil {
		return fmt.Errorf("pipeline: rebuild: no consumer resetter configured")
	}
	newCons, err := p.consumerResetter.Reset(ctx, p.ruleID)
	if err != nil {
		return fmt.Errorf("pipeline: rebuild: reset consumer: %w", err)
	}

	// 4. Update LagPoller so it polls the new consumer (avoids errors on deleted consumer).
	if p.lagPoller != nil {
		p.lagPoller.SetConsumer(newCons)
	}

	// 5. Store new consumer; Run loop picks it up on the next iteration after drain exits.
	// Drain exits naturally because Reset calls DeleteConsumer on the old consumer,
	// which causes mc.Next() to return an error in the running drain loop.
	p.rebuildMu.Lock()
	p.pendingCons = newCons
	p.rebuildMu.Unlock()

	// 6. Launch background goroutine to transition to "active" when lag reaches zero.
	if p.reporter != nil {
		go p.watchRebuildCompletion(ctx, newCons)
	}

	return nil
}

// watchRebuildCompletion polls the given consumer's lag at rebuildPollInterval.
// When NumPending reaches zero, it calls setHealthActive to transition the
// health KV from "rebuilding" back to "active" (AC5).
func (p *Pipeline) watchRebuildCompletion(ctx context.Context, cons jetstream.Consumer) {
	ticker := time.NewTicker(p.rebuildPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := cons.Info(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// Consumer may still be initializing or context cancelled; retry.
				continue
			}
			if info.NumPending == 0 {
				p.setHealthActive(ctx)
				return
			}
		}
	}
}

// restoreHealthState reads health KV on startup and enters the appropriate pause state.
// Returns true if the caller (Run) should return immediately (ctx cancelled during restore).
// "rebuilding" status on startup means the rebuild was interrupted; it is treated as
// active — the operator must re-trigger the rebuild if desired.
func (p *Pipeline) restoreHealthState(ctx context.Context) bool {
	entry, err := p.reporter.GetStatus(ctx)
	if err != nil {
		slog.Warn("pipeline: could not read health KV on startup, assuming active",
			"ruleId", p.ruleID, "err", err)
		return false
	}
	if entry.Status != "paused" {
		// "active", "rebuilding" (interrupted), or unknown status — treat as active.
		return false
	}

	// PauseReason is *string (null when active); guard nil before dereferencing.
	// If status is "paused" but PauseReason is nil, the entry is malformed — log and treat as active.
	if entry.PauseReason == nil {
		slog.Warn("pipeline: paused health entry has nil pauseReason, treating as active",
			"ruleId", p.ruleID)
		return false
	}
	switch *entry.PauseReason {
	case health.PauseReasonInfra:
		slog.Info("pipeline: restoring infra pause from health KV, entering probe loop",
			"ruleId", p.ruleID, "team", p.team)
		cat, probeErr := p.runInfraProbeLoop(ctx)
		if cat == failure.CatStructural {
			slog.Error("pipeline: structural error during probe on restart, pausing until resume or shutdown",
				"ruleId", p.ruleID, "team", p.team, "err", probeErr)
			p.setHealthPaused(ctx, health.PauseReasonStructural, probeErr)
			resumeCh := p.initResumeCh()
			select {
			case <-ctx.Done():
				p.clearResumeCh()
				return true
			case <-resumeCh:
				p.clearResumeCh()
				slog.Info("pipeline: structural pause (probe on restart) cleared by resume",
					"ruleId", p.ruleID, "team", p.team)
				// Resumed — fall through to setHealthActive and return false so Run continues.
			}
		}
		if probeErr != nil {
			return true // ctx cancelled
		}
		p.setHealthActive(ctx)

	case health.PauseReasonStructural:
		slog.Info("pipeline: restoring structural pause from health KV, blocking until resume or shutdown",
			"ruleId", p.ruleID, "team", p.team)
		resumeCh := p.initResumeCh()
		select {
		case <-ctx.Done():
			p.clearResumeCh()
			return true
		case <-resumeCh:
			p.clearResumeCh()
			slog.Info("pipeline: structural pause cleared by resume on startup restore",
				"ruleId", p.ruleID, "team", p.team)
			// Resumed — health KV already set active by Resume(); return false so Run continues.
			return false
		}

	case health.PauseReasonManual:
		slog.Info("pipeline: restoring manual pause from health KV, blocking until resume or shutdown",
			"ruleId", p.ruleID, "team", p.team)
		resumeCh := p.initResumeCh()
		select {
		case <-ctx.Done():
			p.clearResumeCh()
			return true
		case <-resumeCh:
			p.clearResumeCh()
			slog.Info("pipeline: manual pause cleared by resume on startup restore",
				"ruleId", p.ruleID, "team", p.team)
			// Resumed — health KV already set active by Resume(); return false so Run continues.
			return false
		}

	default:
		slog.Warn("pipeline: unrecognised pause reason in health KV, assuming active",
			"ruleId", p.ruleID, "pauseReason", entry.PauseReason)
	}
	return false
}

// drain reads and processes messages from mc until ctx is cancelled, mc errors, or
// an infrastructure/structural failure signals a pause.
// Returns (Infrastructure|Structural, err) to signal a pause, or (Transient, err/nil) otherwise.
func (p *Pipeline) drain(ctx context.Context, mc jetstream.MessagesContext) (failure.Category, error) {
	stopCtx, stopDone := context.WithCancel(ctx)
	defer stopDone() // always fires mc.Stop() via the goroutines below on any exit
	go func() {
		<-stopCtx.Done()
		mc.Stop()
	}()
	// Watch for a manual pause trigger: stop the iterator so drain exits and
	// Run picks up the manualPauseRequested flag on the next loop iteration.
	go func() {
		select {
		case <-p.manualPauseTrigger:
			mc.Stop()
		case <-stopCtx.Done():
		}
	}()

	for {
		msg, err := mc.Next()
		if err != nil {
			if ctx.Err() != nil {
				return failure.CatTransient, nil
			}
			slog.Error("pipeline: receive error, will reconnect", "ruleId", p.ruleID, "err", err)
			return failure.CatTransient, err
		}

		cat, procErr := p.processMsg(ctx, msg)
		if cat == failure.CatInfra || cat == failure.CatStructural {
			return cat, procErr
		}
	}
}

// processMsg handles one Core KV message: decode → evaluate → write → ack/nak.
// Returns the failure category and error:
//   - (Transient, nil)       → success or skip (message was acked)
//   - (Transient, err)       → message was Nak'd; drain continues
//   - (Infrastructure, err)  → do NOT ack/nak; drain must stop and signal Run to pause
//   - (Structural, err)      → do NOT ack/nak; drain must stop and signal Run to pause
func (p *Pipeline) processMsg(ctx context.Context, msg jetstream.Msg) (failure.Category, error) {
	// Tombstone entries (DEL/PURGE) have empty bodies — ack and skip.
	if len(msg.Data()) == 0 {
		if err := msg.Ack(); err != nil {
			slog.Error("pipeline: ack tombstone", "ruleId", p.ruleID, "err", err)
		}
		return failure.CatTransient, nil
	}

	// Extract Core KV key from subject: "$KV.<bucket>.<key>" → "<key>".
	prefix := "$KV." + p.coreKVBucket + "."
	key := strings.TrimPrefix(msg.Subject(), prefix)

	// Classify the key by Lattice Contract #1 §1.5 shape.
	switch substrate.ClassifyKey(key) {
	case substrate.KindAspect:
		slog.Info("pipeline: aspect mutation observed but no handler registered",
			"ruleId", p.ruleID, "key", key,
			"parentVertexKey", key[:strings.LastIndex(key, ".")])
		if err := msg.Ack(); err != nil {
			slog.Error("pipeline: ack aspect key", "ruleId", p.ruleID, "key", key, "err", err)
		}
		return failure.CatTransient, nil
	case substrate.KindLink:
		slog.Info("pipeline: link mutation observed but no handler registered",
			"ruleId", p.ruleID, "key", key)
		if err := msg.Ack(); err != nil {
			slog.Error("pipeline: ack link key", "ruleId", p.ruleID, "key", key, "err", err)
		}
		return failure.CatTransient, nil
	case substrate.KindUnknown:
		slog.Warn("pipeline: unknown key shape — defect signal",
			"ruleId", p.ruleID, "key", key)
		if err := msg.Ack(); err != nil {
			slog.Error("pipeline: ack unknown key", "ruleId", p.ruleID, "key", key, "err", err)
		}
		return failure.CatTransient, nil
	}
	// KindVertex: parse type and id.
	label, _, ok := substrate.ParseVertexKey(key)
	if !ok {
		// Should not occur after ClassifyKey == KindVertex, but guard defensively.
		if err := msg.Ack(); err != nil {
			slog.Error("pipeline: ack vertex parse failure", "ruleId", p.ruleID, "key", key, "err", err)
		}
		return failure.CatTransient, nil
	}

	// Unmarshal payload.
	var props map[string]any
	if err := json.Unmarshal(msg.Data(), &props); err != nil {
		slog.Error("pipeline: unmarshal payload",
			"ruleId", p.ruleID, "team", p.team, "entityId", key,
			"stage", "pipeline", "adapter", p.adapterName, "err", err)
		if nakErr := msg.Nak(); nakErr != nil {
			slog.Error("pipeline: nak failed", "ruleId", p.ruleID, "err", nakErr)
		}
		return failure.CatTransient, err
	}

	// Edge events carry a non-empty "nodeId" field — adjacency builder handles these.
	if nodeID, _ := props["nodeId"].(string); nodeID != "" {
		if err := msg.Ack(); err != nil {
			slog.Error("pipeline: ack edge event", "ruleId", p.ruleID, "err", err)
		}
		return failure.CatTransient, nil
	}

	isDeleted, _ := props["isDeleted"].(bool)
	entry := simple.NodeEntry{
		CoreKVKey:  key,
		NodeLabel:  label,
		IsDeleted:  isDeleted,
		Properties: props,
	}

	// Evaluate.
	results, err := simple.Evaluate(ctx, p.currentPlan(), entry, p.adjKV, p.coreKV)
	if err != nil {
		slog.Error("pipeline: evaluate",
			"ruleId", p.ruleID, "team", p.team, "entityId", key,
			"stage", "traversal", "adapter", p.adapterName, "err", err)
		cat := failure.Classify(err)
		if cat == failure.CatInfra || cat == failure.CatStructural {
			// Do NOT ack or nak — leave message pending for redelivery when pipeline resumes.
			return cat, err
		}
		if cat == failure.CatTerminal {
			p.publishTerminalDLQ(ctx, msg, key, "traversal", err)
			if ackErr := msg.Ack(); ackErr != nil {
				slog.Error("pipeline: ack after terminal evaluate", "ruleId", p.ruleID, "err", ackErr)
			}
			return failure.CatTransient, nil
		}
		// Default (Transient): Nak for redelivery.
		if nakErr := msg.Nak(); nakErr != nil {
			slog.Error("pipeline: nak failed", "ruleId", p.ruleID, "err", nakErr)
		}
		return failure.CatTransient, err
	}

	// Capture the adapter once so all results in this message use the same instance.
	// HotReloadInto may swap adpt between messages; within a single message we
	// want consistent adapter behaviour across the results loop.
	adpt := p.currentAdapter()

	// Write each result to the adapter.
	var enqueuedCount, terminalCount int
	for _, result := range results {
		var writeErr error
		if result.Delete {
			writeErr = adpt.Delete(ctx, result.Keys)
		} else {
			writeErr = adpt.Upsert(ctx, result.Keys, result.Row)
		}

		if writeErr != nil {
			cat := failure.Classify(writeErr)
			op := "upsert"
			if result.Delete {
				op = "delete"
			}
			slog.Error("pipeline: "+op,
				"ruleId", p.ruleID, "team", p.team, "entityId", key,
				"stage", "write", "adapter", p.adapterName, "err", writeErr)

			if cat == failure.CatInfra || cat == failure.CatStructural {
				// Do NOT ack or nak — leave message pending for NATS AckWait redelivery.
				// The pipeline pauses; when it resumes, the message is redelivered.
				return cat, writeErr
			}
			// Terminal: permanently bad data — DLQ immediately, no retry.
			// ACK occurs after the loop so remaining results are still processed.
			if cat == failure.CatTerminal {
				p.publishTerminalDLQ(ctx, msg, key, "write", writeErr)
				terminalCount++
				continue
			}
			// Transient: enqueue for exponential-backoff retry if a queue is configured
			// and retry is enabled (maxAttempts > 0). Otherwise fall through to Nak.
			if cat == failure.CatTransient && p.retryQueue != nil && p.retryMaxAttempts > 0 {
				capturedResult := result  // explicit capture for WriteFn closure
				capturedReporter := p.reporter // capture for OnDLQPublished closure
				// Capture active sequence at enqueue time so the DLQ message stamps
				// the version that was active when the failure first occurred.
				capturedSeq := ""
				if p.reporter != nil {
					if seq := p.reporter.ActiveSequence(); seq != 0 {
						capturedSeq = fmt.Sprintf("%d", seq)
					}
				}
				e := &failure.RetryEntry{
					RuleID:       p.ruleID,
					Team:         p.team,
					EntityID:     key,
					Stage:        "write",
					RawPayload:   msg.Data(),
					RuleSequence: capturedSeq,
					WriteFn: func(rctx context.Context) error {
						if capturedResult.Delete {
							return adpt.Delete(rctx, capturedResult.Keys)
						}
						return adpt.Upsert(rctx, capturedResult.Keys, capturedResult.Row)
					},
					Attempt:     0,
					MaxAttempts: p.retryMaxAttempts,
					BaseBackoff: p.retryBaseBackoff,
					JS:          p.retryJS,
					// AC3: record error in health KV when retry escalates to DLQ.
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
				enqueuedCount++
				continue // remaining results are processed; Ack occurs at end of loop
			}
			// No retry queue, not transient, or maxAttempts==0 — Nak for redelivery.
			if nakErr := msg.Nak(); nakErr != nil {
				slog.Error("pipeline: nak failed", "ruleId", p.ruleID, "err", nakErr)
			}
			return cat, writeErr
		}

		// Write succeeded — append an audit entry (AC1, AC4: only on success).
		p.writeAudit(ctx, key, result)
	}

	// If any results were Terminal or enqueued for retry, ACK the message.
	// NATS must not redeliver: Terminal entities can never succeed, and enqueued
	// entries are tracked by the retry queue.
	if enqueuedCount > 0 || terminalCount > 0 {
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("pipeline: ack after terminal/retry enqueue", "ruleId", p.ruleID, "err", ackErr)
		}
		return failure.CatTransient, nil
	}

	slog.Info("pipeline: processed",
		"ruleId", p.ruleID, "team", p.team, "entityId", key,
		"stage", "pipeline", "adapter", p.adapterName)
	if err := msg.Ack(); err != nil {
		slog.Error("pipeline: ack failed", "ruleId", p.ruleID, "err", err)
	}
	return failure.CatTransient, nil
}

// initResumeCh creates a fresh buffered channel for the current structural pause cycle.
// Acquires resumeMu internally — callers must NOT hold resumeMu when calling this.
// Returns the channel to select on; Resume() sends on p.resumeCh.
func (p *Pipeline) initResumeCh() chan struct{} {
	p.resumeMu.Lock()
	defer p.resumeMu.Unlock()
	p.resumeCh = make(chan struct{}, 1)
	return p.resumeCh
}

// clearResumeCh sets p.resumeCh to nil, indicating the pipeline is no longer paused.
// Must be called after the structural pause select (whether resumed or ctx cancelled).
func (p *Pipeline) clearResumeCh() {
	p.resumeMu.Lock()
	p.resumeCh = nil
	p.resumeMu.Unlock()
}

// Pause signals the pipeline to halt its fetch loop and enter a manual pause state.
// Sets health KV to paused with reason "manual" and sends to manualPauseTrigger so
// the running drain loop exits at its next mc.Next() call.
// The Run loop picks up manualPauseRequested on its next iteration and waits on
// resumeCh until Resume() is called (FR30).
// Safe to call from any goroutine; calling while already paused is idempotent.
func (p *Pipeline) Pause(ctx context.Context) {
	p.setHealthPaused(ctx, health.PauseReasonManual, nil)
	p.manualPauseMu.Lock()
	p.manualPauseRequested = true
	p.manualPauseMu.Unlock()
	// Non-blocking send: if the channel already has a pending value the trigger is already set.
	select {
	case p.manualPauseTrigger <- struct{}{}:
	default:
	}
}

// Resume unblocks an active structural or manual pause, allowing the pipeline to
// re-enter its normal fetch loop. Also sets health KV to active.
// When the pipeline is in an infra probe loop, Resume() forces the probe loop to
// exit immediately so processing resumes without waiting for the next probe (AC4).
// Safe to call from any goroutine; no-op if the pipeline is not currently paused.
func (p *Pipeline) Resume(ctx context.Context) {
	p.setHealthActive(context.WithoutCancel(ctx))
	// Unblock structural/manual pause select in Run loop.
	p.resumeMu.Lock()
	if p.resumeCh != nil {
		select {
		case p.resumeCh <- struct{}{}:
		default: // already queued — no-op
		}
	}
	p.resumeMu.Unlock()
	// Unblock infra probe loop if it is running (AC4).
	select {
	case p.forceResumeCh <- struct{}{}:
	default:
	}
}

// publishTerminalDLQ publishes a DLQ message for an entity whose data is permanently
// unrecoverable (failure.CatTerminal). Uses p.retryJS — the same JetStream handle set via
// SetRetryQueue. If p.retryJS == nil (no JetStream configured), logs and returns without
// panicking, mirroring RetryQueue.escalateToDLQ.
func (p *Pipeline) publishTerminalDLQ(ctx context.Context, msg jetstream.Msg, entityID, stage string, origErr error) {
	if p.retryJS == nil {
		slog.Error("pipeline: terminal failure, no JetStream for DLQ — entity dropped",
			"ruleId", p.ruleID, "team", p.team, "entityId", entityID,
			"stage", stage, "err", origErr)
		return
	}
	// Fill RuleSequence from the reporter's cached active sequence (Story 4.1).
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
		RawPayload:   string(msg.Data()),
	}
	// Use WithoutCancel so a DLQ publish triggered during shutdown still completes.
	pubCtx := context.WithoutCancel(ctx)
	if err := failure.Publish(pubCtx, p.retryJS, p.team, p.ruleID, dlqMsg); err != nil {
		slog.Error("pipeline: terminal DLQ publish failed",
			"ruleId", p.ruleID, "team", p.team, "entityId", entityID,
			"stage", stage, "err", err)
	} else if p.reporter != nil {
		// AC3: increment health KV error count after each DLQ write.
		if recErr := p.reporter.RecordError(pubCtx, origErr.Error()); recErr != nil {
			slog.Error("pipeline: update health errorCount after terminal DLQ",
				"ruleId", p.ruleID, "err", recErr)
		}
	}
}

// runInfraProbeLoop polls the adapter at ProbeInterval until the target store responds,
// ctx is cancelled, or a structural error is discovered during probing.
// Returns (Transient, nil) when recovered, (Infrastructure, ctx.Err()) when ctx cancelled,
// or (Structural, err) if the probe reveals a structural problem (e.g. bucket deleted).
func (p *Pipeline) runInfraProbeLoop(ctx context.Context) (failure.Category, error) {
	// Drain any stale forceResumeCh signal left over from a Resume() call that
	// happened outside of an active infra-probe context. Without this drain, the
	// first iteration of the select would fire on the stale token and return
	// CatTransient without performing any probe.
	select {
	case <-p.forceResumeCh:
	default:
	}
	slog.Info("pipeline: entering probe loop", "ruleId", p.ruleID, "team", p.team)
	for {
		select {
		case <-ctx.Done():
			return failure.CatInfra, ctx.Err()
		case <-p.forceResumeCh:
			// Resume() was called while we were in the probe loop (AC4).
			// Treat as recovered so Run re-enters the normal fetch loop.
			slog.Info("pipeline: infra probe loop overridden by resume op",
				"ruleId", p.ruleID, "team", p.team)
			return failure.CatTransient, nil
		case <-time.After(ProbeInterval):
			err := p.currentAdapter().Probe(ctx)
			if err == nil {
				slog.Info("pipeline: target store recovered, resuming", "ruleId", p.ruleID, "team", p.team)
				return failure.CatTransient, nil
			}
			// Classify the probe error: a structural error (e.g. bucket deleted while paused)
			// must escalate to a permanent structural pause rather than retrying indefinitely.
			if failure.Classify(err) == failure.CatStructural {
				slog.Error("pipeline: structural error discovered during probe, escalating",
					"ruleId", p.ruleID, "team", p.team, "stage", "probe", "err", err)
				return failure.CatStructural, err
			}
			slog.Warn("pipeline: target store not yet available, probing again",
				"ruleId", p.ruleID, "team", p.team, "stage", "probe")
		}
	}
}

// setHealthPaused writes a paused health entry; errors are logged but not returned.
func (p *Pipeline) setHealthPaused(ctx context.Context, reason string, cause error) {
	if p.reporter == nil {
		return
	}
	errStr := ""
	if cause != nil {
		errStr = cause.Error()
	}
	if err := p.reporter.SetPaused(ctx, reason, errStr); err != nil {
		slog.Error("pipeline: write health paused", "ruleId", p.ruleID, "err", err)
	}
}

// setHealthActive writes an active health entry; errors are logged but not returned.
func (p *Pipeline) setHealthActive(ctx context.Context) {
	if p.reporter == nil {
		return
	}
	if err := p.reporter.SetActive(ctx); err != nil {
		slog.Error("pipeline: write health active", "ruleId", p.ruleID, "err", err)
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

// runAdjWatch watches the Materializer-owned adjacency KV for new or updated
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

	// Point-read the Core KV node. Materializer is a pure consumer of Core KV —
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

	results, err := simple.Evaluate(ctx, p.currentPlan(), entry, p.adjKV, p.coreKV)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Warn("pipeline: adj watch: evaluate",
			"ruleId", p.ruleID, "key", nodeKey, "err", err)
		return
	}

	adpt := p.currentAdapter()
	for _, result := range results {
		var writeErr error
		if result.Delete {
			writeErr = adpt.Delete(ctx, result.Keys)
		} else {
			writeErr = adpt.Upsert(ctx, result.Keys, result.Row)
		}
		if writeErr != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("pipeline: adj watch: write",
				"ruleId", p.ruleID, "team", p.team, "key", nodeKey, "err", writeErr)
			// Continue — remaining results are independent; adapter writes are idempotent.
			continue
		}
		slog.Info("pipeline: adj watch: re-evaluated",
			"ruleId", p.ruleID, "team", p.team, "entityId", nodeKey,
			"stage", "pipeline", "adapter", p.adapterName)
	}
}

