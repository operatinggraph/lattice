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

// defaultMaxBindings caps the binding set any one stage of a single evaluation
// may materialize. It is a runaway backstop, not a workload limit: a per-anchor
// lens binds a handful of rows and a corpus lens thousands, so a legitimate
// projection never approaches it, while an unanchored scan feeding a cross
// product can otherwise grow until the host dies.
const defaultMaxBindings = 1_000_000

// Engine is the v2 engine. Satisfies ruleengine.RuleEngine.
type Engine struct {
	maxBindings int
}

// New returns a ready-to-register full engine.
func New() *Engine { return &Engine{maxBindings: defaultMaxBindings} }

// WithMaxBindings returns a copy of the engine whose per-evaluation
// binding-set cap is n; a value <= 0 disables the cap. The receiver is
// unchanged, so an engine shared across lenses cannot be reconfigured by a
// caller derived from it.
func (e *Engine) WithMaxBindings(n int) *Engine {
	c := *e
	c.maxBindings = n
	return &c
}

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
