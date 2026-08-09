package full

// The `*` label-expansion sigil (dynamic-type-taxonomy-design.md §14 Fire A)
// is a grammar-level extension confined to oC_NodePattern: `(l:location*)`
// parses, the visitor records the sigil on NodePattern.LabelExpand, and
// Parse accepts it — resolution against a live taxonomy
// (internal/refractor/taxonomy) is an activation-time question
// (pipeline.useFullEngineBranches), not a parse-time one. The one refusal
// Parse still carries rejects a sigil on a multi-label pattern, which is
// ambiguous about which label it expands.
//
// These tests also pin that the sigil is confined to oC_NodePattern and does
// not leak into oC_NodeLabel, which is reused inside expressions (`n:Foo * 2`
// is multiplication, not a label-expansion typo) and that variable-length
// relationship quantifiers (`-[:r*1..3]->`) are untouched.

import (
	"testing"

	"github.com/antlr4-go/antlr/v4"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full/cypher"
)

// walkQuery lexes and parses body with the same pipeline full.go's Parse
// uses, then drives the AST visitor directly and returns it — WITHOUT
// checking visitor.err. It exists so a test can inspect the AST shape the
// visitor builds for a construct that Parse ultimately refuses (the
// label-expansion sigil): the visitor still finishes populating the
// NodePattern before recording the refusal, and Parse's own refusal path is
// asserted separately via New().Parse.
func walkQuery(t *testing.T, body string) *astVisitor {
	t.Helper()

	input := antlr.NewInputStream(body)
	lexer := cypher.NewCypherLexer(input)
	stream := antlr.NewCommonTokenStream(lexer, antlr.TokenDefaultChannel)
	parser := cypher.NewCypherParser(stream)
	parser.BuildParseTrees = true
	tree := parser.OC_Cypher()

	v := newASTVisitor()
	antlr.ParseTreeWalkerDefault.Walk(v, tree)
	require.NotNil(t, v.query, "visitor produced no query for %q (err=%v)", body, v.err)
	return v
}

// TestCypherStarTokenConstant pins CypherParserT__4 to the `'*'` literal.
// ANTLR numbers implicit literal tokens by the order they appear in the
// grammar, and the generated constant name (T__4) says nothing about which
// literal it is. visitNodePattern matches the sigil on that constant, so a
// grammar edit that introduces a literal ahead of `'*'` would renumber it and
// silently point the sigil check at a different token. Lexing `*` and
// comparing the token type is what makes that a failing test rather than a
// silent mismatch.
func TestCypherStarTokenConstant(t *testing.T) {
	lexer := cypher.NewCypherLexer(antlr.NewInputStream("*"))
	tok := lexer.NextToken()
	require.Equal(t, cypher.CypherParserT__4, tok.GetTokenType(),
		"CypherParserT__4 is no longer the '*' literal — regenerating the parser renumbered the implicit tokens")
}

func TestVisitNodePattern_LabelSigil(t *testing.T) {
	for _, tc := range []struct {
		name            string
		body            string
		wantLabel       string
		wantExpand      bool
		wantPropertyKey string // "" if no property expected
	}{
		{
			name:       "trailing sigil sets LabelExpand",
			body:       `MATCH (l:location*) RETURN l`,
			wantLabel:  "location",
			wantExpand: true,
		},
		{
			name:       "bare label leaves LabelExpand false",
			body:       `MATCH (l:location) RETURN l`,
			wantLabel:  "location",
			wantExpand: false,
		},
		{
			name:            "sigil followed by a property map still binds the properties",
			body:            `MATCH (l:location* {x: 1}) RETURN l`,
			wantLabel:       "location",
			wantExpand:      true,
			wantPropertyKey: "x",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := walkQuery(t, tc.body)
			m := firstMatch(t, v.query)
			require.Len(t, m.Patterns, 1)
			require.Len(t, m.Patterns[0].Nodes, 1)
			n := m.Patterns[0].Nodes[0]
			require.Equal(t, tc.wantLabel, n.Label)
			require.Equal(t, tc.wantExpand, n.LabelExpand)
			if tc.wantPropertyKey != "" {
				_, ok := n.Properties[tc.wantPropertyKey]
				require.True(t, ok, "expected property %q to be bound, got %+v", tc.wantPropertyKey, n.Properties)
			}
		})
	}
}

// TestParse_LabelSigilCompilesUnresolved pins the parse/activation split:
// the sigil parses AND compiles, and the resulting CompiledRule carries the
// flag through unresolved (LabelExpansion nil), since Parse has no graph
// access and cannot resolve anything itself. Resolution happens at
// activation (internal/refractor/pipeline's useFullEngineBranches), not at
// parse time.
func TestParse_LabelSigilCompilesUnresolved(t *testing.T) {
	cr, err := New().Parse(`MATCH (l:location*) RETURN l`)
	require.NoError(t, err)
	compiled, ok := cr.(*CompiledRule)
	require.True(t, ok)
	require.Nil(t, compiled.LabelExpansion)
	labels := compiled.ExpansionLabels()
	require.Equal(t, map[string]struct{}{"location": {}}, labels)
}

// TestParse_MultiLabelSigilRefused pins refusal (a): a sigil on a multi-label
// pattern is ambiguous about which label expands, so it is refused
// independently of anything the taxonomy resolver could ever answer.
func TestParse_MultiLabelSigilRefused(t *testing.T) {
	_, err := New().Parse(`MATCH (n:A:B*) RETURN n`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ambiguous")
}

// TestParse_LabelSigilNoRegressionInExpressionGrammar pins that the sigil
// stays confined to oC_NodePattern: `'*'?` was added there, NOT to
// oC_NodeLabel, because oC_NodeLabels is also reachable from
// oC_PropertyOrLabelsExpression, where a trailing `*` is the multiplication
// operator. A later "simplification" that moved the sigil into oC_NodeLabel
// would make these fail to parse or silently mis-parse as label expansion.
func TestParse_LabelSigilNoRegressionInExpressionGrammar(t *testing.T) {
	for _, body := range []string{
		`MATCH (n) WHERE n:Foo * 2 > 3 RETURN n`,
		`MATCH (n) WHERE n:Foo*2 > 3 RETURN n`,
	} {
		_, err := New().Parse(body)
		require.NoError(t, err, "expected multiplication expression to parse: %s", body)
	}
}

// TestParse_LabelSigilNoRegressionInVariableLengthRel pins that the
// relationship quantifier `*1..3` (a completely different grammar position)
// is unaffected by the node-pattern sigil addition.
func TestParse_LabelSigilNoRegressionInVariableLengthRel(t *testing.T) {
	q := parse(t, `MATCH (a)-[r:REL*1..3]->(b) RETURN a`)
	rel := firstMatch(t, q).Patterns[0].Rels[0]
	require.Equal(t, 1, rel.MinHops)
	require.Equal(t, 3, rel.MaxHops)
}

// TestParse_LabelSigilSpaceBeforeIsSyntaxError pins that the sigil binds
// tight to the label — `location *` with an intervening space is a syntax
// error, not a tolerated variant.
func TestParse_LabelSigilSpaceBeforeIsSyntaxError(t *testing.T) {
	_, err := New().Parse(`MATCH (l:location * ) RETURN l`)
	require.Error(t, err)
}
