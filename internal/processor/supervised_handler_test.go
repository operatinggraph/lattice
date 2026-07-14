package processor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// messageFromEnvelope marshals env into the substrate.Message body dispatch
// consumes (mirrors publishEnvelope's wire shape).
func messageFromEnvelope(t *testing.T, env *OperationEnvelope) substrate.Message {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return substrate.Message{Subject: "ops." + string(env.Lane), Body: b}
}

// errCommitter fails every Commit with a generic (non-conflict, non-protected)
// error, modelling a transient KV/infra failure on the commit step.
type errCommitter struct{}

func (errCommitter) Commit(context.Context, *OperationEnvelope, ScriptResult, Tracker) (CommitAck, error) {
	return CommitAck{}, errors.New("simulated commit infra failure")
}

// denyAuthorizer denies every operation, modelling a step-3 authorization denial.
type denyAuthorizer struct{}

func (denyAuthorizer) Authorize(context.Context, *OperationEnvelope) (Decision, error) {
	return Decision{Authorized: false, Code: ErrCodeAuthDenied, Reason: "denied for test"}, nil
}

// TestDispatch_DecisionMapping pins the commit-path outcome → substrate.Decision
// mapping that the supervisor (production) and the jetstream adapter both apply:
// success/duplicate → Ack, a permanent rejection → Term, a transient commit
// failure → NakWithDelay (bounded redelivery floor, never a hot-loop).
func TestDispatch_DecisionMapping(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, _, _ := setupTestPipeline(t)
	logger := testLogger()

	t.Run("accepted → Ack", func(t *testing.T) {
		env := newTestEnvelope(testNanoID1)
		out, dec := cp.dispatch(ctx, messageFromEnvelope(t, env))
		if out != OutcomeAccepted || dec != substrate.Ack {
			t.Fatalf("got (%q, %v), want (accepted, Ack)", out, dec)
		}
	})

	t.Run("duplicate → Ack", func(t *testing.T) {
		dupID, err := substrate.NewNanoID()
		if err != nil {
			t.Fatalf("nanoid: %v", err)
		}
		env := newTestEnvelope(dupID)
		tr := NewTracker(env, time.Now())
		val, _ := tr.Marshal()
		if _, err := conn.KVCreate(ctx, testCoreBucket, tr.Key, val); err != nil {
			t.Fatalf("seed tracker: %v", err)
		}
		out, dec := cp.dispatch(ctx, messageFromEnvelope(t, env))
		if out != OutcomeDuplicate || dec != substrate.Ack {
			t.Fatalf("got (%q, %v), want (duplicate, Ack)", out, dec)
		}
	})

	t.Run("malformed → Term", func(t *testing.T) {
		out, dec := cp.dispatch(ctx, substrate.Message{Body: []byte(`{"lane":"banana"}`)})
		if out != OutcomeMalformed || dec != substrate.Term {
			t.Fatalf("got (%q, %v), want (malformed, Term)", out, dec)
		}
	})

	t.Run("empty body → Term", func(t *testing.T) {
		out, dec := cp.dispatch(ctx, substrate.Message{Body: nil})
		if out != OutcomeMalformed || dec != substrate.Term {
			t.Fatalf("got (%q, %v), want (malformed, Term)", out, dec)
		}
	})

	t.Run("auth denied → Term", func(t *testing.T) {
		denyID, err := substrate.NewNanoID()
		if err != nil {
			t.Fatalf("nanoid: %v", err)
		}
		cpDeny := NewCommitPath(Deps{
			Conn:       conn,
			CoreBucket: testCoreBucket,
			HealthKV:   testHealthBucket,
			Authorizer: denyAuthorizer{},
			Committer:  errCommitter{}, // never reached: denial returns before commit
			Metrics:    &Metrics{},
			Logger:     logger,
		})
		env := newTestEnvelope(denyID)
		out, dec := cpDeny.dispatch(ctx, messageFromEnvelope(t, env))
		if out != OutcomeRejected || dec != substrate.Term {
			t.Fatalf("got (%q, %v), want (rejected, Term)", out, dec)
		}
	})

	t.Run("transient commit failure → NakWithDelay", func(t *testing.T) {
		failID, err := substrate.NewNanoID()
		if err != nil {
			t.Fatalf("nanoid: %v", err)
		}
		// A minimal pipeline (nil hydrator/executor/validator) whose committer
		// always fails with a generic error reaches the genuine-commit-failure
		// branch — neither a protected-key nor a conflict — which must redeliver
		// on the backoff floor.
		cpFail := NewCommitPath(Deps{
			Conn:       conn,
			CoreBucket: testCoreBucket,
			HealthKV:   testHealthBucket,
			Authorizer: NewStubAuthorizer(logger),
			Committer:  errCommitter{},
			Metrics:    &Metrics{},
			Logger:     logger,
		})
		env := newTestEnvelope(failID)
		out, dec := cpFail.dispatch(ctx, messageFromEnvelope(t, env))
		if out != OutcomeRetryable || dec != substrate.NakWithDelay {
			t.Fatalf("got (%q, %v), want (retryable, NakWithDelay)", out, dec)
		}
	})
}

// TestSupervisedHandler_CommitsThroughSupervisor proves the production wiring:
// the commit path driven by a substrate ConsumerSupervisor (via SupervisedHandler)
// consumes a published operation, commits it, and the supervisor's
// PendingForConsumer — the same reader the heartbeat's lane_lag uses — drains to
// zero. This exercises the real delivery path the in-process adapter tests do not.
func TestSupervisedHandler_CommitsThroughSupervisor(t *testing.T) {
	t.Parallel()
	ctx, conn, cp, _, metrics := setupTestPipeline(t)

	sup := substrate.NewConsumerSupervisor(conn)
	t.Cleanup(sup.Stop)
	const durable = "processor-supervised-test"
	spec := substrate.ConsumerSpec{
		Name:           durable,
		Stream:         testStream,
		FilterSubjects: []string{"ops.default"},
		DeliverPolicy:  substrate.DeliverAll,
		AckWait:        30 * time.Second,
		Handler:        cp.SupervisedHandler(),
		Logger:         testLogger(),
	}
	if err := sup.Add(ctx, spec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	env := newTestEnvelope(testNanoID1)
	publishEnvelope(t, conn, env)

	deadline := time.After(15 * time.Second)
	for metrics.OpsCommitted.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("operation not committed through the supervised path")
		case <-time.After(50 * time.Millisecond):
		}
	}

	entry, err := conn.KVGet(ctx, testCoreBucket, TrackerKey(env.RequestID))
	if err != nil {
		t.Fatalf("tracker not present after supervised commit: %v", err)
	}
	tr, err := ParseTracker(entry.Value)
	if err != nil {
		t.Fatalf("ParseTracker: %v", err)
	}
	if tr.IsDeleted {
		t.Fatalf("tracker should not be tombstoned")
	}

	// The backlog reader the heartbeat uses must show the lane drained.
	drained := time.After(10 * time.Second)
	for {
		n, err := sup.PendingForConsumer(ctx, durable)
		if err != nil {
			t.Fatalf("PendingForConsumer: %v", err)
		}
		if n == 0 {
			break
		}
		select {
		case <-drained:
			t.Fatalf("supervised consumer backlog did not drain: pending=%d", n)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
