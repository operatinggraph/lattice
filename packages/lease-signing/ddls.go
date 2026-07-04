package leasesigning

import (
	"encoding/json"

	"github.com/asolgan/lattice/internal/pkgmgr"
)

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
		leaseServiceOutcomeAspectDDL(),
		leaseServiceDispatchAspectDDL(),
	}
}

// aspectDeclarationOnlyScript is the Starlark for the aspect-type DDLs. The
// aspects are written by the vertexType DDLs' op scripts; these aspect-type DDLs
// are step-6 write gates only, never op handlers — they fail closed if dispatched.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

func leaseAppDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "leaseapp",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateLeaseApplication", "SignLease", "WithdrawLeaseApplication", "DecideLeaseApplication", "SetApplicantProfile"},
		Description: "Lease-application DDL. Vertex shape: vtx.leaseapp.<NanoID>, class=leaseapp, root data = {} " +
			"(minimal, D5 — the application status/gaps are LENS-computed, not stored). The application's applicant " +
			"is a LINK (applicationFor → identity: the later-arriving leaseapp is the source, the pre-existing " +
			"identity is the target, Contract #1 §1.1). The convergence lens walks applicationFor then the service " +
			"instances' providedTo links to read the applicant's bgcheck/payment outcome aspects, and walks the " +
			"appliesToUnit link to the leased location-domain unit (vtx.unit.<NanoID>) to project its address / rent " +
			"as informational columns. CreateLeaseApplication mints the application + the applicationFor link + the " +
			"appliesToUnit link, requiring + validating a live applicant identity AND a live unit (no-orphan, FR29; " +
			"a unit-less application can never exist — there is no missing_unit gap). It optionally writes a .terms " +
			"aspect {moveInDate, leaseTermMonths, requestedRent?} when moveInDate is supplied. A per-(applicant, unit) " +
			"DETERMINISTIC guard LINK lnk.identity.<a>.appliedToUnit.unit.<u> enforces the duplicate-application " +
			"constraint (≤1 live application per applicant+unit; a unit still accepts many DIFFERENT applicants): " +
			"CreateLeaseApplication creates it (a second concurrent application RevisionConflicts on the key — fail closed), " +
			"reviving it from a prior withdraw's tombstone via CAS on re-apply (relationships are links, never keys in an " +
			"aspect — Contract #1). SignLease writes the .signature aspect {signedAt (canonical-UTC " +
			"RFC3339)} on the application (the fact that closes the missing_signature gap); it is the assignTask " +
			"forOperation target the §10.8 playbook binds. WithdrawLeaseApplication{leaseAppKey, unit, applicant} soft-deletes the " +
			"application (the convergence lens filters isDeleted → the row drops from My Applications) and FREES the " +
			"per-(applicant, unit) guard link (tombstones it), verifying both the unit (appliesToUnit link) and the applicant " +
			"(applicationFor link) — the complement to the duplicate-application guard so an applicant can back out + re-apply. " +
			"DecideLeaseApplication{leaseAppKey, decision, reason?} records the landlord's leasing decision as a .decision aspect " +
			"{value (approved|declined), decidedAt (canonical-UTC RFC3339), reason? (optional decline rationale)}. A recorded decision is " +
			"TERMINAL: re-submitting the same decision is idempotent, but changing it to a different value is rejected (DecisionFinal) so a " +
			"decision cannot silently flip / oscillate; an approve is rejected (NotReadyToApprove) unless the application has been signed. It is the human gate the " +
			"listing-flip waits behind: the convergence lens reads .decision.value so an approval opens missing_listingLeased " +
			"(the unit leases) while a decline is a terminal disposition — nothing auto-leases on applicant-readiness alone. " +
			"SetApplicantProfile{leaseAppKey, unit, annualIncome, employmentStatus, employerName?, references?, hasCoApplicant?, " +
			"hasGuarantor?, guarantorName?, guarantorRelationship?, guarantorAnnualIncome?, coApplicantName?, coApplicantContact?} " +
			"captures the applicant's qualification profile as a .profile aspect so the landlord has something to " +
			"decide on. The RAW financials (annualIncome, employerName, the guarantor / co-applicant detail) live in the Core-KV " +
			"aspect plaintext-for-now (the .ssn / .demographics discipline — the deferred Vault plane owns their encryption + a " +
			"raw display later) and are NEVER projected; the op DERIVES the landlord-facing signals (incomeToRentMet — gross monthly " +
			"income ≥ 3× the unit's listing rent, read on demand; employmentVerified; referenceCount; hasCoApplicant; hasGuarantor; " +
			"guarantorIncomeToRentMet — the guarantor's own income ≥ 3× rent) which the lens projects so a landlord sees qualification " +
			"without the raw figures. UNCONDITIONED upsert (re-submittable). It verifies unit is the " +
			"application's appliesToUnit target (the Withdraw precedent) and feeds no gap — capture + surface, not a convergence gate.",
		Script: leaseAppDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"applicant":{"type":"string","description":"vtx.identity.<NanoID> of the applicant this application is for (CreateLeaseApplication: required, validated alive; WithdrawLeaseApplication: required, verified via the applicationFor link, to free the per-(applicant, unit) guard link)."},` +
			`"unit":{"type":"string","description":"vtx.unit.<NanoID> of the location-domain unit this application is to lease (CreateLeaseApplication; required, validated alive)."},` +
			`"moveInDate":{"type":"string","description":"Requested move-in date, RFC3339 (CreateLeaseApplication; optional — present ⇒ writes the .terms aspect and requires leaseTermMonths)."},` +
			`"leaseTermMonths":{"type":"integer","description":"Requested lease term in months (CreateLeaseApplication; required when moveInDate is supplied)."},` +
			`"requestedRent":{"type":"number","description":"Applicant's offered monthly rent (CreateLeaseApplication; optional, only with moveInDate)."},` +
			`"leaseAppId":{"type":"string","description":"Optional bare NanoID for the application vertex (CreateLeaseApplication); absent → minted. The write-ahead seam, mirroring service-domain's instanceId."},` +
			`"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the application to sign (SignLease), withdraw (WithdrawLeaseApplication) or decide (DecideLeaseApplication); required, validated alive."},` +
			`"decision":{"type":"string","enum":["approved","declined"],"description":"The landlord's leasing decision (DecideLeaseApplication; required). approved opens the listing-leased gate (the unit leases); declined is a terminal disposition."},` +
			`"reason":{"type":"string","description":"Optional free-text rationale for a DecideLeaseApplication decline (applicant feedback + a fair-housing record). Stored on the .decision aspect and projected as the declineReason lens column; ignored on an approve."},` +
			`"annualIncome":{"type":"number","description":"The applicant's gross annual income (SetApplicantProfile; required, > 0). RAW — stored in the .profile aspect, NEVER projected; only the derived incomeToRentMet boolean reaches the read model."},` +
			`"employmentStatus":{"type":"string","enum":["employed","self-employed","unemployed","student","retired"],"description":"The applicant's employment status (SetApplicantProfile; required). employed / self-employed derive employmentVerified=true."},` +
			`"employerName":{"type":"string","description":"The applicant's employer (SetApplicantProfile; optional). RAW — stored, never projected."},` +
			`"references":{"type":"array","items":{"type":"string"},"description":"The applicant's references, free-text (SetApplicantProfile; optional). Only the derived referenceCount is projected, never the entries."},` +
			`"hasCoApplicant":{"type":"boolean","description":"Whether the application has a co-applicant (SetApplicantProfile; optional, default false). Projected as a derived signal."},` +
			`"hasGuarantor":{"type":"boolean","description":"Whether the application has a guarantor (SetApplicantProfile; optional, default false). Projected as a derived signal."},` +
			`"guarantorName":{"type":"string","description":"The guarantor's name (SetApplicantProfile; optional, only with hasGuarantor). RAW — stored, never projected."},` +
			`"guarantorRelationship":{"type":"string","description":"The guarantor's relationship to the applicant, e.g. parent (SetApplicantProfile; optional, only with hasGuarantor). RAW — stored, never projected."},` +
			`"guarantorAnnualIncome":{"type":"number","description":"The guarantor's gross annual income (SetApplicantProfile; optional, only with hasGuarantor, > 0). RAW — stored, NEVER projected; only the derived guarantorIncomeToRentMet boolean reaches the read model."},` +
			`"coApplicantName":{"type":"string","description":"The co-applicant's name (SetApplicantProfile; optional, only with hasCoApplicant). RAW — stored, never projected."},` +
			`"coApplicantContact":{"type":"string","description":"The co-applicant's contact (email / phone) (SetApplicantProfile; optional, only with hasCoApplicant). RAW — stored, never projected."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the created or signed application (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"applicant":             "Full vtx.identity.<NanoID> key of the applicant this application is for. CreateLeaseApplication requires it, validates the identity is alive, and writes the applicationFor link (the convergence link the lens walks). WithdrawLeaseApplication also requires it (verified via the applicationFor link) to reconstruct + free the per-(applicant, unit) guard link.",
			"unit":                  "Full vtx.unit.<NanoID> key of the location-domain unit being applied for. CreateLeaseApplication requires it, validates it is alive, and writes the appliesToUnit link (leaseapp→unit). The convergence lens walks it and projects the unit's address / rent as informational columns. Required (no unit-less application). WithdrawLeaseApplication also requires it (verified via the appliesToUnit link) to reconstruct + free the per-(applicant, unit) guard link.",
			"moveInDate":            "Optional requested move-in date (RFC3339). When supplied, CreateLeaseApplication writes the .terms aspect {moveInDate, leaseTermMonths, requestedRent?} and requires leaseTermMonths. Informational application detail (not read by the convergence lens).",
			"leaseTermMonths":       "Requested lease term in months. Required when moveInDate is supplied; written to the .terms aspect.",
			"requestedRent":         "Optional monthly rent the applicant offers. Written to the .terms aspect when supplied (only meaningful alongside moveInDate).",
			"leaseAppId":            "Optional bare NanoID (no dots / key segments) for the application vertex (vtx.leaseapp.<leaseAppId>) created by CreateLeaseApplication. Supplied by a caller that must know the key before commit (the write-ahead seam). Absent → minted with nanoid.new().",
			"leaseAppKey":           "Full vtx.leaseapp.<NanoID> key of the application to act on. SignLease validates it is alive and writes the .signature aspect (flipping missing_signature false); WithdrawLeaseApplication validates it is alive and soft-deletes it; DecideLeaseApplication validates it is alive and writes the .decision aspect; SetApplicantProfile validates it is alive and writes the .profile aspect. The caller lists it in ContextHint.Reads.",
			"annualIncome":          "The applicant's gross annual income (SetApplicantProfile; required, > 0). RAW sensitive financial data: stored in the .profile aspect plaintext-for-now (the .ssn / .demographics discipline — the deferred Vault plane owns encryption + a raw-financial display) and NEVER projected. The op derives incomeToRentMet (gross monthly income ≥ 3× the unit's listing rent) from it, and only that boolean reaches the read model.",
			"employmentStatus":      "The applicant's employment status (SetApplicantProfile; required): employed | self-employed | unemployed | student | retired. employed / self-employed derive the projected employmentVerified=true (an active income source); the rest are captured honestly and read as unverified.",
			"employerName":          "The applicant's employer name (SetApplicantProfile; optional). RAW — stored in the .profile aspect, never projected.",
			"references":            "The applicant's references as free-text strings (SetApplicantProfile; optional). Blank entries are dropped; only the derived referenceCount (the list length) is projected, never the entries themselves.",
			"hasCoApplicant":        "Whether the application includes a co-applicant (SetApplicantProfile; optional, default false). Projected verbatim as a derived qualification signal.",
			"hasGuarantor":          "Whether the application is backed by a guarantor (SetApplicantProfile; optional, default false). Projected verbatim as a derived qualification signal.",
			"guarantorName":         "The guarantor's name (SetApplicantProfile; optional, captured only when hasGuarantor). RAW — stored in the .profile aspect plaintext-for-now (the .ssn / .demographics discipline — the deferred Vault plane owns its display) and NEVER projected.",
			"guarantorRelationship": "The guarantor's relationship to the applicant, e.g. parent / employer (SetApplicantProfile; optional, captured only when hasGuarantor). RAW — stored, never projected.",
			"guarantorAnnualIncome": "The guarantor's gross annual income (SetApplicantProfile; optional, captured only when hasGuarantor, > 0). RAW sensitive financial data — stored, NEVER projected. The op derives guarantorIncomeToRentMet (guarantor gross monthly ≥ 3× the unit's listing rent — the standard reason a guarantor backs a thin-income application) from it, and only that boolean reaches the read model.",
			"coApplicantName":       "The co-applicant's name (SetApplicantProfile; optional, captured only when hasCoApplicant). RAW — stored, never projected (the Vault plane owns its display).",
			"coApplicantContact":    "The co-applicant's contact — email or phone (SetApplicantProfile; optional, captured only when hasCoApplicant). RAW — stored, never projected.",
			"decision":              "The landlord's leasing decision (DecideLeaseApplication; required): approved or declined. Written to the .decision aspect {value, decidedAt}. A recorded decision is TERMINAL — the same value re-submits idempotently, a different value is rejected (DecisionFinal); approve is rejected (NotReadyToApprove) unless the application is signed. The convergence lens reads it: approved opens missing_listingLeased (the unit leases); declined folds into the lens's declined disposition (a terminal rejection).",
			"reason":                "Optional free-text rationale the landlord supplies with a DecideLeaseApplication decline — applicant feedback plus a fair-housing record. Stored on the .decision aspect ({value, decidedAt, reason?}) only when supplied and projected as the declineReason lens column the applicant FE renders on the declined banner. A same-value re-submission (idempotent) can attach / update it; ignored on an approve.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateLeaseApplication — start an application for an applicant",
				Payload: map[string]any{"applicant": "vtx.identity.<applicantNanoID>", "unit": "vtx.unit.<unitNanoID>"},
				ExpectedOutcome: "Validates the applicant identity + the unit (both alive). Atomically commits vtx.leaseapp.<NanoID> (root data {} — D5) " +
					"+ the applicationFor link (leaseapp→identity) + the appliesToUnit link (leaseapp→unit). Accepts an optional " +
					"caller-supplied bare-NanoID leaseAppId, and optional .terms (moveInDate + leaseTermMonths [+ requestedRent]). " +
					"Emits leaseapp.applicationCreated{leaseAppKey, applicant, unit}. Returns primaryKey (the application key). " +
					"Rejects with ScriptError if the applicant or unit is absent.",
			},
			{
				Name:    "SignLease — applicant signs the lease",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Validates the application is alive. Writes the .signature aspect {signedAt: <op.submittedAt, canonical UTC>} " +
					"on the application (root data stays {} — D5). Emits leaseapp.leaseSigned{leaseAppKey}. Returns primaryKey. " +
					"Rejects a non-existent application or one already signed (the .signature CreateOnly guard).",
			},
			{
				Name:    "WithdrawLeaseApplication — applicant cancels / backs out of an application",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>", "unit": "vtx.unit.<unitNanoID>", "applicant": "vtx.identity.<applicantNanoID>"},
				ExpectedOutcome: "Validates the application is alive, that unit is its appliesToUnit target and applicant is its " +
					"applicationFor target (both via their leaseapp-anchored links). Soft-deletes the leaseapp (isDeleted=True, " +
					"root stays {} — D5) so the convergence row deletes and it drops from My Applications, and FREES (tombstones) " +
					"the per-(applicant, unit) guard link lnk.identity.<a>.appliedToUnit.unit.<u> so the applicant can re-apply " +
					"to the same unit (the next CreateLeaseApplication revives it). Emits leaseapp.applicationWithdrawn{leaseAppKey, " +
					"unit}. Returns primaryKey. Rejects a non-existent application, a unit that is not the application's unit " +
					"(UnitMismatch), or an applicant that is not the application's applicant (ApplicantMismatch).",
			},
			{
				Name:    "DecideLeaseApplication — landlord approves or declines an application",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>", "decision": "approved"},
				ExpectedOutcome: "Validates the application is alive and the decision is approved|declined. Writes the .decision aspect " +
					"{value: <decision>, decidedAt: <op.submittedAt, canonical UTC>} on the application (root stays {} — D5). " +
					"A recorded decision is terminal: the same value re-submits idempotently, a different value is rejected " +
					"(DecisionFinal); approve is rejected (NotReadyToApprove) unless the application is signed. approved opens the " +
					"listing-leased convergence (the unit leases); declined is a terminal rejection. Emits " +
					"leaseapp.applicationDecided{leaseAppKey, decision}. Returns primaryKey. Rejects a non-existent application " +
					"(UnknownLeaseApplication) or an out-of-enum decision (BadDecision).",
			},
			{
				Name:    "DecideLeaseApplication — landlord declines with a reason",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>", "decision": "declined", "reason": "Income below the 3x-rent threshold."},
				ExpectedOutcome: "As above, but the optional reason is stored on the .decision aspect ({value, decidedAt, reason}) and projected " +
					"as the declineReason lens column the applicant FE renders on the declined banner. The decline is terminal — a " +
					"different later decision is rejected (DecisionFinal); a same-value re-submission can update the reason. reason is ignored on an approve.",
			},
			{
				Name: "SetApplicantProfile — applicant records their qualification profile",
				Payload: map[string]any{
					"leaseAppKey":           "vtx.leaseapp.<NanoID>",
					"unit":                  "vtx.unit.<unitNanoID>",
					"annualIncome":          96000,
					"employmentStatus":      "employed",
					"employerName":          "Acme Corp",
					"references":            []any{"Prior landlord — Jane Doe", "Manager — John Roe"},
					"hasGuarantor":          true,
					"guarantorName":         "Pat Guarantor",
					"guarantorRelationship": "parent",
					"guarantorAnnualIncome": 120000,
				},
				ExpectedOutcome: "Validates the application is alive and that unit is its appliesToUnit target (via the link). Reads the unit's " +
					".listing rent on demand to derive incomeToRentMet (96000/12 = 8000 ≥ 3× rent?) and, because hasGuarantor, " +
					"guarantorIncomeToRentMet (120000/12 = 10000 ≥ 3× rent?). Writes the .profile aspect with the RAW fields (annualIncome, " +
					"employmentStatus, employerName, references, guarantorName, guarantorRelationship, guarantorAnnualIncome) — never projected — " +
					"plus the DERIVED signals (incomeToRentMet, employmentVerified=true, referenceCount=2, hasCoApplicant=false, hasGuarantor=true, " +
					"guarantorIncomeToRentMet, submittedAt) that the lens projects. UNCONDITIONED upsert (re-submittable). Emits " +
					"leaseapp.profileSubmitted{leaseAppKey}. Returns primaryKey. Rejects a non-existent application, a unit that is not the " +
					"application's unit (UnitMismatch), a non-positive annualIncome, or an out-of-enum employmentStatus.",
			},
		},
		Effects: map[string][]json.RawMessage{
			// SignLease unconditionally writes the .signature aspect on commit —
			// exactly the fact that closes the §10.8 playbook's missing_signature
			// gap (targets.go).
			"SignLease": {json.RawMessage(`{"present":"subject.signature.data.signedAt"}`)},
		},
	}
}

func leaseServiceInstanceDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "leaseServiceInstance",
		Class:         "meta.ddl.vertexType",
		// CreateLeaseServiceInstance creates the instance vertex ROOT (class
		// service.<family>.instance), which misses the exact class→DDL lookup, so the
		// step-6 write-gate resolver walks the instance's instanceOf link to THIS
		// DDL's meta-vertex (the type authority) and enforces this list. The .outcome
		// / .dispatch aspect writes resolve by exact class match to their own
		// aspect-type DDLs (leaseServiceOutcome / leaseServiceDispatchMarker) — so
		// they never walk the instanceOf chain to this DDL. The op SCRIPT is selected
		// by operationType (ClassForCommand).
		PermittedCommands: []string{"CreateLeaseServiceInstance"},
		Description: "ExternalTask instanceOp DDL (Contract #10 §10.5). The op Loom submits for an externalTask step: " +
			"payload {instanceKey (the bare handle Loom minted), subjectKey (the applicant identity), adapter, replyOp, " +
			"params:{family}}. It prepends the package-chosen claim-vertex type `service` → vtx.service.<handle> and mints " +
			"the claim vertex as a service instance: root data {} (D5), the type/subtype discriminator on the vertex " +
			"ENVELOPE class service.<family>.instance (P7 — no .class/.family shadow aspect), an instanceOf link to this " +
			"DDL's own meta-vertex (the write-gate type authority — Contract #1 §1.5 instanceOf terminal, the meta key " +
			"surfaced to the script as ddl[...].metaKey), and the providedTo link to the applicant identity (the " +
			"convergence link the lens walks; the lens discriminates bgcheck/payment by reading inst.class directly). It " +
			"emits the external.<adapter> event via its own transactional outbox (body {instanceKey, adapter, replyOp, " +
			"params, externalRef, idempotencyKey} — the shape the bridge's externalEvent reader consumes); the bridge " +
			"selects its adapter and posts the replyOp.",
		Script: leaseServiceInstanceDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"instanceKey":{"type":"string","description":"The BARE instance handle Loom minted (no dots / key segments / wildcards); the op prepends vtx.service. → vtx.service.<handle>. Required."},` +
			`"subjectKey":{"type":"string","description":"vtx.identity.<NanoID> of the applicant the claim is for (the pattern subject); the providedTo link points at it. Required, validated alive."},` +
			`"adapter":{"type":"string","description":"The external adapter name (e.g. backgroundCheck, stripe), carried into the external.<adapter> event. Required."},` +
			`"replyOp":{"type":"string","description":"The result-op the bridge posts back (RecordLeaseServiceOutcome), carried into the external event. Required."},` +
			`"params":{"type":"object","description":"Opaque pass-through adapter params from the Loom step; params.family (backgroundCheck|payment) sets the instance's envelope class service.<family>.instance."}},` +
			`"required":["instanceKey","subjectKey","adapter","replyOp"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.service.<handle> of the minted claim vertex (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"instanceKey": "The bare instance handle Loom minted for this externalTask (type-free, no dots / key segments / wildcards). The op prepends vtx.service. to it → vtx.service.<handle>. It is echoed back as the reply op's externalRef and is the bridge's adapter dedup key. Required.",
			"subjectKey":  "Full vtx.identity.<NanoID> key of the applicant the externalTask is for (the Loom pattern subject). CreateLeaseServiceInstance validates it is alive and writes the providedTo link (the convergence link the lens reads across). Required.",
			"adapter":     "The registered bridge adapter name (e.g. backgroundCheck, stripe). Carried into the external.<adapter> event class + body so the bridge selects its adapter. Required.",
			"replyOp":     "The result-op type the bridge posts back (RecordLeaseServiceOutcome). Carried into the external event body so the bridge knows which op to submit on success. Required.",
			"params":      "Opaque adapter params passed through from the Loom step. params.family (backgroundCheck|payment) discriminates the claim vertex's envelope class (service.<family>.instance).",
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
				ExpectedOutcome: "Validates the applicant identity (alive). Atomically commits vtx.service.<handle> with envelope " +
					"class service.backgroundCheck.instance (root data {} — D5) + the instanceOf link to the leaseServiceInstance " +
					"type-authority meta + the providedTo link (instance→identity). NO outcome aspect yet (absence = not-yet-complete). Emits the external.backgroundCheck " +
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
		Effects: map[string][]json.RawMessage{
			// RecordLeaseServiceOutcome unconditionally writes the .outcome aspect
			// on commit, regardless of the completed/failed verdict carried in the
			// param — the coarse fact a goal-regression planner (Fire 6) can chain
			// on; a completed-specific effect needs a param-conditioned guard the
			// §10.5 grammar does not express (it reads state, not op params), so
			// this declares only what every commit entails unconditionally.
			"RecordLeaseServiceOutcome": {json.RawMessage(`{"present":"subject.outcome.data.status"}`)},
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
			"handle), vendorRef (the vendor's opaque pending reference — the poll/webhook key), adapter (which adapter to " +
			"Poll), replyOp (the result-op to post on resolve/timeout), nextPollAt + deadline (the bridge's schedule " +
			"instants)}. The bridge submits it with no ContextHint.Reads, so the op reads NOTHING from state: it reconstructs " +
			"the claim vertex key vtx.service.<externalRef> from the bare handle and writes a create-only .dispatch aspect " +
			"{vendorRef, adapter, replyOp, submittedAt (canonical-UTC of op.submittedAt), nextPollAt, deadline} — the PENDING " +
			"MARKER. The bridge's poll/timeout schedules carry only the bare handle in their subject, so the fired handler " +
			"reads the routing (adapter / replyOp) from the schedule payload, not this marker — the marker records it for the lens / Weaver read-model. It writes NO .outcome aspect and emits NO " +
			"orchestration.externalTaskCompleted: the externalTask is NOT done, so Loom's token stays parked (the .dispatch " +
			"and .outcome aspects are deliberately separate — .outcome is the FR58 once-only terminal guard, while pending is " +
			"a distinct state). It emits service.dispatchRecorded (provenance, NOT a completion signal). The marker is recorded " +
			"once: the .dispatch aspect is create-only, so a redelivered Pending conflicts and is rejected (atop the bridge's " +
			"deterministic dispatch-op requestId collapse).",
		Script: leaseServiceDispatchDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The BARE instance handle the bridge echoes (no dots / key segments); the op reconstructs vtx.service.<externalRef>. Required."},` +
			`"vendorRef":{"type":"string","description":"The vendor's opaque pending reference (the poll/webhook key) the bridge got back from the adapter on a Pending outcome. Recorded on the .dispatch marker. Required."},` +
			`"adapter":{"type":"string","description":"The adapter name to re-call on a poll, recorded on the .dispatch marker for the lens / Weaver read-model (the fired handler reads the adapter from the schedule payload). Required."},` +
			`"replyOp":{"type":"string","description":"The result-op type the fired handler posts when the poll resolves or the call times out (RecordLeaseServiceOutcome). Required."},` +
			`"nextPollAt":{"type":"string","description":"RFC3339 instant the next poll is due (the bridge armed schedule.bridge.poll at this instant). Normalized to canonical UTC on the marker. Required."},` +
			`"deadline":{"type":"string","description":"RFC3339 instant the call gives up (the bridge armed schedule.bridge.timeout at this instant); the marker records the same instant for the lens / Weaver read-model. Normalized to canonical UTC. Required."}},` +
			`"required":["externalRef","vendorRef","adapter","replyOp","nextPollAt","deadline"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.service.<handle> of the claim vertex the pending marker was recorded on (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare instance handle the bridge echoes back (the same handle CreateLeaseServiceInstance received). The op reconstructs vtx.service.<externalRef> and writes the create-only .dispatch marker on it. Required.",
			"vendorRef":   "The vendor's opaque pending reference (the poll/webhook key) the bridge received from its adapter when the external call returned Pending. Written to the .dispatch aspect; a later poll/webhook resolution carries it back. Required.",
			"adapter":     "The adapter name to re-call on a poll, recorded on the .dispatch marker for the lens / Weaver read-model (the fired handler reads the adapter from the schedule payload, not the marker). Required.",
			"replyOp":     "The result-op type (RecordLeaseServiceOutcome) the fired handler posts when the poll resolves or the call times out. Required.",
			"nextPollAt":  "RFC3339 instant the next poll is due — the instant the bridge armed schedule.bridge.poll.<handle> at. Normalized to canonical UTC on the marker. Required.",
			"deadline":    "RFC3339 instant the call gives up — the instant the bridge armed schedule.bridge.timeout.<handle> at. The marker records the same instant for the lens / Weaver read-model. Normalized to canonical UTC. Required.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "RecordServiceDispatch — record a pending external call",
				Payload: map[string]any{
					"externalRef": "<bareHandle>",
					"vendorRef":   "vendor-ref-abc123",
					"adapter":     "backgroundCheck",
					"replyOp":     "RecordLeaseServiceOutcome",
					"nextPollAt":  "2026-06-19T10:00:30Z",
					"deadline":    "2026-06-20T10:00:00Z",
				},
				ExpectedOutcome: "Reads no state (the bridge submits no Reads). Reconstructs vtx.service.<handle> from the bare handle. " +
					"Writes the .dispatch aspect {vendorRef, adapter, replyOp, submittedAt: canonical-UTC(op.submittedAt), " +
					"nextPollAt, deadline} as a create-only mutation (the instance root, already {}, is untouched — D5). Writes NO " +
					".outcome and emits NO orchestration.externalTaskCompleted (the task is not done — the token stays parked). Emits " +
					"service.dispatchRecorded (provenance). Returns primaryKey. Rejects a second dispatch for the same handle (the " +
					"create-only .dispatch once-only guard).",
			},
		},
	}
}

// leaseServiceOutcomeAspectDDL declares the .outcome aspect (class
// leaseServiceOutcome) — the step-6 write gate for RecordLeaseServiceOutcome.
// Now that a service instance carries the fine-grained envelope class
// service.<family>.instance (P7) + an instanceOf link to its type authority, an
// aspect write that misses the exact class->DDL lookup would otherwise walk the
// instance's instanceOf chain to the leaseServiceInstance DDL (which permits only
// CreateLeaseServiceInstance) and fail closed. This aspect-type DDL makes the
// .outcome write resolve by exact class match to its own gate instead — the
// resolver never walks the instanceOf chain for it. Declaration-only: no op
// handler (the leaseServiceReply vertexType DDL owns the writing script).
func leaseServiceOutcomeAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "leaseServiceOutcome",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"RecordLeaseServiceOutcome"},
		Description: "Lease service-instance outcome aspect. Stored as vtx.service.<handle>.outcome (class " +
			"leaseServiceOutcome) = {status (completed|failed), completedAt, validUntil}. The terminal external-call " +
			"verdict the convergence lens reads (by local name inst.outcome.data.*, unaffected by the class). Written " +
			"ONLY by RecordLeaseServiceOutcome (whose leaseServiceReply vertexType DDL owns the script); this aspect-type " +
			"DDL is the step-6 write gate (exact class match — the instance's fine-grained envelope class + instanceOf " +
			"type authority would otherwise route the write to the instance DDL and reject it). Declaration-only.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"status":{"type":"string","enum":["completed","failed"]},"completedAt":{"type":"string"},"validUntil":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"status":      "The terminal verdict: completed | failed.",
			"completedAt": "RFC3339 instant the external call completed (canonical UTC).",
			"validUntil":  "RFC3339 freshness horizon (a completed outcome is fresh only while now < validUntil).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "lease service outcome aspect",
				Payload:         map[string]any{"status": "completed", "completedAt": "2026-01-01T00:00:00Z"},
				ExpectedOutcome: "Stored as vtx.service.<handle>.outcome; written by RecordLeaseServiceOutcome.",
			},
		},
	}
}

// leaseServiceDispatchAspectDDL declares the .dispatch aspect (class
// leaseServiceDispatch) — the step-6 write gate for RecordServiceDispatch. Same
// rationale as leaseServiceOutcomeAspectDDL: an exact class match keeps the
// pending-marker write off the instance's instanceOf chain. The vertexType DDL
// leaseServiceDispatch (the op script) and the .dispatch aspect class share the
// name leaseServiceDispatch; aspectType DDLs are excluded from the
// operationType->class reverse index, so there is no script-selection ambiguity.
// Declaration-only.
func leaseServiceDispatchAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "leaseServiceDispatchMarker",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"RecordServiceDispatch"},
		Description: "Lease service-instance pending-dispatch aspect. Stored as vtx.service.<handle>.dispatch (class " +
			"leaseServiceDispatchMarker) = {vendorRef, adapter, replyOp, submittedAt, nextPollAt, deadline}. The async PENDING " +
			"marker (an adapter that returned Pending). Written ONLY by RecordServiceDispatch (whose leaseServiceDispatch " +
			"vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate (exact class match). " +
			"Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"vendorRef":{"type":"string"},"adapter":{"type":"string"},"replyOp":{"type":"string"},"submittedAt":{"type":"string"},"nextPollAt":{"type":"string"},"deadline":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"vendorRef":   "The vendor's opaque pending reference.",
			"adapter":     "The adapter to re-call on a poll.",
			"replyOp":     "The result-op posted on resolve/timeout.",
			"submittedAt": "RFC3339 instant the pending marker was recorded (canonical UTC).",
			"nextPollAt":  "RFC3339 instant the next poll is due.",
			"deadline":    "RFC3339 instant the call gives up.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "lease service dispatch (pending) aspect",
				Payload:         map[string]any{"vendorRef": "vendor-123", "adapter": "backgroundCheck", "replyOp": "RecordLeaseServiceOutcome"},
				ExpectedOutcome: "Stored as vtx.service.<handle>.dispatch; written by RecordServiceDispatch.",
			},
		},
	}
}
