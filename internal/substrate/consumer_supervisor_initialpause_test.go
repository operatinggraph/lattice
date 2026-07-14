package substrate

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestSupervisor_InitialPause_GatesFirstDrainUntilProbePasses proves the
// fail-closed activation gate: a spec with InitialPause: PauseInfra enters the
// probe loop BEFORE its first drain, so a published message is NOT handled until
// Probe passes — the precondition gate the protected-lens verify-and-pause relies
// on. Once the Probe recovers, the message drains.
func TestSupervisor_InitialPause_GatesFirstDrainUntilProbePasses(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &fakeSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var handled, leakedBeforeReady, probes int32
	var ready atomic.Bool
	notReadyErr := errors.New("precondition not yet met")

	spec := ConsumerSpec{
		Name:          "sup-initialpause",
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Health:        sink,
		InitialPause:  PauseInfra,
		ProbeInterval: 40 * time.Millisecond,
		AckWait:       1 * time.Second,
		Classify: func(err error) FailureClass {
			if errors.Is(err, notReadyErr) {
				return ClassInfra
			}
			return ClassTransient
		},
		// Fails until the third probe, then the precondition is "met".
		Probe: func(_ context.Context) error {
			if atomic.AddInt32(&probes, 1) >= 3 {
				ready.Store(true)
				return nil
			}
			return notReadyErr
		},
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			// A handle before the probe passes is a fail-OPEN — the gate leaked.
			if !ready.Load() {
				atomic.AddInt32(&leakedBeforeReady, 1)
			}
			atomic.AddInt32(&handled, 1)
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// The pump persists the seeded infra pause at activation, before any drain.
	waitFor(t, 3*time.Second, func() bool {
		st, rs := sink.snapshot()
		return st == StatusPaused && rs == PauseInfra
	}, "InitialPause not seeded/persisted at activation")

	// Publish AFTER the gate is confirmed paused: the message must wait for the
	// probe to pass, not drain into the still-unverified pump.
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.gate", []byte(`{"v":1}`))

	// Once the probe recovers, the message drains — exactly once, and only after
	// the precondition was met.
	waitFor(t, 3*time.Second, func() bool {
		st, _ := sink.snapshot()
		return st == StatusActive && atomic.LoadInt32(&handled) > 0
	}, "message did not drain after the probe passed")

	if got := atomic.LoadInt32(&leakedBeforeReady); got != 0 {
		t.Fatalf("fail-OPEN: %d message(s) handled before the probe passed (the gate leaked)", got)
	}
}

// TestSupervisor_NoInitialPause_DrainsImmediately is the regression guard: every
// existing consumer leaves InitialPause at its zero value and must drain on the
// first iteration without entering the probe loop — even if a Probe is set, it is
// never consulted because there is no infra pause to recover from.
func TestSupervisor_NoInitialPause_DrainsImmediately(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var handled, probes int32
	spec := ConsumerSpec{
		Name:          "sup-noinitialpause",
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		ProbeInterval: 40 * time.Millisecond,
		// A Probe that would fail if the pump ever entered the probe loop — it must
		// not, because InitialPause is the zero value (drain immediately).
		Probe: func(_ context.Context) error {
			atomic.AddInt32(&probes, 1)
			return errors.New("probe must never be called for a zero-InitialPause spec")
		},
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			atomic.AddInt32(&handled, 1)
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.imm", []byte(`{"v":1}`))

	waitFor(t, 3*time.Second, func() bool {
		return atomic.LoadInt32(&handled) > 0
	}, "zero-InitialPause spec did not drain immediately")

	if got := atomic.LoadInt32(&probes); got != 0 {
		t.Fatalf("zero-InitialPause spec entered the probe loop (%d probes) — it must drain without probing", got)
	}
}
