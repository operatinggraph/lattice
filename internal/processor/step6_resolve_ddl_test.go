package processor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Valid 20-char NanoIDs for the instanceOf-resolver fixtures. The link/aspect
// parsers validate NanoIDs strictly (unlike the DDL cache's raw vtx.meta.>
// prefix scan), so these must be real NanoIDs, not vtx.meta.<class> shorthands.
const (
	svcTypeID = "AbCdEfGhJkMnPqRsTuVw"
	tplID     = "BcDeFgHjKmNpQrStUvWx"
	instID    = "CdEfGhJkMnPqRsTuVwXy"
	cycBID    = "DeFgHjKmNpQrStUvWxYz"
	depH1     = "EfGhJkMnPqRsTuVwXyZa"
	depH2     = "FgHjKmNpQrStUvWxYzAb"
	depH3     = "GhJkMnPqRsTuVwXyZaBc"
	depH4     = "HjKmNpQrStUvWxYzAbCd"
)

// seedWidgetTypeDDL seeds a `widget` vertexType DDL meta-vertex admitting the
// three widget ops. It is the shared type authority every fine-grained
// widget.*.{template,instance} class resolves to via its instanceOf chain.
func seedWidgetTypeDDL(t *testing.T, ctx context.Context, conn substrateConn) {
	t.Helper()
	root := "vtx.meta." + svcTypeID
	doc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"widget","permittedCommands":["CreateWidgetTemplate","CreateWidgetInstance","RecordWidgetOutcome"]}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, root, doc); err != nil {
		t.Fatalf("seed widget type DDL: %v", err)
	}
}

// seedCommittedLink writes a committed link envelope (the on-demand /
// working-set resolution source). isDeleted=true models a tombstoned link.
func seedCommittedLink(t *testing.T, ctx context.Context, conn substrateConn, key string, deleted bool) {
	t.Helper()
	doc := []byte(fmt.Sprintf(`{"class":"instanceOf","isDeleted":%t,"data":{}}`, deleted))
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed link %s: %v", key, err)
	}
}

func buildWidgetValidator(t *testing.T) (*ValidatorImpl, context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedWidgetTypeDDL(t, ctx, conn)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return NewValidator(cache, conn, testCoreBucket, testLogger()), ctx, conn
}

func instanceVertexMut(op, id string) MutationOp {
	return MutationOp{
		Op:  op,
		Key: "vtx.widget." + id,
		Document: map[string]interface{}{
			"class":     "widget.deluxe.instance",
			"isDeleted": false,
			"data":      map[string]interface{}{},
		},
	}
}

func instanceOfLinkMut(srcID, tType, tID string) MutationOp {
	return MutationOp{
		Op:  "create",
		Key: fmt.Sprintf("lnk.widget.%s.instanceOf.%s.%s", srcID, tType, tID),
		Document: map[string]interface{}{
			"class":     "instanceOf",
			"isDeleted": false,
		},
	}
}

// (a) 1-hop, in-batch instanceOf → type DDL: an admitted op PASSes; a
// non-admitted op is rejected exactly as a coarse-class write-scope violation.
func TestResolveGoverningDDL_InBatchOneHop(t *testing.T) {
	t.Parallel()
	v, ctx, _ := buildWidgetValidator(t)

	pass := newTestEnvelope(testNanoID1)
	pass.OperationType = "CreateWidgetInstance"
	result := ScriptResult{Mutations: []MutationOp{
		instanceVertexMut("create", instID),
		instanceOfLinkMut(instID, "meta", svcTypeID),
	}}
	if err := v.Validate(ctx, pass, result, HydratedState{}); err != nil {
		t.Fatalf("admitted op should PASS via instanceOf walk: %v", err)
	}

	deny := newTestEnvelope(testNanoID1)
	deny.OperationType = "DeleteWidgetInstance" // not in the widget DDL's list
	err := v.Validate(ctx, deny, result, HydratedState{})
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) {
		t.Fatalf("non-admitted op should violate permittedCommands, got %T: %v", err, err)
	}
	if ddlErr.ViolatedConstraint != "permittedCommands" {
		t.Fatalf("constraint = %q, want permittedCommands", ddlErr.ViolatedConstraint)
	}
}

// (b) 2-hop instance → template (in-batch) → type (committed, resolved by the
// on-demand connInstanceOfReader). Proves batch + committed state compose.
func TestResolveGoverningDDL_TwoHopBatchPlusCommitted(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	// template → type, committed (the template pre-exists the instance op).
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", tplID, svcTypeID), false)

	env := newTestEnvelope(testNanoID1)
	env.OperationType = "RecordWidgetOutcome"
	result := ScriptResult{Mutations: []MutationOp{
		instanceVertexMut("update", instID),
		instanceOfLinkMut(instID, "widget", tplID), // instance → template, in-batch
	}}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("2-hop admitted op should PASS: %v", err)
	}
}

// (b') the same 2-hop chain, but the template → type link is resolved from the
// hydrated working set rather than an on-demand read.
func TestResolveGoverningDDL_TwoHopWorkingSet(t *testing.T) {
	t.Parallel()
	v, ctx, _ := buildWidgetValidator(t)

	env := newTestEnvelope(testNanoID1)
	env.OperationType = "RecordWidgetOutcome"
	tplLinkKey := fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", tplID, svcTypeID)
	state := HydratedState{Context: ScriptContext{Hydrated: map[string]VertexDoc{
		tplLinkKey: {Key: tplLinkKey, Class: "instanceOf"},
	}}}
	result := ScriptResult{Mutations: []MutationOp{
		instanceVertexMut("update", instID),
		instanceOfLinkMut(instID, "widget", tplID),
	}}
	if err := v.Validate(ctx, env, result, state); err != nil {
		t.Fatalf("2-hop (working-set) admitted op should PASS: %v", err)
	}
}

// (c) a fine-grained class with NO instanceOf link resolves to no governing DDL
// → the §1.5 permissive default (parity with today's coarse-miss behavior): a
// non-admitted op PASSes because nothing gates it.
func TestResolveGoverningDDL_NoInstanceOfPermissive(t *testing.T) {
	t.Parallel()
	v, ctx, _ := buildWidgetValidator(t)
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance" // not admitted, but no DDL resolves
	result := ScriptResult{Mutations: []MutationOp{instanceVertexMut("create", instID)}}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("fine-grained class without instanceOf must hit the permissive default: %v", err)
	}
}

// (c') a tombstoned instanceOf link is no link → permissive default.
func TestResolveGoverningDDL_TombstonedLinkPermissive(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", instID, svcTypeID), true /*deleted*/)
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance"
	result := ScriptResult{Mutations: []MutationOp{instanceVertexMut("create", instID)}}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("tombstoned instanceOf must resolve permissive, got: %v", err)
	}
}

// (d-cycle) a crafted instanceOf cycle terminates via the visited guard and
// resolves permissive — never into a wrong DDL, never an infinite loop.
func TestResolveGoverningDDL_CycleTerminates(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	// instID → cycB → instID, both committed.
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.widget.%s", instID, cycBID), false)
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.widget.%s", cycBID, instID), false)
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance"
	result := ScriptResult{Mutations: []MutationOp{instanceVertexMut("update", instID)}}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("cycle must terminate at the permissive default, got: %v", err)
	}
}

// (d-depth) a chain deeper than maxInstanceOfHops never reaches the type
// authority sitting beyond the bound → permissive default (the bound bites).
func TestResolveGoverningDDL_DepthBound(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	// instID → h1 → h2 → h3 → h4 → meta(widget). meta sits at hop 5; with
	// maxInstanceOfHops == 4 the walk stops before traversing h4 → meta.
	chain := []struct{ from, ftype, to, ttype string }{
		{instID, "widget", depH1, "widget"},
		{depH1, "widget", depH2, "widget"},
		{depH2, "widget", depH3, "widget"},
		{depH3, "widget", depH4, "widget"},
		{depH4, "widget", svcTypeID, "meta"},
	}
	for _, l := range chain {
		seedCommittedLink(t, ctx, conn,
			fmt.Sprintf("lnk.%s.%s.instanceOf.%s.%s", l.ftype, l.from, l.ttype, l.to), false)
	}
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance"
	result := ScriptResult{Mutations: []MutationOp{instanceVertexMut("update", instID)}}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("a chain beyond the hop bound must resolve permissive, got: %v", err)
	}
}

// (e) the exact class→DDL fast path is unchanged: a coarse `widget` class
// resolves directly, no instanceOf link involved — admitted PASSes, non-admitted
// is rejected.
func TestResolveGoverningDDL_ExactFastPath(t *testing.T) {
	t.Parallel()
	v, ctx, _ := buildWidgetValidator(t)
	coarse := MutationOp{
		Op:  "create",
		Key: "vtx.widget." + instID,
		Document: map[string]interface{}{
			"class": "widget", // exact DDL canonicalName
			"data":  map[string]interface{}{},
		},
	}

	pass := newTestEnvelope(testNanoID1)
	pass.OperationType = "CreateWidgetTemplate"
	if err := v.Validate(ctx, pass, ScriptResult{Mutations: []MutationOp{coarse}}, HydratedState{}); err != nil {
		t.Fatalf("coarse class admitted op should PASS via exact lookup: %v", err)
	}

	deny := newTestEnvelope(testNanoID1)
	deny.OperationType = "DeleteWidgetInstance"
	err := v.Validate(ctx, deny, ScriptResult{Mutations: []MutationOp{coarse}}, HydratedState{})
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) || ddlErr.ViolatedConstraint != "permittedCommands" {
		t.Fatalf("coarse class non-admitted op should violate permittedCommands, got %T: %v", err, err)
	}
}

// --- Hardening tests (review fold-in: determinism + tombstone reconciliation
// + the previously-untested terminal/fail-open branches). ---

const (
	gadgetTypeID = "ZyXwVuTsRqPnMkJhGfEd" // sorts AFTER svcTypeID by link key
	aspTypeID    = "JkMnPqRsTuVwXyZaBcDe"
	tgtID        = "KmNpQrStUvWxYzAbCdEf"
)

func seedVertexTypeDDLAs(t *testing.T, ctx context.Context, conn substrateConn, metaID, canonical string, cmdsJSON string) {
	t.Helper()
	doc := []byte(`{"class":"meta.ddl.vertexType","isDeleted":false,"data":{"canonicalName":"` + canonical + `","permittedCommands":` + cmdsJSON + `}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta."+metaID, doc); err != nil {
		t.Fatalf("seed vertexType DDL %s: %v", canonical, err)
	}
}

// E1 — multiple live instanceOf links are AMBIGUOUS → the permissive default
// (design §9 F1, mirroring the ClassForCommand ambiguity guard: never pick a
// type authority when it is ambiguous, so an extra link cannot steer the gate).
// A single strict link rejects a non-admitted op; adding a second live link
// disables enforcement (the op now passes) — proving ambiguity is not a guessed
// pick. Run repeatedly so a map-iteration-random pick would flake.
func TestResolveGoverningDDL_MultipleLiveLinksAreAmbiguous(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedWidgetTypeDDL(t, ctx, conn) // widget (svcTypeID) admits CreateWidgetInstance, not Delete
	seedVertexTypeDDLAs(t, ctx, conn, gadgetTypeID, "gadget", `["CreateGadget"]`)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	v := NewValidator(cache, conn, testCoreBucket, testLogger())

	lkWidget := fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", instID, svcTypeID)
	lkGadget := fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", instID, gadgetTypeID)
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance" // admitted by NEITHER widget nor gadget
	result := ScriptResult{Mutations: []MutationOp{instanceVertexMut("update", instID)}}

	// One live link → widget governs → DeleteWidgetInstance is REJECTED.
	single := HydratedState{Context: ScriptContext{Hydrated: map[string]VertexDoc{
		lkWidget: {Key: lkWidget, Class: "instanceOf"},
	}}}
	if err := v.Validate(ctx, env, result, single); err == nil {
		t.Fatalf("a single strict instanceOf must reject the non-admitted op")
	}

	// Two live links → ambiguous → permissive default → the op PASSES, every time.
	both := HydratedState{Context: ScriptContext{Hydrated: map[string]VertexDoc{
		lkWidget: {Key: lkWidget, Class: "instanceOf"},
		lkGadget: {Key: lkGadget, Class: "instanceOf"},
	}}}
	for i := 0; i < 50; i++ {
		if err := v.Validate(ctx, env, result, both); err != nil {
			t.Fatalf("iter %d: ambiguous (2 live links) must resolve permissive, got: %v", i, err)
		}
	}
}

// E2a — a create-then-tombstone of the SAME instanceOf link in one batch nets to
// no link → permissive default (a non-admitted op passes).
func TestResolveGoverningDDL_BatchCreateThenTombstoneNetDead(t *testing.T) {
	t.Parallel()
	v, ctx, _ := buildWidgetValidator(t)
	lk := fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", instID, svcTypeID)
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance" // not admitted by widget
	result := ScriptResult{Mutations: []MutationOp{
		instanceVertexMut("update", instID),
		{Op: "create", Key: lk, Document: map[string]interface{}{"class": "instanceOf", "isDeleted": false}},
		{Op: "tombstone", Key: lk, Document: map[string]interface{}{"class": "instanceOf", "isDeleted": true}},
	}}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("net-dead in-batch link must resolve permissive, got: %v", err)
	}
}

// E2b — a batch tombstone of an instanceOf link SUPPRESSES the same link that is
// still live in committed state (the batch is the in-flight truth).
func TestResolveGoverningDDL_BatchTombstoneSuppressesCommitted(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	lk := fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", instID, svcTypeID)
	seedCommittedLink(t, ctx, conn, lk, false) // committed-live
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance" // not admitted
	result := ScriptResult{Mutations: []MutationOp{
		instanceVertexMut("update", instID),
		{Op: "tombstone", Key: lk, Document: map[string]interface{}{"class": "instanceOf", "isDeleted": true}},
	}}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("batch tombstone must suppress the committed link → permissive, got: %v", err)
	}
}

// E3 — an instanceOf target that IS a meta-vertex but NOT a vertexType DDL
// (aspectType) is not a governing authority → break to permissive default.
func TestResolveGoverningDDL_MetaTargetNonVertexType(t *testing.T) {
	t.Parallel()
	_, ctx, conn := buildWidgetValidator(t)
	// Seed an aspectType meta-vertex and refresh a fresh cache that holds it.
	doc := []byte(`{"class":"meta.ddl.aspectType","isDeleted":false,"data":{"canonicalName":"someAspect","permittedCommands":["CreateWidgetInstance"]}}`)
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta."+aspTypeID, doc); err != nil {
		t.Fatalf("seed aspectType: %v", err)
	}
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	v2 := NewValidator(cache, conn, testCoreBucket, testLogger())
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance"
	result := ScriptResult{Mutations: []MutationOp{
		instanceVertexMut("create", instID),
		instanceOfLinkMut(instID, "meta", aspTypeID), // → aspectType meta, not a vertexType
	}}
	if err := v2.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("aspectType meta terminal must not enforce → permissive, got: %v", err)
	}
}

// E5 — an on-demand link-reader error fails open to the permissive default,
// never into a wrong DDL.
type errLinkReader struct{}

func (errLinkReader) LiveInstanceOfTargets(context.Context, string) ([]instanceOfEdge, error) {
	return nil, errors.New("injected read fault")
}

func TestResolveGoverningDDL_OnDemandReadErrorFailsOpen(t *testing.T) {
	t.Parallel()
	v, ctx, _ := buildWidgetValidator(t)
	v.linkReader = errLinkReader{} // override the conn-backed reader
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance"
	// No batch/working-set link → the walk reaches the on-demand reader, which errors.
	result := ScriptResult{Mutations: []MutationOp{instanceVertexMut("create", instID)}}
	if err := v.Validate(ctx, env, result, HydratedState{}); err != nil {
		t.Fatalf("on-demand read error must fail open to permissive, got: %v", err)
	}
}

// E6 — the one-hop instance→type terminal where the target's OWN class is a
// registered vertexType DDL (resolved via classOf), not a meta key.
func TestResolveGoverningDDL_OneHopClassOfTerminal(t *testing.T) {
	t.Parallel()
	v, ctx, _ := buildWidgetValidator(t)
	tgtKey := "vtx.widget." + tgtID
	// The target business vertex's class is the registered `widget` DDL name.
	state := HydratedState{Context: ScriptContext{Hydrated: map[string]VertexDoc{
		tgtKey: {Key: tgtKey, Class: "widget"},
	}}}
	result := ScriptResult{Mutations: []MutationOp{
		instanceVertexMut("create", instID),
		instanceOfLinkMut(instID, "widget", tgtID), // instance → business vertex (classOf terminal)
	}}

	pass := newTestEnvelope(testNanoID1)
	pass.OperationType = "CreateWidgetInstance"
	if err := v.Validate(ctx, pass, result, state); err != nil {
		t.Fatalf("one-hop classOf terminal admitted op should PASS: %v", err)
	}

	deny := newTestEnvelope(testNanoID1)
	deny.OperationType = "DeleteWidgetInstance"
	if err := v.Validate(ctx, deny, result, state); err == nil {
		t.Fatalf("one-hop classOf terminal non-admitted op should be rejected")
	}
}

// E7 — a fine-grained-class ASPECT mutation walks its PARENT vertex's instanceOf
// chain and is gated by the parent's type DDL's permittedCommands.
func TestResolveGoverningDDL_AspectMutationWalksParent(t *testing.T) {
	t.Parallel()
	v, ctx, _ := buildWidgetValidator(t)
	aspectMut := MutationOp{
		Op:  "create",
		Key: "vtx.widget." + instID + ".special",
		Document: map[string]interface{}{
			"class": "widget.special.aspect", // fine-grained aspect class, no DDL
			"data":  map[string]interface{}{},
		},
	}
	result := ScriptResult{Mutations: []MutationOp{
		aspectMut,
		instanceOfLinkMut(instID, "meta", svcTypeID), // parent → type DDL, in-batch
	}}

	pass := newTestEnvelope(testNanoID1)
	pass.OperationType = "CreateWidgetInstance"
	if err := v.Validate(ctx, pass, result, HydratedState{}); err != nil {
		t.Fatalf("aspect mutation gated by parent type DDL, admitted op should PASS: %v", err)
	}

	deny := newTestEnvelope(testNanoID1)
	deny.OperationType = "DeleteWidgetInstance"
	err := v.Validate(ctx, deny, result, HydratedState{})
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) || ddlErr.ViolatedConstraint != "permittedCommands" {
		t.Fatalf("aspect mutation non-admitted op should violate permittedCommands, got %T: %v", err, err)
	}
}
