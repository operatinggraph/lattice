package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// ErrLensLabelCap is returned by Install/Upgrade/Apply when a lens this package
// declares cannot narrow its Core KV consumer at its own abstract labels'
// declared worst case (dynamic-type-taxonomy-design.md §10.2). Every entry
// point classifies the refusal through this one sentinel.
var ErrLensLabelCap = errors.New("pkgmgr: lens cannot fit the narrowed-filter label cap at its abstract labels' declared LeafBudget")

// lensCapCandidate is one lens's spec text paired with the identity a refusal
// message has to name.
type lensCapCandidate struct {
	idx    int
	name   string
	bodies []string
}

// lensCapCandidates returns the lenses whose cypher the budget gate must read,
// with each lens's bodies as the runtime compiles them: SpecBranches when a
// multi-walk Personal lens compiled to N independent queries, otherwise the
// single Spec. An eventStream lens (empty Spec, non-nil Source) contributes no
// body and is absent from the result — it has no Core KV consumer to narrow.
//
// def must already be through ExpandReadGrantWalks (preflight does it for every
// entry point), so a read-grant lens's bodies here are the COMPOSED head + walk
// chain + tail the installer actually writes — not the presentation tail the
// package author declared. Reading the declared tail instead would miss every
// label the walk chain binds, which on a grant-producing lens is most of them.
func lensCapCandidates(def Definition) []lensCapCandidate {
	out := make([]lensCapCandidate, 0, len(def.Lenses))
	for idx, l := range def.Lenses {
		var bodies []string
		switch {
		case len(l.SpecBranches) > 0:
			bodies = l.SpecBranches
		case l.Spec != "":
			bodies = []string{l.Spec}
		default:
			continue
		}
		out = append(out, lensCapCandidate{idx: idx, name: l.CanonicalName, bodies: bodies})
	}
	return out
}

// mergeSpecLabels folds one branch's label facts into the whole lens's, exactly
// as the runtime does when it derives one consumer filter for a multi-branch
// rule (pipeline.useFullEngineBranches): the referenced and expansion sets
// UNION across branches, because one consumer serves them all, and
// exhaustiveness is a CONJUNCTION, because a single non-exhaustive branch takes
// that shared consumer broad.
func mergeSpecLabels(dst *SpecLabels, add SpecLabels) {
	for l := range add.Referenced {
		dst.Referenced[l] = struct{}{}
	}
	for l := range add.Expansion {
		dst.Expansion[l] = struct{}{}
	}
	dst.Exhaustive = dst.Exhaustive && add.Exhaustive
}

// lensNeedsCapCheck reports whether a lens's merged label facts can produce a
// refusal at all — the gate's only precondition, and the one property that is
// both decidable from the spec alone and NECESSARY for narrowing on every path.
//
// Two conditions, and the first is the one that keeps this from refusing lenses
// that were never at risk. A NON-EXHAUSTIVE lens takes the broad filter
// whatever its label count: full's ReferencedLabels clears exhaustiveness for
// any unlabeled non-re-reference node and for ANY variable-length hop
// (labels.go's addPattern), and pipeline.useFullEngineBranches turns that
// straight into reprojectAll, which narrowedFilterEligible refuses. The only
// lens in the shipped corpus carrying the `*` sigil —
// packages/service-location's capabilityServiceAccess — is exactly that shape:
// two `[:containedIn*0..]` walks plus unlabeled positions. Refusing its install
// over a label count that can never reach a filter would be a false refusal of
// a shipped package. Second, a lens with no `*` anywhere has no abstract type
// to hold a budget against, and its label count is a static property its own
// author can already see.
//
// WHAT IT DELIBERATELY DOES NOT CHECK, stated rather than implied: the rest of
// narrowedFilterEligible. An actor-aware lens must additionally satisfy §4.2's
// conjunction (pattern-closure, a sweep plan, its anchor type in the label set,
// no secure decryptor), and none of those is a property of the cypher — they
// are installed at activation, in stages, by the host. Reconstructing them here
// would be a second, independently-fallible judgment of the very question the
// pipeline keeps to one derivation. So this over-approximates: a lens that
// fails one of those conjuncts is refused even though it would have taken the
// broad filter regardless. The cost is bounded and the remedy is unchanged —
// declare a budget, or drop a concrete label — and it is the same remedy the
// author needs the moment the missing conjunct is satisfied.
func lensNeedsCapCheck(facts SpecLabels) bool {
	return facts.Exhaustive && len(facts.Expansion) > 0
}

// lensSpecLabels statically parses every lens body in def through the injected
// parser and returns the merged label facts per LENS INDEX (not per body), so a
// multi-branch lens is judged as the one consumer it becomes.
//
// Returns nil when no parser is wired, and omits a lens whose body the parser
// rejects — checkLensLabelCap's doc argues both skips. Pure apart from the
// parse: it reads no KV, which is what lets buildManifestBatch call it BEFORE
// deciding whether this def needs a Core KV key-list scan at all.
func (i *Installer) lensSpecLabels(def Definition) map[int]SpecLabels {
	if i.SpecParser == nil {
		return nil
	}
	out := make(map[int]SpecLabels, len(def.Lenses))
	for _, cand := range lensCapCandidates(def) {
		merged := SpecLabels{
			Referenced: map[string]struct{}{},
			Expansion:  map[string]struct{}{},
			Exhaustive: true,
		}
		parsedAll := true
		for _, body := range cand.bodies {
			facts, err := i.SpecParser.Parse(body)
			if err != nil {
				parsedAll = false
				break
			}
			mergeSpecLabels(&merged, facts)
		}
		if !parsedAll {
			continue
		}
		out[cand.idx] = merged
	}
	return out
}

// needsLensCapScan reports whether any lens in facts can produce a refusal, and
// therefore whether checkLensLabelCap needs the Core KV canonicalName index to
// price its abstract labels. False for every def in the corpus that carries no
// `*` sigil, which is what keeps an ordinary install at zero extra reads.
func needsLensCapScan(facts map[int]SpecLabels) bool {
	for _, f := range facts {
		if lensNeedsCapCheck(f) {
			return true
		}
	}
	return false
}

// checkLensLabelCap refuses an install whose own lens cannot narrow its Core KV
// consumer even at the abstract types' DECLARED worst case
// (dynamic-type-taxonomy-design.md §10.2). It is the refusal consumer of
// DDLSpec.LeafBudget; resolveTaxonomy's warning is the other, and the two land
// on deliberately different actors — a leaf installer is warned and never
// blocked, a lens author is refused at their own install, where they can act
// (§10.2's "the enforcement point follows the threat").
//
// THE ARITHMETIC, derived from §10.1/§10.2 against what the runtime actually
// counts rather than restated from the design's prose. pipeline.ConsumerFilter
// applies the cap to the label set useFullEngineBranches published, which is
// built in three moves: union every branch's ReferencedLabels(); delete each
// ExpansionLabels() member's raw label text (an abstract name matches no
// instance, and a concrete one is already inside its own resolved set); union
// in the resolver's concrete answer for each. So the counted set is
//
//	(Referenced \ Expansion)  ∪  ⋃ resolved(e) for e in Expansion
//
// The first term is §10.1's K — "a lens with one abstract label plus K concrete
// labels" — and it is Referenced MINUS the WHOLE expansion set, not minus the
// one label being judged, because every expansion label's raw text is deleted
// on that pass regardless of which one this check is pricing. The second term
// is unknown at install time and unknowable in the future, which is what
// LeafBudget exists to bound: an abstract type's declared budget is its owner's
// promise about how large resolved(e) may grow.
//
// The check is therefore K + Σ budget(e) ≤ MaxNarrowedFilterLabels, summed over
// the expansion labels that resolve to an abstract type. §10.2 words it as
// "K + leafBudget", which is the same expression whenever a lens carries ONE
// abstract label — every case in §10.1's table, and every case in the corpus.
// It is SUMMED here because a lens carrying two abstract labels has a worst case
// of both budgets at once, and a per-label reading would pass a lens the runtime
// takes broad — the exact silent regression this gate exists to remove. Summing
// refuses a strict superset of what the per-label reading refuses, so §10.2's
// stated contract is enforced in full.
//
// TWO WAYS THE SUM OVER-COUNTS, both conservative and both stated so a refused
// author knows what to do. An expanded set may OVERLAP the concrete labels in K
// (the lens names `:unit` and `:location*`, and unit is one of location's
// leaves), and two abstract labels may share leaves; the runtime unions, this
// sums. Both are visible in the lens's own source and both are fixed by dropping
// the redundant concrete label, so the over-count never leaves an author without
// a move.
//
// WHAT IS SKIPPED, and why each skip is not a hole:
//
//   - No parser wired (i.SpecParser nil). The gate is a STATIC DIAGNOSTIC for a
//     footprint regression, not an authorization gate: the property it protects
//     is already enforced at runtime, unconditionally, by ConsumerFilter's own
//     cap — which drops to the broad filter, logs, and reports
//     filterBroadReason "label-cap" on the lens's health entry. An unwired
//     installer therefore loses the early, decidable answer and nothing else. It
//     is wired at every production entry point (cmd/lattice-pkg, cmd/loupe) so
//     the loss is a test-harness one.
//   - A spec that does not parse. Skipped for that lens, never an install
//     failure. A spec the engine cannot compile never activates a lens, so it
//     can never narrow a consumer and can never regress a footprint; turning
//     this gate into a general parse gate over every shipped lens would be an
//     unrelated new refusal with the whole corpus in its blast radius.
//   - An expansion label naming no installed or batch-local type. Install ORDER
//     is not constrained: a lens package may legally install before the package
//     that declares the abstract type it expands. Refusing here would invent an
//     ordering the platform does not have. The runtime is the fail-closed point
//     for this one — useFullEngineBranches REFUSES activation of a `*` lens
//     whose expansion is unresolvable, rather than narrowing wrongly.
//   - An expansion label resolving to a CONCRETE type. Legal (§3.4/amendment A5
//     — a concrete type may itself have subtypes) and budget-less: LeafBudget is
//     rejected on a non-abstract DDL (abstractscope.go), so no declaration
//     exists to price and defaulting one would refuse a lens whose author has no
//     way at all to fix it. A concrete `*`'s growth is ungoverned by any budget
//     and its footprint regression is caught only at runtime.
//
// scan is the caller's already-fetched Core KV key list + canonicalName index;
// this issues at most one targeted GET per distinct unresolved expansion label
// and never lists the bucket itself.
func (i *Installer) checkLensLabelCap(ctx context.Context, def Definition, facts map[int]SpecLabels, scan metaScanResult) error {
	if len(facts) == 0 {
		return nil
	}
	// Batch-local declarations first, at zero I/O: a package that declares its
	// own abstract type and a lens expanding it must be priced against the
	// budget landing in THIS batch, not against the absent installed one.
	batchAbstractBudget := make(map[string]int, len(def.DDLs))
	for _, d := range def.DDLs {
		class := d.Class
		if class == "" {
			class = ddlClassVertexType
		}
		if class == ddlClassVertexType && d.Abstract {
			batchAbstractBudget[d.CanonicalName] = d.LeafBudget
		}
	}

	for _, cand := range lensCapCandidates(def) {
		f, ok := facts[cand.idx]
		if !ok || !lensNeedsCapCheck(f) {
			continue
		}
		concrete := 0
		for l := range f.Referenced {
			if _, isExpansion := f.Expansion[l]; !isExpansion {
				concrete++
			}
		}
		budgeted := 0
		charged := make([]string, 0, len(f.Expansion))
		for l := range f.Expansion {
			budget, abstract, err := i.abstractLeafBudget(ctx, l, batchAbstractBudget, scan)
			if err != nil {
				return err
			}
			if !abstract {
				continue
			}
			budgeted += budget
			charged = append(charged, fmt.Sprintf("%s (LeafBudget %d)", l, budget))
		}
		if len(charged) == 0 {
			continue
		}
		total := concrete + budgeted
		if total <= subjects.MaxNarrowedFilterLabels {
			continue
		}
		sort.Strings(charged)
		return fmt.Errorf(
			"%w: Lens[%d] %q references %d concrete label(s) plus abstract label(s) %v, a worst case of %d expanded labels against a cap of %d — "+
				"drop a concrete label, or ask the abstract type's owner to declare a smaller LeafBudget "+
				"(an abstract type that declares none takes the whole cap, %d, as its budget)",
			ErrLensLabelCap, cand.idx, cand.name, concrete, charged, total, subjects.MaxNarrowedFilterLabels, leafBudgetDefault)
	}
	return nil
}

// abstractLeafBudget resolves an expansion label's canonicalName to the
// LeafBudget the cap arithmetic charges for it, and reports whether the label
// names an ABSTRACT type at all — the only kind that carries a budget.
//
// Batch-local declarations answer first and at zero I/O; otherwise the label is
// looked up in the caller's canonicalName index and its installed meta-vertex
// read. An undeclared budget (zero) takes leafBudgetDefault, which is the whole
// label cap: an abstract type whose owner made no promise about its growth
// cannot be relied on to leave a consuming lens any room, and §10.2's remedy is
// for that owner to declare a real number.
//
// An unresolvable or non-abstract label answers (0, false) — checkLensLabelCap's
// doc has why each of those skips is not a hole. A read or unmarshal FAULT on a
// meta-vertex that the index says exists is returned as an error rather than
// resolved either way: reading it as non-abstract would silently drop the check
// for the one label it was called about, and reading it as abstract would refuse
// an install over a transient transport failure. Neither answer is one a
// failed read can back.
func (i *Installer) abstractLeafBudget(ctx context.Context, canonicalName string, batchAbstractBudget map[string]int, scan metaScanResult) (budget int, abstract bool, err error) {
	if declared, ok := batchAbstractBudget[canonicalName]; ok {
		if declared <= 0 {
			declared = leafBudgetDefault
		}
		return declared, true, nil
	}
	metaID, ok := scan.names[canonicalName]
	if !ok {
		return 0, false, nil
	}
	rootKey := "vtx.meta." + metaID
	entry, err := i.Conn.KVGet(ctx, CoreBucket, rootKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("pkgmgr: lens label-cap gate: get %s: %w", rootKey, err)
	}
	var env struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			Abstract   bool `json:"abstract"`
			LeafBudget int  `json:"leafBudget"`
		} `json:"data"`
	}
	if uerr := json.Unmarshal(entry.Value, &env); uerr != nil {
		return 0, false, fmt.Errorf("pkgmgr: lens label-cap gate: %s: unmarshal failed: %w", rootKey, uerr)
	}
	if env.IsDeleted || !env.Data.Abstract {
		return 0, false, nil
	}
	if env.Data.LeafBudget <= 0 {
		return leafBudgetDefault, true, nil
	}
	return env.Data.LeafBudget, true, nil
}
