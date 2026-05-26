package substrate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
)

// BatchOp describes a single write inside an atomic batch. Callers
// construct one BatchOp per Core KV mutation and pass the slice to
// AtomicBatch. The helper drives the raw NATS batch headers internally —
// callers never touch Nats-Batch-* directly.
//
// Op semantics:
//
//   - Create-if-absent: leave HasRevision false AND set Revision to 0 by
//     setting CreateOnly true. (Both forms are equivalent at the wire — a
//     revision condition of 0 means "key must not exist". CreateOnly is
//     provided as a more readable spelling for the common create-tracker
//     pattern.)
//
//   - Revision-conditioned update: set HasRevision true and Revision to
//     the expected current revision.
//
//   - Per-key TTL (used for op trackers per Contract #4 §4.3): set TTL to
//     a non-zero duration.
//
//   - Unconditional put: leave CreateOnly false, HasRevision false,
//     and Revision 0. (Note: at most one batch member can be unconditioned
//     against a given key; in practice the Processor always uses Create
//     or Update.)
type BatchOp struct {
	Bucket       string
	Key          string
	Value        []byte
	CreateOnly   bool
	HasRevision  bool
	Revision     uint64
	TTL          time.Duration
}

// BatchAck describes the server's atomic-commit acknowledgement for a
// successful AtomicBatch. Stream + Sequence identify the last message
// (the commit message); BatchID echoes the unique batch identifier
// substrate assigned; Count is the total messages in the batch.
type BatchAck struct {
	Stream   string
	Sequence uint64
	BatchID  string
	Count    uint64
}

// AtomicBatch publishes ops as a single NATS JetStream atomic batch.
// Either every op is durably committed or none are. On failure the
// returned error wraps ErrAtomicBatchRejected.
//
// The atomic batch is implemented over the raw NATS protocol because the
// nats.go client does not expose a high-level PublishBatch API. This helper
// hides those mechanics from callers.
//
// Requirements:
//
//   - Every op's bucket must have AllowAtomicPublish enabled on its
//     underlying KV_<bucket> stream (Core KV is provisioned this way by
//     the bootstrap path).
//
//   - All ops in a single AtomicBatch call must target the SAME bucket.
//     Cross-bucket atomicity is not supported by NATS atomic batch;
//     pass one bucket per call.
//
//   - timeout bounds the round trip on the commit message. Choose a value
//     consistent with the operation's lane SLA (Processor uses 5s by
//     default).
func (c *Conn) AtomicBatch(ops []BatchOp, timeout time.Duration) (*BatchAck, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("substrate: AtomicBatch: empty op list")
	}

	bucket := ops[0].Bucket
	for i, op := range ops {
		if op.Bucket != bucket {
			return nil, fmt.Errorf(
				"substrate: AtomicBatch: cross-bucket batch not supported (op[0]=%q op[%d]=%q)",
				bucket, i, op.Bucket)
		}
		if op.Key == "" {
			return nil, fmt.Errorf("substrate: AtomicBatch: op[%d] missing key", i)
		}
	}

	batchID, err := NewNanoID()
	if err != nil {
		return nil, fmt.Errorf("substrate: AtomicBatch: generate batch id: %w", err)
	}

	msgs := make([]*nats.Msg, len(ops))
	for i, op := range ops {
		m := nats.NewMsg(kvBucketSubject(op.Bucket, op.Key))
		m.Data = op.Value
		m.Header = nats.Header{}
		if op.CreateOnly {
			m.Header.Set("Nats-Expected-Last-Subject-Sequence", "0")
		} else if op.HasRevision {
			m.Header.Set("Nats-Expected-Last-Subject-Sequence",
				strconv.FormatUint(op.Revision, 10))
		}
		if op.TTL > 0 {
			m.Header.Set("Nats-TTL", op.TTL.String())
		}
		msgs[i] = m
	}

	ack, err := publishAtomicBatch(c.nc, batchID, msgs, timeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAtomicBatchRejected, err)
	}
	if ack.Error != nil {
		return nil, fmt.Errorf("%w: code=%d err_code=%d: %s",
			ErrAtomicBatchRejected, ack.Error.Code, ack.Error.ErrCode, ack.Error.Description)
	}
	return &BatchAck{
		Stream:   ack.Stream,
		Sequence: ack.Sequence,
		BatchID:  batchID,
		Count:    ack.BatchSize,
	}, nil
}

// kvBucketSubject returns the JetStream publish subject for a Core KV key.
// KV publish subjects follow the pattern: $KV.<bucket>.<key>
func kvBucketSubject(bucket, key string) string {
	return "$KV." + bucket + "." + key
}

// pubAckResponse mirrors the NATS PubAck JSON envelope returned by the
// server in response to the commit message.
type pubAckResponse struct {
	Stream    string  `json:"stream"`
	Sequence  uint64  `json:"seq"`
	Duplicate bool    `json:"duplicate"`
	BatchID   string  `json:"batch,omitempty"`
	BatchSize uint64  `json:"count,omitempty"`
	Error     *apiErr `json:"error,omitempty"`
}

type apiErr struct {
	Code        int    `json:"code"`
	ErrCode     uint16 `json:"err_code"`
	Description string `json:"description"`
}

// PublishOp describes a single message inside a non-conditional batch
// publish to JetStream. Unlike BatchOp, PublishOp
// targets arbitrary JetStream subjects (e.g. `events.identity.created`)
// rather than KV-bucket subjects, and it does not carry revision
// conditions — the batch is unconditional. Ordering within the batch is
// preserved by `Nats-Batch-Sequence` (1..N), and either the entire
// batch lands or none of it does.
//
// Note: the destination subjects must all belong to the SAME JetStream
// stream (the atomic-batch primitive is stream-scoped). For the
// Processor's event publish, all subjects share the `events.>` filter
// on the `core-events` stream.
type PublishOp struct {
	Subject string
	Data    []byte
	Header  map[string]string // optional extra headers
}

// PublishBatchAck mirrors BatchAck for a non-conditional batch publish.
type PublishBatchAck struct {
	Stream   string
	Sequence uint64
	BatchID  string
	Count    uint64
}

// PublishBatch publishes ops as a single JetStream atomic batch to
// arbitrary subjects (no revision conditions, no per-key TTL). All
// subjects must belong to the same JetStream stream — typically the
// `core-events` stream's `events.>` filter for the Processor's step 9.
//
// Order is preserved via `Nats-Batch-Sequence` (1..N). On failure, no
// message is durably stored — semantics are all-or-nothing.
func (c *Conn) PublishBatch(ops []PublishOp, timeout time.Duration) (*PublishBatchAck, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("substrate: PublishBatch: empty op list")
	}
	for i, op := range ops {
		if op.Subject == "" {
			return nil, fmt.Errorf("substrate: PublishBatch: op[%d] missing subject", i)
		}
	}

	batchID, err := NewNanoID()
	if err != nil {
		return nil, fmt.Errorf("substrate: PublishBatch: generate batch id: %w", err)
	}

	msgs := make([]*nats.Msg, len(ops))
	for i, op := range ops {
		m := nats.NewMsg(op.Subject)
		m.Data = op.Data
		m.Header = nats.Header{}
		for k, v := range op.Header {
			m.Header.Set(k, v)
		}
		msgs[i] = m
	}

	ack, err := publishAtomicBatch(c.nc, batchID, msgs, timeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAtomicBatchRejected, err)
	}
	if ack.Error != nil {
		return nil, fmt.Errorf("%w: code=%d err_code=%d: %s",
			ErrAtomicBatchRejected, ack.Error.Code, ack.Error.ErrCode, ack.Error.Description)
	}
	return &PublishBatchAck{
		Stream:   ack.Stream,
		Sequence: ack.Sequence,
		BatchID:  batchID,
		Count:    ack.BatchSize,
	}, nil
}

// publishAtomicBatch is the raw-protocol atomic-batch publisher.
// All-but-last messages are fire-and-forget; the last carries
// Nats-Batch-Commit and is sent via RequestMsg so the server's commit ack
// can be parsed.
func publishAtomicBatch(nc *nats.Conn, batchID string, messages []*nats.Msg, timeout time.Duration) (*pubAckResponse, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("empty batch")
	}
	for i, m := range messages {
		if m.Header == nil {
			m.Header = nats.Header{}
		}
		seq := uint64(i + 1)
		m.Header.Set("Nats-Batch-Id", batchID)
		m.Header.Set("Nats-Batch-Sequence", strconv.FormatUint(seq, 10))

		if i < len(messages)-1 {
			if err := nc.PublishMsg(m); err != nil {
				return nil, fmt.Errorf("publish msg %d: %w", seq, err)
			}
			continue
		}
		// Last message — commit and wait for ack.
		m.Header.Set("Nats-Batch-Commit", "1")
		resp, err := nc.RequestMsg(m, timeout)
		if err != nil {
			return nil, fmt.Errorf("request commit msg: %w", err)
		}
		var ack pubAckResponse
		if err := json.Unmarshal(resp.Data, &ack); err != nil {
			return nil, fmt.Errorf("unmarshal ack: %w (raw: %s)", err, string(resp.Data))
		}
		return &ack, nil
	}
	panic("substrate: publishAtomicBatch: unreachable")
}
