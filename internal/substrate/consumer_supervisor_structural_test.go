package substrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// structuralSink is a HealthSink that records the persisted CAUSE and the full
// transition sequence, not just the latest state: the relapse latch is only
// observable through the cause string it prefixes, and "the pump never flickered
// through active" is only observable through the sequence.
type structuralSink struct {
	mu           sync.Mutex
	status       HealthStatus
	reason       PauseReason
	lastErr      string
	transitions  []string
	pausedWrites int
}

func (s *structuralSink) SetActive(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.reason, s.lastErr = StatusActive, "", ""
	s.record("active")
	return nil
}

func (s *structuralSink) SetPaused(_ context.Context, reason PauseReason, lastErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.reason, s.lastErr = StatusPaused, reason, lastErr
	s.pausedWrites++
	s.record("paused:" + string(reason))
	return nil
}

func (s *structuralSink) Load(_ context.Context) (HealthStatus, PauseReason, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.reason, nil
}

// record appends a transition, collapsing consecutive writes of the same state
// (a paused pump re-persisting a refreshed cause is not a transition).
func (s *structuralSink) record(state string) {
	if n := len(s.transitions); n > 0 && s.transitions[n-1] == state {
		return
	}
	s.transitions = append(s.transitions, state)
}

func (s *structuralSink) snapshot() (HealthStatus, PauseReason, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.reason, s.lastErr
}

func (s *structuralSink) states() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.transitions...)
}

func (s *structuralSink) pausedWriteCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pausedWrites
}

// announcement is one RecordStructuralAutoRecovery call.
type announcement struct {
	cause    string
	attempts int
}

// announcingSink is a structuralSink that also satisfies the optional
// StructuralRecoveryAnnouncer. structuralSink deliberately does NOT — the tests
// running on the plain sink are the proof that a health implementation lacking
// the optional half is told nothing and behaves identically.
type announcingSink struct {
	structuralSink
	amu           sync.Mutex
	calls         []announcement
	announcements []announcement
	failuresLeft  int
}

// failAnnouncements makes the next n calls fail, standing in for a health store
// that is briefly unreachable. Set before the consumer is added.
func (s *announcingSink) failAnnouncements(n int) {
	s.amu.Lock()
	defer s.amu.Unlock()
	s.failuresLeft = n
}

func (s *announcingSink) RecordStructuralAutoRecovery(_ context.Context, cause string, attempts int) error {
	s.amu.Lock()
	s.calls = append(s.calls, announcement{cause: cause, attempts: attempts})
	if s.failuresLeft > 0 {
		s.failuresLeft--
		s.amu.Unlock()
		return errors.New("health store unreachable")
	}
	s.announcements = append(s.announcements, announcement{cause: cause, attempts: attempts})
	s.amu.Unlock()
	// Also land in the transition log, so a test can assert WHERE in the
	// lifecycle the announcement happened, not merely that it happened.
	s.mu.Lock()
	s.record("announced")
	s.mu.Unlock()
	return nil
}

// seedPaused pre-loads the sink as a previous process left it, without
// recording a transition this process did not make.
func (s *structuralSink) seedPaused(reason PauseReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.reason = StatusPaused, reason
}

func (s *announcingSink) announced() []announcement {
	s.amu.Lock()
	defer s.amu.Unlock()
	return append([]announcement(nil), s.announcements...)
}

// attempted returns every call, including the ones that returned an error.
func (s *announcingSink) attempted() []announcement {
	s.amu.Lock()
	defer s.amu.Unlock()
	return append([]announcement(nil), s.calls...)
}

// assertNever polls cond over window and fails the moment it becomes true. It is
// the negative counterpart of waitFor: the window bounds the OBSERVATION, and a
// violation fails immediately rather than at the end.
func assertNever(t *testing.T, window time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(window)
	for {
		if cond() {
			t.Fatal(msg)
		}
		select {
		case <-deadline:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestSupervisor_StructuralProbe_RecoversAndRedeliversNakdMessage proves the
// opted-in path end to end: a structural failure pauses the pump, the probe
// adjudicates the condition itself (failing structurally twice, then passing),
// the pump resumes with no operator action, and the message that failed comes
// BACK — within the probe's own cadence rather than after the 30s AckWait, which
// is only possible because the structural failure Nak'd it with a delay.
func TestSupervisor_StructuralProbe_RecoversAndRedeliversNakdMessage(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &announcingSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var probes, delivered, acked int32
	var repaired atomic.Bool
	structuralErr := errors.New(`column "reason" does not exist`)

	spec := ConsumerSpec{
		Name:            "sup-structural-recover",
		Stream:          "KV_" + bucket,
		FilterSubject:   "$KV." + bucket + ".vtx.meta.>",
		Health:          sink,
		ProbeInterval:   50 * time.Millisecond,
		StructuralProbe: true,
		// Long enough that AckWait redelivery cannot be what brings the message
		// back inside this test: only the delayed Nak can.
		AckWait: 30 * time.Second,
		Classify: func(err error) FailureClass {
			if errors.Is(err, structuralErr) {
				return ClassStructural
			}
			return ClassTransient
		},
		Probe: func(_ context.Context) error {
			if atomic.AddInt32(&probes, 1) < 3 {
				return structuralErr
			}
			repaired.Store(true)
			return nil
		},
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			atomic.AddInt32(&delivered, 1)
			if !repaired.Load() {
				return Ack, structuralErr
			}
			atomic.AddInt32(&acked, 1)
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.struct", []byte(`{"v":1}`))

	waitFor(t, 3*time.Second, func() bool {
		st, rs, cause := sink.snapshot()
		return st == StatusPaused && rs == PauseStructural && cause == structuralErr.Error()
	}, "structural pause with its cause was not persisted")

	// No operator acts. The probe alone must return the pump to service AND the
	// Nak'd message must be redelivered and acked.
	waitFor(t, 5*time.Second, func() bool {
		st, _, _ := sink.snapshot()
		return st == StatusActive && atomic.LoadInt32(&acked) > 0
	}, "structural pause did not self-heal and redeliver within the probe cadence")

	if got := atomic.LoadInt32(&delivered); got < 2 {
		t.Fatalf("message delivered %d time(s); the failing message must come back after the resume", got)
	}
	// A probe that fails on the tier it is already paused at must not bounce the
	// entry through active on its way to the next attempt.
	if got := sink.states(); len(got) != 3 || got[0] != "paused:structural" || got[1] != "active" || got[2] != "announced" {
		t.Fatalf("health transitions = %v, want [paused:structural active announced] with no flicker", got)
	}
	// Nor may it rewrite the entry per attempt: two probes reported the same
	// fault, and a rewritten entry is a REFRESHED entry — a permanently broken
	// consumer must not publish a permanently fresh one.
	if got := sink.pausedWriteCount(); got != 1 {
		t.Fatalf("health entry written %d times for one unchanged fault, want 1", got)
	}
	// The recovery is announced once, to a sink that asked to hear about it,
	// carrying the pause's own diagnosis and the attempt that lifted it.
	got := sink.announced()
	if len(got) != 1 {
		t.Fatalf("announcements = %+v, want exactly one", got)
	}
	if got[0].cause != structuralErr.Error() || got[0].attempts != 1 {
		t.Fatalf("announcement = %+v, want {cause: %q, attempts: 1}", got[0], structuralErr.Error())
	}
}

// TestSupervisor_StructuralProbe_ProbeReportsNewFault_RepersistsOnce proves the
// other half of the churn rule: a fault that CHANGES is news and reaches the
// operator, even though the pump never left the pause.
func TestSupervisor_StructuralProbe_ProbeReportsNewFault_RepersistsOnce(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &structuralSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	firstErr := errors.New("relation does not exist")
	secondErr := errors.New("unique constraint is missing")
	var probes int32

	spec := ConsumerSpec{
		Name:            "sup-structural-newfault",
		Stream:          "KV_" + bucket,
		FilterSubject:   "$KV." + bucket + ".vtx.meta.>",
		Health:          sink,
		ProbeInterval:   40 * time.Millisecond,
		StructuralProbe: true,
		AckWait:         30 * time.Second,
		Classify:        func(_ error) FailureClass { return ClassStructural },
		// The first two attempts report the original fault, then the diagnosis
		// changes and stays changed.
		Probe: func(_ context.Context) error {
			if atomic.AddInt32(&probes, 1) <= 2 {
				return firstErr
			}
			return secondErr
		},
		Handler: func(_ context.Context, _ Message) (Decision, error) { return Ack, firstErr },
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.newfault", []byte(`{"v":1}`))

	waitFor(t, 5*time.Second, func() bool {
		_, _, cause := sink.snapshot()
		return cause == secondErr.Error()
	}, "the changed fault never reached the health entry")

	// Exactly two writes: the pause itself, and the one changed diagnosis —
	// however many times each was observed.
	assertNever(t, 300*time.Millisecond, func() bool {
		return sink.pausedWriteCount() > 2
	}, "an unchanged fault kept rewriting the health entry")
}

// TestSupervisor_StructuralProbe_ResumeDuringProbe_DiscardsTheVerdict drives the
// interleaving that a probe returning instantly can never reach: an operator
// Resume landing WHILE the probe is in flight. The verdict that comes back
// answers a question about a pause the human has already lifted, so it must be
// dropped whole — no announcement crediting the platform with a recovery a
// person performed, and no relapse left armed across the Resume that exists to
// disarm it.
func TestSupervisor_StructuralProbe_ResumeDuringProbe_DiscardsTheVerdict(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &announcingSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var delivered, processed, probes int32
	inProbe := make(chan struct{}, 1)
	release := make(chan struct{})
	structuralErr := errors.New("table is missing a column")

	spec := ConsumerSpec{
		Name:            "sup-structural-resumerace",
		Stream:          "KV_" + bucket,
		FilterSubject:   "$KV." + bucket + ".vtx.meta.>",
		Health:          sink,
		ProbeInterval:   30 * time.Millisecond,
		StructuralProbe: true,
		AckWait:         30 * time.Second,
		Classify:        func(_ error) FailureClass { return ClassStructural },
		// The FIRST probe hands control to the test and blocks there, so the
		// operator's Resume is guaranteed to land mid-flight. It then passes,
		// which is the verdict that must be discarded.
		Probe: func(_ context.Context) error {
			if atomic.AddInt32(&probes, 1) == 1 {
				inProbe <- struct{}{}
				<-release
			}
			return nil
		},
		// Fails structurally twice: once to open the race, once after it, so the
		// SECOND pause shows whether the first left the relapse counter armed.
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			if atomic.AddInt32(&delivered, 1) <= 2 {
				return Ack, structuralErr
			}
			atomic.AddInt32(&processed, 1)
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.race", []byte(`{"v":1}`))

	select {
	case <-inProbe:
	case <-time.After(5 * time.Second):
		t.Fatal("the probe loop was never entered")
	}
	sup.Resume(ctx, spec.Name)
	close(release)

	waitFor(t, 10*time.Second, func() bool {
		return atomic.LoadInt32(&processed) > 0
	}, "the consumer never returned to service")

	// Exactly one announcement — the second, genuine self-heal — and it is the
	// FIRST attempt of a fresh chain, because the Resume reset the counter.
	got := sink.announced()
	if len(got) != 1 {
		t.Fatalf("announcements = %+v, want exactly one (a resume mid-probe must not fabricate one)", got)
	}
	if got[0].attempts != 1 {
		t.Fatalf("announcement = %+v, want attempts 1: a discarded verdict must not arm the relapse counter", got[0])
	}
}

// TestSupervisor_StructuralProbe_FailedAnnouncement_RetriesAtNextOpen proves the
// recovery survives a health store that is briefly unreachable — plausibly the
// same blip that paused the consumer. A dropped announcement is a lens that
// self-healed with nothing but a log line behind it.
func TestSupervisor_StructuralProbe_FailedAnnouncement_RetriesAtNextOpen(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &announcingSink{}
	sink.failAnnouncements(1)
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var delivered, processed int32
	structuralErr := errors.New("grant table was dropped")

	spec := ConsumerSpec{
		Name:            "sup-structural-announceretry",
		Stream:          "KV_" + bucket,
		FilterSubject:   "$KV." + bucket + ".vtx.meta.>",
		Health:          sink,
		ProbeInterval:   30 * time.Millisecond,
		StructuralProbe: true,
		AckWait:         30 * time.Second,
		Classify:        func(_ error) FailureClass { return ClassStructural },
		Probe:           func(_ context.Context) error { return nil },
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			if atomic.AddInt32(&delivered, 1) == 1 {
				return Ack, structuralErr
			}
			atomic.AddInt32(&processed, 1)
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.retry", []byte(`{"v":1}`))

	// The first announcement is attempted and fails; the pump carries on.
	waitFor(t, 10*time.Second, func() bool {
		return len(sink.attempted()) == 1 && atomic.LoadInt32(&processed) > 0
	}, "the recovery was never announced, or the pump did not resume")
	if got := sink.announced(); len(got) != 0 {
		t.Fatalf("announced = %+v after a failing sink, want none recorded", got)
	}

	// The next open retries it — here provoked by a Reset, which is one of the
	// reopens that occur on their own in a running system.
	if err := sup.Reset(ctx, spec.Name); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	waitFor(t, 10*time.Second, func() bool {
		return len(sink.announced()) == 1
	}, "a failed announcement was dropped instead of retried at the next open")

	tried := sink.attempted()
	if len(tried) != 2 {
		t.Fatalf("calls = %+v, want exactly 2 (one failed, one retried)", tried)
	}
	for i, a := range tried {
		if a.cause != structuralErr.Error() || a.attempts != 1 {
			t.Fatalf("call %d = %+v, want {cause: %q, attempts: 1} — the retry must carry the same recovery",
				i, a, structuralErr.Error())
		}
	}
}

// TestSupervisor_StructuralProbe_RestoredPause_AnnouncesAfterTheSecondGate is
// the placement pin. A restored structural pause that probes its way out falls
// back through runPump's InitialPause seeding and re-verifies its precondition
// BEFORE its first projection, so the pump passes two gates, not one. The
// announcement must land after the second — announcing at the structural clear
// would tell the operator "recovered" while the activation gate is still shut.
func TestSupervisor_StructuralProbe_RestoredPause_AnnouncesAfterTheSecondGate(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	// A previous process left this consumer structurally paused.
	sink := &announcingSink{}
	sink.seedPaused(PauseStructural)

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	spec := ConsumerSpec{
		Name:            "sup-structural-restored",
		Stream:          "KV_" + bucket,
		FilterSubject:   "$KV." + bucket + ".vtx.meta.>",
		Health:          sink,
		ProbeInterval:   30 * time.Millisecond,
		StructuralProbe: true,
		InitialPause:    PauseInfra,
		AckWait:         30 * time.Second,
		Classify:        func(_ error) FailureClass { return ClassStructural },
		Probe:           func(_ context.Context) error { return nil },
		Handler:         func(_ context.Context, _ Message) (Decision, error) { return Ack, nil },
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	waitFor(t, 5*time.Second, func() bool {
		return len(sink.announced()) == 1
	}, "a restored structural pause was never announced as recovered")

	want := []string{"active", "paused:infra", "active", "announced"}
	got := sink.states()
	if len(got) != len(want) {
		t.Fatalf("lifecycle = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lifecycle = %v, want %v (the announcement must follow the activation gate, not the structural clear)", got, want)
		}
	}
	// The cause is empty here and that is honest: HealthSink.Load returns the
	// status and reason, never the recorded cause, so a pause this process did
	// not itself enter has no diagnosis in memory to announce.
	if a := sink.announced()[0]; a.attempts != 1 || a.cause != "" {
		t.Fatalf("announcement = %+v, want {cause: \"\", attempts: 1}", a)
	}
}

// TestSupervisor_StructuralProbe_OperatorResumeIsNotAnnounced pins the boundary
// of the announcement: it names the recovery NOBODY performed. A pause an
// operator lifted is not one, and must produce nothing.
func TestSupervisor_StructuralProbe_OperatorResumeIsNotAnnounced(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &announcingSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var processed int32
	var failNext atomic.Bool
	failNext.Store(true)
	structuralErr := errors.New("table was never provisioned")

	spec := ConsumerSpec{
		Name:            "sup-structural-operator",
		Stream:          "KV_" + bucket,
		FilterSubject:   "$KV." + bucket + ".vtx.meta.>",
		Health:          sink,
		ProbeInterval:   40 * time.Millisecond,
		StructuralProbe: true,
		AckWait:         30 * time.Second,
		Classify:        func(_ error) FailureClass { return ClassStructural },
		// Never adjudicates recovery: only the operator can lift this pause.
		Probe: func(_ context.Context) error { return structuralErr },
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			if failNext.Swap(false) {
				return Ack, structuralErr
			}
			atomic.AddInt32(&processed, 1)
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.operator", []byte(`{"v":1}`))

	waitFor(t, 5*time.Second, func() bool {
		st, rs, _ := sink.snapshot()
		return st == StatusPaused && rs == PauseStructural
	}, "structural pause not persisted")

	sup.Resume(ctx, spec.Name)
	waitFor(t, 5*time.Second, func() bool {
		return atomic.LoadInt32(&processed) > 0
	}, "pump did not drain after the operator resumed it")

	if got := sink.announced(); len(got) != 0 {
		t.Fatalf("announcements = %+v, want none: an operator lifted this pause", got)
	}
}

// TestSupervisor_StructuralProbe_Off_BlocksUntilResumeAndLeavesMessagePending is
// the default-off regression guard: a consumer that leaves StructuralProbe false
// must behave exactly as it does without the flag — the probe is never consulted
// for a structural pause, the pump waits for an operator, and the failing message
// is left PENDING (no Nak), so its redelivery waits out AckWait.
func TestSupervisor_StructuralProbe_Off_BlocksUntilResumeAndLeavesMessagePending(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const ackWait = 2 * time.Second
	sink := &structuralSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var probes int32
	var mu sync.Mutex
	var deliveries []time.Time
	structuralErr := errors.New("relation does not exist")

	spec := ConsumerSpec{
		Name:          "sup-structural-off",
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Health:        sink,
		ProbeInterval: 50 * time.Millisecond,
		AckWait:       ackWait,
		Classify: func(err error) FailureClass {
			if errors.Is(err, structuralErr) {
				return ClassStructural
			}
			return ClassTransient
		},
		// A probe that would resume the pump instantly if it were ever consulted.
		Probe: func(_ context.Context) error {
			atomic.AddInt32(&probes, 1)
			return nil
		},
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			mu.Lock()
			deliveries = append(deliveries, time.Now())
			n := len(deliveries)
			mu.Unlock()
			if n == 1 {
				return Ack, structuralErr
			}
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.off", []byte(`{"v":1}`))

	waitFor(t, 3*time.Second, func() bool {
		st, rs, _ := sink.snapshot()
		return st == StatusPaused && rs == PauseStructural
	}, "structural pause not persisted")

	// The pump must stay down: no probe, no redelivery, no resume of its own.
	assertNever(t, 400*time.Millisecond, func() bool {
		st, _, _ := sink.snapshot()
		return st == StatusActive
	}, "a StructuralProbe:false consumer resumed itself from a structural pause")
	if got := atomic.LoadInt32(&probes); got != 0 {
		t.Fatalf("probe called %d time(s) for a StructuralProbe:false structural pause; it must never be consulted", got)
	}
	mu.Lock()
	n := len(deliveries)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("message delivered %d times while paused, want 1", n)
	}

	sup.Resume(ctx, spec.Name)
	waitFor(t, 2*ackWait+3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(deliveries) >= 2
	}, "message was not redelivered after Resume")

	mu.Lock()
	gap := deliveries[1].Sub(deliveries[0])
	mu.Unlock()
	// Left pending, the un-acked message can only come back when its ack window
	// expires. A delayed Nak would have pulled it forward to one probe interval.
	if gap < ackWait*4/5 {
		t.Fatalf("redelivery gap %v < AckWait %v: the message was Nak'd forward, not left pending", gap, ackWait)
	}
}

// TestSupervisor_StructuralProbe_WithoutProbe_LeavesMessagePending pins that
// ONE predicate decides whether a consumer heals itself. StructuralProbe with no
// Probe cannot self-heal, so the pump waits for an operator — and the message
// must be left pending accordingly, not Nak'd forward for a retest that will
// never happen.
func TestSupervisor_StructuralProbe_WithoutProbe_LeavesMessagePending(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const ackWait = 2 * time.Second
	sink := &structuralSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var mu sync.Mutex
	var deliveries []time.Time
	structuralErr := errors.New("view was never created")

	spec := ConsumerSpec{
		Name:          "sup-structural-noprobe",
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Health:        sink,
		ProbeInterval: 40 * time.Millisecond,
		// Opted in, but with nothing to adjudicate the condition.
		StructuralProbe: true,
		AckWait:         ackWait,
		Classify:        func(_ error) FailureClass { return ClassStructural },
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			mu.Lock()
			deliveries = append(deliveries, time.Now())
			n := len(deliveries)
			mu.Unlock()
			if n == 1 {
				return Ack, structuralErr
			}
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.noprobe", []byte(`{"v":1}`))

	waitFor(t, 3*time.Second, func() bool {
		st, rs, _ := sink.snapshot()
		return st == StatusPaused && rs == PauseStructural
	}, "structural pause not persisted")
	assertNever(t, 300*time.Millisecond, func() bool {
		st, _, _ := sink.snapshot()
		return st == StatusActive
	}, "a consumer with no Probe resumed itself from a structural pause")

	sup.Resume(ctx, spec.Name)
	waitFor(t, 2*ackWait+3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(deliveries) >= 2
	}, "message was not redelivered after Resume")

	mu.Lock()
	gap := deliveries[1].Sub(deliveries[0])
	mu.Unlock()
	if gap < ackWait*4/5 {
		t.Fatalf("redelivery gap %v < AckWait %v: a consumer that cannot probe Nak'd its message forward anyway", gap, ackWait)
	}
}

// TestSupervisor_StructuralProbe_ManualPauseNeverProbes proves the dominance
// rule: a manual pause held alongside a structural one is an operator holding
// the pump down, and no probe — however healthy — may lift it.
func TestSupervisor_StructuralProbe_ManualPauseNeverProbes(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &structuralSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var probes, processed int32
	var failNext atomic.Bool
	failNext.Store(true)
	inHandler := make(chan struct{}, 1)
	release := make(chan struct{})
	structuralErr := errors.New("schema drifted")

	spec := ConsumerSpec{
		Name:            "sup-structural-manual",
		Stream:          "KV_" + bucket,
		FilterSubject:   "$KV." + bucket + ".vtx.meta.>",
		Health:          sink,
		ProbeInterval:   30 * time.Millisecond,
		StructuralProbe: true,
		AckWait:         30 * time.Second,
		Classify: func(err error) FailureClass {
			if errors.Is(err, structuralErr) {
				return ClassStructural
			}
			return ClassTransient
		},
		Probe: func(_ context.Context) error {
			atomic.AddInt32(&probes, 1)
			return nil
		},
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			if failNext.Swap(false) {
				// Hand control to the test so the manual pause is in place
				// BEFORE the structural pause reaches the pause machinery.
				inHandler <- struct{}{}
				<-release
				return Ack, structuralErr
			}
			atomic.AddInt32(&processed, 1)
			return Ack, nil
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.manual", []byte(`{"v":1}`))

	select {
	case <-inHandler:
	case <-time.After(5 * time.Second):
		t.Fatal("handler was never called")
	}
	if !sup.Pause(ctx, spec.Name) {
		t.Fatal("Pause: consumer not managed")
	}
	close(release)

	waitFor(t, 3*time.Second, func() bool {
		st, rs, _ := sink.snapshot()
		return st == StatusPaused && rs == PauseManual
	}, "manual pause did not dominate the structural one")

	assertNever(t, 300*time.Millisecond, func() bool {
		return atomic.LoadInt32(&probes) > 0
	}, "a manual pause was probed: the structural probe must test the whole reason set")
	if got := atomic.LoadInt32(&processed); got != 0 {
		t.Fatalf("pump processed %d message(s) while manually paused", got)
	}

	// Positive vector: the machinery is live, the manual reason was simply
	// dominant. An operator Resume clears both and the message drains.
	sup.Resume(ctx, spec.Name)
	waitFor(t, 5*time.Second, func() bool {
		return atomic.LoadInt32(&processed) > 0
	}, "pump did not drain after the operator resumed both reasons")
}

// TestSupervisor_StructuralProbe_RelapseLatchAndOperatorUnlatch proves the
// bound and its release: a probe that keeps adjudicating recovery while the
// condition still holds gets structuralRelapseLimit attempts, then the worker
// latches into operator-only pausing carrying the cause AND the fact that the
// platform tried; and an operator Resume gives it a fresh set of attempts.
//
// The probe fails structurally between passes, which is the shape that separates
// "a self-heal that did not hold" from "a probe that never passed": only the
// former may spend an attempt.
func TestSupervisor_StructuralProbe_RelapseLatchAndOperatorUnlatch(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	sink := &announcingSink{}
	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	var probes, delivered int32
	structuralErr := errors.New("row is not projectable")

	spec := ConsumerSpec{
		Name:            "sup-structural-latch",
		Stream:          "KV_" + bucket,
		FilterSubject:   "$KV." + bucket + ".vtx.meta.>",
		Health:          sink,
		ProbeInterval:   50 * time.Millisecond,
		StructuralProbe: true,
		AckWait:         30 * time.Second,
		Classify: func(err error) FailureClass {
			if errors.Is(err, structuralErr) {
				return ClassStructural
			}
			return ClassTransient
		},
		// Alternates: still-broken, then "recovered" — a probe whose verdict the
		// handler keeps refuting.
		Probe: func(_ context.Context) error {
			if atomic.AddInt32(&probes, 1)%2 == 1 {
				return structuralErr
			}
			return nil
		},
		// The condition never actually clears.
		Handler: func(_ context.Context, _ Message) (Decision, error) {
			atomic.AddInt32(&delivered, 1)
			return Ack, structuralErr
		},
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.latch", []byte(`{"v":1}`))

	wantPrefix := fmt.Sprintf("structural pause latched after %d self-heal attempts: ", structuralRelapseLimit)
	waitFor(t, 15*time.Second, func() bool {
		st, rs, cause := sink.snapshot()
		return st == StatusPaused && rs == PauseStructural && strings.HasPrefix(cause, wantPrefix)
	}, "the pump never latched after "+wantPrefix)

	if _, _, cause := sink.snapshot(); !strings.HasSuffix(cause, structuralErr.Error()) {
		t.Fatalf("latched cause %q dropped the diagnosis %q", cause, structuralErr.Error())
	}
	// Delivery count is the attempt count made visible: the first failure plus
	// one per relapse.
	if got, want := atomic.LoadInt32(&delivered), int32(structuralRelapseLimit+1); got != want {
		t.Fatalf("handler saw %d deliveries, want %d (one per self-heal attempt, plus the first failure)", got, want)
	}

	// Every attempt was announced, in order, each carrying the diagnosis the
	// pause held at the time it was lifted.
	announced := sink.announced()
	if len(announced) != structuralRelapseLimit {
		t.Fatalf("announcements = %+v, want %d (one per self-heal attempt)", announced, structuralRelapseLimit)
	}
	for i, a := range announced {
		if a.attempts != i+1 || a.cause != structuralErr.Error() {
			t.Fatalf("announcement %d = %+v, want {cause: %q, attempts: %d}", i, a, structuralErr.Error(), i+1)
		}
	}

	// Latched: no further probing, whatever the probe would say.
	probesAtLatch := atomic.LoadInt32(&probes)
	assertNever(t, 400*time.Millisecond, func() bool {
		return atomic.LoadInt32(&probes) > probesAtLatch
	}, "a latched worker kept probing: the latch must make StructuralProbe read false")
	if st, _, _ := sink.snapshot(); st != StatusPaused {
		t.Fatalf("latched worker is %v, want paused until an operator acts", st)
	}

	// An operator Resume returns a fresh set of attempts: the pump re-pauses on
	// the still-broken condition and then resumes ITSELF, which a latched worker
	// cannot do. Asserted over the transition record, not the instantaneous
	// state — a self-heal that holds for one message is a window a poll can miss.
	sup.Resume(ctx, spec.Name)
	base := len(sink.states())
	waitFor(t, 15*time.Second, func() bool {
		return hasPausedThenActive(sink.states(), base)
	}, "the latch survived an operator Resume: no further self-heal was attempted")
}

// hasPausedThenActive reports whether the transitions from base onwards contain
// a structural pause followed by a return to active — i.e. one self-heal that
// no operator triggered.
func hasPausedThenActive(states []string, base int) bool {
	if len(states) <= base {
		return false
	}
	tail := states[base:]
	for i, s := range tail {
		if s != "paused:structural" {
			continue
		}
		for _, later := range tail[i+1:] {
			if later == "active" {
				return true
			}
		}
		return false
	}
	return false
}
