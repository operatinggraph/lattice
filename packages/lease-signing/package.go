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
//     pending does not collide with the once-only terminal .outcome.
//
//   - The `leaseApplicationComplete` actorAggregate convergence lens (§10.2) —
//     anchored on leaseapp, reading identity aspects + the service instance's
//     .outcome aspect across the applicationFor/providedTo links, emitting the
//     bare-NanoID convergence key via 14.2's key column.
//
//   - The §10.8 playbook (meta.weaverTarget leaseApplicationComplete) — gap →
//     remediation: missing_onboarding/missing_bgcheck/missing_payment via
//     triggerLoom (the bgcheck/payment patterns contain an externalTask),
//     missing_signature via assignTask SignLease.
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
// The external-call outcome lives in the .outcome aspect (D5); the leaseapp /
// service vertex roots stay minimal. Depends identity-domain + service-domain +
// orchestration-base. Install via the InstallPackage kernel op.
package leasesigning

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:    "lease-signing",
	Version: "0.17.0",
	Description: "Loftspace lease-application convergence vertical: the leaseapp vertex type + CreateLeaseApplication/SignLease, " +
		"the leaseApplicationComplete actorAggregate convergence lens (§10.2 keyColumn), the leaseApplicationsRead " +
		"protected Postgres read model (Contract #6 §6.14 RLS — the applicant-self read boundary, D1.3 Fire 2; unit_bedrooms/" +
		"unit_bathrooms/unit_available_from columns added D1.5) plus its " +
		"landlordLeaseApplicationsRead sibling (the landlord/residence audience anchored on the managing landlord via the " +
		"loftspace-domain manages link, D1.3 Increment 2), the §10.8 playbook " +
		"(triggerLoom externalTask for bgcheck/payment, assignTask SignLease, triggerLoom onboarding, directOp " +
		"SetListingStatus to mark the unit leased on approval), the externalTask " +
		"instanceOp/replyOp wrapper DDLs, the bgcheck/payment/onboarding loomPatterns, SetApplicantProfile " +
		"(the applicant's qualification profile — raw financials captured in Core KV, only derived landlord-facing " +
		"signals projected), and the lease-renewal chain: a create-only .tenancy aspect (DecideLeaseApplication's " +
		"first approve), the renewal vertex type + its five ops, the leaseExpiry frozen-table target (opens a " +
		"cycle), and the renewalComplete mode:planned target — the first goal-authored Weaver target (Contract #10 " +
		"§10.8 Planner extension), sequencing a per-tenant-variable chain (conditional bgcheck refresh, conditional " +
		"guarantor re-verify, rent-term set, tenant signature) from one declared goal + a 4-action catalog. Depends " +
		"identity-domain + service-domain + orchestration-base.",
	Depends:       []string{"identity-domain", "service-domain", "orchestration-base"},
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	LoomPatterns:  LoomPatterns(),
	OpMetas:       OpMetas(),
}
