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
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
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

// SeedCredentialActor writes the bare identity vertex a raw sign-in credential
// is, straight to Core KV. This is what the Gateway's first-touch
// ProvisionConsumerIdentity establishes before a person ever reaches a
// ceremony, and ClaimIdentity / CompleteCredentialLink require it: the
// boundTo edge they emit names this vertex as its source, and the projection
// that reads that edge anchors on it, so a claim by an actor with no vertex
// would commit a binding no read model can show.
//
// It mirrors what ProvisionConsumerIdentity's fresh-actor branch commits — the
// vertex, a `.state` of "claimed", and the consumer holdsRole grant — because
// a fixture narrower than the op it stands in for proves only itself: a guard
// added later that reads the actor's state or grant would pass here and fail
// in production. roleKey is the consumer role the caller's package resolves;
// pass "" to seed the vertex and state without a grant, for a test that is
// specifically about an actor holding none.
func SeedCredentialActor(t *testing.T, ctx context.Context, conn *substrate.Conn, actorKey, roleKey string) {
	t.Helper()
	doc := map[string]any{
		"key": actorKey, "class": "identity", "isDeleted": false,
		"data": map[string]any{},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal credential actor %s: %v", actorKey, err)
	}
	if _, err := conn.KVPut(ctx, HarnessCoreBucket, actorKey, b); err != nil {
		t.Fatalf("seed credential actor %s: %v", actorKey, err)
	}

	state := map[string]any{
		"class": "state", "vertexKey": actorKey, "localName": "state",
		"isDeleted": false, "data": map[string]any{"value": "claimed"},
	}
	sb, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal credential actor state %s: %v", actorKey, err)
	}
	if _, err := conn.KVPut(ctx, HarnessCoreBucket, actorKey+".state", sb); err != nil {
		t.Fatalf("seed credential actor state %s: %v", actorKey, err)
	}

	if roleKey != "" {
		SeedHoldsRole(t, ctx, conn, actorKey, roleKey)
	}
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
	// RbacRolesActive routes the platform capability read by actor class, the
	// posture production always runs (processor.SelectAuthorizerOpts's own
	// doc: "PRODUCTION ALWAYS SETS THIS TRUE"): the actors named in
	// SystemActorKeys read a UNION of cap.<actor> and cap.roles.<actor>,
	// every other actor reads cap.roles.<actor> alone. Left false, the
	// pipeline uses the rbac-absent fallback — cap.<actor> for EVERY actor —
	// which is where most harness fixtures seed their docs. Set it when a
	// test's meaning depends on an actor's CLASS, so the Processor routes the
	// way the deployment would rather than the way the fixture is convenient.
	RbacRolesActive bool
	// SystemActorKeys is the root system-actor set — the identities holding the
	// primordial `operator` role — the class-aware routing consults. Only read
	// when RbacRolesActive; empty means every actor is ordinary.
	SystemActorKeys []string
	// ClaimRejectionFloor threads processor.Deps.ClaimRejectionFloor — the reply
	// floor that equalizes ClaimIdentity's three rejection causes in the time
	// domain (internal/processor/claim_reply_floor.go). Zero keeps the
	// production default (processor.DefaultClaimRejectionFloor); a NEGATIVE
	// value disables the floor, which is what an instrument measuring the raw
	// per-cause service-time gap needs.
	ClaimRejectionFloor time.Duration
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
		RbacRolesActive:  cfg.RbacRolesActive,
		SystemActorKeys:  cfg.SystemActorKeys,
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
	hydrator.PrimordialActors = PrimordialActors(t)
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

		ClaimRejectionFloor: cfg.ClaimRejectionFloor,
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

// SubmitAndAwaitReply publishes env carrying a reply inbox, drives exactly one
// message through cp, and returns the outcome together with the Processor's
// reply. The reply is what makes a denial testable: MessageOutcome collapses
// every rejection into "rejected", so an outcome-only assertion cannot tell an
// authorization denial from a payload-validation error — which is precisely the
// distinction a caller must not be able to observe on a resource they do not
// own. Tests that assert WHY an op was rejected use this; the rest use
// PublishOp + DriveOne.
//
// The subscription is established before the publish, so the reply cannot be
// missed, and the receive is a bounded wait on a real message rather than a
// sleep.
func SubmitAndAwaitReply(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	env *processor.OperationEnvelope) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	subject := "ops." + string(env.Lane)
	if env.Lane == "" {
		subject = "ops.default"
	}

	// nats.NewInbox rather than a subject derived from the requestId: core NATS
	// fans a publish out to EVERY matching subscriber, so two calls that shared
	// a subject would each receive the other's reply and assert against it. A
	// requestId is caller-supplied and deterministic (GenReqID has no
	// randomness), so a copied label would collide silently.
	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	if err != nil {
		t.Fatalf("subscribe %s: %v", inbox, err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := conn.NATS().Flush(); err != nil {
		t.Fatalf("flush subscription: %v", err)
	}

	msg := &nats.Msg{
		Subject: subject,
		Data:    b,
		Header:  nats.Header{replyInboxHeader: []string{inbox}},
	}
	if _, err := conn.JetStream().PublishMsg(ctx, msg); err != nil {
		t.Fatalf("publish to %s: %v", subject, err)
	}

	outcome := DriveOne(t, ctx, cp, cons, "")

	replyMsg, err := sub.NextMsg(replyWait)
	if err != nil {
		t.Fatalf("no reply on %s within %s (outcome %q): %v", inbox, replyWait, outcome, err)
	}
	var reply processor.OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	return outcome, &reply
}

// replyInboxHeader is the header the Processor reads to find a caller's reply
// subject when the op arrived over JetStream rather than request-reply.
const replyInboxHeader = "Lattice-Reply-Inbox"

// replyWait bounds the wait for a reply the Processor has already been driven
// to produce — a ceiling that only trips on a real defect, not a timing
// assumption.
const replyWait = 30 * time.Second

// PrimordialActors is the trusted-engine actor map a harness pipeline wires
// into the Hydrator, mirroring cmd/processor's AuthWiring.PrimordialActors.
//
// A test driving an op that pins op.actor against `primordialActor["loom"]`
// must ALSO submit as bootstrap.LoomIdentityKey (and likewise
// `primordialActor["weaver"]` / bootstrap.WeaverIdentityKey) — wiring this map
// is what makes that actor the accepted one, not what exempts the op from the
// check.
//
// It reads internal/bootstrap's primordial globals, which EnsurePrimordials
// populates (SetupPackageTestEnv does that), and FAILS the test if they are
// still blank. Returning a half-empty map instead would wire a registry whose
// every guard denies every actor, and the test would fail several steps later
// as an unexplained AuthDenied on the real dispatch path — a fixture ordering
// bug wearing the costume of the security behaviour under test.
func PrimordialActors(t *testing.T) map[string]string {
	t.Helper()
	actors := map[string]string{
		"loom":   bootstrap.LoomIdentityKey,
		"weaver": bootstrap.WeaverIdentityKey,
	}
	for name, key := range actors {
		if strings.TrimSpace(key) == "" {
			t.Fatalf("testutil.PrimordialActors: bootstrap key for %q is empty — call EnsurePrimordials(t) (or SetupPackageTestEnv) before building a pipeline", name)
		}
	}
	return actors
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
