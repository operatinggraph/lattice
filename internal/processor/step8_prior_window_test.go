package processor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// The step-5.5 prior pass reads every update/tombstone mutation key, and stores
// an entry for the ones Core KV does not hold. That entry is a fact about a
// moment, not a standing one: an absent key carries no revision, so the batch
// writes it unconditioned and nothing makes the commit conflict if the entity
// appears in between. The step-8 guards therefore cannot be served from it —
// they must ask again at commit time, the same as they do for a root.
//
// Here the batch tombstones an identity root that does not exist when the prior
// pass runs, and the kernel's own admin identity is created at that key while
// steps 6, 6.5 and 7 run. The tombstone must be refused, not committed over a
// stale "absent".
func TestCommit_RootCreatedProtectedInsideTheWindowIsRefused(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "vtx.identity." + testNanoID2
	mutations := []MutationOp{{Op: "tombstone", Key: key}}

	prior, err := c.ReadPrior(ctx, mutations)
	if err != nil {
		t.Fatalf("ReadPrior: %v", err)
	}
	pd, present := prior[key]
	if !present || pd.Found {
		t.Fatalf("the fixture needs an entry the pass read and did NOT find: present=%v %+v", present, pd)
	}

	// The window.
	seedIdentityRootProtection(t, ctx, c.Conn, key, true)

	env := newTestEnvelope(testNanoID1)
	env.RequestID = "rid-window-protected"
	_, err = c.Commit(ctx, env, ScriptResult{Mutations: mutations}, NewTracker(env, time.Now()), prior)
	var protErr *ProtectedKeyError
	if !errors.As(err, &protErr) {
		t.Fatalf("Commit over a root that turned protected in the window: %T %v, want *ProtectedKeyError", err, err)
	}
	if protErr.Key != key || protErr.Root != key || protErr.Op != "tombstone" {
		t.Fatalf("refusal names %+v, want the tombstoned root %q", protErr, key)
	}
	if after := readStoredDoc(t, ctx, c, key); after["isDeleted"] == true {
		t.Fatalf("the tombstone must not have landed: %v", after)
	}
}

// The sibling shape for the write-once guard, which reads the same map and has
// the same `!found → continue` arm: a permission root absent at step 5.5,
// created inside the window, then rewritten by the batch's own update with a
// changed provenance field. Served from the stale entry the guard would skip
// the key entirely and the rewrite would commit.
func TestCommit_PermissionRootCreatedInsideTheWindowStaysWriteOnce(t *testing.T) {
	t.Parallel()
	ctx, c, _ := buildCommitterPipeline(t)
	key := "vtx.permission.Hd4kRt7wZq2nBv9sMc1x"
	rewrite := MutationOp{
		Op:       "update",
		Key:      key,
		Document: permissionDoc("ApproveLease", "*", "package", "loftspace-domain", "rewritten"),
	}

	prior, err := c.ReadPrior(ctx, []MutationOp{rewrite})
	if err != nil {
		t.Fatalf("ReadPrior: %v", err)
	}
	if pd, present := prior[key]; !present || pd.Found {
		t.Fatalf("the fixture needs an entry the pass read and did NOT find: present=%v %+v", present, pd)
	}

	// The window: the permission arrives, declaring a different operationType.
	commitOne(t, ctx, c, "rid-window-perm-create", MutationOp{
		Op:       "create",
		Key:      key,
		Document: permissionDoc("DeleteLease", "*", "package", "loftspace-domain", ""),
	})

	env := newTestEnvelope(testNanoID1)
	env.RequestID = "rid-window-perm-rewrite"
	_, err = c.Commit(ctx, env, ScriptResult{Mutations: []MutationOp{rewrite}}, NewTracker(env, time.Now()), prior)
	var provErr *PermissionProvenanceError
	if !errors.As(err, &provErr) {
		t.Fatalf("Commit rewriting a permission that appeared in the window: %T %v, want *PermissionProvenanceError", err, err)
	}
	if provErr.Field != "operationType" {
		t.Fatalf("rejected on field %q, want operationType", provErr.Field)
	}
	after := readStoredDoc(t, ctx, c, key)
	data, _ := after["data"].(map[string]interface{})
	if data["operationType"] != "DeleteLease" {
		t.Fatalf("the stored permission must be untouched: %v", data)
	}
}

// The cost side of the same rule, asserted on the key selection itself rather
// than on a round-trip counter the concrete substrate connection does not
// expose: a mutation key the prior pass FOUND is covered, and so is its root
// when the root is that key, so Commit's own pass lists nothing. Only the
// absent entry is re-listed.
func TestCommitKeysFor_ReReadsOnlyTheEntriesThePassDidNotFind(t *testing.T) {
	t.Parallel()
	root := "vtx.identity." + testNanoID2
	aspect := root + ".state"
	mutations := []MutationOp{{Op: "tombstone", Key: root}, {Op: "update", Key: aspect}}

	found := PriorDocs{
		root:   priorDoc{Doc: map[string]interface{}{"class": "identity"}, Revision: 3, Found: true},
		aspect: priorDoc{Doc: map[string]interface{}{"class": "state"}, Revision: 4, Found: true},
	}
	if got := commitKeysFor(mutations, found); len(got) != 0 {
		t.Fatalf("commitKeysFor over a fully found map = %v, want no re-reads", got)
	}

	absent := PriorDocs{
		root:   priorDoc{},
		aspect: priorDoc{Doc: map[string]interface{}{"class": "state"}, Revision: 4, Found: true},
	}
	got := commitKeysFor(mutations, absent)
	if len(got) != 1 || got[0] != root {
		t.Fatalf("commitKeysFor = %v, want exactly the absent root %q re-read", got, root)
	}
}

// The write gate's own lost race. The step-6 stored-class gate reads the map
// the step-5.5 pass produced, so an entry that pass found ABSENT resolves no
// class and admits the mutation on the strength of there being nothing there.
// If the entity arrives before the batch does, that mutation would rewrite or
// remove an entity whose permittedCommands nothing ever checked — and the batch
// leaves an absent key unconditioned, so the substrate would not stop it
// either.
//
// So the commit conflicts, and the pipeline's own OCC retry re-runs hydrate →
// execute → 5.5 → 6 against a world where the key is FOUND. Attempt two reaches
// a verdict on the class actually stored there: refused when its DDL does not
// admit the operation, committed when it does. Both arms are driven through the
// real pipeline, so the conflict really does have to survive the commit path's
// structural attribution to become a retry.
func TestCommit_KeyCreatedInsideTheWindowConflictsAndIsRegatedOnRetry(t *testing.T) {
	t.Parallel()

	const targetID = "Tq4mKn8wRb3pXj6vZd9c"
	target := "vtx.widget." + targetID

	for _, tc := range []struct {
		name     string
		class    string
		durable  string
		outcome  MessageOutcome
		verified func(t *testing.T, reply OperationReply, stored map[string]interface{})
	}{
		{
			// `widget`'s DDL permits CreateWidgetTemplate / CreateWidgetInstance
			// / RecordWidgetOutcome — never the envelope's CreateIdentity.
			name:    "a stored class whose DDL refuses the operation",
			class:   "widget",
			durable: "window-regate-refused",
			outcome: OutcomeRejected,
			verified: func(t *testing.T, reply OperationReply, stored map[string]interface{}) {
				t.Helper()
				if reply.Error == nil || reply.Error.Code != ErrCodeDDLViolation {
					t.Fatalf("reply error = %+v, want a DDLViolation from the re-run gate", reply.Error)
				}
				if got := reply.Error.Details["constraint"]; got != "permittedCommands" {
					t.Fatalf("details.constraint = %v, want permittedCommands", got)
				}
				if stored["isDeleted"] == true {
					t.Fatalf("the tombstone must not have landed: %v", stored)
				}
			},
		},
		{
			// The harness's `identity` DDL permits exactly CreateIdentity.
			name:    "a stored class whose DDL admits it",
			class:   "identity",
			durable: "window-regate-admitted",
			outcome: OutcomeAccepted,
			verified: func(t *testing.T, reply OperationReply, stored map[string]interface{}) {
				t.Helper()
				if reply.Error != nil {
					t.Fatalf("reply error = %+v, want an accepted reply", reply.Error)
				}
				if stored["isDeleted"] != true {
					t.Fatalf("the tombstone must have landed: %v", stored)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn, _, _, _ := setupTestPipeline(t)
			seedWidgetTypeDDL(t, ctx, conn)
			seedScriptSource(t, ctx, conn, "identity", `
def execute(state, op):
    return {"mutations": [{"op": "tombstone", "key": "`+target+`"}], "events": []}
`)
			logger := testLogger()
			cache := NewDDLCache(conn, testCoreBucket, logger)
			if err := cache.Refresh(ctx); err != nil {
				t.Fatalf("ddl cache refresh: %v", err)
			}
			// The window: the entity arrives after the gate has read its
			// absence, on the first attempt only.
			gate := &windowCreatingValidator{
				inner: NewValidator(cache, conn, testCoreBucket, logger),
				arrive: func() {
					seedAspect(t, ctx, conn, target, tc.class)
				},
			}
			cp, cons := newInjectedPipeline(t, ctx, conn,
				NewCommitter(conn, testCoreBucket, cache, logger, time.Now, nil), gate, tc.durable)

			env := newTestEnvelope(testNanoID1)
			sub := publishWithReply(t, conn, env)
			driveOne(t, ctx, cp, cons, tc.outcome)

			if got := gate.calls.Load(); got != 2 {
				t.Fatalf("Validate calls = %d, want 2 (attempt one conflicts, attempt two is re-gated)", got)
			}
			tc.verified(t, awaitReply(t, sub), readStoredDocConn(t, ctx, conn, target))
		})
	}
}

// windowCreatingValidator is the real validator with one concurrent writer
// wedged into the window the step-5.5 pass opens: after the FIRST validation it
// creates the key the batch is about to write, so that attempt gated an absence
// the world has left behind.
type windowCreatingValidator struct {
	inner  Validator
	arrive func()
	calls  atomic.Uint64
}

func (w *windowCreatingValidator) Validate(ctx context.Context, env *OperationEnvelope, result ScriptResult, state HydratedState, prior PriorDocs) error {
	err := w.inner.Validate(ctx, env, result, state, prior)
	if w.calls.Add(1) == 1 && err == nil {
		w.arrive()
	}
	return err
}
