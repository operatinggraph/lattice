package simple

import (
	"context"
	"fmt"

	"github.com/asolgan/lattice/internal/refractor/ruleengine"
)

// Engine adapts the simple v1 parser/compiler to the engine-neutral
// ruleengine.RuleEngine interface. It is registered with ruleengine.NewRegistry
// at process startup and consulted by selection logic in lens validation.
//
// In Story 3.1a only Parse() is exercised by callers via SelectForLens —
// Execute() is wired but the production execution path still calls the
// concrete simple.Evaluate function with simple.QueryPlan directly. 3.1b
// will reconcile the execution path to flow through this interface.
type Engine struct{}

// New returns a ready-to-register simple engine.
func New() *Engine { return &Engine{} }

// Name implements ruleengine.RuleEngine.
func (*Engine) Name() string { return ruleengine.EngineSimple }

// compiledRule wraps the simple engine's *Query AST so it satisfies the
// engine-neutral CompiledRule marker interface.
type compiledRule struct {
	Query *Query
}

func (c *compiledRule) EngineName() string { return ruleengine.EngineSimple }

// Parse implements ruleengine.RuleEngine.
func (*Engine) Parse(ruleBody string) (ruleengine.CompiledRule, error) {
	q, err := Parse(ruleBody)
	if err != nil {
		return nil, &ruleengine.ParseError{
			Engine:  ruleengine.EngineSimple,
			Message: err.Error(),
		}
	}
	return &compiledRule{Query: q}, nil
}

// Execute is wired to satisfy the interface but is not on the hot path in
// 3.1a. Callers continue to invoke simple.Evaluate with a fully-compiled
// QueryPlan. 3.1b will route execution through this method.
func (*Engine) Execute(_ context.Context, cr ruleengine.CompiledRule, _ ruleengine.EventContext) (ruleengine.ProjectionResult, error) {
	if _, ok := cr.(*compiledRule); !ok {
		return ruleengine.ProjectionResult{}, fmt.Errorf("simple.Engine.Execute: compiledRule has wrong type %T", cr)
	}
	// 3.1b will replace this with a proper QueryPlan-driven Evaluate call.
	return ruleengine.ProjectionResult{}, fmt.Errorf("simple.Engine.Execute: not wired in story 3.1a; callers still invoke simple.Evaluate directly")
}
