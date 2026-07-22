// Pipeline + harness helpers for external test packages that exercise
// package-installed DDLs end-to-end.
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

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
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

	// core-events stream — the outbox consumer publishes business events (e.g.
	// PackageInstalled from an InstallPackage commit) to events.<class>.
	// Without it the outbox publish fails and naks for redelivery, replaying the
	// committed op (a benign "duplicate" on the install path but a source
	// of cross-test interference on the shared ops.meta lane). AllowAtomicPublish
	// mirrors production's primordial provisioning (internal/bootstrap/primordial.go)
	// — Conn.PublishBatch requires it on the target stream, or every outbox
	// publish fails closed with "atomic publish is disabled" and nak-loops
	// forever.
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:               HarnessEventsStream,
		Subjects:           []string{"events.>"},
		AllowAtomicPublish: true,
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

// SeedHoldsRole writes the `identity holdsRole role` link a package test's
// ad-hoc actor needs in the GRAPH, not just in its cap doc.
//
// The two are different claims, and package tests had only ever made the
// second. A cap doc says "step 3 will authorize this actor"; the link says
// "this actor holds this role" — what the primordial seed writes for the admin
// and the five service actors (internal/bootstrap/primordial.go), and what the
// kernel's own root-grant lens matches on (MATCH (identity)-[:holdsRole]->
// (role) WHERE role.canonicalName.data.value = 'operator'). An op script that
// asks whether its caller is root — the workplace-confinement guards in
// lease-signing / cafe-domain / clinic-domain / wellness-domain do — reads the
// link, so an actor carrying only a cap doc looks like an unprivileged caller
// no matter what its cap doc grants.
//
// Tests submitting under an operator grant call this so their actor models a
// real operator.
func SeedHoldsRole(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey, roleKey string) {
	t.Helper()
	linkKey := "lnk.identity." + actorKey[len("vtx.identity."):] +
		".holdsRole.role." + roleKey[len("vtx.role."):]
	SeedLink(t, ctx, conn, linkKey, "holdsRole", actorKey, roleKey)
}

// SeedLink writes an alive link document straight to Core KV, for tests that
// need graph topology an op script walks (containment chains, worksAt /
// appliesToUnit / practicesAt / locatedAt edges) without paying for the ops
// that would normally write it.
func SeedLink(t *testing.T, ctx context.Context, conn *substrate.Conn, linkKey, class, source, target string) {
	t.Helper()
	doc := map[string]any{
		"class": class, "isDeleted": false,
		"sourceVertex": source, "targetVertex": target,
		"localName": class, "data": map[string]any{},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal link %s: %v", linkKey, err)
	}
	if _, err := conn.KVPut(ctx, HarnessCoreBucket, linkKey, b); err != nil {
		t.Fatalf("seed link %s: %v", linkKey, err)
	}
}

// testVaultKEK is a fixed, non-secret 32-byte master KEK shared by every
// TestVault instance. Using one constant (rather than a random KEK per call)
// lets independently-constructed LocalBackend instances — one per
// CapabilityPipeline call within a test — decrypt ciphertext minted by any
// other; there is no cross-test isolation concern since this never protects
// real key material.
var testVaultKEK = []byte("lattice-testutil-vault-master-ke")

// TestVault returns a fresh local envelope-encryption Vault backend sealed
// with the shared test KEK (Contract #3 §3.10). Used to wire
// CapabilityPipeline's step-6.5 crypto so sensitive-aspect writers
// (identity-domain's CreateUnclaimedIdentity/RecordIdentityPII/ClaimIdentity)
// round-trip correctly under test.
func TestVault(t *testing.T) vault.Vault {
	t.Helper()
	v, err := vault.NewLocalBackend(testVaultKEK, "test-v1")
	if err != nil {
		t.Fatalf("testutil.TestVault: %v", err)
	}
	return v
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
	// Vault overrides the pipeline's Vault backend. Defaults to a fresh
	// TestVault(t) when nil. Set this when a test needs to observe Vault
	// state a SEPARATE TestVault(t) call would not share — e.g. asserting
	// Decrypt fails after a ShredKey call driven through the same instance
	// (internal/vault/local.go's shredded-set + DEK cache are per-instance
	// in-memory state, not derivable from the KEK alone).
	Vault vault.Vault
}

// CapabilityPipeline builds a CommitPath wired with the real
// CapabilityAuthorizer (reading Capability KV at HarnessCapBucket),
// real DDLCache (from HarnessCoreBucket), real Hydrator + Executor +
// Validator + Committer, and a JetStream consumer bound to the
// `ops.default` subject. Mirrors the
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
	v := cfg.Vault
	if v == nil {
		v = TestVault(t)
	}
	hydrator := processor.NewHydratorWithCache(conn, HarnessCoreBucket, cache, logger)
	hydrator.Vault = v
	committer := processor.NewCommitter(conn, HarnessCoreBucket, cache, logger, time.Now)
	deps := processor.Deps{
		Conn:        conn,
		CoreBucket:  HarnessCoreBucket,
		HealthKV:    HarnessHealthBucket,
		Authorizer:  authz,
		Hydrator:    hydrator,
		Executor:    processor.NewExecutor(processor.NewStarlarkRunner(0, 0), logger),
		Validator:   processor.NewValidator(cache, conn, HarnessCoreBucket, logger),
		Committer:   committer,
		Metrics:     metrics,
		Heartbeater: hb,
		Logger:      logger,
		Vault:       v,
		DDLs:        cache,
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

	EnsurePrimordials(t)
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
