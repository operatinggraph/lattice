package bootstrap_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

// TestReservedBuckets_ExactlyNonLensTargetRows asserts ReservedBuckets() is
// exactly the !LensTarget rows of PlatformBuckets() — no bucket is missing
// (the credential-bindings hole the registry was built to close) and no
// LensTarget bucket is wrongly reserved (which would break the shared
// platform-projection lenses, e.g. weaver-targets).
func TestReservedBuckets_ExactlyNonLensTargetRows(t *testing.T) {
	reserved := bootstrap.ReservedBuckets()
	for _, b := range bootstrap.PlatformBuckets() {
		_, isReserved := reserved[b.Name]
		if b.LensTarget && isReserved {
			t.Errorf("bucket %q is LensTarget but ReservedBuckets() reserves it", b.Name)
		}
		if !b.LensTarget && !isReserved {
			t.Errorf("bucket %q is !LensTarget but ReservedBuckets() does not reserve it", b.Name)
		}
	}
	if got, want := len(reserved), len(bootstrap.PlatformBuckets())-3; got != want {
		// 3 LensTarget rows today: capability-kv, weaver-targets, orchestration-history.
		t.Errorf("ReservedBuckets() len = %d, want %d (PlatformBuckets minus the 3 LensTarget rows)", got, want)
	}
}

// TestPlatformBuckets_OwnerOrSharedWrite asserts every registry row declares
// exactly one of Owner or SharedWrite — the zero-value (neither set) would
// deny every matrix component publish to a bucket nothing can ever write,
// silently bricking whichever component was meant to own it.
func TestPlatformBuckets_OwnerOrSharedWrite(t *testing.T) {
	for _, b := range bootstrap.PlatformBuckets() {
		if b.Owner == "" && !b.SharedWrite {
			t.Errorf("bucket %q declares neither Owner nor SharedWrite — no component could ever write it", b.Name)
		}
		if b.Owner != "" && b.SharedWrite {
			t.Errorf("bucket %q declares both Owner %q and SharedWrite — ambiguous", b.Name, b.Owner)
		}
	}
}

// TestProvisionBuckets_ProvisionsExactlyTheRegistry asserts ProvisionBuckets
// creates exactly the buckets bootstrap.PlatformBuckets() names — the
// registry-to-provisioning parity the design (§6) requires: an unregistered
// bucket must never be provisioned, and a registered one must never be
// missed.
func TestProvisionBuckets_ProvisionsExactlyTheRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("requires embedded NATS")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)

	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err := bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, seeder.ProvisionBuckets(ctx))

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	for _, b := range bootstrap.PlatformBuckets() {
		kv, err := js.KeyValue(ctx, b.Name)
		require.NoError(t, err, "registry bucket %q must be provisioned", b.Name)
		require.Equal(t, b.Name, kv.Bucket())
	}
}
