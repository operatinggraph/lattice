// Package modelrunner is the engine behind cmd/model-runner: a NATS micro
// service that turns a queued request into exactly one external model call and
// records the outcome in a KV bucket.
//
// It is the platform's sole holder of a third-party model credential and its
// sole external-model egress. Everything about that posture is deliberate:
//
//   - It acks the moment it has taken the work, never after the model answers.
//     A caller's JetStream consumer must not sit blocked for a minutes-long
//     turn, so the reply carries only accepted/busy/invalid and the answer
//     lands later in wire.ResultsBucket under the caller's own ref.
//   - It spends at most once per ref. A CAS-created in-flight marker at the
//     result key is the guard: a redelivered dispatch finds it and acks
//     without calling the vendor.
//   - It is domain-free. Requests name a model, prompts, and one tool; the
//     runner forces the model through that tool and passes the tool input
//     back verbatim. It never parses, validates, or interprets the payload —
//     that judgement belongs to whoever asked.
//   - It touches no Core KV and submits no operations. Its only writes are its
//     own result bucket and its Health KV heartbeat.
package modelrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/micro"

	"github.com/operatinggraph/lattice/internal/modelrunner/wire"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	// DefaultMaxConcurrent is how many vendor calls one instance runs at once.
	// Small on purpose: horizontal scale is what the queue group is for, and a
	// tight per-instance bound keeps an unexpected burst from becoming an
	// unexpected bill.
	DefaultMaxConcurrent = 2

	// DefaultDailyCap bounds calls per UTC day across the whole fleet (the
	// counter lives in the shared bucket, not in memory). It applies ONLY when
	// MODEL_RUNNER_DAILY_CAP is unset — an explicit "0" is a hard block, never
	// a fall-through to this default (see Config.DailyCap).
	DefaultDailyCap = 20

	// MaxRequestBytes bounds the serialized request the engine will accept.
	// Defense in depth, ahead of the adapter's own prompt sizing: an oversized
	// prompt/system/tool payload is rejected as invalid at the door rather than
	// carried all the way to a vendor call that would bill for it. Set well
	// under NATS's default 1 MiB max_payload so a request that reaches the
	// handler at all is already within a sane bound.
	MaxRequestBytes = 512 * 1024

	// DefaultResultTTL is how long a terminal result stays readable. Long
	// enough that a caller which was down for a weekend still finds its answer.
	DefaultResultTTL = 7 * 24 * time.Hour

	// DefaultVendorTimeout bounds one model call. Adaptive-thinking turns can
	// run for minutes; this is the wall past which the call is abandoned and
	// recorded as failed.
	DefaultVendorTimeout = 10 * time.Minute

	// DefaultMaxTokens is both the default and the ceiling for a request's
	// MaxTokens — a request asking for more is clamped, never honoured.
	DefaultMaxTokens int64 = 16384

	// DefaultModel answers a request that names no model. Authoring work is
	// intelligence-sensitive and low-frequency, so the default is the strong
	// model rather than the cheap one.
	DefaultModel = "claude-opus-5"

	// usageKeyPrefix namespaces the runner's own bookkeeping inside the result
	// bucket. wire.ValidRef forbids the underscore precisely so no caller ref
	// can ever address a key under this prefix.
	usageKeyPrefix = "__usage."

	// casAttempts bounds the daily-counter read-modify-write retry loop. The
	// contention window is one instance's worker start, so a handful of
	// attempts covers a fleet far larger than this one will ever be.
	casAttempts = 8
)

// VendorRequest is one model call as the engine describes it — the wire
// request minus the bookkeeping, with caps already applied.
type VendorRequest struct {
	Model     string
	MaxTokens int64
	System    string
	Prompt    string
	Tool      wire.Tool
}

// VendorResult is what a vendor call yields. A policy refusal is a result, not
// an error: it is terminal and carries usage that was really spent, so it must
// travel the same path as a completion.
type VendorResult struct {
	// Output is the forced tool's input JSON, exactly as the vendor emitted it.
	Output json.RawMessage
	// Model is what actually answered, read back off the response — the
	// provenance record, which is why the runner enables no fallback routing.
	Model           string
	InputTokens     int64
	OutputTokens    int64
	Refused         bool
	RefusalCategory string
}

// VendorCaller performs one model call. The engine owns capacity, caps,
// idempotency, and recording; an implementation owns only the round trip. The
// seam exists so the engine is testable without a network or a credential.
type VendorCaller interface {
	Generate(ctx context.Context, req VendorRequest) (VendorResult, error)
}

// Config configures an Engine.
type Config struct {
	// Conn is the runner's NATS connection. It serves the micro endpoint from
	// Conn.NATS() and writes results through the typed KV helpers.
	Conn *substrate.Conn
	// Vendor performs the model call. Required.
	Vendor VendorCaller

	// Bucket is the result bucket. Defaults to wire.ResultsBucket.
	Bucket string
	// Subject is the request subject. Defaults to wire.GenerateSubject.
	Subject string
	// QueueGroup is the load-balancing group. Defaults to wire.QueueGroup.
	QueueGroup string
	// Version is the micro service version string.
	Version string

	// MaxConcurrent bounds simultaneous vendor calls on this instance.
	MaxConcurrent int
	// DailyCap bounds fleet-wide vendor calls per UTC day:
	//   > 0   cap at N calls/day
	//   == 0  block ALL calls — the fail-safe zero value, and what
	//         MODEL_RUNNER_DAILY_CAP=0 means: the deliberate "stop spending"
	//         switch, never "no limit"
	//   < 0   disabled (no cap) — reachable only programmatically; the
	//         environment cannot produce a negative, so spending can never be
	//         silently turned off from a deployment.
	// New() does NOT rewrite the zero value, so a Config built without a
	// DailyCap blocks rather than spends.
	DailyCap int
	// ResultTTL is the terminal result's lifetime.
	ResultTTL time.Duration
	// InflightTTL is the in-flight marker's lifetime — the window in which a
	// ref is considered "someone is already spending on this". It must exceed
	// VendorTimeout, and it is deliberately much shorter than ResultTTL: a
	// runner killed mid-call leaves a marker behind, and that marker is the
	// only thing standing between the caller and a retry.
	InflightTTL time.Duration
	// VendorTimeout bounds one model call.
	VendorTimeout time.Duration
	// MaxTokens is the per-call output ceiling requests are clamped to.
	MaxTokens int64

	// RedactStrings are literals scrubbed from every error the engine records
	// or logs — the vendor credential above all. The engine never uses them as
	// credentials; it only refuses to repeat them.
	RedactStrings []string

	Logger *slog.Logger

	// now is a clock seam for tests.
	now func() time.Time
}

// Metrics is the engine's counter set, reported through Health KV.
type Metrics struct {
	AcceptedTotal      int64 `json:"acceptedTotal"`
	BusyTotal          int64 `json:"busyTotal"`
	InvalidTotal       int64 `json:"invalidTotal"`
	RejectedTotal      int64 `json:"rejectedTotal"`
	DedupTotal         int64 `json:"dedupTotal"`
	CompletedTotal     int64 `json:"completedTotal"`
	RefusedTotal       int64 `json:"refusedTotal"`
	FailedTotal        int64 `json:"failedTotal"`
	VendorInputTokens  int64 `json:"vendorInputTokens"`
	VendorOutputTokens int64 `json:"vendorOutputTokens"`
	InFlight           int64 `json:"inFlight"`
	DailyCount         int64 `json:"dailyCount"`
}

// Engine serves the model-call endpoint.
type Engine struct {
	cfg Config
	log *slog.Logger

	svc  micro.Service
	sem  chan struct{}
	wg   sync.WaitGroup
	base context.Context

	accepted, busy, invalid, dedup      atomic.Int64
	rejected                            atomic.Int64
	completed, refused, failed          atomic.Int64
	inputTokens, outputTokens, inFlight atomic.Int64
	lastDailyCount                      atomic.Int64
}

// New validates cfg, applies defaults, and returns an Engine that is not yet
// serving.
func New(cfg Config) (*Engine, error) {
	if cfg.Conn == nil {
		return nil, errors.New("modelrunner: Conn is required")
	}
	if cfg.Vendor == nil {
		return nil, errors.New("modelrunner: Vendor is required")
	}
	if cfg.Bucket == "" {
		cfg.Bucket = wire.ResultsBucket
	}
	if cfg.Subject == "" {
		cfg.Subject = wire.GenerateSubject
	}
	if cfg.QueueGroup == "" {
		cfg.QueueGroup = wire.QueueGroup
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = DefaultMaxConcurrent
	}
	if cfg.ResultTTL <= 0 {
		cfg.ResultTTL = DefaultResultTTL
	}
	if cfg.VendorTimeout <= 0 {
		cfg.VendorTimeout = DefaultVendorTimeout
	}
	if cfg.InflightTTL <= 0 {
		cfg.InflightTTL = 2 * cfg.VendorTimeout
	}
	if cfg.InflightTTL <= cfg.VendorTimeout {
		return nil, fmt.Errorf("modelrunner: InflightTTL (%s) must exceed VendorTimeout (%s) — "+
			"a marker that expires under a live call reopens the double-spend window",
			cfg.InflightTTL, cfg.VendorTimeout)
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = DefaultMaxTokens
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &Engine{
		cfg:  cfg,
		log:  cfg.Logger,
		sem:  make(chan struct{}, cfg.MaxConcurrent),
		base: context.Background(),
	}, nil
}

// Start registers the micro service endpoint. ctx bounds every vendor call the
// engine goes on to make: cancelling it abandons in-flight work (which the
// in-flight marker's TTL then reopens for retry).
func (e *Engine) Start(ctx context.Context) error {
	e.base = ctx
	svc, err := micro.AddService(e.cfg.Conn.NATS(), micro.Config{
		Name:        wire.ServiceName,
		Version:     e.cfg.Version,
		Description: "Lattice model-runner — queued external model calls, one vendor call per ref",
		QueueGroup:  e.cfg.QueueGroup,
		ErrorHandler: func(_ micro.Service, err *micro.NATSError) {
			e.log.Error("model-runner: subscription error", "subject", err.Subject, "error", err.Description)
		},
	})
	if err != nil {
		return fmt.Errorf("modelrunner: add micro service: %w", err)
	}
	if err := svc.AddEndpoint("generate",
		micro.HandlerFunc(e.handle),
		micro.WithEndpointSubject(e.cfg.Subject),
		micro.WithEndpointQueueGroup(e.cfg.QueueGroup),
	); err != nil {
		_ = svc.Stop()
		return fmt.Errorf("modelrunner: add endpoint %s: %w", e.cfg.Subject, err)
	}
	e.svc = svc
	return nil
}

// Stop unsubscribes the endpoint and waits for in-flight workers to finish or
// abandon. Callers cancel Start's context first when they want the calls
// themselves cut short.
func (e *Engine) Stop() error {
	var err error
	if e.svc != nil {
		err = e.svc.Stop()
		e.svc = nil
	}
	e.wg.Wait()
	return err
}

// Wait blocks until every worker started so far has finished. A worker is
// registered before its request is acked, so a caller holding an "accepted"
// ack is guaranteed to be waiting on that request's work.
func (e *Engine) Wait() { e.wg.Wait() }

// VendorTimeout is the per-call ceiling — the bound a graceful drain gives an
// in-flight call to land before it is force-cancelled (cmd/model-runner).
func (e *Engine) VendorTimeout() time.Duration { return e.cfg.VendorTimeout }

// handle answers one request. Everything here is cheap and local: reject a
// hostile reply subject, bound the size, decode, validate, take a slot, claim
// the ref, then check the cap. The vendor call runs on a worker after the
// reply has already gone out.
func (e *Engine) handle(req micro.Request) {
	// Reply-subject guard (SECURITY). On nats-server 2.14 allow_responses
	// grants a one-shot publish to the REQUESTOR-CHOSEN reply subject that
	// overrides the deny list. A caller could otherwise set reply to a
	// JetStream admin subject (e.g. $JS.API.STREAM.DELETE.KV_core-kv) and our
	// ack would land there and execute it — the confused-deputy vector. Every
	// legitimate caller uses nats.go RequestWithContext, whose reply subject is
	// always an "_INBOX." inbox (the per-identity Edge prefix "_INBOX.edge.<U>"
	// still matches). Anything else we drop in silence: responding at all IS
	// the attack, so we must not respond.
	if reply := req.Reply(); !strings.HasPrefix(reply, "_INBOX.") {
		e.rejected.Add(1)
		e.log.Warn("model-runner: dropping request with non-inbox reply subject (not responding)", "reply", reply)
		return
	}

	// Size bound (defense in depth). Reject an oversized payload before it can
	// reach a vendor call. len(req.Data()) is the authoritative serialized size
	// — checking it here avoids a re-marshal in validate.
	if len(req.Data()) > MaxRequestBytes {
		e.respondInvalid(req, "", fmt.Sprintf("request exceeds the %d-byte maximum", MaxRequestBytes))
		return
	}

	var r wire.Request
	if err := json.Unmarshal(req.Data(), &r); err != nil {
		e.respondInvalid(req, "", "malformed request body")
		return
	}
	if reason := validate(r); reason != "" {
		e.respondInvalid(req, r.Ref, reason)
		return
	}

	select {
	case e.sem <- struct{}{}:
	default:
		e.respondBusy(req, r.Ref, "no worker slot available")
		return
	}

	ctx, cancel := context.WithTimeout(e.base, 10*time.Second)
	defer cancel()

	now := e.cfg.now().UTC()
	marker := wire.Result{State: wire.StateInflight, Ref: r.Ref, StartedAt: now.Format(time.RFC3339Nano)}
	body, err := json.Marshal(marker)
	if err != nil {
		<-e.sem
		e.respondBusy(req, r.Ref, "could not record in-flight marker")
		return
	}

	// Claim the ref FIRST, before any cap check. A ref that already exists is a
	// redelivery of work already accounted for: it must always ack accepted and
	// cost nothing — never get a "busy" at the cap that makes the caller Nak
	// for hours over a result already sitting in KV.
	rev, err := e.cfg.Conn.KVCreateWithTTL(ctx, e.cfg.Bucket, r.Ref, body, e.cfg.InflightTTL)
	switch {
	case errors.Is(err, substrate.ErrRevisionConflict):
		// The double-spend guard doing its job on a redelivery: in flight or
		// already answered. Ack accepted, spend nothing, skip the cap entirely.
		<-e.sem
		e.dedup.Add(1)
		e.accepted.Add(1)
		e.respond(req, wire.Ack{Status: wire.AckAccepted, Ref: r.Ref})
		return
	case err != nil:
		<-e.sem
		e.log.Error("model-runner: in-flight marker write failed", "ref", r.Ref, "error", e.redactErr(err))
		e.respondBusy(req, r.Ref, "result store unavailable")
		return
	}

	// The cap gates only NEW refs. The counter itself only moves when a call
	// actually starts (noteCall in run), so the ceiling is on spend, not on
	// attempts; concurrent claims can overshoot by at most the fleet's
	// in-flight width, accepted over the cost of a global per-attempt lease. On
	// over-cap we undo the claim so the same ref is retryable when the day
	// rolls, exactly as if it had never been taken.
	if over, count := e.overDailyCap(ctx); over {
		if delErr := e.cfg.Conn.KVDeleteRevision(ctx, e.cfg.Bucket, r.Ref, rev); delErr != nil {
			e.log.Warn("model-runner: could not release over-cap marker", "ref", r.Ref, "error", e.redactErr(delErr))
		}
		<-e.sem
		e.respondBusy(req, r.Ref, "daily call cap reached")
		e.log.Warn("model-runner: daily cap reached", "ref", r.Ref, "count", count, "cap", e.cfg.DailyCap)
		return
	}

	// Registered before the ack so a caller that sees "accepted" and then
	// waits is never racing the worker's registration.
	e.wg.Add(1)
	e.inFlight.Add(1)
	e.accepted.Add(1)
	e.respond(req, wire.Ack{Status: wire.AckAccepted, Ref: r.Ref})

	go func() {
		defer e.wg.Done()
		defer e.inFlight.Add(-1)
		defer func() { <-e.sem }()
		// Recover FIRST (declared last, runs first): an SDK panic must record a
		// failure for this ref and let the process keep serving, never take the
		// whole runner down and strand every other in-flight ref until its TTL.
		defer e.recoverWorker(r, rev)
		e.run(r, rev)
	}()
}

// recoverWorker turns a panicking vendor call into a recorded failure. Without
// it a single SDK panic kills the process and every in-flight ref waits out
// its InflightTTL before any retry can run.
func (e *Engine) recoverWorker(r wire.Request, markerRev uint64) {
	rec := recover()
	if rec == nil {
		return
	}
	e.failed.Add(1)
	e.log.Error("model-runner: worker panicked; recording failure and continuing",
		"ref", r.Ref, "panic", Redact(fmt.Sprint(rec), e.cfg.RedactStrings...))
	out := wire.Result{
		Ref:         r.Ref,
		State:       wire.StateFailed,
		Error:       "internal error while calling the model",
		CompletedAt: e.cfg.now().UTC().Format(time.RFC3339Nano),
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(e.base), 10*time.Second)
	defer cancel()
	e.recordTerminal(ctx, r.Ref, out, markerRev)
}

// run performs the vendor call for a claimed ref and records the terminal
// result.
func (e *Engine) run(r wire.Request, markerRev uint64) {
	ctx, cancel := context.WithTimeout(e.base, e.cfg.VendorTimeout)
	defer cancel()

	// Counted at the moment spend begins, not when it ends: a call that times
	// out or fails still cost money.
	e.noteCall()

	maxTokens := r.MaxTokens
	if maxTokens <= 0 || maxTokens > e.cfg.MaxTokens {
		maxTokens = e.cfg.MaxTokens
	}
	model := r.Model
	if model == "" {
		model = DefaultModel
	}

	res, err := e.cfg.Vendor.Generate(ctx, VendorRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    r.System,
		Prompt:    r.Prompt,
		Tool:      r.Tool,
	})

	out := wire.Result{
		Ref:         r.Ref,
		Model:       res.Model,
		Usage:       wire.Usage{InputTokens: res.InputTokens, OutputTokens: res.OutputTokens},
		CompletedAt: e.cfg.now().UTC().Format(time.RFC3339Nano),
	}
	switch {
	case err != nil:
		out.State = wire.StateFailed
		out.Error = e.redactErr(err)
		e.failed.Add(1)
	case res.Refused:
		out.State = wire.StateRefused
		out.RefusalCategory = res.RefusalCategory
		e.refused.Add(1)
	default:
		out.State = wire.StateCompleted
		out.Output = res.Output
		e.completed.Add(1)
	}
	e.inputTokens.Add(res.InputTokens)
	e.outputTokens.Add(res.OutputTokens)

	// The terminal write is detached from the call's context: a call that was
	// cancelled or timed out still has an outcome worth recording, and losing
	// it would strand the caller on a marker until it expires.
	writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(e.base), 10*time.Second)
	defer writeCancel()
	e.recordTerminal(writeCtx, r.Ref, out, markerRev)
}

// recordTerminal replaces the in-flight marker with the terminal result,
// re-arming the key onto the long result TTL.
func (e *Engine) recordTerminal(ctx context.Context, ref string, out wire.Result, markerRev uint64) {
	body, err := json.Marshal(out)
	if err != nil {
		e.log.Error("model-runner: marshal result failed", "ref", ref, "error", e.redactErr(err))
		return
	}
	_, err = e.cfg.Conn.KVUpdateWithTTL(ctx, e.cfg.Bucket, ref, body, markerRev, e.cfg.ResultTTL)
	if errors.Is(err, substrate.ErrRevisionConflict) || errors.Is(err, substrate.ErrKeyNotFound) {
		// The marker expired under a long call, or was otherwise replaced.
		// Recreate rather than lose the outcome; a second conflict means
		// another runner has legitimately re-taken the ref, so leave it alone.
		if _, cErr := e.cfg.Conn.KVCreateWithTTL(ctx, e.cfg.Bucket, ref, body, e.cfg.ResultTTL); cErr != nil {
			e.log.Error("model-runner: result write lost — marker gone and key re-taken",
				"ref", ref, "state", string(out.State), "error", e.redactErr(cErr))
		}
		return
	}
	if err != nil {
		e.log.Error("model-runner: result write failed", "ref", ref, "state", string(out.State), "error", e.redactErr(err))
	}
}

// usageKey is the fleet-shared spend counter for one UTC day.
func (e *Engine) usageKey(now time.Time) string {
	return usageKeyPrefix + now.UTC().Format("2006-01-02")
}

type usageCounter struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

// errCorruptCounter marks a usage-counter value that exists but does not decode
// — a should-never-happen for a key only the runner writes, so it is treated as
// tampering with a spend control rather than as a transient blip.
var errCorruptCounter = errors.New("modelrunner: usage counter value is corrupt")

// overDailyCap reports whether today's counter has reached the cap.
//
// The cap's own value carries three meanings (see Config.DailyCap): a negative
// cap disables the gate, a zero cap blocks every call, a positive cap compares.
//
// Read failures split by kind, because "fail open vs fail closed" is not one
// policy for a spend control:
//   - A TRANSIENT read error (KV unreachable) fails OPEN: the runner is a spend
//     gate, not an availability gate, and one KV blip must not become a self-
//     inflicted outage. The per-call MaxTokens ceiling and worker-slot bound
//     still hold.
//   - A CORRUPT counter fails CLOSED: the value only the runner writes did not
//     decode, so the spend total is untrustworthy. Block this call AND reset
//     the counter to the cap so the corruption self-heals to a known-blocked
//     state for the rest of the day rather than persisting unreadable.
func (e *Engine) overDailyCap(ctx context.Context) (bool, int64) {
	switch {
	case e.cfg.DailyCap < 0:
		return false, 0 // disabled
	case e.cfg.DailyCap == 0:
		return true, 0 // hard block
	}
	now := e.cfg.now()
	count, rev, err := e.readUsage(ctx, now)
	if err != nil {
		if errors.Is(err, errCorruptCounter) {
			e.healCorruptCounter(ctx, now, rev)
			e.log.Error("model-runner: usage counter corrupt; blocking this call and resetting to cap", "error", e.redactErr(err))
			return true, int64(e.cfg.DailyCap)
		}
		e.log.Warn("model-runner: usage counter unreadable; allowing the call (availability over precision)", "error", e.redactErr(err))
		return false, 0
	}
	e.lastDailyCount.Store(count)
	return count >= int64(e.cfg.DailyCap), count
}

// readUsage reads the counter for now's UTC day. A missing key is (0, 0, nil);
// a present-but-undecodable value returns errCorruptCounter WITH the live
// revision, so a caller can CAS-overwrite it to heal. Caller pins now and
// passes it in so a single logical read never straddles midnight (the key and
// the revision come from the same instant).
func (e *Engine) readUsage(ctx context.Context, now time.Time) (count int64, revision uint64, err error) {
	key := e.usageKey(now)
	entry, err := e.cfg.Conn.KVGet(ctx, e.cfg.Bucket, key)
	if errors.Is(err, substrate.ErrKeyNotFound) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	var c usageCounter
	if err := json.Unmarshal(entry.Value, &c); err != nil {
		return 0, entry.Revision, fmt.Errorf("%w: key=%s: %v", errCorruptCounter, key, err)
	}
	return c.Count, entry.Revision, nil
}

// healCorruptCounter overwrites a corrupt counter with a valid one pinned to
// the cap, so the next read succeeds and correctly reports the day as blocked.
// Best-effort: a conflicting write means another instance already replaced it.
func (e *Engine) healCorruptCounter(ctx context.Context, now time.Time, rev uint64) {
	key := e.usageKey(now)
	reset := int64(e.cfg.DailyCap)
	if reset < 1 {
		reset = 1
	}
	body, err := json.Marshal(usageCounter{Day: now.UTC().Format("2006-01-02"), Count: reset})
	if err != nil {
		return
	}
	if rev > 0 {
		_, _ = e.cfg.Conn.KVUpdateWithTTL(ctx, e.cfg.Bucket, key, body, rev, e.cfg.ResultTTL)
		return
	}
	_, _ = e.cfg.Conn.KVCreateWithTTL(ctx, e.cfg.Bucket, key, body, e.cfg.ResultTTL)
}

// noteCall increments the day's counter, create-or-update under CAS so
// concurrent workers across the fleet cannot lose an increment to a lost
// update. now is pinned once — the key, the day, and every readUsage inside the
// loop share one instant, so a read straddling midnight can never write one
// day's key with another day's revision.
func (e *Engine) noteCall() {
	now := e.cfg.now().UTC()
	key := e.usageKey(now)
	day := now.Format("2006-01-02")

	// Detached from the call's own context: the counter must record spend even
	// if the caller's deadline is already tight.
	casCtx, cancel := context.WithTimeout(context.WithoutCancel(e.base), 10*time.Second)
	defer cancel()

	for attempt := 0; attempt < casAttempts; attempt++ {
		count, rev, err := e.readUsage(casCtx, now)
		if err != nil {
			if errors.Is(err, errCorruptCounter) {
				// A corrupt counter must not silently stop counting spend: heal
				// it to a known value (the cap) rather than give up, so the gate
				// self-recovers instead of waving calls through untracked.
				e.healCorruptCounter(casCtx, now, rev)
				e.log.Error("model-runner: usage counter corrupt during increment; reset to cap", "key", key, "error", e.redactErr(err))
				return
			}
			e.log.Warn("model-runner: usage counter read failed", "key", key, "error", e.redactErr(err))
			return
		}
		body, err := json.Marshal(usageCounter{Day: day, Count: count + 1})
		if err != nil {
			return
		}
		if rev == 0 {
			if _, err := e.cfg.Conn.KVCreateWithTTL(casCtx, e.cfg.Bucket, key, body, e.cfg.ResultTTL); err == nil {
				e.lastDailyCount.Store(count + 1)
				return
			} else if !errors.Is(err, substrate.ErrRevisionConflict) {
				e.log.Warn("model-runner: usage counter create failed", "key", key, "error", e.redactErr(err))
				return
			}
			continue
		}
		if _, err := e.cfg.Conn.KVUpdateWithTTL(casCtx, e.cfg.Bucket, key, body, rev, e.cfg.ResultTTL); err == nil {
			e.lastDailyCount.Store(count + 1)
			return
		} else if !errors.Is(err, substrate.ErrRevisionConflict) {
			e.log.Warn("model-runner: usage counter update failed", "key", key, "error", e.redactErr(err))
			return
		}
	}
	e.log.Warn("model-runner: usage counter contended out; this call is uncounted", "key", key)
}

// Metrics returns the counter snapshot for a health heartbeat. DailyCount is
// read live from the shared counter so the number reflects the whole fleet,
// falling back to this instance's last observation if the read fails.
func (e *Engine) Metrics(ctx context.Context) Metrics {
	daily := e.lastDailyCount.Load()
	if count, _, err := e.readUsage(ctx, e.cfg.now()); err == nil {
		daily = count
		e.lastDailyCount.Store(count)
	}
	return Metrics{
		AcceptedTotal:      e.accepted.Load(),
		BusyTotal:          e.busy.Load(),
		InvalidTotal:       e.invalid.Load(),
		RejectedTotal:      e.rejected.Load(),
		DedupTotal:         e.dedup.Load(),
		CompletedTotal:     e.completed.Load(),
		RefusedTotal:       e.refused.Load(),
		FailedTotal:        e.failed.Load(),
		VendorInputTokens:  e.inputTokens.Load(),
		VendorOutputTokens: e.outputTokens.Load(),
		InFlight:           e.inFlight.Load(),
		DailyCount:         daily,
	}
}

func (e *Engine) respond(req micro.Request, ack wire.Ack) {
	if err := req.RespondJSON(ack); err != nil {
		e.log.Warn("model-runner: ack failed", "ref", ack.Ref, "status", string(ack.Status), "error", e.redactErr(err))
	}
}

func (e *Engine) respondBusy(req micro.Request, ref, reason string) {
	e.busy.Add(1)
	e.respond(req, wire.Ack{Status: wire.AckBusy, Ref: ref, Reason: reason})
}

func (e *Engine) respondInvalid(req micro.Request, ref, reason string) {
	e.invalid.Add(1)
	e.respond(req, wire.Ack{Status: wire.AckInvalid, Ref: ref, Reason: reason})
}

// validate returns the reason a request is unusable, or "" when it is fine.
// Every check here is structural — nothing about it depends on what the caller
// is trying to author.
func validate(r wire.Request) string {
	if !wire.ValidRef(r.Ref) {
		return "ref must be a non-empty result-bucket key (letters, digits, '-', '/', '=', '.')"
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return "prompt is required"
	}
	if strings.TrimSpace(r.Tool.Name) == "" {
		return "tool.name is required"
	}
	if len(r.Tool.InputSchema.Properties) == 0 {
		return "tool.inputSchema.properties is required — the model must have a shape to answer in"
	}
	return ""
}

// redactErr renders err for a log line or a recorded result with every
// configured secret literal removed. Vendor SDKs echo request state into error
// strings, so no error text leaves this package unscrubbed.
func (e *Engine) redactErr(err error) string {
	if err == nil {
		return ""
	}
	return Redact(err.Error(), e.cfg.RedactStrings...)
}

// redactedPlaceholder is what a scrubbed secret is replaced with — a fixed
// literal, so a reader can tell redaction happened rather than guessing at a
// truncation.
const redactedPlaceholder = "[REDACTED]"

// Redact removes every occurrence of each secret from s. Empty secrets are
// ignored (replacing "" would shred the string).
//
// The scrub is a single left-to-right pass, never a chain of ReplaceAll:
// chaining lets a later secret match inside text an earlier one already
// replaced, which mangles the placeholder and — worse — can reassemble
// fragments a reader then mistakes for real text. Longest secrets are matched
// first so a secret that contains another is redacted whole.
func Redact(s string, secrets ...string) string {
	pairs := make([]string, 0, len(secrets)*2)
	ordered := append([]string(nil), secrets...)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, secret := range ordered {
		if secret == "" {
			continue
		}
		pairs = append(pairs, secret, redactedPlaceholder)
	}
	if len(pairs) == 0 {
		return s
	}
	return strings.NewReplacer(pairs...).Replace(s)
}
