package adapter

import (
	"context"
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
func (m *PoolManager) Acquire(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.pools[dsn]; ok {
		return p, nil
	}
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	m.pools[dsn] = p
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
