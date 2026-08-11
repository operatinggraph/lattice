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
// persists as its cursor — the value a resuming consumer detects a retention
// gap against, and the value it resumes delivery from (ConsumerConfig.StartSeq).
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
	// StartSeq is the stream sequence delivery must begin at: the position
	// the node has already accounted for, plus one
	// (edge-cold-signin-delivery-position-design.md §3.4). A hydrating node
	// names the sequence its hydration snapshot was taken at, because the
	// burst it just received already carries the effect of everything below
	// that point; a warm node names its own persisted cursor. The zero value
	// asserts no position and delivers the subject's whole retained history.
	//
	// Repositioning an existing durable is destructive — see DeltaSource.
	StartSeq uint64
	Logger   *slog.Logger
}

// DeltaSource is the inbound half of the seam: a durable, resumable feed of
// projection deltas. Retention-gap detection is NOT here — an Edge connection
// speaks no $JS.API.STREAM.* verb (its per-identity grant denies it), so the
// earliest-retained sequence is compared to the cursor server-side by the
// personal.syncgap control RPC over the ControlClient half instead
// (edge-syncgap-control-rpc-design.md §3.3).
type DeltaSource interface {
	// RunDurableConsumer delivers cfg's stream/filter to h until ctx is
	// cancelled, starting at cfg.StartSeq — or, when that is zero, at the
	// beginning of the durable's retained history.
	//
	// SHARP EDGE: an implementation honoring StartSeq RECREATES the durable on
	// EVERY attach, not only when a position is named, because a server
	// refuses to move an existing consumer's delivery position in either
	// direction — an already-positioned durable cannot be re-created
	// unpositioned either, so a conditional delete would leave a node that
	// resolves to zero unable to attach at all. **The caller must own the
	// durable**: any other reader on the same durable name loses its consumer
	// mid-flight, along with its ack floor. The Sync Manager satisfies this
	// because the durable name is per (identity, device) and one node holds it
	// at a time.
	//
	// NOT universally honored. The browser host's implementation
	// (internal/edge/browser/jstransport.go) drops StartSeq and attaches at the
	// position its durable already holds. That over-delivers rather than
	// skipping, so a browser node stays correct — it just still walks the
	// retained history a positioned node steps over. A caller must not read a
	// non-zero StartSeq as a guarantee about what will arrive.
	RunDurableConsumer(ctx context.Context, cfg ConsumerConfig, h Handler) error
}

// ControlClient is the outbound half of the seam: request-reply against a
// control-plane subject, carrying the actor identity the control plane
// authorizes against. An empty actor sends no actor header, matching the
// control plane's self-asserted-actor default.
type ControlClient interface {
	Request(ctx context.Context, subject string, data []byte, actor string) ([]byte, error)
}
