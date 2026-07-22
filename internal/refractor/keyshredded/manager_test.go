package keyshredded

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// fakeNullifier is a control.RowNullifier test double. Delete returns err
// (nil for success) and records every call.
type fakeNullifier struct {
	err   error
	calls []map[string]any
}

func (f *fakeNullifier) Delete(_ context.Context, keys map[string]any, _ uint64) error {
	f.calls = append(f.calls, keys)
	return f.err
}

// fakePauser is a control.Pauser test double recording whether Pause was called.
type fakePauser struct {
	paused bool
}

func (f *fakePauser) Pause(_ context.Context) { f.paused = true }

func newTestManager(t *testing.T, svc *control.Service, targets []NullifyTarget) *Manager {
	t.Helper()
	return New(Config{Control: svc, Targets: targets})
}

func keyShreddedMsg(t *testing.T, identityKey string) substrate.Message {
	t.Helper()
	body := []byte(`{"payload":{"identityKey":"` + identityKey + `"}}`)
	return substrate.Message{Body: body}
}

func TestHandleKeyShredded_NoTargets_AcksAndCounts(t *testing.T) {
	svc := control.NewService()
	m := newTestManager(t, svc, nil)

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, uint64(1), m.HandledTotal())
}

func TestHandleKeyShredded_TargetSucceeds_DeletesAndAcks(t *testing.T) {
	svc := control.NewService()
	nullifier := &fakeNullifier{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, uint64(1), m.HandledTotal())
	require.Len(t, nullifier.calls, 1)
	require.Equal(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA", nullifier.calls[0]["identityKey"])
}

func TestHandleKeyShredded_MultipleTargets_AllAttempted(t *testing.T) {
	svc := control.NewService()
	nullifierA := &fakeNullifier{}
	nullifierB := &fakeNullifier{}
	svc.RegisterRowNullifier("lens-a", nullifierA)
	svc.RegisterRowNullifier("lens-b", nullifierB)
	m := newTestManager(t, svc, []NullifyTarget{
		{RuleID: "lens-a", KeyField: "identityKey"},
		{RuleID: "lens-b", KeyField: "identityKey"},
	})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Len(t, nullifierA.calls, 1)
	require.Len(t, nullifierB.calls, 1)
}

// TestHandleKeyShredded_TargetNotRegistered_NaksForRedelivery covers the
// still-starting-up case: a configured target whose lens hasn't registered
// yet is treated as transient (redeliver), not privacy-critical.
func TestHandleKeyShredded_TargetNotRegistered_NaksForRedelivery(t *testing.T) {
	svc := control.NewService() // lens-a never registered
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.NakWithDelay, decision)
	require.Equal(t, uint64(0), m.HandledTotal(), "not-yet-registered must not count as handled")
}

// TestHandleKeyShredded_TargetNeverRegisters_GivesUpAfterMaxDeliveries proves
// a permanently-misconfigured RuleID (a typo'd/decommissioned target) stops
// nak-looping once NumDelivered reaches maxNotRegisteredDeliveries, instead
// of retrying forever.
func TestHandleKeyShredded_TargetNeverRegisters_GivesUpAfterMaxDeliveries(t *testing.T) {
	svc := control.NewService() // lens-a never registered
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}})

	msg := keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA")
	msg.NumDelivered = maxNotRegisteredDeliveries

	decision := m.handleKeyShredded(context.Background(), msg)

	require.Equal(t, substrate.Ack, decision, "must give up (Ack) rather than nak forever once the threshold is reached")
	require.Equal(t, uint64(1), m.HandledTotal())
}

// TestNew_NilControl_Panics proves a misconfigured Manager fails at
// construction (fail fast) rather than mid-stream on the first real event.
func TestNew_NilControl_Panics(t *testing.T) {
	require.Panics(t, func() {
		New(Config{Control: nil})
	})
}

// TestHandleKeyShredded_NullifyFails_RaisesPrivacyCriticalPausesNoRetry is the
// failure-tier proof (vault-crypto-shredding-design.md §6 "a forced
// nullification failure raises the privacy-critical tier — lens halts, no
// retry, alert emitted"): a real Delete failure must pause the affected lens
// and Ack (never retry) rather than Nak.
func TestHandleKeyShredded_NullifyFails_RaisesPrivacyCriticalPausesNoRetry(t *testing.T) {
	svc := control.NewService()
	boom := errors.New("adapter: boom")
	nullifier := &fakeNullifier{err: boom}
	pauser := &fakePauser{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	svc.RegisterPauser("lens-a", pauser)
	m := newTestManager(t, svc, []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision, "a privacy-critical failure must never be retried")
	require.True(t, pauser.paused, "the affected lens must be paused")
	require.Equal(t, uint64(1), m.HandledTotal())
}

// TestHandleKeyShredded_OneTargetFailsAnotherSucceeds_BothAttempted proves a
// privacy-critical failure on one target does not skip the remaining ones.
func TestHandleKeyShredded_OneTargetFailsAnotherSucceeds_BothAttempted(t *testing.T) {
	svc := control.NewService()
	failing := &fakeNullifier{err: errors.New("boom")}
	ok := &fakeNullifier{}
	pauser := &fakePauser{}
	svc.RegisterRowNullifier("lens-fail", failing)
	svc.RegisterPauser("lens-fail", pauser)
	svc.RegisterRowNullifier("lens-ok", ok)
	m := newTestManager(t, svc, []NullifyTarget{
		{RuleID: "lens-fail", KeyField: "identityKey"},
		{RuleID: "lens-ok", KeyField: "identityKey"},
	})

	decision := m.handleKeyShredded(context.Background(), keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.True(t, pauser.paused)
	require.Len(t, failing.calls, 1)
	require.Len(t, ok.calls, 1, "the second target must still be attempted after the first fails")
}

func TestHandleKeyShredded_EmptyBody_Acks(t *testing.T) {
	svc := control.NewService()
	m := newTestManager(t, svc, nil)

	decision := m.handleKeyShredded(context.Background(), substrate.Message{})

	require.Equal(t, substrate.Ack, decision)
}

func TestHandleKeyShredded_UnparseableBody_Terms(t *testing.T) {
	svc := control.NewService()
	m := newTestManager(t, svc, nil)

	decision := m.handleKeyShredded(context.Background(), substrate.Message{Body: []byte("not json")})

	require.Equal(t, substrate.Term, decision)
}

func TestHandleKeyShredded_MissingIdentityKey_Terms(t *testing.T) {
	svc := control.NewService()
	m := newTestManager(t, svc, nil)

	decision := m.handleKeyShredded(context.Background(), substrate.Message{Body: []byte(`{"payload":{}}`)})

	require.Equal(t, substrate.Term, decision)
}

// TestFailurePrivacyCritical_Classify proves the new failure.PrivacyCritical
// tier round-trips through failure.Classify (mirrors the pattern each of the
// other three tiers already covers).
func TestFailurePrivacyCritical_Classify(t *testing.T) {
	err := failure.PrivacyCritical(errors.New("row nullify failed"))
	require.Equal(t, failure.CatPrivacyCritical, failure.Classify(err))
}

// newSubmitTestConn starts an embedded NATS + JetStream with a
// core-operations-shaped stream, for the Fire-4b finalization-submit tests.
// Mirrors internal/privacyworker's harness (jsstore.Dir StoreDir convention).
func newSubmitTestConn(t *testing.T) (*substrate.Conn, context.Context, jetstream.Consumer) {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	stream, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "core-operations", Subjects: []string{"ops.>"},
	})
	require.NoError(t, err)
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{Durable: "ops-observer"})
	require.NoError(t, err)
	return conn, ctx, cons
}

func fetchOneOp(t *testing.T, cons jetstream.Consumer) []byte {
	t.Helper()
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(3*time.Second))
	require.NoError(t, err)
	for m := range msgs.Messages() {
		require.NoError(t, m.Ack())
		return m.Data()
	}
	return nil
}

// TestHandleKeyShredded_CleanPath_SubmitsFinalization proves Fire 4b: with an
// ActorKey configured and every target nullifying cleanly, the listener
// publishes exactly one RecordShredFinalization{projectionsNullified} to
// ops.system before Acking.
func TestHandleKeyShredded_CleanPath_SubmitsFinalization(t *testing.T) {
	conn, ctx, opsCons := newSubmitTestConn(t)
	svc := control.NewService()
	nullifier := &fakeNullifier{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	const actorKey = "vtx.identity.PrivacyActorKMNPQRST"
	m := New(Config{
		Conn: conn, Control: svc, ActorKey: actorKey,
		Targets: []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}},
	})

	decision := m.handleKeyShredded(ctx, keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	data := fetchOneOp(t, opsCons)
	require.NotNil(t, data, "expected a RecordShredFinalization op on ops.system")
	var env struct {
		RequestID     string `json:"requestId"`
		Lane          string `json:"lane"`
		OperationType string `json:"operationType"`
		Actor         string `json:"actor"`
		Payload       struct {
			IdentityKey string `json:"identityKey"`
			Step        string `json:"step"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(data, &env))
	require.Equal(t, "RecordShredFinalization", env.OperationType)
	require.Equal(t, "system", env.Lane)
	require.Equal(t, actorKey, env.Actor)
	require.Equal(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA", env.Payload.IdentityKey)
	require.Equal(t, StepProjectionsNullified, env.Payload.Step)
	require.True(t, substrate.IsValidNanoID(env.RequestID))
}

// TestHandleKeyShredded_PrivacyCritical_SkipsFinalization proves a
// privacy-critical nullification failure still Acks (never retries) but does
// NOT record projectionsNullified — the shredStatus row stays visibly stuck.
func TestHandleKeyShredded_PrivacyCritical_SkipsFinalization(t *testing.T) {
	conn, ctx, opsCons := newSubmitTestConn(t)
	svc := control.NewService()
	nullifier := &fakeNullifier{err: errors.New("injected delete failure")}
	pauser := &fakePauser{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	svc.RegisterPauser("lens-a", pauser)
	m := New(Config{
		Conn: conn, Control: svc, ActorKey: "vtx.identity.PrivacyActorKMNPQRST",
		Targets: []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}},
	})

	decision := m.handleKeyShredded(ctx, keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision, "privacy-critical is Acked, never retried")
	require.True(t, pauser.paused)
	msgs, err := opsCons.Fetch(1, jetstream.FetchMaxWait(1500*time.Millisecond))
	require.NoError(t, err)
	for range msgs.Messages() {
		t.Fatal("no RecordShredFinalization must be published after a privacy-critical failure")
	}
}

// TestHandleKeyShredded_NoActorKey_NoSubmit proves the disabled posture (a
// pre-v15 kernel): the clean path still Acks + counts with no op published.
func TestHandleKeyShredded_NoActorKey_NoSubmit(t *testing.T) {
	conn, ctx, opsCons := newSubmitTestConn(t)
	svc := control.NewService()
	nullifier := &fakeNullifier{}
	svc.RegisterRowNullifier("lens-a", nullifier)
	m := New(Config{
		Conn: conn, Control: svc,
		Targets: []NullifyTarget{{RuleID: "lens-a", KeyField: "identityKey"}},
	})

	decision := m.handleKeyShredded(ctx, keyShreddedMsg(t, "vtx.identity.AAAAAAAAAAAAAAAAAAAA"))

	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, uint64(1), m.HandledTotal())
	msgs, err := opsCons.Fetch(1, jetstream.FetchMaxWait(1500*time.Millisecond))
	require.NoError(t, err)
	for range msgs.Messages() {
		t.Fatal("no op must be published without an ActorKey")
	}
}
