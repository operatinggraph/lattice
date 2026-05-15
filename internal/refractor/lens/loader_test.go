package lens_test

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/lens"
)

// startJetStreamServer starts an in-memory NATS server with JetStream enabled
// and returns a connected *nats.Conn. The server and connection are shut down
// via t.Cleanup at the end of the test.
func startJetStreamServer(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(), // unique dir per test ensures clean JetStream state
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err, "create test NATS server")
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second), "NATS server not ready within 5s")

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err, "connect to test NATS server")

	t.Cleanup(func() {
		nc.Close()
		s.Shutdown()
	})
	return nc
}

// publishRule publishes a rule YAML to the correct rules stream subject.
func publishRule(t *testing.T, nc *nats.Conn, team, ruleID string, payload []byte) {
	t.Helper()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	subject := "materializer.rules." + team + "." + ruleID
	_, err = js.Publish(context.Background(), subject, payload)
	require.NoError(t, err, "publish rule to %s", subject)
}

func TestLoader_PublishRuleAppearsInIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, loader.Start(ctx))

	ruleYAML := []byte(`
id: test-rule
team: test-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: test-bucket
  key: agreement_id
`)
	publishRule(t, nc, "test-team", "test-rule", ruleYAML)

	require.Eventually(t, func() bool {
		_, ok := loader.Get("test-rule")
		return ok
	}, 500*time.Millisecond, 10*time.Millisecond, "rule did not appear in index within 500ms")

	r, ok := loader.Get("test-rule")
	require.True(t, ok)
	assert.Equal(t, "test-rule", r.ID)
	assert.Equal(t, "test-team", r.Team)
	assert.Equal(t, lens.KeyField{"agreement_id"}, r.Into.Key)
}

func TestLoader_UpdatedRuleReplacesOldVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, loader.Start(ctx))

	v1YAML := []byte(`
id: update-rule
team: update-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: v1-bucket
  key: agreement_id
`)
	publishRule(t, nc, "update-team", "update-rule", v1YAML)
	require.Eventually(t, func() bool {
		_, ok := loader.Get("update-rule")
		return ok
	}, 500*time.Millisecond, 10*time.Millisecond, "v1 rule did not appear in index")

	// Publish updated version to same subject
	v2YAML := []byte(`
id: update-rule
team: update-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: v2-bucket
  key: agreement_id
`)
	publishRule(t, nc, "update-team", "update-rule", v2YAML)
	require.Eventually(t, func() bool {
		r, ok := loader.Get("update-rule")
		return ok && r.Into.Bucket == "v2-bucket"
	}, 500*time.Millisecond, 10*time.Millisecond, "rule index was not updated to v2")

	r, ok := loader.Get("update-rule")
	require.True(t, ok)
	assert.Equal(t, "v2-bucket", r.Into.Bucket)
}

func TestLoader_InvalidRuleNotAddedToIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, loader.Start(ctx))

	// Publish invalid YAML (missing id)
	invalidYAML := []byte(`
team: bad-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: test
  key: agreement_id
`)
	publishRule(t, nc, "bad-team", "bad-rule", invalidYAML)

	// Rule must NOT appear in the index — ID is empty, so no entry to look up.
	// Use assert.Never so the test actively polls rather than relying on a fixed sleep.
	assert.Never(t, func() bool {
		return len(loader.All()) > 0
	}, 200*time.Millisecond, 10*time.Millisecond, "invalid rule must not be added to index")

	// Loader must still be running — publish a valid rule and verify it loads
	validYAML := []byte(`
id: valid-after-invalid
team: bad-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: test
  key: agreement_id
`)
	publishRule(t, nc, "bad-team", "valid-after-invalid", validYAML)
	require.Eventually(t, func() bool {
		_, ok := loader.Get("valid-after-invalid")
		return ok
	}, 500*time.Millisecond, 10*time.Millisecond, "loader stopped after invalid rule")
}

// ── Update callback tests ─────────────────────────────────────────────────────

const hotRuleV1YAML = `
id: hot-rule
team: hot-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  dsn: postgres://localhost/test
  table: old_table
  key: agreement_id
`

const hotRuleV2IntoYAML = `
id: hot-rule
team: hot-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  dsn: postgres://localhost/test
  table: new_table
  key: agreement_id
`

const hotRuleV2MatchYAML = `
id: hot-rule
team: hot-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id, a.name AS name
into:
  target: postgres
  dsn: postgres://localhost/test
  table: old_table
  key: agreement_id
`

func TestLoader_UpdateCallback_IntoOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	type callbackResult struct {
		kind lens.UpdateKind
	}
	results := make(chan callbackResult, 2)
	loader.SetUpdateCallback(func(_, _ *lens.Rule, kind lens.UpdateKind) {
		results <- callbackResult{kind: kind}
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, loader.Start(ctx))

	// Publish v1 — callback must NOT fire (no old rule exists yet).
	publishRule(t, nc, "hot-team", "hot-rule", []byte(hotRuleV1YAML))
	require.Eventually(t, func() bool {
		_, ok := loader.Get("hot-rule")
		return ok
	}, 500*time.Millisecond, 10*time.Millisecond, "v1 rule did not load")

	// v1 load must not have fired the callback.
	select {
	case r := <-results:
		t.Fatalf("callback fired on first load unexpectedly: kind=%v", r.kind)
	case <-time.After(100 * time.Millisecond):
		// expected — no callback on first load
	}

	// Publish v2 (INTO-only: same match, different table).
	publishRule(t, nc, "hot-team", "hot-rule", []byte(hotRuleV2IntoYAML))

	select {
	case r := <-results:
		assert.Equal(t, lens.IntoOnly, r.kind, "INTO-only update must fire callback with IntoOnly")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("callback was not fired for INTO-only update within 500ms")
	}
}

func TestLoader_UpdateCallback_MatchChange(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	type callbackResult struct {
		kind lens.UpdateKind
	}
	results := make(chan callbackResult, 2)
	loader.SetUpdateCallback(func(_, _ *lens.Rule, kind lens.UpdateKind) {
		results <- callbackResult{kind: kind}
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, loader.Start(ctx))

	// Publish v1.
	publishRule(t, nc, "hot-team", "hot-rule", []byte(hotRuleV1YAML))
	require.Eventually(t, func() bool {
		_, ok := loader.Get("hot-rule")
		return ok
	}, 500*time.Millisecond, 10*time.Millisecond, "v1 rule did not load")

	// Drain any spurious callback.
	select {
	case <-results:
	case <-time.After(100 * time.Millisecond):
	}

	// Publish v2 with different MATCH clause.
	publishRule(t, nc, "hot-team", "hot-rule", []byte(hotRuleV2MatchYAML))

	select {
	case r := <-results:
		assert.Equal(t, lens.MatchChange, r.kind, "MATCH change must fire callback with MatchChange")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("callback was not fired for MATCH change within 500ms")
	}
}

func TestLoader_UpdateCallback_NotCalledOnFirstLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	fired := make(chan struct{}, 1)
	loader.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {
		fired <- struct{}{}
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, loader.Start(ctx))

	// Publish three different rules — none have prior versions, callback must not fire.
	for _, id := range []string{"rule-x", "rule-y", "rule-z"} {
		publishRule(t, nc, "team-x", id, []byte(`
id: `+id+`
team: team-x
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  dsn: postgres://localhost/test
  table: tbl
  key: agreement_id
`))
	}

	require.Eventually(t, func() bool {
		return len(loader.All()) == 3
	}, 500*time.Millisecond, 10*time.Millisecond, "all three rules must load")

	select {
	case <-fired:
		t.Fatal("callback must not fire on first-time rule load")
	case <-time.After(100 * time.Millisecond):
		// expected
	}
}

func TestLoader_AllReturnsSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, loader.Start(ctx))

	for _, id := range []string{"rule-a", "rule-b", "rule-c"} {
		publishRule(t, nc, "snapshot-team", id, []byte(`
id: `+id+`
team: snapshot-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: test
  key: agreement_id
`))
	}

	require.Eventually(t, func() bool {
		return len(loader.All()) == 3
	}, 500*time.Millisecond, 10*time.Millisecond, "expected 3 rules in index")

	all := loader.All()
	assert.Len(t, all, 3)
}

// ── Sequence and LoadCallback tests ──────────────────────────────────────────

const seqRuleYAML = `
id: seq-rule
team: seq-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: seq-bucket
  key: agreement_id
`

// TestLoader_Rule_SequencePopulated verifies that Rule.Sequence is set to the
// NATS JetStream stream sequence number after the loader processes the message (AC1, AC3).
func TestLoader_Rule_SequencePopulated(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, loader.Start(ctx))

	publishRule(t, nc, "seq-team", "seq-rule", []byte(seqRuleYAML))

	require.Eventually(t, func() bool {
		r, ok := loader.Get("seq-rule")
		return ok && r.Sequence > 0
	}, 500*time.Millisecond, 10*time.Millisecond, "rule must appear in index with Sequence > 0")

	r, ok := loader.Get("seq-rule")
	require.True(t, ok)
	assert.Greater(t, r.Sequence, uint64(0), "Rule.Sequence must be the non-zero JetStream stream sequence")
}

// TestLoader_LoadCallback_FiresOnFirstLoad verifies that SetLoadCallback fires
// when a rule is loaded for the first time (AC1, AC3).
func TestLoader_LoadCallback_FiresOnFirstLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	type callbackResult struct {
		id  string
		seq uint64
	}
	results := make(chan callbackResult, 2)
	loader.SetLoadCallback(func(r *lens.Rule) {
		results <- callbackResult{id: r.ID, seq: r.Sequence}
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, loader.Start(ctx))

	publishRule(t, nc, "seq-team", "seq-rule", []byte(seqRuleYAML))

	select {
	case got := <-results:
		assert.Equal(t, "seq-rule", got.id, "callback must carry the correct rule ID")
		assert.Greater(t, got.seq, uint64(0), "callback must carry non-zero Sequence")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("load callback was not fired within 500ms on first load")
	}
}

// TestLoader_LoadCallback_FiresOnUpdate verifies that SetLoadCallback fires on
// every successful rule load — both first loads and subsequent updates (AC1, AC3).
func TestLoader_LoadCallback_FiresOnUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	fired := make(chan uint64, 4)
	loader.SetLoadCallback(func(r *lens.Rule) {
		fired <- r.Sequence
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, loader.Start(ctx))

	// Publish v1 — callback must fire once.
	publishRule(t, nc, "seq-team", "seq-rule", []byte(seqRuleYAML))

	var seq1 uint64
	select {
	case seq1 = <-fired:
		assert.Greater(t, seq1, uint64(0), "v1 callback must carry non-zero Sequence")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("load callback was not fired for v1")
	}

	// Publish v2 (same rule, different bucket) — callback must fire again.
	v2YAML := []byte(`
id: seq-rule
team: seq-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: nats_kv
  bucket: seq-bucket-v2
  key: agreement_id
`)
	publishRule(t, nc, "seq-team", "seq-rule", v2YAML)

	var seq2 uint64
	select {
	case seq2 = <-fired:
		assert.Greater(t, seq2, seq1, "v2 sequence must be greater than v1 sequence")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("load callback was not fired for v2 update")
	}
}

// TestLoader_LoadCallback_WiresReporterSequence is an integration test demonstrating
// the wiring pattern: loader load callback → reporter.SetRuleSequence → health KV
// activeSequence (AC1, AC3).
func TestLoader_LoadCallback_WiresReporterSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	nc := startJetStreamServer(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	healthKV, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{
		Bucket: "HEALTH-SEQ-TEST",
	})
	require.NoError(t, err)

	reporter := health.New(healthKV, "seq-rule", "seq-team")

	loader, err := lens.NewLoader(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Wire: on every rule load, set the reporter sequence and write active.
	loader.SetLoadCallback(func(r *lens.Rule) {
		reporter.SetRuleSequence(r.Sequence)
		_ = reporter.SetActive(context.Background())
	})
	require.NoError(t, loader.Start(ctx))

	publishRule(t, nc, "seq-team", "seq-rule", []byte(seqRuleYAML))

	// Wait for health KV to reflect the sequence.
	var entry health.Entry
	require.Eventually(t, func() bool {
		e, getErr := reporter.GetStatus(context.Background())
		if getErr != nil {
			return false
		}
		entry = e
		return entry.ActiveSequence > 0
	}, 500*time.Millisecond, 10*time.Millisecond, "activeSequence must be set via load callback")

	// Confirm the sequence in the health KV matches the rule's loaded sequence.
	loadedRule, ok := loader.Get("seq-rule")
	require.True(t, ok)
	assert.Equal(t, loadedRule.Sequence, entry.ActiveSequence,
		"health KV activeSequence must match the loaded rule's Sequence")
	assert.Greater(t, loadedRule.Sequence, uint64(0))
}
