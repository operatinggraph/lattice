package full

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// TestVariableRefsFieldCompleteness gates CollectVariableRefs (bindings.go)
// against silently dropping a dependency: for every concrete Expr
// implementation in this package, and every field of it that can carry a
// nested expression or pattern, a *VariableRef planted in that field must
// come back either named in CollectVariableRefs' result or with unknown=true.
//
// Both halves of the universe are discovered mechanically from the package's
// own source via go/ast rather than trusted to a hand-maintained list, so a
// new Expr type or a new expression-bearing field fails this test by name
// instead of shipping unwalked.
func TestVariableRefsFieldCompleteness(t *testing.T) {
	probes := variableRefProbes()

	t.Run("ExprUniverse", func(t *testing.T) {
		discovered := discoverExprTypes(t)
		covered := make([]string, 0, len(coveredExprTypes))
		for name := range coveredExprTypes {
			covered = append(covered, name)
		}
		sort.Strings(covered)
		if diff := diffStringSets(discovered, covered); diff != "" {
			t.Fatalf("concrete Expr implementations (found via go/ast isExpr() methods) do not match this test's coverage table:\n%s", diff)
		}
	})

	t.Run("FieldCoverage", func(t *testing.T) {
		fieldTypes := loadStructFieldTypes(t)
		for typeName, entry := range coveredExprTypes {
			fields, ok := fieldTypes[typeName]
			if !ok {
				t.Fatalf("%s has an isExpr() method but no struct declaration was found in ast.go", typeName)
			}
			var actualCarriers []string
			for fieldName, typeAST := range fields {
				cls := classifyFieldType(typeAST)
				switch {
				case carrierFieldTypes[cls]:
					actualCarriers = append(actualCarriers, fieldName)
				case inertFieldTypes[cls]:
					// Carries no nested expression or pattern; no probe needed.
				default:
					t.Fatalf("%s.%s has type %q, which this test does not classify as either expression-bearing or inert. "+
						"Add it to carrierFieldTypes or inertFieldTypes in this file, and if it is expression-bearing, add a probe for it.",
						typeName, fieldName, cls)
				}
			}
			sort.Strings(actualCarriers)
			want := append([]string(nil), entry.carrierFields...)
			sort.Strings(want)
			if diff := diffStringSets(actualCarriers, want); diff != "" {
				t.Fatalf("%s's expression-bearing fields (found via go/ast) do not match this test's probe coverage table:\n%s", typeName, diff)
			}
		}
	})

	t.Run("ProbeTableCoversFieldTable", func(t *testing.T) {
		for typeName, entry := range coveredExprTypes {
			for _, field := range entry.carrierFields {
				prefix := typeName + "." + field
				found := false
				for _, p := range probes {
					if strings.HasPrefix(p.name, prefix) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("no probe exercises %s — a field the coverage table lists as expression-bearing has no probe planting a variable reference there", prefix)
				}
			}
		}
	})

	t.Run("Probes", func(t *testing.T) {
		for _, p := range probes {
			t.Run(p.name, func(t *testing.T) {
				names, unknown := CollectVariableRefs(p.expr)
				if !names["probeVar"] && !unknown {
					t.Fatalf("%s drops a variable reference silently: names=%v unknown=%v", p.name, names, unknown)
				}
			})
		}
	})
}

// exprFieldCoverage records, for one concrete Expr implementation, the
// fields this test asserts (and probes) as expression-bearing. A type with
// no such fields — Literal, ParameterRef, VariableRef — is still an entry
// here with a nil list, so it counts as covered rather than silently
// omitted from the universe.
type exprFieldCoverage struct {
	carrierFields []string
}

// coveredExprTypes is the hand-authored side of the gate: what this file
// claims to have probed. TestVariableRefsFieldCompleteness/ExprUniverse and
// /FieldCoverage check it against the package's actual source via go/ast —
// this map is never itself treated as the source of truth for what exists.
var coveredExprTypes = map[string]exprFieldCoverage{
	"Literal":              {},
	"ParameterRef":         {},
	"VariableRef":          {},
	"PropertyAccess":       {carrierFields: []string{"Target"}},
	"BinaryOp":             {carrierFields: []string{"Left", "Right"}},
	"AndOr":                {carrierFields: []string{"Operands"}},
	"Not":                  {carrierFields: []string{"Operand"}},
	"PatternExpr":          {carrierFields: []string{"Pattern"}},
	"FunctionCall":         {carrierFields: []string{"Args"}},
	"MapLiteral":           {carrierFields: []string{"Values"}},
	"ListLiteral":          {carrierFields: []string{"Elements"}},
	"PatternComprehension": {carrierFields: []string{"Pattern", "Where", "Projection"}},
	"CaseExpr":             {carrierFields: []string{"Alternatives", "Else"}},
}

// carrierFieldTypes are the field-type shapes this test treats as able to
// transitively hold an expression or a pattern.
var carrierFieldTypes = map[string]bool{
	"Expr":            true,
	"[]Expr":          true,
	"map[string]Expr": true,
	"PathPattern":     true,
	"[]CaseWhenThen":  true,
}

// inertFieldTypes are field-type shapes known to carry no expression or
// pattern, so CollectVariableRefs has nothing to walk there.
var inertFieldTypes = map[string]bool{
	"string":    true,
	"[]string":  true,
	"bool":      true,
	"int":       true,
	"any":       true,
	"Direction": true,
}

// discoverExprTypes mechanically finds every concrete type in ast.go with an
// `isExpr()` method — the package's own marker for "implements Expr" — by
// parsing the file's AST rather than trusting a maintained list.
func discoverExprTypes(t *testing.T) []string {
	t.Helper()
	file := parseAstGo(t)
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "isExpr" || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		ident, ok := star.X.(*ast.Ident)
		if !ok {
			continue
		}
		names = append(names, ident.Name)
	}
	sort.Strings(names)
	return names
}

// loadStructFieldTypes parses ast.go and returns, for every struct type
// declared there, a map from field name to that field's type AST node.
func loadStructFieldTypes(t *testing.T) map[string]map[string]ast.Expr {
	t.Helper()
	file := parseAstGo(t)
	out := map[string]map[string]ast.Expr{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields := map[string]ast.Expr{}
			for _, f := range st.Fields.List {
				for _, n := range f.Names {
					fields[n.Name] = f.Type
				}
			}
			out[ts.Name.Name] = fields
		}
	}
	return out
}

func parseAstGo(t *testing.T) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "ast.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing ast.go: %v", err)
	}
	return file
}

// classifyFieldType renders a struct field's type AST into the same short
// form used as keys in carrierFieldTypes/inertFieldTypes: "Expr", "[]Expr",
// "map[string]Expr", "PathPattern", and so on. A shape this function cannot
// render falls through to a marker string that matches neither table, which
// is what forces FieldCoverage to fail loudly rather than skip it.
func classifyFieldType(t ast.Expr) string {
	switch x := t.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + classifyFieldType(x.X)
	case *ast.ArrayType:
		return "[]" + classifyFieldType(x.Elt)
	case *ast.MapType:
		return "map[" + classifyFieldType(x.Key) + "]" + classifyFieldType(x.Value)
	default:
		return fmt.Sprintf("<unrecognized:%T>", t)
	}
}

// diffStringSets reports, as human-readable lines, elements present in
// discovered but not in covered ("needs coverage") and elements present in
// covered but not in discovered ("stale — source moved on"). Empty string
// means the two sets are equal.
func diffStringSets(discovered, covered []string) string {
	d := map[string]bool{}
	for _, x := range discovered {
		d[x] = true
	}
	c := map[string]bool{}
	for _, x := range covered {
		c[x] = true
	}
	var uncovered, stale []string
	for x := range d {
		if !c[x] {
			uncovered = append(uncovered, x)
		}
	}
	for x := range c {
		if !d[x] {
			stale = append(stale, x)
		}
	}
	sort.Strings(uncovered)
	sort.Strings(stale)
	var b strings.Builder
	if len(uncovered) > 0 {
		fmt.Fprintf(&b, "  present in source but not covered by this test: %v\n", uncovered)
	}
	if len(stale) > 0 {
		fmt.Fprintf(&b, "  covered by this test but not found in source (stale entry — remove it): %v\n", stale)
	}
	return b.String()
}

// probeVar is the variable reference every probe below plants; each probe's
// expression tree is otherwise the smallest valid instance that reaches the
// field under test.
func probeVar() *VariableRef { return &VariableRef{Name: "probeVar"} }

// probePathPatternNodeVariable places the probe as a node's own binding —
// the direct analogue of `(probeVar:label)`.
func probePathPatternNodeVariable() PathPattern {
	return PathPattern{Nodes: []NodePattern{{Variable: "probeVar"}}}
}

// probePathPatternNodeProperty places the probe inside a node's property
// map, the exact shape of the bug this gate exists to pin:
// `(:task {key: probeVar})`.
func probePathPatternNodeProperty() PathPattern {
	return PathPattern{Nodes: []NodePattern{{
		Variable:   "x",
		Properties: map[string]Expr{"key": probeVar()},
	}}}
}

// probePathPatternRelProperty is the same shape on a relationship's
// property map: `(a)-[:rel {key: probeVar}]->(b)`.
func probePathPatternRelProperty() PathPattern {
	return PathPattern{
		Nodes: []NodePattern{{Variable: "a"}, {Variable: "b"}},
		Rels:  []RelPattern{{Variable: "r", Properties: map[string]Expr{"key": probeVar()}}},
	}
}

type variableRefProbe struct {
	name string
	expr Expr
}

// variableRefProbes is the table driving the Probes subtest. Every entry's
// name is "<Type>.<Field>[ detail]" so ProbeTableCoversFieldTable can match
// it back against coveredExprTypes by prefix.
func variableRefProbes() []variableRefProbe {
	return []variableRefProbe{
		{"PropertyAccess.Target", &PropertyAccess{Target: probeVar(), Key: "k"}},
		{"BinaryOp.Left", &BinaryOp{Op: "=", Left: probeVar(), Right: &Literal{Value: int64(1)}}},
		{"BinaryOp.Right", &BinaryOp{Op: "=", Left: &Literal{Value: int64(1)}, Right: probeVar()}},
		{"AndOr.Operands", &AndOr{Op: "AND", Operands: []Expr{&Literal{Value: true}, probeVar()}}},
		{"Not.Operand", &Not{Operand: probeVar()}},
		{"FunctionCall.Args", &FunctionCall{Name: "f", Args: []Expr{probeVar()}}},
		{"MapLiteral.Values", &MapLiteral{Keys: []string{"k"}, Values: map[string]Expr{"k": probeVar()}}},
		{"ListLiteral.Elements", &ListLiteral{Elements: []Expr{probeVar()}}},

		{"CaseExpr.Alternatives When", &CaseExpr{Alternatives: []CaseWhenThen{
			{When: probeVar(), Then: &Literal{Value: int64(1)}},
		}}},
		{"CaseExpr.Alternatives Then", &CaseExpr{Alternatives: []CaseWhenThen{
			{When: &Literal{Value: true}, Then: probeVar()},
		}}},
		{"CaseExpr.Else", &CaseExpr{
			Alternatives: []CaseWhenThen{{When: &Literal{Value: true}, Then: &Literal{Value: int64(1)}}},
			Else:         probeVar(),
		}},

		{"PatternExpr.Pattern node Variable", &PatternExpr{Pattern: probePathPatternNodeVariable()}},
		{"PatternExpr.Pattern node Properties value", &PatternExpr{Pattern: probePathPatternNodeProperty()}},
		{"PatternExpr.Pattern rel Properties value", &PatternExpr{Pattern: probePathPatternRelProperty()}},

		{"PatternComprehension.Pattern node Variable", &PatternComprehension{
			Pattern: probePathPatternNodeVariable(), Projection: &Literal{Value: int64(1)},
		}},
		{"PatternComprehension.Pattern node Properties value", &PatternComprehension{
			Pattern: probePathPatternNodeProperty(), Projection: &Literal{Value: int64(1)},
		}},
		{"PatternComprehension.Pattern rel Properties value", &PatternComprehension{
			Pattern: probePathPatternRelProperty(), Projection: &Literal{Value: int64(1)},
		}},
		{"PatternComprehension.Where", &PatternComprehension{
			Pattern: probePathPatternNodeVariable(), Where: probeVar(), Projection: &Literal{Value: int64(1)},
		}},
		{"PatternComprehension.Projection", &PatternComprehension{
			Pattern: probePathPatternNodeVariable(), Projection: probeVar(),
		}},
	}
}
