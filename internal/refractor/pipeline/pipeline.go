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

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// maxNarrowedFilterLabels caps how many referenced labels ConsumerFilter will
// derive a narrowed FilterSubjects set from. Each label expands to up to 3
// filter-subject forms (subjects.CoreKVNarrowedFilters), so this bounds how
// large the FilterSubjects slice JetStream evaluates per delivered message
// gets before the broad filter — simpler, and just as fail-safe — is the
// better choice.
const maxNarrowedFilterLabels = 8

// maxNarrowedFilterSubjects caps the TOTAL filter-subject count of a
// relation-narrowed set, whose size is |labels| x (1 + 2|relations|) and so is
// no longer bounded by maxNarrowedFilterLabels alone. It is set to exactly the
// relation-blind ceiling (maxNarrowedFilterLabels x 3), so no lens that narrows
// today can stop narrowing because the relation dimension was added: a lens
// over budget here falls back to the relation-blind narrowed set, and only a
// lens over the LABEL budget falls all the way back to the broad filter.
const maxNarrowedFilterSubjects = maxNarrowedFilterLabels * 3

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

	// fullCRBranches carries a multi-walk Personal lens's N independently-
	// compiled branches (refractor-shared-keyspace-arbitration-design.md
	// §13.2) — nil for every other lens, which keep evaluating fullCR alone.
	// fullCR is always set to branches[0] too when this is populated, so any
	// single-field consumer (the plain-reprojection anchor helpers, which
	// never run on a Personal lens's actor-enumerator path) still sees a
	// valid compiled rule.
	fullCRBranches []ruleengine.CompiledRule

	// fullCRWalkOwnedColumns maps each RETURN column full.
	// ClassifyBranchReturnColumns proved reference only one branch's own
	// bound variables (ColumnWalkOwned) to that owning branch's index — nil
	// for a non-multi-walk lens, or when classification fails (defensive:
	// mergeRowGroup then falls back to requiring every column to agree,
	// exactly as before this field existed). branchmerge.go uses it to tell a
	// walk's own cypher fanning out per anchor (e.g. a multi-hat actor
	// reaching one op via 2+ roles, each producing a row with a different
	// walk-owned column value) from a genuine cross-walk disagreement (always
	// a defect) — only the former is safe to resolve deterministically, since
	// a walk-owned column can only ever be non-null from the one branch that
	// owns it. The owner index (not just a bool) matters when two DIFFERENT
	// branches each own a different walk-owned column: those columns must be
	// resolved independently, never coupled into one shared pick.
	fullCRWalkOwnedColumns map[string]int

	// multiEnvelopeFn is envelopeFn's per-entry sibling (§4.1 of
	// cap-read-per-anchor-grant-keys-design.md). Mutually exclusive with
	// envelopeFn — SetEnvelopeFn / SetMultiEnvelopeFn each clear the other, so
	// executeFullForActor's dispatch on "whichever is non-nil" never sees both
	// set at once.
	multiEnvelopeFn MultiEnvelopeFn

	// plainReprojectLabels is the exhaustive set of vertex types this lens's
	// patterns can bind (full.CompiledRule.ReferencedLabels), used by the
	// plain aspect/link reprojection arms to skip events on types the lens
	// cannot read. plainReprojectAll disables the skip when the set is not
	// exhaustive (unlabeled node pattern / var-length relationship) — every
	// event reprojects.
	plainReprojectLabels map[string]struct{}
	plainReprojectAll    bool

	// plainReprojectRelations is the exhaustive set of link relations this
	// lens's patterns can traverse (full.CompiledRule.ReferencedRelations) —
	// the `<relation>` segment of Contract #1's
	// lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>. The label set above bounds
	// which endpoint TYPES can bind; this bounds which relations between them
	// the lens actually walks, which is what decides whether a link can appear
	// in its traversals at all.
	//
	// plainRelationsExhaustive is stated in the POSITIVE, unlike
	// plainReprojectAll beside it, and deliberately: an EMPTY relation set is
	// the STRONGEST narrowing there is (a lens with no relationship pattern
	// cannot be affected by any link), so an empty set cannot double as the
	// fail-safe the way an empty LABEL set does for ConsumerFilter. The zero
	// value must therefore read as "not exhaustive — every relation
	// reprojects", which is what a positive flag gives to any Pipeline that has
	// not run UseFullEngineBranches.
	//
	// Derived in the same UseFullEngineBranches call as the label set, so a
	// MATCH hot-reload can never leave one stale against the other.
	plainReprojectRelations  map[string]struct{}
	plainRelationsExhaustive bool

	// ruleMu guards every field useFullEngineBranches writes — engineKind,
	// fullEngine, fullCR, fullCRBranches, fullCRWalkOwnedColumns, the two
	// plainReproject pairs above, and seedAnchorLabel. Those are the only
	// fields on this struct rewritten AFTER Run: a MATCH hot-reload
	// (cmd/refractor/reload.go) calls UseFullEngineBranches on a LIVE pipeline
	// from CoreKVSource's dispatch goroutine while the consumer goroutine is
	// inside handle reading them.
	//
	// Every OTHER field is either install-time only (set during activation
	// before Run — the whole evaluate-shape set: actorEnumerator,
	// patternClosedOutput, sweeper, secureDecryptor, both envelope fns,
	// diffRetraction) or carries its own guard: adpt and requireGuardedAdapter
	// under adapterMu, the progress and rebuild counters under their own
	// mutexes and atomics. A new field belongs to one of those three classes;
	// one written after Run with no guard is a bug.
	//
	// Read through ruleState(), never field-by-field. Two properties are the
	// whole fix:
	//
	//   - The rewrite happens under ONE Lock, so the set flips atomically —
	//     which is what the relation pair's doc above already claims and what
	//     separate unsynchronized stores never actually delivered. That matters
	//     most for that pair, because unlike the label pair it does not fail
	//     safe: plainRelationsExhaustive is stated positively, so a reader
	//     seeing the new flag against the not-yet-published map would judge
	//     every link against an EMPTY exhaustive set and skip all of them.
	//   - A snapshot stays coherent for a whole event. Without one a reload
	//     landing mid-event could execute the new rule while deriving the
	//     anchor's projection key from the old, retracting a row the new rule
	//     legitimately produces under a different key.
	//
	// The maps and slices are COPY-ON-WRITE: useFullEngineBranches builds fresh
	// ones every call and never mutates one it has already published, so a
	// snapshot's maps remain safe to read after the lock is dropped. An edit
	// that mutates a published map in place breaks that and must copy instead.
	ruleMu sync.RWMutex

	// ruleGen counts rule publications, and rides in every snapshot so the CDC
	// write path can ask whether the rule it evaluated under is still in force
	// — see supersededRule. Guarded by ruleMu with the set above, because it
	// must advance in the same critical section that swaps the rule.
	ruleGen uint64

	// seedAnchorLabel is this lens's anchor vertex type
	// (full.CompiledRule.AnchorLabel — the first MATCH clause's first node's
	// label), derived once per engine install alongside plainReprojectLabels.
	// An event on this type is a mutation of the anchor itself, so the
	// evaluation it triggers can be narrowed to that one anchor instead of
	// recomputing every anchor's rows (refractor-footprint-reduction-design.md
	// §D2 Phase 1). Empty disables seeding entirely: a multi-walk lens (whose
	// branches' anchors need not agree), a non-full engine, or an unlabeled
	// anchor pattern (which no event type identifies).
	//
	// It holds the AST-derived half of eligibility only. The pipeline-SHAPE
	// half (no ActorEnumerator, no envelope, DiffRetraction off) is evaluated
	// per event in seedAnchorFor, because activation installs those components
	// AFTER UseFullEngine (cmd/refractor/main.go) — a snapshot taken here
	// would read every one of them unset and arm seeding for lenses that must
	// never have it.
	seedAnchorLabel string

	// anchorHops is the compiled pattern GRAPH the affected-anchor derivation
	// walks under (auth-plane-projection-latency-design.md §4.7). Guarded by
	// ruleMu and republished on every rule swap alongside seedAnchorLabel, so a
	// hot-reload can never leave a previous rule body's graph armed. Its zero
	// value is Complete == false, which is the fail-closed answer: fall back to
	// the enumerator's BFS.
	anchorHops full.HopIndex

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

	// derivShadow tallies the pattern-directed affected-anchor derivation — its
	// agreement with the enumerator under `shadow`, and how often it answered
	// at all under `act` (anchor_derivation_shadow.go).
	derivShadow derivationShadow

	// derivMode is this pipeline's override of how the derivation participates
	// in a fan-out. Zero is DerivationModeUnset, i.e. take the package default
	// (anchor_derivation_mode.go).
	derivMode atomic.Int64

	// derivReadCap bounds the adjacency documents one derivation walk may read
	// before it gives up and falls back. Zero means DefaultDerivationReadCap.
	derivReadCap atomic.Int64

	// actorDeleteKey derives the Capability KV target key to delete when an
	// actor disappears (tombstone shortcut and reprojectActors missing-actor
	// path). It maps an actor vertex key to the key this lens's envelope
	// projects to, so each lens removes the key it actually owns. Nil falls
	// back to capabilityKeyForActor (cap.<actor>), the primary lens's shape.
	actorDeleteKey func(actorKey string) string

	// zeroRowRetraction arms the doc-mode zero-row retraction transport
	// (executeFullForActorOnce): an actor whose evaluation succeeds and
	// yields zero result rows synthesizes the same delete EvalResult the
	// tombstone shortcut emits, guarded by a live presence check against the
	// current adapter (zeroRowDeleteKey). Set by
	// projection.InstallActorAggregate for a doc-mode (EntryKeyColumn=="")
	// descriptor whose empty behavior is delete/softDelete
	// (OutputDescriptor.RequiresGuardedTombstone) — false for every other
	// lens, which leaves its existing behavior untouched. A perEntry lens
	// retracts through its own prefix-diff (multiEntryRetractions) instead.
	zeroRowRetraction bool

	// secureDecryptor decrypts a Secure Lens's declared secure columns after
	// evaluation, before any write path (Contract #3 §3.10). Nil for every
	// non-secure lens — rows pass through untouched.
	secureDecryptor *SecureDecryptor

	// patternClosedOutput reports whether this lens's row for an anchor is a
	// function solely of the subgraph its compiled pattern binds from that
	// anchor — the soundness precondition for skipping an event on a type the
	// pattern cannot bind (auth-plane-projection-latency-design.md §4.1). Only
	// projection.InstallActorAggregate sets it: a Personal Lens consults two
	// inputs outside the compiled pattern (the D1 read gate cap-read.<domain>.
	// <actor> and the Interest Set, projection/personal.go), so an actor whose
	// grants widen must keep receiving the role event that widened them.
	//
	// False by default, which is the unsafe-side value on purpose: a pipeline
	// installed through any path that does not assert pattern-closure keeps
	// today's broad behavior rather than silently narrowing.
	patternClosedOutput bool

	// authPlane reports whether this lens projects an authorization surface
	// (projection.IsAuthPlane). Combined with actorAggregate (envelopeFn or
	// multiEnvelopeFn installed) and requiresFootprintValidation, it is the
	// scope predicate for footprint validation (executeFullForActor,
	// refractor-evaluation-consistency-design.md §13.3): only a lens
	// matching all three pays the re-read cost. False by default — every
	// lens installed without SetAuthPlane gets no validation.
	authPlane bool

	// requiresFootprintValidation reports whether this lens's compiled cypher
	// emits at least one multi-binding conjunct unit (projection.Compile's
	// derived ProjectionPlan.RequiresFootprintValidation,
	// refractor-evaluation-consistency-design.md §13.3) — the third conjunct
	// of the footprint-validation scope predicate. A lens installed without
	// SetRequiresFootprintValidation defaults to false, i.e. no validation —
	// callers outside projection.InstallActorAggregate that want validation
	// must set it explicitly, mirroring authPlane's own default.
	requiresFootprintValidation bool

	// latencyBuf captures the (CDC → projection-write) latency per event
	// so the heartbeat can compute mean/p95/p99 per Lens. Nil disables.
	latencyBuf *LatencyRingBuffer
	adapterMu  sync.RWMutex    // protects adpt for concurrent hot-reload
	adpt       adapter.Adapter // access via currentAdapter(); swap via HotReloadInto

	// requireGuardedAdapter records that this lens's writes must run under the
	// §6.2 monotonic projection-write guard. The guard is a property of the
	// LENS (an auth-plane surface, or an empty-behavior soft tombstone), but it
	// is enforced by the ADAPTER — and the adapter is swappable while the
	// pipeline is not, so the requirement is held here and re-checked on every
	// swap. Without that, replacing the adapter silently downgrades a guarded
	// lens to last-writer-wins and reopens the revoke→resurrect window.
	requireGuardedAdapter bool

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
	// rebuildSerial (capacity 1) serializes RebuildAndWait callers per pipeline.
	// A channel rather than a sync.Mutex because acquisition must honour the
	// caller's context: a rebuild drain is unbounded in principle, and a shutting
	// -down consumer must not be pinned waiting for one.
	rebuildSerial chan struct{}
	// rebuildWatch is the completion signal of the rebuild currently in flight:
	// a channel one rebuild creates and its OWN watcher closes, non-nil only
	// while that rebuild is un-drained. A caller waits on the channel it was
	// handed, never on shared state, which is what makes the wait
	// un-clobberable — rebuildInFlight cannot serve, because abandonRebuild
	// clears it unconditionally and the two callers that bypass rebuildSerial
	// (the MATCH hot-reloader and the operator rebuild op) reach abandonRebuild
	// on any failure of their own. A waiter polling that flag would then return
	// over a rescan still running and, for the retention-class consumer,
	// attest to an erasure that had not reached the read models.
	rebuildWatchMu sync.Mutex
	rebuildWatch   *rebuildSignal
	// rebuildOutstanding is the most recent un-drained count watchRebuildCompletion
	// polled, and rebuildProgressAt when that count last went DOWN. Together they
	// separate a rebuild that is draining from one that is wedged — a distinction
	// elapsed time alone cannot make, which is why the sweep-stall detector
	// exempts a rebuild from escalation entirely. A poll that ERRORS deliberately
	// advances neither: OutstandingForConsumer is retried indefinitely, so an
	// error that never clears is exactly the wedge this is here to expose.
	rebuildMu          sync.Mutex
	rebuildOutstanding uint64
	rebuildProgressAt  time.Time

	// sweeper is the auth-plane convergence sweep, installed via SetSweepPlan
	// for an auth-plane actor-aggregate lens only and driven by RunSweep. Nil
	// for every other lens, which is what excludes the personal, plain,
	// convergence, and operation-aggregate lenses structurally.
	sweeper *Sweeper

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

// seedAppliedSeqFromAckFloor seeds lastAppliedSeq from the durable consumer's
// persisted ack floor right after the supervisor registers it (Run). The
// token is per-process state that otherwise starts at zero on every restart
// (capability-projection-reconciliation-design.md §3.4): on a quiet stream —
// nothing to ack since the previous process exited — the pipeline would hold
// no usable ordering token until new traffic arrives, so a reconciliation
// write over an existing row keeps hitting ErrNoOrderingToken indefinitely.
// The durable's ack floor already reflects every message this rule has ever
// applied, restart or not, so seeding from it makes the token usable
// immediately. A read failure (e.g. a transient info-fetch error) is logged
// and otherwise ignored — the pipeline falls back to the pre-existing
// cold-start-at-zero behavior, which is safe, just inert until traffic
// arrives. The write only raises lastAppliedSeq, never lowers it, so it
// cannot regress a value the pump has already advanced to concurrently.
func (p *Pipeline) seedAppliedSeqFromAckFloor(ctx context.Context) {
	floor, err := p.supervisor.AckFloorForConsumer(ctx, p.consumerCfg.Name)
	if err != nil {
		slog.Warn("pipeline: could not read ack floor to seed lastAppliedSeq, starting cold",
			"ruleId", p.ruleID, "err", err)
		return
	}
	p.progressMu.Lock()
	if floor > p.lastAppliedSeq {
		p.lastAppliedSeq = floor
	}
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

// Envelope is one (key, body) pair a MultiEnvelopeFn emits for a single real
// entry of an actor's split list column.
type Envelope struct {
	Keys map[string]any
	Row  map[string]any
}

// MultiEnvelopeFn is EnvelopeFn's per-entry sibling (§4.1 of
// cap-read-per-anchor-grant-keys-design.md): instead of rewriting one
// projection row into exactly one on-wire document, it rewrites the row into
// the actor's fresh CHILD-key set — zero or more Envelopes, one per real
// entry of the descriptor's single split list column. Returning
// ErrSkipProjection declines the whole row (mirrors EnvelopeFn's skip
// contract); any other error fails the actor's projection closed rather than
// writing a partial or malformed key. A nil MultiEnvelopeFn leaves the
// existing one-document EnvelopeFn path untouched.
type MultiEnvelopeFn func(row map[string]any, keys map[string]any, params map[string]any) ([]Envelope, error)

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
		rebuildSerial:       make(chan struct{}, 1),
	}
	p.adpt = adpt
	return p, nil
}

// UseFullEngine switches this pipeline's evaluate path to the full
// openCypher engine. cr must be the *full.CompiledRule that lens.Parse /
// corekv_source produced for this rule. Must be called before Run.
func (p *Pipeline) UseFullEngine(eng *full.Engine, cr ruleengine.CompiledRule) {
	p.useFullEngineBranches(eng, cr, nil)
}

// UseFullEngineBranches is UseFullEngine's multi-walk sibling
// (refractor-shared-keyspace-arbitration-design.md §13.2): branches carries
// a Personal lens's N independently-compiled query branches (lens.Rule.
// CompiledBranches), cr must be branches[0]. Nil/single-element branches
// behaves exactly like UseFullEngine. Must be called before Run.
func (p *Pipeline) UseFullEngineBranches(eng *full.Engine, cr ruleengine.CompiledRule, branches []ruleengine.CompiledRule) {
	p.useFullEngineBranches(eng, cr, branches)
}

func (p *Pipeline) useFullEngineBranches(eng *full.Engine, cr ruleengine.CompiledRule, branches []ruleengine.CompiledRule) {
	// Everything is derived into locals first and published under one Lock at
	// the end (see ruleMu): a reader must never observe a half-rewritten rule.
	next := ruleState{
		engineKind: ruleengine.EngineFull,
		engine:     eng,
		cr:         cr,
	}
	// Unconditional, not just the len>1 arm: a reload (cmd/refractor/reload.go)
	// calls this on an EXISTING pipeline, so a lens edited from 2+ Walks down
	// to a single Walk must clear both fields — leaving them set would keep
	// evaluating a Walk the new spec no longer has.
	if len(branches) > 1 {
		next.branches = branches
		next.walkOwnedColumns = walkOwnedColumns(branches)
	}
	// Pin the vertex types this lens's patterns can bind, so the plain
	// aspect/link reprojection arms skip events on types the lens cannot
	// read (an unbounded label set — unlabeled node pattern or var-length
	// relationship — disables the skip; every event reprojects). Union
	// across every branch for a multi-walk lens: each branch's own clauses
	// bind only that walk's types, but the plain-reprojection arms reason
	// about the lens as a whole.
	next.reprojectAll = true
	all := []ruleengine.CompiledRule{cr}
	if len(branches) > 1 {
		all = branches
	}
	labels := map[string]struct{}{}
	relations := map[string]struct{}{}
	exhaustive := true
	relationsExhaustive := true
	for _, c := range all {
		fullCR, isFull := c.(*full.CompiledRule)
		if !isFull {
			exhaustive = false
			relationsExhaustive = false
			break
		}
		ls, ok := fullCR.ReferencedLabels()
		if !ok {
			exhaustive = false
		} else {
			for l := range ls {
				labels[l] = struct{}{}
			}
		}
		// The relation set is derived independently of the label set: a lens
		// can name every label exhaustively while traversing an untyped
		// relationship, or the reverse. Collecting both to the end (rather
		// than breaking on the first non-exhaustive answer) is what lets the
		// two narrowings degrade separately.
		rs, rok := fullCR.ReferencedRelations()
		if !rok {
			relationsExhaustive = false
		} else {
			for r := range rs {
				relations[r] = struct{}{}
			}
		}
	}
	if exhaustive {
		next.reprojectLabels = labels
		next.reprojectAll = false
	}
	if relationsExhaustive {
		next.reprojectRelations = relations
		next.relationsExhaustive = true
	}
	// Pin the anchor label an anchor-labeled event can seed the evaluation
	// with. Derived unconditionally like the label set above, and for the same
	// reason: a reload must never leave a previous rule body's anchor armed. A
	// multi-walk lens is excluded outright — branch merging evaluates N
	// independent queries, each with its own anchor, and one seed cannot speak
	// for all of them.
	if len(branches) <= 1 {
		if fullCR, isFull := cr.(*full.CompiledRule); isFull {
			if label, ok := fullCR.AnchorLabel(); ok {
				next.seedAnchorLabel = label
			}
			// The pattern graph the affected-anchor derivation walks under
			// (auth-plane-projection-latency-design.md §4.7). Derived here for
			// the same two reasons the anchor label is, and excluded on the
			// same multi-walk arm: each branch carries its own anchor, and one
			// graph cannot speak for all of them.
			next.anchorHops = fullCR.AnchorHopIndex()
		}
	}
	p.publishRuleState(next)
}

// ruleState is a coherent snapshot of everything useFullEngineBranches
// rewrites — the fields ruleMu guards. Taken once per consumer entry and
// threaded down, so a MATCH hot-reload landing mid-event cannot show one gate
// the new rule and the next gate the old one.
//
// Its maps and slices alias the Pipeline's own, which is safe precisely
// because that publication is copy-on-write (see ruleMu). Nothing here may be
// mutated.
type ruleState struct {
	gen                 uint64
	engineKind          string
	engine              *full.Engine
	cr                  ruleengine.CompiledRule
	branches            []ruleengine.CompiledRule
	walkOwnedColumns    map[string]int
	reprojectLabels     map[string]struct{}
	reprojectAll        bool
	reprojectRelations  map[string]struct{}
	relationsExhaustive bool
	seedAnchorLabel     string
	anchorHops          full.HopIndex
}

// ruleState returns the pipeline's current compiled rule as one snapshot.
//
// Callers must take it ONCE at the top of an operation and pass it down rather
// than re-reading per gate. The reason is COHERENCE, not lock safety: this
// function holds ruleMu only for the struct copy and releases before it
// returns, so a second call is always sequential and cannot deadlock. What a
// second call costs is the guarantee — two snapshots in one operation can
// straddle a hot-reload, which is exactly the incoherence ruleMu exists to
// remove.
//
// The returned maps are the live published ones, safe to read outside the lock
// only because publication is copy-on-write. No caller may mutate them — that
// includes what ActorAwareNarrowingLabels and NarrowedFilterEligible hand back.
func (p *Pipeline) ruleState() ruleState {
	p.ruleMu.RLock()
	defer p.ruleMu.RUnlock()
	return ruleState{
		gen:                 p.ruleGen,
		engineKind:          p.engineKind,
		engine:              p.fullEngine,
		cr:                  p.fullCR,
		branches:            p.fullCRBranches,
		walkOwnedColumns:    p.fullCRWalkOwnedColumns,
		reprojectLabels:     p.plainReprojectLabels,
		reprojectAll:        p.plainReprojectAll,
		reprojectRelations:  p.plainReprojectRelations,
		relationsExhaustive: p.plainRelationsExhaustive,
		seedAnchorLabel:     p.seedAnchorLabel,
		anchorHops:          p.anchorHops,
	}
}

// publishRuleState installs a freshly-derived rule as one atomic swap.
func (p *Pipeline) publishRuleState(rs ruleState) {
	p.ruleMu.Lock()
	defer p.ruleMu.Unlock()
	p.ruleGen++
	p.engineKind = rs.engineKind
	p.fullEngine = rs.engine
	p.fullCR = rs.cr
	p.fullCRBranches = rs.branches
	p.fullCRWalkOwnedColumns = rs.walkOwnedColumns
	p.plainReprojectLabels = rs.reprojectLabels
	p.plainReprojectAll = rs.reprojectAll
	p.plainReprojectRelations = rs.reprojectRelations
	p.plainRelationsExhaustive = rs.relationsExhaustive
	p.seedAnchorLabel = rs.seedAnchorLabel
	p.anchorHops = rs.anchorHops
}

// seedAnchorFor returns the vertex key an event on (eventLabel, eventKey) may
// seed this lens's evaluation with — narrowing it to that one anchor — or ""
// when the evaluation must recompute the lens's whole row set as it always
// has. It is the pipeline half of refractor-footprint-reduction-design.md
// §D2's eligibility; the engine independently re-derives that the key's own
// type matches the compiled anchor pattern's label before narrowing anything.
//
// Every conjunct is a correctness requirement, not a heuristic:
//
//   - eventLabel == seedAnchorLabel — only a mutation of the anchor ITSELF
//     bounds the change to one anchor. A neighbor (referenced non-anchor type)
//     event can affect any number of anchors through the walk, and deriving
//     which ones is §D2 Phase 2; it keeps the full recompute.
//   - no ActorEnumerator and no envelope — an actor-aware/personal evaluation
//     is already scoped to one actor, and its "anchor" is that actor, not the
//     event vertex; seeding it with an event key would evaluate the wrong
//     entity.
//   - DiffRetraction off — that retraction diffs the target's FULL live key
//     set against the evaluation's row set, so a single-anchor row set would
//     read as "every other anchor's rows are gone" and retract them all. This
//     conjunct is what makes applyDiffRetraction unreachable from a seeded
//     evaluation.
func (p *Pipeline) seedAnchorFor(rs ruleState, eventLabel, eventKey string) string {
	if rs.seedAnchorLabel == "" || eventKey == "" || eventLabel != rs.seedAnchorLabel {
		return ""
	}
	if p.actorEnumerator != nil || p.envelopeFn != nil || p.multiEnvelopeFn != nil {
		return ""
	}
	if p.diffRetraction {
		return ""
	}
	return eventKey
}

// plainReactsTo reports whether the plain aspect/link reprojection arms should
// re-execute this lens for an event whose owner/endpoint vertex has the given
// type. A lens with an exhaustive label set reprojects only for types its
// patterns can bind.
func (rs ruleState) plainReactsTo(vertexType string) bool {
	if rs.engineKind != ruleengine.EngineFull {
		return false
	}
	if rs.reprojectAll {
		return true
	}
	_, ok := rs.reprojectLabels[vertexType]
	return ok
}

// plainLinkReactsTo reports whether the plain link reprojection arm should
// re-execute this lens for a link event on the given relation. It is
// plainReactsTo's companion on the other half of the link key: that one asks
// whether either ENDPOINT TYPE can bind, this one whether the RELATION between
// them is one the lens's patterns actually traverse. A link satisfying only the
// endpoint test — `lnk.service.<id>.providedTo.identity.<id>` reaching a lens
// whose sole relationship pattern is `(pr)-[:identifiedBy]->(id:identity)` —
// cannot appear in any traversal, and re-executing for it is pure cost.
//
// The false case lands in the SAME already-sanctioned skip class as
// evalPlainLinkReprojection's "neither endpoint type is bindable": no
// reprojection, and no adjacency self-apply, whose authoritative writer is the
// dedicated whole-stream adjacency consumer rather than any lens pipeline.
//
// Every uncertain case defaults to relevant, exactly as plainReactsTo does: a
// non-full engine, an empty/unparsed relation, or a non-exhaustive relation set
// (an untyped or variable-length relationship anywhere in the lens) all
// reproject.
func (rs ruleState) plainLinkReactsTo(relation string) bool {
	if rs.engineKind != ruleengine.EngineFull {
		return true
	}
	if !rs.relationsExhaustive || relation == "" {
		return true
	}
	_, ok := rs.reprojectRelations[relation]
	return ok
}

// plainVertexRelevant reports whether a plain (non-actor-aware) lens's
// KindVertex handling should evaluate a vertex-root event of the given type,
// or skip-and-Ack it as irrelevant. It shares plainReactsTo's label data but
// NOT its default: plainReactsTo's false case only tells the aspect/link
// arms not to run their OWN special-cased reprojection, which is always safe
// because the caller has no other write path to lose — whereas this gate's
// false case drops the vertex-root CDC event outright, with no fallback. A
// wrong "irrelevant" here would blind the lens to real writes, so every
// uncertain case must default to relevant: a non-full engine (plainReactsTo
// itself only exists for the full engine's label data, so an engine that
// isn't full has none to trust), an empty/unrecognized vertex type, or a
// non-exhaustive referenced-label set (plainReprojectAll) all fall through
// to evaluation — this gate only ever narrows a full-engine lens's exhaustive
// label set, it never re-scopes what any other lens evaluates. Only a
// full-engine lens with an exhaustive label set that provably excludes
// vertexType is skipped.
func (rs ruleState) plainVertexRelevant(vertexType string) bool {
	if rs.engineKind != ruleengine.EngineFull {
		return true
	}
	if rs.reprojectAll || vertexType == "" {
		return true
	}
	_, ok := rs.reprojectLabels[vertexType]
	return ok
}

// ActorAwareNarrowingLabels reports whether this actor-aware pipeline's fan-out
// arms may skip an event whose vertex types its compiled patterns provably
// cannot bind, and if so the exhaustive label set to judge against. It is the
// conjunction in auth-plane-projection-latency-design.md §4.2, and every
// conjunct is fail-closed: any one of them failing yields the pipeline's
// existing unconditional fan-out.
//
// It is evaluated per event rather than snapshotted at installation, mirroring
// seedAnchorFor. Activation installs these components in stages —
// UseFullEngineBranches, then projection.InstallActorAggregate, then
// SetSecureDecryptor (cmd/refractor/main.go) — so a snapshot taken during
// installation would read a later stage's component as absent. For the
// decryptor conjunct specifically that would narrow every Secure Lens, which is
// the one case the conjunct exists to refuse.
func (p *Pipeline) ActorAwareNarrowingLabels() (map[string]struct{}, bool) {
	return p.actorAwareNarrowingLabels(p.ruleState())
}

// actorAwareNarrowingLabels is ActorAwareNarrowingLabels against a snapshot the
// caller already holds — the form every in-pipeline caller uses, since taking a
// second snapshot mid-event would reintroduce exactly the incoherence ruleMu
// exists to remove.
func (p *Pipeline) actorAwareNarrowingLabels(rs ruleState) (map[string]struct{}, bool) {
	// Not actor-aware: the plain arms own their own gates.
	if p.actorEnumerator == nil {
		return nil, false
	}
	// ReferencedLabels only exists for the full engine, and only an exhaustive
	// set proves a type cannot bind.
	if rs.engineKind != ruleengine.EngineFull || rs.reprojectAll {
		return nil, false
	}
	// An input outside the compiled pattern breaks the closure claim.
	if !p.patternClosedOutput {
		return nil, false
	}
	// Narrowing removes an incidental reprojection that today happens to heal a
	// lost row. The convergence sweep is the standing healer that replaces it,
	// and sweepEnrolment may refuse with only a warning (projection/driver.go),
	// so a lens without a plan must not also lose the accident.
	//
	// This proves the plan is INSTALLED, not that a healer is turning: the sweep
	// runs only where the host also calls RunSweep (cmd/refractor does). A host
	// that installs lenses without starting sweeps gets narrowing with no
	// standing healer, and Sweeper.suppressed additionally idles the tick while
	// the lens is non-active or rebuilding.
	if p.sweeper == nil {
		return nil, false
	}
	// The anchor's own soft-delete arrives as a vertex event of the anchor's
	// type. A lens that cannot see that type would never retract the anchor's
	// row, and on the auth plane a missed retraction is an over-grant.
	if _, ok := rs.reprojectLabels[p.actorEnumerator.actorType]; !ok {
		return nil, false
	}
	// A key holder is not implied by the pattern: the decryptor resolves custody
	// from the ciphertext's own keyId, and a holder vertex may not be one the
	// cypher binds at all. The in-band scrub is a CDC event on <holder>.piiKey,
	// so a narrowed lens that cannot see a declared holder type would never be
	// delivered that holder's destruction and would keep projecting decrypted
	// plaintext. Judge against what the lens DECLARED — the declaration is the
	// only place a holder type is knowable without parsing compiled cypher.
	//
	// This conjunct guards a combination that cannot exist yet, and claims
	// nothing about the lenses that ship today: reaching here at all requires an
	// actorEnumerator (narrowedFilterEligible), and a secure lens is refused on
	// any non-empty projectionKind at translate time, so every shipped secure
	// lens takes the PLAIN branch, which carries no holder-type conjunct. What
	// contains the exposure meanwhile is pkgmgr's custody-scope gate, which
	// refuses a non-identity holder at install. Whoever lifts either ban owns
	// carrying this requirement onto the arm they open.
	if p.secureDecryptor != nil {
		for _, holderType := range p.secureDecryptor.HolderTypes() {
			if _, ok := rs.reprojectLabels[holderType]; !ok {
				return nil, false
			}
		}
	}
	return rs.reprojectLabels, true
}

// actorAwareFanOutRelevant reports whether the actor-aware fan-out arms must run
// for an event touching the given vertex types — the counterpart of
// plainReactsTo/plainVertexRelevant for the arms D1 and Fire 3 excluded. An
// event is relevant when ANY of the types it touches is in the label set, so a
// link is skipped only when NEITHER endpoint can bind.
//
// Every uncertain case defaults to relevant: an ineligible pipeline, an empty
// or unparsed type, or no types at all.
func (p *Pipeline) actorAwareFanOutRelevant(rs ruleState, types ...string) bool {
	if len(types) == 0 {
		return true
	}
	labels, ok := p.actorAwareNarrowingLabels(rs)
	if !ok {
		return true
	}
	for _, t := range types {
		if t == "" {
			return true
		}
		if _, in := labels[t]; in {
			return true
		}
	}
	return false
}

// vertexEventRelevant is the single gate the KindVertex arm consults for both
// pipeline shapes: a plain lens judges by plainVertexRelevant, an actor-aware
// one by §4.2's conjunction.
func (p *Pipeline) vertexEventRelevant(rs ruleState, vertexType string) bool {
	if p.actorEnumerator != nil {
		return p.actorAwareFanOutRelevant(rs, vertexType)
	}
	return rs.plainVertexRelevant(vertexType)
}

// SetPatternClosedOutput declares that this lens's row for an anchor is a
// function solely of the subgraph its compiled pattern binds from that anchor.
// Called by projection.InstallActorAggregate; see the field's doc for why every
// other installation path leaves it false.
func (p *Pipeline) SetPatternClosedOutput(v bool) {
	p.patternClosedOutput = v
}

// PatternClosedOutput reports what SetPatternClosedOutput declared, so a test
// can pin which installation paths assert pattern-closure without reaching into
// the pipeline's internals.
func (p *Pipeline) PatternClosedOutput() bool { return p.patternClosedOutput }

// NarrowedFilterEligible reports whether this pipeline's Core KV consumer may
// be scoped to a narrowed, server-side FilterSubjects set instead of the
// broad $KV.<bucket>.> filter, and if so the exhaustive set of vertex-type
// labels to derive it from — the SAME set useFullEngineBranches already
// computed from every compiled branch's ReferencedLabels(), not a second
// derivation.
//
// The invariant is one sentence: a narrowed consumer is never denied an event
// this pipeline's own CLIENT-side gate would have kept. Server-side filtering is
// therefore strictly more conservative than that gate, derived from the exact
// data the gate already trusts — never a second, independently-fallible
// judgment. Each pipeline shape brings its own client gate, so each brings its
// own eligibility:
//
//   - PLAIN — engineKind == EngineFull (plainReactsTo/plainVertexRelevant have
//     no label data for any other engine) and an EXHAUSTIVE referenced-label set
//     (!reprojectAll): the two conditions those gates already require.
//   - ACTOR-AWARE — the §4.2 conjunction, actorAwareNarrowingLabels. Whether a
//     fan-out arm may skip an event and whether the server may withhold it are
//     the same question (auth-plane-projection-latency-design.md §4.6), so there
//     is one answer to it.
//
// An actor-aware pipeline's FAN-OUT breadth is not bounded by its own MATCH
// labels — the enumerator walks adjacency, so one relevant event can reach
// actors no label names. That is a different question from RELEVANCE: once §4.2
// holds, whether any actor can be affected at all IS bounded by the pattern's
// label set, and only that second question decides delivery. The enumerator
// itself is unaffected by a narrower filter because adjacency is written by its
// own dedicated whole-stream consumer (refractor/consumer/bootstrap.go), not by
// this pipeline's deliveries.
//
// Label-set-to-subject alignment, which is what makes "the exact data" exact
// rather than nearly so: CoreKVNarrowedFilters emits a vertex form per label,
// and a Contract #1 aspect key is the 4-segment vtx.<type>.<id>.<localName>, so
// a label's vertex form already covers its aspects — which is what the aspect
// arm judges by parent type. It emits a source-pinned AND a target-pinned link
// form, so a link is admitted when EITHER endpoint type is in the set — which is
// what the link arm judges, skipping only when NEITHER is.
func (p *Pipeline) NarrowedFilterEligible() (labels map[string]struct{}, ok bool) {
	return p.narrowedFilterEligible(p.ruleState())
}

// narrowedFilterEligible is NarrowedFilterEligible against a snapshot the
// caller already holds — see actorAwareNarrowingLabels for why the in-pipeline
// callers must not take their own.
func (p *Pipeline) narrowedFilterEligible(rs ruleState) (labels map[string]struct{}, ok bool) {
	if p.actorEnumerator != nil {
		return p.actorAwareNarrowingLabels(rs)
	}
	if rs.engineKind != ruleengine.EngineFull || rs.reprojectAll {
		return nil, false
	}
	return rs.reprojectLabels, true
}

// ConsumerFilter derives this lens's Core KV consumer filter from its CURRENT
// compiled rule: a narrowed, server-side FilterSubjects set when the lens
// qualifies (NarrowedFilterEligible, and the deduped label count at or under
// maxNarrowedFilterLabels), or the broad $KV.<bucket>.> FilterSubject
// otherwise. Exactly one of the two return values is non-empty.
//
// Pure over the pipeline's current state, not cached, so every caller
// recomputes the identical value from the identical inputs with nothing to
// keep in sync: the initial activation (RunOn's caller builds the spec from
// this) and a later Rebuild both call it. Rebuild MUST call it again rather
// than reuse whatever filter activation chose — a MATCH hot-reload's
// UseFullEngineBranches call can widen or narrow the referenced-label set
// before the next rebuild, and a stale filter left in place would silently
// under-deliver forever (a JetStream filter update never resets the
// consumer's cursor — nats-server v2.14.0).
//
// This is also the one place a per-event predicate gets SNAPSHOTTED, because a
// consumer's filter is fixed at registration. actorAwareNarrowingLabels is
// deliberately lazy — the host installs its inputs in stages, so a snapshot
// taken mid-installation reads a later stage's component as absent — and the
// snapshot here is sound only when every one of those stages has already run.
// The order that satisfies it, verified live: UseFullEngineBranches →
// InstallActorAggregate (enumerator, pattern-closure, sweep plan) →
// SetSecureDecryptor → ConsumerFilter → RunOn (cmd/refractor/main.go).
//
// GETTING THAT ORDER WRONG COSTS CORRECTNESS, NOT MERELY NARROWING, and the
// failure is counter-intuitive enough to state outright: a missing stage does
// NOT fall back to the broad filter. With the enumerator not yet installed this
// pipeline is indistinguishable from a plain one, so narrowedFilterEligible
// takes the PLAIN branch — whose two conditions (full engine, exhaustive labels)
// UseFullEngineBranches has already satisfied. The result is a narrowed filter
// granted with NONE of §4.2's conjuncts evaluated: no pattern-closure, no sweep
// plan, no anchor-type-in-labels, no decryptor check — and then the relation
// dimension applies too, because that gate also reads the enumerator. Calling
// this early therefore yields the MOST aggressive filter, not the broadest. A
// missed anchor soft-delete is an over-grant, and per the paragraph below no
// revert recovers it. Any new install stage belongs ABOVE this call.
//
// The secure-decryptor conjunct is a special case of the same rule: installing a
// decryptor after this call would narrow a secure lens whose decryptor conjunct
// was never evaluated. Secure columns are refused on any non-empty
// projectionKind at spec load, so secure ∧ actorAggregate cannot exist today;
// whoever lifts that ban owns this ordering.
//
// RECOVERING FROM A WRONG NARROW IS NOT A CODE REVERT, and this is the site that
// has to say so. A JetStream filter update never rewinds the consumer's cursor,
// so widening the filter back — by any means, reverting the code that narrowed
// it included — leaves every event the narrow filter already excluded
// permanently undelivered. The recovery is Pipeline.Rebuild (consumer reset plus
// re-projection from the DeliverLastPerSubject snapshot) or the convergence
// sweep, which is why a sweep plan is one of §4.2's conjuncts rather than a
// nice-to-have: a narrowed lens must always have a standing healer.
func (p *Pipeline) ConsumerFilter() (filterSubjects []string, filterSubject string) {
	// One snapshot for both dimensions: the label set and the relation set must
	// come from the SAME compiled rule, or a hot-reload landing between the two
	// reads would build a filter no rule ever asked for.
	rs := p.ruleState()
	// One read of the actor dimension for the whole derivation, for the same
	// reason rs is one snapshot: this function consults it twice (eligibility,
	// then the relation gate below), and two reads could straddle a change and
	// build a filter no single pipeline shape ever asked for. ruleState cannot
	// carry this — actor-awareness is installed, not compiled — so it is hoisted
	// here instead.
	actorAware := p.actorEnumerator != nil
	labels, ok := p.narrowedFilterEligible(rs)
	if !ok || len(labels) == 0 || len(labels) > maxNarrowedFilterLabels {
		return nil, subjects.CoreKVFilter(p.coreKVBucket)
	}
	labelList := make([]string, 0, len(labels))
	for l := range labels {
		labelList = append(labelList, l)
	}
	// Relation-narrowed when the relation set is exhaustive too and the
	// resulting subject count fits the budget; otherwise the relation-blind
	// narrowed set. Degrading here rather than at NarrowedFilterEligible keeps
	// the label narrowing's own eligibility untouched — the two dimensions fail
	// back independently, and a lens that narrows by label today never stops
	// because its relations do not qualify.
	//
	// The relation dimension is PLAIN-ONLY, and that is a correctness gate, not
	// a budget one. It is sound for a plain lens because plainLinkReactsTo is its
	// client-side counterpart: the arm already skips a link whose relation the
	// patterns never traverse, so the server withholding that link is strictly
	// more conservative than a gate that ran regardless. The actor-aware link arm
	// has no relation gate — actorAwareFanOutRelevant judges by ENDPOINT TYPE
	// alone, skipping only when NEITHER endpoint can bind — so narrowing by
	// relation there would withhold events its client gate keeps: a second,
	// independently-fallible judgment, which is the one thing
	// NarrowedFilterEligible's invariant forbids. This is reachable rather than
	// theoretical (capabilityRoles derives an exhaustive relation set), and the
	// event it would lose is a real one: a link joining an in-label endpoint to an
	// out-of-label one over a relation the pattern never walks. Extending the
	// dimension means giving the fan-out arm a relation gate first.
	if !actorAware && rs.relationsExhaustive {
		relationList := make([]string, 0, len(rs.reprojectRelations))
		for r := range rs.reprojectRelations {
			relationList = append(relationList, r)
		}
		if len(labelList)*(1+2*len(relationList)) <= maxNarrowedFilterSubjects {
			return subjects.CoreKVRelationNarrowedFilters(p.coreKVBucket, labelList, relationList), ""
		}
	}
	return subjects.CoreKVNarrowedFilters(p.coreKVBucket, labelList), ""
}

// SetEnvelopeFn installs the on-wire envelope wrapper. Pass nil to clear.
// Clears any installed MultiEnvelopeFn — the two are alternatives, never both
// active on the same pipeline. Must be called before Run.
func (p *Pipeline) SetEnvelopeFn(fn EnvelopeFn) {
	p.envelopeFn = fn
	if fn != nil {
		p.multiEnvelopeFn = nil
	}
}

// SetMultiEnvelopeFn installs the per-entry envelope wrapper (§4.1 of
// cap-read-per-anchor-grant-keys-design.md). Pass nil to clear. Clears any
// installed EnvelopeFn — the two are alternatives, never both active on the
// same pipeline. Must be called before Run.
func (p *Pipeline) SetMultiEnvelopeFn(fn MultiEnvelopeFn) {
	p.multiEnvelopeFn = fn
	if fn != nil {
		p.envelopeFn = nil
	}
}

// IsPerEntry reports whether this pipeline projects through the per-entry
// envelope (a MultiEnvelopeFn installed via SetMultiEnvelopeFn) rather than
// the one-document-per-actor EnvelopeFn — the same distinction
// DeleteAllForActor's refusal reads, exposed for callers (installation-time
// wiring checks, tests) that need to observe the shape without exercising the
// live shred path.
func (p *Pipeline) IsPerEntry() bool {
	return p.multiEnvelopeFn != nil
}

// SetDiffRetraction opts this plain lens into Fire 3's target-diff retraction
// (see the diffRetraction field doc). Must be called before Run.
//
// Enabling it against an adapter that cannot enumerate its keys is refused: the
// mechanism is defined by the diff between the target's live key set and a
// fresh re-projection, so without adapter.KeyLister there is no live key set
// and the lens retracts nothing, forever, without erroring. That silence is the
// danger — a grant producer's retraction IS its revocation path, so an inert
// one presents as a working security control while access outlives the
// relationship that justified it. Refusing here lets the caller fail the
// activation, leaving the lens dark rather than half-armed.
func (p *Pipeline) SetDiffRetraction(enabled bool) error {
	if enabled {
		if _, ok := p.currentAdapter().(adapter.KeyLister); !ok {
			return fmt.Errorf("pipeline: diff retraction requires an adapter implementing adapter.KeyLister; %T does not — the lens could never retract a row", p.currentAdapter())
		}
	}
	p.diffRetraction = enabled
	return nil
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

// SetZeroRowRetraction arms the doc-mode zero-row retraction transport (see
// the zeroRowRetraction field doc): a filtering WHERE on the anchor match
// itself — not merely an OPTIONAL MATCH secondary pattern — makes the cypher
// return no row at all once the anchor stops matching, which starves the
// per-row envelope callback (EmptyBehavior's other transport,
// projection/driver.go's EnvelopeFn) of anything to decline. Must be called
// before Run.
func (p *Pipeline) SetZeroRowRetraction(v bool) {
	p.zeroRowRetraction = v
}

// ZeroRowRetractionArmed reports whether the doc-mode zero-row retraction
// transport is armed, exposed for callers (installation-time wiring checks,
// tests) that need to observe the setting without exercising the live
// evaluation path — the same reasoning IsPerEntry documents for its own flag.
func (p *Pipeline) ZeroRowRetractionArmed() bool {
	return p.zeroRowRetraction
}

// SetSecureDecryptor installs the Secure-Lens decrypt-at-projection transform
// (Contract #3 §3.10). Pass nil to clear. Must be called before Run.
func (p *Pipeline) SetSecureDecryptor(d *SecureDecryptor) {
	p.secureDecryptor = d
}

// SetAuthPlane records whether this lens projects an authorization surface
// (projection.IsAuthPlane). Combined with actorAggregate and
// requiresFootprintValidation, it gates footprint validation (see the
// authPlane field doc). Must be called before Run.
func (p *Pipeline) SetAuthPlane(v bool) {
	p.authPlane = v
}

// SetRequiresFootprintValidation records whether this lens's compiled cypher
// emits a multi-binding conjunct unit (projection.ProjectionPlan's derived
// field, see the requiresFootprintValidation field doc). Combined with
// authPlane and actorAggregate, it gates footprint validation. Must be
// called before Run.
func (p *Pipeline) SetRequiresFootprintValidation(v bool) {
	p.requiresFootprintValidation = v
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

// SetAuditWriter attaches an AuditWriter that appends this rule's entries to
// the shared JetStream audit stream (health.AuditStreamName) on every
// successful write.
// Must be called before Run. health.EnsureAuditStream must have been called
// once at process startup before Run.
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

// AckStats returns the supervised consumer's un-acked count and ack floor. It
// is what distinguishes a lens that is caught up from one that has been handed
// everything and cannot finish it — both report Pending 0. Errors on the same
// two windows Pending does, and the lag poller treats either as "skip this
// cycle".
func (p *Pipeline) AckStats(ctx context.Context) (substrate.AckStats, error) {
	if p.supervisor == nil {
		return substrate.AckStats{}, fmt.Errorf("pipeline: ack stats: no supervisor configured (RunOn not called)")
	}
	return p.supervisor.AckStatsForConsumer(ctx, p.consumerCfg.Name)
}

// RebuildInFlight reports whether a rebuild rescan is still draining — true
// from the start of Rebuild until the completion watcher observes the consumer
// fully drained (or the rebuild aborts). While true the lens's health entry is
// "rebuilding": a supervisor active-persist during the window re-persists
// "rebuilding" rather than a premature "active".
func (p *Pipeline) RebuildInFlight() bool {
	return p.rebuildInFlight.Load()
}

// RequireGuardedAdapter declares that every adapter this pipeline writes
// through must enforce the §6.2 monotonic projection-write guard. Called by the
// installer once it has enabled the guard on the activation adapter; from then
// on HotReloadInto refuses a replacement that cannot enforce it.
func (p *Pipeline) RequireGuardedAdapter() {
	p.adapterMu.Lock()
	p.requireGuardedAdapter = true
	p.adapterMu.Unlock()
}

// HotReloadInto atomically replaces the adapter. Any message already in processMsg
// continues with the adapter it captured at the start of that call; the next message
// will use newAdpt. Returns an error if newAdpt is nil.
// Used by the orchestrator for INTO-only rule updates (FR4).
//
// A pipeline that requires the projection-write guard refuses an unguarded
// replacement rather than swapping it in: the guard is what stops a stale
// re-projection resurrecting a revoked capability, and an adapter is only
// guarded because something flipped the flag on that instance — a freshly built
// one starts open. Refusing leaves the running (guarded) adapter in place,
// which keeps the lens correct and stale rather than live and unguarded.
//
// The requirement does not lapse when a rule edit would drop it: a lens that
// stops projecting an authorization surface has changed identity, not its INTO
// config, and a hot swap has no way to retract what the guarded lens already
// wrote. That case is a delete-and-re-create, the same remedy an unsupported
// secureColumns change takes.
func (p *Pipeline) HotReloadInto(newAdpt adapter.Adapter) error {
	if newAdpt == nil {
		return errors.New("pipeline: HotReloadInto: newAdpt must not be nil")
	}
	p.adapterMu.Lock()
	defer p.adapterMu.Unlock()
	guard, reports := newAdpt.(adapter.SeqGuarded)
	guarded := reports && guard.Guarded()
	if p.requireGuardedAdapter && !guarded {
		return fmt.Errorf("pipeline: HotReloadInto: lens %s requires the projection-write guard but the replacement adapter (%T) does not enforce it — delete and re-create the lens if it should no longer project an authorization surface", p.ruleID, newAdpt)
	}
	// A guarded adapter arms the requirement wherever it arrives from. A rule
	// edit can move a lens ONTO an authorization surface, which guards the
	// adapter built for it without the installer running again — so latching
	// here is what keeps a lens that becomes guarded mid-life from being
	// downgraded by the swap after it.
	if guarded {
		p.requireGuardedAdapter = true
	}
	p.adpt = newAdpt
	return nil
}

// registerWithFilterFallback runs register — the supervisor call that
// (re)creates this lens's Core KV durable (supervisor.Add for the initial
// Run, supervisor.Reset for a Rebuild) — and, if it fails while filterSubjects
// is non-empty (a narrowed consumer was attempted), falls back to the broad
// Core KV filter: logs loudly, records the fact on the lens's own health
// entry (RecordError — the same per-lens errorCount/lastError surface
// `lattice health summary` and Lamplighter's Health KV read already
// classify, docs/observability/health-kv-schema.md's per-lens
// reporter-status entry — reused rather than a parallel signal), applies the
// broad filter via applyBroad, and retries register once.
//
// A narrowed filter is strictly an optimization over the broad one
// (ConsumerFilter's doc); it must never be the reason a lens goes dark or
// loses its durable, whatever the underlying registration error actually
// was — a JetStream rejection this package's own derivation did not
// anticipate is exactly the case this exists for.
//
// A clean FIRST attempt (register succeeds with no fallback fired) instead
// clears any stale LastError an earlier process's fallback left on this same
// health entry (Reporter.ClearLastError) — the fallback path below is the
// only writer of that latch, and nothing else ever revisits it, so left
// alone it survives every restart even once the lens is provably healthy
// again. The clear is scoped to LastError alone precisely so it cannot race
// the supervisor's startup restore of a persisted pause (this doc's own
// caller, Pipeline.Run: "restore persisted paused state on startup (NFR4)";
// substrate.ConsumerSupervisor's restoreState, run once from the pump
// goroutine Run's Add spawns, concurrently with Run itself — Rebuild's Reset
// call re-signals that same already-running pump instead of restarting it,
// so it never re-triggers restoreState) — see ClearLastError's own doc for
// why SetActive is the wrong tool here.
func (p *Pipeline) registerWithFilterFallback(ctx context.Context, filterSubjects []string, applyBroad func(), register func() error) error {
	err := register()
	if err == nil {
		if p.reporter != nil {
			if clrErr := p.reporter.ClearLastError(ctx); clrErr != nil {
				slog.Error("pipeline: clear stale health signal after clean registration", "ruleId", p.ruleID, "err", clrErr)
			}
		}
		return nil
	}
	if len(filterSubjects) == 0 {
		return err
	}
	slog.Error("pipeline: narrowed Core KV consumer registration failed — retrying with the broad filter",
		"ruleId", p.ruleID, "filterSubjects", filterSubjects, "err", err)
	if p.reporter != nil {
		msg := fmt.Sprintf("narrowed Core KV filter registration failed, fell back to the broad filter: %v", err)
		if recErr := p.reporter.RecordError(ctx, msg); recErr != nil {
			slog.Error("pipeline: record narrowed-filter fallback health signal", "ruleId", p.ruleID, "err", recErr)
		}
	}
	applyBroad()
	return register()
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

	spec := p.consumerCfg
	spec.Handler = p.handleTracked
	spec.Classify = classifyForSupervisor
	spec.Probe = func(pctx context.Context) error { return p.currentAdapter().Probe(pctx) }
	spec.Health = newHealthSink(p.reporter, p.rebuildInFlight.Load)
	// ProbeInterval is exported so tests can shrink it for fast recovery detection.
	if spec.ProbeInterval <= 0 {
		spec.ProbeInterval = ProbeInterval
	}

	narrowedFilters := spec.FilterSubjects
	err := p.registerWithFilterFallback(ctx, narrowedFilters, func() {
		spec.FilterSubjects = nil
		spec.FilterSubject = subjects.CoreKVFilter(p.coreKVBucket)
	}, func() error { return p.supervisor.Add(ctx, spec) })
	if err != nil {
		slog.Error("pipeline: supervisor add", "ruleId", p.ruleID, "err", err)
		return
	}
	p.seedAppliedSeqFromAckFloor(ctx)
	// Signal that the supervised consumer is registered so Pause/Resume issued
	// immediately after Run starts (in a goroutine) act on a live consumer.
	close(p.started)
	p.resumeInterruptedRebuild(ctx)

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
// abandonRebuild is every Rebuild error return's exit: it clears the in-flight
// flag, records the cause on the lens's health entry, and takes the status back
// out of "rebuilding". It returns the error unchanged so a caller reads exactly
// what went wrong.
//
// Leaving the status alone is what made this necessary. SetRebuilding is written
// before any of the work, but the only writer of the rebuilding → active
// transition is watchRebuildCompletion, which Rebuild launches on the SUCCESS
// path only. So a rebuild that failed left "rebuilding" latched with no watcher
// remaining to clear it, and Sweeper.suppressed refuses every tick whose status
// is not "active" — for the life of the process, since resumeInterruptedRebuild
// runs at startup only. RebuildInFlight() reads false by then, so the sweep's
// first suppression check does not catch it either.
//
// That is not a cosmetic status bug for a NARROWED lens. ActorAwareNarrowingLabels
// authorizes narrowing partly on the strength of a standing healer (the sweeper
// conjunct, and see ConsumerFilter's doc: Rebuild or the sweep are the only two
// recoveries from a wrong narrow). A failed Rebuild is the one event that could
// switch BOTH off at once — the rebuild did not happen, and the status it left
// behind suppresses the sweep that would have covered for it.
//
// "active" plus a recorded LastError is the honest pair, not a contradiction: the
// rebuild did not run, so the lens is still consuming under exactly the filter
// and cursor it had before the call — it is active, and it also has a fault worth
// an operator's attention. The status carries liveness; LastError carries the
// verdict. Loupe's fault conjunct keys off a live LastError precisely so the two
// can be read together.
func (p *Pipeline) abandonRebuild(ctx context.Context, sig *rebuildSignal, cause error) error {
	// Release every waiter on THIS rebuild's signal, always. An abandoned
	// rebuild is a finished one as far as a waiter is concerned — it is not
	// draining and nothing further will end it — but `drained` is never set, so
	// the waiter reads it as the failure it is.
	//
	// endRebuild clears the in-flight flag when this rebuild still owns it, and
	// reports that ownership. The status write below belongs to whichever rebuild
	// is current, and this one may no longer be it: announcing "active" under a
	// NEWER rebuild's live rescan would report a lens that is still draining. An
	// older rebuild's failure is still worth recording, but it does not get to
	// retire a newer one's status.
	if owned := p.endRebuild(sig); !owned {
		if p.reporter != nil {
			if recErr := p.reporter.RecordError(ctx, cause.Error()); recErr != nil {
				slog.Error("pipeline: rebuild: record abandoned-rebuild health signal",
					"ruleId", p.ruleID, "err", recErr)
			}
		}
		return cause
	}

	if p.reporter == nil {
		return cause
	}
	// Status FIRST, cause SECOND, and the order is load-bearing: SetActive writes
	// LastError back as an explicit JSON null (Reporter.SetActive), so recording
	// the cause before restoring the status would erase the very thing that makes
	// "active" honest and leave a clean-looking entry behind.
	if setErr := p.reporter.SetActive(ctx); setErr != nil {
		// Louder than the usual health-write warning: this is the path that
		// leaves the convergence sweep suppressed with nothing to un-latch it.
		slog.Error("pipeline: rebuild: could not clear rebuilding status after a failed rebuild — the convergence sweep stays suppressed until this lens is rebuilt or restarted",
			"ruleId", p.ruleID, "err", setErr)
	}
	if recErr := p.reporter.RecordError(ctx, cause.Error()); recErr != nil {
		slog.Error("pipeline: rebuild: record abandoned-rebuild health signal",
			"ruleId", p.ruleID, "err", recErr)
	}
	return cause
}

// rebuildSignal is one rebuild's completion record. `done` is closed when that
// rebuild ends for ANY reason; `drained` is set only by the watcher that
// actually observed the rescan reach zero outstanding.
//
// The two are separate because a closed channel alone cannot answer the
// question an attesting caller is asking. Every exit closes `done`, including a
// watcher cancelled at shutdown — and a waiter selecting on a closed channel and
// an expired context takes either arm at random, so a close read as completion
// makes "the rescan drained" and "the process is going down mid-rescan" the same
// observation about half the time. The flag is what the waiter reads; the
// channel only says "stop waiting".
type rebuildSignal struct {
	done    chan struct{}
	drained atomic.Bool
}

// beginRebuild installs a fresh completion signal for a rebuild about to start
// and returns it. The returned signal belongs to that rebuild: only the
// goroutine that ends it (its watcher, or the error path that abandons it)
// ends it, via endRebuild.
func (p *Pipeline) beginRebuild() *rebuildSignal {
	sig := &rebuildSignal{done: make(chan struct{})}
	p.rebuildWatchMu.Lock()
	p.rebuildWatch = sig
	p.rebuildWatchMu.Unlock()
	return sig
}

// endRebuild ends sig: it clears the pipeline-wide in-flight flag, retracts the
// installed signal and releases every waiter, and reports whether sig was still
// the installed rebuild. The equality check keeps a slow finisher from
// retracting a newer rebuild's signal: a rebuild that started after this one has
// already replaced rebuildWatch, and its own waiters must keep waiting.
//
// Ownership, the flag and the release all move inside ONE critical section
// because they are one decision. Checking ownership through a separate call
// leaves a window between the answer and the mutation it gates, and clearing the
// flag after the release leaves a wider one: a waiter freed by the close can
// begin the next rebuild — beginRebuild takes this same lock, so it cannot — and
// have its `rebuildInFlight` cleared out from under it by the goroutine that
// just ended. That un-suppresses the convergence sweep (Sweeper.suppressed reads
// RebuildInFlight) while the newer rescan is still draining.
//
// What ownership means here is narrow, and the narrowness is why this is not the
// whole story: rebuildWatch tracks the most recently BEGUN rebuild, not the set
// of rescans still running. `Rebuild` is fire-and-forget and does not take
// rebuildSerial, so a second caller can begin — and then abandon — a rebuild
// while an earlier watcher is still polling a live rescan; that abandon owns the
// signal, clears the flag, and un-suppresses the sweep under the earlier rescan.
// The ordering below closes the successor race, NOT that one. Closing it too
// needs the flag to count live watchers rather than name one signal, and the
// consequence meanwhile is bounded: the sweep is a healer, and the attestation
// path reads `drained`, never this flag.
//
// The health STATUS write also stays with the caller, after this returns: it is
// remote I/O and does not belong under a mutex every watcher takes. Two residues
// follow, both bounded. An older goroutine's "active" can land after a newer
// rebuild's "rebuilding", which the newer rebuild corrects when it drains; and
// waiters are now released BEFORE that write, so a caller can return from
// RebuildAndWait while the entry still reads "rebuilding" — no consumer reads
// status after a rebuild, and the flag, which is the load-bearing half, is
// ordered here.
//
// It does NOT set `drained` — only the watcher that saw the rescan finish does
// that — so every other exit (abandoned, cancelled, never watched) releases
// waiters with an honest "this did not drain".
func (p *Pipeline) endRebuild(sig *rebuildSignal) bool {
	p.rebuildWatchMu.Lock()
	defer p.rebuildWatchMu.Unlock()
	owned := p.rebuildWatch == sig
	if owned {
		p.rebuildWatch = nil
		p.rebuildInFlight.Store(false)
	}
	// Closing under the lock rather than beside it: two enders racing on the
	// select/default form would both see it open and double-close, which panics.
	select {
	case <-sig.done:
	default:
		close(sig.done)
	}
	return owned
}

// currentRebuildSignal returns the in-flight rebuild's completion signal, or
// nil when none is running.
func (p *Pipeline) currentRebuildSignal() *rebuildSignal {
	p.rebuildWatchMu.Lock()
	defer p.rebuildWatchMu.Unlock()
	return p.rebuildWatch
}

// RebuildAndWait rebuilds this pipeline and blocks until the rescan has
// drained, serialized against any other RebuildAndWait caller on the same
// pipeline. Rebuild itself is fire-and-forget — it returns as soon as the
// durable has been reset, leaving a background watcher to notice the drain — so
// a caller that must ATTEST to a completed rebuild (the retention-class key
// destruction consumer, retention-class-key-custody-design.md §6.3 step 4)
// cannot use it directly.
//
// It waits out an already-in-flight rebuild BEFORE starting its own rather than
// adopting it, and that ordering is the whole point. A rebuild in progress may
// have been started by another path — the MATCH hot-reloader
// (cmd/refractor/reload.go) or the operator "rebuild" control op
// (control.Service.rebuildRule), neither of which passes through
// rebuildSerial — and it may have begun BEFORE the key destruction this rebuild
// is answering to. Its rows can therefore still carry plaintext the destruction
// was supposed to erase. Counting it as this destruction's rebuild would attest
// to an erasure that never happened, so the wait is fail-closed where a
// CAS-and-skip would fail open.
//
// rebuildSerial serializes RebuildAndWait callers against EACH OTHER and
// nothing more; it does not exclude those two paths, which is exactly why each
// wait is on a per-rebuild signal rather than on shared in-flight state.
//
// wait bounds the two waits, not the rebuild: a deadline-bearing context passed
// into Rebuild would also cancel watchRebuildCompletion, which would leave the
// health entry latched on "rebuilding" and, through Sweeper.suppressed, retire
// the convergence sweep for the life of the process. A rebuild that outlives
// the budget therefore keeps running and keeps its watcher; only this caller
// gives up, with control.ErrRebuildWaitTimeout. A wait <= 0 means no bound, which no
// attesting caller should use — an unbounded wait inside a serial durable
// handler stops every subsequent message on that consumer.
func (p *Pipeline) RebuildAndWait(ctx context.Context, truncate bool, wait time.Duration) error {
	select {
	case p.rebuildSerial <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.rebuildSerial }()

	waitCtx := ctx
	if wait > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, wait)
		defer cancel()
	}

	// Waiting OUT a pre-existing rebuild asks a weaker question than waiting on
	// our own: all that matters is that it has ENDED, so this rescan cannot be
	// mistaken for it. How it ended is that rebuild's business — an abandoned or
	// unwatched one leaves the coast just as clear as a drained one, and
	// refusing to proceed on it would make an unrelated failure permanently
	// block every attesting caller.
	if err := p.waitRebuildSignal(waitCtx, ctx, p.currentRebuildSignal()); err != nil &&
		!errors.Is(err, control.ErrRebuildNotDrained) {
		return fmt.Errorf("pipeline: rebuild: waiting out an in-flight rebuild: %w", err)
	}
	sig, err := p.rebuildWithSignal(ctx, truncate)
	if err != nil {
		return err
	}
	return p.waitRebuildSignal(waitCtx, ctx, sig)
}

// waitRebuildSignal blocks until sig ends, waitCtx expires, or the caller's own
// ctx is cancelled. A nil sig means no rebuild was in flight.
//
// Success is `sig.drained`, never the closed channel: every other way a rebuild
// ends closes the channel too, so reading the close as completion is how a
// shutdown, an abandoned rescan, or a rebuild nothing ever watched all came
// back as "rebuilt". The three error classes are kept distinct because the
// caller's response differs — a cancelled caller retries on the next process, a
// budget expiry means the rescan is still running and healthy, and neither is a
// fault of the LENS, so neither may be answered by pausing it.
func (p *Pipeline) waitRebuildSignal(waitCtx, ctx context.Context, sig *rebuildSignal) error {
	if sig == nil {
		return nil
	}
	select {
	case <-sig.done:
		if sig.drained.Load() {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return control.ErrRebuildNotDrained
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return control.ErrRebuildWaitTimeout
	}
}

// Rebuild starts a rescan and returns immediately, discarding the completion
// signal — the fire-and-forget form every caller that does not attest uses.
func (p *Pipeline) Rebuild(ctx context.Context, truncate bool) error {
	_, err := p.rebuildWithSignal(ctx, truncate)
	return err
}

// rebuildWithSignal is Rebuild plus the completion channel of the rebuild it
// started, closed when that rescan drains (or when it is abandoned). On error
// the signal is already closed and nil is returned: there is nothing to wait
// for.
func (p *Pipeline) rebuildWithSignal(ctx context.Context, truncate bool) (*rebuildSignal, error) {
	sig := p.beginRebuild()
	if err := p.rebuild(ctx, truncate, sig); err != nil {
		return nil, err
	}
	return sig, nil
}

func (p *Pipeline) rebuild(ctx context.Context, truncate bool, sig *rebuildSignal) error {
	// Mark the rebuild in flight before the status write so a concurrent
	// supervisor health persist (probe recovery, operator resume) cannot
	// publish "active" while the rescan is still draining.
	p.rebuildInFlight.Store(true)

	// Clear the previous rebuild's progress record so this one is judged on its
	// own draining, not on a stale timestamp a finished rebuild left behind.
	p.rebuildMu.Lock()
	p.rebuildOutstanding, p.rebuildProgressAt = 0, time.Time{}
	p.rebuildMu.Unlock()

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
	//
	// The force applies only to a target that can actually be truncated. A
	// guarded target that cannot (the grant family, which shares one table
	// across every producer and so must never TRUNCATE it) gets the honest
	// account instead: forcing there would announce a repair that the
	// Truncater branch below then silently declines, leaving the operator to
	// believe the watermarks were cleared when a replay is still about to be
	// rejected against them.
	//
	// "Rejected against them" is only half of what happens, and the half an
	// operator reaching for a rebuild usually does not want. The §6.14 guard
	// lives in the ON CONFLICT arm, so a row still PRESENT replays against its
	// own stored seq and is a no-op, while a row ABSENT — the out-of-band
	// restore or partial wipe a rebuild is the response to — takes the plain
	// INSERT and is re-derived. So an un-truncatable guarded rebuild is a
	// repair for exactly the divergence it looks powerless against, and saying
	// only that rows "survive" discourages the action that fixes it.
	if g, ok := p.currentAdapter().(interface{ Guarded() bool }); ok && g.Guarded() && !truncate {
		if _, truncatable := p.currentAdapter().(adapter.Truncater); truncatable {
			slog.Info("pipeline: rebuild: guarded bucket forces truncate (avoids rejected-write holes)",
				"ruleId", p.ruleID)
			truncate = true
		} else {
			slog.Info("pipeline: rebuild: guarded target cannot be truncated (shared with other producers) — the replay re-derives rows that are ABSENT and leaves rows already present at or above their stored watermark unchanged; this repairs an out-of-band wipe but does not rewrite live rows",
				"ruleId", p.ruleID)
		}
	}
	if truncate {
		adpt := p.currentAdapter()
		if t, ok := adpt.(adapter.Truncater); ok {
			if err := t.Truncate(ctx); err != nil {
				return p.abandonRebuild(ctx, sig, fmt.Errorf("pipeline: rebuild: truncate: %w", err))
			}
		} else {
			slog.Warn("pipeline: rebuild: truncate=true but adapter does not implement Truncater; skipping",
				"ruleId", p.ruleID)
		}
	}

	// 3. Recompute the Core KV filter from the CURRENT compiled rule (see
	// ConsumerFilter's doc: a MATCH hot-reload's UseFullEngineBranches call may
	// have already changed the referenced-label set by the time a rebuild
	// reaches here — activation's filter must not ride forward unexamined) and
	// reset the durable via the supervisor (delete-recreate-swap) with it.
	if p.supervisor == nil {
		return p.abandonRebuild(ctx, sig, fmt.Errorf("pipeline: rebuild: no supervisor configured"))
	}
	filterSubjects, filterSubject := p.ConsumerFilter()
	resetWithFilter := func() error {
		if err := p.supervisor.UpdateSpec(p.consumerCfg.Name, func(spec *substrate.ConsumerSpec) {
			spec.FilterSubjects = filterSubjects
			spec.FilterSubject = filterSubject
		}); err != nil {
			return err
		}
		return p.supervisor.Reset(ctx, p.consumerCfg.Name)
	}
	if err := p.registerWithFilterFallback(ctx, filterSubjects, func() {
		filterSubjects = nil
		filterSubject = subjects.CoreKVFilter(p.coreKVBucket)
	}, resetWithFilter); err != nil {
		return p.abandonRebuild(ctx, sig, fmt.Errorf("pipeline: rebuild: reset consumer: %w", err))
	}

	// 4. Launch background goroutine to transition to "active" when lag reaches zero.
	if p.reporter != nil {
		go p.watchRebuildCompletion(ctx, sig)
	} else {
		// No reporter → no completion watcher, so this rebuild has no observable
		// end. Ending the signal keeps a waiter from blocking forever on a lens
		// that will never report, and leaving `drained` unset is what keeps that
		// waiter from reading the release as a completed rescan: the rescan is
		// still running, it simply cannot be watched. The Rebuilder contract
		// exists so an attesting caller cannot be handed a completion nobody
		// observed, and this is the branch that would otherwise hand it one.
		p.endRebuild(sig)
	}

	return nil
}

// resumeInterruptedRebuild re-arms the rebuilding → active transition for a
// lens whose persisted health entry still reads "rebuilding" when this process
// starts. The watcher Rebuild launches lives only as long as the process that
// armed it; the rescan it watches outlives that process, because the rebuild
// IS the durable's reset cursor and JetStream keeps it. So after a crash or an
// ordinary cycle the drain carries on with nothing left to declare it
// finished, and the status reads "rebuilding" for the rest of the lens's life.
//
// That is not a cosmetic mislabel: the capability convergence sweep suppresses
// itself on a rebuilding lens, so a latched status quietly retires the auth
// plane's only projection-convergence check while every liveness signal stays
// green.
//
// Re-arming rather than clearing is the point. An interrupted rebuild whose
// backlog has not drained is genuinely still rebuilding, and writing "active"
// over it would be exactly the premature transition watchRebuildCompletion's
// outstanding-not-backlog check exists to prevent. One poll answers both
// cases: it clears on the first tick when the drain already finished, and
// holds an honest "rebuilding" while it has not.
func (p *Pipeline) resumeInterruptedRebuild(ctx context.Context) {
	if p.reporter == nil {
		return
	}
	entry, err := p.reporter.GetStatus(ctx)
	if err != nil {
		slog.Warn("pipeline: could not read health status to resume an interrupted rebuild",
			"ruleId", p.ruleID, "err", err)
		return
	}
	if entry.Status != health.StatusRebuilding {
		return
	}
	// Losing this swap means a rebuild is already in flight in THIS process —
	// a control-plane Rebuild that arrived while Run was still starting — and
	// it already owns a watcher.
	if !p.rebuildInFlight.CompareAndSwap(false, true) {
		return
	}
	slog.Info("pipeline: resuming the watch for a rebuild interrupted by a restart", "ruleId", p.ruleID)
	go p.watchRebuildCompletion(ctx, p.beginRebuild())
}

// watchRebuildCompletion polls the supervised consumer's outstanding count at
// rebuildPollInterval. When it reaches zero, it transitions health KV from
// "rebuilding" back to "active" (AC5).
//
// Outstanding counts the un-delivered backlog *and* the delivered-but-unacked
// messages: a message the pump has fetched leaves the backlog the instant it is
// delivered, so a backlog-only check reads zero mid-flight and would publish
// "active" over a rescan that has not drained — and that is not a transient
// mislabel when the in-flight message then fails and is redelivered.
func (p *Pipeline) watchRebuildCompletion(ctx context.Context, sig *rebuildSignal) {
	// The rebuild window ends when this watcher exits for any reason, so the
	// deferred endRebuild both releases the waiters on THIS rebuild and clears
	// the in-flight flag — but only while this rebuild still owns it. Clearing it
	// unconditionally is the same hazard the abandon path guards against, on the
	// path that runs every time: a watcher cancelled at shutdown, or one that
	// returns after a newer rebuild has already begun, would otherwise
	// un-suppress the convergence sweep under a live rescan.
	defer p.endRebuild(sig)
	ticker := time.NewTicker(p.rebuildPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			outstanding, err := p.supervisor.OutstandingForConsumer(ctx, p.consumerCfg.Name)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// Consumer may still be initializing or context cancelled; retry.
				// No progress is recorded: this retries forever, so an error that
				// never clears must read as wedged rather than as quietly fine.
				continue
			}
			p.recordRebuildProgress(outstanding)
			if outstanding == 0 {
				// The ONE place a rebuild is recorded as genuinely finished. Set
				// before the release below, so no waiter can observe the closed
				// channel without it.
				sig.drained.Store(true)
				// endRebuild clears the in-flight flag before releasing waiters,
				// so a concurrent health sink re-checking the flag converges on
				// "active" — and reports whether this rebuild is still the current
				// one. Announcing "active" when it is not would retire a newer
				// rescan's status mid-drain. The deferred endRebuild runs again on
				// the way out and is a no-op by then.
				if owned := p.endRebuild(sig); owned && p.reporter != nil {
					if serr := p.reporter.SetActive(ctx); serr != nil {
						slog.Error("pipeline: rebuild: set active", "ruleId", p.ruleID, "err", serr)
					}
				}
				return
			}
		}
	}
}

// recordRebuildProgress folds one poll of the un-drained count into the rebuild's
// progress record. The timestamp advances only on a STRICT decrease, so it
// answers "when did this rebuild last actually drain something" rather than
// "when was it last observed" — the second is true of a wedged rebuild every
// poll interval and would report it as healthy forever.
//
// A count that goes UP is still progress in the sense that matters here: the
// consumer is live and the backlog is real, and a rebuild racing new writes can
// legitimately grow. What it must not do is reset the clock, so an oscillating
// count cannot mask a rebuild that never gets closer to done.
func (p *Pipeline) recordRebuildProgress(outstanding uint64) {
	p.rebuildMu.Lock()
	defer p.rebuildMu.Unlock()
	if p.rebuildProgressAt.IsZero() || outstanding < p.rebuildOutstanding {
		p.rebuildProgressAt = time.Now()
	}
	p.rebuildOutstanding = outstanding
}

// RebuildProgress reports the rebuild's un-drained count and when it last
// decreased. progressAt is zero when no rebuild has polled yet, which a consumer
// must read as "unknown", never as "stalled since the epoch".
func (p *Pipeline) RebuildProgress() (outstanding uint64, progressAt time.Time) {
	p.rebuildMu.Lock()
	defer p.rebuildMu.Unlock()
	return p.rebuildOutstanding, p.rebuildProgressAt
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

	// One snapshot of the compiled rule for this whole message (see ruleMu). A
	// MATCH hot-reload runs on CoreKVSource's dispatch goroutine and can land at
	// any point below, so taking the rule once here is what makes this event
	// judged AND evaluated by a single rule — the next event picks up the new
	// one. Every gate and every evaluate arm reached from here is threaded this
	// value rather than re-reading the pipeline.
	rs := p.ruleState()

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
			// An eligible actor-aware lens skips an aspect whose parent vertex
			// type its patterns cannot bind. A key that fails to parse falls
			// through so the fan-out raises the real error rather than being
			// silently dropped here.
			if _, parentType, _, _, pok := substrate.ParseAspectKey(key); pok &&
				!p.actorAwareFanOutRelevant(rs, parentType) {
				return substrate.Ack, nil
			}
			return p.evalAspectFanOut(ctx, rs, msg, key)
		}
		return p.evalPlainAspectReprojection(ctx, rs, msg, key)
	case substrate.KindLink:
		// A pure link mutation (holdsRole/manages/appliesToUnit/...) changes
		// graph topology with no vertex event. On the actor-aware pipeline it
		// drives a fan-out reprojection from both endpoints; on a plain lens
		// it re-executes seeded from each endpoint vertex — refreshing
		// link-derived rows and retracting an endpoint anchor that a
		// required-link removal drops from the matched set.
		if p.actorEnumerator != nil {
			// Skipped only when NEITHER endpoint type can bind. The fan-out's
			// idempotent adjacency pre-apply is lost with the skip and that is
			// sound: it exists to stop THIS pipeline's reprojection racing ahead
			// of its own trigger edge (evaluateLinkFanOut), and the
			// authoritative adjacency write is the dedicated whole-stream
			// consumer's, not this one's. No reprojection, nothing to order.
			if t1, _, _, t2, _, pok := substrate.ParseLinkKey(key); pok &&
				!p.actorAwareFanOutRelevant(rs, t1, t2) {
				return substrate.Ack, nil
			}
			return p.evalLinkFanOut(ctx, rs, msg, key, tombstone)
		}
		return p.evalPlainLinkReprojection(ctx, rs, msg, key)
	case substrate.KindUnknown:
		slog.Warn("pipeline: unknown key shape — defect signal",
			"ruleId", p.ruleID, "key", key)
		return substrate.Ack, nil
	}

	// KindVertex. An empty body carries no props for any lens shape, so it is
	// acked and skipped unconditionally — the actor-aware pipeline included.
	// The event that actually retracts an actor is the SOFT delete
	// (isDeleted: true on a non-empty vertex root), which reaches the evaluate
	// path below and emits the cap Delete from there.
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

	// A lens whose compiled patterns provably cannot bind this vertex type
	// skips the re-execute outright — the KindVertex counterpart of the
	// aspect/link arms' relevance skip. Unlike those arms, KindVertex has no
	// per-type dispatch of its own (every vertex-root event runs the same full
	// evaluate below), so every vertex write in the system would otherwise fan
	// into a full evaluation regardless of type. Both pipeline shapes consult
	// the one gate: a plain lens through plainVertexRelevant, an actor-aware one
	// through §4.2's conjunction.
	if !p.vertexEventRelevant(rs, label) {
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
	results, enumeratedActors, err := p.evaluateForEntry(ctx, rs, entry)
	if err != nil {
		slog.Error("pipeline: evaluate",
			"ruleId", p.ruleID, "entityId", key,
			"stage", "traversal", "adapter", p.adapterName, "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}

	// Write each result through the shared write path (failure classification,
	// terminal DLQ, retry enqueue, ack discipline). The adapter is captured
	// once inside writeResults so all results in this message use a consistent
	// instance even if HotReloadInto swaps it between messages.
	return p.writeResults(ctx, rs, msg, key, results, enumeratedActors)
}

// evalLinkFanOut handles a KindLink CDC event on the actor-aware pipeline.
// It determines whether the link is a create or a tombstone, drives the
// endpoint-seeded fan-out reprojection (evaluateLinkFanOut), and writes the
// resulting capability projections through the normal write path.
//
// A link tombstone arrives with an empty body (NATS DEL/PURGE). A link create
// or update arrives with a body whose `isDeleted` field distinguishes a
// soft-delete (revocation) from an active link.
func (p *Pipeline) evalLinkFanOut(ctx context.Context, rs ruleState, msg substrate.Message, key string, tombstone bool) (substrate.Decision, error) {
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

	results, enumeratedActors, err := p.evaluateLinkFanOut(ctx, rs, key, isDeleted)
	if err != nil {
		slog.Error("pipeline: link fan-out: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}
	if err := p.applySecureDecrypt(ctx, results); err != nil {
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}

	return p.writeResults(ctx, rs, msg, key, results, enumeratedActors)
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
func (p *Pipeline) evalPlainAspectReprojection(ctx context.Context, rs ruleState, msg substrate.Message, key string) (substrate.Decision, error) {
	parentVtx, parentType, _, _, ok := substrate.ParseAspectKey(key)
	if !ok {
		return substrate.Ack, nil
	}
	if !rs.plainReactsTo(parentType) {
		// The lens's patterns cannot bind this vertex type — the mutation
		// cannot change its rows; skip the re-execute.
		return substrate.Ack, nil
	}
	results, err := p.evaluatePlainFromVertex(ctx, rs, parentVtx, parentType)
	if err != nil {
		slog.Error("pipeline: plain aspect reprojection: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, nil)
	}
	return p.writeResults(ctx, rs, msg, key, results, nil)
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
// the same CDC event with no cross-consumer ordering guarantee, so applying
// it here first is what guarantees the re-execute always reads a consistent
// edge set — a tombstone's re-execute can never still see the removed edge.
func (p *Pipeline) evalPlainLinkReprojection(ctx context.Context, rs ruleState, msg substrate.Message, key string) (substrate.Decision, error) {
	type1, id1, linkName, type2, id2, ok := substrate.ParseLinkKey(key)
	if !ok {
		return substrate.Ack, nil
	}
	if !rs.plainLinkReactsTo(linkName) {
		// The lens's patterns never traverse this relation, so the link
		// cannot appear in any of them whatever its endpoints are — the same
		// skip class as the endpoint test below, for the same reason.
		return substrate.Ack, nil
	}
	reacts1, reacts2 := rs.plainReactsTo(type1), rs.plainReactsTo(type2)
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
				failure.Terminal(fmt.Errorf("pipeline: plain link reprojection: unmarshal %q: %w", key, err)), nil)
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
			return p.dispositionEvalErr(ctx, msg, key, "traversal", err, nil)
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
		results, err := p.evaluatePlainFromVertex(ctx, rs, ep.vtx, ep.label)
		if err != nil {
			slog.Error("pipeline: plain link reprojection: evaluate",
				"ruleId", p.ruleID, "entityId", key, "endpoint", ep.vtx,
				"stage", "traversal", "err", err)
			return p.dispositionEvalErr(ctx, msg, key, "traversal", err, nil)
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
	return p.writeResults(ctx, rs, msg, key, combined, nil)
}

// evaluatePlainFromVertex point-reads a vertex and runs the plain evaluate
// path seeded from it — the shared core of the plain aspect/link reprojection
// arms. A missing or tombstoned vertex yields (nil, nil): its row lifecycle
// belongs to the vertex-root event path.
func (p *Pipeline) evaluatePlainFromVertex(ctx context.Context, rs ruleState, vtxKey, vtxType string) ([]ruleengine.EvalResult, error) {
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
	results, _, err := p.evaluateForEntry(ctx, rs, entry)
	return results, err
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
func (p *Pipeline) evalAspectFanOut(ctx context.Context, rs ruleState, msg substrate.Message, key string) (substrate.Decision, error) {
	results, enumeratedActors, err := p.evaluateAspectFanOut(ctx, rs, key)
	if err != nil {
		slog.Error("pipeline: aspect fan-out: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}
	if err := p.applySecureDecrypt(ctx, results); err != nil {
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}

	return p.writeResults(ctx, rs, msg, key, results, enumeratedActors)
}

// dispositionEvalErr maps an evaluate-stage error to a Decision (+ error for the
// pause path), mirroring the inline ack/nak discipline the pre-supervisor
// pipeline applied: Infra/Structural leave the message pending (return the error
// so the supervisor pauses); Terminal publishes a DLQ entry and acks; Transient
// naks for redelivery.
//
// A footprint-validation drift (failure.ErrEvalDrift, evaluation-consistency-
// design.md §4.3) is Transient like any other, but a bare Nak would only
// redeliver the SAME triggering message — re-running the exact evaluation
// that just drifted, with no backoff and no health signal. When this
// pipeline is actor-aggregate (an envelope wrapper installed, the only shape
// ErrEvalDrift can ever come from — needsFootprintValidation gates on it) and
// a retry queue is configured, the failing actor(s) are enqueued through the
// same re-evaluate-and-write closure the write-path transient-failure branch
// already uses (enqueueActorReprojectRetry), so the drift gets the queue's
// backoff/DLQ/health machinery instead of an un-backed-off redelivery loop.
// enumeratedActors is nil for a plain lens (which can never produce
// ErrEvalDrift — needsFootprintValidation always false) and for the actor's
// own vertex re-evaluating itself, where key IS the actor key.
func (p *Pipeline) dispositionEvalErr(ctx context.Context, msg substrate.Message, key, stage string, err error, enumeratedActors []string) (substrate.Decision, error) {
	cat := failure.Classify(err)
	if cat == failure.CatInfra || cat == failure.CatStructural {
		// Do NOT dispose — leave pending for redelivery when the pipeline resumes.
		return substrate.Nak, err
	}
	if cat == failure.CatTerminal {
		p.publishTerminalDLQ(ctx, msg.Body, key, stage, err)
		return substrate.Ack, nil
	}
	if errors.Is(err, failure.ErrEvalDrift) && p.retryQueue != nil && p.retryMaxAttempts > 0 &&
		(p.envelopeFn != nil || p.multiEnvelopeFn != nil) && p.actorEnumerator != nil {
		actors := enumeratedActors
		if len(actors) == 0 {
			actors = []string{key}
		}
		for _, actor := range actors {
			p.enqueueActorReprojectRetry(msg.Body, actor)
		}
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
//
// enumeratedActors (personal-lens-retraction-design.md §3.2, R1) is nil for
// a plain lens or a non-personal actor-aware pipeline; otherwise a keyset
// frame is published per enumerated actor once every result has cleanly
// applied (no retry-enqueue, no terminal DLQ) — a partially-disposed batch
// emits no frame, so a would-be-retracting frame can never race ahead of
// the write it is supposed to describe.
// supersededRule reports whether the rule an evaluation ran under has been
// replaced since its snapshot was taken. A true answer means the results in
// hand were derived from a rule no longer in force, and must not be written.
//
// This exists because a stale write is not self-correcting on the CDC path.
// Every result is stamped with THIS message's stream sequence (writeResults
// below), and a MATCH hot-reload's rebuild replays the same messages with the
// same sequences — so the guarded adapter's monotonic check (natskv.go's
// storedSeq >= incomingSeq) drops the replay as an idempotent no-op. The
// rebuild's truncate is what normally clears the way for it, but the truncate
// runs on the reload's own goroutine while this handler is still mid-flight
// (cmd/refractor/reload.go's MatchChange arm), so an in-flight evaluation can
// land its stale row AFTER the purge and then swallow its own correction.
//
// On the auth plane that row is the pre-edit permission set: a MATCH edit made
// to REVOKE something would be silently defeated for every actor the in-flight
// event touched. The convergence sweep heals it eventually, but only for a lens
// that got a sweep plan — projection/driver.go refuses enrolment with a warning
// only — so it is not a bound worth relying on.
//
// Naking is the honest disposition: redelivery re-evaluates the message under
// the new rule, which is exactly what the reload wanted. It cannot loop, since
// each Nak is answered by a redelivery that finds a settled rule unless yet
// another reload has landed.
func (p *Pipeline) supersededRule(rs ruleState) bool {
	p.ruleMu.RLock()
	defer p.ruleMu.RUnlock()
	return p.ruleGen != rs.gen
}

func (p *Pipeline) writeResults(ctx context.Context, rs ruleState, msg substrate.Message, key string, results []ruleengine.EvalResult, enumeratedActors []string) (substrate.Decision, error) {
	if p.supersededRule(rs) {
		slog.Info("pipeline: rule swapped mid-event — naking so redelivery evaluates the new rule",
			"ruleId", p.ruleID, "entityId", key, "seq", msg.Sequence)
		return substrate.Nak, nil
	}
	adpt := p.currentAdapter()
	// Resolved once per call — adpt itself is captured once above, so whether
	// it reports upsert outcomes cannot change across this call's loop.
	outcomeAdpt, reportsOutcome := adpt.(adapter.OutcomeUpserter)
	var retryResults []ruleengine.EvalResult
	var terminalErrs []error
	transientActorRetry := false
	for i := range results {
		// Stamp the triggering CDC message's stream sequence as the monotonic
		// ordering token before any write. The retry-queue capture copies the
		// stamped result, so a replay carries this same (original, lower) seq,
		// which is exactly what must lose to a later real reprojection.
		results[i].ProjectionSeq = msg.Sequence
	}
	for _, result := range results {
		var writeErr error
		wrote := true
		if result.Delete {
			writeErr = adpt.Delete(ctx, result.Keys, result.ProjectionSeq)
		} else if reportsOutcome {
			var outcome adapter.UpsertOutcome
			outcome, writeErr = outcomeAdpt.UpsertWithOutcome(ctx, result.Keys, result.Row, result.ProjectionSeq)
			wrote = outcome.Wrote
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

			if result.FailClosed {
				// A FailClosed result's own failure must never be masked by
				// continuing to write its batch siblings (ruleengine.EvalResult's
				// doc) — abort for a full redelivery regardless of category,
				// rather than let e.g. CatTransient's per-actor-continue land a
				// sibling's fresh upsert while this retraction never took effect.
				return substrate.Nak, writeErr
			}
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
				if p.multiEnvelopeFn != nil {
					// §4.3: a perEntry lens's grants live under N per-anchor
					// keys, not the single parent row a raw WriteFn replay
					// (enqueueRetry) assumes. Replaying this failed write's
					// captured Keys/Row/Seq later could resurrect a
					// since-revoked anchor through the absent-key Create
					// door — a grant-era write that failed here leaves no
					// key and no watermark, so a later revocation's prefix
					// diff can never tombstone it, and the stale replay then
					// lands unopposed. Refuse the raw replay and re-evaluate
					// the actor instead (enqueueActorReprojectRetry) — the
					// same unit the sweep already repairs at, so a revoked
					// anchor is simply absent from the fresh set rather than
					// resurrected.
					transientActorRetry = true
					continue
				}
				retryResults = append(retryResults, result)
				continue
			}
			return substrate.Nak, nil
		}

		p.recordProjected()
		if wrote {
			p.writeAudit(ctx, key, result)
		}
	}

	for _, terr := range terminalErrs {
		p.publishTerminalDLQ(ctx, msg.Body, key, "write", terr)
	}
	for _, r := range retryResults {
		p.enqueueRetry(key, msg.Body, r)
	}
	if transientActorRetry {
		// enumeratedActors is nil for the actor's own vertex re-evaluating
		// itself (key IS the actor key there); a fan-out call already names
		// every affected actor — but only because InstallActorAggregate
		// (projection/driver.go) always pairs multiEnvelopeFn with an
		// ActorEnumerator, so this fallback is safe ONLY under that pairing.
		// Nothing here structurally enforces it (a hand-built pipeline could
		// set multiEnvelopeFn alone), and getting it wrong is worse than the
		// bug this mechanism replaces: Reproject on a non-actor key (an
		// aspect/link key) evaluates to zero rows and returns a clean
		// "wrote nothing", which reads as success — the failed write
		// vanishes with no DLQ, no trace. Refuse closed instead of guessing.
		if p.actorEnumerator == nil {
			err := fmt.Errorf("pipeline: writeResults: rule %q: a perEntry lens (multiEnvelopeFn) has no ActorEnumerator installed — refusing to guess an actor key for retry rather than risk reprojecting the wrong entity or silently losing the write", p.ruleID)
			slog.Error("pipeline: transient write refused — perEntry lens missing its ActorEnumerator pairing", "ruleId", p.ruleID, "entityId", key, "err", err)
			return substrate.Nak, err
		}
		// Reprojects every actor this batch touched, not just the one whose
		// write failed — a zero-write no-op for any actor whose row already
		// converged (Reproject's own comparison). This is a known cost, not
		// a free one: a large fan-out (ActorEnumerator's own documented cap
		// is in the thousands) turns one transient blip into that many full
		// cypher re-evaluations queued on the pipeline's single shared
		// RetryQueue, head-of-line-blocking every other lens's retries for
		// the duration. Narrowing this to only the actor(s) that actually
		// own a failed result needs the same key→actor inversion §4.4's
		// AnchorFromKey builds for the sweep — deliberately not duplicated
		// here; this increment accepts the coarser, safe-but-costlier retry
		// unit and names the cost rather than silently absorbing it.
		//
		// enumeratedActors is nil for the actor's own vertex re-evaluating
		// itself — key IS the actor key there, safe now that the check
		// above has confirmed p.actorEnumerator != nil (the only condition
		// that fallback ever relies on).
		actors := enumeratedActors
		if len(actors) == 0 {
			actors = []string{key}
		}
		for _, actor := range actors {
			p.enqueueActorReprojectRetry(msg.Body, actor)
		}
	}

	if len(retryResults) > 0 || len(terminalErrs) > 0 || transientActorRetry {
		// Transient enqueue / terminal DLQ: the message is fully disposed —
		// ack to prevent redelivery (the retry queue owns the eventual write).
		// No frame here — a retry-enqueued or DLQ'd result did not (yet, or
		// ever) apply, so a frame built from `results` would describe state
		// that isn't true; the next live event or hydrate heals (§3.5).
		return substrate.Ack, nil
	}

	p.emitPersonalFrames(ctx, adpt, enumeratedActors, results, msg.Sequence)

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

// enqueueActorReprojectRetry constructs and enqueues a RetryEntry for a
// perEntry lens's transient write failure, re-evaluating actorKey (via
// Reproject) on each attempt instead of replaying a captured raw write
// (§4.3 of cap-read-per-anchor-grant-keys-design.md — see the writeResults
// comment at the call site for why a raw replay is unsafe for per-anchor
// keys). Reuses the same RetryEntry queue/backoff/DLQ-escalation machinery
// as enqueueRetry; only the WriteFn's unit of work changes, from "the write"
// to "the actor".
func (p *Pipeline) enqueueActorReprojectRetry(rawPayload []byte, actorKey string) {
	capturedReporter := p.reporter
	capturedSeq := ""
	if p.reporter != nil {
		if seq := p.reporter.ActiveSequence(); seq != 0 {
			capturedSeq = fmt.Sprintf("%d", seq)
		}
	}
	e := &failure.RetryEntry{
		RuleID: p.ruleID,
		// actorKey, not key: key is the triggering entity (an actor's own
		// vertex, or a fan-out's aspect/link key) — actorKey is the thing
		// actually being retried, and the only identity worth logging or
		// escalating to the DLQ on exhaustion. A fan-out batch enqueues one
		// entry per actor, so each must name its OWN actor here or every
		// entry (and every DLQ message on exhaustion) reads identically and
		// names none of them.
		EntityID:     actorKey,
		Stage:        "write",
		RawPayload:   rawPayload,
		RuleSequence: capturedSeq,
		WriteFn: func(rctx context.Context) error {
			res, err := p.Reproject(rctx, actorKey)
			if err != nil {
				return err
			}
			if res.Verdict == VerdictBlocked {
				// The reconciliation ran and the ordering guard declined its
				// write, so the repair this entry owes has NOT been made —
				// retiring the entry here would report the owed repair as
				// delivered and leave nothing else looking at the row.
				//
				// Retrying is productive rather than a spin: the token is the
				// pipeline's last-applied sequence, which advances on every
				// acked event, so a later attempt carries a token that can
				// outrank the stored watermark. If the backoff is exhausted
				// first, the entry reaches the DLQ and records an error, which
				// is an honest terminal signal — and strictly better than the
				// silence it replaces.
				return fmt.Errorf("pipeline: actor-reproject %q: %s", actorKey, res.VerdictReason)
			}
			return nil
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

// RemoveConsumer stops this pipeline's supervised consumer and deletes its
// server-side durable — the JetStream state a lens tombstone or an operator
// "delete" must not strand (docs/components/refractor.md's Lens lifecycle
// step 9; control.Deleter). Waits (briefly) for Run to have registered the
// consumer first — the same awaitStarted race guard Pause/Resume use — so a
// removal issued in the narrow window right after Run starts is not silently
// dropped. No-op if the consumer never started or RunOn was never called.
//
// MUST be called BEFORE the pipeline's run context is cancelled. Run's own
// shutdown calls the supervisor's Stop on ctx.Done, and Stop — by substrate
// doctrine — clears the supervisor's managed-consumer registry WITHOUT
// deleting anything (a durable's persisted ack floor is the point of its
// durability). A removal attempted after that point finds nothing registered
// and silently no-ops, leaving the durable stranded — exactly the leak this
// method exists to close. Calling it first, while Run is still alive and the
// supervisor's registry still holds the entry, avoids that; the supervisor's
// Remove already stops the pump before deleting the durable, so no live pull
// loop is fetching against a consumer that is about to disappear (no
// delete-out-from-under-a-live-loop error noise).
//
// Safe to call from any goroutine; idempotent — removing an already-gone
// consumer is a no-op, matching every other substrate Delete*/Remove call.
func (p *Pipeline) RemoveConsumer(ctx context.Context) error {
	if p.supervisor == nil {
		return nil
	}
	if !p.awaitStarted(ctx) {
		return nil
	}
	return p.supervisor.Remove(ctx, p.consumerCfg.Name)
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

// DeleteAllForActor removes every child key under actorKey's perEntry prefix
// (cap-read-per-anchor-grant-keys-design.md §4.2 point (d)), via the
// currently-active adapter, independent of the rule's own CDC stream — the
// perEntry-lens analog of Delete: a perEntry lens's grants for one actor live
// under N per-anchor keys rather than the single parent key Delete targets, so
// the Refractor KeyShredded nullification listener (control.RowSetNullifier)
// calls this instead when a target lens is configured PerEntry. Refuses
// closed when this pipeline is not actually a perEntry lens (no
// MultiEnvelopeFn installed): a doc-mode lens's row lives at the parent key
// itself, not under a trailing-dot child prefix, so a mis-flagged
// NullifyTarget would otherwise list zero keys and return nil — a silent,
// falsely-clean shred rather than a loud misconfiguration error. Also
// requires the adapter to support prefix listing (adapter.PrefixKeyLister —
// the same requirement multiEntryRetractions already enforces for CDC-driven
// retraction). Attempts every listed key even if some fail, joining their
// errors, so a transient failure on one key never abandons the rest (this
// path is never retried by its caller — see keyshredded's privacy-critical
// tier — so partial progress here is strictly better than none). Safe to
// call from any goroutine; the adapter's Delete is idempotent per key, so
// re-shredding an already-tombstoned entry is harmless. NOT a closed
// guarantee against a child key created after the enumeration below: unlike
// NullifyRow (which can plant a MaxInt64 tombstone at a key that does not
// exist yet), this method can only act on keys that were live at ListKeysPrefix
// time — a later CDC delivery that re-derives the actor's projection can
// still write a brand-new anchor key this call never saw.
func (p *Pipeline) DeleteAllForActor(ctx context.Context, actorKey string, projectionSeq uint64) error {
	if p.multiEnvelopeFn == nil {
		return fmt.Errorf("pipeline: DeleteAllForActor: rule %q is not a perEntry lens (no MultiEnvelopeFn installed) — refusing rather than risk silently reporting a clean shred", p.ruleID)
	}
	adpt := p.currentAdapter()
	lister, ok := adpt.(adapter.PrefixKeyLister)
	if !ok {
		return fmt.Errorf("pipeline: DeleteAllForActor: adapter %T cannot enumerate keys by prefix — a perEntry lens cannot shred an actor's per-anchor keys", adpt)
	}
	childPrefix := p.actorDeleteKeyFor(actorKey) + "."
	existing, err := lister.ListKeysPrefix(ctx, childPrefix)
	if err != nil {
		return fmt.Errorf("pipeline: DeleteAllForActor: list %q: %w", childPrefix, err)
	}
	var errs []error
	deleted := 0
	for _, keys := range existing {
		if err := adpt.Delete(ctx, keys, projectionSeq); err != nil {
			errs = append(errs, fmt.Errorf("delete %v: %w", keys, err))
			continue
		}
		deleted++
	}
	if len(errs) > 0 {
		return fmt.Errorf("pipeline: DeleteAllForActor: deleted %d/%d keys under %q, then failed: %w",
			deleted, len(existing), childPrefix, errors.Join(errs...))
	}
	return nil
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
