package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// MetricsInterval is the default polling interval for new LagPoller instances.
// Set this before calling NewLagPoller to override the default (5 seconds).
// Exported so tests can override it to a short value without real sleeps.
// The interval is captured into the LagPoller at construction time, so changes
// after NewLagPoller returns have no effect on running pollers.
var MetricsInterval = 5 * time.Second

// LagMetric is the JSON payload published to lattice.refractor.metrics.<lensId> on each poll.
// All field names are camelCase per FR21 convention.
type LagMetric struct {
	RuleID      string `json:"ruleId"`
	ConsumerLag uint64 `json:"consumerLag"`
	Timestamp   string `json:"timestamp"` // RFC3339 UTC
}

// LagFunc returns the current consumer lag (pending message count) for the rule.
// It returns an error when the lag source is not yet available — e.g. the
// supervised consumer has not finished registering at startup — which the poller
// treats as "skip this cycle", not a fatal condition.
type LagFunc func(ctx context.Context) (uint64, error)

// AckStatsFunc optionally returns the consumer's un-acked count and ack floor.
// Same error posture as LagFunc: an error means "skip this cycle", not fatal.
type AckStatsFunc func(ctx context.Context) (substrate.AckStats, error)

// ProgressFunc optionally returns the pipeline's projection-progress
// lastProjectedAt clock for inclusion in the health Entry each poll cycle
// (lens-projection-liveness-design.md §3.2). Returns the zero time before the
// lens's first projection.
type ProgressFunc func() time.Time

// PeakRowsFunc optionally returns the lens's peak binding rows over the
// pipeline's rolling observation window, and whether the window holds any
// sample at all. A false second return means "no evaluation to report" — the
// poller then writes nothing, rather than laying a fabricated zero over a real
// earlier observation.
type PeakRowsFunc func() (uint64, bool)

// LagPoller publishes per-lens consumer lag metrics to lattice.refractor.metrics.<lensId>
// at the interval captured from MetricsInterval at construction time.
// It also updates the health KV consumerLag/projectionLag/lastProjectedAt fields
// on each cycle. Call Start in a dedicated goroutine.
type LagPoller struct {
	conn     *substrate.Conn
	lag      LagFunc
	reporter *Reporter // may be nil — health KV update skipped when nil
	ruleID   string
	interval time.Duration // captured from MetricsInterval at NewLagPoller time

	// progress optionally supplies the lastProjectedAt clock. nil (the default,
	// unless SetProgressFunc is called) folds in a zero time — the Entry's
	// lastProjectedAt is then left at whatever it already held (see
	// Reporter.SetProjectionProgress).
	progress ProgressFunc

	// peakRows optionally supplies the lens's peak-binding-rows gauge. nil (the
	// default, unless SetPeakRowsFunc is called) leaves the Entry's
	// peakBindingRows untouched, so a caller that does not wire it is unchanged
	// rather than reporting a fabricated zero.
	peakRows PeakRowsFunc

	// ackStats optionally supplies the consumer's un-acked count and ack floor.
	// nil (the default, unless SetAckStatsFunc is called) leaves the Entry's
	// ackPending/ackFloorProgressAt untouched, so a caller that does not wire
	// it is unchanged rather than reporting a fabricated zero.
	ackStats AckStatsFunc

	// lagOutstanding / lagProgressAt track whether ConsumerLag is actively
	// draining, mirroring Pipeline's rebuild-progress clock
	// (recordRebuildProgress/RebuildProgress): stamped at the first poll and
	// re-stamped whenever lag DECREASES from the previous poll. A backlog that
	// keeps falling — even slowly, as on a cold bring-up replay against a
	// bucket-wide consumer filter — is legitimate catch-up, not staleness; a
	// backlog that has stopped falling for a while is what a reader should
	// treat as the real signal. Single dedicated goroutine (Start), so no lock.
	lagOutstanding uint64
	lagProgressAt  time.Time

	// ackFloor / ackFloorProgressAt are the same shape for DELIVERED work: the
	// ack floor is stamped at the first poll and re-stamped whenever it RISES.
	// Lag falling and the floor rising are the two independent ways a consumer
	// can be making progress, and a wedge shows up in the second while the
	// first reads a perfectly healthy zero. Single dedicated goroutine (Start),
	// so no lock.
	ackFloor           uint64
	ackFloorSeen       bool
	ackFloorProgressAt time.Time
}

// SetProgressFunc attaches the pipeline's projection-progress source. Must be
// called before Start. Pass nil to clear (the default).
func (lp *LagPoller) SetProgressFunc(fn ProgressFunc) {
	lp.progress = fn
}

// SetAckStatsFunc attaches the consumer's ack-stats source. Must be called
// before Start. Pass nil to clear (the default).
func (lp *LagPoller) SetAckStatsFunc(fn AckStatsFunc) {
	lp.ackStats = fn
}

// SetPeakRowsFunc attaches the pipeline's peak-binding-rows source. Must be
// called before Start. Pass nil to clear (the default).
func (lp *LagPoller) SetPeakRowsFunc(fn PeakRowsFunc) {
	lp.peakRows = fn
}

// NewLagPoller creates a LagPoller for the given rule. The lag source is read
// from the supervised consumer (the pipeline's ConsumerSupervisor) by durable
// name, so it tracks the live consumer across a rebuild reset with no handle
// re-binding. Metrics are published through the substrate connection.
// Panics if conn or lag is nil (both required). reporter may be nil — health KV
// updates are skipped in that case. The polling interval is captured from
// MetricsInterval at call time.
func NewLagPoller(conn *substrate.Conn, lag LagFunc, reporter *Reporter, ruleID string) *LagPoller {
	if conn == nil {
		panic("health: NewLagPoller: conn must not be nil")
	}
	if lag == nil {
		panic("health: NewLagPoller: lag must not be nil")
	}
	iv := MetricsInterval
	if iv <= 0 {
		iv = 5 * time.Second // safe default if MetricsInterval was set to an invalid value
	}
	return &LagPoller{
		conn:     conn,
		lag:      lag,
		reporter: reporter,
		ruleID:   ruleID,
		interval: iv,
	}
}

// Start runs the lag polling loop until ctx is cancelled.
// Run in a dedicated goroutine.
func (lp *LagPoller) Start(ctx context.Context) {
	ticker := time.NewTicker(lp.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lp.poll(ctx)
		}
	}
}

// poll reads the consumer lag and publishes one metric message.
// Errors are logged as warnings — polling continues on failure.
func (lp *LagPoller) poll(ctx context.Context) {
	lag, err := lp.lag(ctx)
	if err != nil {
		// Suppress context-cancellation noise on graceful shutdown. A transient
		// "not managed" at startup (the supervised consumer is still registering)
		// also lands here — the next cycle recovers.
		if ctx.Err() == nil {
			slog.Warn("lag poller: lag source unavailable",
				"ruleId", lp.ruleID, "err", err)
		}
		return
	}
	lp.recordLagProgress(lag)

	msg := LagMetric{
		RuleID:      lp.ruleID,
		ConsumerLag: lag,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Warn("lag poller: marshal failed",
			"ruleId", lp.ruleID, "err", err)
		return
	}
	if err := lp.conn.PublishCore(ctx, subjects.Metrics(lp.ruleID), data); err != nil {
		if ctx.Err() == nil {
			slog.Warn("lag poller: publish failed",
				"ruleId", lp.ruleID, "err", err)
		}
	}

	if lp.reporter != nil {
		var lastProjectedAt time.Time
		if lp.progress != nil {
			lastProjectedAt = lp.progress()
		}
		ackPending, ackFloorProgressAt := lp.pollAckStats(ctx)
		if err := lp.reporter.SetProjectionProgress(ctx, lag, lastProjectedAt, lp.lagProgressAt, ackPending, ackFloorProgressAt); err != nil {
			if ctx.Err() == nil {
				slog.Warn("lag poller: SetProjectionProgress failed",
					"ruleId", lp.ruleID, "err", err)
			}
		}
		lp.pollPeakRows(ctx)
	}
}

// pollPeakRows publishes the lens's peak-binding-rows gauge onto its health
// entry. It is a separate read-modify-write from SetProjectionProgress because
// it is a separate concern with its own source and its own "nothing to say"
// case: a nil source or an empty observation window writes nothing at all,
// leaving whatever the entry already holds — the alternative, folding a
// fabricated zero into the progress write, would erase a real peak every time a
// lens went quiet.
func (lp *LagPoller) pollPeakRows(ctx context.Context) {
	if lp.peakRows == nil {
		return
	}
	rows, ok := lp.peakRows()
	if !ok {
		return
	}
	if err := lp.reporter.SetPeakBindingRows(ctx, rows); err != nil {
		if ctx.Err() == nil {
			slog.Warn("lag poller: SetPeakBindingRows failed",
				"ruleId", lp.ruleID, "err", err)
		}
	}
}

// pollAckStats reads the consumer's un-acked count and ack floor and stamps the
// floor's forward-progress clock. Returns the values to fold into the health
// entry; a nil source or a read error yields the zero pair, which
// SetProjectionProgress treats as "leave the stored fields alone" rather than
// writing a fabricated zero over a real observation.
func (lp *LagPoller) pollAckStats(ctx context.Context) (uint64, time.Time) {
	if lp.ackStats == nil {
		return 0, time.Time{}
	}
	stats, err := lp.ackStats(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("lag poller: ack stats unavailable",
				"ruleId", lp.ruleID, "err", err)
		}
		return 0, time.Time{}
	}
	lp.recordAckFloorProgress(stats.AckFloor)
	return stats.AckPending, lp.ackFloorProgressAt
}

// recordAckFloorProgress stamps ackFloorProgressAt at the first observation and
// whenever the floor has RISEN since the previous poll. The floor is monotonic
// per durable, but a rebuild recreates the durable and resets it, so a floor
// that moved backwards is a new consumer generation, not a regression — stamp
// that too, since the generation itself is forward progress.
func (lp *LagPoller) recordAckFloorProgress(floor uint64) {
	if !lp.ackFloorSeen || floor != lp.ackFloor {
		lp.ackFloorProgressAt = time.Now()
	}
	lp.ackFloor = floor
	lp.ackFloorSeen = true
}

// recordLagProgress stamps lagProgressAt at the first observation and
// whenever lag has fallen since the previous poll — mirroring
// pipeline.Pipeline.recordRebuildProgress. A momentary uptick between polls
// (a concurrent write growing the backlog while it is otherwise draining)
// does not reset the clock; only a lag that stops falling ages it.
func (lp *LagPoller) recordLagProgress(lag uint64) {
	if lp.lagProgressAt.IsZero() || lag < lp.lagOutstanding {
		lp.lagProgressAt = time.Now()
	}
	lp.lagOutstanding = lag
}
