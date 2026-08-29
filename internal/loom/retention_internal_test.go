package loom

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/healthkv"
)

// Instance ids for the retention tests: valid 20-character NanoIDs (Contract #1
// alphabet), because the trigger path and the instance.<id> / instance.<id>.pattern
// key split both assume one.
const (
	retentionInstanceID = "kkkkmmmmnnnnppppqqqq"
	retentionPatternRef = "vtx.meta.RetentionPatternAbc"
)

// retentionPattern is a one-step pattern, enough to satisfy the pattern pin
// createInstance/redrive co-write.
func retentionPattern() *Pattern {
	return &Pattern{
		PatternID:   "retention-pattern",
		SubjectType: "widget",
		Steps:       []Step{{Kind: StepKindSystemOp, Operation: "StepA"}},
	}
}

// newRetentionStore returns a stateStore over a loom-state bucket provisioned
// exactly as bootstrap provisions it for the TTL path — LimitMarkerTTL (which is
// what enables AllowMsgTTL, so a Nats-TTL header is accepted) plus atomic
// publish for the transition batch — with retention pre-resolved to the value
// Engine.Start would have written.
func newRetentionStore(t *testing.T, retention time.Duration) (*stateStore, context.Context) {
	t.Helper()
	conn, ctx := newControlTestConn(t)
	s := newStateStore(conn, "loom-state")
	s.instanceRetention = retention
	return s, ctx
}

// instanceMsgTTL returns the Nats-TTL header on the CURRENT (last) message for
// the instance record's KV subject — "" when the record carries no expiry. This
// reads the backing stream rather than the KV view because the TTL is a message
// header the KV entry does not surface.
func instanceMsgTTL(t *testing.T, ctx context.Context, s *stateStore, instanceID string) string {
	t.Helper()
	stream, err := s.conn.JetStream().Stream(ctx, "KV_"+s.bucket)
	require.NoError(t, err)
	msg, err := stream.GetLastMsgForSubject(ctx, "$KV."+s.bucket+"."+instanceKey(instanceID))
	require.NoError(t, err, "instance record must be present on the backing stream")
	return msg.Header.Get("Nats-TTL")
}

// instanceRecordPresent reports whether the instance cursor record is still
// readable through the KV view (an expired record reads as ErrKeyNotFound).
func instanceRecordPresent(t *testing.T, ctx context.Context, s *stateStore, instanceID string) bool {
	t.Helper()
	inst, err := s.getInstance(ctx, instanceID)
	require.NoError(t, err)
	return inst != nil
}

// instanceRecordGone reports whether the cursor record has actually vanished.
// It is the predicate for the Eventually/Never polls, so it must never assert:
// those run the predicate on their own goroutine, which can outlive the test
// body and therefore observe the fixture context after t.Cleanup cancels it. A
// read that errors proves nothing about absence, so it answers "not gone" —
// only an authoritative not-found does. Asserting here instead would fail a
// passing test from a non-test goroutine, with the read error standing in for a
// verdict the poll never actually reached.
func instanceRecordGone(ctx context.Context, s *stateStore, instanceID string) bool {
	inst, err := s.getInstance(ctx, instanceID)
	if err != nil {
		return false
	}
	return inst == nil
}

// seedRunningInstance creates the instance + its pattern pin the way the trigger
// path does, so the terminal transition under test runs against a real record.
func seedRunningInstance(t *testing.T, ctx context.Context, s *stateStore) *Instance {
	t.Helper()
	inst := &Instance{
		InstanceID: retentionInstanceID,
		PatternRef: retentionPatternRef,
		SubjectKey: "vtx.widget.aaaabbbbccccddddeeee",
		Cursor:     0,
		Status:     StatusRunning,
	}
	require.NoError(t, s.createInstance(ctx, inst, retentionPattern()))
	return inst
}

// A terminal transition stamps the retention TTL on the instance cursor record,
// which is what bounds loom-state's growth: the record expires a retention
// window after the instance finished instead of being kept forever.
func TestTransition_TerminalStampsRetentionTTL(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	s, ctx := newRetentionStore(t, 8*24*time.Hour)
	inst := seedRunningInstance(t, ctx, s)

	inst.Status = StatusComplete
	inst.Cursor = 1
	require.NoError(t, s.transition(ctx, inst, "", "", nil, 0))

	require.Equal(t, (8 * 24 * time.Hour).String(), instanceMsgTTL(t, ctx, s, inst.InstanceID),
		"a terminal cursor record must carry the retention TTL")
	require.True(t, instanceRecordPresent(t, ctx, s, inst.InstanceID),
		"the record is retained for the window, not deleted at terminal")
}

// The positive vector for every "must not expire" assertion below: the stamped
// TTL is really enforced by the server, so a terminal record that is never
// resumed does disappear once its window elapses. Without this, an assertion
// that some record survives proves nothing.
func TestTransition_TerminalRecordExpiresAfterRetention(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const shortRetention = time.Second
	s, ctx := newRetentionStore(t, shortRetention)
	inst := seedRunningInstance(t, ctx, s)

	inst.Status = StatusComplete
	require.NoError(t, s.transition(ctx, inst, "", "", nil, 0))
	require.True(t, instanceRecordPresent(t, ctx, s, inst.InstanceID),
		"the record is readable for its retention window")

	require.Eventually(t, func() bool {
		return instanceRecordGone(ctx, s, inst.InstanceID)
	}, 20*shortRetention, 200*time.Millisecond,
		"a terminal record must expire once its retention window elapses")
}

// The load-bearing invariant behind the whole design: a resume CLEARS the TTL.
// loom-state is History:1, so the untagged instance PUT a resume performs evicts
// the TTL-bearing message on that subject and the record stops expiring. If this
// were false, an operator resuming an about-to-expire failed instance would
// watch its cursor vanish mid-flight — and every terminal record would need an
// explicit TTL-clearing write instead.
//
// This drives the whole real path: a running instance is failed through the
// terminal batch that stamps the TTL, then transitioned back to running. The
// retention window is one second (the NATS per-key TTL floor), so an un-cleared
// TTL expires well inside the test rather than at a horizon it could not observe.
func TestTransition_ResumeClearsRetentionTTL(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const shortRetention = time.Second
	s, ctx := newRetentionStore(t, shortRetention)
	inst := seedRunningInstance(t, ctx, s)

	inst.Status = StatusFailed
	require.NoError(t, s.transition(ctx, inst, "", "", nil, 0))
	require.Equal(t, shortRetention.String(), instanceMsgTTL(t, ctx, s, inst.InstanceID),
		"precondition: the failed record must be expiring before the resume")

	inst.Status = StatusRunning
	require.NoError(t, s.transition(ctx, inst, "", "", nil, time.Minute))

	require.Empty(t, instanceMsgTTL(t, ctx, s, inst.InstanceID),
		"the resume's untagged PUT must evict the TTL-bearing message (History:1)")
	require.Never(t, func() bool {
		return instanceRecordGone(ctx, s, inst.InstanceID)
	}, 6*shortRetention, 200*time.Millisecond,
		"a resumed instance must not expire on the TTL its terminal stamped")
}

// The operator's resume path — RedriveInstance → stateStore.redrive — clears the
// TTL the same way, through the same untagged instance PUT, from the state a
// REAL terminal batch leaves behind (TTL stamped on the cursor, pattern pin
// deleted).
func TestRedrive_ClearsRetentionTTL(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	const shortRetention = time.Second
	s, ctx := newRetentionStore(t, shortRetention)
	inst := seedRunningInstance(t, ctx, s)

	inst.Status = StatusFailed
	require.NoError(t, s.transition(ctx, inst, "", "", nil, 0))
	require.Equal(t, shortRetention.String(), instanceMsgTTL(t, ctx, s, inst.InstanceID),
		"precondition: the failed record must be expiring before the redrive")

	_, revision, err := s.getInstanceAtRevision(ctx, inst.InstanceID)
	require.NoError(t, err)
	inst.Status = StatusRunning
	require.NoError(t, s.redrive(ctx, inst, retentionPattern(), revision))

	require.Empty(t, instanceMsgTTL(t, ctx, s, inst.InstanceID),
		"the redrive's untagged PUT must evict the TTL-bearing message (History:1)")
	require.Never(t, func() bool {
		return instanceRecordGone(ctx, s, inst.InstanceID)
	}, 6*shortRetention, 200*time.Millisecond,
		"a redriven instance must not expire on the old terminal TTL")
}

// A non-terminal transition never stamps a TTL: a running instance's cursor is
// live state, and an expiry on it would drop an in-flight instance.
func TestTransition_RunningStampsNoTTL(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	s, ctx := newRetentionStore(t, 8*24*time.Hour)
	inst := seedRunningInstance(t, ctx, s)

	inst.Cursor = 1
	inst.PendingToken = "step-1-token"
	require.NoError(t, s.transition(ctx, inst, inst.PendingToken, "", nil, time.Minute))

	require.Empty(t, instanceMsgTTL(t, ctx, s, inst.InstanceID),
		"a running cursor record must never carry an expiry")
}

// The PendingToken guard suppresses the stamp on a terminal record: expiring a
// cursor that still names a pending token would strand that token.<t>/outbox.<t>
// peer with nothing to resolve it back to.
func TestTransition_TerminalWithPendingTokenStampsNoTTL(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	s, ctx := newRetentionStore(t, 8*24*time.Hour)
	inst := seedRunningInstance(t, ctx, s)

	inst.Status = StatusFailed
	inst.PendingToken = "orphan-token"
	require.NoError(t, s.transition(ctx, inst, "", "", nil, 0))

	require.Empty(t, instanceMsgTTL(t, ctx, s, inst.InstanceID),
		"a terminal still naming a pending token must be retained, not expired")
}

// With pruning off (the safety gate's OFF value threaded into the store), a
// terminal transition stamps nothing — the pre-gate behaviour, unchanged.
func TestTransition_PruningOffStampsNoTTL(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	s, ctx := newRetentionStore(t, 0)
	inst := seedRunningInstance(t, ctx, s)

	inst.Status = StatusComplete
	require.NoError(t, s.transition(ctx, inst, "", "", nil, 0))

	require.Empty(t, instanceMsgTTL(t, ctx, s, inst.InstanceID),
		"pruning off must leave terminal records unexpiring")
}

// The safety gate: pruning is enabled only when the retention window strictly
// exceeds the events stream's configured MaxAge, and every other outcome —
// window too short, equal to MaxAge, an unlimited stream, or a stream whose
// config cannot be read — resolves to OFF with an operator-visible issue.
func TestResolveInstanceRetention_Gate(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	cases := []struct {
		name         string
		createStream bool
		maxAge       time.Duration
		retention    time.Duration
		want         time.Duration
	}{
		{"window clears MaxAge: pruning on", true, time.Hour, 8 * 24 * time.Hour, 8 * 24 * time.Hour},
		{"window shorter than MaxAge: off", true, 7 * 24 * time.Hour, 24 * time.Hour, 0},
		{"window equal to MaxAge is not strictly greater: off", true, 8 * 24 * time.Hour, 8 * 24 * time.Hour, 0},
		{"unlimited stream retention: off", true, 0, 8 * 24 * time.Hour, 0},
		{"stream config unreadable: fails closed", false, 0, 8 * 24 * time.Hour, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conn, ctx := newControlTestConn(t)
			if tc.createStream {
				_, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
					Name:      "core-events",
					Subjects:  []string{"events.>"},
					Retention: jetstream.LimitsPolicy,
					MaxAge:    tc.maxAge,
				})
				require.NoError(t, err)
			}
			e := NewEngine(conn, Config{
				LoomStateBucket:   "loom-state",
				EventsStream:      "core-events",
				ActorKey:          "vtx.identity.LoomCtrlActor123",
				InstanceRetention: tc.retention,
				Logger:            controlTestLogger(),
			})

			got, issue := e.resolveInstanceRetention(ctx)
			require.Equal(t, tc.want, got)
			if tc.want > 0 {
				require.Nil(t, issue, "pruning on raises no issue")
				return
			}
			require.NotNil(t, issue, "pruning off must be operator-visible")
			require.Equal(t, instanceRetentionDisabledCode, issue.Code)
			require.Equal(t, "warning", issue.Severity)
			require.NotEmpty(t, issue.Since)
		})
	}
}

// The sub-second clamp: a retention below the NATS per-key TTL floor would stamp
// nothing at all, so it is clamped up to the floor rather than silently degraded
// (same posture as StepTimeout).
func TestConfig_InstanceRetentionDefaultsAndClamp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero takes the default", 0, 8 * 24 * time.Hour},
		{"negative takes the default", -time.Hour, 8 * 24 * time.Hour},
		{"sub-second clamps up to the TTL floor", 250 * time.Millisecond, time.Second},
		{"an explicit window is kept", 30 * 24 * time.Hour, 30 * 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := Config{InstanceRetention: tc.in}
			c.withDefaults()
			require.Equal(t, tc.want, c.InstanceRetention)
		})
	}
}

// The gate's issue reaches the operator: it rides every heartbeat for the life
// of the process (nothing at runtime can clear it) and degrades the reported
// status, so "pruning is disabled" is visible in health.loom.<instance> rather
// than only in a startup log line.
func TestHeartbeat_CarriesRetentionDisabledIssue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	// LimitMarkerTTL is what enables the per-key TTL the heartbeat's own
	// KVPutWithTTL emit needs (Contract #5 §5.6).
	_, err := conn.JetStream().CreateOrUpdateKeyValue(ctx,
		jetstream.KeyValueConfig{Bucket: "health-kv", LimitMarkerTTL: time.Second})
	require.NoError(t, err)

	e := NewEngine(conn, Config{
		LoomStateBucket:   "loom-state",
		EventsStream:      "core-events",
		ActorKey:          "vtx.identity.LoomCtrlActor123",
		InstanceRetention: time.Hour,
		Logger:            controlTestLogger(),
	})
	retention, issue := e.resolveInstanceRetention(ctx)
	require.Zero(t, retention)
	require.NotNil(t, issue)

	hb := newHeartbeater(conn, "health-kv", "loom-state", "loom-retention-test", time.Second,
		healthkv.NewConsumerStateCache(), controlTestLogger())
	hb.addStaticIssue(*issue)
	hb.emit(ctx, "healthy")
	hb.emit(ctx, "healthy")

	entry, err := conn.KVGet(ctx, "health-kv", "health.loom.loom-retention-test")
	require.NoError(t, err)
	var doc loomHealthDoc
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	require.Len(t, doc.Issues, 1, "a static issue is carried once per heartbeat, not accumulated")
	require.Equal(t, instanceRetentionDisabledCode, doc.Issues[0].Code)
	require.Equal(t, issue.Since, doc.Issues[0].Since, "the since is stamped once, at resolution")
	require.Equal(t, "degraded", doc.Status)
}

// The not-found sentinel is matchable, so the control plane can tell "this id
// never started, or aged out" apart from a KV read failure without matching on
// message text.
func TestInspectInstance_NotFoundIsMatchableSentinel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	conn, ctx := newControlTestConn(t)
	e := newControlEngine(conn)

	_, err := e.InspectInstance(ctx, retentionInstanceID)
	require.ErrorIs(t, err, ErrInstanceNotFound)
	require.Contains(t, err.Error(), retentionInstanceID, "the message must still name the instance")

	require.ErrorIs(t, e.RedriveInstance(ctx, retentionInstanceID), ErrInstanceNotFound)

	// A read failure (bucket absent) is NOT the sentinel.
	other := NewEngine(conn, Config{
		LoomStateBucket: "loom-state-never-provisioned",
		EventsStream:    "core-events",
		ActorKey:        "vtx.identity.LoomCtrlActor123",
		Logger:          controlTestLogger(),
	})
	_, err = other.InspectInstance(ctx, retentionInstanceID)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrInstanceNotFound)
}
