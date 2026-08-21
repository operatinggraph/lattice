package modelrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/modelrunner/wire"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// fakeVendor stands in for the model API. Nothing in the engine's tests
// touches the network or a credential: the seam exists so CI can prove the
// spend guards without spending.
type fakeVendor struct {
	mu     sync.Mutex
	calls  int
	seen   []VendorRequest
	result VendorResult
	err    error

	// gate, when non-nil, holds every call inside Generate until it is closed
	// — the deterministic stand-in for "a model turn is still running".
	gate chan struct{}
	// started receives once per call entry, so a test can know a worker is
	// occupied without sleeping.
	started chan struct{}
	// panicMsg, when set, makes Generate panic — the deterministic stand-in
	// for an SDK that blows up mid-call.
	panicMsg string
}

func (f *fakeVendor) Generate(ctx context.Context, req VendorRequest) (VendorResult, error) {
	f.mu.Lock()
	f.calls++
	f.seen = append(f.seen, req)
	gate, started := f.gate, f.started
	res, err := f.result, f.err
	panicMsg := f.panicMsg
	f.mu.Unlock()

	if panicMsg != "" {
		panic(panicMsg)
	}
	if started != nil {
		started <- struct{}{}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return VendorResult{}, ctx.Err()
		}
	}
	return res, err
}

func (f *fakeVendor) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeVendor) lastRequest(t *testing.T) VendorRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) == 0 {
		t.Fatal("vendor was never called")
	}
	return f.seen[len(f.seen)-1]
}

const testBucket = "model-results"

// harness starts an embedded NATS server, provisions the result bucket, and
// runs an Engine against it.
type harness struct {
	engine *Engine
	conn   *substrate.Conn
	client *wire.Client
	vendor *fakeVendor
}

func newHarness(t *testing.T, tune func(*Config)) *harness {
	t.Helper()
	_, nc := natsfixture.Server(t)

	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("wrap conn: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         testBucket,
		LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatalf("provision %s: %v", testBucket, err)
	}

	vendor := &fakeVendor{result: VendorResult{
		Output:       json.RawMessage(`{"answer":"ok"}`),
		Model:        "claude-opus-5",
		InputTokens:  11,
		OutputTokens: 7,
	}}
	cfg := Config{
		Conn:   conn,
		Vendor: vendor,
		Bucket: testBucket,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// Cap-agnostic tests must not be blocked by the fail-safe zero value
		// (DailyCap==0 blocks everything); disable the gate by default and let
		// the cap tests opt in with a positive value.
		DailyCap: -1,
	}
	if tune != nil {
		tune(&cfg)
	}
	engine, err := New(cfg)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	t.Cleanup(runCancel)
	if err := engine.Start(runCtx); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Stop() })

	return &harness{
		engine: engine,
		conn:   conn,
		client: wire.NewClient(nc),
		vendor: vendor,
	}
}

func (h *harness) dispatch(t *testing.T, req wire.Request) wire.Ack {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ack, err := h.client.Dispatch(ctx, req)
	if err != nil {
		t.Fatalf("dispatch %s: %v", req.Ref, err)
	}
	return ack
}

func (h *harness) result(t *testing.T, ref string) wire.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entry, err := h.conn.KVGet(ctx, testBucket, ref)
	if err != nil {
		t.Fatalf("read result %s: %v", ref, err)
	}
	var res wire.Result
	if err := json.Unmarshal(entry.Value, &res); err != nil {
		t.Fatalf("decode result %s: %v", ref, err)
	}
	return res
}

func (h *harness) absent(t *testing.T, ref string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := h.conn.KVGet(ctx, testBucket, ref)
	if err == nil {
		t.Fatalf("result %s: want no entry, got one", ref)
	}
	// A ref the KV layer will not even parse as a key is absent by
	// construction — which is the point of rejecting it at the door.
	if !errors.Is(err, substrate.ErrKeyNotFound) && !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("result %s: want ErrKeyNotFound, got %v", ref, err)
	}
}

func goodRequest(ref string) wire.Request {
	return wire.Request{
		Ref:    ref,
		Prompt: "author a weaver target that keeps every lease countersigned",
		Tool: wire.Tool{
			Name:        "emit_artifact",
			Description: "return the authored artifact",
			InputSchema: wire.ToolSchema{
				Properties: map[string]any{"content": map[string]any{"type": "string"}},
				Required:   []string{"content"},
			},
		},
	}
}

func TestHandle_AcceptedRequestCompletesAndRecords(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	ack := h.dispatch(t, goodRequest("ref-happy"))
	if ack.Status != wire.AckAccepted {
		t.Fatalf("ack: want accepted, got %q (%s)", ack.Status, ack.Reason)
	}
	if ack.Ref != "ref-happy" {
		t.Fatalf("ack ref: want ref-happy, got %q", ack.Ref)
	}
	h.engine.Wait()

	res := h.result(t, "ref-happy")
	if res.State != wire.StateCompleted {
		t.Fatalf("state: want completed, got %q (error=%q)", res.State, res.Error)
	}
	if string(res.Output) != `{"answer":"ok"}` {
		t.Fatalf("output: want the tool input verbatim, got %s", res.Output)
	}
	if res.Model != "claude-opus-5" {
		t.Fatalf("model: want the model the vendor reported, got %q", res.Model)
	}
	if res.Usage.InputTokens != 11 || res.Usage.OutputTokens != 7 {
		t.Fatalf("usage: want 11/7, got %d/%d", res.Usage.InputTokens, res.Usage.OutputTokens)
	}
	if res.CompletedAt == "" {
		t.Error("completedAt: want a timestamp, got empty")
	}

	m := h.engine.Metrics(context.Background())
	if m.AcceptedTotal != 1 || m.CompletedTotal != 1 {
		t.Errorf("metrics: want accepted=1 completed=1, got %+v", m)
	}
	if m.VendorInputTokens != 11 || m.VendorOutputTokens != 7 {
		t.Errorf("metrics usage: want 11/7, got %d/%d", m.VendorInputTokens, m.VendorOutputTokens)
	}
	if m.DailyCount != 1 {
		t.Errorf("dailyCount: want 1, got %d", m.DailyCount)
	}
}

// The default model and the token ceiling are the runner's, not the caller's:
// a request that names neither still gets a fully-specified vendor call.
func TestHandle_AppliesDefaultsAndClampsMaxTokens(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	req := goodRequest("ref-clamp")
	req.MaxTokens = DefaultMaxTokens * 4
	h.dispatch(t, req)
	h.engine.Wait()

	got := h.vendor.lastRequest(t)
	if got.Model != DefaultModel {
		t.Errorf("model: want %q, got %q", DefaultModel, got.Model)
	}
	if got.MaxTokens != DefaultMaxTokens {
		t.Errorf("maxTokens: want clamp to %d, got %d", DefaultMaxTokens, got.MaxTokens)
	}
}

func TestHandle_InvalidRequestsAreRejectedWithoutSpending(t *testing.T) {
	t.Parallel()

	reserved := goodRequest("x")
	reserved.Ref = "__usage.2026-08-21"

	noPrompt := goodRequest("ref-noprompt")
	noPrompt.Prompt = "   "

	noSchema := goodRequest("ref-noschema")
	noSchema.Tool.InputSchema.Properties = nil

	noToolName := goodRequest("ref-notool")
	noToolName.Tool.Name = ""

	cases := []struct {
		name string
		req  wire.Request
	}{
		{"empty ref", goodRequest("")},
		{"ref with a wildcard", goodRequest("ref.*")},
		// The runner's own spend counter lives under "__" in the same bucket;
		// a ref able to address it could zero the daily cap.
		{"ref reaching the usage counter", reserved},
		{"blank prompt", noPrompt},
		{"no tool name", noToolName},
		{"no tool schema", noSchema},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, nil)
			ack := h.dispatch(t, tc.req)
			if ack.Status != wire.AckInvalid {
				t.Fatalf("ack: want invalid, got %q", ack.Status)
			}
			if ack.Reason == "" {
				t.Error("invalid ack: want a reason, got empty")
			}
			h.engine.Wait()
			if h.vendor.callCount() != 0 {
				t.Errorf("vendor calls: want 0, got %d", h.vendor.callCount())
			}
			if tc.req.Ref != "" {
				h.absent(t, tc.req.Ref)
			}
			if m := h.engine.Metrics(context.Background()); m.InvalidTotal != 1 || m.AcceptedTotal != 0 {
				t.Errorf("metrics: want invalid=1 accepted=0, got %+v", m)
			}
		})
	}
}

func TestHandle_MalformedBodyIsInvalid(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, err := h.conn.NATS().RequestWithContext(ctx, wire.GenerateSubject, []byte("{not json"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var ack wire.Ack
	if err := json.Unmarshal(msg.Data, &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.Status != wire.AckInvalid {
		t.Fatalf("ack: want invalid, got %q", ack.Status)
	}
	if h.vendor.callCount() != 0 {
		t.Errorf("vendor calls: want 0, got %d", h.vendor.callCount())
	}
}

func TestHandle_BusyWhenEveryWorkerSlotIsHeld(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	h := newHarness(t, func(c *Config) { c.MaxConcurrent = 1 })
	h.vendor.gate, h.vendor.started = gate, started

	if ack := h.dispatch(t, goodRequest("ref-slot-1")); ack.Status != wire.AckAccepted {
		t.Fatalf("first ack: want accepted, got %q", ack.Status)
	}
	<-started // the only slot is now held inside the vendor call

	ack := h.dispatch(t, goodRequest("ref-slot-2"))
	if ack.Status != wire.AckBusy {
		t.Fatalf("second ack: want busy, got %q", ack.Status)
	}
	// Busy must leave no trace: the caller retries the same ref, and a
	// leftover marker would make that retry a permanent no-op.
	h.absent(t, "ref-slot-2")

	close(gate)
	h.engine.Wait()
	if h.vendor.callCount() != 1 {
		t.Errorf("vendor calls: want 1, got %d", h.vendor.callCount())
	}
	if m := h.engine.Metrics(context.Background()); m.BusyTotal != 1 {
		t.Errorf("metrics: want busy=1, got %+v", m)
	}
}

// A redelivered dispatch for a ref whose call is still running must not start a
// second call — the CAS-created marker is the whole double-spend guard.
func TestHandle_RedeliveryWhileInflightSpendsOnce(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	h := newHarness(t, nil)
	h.vendor.gate, h.vendor.started = gate, started

	if ack := h.dispatch(t, goodRequest("ref-dedup")); ack.Status != wire.AckAccepted {
		t.Fatalf("first ack: want accepted, got %q", ack.Status)
	}
	<-started

	ack := h.dispatch(t, goodRequest("ref-dedup"))
	if ack.Status != wire.AckAccepted {
		t.Fatalf("redelivery ack: want accepted, got %q (%s)", ack.Status, ack.Reason)
	}

	close(gate)
	h.engine.Wait()
	if got := h.vendor.callCount(); got != 1 {
		t.Fatalf("vendor calls: want exactly 1 for one ref, got %d", got)
	}
	if m := h.engine.Metrics(context.Background()); m.DedupTotal != 1 || m.AcceptedTotal != 2 {
		t.Errorf("metrics: want dedup=1 accepted=2, got %+v", m)
	}
}

// The same guard must hold after the answer has landed: a late redelivery
// re-reads the terminal result rather than re-authoring it.
func TestHandle_RedeliveryAfterTerminalSpendsOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	h.dispatch(t, goodRequest("ref-late"))
	h.engine.Wait()
	first := h.result(t, "ref-late")

	if ack := h.dispatch(t, goodRequest("ref-late")); ack.Status != wire.AckAccepted {
		t.Fatalf("late redelivery ack: want accepted, got %q", ack.Status)
	}
	h.engine.Wait()

	if got := h.vendor.callCount(); got != 1 {
		t.Fatalf("vendor calls: want 1, got %d", got)
	}
	second := h.result(t, "ref-late")
	if second.State != first.State || string(second.Output) != string(first.Output) ||
		second.CompletedAt != first.CompletedAt || second.Usage != first.Usage {
		t.Errorf("result changed under a redelivery:\n first=%+v\nsecond=%+v", first, second)
	}
}

func TestHandle_BusyAtDailyCap(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(c *Config) { c.DailyCap = 1 })

	if ack := h.dispatch(t, goodRequest("ref-cap-1")); ack.Status != wire.AckAccepted {
		t.Fatalf("first ack: want accepted, got %q", ack.Status)
	}
	h.engine.Wait()

	ack := h.dispatch(t, goodRequest("ref-cap-2"))
	if ack.Status != wire.AckBusy {
		t.Fatalf("over-cap ack: want busy, got %q", ack.Status)
	}
	// Over-cap touches nothing at all, so the same ref can be retried once the
	// UTC day rolls.
	h.absent(t, "ref-cap-2")
	if got := h.vendor.callCount(); got != 1 {
		t.Fatalf("vendor calls: want 1, got %d", got)
	}
}

// A cap of exactly 0 is the operator's "stop spending" switch — it must block
// every call, never (as a naive `<= 0 disables` check did) remove the ceiling.
func TestHandle_DailyCapZeroBlocksEverything(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(c *Config) { c.DailyCap = 0 })

	ack := h.dispatch(t, goodRequest("ref-zero"))
	if ack.Status != wire.AckBusy {
		t.Fatalf("cap=0 ack: want busy (spending off), got %q", ack.Status)
	}
	h.engine.Wait()
	if got := h.vendor.callCount(); got != 0 {
		t.Fatalf("cap=0 must make zero vendor calls, got %d", got)
	}
	h.absent(t, "ref-zero")
}

// A redelivery of a ref already in KV must ack accepted EVEN at the cap: the
// dedup guard runs before the cap check, so the bridge never Naks for hours
// over a result already written.
func TestHandle_RedeliveryAtCapStillAccepts(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(c *Config) { c.DailyCap = 1 })

	// Burn the single slot on ref-a; it completes and its result sits in KV.
	if ack := h.dispatch(t, goodRequest("ref-a")); ack.Status != wire.AckAccepted {
		t.Fatalf("first ack: want accepted, got %q", ack.Status)
	}
	h.engine.Wait()

	// A brand-new ref is now correctly refused.
	if ack := h.dispatch(t, goodRequest("ref-new")); ack.Status != wire.AckBusy {
		t.Fatalf("new ref at cap: want busy, got %q", ack.Status)
	}

	// But redelivering ref-a — whose answer is already in KV — must ack
	// accepted, not busy.
	if ack := h.dispatch(t, goodRequest("ref-a")); ack.Status != wire.AckAccepted {
		t.Fatalf("redelivery at cap: want accepted (result already in KV), got %q (%s)", ack.Status, ack.Reason)
	}
	h.engine.Wait()
	if got := h.vendor.callCount(); got != 1 {
		t.Fatalf("vendor calls: want exactly 1, got %d", got)
	}
	if m := h.engine.Metrics(context.Background()); m.DedupTotal != 1 {
		t.Errorf("metrics: want dedup=1, got %+v", m)
	}
}

// A request whose reply subject is not an inbox is the confused-deputy vector
// (allow_responses overrides the deny list on nats-server 2.14): the runner
// must DROP it in silence — responding at all would publish the ack to the
// requestor-chosen subject, which could be a JetStream admin verb.
func TestHandle_DropsNonInboxReplySubject(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	nc := h.conn.NATS()

	const attack = "attack.reply.target"
	got := make(chan *nats.Msg, 1)
	sub, err := nc.Subscribe(attack, func(m *nats.Msg) { got <- m })
	if err != nil {
		t.Fatalf("subscribe attack subject: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	body, err := json.Marshal(goodRequest("ref-attack"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// A request with a hand-chosen (non-inbox) reply subject.
	if err := nc.PublishMsg(&nats.Msg{Subject: wire.GenerateSubject, Reply: attack, Data: body}); err != nil {
		t.Fatalf("publish malicious request: %v", err)
	}

	// A normal request afterward on the same connection. The endpoint's
	// subscription processes messages serially, so this one's ack proves the
	// malicious message was already handled — no sleep needed.
	if ack := h.dispatch(t, goodRequest("ref-legit")); ack.Status != wire.AckAccepted {
		t.Fatalf("legit ack: want accepted, got %q", ack.Status)
	}

	select {
	case m := <-got:
		t.Fatalf("engine responded to a non-inbox reply subject: %q", string(m.Data))
	default:
	}
	if m := h.engine.Metrics(context.Background()); m.RejectedTotal != 1 {
		t.Errorf("metrics: want rejected=1, got %+v", m)
	}
	h.engine.Wait()
	// The dropped request must have claimed no ref.
	h.absent(t, "ref-attack")
	if got := h.vendor.callCount(); got != 1 {
		t.Errorf("vendor calls: want 1 (only the legit request), got %d", got)
	}
}

// An oversized request is rejected as invalid before any vendor call — defense
// in depth against a giant prompt/system/tool payload.
func TestHandle_RejectsOversizedRequest(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	req := goodRequest("ref-big")
	req.Prompt = strings.Repeat("x", MaxRequestBytes+1)
	ack := h.dispatch(t, req)
	if ack.Status != wire.AckInvalid {
		t.Fatalf("oversized ack: want invalid, got %q", ack.Status)
	}
	h.engine.Wait()
	if got := h.vendor.callCount(); got != 0 {
		t.Errorf("oversized request must make zero vendor calls, got %d", got)
	}
	h.absent(t, "ref-big")
}

// A panic in the vendor call must record a failure for that ref and leave the
// process serving — never crash the runner and strand every other in-flight
// ref until its TTL.
func TestRun_WorkerPanicRecordsFailedAndSurvives(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.vendor.panicMsg = "sdk exploded mid-stream"

	h.dispatch(t, goodRequest("ref-panic"))
	h.engine.Wait()

	res := h.result(t, "ref-panic")
	if res.State != wire.StateFailed {
		t.Fatalf("state after panic: want failed, got %q", res.State)
	}
	// The recorded error is a fixed generic string, never the panic text — so a
	// panic carrying request context can never leak through the result.
	if strings.Contains(res.Error, "sdk exploded") {
		t.Errorf("recorded error leaked the panic text: %q", res.Error)
	}
	if m := h.engine.Metrics(context.Background()); m.FailedTotal != 1 {
		t.Errorf("metrics: want failed=1, got %+v", m)
	}

	// The engine is still alive: a subsequent request completes normally.
	h.vendor.panicMsg = ""
	if ack := h.dispatch(t, goodRequest("ref-after-panic")); ack.Status != wire.AckAccepted {
		t.Fatalf("post-panic ack: want accepted, got %q", ack.Status)
	}
	h.engine.Wait()
	if h.result(t, "ref-after-panic").State != wire.StateCompleted {
		t.Fatal("engine did not survive the panic: follow-up request did not complete")
	}
}

// A corrupt daily counter is a spend control that lost its integrity: fail
// CLOSED (block the call) and overwrite the counter so it self-heals to a known
// value rather than staying unreadable and waving calls through.
func TestOverDailyCap_CorruptCounterBlocksAndHeals(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, func(c *Config) {
		c.DailyCap = 5
		c.now = func() time.Time { return fixed }
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := "__usage.2026-08-21"
	if _, err := h.conn.KVPut(ctx, testBucket, key, []byte("{ this is not valid json")); err != nil {
		t.Fatalf("seed corrupt counter: %v", err)
	}

	ack := h.dispatch(t, goodRequest("ref-corrupt"))
	if ack.Status != wire.AckBusy {
		t.Fatalf("corrupt counter ack: want busy (fail closed), got %q", ack.Status)
	}
	h.engine.Wait()
	if got := h.vendor.callCount(); got != 0 {
		t.Errorf("corrupt counter must block: want 0 vendor calls, got %d", got)
	}

	// The counter self-healed to a valid, decodable value pinned at the cap.
	entry, err := h.conn.KVGet(ctx, testBucket, key)
	if err != nil {
		t.Fatalf("read healed counter: %v", err)
	}
	var c usageCounter
	if err := json.Unmarshal(entry.Value, &c); err != nil {
		t.Fatalf("counter did not self-heal — still corrupt: %v", err)
	}
	if c.Count != 5 {
		t.Errorf("healed counter: want count=cap=5, got %d", c.Count)
	}
}

// The counter is keyed by UTC day, so the cap releases when the day rolls
// rather than on a sliding window.
func TestHandle_DailyCapReleasesOnDayRoll(t *testing.T) {
	t.Parallel()
	day := time.Date(2026, 8, 21, 23, 59, 0, 0, time.UTC)
	var mu sync.Mutex
	h := newHarness(t, func(c *Config) {
		c.DailyCap = 1
		c.now = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return day
		}
	})

	h.dispatch(t, goodRequest("ref-day1"))
	h.engine.Wait()
	if ack := h.dispatch(t, goodRequest("ref-day1-again")); ack.Status != wire.AckBusy {
		t.Fatalf("same-day ack: want busy, got %q", ack.Status)
	}

	mu.Lock()
	day = day.Add(2 * time.Minute) // crosses midnight UTC
	mu.Unlock()

	if ack := h.dispatch(t, goodRequest("ref-day2")); ack.Status != wire.AckAccepted {
		t.Fatalf("next-day ack: want accepted, got %q (%s)", ack.Status, ack.Reason)
	}
	h.engine.Wait()
	if got := h.vendor.callCount(); got != 2 {
		t.Fatalf("vendor calls: want 2 (one per day), got %d", got)
	}
}

func TestRun_RefusalIsTerminalAndCategorised(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.vendor.result = VendorResult{
		Model:           "claude-opus-5",
		Refused:         true,
		RefusalCategory: "cyber",
		InputTokens:     5,
		OutputTokens:    1,
	}

	h.dispatch(t, goodRequest("ref-refused"))
	h.engine.Wait()

	res := h.result(t, "ref-refused")
	if res.State != wire.StateRefused {
		t.Fatalf("state: want refused, got %q", res.State)
	}
	if res.RefusalCategory != "cyber" {
		t.Errorf("refusalCategory: want cyber, got %q", res.RefusalCategory)
	}
	if res.Output != nil {
		t.Errorf("output: want none on a refusal, got %s", res.Output)
	}
	// A refusal still burned tokens; under-reporting spend would understate
	// the bill.
	if res.Usage.InputTokens != 5 || res.Usage.OutputTokens != 1 {
		t.Errorf("usage: want 5/1, got %d/%d", res.Usage.InputTokens, res.Usage.OutputTokens)
	}
	if m := h.engine.Metrics(context.Background()); m.RefusedTotal != 1 || m.CompletedTotal != 0 {
		t.Errorf("metrics: want refused=1 completed=0, got %+v", m)
	}
}

func TestRun_VendorErrorIsRecordedAsFailed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.vendor.err = errors.New("upstream said no")

	h.dispatch(t, goodRequest("ref-failed"))
	h.engine.Wait()

	res := h.result(t, "ref-failed")
	if res.State != wire.StateFailed {
		t.Fatalf("state: want failed, got %q", res.State)
	}
	if !strings.Contains(res.Error, "upstream said no") {
		t.Errorf("error: want the vendor message, got %q", res.Error)
	}
	if m := h.engine.Metrics(context.Background()); m.FailedTotal != 1 {
		t.Errorf("metrics: want failed=1, got %+v", m)
	}
}

// The credential must not survive into anything an operator or a caller can
// read — the recorded result is the furthest-travelling of those surfaces.
func TestRun_CredentialIsRedactedFromRecordedError(t *testing.T) {
	t.Parallel()
	const secret = "sk-ant-fixture-000111222333"
	h := newHarness(t, func(c *Config) { c.RedactStrings = []string{secret} })
	h.vendor.err = fmt.Errorf("POST /v1/messages failed (x-api-key: %s)", secret)

	h.dispatch(t, goodRequest("ref-secret"))
	h.engine.Wait()

	res := h.result(t, "ref-secret")
	if strings.Contains(res.Error, secret) {
		t.Fatalf("recorded error leaked the credential: %q", res.Error)
	}
	if !strings.Contains(res.Error, "[REDACTED]") {
		t.Errorf("recorded error: want a redaction marker, got %q", res.Error)
	}
	if !strings.Contains(res.Error, "POST /v1/messages failed") {
		t.Errorf("recorded error: want the diagnostic text preserved, got %q", res.Error)
	}
}

func TestRedact(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		secrets []string
		want    string
	}{
		{"replaces every occurrence", "a S b S", []string{"S"}, "a [REDACTED] b [REDACTED]"},
		{"leaves unrelated text", "nothing here", []string{"S"}, "nothing here"},
		{"ignores an empty secret", "keep me", []string{""}, "keep me"},
		{"applies every secret", "a S b T", []string{"S", "T"}, "a [REDACTED] b [REDACTED]"},
		// A second pass over already-redacted text would corrupt the marker
		// (the placeholder itself contains letters a later secret may match).
		{"a secret matching the placeholder", "x SECRET y", []string{"SECRET", "E"}, "x [REDACTED] y"},
		{"a longer secret containing a shorter one", "key=ABCDEF", []string{"ABC", "ABCDEF"}, "key=[REDACTED]"},
		{"no secrets configured", "a S", nil, "a S"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Redact(tc.in, tc.secrets...); got != tc.want {
				t.Errorf("Redact(%q, %v) = %q, want %q", tc.in, tc.secrets, got, tc.want)
			}
		})
	}
}

// An in-flight marker that could outlive its own call would reopen the
// double-spend window on the retry side, so the constructor refuses the
// configuration outright rather than logging about it.
func TestNew_RejectsInflightTTLUnderVendorTimeout(t *testing.T) {
	t.Parallel()
	_, err := New(Config{
		Conn:          &substrate.Conn{},
		Vendor:        &fakeVendor{},
		VendorTimeout: 10 * time.Minute,
		InflightTTL:   time.Minute,
	})
	if err == nil {
		t.Fatal("want an error for InflightTTL <= VendorTimeout, got nil")
	}
	if !strings.Contains(err.Error(), "InflightTTL") {
		t.Errorf("error should name the offending field, got %q", err)
	}
}

// The bucket is provisioned by name in the platform registry and addressed by
// name on the wire; bootstrap does not import the runner's packages, so this
// is what keeps the two spellings from drifting apart into a runner that
// writes to a bucket nobody provisioned.
func TestResultsBucketMatchesPlatformRegistry(t *testing.T) {
	t.Parallel()
	if wire.ResultsBucket != bootstrap.ModelResultsBucket {
		t.Fatalf("bucket name drift: wire.ResultsBucket=%q, bootstrap.ModelResultsBucket=%q",
			wire.ResultsBucket, bootstrap.ModelResultsBucket)
	}
	for _, b := range bootstrap.PlatformBuckets() {
		if b.Name != wire.ResultsBucket {
			continue
		}
		if !b.PerKeyTTL {
			t.Error("model-results must be PerKeyTTL: every key the runner writes is TTL'd, " +
				"and KVCreateWithTTL silently degrades to a durable write without it")
		}
		if b.Owner != wire.ServiceName {
			t.Errorf("model-results owner: want %q, got %q", wire.ServiceName, b.Owner)
		}
		if b.LensTarget {
			t.Error("model-results must not be a lens target: it is runner↔caller operational state, not a projection")
		}
		return
	}
	t.Fatalf("model-results is absent from bootstrap.PlatformBuckets() — it would never be provisioned or granted")
}

func TestNew_RequiresConnAndVendor(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{Vendor: &fakeVendor{}}); err == nil {
		t.Error("want an error with no Conn, got nil")
	}
	if _, err := New(Config{Conn: &substrate.Conn{}}); err == nil {
		t.Error("want an error with no Vendor, got nil")
	}
}
