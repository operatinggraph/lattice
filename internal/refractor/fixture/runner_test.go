package fixture_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/fixture"
	"github.com/asolgan/lattice/internal/substrate"
)

// startFixtureJS starts an in-memory NATS server and returns a JetStream handle
// plus a substrate connection. Skips via testing.Short() — callers do not need a
// separate guard.
func startFixtureJS(t *testing.T) (jetstream.JetStream, *substrate.Conn) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  jsstore.Dir(t),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err, "create NATS server")
	t.Cleanup(s.Shutdown)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second), "NATS server not ready within 5s")

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	return js, conn
}

// createFixtureBuckets creates the three KV buckets required by RunFixture:
// ADJ (adjacency), CORE (Core KV), and the target bucket named by fix.Rule.Into.Bucket.
func createFixtureBuckets(t *testing.T, js jetstream.JetStream, conn *substrate.Conn, fix *fixture.Fixture) (adjKV, coreKV, targetKV *substrate.KV) {
	t.Helper()
	ctx := context.Background()

	_, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "ADJ"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "CORE"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: fix.Rule.Into.Bucket})
	require.NoError(t, err)

	adjKV, err = conn.OpenKV(ctx, "ADJ")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "CORE")
	require.NoError(t, err)
	targetKV, err = conn.OpenKV(ctx, fix.Rule.Into.Bucket)
	require.NoError(t, err)

	return
}

// fixtureDataPath resolves the absolute path to a file in testdata/fixtures/.
func fixtureDataPath(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "../../testdata/fixtures", name)
}

// TestRunFixture_BasicUpsert runs the basic_upsert.yaml fixture end-to-end against
// an in-memory NATS server. Exercises the full evaluate → project → write pipeline (AC1, AC4).
func TestRunFixture_BasicUpsert(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}

	fix, err := fixture.Load(fixtureDataPath("basic_upsert.yaml"))
	require.NoError(t, err)

	js, conn := startFixtureJS(t)
	adjKV, coreKV, targetKV := createFixtureBuckets(t, js, conn, fix)

	// RunFixture evaluates all inputs and asserts the expected outputs internally.
	fixture.RunFixture(t, fix, adjKV, coreKV, targetKV)

	// Independent double-check: verify the target KV entry directly (AC4).
	entry, err := targetKV.Get(context.Background(), "abc")
	require.NoError(t, err, "target KV key 'abc' must exist after fixture run")
	assert.JSONEq(t, `{"agreement_id":"abc","party_name":"Acme"}`, string(entry.Value),
		"target KV value must match expected projection")
}

// TestRunFixture_BasicUpsert_Determinism runs the same fixture twice against independent
// in-memory NATS servers and asserts identical output bytes (NFR20, AC3).
func TestRunFixture_BasicUpsert_Determinism(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}

	fix, err := fixture.Load(fixtureDataPath("basic_upsert.yaml"))
	require.NoError(t, err)

	// Run 1: independent NATS server + fresh buckets.
	js1, conn1 := startFixtureJS(t)
	adj1, core1, target1 := createFixtureBuckets(t, js1, conn1, fix)
	fixture.RunFixture(t, fix, adj1, core1, target1)
	entry1, err := target1.Get(context.Background(), "abc")
	require.NoError(t, err)

	// Run 2: separate NATS server — no shared state with run 1.
	js2, conn2 := startFixtureJS(t)
	adj2, core2, target2 := createFixtureBuckets(t, js2, conn2, fix)
	fixture.RunFixture(t, fix, adj2, core2, target2)
	entry2, err := target2.Get(context.Background(), "abc")
	require.NoError(t, err)

	assert.Equal(t, entry1.Value, entry2.Value,
		"fixture output must be identical across independent runs (NFR20)")
}

// TestRunFixture_DeleteOnSoftDelete verifies that isDeleted=true produces a hard delete
// and a subsequent reversal (isDeleted=false) re-upserts the entry (AC1).
func TestRunFixture_DeleteOnSoftDelete(t *testing.T) {
	fix, err := fixture.Load(fixtureDataPath("delete_on_soft_delete.yaml"))
	require.NoError(t, err)

	js, conn := startFixtureJS(t)
	adjKV, coreKV, targetKV := createFixtureBuckets(t, js, conn, fix)

	fixture.RunFixture(t, fix, adjKV, coreKV, targetKV)

	entry, err := targetKV.Get(context.Background(), "abc")
	require.NoError(t, err, "target KV key 'abc' must exist after soft-delete reversal")
	assert.JSONEq(t, `{"agreement_id":"abc","agreement_name":"Acme Updated"}`, string(entry.Value),
		"target KV value must match re-upserted projection after soft-delete reversal")
}

// TestRunFixture_OptionalMatchNull verifies that OPTIONAL MATCH with no matching edge
// produces null for the optional column (AC2).
func TestRunFixture_OptionalMatchNull(t *testing.T) {
	fix, err := fixture.Load(fixtureDataPath("optional_match_null.yaml"))
	require.NoError(t, err)

	js, conn := startFixtureJS(t)
	adjKV, coreKV, targetKV := createFixtureBuckets(t, js, conn, fix)

	fixture.RunFixture(t, fix, adjKV, coreKV, targetKV)

	entry, err := targetKV.Get(context.Background(), "abc")
	require.NoError(t, err, "target KV key 'abc' must exist after fixture run")
	assert.JSONEq(t, `{"agreement_id":"abc","contact_email":null}`, string(entry.Value),
		"target KV value must contain null for unmatched optional column")
}

// TestRunFixture_OutOfOrderRetry verifies eventual consistency when a non-anchor node
// arrives before its anchor: the non-anchor delivery produces no write, and the
// subsequent anchor delivery produces the correct upsert (AC3).
func TestRunFixture_OutOfOrderRetry(t *testing.T) {
	fix, err := fixture.Load(fixtureDataPath("out_of_order_retry.yaml"))
	require.NoError(t, err)

	js, conn := startFixtureJS(t)
	adjKV, coreKV, targetKV := createFixtureBuckets(t, js, conn, fix)

	fixture.RunFixture(t, fix, adjKV, coreKV, targetKV)

	entry, err := targetKV.Get(context.Background(), "abc")
	require.NoError(t, err, "target KV key 'abc' must exist after anchor delivery")
	assert.JSONEq(t, `{"agreement_id":"abc","party_name":"Alice"}`, string(entry.Value),
		"target KV value must match expected projection after out-of-order delivery")
}

// TestRunFixture_CompositeKey verifies that a two-field into.key produces a '.'-joined
// KV key (Contract #1 segment separator) and that both key fields appear in the
// projected row (AC4).
func TestRunFixture_CompositeKey(t *testing.T) {
	fix, err := fixture.Load(fixtureDataPath("composite_key.yaml"))
	require.NoError(t, err)

	js, conn := startFixtureJS(t)
	adjKV, coreKV, targetKV := createFixtureBuckets(t, js, conn, fix)

	fixture.RunFixture(t, fix, adjKV, coreKV, targetKV)

	entry, err := targetKV.Get(context.Background(), "team1.abc")
	require.NoError(t, err, "target KV key 'team1.abc' must exist after fixture run")
	assert.JSONEq(t, `{"team_id":"team1","agreement_id":"abc"}`, string(entry.Value),
		"target KV value must contain both composite key fields")
}
