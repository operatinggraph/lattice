package leasesigning

import (
	"encoding/json"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// DDLs returns the package's DDL meta-vertex declarations:
//
//   - `leaseapp` (vertex type) — CreateLeaseApplication + SignLease. The
//     application's applicant is a link (applicationFor → identity); the
//     signature is a .signature aspect (D5 — root data {}).
//   - `applicantProfile` / `underwritingParties` / `applicationSignals` — the
//     three aspect-type DDLs SetApplicantProfile writes in one batch (the
//     leaseapp vertexType script owns the write; these are its step-6 write
//     gates). `applicantProfile` (SENSITIVE, custodied on the
//     underwritingRecord retention class; LOCAL NAME stays .profile —
//     Contract #1 §1.5 namespaces the CLASS only) carries the applicant's own
//     raw financials; `underwritingParties` (SENSITIVE, same class) carries
//     the guarantor/co-applicant identifiers AND the applicant's own
//     references — every field there names a third party with no identity of
//     its own to be custodied on (retention-class-key-custody-design.md
//     §8.7, RetentionClasses()). `applicationSignals` (NON-sensitive) carries
//     the derived qualification booleans/counts the three shipped lenses
//     project. See RetentionClasses().
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
//
// The executed-lease document-generation triad (leaseDocInstance /
// leaseDocReply / the leaseDocOutcome aspect gate) is appended from
// leasedoc_ddls.go, and the renewal chain's DDLs from renewal_ddls.go.
func DDLs() []pkgmgr.DDLSpec {
	ddls := []pkgmgr.DDLSpec{
		leaseAppDDL(),
		profileAspectDDL(),
		underwritingPartiesAspectDDL(),
		applicationSignalsAspectDDL(),
		leaseServiceInstanceDDL(),
		leaseServiceReplyDDL(),
		leaseServiceDispatchDDL(),
		leaseServiceOutcomeAspectDDL(),
		leaseServiceDispatchAspectDDL(),
	}
	ddls = append(ddls, LeaseDocDDLs()...)
	return append(ddls, RenewalDDLs()...)
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
			"DecideLeaseApplication{leaseAppKey, decision, reason?, unit?} records the landlord's leasing decision as a .decision aspect " +
			"{value (approved|declined), decidedAt (canonical-UTC RFC3339), reason? (optional decline rationale)}. A recorded decision is " +
			"TERMINAL: re-submitting the same decision is idempotent, but changing it to a different value is rejected (DecisionFinal) so a " +
			"decision cannot silently flip / oscillate; an approve is rejected (NotReadyToApprove) unless the application has been signed. It is the human gate the " +
			"listing-flip waits behind: the convergence lens reads .decision.value so an approval opens missing_listingLeased " +
			"(the unit leases) while a decline is a terminal disposition — nothing auto-leases on applicant-readiness alone. " +
			"On the FIRST approve only, it additionally CREATE-ONLY-stamps the .tenancy aspect {leaseStart, leaseEnd, " +
			"renewalOpensAt} (the tenancy-term fact the renewal target reads) from the unit's .listing.availableFrom + " +
			"leaseTermMonths (required alongside unit on that call; the unit is verified against the leaseapp's own " +
			"appliesToUnit link, never trusted from the payload alone): leaseStart = availableFrom; leaseEnd = leaseStart " +
			"+ leaseTermMonths (calendar months); renewalOpensAt = leaseEnd - the package's renewalWindow. Idempotent " +
			"re-approves and declines never touch .tenancy once it exists, so a landlord who approved, and a tenant who " +
			"later signs a renewal extending leaseEnd, is never silently truncated back to the original term. " +
			"SetApplicantProfile{leaseAppKey, unit, annualIncome, employmentStatus, employerName?, references?, hasCoApplicant?, " +
			"hasGuarantor?, guarantorName?, guarantorRelationship?, guarantorAnnualIncome?, coApplicantName?, coApplicantContact?} " +
			"captures the applicant's qualification profile so the landlord has something to decide on, split three ways along the " +
			"retention-class-key-custody-design.md §9.1 sensitivity boundary (one non-sensitive aspect sharing a sensitive aspect's " +
			"data map goes unreadable to every plain lens, so co-locating raw and derived facts is not an option). The applicant's " +
			"OWN raw financials (annualIncome, employmentStatus, employerName, guarantorRelationship, guarantorAnnualIncome) go to " +
			"the SENSITIVE .profile aspect (class applicantProfile — Contract #1 §1.5 namespacing; the LOCAL NAME stays profile), " +
			"custodied on the package's own underwritingRecord retention class (RetentionClasses) rather than the applicant's " +
			"identity — the record survives the applicant's erasure. Every THIRD-PARTY identifier — the guarantor's/co-applicant's " +
			"own name/contact (guarantorName, coApplicantName, coApplicantContact) AND the applicant's own references (who THEY " +
			"name, e.g. \"Prior landlord — Jane Doe\") — goes to the SENSITIVE .underwritingParties aspect, same class. Neither " +
			"sensitive aspect is ever projected by a lens. The op DERIVES the landlord-facing signals (incomeToRentMet — gross " +
			"monthly income ≥ 3× the unit's listing rent, read on demand; employmentVerified; referenceCount; hasCoApplicant; " +
			"hasGuarantor; guarantorIncomeToRentMet — the guarantor's own income ≥ 3× rent) into the NON-sensitive " +
			".applicationSignals aspect, which the three shipped lenses project so a landlord sees qualification without the raw " +
			"figures or the third-party identities. All three aspects are written in ONE mutation batch every time — a " +
			"SetApplicantProfile that wrote .profile without .applicationSignals would leave the landlord surface silently blind " +
			"to a submitted application, and an omitted .underwritingParties would leave a PRIOR submission's guarantor/" +
			"co-applicant/references fields stale on a re-submit that drops them. Each sensitive aspect's written key set is " +
			"STABLE: an omitted optional string writes as \"\" rather than being dropped (a future secure-lens column decrypting " +
			"a missing field is a spec mismatch, not an absent value), and a field is omitted only when it is structurally absent " +
			"(no guarantor ⇒ no guarantor fields at all; no references supplied ⇒ no references field). UNCONDITIONED upsert " +
			"(re-submittable — a re-submit overwrites all three aspects). It verifies unit is the application's appliesToUnit " +
			"target (the Withdraw precedent) and feeds no gap — capture + surface, not a convergence gate.",
		Script: leaseAppDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"applicant":{"type":"string","description":"vtx.identity.<NanoID> of the applicant this application is for (CreateLeaseApplication: required, validated alive; WithdrawLeaseApplication: required, verified via the applicationFor link, to free the per-(applicant, unit) guard link)."},` +
			`"unit":{"type":"string","description":"vtx.unit.<NanoID> of the location-domain unit this application is to lease (CreateLeaseApplication; required, validated alive). Also required on the FIRST DecideLeaseApplication approve (verified via the appliesToUnit link) so the op can read the unit's .listing economics and stamp the .tenancy aspect."},` +
			`"moveInDate":{"type":"string","description":"Requested move-in date, RFC3339 (CreateLeaseApplication; optional — present ⇒ writes the .terms aspect and requires leaseTermMonths)."},` +
			`"leaseTermMonths":{"type":"integer","description":"Requested lease term in months (CreateLeaseApplication; required when moveInDate is supplied)."},` +
			`"requestedRent":{"type":"number","description":"Applicant's offered monthly rent (CreateLeaseApplication; optional, only with moveInDate)."},` +
			`"leaseAppId":{"type":"string","description":"Optional bare NanoID for the application vertex (CreateLeaseApplication); absent → minted. The write-ahead seam, mirroring service-domain's instanceId."},` +
			`"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the application to sign (SignLease), withdraw (WithdrawLeaseApplication) or decide (DecideLeaseApplication); required, validated alive."},` +
			`"decision":{"type":"string","enum":["approved","declined"],"description":"The landlord's leasing decision (DecideLeaseApplication; required). approved opens the listing-leased gate (the unit leases); declined is a terminal disposition."},` +
			`"reason":{"type":"string","description":"Optional free-text rationale for a DecideLeaseApplication decline (applicant feedback + a fair-housing record). Stored on the .decision aspect and projected as the declineReason lens column; ignored on an approve."},` +
			`"annualIncome":{"type":"number","description":"The applicant's gross annual income (SetApplicantProfile; required, > 0). SENSITIVE — stored in the .profile aspect (underwritingRecord retention class), NEVER projected; only the derived incomeToRentMet boolean reaches the read model."},` +
			`"employmentStatus":{"type":"string","enum":["employed","self-employed","unemployed","student","retired"],"description":"The applicant's employment status (SetApplicantProfile; required). SENSITIVE — stored in .profile. employed / self-employed derive the projected employmentVerified=true."},` +
			`"employerName":{"type":"string","description":"The applicant's employer (SetApplicantProfile; optional). SENSITIVE — stored in .profile, never projected."},` +
			`"references":{"type":"array","items":{"type":"string"},"description":"The applicant's references, free-text (SetApplicantProfile; optional). SENSITIVE — each names a third party, so it is stored in .underwritingParties, never .profile; only the derived referenceCount is projected, never the entries. Omitted from the write entirely when empty."},` +
			`"hasCoApplicant":{"type":"boolean","description":"Whether the application has a co-applicant (SetApplicantProfile; optional, default false). NON-sensitive — stored + projected from .applicationSignals."},` +
			`"hasGuarantor":{"type":"boolean","description":"Whether the application has a guarantor (SetApplicantProfile; optional, default false). NON-sensitive — stored + projected from .applicationSignals."},` +
			`"guarantorName":{"type":"string","description":"The guarantor's name (SetApplicantProfile; optional, only with hasGuarantor). SENSITIVE — a third party's identifier, stored in .underwritingParties (underwritingRecord retention class), never projected."},` +
			`"guarantorRelationship":{"type":"string","description":"The guarantor's relationship to the applicant, e.g. parent (SetApplicantProfile; optional, only with hasGuarantor). SENSITIVE — stored in .profile, never projected."},` +
			`"guarantorAnnualIncome":{"type":"number","description":"The guarantor's gross annual income (SetApplicantProfile; optional, only with hasGuarantor, > 0). SENSITIVE — stored in .profile, NEVER projected; only the derived guarantorIncomeToRentMet boolean (in .applicationSignals) reaches the read model."},` +
			`"coApplicantName":{"type":"string","description":"The co-applicant's name (SetApplicantProfile; optional, only with hasCoApplicant). SENSITIVE — a third party's identifier, stored in .underwritingParties, never projected."},` +
			`"coApplicantContact":{"type":"string","description":"The co-applicant's contact (email / phone) (SetApplicantProfile; optional, only with hasCoApplicant). SENSITIVE — a third party's identifier, stored in .underwritingParties, never projected."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the created or signed application (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"applicant":             "Full vtx.identity.<NanoID> key of the applicant this application is for. CreateLeaseApplication requires it, validates the identity is alive, and writes the applicationFor link (the convergence link the lens walks). WithdrawLeaseApplication also requires it (verified via the applicationFor link) to reconstruct + free the per-(applicant, unit) guard link.",
			"unit":                  "Full vtx.unit.<NanoID> key of the location-domain unit being applied for. CreateLeaseApplication requires it, validates it is alive, and writes the appliesToUnit link (leaseapp→unit). The convergence lens walks it and projects the unit's address / rent as informational columns. Required (no unit-less application). WithdrawLeaseApplication also requires it (verified via the appliesToUnit link) to reconstruct + free the per-(applicant, unit) guard link. DecideLeaseApplication requires it on the FIRST approve only (verified the same way) to read the unit's .listing.availableFrom/leaseTermMonths and stamp the .tenancy aspect {leaseStart, leaseEnd, renewalOpensAt} — omitted on a decline or a re-approve (the .tenancy write is create-only).",
			"moveInDate":            "Optional requested move-in date (RFC3339). When supplied, CreateLeaseApplication writes the .terms aspect {moveInDate, leaseTermMonths, requestedRent?} and requires leaseTermMonths. Informational application detail (not read by the convergence lens).",
			"leaseTermMonths":       "Requested lease term in months. Required when moveInDate is supplied; written to the .terms aspect.",
			"requestedRent":         "Optional monthly rent the applicant offers. Written to the .terms aspect when supplied (only meaningful alongside moveInDate).",
			"leaseAppId":            "Optional bare NanoID (no dots / key segments) for the application vertex (vtx.leaseapp.<leaseAppId>) created by CreateLeaseApplication. Supplied by a caller that must know the key before commit (the write-ahead seam). Absent → minted with nanoid.new().",
			"leaseAppKey":           "Full vtx.leaseapp.<NanoID> key of the application to act on. SignLease validates it is alive and writes the .signature aspect (flipping missing_signature false); WithdrawLeaseApplication validates it is alive and soft-deletes it; DecideLeaseApplication validates it is alive and writes the .decision aspect; SetApplicantProfile validates it is alive and writes the .profile / .underwritingParties / .applicationSignals aspects in one batch. The caller lists it in ContextHint.Reads.",
			"annualIncome":          "The applicant's gross annual income (SetApplicantProfile; required, > 0). SENSITIVE: stored in the .profile aspect, custodied on the package's underwritingRecord retention class (RetentionClasses) rather than the applicant's identity, and NEVER projected. The op derives incomeToRentMet (gross monthly income ≥ 3× the unit's listing rent) from it into the non-sensitive .applicationSignals aspect, and only that boolean reaches the read model.",
			"employmentStatus":      "The applicant's employment status (SetApplicantProfile; required): employed | self-employed | unemployed | student | retired. SENSITIVE — stored in .profile. employed / self-employed derive the projected employmentVerified=true (an active income source); the rest are captured honestly and read as unverified.",
			"employerName":          "The applicant's employer name (SetApplicantProfile; optional). SENSITIVE — stored in the .profile aspect, never projected.",
			"references":            "The applicant's references as free-text strings (SetApplicantProfile; optional). SENSITIVE: each names a third party (e.g. \"Prior landlord — Jane Doe\"), so it is stored in the .underwritingParties aspect, not .profile. Blank entries are dropped; an empty result is omitted from the write entirely (no field, no DEK minted for nothing); only the derived referenceCount (the list length, in .applicationSignals) is projected, never the entries themselves.",
			"hasCoApplicant":        "Whether the application includes a co-applicant (SetApplicantProfile; optional, default false). NON-sensitive — stored in .applicationSignals and projected verbatim as a derived qualification signal.",
			"hasGuarantor":          "Whether the application is backed by a guarantor (SetApplicantProfile; optional, default false). NON-sensitive — stored in .applicationSignals and projected verbatim as a derived qualification signal.",
			"guarantorName":         "The guarantor's name (SetApplicantProfile; optional, captured only when hasGuarantor). SENSITIVE: a third party's identifier — the guarantor never applied and has no identity of their own to custody it on, so it is stored in the .underwritingParties aspect (SAME underwritingRecord retention class as .profile, kept as a SEPARATE aspect so a later fire can rehome it without touching the financial record) and NEVER projected.",
			"guarantorRelationship": "The guarantor's relationship to the applicant, e.g. parent / employer (SetApplicantProfile; optional, captured only when hasGuarantor). SENSITIVE — stored in .profile (it describes the applicant's own qualification story, not a third-party identifier), never projected.",
			"guarantorAnnualIncome": "The guarantor's gross annual income (SetApplicantProfile; optional, captured only when hasGuarantor, > 0). SENSITIVE — stored in .profile, NEVER projected. The op derives guarantorIncomeToRentMet (guarantor gross monthly ≥ 3× the unit's listing rent — the standard reason a guarantor backs a thin-income application) from it into .applicationSignals, and only that boolean reaches the read model.",
			"coApplicantName":       "The co-applicant's name (SetApplicantProfile; optional, captured only when hasCoApplicant). SENSITIVE: a third party's identifier, stored in the .underwritingParties aspect (underwritingRecord retention class), never projected.",
			"coApplicantContact":    "The co-applicant's contact — email or phone (SetApplicantProfile; optional, captured only when hasCoApplicant). SENSITIVE: a third party's identifier, stored in .underwritingParties, never projected.",
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
				Name:    "DecideLeaseApplication — landlord approves an application (first approve stamps .tenancy)",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>", "decision": "approved", "unit": "vtx.unit.<unitNanoID>"},
				ExpectedOutcome: "Validates the application is alive and the decision is approved|declined. Writes the .decision aspect " +
					"{value: <decision>, decidedAt: <op.submittedAt, canonical UTC>} on the application (root stays {} — D5). " +
					"A recorded decision is terminal: the same value re-submits idempotently, a different value is rejected " +
					"(DecisionFinal); approve is rejected (NotReadyToApprove) unless the application is signed. approved opens the " +
					"listing-leased convergence (the unit leases). Because no .tenancy aspect exists yet, this FIRST approve also " +
					"verifies unit against the appliesToUnit link, reads the unit's .listing {availableFrom, leaseTermMonths}, and " +
					"CREATE-ONLY-writes .tenancy {leaseStart: availableFrom, leaseEnd: availableFrom + leaseTermMonths, " +
					"renewalOpensAt: leaseEnd - renewalWindow} — the fact the leaseExpiry/renewalComplete targets read. Emits " +
					"leaseapp.applicationDecided{leaseAppKey, decision}. Returns primaryKey. Rejects a non-existent application " +
					"(UnknownLeaseApplication), an out-of-enum decision (BadDecision), or (on the first approve) a unit that is not " +
					"this application's unit (UnitMismatch) or one with no .listing (NoListing).",
			},
			{
				Name:    "DecideLeaseApplication — re-approve never re-derives .tenancy",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>", "decision": "approved"},
				ExpectedOutcome: "A SECOND approved submission for an application that already carries a .tenancy aspect (e.g. one " +
					"SignRenewal has since extended) is the idempotent re-submit path: the .decision aspect re-writes to the same " +
					"value, but the create-only .tenancy guard sees the aspect already present and skips the stamp entirely — unit " +
					"is not required on this call. This is the invariant that keeps a routine re-approve from truncating an " +
					"extended leaseEnd back to the original term.",
			},
			{
				Name:    "DecideLeaseApplication — landlord declines with a reason",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>", "decision": "declined", "reason": "Income below the 3x-rent threshold."},
				ExpectedOutcome: "As above, but the optional reason is stored on the .decision aspect ({value, decidedAt, reason}) and projected " +
					"as the declineReason lens column the applicant FE renders on the declined banner. The decline is terminal — a " +
					"different later decision is rejected (DecisionFinal); a same-value re-submission can update the reason. reason is ignored on an approve, and no .tenancy is ever written on a decline.",
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
					"guarantorIncomeToRentMet (120000/12 = 10000 ≥ 3× rent?). Writes THREE aspects in one batch: the SENSITIVE .profile " +
					"(class applicantProfile — annualIncome, employmentStatus, employerName, guarantorRelationship, " +
					"guarantorAnnualIncome — never projected, custodied on the underwritingRecord retention class), the SENSITIVE " +
					".underwritingParties (references, guarantorName — every field here names a third party, same class, never " +
					"projected), and the NON-sensitive .applicationSignals (incomeToRentMet, employmentVerified=true, referenceCount=2, " +
					"hasCoApplicant=false, hasGuarantor=true, guarantorIncomeToRentMet, submittedAt) that the three shipped lenses " +
					"project. UNCONDITIONED upsert (re-submittable — a re-submit overwrites all three aspects, including clearing a " +
					"PRIOR submission's guarantor/co-applicant/references fields on a re-submit that drops them). Emits " +
					"leaseapp.profileSubmitted{leaseAppKey}. Returns primaryKey. Rejects a non-existent application, a unit that " +
					"is not the application's unit (UnitMismatch), a non-positive annualIncome, or an out-of-enum " +
					"employmentStatus.",
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

// profileAspectDDL declares the .profile aspect (class applicantProfile) — the
// RETAINED FINANCIAL half of SetApplicantProfile's three-way split (the
// leaseapp vertexType DDL owns the script). Declaration-only.
//
// The CLASS is applicantProfile, not the bare "profile" — Contract #1 §1.5
// makes aspect-type canonicalNames globally unique, and "profile" is a generic
// word every other package that has one (providerProfile, studioProfile,
// instructorProfile, serviceProviderProfile) namespaces for exactly that
// reason (clinic-domain/ddls.go states the rule). The LOCAL NAME stays
// "profile" — the key shape vtx.leaseapp.<NanoID>.profile does not change.
//
// SENSITIVE, custodied on the underwritingRecord retention class
// (RetentionClasses) rather than the applicant's own identity: a landlord's
// underwriting decision is a business record that outlives the applicant's
// erasure request, so its DEK lives on a holder the applicant's
// ShredIdentityKey cannot reach — after that shred the record is still
// readable, pseudonymized (retention-class-key-custody-design.md §6.4). Step
// 6.5 encrypts the WHOLE aspect data map, which is why the derived,
// non-identifying qualification signals (incomeToRentMet, employmentVerified,
// referenceCount, hasCoApplicant, hasGuarantor, guarantorIncomeToRentMet,
// submittedAt) live on the sibling .applicationSignals aspect instead of here,
// and why every THIRD-PARTY identifier — the guarantor's/co-applicant's own
// name/contact, AND the applicant's references (who THEY name) — lives on the
// sibling .underwritingParties aspect rather than here — a different
// population, held under the SAME class but kept separable (§8.7). This
// aspect carries ONLY the applicant's own raw financials + the guarantor's
// relationship/income (which describe the applicant's qualification story,
// not a third party's identity), and NO shipped lens reads it — the retained
// fields are captured + custodied, not yet decrypted back to any reader.
func profileAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "applicantProfile",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetApplicantProfile"},
		Sensitive:         true,
		Custody:           pkgmgr.CustodySpec{Kind: pkgmgr.CustodyKindRetentionClass, RetentionClass: underwritingRecordRetentionClass},
		Description: "Applicant qualification-profile aspect (lease-signing), the RETAINED FINANCIAL half of SetApplicantProfile's " +
			"three-way split. Stored as vtx.leaseapp.<NanoID>.profile (class applicantProfile — namespaced per Contract #1 §1.5; " +
			"the LOCAL NAME stays profile) = {annualIncome, employmentStatus, employerName, guarantorRelationship, " +
			"guarantorAnnualIncome} — ONLY the applicant's own raw financials plus the guarantor's relationship/income (which " +
			"describe the applicant's OWN qualification story, not a third-party identity; the guarantor's NAME, the " +
			"co-applicant's identifiers, and the applicant's own references all live on the sibling .underwritingParties " +
			"aspect instead, since they name a third party). SENSITIVE: its DEK is custodied on the underwritingRecord " +
			"retention-class holder (RetentionClasses), not on the applicant's identity — the record survives " +
			"ShredIdentityKey on its applicant as a pseudonymized retained record. The derived, non-identifying " +
			"qualification signals (incomeToRentMet / employmentVerified / referenceCount / hasCoApplicant / hasGuarantor / " +
			"guarantorIncomeToRentMet / submittedAt) live on the sibling .applicationSignals aspect, which is what " +
			"leaseApplicationComplete / leaseApplicationsRead / landlordLeaseApplicationsRead project. STABLE WRITTEN " +
			"SHAPE: employerName writes as \"\" when omitted (not dropped) so a later per-field secure column never meets a " +
			"missing key; guarantorRelationship/guarantorAnnualIncome are omitted only when there is no guarantor at all " +
			"(structurally absent), and always written (guarantorRelationship as \"\" if unset) when hasGuarantor is true. No " +
			"shipped lens reads this aspect's content. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"annualIncome":{"type":"number"},"employmentStatus":{"type":"string"},"employerName":{"type":"string"},` +
			`"guarantorRelationship":{"type":"string"},"guarantorAnnualIncome":{"type":"number"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"annualIncome":          "The applicant's gross annual income (RAW PHI-adjacent financial data — never projected; only the derived incomeToRentMet boolean, on .applicationSignals, reaches the read model).",
			"employmentStatus":      "The applicant's employment status: employed | self-employed | unemployed | student | retired.",
			"employerName":          "The applicant's employer. Written as \"\" when not supplied (stable shape).",
			"guarantorRelationship": "The guarantor's relationship to the applicant, e.g. parent. Present only when the applicant has a guarantor; written as \"\" when a guarantor exists but no relationship was supplied.",
			"guarantorAnnualIncome": "The guarantor's gross annual income. Present only when the applicant has a guarantor; never projected — only the derived guarantorIncomeToRentMet boolean (.applicationSignals) reaches the read model.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "applicant qualification-profile aspect",
				Payload:         map[string]any{"annualIncome": 96000, "employmentStatus": "employed", "employerName": "Acme Corp"},
				ExpectedOutcome: "Stored ENCRYPTED as vtx.leaseapp.<NanoID>.profile, written by SetApplicantProfile, DEK custodied on the underwritingRecord retention-class holder. Never projected by any lens.",
			},
		},
	}
}

// underwritingPartiesAspectDDL declares the .underwritingParties aspect (class
// underwritingParties) — the THIRD-PARTY IDENTIFIER half of
// SetApplicantProfile's three-way split (the leaseapp vertexType DDL owns the
// script). Declaration-only.
//
// SENSITIVE, SAME underwritingRecord retention-class custody as .profile — but
// a SEPARATE aspect, deliberately: the guarantor and co-applicant never
// applied, never consented to this record, and have no identity of their own
// this platform could custody their DEK on (retention-class-key-custody-
// design.md §8.7 rejects minting an unclaimed identity for a person who can
// never claim it — that invents machinery to hold data they cannot reach).
// Splitting them from .profile means a later fire can rehome or independently
// erase the third-party identifiers without touching the applicant's own
// financial record. references also lands here, not on .profile: a reference
// (e.g. "Prior landlord — Jane Doe") names a third party, exactly the
// population this aspect exists to hold — it is never the applicant's own
// raw financial data.
func underwritingPartiesAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "underwritingParties",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetApplicantProfile"},
		Sensitive:         true,
		Custody:           pkgmgr.CustodySpec{Kind: pkgmgr.CustodyKindRetentionClass, RetentionClass: underwritingRecordRetentionClass},
		Description: "Underwriting third-party identifiers aspect (lease-signing), the THIRD-PARTY half of SetApplicantProfile's " +
			"three-way split. Stored as vtx.leaseapp.<NanoID>.underwritingParties (class underwritingParties) = {references, " +
			"guarantorName, coApplicantName, coApplicantContact} — the applicant's references (who THEY name, e.g. " +
			"\"Prior landlord — Jane Doe\") plus the guarantor's and co-applicant's OWN identifiers: every field here names a " +
			"third party, a population distinct from the applicant's own raw financials (which stay on the sibling .profile " +
			"aspect). SENSITIVE, custodied on the SAME underwritingRecord retention-class holder as .profile (RetentionClasses) " +
			"— kept as a SEPARATE aspect rather than folded into .profile so a later fire can rehome or independently address " +
			"these third-party identifiers without touching the applicant's financial record. Neither the guarantor nor the " +
			"co-applicant has an identity of their own this record is custodied on (§8.7): they never applied and cannot " +
			"exercise ShredIdentityKey against this data. STABLE WRITTEN SHAPE: guarantorName is present (defaulting to \"\" " +
			"if unset) exactly when the applicant has a guarantor; coApplicantName/coApplicantContact are present (defaulting " +
			"to \"\" if unset) exactly when the applicant has a co-applicant — a field is omitted only when the corresponding " +
			"party is structurally absent (no guarantor / no co-applicant at all). references is omitted when the applicant " +
			"supplied none. UNCONDITIONED upsert: SetApplicantProfile writes this aspect on every submission, even one with no " +
			"references and neither a guarantor nor a co-applicant (an empty {}) — a re-submit that drops a PRIOR submission's " +
			"guarantor/co-applicant/references fields must clear them, which an omitted mutation cannot do. No shipped lens " +
			"reads this aspect's content. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"references":{"type":"array","items":{"type":"string"}},"guarantorName":{"type":"string"},` +
			`"coApplicantName":{"type":"string"},"coApplicantContact":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"references":         "The applicant's references, free-text (each names a third party, e.g. \"Prior landlord — Jane Doe\"). Omitted when the applicant supplied none. Never projected; only the derived count (.applicationSignals.referenceCount) reaches the read model.",
			"guarantorName":      "The guarantor's name. Present only when the applicant has a guarantor; written as \"\" when a guarantor exists but no name was supplied. Never projected.",
			"coApplicantName":    "The co-applicant's name. Present only when the applicant has a co-applicant; written as \"\" when unset. Never projected.",
			"coApplicantContact": "The co-applicant's contact — email or phone. Present only when the applicant has a co-applicant; written as \"\" when unset. Never projected.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "underwriting third-party identifiers aspect",
				Payload:         map[string]any{"guarantorName": "Pat Guarantor"},
				ExpectedOutcome: "Stored ENCRYPTED as vtx.leaseapp.<NanoID>.underwritingParties, written by SetApplicantProfile, DEK custodied on the SAME underwritingRecord retention-class holder as .profile. Never projected by any lens.",
			},
		},
	}
}

// applicationSignalsAspectDDL declares the .applicationSignals aspect (class
// applicationSignals) — the DERIVED, non-identifying half of
// SetApplicantProfile's three-way split (the leaseapp vertexType DDL owns the
// script). Declaration-only.
//
// NON-sensitive, no custody: this aspect exists because step 6.5 encrypts an
// entire aspect's data map, so these plainly operational booleans/counts
// sharing the sensitive .profile aspect's data would be encrypted along with
// the raw financials and unreadable to every plain lens — the aspect-level
// sensitivity boundary forces the split. leaseApplicationComplete,
// leaseApplicationsRead, and landlordLeaseApplicationsRead (this package's own
// lenses) all read this aspect for the qualification-signal columns; the
// renewal chain's VerifyGuarantor/SignRenewal ops and renewalComplete/
// renewalsRead lenses read its hasGuarantor field.
func applicationSignalsAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "applicationSignals",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SetApplicantProfile"},
		Description: "Applicant qualification-signals aspect (lease-signing), the DERIVED half of SetApplicantProfile's " +
			"three-way split. Stored as vtx.leaseapp.<NanoID>.applicationSignals (class applicationSignals) = {submittedAt, " +
			"incomeToRentMet?, employmentVerified, referenceCount, hasCoApplicant, hasGuarantor, guarantorIncomeToRentMet?} — " +
			"the OPERATIONAL, non-identifying signals a landlord's read model surfaces; every field is computed by the op, " +
			"never a raw fact. It is a SEPARATE aspect from .profile / .underwritingParties because step 6.5 encrypts an " +
			"entire aspect's data map: a non-sensitive field sharing a sensitive aspect's data would be encrypted along with " +
			"the raw financials / third-party identifiers and unreadable to every plain lens, so the aspect-level sensitivity " +
			"boundary forces this split. Consumed by leaseApplicationComplete, leaseApplicationsRead, and " +
			"landlordLeaseApplicationsRead (this package's own lenses, D1.5 Rec C) for the seven qualification-signal " +
			"columns, and by the renewal chain (VerifyGuarantor/SignRenewal op scripts, renewalComplete/renewalsRead lenses) " +
			"for hasGuarantor. incomeToRentMet / guarantorIncomeToRentMet are omitted (not written) when the unit's listing " +
			"rent is unknown at submit time — an unknown income-to-rent signal, not a false one. Declaration-only: no op " +
			"handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"submittedAt":{"type":"string"},"incomeToRentMet":{"type":"boolean"},"employmentVerified":{"type":"boolean"},` +
			`"referenceCount":{"type":"integer"},"hasCoApplicant":{"type":"boolean"},"hasGuarantor":{"type":"boolean"},` +
			`"guarantorIncomeToRentMet":{"type":"boolean"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"submittedAt":              "RFC3339 instant the profile was (re-)submitted (canonical UTC, = op.submittedAt). Operational — projected.",
			"incomeToRentMet":          "Whether the applicant's gross monthly income ≥ 3× the unit's listing rent. Omitted when the unit's listing rent is unknown at submit time.",
			"employmentVerified":       "Whether the applicant reported an active income source (employed / self-employed). Operational — projected.",
			"referenceCount":           "Count of the applicant's supplied references. Operational — projected.",
			"hasCoApplicant":           "Whether the application has a co-applicant. Operational — projected.",
			"hasGuarantor":             "Whether the application has a guarantor. Operational — projected; also read by the renewal chain's VerifyGuarantor/SignRenewal ops.",
			"guarantorIncomeToRentMet": "Whether the guarantor's gross monthly income ≥ 3× the unit's listing rent. Omitted when there is no guarantor, or the unit's listing rent is unknown at submit time.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "applicant qualification-signals aspect",
				Payload:         map[string]any{"submittedAt": "2026-06-27T10:00:00Z", "employmentVerified": true, "referenceCount": 2, "hasCoApplicant": false, "hasGuarantor": true},
				ExpectedOutcome: "Stored PLAINTEXT as vtx.leaseapp.<NanoID>.applicationSignals, written by SetApplicantProfile. Read by leaseApplicationComplete / leaseApplicationsRead / landlordLeaseApplicationsRead.",
			},
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
