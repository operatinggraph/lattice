package opstatus_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/opstatus"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const testBucket = "core-kv"

func startTestConn(t *testing.T) *substrate.Conn {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	_, err = conn.JetStream().CreateOrUpdateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: testBucket})
	require.NoError(t, err)
	return conn
}

func startTestService(t *testing.T, conn *substrate.Conn) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc := opstatus.NewService(conn, testBucket, nil)
	require.NoError(t, svc.StartNATSListener(ctx, conn.NATS()))
}

func sendStatus(t *testing.T, conn *substrate.Conn, requestID string) opstatus.Response {
	t.Helper()
	data, err := json.Marshal(opstatus.Request{RequestID: requestID})
	require.NoError(t, err)
	reply, err := conn.NATS().Request(opstatus.Subject, data, 2*time.Second)
	require.NoError(t, err, "NATS request to %s must succeed", opstatus.Subject)
	var resp opstatus.Response
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	return resp
}

func TestService_NotFound(t *testing.T) {
	conn := startTestConn(t)
	startTestService(t, conn)

	resp := sendStatus(t, conn, "Rm7q3pntwzkfbcxv5p9j")
	require.Empty(t, resp.Error)
	require.False(t, resp.Found)
	require.False(t, resp.Committed)
}

func TestService_Committed(t *testing.T) {
	conn := startTestConn(t)
	startTestService(t, conn)

	reqID := "Rm7q3pntwzkfbcxv5p9j"
	_, err := conn.KVPut(context.Background(), testBucket, "vtx.op."+reqID,
		[]byte(`{"class":"op","isDeleted":false,"data":{"committedAt":"2026-04-11T14:32:18.215Z"}}`))
	require.NoError(t, err)

	resp := sendStatus(t, conn, reqID)
	require.Empty(t, resp.Error)
	require.True(t, resp.Found)
	require.True(t, resp.Committed)
	require.False(t, resp.IsDeleted)
	require.Equal(t, "2026-04-11T14:32:18.215Z", resp.CommittedAt)
	require.Equal(t, "op", resp.Class)
}

// TestService_Tombstoned proves Contract #4 §4.3: a present-but-deleted
// tracker is the operator-driven retry signal — found, but NOT committed.
func TestService_Tombstoned(t *testing.T) {
	conn := startTestConn(t)
	startTestService(t, conn)

	reqID := "Rm7q3pntwzkfbcxv5p9j"
	_, err := conn.KVPut(context.Background(), testBucket, "vtx.op."+reqID,
		[]byte(`{"class":"op","isDeleted":true,"data":{}}`))
	require.NoError(t, err)

	resp := sendStatus(t, conn, reqID)
	require.Empty(t, resp.Error)
	require.True(t, resp.Found)
	require.False(t, resp.Committed)
	require.True(t, resp.IsDeleted)
}

func TestService_InvalidRequestID(t *testing.T) {
	conn := startTestConn(t)
	startTestService(t, conn)

	for _, id := range []string{"", "vtx.op.foo", "a.b", "a*", "a>", "a b"} {
		resp := sendStatus(t, conn, id)
		require.NotEmpty(t, resp.Error, "requestId %q must be rejected", id)
		require.False(t, resp.Found)
	}
}

func TestService_UnparseableTracker_ReportsNotFound(t *testing.T) {
	conn := startTestConn(t)
	startTestService(t, conn)

	reqID := "Rm7q3pntwzkfbcxv5p9j"
	_, err := conn.KVPut(context.Background(), testBucket, "vtx.op."+reqID, []byte(`{not valid json`))
	require.NoError(t, err)

	resp := sendStatus(t, conn, reqID)
	require.Empty(t, resp.Error)
	require.False(t, resp.Found)
}
