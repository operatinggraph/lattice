// Package testutil provides shared test scaffolding. Story 1.8 introduces
// the `FailAfterN` fault-injection harness used by the Processor NFR-R1
// crash-recoverability test suite (internal/processor/nfr_r1_test.go).
//
// The harness is a family of wrappers — one per Processor step interface
// — each constructed via a `FailAfterN`-style helper. Each wrapper
// counts step-boundary calls and returns a fault-injected error after
// the Nth call. The "crash" is modeled as an error return rather than a
// panic so the commit_path's existing error-return discipline triggers
// JetStream redelivery cleanly (Architecture Decision #5).
package testutil

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/operatinggraph/lattice/internal/processor"
)

// ErrFaultInjected is the typed sentinel every fault-injection wrapper
// returns. Tests use errors.Is for the assertion that a fault fired.
var ErrFaultInjected = errors.New("fault injected")

// FaultLabel describes which step is being injected — used only in the
// error message for diagnostics.
type FaultLabel string

const (
	FaultStep1Consume  FaultLabel = "step1-consume"
	FaultStep2Dedup    FaultLabel = "step2-dedup"
	FaultStep3Auth     FaultLabel = "step3-auth"
	FaultStep4Hydrate  FaultLabel = "step4-hydrate"
	FaultStep5Execute  FaultLabel = "step5-execute"
	FaultStep6Validate FaultLabel = "step6-validate"
	FaultStep7Events   FaultLabel = "step7-events"
	FaultStep8Commit   FaultLabel = "step8-commit"
	FaultStep9Ack      FaultLabel = "step9-ack"
)

// FaultError is the error returned by a triggered fault. Wraps
// ErrFaultInjected so errors.Is works.
type FaultError struct {
	Label FaultLabel
	Call  int
}

func (e *FaultError) Error() string {
	return "fault injected at " + string(e.Label) + " call " + itoa(e.Call)
}

func (e *FaultError) Unwrap() error { return ErrFaultInjected }

// itoa is a tiny strconv.Itoa replacement to avoid the import here.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// counter is a small shared atomic call counter used by every wrapper.
type counter struct {
	calls  atomic.Int64
	failAt int
	label  FaultLabel
}

// trip increments the counter and, if the call number equals failAt,
// returns a typed FaultError. Otherwise returns nil.
func (c *counter) trip() error {
	n := c.calls.Add(1)
	if int(n) == c.failAt {
		return &FaultError{Label: c.label, Call: int(n)}
	}
	return nil
}

// FailAfterN returns a function that fires a fault on its Nth call.
// The generic shape `func() error` makes it composable with any
// step-interface wrapper below. Tests typically use the per-interface
// constructors (FailHydratorAfterN, FailCommitterAfterN, etc.) which
// build on this primitive.
//
// AC: "the fault injection harness is implemented as
// `internal/testutil/faultinjector.go` with a `FailAfterN(n int)`
// constructor." This is the constructor; the per-interface wrappers
// instantiate it for their respective steps.
func FailAfterN(n int, label FaultLabel) func() error {
	c := &counter{failAt: n, label: label}
	return c.trip
}

// --- Per-interface wrappers ---
//
// Each wrapper is a thin pass-through that consults a shared counter
// before delegating to an inner real implementation. The counter is
// shared so tests can build a single "trip on call N" predicate and
// apply it at the step under test.

// FaultyHydrator wraps an inner Hydrator with FailAfterN semantics.
type FaultyHydrator struct {
	Inner processor.Hydrator
	Trip  func() error
}

func (f *FaultyHydrator) Hydrate(ctx context.Context, env *processor.OperationEnvelope) (processor.HydratedState, error) {
	if err := f.Trip(); err != nil {
		return processor.HydratedState{}, err
	}
	return f.Inner.Hydrate(ctx, env)
}

// FailHydratorAfterN returns a Hydrator that fails on its Nth call.
func FailHydratorAfterN(inner processor.Hydrator, n int) *FaultyHydrator {
	return &FaultyHydrator{Inner: inner, Trip: FailAfterN(n, FaultStep4Hydrate)}
}

// FaultyExecutor wraps an inner Executor with FailAfterN semantics.
type FaultyExecutor struct {
	Inner processor.Executor
	Trip  func() error
}

func (f *FaultyExecutor) Execute(ctx context.Context, env *processor.OperationEnvelope, state processor.HydratedState) (processor.ScriptResult, error) {
	if err := f.Trip(); err != nil {
		return processor.ScriptResult{}, err
	}
	return f.Inner.Execute(ctx, env, state)
}

// FailExecutorAfterN returns an Executor that fails on its Nth call.
func FailExecutorAfterN(inner processor.Executor, n int) *FaultyExecutor {
	return &FaultyExecutor{Inner: inner, Trip: FailAfterN(n, FaultStep5Execute)}
}

// FaultyValidator wraps an inner Validator with FailAfterN semantics.
type FaultyValidator struct {
	Inner processor.Validator
	Trip  func() error
}

func (f *FaultyValidator) Validate(ctx context.Context, env *processor.OperationEnvelope, result processor.ScriptResult, state processor.HydratedState) error {
	if err := f.Trip(); err != nil {
		return err
	}
	return f.Inner.Validate(ctx, env, result, state)
}

// FailValidatorAfterN returns a Validator that fails on its Nth call.
func FailValidatorAfterN(inner processor.Validator, n int) *FaultyValidator {
	return &FaultyValidator{Inner: inner, Trip: FailAfterN(n, FaultStep6Validate)}
}

// FaultyCommitter wraps an inner Committer with FailAfterN semantics.
type FaultyCommitter struct {
	Inner processor.Committer
	Trip  func() error
}

func (f *FaultyCommitter) Commit(ctx context.Context, env *processor.OperationEnvelope, result processor.ScriptResult, tracker processor.Tracker) (processor.CommitAck, error) {
	if err := f.Trip(); err != nil {
		return processor.CommitAck{}, err
	}
	return f.Inner.Commit(ctx, env, result, tracker)
}

// FailCommitterAfterN returns a Committer that fails on its Nth call.
func FailCommitterAfterN(inner processor.Committer, n int) *FaultyCommitter {
	return &FaultyCommitter{Inner: inner, Trip: FailAfterN(n, FaultStep8Commit)}
}

// FaultyAuthorizer wraps an inner Authorizer.
type FaultyAuthorizer struct {
	Inner processor.Authorizer
	Trip  func() error
}

func (f *FaultyAuthorizer) Authorize(ctx context.Context, env *processor.OperationEnvelope) (processor.Decision, error) {
	if err := f.Trip(); err != nil {
		return processor.Decision{}, err
	}
	return f.Inner.Authorize(ctx, env)
}

// FailAuthorizerAfterN returns an Authorizer that fails on its Nth call.
func FailAuthorizerAfterN(inner processor.Authorizer, n int) *FaultyAuthorizer {
	return &FaultyAuthorizer{Inner: inner, Trip: FailAfterN(n, FaultStep3Auth)}
}

// FaultyAcker wraps an inner Acker (Story 1.8 step 9).
type FaultyAcker struct {
	Inner processor.Acker
	Trip  func() error
}

func (f *FaultyAcker) Ack(ctx context.Context) error {
	if err := f.Trip(); err != nil {
		return err
	}
	return f.Inner.Ack(ctx)
}

// FailAckerAfterN returns an Acker that fails on its Nth call.
func FailAckerAfterN(inner processor.Acker, n int) *FaultyAcker {
	return &FaultyAcker{Inner: inner, Trip: FailAfterN(n, FaultStep9Ack)}
}
