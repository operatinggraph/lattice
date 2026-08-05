// Package projectionhealth lets a vertical app's protected-read handler tell
// an RLS-empty result apart from a paused Refractor projection, by reading
// the same Health KV entry the lens's own internal/refractor/health.Reporter
// writes (health-kv is operational self-reporting, not Core KV and not a
// lens — CLAUDE.md's P5 read-path rule does not apply to it).
package projectionhealth

import (
	"context"
	"encoding/json"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/refractor/health/healthwire"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Status is what a protected-read handler can tell an empty result apart from
// a paused projection. Known is false whenever the health entry could not be
// read or decoded — a nil conn, an unreachable health-kv bucket, or a lens
// that has never reported — and the caller must treat that as "couldn't
// check", never as a false healthy or a false paused.
type Status struct {
	Known       bool
	Paused      bool
	PauseReason string // "" unless Paused
}

// Check reads ruleID's Health KV entry and reports whether its projection is
// currently paused. conn may be nil (NATS unreachable at handler
// construction); Check returns the zero Status rather than panicking, so a
// handler can call this unconditionally and only branch on Known.
func Check(ctx context.Context, conn *substrate.Conn, ruleID string) Status {
	if conn == nil {
		return Status{}
	}
	entry, err := conn.KVGet(ctx, bootstrap.HealthKVBucket, ruleID)
	if err != nil {
		return Status{}
	}
	var e healthwire.Entry
	if json.Unmarshal(entry.Value, &e) != nil {
		return Status{}
	}
	st := Status{Known: true, Paused: e.Status == healthwire.StatusPaused}
	if st.Paused && e.PauseReason != nil {
		st.PauseReason = *e.PauseReason
	}
	return st
}
