// Package full's AST is the Refractor-native representation of an openCypher
// rule produced by the visitor in visitor.go. It deliberately holds NO ANTLR
// types — everything below is pure Go so the rest of Refractor can consume
// the AST without leaking the vendored parser.
//
// The executor walks these nodes against Core/Adjacency KV to produce
// projections.
package full

import (
	"errors"
	"fmt"
	"sort"
)

// Direction names the orientation of a RelPattern.
type Direction int

const (
	// DirOut is `-[:t]->` (left → right).
	DirOut Direction = iota
	// DirIn is `<-[:t]-` (right → left).
	DirIn
	// DirBoth is `-[:t]-` (no arrowhead on either side; either direction
	// satisfies the pattern). Bootstrap query does not currently use this
	// form, but the visitor accepts it because the grammar permits it.
	DirBoth
)

func (d Direction) String() string {
	switch d {
	case DirOut:
		return "out"
	case DirIn:
		return "in"
	case DirBoth:
		return "both"
	default:
		return "unknown"
	}
}

// Query is the top-level AST node. Clauses appear in source order.
type Query struct {
	Clauses []Clause
}

// Clause is one of Match, With, Return.
type Clause interface{ isClause() }

// Match models `MATCH …` and `OPTIONAL MATCH …` (Optional=true) with an
// optional WHERE expression. Pattern carries the alternating node/rel chain.
type Match struct {
	Optional bool
	Patterns []PathPattern // a MATCH can list multiple comma-separated patterns
	Where    Expr          // nil if absent
}

func (*Match) isClause() {}

// With carries forward named bindings from the preceding read clauses into
// the next clause group. WITH also accepts a WHERE filter.
type With struct {
	Distinct bool
	Items    []ProjectionItem
	Where    Expr
}

func (*With) isClause() {}

// Return is the projection emitted as the rule's output.
type Return struct {
	Distinct bool
	Items    []ProjectionItem
}

func (*Return) isClause() {}

// PathPattern is an alternating chain of node patterns and relationship
// patterns. len(Rels) == len(Nodes)-1. The first element is always a node.
type PathPattern struct {
	Nodes []NodePattern
	Rels  []RelPattern
}

// NodePattern is `(var:Label {props})`. Any field may be empty.
//
// LabelExpand records the trailing `*` sigil: `(l:location*)` means the
// label plus its transitive subtypes, while a bare `(l:location)` means
// exactly `vtx.location.<id>`.
type NodePattern struct {
	Variable    string
	Label       string
	LabelExpand bool
	Properties  map[string]Expr
}

// RelPattern is the relationship segment of a path pattern.
//
// MinHops/MaxHops carry variable-length quantifier info:
//
//	no `*`              → MinHops=1, MaxHops=1
//	`*0..`              → MinHops=0, MaxHops=-1 (unbounded)
//	`*0..2`             → MinHops=0, MaxHops=2
//	`*N..M`             → MinHops=N, MaxHops=M
//
// MaxHops=-1 means "unbounded".
type RelPattern struct {
	Variable   string
	Type       string
	Direction  Direction
	MinHops    int
	MaxHops    int
	Properties map[string]Expr
}

// ProjectionItem is one entry in a WITH or RETURN list.
type ProjectionItem struct {
	Expr  Expr
	Alias string // "" when no AS provided
}

// Expr is the marker interface for all expression nodes.
type Expr interface{ isExpr() }

// Literal holds a primitive value: bool, int64, float64, string, or nil.
type Literal struct {
	Value any
}

func (*Literal) isExpr() {}

// ParameterRef captures `$name` references. Bound to actual values by the
// executor in 3.1b-ii from EventContext.Parameters.
type ParameterRef struct {
	Name string
}

func (*ParameterRef) isExpr() {}

// VariableRef is a bare variable, e.g. `identity` or `perm`.
type VariableRef struct {
	Name string
}

func (*VariableRef) isExpr() {}

// PropertyAccess is `target.key`. Nested access (`a.b.c`) chains via Target
// being another PropertyAccess.
type PropertyAccess struct {
	Target Expr
	Key    string
}

func (*PropertyAccess) isExpr() {}

// BinaryOp covers comparison ops (=, <>, <, >, <=, >=) and arithmetic ops
// (+, -, *, /, %). For boolean AND/OR see AndOr.
type BinaryOp struct {
	Op    string
	Left  Expr
	Right Expr
}

func (*BinaryOp) isExpr() {}

// AndOr models n-ary boolean combinators. Op is "AND" or "OR".
type AndOr struct {
	Op       string
	Operands []Expr
}

func (*AndOr) isExpr() {}

// Not is logical negation of any boolean expression. Used both for plain
// `NOT x` and for the anti-pattern `NOT (a)-[:b]->(c)` (the operand is a
// PatternExpr in that case).
type Not struct {
	Operand Expr
}

func (*Not) isExpr() {}

// PatternExpr wraps a pattern used as an existence test inside expressions
// (most commonly inside `WHERE NOT (...)`).
type PatternExpr struct {
	Pattern PathPattern
}

func (*PatternExpr) isExpr() {}

// FunctionCall captures any function invocation. The `collect()` and
// `collect(DISTINCT ...)` calls land here with Name=="collect" and
// Distinct=true when applicable.
type FunctionCall struct {
	Namespace []string
	Name      string
	Distinct  bool
	Args      []Expr
}

func (*FunctionCall) isExpr() {}

// MapLiteral is `{key: expr, ...}` — preserves insertion order via Keys.
type MapLiteral struct {
	Keys   []string
	Values map[string]Expr
}

func (*MapLiteral) isExpr() {}

// ListLiteral is `[expr, expr, ...]`.
type ListLiteral struct {
	Elements []Expr
}

func (*ListLiteral) isExpr() {}

// PatternComprehension is `[pattern | projection]` or
// `[pattern WHERE pred | projection]`. The bootstrap query uses this form
// inside the `serviceAccess` map literal's `allowedOperations` field.
type PatternComprehension struct {
	Variable   string // optional named binding
	Pattern    PathPattern
	Where      Expr
	Projection Expr
}

func (*PatternComprehension) isExpr() {}

// CaseWhenThen is one `WHEN <cond> THEN <result>` alternative of a generic
// CASE expression.
type CaseWhenThen struct {
	When Expr
	Then Expr
}

// CaseExpr is the generic (no test-expression) form of a CASE expression:
// `CASE (WHEN cond THEN result)+ (ELSE default)? END`. Each WHEN condition
// is evaluated in order and is truthy-tested; the first match's THEN value
// is returned. Else is nil when absent (matching Cypher's implicit
// `ELSE NULL`). The simple (test-expression) form `CASE expr WHEN val ...`
// is not supported.
type CaseExpr struct {
	Alternatives []CaseWhenThen
	Else         Expr
}

func (*CaseExpr) isExpr() {}

// CompiledRule satisfies ruleengine.CompiledRule. It is the opaque value
// full.Engine.Parse returns; full.Engine.Execute (3.1b-ii) will consume it.
type CompiledRule struct {
	Query *Query

	// KeyColumns are the RETURN aliases designated as the projection's output
	// key, threaded from Rule.Into.Key at activation. When set, the executor
	// builds the complete multi-column key map (mirroring the simple engine) so
	// a composite-key lens — e.g. a GrantTable lens keyed on
	// (actor_id, anchor_id, grant_source) — projects every key column the
	// adapter requires. Empty/unset keeps the legacy first-RETURN-item key, so
	// single-key lenses and directly-constructed test rules are unchanged.
	KeyColumns []string

	// LabelExpansion maps a label carrying the `*` taxonomy-expansion sigil
	// (NodePattern.LabelExpand) to the set of concrete vertex key types it
	// admits — the resolver's answer for this query, captured once at
	// activation/re-derivation by WithLabelExpansion
	// (dynamic-type-taxonomy-design.md §4.3). Nil for any query with no `*`
	// pattern, and for every query until an activation has resolved one.
	//
	// The four label-equality sites key their lookup on the PATTERN's own
	// LabelExpand flag, never on the label string alone — a query may
	// contain both `(a:location)` and `(b:location*)`, and only the second
	// ever looks in this map.
	LabelExpansion map[string]map[string]struct{}

	// groupingRedundant marks, per projecting clause, the items whose value is
	// already determined by the aliases still in that clause's grouping key —
	// so rendering them into the key cannot change the partition (grouping.go).
	// Absent clause, absent mask, and nil map all mean "render everything",
	// which is the executor's behaviour with no analysis at all: a directly
	// constructed *CompiledRule gets nil here and is unaffected.
	//
	// Written once by Parse, over the Query above, and never mutated
	// afterwards — a compiled rule is shared across concurrent evaluations.
	groupingRedundant map[Clause][]bool

	// branchStages holds, per projecting clause, the sibling OPTIONAL MATCH
	// branches the executor folds against the base row set separately instead of
	// through their cross product (branchgroups.go), and branchDeferred is the
	// set of clauses run() therefore skips. Absent clause, nil maps, and a
	// directly constructed *CompiledRule all mean "evaluate the product", which
	// is the executor's behaviour with no analysis at all.
	//
	// Same immutability contract as groupingRedundant above: written once by
	// Parse, never afterwards. Per-EVALUATION state (the branch row counters
	// each stage's expansion is capped against) lives on the executor.
	branchStages   map[Clause]*stagePlan
	branchDeferred map[*Match]struct{}

	// withAliasEnv resolves a projected alias back to the expression that
	// produces it — one map per WITH clause, in clause order, each already
	// resolved against the boundary before it (withalias.go). It is what lets
	// a key-column predicate read a RETURN expression as an expression over
	// PATTERN VARIABLES when a WITH stands between the two. Empty for a query
	// with no WITH, which needs no substitution.
	//
	// withScopeVerdict is withScopeReject's answer over the same Query: "" when
	// no boundary strands a name a later clause re-reads and every clause and
	// expression in the query is a shape that walk models, else the reason to
	// refuse. It is memoized because anchorProjectionShape asks it on every
	// neighbour event of every plain lens, where a per-event walk of the whole
	// AST would be the expensive part of the path the verdict licenses.
	//
	// withAliasResolved records that Parse computed both of the above. It is
	// deliberately NOT the same signal as an empty env: a query with no WITH
	// also yields an empty env and must be ADMITTED, while a rule that never
	// went through Parse must be REFUSED — collapsing the two would let an
	// unparsed rule read as a clean WITH-free lens, which is the fail-open
	// shape. A directly constructed *CompiledRule gets false here, and
	// anchorProjectionShape refuses any WITH-bearing query on it.
	//
	// Same immutability contract as groupingRedundant above: written once by
	// Parse, over the Query, and never mutated afterwards — a compiled rule is
	// shared across concurrent evaluations.
	withAliasEnv      []map[string]aliasBinding
	withScopeVerdict  string
	withAliasResolved bool
}

// EngineName implements ruleengine.CompiledRule.
func (*CompiledRule) EngineName() string { return "full" }

// returnAliases returns the effective output aliases of the rule's RETURN
// clause (the explicit alias, else the auto-alias) in declaration order, and
// whether a RETURN clause was found.
func (cr *CompiledRule) returnAliases() ([]string, bool) {
	if cr == nil || cr.Query == nil {
		return nil, false
	}
	for _, c := range cr.Query.Clauses {
		r, isReturn := c.(*Return)
		if !isReturn {
			continue
		}
		aliases := make([]string, 0, len(r.Items))
		for i, it := range r.Items {
			a := it.Alias
			if a == "" {
				a = projectionAutoAlias(it.Expr, i)
			}
			aliases = append(aliases, a)
		}
		return aliases, true
	}
	return nil, false
}

// ValidateKeyColumns fails closed when a declared key column is not a RETURN
// alias of the compiled query — the column's value would otherwise be silently
// absent from the projection key map at write time. This mirrors the simple
// engine's compile-time key-field validation, keeping the §6.13 fail-closed
// activation posture for composite-key full-engine lenses (e.g. GrantTable).
// With no key columns declared the engine keeps its first-RETURN-item key, so
// there is nothing to validate.
func (cr *CompiledRule) ValidateKeyColumns() error {
	if cr == nil || len(cr.KeyColumns) == 0 {
		return nil
	}
	aliases, ok := cr.returnAliases()
	if !ok {
		return errors.New("full engine: compiled rule has no RETURN clause")
	}
	have := make(map[string]struct{}, len(aliases))
	for _, a := range aliases {
		have[a] = struct{}{}
	}
	for _, col := range cr.KeyColumns {
		if _, present := have[col]; !present {
			return fmt.Errorf("full engine: key column %q is not a RETURN alias", col)
		}
	}
	return nil
}

// ValidateReturnAliases fails closed when any of names is not a RETURN alias
// of the compiled query. Activation-time counterpart of ValidateKeyColumns
// for columns the caller consumes off the row map (e.g. a Secure Lens's
// secure + identity-key columns) — a missing alias would otherwise be
// silently null on every row, indistinguishable from legitimately-absent
// data.
func (cr *CompiledRule) ValidateReturnAliases(names ...string) error {
	if cr == nil || len(names) == 0 {
		return nil
	}
	aliases, ok := cr.returnAliases()
	if !ok {
		return errors.New("full engine: compiled rule has no RETURN clause")
	}
	have := make(map[string]struct{}, len(aliases))
	for _, a := range aliases {
		have[a] = struct{}{}
	}
	for _, n := range names {
		if _, present := have[n]; !present {
			return fmt.Errorf("full engine: column %q is not a RETURN alias", n)
		}
	}
	return nil
}

// ValidateUnanchoredForDiffRetraction fails closed when the compiled query
// references the $actorKey parameter anywhere — the per-event seed that
// scopes evaluation to one specific triggering vertex.
//
// Fire 3's target-diff retraction (negative-filter-retraction-projection-
// design.md §2.4, pipeline.applyDiffRetraction) is sound ONLY for a
// genuinely unanchored whole-scan lens: it diffs the adapter's FULL live key
// set against the re-execute's FULL freshly-computed row set, which is exact
// only when that row set already represents the complete current truth
// system-wide. If the query instead filtered on $actorKey (scoping the MATCH
// to the single vertex that triggered this event), the fresh row set would
// contain only that vertex's own rows, and every OTHER live anchor's rows —
// still correctly present in the target — would read as "dropped" and be
// deleted: a silent mass-retraction, not a bug an adversarial test would
// likely reproduce by accident. This is the activation-time backstop for
// that authoring invariant, called wherever a lens opts into DiffRetraction
// (cmd/refractor/main.go), so a future or misconfigured anchored lens fails
// to activate rather than corrupting its target on its first live event.
func (cr *CompiledRule) ValidateUnanchoredForDiffRetraction() error {
	if cr == nil || cr.Query == nil {
		return errors.New("full engine: DiffRetraction: compiled rule has no query")
	}
	for _, c := range cr.Query.Clauses {
		switch cl := c.(type) {
		case *Match:
			for _, p := range cl.Patterns {
				if pathPatternReferencesActorKey(p) {
					return errors.New("full engine: DiffRetraction requires an unanchored query — a MATCH pattern references $actorKey")
				}
			}
			if exprReferencesActorKey(cl.Where) {
				return errors.New("full engine: DiffRetraction requires an unanchored query — a MATCH WHERE references $actorKey")
			}
		case *With:
			for _, item := range cl.Items {
				if exprReferencesActorKey(item.Expr) {
					return errors.New("full engine: DiffRetraction requires an unanchored query — a WITH item references $actorKey")
				}
			}
			if exprReferencesActorKey(cl.Where) {
				return errors.New("full engine: DiffRetraction requires an unanchored query — a WITH WHERE references $actorKey")
			}
		case *Return:
			for _, item := range cl.Items {
				if exprReferencesActorKey(item.Expr) {
					return errors.New("full engine: DiffRetraction requires an unanchored query — a RETURN item references $actorKey")
				}
			}
		}
	}
	return nil
}

// ValidateNoFilteringWhereForConvergence fails closed when the compiled
// query carries a filtering WHERE — a non-optional MATCH's WHERE, or any
// WITH's WHERE — that can drop the anchor out of the RETURN row set.
//
// The plain (non-actorAggregate) full-engine path retracts via
// pipeline.evaluateForEntryRaw's post-re-execute presence check
// (negative-filter-retraction-projection-design.md Fire 2): when the
// anchor's own mutation drops it from the freshly-computed match, the
// adapter receives a Delete on that key. For a lens projecting into the
// shared weaver-targets bucket, Weaver reads that Delete as "the entity is
// gone" — not "it stopped violating" — so a filtering WHERE there would
// silently misreport convergence state (docs/components/refractor.md's
// convergence-lens authoring invariant). This is the activation-time
// backstop, called for every plain weaver-targets lens
// (cmd/refractor/main.go), so a future or misconfigured lens fails to
// activate rather than corrupting Weaver's read of it.
//
// A WHERE on an OPTIONAL MATCH is exempt: per the null-restore semantics
// (docs/components/refractor.md), it cannot drop the anchor row — a failed
// optional predicate restores nulls for that pattern's bindings rather than
// removing the row. Only a required (non-optional) MATCH's WHERE, or a
// WITH's WHERE (which always operates on already-bound rows), can collapse
// the anchor out of RETURN.
//
// actorAggregate lenses (e.g. unroutedTasks) are out of scope: their
// retraction runs through the envelope's EmptyBehavior, not this
// presence-check path, so a filtering WHERE there is safe by construction
// and already shipped — callers gate on !projection.IsActorAggregate(r)
// before invoking this.
func (cr *CompiledRule) ValidateNoFilteringWhereForConvergence() error {
	if cr == nil || cr.Query == nil {
		return errors.New("full engine: convergence lens: compiled rule has no query")
	}
	for _, c := range cr.Query.Clauses {
		switch cl := c.(type) {
		case *Match:
			if !cl.Optional && cl.Where != nil {
				return errors.New("full engine: convergence lens requires no filtering WHERE — a required MATCH carries one")
			}
		case *With:
			if cl.Where != nil {
				return errors.New("full engine: convergence lens requires no filtering WHERE — a WITH carries one")
			}
		}
	}
	return nil
}

// ExistenceDependsOnNeighbour reports whether a row's EXISTENCE in this rule's
// output turns on some vertex other than its own anchor — the question that
// decides whether a plain lens needs a neighbour-retraction transport at all
// (secure-plain-lens-retraction-and-audit-design.md §4.4). A lens whose rows
// depend only on the anchor retracts through the anchor's own event; a lens
// whose rows can be dropped by a NEIGHBOUR event needs a transport that no
// anchor event names.
//
// Two shapes make existence depend on a neighbour:
//
//   - a non-OPTIONAL MATCH that reaches any element other than the anchor. The
//     pattern must match for the row to exist at all, so that element's
//     disappearance drops the row. An OPTIONAL MATCH cannot: a failed optional
//     pattern restores nulls for its bindings rather than removing the row
//     (the null-restore semantics ValidateNoFilteringWhereForConvergence
//     above reads the same way).
//   - a WHERE — on a required MATCH, or on any WITH — that reads a non-anchor
//     variable, directly or through a WITH alias, or that evaluates a PATTERN
//     reaching past the anchor. `OPTIONAL MATCH (a)-->(b) WITH a, count(b) AS n
//     WHERE n > 0` gates existence on b while naming only an alias no pattern
//     binds, so each reference is resolved through the WITH-alias environment
//     analyseWithAliases already builds (withalias.go) to the expression over
//     pattern variables that produces it, and judged there.
//
// "ELEMENT", NOT "VARIABLE", IN BOTH. A pattern reaches past the anchor through
// its unnamed nodes and relationships as much as through its bindings:
// `MATCH (a:x)-[:rel]->(:y)` names only `a` while its row lives or dies with a
// link and a `:y`, and `WHERE NOT (a)-->(:role)` gates existence on a
// neighbourhood no binding appears in. So each pattern is asked twice — for the
// non-anchor variables it binds (collectPatternVariableRefs) and for the
// elements it names nothing at all (patternReachesAnonymousElement) — and either
// answer is a dependency. Reading only the bindings answers "no" for the two
// shapes above, which is the fail-open direction this classifier's whole
// disposition is against.
//
// exhaustive is false when the answer could not be derived: an unparsed rule
// carrying a WITH (no alias environment was ever computed), a WITH scope the
// general walk refuses, an anchor pattern that does not resolve, an alias whose
// provenance the resolver does not model, or any AST node the variable walk has
// no case for. A non-exhaustive answer is a REFUSAL at every consumer, never a
// pass — reading "could not tell" as "does not depend" is the fail-open
// direction, and the flag exists so no caller can take it by accident.
//
// reasons names each dependency found, in clause order and de-duplicated, so a
// refusal an operator reads says which variable carries the dependency rather
// than only that one does.
func (cr *CompiledRule) ExistenceDependsOnNeighbour() (depends bool, reasons []string, exhaustive bool) {
	if cr == nil || cr.Query == nil {
		return false, nil, false
	}
	q := cr.Query
	// A rule that never went through Parse carries no alias resolution, and a
	// WITH boundary the general scope walk refuses leaves names whose
	// provenance nothing here can trace — the same precondition
	// anchorProjectionShape states, for the same reason.
	if carriesWith(q.Clauses) && (!cr.withAliasResolved || cr.withScopeVerdict != "") {
		return false, nil, false
	}
	anchorVar, _, _, found := anchorNode(q)
	if !found || anchorVar == "" {
		return false, nil, false
	}

	exhaustive = true
	seen := map[string]struct{}{}
	note := func(reason string) {
		if _, dup := seen[reason]; dup {
			return
		}
		seen[reason] = struct{}{}
		reasons = append(reasons, reason)
		depends = true
	}
	// withIdx is the index of the most recent WITH boundary, which is the
	// environment a WHERE resolves against: a WITH's own WHERE reads that
	// boundary's projected names, and a later MATCH's WHERE reads the same
	// names until the next boundary replaces them.
	withIdx := -1
	for _, c := range q.Clauses {
		switch cl := c.(type) {
		case *Match:
			if cl.Optional {
				continue
			}
			bound := map[string]bool{}
			for _, p := range cl.Patterns {
				if collectPatternVariableRefs(p, bound) {
					exhaustive = false
				}
				if patternReachesAnonymousElement(p) {
					note(fmt.Sprintf("a required MATCH pattern %s reaches an element no variable names, which the anchor %q therefore cannot be",
						renderPathPattern(p), anchorVar))
				}
			}
			for _, v := range sortedVariableNames(bound) {
				if v == anchorVar {
					continue
				}
				note(fmt.Sprintf("a required MATCH reaches %q, which the anchor %q does not bind", v, anchorVar))
			}
			if cl.Where != nil && !cr.whereStaysOnAnchor(cl.Where, anchorVar, withIdx, note) {
				exhaustive = false
			}
		case *With:
			withIdx++
			if cl.Where != nil && !cr.whereStaysOnAnchor(cl.Where, anchorVar, withIdx, note) {
				exhaustive = false
			}
		case *Return:
			// A RETURN projects the row that already exists; it cannot drop one.
		default:
			// A clause type added to the AST without a case here could carry a
			// filter this walk never saw, so it refuses rather than passing.
			exhaustive = false
		}
	}
	return depends, reasons, exhaustive
}

// whereStaysOnAnchor resolves every variable a WHERE reads back to the pattern
// variables underneath it, and every pattern it evaluates to the elements those
// patterns reach, calling note for each one that is not the anchor.
// It returns false when the resolution could not be completed — an unmodelled
// AST node, or an alias whose provenance analyseWithAliases could not
// reconstruct — which its caller turns into a non-exhaustive answer.
//
// envIdx names the WITH boundary in force at this clause, -1 before the first
// one. An alias resolves through substituteAliases (the resolver
// anchorProjectionShape's key columns already go through) rather than through a
// second traversal written here: two resolutions of the same alias are two
// chances to disagree about which variable produced a value.
func (cr *CompiledRule) whereStaysOnAnchor(where Expr, anchorVar string, envIdx int, note func(string)) bool {
	refs := map[string]bool{}
	var patterns []PathPattern
	if collectVariableRefsAndPatterns(where, refs, &patterns) {
		return false
	}
	// The patterns the WHERE itself evaluates, asked for the elements no
	// variable names — `NOT (a)-->(:role)` reads as anchor-only through the
	// bindings, while the row it gates lives or dies with a link and a `:role`.
	for _, p := range patterns {
		if patternReachesAnonymousElement(p) {
			note(fmt.Sprintf("a WHERE evaluates the pattern %s, which reaches an element no variable names and the anchor %q therefore cannot be",
				renderPathPattern(p), anchorVar))
		}
	}
	var env map[string]aliasBinding
	if envIdx >= 0 && envIdx < len(cr.withAliasEnv) {
		env = cr.withAliasEnv[envIdx]
	}
	for _, name := range sortedVariableNames(refs) {
		if _, isAlias := env[name]; !isAlias {
			// A name no boundary binds is a pattern variable (or one carried
			// under its own name), and stands for itself.
			if name != anchorVar {
				note(fmt.Sprintf("a WHERE reads %q, which the anchor %q does not bind", name, anchorVar))
			}
			continue
		}
		resolved, resolvable := substituteAliases(&VariableRef{Name: name}, env)
		if !resolvable {
			return false
		}
		source := map[string]bool{}
		if collectVariableRefsInto(resolved, source) {
			return false
		}
		for _, v := range sortedVariableNames(source) {
			if v == anchorVar {
				continue
			}
			note(fmt.Sprintf("a WHERE reads %q, whose alias %q resolves through it, and the anchor %q does not bind it", v, name, anchorVar))
		}
	}
	return true
}

// sortedVariableNames renders a variable set in a stable order, so the reasons
// a refusal carries read the same on every run.
func sortedVariableNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// pathPatternReferencesActorKey reports whether any node/relationship
// property map in p embeds a $actorKey reference (e.g. `(x {key: $actorKey})`).
func pathPatternReferencesActorKey(p PathPattern) bool {
	for _, n := range p.Nodes {
		for _, v := range n.Properties {
			if exprReferencesActorKey(v) {
				return true
			}
		}
	}
	for _, r := range p.Rels {
		for _, v := range r.Properties {
			if exprReferencesActorKey(v) {
				return true
			}
		}
	}
	return false
}

// exprReferencesActorKey reports whether e's expression tree contains a
// $actorKey ParameterRef anywhere. Conservative by construction — an
// unrecognized future node type reports false, matching this package's
// existing exprReferencesOnlyVariable convention (anchor_delete.go); a query
// shape this walk cannot see through would need its own case added here.
func exprReferencesActorKey(e Expr) bool {
	switch x := e.(type) {
	case nil:
		return false
	case *ParameterRef:
		return x.Name == "actorKey"
	case *PropertyAccess:
		return exprReferencesActorKey(x.Target)
	case *BinaryOp:
		return exprReferencesActorKey(x.Left) || exprReferencesActorKey(x.Right)
	case *AndOr:
		for _, op := range x.Operands {
			if exprReferencesActorKey(op) {
				return true
			}
		}
		return false
	case *Not:
		return exprReferencesActorKey(x.Operand)
	case *FunctionCall:
		for _, a := range x.Args {
			if exprReferencesActorKey(a) {
				return true
			}
		}
		return false
	case *MapLiteral:
		for _, v := range x.Values {
			if exprReferencesActorKey(v) {
				return true
			}
		}
		return false
	case *ListLiteral:
		for _, el := range x.Elements {
			if exprReferencesActorKey(el) {
				return true
			}
		}
		return false
	case *CaseExpr:
		for _, alt := range x.Alternatives {
			if exprReferencesActorKey(alt.When) || exprReferencesActorKey(alt.Then) {
				return true
			}
		}
		return exprReferencesActorKey(x.Else)
	case *PatternExpr:
		return pathPatternReferencesActorKey(x.Pattern)
	case *PatternComprehension:
		return pathPatternReferencesActorKey(x.Pattern) ||
			exprReferencesActorKey(x.Where) || exprReferencesActorKey(x.Projection)
	default:
		return false
	}
}
