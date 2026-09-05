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
// Set this before calling NewLagPoller to override the default (30 seconds).
// Exported so tests can override it to a short value without real sleeps.
// The interval is captured into the LagPoller at construction time, so changes
// after NewLagPoller returns have no effect on running pollers.
var MetricsInterval = 30 * time.Second

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

// PersonalHealerPassFunc optionally returns the personal plane's standing
// healer's last-pass clock, its cadence, and the token that pass published.
// ok == false means this process runs no personal healer, and the poller then
// touches the field at all.
type PersonalHealerPassFunc func() (lastPassAt time.Time, interval time.Duration, staleCycles int, ok bool)

// PeakRowsFunc optionally returns the lens's peak binding rows over the
// pipeline's rolling observation window, and whether the window holds any
// sample at all. A false second return means "no evaluation to report" — the
// poller then writes nothing, rather than laying a fabricated zero over a real
// earlier observation.
type PeakRowsFunc func() (uint64, bool)

// WithholdCountsFunc optionally returns the lens's two cumulative withholding
// tallies: the per-entry rows it did not write because the target already held
// them, and the batched read-backs that failed. Both are monotone for the life
// of the process.
//
// ok == false means this lens CANNOT withhold at all — a plain, doc-mode or
// personal lens, which has no per-entry set to compare — and the poller then
// writes nothing rather than a zero. An unmeasured quantity is absent, never 0
// (Contract #5 §5.4): a stored 0 on such a lens would read as "the mechanism is
// installed and has saved nothing", which is a different and more alarming
// claim than "the mechanism does not apply here". Only a lens genuinely capable
// of withholding publishes a real 0.
type WithholdCountsFunc func() (withheld, readFailures uint64, ok bool)

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

	// withholdCounts optionally supplies the lens's withholding tallies. nil
	// (the default, unless SetWithholdCountsFunc is called) leaves the Entry's
	// two counters untouched, and so does a source reporting ok == false — a
	// lens whose host never wired the source, or one that cannot withhold at
	// all, has not measured zero, and writing one would say it had.
	withholdCounts WithholdCountsFunc

	// healerPass optionally supplies the personal healer's last-pass clock, so
	// this poller can escalate a stored personalSweepVerdict to `stale`. nil
	// leaves the field entirely alone.
	healerPass PersonalHealerPassFunc

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

	// lastWritten is the SetProjectionProgress input tuple last actually
	// written to Health KV, and lastWrittenSet reports whether any write has
	// landed yet. poll skips the read-modify-write when every value would be
	// identical to this tuple — the RMW costs a Health-KV round trip every
	// cycle for a number that mostly does not move on a quiet lens. The
	// metrics publish above it is unconditional; only this Health-KV mirror is
	// skippable. Single dedicated goroutine (Start), so no lock.
	lastWritten    projectionProgressTuple
	lastWrittenSet bool

	// lastPeakRows / lastPeakRowsSet are pollPeakRows' own mirror of the same
	// idea, for its separate gauge and its separate "nothing to say" case.
	// Single dedicated goroutine (Start), so no lock.
	lastPeakRows    uint64
	lastPeakRowsSet bool

	// lastWithheld / lastWithholdFailures / lastWithholdSet are the same
	// unchanged-value skip for the withholding tallies: two monotone counters
	// that stop moving the moment a lens goes quiet, and re-writing an
	// unchanged pair would cost a Health-KV round trip per cycle for a number
	// nobody's reading changed. Single dedicated goroutine (Start), so no lock.
	lastWithheld         uint64
	lastWithholdFailures uint64
	lastWithholdSet      bool

	// staleWritten latches the escalation so a stalled healer costs one Health-KV
	// write, not one per poll for as long as it stays stalled. Cleared the moment
	// the healer's clock moves again, which is what lets a recovered healer's own
	// write be the next thing on the field.
	staleWritten   bool
	lastSeenPassAt time.Time
}

// projectionProgressTuple is the SetProjectionProgress input set the LagPoller
// compares poll-over-poll to decide whether the Health-KV write is needed at
// all — see lastWritten.
type projectionProgressTuple struct {
	lag                uint64
	lastProjectedAt    time.Time
	lagProgressAt      time.Time
	ackPending         uint64
	ackFloorProgressAt time.Time
}

// equal reports whether t and o describe the same SetProjectionProgress call.
// time.Time fields compare via Equal rather than ==: lastProjectedAt and
// ackFloorProgressAt both originate from a stamp this same process took, but
// nothing guarantees every path handing them here preserves the monotonic
// reading == would otherwise be sensitive to.
func (t projectionProgressTuple) equal(o projectionProgressTuple) bool {
	return t.lag == o.lag &&
		t.lastProjectedAt.Equal(o.lastProjectedAt) &&
		t.lagProgressAt.Equal(o.lagProgressAt) &&
		t.ackPending == o.ackPending &&
		t.ackFloorProgressAt.Equal(o.ackFloorProgressAt)
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

// SetPersonalHealerPassFunc installs the clock this poller escalates a stale
// personal-sweep verdict from. Must be called before Start. Pass nil to clear
// (the default), which leaves the field entirely untouched. See
// pollPersonalHealerStaleness for the two-writer rule that makes a second writer
// of that field safe.
func (lp *LagPoller) SetPersonalHealerPassFunc(fn PersonalHealerPassFunc) {
	lp.healerPass = fn
}

// SetPeakRowsFunc attaches the pipeline's peak-binding-rows source. Must be
// called before Start. Pass nil to clear (the default).
func (lp *LagPoller) SetPeakRowsFunc(fn PeakRowsFunc) {
	lp.peakRows = fn
}

// SetWithholdCountsFunc attaches the pipeline's withholding tallies. Must be
// called before Start. Pass nil to clear (the default), which leaves both
// fields on the entry entirely untouched.
func (lp *LagPoller) SetWithholdCountsFunc(fn WithholdCountsFunc) {
	lp.withholdCounts = fn
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
		iv = 30 * time.Second // safe default if MetricsInterval was set to an invalid value
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
		tuple := projectionProgressTuple{
			lag: lag, lastProjectedAt: lastProjectedAt, lagProgressAt: lp.lagProgressAt,
			ackPending: ackPending, ackFloorProgressAt: ackFloorProgressAt,
		}
		// Written on the first poll unconditionally (lastWrittenSet false) and
		// whenever any of the five values differs from what was last actually
		// written; a lens sitting at a steady lag/progress costs reads only,
		// mirroring the sweep survey cache's own reasoning in pipeline/sweep.go.
		if !lp.lastWrittenSet || !tuple.equal(lp.lastWritten) {
			if err := lp.reporter.SetProjectionProgress(ctx, lag, lastProjectedAt, lp.lagProgressAt, ackPending, ackFloorProgressAt); err != nil {
				if ctx.Err() == nil {
					slog.Warn("lag poller: SetProjectionProgress failed",
						"ruleId", lp.ruleID, "err", err)
				}
			} else {
				lp.lastWritten = tuple
				lp.lastWrittenSet = true
			}
		}
		lp.pollPeakRows(ctx)
		lp.pollWithholdCounts(ctx)
		lp.pollPersonalHealerStaleness(ctx)
	}
}

// pollPersonalHealerStaleness escalates this lens's stored personalSweepVerdict
// to `stale` once the personal plane's standing healer has gone quiet for longer
// than the derivation licence's own window.
//
// IT EXISTS BECAUSE THE FIELD'S ONLY OTHER WRITER IS THE THING IT REPORTS ON. The
// sweeper writes the verdict at the end of every pass, so a sweeper that stops
// passing — a wedged goroutine, a cancelled context, a ticker that never fires
// — leaves `clean` standing on fifteen lens entries forever, which is the entry
// reading healthy through the exact condition it exists to report. Surfacing that
// needs a writer on a DIFFERENT clock, and this poller is the one per-lens
// periodic writer there is.
//
// THE TWO-WRITER RULE, stated because two writers of one field is a hazard and
// not a pattern: this poller may only ever write the single token `stale`, and
// the sweeper never writes it. So the field has one owner per value — the
// sweeper owns every verdict a pass can reach, this poller owns the absence of
// passes — and the two cannot disagree about a value they never both produce.
// If they interleave (a sweeper pass landing as this poller escalates), the
// later write wins and the next cycle re-derives: the escalation is level-
// triggered off the healer's own clock, not edge-triggered off a transition, so
// it converges either way.
//
// The write is latched, so a healer stalled for an hour costs one write rather
// than one per poll, and unlatched the moment the healer's clock moves — which
// is what lets the sweeper's own next verdict be the next thing on the field.
func (lp *LagPoller) pollPersonalHealerStaleness(ctx context.Context) {
	if lp.healerPass == nil {
		return
	}
	lastPassAt, interval, staleCycles, ok := lp.healerPass()
	if !ok {
		return
	}
	if !lastPassAt.Equal(lp.lastSeenPassAt) {
		// The healer's clock moved: whatever it wrote is current, and this
		// poller has nothing to add until the clock stops again.
		lp.lastSeenPassAt = lastPassAt
		lp.staleWritten = false
		return
	}
	if lastPassAt.IsZero() || interval <= 0 || staleCycles <= 0 {
		// A healer that has never completed a pass already publishes
		// `never-passed`, and a cadence this poller cannot read is not a window
		// it may judge against.
		return
	}
	if lp.staleWritten || time.Since(lastPassAt) <= time.Duration(staleCycles)*interval {
		return
	}
	if err := lp.reporter.SetPersonalSweepVerdict(ctx, PersonalSweepVerdictStale); err != nil {
		if ctx.Err() == nil {
			slog.Warn("lag poller: could not escalate the personal sweep verdict to stale",
				"ruleId", lp.ruleID, "err", err)
		}
		return
	}
	lp.staleWritten = true
	slog.Warn("lag poller: the personal plane's standing healer has not completed a pass inside the derivation licence's window — this lens's health entry now reads stale, and the licence refuses",
		"ruleId", lp.ruleID, "lastPassAt", lastPassAt, "window", time.Duration(staleCycles)*interval)
}

// pollPeakRows publishes the lens's peak-binding-rows gauge onto its health
// entry. It is a separate read-modify-write from SetProjectionProgress because
// it is a separate concern with its own source and its own "nothing to say"
// case: a nil source or an empty observation window writes nothing at all,
// leaving whatever the entry already holds — the alternative, folding a
// fabricated zero into the progress write, would erase a real peak every time a
// lens went quiet. It applies the same unchanged-value skip as the progress
// write: a peak that has not moved since the last successful write costs a
// read, not a Health-KV round trip.
func (lp *LagPoller) pollPeakRows(ctx context.Context) {
	if lp.peakRows == nil {
		return
	}
	rows, ok := lp.peakRows()
	if !ok {
		return
	}
	if lp.lastPeakRowsSet && rows == lp.lastPeakRows {
		return
	}
	if err := lp.reporter.SetPeakBindingRows(ctx, rows); err != nil {
		if ctx.Err() == nil {
			slog.Warn("lag poller: SetPeakBindingRows failed",
				"ruleId", lp.ruleID, "err", err)
		}
		return
	}
	lp.lastPeakRows = rows
	lp.lastPeakRowsSet = true
}

// pollWithholdCounts publishes the lens's two withholding tallies onto its
// health entry. A separate read-modify-write from the progress and gauge writes
// for the same reason those are separate from each other: its own source, its
// own "nothing to say" case, and the same unchanged-value skip, so a lens whose
// counters have stopped moving costs a read rather than a Health-KV round trip.
//
// "Nothing to say" is two states and both are silence, mirroring
// SetPeakBindingRows' refusal to write without a sample: no source wired (the
// host never called the setter), and a source reporting ok == false (a lens
// that cannot withhold). Writing a zero for either would put a measurement on
// the entry that no reading produced.
func (lp *LagPoller) pollWithholdCounts(ctx context.Context) {
	if lp.withholdCounts == nil {
		return
	}
	withheld, failures, ok := lp.withholdCounts()
	if !ok {
		return
	}
	if lp.lastWithholdSet && withheld == lp.lastWithheld && failures == lp.lastWithholdFailures {
		return
	}
	if err := lp.reporter.SetWithholdCounts(ctx, withheld, failures); err != nil {
		if ctx.Err() == nil {
			slog.Warn("lag poller: SetWithholdCounts failed",
				"ruleId", lp.ruleID, "err", err)
		}
		return
	}
	lp.lastWithheld, lp.lastWithholdFailures = withheld, failures
	lp.lastWithholdSet = true
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
