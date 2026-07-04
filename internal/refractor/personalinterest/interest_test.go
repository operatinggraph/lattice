package personalinterest_test

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/personalinterest"
	"github.com/asolgan/lattice/internal/substrate"
)

func newTestKV(t *testing.T) *substrate.KV {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx := context.Background()
	_, err = conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "personal-lens-interest"})
	require.NoError(t, err)

	kv, err := conn.OpenKV(ctx, "personal-lens-interest")
	require.NoError(t, err)
	return kv
}

func TestIsRelevant_NoRegistration_AdmitsEverything(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant, "no registered device must default to admit-all")
}

func TestIsRelevant_EmptyFilterRegistration_AdmitsEverything(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", nil, nil, time.Now().UTC().Format(time.RFC3339)))

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant, "a registration with no declared types/anchors must admit everything")
}

func TestIsRelevant_TypeMatch(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"lease"}, nil, time.Now().UTC().Format(time.RFC3339)))

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant)

	relevant, err = personalinterest.IsRelevant(ctx, kv, "identityA", "payment", "payment.1")
	require.NoError(t, err)
	require.False(t, relevant, "a declared type filter must exclude a non-matching anchor type")
}

func TestIsRelevant_AnchorMatch(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", nil, []string{"lease.1"}, time.Now().UTC().Format(time.RFC3339)))

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant)

	relevant, err = personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.2")
	require.NoError(t, err)
	require.False(t, relevant, "a declared anchor filter must exclude a non-matching anchor id")
}

func TestIsRelevant_MultiDevice_AnyMatchAdmits(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"payment"}, nil, time.Now().UTC().Format(time.RFC3339)))
	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceY", []string{"lease"}, nil, time.Now().UTC().Format(time.RFC3339)))

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant, "any one device's filter admitting the delta must admit it")
}

func TestDeregister_RemovesRegistration(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"payment"}, nil, time.Now().UTC().Format(time.RFC3339)))
	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.False(t, relevant)

	require.NoError(t, personalinterest.Deregister(ctx, kv, "identityA", "deviceX"))

	relevant, err = personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant, "deregistering the only device must revert to admit-all")
}

func TestDeregister_AbsentDevice_NoError(t *testing.T) {
	kv := newTestKV(t)
	require.NoError(t, personalinterest.Deregister(context.Background(), kv, "identityA", "deviceX"))
}

func TestRegister_MissingIdentityOrDevice_Errors(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.Error(t, personalinterest.Register(ctx, kv, "", "deviceX", nil, nil, time.Now().UTC().Format(time.RFC3339)))
	require.Error(t, personalinterest.Register(ctx, kv, "identityA", "", nil, nil, time.Now().UTC().Format(time.RFC3339)))
}

func TestIsRelevant_ScopedToIdentityPrefix(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	// A registration for a DIFFERENT identity that happens to share a
	// deviceId suffix must not leak into identityA's prefix scan.
	require.NoError(t, personalinterest.Register(ctx, kv, "identityB", "deviceX", []string{"payment"}, nil, time.Now().UTC().Format(time.RFC3339)))

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	require.True(t, relevant, "identityA has no registration of its own and must default to admit-all")
}
