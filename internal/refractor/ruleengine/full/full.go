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
	// hubReadScope overrides the package-wide hub read-scope posture for this
	// engine alone; HubReadScopeModeUnset (the zero value) takes the default.
	hubReadScope HubReadScopeMode

	// prefetchDisabled takes this engine's evaluations down the point-read
	// path for every node and aspect read — one key per round trip, no batched
	// prefetch. The zero value batches, which is what a registered engine does;
	// the comparison tests that pin the two paths to the same rows and the same
	// read-surface footprint are what set it.
	prefetchDisabled bool
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

// WithHubReadScopeMode returns a copy of the engine whose hub read-scope
// posture is m; HubReadScopeModeUnset returns it to the package default. The
// receiver is unchanged — an engine shared across lenses cannot be
// reconfigured by a caller derived from it, and this is the form a test uses
// so it never mutates package state.
func (e *Engine) WithHubReadScopeMode(m HubReadScopeMode) *Engine {
	c := *e
	c.hubReadScope = m
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
	// A relationship variable projects its link key and its relation name and
	// nothing else, so the shapes that would execute into a silent column of
	// nulls are refused here, in front of the author, rather than at evaluation
	// where the only symptom is an empty column (relbinding.go).
	if reject := relBindingReject(v.query); reject != "" {
		return nil, &ruleengine.ParseError{
			Engine:  ruleengine.EngineFull,
			Message: reject,
		}
	}
	// A required MATCH that binds nothing new expands into no row the executor
	// recognises as a match, so it drops the rows it reads as filtering
	// (requiredmatch.go). The judgement needs the clause list in source order,
	// which is why it runs here rather than in the visitor.
	if reject := requiredMatchReject(v.query); reject != "" {
		return nil, &ruleengine.ParseError{
			Engine:  ruleengine.EngineFull,
			Message: reject,
		}
	}

	branchStages, branchDeferred := analyseBranchDecomposition(v.query)
	return &CompiledRule{
		Query:             v.query,
		groupingRedundant: analyseGroupingRedundancy(v.query),
		branchStages:      branchStages,
		branchDeferred:    branchDeferred,
	}, nil
}

// Execute is implemented in executor.go (Story 3.1b-ii). The interface-level
// stub here is unused; the engine-neutral signature can't carry KV handles,
// so the real entry point is ExecuteWith. Execute(ctx, cr, ec) returns a
// typed error directing callers to use ExecuteWith.
