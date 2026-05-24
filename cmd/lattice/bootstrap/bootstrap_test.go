package bootstrap

import (
	"context"
	"testing"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
)

// TestBootstrapVerify_HappyPath verifies that VerifyKernel returns no failures
// when the kernel has been properly seeded.
func TestBootstrapVerify_HappyPath(t *testing.T) {
	ctx, conn := setupBootstrapEnv(t)

	// SetupPackageTestEnv seeds the full kernel via bootstrap.SeedPrimordial.
	// Verify that all assertions pass after seeding.
	failures := bootstrap.VerifyKernel(ctx, conn)
	if len(failures) > 0 {
		t.Fatalf("expected no verification failures after full bootstrap, got:\n%v", failures)
	}
}

func setupBootstrapEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	// SetupPackageTestEnv provisions buckets + seeds kernel + installs packages.
	ctx, conn := testutil.SetupPackageTestEnv(t)

	// Provision the full bucket + stream surface that VerifyKernel checks.
	// SetupPackageTestEnv's ProvisionHarness only creates the three buckets
	// needed by the Processor (core-kv, health-kv, capability-kv). VerifyKernel
	// also requires weaver-state, weaver-claims, refractor-adjacency, and the
	// core-events stream. The full seeder's ProvisionBuckets call is idempotent.
	seeder, err := bootstrap.NewSeeder(conn.NATS(), testutil.TestLogger())
	if err != nil {
		t.Fatalf("NewSeeder: %v", err)
	}
	if err := seeder.ProvisionBuckets(ctx); err != nil {
		t.Fatalf("ProvisionBuckets: %v", err)
	}
	if err := bootstrap.MarkBootstrapComplete(ctx, conn.NATS(), testutil.TestLogger()); err != nil {
		t.Fatalf("MarkBootstrapComplete: %v", err)
	}
	return ctx, conn
}
