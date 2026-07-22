package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

func newNATSSubmitterTestConn(t *testing.T, ctx context.Context) *substrate.Conn {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url})
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	_, err = conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "core-operations",
		Subjects: []string{"ops.>"},
	})
	require.NoError(t, err)
	return conn
}

// startFakeProcessor replies to every operation envelope published on
// ops.> according to decide, mimicking the Processor's synchronous
// request-reply (commit_path.go's Lattice-Reply-Inbox header path) without
// running the real pipeline.
func startFakeProcessor(t *testing.T, conn *substrate.Conn, decide func(*processor.OperationEnvelope) processor.OperationReply) {
	t.Helper()
	sub, err := conn.NATS().Subscribe("ops.>", func(msg *nats.Msg) {
		var env processor.OperationEnvelope
		if err := json.Unmarshal(msg.Data, &env); err != nil {
			return
		}
		inbox := msg.Header.Get(replyInboxHeader)
		if inbox == "" {
			return
		}
		reply := decide(&env)
		b, err := json.Marshal(reply)
		if err != nil {
			return
		}
		_ = conn.NATS().Publish(inbox, b)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
}

func TestNATSSubmitter_SubmitReturnsProcessorReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := newNATSSubmitterTestConn(t, ctx)
	startFakeProcessor(t, conn, func(env *processor.OperationEnvelope) processor.OperationReply {
		return processor.OperationReply{RequestID: env.RequestID, Status: processor.ReplyStatusAccepted, Decision: "committed"}
	})

	s := &NATSSubmitter{Conn: conn}
	env := testEnv("req1")
	reply, err := s.Submit(ctx, env)
	require.NoError(t, err)
	require.Equal(t, processor.ReplyStatusAccepted, reply.Status)
	require.Equal(t, "req1", reply.RequestID)
}

func TestNATSSubmitter_SubmitErrorsWhenNoResponder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn := newNATSSubmitterTestConn(t, ctx) // no fake processor started — no responder ever replies.

	s := &NATSSubmitter{Conn: conn}
	drainCtx, drainCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer drainCancel()
	_, err := s.Submit(drainCtx, testEnv("req1"))
	require.Error(t, err)
}
