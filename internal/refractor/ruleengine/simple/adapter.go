package simple

import (
	"context"
	"fmt"

	"github.com/asolgan/lattice/internal/refractor/ruleengine"
)

// Engine adapts the simple v1 parser/compiler to the engine-neutral
// ruleengine.RuleEngine interface. It is registered with ruleengine.NewRegistry
// at process startup and consulted by selection logic in lens validation.
// Only Parse() is exercised via SelectForLens; the production execution path
// calls simple.Evaluate directly with a fully-compiled QueryPlan.
type Engine struct{}

// New returns a ready-to-register simple engine.
func New() *Engine { return &Engine{} }

// Name implements ruleengine.RuleEngine.
func (*Engine) Name() string { return ruleengine.EngineSimple }

// CompiledRule wraps the simple engine's *Query AST so it satisfies the
// engine-neutral CompiledRule marker interface.
type CompiledRule struct {
	Query *Query
}

func (c *CompiledRule) EngineName() string { return ruleengine.EngineSimple }

// Parse implements ruleengine.RuleEngine.
func (*Engine) Parse(ruleBody string) (ruleengine.CompiledRule, error) {
	q, err := Parse(ruleBody)
	if err != nil {
		return nil, &ruleengine.ParseError{
			Engine:  ruleengine.EngineSimple,
			Message: err.Error(),
		}
	}
	return &CompiledRule{Query: q}, nil
}

// Execute satisfies ruleengine.RuleEngine. Production callers use simple.Evaluate
// directly with a fully-compiled QueryPlan; this method is not on the hot path.
func (*Engine) Execute(_ context.Context, cr ruleengine.CompiledRule, _ ruleengine.EventContext) (ruleengine.ProjectionResult, error) {
	if _, ok := cr.(*CompiledRule); !ok {
		return ruleengine.ProjectionResult{}, fmt.Errorf("simple.Engine.Execute: CompiledRule has wrong type %T", cr)
	}
	return ruleengine.ProjectionResult{}, fmt.Errorf("simple.Engine.Execute: production callers use simple.Evaluate directly; this method is not on the hot path")
}
