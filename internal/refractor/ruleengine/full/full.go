// Package full is the v2 openCypher rule engine. It provides a real
// lex/parse/walk pipeline (visitor + AST) and an executor that evaluates
// the AST against Core KV and Adjacency KV.
package full

import (
	"fmt"
	"strings"

	"github.com/antlr4-go/antlr/v4"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full/cypher"
)

// Engine is the v2 engine. Satisfies ruleengine.RuleEngine.
type Engine struct{}

// New returns a ready-to-register full engine.
func New() *Engine { return &Engine{} }

// Name implements ruleengine.RuleEngine.
func (*Engine) Name() string { return ruleengine.EngineFull }

// errorListener accumulates ANTLR syntax errors so Parse can return them
// as a structured *ruleengine.ParseError instead of swallowing them.
type errorListener struct {
	*antlr.DefaultErrorListener
	errs []string
}

func (l *errorListener) SyntaxError(_ antlr.Recognizer, _ any, line, column int, msg string, _ antlr.RecognitionException) {
	l.errs = append(l.errs, fmt.Sprintf("line %d:%d %s", line, column, msg))
}

// Parse lexes, parses, and walks the rule body, returning a CompiledRule
// wrapping a Refractor-native AST. Errors collected from the ANTLR error
// listener and from the AST visitor are merged into a single ParseError.
func (*Engine) Parse(ruleBody string) (ruleengine.CompiledRule, error) {
	if strings.TrimSpace(ruleBody) == "" {
		return nil, &ruleengine.ParseError{
			Engine:  ruleengine.EngineFull,
			Message: "empty rule body",
		}
	}

	input := antlr.NewInputStream(ruleBody)

	lexer := cypher.NewCypherLexer(input)
	lexerListener := &errorListener{}
	lexer.RemoveErrorListeners()
	lexer.AddErrorListener(lexerListener)

	stream := antlr.NewCommonTokenStream(lexer, antlr.TokenDefaultChannel)

	parser := cypher.NewCypherParser(stream)
	parserListener := &errorListener{}
	parser.RemoveErrorListeners()
	parser.AddErrorListener(parserListener)
	parser.BuildParseTrees = true

	tree := parser.OC_Cypher()

	if len(lexerListener.errs) > 0 || len(parserListener.errs) > 0 {
		msgs := append([]string{}, lexerListener.errs...)
		msgs = append(msgs, parserListener.errs...)
		return nil, &ruleengine.ParseError{
			Engine:  ruleengine.EngineFull,
			Message: strings.Join(msgs, "; "),
		}
	}

	v := newASTVisitor()
	antlr.ParseTreeWalkerDefault.Walk(v, tree)

	if v.err != nil {
		return nil, &ruleengine.ParseError{
			Engine:  ruleengine.EngineFull,
			Message: v.err.Error(),
		}
	}
	if v.query == nil {
		return nil, &ruleengine.ParseError{
			Engine:  ruleengine.EngineFull,
			Message: "visitor produced no query",
		}
	}

	return &CompiledRule{Query: v.query}, nil
}

// Execute is implemented in executor.go (Story 3.1b-ii). The interface-level
// stub here is unused; the engine-neutral signature can't carry KV handles,
// so the real entry point is ExecuteWith. Execute(ctx, cr, ec) returns a
// typed error directing callers to use ExecuteWith.
