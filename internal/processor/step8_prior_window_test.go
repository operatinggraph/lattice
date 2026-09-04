package processor

import (
	"errors"
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
