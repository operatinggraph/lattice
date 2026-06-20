package leasesigning

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations:
//
//   - `leaseapp` (vertex type) — CreateLeaseApplication + SignLease. The
//     application's applicant is a link (applicationFor → identity); the
//     signature is a .signature aspect (D5 — root data {}).
//   - `leaseServiceInstance` — CreateLeaseServiceInstance, the externalTask
//     instanceOp Loom submits: mints the claim vertex vtx.service.<handle>,
//     records its family + the providedTo link, and emits external.<adapter>.
//   - `leaseServiceReply` — RecordLeaseServiceOutcome, the externalTask replyOp
//     the bridge submits: records the .outcome aspect from
//     {externalRef, status, result} and emits
//     orchestration.externalTaskCompleted{externalRef}.
//   - `leaseServiceDispatch` — RecordServiceDispatch, the externalTask dispatchOp
//     the bridge submits when its adapter returns Pending: records a create-only
//     .dispatch marker from {externalRef, vendorRef} and emits NO completion
//     signal (the task is not done — the token stays parked).
//
// The two externalTask wrapper DDLs are a matched pair: both choose `service`
// as the claim-vertex type, both speak the bare handle ↔ vtx.service.<handle>
// mapping, and the replyOp's externalRef echo is the same bare handle the
// instanceOp received. The package ships its own wrappers (not 14.1's
// CreateServiceInstance / RecordServiceOutcome) because (a) 14.1's create does
// not emit the external.<adapter> event and (b) 14.1's record takes a full
// instanceKey + a caller-supplied completedAt and emits service.outcomeRecorded
// — not the orchestration.externalTaskCompleted Loom correlates on — while the
// bridge supplies {externalRef, status, result} against a bare handle and needs
// the completion signal. The .outcome aspect SHAPE is reused (D5 fidelity); the
// ops are package-local.
//
// Known-key reads only (mirrors service-domain / orchestration-base): the
// leaseapp + instanceOp ops validate their link endpoints by the keys the
// caller lists in ContextHint.Reads. The replyOp is the exception — the bridge
// submits it with no Reads, so it reads no state and relies on the create-only
// .outcome write for its once-only guarantee.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		leaseAppDDL(),
		leaseServiceInstanceDDL(),
		leaseServiceReplyDDL(),
		leaseServiceDispatchDDL(),
	}
}

func leaseAppDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "leaseapp",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateLeaseApplication", "SignLease"},
		Description: "Lease-application DDL. Vertex shape: vtx.leaseapp.<NanoID>, class=leaseapp, root data = {} " +
			"(minimal, D5 — the application status/gaps are LENS-computed, not stored). The application's applicant " +
			"is a LINK (applicationFor → identity: the later-arriving leaseapp is the source, the pre-existing " +
			"identity is the target, Contract #1 §1.1). The convergence lens walks applicationFor then the service " +
			"instances' providedTo links to read the applicant's bgcheck/payment outcome aspects. " +
			"CreateLeaseApplication mints the application + the applicationFor link, requiring + validating a live " +
			"applicant identity (no-orphan, FR29). SignLease writes the .signature aspect {signedAt (canonical-UTC " +
			"RFC3339)} on the application (the fact that closes the missing_signature gap); it is the assignTask " +
			"forOperation target the §10.8 playbook binds.",
		Script: leaseAppDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"applicant":{"type":"string","description":"vtx.identity.<NanoID> of the applicant this application is for (CreateLeaseApplication; required, validated alive)."},` +
			`"leaseAppId":{"type":"string","description":"Optional bare NanoID for the application vertex (CreateLeaseApplication); absent → minted. The write-ahead seam, mirroring service-domain's instanceId."},` +
			`"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the application to sign (SignLease; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the created or signed application (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"applicant":   "Full vtx.identity.<NanoID> key of the applicant this application is for. CreateLeaseApplication requires it, validates the identity is alive, and writes the applicationFor link (the convergence link the lens walks).",
			"leaseAppId":  "Optional bare NanoID (no dots / key segments) for the application vertex (vtx.leaseapp.<leaseAppId>) created by CreateLeaseApplication. Supplied by a caller that must know the key before commit (the write-ahead seam). Absent → minted with nanoid.new().",
			"leaseAppKey": "Full vtx.leaseapp.<NanoID> key of the application to sign. SignLease validates it is alive and writes the .signature aspect, flipping the missing_signature gap false.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateLeaseApplication — start an application for an applicant",
				Payload: map[string]any{"applicant": "vtx.identity.<applicantNanoID>"},
				ExpectedOutcome: "Validates the applicant identity (alive). Atomically commits vtx.leaseapp.<NanoID> (root data {} — D5) " +
					"+ the applicationFor link (leaseapp→identity). Accepts an optional caller-supplied bare-NanoID leaseAppId. " +
					"Emits leaseapp.applicationCreated{leaseAppKey, applicant}. Returns primaryKey (the application key). " +
					"Rejects with ScriptError if the applicant is absent.",
			},
			{
				Name:    "SignLease — applicant signs the lease",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Validates the application is alive. Writes the .signature aspect {signedAt: <op.submittedAt, canonical UTC>} " +
					"on the application (root data stays {} — D5). Emits leaseapp.leaseSigned{leaseAppKey}. Returns primaryKey. " +
					"Rejects a non-existent application or one already signed (the .signature CreateOnly guard).",
			},
		},
	}
}

func leaseServiceInstanceDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "leaseServiceInstance",
		Class:         "meta.ddl.vertexType",
		// Only the instanceOp writes a leaseServiceInstance-class vertex (it mints
		// it). The replyOp mutates only the outcome-class .outcome aspect, never
		// this vertex, so the step-6 permittedCommands gate (keyed by the MUTATED
		// vertex's class) never consults this list for the replyOp. The op's
		// SCRIPT is still selected by the op envelope's own Class.
		PermittedCommands: []string{"CreateLeaseServiceInstance"},
		Description: "ExternalTask instanceOp DDL (Contract #10 §10.5). The op Loom submits for an externalTask step: " +
			"payload {instanceKey (the bare handle Loom minted), subjectKey (the applicant identity), adapter, replyOp, " +
			"params:{family}}. It prepends the package-chosen claim-vertex type `service` → vtx.service.<handle> and mints " +
			"the claim vertex exactly as a service instance: root data {} (D5), a .class aspect service.<family>.instance " +
			"(14.1 shape fidelity), a .family aspect {value:<family>} (the lens's bgcheck/payment discriminator — read as " +
			"a distinct aspect because the vertex envelope `class` field shadows the .class aspect on the projection read " +
			"path), and the providedTo link to the applicant identity (the convergence link the lens walks). The instance " +
			"is template-less (no instanceOf): the lens hops providedTo, not instanceOf, and buckets family via .family, so " +
			"a template adds install-seeding for zero convergence value. It emits the external.<adapter> event via its own " +
			"transactional outbox (body {instanceKey, adapter, replyOp, params, externalRef, idempotencyKey} — the shape " +
			"the bridge's externalEvent reader consumes); the bridge selects its adapter and posts the replyOp.",
		Script: leaseServiceInstanceDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"instanceKey":{"type":"string","description":"The BARE instance handle Loom minted (no dots / key segments / wildcards); the op prepends vtx.service. → vtx.service.<handle>. Required."},` +
			`"subjectKey":{"type":"string","description":"vtx.identity.<NanoID> of the applicant the claim is for (the pattern subject); the providedTo link points at it. Required, validated alive."},` +
			`"adapter":{"type":"string","description":"The external adapter name (e.g. backgroundCheck, stripe), carried into the external.<adapter> event. Required."},` +
			`"replyOp":{"type":"string","description":"The result-op the bridge posts back (RecordLeaseServiceOutcome), carried into the external event. Required."},` +
			`"params":{"type":"object","description":"Opaque pass-through adapter params from the Loom step; params.family (backgroundCheck|payment) sets the .class + .family aspects."}},` +
			`"required":["instanceKey","subjectKey","adapter","replyOp"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.service.<handle> of the minted claim vertex (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"instanceKey": "The bare instance handle Loom minted for this externalTask (type-free, no dots / key segments / wildcards). The op prepends vtx.service. to it → vtx.service.<handle>. It is echoed back as the reply op's externalRef and is the bridge's adapter dedup key. Required.",
			"subjectKey":  "Full vtx.identity.<NanoID> key of the applicant the externalTask is for (the Loom pattern subject). CreateLeaseServiceInstance validates it is alive and writes the providedTo link (the convergence link the lens reads across). Required.",
			"adapter":     "The registered bridge adapter name (e.g. backgroundCheck, stripe). Carried into the external.<adapter> event class + body so the bridge selects its adapter. Required.",
			"replyOp":     "The result-op type the bridge posts back (RecordLeaseServiceOutcome). Carried into the external event body so the bridge knows which op to submit on success. Required.",
			"params":      "Opaque adapter params passed through from the Loom step. params.family (backgroundCheck|payment) discriminates the claim vertex's .class (service.<family>.instance) and .family aspects.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateLeaseServiceInstance — claim a background check for an applicant",
				Payload: map[string]any{
					"instanceKey": "<bareHandle>",
					"subjectKey":  "vtx.identity.<applicantNanoID>",
					"adapter":     "backgroundCheck",
					"replyOp":     "RecordLeaseServiceOutcome",
					"params":      map[string]any{"family": "backgroundCheck"},
				},
				ExpectedOutcome: "Validates the applicant identity (alive). Atomically commits vtx.service.<handle> (root data {} — D5) " +
					"+ a .class aspect service.backgroundCheck.instance + a .family aspect {value: backgroundCheck} + the providedTo " +
					"link (instance→identity). NO outcome aspect yet (absence = not-yet-complete). Emits the external.backgroundCheck " +
					"event (body {instanceKey, adapter, replyOp, params, externalRef, idempotencyKey}) off the op's outbox. " +
					"Returns primaryKey (the claim-vertex key). Rejects with ScriptError if the applicant is absent or the handle is malformed.",
			},
		},
	}
}

func leaseServiceReplyDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "leaseServiceReply",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"RecordLeaseServiceOutcome"},
		Description: "ExternalTask replyOp DDL (Contract #10 §10.5/§10.6). The op the bridge submits as the result op: " +
			"payload {externalRef (the bare handle), status (the adapter's terminal verdict, completed | failed — REQUIRED, " +
			"copied verbatim from the adapter's Result.Status), result (the adapter's free-form Detail string)} — the bridge " +
			"supplies NO completedAt. The bridge submits it with no ContextHint.Reads, so the op reads NOTHING from " +
			"state: it reconstructs the claim vertex key vtx.service.<externalRef> from the bare handle, takes the required " +
			"status (an adapter error is Nak+retry — never a reply — so every reply carries a definitive business outcome) " +
			"and derives completedAt = time.rfc3339_utc(op.submittedAt) (the bridge supplies no timestamp), and writes the " +
			".outcome aspect {status, completedAt} (D5 — root data stays {}, untouched). The free-form result is kept OFF the " +
			"lens-readable projection plane (it can carry PII / payment data) and rides the service.outcomeRecorded provenance " +
			"event body instead. It emits orchestration.externalTaskCompleted{externalRef: <bare handle>} — the uniform " +
			"orchestration-domain completion signal Loom correlates on (symmetric to orchestration.taskCompleted{taskKey} for " +
			"a userTask); WITHOUT it the externalTask never completes (the creation-deadline disarmed on instanceOp commit, " +
			"the bridge reply carried no completion signal). The outcome is recorded once: the .outcome aspect is create-only, " +
			"so a redelivered reply conflicts and is rejected (the FR58 redelivery defense at the DDL layer, atop the bridge's " +
			"deterministic result-op requestId collapse).",
		Script: leaseServiceReplyDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The BARE instance handle the bridge echoes (no dots / key segments); the op reconstructs vtx.service.<externalRef>. Required."},` +
			`"status":{"type":"string","enum":["completed","failed"],"description":"The adapter's terminal verdict: completed = the external call succeeded with a satisfying result; failed = a definitive business rejection (a declined charge, a failed background check). Copied verbatim by the bridge from the adapter's Result.Status. Required."},` +
			`"result":{"type":"string","description":"The adapter's free-form result Detail string. Carried on the service.outcomeRecorded provenance event body for the audit join; NOT written to the projection-plane .outcome aspect and NOT parsed for pass/fail (status is its own required field)."}},` +
			`"required":["externalRef","status"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.service.<handle> of the claim vertex the outcome was recorded on (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare instance handle the bridge echoes back (the same handle CreateLeaseServiceInstance received). The op reconstructs vtx.service.<externalRef> and emits orchestration.externalTaskCompleted carrying this bare handle (Loom parks on token.<handle> and correlates payload.externalRef — never the full vtx key). Required.",
			"status":      "The adapter's terminal verdict, copied verbatim by the bridge from the adapter's Result.Status: completed (the external call succeeded with a satisfying result) or failed (a definitive business rejection — a declined charge, a failed background check). Written to the .outcome aspect; the lens reads it to decide whether the service converged. Required (no default).",
			"result":      "The adapter's free-form result Detail string (e.g. \"background-check cleared for <subject>\"). Carried on the service.outcomeRecorded provenance event body, NOT written to the lens-readable .outcome aspect (it can carry PII / payment data in production). The pass/fail decision is the separate required status field, not parsed from this string.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "RecordLeaseServiceOutcome — record a passing bridge reply",
				Payload: map[string]any{
					"externalRef": "<bareHandle>",
					"status":      "completed",
					"result":      "background-check cleared for vtx.identity.<applicantNanoID>",
				},
				ExpectedOutcome: "Reads no state (the bridge submits no Reads). Reconstructs vtx.service.<handle> from the bare handle. " +
					"Takes status=completed (required) + derives completedAt = canonical-UTC(op.submittedAt). Writes the .outcome aspect " +
					"{status: completed, completedAt} as a create-only mutation (the instance root, already {}, is untouched — D5). " +
					"Emits orchestration.externalTaskCompleted{externalRef: <handle>} (the Loom completion signal) + " +
					"service.outcomeRecorded (provenance, carrying result). Returns primaryKey. Rejects a second reply for the same " +
					"handle (the create-only .outcome once-only guard — the FR58 redelivery defense).",
			},
			{
				Name: "RecordLeaseServiceOutcome — record a failing bridge reply",
				Payload: map[string]any{
					"externalRef": "<bareHandle>",
					"status":      "failed",
					"result":      "background-check declined for vtx.identity.<applicantNanoID>",
				},
				ExpectedOutcome: "Same shape as the passing reply, but the terminal status is failed — a definitive business " +
					"rejection (a declined charge / a failed background check; an adapter ERROR is Nak+retry, never a reply, so this " +
					"is a verdict, not a transient failure). Writes the .outcome aspect {status: failed, completedAt}. The convergence " +
					"lens reads status=failed as the service NOT having converged (the applicant stays unsatisfied / the gap predicate " +
					"keeps violating). Emits the same completion + provenance events. Rejects an absent or non-{completed,failed} status " +
					"with InvalidArgument.",
			},
		},
	}
}

func leaseServiceDispatchDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "leaseServiceDispatch",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"RecordServiceDispatch"},
		Description: "ExternalTask dispatchOp DDL (Contract #10 §10.5/§10.6). The op the bridge submits when its adapter " +
			"returns Pending (the external call was submitted but has not resolved yet): payload {externalRef (the bare " +
			"handle), vendorRef (the vendor's opaque pending reference — the poll/webhook key)}. The bridge submits it with " +
			"no ContextHint.Reads, so the op reads NOTHING from state: it reconstructs the claim vertex key " +
			"vtx.service.<externalRef> from the bare handle and writes a create-only .dispatch aspect {vendorRef, submittedAt " +
			"(canonical-UTC of op.submittedAt)} — the PENDING MARKER. It writes NO .outcome aspect and emits NO " +
			"orchestration.externalTaskCompleted: the externalTask is NOT done, so Loom's token stays parked (the .dispatch " +
			"and .outcome aspects are deliberately separate — .outcome is the FR58 once-only terminal guard, while pending is " +
			"a distinct state). It emits service.dispatchRecorded (provenance, NOT a completion signal). The marker is recorded " +
			"once: the .dispatch aspect is create-only, so a redelivered Pending conflicts and is rejected (atop the bridge's " +
			"deterministic dispatch-op requestId collapse).",
		Script: leaseServiceDispatchDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The BARE instance handle the bridge echoes (no dots / key segments); the op reconstructs vtx.service.<externalRef>. Required."},` +
			`"vendorRef":{"type":"string","description":"The vendor's opaque pending reference (the poll/webhook key) the bridge got back from the adapter on a Pending outcome. Recorded on the .dispatch marker. Required."}},` +
			`"required":["externalRef","vendorRef"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.service.<handle> of the claim vertex the pending marker was recorded on (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare instance handle the bridge echoes back (the same handle CreateLeaseServiceInstance received). The op reconstructs vtx.service.<externalRef> and writes the create-only .dispatch marker on it. Required.",
			"vendorRef":   "The vendor's opaque pending reference (the poll/webhook key) the bridge received from its adapter when the external call returned Pending. Written to the .dispatch aspect; a later poll/webhook resolution carries it back. Required.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "RecordServiceDispatch — record a pending external call",
				Payload: map[string]any{
					"externalRef": "<bareHandle>",
					"vendorRef":   "vendor-ref-abc123",
				},
				ExpectedOutcome: "Reads no state (the bridge submits no Reads). Reconstructs vtx.service.<handle> from the bare handle. " +
					"Writes the .dispatch aspect {vendorRef, submittedAt: canonical-UTC(op.submittedAt)} as a create-only mutation " +
					"(the instance root, already {}, is untouched — D5). Writes NO .outcome and emits NO " +
					"orchestration.externalTaskCompleted (the task is not done — the token stays parked). Emits service.dispatchRecorded " +
					"(provenance). Returns primaryKey. Rejects a second dispatch for the same handle (the create-only .dispatch once-only guard).",
			},
		},
	}
}
