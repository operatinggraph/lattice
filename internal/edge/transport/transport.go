// Package transport is the Edge engine's host-coupled transport seam
// (edge-browser-node-design.md §3.2): the narrow interface pair the semantics
// packages (internal/edge/{sync,vault}) depend on instead of a concrete NATS
// connection, plus the substrate-backed implementation the trusted Go hosts
// (cmd/edge, cmd/facet) wire in.
//
// The seam carries only plain types — a payload, a stream sequence, a subject
// string — so an implementation needs nothing from internal/substrate. That is
// the point: a browser host supplies the delta feed and the control RPCs from
// a JS NATS client over WebSocket, while the semantics they feed (last-writer-
// wins-by-revision, cursor/gap detection, hydrate) stay single-sourced in Go.
package transport

import (
	"context"
	"log/slog"
)

// Delta is one message delivered by a DeltaSource: the raw envelope payload
// plus the stream sequence that ordered it. Sequence is what the Sync Manager
// persists as its cursor, so a resuming consumer can detect a retention gap.
type Delta struct {
	Subject  string
	Body     []byte
	Sequence uint64
}

// Decision is a handler's verdict on one delivered Delta, mirroring the
// three dispositions any at-least-once delivery needs.
type Decision int

const (
	// Ack marks the delta handled; the consumer advances past it.
	Ack Decision = iota
	// Nak requests redelivery — the failure is transient (a failed local
	// write), so the same payload may well succeed next time.
	Nak
	// Term drops the delta permanently — the payload itself is the problem
	// (a malformed envelope), so redelivery would only hot-loop.
	Term
)

// Handler processes one delivered Delta. It must be idempotent: an
// at-least-once source can redeliver a delta the handler already applied.
type Handler func(ctx context.Context, d Delta) Decision

// ConsumerConfig names the durable delta feed a DeltaSource should run.
type ConsumerConfig struct {
	Stream        string
	Durable       string
	FilterSubject string
	Logger        *slog.Logger
}

// DeltaSource is the inbound half of the seam: a durable, resumable feed of
// projection deltas, plus the stream's earliest retained sequence so a
// resuming consumer can tell whether retention pruned messages it never saw.
type DeltaSource interface {
	// RunDurableConsumer delivers cfg's stream/filter to h until ctx is
	// cancelled, resuming from the durable's own ack floor across restarts.
	RunDurableConsumer(ctx context.Context, cfg ConsumerConfig, h Handler) error
	// FirstSequence returns stream's earliest still-retained sequence. A
	// cursor below it means retention pruned deltas this node never applied.
	FirstSequence(ctx context.Context, stream string) (uint64, error)
}

// ControlClient is the outbound half of the seam: request-reply against a
// control-plane subject, carrying the actor identity the control plane
// authorizes against. An empty actor sends no actor header, matching the
// control plane's self-asserted-actor default.
type ControlClient interface {
	Request(ctx context.Context, subject string, data []byte, actor string) ([]byte, error)
}
