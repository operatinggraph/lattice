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

// seedHolder writes one `identity holdsRole role` edge.
func seedHolder(ctx context.Context, t *testing.T, kv jetstream.KeyValue, roleID string, deleted bool) {
	t.Helper()
	identityID := newNanoID(t)
	identityKey := substrate.VertexKey("identity", identityID)
	linkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", roleID)
	val, err := MakeLinkEnvelope(linkKey, identityKey, substrate.VertexKey("role", roleID),
		"holdsRole", "link.holdsRole", nil)
	putDoc(ctx, t, kv, linkKey, val, err, deleted)
}

// seedCurrentEpoch plants this deployment's own operator role exactly as
// primordial.go seeds it — vertex, canonicalName, one holder, one grant. Every
// test carries it, so a predicate that ever reported the LIVE kernel role
// (an unloaded id table, a broken id filter) reddens here rather than in one
// dedicated case.
func seedCurrentEpoch(ctx context.Context, t *testing.T, kv jetstream.KeyValue) {
	t.Helper()
	seedRole(ctx, t, kv, RoleOperatorID, "operator")
	seedHolder(ctx, t, kv, RoleOperatorID, false)
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
func TestStrandedOperatorEpochs_SingleEpochBucketIsSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, stranded, "the live kernel role must never be reported as stranded")
}

// TestStrandedOperatorEpochs_RotatedIdFileStrandsPriorRole is the positive
// vector every negative below is written against: a regenerated id file leaves
// the prior epoch's operator role live, granted, and held by nobody.
func TestStrandedOperatorEpochs_RotatedIdFileStrandsPriorRole(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	priorID := newNanoID(t)
	priorKey := seedRole(ctx, t, kv, priorID, "operator")
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
	require.True(t, stranded[0].Protected, "the report carries data.protected as corroboration")
}

// TestStrandedOperatorEpochs_HeldRoleIsNeverStranded pins the strand test
// itself: a held role is somebody's live role whatever it is named, so one
// live holdsRole edge ends the candidate silently.
func TestStrandedOperatorEpochs_HeldRoleIsNeverStranded(t *testing.T) {
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
	seedHolder(ctx, t, kv, priorID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, stranded, "a role with a live holder is not stranded")
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
