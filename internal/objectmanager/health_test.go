package objectmanager

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/healthkv"
)

// TestAggregateStatus locks the Contract #5 §5.2/§5.3 reconciliation: a heartbeat
// carrying issues can never self-report "healthy", lifecycle phases pass through,
// and error wins over warning. Mirrors the Loom/Weaver/Bridge/Gateway heartbeaters.
func TestAggregateStatus(t *testing.T) {
	t.Parallel()
	warn := healthIssue{Severity: severityWarning, Code: "ObjectDeleteFailed", Message: "x"}
	errIssue := healthIssue{Severity: severityError, Code: "Boom", Message: "y"}

	cases := []struct {
		name      string
		lifecycle string
		issues    []healthIssue
		want      string
	}{
		{"healthy no issues stays healthy", "healthy", nil, "healthy"},
		{"healthy with warning degrades", "healthy", []healthIssue{warn}, "degraded"},
		{"healthy with error is unhealthy", "healthy", []healthIssue{errIssue}, "unhealthy"},
		{"error wins over warning", "healthy", []healthIssue{warn, errIssue}, "unhealthy"},
		{"starting passes through despite issues", "starting", []healthIssue{warn, errIssue}, "starting"},
		{"shutdown passes through despite issues", "shutdown", []healthIssue{errIssue}, "shutdown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := aggregateStatus(tc.lifecycle, tc.issues); got != tc.want {
				t.Fatalf("aggregateStatus(%q, %v) = %q, want %q", tc.lifecycle, tc.issues, got, tc.want)
			}
		})
	}
}

// TestEmitHeartbeat_IssueDegradesStatus proves the false-green fix end to end:
// a heartbeat emitted while an issue is set can never report "healthy", and the
// full Contract #5 §5.2 shape (version/heartbeatAt/startedAt/uptime/metrics) is
// present — object-store-manager's prior heartbeat carried none of these.
func TestEmitHeartbeat_IssueDegradesStatus(t *testing.T) {
	conn, ctx := testConn(t)
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         "health-kv",
		LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatalf("create health-kv bucket: %v", err)
	}

	m := New(Config{
		Conn:           conn,
		CoreKVBucket:   "core-kv",
		ObjectsBucket:  "core-objects",
		EventsStream:   "core-events",
		ReconcileGrace: time.Hour,
		HealthKVBucket: "health-kv",
		Instance:       "objmgr-degrade-test",
	})
	m.issues.set("tombstone-reclaim:x", severityWarning, "ObjectDeleteFailed", "stuck")
	m.emitHeartbeat(ctx)

	entry, err := conn.KVGet(ctx, "health-kv", "health.object-store-manager.objmgr-degrade-test")
	if err != nil {
		t.Fatalf("heartbeat key missing: %v", err)
	}
	var doc objmgrHealthDoc
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal heartbeat: %v", err)
	}
	if doc.Status != "degraded" {
		t.Errorf("Status = %q, want degraded (an open warning issue must never self-report healthy)", doc.Status)
	}
	if doc.Version == "" || doc.HeartbeatAt == "" || doc.StartedAt == "" || doc.Uptime == "" || doc.Metrics == nil {
		t.Errorf("heartbeat doc missing Contract #5 §5.2 fields: %+v", doc)
	}
	if len(doc.Issues) != 1 || doc.Issues[0].Code != "ObjectDeleteFailed" {
		t.Errorf("Issues = %+v, want one ObjectDeleteFailed entry", doc.Issues)
	}
}

// emitHeartbeat writes with a TTL derived from heartbeatEvery ×
// healthkv.DefaultTTLMultiplier (Contract #5 §5.6) so a crashed instance's key
// self-expires instead of orphaning forever. Real NATS expiry mechanics are
// proven once at the substrate layer and by the Processor heartbeater's
// end-to-end TTL test; this proves the write succeeds against a TTL-enabled
// bucket and pins the derived value.
func TestEmitHeartbeat_WritesWithDerivedTTL(t *testing.T) {
	if got, want := heartbeatEvery*healthkv.DefaultTTLMultiplier, 100*time.Second; got != want {
		t.Fatalf("derived heartbeat TTL = %v, want %v", got, want)
	}

	conn, ctx := testConn(t)
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         "health-kv",
		LimitMarkerTTL: time.Second, // enables AllowMsgTTL so KVPutWithTTL works in tests
	}); err != nil {
		t.Fatalf("create health-kv bucket: %v", err)
	}

	m := New(Config{
		Conn:           conn,
		CoreKVBucket:   "core-kv",
		ObjectsBucket:  "core-objects",
		EventsStream:   "core-events",
		ReconcileGrace: time.Hour,
		HealthKVBucket: "health-kv",
		Instance:       "objmgr-ttl-test",
	})
	m.emitHeartbeat(ctx)

	if _, err := conn.KVGet(ctx, "health-kv", "health.object-store-manager.objmgr-ttl-test"); err != nil {
		t.Fatalf("heartbeat key missing right after emit: %v", err)
	}
}

// issueCache.set must stamp since (Contract #5 §5.5) on first appearance, hold
// it steady across repeat set calls for the same key while the issue stays
// open, and clear it with the issue so a later re-occurrence gets a fresh
// since rather than reusing the stale one.
func TestIssueCacheSincePersistence(t *testing.T) {
	t.Parallel()
	c := newIssueCache()

	c.set("k", severityWarning, "Code", "first")
	first := c.snapshot()
	if len(first) != 1 || first[0].Since == "" {
		t.Fatalf("first set: got %+v, want one issue with a non-empty since", first)
	}
	since := first[0].Since

	c.set("k", severityWarning, "Code", "still open")
	second := c.snapshot()
	if len(second) != 1 || second[0].Since != since {
		t.Fatalf("since not persisted across repeat set: first %q, second %+v", since, second)
	}

	c.clear("k")
	if len(c.snapshot()) != 0 {
		t.Fatalf("cleared key still present: %+v", c.snapshot())
	}

	c.set("k", severityWarning, "Code", "reoccurred")
	reoccurred := c.snapshot()
	if len(reoccurred) != 1 || reoccurred[0].Since == since {
		t.Fatalf("reoccurred issue reused stale since %q: %+v", since, reoccurred)
	}
}
