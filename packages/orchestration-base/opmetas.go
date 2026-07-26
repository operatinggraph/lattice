package orchestrationbase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3) for the one orchestration-base op a person triggers: ClaimTask.
//
// Every other op this package grants goes to `operator` alone (permissions.go) —
// a trusted admin tool that hardcodes its own dispatch — so none owes a
// descriptor. ClaimTask is different by design: permissions.go grants it to
// frontOfHouse and backOfHouse precisely so a staff member can pull their own
// queued work (FR28), and pulling work off a queue is the most descriptor-shaped
// act in the package. No other package declares a ClaimTask meta, so this one
// does not shadow anyone: orchestration-base owns the task DDL that defines it.
//
// AuthContext is "standing" — the claimant's authority is the role they hold,
// not a relationship to the task, so the client sends no authContext object
// (OpDispatchSpec.AuthContext's fourth case). That the grant is platform-wide
// does not make the claim platform-wide: the script resolves the task's own
// queuedFor link and rejects a claimant who does not hold THAT role.
//
// Dispatch.Class is the task DDL's own CanonicalName, the Contract #2 §2.1
// envelope `class` DDL-hint.
//
// The only declared read is the claimant's own assignedTo link, and it is
// optional: it is absent on every genuine claim and present only on the
// idempotent re-claim branch, so requiring it would fail the common case. The
// script's other two reads are deliberately undeclared — the queuedFor
// enumeration is a class-(e) bounded walk, and the holdsRole probe hangs off a
// role id the enumeration produces, so neither key is knowable to the caller
// before submitting.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "ClaimTask",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Claim this task",
				Description: "Take a queued task off your team's queue and onto your own list.",
				Icon:        "hand",
				Tone:        "primary",
				SubmitLabel: "Claim",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"taskKey":{"type":"string","description":"vtx.task.<NanoID> of the queued task to claim — auto-filled from the task being viewed."}},` +
				`"required":["taskKey"]}`,
			FieldDescriptions: map[string]string{
				"taskKey": "The task being claimed — auto-filled by the client from the task being viewed (dispatch.targetField), not user-entered. You may only claim a task queued for a role you hold, and only while it is still open.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "task",
				AuthContext: "standing",
				TargetField: "taskKey",
				TargetType:  "task",
				OptionalReads: []string{
					"lnk.task.{payload.taskKey:id}.assignedTo.identity.{actor:id}",
				},
			},
		},
	}
}
