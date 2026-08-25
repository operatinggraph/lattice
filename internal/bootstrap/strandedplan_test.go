package bootstrap

// Tests for how the stranded-epoch census travels through the reconcile plan.
// The subject is posture, not arithmetic: an advisory scan must never be able
// to fail a boot or a built-set comparison, and the whole item depends on that
// holding, because VerifyKernel's failures slice drives `make up`'s freshness
// oracle.

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// stubStrandedScan replaces the census planReconcile runs for one test.
func stubStrandedScan(t *testing.T, fn func(context.Context, jetstream.KeyValue) ([]StrandedOperatorEpoch, error)) {
	t.Helper()
	prev := strandedScan
	t.Cleanup(func() { strandedScan = prev })
	strandedScan = fn
}

// TestPlanReconcile_StrandedScanFailureNeverReachesTheReturnedError pins
// dossier entry 1 at the only place it can actually be broken. A projection
// assertion cannot see this: rewriting planReconcile's last lines to
// `return plan, strandedErr` turns every boot on a stranded bucket into a
// failed boot while every report-shape test still passes.
func TestPlanReconcile_StrandedScanFailureNeverReachesTheReturnedError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	seeder, kv := newReconcileSeeder(ctx, t)

	_, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	boom := errors.New("stranded scan could not run")
	stubStrandedScan(t, func(context.Context, jetstream.KeyValue) ([]StrandedOperatorEpoch, error) {
		return nil, boom
	})

	plan, err := planReconcile(ctx, kv)
	require.NoError(t, err, "an advisory scan's failure must not fail the plan")
	require.Equal(t, boom, plan.strandedScanErr, "it must be carried instead")

	report, err := ReadKernelReport(ctx, kv)
	require.NoError(t, err, "nor the report every consumer depends on")
	require.Equal(t, boom, report.StrandedScanErr)
	require.Empty(t, report.Missing, "the built-set comparison must still deliver its own answer")
	require.Empty(t, report.Stale)

	res, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err, "nor a boot")
	require.False(t, res.Changed(), "a converged kernel still writes nothing")
}

// TestReconcilePrimordial_StrandedEpochIsAdvisoryNotFatal pins §4's headline
// promise — "Boot: advisory, always" — which is the promise most worth a pin
// given what a failure costs downstream. A boot cannot fix this condition, and
// a boot that refused to come up over it would take the deployment down without
// moving it any closer to repaired.
func TestReconcilePrimordial_StrandedEpochIsAdvisoryNotFatal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	seeder, kv := newReconcileSeeder(ctx, t)

	first, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)
	require.True(t, first.Changed())

	priorID := newNanoID(t)
	seedRole(ctx, t, kv, priorID, "operator")
	seedHolder(ctx, t, kv, newNanoID(t), priorID, false)
	seedGrant(ctx, t, kv, priorID, false)

	report, err := ReadKernelReport(ctx, kv)
	require.NoError(t, err)
	require.Len(t, report.StrandedOperatorEpochs, 1, "fixture sanity: the scan must see it")
	require.Equal(t, StrandedSeverityLiveAuthority, report.StrandedOperatorEpochs[0].Severity(),
		"fixture sanity: at the rank that fails verify-kernel")

	second, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err, "a stranded epoch must never fail a boot")
	require.False(t, second.Changed(), "and must not provoke a write")
	require.Equal(t, 0, second.Created)
	require.Equal(t, 0, second.Updated)
	require.Equal(t, 0, second.Retained)
	require.Equal(t, first.Created, second.Unchanged, "the kernel comparison is untouched by the finding")
}

// TestReconcilePrimordial_StrandedScanFailureIsAdvisoryNotFatal is the same
// promise for the scan's own failure.
func TestReconcilePrimordial_StrandedScanFailureIsAdvisoryNotFatal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	seeder, _ := newReconcileSeeder(ctx, t)

	stubStrandedScan(t, func(context.Context, jetstream.KeyValue) ([]StrandedOperatorEpoch, error) {
		return nil, errors.New("stranded scan could not run")
	})

	res, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err, "a failed advisory scan must not fail the seed pass")
	require.True(t, res.Changed(), "and must not suppress the kernel repair it rides along with")
}
