package clinicdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// RetentionClasses returns the package's one retention-class key holder: the
// clinicalRecord class the .encounter aspect's Custody names. Declaring the
// class here (rather than inline on the DDL) mirrors Roles/Permissions — a
// package's own list of key holders, addressable by canonicalName from any
// DDL this same package ships.
func RetentionClasses() []pkgmgr.RetentionClassSpec {
	return []pkgmgr.RetentionClassSpec{
		{
			CanonicalName:   clinicalRecordRetentionClass,
			Policy:          pkgmgr.RetentionPolicyEraseOnExpiry,
			RetentionPeriod: "P7Y",
			Description: "Holds the raw clinical record (the .encounter aspect: summary / assessment / plan) whose " +
				"retention obligation outlives any one patient's erasure request. After ShredIdentityKey on a patient's " +
				"identity the record survives — the appointment's identifiedBy path to that identity resolves to " +
				"nothing, but .encounter itself stays readable — while that patient's directly-identifying " +
				".name/.email/.phone become unrecoverable. The obligation this class binds the package to: a record " +
				"retained under this class MUST NOT duplicate the subject's direct identifiers — a patient name, " +
				"contact detail, or other direct identifier copied into a summary, assessment, or plan survives the " +
				"erasure it was supposed to be subject to and defeats the whole plane. THAT OBLIGATION IS NOT YET MET " +
				"HERE: the patient's name sits plaintext on vtx.patient.<NanoID>.demographics.fullName, outside the " +
				"identity ShredIdentityKey reaches, and projects as patient_name onto the same read-model rows that " +
				"carry the visit — so a shredded patient's retained record is still identified. Moving fullName onto " +
				"the identity's own sensitive .name aspect is what makes the survival pseudonymous; until that lands, " +
				"this class delivers retention WITHOUT pseudonymization. RetentionPeriod is DECLARATIVE: no automatic " +
				"expiry timer exists yet, so P7Y states the controller's schedule rather than arming one. Destruction " +
				"is the operator-driven ShredRetentionClassKey, and it reaches only records written under this " +
				"declaration — an .encounter written before it is plaintext at rest and outside the shred. The one " +
				"read path back to a record held here is clinicEncountersRead, the provider-anchored Secure Lens " +
				"that decrypts summary/assessment/plan at projection for the treating provider; destroying this " +
				"class's key nulls all three on the rebuild cmd/refractor triggers for every lens declaring this " +
				"holder type.",
		},
	}
}
