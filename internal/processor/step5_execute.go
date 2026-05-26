package processor

import (
	"context"
	"log/slog"
)

// ExecutorImpl is the step-5 implementation (Starlark Execute). It runs the
// operation's class script (hydrated at step 4) against the ScriptContext and
// returns the proposed ScriptResult. DDL validation of the result is step 6.
type ExecutorImpl struct {
	Runner *StarlarkRunner
	Logger *slog.Logger
}

// NewExecutor constructs an Executor with the given runner. Pass nil to
// use a default-budget runner.
func NewExecutor(runner *StarlarkRunner, logger *slog.Logger) *ExecutorImpl {
	if runner == nil {
		runner = NewStarlarkRunner(0, 0)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ExecutorImpl{Runner: runner, Logger: logger}
}

// Execute implements Executor.
func (e *ExecutorImpl) Execute(ctx context.Context, env *OperationEnvelope, state HydratedState) (ScriptResult, error) {
	rid := env.RequestID
	sc := state.Context
	if sc.Operation == nil {
		// Defensive: someone constructed HydratedState without going
		// through HydratorImpl.
		sc.Operation = env
	}
	if sc.ScriptSource == "" {
		return ScriptResult{}, &ScriptError{
			Code:               "ScriptError",
			Message:            "no script source in hydrated state — step 4 may have been skipped",
			OperationRequestID: rid,
		}
	}

	result, err := e.Runner.Run(ctx, sc)
	if err != nil {
		// Already typed as *ScriptError.
		return ScriptResult{}, err
	}

	e.Logger.Info("step 5: executed",
		"requestId", rid,
		"class", sc.ScriptClass,
		"mutations", len(result.Mutations),
		"events", len(result.Events),
	)

	return result, nil
}
