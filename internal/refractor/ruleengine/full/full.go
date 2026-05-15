// Package full is the v2 openCypher rule engine — the visitor + executor
// pair that consumes the vendored ANTLR parser at
// internal/refractor/ruleengine/full/cypher.
//
// Story 3.1b will replace this stub with the real visitor + executor.
// In 3.1a Parse() always returns a ParseError so selection-logic can verify
// the boundary behaves correctly when a Lens explicitly opts into "full"
// or when absent-fallback exhausts the simple engine.
//
// The cypher subpackage is intentionally NOT imported here in 3.1a — there
// is nothing for the stub to do with the parser yet. 3.1b will add that
// import alongside the visitor implementation.
package full

import (
	"context"
	"fmt"

	"github.com/asolgan/lattice/internal/refractor/ruleengine"
)

// stubMessage is the error text emitted by Parse(). Tests assert on this
// substring; keep it stable until 3.1b replaces the stub.
const stubMessage = "full engine not yet implemented (Story 3.1b)"

// Engine is the stub v2 engine. It satisfies ruleengine.RuleEngine so the
// registry + selection-logic compile and behave correctly today, but every
// Parse() call fails by design.
type Engine struct{}

// New returns a ready-to-register stub full engine.
func New() *Engine { return &Engine{} }

// Name implements ruleengine.RuleEngine.
func (*Engine) Name() string { return ruleengine.EngineFull }

// Parse implements ruleengine.RuleEngine. ALWAYS returns a ParseError in
// 3.1a — this is the contracted stub behaviour.
func (*Engine) Parse(_ string) (ruleengine.CompiledRule, error) {
	return nil, &ruleengine.ParseError{
		Engine:  ruleengine.EngineFull,
		Message: stubMessage,
	}
}

// Execute is defensively reachable only if a caller bypasses Parse(). In
// that case we panic — Parse() is the gate, and any caller skipping it has
// a serious bug we want surfaced immediately.
func (*Engine) Execute(_ context.Context, _ ruleengine.CompiledRule, _ ruleengine.EventContext) (ruleengine.ProjectionResult, error) {
	panic(fmt.Sprintf("full.Engine.Execute called but %s — Parse() should have failed", stubMessage))
}
