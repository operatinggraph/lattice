package gateway

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

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
)

func newHealthTestConn(t *testing.T) (*substrate.Conn, context.Context) {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	js := conn.JetStream()
	_, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "health-kv"})
	require.NoError(t, err)
	return conn, ctx
}

func TestHeartbeater_EmitWritesContract5Doc(t *testing.T) {
	conn, ctx := newHealthTestConn(t)
	m := &Metrics{}
	m.requestsTotal.Store(3)
	m.opsSubmittedTotal.Store(2)

	hb := NewHeartbeater(conn, "health-kv", "gw-test1", m, nil)
	hb.emit(ctx, "healthy")

	entry, err := conn.KVGet(ctx, "health-kv", "health.gateway.gw-test1")
	require.NoError(t, err)

	var doc healthDoc
	require.NoError(t, json.Unmarshal(entry.Value, &doc))

	if doc.Component != "gateway" {
		t.Errorf("Component = %q, want gateway", doc.Component)
	}
	if doc.Instance != "gw-test1" {
		t.Errorf("Instance = %q, want gw-test1", doc.Instance)
	}
	if doc.Status != "healthy" {
		t.Errorf("Status = %q, want healthy", doc.Status)
	}
	if doc.Key != "health.gateway.gw-test1" {
		t.Errorf("Key = %q, want health.gateway.gw-test1", doc.Key)
	}
	if doc.Issues == nil {
		t.Error("Issues must be [] not null (Contract #5 §5.2)")
	}
	reqTotal, _ := doc.Metrics["requests_total"].(float64)
	if int64(reqTotal) != 3 {
		t.Errorf("metrics.requests_total = %v, want 3", doc.Metrics["requests_total"])
	}
}

func TestHeartbeater_RunEmitsImmediatelyThenStopsOnCancel(t *testing.T) {
	conn, ctx := newHealthTestConn(t)
	hb := NewHeartbeater(conn, "health-kv", "gw-test2", &Metrics{}, nil)
	hb.every = time.Hour // never ticks again within the test

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		hb.Run(runCtx)
		close(done)
	}()

	require.Eventually(t, func() bool {
		_, err := conn.KVGet(ctx, "health-kv", "health.gateway.gw-test2")
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "expected an immediate heartbeat write")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "PT0H0M"},
		{90 * time.Minute, "PT1H30M"},
		{72*time.Hour + 15*time.Minute, "PT72H15M"},
	}
	for _, tc := range cases {
		if got := formatUptime(tc.d); got != tc.want {
			t.Errorf("formatUptime(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
