// Package projectionhealth lets a vertical app's protected-read handler tell
// an RLS-empty result apart from a Refractor projection that is paused or
// stalled, by reading the same Health KV entry the lens's own
// internal/refractor/health.Reporter writes (health-kv is operational
// self-reporting, not Core KV and not a lens — CLAUDE.md's P5 read-path rule
// does not apply to it). Paused is an explicit status the lens's own
// supervisor set; stalled is inferred from the entry's forward-progress
// clocks — a lens that is still "active" but whose outstanding backlog has
// stopped shrinking for a while, which looks identical to a healthy quiet
// lens unless a reader also checks how long it has been stuck.
package projectionhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/refractor/health/healthwire"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// StallThreshold is how long a lens's outstanding backlog (consumer lag or
// ack-pending work) must show no forward progress before Check reports it
// stalled. Exported so tests can shrink it to avoid a real sleep, mirroring
// internal/refractor/health.MetricsInterval's test-override pattern.
var StallThreshold = 2 * time.Minute

// Status is what a protected-read handler can tell an empty result apart from
// a paused or stalled projection. Known is false whenever the health entry
// could not be read or decoded — a nil conn, an unreachable health-kv bucket,
// or a lens that has never reported — and the caller must treat that as
// "couldn't check", never as a false healthy, paused, or stalled.
type Status struct {
	Known       bool
	Paused      bool
	PauseReason string // "" unless Paused
	Stalled     bool
	StallReason string // "" unless Stalled
}

// Check reads ruleID's Health KV entry and reports whether its projection is
// currently paused or stalled. conn may be nil (NATS unreachable at handler
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
	// A paused lens is definitionally not "stalled" — pausing is an explicit,
	// already-reported state, and evaluating staleness on top of it would risk
	// reporting the same outage twice under two different names.
	if !st.Paused {
		st.Stalled, st.StallReason = evalStall(e, time.Now())
	}
	return st
}

// evalStall checks, in order, whether ConsumerLag or AckPending has shown no
// forward progress for at least StallThreshold. now is passed in rather than
// read internally so the logic is trivially unit-testable.
//
// An unparseable forward-progress timestamp is treated as "cannot evaluate
// this signal" rather than a panic or a guess in either direction: the other
// signal is still checked, and if neither is evaluable Stalled stays false —
// the same "Known=false means couldn't check, don't guess" philosophy the
// package applies at the entry level, applied here to a sub-signal.
func evalStall(e healthwire.Entry, now time.Time) (bool, string) {
	if e.ConsumerLag > 0 && e.LagProgressAt != "" {
		if t, err := time.Parse(time.RFC3339, e.LagProgressAt); err == nil {
			if stuck := now.Sub(t); stuck >= StallThreshold {
				return true, fmt.Sprintf("consumer lag %d has not decreased in over %s", e.ConsumerLag, StallThreshold)
			}
		}
	}
	if e.AckPending > 0 && e.AckFloorProgressAt != "" {
		if t, err := time.Parse(time.RFC3339, e.AckFloorProgressAt); err == nil {
			if stuck := now.Sub(t); stuck >= StallThreshold {
				return true, fmt.Sprintf("%d delivered messages have not been acked in over %s", e.AckPending, StallThreshold)
			}
		}
	}
	return false, ""
}
