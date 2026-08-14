// Package bypass holds the outcome-level adversarial residual for the
// Capability Lens security plane — assemblies that don't reduce to one
// mechanism's colocated white-box test.
//
// Runtime self-mint of a reserved destruction verb.
//
// Attack: `operator` holds CreatePermission and GrantPermission at scope:any,
// and CreatePermission takes operationType as a FREE STRING with no allow-list.
// An operator can therefore mint themselves a permission for
// ShredRetentionClassKey — the widest-blast-radius verb the platform has, which
// privacy-base deliberately ships no grant for — and grant it to their own role
// in two ops. Nothing about the two ops is itself unauthorized.
//
// Defense (grant-provenance-runtime-permission-minting-design.md, Contract #6
// §6.1): the mint stamps `data.origin: "runtime"`, the capabilityRoles lens
// projects the stamp onto the grant entry, and step 3 refuses any
// runtime-origin entry naming a core-reserved operationType while raising a
// `reserved-operation-grant-rejected` Health alert.
//
// What makes this an assembly and not a unit test: the mechanism is an AUDIT
// distinction, not a deny-list, and the distinction only exists if the stamp
// survives all three hops. The DDL's own test proves the stamp is written; the
// lens cypher test proves it projects; the authorizer's table test proves the
// refusal keys on it. Only here does one ShredRetentionClassKey grant travel
// the REAL rbac Starlark → the REAL capabilityRoles cypher → the REAL
// CapabilityAuthorizer → the REAL Health KV, which is the path a live
// self-minting operator would actually take.
//
//	Control: the same verb declared with `origin: "package"` (what the
//	         installer writes for a PermissionSpec) → must AUTHORIZE
//	Attack:  the same verb minted through CreatePermission+GrantPermission
//	         → must be REFUSED, and the alert must land in Health KV
//
// DEFENDED when: the runtime-minted grant does not authorize the verb, the
// package-declared one does, and an operator can see the refusal happened.
package bypass

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

const (
	// The self-minting operator and the role it holds. Distinct from the other
	// capadv vectors' ids so the fixtures cannot interfere.
	rgOperatorID  = "CAdvRGActorBbCdEfGhJ" // 20 chars, substrate.Alphabet
	rgRoleID      = "CAdvRGRozeBbCdEfGhJk" // 20 chars
	rgOperatorKey = "vtx.identity." + rgOperatorID
	rgRoleKey     = "vtx.role." + rgRoleID

	// The two verbs under reservation. ShredRetentionClassKey is the
	// destruction verb; UpdatePermission is the write-once guard, and the more
	// dangerous of the pair — holding it means being able to rewrite any
	// permission vertex's body, origin stamp included.
	rgReservedOp     = "ShredRetentionClassKey"
	rgReservedUpdate = "UpdatePermission"

	rgAdjBucket = "adjacency-kv"
	rgAlertKey  = "health.alerts.security." + processor.AlertCodeReservedOperationGrantRejected
)

// rgWorld is the live graph + the projection/authorization wiring one case runs
// against. Each case gets its own, so the control and the attack cannot share a
// projected doc.
type rgWorld struct {
	ctx    context.Context
	conn   *substrate.Conn
	coreKV *substrate.KV
	adjKV  *substrate.KV
}

func newRGWorld(t *testing.T) *rgWorld {
	t.Helper()
	ctx, conn := setupCapAdvHarness(t)

	js := conn.JetStream()
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: rgAdjBucket}); err != nil {
		t.Fatalf("rg: create adjacency bucket: %v", err)
	}
	coreKV, err := conn.OpenKV(ctx, capadvCoreBucket)
	if err != nil {
		t.Fatalf("rg: open core-kv: %v", err)
	}
	adjKV, err := conn.OpenKV(ctx, rgAdjBucket)
	if err != nil {
		t.Fatalf("rg: open adjacency-kv: %v", err)
	}

	w := &rgWorld{ctx: ctx, conn: conn, coreKV: coreKV, adjKV: adjKV}
	// The operator and the role they hold pre-exist the grant — the rbac DDL
	// gates GrantPermission on both vertices being alive.
	w.putVertex(t, rgOperatorKey, "identity", map[string]any{})
	w.putVertex(t, rgRoleKey, "role", map[string]any{})
	w.link(t, "holdsRole", "identity", rgOperatorID, "role", rgRoleID)
	return w
}

func (w *rgWorld) putVertex(t *testing.T, key, class string, data map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"key": key, "class": class, "isDeleted": false, "data": data,
	})
	if err != nil {
		t.Fatalf("rg: marshal vertex %s: %v", key, err)
	}
	if _, err := w.coreKV.Put(w.ctx, key, raw); err != nil {
		t.Fatalf("rg: put vertex %s: %v", key, err)
	}
}

// link builds both adjacency directions for one edge, which is what the cypher
// engine walks.
func (w *rgWorld) link(t *testing.T, name, fromType, fromID, toType, toID string) {
	t.Helper()
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	for _, ev := range []adjacency.CoreKVEvent{
		{CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound",
			NodeID: fromID, OtherNodeID: toID, OtherType: toType},
		{CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound",
			NodeID: toID, OtherNodeID: fromID, OtherType: fromType},
	} {
		if err := adjacency.Build(w.ctx, w.adjKV, ev); err != nil {
			t.Fatalf("rg: adjacency.Build %s (%s): %v", linkKey, ev.Direction, err)
		}
	}
}

// rbacScriptSource returns the shipped `rbac` DDL Starlark. Running the real
// script — not a hand-written vertex body — is what makes this vector prove the
// mint channel actually stamps, rather than proving a fixture does.
func rbacScriptSource(t *testing.T) string {
	t.Helper()
	for _, d := range rbacdomain.Package.DDLs {
		if d.CanonicalName == "rbac" {
			return d.Script
		}
	}
	t.Fatal("rg: rbac DDL not found in rbac-domain package")
	return ""
}

// rgLinkLister is the kv.Links seam; no link enumeration is reachable on the
// CreatePermission / GrantPermission branches.
type rgLinkLister struct{}

func (rgLinkLister) ListLinks(_ context.Context, _, _ string, _ int) ([]processor.LinkDoc, string, error) {
	return nil, "", nil
}

// runRbacOp executes one rbac op branch through the production Starlark runner
// and returns its mutations.
func (w *rgWorld) runRbacOp(t *testing.T, opType, payload string,
	hydrated map[string]processor.VertexDoc) []processor.MutationOp {
	t.Helper()
	runner := processor.NewStarlarkRunner(0, 0)
	res, err := runner.Run(w.ctx, processor.ScriptContext{
		Operation: &processor.OperationEnvelope{
			RequestID:     capadvReqV2Op1,
			Lane:          processor.LaneDefault,
			OperationType: opType,
			Actor:         rgOperatorKey,
			SubmittedAt:   "2026-08-14T10:00:00Z",
			Payload:       json.RawMessage(payload),
		},
		Hydrated:     hydrated,
		DDLLookup:    map[string]processor.MetaVertex{},
		ScriptSource: rbacScriptSource(t),
		ScriptClass:  "rbac",
		LinkLister:   rgLinkLister{},
	})
	if err != nil {
		t.Fatalf("rg: %s: %v", opType, err)
	}
	return res.Mutations
}

// selfMintReservedGrant walks the two-op path an operator would actually take:
// CreatePermission mints the vertex (stamping its own provenance), then
// GrantPermission links it to the role the operator holds. Both mutations are
// committed to Core KV as the Processor's committer would, so the graph the
// lens reads back is the one those ops produced.
func (w *rgWorld) selfMintReservedGrant(t *testing.T, op string) string {
	t.Helper()
	muts := w.runRbacOp(t, "CreatePermission",
		`{"operationType":"`+op+`","scope":"any"}`, nil)
	if len(muts) != 1 {
		t.Fatalf("rg: CreatePermission produced %d mutations, want 1", len(muts))
	}
	permKey := muts[0].Key
	raw, err := json.Marshal(withKey(muts[0].Document, permKey))
	if err != nil {
		t.Fatalf("rg: marshal minted permission: %v", err)
	}
	if _, err := w.coreKV.Put(w.ctx, permKey, raw); err != nil {
		t.Fatalf("rg: commit minted permission: %v", err)
	}

	grantMuts := w.runRbacOp(t, "GrantPermission",
		`{"permKey":"`+permKey+`","roleKey":"`+rgRoleKey+`"}`,
		map[string]processor.VertexDoc{
			permKey:   {Key: permKey, Class: "permission", IsDeleted: false, Data: map[string]any{}},
			rgRoleKey: {Key: rgRoleKey, Class: "role", IsDeleted: false, Data: map[string]any{}},
		})
	if len(grantMuts) != 1 {
		t.Fatalf("rg: GrantPermission produced %d mutations, want 1", len(grantMuts))
	}
	permID := permKey[len("vtx.permission."):]
	w.link(t, "grantedBy", "permission", permID, "role", rgRoleID)
	return permKey
}

// declarePackageGrant writes the permission vertex the INSTALLER would mint for
// a declared PermissionSpec of the same verb (internal/pkgmgr/build.go) and
// grants it to the same role. This is the control: the identical verb, reached
// through the sanctioned authoring channel.
func (w *rgWorld) declarePackageGrant(t *testing.T, permID, op string) {
	t.Helper()
	w.putVertex(t, "vtx.permission."+permID, "permission", map[string]any{
		"operationType": op,
		"scope":         "any",
		"origin":        "package",
		"declaredBy":    "privacy-base",
	})
	w.link(t, "grantedBy", "permission", permID, "role", rgRoleID)
}

func withKey(doc map[string]any, key string) map[string]any {
	out := map[string]any{"key": key}
	for k, v := range doc {
		out[k] = v
	}
	return out
}

// projectCapRoles runs the SHIPPED capabilityRoles cypher over the live graph
// and writes the projected row to the disjoint key an ordinary actor's platform
// path reads. The row's platformPermissions travel to KV as the cypher emitted
// them — no Go-side re-typing — so a projection that dropped `origin` would
// show up here rather than being papered over by a hand-built struct.
func (w *rgWorld) projectCapRoles(t *testing.T) {
	t.Helper()
	var spec string
	for _, l := range rbacdomain.Package.Lenses {
		if l.CanonicalName == "capabilityRoles" {
			spec = l.Spec
		}
	}
	if spec == "" {
		t.Fatal("rg: capabilityRoles lens not found in rbac-domain package")
	}

	eng := full.New()
	cr, err := eng.Parse(spec)
	if err != nil {
		t.Fatalf("rg: parse capabilityRoles: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := eng.ExecuteWith(w.ctx, cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": rgOperatorKey, "now": now, "projectedAt": now,
	}}, w.adjKV, w.coreKV)
	if err != nil {
		t.Fatalf("rg: execute capabilityRoles: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rg: capabilityRoles projected %d rows, want 1", len(rows))
	}
	writeCapRoles(t, w, rows[0])
}

func writeCapRoles(t *testing.T, w *rgWorld, row ruleengine.ProjectionResult) {
	t.Helper()
	key := "cap.roles.identity." + rgOperatorID
	body := map[string]any{
		"key":                    key,
		"actor":                  rgOperatorKey,
		"version":                "1.0",
		"projectedAt":            time.Now().UTC().Format(time.RFC3339Nano),
		"projectedFromRevisions": map[string]uint64{rgOperatorKey: 1},
		"lanes":                  []string{"default"},
		"platformPermissions":    row.Values["platformPermissions"],
		"serviceAccess":          []any{},
		"ephemeralGrants":        []any{},
		"roles":                  row.Values["roles"],
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("rg: marshal cap.roles doc: %v", err)
	}
	if _, err := w.conn.KVPut(w.ctx, capadvCapBucket, key, raw); err != nil {
		t.Fatalf("rg: put cap.roles doc: %v", err)
	}
}

// authorizer wires the REAL production selection path with a REAL Health KV
// emitter, so the alert assertion reads a persisted Health entry rather than a
// recording double.
func (w *rgWorld) authorizer(t *testing.T) processor.Authorizer {
	t.Helper()
	a, err := processor.SelectAuthorizerArgs(processor.SelectAuthorizerOpts{
		Mode:             processor.AuthModeCapability,
		Logger:           bypassLogger(),
		Reader:           w.conn,
		CapabilityBucket: capadvCapBucket,
		Emitter:          processor.NewHealthAlertEmitter(w.conn, capadvHealthBucket, bypassLogger()),
		// Production always sets this: an ordinary actor reads
		// cap.roles.<actor> alone, which is the key the projection above wrote.
		RbacRolesActive: true,
	})
	if err != nil {
		t.Fatalf("rg: SelectAuthorizerArgs: %v", err)
	}
	return a
}

func (w *rgWorld) authorizeReservedOp(t *testing.T, op string) processor.Decision {
	t.Helper()
	dec, err := w.authorizer(t).Authorize(w.ctx, &processor.OperationEnvelope{
		RequestID:     capadvReqV2Op2,
		Lane:          processor.LaneDefault,
		OperationType: op,
		Actor:         rgOperatorKey,
		SubmittedAt:   "2026-08-14T10:00:01Z",
		Payload:       json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("rg: Authorize: %v", err)
	}
	return dec
}

// reservedGrantAlert returns the persisted Health alert body, or nil if none
// landed.
func (w *rgWorld) reservedGrantAlert(t *testing.T) map[string]any {
	t.Helper()
	entry, err := w.conn.KVGet(w.ctx, capadvHealthBucket, rgAlertKey)
	if err != nil {
		return nil
	}
	var body map[string]any
	if err := json.Unmarshal(entry.Value, &body); err != nil {
		t.Fatalf("rg: unmarshal alert body: %v", err)
	}
	return body
}

// TestCapAdv_PackageDeclaredReservedGrant_Authorizes is the POSITIVE VECTOR,
// and it runs first for a reason: the refusal below would pass just as happily
// against a fixture whose graph never authorized anything at all — a broken
// projection, a mis-keyed cap doc, an actor with no role. This proves the whole
// assembly can say yes to this exact verb before anything asserts it says no.
func TestCapAdv_PackageDeclaredReservedGrant_Authorizes(t *testing.T) {
	w := newRGWorld(t)
	w.declarePackageGrant(t, "CAdvRGPkgPermBbCdEf1", rgReservedOp)
	w.projectCapRoles(t)

	dec := w.authorizeReservedOp(t, rgReservedOp)
	if !dec.Authorized {
		t.Fatalf("control BROKEN — a package-declared grant of %s must authorize; the "+
			"reservation constrains the runtime channel, not the declaration channel. got %+v",
			rgReservedOp, dec)
	}
	if alert := w.reservedGrantAlert(t); alert != nil {
		t.Fatalf("a package-declared grant must raise no reserved-operation alert; got %v", alert)
	}
	t.Log("control: a package-declared ShredRetentionClassKey grant authorizes ✓")
}

// TestCapAdv_RuntimeMintedReservedGrant_RefusedAndAlerted is the attack: the
// same verb, reached through CreatePermission+GrantPermission, must not
// authorize — and the operator must be able to see that someone tried.
func TestCapAdv_RuntimeMintedReservedGrant_RefusedAndAlerted(t *testing.T) {
	w := newRGWorld(t)
	permKey := w.selfMintReservedGrant(t, rgReservedOp)

	// The mint itself succeeded — nothing about the two ops was unauthorized,
	// which is precisely why the defense has to live at consumption.
	entry, err := w.coreKV.Get(w.ctx, permKey)
	if err != nil {
		t.Fatalf("rg: minted permission not committed: %v", err)
	}
	var minted map[string]any
	if err := json.Unmarshal(entry.Value, &minted); err != nil {
		t.Fatalf("rg: unmarshal minted permission: %v", err)
	}
	data, _ := minted["data"].(map[string]any)
	if got, _ := data["origin"].(string); got != "runtime" {
		t.Fatalf("EXPOSED at the mint — the self-minted permission carries origin %q, not "+
			"\"runtime\"; an unstamped vertex is indistinguishable from a package declaration "+
			"downstream and the refusal can never fire", got)
	}

	w.projectCapRoles(t)

	dec := w.authorizeReservedOp(t, rgReservedOp)
	if dec.Authorized {
		t.Fatalf("EXPOSED — an operator minted themselves %s in two ops and the platform "+
			"authorized it; the runtime-origin reservation did not hold end to end. got %+v",
			rgReservedOp, dec)
	}
	if dec.Code != processor.ErrCodeAuthDenied {
		t.Errorf("refusal code = %q, want %q", dec.Code, processor.ErrCodeAuthDenied)
	}

	// Refused is not enough — a silent refusal leaves nobody aware that an
	// operator reached for the platform's widest-blast-radius verb.
	alert := w.reservedGrantAlert(t)
	if alert == nil {
		t.Fatalf("EXPOSED (audit) — the grant was refused but no %s entry reached Health KV; "+
			"the design's whole point is that a reserved self-mint is visible, not silently dropped",
			rgAlertKey)
	}
	if got, _ := alert["alertCode"].(string); got != processor.AlertCodeReservedOperationGrantRejected {
		t.Errorf("alert alertCode = %q, want %q", got, processor.AlertCodeReservedOperationGrantRejected)
	}
	details, _ := alert["details"].(map[string]any)
	if got, _ := details["operationType"].(string); got != rgReservedOp {
		t.Errorf("alert details.operationType = %q, want %q", got, rgReservedOp)
	}
	if got, _ := details["actor"].(string); got != rgOperatorKey {
		t.Errorf("alert details.actor = %q, want %q", got, rgOperatorKey)
	}
	if got, _ := details["origin"].(string); got != "runtime" {
		t.Errorf("alert details.origin = %q, want \"runtime\"", got)
	}
	t.Log("DEFENDED: a runtime-minted ShredRetentionClassKey grant is refused at step 3 and alerted ✓")
}

// TestCapAdv_RuntimeMintedReservedGrant_PackageGrantStillWins is the mixed
// case at assembly scale. An actor can legitimately hold the same verb from two
// roles, so a runtime self-mint sitting alongside a package declaration must
// refuse the self-mint WITHOUT retiring the operation — otherwise minting
// yourself a duplicate grant would be a denial-of-service against the
// deployment's own deliberate one.
func TestCapAdv_RuntimeMintedReservedGrant_PackageGrantStillWins(t *testing.T) {
	w := newRGWorld(t)
	w.selfMintReservedGrant(t, rgReservedOp)
	w.declarePackageGrant(t, "CAdvRGPkgPermBbCdEf2", rgReservedOp)
	w.projectCapRoles(t)

	dec := w.authorizeReservedOp(t, rgReservedOp)
	if !dec.Authorized {
		t.Fatalf("EXPOSED (availability) — a self-minted grant refused the DEPLOYMENT's "+
			"package-declared grant of the same verb; the refusal must retire one entry, "+
			"not the operation. got %+v", dec)
	}
	if alert := w.reservedGrantAlert(t); alert == nil {
		t.Fatalf("the self-mint's refusal must still be visible even though the op proceeded "+
			"on package authority; no %s entry in Health KV", rgAlertKey)
	}
	t.Log("DEFENDED: the refusal retires the runtime entry only; the package grant still authorizes ✓")
}

// TestCapAdv_RuntimeMintedUpdatePermission_RefusedAndAlerted is the write-once
// bypass, end to end.
//
// Inc 1 withdrew rbac-domain's UpdatePermission grant, which closes the
// DECLARED channel — no package ships it any more. But CreatePermission takes
// operationType as a free string with no allow-list, so the withdrawal does
// nothing to stop an operator minting a BRAND-NEW
// permission{operationType:"UpdatePermission"} vertex and granting it to a role
// they already hold. Holding it means being able to rewrite any permission
// vertex's body — including stripping `origin` off a package's reserved grant,
// silently downgrading it to unstamped → runtime → refused, and thereby
// disarming the very reservation this fire builds.
//
// So the write-once precondition the whole design rests on is only real if the
// runtime channel is closed too. This drives the actual two-op mint through the
// shipped rbac Starlark and proves the platform refuses the result.
func TestCapAdv_RuntimeMintedUpdatePermission_RefusedAndAlerted(t *testing.T) {
	w := newRGWorld(t)

	// Positive vector first: a package-declared UpdatePermission still
	// authorizes. The reservation constrains the runtime channel, not the verb
	// — no package ships this grant today, but that is rbac-domain's choice,
	// not a platform prohibition, and the refusal below must be the provenance
	// gate rather than a blanket ban that would silently break a deployment
	// that did declare it.
	w.declarePackageGrant(t, "CAdvRGUpdPermBbCdEf1", rgReservedUpdate)
	w.projectCapRoles(t)
	if dec := w.authorizeReservedOp(t, rgReservedUpdate); !dec.Authorized {
		t.Fatalf("control BROKEN — a package-declared %s grant must authorize; got %+v",
			rgReservedUpdate, dec)
	}

	// The attack, in its own world so the package grant cannot carry it.
	w2 := newRGWorld(t)
	w2.selfMintReservedGrant(t, rgReservedUpdate)
	w2.projectCapRoles(t)

	dec := w2.authorizeReservedOp(t, rgReservedUpdate)
	if dec.Authorized {
		t.Fatalf("EXPOSED — an operator minted themselves %s in two ops and the platform "+
			"authorized it. Holding it lets them rewrite any permission vertex body, "+
			"including forging or stripping the origin stamp the reservation reads — the "+
			"write-once precondition the whole design rests on is gone. got %+v",
			rgReservedUpdate, dec)
	}

	alert := w2.reservedGrantAlert(t)
	if alert == nil {
		t.Fatalf("EXPOSED (audit) — the %s grant was refused but nothing reached Health KV",
			rgReservedUpdate)
	}
	details, _ := alert["details"].(map[string]any)
	if got, _ := details["operationType"].(string); got != rgReservedUpdate {
		t.Errorf("alert details.operationType = %q, want %q", got, rgReservedUpdate)
	}
	t.Log("DEFENDED: a runtime-minted UpdatePermission grant is refused at step 3 and alerted ✓")
}
