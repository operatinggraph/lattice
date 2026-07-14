package substrate

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// fakeSink is an in-memory HealthSink shared across supervisor instances so a
// restart test can observe persisted state.
type fakeSink struct {
	mu      sync.Mutex
	status  HealthStatus
	reason  PauseReason
	loadErr error
}

func (f *fakeSink) SetActive(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = StatusActive
	f.reason = ""
	return nil
}

func (f *fakeSink) SetPaused(_ context.Context, reason PauseReason, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = StatusPaused
	f.reason = reason
	return nil
}

func (f *fakeSink) Load(_ context.Context) (HealthStatus, PauseReason, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return StatusActive, "", f.loadErr
	}
	return f.status, f.reason, nil
}

func (f *fakeSink) snapshot() (HealthStatus, PauseReason) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.reason
}

// consumerInfoByName reads consumer config via a test-side jetstream handle.
func consumerInfoByName(ctx context.Context, t *testing.T, c *Conn, stream, durable string) *jetstream.ConsumerInfo {
	t.Helper()
	cons, err := c.js.Consumer(ctx, stream, durable)
	if err != nil {
		t.Fatalf("consumer %q: %v", durable, err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatalf("consumer info %q: %v", durable, err)
	}
	return info
}

// TestSupervisor_NoMaxDeliver_UnboundedRedelivery proves a supervisor-created
// durable has no MaxDeliver bound and a repeatedly-Nak'd message keeps
// redelivering past any small bound (AC6b).
func TestSupervisor_NoMaxDeliver_UnboundedRedelivery(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var deliveries int32
	enough := make(chan struct{})
	spec := ConsumerSpec{
		Name:          "sup-nomax",
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			if atomic.AddInt32(&deliveries, 1) == 6 {
				close(enough)
			}
			return Nak, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.spin", []byte(`{"v":1}`))

	select {
	case <-enough:
	case <-time.After(8 * time.Second):
		t.Fatalf("message did not redeliver past a small bound; deliveries=%d", atomic.LoadInt32(&deliveries))
	}

	info := consumerInfoByName(ctx, t, c, "KV_"+bucket, "sup-nomax")
	if info.Config.MaxDeliver != 0 && info.Config.MaxDeliver != -1 {
		t.Fatalf("supervisor durable has MaxDeliver=%d, want unbounded (0 or -1)", info.Config.MaxDeliver)
	}
}

// TestSupervisor_ManualPause_SurvivesRestart proves a manual pause persists
// through the sink and a new supervisor instance restores into PausedManual and
// does not pump until Resume (AC6c).
func TestSupervisor_ManualPause_SurvivesRestart(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &fakeSink{}

	// First supervisor: pause manually.
	sup1 := NewConsumerSupervisor(c)
	spec := ConsumerSpec{
		Name:          "sup-manual",
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Health:        sink,
		Handler:       func(_ context.Context, _ Message) (Decision, error) { return Ack, nil },
	}
	if err := sup1.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	sup1.Pause(ctx, "sup-manual")

	deadline := time.After(2 * time.Second)
	for {
		st, rs := sink.snapshot()
		if st == StatusPaused && rs == PauseManual {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("sink did not persist manual pause: status=%v reason=%v", st, rs)
		case <-time.After(20 * time.Millisecond):
		}
	}
	sup1.Stop()

	// Second supervisor with the SAME sink: must restore into manual pause and
	// not process messages until Resume.
	sup2 := NewConsumerSupervisor(c)
	t.Cleanup(sup2.Stop)
	var processed int32
	spec.Handler = func(_ context.Context, _ Message) (Decision, error) {
		atomic.AddInt32(&processed, 1)
		return Ack, nil
	}
	if err := sup2.Add(ctx, spec); err != nil {
		t.Fatalf("Add (restart): %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.held-while-paused", []byte(`{"v":1}`))
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.restored", []byte(`{"v":1}`))

	time.Sleep(400 * time.Millisecond)
	if atomic.LoadInt32(&processed) != 0 {
		t.Fatalf("restored manual pause must not pump; processed=%d", atomic.LoadInt32(&processed))
	}

	sup2.Resume(ctx, "sup-manual")
	deadline = time.After(3 * time.Second)
	for atomic.LoadInt32(&processed) == 0 {
		select {
		case <-deadline:
			t.Fatalf("pump did not resume after Resume")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if st, _ := sink.snapshot(); st != StatusActive {
		t.Fatalf("sink must be active after resume, got %v", st)
	}
}

// TestSupervisor_InfraPause_ProbeRecovers proves an injected Infra classification
// enters the probe loop, recovers on a passing probe, and the sink shows active.
// Composability: while PausedManual, a probe success does NOT resume the pump (AC6d).
func TestSupervisor_InfraPause_ProbeRecovers(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &fakeSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var upserts, probes int32
	infraErr := errors.New("infra down")
	var recovered atomic.Bool
	var processedAfter int32

	spec := ConsumerSpec{
		Name:          "sup-infra",
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Health:        sink,
		ProbeInterval: 40 * time.Millisecond,
		AckWait:       1 * time.Second,
		Classify: func(err error) FailureClass {
			if errors.Is(err, infraErr) {
				return ClassInfra
			}
			return ClassTransient
		},
		Probe: func(_ context.Context) error {
			if atomic.AddInt32(&probes, 1) >= 2 {
				recovered.Store(true)
				return nil
			}
			return infraErr
		},
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			if !recovered.Load() {
				atomic.AddInt32(&upserts, 1)
				return Nak, infraErr
			}
			atomic.AddInt32(&processedAfter, 1)
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.inf", []byte(`{"v":1}`))

	// Pump entered infra pause (handler called, sink paused/infra).
	waitFor(t, 3*time.Second, func() bool {
		st, rs := sink.snapshot()
		return st == StatusPaused && rs == PauseInfra
	}, "infra pause not persisted")

	// Probe recovers → sink active, message reprocessed.
	waitFor(t, 3*time.Second, func() bool {
		st, _ := sink.snapshot()
		return st == StatusActive && atomic.LoadInt32(&processedAfter) > 0
	}, "did not recover + reprocess after probe")
}

// TestSupervisor_ManualPause_BlocksProbeResume proves composability: a manual
// pause holds even when infra would clear — operator pause is not cleared by a
// passing probe.
func TestSupervisor_ManualPause_BlocksProbeResume(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &fakeSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var processed int32
	spec := ConsumerSpec{
		Name:          "sup-compose",
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Health:        sink,
		ProbeInterval: 30 * time.Millisecond,
		Probe:         func(_ context.Context) error { return nil }, // always healthy
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			atomic.AddInt32(&processed, 1)
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Manually pause; even though the probe would pass, the manual reason holds.
	sup.Pause(ctx, "sup-compose")
	waitFor(t, 2*time.Second, func() bool {
		st, rs := sink.snapshot()
		return st == StatusPaused && rs == PauseManual
	}, "manual pause not persisted")

	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.held", []byte(`{"v":1}`))
	time.Sleep(300 * time.Millisecond)
	if atomic.LoadInt32(&processed) != 0 {
		t.Fatalf("manual pause must hold despite healthy probe; processed=%d", atomic.LoadInt32(&processed))
	}
}

// TestSupervisor_Reset_RecreatesWithNewFilter proves Add with filter A, then a
// spec filter change to B + Reset, recreates the durable with filter B (AC6e).
func TestSupervisor_Reset_RecreatesWithNewFilter(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	filterA := "$KV." + bucket + ".vtx.agreement.>"
	filterB := "$KV." + bucket + ".vtx.identity.>"
	spec := ConsumerSpec{
		Name:          "sup-reset",
		Stream:        "KV_" + bucket,
		FilterSubject: filterA,
		DeliverGroup:  "sup-reset",
		Handler:       func(_ context.Context, _ Message) (Decision, error) { return Ack, nil },
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	info := consumerInfoByName(ctx, t, c, "KV_"+bucket, "sup-reset")
	if info.Config.FilterSubject != filterA {
		t.Fatalf("initial filter = %q, want %q", info.Config.FilterSubject, filterA)
	}
	if info.Config.DeliverGroup != "sup-reset" {
		t.Fatalf("initial DeliverGroup = %q, want %q", info.Config.DeliverGroup, "sup-reset")
	}

	if err := sup.UpdateSpec("sup-reset", func(s *ConsumerSpec) { s.FilterSubject = filterB }); err != nil {
		t.Fatalf("UpdateSpec: %v", err)
	}
	if err := sup.Reset(ctx, "sup-reset"); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	info = consumerInfoByName(ctx, t, c, "KV_"+bucket, "sup-reset")
	if info.Config.FilterSubject != filterB {
		t.Fatalf("after Reset filter = %q, want %q", info.Config.FilterSubject, filterB)
	}
	if info.Config.DeliverGroup != "sup-reset" {
		t.Fatalf("after Reset DeliverGroup = %q, want %q (queue group must survive Reset)", info.Config.DeliverGroup, "sup-reset")
	}
}

// TestSupervisor_Remove_DeletesDurable_StopPreserves verifies Remove deletes the
// server-side durable while Stop preserves it (Winston Q3 ruling).
func TestSupervisor_Remove_DeletesDurable_StopPreserves(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	// Stop preserves.
	sup := NewConsumerSupervisor(c)
	spec := ConsumerSpec{
		Name:    "sup-stop-keep",
		Stream:  "KV_" + bucket,
		Handler: func(_ context.Context, _ Message) (Decision, error) { return Ack, nil },
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	sup.Stop()
	if _, err := c.js.Consumer(ctx, "KV_"+bucket, "sup-stop-keep"); err != nil {
		t.Fatalf("Stop must preserve durable, but it is gone: %v", err)
	}

	// Remove deletes.
	sup2 := NewConsumerSupervisor(c)
	t.Cleanup(sup2.Stop)
	spec.Name = "sup-remove-del"
	if err := sup2.Add(ctx, spec); err != nil {
		t.Fatalf("Add2: %v", err)
	}
	if err := sup2.Remove(ctx, "sup-remove-del"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := c.js.Consumer(ctx, "KV_"+bucket, "sup-remove-del"); !errors.Is(err, jetstream.ErrConsumerNotFound) {
		t.Fatalf("Remove must delete durable, got err=%v", err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal(msg)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
