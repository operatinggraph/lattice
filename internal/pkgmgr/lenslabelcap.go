package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

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
// lensCapRemedy's two moves — and it is the same remedy the author needs the
// moment the missing conjunct is satisfied.
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
// sums. Both are visible in the lens's own source, and lensCapRemedy's first
// move — rewriting the redundant concrete label AS the sigil — collapses either
// one, so the over-count never leaves an author without a move.
//
// The overlap is NOT subtracted, and that is a decision rather than an
// omission. Subtracting |K ∩ currentLeaves(e)| would price against today's
// membership, and membership is exactly the thing LeafBudget exists because
// nobody can pin: an overlapping leaf can be re-parented OUT of the closure by
// its own package's next version while the abstract adds new leaves up to its
// budget, at which point the subtracted count is below the runtime's and the
// lens narrows nothing. A conservative over-count refuses a lens that would
// have fit; a subtraction admits one that goes broad, which is the failure this
// gate exists to remove.
//
// WHAT IS SKIPPED, and why each skip is not a hole:
//
//   - No parser wired (i.SpecParser nil). The gate is a STATIC DIAGNOSTIC for a
//     footprint regression, not an authorization gate: the property it protects
//     is already enforced at runtime, unconditionally, by ConsumerFilter's own
//     cap — which drops to the broad filter, logs, and reports
//     filterBroadReason "label-cap" on the lens's health entry. An unwired
//     installer therefore loses the early, decidable answer and nothing else. It
//     is wired at every production INSTALL entry point (cmd/lattice-pkg,
//     cmd/loupe) so the loss is a test-harness one.
//   - A spec that does not parse. Skipped for that lens, never an install
//     failure. A spec the engine cannot compile never activates a lens, so it
//     can never narrow a consumer and can never regress a footprint; turning
//     this gate into a general parse gate over every shipped lens would be an
//     unrelated new refusal with the whole corpus in its blast radius.
//   - An expansion label naming no installed or batch-local type. Install ORDER
//     is not constrained: a lens package may legally install before the package
//     that declares the abstract type it expands. Charging one a budget here
//     would invent an ordering the platform does not have, so it contributes
//     NOTHING — and because a partially-priced expansion set would otherwise
//     report a subset's arithmetic as a whole worst case, every un-priced label
//     is NAMED in the refusal and the number is called a floor. The runtime is
//     the fail-closed point for this one: useFullEngineBranches REFUSES
//     activation of a `*` lens whose expansion is unresolvable, rather than
//     narrowing wrongly.
//   - Lenses installed outside pkgmgr entirely. internal/bootstrap seeds the
//     primordial lenses (lenses.go, primordial.go) straight into Core KV
//     without constructing an Installer, so no primordial lens passes through
//     this gate at all; none carries the `*` sigil today, and the runtime cap
//     remains their only enforcement point.
//
// WHAT IS NOT SKIPPED, though it carries no budget to be priced against: an
// expansion label resolving to a CONCRETE type. It is legal (§3.4/amendment A5
// — a concrete type may itself have subtypes) and the runtime treats it EXACTLY
// like an abstract one, unioning in resolved(e) for every expansion label
// alike. A concrete closure is reflexive, so it is never smaller than 1 — and
// never smaller than 2 for a `*` that activates at all, since a closure of
// exactly itself is refused at activation as an inert sigil. A zero charge
// would therefore price a label set strictly smaller than the one the runtime
// builds, admitting exactly the lens this gate exists to refuse. So it is
// charged its CURRENT resolved closure, counted over the same subtypeOf graph
// the resolver walks.
//
// That charge is a FLOOR, not a bound, and the difference is stated because it
// is real: LeafBudget is rejected on a non-abstract DDL (abstractscope.go), so
// a concrete type's growth is governed by NO declared worst case and a package
// installed tomorrow can add subtypes beneath it with nothing refused at either
// install. The abstract case has a promise to price against; this one has only
// the present. Pricing the present is strictly better than pricing zero, and
// the runtime cap stays the backstop for the growth no declaration covers.
//
// scan is the caller's already-fetched Core KV key list + canonicalName index.
// Pricing an abstract label issues at most one targeted GET; the first CONCRETE
// expansion label additionally builds the batch's subtypeOf graph once
// (buildLensCapTaxonomy), and nothing here ever lists the bucket itself.
func (i *Installer) checkLensLabelCap(ctx context.Context, def Definition, facts map[int]SpecLabels, scan metaScanResult) error {
	if len(facts) == 0 {
		return nil
	}
	batch := lensCapBatchTypes(def)

	// Built on first need only: an abstract-only `*` corpus never pays for it.
	var tax *lensCapTaxonomy

	// EVERY offending lens is reported, not the first: a package declaring
	// three over-budget lenses would otherwise need three install attempts to
	// learn about three problems its author can see all at once.
	var offenses []string
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
		expansion := make([]string, 0, len(f.Expansion))
		for l := range f.Expansion {
			expansion = append(expansion, l)
		}
		sort.Strings(expansion)

		budgeted := 0
		charged := make([]string, 0, len(expansion))
		unpriced := make([]string, 0, len(expansion))
		for _, l := range expansion {
			budget, kind, err := i.abstractLeafBudget(ctx, l, batch, scan)
			if err != nil {
				return err
			}
			switch kind {
			case expansionAbstract:
				budgeted += budget
				charged = append(charged, fmt.Sprintf("%s (abstract, LeafBudget %d)", l, budget))
			case expansionConcrete:
				if tax == nil {
					tax, err = i.buildLensCapTaxonomy(ctx, def, scan)
					if err != nil {
						return err
					}
				}
				closure := i.concreteExpansionCharge(ctx, l, tax)
				budgeted += closure
				charged = append(charged, fmt.Sprintf(
					"%s (concrete, closure %d today — no LeafBudget governs its growth)", l, closure))
			case expansionUnresolved:
				unpriced = append(unpriced, l)
			}
		}
		total := concrete + budgeted
		if total <= subjects.MaxNarrowedFilterLabels {
			continue
		}

		priced := "no expansion label this gate can price"
		if len(charged) > 0 {
			priced = fmt.Sprintf("expansion label(s) %v", charged)
		}
		floor := ""
		if len(unpriced) > 0 {
			floor = fmt.Sprintf(
				" — expansion label(s) %v name no type this batch or the installed kernel declares, so they are NOT priced here and %d is a floor",
				unpriced, total)
		}
		offenses = append(offenses, fmt.Sprintf(
			"Lens[%d] %q references %d concrete label(s) plus %s, a worst case of %d expanded labels against a cap of %d%s",
			cand.idx, cand.name, concrete, priced, total, subjects.MaxNarrowedFilterLabels, floor))
	}
	if len(offenses) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s. FIX: %s", ErrLensLabelCap, strings.Join(offenses, "; "), lensCapRemedy)
}

// lensCapRemedy is the advice every refusal carries, and it exists as its own
// named string because the OBVIOUS move is the wrong one.
//
// Deleting the offending concrete label does not shrink the count — it removes
// the lens from the narrowing population entirely. A node pattern with no label
// and no label-constrained re-reference clears the whole lens's exhaustiveness
// (full's ReferencedLabels), which useFullEngineBranches turns straight into
// reprojectAll, which narrowedFilterEligible refuses: the lens then installs,
// activates, and runs on the BROAD consumer filter forever. An author who took
// that advice would trade a loud refusal for exactly the silent footprint
// regression this gate exists to detect, and would never learn they had.
//
// The two moves that leave the lens narrowing: rewrite the redundant concrete
// label AS the abstract sigil where the semantics allow (the concrete type is
// already inside the abstract's expansion, so the pattern still binds it and
// the label count drops by one), or ask the abstract type's owner to declare a
// smaller LeafBudget in their own package version.
var lensCapRemedy = fmt.Sprintf(
	"rewrite a redundant concrete label AS the abstract sigil where the semantics allow — its type is already inside the expansion, "+
		"so the pattern still binds it and the count drops by one — or ask the abstract type's owner to declare a smaller LeafBudget "+
		"(an abstract type that declares none takes the whole cap, %d, as its budget). Do NOT simply remove the label: an unlabeled node "+
		"pattern clears the lens's exhaustiveness, and a non-exhaustive lens runs on the BROAD consumer filter forever — the very "+
		"footprint regression this refusal exists to prevent",
	leafBudgetDefault)

// expansionPricing is how the cap arithmetic charges one `*` label, and the
// three values differ in what KIND of number they produce — which is why they
// are distinguished rather than collapsed into "has a budget or not".
type expansionPricing int

const (
	// expansionUnresolved — no live type of this canonicalName is declared by
	// this batch or present in the installed kernel. Nothing to price;
	// checkLensLabelCap's doc has why that is a skip rather than a refusal, and
	// the refusal names such a label so its count reads as the floor it is.
	expansionUnresolved expansionPricing = iota
	// expansionAbstract — priced at the type's declared LeafBudget, which is a
	// genuine worst-case BOUND because the budget is its owner's promise.
	expansionAbstract
	// expansionConcrete — priced at the type's CURRENT resolved closure, which
	// is a FLOOR: no budget governs a concrete type's growth.
	expansionConcrete
)

// lensCapBatchType records what THIS batch declares about one vertexType
// canonicalName, so a package declaring its own type and a lens expanding it is
// priced against the declaration landing in this batch rather than against the
// absent installed one.
type lensCapBatchType struct {
	abstract   bool
	leafBudget int
}

// lensCapBatchTypes indexes def's vertexType DDLs by canonicalName. Non-
// vertexType classes are excluded: Abstract and LeafBudget are meaningful only
// on a vertexType (abstractscope.go enforces that), and an aspect or link DDL's
// canonicalName occupies a different key segment entirely, so it can never be
// the type an expansion label names.
func lensCapBatchTypes(def Definition) map[string]lensCapBatchType {
	out := make(map[string]lensCapBatchType, len(def.DDLs))
	for _, d := range def.DDLs {
		class := d.Class
		if class == "" {
			class = ddlClassVertexType
		}
		if class != ddlClassVertexType {
			continue
		}
		out[d.CanonicalName] = lensCapBatchType{abstract: d.Abstract, leafBudget: d.LeafBudget}
	}
	return out
}

// abstractLeafBudget resolves an expansion label's canonicalName to the number
// the cap arithmetic charges for it, and reports WHICH KIND of charge that is
// (expansionPricing) — the distinction the caller needs, since only the
// abstract charge is a bound.
//
// Batch-local declarations answer first and at zero I/O; otherwise the label is
// looked up in the caller's canonicalName index and its installed meta-vertex
// read. For an ABSTRACT type an undeclared budget (zero) takes
// leafBudgetDefault, which is the whole label cap: an owner who made no promise
// about their type's growth cannot be relied on to leave a consuming lens any
// room, and §10.2's remedy is for that owner to declare a real number. A
// CONCRETE type carries no budget at all — its charge is its closure, which
// concreteExpansionCharge counts — so this returns 0 alongside
// expansionConcrete and never invents a budget for it.
//
// A read or unmarshal FAULT on a meta-vertex the index says exists is returned
// as an ERROR rather than resolved either way: reading it as unresolved would
// silently drop the check for the one label it was called about, and reading it
// as abstract would refuse an install over a transient transport failure.
// Neither answer is one a failed read can back.
//
// That posture deliberately differs from taxonomy.go's isAbstractMetaVertex /
// leafBudgetForMetaVertex, which swallow the identical fault — adjudicated,
// not an oversight, and recorded here so it is not re-raised as an
// inconsistency. The posture follows the CONSUMER: those two feed an advisory
// warning string whose whole licence is "never worse than no signal", so a
// fault there costs nothing. This one decides whether an install proceeds, and
// an install that cannot read the dependency metadata its own refusal turns on
// should fail loudly and be retried rather than proceed on a guess.
func (i *Installer) abstractLeafBudget(ctx context.Context, canonicalName string, batch map[string]lensCapBatchType, scan metaScanResult) (budget int, kind expansionPricing, err error) {
	if declared, ok := batch[canonicalName]; ok {
		if !declared.abstract {
			return 0, expansionConcrete, nil
		}
		if declared.leafBudget <= 0 {
			return leafBudgetDefault, expansionAbstract, nil
		}
		return declared.leafBudget, expansionAbstract, nil
	}
	metaID, ok := scan.names[canonicalName]
	if !ok {
		return 0, expansionUnresolved, nil
	}
	rootKey := "vtx.meta." + metaID
	entry, err := i.Conn.KVGet(ctx, CoreBucket, rootKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return 0, expansionUnresolved, nil
		}
		return 0, expansionUnresolved, fmt.Errorf("pkgmgr: lens label-cap gate: get %s: %w", rootKey, err)
	}
	var env struct {
		Class     string `json:"class"`
		IsDeleted bool   `json:"isDeleted"`
		Data      struct {
			Abstract   bool `json:"abstract"`
			LeafBudget int  `json:"leafBudget"`
		} `json:"data"`
	}
	if uerr := json.Unmarshal(entry.Value, &env); uerr != nil {
		return 0, expansionUnresolved, fmt.Errorf("pkgmgr: lens label-cap gate: %s: unmarshal failed: %w", rootKey, uerr)
	}
	if env.IsDeleted {
		return 0, expansionUnresolved, nil
	}
	// A meta-vertex of any other class shares the canonicalName namespace but
	// is not a type the resolver can expand — it answers as no type at all,
	// exactly like an absent one.
	class := env.Class
	if class == "" {
		class = ddlClassVertexType
	}
	if class != ddlClassVertexType {
		return 0, expansionUnresolved, nil
	}
	if !env.Data.Abstract {
		return 0, expansionConcrete, nil
	}
	if env.Data.LeafBudget <= 0 {
		return leafBudgetDefault, expansionAbstract, nil
	}
	return env.Data.LeafBudget, expansionAbstract, nil
}

// lensCapTaxonomy is the subtypeOf graph a CONCRETE expansion label is priced
// against: the same edge set the runtime resolver walks, viewed downward, plus
// the abstractness facts collectDownLocked's mirror
// (countTransitiveConcreteDescendants) needs to decide which nodes count.
type lensCapTaxonomy struct {
	// ids maps a vertexType canonicalName to its meta-vertex NanoID, this
	// batch's own declarations shadowing the installed kernel's.
	ids map[string]string
	// downward is parent NanoID -> child NanoIDs.
	downward map[string][]string
	// batchAbstract answers abstractness for a node this batch declares, at
	// zero I/O; abstractCache memoizes the installed ones read during a walk.
	batchAbstract map[string]bool
	abstractCache map[string]bool
}

// buildLensCapTaxonomy assembles the graph checkLensLabelCap prices a concrete
// `*` against, from the caller's already-fetched key list plus this batch's own
// declarations. Built at most once per gate call, and only when a concrete
// expansion label is actually met.
//
// The edge set matches resolveTaxonomy's merged graph, including its exclusion:
// this package's OWN previously-installed subtypeOf edges are dropped, because
// what it declares now supersedes what a prior version declared — counting both
// would price a re-parenting upgrade against a shape that no longer exists.
//
// The walk this feeds is depth-bounded and visited-guarded on its own
// (countTransitiveConcreteDescendants), which matters HERE more than it does
// for resolveTaxonomy's own use of it: this gate runs BEFORE resolveTaxonomy
// has proved the merged graph acyclic, so the bound is load-bearing rather than
// defensive. A cyclic or over-deep graph yields a truncated count and then
// fails the install a moment later with ErrTaxonomyCycle, naming the real
// problem.
func (i *Installer) buildLensCapTaxonomy(ctx context.Context, def Definition, scan metaScanResult) (*lensCapTaxonomy, error) {
	tax := &lensCapTaxonomy{
		ids:           make(map[string]string, len(scan.names)+len(def.DDLs)),
		downward:      map[string][]string{},
		batchAbstract: make(map[string]bool, len(def.DDLs)),
		abstractCache: map[string]bool{},
	}
	for name, id := range scan.names {
		tax.ids[name] = id
	}
	batchIDs := make(map[string]bool, len(def.DDLs))
	batchTypeIDs := make(map[string]string, len(def.DDLs))
	for _, d := range def.DDLs {
		class := d.Class
		if class == "" {
			class = ddlClassVertexType
		}
		if class != ddlClassVertexType {
			continue
		}
		id := entityNanoID(def.Name, "ddl:"+d.CanonicalName)
		tax.ids[d.CanonicalName] = id
		batchTypeIDs[d.CanonicalName] = id
		batchIDs[id] = true
		tax.batchAbstract[id] = d.Abstract
	}

	installed, err := scanInstalledSubtypeOfEdgesFromKeys(ctx, i.Conn, scan.keys)
	if err != nil {
		return nil, err
	}
	for child, parents := range installed {
		if batchIDs[child] {
			continue
		}
		for _, parent := range parents {
			tax.downward[parent] = append(tax.downward[parent], child)
		}
	}
	for _, d := range def.DDLs {
		if d.SubtypeOfRef == "" {
			continue
		}
		child, ok := batchTypeIDs[d.CanonicalName]
		if !ok {
			continue
		}
		parent, ok := batchTypeIDs[d.SubtypeOfRef]
		if !ok {
			parent, ok = scan.names[d.SubtypeOfRef]
		}
		if !ok {
			// An unresolvable SubtypeOfRef is resolveTaxonomy's fail-closed
			// call to make (ErrSubtypeOfRefUnresolved, a few lines later); this
			// gate must not pre-empt it with a differently-worded refusal, so
			// the edge is simply absent from the count.
			continue
		}
		tax.downward[parent] = append(tax.downward[parent], child)
	}
	return tax, nil
}

// concreteExpansionCharge prices a concrete `*` label at the number of concrete
// types the resolver would expand it into TODAY — its own canonicalName plus
// every concrete descendant, which is exactly what collectDownLocked admits and
// what useFullEngineBranches unions into the consumer's label set.
//
// One as the floor, never zero, on either path where the closure cannot be
// counted: a resolved concrete label always contributes at least itself to the
// runtime's label set, so zero is an answer no state of the taxonomy can
// justify — and an under-count here is precisely what admits an over-cap lens.
func (i *Installer) concreteExpansionCharge(ctx context.Context, canonicalName string, tax *lensCapTaxonomy) int {
	id, ok := tax.ids[canonicalName]
	if !ok {
		return 1
	}
	n := i.countTransitiveConcreteDescendants(ctx, id, tax.downward, tax.batchAbstract, tax.abstractCache)
	if n < 1 {
		return 1
	}
	return n
}
