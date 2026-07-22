package output

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// replyInboxHeader is the NATS message header the CLI sets to carry the
// reply inbox subject through JetStream delivery. JetStream pull consumers
// rewrite msg.Reply() to the JetStream ACK subject; the Processor reads this
// header to locate the caller's inbox when it is present.
const replyInboxHeader = "Lattice-Reply-Inbox"

// SubmitOp publishes an OperationEnvelope to ops.<lane> via JetStream,
// routing the Processor's reply back to a NATS core inbox subscription.
//
// JetStream pull consumers rewrite msg.Reply() to the stream's ACK subject,
// so a plain NATS Request() would receive only the JetStream publish-ack.
// The inbox is instead carried in a message header; the Processor reads
// it to deliver both accepted and rejected replies directly to the caller.
func SubmitOp(ctx context.Context, conn *substrate.Conn, env *processor.OperationEnvelope) (*processor.OperationReply, error) {
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	// Create an inbox and subscribe before publishing so no reply is missed.
	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("subscribe inbox: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Build the NATS message with the inbox in a header so the Processor
	// can reply directly to the caller's inbox.
	subject := "ops." + string(env.Lane)
	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header:  nats.Header{replyInboxHeader: []string{inbox}},
	}
	if _, err := conn.JetStream().PublishMsg(ctx, msg); err != nil {
		return nil, fmt.Errorf("publish to %s: %w", subject, err)
	}

	// Wait for the Processor reply on the inbox.
	replyMsg, err := sub.NextMsgWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for reply: %w", err)
	}

	var reply processor.OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		return nil, fmt.Errorf("parse reply: %w", err)
	}
	return &reply, nil
}
