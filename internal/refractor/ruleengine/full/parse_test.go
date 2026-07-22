package full

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	identityhygiene "github.com/operatinggraph/lattice/packages/identity-hygiene"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

// parse compiles a body via the public Engine API and returns the wrapped
// *Query for assertions. Test fails on parse error.
func parse(t *testing.T, body string) *Query {
	t.Helper()
	cr, err := New().Parse(body)
	if err != nil {
		t.Fatalf("Parse(%q) returned error: %v", body, err)
	}
	c, ok := cr.(*CompiledRule)
	if !ok {
		t.Fatalf("Parse returned %T, want *CompiledRule", cr)
	}
	if c.Query == nil {
		t.Fatalf("CompiledRule.Query is nil")
	}
	return c.Query
}

func firstMatch(t *testing.T, q *Query) *Match {
	t.Helper()
	for _, c := range q.Clauses {
		if m, ok := c.(*Match); ok {
			return m
		}
	}
	t.Fatalf("no MATCH clause found in query (clauses=%d)", len(q.Clauses))
	return nil
}

func firstReturn(t *testing.T, q *Query) *Return {
	t.Helper()
	for _, c := range q.Clauses {
		if r, ok := c.(*Return); ok {
			return r
		}
	}
	t.Fatalf("no RETURN clause found in query")
	return nil
}

// --- Per-feature parse tests (Decision #8 list) ---

func TestParse_MatchSimple(t *testing.T) {
	q := parse(t, `MATCH (a:agreement {key: "x"}) RETURN a.id`)
	m := firstMatch(t, q)
	if m.Optional {
		t.Fatalf("expected non-optional match")
	}
	if len(m.Patterns) != 1 || len(m.Patterns[0].Nodes) != 1 {
		t.Fatalf("expected one pattern with one node, got %+v", m.Patterns)
	}
	n := m.Patterns[0].Nodes[0]
	if n.Variable != "a" || n.Label != "agreement" {
		t.Fatalf("unexpected node: %+v", n)
	}
	if _, ok := n.Properties["key"]; !ok {
		t.Fatalf("expected property 'key' on node, got %+v", n.Properties)
	}
}

func TestParse_OptionalMatch(t *testing.T) {
	q := parse(t, `MATCH (a) OPTIONAL MATCH (a)-[:r]->(b) RETURN a, b`)
	if len(q.Clauses) < 2 {
		t.Fatalf("expected at least 2 clauses, got %d", len(q.Clauses))
	}
	m, ok := q.Clauses[1].(*Match)
	if !ok || !m.Optional {
		t.Fatalf("expected second clause to be OPTIONAL MATCH, got %+v", q.Clauses[1])
	}
}

func TestParse_OutboundRel(t *testing.T) {
	q := parse(t, `MATCH (a)-[:r]->(b) RETURN a`)
	m := firstMatch(t, q)
	if got := m.Patterns[0].Rels[0].Direction; got != DirOut {
		t.Fatalf("expected DirOut, got %v", got)
	}
	if m.Patterns[0].Rels[0].Type != "r" {
		t.Fatalf("expected rel type 'r', got %q", m.Patterns[0].Rels[0].Type)
	}
}

func TestParse_InboundRel(t *testing.T) {
	q := parse(t, `MATCH (a)<-[:r]-(b) RETURN a`)
	m := firstMatch(t, q)
	if got := m.Patterns[0].Rels[0].Direction; got != DirIn {
		t.Fatalf("expected DirIn, got %v", got)
	}
}

func TestParse_VarLengthZeroToUnbounded(t *testing.T) {
	q := parse(t, `MATCH (a)-[:r*0..]->(b) RETURN a`)
	rel := firstMatch(t, q).Patterns[0].Rels[0]
	if rel.MinHops != 0 || rel.MaxHops != -1 {
		t.Fatalf("expected MinHops=0 MaxHops=-1, got %+v", rel)
	}
}

func TestParse_VarLengthBounded(t *testing.T) {
	q := parse(t, `MATCH (a)-[:r*0..2]->(b) RETURN a`)
	rel := firstMatch(t, q).Patterns[0].Rels[0]
	if rel.MinHops != 0 || rel.MaxHops != 2 {
		t.Fatalf("expected MinHops=0 MaxHops=2, got %+v", rel)
	}
}

func TestParse_WhereAndOr(t *testing.T) {
	q := parse(t, `MATCH (a) WHERE a.x = 1 AND a.y = 2 OR a.z = 3 RETURN a`)
	m := firstMatch(t, q)
	if m.Where == nil {
		t.Fatalf("expected WHERE clause")
	}
	// Top-level should be OR over (AND, eq).
	or, ok := m.Where.(*AndOr)
	if !ok || or.Op != "OR" {
		t.Fatalf("expected top-level OR, got %T %+v", m.Where, m.Where)
	}
	if len(or.Operands) != 2 {
		t.Fatalf("expected 2 OR operands, got %d", len(or.Operands))
	}
	and, ok := or.Operands[0].(*AndOr)
	if !ok || and.Op != "AND" {
		t.Fatalf("expected first OR operand to be AND, got %T", or.Operands[0])
	}
	if len(and.Operands) != 2 {
		t.Fatalf("expected 2 AND operands, got %d", len(and.Operands))
	}
}

func TestParse_WhereAntiPattern(t *testing.T) {
	q := parse(t, `MATCH (a) WHERE NOT (a)-[:r]->(b) RETURN a`)
	m := firstMatch(t, q)
	not, ok := m.Where.(*Not)
	if !ok {
		t.Fatalf("expected Not at top, got %T", m.Where)
	}
	if _, ok := not.Operand.(*PatternExpr); !ok {
		t.Fatalf("expected PatternExpr under Not, got %T", not.Operand)
	}
}

func TestParse_WithChain(t *testing.T) {
	q := parse(t, `MATCH (a) WITH a AS aa MATCH (aa)-[:r]->(b) RETURN aa, b`)
	var foundWith bool
	var matchesAfter int
	sawWith := false
	for _, c := range q.Clauses {
		switch c.(type) {
		case *With:
			foundWith = true
			sawWith = true
		case *Match:
			if sawWith {
				matchesAfter++
			}
		}
	}
	if !foundWith {
		t.Fatalf("expected a WITH clause, clauses=%+v", q.Clauses)
	}
	if matchesAfter < 1 {
		t.Fatalf("expected at least one MATCH after WITH")
	}
}

func TestParse_ReturnMapLiteral(t *testing.T) {
	q := parse(t, `MATCH (a) RETURN {x: a.x, y: a.y} AS m`)
	r := firstReturn(t, q)
	if len(r.Items) != 1 {
		t.Fatalf("expected 1 return item, got %d", len(r.Items))
	}
	ml, ok := r.Items[0].Expr.(*MapLiteral)
	if !ok {
		t.Fatalf("expected MapLiteral, got %T", r.Items[0].Expr)
	}
	if len(ml.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %v", ml.Keys)
	}
	if r.Items[0].Alias != "m" {
		t.Fatalf("expected alias 'm', got %q", r.Items[0].Alias)
	}
}

func TestParse_CollectDistinct(t *testing.T) {
	q := parse(t, `MATCH (a) RETURN collect(DISTINCT a.x) AS xs`)
	r := firstReturn(t, q)
	fc, ok := r.Items[0].Expr.(*FunctionCall)
	if !ok {
		t.Fatalf("expected FunctionCall, got %T", r.Items[0].Expr)
	}
	if fc.Name != "collect" {
		t.Fatalf("expected name 'collect', got %q", fc.Name)
	}
	if !fc.Distinct {
		t.Fatalf("expected Distinct=true")
	}
}

func TestParse_ListConcat(t *testing.T) {
	q := parse(t, `MATCH (a) RETURN collect(a.x) + collect(a.y) AS combined`)
	r := firstReturn(t, q)
	bo, ok := r.Items[0].Expr.(*BinaryOp)
	if !ok || bo.Op != "+" {
		t.Fatalf("expected BinaryOp +, got %T %+v", r.Items[0].Expr, r.Items[0].Expr)
	}
	if _, ok := bo.Left.(*FunctionCall); !ok {
		t.Fatalf("expected left to be FunctionCall, got %T", bo.Left)
	}
	if _, ok := bo.Right.(*FunctionCall); !ok {
		t.Fatalf("expected right to be FunctionCall, got %T", bo.Right)
	}
}

func TestParse_Parameters(t *testing.T) {
	q := parse(t, `MATCH (a:identity {key: $actorKey}) WHERE a.expiresAt > $now RETURN $projectedAt AS at`)
	m := firstMatch(t, q)
	props := m.Patterns[0].Nodes[0].Properties
	pr, ok := props["key"].(*ParameterRef)
	if !ok || pr.Name != "actorKey" {
		t.Fatalf("expected ParameterRef actorKey, got %+v", props["key"])
	}
	// WHERE side: a.expiresAt > $now
	bo, ok := m.Where.(*BinaryOp)
	if !ok || bo.Op != ">" {
		t.Fatalf("expected BinaryOp '>' in WHERE, got %T %+v", m.Where, m.Where)
	}
	pn, ok := bo.Right.(*ParameterRef)
	if !ok || pn.Name != "now" {
		t.Fatalf("expected ParameterRef now on RHS, got %+v", bo.Right)
	}
	r := firstReturn(t, q)
	if pr2, ok := r.Items[0].Expr.(*ParameterRef); !ok || pr2.Name != "projectedAt" {
		t.Fatalf("expected RETURN $projectedAt, got %+v", r.Items[0].Expr)
	}
}

func TestParse_PropertyAccess(t *testing.T) {
	q := parse(t, `MATCH (a) RETURN a.x AS x, a.x.y AS xy`)
	r := firstReturn(t, q)
	pa, ok := r.Items[0].Expr.(*PropertyAccess)
	if !ok || pa.Key != "x" {
		t.Fatalf("expected PropertyAccess a.x, got %+v", r.Items[0].Expr)
	}
	// nested
	pa2, ok := r.Items[1].Expr.(*PropertyAccess)
	if !ok || pa2.Key != "y" {
		t.Fatalf("expected nested PropertyAccess .y, got %+v", r.Items[1].Expr)
	}
	if inner, ok := pa2.Target.(*PropertyAccess); !ok || inner.Key != "x" {
		t.Fatalf("expected inner .x, got %+v", pa2.Target)
	}
}

func TestParse_CaseExpression(t *testing.T) {
	q := parse(t, `MATCH (a), (b) RETURN CASE
         WHEN a.email = b.email THEN 'exact-email'
         WHEN a.phone = b.phone THEN 'exact-phone'
         ELSE 'levenshtein-name'
       END AS criterion`)
	r := firstReturn(t, q)
	if len(r.Items) != 1 {
		t.Fatalf("expected 1 return item, got %d", len(r.Items))
	}
	if r.Items[0].Alias != "criterion" {
		t.Fatalf("expected alias 'criterion', got %q", r.Items[0].Alias)
	}
	ce, ok := r.Items[0].Expr.(*CaseExpr)
	if !ok {
		t.Fatalf("expected *CaseExpr, got %T", r.Items[0].Expr)
	}
	if len(ce.Alternatives) != 2 {
		t.Fatalf("expected 2 WHEN/THEN alternatives, got %d", len(ce.Alternatives))
	}
	if _, ok := ce.Alternatives[0].When.(*BinaryOp); !ok {
		t.Fatalf("expected first WHEN to be a BinaryOp, got %T", ce.Alternatives[0].When)
	}
	if lit, ok := ce.Alternatives[0].Then.(*Literal); !ok || lit.Value != "exact-email" {
		t.Fatalf("expected first THEN literal 'exact-email', got %+v", ce.Alternatives[0].Then)
	}
	if ce.Else == nil {
		t.Fatalf("expected ELSE branch to be present")
	}
	if lit, ok := ce.Else.(*Literal); !ok || lit.Value != "levenshtein-name" {
		t.Fatalf("expected ELSE literal 'levenshtein-name', got %+v", ce.Else)
	}
}

// --- Bootstrap acceptance oracle ---

func TestParse_BootstrapCapabilityLens(t *testing.T) {
	body := bootstrap.CapabilityLensDefinition().CypherRule
	q := parse(t, body)

	var matchCount, optMatchCount, returnCount int
	for _, c := range q.Clauses {
		switch m := c.(type) {
		case *Match:
			if m.Optional {
				optMatchCount++
			} else {
				matchCount++
			}
		case *Return:
			returnCount++
		}
	}
	// The primordial-identity anchor is a single required MATCH (with an
	// inline holdsRole->role hop, Contract #7 §7.7) whose WHERE restricts
	// projection to identities holding the primordial `operator` role, and
	// exactly one RETURN. It references no rbac PERMISSION vocabulary, so it
	// carries no OPTIONAL MATCH and no anti-pattern (the grantedBy/permission
	// walk lives in rbac-domain's capabilityRoles lens).
	if matchCount != 1 {
		t.Fatalf("expected exactly one MATCH, got %d", matchCount)
	}
	if optMatchCount != 0 {
		t.Fatalf("expected no OPTIONAL MATCH, got %d", optMatchCount)
	}
	if returnCount != 1 {
		t.Fatalf("expected exactly 1 RETURN, got %d", returnCount)
	}

	// The anchor MATCH carries a WHERE gating on the operator role's canonicalName.
	var anchorWhere Expr
	for _, c := range q.Clauses {
		if m, ok := c.(*Match); ok && !m.Optional && m.Where != nil {
			anchorWhere = m.Where
			break
		}
	}
	if anchorWhere == nil {
		t.Fatalf("expected the anchor MATCH to carry a WHERE clause")
	}

	// RETURN projects platformPermissions as a literal list of map literals —
	// the fixed kernel root-grant set, NOT a graph walk.
	r := firstReturn(t, q)
	var foundListLit, foundMapLit bool
	for _, it := range r.Items {
		walkExpr(it.Expr, func(e Expr) {
			if _, ok := e.(*ListLiteral); ok {
				foundListLit = true
			}
			if _, ok := e.(*MapLiteral); ok {
				foundMapLit = true
			}
		})
	}
	if !foundListLit {
		t.Fatalf("expected a list literal in RETURN")
	}
	if !foundMapLit {
		t.Fatalf("expected a map literal in RETURN")
	}
}

// TestParse_RbacCapabilityRolesLens parses rbac-domain's capabilityRoles lens
// spec through the full engine and asserts it carries the role/permission walk
// (collect(DISTINCT ...) over the OPTIONAL holdsRole→role←grantedBy→permission
// pattern) the primordial anchor no longer contains.
func TestParse_RbacCapabilityRolesLens(t *testing.T) {
	var body string
	for _, l := range rbacdomain.Lenses() {
		if l.CanonicalName == "capabilityRoles" {
			body = l.Spec
		}
	}
	if body == "" {
		t.Fatal("rbac-domain must declare a capabilityRoles lens")
	}
	q := parse(t, body)

	var optMatchCount int
	for _, c := range q.Clauses {
		if m, ok := c.(*Match); ok && m.Optional {
			optMatchCount++
		}
	}
	if optMatchCount < 1 {
		t.Fatalf("expected at least one OPTIONAL MATCH in capabilityRoles, got %d", optMatchCount)
	}

	r := firstReturn(t, q)
	var foundCollectDistinct, foundMapLit bool
	for _, it := range r.Items {
		walkExpr(it.Expr, func(e Expr) {
			if fc, ok := e.(*FunctionCall); ok && strings.EqualFold(fc.Name, "collect") && fc.Distinct {
				foundCollectDistinct = true
			}
			if _, ok := e.(*MapLiteral); ok {
				foundMapLit = true
			}
		})
	}
	if !foundCollectDistinct {
		t.Fatalf("expected collect(DISTINCT ...) in capabilityRoles RETURN")
	}
	if !foundMapLit {
		t.Fatalf("expected a map literal in capabilityRoles RETURN")
	}
}

// TestParse_IdentityHygieneDuplicateCandidatesLens parses the real
// `duplicateCandidates` lens spec from packages/identity-hygiene through the
// full engine — the same engine selected at activation time via
// `engine: "full"` — and asserts it compiles with the minimal, PII-free
// duplicateOf-link-traversal RETURN shape
// (dedup-over-encrypted-pii-design.md §3.3): no PII detail columns, no edge
// enumeration, just the pair's bare NanoIDs + full keys.
func TestParse_IdentityHygieneDuplicateCandidatesLens(t *testing.T) {
	if len(identityhygiene.Package.Lenses) != 1 {
		t.Fatalf("expected exactly 1 lens, got %d", len(identityhygiene.Package.Lenses))
	}
	lens := identityhygiene.Package.Lenses[0]
	if lens.Engine != "full" {
		t.Fatalf("expected lens.Engine=%q, got %q", "full", lens.Engine)
	}

	q := parse(t, lens.Spec)

	r := firstReturn(t, q)
	wantAliases := []string{"primaryId", "secondaryId", "primaryKey", "secondaryKey"}
	if len(r.Items) != len(wantAliases) {
		t.Fatalf("expected %d RETURN items, got %d: %+v", len(wantAliases), len(r.Items), r.Items)
	}
	for i, want := range wantAliases {
		if got := r.Items[i].Alias; got != want {
			t.Fatalf("RETURN item[%d] alias = %q, want %q", i, got, want)
		}
	}
}

// walkExpr applies f to every expression node reachable from root.
func walkExpr(root Expr, f func(Expr)) {
	if root == nil {
		return
	}
	f(root)
	switch e := root.(type) {
	case *AndOr:
		for _, op := range e.Operands {
			walkExpr(op, f)
		}
	case *Not:
		walkExpr(e.Operand, f)
	case *BinaryOp:
		walkExpr(e.Left, f)
		walkExpr(e.Right, f)
	case *PropertyAccess:
		walkExpr(e.Target, f)
	case *FunctionCall:
		for _, a := range e.Args {
			walkExpr(a, f)
		}
	case *MapLiteral:
		for _, k := range e.Keys {
			walkExpr(e.Values[k], f)
		}
	case *ListLiteral:
		for _, el := range e.Elements {
			walkExpr(el, f)
		}
	case *PatternComprehension:
		walkExpr(e.Where, f)
		walkExpr(e.Projection, f)
	case *CaseExpr:
		for _, alt := range e.Alternatives {
			walkExpr(alt.When, f)
			walkExpr(alt.Then, f)
		}
		walkExpr(e.Else, f)
	}
}
