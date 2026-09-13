// Negated variable-length-bound corpus census — the executable form of
// scripts/lint-lens-anchors.go's negated-bound rule.
//
// THE RULE. A variable-length relationship hop that carries a FINITE upper
// bound is refused wherever it sits inside a negated pattern (`NOT (...)`,
// `WHERE NOT (...)`, `AND NOT (...)`). Polarity decides which way a too-shallow
// bound is unsound: on a POSITIVE pattern it is fail-CLOSED — the pattern stops
// matching, a row disappears, someone notices — while inside a NEGATION the
// very same edit is fail-OPEN, because it drops an EXCLUSION. A service the
// exclusion arm would have removed at depth 3 stays in the projection when the
// bound says 2, and on the auth plane a row that should not exist is a grant.
// Nothing at runtime reports it.
//
// An OPEN range (`*0..`, `*1..`) inside a negation is sound and is never
// flagged: the executor's own clamp (maxVarLengthHops,
// ruleengine/full/executor.go — read here off the compiled hop index rather
// than copied as a literal) already bounds the walk, and a derivation complete
// with respect to what the executor evaluates misses nothing that could have
// produced a row. A finite bound at or above that clamp is the open form by
// another spelling and is likewise left alone. A ranged hop in a POSITIVE
// pattern is never flagged, whatever its bound — the asymmetry IS the rule.
//
// WHY THIS CENSUS EXISTS BESIDE THE LINT. The lint reads Go SOURCE TEXT and can
// resolve a lens Spec only when it is a string literal or a same-file `const`.
// Every lens assembled with `fmt.Sprintf` — 21 of them today, including the
// auth-plane `capabilityEphemeral` and `cafeStaleTabSettlement` — degrades
// there to an advisory "check it by hand", which binds nobody. This census
// closes exactly that blind spot: it walks the REAL corpus (forEachCorpusCypher
// hands over every executable cypher with its spec already RENDERED), compiles
// each on the production engine, and applies the same rule to the COMPILED AST.
// A `fmt.Sprintf` spec is seen whole here, and a bound smuggled in through a
// format verb is seen at all.
//
// NO ALLOWLIST. The asserted set of (lens, relationship, bound) is EMPTY. A
// lens that acquires such a hop fails by name with the fail-open reason; the
// fix is to open the range, not to add a row here.
//
// The walk cases every Expr type in ruleengine/full's AST (mirroring
// params.go's ReferencesParam and hopindex.go's addExpr, both of which
// default-deny the same way). Its default arm does not skip: an unmodelled node
// FAILS the census naming the lens and the Go type, because an unwalked
// expression may hold the negated pattern this census exists to find, and a
// silent skip would report "clean" for a shape nobody looked at.
package refractor_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// negatedBoundFinding is one violation: a variable-length hop with a finite
// bound below the executor's clamp, reached under a negation.
type negatedBoundFinding struct {
	lens string
	rel  string
	min  int
	max  int
}

func (f negatedBoundFinding) String() string {
	rel := f.rel
	if rel == "" {
		rel = "<untyped>"
	}
	return fmt.Sprintf("%s: -[:%s*%d..%d]- inside a negated pattern", f.lens, rel, f.min, f.max)
}

// negatedBoundWalk accumulates one lens's walk.
//
// negatedPatterns is the positive control the census carries about ITSELF: a
// walker that reaches no negation at all would report an empty finding set for
// the most uninteresting reason available, so the corpus census asserts this
// count is non-zero as well.
type negatedBoundWalk struct {
	lens            string
	clamp           int
	findings        []negatedBoundFinding
	negatedPatterns int
	rangedInNegated int
	notNodes        int
	unmodelled      []string
}

// walkNegatedBounds applies the rule to a compiled rule's whole AST.
//
// clamp is the executor's own maxVarLengthHops, discovered at runtime by
// negatedBoundClamp rather than duplicated as a literal — a census that hard-
// coded 10 would start flagging sound lenses the day the executor's ceiling
// moved, or stop flagging unsound ones.
func walkNegatedBounds(lensName string, cr *full.CompiledRule, clamp int) *negatedBoundWalk {
	w := &negatedBoundWalk{lens: lensName, clamp: clamp}
	if cr == nil || cr.Query == nil {
		w.unmodelled = append(w.unmodelled, "<nil query>")
		return w
	}
	for _, c := range cr.Query.Clauses {
		switch cl := c.(type) {
		case *full.Match:
			// A MATCH's own patterns are asserted, never negated; its WHERE is
			// where `NOT (...)` / `AND NOT (...)` lives.
			for _, p := range cl.Patterns {
				w.pattern(p, false)
			}
			w.expr(cl.Where, false)
		case *full.With:
			for _, it := range cl.Items {
				w.expr(it.Expr, false)
			}
			w.expr(cl.Where, false)
		case *full.Return:
			for _, it := range cl.Items {
				w.expr(it.Expr, false)
			}
		default:
			// A Clause shape this walk does not model may carry a negated
			// pattern in an expression it never descends into.
			w.unmodelled = append(w.unmodelled, fmt.Sprintf("clause %T", c))
		}
	}
	return w
}

// pattern applies the rule to every relationship segment of one path pattern,
// and descends the node/relationship PROPERTY MAPS, which are general
// expressions and can nest a pattern comprehension carrying further hops.
//
// A hop is variable-length when its range is anything but the implicit single
// hop; MinHops == MaxHops == 1 is both a plain `-[:r]->` and an explicit `*1`,
// which the AST cannot tell apart and which mean the same traversal anyway.
// MaxHops == -1 is the open form.
func (w *negatedBoundWalk) pattern(p full.PathPattern, negated bool) {
	if negated {
		w.negatedPatterns++
	}
	for _, r := range p.Rels {
		varLength := r.MinHops != 1 || r.MaxHops != 1
		finite := r.MaxHops >= 0
		if negated && varLength {
			w.rangedInNegated++
		}
		switch {
		case !negated, !varLength, !finite:
			// Positive (fail-closed), a fixed single hop, or open — sound.
		case r.MaxHops >= w.clamp:
			// At or past the executor's own ceiling: the open form spelled out.
		default:
			w.findings = append(w.findings, negatedBoundFinding{
				lens: w.lens, rel: r.Type, min: r.MinHops, max: r.MaxHops,
			})
		}
		for _, v := range r.Properties {
			w.expr(v, negated)
		}
	}
	for _, n := range p.Nodes {
		for _, v := range n.Properties {
			w.expr(v, negated)
		}
	}
}

// expr descends an expression, carrying the polarity of the position it is in.
//
// Polarity is STICKY rather than toggled: once under a `NOT`, everything below
// stays negated. Double negation would restore positive polarity semantically,
// but a census that modelled that would have to be right about it; staying
// negated can only widen the finding set, which is the direction a fail-open
// guard is allowed to err in.
func (w *negatedBoundWalk) expr(e full.Expr, negated bool) {
	switch x := e.(type) {
	case nil:
	case *full.Not:
		w.notNodes++
		w.expr(x.Operand, true)
	case *full.PatternExpr:
		w.pattern(x.Pattern, negated)
	case *full.PatternComprehension:
		w.pattern(x.Pattern, negated)
		w.expr(x.Where, negated)
		w.expr(x.Projection, negated)
	case *full.PropertyAccess:
		w.expr(x.Target, negated)
	case *full.BinaryOp:
		w.expr(x.Left, negated)
		w.expr(x.Right, negated)
	case *full.AndOr:
		for _, op := range x.Operands {
			w.expr(op, negated)
		}
	case *full.FunctionCall:
		for _, a := range x.Args {
			w.expr(a, negated)
		}
	case *full.MapLiteral:
		for _, v := range x.Values {
			w.expr(v, negated)
		}
	case *full.ListLiteral:
		for _, el := range x.Elements {
			w.expr(el, negated)
		}
	case *full.CaseExpr:
		for _, alt := range x.Alternatives {
			w.expr(alt.When, negated)
			w.expr(alt.Then, negated)
		}
		w.expr(x.Else, negated)
	case *full.Literal, *full.ParameterRef, *full.VariableRef:
		// The leaves that hold no sub-expression and no pattern. Named
		// explicitly so the arm below keeps its job: noticing a shape nobody
		// walked.
	default:
		w.unmodelled = append(w.unmodelled, fmt.Sprintf("expression %T", e))
	}
}

// negatedBoundCompile parses one spec on the production engine.
func negatedBoundCompile(t *testing.T, name, spec string) *full.CompiledRule {
	t.Helper()
	cr, err := full.New().Parse(spec)
	require.NoErrorf(t, err, "%s must parse", name)
	fullCR, ok := cr.(*full.CompiledRule)
	require.Truef(t, ok, "%s must compile to the full engine", name)
	return fullCR
}

// negatedBoundClamp reads the executor's maxVarLengthHops off the engine
// itself: an OPEN range's hop comes back from the compiled hop index already
// clamped to that ceiling (hopindex.go's PatternHop.Max contract), so the
// probe's answer IS the constant, without this package needing it exported.
func negatedBoundClamp(t *testing.T) int {
	t.Helper()
	const probe = `MATCH (a {key: $actorKey})-[:containedIn*0..]->(b)
RETURN a.key AS key`
	ix := negatedBoundCompile(t, "varLengthClampProbe", probe).AnchorHopIndex()
	require.Truef(t, ix.Complete, "the clamp probe must index: %s", ix.Incomplete)
	for _, h := range ix.Hops {
		if h.Rel == "containedIn" {
			require.Greaterf(t, h.Max, 1, "an open range must clamp above a single hop, got %d", h.Max)
			return h.Max
		}
	}
	t.Fatal("the clamp probe's containedIn hop is missing from the compiled hop index")
	return 0
}

// negatedBoundCorpusFloor is the floor on how many executable cyphers the
// census must have enumerated. The live count is roughly twice it; the floor
// exists so a corpus that silently stops being walked (a registry that fails to
// expand, a helper that starts skipping) fails here rather than passing as a
// census of nothing.
const negatedBoundCorpusFloor = 60

func TestNegatedVarLengthBoundCorpusCensus(t *testing.T) {
	clamp := negatedBoundClamp(t)
	t.Logf("executor variable-length clamp (maxVarLengthHops) = %d", clamp)

	var (
		enumerated      int
		negatedPatterns int
		rangedInNegated int
		notNodes        int
		findings        []negatedBoundFinding
		unmodelled      []string
		withNegation    []string
	)
	forEachCorpusCypher(t, func(name, spec string, _ *lens.Rule, _, _ bool) {
		enumerated++
		w := walkNegatedBounds(name, negatedBoundCompile(t, name, spec), clamp)
		negatedPatterns += w.negatedPatterns
		rangedInNegated += w.rangedInNegated
		notNodes += w.notNodes
		findings = append(findings, w.findings...)
		if w.negatedPatterns > 0 {
			withNegation = append(withNegation, name)
		}
		for _, u := range w.unmodelled {
			unmodelled = append(unmodelled, fmt.Sprintf("%s: %s", name, u))
		}
	})

	t.Logf("enumerated %d executable corpus cyphers; %d NOT expressions; %d negated PATTERNS across %d lenses (%s); "+
		"%d variable-length hops inside a negation",
		enumerated, notNodes, negatedPatterns, len(withNegation), strings.Join(withNegation, ", "), rangedInNegated)

	require.Emptyf(t, unmodelled,
		"this census walked an AST shape it does not model, so its clean verdict covers less than the corpus — "+
			"add the case to walkNegatedBounds/expr rather than trusting the empty finding set:\n%s",
		strings.Join(unmodelled, "\n"))

	require.GreaterOrEqualf(t, enumerated, negatedBoundCorpusFloor,
		"the census enumerated only %d cyphers — below the floor, which means the corpus walk, not the corpus, shrank", enumerated)

	// The census's positive control: the walker demonstrably REACHES negations
	// in the real corpus, so an empty finding set is a verdict rather than an
	// artefact of never descending into a `NOT`.
	require.Positivef(t, negatedPatterns,
		"no negated pattern was reached anywhere in %d corpus cyphers — the finding set below is empty for the wrong reason", enumerated)

	if len(findings) > 0 {
		lines := make([]string, 0, len(findings))
		for _, f := range findings {
			lines = append(lines, f.String())
		}
		sort.Strings(lines)
		t.Fatalf("a variable-length hop carries a FINITE upper bound inside a negated pattern (clamp = %d). "+
			"Inside a negation a too-shallow bound is FAIL-OPEN: it drops an EXCLUSION, so a row that should have been "+
			"removed is projected and — on the auth plane — granted, silently. Open the range (`*N..`) instead of "+
			"widening the number, and never add an allowlist entry here:\n%s",
			clamp, strings.Join(lines, "\n"))
	}
}

// TestNegatedVarLengthBoundWalkerControls holds the walker to all four vectors
// of the rule on hand-written specs, so the corpus census's empty finding set
// is read against a walker proven to answer both ways.
func TestNegatedVarLengthBoundWalkerControls(t *testing.T) {
	clamp := negatedBoundClamp(t)

	atClamp := fmt.Sprintf(`MATCH (loc0:location {key: $actorKey})
WHERE NOT (loc0)-[:containedIn*0..%d]->(exLoc)<-[:unavailableAt]-(svc:service)
RETURN loc0.key AS key`, clamp)

	cases := []struct {
		name string
		spec string
		want bool
	}{
		{
			// The violation: an exclusion that stops excluding past two hops.
			name: "a finite bound inside a negation is flagged",
			spec: `MATCH (loc0:location {key: $actorKey})
WHERE NOT (loc0)-[:containedIn*1..2]->(exLoc)<-[:unavailableAt]-(svc:service)
RETURN loc0.key AS key`,
			want: true,
		},
		{
			// The sanctioned form: the executor's clamp bounds it.
			name: "an open range inside a negation is not flagged",
			spec: `MATCH (loc0:location {key: $actorKey})
WHERE NOT (loc0)-[:containedIn*1..]->(exLoc)<-[:unavailableAt]-(svc:service)
RETURN loc0.key AS key`,
			want: false,
		},
		{
			// Same bound, opposite polarity: fail-closed, so never flagged.
			name: "a finite bound in a positive pattern is not flagged",
			spec: `MATCH (loc0:location {key: $actorKey})-[:containedIn*1..2]->(exLoc)
RETURN loc0.key AS key`,
			want: false,
		},
		{
			name: "a finite bound at the executor's clamp is not flagged",
			spec: atClamp,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := walkNegatedBounds(tc.name, negatedBoundCompile(t, tc.name, tc.spec), clamp)
			require.Emptyf(t, w.unmodelled, "control spec hit an unmodelled AST shape: %v", w.unmodelled)
			if tc.want {
				require.Lenf(t, w.findings, 1, "expected exactly one finding, got %v", w.findings)
				require.Equal(t, "containedIn", w.findings[0].rel)
				return
			}
			require.Emptyf(t, w.findings, "expected no finding, got %v", w.findings)
		})
	}

	// The negated arms must actually have been walked as negated — otherwise
	// every "not flagged" row above would pass for the wrong reason.
	negated := walkNegatedBounds("control", negatedBoundCompile(t, "control", cases[1].spec), clamp)
	require.Positive(t, negated.negatedPatterns, "the WHERE NOT pattern must be reached under negation")
	positive := walkNegatedBounds("control", negatedBoundCompile(t, "control", cases[2].spec), clamp)
	require.Zero(t, positive.negatedPatterns, "a MATCH pattern carries no negation")
}
