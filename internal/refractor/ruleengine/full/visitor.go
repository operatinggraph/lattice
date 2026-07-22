// Visitor — ANTLR parse tree → Refractor-native AST.
//
// Approach: we embed cypher.BaseCypherListener (which provides no-op
// implementations for every Enter*/Exit* method on the generated listener
// interface) and override EnterOC_Cypher only. From there we recurse
// manually over the typed parse-tree context nodes, building AST nodes
// directly. This is cleaner than a stack-of-partials over hundreds of
// independent Enter/Exit pairs: the recursive descent mirrors the grammar
// production-by-production with explicit control flow and explicit error
// reporting.
//
// The walker is still driven by antlr.ParseTreeWalkerDefault.Walk(); the
// walker's first call on the root happens to be EnterOC_Cypher, which is
// where we take over. (We don't *need* Exit* hooks since we recurse
// eagerly.)
//
// All ANTLR / cypher.* types stay confined to this file plus full.go.
// The AST in ast.go is pure Go.

package full

import (
	"fmt"
	"strconv"

	"github.com/antlr4-go/antlr/v4"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full/cypher"
)

// astVisitor is the ANTLR listener that drives the recursive descent.
type astVisitor struct {
	cypher.BaseCypherListener

	query *Query
	err   error
}

func newASTVisitor() *astVisitor {
	return &astVisitor{}
}

// fail records the first error encountered. Subsequent calls are no-ops.
func (v *astVisitor) fail(format string, args ...any) {
	if v.err == nil {
		v.err = fmt.Errorf(format, args...)
	}
}

// EnterOC_Cypher kicks off the descent. The grammar guarantees a single
// statement holding a single query at the root.
func (v *astVisitor) EnterOC_Cypher(ctx *cypher.OC_CypherContext) {
	if v.err != nil {
		return
	}
	stmt := ctx.OC_Statement()
	if stmt == nil {
		v.fail("oC_Cypher: missing oC_Statement")
		return
	}
	q := stmt.OC_Query()
	if q == nil {
		v.fail("oC_Statement: missing oC_Query")
		return
	}
	rq := q.OC_RegularQuery()
	if rq == nil {
		v.fail("oC_Query: missing oC_RegularQuery (standalone CALL not supported)")
		return
	}
	if len(rq.AllOC_Union()) > 0 {
		v.fail("UNION is not supported")
		return
	}
	sq := rq.OC_SingleQuery()
	if sq == nil {
		v.fail("oC_RegularQuery: missing oC_SingleQuery")
		return
	}
	v.query = v.visitSingleQuery(sq)
}

// visitSingleQuery yields a Query with the clause list flattened.
// Grammar: singleQuery = singlePartQuery | multiPartQuery
//
//	singlePartQuery = readingClause* (return | updatingClause+ return?)
//	multiPartQuery = ( readingClause* updatingClause* with )+ singlePartQuery
func (v *astVisitor) visitSingleQuery(ctx cypher.IOC_SingleQueryContext) *Query {
	q := &Query{}
	if mp := ctx.OC_MultiPartQuery(); mp != nil {
		v.appendMultiPart(q, mp)
	}
	if sp := ctx.OC_SinglePartQuery(); sp != nil {
		v.appendSinglePart(q, sp)
	}
	return q
}

func (v *astVisitor) appendMultiPart(q *Query, ctx cypher.IOC_MultiPartQueryContext) {
	// MultiPart interleaves reading clauses, updating clauses, and WITH
	// clauses across (one or more) groups before terminating in a
	// singlePartQuery.
	//
	// The grammar lists children in source order via ctx.GetChildren(); we
	// iterate them so the resulting clause sequence matches source order.
	for _, child := range ctx.GetChildren() {
		switch c := child.(type) {
		case cypher.IOC_ReadingClauseContext:
			v.appendReadingClause(q, c)
		case cypher.IOC_WithContext:
			q.Clauses = append(q.Clauses, v.visitWith(c))
		case cypher.IOC_UpdatingClauseContext:
			v.fail("UPDATING clauses (CREATE/SET/DELETE/MERGE/REMOVE) are not supported")
		}
		if v.err != nil {
			return
		}
	}
	if sp := ctx.OC_SinglePartQuery(); sp != nil {
		v.appendSinglePart(q, sp)
	}
}

func (v *astVisitor) appendSinglePart(q *Query, ctx cypher.IOC_SinglePartQueryContext) {
	for _, child := range ctx.GetChildren() {
		switch c := child.(type) {
		case cypher.IOC_ReadingClauseContext:
			v.appendReadingClause(q, c)
		case cypher.IOC_UpdatingClauseContext:
			v.fail("UPDATING clauses (CREATE/SET/DELETE/MERGE/REMOVE) are not supported")
		case cypher.IOC_ReturnContext:
			q.Clauses = append(q.Clauses, v.visitReturn(c))
		}
		if v.err != nil {
			return
		}
	}
}

func (v *astVisitor) appendReadingClause(q *Query, ctx cypher.IOC_ReadingClauseContext) {
	for _, child := range ctx.GetChildren() {
		switch c := child.(type) {
		case cypher.IOC_MatchContext:
			q.Clauses = append(q.Clauses, v.visitMatch(c))
		case cypher.IOC_UnwindContext:
			v.fail("UNWIND is not supported")
		case cypher.IOC_InQueryCallContext:
			v.fail("CALL is not supported")
		}
	}
}

// ---------- Clauses ----------

func (v *astVisitor) visitMatch(ctx cypher.IOC_MatchContext) *Match {
	m := &Match{Optional: ctx.OPTIONAL() != nil}
	if pat := ctx.OC_Pattern(); pat != nil {
		for _, pp := range pat.AllOC_PatternPart() {
			m.Patterns = append(m.Patterns, v.visitPatternPart(pp))
		}
	}
	if w := ctx.OC_Where(); w != nil {
		m.Where = v.visitExpression(w.OC_Expression())
	}
	return m
}

func (v *astVisitor) visitWith(ctx cypher.IOC_WithContext) *With {
	w := &With{}
	pb := ctx.OC_ProjectionBody()
	if pb != nil {
		w.Distinct = pb.DISTINCT() != nil
		if items := pb.OC_ProjectionItems(); items != nil {
			for _, it := range items.AllOC_ProjectionItem() {
				w.Items = append(w.Items, v.visitProjectionItem(it))
			}
		}
	}
	if where := ctx.OC_Where(); where != nil {
		w.Where = v.visitExpression(where.OC_Expression())
	}
	return w
}

func (v *astVisitor) visitReturn(ctx cypher.IOC_ReturnContext) *Return {
	r := &Return{}
	pb := ctx.OC_ProjectionBody()
	if pb != nil {
		r.Distinct = pb.DISTINCT() != nil
		if items := pb.OC_ProjectionItems(); items != nil {
			for _, it := range items.AllOC_ProjectionItem() {
				r.Items = append(r.Items, v.visitProjectionItem(it))
			}
		}
	}
	return r
}

func (v *astVisitor) visitProjectionItem(ctx cypher.IOC_ProjectionItemContext) ProjectionItem {
	item := ProjectionItem{}
	item.Expr = v.visitExpression(ctx.OC_Expression())
	if ctx.AS() != nil {
		if vr := ctx.OC_Variable(); vr != nil {
			item.Alias = identifierText(vr)
		}
	}
	return item
}

// ---------- Patterns ----------

func (v *astVisitor) visitPatternPart(ctx cypher.IOC_PatternPartContext) PathPattern {
	if apx := ctx.OC_AnonymousPatternPart(); apx != nil {
		if pe := apx.OC_PatternElement(); pe != nil {
			return v.visitPatternElement(pe)
		}
	}
	return PathPattern{}
}

func (v *astVisitor) visitPatternElement(ctx cypher.IOC_PatternElementContext) PathPattern {
	// PatternElement permits surrounding parens; unwrap.
	if inner := ctx.OC_PatternElement(); inner != nil {
		return v.visitPatternElement(inner)
	}
	pp := PathPattern{}
	if np := ctx.OC_NodePattern(); np != nil {
		pp.Nodes = append(pp.Nodes, v.visitNodePattern(np))
	}
	for _, chain := range ctx.AllOC_PatternElementChain() {
		rp := v.visitRelPattern(chain.OC_RelationshipPattern())
		np := v.visitNodePattern(chain.OC_NodePattern())
		pp.Rels = append(pp.Rels, rp)
		pp.Nodes = append(pp.Nodes, np)
	}
	return pp
}

func (v *astVisitor) visitNodePattern(ctx cypher.IOC_NodePatternContext) NodePattern {
	np := NodePattern{}
	if vr := ctx.OC_Variable(); vr != nil {
		np.Variable = identifierText(vr)
	}
	if nls := ctx.OC_NodeLabels(); nls != nil {
		labels := nls.AllOC_NodeLabel()
		if len(labels) > 0 {
			if ln := labels[0].OC_LabelName(); ln != nil {
				np.Label = ln.GetText()
			}
		}
	}
	if props := ctx.OC_Properties(); props != nil {
		np.Properties = v.visitPropertiesMap(props)
	}
	return np
}

func (v *astVisitor) visitRelPattern(ctx cypher.IOC_RelationshipPatternContext) RelPattern {
	rp := RelPattern{MinHops: 1, MaxHops: 1}
	hasLeft := ctx.OC_LeftArrowHead() != nil
	hasRight := ctx.OC_RightArrowHead() != nil
	switch {
	case hasLeft && hasRight:
		rp.Direction = DirBoth // <-...->: bidi (rare)
	case hasLeft:
		rp.Direction = DirIn
	case hasRight:
		rp.Direction = DirOut
	default:
		rp.Direction = DirBoth
	}
	if detail := ctx.OC_RelationshipDetail(); detail != nil {
		if vr := detail.OC_Variable(); vr != nil {
			rp.Variable = identifierText(vr)
		}
		if rts := detail.OC_RelationshipTypes(); rts != nil {
			names := rts.AllOC_RelTypeName()
			if len(names) > 0 {
				if sn := names[0].OC_SchemaName(); sn != nil {
					rp.Type = sn.GetText()
				}
			}
		}
		if rl := detail.OC_RangeLiteral(); rl != nil {
			rp.MinHops, rp.MaxHops = v.visitRangeLiteral(rl)
		}
		if props := detail.OC_Properties(); props != nil {
			rp.Properties = v.visitPropertiesMap(props)
		}
	}
	return rp
}

// visitRangeLiteral interprets `*` quantifier.
//
//   - → 0, -1 (unbounded both sides; grammar permits but rare)
//     *N..        → N, -1
//     *..M        → 0, M    (grammar: leading integer is optional)
//     *N..M       → N, M
//     *N          → N, N    (single integer with no `..`)
//
// We detect `..` by checking the literal text since the rule loses the
// dot-dot terminal in the context API.
func (v *astVisitor) visitRangeLiteral(ctx cypher.IOC_RangeLiteralContext) (int, int) {
	text := ctx.GetText()
	hasDotDot := false
	for i := 0; i < len(text)-1; i++ {
		if text[i] == '.' && text[i+1] == '.' {
			hasDotDot = true
			break
		}
	}
	ints := ctx.AllOC_IntegerLiteral()
	parseInt := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0
		}
		return n
	}

	if !hasDotDot {
		if len(ints) == 0 {
			// bare `*`
			return 0, -1
		}
		n := parseInt(ints[0].GetText())
		return n, n
	}
	// has `..`
	// Could be: `*..`, `*N..`, `*..M`, `*N..M`.
	// Determine which side(s) have integers by inspecting position relative
	// to `..`.
	dotIdx := -1
	for i := 0; i < len(text)-1; i++ {
		if text[i] == '.' && text[i+1] == '.' {
			dotIdx = i
			break
		}
	}
	minHops := 0
	maxHops := -1
	for _, lit := range ints {
		lt := lit.GetText()
		// position of this token in the text:
		// crude — search for first un-consumed match
		idx := indexOfInt(text, lt)
		if idx >= 0 && idx < dotIdx {
			minHops = parseInt(lt)
		} else if idx > dotIdx {
			maxHops = parseInt(lt)
		}
	}
	return minHops, maxHops
}

// indexOfInt finds the first textual occurrence of n in s. Robust enough
// for the range literal context where the only tokens are `*`, integers,
// and `..`.
func indexOfInt(s, n string) int {
	for i := 0; i+len(n) <= len(s); i++ {
		if s[i:i+len(n)] == n {
			// avoid matching the second '..' as starting a digit
			if n != ".." && (s[i] == '.' || s[i] == '*') {
				continue
			}
			return i
		}
	}
	return -1
}

func (v *astVisitor) visitPropertiesMap(ctx cypher.IOC_PropertiesContext) map[string]Expr {
	if ml := ctx.OC_MapLiteral(); ml != nil {
		out := make(map[string]Expr)
		keys := ml.AllOC_PropertyKeyName()
		exprs := ml.AllOC_Expression()
		for i := 0; i < len(keys) && i < len(exprs); i++ {
			out[keys[i].GetText()] = v.visitExpression(exprs[i])
		}
		return out
	}
	// Parameter form `{$p}` — not used by bootstrap; record as nil.
	return nil
}

// ---------- Expressions ----------

func (v *astVisitor) visitExpression(ctx cypher.IOC_ExpressionContext) Expr {
	if ctx == nil {
		return nil
	}
	return v.visitOrExpression(ctx.OC_OrExpression())
}

func (v *astVisitor) visitOrExpression(ctx cypher.IOC_OrExpressionContext) Expr {
	xors := ctx.AllOC_XorExpression()
	if len(xors) == 1 {
		return v.visitXorExpression(xors[0])
	}
	out := &AndOr{Op: "OR"}
	for _, x := range xors {
		out.Operands = append(out.Operands, v.visitXorExpression(x))
	}
	return out
}

func (v *astVisitor) visitXorExpression(ctx cypher.IOC_XorExpressionContext) Expr {
	ands := ctx.AllOC_AndExpression()
	if len(ands) == 1 {
		return v.visitAndExpression(ands[0])
	}
	out := &AndOr{Op: "XOR"}
	for _, a := range ands {
		out.Operands = append(out.Operands, v.visitAndExpression(a))
	}
	return out
}

func (v *astVisitor) visitAndExpression(ctx cypher.IOC_AndExpressionContext) Expr {
	nots := ctx.AllOC_NotExpression()
	if len(nots) == 1 {
		return v.visitNotExpression(nots[0])
	}
	out := &AndOr{Op: "AND"}
	for _, n := range nots {
		out.Operands = append(out.Operands, v.visitNotExpression(n))
	}
	return out
}

func (v *astVisitor) visitNotExpression(ctx cypher.IOC_NotExpressionContext) Expr {
	inner := v.visitComparisonExpression(ctx.OC_ComparisonExpression())
	notCount := len(ctx.AllNOT())
	for i := 0; i < notCount; i++ {
		inner = &Not{Operand: inner}
	}
	return inner
}

func (v *astVisitor) visitComparisonExpression(ctx cypher.IOC_ComparisonExpressionContext) Expr {
	left := v.visitAddOrSubtract(ctx.OC_AddOrSubtractExpression())
	partials := ctx.AllOC_PartialComparisonExpression()
	if len(partials) == 0 {
		return left
	}
	cur := left
	for _, p := range partials {
		// Operator is the first token of the partial-comparison rule.
		op := firstNonSpaceToken(p)
		right := v.visitAddOrSubtract(p.OC_AddOrSubtractExpression())
		cur = &BinaryOp{Op: op, Left: cur, Right: right}
	}
	return cur
}

func (v *astVisitor) visitAddOrSubtract(ctx cypher.IOC_AddOrSubtractExpressionContext) Expr {
	mds := ctx.AllOC_MultiplyDivideModuloExpression()
	if len(mds) == 1 {
		return v.visitMultDivMod(mds[0])
	}
	// Walk children to read operators (+ or -) interleaved with operands.
	var cur Expr
	op := ""
	for _, child := range ctx.GetChildren() {
		switch c := child.(type) {
		case cypher.IOC_MultiplyDivideModuloExpressionContext:
			next := v.visitMultDivMod(c)
			if cur == nil {
				cur = next
			} else {
				cur = &BinaryOp{Op: op, Left: cur, Right: next}
				op = ""
			}
		case antlr.TerminalNode:
			t := c.GetText()
			if t == "+" || t == "-" {
				op = t
			}
		}
	}
	return cur
}

func (v *astVisitor) visitMultDivMod(ctx cypher.IOC_MultiplyDivideModuloExpressionContext) Expr {
	pows := ctx.AllOC_PowerOfExpression()
	if len(pows) == 1 {
		return v.visitPowerOf(pows[0])
	}
	var cur Expr
	op := ""
	for _, child := range ctx.GetChildren() {
		switch c := child.(type) {
		case cypher.IOC_PowerOfExpressionContext:
			next := v.visitPowerOf(c)
			if cur == nil {
				cur = next
			} else {
				cur = &BinaryOp{Op: op, Left: cur, Right: next}
				op = ""
			}
		case antlr.TerminalNode:
			t := c.GetText()
			if t == "*" || t == "/" || t == "%" {
				op = t
			}
		}
	}
	return cur
}

func (v *astVisitor) visitPowerOf(ctx cypher.IOC_PowerOfExpressionContext) Expr {
	uns := ctx.AllOC_UnaryAddOrSubtractExpression()
	if len(uns) == 1 {
		return v.visitUnary(uns[0])
	}
	// right-associative power: a ^ b ^ c = a ^ (b ^ c)
	last := v.visitUnary(uns[len(uns)-1])
	for i := len(uns) - 2; i >= 0; i-- {
		last = &BinaryOp{Op: "^", Left: v.visitUnary(uns[i]), Right: last}
	}
	return last
}

func (v *astVisitor) visitUnary(ctx cypher.IOC_UnaryAddOrSubtractExpressionContext) Expr {
	inner := v.visitStringListNullOp(ctx.OC_StringListNullOperatorExpression())
	// Count leading '-' tokens to flip sign; '+' is a no-op.
	negCount := 0
	for _, child := range ctx.GetChildren() {
		t, ok := child.(antlr.TerminalNode)
		if !ok {
			continue
		}
		if t.GetText() == "-" {
			negCount++
		}
	}
	if negCount%2 == 1 {
		inner = &BinaryOp{Op: "-", Left: &Literal{Value: int64(0)}, Right: inner}
	}
	return inner
}

func (v *astVisitor) visitStringListNullOp(ctx cypher.IOC_StringListNullOperatorExpressionContext) Expr {
	// 3.1b-i: pass through to oC_PropertyOrLabelsExpression. String/list/null
	// operator suffixes (STARTS WITH, IN, IS NULL, etc.) are not used by the
	// bootstrap query and are not in the required-feature list.
	return v.visitPropertyOrLabels(ctx.OC_PropertyOrLabelsExpression())
}

func (v *astVisitor) visitPropertyOrLabels(ctx cypher.IOC_PropertyOrLabelsExpressionContext) Expr {
	cur := v.visitAtom(ctx.OC_Atom())
	for _, lookup := range ctx.AllOC_PropertyLookup() {
		key := ""
		if kn := lookup.OC_PropertyKeyName(); kn != nil {
			key = kn.GetText()
		}
		cur = &PropertyAccess{Target: cur, Key: key}
	}
	return cur
}

func (v *astVisitor) visitAtom(ctx cypher.IOC_AtomContext) Expr {
	if lit := ctx.OC_Literal(); lit != nil {
		return v.visitLiteral(lit)
	}
	if p := ctx.OC_Parameter(); p != nil {
		return &ParameterRef{Name: parameterName(p)}
	}
	if fi := ctx.OC_FunctionInvocation(); fi != nil {
		return v.visitFunctionInvocation(fi)
	}
	if pc := ctx.OC_PatternComprehension(); pc != nil {
		return v.visitPatternComprehension(pc)
	}
	if rp := ctx.OC_RelationshipsPattern(); rp != nil {
		// Path expression used inside boolean context (anti-pattern).
		return &PatternExpr{Pattern: v.visitRelationshipsPattern(rp)}
	}
	if pe := ctx.OC_ParenthesizedExpression(); pe != nil {
		return v.visitExpression(pe.OC_Expression())
	}
	if vr := ctx.OC_Variable(); vr != nil {
		return &VariableRef{Name: identifierText(vr)}
	}
	if ce := ctx.OC_CaseExpression(); ce != nil {
		return v.visitCaseExpression(ce)
	}
	// ListComprehension, COUNT(*), ALL/ANY/NONE/SINGLE filter expressions
	// are out of scope for 3.1b-i.
	v.fail("unsupported atom: %q", ctx.GetText())
	return nil
}

// visitCaseExpression handles the generic form of CASE:
//
//	CASE (WHEN <cond> THEN <result>)+ (ELSE <default>)? END
//
// The simple (test-expression) form `CASE <expr> WHEN <value> THEN ...` is
// rejected — none of the lenses shipped today need it, and supporting it
// would require threading an extra equality comparison through every
// alternative.
func (v *astVisitor) visitCaseExpression(ctx cypher.IOC_CaseExpressionContext) Expr {
	alts := ctx.AllOC_CaseAlternatives()
	if len(alts) == 0 {
		v.fail("unsupported atom: %q", ctx.GetText())
		return nil
	}

	exprs := ctx.AllOC_Expression()
	var elseExpr cypher.IOC_ExpressionContext
	switch len(exprs) {
	case 0:
		// No ELSE, generic form.
	case 1:
		// Either the simple-form test-expression (before the first WHEN) or
		// a trailing ELSE (after the last alternative). Position relative to
		// the first WHEN/THEN pair disambiguates.
		if exprs[0].GetStart().GetTokenIndex() < alts[0].GetStart().GetTokenIndex() {
			v.fail("unsupported atom: %q (simple-form CASE <expr> WHEN ... is not supported)", ctx.GetText())
			return nil
		}
		elseExpr = exprs[0]
	default:
		// >=2 expressions outside the alternatives implies a simple-form
		// test-expression plus an ELSE — both unsupported together here.
		v.fail("unsupported atom: %q (simple-form CASE <expr> WHEN ... is not supported)", ctx.GetText())
		return nil
	}

	out := &CaseExpr{}
	for _, alt := range alts {
		altExprs := alt.AllOC_Expression()
		if len(altExprs) != 2 {
			v.fail("malformed CASE alternative: %q", alt.GetText())
			return nil
		}
		out.Alternatives = append(out.Alternatives, CaseWhenThen{
			When: v.visitExpression(altExprs[0]),
			Then: v.visitExpression(altExprs[1]),
		})
	}
	if elseExpr != nil {
		out.Else = v.visitExpression(elseExpr)
	}
	return out
}

func (v *astVisitor) visitLiteral(ctx cypher.IOC_LiteralContext) Expr {
	if nl := ctx.OC_NumberLiteral(); nl != nil {
		if il := nl.OC_IntegerLiteral(); il != nil {
			t := il.GetText()
			n, err := strconv.ParseInt(t, 0, 64)
			if err != nil {
				v.fail("invalid integer literal %q: %v", t, err)
				return nil
			}
			return &Literal{Value: n}
		}
		if dl := nl.OC_DoubleLiteral(); dl != nil {
			t := dl.GetText()
			f, err := strconv.ParseFloat(t, 64)
			if err != nil {
				v.fail("invalid double literal %q: %v", t, err)
				return nil
			}
			return &Literal{Value: f}
		}
	}
	if sl := ctx.StringLiteral(); sl != nil {
		raw := sl.GetText()
		// Strip the surrounding quote characters. We do not unescape here;
		// the executor (3.1b-ii) can handle that if needed. The bootstrap
		// query uses only literal strings ("task").
		if len(raw) >= 2 {
			raw = raw[1 : len(raw)-1]
		}
		return &Literal{Value: raw}
	}
	if bl := ctx.OC_BooleanLiteral(); bl != nil {
		return &Literal{Value: bl.TRUE() != nil}
	}
	if ctx.NULL() != nil {
		return &Literal{Value: nil}
	}
	if ml := ctx.OC_MapLiteral(); ml != nil {
		return v.visitMapLiteral(ml)
	}
	if ll := ctx.OC_ListLiteral(); ll != nil {
		out := &ListLiteral{}
		for _, e := range ll.AllOC_Expression() {
			out.Elements = append(out.Elements, v.visitExpression(e))
		}
		return out
	}
	v.fail("unsupported literal: %q", ctx.GetText())
	return nil
}

func (v *astVisitor) visitMapLiteral(ctx cypher.IOC_MapLiteralContext) *MapLiteral {
	ml := &MapLiteral{Values: map[string]Expr{}}
	keys := ctx.AllOC_PropertyKeyName()
	exprs := ctx.AllOC_Expression()
	for i := 0; i < len(keys) && i < len(exprs); i++ {
		k := keys[i].GetText()
		ml.Keys = append(ml.Keys, k)
		ml.Values[k] = v.visitExpression(exprs[i])
	}
	return ml
}

func (v *astVisitor) visitFunctionInvocation(ctx cypher.IOC_FunctionInvocationContext) Expr {
	fn := &FunctionCall{Distinct: ctx.DISTINCT() != nil}
	if name := ctx.OC_FunctionName(); name != nil {
		if ns := name.OC_Namespace(); ns != nil {
			for _, sn := range ns.AllOC_SymbolicName() {
				fn.Namespace = append(fn.Namespace, sn.GetText())
			}
		}
		if sn := name.OC_SymbolicName(); sn != nil {
			fn.Name = sn.GetText()
		} else if name.EXISTS() != nil {
			fn.Name = "EXISTS"
		}
	}
	for _, arg := range ctx.AllOC_Expression() {
		fn.Args = append(fn.Args, v.visitExpression(arg))
	}
	return fn
}

func (v *astVisitor) visitPatternComprehension(ctx cypher.IOC_PatternComprehensionContext) Expr {
	pc := &PatternComprehension{}
	if vr := ctx.OC_Variable(); vr != nil {
		pc.Variable = identifierText(vr)
	}
	if rp := ctx.OC_RelationshipsPattern(); rp != nil {
		pc.Pattern = v.visitRelationshipsPattern(rp)
	}
	exprs := ctx.AllOC_Expression()
	if ctx.WHERE() != nil && len(exprs) >= 2 {
		pc.Where = v.visitExpression(exprs[0])
		pc.Projection = v.visitExpression(exprs[1])
	} else if len(exprs) >= 1 {
		pc.Projection = v.visitExpression(exprs[len(exprs)-1])
	}
	return pc
}

func (v *astVisitor) visitRelationshipsPattern(ctx cypher.IOC_RelationshipsPatternContext) PathPattern {
	pp := PathPattern{}
	if np := ctx.OC_NodePattern(); np != nil {
		pp.Nodes = append(pp.Nodes, v.visitNodePattern(np))
	}
	for _, chain := range ctx.AllOC_PatternElementChain() {
		pp.Rels = append(pp.Rels, v.visitRelPattern(chain.OC_RelationshipPattern()))
		pp.Nodes = append(pp.Nodes, v.visitNodePattern(chain.OC_NodePattern()))
	}
	return pp
}

// ---------- Small helpers ----------

// parameterName returns the symbolic name of `$name` (or the decimal int form,
// rare). The leading `$` is stripped.
func parameterName(ctx cypher.IOC_ParameterContext) string {
	if sn := ctx.OC_SymbolicName(); sn != nil {
		return sn.GetText()
	}
	if di := ctx.DecimalInteger(); di != nil {
		return di.GetText()
	}
	// Fallback: drop leading `$` from raw text.
	t := ctx.GetText()
	if len(t) > 0 && t[0] == '$' {
		return t[1:]
	}
	return t
}

// identifierText returns the raw text of an oC_Variable (no escape decoding).
func identifierText(ctx cypher.IOC_VariableContext) string {
	if sn := ctx.OC_SymbolicName(); sn != nil {
		return sn.GetText()
	}
	return ctx.GetText()
}

// firstNonSpaceToken finds the first terminal token text inside a parse-tree
// context that is not whitespace. Used to extract the comparison operator
// out of an oC_PartialComparisonExpression node, whose grammar shape is
// (op SP? expr).
func firstNonSpaceToken(ctx antlr.ParserRuleContext) string {
	for _, child := range ctx.GetChildren() {
		t, ok := child.(antlr.TerminalNode)
		if !ok {
			continue
		}
		s := t.GetText()
		// Skip SP (which the lexer surfaces as plain space characters).
		trimmed := ""
		for _, r := range s {
			if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
				trimmed += string(r)
			}
		}
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
