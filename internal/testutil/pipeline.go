// Story 4.7 cleanup — pipeline + harness helpers for external test
// packages that exercise package-installed DDLs end-to-end.
//
// The processor's own integration tests use unexported helpers in
// internal/processor/integration_test.go to provision KV buckets,
// install packages, seed Capability docs, and assemble a CommitPath.
// External test packages (packages/identity-domain/_test,
// packages/rbac-domain/_test, etc.) can't reach those `_test.go`
// helpers, so the equivalent surface is reproduced here as a
// non-test-file API.
//
// The helpers are still strictly test-only: they take *testing.T and
// call t.Fatalf / t.Cleanup. They live in `internal/testutil` so they
// never reach a production binary.
package testutil

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// Bucket / stream / lane constants used by the test harness. They
// match the production names so any DDL or script behavior that
// references KV bucket names directly (none today) keeps working.
const (
	HarnessCoreBucket   = "core-kv"
	HarnessHealthBucket = "health-kv"
	HarnessCapBucket    = "capability-kv"
	HarnessOpsStream    = "core-operations"
	HarnessEventsStream = "core-events"
)

// ProvisionHarness configures the post-bootstrap KV bucket + stream
// surface that production sets up:
//   - core-kv, health-kv, capability-kv KV buckets (with TTL)
//   - AllowAtomicPublish on the core-kv stream
//   - core-operations JetStream stream
//
// Idempotent. Safe to call repeatedly per test setup.
func ProvisionHarness(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()

	for _, bucket := range []string{HarnessCoreBucket, HarnessHealthBucket, HarnessCapBucket} {
		_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket:         bucket,
			LimitMarkerTTL: time.Second,
		})
		if err != nil {
			t.Fatalf("create KV %q: %v", bucket, err)
		}
	}

	// AllowAtomicPublish on Core KV's backing stream.
	streamName := "KV_" + HarnessCoreBucket
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("get stream %q: %v", streamName, err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("enable AllowAtomicPublish: %v", err)
	}

	// core-operations stream.
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     HarnessOpsStream,
		Subjects: []string{"ops.>"},
	})
	if err != nil {
		t.Fatalf("create core-operations stream: %v", err)
	}

	// core-events stream — step 9 publishes business events (e.g.
	// PackageInstalled from an InstallPackage commit) to events.<class>.
	// Without it step 9 fails and naks for redelivery, replaying the
	// committed op (a benign "duplicate" on the install path but a source
	// of cross-test interference on the shared ops.meta lane).
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     HarnessEventsStream,
		Subjects: []string{"events.>"},
	})
	if err != nil {
		t.Fatalf("create core-events stream: %v", err)
	}
}

// SeedCapDoc writes a Capability KV document for the given actor.
// External test packages use this to seed actor cap docs that grant the
// platformPermissions a test needs (the production projection comes
// from the Refractor's Capability Lens; tests short-circuit it).
func SeedCapDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, doc *processor.CapabilityDoc) {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal cap doc: %v", err)
	}
	if _, err := conn.KVPut(ctx, HarnessCapBucket, doc.Key, b); err != nil {
		t.Fatalf("seed cap doc %s: %v", doc.Key, err)
	}
}

// PipelineConfig configures a CapabilityPipeline.
type PipelineConfig struct {
	Durable      string // consumer durable name; must be unique per test
	Instance     string // health-heartbeater instance label; defaults to durable
	ClaimEmitter processor.ClaimAttemptEmitter
	// FilterSubjects overrides the JetStream consumer's filter subjects.
	// Defaults to []string{"ops.default"} when empty. Use []string{"ops.meta"}
	// for meta-lane pipelines (CreateMetaVertex / TombstoneMetaVertex).
	FilterSubjects []string
}

// CapabilityPipeline builds a CommitPath wired with the real
// CapabilityAuthorizer (reading Capability KV at HarnessCapBucket),
// real DDLCache (from HarnessCoreBucket), real Hydrator + Executor +
// Validator + Committer, a StubEventPublisher, and a JetStream
// consumer bound to the `ops.default` subject. Mirrors the
// newCapabilityPipeline helper that lived in
// internal/processor/role_mgmt_integration_test.go and friends.
func CapabilityPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, cfg PipelineConfig) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	logger := TestLogger()
	metrics := &processor.Metrics{}
	instance := cfg.Instance
	if instance == "" {
		instance = cfg.Durable
	}
	hb := processor.NewHealthHeartbeater(conn, HarnessHealthBucket, instance, 10*time.Second, metrics, logger)
	cache := processor.NewDDLCache(conn, HarnessCoreBucket, logger)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("ddl cache refresh: %v", err)
	}
	authz, err := processor.SelectAuthorizerArgs(processor.SelectAuthorizerOpts{
		Mode:             processor.AuthModeCapability,
		Reader:           conn,
		CapabilityBucket: HarnessCapBucket,
		Logger:           logger,
	})
	if err != nil {
		t.Fatalf("SelectAuthorizerArgs: %v", err)
	}
	committer := processor.NewCommitter(conn, HarnessCoreBucket, cache, logger, time.Now)
	deps := processor.Deps{
		Conn:        conn,
		CoreBucket:  HarnessCoreBucket,
		HealthKV:    HarnessHealthBucket,
		Authorizer:  authz,
		Hydrator:    processor.NewHydratorWithCache(conn, HarnessCoreBucket, cache, logger),
		Executor:    processor.NewExecutor(processor.NewStarlarkRunner(0, 0), logger),
		Validator:   processor.NewValidator(cache, logger),
		Committer:   committer,
		Events:      processor.NewStubEventPublisher(logger),
		Metrics:     metrics,
		Heartbeater: hb,
		Logger:      logger,
	}
	if cfg.ClaimEmitter != nil {
		deps.ClaimEmitter = cfg.ClaimEmitter
	}
	cp := processor.NewCommitPath(deps)
	filterSubjects := cfg.FilterSubjects
	if len(filterSubjects) == 0 {
		filterSubjects = []string{"ops.default"}
	}
	cons, err := processor.EnsureConsumer(ctx, conn.JetStream(), processor.ConsumerConfig{
		StreamName:     HarnessOpsStream,
		Durable:        cfg.Durable,
		FilterSubjects: filterSubjects,
		AckWait:        5 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}
	return cp, cons
}

// PublishOp marshals env and publishes to ops.<lane>. Mirrors the
// per-test publish helpers that previously lived in each integration
// test file.
func PublishOp(t *testing.T, conn *substrate.Conn, env *processor.OperationEnvelope) {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	subject := "ops." + string(env.Lane)
	if env.Lane == "" {
		subject = "ops.default"
	}
	_, err = conn.JetStream().Publish(context.Background(), subject, b)
	if err != nil {
		t.Fatalf("publish to %s: %v", subject, err)
	}
}

// SetupPackageTestEnv composes the standard test harness used by
// package-level integration tests:
//
//  1. Start an embedded NATS server.
//  2. Connect.
//  3. ProvisionHarness (KV buckets + ops stream).
//  4. Generate fresh primordial IDs (in-memory only — no file persist).
//  5. SeedPrimordial (kernel only after Story 4.7).
//  6. InstallPhase1Packages (rbac-domain, identity-domain,
//     identity-hygiene).
//
// Returns ctx + conn ready for cap-doc seeding + pipeline construction.
func SetupPackageTestEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "pkg-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	ProvisionHarness(t, ctx, conn)

	// Bootstrap primordials in-memory only. The test harness owns its
	// own NATS instance; the JSON persistence step (lattice.bootstrap.json)
	// is skipped because nothing else here reads it.
	tmpPath := t.TempDir() + "/lattice-test-bootstrap.json"
	if _, err := bootstrap.LoadOrGenerate(tmpPath); err != nil {
		t.Fatalf("bootstrap.LoadOrGenerate: %v", err)
	}
	seeder, err := bootstrap.NewSeeder(conn.NATS(), TestLogger())
	if err != nil {
		t.Fatalf("bootstrap.NewSeeder: %v", err)
	}
	if err := seeder.SeedPrimordial(ctx); err != nil {
		t.Fatalf("bootstrap.SeedPrimordial: %v", err)
	}
	InstallPhase1Packages(t, ctx, conn)
	return ctx, conn
}
