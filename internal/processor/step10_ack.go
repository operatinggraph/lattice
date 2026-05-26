package processor

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go/jetstream"
)

// AckerImpl is the step-10 implementation. It wraps the original
// jetstream.Msg and calls Ack(ctx) on it. The explicit Acker boundary
// makes step 10 testable for fault injection (NFR-R1): an AckerImpl
// wrapped by FailAfterN can simulate a crash exactly at step 10.
//
// Lifecycle: a new AckerImpl is constructed per delivered message —
// step 1's parseEnvelopeFromMessage path hands the msg to a fresh
// MessageAcker; the commit_path invokes step 10 only after step 9
// returns nil.
type AckerImpl struct {
	Msg    jetstream.Msg
	Logger *slog.Logger
}

// NewAcker constructs a per-message Acker.
func NewAcker(msg jetstream.Msg, logger *slog.Logger) *AckerImpl {
	if logger == nil {
		logger = slog.Default()
	}
	return &AckerImpl{Msg: msg, Logger: logger}
}

// Ack implements Acker (step 10). It calls jetstream.Msg.Ack and surfaces ack
// errors. If ack fails, the durable consumer redelivers — step-2 tracker dedup
// short-circuits safely (Contract #4 §4.5).
func (a *AckerImpl) Ack(ctx context.Context) error {
	_ = ctx // jetstream.Msg.Ack does not take a ctx in the v1.x API
	if a.Msg == nil {
		return fmt.Errorf("step 10: ack called with nil msg")
	}
	if err := a.Msg.Ack(); err != nil {
		return fmt.Errorf("step 10: ack: %w", err)
	}
	return nil
}
