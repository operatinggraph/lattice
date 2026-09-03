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

// RebuildPollBackoffCap bounds how far watchRebuildCompletion's poll delay may
// grow while a rebuild's outstanding count holds steady or climbs against
// racing writes: the delay doubles from RebuildPollInterval on such a poll and
// resets to it the instant outstanding strictly decreases, so a rebuild that
// starts draining again is checked promptly right when a completion might be
// near. Exported alongside RebuildPollInterval so a test can lower it too.
var RebuildPollBackoffCap = 5 * time.Second

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

	// envelopeScopeFn (when non-nil) computes per-evaluation state the
	// envelope answers from, so an envelope gating every row on one
	// actor-scoped input reads it once per evaluation rather than once per
	// row. It is inert without an envelope installed, and what it returns is
	// merged into a copy of the evaluation's parameters that only the
	// envelope sees — see EnvelopeScopeFn.
	envelopeScopeFn EnvelopeScopeFn

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

	// anchorHopsPerBranch is the multi-walk arm's pattern graphs — one per
	// compiled branch — and anchorHopsPerBranchRefusal the conjunct that refused
	// the set whole (personal-lens-derivation-licence-design.md §4.5). See the
	// ruleState fields of the same names, which these publish into. Guarded by
	// ruleMu and republished on every rule swap alongside anchorHops, so a lens
	// reloaded from three walks down to one cannot leave the previous body's
	// graphs armed. Their zero values are nil and "", which are fail-closed only
	// READ AS A PAIR — "no graphs" alone would be a union over no walks.
	anchorHopsPerBranch        []full.HopIndex
	anchorHopsPerBranchRefusal string

	// walkScope bounds which relations the ActorEnumerator's BFS follows at
	// each vertex type (refractor-hub-walk-and-periodic-load-design.md §5.1),
	// and walkScopeRefusal names the conjunct that left it nil. Guarded by
	// ruleMu and republished on every rule swap alongside anchorHops. Its zero
	// value, nil, is the fail-closed answer: the relation-blind walk.
	//
	// Unlike anchorHops it is derived over EVERY branch, because one enumerator
	// serves the whole lens rather than one walk.
	walkScope        *walkScope
	walkScopeRefusal string

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

	// walkScopeMode is this pipeline's override of whether the actor walk runs
	// pattern-scoped at all. Zero is WalkScopeModeUnset, i.e. take the package
	// default (walkscope.go).
	walkScopeMode atomic.Int64

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

	// personalPlaneHealer records that this pipeline is registered with the
	// PERSONAL plane's standing healer — grantchange.PersonalSweeper plus the
	// D1 grant-change edge. It is the personal counterpart of sweeper: a
	// Personal Lens never receives a SweepPlan, so sweeper is nil for one, and
	// without this flag a lens that really is healed would read as unhealed.
	//
	// Set by the host at the registration site itself (cmd/refractor's
	// grantReprojector.RegisterPersonal call), never inferred from the envelope
	// or descriptor shape: registration is what makes the healer real, and a
	// deployment that installs personal lenses without the reprojector must
	// read as unhealed. Its zero value, false, is that fail-closed answer.
	//
	// Atomic because a host may register after Run has started, and every read
	// of it is on the per-event path.
	personalPlaneHealer atomic.Bool

	// personalClockRefusal is the published half of the personal narrowing
	// licence's conjunct 4 — see ruleState's field of the same name for why it
	// is derived at publication rather than per event. Guarded by ruleMu with
	// the rest of the compiled rule.
	personalClockRefusal string

	// personalLicence carries the host's assertion of the personal narrowing
	// licence's wiring conjuncts plus the accessor its live conjuncts read
	// (anchor_derivation_personal.go). Nil — a host that asserted nothing — is
	// the refusing answer, exactly like personalPlaneHealer's false.
	//
	// A pointer swapped atomically rather than a mutex-guarded struct because
	// every read is on the per-event path, and because the wiring and the
	// accessor must move together: a reader that saw one registration's wiring
	// beside another's accessor would be answering about neither.
	personalLicence atomic.Pointer[personalLicenceWiring]

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

	// projectionWrites counts every write this pipeline attempts against its
	// target — writeResults' primary write loop and replayWrite's retry
	// replay (results.go), across both the outcome-reporting and plain
	// Upsert/Delete adapter calls. It is the sweep survey cache's second
	// invalidation signal (sweep.go): the target key set can only move
	// through a write this pipeline itself makes, so an unmoved counter since
	// the last survey is proof the target side of that comparison cannot have
	// changed. Counting every ATTEMPT rather than only a confirmed commit is
	// deliberately conservative — it can force an unneeded re-survey, never
	// miss a needed one.
	projectionWrites atomic.Uint64
	// unsanctionedGrantKeyOnce bounds the read-grant namespace refusal's health
	// fault to one per lens. It lives on the PIPELINE, which outlives its
	// adapters: a fresh adapter is built for every INTO-only hot reload, so a
	// once on that type would re-arm on a package reinstall — exactly the
	// moment an operator would be reading the entry.
	unsanctionedGrantKeyOnce sync.Once
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

// OrderingTokenSeeded reports whether this pipeline holds an ordering token —
// whether Progress().LastAppliedSeq is non-zero.
//
// It is the same value ReprojectPersonalActor captures and REFUSES on, exposed
// as the question its callers actually ask: can a reprojection of this lens
// publish a frame at all? A frame at revision 0 is one the client discards, so
// publishing it would report a retraction that provably cannot retract.
//
// The grant-change drain reads it before consuming a signal, because it
// consumes each signal exactly once and does not re-enqueue a refusal. Asking
// here rather than re-deriving `Progress().LastAppliedSeq != 0` at each caller
// keeps the drain's readiness and the reprojection's refusal reading the same
// fact.
func (p *Pipeline) OrderingTokenSeeded() bool {
	return p.Progress().LastAppliedSeq != 0
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

// recordProjectionWrite marks one attempted adapter write against this
// pipeline's target. Called from every site that calls into the adapter's
// Upsert/Delete, with or without outcome reporting — see projectionWrites.
func (p *Pipeline) recordProjectionWrite() {
	p.projectionWrites.Add(1)
}

// ProjectionWrites returns the monotone count recordProjectionWrite has
// reached. Read by the sweep survey cache (sweep.go); see projectionWrites'
// doc for what it counts.
func (p *Pipeline) ProjectionWrites() uint64 {
	return p.projectionWrites.Load()
}

// EnvelopeFn rewrites a projection-row map into the on-wire shape the
// adapter writes (e.g. Contract #6 §6.2 Capability KV envelope). The
// function receives the raw RETURN-row map produced by the engine plus the
// EventContext.Parameters (so it can derive `projectedAt`, `$actorKey`, etc.)
// and returns the wrapped row + a possibly-rewritten Key map.
// A nil EnvelopeFn writes the row verbatim.
type EnvelopeFn func(row map[string]any, keys map[string]any, params map[string]any) (newRow, newKeys map[string]any, err error)

// EnvelopeScopeFn computes, ONCE per evaluation, the state every row's
// envelope then answers from — for an envelope whose per-row decision would
// otherwise re-read the same actor-scoped input once for each of that actor's
// rows.
//
// It is called after the engine has produced rows, only when the evaluation
// produced at least one and an envelope is installed, with that evaluation's
// ctx and its parameters. The entries it returns are merged into a COPY of
// those parameters, and that copy is what reaches the envelope: the map the
// engine evaluated against is never mutated, so no `$name` in a cypher can
// ever bind to anything a scope emitted. An error fails the whole evaluation,
// the same posture an envelope error takes — a scope that could not be
// computed is a decision input that could not be READ, never an absent one.
//
// The state it returns lives exactly as long as the evaluation. Nothing may
// hold it beyond that: an envelope's decision inputs are typically projections
// other pipelines write, so a value outliving the evaluation that read it is a
// stale decision with no bound on how stale.
type EnvelopeScopeFn func(ctx context.Context, params map[string]any) (map[string]any, error)

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
	p.bindAdapterReporters(adpt)
	return p, nil
}

// bindAdapterReporters wires an adapter this pipeline is about to write through
// to the lens's own health entry.
//
// Called from both places a pipeline takes an adapter — construction, and the
// replacement HotReloadInto swaps in — because a reload builds a FRESH adapter
// and one that arrived without its reporter would refuse writes into a log line
// with no health fault behind it. The pipeline is the only object that spans
// both, which is why the binding lives here rather than at either caller.
func (p *Pipeline) bindAdapterReporters(adpt adapter.Adapter) {
	if nkv, ok := adpt.(*adapter.NatsKVAdapter); ok {
		nkv.SetUnsanctionedGrantKeyReporter(p.RecordUnsanctionedGrantKeyRefusal)
	}
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
// active on the same pipeline — and any installed EnvelopeScopeFn, because a
// scope computes the decision inputs of ONE envelope: carried onto a different
// envelope it would either be dead weight or, on a security-plane envelope
// reading params by name, a set of admissions computed for something else.
// A caller replacing the envelope installs the scope that belongs to it,
// AFTER this. Must be called before Run.
func (p *Pipeline) SetEnvelopeFn(fn EnvelopeFn) {
	p.envelopeFn = fn
	p.envelopeScopeFn = nil
	if fn != nil {
		p.multiEnvelopeFn = nil
	}
}

// SetMultiEnvelopeFn installs the per-entry envelope wrapper (§4.1 of
// cap-read-per-anchor-grant-keys-design.md). Pass nil to clear. Clears any
// installed EnvelopeFn — the two are alternatives, never both active on the
// same pipeline — and any installed EnvelopeScopeFn, for the reason
// SetEnvelopeFn gives. Must be called before Run.
func (p *Pipeline) SetMultiEnvelopeFn(fn MultiEnvelopeFn) {
	p.multiEnvelopeFn = fn
	p.envelopeScopeFn = nil
	if fn != nil {
		p.envelopeFn = nil
	}
}

// SetEnvelopeScope installs the per-evaluation envelope scope. Pass nil to
// clear. Must be called before Run, and AFTER the envelope it belongs to —
// installing an envelope clears the scope, so the reverse order leaves the
// pipeline unscoped.
//
// The scope runs for whichever of EnvelopeFn / MultiEnvelopeFn the pipeline
// carries, and does nothing at all when neither is set.
func (p *Pipeline) SetEnvelopeScope(fn EnvelopeScopeFn) {
	p.envelopeScopeFn = fn
}

// HasEnvelopeScope reports whether an EnvelopeScopeFn is installed — the
// observation an installation-time wiring check needs without driving a live
// evaluation.
func (p *Pipeline) HasEnvelopeScope() bool {
	return p.envelopeScopeFn != nil
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
	p.bindAdapterReporters(newAdpt)
	return nil
}

// narrowedFilterFallbackPrefix opens the one health LastError
// registerWithFilterFallback writes, and is the exact latch its clean-
// registration path is licensed to retire. Writer and clearer read the same
// constant so the two cannot drift into a clear that retires nothing (a stale
// fallback message outliving every process that could explain it) or one that
// retires everything (an unrelated registration erasing another writer's live
// diagnosis).
const narrowedFilterFallbackPrefix = "narrowed Core KV filter registration failed"

// isNarrowedFilterFallback reports whether a stored health LastError is the
// narrowed-filter fallback's own message.
func isNarrowedFilterFallback(lastError string) bool {
	return strings.HasPrefix(lastError, narrowedFilterFallbackPrefix)
}

// retiredByCleanRegistration reports whether a stored health LastError names a
// condition a clean consumer registration has settled, and so may clear.
//
// Two classes qualify, for the same reason read from opposite ends. The
// narrowed-filter fallback is this function's own message, and a registration
// that succeeded without falling back IS the proof it no longer holds. A
// hot-reload refusal (health.HotReloadRefusalPrefix, recorded by
// cmd/refractor's reloader) says a SWAP could not carry a spec edit — and
// registering a consumer means this process activated the lens, which reads the
// current spec and installs all of it, so the edit that verdict was about has
// already applied or failed on its own terms. Both are latches with no other
// retirement path: a restart does not touch health KV and RecordError only ever
// appends, so left alone either outlives every process that could explain it.
//
// The hot-reload arm has a second form, because health KV outlives the binary
// that wrote it. A verdict persisted without the class marker names its REMEDY
// instead (health.ReactivateRemedy, the sentence every refusal ends with), and
// that remedy is activation — so a registration settles it on the identical
// argument, and matching the suffix is what reaches the entries a deployment is
// already carrying when it comes up on a binary that writes the marker.
//
// Everything else on the entry belongs to a writer this one is not. Both the
// writer and the clearer read the constants above, so the pair cannot drift into
// a clear that retires nothing or one that retires another writer's live
// diagnosis — a re-activation that could not purge its target, or a lens left
// dark, match none of the three forms precisely because a registration settles
// neither.
func retiredByCleanRegistration(lastError string) bool {
	return isNarrowedFilterFallback(lastError) ||
		strings.HasPrefix(lastError, health.HotReloadRefusalPrefix) ||
		strings.HasSuffix(lastError, health.ReactivateRemedy)
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
// clears a stale LastError an earlier process's fallback left on this same
// health entry (Reporter.ClearLastErrorIf) — that message has no other
// retirement path, since nothing else ever revisits it, so left alone it
// survives every restart even once the lens is provably healthy again.
//
// It clears ONLY what a clean registration has actually settled
// (retiredByCleanRegistration): its own fallback message, and a hot-reload
// refusal, which registering supersedes because activation reads the current
// spec. LastError is a latch many writers append to, so an unscoped clear
// retires whichever diagnosis happens to be standing, including one raised
// seconds earlier by a re-activation that cannot purge its target. The clear is
// also scoped to
// LastError alone precisely so it cannot race
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
			if clrErr := p.reporter.ClearLastErrorIf(ctx, retiredByCleanRegistration); clrErr != nil {
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
		msg := fmt.Sprintf("%s, fell back to the broad filter: %v", narrowedFilterFallbackPrefix, err)
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
//
// actorKey is the identity the shred names, in full Contract #1 vertex-key
// form. It is carried rather than derived because this retraction ANNOUNCES on
// the read-grant change edge, and the announcement's fallback arm — an adapter
// that reports no liveness transition — has no written key to invert back to an
// actor. Empty is permitted, for a caller naming no actor; that arm then
// announces nothing, the same fail-slow direction a missing sink takes.
func (p *Pipeline) Delete(ctx context.Context, keys map[string]any, actorKey string, projectionSeq uint64) error {
	adpt := p.currentAdapter()
	if deleter, ok := grantTransitionDeleter(adpt, p.grantSink); ok {
		outcome, err := deleter.DeleteWithOutcome(ctx, keys, projectionSeq)
		p.recordProjectionWrite()
		if err != nil {
			return err
		}
		if grantSignalOwed(outcome) && !p.notifyGrantChangeSignalled(outcome.Key, outcome.Transition) {
			p.notifyActorGrantChange(actorKey)
		}
		return nil
	}
	err := adpt.Delete(ctx, keys, projectionSeq)
	p.recordProjectionWrite()
	if err != nil {
		return err
	}
	p.notifyActorGrantChange(actorKey)
	return nil
}

// grantSignalOwed reports whether a retraction outcome leaves an announcement
// owed that the per-key path may not have made.
//
// Two of the three cases are silences the per-key path produces for reasons
// that are NOT "nothing was revoked": a sequence-less guarded write leaves the
// liveness unclassified (TransitionUnknown), and a key the lens's own inverse
// does not claim emits nothing at all. Both are correctly fail-slow for a CDC
// write, which has no actor in hand — a shred has one, so the coarser signal
// costs at most an extra reprojection.
//
// The third is DeclinedByWatermark, and it is the one that reads backwards. A
// declined guarded retraction returns the ZERO transition — not because the row
// was a tombstone, but because the guard never compared: it saw a stored
// watermark at or above this call's token and returned before reading the body.
// The row it left behind may be perfectly live. So TransitionNone alone does
// NOT positively mean nothing was revoked; only TransitionNone with the write
// actually attempted does.
//
// (A shred stamps math.MaxInt64, so no stored watermark can decline one today.
// That is a property of one caller's argument, not of this outcome type, and a
// predicate that reads the field is right whether or not the caller changes.)
func grantSignalOwed(outcome adapter.DeleteOutcome) bool {
	return outcome.Transition != adapter.TransitionNone || outcome.DeclinedByWatermark
}

// grantTransitionDeleter reports the outcome-returning deleter to route a
// retraction through when — and only when — the announcement it feeds will
// carry a real liveness transition.
//
// Both conjuncts are load-bearing and neither implies the other. A sink-less
// lens has nobody to announce to, so the outcome form buys nothing and the
// plain Delete stays the path. And an adapter satisfying OutcomeDeleter is NOT
// evidence that a transition will be derived: GrantWriterAdapter and
// PostgresAdapter both satisfy it while leaving Transition at TransitionNone,
// so keying on the interface would announce nothing for every key and report
// the retraction path covered while it emitted no signal at all. Only
// adapter.GrantTransitionDeriver answers the question actually being asked.
func grantTransitionDeleter(adpt adapter.Adapter, sink GrantChangeSink) (adapter.OutcomeDeleter, bool) {
	if sink == nil {
		return nil, false
	}
	deriver, ok := adpt.(adapter.GrantTransitionDeriver)
	if !ok || !deriver.DerivesGrantTransition() {
		return nil, false
	}
	deleter, ok := adpt.(adapter.OutcomeDeleter)
	return deleter, ok
}

// DeleteAllForActor removes every child key under actorKey's perEntry prefix
// (cap-read-per-anchor-grant-keys-design.md §4.2 point (d)), via the
// currently-active adapter, independent of the rule's own CDC stream — the
// perEntry-lens analog of Delete: a perEntry lens's grants for one actor live
// under N per-anchor keys rather than the single parent key Delete targets, so
// the Refractor KeyShredded nullification listener (control.RowSetNullifier)
// calls this instead when a target lens is configured PerEntry. Announces
// every grant it withdraws on the read-grant change edge, so a shred re-drives
// the personal plane the same way a CDC-path revocation does. Refuses
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
	// A shred is a revocation, and a revocation nobody hears about leaves the
	// consumer of the read-grant projection honouring a grant that is gone —
	// the over-grant direction. Where the adapter derives liveness the
	// announcement is per key and only for the keys that were actually live
	// (an already-tombstoned child transitions nowhere and signals nothing);
	// where it does not, one announcement names the actor after the loop, which
	// is the same actor every per-key signal would have inverted to anyway.
	deleter, perKey := grantTransitionDeleter(adpt, p.grantSink)
	// silentKey records that at least one key owed an announcement the per-key
	// path did not make — see grantSignalOwed for the three ways that happens.
	// Those are silences a shred can afford to close, because it holds the
	// actor by name.
	silentKey := false
	for _, keys := range existing {
		var err error
		if perKey {
			var outcome adapter.DeleteOutcome
			outcome, err = deleter.DeleteWithOutcome(ctx, keys, projectionSeq)
			if err == nil && grantSignalOwed(outcome) &&
				!p.notifyGrantChangeSignalled(outcome.Key, outcome.Transition) {
				silentKey = true
			}
		} else {
			err = adpt.Delete(ctx, keys, projectionSeq)
		}
		p.recordProjectionWrite()
		if err != nil {
			errs = append(errs, fmt.Errorf("delete %v: %w", keys, err))
			continue
		}
		deleted++
	}
	// The non-deriving arm announces on `deleted > 0` alone, which means it
	// announces again on a REDELIVERED shred of an actor whose keys are already
	// gone. That is accepted rather than overlooked: such an adapter cannot tell
	// a live key from a tombstone without a read-before-delete it deliberately
	// does not perform, so the only alternatives are this or announcing nothing
	// at all — and the cost is bounded to one reprojection per redelivery,
	// coalesced per identity by the reprojector's own dirty set, against the
	// benefit of never leaving a real revocation silent. The guarded arm above
	// has the liveness in hand and is exact.
	if (!perKey && deleted > 0) || silentKey {
		p.notifyActorGrantChange(actorKey)
	}
	if len(errs) > 0 {
		return fmt.Errorf("pipeline: DeleteAllForActor: deleted %d/%d keys under %q, then failed: %w",
			deleted, len(existing), childPrefix, errors.Join(errs...))
	}
	return nil
}
