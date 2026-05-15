package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/subjects"
)

// MetricsInterval is the default polling interval for new LagPoller instances.
// Set this before calling NewLagPoller to override the default (5 seconds).
// Exported so tests can override it to a short value without real sleeps.
// The interval is captured into the LagPoller at construction time, so changes
// after NewLagPoller returns have no effect on running pollers.
var MetricsInterval = 5 * time.Second

// LagMetric is the JSON payload published to materializer.metrics.<ruleId> on each poll.
// All field names are camelCase per FR21 convention.
type LagMetric struct {
	RuleID      string `json:"ruleId"`
	Team        string `json:"team"`
	ConsumerLag uint64 `json:"consumerLag"`
	Timestamp   string `json:"timestamp"` // RFC3339 UTC
}

// LagPoller publishes per-rule consumer lag metrics to materializer.metrics.<ruleId>
// at the interval captured from MetricsInterval at construction time.
// It also updates the health KV consumerLag field on each cycle.
// Call Start in a dedicated goroutine.
type LagPoller struct {
	nc       *nats.Conn
	mu       sync.RWMutex   // protects consumer
	consumer jetstream.Consumer
	reporter *Reporter     // may be nil — health KV update skipped when nil
	ruleID   string
	team     string
	interval time.Duration // captured from MetricsInterval at NewLagPoller time
}

// NewLagPoller creates a LagPoller for the given rule.
// Panics if nc or consumer is nil (both are required for correct operation).
// reporter may be nil — health KV updates are skipped in that case.
// The polling interval is captured from MetricsInterval at call time.
func NewLagPoller(nc *nats.Conn, consumer jetstream.Consumer, reporter *Reporter, ruleID, team string) *LagPoller {
	if nc == nil {
		panic("health: NewLagPoller: nc must not be nil")
	}
	if consumer == nil {
		panic("health: NewLagPoller: consumer must not be nil")
	}
	iv := MetricsInterval
	if iv <= 0 {
		iv = 5 * time.Second // safe default if MetricsInterval was set to an invalid value
	}
	return &LagPoller{
		nc:       nc,
		consumer: consumer,
		reporter: reporter,
		ruleID:   ruleID,
		team:     team,
		interval: iv,
	}
}

// SetConsumer atomically replaces the consumer used by the poller.
// Call after a rebuild consumer reset so the poller does not query a deleted consumer.
// Thread-safe; may be called while Start is running.
// Panics if c is nil — a nil consumer would cause the next poll to panic.
func (lp *LagPoller) SetConsumer(c jetstream.Consumer) {
	if c == nil {
		panic("health: LagPoller.SetConsumer: consumer must not be nil")
	}
	lp.mu.Lock()
	lp.consumer = c
	lp.mu.Unlock()
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
	lp.mu.RLock()
	cons := lp.consumer
	lp.mu.RUnlock()
	info, err := cons.Info(ctx)
	if err != nil {
		// Suppress context-cancellation noise on graceful shutdown.
		if ctx.Err() == nil {
			slog.Warn("lag poller: consumer.Info failed",
				"ruleId", lp.ruleID, "err", err)
		}
		return
	}
	lag := info.NumPending

	msg := LagMetric{
		RuleID:      lp.ruleID,
		Team:        lp.team,
		ConsumerLag: lag,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		slog.Warn("lag poller: marshal failed",
			"ruleId", lp.ruleID, "err", err)
		return
	}
	if err := lp.nc.Publish(subjects.Metrics(lp.ruleID), data); err != nil {
		slog.Warn("lag poller: publish failed",
			"ruleId", lp.ruleID, "err", err)
	}

	if lp.reporter != nil {
		if err := lp.reporter.SetConsumerLag(ctx, lag); err != nil {
			if ctx.Err() == nil {
				slog.Warn("lag poller: SetConsumerLag failed",
					"ruleId", lp.ruleID, "err", err)
			}
		}
	}
}
