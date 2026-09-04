// Package full is the v2 openCypher rule engine. It provides a real
// lex/parse/walk pipeline (visitor + AST) and an executor that evaluates
// the AST against Core KV and Adjacency KV.
package full

import (
	"fmt"
	"strings"
	"sync/atomic"

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

	// prefetchDisabled is a per-engine override that forces this engine's
	// evaluations down the point-read path for every node, aspect and
	// adjacency read — one key per round trip, no batched prefetch. It is set
	// only by the test-only prefetchOff helper (the comparison tests that pin
	// the two paths to the same rows and the same read-surface footprint);
	// every registered engine leaves it false (the zero value) and instead
	// takes the package-wide posture — prefetchModeDisabled resolves that to
	// batching unless an operator has turned it off (DefaultPrefetchMode,
	// REFRACTOR_ENGINE_PREFETCH).
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

// PrefetchMode is the posture deciding whether prefetchAspects, prefetchEdges
// and prefetchNodes batch the node, aspect and adjacency reads an evaluation
// is about to make into few round trips, or leave every one of them to its
// own point read.
type PrefetchMode int

const (
	// PrefetchModeUnset means "take the package default", and is the zero
	// value deliberately: an engine carries the mode as a plain field whose
	// unset state is zero, so zero must mean unset rather than a real mode.
	PrefetchModeUnset PrefetchMode = iota
	PrefetchModeOff
	PrefetchModeOn
)

func (m PrefetchMode) String() string {
	switch m {
	case PrefetchModeOff:
		return "off"
	case PrefetchModeOn:
		return "on"
	default:
		return "unset"
	}
}

// ParsePrefetchMode maps an operator-supplied string onto a mode, rejecting
// rather than guessing — a typo resolving silently to `off` would put every
// evaluation back on the one-key-at-a-time path with nothing saying so.
func ParsePrefetchMode(s string) (PrefetchMode, error) {
	switch s {
	case "on":
		return PrefetchModeOn, nil
	case "off":
		return PrefetchModeOff, nil
	default:
		return PrefetchModeUnset, fmt.Errorf("full engine: unknown prefetch mode %q (want on or off)", s)
	}
}

// defaultPrefetchMode is the process-wide posture every engine without its own
// override uses. Package-level for the same reason defaultHubReadScopeMode is:
// the operator decision is one per process (cmd/refractor reads
// REFRACTOR_ENGINE_PREFETCH once) while engines are constructed wherever a
// pipeline is built, and threading a startup flag through every construction
// site makes it possible to miss one.
//
// LIFETIME: written once at boot, by cmd/refractor's env read. Tests take the
// per-engine prefetchOff copy instead and leave this alone, so no test can
// change the posture another test is running under. Read per evaluation, at
// executor construction, for the same reason defaultHubReadScopeMode is: it is
// an operator posture, not evaluation state, so it is deliberately NOT reset or
// re-derived at rebuild, replay, reconnect, tombstone or rule hot-reload. It
// does not survive the process, which is correct: the env var is re-read at
// the next boot.
var defaultPrefetchMode atomic.Int64

// SetDefaultPrefetchMode sets the posture every engine without its own
// override uses. PrefetchModeUnset restores the built-in.
func SetDefaultPrefetchMode(m PrefetchMode) { defaultPrefetchMode.Store(int64(m)) }

// DefaultPrefetchMode reports that posture resolved to a real mode rather than
// to Unset, so a host can state at boot which behaviour it runs.
func DefaultPrefetchMode() PrefetchMode {
	if m := PrefetchMode(defaultPrefetchMode.Load()); m != PrefetchModeUnset {
		return m
	}
	return PrefetchModeOn
}

// prefetchModeDisabled resolves this engine's prefetch-batching posture to the
// bool prefetchAspects/prefetchEdges/prefetchNodes gate on: this engine's own
// prefetchDisabled override when it is set, the package default otherwise.
func (e *Engine) prefetchModeDisabled() bool {
	if e.prefetchDisabled {
		return true
	}
	return DefaultPrefetchMode() == PrefetchModeOff
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
