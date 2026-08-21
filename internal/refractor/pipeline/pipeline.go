package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// maxNarrowedFilterLabels and maxNarrowedFilterSubjects are the narrowed-filter
// budgets this package derives a consumer filter against. Both are aliases of
// the single definitions in internal/refractor/subjects, which owns them
// because the label cap has a second, independent reader: internal/pkgmgr
// refuses at install time a lens whose worst-case expanded label count would
// cross it (dynamic-type-taxonomy-design.md §10.2). Aliased rather than
// referenced inline so every use site in this file reads as the local budget it
// has always been, while there remains exactly one number.
const (
	maxNarrowedFilterLabels   = subjects.MaxNarrowedFilterLabels
	maxNarrowedFilterSubjects = subjects.MaxNarrowedFilterSubjects
)

// rebuildReopenWait bounds how long a rebuild waits for the pump to re-open
// against the durable it just recreated (ConsumerSupervisor.ResetAwaitReopen).
//
// A rebuild runs inside a bounded rebuild slot, and the point of that bound is
// that the NATS server is not asked for many simultaneous durable transitions;
// the reopen is part of the transition, so the slot has to cover it. Ten seconds
// is set against what the wait is actually blocked on: a pump only sees the
// reopen request once its in-flight message returns from the handler, and a
// projection handler that has been running longer than this is not a reopen this
// caller should keep a slot for. It is deliberately far below the handler
// latencies a rebuild-completion watch tolerates — this bounds the HANDOVER, not
// the rescan, and the rescan has its own watcher. Expiry is not a failure: the
// durable is recreated either way and the pump reopens on its own backoff.
const rebuildReopenWait = 10 * time.Second

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

	// plainNarrowingBlocked is why plainReprojectAll is set — one of health's
	// FilterBroadReason values, "" when the label set is exhaustive. Purely
	// observational: it is reported on the lens's health entry and read by no
	// gate, so it can never change which events this lens is delivered. See
	// ruleState.narrowingBlocked for its lifetime.
	plainNarrowingBlocked string

	// ruleMu guards every field useFullEngineBranches writes — engineKind,
	// fullEngine, fullCR, fullCRBranches, fullCRWalkOwnedColumns, the two
	// plainReproject pairs above, and seedAnchorLabels. Those are the only
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

	// seedAnchorLabels is the set of vertex types an event may seed this
	// lens's evaluation with — a singleton {AnchorLabel} for a bare anchor
	// pattern, or the taxonomy-resolved downward closure for a `*`-suffixed
	// one (dynamic-type-taxonomy-design.md §5.1 site 4), derived once per
	// engine install alongside plainReprojectLabels. An event whose type is a
	// member of this set is a mutation of the anchor itself, so the
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
	seedAnchorLabels map[string]struct{}

	// anchorHops is the compiled pattern GRAPH the affected-anchor derivation
	// walks under (auth-plane-projection-latency-design.md §4.7). Guarded by
	// ruleMu and republished on every rule swap alongside seedAnchorLabels, so
	// a hot-reload can never leave a previous rule body's graph armed. Its zero
	// value is Complete == false, which is the fail-closed answer: fall back to
	// the enumerator's BFS.
	anchorHops full.HopIndex

	// rootHops is the plain arm's OWN pattern graph — Increment 1's
	// ScanRootHopIndex, terminated at the anchor PATTERN rather than at
	// `{key: $actorKey}` (plain-lens-neighbour-anchor-derivation-design.md
	// §4.1/§10). Guarded by ruleMu and republished UNCONDITIONALLY on every
	// rule swap alongside anchorHops — never carried forward, mirroring the
	// branches/walkOwnedColumns pair's own "unconditional, not just the
	// len>1 arm" rule (pipeline.go's useFullEngineBranches, near the top).
	// Its zero value is Complete == false, the same fail-closed answer
	// anchorHops's zero value is: fall back to today's unseeded evaluation.
	rootHops full.HopIndex

	// declaresActorAnchor is whether the PUBLISHED rule's cypher pins a pattern
	// position with `{key: $actorKey}` — see the ruleState field of the same
	// name for what reads it and why. Guarded by ruleMu and republished on every
	// rule swap alongside anchorHops. Its zero value is false, which reads as
	// "declares nothing", the correct answer for a pipeline no rule has ever
	// been published on: ConsumerFilter's plain arm already refuses such a
	// pipeline on engineKind.
	declaresActorAnchor bool

	// labelExpansion is the taxonomy expansion the PUBLISHED rule state
	// matches against — the very map threaded into full.WithLabelExpansion,
	// HopIndex.WithLabelExpansion and seedAnchorLabels by the publication that
	// installed it, nil for a lens with no `*` anywhere. Guarded by ruleMu and
	// republished with the rule it describes, so it can never name a set the
	// running matcher is not actually using.
	//
	// It is also this pipeline's LAST KNOWN GOOD expansion, which is what a
	// live re-derivation reads when the resolver has stopped being able to
	// answer (useFullEngineBranches' StatusUnknown arm). Its lifetime is the
	// published rule state's, exactly: born at the activation that first
	// resolved an expansion, replaced wholesale by every later publication,
	// and gone when the process is. It is deliberately NOT persisted across a
	// restart — a fresh process re-activates through UseFullEngineBranches,
	// which refuses a lens whose expansion is unknown rather than running it
	// against a set no live resolver vouches for.
	labelExpansion map[string]map[string]struct{}

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

	// plainDerivedAnchorCapOverride bounds the number of anchor vertices the
	// plain arm's derivation (anchor_derivation_plain.go) may return before
	// it falls back to today's unseeded evaluation. Zero means
	// DefaultPlainDerivedAnchorCap (or the package override, if set). Its
	// unit is derived ROOT VERTICES, never projected rows — see
	// DefaultPlainDerivedAnchorCap's own doc.
	plainDerivedAnchorCapOverride atomic.Int64

	// peerAnchorMode is this pipeline's override of whether an event on a vertex
	// of the actor type may reach anchors other than that vertex. Zero is
	// PeerAnchorModeUnset, i.e. take the package default (actor_enumerator.go).
	peerAnchorMode atomic.Int64

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

	// taxonomyResolver expands a `*`-suffixed label pattern into its
	// taxonomy-declared downward closure (dynamic-type-taxonomy-design.md
	// §4). Installed once before Run via SetTaxonomyResolver, like
	// secureDecryptor and sweeper above — useFullEngineBranches reads it on
	// every derivation, including a MATCH hot-reload, but nothing ever
	// writes it after activation. Nil behaves exactly like a Resolver with
	// no snapshot loaded (taxonomy.StatusUnknown): a lens with no `*`
	// pattern never consults it at all (§14 Fire A item 3's inertness
	// guarantee), so a pipeline that never calls SetTaxonomyResolver is
	// unaffected unless a compiled rule carries the sigil, in which case it
	// correctly refuses activation.
	taxonomyResolver *taxonomy.Resolver

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
	// (projection.IsAuthPlane). It has two readers, and both treat it as the
	// lens's PLANE rather than as a lens kind:
	//
	//   - footprint validation (needsFootprintValidation, executeFullForActor,
	//     refractor-evaluation-consistency-design.md §13.3), combined with
	//     actorAggregate (envelopeFn or multiEnvelopeFn installed) and
	//     requiresFootprintValidation — only a lens matching all three pays the
	//     re-read cost;
	//   - the plain arm's narrowing licence (plainDerivationLicence), which
	//     refuses the plane outright, so a plain auth-plane lens keeps today's
	//     whole-corpus reprojection.
	//
	// False by default — a lens installed without SetAuthPlane gets no
	// validation. The activation path sets it for EVERY lens, not only through
	// the actor-aggregate installer, because a plain-kind lens declaring
	// nats_kv into the capability bucket is exactly the shape the licence must
	// refuse and is not actor-aggregate.
	//
	// Marking those lenses does not widen footprint validation, and the
	// conjunct that keeps them out is requiresFootprintValidation below, whose
	// only setter is projection.InstallActorAggregate — false by construction
	// for every lens that installer never runs for. The envelope conjunct is
	// NOT what does that work: the operation-role-index lens targets the
	// capability bucket (so it is auth-plane) and installs an envelope through
	// its own activation branch, never reaching that installer. Giving any
	// other path a way to set requiresFootprintValidation arms validation for
	// that family, so the two move together.
	authPlane bool

	// grantSink receives the actor behind every D1 read-grant liveness
	// transition this lens's writes make, and grantAnchorFromKey is the
	// lens's own target-key → anchor-vertex-key inversion used to name that
	// actor. Both nil (the default) means this lens carries no grant-change
	// edge — the fail-slow posture SetGrantChangeSink documents. They move
	// together and are only ever set at construction time.
	grantSink          GrantChangeSink
	grantAnchorFromKey func(targetKey string) (string, bool)

	// personalPublishLocks holds one publish slot per actor currently being
	// reprojected on this lens, and personalPublishMu guards the map itself.
	// Slots are created on demand and dropped the moment nobody holds or wants
	// one, so the map is bounded by concurrent reprojections rather than by the
	// identity population. Nothing survives a restart, and nothing needs to:
	// the lock orders publishers within one process, which is the only place
	// they exist. See lockPersonalActor for what it orders and why.
	personalPublishMu    sync.Mutex
	personalPublishLocks map[string]*actorPublishLock

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

	// peakRowsBuf captures the peak binding rows each full-engine evaluation
	// materialized, so the lag poller can publish the lens's peakBindingRows
	// gauge. New installs one on every pipeline — the gauge answers "why was
	// this evaluation refused", which no lens is exempt from — but a directly
	// constructed Pipeline leaves it nil and simply records nothing.
	peakRowsBuf *PeakRowsRingBuffer
	adapterMu   sync.RWMutex    // protects adpt for concurrent hot-reload
	adpt        adapter.Adapter // access via currentAdapter(); swap via HotReloadInto

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

	// auditor is the plain-lens divergence audit, installed via InstallAudit
	// and driven by RunAudit. Nil until InstallAudit has run; non-nil and
	// carrying Enrolled=false for a lens the enrolment conjuncts REFUSED, so
	// the refusal is a published verdict rather than an absence.
	auditor *Auditor

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
		peakRowsBuf:         NewPeakRowsRingBuffer(DefaultPeakRowsBufferSize),
	}
	p.adpt = adpt
	return p, nil
}

// UseFullEngine switches this pipeline's evaluate path to the full
// openCypher engine. cr must be the *full.CompiledRule that lens.Parse /
// corekv_source produced for this rule. Must be called before Run.
//
// Returns an error — and leaves the pipeline's previously published rule
// state untouched — when a `*` label pattern's taxonomy expansion cannot be
// trusted at all (taxonomy.StatusUnknown: no resolver installed, no
// snapshot loaded, an unresolvable label, or a cycle/depth fault). See
// useFullEngineBranches.
func (p *Pipeline) UseFullEngine(eng *full.Engine, cr ruleengine.CompiledRule) error {
	return p.useFullEngineBranches(eng, cr, nil, false)
}

// UseFullEngineBranches is UseFullEngine's multi-walk sibling
// (refractor-shared-keyspace-arbitration-design.md §13.2): branches carries
// a Personal lens's N independently-compiled query branches (lens.Rule.
// CompiledBranches), cr must be branches[0]. Nil/single-element branches
// behaves exactly like UseFullEngine. Must be called before Run.
func (p *Pipeline) UseFullEngineBranches(eng *full.Engine, cr ruleengine.CompiledRule, branches []ruleengine.CompiledRule) error {
	return p.useFullEngineBranches(eng, cr, branches, false)
}

// narrowingBlockRank orders the causes that can clear a rule's exhaustiveness
// by how much SURVIVES fixing the others — lowest rank reported. It is a table
// rather than call order because one derivation can trip several sites and the
// site order is not the actionability order: the unarmed-resolver site runs
// before the zero-concrete-leaves one, so a positional rule would report a
// cause that clears on its own for a rule that also carries one that never
// does.
//
//	0  non-exhaustive        the cypher itself can bind a type no label names,
//	                         or a `*` resolved to no concrete type at all.
//	                         Survives BOTH arming the resolver and repairing
//	                         the taxonomy, so it is always the true verdict.
//	1  taxonomy-unresolvable the taxonomy cannot answer at all. Needs a package
//	                         fix; waiting never clears it.
//	2  taxonomy-unarmed      the answer is known but not guaranteed current.
//	                         The one cause that clears with no edit anywhere,
//	                         so it is reported only when it is the ONLY thing
//	                         blocking narrowing.
var narrowingBlockRank = map[string]int{
	health.FilterBroadReasonNonExhaustive:        0,
	health.FilterBroadReasonTaxonomyUnresolvable: 1,
	health.FilterBroadReasonTaxonomyUnarmed:      2,
}

// narrowingBlockRankOf ranks an UNREGISTERED reason last rather than first.
// Reading the map directly would give one the zero value, which silently
// outranks every real cause — precisely the failure a written-down precedence
// exists to remove, and precisely what a new site added without a table row
// would hit.
func narrowingBlockRankOf(reason string) int {
	if rank, ok := narrowingBlockRank[reason]; ok {
		return rank
	}
	return len(narrowingBlockRank)
}

// UseFullEngineBranchesForReDerivation is UseFullEngineBranches' LIVE
// re-derivation sibling (dynamic-type-taxonomy-design.md §14 Fire A item 4).
// Byte-identical to UseFullEngineBranches except for the taxonomy.
// StatusUnknown branch, where the two entry points are governed by different
// sections of the design: an ACTIVATION (UseFullEngineBranches) refuses
// outright per §4.2 — nothing published, stay dark, which is safe because
// the pipeline was never running against this rule. A LIVE pipeline
// re-deriving cannot take that same refusal without violating §6.5's "never
// keep a stale narrow set": it is already projecting against a set the
// resolver has just proven it cannot currently trust, so this entry point
// degrades to the broad filter (the existing exhaustive=false /
// reprojectAll=true machinery every other non-StatusArmed answer already goes
// through) and PUBLISHES that, instead of refusing.
//
// The degradation is on the DELIVERY axis alone. The matcher keeps this
// pipeline's last known good expansion (Pipeline.labelExpansion), because a
// broad filter over a blank matcher is not "slower", it is zero rows — and a
// Rebuild against zero rows retracts every row the lens has ever written.
// Correct-but-slower means the row set stays right and only the narrowing is
// lost. The one state that cannot degrade is a rule with a label the carried
// expansion does not cover, which refuses like activation does and which no
// RUNNING `*` lens can be in (see the StatusUnknown arm's own comment). Never wrong, only un-narrowed, until the next successful
// re-derivation — the caller (reloader.rederiveEntry) still drives the
// Rebuild that re-registers the widened server-side filter; this call only
// updates the client gate.
func (p *Pipeline) UseFullEngineBranchesForReDerivation(eng *full.Engine, cr ruleengine.CompiledRule, branches []ruleengine.CompiledRule) error {
	return p.useFullEngineBranches(eng, cr, branches, true)
}

func (p *Pipeline) useFullEngineBranches(eng *full.Engine, cr ruleengine.CompiledRule, branches []ruleengine.CompiledRule, liveReDerivation bool) error {
	// Everything is derived into locals first and published under one Lock at
	// the end (see ruleMu): a reader must never observe a half-rewritten rule.
	// Nothing is published at all if this function returns an error part way
	// through — the taxonomy-expansion refusal below is an activation
	// failure, not a rule swap, so the pipeline keeps evaluating whatever
	// rule (if any) it already had.
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
	// Why this rule's label set ends up non-exhaustive, in the health entry's
	// own vocabulary — carried onto the published ruleState so ConsumerFilter
	// can REPORT the cause it already acts on, rather than re-deriving it from
	// a snapshot that no longer knows which site fired. Paired with every
	// `exhaustive = false` below and set nowhere else, which is what makes
	// `narrowingBlocked != ""` and `!exhaustive` the same condition.
	//
	// The highest-RANKED cause wins (narrowingBlockRank), not the first one
	// written: the sites do not fire in actionability order, so positional
	// precedence would report a transient cause for a rule that also carries a
	// permanent one and leave an operator waiting for an arming that changes
	// nothing.
	narrowingBlocked := ""
	blockNarrowing := func(reason string) {
		if narrowingBlocked == "" || narrowingBlockRankOf(reason) < narrowingBlockRankOf(narrowingBlocked) {
			narrowingBlocked = reason
		}
	}
	expansionNeeded := map[string]struct{}{}
	for _, c := range all {
		fullCR, isFull := c.(*full.CompiledRule)
		if !isFull {
			exhaustive = false
			blockNarrowing(health.FilterBroadReasonNonExhaustive)
			relationsExhaustive = false
			// continue, not break: a LATER branch may still be a
			// *full.CompiledRule carrying `*`, and expansionNeeded below
			// must see it regardless of exhaustive already being lost —
			// otherwise an unresolvable label on that branch would be
			// silently dropped instead of refusing activation loudly
			// through the same path every other `*` branch goes through.
			continue
		}
		// The lens's declared projection kind, collected in the same lockstep
		// walk as the label and relation sets so the three come from one
		// traversal of one rule and cannot disagree about it. One branch
		// declaring the anchor makes the lens actor-anchored.
		if fullCR.DeclaresActorAnchor() {
			next.declaresActorAnchor = true
		}
		ls, ok := fullCR.ReferencedLabels()
		if !ok {
			exhaustive = false
			blockNarrowing(health.FilterBroadReasonNonExhaustive)
		} else {
			for l := range ls {
				labels[l] = struct{}{}
			}
		}
		for l := range fullCR.ExpansionLabels() {
			expansionNeeded[l] = struct{}{}
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

	// Taxonomy expansion (dynamic-type-taxonomy-design.md §4). Consulted only
	// when at least one pattern in the query carries the `*` sigil: a
	// sigil-free query's labels are derived from ReferencedLabels() alone,
	// above, and the resolver is never called for it (§14 Fire A item 3's
	// inertness guarantee).
	var expandedLabels map[string]map[string]struct{}
	if len(expansionNeeded) > 0 {
		status := taxonomy.StatusUnknown
		reason := "no taxonomy resolver is installed on this pipeline"
		var inert map[string]struct{}
		if p.taxonomyResolver != nil {
			expandedLabels, inert, status, reason = p.taxonomyResolver.Expand(expansionNeeded)
		}
		if status == taxonomy.StatusUnknown {
			if !liveReDerivation {
				// §4.2's two-tier fork: a set-KNOWN-but-possibly-stale answer
				// may still activate broad (below); a set UNKNOWN answer must
				// never activate at all, because no filter width rescues a
				// MATCHER evaluating against a wrong expansion. Nothing has
				// been published — the pipeline's previous rule
				// state (if any) is untouched.
				return fmt.Errorf(
					"pipeline: taxonomy expansion unknown for label(s) %s — %s; refusing activation rather than risk projecting the wrong row set",
					sortedLabelList(expansionNeeded), reason)
			}
			// A live re-derivation is governed by §6.5, not §4.2 (see
			// UseFullEngineBranchesForReDerivation's doc): degrade rather
			// than refuse — but degrade on the DELIVERY axis only. Expand
			// answered nil (its own StatusUnknown contract), and publishing
			// that nil would carry the degradation onto the PROJECTION axis
			// too: executor.go's nodeMatches finds no entry for the `*`
			// label and binds nothing, so the lens matches zero rows, and
			// the Rebuild the caller drives next replays every anchor
			// against that blank matcher — on a lens with filter/diff/
			// presence retraction, a mass Delete; on a grant lens, a mass
			// revoke. §6.5 promises a broad FILTER, which costs delivered-
			// then-skipped events; it never promises an empty matcher.
			//
			// So the matcher keeps this pipeline's last known good
			// expansion (carriedLabelExpansion — the set the currently
			// published rule state is already matching against) while
			// exhaustive=false below takes the filter broad and reports
			// taxonomy-unresolvable. Correct-but-slower, which is what §6.5
			// asks for: the rows stay right, only the narrowing is lost,
			// until a later re-derivation finds a trustworthy answer.
			//
			// With nothing carried there is nothing to degrade TO, and this
			// arm refuses like activation does — the asymmetry between the
			// two entry points is "activation refuses; a LIVE lens degrades
			// and keeps serving", and a lens with no published expansion is
			// not a live one: UseFullEngineBranches refuses any `*` lens
			// whose expansion is unknown, so every running `*` lens reached
			// its RunOn with an expansion published. This is the belt to
			// that brace, and it fails the way the brace does.
			//
			// What makes the carry safe is COVERAGE, not mere presence: every
			// label this rule expands must have an entry, because
			// full.WithLabelExpansion threads one map into all of them and
			// executor.go's nodeMatches binds nothing for a `*` label the map
			// does not mention. A map covering some labels and not others
			// would publish a partly-blank matcher, which is the same zero-row
			// Rebuild this arm exists to prevent, reached by a narrower door.
			// Tested here against expansionNeeded rather than argued from what
			// the caller's rule can be: the guard has to hold for the rule in
			// hand, not for the ordering of the reload that produced it.
			expandedLabels = p.carriedLabelExpansion()
			if missing := labelsWithoutExpansion(expansionNeeded, expandedLabels); len(missing) > 0 {
				return fmt.Errorf(
					"pipeline: live taxonomy re-derivation found the expansion unknown for label(s) %s — %s; this pipeline has no previously-resolved expansion for label(s) %s to keep matching against, so it refuses rather than publish a matcher that binds nothing for them",
					sortedLabelList(expansionNeeded), reason, sortedLabelList(missing))
			}
			slog.Warn("pipeline: live taxonomy re-derivation found the expansion unknown — degrading to the broad filter and keeping the last resolved expansion for matching, rather than keeping a stale narrow set or blanking the matcher",
				"ruleId", p.ruleID, "labels", sortedLabelList(expansionNeeded), "reason", reason)
		} else if !liveReDerivation && len(inert) > 0 {
			// ACTIVATION-only (taxonomy.Resolver.Expand's doc has the full
			// split): a `*` whose resolved closure is exactly {itself}
			// asserts a polymorphism the taxonomy does not currently
			// declare — refused here as an authoring mistake, never a
			// silent no-op. A LIVE re-derivation must NOT take this
			// branch: a concrete type's LAST subtypeOf child can be
			// uninstalled by a DIFFERENT package while this lens is
			// running and correct, and {itself} is the truthful, merely
			// un-widened answer for it right now (§6.5) — refusing here
			// would take the lens's own still-resolvable instances dark
			// along with the widening it lost. liveReDerivation's branch
			// below (via exhaustive/expandedLabels) accepts inert answers
			// exactly like any other resolved label.
			return fmt.Errorf(
				"pipeline: taxonomy expansion for label(s) %s resolves to exactly itself — the `*` sigil asserts a polymorphism the taxonomy does not currently declare; refusing activation rather than accept a no-op sigil",
				sortedLabelList(inert))
		}
		if status != taxonomy.StatusArmed {
			// Known but not guaranteed current: correct-but-slower, never
			// wrong-but-fast. Forces the broad filter even though every
			// referenced label above resolved exhaustively.
			exhaustive = false
			// Two very different states reach here, and only one of them is
			// waiting for something. StatusStale is a loaded snapshot with a
			// dead invalidation consumer — it clears the moment the resolver
			// arms, with no edit anywhere. StatusUnknown is the resolver
			// unable to answer at all (a cycle, an over-depth chain, an
			// ambiguous canonicalName, a vanished abstract, no snapshot ever
			// loaded), reachable here only on a LIVE re-derivation because
			// activation refuses outright above — and it never clears until a
			// package is fixed. Reporting them under one word tells an
			// operator to wait out a state that will not end.
			if status == taxonomy.StatusUnknown {
				blockNarrowing(health.FilterBroadReasonTaxonomyUnresolvable)
			} else {
				blockNarrowing(health.FilterBroadReasonTaxonomyUnarmed)
			}
		}
		// An abstract label with no concrete descendants (or whose
		// descendants are all themselves abstract) resolves to a KNOWN, empty
		// set — Expand reports ok/StatusArmed for it, not StatusUnknown, per
		// §3.4's expanded-set row: "genuinely zero leaves" is a real answer,
		// not a resolver fault. But publishing exhaustive=true on it would
		// make reprojectLabels lose that label's contribution entirely
		// (nothing is unioned in below) while every OTHER gate keyed off
		// reprojectLabels — plainVertexRelevant chief among them, whose false
		// branch acks-and-drops with no fallback — reads the narrowed set as
		// authoritative. That is the "stale narrow set" §6.5 calls the only
		// unacceptable state: the lens goes silently dark on that type while
		// presenting as narrowed and health-green. (Actor-aware lenses do not
		// share this hazard — an unseeded fan-out re-executes rather than
		// relying on the plain reprojection gate — so this is plain-lens
		// specific.) Forces the broad filter instead, exactly like a
		// not-yet-armed resolver.
		// Judged on every path, a carried expansion included: a label whose
		// concrete set is empty is non-exhaustive DURABLY — arming the
		// resolver and repairing the taxonomy both leave it so — and rank 0 is
		// the true verdict for exactly that reason (narrowingBlockRank). A
		// degrade that reported taxonomy-unresolvable over it would send an
		// operator to fix a taxonomy that, once fixed, leaves the lens broad
		// anyway.
		for l, set := range expandedLabels {
			if len(set) == 0 {
				exhaustive = false
				blockNarrowing(health.FilterBroadReasonNonExhaustive)
				slog.Warn("pipeline: taxonomy label resolved to zero concrete types — degrading to the broad filter",
					"ruleId", p.ruleID, "label", l)
			}
		}
		// Every LabelExpand label's own raw string was already added to
		// labels by ReferencedLabels() above (it collects the AST label text
		// unconditionally, blind to the `*` sigil). That string must not
		// survive into reprojectLabels on its own: for an ABSTRACT label it
		// names no instance at all (§3.4's expanded-set row — including it
		// would add a filter subject that can never match, and would let
		// plainVertexRelevant admit a type that cannot exist), and for a
		// CONCRETE label the resolved set already contains it via Expand's
		// reflexivity. So each expanded label is deleted and replaced
		// wholesale by its resolved concrete member set.
		for l := range expandedLabels {
			delete(labels, l)
		}
		for _, set := range expandedLabels {
			for vt := range set {
				labels[vt] = struct{}{}
			}
		}
	}

	if exhaustive {
		next.reprojectLabels = labels
		next.reprojectAll = false
	}
	// Published unconditionally, so the reason is derived fresh from THIS rule
	// alongside the label set it explains and nothing survives a swap: "" here
	// is exactly the exhaustive case, since every site that clears exhaustive
	// sets it.
	next.narrowingBlocked = narrowingBlocked
	if relationsExhaustive {
		next.reprojectRelations = relations
		next.relationsExhaustive = true
	}

	// Publish the taxonomy-resolved compiled rule(s) — a copy carrying
	// LabelExpansion (full.WithLabelExpansion, §4.3), never a mutation of the
	// caller's cr/branches — but only when expansion actually ran; a lens
	// with no `*` keeps the exact rule object it was given, preserving
	// identity as well as behaviour.
	if len(expansionNeeded) > 0 {
		// Recorded on the same snapshot the wrapped rules ride, so the
		// pipeline's last-known-good is by construction the set its matcher is
		// using — the two cannot drift, because one publication writes both.
		next.labelExpansion = expandedLabels
		wrapped := make([]ruleengine.CompiledRule, len(all))
		for i, c := range all {
			if fullCR, isFull := c.(*full.CompiledRule); isFull {
				wrapped[i] = full.WithLabelExpansion(fullCR, expandedLabels)
			} else {
				wrapped[i] = c
			}
		}
		next.cr = wrapped[0]
		if len(branches) > 1 {
			next.branches = wrapped
		}
	}

	// Pin the anchor label(s) an anchor-labeled event can seed the evaluation
	// with. Derived unconditionally like the label set above, and for the same
	// reason: a reload must never leave a previous rule body's anchor armed. A
	// multi-walk lens is excluded outright — branch merging evaluates N
	// independent queries, each with its own anchor, and one seed cannot speak
	// for all of them.
	if len(branches) <= 1 {
		if fullCR, isFull := next.cr.(*full.CompiledRule); isFull {
			if label, ok := fullCR.AnchorLabel(); ok {
				if fullCR.AnchorLabelExpand() {
					if set, ok := expandedLabels[label]; ok {
						next.seedAnchorLabels = set
					}
				} else {
					next.seedAnchorLabels = map[string]struct{}{label: {}}
				}
			}
			// The pattern graph the affected-anchor derivation walks under
			// (auth-plane-projection-latency-design.md §4.7). Derived here for
			// the same two reasons the anchor label is, and excluded on the
			// same multi-walk arm: each branch carries its own anchor, and one
			// graph cannot speak for all of them. WithLabelExpansion threads
			// the SAME resolved sets into every `*` position the graph
			// carries — PositionsBinding, AnchorSideSeeds and the walk's
			// far-end prune all read them (dynamic-type-taxonomy-design.md
			// §5.1's HopIndex-shaped sixth mechanism) — mirroring
			// full.WithLabelExpansion exactly: a no-op when expandedLabels is
			// nil (no `*` anywhere in this query).
			next.anchorHops = fullCR.AnchorHopIndex().WithLabelExpansion(expandedLabels)
			// The plain arm's own terminus (plain-lens-neighbour-anchor-
			// derivation-design.md §4.1/§10) — built alongside anchorHops, on
			// the SAME multi-walk exclusion and the SAME label-expansion
			// threading, so a hot reload can never leave one of the pair
			// stale against the other. Unconditional within this branch: a
			// plain lens (AnchorLabel not ok) still gets a rootHops, since
			// ScanRootHopIndex's terminus is the anchor PATTERN, never
			// `{key: $actorKey}` — the two termini are independent questions
			// answered by the same builder.
			next.rootHops = fullCR.ScanRootHopIndex().WithLabelExpansion(expandedLabels)
		}
	}
	p.publishRuleState(next)
	return nil
}

// sortedLabelList returns labels' keys sorted, for a deterministic error
// message.
func sortedLabelList(labels map[string]struct{}) []string {
	out := make([]string, 0, len(labels))
	for l := range labels {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// labelsWithoutExpansion returns the labels in needed that exp has no entry
// for — the coverage test the live-re-derivation carry-forward turns on.
//
// A PRESENT but empty set is covered: an abstract with no concrete
// descendants is a real, resolved answer (§3.4), and the pattern binding
// nothing is then the truth rather than a blank matcher. Only a MISSING key
// is the un-carryable case, because that is the one executor.go's nodeMatches
// cannot tell apart from "never resolved".
func labelsWithoutExpansion(needed map[string]struct{}, exp map[string]map[string]struct{}) map[string]struct{} {
	missing := map[string]struct{}{}
	for l := range needed {
		if _, ok := exp[l]; !ok {
			missing[l] = struct{}{}
		}
	}
	return missing
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
	seedAnchorLabels    map[string]struct{}
	anchorHops          full.HopIndex
	// rootHops is the plain arm's own scan-root pattern graph — see the
	// Pipeline field of the same name, which this publishes into, and §10 of
	// plain-lens-neighbour-anchor-derivation-design.md for its lifetime.
	rootHops full.HopIndex
	// declaresActorAnchor is true when this rule's cypher pins a pattern
	// position with `{key: $actorKey}` — the lens's DECLARED projection kind,
	// as against p.actorEnumerator, which is what the host has INSTALLED so
	// far. ConsumerFilter compares the two: declared actor-anchored with no
	// enumerator is an install that has not finished, and the only input in
	// that derivation whose absence is otherwise indistinguishable from a
	// genuinely plain lens.
	//
	// Derived over every branch, not only the single-walk arm anchorHops takes:
	// a multi-walk Personal lens's branches each carry their own anchor, and
	// one branch declaring it is enough to make the lens actor-anchored. It
	// rides the rule snapshot for the same reason narrowingBlocked does — it is
	// a property OF this compiled rule, published atomically with it, so a
	// reload cannot leave a previous rule body's declaration standing.
	declaresActorAnchor bool
	// labelExpansion is the taxonomy expansion this rule state's matcher,
	// anchor graph and seed set were all built from — see the Pipeline field
	// of the same name, which this publishes into.
	labelExpansion map[string]map[string]struct{}
	// narrowingBlocked is why reprojectAll is set — one of health's
	// FilterBroadReason values, "" when the label set IS exhaustive. It rides
	// the rule snapshot rather than living in its own state because the cause
	// is a property OF this compiled rule and of nothing else: it is derived
	// where the decision is made (useFullEngineBranches) and read where the
	// decision is reported (ConsumerFilter), with a rule swap replacing it
	// wholesale under the same atomic publication as the label set it explains.
	// A separate latch would be a second thing to keep in sync with the very
	// value it describes.
	//
	// A never-compiled pipeline (no engine wired, or a non-full one — nothing
	// but useFullEngineBranches ever publishes a rule) carries the zero value
	// "", which ConsumerFilter reports as not-eligible rather than as a missing
	// reason.
	narrowingBlocked string
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
		seedAnchorLabels:    p.seedAnchorLabels,
		anchorHops:          p.anchorHops,
		rootHops:            p.rootHops,
		declaresActorAnchor: p.declaresActorAnchor,
		labelExpansion:      p.labelExpansion,
		narrowingBlocked:    p.plainNarrowingBlocked,
	}
}

// carriedLabelExpansion returns the expansion the currently published rule
// state matches against — this pipeline's last known good answer, nil when no
// publication has ever resolved one.
//
// It is read exactly once, by useFullEngineBranches' live-re-derivation
// StatusUnknown arm, BEFORE that call publishes anything: keeping the matcher
// on the set it is already using is what makes an unresolvable taxonomy a
// delivery widening rather than a projection blackout. That arm checks the
// map COVERS every label the rule in hand expands (labelsWithoutExpansion)
// before using it — this returns whatever was last published, which is not by
// itself a promise about any particular rule's labels.
func (p *Pipeline) carriedLabelExpansion() map[string]map[string]struct{} {
	p.ruleMu.RLock()
	defer p.ruleMu.RUnlock()
	return p.labelExpansion
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
	p.seedAnchorLabels = rs.seedAnchorLabels
	p.anchorHops = rs.anchorHops
	p.rootHops = rs.rootHops
	p.declaresActorAnchor = rs.declaresActorAnchor
	p.labelExpansion = rs.labelExpansion
	p.plainNarrowingBlocked = rs.narrowingBlocked
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
//   - eventLabel is a member of seedAnchorLabels — only a mutation of the
//     anchor ITSELF (or, for a `*`-suffixed anchor, one of its
//     taxonomy-resolved concrete subtypes) bounds the change to one anchor.
//     A neighbor (referenced non-anchor type) event can affect any number of
//     anchors through the walk, and deriving which ones is §D2 Phase 2; it
//     keeps the full recompute.
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
	if eventKey == "" {
		return ""
	}
	if _, ok := rs.seedAnchorLabels[eventLabel]; !ok {
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

// linkRelationReactsTo reports whether a link event on the given relation can
// affect this lens at all. It is the RELATION half of a link key's judgment,
// and every link arm pairs it with an ENDPOINT-TYPE half: the plain arm's
// plainReactsTo on each endpoint (evalPlainLinkReprojection), the actor-aware
// arm's §4.2 label set (actorAwareLinkRelevant). That half asks whether either
// endpoint type can bind, this one whether the relation between them is one the
// lens's patterns actually traverse. A link satisfying only the endpoint test —
// `lnk.service.<id>.providedTo.identity.<id>` reaching a lens whose sole
// relationship pattern is `(pr)-[:identifiedBy]->(id:identity)` — cannot appear
// in any traversal, and re-executing for it is pure cost.
//
// ONE predicate for both arms is what entitles ConsumerFilter to pin the
// relation segment of a narrowed filter's link subjects: the server then
// withholds exactly the links some arm skips anyway, rather than making a
// second, independently-fallible judgment about them.
//
// The false case lands in the SAME already-sanctioned skip class as the
// endpoint test's: no reprojection, and no adjacency self-apply, whose
// authoritative writer is the dedicated whole-stream adjacency consumer rather
// than any lens pipeline.
//
// Every uncertain case defaults to relevant, exactly as plainReactsTo does: a
// non-full engine, an empty/unparsed relation, or a non-exhaustive relation set
// (an untyped or variable-length relationship anywhere in the lens) all
// reproject.
func (rs ruleState) linkRelationReactsTo(relation string) bool {
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
	return anyTypeBindable(labels, types...)
}

// actorAwareLinkRelevant reports whether the actor-aware link fan-out must run
// for a link event on the given relation between the given endpoint types. It
// is actorAwareFanOutRelevant's link form, carrying the conjunct the aspect and
// vertex arms have no key segment for: a link is relevant when the lens's
// patterns TRAVERSE its relation AND either endpoint type can bind.
//
// The two conjuncts arm together, behind the one §4.2 eligibility answer. That
// is what makes the pair the exact client-side counterpart of a
// relation-narrowed filter's link subjects, which pin a (label, relation) pair
// in each direction: a lens that fails §4.2 keeps the unconditional fan-out on
// both axes and takes the broad filter, and a lens that clears it skips on both
// axes and has its server-side subjects pinned on both.
//
// The relation conjunct is sound for the same reason the endpoint one is, and
// on the same terms: an event that cannot enter any traversal cannot move a row
// that is a function of the pattern-bound subgraph, which is exactly what
// §4.2's patternClosedOutput conjunct asserts and what a Personal lens's
// out-of-pattern read gate denies. The fan-out's idempotent adjacency
// pre-apply is lost with either skip, and that is sound for both: it exists to
// stop THIS pipeline's reprojection racing ahead of its own trigger edge
// (evaluateLinkFanOut), and there is no reprojection left to order.
//
// The enumerator's own breadth is untouched. It walks adjacency
// relation-blind, including the fixed reportsTo hop, and adjacency is written
// by the dedicated whole-stream consumer rather than by this pipeline's
// deliveries — so a skipped link narrows nothing about WHICH anchors a later
// relevant event reaches.
func (p *Pipeline) actorAwareLinkRelevant(rs ruleState, relation, typeA, typeB string) bool {
	labels, ok := p.actorAwareNarrowingLabels(rs)
	if !ok {
		return true
	}
	if !rs.linkRelationReactsTo(relation) {
		return false
	}
	return anyTypeBindable(labels, typeA, typeB)
}

// anyTypeBindable reports whether any of types is one the label set can bind.
// It skips only on a POSITIVE proof that no named type is in the set, so an
// empty list and any type that failed to parse both read as bindable.
func anyTypeBindable(labels map[string]struct{}, types ...string) bool {
	if len(types) == 0 {
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

// linkEventRelevant is the single gate both link arms consult, the KindLink
// counterpart of vertexEventRelevant: an actor-aware lens judges by §4.2's
// conjunction plus the traversed-relation set, a plain lens by plainReactsTo on
// each endpoint plus the same relation set. False means the link cannot appear
// in any of the lens's traversals, so the arm acks and skips it.
func (p *Pipeline) linkEventRelevant(rs ruleState, relation, typeA, typeB string) bool {
	if p.actorEnumerator != nil {
		return p.actorAwareLinkRelevant(rs, relation, typeA, typeB)
	}
	return rs.linkRelationReactsTo(relation) &&
		(rs.plainReactsTo(typeA) || rs.plainReactsTo(typeB))
}

// LinkEventRelevant reports whether this pipeline's link arm would do any work
// for a link CDC event on a Contract #1 lnk.<typeA>.<idA>.<relation>.<typeB>.
// <idB> key — the CLIENT-side half of the decision whose SERVER-side half is the
// link subjects ConsumerFilter derives.
//
// It exists so a census can assert the two halves against each other on the real
// shipped corpus rather than assuming they agree. It answers off the same gate
// the delivery path runs, never a restatement of it: a probe that reimplemented
// the condition would agree with a broken gate.
func (p *Pipeline) LinkEventRelevant(relation, typeA, typeB string) bool {
	return p.linkEventRelevant(p.ruleState(), relation, typeA, typeB)
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

// RebuildTruncateIsScoped reports whether a truncating rebuild on this
// pipeline's CURRENT adapter would clear only the rows this lens wrote.
//
// It is the precondition for asking for a truncate that is not already forced:
// NatsKVAdapter.Truncate with no bound key prefix purges the WHOLE bucket, and
// several shipped lenses share a bucket with other producers (capability-kv
// carries the core cap.<actor> surface alongside per-package producers). For
// those, a truncate is not a rebuild of this lens — it is a wipe of everyone
// else's rows, healed only at sweep pace.
//
// Scoped means one of two things:
//
//   - the adapter carries a key prefix (projection.ApplyTruncateScope bound the
//     lens's own declared output prefix onto it), so the purge is confined to
//     the keys under it; or
//   - the adapter truncates a target it does not share by construction — a
//     Postgres table named by the lens's own INTO, whose one shared instance
//     (actor_read_grants) is served by GrantWriterAdapter, which deliberately
//     implements no Truncater at all and so answers false here.
//
// An adapter that cannot truncate answers false: asking for a truncate it will
// decline would make the caller believe rows were cleared that were not.
func (p *Pipeline) RebuildTruncateIsScoped() bool {
	adpt := p.currentAdapter()
	if _, ok := adpt.(adapter.Truncater); !ok {
		return false
	}
	if prefixed, ok := adpt.(interface{ KeyPrefix() string }); ok {
		return prefixed.KeyPrefix() != ""
	}
	return true
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

// SetTaxonomyResolver installs the taxonomy resolver this pipeline's `*`
// label patterns expand against (dynamic-type-taxonomy-design.md §4). Item
// 4's CDC consumer constructs one Resolver and calls this once per pipeline
// at activation, mirroring SetSweepPlan/SetSecureDecryptor. Must be called
// before Run — like SetSecureDecryptor, not after: useFullEngineBranches
// reads taxonomyResolver unguarded, and a MATCH hot-reload
// (cmd/refractor/reload.go) calls it on a LIVE pipeline from CoreKVSource's
// dispatch goroutine, so a later SetTaxonomyResolver call would race that
// read.
func (p *Pipeline) SetTaxonomyResolver(r *taxonomy.Resolver) {
	p.taxonomyResolver = r
}

// SetAuthPlane records whether this lens projects an authorization surface
// (projection.IsAuthPlane) — the gate on footprint validation and the plain
// arm's narrowing licence alike (see the authPlane field doc). Must be called
// before Run.
func (p *Pipeline) SetAuthPlane(v bool) {
	p.authPlane = v
}

// AuthPlane reports what SetAuthPlane recorded, so a test can pin which
// activation paths declare the lens's plane without reaching into the
// pipeline's internals — the same reasoning PatternClosedOutput documents for
// its own flag.
func (p *Pipeline) AuthPlane() bool { return p.authPlane }

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

// PeakBindingRows reports the largest binding set any of this lens's recent
// evaluations materialized, and whether the window holds any sample at all.
// The false return is "no evaluation to report", which a publisher must not
// write as a zero — health.PeakRowsFunc, the lag poller's source hook.
func (p *Pipeline) PeakBindingRows() (uint64, bool) {
	if p.peakRowsBuf == nil {
		return 0, false
	}
	snap := p.peakRowsBuf.Snapshot()
	if snap.Count == 0 {
		return 0, false
	}
	return uint64(snap.Peak), true
}

// recordPeakBindingRows folds one evaluation's peak into the observation
// window. Called for every engine evaluation, refusals included — a refused
// evaluation's peak IS the diagnosis, so it must land before the pipeline
// disposes of the error.
func (p *Pipeline) recordPeakBindingRows(stats ruleengine.EvalStats) {
	if p.peakRowsBuf == nil {
		return
	}
	p.peakRowsBuf.Record(stats.PeakBindingRows)
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
// Run, supervisor.ResetAwaitReopen for a Rebuild) — and, if it fails while filterSubjects
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
	// The footprint half of the same event, alongside the fault above rather
	// than instead of it: the lens is running on a filter its own derivation
	// did not choose, so the decision ConsumerFilter already reported is now
	// wrong and is overwritten here. This is the one broad reason decided after
	// the derivation, which is why it cannot come from ConsumerFilter — and it
	// covers BOTH registration paths, since Run's initial supervisor.Add and
	// Rebuild's supervisor reset each fall back through this one function.
	p.RecordFilterDecision(ctx, registrationFailedDecision())
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
