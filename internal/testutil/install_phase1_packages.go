// Package testutil — installs the rbac-domain + identity-domain +
// identity-hygiene packages on top of a freshly-seeded kernel, supplying the
// identity + role DDLs that the kernel itself does not seed.
//
// Installs route through the Processor as InstallPackage ops.
// InstallPhase1Packages therefore stands up a REAL meta-lane
// CommitPath (stub-auth) so the submitted ops are consumed: the
// InstallPackage DDL script, step-6 validation, and step-8 atomic commit
// all run exactly as in production. Only the auth step is stubbed — every
// guardrail and the commit are real, so package installs are not faked.
//
// Usage:
//
//	func TestMyIntegrationStuff(t *testing.T) {
//	    ctx, conn := startEmbeddedNATSConnection(t)
//	    bootstrap.SeedPrimordial(ctx, conn)  // kernel-only
//	    testutil.InstallPhase1Packages(t, ctx, conn)
//	    // ...
//	}
//
// Idempotent: the installer's per-package presence check + the
// deterministic-requestId dedup skip already-installed packages.
package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	identitydomain "github.com/asolgan/lattice/packages/identity-domain"
	identityhygiene "github.com/asolgan/lattice/packages/identity-hygiene"
	rbacdomain "github.com/asolgan/lattice/packages/rbac-domain"
)

// InstallPhase1Packages installs rbac-domain, identity-domain, and
// identity-hygiene in dependency order against the given substrate
// connection by submitting InstallPackage ops through a real meta-lane
// CommitPath. The caller is responsible for having called
// bootstrap.LoadOrGenerate + bootstrap.SeedPrimordial first so the
// kernel (incl. the primordial InstallPackage DDL) + admin identity
// exist.
//
// Each install is idempotent; calling this helper twice with the same
// connection is safe.
func InstallPhase1Packages(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()

	stop := RunMetaInstallPipeline(t, ctx, conn)
	defer stop()

	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{
		"operator": bootstrap.RoleOperatorID,
	}

	for _, def := range []pkgmgr.Definition{
		rbacdomain.Package,
		identitydomain.Package,
		identityhygiene.Package,
	} {
		if _, err := inst.Install(ctx, def); err != nil {
			t.Fatalf("InstallPhase1Packages: install %s: %v", def.Name, err)
		}
	}
}

// RunMetaInstallPipeline stands up a real stub-auth CommitPath bound to
// the ops.meta lane and starts consuming, so InstallPackage /
// UninstallPackage ops submitted by the installer are processed through
// the full pipeline (real DDL script, step-6 validation, step-8 atomic
// commit; only auth is stubbed). Returns a stop function the caller must
// defer.
//
// Use this directly when a test submits install/uninstall ops outside of
// InstallPhase1Packages (e.g. an uninstall-then-reinstall cycle).
func RunMetaInstallPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn) (stop func()) {
	t.Helper()
	logger := TestLogger()
	cp, _, err := processor.MakeStubPipeline(conn, HarnessCoreBucket, HarnessHealthBucket, processor.AuthModeStub, logger, "testutil-meta-install")
	if err != nil {
		t.Fatalf("RunMetaInstallPipeline: MakeStubPipeline: %v", err)
	}
	cons, err := processor.EnsureConsumer(ctx, conn.JetStream(), processor.ConsumerConfig{
		StreamName:     HarnessOpsStream,
		Durable:        "testutil-meta-install",
		FilterSubjects: []string{"ops.meta"},
		AckWait:        5 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("RunMetaInstallPipeline: EnsureConsumer: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	cc, err := cons.Consume(func(m jetstream.Msg) {
		cp.HandleMessage(runCtx, m)
	})
	if err != nil {
		cancel()
		t.Fatalf("RunMetaInstallPipeline: Consume: %v", err)
	}
	return func() {
		cc.Stop()
		cancel()
		// Delete the install durable so it cannot keep receiving ops.meta
		// messages a test publishes after install.
		_ = conn.JetStream().DeleteConsumer(context.Background(), HarnessOpsStream, "testutil-meta-install")
		// Purge the already-committed InstallPackage ops from the ops.meta
		// subject. They are durable in the stream but fully committed; left
		// in place, a meta-lane consumer a test later creates (DeliverAll)
		// would replay them ahead of the test's own op and surface them as
		// spurious "duplicate" outcomes.
		stream, err := conn.JetStream().Stream(context.Background(), HarnessOpsStream)
		if err == nil {
			_ = stream.Purge(context.Background(), jetstream.WithPurgeSubject("ops.meta"))
		}
	}
}
