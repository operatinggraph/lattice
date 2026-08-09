package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// subtypeOfRelation is the link localName a DDLSpec.SubtypeOfRef declaration
// emits (dynamic-type-taxonomy-design.md §3.3): `lnk.meta.<leafId>.subtypeOf
// .meta.<parentId>`, leaf -> parent per Contract #1 §1.1 (the later-arriving
// vertex is the source).
const subtypeOfRelation = "subtypeOf"

// maxTaxonomyDepth bounds the install-time upward subtypeOf walk, mirroring
// maxInstanceOfHops's stated rationale (internal/processor/step6_resolve_ddl.go:20):
// a bound plus a visited-path set keeps the walk terminating and abuse-proof
// against a crafted or accidentally very-long chain, exceeding which is an
// install error (never silently truncated).
const maxTaxonomyDepth = 4

// leafBudgetDefault is the narrowed-filter label cap (maxNarrowedFilterLabels,
// internal/refractor/ruleengine/full/pipeline.go) an abstract type's
// LeafBudget defaults to when the author leaves it unset (§10.2).
const leafBudgetDefault = 8

// ErrSubtypeOfRefUnresolved is returned by Install/Upgrade/Apply when a
// DDLSpec.SubtypeOfRef does not resolve to a live vertexType meta-vertex —
// neither batch-local nor already installed. Fail-closed per §3.5: unlike
// resolveLensRef's NanoID pass-through (build.go), an unresolvable
// SubtypeOfRef is never accepted verbatim. Per Andrew's ratification, the
// resolved target may be concrete OR abstract (§3.4 — "a concrete type may
// have subtypes"); only its CLASS is checked, which is what catches a ref
// that names a lens (or other non-vertexType meta-vertex) canonicalName by
// typo.
var ErrSubtypeOfRefUnresolved = errors.New("pkgmgr: SubtypeOfRef does not resolve to a live, installed vertexType meta-vertex")

// ErrTaxonomyCycle is returned when a DDL's subtypeOf declarations, combined
// with the taxonomy graph already installed, form a cycle or would require an
// upward walk deeper than maxTaxonomyDepth.
var ErrTaxonomyCycle = errors.New("pkgmgr: subtypeOf taxonomy graph is cyclic or exceeds maxTaxonomyDepth")

// needsTaxonomyScan reports whether def declares a SubtypeOfRef to resolve,
// or an Abstract declaration whose live-instance guard
// (checkAbstractNoLiveInstances) needs the bucket's key list. False for an
// ordinary def with neither field declared on any DDL, so buildManifestBatch
// performs zero extra Core KV reads for it.
func needsTaxonomyScan(def Definition) bool {
	for _, d := range def.DDLs {
		if d.SubtypeOfRef != "" || d.Abstract {
			return true
		}
	}
	return false
}

// resolveTaxonomy resolves every DDLSpec.SubtypeOfRef declared in def to its
// parent's meta-vertex NanoID (dynamic-type-taxonomy-design.md §3.5):
// batch-local first (a vertexType DDL this SAME Definition declares — Andrew
// ratified §3.4: a concrete type may have subtypes too, so batch-local
// resolution indexes every vertexType DDL, not only abstract ones), then
// against the already-installed kernel. It validates the resulting subtypeOf
// graph is acyclic and within maxTaxonomyDepth, and computes LeafBudget
// warnings (§10.2 — advisory only, never a rejection).
//
// scan is the caller's already-fetched Core KV key list + canonicalName
// index (buildManifestBatch fetches it lazily, at most once per call) — this
// function performs no KVListKeys of its own.
//
// A package's own previously-installed subtypeOf edges are EXCLUDED from the
// "already installed" half of the graph before merging: what this package
// declares NOW supersedes what it declared in a prior version, so an upgrade
// that legally re-parents its own taxonomy (e.g. inverting which of two
// types is the ancestor) is never rejected as a false cycle against its own
// stale edge, and never double-counted in the LeafBudget arithmetic either.
//
// Returns the resolved parent NanoID per DDL index (covering only DDLs that
// declared a non-empty SubtypeOfRef, for buildInstallBatch's subtypeOf link
// emission) and the LeafBudget warning strings (nil when nothing is at
// risk).
func (i *Installer) resolveTaxonomy(ctx context.Context, def Definition, scan metaScanResult) (map[int]string, []string, error) {
	ddlIDs := make([]string, len(def.DDLs))
	batchVertexTypeIDs := make(map[string]string, len(def.DDLs)) // canonicalName -> id, EVERY vertexType DDL this batch declares
	batchLeafBudgets := make(map[string]int, len(def.DDLs))
	thisPackageDDLIDs := make(map[string]bool, len(def.DDLs))
	for idx, d := range def.DDLs {
		id := entityNanoID(def.Name, "ddl:"+d.CanonicalName)
		ddlIDs[idx] = id
		thisPackageDDLIDs[id] = true
		class := d.Class
		if class == "" {
			class = ddlClassVertexType
		}
		if class == ddlClassVertexType {
			batchVertexTypeIDs[d.CanonicalName] = id
		}
		if d.Abstract {
			batchLeafBudgets[id] = d.LeafBudget
		}
	}

	resolved := make(map[int]string, len(def.DDLs))
	targetLeafBudget := map[string]int{}
	newChildrenByTarget := map[string]int{}

	for idx, d := range def.DDLs {
		if d.SubtypeOfRef == "" {
			continue
		}
		var targetID string
		var budget int
		if id, ok := batchVertexTypeIDs[d.SubtypeOfRef]; ok {
			targetID, budget = id, batchLeafBudgets[id]
		} else {
			extID, extBudget, err := i.resolveExternalSubtypeTarget(ctx, d.SubtypeOfRef, scan.names)
			if err != nil {
				return nil, nil, fmt.Errorf("pkgmgr: DDL[%d] %q: %w", idx, d.CanonicalName, err)
			}
			targetID, budget = extID, extBudget
		}
		resolved[idx] = targetID
		if _, ok := targetLeafBudget[targetID]; !ok {
			targetLeafBudget[targetID] = budget
		}
		newChildrenByTarget[targetID]++
	}

	if len(resolved) == 0 {
		return resolved, nil, nil
	}

	installedEdgesRaw, err := scanInstalledSubtypeOfEdgesFromKeys(ctx, i.Conn, scan.keys)
	if err != nil {
		return nil, nil, err
	}
	// Drop this package's OWN previously-installed edges — see the doc
	// comment above. Everything else (edges declared by every OTHER
	// package) still applies.
	installedEdges := make(map[string][]string, len(installedEdgesRaw))
	for child, parents := range installedEdgesRaw {
		if thisPackageDDLIDs[child] {
			continue
		}
		installedEdges[child] = parents
	}

	// Acyclicity + depth: build the merged graph (surviving installed edges +
	// this batch's own new edges) and walk EVERY node in it, not just this
	// batch's new leaves — an edge added ABOVE an already-installed node can
	// lengthen a chain through descendants this batch never touches
	// directly, and only a full re-walk catches that.
	merged := make(map[string][]string, len(installedEdges)+len(resolved))
	for child, parents := range installedEdges {
		merged[child] = append(merged[child], parents...)
	}
	for idx, targetID := range resolved {
		merged[ddlIDs[idx]] = append(merged[ddlIDs[idx]], targetID)
	}
	nodes := make([]string, 0, len(merged))
	for n := range merged {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		if err := walkTaxonomyNoCycle(n, merged, map[string]bool{n: true}, 0); err != nil {
			return nil, nil, err
		}
	}

	// LeafBudget warnings (§10.2 — NEVER a rejection). Counts the TRANSITIVE
	// concrete-descendant closure, matching what the runtime resolver
	// (internal/refractor/taxonomy, §4.2) actually expands a `*` label into.
	// A DIRECT-child count is exact only for a single-level taxonomy and
	// silently passes a multi-level one straight through: an abstract with
	// 4 direct (abstract) children of 4 leaves each has a direct-child
	// count of 4 but a runtime expansion of 16, which is the count that
	// actually meets maxNarrowedFilterLabels
	// (internal/refractor/pipeline/pipeline.go).
	// downwardEdges is parent -> children, the DOWNWARD mirror of merged
	// (child -> parents) above, built from the identical edge set — this
	// batch's own new edges (resolved) UNIONed with every surviving
	// installed edge (installedEdges, already filtered of this package's
	// own stale edges) — so the walk below sees exactly the graph the
	// acyclicity check just proved terminates within maxTaxonomyDepth.
	downwardEdges := make(map[string][]string, len(installedEdges)+len(resolved))
	for child, parents := range installedEdges {
		for _, p := range parents {
			downwardEdges[p] = append(downwardEdges[p], child)
		}
	}
	for idx, targetID := range resolved {
		downwardEdges[targetID] = append(downwardEdges[targetID], ddlIDs[idx])
	}
	batchAbstract := make(map[string]bool, len(def.DDLs))
	for idx, d := range def.DDLs {
		batchAbstract[ddlIDs[idx]] = d.Abstract
	}
	abstractCache := map[string]bool{}

	// A TRANSITIVE count means an ANCESTOR's budget can be pushed over by a
	// batch that never names the ancestor at all: package A declares
	// abstract root (LeafBudget 2); B parents abstract branch under root; C
	// installs three leaves under branch. C's own newChildrenByTarget
	// contains only branch — root's transitive count just grew to 3 too,
	// through a package that has no idea root exists. So every ANCESTOR of
	// a newly-parented node must be re-walked too, not only the node's own
	// immediate target — a direct child count would have been the
	// ancestor's own count for free at a single level, but a transitive one
	// is not. ancestorsOf walks merged (built above, child -> []parents,
	// the FULL installed+new graph, already proven acyclic and within
	// maxTaxonomyDepth by the walk immediately above) upward, so it
	// inherits that same bound.
	recheckTargets := map[string]bool{}
	for targetID := range newChildrenByTarget {
		recheckTargets[targetID] = true
		for _, ancestor := range ancestorsOf(targetID, merged) {
			recheckTargets[ancestor] = true
		}
	}

	var warnings []string
	for targetID := range recheckTargets {
		// targetLeafBudget only carries an entry for a DIRECTLY-referenced
		// target (resolved via SubtypeOfRef, above) — an ancestor pulled in
		// only by the upward walk was never looked up that way, so its own
		// declared budget is read here for the first time.
		budget, ok := targetLeafBudget[targetID]
		if !ok {
			budget = i.leafBudgetForMetaVertex(ctx, targetID, batchLeafBudgets)
		}
		if budget <= 0 {
			budget = leafBudgetDefault
		}
		total := i.countTransitiveConcreteDescendants(ctx, targetID, downwardEdges, batchAbstract, abstractCache)
		if total > budget {
			warnings = append(warnings, fmt.Sprintf(
				"subtypeOf target vtx.meta.%s: leaf count %d exceeds declared LeafBudget %d (transitive concrete descendant count, not direct children)",
				targetID, total, budget))
		}
	}
	sort.Strings(warnings)

	return resolved, warnings, nil
}

// ancestorsOf walks node's parents upward through adj (child -> []parents,
// e.g. resolveTaxonomy's merged), depth-first, returning every ancestor
// reached — node itself is never included. Mirrors walkTaxonomyNoCycle's
// traversal shape but collects nodes instead of checking for a cycle: adj
// is always the SAME graph a walkTaxonomyNoCycle pass over every node
// already proved acyclic and within maxTaxonomyDepth before this is ever
// called, so no cycle guard is needed here — only the identical depth cap,
// carried over defensively.
func ancestorsOf(node string, adj map[string][]string) []string {
	var out []string
	visited := map[string]bool{node: true}
	var walk func(n string, depth int)
	walk = func(n string, depth int) {
		if depth > maxTaxonomyDepth {
			return
		}
		for _, parent := range adj[n] {
			if visited[parent] {
				continue
			}
			visited[parent] = true
			out = append(out, parent)
			walk(parent, depth+1)
		}
	}
	walk(node, 0)
	return out
}

// leafBudgetForMetaVertex returns id's declared LeafBudget (0 meaning
// undeclared, which the caller defaults to leafBudgetDefault), consulting
// batchLeafBudgets first (an Abstract DDL this SAME batch declares, at zero
// extra I/O) and otherwise reading the already-installed root document —
// the ancestor-recheck counterpart to resolveExternalSubtypeTarget's inline
// read for a DIRECTLY-referenced target: an ancestor discovered only by
// ancestorsOf was never looked up by SubtypeOfRef resolution, so its budget
// has not been read at all until now. A missing document, a read fault, or
// a malformed body all read as undeclared (0) — the same "never worse than
// no signal" licence countTransitiveConcreteDescendants documents.
func (i *Installer) leafBudgetForMetaVertex(ctx context.Context, id string, batchLeafBudgets map[string]int) int {
	if budget, ok := batchLeafBudgets[id]; ok {
		return budget
	}
	entry, err := i.Conn.KVGet(ctx, CoreBucket, "vtx.meta."+id)
	if err != nil {
		return 0
	}
	var env struct {
		Data struct {
			LeafBudget int `json:"leafBudget"`
		} `json:"data"`
	}
	if uerr := json.Unmarshal(entry.Value, &env); uerr != nil {
		return 0
	}
	return env.Data.LeafBudget
}

// countTransitiveConcreteDescendants counts rootID's own CONCRETE
// contribution (if any) plus every CONCRETE descendant reached by walking
// downwardEdges (parent -> children) depth-first, mirroring EXACTLY what
// the runtime resolver's Expand admits for a `*` label over the same graph
// (internal/refractor/taxonomy's collectDownLocked, which is reflexive for
// a concrete id and skips an abstract one — §3.4/amendment A5, "a concrete
// type may itself have subtypes"). rootID itself is counted when it
// resolves concrete: amendment A5 makes a concrete type a legal subtypeOf
// target, and collectDownLocked adds a concrete id's own canonicalName
// before it ever looks at children — omitting that here would under-count
// by exactly one for every concrete target, the boundary at which "count ==
// budget, no warning" silently hides a real 1-over overrun.
//
// Bounded by maxTaxonomyDepth as a defensive cap, not a correctness
// requirement — the graph downwardEdges is built from is the SAME edge set
// resolveTaxonomy's caller just proved acyclic and within maxTaxonomyDepth
// (the walk immediately above), and a DAG proven bounded in one direction
// is bounded in the other. visited (by node ID, not by path) dedupes a
// diamond so a doubly-reachable descendant is counted once, matching
// collectDownLocked's set semantics.
//
// Never returns an error: this is an advisory, install-time SIGNAL (§10.2
// — a LeafBudget overrun is a warning, never a rejection), so a read fault
// partway through the walk degrades to under-counting that one branch
// rather than failing the install for a check that exists only to warn.
// "Never worse than no signal" is what licenses that degradation.
func (i *Installer) countTransitiveConcreteDescendants(ctx context.Context, rootID string, downwardEdges map[string][]string, batchAbstract map[string]bool, abstractCache map[string]bool) int {
	visited := map[string]bool{rootID: true}
	count := 0
	if !i.isAbstractMetaVertex(ctx, rootID, batchAbstract, abstractCache) {
		count++
	}
	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		if depth > maxTaxonomyDepth {
			return
		}
		for _, child := range downwardEdges[id] {
			if visited[child] {
				continue
			}
			visited[child] = true
			if !i.isAbstractMetaVertex(ctx, child, batchAbstract, abstractCache) {
				count++
			}
			walk(child, depth+1)
		}
	}
	walk(rootID, 0)
	return count
}

// isAbstractMetaVertex reports whether meta vertex id declares
// data.abstract == true, consulting batchAbstract first (a node this SAME
// install batch declares, at zero extra I/O) and otherwise reading the
// already-installed root document, caching the answer in abstractCache so a
// node reached via more than one path in one countTransitiveConcreteDescendants
// call is read at most once.
//
// A missing document, a read fault, or a present-but-non-bool `abstract`
// value all resolve to CONCRETE (false) — the opposite fail-closed direction
// from internal/processor/ddl_cache.go's reading of the identical field, and
// deliberately so, mirroring internal/refractor/lens.abstractFlagFromData's
// reasoning for the SAME divergence: there, `abstract=true` silently
// narrows a live filter, so it fails toward false. Here, wrongly reading a
// real abstract mid-type as concrete makes the LeafBudget count LARGER, not
// smaller — the safe direction for a check whose entire purpose is
// catching a count that got too big, since the failure mode of an
// over-generous count is a spurious warning (harmless — this check never
// blocks) while the failure mode of an under-count is a real overrun that
// silently goes unwarned.
func (i *Installer) isAbstractMetaVertex(ctx context.Context, id string, batchAbstract map[string]bool, abstractCache map[string]bool) bool {
	if v, ok := batchAbstract[id]; ok {
		return v
	}
	if v, ok := abstractCache[id]; ok {
		return v
	}
	entry, err := i.Conn.KVGet(ctx, CoreBucket, "vtx.meta."+id)
	if err != nil {
		abstractCache[id] = false
		return false
	}
	var env struct {
		Data struct {
			Abstract bool `json:"abstract"`
		} `json:"data"`
	}
	abstract := false
	if uerr := json.Unmarshal(entry.Value, &env); uerr == nil {
		abstract = env.Data.Abstract
	}
	abstractCache[id] = abstract
	return abstract
}

// resolveExternalSubtypeTarget resolves ref to an already-installed
// vertexType meta-vertex's NanoID + declared LeafBudget (0 when undeclared),
// reading its root document to confirm it is not tombstoned and its class is
// meta.ddl.vertexType (an empty class defaults to vertexType, mirroring
// buildInstallBatch's own default). Per Andrew's ratification the target need
// NOT be abstract — a concrete type may have subtypes (§3.4) — but it must
// still be a vertexType, which is what catches a ref that names a lens (or
// any other non-vertexType meta-vertex) canonicalName by typo.
//
// scan.names is the canonicalName -> NanoID map the caller already built
// from one KVListKeys pass; this issues at most one additional targeted GET.
//
// Fails closed on every miss (§3.5): unlike resolveLensRef's NanoID
// pass-through (build.go:533-551), an unresolvable, non-vertexType, or
// tombstoned ref is never accepted verbatim. A read/unmarshal fault is
// wrapped in ErrSubtypeOfRefUnresolved too, so every caller classifying this
// failure via errors.Is sees the same sentinel regardless of which specific
// check inside here tripped.
func (i *Installer) resolveExternalSubtypeTarget(ctx context.Context, ref string, metaNames map[string]string) (id string, leafBudget int, err error) {
	metaID, ok := metaNames[ref]
	if !ok {
		return "", 0, fmt.Errorf("%w: canonicalName %q not found in the installed kernel", ErrSubtypeOfRefUnresolved, ref)
	}
	rootKey := "vtx.meta." + metaID
	entry, err := i.Conn.KVGet(ctx, CoreBucket, rootKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return "", 0, fmt.Errorf("%w: canonicalName %q (meta-vertex %s not found)", ErrSubtypeOfRefUnresolved, ref, rootKey)
		}
		return "", 0, fmt.Errorf("pkgmgr: get %s: %w", rootKey, err)
	}
	var env struct {
		Class     string `json:"class"`
		IsDeleted bool   `json:"isDeleted"`
		Data      struct {
			LeafBudget int `json:"leafBudget"`
		} `json:"data"`
	}
	if uerr := json.Unmarshal(entry.Value, &env); uerr != nil {
		return "", 0, fmt.Errorf("%w: canonicalName %q (meta-vertex %s: unmarshal failed: %v)", ErrSubtypeOfRefUnresolved, ref, rootKey, uerr)
	}
	if env.IsDeleted {
		return "", 0, fmt.Errorf("%w: canonicalName %q (meta-vertex %s is tombstoned)", ErrSubtypeOfRefUnresolved, ref, rootKey)
	}
	class := env.Class
	if class == "" {
		class = ddlClassVertexType
	}
	if class != ddlClassVertexType {
		return "", 0, fmt.Errorf("%w: canonicalName %q (meta-vertex %s has class %q, not %q — a subtypeOf target must be a vertex type)",
			ErrSubtypeOfRefUnresolved, ref, rootKey, env.Class, ddlClassVertexType)
	}
	return metaID, env.Data.LeafBudget, nil
}

// checkAbstractNoLiveInstances refuses a DDL declaring Abstract: true when a
// live (non-tombstoned) instance of its type already exists. Without this
// guard, flipping a live concrete type to abstract would leave every
// existing instance unable to receive a create/update/tombstone (the two
// step-6 write-path gates refuse all three against an abstract-typed key or
// class), with no cleanup path at all — refusing the DECLARATION here, with
// a clear error naming the type and one offending key, is far better than
// letting the flip land and surfacing as a mysterious step-6 rejection on
// the next write to a pre-existing instance.
//
// keys is the caller's already-fetched Core KV key list (buildManifestBatch
// fetches it lazily, at most once) — no KVListKeys of its own.
//
// What makes the exact-prefix scan below SOUND: a live key's type segment
// is always lowercase-ASCII ([a-z][a-z0-9]*) — step6_validate.go's
// ClassifyKey enforces that on every commit (Contract #2 P2) — and, for a
// taxonomy-participating DDL, abstractscope.go's key-type-segment gate now
// constrains CanonicalName to that identical alphabet before this ever
// runs. Two strings drawn from the same lowercase-ASCII alphabet are equal
// iff they are byte-identical, so an exact-prefix match already answers
// "is this the segment the instance actually uses" — no case-insensitive
// comparison is needed or meaningful here.
//
// The residual this does NOT close, named plainly rather than implied
// covered: a CanonicalName that IS a valid key-type segment but simply is
// not the segment the type's own instances key under — e.g. declaring
// Abstract: true on "workorder" while every instance is actually keyed
// vtx.wo.<id>, an equally valid but DIFFERENT segment. Nothing catches
// that today: the scan finds no live "vtx.workorder.*" key, the
// declaration proceeds, and step 6's write-path gates then refuse every
// future write to the "vtx.wo.*" instances with no cleanup path. Closing
// that gap needs a way to ask "what segment does THIS type's instances
// actually use," which nothing in the platform can answer for an existing
// type today.
func (i *Installer) checkAbstractNoLiveInstances(ctx context.Context, def Definition, keys []string) error {
	for idx, d := range def.DDLs {
		if !d.Abstract {
			continue
		}
		prefix := "vtx." + d.CanonicalName + "."
		for _, k := range keys {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			live, err := i.isLiveMetaOrVertexKey(ctx, k)
			if err != nil {
				return err
			}
			if !live {
				continue
			}
			return fmt.Errorf(
				"pkgmgr: DDL[%d] %q: Abstract is true but a live instance already exists (e.g. %s) — declare Abstract only over a type with no live instances",
				idx, d.CanonicalName, k)
		}
	}
	return nil
}

// isLiveMetaOrVertexKey reports whether k names a live (non-tombstoned)
// Core KV entry. A missing key or an unmarshal failure both read as "not
// live" — the same permissive treatment checkAbstractNoLiveInstances' scan
// always gave them.
func (i *Installer) isLiveMetaOrVertexKey(ctx context.Context, k string) (bool, error) {
	entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("pkgmgr: get %s: %w", k, err)
	}
	var env struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if uerr := json.Unmarshal(entry.Value, &env); uerr != nil {
		return false, nil
	}
	return !env.IsDeleted, nil
}

// scanInstalledSubtypeOfEdgesFromKeys filters the caller's already-fetched
// Core KV key list for every live (non-tombstoned) installed
// `lnk.meta.<leafId>.subtypeOf.meta.<parentId>` edge, returning leaf NanoID
// -> its parent NanoIDs (a leaf may carry more than one — §3.4 permits
// multiple parents). Targeted GETs only, no KVListKeys — the caller supplies
// the list so one Install/Upgrade/Apply call never lists the bucket twice.
func scanInstalledSubtypeOfEdgesFromKeys(ctx context.Context, conn *substrate.Conn, keys []string) (map[string][]string, error) {
	edges := map[string][]string{}
	for _, k := range keys {
		t1, id1, rel, t2, id2, ok := substrate.ParseLinkKey(k)
		if !ok || t1 != "meta" || rel != subtypeOfRelation || t2 != "meta" {
			continue
		}
		entry, err := conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("pkgmgr: get %s: %w", k, err)
		}
		var env struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if err := json.Unmarshal(entry.Value, &env); err != nil || env.IsDeleted {
			continue
		}
		edges[id1] = append(edges[id1], id2)
	}
	return edges, nil
}

// walkTaxonomyNoCycle walks node's parents (adj[node]) upward, depth-first,
// rejecting a back-edge to any node already on the current path (a cycle) and
// a path longer than maxTaxonomyDepth. onPath is mutated and restored
// (backtracked) around each branch, so a legitimate multi-parent diamond —
// two branches converging on a shared ancestor — is never mistaken for a
// cycle; only a genuine back-edge to a current-path ancestor is.
func walkTaxonomyNoCycle(node string, adj map[string][]string, onPath map[string]bool, depth int) error {
	for _, parent := range adj[node] {
		if onPath[parent] {
			return fmt.Errorf("%w: %q -> %q closes a cycle", ErrTaxonomyCycle, node, parent)
		}
		if depth+1 > maxTaxonomyDepth {
			return fmt.Errorf("%w: subtypeOf chain from %q exceeds maxTaxonomyDepth=%d",
				ErrTaxonomyCycle, node, maxTaxonomyDepth)
		}
		onPath[parent] = true
		if err := walkTaxonomyNoCycle(parent, adj, onPath, depth+1); err != nil {
			return err
		}
		delete(onPath, parent)
	}
	return nil
}
