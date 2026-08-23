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

func (errLinkReader) LiveInstanceOfTargets(context.Context, string, *liveReadBudgetTracker) ([]instanceOfEdge, error) {
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

// E8 — a successful on-demand instanceOf resolution charges its round trips
// (the prefix list + its per-key GET) against the shared live-read budget —
// closing the gap where this reader's live reads sat outside the SAME budget
// kv.Read/kv.Links draw from (the "found reviewing the script-live-read-budget
// fix" backlog item).
func TestResolveGoverningDDL_OnDemandReadChargesLiveReadBudget(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", instID, svcTypeID), false)
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "CreateWidgetInstance"
	result := ScriptResult{Mutations: []MutationOp{instanceVertexMut("create", instID)}}
	tracker := &liveReadBudgetTracker{budget: DefaultLiveReadBudget}
	state := HydratedState{Context: ScriptContext{LiveReads: tracker}}
	if err := v.Validate(ctx, env, result, state); err != nil {
		t.Fatalf("admitted op via on-demand instanceOf resolution should PASS: %v", err)
	}
	if tracker.spent == 0 {
		t.Fatalf("on-demand instanceOf resolution must charge the shared live-read budget, spent = 0")
	}
}

// E9 — an execution already out of live-read budget fails the on-demand
// instanceOf read open to the permissive default, same as any other read
// fault at this seam (E5) — never a partial or wrong resolution.
func TestResolveGoverningDDL_LiveReadBudgetExhaustedFailsOpen(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", instID, svcTypeID), false)
	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance" // not admitted by widget
	result := ScriptResult{Mutations: []MutationOp{instanceVertexMut("create", instID)}}
	state := HydratedState{Context: ScriptContext{LiveReads: &liveReadBudgetTracker{budget: 0}}}
	if err := v.Validate(ctx, env, result, state); err != nil {
		t.Fatalf("exhausted live-read budget must fail open to permissive, got: %v", err)
	}
}

// classTargetMut writes `class` onto the shared classOf-terminal vertex key.
// Several of these in one batch is the duplicate-key shape the commit path
// collapses to its last member.
func classTargetMut(op, class string) MutationOp {
	return MutationOp{
		Op:  op,
		Key: "vtx.widget." + tgtID,
		Document: map[string]interface{}{
			"class":     class,
			"isDeleted": false,
			"data":      map[string]interface{}{},
		},
	}
}

// buildAbstractWidgetValidator seeds the `widget` type DDL alongside an
// ABSTRACT `location` DDL, so a classOf terminal can be pointed at either a
// concrete or an abstract authority within one batch.
func buildAbstractWidgetValidator(t *testing.T) (*ValidatorImpl, context.Context) {
	t.Helper()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedWidgetTypeDDL(t, ctx, conn)
	seedAbstractDDL(t, ctx, conn, "location")
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return NewValidator(cache, conn, testCoreBucket, testLogger()), ctx
}

// walkFromInstance resolves the governing DDL for the fine-grained instance
// vertex whose only route to a type authority is the instanceOf link into the
// classOf terminal — the path classOf sits on.
func walkFromInstance(t *testing.T, v *ValidatorImpl, ctx context.Context, muts []MutationOp) (MetaVertexRef, bool) {
	t.Helper()
	result := ScriptResult{Mutations: append([]MutationOp{
		instanceVertexMut("create", instID),
		instanceOfLinkMut(instID, "widget", tgtID),
	}, muts...)}
	return v.resolveGoverningDDL(ctx, "widget.deluxe.instance",
		"vtx.widget."+instID, substrate.KindVertex, result, HydratedState{})
}

// TestResolveGoverningDDL_ClassOfLastWriteWins pins the commit path's own
// duplicate-key rule inside the resolver: a batch writing a key twice commits
// only the last write, so the terminal's class is the LAST one the batch
// declares. The single-mutation control below it proves the fixture reaches
// classOf at all, so the decoy case cannot pass by failing to resolve.
func TestResolveGoverningDDL_ClassOfLastWriteWins(t *testing.T) {
	t.Parallel()
	v, ctx := buildAbstractWidgetValidator(t)

	// Control: the concrete class alone resolves to the widget authority.
	ref, ok := walkFromInstance(t, v, ctx, []MutationOp{classTargetMut("create", "widget")})
	if !ok || ref.CanonicalName != "widget" {
		t.Fatalf("control: want the widget DDL, got ok=%v name=%q", ok, ref.CanonicalName)
	}

	// A benign decoy ahead of the real write must not be what classifies the
	// terminal — only the update reaches Core KV.
	ref, ok = walkFromInstance(t, v, ctx, []MutationOp{
		classTargetMut("create", "widget"),
		classTargetMut("update", "location"),
	})
	if !ok {
		t.Fatalf("the last write's class is registered and must resolve")
	}
	if ref.CanonicalName != "location" || !ref.Abstract {
		t.Fatalf("terminal must be classified by the LAST write (abstract %q), got name=%q abstract=%v",
			"location", ref.CanonicalName, ref.Abstract)
	}
}

// TestResolveGoverningDDL_ClassOfBatchTombstoneWins is the removal half of the
// same rule: a batch whose last write on the terminal is a tombstone leaves no
// vertex to classify after commit, so the walk must resolve to nothing rather
// than fall back to the class an earlier mutation in the same batch declared.
func TestResolveGoverningDDL_ClassOfBatchTombstoneWins(t *testing.T) {
	t.Parallel()
	v, ctx := buildAbstractWidgetValidator(t)
	ref, ok := walkFromInstance(t, v, ctx, []MutationOp{
		classTargetMut("create", "widget"),
		{Op: "tombstone", Key: "vtx.widget." + tgtID},
	})
	if ok {
		t.Fatalf("a terminal the batch tombstones must resolve to no governing DDL, got %q", ref.CanonicalName)
	}
}

// --- Fire 1 Inc 2a: connInstanceOfReader.LiveInstanceOfTargets issues one
// KVGetMulti wildcard call, not one KVListKeysPrefix + N KVGet.

// fakeInstanceOfConnReader is an in-memory instanceOfConnReader that counts
// calls, proving connInstanceOfReader's round-trip collapse (Fire 1 Inc 2a).
type fakeInstanceOfConnReader struct {
	entries       map[string]*substrate.KVEntry
	getMultiCalls int
	getMultiKeys  [][]string
}

func (f *fakeInstanceOfConnReader) KVGetMulti(_ context.Context, _ string, keys []string) (map[string]*substrate.KVEntry, error) {
	f.getMultiCalls++
	f.getMultiKeys = append(f.getMultiKeys, append([]string{}, keys...))
	out := make(map[string]*substrate.KVEntry, len(f.entries))
	// keys is the single wildcard filter Inc 2a issues; a fake real enough to
	// prove the call SHAPE returns every seeded entry, mirroring what a real
	// KVGetMulti would match against that filter.
	for k, e := range f.entries {
		out[k] = e
	}
	return out, nil
}

// TestConnInstanceOfReader_OneGetMultiCallReplacesListPlusPerKeyGets pins the
// round-trip collapse itself. Mutation-verified by hand against the
// pre-Fire-1 list-then-loop implementation, which issued 1 (list) + N (gets)
// calls here.
func TestConnInstanceOfReader_OneGetMultiCallReplacesListPlusPerKeyGets(t *testing.T) {
	root := "vtx.widget." + instID
	live1 := "lnk.widget." + instID + ".instanceOf.widget." + tplID
	live2 := "lnk.widget." + instID + ".instanceOf.widget." + svcTypeID
	fake := &fakeInstanceOfConnReader{entries: map[string]*substrate.KVEntry{
		live1: {Value: []byte(`{"class":"instanceOf","isDeleted":false}`)},
		live2: {Value: []byte(`{"class":"instanceOf","isDeleted":false}`)},
	}}
	reader := &connInstanceOfReader{conn: fake, coreBucket: testCoreBucket}
	edges, err := reader.LiveInstanceOfTargets(context.Background(), root, &liveReadBudgetTracker{budget: 100})
	if err != nil {
		t.Fatalf("LiveInstanceOfTargets: %v", err)
	}
	if fake.getMultiCalls != 1 {
		t.Fatalf("getMultiCalls = %d, want 1 (was 1+N per-key GETs before Fire 1 Inc 2a)", fake.getMultiCalls)
	}
	if len(fake.getMultiKeys) != 1 || len(fake.getMultiKeys[0]) != 1 {
		t.Fatalf("want the single call to carry exactly one wildcard filter, got %v", fake.getMultiKeys)
	}
	if got := fake.getMultiKeys[0][0]; got != "lnk.widget."+instID+".instanceOf.>" {
		t.Errorf("filter = %q, want the source-anchored instanceOf wildcard", got)
	}
	// Batching must not merge or drop edges: a multi-edge root still surfaces
	// BOTH live edges here, so the caller's soleTarget ambiguity guard
	// (§9 F1) still sees two and refuses to resolve — proven at the
	// resolver level by TestResolveGoverningDDL_MultipleLiveLinksAreAmbiguous.
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2 — batching must not silently merge multi-edge roots", len(edges))
	}
}

// TestConnInstanceOfReader_AgainstCoreKV exercises the production
// connInstanceOfReader against a real embedded Core KV: a tombstoned
// instanceOf link is excluded, a live one is returned with its target parsed
// correctly from the link key.
func TestConnInstanceOfReader_AgainstCoreKV(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root := "vtx.widget." + instID
	seedCommittedLink(t, ctx, conn, "lnk.widget."+instID+".instanceOf.widget."+tplID, false)
	seedCommittedLink(t, ctx, conn, "lnk.widget."+instID+".instanceOf.widget."+cycBID, true)

	reader := &connInstanceOfReader{conn: conn, coreBucket: testCoreBucket}
	edges, err := reader.LiveInstanceOfTargets(ctx, root, &liveReadBudgetTracker{budget: 100})
	if err != nil {
		t.Fatalf("LiveInstanceOfTargets: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1 (tombstoned link must be excluded)", len(edges))
	}
	if want := "vtx.widget." + tplID; edges[0].target != want {
		t.Errorf("target = %q, want %q", edges[0].target, want)
	}
}

// --- Fire 1 Inc 2b: ddlResolutionMemo collapses repeat walks within one
// execution. countingLinkReader fakes instanceOfTargetReader and counts
// LiveInstanceOfTargets calls PER NODE, so a test can assert a shared
// intermediate node is only ever asked once.

type countingLinkReader struct {
	edges map[string][]instanceOfEdge
	calls map[string]int
}

func (f *countingLinkReader) LiveInstanceOfTargets(_ context.Context, vtxRoot string, _ *liveReadBudgetTracker) ([]instanceOfEdge, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[vtxRoot]++
	return f.edges[vtxRoot], nil
}

func mustNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	return id
}

// TestDDLResolutionMemo_SharedIntermediateNodeResolvedOnce pins the payoff
// claim directly: two roots whose walk shares an intermediate node (the
// self-pay shape — eight distinct transaction aspects all instanceOf the
// SAME template) resolve that shared node's live read exactly once across
// two resolutions in one execution, while each root's own (distinct) hop is
// still read once each. Mutation-verified by hand: a nil memo (today's
// behavior) makes the shared node's call count 2, not 1.
func TestDDLResolutionMemo_SharedIntermediateNodeResolvedOnce(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root1, root2, shared, metaID := mustNanoID(t), mustNanoID(t), mustNanoID(t), mustNanoID(t)
	root1Key := "vtx.widget." + root1
	root2Key := "vtx.widget." + root2
	sharedKey := "vtx.widget." + shared
	seedVertexTypeDDLAs(t, ctx, conn, metaID, "sharedType", `["X"]`)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	fake := &countingLinkReader{edges: map[string][]instanceOfEdge{
		root1Key:  {{linkKey: "lnk.widget." + root1 + ".instanceOf.widget." + shared, target: sharedKey}},
		root2Key:  {{linkKey: "lnk.widget." + root2 + ".instanceOf.widget." + shared, target: sharedKey}},
		sharedKey: {{linkKey: "lnk.widget." + shared + ".instanceOf.meta." + metaID, target: "vtx.meta." + metaID}},
	}}
	resolver := &ddlResolver{DDLs: cache, linkReader: fake}
	memo := &ddlResolutionMemo{}
	state := HydratedState{Context: ScriptContext{DDLResolutionMemo: memo}}

	ref1, ok1 := resolver.resolveGoverningDDL(ctx, "unregistered.class.a", root1Key, substrate.KindVertex, ScriptResult{}, state)
	ref2, ok2 := resolver.resolveGoverningDDL(ctx, "unregistered.class.b", root2Key, substrate.KindVertex, ScriptResult{}, state)

	if !ok1 || ref1.CanonicalName != "sharedType" {
		t.Fatalf("root1 resolve: ok=%v ref=%+v, want sharedType", ok1, ref1)
	}
	if !ok2 || ref2.CanonicalName != "sharedType" {
		t.Fatalf("root2 resolve: ok=%v ref=%+v, want sharedType", ok2, ref2)
	}
	if fake.calls[sharedKey] != 1 {
		t.Fatalf("calls[shared] = %d, want 1 — the shared intermediate node must resolve once across two walks", fake.calls[sharedKey])
	}
	if fake.calls[root1Key] != 1 || fake.calls[root2Key] != 1 {
		t.Fatalf("calls[root1]=%d calls[root2]=%d, want 1 each — each walk's own distinct first hop is unavoidable", fake.calls[root1Key], fake.calls[root2Key])
	}
}

// TestDDLResolutionMemo_SameClassDifferentEdgesResolveDifferently pins that
// the memo is keyed on the WALK NODE, never the class: two vertices sharing
// one class but pointing at different instanceOf targets must resolve to
// DIFFERENT governing DDLs in the same execution — a class-keyed memo would
// collapse them incorrectly.
func TestDDLResolutionMemo_SameClassDifferentEdgesResolveDifferently(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	vtxA, vtxB, targetX, targetY := mustNanoID(t), mustNanoID(t), mustNanoID(t), mustNanoID(t)
	vtxAKey := "vtx.widget." + vtxA
	vtxBKey := "vtx.widget." + vtxB
	targetXKey := "vtx.meta." + targetX
	targetYKey := "vtx.meta." + targetY
	seedVertexTypeDDLAs(t, ctx, conn, targetX, "typeX", `["X"]`)
	seedVertexTypeDDLAs(t, ctx, conn, targetY, "typeY", `["Y"]`)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	fake := &countingLinkReader{edges: map[string][]instanceOfEdge{
		vtxAKey: {{linkKey: "lnk.widget." + vtxA + ".instanceOf.meta." + targetX, target: targetXKey}},
		vtxBKey: {{linkKey: "lnk.widget." + vtxB + ".instanceOf.meta." + targetY, target: targetYKey}},
	}}
	resolver := &ddlResolver{DDLs: cache, linkReader: fake}
	state := HydratedState{Context: ScriptContext{DDLResolutionMemo: &ddlResolutionMemo{}}}

	refA, okA := resolver.resolveGoverningDDL(ctx, "shared.class", vtxAKey, substrate.KindVertex, ScriptResult{}, state)
	refB, okB := resolver.resolveGoverningDDL(ctx, "shared.class", vtxBKey, substrate.KindVertex, ScriptResult{}, state)

	if !okA || refA.CanonicalName != "typeX" {
		t.Fatalf("vtxA (shared.class) resolve: ok=%v ref=%+v, want typeX", okA, refA)
	}
	if !okB || refB.CanonicalName != "typeY" {
		t.Fatalf("vtxB (shared.class) resolve: ok=%v ref=%+v, want typeY — must NOT collapse to vtxA's typeX", okB, refB)
	}
}

// TestDDLResolutionMemo_BatchTombstoneHonoredAfterMemoWarms pins the
// ordering row: the batch/working-set layers are re-consulted FRESH on
// every call, memo hit or not. A first resolution warms the memo with a
// live edge; a second resolution of the SAME node, now carrying a batch
// mutation that tombstones that exact link, must NOT reuse the warm memo's
// positive answer — the fresh batchDead exclusion must still apply.
func TestDDLResolutionMemo_BatchTombstoneHonoredAfterMemoWarms(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root, metaID := mustNanoID(t), mustNanoID(t)
	rootKey := "vtx.widget." + root
	targetKey := "vtx.meta." + metaID
	linkKey := "lnk.widget." + root + ".instanceOf.meta." + metaID
	seedVertexTypeDDLAs(t, ctx, conn, metaID, "typeZ", `["Z"]`)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	fake := &countingLinkReader{edges: map[string][]instanceOfEdge{
		rootKey: {{linkKey: linkKey, target: targetKey}},
	}}
	resolver := &ddlResolver{DDLs: cache, linkReader: fake}
	memo := &ddlResolutionMemo{}
	state := HydratedState{Context: ScriptContext{DDLResolutionMemo: memo}}

	// First call: no batch, resolves live and warms the memo.
	ref1, ok1 := resolver.resolveGoverningDDL(ctx, "unregistered.class", rootKey, substrate.KindVertex, ScriptResult{}, state)
	if !ok1 || ref1.CanonicalName != "typeZ" {
		t.Fatalf("first resolve: ok=%v ref=%+v, want typeZ", ok1, ref1)
	}
	if fake.calls[rootKey] != 1 {
		t.Fatalf("calls[root] after first resolve = %d, want 1", fake.calls[rootKey])
	}

	// Second call: SAME node, but the batch now tombstones the very link the
	// memo cached. The memo must not short-circuit past this.
	tombstoningBatch := ScriptResult{Mutations: []MutationOp{{Op: "tombstone", Key: linkKey}}}
	ref2, ok2 := resolver.resolveGoverningDDL(ctx, "unregistered.class", rootKey, substrate.KindVertex, tombstoningBatch, state)
	if ok2 {
		t.Fatalf("second resolve: ok=%v ref=%+v, want no resolution — the batch tombstone must win even with a warm memo", ok2, ref2)
	}
	// A tombstone-only mutation leaves batchLive empty (reconcileBatchInstanceOf
	// only returns a NET-LIVE edge there), so this resolves via the memo HIT
	// at the live layer, filtered by THIS call's fresh excludeDead — not via
	// the batch-layer early-return at the top of instanceOfTargetOf. Either
	// way the fake must not be re-consulted: the round trip is what the memo
	// exists to skip.
	if fake.calls[rootKey] != 1 {
		t.Fatalf("calls[root] after second resolve = %d, want still 1 — no live re-read needed once the batch names the tombstone", fake.calls[rootKey])
	}
}

// TestDDLResolutionMemo_ExcludeDeadDoesNotCorruptStoredEdges is a regression
// test guarding excludeDead against compacting its input slice IN PLACE
// (out := edges[:0]): the memo hands out the exact slice a live read
// returned, so filtering it against a batch tombstone would silently
// overwrite the memo's own backing array. Two live edges, tombstone ONE of
// them, resolve twice with the SAME batch: the first call filters and
// resolves cleanly to the survivor; if that filter corrupted the memo's
// array, the identical second call would see a duplicated/wrong edge set and
// resolve AMBIGUOUS instead of the same clean answer — the exact divergence
// that would silently disable a step 6.5 sensitivity gate on a repeat call.
func TestDDLResolutionMemo_ExcludeDeadDoesNotCorruptStoredEdges(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	root, metaA, metaB := mustNanoID(t), mustNanoID(t), mustNanoID(t)
	rootKey := "vtx.widget." + root
	linkA := "lnk.widget." + root + ".instanceOf.meta." + metaA
	linkB := "lnk.widget." + root + ".instanceOf.meta." + metaB
	seedVertexTypeDDLAs(t, ctx, conn, metaA, "typeA", `["A"]`)
	seedVertexTypeDDLAs(t, ctx, conn, metaB, "typeB", `["B"]`)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	fake := &countingLinkReader{edges: map[string][]instanceOfEdge{
		rootKey: {
			{linkKey: linkA, target: "vtx.meta." + metaA},
			{linkKey: linkB, target: "vtx.meta." + metaB},
		},
	}}
	resolver := &ddlResolver{DDLs: cache, linkReader: fake}
	memo := &ddlResolutionMemo{}
	state := HydratedState{Context: ScriptContext{DDLResolutionMemo: memo}}
	tombstoneA := ScriptResult{Mutations: []MutationOp{{Op: "tombstone", Key: linkA}}}

	// Call 1: cold memo, live read returns [A,B]; the batch tombstones A →
	// filtered to just B → resolves cleanly. This is the call whose
	// excludeDead invocation used to corrupt the memo's backing array.
	ref1, ok1 := resolver.resolveGoverningDDL(ctx, "unregistered.class", rootKey, substrate.KindVertex, tombstoneA, state)
	if !ok1 || ref1.CanonicalName != "typeB" {
		t.Fatalf("call 1: ok=%v ref=%+v, want typeB (linkA tombstoned, only B live)", ok1, ref1)
	}

	// Call 2: the IDENTICAL batch tombstone again, warm memo. A correct memo
	// resolves to typeB again; a corrupted one resolves ambiguous instead.
	ref2, ok2 := resolver.resolveGoverningDDL(ctx, "unregistered.class", rootKey, substrate.KindVertex, tombstoneA, state)
	if !ok2 || ref2.CanonicalName != "typeB" {
		t.Fatalf("call 2: ok=%v ref=%+v, want typeB again — a memo corrupted by call 1's excludeDead would resolve ambiguous here", ok2, ref2)
	}
	if fake.calls[rootKey] != 1 {
		t.Fatalf("calls[root] = %d, want 1 — both calls must share the one memoized read", fake.calls[rootKey])
	}
}
