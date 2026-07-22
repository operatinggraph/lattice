//go:build !js

// Package natstransport implements the Edge engine's transport seam
// (internal/edge/transport) over a live NATS connection: the durable
// JetStream delta feed and core-NATS control request-reply the trusted Go
// hosts (cmd/edge, cmd/facet) run on.
//
// It is deliberately a package of its own rather than a file beside the
// interfaces: importing it links a NATS client, and the browser-hosted engine
// (edge-browser-node-design.md §3.2) imports the interfaces only, so the
// semantics packages must be able to reach the seam without reaching this.
// The js/wasm exclusion makes that separation the compiler's business rather
// than a convention — nats.go builds for js/wasm, so nothing else would stop
// a browser build from dialling NATS directly and bypassing the Gateway.
package natstransport

import (
	"context"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/edge/transport"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Conn adapts a live substrate connection to both halves of the seam.
type Conn struct {
	conn *substrate.Conn
}

// New builds the NATS-backed transport over conn.
func New(conn *substrate.Conn) *Conn { return &Conn{conn: conn} }

var (
	_ transport.DeltaSource   = (*Conn)(nil)
	_ transport.ControlClient = (*Conn)(nil)
)

// RunDurableConsumer runs cfg's durable JetStream consumer, translating each
// delivered message into a Delta and the handler's verdict back into a
// substrate decision.
func (s *Conn) RunDurableConsumer(ctx context.Context, cfg transport.ConsumerConfig, h transport.Handler) error {
	return s.conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
		Stream:        cfg.Stream,
		FilterSubject: cfg.FilterSubject,
		Durable:       cfg.Durable,
		Logger:        cfg.Logger,
	}, func(ctx context.Context, msg substrate.Message) substrate.Decision {
		switch h(ctx, transport.Delta{Subject: msg.Subject, Body: msg.Body, Sequence: msg.Sequence}) {
		case transport.Ack:
			return substrate.Ack
		case transport.Term:
			return substrate.Term
		case transport.Nak:
			return substrate.Nak
		default:
			// Redeliver rather than advance past a verdict this adapter does
			// not recognise: a decision added to the seam later must fail
			// safe here until it is mapped deliberately.
			return substrate.Nak
		}
	})
}

// Request issues one core-NATS request-reply (Refractor control planes are
// NATS-Services micro-services over core NATS, not JetStream), stamping actor
// as the Lattice-Actor header when non-empty.
func (s *Conn) Request(ctx context.Context, subject string, data []byte, actor string) ([]byte, error) {
	msg := &nats.Msg{Subject: subject, Data: data}
	if actor != "" {
		msg.Header = nats.Header{}
		msg.Header.Set(controlauth.HeaderActor, actor)
	}
	reply, err := s.conn.NATS().RequestMsgWithContext(ctx, msg)
	if err != nil {
		return nil, err
	}
	return reply.Data, nil
}
