package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/healthkv"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// externalDurable is the bridge's single fixed durable consumer name on the
// core-events stream, filtered to events.external.>.
const externalDurable = "bridge-external"

// externalFilterSubject is the subject filter for the external-call event
// consumer. The external domain is ordinary (the open <domain>.<eventName>
// model — no Processor allowlist); every external.<adapter> event lands here.
const externalFilterSubject = "events.external.>"

// Config parameterizes the engine. Bucket/stream names default to the
// platform-standard values; callers (cmd/bridge, tests) override only what they
// need.
type Config struct {
	// EventsStream is the core-events stream the external-call consumer attaches
	// to. Default "core-events".
	EventsStream string
	// HealthKVBucket holds the Contract #5 heartbeat (health.bridge.<instance>)
	// and the per-consumer pause-state entries. Default "health-kv".
	HealthKVBucket string
	// ActorKey is the identity:bridge service-actor vertex key the result ops are
	// submitted under (vtx.identity.<id>, the primordial bridge service actor).
	ActorKey string
	// Lane is the ops lane result ops are submitted on. Default "system".
	Lane string
	// Instance distinguishes this engine process; it is the key segment for this
	// process's Contract #5 heartbeat (health.bridge.<instance>) and per-consumer
	// pause-state entries. MUST be unique per bridge process sharing a health-kv
	// bucket. Defaults to "<hostname>-<pid>-<NanoID>" (sanitized) when empty.
	Instance string
	// HeartbeatEvery is the Contract #5 heartbeat cadence. The 10s default is the
	// §5.6/NFR-O1 production cadence; a shorter value lets a test observe
	// heartbeat-driven state without waiting out production timing. Values <= 0
	// take the default.
	HeartbeatEvery time.Duration
	// RedeliveryDelay is the floor applied to the external-call consumer when the
	// handler returns NakWithDelay (a transient adapter failure or a publish
	// failure). Zero leaves the substrate default. A short value lets a test
	// observe redelivery-driven recovery without waiting out the production floor.
	RedeliveryDelay time.Duration
	// SkipOnRedelivery enables the optional skip-on-redelivery probe (mechanism
	// #3): before dispatching, ask the lattice.op.status RPC for the
	// deterministic result-op requestId and skip the adapter call if the
	// result already landed. It is a pure optimization — correctness holds
	// via the deterministic requestId + the adapter's idempotencyKey dedup
	// WITHOUT it. Defaults to true; callers (tests) may disable it to prove
	// correctness without it.
	SkipOnRedelivery *bool
	// CoreSchedulesStream is the platform message-scheduling stream the poll/
	// timeout lane binds to and arms @at schedules on (Contract #10 §10.4). The
	// fired consumer attaches here; the actuator publishes schedule.bridge.* here.
	// Default "core-schedules" — kept literal, like the other defaults, so
	// internal/bridge does not import internal/bootstrap.
	CoreSchedulesStream string
	// PollInterval is the cadence between vendor polls for a pending external call:
	// the first poll is armed at now+PollInterval, and a still-pending poll re-arms
	// at now+PollInterval (the self-rescheduling @at chain). Values <= 0 take the
	// default. The 30s default is a sane hours-scale-vendor cadence; a test
	// shortens it to fire promptly.
	PollInterval time.Duration
	// CallDeadline is the give-up horizon for a pending external call: the timeout
	// schedule fires at now+CallDeadline and posts a terminal failed reply if the
	// call has not resolved. Values <= 0 take the default. The 24h default outlasts
	// a typical vendor SLA (the bridge timeout is the SLA give-up; Loom's
	// per-instance deadline is the dead-bridge backstop). A test shortens it.
	CallDeadline time.Duration
	// Logger is the diagnostics sink. Defaults to slog.Default().
	Logger *slog.Logger
}

// defaultPollInterval and defaultCallDeadline are the poll-lane horizons applied
// when Config leaves them unset. The poll cadence is short relative to the
// give-up deadline, which is sized to outlast a vendor SLA.
const (
	defaultPollInterval = 30 * time.Second
	defaultCallDeadline = 24 * time.Hour
)

// instanceSegmentReplacer sanitizes a hostname for use as a KV key segment
// (Contract #5 health.bridge.<instance>): '.' would be read as a key-segment
// separator and is replaced with '-'.
var instanceSegmentReplacer = strings.NewReplacer(".", "-")

// defaultInstance returns a host/pid-attributable, per-construction-unique
// instance id ("<hostname>-<pid>-<NanoID>", sanitized for KV key segments) used
// when Config.Instance is empty.
func defaultInstance() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "bridge"
	}
	host = instanceSegmentReplacer.Replace(host)
	suffix, err := substrate.NewNanoID()
	if err != nil {
		suffix = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return host + "-" + strconv.Itoa(os.Getpid()) + "-" + suffix
}

func (c *Config) withDefaults() {
	if c.EventsStream == "" {
		c.EventsStream = "core-events"
	}
	if c.HealthKVBucket == "" {
		c.HealthKVBucket = "health-kv"
	}
	if c.Lane == "" {
		c.Lane = "system"
	}
	if c.Instance == "" {
		c.Instance = defaultInstance()
	}
	if c.SkipOnRedelivery == nil {
		on := true
		c.SkipOnRedelivery = &on
	}
	if c.CoreSchedulesStream == "" {
		c.CoreSchedulesStream = "core-schedules"
	}
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.CallDeadline <= 0 {
		c.CallDeadline = defaultCallDeadline
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Engine is the bridge: the platform's generic, trusted-infra egress. It is a
// stateless dispatcher — a durable consumer on events.external.> that, per
// event, dispatches to a named registered adapter (idempotencyKey = the opaque
// instanceKey) and posts a result op back to ops.<lane> with a deterministic
// requestId. It keeps no durable outbox: the message ack is the commit point,
// and the deterministic reply requestId makes redelivery idempotent, so its only
// durable state is the consumer ack floor and the Contract #4 op tracker (owned
// by the Processor). It imports internal/substrate for all cross-component
// interaction over NATS, plus internal/vault for the DecryptRequest/Response
// wire types of the lattice.vault.decrypt RPC its egress-unwrap boundary calls
// (design sensitive-param-egress §3.5) — the bridge reads no Core KV; its
// transport surfaces are the vault.decrypt RPC, one lens-bucket read (P5), and
// the lattice.op.status RPC its skip-on-redelivery probe calls instead of a
// direct KVGet (op-status-read-surface-design.md Fire 1).
type Engine struct {
	cfg        Config
	conn       *substrate.Conn
	logger     *slog.Logger
	registry   *Registry
	act        *actuator
	supervisor *substrate.ConsumerSupervisor
	states     *healthkv.ConsumerStateCache
	issues     *issueCache
	metrics    *dispatchMetrics
}

// NewEngine constructs an Engine over conn.
func NewEngine(conn *substrate.Conn, cfg Config) *Engine {
	cfg.withDefaults()
	return &Engine{
		cfg:        cfg,
		conn:       conn,
		logger:     cfg.Logger,
		registry:   NewRegistry(),
		act:        newActuator(conn, cfg.Lane, cfg.ActorKey),
		supervisor: substrate.NewConsumerSupervisor(conn),
		states:     healthkv.NewConsumerStateCache(),
		issues:     newIssueCache(),
		metrics:    newDispatchMetrics(),
	}
}

// RegisterAdapter binds an adapter name to a concrete Adapter, the seam that
// wires the reference adapters (cmd/bridge demo, tests) before Start. A
// duplicate name or nil adapter is rejected by the registry (a wiring bug,
// surfaced). MUST be called before Start: the registry has no lock-step with the
// dispatch path, so registering after an event has already dispatched would be a
// race against Lookup.
func (e *Engine) RegisterAdapter(name string, a Adapter) error {
	return e.registry.Register(name, a)
}

// Start runs the engine until ctx is cancelled. It starts the fixed
// events.external.> consumer and the Contract #5 heartbeater, then blocks on
// ctx. There is no startup index rebuild and no durable bucket to recover: a
// redelivered external event is re-dispatched idempotently (the deterministic
// result-op requestId collapses any duplicate on the Contract #4 tracker).
//
// An empty ActorKey is a fatal misconfiguration: result ops carry the bridge's
// identity:bridge service-actor key, and an empty actor publishes ops the
// Processor rejects off-stream with no signal. Start fails LOUD here — before
// any consumer attaches — rather than fabricating an identity; there is no
// sensible default for an identity key.
func (e *Engine) Start(ctx context.Context) (err error) {
	if e.cfg.ActorKey == "" {
		return fmt.Errorf("bridge: ActorKey required")
	}

	defer func() {
		if err != nil {
			e.supervisor.Stop()
		}
	}()

	if err := e.supervisor.Add(ctx, e.externalSpec()); err != nil {
		return fmt.Errorf("bridge: add %s consumer: %w", externalDurable, err)
	}

	// The poll/timeout lane: a fixed durable on core-schedules that drives a
	// pending external call's resolution (poll the vendor) or its give-up
	// (timeout). Armed schedules are published by handlePending on a Pending
	// outcome; this consumer fires them.
	if err := e.supervisor.Add(ctx, e.scheduleSpec()); err != nil {
		return fmt.Errorf("bridge: add %s consumer: %w", scheduleConsumerName, err)
	}

	hb := newHeartbeater(e.conn, e.cfg.HealthKVBucket, e.cfg.Instance, e.cfg.HeartbeatEvery, e.states, e.issues, e.metrics, e.logger)
	go hb.run(ctx)

	e.logger.Info("bridge engine started",
		"events", e.cfg.EventsStream, "schedules", e.cfg.CoreSchedulesStream, "lane", e.cfg.Lane,
		"skipOnRedelivery", *e.cfg.SkipOnRedelivery, "pollInterval", e.cfg.PollInterval, "callDeadline", e.cfg.CallDeadline)
	<-ctx.Done()
	e.supervisor.Stop()
	return nil
}

// externalSpec describes the fixed external-call consumer (bridge-external) on
// events.external.>.
func (e *Engine) externalSpec() substrate.ConsumerSpec {
	return substrate.ConsumerSpec{
		Name:            externalDurable,
		Stream:          e.cfg.EventsStream,
		FilterSubject:   externalFilterSubject,
		DeliverPolicy:   substrate.DeliverAll,
		RedeliveryDelay: e.cfg.RedeliveryDelay,
		Handler:         supervisedHandler(e.handleExternal),
		Health:          e.healthSinkFor(externalDurable),
		Logger:          e.logger,
	}
}

// supervisedHandler adapts a Decision-returning handler to the supervisor's
// SupervisedHandler signature. handleExternal encodes every outcome as a
// Decision, so the error channel is always nil and Classify is never exercised.
func supervisedHandler(h func(context.Context, substrate.Message) substrate.Decision) substrate.SupervisedHandler {
	return func(ctx context.Context, msg substrate.Message) (substrate.Decision, error) {
		return h(ctx, msg), nil
	}
}

// healthSinkFor builds a per-consumer HealthSink that persists pause-state to
// health-kv and feeds the engine's consumer-state cache.
func (e *Engine) healthSinkFor(name string) substrate.HealthSink {
	return healthkv.NewConsumerSink(e.conn, e.cfg.HealthKVBucket, "bridge", name, e.states)
}
