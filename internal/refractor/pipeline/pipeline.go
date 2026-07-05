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

	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/failure"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
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
	coreKVBucket string // Core KV bucket name; used to strip the $KV prefix from subjects
	adjKV        *substrate.KV
	coreKV       *substrate.KV

	// engineKind is set to ruleengine.EngineFull by UseFullEngine (called for
	// every activated lens). fullCR is the compiled rule UseFullEngine
	// installed; envelopeFn (when non-nil) rewrites each projection row into
	// the on-wire envelope expected by the adapter target (e.g. Contract #6
	// §6.2 Capability KV shape).
	engineKind string
	fullEngine *full.Engine
	fullCR     ruleengine.CompiledRule
	envelopeFn EnvelopeFn

	// plainReprojectLabels is the exhaustive set of vertex types this lens's
	// patterns can bind (full.CompiledRule.ReferencedLabels), used by the
	// plain aspect/link reprojection arms to skip events on types the lens
	// cannot read. plainReprojectAll disables the skip when the set is not
	// exhaustive (unlabeled node pattern / var-length relationship) — every
	// event reprojects.
	plainReprojectLabels map[string]struct{}
	plainReprojectAll    bool

	// diffRetraction opts a plain lens into Fire 3's neighbor-driven / multi-row
	// target-diff retraction (negative-filter-retraction-projection-design.md
	// §2.4): when Fire 2's read-free AnchorProjectionKey check cannot derive a
	// single anchor-keyed row (a composite key with a column bound to a
	// non-anchor variable — e.g. landlordLeaseApplicationsRead's landlord_id,
	// resolved by walking a `manages` link off the unit, not the leaseapp
	// anchor), the re-execute's fresh row set is instead diffed against the
	// adapter's full live key set (adapter.KeyLister) and every key the target
	// still carries but the fresh computation no longer produces is retracted.
	// False by default — a convergence (`violating`-flag) lens is ALSO a plain,
	// often multi-row lens, and must never have rows silently retracted; only a
	// lens that explicitly opts in (SetDiffRetraction) pays this cost.
	diffRetraction bool

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

	// secureDecryptor decrypts a Secure Lens's declared secure columns after
	// evaluation, before any write path (Contract #3 §3.10). Nil for every
	// non-secure lens — rows pass through untouched.
	secureDecryptor *SecureDecryptor

	// latencyBuf captures the (CDC → projection-write) latency per event
	// so the heartbeat can compute mean/p95/p99 per Lens. Nil disables.
	latencyBuf *LatencyRingBuffer
	adapterMu  sync.RWMutex    // protects adpt for concurrent hot-reload
	adpt       adapter.Adapter // access via currentAdapter(); swap via HotReloadInto

	reporter *health.Reporter // nil → skip health KV operations (optional)

	// Retry queue (optional). When non-nil and retryMaxAttempts > 0, transient write
	// failures are enqueued for exponential-backoff retry instead of Nak'd.
	// Set via SetRetryQueue before calling Run.
	retryQueue       *failure.RetryQueue
	retryMaxAttempts int
	retryBaseBackoff time.Duration
	retryConn        *substrate.Conn // substrate connection for DLQ escalation after retry exhaustion

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

	// progressMu guards lastAppliedSeq / lastProjectedAt — the lens's
	// projection-liveness clocks (lens-projection-liveness-design.md §3.1).
	progressMu sync.Mutex
	// lastAppliedSeq is the Core KV stream sequence of the last event this
	// consumer acked, including ack-and-skip. Advances whenever the lens
	// consumes anything; a wedged consumer (delivering nothing) leaves it frozen.
	lastAppliedSeq uint64
	// lastProjectedAt is the wall-clock of the last successful target write.
	// Advances only on real output, so a caught-up-but-no-op consumer leaves it
	// frozen even as lastAppliedSeq moves. Zero until the first projection.
	lastProjectedAt time.Time
}

// ProjectionProgress is the lens's forward-progress snapshot for the health
// plane (lens-projection-liveness-design.md §3.1).
type ProjectionProgress struct {
	LastAppliedSeq  uint64
	LastProjectedAt time.Time
}

// Progress returns the pipeline's current forward-progress snapshot.
// Thread-safe; read by the LagPoller each cycle.
func (p *Pipeline) Progress() ProjectionProgress {
	p.progressMu.Lock()
	defer p.progressMu.Unlock()
	return ProjectionProgress{LastAppliedSeq: p.lastAppliedSeq, LastProjectedAt: p.lastProjectedAt}
}

// recordAppliedSeq advances the consumer's forward cursor. Called for every
// acked message (including ack-and-skip), never for a Nak (redelivery means
// the message has not actually been consumed yet).
func (p *Pipeline) recordAppliedSeq(seq uint64) {
	p.progressMu.Lock()
	p.lastAppliedSeq = seq
	p.progressMu.Unlock()
}

// recordProjected stamps the read-model's last-touch clock. Called only after
// a successful adapter write (Create/Update/Delete actually reaching the
// target) — never on ack-and-skip or a write error.
func (p *Pipeline) recordProjected() {
	p.progressMu.Lock()
	p.lastProjectedAt = time.Now()
	p.progressMu.Unlock()
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
	coreKVBucket string,
	adjKV, coreKV *substrate.KV,
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
		coreKVBucket:        coreKVBucket,
		adjKV:               adjKV,
		coreKV:              coreKV,
		reporter:            reporter,
		rebuildPollInterval: iv,
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
	// Pin the vertex types this lens's patterns can bind, so the plain
	// aspect/link reprojection arms skip events on types the lens cannot
	// read (an unbounded label set — unlabeled node pattern or var-length
	// relationship — disables the skip; every event reprojects).
	p.plainReprojectLabels = nil
	p.plainReprojectAll = true
	if fullCR, isFull := cr.(*full.CompiledRule); isFull {
		if labels, exhaustive := fullCR.ReferencedLabels(); exhaustive {
			p.plainReprojectLabels = labels
			p.plainReprojectAll = false
		}
	}
}

// plainReactsTo reports whether the plain aspect/link reprojection arms should
// re-execute this lens for an event whose owner/endpoint vertex has the given
// type. A lens with an exhaustive label set reprojects only for types its
// patterns can bind.
func (p *Pipeline) plainReactsTo(vertexType string) bool {
	if p.engineKind != ruleengine.EngineFull {
		return false
	}
	if p.plainReprojectAll {
		return true
	}
	_, ok := p.plainReprojectLabels[vertexType]
	return ok
}

// SetEnvelopeFn installs the on-wire envelope wrapper. Pass nil to clear.
// Must be called before Run.
func (p *Pipeline) SetEnvelopeFn(fn EnvelopeFn) {
	p.envelopeFn = fn
}

// SetDiffRetraction opts this plain lens into Fire 3's target-diff retraction
// (see the diffRetraction field doc). Must be called before Run.
func (p *Pipeline) SetDiffRetraction(enabled bool) {
	p.diffRetraction = enabled
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

// SetSecureDecryptor installs the Secure-Lens decrypt-at-projection transform
// (Contract #3 §3.10). Pass nil to clear. Must be called before Run.
func (p *Pipeline) SetSecureDecryptor(d *SecureDecryptor) {
	p.secureDecryptor = d
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

// SetRetryQueue configures the pipeline to use q for transient write failure retry.
// maxAttempts is the maximum number of retry attempts before DLQ escalation (0 = no retry).
// baseBackoff is the base exponential-backoff duration (doubles each attempt).
// conn is the substrate connection used to publish DLQ messages on exhaustion (may be nil if DLQ is not needed).
// Must be called before Run.
func (p *Pipeline) SetRetryQueue(q *failure.RetryQueue, conn *substrate.Conn, maxAttempts int, baseBackoff time.Duration) {
	p.retryQueue = q
	p.retryConn = conn
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

// Pending returns the supervised consumer's pending (un-delivered) message
// count — the lens's consumer lag. It returns an error before RunOn (no
// supervisor) and during the brief startup window before Run registers the
// consumer with the supervisor (PendingForConsumer reports "not managed"); the
// lag poller treats either as "skip this cycle". This is the substrate-typed
// replacement for reading NumPending off a raw NATS consumer handle.
func (p *Pipeline) Pending(ctx context.Context) (uint64, error) {
	if p.supervisor == nil {
		return 0, fmt.Errorf("pipeline: pending: no supervisor configured (RunOn not called)")
	}
	return p.supervisor.PendingForConsumer(ctx, p.consumerCfg.Name)
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
	spec.Handler = p.handleTracked
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

	// 2. Optional target-store truncation. A guarded bucket forces truncate: its
	// monotonic watermarks would reject a lower-seq historical replay, leaving
	// rejected-write holes. Truncating clears the watermarks with the data so the
	// stream replays from empty and the highest-seq write wins, yielding a steady
	// state identical to a from-scratch projection (Contract #6 §6.2). The force
	// keys off Guarded() so the pipeline never learns lens canonical names.
	if g, ok := p.currentAdapter().(interface{ Guarded() bool }); ok && g.Guarded() && !truncate {
		slog.Info("pipeline: rebuild: guarded bucket forces truncate (avoids rejected-write holes)",
			"ruleId", p.ruleID)
		truncate = true
	}
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

// handleTracked wraps handle to advance the projection-liveness forward
// cursor (lastAppliedSeq) on every Ack — including ack-and-skip — but never on
// Nak (redelivery means the message has not actually been consumed yet).
func (p *Pipeline) handleTracked(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
	decision, err := p.handle(ctx, msg)
	if decision == substrate.Ack {
		p.recordAppliedSeq(msg.Sequence)
	}
	return decision, err
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
		// An aspect-only mutation (e.g. identity .state, unit .listing, a
		// piiKey shred) changes a vertex's projected state with no
		// vertex-root event. On the actor-aware (capability) pipeline it
		// drives a fan-out reprojection seeded from the parent vertex. On a
		// plain lens it re-executes seeded from the owner vertex — refreshing
		// the row's aspect-derived fields (a Secure Lens's piiKey shred
		// scrubs projected plaintext to null this way) and, when the
		// mutation drops the owner out of the matched set (a WHERE flip /
		// keyed-aspect deletion), retracting its row via the evaluate path's
		// filter-retraction presence check.
		if p.actorEnumerator != nil {
			return p.evalAspectFanOut(ctx, msg, key)
		}
		return p.evalPlainAspectReprojection(ctx, msg, key)
	case substrate.KindLink:
		// A pure link mutation (holdsRole/manages/appliesToUnit/...) changes
		// graph topology with no vertex event. On the actor-aware pipeline it
		// drives a fan-out reprojection from both endpoints; on a plain lens
		// it re-executes seeded from each endpoint vertex — refreshing
		// link-derived rows and retracting an endpoint anchor that a
		// required-link removal drops from the matched set.
		if p.actorEnumerator != nil {
			return p.evalLinkFanOut(ctx, msg, key, tombstone)
		}
		return p.evalPlainLinkReprojection(ctx, msg, key)
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
	entry := ruleengine.NodeEntry{
		CoreKVKey:  key,
		NodeLabel:  label,
		IsDeleted:  isDeleted,
		Properties: props,
	}

	// Evaluate against the full engine ([]ProjectionResult{Key,Values,Delete}).
	// evaluateForEntry normalises and applies the envelope so the downstream
	// write path sees a single []ruleengine.EvalResult shape.
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
	if err := p.applySecureDecrypt(ctx, results); err != nil {
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err)
	}

	return p.writeResults(ctx, msg, key, results)
}

// evalPlainAspectReprojection handles a KindAspect CDC event on a plain
// (non-actor-aware) lens: re-evaluate the owner (parent) vertex so its
// projected row reflects the aspect's current state — a changed field
// refreshes, a Secure Lens's piiKey shred scrubs plaintext to null
// (decrypt fails ErrKeyShredded → secure columns project null), and an
// aspect mutation that drops the owner out of the matched set retracts its
// row through the evaluate path's filter-retraction presence check. The
// aspect body is irrelevant (the re-execute reads current Core KV state),
// so a tombstone and a value change take the same path. An owner whose type
// doesn't bind the lens's MATCH evaluates to zero rows with no derivable
// anchor key (harmless no-op); a missing/tombstoned owner is skipped — row
// deletion belongs to the anchor-tombstone path.
func (p *Pipeline) evalPlainAspectReprojection(ctx context.Context, msg substrate.Message, key string) (substrate.Decision, error) {
	parentVtx, parentType, _, _, ok := substrate.ParseAspectKey(key)
	if !ok {
		return substrate.Ack, nil
	}
	if !p.plainReactsTo(parentType) {
		// The lens's patterns cannot bind this vertex type — the mutation
		// cannot change its rows; skip the re-execute.
		return substrate.Ack, nil
	}
	results, err := p.evaluatePlainFromVertex(ctx, parentVtx, parentType)
	if err != nil {
		slog.Error("pipeline: plain aspect reprojection: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err)
	}
	return p.writeResults(ctx, msg, key, results)
}

// evalPlainLinkReprojection handles a KindLink CDC event on a plain
// (non-actor-aware) lens: re-evaluate both endpoint vertices so rows derived
// through the link refresh, and an endpoint anchor that a required-link
// removal drops from the matched set retracts through the evaluate path's
// filter-retraction presence check. A link tombstone (empty body, or a body
// with isDeleted) and a link create take the same evaluate path — the
// re-execute reads current adjacency either way. Results are deduplicated
// across the two endpoint evaluations (a whole-type-scan lens re-derives the
// same row set from each seed).
//
// Like the actor-aware link fan-out, the link is first idempotently applied
// to adjKV (both directional entries, the link key as EdgeID — matching the
// dedicated adjacency consumer's events exactly): the two consumers observe
// the same CDC event with no cross-consumer ordering guarantee, and without
// this a tombstone's re-execute could still see the removed edge and miss the
// retraction until the adjacency watch heals it.
func (p *Pipeline) evalPlainLinkReprojection(ctx context.Context, msg substrate.Message, key string) (substrate.Decision, error) {
	type1, id1, linkName, type2, id2, ok := substrate.ParseLinkKey(key)
	if !ok {
		return substrate.Ack, nil
	}
	reacts1, reacts2 := p.plainReactsTo(type1), p.plainReactsTo(type2)
	if !reacts1 && !reacts2 {
		// Neither endpoint type is bindable by the lens's patterns — the
		// link cannot appear in its traversals; skip (including the
		// adjacency self-apply: the dedicated consumer owns the index, this
		// lens just doesn't need it applied-before-read).
		return substrate.Ack, nil
	}
	isDeleted := len(msg.Body) == 0
	if !isDeleted {
		var linkProps map[string]any
		if err := json.Unmarshal(msg.Body, &linkProps); err != nil {
			// A malformed link body can never parse on redelivery — Terminal
			// (DLQ), matching the dedicated adjacency consumer's disposition
			// for the identical message. A bare Nak here would poison-loop
			// every plain pipeline on one bad body.
			slog.Error("pipeline: plain link reprojection: unmarshal payload",
				"ruleId", p.ruleID, "entityId", key, "err", err)
			return p.dispositionEvalErr(ctx, msg, key, "decode",
				failure.Terminal(fmt.Errorf("pipeline: plain link reprojection: unmarshal %q: %w", key, err)))
		}
		isDeleted, _ = linkProps["isDeleted"].(bool)
	}
	for _, evt := range []adjacency.CoreKVEvent{
		{CoreKvKey: key, EdgeID: key, Name: linkName, Direction: "outbound",
			NodeID: id1, OtherNodeID: id2, OtherType: type2, IsDeleted: isDeleted},
		{CoreKvKey: key, EdgeID: key, Name: linkName, Direction: "inbound",
			NodeID: id2, OtherNodeID: id1, OtherType: type1, IsDeleted: isDeleted},
	} {
		if err := adjacency.Build(ctx, p.adjKV, evt); err != nil {
			slog.Error("pipeline: plain link reprojection: adjacency build",
				"ruleId", p.ruleID, "entityId", key, "err", err)
			return p.dispositionEvalErr(ctx, msg, key, "traversal", err)
		}
	}
	endpoints := []struct {
		vtx, label string
		reacts     bool
	}{
		{"vtx." + type1 + "." + id1, type1, reacts1},
		{"vtx." + type2 + "." + id2, type2, reacts2},
	}
	var combined []ruleengine.EvalResult
	seen := make(map[string]bool)
	for _, ep := range endpoints {
		if !ep.reacts {
			continue
		}
		results, err := p.evaluatePlainFromVertex(ctx, ep.vtx, ep.label)
		if err != nil {
			slog.Error("pipeline: plain link reprojection: evaluate",
				"ruleId", p.ruleID, "entityId", key, "endpoint", ep.vtx,
				"stage", "traversal", "err", err)
			return p.dispositionEvalErr(ctx, msg, key, "traversal", err)
		}
		for _, r := range results {
			id := dedupeKeyFor(r)
			if seen[id] {
				continue
			}
			seen[id] = true
			combined = append(combined, r)
		}
	}
	return p.writeResults(ctx, msg, key, combined)
}

// evaluatePlainFromVertex point-reads a vertex and runs the plain evaluate
// path seeded from it — the shared core of the plain aspect/link reprojection
// arms. A missing or tombstoned vertex yields (nil, nil): its row lifecycle
// belongs to the vertex-root event path.
func (p *Pipeline) evaluatePlainFromVertex(ctx context.Context, vtxKey, vtxType string) ([]ruleengine.EvalResult, error) {
	props, err := p.fetchVertexProps(ctx, vtxKey)
	if err != nil {
		return nil, err
	}
	if props == nil {
		return nil, nil
	}
	entry := ruleengine.NodeEntry{
		CoreKVKey:  vtxKey,
		NodeLabel:  vtxType,
		Properties: props,
	}
	return p.evaluateForEntry(ctx, entry)
}

// dedupeKeyFor returns a canonical identity for an EvalResult's target key
// (encoding/json sorts map keys, so the marshalled form is deterministic).
// The Delete flag is part of the identity so a Delete and an Upsert for the
// same key are never conflated.
func dedupeKeyFor(r ruleengine.EvalResult) string {
	b, err := json.Marshal(r.Keys)
	if err != nil {
		// Un-marshallable key values cannot occur for engine-produced rows
		// (scalars from JSON); fall back to the fmt rendering.
		return fmt.Sprintf("%t|%v", r.Delete, r.Keys)
	}
	return fmt.Sprintf("%t|%s", r.Delete, b)
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
	if err := p.applySecureDecrypt(ctx, results); err != nil {
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
func (p *Pipeline) writeResults(ctx context.Context, msg substrate.Message, key string, results []ruleengine.EvalResult) (substrate.Decision, error) {
	adpt := p.currentAdapter()
	var retryResults []ruleengine.EvalResult
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

		p.recordProjected()
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
func (p *Pipeline) enqueueRetry(key string, rawPayload []byte, result ruleengine.EvalResult) {
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
		Conn:        p.retryConn,
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

// Delete removes this rule's projected row for keys, via the currently-active
// adapter (adapter.Delete — the same hard/soft-delete path a vertex tombstone
// takes, adapter/postgres.go and adapter/natskv.go). Used by the Refractor
// KeyShredded nullification listener (control.RowNullifier) to scrub a
// shredded identity's row out-of-band, independent of the rule's own CDC
// stream. Safe to call from any goroutine; the adapter itself is idempotent
// (deleting an absent row/key is a no-op).
func (p *Pipeline) Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error {
	return p.currentAdapter().Delete(ctx, keys, projectionSeq)
}

// publishTerminalDLQ publishes a DLQ message for an entity whose data is permanently
// unrecoverable (failure.CatTerminal). Uses p.retryConn — the same substrate connection set via
// SetRetryQueue. If p.retryConn == nil (no connection configured), logs and returns without
// panicking, mirroring RetryQueue.escalateToDLQ. rawBody is the message body
// stored as the DLQ rawPayload.
func (p *Pipeline) publishTerminalDLQ(ctx context.Context, rawBody []byte, entityID, stage string, origErr error) {
	if p.retryConn == nil {
		slog.Error("pipeline: terminal failure, no connection for DLQ — entity dropped",
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
	if err := failure.Publish(pubCtx, p.retryConn, p.ruleID, dlqMsg); err != nil {
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
func (p *Pipeline) writeAudit(ctx context.Context, entityID string, result ruleengine.EvalResult) {
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
		updates, err := p.adjKV.WatchUpdates(ctx)
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
		p.drainAdjWatch(ctx, updates)
	}
}

// drainAdjWatch reads adjacency-change events until the channel closes (the
// watcher stopped — runAdjWatch reconnects) or ctx is done. The substrate watch
// already filters the end-of-replay nil sentinel, so every event is a real
// mutation.
func (p *Pipeline) drainAdjWatch(ctx context.Context, updates <-chan substrate.KVEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-updates:
			if !ok {
				// Channel closed — watcher stopped; runAdjWatch will reconnect.
				return
			}
			p.handleAdjUpdate(ctx, evt.Key)
		}
	}
}

// handleAdjUpdate processes one adjacency KV change keyed by adjKey. It strips
// the "adj." prefix to recover the Core KV node key, fetches the node value
// directly (read-only), and hands off to handleAdjNode for parsing,
// re-evaluation, and write.
func (p *Pipeline) handleAdjUpdate(ctx context.Context, adjKey string) {
	const adjPrefix = "adj."
	nodeKey := strings.TrimPrefix(adjKey, adjPrefix)
	if nodeKey == adjKey {
		// Key does not start with "adj." — unexpected format, skip.
		return
	}

	// Point-read the Core KV node. Refractor is a pure consumer of Core KV —
	// this is a read, not a write (ADR-16).
	coreEntry, err := p.coreKV.Get(ctx, nodeKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
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

	data := coreEntry.Value
	if len(data) == 0 {
		// Tombstone — node was deleted; skip (the normal consumer handles deletes).
		return
	}

	p.handleAdjNode(ctx, nodeKey, data)
}

// handleAdjNode parses a Core KV node body fetched by handleAdjUpdate and
// re-evaluates it through the normal evaluate-and-write path. Split out of
// handleAdjUpdate so the parse/evaluate/write arms below are reachable
// without a seeded Core KV read.
func (p *Pipeline) handleAdjNode(ctx context.Context, nodeKey string, data []byte) {
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
	entry := ruleengine.NodeEntry{
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
		p.recordProjected()
		slog.Info("pipeline: adj watch: re-evaluated",
			"ruleId", p.ruleID, "entityId", nodeKey,
			"stage", "pipeline", "adapter", p.adapterName)
	}
}
