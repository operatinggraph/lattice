package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// underwritingRecordRetentionClass is the canonicalName of the retention-class
// key holder the .profile, .underwritingParties, and .decidedProfileSnapshot
// aspect DDLs' Custody names.
const underwritingRecordRetentionClass = "underwritingRecord"

// executedLeaseRecordRetentionClass is the canonicalName of the retention-class
// key holder the .tenantName aspect DDL's Custody names.
const executedLeaseRecordRetentionClass = "executedLeaseRecord"

// RetentionClasses returns the package's two retention-class key holders: the
// underwritingRecord class the .profile / .underwritingParties /
// .decidedProfileSnapshot aspects' Custody names, and the executedLeaseRecord
// class the .tenantName aspect's Custody names (mirrors
// clinic-domain/retention.go — a package's own list of key holders,
// addressable by canonicalName from any DDL this same package ships).
func RetentionClasses() []pkgmgr.RetentionClassSpec {
	return []pkgmgr.RetentionClassSpec{
		{
			CanonicalName:   underwritingRecordRetentionClass,
			Policy:          pkgmgr.RetentionPolicyEraseOnExpiry,
			RetentionPeriod: "P7Y",
			Description: "Holds the applicant's underwriting record — the raw financial half of SetApplicantProfile's " +
				"three-way split (.profile: annualIncome/employmentStatus/employerName/references/guarantorRelationship/" +
				"guarantorAnnualIncome) and the third-party identifiers that back it (.underwritingParties: " +
				"guarantorName/coApplicantName/coApplicantContact). The obligation this class binds the package to: a " +
				"landlord's underwriting decision is a business record that outlives the applicant's own erasure " +
				"request, so its DEK is custodied here rather than on the applicant's identity — after " +
				"ShredIdentityKey on the applicant, the record survives, pseudonymized, while the applicant's " +
				"directly-identifying .name/.email/.phone become unrecoverable. The guarantor/co-applicant " +
				"identifiers are a DIFFERENT population from the applicant's own PII: they belong to a person who " +
				"never applied and never consented to this record, and who cannot exercise ShredIdentityKey against " +
				"it (retention-class-key-custody-design.md §8.7 rejects minting an unclaimed identity for a third " +
				"party who can never claim it — that would invent machinery to hold data the guarantor cannot " +
				"reach). Rather than a per-row traversed custody (§8.7's rejected mechanism) or a fourth identity no " +
				"one owns, they are held under this SAME class, in their OWN aspect (.underwritingParties) so a later " +
				"fire can rehome them without touching the financial record. THAT OBLIGATION IS NOT YET FULLY MET: " +
				"the guarantor/co-applicant identifiers have no erasure path of their own — they are retained exactly " +
				"as long as the class key exists, with no independent trigger — and this class carries no home for a " +
				"future third-party erasure request; a real one would need to be handled at the class-destruction " +
				"granularity (the whole class, not one row). RetentionPeriod is DECLARATIVE: no automatic expiry timer " +
				"exists yet, so P7Y states the controller's schedule rather than arming one. Destruction is the " +
				"operator-driven ShredRetentionClassKey, and it reaches only records written under this declaration. " +
				"The derived, non-identifying qualification signals (.applicationSignals — incomeToRentMet, " +
				"employmentVerified, referenceCount, hasCoApplicant, hasGuarantor, guarantorIncomeToRentMet, " +
				"submittedAt) are NOT held here — they carry no raw financial fact or third-party identifier, so they " +
				"are a separate, non-sensitive aspect the three shipped lenses (leaseApplicationComplete, " +
				"leaseApplicationsRead, landlordLeaseApplicationsRead) read directly. No Secure Lens exists over this " +
				"class in this fire — the retained fields are captured + custodied, not yet decrypted back to any " +
				"reader.",
		},
		{
			CanonicalName:   executedLeaseRecordRetentionClass,
			Policy:          pkgmgr.RetentionPolicyEraseOnExpiry,
			RetentionPeriod: "P7Y",
			Description: "Holds the applicant's tenant name as it stood at signing (.tenantName, snapshotted by SignLease " +
				"from the applicant identity's own .name) — the party name an executed lease document must render. A " +
				"SEPARATE class from underwritingRecord, deliberately: underwritingRecord's Description enumerates the " +
				"financial-qualification aspects alone (.profile / .underwritingParties / .decidedProfileSnapshot), a " +
				"different population and a different obligation — mixing the signed contract's party name into that " +
				"class would blur the population-separation discipline underwritingRecord's own Description argues for. " +
				"The obligation this class binds the package to (Contract #3 §3.10: \"a contract record keeps its " +
				"parties' names for as long as the contract must be kept\"): a signed lease is a legal document naming " +
				"its tenant, so the name outlives the applicant's own erasure request — after ShredIdentityKey on the " +
				"applicant, the executed lease still names its tenant, pseudonymized against the applicant's other " +
				"directly-identifying aspects. RetentionPeriod is DECLARATIVE: no automatic expiry timer exists yet, so " +
				"P7Y states the controller's schedule rather than arming one. Destruction is the operator-driven " +
				"ShredRetentionClassKey, and it reaches only records written under this declaration.",
		},
	}
}
