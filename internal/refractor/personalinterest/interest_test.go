package personalinterest_test

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
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
	"github.com/operatinggraph/lattice/internal/substrate"
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

func TestSetRevisionCursor_NewDevice_CreatesCursorOnlyDoc(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.SetRevisionCursor(ctx, kv, "identityA", "deviceX", 10500,
		time.Now().UTC().Format(time.RFC3339)))

	key, err := personalinterest.Key("identityA", "deviceX")
	require.NoError(t, err)
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	require.Equal(t, float64(10500), doc["revisionCursor"])
}

func TestSetRevisionCursor_PreservesExistingFilter(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"lease"}, nil, time.Now().UTC().Format(time.RFC3339)))
	require.NoError(t, personalinterest.SetRevisionCursor(ctx, kv, "identityA", "deviceX", 20000,
		time.Now().UTC().Format(time.RFC3339)))

	// The Interest Set filter must survive the cursor update — a hydrate call
	// must not silently revert a device to admit-all.
	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "payment", "payment.1")
	require.NoError(t, err)
	require.False(t, relevant, "the pre-existing type filter must survive a revision-cursor update")

	key, err := personalinterest.Key("identityA", "deviceX")
	require.NoError(t, err)
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	require.Equal(t, float64(20000), doc["revisionCursor"])
}

func TestSetRevisionCursor_ConcurrentCallers_NeitherUpdateIsLost(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"lease"}, nil, now))

	// Two concurrent cursor-record calls for the SAME device (e.g. a hydrate
	// racing another hydrate, or a register racing a hydrate) must both
	// survive via the CAS retry loop — a plain Get-then-Put would let the
	// second Put silently clobber the first's revision.
	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(rev uint64) {
			errs <- personalinterest.SetRevisionCursor(ctx, kv, "identityA", "deviceX", rev, now)
		}(uint64(1000 + i))
	}
	for i := 0; i < n; i++ {
		require.NoError(t, <-errs)
	}

	// Whichever call's write landed last, the filter set by Register at the
	// start must still be intact — no update was lost outright, only raced.
	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "payment", "payment.1")
	require.NoError(t, err)
	require.False(t, relevant, "the type filter must survive concurrent cursor updates")

	key, err := personalinterest.Key("identityA", "deviceX")
	require.NoError(t, err)
	entry, err := kv.Get(ctx, key)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	cursor, ok := doc["revisionCursor"].(float64)
	require.True(t, ok)
	require.GreaterOrEqual(t, cursor, float64(1000))
	require.Less(t, cursor, float64(1000+n))
}

func TestSetRevisionCursor_MissingIdentityOrDevice_Errors(t *testing.T) {
	kv := newTestKV(t)
	ctx := context.Background()

	require.Error(t, personalinterest.SetRevisionCursor(ctx, kv, "", "deviceX", 1, time.Now().UTC().Format(time.RFC3339)))
	require.Error(t, personalinterest.SetRevisionCursor(ctx, kv, "identityA", "", 1, time.Now().UTC().Format(time.RFC3339)))
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
