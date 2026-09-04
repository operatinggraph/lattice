package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// storedDoc is a decoded stored body for a hand-built prior map — the shape
// step 5.5's read produces for a key Core KV holds.
func storedDoc(class string) priorDoc {
	doc := map[string]interface{}{"isDeleted": false}
	if class != "" {
		doc["class"] = class
	}
	return priorDoc{Doc: doc, Revision: 7, Found: true}
}

func expectPermittedCommandsViolation(t *testing.T, err error, key string) {
	t.Helper()
	var ddlErr *DDLViolation
	if !errors.As(err, &ddlErr) {
		t.Fatalf("expected *DDLViolation, got %T: %v", err, err)
	}
	if ddlErr.ViolatedConstraint != "permittedCommands" {
		t.Fatalf("ViolatedConstraint = %q, want permittedCommands", ddlErr.ViolatedConstraint)
	}
	if ddlErr.MutationKey != key {
		t.Fatalf("MutationKey = %q, want %q", ddlErr.MutationKey, key)
	}
}

// A bare tombstone carries no document at all, so the class stored at the key
// is the only class it has, and it is what admits or refuses the operation. An
// absent key stays permissive (the meta-vertex tombstone cascade depends on it).
func TestValidate_TombstoneGovernedByStoredClass(t *testing.T) {
	t.Parallel()
	v, _, ctx := buildValidatorWithCache(t)
	key := "vtx.identity." + testNanoID2
	// The harness's identity DDL permits CreateIdentity and nothing else.
	prior := PriorDocs{key: storedDoc("identity")}
	tombstone := ScriptResult{Mutations: []MutationOp{{Op: "tombstone", Key: key}}}

	denied := newTestEnvelope(testNanoID1)
	denied.OperationType = "DeleteIdentity"
	expectPermittedCommandsViolation(t, v.Validate(ctx, denied, tombstone, HydratedState{}, prior), key)

	admitted := newTestEnvelope(testNanoID1) // OperationType CreateIdentity
	if err := v.Validate(ctx, admitted, tombstone, HydratedState{}, prior); err != nil {
		t.Fatalf("a tombstone by an admitted operation must pass: %v", err)
	}

	if err := v.Validate(ctx, denied, tombstone, HydratedState{}, PriorDocs{}); err != nil {
		t.Fatalf("a tombstone of an absent key must stay permissive: %v", err)
	}
}

// The stored-class walk is the gate's own resolution, not the script's, so it
// must resolve a chain-typed entity even out of an execution whose live-read
// budget the script has already spent. Otherwise a submitter switches the gate
// off by exhausting its own allowance first.
func TestValidate_StoredClassWalkIsOffTheScriptsBudget(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", instID, svcTypeID), false)
	key := "vtx.widget." + instID
	// widget.deluxe.instance has no DDL of its own; only the chain reaches the
	// widget type authority, and only a live read reaches the chain.
	prior := PriorDocs{key: storedDoc("widget.deluxe.instance")}
	tombstone := ScriptResult{Mutations: []MutationOp{{Op: "tombstone", Key: key}}}
	exhausted := func() HydratedState {
		return HydratedState{Context: ScriptContext{LiveReads: &liveReadBudgetTracker{budget: 0}}}
	}

	denied := newTestEnvelope(testNanoID1)
	denied.OperationType = "DeleteWidgetInstance"
	expectPermittedCommandsViolation(t, v.Validate(ctx, denied, tombstone, exhausted(), prior), key)

	admitted := newTestEnvelope(testNanoID1)
	admitted.OperationType = "CreateWidgetInstance"
	if err := v.Validate(ctx, admitted, tombstone, exhausted(), prior); err != nil {
		t.Fatalf("the same off-budget walk must ADMIT the permitted operation: %v", err)
	}
}

// A fault at ANY hop of the chain walk stops it. The hop the gate could not
// read may itself be the type authority, and a CLOSER authority is the one
// whose permittedCommands governs; walking past it lets a FARTHER one — which
// may admit exactly what the closer one refuses — decide the verdict on the
// strength of a read that failed. That is fail-open, and non-deterministic with
// it, so the fault is retryable instead.
//
// The chain: vtx.widget.<inst> (stored class resolvable only through the chain)
// → vtx.widget.<tpl>, whose committed class IS registered DDL A and A refuses
// CreateWidgetInstance → vtx.meta.<B>, a vertexType DDL that ADMITS it.
func TestValidate_StoredClassFaultAtACloserHopIsRetryable(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	// DDL B, the FARTHER authority: admits CreateWidgetInstance.
	seedWidgetTypeDDL(t, ctx, conn)
	// DDL A, the CLOSER authority: the template's own committed class. Its one
	// permitted command is named by no other DDL here, so the two authorities
	// disagree about CreateWidgetInstance and about nothing else.
	seedVertexTypeDDLAs(t, ctx, conn, testMetaNanoID2, "widget.deluxe.template", `["ReviseWidgetTemplate"]`)
	cache := NewDDLCache(conn, testCoreBucket, testLogger())
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	v := NewValidator(cache, conn, testCoreBucket, testLogger())

	tplKey := "vtx.widget." + tplID
	seedCommittedVertexClass(t, ctx, conn, tplKey, "widget.deluxe.template")
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.widget.%s", instID, tplID), false)
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", tplID, svcTypeID), false)

	key := "vtx.widget." + instID
	prior := PriorDocs{key: storedDoc("widget.deluxe.instance")}
	tombstone := ScriptResult{Mutations: []MutationOp{{Op: "tombstone", Key: key}}}

	// The positive vector, with no fault: the closer authority is reachable and
	// it REFUSES, so the walk never consults the admitting one behind it.
	denied := newTestEnvelope(testNanoID1)
	denied.OperationType = "CreateWidgetInstance"
	expectPermittedCommandsViolation(t, v.Validate(ctx, denied, tombstone, HydratedState{}, prior), key)

	// Now the read of that closer authority's class faults. The answer must be
	// "come back later", never the admission the farther DDL would have given.
	v.classReader = errClassReader{}
	err := v.Validate(ctx, denied, tombstone, HydratedState{}, prior)
	var faultErr *ResolveFaultError
	if !errors.As(err, &faultErr) {
		t.Fatalf("a fault at the closer hop must surface as *ResolveFaultError, got %T: %v", err, err)
	}
	if faultErr.MutationKey != key || faultErr.Class != "widget.deluxe.instance" {
		t.Fatalf("fault names key %q class %q", faultErr.MutationKey, faultErr.Class)
	}
	// (TestValidate_StoredClassReadFaultIsRetryable pins that a
	// *ResolveFaultError leaves the commit path as OutcomeRetryable with nothing
	// committed.)
}

// errClassReader fails every committed class read, so a walk that consults it
// records a fault and stops at that hop.
type errClassReader struct{}

func (errClassReader) ClassOf(context.Context, string, *liveReadBudgetTracker) (string, bool, error) {
	return "", false, errors.New("injected class-read fault")
}

// countingClassReader answers a fixed class per key and counts the round trips,
// so a test can assert how many times one node was asked.
type countingClassReader struct {
	class map[string]string
	calls map[string]int
}

func (f *countingClassReader) ClassOf(_ context.Context, key string, _ *liveReadBudgetTracker) (string, bool, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[key]++
	class, ok := f.class[key]
	return class, ok, nil
}

// A cascade's mutations share their chain terminal — one template every subtype
// instance points at — and the terminal's own class is the read that says
// whether it is a registered type authority. Memoized per node, that read is
// paid once for the whole batch; unmemoized it is paid once per mutation, which
// is the shape the instanceOf edge memo already collapses on the other half of
// the same walk.
func TestValidate_StoredClassTerminalClassReadIsMemoisedPerNode(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	tplKey := "vtx.widget." + tplID
	reader := &countingClassReader{class: map[string]string{tplKey: "widget"}}
	v.classReader = reader

	prior := PriorDocs{}
	var muts []MutationOp
	for i := 0; i < 5; i++ {
		id := mustNanoID(t)
		seedCommittedLink(t, ctx, conn,
			fmt.Sprintf("lnk.widget.%s.instanceOf.widget.%s", id, tplID), false)
		key := "vtx.widget." + id
		prior[key] = storedDoc("widget.deluxe.instance")
		muts = append(muts, MutationOp{Op: "tombstone", Key: key})
	}
	state := HydratedState{Context: ScriptContext{DDLResolutionMemo: &ddlResolutionMemo{}}}

	admitted := newTestEnvelope(testNanoID1)
	admitted.OperationType = "CreateWidgetInstance"
	if err := v.Validate(ctx, admitted, ScriptResult{Mutations: muts}, state, prior); err != nil {
		t.Fatalf("five tombstones the widget DDL admits must validate: %v", err)
	}
	if got := reader.calls[tplKey]; got != 1 {
		t.Fatalf("calls[terminal] = %d, want 1 — five mutations sharing one chain terminal must ask its class once", got)
	}

	// The positive vector: the gate really did resolve through that terminal, so
	// the single read is a memo hit and not a seam nobody used.
	denied := newTestEnvelope(testNanoID1)
	denied.OperationType = "DeleteWidgetInstance"
	expectPermittedCommandsViolation(t,
		v.Validate(ctx, denied, ScriptResult{Mutations: muts}, state, prior), muts[0].Key)
}

// An update is governed by BOTH classes: the one stored at the key (the entity
// it rewrites) and the one its own document declares (the entity it writes).
// Contract #1 §1.3 makes class mutable, so a re-typing must satisfy both.
func TestValidate_UpdateGovernedByStoredAndDeclaredClass(t *testing.T) {
	t.Parallel()

	t.Run("the stored class governs a re-typing the declared class cannot", func(t *testing.T) {
		v, _, ctx := buildValidatorWithCache(t)
		key := "vtx.identity." + testNanoID2
		prior := PriorDocs{key: storedDoc("identity")}
		retype := ScriptResult{Mutations: []MutationOp{{
			Op:  "update",
			Key: key,
			// "foo" resolves to no DDL at all, so the declared-class checks are
			// permissive — the stored class is the only thing standing here.
			Document: map[string]interface{}{"class": "foo", "isDeleted": false, "data": map[string]interface{}{}},
		}}}

		denied := newTestEnvelope(testNanoID1)
		denied.OperationType = "DeleteIdentity"
		expectPermittedCommandsViolation(t, v.Validate(ctx, denied, retype, HydratedState{}, prior), key)

		admitted := newTestEnvelope(testNanoID1) // CreateIdentity, which identity admits
		if err := v.Validate(ctx, admitted, retype, HydratedState{}, prior); err != nil {
			t.Fatalf("a re-typing by an operation the stored class admits must pass: %v", err)
		}

		sameClass := ScriptResult{Mutations: []MutationOp{{
			Op:       "update",
			Key:      key,
			Document: map[string]interface{}{"class": "identity", "isDeleted": false, "data": map[string]interface{}{}},
		}}}
		if err := v.Validate(ctx, admitted, sameClass, HydratedState{}, prior); err != nil {
			t.Fatalf("a same-class update by an admitted operation must pass: %v", err)
		}
		expectPermittedCommandsViolation(t, v.Validate(ctx, denied, sameClass, HydratedState{}, prior), key)
	})

	t.Run("the declared-class walk on an update runs off the script's live-read budget", func(t *testing.T) {
		v, ctx, conn := buildWidgetValidator(t)
		seedCommittedLink(t, ctx, conn,
			fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", instID, svcTypeID), false)
		key := "vtx.widget." + instID
		// The stored body carries no class, so nothing but the DECLARED class —
		// widget.deluxe.instance, resolvable only through the instanceOf chain —
		// can govern this mutation.
		prior := PriorDocs{key: storedDoc("")}
		result := ScriptResult{Mutations: []MutationOp{instanceVertexMut("update", instID)}}
		exhausted := func() HydratedState {
			return HydratedState{Context: ScriptContext{LiveReads: &liveReadBudgetTracker{budget: 0}}}
		}

		denied := newTestEnvelope(testNanoID1)
		denied.OperationType = "DeleteWidgetInstance"
		expectPermittedCommandsViolation(t, v.Validate(ctx, denied, result, exhausted(), prior), key)

		admitted := newTestEnvelope(testNanoID1)
		admitted.OperationType = "CreateWidgetInstance"
		if err := v.Validate(ctx, admitted, result, exhausted(), prior); err != nil {
			t.Fatalf("the same off-budget walk must ADMIT the permitted operation: %v", err)
		}
	})
}

// The type authority of a STORED class is a fact about the committed graph: a
// batch that tombstones the entity's own instanceOf link in the same breath as
// mutating it must not thereby un-type it for the gate.
func TestValidate_StoredClassAuthorityIgnoresBatchInstanceOfTombstone(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	linkKey := fmt.Sprintf("lnk.widget.%s.instanceOf.meta.%s", instID, svcTypeID)
	seedCommittedLink(t, ctx, conn, linkKey, false)

	key := "vtx.widget." + instID
	prior := PriorDocs{key: storedDoc("widget.deluxe.instance")}
	// The root update declares NO class, so the refusal can only come from the
	// stored-class walk; and the batch tombstones the very link that walk reads.
	bundled := ScriptResult{Mutations: []MutationOp{
		{
			Op:       "update",
			Key:      key,
			Document: map[string]interface{}{"isDeleted": false, "data": map[string]interface{}{}},
		},
		{Op: "tombstone", Key: linkKey},
	}}

	denied := newTestEnvelope(testNanoID1)
	denied.OperationType = "DeleteWidgetInstance" // widget does not admit it
	expectPermittedCommandsViolation(t, v.Validate(ctx, denied, bundled, HydratedState{}, prior), key)

	admitted := newTestEnvelope(testNanoID1)
	admitted.OperationType = "CreateWidgetInstance"
	if err := v.Validate(ctx, admitted, bundled, HydratedState{}, prior); err != nil {
		t.Fatalf("the committed authority must ADMIT the permitted operation: %v", err)
	}
}

// A live-read fault leaves the resolution DEGRADED, not empty. It must not
// reach the permissive default (a fault would then decide an entity is
// ungoverned) and must not terminate the operation either (a transient blip
// would permanently reject a valid op) — it is retryable.
func TestValidate_StoredClassReadFaultIsRetryable(t *testing.T) {
	t.Parallel()

	t.Run("a chain read fault", func(t *testing.T) {
		v, ctx, _ := buildWidgetValidator(t)
		v.linkReader = errLinkReader{}
		key := "vtx.widget." + instID
		prior := PriorDocs{key: storedDoc("widget.deluxe.instance")}
		result := ScriptResult{Mutations: []MutationOp{{Op: "tombstone", Key: key}}}
		env := newTestEnvelope(testNanoID1)
		env.OperationType = "DeleteWidgetInstance"

		err := v.Validate(ctx, env, result, HydratedState{}, prior)
		var faultErr *ResolveFaultError
		if !errors.As(err, &faultErr) {
			t.Fatalf("a degraded stored-class resolution must surface as *ResolveFaultError, got %T: %v", err, err)
		}
		var ddlErr *DDLViolation
		if errors.As(err, &ddlErr) {
			t.Fatalf("a read fault must not be reported as a terminal DDLViolation: %v", err)
		}
		if faultErr.MutationKey != key || faultErr.Class != "widget.deluxe.instance" {
			t.Fatalf("fault names key %q class %q", faultErr.MutationKey, faultErr.Class)
		}
	})

	t.Run("a prior read fault redelivers", func(t *testing.T) {
		ctx, conn, _, _, _ := setupTestPipeline(t)
		committer := &faultingPriorCommitter{err: errors.New("core kv unreachable")}
		cp, cons := newInjectedCommitterPipeline(t, ctx, conn, committer, "prior-fault")
		env := newTestEnvelope(testNanoID1)
		publishEnvelope(t, conn, env)
		driveOne(t, ctx, cp, cons, OutcomeRetryable)
		if committer.commits.Load() != 0 {
			t.Fatalf("a failed prior read must not reach the commit: %d commits", committer.commits.Load())
		}
	})

	// The chain fault reaches the same disposition through the commit path, not
	// only as a returned error type: a *ResolveFaultError must leave step 6 as a
	// NakWithDelay redelivery with nothing committed, never as a rejection reply
	// and never as a permissive pass-through into the batch.
	t.Run("a chain read fault redelivers through the commit path", func(t *testing.T) {
		ctx, conn, _, _, _ := setupTestPipeline(t)
		seedWidgetTypeDDL(t, ctx, conn)
		key := "vtx.widget." + instID
		seedScriptSource(t, ctx, conn, "identity", `
def execute(state, op):
    return {"mutations": [{"op": "tombstone", "key": "`+key+`"}], "events": []}
`)
		logger := testLogger()
		cache := NewDDLCache(conn, testCoreBucket, logger)
		if err := cache.Refresh(ctx); err != nil {
			t.Fatalf("ddl cache refresh: %v", err)
		}
		validator := NewValidator(cache, conn, testCoreBucket, logger)
		validator.linkReader = errLinkReader{}
		// The stored class resolves only through the chain, and the chain read
		// is the one that faults.
		committer := &scriptedPriorCommitter{prior: PriorDocs{key: storedDoc("widget.deluxe.instance")}}
		cp, cons := newInjectedPipeline(t, ctx, conn, committer, validator, "chain-fault")

		env := newTestEnvelope(testNanoID1)
		env.OperationType = "DeleteWidgetInstance"
		publishEnvelope(t, conn, env)
		driveOne(t, ctx, cp, cons, OutcomeRetryable)
		if got := committer.commits.Load(); got != 0 {
			t.Fatalf("a degraded resolution must not reach the commit: %d commits", got)
		}
	})
}

// A stored body the gate cannot read splits by operation. An UPDATE of it is
// refused `storedClass`: rewriting content nothing can classify is not a write
// any DDL governs. A TOMBSTONE of it is admitted — it carries no document, the
// batch builder writes no readable content forward for it, and a corrupt key
// that could not be removed through the operation plane could not be removed at
// all. An absent class field is a third state again: permissive.
func TestValidate_CorruptStoredBodyIsRefused(t *testing.T) {
	t.Parallel()
	v, _, ctx := buildValidatorWithCache(t)
	key := "vtx.identity." + testNanoID2
	tombstone := ScriptResult{Mutations: []MutationOp{{Op: "tombstone", Key: key}}}
	update := ScriptResult{Mutations: []MutationOp{{
		Op:  "update",
		Key: key,
		// No class of its own, so the declared-class checks resolve nothing and
		// every refusal below belongs to the stored body.
		Document: map[string]interface{}{"isDeleted": false, "data": map[string]interface{}{}},
	}}}
	// An operation the identity DDL admits, so no refusal below can be the
	// permittedCommands gate wearing the corrupt body's clothes.
	env := newTestEnvelope(testNanoID1)

	corrupt := map[string]PriorDocs{
		"undecodable": {key: priorDoc{Revision: 3, Found: true}},
		"non-string class": {key: priorDoc{
			Doc:      map[string]interface{}{"class": float64(7), "isDeleted": false},
			Revision: 3,
			Found:    true,
		}},
	}
	for name, prior := range corrupt {
		t.Run("an update over an "+name+" body is refused", func(t *testing.T) {
			err := v.Validate(ctx, env, update, HydratedState{}, prior)
			var ddlErr *DDLViolation
			if !errors.As(err, &ddlErr) {
				t.Fatalf("expected *DDLViolation, got %T: %v", err, err)
			}
			if ddlErr.ViolatedConstraint != "storedClass" {
				t.Fatalf("ViolatedConstraint = %q, want storedClass", ddlErr.ViolatedConstraint)
			}
			if ddlErr.MutationKey != key {
				t.Fatalf("MutationKey = %q, want %q", ddlErr.MutationKey, key)
			}
		})
		t.Run("a tombstone over an "+name+" body is the heal path", func(t *testing.T) {
			if err := v.Validate(ctx, env, tombstone, HydratedState{}, prior); err != nil {
				t.Fatalf("a corrupt key must stay removable through the operation plane: %T %v", err, err)
			}
			// And the removal is not admitted merely because tombstones are
			// ungated: the same tombstone by an operation the stored class does
			// not admit is still refused when the body IS readable.
			denied := newTestEnvelope(testNanoID1)
			denied.OperationType = "DeleteIdentity"
			expectPermittedCommandsViolation(t,
				v.Validate(ctx, denied, tombstone, HydratedState{}, PriorDocs{key: storedDoc("identity")}), key)
		})
	}

	noClass := PriorDocs{key: storedDoc("")}
	denied := newTestEnvelope(testNanoID1)
	denied.OperationType = "DeleteIdentity"
	if err := v.Validate(ctx, denied, tombstone, HydratedState{}, noClass); err != nil {
		t.Fatalf("a stored body with no class field must stay permissive: %v", err)
	}
	if err := v.Validate(ctx, denied, update, HydratedState{}, noClass); err != nil {
		t.Fatalf("an update over a body with no class field must stay permissive: %v", err)
	}
}

// The key-shape verdict is reached before the first read. A key nats.go would
// refuse (ErrInvalidKey) must terminate as keyPattern, not enter the
// prior-document pass — where the error would read as a read fault and
// redeliver the same doomed operation forever.
func TestValidate_MalformedKeyRefusedBeforeAnyRead(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedScriptSource(t, ctx, conn, "identity", `
def execute(state, op):
    return {"mutations": [{"op": "tombstone", "key": "vtx.patient.a b.x"}], "events": []}
`)
	committer := &countingCommitter{}
	cp, cons := newInjectedCommitterPipeline(t, ctx, conn, committer, "malformed-key")
	env := newTestEnvelope(testNanoID1)
	sub := publishWithReply(t, conn, env)
	driveOne(t, ctx, cp, cons, OutcomeRejected)

	reply := awaitReply(t, sub)
	if reply.Error == nil || reply.Error.Code != ErrCodeDDLViolation {
		t.Fatalf("reply error = %+v, want a DDLViolation", reply.Error)
	}
	if got := reply.Error.Details["constraint"]; got != "keyPattern" {
		t.Fatalf("details.constraint = %v, want keyPattern", got)
	}
	if got := committer.reads.Load(); got != 0 {
		t.Fatalf("a malformed key must be refused before any read: %d prior reads", got)
	}
	if got := committer.commits.Load(); got != 0 {
		t.Fatalf("a malformed key must never reach the commit: %d commits", got)
	}
}

// A `class` a Go type assertion would read as absent must never commit: the
// stored-class gate reads that field back, so one admitted `{"class": 7}` would
// leave the entity ungoverned by the very gate that governs it, with no later
// mutation able to restore the governance. The verdict is reached in the same
// batch-wide pre-pass as the key shape, before the first prior read.
func TestValidate_ClassMustBeAStringBeforeAnyRead(t *testing.T) {
	t.Parallel()

	t.Run("the pre-pass refuses every non-string class, on a create and an update", func(t *testing.T) {
		t.Parallel()
		key := "vtx.identity." + testNanoID2
		bad := map[string]interface{}{
			"a number": float64(7),
			"null":     nil,
			"a bool":   true,
			"an object": map[string]interface{}{
				"canonicalName": "identity",
			},
		}
		for _, op := range []string{"create", "update"} {
			for name, value := range bad {
				muts := []MutationOp{{
					Op:       op,
					Key:      key,
					Document: map[string]interface{}{"class": value, "isDeleted": false},
				}}
				verr := validateMutationKeyShapes(muts, testNanoID1)
				if verr == nil {
					t.Fatalf("%s with %s class must be refused", op, name)
				}
				if verr.ViolatedConstraint != "classType" {
					t.Fatalf("%s with %s class: constraint = %q, want classType", op, name, verr.ViolatedConstraint)
				}
				if verr.MutationKey != key {
					t.Fatalf("%s with %s class: MutationKey = %q, want %q", op, name, verr.MutationKey, key)
				}
			}
			ok := []MutationOp{{
				Op:       op,
				Key:      key,
				Document: map[string]interface{}{"class": "identity", "isDeleted": false},
			}}
			if verr := validateMutationKeyShapes(ok, testNanoID1); verr != nil {
				t.Fatalf("%s with a string class must pass the pre-pass: %v", op, verr)
			}
		}
	})

	t.Run("the verdict precedes the prior-document read", func(t *testing.T) {
		t.Parallel()
		ctx, conn, _, _, _ := setupTestPipeline(t)
		seedScriptSource(t, ctx, conn, "identity", `
def execute(state, op):
    return {"mutations": [{"op": "create", "key": "vtx.identity.`+testNanoID2+`",
                           "document": {"class": 7, "isDeleted": False, "data": {}}}], "events": []}
`)
		committer := &countingCommitter{}
		cp, cons := newInjectedCommitterPipeline(t, ctx, conn, committer, "class-type")
		env := newTestEnvelope(testNanoID1)
		sub := publishWithReply(t, conn, env)
		driveOne(t, ctx, cp, cons, OutcomeRejected)

		reply := awaitReply(t, sub)
		if reply.Error == nil || reply.Error.Details["constraint"] != "classType" {
			t.Fatalf("reply error = %+v, want details.constraint classType", reply.Error)
		}
		if got := committer.reads.Load(); got != 0 {
			t.Fatalf("a non-string class must be refused before any read: %d prior reads", got)
		}
		if got := committer.commits.Load(); got != 0 {
			t.Fatalf("a non-string class must never reach the commit: %d commits", got)
		}
	})
}

// countingCommitter records how many times each stage was reached. It never
// reads or writes Core KV, so a test asserting "zero reads" is asserting over
// the seam the real Committer's KVGets sit behind.
type countingCommitter struct {
	reads   atomic.Uint64
	commits atomic.Uint64
}

func (c *countingCommitter) ReadPrior(_ context.Context, _ []MutationOp) (PriorDocs, error) {
	c.reads.Add(1)
	return PriorDocs{}, nil
}

func (c *countingCommitter) Commit(_ context.Context, _ *OperationEnvelope, _ ScriptResult, _ Tracker, _ PriorDocs) (CommitAck, error) {
	c.commits.Add(1)
	return CommitAck{}, nil
}

// faultingPriorCommitter fails the step-5.5 read, modelling a transient Core KV
// fault on the pass the gate and the batch builder share.
type faultingPriorCommitter struct {
	err     error
	commits atomic.Uint64
}

func (f *faultingPriorCommitter) ReadPrior(_ context.Context, _ []MutationOp) (PriorDocs, error) {
	return nil, f.err
}

func (f *faultingPriorCommitter) Commit(_ context.Context, _ *OperationEnvelope, _ ScriptResult, _ Tracker, _ PriorDocs) (CommitAck, error) {
	f.commits.Add(1)
	return CommitAck{}, nil
}

// scriptedPriorCommitter hands the pipeline a prior map the test wrote, so a
// pipeline-level test can put a chosen stored class in front of the step-6
// gate without seeding and mutating real Core KV documents.
type scriptedPriorCommitter struct {
	prior   PriorDocs
	commits atomic.Uint64
}

func (s *scriptedPriorCommitter) ReadPrior(_ context.Context, _ []MutationOp) (PriorDocs, error) {
	return s.prior, nil
}

func (s *scriptedPriorCommitter) Commit(_ context.Context, _ *OperationEnvelope, _ ScriptResult, _ Tracker, _ PriorDocs) (CommitAck, error) {
	s.commits.Add(1)
	return CommitAck{}, nil
}

// seedScriptSource replaces the script aspect of a class's DDL meta-vertex, so
// a pipeline test can drive an arbitrary mutation set through the real
// Hydrator, Executor and commit path.
func seedScriptSource(t *testing.T, ctx context.Context, conn *substrate.Conn, class, source string) {
	t.Helper()
	doc, err := json.Marshal(map[string]interface{}{
		"class":     "meta.script",
		"isDeleted": false,
		"data":      map[string]interface{}{"source": source},
	})
	if err != nil {
		t.Fatalf("marshal script aspect: %v", err)
	}
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta."+class+".script", doc); err != nil {
		t.Fatalf("seed script for %s: %v", class, err)
	}
}

// newInjectedCommitterPipeline wires the real Hydrator/Executor/Validator with
// a caller-supplied Committer, on its own durable so it never drains another
// test's message.
func newInjectedCommitterPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, committer Committer, durableSuffix string) (*CommitPath, jetstream.Consumer) {
	t.Helper()
	return newInjectedPipeline(t, ctx, conn, committer, nil, durableSuffix)
}

// newInjectedPipeline is newInjectedCommitterPipeline with the Validator open
// too, so a test can drive the real commit path against a resolver whose
// readers it has replaced. A nil validator wires the real one.
func newInjectedPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, committer Committer, validator Validator, durableSuffix string) (*CommitPath, jetstream.Consumer) {
	t.Helper()
	logger := testLogger()
	metrics := &Metrics{}
	cache := NewDDLCache(conn, testCoreBucket, logger)
	if err := cache.Refresh(ctx); err != nil {
		t.Fatalf("ddl cache refresh: %v", err)
	}
	if validator == nil {
		validator = NewValidator(cache, conn, testCoreBucket, logger)
	}
	cp := NewCommitPath(Deps{
		Conn:        conn,
		CoreBucket:  testCoreBucket,
		HealthKV:    testHealthBucket,
		Authorizer:  NewStubAuthorizer(logger),
		Hydrator:    NewHydratorWithCache(conn, testCoreBucket, cache, logger),
		Executor:    NewExecutor(NewStarlarkRunner(0, 0), logger),
		Validator:   validator,
		Committer:   committer,
		Metrics:     metrics,
		Heartbeater: NewHealthHeartbeater(conn, testHealthBucket, "proc-test-"+durableSuffix, 10*time.Second, metrics, logger),
		Logger:      logger,
	})
	cons, err := EnsureConsumer(ctx, conn.JetStream(), ConsumerConfig{
		StreamName:     testStream,
		Durable:        testDurable + "-" + durableSuffix,
		FilterSubjects: []string{"ops.default"},
		AckWait:        2 * time.Second,
	}, logger)
	if err != nil {
		t.Fatalf("EnsureConsumer: %v", err)
	}
	return cp, cons
}

// The type authority of a STORED class is a fact about the committed graph at
// every hop of the walk, not just the first. A batch that re-types the chain
// TERMINAL — the template a subtype instance resolves through — must not
// thereby un-type the instance for the gate.
func TestValidate_StoredClassAuthorityIgnoresBatchTerminalRetype(t *testing.T) {
	t.Parallel()
	v, ctx, conn := buildWidgetValidator(t)
	// Committed: the instance points at a template whose OWN class is the
	// registered `widget` DDL — the one-hop classOf terminal shape.
	tplKey := "vtx.widget." + tplID
	seedCommittedVertexClass(t, ctx, conn, tplKey, "widget")
	seedCommittedLink(t, ctx, conn,
		fmt.Sprintf("lnk.widget.%s.instanceOf.widget.%s", instID, tplID), false)

	key := "vtx.widget." + instID
	prior := PriorDocs{key: storedDoc("widget.deluxe.instance")}
	// The batch re-types the terminal to a class no DDL knows, in the same
	// breath as removing the instance. Under the committed disposition the
	// terminal still answers `widget`, so the widget DDL still governs.
	bundled := ScriptResult{Mutations: []MutationOp{
		{
			Op:       "update",
			Key:      tplKey,
			Document: map[string]interface{}{"class": "unregistered", "isDeleted": false, "data": map[string]interface{}{}},
		},
		{Op: "tombstone", Key: key},
	}}

	denied := newTestEnvelope(testNanoID1)
	denied.OperationType = "DeleteWidgetInstance"
	expectPermittedCommandsViolation(t, v.Validate(ctx, denied, bundled, HydratedState{}, prior), key)

	admitted := newTestEnvelope(testNanoID1)
	admitted.OperationType = "CreateWidgetInstance"
	if err := v.Validate(ctx, admitted, bundled, HydratedState{}, prior); err != nil {
		t.Fatalf("the committed authority must ADMIT the permitted operation: %v", err)
	}
}

// NanoIDs for the kernel-typed roots a package cascade tombstones. Distinct
// from every other id in the package so a mis-scoped short-circuit cannot pass
// by colliding with a seeded key.
const (
	censusPermissionID = "Pv3nKt8wRb5mQx7jYd2c"
	censusRoleID       = "Rk9mXt4wNb6pQj3vZd8h"
	censusRoleIndexID  = "Zx2vNt7wKb4mRj9qYd6s"
)

// A kernel-typed key resolves its stored class by the exact lookup alone. The
// kernel types its own meta-vertices, permissions, roles and role indexes:
// none of the four is ever the source of an instanceOf edge (the two corpus
// censuses hold that closed), so a walk over one can only come back empty —
// while a package uninstall or upgrade, whose cascade is made almost entirely
// of exactly these types, would pay one KVGetMulti per root for the privilege.
func TestValidate_KernelTypedKeysSkipTheStoredClassChainWalk(t *testing.T) {
	t.Parallel()
	v, ctx, _ := buildWidgetValidator(t)
	reader := &countingLinkReader{}
	v.linkReader = reader

	root := "vtx.meta." + testMetaNanoID1
	permission := "vtx.permission." + censusPermissionID
	role := "vtx.role." + censusRoleID
	roleIndex := "vtx.roleindex." + censusRoleIndexID
	// None of permission/role/roleindex is a registered class, so each misses
	// the exact lookup — which is precisely what sends an un-short-circuited
	// root into the walk.
	prior := PriorDocs{
		root:             storedDoc("meta.ddl.vertexType"),
		root + ".script": storedDoc("meta.script"),
		permission:       storedDoc("permission"),
		role:             storedDoc("role"),
		roleIndex:        storedDoc("roleindex"),
	}
	cascade := ScriptResult{Mutations: []MutationOp{
		{Op: "tombstone", Key: root},
		{Op: "tombstone", Key: root + ".script"},
		{Op: "tombstone", Key: permission},
		{Op: "tombstone", Key: role},
		{Op: "tombstone", Key: roleIndex},
	}}

	env := newTestEnvelope(testNanoID1)
	env.OperationType = "UninstallPackage"
	if err := v.Validate(ctx, env, cascade, HydratedState{}, prior); err != nil {
		t.Fatalf("a kernel-typed tombstone cascade must validate: %v", err)
	}
	if got := chainReads(reader); got != 0 {
		t.Fatalf("a kernel-typed root's stored class must cost no chain read: %d instanceOf reads", got)
	}

	// The positive vector: a NON-kernel key on the same validator does reach the
	// reader, so the zero above is the short-circuit and not a dead seam.
	business := ScriptResult{Mutations: []MutationOp{{Op: "tombstone", Key: "vtx.widget." + instID}}}
	if err := v.Validate(ctx, env, business,
		HydratedState{}, PriorDocs{"vtx.widget." + instID: storedDoc("widget.deluxe.instance")}); err != nil {
		t.Fatalf("business tombstone: %v", err)
	}
	if got := chainReads(reader); got == 0 {
		t.Fatalf("a business root's stored class must reach the chain reader")
	}
}

// A link's stored class is an exact lookup and nothing more: vertexRootForResolve
// gives a link key no instanceOf-governed root, so the walk never starts. With
// no linkType DDL the class is ungoverned (Contract #1 §1.6's open posture, which
// the three shipped link DDLs all choose); with a closed permittedCommands the
// tombstone is held to it exactly as a vertex's is.
func TestValidate_LinkTombstoneStoredClassIsExactLookupOnly(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	seedWidgetTypeDDL(t, ctx, conn)
	linkKey := fmt.Sprintf("lnk.widget.%s.indexes.widget.%s", instID, tplID)
	tombstone := ScriptResult{Mutations: []MutationOp{{Op: "tombstone", Key: linkKey}}}
	prior := PriorDocs{linkKey: storedDoc("indexes")}

	env := newTestEnvelope(testNanoID1)
	env.OperationType = "DeleteWidgetInstance"

	t.Run("no linkType DDL leaves the class ungoverned", func(t *testing.T) {
		cache := NewDDLCache(conn, testCoreBucket, testLogger())
		if err := cache.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		v := NewValidator(cache, conn, testCoreBucket, testLogger())
		reader := &countingLinkReader{}
		v.linkReader = reader
		if err := v.Validate(ctx, env, tombstone, HydratedState{}, prior); err != nil {
			t.Fatalf("a link class with no DDL must stay permissive: %v", err)
		}
		if got := chainReads(reader); got != 0 {
			t.Fatalf("a link's stored class must cost no chain read: %d instanceOf reads", got)
		}
	})

	t.Run("a closed linkType DDL refuses the tombstone", func(t *testing.T) {
		seedLinkTypeDDL(t, ctx, conn, "indexes", []string{"CreateWidgetInstance"})
		cache := NewDDLCache(conn, testCoreBucket, testLogger())
		if err := cache.Refresh(ctx); err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		v := NewValidator(cache, conn, testCoreBucket, testLogger())
		reader := &countingLinkReader{}
		v.linkReader = reader
		expectPermittedCommandsViolation(t, v.Validate(ctx, env, tombstone, HydratedState{}, prior), linkKey)
		if got := chainReads(reader); got != 0 {
			t.Fatalf("the refusal must come from the exact lookup, not a walk: %d instanceOf reads", got)
		}

		admitted := newTestEnvelope(testNanoID1)
		admitted.OperationType = "CreateWidgetInstance"
		if err := v.Validate(ctx, admitted, tombstone, HydratedState{}, prior); err != nil {
			t.Fatalf("the permitted operation must be admitted: %v", err)
		}
	})
}

// chainReads totals the on-demand instanceOf reads a walk made, across nodes.
func chainReads(r *countingLinkReader) int {
	n := 0
	for _, c := range r.calls {
		n += c
	}
	return n
}

// seedCommittedVertexClass writes a minimal committed vertex envelope carrying
// only the class — what the on-demand classOf read consumes.
func seedCommittedVertexClass(t *testing.T, ctx context.Context, conn substrateConn, key, class string) {
	t.Helper()
	doc := []byte(fmt.Sprintf(`{"class":%q,"isDeleted":false,"data":{}}`, class))
	if _, err := conn.KVPut(ctx, testCoreBucket, key, doc); err != nil {
		t.Fatalf("seed vertex %s: %v", key, err)
	}
}

// seedLinkTypeDDL registers a linkType DDL meta-vertex for a link class.
func seedLinkTypeDDL(t *testing.T, ctx context.Context, conn substrateConn, class string, permitted []string) {
	t.Helper()
	pc, err := json.Marshal(permitted)
	if err != nil {
		t.Fatalf("marshal permittedCommands: %v", err)
	}
	doc := []byte(fmt.Sprintf(
		`{"class":"meta.ddl.linkType","isDeleted":false,"data":{"canonicalName":%q,"permittedCommands":%s}}`,
		class, pc))
	if _, err := conn.KVPut(ctx, testCoreBucket, "vtx.meta."+class, doc); err != nil {
		t.Fatalf("seed linkType DDL %s: %v", class, err)
	}
}
