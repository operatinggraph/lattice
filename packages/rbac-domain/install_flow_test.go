// Story 1.5.5 — install-flow integration tests proving the three
// guarantees the "route installs through the Processor" change must hold:
//
//  1. M5/B2 cache coherence — install a package via InstallPackage, then
//     submit a domain op on a class the package JUST declared, on the SAME
//     running Processor (same shared DDL cache, no restart), and assert it
//     commits. This proves step 8's in-commit vtx.meta.* invalidation makes
//     the new class usable immediately.
//  2. F-001 orphan-free — install -> uninstall -> re-install leaves no live
//     orphans (every declared key from the first install is tombstoned by
//     uninstall, and a clean re-install onto a fresh keyspace succeeds).
//  3. Kernel protection (§3.4) — a TombstoneMetaVertex / UpdateMetaVertex
//     against a protected primordial meta-vertex (the meta-root DDL) is
//     rejected with ProtectedMetaVertex.
package rbacdomain_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	identitydomain "github.com/asolgan/lattice/packages/identity-domain"
	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
)

// sharedCachePipeline builds a stub-auth CommitPath over an explicit,
// caller-supplied DDLCache and starts a consumer bound to filterSubjects.
// Returning the cache lets a test refresh it BEFORE an install and then
// observe that the install's in-commit invalidation (step 8) updated the
// SAME cache instance — the no-restart M5/B2 proof.
func sharedCachePipeline(
	t *testing.T, ctx context.Context, conn *substrate.Conn,
	cache *processor.DDLCache, durable string, filterSubjects []string,
) func() {
	t.Helper()
	logger := testutil.TestLogger()
	metrics := &processor.Metrics{}
	hb := processor.NewHealthHeartbeater(conn, testutil.HarnessHealthBucket, durable, 10*time.Second, metrics, logger)
	committer := processor.NewCommitter(conn, testutil.HarnessCoreBucket, cache, logger, time.Now)
	cp := processor.NewCommitPath(processor.Deps{
		Conn:        conn,
		CoreBucket:  testutil.HarnessCoreBucket,
		HealthKV:    testutil.HarnessHealthBucket,
		Authorizer:  processor.NewStubAuthorizer(logger),
		Hydrator:    processor.NewHydratorWithCache(conn, testutil.HarnessCoreBucket, cache, logger),
		Executor:    processor.NewExecutor(processor.NewStarlarkRunner(0, 0), logger),
		Validator:   processor.NewValidator(cache, logger),
		Committer:   committer,
		Events:      processor.NewStubEventPublisher(logger),
		Metrics:     metrics,
		Heartbeater: hb,
		Logger:      logger,
	})
	cons, err := processor.EnsureConsumer(ctx, conn.JetStream(), processor.ConsumerConfig{
		StreamName:     testutil.HarnessOpsStream,
		Durable:        durable,
		FilterSubjects: filterSubjects,
		AckWait:        5 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	cc, err := cons.Consume(func(m jetstream.Msg) { cp.HandleMessage(runCtx, m) })
	if err != nil {
		cancel()
		t.Fatalf("Consume: %v", err)
	}
	return func() {
		cc.Stop()
		cancel()
		_ = conn.JetStream().DeleteConsumer(context.Background(), testutil.HarnessOpsStream, durable)
	}
}

// bareKernelEnv stands up the test harness WITHOUT installing the phase-1
// packages — just the embedded NATS + buckets + streams + primordial
// kernel (which includes the InstallPackage DDL). The caller drives the
// install itself so it can observe cache coherence.
func bareKernelEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "install-flow-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	testutil.ProvisionHarness(t, ctx, conn)

	tmpPath := t.TempDir() + "/lattice-test-bootstrap.json"
	if _, err := bootstrap.LoadOrGenerate(tmpPath); err != nil {
		t.Fatalf("bootstrap.LoadOrGenerate: %v", err)
	}
	seeder, err := bootstrap.NewSeeder(conn.NATS(), testutil.TestLogger())
	if err != nil {
		t.Fatalf("bootstrap.NewSeeder: %v", err)
	}
	if err := seeder.SeedPrimordial(ctx); err != nil {
		t.Fatalf("bootstrap.SeedPrimordial: %v", err)
	}
	return ctx, conn
}

// TestInstallFlow_M5B2_DomainOpWithoutRestart installs rbac-domain through
// the Processor, then — on the SAME running Processor (a single shared DDL
// cache that did NOT contain the `rbac` class at refresh time) — submits a
// CreateRole op on the just-declared `rbac` class and asserts it commits.
func TestInstallFlow_M5B2_DomainOpWithoutRestart(t *testing.T) {
	ctx, conn := bareKernelEnv(t)

	// One shared cache, refreshed BEFORE the install — it does NOT yet know
	// the `rbac` class. The install's step-8 in-commit invalidation must
	// update THIS instance for the later domain op to resolve.
	cache := processor.NewDDLCache(conn, testutil.HarnessCoreBucket, testutil.TestLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("initial cache refresh: %v", err)
	}
	if _, ok := cache.Lookup("rbac"); ok {
		t.Fatal("precondition: `rbac` class must be ABSENT before install")
	}

	// A single pipeline (shared cache) consuming both meta (install) and
	// default (domain op) lanes — i.e. one Processor process, no restart.
	stop := sharedCachePipeline(t, ctx, conn, cache, "m5b2-shared", []string{"ops.meta", "ops.default"})
	defer stop()

	// Install rbac-domain via InstallPackage through this pipeline.
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst.Install(ctx, rbacdomain.Package); err != nil {
		t.Fatalf("install rbac-domain: %v", err)
	}

	// The class must now be resolvable on the SAME shared cache — proof the
	// in-commit invalidation fired (no restart, no manual refresh).
	if _, ok := cache.Lookup("rbac"); !ok {
		t.Fatal("M5/B2: `rbac` class not in the shared cache after install — in-commit invalidation did not fire")
	}

	// Submit a domain op on the just-declared class and assert it commits.
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("M5B2CreateRole00001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateRole",
		Actor:         bootstrap.BootstrapIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Class:         "rbac",
		Payload:       json.RawMessage(`{"name":"M5B2Role","description":"created without a Processor restart"}`),
	}
	reqID := env.RequestID
	testutil.PublishOp(t, conn, env)

	// Drive the default-lane op on the same running pipeline. Wait for the
	// tracker to materialize (the shared consumer processes it async).
	deadline := time.Now().Add(20 * time.Second)
	var committed bool
	for time.Now().Before(deadline) {
		if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.TrackerKey(reqID)); err == nil {
			committed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !committed {
		t.Fatal("M5/B2: CreateRole on the just-declared class did not commit (no tracker) — class not usable without restart")
	}
}

// TestInstallFlow_F001_ReinstallNoOrphans installs identity-domain (which
// declares roles formerly seeded substrate-direct — the F-001 surface),
// uninstalls it, and asserts every declared key is tombstoned (no live
// orphan), then re-installs onto a fresh keyspace and asserts it succeeds.
func TestInstallFlow_F001_ReinstallNoOrphans(t *testing.T) {
	ctx, conn := bareKernelEnv(t)
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()

	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}

	// rbac-domain first (identity-domain depends on it for grant targets;
	// the dependency is warn-only but we install it so the env is realistic).
	if _, err := inst.Install(ctx, rbacdomain.Package); err != nil {
		t.Fatalf("install rbac-domain: %v", err)
	}

	res, err := inst.Install(ctx, identityDomainPkg())
	if err != nil {
		t.Fatalf("install identity-domain: %v", err)
	}
	if len(res.DeclaredKeys) == 0 {
		t.Fatal("identity-domain install declared no keys")
	}
	// The folded roles must be present in declaredKeys (F-001: they used to
	// be seeded substrate-direct and orphaned at uninstall).
	assertRoleKeysDeclared(t, res.DeclaredKeys)

	// Uninstall — every declared key must end up tombstoned (no live orphan).
	if _, err := inst.Uninstall(ctx, "identity-domain"); err != nil {
		t.Fatalf("uninstall identity-domain: %v", err)
	}
	for _, k := range res.DeclaredKeys {
		entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, k)
		if err != nil {
			t.Fatalf("post-uninstall read %s: %v", k, err)
		}
		var env struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if json.Unmarshal(entry.Value, &env) == nil && !env.IsDeleted {
			t.Fatalf("F-001 orphan: declared key %s is still LIVE after uninstall", k)
		}
	}

	// Re-install onto a fresh keyspace must succeed (idempotent, orphan-free).
	ctx2, conn2 := bareKernelEnv(t)
	stop2 := testutil.RunMetaInstallPipeline(t, ctx2, conn2)
	defer stop2()
	inst2 := pkgmgr.NewInstaller(conn2, bootstrap.BootstrapIdentityKey)
	inst2.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	if _, err := inst2.Install(ctx2, rbacdomain.Package); err != nil {
		t.Fatalf("reinstall rbac-domain: %v", err)
	}
	if _, err := inst2.Install(ctx2, identityDomainPkg()); err != nil {
		t.Fatalf("reinstall identity-domain: %v", err)
	}
}

// TestInstallFlow_ProtectedMetaVertexRejected asserts the §3.4 kernel
// protection: a TombstoneMetaVertex (and an UpdateMetaVertex) targeting a
// protected primordial meta-vertex (the meta-root DDL) is rejected.
func TestInstallFlow_ProtectedMetaVertexRejected(t *testing.T) {
	ctx, conn := bareKernelEnv(t)
	cache := processor.NewDDLCache(conn, testutil.HarnessCoreBucket, testutil.TestLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("cache refresh: %v", err)
	}
	stop := sharedCachePipeline(t, ctx, conn, cache, "protected-meta", []string{"ops.meta"})
	defer stop()

	protectedKey := bootstrap.MetaRootKey

	// Tombstone against the protected meta-root DDL must be rejected.
	tombstoneEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("ProtTombstone000001"),
		Lane:          processor.LaneMeta,
		OperationType: "TombstoneMetaVertex",
		Actor:         bootstrap.BootstrapIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Class:         "root",
		Payload:       json.RawMessage(`{"metaKey":"` + protectedKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{protectedKey}},
	}
	assertRejectedNoTombstone(t, ctx, conn, tombstoneEnv, protectedKey)

	// Update against the same protected key must also be rejected.
	updateEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("ProtUpdate000000001"),
		Lane:          processor.LaneMeta,
		OperationType: "UpdateMetaVertex",
		Actor:         bootstrap.BootstrapIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Class:         "root",
		Payload:       json.RawMessage(`{"metaKey":"` + protectedKey + `","description":"hijacked"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{protectedKey}},
	}
	assertRejectedNoTombstone(t, ctx, conn, updateEnv, protectedKey)
}

// TestInstallFlow_UninstallProtectedKeyRejected is the authoritative-path
// proof (CR KP-2): it submits a crafted UninstallPackage op whose declaredKeys
// includes a protected kernel key (the meta-root DDL) and asserts the Processor
// commit-time guard (step 8 rejectProtectedMutations) REJECTS the whole op with
// ErrCodeProtectedKey and leaves the protected key un-tombstoned. This exercises
// the REAL authoritative path — the install/uninstall scripts perform no
// protected-key check (empty hydrated state), so without the Processor guard
// this op would tombstone the kernel and brick auth.
func TestInstallFlow_UninstallProtectedKeyRejected(t *testing.T) {
	ctx, conn := bareKernelEnv(t)
	cache := processor.NewDDLCache(conn, testutil.HarnessCoreBucket, testutil.TestLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("cache refresh: %v", err)
	}
	stop := sharedCachePipeline(t, ctx, conn, cache, "uninstall-protected", []string{"ops.meta"})
	defer stop()

	protectedKey := bootstrap.MetaRootKey

	before, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, protectedKey)
	if err != nil {
		t.Fatalf("read protected key before: %v", err)
	}

	// A crafted UninstallPackage op naming the protected meta-root DDL among
	// its declaredKeys. The script emits an unconditional tombstone for it;
	// only the Processor guard stands between this op and a bricked kernel.
	uninstallEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("UninstProtected00001"),
		Lane:          processor.LaneMeta,
		OperationType: "UninstallPackage",
		Actor:         bootstrap.BootstrapIdentityKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		Class:         "UninstallPackage",
		Payload:       json.RawMessage(`{"name":"malicious","declaredKeys":["` + protectedKey + `"]}`),
	}

	reply := submitForReply(t, ctx, conn, uninstallEnv)
	if reply.Status != processor.ReplyStatusRejected {
		t.Fatalf("UninstallPackage of protected key: status = %q, want rejected", reply.Status)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeProtectedKey {
		t.Fatalf("UninstallPackage of protected key: rejection code = %+v, want ProtectedKey", reply.Error)
	}

	after, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, protectedKey)
	if err != nil {
		t.Fatalf("read protected key after: %v", err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("protected key %s was mutated (rev %d -> %d) — Processor guard did not fail closed",
			protectedKey, before.Revision, after.Revision)
	}
	var env2 struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if json.Unmarshal(after.Value, &env2) == nil && env2.IsDeleted {
		t.Fatalf("protected key %s was tombstoned — Processor guard did not fail closed", protectedKey)
	}
}

// assertRejectedNoTombstone submits env (capturing the reply on an inbox),
// asserts the reply is a rejection whose error mentions ProtectedMetaVertex,
// and confirms the target key was NOT mutated (same revision, still live).
func assertRejectedNoTombstone(t *testing.T, ctx context.Context, conn *substrate.Conn, env *processor.OperationEnvelope, protectedKey string) {
	t.Helper()
	before, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, protectedKey)
	if err != nil {
		t.Fatalf("read protected key before: %v", err)
	}

	reply := submitForReply(t, ctx, conn, env)
	if reply.Status != processor.ReplyStatusRejected {
		t.Fatalf("%s against protected key: status = %q, want rejected", env.OperationType, reply.Status)
	}
	if reply.Error == nil || !containsStr(string(reply.Error.Code)+" "+reply.Error.Message, "ProtectedMetaVertex") {
		t.Fatalf("%s against protected key: rejection did not mention ProtectedMetaVertex: %+v", env.OperationType, reply.Error)
	}

	after, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, protectedKey)
	if err != nil {
		t.Fatalf("read protected key after: %v", err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("protected key %s was mutated (rev %d -> %d) — guard did not reject %s",
			protectedKey, before.Revision, after.Revision, env.OperationType)
	}
	var env2 struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if json.Unmarshal(after.Value, &env2) == nil && env2.IsDeleted {
		t.Fatalf("protected key %s was tombstoned — guard did not reject %s", protectedKey, env.OperationType)
	}
}

// submitForReply publishes env with a reply inbox and returns the
// Processor's OperationReply.
func submitForReply(t *testing.T, ctx context.Context, conn *substrate.Conn, env *processor.OperationEnvelope) *processor.OperationReply {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	if err != nil {
		t.Fatalf("subscribe inbox: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	subject := "ops." + string(env.Lane)
	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header:  nats.Header{"Lattice-Reply-Inbox": []string{inbox}},
	}
	bctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := conn.JetStream().PublishMsg(bctx, msg); err != nil {
		t.Fatalf("publish to %s: %v", subject, err)
	}
	replyMsg, err := sub.NextMsgWithContext(bctx)
	if err != nil {
		t.Fatalf("wait for reply: %v", err)
	}
	var reply processor.OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		t.Fatalf("parse reply: %v", err)
	}
	return &reply
}

func containsStr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// assertRoleKeysDeclared confirms the folded user-facing roles (formerly
// substrate-direct PreInstall) are in the install's declaredKeys: >=3 role
// vertices + >=3 roleindex vertices. This is the F-001 fix — these keys are
// now reclaimed at uninstall instead of orphaned.
func assertRoleKeysDeclared(t *testing.T, declared []string) {
	t.Helper()
	roleVtx, roleIdx := 0, 0
	for _, k := range declared {
		if hasPrefix(k, "vtx.role.") && countDots(k) == 2 {
			roleVtx++
		}
		if hasPrefix(k, "vtx.roleindex.") {
			roleIdx++
		}
	}
	if roleVtx < 3 {
		t.Fatalf("F-001 fold: expected >=3 declared role vertices, got %d", roleVtx)
	}
	if roleIdx < 3 {
		t.Fatalf("F-001 fold: expected >=3 declared roleindex vertices, got %d", roleIdx)
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func countDots(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			n++
		}
	}
	return n
}

// identityDomainPkg returns the identity-domain Definition.
func identityDomainPkg() pkgmgr.Definition {
	return identitydomain.Package
}
