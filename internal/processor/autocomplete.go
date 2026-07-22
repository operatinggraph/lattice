package processor

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// taskAutoCompletion is the read-current-state result for the task an op's
// ephemeral grant names. It carries the conditional mutation + event the
// commit path injects when the task is open, and a found/open flag so a
// non-open or absent task injects nothing.
type taskAutoCompletion struct {
	taskKey string
	// open is true only when the task root exists, is live, and status==open.
	// When false the commit path injects nothing (no double-complete, no
	// cancelled-resurrection).
	open bool
	// revision is the task root's read revision (the CAS handle) when open.
	revision uint64
	mutation MutationOp
	event    EventSpec
}

// taskKeyFromTaskPathDecision returns the ephemeral grant's TaskKey when the
// step-3 decision resolved on the task path (Contract #10 §10.7), else "".
// The matched grant already names the task to complete — T is never
// re-derived from anywhere else.
func taskKeyFromTaskPathDecision(rp *ResolvedPermission) string {
	if rp == nil || rp.Path != "task" || rp.EphemeralGrant == nil {
		return ""
	}
	return rp.EphemeralGrant.TaskKey
}

// readTaskAutoCompletion reads the current task root and, when it is open,
// builds the conditional status→complete mutation (CAS on the read revision,
// Contract #10 §10.6) + the TaskCompleted event. A task that is absent,
// tombstoned, or not open yields open=false → the caller injects nothing.
//
// Reading the current status BEFORE assembling the batch is what makes a
// failed/raced auto-complete a clean no-op: it never resurrects a cancelled
// task and never double-completes an already-complete one (the three races in
// §10.6). The TaskCompleted payload shape is kept identical to the explicit
// CompleteTask path so Loom consumes one shape.
func readTaskAutoCompletion(ctx context.Context, conn *substrate.Conn, coreBucket, taskKey string) (taskAutoCompletion, error) {
	out := taskAutoCompletion{taskKey: taskKey}
	entry, err := conn.KVGet(ctx, coreBucket, taskKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return out, nil
		}
		return out, err
	}
	var doc struct {
		IsDeleted bool                   `json:"isDeleted"`
		Data      map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return out, err
	}
	if doc.IsDeleted {
		return out, nil
	}
	status, _ := doc.Data["status"].(string)
	if status != "open" {
		return out, nil
	}

	// The task root data is scalars only — {status, expiresAt} (Contract #10
	// §10.1). The auto-complete mutation carries exactly the same shape the
	// explicit CompleteTask path writes: status flips to "complete" and the
	// existing expiresAt scalar is carried forward (absent → "", matching
	// root_expires_at semantics). No other fields ride the root data.
	expiresAt, _ := doc.Data["expiresAt"].(string)
	data := map[string]interface{}{
		"status":    "complete",
		"expiresAt": expiresAt,
	}

	rev := entry.Revision
	out.open = true
	out.revision = rev
	out.mutation = MutationOp{
		Op:               "update",
		Key:              taskKey,
		ExpectedRevision: &rev,
		Document: map[string]interface{}{
			"class":     "task",
			"isDeleted": false,
			"data":      data,
		},
	}
	out.event = EventSpec{
		Class: "orchestration.taskCompleted",
		Data: map[string]interface{}{
			"taskKey": taskKey,
		},
	}
	return out, nil
}

// injectTaskAutoCompletion appends the conditional completion mutation + event
// to a copy of result, leaving the caller's ScriptResult untouched until it
// chooses to adopt the augmented one. The injected ops are platform-injected
// (Adjudication #1, seam a): they ride the existing batch builder, BuildEventList,
// and the transactional outbox unchanged — there is no second assembly path.
func injectTaskAutoCompletion(result ScriptResult, ac taskAutoCompletion) ScriptResult {
	muts := make([]MutationOp, 0, len(result.Mutations)+1)
	muts = append(muts, result.Mutations...)
	muts = append(muts, ac.mutation)

	evs := make([]EventSpec, 0, len(result.Events)+1)
	evs = append(evs, result.Events...)
	evs = append(evs, ac.event)

	return ScriptResult{
		Mutations:  muts,
		Events:     evs,
		PrimaryKey: result.PrimaryKey,
	}
}
