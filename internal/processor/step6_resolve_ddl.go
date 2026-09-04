package processor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// maxInstanceOfHops bounds the instanceOf-chain walk in the step-6 write-gate's
// governing-DDL resolution (Contract #1 §1.5). The deepest real domain chain is
// two hops (instance → template → type); the bound + a visited-set cycle guard
// keep the walk terminating and abuse-proof for crafted link cycles. Exceeding
// the bound yields "no governing DDL" → the §1.5 step-5 permissive default
// (fail-open to today's behavior, never into a wrong DDL).
const maxInstanceOfHops = 4

const instanceOfRelation = "instanceOf"

// instanceOfEdge is one resolved instanceOf link: its 6-segment link key and the
// 3-segment vertex key it points at. A vertex is expected to carry at most one
// live instanceOf (Contract #1 §1.5 design assumption); more than one is
// ambiguous and resolves to the permissive default (design §9 F1), never a
// guessed pick. Edges are kept link-key-sorted for stable, retry-identical logs.
type instanceOfEdge struct {
	linkKey string
	target  string
}

// errInstanceOfLiveReadBudgetExceeded is returned by the on-demand instanceOf
// readers when charging their round trips against the shared live-read budget
// (Contract #2 §2.5 "What the ceiling does not cover") pushes the execution
// over it. It reaches instanceOfTargetOf / classOf as an ordinary read error:
// the walk stops at that hop and the resolution comes back empty with the fault
// recorded — never a wrong DDL, never a partial resolution. Whether the empty
// answer is then the permissive default or a retryable refusal is the caller's
// disposition (resolveGoverningDDL vs the Checked/Committed variants).
var errInstanceOfLiveReadBudgetExceeded = errors.New("step 6 instanceOf: live-read budget exceeded")

// instanceOfTargetReader enumerates a vertex's live instanceOf-link targets from
// committed Core KV — the on-demand fallback used only when the link is in
// neither the in-flight batch nor the hydrated working set. A single bounded
// `lnk.<root>.instanceOf.>` prefix list (source-anchored, so the read is bounded
// by construction: the source segments sit in the prefix). Optional on the
// validator (nil ⇒ on-demand discovery is skipped; batch + working-set paths
// still resolve).
type instanceOfTargetReader interface {
	// LiveInstanceOfTargets returns every live instanceOf edge sourced at
	// vtxRoot, sorted by link key for a deterministic selection. Tombstoned,
	// unparseable, or (between list and get) hard-deleted links are skipped.
	// liveReads charges the prefix list + each per-key GET against the same
	// shared budget kv.Read/kv.Links draw from (nil-safe: unlimited).
	LiveInstanceOfTargets(ctx context.Context, vtxRoot string, liveReads *liveReadBudgetTracker) ([]instanceOfEdge, error)
}

// instanceOfConnReader is the narrow substrate.Conn surface connInstanceOfReader
// needs. Named so a test can fake it with a call counter without an embedded
// NATS server. *substrate.Conn satisfies it structurally.
type instanceOfConnReader interface {
	KVGetMulti(ctx context.Context, bucket string, keys []string) (map[string]*substrate.KVEntry, error)
}

// connInstanceOfReader is the production instanceOfTargetReader, backed by the
// Processor's substrate connection. It reads the source-anchored
// `lnk.<root>.instanceOf.>` wildcard in ONE atomic KVGetMulti call — safe here
// (unlike kv.Links' paged enumeration) because there is no paging contract to
// honor and the set is structurally tiny: a vertex carries at most one live
// instanceOf by design (§9 F1's ambiguity guard is what makes more than one a
// no-op, not a crash). Tombstones are honored from the returned envelope.
type connInstanceOfReader struct {
	conn       instanceOfConnReader
	coreBucket string
}

func (r *connInstanceOfReader) LiveInstanceOfTargets(ctx context.Context, vtxRoot string, liveReads *liveReadBudgetTracker) ([]instanceOfEdge, error) {
	vt, id, ok := substrate.ParseVertexKey(vtxRoot)
	if !ok {
		return nil, nil
	}
	if !liveReads.charge(1) {
		return nil, errInstanceOfLiveReadBudgetExceeded
	}
	filter := "lnk." + vt + "." + id + "." + instanceOfRelation + ".>"
	entries, err := r.conn.KVGetMulti(ctx, r.coreBucket, []string{filter})
	if err != nil {
		return nil, err
	}
	if !liveReads.charge(len(entries)) {
		return nil, errInstanceOfLiveReadBudgetExceeded
	}
	var edges []instanceOfEdge
	for k, entry := range entries {
		_, _, _, t2, id2, ok := substrate.ParseLinkKey(k)
		if !ok {
			continue
		}
		var d struct {
			IsDeleted bool `json:"isDeleted"`
		}
		// A malformed/unparseable link envelope is skipped (treated as no link),
		// not trusted as live — fail-open to the permissive default, never
		// resolve a corrupt edge into a governing DDL.
		if uerr := json.Unmarshal(entry.Value, &d); uerr != nil || d.IsDeleted {
			continue
		}
		edges = append(edges, instanceOfEdge{linkKey: k, target: substrate.VertexKey(t2, id2)})
	}
	sortEdges(edges)
	return edges, nil
}

// ddlResolutionMemo caches the on-demand answers of the governing-DDL walk per
// walk node within one execution (Fire 1 Inc 2b), collapsing G10's defect: a
// fresh ddlResolver per read otherwise re-walks the SAME shared nodes (e.g. a
// template vertex every sibling aspect's chain passes through) once per read.
//
// Two answers are cached, one per on-demand reader, under the same discipline:
//
//   - edges — what LiveInstanceOfTargets returned for a node ("what does this
//     vertex point at").
//   - classes — what the vertexClassReader returned for a node ("what class
//     does this vertex carry"), the read classOf makes to recognise a chain
//     terminal whose own class is a registered DDL. A batch of mutations whose
//     stored classes chain to ONE terminal asks that terminal its class once
//     per mutation; without this each asks Core KV again.
//
// Both cache ONLY the live-read layer's RAW answer — before any in-flight-batch
// exclusion or re-typing — never the batch/working-set layers or the final
// disposition: those are re-derived from THIS call's result.Mutations every
// time, memo hit or not (instanceOfTargetOf and classOf below), so a batch
// tombstone or re-typing landing after the memo warms is still honored. A
// FAULT is never cached by either: a transient read error must not calcify into
// a permanent answer for the rest of the execution.
//
// Keyed on the walk node (the vertex being asked), not on the class or the read
// that started the walk: two vertices of the same class can resolve differently
// if their own edges differ, and the root varies per read (defeating reuse)
// while an intermediate/terminal node is what siblings actually share.
//
// Lifetime: created lazily on the first on-demand resolution in an
// execution; owned by ScriptContext (never the resolver, which may outlive
// one execution); never reset within an execution; never carried across
// executions, a redelivery, or a derive_reads pre-pass into the main pass —
// one ScriptContext, one memo, it dies with the context. Nil-safe = "no
// memoization", the same convention as liveReadBudgetTracker.
type ddlResolutionMemo struct {
	edges   map[string][]instanceOfEdge
	classes map[string]memoizedClass
}

// memoizedClass is one node's committed class as the on-demand reader answered
// it. ok=false is a real answer — the key holds nothing, or holds a tombstoned
// or unparseable body — and is memoized as such.
type memoizedClass struct {
	class string
	ok    bool
}

// get returns the memoized live-read edges for vtxRoot and whether this
// execution has already resolved that node on-demand — found=false means
// "never asked Core KV this execution", NOT "asked and got nothing" (that is
// found=true with a nil/empty slice, a memoized negative).
func (m *ddlResolutionMemo) get(vtxRoot string) (edges []instanceOfEdge, found bool) {
	if m == nil {
		return nil, false
	}
	edges, found = m.edges[vtxRoot]
	return edges, found
}

// set records vtxRoot's live-read edges, including nil/empty (a node with no
// live instanceOf link), so a later hop that reaches the same node this
// execution does not re-pay the round trip.
func (m *ddlResolutionMemo) set(vtxRoot string, edges []instanceOfEdge) {
	if m == nil {
		return
	}
	if m.edges == nil {
		m.edges = make(map[string][]instanceOfEdge)
	}
	m.edges[vtxRoot] = edges
}

// getClass returns the memoized committed class of vtxKey and whether this
// execution has already asked Core KV for it. found=false means "never asked",
// never "asked and the key was empty" — that is found=true carrying ok=false.
func (m *ddlResolutionMemo) getClass(vtxKey string) (answer memoizedClass, found bool) {
	if m == nil {
		return memoizedClass{}, false
	}
	answer, found = m.classes[vtxKey]
	return answer, found
}

// setClass records vtxKey's committed class, including the negative (a key that
// holds nothing, or a body that is tombstoned or unparseable), so a later
// mutation whose chain reaches the same terminal does not re-pay the round trip.
func (m *ddlResolutionMemo) setClass(vtxKey string, answer memoizedClass) {
	if m == nil {
		return
	}
	if m.classes == nil {
		m.classes = make(map[string]memoizedClass)
	}
	m.classes[vtxKey] = answer
}

// ddlResolver carries the DDL cache + the on-demand instanceOf reader behind
// the shared governing-DDL resolution below. Step 6 (ValidatorImpl embeds
// one), step 6.5's encrypt, and decrypt-on-read each hold their own
// ddlResolver so a sensitive class resolvable only via the instanceOf chain —
// not step 6's exact class→DDL lookup — is recognized identically by every
// path that needs to know a mutation or document is sensitive, not just the
// write-scope gate.
type ddlResolver struct {
	DDLs *DDLCache
	// linkReader is the on-demand fallback for resolving a vertex's instanceOf
	// target from committed Core KV (Contract #1 §1.5 governing-DDL walk). Nil
	// ⇒ on-demand discovery is skipped (the batch + working-set paths still
	// resolve, when the caller has any of those to offer).
	linkReader instanceOfTargetReader
	// classReader is the on-demand fallback classOf uses to learn a chain
	// terminal's own class from committed Core KV. Deliberately NOT a
	// ScriptKVReader/decrypt-aware read: a vertex's class field is never
	// encrypted (only its data is), and routing this through
	// decryptSensitiveDoc would let a chain terminal that is ITSELF
	// unresolved-by-exact-lookup recurse back into resolveGoverningDDL —
	// unbounded across a mutual instanceOf cycle, since each nested call gets
	// its own fresh hop/visited bound with no shared depth guard. Nil ⇒
	// on-demand discovery is skipped (the batch + working-set paths still
	// resolve).
	classReader vertexClassReader
	// Logger receives a warning on an on-demand read fault (fail-open to the
	// permissive default). Nil is safe — slog.Default() is used instead.
	Logger *slog.Logger
}

// vertexClassReader reads only a committed vertex's class field — the
// minimal live-read classOf needs, never the full decrypt-aware VertexDoc.
// liveReads charges the GET against the same shared budget kv.Read/kv.Links
// draw from (nil-safe: unlimited).
type vertexClassReader interface {
	ClassOf(ctx context.Context, key string, liveReads *liveReadBudgetTracker) (class string, ok bool, err error)
}

// connVertexClassReader is the production vertexClassReader, backed by the
// Processor's substrate connection. A single key GET, no decrypt — the class
// field is plaintext regardless of whether the document is sensitive.
type connVertexClassReader struct {
	conn       *substrate.Conn
	coreBucket string
}

func (r *connVertexClassReader) ClassOf(ctx context.Context, key string, liveReads *liveReadBudgetTracker) (string, bool, error) {
	if !liveReads.charge(1) {
		return "", false, errInstanceOfLiveReadBudgetExceeded
	}
	entry, err := r.conn.KVGet(ctx, r.coreBucket, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	var d struct {
		Class     string `json:"class"`
		IsDeleted bool   `json:"isDeleted"`
	}
	if uerr := json.Unmarshal(entry.Value, &d); uerr != nil || d.IsDeleted {
		return "", false, nil
	}
	return d.Class, true, nil
}

func (r *ddlResolver) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// resolveDisposition selects which layers the governing-DDL walk may consult.
//
//   - resolveWithBatch is the walk over the world the batch is proposing: the
//     in-flight mutations are the authoritative layer (a batch tombstone of a
//     link suppresses the same link committed below), then the hydrated working
//     set, then a bounded on-demand Core KV read. It governs the class a
//     document DECLARES — for a create, the batch is the only place that class's
//     type authority can exist.
//   - resolveCommittedOnly is the walk over the world as STORED: the in-flight
//     layer is skipped entirely, in both the instanceOf walk and the
//     class-of-terminal lookup, so neither a batch tombstone of a vertex's own
//     instanceOf link nor a batch re-typing of a chain terminal can un-type the
//     entity for the gate. It governs the class stored at an update's or
//     tombstone's key, whose type authority is a fact about the committed graph
//     and not a proposal the same batch can move. A meta-rooted key resolves by
//     the exact class lookup alone under this disposition — see resolveWithFault.
//
// Both dispositions share one ddlResolutionMemo: it caches only the raw
// live-read answer per walk node, before any in-flight exclusion, so an entry
// warmed under either disposition answers the same question for the other.
type resolveDisposition uint8

const (
	resolveWithBatch resolveDisposition = iota
	resolveCommittedOnly
)

// resolveGoverningDDL resolves the DDL that gates a mutation's write
// (Contract #1 §1.5). It first tries the exact class→DDL lookup (today's fast
// path, unchanged); on a miss it walks the mutation's vertex root up its
// instanceOf chain to the nearest type-authority DDL, so a fine-grained
// dotted discriminator class is governed by its shared type DDL (the type
// authority the chain reaches) with zero per-subtype DDLs. A miss on both →
// (zero, false) → the caller applies the §1.5 step-5 permissive default.
//
// The walk is read-lazy: in-flight batch first, then the hydrated working set,
// then (only if both miss) a single bounded on-demand Core KV read. For every
// coarse-class vertex shipping today the exact lookup wins first, so the walk —
// and any read — never runs.
func (r *ddlResolver) resolveGoverningDDL(ctx context.Context, class, key string, kind substrate.KeyKind, result ScriptResult, state HydratedState) (MetaVertexRef, bool) {
	ref, ok, _ := r.resolveGoverningDDLChecked(ctx, class, key, kind, result, state)
	return ref, ok
}

// resolveGoverningDDLCommitted resolves the DDL that gates a mutation's write
// from the committed graph alone (resolveCommittedOnly), reporting a live-read
// fault the way resolveGoverningDDLChecked does. It is how a stored class —
// the class of the document an update or tombstone rewrites or removes — finds
// its type authority: the entity as stored has one, and the same batch must not
// be able to move it.
func (r *ddlResolver) resolveGoverningDDLCommitted(ctx context.Context, class, key string, kind substrate.KeyKind, result ScriptResult, state HydratedState) (MetaVertexRef, bool, error) {
	var fault error
	ref, ok := r.resolveWithFault(ctx, class, key, kind, result, state, resolveCommittedOnly, &fault)
	return ref, ok, fault
}

// resolveGoverningDDLChecked is resolveGoverningDDL plus the one thing the
// permissive default deliberately hides: whether resolution was DEGRADED by a
// live-read fault rather than genuinely finding no governing DDL. Both
// outcomes return ok=false, and for step 6 that conflation is correct — a read
// fault must degrade to today's permissive behavior, never resolve into a
// wrong DDL.
//
// It is NOT correct for step 6.5, which re-resolves the SAME mutation against
// the SAME shared live-read budget step 6 just spent from. Step 6 can resolve
// a class as sensitive, pass its anchoring check, and leave the budget
// exhausted; step 6.5's identical call then faults, reads ok=false as "not
// sensitive", and commits the aspect as PLAINTEXT. Two steps disagreeing about
// sensitivity fails open in the one direction this plane cannot tolerate, so
// step 6.5 takes the error and fails the operation instead.
func (r *ddlResolver) resolveGoverningDDLChecked(ctx context.Context, class, key string, kind substrate.KeyKind, result ScriptResult, state HydratedState) (MetaVertexRef, bool, error) {
	var fault error
	ref, ok := r.resolveWithFault(ctx, class, key, kind, result, state, resolveWithBatch, &fault)
	return ref, ok, fault
}

func (r *ddlResolver) resolveWithFault(ctx context.Context, class, key string, kind substrate.KeyKind, result ScriptResult, state HydratedState, disp resolveDisposition, fault *error) (MetaVertexRef, bool) {
	if ref, ok := r.DDLs.Lookup(class); ok {
		return ref, true // exact match — unchanged Contract #1 §1.5 step-3 path
	}

	root := vertexRootForResolve(key, kind)
	if root == "" {
		return MetaVertexRef{}, false // links / unparseable → permissive default (today's behavior)
	}
	if disp == resolveCommittedOnly && isKernelTypedVertexKey(root) {
		// A kernel-typed vertex is typed by the kernel, not by an instanceOf
		// edge, so the walk can only ever come back empty — while a package
		// uninstall, a package upgrade or a meta-vertex tombstone cascade, all
		// bulk and all made almost entirely of these types, pays one KVGetMulti
		// per distinct root for the privilege. Short-circuiting leaves them the
		// exact class lookup alone, the same way a link key takes the early
		// return above.
		//
		// That premise is a closed-set census over the whole corpus, run in CI,
		// not an assertion: internal/pkgmgr's
		// TestPackages_NeverEmitAMetaRootedInstanceOfLink builds every
		// registered package's install batch through the installer's own
		// builder, and internal/bootstrap's
		// TestPrimordialEntries_NeverEmitAMetaRootedInstanceOfLink covers the
		// seeder. Both range over the whole kernel-typed set, not meta alone. A
		// package that ever needs such an edge fails them before it reaches this
		// branch.
		//
		// The bound this leaves standing: a BUSINESS-vertex cascade still pays
		// at most one KVGetMulti per distinct un-registered root, plus one class
		// read per chain terminal, both memoised on the shared
		// DDLResolutionMemo. The reads are serial inside validateOne and off the
		// script's live-read budget, bounded by substrate.MaxBatchMessages (the
		// batch can hold no more roots than that) and by the lane deadline.
		return MetaVertexRef{}, false
	}

	visited := map[string]bool{root: true}
	cur := root
	for hop := 0; hop < maxInstanceOfHops; hop++ {
		target, ok := r.instanceOfTargetOf(ctx, cur, result, state, disp, fault)
		if !ok {
			break // no instanceOf link → no type authority
		}
		if visited[target] {
			break // cycle guard → permissive default
		}
		visited[target] = true

		if isMetaVertexKey(target) {
			// Terminal: the target IS a DDL meta-vertex. Only a vertexType DDL
			// is a legitimate governing authority (an aspect/link/event DDL is
			// not a write-gate type).
			if ref, ok := r.DDLs.LookupByMetaKey(target); ok && ref.Kind == "vertexType" {
				return ref, true
			}
			break
		}

		// A business vertex whose own class is itself a registered DDL is also a
		// terminal (the one-hop instance→type domain shape).
		tclass, ok, err := r.classOf(ctx, target, result, state, disp)
		if err != nil {
			// The walk STOPS here, exactly as it stops on an instanceOf read
			// fault. This hop's node may itself be the type authority — a
			// terminal whose permittedCommands refuses the operation — and its
			// class is what says so. Walking past it would let a FARTHER
			// authority, which may admit what this one refuses, decide the
			// gate's verdict on the strength of a read that failed: fail-open,
			// and non-deterministically so. The fault is recorded, the
			// resolution comes back empty, and the caller that cannot tolerate
			// a degraded answer (validateStoredClass, resolveDeclaredClass)
			// turns it into a retryable refusal.
			recordResolveFault(fault, err)
			r.logger().Warn("step 6: committed class read failed; stopping the governing-DDL walk",
				"vtxKey", target, "error", err)
			return MetaVertexRef{}, false
		}
		if ok {
			if ref, ok := r.DDLs.Lookup(tclass); ok {
				return ref, true
			}
		}

		cur = target // keep walking (instance → template → type)
	}

	return MetaVertexRef{}, false
}

// instanceOfTargetOf returns the live instanceOf target of vtxRoot. A vertex is
// expected to carry exactly one live instanceOf (Contract #1 §1.5 design
// assumption); the resolving layer therefore resolves only when it holds
// **exactly one** live edge. Multiple live edges are **ambiguous → no
// resolution** (the caller applies the permissive default) — mirroring the
// `ClassForCommand` ambiguity guard (design §9 F1: never pick the admitting DDL
// when the type authority is ambiguous, so an extra instanceOf link cannot steer
// the gate). The in-flight batch is the authoritative layer (last op per link
// key wins; a tombstone in the batch suppresses the same link committed below);
// then the hydrated working set, then a single bounded on-demand Core KV read.
//
// Under resolveCommittedOnly the in-flight layer is skipped and its tombstones
// suppress nothing: the caller is asking what the entity's type authority IS,
// not what this batch proposes it become.
func (r *ddlResolver) instanceOfTargetOf(ctx context.Context, vtxRoot string, result ScriptResult, state HydratedState, disp resolveDisposition, fault *error) (string, bool) {
	var batchDead map[string]bool
	if disp == resolveWithBatch {
		var batchLive []instanceOfEdge
		batchLive, batchDead = reconcileBatchInstanceOf(vtxRoot, result.Mutations)
		if len(batchLive) > 0 {
			return soleTarget(batchLive)
		}
	}
	if edges := workingSetInstanceOfEdges(vtxRoot, state.Context.Hydrated, batchDead); len(edges) > 0 {
		return soleTarget(edges)
	}
	// Fire 1 Inc 2b: a memo hit skips ONLY the Core KV round trip below — the
	// batch/working-set layers above already ran fresh for THIS call, and the
	// batchDead exclusion below re-runs against THIS call's batch too, so a
	// tombstone the batch adds after the memo warms is still honored.
	memo := state.Context.DDLResolutionMemo
	if edges, found := memo.get(vtxRoot); found {
		if edges = excludeDead(edges, batchDead); len(edges) > 0 {
			return soleTarget(edges)
		}
		return "", false
	}
	if r.linkReader != nil {
		edges, err := r.linkReader.LiveInstanceOfTargets(ctx, vtxRoot, state.Context.LiveReads)
		if err != nil {
			// Fail-open to the permissive default — never resolve into a wrong
			// DDL on a read fault. A read error degrades to today's behavior.
			// The fault is RECORDED, not acted on, so a caller that cannot
			// tolerate a degraded resolution (step 6.5) can tell this apart
			// from a genuine "no DDL". NOT memoized: a transient fault must
			// not calcify into a permanent negative for the rest of the
			// execution — the next hop that reaches vtxRoot should retry.
			recordResolveFault(fault, err)
			r.logger().Warn("step 6: instanceOf on-demand read failed; resolving to permissive default",
				"vtxRoot", vtxRoot, "error", err)
			return "", false
		}
		// Memoize the RAW read (pre-exclusion) — the answer Core KV gave for
		// this node — not the post-batchDead disposition, so a later call
		// with a different batch still filters correctly against its own.
		memo.set(vtxRoot, edges)
		if edges = excludeDead(edges, batchDead); len(edges) > 0 {
			return soleTarget(edges)
		}
	}
	return "", false
}

// soleTarget returns the single edge's target, or no resolution when the layer
// carries more than one live instanceOf (ambiguous type authority → permissive
// default per design §9 F1).
func soleTarget(edges []instanceOfEdge) (string, bool) {
	if len(edges) == 1 {
		return edges[0].target, true
	}
	return "", false
}

// reconcileBatchInstanceOf folds the in-flight mutations into the net instanceOf
// state of vtxRoot: last op per link key wins, so a create-then-tombstone (or a
// tombstone alone) of a link leaves it dead. Returns the net-live edges and the
// set of link keys the batch tombstoned (which must be suppressed in the
// committed/working-set layers — the batch is the in-flight truth).
func reconcileBatchInstanceOf(vtxRoot string, muts []MutationOp) (live []instanceOfEdge, dead map[string]bool) {
	vt, id, ok := substrate.ParseVertexKey(vtxRoot)
	if !ok {
		return nil, nil
	}
	liveByKey := map[string]instanceOfEdge{}
	dead = map[string]bool{}
	for _, m := range muts {
		t1, id1, name, t2, id2, ok := substrate.ParseLinkKey(m.Key)
		if !ok || name != instanceOfRelation || t1 != vt || id1 != id {
			continue
		}
		if mutationTombstoned(m) {
			delete(liveByKey, m.Key)
			dead[m.Key] = true
			continue
		}
		delete(dead, m.Key)
		liveByKey[m.Key] = instanceOfEdge{linkKey: m.Key, target: substrate.VertexKey(t2, id2)}
	}
	for _, e := range liveByKey {
		live = append(live, e)
	}
	sortEdges(live)
	return live, dead
}

// workingSetInstanceOfEdges collects the live instanceOf edges sourced at
// vtxRoot among the hydrated reads, excluding any link the batch tombstoned.
func workingSetInstanceOfEdges(vtxRoot string, hydrated map[string]VertexDoc, dead map[string]bool) []instanceOfEdge {
	vt, id, ok := substrate.ParseVertexKey(vtxRoot)
	if !ok {
		return nil
	}
	prefix := "lnk." + vt + "." + id + "." + instanceOfRelation + "."
	var edges []instanceOfEdge
	for k, doc := range hydrated {
		if !strings.HasPrefix(k, prefix) || doc.IsDeleted || dead[k] {
			continue
		}
		if _, _, _, t2, id2, ok := substrate.ParseLinkKey(k); ok {
			edges = append(edges, instanceOfEdge{linkKey: k, target: substrate.VertexKey(t2, id2)})
		}
	}
	sortEdges(edges)
	return edges
}

// excludeDead drops edges whose link key the batch tombstoned. Allocates a
// fresh slice rather than compacting in place (Fire 1 Inc 2b review finding):
// a caller may hold the same backing array elsewhere — ddlResolutionMemo
// stores the exact slice a live read returns, and an in-place compaction here
// would silently corrupt the memoized entry for every later call, including
// ones with a DIFFERENT (or no) batch tombstone.
func excludeDead(edges []instanceOfEdge, dead map[string]bool) []instanceOfEdge {
	if len(dead) == 0 {
		return edges
	}
	out := make([]instanceOfEdge, 0, len(edges))
	for _, e := range edges {
		if !dead[e.linkKey] {
			out = append(out, e)
		}
	}
	return out
}

// sortEdges orders edges by link key for deterministic, retry-stable selection.
func sortEdges(edges []instanceOfEdge) {
	sort.Slice(edges, func(i, j int) bool { return edges[i].linkKey < edges[j].linkKey })
}

// classOf resolves the class of the vertex at targetKey, preferring the batch,
// then the working set, then the execution's memo, then a single on-demand Core
// KV read. Used to detect a terminal whose own class is itself a registered DDL.
//
// Within the batch the LAST mutation on the key wins, which is why the scan
// runs to the end rather than returning at its first match: a batch carrying
// two writes to one key commits only the last of them (substrate/batch.go's
// duplicate-key semantics), so classifying by the first reads a class that
// never reaches Core KV — and a decoy create placed ahead of the real update
// would steer the governing-DDL walk at will. reconcileBatchInstanceOf folds
// the link side of this same walk under exactly that rule.
// Under resolveCommittedOnly the batch scan is skipped: a terminal the batch
// re-types or removes still answers with the class it carries as stored, so a
// stored class's authority is the committed one at every hop of the walk.
//
// A read fault is REPORTED, not swallowed: the caller stops the walk on it. The
// memo is warmed only by an answer the reader actually gave, so a fault leaves
// the node unmemoized and the next resolution that reaches it retries.
func (r *ddlResolver) classOf(ctx context.Context, targetKey string, result ScriptResult, state HydratedState, disp resolveDisposition) (string, bool, error) {
	var last *MutationOp
	if disp == resolveWithBatch {
		for i := range result.Mutations {
			m := &result.Mutations[i]
			if m.Key != targetKey {
				continue
			}
			switch m.Op {
			case "create", "update", "tombstone":
				last = m
			}
		}
	}
	if last != nil {
		if mutationTombstoned(*last) {
			// The batch removes the vertex, so after commit it carries no class
			// at all and must not resolve through the committed layers below —
			// the in-flight batch is the truth, the same way batchDead
			// suppresses a committed link the batch tombstoned.
			return "", false, nil
		}
		if c, ok := last.Document["class"].(string); ok {
			return c, true, nil
		}
		// A write that restates no class leaves the committed one standing, so
		// fall through to the layers below rather than call the vertex classless.
	}
	if doc, ok := state.Context.Hydrated[targetKey]; ok && !doc.IsDeleted {
		return doc.Class, true, nil
	}
	// A memo hit skips ONLY the Core KV round trip below — the batch and
	// working-set layers above already ran fresh for THIS call, so a batch
	// re-typing added after the memo warms still wins over the memoized
	// committed answer.
	memo := state.Context.DDLResolutionMemo
	if answer, found := memo.getClass(targetKey); found {
		return answer.class, answer.ok, nil
	}
	if r.classReader != nil {
		class, ok, err := r.classReader.ClassOf(ctx, targetKey, state.Context.LiveReads)
		if err != nil {
			return "", false, err
		}
		memo.setClass(targetKey, memoizedClass{class: class, ok: ok})
		if ok {
			return class, true, nil
		}
	}
	return "", false, nil
}

// recordResolveFault keeps the FIRST live-read fault of a resolution. First,
// not last: it is the one that truncated the walk, so it is the one that
// explains why the resolution came back empty. Nil-safe — every caller that
// does not care passes nil.
func recordResolveFault(fault *error, err error) {
	if fault != nil && *fault == nil {
		*fault = err
	}
}

// vertexRootForResolve derives the 3-segment vertex root whose instanceOf chain
// governs a mutation. A vertex mutation roots at itself; an aspect mutation at
// its parent vertex. Link/unknown mutations have no instanceOf-governed root —
// they fall through to the permissive default exactly as today (a link carries
// its own link class, never a fine-grained vertex discriminator).
func vertexRootForResolve(key string, kind substrate.KeyKind) string {
	switch kind {
	case substrate.KindVertex:
		return key
	case substrate.KindAspect:
		if vk, _, _, _, ok := substrate.ParseAspectKey(key); ok {
			return vk
		}
	}
	return ""
}

// isMetaVertexKey reports whether key is a DDL meta-vertex (vtx.meta.<NanoID>).
func isMetaVertexKey(key string) bool {
	return strings.HasPrefix(key, "vtx.meta.")
}

// kernelVertexTypes are the vertex types the kernel owns outright: a DDL
// meta-vertex, and the three authorization entities the package-lifecycle
// primitives write and no DDL governs (step 6 resolves no DDL for class
// "permission", "role" or "roleindex" — rejectPermissionRoleRewrites is their
// gate). Every one is seeded or written once by the platform and never subtyped
// through an instanceOf edge, which is what lets the committed-only walk skip
// them; the two corpus censuses hold that closed.
var kernelVertexTypes = map[string]struct{}{
	"meta":       {},
	"permission": {},
	"role":       {},
	"roleindex":  {},
}

// isKernelTypedVertexKey reports whether key is a 3-segment vertex root of a
// kernel-owned type.
func isKernelTypedVertexKey(key string) bool {
	seg := strings.Split(key, ".")
	if len(seg) != 3 || seg[0] != "vtx" {
		return false
	}
	_, kernel := kernelVertexTypes[seg[1]]
	return kernel
}

// mutationTombstoned reports whether a mutation removes (or carries a removed)
// document — a tombstoned link/vertex is no link/vertex for resolution.
func mutationTombstoned(m MutationOp) bool {
	if m.Op == "tombstone" {
		return true
	}
	if m.Document != nil {
		if del, ok := m.Document["isDeleted"].(bool); ok && del {
			return true
		}
	}
	return false
}
