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
	"github.com/operatinggraph/lattice/internal/loom"
	"github.com/operatinggraph/lattice/internal/processor"
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

// TestPlatformBuckets_MarkerTTLAtOrAboveTheServerFloor asserts every registry
// row's MarkerTTL is either zero (no per-key TTL support) or at least
// bootstrap.MinMarkerTTL. A sub-second marker TTL is refused by the server
// itself (nats-server/server/stream.go:1768-1771), so the row would not be a
// slow bucket but a bootstrap that cannot provision — every component then
// waits on a kernel that never comes up. Fail here, where the value is
// authored, instead.
func TestPlatformBuckets_MarkerTTLAtOrAboveTheServerFloor(t *testing.T) {
	for _, b := range bootstrap.PlatformBuckets() {
		if b.MarkerTTL != 0 && b.MarkerTTL < bootstrap.MinMarkerTTL {
			t.Errorf("bucket %q declares MarkerTTL %v, below the server floor %v — ProvisionBuckets could not create it",
				b.Name, b.MarkerTTL, bootstrap.MinMarkerTTL)
		}
	}
}

// TestLoomStateMarkerTTL_FitsInsideTheTrackerLifetime pins the invariant that
// bounds the loom-state delivery window from above:
//
//	maxDeadlineArm + bootstrap.LoomStateMarkerTTL < processor.TrackerTTL
//
// Loom's deadline probe decides rejected-or-lost from the ABSENCE of the op
// tracker, which the Processor writes at op submit with processor.TrackerTTL.
// A marker consumed after the tracker has aged out reads a healthy instance as
// failed. The arm is bounded by loom.MaxDeadlineArm — the ceiling withDefaults
// clamps both StepTimeout and CreateTaskTimeout down to — so the widest
// delivery any deployment can reach is that ceiling plus this window, and the
// two constants are asserted against each other here rather than each being
// believed on its own.
//
// The margin demanded is an order of magnitude, not a hair, because the
// failure this guards is silent: a window raised until it crosses the tracker
// turns the deadline probe's evidence-absence test from "the op was rejected"
// into "the op is simply old". Raising either constant past what this allows is
// a decision about that probe, and belongs with the structural fix
// loom-state-tombstone-sweep-design.md §11.2 files (a durable armed/disarmed
// fact on the instance record the probe can read) — not with a bigger constant.
func TestLoomStateMarkerTTL_FitsInsideTheTrackerLifetime(t *testing.T) {
	widestDelivery := loom.MaxDeadlineArm + bootstrap.LoomStateMarkerTTL
	require.Less(t, widestDelivery, processor.TrackerTTL,
		"a deadline marker delivered %v after the op outlives the tracker (%v) the probe reads as its evidence",
		widestDelivery, processor.TrackerTTL)
	require.Less(t, 10*widestDelivery, processor.TrackerTTL,
		"widest deadline delivery (%v) leaves less than 10x headroom inside the tracker lifetime (%v)",
		widestDelivery, processor.TrackerTTL)
}

// provisionedStreamConfigs runs ProvisionBuckets twice against a fresh
// embedded NATS and returns each registry bucket's backing stream config, so a
// live gate reads what the server actually holds after the idempotent
// re-provision rather than after a first create.
func provisionedStreamConfigs(t *testing.T) map[string]jetstream.StreamConfig {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)

	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err := bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, seeder.ProvisionBuckets(ctx), "first ProvisionBuckets must not error")
	require.NoError(t, seeder.ProvisionBuckets(ctx), "re-run ProvisionBuckets must not error")

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	out := make(map[string]jetstream.StreamConfig)
	for _, b := range bootstrap.PlatformBuckets() {
		stream, err := js.Stream(ctx, "KV_"+b.Name)
		require.NoError(t, err, "registry bucket %q must have a backing stream", b.Name)
		info, err := stream.Info(ctx)
		require.NoError(t, err)
		out[b.Name] = info.Config
	}
	return out
}

// TestProvisionBuckets_MarkerTTLReachesTheServer asserts each registry row's
// MarkerTTL is the marker lifetime the live stream carries, and that per-key
// TTL support is on exactly the rows that declare one. The registry is where
// the value is decided, but the window a consumer actually gets is the
// server's SubjectDeleteMarkerTTL: this is what keeps the two the same value,
// including after the idempotent re-provision that runs on every boot against
// an existing deployment.
func TestProvisionBuckets_MarkerTTLReachesTheServer(t *testing.T) {
	if testing.Short() {
		t.Skip("requires embedded NATS")
	}
	cfgs := provisionedStreamConfigs(t)
	for _, b := range bootstrap.PlatformBuckets() {
		cfg := cfgs[b.Name]
		require.Equal(t, b.MarkerTTL, cfg.SubjectDeleteMarkerTTL,
			"bucket %q: the live marker lifetime must be the registry's MarkerTTL", b.Name)
		require.Equal(t, b.MarkerTTL > 0, cfg.AllowMsgTTL,
			"bucket %q: per-key TTL support must be on exactly the rows declaring a MarkerTTL", b.Name)
	}
}

// TestProvisionBuckets_HistoryIsOneOnMarkerTTLBuckets asserts every bucket with
// a marker TTL keeps MaxMsgsPerSubject == 1 (KV History 1, the nats.go
// default — nats.go/jetstream/kv.go:619-625, 672).
//
// The server raises any per-message TTL below the stream's marker TTL up to
// the marker TTL, EXCEPT when MaxMsgsPer == 1
// (nats-server/server/stream.go:6890-6897). Every per-key TTL in the platform
// rides on that exception. Set History: 2 on loom-state and its 60s step
// deadline silently becomes an hour — a step's deadline would then fire an
// hour after the step, long past the completion it was meant to police — and
// the tombstone sweep's short TTL'd purges would linger just as long.
func TestProvisionBuckets_HistoryIsOneOnMarkerTTLBuckets(t *testing.T) {
	if testing.Short() {
		t.Skip("requires embedded NATS")
	}
	cfgs := provisionedStreamConfigs(t)
	for _, b := range bootstrap.PlatformBuckets() {
		if b.MarkerTTL == 0 {
			continue
		}
		require.EqualValues(t, 1, cfgs[b.Name].MaxMsgsPerSubject,
			"bucket %q carries a %v marker TTL, so MaxMsgsPerSubject must stay 1: above 1 the server raises every "+
				"shorter per-key TTL in the bucket to the marker TTL", b.Name, b.MarkerTTL)
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
