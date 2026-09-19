// Package leasesigning is the Loftspace lease-application convergence vertical —
// the Epic 14 centerpiece that wires the prior bricks into one convergent
// package, extended with the lease-renewal chain (the first goal-authored
// Weaver target).
//
// It declares:
//
//   - The `leaseapp` vertex type (DDL `leaseapp`) + two ops: CreateLeaseApplication
//     (mints vtx.leaseapp.<id>, root data {} per D5, with the applicationFor link
//     to the applicant identity) and SignLease (writes the .signature aspect, the
//     fact that closes the missing_signature gap).
//
//   - The two externalTask wrapper DDLs the Loom externalTask step binds
//     (Contract #10 §10.5/§10.6):
//
//   - `leaseServiceInstance` / CreateLeaseServiceInstance — the instanceOp.
//     Mints the claim vertex vtx.service.<handle> (the same shape 14.1's
//     service instance uses, reusing its .outcome aspect shape) + a .family
//     discriminator aspect + the providedTo link to the applicant, and emits
//     the external.<adapter> event off its own transactional outbox.
//
//   - `leaseServiceReply` / RecordLeaseServiceOutcome — the replyOp the bridge
//     posts back. Reconstructs vtx.service.<handle> from the bare externalRef,
//     derives status=completed + completedAt=op.submittedAt, writes the
//     .outcome aspect, and emits orchestration.externalTaskCompleted{externalRef}
//     — the uniform completion signal Loom correlates on.
//
//   - `leaseServiceDispatch` / RecordServiceDispatch — the dispatchOp the bridge
//     posts when its adapter returns Pending (the external call was submitted but
//     has not resolved yet). Reconstructs vtx.service.<handle> from the bare
//     externalRef and writes a create-only .dispatch marker {vendorRef,
//     submittedAt}; it emits NO completion signal (the task is not done — the
//     token stays parked). The .dispatch and .outcome aspects are separate so
//     pending does not collide with the once-only terminal .outcome. It is
//     family-agnostic: the docGen triad reuses it (and its marker gate)
//     unchanged as its own pending path.
//
//   - The docGen triad (`leaseDocInstance` / CreateLeaseDocInstance,
//     `leaseDocReply` / RecordLeaseDocOutcome, the `leaseDocOutcome` aspect
//     gate) — executed-lease document generation as external I/O over the
//     SIGNED leaseapp subject: the instanceOp assembles the document fields
//     Processor-side and emits external.docGen; the reference vendor adapter
//     (internal/bridge FakeDocGen) renders + stores the bytes; the replyOp
//     records the pointer-carrying .outcome; Weaver anchors the artifact via
//     directOp AttachObject (the signedLease slot) off the
//     missing_leaseDocAttach gap. See leasedoc_ddls.go.
//
//   - The `leaseApplicationComplete` actorAggregate convergence lens (§10.2) —
//     anchored on leaseapp, reading identity aspects + the service instance's
//     .outcome aspect across the applicationFor/providedTo links, emitting the
//     bare-NanoID convergence key via 14.2's key column.
//
//   - The `applicantOnboarding` actorAggregate convergence lens + its target —
//     anchored on the applicant IDENTITY, one row per person however many
//     applications they hold. It carries the single missing_onboarding gap and
//     is what dispatches the onboarding pattern; the leaseapp-anchored target
//     keeps projecting the column but declares it `surface`, because the work
//     (recording PII) is per-person while that anchor is per-application.
//
//   - The §10.8 playbook (meta.weaverTarget leaseApplicationComplete) — gap →
//     remediation: missing_bgcheck/missing_payment via triggerLoom (the
//     bgcheck/payment patterns contain an externalTask), missing_signature via
//     assignTask SignLease.
//
//   - The three loomPatterns — backgroundCheck + collectPayment (each a single
//     externalTask step, completionDomains ["orchestration"]) and onboarding (a
//     userTask step over the applicant identity).
//
//   - Op-metas (SignLease is the assignTask forOperation target — functionally
//     required; the externalTask ops are declared for discoverability) and
//     permissions.
//
//   - The renewal vertex type (DDL `renewal`) + its five ops (OpenRenewal /
//     SetRenewalTerms / VerifyGuarantor / SignRenewal / CancelRenewal), a
//     create-only `.tenancy` aspect stamped on the leaseapp by
//     DecideLeaseApplication's first approve, and the two renewal targets:
//     leaseExpiry (Target A, frozen table — opens a cycle) and
//     renewalComplete (Target B, mode: planned — the FIRST goal-authored
//     Weaver target, Contract #10 §10.8 Planner extension). See
//     _bmad-output/implementation-artifacts/loftspace-lease-renewal-goal-authored-target-design.md.
//
//   - The tenancyEnd frozen-table target + EndTenancy (leaseapp DDL,
//     operator-granted): leaseExpiry's sibling one horizon later — a timer on
//     each signed, approved tenancy's leaseEnd, the recorded lapse ending the
//     term (.tenancy.endedAt = leaseEnd) unless an open renewal holds it, and
//     the ended tenancy's unit relisted via SetListingStatus unless another
//     approved tenancy now holds it. See
//     _bmad-output/implementation-artifacts/loftspace-lease-term-and-tenancy-end-design.md.
//
//   - GiveNotice (leaseapp DDL; operator any + consumer self — the tenant via
//     the applicationFor link, the landlord via the manages link): a tenant
//     gives notice and the lease ends early. The create-only .notice aspect
//     {moveOutAt, givenAt, givenBy} is the recorded fact; the term's
//     effective end (termEnd = the earlier of moveOutAt and leaseEnd) is
//     carried in the tenancyEnd lens, which then overrides the open-renewal
//     hold, and in EndTenancy (endedAt = termEnd); SignRenewal refuses
//     NoticeGiven, leaseExpiry opens no cycle, and the three read models
//     project the notice. See docs/reviews/loftspace-tenancy-notice-2026-09-15.md.
//
//   - SetLateFee (leaseapp DDL; operator any + consumer self — the landlord
//     via the manages link on the application's own unit, the
//     DecideLeaseApplication probe): a landlord records the lease's late-fee
//     term as .lateFee {amountCents, recordedAt}, create-or-update, on an
//     approved, not-ended tenancy (NotApproved / TenancyEnded otherwise).
//     semantic-contracts' leaseRentSettlement mints the purpose=lateFee
//     perArrearsEpisode clause from it and amends the clause when the amount
//     changes; loftspace-ledger's arrears evaluation bills that clause once
//     per arrears episode. The two protected read models project
//     late_fee_cents. See docs/reviews/loftspace-ledger-reversal-and-late-fee-2026-09-18.md.
//
//   - RecordApplicationLoss (leaseapp DDL, operator-granted), dispatched by
//     leaseApplicationComplete's missing_lossRecorded gap: a losing rival's
//     loss recorded on the application as .decision = lost, the third
//     terminal value of the landlord's decision aspect, read by every
//     liveness consumer so a relisted unit revives no rival. See
//     _bmad-output/implementation-artifacts/loftspace-recorded-application-loss-design.md.
//
// The external-call outcome lives in the .outcome aspect (D5); the leaseapp /
// service vertex roots stay minimal. Depends identity-domain + service-domain +
// orchestration-base. Install via the InstallPackage kernel op.
package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "lease-signing",
	Version: "0.44.0",
	Description: "Loftspace lease-application convergence vertical: the leaseapp vertex type + CreateLeaseApplication/SignLease, " +
		"the leaseApplicationComplete actorAggregate convergence lens (§10.2 keyColumn), the leaseApplicationsRead " +
		"protected Postgres read model (Contract #6 §6.14 RLS — the applicant-self read boundary, D1.3 Fire 2; carries " +
		"the anchored executed-lease artifact's doc_store_name/doc_filename/doc_content_type pointers) plus its " +
		"landlordLeaseApplicationsRead sibling (the landlord/residence audience anchored on the managing landlord via the " +
		"loftspace-domain manages link, D1.3 Increment 2; carries the SAME doc pointers so the managing landlord reads " +
		"the executed lease off their own anchored row), the identity-anchored applicantOnboarding convergence lens + " +
		"target (one row per applicant, so the PII request is made once however many applications they hold — " +
		"leaseApplicationComplete keeps missing_onboarding as a surface-declared column), the task-anchored " +
		"staleUserTasks lens + target (an open RecordIdentityPII/SignLease/SetRenewalTerms task whose own gap already " +
		"closed through another route is cancelled via directOp CancelTask, mirroring orchestration-base's " +
		"orphanedTaskGrants sweep for the dead-grant half of the same problem), the service-anchored " +
		"backgroundCheckFreshness lens + gap-less target (a completed check's freshness window gets its timer on the " +
		"check itself, so the fired timer records the lapse where it happens and every view of bgcheck freshness reads " +
		"that recorded fact instead of a clock), the §10.8 playbook " +
		"(triggerLoom externalTask for bgcheck/payment/leaseDocument, assignTask SignLease, triggerLoom onboarding off " +
		"applicantOnboarding, directOp " +
		"SetListingStatus to mark the unit leased on approval, directOp AttachObject to anchor the produced executed-lease " +
		"artifact under the signedLease slot), an augur escalation on \"exhausted\" so a gap that spends its retry budget " +
		"reaches AI reasoning instead of an unread Health-KV warning, the externalTask " +
		"instanceOp/replyOp wrapper DDLs (identity-family bgcheck/payment AND the leaseapp-subject docGen triad — the " +
		"vendor-rendered executed-lease document), the bgcheck/payment/onboarding/leaseDocument loomPatterns, SetApplicantProfile " +
		"(the applicant's qualification profile, split three ways along the sensitivity boundary: .profile and " +
		".underwritingParties are SENSITIVE, DEK custodied on the package's own underwritingRecord retention class rather than " +
		"any identity — the applicant's own raw financials and the guarantor/co-applicant's third-party identifiers " +
		"respectively (retention-class-key-custody-design.md §8.7, RetentionClasses()) — while .applicationSignals is the " +
		"NON-sensitive derived half the three shipped lenses project), the fair-housing .decidedProfileSnapshot aspect " +
		"(SENSITIVE, same underwritingRecord custody — DecideLeaseApplication CREATE-ONLY-stamps it on the FIRST decision " +
		"of either value, copying .profile/.underwritingParties/.applicationSignals as they stood then, since " +
		"SetApplicantProfile stays freely re-submittable), SignLease's .tenantName aspect (SENSITIVE, its OWN " +
		"executedLeaseRecord retention class — a different obligation and population from underwritingRecord — " +
		"CREATE-ONLY-stamped at signing from the applicant identity's .name; the leaseDocument pattern egresses it " +
		"to the docGen vendor via the declared subject.tenantName.data.value path), and the lease-renewal chain: a create-only .tenancy aspect (DecideLeaseApplication's " +
		"first approve), the renewal vertex type + its five ops, the leaseExpiry frozen-table target (opens a " +
		"cycle), and the renewalComplete mode:planned target — the first goal-authored Weaver target (Contract #10 " +
		"§10.8 Planner extension), sequencing a per-tenant-variable chain (conditional bgcheck refresh, conditional " +
		"guarantor re-verify, rent-term set, tenant signature) from one declared goal + a 4-action catalog. A lease that " +
		"ends frees its unit: the tenancyEnd frozen-table target arms a timer on each signed, approved tenancy's leaseEnd, " +
		"dispatches EndTenancy (operator-granted) to record .tenancy.endedAt once the recorded lapse reaches it with no " +
		"open renewal, and relists the ended tenancy's unit via SetListingStatus unless another approved tenancy now holds " +
		"it; an ended tenancy is terminal in leaseApplicationComplete and leaseExpiry, and SignRenewal refuses it. A " +
		"tenant gives notice and the lease ends early: GiveNotice (tenant self, landlord manages, or operator) writes the " +
		"create-only .notice aspect {moveOutAt, givenAt, givenBy}; the term's effective end becomes the earlier of the " +
		"move-out and leaseEnd in the tenancyEnd lens (which then overrides the open-renewal hold) and in EndTenancy " +
		"(endedAt = that end), SignRenewal refuses NoticeGiven, leaseExpiry opens no cycle, and the three read models " +
		"project the notice. A " +
		"losing rival's loss is a recorded fact, not a live derivation: leaseApplicationComplete's missing_lossRecorded gap " +
		"dispatches RecordApplicationLoss (operator-granted) to write .decision = lost on an undecided application whose " +
		"unit leased to someone else, and every liveness consumer reads the recorded value, so the winner's later tenancy " +
		"end and relist revive no rival. A landlord records a lease's late-fee term: SetLateFee (landlord manages, or " +
		"operator) writes .lateFee {amountCents, recordedAt} on an approved, not-ended tenancy, which " +
		"semantic-contracts mints as the lease's purpose=lateFee clause and loftspace-ledger bills once per arrears " +
		"episode; the read models project late_fee_cents. An approved lease moves the tenant in: leaseApplicationComplete's " +
		"missing_residence gap dispatches a cross-package directOp WireResidesIn (service-location, operator-granted via " +
		"Weaver's service actor) the instant the term exists, wiring the applicant's residesIn link to the leased unit; " +
		"tenancyEnd's missing_residenceUnwired gap dispatches the mirror UnwireResidesIn once the term ends, unless " +
		"another live tenancy of the SAME applicant on the SAME unit still needs the link. Depends identity-domain + " +
		"service-domain + orchestration-base.",
	Depends:          []string{"identity-domain", "service-domain", "orchestration-base"},
	DDLs:             DDLs(),
	Lenses:           Lenses(),
	Permissions:      Permissions(),
	WeaverTargets:    WeaverTargets(),
	LoomPatterns:     LoomPatterns(),
	OpMetas:          OpMetas(),
	RetentionClasses: RetentionClasses(),
}
