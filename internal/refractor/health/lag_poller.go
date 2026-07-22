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

// ProgressFunc optionally returns the pipeline's projection-progress
// lastProjectedAt clock for inclusion in the health Entry each poll cycle
// (lens-projection-liveness-design.md §3.2). Returns the zero time before the
// lens's first projection.
type ProgressFunc func() time.Time

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
}

// SetProgressFunc attaches the pipeline's projection-progress source. Must be
// called before Start. Pass nil to clear (the default).
func (lp *LagPoller) SetProgressFunc(fn ProgressFunc) {
	lp.progress = fn
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
		if err := lp.reporter.SetProjectionProgress(ctx, lag, lastProjectedAt); err != nil {
			if ctx.Err() == nil {
				slog.Warn("lag poller: SetProjectionProgress failed",
					"ruleId", lp.ruleID, "err", err)
			}
		}
	}
}
