package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// alertSeverity maps known alert codes to their Contract #5 severity; unknown
// codes fall back to "warning" (never silently empty).
func TestAlertSeverity(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"stub-auth-active", "warning"},
		{"some-unknown-future-code", "warning"},
		{"", "warning"},
	}
	for _, tc := range cases {
		if got := alertSeverity(tc.code); got != tc.want {
			t.Fatalf("alertSeverity(%q) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

// NewHealthAlertEmitter / NewClaimAttemptEmitter must return inert noop
// implementations (never a live emitter holding a nil conn that would panic on
// first write) when their wiring is incomplete. Exercising the returned
// emitter must not panic and must touch no KV.
func TestEmitterConstructors_NoopGuards(t *testing.T) {
	ctx := context.Background()

	t.Run("alert emitter: nil conn → noop", func(t *testing.T) {
		e := NewHealthAlertEmitter(nil, "health-kv", nil)
		if _, ok := e.(noopAlertEmitter); !ok {
			t.Fatalf("nil conn: got %T, want noopAlertEmitter", e)
		}
		e.EmitAlert(ctx, "stub-auth-active", map[string]any{"k": "v"}) // must not panic
	})

	t.Run("alert emitter: empty bucket → noop", func(t *testing.T) {
		e := NewHealthAlertEmitter(&substrate.Conn{}, "", nil)
		if _, ok := e.(noopAlertEmitter); !ok {
			t.Fatalf("empty bucket: got %T, want noopAlertEmitter", e)
		}
	})

	t.Run("claim emitter: nil conn → noop", func(t *testing.T) {
		e := NewClaimAttemptEmitter(nil, "health-kv", "proc-1", nil)
		if _, ok := e.(noopClaimAttemptEmitter); !ok {
			t.Fatalf("nil conn: got %T, want noopClaimAttemptEmitter", e)
		}
		e.RecordClaimAttempt(ctx, "success") // must not panic
	})

	t.Run("claim emitter: empty bucket → noop", func(t *testing.T) {
		e := NewClaimAttemptEmitter(&substrate.Conn{}, "", "proc-1", nil)
		if _, ok := e.(noopClaimAttemptEmitter); !ok {
			t.Fatalf("empty bucket: got %T, want noopClaimAttemptEmitter", e)
		}
	})

	t.Run("claim emitter: empty instance → noop", func(t *testing.T) {
		e := NewClaimAttemptEmitter(&substrate.Conn{}, "health-kv", "", nil)
		if _, ok := e.(noopClaimAttemptEmitter); !ok {
			t.Fatalf("empty instance: got %T, want noopClaimAttemptEmitter", e)
		}
	})
}

// readHealthDoc fetches a Health-KV entry and unmarshals it, failing the test
// if the key is absent or malformed.
func readHealthDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, bucket, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, bucket, key)
	if err != nil {
		t.Fatalf("KVGet %s/%s: %v", bucket, key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return doc
}

// EmitAlert writes a well-formed Contract #5 alert entry to
// health.alerts.security.<code> — the channel the Lamplighter reads off the
// sole Core-KV writer. The body must carry key/alertCode/severity/observedAt
// and the verbatim details, and a re-emit must overwrite (same key, "currently
// happening", not an audit log).
func TestEmitAlert_LiveKV(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn := connectSubstrate(t, url, "alert-test")
	provisionHarness(t, ctx, conn)

	e := NewHealthAlertEmitter(conn, testHealthBucket, testLogger())
	if _, ok := e.(*HealthAlertEmitter); !ok {
		t.Fatalf("wired emitter: got %T, want *HealthAlertEmitter", e)
	}

	e.EmitAlert(ctx, "stub-auth-active", map[string]any{"authMode": "stub", "count": float64(3)})

	const key = "health.alerts.security.stub-auth-active"
	doc := readHealthDoc(t, ctx, conn, testHealthBucket, key)
	if doc["key"] != key {
		t.Fatalf("key = %v, want %q", doc["key"], key)
	}
	if doc["alertCode"] != "stub-auth-active" {
		t.Fatalf("alertCode = %v, want stub-auth-active", doc["alertCode"])
	}
	if doc["severity"] != "warning" {
		t.Fatalf("severity = %v, want warning", doc["severity"])
	}
	if doc["observedAt"] == nil || doc["observedAt"] == "" {
		t.Fatalf("observedAt missing: %v", doc["observedAt"])
	}
	details, ok := doc["details"].(map[string]any)
	if !ok {
		t.Fatalf("details not an object: %T", doc["details"])
	}
	if details["authMode"] != "stub" || details["count"] != float64(3) {
		t.Fatalf("details = %+v, want authMode=stub count=3", details)
	}

	// Re-emit overwrites in place (same key), not appends.
	e.EmitAlert(ctx, "stub-auth-active", map[string]any{"authMode": "stub", "count": float64(9)})
	doc2 := readHealthDoc(t, ctx, conn, testHealthBucket, key)
	if d := doc2["details"].(map[string]any); d["count"] != float64(9) {
		t.Fatalf("after re-emit count = %v, want 9 (overwrite)", d["count"])
	}
}

// RecordClaimAttempt writes a read-modify-write counter to
// health.processor.<instance>.claim-attempts.<outcome>; the first write starts
// at 1 and each subsequent write for the same outcome increments, while a
// different outcome tracks its own counter independently.
func TestRecordClaimAttempt_LiveKVCounter(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn := connectSubstrate(t, url, "claim-test")
	provisionHarness(t, ctx, conn)

	const instance = "proc-claim-1"
	e := NewClaimAttemptEmitter(conn, testHealthBucket, instance, testLogger())

	e.RecordClaimAttempt(ctx, "success")
	successKey := "health.processor." + instance + ".claim-attempts.success"
	doc := readHealthDoc(t, ctx, conn, testHealthBucket, successKey)
	if doc["key"] != successKey {
		t.Fatalf("key = %v, want %q", doc["key"], successKey)
	}
	if doc["instance"] != instance {
		t.Fatalf("instance = %v, want %q", doc["instance"], instance)
	}
	if doc["outcome"] != "success" {
		t.Fatalf("outcome = %v, want success", doc["outcome"])
	}
	if doc["count"] != float64(1) {
		t.Fatalf("first count = %v, want 1", doc["count"])
	}
	if doc["lastAt"] == nil || doc["lastAt"] == "" {
		t.Fatalf("lastAt missing: %v", doc["lastAt"])
	}

	// Second + third success increment the same counter (read-modify-write).
	e.RecordClaimAttempt(ctx, "success")
	e.RecordClaimAttempt(ctx, "success")
	if c := readHealthDoc(t, ctx, conn, testHealthBucket, successKey)["count"]; c != float64(3) {
		t.Fatalf("after 3 records count = %v, want 3", c)
	}

	// A different outcome is an independent counter.
	e.RecordClaimAttempt(ctx, "invalid-key")
	invalidKey := "health.processor." + instance + ".claim-attempts.invalid-key"
	if c := readHealthDoc(t, ctx, conn, testHealthBucket, invalidKey)["count"]; c != float64(1) {
		t.Fatalf("invalid-key count = %v, want 1 (independent)", c)
	}
	// success counter is untouched by the invalid-key write.
	if c := readHealthDoc(t, ctx, conn, testHealthBucket, successKey)["count"]; c != float64(3) {
		t.Fatalf("success count after invalid-key = %v, want 3 (independent)", c)
	}
}

// emitCapabilityAuthSignals writes the per-heartbeat step3-latency liveness
// signal only when a CapabilityAuthorizer is attached: nil authorizer (stub
// mode) emits nothing, an attached authorizer emits the zero-sample doc.
func TestEmitCapabilityAuthSignals_LiveKV(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn := connectSubstrate(t, url, "step3lat-test")
	provisionHarness(t, ctx, conn)

	const instance = "proc-lat-1"
	latencyKey := "health.processor." + instance + ".step3-latency"

	t.Run("no authorizer attached → no key written", func(t *testing.T) {
		hb := NewHealthHeartbeater(conn, testHealthBucket, instance, 10*time.Second, &Metrics{}, testLogger())
		hb.emitCapabilityAuthSignals(ctx)
		if _, err := conn.KVGet(ctx, testHealthBucket, latencyKey); err == nil {
			t.Fatalf("step3-latency key written with no authorizer attached")
		}
	})

	t.Run("authorizer attached → zero-sample liveness doc emitted", func(t *testing.T) {
		reader := &fakeReader{entries: map[string][]byte{}}
		ca, err := NewCapabilityAuthorizer(reader, "capability-kv", &fakeClock{now: time.Now()},
			DefaultCapabilityAuthorizerConfig(), testLogger())
		if err != nil {
			t.Fatalf("NewCapabilityAuthorizer: %v", err)
		}
		hb := NewHealthHeartbeater(conn, testHealthBucket, instance, 10*time.Second, &Metrics{}, testLogger())
		hb.AttachCapabilityAuthorizer(ca)
		hb.emitCapabilityAuthSignals(ctx)

		doc := readHealthDoc(t, ctx, conn, testHealthBucket, latencyKey)
		if doc["key"] != latencyKey {
			t.Fatalf("key = %v, want %q", doc["key"], latencyKey)
		}
		if doc["component"] != "processor" || doc["instance"] != instance {
			t.Fatalf("component/instance = %v/%v, want processor/%s", doc["component"], doc["instance"], instance)
		}
		if doc["count"] != float64(0) {
			t.Fatalf("count = %v, want 0 (zero-sample liveness)", doc["count"])
		}
		for _, f := range []string{"meanNs", "p95Ns", "p99Ns", "observedAt"} {
			if _, ok := doc[f]; !ok {
				t.Fatalf("step3-latency doc missing field %q", f)
			}
		}
	})

	t.Run("a heartbeat tick writes the key", func(t *testing.T) {
		// emitCapabilityAuthSignals reachable from emit() is the whole wiring: a
		// signal only a test calls directly never reaches Health KV in production.
		const tickInstance = "proc-lat-tick"
		reader := &fakeReader{entries: map[string][]byte{}}
		ca, err := NewCapabilityAuthorizer(reader, "capability-kv", &fakeClock{now: time.Now()},
			DefaultCapabilityAuthorizerConfig(), testLogger())
		if err != nil {
			t.Fatalf("NewCapabilityAuthorizer: %v", err)
		}
		hb := NewHealthHeartbeater(conn, testHealthBucket, tickInstance, 10*time.Second, &Metrics{}, testLogger())
		hb.AttachCapabilityAuthorizer(ca)
		hb.emit(ctx, "healthy")
		doc := readHealthDoc(t, ctx, conn, testHealthBucket, "health.processor."+tickInstance+".step3-latency")
		if doc["instance"] != tickInstance {
			t.Fatalf("instance = %v, want %q", doc["instance"], tickInstance)
		}
	})
}

// emitStep5Signals writes the per-heartbeat step5-latency signal only when an
// Executor is attached: nil executor emits nothing, an attached one emits the
// zero-sample doc with both means NULL (Contract #5 §5.4 — an unmeasured metric
// is reported as unmeasured, never as a fabricated 0), and an executor that has
// run reports the real window.
func TestEmitStep5Signals_LiveKV(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn := connectSubstrate(t, url, "step5lat-test")
	provisionHarness(t, ctx, conn)

	const instance = "proc-step5-1"
	latencyKey := "health.processor." + instance + ".step5-latency"

	t.Run("no executor attached → no key written", func(t *testing.T) {
		hb := NewHealthHeartbeater(conn, testHealthBucket, instance, 10*time.Second, &Metrics{}, testLogger())
		hb.emitStep5Signals(ctx)
		if _, err := conn.KVGet(ctx, testHealthBucket, latencyKey); err == nil {
			t.Fatalf("step5-latency key written with no executor attached")
		}
	})

	t.Run("executor attached, zero samples → null means", func(t *testing.T) {
		hb := NewHealthHeartbeater(conn, testHealthBucket, instance, 10*time.Second, &Metrics{}, testLogger())
		hb.AttachExecutor(NewExecutor(NewStarlarkRunner(0, 0), testLogger()))
		hb.emitStep5Signals(ctx)

		doc := readHealthDoc(t, ctx, conn, testHealthBucket, latencyKey)
		if doc["key"] != latencyKey {
			t.Fatalf("key = %v, want %q", doc["key"], latencyKey)
		}
		if doc["component"] != "processor" || doc["instance"] != instance {
			t.Fatalf("component/instance = %v/%v, want processor/%s", doc["component"], doc["instance"], instance)
		}
		if doc["count"] != float64(0) {
			t.Fatalf("count = %v, want 0 (zero-sample liveness)", doc["count"])
		}
		if doc["timeoutsTotal"] != float64(0) {
			t.Fatalf("timeoutsTotal = %v, want 0", doc["timeoutsTotal"])
		}
		for _, f := range []string{"meanNs", "p95Ns", "p99Ns", "observedAt"} {
			if _, ok := doc[f]; !ok {
				t.Fatalf("step5-latency doc missing field %q", f)
			}
		}
		// PRESENT and null — a reader distinguishes "no measurement" from "the
		// metric is not emitted at all", which an absent key would not allow.
		for _, f := range []string{"meanLiveReads", "meanListings"} {
			v, ok := doc[f]
			if !ok {
				t.Fatalf("step5-latency doc missing field %q — it must be present with a null value at count 0", f)
			}
			if v != nil {
				t.Fatalf("%s = %v at count 0, want JSON null", f, v)
			}
		}
	})

	t.Run("a heartbeat tick writes the key", func(t *testing.T) {
		// emitStep5Signals reachable from emit() is the whole wiring: a signal
		// only a test calls directly never reaches Health KV in production.
		const tickInstance = "proc-step5-tick"
		hb := NewHealthHeartbeater(conn, testHealthBucket, tickInstance, 10*time.Second, &Metrics{}, testLogger())
		hb.AttachExecutor(NewExecutor(NewStarlarkRunner(0, 0), testLogger()))
		hb.emit(ctx, "healthy")
		doc := readHealthDoc(t, ctx, conn, testHealthBucket, "health.processor."+tickInstance+".step5-latency")
		if doc["instance"] != tickInstance {
			t.Fatalf("instance = %v, want %q", doc["instance"], tickInstance)
		}
	})

	t.Run("after one execution → count 1 and numeric means", func(t *testing.T) {
		exec := NewExecutor(NewStarlarkRunner(0, 0), testLogger())
		sc := step5ReadingContext()
		if _, err := exec.Execute(ctx, sc.Operation, HydratedState{Context: sc}); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		hb := NewHealthHeartbeater(conn, testHealthBucket, instance, 10*time.Second, &Metrics{}, testLogger())
		hb.AttachExecutor(exec)
		hb.emitStep5Signals(ctx)

		doc := readHealthDoc(t, ctx, conn, testHealthBucket, latencyKey)
		if doc["count"] != float64(1) {
			t.Fatalf("count = %v, want 1", doc["count"])
		}
		if doc["meanLiveReads"] != float64(1) {
			t.Fatalf("meanLiveReads = %v, want 1", doc["meanLiveReads"])
		}
		if doc["meanListings"] != float64(1) {
			t.Fatalf("meanListings = %v, want 1", doc["meanListings"])
		}
		mean, ok := doc["meanNs"].(float64)
		if !ok || mean <= 0 {
			t.Fatalf("meanNs = %v, want a positive wall time", doc["meanNs"])
		}
	})
}
