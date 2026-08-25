package bootstrap

// Tests for StrandedOperatorEpochs — the detector for live roles named
// `operator` that this deployment's primordial table does not name
// (primordial-epoch-stranded-authority-design.md §6). They live in-package
// because the predicate is keyed on the primordial ID globals, which the
// unloaded-table case has to be able to clear, and because the plan-level
// posture tests stub the unexported strandedScan seam.
//
// Every negative here is written against the positive vector in
// _RotatedIdFileStrandsPriorRole: the same fixture, one guard's precondition
// changed.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
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

// seedRole writes a live role vertex plus its canonicalName aspect.
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

// seedGrant writes a permission vertex and its `permission grantedBy role`
// edge, returning the permission vertex key. Both ends exist because the scan
// requires both to be live before it counts a grant.
func seedGrant(ctx context.Context, t *testing.T, kv jetstream.KeyValue, roleID string, edgeDeleted bool) string {
	t.Helper()
	return seedGrantWithState(ctx, t, kv, roleID, false, edgeDeleted)
}

func seedGrantWithState(ctx context.Context, t *testing.T, kv jetstream.KeyValue, roleID string, permDeleted, edgeDeleted bool) string {
	t.Helper()
	permID := newNanoID(t)
	permKey := substrate.VertexKey("permission", permID)
	permVal, permErr := MakeVertexEnvelope(permKey, "permission", map[string]any{"operationType": "AttachObject"})
	putDoc(ctx, t, kv, permKey, permVal, permErr, permDeleted)

	linkKey := substrate.LinkKey("permission", permID, "grantedBy", "role", roleID)
	val, err := MakeLinkEnvelope(linkKey, permKey, substrate.VertexKey("role", roleID),
		"grantedBy", "link.grantedBy", nil)
	putDoc(ctx, t, kv, linkKey, val, err, edgeDeleted)
	return permKey
}

// seedHolder writes an identity vertex and its `identity holdsRole role` edge,
// returning the identity's vertex key.
func seedHolder(ctx context.Context, t *testing.T, kv jetstream.KeyValue, identityID, roleID string, edgeDeleted bool) string {
	t.Helper()
	return seedHolderWithState(ctx, t, kv, identityID, roleID, false, edgeDeleted)
}

func seedHolderWithState(ctx context.Context, t *testing.T, kv jetstream.KeyValue, identityID, roleID string, identityDeleted, edgeDeleted bool) string {
	t.Helper()
	identityKey := substrate.VertexKey("identity", identityID)
	idVal, idErr := MakeVertexEnvelope(identityKey, "identity", nil)
	putDoc(ctx, t, kv, identityKey, idVal, idErr, identityDeleted)

	linkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", roleID)
	val, err := MakeLinkEnvelope(linkKey, identityKey, substrate.VertexKey("role", roleID),
		"holdsRole", "link.holdsRole", nil)
	putDoc(ctx, t, kv, linkKey, val, err, edgeDeleted)
	return identityKey
}

// holdsRoleKey is the link key seedHolder writes.
func holdsRoleKey(identityKey, roleID string) string {
	_, identityID, _ := substrate.ParseVertexKey(identityKey)
	return substrate.LinkKey("identity", identityID, "holdsRole", "role", roleID)
}

// seedCurrentEpoch plants this deployment's own operator role exactly as
// primordial.go seeds it: vertex, canonicalName, one grant, and a holdsRole
// edge from each of the six primordial identities that Contract #7 §7.2 gives
// the role to (primordial.go:800-809; the Gateway deliberately gets none).
//
// Seeding all six matters beyond realism — the accounted-for set is read from
// these very edges, so a fixture that seeded only the admin would leave every
// service actor looking unaccounted-for and quietly make the wrong tests pass.
func seedCurrentEpoch(ctx context.Context, t *testing.T, kv jetstream.KeyValue) {
	t.Helper()
	seedRole(ctx, t, kv, RoleOperatorID, "operator")
	for _, id := range []string{
		BootstrapIdentityID, LoomIdentityID, WeaverIdentityID,
		BridgeIdentityID, ObjmgrIdentityID, PrivacyIdentityID,
	} {
		seedHolder(ctx, t, kv, id, RoleOperatorID, false)
	}
	seedGrant(ctx, t, kv, RoleOperatorID, false)
}

func strandedTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// sortedCopy sorts a copy of keys using the standard library, so an assertion
// never checks production output against the production helper that produced
// its ordering.
func sortedCopy(keys []string) []string {
	out := append([]string(nil), keys...)
	sort.Strings(out)
	return out
}

// TestStrandedOperatorEpochs_SingleEpochBucketIsSilent is the no-false-red pin
// for CI: `stack-gates` runs verify-kernel against a container that generated
// its id file and seeded an empty bucket in the same job, so the bucket holds
// exactly one operator role and it is the current one.
//
// The second case pins the id filter specifically. In the first, the current
// role has a holder from the primordial table, so the reachability rule alone
// would classify it harmlessly even if the id comparison were wrong; strip the
// holders and the id filter is the ONLY thing keeping the deployment's own
// authority out of the report.
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
		require.Empty(t, stranded, "the live kernel role must never be reported")
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
// and deletes nothing: reconcile.go classifies every non-vtx.meta.* entry as
// retained, "deliberately left alone". So the prior epoch's admin identity, its
// service actors, and their holdsRole edges into the prior operator role are
// ALL still live. The whole epoch strands together, which is why a live holder
// is not on its own evidence that the role is reachable — and why this holder
// makes the finding severe rather than silencing it.
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
	require.Equal(t, sortedCopy(wantGrants), stranded[0].GrantedBy, "grants must be reported sorted")
	require.Equal(t, []string{priorAdmin}, stranded[0].Holders,
		"the prior epoch's own holder is part of the island and must be named, not treated as reachability")
	require.Empty(t, stranded[0].ReachableVia)
	require.Zero(t, stranded[0].UnreadableEdges)
	require.True(t, stranded[0].Protected, "the report carries data.protected as a repairability signal")
	require.Equal(t, StrandedSeverityLiveAuthority, stranded[0].Severity())
}

// TestStrandedOperatorEpochs_AccountedHolderWithGrantsIsLiveAuthority is the
// escalation an "already root, so nothing new" premise misses. rbac-domain's
// capabilityRolesSpec (packages/rbac-domain/lenses.go:91-104) matches
// `(identity)-[:holdsRole]->(role)<-[:grantedBy]-(perm)` with NO canonicalName
// and NO id filter, so a current-operator identity that also holds a stranded
// `operator` role gets every permission THAT role grants materialized into its
// cap.roles document — and those grants are by construction not a subset of the
// current role's; that non-overlap is the board row's whole premise.
//
// One unprotected AssignRole reaches this state, so it must fail the gate.
func TestStrandedOperatorEpochs_AccountedHolderWithGrantsIsLiveAuthority(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	priorID := newNanoID(t)
	priorKey := seedRole(ctx, t, kv, priorID, "operator")
	seedGrant(ctx, t, kv, priorID, false)
	holder := seedHolder(ctx, t, kv, BootstrapIdentityID, priorID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1)
	require.Equal(t, priorKey, stranded[0].RoleKey)
	require.Empty(t, stranded[0].Holders, "a verified current-operator identity is accounted for")
	require.Equal(t, []string{holdsRoleKey(holder, priorID)}, stranded[0].ReachableVia,
		"the report must name the edge")
	require.Len(t, stranded[0].GrantedBy, 1)
	require.Equal(t, StrandedSeverityLiveAuthority, stranded[0].Severity(),
		"grants reachable through cap.roles are live authority even for an accounted-for holder")
}

// TestStrandedOperatorEpochs_AccountedHolderWithoutGrantsIsNoAddedAuthority is
// the surviving benign case, and the only thing keeping the middle rank
// inhabited: a verified current-operator identity holding a stranded role that
// confers nothing. Both name-matching lenses hand such a holder exactly what
// its current role already does, and capabilityRolesSpec has no grantedBy edge
// to walk. Still reported — an ordinary AssignRole must not silence the check.
func TestStrandedOperatorEpochs_AccountedHolderWithoutGrantsIsNoAddedAuthority(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	priorID := newNanoID(t)
	priorKey := seedRole(ctx, t, kv, priorID, "operator")
	holder := seedHolder(ctx, t, kv, WeaverIdentityID, priorID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1, "a benign finding is demoted, never dropped")
	require.Equal(t, priorKey, stranded[0].RoleKey)
	require.Empty(t, stranded[0].Holders)
	require.Empty(t, stranded[0].GrantedBy)
	require.Equal(t, []string{holdsRoleKey(holder, priorID)}, stranded[0].ReachableVia)
	require.Equal(t, StrandedSeverityNoAddedAuthority, stranded[0].Severity())
}

// TestStrandedOperatorEpochs_PrimordialIdentityWithoutCurrentRoleIsNotAccounted
// pins that the accounted-for set is read from the GRAPH, not assumed from the
// id file. A primordial identity's holdsRole edge is an ordinary link and can
// be revoked like any other; one whose current-role edge is gone but which
// holds a stranded `operator` role is ACQUIRING root through the name-matching
// lenses, not retaining it, so it must rank as an unaccounted-for holder.
func TestStrandedOperatorEpochs_PrimordialIdentityWithoutCurrentRoleIsNotAccounted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)
	// Revoke Loom's edge into the CURRENT operator role, leaving the identity
	// itself live and the other five untouched.
	seedHolder(ctx, t, kv, LoomIdentityID, RoleOperatorID, true)

	priorID := newNanoID(t)
	seedRole(ctx, t, kv, priorID, "operator")
	loom := seedHolder(ctx, t, kv, LoomIdentityID, priorID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1)
	require.Equal(t, []string{loom}, stranded[0].Holders,
		"membership in the id table is not proof of holding the current role")
	require.Empty(t, stranded[0].ReachableVia)
	require.Equal(t, StrandedSeverityLiveAuthority, stranded[0].Severity())
}

// TestStrandedOperatorEpochs_GatewayHolderIsNeverAccounted pins Contract #7
// §7.2's one exception, in the state where it bites.
//
// Six of the seven primordial identities hold the operator role; the Gateway
// deliberately does not, being internet-facing and scoped narrow
// (primordial.go:492-502). The fixture gives it the CURRENT operator role
// anyway — the "fix" an operator might reach for — and then a stranded one. If
// the Gateway were a member of the primordial set, that current-role edge would
// launder it into the accounted-for census and demote the finding. It must not:
// the internet-facing actor holding an `operator`-named role is the most
// serious finding this scan can make, never evidence of health.
func TestStrandedOperatorEpochs_GatewayHolderIsNeverAccounted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)
	seedHolder(ctx, t, kv, GatewayIdentityID, RoleOperatorID, false)

	priorID := newNanoID(t)
	seedRole(ctx, t, kv, priorID, "operator")
	gateway := seedHolder(ctx, t, kv, GatewayIdentityID, priorID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1)
	require.Equal(t, []string{gateway}, stranded[0].Holders,
		"the Gateway can never account for a stranded role, even holding the current one")
	require.Empty(t, stranded[0].ReachableVia)
	require.Equal(t, StrandedSeverityLiveAuthority, stranded[0].Severity())
}

// TestStrandedOperatorEpochs_PriorEpochHolderDoesNotSuppress is the focused
// negative against reachability, and the reason the holder check cannot be
// existential. The fixture differs from the demotion case in one respect — the
// holding identity is outside the primordial table — and that single difference
// must flip the rank.
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
	foreignHolder := seedHolder(ctx, t, kv, newNanoID(t), priorID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1)
	require.Equal(t, priorKey, stranded[0].RoleKey)
	require.Equal(t, []string{foreignHolder}, stranded[0].Holders)
	require.Equal(t, StrandedSeverityLiveAuthority, stranded[0].Severity())
}

// TestStrandedOperatorEpochs_UnaccountedHolderOutranksAnAccountedOne pins the
// rank ordering: an accounted-for holder alongside an unaccounted-for one must
// not demote the finding, or the demotion path would be the silencer the
// classification was introduced to prevent.
func TestStrandedOperatorEpochs_UnaccountedHolderOutranksAnAccountedOne(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	priorID := newNanoID(t)
	seedRole(ctx, t, kv, priorID, "operator")
	foreignHolder := seedHolder(ctx, t, kv, newNanoID(t), priorID, false)
	seedHolder(ctx, t, kv, LoomIdentityID, priorID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Len(t, stranded, 1)
	require.Equal(t, []string{foreignHolder}, stranded[0].Holders)
	require.Len(t, stranded[0].ReachableVia, 1)
	require.Equal(t, StrandedSeverityLiveAuthority, stranded[0].Severity(),
		"an AssignRole to an accounted-for identity must not demote a live escalation")
}

// TestStrandedOperatorEpochs_DeadEndpointsAreNotLiveEdges covers the liveness
// of both ends of both link families. An edge is only authority if the document
// at each end is live: a revoked edge, a tombstoned permission, a tombstoned
// identity. The identity case matters most — it is the false-negative
// direction, where a dead service actor's surviving edge would otherwise demote
// a severe finding.
func TestStrandedOperatorEpochs_DeadEndpointsAreNotLiveEdges(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	t.Run("tombstoned_grant_edge", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)
		seedCurrentEpoch(ctx, t, kv)

		priorID := newNanoID(t)
		seedRole(ctx, t, kv, priorID, "operator")
		seedHolder(ctx, t, kv, newNanoID(t), priorID, false)
		seedGrant(ctx, t, kv, priorID, true)

		stranded, err := StrandedOperatorEpochs(ctx, kv)
		require.NoError(t, err)
		require.Len(t, stranded, 1)
		require.Empty(t, stranded[0].GrantedBy, "a revoked grant is not live authority")
	})

	t.Run("tombstoned_permission_vertex", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)
		seedCurrentEpoch(ctx, t, kv)

		priorID := newNanoID(t)
		seedRole(ctx, t, kv, priorID, "operator")
		seedHolder(ctx, t, kv, newNanoID(t), priorID, false)
		seedGrantWithState(ctx, t, kv, priorID, true, false)

		stranded, err := StrandedOperatorEpochs(ctx, kv)
		require.NoError(t, err)
		require.Len(t, stranded, 1)
		require.Empty(t, stranded[0].GrantedBy,
			"a live edge to a tombstoned permission confers nothing")
	})

	t.Run("tombstoned_holder_edge", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)
		seedCurrentEpoch(ctx, t, kv)

		priorID := newNanoID(t)
		seedRole(ctx, t, kv, priorID, "operator")
		seedGrant(ctx, t, kv, priorID, false)
		seedHolder(ctx, t, kv, newNanoID(t), priorID, true)

		stranded, err := StrandedOperatorEpochs(ctx, kv)
		require.NoError(t, err)
		require.Len(t, stranded, 1)
		require.Empty(t, stranded[0].Holders, "a revoked holdsRole edge confers no reachability")
		require.Equal(t, StrandedSeverityInert, stranded[0].Severity())
	})

	t.Run("tombstoned_current_epoch_identity_does_not_demote", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)
		seedCurrentEpoch(ctx, t, kv)

		priorID := newNanoID(t)
		seedRole(ctx, t, kv, priorID, "operator")
		foreignHolder := seedHolder(ctx, t, kv, newNanoID(t), priorID, false)
		seedHolderWithState(ctx, t, kv, LoomIdentityID, priorID, true, false)

		stranded, err := StrandedOperatorEpochs(ctx, kv)
		require.NoError(t, err)
		require.Len(t, stranded, 1)
		require.Empty(t, stranded[0].ReachableVia,
			"a tombstoned identity reaches nothing, so its surviving edge cannot demote")
		require.Equal(t, []string{foreignHolder}, stranded[0].Holders)
		require.Equal(t, StrandedSeverityLiveAuthority, stranded[0].Severity())
	})
}

// TestStrandedOperatorEpochs_TombstonedRoleIsSilentAndRevokedGrantsStillReport
// covers the tombstone positions on the ROLE itself, and the boundary between
// "nothing to report" and "report with nothing in it". A soft-deleted role
// vertex or canonicalName aspect is already-retired residue; a live role whose
// grants and holders are all revoked is still reported, at the inert rank.
func TestStrandedOperatorEpochs_TombstonedRoleIsSilentAndRevokedGrantsStillReport(t *testing.T) {
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

	t.Run("all_edges_revoked_reports_at_inert_rank", func(t *testing.T) {
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
		require.Empty(t, stranded[0].GrantedBy)
		require.Empty(t, stranded[0].Holders)
		require.Equal(t, StrandedSeverityInert, stranded[0].Severity())
	})
}

// TestStrandedOperatorEpochs_ForeignRoleNameIsSilent pins the canonicalName
// equality: a held, granted role that is not named `operator` is ordinary
// topology whose lifecycle belongs to operations.
func TestStrandedOperatorEpochs_ForeignRoleNameIsSilent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	foreignID := newNanoID(t)
	seedRole(ctx, t, kv, foreignID, "loftspaceStaff")
	seedHolder(ctx, t, kv, newNanoID(t), foreignID, false)
	seedGrant(ctx, t, kv, foreignID, false)

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, stranded, "only an `operator` role names this class")
}

// TestStrandedOperatorEpochs_ReportsEveryStrandedRoleSorted pins the ordering
// of the returned slice, which no single-role fixture can exercise.
func TestStrandedOperatorEpochs_ReportsEveryStrandedRoleSorted(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	var want []string
	for range 3 {
		id := newNanoID(t)
		want = append(want, seedRole(ctx, t, kv, id, "operator"))
		seedHolder(ctx, t, kv, newNanoID(t), id, false)
	}

	stranded, err := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, err)
	var got []string
	for _, s := range stranded {
		got = append(got, s.RoleKey)
	}
	require.Equal(t, sortedCopy(want), got, "findings must come back sorted by role key")
}

// getFailingKV fails Get for one key and delegates everything else, so a
// per-candidate read failure can be induced with no timing dependence.
type getFailingKV struct {
	jetstream.KeyValue
	failOn string
	err    error
}

func (k getFailingKV) Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error) {
	if key == k.failOn {
		return nil, k.err
	}
	return k.KeyValue.Get(ctx, key)
}

// TestStrandedOperatorEpochs_ReadFailureAbortsRatherThanReportingClean is the
// pin against the most dangerous failure this scan can have. Absorbing a read
// error as "not live" would let an expired context — scripts/verify-kernel.go
// runs the whole gate under a 15s budget — skip every candidate and return an
// empty, authoritative-looking all-clear over a bucket full of live authority.
// A key that is genuinely absent is still an answer, not a failure.
func TestStrandedOperatorEpochs_ReadFailureAbortsRatherThanReportingClean(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	t.Run("transient_read_error_propagates", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)
		seedCurrentEpoch(ctx, t, kv)

		priorID := newNanoID(t)
		priorKey := seedRole(ctx, t, kv, priorID, "operator")
		seedHolder(ctx, t, kv, newNanoID(t), priorID, false)

		boom := errors.New("nats: connection closed")
		stranded, err := StrandedOperatorEpochs(ctx, getFailingKV{KeyValue: kv, failOn: priorKey, err: boom})
		require.ErrorIs(t, err, boom, "a read that failed must not be reported as clean")
		require.Nil(t, stranded)
	})

	t.Run("cancelled_context_never_reports_clean", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)
		seedCurrentEpoch(ctx, t, kv)
		seedRole(ctx, t, kv, newNanoID(t), "operator")

		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		stranded, err := StrandedOperatorEpochs(cancelled, kv)
		require.Error(t, err)
		require.Nil(t, stranded)
	})

	t.Run("absent_key_is_an_answer_not_an_error", func(t *testing.T) {
		ctx := strandedTestContext(t)
		_, kv := newReconcileSeeder(ctx, t)
		seedCurrentEpoch(ctx, t, kv)

		priorID := newNanoID(t)
		seedRole(ctx, t, kv, priorID, "operator")
		// A holdsRole edge whose identity vertex was never written: the
		// endpoint read comes back ErrKeyNotFound, which establishes "not
		// live" rather than "could not tell".
		identityID := newNanoID(t)
		linkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", priorID)
		val, err := MakeLinkEnvelope(linkKey, substrate.VertexKey("identity", identityID),
			substrate.VertexKey("role", priorID), "holdsRole", "link.holdsRole", nil)
		putDoc(ctx, t, kv, linkKey, val, err, false)

		stranded, scanErr := StrandedOperatorEpochs(ctx, kv)
		require.NoError(t, scanErr)
		require.Len(t, stranded, 1)
		require.Empty(t, stranded[0].Holders)
		require.Zero(t, stranded[0].UnreadableEdges, "an absent endpoint is established, not unreadable")
	})
}

// TestStrandedOperatorEpochs_UnreadableEdgeIsCountedNotDropped pins the
// remaining silent-skip. An unparseable edge document cannot be classified, and
// every such skip can only shrink the holder list severity keys on — so it both
// marks the report a lower bound AND lifts the finding off the inert rank. A
// clean exit status over an unread fact is the failure mode this prevents.
func TestStrandedOperatorEpochs_UnreadableEdgeIsCountedNotDropped(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := strandedTestContext(t)
	_, kv := newReconcileSeeder(ctx, t)

	seedCurrentEpoch(ctx, t, kv)

	priorID := newNanoID(t)
	seedRole(ctx, t, kv, priorID, "operator")
	corruptEdge := substrate.LinkKey("identity", newNanoID(t), "holdsRole", "role", priorID)
	_, err := kv.Put(ctx, corruptEdge, []byte(`{"isDeleted":"not-a-bool"}`))
	require.NoError(t, err)

	stranded, scanErr := StrandedOperatorEpochs(ctx, kv)
	require.NoError(t, scanErr)
	require.Len(t, stranded, 1)
	require.Empty(t, stranded[0].Holders)
	require.Equal(t, 1, stranded[0].UnreadableEdges)
	require.Equal(t, StrandedSeverityNoAddedAuthority, stranded[0].Severity(),
		"an unclassifiable holdsRole edge might be a holder — unknown must not rank as inert")
	require.Contains(t, stranded[0].Report(), "LOWER BOUND",
		"and the line must say so rather than reading as an all-clear")
}

// TestStrandedOperatorEpochs_UnloadedPrimordialTableRefuses pins the sharpest
// hazard: both halves of the predicate are keyed on the primordial table, so an
// unloaded one would match every role AND empty the reachability set, reporting
// the live kernel role with its own holders counted as strangers. The refusal
// must land before the graph is touched — the nil bucket is what proves it.
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

// TestStrandedOperatorEpoch_SeverityAndReport covers every rank boundary
// without a bucket, and the property that made a bare count wrong: the report
// has to name the keys an operator would act on.
func TestStrandedOperatorEpoch_SeverityAndReport(t *testing.T) {
	role := "vtx.role.aaaaaaaaaaaaaaaaaaaa"
	holder := "vtx.identity.bbbbbbbbbbbbbbbbbbbb"
	perm := "vtx.permission.cccccccccccccccccccc"
	edge := "lnk.identity.dddddddddddddddddddd.holdsRole.role.aaaaaaaaaaaaaaaaaaaa"

	unaccounted := StrandedOperatorEpoch{RoleKey: role, Holders: []string{holder}}
	require.Equal(t, StrandedSeverityLiveAuthority, unaccounted.Severity(),
		"an unaccounted-for holder is root-equivalent through the name-matching lenses, grants or not")
	require.Contains(t, unaccounted.Report(), holder, "the holder key is the remedy, not a statistic")
	require.Contains(t, unaccounted.Report(), "Remedy: tombstone")

	accountedWithGrants := StrandedOperatorEpoch{
		RoleKey: role, ReachableVia: []string{edge}, GrantedBy: []string{perm},
	}
	require.Equal(t, StrandedSeverityLiveAuthority, accountedWithGrants.Severity(),
		"cap.roles walks grantedBy for ANY held role, so grants are authority the current role lacks")
	require.Contains(t, accountedWithGrants.Report(), perm)
	require.Contains(t, accountedWithGrants.Report(), edge)

	accountedNoGrants := StrandedOperatorEpoch{RoleKey: role, ReachableVia: []string{edge}}
	require.Equal(t, StrandedSeverityNoAddedAuthority, accountedNoGrants.Severity(),
		"an accounted-for holder of a role conferring nothing gains nothing")
	require.Contains(t, accountedNoGrants.Report(), edge, "the demoting edge must be named")

	unknown := StrandedOperatorEpoch{RoleKey: role, UnreadableEdges: 2}
	require.Equal(t, StrandedSeverityNoAddedAuthority, unknown.Severity(),
		"an unread edge might be a holder — unknown outranks inert")
	require.Contains(t, unknown.Report(), "LOWER BOUND")

	inert := StrandedOperatorEpoch{RoleKey: role, GrantedBy: []string{perm}}
	require.Equal(t, StrandedSeverityInert, inert.Severity(),
		"grants with no holder are unreachable — nothing walks grantedBy except through a holder")
	require.Contains(t, inert.Report(), perm)

	many := make([]string, strandedReportKeysShown+4)
	for i := range many {
		many[i] = perm
	}
	require.Contains(t, StrandedOperatorEpoch{RoleKey: role, Holders: many}.Report(), "+4 more",
		"a long list must be elided by count, not printed whole")
}

// TestSortedUnique_DoesNotMutateItsInput pins the property a caller reusing a
// slice after passing it here depends on.
func TestSortedUnique_DoesNotMutateItsInput(t *testing.T) {
	input := []string{"c", "a", "b", "a"}
	require.Equal(t, []string{"a", "b", "c"}, sortedUnique(input))
	require.Equal(t, []string{"c", "a", "b", "a"}, input, "sortedUnique must leave its argument alone")
}
