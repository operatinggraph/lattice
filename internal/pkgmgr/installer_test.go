package pkgmgr

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// startEmbeddedNATS spins up an in-process JetStream-enabled NATS server
// for the installer integration tests. Mirrors the harness used in
// internal/processor.
func startEmbeddedNATS(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	s := natsserver.RunServer(&opts)
	t.Cleanup(func() {
		if jsCfg := s.JetStreamConfig(); jsCfg != nil {
			defer os.RemoveAll(jsCfg.StoreDir)
		}
		s.Shutdown()
		_ = server.VERSION
	})
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

	uninst, err := inst.Uninstall(ctx, def.Name)
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
