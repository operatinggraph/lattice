package maintenancedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants the two work-order ops.
//
// ReportIssue goes to `operator` and to BOTH staff roles: front-of-house takes
// the walk-in report ("the tap in 204 is dripping"), back-of-house raises the
// work it finds itself. Neither grant is a widening — the workplace guard in
// the script confines each holder to the locations they worksAt, and root is
// the only unconfined caller.
//
// ResolveWorkOrder is granted to `operator` ONLY, and that is the whole point
// rather than an oversight: the maintenance tech does not hold a standing
// resolve grant, they resolve the work order the queued task GRANTS them
// (orchestration-base's capabilityEphemeral lens link-sources the op from the
// task's forOperation, scoped to its scopedTo target). This is lease-signing's
// SignLease posture exactly — the op is operator-granted and the real performer
// reaches it through the §10.7 ephemeral task grant. A standing grant would
// hand every back-of-house holder every work order in the building and make
// the claim ceremony decorative.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "ReportIssue",
			Scope:         "any",
			Note:          "Grants the operator and both staff roles the right to raise a maintenance work order. The script's workplace guard confines each staff holder to the locations they worksAt; only root is unconfined.",
			GrantsTo:      []string{"operator", "frontOfHouse", "backOfHouse"},
		},
		{
			OperationType: "ResolveWorkOrder",
			Scope:         "any",
			Note:          "Grants the operator the right to submit ResolveWorkOrder; the maintenance tech reaches it through the §10.7 ephemeral grant of the task queued to their role (same posture as lease-signing's SignLease), never a standing grant.",
			GrantsTo:      []string{"operator"},
		},
	}
}

// OpMetas declares the op-meta vertices that make these ops
// forOperation-resolvable and descriptor-renderable.
//
// ResolveWorkOrder's op-meta is REQUIRED, not hygiene: it is the vertex a
// CreateTask names as `forOperation` when it queues a work order, so its
// absence would make the whole F5 beat unroutable. It carries the full
// descriptor vocabulary because the Facet task row is its only client — a
// claimant opens the task, and the form, labels, and declared reads are built
// from this vertex alone (edge-showcase-app-design.md §3.3).
//
// Dispatch.AuthContext is "task": the claimant's authority is the task's
// ephemeral grant, so the client sends authContext {task, target} — the shape
// cmd/facet/web already authors from a task row (openTaskDetail). TargetField
// is `workOrderKey`, auto-filled from the task's own scopedTo, so the tech
// types only the notes.
//
// ReportIssue carries an op-meta too, for a different reason: it is the op a
// standing staff catalog offers ("something's broken"), so it needs
// presentation + a form. Its authContext is "standing" (the fourth case F2
// added) — a role-granted caller sends no authContext object at all.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "ResolveWorkOrder",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Resolve a work order",
				ShortLabel:  "Resolve",
				Description: "Record what you did. Closing this closes the work order and the task together.",
				Icon:        "wrench",
				Tone:        "primary",
				SubmitLabel: "Mark resolved",
				Group:       "Maintenance",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"workOrderKey":{"type":"string","description":"vtx.workorder.<NanoID> being resolved — auto-filled from the task."},` +
				`"notes":{"type":"string","description":"What you did to resolve it."}},` +
				`"required":["workOrderKey","notes"]}`,
			FieldDescriptions: map[string]string{
				"workOrderKey": "The work order this task is scoped to — filled by the client from the task, not typed.",
				"notes":        "What you actually did. Re-submitting the same notes is harmless (which is what makes resolving offline and syncing later safe), but different notes are refused once a resolution is recorded.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       workOrderVertexDDL,
				AuthContext: "task",
				TargetField: "workOrderKey",
				TargetType:  "workorder",
				Reads:       []string{"{payload.workOrderKey}"},
				// Genuinely absence-tolerant, and therefore NOT a required
				// read: `.resolution` is absent on every first resolve — it IS
				// the read-before-write terminal marker. Declaring it required
				// would fault the op on the key that is correctly missing.
				OptionalReads: []string{"{payload.workOrderKey}.resolution"},
			},
		},
		{
			OperationType: "ReportIssue",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Report an issue",
				ShortLabel:  "Report",
				Description: "Raise a maintenance work order against a place.",
				Icon:        "wrench",
				Tone:        "default",
				SubmitLabel: "Report it",
				Group:       "Maintenance",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"summary":{"type":"string","description":"What is wrong."},` +
				`"priority":{"type":"string","enum":["low","normal","urgent"],"description":"How urgent it is."},` +
				`"location":{"type":"string","description":"vtx.<locType>.<NanoID> of the place — auto-filled from the place being viewed."}},` +
				`"required":["summary","location"]}`,
			FieldDescriptions: map[string]string{
				"summary":  "One line describing the issue. Keep resident details out of it — this text syncs to staff devices.",
				"priority": "low, normal, or urgent. Defaults to normal.",
				"location": "The place the issue is at — filled by the client from the place in view, not typed.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       workOrderVertexDDL,
				AuthContext: "standing",
				// No TargetField/TargetType: `location` is NOT a browsed
				// dispatch target the client picks off a manifest.ent row —
				// there is no entity lens projecting places as browsable
				// entities, so declaring one would only make the op
				// permanently unresolvable (the targetType renderer gate
				// degrades what it cannot resolve). It is instead the
				// submitter's OWN workplace, which is exactly what the
				// `{me.<type>}` self-anchor vocabulary addresses: edgeIdentity
				// projects the worksAt building as the `workplace` selfAnchor,
				// so a staff form asks only for the summary. An identity with
				// no workplace cannot answer it and the client declines to
				// offer the op — fail-closed, and correct: someone who works
				// nowhere has nowhere to report an issue at.
				ContextParams: map[string]string{"location": "{me.workplace}"},
				Reads:         []string{"{payload.location}"},
			},
		},
	}
}
