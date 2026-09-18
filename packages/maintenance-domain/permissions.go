package maintenancedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants the five work-order ops.
//
// Grant matrix:
//
//	ReportIssue                          → operator, frontOfHouse, backOfHouse   (scope=any)
//	ReportIssue                          → consumer                              (scope=self)
//	ResolveWorkOrder                     → operator                              (scope=any)
//	ResolveWorkOrder                     → consumer                              (scope=self)
//	LinkWorkOrderReporter                → operator                              (scope=any)
//	RecordWorkOrderResolvedNotice        → operator                              (scope=any)
//	RecordWorkOrderResolvedNotification  → operator                              (scope=any)
//
// ReportIssue goes to `operator` and to BOTH staff roles: front-of-house takes
// the walk-in report ("the tap in 204 is dripping"), back-of-house raises the
// work it finds itself. Neither grant is a widening — the workplace guard in
// the script confines each holder to the locations they worksAt, and root is
// the only unconfined caller.
//
// ReportIssue's consumer grant is scope=self — the resident reporting a
// problem with their own home. The capability plane validates only that the
// authContext target IS the caller; what confines the write to the caller's
// home is the script's own residence bind (require_residence, ddls.go): the
// reported location must be a unit, and the caller's deterministic residesIn
// link to that unit must be live. A resident holds no worksAt link, so the
// staff guard would deny every self-service report; the self leg is the
// guard that path needs, and the staff walk still binds everyone else.
//
// ResolveWorkOrder's standing grant is `operator` ONLY, and that is the whole
// point rather than an oversight: the maintenance tech does not hold a standing
// resolve grant, they resolve the work order the queued task GRANTS them
// (orchestration-base's capabilityEphemeral lens link-sources the op from the
// task's forOperation, scoped to its scopedTo target). This is lease-signing's
// SignLease posture exactly — the op is operator-granted and the real performer
// reaches it through the §10.7 ephemeral task grant. A standing staff grant
// would hand every back-of-house holder every work order in the building and
// make the claim ceremony decorative.
//
// ResolveWorkOrder's consumer grant is scope=self — the landlord closing an
// order at a unit they manage, lease-signing's DecideLeaseApplication posture.
// The capability plane validates only that the authContext target IS the
// caller; what confines the write is the script's own management bind
// (require_manages_unit, ddls.go): the order must be locatedAt a unit, and the
// caller's deterministic manages link to that unit must be live in the
// declared read set. A tenant holding consumer reaches the leg and is refused
// (no manages link); the order's queued task, left open by a landlord resolve,
// is cancelled by the staleWorkOrderTasks target rather than by the op.
//
// The three operator-only grants are orchestration-internal: LinkWorkOrderReporter
// and RecordWorkOrderResolvedNotice are the directOps the workOrderQueue and
// workOrderResolvedNotices playbooks dispatch under Weaver's service actor (the
// notice op's script pins op.actor to that actor), and
// RecordWorkOrderResolvedNotification is the replyOp the bridge posts after its
// notification adapter Executes. Not console operations: nothing in Loupe or
// loftspace-app dispatches them.
func Permissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "ReportIssue",
			Scope:         "any",
			Note:          "Grants the operator and both staff roles the right to raise a maintenance work order. The script's workplace guard confines each staff holder to the locations they worksAt; only root is unconfined.",
			GrantsTo:      []string{"operator", "frontOfHouse", "backOfHouse"},
		},
		{
			OperationType: "ReportIssue",
			Scope:         "self",
			Note:          "Grants a consumer the right to report an issue at the unit they reside in (the script binds the validated self target to the caller and the caller to the unit by its residesIn link).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "ResolveWorkOrder",
			Scope:         "any",
			Note:          "Grants the operator the right to submit ResolveWorkOrder; the maintenance tech reaches it through the §10.7 ephemeral grant of the task queued to their role (same posture as lease-signing's SignLease), never a standing grant.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "ResolveWorkOrder",
			Scope:         "self",
			Note:          "Grants a landlord the right to resolve a work order at a unit they MANAGE (the acting identity is signed in as itself; the script resolves the order's own locatedAt unit and requires the acting identity's manages link, declared as an OptionalRead by the landlord dispatcher).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "LinkWorkOrderReporter",
			Scope:         "any",
			Note:          "Grants the operator the right to submit LinkWorkOrderReporter (orchestration-internal: the workOrderQueue target's missing_reporter directOp, dispatched by Weaver's service actor to backfill the reportedBy link on an order minted before ReportIssue wrote it).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: resolvedNoticeOp,
			Scope:         "any",
			Note:          "Grants the operator the right to submit RecordWorkOrderResolvedNotice (orchestration-internal: the workOrderResolvedNotices directOp playbook, dispatched by Weaver's service actor; the script pins op.actor to that actor).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: resolvedNotificationOp,
			Scope:         "any",
			Note:          "Grants the operator (the bridge's service actor) the right to submit RecordWorkOrderResolvedNotification — the replyOp the bridge posts after its \"notification\" adapter Executes for a resolved notice. Not a console operation: nothing in Loupe or loftspace-app dispatches it, the bridge does, so the grant needs no consoleOperator counterpart.",
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
// added) — a role-granted caller sends no authContext object at all. The
// consumer self legs are NOT described here: their dispatcher is loftspace-app's
// hand-built tenant submit (ReportIssue, sending authContext {target: self}
// and declaring the residesIn link) and its hand-built landlord Resolve
// (ResolveWorkOrder, the same target shape, declaring the manages link); a
// descriptor-driven form never reaches either leg, and Facet's catalog
// withholds a scope=self grant on a task- or standing-dispatched descriptor.
//
// The two notice ops carry BARE op-metas (no presentation, no form, no
// dispatch) for discoverability only — the loftspace-ledger replyOp posture:
// the playbook dispatches RecordWorkOrderResolvedNotice directly and the
// bridge resolves RecordWorkOrderResolvedNotification from the event body, so
// neither meta is load-bearing for dispatch. LinkWorkOrderReporter carries
// none: it is reached by its playbook alone.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "ResolveWorkOrder",
			// refusal-courtesy(facet): AlreadyResolved: none — the task form is offered while the task is open, and a resolution landing under it by another route (a landlord's self leg, an operator's console) retires that task through the staleWorkOrderTasks target by convergence; in the window before the cancellation lands, the refusal itself is the answer the tech needs.
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
				`"notes":{"type":"string","title":"What you did","description":"What you did to resolve it."}},` +
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
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "ReportIssue",
			// refusal-courtesy(facet): NotResident: unreachable — the descriptor's authContext is standing, so Facet's staff form sends no authContext.target and the script's self leg (the only path raising NotResident) never runs.
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Report an issue",
				ShortLabel:  "Report",
				Description: "Raise a maintenance work order against a place.",
				Icon:        "wrench",
				Tone:        "primary",
				SubmitLabel: "Report it",
				Group:       "Maintenance",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"summary":{"type":"string","title":"What's wrong","description":"What is wrong."},` +
				`"priority":{"type":"string","title":"Priority","enum":["low","normal","urgent"],"description":"How urgent it is."},` +
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
				// The operator-role confinement probe: the workplace-exempt
				// short-circuit walks the actor's own holdsRole links to test
				// for the operator role (actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{OperationType: resolvedNoticeOp},
		{OperationType: resolvedNotificationOp},
	}
}
