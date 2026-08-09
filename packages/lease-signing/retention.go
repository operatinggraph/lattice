package leasesigning

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// underwritingRecordRetentionClass is the canonicalName of the retention-class
// key holder the .profile and .underwritingParties aspect DDLs' Custody names.
const underwritingRecordRetentionClass = "underwritingRecord"

// RetentionClasses returns the package's one retention-class key holder: the
// underwritingRecord class the .profile / .underwritingParties aspects'
// Custody names (mirrors clinic-domain/retention.go — a package's own list of
// key holders, addressable by canonicalName from any DDL this same package
// ships).
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
	}
}
