package processor

import (
	"context"
	"log/slog"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// The interfaces in this file are Story 1.5 scaffolding for the commit
// path's downstream steps. Stories 1.6, 1.7, 1.8 will provide real
// implementations behind these same interface boundaries. The point of
// defining them now is to lock the wiring so future stories swap
// implementations without disturbing the commit_path driver.

// Hydrator (step 4) — JIT Hydration. Real implementation in step4_hydrate.go.
type Hydrator interface {
	Hydrate(ctx context.Context, env *OperationEnvelope) (HydratedState, error)
}

// Executor (step 5) — Starlark execution. Real implementation in step5_execute.go.
type Executor interface {
	Execute(ctx context.Context, env *OperationEnvelope, state HydratedState) (ScriptResult, error)
}

// Validator (step 6+7) — DDL JSON Schema validation, write-scope check,
// sensitivity rule, event-schema validation. Stories 1.7 + 1.9.
type Validator interface {
	Validate(ctx context.Context, env *OperationEnvelope, result ScriptResult) error
}

// Committer (step 8) — assembles the atomic batch (tracker + real
// mutations) and publishes it. Story 1.7.
type Committer interface {
	Commit(ctx context.Context, env *OperationEnvelope, result ScriptResult, tracker Tracker) (CommitAck, error)
}

// CommitAck mirrors substrate.BatchAck for the commit path.
type CommitAck struct {
	Stream   string
	Sequence uint64
	BatchID  string
	Count    uint64
}

// EventPublisher (step 9) — fans events out to `core-events`. Story 1.8.
type EventPublisher interface {
	Publish(ctx context.Context, env *OperationEnvelope, result ScriptResult) error
}

// Acker (step 10) — acks the JetStream message. Story 1.8 may keep this
// as a thin shim; the interface is here so fault-injection tests can
// substitute crashy implementations.
type Acker interface {
	Ack(ctx context.Context) error
}

// --- Stub implementations ---
//
// Each stub logs "step N: stubbed" and returns a success-shaped value.
// `commit_path.go` wires these into a working-but-incomplete pipeline so
// Story 1.5 can exercise the JetStream consume → tracker write → ack path
// end-to-end while leaving real logic for the future stories.

// StubHydrator and StubExecutor were removed in Story 1.6 — real
// implementations live in step4_hydrate.go and step5_execute.go.

type StubValidator struct{ logger *slog.Logger }

func (s *StubValidator) Validate(_ context.Context, env *OperationEnvelope, _ ScriptResult) error {
	s.logger.Info("step 6+7: stubbed", "step", "validate", "requestId", env.RequestID)
	return nil
}

// StubCommitter performs the Story-1.5 single-message atomic batch: the
// tracker write only. Story 1.7 will replace this with the full batch
// (tracker + real mutations). The interface stays the same.
type StubCommitter struct {
	conn   *substrate.Conn
	bucket string
	logger *slog.Logger
	clock  func() time.Time
}

// NewStubCommitter wires the substrate conn used for the tracker write.
func NewStubCommitter(conn *substrate.Conn, bucket string, logger *slog.Logger, clock func() time.Time) *StubCommitter {
	if clock == nil {
		clock = time.Now
	}
	return &StubCommitter{conn: conn, bucket: bucket, logger: logger, clock: clock}
}

// Commit writes the tracker via substrate.AtomicBatch with a single-op
// payload. CreateOnly + 24h TTL. Returns the BatchAck.
func (s *StubCommitter) Commit(_ context.Context, env *OperationEnvelope, _ ScriptResult, tracker Tracker) (CommitAck, error) {
	s.logger.Info("step 8: stubbed (tracker-only atomic batch)",
		"step", "commit", "requestId", env.RequestID, "trackerKey", tracker.Key)
	val, err := tracker.Marshal()
	if err != nil {
		return CommitAck{}, err
	}
	ops := []substrate.BatchOp{{
		Bucket:     s.bucket,
		Key:        tracker.Key,
		Value:      val,
		CreateOnly: true,
		TTL:        TrackerTTL,
	}}
	ack, err := s.conn.AtomicBatch(ops, 5*time.Second)
	if err != nil {
		return CommitAck{}, err
	}
	return CommitAck{
		Stream:   ack.Stream,
		Sequence: ack.Sequence,
		BatchID:  ack.BatchID,
		Count:    ack.Count,
	}, nil
}

type StubEventPublisher struct{ logger *slog.Logger }

func (s *StubEventPublisher) Publish(_ context.Context, env *OperationEnvelope, _ ScriptResult) error {
	s.logger.Info("step 9: stubbed", "step", "events", "requestId", env.RequestID)
	return nil
}
