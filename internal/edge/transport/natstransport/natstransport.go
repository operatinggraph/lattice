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
	"fmt"

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
//
// Every attach DELETES the named durable before creating it, whatever
// cfg.StartSeq says. That is forced by the server, not chosen here: JetStream
// rejects a changed DeliverPolicy or OptStartSeq on an existing consumer
// (nats-server 2.14 server/consumer.go:2435,:2438), so a durable created at one
// position can never be created at another — including back to the unpositioned
// DeliverAll form a zero StartSeq asks for. Deleting only when a position is
// named would leave a node that resolves to zero unable to attach AT ALL, which
// is a far worse failure than the replay a fresh DeliverAll consumer costs. A
// zero StartSeq means "this node asserts no position", and taking the subject's
// whole retained history is the honest reading of that.
//
// **The caller must own the durable** — any other reader attached to the same
// durable name has its consumer removed underneath it, losing that consumer's
// in-flight deliveries and ack floor. The Sync Manager satisfies this: the
// durable name is per (identity, device) and one node holds it at a time. It is
// also why that Manager takes its resume position from its own persisted cursor
// rather than from the ack floor this delete discards.
//
// Ownership here is a design convention, not a server-enforced one at this
// granularity: the per-connection grant scopes DELETE to one identity's own
// durable family, but the device segment inside that family is the
// CONNECT-time client name the caller supplies (internal/gateway/natsauth
// PermissionsFor, sourced from req.ClientInformation.Name), not a value the
// server attributes to any specific already-running node. A second connection
// authenticated as the same identity can name any device segment and delete
// that durable out from under whichever node holds it. A node whose durable
// is deleted this way does not recover on its own: runDurableLoop
// (internal/substrate/consumer.go) reopens the message ITERATOR on error, not
// the consumer, so it spins at durableReconnect backoff against a name that
// no longer exists until the process restarts and calls RunDurableConsumer
// again.
func (s *Conn) RunDurableConsumer(ctx context.Context, cfg transport.ConsumerConfig, h transport.Handler) error {
	if err := s.conn.DeleteStreamConsumer(ctx, cfg.Stream, cfg.Durable); err != nil {
		return fmt.Errorf("natstransport: reposition durable %q on %q to sequence %d: %w",
			cfg.Durable, cfg.Stream, cfg.StartSeq, err)
	}
	return s.conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
		Stream:        cfg.Stream,
		FilterSubject: cfg.FilterSubject,
		Durable:       cfg.Durable,
		StartSeq:      cfg.StartSeq,
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
