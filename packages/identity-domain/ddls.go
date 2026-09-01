package identitydomain

import (
	"strings"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// consumerRoleKey is this package's own "consumer" role key, computed
// deterministically (pkgmgr.RoleID mirrors what the installer mints at
// install time — no KV read required). ProvisionConsumerIdentity's script
// pins its consumerRoleKey payload field against this literal rather than
// trusting any live vtx.role.* the caller supplies (defense-in-depth: the
// grant matrix already restricts who can call the op, but the op's OWN
// script should not be able to be steered into granting a different role,
// e.g. operator, to a first-touch actor).
var consumerRoleKey = "vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")

// DDLs returns the package's DDL meta-vertex declarations:
//   - `identity` (meta.ddl.vertexType) — handles CreateUnclaimedIdentity,
//     UpdateIdentityState, ClaimIdentity, RecordIdentityPII. State machine:
//     unclaimed → claimed; merged is set only by identity-hygiene's
//     MergeIdentity.
//   - `ssn`, `dob`, `name`, `email`, `phone`, `claimKey`,
//     `credentialBinding` (meta.ddl.aspectType, sensitive) — declare the
//     identity domain's sensitive PII aspect types. Marking them sensitive=true
//     makes the Processor's step-6 validator anchor them to identity vertices
//     (NFR-S3 / lattice-architecture Item 6). ssn/dob are written only by
//     RecordIdentityPII and carry permittedCommands:["RecordIdentityPII"]. The
//     other five are written by multiple ops across packages
//     (CreateUnclaimedIdentity, ClaimIdentity, and identity-hygiene's
//     MergeIdentity), so they carry no permittedCommands — sensitivity
//     (identity-anchoring) is their only enforcement, deliberately leaving the
//     writer unrestricted.
//
// Architectural rules: known-key reads only. The duplicate-detection
// index lookups (vtx.identityindex.*) use crypto.sha256NanoID-derived
// known keys provided by the caller in ContextHint.Reads.
// ProvisionConsumerIdentity's read-before-create existence check and its
// consumerRoleKey validity check use kv.Read instead (Contract #2 §2.5):
// both keys may legitimately be absent, and a declared-but-absent
// ContextHint read faults (HydrationMiss) the first time the script touches
// it — not at hydration, which only records the absence.
// RecordIdentityPII's role-confinement check (a non-operator caller without a
// validated target naming the identity may write only on an unclaimed one —
// facet-staff-worlds-design.md §3.2)
// is the one sanctioned live enumeration, read-posture (e): the actor's
// holdsRole links, mirroring the F4 workplace guard's own
// actor_holds_operator walk.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		{
			CanonicalName: "identity",
			Class:         "meta.ddl.vertexType",
			PermittedCommands: []string{
				"CreateUnclaimedIdentity",
				"UpdateIdentityState",
				"ClaimIdentity",
				"RotateClaimKey",
				"RecordIdentityPII",
				"ProvisionConsumerIdentity",
				"InitiateCredentialLink",
				"CompleteCredentialLink",
				"UnlinkCredential",
				"ReconcileCredentialBinding",
			},
			Description: "Identity domain DDL. " +
				"Vertex shape: vtx.identity.<NanoID>, class=identity. " +
				"Aspects: name (sensitive, required, maxLen 200), email (sensitive, lowercase-normalized), " +
				"phone (sensitive, E.164-normalized), state (enum: unclaimed|claimed|merged), " +
				"ssn (sensitive, applicant SSN: 9 digits; any hyphens accepted and stripped; written by RecordIdentityPII), " +
				"dob (sensitive, ISO YYYY-MM-DD applicant date of birth, written by RecordIdentityPII), " +
				"claimKey (sensitive, stores the client-supplied claimKeyHash verbatim; tombstoned after claim), " +
				"linkKey (sensitive; stores the client-supplied linkKeyHash verbatim; armed by InitiateCredentialLink, " +
				"tombstoned by CompleteCredentialLink; re-initiating overwrites), " +
				"credentialBinding (sensitive; null pre-claim; data.credentials is the N-credential array a second " +
				"CompleteCredentialLink appends to, and UnlinkCredential removes an entry from — " +
				"multi-credential-identity-linking-design.md §3.1/§8), " +
				"idpBinding (sensitive; the raw iss/sub of the external IdP token an opaque-mode ActorID was " +
				"derived from — Contract #11 §11.3, written only by ProvisionConsumerIdentity, absent for a " +
				"nanoid-mode/dev-provisioned actor), " +
				"mergedInto (vertex-key reference, set only by identity-hygiene package's MergeIdentity), " +
				"erasureRequested (privacy-base-owned marker; its PRESENCE closes this identity's write path — " +
				"ClaimIdentity, CompleteCredentialLink and ReconcileCredentialBinding refuse once it exists, in " +
				"EITHER link position (UnbindIdentityCredentials erases boundTo in both directions on erasure, so " +
				"an erased identity is gated as the credential as well as as the owner), and CreateUnclaimedIdentity stops treating the " +
				"identity as a dedup incumbent so no fresh duplicateOf names it. That is what makes the residue " +
				"count the erasure's completion seal rests on measure a CLOSED set; erasure-orchestration-design.md " +
				"§6, hydrated by this DDL's own derive_reads so no submitter declares it). " +
				"The client mints the claim secret, submits only claimKeyHash; Lattice never holds the plaintext. " +
				"State machine + IdentityMerged guard enforced in .script. " +
				"ProvisionConsumerIdentity: idempotently creates a bare, already-claimed consumer identity at a " +
				"caller-supplied key (the Gateway's authenticated-touch auto-provisioning pre-flight) — " +
				"the deterministic ActorID a verified JWT subject maps to, not a minted key. Grants the consumer " +
				"role via a holdsRole link; optionally records IdP provenance (.idpBinding) when the token was " +
				"opaque-mode; otherwise no PII. Where the identity vertex already exists but holds no consumer " +
				"grant — a seeded persona, or an identity an entity binding minted — it creates the grant alone, " +
				"so the op's contract holds for every actor the Gateway verifies and not only for ones it minted; " +
				"a grant that was explicitly revoked stays revoked. " +
				"RotateClaimKey: staff-gated re-issue of an unclaimed identity's claimKeyHash (the lost-secret " +
				"recovery path — Lattice never held the plaintext to recover). Overwrites .claimKey; fails closed " +
				"unless state==unclaimed. " +
				"RecordIdentityPII: a STANDING frontOfHouse/backOfHouse caller may target only " +
				"an unclaimed identity — the walk-in-registration beat this op exists for; a claimed identity's " +
				"PII belongs to an already-onboarded person, and AuthDenied there closes the unscoped-write gap " +
				"facet-staff-worlds-design.md §3.2 flagged. Root (operator) is exempt, and so is a submission " +
				"whose target step 3 VALIDATED (a task grant scopedTo the identity, or scope=self) AND whose " +
				"validated target NAMES the identity being written — e.g. lease-signing's onboarding userTask, " +
				"where the applicant records their own PII and is already bound by the task's own grant. " +
				"Attaching an authContext.target of one's own choosing reaches nothing: an unvalidated target " +
				"exempts nobody, and a validated one may not be paired with a payload naming someone else. " +
				"ReconcileCredentialBinding: converges the boundTo link plane onto the credentialindex vertex " +
				"for one credential (operator-only). The index vertex is the authority — it carries " +
				"{actorKey, identityKey, boundAt} in plaintext, it is what UnlinkCredential tombstones, and the " +
				"payload's identityKey must equal the one it records or the op rejects, so a caller cannot mint " +
				"an edge the index does not already assert. Writes the index's original boundAt, never the " +
				"submission time; re-runnable, and a tombstoned index rejects rather than reviving an unlinked " +
				"credential. Also requires the credential to be a live identity vertex " +
				"(credential-not-provisioned): the index names its credential in its own body and can therefore " +
				"assert one that has no vertex, and the pre-link corpus this op exists to converge is the " +
				"population most likely to contain them — without the check the repair path would publish " +
				"exactly the dangling edges the bind paths refuse.",
			Script: identityDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"name":{"type":"string","maxLength":200,"description":"Person's display name. Required for CreateUnclaimedIdentity."},` +
				`"email":{"type":"string","description":"Email address, case-insensitive normalized. At least one of email/phone required."},` +
				`"phone":{"type":"string","description":"Phone number, E.164 digits only. At least one of email/phone required."},` +
				`"claimKeyHash":{"type":"string","description":"Lowercase hex sha256 of the client-minted claim secret (CreateUnclaimedIdentity, required). Lattice stores it verbatim; the plaintext never enters Lattice."},` +
				`"claimKeyAlgo":{"type":"string","enum":["sha256"],"description":"Hash algorithm for claimKeyHash. Optional; defaults to sha256 (the only accepted value)."},` +
				`"identityKey":{"type":"string","description":"vtx.identity.<NanoID> — target identity for UpdateIdentityState, RecordIdentityPII, and ReconcileCredentialBinding (where it is the owner the credentialindex vertex must already record)."},` +
				`"newState":{"type":"string","enum":["claimed"],"description":"Target state for UpdateIdentityState. Only unclaimed→claimed is permitted."},` +
				`"claimKey":{"type":"string","description":"One-time-use claim key plaintext (ClaimIdentity). Its sha256 must match the stored hash."},` +
				`"targetIdentityKey":{"type":"string","description":"vtx.identity.<NanoID> of the unclaimed identity to claim (ClaimIdentity)."},` +
				`"ssn":{"type":"string","description":"Applicant Social Security Number (RecordIdentityPII, required). 9 digits; any hyphens are accepted and stripped; stored normalized as a sensitive aspect."},` +
				`"dob":{"type":"string","description":"Applicant date of birth (RecordIdentityPII, required). ISO YYYY-MM-DD; stored as a sensitive aspect."},` +
				`"targetActorKey":{"type":"string","description":"vtx.identity.<NanoID> — the ActorID a verified JWT subject maps to (ProvisionConsumerIdentity). Caller-derived, never minted."},` +
				`"consumerRoleKey":{"type":"string","description":"vtx.role.<NanoID> of the consumer role to grant (ProvisionConsumerIdentity). Caller-resolved via pkgmgr.RoleID; validated alive before granting."},` +
				`"idpIssuer":{"type":"string","description":"Raw JWT iss claim (ProvisionConsumerIdentity, optional). Present only for an opaque-mode token (Contract #11 §11.3); written verbatim into the .idpBinding aspect."},` +
				`"idpSubject":{"type":"string","description":"Raw JWT sub claim (ProvisionConsumerIdentity, optional). Must accompany idpIssuer; written verbatim into the .idpBinding aspect."},` +
				`"linkKeyHash":{"type":"string","description":"Lowercase hex sha256 of the client-minted link secret (InitiateCredentialLink, required). Lattice stores it verbatim; the plaintext never enters Lattice."},` +
				`"linkKeyAlgo":{"type":"string","enum":["sha256"],"description":"Hash algorithm for linkKeyHash. Optional; defaults to sha256 (the only accepted value)."},` +
				`"linkKey":{"type":"string","description":"One-time-use link key plaintext (CompleteCredentialLink). Its sha256 must match the stored hash on targetIdentityKey."},` +
				`"credentialActorKey":{"type":"string","description":"vtx.identity.<NanoID> of the bound credential to remove (UnlinkCredential, required). Submitted as U (op.actor==U, scope=self); names one entry in U's own credentials array. On ReconcileCredentialBinding it names the credential whose boundTo edge is being converged onto its index vertex."}}}`,
			OutputSchema: `{"type":"object","properties":` +
				`{"primaryKey":{"type":"string","description":"vtx.identity.<NanoID> of the created, claimed, or PII-recorded identity (the operation's principal key)."}}}`,
			FieldDescription: map[string]string{
				"name":               "Person's display name. Required on CreateUnclaimedIdentity. Stored as sensitive aspect.",
				"email":              "Email address. Stored lowercase-normalized. Used as a deduplication index key.",
				"phone":              "Phone number. Stored as E.164 digit string. Used as a deduplication index key.",
				"claimKeyHash":       "Lowercase hex sha256 of the client-minted claim secret. Required on CreateUnclaimedIdentity. Stored verbatim; Lattice never holds the plaintext.",
				"claimKeyAlgo":       "Hash algorithm for claimKeyHash. Optional; defaults to sha256 (the only accepted value).",
				"identityKey":        "Full vtx.identity.<NanoID> key of an existing identity vertex.",
				"newState":           "Desired state after UpdateIdentityState. State machine: unclaimed → claimed only.",
				"claimKey":           "The plaintext one-time claim key the client minted at CreateUnclaimedIdentity. Used for ClaimIdentity verification (its sha256 is compared to the stored hash).",
				"targetIdentityKey":  "Full vtx.identity.<NanoID> of the unclaimed identity the calling actor wants to claim.",
				"ssn":                "Applicant SSN. Required on RecordIdentityPII. 9 digits; any hyphens are accepted and stripped; stored normalized in a sensitive vtx.identity.<NanoID>.ssn aspect.",
				"dob":                "Applicant date of birth. Required on RecordIdentityPII. ISO YYYY-MM-DD; stored in a sensitive vtx.identity.<NanoID>.dob aspect.",
				"targetActorKey":     "vtx.identity.<NanoID> ActorID to provision (ProvisionConsumerIdentity, required). Must be the exact key a verified JWT subject resolves to; rejected if not NanoID-shaped.",
				"consumerRoleKey":    "vtx.role.<NanoID> of the consumer role (ProvisionConsumerIdentity, required). Must resolve to a live role vertex; rejected otherwise.",
				"idpIssuer":          "Raw JWT iss claim (ProvisionConsumerIdentity, optional; present only for an opaque-mode token). Stored verbatim in the sensitive .idpBinding aspect.",
				"idpSubject":         "Raw JWT sub claim (ProvisionConsumerIdentity, optional; must accompany idpIssuer). Stored verbatim in the sensitive .idpBinding aspect.",
				"linkKeyHash":        "Lowercase hex sha256 of the client-minted link secret. Required on InitiateCredentialLink. Stored verbatim; Lattice never holds the plaintext.",
				"linkKeyAlgo":        "Hash algorithm for linkKeyHash. Optional; defaults to sha256 (the only accepted value).",
				"linkKey":            "The plaintext one-time link key the client minted at InitiateCredentialLink. Used for CompleteCredentialLink verification (its sha256 is compared to the stored hash).",
				"credentialActorKey": "vtx.identity.<NanoID> of the bound credential to remove (UnlinkCredential, required). Must name a live entry in the caller's own credentials array; the caller's implicit self-credential and the set's last remaining entry are both rejected.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:    "CreateUnclaimedIdentity — new customer with email",
					Payload: map[string]any{"name": "Alice Smith", "email": "alice@example.com", "claimKeyHash": "<sha256-hex-of-client-minted-secret>"},
					ExpectedOutcome: "Creates vtx.identity.<NanoID> with class=identity, writes name/email/state/claimKey aspects " +
						"(claimKey stores the supplied hash verbatim). Returns primaryKey (the identity key). " +
						"Duplicate detection rides the identity.created event's data.duplicate flag, not the reply.",
				},
				{
					Name:    "ClaimIdentity — actor claims their identity",
					Payload: map[string]any{"targetIdentityKey": "vtx.identity.<NanoID>", "claimKey": "<plaintextKey>"},
					ExpectedOutcome: "Verifies claimKey hash, writes credentialBinding aspect, transitions state unclaimed→claimed, " +
						"tombstones claimKey aspect, grants holdsRole→consumer to the claimed identity. Requires the " +
						"submitting credential (op.actor) to be a LIVE identity vertex — the emitted boundTo edge is the " +
						"sole input to identityCredentialBindingsRead, which anchors on the credential and would project " +
						"nothing at all for a credential that was never provisioned. An unprovisioned actor is refused " +
						"`credential-not-provisioned` (mint it with ProvisionConsumerIdentity, or `lattice identity " +
						"provision --actor`); a TOMBSTONED actor is refused the same way and permanently, because " +
						"ProvisionConsumerIdentity no-ops on a tombstoned actor — a revoked credential is revoked, not " +
						"invalid. Both collapse to the generic ClaimKeyInvalid wire code (NFR-S6).",
				},
				{
					Name:    "RotateClaimKey — staff re-issues a lost claim secret",
					Payload: map[string]any{"identityKey": "vtx.identity.<NanoID>", "claimKeyHash": "<sha256-hex-of-a-new-client-minted-secret>"},
					ExpectedOutcome: "Overwrites the identity's .claimKey aspect with the new hash. Rejects a " +
						"claimed/merged/tombstoned identity (InvalidStateTransition) — only an unclaimed identity " +
						"has a secret worth rotating.",
				},
				{
					Name:    "InitiateCredentialLink — U arms a link secret for a second credential",
					Payload: map[string]any{"linkKeyHash": "<sha256-hex-of-client-minted-secret>"},
					ExpectedOutcome: "Submitted as the already-claimed identity U (op.actor == U, scope=self). Writes/overwrites " +
						"vtx.identity.<NanoID>.linkKey {hash, algo}. Re-initiating rotates a lost secret. Rejects a not-found, " +
						"tombstoned, unclaimed, or merged U.",
				},
				{
					Name:    "CompleteCredentialLink — a second credential proves the secret, binds to U",
					Payload: map[string]any{"targetIdentityKey": "vtx.identity.<NanoID-of-U>", "linkKey": "<plaintextLinkKey>"},
					ExpectedOutcome: "Submitted as the raw new credential A2 (op.actor == A2, scope=self, Gateway raw-credential " +
						"carve-out). Verifies the linkKey hash, creates vtx.credentialindex.<hash(A2)>, appends " +
						"{actorKey:A2,boundAt} to U.credentialBinding.credentials (creating the aspect if U never had one), " +
						"tombstones U.linkKey, emits identity.claimed{identityKey:U, actorKey:A2} — the same class " +
						"ClaimIdentity emits, so the credential-bindings materializer folds it with zero changes. Rejects a " +
						"wrong/spent secret, an already-bound A2, an A2 that is not a live identity vertex " +
						"(`credential-not-provisioned` — same endpoint guard and same reasoning as ClaimIdentity), " +
						"or a not-claimed U — all collapse to the same " +
						"generic ClaimKeyInvalid wire code ClaimIdentity uses (NFR-S6 anti-enumeration; the " +
						"Processor's classifier reclassifies any \"ClaimKeyInvalid: <outcome>\" fail() message the " +
						"same way regardless of which op raised it — specific outcomes surface only via Health KV).",
				},
				{
					Name:    "UnlinkCredential — U removes a bound credential",
					Payload: map[string]any{"credentialActorKey": "vtx.identity.<NanoID-of-A>"},
					ExpectedOutcome: "Submitted as U (op.actor == U, scope=self). Tombstones vtx.credentialindex.<hash(A)>, removes A " +
						"from U.credentialBinding.credentials, emits identity.unbound{identityKey:U, actorKey:A} — the " +
						"credential-bindings materializer deletes A's bucket entry (the one explicit row-set shrink in " +
						"this plane). Rejects (generic CredentialUnlinkRejected) a credential not in U's set, or removing " +
						"the set's last remaining entry — an identity must keep at least one sign-in path.",
				},
				{
					Name:    "RecordIdentityPII — capture applicant SSN/DOB",
					Payload: map[string]any{"identityKey": "vtx.identity.<NanoID>", "ssn": "123-45-6789", "dob": "1990-01-15"},
					ExpectedOutcome: "Validates formats, writes sensitive vtx.identity.<NanoID>.ssn (normalized to 123456789) and " +
						".dob aspects onto the existing identity; the identity vertex root data is not mutated. " +
						"A sensitive ssn/dob aspect on any non-identity vertex is rejected by the step-6 sensitiveAspectScope rule. " +
						"A STANDING (no authContext) frontOfHouse/backOfHouse caller targeting an already-claimed " +
						"identity is rejected AuthDenied; operator and any task/self-scoped submission are exempt.",
				},
				{
					Name:    "ProvisionConsumerIdentity — Gateway first-touch auto-provisioning",
					Payload: map[string]any{"targetActorKey": "vtx.identity.<NanoID>", "consumerRoleKey": "vtx.role.<NanoID>"},
					ExpectedOutcome: "Fresh actor: creates the identity vertex + a .state=claimed aspect + a holdsRole link to " +
						"consumerRoleKey, emits identity.provisioned, returns primaryKey=targetActorKey. Existing identity " +
						"without the grant (a seeded persona, an entity binding's identity): creates the holdsRole link alone, " +
						"emits identity.consumerGranted, returns primaryKey=<link key>. Already-granted actor: no-op (empty " +
						"mutations/events, no response) — safe to call on every request. A tombstoned grant stays revoked and a " +
						"tombstoned identity acquires nothing.",
				},
				{
					Name: "ProvisionConsumerIdentity — opaque-mode first touch with IdP provenance",
					Payload: map[string]any{
						"targetActorKey": "vtx.identity.<NanoID>", "consumerRoleKey": "vtx.role.<NanoID>",
						"idpIssuer": "https://accounts.google.com", "idpSubject": "110169484474386276334",
					},
					ExpectedOutcome: "Same as the fresh-actor case, plus a sensitive .idpBinding aspect recording the raw " +
						"iss/sub (Contract #11 §11.3) — the audit answer to which IdP account this identity is.",
				},
			},
		},
		{
			CanonicalName:     "ssn",
			Class:             "meta.ddl.aspectType",
			Sensitive:         true,
			PermittedCommands: []string{"RecordIdentityPII"},
			Description: "Applicant Social Security Number. Sensitive aspect-type " +
				"(lattice-architecture Item 6 / PRD §358): stored as vtx.identity.<NanoID>.ssn, " +
				"sensitive=true, identity-anchored, the crypto-shred unit. Written by RecordIdentityPII.",
			Script: sensitiveAspectDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"ssn":{"type":"string","description":"SSN: 9 digits; any hyphens are accepted and stripped."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"ssn": "Applicant SSN: 9 digits; any hyphens are accepted and stripped; stored normalized as a sensitive aspect on the identity.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "ssn aspect",
					Payload:         map[string]any{"ssn": "123-45-6789"},
					ExpectedOutcome: "Stored as sensitive vtx.identity.<NanoID>.ssn; rejected on any non-identity vertex by step-6 sensitiveAspectScope.",
				},
			},
		},
		{
			CanonicalName:     "dob",
			Class:             "meta.ddl.aspectType",
			Sensitive:         true,
			PermittedCommands: []string{"RecordIdentityPII"},
			Description: "Applicant date of birth. Sensitive aspect-type " +
				"(lattice-architecture Item 6 / PRD §358): stored as vtx.identity.<NanoID>.dob, " +
				"sensitive=true, identity-anchored, the crypto-shred unit. Written by RecordIdentityPII.",
			Script: sensitiveAspectDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"dob":{"type":"string","description":"ISO 8601 calendar date, YYYY-MM-DD."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"dob": "Applicant date of birth, ISO YYYY-MM-DD, stored as a sensitive aspect on the identity.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "dob aspect",
					Payload:         map[string]any{"dob": "1990-01-15"},
					ExpectedOutcome: "Stored as sensitive vtx.identity.<NanoID>.dob; rejected on any non-identity vertex by step-6 sensitiveAspectScope.",
				},
			},
		},
		{
			CanonicalName: "name",
			Class:         "meta.ddl.aspectType",
			Sensitive:     true,
			Description: "Person's display name. Sensitive aspect-type " +
				"(lattice-architecture Item 6 / PRD §358): stored as vtx.identity.<NanoID>.name, " +
				"sensitive=true, identity-anchored. Written by CreateUnclaimedIdentity and " +
				"overwritten by identity-hygiene's MergeIdentity aspectConflictResolution; " +
				"permittedCommands is intentionally empty so any identity-anchored writer is allowed.",
			Script: sensitiveAspectDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"name":{"type":"string","maxLength":200,"description":"Person's display name."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"name": "Person's display name, stored as a sensitive aspect on the identity.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "name aspect",
					Payload:         map[string]any{"name": "Alice Smith"},
					ExpectedOutcome: "Stored as sensitive vtx.identity.<NanoID>.name; rejected on any non-identity vertex by step-6 sensitiveAspectScope.",
				},
			},
		},
		{
			CanonicalName: "email",
			Class:         "meta.ddl.aspectType",
			Sensitive:     true,
			Description: "Email address. Sensitive aspect-type " +
				"(lattice-architecture Item 6 / PRD §358): stored as vtx.identity.<NanoID>.email, " +
				"sensitive=true, identity-anchored. Written by CreateUnclaimedIdentity and " +
				"overwritten by identity-hygiene's MergeIdentity aspectConflictResolution; " +
				"permittedCommands is intentionally empty so any identity-anchored writer is allowed.",
			Script: sensitiveAspectDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"email":{"type":"string","description":"Email address, lowercase-normalized."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"email": "Email address, lowercase-normalized, stored as a sensitive aspect on the identity.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "email aspect",
					Payload:         map[string]any{"email": "alice@example.com"},
					ExpectedOutcome: "Stored as sensitive vtx.identity.<NanoID>.email; rejected on any non-identity vertex by step-6 sensitiveAspectScope.",
				},
			},
		},
		{
			CanonicalName: "phone",
			Class:         "meta.ddl.aspectType",
			Sensitive:     true,
			Description: "Phone number. Sensitive aspect-type " +
				"(lattice-architecture Item 6 / PRD §358): stored as vtx.identity.<NanoID>.phone, " +
				"sensitive=true, identity-anchored. Written by CreateUnclaimedIdentity and " +
				"overwritten by identity-hygiene's MergeIdentity aspectConflictResolution; " +
				"permittedCommands is intentionally empty so any identity-anchored writer is allowed.",
			Script: sensitiveAspectDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"phone":{"type":"string","description":"Phone number, E.164 digit string."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"phone": "Phone number, E.164 digit string, stored as a sensitive aspect on the identity.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "phone aspect",
					Payload:         map[string]any{"phone": "+15551234567"},
					ExpectedOutcome: "Stored as sensitive vtx.identity.<NanoID>.phone; rejected on any non-identity vertex by step-6 sensitiveAspectScope.",
				},
			},
		},
		{
			CanonicalName: "claimKey",
			Class:         "meta.ddl.aspectType",
			Sensitive:     true,
			Description: "Client-supplied claim-key hash. Sensitive aspect-type " +
				"(lattice-architecture Item 6 / PRD §358): stored as vtx.identity.<NanoID>.claimKey, " +
				"sensitive=true, identity-anchored. Written by CreateUnclaimedIdentity and tombstoned " +
				"by ClaimIdentity; permittedCommands is intentionally empty so any identity-anchored " +
				"writer is allowed.",
			Script: sensitiveAspectDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"hash":{"type":"string","description":"Lowercase hex sha256 of the client-minted claim secret, stored verbatim."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"hash": "Lowercase hex sha256 of the client-minted claim secret, stored verbatim as a sensitive aspect on the identity.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "claimKey aspect",
					Payload:         map[string]any{"hash": "<sha256-hex-of-client-minted-secret>"},
					ExpectedOutcome: "Stored as sensitive vtx.identity.<NanoID>.claimKey; rejected on any non-identity vertex by step-6 sensitiveAspectScope.",
				},
			},
		},
		{
			CanonicalName: "linkKey",
			Class:         "meta.ddl.aspectType",
			Sensitive:     true,
			Description: "Client-supplied link-key hash — the claimKey twin for binding a SECOND credential " +
				"to an already-claimed identity (multi-credential-identity-linking-design.md §3.2). Sensitive " +
				"aspect-type: stored as vtx.identity.<NanoID>.linkKey, sensitive=true, identity-anchored. " +
				"Written (create-or-overwrite) by InitiateCredentialLink, verified + tombstoned by " +
				"CompleteCredentialLink; permittedCommands is intentionally empty, mirroring claimKey.",
			Script: sensitiveAspectDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"hash":{"type":"string","description":"Lowercase hex sha256 of the client-minted link secret, stored verbatim."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"hash": "Lowercase hex sha256 of the client-minted link secret, stored verbatim as a sensitive aspect on the identity.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "linkKey aspect",
					Payload:         map[string]any{"hash": "<sha256-hex-of-client-minted-secret>"},
					ExpectedOutcome: "Stored as sensitive vtx.identity.<NanoID>.linkKey; rejected on any non-identity vertex by step-6 sensitiveAspectScope.",
				},
			},
		},
		{
			CanonicalName: "credentialBinding",
			Class:         "meta.ddl.aspectType",
			Sensitive:     true,
			Description: "Actor-to-identity credential binding. Sensitive aspect-type " +
				"(lattice-architecture Item 6 / PRD §358): stored as vtx.identity.<NanoID>.credentialBinding, " +
				"sensitive=true, identity-anchored. Written by ClaimIdentity (first credential) and " +
				"CompleteCredentialLink (Nth credential, appends to data.credentials); permittedCommands is " +
				"intentionally empty so any identity-anchored writer is allowed.",
			Script: sensitiveAspectDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"actorKey":{"type":"string","description":"First-bound credential's actor key (Contract #9 record; kept for the single-credential case)."},` +
				`"boundAt":{"type":"string","description":"Timestamp the first binding was established."},` +
				`"credentials":{"type":"array","description":"N-credential array [{actorKey,boundAt}, ...] every credential resolving to this identity; absent on a pre-Fire-2 record (readers fall back to the singular actorKey/boundAt fields).","items":{"type":"object"}}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"actorKey":    "Actor key bound to the identity at claim time, stored as a sensitive aspect on the identity.",
				"boundAt":     "Timestamp the credential binding was established.",
				"credentials": "N-credential array; every CompleteCredentialLink/MergeIdentity repoint appends/unions into this array.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "credentialBinding aspect",
					Payload:         map[string]any{"actorKey": "vtx.actor.<NanoID>", "boundAt": "2026-05-22T11:00:00Z"},
					ExpectedOutcome: "Stored as sensitive vtx.identity.<NanoID>.credentialBinding; rejected on any non-identity vertex by step-6 sensitiveAspectScope.",
				},
			},
		},
		{
			CanonicalName:     "idpBinding",
			Class:             "meta.ddl.aspectType",
			Sensitive:         true,
			PermittedCommands: []string{"ProvisionConsumerIdentity"},
			Description: "External IdP account provenance. Sensitive aspect-type " +
				"(Contract #11 §11.3): stored as vtx.identity.<NanoID>.idpBinding, sensitive=true, " +
				"identity-anchored, the crypto-shred unit — shredding the identity's DEK severs the " +
				"IdP-account linkage. Written only by ProvisionConsumerIdentity, and only for an opaque-mode " +
				"token (Contract #11 §11.3); a nanoid-mode/dev-provisioned actor never gets this aspect. The " +
				"audit/support answer to \"which IdP account is this identity?\" — the derivation " +
				"(SHA256NanoID) is one-way, so without this aspect the question is unanswerable.",
			Script: sensitiveAspectDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"iss":{"type":"string","description":"Raw JWT iss claim of the external IdP token the ActorID was derived from."},` +
				`"sub":{"type":"string","description":"Raw JWT sub claim of the external IdP token the ActorID was derived from."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"iss": "Raw JWT iss claim, stored verbatim as a sensitive aspect on the identity.",
				"sub": "Raw JWT sub claim, stored verbatim as a sensitive aspect on the identity.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "idpBinding aspect",
					Payload:         map[string]any{"iss": "https://accounts.google.com", "sub": "110169484474386276334"},
					ExpectedOutcome: "Stored as sensitive vtx.identity.<NanoID>.idpBinding; rejected on any non-identity vertex by step-6 sensitiveAspectScope.",
				},
			},
		},
		{
			CanonicalName: "indexes",
			Class:         "meta.ddl.linkType",
			Description: "identityindex indexes identity. Ownership edge from a " +
				"vtx.identityindex.<hash> vertex to the vtx.identity.<NanoID> it currently points at " +
				"(lnk.identityindex.<hash>.indexes.identity.<NanoID>). Created in the same batch as the " +
				"index vertex by CreateUnclaimedIdentity, which also repoints it (tombstone + create) " +
				"when the incumbent it names has had its erasure write path closed — a live index owned " +
				"by an erased person otherwise denies every later registrant on that contact an index of " +
				"their own. Repointed the same way by identity-hygiene's MergeIdentity; tombstoned by " +
				"privacy-base's PurgeIdentityDedupFootprint on erasure. Makes merge repoint and erasure sweep decrypt-free — linkage is ownership, so " +
				"no plaintext lookup is needed. " +
				"permittedCommands is intentionally empty: multi-writer, open posture (mirrors the " +
				"identity-anchored aspect DDLs above).",
			Script:       linkTypeDDLScript,
			InputSchema:  `{"type":"object"}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"link": "No payload fields — this DDL declares the indexes link class/direction only; it is never an operation handler.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "indexes link",
					Payload:         map[string]any{},
					ExpectedOutcome: "lnk.identityindex.<hash>.indexes.identity.<NanoID>, data {}; owner of the index vertex's identityKey.",
				},
			},
		},
		{
			CanonicalName: "duplicateOf",
			Class:         "meta.ddl.linkType",
			Description: "identity duplicateOf identity. Durable pair evidence " +
				"(lnk.identity.<newId>.duplicateOf.identity.<existingId>) recorded by CreateUnclaimedIdentity " +
				"when a new identity's normalized email/phone/name collides with a live identityindex hit; " +
				"the later-arriving identity is the source. data.criteria unions the matched dimensions " +
				"(exact-email/exact-phone/exact-name). Tombstoned (both directions) by identity-hygiene's " +
				"MergeIdentity on merge, and by privacy-base's PurgeIdentityDedupFootprint on erasure. " +
				"permittedCommands is intentionally empty: multi-writer, open posture.",
			Script:       linkTypeDDLScript,
			InputSchema:  `{"type":"object","properties":{"criteria":{"type":"array","items":{"type":"string"},"description":"Matched dimensions: exact-email, exact-phone, exact-name."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"criteria": "Which normalized dimensions matched the incumbent identity.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "duplicateOf link",
					Payload:         map[string]any{"criteria": []any{"exact-email"}},
					ExpectedOutcome: "lnk.identity.<newId>.duplicateOf.identity.<existingId>, data {criteria: [\"exact-email\"]}.",
				},
			},
		},
		{
			CanonicalName: "boundTo",
			Class:         "meta.ddl.linkType",
			Description: "identity boundTo identity. The credential-to-owner edge " +
				"(lnk.identity.<credentialId>.boundTo.identity.<ownerId>): the identity the Gateway " +
				"provisioned for a raw sign-in, bound to the business identity it proved control of. " +
				"The later-arriving credential is the source. Emitted by ClaimIdentity (first credential) " +
				"and CompleteCredentialLink (Nth); tombstoned by UnlinkCredential, and by " +
				"UnbindIdentityCredentials in both directions on erasure — it names in plaintext which " +
				"credential belonged to an erased person, so it must not outlive the key; repointed to the " +
				"primary by identity-hygiene's MergeIdentity. Carries the same {actorKey, boundAt} pair " +
				"the credentialBinding aspect's array entry does — as a link, so the set is projectable " +
				"one row per credential without decrypting a sensitive aspect (Contract #1: a " +
				"relationship is a link, not a ref inside data). data.boundAt is provenance only: no lens " +
				"can read it until the engine binds a relationship variable. permittedCommands is " +
				"intentionally empty: multi-writer, open posture (mirrors indexes/duplicateOf above).",
			Script:       linkTypeDDLScript,
			InputSchema:  `{"type":"object","properties":{"boundAt":{"type":"string","description":"RFC3339 instant the credential was bound, mirroring the credentialBinding array entry."}}}`,
			OutputSchema: `{"type":"object"}`,
			FieldDescription: map[string]string{
				"boundAt": "When this credential was bound to this identity — the same instant the credentialBinding entry and the credentialindex vertex carry.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:            "boundTo link",
					Payload:         map[string]any{"boundAt": "2026-08-03T12:00:00Z"},
					ExpectedOutcome: "lnk.identity.<credentialId>.boundTo.identity.<ownerId>, data {boundAt}; one per live entry in the owner's credentials array.",
				},
			},
		},
		RevocationDDL(),
		ActorRevokedEventDDL(),
		ActorUnrevokedEventDDL(),
		UnbindIdentityCredentialsDDL(),
		TombstoneOrphanedCredentialIndexDDL(),
	}
}

// sensitiveAspectDDLScript is the declaration-only Starlark shared by every
// sensitive aspect-type DDL in this package (ssn, dob, name, email, phone,
// claimKey, credentialBinding). An aspect-type DDL declares a sensitive
// aspect's shape and anchoring; it is not an operation handler (the identity
// DDL's operations write the aspects). No operation carries an aspect class as
// its operation class, so execute is never dispatched here — it fails closed if
// it ever is.
const sensitiveAspectDDLScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

// linkTypeDDLScript is the declaration-only Starlark shared by this
// package's link-type DDLs (indexes, duplicateOf, boundTo). A link-type DDL declares
// a link class's shape and direction; it is not an operation handler (the
// identity/identity-hygiene DDLs' operations create/tombstone the links).
// No operation carries a link class as its operation class, so execute is
// never dispatched here — it fails closed if it ever is.
const linkTypeDDLScript = `
def execute(state, op):
    fail("link-type DDL: not an operation handler: " + op.operationType)
`

// identityDDLScript is the identity DDL Starlark script. State machine:
// unclaimed -> claimed. The merged state is set only by the
// identity-hygiene package's MergeIdentity script.
// identityDDLScript is derived from identityDDLScriptTemplate by pinning
// every occurrence of the placeholder — the package's own consumer role key
// — to its real, deterministic value (see consumerRoleKey above): both
// ProvisionConsumerIdentity (enforcing its role grant by equality against a
// caller-supplied field) and ClaimIdentity (granting the same role
// unconditionally, no caller input involved) reference the one literal.
var identityDDLScript = strings.ReplaceAll(identityDDLScriptTemplate, "__EXPECTED_CONSUMER_ROLE_KEY__", consumerRoleKey)

const identityDDLScriptTemplate = `
def make_update(key, data):
    return {"op": "update", "key": key, "document": {"isDeleted": False, "data": data}}

def index_vertex_mutation(index_key, contact_type, identity_key, existing):
    # dedup-over-encrypted-pii-design.md §3.5: an erased identity's owned
    # identityindex vertices are tombstoned by the erasure's dedup sweep
    # (privacy-base/PurgeIdentityDedupFootprint), so a later create for
    # the SAME contact must be able to re-derive a live index -- a blind
    # "create" collides with the tombstone's own write history (CreateOnly
    # asserts revision 0, which a previously-written key can never satisfy
    # again). Mirrors orchestration-base's make_vtx_revive_occ /
    # loftspace-domain's make_link_revive_occ precedent: a present-but-
    # tombstoned index revives via a CAS-guarded update; a truly absent one
    # still gets a plain create.
    doc = {"class": "identityindex", "isDeleted": False,
           "data": {"contactType": contact_type, "identityKey": identity_key}}
    if existing != None:
        return {"op": "update", "key": index_key, "document": doc, "expectedRevision": existing.revision}
    return {"op": "create", "key": index_key, "document": doc}

def stale_indexes_tombstone(index_key, hit):
    # Tombstones the ownership edge from an identityindex vertex to the
    # identity it currently names, so a repoint of that vertex does not leave
    # two live indexes out-links. The old link key is a deterministic
    # derivation off the hit's own body -- no enumeration, no extra read.
    #
    # Deliberately unconditioned (no expectedRevision), mirroring
    # identity-hygiene's MergeIdentity idx_repoints loop. Within THIS op that is
    # airtight: the tombstone never lands alone, it is always in the same atomic
    # batch as index_vertex_mutation's CAS-guarded repoint of the very vertex it
    # hangs off, so anything that races the repoint invalidates the whole batch.
    #
    # It says nothing, though, about a DIFFERENT op racing the same vertex, and
    # two of them do. privacy-base's PurgeIdentityDedupFootprint enumerates a
    # sealed subject's indexes links and tombstones their source vertices, and
    # MergeIdentity repoints those vertices to a merge primary; neither carries
    # a revision or a content assertion. Before this repoint existed neither
    # could lose that race, because nothing else ever wrote a LIVE index vertex
    # out from under them. Both now gate on the vertex still naming the identity
    # they believe owns it (privacy-base's index_owned_by, identity-hygiene's
    # idx_names_owner), each pinned to the revision that check read: the content
    # gate answers whether to write at all -- a question no CAS can answer --
    # and the pin makes the answer atomic with the batch that acts on it.
    old_identity_id = hit.data["identityKey"][len("vtx.identity."):]
    return {"op": "update",
            "key": "lnk." + index_key[len("vtx."):] + ".indexes.identity." + old_identity_id,
            "document": {"class": "indexes", "isDeleted": True, "data": {}}}

def credential_index_mutation(cred_index_key, existing, actor_key, identity_key, bound_at):
    # multi-credential-identity-linking-design.md §8: UnlinkCredential is the
    # first path that ever tombstones a credentialindex vertex, so a later
    # (re)bind of the SAME actor -- to this identity or a different one --
    # must be able to re-derive a live index. Same revive-on-CAS idiom as
    # index_vertex_mutation above: a present-but-tombstoned index revives via
    # a CAS-guarded update (declared optionalReads gives existing.revision);
    # a truly absent (or undeclared -- the live-conflict security backstop is
    # unchanged) one still gets a plain CreateOnly create.
    doc = {"class": "credentialindex", "isDeleted": False,
           "data": {"actorKey": actor_key, "identityKey": identity_key, "boundAt": bound_at}}
    if existing != None:
        return {"op": "update", "key": cred_index_key, "document": doc, "expectedRevision": existing.revision}
    return {"op": "create", "key": cred_index_key, "document": doc}

def read_state(state, identity_key):
    aspect_key = identity_key + ".state"
    if aspect_key in state:
        doc = state[aspect_key]
        if doc.data != None and "value" in doc.data:
            return doc.data["value"]
    return None

def read_merged_into(state, identity_key):
    aspect_key = identity_key + ".mergedInto"
    if aspect_key in state:
        doc = state[aspect_key]
        if doc.data != None and "value" in doc.data:
            return doc.data["value"]
    return None

def enforce_not_merged(current_state, merged_into):
    if current_state == "merged":
        fail("IdentityMerged: mergedInto=" + (merged_into if merged_into != None else "<unknown>"))

def write_path_closed(identity_key):
    # The erasure write-path gate (erasure-orchestration-design.md §6). True
    # when EITHER this person invoked a right to be forgotten -- the PRESENCE
    # of vtx.identity.<NanoID>.erasureRequested -- OR their PII key has already
    # been destroyed. From either instant no writer may create a fresh erasable
    # representation of them, otherwise the residue count the erasure's
    # completion seal rests on measures an OPEN set, and "zero" degrades from
    # "erased" to "zero at projection time".
    #
    # Read through kv.Read rather than state[...], and that difference IS the
    # guard. A state[...] lookup of a key no dispatcher declared reads as
    # ABSENT, so one missing declaration would silently open the gate -- the
    # one failure mode a fail-closed guard may not have. An undeclared kv.Read
    # falls through to a live Core KV GET instead (internal/processor/
    # starlark_kv.go), so the gate refuses either way; declaring the key only
    # buys it the step-4 snapshot with no round trip.
    #
    # read-posture: (d) declared in optionalReads by this package's own
    # derive_reads (Contract #2 §2.5 class (g)). Absence-tolerant, and absence
    # is the ordinary case -- no identity carries this marker until it is
    # sealed for erasure.
    if marker_closes_write_path(kv.Read(identity_key + ".erasureRequested")):
        return True
    # The SECOND condition, and the marker is checked first because it is the
    # one derive_reads hydrates -- a sealed subject costs no round trip here.
    #
    # A destroyed PII key closes the write path on its own, with no marker
    # beside it. The marker is written by the erasure PATTERN's seal, so it is
    # present for every subject erased through the pattern; a bare
    # ShredIdentityKey submit writes only piiKey.shredded, and those subjects
    # exist: the operator Shred button submits the op directly. Gating on the
    # marker alone leaves
    # each of them reading as a live, unerased dedup incumbent: an ordinary
    # same-contact walk-in mints a fresh duplicateOf naming a person whose key
    # is already destroyed, which is a NEW decrypt-free correlation to someone
    # already erased -- the exact growth of the erased set §6 exists to forbid,
    # arriving through the path that has always been the common one.
    #
    # It is also the weaker fact deliberately, not accidentally: shredded means
    # the key is gone, so no writer can produce a decryptable representation of
    # this person again either way. Closing on it costs a recoverable refusal
    # for a subject nobody is actively converging; leaving it open costs a
    # durable correlation nothing can walk back.
    #
    # read-posture: (d) declared in optionalReads by derive_reads alongside the
    # marker, absence-tolerant for the same reason -- no identity carries a
    # piiKey until its first sensitive write.
    return key_shredded_closes_write_path(kv.Read(identity_key + ".piiKey"))

def key_shredded_closes_write_path(doc):
    # The piiKey half of the gate, shared for the same anti-drift reason as
    # marker_closes_write_path below, and checking the class for the same
    # reason: this key's aspect-type DDL is privacy-base's, so a document
    # declaring some other class at the same key falls to resolveGoverningDDL's
    # permissive default. Only a real piiKey envelope may shut the path.
    #
    # Tombstone-tolerant, again like the marker: the envelope is the record
    # that a key was destroyed, and destruction does not become untrue when the
    # aspect is deleted.
    if doc == None:
        return False
    if not hasattr(doc, "class") or getattr(doc, "class") != "piiKey":
        return False
    if doc.data == None:
        return False
    return doc.data.get("shredded", False) == True

def marker_closes_write_path(doc):
    # Shared by every gate in this package so the four of them cannot drift.
    #
    # The CLASS is checked, not merely the key. privacy-base records that its
    # aspect-type DDL gates the class rather than the key
    # (seal_identity_for_erasure.go), so a mutation at this key declaring some
    # OTHER class falls to resolveGoverningDDL's permissive default and any
    # package script could write one. Without this check such a document would
    # close a person's claim, link, reconcile and merge paths permanently --
    # nothing removes the marker, so there would be no way back. Requiring the
    # real class means only the real seal can shut the path.
    #
    # "class" is a Starlark reserved word, so it cannot be read via dotted
    # attribute access -- getattr with the string key is required (the same
    # idiom location-domain's root_class uses).
    if doc == None:
        return False
    if not hasattr(doc, "class") or getattr(doc, "class") != "erasureRequested":
        return False
    # A tombstoned document reads as PRESENT carrying the flag, not as None,
    # and it still closes. Nothing removes this marker -- its non-removal is
    # the convention §7's convergence rests on -- but a gate that reopened on a
    # tombstone would let the erased set grow again exactly when that was least
    # observable. Presence of the right class is the signal, live or not.
    #
    # This is deliberately WIDER than §7.1's residue anchor, which projects on
    # a live .erasureRequested.data.requestedAt that is not null. A tombstoned or
    # body-less marker therefore shuts the write path while producing no
    # residue row -- an identity with nothing to converge and no operator
    # surface. Both states require a writer that does not exist (this class has
    # exactly one, and it always writes both fields), and of the two ways to be
    # wrong, refusing writes for a person nobody is erasing is recoverable
    # while growing the erased set is the thing §6 exists to prevent. Recorded
    # in the design's inc-2 build note rather than papered over.
    return True

ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    # Byte-identical in spirit to the F4 workplace guard's own
    # actor_holds_operator (cafe-domain/clinic-domain/lease-signing/
    # wellness-domain/maintenance-domain) -- this package has no location to
    # pair it with, so only the operator-exemption half is needed here.
    #
    # Paginated: a role beyond page 1 must not read as "not held" -- the walk
    # follows the cursor up to MAX_ROLE_PAGES pages before giving up, and
    # giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan. A role granted concurrently with
        # this write is not a race worth closing: it can only widen authority, and
        # the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def require_live_role(role_key):
    # Called only on a path that is about to grant the role, so a caller that
    # needs no grant never depends on the role vertex.
    # read-posture: (a) declared in contextHint.reads by the Gateway's
    # provisionActorIfNeeded dispatcher (internal/gateway/gateway.go) — a
    # pinned, always-live role vertex; absence is a wiring fault.
    role_vtx = kv.Read(role_key)
    if role_vtx == None or role_vtx.isDeleted:
        fail("UnknownRole: " + role_key)

def validate_state_transition(current, new):
    if current == None:
        fail("InvalidStateTransition: <missing> -> " + str(new))
    allowed = {
        "unclaimed": ["claimed"],
    }
    targets = allowed.get(current)
    if targets == None or new not in targets:
        fail("InvalidStateTransition: " + str(current) + " -> " + str(new))

# -- Contact normalization + index-key derivation ------------------------------
#
# These five helpers are the package's own semantics for turning a submitted
# contact into an index key, and they are the SINGLE definition of it: both
# derive_reads (below) and execute call them. That is the whole point -- the
# derived declared read (Contract #2 §2.5 class (g)) is only worth having if it
# names the key the operation will actually probe, and two hand-kept copies of a
# normalization are exactly the divergence this mechanism exists to end. A
# change here moves both passes or neither.
#
# Each normalizer returns None for input it cannot normalize, and NEVER fails:
# derive_reads runs before the operation's own validation, so failing here would
# turn a clean InvalidArgument into an opaque hydration fault.

#
# MAX_CONTACT_INPUT bounds the work every normalizer does on a caller-supplied
# string. derive_reads runs BEFORE execute's validation, so without it a ~1 MB
# name (the Gateway body cap is 1 MiB) buys a lower()+split()+join() over half a
# million tokens -- none of which the sandbox's step ceiling counts, since each
# is a single builtin call -- on a payload execute is about to reject anyway.
#
# The cap is deliberately NOT enforced only here. A normalizer that silently
# returned None over the limit while execute still ACCEPTED the value would
# resurrect precisely the divergence this mechanism exists to end: 300 leading
# spaces plus "Ada" is a 303-character raw input and a 3-character name, so
# execute would accept it while the derived probe went missing. execute
# therefore rejects the same inputs, on the RAW value, before it strips (see
# require_contact_input). The two passes agree by construction, and the bound
# sits far above any real name, email, or phone number.
MAX_CONTACT_INPUT = 4096

def within_contact_limit(raw):
    return type(raw) == type("") and len(raw) <= MAX_CONTACT_INPUT

def require_contact_input(field, raw):
    # execute's half of the MAX_CONTACT_INPUT bound. Only a PRESENT string over
    # the limit is a caller error; absent or non-string is left to each branch's
    # own required/optional rules, which report better messages for it.
    if type(raw) == type("") and len(raw) > MAX_CONTACT_INPUT:
        fail("InvalidArgument: " + field + ": exceeds " + str(MAX_CONTACT_INPUT) + " characters")

def normalize_name(raw):
    if not within_contact_limit(raw):
        return None
    normalized = " ".join(raw.lower().split())
    if len(normalized) == 0:
        return None
    return normalized

def normalize_email(raw):
    if not within_contact_limit(raw):
        return None
    e = raw.strip().lower()
    if len(e) == 0:
        return None
    return e

def normalize_phone(raw):
    # E.164-ish: digits and a plus survive, everything else is punctuation the
    # submitter's formatting added.
    if not within_contact_limit(raw):
        return None
    stripped = ""
    for ch in raw.elems():
        if ch >= "0" and ch <= "9":
            stripped += ch
        elif ch == "+":
            stripped += ch
    if len(stripped) == 0:
        return None
    return stripped

def identity_index_key(contact_type, normalized):
    return "vtx.identityindex." + crypto.sha256NanoID(contact_type + ":" + normalized)

def credential_index_key(actor_key):
    return "vtx.credentialindex." + crypto.sha256NanoID(actor_key)

def vertex_alive(state, key):
    # A hydrated key that is present, non-nil and not tombstoned. Same shape as
    # unbind_identity_credentials.go's helper of this name, so the two scripts
    # answer "does this vertex exist" identically. Absent and tombstoned are
    # deliberately one answer: a revoked credential must not claim either.
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def identity_id(identity_key):
    return identity_key[len("vtx.identity."):]

def credential_bound_to_key(credential_actor_key, owner_identity_key):
    # Contract #1 §1.1: the later-arriving vertex is the source, so the
    # credential is the source and the identity it binds to is the target --
    # "credential boundTo identity" reads as the sentence it is. Both
    # endpoints are identity vertices: a credential is the identity the
    # Gateway provisioned for the raw sign-in, distinct from the business
    # identity it proves control of.
    return ("lnk.identity." + identity_id(credential_actor_key) +
            ".boundTo.identity." + identity_id(owner_identity_key))

def credential_bound_to_mutation(credential_actor_key, owner_identity_key, bound_at):
    # An update, never a create: UnlinkCredential tombstones this link, and a
    # later re-bind of the SAME credential to the SAME identity must revive it.
    # A create asserts revision 0 and would take the whole atomic batch down
    # with a RevisionConflict on exactly that path. Same revive posture as
    # credential_index_mutation; the key is declared in derive_reads, so
    # step 8 conditions the write on the revision it was read at when present
    # and commits unconditioned when it is genuinely absent.
    doc = {"class": "boundTo", "isDeleted": False,
           "sourceVertex": credential_actor_key, "targetVertex": owner_identity_key,
           "localName": "boundTo", "data": {"boundAt": bound_at}}
    return {"op": "update", "key": credential_bound_to_key(credential_actor_key, owner_identity_key),
            "document": doc}

# Fixed-length stand-ins the claim and credential-link branches hash and
# compare against when the real operands are absent, so both crypto calls run
# on every rejection cause (nfr-s6-release-quantum-payload-design.md §4.1).
# Each is 64 characters for its OWN reason, and the two reasons are different.
#
# PLACEHOLDER_SECRET is a crypto.sha256 INPUT and never a compare operand. A
# real claim or link secret is 32 random bytes hex-encoded -- 64 characters
# (Contract #9 §9.4, cmd/lattice/identity/identity.go) -- so a digest of the
# stand-in costs exactly what a digest of a real secret costs.
#
# PLACEHOLDER_STORED_HASH is a compare operand, and its 64 characters are what
# make the comparison run at all: crypto.constant_time_equal returns on a
# length mismatch before comparing, which would put the absence of a stored
# hash straight back into the timing it is being taken out of.
#
# An identity armed with sha256(PLACEHOLDER_SECRET), claimed with no secret at
# all, DOES pass constant_time_equal -- the stand-in digest equals the stored
# hash. What refuses it is the accumulate shape rather than the constant: the
# payload check sits ABOVE the compare, has already recorded "invalid-key",
# and first_outcome keeps the first word whatever the compare then decides.
# Moving that check below the compare would open exactly this hole.
PLACEHOLDER_SECRET = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
PLACEHOLDER_STORED_HASH = "0000000000000000000000000000000000000000000000000000000000000000"

def first_outcome(recorded, failed, word):
    # The accumulate-then-fail-once shape the claim and credential-link
    # branches require: every check is evaluated whatever the ones before it
    # decided, and the FIRST one that fails supplies the outcome word Health KV
    # records. A later failure is dropped, so the word is the one the check
    # order ranks highest -- and no cause exits before another, which is what
    # keeps the causes indistinguishable in the time domain
    # (nfr-s6-release-quantum-payload-design.md §4.1).
    if recorded != None:
        return recorded
    if failed:
        return word
    return None

NANOID_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

def is_identity_vertex_key(key):
    # True only for a key the Contract #1 grammar accepts as
    # vtx.identity.<NanoID>, i.e. exactly what substrate's ClassifyKey will
    # accept once ".erasureRequested" is appended to it.
    #
    # This is not decoration. The Processor validates every key derive_reads
    # returns against that grammar and answers a malformed one with
    # DeriveReadsInvalid -- a hydration fault, raised BEFORE the operation's
    # own validation runs. Deriving from an unvalidated payload would therefore
    # convert a clean "ClaimKeyInvalid: no-target" into an opaque
    # HydrationFailed, and on an NFR-S6-protected path that is a new,
    # distinguishable wire code any caller can reach with "vtx.identity.x".
    # The same rule the contact normalizers above already obey: a derivation
    # never fails, and never widens what the operation itself rejects.
    if type(key) != type("") or not key.startswith("vtx.identity."):
        return False
    parts = key.split(".")
    if len(parts) != 3 or len(parts[2]) != 20:
        return False
    for ch in parts[2].elems():
        if ch not in NANOID_ALPHABET:
            return False
    return True

def erasure_gate_keys(identity_keys):
    # BOTH keys the §6 gate reads for a set of identity positions -- the
    # erasure marker and the piiKey envelope -- skipping any position that is
    # absent or not a well-formed identity vertex key. Deduped: the Processor
    # tolerates a repeated key, but a self-claim would otherwise derive the same
    # pair twice and spend four of the declared-read budget on two facts.
    keys = []
    for k in identity_keys:
        if is_identity_vertex_key(k):
            for suffix in [".erasureRequested", ".piiKey"]:
                gate_key = k + suffix
                if gate_key not in keys:
                    keys.append(gate_key)
    return keys

def derive_reads(op):
    # Contract #2 §2.5 class (g). The Processor runs this at the head of step 4
    # and merges what it returns into the declared read set, so a submitter no
    # longer has to reproduce sha256NanoID and this package's contact
    # normalization in its own language to get the dedup probes hydrated.
    #
    # Every key here is optionalReads and must stay that way: each one is a
    # read-BEFORE-create probe whose absence is the ordinary case (a first-time
    # contact, an unbound credential). Declaring one under "reads" would fault
    # HydrationMiss on precisely the branch the probe exists to take.
    #
    # The op argument is a struct -- op.operationType, op.actor, op.payload
    # (also a struct). No kv, no nanoid: both are bound to fail-closed stubs in this
    # pass, and a derivation that reads state is a read, not a derivation.
    ot = op.operationType
    p = op.payload

    if ot == "CreateUnclaimedIdentity":
        keys = []
        email = normalize_email(getattr(p, "email", None))
        if email != None:
            keys.append(identity_index_key("email", email))
        phone = normalize_phone(getattr(p, "phone", None))
        if phone != None:
            keys.append(identity_index_key("phone", phone))
        name = normalize_name(getattr(p, "name", None))
        if name != None:
            keys.append(identity_index_key("name", name))
        if len(keys) == 0:
            return {}
        return {"optionalReads": keys}

    if ot == "ClaimIdentity" or ot == "CompleteCredentialLink":
        # The one-credential-<=-one-identity guard reads the index for the
        # SUBMITTING credential, so this key derives from the actor, not the
        # payload.
        keys = [credential_index_key(op.actor)]
        target = getattr(p, "targetIdentityKey", None)
        # The two keys the §6 gate reads -- the erasure marker and the piiKey
        # envelope -- for BOTH ends of the boundTo this op emits, since
        # UnbindIdentityCredentials erases that link in both directions on
        # erasure (identity-domain/unbind_identity_credentials.go), so both
        # positions are gated.
        #
        # Derived here rather than asked of every submitter for the reason
        # class (g) exists: three separate front ends dispatch these two ops,
        # and a gate whose correctness depended on each of them remembering a
        # declaration would be a gate with three ways to be forgotten. (It
        # still refuses if this derivation is ever missed -- the gate reads
        # through kv.Read, which falls through to a live GET -- but then it
        # costs a round trip per claim rather than a snapshot lookup.)
        keys += erasure_gate_keys([target, op.actor])
        # The credential actor's OWN vertex. The emitted boundTo link is the
        # sole input to a projection presented as a person's COMPLETE list of
        # sign-in methods, so Contract #3 §3.5's duty applies: a script whose
        # link must guarantee its endpoints declares them and validates them.
        # For that reader an absent endpoint is a silent under-report, not a
        # null -- identityCredentialBindingsRead anchors on (c:identity),
        # so a credential with no vertex projects no row at all while the
        # sibling credentials lens still lists it.
        #
        # optionalReads, never reads: absence is the very condition the guard
        # below rejects with a named outcome, and a reads declaration would
        # fault HydrationMiss first -- an opaque wiring error where the caller
        # should see the ceremony's own generic refusal.
        if is_identity_vertex_key(op.actor):
            keys.append(op.actor)
        # Both endpoints must be WELL-FORMED, not merely prefixed. A prefix
        # check accepts "vtx.identity.x", from which this builds a link key the
        # Contract #1 grammar rejects -- and the Processor answers a malformed
        # derived key with DeriveReadsInvalid, a hydration fault raised before
        # the script's own validation. On this NFR-S6-protected path that is a
        # distinguishable wire code where the caller should have seen the
        # generic ClaimKeyInvalid. For a well-formed payload nothing changes.
        if is_identity_vertex_key(target) and is_identity_vertex_key(op.actor):
            # The boundTo link this op emits. Optional, and absent on every
            # first bind -- present only when the same credential was unlinked
            # from this same identity earlier and is being re-bound, which is
            # the one case whose write must CAS rather than blind-Put.
            keys.append(credential_bound_to_key(op.actor, target))
            if ot == "ClaimIdentity":
                # The consumer holdsRole grant ClaimIdentity upserts. A
                # deterministic link key built from the payload target and the
                # package's own pinned role literal -- derivable here for
                # exactly the reason it was left underived before: no browser
                # client can compute it, but the package can.
                keys.append("lnk.identity." + identity_id(target) +
                            ".holdsRole.role." + "__EXPECTED_CONSUMER_ROLE_KEY__"[len("vtx.role."):])
        return {"optionalReads": keys}

    if ot == "UnlinkCredential":
        # The index and the link this op tombstones. Both derive from the
        # payload's credentialActorKey under package semantics, and both are
        # optional: a caller naming a credential that is not bound takes the
        # not-found branch, which must not fault on a hydration miss.
        credential_actor_key = getattr(p, "credentialActorKey", None)
        if not is_identity_vertex_key(credential_actor_key):
            return {}
        keys = [credential_index_key(credential_actor_key)]
        if is_identity_vertex_key(op.actor):
            keys.append(credential_bound_to_key(credential_actor_key, op.actor))
        return {"optionalReads": keys}

    if ot == "ReconcileCredentialBinding":
        # The index vertex execute reads as its authority, and the link it
        # converges. The owner comes from the PAYLOAD rather than the actor:
        # this op is submitted by an operator on someone else's behalf, so
        # op.actor names the operator, not the person the edge belongs to.
        # That is exactly why execute re-derives nothing from the payload it
        # has not first checked against the index.
        credential_actor_key = getattr(p, "credentialActorKey", None)
        identity_key = getattr(p, "identityKey", None)
        if not is_identity_vertex_key(credential_actor_key):
            return {}
        keys = [credential_index_key(credential_actor_key), credential_actor_key]
        if is_identity_vertex_key(identity_key):
            keys.append(credential_bound_to_key(credential_actor_key, identity_key))
        # The two keys the §6 gate reads, both ends. This op's only dispatcher
        # declares no contextHint at all, so class (g) is the only place they
        # can come from.
        keys += erasure_gate_keys([identity_key, credential_actor_key])
        return {"optionalReads": keys}

    return {}

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "UpdateIdentityState":
        identity_key = p.identityKey
        new_state = p.newState
        current = read_state(state, identity_key)
        merged_into = read_merged_into(state, identity_key)
        enforce_not_merged(current, merged_into)
        validate_state_transition(current, new_state)
        mutations = [make_update(identity_key + ".state", {"value": new_state})]
        events = [{"class": "identity.stateChanged", "data": {
            "identityKey": identity_key,
            "oldState": current,
            "newState": new_state,
        }}]
        return {"mutations": mutations, "events": events}

    if ot == "CreateUnclaimedIdentity":
        # The RAW-input bound runs first, on the same values derive_reads saw,
        # so no payload this branch accepts can be one the normalizers declined
        # to normalize (MAX_CONTACT_INPUT above).
        require_contact_input("name", getattr(p, "name", None))
        require_contact_input("email", getattr(p, "email", None))
        require_contact_input("phone", getattr(p, "phone", None))

        name = p.name if hasattr(p, "name") else None
        if name == None or type(name) != type("") or len(name.strip()) == 0:
            fail("InvalidArgument: name: required, maxLen 200")
        name = name.strip()
        if len(name) > 200:
            fail("InvalidArgument: name: required, maxLen 200")

        email = normalize_email(getattr(p, "email", None))
        phone = normalize_phone(getattr(p, "phone", None))

        if email == None and phone == None:
            fail("InvalidArgument: email or phone: at least one required")

        claim_key_hash = p.claimKeyHash if hasattr(p, "claimKeyHash") else None
        if claim_key_hash == None or type(claim_key_hash) != type("") or len(claim_key_hash) == 0:
            fail("InvalidArgument: claimKeyHash: required non-empty lowercase hex sha256")
        if len(claim_key_hash) != 64:
            fail("InvalidArgument: claimKeyHash: must be 64-char lowercase hex sha256")
        for ch in claim_key_hash.elems():
            if not ((ch >= "0" and ch <= "9") or (ch >= "a" and ch <= "f")):
                fail("InvalidArgument: claimKeyHash: must be lowercase hex")
        claim_key_algo = p.claimKeyAlgo if hasattr(p, "claimKeyAlgo") else None
        if claim_key_algo == None or claim_key_algo == "":
            claim_key_algo = "sha256"
        if claim_key_algo != "sha256":
            fail("InvalidArgument: claimKeyAlgo: only sha256 is supported")

        name_index_key = identity_index_key("name", normalize_name(name))

        # The erasure gate on the dedup path (§6). A live index hit is what
        # turns a new registration into a duplicateOf link naming the
        # incumbent, in a link key plus its match criteria in plaintext. If
        # that incumbent has been sealed for erasure, the link is a BRAND-NEW
        # correlation to a person who asked to be forgotten, created after the
        # seal -- the erased set growing, which is the one property §6 exists
        # to forbid. It needs no exotic path: the name index always matches, so
        # an ordinary same-named walk-in during the convergence window does it.
        #
        # Skipping the match, rather than refusing the whole op, is the right
        # shape. The incumbent is not the caller's business and the person in
        # front of the front desk is entitled to be registered; what they are
        # not entitled to is a recorded correlation with someone being erased.
        # A skipped match reads exactly as "no duplicate", which is what the
        # corpus will say about that contact once the erasure converges.
        def match_is_erased(hit):
            # read-posture: (e) bounded per-candidate follow-up off the three
            # contact-index probes derive_reads declares -- at most TWO reads
            # per contact type (the marker, then the piiKey only if the marker
            # is absent), six in the worst case, never a scan. The incumbent's
            # key is only knowable from the hit's own body, so neither can be
            # declared ahead of hydration the way the index keys are.
            #
            # Both conditions, via the same helper the claim/link/reconcile
            # gates use: the dedup path is where a bare-shredded incumbent does
            # its damage -- it is the one gate whose refusal PREVENTS a new
            # correlation rather than merely refusing to extend an old one.
            return write_path_closed(hit.data["identityKey"])

        def live_hit(hit):
            return hit != None and (not hasattr(hit, "isDeleted") or not hit.isDeleted)

        # The erased flags are computed ONCE per contact type and reused by the
        # mutation-build block below. match_is_erased does live, undeclared
        # kv.Reads, so a second call on the same hit would double this path's
        # round trips for an answer it already has. Each is evaluated only
        # behind live_hit, so a miss costs nothing.
        def hit_is_erased(hit):
            return live_hit(hit) and match_is_erased(hit)

        duplicate = False
        matched = {}
        email_erased = False
        phone_erased = False
        if email != None:
            email_index_key = identity_index_key("email", email)
            email_hit = state[email_index_key] if email_index_key in state else None
            email_erased = hit_is_erased(email_hit)
            if live_hit(email_hit) and not email_erased:
                duplicate = True
                incumbent_key = email_hit.data["identityKey"]
                matched[incumbent_key] = (matched[incumbent_key] if incumbent_key in matched else []) + ["exact-email"]
        if phone != None:
            phone_index_key = identity_index_key("phone", phone)
            phone_hit = state[phone_index_key] if phone_index_key in state else None
            phone_erased = hit_is_erased(phone_hit)
            if live_hit(phone_hit) and not phone_erased:
                duplicate = True
                incumbent_key = phone_hit.data["identityKey"]
                matched[incumbent_key] = (matched[incumbent_key] if incumbent_key in matched else []) + ["exact-phone"]
        name_hit = state[name_index_key] if name_index_key in state else None
        name_erased = hit_is_erased(name_hit)
        if live_hit(name_hit) and not name_erased:
            duplicate = True
            incumbent_key = name_hit.data["identityKey"]
            matched[incumbent_key] = (matched[incumbent_key] if incumbent_key in matched else []) + ["exact-name"]

        identity_id = nanoid.new()
        identity_key = "vtx.identity." + identity_id

        initial_state = "unclaimed"

        mutations = [
            {"op": "create", "key": identity_key,
             "document": {"class": "identity", "isDeleted": False, "data": {}}},
            {"op": "create", "key": identity_key + ".name",
             "document": {"class": "name", "vertexKey": identity_key, "localName": "name",
                          "isDeleted": False, "data": {"value": name}}},
            {"op": "create", "key": identity_key + ".state",
             "document": {"class": "state", "vertexKey": identity_key, "localName": "state",
                          "isDeleted": False, "data": {"value": initial_state}}},
            {"op": "create", "key": identity_key + ".claimKey",
             "document": {"class": "claimKey", "vertexKey": identity_key, "localName": "claimKey",
                          "isDeleted": False, "data": {"hash": claim_key_hash, "algo": claim_key_algo}}},
        ]
        if email != None:
            mutations.append({"op": "create", "key": identity_key + ".email",
                "document": {"class": "email", "vertexKey": identity_key, "localName": "email",
                             "isDeleted": False, "data": {"value": email}}})
            if email_hit == None or (hasattr(email_hit, "isDeleted") and email_hit.isDeleted) or email_erased:
                if email_erased:
                    mutations.append(stale_indexes_tombstone(email_index_key, email_hit))
                mutations.append(index_vertex_mutation(email_index_key, "email", identity_key, email_hit))
                mutations.append({"op": "create", "key": "lnk." + email_index_key[len("vtx."):] + ".indexes.identity." + identity_id,
                    "document": {"class": "indexes", "isDeleted": False,
                                 "sourceVertex": email_index_key, "targetVertex": identity_key,
                                 "localName": "indexes", "data": {}}})
        if phone != None:
            mutations.append({"op": "create", "key": identity_key + ".phone",
                "document": {"class": "phone", "vertexKey": identity_key, "localName": "phone",
                             "isDeleted": False, "data": {"value": phone}}})
            if phone_hit == None or (hasattr(phone_hit, "isDeleted") and phone_hit.isDeleted) or phone_erased:
                if phone_erased:
                    mutations.append(stale_indexes_tombstone(phone_index_key, phone_hit))
                mutations.append(index_vertex_mutation(phone_index_key, "phone", identity_key, phone_hit))
                mutations.append({"op": "create", "key": "lnk." + phone_index_key[len("vtx."):] + ".indexes.identity." + identity_id,
                    "document": {"class": "indexes", "isDeleted": False,
                                 "sourceVertex": phone_index_key, "targetVertex": identity_key,
                                 "localName": "indexes", "data": {}}})
        if name_hit == None or (hasattr(name_hit, "isDeleted") and name_hit.isDeleted) or name_erased:
            if name_erased:
                mutations.append(stale_indexes_tombstone(name_index_key, name_hit))
            mutations.append(index_vertex_mutation(name_index_key, "name", identity_key, name_hit))
            mutations.append({"op": "create", "key": "lnk." + name_index_key[len("vtx."):] + ".indexes.identity." + identity_id,
                "document": {"class": "indexes", "isDeleted": False,
                             "sourceVertex": name_index_key, "targetVertex": identity_key,
                             "localName": "indexes", "data": {}}})

        matched_identity_keys = []
        for incumbent_key in matched:
            matched_identity_keys.append(incumbent_key)
            mutations.append({"op": "create",
                "key": "lnk." + identity_key[len("vtx."):] + ".duplicateOf." + incumbent_key[len("vtx."):],
                "document": {"class": "duplicateOf", "isDeleted": False,
                             "sourceVertex": identity_key, "targetVertex": incumbent_key,
                             "localName": "duplicateOf", "data": {"criteria": matched[incumbent_key]}}})

        events = [{"class": "identity.created", "data": {
            "identityKey": identity_key,
            "state": initial_state,
            "duplicate": duplicate,
            "matchedIdentityKeys": matched_identity_keys,
        }}]

        return {
            "mutations": mutations,
            "events": events,
            "response": {"primaryKey": identity_key},
        }

    if ot == "ProvisionConsumerIdentity":
        nanoid_alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"
        target_actor_key = p.targetActorKey if hasattr(p, "targetActorKey") else None
        if target_actor_key == None or type(target_actor_key) != type(""):
            fail("InvalidArgument: targetActorKey: required")
        prefix = "vtx.identity."
        if not target_actor_key.startswith(prefix):
            fail("InvalidArgument: targetActorKey: must be vtx.identity.<NanoID>")
        actor_id = target_actor_key[len(prefix):]
        if len(actor_id) != 20:
            fail("InvalidArgument: targetActorKey: id segment must be a 20-char NanoID")
        for ch in actor_id.elems():
            if ch not in nanoid_alphabet:
                fail("InvalidArgument: targetActorKey: id segment must be NanoID-alphabet")

        # Pinned to the package's OWN consumer role, not trusted from the
        # payload: the grant matrix lets identityProvisioner AND operator
        # call this op, so a caller-supplied consumerRoleKey that was merely
        # checked for "is some live role" could steer an actor into ANY role
        # (e.g. operator) instead of consumer. Equality against the literal
        # closes that — the field still exists so the caller is explicit
        # about intent, but the script is the enforcement boundary.
        consumer_role_key = p.consumerRoleKey if hasattr(p, "consumerRoleKey") else None
        if consumer_role_key != "__EXPECTED_CONSUMER_ROLE_KEY__":
            fail("InvalidArgument: consumerRoleKey: must be the identity-domain consumer role")
        role_id = consumer_role_key[len("vtx.role."):]
        link_key = "lnk.identity." + actor_id + ".holdsRole.role." + role_id

        # target_actor_key legitimately may not exist yet (the fresh-actor
        # case).
        # read-posture: (d) declared in contextHint.optionalReads by the
        # Gateway's provisionActorIfNeeded dispatcher (internal/gateway/gateway.go)
        #
        # Every read past this point sits inside a branch that is about to
        # write. The dominant case by far is a returning actor who already
        # holds the grant, and that path deliberately touches nothing beyond
        # these two keys: this op runs on every authenticated request, so a
        # no-op must not be able to fail on the health of state it does not
        # need.
        existing = kv.Read(target_actor_key)
        if existing != None:
            # The vertex exists but the grant may not: an identity minted by
            # any other path (a seeded persona, an entity binding's
            # CreateUnclaimedIdentity) holds whatever roles that path granted
            # and no consumer among them. This op's contract is "the actor the
            # Gateway verified holds consumer", so completing a half-provisioned
            # identity is the same job as provisioning a fresh one.
            #
            # ABSENT-ONLY, deliberately diverging from AssignRole's revive
            # branch (rbac-domain grant_link): that caller is an operator
            # explicitly re-granting, whereas this one is an automatic
            # per-request pre-flight, so reviving a tombstone would mean a
            # RevokeRole on consumer is undone by the revoked actor's very
            # next request. Tombstoned stays tombstoned.
            #
            # A tombstoned IDENTITY confers nothing and must not acquire a
            # role, so it takes the same no-op path.
            if existing.isDeleted:
                return {"mutations": [], "events": []}
            granted = state[link_key] if link_key in state else None
            if granted != None:
                # No "response" on the no-op path: the write-path
                # reply-constraint rejects a script-named primaryKey that isn't
                # in this op's own write footprint, and an empty-mutations
                # no-op writes nothing (mirrors AssignRole / CreateTask's
                # idempotent no-op shape). Absence here is indistinguishable
                # from an undeclared read, which is why the deterministic
                # link_key is a REQUIRED optionalReads entry at every
                # dispatcher: hydrated as absent, an already-granted actor
                # would fall through to a create and RevisionConflict on
                # every authenticated request.
                return {"mutations": [], "events": []}
            require_live_role(consumer_role_key)
            grant = {"op": "create", "key": link_key,
                     "document": {"class": "holdsRole", "isDeleted": False,
                                  "sourceVertex": target_actor_key,
                                  "targetVertex": consumer_role_key,
                                  "localName": "holdsRole", "data": {}}}
            # primaryKey is the link, not the identity: the link is the only
            # key in this branch's write footprint.
            return {"mutations": [grant],
                    "events": [{"class": "identity.consumerGranted",
                                "data": {"identityKey": target_actor_key,
                                         "roleKey": consumer_role_key,
                                         "linkKey": link_key}}],
                    "response": {"primaryKey": link_key}}

        require_live_role(consumer_role_key)
        mutations = [
            {"op": "create", "key": target_actor_key,
             "document": {"class": "identity", "isDeleted": False, "data": {}}},
            {"op": "create", "key": target_actor_key + ".state",
             "document": {"class": "state", "vertexKey": target_actor_key, "localName": "state",
                          "isDeleted": False, "data": {"value": "claimed"}}},
            {"op": "create", "key": link_key,
             "document": {"class": "holdsRole", "isDeleted": False,
                          "sourceVertex": target_actor_key, "targetVertex": consumer_role_key,
                          "localName": "holdsRole", "data": {}}},
        ]

        # Optional IdP provenance (Contract #11 §11.3): present only for an
        # opaque-mode token (a real external IdP); a nanoid-mode/dev token
        # carries neither, so this whole block is skipped for dev-provisioned
        # actors — exactly the "absent for nanoid-mode" behavior the DDL
        # documents. The pair travels together: a caller supplying one
        # without the other is a wiring fault, not a partial-provenance case.
        idp_issuer = p.idpIssuer if hasattr(p, "idpIssuer") else None
        idp_subject = p.idpSubject if hasattr(p, "idpSubject") else None
        if (idp_issuer == None) != (idp_subject == None):
            fail("InvalidArgument: idpIssuer/idpSubject: must both be present or both absent")
        if idp_issuer != None:
            mutations.append({"op": "create", "key": target_actor_key + ".idpBinding",
                "document": {"class": "idpBinding", "vertexKey": target_actor_key, "localName": "idpBinding",
                             "isDeleted": False, "data": {"iss": idp_issuer, "sub": idp_subject}}})

        events = [{"class": "identity.provisioned", "data": {"identityKey": target_actor_key}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": target_actor_key}}

    if ot == "ClaimIdentity":
        def fail_claim(outcome):
            fail("ClaimKeyInvalid: " + outcome)

        # This branch adjudicates three different facts about the graph -- no
        # such identity, one that exists but is already claimed, one that
        # exists and is unclaimed but whose secret the caller does not hold --
        # and answers all of them with a single wire shape (Contract #9 §9.3).
        # A cascade of early returns would make those answers separable in TIME
        # instead: each exit does strictly less work than the next, and a few
        # tenths of a millisecond of monotone bias is recoverable by averaging,
        # which is an identity-existence oracle on an endpoint every consumer
        # holds and nothing rate-limits.
        #
        # So nothing here returns early. Every check runs, the first failing
        # one supplies the outcome word (first_outcome), and the branch fails
        # once at the bottom. The checks are ordered by which outcome word
        # takes precedence, so every vector records the same Health-KV outcome
        # its cause is named by.
        #
        # What has to be uniform is the work that depends on the TARGET's
        # state: that is the only fact the caller does not already hold. Work
        # that varies with the caller's OWN payload -- an absent claimKey, a
        # targetIdentityKey that is not an identity key at all -- tells them
        # nothing they did not just type, which is why the stand-ins below can
        # substitute for a malformed payload rather than a check being skipped
        # for a target.
        outcome = None

        claim_key_plaintext = p.claimKey if hasattr(p, "claimKey") else None
        claim_key_usable = (claim_key_plaintext != None and
                            type(claim_key_plaintext) == type("") and
                            len(claim_key_plaintext) > 0)
        outcome = first_outcome(outcome, not claim_key_usable, "invalid-key")

        target_identity_key = p.targetIdentityKey if hasattr(p, "targetIdentityKey") else None
        target_key_usable = (target_identity_key != None and
                             type(target_identity_key) == type("") and
                             len(target_identity_key) > 0)
        outcome = first_outcome(outcome, not target_key_usable, "no-target")

        # Every lookup below needs a string operand. An unusable payload key
        # becomes the empty string rather than a skipped block: "" is a legal
        # dict key, a legal startswith receiver and a legal concatenation
        # operand, and it matches nothing in the hydrated state.
        lookup_key = target_identity_key if target_key_usable else ""
        outcome = first_outcome(outcome, not lookup_key.startswith("vtx.identity."), "no-target")
        outcome = first_outcome(outcome, not vertex_alive(state, lookup_key), "no-target")

        state_aspect_key = lookup_key + ".state"
        state_aspect = state[state_aspect_key] if state_aspect_key in state else None
        current_state = None
        if state_aspect != None and state_aspect.data != None and "value" in state_aspect.data:
            current_state = state_aspect.data["value"]
        outcome = first_outcome(outcome, current_state == None, "no-target")
        outcome = first_outcome(outcome, current_state == "claimed", "wrong-state")
        outcome = first_outcome(outcome, current_state == "flagged-for-review", "flagged")
        outcome = first_outcome(outcome, current_state == "merged", "merged")
        outcome = first_outcome(outcome, current_state != "unclaimed", "wrong-state")

        actor_key = op.actor
        # The credential must be a LIVE vertex before its boundTo edge is
        # emitted. Contract #3 §3.5 leaves endpoint resolution to the script
        # and this script must guarantee it: the edge is the sole input to
        # identityCredentialBindingsRead, which anchors on (c:identity) and
        # is presented as a person's COMPLETE list of sign-in methods. With no
        # credential vertex the binding row never appears, while
        # identityCredentialsRead still lists the credential -- for that
        # reader an absent endpoint is a silent under-report, not a null.
        #
        # Reject rather than mint: creating the vertex is
        # ProvisionConsumerIdentity's job, granted scope=any to
        # identityProvisioner/operator precisely because minting identities is
        # privileged. Minting here would make a claim secret sufficient to
        # create an arbitrary vtx.identity.<NanoID> at a caller-chosen key
        # under a scope=self grant, and would leave it half-provisioned -- no
        # .state, no holdsRole -- a shape nothing else in the corpus produces.
        #
        # A TOMBSTONED credential takes this branch too, and that is the point:
        # ProvisionConsumerIdentity deliberately no-ops on a tombstoned actor
        # ("tombstoned stays tombstoned"), so a revoked credential must not be
        # able to claim. The refusal is permanent by design, not transient.
        outcome = first_outcome(outcome, not vertex_alive(state, actor_key), "credential-not-provisioned")

        cred_index_key = credential_index_key(actor_key)
        cred_index = state[cred_index_key] if cred_index_key in state else None
        outcome = first_outcome(outcome, vertex_alive(state, cred_index_key), "credential-already-bound")

        claim_key_aspect_key = lookup_key + ".claimKey"
        claim_key_aspect = state[claim_key_aspect_key] if claim_key_aspect_key in state else None
        claim_key_alive = (claim_key_aspect != None and
                           not (hasattr(claim_key_aspect, "isDeleted") and claim_key_aspect.isDeleted))
        stored_hash = None
        if claim_key_alive and claim_key_aspect.data != None and "hash" in claim_key_aspect.data:
            stored_hash = claim_key_aspect.data["hash"]
        outcome = first_outcome(outcome, stored_hash == None, "invalid-key")
        # The compare's operands are typed here rather than left to the
        # stand-in, so a stored hash of the wrong type is a refusal this check
        # makes and not a coincidence of nothing hashing to the stand-in.
        outcome = first_outcome(outcome, type(stored_hash) != type(""), "invalid-key")

        # Both crypto calls run on every cause, against the stand-ins when the
        # real operands are absent, so a caller cannot separate "there was
        # nothing to compare against" from "the comparison failed". The
        # stand-in comparand is what keeps a wrongly-typed stored hash from
        # raising a builtin argument error -- crypto builtins refuse a
        # non-string operand -- while the typed check above supplies its word.
        submitted_hash = crypto.sha256(claim_key_plaintext if claim_key_usable else PLACEHOLDER_SECRET)
        comparand = stored_hash if type(stored_hash) == type("") else PLACEHOLDER_STORED_HASH
        outcome = first_outcome(outcome, not crypto.constant_time_equal(submitted_hash, comparand), "invalid-key")

        # Erasure gate (§6), deliberately placed AFTER the secret comparison.
        # A claim writes a credentialindex vertex and a boundTo link at BOTH
        # ends, and every one of those is inside what UnbindIdentityCredentials
        # erases (it sweeps "in" AND "out", identity-domain/
        # unbind_identity_credentials.go, because the identity is the target of
        # every credential bound to it and the SOURCE when it is itself someone
        # else's credential). So both positions are gated, not just the target.
        #
        # Below the secret check, and the reason is counter attribution. The
        # outcome word never reaches the wire -- the Processor reclassifies
        # every ClaimKeyInvalid to one generic code (NFR-S6) -- so Health KV is
        # the only channel that carries it, and a gate placed above the
        # comparison would divert every WRONG-secret attempt against a sealed
        # identity out of the claim-attempts.invalid-key counter an operator
        # watches for brute force, and into claim-attempts.erased. Below it,
        # only a caller who proved the secret learns the WORD, and the
        # brute-force counter keeps counting brute force.
        #
        # The word, not the work: both write_path_closed calls run on every
        # cause, and write_path_closed short-circuits on a present marker
        # without going on to read .piiKey, so a sealed position costs one
        # snapshot lookup where an unsealed one costs two. derive_reads
        # hydrates both keys, so that residue is microseconds of Starlark
        # rather than a round trip -- far under the substrate-rooted hydration
        # difference §6.3 already accepts.
        #
        # Both positions are asked only about a key the Contract #1 grammar
        # accepts, under is_identity_vertex_key -- the same predicate
        # derive_reads applies through erasure_gate_keys. The gate reads
        # through kv.Read, and a position the grammar rejects has no declared
        # gate key behind it, so asking about one would fall through to a live
        # Core KV GET on a malformed key and answer an internal fault where a
        # named outcome belongs. Asking exactly what the derivation answered
        # keeps the two in step. The predicate turns on the caller's own
        # submitted strings rather than on any target's state, so it separates
        # nothing an attacker does not already hold.
        #
        # That lockstep is load-bearing: a position this predicate skips was
        # never hydrated either, so it has already failed vertex_alive above
        # and carries a recorded outcome. Relaxing one predicate without the
        # other -- widening the gate here, or narrowing the derivation there --
        # silently opens the gate on a position nothing else refuses.
        gate_target = lookup_key if is_identity_vertex_key(lookup_key) else None
        outcome = first_outcome(outcome, gate_target != None and write_path_closed(gate_target), "erased")
        gate_actor = actor_key if is_identity_vertex_key(actor_key) else None
        outcome = first_outcome(outcome, gate_actor != None and write_path_closed(gate_actor), "erased")

        if outcome != None:
            fail_claim(outcome)

        observed_at = op.submittedAt

        # The claim is the moment the identity becomes an acting business
        # identity (env.Actor / lattice.actor_id resolve to it from here on,
        # gateway-claim-flow-identity-provisioning-design.md §11.0 R1) — grant
        # consumer in the same commit so there is no window where the person
        # acts as a role-less identity. Pinned to the package's own literal
        # (mirrors ProvisionConsumerIdentity, §11.5 R2): no caller input names
        # the role, so there is nothing to steer.
        consumer_role_key = "__EXPECTED_CONSUMER_ROLE_KEY__"
        consumer_role_id = consumer_role_key[len("vtx.role."):]
        target_id = target_identity_key[len("vtx.identity."):]
        consumer_grant_key = "lnk.identity." + target_id + ".holdsRole.role." + consumer_role_id

        mutations = [
            {"op": "create", "key": target_identity_key + ".credentialBinding",
             "document": {"class": "credentialBinding", "vertexKey": target_identity_key,
                          "localName": "credentialBinding", "isDeleted": False,
                          "data": {"actorKey": actor_key, "boundAt": observed_at,
                                   "credentials": [{"actorKey": actor_key, "boundAt": observed_at}]}}},
            {"op": "update", "key": target_identity_key + ".state",
             "document": {"class": "state", "vertexKey": target_identity_key,
                          "localName": "state", "isDeleted": False,
                          "data": {"value": "claimed"}}},
            {"op": "tombstone", "key": target_identity_key + ".claimKey"},
            credential_index_mutation(cred_index_key, cred_index, actor_key, target_identity_key, observed_at),
            credential_bound_to_mutation(actor_key, target_identity_key, observed_at),
            # An upsert, not a create: the grant may already be present, because
            # any authenticated touch of this identity provisions it (the
            # Gateway's ProvisionConsumerIdentity pre-flight) and a seeder may
            # grant it outright via AssignRole. A create asserts revision 0 and
            # would take the whole atomic claim batch down with a
            # RevisionConflict, so the ceremony would be unable to complete for
            # exactly the identities that had already been reached. An update
            # commits conditioned on the revision the key is read at (step 8),
            # or unconditioned when it is genuinely absent, which is what
            # "ensure this claimed identity holds consumer" means. Unlike the
            # per-request pre-flight — which grants only where the link is
            # absent, so a RevokeRole is never undone by mere traffic — the
            # ceremony is a one-time, secret-bearing act that must leave the
            # person able to act at all, and its dispatchers include browser
            # clients that cannot compute the deterministic role key a declared
            # read would require.
            {"op": "update", "key": consumer_grant_key,
             "document": {"class": "holdsRole", "isDeleted": False,
                          "sourceVertex": target_identity_key, "targetVertex": consumer_role_key,
                          "localName": "holdsRole", "data": {}}},
        ]

        events = [{"class": "identity.claimed", "data": {
            "identityKey": target_identity_key,
            "actorKey": actor_key,
        }}]

        # The identity vertex itself is not mutated by a claim; the principal
        # committed key is the state aspect (unclaimed -> claimed). primaryKey
        # names the principal entity (the identity); the Processor accepts it as
        # the 3-segment root of the committed aspects.
        return {
            "mutations": mutations,
            "events": events,
            "response": {"primaryKey": target_identity_key},
        }

    if ot == "RotateClaimKey":
        # R4 (gateway-claim-flow-identity-provisioning-design.md §11.5): a
        # staff-gated re-issue for a lost claim secret. CreateUnclaimedIdentity's
        # claimKeyHash is the only copy Lattice ever stores (hash-only), so an
        # unclaimed identity whose secret never reached the applicant (or was
        # discarded client-side) is otherwise permanently unclaimable. Mirrors
        # CreateUnclaimedIdentity's own claimKeyHash validation; grant roles are
        # identical (staff, not the identity itself, since it has no credential yet).
        identity_key = p.identityKey if hasattr(p, "identityKey") else None
        if identity_key == None or type(identity_key) != type("") or len(identity_key) == 0:
            fail("InvalidArgument: identityKey: required")
        if not identity_key.startswith("vtx.identity."):
            fail("InvalidArgument: identityKey: must be a vtx.identity.<NanoID> key")

        target_vtx = state[identity_key] if identity_key in state else None
        if target_vtx == None or (hasattr(target_vtx, "isDeleted") and target_vtx.isDeleted):
            fail("InvalidArgument: identityKey: no such identity")
        current_state = read_state(state, identity_key)
        if current_state != "unclaimed":
            fail("InvalidStateTransition: RotateClaimKey requires state=unclaimed, got " + str(current_state))

        new_hash = p.claimKeyHash if hasattr(p, "claimKeyHash") else None
        if new_hash == None or type(new_hash) != type("") or len(new_hash) == 0:
            fail("InvalidArgument: claimKeyHash: required non-empty lowercase hex sha256")
        if len(new_hash) != 64:
            fail("InvalidArgument: claimKeyHash: must be 64-char lowercase hex sha256")
        for ch in new_hash.elems():
            if not ((ch >= "0" and ch <= "9") or (ch >= "a" and ch <= "f")):
                fail("InvalidArgument: claimKeyHash: must be lowercase hex")
        new_algo = p.claimKeyAlgo if hasattr(p, "claimKeyAlgo") else None
        if new_algo == None or new_algo == "":
            new_algo = "sha256"
        if new_algo != "sha256":
            fail("InvalidArgument: claimKeyAlgo: only sha256 is supported")

        # .claimKey is declared Reads: an unclaimed identity always has one
        # (created together with the vertex by CreateUnclaimedIdentity, tombstoned
        # only by a successful ClaimIdentity) -- absence here means the state and
        # claimKey aspects have drifted, a wiring fault, not a normal case.
        claim_key_aspect_key = identity_key + ".claimKey"
        claim_key_aspect = state[claim_key_aspect_key] if claim_key_aspect_key in state else None
        if claim_key_aspect == None or (hasattr(claim_key_aspect, "isDeleted") and claim_key_aspect.isDeleted):
            fail("InvalidArgument: identityKey: no claimKey aspect to rotate")

        mutations = [
            {"op": "update", "key": claim_key_aspect_key,
             "document": {"class": "claimKey", "vertexKey": identity_key, "localName": "claimKey",
                          "isDeleted": False, "data": {"hash": new_hash, "algo": new_algo}}},
        ]

        # No event: mirrors claimKey's own no-event-on-write posture (see
        # InitiateCredentialLink below) -- nothing consumes an armed-but-unused
        # claim secret.
        return {
            "mutations": mutations,
            "events": [],
            "response": {"primaryKey": identity_key},
        }

    if ot == "InitiateCredentialLink":
        # "as U: arm a link secret" -- submitted through the normal resolved
        # path (env.Actor == U, authContext.target == U), so the identity
        # being armed IS the caller; no separate target field
        # (multi-credential-identity-linking-design.md §3.2).
        link_key_hash = p.linkKeyHash if hasattr(p, "linkKeyHash") else None
        if link_key_hash == None or type(link_key_hash) != type("") or len(link_key_hash) == 0:
            fail("InvalidArgument: linkKeyHash: required non-empty lowercase hex sha256")
        if len(link_key_hash) != 64:
            fail("InvalidArgument: linkKeyHash: must be 64-char lowercase hex sha256")
        for ch in link_key_hash.elems():
            if not ((ch >= "0" and ch <= "9") or (ch >= "a" and ch <= "f")):
                fail("InvalidArgument: linkKeyHash: must be lowercase hex")
        link_key_algo = p.linkKeyAlgo if hasattr(p, "linkKeyAlgo") else None
        if link_key_algo == None or link_key_algo == "":
            link_key_algo = "sha256"
        if link_key_algo != "sha256":
            fail("InvalidArgument: linkKeyAlgo: only sha256 is supported")

        u_key = op.actor
        if not u_key.startswith("vtx.identity."):
            fail("InvalidArgument: actor: must be a vtx.identity.<NanoID>")

        u_vtx = state[u_key] if u_key in state else None
        if u_vtx == None or (hasattr(u_vtx, "isDeleted") and u_vtx.isDeleted):
            fail("IdentityNotFound: " + u_key)
        u_state = read_state(state, u_key)
        u_merged_into = read_merged_into(state, u_key)
        enforce_not_merged(u_state, u_merged_into)
        if u_state != "claimed":
            fail("InvalidStateTransition: InitiateCredentialLink requires state=claimed, got " + str(u_state))

        # Create-or-overwrite: .linkKey is declared optionalReads by the
        # caller (not Reads), so this key carries no step-4 hydrated
        # revision and "update" commits as an unconditioned blind Put --
        # arming a fresh secret whether or not one was already armed
        # (re-initiating rotates a lost secret). Mirrors the merge script's
        # unconditioned-update idiom (identity-hygiene MergeIdentity §3.3).
        mutations = [
            {"op": "update", "key": u_key + ".linkKey",
             "document": {"class": "linkKey", "vertexKey": u_key, "localName": "linkKey",
                          "isDeleted": False, "data": {"hash": link_key_hash, "algo": link_key_algo}}},
        ]

        # No event: nothing consumes an armed-but-unused link secret
        # (mirrors claimKey's own no-event-on-write posture).
        return {
            "mutations": mutations,
            "events": [],
            "response": {"primaryKey": u_key},
        }

    if ot == "CompleteCredentialLink":
        def fail_link(outcome):
            # Reuses ClaimIdentity's exact "ClaimKeyInvalid: " prefix so the
            # Processor's existing classifyScriptError/classifyStepError
            # reclassification (NFR-S6 anti-enumeration — generic wire code,
            # specifics via Health KV only) covers this op with zero Go-side
            # changes, instead of adding a new ErrorCode (a frozen Contract #2
            # §2.6 change requiring its own ratification).
            fail("ClaimKeyInvalid: " + outcome)

        # The same accumulate-then-fail-once shape ClaimIdentity uses, for the
        # same reason: this op redeems a secret against a target whose state a
        # caller is not entitled to learn, and a cascade of early returns puts
        # the causes it refuses to name on the wire back into the time domain
        # (nfr-s6-release-quantum-payload-design.md §4.1). Every check runs,
        # first_outcome keeps the highest-ranked failure, and the branch fails
        # once at the bottom.
        outcome = None

        link_key_plaintext = p.linkKey if hasattr(p, "linkKey") else None
        link_key_usable = (link_key_plaintext != None and
                           type(link_key_plaintext) == type("") and
                           len(link_key_plaintext) > 0)
        outcome = first_outcome(outcome, not link_key_usable, "invalid-key")

        target_identity_key = p.targetIdentityKey if hasattr(p, "targetIdentityKey") else None
        target_key_usable = (target_identity_key != None and
                             type(target_identity_key) == type("") and
                             len(target_identity_key) > 0)
        outcome = first_outcome(outcome, not target_key_usable, "no-target")

        # An unusable payload key becomes the empty string rather than a
        # skipped block, so every lookup below still has a string operand that
        # matches nothing in the hydrated state.
        lookup_key = target_identity_key if target_key_usable else ""
        outcome = first_outcome(outcome, not lookup_key.startswith("vtx.identity."), "no-target")
        outcome = first_outcome(outcome, not vertex_alive(state, lookup_key), "no-target")

        target_state = read_state(state, lookup_key)
        outcome = first_outcome(outcome, target_state == "merged", "merged")
        outcome = first_outcome(outcome, target_state != "claimed", "wrong-state")

        # The same one-credential-<=-one-identity dedup guard ClaimIdentity
        # applies (#11 §11.4): this is a declared-optionalReads guard for the
        # friendly generic error only -- the load-bearing stop is the
        # CreateOnly create of cred_index_key below, which RevisionConflicts
        # on an already-bound credential regardless of declaration (finding
        # A4, mirrored from ClaimIdentity).
        actor_key = op.actor
        # The same endpoint guard ClaimIdentity applies, for the same reason
        # and on the same edge: this op emits the boundTo whose absence the
        # bindings projection cannot distinguish from "no such sign-in
        # method". Second-credential linking reaches this through the
        # Gateway's raw-credential carve-out, so the actor here is the NEW
        # credential -- exactly the one first-touch provisioning may have
        # skipped.
        outcome = first_outcome(outcome, not vertex_alive(state, actor_key), "credential-not-provisioned")

        cred_index_key = credential_index_key(actor_key)
        cred_index = state[cred_index_key] if cred_index_key in state else None
        outcome = first_outcome(outcome, vertex_alive(state, cred_index_key), "credential-already-bound")

        link_key_aspect_key = lookup_key + ".linkKey"
        link_key_aspect = state[link_key_aspect_key] if link_key_aspect_key in state else None
        link_key_alive = (link_key_aspect != None and
                          not (hasattr(link_key_aspect, "isDeleted") and link_key_aspect.isDeleted))
        stored_hash = None
        if link_key_alive and link_key_aspect.data != None and "hash" in link_key_aspect.data:
            stored_hash = link_key_aspect.data["hash"]
        outcome = first_outcome(outcome, stored_hash == None, "invalid-key")
        # The compare's operands are typed here rather than left to the
        # stand-in, so a stored hash of the wrong type is a refusal this check
        # makes and not a coincidence of nothing hashing to the stand-in.
        outcome = first_outcome(outcome, type(stored_hash) != type(""), "invalid-key")

        # Both crypto calls run on every cause, against the stand-ins when the
        # real operands are absent, so "nothing to compare against" and "the
        # comparison failed" cost the same. The stand-in comparand is what
        # keeps a wrongly-typed stored hash from raising a builtin argument
        # error -- crypto builtins refuse a non-string operand -- while the
        # typed check above supplies its word.
        submitted_hash = crypto.sha256(link_key_plaintext if link_key_usable else PLACEHOLDER_SECRET)
        comparand = stored_hash if type(stored_hash) == type("") else PLACEHOLDER_STORED_HASH
        outcome = first_outcome(outcome, not crypto.constant_time_equal(submitted_hash, comparand), "invalid-key")

        # Erasure gate (§6) -- the same two link positions and the same
        # below-the-secret placement as ClaimIdentity, for the same reasons.
        # The placement buys the counter attribution, and it buys it on the
        # outcome WORD only: both write_path_closed calls run on every cause,
        # and write_path_closed short-circuits on a present marker without
        # reading .piiKey, so a sealed position costs one snapshot lookup where
        # an unsealed one costs two. derive_reads hydrates both keys, so that
        # residue is microseconds of Starlark rather than a round trip.
        #
        # Both positions are asked only about a key the Contract #1 grammar
        # accepts, under the same is_identity_vertex_key predicate
        # derive_reads applies through erasure_gate_keys: the gate reads
        # through kv.Read, and a position the grammar rejects has no declared
        # gate key behind it, so asking about one would fall through to a live
        # Core KV GET and answer an internal fault where a named outcome
        # belongs. The predicate turns on the caller's own submitted strings
        # rather than on any target's state.
        #
        # The lockstep is load-bearing: a position this predicate skips was
        # never hydrated either, so it has already failed vertex_alive above
        # and carries a recorded outcome. Relaxing one predicate without the
        # other opens the gate on a position nothing else refuses.
        gate_target = lookup_key if is_identity_vertex_key(lookup_key) else None
        outcome = first_outcome(outcome, gate_target != None and write_path_closed(gate_target), "erased")
        gate_actor = actor_key if is_identity_vertex_key(actor_key) else None
        outcome = first_outcome(outcome, gate_actor != None and write_path_closed(gate_actor), "erased")

        if outcome != None:
            fail_link(outcome)

        observed_at = op.submittedAt
        new_entry = {"actorKey": actor_key, "boundAt": observed_at}

        # U.credentialBinding is declared optionalReads: absent entirely for
        # a Scenario-B identity that never claimed via ClaimIdentity (its
        # implicit self-credential lives only as its own vertex key), in
        # which case this branch creates the aspect for the first time --
        # otherwise it appends to the existing array (or the pre-Fire-2
        # singular actorKey/boundAt fields, folded into a one-element array).
        binding_key = target_identity_key + ".credentialBinding"
        existing_binding = state[binding_key] if binding_key in state else None
        binding_absent = existing_binding == None or (hasattr(existing_binding, "isDeleted") and existing_binding.isDeleted)

        mutations = [
            credential_index_mutation(cred_index_key, cred_index, actor_key, target_identity_key, observed_at),
            credential_bound_to_mutation(actor_key, target_identity_key, observed_at),
            {"op": "tombstone", "key": link_key_aspect_key},
        ]

        if binding_absent:
            mutations.append({"op": "create", "key": binding_key,
                "document": {"class": "credentialBinding", "vertexKey": target_identity_key,
                             "localName": "credentialBinding", "isDeleted": False,
                             "data": {"actorKey": actor_key, "boundAt": observed_at,
                                      "credentials": [new_entry]}}})
        else:
            existing_data = existing_binding.data if existing_binding.data != None else {}
            existing_credentials = existing_data.get("credentials")
            if existing_credentials == None or type(existing_credentials) != type([]):
                first_actor = existing_data.get("actorKey")
                if first_actor != None:
                    existing_credentials = [{"actorKey": first_actor, "boundAt": existing_data.get("boundAt")}]
                else:
                    existing_credentials = []
            unioned = list(existing_credentials) + [new_entry]
            singular_actor = existing_data.get("actorKey")
            singular_bound = existing_data.get("boundAt")
            if singular_actor == None:
                singular_actor = actor_key
                singular_bound = observed_at
            mutations.append({"op": "update", "key": binding_key,
                "document": {"class": "credentialBinding", "vertexKey": target_identity_key,
                             "localName": "credentialBinding", "isDeleted": False,
                             "data": {"actorKey": singular_actor, "boundAt": singular_bound,
                                      "credentials": unioned}}})

        # Deliberately the existing identity.claimed class: the semantic
        # ("this credential is now bound to this identity") and payload are
        # identical, so the shipped credential-bindings materializer folds
        # this with zero changes (multi-credential-identity-linking-design.md
        # §4.3).
        events = [{"class": "identity.claimed", "data": {
            "identityKey": target_identity_key,
            "actorKey": actor_key,
        }}]

        return {
            "mutations": mutations,
            "events": events,
            "response": {"primaryKey": target_identity_key},
        }

    if ot == "UnlinkCredential":
        # {Scope: self} as U -- the normal resolved path (op.actor == U ==
        # target), not the raw-credential carve-out: U is removing an entry
        # from its OWN credentials array, not proving control of the
        # credential being removed (multi-credential-identity-linking-
        # design.md §8).
        def fail_unlink(outcome):
            fail("CredentialUnlinkRejected: " + outcome)

        u_key = op.actor
        if not u_key.startswith("vtx.identity."):
            fail_unlink("no-target")

        u_vtx = state[u_key] if u_key in state else None
        if u_vtx == None or (hasattr(u_vtx, "isDeleted") and u_vtx.isDeleted):
            fail_unlink("no-target")
        u_state = read_state(state, u_key)
        u_merged_into = read_merged_into(state, u_key)
        enforce_not_merged(u_state, u_merged_into)
        if u_state != "claimed":
            fail_unlink("wrong-state")

        credential_actor_key = p.credentialActorKey if hasattr(p, "credentialActorKey") else None
        if credential_actor_key == None or type(credential_actor_key) != type("") or len(credential_actor_key) == 0:
            fail_unlink("not-found")

        # U.credentialBinding is declared optionalReads (opmetas.go's
        # UnlinkCredential Dispatch spec), and it has to be: a claimed U
        # usually has one -- written by ClaimIdentity, or by
        # CompleteCredentialLink's binding_absent branch for a Scenario-B
        # identity's first linked credential -- but absence is a real,
        # ordinary state, not a wiring fault. It means U has nothing to
        # unlink: the implicit self-credential case (§8: "not an array entry,
        # not unlinkable -- it IS the identity") folds into the same generic
        # not-found outcome below, and a required-read declaration would
        # fault HydrationMiss before this branch could render it. A TOMBSTONED
        # binding -- what MergeIdentity leaves on a merged-away secondary --
        # takes the same branch.
        binding_key = u_key + ".credentialBinding"
        existing_binding = state[binding_key] if binding_key in state else None
        binding_absent = existing_binding == None or (hasattr(existing_binding, "isDeleted") and existing_binding.isDeleted)
        if binding_absent:
            fail_unlink("not-found")

        existing_data = existing_binding.data if existing_binding.data != None else {}
        existing_credentials = existing_data.get("credentials")
        if existing_credentials == None or type(existing_credentials) != type([]):
            first_actor = existing_data.get("actorKey")
            if first_actor != None:
                existing_credentials = [{"actorKey": first_actor, "boundAt": existing_data.get("boundAt")}]
            else:
                existing_credentials = []

        remaining = []
        removed_entry = None
        for c in existing_credentials:
            if c.get("actorKey") == credential_actor_key and removed_entry == None:
                removed_entry = c
                continue
            remaining.append(c)

        if removed_entry == None:
            fail_unlink("not-found")
        if len(remaining) == 0:
            fail_unlink("last-credential")

        singular_actor = existing_data.get("actorKey")
        singular_bound = existing_data.get("boundAt")
        if singular_actor == credential_actor_key:
            singular_actor = remaining[0]["actorKey"]
            singular_bound = remaining[0]["boundAt"]

        mutations = [
            {"op": "tombstone", "key": credential_index_key(credential_actor_key)},
            {"op": "tombstone", "key": credential_bound_to_key(credential_actor_key, u_key)},
            {"op": "update", "key": binding_key,
             "document": {"class": "credentialBinding", "vertexKey": u_key, "localName": "credentialBinding",
                          "isDeleted": False,
                          "data": {"actorKey": singular_actor, "boundAt": singular_bound,
                                   "credentials": remaining}}},
        ]

        # identity.unbound: the credential-bindings materializer's one
        # explicit bucket-key DELETE fold (Contract #11 §11.4, design §8) --
        # every other event this package emits (claimed/rebound) only ever
        # writes/overwrites, never shrinks the bucket's row set.
        events = [{"class": "identity.unbound", "data": {
            "identityKey": u_key,
            "actorKey": credential_actor_key,
        }}]

        return {
            "mutations": mutations,
            "events": events,
            "response": {"primaryKey": u_key},
        }

    if ot == "ReconcileCredentialBinding":
        # Converges the boundTo link plane onto the credentialindex vertex for
        # one credential. Every path that binds a credential writes the index
        # vertex and the link in the SAME batch, so the index answers "which
        # credential belongs to whom" for bindings the link plane never
        # recorded. It is authoritative over that set and no wider: an identity
        # whose only sign-in is the raw actor ProvisionConsumerIdentity minted
        # has no index vertex at all, and nothing here reaches it.
        def fail_reconcile(outcome):
            fail("CredentialReconcileRejected: " + outcome)

        credential_actor_key = getattr(p, "credentialActorKey", None)
        identity_key = getattr(p, "identityKey", None)
        if type(credential_actor_key) != type("") or not credential_actor_key.startswith("vtx.identity."):
            fail("InvalidArgument: credentialActorKey: must be a vtx.identity.<NanoID> key")
        if type(identity_key) != type("") or not identity_key.startswith("vtx.identity."):
            fail("InvalidArgument: identityKey: must be a vtx.identity.<NanoID> key")
        if credential_actor_key == identity_key:
            # The same self-loop guard MergeIdentity's rekey loop applies: a
            # vertex cannot be its own credential, and the link key would name
            # one vertex twice.
            fail_reconcile("self-loop")

        # An EXISTING tombstone on the link is a retraction somebody made, and
        # this op never overturns one. The index cannot stand in for that
        # judgement: UnlinkCredential and the erasure path's own
        # UnbindIdentityCredentials both retire the index and the link
        # together, but nothing in this DDL's permittedCommands (deliberately
        # empty, multi-writer, open posture) guarantees every writer of a
        # boundTo tombstone keeps the pair in lockstep. Treating a live index
        # as sufficient authority would let any writer that tombstones the
        # link alone have its retraction silently undone.
        #
        # So the writable case is the ABSENT link, which is the only thing this
        # op was built to reach: a binding made before the link type existed.
        # A live link re-converges harmlessly (same document); a tombstoned one
        # is left exactly as whoever retracted it left it.
        link_key = credential_bound_to_key(credential_actor_key, identity_key)
        existing_link = state[link_key] if link_key in state else None
        if existing_link != None and hasattr(existing_link, "isDeleted") and existing_link.isDeleted:
            fail_reconcile("retracted")

        # The index vertex is the authority for the edge's CONTENT -- who owns
        # the credential and when it was bound. identityKey is client-supplied
        # (derive_reads needs both halves of the link key before anything is
        # hydrated), so an owner the index does not record is a forged edge.
        # A tombstoned index is the deliberate-unlink case.
        index_key = credential_index_key(credential_actor_key)
        index_vtx = state[index_key] if index_key in state else None
        if index_vtx == None or (hasattr(index_vtx, "isDeleted") and index_vtx.isDeleted):
            fail_reconcile("not-bound")
        index_data = index_vtx.data if index_vtx.data != None else {}
        if index_data.get("identityKey") != identity_key:
            fail_reconcile("owner-mismatch")

        # The same endpoint guard the bind paths apply, and this op needs it
        # most: what it exists to converge is the pre-link corpus -- bindings
        # made before the boundTo type existed -- which is exactly the
        # population whose credential vertex may never have been minted. Its
        # authority is the INDEX, which records the association in its own body
        # and can therefore name a credential that has no vertex at all; without
        # this check the one operator-run repair path would manufacture the
        # dangling edges the bind paths refuse, and identityCredentialBindings-
        # Read (anchored on the credential) would still project no row for them.
        if not vertex_alive(state, credential_actor_key):
            fail_reconcile("credential-not-provisioned")

        # Erasure gate (§6), placed AFTER not-bound and owner-mismatch. This op
        # can CREATE a boundTo link that never existed -- the legacy-binding
        # case this op exists for, a live credentialindex with no link because
        # it predates the link type. UnbindIdentityCredentials's sweep walks
        # boundTo links, not credentialindex vertices, so a legacy binding with
        # no link is never inside its enumeration; an index-only judgement here
        # would publish, for the first time, a decrypt-free boundTo association
        # for a sealed identity. Both endpoints are gated for the same reason
        # as the claim path.
        #
        # Unlike ClaimKeyInvalid, CredentialReconcileRejected is NOT
        # reclassified by the Processor, so this outcome word reaches the
        # caller verbatim. Above the two checks it would have answered "is this
        # identity sealed for erasure?" for any identity key at all, including
        # one with no index -- contradicting this op's own permission Note that
        # it reaches nothing the index does not already assert. Below them, a
        # caller learns it only for a binding the index already confirms is
        # theirs. Both endpoints are gated for the same reason as the claim
        # path: UnbindIdentityCredentials erases boundTo in both directions.
        if write_path_closed(identity_key):
            fail_reconcile("erased")
        if write_path_closed(credential_actor_key):
            fail_reconcile("erased")

        # The index's own boundAt, never observed_at: stamping the run time
        # would rewrite every historical binding's provenance to say it was
        # made the day this op ran.
        bound_at = index_data.get("boundAt")

        # The index is re-Put unchanged, conditioned on the revision this pass
        # read it at, and that guard is the only thing standing between the
        # decision and the commit. The whole judgement above rests on the index
        # -- but only MUTATION keys get their hydrated revision applied at step
        # 8, and without this the index would be read-and-forgotten: an
        # UnlinkCredential committing in the window between hydrate and commit
        # would tombstone the index and the link, and this batch would then
        # publish the edge live on top of it. The result is unrecoverable
        # rather than merely stale -- a later UnlinkCredential rejects
        # not-found (its array entry is already gone), so nothing can retract
        # the edge again and the lens projects a credential the person removed.
        # Conflicting here instead sends the whole op back through commitPipeline's
        # OCC retry, which re-hydrates and rejects not-bound on the tombstone.
        index_guard = {"op": "update", "key": index_key,
                       "document": {"class": "credentialindex", "isDeleted": False, "data": index_data},
                       "expectedRevision": index_vtx.revision}

        # The link IS this op's principal key: it is what the op exists to
        # write, and the reply-constraint admits a mutation key or the vertex
        # root of one, neither of which the owning identity is here.
        return {
            "mutations": [
                index_guard,
                credential_bound_to_mutation(credential_actor_key, identity_key, bound_at),
            ],
            "events": [],
            "response": {"primaryKey": credential_bound_to_key(credential_actor_key, identity_key)},
        }

    if ot == "RecordIdentityPII":
        identity_key = p.identityKey if hasattr(p, "identityKey") else None
        if identity_key == None or type(identity_key) != type("") or len(identity_key) == 0:
            fail("InvalidArgument: identityKey: required")
        if not identity_key.startswith("vtx.identity."):
            fail("InvalidArgument: identityKey: must be a vtx.identity.<NanoID> key")

        # The target identity must already exist, not be tombstoned, and not be
        # merged. The caller declares identity_key + its .state aspect in
        # ContextHint.Reads — known-key reads only. The .state aspect is always
        # present on a created identity; the merged guard keys off
        # state == "merged" (MergeIdentity sets state and mergedInto together),
        # so .mergedInto need not be hydrated here (it is absent pre-merge and
        # would otherwise be a hydration miss).
        target_vtx = state[identity_key] if identity_key in state else None
        if target_vtx == None or (hasattr(target_vtx, "isDeleted") and target_vtx.isDeleted):
            fail("InvalidArgument: identityKey: no such identity")
        current_state = read_state(state, identity_key)
        enforce_not_merged(current_state, read_merged_into(state, identity_key))

        # facet-staff-worlds-design.md §3.2's carried-forward scoping question:
        # this write predates the staff read spine and, unconfined, reaches ANY
        # identity -- F4's location-derived guard cannot bind it because a
        # walk-in identity has no location to confine against. The domain-shaped
        # boundary is the state machine instead: a STANDING front-desk grant
        # (frontOfHouse/backOfHouse, scope=any, no authContext) may only target
        # an unclaimed identity -- the walk-in-registration beat this op exists
        # for, which by construction targets an identity CreateUnclaimedIdentity
        # just minted. Once claimed, the PII belongs to an already-onboarded
        # person, and an unscoped standing-grant write over it is exactly the
        # over-broad reach the design flagged.
        #
        # Binds the STANDING path only -- exactly F4's require_workplace:
        # op.authTargetValidated is true only when step 3 CHECKED the target
        # (scope=self proved target == actor; a task grant proved the grant's
        # scopedTo == authContext.target), which is the scope=self or
        # task-scoped submission this confinement deliberately lets through
        # (e.g. lease-signing's onboarding userTask, assignee == the applicant
        # identity itself, patterns.go) -- already bound by its own narrower
        # grant, and legitimately targeting a claimed identity: an applicant
        # recording their own PII mid-application is not the walk-in front-desk
        # case this confinement is about. It is NOT keyed on target PRESENCE:
        # step 3 authorizes a scope=ANY grant without inspecting
        # authContext.target and the Gateway forwards it verbatim, so a
        # standing front-desk actor could otherwise attach any target it liked
        # and skip the confinement entirely.
        #
        # The validated target must also NAME this identity. A validated target
        # proves step 3 checked it, NOT that it is the identity being written:
        # the grant's scopedTo (authContext.target) and payload.identityKey are
        # independent client fields, and this op carries no ownership probe that
        # would bind them downstream. Without the equality, the applicant
        # holding a legitimate onboarding grant over their OWN identity could
        # record PII onto anyone else's -- an escalation from a consumer who
        # holds no standing RecordIdentityPII grant at all. Root (operator) is
        # exempt, mirroring every other confinement guard in the platform.
        # authcontext-target: (resource-bind) the VALIDATED target must name the
        # identity this op writes PII onto.
        resource_bound = op.authTargetValidated and op.authContextTarget == identity_key
        if not resource_bound and current_state != "unclaimed" and not actor_holds_operator(op.actor):
            fail("AuthDenied: " + op.actor + " may only RecordIdentityPII on an unclaimed identity (walk-in registration); state=" + str(current_state))

        # SSN: 9 digits; any hyphens are accepted and stripped regardless of
        # position; any other character is rejected. Stored normalized (digits
        # only). Format gate only — SSN allocation rules (area/group/serial) are
        # out of scope (the bgcheck externalTask, not this op, verifies the
        # identity).
        raw_ssn = p.ssn if hasattr(p, "ssn") else None
        if raw_ssn == None or type(raw_ssn) != type("") or len(raw_ssn) == 0:
            fail("InvalidArgument: ssn: required")
        ssn_digits = ""
        for ch in raw_ssn.elems():
            if ch >= "0" and ch <= "9":
                ssn_digits += ch
            elif ch == "-":
                continue
            else:
                fail("InvalidArgument: ssn: must be 9 digits")
        if len(ssn_digits) != 9:
            fail("InvalidArgument: ssn: must be 9 digits")

        # DOB: ISO YYYY-MM-DD. Two gates: (1) string-shape (length 10, '-' at
        # positions 4 and 7, the rest digits), then (2) a real calendar date —
        # month 1..12, day within the month's length, Feb 29 only in leap years.
        # The deterministic Starlark sandbox has no clock, so the date is NOT
        # bounded against "today" (no future-date / age check here). Stored
        # verbatim.
        dob = p.dob if hasattr(p, "dob") else None
        if dob == None or type(dob) != type("") or len(dob) != 10:
            fail("InvalidArgument: dob: must be ISO YYYY-MM-DD")
        dob_chars = dob.elems()
        idx = 0
        for ch in dob_chars:
            if idx == 4 or idx == 7:
                if ch != "-":
                    fail("InvalidArgument: dob: must be ISO YYYY-MM-DD")
            elif ch < "0" or ch > "9":
                fail("InvalidArgument: dob: must be ISO YYYY-MM-DD")
            idx += 1

        year = int(dob[0:4])
        month = int(dob[5:7])
        day = int(dob[8:10])
        if year < 1:
            fail("InvalidArgument: dob: year out of range")
        if month < 1 or month > 12:
            fail("InvalidArgument: dob: month out of range")
        days_in_month = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
        max_day = days_in_month[month - 1]
        is_leap = (year % 4 == 0 and year % 100 != 0) or (year % 400 == 0)
        if month == 2 and is_leap:
            max_day = 29
        if day < 1 or day > max_day:
            fail("InvalidArgument: dob: day out of range for month")

        # Write the PII as sensitive aspects on the identity. class MUST be
        # ssn/dob so the step-6 validator's Lookup(class) resolves the sensitive
        # aspect-type DDL and anchors the aspect to the identity. The identity
        # vertex root is NOT mutated (D5: PII lives in aspects, not vertex root).
        mutations = [
            {"op": "create", "key": identity_key + ".ssn",
             "document": {"class": "ssn", "vertexKey": identity_key, "localName": "ssn",
                          "isDeleted": False, "data": {"value": ssn_digits}}},
            {"op": "create", "key": identity_key + ".dob",
             "document": {"class": "dob", "vertexKey": identity_key, "localName": "dob",
                          "isDeleted": False, "data": {"value": dob}}},
        ]

        # The event carries only the identity key — no SSN/DOB plaintext (events
        # are not sensitive-aspect-scoped; PII stays in the anchored aspects).
        events = [{"class": "identity.piiRecorded", "data": {
            "identityKey": identity_key,
        }}]

        return {
            "mutations": mutations,
            "events": events,
            "response": {"primaryKey": identity_key},
        }

    fail("identity DDL: unknown operationType: " + ot)
`
