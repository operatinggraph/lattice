package processor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// recordingCommitter captures every ScriptResult handed to Commit and lets a
// test script the per-call error (e.g. an OCC conflict on the first attempt).
type recordingCommitter struct {
	calls   []ScriptResult
	errs    []error        // errs[i] returned on call i; nil/short → nil
	onCall  map[int]func() // side effect to run just before call i returns (models a concurrent transition)
	commits int
}

func (c *recordingCommitter) Commit(_ context.Context, _ *OperationEnvelope, result ScriptResult, _ Tracker) (CommitAck, error) {
	c.calls = append(c.calls, result)
	i := c.commits
	c.commits++
	if c.onCall != nil {
		if fn := c.onCall[i]; fn != nil {
			fn()
		}
	}
	if i < len(c.errs) && c.errs[i] != nil {
		return CommitAck{}, c.errs[i]
	}
	return CommitAck{}, nil
}

func acTestCommitPath(conn *substrate.Conn, committer Committer) *CommitPath {
	return &CommitPath{deps: Deps{
		Conn:       conn,
		CoreBucket: testCoreBucket,
		Committer:  committer,
		Logger:     testLogger(),
	}}
}

func acTaskPathPermission(taskKey string) *ResolvedPermission {
	return &ResolvedPermission{
		Path:           "task",
		EphemeralGrant: &EphemeralGrant{TaskKey: taskKey},
	}
}

// seedTaskRoot writes a task root vertex with the given status and returns its key.
func seedTaskRoot(t *testing.T, ctx context.Context, conn *substrate.Conn, id, status string) string {
	t.Helper()
	key := "vtx.task." + id
	doc := map[string]any{
		"class": "task", "isDeleted": false,
		"data": map[string]any{"status": status, "expiresAt": "2030-01-01T00:00:00Z"},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testCoreBucket, key, b); err != nil {
		t.Fatalf("seed task root: %v", err)
	}
	return key
}

func acUserResult() ScriptResult {
	return ScriptResult{
		Mutations:  []MutationOp{{Op: "update", Key: "vtx.leaseapp.ABCDEFGHJKMNPQRSTUVW", Document: map[string]any{"data": map[string]any{"state": "approved"}}}},
		Events:     []EventSpec{{Class: "loftspace.leaseApproved"}},
		PrimaryKey: "vtx.leaseapp.ABCDEFGHJKMNPQRSTUVW",
	}
}

func committedHasTaskCompletion(result ScriptResult, taskKey string) (hasMut, hasEvent bool) {
	for _, m := range result.Mutations {
		if m.Op == "update" && m.Key == taskKey {
			data, _ := m.Document["data"].(map[string]any)
			if s, _ := data["status"].(string); s == "complete" {
				hasMut = true
			}
		}
	}
	for _, e := range result.Events {
		if e.Class == "orchestration.taskCompleted" {
			if tk, _ := e.Data["taskKey"].(string); tk == taskKey {
				hasEvent = true
			}
		}
	}
	return
}

// TestAutoComplete_OpenTask_InjectsCompletion: a task-path op committing while
// the task is open injects the conditional status→complete mutation (with the
// CAS revision) + exactly one TaskCompleted into the SAME committed batch.
func TestAutoComplete_OpenTask_InjectsCompletion(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, conn := acConnect(t, url)
	taskKey := seedTaskRoot(t, ctx, conn, "AaOpenTaskJKMNPQRSTUV", "open")

	rc := &recordingCommitter{}
	cp := acTestCommitPath(conn, rc)
	_, err := cp.commitWithTaskAutoComplete(ctx, acEnv(), acUserResult(), Tracker{}, acTaskPathPermission(taskKey))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if rc.commits != 1 {
		t.Fatalf("expected exactly one commit, got %d", rc.commits)
	}
	committed := rc.calls[0]
	hasMut, hasEvent := committedHasTaskCompletion(committed, taskKey)
	if !hasMut || !hasEvent {
		t.Fatalf("open task: expected injected completion mutation+event; hasMut=%v hasEvent=%v muts=%+v", hasMut, hasEvent, committed.Mutations)
	}
	// CAS handle present on the injected update.
	for _, m := range committed.Mutations {
		if m.Key == taskKey && m.ExpectedRevision == nil {
			t.Fatalf("injected task update must carry an expectedRevision (CAS-on-open)")
		}
	}
	// The user's own effect rides the same batch.
	if !mutationKeyPresent(committed, "vtx.leaseapp.ABCDEFGHJKMNPQRSTUVW") {
		t.Fatalf("user mutation missing from committed batch")
	}
}

// TestAutoComplete_AlreadyComplete_NoInjection: a redelivery / raced admin
// CompleteTask left the task complete → no second TaskCompleted (CAS-on-open
// makes re-injection a no-op).
func TestAutoComplete_AlreadyComplete_NoInjection(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, conn := acConnect(t, url)
	taskKey := seedTaskRoot(t, ctx, conn, "AaDoneTaskJKMNPQRSTUV", "complete")

	rc := &recordingCommitter{}
	cp := acTestCommitPath(conn, rc)
	if _, err := cp.commitWithTaskAutoComplete(ctx, acEnv(), acUserResult(), Tracker{}, acTaskPathPermission(taskKey)); err != nil {
		t.Fatalf("commit: %v", err)
	}
	hasMut, hasEvent := committedHasTaskCompletion(rc.calls[0], taskKey)
	if hasMut || hasEvent {
		t.Fatalf("already-complete task must NOT be re-completed; hasMut=%v hasEvent=%v", hasMut, hasEvent)
	}
}

// TestAutoComplete_Cancelled_NotResurrected: a raced admin CancelTask left the
// task cancelled → the op still commits, the task is NOT resurrected, and no
// TaskCompleted is emitted.
func TestAutoComplete_Cancelled_NotResurrected(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, conn := acConnect(t, url)
	taskKey := seedTaskRoot(t, ctx, conn, "AaCancTaskJKMNPQRSTUV", "cancelled")

	rc := &recordingCommitter{}
	cp := acTestCommitPath(conn, rc)
	if _, err := cp.commitWithTaskAutoComplete(ctx, acEnv(), acUserResult(), Tracker{}, acTaskPathPermission(taskKey)); err != nil {
		t.Fatalf("commit: %v", err)
	}
	committed := rc.calls[0]
	hasMut, hasEvent := committedHasTaskCompletion(committed, taskKey)
	if hasMut || hasEvent {
		t.Fatalf("cancelled task must NOT be resurrected/completed; hasMut=%v hasEvent=%v", hasMut, hasEvent)
	}
	// The user's op still commits.
	if !mutationKeyPresent(committed, "vtx.leaseapp.ABCDEFGHJKMNPQRSTUVW") {
		t.Fatalf("user op must still commit when the task is cancelled")
	}
}

// TestAutoComplete_NonTaskPath_NoInjection: a role/platform-authorized op (no
// task path) injects nothing.
func TestAutoComplete_NonTaskPath_NoInjection(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, conn := acConnect(t, url)
	// Seed an open task that is NOT named by the (platform) permission.
	_ = seedTaskRoot(t, ctx, conn, "AaUnrelTaskJKMNPQRSTU", "open")

	rc := &recordingCommitter{}
	cp := acTestCommitPath(conn, rc)
	plat := &ResolvedPermission{Path: "platform", PlatformPermission: &PlatformPermission{OperationType: "X", Scope: "any"}}
	if _, err := cp.commitWithTaskAutoComplete(ctx, acEnv(), acUserResult(), Tracker{}, plat); err != nil {
		t.Fatalf("commit: %v", err)
	}
	committed := rc.calls[0]
	for _, m := range committed.Mutations {
		if m.Op == "update" && m.Document != nil {
			if data, ok := m.Document["data"].(map[string]any); ok {
				if s, _ := data["status"].(string); s == "complete" {
					t.Fatalf("non-task-path op must not inject any task completion")
				}
			}
		}
	}
	for _, e := range committed.Events {
		if e.Class == "orchestration.taskCompleted" {
			t.Fatalf("non-task-path op must not emit orchestration.taskCompleted")
		}
	}
}

// TestAutoComplete_OCCRace_StillOpen_RetriesSucceeds: the first commit loses
// the OCC race but the task is still open at a NEWER revision (a concurrent
// touch that did not close it). The injection re-reads the fresh CAS handle and
// the retry succeeds — the user's op is never bounced (Adjudication #2).
func TestAutoComplete_OCCRace_StillOpen_RetriesSucceeds(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, conn := acConnect(t, url)
	taskKey := seedTaskRoot(t, ctx, conn, "AaRcOpTaskJKMNPQRSTUV", "open")

	conflict := &ConflictError{ConflictingKey: taskKey, Cause: substrate.ErrAtomicBatchRejected}
	// As the first commit fails, the task root is rewritten (still open) so its
	// revision advances; the recheck sees open-at-newer-revision → retry.
	rc := &recordingCommitter{
		errs: []error{conflict},
		onCall: map[int]func(){
			0: func() { seedTaskRoot(t, ctx, conn, "AaRcOpTaskJKMNPQRSTUV", "open") },
		},
	}

	cp := acTestCommitPath(conn, rc)
	if _, err := cp.commitWithTaskAutoComplete(ctx, acEnv(), acUserResult(), Tracker{}, acTaskPathPermission(taskKey)); err != nil {
		t.Fatalf("a task-side OCC race must not fail the user's op, got: %v", err)
	}
	if rc.commits < 2 {
		t.Fatalf("expected a retry commit after the OCC conflict, got %d commits", rc.commits)
	}
	last := rc.calls[len(rc.calls)-1]
	if hasMut, hasEvent := committedHasTaskCompletion(last, taskKey); !hasMut || !hasEvent {
		t.Fatalf("retry must re-inject the completion at the fresh revision; hasMut=%v hasEvent=%v", hasMut, hasEvent)
	}
}

// TestAutoComplete_ConflictOnUserMutation_Surfaces: when the atomic-batch
// conflict is NOT on the injected task update (the task root is untouched at
// the asserted revision), it is a conflict on one of the USER's own mutations
// and is surfaced unchanged (the existing RevisionConflict branch handles it).
func TestAutoComplete_ConflictOnUserMutation_Surfaces(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, conn := acConnect(t, url)
	taskKey := seedTaskRoot(t, ctx, conn, "AaUsrCnflJKMNPQRSTUVW", "open")

	// The conflict names the user's leaseapp key, and the task root never moves,
	// so the recheck attributes the conflict to the user mutation → propagate.
	conflict := &ConflictError{ConflictingKey: "vtx.leaseapp.ABCDEFGHJKMNPQRSTUVW", Cause: substrate.ErrAtomicBatchRejected}
	rc := &recordingCommitter{errs: []error{conflict}}

	cp := acTestCommitPath(conn, rc)
	_, err := cp.commitWithTaskAutoComplete(ctx, acEnv(), acUserResult(), Tracker{}, acTaskPathPermission(taskKey))
	if err == nil {
		t.Fatalf("a conflict on the user's own mutation must surface, got nil")
	}
	var confErr *ConflictError
	if !errors.As(err, &confErr) {
		t.Fatalf("expected *ConflictError to propagate, got %T: %v", err, err)
	}
	// Only ONE commit attempt — the conflict is not on the auto-complete, so no
	// drop-and-retry-alone happens.
	if rc.commits != 1 {
		t.Fatalf("a user-mutation conflict must not trigger a drop-retry; commits=%d", rc.commits)
	}
}

// TestAutoComplete_OCCRace_TaskClosed_DropsInjection: when the first commit
// conflicts and the recheck finds the task closed (a concurrent admin
// transition), the injection is dropped and the user's op commits alone — no
// RevisionConflict surfaced to the user.
func TestAutoComplete_OCCRace_TaskClosed_DropsInjection(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, conn := acConnect(t, url)
	taskKey := seedTaskRoot(t, ctx, conn, "AaRcClTaskJKMNPQRSTUV", "open")

	conflict := &ConflictError{ConflictingKey: taskKey, Cause: substrate.ErrAtomicBatchRejected}
	// The first commit (with the injection) loses the OCC race; AS IT FAILS, a
	// concurrent admin CompleteTask closes the task — modeled by flipping the
	// root to complete right before the first call returns its conflict. The
	// recheck then sees a closed task and drops the injection.
	rc := &recordingCommitter{
		errs: []error{conflict},
		onCall: map[int]func(){
			0: func() { seedTaskRoot(t, ctx, conn, "AaRcClTaskJKMNPQRSTUV", "complete") },
		},
	}

	cp := acTestCommitPath(conn, rc)
	if _, err := cp.commitWithTaskAutoComplete(ctx, acEnv(), acUserResult(), Tracker{}, acTaskPathPermission(taskKey)); err != nil {
		t.Fatalf("a closed-task race must not fail the user's op, got: %v", err)
	}
	// The final commit-alone call carries the user op but NO task completion.
	last := rc.calls[len(rc.calls)-1]
	if hasMut, hasEvent := committedHasTaskCompletion(last, taskKey); hasMut || hasEvent {
		t.Fatalf("a closed-task race must drop the injection; hasMut=%v hasEvent=%v", hasMut, hasEvent)
	}
	if !mutationKeyPresent(last, "vtx.leaseapp.ABCDEFGHJKMNPQRSTUVW") {
		t.Fatalf("the user op must still commit when the injection is dropped")
	}
}

// --- helpers ---

func mutationKeyPresent(result ScriptResult, key string) bool {
	for _, m := range result.Mutations {
		if m.Key == key {
			return true
		}
	}
	return false
}

func acEnv() *OperationEnvelope {
	return &OperationEnvelope{
		RequestID:     "AaReqIdJKMNPQRSTUVWX",
		Lane:          LaneDefault,
		OperationType: "ApproveLeaseApplication",
		Actor:         "vtx.identity.AaActorJKMNPQRSTUVW",
		SubmittedAt:   "2026-06-04T00:00:00Z",
	}
}

func acConnect(t *testing.T, url string) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "ac-test"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	provisionHarness(t, ctx, conn)
	return ctx, conn
}
