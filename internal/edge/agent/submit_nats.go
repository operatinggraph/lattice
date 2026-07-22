//go:build !js

// NATSSubmitter is excluded from the js/wasm build: it is the trusted,
// pre-Gateway submit path, and a browser host is untrusted by construction, so
// the browser build must not be able to reach it even by mistake. Browser
// hosts submit through GatewaySubmitter, which carries a bearer credential the
// Gateway verifies.

package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/processor/opwire"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// replyInboxHeader carries the reply inbox subject through JetStream
// delivery (mirrors cmd/lattice/output.SubmitOp; reproduced rather than
// imported so this internal package does not depend on a cmd/ package —
// the same rationale as internal/pkgmgr.Installer.submitOp).
const replyInboxHeader = "Lattice-Reply-Inbox"

// NATSSubmitter submits directly to core-operations via JetStream and waits
// for the Processor's reply on a NATS core inbox (mirrors cmd/lattice/
// output.SubmitOp). Trusted posture — pre-Gateway, single identity
// (EDGE.1/2); no bearer credential is verified anywhere on this path, so it
// must never carry an untrusted actor's traffic.
type NATSSubmitter struct {
	Conn *substrate.Conn
}

// Submit implements Submitter.
func (s *NATSSubmitter) Submit(ctx context.Context, env *opwire.OperationEnvelope) (*opwire.OperationReply, error) {
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	inbox := nats.NewInbox()
	sub, err := s.Conn.NATS().SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("subscribe inbox: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	subject := "ops." + string(env.Lane)
	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header:  nats.Header{replyInboxHeader: []string{inbox}},
	}
	if _, err := s.Conn.JetStream().PublishMsg(ctx, msg); err != nil {
		return nil, fmt.Errorf("publish to %s: %w", subject, err)
	}

	replyMsg, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for reply: %w", err)
	}
	var reply opwire.OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		return nil, fmt.Errorf("parse reply: %w", err)
	}
	return &reply, nil
}
