package natsperm

// This file pins §8 of
// _bmad-output/implementation-artifacts/protected-consumer-ack-plane-denies-design.md
// — the reply-subject write primitive that every $JS.API.> holder can
// self-serve with no AllowResponses grant. §8.3 explains why the flag is
// irrelevant here: a $JS.API.* request is answered by the server's internal
// JetStream client, created with perms == nil (nats-server v2.14.0
// server/client.go:4280-4287), so its publish to the caller-named reply
// subject is never permission-checked at all. isReservedReply
// (:4215-4226) rejects only $JS.ACK./service-reply/gateway prefixes; `$KV.…`
// and `ops.…` are ordinary subjects and sail through.
//
// TestReplySubjectWriteAuthority is a CHARACTERIZATION test of a live,
// grounded, filed security defect — it is not a blessing of the behaviour it
// asserts. §8.4 records, in detail, why no narrowing of this matrix (not
// CONSUMER.CREATE, not DIRECT.GET-on-the-writable-bucket, not content
// control, not a server option) closes the class, and names the only
// candidate remedy — NATS account isolation — as a platform-trust-topology
// decision for Andrew, out of scope for this package. When that fix lands,
// TestReplySubjectWriteAuthority's assertions must be INVERTED, not deleted:
// a failure there afterward means the boundary changed underneath this test,
// which is the signal to go update it, not evidence of a broken test.
//
// TestPushConsumerDeliverSubjectDoesNotReachOpsLane and
// TestPullFetchReplyDoesNotReachOpsLane pin the two routes §8.2 found
// genuinely contained: a consumer delivery is re-subjected at the routing
// layer only, so the message the subscriber receives keeps its ORIGINAL
// subject — which is exactly why a CONSUMER.CREATE-shaped narrowing was
// rejected in §8.4 as buying nothing.
//
// TestReplySubjectBypassIsNotAllowResponsesGated pins the premise that lets
// TestReplySubjectWriteAuthority claim "no flag needed": the four vertical
// apps carry AllowResponses == false, and facet — §8.3's sole contained
// component — holds no $JS.API.> grant at all. If either fact changes, the
// claims above need re-deriving, not silent rot.

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// jsAPIDirectGetHealthKV is the $JS.API.DIRECT.GET subject for the
// health-kv bucket's backing stream — the request every subtest of
// TestReplySubjectWriteAuthority sends, in one of the two wire forms
// nats-server accepts for it (§8.1's table probes both live, separately).
const jsAPIDirectGetHealthKV = "$JS.API.DIRECT.GET.KV_" + bootstrap.HealthKVBucket

// bareDirectGetRequest builds the BARE wire form: the request subject names
// no key, and the target key rides in the JSON body's last_by_subj field.
func bareDirectGetRequest(key string) (subject string, body []byte) {
	return jsAPIDirectGetHealthKV,
		[]byte(`{"last_by_subj":"$KV.` + bootstrap.HealthKVBucket + `.` + key + `"}`)
}

// subjectTokenDirectGetRequest builds the SUBJECT-TOKEN wire form: the
// target key rides in the request subject itself, and the body is empty.
func subjectTokenDirectGetRequest(key string) (subject string, body []byte) {
	return jsAPIDirectGetHealthKV + ".$KV." + bootstrap.HealthKVBucket + "." + key, nil
}

// replySubjectWireForms is the wire-form cross-product TestReplySubjectWriteAuthority
// runs against every target: a permission claim has to cover every wire form
// the server accepts, not just the one a well-behaved client library sends.
var replySubjectWireForms = []struct {
	name    string
	request func(key string) (subject string, body []byte)
}{
	{"bare", bareDirectGetRequest},
	{"subject-token", subjectTokenDirectGetRequest},
}

// writeHealthKey writes attacker-chosen bytes to clinic-app's own health-kv
// key — the SharedWrite bucket every non-bootstrap component may write,
// and the source of the forged bytes every subtest below tries to land on a
// protected target via a chosen reply subject.
func writeHealthKey(t *testing.T, app *substrate.Conn, key string, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := app.KVPut(ctx, bootstrap.HealthKVBucket, key, payload); err != nil {
		t.Fatalf("clinic-app KVPut %s/%s: %v", bootstrap.HealthKVBucket, key, err)
	}
}

// pollForPayload polls cons until a delivered message's data equals want, or
// timeout elapses. Every subtest of TestReplySubjectWriteAuthority that
// targets ops.default shares one fixed literal subject (that IS the
// vulnerable target — it cannot be parameterized per subtest), so parallel
// subtests' consumers all see every wire form's response; matching on the
// forged payload's own content is what keeps one subtest from mistaking a
// sibling's message for its own.
func pollForPayload(t *testing.T, cons jetstream.Consumer, want []byte, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs, err := cons.Fetch(10, jetstream.FetchMaxWait(500*time.Millisecond))
		if err != nil {
			continue
		}
		for m := range msgs.Messages() {
			_ = m.Ack()
			if bytes.Equal(m.Data(), want) {
				return true
			}
		}
	}
	return false
}

// pollKVValue polls bucket/key on c until a KVGet succeeds, or timeout
// elapses, returning the stored value. The short sleep between attempts is a
// poll backoff, not the synchronization mechanism itself — the loop's exit
// condition is the KVGet succeeding.
func pollKVValue(t *testing.T, c *substrate.Conn, bucket, key string, timeout time.Duration) ([]byte, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		e, err := c.KVGet(ctx, bucket, key)
		cancel()
		if err == nil {
			return e.Value, true
		}
		time.Sleep(75 * time.Millisecond)
	}
	return nil, false
}

// TestReplySubjectWriteAuthority pins §8.1's central finding as a live vector
// against the committed deploy/nats-server.conf: as clinic-app — a component
// with no AllowResponses and no $JS.ACK.> — write forged bytes to its own
// health-kv key, then issue a DIRECT.GET for that key naming a PROTECTED
// subject as the reply, and the server hands the stored bytes to that
// subject verbatim. No AllowResponses grant, no wildcard subscribe, no
// inbound delivery is involved (§8.3); the responder is the server's own
// internal JetStream client, and it is unconditional.
//
// This is a CHARACTERIZATION test of an open, grounded, filed defect, not a
// blessing of it. There is no closure available in this matrix (§8.4); the
// remedy is account isolation, Andrew's call, out of scope here. When it
// lands, these assertions must be INVERTED, not deleted — a failure here
// afterward means the boundary moved, which is the signal, not a broken
// test.
func TestReplySubjectWriteAuthority(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, bootstrap.HealthKVBucket)
	provision(t, boot, "core-kv")
	provision(t, boot, "capability-kv")
	provisionStream(t, boot, bootstrap.CoreOpsStreamName, []string{bootstrap.OpsWildcardSubject})

	for _, form := range replySubjectWireForms {
		form := form

		// Target: ops.default — captured by the core-operations stream,
		// exactly the lane the vertical-app tier is denied a publish door to
		// (TestVerticalAppOpsPublishDenied). This is what makes the reply
		// subject a bypass of that denial, not just an isolated oddity.
		t.Run(form.name+"/ops.default", func(t *testing.T) {
			t.Parallel()
			app := connectAs(t, url, "clinic-app")
			key := "health.clinic-app.replysubj-ops-" + form.name
			payload := []byte("replysubj-forged-ops-" + form.name)
			writeHealthKey(t, app, key, payload)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cons, err := boot.JetStream().CreateOrUpdateConsumer(ctx, bootstrap.CoreOpsStreamName, jetstream.ConsumerConfig{
				Durable:       "replysubj-ops-" + form.name,
				FilterSubject: bootstrap.OpsWildcardSubject,
			})
			if err != nil {
				t.Fatalf("watch consumer on %s: %v", bootstrap.CoreOpsStreamName, err)
			}

			subject, body := form.request(key)
			if err := app.NATS().PublishRequest(subject, "ops.default", body); err != nil {
				t.Fatalf("clinic-app DIRECT.GET (%s) reply=ops.default: %v", form.name, err)
			}
			if err := app.NATS().Flush(); err != nil {
				t.Fatalf("flush: %v", err)
			}

			if !pollForPayload(t, cons, payload, 8*time.Second) {
				t.Fatalf("core-operations never captured the %s DIRECT.GET response — want the forged health-kv bytes landing on ops.default (§8.1)", form.name)
			}
		})

		// Target: $KV.core-kv.<key> — the sole-writer (processor) bucket
		// TestCoreKVWriteIsolation pins every other component out of at the
		// transport layer. The reply-subject route reaches it anyway.
		t.Run(form.name+"/core-kv", func(t *testing.T) {
			t.Parallel()
			app := connectAs(t, url, "clinic-app")
			key := "health.clinic-app.replysubj-corekv-" + form.name
			target := "vtx.probe.replysubj-corekv-" + form.name
			payload := []byte("replysubj-forged-corekv-" + form.name)
			writeHealthKey(t, app, key, payload)

			subject, body := form.request(key)
			if err := app.NATS().PublishRequest(subject, "$KV.core-kv."+target, body); err != nil {
				t.Fatalf("clinic-app DIRECT.GET (%s) reply=$KV.core-kv.%s: %v", form.name, target, err)
			}
			if err := app.NATS().Flush(); err != nil {
				t.Fatalf("flush: %v", err)
			}

			got, ok := pollKVValue(t, boot, "core-kv", target, 8*time.Second)
			if !ok {
				t.Fatalf("core-kv key %q was never written — want the forged health-kv bytes landing there (§8.1)", target)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("core-kv key %q = %q, want the forged bytes %q", target, got, payload)
			}
		})

		// Target: $KV.capability-kv.<key> — the auth-plane projection only
		// refractor may write (TestCapabilityKVWriteIsolation). The reply-
		// subject route reaches it too: this is not scoped to Core state.
		t.Run(form.name+"/capability-kv", func(t *testing.T) {
			t.Parallel()
			app := connectAs(t, url, "clinic-app")
			key := "health.clinic-app.replysubj-capkv-" + form.name
			target := "cap.probe.replysubj-" + form.name
			payload := []byte("replysubj-forged-capkv-" + form.name)
			writeHealthKey(t, app, key, payload)

			subject, body := form.request(key)
			if err := app.NATS().PublishRequest(subject, "$KV.capability-kv."+target, body); err != nil {
				t.Fatalf("clinic-app DIRECT.GET (%s) reply=$KV.capability-kv.%s: %v", form.name, target, err)
			}
			if err := app.NATS().Flush(); err != nil {
				t.Fatalf("flush: %v", err)
			}

			got, ok := pollKVValue(t, boot, "capability-kv", target, 8*time.Second)
			if !ok {
				t.Fatalf("capability-kv key %q was never written — want the forged health-kv bytes landing there (§8.1)", target)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("capability-kv key %q = %q, want the forged bytes %q", target, got, payload)
			}
		})
	}
}

// replySubjectSettleWindow bounds the two containment tests' negative
// assertions below. A positive wait can return the instant its condition is
// met; a negative has no event to poll for, so it has to sit out one bounded
// window before concluding nothing arrived. A single Fetch call with this as
// FetchMaxWait does exactly that in one step: it returns immediately if a
// message shows up (failing the assertion) and otherwise blocks for the
// whole window before coming back empty — no time.Sleep needed.
const replySubjectSettleWindow = 2 * time.Second

// assertCoreOperationsCapturedNothing is the negative half of both
// containment tests: after the positive control has already proven the
// vector reached the mechanism, this proves core-operations still saw none
// of it.
func assertCoreOperationsCapturedNothing(t *testing.T, cons jetstream.Consumer, label string) {
	t.Helper()
	msgs, err := cons.Fetch(10, jetstream.FetchMaxWait(replySubjectSettleWindow))
	if err != nil {
		return
	}
	n := 0
	for m := range msgs.Messages() {
		n++
		t.Logf("%s: core-operations captured subject=%q data=%q", label, m.Subject(), string(m.Data()))
		_ = m.Ack()
	}
	if n != 0 {
		t.Errorf("%s: core-operations captured %d message(s), want 0 — the containment §8.2 describes should have kept this off the ops lane", label, n)
	}
}

// TestPushConsumerDeliverSubjectDoesNotReachOpsLane pins §8.2's contained
// route: as clinic-app, create a push consumer on KV_health-kv (filtered to
// its own key) with DeliverSubject: ops.default, via a raw
// $JS.API.CONSUMER.CREATE request with hand-built JSON — not the client
// library, because the library is not the attacker's constraint. The vector
// reaches the mechanism (the app's own subscriber on ops.default receives
// the delivery — the positive control), but the delivered message keeps its
// ORIGINAL subject ($KV.health-kv.…), never becoming "ops.default" itself,
// so core-operations — whose capture predicate reads the message's subject —
// sees nothing.
//
// Unlike TestReplySubjectWriteAuthority, this is not expected to ever
// invert: §8.2 is why a CONSUMER.CREATE-shaped narrowing was rejected in
// §8.4 as buying nothing — the containment here is real and structural, not
// a gap waiting to be found. A failure here would mean the routing/subject
// mechanics changed, which is worth knowing regardless.
func TestPushConsumerDeliverSubjectDoesNotReachOpsLane(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, bootstrap.HealthKVBucket)
	provisionStream(t, boot, bootstrap.CoreOpsStreamName, []string{bootstrap.OpsWildcardSubject})

	app := connectAs(t, url, "clinic-app")
	key := "health.clinic-app.push-deliver-probe"
	origSubject := "$KV." + bootstrap.HealthKVBucket + "." + key
	writeHealthKey(t, app, key, []byte("push-deliver-payload"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opsWatch, err := boot.JetStream().CreateOrUpdateConsumer(ctx, bootstrap.CoreOpsStreamName, jetstream.ConsumerConfig{
		Durable:       "replysubj-push-watch",
		FilterSubject: bootstrap.OpsWildcardSubject,
	})
	if err != nil {
		t.Fatalf("watch consumer on %s: %v", bootstrap.CoreOpsStreamName, err)
	}

	// Subscribe to the deliver subject before creating the push consumer, so
	// the delivery has an interested subscriber to land on.
	sub, err := app.NATS().SubscribeSync("ops.default")
	if err != nil {
		t.Fatalf("clinic-app subscribe ops.default: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := app.NATS().Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	createReq := fmt.Sprintf(
		`{"stream_name":"KV_%s","config":{"name":"replysubj-push","deliver_subject":"ops.default","filter_subject":%q,"deliver_policy":"all","ack_policy":"none","replay_policy":"instant"}}`,
		bootstrap.HealthKVBucket, origSubject,
	)
	if _, err := app.NATS().Request("$JS.API.CONSUMER.CREATE.KV_"+bootstrap.HealthKVBucket+".replysubj-push", []byte(createReq), 5*time.Second); err != nil {
		t.Fatalf("clinic-app raw CONSUMER.CREATE (push, DeliverSubject=ops.default): %v", err)
	}

	// Positive control: the vector reached the mechanism.
	msg, err := sub.NextMsg(8 * time.Second)
	if err != nil {
		t.Fatalf("clinic-app subscriber on ops.default: want the push delivery, got %v", err)
	}
	// The containment mechanism itself: the delivered message keeps its
	// ORIGINAL subject. DeliverSubject only routed it; it never became the
	// message's own subject, which is what keeps it off core-operations.
	if msg.Subject != origSubject {
		t.Errorf("delivered message Subject = %q, want the original %q", msg.Subject, origSubject)
	}

	assertCoreOperationsCapturedNothing(t, opsWatch, "push-consumer DeliverSubject=ops.default")
}

// TestPullFetchReplyDoesNotReachOpsLane is
// TestPushConsumerDeliverSubjectDoesNotReachOpsLane's pull-consumer twin
// (§8.1's table lists it as the same CONTAINED mechanism): a
// $JS.API.CONSUMER.MSG.NEXT request whose reply subject is ops.default. Same
// structure, same non-inverting expectation — see that test's doc comment.
func TestPullFetchReplyDoesNotReachOpsLane(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, bootstrap.HealthKVBucket)
	provisionStream(t, boot, bootstrap.CoreOpsStreamName, []string{bootstrap.OpsWildcardSubject})

	app := connectAs(t, url, "clinic-app")
	key := "health.clinic-app.pull-fetch-probe"
	origSubject := "$KV." + bootstrap.HealthKVBucket + "." + key
	writeHealthKey(t, app, key, []byte("pull-fetch-payload"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opsWatch, err := boot.JetStream().CreateOrUpdateConsumer(ctx, bootstrap.CoreOpsStreamName, jetstream.ConsumerConfig{
		Durable:       "replysubj-pull-watch",
		FilterSubject: bootstrap.OpsWildcardSubject,
	})
	if err != nil {
		t.Fatalf("watch consumer on %s: %v", bootstrap.CoreOpsStreamName, err)
	}

	createReq := fmt.Sprintf(
		`{"stream_name":"KV_%s","config":{"name":"replysubj-pull","filter_subject":%q,"deliver_policy":"all","ack_policy":"none","replay_policy":"instant"}}`,
		bootstrap.HealthKVBucket, origSubject,
	)
	if _, err := app.NATS().Request("$JS.API.CONSUMER.CREATE.KV_"+bootstrap.HealthKVBucket+".replysubj-pull", []byte(createReq), 5*time.Second); err != nil {
		t.Fatalf("clinic-app raw CONSUMER.CREATE (pull): %v", err)
	}

	sub, err := app.NATS().SubscribeSync("ops.default")
	if err != nil {
		t.Fatalf("clinic-app subscribe ops.default: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := app.NATS().Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if err := app.NATS().PublishRequest(
		"$JS.API.CONSUMER.MSG.NEXT.KV_"+bootstrap.HealthKVBucket+".replysubj-pull",
		"ops.default", []byte(`{"batch":1,"expires":2000000000}`)); err != nil {
		t.Fatalf("clinic-app MSG.NEXT reply=ops.default: %v", err)
	}

	// Positive control: the vector reached the mechanism.
	msg, err := sub.NextMsg(8 * time.Second)
	if err != nil {
		t.Fatalf("clinic-app subscriber on ops.default: want the pull delivery, got %v", err)
	}
	if msg.Subject != origSubject {
		t.Errorf("delivered message Subject = %q, want the original %q", msg.Subject, origSubject)
	}

	assertCoreOperationsCapturedNothing(t, opsWatch, "pull-consumer MSG.NEXT reply=ops.default")
}

// TestReplySubjectBypassIsNotAllowResponsesGated pins the premise §8.3 rests
// on, in TestAckGrantRoster's roster-pinning style: TestReplySubjectWriteAuthority's
// claim that the reply-subject route needs no AllowResponses grant holds only
// while every vertical app actually carries AllowResponses == false, and
// §8.3's claim that facet is the sole contained component holds only while
// facet holds no $JS.API.> grant at all. If either fact silently changes,
// this test fails loudly instead of letting the claims above rot — it does
// not need to be inverted alongside TestReplySubjectWriteAuthority when the
// remedy lands; it is a premise pin, not a characterization of the defect
// itself.
func TestReplySubjectBypassIsNotAllowResponsesGated(t *testing.T) {
	t.Parallel()

	isVerticalApp := make(map[string]bool, len(verticalAppNames))
	for _, name := range verticalAppNames {
		isVerticalApp[name] = true
	}

	seenApp := make(map[string]bool, len(verticalAppNames))
	seenFacet := false
	for _, c := range Matrix {
		c := c
		if isVerticalApp[c.Name] {
			seenApp[c.Name] = true
			if c.AllowResponses {
				t.Errorf("%s: AllowResponses = true, want false — TestReplySubjectWriteAuthority's \"no flag needed\" claim (§8.3) holds only while every vertical app carries AllowResponses=false", c.Name)
			}
		}
		if c.Name == "facet" {
			seenFacet = true
			for _, s := range c.ExtraPubAllow {
				if s == "$JS.API.>" {
					t.Errorf("facet: ExtraPubAllow contains %q — §8.3 records facet as the sole contained component precisely because it holds no $JS.API.> grant; if this changes, the remedy shape recorded in §8.4 needs re-deriving", s)
				}
			}
		}
	}
	for _, name := range verticalAppNames {
		if !seenApp[name] {
			t.Errorf("%q not found in Matrix — verticalAppNames has drifted from Matrix", name)
		}
	}
	if !seenFacet {
		t.Error(`"facet" not found in Matrix`)
	}
}
