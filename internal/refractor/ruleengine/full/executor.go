// Package full's executor walks the Refractor-native AST against Core KV
// (vertex/aspect data) and Adjacency KV (edges) to produce projection rows.
//
// The design stays close to the AST — there is no separate "plan" stage
// between Parse and Execute. Execution proceeds clause-by-clause over a
// list of bindings; each binding maps variable names to either a *nodeRef
// (graph node) or any other value (post-WITH alias).
//
// All Core KV reads filter `isDeleted: true` per Contract #1. All edge
// lookups go through Adjacency KV via the adjacency package.
package full

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// maxVarLengthHops is the sanity cap for variable-length traversals when the
// AST records MaxHops=-1 (unbounded). Without a cap a cycle-free but deep
// graph could trigger pathological BFS.
const maxVarLengthHops = 10

// errCoreKVReadDisabled signals that an expression needs a Core-KV read while
// the executor is in read-free mode (coreKV == nil — the anchor-tombstone
// delete key resolution). The caller treats it as "key column unresolvable" and
// falls through to a re-execute, never a wrong Delete.
var errCoreKVReadDisabled = errors.New("full engine: Core KV read disabled (read-free key resolution)")

// nodeRef is the executor's in-memory handle to one Core KV entry a variable
// is bound to — a vertex, or the LINK a relationship variable binds.
// A nil nodeRef represents an OPTIONAL MATCH null binding.
//
// rel is the relation name of the link this handle refers to, and is empty for
// a vertex. It is what tells a relationship binding apart from a node one, and
// what type(r) returns. It is a field rather than a reserved props entry
// because props is the entry's own body: a marker kept in there would collide
// with a link whose data carries a field of that name.
//
// revision is the Core KV revision the entry was read at (0 for an absent
// key) — the read-surface footprint a validating caller compares after
// evaluation to detect a mid-evaluation write to this key. A relationship
// binding carries 0, because it is built from the adjacency entry rather than
// read: the link document is read only where a lens dereferences a property
// off it, and that read enters the footprint as its own memo entry at its own
// revision.
type nodeRef struct {
	key      string
	props    map[string]any
	rel      string
	revision uint64
}

// binding maps variable names to values. Variable bindings established by
// MATCH/OPTIONAL MATCH carry *nodeRef values; WITH aliases may carry any
// scalar, list, map, or *nodeRef.
type binding map[string]any

// executor carries per-call mutable state for one Execute invocation.
type executor struct {
	ctx        context.Context
	adjKV      *substrate.KV
	coreKV     *substrate.KV
	params     map[string]any
	keyColumns []string

	// seedAnchor is the Core KV vertex key the anchor pattern's candidate set
	// narrows to for this evaluation (EventContext.SeedAnchor), armed only
	// when seedAnchorFor proved the query's anchor pattern can be point-seeded
	// by it. Empty means every pattern scans as usual.
	//
	// It is consumed exactly once — takeSeedAnchor clears it at the first
	// candidate set this evaluation builds by scan, which is the anchor by
	// construction (the first MATCH clause's first node is the first pattern
	// any evaluation seeds). Clearing is what keeps a later MATCH clause, an
	// OPTIONAL MATCH, or a sibling comma-separated pattern of the same label
	// from also collapsing to the event vertex.
	seedAnchor string

	// nodes memoizes fetchNode by Core KV key for the life of ONE evaluation,
	// giving the executor repeatable-read semantics: every access to a given
	// key inside one Execute observes the same value, whatever commits land
	// while the evaluation runs.
	//
	// This is a correctness requirement, not a cache. An aspect hop
	// (`u.listing.data.status`) that misses the vertex root body resolves as a
	// live point-read, and projectItems evaluates every non-aggregate WITH
	// column once per binding row — so one column is read once per row of the
	// OPTIONAL MATCH cross-product. Without the memo a commit landing between
	// two of those reads yields two different values for one column; because
	// the non-aggregate columns ARE the grouping key, that splits a single
	// anchor into two groups, and an actor-aggregate lens then emits two rows
	// sharing one anchor-derived output key. The pipeline's collision guard
	// fails that actor closed, so the projection update is dropped entirely.
	// Memoizing by key closes the class: a group splits only when the same
	// expression yields two values, and the same expression over the same node
	// is always the same key.
	//
	// A nil entry (absent / tombstoned / null-body) is memoized too — an
	// absent-then-created aspect splits a group exactly as a changed one does.
	nodes map[string]*nodeRef

	// prefetched stages the entries a BATCHED read fetched ahead of use, keyed
	// by Core KV key exactly as nodes is, with the same nil-means-absent
	// convention. It is a staging area, not the memo: fetchNode promotes an
	// entry into nodes the first time the evaluation dereferences that key.
	//
	// The split is what keeps the read-surface footprint the set of keys the
	// evaluation USED rather than the set a batch guessed at. A prefetch reads
	// what a clause is about to want, and a clause does not always want all of
	// it — a CASE takes one branch, an AND stops at its first false, a coalesce
	// at its first non-null. Promoting on use means a key the evaluation never
	// dereferenced never enters the certificate a validating caller re-checks,
	// so batching cannot widen what counts as mid-evaluation drift.
	//
	// Lives for ONE evaluation, exactly like nodes.
	prefetched map[string]*nodeRef

	// prefetchDisabled takes every node, aspect and adjacency read down the
	// one-at-a-time path, stamped at construction from the engine's posture.
	prefetchDisabled bool

	// prefetchedEdges stages the adjacency answers a BATCHED node-state read
	// fetched ahead of use, keyed by adjacency NodeID. It is the edge twin of
	// prefetched, and staged for the same reason: fetchEdges promotes an entry
	// through memoizeWhole at first use, so composition with an already-pinned
	// relation, the edgeRevisions record and the read observation all happen at
	// exactly the point they do without a batch, for exactly the nodes the walk
	// reaches.
	//
	// A staged entry is relation-agnostic — an unmarked node's read is its
	// whole document whatever relation a hop follows — so one entry serves a
	// typed hop and an untyped one alike. An entry for an overflow-MARKED node
	// carries no edges and is ignored: that node's edges live in Core KV's link
	// keyspace, and reading it stays the scoped read's job.
	//
	// Lives for ONE evaluation, exactly like edges.
	prefetchedEdges map[string]adjacency.Prefetched

	// pointReads counts the single-key Core KV reads this evaluation issued,
	// and adjReads the per-node adjacency reads — the round trips batching
	// removes, and the quantities that say whether a hop's frontier, a
	// projection's aspects, or a stage's bound sources were served from a
	// batch.
	pointReads int
	adjReads   int

	// batchReads counts the multi-get REQUESTS this evaluation issued —
	// including each retry a request split into. It is what says a batch is a
	// batch: an assertion counting only the point reads a batch removed stays
	// satisfied by a batch that issued one request per key.
	batchReads int

	// edges memoizes a WHOLE adjacency read by adjacency NodeID for the life
	// of ONE evaluation — the link twin of nodes, closing the same class of
	// defect for relationship hops: without it, every hop through a given
	// node reads live from Adjacency KV, so two hops through the same node
	// inside one evaluation (a variable-length traversal revisiting a
	// frontier node, or two separate MATCH clauses walking through it) could
	// observe two different edge lists for it. A whole memo entry serves
	// every hop on that node whatever relation each one follows, since it
	// holds the node's complete edge list and the caller filters.
	//
	// The list stored here is not always one read's answer: a node this
	// evaluation already read at a relation scope is COMPOSED before it is
	// memoized whole (composeWholeRead), so each relation in the stored list
	// is the one the first hop to follow that relation saw. That is what makes
	// the repeatable-read guarantee hold per RELATION rather than merely per
	// read: whatever order the hops come in, and whether or not the node's
	// overflow mark changed mid-evaluation, every hop's view of a relation on
	// a node is the first hop's view of that relation.
	//
	// edgeRevisions records the fingerprint each memoized node's WHOLE read
	// returned (an adjacency document's KV revision, 0 = absent; or a marked
	// node's link-set hash) — the adjacency half of the read-surface
	// footprint a validating caller compares after evaluation. Its key set is
	// exactly edges'. It is the whole READ's fingerprint, not the composed
	// list's, and on its own it says nothing about a substituted relation. The
	// write such a relation is exposed to landed BEFORE that read — between the
	// scoped read that pinned the relation and the whole read that took the
	// fingerprint — so the fingerprint already reflects it, and re-comparing it
	// later finds nothing moved. What catches it is the both-direction Matched
	// set the composition records (recordComposedPins), which holds the
	// relation as the EARLIER read found it and which the validator re-derives
	// alongside the fingerprint (pipeline.footprintValid). A relation-scoped
	// read contributes no fingerprint of its own, being incomparable with a
	// whole read's.
	edges         map[string][]adjacency.EdgeEntry
	edgeRevisions map[string]uint64

	// hubEdges memoizes a RELATION-SCOPED read of an overflow-marked node,
	// keyed by (node, relation) — the unit a typed hop over such a node reads
	// when hub read-scoping is on, in place of draining the hub's whole link
	// keyspace out of Core KV. Two different relations on one hub are two
	// reads of one relation each rather than two drains of everything.
	//
	// The RELATION is the memo key, and it stays the memo key even after the
	// node is also read whole: a later whole read of the same node is COMPOSED
	// against these entries before it is memoized (composeWholeRead), so the
	// relations recorded here keep the edges the hop that first crossed them
	// saw. Entries are kept rather than dropped after that composition — the
	// composed whole entry is consulted first, so they are never read again,
	// and they are the record of which relations in it came from an earlier
	// instant.
	//
	// An entry covers its relation in BOTH directions, because that is what a
	// relation-scoped read answers with, and the composition substitutes it
	// entire. That is why the composition footprints each substituted relation
	// under a both-direction selector of its own (recordComposedPins) rather
	// than leaving it to the one direction a hop happened to walk.
	//
	// A hub read contributes no fingerprint: a scoped fingerprint is not
	// comparable with a whole read's, and the unit that pins such a read is
	// the Matched set recordEdgeSelector records for the same hop (see
	// edgeSelectors). Lives for ONE evaluation, exactly like edges.
	hubEdges map[hubKey][]adjacency.EdgeEntry

	// hubReadScope is the resolved posture for this evaluation: whether a
	// typed hop may take the relation-scoped hub read at all. Stamped at
	// construction from the engine's own override or the package default, so
	// one evaluation never straddles two postures.
	hubReadScope bool

	// edgeSelectors records, per adjacency node, the selector-scoped read
	// surface §13.4 adds: which (relation type, direction) pairs traverseRel
	// consulted on that node, and which edge identities passed each one —
	// the narrower unit footprintValid compares against instead of the
	// node's whole-document revision, so a write to an unrelated relation on
	// a shared hub node does not read as drift. For a node read whole it
	// narrows what counts as drift; for a hub read it is the ONLY thing
	// pinning the node, since no fingerprint was recorded. Never populated
	// for a node this evaluation never traversed a typed relationship
	// through (fetchEdges alone, with no traverseRel selector recording,
	// leaves no entry here — a whole-read node then falls back to
	// whole-document comparison, same as Fallback).
	edgeSelectors map[string]*ruleengine.EdgeSelectorFootprint

	// maxBindings caps the binding set any one stage of this evaluation may
	// materialize; 0 disables the cap. steps counts checkpoint calls so the
	// context check can be sampled rather than paid per row.
	maxBindings int
	steps       int

	// peakBindingRows is the largest binding-slice size any stage of THIS
	// evaluation materialized — the high-water mark of the same quantity
	// maxBindings caps, stamped by checkBindings wherever a stage's slice has
	// just grown. It is per-evaluation state and lives here rather than on the
	// *CompiledRule, which Parse writes once and every concurrent evaluation of
	// the lens shares.
	peakBindingRows int

	// labelExpansion is the compiled rule's LabelExpansion, threaded through
	// so nodeMatches and the re-check inside seedNodes can resolve a
	// LabelExpand-flagged pattern without a graph read (§4.4 — the hot path
	// stays a map lookup). Nil for a rule with no `*` pattern, matching
	// CompiledRule.LabelExpansion's own zero value.
	labelExpansion map[string]map[string]struct{}

	// groupingRedundant is the compiled rule's parse-time grouping analysis
	// (grouping.go), read through redundantFor as each projecting clause runs.
	// Nil means every non-aggregating item is rendered into the grouping key.
	groupingRedundant map[Clause][]bool

	// branchStages and branchDeferred are the compiled rule's parse-time
	// branch-group analysis (branchgroups.go): the OPTIONAL MATCH clauses run
	// skips, and the plan projectItems expands them under, per base row. Nil
	// means every clause runs in the product, which is the path every evaluation
	// took before the analysis existed.
	branchStages   map[Clause]*stagePlan
	branchDeferred map[*Match]struct{}

	// provHead is the chain the MATCH walk currently in flight records onto:
	// the head binding traverseRel expands, or the source binding matchPath
	// seeds from. Every candidate such a walk fetches — bound, tombstoned or
	// rejected — belongs to that head, and so to every row descending from it.
	//
	// provCursor is the row one expression evaluation reads on behalf of, and
	// TAKES PRECEDENCE over provHead whenever it is set: a pattern
	// comprehension, an existence predicate and a decomposed branch all walk
	// patterns whose bindings are discarded, and the vertices they reach are
	// the evaluating row's dependencies rather than the discarded clones'.
	//
	// provFolded memoizes provVertexKeys per node, so a chain shared by many
	// output rows folds once.
	//
	// All three are nil on the read-free key-resolution executor
	// (anchor_delete), which records nothing because it fetches nothing.
	provHead   *provNode
	provCursor *provNode
	provFolded map[*provNode][]string
}

// hubKey identifies one relation-scoped read of an overflow-marked node: the
// adjacency NodeID and the single relation name the read asked for. It is the
// unit such a read is repeatable per — a hub read of relA and a hub read of
// relB on one node are two independent answers, neither of them the node's
// whole edge list.
type hubKey struct {
	nodeID string
	rel    string
}

// branchPlanFor returns c's branch-decomposition plan, or nil when the rule
// carries none for it — the product path, which is what a directly constructed
// *CompiledRule takes.
func (ex *executor) branchPlanFor(c Clause) *stagePlan {
	if ex.branchStages == nil {
		return nil
	}
	return ex.branchStages[c]
}

// redundantFor returns c's redundant-item mask, or nil when the rule carries no
// analysis for it — the unreduced path, which is what a directly constructed
// *CompiledRule takes.
func (ex *executor) redundantFor(c Clause) []bool {
	if ex.groupingRedundant == nil {
		return nil
	}
	return ex.groupingRedundant[c]
}

// cancelCheckInterval is how many checkpoint calls pass between context
// cancellation checks. A runaway evaluation is runaway because of its row
// count, so sampling bounds shutdown latency without paying a mutex-guarded
// ctx.Err() on every row of every stage.
const cancelCheckInterval = 1024

// checkCancelled reports the evaluation's context cancellation, sampled every
// cancelCheckInterval calls.
//
// Every read this evaluation makes is memoized, so once the candidate set is
// built a large MATCH + aggregation pass issues no further KV calls and is
// pure in-process work. Without a checkpoint inside it, a runaway evaluation
// runs to completion (or takes the host down) after SIGTERM: the consumer
// supervisor's Stop and the process's final WaitGroup both block on the
// in-flight handler, so nothing can exit until the evaluation finishes on its
// own. Aborting here surfaces as an evaluation error, which the pipeline
// disposes as a redelivery — never as an empty result set, so no projection
// target is retracted on the way down.
func (ex *executor) checkCancelled() error {
	ex.steps++
	if ex.steps%cancelCheckInterval != 0 {
		return nil
	}
	if err := ex.ctx.Err(); err != nil {
		return fmt.Errorf("full engine: evaluation cancelled: %w", err)
	}
	return nil
}

// checkBindings is the per-stage checkpoint: cancellation plus the binding-set
// cap, called wherever a stage's materialized binding slice has just grown.
// Each such slice is resident for the stage's life, so capping every one of
// them is what bounds the evaluation's heap.
//
// It errors rather than truncating. A truncated binding set silently produces
// a WRONG projection row — a short count(), a partial collect() — and writes it
// to the read model, where nothing downstream can tell it from a correct one.
// A refused evaluation is redelivered and visible. (This is the opposite call
// from the actor enumerator's cap, which truncates-and-warns because it bounds
// who gets notified, not the content of a row.)
//
// It is also where the evaluation's peak binding rows is stamped, because this
// is the one place every MATERIALIZED binding slice passes through — the clause
// accumulator in applyMatch, the per-pattern expansion in matchPatterns, and
// both the seed set and each relationship hop in matchPath. The stamp lands
// BEFORE the cancellation and cap checks so a refused or cancelled evaluation
// still reports the row count it reached, which is the number an operator
// diagnosing that refusal is looking for.
//
// A running total across slices that are never co-resident goes to
// checkBindingsCumulative instead, which caps without stamping: the peak is what
// the evaluation held at one time, not what it walked in total.
func (ex *executor) checkBindings(rows int) error {
	if rows > ex.peakBindingRows {
		ex.peakBindingRows = rows
	}
	return ex.enforceBindingCap(rows)
}

// checkBindingsCumulative applies the cancellation check and the cap to a
// RUNNING TOTAL — the rows a decomposed branch has expanded across every base
// row of one stage — and deliberately does NOT stamp the peak.
//
// The two quantities are different and both are wanted. The CAP bounds the work
// an evaluation may do: a branch walked once per base row costs the whole total
// even though each expansion is discarded before the next is built, so capping
// only the widest single expansion would admit an evaluation the product path
// correctly refuses. The PEAK gauge answers a different question — what the
// evaluation held at any ONE point, which is what
// docs/observability/health-kv-schema.md promises an operator, and the only
// reading under which decomposition is visible at all: a summed gauge reports
// the same number for the product and for the branches it replaced. Each
// expansion's own co-resident size is stamped where it is materialized, by
// checkBindings inside applyMatch.
func (ex *executor) checkBindingsCumulative(rows int) error {
	if err := ex.checkCancelled(); err != nil {
		return err
	}
	return ex.enforceBindingCap(rows)
}

// enforceBindingCap refuses past the cap, and never truncates.
func (ex *executor) enforceBindingCap(rows int) error {
	if ex.maxBindings <= 0 || rows <= ex.maxBindings {
		return nil
	}
	slog.Warn("full engine: binding-set cap exceeded; evaluation refused",
		"rows", rows, "cap", ex.maxBindings)
	return fmt.Errorf(
		"full engine: binding set reached %d rows, over the cap of %d (raise REFRACTOR_MAX_BINDINGS if this query is legitimate)",
		rows, ex.maxBindings)
}

// ExecuteWith runs cr against the given Core and Adjacency KVs, binding
// `$name` references from ec.Parameters. Called by the pipeline for each
// CDC event on the full-engine path.
//
// Returns one ProjectionResult per result row. Empty result => zero rows.
// ExecuteWithFootprint is the sibling entry point that also returns the
// evaluation's read-surface footprint; ExecuteWith exists unchanged so every
// pre-existing caller (package lens tests across packages/*, and any
// production caller uninterested in footprint validation) keeps compiling
// against the same two-return shape.
func (e *Engine) ExecuteWith(
	ctx context.Context,
	cr ruleengine.CompiledRule,
	ec ruleengine.EventContext,
	adjKV, coreKV *substrate.KV,
) ([]ruleengine.ProjectionResult, error) {
	results, _, err := e.ExecuteWithFootprint(ctx, cr, ec, adjKV, coreKV)
	return results, err
}

// ExecuteWithFootprint runs cr exactly as ExecuteWith does, additionally
// returning this evaluation's read-surface footprint (every vertex/aspect key
// and adjacency node it read, with the revision observed) — the certificate a
// validating caller (executeFullForActor in package pipeline) compares
// against current KV state after evaluation to detect a mid-evaluation write.
func (e *Engine) ExecuteWithFootprint(
	ctx context.Context,
	cr ruleengine.CompiledRule,
	ec ruleengine.EventContext,
	adjKV, coreKV *substrate.KV,
) ([]ruleengine.ProjectionResult, ruleengine.EvalFootprint, error) {
	results, footprint, _, err := e.ExecuteWithStats(ctx, cr, ec, adjKV, coreKV)
	return results, footprint, err
}

// ExecuteWithStats runs cr exactly as ExecuteWithFootprint does, additionally
// returning the evaluation's cost observations (ruleengine.EvalStats) — the
// gauge that sizes what a lens actually materializes against the binding-set
// cap that refuses it.
//
// The stats are returned on EVERY path, error included, and are meaningful
// there: a cap refusal or a cancellation still reports the peak row count it
// had reached, because that number is the diagnosis. Results and footprint
// keep their existing error-path contract (both zero when err is non-nil).
func (e *Engine) ExecuteWithStats(
	ctx context.Context,
	cr ruleengine.CompiledRule,
	ec ruleengine.EventContext,
	adjKV, coreKV *substrate.KV,
) ([]ruleengine.ProjectionResult, ruleengine.EvalFootprint, ruleengine.EvalStats, error) {
	compiled, ok := cr.(*CompiledRule)
	if !ok {
		return nil, ruleengine.EvalFootprint{}, ruleengine.EvalStats{},
			fmt.Errorf("full engine: expected *CompiledRule, got %T", cr)
	}
	if compiled.Query == nil {
		return nil, ruleengine.EvalFootprint{}, ruleengine.EvalStats{},
			errors.New("full engine: compiled rule has nil query")
	}

	ex := e.newExecutor(ctx, compiled, ec, adjKV, coreKV)

	results, footprint, err := ex.run(compiled)
	return results, footprint, ex.evalStats(), err
}

// newExecutor builds the per-evaluation state for one run of compiled: the
// engine's postures and caps, the event's parameters and seed, the compiled
// rule's parse-time analyses, and the empty memos every read of this
// evaluation goes through.
func (e *Engine) newExecutor(
	ctx context.Context,
	compiled *CompiledRule,
	ec ruleengine.EventContext,
	adjKV, coreKV *substrate.KV,
) *executor {
	return &executor{
		ctx:               ctx,
		adjKV:             adjKV,
		coreKV:            coreKV,
		params:            ec.Parameters,
		keyColumns:        compiled.KeyColumns,
		seedAnchor:        seedAnchorFor(compiled.Query, ec.SeedAnchor, compiled.LabelExpansion),
		nodes:             map[string]*nodeRef{},
		prefetched:        map[string]*nodeRef{},
		prefetchDisabled:  e.prefetchModeDisabled(),
		edges:             map[string][]adjacency.EdgeEntry{},
		prefetchedEdges:   map[string]adjacency.Prefetched{},
		edgeRevisions:     map[string]uint64{},
		hubEdges:          map[hubKey][]adjacency.EdgeEntry{},
		hubReadScope:      e.hubReadScopeEnabled(),
		edgeSelectors:     map[string]*ruleengine.EdgeSelectorFootprint{},
		maxBindings:       e.maxBindings,
		labelExpansion:    compiled.LabelExpansion,
		groupingRedundant: compiled.groupingRedundant,
		branchStages:      compiled.branchStages,
		branchDeferred:    compiled.branchDeferred,
		provFolded:        map[*provNode][]string{},
	}
}

// run walks compiled's clauses over this executor's per-evaluation state and
// returns the result rows with the read-surface footprint. It is the body of
// one evaluation, split from the entry point so every return path — including
// each error — passes back through the caller, which reports the evaluation's
// EvalStats whatever the outcome.
func (ex *executor) run(compiled *CompiledRule) ([]ruleengine.ProjectionResult, ruleengine.EvalFootprint, error) {
	bindings := []binding{{provBindingKey: provRoot()}}
	var lastReturn *Return

	for _, clause := range compiled.Query.Clauses {
		switch c := clause.(type) {
		case *Match:
			if _, deferred := ex.branchDeferred[c]; deferred {
				// A decomposed branch: its rows never enter this stage's
				// binding slice at all. projectItems expands it against each
				// base row instead, through this same applyMatch, and folds it
				// into its own aggregator.
				break
			}
			next, err := ex.applyMatch(bindings, c)
			if err != nil {
				return nil, ruleengine.EvalFootprint{}, err
			}
			bindings = next
		case *With:
			next, err := ex.applyWith(bindings, c)
			if err != nil {
				return nil, ruleengine.EvalFootprint{}, err
			}
			bindings = next
		case *Return:
			lastReturn = c
		}
	}

	if lastReturn == nil {
		return nil, ruleengine.EvalFootprint{}, errors.New("full engine: query missing RETURN clause")
	}
	results, err := ex.applyReturn(bindings, lastReturn)
	if err != nil {
		return nil, ruleengine.EvalFootprint{}, err
	}
	footprint := ex.footprint()
	if hook := footprintCapturedHook(ex.ctx); hook != nil {
		hook()
	}
	return results, footprint, nil
}

// evalStats returns this evaluation's cost observations. Valid at any point
// after the executor is built — the peak is stamped as the evaluation runs, so
// a caller that abandoned the run on an error reads the high-water mark it had
// reached rather than a zero.
func (ex *executor) evalStats() ruleengine.EvalStats {
	return ruleengine.EvalStats{PeakBindingRows: ex.peakBindingRows}
}

// footprint returns the read-surface certificate this evaluation observed —
// every Core KV key it read (via the node memo, absent = revision 0), every
// adjacency node it read WHOLE (via the edge memo, absent = revision 0), and
// the selector-scoped record of every typed hop. A node read only at a
// relation scope (an overflow-marked hub crossed by typed hops) carries no
// EdgeRevisions entry and appears in EdgeSelectors alone: its scoped
// fingerprint is not comparable with a whole read's, and its Matched sets are
// what a validating caller re-derives. Every memo is already populated for
// the life of the evaluation (see the nodes/edges/hubEdges field docs), so
// building the footprint costs no extra reads.
func (ex *executor) footprint() ruleengine.EvalFootprint {
	nodeRevs := make(map[string]uint64, len(ex.nodes))
	for key, ref := range ex.nodes {
		if ref == nil {
			nodeRevs[key] = 0
			continue
		}
		nodeRevs[key] = ref.revision
	}
	edgeRevs := make(map[string]uint64, len(ex.edgeRevisions))
	for nodeID, rev := range ex.edgeRevisions {
		edgeRevs[nodeID] = rev
	}
	edgeSelectors := make(map[string]ruleengine.EdgeSelectorFootprint, len(ex.edgeSelectors))
	for nodeID, sel := range ex.edgeSelectors {
		edgeSelectors[nodeID] = *sel
	}
	return ruleengine.EvalFootprint{NodeRevisions: nodeRevs, EdgeRevisions: edgeRevs, EdgeSelectors: edgeSelectors}
}

// Execute satisfies ruleengine.RuleEngine. It is the single-row convenience
// Execute satisfies ruleengine.RuleEngine but cannot operate on a real graph
// because the engine-neutral signature does not carry KV handles. The pipeline
// calls ExecuteWith directly. Returning a typed error keeps the contract honest.
func (e *Engine) Execute(_ context.Context, _ ruleengine.CompiledRule, _ ruleengine.EventContext) (ruleengine.ProjectionResult, error) {
	return ruleengine.ProjectionResult{}, errors.New(
		"full engine: Execute requires KV handles — call ExecuteWith from the pipeline")
}

// --- MATCH ---

func (ex *executor) applyMatch(bindings []binding, m *Match) ([]binding, error) {
	// The loop below walks each row on its own, so a pattern hanging off an
	// already-bound variable reads one node's adjacency per row. Reading them
	// all in one batch first is what turns that into a round trip per chunk.
	if err := ex.prefetchPathSources(bindings, m.Patterns); err != nil {
		return nil, err
	}
	var out []binding
	for _, b := range bindings {
		expanded, err := ex.matchPatterns(b, m.Patterns, m.Optional)
		if err != nil {
			return nil, err
		}
		// Apply WHERE. For OPTIONAL MATCH, WHERE filters MATCH'd rows but if
		// all matches are filtered out, the optional null-binding preserves
		// the original binding (Cypher OPTIONAL MATCH ... WHERE semantics).
		var passing []binding
		hadNonNullMatch := false
		for _, nb := range expanded {
			// "Non-null match" = at least one newly introduced pattern var
			// is bound to a real *nodeRef (not the null sentinel) in this
			// expansion that wasn't bound in b.
			if isNonNullExpansion(b, nb, m.Patterns) {
				hadNonNullMatch = true
				if m.Where != nil {
					// The predicate reads on this expansion's behalf, so
					// whatever it dereferences belongs to the row it admits.
					restore := ex.provPushRow(provChain(nb))
					v, err := ex.evalExpr(nb, m.Where)
					ex.provPopRow(restore)
					if err != nil {
						return nil, err
					}
					if !truthy(v) {
						// An excluded expansion projects no row, so what its
						// predicate reached is carried by the source binding
						// instead: the rows that survive this clause, and the
						// OPTIONAL null binding that stands in when none do,
						// all descend from it.
						provAbsorb(provChain(b), provChain(nb))
						continue
					}
				}
				passing = append(passing, nb)
			} else {
				// Null-preserving row — keep regardless of WHERE for OPTIONAL,
				// drop for required (which shouldn't produce null expansions).
				if m.Optional {
					passing = append(passing, nb)
				}
			}
		}
		if m.Optional && hadNonNullMatch {
			// Drop the null-preserving fallback rows when at least one real
			// match exists for THIS source binding.
			filtered := passing[:0]
			for _, nb := range passing {
				if isNonNullExpansion(b, nb, m.Patterns) {
					filtered = append(filtered, nb)
				}
			}
			// All real matches were excluded by WHERE: the OPTIONAL MATCH yields
			// no neighbor, so the anchor survives with the pattern variables bound
			// null (Cypher OPTIONAL MATCH ... WHERE semantics). The null fallback
			// is constructed from the source binding — the expansion set holds only
			// the now-filtered real rows when the pattern matched real neighbors, so
			// there is no null row there to recover.
			if len(filtered) == 0 {
				filtered = append(filtered, nullBindNewVars(b, m.Patterns))
			}
			passing = filtered
		}
		out = append(out, passing...)
		if err := ex.checkBindings(len(out)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// isNonNullExpansion reports whether nb is a "real" match expansion of b
// (i.e. at least one newly introduced variable from patterns is bound to a
// non-nil *nodeRef in nb but absent in b).
func isNonNullExpansion(b, nb binding, patterns []PathPattern) bool {
	for _, p := range patterns {
		for _, n := range p.Nodes {
			if n.Variable == "" {
				continue
			}
			if _, had := b[n.Variable]; had {
				continue
			}
			if ref, ok := nb[n.Variable].(*nodeRef); ok && ref != nil {
				return true
			}
		}
		for _, r := range p.Rels {
			if r.Variable == "" {
				continue
			}
			if _, had := b[r.Variable]; had {
				continue
			}
			if ref, ok := nb[r.Variable].(*nodeRef); ok && ref != nil {
				return true
			}
		}
	}
	return false
}

// nullBindNewVars clones b and binds every pattern variable in patterns that b
// does not already carry to the OPTIONAL-MATCH null sentinel ((*nodeRef)(nil)).
// It is the null fallback an OPTIONAL MATCH preserves when a pattern matches no
// neighbor at all (matchPatterns) OR when a WHERE excludes every real neighbor
// (applyMatch): both cases keep the source binding and null the newly introduced
// variables, the correct Cypher OPTIONAL MATCH semantics.
func nullBindNewVars(b binding, patterns []PathPattern) binding {
	nb := cloneBinding(b)
	for _, p := range patterns {
		for _, n := range p.Nodes {
			if n.Variable == "" {
				continue
			}
			if _, has := nb[n.Variable]; !has {
				nb[n.Variable] = (*nodeRef)(nil)
			}
		}
		for _, r := range p.Rels {
			if r.Variable == "" {
				continue
			}
			if _, has := nb[r.Variable]; !has {
				nb[r.Variable] = (*nodeRef)(nil)
			}
		}
	}
	return nb
}

// matchPatterns expands a binding across all comma-separated patterns in a
// single MATCH/OPTIONAL MATCH clause. For OPTIONAL MATCH that yields zero
// expansions, the original binding is preserved with null assignments for
// any newly introduced variables.
func (ex *executor) matchPatterns(b binding, patterns []PathPattern, optional bool) ([]binding, error) {
	current := []binding{b}
	for _, p := range patterns {
		var next []binding
		for _, cb := range current {
			expansions, err := ex.matchPath(cb, p)
			if err != nil {
				return nil, err
			}
			if len(expansions) == 0 && optional {
				// Null-bind every new variable introduced by this path.
				next = append(next, nullBindNewVars(cb, []PathPattern{p}))
			} else {
				next = append(next, expansions...)
			}
			if err := ex.checkBindings(len(next)); err != nil {
				return nil, err
			}
		}
		current = next
	}
	return current, nil
}

// matchPath expands binding b across one PathPattern. Returns zero or more
// new bindings — one per matched path.
func (ex *executor) matchPath(b binding, p PathPattern) ([]binding, error) {
	// The seed scan and the bound-head checks below read on b's behalf: their
	// candidates — the ones they admit and the ones they reject — are what
	// every row this path produces was derived from.
	prevHead := ex.provHead
	ex.provHead = provChain(b)
	defer func() { ex.provHead = prevHead }()

	if len(p.Nodes) == 0 {
		return []binding{b}, nil
	}

	// First node: either bound (existing variable) or seed by scan.
	first := p.Nodes[0]
	var heads []binding
	if first.Variable != "" {
		if existing, ok := b[first.Variable]; ok {
			ref, _ := existing.(*nodeRef)
			if ref == nil {
				// Null binding cannot extend.
				return nil, nil
			}
			if !ex.nodeMatches(ref, first) {
				return nil, nil
			}
			ok, err := ex.propsAllMatch(b, ref, first)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, nil
			}
			heads = []binding{b}
		}
	}
	if heads == nil {
		// Need to seed: scan Core KV for nodes matching label + props.
		seeds, err := ex.seedNodes(b, first)
		if err != nil {
			return nil, err
		}
		for _, s := range seeds {
			nb := cloneBinding(b)
			if first.Variable != "" {
				nb[first.Variable] = s
			}
			heads = append(heads, nb)
			if err := ex.checkBindings(len(heads)); err != nil {
				return nil, err
			}
		}
	}

	// Walk relationships.
	for i, rel := range p.Rels {
		toNode := p.Nodes[i+1]
		var next []binding
		for _, h := range heads {
			fromRef := ex.currentNode(h, p.Nodes[i])
			if fromRef == nil {
				provAbsorb(provParent(h), provChain(h))
				continue
			}
			reached, err := ex.traverseRel(h, fromRef, rel, toNode)
			if err != nil {
				return nil, err
			}
			if len(reached) == 0 {
				// This head expanded to nothing, so no row of its own will
				// carry what it read. Its siblings' rows are what an aggregate
				// over this hop projects, and they are what has to see the
				// candidate this head found tombstoned or rejected — so the
				// binding they share hands it on.
				provAbsorb(provParent(h), provChain(h))
			}
			next = append(next, reached...)
			if err := ex.checkBindings(len(next)); err != nil {
				return nil, err
			}
		}
		if len(next) == 0 {
			// The whole walk died at this hop. What it read belongs to the
			// binding it started from, which is the one an OPTIONAL MATCH
			// clones for its null binding and the one a required MATCH drops
			// outright; absorbing the frontier carries every intermediate hop
			// with it, since a frontier's chain names its own ancestors.
			for _, h := range heads {
				provAbsorb(provChain(b), provChain(h))
			}
		}
		heads = next
	}
	return heads, nil
}

// currentNode resolves the *nodeRef bound to nodePattern's variable (after
// seeding). For unnamed pattern nodes we fall back to the rel's "from" side
// in traverseRel, so this returns nil only when the variable name doesn't
// resolve.
func (ex *executor) currentNode(b binding, n NodePattern) *nodeRef {
	if n.Variable == "" {
		return nil
	}
	r, _ := b[n.Variable].(*nodeRef)
	return r
}

// nodeMatches checks label match. A pattern label IS the Contract #1 vertex key
// type: it matches iff the node's key parses as `vtx.<type>.<id>` and `<type>`
// equals the label, OR — when the pattern carries the `*` taxonomy-expansion
// sigil (NodePattern.LabelExpand) — `<type>` is a member of the label's
// resolved downward closure (dynamic-type-taxonomy-design.md §5.1 site 1).
// Fine-grained classification lives in the body's `class` field (Contract #1
// §1) and is matched with a property predicate — `MATCH (n {class:
// "service.laundry.template"})` — never with a label. A polymorphic TYPE
// question is the label's job, not the body's: `(l:location*)` expands against
// the declared taxonomy to the concrete key types, which is what a body
// predicate cannot do at all now that a location's class is its own key type.
//
// This is the reading of a label that every other mechanism already applies,
// and each of them depends on the binder agreeing: the labeled seed scan
// (seedNodes), event seeding (seedAnchorBinds), anchor retraction and the D2
// seeding eligibility the pipeline reads off it (anchor_delete.go's
// AnchorLabel), and the narrowing derivation (ReferencedLabels, consumed by the
// plain reproject gate, the client relevance gate and the actor-aware narrowed
// filter). A binder admitting a second resolution would let those narrow on a
// label set the executor does not honor — on the auth plane, a grant that never
// updates and never retracts.
//
// The lookup is keyed on the PATTERN's own LabelExpand flag, never on the
// label string alone: a query may bind both `(a:location)` and
// `(b:location*)`, and only the second ever consults LabelExpansion. A
// LabelExpand pattern whose label has no entry in LabelExpansion (the
// expansion was never resolved for it) matches nothing — fail closed, never
// fall back to the bare-label reading.
//
// Returns true when the pattern label is empty.
func (ex *executor) nodeMatches(ref *nodeRef, n NodePattern) bool {
	if n.Label == "" {
		return true
	}
	if ref == nil {
		return false
	}
	vtype, _, ok := substrate.ParseVertexKey(ref.key)
	if !ok {
		return false
	}
	if n.LabelExpand {
		set, hasSet := ex.labelExpansion[n.Label]
		if !hasSet {
			return false
		}
		_, hit := set[vtype]
		return hit
	}
	return vtype == n.Label
}

// propsAllMatch evaluates each property predicate in n against ref.
func (ex *executor) propsAllMatch(b binding, ref *nodeRef, n NodePattern) (bool, error) {
	if len(n.Properties) == 0 {
		return true, nil
	}
	for k, vexpr := range n.Properties {
		want, err := ex.evalExpr(b, vexpr)
		if err != nil {
			return false, err
		}
		got, ok := ref.props[k]
		if !ok {
			// Try "key" alias against Core KV key itself.
			if k == "key" {
				got = ref.key
			} else {
				return false, nil
			}
		}
		if !equalsAny(got, want) {
			return false, nil
		}
	}
	return true, nil
}

// fetchNode reads a Core KV vertex, returning nil for missing or soft-deleted.
// fetchNode reads the vertex (or aspect) at key, memoized for the life of this
// evaluation — see executor.nodes for why repeatable-read is a correctness
// requirement here. A read error is never memoized: it is transport, not a
// value, so a retry within the same evaluation may legitimately succeed.
//
// An entry a batched read staged ahead of this call is PROMOTED into the memo
// here rather than re-read, which is what makes a prefetch free of a second
// round trip and what puts the key into the read-surface footprint at the
// moment the evaluation actually uses it (see executor.prefetched).
//
// Every path through here — a live read, a promoted staging entry and a memo
// hit alike — records the key onto the current row's provenance, because all
// three are the evaluation dereferencing the key on that row's behalf: the
// second row to reach a node depends on it exactly as the first did.
func (ex *executor) fetchNode(key string) (*nodeRef, error) {
	ex.recordProv(key)
	if ref, ok := ex.nodes[key]; ok {
		return ref, nil
	}
	ref, staged := ex.prefetched[key]
	if staged {
		// The staging area holds only entries the evaluation has not reached
		// yet, so promoting one drops it.
		delete(ex.prefetched, key)
	} else {
		var err error
		ref, err = ex.readNode(key)
		if err != nil {
			return nil, err
		}
	}
	// The read-free key-resolution path (anchor_delete) builds an executor with
	// no memo; it resolves at most one key per call, so a nil map just means no
	// memoization rather than a second read.
	if ex.nodes != nil {
		ex.nodes[key] = ref
	}
	return ref, nil
}

// readNode performs the uncached Core KV point-read behind fetchNode.
func (ex *executor) readNode(key string) (*nodeRef, error) {
	ex.pointReads++
	entry, err := ex.coreKV.Get(ex.ctx, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("full engine: get %q: %w", key, err)
	}
	return decodeNode(key, entry)
}

// decodeNode renders one Core KV entry into the handle a variable binds — the
// single decoder behind both the point read and the batched prefetch, so a key
// yields the same value and the same revision however it was fetched.
//
// A nil entry (the key is absent), a JSON "null" body, and a soft-deleted
// entry all decode to a nil handle: absent, in the executor's terms. A
// null-body entry is a corrupted or transitional write and is read as a
// tombstone rather than as an empty vertex.
func decodeNode(key string, entry *substrate.KVEntry) (*nodeRef, error) {
	if entry == nil {
		return nil, nil
	}
	var props map[string]any
	if err := json.Unmarshal(entry.Value, &props); err != nil {
		return nil, fmt.Errorf("full engine: unmarshal %q: %w", key, err)
	}
	if props == nil {
		return nil, nil
	}
	if deleted, _ := props["isDeleted"].(bool); deleted {
		return nil, nil
	}
	props["key"] = key
	return &nodeRef{key: key, props: props, revision: entry.Revision}, nil
}

// prefetchChunk is the largest exact-key set one batched read asks for. It is
// the substrate multi-get's atomic fast-path cap (substrate.KVGetMulti): at or
// under it the response is computed under the stream's read lock in one round
// trip, and past it the primitive falls back to draining a consumer. Chunking
// here keeps every prefetch this executor issues on the fast path.
const prefetchChunk = 1024

// prefetchChunkFloor is the request size below which a failed read is the
// evaluation's error rather than another split. The response BYTE ceiling no
// subject count can predict (substrate.ChunkedMultiGet) is what the splitting
// exists for; sixteen vertex or aspect bodies are far under it, so a failure
// there is about something other than size.
const prefetchChunkFloor = 16

// prefetchNodes reads the given Core KV keys in batched requests and stages the
// decoded entries for the fetchNode calls about to ask for them, replacing one
// sequential point read per key with one round trip per chunk.
//
// The read is NoSnapshot and by EXACT KEY — never a wildcard. Each key is an
// independent fact: the pipeline re-validates the whole evaluation by comparing
// the read-surface footprint afterwards, so a set assembled from one instant
// buys this caller nothing and would fail outright whenever any of thousands of
// keys took a write mid-read (see substrate.KVGetMultiNoSnapshot, which spells
// out which callers are entitled to it).
//
// An absent key stages a nil handle, exactly what a point read of it yields, so
// promoting it memoizes the absence and footprints it at revision 0. A read
// error stages nothing and returns: transport is not a value. A key whose body
// does NOT DECODE also stages nothing, and is logged: the point read then fires
// if and only if the evaluation dereferences that key, and fails exactly where
// it failed before any batching — so a corrupt body on a branch the evaluation
// never takes cannot fail the evaluation.
//
// Fewer than two unstaged keys is left to the point-read path — one batched
// read of one key is one round trip either way. The read-free key-resolution
// executor (nil coreKV, nil memo) prefetches nothing.
func (ex *executor) prefetchNodes(keys []string) error {
	if ex.prefetchDisabled || ex.coreKV == nil || ex.nodes == nil || len(keys) < 2 {
		return nil
	}
	want := make([]string, 0, len(keys))
	requested := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, memoized := ex.nodes[key]; memoized {
			continue
		}
		if _, staged := ex.prefetched[key]; staged {
			continue
		}
		if _, dup := requested[key]; dup {
			continue
		}
		requested[key] = struct{}{}
		want = append(want, key)
	}
	if len(want) < 2 {
		return nil
	}
	read := func(ctx context.Context, chunk []string) (map[string]*substrate.KVEntry, error) {
		ex.batchReads++
		return ex.coreKV.GetMultiNoSnapshot(ctx, chunk)
	}
	visit := func(chunk []string, entries map[string]*substrate.KVEntry) error {
		for _, key := range chunk {
			ref, err := decodeNode(key, entries[key])
			if err != nil {
				slog.Warn("full engine: prefetched entry did not decode; leaving the key to its own read",
					"key", key, "error", err)
				continue
			}
			ex.stage(key, ref)
		}
		return nil
	}
	if err := substrate.ChunkedMultiGet(
		ex.ctx, want, prefetchChunk, prefetchChunkFloor, read, visit,
	); err != nil {
		return fmt.Errorf("full engine: prefetch %d keys: %w", len(want), err)
	}
	return nil
}

// stage records one prefetched entry, allocating the staging area on first use
// so an executor built without one still batches.
func (ex *executor) stage(key string, ref *nodeRef) {
	if ex.prefetched == nil {
		ex.prefetched = map[string]*nodeRef{}
	}
	ex.prefetched[key] = ref
}

// prefetchAspects batches the aspect and link reads the expressions in exprs
// are about to make against the node bindings in bindings — the projection twin
// of the frontier prefetch traverseRel takes.
//
// The keys it collects mirror resolveProperty's own derivation: a property
// absent from a vertex's root body resolves as the aspect key
// <nodeKey>.<property>, and a projectable property of a relationship binding
// resolves as a read of the link key itself. A property already in the root
// body, and the "key" alias, resolve with no read and are not collected.
//
// Only a property read straight off a VARIABLE is collectable, which is the
// only hop that reads: a deeper access (`n.aspect.data.name`) navigates the
// body the first hop returned, in memory.
//
// Collecting a key the evaluation then does not dereference — a CASE branch not
// taken, a short-circuited AND, a coalesce that stopped at its first argument —
// mostly costs the batched read and nothing else: a staged entry enters the
// memo, and so the read-surface footprint, only when fetchNode promotes it. A
// decode failure on such a key really does cost nothing more (prefetchNodes
// warns and leaves it unstaged, so a branch that never dereferences it never
// notices) — but a TRANSPORT failure on its chunk fails the whole evaluation
// regardless of which keys any binding was actually going to dereference
// (fail-closed: the error Naks the message for redelivery), so an unused key
// is not entirely free.
func (ex *executor) prefetchAspects(bindings []binding, exprs []Expr) error {
	if ex.prefetchDisabled || ex.coreKV == nil || ex.nodes == nil || len(bindings) == 0 {
		return nil
	}
	type propRef struct{ variable, property string }
	var refs []propRef
	seen := map[propRef]struct{}{}
	for _, e := range exprs {
		walkExprAll(e, func(sub Expr) {
			access, ok := sub.(*PropertyAccess)
			if !ok {
				return
			}
			target, ok := access.Target.(*VariableRef)
			if !ok {
				return
			}
			ref := propRef{variable: target.Name, property: access.Key}
			if _, dup := seen[ref]; dup {
				return
			}
			seen[ref] = struct{}{}
			refs = append(refs, ref)
		})
	}
	if len(refs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(bindings)*len(refs))
	for _, b := range bindings {
		for _, ref := range refs {
			node, _ := b[ref.variable].(*nodeRef)
			if node == nil {
				continue
			}
			if _, present := node.props[ref.property]; present {
				continue
			}
			if ref.property == "key" {
				continue
			}
			if node.rel != "" {
				if relPropertyProjectable(ref.property) {
					keys = append(keys, node.key)
				}
				continue
			}
			keys = append(keys, node.key+"."+ref.property)
		}
	}
	return ex.prefetchNodes(keys)
}

// projectionExprs returns the expressions items project, the argument the
// stage prefetch walks for the reads a projecting clause is about to make.
func projectionExprs(items []ProjectionItem) []Expr {
	exprs := make([]Expr, 0, len(items))
	for _, it := range items {
		exprs = append(exprs, it.Expr)
	}
	return exprs
}

// prefetchEdges reads the adjacency state of many nodes in one batched request
// and stages it for the fetchEdges calls about to ask for them — the adjacency
// twin of prefetchNodes, for the hops a stage takes from nodes it has ALREADY
// bound.
//
// rel is the relation the hop will follow, and decides only whether an id
// already answered at that relation's scope is worth asking for again: the
// answer a batch stages is the node's whole document, which is what an unmarked
// node's read returns at any scope.
//
// Fewer than two nodes left to ask for is left to the per-node read, which is
// one round trip either way.
func (ex *executor) prefetchEdges(nodeIDs []string, rel string) error {
	if ex.prefetchDisabled || ex.adjKV == nil || ex.edges == nil || len(nodeIDs) < 2 {
		return nil
	}
	scoped := ex.hopRelations(rel) != nil
	want := make([]string, 0, len(nodeIDs))
	requested := make(map[string]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		if _, memoized := ex.edges[id]; memoized {
			continue
		}
		if _, staged := ex.prefetchedEdges[id]; staged {
			continue
		}
		if scoped {
			if _, pinned := ex.hubEdges[hubKey{nodeID: id, rel: rel}]; pinned {
				continue
			}
		}
		if _, dup := requested[id]; dup {
			continue
		}
		requested[id] = struct{}{}
		want = append(want, id)
	}
	if len(want) < 2 {
		return nil
	}
	staged, requests, err := adjacency.PrefetchNodes(ex.ctx, ex.adjKV, want)
	ex.batchReads += requests
	if err != nil {
		return err
	}
	for id, entry := range staged {
		ex.stageEdges(id, entry)
	}
	return nil
}

// stageEdges records one prefetched adjacency answer, allocating the staging
// area on first use so an executor built without one still batches.
func (ex *executor) stageEdges(nodeID string, entry adjacency.Prefetched) {
	if ex.prefetchedEdges == nil {
		ex.prefetchedEdges = map[string]adjacency.Prefetched{}
	}
	ex.prefetchedEdges[nodeID] = entry
}

// prefetchPathSources batches the adjacency reads the first hop of each of
// patterns is about to make, over every row's ALREADY-BOUND source node.
//
// This is the shape that otherwise pays one round trip per row: matchPath walks
// each binding on its own, so an OPTIONAL MATCH hanging off a variable bound to
// thousands of rows reads thousands of nodes one at a time. A pattern whose
// first node is not an already-bound variable contributes nothing — it seeds by
// scan, and what it will hop from is not known until the seed is read.
//
// Batching a node the walk turns out not to reach costs the read and nothing
// else: a staged entry is neither memoized nor observed until fetchEdges
// promotes it.
func (ex *executor) prefetchPathSources(bindings []binding, patterns []PathPattern) error {
	for _, p := range patterns {
		if len(p.Rels) == 0 || len(p.Nodes) < 2 {
			continue
		}
		variable := p.Nodes[0].Variable
		if variable == "" {
			continue
		}
		ids := make([]string, 0, len(bindings))
		for _, b := range bindings {
			ref, _ := b[variable].(*nodeRef)
			if ref == nil {
				continue
			}
			ids = append(ids, adjacencyNodeID(ref.key))
		}
		if err := ex.prefetchEdges(ids, p.Rels[0].Type); err != nil {
			return err
		}
	}
	return nil
}

// prefetchStageReads batches the reads the expressions of one projecting stage
// are about to make over rows: the aspect and link bodies they dereference, and
// the adjacency of the bound nodes any pattern in them will hop from.
//
// Two expression shapes carry a pattern, and both walk it once per row through
// matchPath: a pattern comprehension, and an existence predicate (a bare path in
// a WHERE, and the `NOT (path)` anti-pattern, whose operand is that same node).
func (ex *executor) prefetchStageReads(bindings []binding, exprs []Expr) error {
	if err := ex.prefetchAspects(bindings, exprs); err != nil {
		return err
	}
	var patterns []PathPattern
	for _, e := range exprs {
		walkExprAll(e, func(sub Expr) {
			switch x := sub.(type) {
			case *PatternComprehension:
				patterns = append(patterns, x.Pattern)
			case *PatternExpr:
				patterns = append(patterns, x.Pattern)
			}
		})
	}
	if len(patterns) == 0 {
		return nil
	}
	return ex.prefetchPathSources(bindings, patterns)
}

// fetchEdges returns the edges of adjNodeID this hop may consume, memoized for
// the life of this evaluation — see executor.edges for why repeatable-read is a
// correctness requirement here, mirroring fetchNode/nodes for adjacency reads.
// rel is the hop's relation name, or "" for an untyped hop that consumes every
// edge on the node regardless of type.
//
// The answer is a SUPERSET of what the hop consumes, never a subset, and the
// caller filters either way:
//
//   - A whole memo entry (ex.edges) serves any hop on the node, typed or not.
//   - A typed hop on a node with no whole entry, with hub read-scoping on,
//     reads at the hop's relation. An UNMARKED node still answers with its
//     whole document — one key however many relations cross it — so it is
//     memoized whole and every later hop on it is served from that entry,
//     exactly as an untyped hop's read would be. A MARKED node answers with
//     that relation's links alone and is memoized under (node, relation) in
//     ex.hubEdges; its scoped fingerprint is discarded rather than recorded,
//     because it is not comparable with a whole read's — the Matched set
//     traverseRel records right after this call is what pins that read.
//   - An untyped hop, or any hop with scoping off, reads the node whole.
//
// Both whole-memo installs go through memoizeWhole, which COMPOSES the read
// against any relation of that node this evaluation already pinned and
// footprints what it substituted. A whole read is simply the union of every
// relation's read and must not be allowed to overwrite one that already has an
// answer: without the composition, a typed hop on a marked hub followed by an
// untyped hop on the same hub would leave the second read's list in the whole
// memo, and a third hop on the FIRST relation would be served an edge list from
// a later instant — the very defect the memo exists to close. The same holds if
// the node stops being marked mid-evaluation (a bucket wipe, the only way a
// mark clears), which is why the whole=true arm composes too.
//
// A read error is never memoized: it is transport, not a value. Mirrors
// fetchNode's nil-map guard on both memos: the read-free key-resolution
// executor (anchor_delete) never traverses a relationship, so this only
// matters defensively.
func (ex *executor) fetchEdges(adjNodeID string, rel string) ([]adjacency.EdgeEntry, error) {
	if edges, ok := ex.edges[adjNodeID]; ok {
		return edges, nil
	}
	rels := ex.hopRelations(rel)
	// An entry a batched node-state read staged is promoted here, which is
	// where the per-node read would have happened: it is memoized through the
	// same composition, and it reports the read it stands in for to a context
	// observer at the same point. A staged MARKED node carries no edges and
	// falls through to the read below, the only path that can answer for it.
	if staged, ok := ex.prefetchedEdges[adjNodeID]; ok && !staged.Marked {
		delete(ex.prefetchedEdges, adjNodeID)
		adjacency.ObserveWholeRead(ex.ctx, adjNodeID, rels)
		return ex.memoizeWhole(adjNodeID, staged.Edges, staged.Fingerprint), nil
	}
	if rels != nil {
		hk := hubKey{nodeID: adjNodeID, rel: rel}
		if edges, ok := ex.hubEdges[hk]; ok {
			return edges, nil
		}
		ex.adjReads++
		edges, fingerprint, whole, err := adjacency.NeighborsScoped(
			ex.ctx, ex.adjKV, ex.coreKV, adjNodeID, rels)
		if err != nil {
			return nil, err
		}
		if whole {
			return ex.memoizeWhole(adjNodeID, edges, fingerprint), nil
		}
		if ex.hubEdges != nil {
			ex.hubEdges[hk] = edges
		}
		return edges, nil
	}
	ex.adjReads++
	edges, revision, err := adjacency.Neighbors(ex.ctx, ex.adjKV, ex.coreKV, adjNodeID)
	if err != nil {
		return nil, err
	}
	return ex.memoizeWhole(adjNodeID, edges, revision), nil
}

// hopRelations is the relation scope fetchEdges reads a node at for a hop of
// rel: that single relation when the hop is typed AND hub read-scoping is on,
// and nil — the whole node — otherwise. It is the one derivation, so the batch
// that prefetches a set of nodes and the read that would have fetched one of
// them cannot come to disagree about the scope they were read at.
func (ex *executor) hopRelations(rel string) map[string]struct{} {
	if rel == "" || !ex.hubReadScope {
		return nil
	}
	return map[string]struct{}{rel: {}}
}

// memoizeWhole installs a whole read of adjNodeID as the node's memo entry and
// returns the list every later hop on that node will be served, which is the
// composition of this read with any relation an earlier relation-scoped read
// already pinned (composeWholeRead).
//
// fingerprint is the READ's, recorded as-is: it is what a validating caller
// re-derives by re-reading the node whole, and composing changed the list this
// evaluation uses, not the state the read observed. What pins a substituted
// relation instead is the both-direction selector recordComposedPins writes for
// it — the two go together, and neither is sound without the other.
func (ex *executor) memoizeWhole(adjNodeID string, edges []adjacency.EdgeEntry, fingerprint uint64) []adjacency.EdgeEntry {
	pinned := ex.pinnedRelations(adjNodeID)
	composed := composeWholeRead(edges, pinned)
	ex.recordComposedPins(adjNodeID, pinned)
	if ex.edges != nil && ex.edgeRevisions != nil {
		ex.edges[adjNodeID] = composed
		ex.edgeRevisions[adjNodeID] = fingerprint
	}
	return composed
}

// pinnedRelations returns the relations of adjNodeID this evaluation has
// already read at a relation scope, keyed by relation name — nil when there
// are none, which is every evaluation that never crossed an overflow-marked
// hub.
//
// The scan is over the whole hub memo rather than a per-node index because
// that memo holds one entry per (marked hub, relation) this evaluation
// actually crossed, which is a handful at most; a node-keyed index would buy
// nothing at that size.
func (ex *executor) pinnedRelations(adjNodeID string) map[string][]adjacency.EdgeEntry {
	if len(ex.hubEdges) == 0 {
		return nil
	}
	var pinned map[string][]adjacency.EdgeEntry
	for hk, edges := range ex.hubEdges {
		if hk.nodeID != adjNodeID {
			continue
		}
		if pinned == nil {
			pinned = map[string][]adjacency.EdgeEntry{}
		}
		pinned[hk.rel] = edges
	}
	return pinned
}

// composeWholeRead folds pinned relations back into a whole read of the same
// node: every edge of a pinned relation is dropped from whole and replaced by
// the pinned read's own edges, in BOTH directions, since a relation-scoped read
// answers with every link of that relation incident to the node whichever end
// the node is. The result is sorted into adjacency.SortEdges' canonical order
// (EdgeID, then Direction), which is the order a marked node's own read
// produces. The composed list is several reads spliced together, and the whole
// read it splices into may be a document read whose order is the document's, so
// there is no single read whose order to reproduce — a canonical one is what
// makes the result stable instead.
//
// A relation nothing pinned passes through untouched, so a whole read with no
// pinned relations is returned unchanged.
func composeWholeRead(whole []adjacency.EdgeEntry, pinned map[string][]adjacency.EdgeEntry) []adjacency.EdgeEntry {
	if len(pinned) == 0 {
		return whole
	}
	composed := make([]adjacency.EdgeEntry, 0, len(whole))
	for _, e := range whole {
		if _, replaced := pinned[e.Name]; replaced {
			continue
		}
		composed = append(composed, e)
	}
	for _, edges := range pinned {
		composed = append(composed, edges...)
	}
	adjacency.SortEdges(composed)
	return composed
}

// recordComposedPins footprints what the composition just did: for every
// relation it substituted, the edge identities of that relation on this node,
// under a BOTH-direction selector.
//
// It has to be the composition that writes this, and it has to be
// direction-blind. A relation-scoped read answers in both directions, so the
// composition substitutes the relation entire — but the only selector a typed
// hop records is the one direction it walked, and recordEdgeSelector stops
// recording altogether once an untyped hop has set Fallback. A link of that
// relation arriving on the direction no hop walked would then sit in a
// composed list that no recorded set covers, under a fingerprint taken at the
// later instant, and validation would confirm a row assembled from a view no
// instant of the graph ever held. The both-direction pin is what a validator
// re-derives to catch exactly that: over the whole re-read on the coarse path,
// and over a scoped re-read that now names this relation on the selector path.
//
// The pin is written ONLY here, so a node that is never COMPOSED keeps its
// exact directional selectors and pays nothing: it exists from the moment a
// whole read consumed a substituted relation, and not before. Composition is
// what the predicate turns on, not the hops' shape — a node whose hops are all
// typed still gets a pin if its mark cleared mid-evaluation and a later typed
// hop took the whole arm. That node keeps Fallback false, so validation takes
// the selector path and compares the pin there, which is the same answer by a
// different route.
func (ex *executor) recordComposedPins(adjNodeID string, pinned map[string][]adjacency.EdgeEntry) {
	if len(pinned) == 0 || ex.edgeSelectors == nil {
		return
	}
	entry := ex.edgeSelectors[adjNodeID]
	if entry == nil {
		entry = &ruleengine.EdgeSelectorFootprint{}
		ex.edgeSelectors[adjNodeID] = entry
	}
	if entry.Matched == nil {
		entry.Matched = map[ruleengine.EdgeSelector]map[string]struct{}{}
	}
	for rel, edges := range pinned {
		ids := make(map[string]struct{}, len(edges))
		for _, e := range edges {
			ids[e.EdgeID] = struct{}{}
		}
		entry.Matched[ruleengine.EdgeSelector{RelType: rel, Direction: DirBoth.String()}] = ids
	}
}

// recordEdgeSelector records, for adjNodeID, the (rel.Type, rel.Direction)
// selector this hop consulted against edges — the selector-scoped read-
// surface unit (§13.4) a validating caller later re-applies to a fresh read
// instead of comparing adjNodeID's whole adjacency-document revision, so a
// write to an unrelated relation on a shared hub node does not read as
// drift. For a node fetchEdges read at the hop's relation rather than whole
// (an overflow-marked hub), this Matched set is the node's ONLY footprint
// entry: no fingerprint was recorded for it, so validation re-reads at the
// same relation scope and compares these identities.
//
// An untyped selector (rel.Type == "") consumes every edge on the node
// regardless of type — it marks the node's entry Fallback instead of
// recording a Matched set, since whole-document revision comparison already
// covers (and is the only sound comparison for) an untyped read; the same
// hop reads the node whole, so a fingerprint is always there to compare.
// Once a node's entry is Fallback, a later typed hop on the same node
// records nothing further: the coarser comparison already subsumes it. The
// sets an entry carries alongside Fallback are therefore exactly the typed
// hops that PRECEDED the untyped one, each observed at its own earlier
// instant — which is why validation re-applies them as well as comparing the
// fingerprint.
//
// Called once per (node, hop-iteration) from traverseRel's per-node edge
// loop; a node revisited via the same selector later in the same evaluation
// (a variable-length walk crossing it twice, or two separate MATCH clauses)
// just unions into the same Matched set — idempotent, no special-casing
// needed.
func (ex *executor) recordEdgeSelector(adjNodeID string, rel RelPattern, edges []adjacency.EdgeEntry) {
	if ex.edgeSelectors == nil {
		return
	}
	entry := ex.edgeSelectors[adjNodeID]
	if entry == nil {
		entry = &ruleengine.EdgeSelectorFootprint{}
		ex.edgeSelectors[adjNodeID] = entry
	}
	if rel.Type == "" {
		entry.Fallback = true
		return
	}
	if entry.Fallback {
		return
	}
	sel := ruleengine.EdgeSelector{RelType: rel.Type, Direction: rel.Direction.String()}
	ids := entry.Matched[sel]
	if ids == nil {
		ids = map[string]struct{}{}
		if entry.Matched == nil {
			entry.Matched = map[ruleengine.EdgeSelector]map[string]struct{}{}
		}
		entry.Matched[sel] = ids
	}
	for _, e := range edges {
		if e.Name != rel.Type {
			continue
		}
		if !adjacency.DirectionMatches(e.Direction, rel.Direction.String()) {
			continue
		}
		ids[e.EdgeID] = struct{}{}
	}
}

// --- WITH ---

func (ex *executor) applyWith(bindings []binding, w *With) ([]binding, error) {
	projected, err := ex.projectItems(bindings, w.Items, ex.redundantFor(w), ex.branchPlanFor(w))
	if err != nil {
		return nil, err
	}
	// WITH DISTINCT de-duplicates the projected rows, first occurrence wins, by
	// the same injective rendering applyReturn's DISTINCT and the grouping path
	// key on — never json.Marshal, which renders every *nodeRef as `{}` and so
	// collapses two rows that differ only in the node they bound.
	//
	// It runs on the PROJECTED rows and before the WHERE, matching Cypher's
	// `WITH DISTINCT … WHERE …`: the filter reads the distinct set. With no
	// ORDER BY/SKIP/LIMIT in the clause the two orders yield the same rows for
	// any row predicate — a duplicate pair is identical, so a filter keeps both
	// copies or neither — so what this ordering buys is the predicate being
	// evaluated once per distinct row rather than once per duplicate, and the
	// semantics staying right the day the clause gains a row limit.
	if w.Distinct {
		seen := make(map[string]struct{}, len(projected))
		deduped := projected[:0]
		for _, row := range projected {
			key := normalizeForKey(provStripped(row))
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				deduped = append(deduped, row)
			}
		}
		projected = deduped
	}
	if w.Where != nil {
		// The predicate runs once per projected row, so whatever bodies and
		// neighbour sets it reaches off those rows are one batched read for
		// the clause.
		if err := ex.prefetchStageReads(projected, []Expr{w.Where}); err != nil {
			return nil, err
		}
		var filtered []binding
		for _, b := range projected {
			// The predicate reads on this row's behalf — a pattern
			// comprehension or existence check inside it walks vertices the
			// row's survival depends on.
			restore := ex.provPushRow(provChain(b))
			v, err := ex.evalExpr(b, w.Where)
			ex.provPopRow(restore)
			if err != nil {
				return nil, err
			}
			if truthy(v) {
				filtered = append(filtered, b)
			}
		}
		projected = filtered
	}
	return projected, nil
}

// projectItems evaluates each ProjectionItem against the inbound bindings.
// If any item is an aggregating expression (e.g. collect), the result is
// grouped by the non-aggregating items.
//
// redundant is the clause's parse-time grouping analysis (grouping.go), indexed
// by item: a marked item is still EVALUATED — its value is what the group row
// projects, and evaluating it is what keeps this evaluation's read-surface
// footprint bit-identical — but contributes no fragment to the grouping key,
// because the aliases still in that key already determine its value. A nil or
// short slice renders every item, which is the behaviour with no analysis.
//
// plan is the clause's parse-time branch-group analysis (branchgroups.go). When
// it is non-nil, `bindings` is the BASE row set — the stage's sibling OPTIONAL
// MATCH branches it names were never applied to it — and this function expands
// each of them per base row, folding each into the aggregators stamped with it.
// Nil is the product path, where every branch already multiplied the rows.
func (ex *executor) projectItems(bindings []binding, items []ProjectionItem, redundant []bool, plan *stagePlan) ([]binding, error) {
	// Both arms below evaluate the items once per inbound row, so the bodies
	// and neighbour sets those items reach are a single batched read for the
	// whole stage rather than one round trip per row per column.
	if err := ex.prefetchStageReads(bindings, projectionExprs(items)); err != nil {
		return nil, err
	}
	// Both arms evaluate one inbound row's items at a time, and each arm sets
	// the cursor to that row for the whole of it — an item's aspect hop, a
	// pattern comprehension's walk and a decomposed branch's own MATCH all
	// read on behalf of the row being projected. outerRow is what an enclosing
	// row's evaluation had set, which keeps precedence and is restored on
	// every exit from this stage.
	outerRow := ex.provCursor
	defer func() { ex.provCursor = outerRow }()

	// Decide aggregating vs non-aggregating per item.
	itemAggregating := make([]bool, len(items))
	anyAggregating := false
	for i, it := range items {
		if containsAggregator(it.Expr) {
			itemAggregating[i] = true
			anyAggregating = true
		}
	}
	itemAlias := func(i int) string {
		if items[i].Alias != "" {
			return items[i].Alias
		}
		return projectionAutoAlias(items[i].Expr, i)
	}

	if !anyAggregating {
		// A plan here would mean run() skipped this stage's deferred branches
		// while the projection has no fold to feed them into — their rows would
		// simply be gone, which is a SHORT projection written to the read model
		// with nothing downstream able to tell it from a correct one. The
		// analysis cannot produce that pairing (it defers nothing unless the
		// stage aggregates), so this is the impossible case failing loudly
		// rather than the engine's one silent-wrongness mode.
		if plan != nil && len(plan.deferred) > 0 {
			return nil, errors.New(
				"full engine: branch decomposition deferred a clause into a projection that aggregates nothing")
		}
		out := make([]binding, 0, len(bindings))
		for _, b := range bindings {
			if err := ex.checkCancelled(); err != nil {
				return nil, err
			}
			ex.provCursor = provRowTarget(outerRow, b)
			// The projected row is a fresh binding carrying only the items'
			// values, so it descends from b's chain explicitly: without that
			// link every read the traversal feeding this stage made would be
			// dropped at the WITH boundary, which is where every corpus lens's
			// tail begins.
			nb := binding{provBindingKey: &provNode{parent: provChain(b)}}
			for i, it := range items {
				v, err := ex.evalExpr(b, it.Expr)
				if err != nil {
					return nil, err
				}
				nb[itemAlias(i)] = v
			}
			out = append(out, nb)
		}
		return out, nil
	}

	// Group: compute the grouping key per row, folding each aggregating item
	// into its group as the row is visited. Only the running result is held —
	// the rows' argument values are never accumulated, so a count() or max()
	// over a large MATCH costs a scalar per group rather than one boxed value
	// per binding row.
	type groupAcc struct {
		row  binding
		aggs []aggFold // indexed by item; nil for a non-aggregating item
	}
	groups := map[string]*groupAcc{}
	var order []string
	// branchRows counts, per decomposed branch, the rows this stage has expanded
	// through it across every base row — the quantity that was |base| × Π|G| in
	// the product and is now |base ⋈ G| per branch. The CAP is enforced against
	// that total, because the work is really done even though each base row's
	// expansion is discarded before the next is built; capping one expansion
	// instead would admit an evaluation the product path refuses. The PEAK gauge
	// is not stamped from it — these expansions are never co-resident, and each
	// one's own size is stamped inside applyMatch where it is materialized.
	var branchRows []int
	if plan != nil {
		branchRows = make([]int, len(plan.deferred))
		// Each deferred branch is expanded ONE base row at a time below, so no
		// batch can form inside applyMatch there: its first clause's hops cost
		// a node-state read per base row. Reading every base row's sources for
		// that clause here — once per branch, before the fold loop — is what
		// turns them into a request per chunk.
		//
		// Only the first clause. A later clause of a branch hops from a head
		// the expansion itself binds, so what it will read is not known until
		// that expansion runs, and it stays per row.
		for _, branch := range plan.deferred {
			if len(branch.clauses) == 0 {
				continue
			}
			if err := ex.prefetchPathSources(bindings, branch.clauses[0].Patterns); err != nil {
				return nil, err
			}
		}
	}
	for _, b := range bindings {
		if err := ex.checkCancelled(); err != nil {
			return nil, err
		}
		ex.provCursor = provRowTarget(outerRow, b)
		// Build grouping key
		keyParts := make([]string, 0, len(items))
		groupVals := map[int]any{}
		for i, it := range items {
			if itemAggregating[i] {
				continue
			}
			v, err := ex.evalExpr(b, it.Expr)
			if err != nil {
				return nil, err
			}
			groupVals[i] = v
			if i < len(redundant) && redundant[i] {
				continue
			}
			// Each fragment is index-tagged, so omitting item i cannot make two
			// different item sets render alike: injectivity over the retained
			// items is preserved without touching normalizeForKey.
			keyParts = append(keyParts, fmt.Sprintf("%d=%v", i, normalizeForKey(v)))
		}
		k := strings.Join(keyParts, "|")
		g, ok := groups[k]
		if !ok {
			g = &groupAcc{
				row:  binding{provBindingKey: &provNode{}},
				aggs: make([]aggFold, len(items)),
			}
			for i := range items {
				if v, present := groupVals[i]; present {
					g.row[itemAlias(i)] = v
				}
			}
			for i, it := range items {
				if !itemAggregating[i] {
					continue
				}
				var (
					f   aggFold
					err error
				)
				if plan == nil {
					f, err = newAggFold(it.Expr)
				} else {
					f, err = newAggFoldRouted(it.Expr, plan.foldGroup)
				}
				if err != nil {
					return nil, err
				}
				g.aggs[i] = f
			}
			groups[k] = g
			order = append(order, k)
		}
		// The group's row is derived from every member folded into it, so its
		// provenance is their union: a change to anything any member read can
		// move the aggregate this one row projects.
		if member := provChain(b); member != nil {
			if gn := provChain(g.row); gn != nil {
				gn.merged = append(gn.merged, member)
			}
		}
		if plan == nil {
			for i := range items {
				if !itemAggregating[i] {
					continue
				}
				if err := g.aggs[i].add(ex, b); err != nil {
					return nil, err
				}
			}
			continue
		}
		// A fold reading no decomposed branch sees each base row ONCE, where the
		// product fed it once per product row. Every such fold is
		// multiplicity-insensitive by §4.2, so the two agree; feeding it once is
		// what makes that agreement independent of how wide the branches are.
		for i := range items {
			if !itemAggregating[i] {
				continue
			}
			if err := g.aggs[i].addRouted(ex, b, branchGroupBase); err != nil {
				return nil, err
			}
		}
		for di, branch := range plan.deferred {
			rows := []binding{b}
			for _, m := range branch.clauses {
				next, err := ex.applyMatch(rows, m)
				if err != nil {
					return nil, err
				}
				rows = next
			}
			branchRows[di] += len(rows)
			if err := ex.checkBindingsCumulative(branchRows[di]); err != nil {
				return nil, err
			}
			for _, rb := range rows {
				for i := range items {
					if !itemAggregating[i] {
						continue
					}
					if err := g.aggs[i].addRouted(ex, rb, di); err != nil {
						return nil, err
					}
				}
			}
		}
	}

	out := make([]binding, 0, len(order))
	for _, k := range order {
		g := groups[k]
		for i := range items {
			if !itemAggregating[i] {
				continue
			}
			v, err := g.aggs[i].value()
			if err != nil {
				return nil, err
			}
			g.row[itemAlias(i)] = v
		}
		out = append(out, g.row)
	}
	// out is empty here iff bindings was empty on entry: every binding that
	// reaches this point lands in exactly one group (the grouping loop above
	// unconditionally creates/reuses a group for each b), so a non-empty
	// bindings always yields a non-empty out. A required MATCH that bound
	// zero rows (an unanchored scan with no matching vertex) has no anchor to
	// attach an aggregate to — zero rows in must project zero rows out, never
	// a synthetic all-null row: a fabricated row's nil-derived key/anchor
	// columns are unwritable by a protected adapter (an anchor column is
	// never nil) and would suppress the target-retraction a genuinely empty
	// result set is supposed to drive. (Cypher's "collect() on empty input
	// yields []" applies to a per-anchor OPTIONAL MATCH whose neighbor set is
	// empty; that anchor's binding is preserved by applyMatch's null-binding
	// fallback and already produced a real group above — see
	// TestExec_MaxNoMatchIsNull.)
	return out, nil
}

func projectionAutoAlias(e Expr, idx int) string {
	switch x := e.(type) {
	case *VariableRef:
		return x.Name
	case *PropertyAccess:
		return x.Key
	}
	return fmt.Sprintf("_col%d", idx)
}

// containsAggregator returns true if the expression tree contains an aggregator
// this engine folds, reading the same name set newAggFold dispatches on
// (aggregate.go). An item holding one GROUPS its clause's input rows; an item
// holding none is a grouping term.
func containsAggregator(e Expr) bool {
	found := false
	walkExprAll(e, func(x Expr) {
		if fc, ok := x.(*FunctionCall); ok && aggregatorName(fc.Name) != "" {
			found = true
		}
	})
	return found
}

// --- RETURN ---

func (ex *executor) applyReturn(bindings []binding, r *Return) ([]ruleengine.ProjectionResult, error) {
	rows, err := ex.projectItems(bindings, r.Items, ex.redundantFor(r), ex.branchPlanFor(r))
	if err != nil {
		return nil, err
	}
	// Deduplicate rows when RETURN DISTINCT is specified; order is preserved
	// (first occurrence wins).
	//
	// Rows are compared by the same injective rendering the grouping path keys
	// on, NOT by json.Marshal. A node-valued column holds a *nodeRef, whose
	// fields are all unexported, so it marshals to `{}` — two rows differing
	// only in which node they bound would render identically and collapse into
	// one. normalizeForKey renders a node as its key, and every leaf carries a
	// type tag, so distinct rows stay distinct. It also cannot fail, which
	// retires a dropped marshal error on a path that has no way to report one.
	if r.Distinct {
		seen := make(map[string]struct{}, len(rows))
		deduped := rows[:0]
		for _, row := range rows {
			key := normalizeForKey(provStripped(row))
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				deduped = append(deduped, row)
			}
		}
		rows = deduped
	}
	out := make([]ruleengine.ProjectionResult, 0, len(rows))
	for _, row := range rows {
		values := map[string]any{}
		for k, v := range row {
			if k == provBindingKey {
				// The chain is the row's read record, not a projected column:
				// an adapter renders Values straight into the target document.
				continue
			}
			values[k] = v
		}
		// Build the projection key. When the rule's key columns are threaded
		// (from Rule.Into.Key at activation), build the complete multi-column
		// key — every key column is a RETURN alias, so its value is in the
		// projected row. This mirrors the simple engine and lets a composite-key
		// lens (e.g. a GrantTable lens keyed on actor_id/anchor_id/grant_source)
		// hand the adapter every key column it requires. With no key columns
		// threaded, fall back to the first RETURN item — byte-identical to the
		// single-key lenses that have always used this path.
		keyMap := map[string]any{}
		if len(ex.keyColumns) > 0 {
			for _, col := range ex.keyColumns {
				keyMap[col] = values[col]
			}
		} else if len(r.Items) > 0 {
			alias := r.Items[0].Alias
			if alias == "" {
				alias = projectionAutoAlias(r.Items[0].Expr, 0)
			}
			keyMap[alias] = values[alias]
		}
		out = append(out, ruleengine.ProjectionResult{
			Key:        keyMap,
			Values:     values,
			Provenance: ex.provVertexKeys(provChain(row)),
		})
	}
	return out, nil
}

// isNullBound reports whether v is Cypher NULL: a nil interface, or a node
// variable's OPTIONAL-MATCH null sentinel ((*nodeRef)(nil)) — a bare `v ==
// nil` misses the latter because a typed nil pointer boxed in `any` is
// itself a non-nil interface value.
func isNullBound(v any) bool {
	if v == nil {
		return true
	}
	if ref, ok := v.(*nodeRef); ok {
		return ref == nil
	}
	return false
}

// nanoIDFromVertexKey extracts the bare NanoID (the <id> segment) from a
// vtx.<type>.<id> vertex key, fail-closed: only a well-formed vertex key —
// exactly three dot-segments with a leading "vtx" and non-empty type + id —
// yields a NanoID. An aspect key (vtx.<type>.<id>.<localName>, four segments),
// a link key (lnk.…, six segments), or any malformed string ERRORS rather than
// returning a wrong token, so an auth-plane lens can never project an anchor
// that matches the wrong resource. (Contract #1 §1.1 vertex key shape.)
func nanoIDFromVertexKey(s string) (string, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] != "vtx" || parts[1] == "" || parts[2] == "" {
		return "", fmt.Errorf("full engine: nanoIdFromKey requires a vtx.<type>.<id> vertex key, got %q", s)
	}
	return parts[2], nil
}

// levenshteinDistance computes the classical Wagner-Fischer edit distance
// between strings a and b. Uses a rolling-row approach for O(min(|a|,|b|))
// space. Cost: insert=1, delete=1, substitute=1. Operates over runes so
// multi-byte UTF-8 sequences count as single characters.
func levenshteinDistance(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	n, m := len(ra), len(rb)
	if n == 0 {
		return m
	}
	prev := make([]int, n+1)
	curr := make([]int, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = j
	}
	for i := 1; i <= m; i++ {
		curr[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if rb[i-1] == ra[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = del
			if ins < curr[j] {
				curr[j] = ins
			}
			if sub < curr[j] {
				curr[j] = sub
			}
		}
		prev, curr = curr, prev
	}
	return prev[n]
}

// evalPatternComprehension implements `[(x)-[:t]->(y) | projection]`.
// It re-walks the pattern starting from the current binding, evaluating the
// projection expression for each match and returning the resulting list.
func (ex *executor) evalPatternComprehension(b binding, pc *PatternComprehension) (any, error) {
	matches, err := ex.matchPath(b, pc.Pattern)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(matches))
	for _, m := range matches {
		if pc.Where != nil {
			v, err := ex.evalExpr(m, pc.Where)
			if err != nil {
				return nil, err
			}
			if !truthy(v) {
				continue
			}
		}
		v, err := ex.evalExpr(m, pc.Projection)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// existsAsPredicate returns true if pattern has at least one match starting
// from the current binding. Used by NOT (path) anti-pattern WHERE.
func (ex *executor) existsAsPredicate(b binding, p PathPattern) (bool, error) {
	matches, err := ex.matchPath(b, p)
	if err != nil {
		return false, err
	}
	return len(matches) > 0, nil
}

// --- helpers ---

// cloneBinding copies b's variables into a new binding and gives it a
// provenance chain of its own, descending from b's. The child inherits
// everything b has read by POINTER — the head's candidate set is stored once
// however many rows come off it — while what the child goes on to read stays
// off its siblings.
func cloneBinding(b binding) binding {
	nb := make(binding, len(b))
	for k, v := range b {
		nb[k] = v
	}
	if parent, chained := b[provBindingKey].(*provNode); chained {
		nb[provBindingKey] = &provNode{parent: parent}
	}
	return nb
}

// walkExprAll applies f to every expression node reachable from root.
// Independent of the test-only walker in parse_test.go so production code
// doesn't depend on test helpers.
func walkExprAll(root Expr, f func(Expr)) {
	if root == nil {
		return
	}
	f(root)
	switch e := root.(type) {
	case *AndOr:
		for _, op := range e.Operands {
			walkExprAll(op, f)
		}
	case *Not:
		walkExprAll(e.Operand, f)
	case *BinaryOp:
		walkExprAll(e.Left, f)
		walkExprAll(e.Right, f)
	case *PropertyAccess:
		walkExprAll(e.Target, f)
	case *FunctionCall:
		for _, a := range e.Args {
			walkExprAll(a, f)
		}
	case *MapLiteral:
		for _, k := range e.Keys {
			walkExprAll(e.Values[k], f)
		}
	case *ListLiteral:
		for _, el := range e.Elements {
			walkExprAll(el, f)
		}
	case *PatternComprehension:
		walkExprAll(e.Where, f)
		walkExprAll(e.Projection, f)
	case *CaseExpr:
		for _, alt := range e.Alternatives {
			walkExprAll(alt.When, f)
			walkExprAll(alt.Then, f)
		}
		walkExprAll(e.Else, f)
	}
}
