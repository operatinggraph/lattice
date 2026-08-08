package substrate

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// publishDurableTestMsg writes a value under the core-kv bucket; the backing
// stream subject is "$KV.<bucket>.<key>", which the durable consumer filters
// and delivers.
func publishDurableTestMsg(ctx context.Context, t *testing.T, c *Conn, bucket, key string, body []byte) {
	t.Helper()
	if _, err := c.KVPut(ctx, bucket, key, body); err != nil {
		t.Fatalf("KVPut %q: %v", key, err)
	}
}

// awaitDurableQuiesced polls the durable's consumer info until it reports no
// ack-pending message and no waiting pull request. A stopped RunDurableConsumer
// unsubscribes asynchronously (the client's pull goroutine sends the UNSUB
// after Stop returns), so its server-side pull request can outlive the run; a
// message published while that request is still waiting is delivered into the
// dead iterator and surfaces again only after AckWait — far beyond any test
// deadline. Consumer info prunes waiting requests whose reply interest is gone
// before reporting NumWaiting, so this poll converges as soon as the server
// has processed the unsubscribe.
func awaitDurableQuiesced(ctx context.Context, t *testing.T, c *Conn, stream, durable string) {
	t.Helper()
	cons, err := c.js.Consumer(ctx, stream, durable)
	if err != nil {
		t.Fatalf("consumer %q: %v", durable, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := cons.Info(ctx)
		if err != nil {
			t.Fatalf("consumer info %q: %v", durable, err)
		}
		if info.NumAckPending == 0 && info.NumWaiting == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("durable %q did not quiesce: NumAckPending=%d NumWaiting=%d",
				durable, info.NumAckPending, info.NumWaiting)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunDurableConsumer_AckResumeFromLastAck verifies the ack floor advances
// and a re-run with the same Durable resumes at the next unacked message rather
// than replaying from the start.
func TestRunDurableConsumer_AckResumeFromLastAck(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	const durable = "dc-test-resume"
	cfg := DurableConsumerConfig{
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Durable:       durable,
	}

	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.alpha", []byte(`{"v":1}`))

	// First run: consume and ack exactly the first message, then cancel.
	run1Ctx, cancel1 := context.WithCancel(ctx)
	var seen1 []string
	var mu sync.Mutex
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		_ = c.RunDurableConsumer(run1Ctx, cfg, func(_ context.Context, msg Message) Decision {
			mu.Lock()
			seen1 = append(seen1, msg.Subject)
			mu.Unlock()
			cancel1()
			return Ack
		})
	}()
	<-done1

	if len(seen1) == 0 || seen1[0] != "$KV."+bucket+".vtx.meta.alpha" {
		t.Fatalf("first run did not deliver alpha: %v", seen1)
	}

	// Barrier before the between-runs writes: until run1's pull request is
	// gone from the server, a publish could be delivered into the stopped
	// iterator (see awaitDurableQuiesced) and stall until AckWait. Quiescing
	// first guarantees beta/gamma stay pending for run2's own pull.
	awaitDurableQuiesced(ctx, t, c, cfg.Stream, durable)

	// Writes between runs — these are what the resumed consumer must surface.
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.beta", []byte(`{"v":2}`))
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.gamma", []byte(`{"v":3}`))

	// Second run: same Durable. Must resume at beta/gamma, never replay alpha.
	run2Ctx, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	seen2 := make(chan string, 8)
	go func() {
		_ = c.RunDurableConsumer(run2Ctx, cfg, func(_ context.Context, msg Message) Decision {
			seen2 <- msg.Subject
			return Ack
		})
	}()

	// 8s (not the file's usual 3-5s): this path stands up a second consumer
	// instance on the durable before the first delivery can happen, so it has
	// less headroom under CI's -p 4 contention.
	got := map[string]bool{}
	deadline := time.After(8 * time.Second)
	for len(got) < 2 {
		select {
		case s := <-seen2:
			if s == "$KV."+bucket+".vtx.meta.alpha" {
				t.Fatalf("resumed consumer replayed already-acked alpha — position not held")
			}
			got[s] = true
		case <-deadline:
			t.Fatalf("timed out waiting for resumed messages, got %v", got)
		}
	}
	if !got["$KV."+bucket+".vtx.meta.beta"] || !got["$KV."+bucket+".vtx.meta.gamma"] {
		t.Fatalf("resumed consumer did not deliver both new messages: %v", got)
	}
}

// TestRunDurableConsumer_NakRedelivers verifies Nak triggers redelivery
// (at-least-once): the handler Naks once, then Acks, and the same message is
// delivered at least twice.
func TestRunDurableConsumer_NakRedelivers(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	cfg := DurableConsumerConfig{
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Durable:       "dc-test-nak",
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.nakme", []byte(`{"v":1}`))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var deliveries int32
	acked := make(chan struct{})
	go func() {
		_ = c.RunDurableConsumer(runCtx, cfg, func(_ context.Context, _ Message) Decision {
			n := atomic.AddInt32(&deliveries, 1)
			if n == 1 {
				return Nak
			}
			close(acked)
			return Ack
		})
	}()

	select {
	case <-acked:
	case <-time.After(5 * time.Second):
		t.Fatalf("message never redelivered+acked after Nak; deliveries=%d", atomic.LoadInt32(&deliveries))
	}
	if atomic.LoadInt32(&deliveries) < 2 {
		t.Fatalf("expected >= 2 deliveries (Nak redelivery), got %d", atomic.LoadInt32(&deliveries))
	}
}

// TestRunDurableConsumer_TermDoesNotRedeliver verifies Term drops the message
// (no redelivery) while a subsequent message still flows.
func TestRunDurableConsumer_TermDoesNotRedeliver(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	cfg := DurableConsumerConfig{
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Durable:       "dc-test-term",
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.poison", []byte(`{"bad":1}`))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var poisonDeliveries int32
	nextSeen := make(chan struct{})
	go func() {
		_ = c.RunDurableConsumer(runCtx, cfg, func(_ context.Context, msg Message) Decision {
			if msg.Subject == "$KV."+bucket+".vtx.meta.poison" {
				atomic.AddInt32(&poisonDeliveries, 1)
				return Term
			}
			if msg.Subject == "$KV."+bucket+".vtx.meta.good" {
				close(nextSeen)
			}
			return Ack
		})
	}()

	// Allow the poison message to be delivered+termed, then publish the next.
	time.Sleep(300 * time.Millisecond)
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.good", []byte(`{"ok":1}`))

	select {
	case <-nextSeen:
	case <-time.After(5 * time.Second):
		t.Fatalf("subsequent message did not flow after Term")
	}
	if got := atomic.LoadInt32(&poisonDeliveries); got != 1 {
		t.Fatalf("poison message delivered %d times, want exactly 1 (Term = no redelivery)", got)
	}
}

// TestRunDurableConsumer_CleanShutdown verifies ctx cancellation unblocks
// RunDurableConsumer promptly (the mc.Stop()-on-ctx watcher) and it returns.
func TestRunDurableConsumer_CleanShutdown(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	cfg := DurableConsumerConfig{
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Durable:       "dc-test-shutdown",
	}

	runCtx, cancel := context.WithCancel(ctx)
	returned := make(chan error, 1)
	go func() {
		returned <- c.RunDurableConsumer(runCtx, cfg, func(_ context.Context, _ Message) Decision {
			return Ack
		})
	}()

	// Let the consumer settle on the blocking Next(), then cancel.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("RunDurableConsumer returned error on clean shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("RunDurableConsumer did not return promptly after ctx cancel (hung)")
	}
}

// TestRunDurableConsumer_ReadFromBody verifies the handler receives the message
// body and that Message.Subject is the raw stream subject (so a caller can
// strip a "$KV.<bucket>." prefix for key recovery).
func TestRunDurableConsumer_ReadFromBody(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	cfg := DurableConsumerConfig{
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Durable:       "dc-test-body",
	}
	wantBody := []byte(`{"identity":"andrew","v":42}`)
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.payload", wantBody)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	got := make(chan Message, 1)
	go func() {
		_ = c.RunDurableConsumer(runCtx, cfg, func(_ context.Context, msg Message) Decision {
			got <- msg
			return Ack
		})
	}()

	select {
	case msg := <-got:
		if string(msg.Body) != string(wantBody) {
			t.Fatalf("body: got %q want %q", msg.Body, wantBody)
		}
		if msg.Subject != "$KV."+bucket+".vtx.meta.payload" {
			t.Fatalf("subject: got %q want raw stream subject %q",
				msg.Subject, "$KV."+bucket+".vtx.meta.payload")
		}
		if msg.Sequence == 0 {
			t.Fatalf("sequence: got 0, want backing-stream sequence")
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("handler never received the message")
	}
}

// AckWait and MaxPrefetch are one fix, not two, and neither is testable through
// behaviour alone at a sane test duration — so this asserts they reach the
// JetStream consumer, which is the part that can silently regress.
//
// Why they travel together: AckWait's clock runs from when a message is
// DELIVERED, and with nats.go's default prefetch of 500 "delivered" means "into
// the client's buffer", not "into the handler". This loop is strictly serial, so
// on a DeliverAll replay a queued message sits through every preceding handler
// and is redelivered before its own ever runs, however large AckWait is. Capping
// the prefetch is what makes the ack clock start when the work does.
func TestRunDurableConsumer_AckWaitAndPrefetchReachTheConsumer(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	cfg := DurableConsumerConfig{
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Durable:       "dc-test-ackwait",
		AckWait:       97 * time.Second,
		MaxPrefetch:   1,
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.ackwait", []byte(`{"v":1}`))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	delivered := make(chan struct{})
	go func() {
		_ = c.RunDurableConsumer(runCtx, cfg, func(_ context.Context, _ Message) Decision {
			select {
			case <-delivered:
			default:
				close(delivered)
			}
			return Ack
		})
	}()

	select {
	case <-delivered:
	case <-time.After(10 * time.Second):
		t.Fatal("consumer never delivered a message")
	}

	cons, err := c.js.Consumer(ctx, "KV_"+bucket, "dc-test-ackwait")
	if err != nil {
		t.Fatalf("look up consumer: %v", err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.Config.AckWait != 97*time.Second {
		t.Fatalf("AckWait = %v, want 97s — a slow handler is redelivered mid-flight without it", info.Config.AckWait)
	}
}

// A zero AckWait must leave JetStream's own default rather than writing one, so
// every existing consumer keeps the behaviour it was built against.
func TestRunDurableConsumer_ZeroAckWaitLeavesTheDefault(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	bucket := "core-kv"
	provisionCoreBucket(ctx, t, c, bucket)

	cfg := DurableConsumerConfig{
		Stream:        "KV_" + bucket,
		FilterSubject: "$KV." + bucket + ".vtx.meta.>",
		Durable:       "dc-test-ackwait-default",
	}
	publishDurableTestMsg(ctx, t, c, bucket, "vtx.meta.ackwaitdef", []byte(`{"v":1}`))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	delivered := make(chan struct{})
	go func() {
		_ = c.RunDurableConsumer(runCtx, cfg, func(_ context.Context, _ Message) Decision {
			select {
			case <-delivered:
			default:
				close(delivered)
			}
			return Ack
		})
	}()
	select {
	case <-delivered:
	case <-time.After(10 * time.Second):
		t.Fatal("consumer never delivered a message")
	}

	cons, err := c.js.Consumer(ctx, "KV_"+bucket, "dc-test-ackwait-default")
	if err != nil {
		t.Fatalf("look up consumer: %v", err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.Config.AckWait != 30*time.Second {
		t.Fatalf("AckWait = %v, want JetStream's 30s default left untouched", info.Config.AckWait)
	}
}
