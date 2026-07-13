package starlarksandbox

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	starlarklib "go.starlark.net/starlark"
)

// loadColonRe matches go.starlark.net's "load: <reason>" resolve-time error
// family (e.g. "load: empty identifier", "load: name %s not found in
// module %s") without false-positiving on an unrelated message that merely
// contains "load:" as a substring of a longer word — a script's own
// fail("invalid payload: ...") contains "load:" inside "payload:" and must
// NOT classify as SandboxViolation.
var loadColonRe = regexp.MustCompile(`\bload:`)

// Budget bounds a single Execute call.
type Budget struct {
	// Wall is the wall-clock execution budget covering Init+Call (compile
	// is not charged against it — it happens before Wall's clock starts).
	// If Wall > 0, Execute derives its own ctx via context.WithTimeout; if
	// Wall <= 0, the caller's ctx is used as-is with no added deadline.
	Wall time.Duration
	// MaxSteps is the secondary safeguard against infinite loops
	// (starlark.Thread.SetMaxExecutionSteps). The wall clock is the
	// primary fence; MaxSteps <= 0 leaves the step count unbounded.
	MaxSteps int64
}

// ErrorCode classifies why Execute failed.
type ErrorCode string

const (
	// SandboxViolation: the script referenced an unbound name (a
	// forbidden module like os/http) or called load(...). Detected at
	// compile (resolve) time via the predeclared-name probe, or at
	// runtime via the always-nil Load hook.
	SandboxViolation ErrorCode = "SandboxViolation"
	// ScriptError: a syntax error, an uncaught runtime error (fail(),
	// division by zero, ...), or a MaxSteps exhaustion. The catch-all.
	ScriptError ErrorCode = "ScriptError"
	// ScriptTimeout: the wall budget elapsed during Call.
	ScriptTimeout ErrorCode = "ScriptTimeout"
	// InvalidReturnShape: entrypoint is not among the names the script's
	// top-level statements defined.
	InvalidReturnShape ErrorCode = "InvalidReturnShape"
)

// SandboxError is Execute's failure type.
type SandboxError struct {
	Code    ErrorCode
	Message string
	Line    int
	Column  int
}

func (e *SandboxError) Error() string { return e.Message }

// ctxLocalKey is the starlark.Thread local key (Thread.Local/SetLocal take
// a string key) Execute stores its execution-scoped context under,
// immediately before Init. A caller-supplied impure builtin (e.g. the
// Processor's kv.Read) reads it via ContextFromThread so a slow round-trip
// counts against the same wall budget the script itself is bound by.
const ctxLocalKey = "starlarksandbox.ctx"

// ContextFromThread returns the execution-scoped context Execute bound to
// thread. It returns fallback if thread carries no such local — the
// defensive path for a thread Execute did not create.
func ContextFromThread(thread *starlarklib.Thread, fallback context.Context) context.Context {
	if v, ok := thread.Local(ctxLocalKey).(context.Context); ok {
		return v
	}
	return fallback
}

// Execute compiles source with globals as both the predeclared-name set
// (resolve time) and the initial global bindings (Init time), looks up
// entrypoint among the names Init defines, and calls it with args.
// `load` is always disabled: go.starlark.net's SourceProgram resolves
// every name against globals.Has before a single statement runs, so a
// reference to a name absent from globals is a compile-time
// SandboxViolation, and the Thread built here carries no Load hook, so a
// `load(...)` statement always fails at runtime.
//
// Zero internal deps: Execute takes no Lattice-specific globals itself —
// the caller supplies everything a script can see, pure or impure alike,
// via globals.
func Execute(ctx context.Context, source, entrypoint string, args starlarklib.Tuple, globals starlarklib.StringDict, budget Budget) (starlarklib.Value, *SandboxError) {
	//nolint:staticcheck // SA1019: SourceProgramOptions migration deferred; current API verified safe for the sandboxed use case
	_, prog, err := starlarklib.SourceProgram("<script>", source, globals.Has)
	if err != nil {
		return nil, classify(err)
	}

	execCtx := ctx
	if budget.Wall > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, budget.Wall)
		defer cancel()
	}

	thread := &starlarklib.Thread{Name: "starlarksandbox"}
	thread.SetLocal(ctxLocalKey, execCtx)
	if budget.MaxSteps > 0 {
		thread.SetMaxExecutionSteps(uint64(budget.MaxSteps))
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-execCtx.Done():
			thread.Cancel(execCtx.Err().Error())
		case <-done:
		}
	}()

	defined, err := prog.Init(thread, globals)
	if err != nil {
		return nil, classify(err)
	}

	entryFn, ok := defined[entrypoint]
	if !ok {
		return nil, &SandboxError{
			Code:    InvalidReturnShape,
			Message: fmt.Sprintf("script must define a %q function", entrypoint),
		}
	}

	out, err := starlarklib.Call(thread, entryFn, args, nil)
	if err != nil {
		// Only the Call phase is checked against the wall budget here —
		// matching the original single-file runner's behavior, where a
		// hang during Init (top-level statements, not the entrypoint
		// body) was never observed in practice and is left classified
		// generically rather than special-cased.
		if execCtx.Err() != nil && errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return nil, &SandboxError{
				Code:    ScriptTimeout,
				Message: fmt.Sprintf("script exceeded wall budget %s", budget.Wall),
			}
		}
		return nil, classify(err)
	}
	return out, nil
}

// Validate compiles source with globals as the predeclared-name set, runs its
// top-level statements (Init — under the same `load`-disabled, predeclared-only,
// budget-bounded sandbox Execute uses for Init+Call), and checks that
// entrypoint is defined and callable with exactly nParams positional
// parameters. It does NOT call entrypoint — callers that need to validate a
// script before any input value exists to call it with (e.g. at package-data
// load time, ahead of any invocation) use this instead of Execute.
//
// budget bounds Init exactly as Execute bounds Init+Call: Init runs arbitrary
// top-level script statements (not just the entrypoint body), so an unbounded
// Validate would let a pathological top-level statement (an infinite loop
// outside the entrypoint function) hang the caller indefinitely — load-bearing
// for a caller like Loom's parseGuard, which re-validates a Starlark guard on
// EVERY step-transition attempt, not just once at pattern-install time.
// Validate has no caller-supplied context (there is no in-flight request at
// validate time), so it always derives its own bounded one from
// context.Background() when budget.Wall > 0 — unlike Execute, a Wall <= 0
// here leaves Init genuinely unbounded, so callers MUST pass a real budget.
//
// Returns nil on success, or a *SandboxError classifying the same failure
// families Execute's compile/Init phase would (SandboxViolation / ScriptError
// / ScriptTimeout), plus InvalidReturnShape for a missing/wrong-arity/
// non-callable entrypoint.
func Validate(source, entrypoint string, nParams int, globals starlarklib.StringDict, budget Budget) *SandboxError {
	//nolint:staticcheck // SA1019: SourceProgramOptions migration deferred; current API verified safe for the sandboxed use case
	_, prog, err := starlarklib.SourceProgram("<script>", source, globals.Has)
	if err != nil {
		return classify(err)
	}

	ctx := context.Background()
	if budget.Wall > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget.Wall)
		defer cancel()
	}

	thread := &starlarklib.Thread{Name: "starlarksandbox-validate"}
	thread.SetLocal(ctxLocalKey, ctx)
	if budget.MaxSteps > 0 {
		thread.SetMaxExecutionSteps(uint64(budget.MaxSteps))
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			thread.Cancel(ctx.Err().Error())
		case <-done:
		}
	}()

	defined, err := prog.Init(thread, globals)
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return &SandboxError{
				Code:    ScriptTimeout,
				Message: fmt.Sprintf("script exceeded wall budget %s during validate", budget.Wall),
			}
		}
		return classify(err)
	}

	entryVal, ok := defined[entrypoint]
	if !ok {
		return &SandboxError{
			Code:    InvalidReturnShape,
			Message: fmt.Sprintf("script must define a %q function", entrypoint),
		}
	}
	fn, ok := entryVal.(*starlarklib.Function)
	if !ok {
		return &SandboxError{
			Code:    InvalidReturnShape,
			Message: fmt.Sprintf("%q must be a function, got %s", entrypoint, entryVal.Type()),
		}
	}
	if fn.NumParams() != nParams {
		return &SandboxError{
			Code:    InvalidReturnShape,
			Message: fmt.Sprintf("%q must take exactly %d parameter(s), got %d", entrypoint, nParams, fn.NumParams()),
		}
	}
	return nil
}

// classify maps a go.starlark.net error onto ErrorCode + extracts a
// line/column when the error type exposes one.
func classify(err error) *SandboxError {
	msg := err.Error()

	// Resolve errors arrive as resolve.ErrorList, wrapped by SourceProgram.
	// The string contains "undefined:" for the classic sandbox-violation
	// case (an unbound name — the only way a script references os/http/etc.).
	if strings.Contains(msg, "undefined:") {
		line, col := extractPosition(err)
		return &SandboxError{Code: SandboxViolation, Message: msg, Line: line, Column: col}
	}
	// `load` calls fail with "load not implemented by this application" (no
	// Load hook — the sandbox's actual posture) or, in a hypothetical
	// configuration with a Load hook, a resolve-time "load: <reason>"
	// message (e.g. "load: empty identifier"). loadColonRe is word-bounded
	// so it cannot false-positive on an unrelated message that merely
	// contains "load:" as a substring of a longer word (e.g. a script's own
	// `fail("invalid payload: ...")` — "payload:" contains "load:" — must
	// classify as ScriptError, not SandboxViolation).
	if strings.Contains(msg, "load not implemented") || loadColonRe.MatchString(msg) {
		line, col := extractPosition(err)
		return &SandboxError{Code: SandboxViolation, Message: msg, Line: line, Column: col}
	}
	// Anything else — syntax error, runtime fail() call, step-limit
	// exhaustion, division by zero, etc.
	line, col := extractPosition(err)
	return &SandboxError{Code: ScriptError, Message: msg, Line: line, Column: col}
}

// extractPosition pulls line/column from a starlark EvalError or
// SyntaxError-shaped error. Returns (0,0) if the error type carries none.
func extractPosition(err error) (int, int) {
	type positioner interface{ Position() (string, int, int) }
	var p positioner
	if errors.As(err, &p) {
		_, line, col := p.Position()
		return line, col
	}
	var evalErr *starlarklib.EvalError
	if errors.As(err, &evalErr) && len(evalErr.CallStack) > 0 {
		pos := evalErr.CallStack[len(evalErr.CallStack)-1].Pos
		return int(pos.Line), int(pos.Col)
	}
	return 0, 0
}
