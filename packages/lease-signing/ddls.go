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
//   - `decidedProfileSnapshot` — the fair-housing preservation aspect-type DDL
//     DecideLeaseApplication write-gates (the leaseapp vertexType script owns
//     the write). SENSITIVE, same underwritingRecord retention class as
//     `applicantProfile`. CREATE-ONLY-stamped on the FIRST .decision write of
//     either value, copying the then-current .profile / .underwritingParties
//     / .applicationSignals data maps so the record of what the landlord
//     actually saw at decision time survives a later SetApplicantProfile
//     re-submission.
//   - `tenantName` — the executed-lease party-name aspect-type DDL SignLease
//     write-gates (the leaseapp vertexType script owns the write). SENSITIVE,
//     custodied on its OWN executedLeaseRecord retention class (a different
//     obligation and population from underwritingRecord). CREATE-ONLY-stamped
//     once, at signing, snapshotting the applicant identity's own .name;
//     absent when the applicant had no live .name to snapshot. See
//     RetentionClasses().
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
		decidedProfileSnapshotAspectDDL(),
		tenantNameAspectDDL(),
		tenancyNoticeAspectDDL(),
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
		PermittedCommands: []string{"CreateLeaseApplication", "SignLease", "WithdrawLeaseApplication", "DecideLeaseApplication", "SetApplicantProfile", "BackfillLeaseTerms", "ReassignLeaseUnit", "EndTenancy", "GiveNotice", "RecordApplicationLoss"},
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
			"(applicationFor link) — the complement to the duplicate-application guard so an applicant can back out + re-apply; " +
			"an APPROVED application is an executed lease (its account, balance and rent clause hang off it, the unit is leased) " +
			"and is refused (AlreadyApproved), a declined one stays withdrawable. " +
			"DecideLeaseApplication{leaseAppKey, decision, reason?, unit?} records the landlord's leasing decision as a .decision aspect " +
			"{value (approved|declined), decidedAt (canonical-UTC RFC3339), reason? (optional decline rationale)} — the aspect's value is " +
			"approved|declined|lost, the third recorded by RecordApplicationLoss (below), never submitted by a landlord. A recorded decision is " +
			"TERMINAL: re-submitting the same decision is idempotent, but changing it to a different value is rejected (DecisionFinal) so a " +
			"decision cannot silently flip / oscillate; an approve is rejected (NotReadyToApprove) unless the application has been signed. It is the human gate the " +
			"listing-flip waits behind: the convergence lens reads .decision.value so an approval opens missing_listingLeased " +
			"(the unit leases) while a decline is a terminal disposition — nothing auto-leases on applicant-readiness alone. " +
			"On the FIRST approve only, it additionally CREATE-ONLY-stamps the .tenancy aspect {leaseStart, leaseEnd, " +
			"renewalOpensAt, rentAmount?} (the tenancy-term fact the renewal target reads) derived from the " +
			"application's OWN .terms {moveInDate, leaseTermMonths, requestedRent} first, falling back field-by-field " +
			"to the unit's .listing.availableFrom / leaseTermMonths / rentAmount wherever .terms carries nothing (a " +
			"bare applicant+unit application with no moveInDate; unit is required alongside on that call as the " +
			"fallback source, verified against the leaseapp's own appliesToUnit link, never trusted from the payload " +
			"alone): leaseStart = terms.moveInDate else listing.availableFrom (a bare YYYY-MM-DD is read as midnight " +
			"UTC); leaseEnd = leaseStart + leaseTermMonths (calendar months, terms.leaseTermMonths else " +
			"listing.leaseTermMonths); renewalOpensAt = leaseEnd - the package's renewalWindow; rentAmount = " +
			"terms.requestedRent else listing.rentAmount (numeric > 0), omitted from .tenancy entirely when neither " +
			"exists. The approval commits to what the applicant actually asked for — never silently clamped to the " +
			"listing's availableFrom. Idempotent re-approves and declines never touch .tenancy once it exists, so a " +
			"landlord who approved, and a tenant who later signs a renewal extending leaseEnd, is never silently " +
			"truncated back to the original term. (SignRenewal also records termStart on .tenancy and may rewrite " +
			"rentAmount; this op writes rentAmount only from the sources above.) " +
			"On the FIRST decision of EITHER value (approve or decline), it also CREATE-ONLY-stamps a .decidedProfileSnapshot " +
			"aspect (class decidedProfileSnapshot, SENSITIVE, same underwritingRecord retention class as .profile) copying the " +
			"then-current .profile / .underwritingParties / .applicationSignals data maps (each keyed under its own name, " +
			"{} when the corresponding aspect was never submitted) — the fair-housing preservation record of what the " +
			"landlord actually saw when they decided, since SetApplicantProfile stays a freely re-submittable upsert and a " +
			"later submission would otherwise silently overwrite it. A re-decision (idempotent re-submit or a later approve " +
			"after a decline is rejected by the terminal-decision guard) never re-derives or overwrites the snapshot. " +
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
			"target (the Withdraw precedent) and feeds no gap — capture + surface, not a convergence gate. " +
			"BackfillLeaseTerms{leaseAppKey} is operator-granted (never person-facing) and repairs an approved " +
			"application carrying no requestedRent — a .terms aspect never written, or written before " +
			"CreateLeaseApplication's unit-listing-rent fallback existed (0.31.14, this package) — so " +
			"leaseRentSettlementSpec (semantic-contracts) has an agreed rent to bill and can open its account/clause " +
			"gaps. leaseRentSettlementSpec's own missing_terms gap dispatches it automatically over every such " +
			"lease; an operator may also run it by hand. It resolves the application's OWN appliesToUnit link " +
			"(never a payload field, the leaseapp_unit resolver's forgery-resistance rationale) and writes " +
			"{requestedRent: unit.listing.rentAmount} onto .terms, preserving any moveInDate/leaseTermMonths " +
			"already present; no-ops cleanly if requestedRent is already set (mirrors BackfillPatientRegistration's " +
			"own already-present no-op, clinic-domain). " +
			"ReassignLeaseUnit{leaseAppKey, newUnitKey} is operator-granted (never person-facing) and re-points a live " +
			"application's appliesToUnit link to a different unit — the repair for a lease whose unit was tombstoned " +
			"(TombstoneLocation does not cascade; the SetMenuItemLocation / ReassignSession repair shape), and an " +
			"ordinary move of a live application besides. It resolves the application's CURRENT appliesToUnit link and " +
			"its applicant's applicationFor link itself (never payload fields), tombstones the old link CAS-guarded on " +
			"its own revision (two concurrent re-points to different units RevisionConflicts rather than leaving two " +
			"live links), and creates-or-revives lnk.leaseapp.<id>.appliesToUnit.unit.<newUnitId>. It re-keys the " +
			"per-(applicant, unit) duplicate-application guard for the NEW pair — CreateLeaseApplication's own three-way " +
			"alive/absent/tombstoned block — and FREES (tombstones) the VACATED (applicant, oldUnit) guard: the guard's " +
			"contract is at most one live application per (applicant, unit), so a moved lease no longer justifies holding " +
			"the pair it left, and a later re-apply or a move back to that unit must not collide with its own stale guard. " +
			"No-ops cleanly when the application already applies to newUnitKey. Emits " +
			"leaseapp.unitReassigned{leaseAppKey, oldUnitKey, newUnitKey}. " +
			"EndTenancy{leaseAppKey} is operator-granted (never person-facing) and records that a lease term ended — " +
			"the directOp the tenancyEnd target dispatches once a signed, approved tenancy's term has lapsed with " +
			"no OPEN renewal for that cycle or with a recorded notice (an open renewal holds the term; a cancelled or " +
			"never-opened one does not; a notice overrides the hold), and runnable by hand. It reads the application + " +
			"its .tenancy (both REQUIRED declared reads — the gap only opens on a leaseapp with a tenancy; an undeclared " +
			"or absent .tenancy is refused NoTenancy, never lazily read) and its .notice (a declared OptionalRead: the " +
			"term's effective end is the recorded move-out when a notice precedes leaseEnd, else leaseEnd — a submitter " +
			"that never declared .notice gets the leaseEnd fallback, never a lazy read), refuses NotYetEnded when " +
			"submittedAt is before that end (write-path honesty — the op does not trust the dispatcher's clock; the " +
			"refusal names the end by its UTC calendar date), no-ops with zero mutations when endedAt is already set, " +
			"and otherwise rewrites .tenancy with every existing field preserved plus endedAt = that end (the term " +
			"ended on its own recorded date, never the fire instant), pinned to the hydrated .tenancy revision so a " +
			"SignRenewal extension that lands between hydration and commit conflicts instead of being overwritten. It " +
			"does not walk renewals — the open-renewal hold is the lens's dispatch gate. Emits " +
			"leaseapp.tenancyEnded{leaseAppKey, leaseEnd, endedAt}. Once endedAt is set, SignRenewal refuses TenancyEnded, " +
			"leaseApplicationComplete's applicant gaps and listing flip close (an ended tenancy is terminal, the decline's " +
			"shape — a relisted unit is never re-leased to the ended tenant), leaseExpiry opens no cycle, and tenancyEnd's " +
			"own missing_relist relists the unit unless another approved tenancy now holds it. " +
			"GiveNotice{leaseAppKey, moveOutDate} records that the tenancy ends EARLY, on a move-out date inside the " +
			"term — the tenant's own notice (consumer scope=self, the acting identity is this application's applicant via " +
			"the applicationFor link), the landlord's early end (the same self grant, the acting identity manages the " +
			"application's own unit), or an operator's by hand — writing the .notice aspect {moveOutAt, givenAt, givenBy} " +
			"CREATE-ONLY (a recorded notice is never changed here). It reads the application, its .tenancy and its " +
			".signature (REQUIRED declared reads — NoTenancy on an undecided application, LeaseNotSigned on an unsigned " +
			"one) and its .notice (a declared OptionalRead — NoticeAlreadyGiven when live); refuses TenancyEnded once " +
			"endedAt is set, MoveOutBeforeToday when the move-out precedes the UTC calendar day of submittedAt (a " +
			"same-day move-out is admitted), MoveOutBeforeStart when it is on or before leaseStart, and MoveOutAfterEnd " +
			"when it is on or after leaseEnd (the term ends on its own date; nothing to record). moveOutDate is a date-only " +
			"fact — a bare YYYY-MM-DD, or an RFC3339 instant read as its UTC calendar day — stored as that day's midnight " +
			"UTC. Under an OPEN renewal cycle the notice " +
			"is admitted — the tenant declines by leaving; SignRenewal refuses NoticeGiven and the tenancyEnd target ends " +
			"the term on the move-out even while the cycle is open. Emits leaseapp.noticeGiven{leaseAppKey, moveOutAt, " +
			"givenBy}. " +
			"RecordApplicationLoss{leaseAppKey} is operator-granted (never person-facing) and records that an application lost its " +
			"unit to another applicant — the directOp leaseApplicationComplete's missing_lossRecorded gap dispatches once an " +
			"undecided application's unit reads leased, and runnable by hand via the CLI under the primordial admin (as EndTenancy; " +
			"Loupe's console identity holds consoleOperator, not operator). It reads the application (a REQUIRED declared read) " +
			"and its .decision (declared optionalReads — absent is the gap's own premise); ANY recorded decision (lost, approved " +
			"or declined) is an idempotent no-op with zero mutations and no event. It resolves the unit from the application's " +
			"OWN appliesToUnit link (never a payload field; NoUnit when there is no live endpoint) and reads that unit's .listing " +
			"as a follow-up, refusing UnitNotLeased when the listing is absent, deleted, or its status is not leased (write-path " +
			"honesty — an operator cannot mark an application lost against an available unit). Otherwise it writes .decision " +
			"{value: lost, decidedAt (canonical-UTC RFC3339, the instant the loss was recorded)} — no reason, no " +
			".decidedProfileSnapshot (nobody decided this application) — conditioned CreateOnly by the declared absence, so a " +
			"landlord decision racing the dispatch conflicts and the re-dispatch reads it as decided. Emits " +
			"leaseapp.applicationLost{leaseAppKey, unitKey}. A lost application is terminal and never revived: " +
			"DecideLeaseApplication refuses DecisionFinal on it, the four applicant gaps and lost_to_rival read the recorded " +
			"value across the unit's later relist, and WithdrawLeaseApplication still accepts it (freeing the per-(applicant, " +
			"unit) guard so the applicant may re-apply to the relisted unit afresh).",
		Script: leaseAppDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"applicant":{"type":"string","description":"vtx.identity.<NanoID> of the applicant this application is for (CreateLeaseApplication: required, validated alive; WithdrawLeaseApplication: required, verified via the applicationFor link, to free the per-(applicant, unit) guard link)."},` +
			`"unit":{"type":"string","description":"vtx.unit.<NanoID> of the location-domain unit this application is to lease (CreateLeaseApplication; required, validated alive). Also required on the FIRST DecideLeaseApplication approve (verified via the appliesToUnit link) so the op can read the unit's .listing as the fallback source for the .tenancy aspect (derived from the application's own .terms first)."},` +
			`"moveInDate":{"type":"string","description":"Requested move-in date, RFC3339 or a bare YYYY-MM-DD read as midnight UTC (CreateLeaseApplication; optional — present ⇒ writes the .terms aspect and requires leaseTermMonths). Stored normalized to the RFC3339 instant; a value that parses as neither is refused."},` +
			`"leaseTermMonths":{"type":"integer","description":"Requested lease term in months — a whole number ≥ 1 (CreateLeaseApplication; required when moveInDate is supplied; a zero, negative or fractional count is refused InvalidTerms, as is one read back at the first approve)."},` +
			`"requestedRent":{"type":"number","description":"Applicant's offered monthly rent, > 0 when supplied (CreateLeaseApplication; optional, only with moveInDate; zero or negative is refused InvalidTerms). Omitted → falls back to the unit's own listed rent (unit.listing.rentAmount) when the unit has one."},` +
			`"leaseAppId":{"type":"string","description":"Optional bare NanoID for the application vertex (CreateLeaseApplication); absent → minted. The write-ahead seam, mirroring service-domain's instanceId."},` +
			`"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of the application to sign (SignLease), withdraw (WithdrawLeaseApplication), decide (DecideLeaseApplication), backfill (BackfillLeaseTerms), re-point at a different unit (ReassignLeaseUnit), whose lease term to record as ended (EndTenancy), whose tenancy to give notice on (GiveNotice), or whose loss of its unit to another applicant to record (RecordApplicationLoss); required, validated alive."},` +
			`"moveOutDate":{"type":"string","description":"The date the tenant moves out — a DATE-ONLY fact: a bare YYYY-MM-DD, or an RFC3339 instant read as its UTC calendar day (the clock part is dropped; 2027-04-01T00:00:00-07:00 records 2027-04-01) (GiveNotice; required). Stored as .notice.moveOutAt = that day's midnight UTC. Must be today (the UTC calendar day of submittedAt) or later, after the term's leaseStart and before its leaseEnd; a value that parses as neither shape is refused InvalidArgument."},` +
			`"newUnitKey":{"type":"string","description":"vtx.unit.<NanoID> of the unit to re-point the application's appliesToUnit link at (ReassignLeaseUnit; required, validated alive). The operator repair for an application whose unit was tombstoned."},` +
			`"decision":{"type":"string","enum":["approved","declined"],"description":"The landlord's leasing decision (DecideLeaseApplication; required). approved opens the listing-leased gate (the unit leases); declined is a terminal disposition. The recorded .decision aspect's value is approved|declined|lost — lost is written by RecordApplicationLoss when the unit went to another applicant, never submitted here."},` +
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
			"unit":                  "Full vtx.unit.<NanoID> key of the location-domain unit being applied for. CreateLeaseApplication requires it, validates it is alive, and writes the appliesToUnit link (leaseapp→unit). The convergence lens walks it and projects the unit's address / rent as informational columns. Required (no unit-less application). WithdrawLeaseApplication also requires it (verified via the appliesToUnit link) to reconstruct + free the per-(applicant, unit) guard link. DecideLeaseApplication requires it on the FIRST approve only (verified the same way) as the fallback source for the .tenancy aspect {leaseStart, leaseEnd, renewalOpensAt, rentAmount?} — derived from the application's own .terms first, the unit's .listing.availableFrom/leaseTermMonths/rentAmount only where .terms carries nothing — omitted on a decline or a re-approve (the .tenancy write is create-only).",
			"moveInDate":            "Optional requested move-in date — RFC3339, or a bare YYYY-MM-DD read as midnight UTC; stored normalized to the RFC3339 instant, and a value that parses as neither is refused. When supplied, CreateLeaseApplication writes the .terms aspect {moveInDate, leaseTermMonths, requestedRent?} and requires leaseTermMonths. The first approve signs the lease on these terms (leaseStart = moveInDate).",
			"leaseTermMonths":       "Requested lease term in months — a whole number ≥ 1 (a zero, negative or fractional count is refused InvalidTerms at CreateLeaseApplication, and again at the first approve if a stored value fails the test). Required when moveInDate is supplied; written to the .terms aspect and signed on at the first approve (leaseEnd = moveInDate + this many calendar months).",
			"requestedRent":         "Optional monthly rent the applicant offers, > 0 when supplied (zero or negative is refused InvalidTerms). Written to the .terms aspect when supplied (only meaningful alongside moveInDate); the first approve records it as .tenancy.rentAmount, falling back to the unit's listed rent where it is absent or non-positive.",
			"leaseAppId":            "Optional bare NanoID (no dots / key segments) for the application vertex (vtx.leaseapp.<leaseAppId>) created by CreateLeaseApplication. Supplied by a caller that must know the key before commit (the write-ahead seam). Absent → minted with nanoid.new().",
			"leaseAppKey":           "Full vtx.leaseapp.<NanoID> key of the application to act on. SignLease validates it is alive and writes the .signature aspect (flipping missing_signature false); WithdrawLeaseApplication validates it is alive and soft-deletes it; DecideLeaseApplication validates it is alive and writes the .decision aspect; SetApplicantProfile validates it is alive and writes the .profile / .underwritingParties / .applicationSignals aspects in one batch; BackfillLeaseTerms validates it is alive and upserts the .terms aspect's requestedRent from the application's own unit's listed rent; ReassignLeaseUnit validates it is alive and re-points its appliesToUnit link at newUnitKey; EndTenancy validates it is alive and rewrites its .tenancy aspect with endedAt = leaseEnd (the .tenancy is a required declared read too); RecordApplicationLoss validates it is alive and writes .decision {value: lost, decidedAt} once its unit has leased to another applicant (the .decision is a declared optionalReads). The caller lists it in ContextHint.Reads.",
			"newUnitKey":            "Full vtx.unit.<NanoID> key of the unit ReassignLeaseUnit re-points the application at (required, validated alive). The operator names the unit directly — the application's OWN appliesToUnit / applicationFor links, never payload fields, are what the op reads to find the CURRENT unit and the applicant.",
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
			"decision":              "The landlord's leasing decision (DecideLeaseApplication; required): approved or declined. Written to the .decision aspect {value, decidedAt}, whose recorded value is approved|declined|lost — lost is recorded by RecordApplicationLoss when the unit went to another applicant, never submitted by a landlord. A recorded decision is TERMINAL — the same value re-submits idempotently, a different value is rejected (DecisionFinal, a lost application included); approve is rejected (NotReadyToApprove) unless the application is signed. The convergence lens reads it: approved opens missing_listingLeased (the unit leases); declined folds into the lens's declined disposition (a terminal rejection); lost closes the applicant gaps and projects lost_to_rival.",
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
				ExpectedOutcome: "Validates the application is alive, then re-verifies (independent of the dispatched grant) that the applied-to " +
					"unit is still live and either not leased or leased on THIS application's own approved decision. Writes the .signature aspect " +
					"{signedAt: <op.submittedAt, canonical UTC>} on the application (root data stays {} — D5). Also snapshots the applicant's " +
					"identity's own .name onto a CREATE-ONLY .tenantName aspect (SENSITIVE, custodied on the executedLeaseRecord retention " +
					"class) when a live, non-blank name is available; writes no .tenantName when the applicant has no live applicationFor " +
					"link, no live .name, a blank name, or a crypto-shredded identity (ShredIdentityKey) — signing still succeeds, and the " +
					"executed-lease document degrades to the bare applicant key. Emits leaseapp.leaseSigned{leaseAppKey}. " +
					"Returns primaryKey. Rejects a non-existent application, one already signed (the .signature CreateOnly guard), or one whose unit " +
					"is now tombstoned or already leased to a different applicant (UnitNoLongerAvailable).",
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
					"(UnitMismatch), an applicant that is not the application's applicant (ApplicantMismatch), or an approved " +
					"application (AlreadyApproved — an executed lease is never withdrawn; its .decision is declared as an " +
					"optionalRead, absent on the undecided application).",
			},
			{
				Name:    "DecideLeaseApplication — landlord approves an application (first approve stamps .tenancy)",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>", "decision": "approved", "unit": "vtx.unit.<unitNanoID>"},
				ExpectedOutcome: "Validates the application is alive and the decision is approved|declined. Writes the .decision aspect " +
					"{value: <decision>, decidedAt: <op.submittedAt, canonical UTC>} on the application (root stays {} — D5). " +
					"A recorded decision is terminal: the same value re-submits idempotently, a different value is rejected " +
					"(DecisionFinal); approve is rejected (NotReadyToApprove) unless the application is signed. approved opens the " +
					"listing-leased convergence (the unit leases). Because no .tenancy aspect exists yet, this FIRST approve also " +
					"verifies unit against the appliesToUnit link, reads the application's own .terms and the unit's .listing " +
					"{availableFrom, leaseTermMonths, rentAmount}, and CREATE-ONLY-writes .tenancy {leaseStart: terms.moveInDate " +
					"else availableFrom, leaseEnd: leaseStart + (terms.leaseTermMonths else leaseTermMonths), " +
					"renewalOpensAt: leaseEnd - renewalWindow, rentAmount: terms.requestedRent else listing.rentAmount (omitted " +
					"when neither exists)} — the fact the leaseExpiry/renewalComplete targets read. Emits " +
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
			{
				Name:    "BackfillLeaseTerms — repair an approved lease missing requestedRent",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Resolves the application's own appliesToUnit link and upserts .terms with requestedRent set to " +
					"the unit's own listed rent (unit.listing.rentAmount), preserving any moveInDate/leaseTermMonths already " +
					"present. No-ops if requestedRent is already set. Operator-only. Rejects a non-existent application " +
					"(UnknownLeaseApplication), one whose unit is no longer live (UnitNoLongerAvailable), or a unit carrying " +
					"no listed rent to backfill from (NoRentSource).",
			},
			{
				Name:    "ReassignLeaseUnit — repair a lease whose unit was tombstoned",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>", "newUnitKey": "vtx.unit.<liveUnitNanoID>"},
				ExpectedOutcome: "Resolves the application's CURRENT appliesToUnit link and its applicationFor applicant itself " +
					"(never payload fields). Tombstones the old link CAS-guarded on its own revision, and creates-or-revives " +
					"lnk.leaseapp.<id>.appliesToUnit.unit.<newUnitId>. Re-keys the per-(applicant, unit) duplicate-application " +
					"guard for the NEW pair (alive → DuplicateApplication; absent → create; tombstoned → revive), and FREES " +
					"(tombstones) the VACATED (applicant, oldUnit) guard. No-ops (zero mutations) if the application already applies to newUnitKey. " +
					"Emits leaseapp.unitReassigned{leaseAppKey, oldUnitKey, newUnitKey}. Every mutation is relational (no write " +
					"ever touches the leaseapp vertex or one of its aspects), so primaryKey is the NEW appliesToUnit link " +
					"itself, not leaseAppKey (the reply-constraint requires primaryKey within the committed write footprint — " +
					"the AssignUnitOwner link-as-primaryKey shape, loftspace-domain/ownership.go); a no-op returns no " +
					"primaryKey at all. Operator-only. Rejects a non-existent " +
					"application (UnknownLeaseApplication) or unit (UnknownUnit), one carrying no live appliesToUnit or " +
					"applicationFor link (InvalidState), or a newUnitKey the applicant already has a live application on " +
					"(DuplicateApplication).",
			},
			{
				Name:    "EndTenancy — record that a lease term ended",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Reads the application and its .tenancy (both declared in ContextHint.Reads — required) and " +
					"its .notice (declared in ContextHint.OptionalReads; the term's end is the recorded move-out when a " +
					"notice precedes leaseEnd, else leaseEnd — an undeclared .notice falls back to leaseEnd). " +
					"If .tenancy already carries endedAt, no-ops with zero mutations and no event (idempotent under " +
					"at-least-once dispatch; no primaryKey). If op.submittedAt is before that end, rejects " +
					"NotYetEnded naming the end's UTC calendar date. Otherwise rewrites .tenancy with every existing field " +
					"preserved (leaseStart, renewalOpensAt, a renewed term's termStart / rentAmount) plus endedAt = that end, " +
					"pinned to the revision the read hydrated (a concurrent SignRenewal rewrite RevisionConflicts). Emits " +
					"leaseapp.tenancyEnded{leaseAppKey, leaseEnd, endedAt}. Returns primaryKey. Operator-only (Weaver's service " +
					"actor via the tenancyEnd target, or by hand). Rejects a non-existent application (UnknownLeaseApplication) " +
					"or one with no .tenancy / no leaseEnd (NoTenancy) — including a submission that failed to declare the " +
					".tenancy read, which is refused rather than read on demand.",
			},
			{
				Name:    "GiveNotice — record that a tenancy ends early",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>", "moveOutDate": "2027-03-31"},
				ExpectedOutcome: "Reads the application, its .tenancy and its .signature (all declared in ContextHint.Reads — " +
					"required) and its .notice (declared in ContextHint.OptionalReads). On the platform-validated self path " +
					"the caller is the tenant (the applicationFor link keyed on the acting identity is live — declared in " +
					"OptionalReads) or else the landlord (require_manages on the application's own unit; AuthDenied " +
					"otherwise); an operator is admitted on the standing grant. Rejects NoTenancy (no .tenancy), " +
					"LeaseNotSigned (no .signature), TenancyEnded (endedAt set), NoticeAlreadyGiven (.notice live), " +
					"MoveOutBeforeToday / MoveOutBeforeStart / MoveOutAfterEnd (the move-out is not from today on and " +
					"strictly inside [leaseStart, leaseEnd)). Otherwise writes .notice {moveOutAt: 2027-03-31T00:00:00Z, " +
					"givenAt: <op.submittedAt, canonical UTC>, givenBy: tenant|landlord|operator} create-only. Emits " +
					"leaseapp.noticeGiven{leaseAppKey, moveOutAt, givenBy}. Returns primaryKey. Rejects a non-existent " +
					"application (UnknownLeaseApplication) or a malformed moveOutDate (InvalidArgument).",
			},
			{
				Name:    "RecordApplicationLoss — record that an application lost its unit to another applicant",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Reads the application (declared in ContextHint.Reads — required) and its .decision (declared in " +
					"ContextHint.OptionalReads). If .decision already carries a value — lost, approved or declined — no-ops with " +
					"zero mutations and no event (idempotent under at-least-once dispatch; a landlord decision racing the dispatch " +
					"is nothing to record; no primaryKey). Resolves the unit from the application's own appliesToUnit link and " +
					"reads its .listing; rejects NoUnit when the link has no live endpoint and UnitNotLeased when the listing is " +
					"absent, deleted, or its status is not leased. Otherwise writes .decision {value: lost, decidedAt: " +
					"<op.submittedAt, canonical UTC>} (create-only under the declared absence; no reason, no " +
					".decidedProfileSnapshot). Emits leaseapp.applicationLost{leaseAppKey, unitKey}. Returns primaryKey. " +
					"Operator-only (Weaver's service actor via leaseApplicationComplete's missing_lossRecorded gap, or by hand via the CLI " +
					"under the primordial admin, as EndTenancy). " +
					"Rejects a non-existent application (UnknownLeaseApplication).",
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

// decidedProfileSnapshotAspectDDL declares the .decidedProfileSnapshot aspect
// (class decidedProfileSnapshot) — the fair-housing preservation record
// DecideLeaseApplication CREATE-ONLY-stamps on the FIRST .decision write of
// either value (the leaseapp vertexType DDL owns the script). Declaration-only.
//
// SetApplicantProfile stays a freely re-submittable, unconditioned upsert
// (design §5: profile-terminal-state guard shapes were tried and empirically
// falsified — every .decision-keyed guard breaks the renewal chain, which
// re-reads .applicationSignals off the ORIGINAL leaseapp years after the
// decision). Without a snapshot, the record of what the landlord actually saw
// when THEY decided is lost the moment a later submission overwrites .profile
// / .underwritingParties / .applicationSignals — the exact fact a fair-housing
// review needs to reconstruct. This aspect exists solely to preserve it.
//
// SENSITIVE, SAME underwritingRecord retention-class custody as .profile /
// .underwritingParties (RetentionClasses) — its data map nests copies of both
// sensitive source aspects' raw content, so it can be no less protected than
// either. CREATE-ONLY: the op stamps it once, on the first .decision write;
// a re-decision (idempotent re-submit, or a later approve after the
// terminal-decision guard has already rejected a value change) never
// re-derives or overwrites it, so the snapshot always reflects the ORIGINAL
// decision's inputs even as .profile is re-submitted afterward.
//
// No shipped lens reads this aspect: retention-class-key-custody-design.md
// §9.1's Secure Lens (over the underwritingRecord class) does not exist yet
// (design §5 residual) — the preserved record is captured, not yet
// inspectable. The reader fire (an authorized fair-housing review surface) is
// its own item, filed when that consumer is real.
func decidedProfileSnapshotAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "decidedProfileSnapshot",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"DecideLeaseApplication"},
		Sensitive:         true,
		Custody:           pkgmgr.CustodySpec{Kind: pkgmgr.CustodyKindRetentionClass, RetentionClass: underwritingRecordRetentionClass},
		Description: "Fair-housing decided-profile-snapshot aspect (lease-signing). Stored as " +
			"vtx.leaseapp.<NanoID>.decidedProfileSnapshot (class decidedProfileSnapshot) = {profile, " +
			"underwritingParties, applicationSignals} — a nested, point-in-time COPY of the leaseapp's own " +
			".profile / .underwritingParties / .applicationSignals data maps, taken by DecideLeaseApplication " +
			"on the FIRST .decision write of either value (approve OR decline). Each nested key holds the " +
			"corresponding source aspect's data map verbatim, or {} when that aspect was never submitted " +
			"(a landlord may decide before SetApplicantProfile is ever called; the decision still succeeds " +
			"and stamps an empty/partial snapshot rather than failing). SENSITIVE, custodied on the SAME " +
			"underwritingRecord retention-class holder as .profile (RetentionClasses) — never the applicant's " +
			"identity, so the record survives the applicant's erasure exactly as .profile does. CREATE-ONLY: " +
			"stamped exactly once per application, on the first decision; SetApplicantProfile remains a freely " +
			"re-submittable upsert on .profile / .underwritingParties / .applicationSignals themselves, so " +
			"without this snapshot the qualification data a landlord's decision was actually based on would be " +
			"unrecoverable the moment a later submission overwrote it. No shipped lens reads this aspect's " +
			"content — no Secure Lens exists yet over the underwritingRecord retention class (residual, " +
			"design §5): the record is preserved, not yet inspectable. Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"profile":{"type":"object","description":"Verbatim copy of the leaseapp's .profile data map at decision time, or {} if never submitted."},` +
			`"underwritingParties":{"type":"object","description":"Verbatim copy of the leaseapp's .underwritingParties data map at decision time, or {} if never submitted."},` +
			`"applicationSignals":{"type":"object","description":"Verbatim copy of the leaseapp's .applicationSignals data map at decision time, or {} if never submitted."}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"profile":             "Point-in-time copy of the applicant's raw-financials .profile aspect (annualIncome, employmentStatus, employerName, guarantorRelationship, guarantorAnnualIncome) as it stood at the FIRST decision. {} if the applicant never submitted a profile before the landlord decided. Never projected by any lens.",
			"underwritingParties": "Point-in-time copy of the .underwritingParties aspect (references, guarantorName, coApplicantName, coApplicantContact) as it stood at the FIRST decision. {} if never submitted. Never projected by any lens.",
			"applicationSignals":  "Point-in-time copy of the derived .applicationSignals aspect (incomeToRentMet, employmentVerified, referenceCount, hasCoApplicant, hasGuarantor, guarantorIncomeToRentMet, submittedAt) as it stood at the FIRST decision. {} if never submitted. Never projected by any lens.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "decided-profile-snapshot aspect (fair-housing record)",
				Payload: map[string]any{
					"profile":             map[string]any{"annualIncome": 96000, "employmentStatus": "employed"},
					"underwritingParties": map[string]any{},
					"applicationSignals":  map[string]any{"employmentVerified": true, "referenceCount": 2},
				},
				ExpectedOutcome: "Stored ENCRYPTED as vtx.leaseapp.<NanoID>.decidedProfileSnapshot, written CREATE-ONLY by DecideLeaseApplication on the first decision, DEK custodied on the SAME underwritingRecord retention-class holder as .profile. Never re-written by a later decision or SetApplicantProfile re-submission. Never projected by any lens.",
			},
		},
	}
}

// tenantNameAspectDDL declares the .tenantName aspect (class tenantName) — the
// applicant's display name as it stood at signing, snapshotted by SignLease
// from the applicant identity's own .name (the leaseapp vertexType DDL owns
// the script). Declaration-only.
//
// SENSITIVE, custodied on the executedLeaseRecord retention class
// (RetentionClasses) — a SEPARATE class from underwritingRecord: a signed
// lease's party name is a different obligation from the financial-
// qualification record underwritingRecord's own Description scopes to
// (.profile / .underwritingParties / .decidedProfileSnapshot), and folding an
// unrelated obligation into that class would blur the population-separation
// discipline that class's own Description argues for. After ShredIdentityKey
// on the applicant, the executed lease still names its tenant, pseudonymized
// against the applicant's other directly-identifying aspects (Contract #3
// §3.10: "a contract record keeps its parties' names for as long as the
// contract must be kept").
func tenantNameAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "tenantName",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"SignLease"},
		Sensitive:         true,
		Custody:           pkgmgr.CustodySpec{Kind: pkgmgr.CustodyKindRetentionClass, RetentionClass: executedLeaseRecordRetentionClass},
		Description: "Executed-lease tenant-name aspect (lease-signing). Stored as vtx.leaseapp.<NanoID>.tenantName " +
			"(class tenantName) = {value} — the applicant's display name as it stood at signing, snapshotted by " +
			"SignLease from the applicant identity's own .name (walked via the leaseapp's own applicationFor link) " +
			"the moment the application is signed. SENSITIVE, custodied on the executedLeaseRecord retention-class " +
			"holder (RetentionClasses), NOT the applicant's identity: the executed lease is a legal document naming " +
			"its tenant, so the name outlives the applicant's own erasure request — after ShredIdentityKey the lease " +
			"still names its tenant, pseudonymized. CREATE-ONLY, written once by SignLease: no op ever re-writes it, " +
			"so a later change to the applicant's own .name does not retroactively alter the name an already-executed " +
			"lease carries. Absent when the applicant identity had no live, non-blank .name at signing, when the " +
			"application carries no live applicant link, or when the applicant identity was crypto-shredded " +
			"(ShredIdentityKey) before signing — the executed-lease document then degrades to the bare applicant " +
			"key. Read via the shipped declared-egress path (subject.tenantName.data.value, the leaseDocument " +
			"pattern's Params) at " +
			"CreateLeaseDocInstance — never a plaintext read. Declaration-only: no op handler.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"value": "The applicant's display name, as it stood at signing. Never projected by any lens; reachable only through the declared sensitive-egress path.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "executed-lease tenant-name aspect",
				Payload:         map[string]any{"value": "Alice Smith"},
				ExpectedOutcome: "Stored ENCRYPTED as vtx.leaseapp.<NanoID>.tenantName, written CREATE-ONLY by SignLease, DEK custodied on the executedLeaseRecord retention-class holder. Never projected by any lens; egressed only via the declared subject.tenantName.data.value path.",
			},
		},
	}
}

// tenancyNoticeAspectDDL declares the leaseapp's .notice aspect — the recorded
// fact that a tenancy ends early. Written ONCE by GiveNotice (create-only; a
// change of date is not modelled), never rewritten: it is its own aspect
// rather than a .tenancy field because .tenancy already has two whole-aspect
// writers (DecideLeaseApplication, SignRenewal) and a third would put the
// notice under SignRenewal's rewrite. Three readers key on it: the tenancyEnd
// lens (the term's effective end, termEnd = min(moveOutAt, leaseEnd), arms
// the timer and overrides the open-renewal hold), EndTenancy (endedAt =
// termEnd), and SignRenewal (refuses NoticeGiven); leaseExpiry opens no
// renewal cycle on a lease under notice, and semantic-contracts'
// leaseRentSettlement shortens the rent clause's term to the move-out.
// Not sensitive: a move-out date and who recorded it are lease facts, not
// personal data.
func tenancyNoticeAspectDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "tenancyNotice",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"GiveNotice"},
		Description: "Tenancy-notice aspect (lease-signing). Stored as vtx.leaseapp.<NanoID>.notice (class tenancyNotice) " +
			"= {moveOutAt, givenAt, givenBy}: the recorded fact that the tenancy ends EARLY, on moveOutAt (a date-only fact: " +
			"midnight UTC of the move-out's UTC calendar day, canonical-UTC RFC3339), given at givenAt (the op's submittedAt) by givenBy ∈ tenant | landlord " +
			"| operator (which probe admitted the caller: the applicationFor link, the manages link, or the standing " +
			"operator grant). Written CREATE-ONLY by GiveNotice — once, never rewritten; a recorded notice is not changed. " +
			"Read by the tenancyEnd lens (the term's effective end is the earlier of moveOutAt and leaseEnd; a notice " +
			"overrides the open-renewal hold), by EndTenancy (endedAt = that end), by SignRenewal (NoticeGiven — a lease " +
			"under notice cannot be renewed), by leaseExpiry (no renewal cycle opens), by the renewalsRead / " +
			"leaseApplicationsRead / landlordLeaseApplicationsRead read models (notice_move_out_at / notice_given_at / " +
			"notice_given_by), and by semantic-contracts' leaseRentSettlement (the rent clause's term is shortened to the " +
			"move-out). Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"moveOutAt":{"type":"string"},"givenAt":{"type":"string"},"givenBy":{"type":"string","enum":["tenant","landlord","operator"]}},` +
			`"required":["moveOutAt","givenAt","givenBy"]}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"moveOutAt": "The move-out date as midnight UTC of its calendar day, canonical-UTC RFC3339 (a date-only fact: an instant supplied with a clock part is recorded as its UTC calendar day). Strictly inside the term (after leaseStart, before leaseEnd) and not before the UTC calendar day the notice was given. The term's effective end from here on.",
			"givenAt":   "When the notice was recorded — the op's submittedAt, canonical UTC.",
			"givenBy":   "Who recorded it: tenant (the application's own applicant, via the applicationFor link), landlord (a manager of the application's unit, via the manages link), or operator (the standing grant).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "tenancy-notice aspect",
				Payload:         map[string]any{"moveOutAt": "2027-03-31T00:00:00Z", "givenAt": "2027-02-14T09:30:00Z", "givenBy": "tenant"},
				ExpectedOutcome: "Stored as vtx.leaseapp.<NanoID>.notice, written CREATE-ONLY by GiveNotice. The tenancyEnd lens now arms its timer on 2027-03-31T00:00:00Z (if that precedes leaseEnd) and EndTenancy records endedAt there; SignRenewal refuses NoticeGiven; leaseExpiry opens no cycle.",
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
		//
		// TombstoneSupersededLeaseServiceInstance's three tombstones carry NO document
		// (the bare op:tombstone form), and this list is what admits them: an update or
		// a tombstone is governed by the class STORED at its key, so the instance root
		// tombstone resolves service.<family>.instance, walks the instanceOf chain to
		// this meta-vertex — against the COMMITTED graph, so the instanceOf tombstone
		// riding the same batch cannot un-type it — and must name this op here. The
		// instanceOf / providedTo link tombstones carry the link relation as their
		// stored class, and no linkType DDL registers those names: Contract #1
		// §1.5/§1.6's permissive default. Its fourth mutation — the successor's
		// supersedes link to the retired predecessor — is a link CREATE, and the gate
		// resolves a mutation's governing DDL by exact class FIRST: the class of a
		// link create is its relation, `supersedes`, which no linkType DDL registers,
		// so resolution falls through to the instanceOf walk, which needs a vertex
		// root a link mutation does not have — hence the same permissive default, and
		// no entry in any DDL's list. A linkType DDL declaring `supersedes` would put
		// the link under that DDL's own list instead.
		PermittedCommands: []string{"CreateLeaseServiceInstance", "TombstoneSupersededLeaseServiceInstance"},
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
			"selects its adapter and posts the replyOp. " +
			"TombstoneSupersededLeaseServiceInstance{instanceKey, supersededBy, subjectKey} (bgcheck-supersession-" +
			"convergence-rule-design.md) retires a service instance once a LATER instance of the SAME family has " +
			"completed for the SAME subject, so a readiness aggregate that fans out over every instance of an applicant " +
			"(the leaseApplicationComplete lens) stops reading a retired check — a superseded check's instance is " +
			"retired so readers aggregate only over live checks. All three payload keys are FULL keys (not bare " +
			"handles). SUBMITTERS: Weaver's service actor is the durable one — the supersededBackgroundChecks " +
			"convergence target dispatches this op as a directOp off a lens row that has already proven the pair — and " +
			"an operator or trusted tool may run it by hand under the same operator/Scope:\"any\" grant. Refuses " +
			"op.actor == primordialActor[\"loom\"] outright (AuthDenied): Loom MINTS instances " +
			"(CreateLeaseServiceInstance is restricted to exactly Loom's relay actor) and never retires them, so that " +
			"actor here is a confused or compromised caller. Weaver's admission widens who may ASK, never what can be " +
			"proven: every trust-bearing key is derived from the payload and from ddl[...].metaKey and re-proven " +
			"against hydrated state, never taken from the caller. Guards, fail-closed, in order: instanceKey != " +
			"supersededBy; both parse as vtx.service.<handle>; subjectKey parses as vtx.identity.<NanoID>; " +
			"instanceKey's root alive; instanceKey's instanceOf link resolves to THIS DDL's own meta-vertex (Contract " +
			"#1 §1.5 type authority, derived the same way CreateLeaseServiceInstance derives it) — the OWNERSHIP " +
			"check: a foreign instance minted by another mechanism can carry the identical readable shape (envelope " +
			"class / .outcome / providedTo) while its real instanceOf link targets a different type authority, so this " +
			"runs before anything else about instanceKey is trusted; supersededBy's root alive; both roots carry the " +
			"SAME non-empty envelope class (a successor supersedes only its own family); both carry a .outcome aspect " +
			"with status=completed; supersededBy's outcome.completedAt is LATER than instanceKey's, or EQUAL with the " +
			"greater instance key (compared as RFC3339 UTC strings; completedAt is whole-second, so an equal stamp is " +
			"reachable and the key breaks the tie — the textually identical rule the supersededBackgroundChecks lens " +
			"projects a row by), each stamp first checked for that whole-second UTC form (20 characters, Z-suffixed) — " +
			"a form precondition string ordering depends on, not part of the ordering rule itself; both instances' " +
			"providedTo link to subjectKey is alive; and supersededBy's OWN instanceOf link resolves to this DDL's " +
			"meta-vertex too (NotOwned otherwise) — the EIGHTH proof, held on EVERY submission path, so a same-class " +
			"instance minted by another type authority can never supersede one of ours and can never source the " +
			"supersedes link below. That proof is a BOUNDED ENUMERATION rather than a read (Contract #2 §2.5 class " +
			"(e)): kv.Links over supersededBy's outbound instanceOf relation — degree 1 by construction, one link per " +
			"instance minted by CreateLeaseServiceInstance — accepted only on a LIVE link whose target is this DDL's " +
			"metaKey. Neither route a declared read takes can carry it: derive_reads cannot reach ddl[...].metaKey, and " +
			"a lens cannot project a relationship variable through an aggregate. So every dispatcher declares the WALK " +
			"instead: Weaver's supersededBackgroundChecks target as an Enumerations entry on the gap (hub " +
			"row.supersededBy, relation instanceOf, direction out), an operator as contextHint.enumerations. " +
			"READS, seven: SIX are DERIVED server-side by this DDL's own derive_reads(op) (Contract #2 §2.5 class (g) " +
			"— pure arithmetic on the payload: instanceKey; supersededBy; instanceKey+\".outcome\"; " +
			"supersededBy+\".outcome\"; lnk.service.<instanceKey's handle>.providedTo.identity.<subjectKey's id>; " +
			"lnk.service.<supersededBy's handle>.providedTo.identity.<subjectKey's id>), so no dispatcher restates " +
			"them. The derivation runs only on a payload already right in SHAPE and TYPE (3 segments, vtx, service / " +
			"service / identity); anything else — a missing field, a non-key string, a 3-segment non-vtx key, a key of " +
			"the wrong type — derives nothing, and execute() raises the real InvalidArgument. It does not check the id " +
			"segment: an id that is not a valid NanoID still derives, and parts_of rejects it in execute(). " +
			"The SEVENTH, lnk.service.<instanceKey's handle>.instanceOf.meta.<this DDL's metaKey id> (the ownership " +
			"link), is the ONE key a dispatcher declares in contextHint.reads, because the derivation pre-pass cannot " +
			"reach ddl[...].metaKey: Weaver declares it as row.instanceOfLink, an operator declares the key above. " +
			"That declaration is REQUIRED of every submitter: the op reads the ownership link from the step-4 snapshot " +
			"(never a lazy live kv.Read, which would make the trust-bearing key an undeclared class-(b) read outside " +
			"the OCC snapshot the mutations are conditioned on), so an undeclared ownership link is refused " +
			"InvalidArgument. A dispatcher that ALSO declares the six derived keys is valid (weakest-wins merge). " +
			"Every one of the seven is " +
			"REQUIRED (never optionalReads): in production a foreign (unowned) instanceKey, an unknown instanceKey or " +
			"supersededBy, or a genuinely wrong subjectKey each name a key that never existed at all, so step 4 records " +
			"it required-absent and the script's FIRST TOUCH of it faults HydrationMiss (Contract #2 §2.5) instead of " +
			"branching on a None — the script's own NotOwned: / UnknownInstance: / SubjectMismatch: messages fire only " +
			"for the narrower present-but-tombstoned residual (e.g. a repeat submission racing a concurrent purge, the " +
			"fail-closed default handling the rest). FOUR mutations in one batch: tombstones (bare op:tombstone, no " +
			"document) the instance root + its instanceOf link + its providedTo link — never the successor — and " +
			"creates lnk.service.<supersededBy's handle>.supersedes.service.<instanceKey's handle> (source = the live " +
			"successor, target = the predecessor retired in the same batch, Contract #1 §1.1's later-arriving-vertex " +
			"rule; the sentence reads \"new supersedes old\"), so the retirement is walkable by key at rest even " +
			"though no live lens chains through a tombstoned target (a live walk decodes a tombstoned neighbour as " +
			"absent). step6_validate.go applies no same-batch endpoint-liveness rule to a link create — its one " +
			"endpoint rule covers required-ABSENT endpoints, and both endpoints here are hydrated. A predecessor " +
			"retired in its own turn keeps its outbound supersedes link live, so a chain of retirements stays walkable " +
			"link by link. Emits " +
			"lease.serviceInstanceSuperseded{instanceKey, supersededBy, subjectKey}. Returns primaryKey (instanceKey, " +
			"the tombstoned instance).",
		Script: leaseServiceInstanceDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"instanceKey":{"type":"string","description":"CreateLeaseServiceInstance: the BARE instance handle Loom minted (no dots / key segments / wildcards); the op prepends vtx.service. → vtx.service.<handle>. TombstoneSupersededLeaseServiceInstance: the FULL vtx.service.<handle> key of the (older, superseded) instance to retire. Required."},` +
			`"supersededBy":{"type":"string","description":"TombstoneSupersededLeaseServiceInstance only: the FULL vtx.service.<handle> key of the LATER completed instance that supersedes instanceKey (same envelope class, same subject, and an outcome.completedAt later than instanceKey's or equal with the greater key). Sources the supersedes link the op mints. Required."},` +
			`"subjectKey":{"type":"string","description":"CreateLeaseServiceInstance: vtx.identity.<NanoID> of the applicant the claim is for (the pattern subject); the providedTo link points at it, required, validated alive. TombstoneSupersededLeaseServiceInstance: vtx.identity.<NanoID> both instanceKey and supersededBy must carry a live providedTo link to (the shared subject). Required."},` +
			`"adapter":{"type":"string","description":"CreateLeaseServiceInstance only: the external adapter name (e.g. backgroundCheck, stripe), carried into the external.<adapter> event. Required."},` +
			`"replyOp":{"type":"string","description":"CreateLeaseServiceInstance only: the result-op the bridge posts back (RecordLeaseServiceOutcome), carried into the external event. Required."},` +
			`"params":{"type":"object","description":"CreateLeaseServiceInstance only: opaque pass-through adapter params from the Loom step; params.family (backgroundCheck|payment) sets the instance's envelope class service.<family>.instance."}},` +
			`"required":["instanceKey","subjectKey"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"CreateLeaseServiceInstance: vtx.service.<handle> of the minted claim vertex. TombstoneSupersededLeaseServiceInstance: vtx.service.<handle> of the tombstoned (superseded) instance. Either way, the operation's principal key."}}}`,
		FieldDescription: map[string]string{
			"instanceKey":  "CreateLeaseServiceInstance: the bare instance handle Loom minted for this externalTask (type-free, no dots / key segments / wildcards); the op prepends vtx.service. to it → vtx.service.<handle>, echoed back as the reply op's externalRef and the bridge's adapter dedup key. TombstoneSupersededLeaseServiceInstance: the FULL vtx.service.<handle> key of the older, superseded instance being retired — validated alive, owned by this DDL's type authority, same envelope class as supersededBy, providedTo subjectKey; its root, instanceOf link and providedTo link are tombstoned and it becomes the target of the supersedes link. Required either way.",
			"supersededBy": "TombstoneSupersededLeaseServiceInstance only: full vtx.service.<handle> key of the later completed instance that supersedes instanceKey — validated alive, same envelope class, providedTo the same subjectKey, its outcome.completedAt later than instanceKey's or equal with the greater key (whole-second stamps make an equal completedAt reachable), and OWNED by this DDL's type authority, proven by a bounded instanceOf enumeration off this key (Contract #2 §2.5 class (e); NotOwned otherwise) — every dispatcher declares that walk, Weaver as the gap's Enumerations entry on row.supersededBy, an operator in contextHint.enumerations. Never itself tombstoned by this op; it SOURCES the supersedes link the op mints to the retired predecessor. Required.",
			"subjectKey":   "CreateLeaseServiceInstance: full vtx.identity.<NanoID> key of the applicant the externalTask is for (the Loom pattern subject); validated alive, the providedTo link target (the convergence link the lens reads across). TombstoneSupersededLeaseServiceInstance: full vtx.identity.<NanoID> key both instanceKey and supersededBy must carry a live providedTo link to — the shared subject that makes one a legitimate successor of the other. Required either way.",
			"adapter":      "CreateLeaseServiceInstance only: the registered bridge adapter name (e.g. backgroundCheck, stripe). Carried into the external.<adapter> event class + body so the bridge selects its adapter. Required.",
			"replyOp":      "CreateLeaseServiceInstance only: the result-op type the bridge posts back (RecordLeaseServiceOutcome). Carried into the external event body so the bridge knows which op to submit on success. Required.",
			"params":       "CreateLeaseServiceInstance only: opaque adapter params passed through from the Loom step. params.family (backgroundCheck|payment) discriminates the claim vertex's envelope class (service.<family>.instance).",
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
			{
				Name: "TombstoneSupersededLeaseServiceInstance — retire a background check superseded by a later completed one",
				Payload: map[string]any{
					"instanceKey":  "vtx.service.<olderHandle>",
					"supersededBy": "vtx.service.<newerHandle>",
					"subjectKey":   "vtx.identity.<applicantNanoID>",
				},
				ExpectedOutcome: "The submitter declares ONE key in contextHint.reads — instanceKey's ownership " +
					"instanceOf link (Weaver's supersededBackgroundChecks target sends it as row.instanceOfLink) — plus " +
					"ONE walk in contextHint.enumerations (supersededBy's outbound instanceOf relation), and " +
					"this DDL's own derive_reads(op) supplies the other six reads (both roots, both .outcome aspects, both " +
					"providedTo links to subjectKey). A dispatcher that declares all seven is equally valid " +
					"(weakest-wins merge), but the ownership link is never optional: the op reads it from the step-4 " +
					"snapshot, so a submission that declares nothing at all is refused InvalidArgument rather than " +
					"served a lazy live read. All seven are REQUIRED, so in production a foreign/unowned instanceKey, " +
					"an unknown instanceKey or supersededBy, or a wrong subjectKey each name a key that was never " +
					"created, and the script's first touch of it faults HydrationMiss; a malformed payload (a missing " +
					"field, a non-key string, a 3-segment non-vtx key, a key of the wrong type) derives nothing and " +
					"rejects InvalidArgument from execute() instead. Once all seven hydrate, the " +
					"script validates instanceKey != supersededBy; instanceKey's instanceOf link actually resolves to " +
					"this DDL's own meta-vertex (NotOwned otherwise — reachable only for a present-but-tombstoned " +
					"ownership link); both roots carry the SAME non-empty envelope class (e.g. both " +
					"service.backgroundCheck.instance); both outcomes status=completed; supersededBy's " +
					"outcome.completedAt later than instanceKey's, or equal with the greater instance key (RFC3339 UTC " +
					"string compare, NotSuperseded otherwise); both instances providedTo subjectKey (SubjectMismatch " +
					"otherwise — same present-but-tombstoned-only reachability), each completedAt carrying the " +
					"whole-second UTC form string ordering depends on (a stamp of any other shape, or of a " +
					"non-string type, is NotSuperseded). supersededBy's own ownership is proven too, on every " +
					"path: a bounded kv.Links walk of its outbound instanceOf relation must find a live link to " +
					"this DDL's metaKey (NotOwned otherwise), which the submitter declares as an enumeration — " +
					"contextHint.enumerations {hub: supersededBy, relation: instanceOf, direction: out}, which " +
					"Weaver's target carries as Enumerations on the gap. Commits four " +
					"mutations in one batch: bare op:tombstone (no document) of instanceKey's root + its instanceOf " +
					"link + its providedTo link, and a create of " +
					"lnk.service.<newerHandle>.supersedes.service.<olderHandle> (source = supersededBy, target = " +
					"instanceKey) — supersededBy's own root and links are left untouched, and a predecessor retired " +
					"later keeps this link live, so a chain stays walkable. Emits " +
					"lease.serviceInstanceSuperseded{instanceKey, supersededBy, subjectKey}. Returns primaryKey " +
					"(instanceKey). Rejects AuthDenied if the actor is Loom's, HydrationMiss (at dispatch) if a " +
					"declared or derived key genuinely never existed, or a ScriptError (InvalidArgument / " +
					"UnknownInstance / NotOwned / WrongClass / NotSuperseded / SubjectMismatch) for a guard the script " +
					"itself evaluates.",
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
