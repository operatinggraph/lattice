package bootstrap

// Tests for StrandedOperatorEpochs — the cross-epoch orphan detector
// (primordial-epoch-stranded-authority-design.md §6.1-6.5). They live
// in-package because the predicate is keyed on the RoleOperatorID package
// variable, which the unloaded-table case has to be able to clear.
//
// Every negative here is written against the §6.2 positive: the same fixture,
// one guard's precondition changed, expecting silence.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// newNanoID returns a fresh Contract #1 NanoID.
func newNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}

// putDoc writes a built envelope at key, optionally soft-tombstoned. The
// tombstone is applied to the marshalled envelope rather than hand-building
// one, so the fixture cannot drift from the real envelope shape.
func putDoc(ctx context.Context, t *testing.T, kv jetstream.KeyValue, key string, raw []byte, err error, deleted bool) {
	t.Helper()
	require.NoError(t, err)
	if deleted {
		var env map[string]any
		require.NoError(t, json.Unmarshal(raw, &env))
		env["isDeleted"] = true
		raw, err = json.Marshal(env)
		require.NoError(t, err)
	}
	_, putErr := kv.Put(ctx, key, raw)
	require.NoError(t, putErr)
}

// seedRole writes a role vertex plus its canonicalName aspect.
func seedRole(ctx context.Context, t *testing.T, kv jetstream.KeyValue, roleID, canonicalName string) string {
	t.Helper()
	return seedRoleWithState(ctx, t, kv, roleID, canonicalName, false, false)
}

// seedRoleWithState writes a role vertex plus its canonicalName aspect, with
// independent control over each one's tombstone.
func seedRoleWithState(ctx context.Context, t *testing.T, kv jetstream.KeyValue, roleID, canonicalName string, roleDeleted, aspectDeleted bool) string {
	t.Helper()
	roleKey := substrate.VertexKey("role", roleID)
	val, err := MakeVertexEnvelope(roleKey, "role", map[string]any{"protected": true})
	putDoc(ctx, t, kv, roleKey, val, err, roleDeleted)

	cnKey := substrate.AspectKey(roleKey, "canonicalName")
	cnVal, cnErr := MakeAspectEnvelope(cnKey, roleKey, "canonicalName", "canonicalName",
		map[string]any{"value": canonicalName})
	putDoc(ctx, t, kv, cnKey, cnVal, cnErr, aspectDeleted)
	return roleKey
}

// seedGrant writes one `permission grantedBy role` edge and returns the
// permission vertex key it grants.
func seedGrant(ctx context.Context, t *testing.T, kv jetstream.KeyValue, roleID string, deleted bool) string {
	t.Helper()
	permID := newNanoID(t)
	permKey := substrate.VertexKey("permission", permID)
	linkKey := substrate.LinkKey("permission", permID, "grantedBy", "role", roleID)
	val, err := MakeLinkEnvelope(linkKey, permKey, substrate.VertexKey("role", roleID),
		"grantedBy", "link.grantedBy", nil)
	putDoc(ctx, t, kv, linkKey, val, err, deleted)
	return permKey
}

// seedHolder writes one `identity holdsRole role` edge and returns the holding
// identity's vertex key.
func seedHolder(ctx context.Context, t *testing.T, kv jetstream.KeyValue, identityID, roleID string, deleted bool) string {
	t.Helper()
	identityKey := substrate.VertexKey("identity", identityID)
	linkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", roleID)
	val, err := MakeLinkEnvelope(linkKey, identityKey, substrate.VertexKey("role", roleID),
		"holdsRole", "link.holdsRole", nil)
	putDoc(ctx, t, kv, linkKey, val, err, deleted)
	return identityKey
}

// seedCurrentEpoch plants this deployment's own operator role exactly as
// primordial.go seeds it — vertex, canonicalName, one holder, one grant. Every
// test carries it, so a predicate that ever reported the LIVE kernel role
// (an unloaded id table, a broken id filter) reddens here rather than in one
// dedicated case.
func seedCurrentEpoch(ctx context.Context, t *testing.T, kv jetstream.KeyValue) {
	t.Helper()
	seedRole(ctx, t, kv, RoleOperatorID, "operator")
	seedHolder(ctx, t, kv, BootstrapIdentityID, RoleOperatorID, false)
	seedGrant(ctx, t, kv, RoleOperatorID, false)
}

func strandedTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestStrandedOperatorEpochs_SingleEpochBucketIsSilent is the no-false-red pin
// for CI: `stack-gates` runs verify-kernel against a container that generated
// its id file and seeded an empty bucket in the same job, so the bucket holds
// exactly one operator role and it is the current one. That deployment must
// scan silent.
//
// The second case is what pins the id filter specifically. In the first, the
// current role has a holder, so the strand test alone would keep it silent
// even if the id comparison were wrong; strip the holders and the id filter is
// the ONLY thing standing between the live kernel role and a report of the
// deployment's own authority as stranded.
func TestStrandedOperatorEpochs_SingleEpochBucketIsSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	t.Run("current_role_held", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)

		seedCurrentEpoch(ctx, t, kv)

		stranded, err := StrandedOperatorEpochs(ctx, kv)
		require.NoError(t, err)
		require.Empty(t, stranded, "the live kernel role must never be reported as stranded")
	})

	t.Run("current_role_with_every_holder_revoked", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)

		seedRole(ctx, t, kv, RoleOperatorID, "operator")
		seedHolder(ctx, t, kv, BootstrapIdentityID, RoleOperatorID, true)
		seedGrant(ctx, t, kv, RoleOperatorID, false)

		stranded, err := StrandedOperatorEpochs(ctx, kv)
		require.NoError(t, err)
		require.Empty(t, stranded,
			"the current epoch's role is this deployment's own authority whether or not anyone holds it")
	})
}

// TestStrandedOperatorEpochs_RotatedIdFileStrandsPriorRole is the positive
// vector every negative below is written against, and its fixture is the REAL
// post-rotation state of a bucket that was never wiped.
//
// A re-bootstrap on a regenerated id file takes the full create-only seed path
// (DecideReseed returns true on a freshly generated file, primordial.go:262)
// and deletes nothing: reconcile.go:155 classifies every non-vtx.meta.* entry
// as retained, "deliberately left alone". So the prior epoch's admin identity,
// its service actors, and their holdsRole edges into the prior operator role
// are ALL still live. The whole epoch strands together — which is why a live
// holder is not, on its own, evidence that the role is reachable. The holder
// below is part of the island, and the role must still be reported.
func TestStrandedOperatorEpochs_RotatedIdFileStrandsPriorRole(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	priorID := newNanoID(t)
	priorKey := seedRole(ctx, t, kv, priorID, "operator")
	priorAdmin := seedHolder(ctx, t, kv, newNanoID(t), priorID, false)
	wantGrants := []string{
		seedGrant(ctx, t, kv, priorID, false),
		seedGrant(ctx, t, kv, priorID, false),
		seedGrant(ctx, t, kv, priorID, false),
	}

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1, "exactly the prior epoch's role must be reported")
	require.Equal(t, priorKey, stranded[0].RoleKey)
	require.ElementsMatch(t, wantGrants, stranded[0].GrantedBy)
	require.Equal(t, sortedUnique(wantGrants), stranded[0].GrantedBy, "grants must be reported sorted")
	_ = priorAdmin
	require.True(t, stranded[0].Protected, "the report carries data.protected as corroboration")
}

// TestStrandedOperatorEpochs_CurrentEpochHolderSuppresses pins the reachability
// test: an identity of the CURRENT primordial epoch holding a non-current
// `operator` role really can reach it, so the role is live topology and the
// scan says nothing. This is the only holder relationship that suppresses.
func TestStrandedOperatorEpochs_CurrentEpochHolderSuppresses(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	priorID := newNanoID(t)
	seedRole(ctx, t, kv, priorID, "operator")
	seedGrant(ctx, t, kv, priorID, false)
	seedGrant(ctx, t, kv, priorID, false)
	seedHolder(ctx, t, kv, BootstrapIdentityID, priorID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, stranded, "a role a current-epoch identity holds is genuinely reachable")
}

// TestStrandedOperatorEpochs_PriorEpochHolderDoesNotSuppress is the focused
// negative against the suppressor above, and the reason the holder check cannot
// be existential. The fixture differs from _CurrentEpochHolderSuppresses in one
// respect — the holding identity is outside the current epoch's primordial set —
// and that single difference must flip silence into a report, because a
// prior-epoch identity is itself stranded and reaches nothing.
func TestStrandedOperatorEpochs_PriorEpochHolderDoesNotSuppress(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	priorID := newNanoID(t)
	priorKey := seedRole(ctx, t, kv, priorID, "operator")
	seedGrant(ctx, t, kv, priorID, false)
	seedGrant(ctx, t, kv, priorID, false)
	foreignHolder := seedHolder(ctx, t, kv, newNanoID(t), priorID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1, "a holder outside the current epoch is part of the island, not reachability")
	require.Equal(t, priorKey, stranded[0].RoleKey)
	require.Equal(t, []string{foreignHolder}, stranded[0].Holders)
	require.Len(t, stranded[0].GrantedBy, 2)
}

// TestStrandedOperatorEpochs_TombstonedHolderDoesNotSuppress keeps the
// suppressor honest about liveness: a revoked grant of the role to a
// current-epoch identity is not reachability either.
func TestStrandedOperatorEpochs_TombstonedHolderDoesNotSuppress(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	priorID := newNanoID(t)
	priorKey := seedRole(ctx, t, kv, priorID, "operator")
	seedGrant(ctx, t, kv, priorID, false)
	seedHolder(ctx, t, kv, LoomIdentityID, priorID, true)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1, "a tombstoned holdsRole edge confers no reachability")
	require.Equal(t, priorKey, stranded[0].RoleKey)
	require.Empty(t, stranded[0].Holders, "a revoked holder is not a live holder")
}

// TestStrandedOperatorEpochs_TombstonedRoleAndTombstonedEdgesAreSilent covers
// the three tombstone positions: a soft-deleted role vertex and a soft-deleted
// canonicalName aspect are already-retired residue with nothing to report,
// while a live role whose every grant is revoked drops to zero grants — the
// notice class of §4, not a failure and not silence.
func TestStrandedOperatorEpochs_TombstonedRoleAndTombstonedEdgesAreSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	t.Run("tombstoned_role_vertex", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)
		seedCurrentEpoch(ctx, t, kv)

		priorID := newNanoID(t)
		seedRoleWithState(ctx, t, kv, priorID, "operator", true, false)
		seedGrant(ctx, t, kv, priorID, false)

		stranded, err := StrandedOperatorEpochs(ctx, kv)
		require.NoError(t, err)
		require.Empty(t, stranded, "an already-tombstoned role is residue, not a report")
	})

	t.Run("tombstoned_canonicalName_aspect", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)
		seedCurrentEpoch(ctx, t, kv)

		priorID := newNanoID(t)
		seedRoleWithState(ctx, t, kv, priorID, "operator", false, true)
		seedGrant(ctx, t, kv, priorID, false)

		stranded, err := StrandedOperatorEpochs(ctx, kv)
		require.NoError(t, err)
		require.Empty(t, stranded, "a role whose canonicalName is tombstoned no longer names the class")
	})

	t.Run("all_grants_tombstoned_reports_zero_grants", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)
		seedCurrentEpoch(ctx, t, kv)

		priorID := newNanoID(t)
		priorKey := seedRole(ctx, t, kv, priorID, "operator")
		seedGrant(ctx, t, kv, priorID, true)
		seedGrant(ctx, t, kv, priorID, true)

		stranded, err := StrandedOperatorEpochs(ctx, kv)
		require.NoError(t, err)
		require.Len(t, stranded, 1)
		require.Equal(t, priorKey, stranded[0].RoleKey)
		require.Empty(t, stranded[0].GrantedBy, "a revoked grant is not live authority")
	})
}

// TestStrandedOperatorEpochs_ForeignRoleNameIsSilent pins the canonicalName
// equality: a holderless, granted role that is not an `operator` role is
// ordinary topology whose lifecycle belongs to operations.
func TestStrandedOperatorEpochs_ForeignRoleNameIsSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	foreignID := newNanoID(t)
	seedRole(ctx, t, kv, foreignID, "loftspaceStaff")
	seedGrant(ctx, t, kv, foreignID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, stranded, "only an `operator` role names this class")
}

// TestStrandedOperatorEpochs_UnloadedPrimordialTableRefuses pins the sharpest
// hazard: the predicate excludes the current role by id, so an unloaded table
// (empty string) matches every role and would report the LIVE kernel role as
// stranded. The refusal must land before the graph is touched at all — the nil
// bucket below is what proves "before".
func TestStrandedOperatorEpochs_UnloadedPrimordialTableRefuses(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)
	seedCurrentEpoch(ctx, t, kv)

	loaded := RoleOperatorID
	t.Cleanup(func() { RoleOperatorID = loaded })
	RoleOperatorID = ""

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.ErrorIs(t, err, ErrPrimordialIDsUnloaded)
	require.Empty(t, stranded)

	strandedNilKV, nilErr := StrandedOperatorEpochs(ctx, nil)
	require.ErrorIs(t, nilErr, ErrPrimordialIDsUnloaded, "the refusal must precede every read")
	require.Empty(t, strandedNilKV)
}

// TestReadKernelReport_StrandedScanErrNeverFailsTheBuiltSetComparison pins the
// posture the whole surfacing rests on: the stranded scan is advisory, so its
// failure travels in the report and never in ReadKernelReport's returned error.
// Every consumer of the built-set comparison — KernelDrift, VerifyKernel,
// ReconcilePrimordial's repair path — depends on getting its own answer whether
// or not an unrelated advisory scan could run.
//
// The failure is injected at the plan→report projection rather than through a
// live bucket, because neither of the scan's two failure modes is reachable
// end-to-end here. An unloaded primordial table never gets as far as the scan:
// buildPrimordialEntries, which planReconcile calls first, refuses an empty
// NanoID outright. A failed listing is a substrate condition with no
// deterministic trigger — and inducing one by breaking the bucket would break
// the built-set comparison in the same stroke, which is precisely the confusion
// this test exists to rule out. The live half below covers the succeeding path.
func TestReadKernelReport_StrandedScanErrNeverFailsTheBuiltSetComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	t.Run("failed_scan_leaves_the_comparison_intact", func(t *testing.T) {
		missingKey := "vtx.meta.aaaaaaaaaaaaaaaaaaaa"
		staleKey := "vtx.meta.bbbbbbbbbbbbbbbbbbbb.script"
		scanErr := errors.New(`list "vtx.role.*.canonicalName": interrupted (partial result discarded)`)
		plan := reconcilePlan{
			missing:         []reconcileStep{{key: missingKey}},
			stale:           []reconcileStep{{key: staleKey}},
			strandedScanErr: scanErr,
		}

		report := kernelReportFromPlan(plan)
		require.Equal(t, scanErr, report.StrandedScanErr, "the scan's failure belongs in its own field")
		require.Empty(t, report.StrandedOperatorEpochs, "a failed scan reports nothing")
		require.Equal(t, []string{missingKey}, report.Missing,
			"the built-set comparison must survive an advisory scan's failure")
		require.Equal(t, []string{staleKey}, report.Stale)
	})

	t.Run("succeeding_scan_reports_alongside_a_stale_kernel", func(t *testing.T) {
		ctx := strandedTestContext(t)
		seeder, kv := newReconcileSeeder(ctx, t)

		_, err := seeder.ReconcilePrimordial(ctx)
		require.NoError(t, err)

		staleKey := upgradeScriptKey()
		staleVal, staleErr := MakeAspectEnvelope(staleKey, UpgradePackageDDLKey, "script", "script",
			map[string]any{"source": "def execute(state, op):\n    fail(\"an older binary\")\n"})
		require.NoError(t, staleErr)
		_, err = kv.Put(ctx, staleKey, staleVal)
		require.NoError(t, err)

		priorID := newNanoID(t)
		priorKey := seedRole(ctx, t, kv, priorID, "operator")
		seedGrant(ctx, t, kv, priorID, false)

		report, err := ReadKernelReport(ctx, kv)
		require.NoError(t, err)
		require.NoError(t, report.StrandedScanErr)
		require.Contains(t, report.Stale, staleKey,
			"the built-set comparison and the stranded scan are answered from one plan")
		require.Len(t, report.StrandedOperatorEpochs, 1)
		require.Equal(t, priorKey, report.StrandedOperatorEpochs[0].RoleKey)
	})
}
