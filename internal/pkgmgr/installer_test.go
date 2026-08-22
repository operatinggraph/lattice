package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// startEmbeddedNATS spins up an in-process JetStream-enabled NATS server
// for the installer integration tests. Mirrors the harness used in
// internal/processor.
func startEmbeddedNATS(t *testing.T) string {
	t.Helper()
	s := natsfixture.StartServer(t)
	return s.ClientURL()
}

// newInstallerHarness boots NATS + creates the core-kv bucket with
// AllowAtomicPublish enabled (the installer's only KV bucket).
func newInstallerHarness(t *testing.T) (context.Context, *substrate.Conn, *Installer) {
	t.Helper()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "pkgmgr-installer-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	js := conn.JetStream()
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         CoreBucket,
		LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatalf("create %s bucket: %v", CoreBucket, err)
	}
	// Health KV — the pipeline's heartbeater writes here.
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         "health-kv",
		LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatalf("create health-kv bucket: %v", err)
	}
	streamName := "KV_" + CoreBucket
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("get stream %q: %v", streamName, err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("enable AllowAtomicPublish: %v", err)
	}
	// ops + events streams — installs route through the Processor as
	// InstallPackage ops (Story 1.5.5).
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "core-operations",
		Subjects: []string{"ops.>"},
	}); err != nil {
		t.Fatalf("create core-operations stream: %v", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "core-events",
		Subjects: []string{"events.>"},
	}); err != nil {
		t.Fatalf("create core-events stream: %v", err)
	}

	// Seed primordials so the InstallPackage / UninstallPackage DDLs +
	// admin identity + operator role exist and installs can route through
	// the Processor.
	//
	// Stays on the direct call (not testutil.EnsurePrimordials): this file is
	// `package pkgmgr` (internal test), and testutil imports pkgmgr
	// (install_phase1_packages.go) — importing testutil here closes an import
	// cycle. Exempted the same way internal/bootstrap's own suite is.
	tmpPath := t.TempDir() + "/lattice-test-bootstrap.json"
	if _, err := bootstrap.LoadOrGenerate(tmpPath); err != nil {
		t.Fatalf("bootstrap.LoadOrGenerate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	seeder, err := bootstrap.NewSeeder(conn.NATS(), logger)
	if err != nil {
		t.Fatalf("bootstrap.NewSeeder: %v", err)
	}
	if err := seeder.SeedPrimordial(ctx); err != nil {
		t.Fatalf("bootstrap.SeedPrimordial: %v", err)
	}

	// Run a real meta-lane stub-auth pipeline so submitted InstallPackage /
	// UninstallPackage ops are consumed end-to-end (real DDL script, step-6
	// validation, step-8 atomic commit; only auth is stubbed).
	stop := runMetaPipeline(t, ctx, conn, logger)
	t.Cleanup(stop)

	inst := NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{
		"operator": bootstrap.RoleOperatorID,
	}
	return ctx, conn, inst
}

// runMetaPipeline stands up a stub-auth CommitPath bound to ops.meta and
// starts consuming. Returns a stop func the caller must defer/Cleanup. On
// stop it deletes the durable and purges committed install ops so they do
// not interfere with other consumers. Mirrors testutil.RunMetaInstallPipeline
// (reproduced here to avoid the testutil→pkgmgr import cycle).
func runMetaPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, logger *slog.Logger) func() {
	t.Helper()
	cp, _, err := processor.MakeStubPipeline(conn, CoreBucket, "health-kv", processor.AuthModeStub, logger, "pkgmgr-test-meta")
	if err != nil {
		t.Fatalf("MakeStubPipeline: %v", err)
	}
	cons, err := processor.EnsureConsumer(ctx, conn.JetStream(), processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "pkgmgr-test-meta",
		FilterSubjects: []string{"ops.meta"},
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
		_ = conn.JetStream().DeleteConsumer(context.Background(), "core-operations", "pkgmgr-test-meta")
		if s, err := conn.JetStream().Stream(context.Background(), "core-operations"); err == nil {
			_ = s.Purge(context.Background(), jetstream.WithPurgeSubject("ops.meta"))
		}
	}
}

// newDualLaneInstallerHarness is newInstallerHarness's setup, except its one
// stub-auth CommitPath consumes BOTH ops.meta (package-lifecycle ops) and
// ops.default (a normal business op, e.g. CancelTask, never routed on the
// meta lane). A single CommitPath is load-bearing here, not cosmetic: the
// DDL cache a CommitPath resolves operationTypes against is populated by its
// own CDC watch over vtx.meta.>, so two independent CommitPath instances
// each carry an independently-lagging cache — a DDL installed on one
// instance is not guaranteed visible yet on the other's. One instance
// serving both lanes has exactly one cache, so install-then-submit within a
// test never races it. Lives here (not a caller's own test file) because
// only this file is exempted from the direct bootstrap.LoadOrGenerate rule
// (see the comment below).
func newDualLaneInstallerHarness(t *testing.T) (context.Context, *substrate.Conn, *Installer) {
	t.Helper()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "pkgmgr-dual-lane-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	js := conn.JetStream()
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         CoreBucket,
		LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatalf("create %s bucket: %v", CoreBucket, err)
	}
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         "health-kv",
		LimitMarkerTTL: time.Second,
	}); err != nil {
		t.Fatalf("create health-kv bucket: %v", err)
	}
	streamName := "KV_" + CoreBucket
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("get stream %q: %v", streamName, err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("enable AllowAtomicPublish: %v", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "core-operations",
		Subjects: []string{"ops.>"},
	}); err != nil {
		t.Fatalf("create core-operations stream: %v", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "core-events",
		Subjects: []string{"events.>"},
	}); err != nil {
		t.Fatalf("create core-events stream: %v", err)
	}

	// Stays on the direct call (not testutil.EnsurePrimordials): this file is
	// package pkgmgr (internal test), and testutil imports pkgmgr — see
	// newInstallerHarness's identical note above (loadOrGenerateExemptFile,
	// scripts/lint-conventions.go).
	tmpPath := t.TempDir() + "/lattice-test-bootstrap.json"
	if _, err := bootstrap.LoadOrGenerate(tmpPath); err != nil {
		t.Fatalf("bootstrap.LoadOrGenerate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	seeder, err := bootstrap.NewSeeder(conn.NATS(), logger)
	if err != nil {
		t.Fatalf("bootstrap.NewSeeder: %v", err)
	}
	if err := seeder.SeedPrimordial(ctx); err != nil {
		t.Fatalf("bootstrap.SeedPrimordial: %v", err)
	}

	stop := runDualLanePipeline(t, ctx, conn, logger)
	t.Cleanup(stop)

	inst := NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	return ctx, conn, inst
}

// runDualLanePipeline stands up one stub-auth CommitPath and two consumers
// against it, ops.meta and ops.default, so package-lifecycle ops and a
// business op like CancelTask share one DDL cache (see
// newDualLaneInstallerHarness's doc comment for why that has to be one
// instance, not two).
func runDualLanePipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, logger *slog.Logger) func() {
	t.Helper()
	cp, _, err := processor.MakeStubPipeline(conn, CoreBucket, "health-kv", processor.AuthModeStub, logger, "pkgmgr-test-dual")
	if err != nil {
		t.Fatalf("MakeStubPipeline: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)

	metaCons, err := processor.EnsureConsumer(ctx, conn.JetStream(), processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "pkgmgr-test-dual-meta",
		FilterSubjects: []string{"ops.meta"},
		AckWait:        5 * time.Second,
	}, logger)
	if err != nil {
		cancel()
		t.Fatalf("EnsureConsumer meta: %v", err)
	}
	defaultCons, err := processor.EnsureConsumer(ctx, conn.JetStream(), processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "pkgmgr-test-dual-default",
		FilterSubjects: []string{"ops.default"},
		AckWait:        5 * time.Second,
	}, logger)
	if err != nil {
		cancel()
		t.Fatalf("EnsureConsumer default: %v", err)
	}
	metaCC, err := metaCons.Consume(func(m jetstream.Msg) { cp.HandleMessage(runCtx, m) })
	if err != nil {
		cancel()
		t.Fatalf("Consume meta: %v", err)
	}
	defaultCC, err := defaultCons.Consume(func(m jetstream.Msg) { cp.HandleMessage(runCtx, m) })
	if err != nil {
		metaCC.Stop()
		cancel()
		t.Fatalf("Consume default: %v", err)
	}
	return func() {
		metaCC.Stop()
		defaultCC.Stop()
		cancel()
		_ = conn.JetStream().DeleteConsumer(context.Background(), "core-operations", "pkgmgr-test-dual-meta")
		_ = conn.JetStream().DeleteConsumer(context.Background(), "core-operations", "pkgmgr-test-dual-default")
		if s, err := conn.JetStream().Stream(context.Background(), "core-operations"); err == nil {
			_ = s.Purge(context.Background(), jetstream.WithPurgeSubject("ops.meta"))
			_ = s.Purge(context.Background(), jetstream.WithPurgeSubject("ops.default"))
		}
	}
}

func sampleDef(version string) Definition {
	return Definition{
		Name:        "sample-pkg",
		Version:     version,
		Description: "Sample package for installer tests.",
		DDLs: []DDLSpec{
			{
				CanonicalName:     "sampleClass",
				Class:             "meta.ddl.vertexType",
				PermittedCommands: []string{"SampleOp"},
				Description:       "sample",
				Script:            "def execute(state, op):\n    return {\"mutations\": [], \"events\": []}\n",
				InputSchema:       `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`,
				OutputSchema:      `{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`,
				FieldDescription:  map[string]string{"id": "Sample entity ID."},
				Examples: []ExampleSpec{
					{Name: "SampleOp example", Payload: map[string]any{"id": "abc"}, ExpectedOutcome: "Creates sample vertex."},
				},
			},
		},
		Lenses: []LensSpec{
			{
				CanonicalName: "sampleLens",
				Class:         "meta.lens",
				Adapter:       "nats-kv",
				Bucket:        "sample-bucket",
				Engine:        "full",
				Spec:          `MATCH (n:sample) RETURN n.key AS key`,
			},
		},
		Permissions: []PermissionSpec{
			{
				OperationType: "SampleOp",
				Scope:         "any",
				Note:          "sample grant",
				GrantsTo:      []string{"operator"},
			},
		},
	}
}

// otherDef returns a second synthetic package (distinct Name) whose single
// DDL canonicalName is the supplied value, so a test can choose whether it
// collides with an already-installed package's meta canonicalName.
func otherDef(version, ddlCanonical string) Definition {
	return Definition{
		Name:        "other-pkg",
		Version:     version,
		Description: "Second package for collision tests.",
		DDLs: []DDLSpec{
			{
				CanonicalName:     ddlCanonical,
				Class:             "meta.ddl.vertexType",
				PermittedCommands: []string{"OtherOp"},
				Description:       "other",
				Script:            "def execute(state, op):\n    return {\"mutations\": [], \"events\": []}\n",
				InputSchema:       `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`,
				OutputSchema:      `{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`,
				FieldDescription:  map[string]string{"id": "Other entity ID."},
				Examples: []ExampleSpec{
					{Name: "OtherOp example", Payload: map[string]any{"id": "xyz"}, ExpectedOutcome: "Creates other vertex."},
				},
			},
		},
	}
}

// TestInstaller_RejectsCanonicalNameCollision installs package A, then a
// package B (distinct name) whose DDL reuses A's lens canonicalName. The
// second install must fail with ErrCanonicalNameCollision; a non-colliding B
// then installs cleanly.
func TestInstaller_RejectsCanonicalNameCollision(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("install A: %v", err)
	}

	// B reuses A's lens canonicalName ("sampleLens") on its DDL → collision.
	colliding := otherDef("0.1.0", "sampleLens")
	_, err := inst.Install(ctx, colliding)
	if err == nil {
		t.Fatal("expected ErrCanonicalNameCollision installing a package that reuses an installed canonicalName, got nil")
	}
	if !errors.Is(err, ErrCanonicalNameCollision) {
		t.Fatalf("expected ErrCanonicalNameCollision, got %v", err)
	}
	if !strings.Contains(err.Error(), "sampleLens") {
		t.Errorf("collision error should name the colliding canonicalName; got %v", err)
	}

	// A non-colliding B installs fine.
	clean := otherDef("0.1.0", "otherClass")
	if _, err := inst.Install(ctx, clean); err != nil {
		t.Fatalf("non-colliding package should install, got: %v", err)
	}
}

// TestInstaller_CollisionCheckPreservesIdempotency asserts the against-installed
// collision scan does not break re-install idempotency or version-mismatch
// detection: re-installing the same name+version still skips (the scan must not
// see the package's own previously-written meta-vertices as a self-collision),
// and a different-version re-install still returns ErrVersionMismatch.
func TestInstaller_CollisionCheckPreservesIdempotency(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("first install: %v", err)
	}

	// Same name+version re-install skips idempotently (no false self-collision).
	res, err := inst.Install(ctx, sampleDef("0.1.0"))
	if err != nil {
		t.Fatalf("re-install same version: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected Skipped=true on same-version re-install, got %+v", res)
	}

	// Different version still returns ErrVersionMismatch (collision check must
	// not preempt the version-mismatch path).
	_, err = inst.Install(ctx, sampleDef("0.2.0"))
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("expected ErrVersionMismatch on different-version re-install, got %v", err)
	}
}

// weaverTargetDef returns a synthetic package declaring one lens and one weaver
// target that references that lens by canonicalName, with the given targetId.
// Distinct pkgName/lensCanonical/ddlCanonical let a test collide on targetId
// alone (not a canonicalName).
func weaverTargetDef(pkgName, ddlCanonical, lensCanonical, targetID, version string) Definition {
	return Definition{
		Name:    pkgName,
		Version: version,
		DDLs: []DDLSpec{
			{
				CanonicalName:     ddlCanonical,
				Class:             "meta.ddl.vertexType",
				PermittedCommands: []string{"WtOp"},
				Description:       "wt",
				Script:            "def execute(state, op):\n    return {\"mutations\": [], \"events\": []}\n",
				InputSchema:       `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`,
				OutputSchema:      `{"type":"object","properties":{"key":{"type":"string"}},"required":["key"]}`,
				FieldDescription:  map[string]string{"id": "Weaver-target entity ID."},
				Examples: []ExampleSpec{
					{Name: "WtOp example", Payload: map[string]any{"id": "abc"}, ExpectedOutcome: "Creates wt vertex."},
				},
			},
		},
		Lenses: []LensSpec{
			{
				CanonicalName: lensCanonical,
				Class:         "meta.lens",
				Adapter:       "nats-kv",
				Bucket:        "wt-bucket",
				Engine:        "full",
				Spec:          `MATCH (n:wt) RETURN n.key AS key`,
			},
		},
		WeaverTargets: []WeaverTargetSpec{
			{
				TargetID: targetID,
				LensRef:  lensCanonical,
				Gaps: map[string]GapActionSpec{
					"missing_x": {Action: "directOp", Operation: "WtOp"},
				},
			},
		},
	}
}

// TestInstaller_RejectsWeaverTargetIDCollision installs package A declaring
// weaver targetId "foo", then a distinct package B also declaring "foo" (with
// its own lens/DDL canonicalNames so the ONLY collision is the targetId). B must
// fail with ErrWeaverTargetIDCollision — a weaver target has no canonicalName
// aspect, so checkCanonicalNameCollision cannot catch this; the §10.8
// cross-target uniqueness check must. Re-installing A stays idempotent (the
// targetId scan must not see A's own prior target as a self-collision).
func TestInstaller_RejectsWeaverTargetIDCollision(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	a := weaverTargetDef("wt-pkg-a", "wtClassA", "wtLensA", "foo", "0.1.0")
	if _, err := inst.Install(ctx, a); err != nil {
		t.Fatalf("install A: %v", err)
	}

	// Re-installing A stays idempotent — no false self-collision on "foo".
	res, err := inst.Install(ctx, a)
	if err != nil {
		t.Fatalf("re-install A: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected Skipped=true re-installing A, got %+v", res)
	}

	// B collides on targetId "foo" alone.
	b := weaverTargetDef("wt-pkg-b", "wtClassB", "wtLensB", "foo", "0.1.0")
	_, err = inst.Install(ctx, b)
	if err == nil {
		t.Fatal("expected ErrWeaverTargetIDCollision installing a package reusing an installed targetId, got nil")
	}
	if !errors.Is(err, ErrWeaverTargetIDCollision) {
		t.Fatalf("expected ErrWeaverTargetIDCollision, got %v", err)
	}
	if !strings.Contains(err.Error(), "foo") {
		t.Errorf("collision error should name the colliding targetId; got %v", err)
	}

	// A non-colliding B (distinct targetId) installs fine.
	bClean := weaverTargetDef("wt-pkg-b", "wtClassB", "wtLensB", "bar", "0.1.0")
	if _, err := inst.Install(ctx, bClean); err != nil {
		t.Fatalf("non-colliding targetId should install, got: %v", err)
	}
}

// TestInstaller_HappyPath installs a synthetic package and asserts the
// DDL meta-vertex, Lens meta-vertex, permission vertex, grant link, and
// package vertex are all written.
func TestInstaller_HappyPath(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")

	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Skipped {
		t.Fatalf("expected install (not skipped), got skipped=%v reason=%q", res.Skipped, res.Reason)
	}
	if res.PackageKey == "" {
		t.Fatalf("PackageKey empty")
	}
	if len(res.DeclaredKeys) == 0 {
		t.Fatalf("DeclaredKeys empty")
	}

	// Spot-check: every declared key exists in core-kv.
	for _, k := range res.DeclaredKeys {
		if _, err := conn.KVGet(ctx, CoreBucket, k); err != nil {
			t.Fatalf("declared key %s missing: %v", k, err)
		}
	}
	// Package vertex + manifest aspect present.
	if _, err := conn.KVGet(ctx, CoreBucket, res.PackageKey); err != nil {
		t.Fatalf("package vertex missing: %v", err)
	}
	if _, err := conn.KVGet(ctx, CoreBucket, res.PackageKey+".manifest"); err != nil {
		t.Fatalf("package manifest aspect missing: %v", err)
	}
}

// TestInstaller_Idempotent installs twice with the same version; the
// second call must short-circuit to Skipped=true.
func TestInstaller_Idempotent(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected Skipped=true on re-install, got %+v", res)
	}
}

// TestInstaller_RefusesDifferentVersion installs v0.1.0, then attempts
// v0.2.0 and expects ErrVersionMismatch.
func TestInstaller_RefusesDifferentVersion(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	_, err := inst.Install(ctx, sampleDef("0.2.0"))
	if err == nil {
		t.Fatalf("expected ErrVersionMismatch, got nil")
	}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("expected ErrVersionMismatch, got %v", err)
	}
}

// TestInstaller_RejectsReservedBucketAlias asserts Install fails closed end-
// to-end when a lens declares the short auth-plane alias "capability", and
// that the canonical "capability-kv" form installs successfully.
func TestInstaller_RejectsReservedBucketAlias(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	bad := sampleDef("0.1.0")
	bad.Lenses[0].Bucket = "capability"
	_, err := inst.Install(ctx, bad)
	if err == nil {
		t.Fatal("expected Install to reject lens Bucket \"capability\", got nil error")
	}
	if !strings.Contains(err.Error(), "capability-kv") {
		t.Fatalf("rejection should direct author to canonical bucket; got %v", err)
	}

	good := sampleDef("0.1.0")
	good.Lenses[0].Bucket = "capability-kv"
	if _, err := inst.Install(ctx, good); err != nil {
		t.Fatalf("canonical Bucket \"capability-kv\" should install, got: %v", err)
	}
}

// TestInstaller_ListShowsInstalled exercises List before + after install
// and after uninstall.
func TestInstaller_ListShowsInstalled(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	before, err := inst.List(ctx)
	if err != nil {
		t.Fatalf("List pre-install: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("expected empty list pre-install, got %d", len(before))
	}

	def := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	after, err := inst.List(ctx)
	if err != nil {
		t.Fatalf("List post-install: %v", err)
	}
	if len(after) != 1 || after[0].PackageName() != def.Name {
		t.Fatalf("expected one entry %q, got %+v", def.Name, after)
	}
}

// TestInstaller_Uninstall installs then uninstalls; every declared key
// (and the package vertex itself) must be soft-deleted.
func TestInstaller_Uninstall(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")
	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(uninst.Tombstoned) == 0 {
		t.Fatalf("Tombstoned empty: %+v", uninst)
	}

	// Every declared key plus the package vertex should now read back
	// with isDeleted=true in the envelope's JSON.
	allKeys := append([]string{}, res.DeclaredKeys...)
	allKeys = append(allKeys, res.PackageKey)
	for _, k := range allKeys {
		entry, err := conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			// A soft-delete still resolves to a value (isDeleted=true). A
			// hard-not-found here means the install never wrote it OR the
			// substrate evicted the key — either way an issue.
			t.Fatalf("post-uninstall read %s: %v", k, err)
		}
		// Cheap shape check on the JSON to confirm tombstone marker.
		val := string(entry.Value)
		if !contains(val, `"isDeleted":true`) {
			t.Fatalf("key %s not tombstoned: %s", k, val)
		}
	}

	// List should be empty after uninstall.
	after, err := inst.List(ctx)
	if err != nil {
		t.Fatalf("List post-uninstall: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected empty list post-uninstall, got %+v", after)
	}
}

// TestInstaller_Uninstall_PreservesRetentionClassHolder is the uninstall half
// of the removal path the upgrade diff already guards: uninstall enumerates the
// manifest's declaredKeys, which includes a retention-class holder's root and
// its .retentionPolicy aspect, and tombstoning either would put the class's DEK
// beyond ShredRetentionClassKey forever (its vertex_alive guard refuses a
// tombstoned holder). So the holder survives the uninstall, live but no longer
// declared, while every other declared key tombstones normally.
func TestInstaller_Uninstall_PreservesRetentionClassHolder(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := defWithRetentionClass("0.1.0", "sampleClass1")
	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	holderKey := RetentionClassKey(def.Name, "sampleClass1")
	policyKey := holderKey + ".retentionPolicy"
	preserved := map[string]bool{holderKey: true, policyKey: true}
	// The whole test is vacuous unless the install actually declared the holder
	// keys — declaredKeys is the exact list uninstall walks, so if the holder
	// were absent here there would be nothing to preserve and the assertions
	// below would pass without exercising the skip at all.
	declared := map[string]bool{}
	for _, k := range res.DeclaredKeys {
		declared[k] = true
	}
	for k := range preserved {
		if !declared[k] {
			t.Fatalf("install did not declare %s; declaredKeys = %v", k, res.DeclaredKeys)
		}
	}

	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	got := append([]string{}, uninst.RetentionHoldersPreserved...)
	slices.Sort(got)
	want := []string{holderKey, policyKey}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("RetentionHoldersPreserved = %v, want exactly %v (%+v)", got, want, uninst)
	}

	// Both holder keys must still read live: a tombstone on either is the bug.
	for k := range preserved {
		entry, err := conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			t.Fatalf("post-uninstall read %s: %v", k, err)
		}
		if contains(string(entry.Value), `"isDeleted":true`) {
			t.Fatalf("%s was tombstoned by uninstall — ShredRetentionClassKey can never destroy this class key now: %s", k, entry.Value)
		}
	}

	// Everything else the package declared — the sample DDL's meta vertex, its
	// permission, the lens, and the package vertex itself — tombstones exactly
	// as any other declared key.
	for _, k := range append(append([]string{}, res.DeclaredKeys...), res.PackageKey) {
		if preserved[k] {
			continue
		}
		entry, err := conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			t.Fatalf("post-uninstall read %s: %v", k, err)
		}
		if !contains(string(entry.Value), `"isDeleted":true`) {
			t.Fatalf("key %s not tombstoned: %s", k, entry.Value)
		}
	}
	for _, k := range uninst.Tombstoned {
		if preserved[k] {
			t.Fatalf("Tombstoned names the preserved holder key %s: %v", k, uninst.Tombstoned)
		}
	}
}

// TestInstaller_Uninstall_ReportsAlreadyStrandedHolderSeparately is the
// uninstall half of the same classification: the prefix match that excludes a
// holder says nothing about the holder's state, so an already-tombstoned one
// would otherwise be reported as "preserved — still destroyable by
// ShredRetentionClassKey" when that verb's vertex_alive guard has already
// refused it for good. Stranded custody must read as stranded.
func TestInstaller_Uninstall_ReportsAlreadyStrandedHolderSeparately(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := defWithRetentionClass("0.1.0", "sampleClass1")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}
	holderKey := RetentionClassKey(def.Name, "sampleClass1")
	policyKey := holderKey + ".retentionPolicy"

	entry, err := conn.KVGet(ctx, CoreBucket, holderKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", holderKey, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", holderKey, err)
	}
	doc["isDeleted"] = true
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal stranded holder: %v", err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, holderKey, raw); err != nil {
		t.Fatalf("KVPut stranded holder: %v", err)
	}

	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !slices.Equal(uninst.RetentionHoldersAlreadyStranded, []string{holderKey}) {
		t.Fatalf("RetentionHoldersAlreadyStranded = %v, want exactly [%s] (%+v)", uninst.RetentionHoldersAlreadyStranded, holderKey, uninst)
	}
	if !slices.Equal(uninst.RetentionHoldersPreserved, []string{policyKey}) {
		t.Fatalf("RetentionHoldersPreserved = %v, want exactly [%s] — the aspect is still live (%+v)", uninst.RetentionHoldersPreserved, policyKey, uninst)
	}
	for _, k := range uninst.Tombstoned {
		if k == holderKey || k == policyKey {
			t.Fatalf("a stranded holder is still excluded from the tombstone set, got %v", uninst.Tombstoned)
		}
	}
}

// TestInstaller_Uninstall_ReportsSecureColumnsErased is Uninstall's half of
// §30.6's adjacent find: a lens's committed secureColumns describe key-custody
// history the tombstone erases from the destruction-readiness oracle's view
// (registry_probe.declaredLensIDs stops seeing the lens) without touching the
// ciphertext still sitting in the target store. Uninstall takes a package NAME,
// not a Definition, so the attestation is the OPERATOR's, supplied through
// UninstallOptions; with it in hand the tombstone commits and the erasure is
// reported key by key.
func TestInstaller_Uninstall_ReportsSecureColumnsErased(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{RetiredSecureLenses: []RetiredSecureLens{{
		Lens: "sampleSecureLens",
		Note: "test fixture — no ciphertext was ever written",
	}}})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(uninst.SecureColumnsErased) != 1 {
		t.Fatalf("SecureColumnsErased = %+v, want exactly 1 entry", uninst.SecureColumnsErased)
	}
	got := uninst.SecureColumnsErased[0]
	wantKey := secureLensSpecKey(def)
	if got.Key != wantKey {
		t.Fatalf("SecureColumnsErased[0].Key = %q, want %q", got.Key, wantKey)
	}
	if got.Lens != "sampleSecureLens" {
		t.Fatalf("SecureColumnsErased[0].Lens = %q, want %q", got.Lens, "sampleSecureLens")
	}
	if !slices.Equal(got.Columns, []string{"applicant_name"}) {
		t.Fatalf("SecureColumnsErased[0].Columns = %v, want [applicant_name]", got.Columns)
	}
	if !slices.Equal(got.Holders, []string{"identity"}) {
		t.Fatalf("SecureColumnsErased[0].Holders = %v, want [identity]", got.Holders)
	}
	if !slices.Contains(uninst.Tombstoned, wantKey) {
		t.Fatalf("secure lens spec key not tombstoned: %v", uninst.Tombstoned)
	}
}

// TestInstaller_Uninstall_ReportsNoSecureColumnsErasedWithoutSecureLens is the
// negative vector: a package declaring no Secure Lens reports an empty
// SecureColumnsErased.
func TestInstaller_Uninstall_ReportsNoSecureColumnsErasedWithoutSecureLens(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(uninst.SecureColumnsErased) != 0 {
		t.Fatalf("SecureColumnsErased = %+v, want none", uninst.SecureColumnsErased)
	}
}

// TestInstaller_Uninstall_ReportsAlreadyErasedSecureColumnsSeparately is B4's
// regression, keyed on the predicate the destruction-readiness oracle actually
// uses: declaredLensIDs enumerates vertex ROOTS, so a lens whose root was
// tombstoned out-of-band before this uninstall ran had already dropped out of
// its view — re-tombstoning it here erases nothing NEW. It must land in
// SecureColumnsAlreadyErased (pre-existing damage to escalate) and need no
// operator attestation, never in SecureColumnsErased (blaming this run for
// something it did not do), mirroring the retention-holder
// preserved/already-stranded split for the identical reason.
func TestInstaller_Uninstall_ReportsAlreadyErasedSecureColumnsSeparately(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	specKey := secureLensSpecKey(def)
	tombstoneKey(t, ctx, conn, strings.TrimSuffix(specKey, ".spec"))

	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(uninst.SecureColumnsErased) != 0 {
		t.Fatalf("SecureColumnsErased = %+v, want none — the lens root was already tombstoned before this run", uninst.SecureColumnsErased)
	}
	if len(uninst.SecureColumnsAlreadyErased) != 1 || uninst.SecureColumnsAlreadyErased[0].Key != specKey {
		t.Fatalf("SecureColumnsAlreadyErased = %+v, want exactly one entry for %s", uninst.SecureColumnsAlreadyErased, specKey)
	}
	if uninst.SecureLensRetirementsAttested != 0 {
		t.Fatalf("SecureLensRetirementsAttested = %d, want 0 — nothing new was erased to attest", uninst.SecureLensRetirementsAttested)
	}
}

// tombstoneKey flips a committed Core-KV document's isDeleted in place, the
// out-of-band soft delete a test uses to stand up a key state some earlier run
// left behind. It writes through KVPut rather than an op deliberately: the
// point is a state the installer did not create and must classify correctly.
func tombstoneKey(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, CoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	doc["isDeleted"] = true
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal tombstoned %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, key, raw); err != nil {
		t.Fatalf("KVPut tombstoned %s: %v", key, err)
	}
}

// TestInstaller_Uninstall_EventStreamSpecNeedsNoAttestation covers the third
// leg of the oracle's visibility predicate: declaredLensIDs skips a spec whose
// source.kind is "eventStream" (Chronicler owns those) no matter how live its
// vertex root is, so tombstoning one erases nothing from the oracle's view and
// needs no attestation.
//
// The state is written out-of-band because that is the only way it can exist:
// validateSecureLensBuckets refuses SecureColumns on an eventStream Source at
// install and upgrade alike, so a spec carrying both was hand-written into Core
// KV — and a guard that mirrors a reader's predicate has to agree with it on
// exactly the states the reader can encounter, not only the ones a package can
// produce.
func TestInstaller_Uninstall_EventStreamSpecNeedsNoAttestation(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	specKey := secureLensSpecKey(def)
	entry, err := conn.KVGet(ctx, CoreBucket, specKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", specKey, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", specKey, err)
	}
	body, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("spec %s carries no data envelope: %s", specKey, entry.Value)
	}
	body["source"] = map[string]any{"kind": "eventStream"}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal eventStream spec: %v", err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, specKey, raw); err != nil {
		t.Fatalf("KVPut eventStream spec: %v", err)
	}

	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(uninst.SecureColumnsErased) != 0 {
		t.Fatalf("SecureColumnsErased = %+v, want none — the oracle skips an eventStream spec", uninst.SecureColumnsErased)
	}
	if len(uninst.SecureColumnsAlreadyErased) != 1 || uninst.SecureColumnsAlreadyErased[0].Key != specKey {
		t.Fatalf("SecureColumnsAlreadyErased = %+v, want exactly one entry for %s", uninst.SecureColumnsAlreadyErased, specKey)
	}
}

// TestInstaller_Uninstall_PoisonedEventStreamSpecStillNeedsAttestation closes
// the gate bypass the eventStream leg opened. The oracle skips an eventStream
// spec ONLY when the whole spec probe decodes cleanly (registry_probe.go's
// `specRead := present && json.Unmarshal(...) == nil` guarding the skip), and
// its probe types holderTypes as []string — so a holderTypes list carrying a
// non-string fails that decode, the skip does not apply, and the lens stays
// DECLARED and counted for every holder type.
//
// A map-based "does source.kind say eventStream" test disagrees in the unsafe
// direction: it calls the lens invisible, files a live custody record under
// already-erased, and commits the uninstall with a zero UninstallOptions. Any
// document that fails the typed decode is declared, so it must read as visible
// here — attest it, or be refused.
func TestInstaller_Uninstall_PoisonedEventStreamSpecStillNeedsAttestation(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	specKey := secureLensSpecKey(def)
	entry, err := conn.KVGet(ctx, CoreBucket, specKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", specKey, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", specKey, err)
	}
	body, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("spec %s carries no data envelope: %s", specKey, entry.Value)
	}
	body["source"] = map[string]any{"kind": "eventStream"}
	// The poison: a holderTypes list the oracle's []string probe cannot decode.
	cfg, ok := body["targetConfig"].(map[string]any)
	if !ok {
		t.Fatalf("spec %s carries no targetConfig: %s", specKey, entry.Value)
	}
	cols, ok := cfg["secureColumns"].([]any)
	if !ok || len(cols) == 0 {
		t.Fatalf("spec %s carries no secureColumns: %s", specKey, entry.Value)
	}
	cols[0].(map[string]any)["holderTypes"] = []any{"identity", 5}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal poisoned spec: %v", err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, specKey, raw); err != nil {
		t.Fatalf("KVPut poisoned spec: %v", err)
	}
	// The premise, asserted rather than assumed: this document really does
	// defeat the oracle's typed decode, which is the only reason its eventStream
	// skip does not apply.
	var probe uninstallSpecProbe
	if json.Unmarshal(raw, &probe) == nil {
		t.Fatal("the poisoned spec decodes cleanly — the fixture no longer reproduces the bypass")
	}

	_, err = inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err == nil {
		t.Fatal("expected Uninstall to refuse: the oracle still declares a lens whose spec fails its typed decode")
	}
	if !errors.Is(err, ErrUndeclaredSecureLensErasure) {
		t.Fatalf("Uninstall error = %v, want one wrapping ErrUndeclaredSecureLensErasure", err)
	}
}

// TestInstaller_Uninstall_SecureColumnsUnderDecoyTargetConfigAreFound closes the
// detection-side twin of the same class. The oracle's mayHoldHolderType reads
// BOTH targetConfig levels "never one in preference to the other"; a detection
// that reads only the level it finds first sees a decoy top-level targetConfig,
// concludes the lens has no secure columns, constructs no erasure at all — and
// the gate is never consulted, so the custody record leaves in silence.
func TestInstaller_Uninstall_SecureColumnsUnderDecoyTargetConfigAreFound(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	specKey := secureLensSpecKey(def)
	entry, err := conn.KVGet(ctx, CoreBucket, specKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", specKey, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", specKey, err)
	}
	// The real secureColumns stay untouched under data.targetConfig; a decoy
	// top-level targetConfig declares none.
	doc["targetConfig"] = map[string]any{"bucket": "decoy"}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal decoy spec: %v", err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, specKey, raw); err != nil {
		t.Fatalf("KVPut decoy spec: %v", err)
	}

	_, err = inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err == nil {
		t.Fatal("expected Uninstall to refuse: data.targetConfig still declares a live secure column")
	}
	if !errors.Is(err, ErrUndeclaredSecureLensErasure) {
		t.Fatalf("Uninstall error = %v, want one wrapping ErrUndeclaredSecureLensErasure", err)
	}
	if !strings.Contains(err.Error(), "applicant_name") {
		t.Errorf("refusal does not name the secure column hidden under data.targetConfig: %v", err)
	}
}

// TestInstaller_Uninstall_ReportsLensTombstonedWithUnusableSpec covers the third
// population. The oracle is fail-closed on a spec it cannot read: the lens
// counts as declared AND its holder-type filter never runs, so it counts for
// EVERY holder type. Tombstoning the root removes that signal — but there are
// no declared holder types left to attest ABOUT, and refusing would make a
// package whose spec was purged out of band permanently un-uninstallable. So
// the uninstall proceeds and REPORTS, rather than gating.
func TestInstaller_Uninstall_ReportsLensTombstonedWithUnusableSpec(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	specKey := secureLensSpecKey(def)
	rootKey := strings.TrimSuffix(specKey, ".spec")
	if err := conn.KVPurge(ctx, CoreBucket, specKey); err != nil {
		t.Fatalf("KVPurge %s: %v", specKey, err)
	}

	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !slices.Equal(uninst.LensesTombstonedWithUnusableSpec, []string{rootKey}) {
		t.Fatalf("LensesTombstonedWithUnusableSpec = %v, want exactly [%s] (%+v)", uninst.LensesTombstonedWithUnusableSpec, rootKey, uninst)
	}
	if len(uninst.SecureColumnsErased) != 0 || len(uninst.SecureColumnsAlreadyErased) != 0 {
		t.Fatalf("a lens with no readable spec declares no columns to bucket: erased=%+v alreadyErased=%+v",
			uninst.SecureColumnsErased, uninst.SecureColumnsAlreadyErased)
	}
	if !slices.Contains(uninst.Tombstoned, rootKey) {
		t.Fatalf("the lens root was not tombstoned: %v", uninst.Tombstoned)
	}
}

// TestInstaller_Uninstall_ReportsLensTombstonedWithUnusableSpecShapes covers
// the two present-but-unusable shapes beside the absent one. The oracle's
// holder-type filter runs only on a spec that decoded into its probe, and that
// filter answers "may hold" for EVERY holder type when there is no targetConfig
// at either level — so on both of these shapes it counts the lens for every
// holder type, exactly as it does for a spec it could not fetch.
//
// Neither shape declares a secure column this scan can read, so neither builds
// an erasure and neither reaches the gate. Without this population they would
// be tombstoned in complete silence.
func TestInstaller_Uninstall_ReportsLensTombstonedWithUnusableSpecShapes(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(doc, body map[string]any)
	}{
		{
			name: "no targetConfig at either level",
			corrupt: func(doc, body map[string]any) {
				delete(body, "targetConfig")
			},
		},
		{
			name: "secureColumns is not a list",
			corrupt: func(doc, body map[string]any) {
				body["targetConfig"].(map[string]any)["secureColumns"] = "not-a-list"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn, inst := newInstallerHarness(t)
			def := defWithSecureLens("0.1.0", []string{"identity"}, "")
			if _, err := inst.Install(ctx, def); err != nil {
				t.Fatalf("Install: %v", err)
			}
			specKey := secureLensSpecKey(def)
			rootKey := strings.TrimSuffix(specKey, ".spec")
			entry, err := conn.KVGet(ctx, CoreBucket, specKey)
			if err != nil {
				t.Fatalf("KVGet %s: %v", specKey, err)
			}
			var doc map[string]any
			if err := json.Unmarshal(entry.Value, &doc); err != nil {
				t.Fatalf("unmarshal %s: %v", specKey, err)
			}
			tc.corrupt(doc, doc["data"].(map[string]any))
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("marshal corrupted spec: %v", err)
			}
			if _, err := conn.KVPut(ctx, CoreBucket, specKey, raw); err != nil {
				t.Fatalf("KVPut corrupted spec: %v", err)
			}

			uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
			if err != nil {
				t.Fatalf("Uninstall: %v", err)
			}
			if !slices.Equal(uninst.LensesTombstonedWithUnusableSpec, []string{rootKey}) {
				t.Fatalf("LensesTombstonedWithUnusableSpec = %v, want exactly [%s] (%+v)",
					uninst.LensesTombstonedWithUnusableSpec, rootKey, uninst)
			}
		})
	}
}

// TestInstaller_Uninstall_PlainLensIsNotReportedAsUnusable is the negative
// vector that keeps the population meaningful: a lens with a real targetConfig
// and no secure columns is a genuine "holds no ciphertext" — the oracle's
// filter excludes it from every holder type, so tombstoning it loses nothing
// and it must not appear beside the corrupted shapes.
func TestInstaller_Uninstall_PlainLensIsNotReportedAsUnusable(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}
	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(uninst.LensesTombstonedWithUnusableSpec) != 0 {
		t.Fatalf("LensesTombstonedWithUnusableSpec = %v, want none — a plain lens declares no custody to lose",
			uninst.LensesTombstonedWithUnusableSpec)
	}
}

// TestInstaller_Uninstall_RefusesUnattestedSecureLensErasure is §31's green
// bar: an uninstall that would newly erase a live Secure Lens's key-custody
// record from the destruction-readiness oracle's view is refused outright, and
// refused BEFORE the UninstallPackage op is submitted — once that batch
// commits, the oracle stops seeing the lens and nothing records which holder
// types its ciphertext was written under, so there is no later moment at which
// the operator could be asked. The refusal names the lens, its spec key, the
// columns and the holder types, because an operator who cannot see what they
// are attesting to cannot attest to it.
func TestInstaller_Uninstall_RefusesUnattestedSecureLensErasure(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	_, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err == nil {
		t.Fatal("expected Uninstall to refuse an unattested secure-lens erasure, got nil error")
	}
	if !errors.Is(err, ErrUndeclaredSecureLensErasure) {
		t.Fatalf("Uninstall error = %v, want one wrapping ErrUndeclaredSecureLensErasure", err)
	}
	specKey := secureLensSpecKey(def)
	for _, want := range []string{"sampleSecureLens", specKey, "applicant_name", "identity", "--retire-secure-lens"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}

	// Nothing was submitted: every declared key is still live, so the package
	// is exactly as installed and the operator can re-run with an attestation.
	for _, k := range []string{specKey, strings.TrimSuffix(specKey, ".spec")} {
		entry, getErr := conn.KVGet(ctx, CoreBucket, k)
		if getErr != nil {
			t.Fatalf("post-refusal read %s: %v", k, getErr)
		}
		if contains(string(entry.Value), `"isDeleted":true`) {
			t.Fatalf("key %s was tombstoned by a refused uninstall: %s", k, entry.Value)
		}
	}
}

// TestInstaller_Uninstall_RefusalNamesEverySecureLensAtOnce pins that a refusal
// states the WHOLE bill. An uninstall is all-or-nothing over a whole package,
// so a guard that stopped at the first unattested lens would hand a two-lens
// package back one lens at a time — attest, re-run, get refused over the lens
// the first refusal never mentioned. The typed error carries all of them in
// Unattested, and the message names all of them, so the operator's next run is
// their last.
//
// Read from the typed error's FIELDS, never scraped from the prose: that is
// what makes Loupe's per-lens re-prompt possible without a regex over an error
// message.
func TestInstaller_Uninstall_RefusalNamesEverySecureLensAtOnce(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := defWithTwoSecureLenses("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// One of the two attested: the refusal must still name the other, and must
	// not be satisfied by the partial attestation.
	_, err := inst.Uninstall(ctx, def.Name, UninstallOptions{RetiredSecureLenses: []RetiredSecureLens{{
		Lens: "sampleSecureLens",
		Note: "test fixture — no ciphertext was ever written",
	}}})
	if err == nil {
		t.Fatal("expected Uninstall to refuse: the second secure lens is unattested")
	}
	var partial *UndeclaredSecureLensErasureError
	if !errors.As(err, &partial) {
		t.Fatalf("Uninstall error = %v, want a *UndeclaredSecureLensErasureError", err)
	}
	if len(partial.Unattested) != 1 || partial.Unattested[0].Lens != "otherSecureLens" {
		t.Fatalf("Unattested = %+v, want exactly the unattested lens otherSecureLens", partial.Unattested)
	}

	// Neither attested: ONE refusal, naming BOTH, in fields and in prose.
	_, err = inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err == nil {
		t.Fatal("expected Uninstall to refuse both unattested secure lenses")
	}
	var refusal *UndeclaredSecureLensErasureError
	if !errors.As(err, &refusal) {
		t.Fatalf("Uninstall error = %v, want a *UndeclaredSecureLensErasureError", err)
	}
	if !errors.Is(err, ErrUndeclaredSecureLensErasure) {
		t.Fatalf("the typed refusal must still unwrap to the sentinel: %v", err)
	}
	if refusal.PackageName != def.Name {
		t.Errorf("PackageName = %q, want %q", refusal.PackageName, def.Name)
	}
	if len(refusal.Unattested) != 2 {
		t.Fatalf("Unattested = %+v, want both lenses in ONE refusal", refusal.Unattested)
	}
	named := []string{refusal.Unattested[0].Lens, refusal.Unattested[1].Lens}
	slices.Sort(named)
	if !slices.Equal(named, []string{"otherSecureLens", "sampleSecureLens"}) {
		t.Fatalf("Unattested lenses = %v, want both secure lenses", named)
	}
	for _, want := range []string{"sampleSecureLens", "otherSecureLens", "applicant_name", "tenant_email"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
	// The remedy is one runnable command line carrying BOTH flags — a remedy
	// that only fixes half of what it refused sends the operator back for a
	// second refusal.
	if n := strings.Count(err.Error(), "--retire-secure-lens"); n != 2 {
		t.Errorf("refusal carries %d --retire-secure-lens flags, want one per unattested lens (2): %v", n, err)
	}
	// And it actually RUNS: Go's flag package stops parsing at the first
	// non-flag argument, so a command line with the package name ahead of the
	// flags exits 2 without doing anything. Counting flags alone would not
	// catch that; their position relative to the name is the whole test.
	remedy := err.Error()[strings.Index(err.Error(), "lattice-pkg uninstall"):]
	firstFlag := strings.Index(remedy, "--retire-secure-lens")
	namePos := strings.Index(remedy, " "+def.Name+"`")
	if firstFlag < 0 || namePos < 0 || firstFlag > namePos {
		t.Errorf("the printed remedy does not parse: flags must precede the package name, got %q", remedy)
	}

	// And both attested at once: one re-run, done, counting both lenses' columns.
	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{RetiredSecureLenses: []RetiredSecureLens{
		{Lens: "sampleSecureLens", Note: "test fixture — no ciphertext was ever written"},
		{Lens: "otherSecureLens", Note: "test fixture — no ciphertext was ever written"},
	}})
	if err != nil {
		t.Fatalf("Uninstall with both attestations: %v", err)
	}
	if uninst.SecureLensRetirementsAttested != 2 {
		t.Fatalf("SecureLensRetirementsAttested = %d, want 2 (one secure column per lens)", uninst.SecureLensRetirementsAttested)
	}
}

// TestEnforceUninstallSecureLensRetirement_CountsEveryDeclaredColumn pins the
// unit of SecureLensRetirementsAttested. A committed spec entry whose `column`
// field is unnameable still recorded holder types, so it is still one custody
// record leaving the oracle's view; counting only the nameable subset would
// report three retired records as two and quietly under-state exactly the
// number an operator is meant to notice.
func TestEnforceUninstallSecureLensRetirement_CountsEveryDeclaredColumn(t *testing.T) {
	erasure := UninstallSecureColumnErasure{
		Key:      metaVertexPrefix + LensID("sample-pkg", "aLens") + ".spec",
		Lens:     "aLens",
		Columns:  []string{"named_one", "named_two"},
		Declared: 3, // the third entry carried no nameable column
		Holders:  []string{"identity"},
	}
	attested, unused, err := enforceUninstallSecureLensRetirement("sample-pkg",
		UninstallOptions{RetiredSecureLenses: []RetiredSecureLens{{Lens: "aLens", Note: "why"}}},
		[]UninstallSecureColumnErasure{erasure})
	if err != nil {
		t.Fatalf("enforce: %v", err)
	}
	if attested != 3 {
		t.Fatalf("attested = %d, want 3 — every DECLARED entry is a custody record, not just the nameable ones", attested)
	}
	if len(unused) != 0 {
		t.Fatalf("unused = %v, want none", unused)
	}
}

// TestEnforceUninstallSecureLensRetirement_OneAttestationExcusesOneLens pins
// the "one erasure, one attestation" invariant against the double-spelling
// match. Both readings of an operator's string are registered as candidate keys
// so a stray space still resolves — but "foo" and " foo" are two different
// lenses holding two different custody records, and canonicalName uniqueness is
// an exact-string check, so a package can declare both. One Note must never
// excuse both.
func TestEnforceUninstallSecureLensRetirement_OneAttestationExcusesOneLens(t *testing.T) {
	trimmed := UninstallSecureColumnErasure{
		Key: metaVertexPrefix + LensID("sample-pkg", "foo") + ".spec", Lens: "foo", Declared: 1,
	}
	spaced := UninstallSecureColumnErasure{
		Key: metaVertexPrefix + LensID("sample-pkg", " foo") + ".spec", Lens: " foo", Declared: 1,
	}
	_, _, err := enforceUninstallSecureLensRetirement("sample-pkg",
		UninstallOptions{RetiredSecureLenses: []RetiredSecureLens{{Lens: " foo", Note: "one note"}}},
		[]UninstallSecureColumnErasure{trimmed, spaced})
	if err == nil {
		t.Fatal("one attestation excused two lenses' custody records — the second erasure must be refused")
	}
	var refusal *UndeclaredSecureLensErasureError
	if !errors.As(err, &refusal) {
		t.Fatalf("enforce error = %v, want a *UndeclaredSecureLensErasureError", err)
	}
	if len(refusal.Unattested) != 1 {
		t.Fatalf("Unattested = %+v, want exactly the one lens the attestation did not cover", refusal.Unattested)
	}
}

// TestEnforceUninstallSecureLensRetirement_RepeatsValidation keeps validate()'s
// own doc claim true: the enforcer is the fail-closed floor for any path that
// reaches it with unvalidated options, so it repeats every check — including
// the duplicate-Lens one. Without it, a direct caller passing {"aLens"} and
// {" aLens"} gets a clean run that reports one of them as UNUSED: a lens named
// as unmatched when it did in fact match.
func TestEnforceUninstallSecureLensRetirement_RepeatsValidation(t *testing.T) {
	cases := []struct {
		name  string
		atts  []RetiredSecureLens
		wants string
	}{
		{"empty lens", []RetiredSecureLens{{Lens: " ", Note: "why"}}, "names no Lens"},
		{"empty note", []RetiredSecureLens{{Lens: "aLens", Note: " "}}, "empty Note"},
		{"duplicate lens", []RetiredSecureLens{
			{Lens: "aLens", Note: "first"}, {Lens: "aLens", Note: "second"},
		}, "twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := enforceUninstallSecureLensRetirement("sample-pkg",
				UninstallOptions{RetiredSecureLenses: tc.atts}, nil)
			if err == nil {
				t.Fatal("expected the enforcer to repeat the validation refusal, got nil")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("enforce error = %v, want one naming %q", err, tc.wants)
			}
			if !errors.Is(err, ErrInvalidUninstallOptions) {
				t.Fatalf("enforce error = %v, want one wrapping ErrInvalidUninstallOptions (Loupe maps it to 400)", err)
			}
		})
	}
}

// TestEnforceUninstallSecureLensRetirement_WhitespaceTwinsAreTwoLenses pins
// that "duplicate" is judged on the RAW Lens. LensID salts the raw string, so
// "l1" and " l1 " address two different keyspaces holding two different custody
// records — a package can declare both. Treating them as a duplicate pair would
// make the refusal over the second one print a remedy that is itself refused as
// a duplicate when pasted beside the first: a different error, and no way out.
func TestEnforceUninstallSecureLensRetirement_WhitespaceTwinsAreTwoLenses(t *testing.T) {
	trimmed := UninstallSecureColumnErasure{
		Key: metaVertexPrefix + LensID("sample-pkg", "l1") + ".spec", Lens: "l1", Declared: 1,
	}
	spaced := UninstallSecureColumnErasure{
		Key: metaVertexPrefix + LensID("sample-pkg", " l1 ") + ".spec", Lens: " l1 ", Declared: 1,
	}
	attested, unused, err := enforceUninstallSecureLensRetirement("sample-pkg",
		UninstallOptions{RetiredSecureLenses: []RetiredSecureLens{
			{Lens: "l1", Note: "the trimmed lens"},
			{Lens: " l1 ", Note: "the spaced lens — a different keyspace"},
		}},
		[]UninstallSecureColumnErasure{trimmed, spaced})
	if err != nil {
		t.Fatalf("two attestations for two genuinely distinct lenses must be accepted: %v", err)
	}
	if attested != 2 {
		t.Fatalf("attested = %d, want 2 — one custody record per lens", attested)
	}
	if len(unused) != 0 {
		t.Fatalf("unused = %v, want none — each attestation matched its own lens", unused)
	}
	// The same pair through the pre-read validator, which is the path the
	// pasted remedy actually takes.
	if err := (UninstallOptions{RetiredSecureLenses: []RetiredSecureLens{
		{Lens: "l1", Note: "the trimmed lens"},
		{Lens: " l1 ", Note: "the spaced lens"},
	}}).validate(); err != nil {
		t.Fatalf("validate rejected two distinct lenses as duplicates: %v", err)
	}
}

// TestUndeclaredSecureLensErasureError_NumbersUnreadableLensNames pins that a
// refusal over two lenses whose canonicalNames could not be read prints two
// DISTINCT placeholders. Printing the same literal twice makes the remedy
// self-collide on validate()'s duplicate-Lens check, so an operator who pastes
// it is handed a different error than the one they set out to fix.
func TestUndeclaredSecureLensErasureError_NumbersUnreadableLensNames(t *testing.T) {
	err := undeclaredSecureLensErasureError("demo-domain", []UninstallSecureColumnErasure{
		{Key: "vtx.meta.aaaaaaaaaaaaaaaaaaaa.spec", Declared: 2},
		{Key: "vtx.meta.bbbbbbbbbbbbbbbbbbbb.spec", Declared: 1},
	})
	msg := err.Error()
	// An erasure with no nameable columns must still say how many custody
	// records it covers — a bill line reading "named []" and nothing else names
	// no subject the operator could attest about.
	if !strings.Contains(msg, "2 declared secure column(s)") {
		t.Errorf("refusal does not carry the declared count for an erasure with no nameable columns: %v", msg)
	}
	for _, want := range []string{"<lens canonicalName #1>", "<lens canonicalName #2>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not carry a distinct placeholder %q: %v", want, msg)
		}
	}
	if strings.Contains(msg, "<lens canonicalName>=") {
		t.Errorf("refusal still prints an unnumbered placeholder, which collides when pasted: %v", msg)
	}
}

// defWithTwoSecureLenses returns the sample package carrying a SECOND Secure
// Lens beside defWithSecureLens's, so a test can observe how a refusal handles
// a package whose erasures are plural — the shape one uninstall actually takes.
func defWithTwoSecureLenses(version string) Definition {
	def := defWithSecureLens(version, []string{"identity"}, "")
	def.Lenses = append(def.Lenses, LensSpec{
		CanonicalName: "otherSecureLens",
		Class:         "meta.lens",
		Adapter:       "postgres",
		Table:         "read_other_secure",
		Engine:        "full",
		Protected:     true,
		Spec:          `MATCH (n:sample) RETURN n.key AS key, n.tenant_email AS tenant_email`,
		Columns: []PostgresColumn{
			{Name: "key", Type: "text"},
			{Name: "tenant_email", Type: "text"},
		},
		SecureColumns: []SecureColumn{{
			Column:      "tenant_email",
			HolderTypes: []string{"retentionclass"},
			Field:       "value",
		}},
	})
	return def
}

// TestInstaller_Uninstall_AttestedSecureLensErasureProceeds is the positive
// vector for the refusal above: the SAME uninstall carrying the operator's
// attestation commits, and reports the retirement in the unit
// UpgradeResult.SecureColumnsRetired uses — secure COLUMNS, so the two counts
// are comparable as one measure of custody history leaving the system.
func TestInstaller_Uninstall_AttestedSecureLensErasureProceeds(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{RetiredSecureLenses: []RetiredSecureLens{{
		Lens: "sampleSecureLens",
		Note: "test fixture — no ciphertext was ever written",
	}}})
	if err != nil {
		t.Fatalf("Uninstall with attestation: %v", err)
	}
	if uninst.SecureLensRetirementsAttested != 1 {
		t.Fatalf("SecureLensRetirementsAttested = %d, want 1 (the lens's single secure column)", uninst.SecureLensRetirementsAttested)
	}
	if len(uninst.SecureLensRetirementsUnused) != 0 {
		t.Fatalf("SecureLensRetirementsUnused = %v, want none — the attestation matched the erasure", uninst.SecureLensRetirementsUnused)
	}
	if !slices.Contains(uninst.Tombstoned, secureLensSpecKey(def)) {
		t.Fatalf("secure lens spec key not tombstoned: %v", uninst.Tombstoned)
	}
}

// TestInstaller_Uninstall_RefusesErasureOfTombstonedSpecUnderLiveRoot is the
// bypass the classification predicate exists to close. A lens `.spec` soft-
// deleted out of band still decodes and still declares its secureColumns, and
// declaredLensIDs reads it off a lens it selected by the vertex ROOT — so with
// the root live the oracle STILL sees this lens, and the root tombstone this
// uninstall is about to commit is precisely what erases it. Classifying on the
// spec's own isDeleted would file it under "already erased" and wave the
// erasure straight past the guard with a clean report.
func TestInstaller_Uninstall_RefusesErasureOfTombstonedSpecUnderLiveRoot(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	specKey := secureLensSpecKey(def)
	tombstoneKey(t, ctx, conn, specKey)

	_, err := inst.Uninstall(ctx, def.Name, UninstallOptions{})
	if err == nil {
		t.Fatal("expected Uninstall to refuse: the lens ROOT is live, so the oracle still sees this lens and this run erases it")
	}
	if !errors.Is(err, ErrUndeclaredSecureLensErasure) {
		t.Fatalf("Uninstall error = %v, want one wrapping ErrUndeclaredSecureLensErasure", err)
	}

	// And with the attestation it proceeds, counted as a real retirement — the
	// erasure is genuine, not an artefact of the tombstoned spec.
	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{RetiredSecureLenses: []RetiredSecureLens{{
		Lens: "sampleSecureLens",
		Note: "test fixture — no ciphertext was ever written",
	}}})
	if err != nil {
		t.Fatalf("Uninstall with attestation: %v", err)
	}
	if len(uninst.SecureColumnsErased) != 1 || uninst.SecureColumnsErased[0].Key != specKey {
		t.Fatalf("SecureColumnsErased = %+v, want exactly one entry for %s", uninst.SecureColumnsErased, specKey)
	}
	if uninst.SecureLensRetirementsAttested != 1 {
		t.Fatalf("SecureLensRetirementsAttested = %d, want 1", uninst.SecureLensRetirementsAttested)
	}
}

// TestInstaller_Uninstall_ReportsUnusedSecureLensAttestation pins the reporting
// half: an attestation that matched no erasure is inert, never a refusal — the
// uninstall is not the operator's chance to be told their spelling was wrong by
// having the whole run rejected. It IS reported, because a misspelled
// canonicalName is otherwise indistinguishable from one that covered something.
func TestInstaller_Uninstall_ReportsUnusedSecureLensAttestation(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	uninst, err := inst.Uninstall(ctx, def.Name, UninstallOptions{RetiredSecureLenses: []RetiredSecureLens{{
		Lens: "noSuchLens",
		Note: "attests to an erasure this package does not have",
	}}})
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !slices.Equal(uninst.SecureLensRetirementsUnused, []string{"noSuchLens"}) {
		t.Fatalf("SecureLensRetirementsUnused = %v, want [noSuchLens]", uninst.SecureLensRetirementsUnused)
	}
	if uninst.SecureLensRetirementsAttested != 0 {
		t.Fatalf("SecureLensRetirementsAttested = %d, want 0", uninst.SecureLensRetirementsAttested)
	}
}

// TestInstaller_Uninstall_NotInstalledWrapsTheSentinel keeps Loupe's status
// mapping honest: the uninstall path is the only caller that reported "not
// installed" as a bare error, so the 409 row covering ErrNotInstalled pinned
// nothing here and the console rendered a permanent condition as the 502 its
// own front end retries.
func TestInstaller_Uninstall_NotInstalledWrapsTheSentinel(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	_, err := inst.Uninstall(ctx, "never-installed-package", UninstallOptions{})
	if err == nil {
		t.Fatal("expected Uninstall of an absent package to fail")
	}
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Uninstall error = %v, want one wrapping ErrNotInstalled", err)
	}
}

// TestInstaller_Uninstall_RefusesMalformedSecureLensAttestation pins the
// validation floor and the moment it runs. Each case names a package that is
// not installed, so the "not installed" error is what a run that read anything
// at all would return: getting the validation error instead is the proof that a
// malformed attestation is refused on EVERY run, not only on the ones whose
// erasures happen to reach the enforcement half.
func TestInstaller_Uninstall_RefusesMalformedSecureLensAttestation(t *testing.T) {
	cases := []struct {
		name  string
		atts  []RetiredSecureLens
		wants string
	}{
		{
			name:  "empty lens",
			atts:  []RetiredSecureLens{{Lens: "  ", Note: "why this is safe"}},
			wants: "names no Lens",
		},
		{
			name:  "empty note",
			atts:  []RetiredSecureLens{{Lens: "sampleSecureLens", Note: "  "}},
			wants: "empty Note",
		},
		{
			name: "duplicate lens",
			atts: []RetiredSecureLens{
				{Lens: "sampleSecureLens", Note: "first reason"},
				{Lens: "sampleSecureLens", Note: "second reason"},
			},
			wants: "twice",
		},
	}
	ctx, _, inst := newInstallerHarness(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inst.Uninstall(ctx, "never-installed-package", UninstallOptions{RetiredSecureLenses: tc.atts})
			if err == nil {
				t.Fatal("expected Uninstall to refuse a malformed attestation, got nil error")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("Uninstall error = %v, want one naming %q", err, tc.wants)
			}
			if errors.Is(err, ErrNotInstalled) {
				t.Fatalf("the attestation was validated only after the installed-package read: %v", err)
			}
			if !errors.Is(err, ErrInvalidUninstallOptions) {
				t.Fatalf("Uninstall error = %v, want one wrapping ErrInvalidUninstallOptions (Loupe maps it to 400)", err)
			}
		})
	}
}

// TestInstaller_Uninstall_ReturnsErrorOnUnparseableSpec is B5's regression:
// a `.spec` document that fails to json.Unmarshal must surface as an error,
// not be silently swallowed into "no custody left the oracle's view" — the
// one claim that cannot be substantiated when the document could not even be
// read. Mirrors the retention-holder loop's identical parse-error handling.
func TestInstaller_Uninstall_ReturnsErrorOnUnparseableSpec(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	specKey := secureLensSpecKey(def)
	if _, err := conn.KVPut(ctx, CoreBucket, specKey, []byte("not json")); err != nil {
		t.Fatalf("KVPut malformed spec: %v", err)
	}

	if _, err := inst.Uninstall(ctx, def.Name, UninstallOptions{}); err == nil {
		t.Fatal("expected Uninstall to fail on an unparseable spec, got nil error")
	}
}

// TestInstaller_Upgrade_RefusesClassFlip proves the retention-class-key-
// custody-design.md §30 B1 bypass is closed end-to-end through the real
// installer harness: an upgrade that flips a secure lens's Class away from
// "meta.lens" while KEEPING its committed SecureColumns is now refused before
// any KV write, not merely at the unit-validator level. Without this refusal
// the upgrade would land clean with UpgradeResult.SecureColumnsRetired == 0
// — the drop guard never fires because the columns are never dropped — while
// both Refractor's destruction-readiness oracle (declaredLensIDs,
// internal/refractor/health/registry_probe.go) and its own lens registry
// silently stop seeing the lens, whose ciphertext stays live in Postgres.
func TestInstaller_Upgrade_RefusesClassFlip(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	v2 := defWithSecureLens("0.2.0", []string{"identity"}, "")
	found := false
	for i := range v2.Lenses {
		if v2.Lenses[i].CanonicalName == "sampleSecureLens" {
			v2.Lenses[i].Class = "meta.lens.archived"
			found = true
		}
	}
	if !found {
		t.Fatal("test fixture drift: sampleSecureLens not found in defWithSecureLens output")
	}

	_, err := inst.Upgrade(ctx, v2)
	if err == nil {
		t.Fatal("expected the Class flip (with SecureColumns still declared) to be refused, got nil error")
	}
	if !strings.Contains(err.Error(), "meta.lens") {
		t.Fatalf("refusal does not name the reserved Class literal, got: %v", err)
	}
}

// TestInstaller_Upgrade_RefusesEventStreamSecureColumnCombination proves the
// §30 B2 bypass is closed the same way: an upgrade that adds an eventStream
// Source to a lens while KEEPING its committed SecureColumns is refused,
// instead of landing clean with SecureColumnsRetired == 0 while both
// Refractor's CoreKVSource discovery and declaredLensIDs skip the eventStream
// spec outright, stranding the ciphertext with neither side raising a signal.
func TestInstaller_Upgrade_RefusesEventStreamSecureColumnCombination(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	v2 := defWithSecureLens("0.2.0", []string{"identity"}, "")
	found := false
	for i := range v2.Lenses {
		if v2.Lenses[i].CanonicalName == "sampleSecureLens" {
			v2.Lenses[i].Source = &SourceConfig{Kind: "eventStream"}
			found = true
		}
	}
	if !found {
		t.Fatal("test fixture drift: sampleSecureLens not found in defWithSecureLens output")
	}

	_, err := inst.Upgrade(ctx, v2)
	if err == nil {
		t.Fatal("expected the eventStream+SecureColumns combination to be refused, got nil error")
	}
	if !strings.Contains(err.Error(), "eventStream") {
		t.Fatalf("refusal does not name the eventStream combination, got: %v", err)
	}
}

// TestInstaller_Uninstall_RaceOnDeclaredKeyRejected proves the F-011 per-key
// OCC fix (Contract #8 §8.3): a concurrent write to a declared key between
// the moment it is read and the moment the tombstone commits is rejected
// (RevisionConflict), not silently overwritten, and the whole atomic batch
// leaves the package fully installed — no partial tombstone.
//
// A live goroutine race on Installer.Uninstall's internal read-then-submit
// window isn't observable from a black-box test without a production-code
// hook; instead this reconstructs the exact interleave Uninstall would hit —
// capture a declared key's revision (as Uninstall's own read loop does),
// have a concurrent write bump it, then submit the SAME UninstallPackage op
// shape Uninstall builds, keyed on the now-stale captured revision — proving
// the script + Processor honor the OCC condition end-to-end.
func TestInstaller_Uninstall_RaceOnDeclaredKeyRejected(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")
	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(res.DeclaredKeys) == 0 {
		t.Fatalf("no declared keys to race against")
	}
	raceKey := res.DeclaredKeys[0]

	// Capture the revision Uninstall's own read loop would see right now.
	entry, err := conn.KVGet(ctx, CoreBucket, raceKey)
	if err != nil {
		t.Fatalf("capture revision: %v", err)
	}
	staleRev := entry.Revision

	// A concurrent write races in and bumps the key past the captured
	// revision (simulates another admin action landing in the window
	// between Uninstall's read and its commit).
	if _, err := conn.KVUpdate(ctx, CoreBucket, raceKey, entry.Value, staleRev); err != nil {
		t.Fatalf("simulated concurrent write: %v", err)
	}

	// Build the exact op shape Uninstall submits, keyed on the now-stale
	// revision, and submit it directly.
	requestID := deterministicNanoID(def.Name, def.Version, "race-uninstall-op")
	payload := map[string]any{
		"name": def.Name,
		"declaredKeys": []map[string]any{
			{"key": raceKey, "expectedRevision": staleRev},
		},
	}
	reply, err := inst.submitOp(ctx, "UninstallPackage", "UninstallPackage", requestID, payload)
	if err != nil {
		t.Fatalf("submitOp: %v", err)
	}
	if reply.Status != processor.ReplyStatusRejected {
		t.Fatalf("status = %q, want rejected", reply.Status)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeRevisionConflict {
		t.Fatalf("error = %+v, want code RevisionConflict", reply.Error)
	}

	// The package must be left fully installed — nothing tombstoned.
	after, err := conn.KVGet(ctx, CoreBucket, raceKey)
	if err != nil {
		t.Fatalf("post-conflict read: %v", err)
	}
	if strings.Contains(string(after.Value), `"isDeleted":true`) {
		t.Fatalf("key %s was tombstoned despite the OCC rejection", raceKey)
	}

	// A subsequent, ordinary uninstall (re-reading fresh) succeeds — the
	// documented retry story.
	if _, err := inst.Uninstall(ctx, def.Name, UninstallOptions{}); err != nil {
		t.Fatalf("retry Uninstall after conflict: %v", err)
	}
}

// TestInstaller_SubmitOp_UsesSubmitFieldWhenSet proves submitOp's dispatch:
// when Submit is set, it is used INSTEAD of direct-NATS request/reply (the
// Installer holds no Conn/NATS connection at all here — a fallback to the
// direct-NATS path would panic on the nil Conn before this test could ever
// observe a false pass). cmd/loupe wires Submit to relay through the Gateway
// under the caller's own operator credential
// (loupe-operator-auth-lift-design.md §3.2); cmd/lattice-pkg and every
// existing test in this file leave Submit nil and are provably unaffected
// (they predate this field and continue to pass unchanged).
func TestInstaller_SubmitOp_UsesSubmitFieldWhenSet(t *testing.T) {
	var gotOpType, gotClass, gotReqID string
	var gotPayload map[string]any
	inst := &Installer{
		AdminActor: "vtx.identity.admin1",
		Now:        func() time.Time { return time.Time{} },
		Submit: func(ctx context.Context, operationType, class, requestID string, payload map[string]any) (*processor.OperationReply, error) {
			gotOpType, gotClass, gotReqID, gotPayload = operationType, class, requestID, payload
			return &processor.OperationReply{Status: processor.ReplyStatusAccepted, RequestID: requestID}, nil
		},
	}

	reply, err := inst.submitOp(context.Background(), "InstallPackage", "InstallPackage", "req-1", map[string]any{"x": float64(1)})
	if err != nil {
		t.Fatalf("submitOp: %v", err)
	}
	if reply.Status != processor.ReplyStatusAccepted {
		t.Errorf("Status = %q, want accepted", reply.Status)
	}
	if gotOpType != "InstallPackage" || gotClass != "InstallPackage" || gotReqID != "req-1" {
		t.Errorf("Submit received opType=%q class=%q reqID=%q, want InstallPackage/InstallPackage/req-1", gotOpType, gotClass, gotReqID)
	}
	if gotPayload["x"] != float64(1) {
		t.Errorf("Submit received payload %+v", gotPayload)
	}
}

// recordingSubmit replaces an Installer's op submission with one that records
// what it was asked to send and never reaches the Processor. An assertion that
// nothing was recorded is what proves a refusal happened BEFORE the submit —
// checking Core KV instead proves nothing here, because an install whose batch
// is create-only over occupied keys commits nothing whether it is refused up
// front or rejected atomically at the end.
func recordingSubmit(sent *[]string) func(context.Context, string, string, string, map[string]any) (*processor.OperationReply, error) {
	return func(_ context.Context, operationType, _, requestID string, _ map[string]any) (*processor.OperationReply, error) {
		*sent = append(*sent, operationType)
		return &processor.OperationReply{Status: processor.ReplyStatusAccepted, RequestID: requestID}, nil
	}
}

// TestInstaller_RefusesReinstallOverUninstall pins the occupancy gate on an
// install whose own declared keys are all occupied by tombstones: an uninstall
// soft-deletes every declared key without freeing it, and the
// version-independent keys a fresh manifest mints are the exact same ones. The
// install is refused before it is submitted, and the refusal names the keys,
// the two-verb restore, and what that restore does and does not put back.
func TestInstaller_RefusesReinstallOverUninstall(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")

	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("first Install: %v", err)
	}
	if _, err := inst.Uninstall(ctx, def.Name, UninstallOptions{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	var sent []string
	inst.Submit = recordingSubmit(&sent)

	_, err = inst.Install(ctx, def)
	if err == nil {
		t.Fatal("reinstall over an uninstall must be refused; a nil error here is the false green the gate exists to kill")
	}
	if !errors.Is(err, ErrDeclaredKeysOccupied) {
		t.Fatalf("want ErrDeclaredKeysOccupied, got %v", err)
	}
	if len(sent) != 0 {
		t.Fatalf("the refusal must precede the submit; ops sent: %v", sent)
	}

	// Bucket membership is read from the typed error, not from the prose.
	var occ *DeclaredKeysOccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("refusal must be a *DeclaredKeysOccupiedError, got %T", err)
	}
	if occ.PackageName != def.Name {
		t.Errorf("PackageName = %q, want %q", occ.PackageName, def.Name)
	}
	wantTombstoned := append([]string{}, res.DeclaredKeys...)
	slices.Sort(wantTombstoned)
	if !slices.Equal(occ.Tombstoned, wantTombstoned) {
		t.Errorf("Tombstoned = %v, want every declared key %v", occ.Tombstoned, wantTombstoned)
	}
	// Nothing this package declared is live, so a bucket that also collects the
	// tombstoned keys shows up here rather than hiding behind matching prose.
	if len(occ.Live) != 0 {
		t.Errorf("Live = %v, want empty — no declared key survives this uninstall", occ.Live)
	}

	msg := err.Error()
	if !strings.Contains(msg, def.Name) {
		t.Errorf("refusal must name the package; got %v", msg)
	}
	// The sample is the sorted head of the tombstoned bucket, which here is
	// every declared key — so the lowest-sorting declared key must appear.
	if !strings.Contains(msg, wantTombstoned[0]) {
		t.Errorf("refusal must name a sample of the tombstoned keys (expected %q); got %v", wantTombstoned[0], msg)
	}
	if !strings.Contains(msg, "tombstoned by an uninstall") {
		t.Errorf("refusal must say the keys were tombstoned by an uninstall; got %v", msg)
	}
	// The restore remedy is two verbs, in order, and the SECOND is the one that
	// confers authority — a message naming only the mint sends the operator to a
	// success reply with no grant behind it.
	mintAt, grantAt := strings.Index(msg, restoreMintVerb), strings.Index(msg, restoreGrantVerb)
	if mintAt < 0 || grantAt < 0 {
		t.Errorf("refusal must name both %s and %s; got %v", restoreMintVerb, restoreGrantVerb, msg)
	} else if mintAt > grantAt {
		t.Errorf("the restore verbs must be named in execution order (%s then %s); got %v",
			restoreMintVerb, restoreGrantVerb, msg)
	}
	if !strings.Contains(msg, "grantedBy") {
		t.Errorf("refusal must say the grant EDGE is what confers authority; got %v", msg)
	}
	// What the remedy produces is runtime-origin, which the operator has to know
	// before choosing it: no manifest carries it and no uninstall retracts it.
	if !strings.Contains(msg, "declaredBy") || !strings.Contains(msg, "UninstallPackage") {
		t.Errorf("refusal must say the restored grant is runtime-origin and never retracted by a future uninstall; got %v", msg)
	}
	// Clearing the key by hand is the obvious next move after "cannot be
	// re-created", and it does not work either. The message has to say so.
	if !strings.Contains(msg, "nats kv del") || !strings.Contains(msg, "Nats-Expected-Last-Subject-Sequence") {
		t.Errorf("refusal must close the KV delete/purge trap it otherwise opens; got %v", msg)
	}
}

// TestInstaller_RefusesLiveDeclaredKeyOccupant is the other bucket: a declared
// key that is occupied and NOT tombstoned, with no live package manifest
// anywhere. The occupant seeded here is a FOREIGN one — not under the
// retention-class prefix — so it must draw the foreign instruction (read it,
// then rename or upgrade the owner), never the holder one.
func TestInstaller_RefusesLiveDeclaredKeyOccupant(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")

	perm := def.Permissions[0]
	occupied := "vtx.permission." + entityNanoID(def.Name, permTag(perm.OperationType, perm.Scope))
	doc := map[string]any{
		"class":     "permission",
		"isDeleted": false,
		"data": map[string]any{
			"operationType": perm.OperationType,
			"scope":         perm.Scope,
			"origin":        "runtime",
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal live occupant: %v", err)
	}
	if _, err := conn.KVCreate(ctx, CoreBucket, occupied, raw); err != nil {
		t.Fatalf("seed live occupant %s: %v", occupied, err)
	}

	_, err = inst.Install(ctx, def)
	if err == nil {
		t.Fatal("install over a live occupant of a declared key must be refused; the create-only batch cannot write it")
	}
	if !errors.Is(err, ErrDeclaredKeysOccupied) {
		t.Fatalf("want ErrDeclaredKeysOccupied, got %v", err)
	}
	var occ *DeclaredKeysOccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("refusal must be a *DeclaredKeysOccupiedError, got %T", err)
	}
	if !slices.Equal(occ.Live, []string{occupied}) {
		t.Errorf("Live = %v, want exactly [%s]", occ.Live, occupied)
	}
	if len(occ.Tombstoned) != 0 {
		t.Errorf("Tombstoned = %v, want empty — this occupant is live", occ.Tombstoned)
	}

	msg := err.Error()
	if !strings.Contains(msg, "LIVE") || !strings.Contains(msg, occupied) {
		t.Errorf("refusal must report %s in the live bucket; got %v", occupied, msg)
	}
	if strings.Contains(msg, "tombstoned by an uninstall") {
		t.Errorf("a live occupant must not be reported as tombstoned by an uninstall; got %v", msg)
	}
	if !strings.Contains(msg, "1 already-committed key(s)") {
		t.Errorf("exactly one key is occupied; got %v", msg)
	}
	// A foreign occupant gets the foreign instruction. Reporting it under the
	// retention-holder clause would tell the operator a key another writer owns
	// is expected and untouchable.
	if !strings.Contains(msg, "held by a writer other than this package") {
		t.Errorf("a foreign occupant must draw the foreign-occupant instruction; got %v", msg)
	}
	if strings.Contains(msg, "retention-class holder") {
		t.Errorf("a foreign occupant must not be described as a retention-class holder; got %v", msg)
	}

	// Nothing installed: the refusal precedes the submit.
	pkg, err := inst.findInstalledPackage(ctx, def.Name)
	if err != nil {
		t.Fatalf("findInstalledPackage: %v", err)
	}
	if pkg != nil {
		t.Fatalf("a refused install left a package vertex behind: %+v", pkg)
	}
}

// TestInstaller_ReinstallOverUninstall_ReportsRetentionHolderAsHolder is the
// routine two-bucket case, not an exotic one: a package declaring a retention
// class has its holder keys in declaredKeys, and uninstall deliberately leaves
// them LIVE while tombstoning everything else. So an uninstall→reinstall of any
// such package prints both clauses every time — and the holder's key is derived
// from this package's own name and the class's canonicalName, so the foreign
// occupant's "install under a name whose keys are free" is not a move that
// exists for it under this package name at all.
func TestInstaller_ReinstallOverUninstall_ReportsRetentionHolderAsHolder(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := defWithRetentionClass("0.1.0", "sampleClass1")

	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := inst.Uninstall(ctx, def.Name, UninstallOptions{}); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	_, err := inst.Install(ctx, def)
	if !errors.Is(err, ErrDeclaredKeysOccupied) {
		t.Fatalf("want ErrDeclaredKeysOccupied on reinstall, got %v", err)
	}
	holderKey := RetentionClassKey(def.Name, "sampleClass1")
	wantLive := []string{holderKey, holderKey + ".retentionPolicy"}
	slices.Sort(wantLive)

	var occ *DeclaredKeysOccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("refusal must be a *DeclaredKeysOccupiedError, got %T", err)
	}
	if !slices.Equal(occ.Live, wantLive) {
		t.Errorf("Live = %v, want exactly the two holder keys %v", occ.Live, wantLive)
	}
	if len(occ.Tombstoned) == 0 {
		t.Errorf("Tombstoned is empty; the rest of the declared set is tombstoned by the uninstall")
	}
	for _, k := range occ.Tombstoned {
		if slices.Contains(wantLive, k) {
			t.Errorf("holder key %s is reported in BOTH buckets: %v", k, occ.Tombstoned)
		}
	}

	msg := err.Error()
	if !strings.Contains(msg, holderKey) {
		t.Errorf("refusal must name the live holder key %s; got %v", holderKey, msg)
	}
	if !strings.Contains(msg, "retention-class holder") || !strings.Contains(msg, "ShredRetentionClassKey") {
		t.Errorf("a live holder must draw the holder clause, which names the one verb that may destroy it; got %v", msg)
	}
	// The holder's key comes from this package's own name, so the foreign
	// remedy is unactionable for it and must not be applied to it.
	if strings.Contains(msg, "held by a writer other than this package") {
		t.Errorf("this package's own retention holder is not a foreign occupant; got %v", msg)
	}
	// Both buckets are reported, and neither absorbs the other.
	if !strings.Contains(msg, "tombstoned by an uninstall") {
		t.Errorf("the rest of the declared set is tombstoned and must be reported as such; got %v", msg)
	}
}

// TestDeclaredKeyOccupants_FailsClosedOnReadError pins the ratified fail-closed
// posture: a batched read that FAILS is not evidence of a clean bucket. A probe
// that swallowed the error would report every key free and hand the install
// straight back to the silent failure the gate exists to replace, so the error
// has to reach the caller. A cancelled context is the cheapest read failure
// that needs no fault injection.
func TestDeclaredKeyOccupants_FailsClosedOnReadError(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")
	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Sanity: the same call over the same keys succeeds on a live context, so a
	// failure below is the cancellation and not an empty key set.
	if _, live, err := inst.declaredKeyOccupants(ctx, res.DeclaredKeys); err != nil || len(live) == 0 {
		t.Fatalf("precondition: declaredKeyOccupants over an installed package = (live %d, %v), want a live set and no error", len(live), err)
	}

	dead, cancel := context.WithCancel(ctx)
	cancel()
	tombstoned, live, err := inst.declaredKeyOccupants(dead, res.DeclaredKeys)
	if err == nil {
		t.Fatalf("a failed read must refuse the install, not report a clean bucket (tombstoned %v, live %v)", tombstoned, live)
	}
	if tombstoned != nil || live != nil {
		t.Errorf("a failed read must return no buckets at all, got tombstoned %v live %v", tombstoned, live)
	}
}

// TestReadDeclaredKeyOccupants_ClassifiesAcrossChunkBoundaries drives the
// batched read across several requests with a chunk size small enough to make
// the boundary reachable. Every key must be classified regardless of which
// request carried it: a loop that stops after the first chunk, or one that
// mis-slices at the boundary, silently reports the rest of a package's keys as
// free — the same false green as swallowing a read error, arriving by a
// different route.
func TestReadDeclaredKeyOccupants_ClassifiesAcrossChunkBoundaries(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	live := []string{"vtx.sample.Live1AaBbCcDdEeFfGg", "vtx.sample.Live2AaBbCcDdEeFfGg", "vtx.sample.Live3AaBbCcDdEeFfGg"}
	tombstoned := []string{"vtx.sample.Dead1AaBbCcDdEeFfGg", "vtx.sample.Dead2AaBbCcDdEeFfGg"}
	absent := "vtx.sample.Gone1AaBbCcDdEeFfGg"

	seed := func(key string, deleted bool) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"class": "sample", "isDeleted": deleted, "data": map[string]any{}})
		if err != nil {
			t.Fatalf("marshal %s: %v", key, err)
		}
		if _, err := conn.KVCreate(ctx, CoreBucket, key, raw); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	for _, k := range live {
		seed(k, false)
	}
	for _, k := range tombstoned {
		seed(k, true)
	}

	// Interleaved so no chunk holds only one class, and the absent key sits at a
	// boundary rather than at either end.
	declared := []string{live[0], tombstoned[0], absent, live[1], tombstoned[1], live[2]}
	for _, chunkSize := range []int{1, 2, 4, len(declared), len(declared) + 3} {
		gotTombstoned, gotLive, err := inst.readDeclaredKeyOccupants(ctx, declared, chunkSize)
		if err != nil {
			t.Fatalf("chunkSize %d: %v", chunkSize, err)
		}
		wantLive := append([]string{}, live...)
		slices.Sort(wantLive)
		wantTombstoned := append([]string{}, tombstoned...)
		slices.Sort(wantTombstoned)
		if !slices.Equal(gotLive, wantLive) {
			t.Errorf("chunkSize %d: live = %v, want %v", chunkSize, gotLive, wantLive)
		}
		if !slices.Equal(gotTombstoned, wantTombstoned) {
			t.Errorf("chunkSize %d: tombstoned = %v, want %v", chunkSize, gotTombstoned, wantTombstoned)
		}
		if slices.Contains(gotLive, absent) || slices.Contains(gotTombstoned, absent) {
			t.Errorf("chunkSize %d: a key absent from the bucket must be reported in neither bucket", chunkSize)
		}
	}
}

// TestPartitionLiveOccupants pins the split the live clauses key their differing
// instructions on: this package's own retention-class holders, which an
// uninstall preserves on purpose and whose keys derive from the package's own
// name, versus everything else.
func TestPartitionLiveOccupants(t *testing.T) {
	holderKey := RetentionClassKey("sample-pkg", "sampleClass1")
	holders, foreign := partitionLiveOccupants([]string{
		"vtx.permission.Frozen1PermKeyAbcdef",
		holderKey,
		holderKey + ".retentionPolicy",
		"vtx.meta.Frozen1MetaKeyAbcdef",
	})
	wantHolders := []string{holderKey, holderKey + ".retentionPolicy"}
	if !slices.Equal(holders, wantHolders) {
		t.Errorf("holders = %v, want %v", holders, wantHolders)
	}
	wantForeign := []string{"vtx.permission.Frozen1PermKeyAbcdef", "vtx.meta.Frozen1MetaKeyAbcdef"}
	if !slices.Equal(foreign, wantForeign) {
		t.Errorf("foreign = %v, want %v", foreign, wantForeign)
	}
}

// clauseWith returns the one clause containing every token, or "". Assertions
// run against a single clause rather than the concatenated message so a token
// belonging to one claim can never satisfy an assertion about another.
func clauseWith(clauses []string, tokens ...string) string {
	for _, c := range clauses {
		hit := true
		for _, tok := range tokens {
			if !strings.Contains(c, tok) {
				hit = false
				break
			}
		}
		if hit {
			return c
		}
	}
	return ""
}

// TestClassifyRestorePermissions pins the partition the refusal's honesty rests
// on: which declared grants the two-verb runtime restore can actually put back.
// Reserved outranks the lane note (an operationType that can never authorize
// from a runtime mint has no partial restore to describe), and an operationType
// declared at two scopes is named once.
func TestClassifyRestorePermissions(t *testing.T) {
	adv := classifyRestorePermissions([]PermissionSpec{
		{OperationType: "SampleOp", Scope: "any"},
		{OperationType: "SampleOp", Scope: "self"},
		{OperationType: "UpdatePermission", Scope: "any"},
		{OperationType: "ShredRetentionClassKey", Scope: "any"},
		{OperationType: "PrivilegedOp", Scope: "any", Lanes: []string{"meta"}},
		{OperationType: restoreMintVerb, Scope: "any"},
		{OperationType: restoreGrantVerb, Scope: "any"},
	})

	wantRestorable := []string{restoreMintVerb, restoreGrantVerb, "SampleOp"}
	slices.Sort(wantRestorable)
	if !slices.Equal(adv.restorable, wantRestorable) {
		t.Errorf("restorable = %v, want %v", adv.restorable, wantRestorable)
	}
	if !slices.Equal(adv.reserved, []string{"ShredRetentionClassKey", "UpdatePermission"}) {
		t.Errorf("reserved = %v, want the two core-reserved operationTypes", adv.reserved)
	}
	if !slices.Equal(adv.lanePartial, []string{"PrivilegedOp (lane meta)"}) {
		t.Errorf("lanePartial = %v, want PrivilegedOp rendered with its lane", adv.lanePartial)
	}
	wantGrantors := []string{restoreMintVerb, restoreGrantVerb}
	slices.Sort(wantGrantors)
	if !slices.Equal(adv.grantorVerbs, wantGrantors) {
		t.Errorf("grantorVerbs = %v, want %v", adv.grantorVerbs, wantGrantors)
	}

	// The claim that would be false if a qualification were folded back into the
	// general advice: nothing unrestorable may appear as restorable.
	for _, op := range []string{"UpdatePermission", "ShredRetentionClassKey", "PrivilegedOp"} {
		if slices.Contains(adv.restorable, op) {
			t.Errorf("%s is not restorable by the two-verb runtime mint; restorable = %v", op, adv.restorable)
		}
	}

	// The reserved test is the Processor's own predicate, so a change to the
	// reserved set moves both sides together. Anchor that it is really consulted.
	if !processor.WouldRefuseReservedGrant("UpdatePermission", "runtime") {
		t.Fatal("UpdatePermission is expected to be core-reserved against a runtime origin")
	}
}

// TestTombstonedOccupancyClauses_QualifyPerDeclaredPermission pins the refusal
// text against the state it is printed in. The generic advice — "mint it back
// at runtime" — is true for an ordinary grant, partial for a lane-bearing one,
// false for a core-reserved one, and possibly unrunnable when the uninstalled
// package is the one that grants the restore verbs.
func TestTombstonedOccupancyClauses_QualifyPerDeclaredPermission(t *testing.T) {
	occupied := []string{"vtx.permission.Frozen1PermKeyAbcdef"}

	t.Run("MixedRestorability", func(t *testing.T) {
		def := Definition{
			Name: "qualified-pkg",
			Permissions: []PermissionSpec{
				{OperationType: "SampleOp", Scope: "any"},
				{OperationType: "PrivilegedOp", Scope: "any", Lanes: []string{"meta"}},
				{OperationType: "UpdatePermission", Scope: "any"},
				{OperationType: restoreGrantVerb, Scope: "any"},
			},
		}
		clauses := tombstonedOccupancyClauses(def, occupied)

		remedy := clauseWith(clauses, restoreMintVerb, restoreGrantVerb, "grantedBy")
		if remedy == "" {
			t.Fatalf("no clause names both restore verbs and the grant edge: %v", clauses)
		}
		if strings.Index(remedy, restoreMintVerb) > strings.Index(remedy, restoreGrantVerb) {
			t.Errorf("the verbs must be named in execution order (%s then %s): %q",
				restoreMintVerb, restoreGrantVerb, remedy)
		}
		if clauseWith(clauses, "declaredBy", "UninstallPackage") == "" {
			t.Errorf("no clause states the restored grant is runtime-origin and never retracted: %v", clauses)
		}
		if clauseWith(clauses, "nats kv del", "Nats-Expected-Last-Subject-Sequence") == "" {
			t.Errorf("no clause closes the KV delete/purge trap: %v", clauses)
		}

		// The fully-restorable list is the assertion that fails if any
		// qualification is dropped back into the general claim.
		restorable := clauseWith(clauses, "fully restorable")
		if restorable == "" || !strings.Contains(restorable, "SampleOp") {
			t.Fatalf("the ordinary grant must be named as fully restorable: %v", clauses)
		}
		for _, op := range []string{"UpdatePermission", "PrivilegedOp"} {
			if strings.Contains(restorable, op) {
				t.Errorf("%s is named as fully restorable: %q", op, restorable)
			}
		}

		if clauseWith(clauses, "UpdatePermission", processor.AlertCodeReservedOperationGrantRejected) == "" {
			t.Errorf("the core-reserved grant must be named alongside the alert its mint raises: %v", clauses)
		}
		if clauseWith(clauses, "PrivilegedOp (lane meta)", "UpdatePermission") == "" {
			t.Errorf("the lane-bearing grant must be named with its lane and with the reason the lane is unrecoverable: %v", clauses)
		}
		if clauseWith(clauses, "declares "+restoreGrantVerb+" itself") == "" {
			t.Errorf("a package declaring a restore verb must be warned the uninstall revoked it: %v", clauses)
		}
	})

	t.Run("NothingRestorable", func(t *testing.T) {
		def := Definition{
			Name:        "reserved-only-pkg",
			Permissions: []PermissionSpec{{OperationType: "UpdatePermission", Scope: "any"}},
		}
		clauses := tombstonedOccupancyClauses(def, occupied)
		if c := clauseWith(clauses, restoreGrantVerb); c != "" {
			t.Errorf("no declared grant is restorable, so the two-verb remedy must not be offered: %q", c)
		}
		if clauseWith(clauses, "only route back is installing under a package name") == "" {
			t.Errorf("a package with nothing restorable must be told what IS available: %v", clauses)
		}
	})

	t.Run("NoDeclaredPermissions", func(t *testing.T) {
		def := Definition{Name: "no-perms-pkg"}
		clauses := tombstonedOccupancyClauses(def, occupied)
		if c := clauseWith(clauses, restoreMintVerb); c != "" {
			t.Errorf("a package that declared no permission has no grant to restore: %q", c)
		}
		// The create-only wall and the delete/purge trap still apply.
		if clauseWith(clauses, "cannot be re-created") == "" ||
			clauseWith(clauses, "nats kv del") == "" {
			t.Errorf("the create-only fact and the KV-delete trap are independent of any grant: %v", clauses)
		}
	})
}

// TestInstaller_OccupancyGateLeavesCleanInstallsAlone is the negative vector on
// both sides of the gate: a greenfield install still installs, and a
// same-version re-run of a LIVE install still short-circuits to Skipped at the
// idempotency check — the gate runs after that arm, never in front of it.
func TestInstaller_OccupancyGateLeavesCleanInstallsAlone(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")

	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("clean fresh install must not be refused: %v", err)
	}
	if res.Skipped || len(res.DeclaredKeys) == 0 {
		t.Fatalf("clean fresh install: want a committed create batch, got %+v", res)
	}

	again, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("same-version re-install of a LIVE package must still skip, got error: %v", err)
	}
	if !again.Skipped {
		t.Fatalf("same-version re-install: want Skipped=true, got %+v", again)
	}
}

// contains is a copy of strings.Contains so this test file stays
// dependency-light (matches the style used in packages/identity-hygiene/package_test.go).
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestSampleWithOverflow_Truncates covers the renderer both refusal messages
// share. Its whole job is to stop a sample reading as the whole set, and
// nothing else asserted that it truncates at all — a cap raised to a huge
// number, or an overflow marker that never printed, would have been invisible.
func TestSampleWithOverflow_Truncates(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e", "f", "g"}

	t.Run("under the cap names everything and says nothing more", func(t *testing.T) {
		got := sampleWithOverflow(items[:3], 5, "and %d more")
		if got != "a, b, c" {
			t.Fatalf("got %q, want the whole list unannotated", got)
		}
	})
	t.Run("at the cap is still the whole list", func(t *testing.T) {
		if got := sampleWithOverflow(items[:5], 5, "and %d more"); got != "a, b, c, d, e" {
			t.Fatalf("got %q — the cap is inclusive, so five of five names five", got)
		}
	})
	t.Run("over the cap names the cap and counts the rest", func(t *testing.T) {
		got := sampleWithOverflow(items, 5, "and %d more")
		if got != "a, b, c, d, e, and 2 more" {
			t.Fatalf("got %q, want five names plus the unnamed count", got)
		}
	})
	t.Run("each refusal keeps its own phrasing", func(t *testing.T) {
		if got := sampleWithOverflow(items, removalSampleCap, "+%d more"); got != "a, b, c, d, +3 more" {
			t.Fatalf("got %q, want the coverage refusal's shorter cap and marker", got)
		}
	})
}

// TestOccupancyRefusalTruncates drives the occupancy refusal's own message,
// which reaches the renderer through boundedSample. The two refusals share one
// implementation, so this is what keeps a change made for one of them from
// silently re-shaping the other.
func TestOccupancyRefusalTruncates(t *testing.T) {
	live := []string{"vtx.a", "vtx.b", "vtx.c", "vtx.d", "vtx.e", "vtx.f", "vtx.g"}
	err := occupiedDeclaredKeysError(Definition{Name: "sample-pkg"}, nil, live)
	msg := err.Error()
	if !strings.Contains(msg, "and 2 more") {
		t.Fatalf("the occupancy refusal must count what it did not name; got %q", msg)
	}
	if strings.Contains(msg, "vtx.g") {
		t.Fatalf("the occupancy refusal named a key past its cap, so the sample reads as the whole set: %q", msg)
	}
	if !strings.Contains(msg, "7 already-committed key(s)") {
		t.Fatalf("the refusal must still state the TOTAL, or a truncated sample understates the problem; got %q", msg)
	}
}

// TestApply_RefuseRemovals_MalformedDeclarationRefuses pins the fail-closed
// direction of the one read the coverage guard rests on.
//
// A manifest whose declaredKeys cannot be read whole yields a SHORT old set,
// and a short old set makes any Definition look like it covers everything. That
// is the one wrong answer a coverage guard can give, so the partial-Definition
// caller refuses instead. A source-authored caller still converges: re-declaring
// from source is how the damaged manifest gets repaired, and refusing there
// would strand it.
func TestApply_RefuseRemovals_MalformedDeclarationRefuses(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	def := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}
	manifestKey := PackageVertexPrefix + entityNanoID(def.Name, "package") + ".manifest"
	doc := kvDoc(t, ctx, conn, manifestKey)
	data, _ := doc["data"].(map[string]any)
	// The real declaration, plus one entry that is no longer a key. Keeping the
	// real keys matters: the package still owns exactly what it owns, so the
	// only thing under test is the unreadable entry.
	data["declaredKeys"] = append(data["declaredKeys"].([]any), 42)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	entry, err := conn.KVGet(ctx, CoreBucket, manifestKey)
	if err != nil {
		t.Fatalf("KVGet manifest: %v", err)
	}
	if _, err := conn.KVUpdate(ctx, CoreBucket, manifestKey, raw, entry.Revision); err != nil {
		t.Fatalf("corrupt the manifest: %v", err)
	}

	if _, err := inst.Apply(ctx, sampleDef("0.2.0"), ApplyOptions{RefuseRemovals: true}); !errors.Is(err, ErrMalformedDeclaredKeys) {
		t.Fatalf("want ErrMalformedDeclaredKeys — a coverage guard may not conclude coverage from a list it cannot read; got %v", err)
	}
	// The source-authored path still converges over the same damage.
	if _, err := inst.Apply(ctx, sampleDef("0.2.0"), ApplyOptions{}); err != nil {
		t.Fatalf("the source-authored path must still repair a damaged manifest, got %v", err)
	}
}
