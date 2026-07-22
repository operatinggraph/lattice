package adapter_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// fakeDSN is a syntactically valid but unreachable DSN — safe to use because
// pgxpool.New uses lazy connection establishment (no dial at creation time).
const fakeDSN = "host=fake-host-that-does-not-exist user=test dbname=test"
const fakeDSN2 = "host=another-fake-host user=test dbname=other"

func TestPoolManager_SamePool_ForSameDSN(t *testing.T) {
	pm := adapter.NewPoolManager()
	defer pm.Close()

	ctx := context.Background()
	p1, err := pm.Acquire(ctx, fakeDSN)
	require.NoError(t, err)

	p2, err := pm.Acquire(ctx, fakeDSN)
	require.NoError(t, err)

	assert.Same(t, p1, p2, "same DSN must return the same pool instance")
}

func TestPoolManager_DifferentPools_ForDifferentDSNs(t *testing.T) {
	pm := adapter.NewPoolManager()
	defer pm.Close()

	ctx := context.Background()
	p1, err := pm.Acquire(ctx, fakeDSN)
	require.NoError(t, err)

	p2, err := pm.Acquire(ctx, fakeDSN2)
	require.NoError(t, err)

	assert.NotSame(t, p1, p2, "different DSNs must produce different pool instances")
}

func TestPoolManager_Close_AllowsFreshAcquire(t *testing.T) {
	pm := adapter.NewPoolManager()

	ctx := context.Background()
	p1, err := pm.Acquire(ctx, fakeDSN)
	require.NoError(t, err)
	require.NotNil(t, p1)

	pm.Close() // drains all pools

	// After Close, a new Acquire must produce a fresh pool (map was cleared).
	p2, err := pm.Acquire(ctx, fakeDSN)
	require.NoError(t, err)
	assert.NotSame(t, p1, p2, "after Close, Acquire must return a new pool")
}

func TestPoolManager_Close_DoesNotPanic(t *testing.T) {
	pm := adapter.NewPoolManager()
	// Close on empty manager must not panic.
	assert.NotPanics(t, func() { pm.Close() })
}

// TestPoolManager_Probe_RequiresPostgres tests pool.Ping against a real Postgres.
// Skipped unless POSTGRES_TEST_DSN env var is set and -short is not active.
func TestPoolManager_Probe_RequiresPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Postgres integration test in short mode")
	}
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("skipping: POSTGRES_TEST_DSN not set")
	}

	pm := adapter.NewPoolManager()
	defer pm.Close()

	ctx := context.Background()
	pool, err := pm.Acquire(ctx, dsn)
	require.NoError(t, err)

	err = pool.Ping(ctx)
	assert.NoError(t, err, "Ping against real Postgres must succeed")
}
