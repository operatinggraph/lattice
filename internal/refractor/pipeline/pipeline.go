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
	p.engineKind = ruleengine.EngineFull
	p.fullEngine = eng
	p.fullCR = cr
	// Unconditional, not just the len>1 arm: a reload (cmd/refractor/reload.go)
	// calls this on an EXISTING pipeline, so a lens edited from 2+ Walks down
	// to a single Walk must clear both fields — leaving them set would keep
	// evaluating a Walk the new spec no longer has.
	if len(branches) > 1 {
		p.fullCRBranches = branches
		p.fullCRWalkOwnedColumns = walkOwnedColumns(branches)
	} else {
		p.fullCRBranches = nil
		p.fullCRWalkOwnedColumns = nil
	}
	// Pin the vertex types this lens's patterns can bind, so the plain
	// aspect/link reprojection arms skip events on types the lens cannot
	// read (an unbounded label set — unlabeled node pattern or var-length
	// relationship — disables the skip; every event reprojects). Union
	// across every branch for a multi-walk lens: each branch's own clauses
	// bind only that walk's types, but the plain-reprojection arms reason
	// about the lens as a whole.
	p.plainReprojectLabels = nil
	p.plainReprojectAll = true
	all := []ruleengine.CompiledRule{cr}
	if len(branches) > 1 {
		all = branches
	}
	labels := map[string]struct{}{}
	exhaustive := true
	for _, c := range all {
		fullCR, isFull := c.(*full.CompiledRule)
		if !isFull {
			exhaustive = false
			break
		}
		ls, ok := fullCR.ReferencedLabels()
		if !ok {
			exhaustive = false
			break
		}
		for l := range ls {
			labels[l] = struct{}{}
		}
	}
	if exhaustive {
		p.plainReprojectLabels = labels
		p.plainReprojectAll = false
	}
	// Pin the anchor label an anchor-labeled event can seed the evaluation
	// with. Unconditional like the label set above, and for the same reason: a
	// reload must never leave a previous rule body's anchor armed. A multi-walk
	// lens is excluded outright — branch merging evaluates N independent
	// queries, each with its own anchor, and one seed cannot speak for all of
	// them.
	p.seedAnchorLabel = ""
	if len(branches) <= 1 {
		if fullCR, isFull := cr.(*full.CompiledRule); isFull {
			if label, ok := fullCR.AnchorLabel(); ok {
				p.seedAnchorLabel = label
			}
		}
	}
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
func (p *Pipeline) seedAnchorFor(eventLabel, eventKey string) string {
	if p.seedAnchorLabel == "" || eventKey == "" || eventLabel != p.seedAnchorLabel {
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
func (p *Pipeline) plainVertexRelevant(vertexType string) bool {
	if p.engineKind != ruleengine.EngineFull {
		return true
	}
	if p.plainReprojectAll || vertexType == "" {
		return true
	}
	_, ok := p.plainReprojectLabels[vertexType]
	return ok
}

// NarrowedFilterEligible reports whether this pipeline's Core KV consumer may
// be scoped to a narrowed, server-side FilterSubjects set instead of the
// broad $KV.<bucket>.> filter, and if so the exhaustive set of vertex-type
// labels to derive it from — the SAME plainReprojectLabels/plainReprojectAll
// useFullEngineBranches already computed from every compiled branch's
// ReferencedLabels(), not a second derivation.
//
// Eligible only when every condition the plain aspect/link/vertex
// reprojection arms already gate CORRECTNESS on also holds:
// actorEnumerator == nil (the exact check the KindVertex handler makes
// before consulting plainVertexRelevant — an actor-aware pipeline's fan-out
// is not bounded by its own MATCH labels, so it must keep the broad filter),
// engineKind == EngineFull (plainReactsTo/plainVertexRelevant have no label
// data for any other engine), and an EXHAUSTIVE referenced-label set
// (!plainReprojectAll). A narrowed consumer can therefore never be delivered
// an event the client-side gates would not already have ack-and-skipped as
// irrelevant: server-side filtering here is strictly more conservative than
// plainVertexRelevant/plainReactsTo, computed from the exact same data those
// gates already trust — never a second, independently-fallible judgment.
func (p *Pipeline) NarrowedFilterEligible() (labels map[string]struct{}, ok bool) {
	if p.actorEnumerator != nil || p.engineKind != ruleengine.EngineFull || p.plainReprojectAll {
		return nil, false
	}
	return p.plainReprojectLabels, true
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
func (p *Pipeline) ConsumerFilter() (filterSubjects []string, filterSubject string) {
	labels, ok := p.NarrowedFilterEligible()
	if !ok || len(labels) == 0 || len(labels) > maxNarrowedFilterLabels {
		return nil, subjects.CoreKVFilter(p.coreKVBucket)
	}
	labelList := make([]string, 0, len(labels))
	for l := range labels {
		labelList = append(labelList, l)
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
				p.rebuildInFlight.Store(false)
				return fmt.Errorf("pipeline: rebuild: truncate: %w", err)
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
		p.rebuildInFlight.Store(false)
		return fmt.Errorf("pipeline: rebuild: no supervisor configured")
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

// watchRebuildCompletion polls the supervised consumer's outstanding count at
// rebuildPollInterval. When it reaches zero, it transitions health KV from
// "rebuilding" back to "active" (AC5).
//
// Outstanding counts the un-delivered backlog *and* the delivered-but-unacked
// messages: a message the pump has fetched leaves the backlog the instant it is
// delivered, so a backlog-only check reads zero mid-flight and would publish
// "active" over a rescan that has not drained — and that is not a transient
// mislabel when the in-flight message then fails and is redelivered.
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

	// A plain lens whose compiled patterns provably cannot bind this vertex
	// type skips the re-execute outright — the KindVertex counterpart of the
	// aspect/link arms' plainReactsTo skip. Unlike those arms, KindVertex has
	// no per-type dispatch of its own (every vertex-root event runs the same
	// full evaluate below), so every vertex write in the system would
	// otherwise fan into a full evaluation on every plain lens regardless of
	// type. The actor-aware pipeline has its own
	// fan-out semantics (evaluateFanOut's actorType routing) and is untouched.
	if p.actorEnumerator == nil && !p.plainVertexRelevant(label) {
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
	results, enumeratedActors, err := p.evaluateForEntry(ctx, entry)
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
	return p.writeResults(ctx, msg, key, results, enumeratedActors)
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

	results, enumeratedActors, err := p.evaluateLinkFanOut(ctx, key, isDeleted)
	if err != nil {
		slog.Error("pipeline: link fan-out: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}
	if err := p.applySecureDecrypt(ctx, results); err != nil {
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}

	return p.writeResults(ctx, msg, key, results, enumeratedActors)
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
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, nil)
	}
	return p.writeResults(ctx, msg, key, results, nil)
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
		results, err := p.evaluatePlainFromVertex(ctx, ep.vtx, ep.label)
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
	return p.writeResults(ctx, msg, key, combined, nil)
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
	results, _, err := p.evaluateForEntry(ctx, entry)
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
func (p *Pipeline) evalAspectFanOut(ctx context.Context, msg substrate.Message, key string) (substrate.Decision, error) {
	results, enumeratedActors, err := p.evaluateAspectFanOut(ctx, key)
	if err != nil {
		slog.Error("pipeline: aspect fan-out: evaluate",
			"ruleId", p.ruleID, "entityId", key, "stage", "traversal", "err", err)
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}
	if err := p.applySecureDecrypt(ctx, results); err != nil {
		return p.dispositionEvalErr(ctx, msg, key, "traversal", err, enumeratedActors)
	}

	return p.writeResults(ctx, msg, key, results, enumeratedActors)
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
func (p *Pipeline) writeResults(ctx context.Context, msg substrate.Message, key string, results []ruleengine.EvalResult, enumeratedActors []string) (substrate.Decision, error) {
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
			_, err := p.Reproject(rctx, actorKey)
			return err
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
