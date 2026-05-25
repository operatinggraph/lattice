package adapter

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolManager maintains one pgxpool.Pool per unique DSN.
// All rules targeting the same Postgres DSN share one pool (ADR-9).
// It is safe for concurrent use.
type PoolManager struct {
	mu    sync.Mutex
	pools map[string]*pgxpool.Pool
}

// NewPoolManager creates an empty PoolManager ready for use.
func NewPoolManager() *PoolManager {
	return &PoolManager{pools: make(map[string]*pgxpool.Pool)}
}

// Acquire returns the existing pool for dsn, or creates a new one if none exists.
// pgxpool.New uses lazy connections — no real connection attempt occurs at creation time;
// the first Ping or query triggers the dial.
// The DSN is canonicalized via pgxpool.ParseConfig before use as the map key so that
// differently-ordered query-parameter strings targeting the same server share one pool.
func (m *PoolManager) Acquire(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	// Normalize the DSN so that query-param order differences do not create
	// duplicate pools for the same server (ADR-9 bounded connection count).
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pool: parse DSN: %w", err)
	}
	canonicalKey := cfg.ConnString()

	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[canonicalKey]; ok {
		return p, nil
	}
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	m.pools[canonicalKey] = p
	return p, nil
}

// Close drains and closes all managed pools, releasing all connections.
// After Close, the PoolManager is empty and Acquire will create fresh pools.
// The map is swapped out under the lock so that concurrent Acquire calls are
// not blocked while pools drain (pgxpool.Close can block on inflight queries).
func (m *PoolManager) Close() {
	m.mu.Lock()
	pools := m.pools
	m.pools = make(map[string]*pgxpool.Pool)
	m.mu.Unlock()
	for _, p := range pools {
		p.Close()
	}
}
