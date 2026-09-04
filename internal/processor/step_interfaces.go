package processor

import (
	"context"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Interface definitions for the commit path's downstream steps. Real
// implementations live in the corresponding step*.go files. The interfaces
// lock the wiring so implementations can change without disturbing the
// commit_path driver.

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
//
// prior carries the documents stored at the mutation keys (step 5.5's
// Committer.ReadPrior), because the DDL governing an update or a tombstone is
// the one of the class stored at the key, not only the one the script's own
// document declares.
type Validator interface {
	Validate(ctx context.Context, env *OperationEnvelope, result ScriptResult, state HydratedState, prior PriorDocs) error
}

// Committer (step 8) — reads the stored documents the write path needs
// (step 5.5) and assembles + commits the atomic batch (tracker + mutations)
// to Core KV.
//
// ReadPrior runs before step 6 so the validator and the commit share one pass;
// Commit takes that map back and tops it up for any mutation appended after
// validation. An error from ReadPrior is a read fault — the caller redelivers.
type Committer interface {
	ReadPrior(ctx context.Context, mutations []MutationOp) (PriorDocs, error)
	Commit(ctx context.Context, env *OperationEnvelope, result ScriptResult, tracker Tracker, prior PriorDocs) (CommitAck, error)
}

// CommitAck mirrors substrate.BatchAck for the commit path. Events carries
// the EventList built during step 8 so the outbox consumer can publish the
// exact same event IDs that were recorded in the tracker — BuildEventList is
// called exactly once per operation.
type CommitAck struct {
	Stream   string
	Sequence uint64
	BatchID  string
	Count    uint64
	Events   EventList
	// Revisions maps each business mutation key to the Core KV revision it
	// committed at, filtered to exclude the idempotency tracker key. Nil
	// when the substrate could not derive per-key revisions.
	Revisions map[string]uint64
}

// Acker (step 9) — acks the JetStream message. AckerImpl is the production
// implementation (step9_ack.go); fault-injection tests substitute a
// FailAfterN-wrapped implementation.
type Acker interface {
	Ack(ctx context.Context) error
}

// AckerFactory builds a per-message Acker. The commit_path constructs
// one Acker per delivered jetstream.Msg so the Acker holds the msg
// reference internally (Architecture Decision #4). The factory pattern
// is the seam tests use to inject a FailAfterN wrapper around the real
// AckerImpl.
type AckerFactory func(msg jetstream.Msg, logger *slog.Logger) Acker

// DefaultAckerFactory returns a real AckerImpl. Tests inject a wrapper.
func DefaultAckerFactory(msg jetstream.Msg, logger *slog.Logger) Acker {
	return NewAcker(msg, logger)
}

// --- Stub implementations ---
//
// Each stub logs "step N: stubbed" and returns a success-shaped value. Used
// in test pipelines where the step under test is isolated and the remaining
// steps should be no-ops.
//
// StubHydrator and StubExecutor have been removed — real implementations live
// in step4_hydrate.go and step5_execute.go.

type StubValidator struct{ logger *slog.Logger }

func (s *StubValidator) Validate(_ context.Context, env *OperationEnvelope, _ ScriptResult, _ HydratedState, _ PriorDocs) error {
	s.logger.Info("step 6+7: stubbed", "step", "validate", "requestId", env.RequestID)
	return nil
}

// StubCommitter performs a tracker-only atomic batch (no business mutations).
// Used in tests that exercise step-2/3 without needing real mutations.
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

// ReadPrior reads nothing: the stub writes no business mutation, so there is no
// stored document for the gate or the batch builder to consult.
func (s *StubCommitter) ReadPrior(_ context.Context, _ []MutationOp) (PriorDocs, error) {
	return PriorDocs{}, nil
}

// Commit writes the tracker via substrate.AtomicBatch with a single-op
// payload. CreateOnly + 24h TTL. Returns the BatchAck.
func (s *StubCommitter) Commit(ctx context.Context, env *OperationEnvelope, _ ScriptResult, tracker Tracker, _ PriorDocs) (CommitAck, error) {
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
	bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ack, err := s.conn.AtomicBatch(bctx, ops)
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
