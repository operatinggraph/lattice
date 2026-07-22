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

	"github.com/operatinggraph/lattice/internal/gateway/revocation"
	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
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
	// TTL-capable, mirroring how bootstrap provisions health-kv (PlatformBuckets
	// PerKeyTTL ⇒ LimitMarkerTTL, primordial.go): the heartbeat is written with
	// a per-key TTL, so a fixture bucket without it would reject every write for
	// a reason the real bucket never has.
	_, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         "health-kv",
		LimitMarkerTTL: time.Second,
	})
	require.NoError(t, err)
	return conn, ctx
}

// TestHeartbeater_EmitArmsHeartbeatTTL pins the heartbeat key as TTL-armed on
// every write, matching every other Contract #5 §5.6 emitter. An untimed write
// leaks one permanent health.gateway.<instance> key per restart, and the health
// rollup counts each leaked key as a stale component — enough of them and
// overall platform health can never read green again.
func TestHeartbeater_EmitArmsHeartbeatTTL(t *testing.T) {
	conn, ctx := newHealthTestConn(t)

	hb := NewHeartbeater(conn, "health-kv", "gw-ttl", &Metrics{}, nil)
	hb.emit(ctx, "healthy")

	// The write lands (a TTL-less bucket would have rejected it outright)...
	_, err := conn.KVGet(ctx, "health-kv", "health.gateway.gw-ttl")
	require.NoError(t, err)

	// ...and the key carries the §5.6 interval-derived TTL, not an unbounded one.
	require.Equal(t, DefaultHeartbeatEvery*healthkv.DefaultTTLMultiplier, hb.heartbeatTTL(),
		"heartbeat TTL must follow the shared interval × DefaultTTLMultiplier convention")

	stream, err := conn.JetStream().Stream(ctx, "KV_health-kv")
	require.NoError(t, err)
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.True(t, info.Config.AllowMsgTTL,
		"health-kv must be TTL-capable for the heartbeat to age out (bootstrap provisions it so)")
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

// TestHeartbeater_EmitIncludesRevocationBlock locks the §2.6 heartbeat
// extension: a fresh Heartbeater (no materializer wired) reports the
// pre-materializer zero state, RecordRevocationSync/SetRevocationConnected
// surface in the next emit, and revokedCount is scanned live off the
// token-revocation bucket rather than cached at call time.
func TestHeartbeater_EmitIncludesRevocationBlock(t *testing.T) {
	conn, ctx := newHealthTestConn(t)
	_, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: revocation.BucketName})
	require.NoError(t, err)

	hb := NewHeartbeater(conn, "health-kv", "gw-test-rev", &Metrics{}, nil)
	hb.emit(ctx, "healthy")

	entry, err := conn.KVGet(ctx, "health-kv", "health.gateway.gw-test-rev")
	require.NoError(t, err)
	var doc healthDoc
	require.NoError(t, json.Unmarshal(entry.Value, &doc))

	if !doc.Revocation.ConsumerConnected {
		t.Error("Revocation.ConsumerConnected = false before any SetRevocationConnected(false) call, want true (assumed-connected default)")
	}
	if doc.Revocation.RevokedCount != 0 {
		t.Errorf("Revocation.RevokedCount = %d, want 0 (empty bucket)", doc.Revocation.RevokedCount)
	}
	if doc.Revocation.LastEventSeq != 0 {
		t.Errorf("Revocation.LastEventSeq = %d, want 0", doc.Revocation.LastEventSeq)
	}
	if doc.Revocation.LastSyncAt != "" {
		t.Errorf("Revocation.LastSyncAt = %q, want empty (never synced)", doc.Revocation.LastSyncAt)
	}

	_, err = conn.KVPut(ctx, revocation.BucketName, "vtx.identity.Revoked1", []byte(`{}`))
	require.NoError(t, err)
	hb.SetRevocationConnected(false) // e.g. the consumer paused (§2.6 fail-safe half)
	syncAt := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	hb.RecordRevocationSync(42, syncAt)
	hb.emit(ctx, "healthy")

	entry, err = conn.KVGet(ctx, "health-kv", "health.gateway.gw-test-rev")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(entry.Value, &doc))

	if doc.Revocation.ConsumerConnected {
		t.Error("Revocation.ConsumerConnected = true after SetRevocationConnected(false), want false")
	}
	if doc.Revocation.RevokedCount != 1 {
		t.Errorf("Revocation.RevokedCount = %d, want 1", doc.Revocation.RevokedCount)
	}
	if doc.Revocation.LastEventSeq != 42 {
		t.Errorf("Revocation.LastEventSeq = %d, want 42", doc.Revocation.LastEventSeq)
	}
	if doc.Revocation.LastSyncAt != syncAt.Format(time.RFC3339) {
		t.Errorf("Revocation.LastSyncAt = %q, want %q", doc.Revocation.LastSyncAt, syncAt.Format(time.RFC3339))
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

func TestHeartbeater_SetIssueDegradesStatus(t *testing.T) {
	conn, ctx := newHealthTestConn(t)
	hb := NewHeartbeater(conn, "health-kv", "gw-test3", &Metrics{}, nil)
	hb.SetIssue("revocation-kill-switch", severityWarning, "GatewayRevocationDisabled", "kill-switch disabled")
	hb.emit(ctx, "healthy")

	entry, err := conn.KVGet(ctx, "health-kv", "health.gateway.gw-test3")
	require.NoError(t, err)
	var doc healthDoc
	require.NoError(t, json.Unmarshal(entry.Value, &doc))

	if doc.Status != "degraded" {
		t.Errorf("Status = %q, want degraded (an open warning issue must never self-report healthy)", doc.Status)
	}
	if len(doc.Issues) != 1 || doc.Issues[0].Code != "GatewayRevocationDisabled" {
		t.Errorf("Issues = %+v, want one GatewayRevocationDisabled entry", doc.Issues)
	}

	hb.ClearIssue("revocation-kill-switch")
	hb.emit(ctx, "healthy")
	entry, err = conn.KVGet(ctx, "health-kv", "health.gateway.gw-test3")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	if doc.Status != "healthy" {
		t.Errorf("Status after ClearIssue = %q, want healthy", doc.Status)
	}
}

// TestAggregateStatus locks the Contract #5 §5.2/§5.3 reconciliation: a heartbeat
// carrying issues can never self-report "healthy", lifecycle phases pass through,
// and error wins over warning. Mirrors the Loom/Weaver/Bridge/Processor heartbeaters.
func TestAggregateStatus(t *testing.T) {
	t.Parallel()
	warn := healthIssue{Severity: severityWarning, Code: "GatewayRevocationDisabled", Message: "x"}
	errIssue := healthIssue{Severity: severityError, Code: "Boom", Message: "y"}

	cases := []struct {
		name      string
		lifecycle string
		issues    []healthIssue
		want      string
	}{
		{"healthy no issues stays healthy", "healthy", nil, "healthy"},
		{"healthy with warning degrades", "healthy", []healthIssue{warn}, "degraded"},
		{"healthy with error is unhealthy", "healthy", []healthIssue{errIssue}, "unhealthy"},
		{"error wins over warning", "healthy", []healthIssue{warn, errIssue}, "unhealthy"},
		{"starting passes through despite issues", "starting", []healthIssue{warn, errIssue}, "starting"},
		{"shutdown passes through despite issues", "shutdown", []healthIssue{errIssue}, "shutdown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aggregateStatus(tc.lifecycle, tc.issues); got != tc.want {
				t.Fatalf("aggregateStatus(%q, %v) = %q, want %q", tc.lifecycle, tc.issues, got, tc.want)
			}
		})
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

// issueCache.set must stamp since (Contract #5 §5.5) on first appearance, hold
// it steady across repeat set calls for the same key while the issue stays
// open, and clear it with the issue so a later re-occurrence gets a fresh
// since rather than reusing the stale one.
func TestIssueCacheSincePersistence(t *testing.T) {
	t.Parallel()
	c := newIssueCache()

	c.set("k", severityWarning, "Code", "first")
	first := c.snapshot()
	if len(first) != 1 || first[0].Since == "" {
		t.Fatalf("first set: got %+v, want one issue with a non-empty since", first)
	}
	since := first[0].Since

	c.set("k", severityWarning, "Code", "still open")
	second := c.snapshot()
	if len(second) != 1 || second[0].Since != since {
		t.Fatalf("since not persisted across repeat set: first %q, second %+v", since, second)
	}

	c.clear("k")
	if len(c.snapshot()) != 0 {
		t.Fatalf("cleared key still present: %+v", c.snapshot())
	}

	c.set("k", severityWarning, "Code", "reoccurred")
	reoccurred := c.snapshot()
	if len(reoccurred) != 1 || reoccurred[0].Since == since {
		t.Fatalf("reoccurred issue reused stale since %q: %+v", since, reoccurred)
	}
}
