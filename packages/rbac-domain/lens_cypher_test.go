package rbacdomain

// Rule-engine proof of the two capability lenses, driven through the `full`
// engine — the one activation selects via engine:"full" — against an embedded
// NATS Core/Adjacency KV. Same harness shape as edge-manifest / clinic-domain's
// lens cypher tests.
//
// These two lenses ARE the write-side authorization surface: capabilityRoles
// projects the platformPermissions every ordinary actor's step-3 dispatch reads
// out of cap.roles.<actor>, and capabilityRoleIndex feeds the FR22 denial
// response. The package's other tests exercise the Starlark that writes the
// role graph; nothing until now executed the cypher that reads it back.
//
// Three properties only execution can show:
//
//   - the `{key: $actorKey}` anchor is what keeps one actor's grants out of
//     another's row. A spec that dropped the anchor still parses, still
//     projects, and hands every actor the union of the whole graph's
//     permissions.
//   - an actor holding NO role must still produce exactly ONE row, carrying a
//     single degenerate all-null collect entry. That shape is not cosmetic:
//     the Output descriptor's RealnessFilter:"operationType" filters those
//     entries out and only then does EmptyBehavior:"delete" fire, which is how
//     a revoked actor's cap.roles.<id> key gets removed rather than stranded
//     (Contract #6 §6.8, absence = denial). No rows at all, or a non-null
//     entry, and the revoke leaks.
//   - capabilityRoleIndex must COLLAPSE per operationType: two roles granting
//     the same op are one row naming both, not two rows racing for one key.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type rbacFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newRbacFixture(t *testing.T) *rbacFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &rbacFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *rbacFixture) vtx(t *testing.T, name, typ string, data map[string]any) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	if data == nil {
		data = map[string]any{}
	}
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *rbacFixture) key(name string) string {
	return "vtx." + f.types[f.ids[name]] + "." + f.ids[name]
}

func (f *rbacFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := f.key(ownerName)
	k := owner + "." + local
	body := map[string]any{"key": k, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), k, raw)
	require.NoError(t, err)
}

func (f *rbacFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

// role creates a role vertex plus the canonicalName aspect capabilityRoleIndex
// names its rows by.
func (f *rbacFixture) role(t *testing.T, name, canonical string) {
	t.Helper()
	f.vtx(t, name, "role", nil)
	f.aspect(t, name, "canonicalName", "canonicalName", map[string]any{"value": canonical})
}

// grants creates a permission vertex and the `permission grantedBy role` link
// (Contract #1 §1.1 direction: the later-arriving permission is the source).
func (f *rbacFixture) grants(t *testing.T, permName, roleName, opType, scope string, lanes []string) {
	t.Helper()
	data := map[string]any{"operationType": opType, "scope": scope}
	if lanes != nil {
		data["lanes"] = lanes
	}
	f.vtx(t, permName, "permission", data)
	f.edge(t, "grantedBy", permName, roleName)
}

// grantsWithOrigin is `grants` plus the Contract #6 §6.1 provenance stamp the
// installer / CreatePermission write. Separate from `grants` on purpose: the
// unstamped helper keeps exercising the pre-stamp vertex shape, which is what
// a live graph is full of until every package is re-applied.
func (f *rbacFixture) grantsWithOrigin(t *testing.T, permName, roleName, opType, scope, origin string) {
	t.Helper()
	f.vtx(t, permName, "permission", map[string]any{
		"operationType": opType, "scope": scope, "origin": origin,
	})
	f.edge(t, "grantedBy", permName, roleName)
}

func (f *rbacFixture) project(t *testing.T, spec, actorKey string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "rbac-domain lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": actorKey, "now": now, "projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// rbacWorld seeds two role-holders and one role-less identity:
//
//	tenant   -[:holdsRole]-> tenantRole   <-[:grantedBy]- SubmitApplication (self, [default])
//	                                      <-[:grantedBy]- WithdrawApplication (self)
//	landlord -[:holdsRole]-> landlordRole <-[:grantedBy]- DecideApplication (any)
//	stranger — no role at all
//
// SubmitApplication is granted by BOTH roles, which is what makes the
// capabilityRoleIndex collapse assertion meaningful.
func rbacWorld(t *testing.T) *rbacFixture {
	f := newRbacFixture(t)

	f.role(t, "tenantRole", "tenant")
	f.role(t, "landlordRole", "landlord")

	f.vtx(t, "tenant", "identity", nil)
	f.vtx(t, "landlord", "identity", nil)
	f.vtx(t, "stranger", "identity", nil)

	f.edge(t, "holdsRole", "tenant", "tenantRole")
	f.edge(t, "holdsRole", "landlord", "landlordRole")

	f.grants(t, "permSubmit", "tenantRole", "SubmitApplication", "self", []string{"default"})
	f.grants(t, "permWithdraw", "tenantRole", "WithdrawApplication", "self", nil)
	f.grants(t, "permDecide", "landlordRole", "DecideApplication", "any", nil)
	f.grants(t, "permSubmitLandlord", "landlordRole", "SubmitApplication", "any", nil)
	return f
}

func TestCapabilityRoles_ProjectsOnlyTheAnchoredActorsGrants(t *testing.T) {
	f := rbacWorld(t)

	rows := f.project(t, capabilityRolesSpec, f.key("tenant"))
	require.Len(t, rows, 1, "an actor-aggregate lens projects exactly one row per anchor; got %v", rows)

	row := rows[0].Values
	require.Equal(t, f.key("tenant"), row["actorKey"])

	perms := row["platformPermissions"].([]any)
	byOp := map[string]map[string]any{}
	for _, p := range perms {
		e := p.(map[string]any)
		op, _ := e["operationType"].(string)
		byOp[op] = e
	}
	require.Len(t, byOp, 2,
		"the tenant holds exactly its own role's two permissions — the landlord's must not appear, or the $actorKey anchor is not binding. got %v", perms)
	require.Contains(t, byOp, "SubmitApplication")
	require.Contains(t, byOp, "WithdrawApplication")
	require.NotContains(t, byOp, "DecideApplication",
		"another role's grant leaked into this actor's capability row")

	require.Equal(t, "self", byOp["SubmitApplication"]["scope"])
	require.Equal(t, []any{"default"}, byOp["SubmitApplication"]["lanes"],
		"lanes is per-op and optional (Contract #6 §6.4) — it must survive the collect verbatim when the granting permission set it")
	require.Nil(t, byOp["WithdrawApplication"]["lanes"],
		"a permission that set no lanes must project null lanes, not the neighbouring entry's")

	roles := row["roles"].([]any)
	require.Equal(t, []any{f.key("tenantRole")}, roles)
}

func TestCapabilityRoles_RolelessActorYieldsTheDegenerateEntry(t *testing.T) {
	f := rbacWorld(t)

	rows := f.project(t, capabilityRolesSpec, f.key("stranger"))
	require.Len(t, rows, 1,
		"the OPTIONAL MATCH must still project a row for a role-less actor — the delete behavior needs a row to evaluate; no row at all leaves cap.roles.<id> stranded")

	row := rows[0].Values
	require.Equal(t, f.key("stranger"), row["actorKey"])

	perms := row["platformPermissions"].([]any)
	require.Len(t, perms, 1, "exactly one degenerate collect entry; got %v", perms)
	entry := perms[0].(map[string]any)
	require.Nil(t, entry["operationType"],
		"RealnessFilter:\"operationType\" is what turns this entry into an empty projection — a non-null operationType here means a revoked actor keeps a live capability key")
	require.Nil(t, entry["scope"])
	require.Nil(t, entry["lanes"])
	require.Nil(t, entry["origin"],
		"the provenance field must join the degenerate entry as null like every other — a non-null constant here would make the entry look real to RealnessFilter")

	// The two collects behave differently on a null binding, and the descriptor
	// depends on which: collecting a MAP LITERAL yields one all-null entry (the
	// one asserted above), while collecting a bare PROPERTY yields an empty
	// list. That asymmetry is why RealnessFilter names a field inside
	// platformPermissions — pointed at `roles` it would find nothing to filter
	// and could never mark the row unreal.
	require.Empty(t, row["roles"].([]any),
		"a bare-property collect over a null binding is empty, not [null]")
}

// The provenance stamp is only a discriminator if it survives the projection.
// Contract #6 §6.1 puts the reserved-operationType refusal on the PROJECTED
// entry, not on the vertex, so a stamp the cypher drops is a stamp that does
// not exist as far as step 3 is concerned — and, since absence reads as
// `runtime`, a dropped `package` stamp would silently reclassify every
// package-declared grant as a self-mint.
//
// Three shapes have to come through distinctly, because the consuming
// predicate treats exactly one of them as exempt:
//
//	package  → the declared, uninstallable deployment decision (exempt)
//	runtime  → the self-mint (governed)
//	absent   → a vertex minted before the stamp existed (governed, fail-closed)
func TestCapabilityRoles_ProjectsPermissionOrigin(t *testing.T) {
	f := newRbacFixture(t)
	f.role(t, "opRole", "operator")
	f.vtx(t, "op", "identity", nil)
	f.edge(t, "holdsRole", "op", "opRole")

	f.grantsWithOrigin(t, "permDeclared", "opRole", "ShredRetentionClassKey", "any", "package")
	f.grantsWithOrigin(t, "permMinted", "opRole", "CreateRole", "any", "runtime")
	// No stamp at all — the pre-migration vertex shape.
	f.grants(t, "permLegacy", "opRole", "ArchiveLease", "any", nil)

	rows := f.project(t, capabilityRolesSpec, f.key("op"))
	require.Len(t, rows, 1)

	byOp := map[string]map[string]any{}
	for _, p := range rows[0].Values["platformPermissions"].([]any) {
		e := p.(map[string]any)
		op, _ := e["operationType"].(string)
		byOp[op] = e
	}
	require.Len(t, byOp, 3, "all three grants must project; got %v", byOp)

	require.Equal(t, "package", byOp["ShredRetentionClassKey"]["origin"],
		"a package-declared grant must project its stamp verbatim — this is the entry step 3 exempts from the reserved-operationType refusal, and losing the stamp denies a legitimate deployment decision")
	require.Equal(t, "runtime", byOp["CreateRole"]["origin"],
		"a runtime mint must project as runtime, or the refusal has nothing to key on")
	require.Nil(t, byOp["ArchiveLease"]["origin"],
		"an unstamped vertex must project null rather than borrowing a neighbouring entry's origin — the consuming side reads null as `runtime` (fail-closed), which is only safe if null actually arrives")
}

func TestCapabilityRoleIndex_CollapsesRolesPerOperation(t *testing.T) {
	f := rbacWorld(t)

	// Keyed by operationType, not by actor: the spec takes no $actorKey.
	rows := f.project(t, capabilityRoleIndexSpec, "")
	byOp := map[string][]any{}
	for _, r := range rows {
		op, _ := r.Values["operationType"].(string)
		require.NotContains(t, byOp, op,
			"one row per operationType — two rows for %q would race for a single cap.role-by-operation key", op)
		byOp[op] = r.Values["roles"].([]any)
	}

	require.Len(t, byOp, 3, "three distinct operationTypes are granted across the two roles; got %v", byOp)
	require.ElementsMatch(t, []any{"tenant", "landlord"}, byOp["SubmitApplication"],
		"an op granted by two roles collapses into one row naming both — this is what FR22's rolesCarryingPermission renders")
	require.Equal(t, []any{"tenant"}, byOp["WithdrawApplication"])
	require.Equal(t, []any{"landlord"}, byOp["DecideApplication"])
}

func TestCapabilityRoleIndex_NamesRolesByCanonicalNameNotKey(t *testing.T) {
	f := rbacWorld(t)

	rows := f.project(t, capabilityRoleIndexSpec, "")
	require.NotEmpty(t, rows)
	for _, r := range rows {
		for _, role := range r.Values["roles"].([]any) {
			require.NotContains(t, role, "vtx.role.",
				"the denial response renders these to a human — a vertex key here would surface a NanoID where a role name belongs")
		}
	}
}

func TestCapabilityRoleIndex_ActivatesWithItsDeclaredIntoKey(t *testing.T) {
	eng := full.New()
	compiled, err := eng.Parse(capabilityRoleIndexSpec)
	require.NoError(t, err)
	cr, ok := compiled.(*full.CompiledRule)
	require.True(t, ok)
	cr.KeyColumns = []string{"operationType"}
	require.NoError(t, cr.ValidateKeyColumns(),
		"the lens must activate against its declared IntoKey — the activation-time gate a mis-declared key column dies on")
}
