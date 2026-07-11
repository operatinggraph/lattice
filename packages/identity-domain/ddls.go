package identitydomain

import (
	"strings"

	"github.com/asolgan/lattice/internal/pkgmgr"
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
// ContextHint read faults (HydrationMiss) before the script runs.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		{
			CanonicalName: "identity",
			Class:         "meta.ddl.vertexType",
			PermittedCommands: []string{
				"CreateUnclaimedIdentity",
				"UpdateIdentityState",
				"ClaimIdentity",
				"RecordIdentityPII",
				"ProvisionConsumerIdentity",
				"InitiateCredentialLink",
				"CompleteCredentialLink",
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
				"CompleteCredentialLink appends to — multi-credential-identity-linking-design.md §3.1), " +
				"idpBinding (sensitive; the raw iss/sub of the external IdP token an opaque-mode ActorID was " +
				"derived from — Contract #11 §3.3, written only by ProvisionConsumerIdentity, absent for a " +
				"nanoid-mode/dev-provisioned actor), " +
				"mergedInto (vertex-key reference, set only by identity-hygiene package's MergeIdentity). " +
				"The client mints the claim secret, submits only claimKeyHash; Lattice never holds the plaintext. " +
				"State machine + IdentityMerged guard enforced in .script. " +
				"ProvisionConsumerIdentity: idempotently creates a bare, already-claimed consumer identity at a " +
				"caller-supplied key (the Gateway's first-authenticated-touch auto-provisioning pre-flight) — " +
				"the deterministic ActorID a verified JWT subject maps to, not a minted key. Grants the consumer " +
				"role via a holdsRole link; optionally records IdP provenance (.idpBinding) when the token was " +
				"opaque-mode; otherwise no PII.",
			Script: identityDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"name":{"type":"string","maxLength":200,"description":"Person's display name. Required for CreateUnclaimedIdentity."},` +
				`"email":{"type":"string","description":"Email address, case-insensitive normalized. At least one of email/phone required."},` +
				`"phone":{"type":"string","description":"Phone number, E.164 digits only. At least one of email/phone required."},` +
				`"claimKeyHash":{"type":"string","description":"Lowercase hex sha256 of the client-minted claim secret (CreateUnclaimedIdentity, required). Lattice stores it verbatim; the plaintext never enters Lattice."},` +
				`"claimKeyAlgo":{"type":"string","enum":["sha256"],"description":"Hash algorithm for claimKeyHash. Optional; defaults to sha256 (the only accepted value)."},` +
				`"identityKey":{"type":"string","description":"vtx.identity.<NanoID> — target identity for UpdateIdentityState and RecordIdentityPII."},` +
				`"newState":{"type":"string","enum":["claimed"],"description":"Target state for UpdateIdentityState. Only unclaimed→claimed is permitted."},` +
				`"claimKey":{"type":"string","description":"One-time-use claim key plaintext (ClaimIdentity). Its sha256 must match the stored hash."},` +
				`"targetIdentityKey":{"type":"string","description":"vtx.identity.<NanoID> of the unclaimed identity to claim (ClaimIdentity)."},` +
				`"ssn":{"type":"string","description":"Applicant Social Security Number (RecordIdentityPII, required). 9 digits; any hyphens are accepted and stripped; stored normalized as a sensitive aspect."},` +
				`"dob":{"type":"string","description":"Applicant date of birth (RecordIdentityPII, required). ISO YYYY-MM-DD; stored as a sensitive aspect."},` +
				`"targetActorKey":{"type":"string","description":"vtx.identity.<NanoID> — the ActorID a verified JWT subject maps to (ProvisionConsumerIdentity). Caller-derived, never minted."},` +
				`"consumerRoleKey":{"type":"string","description":"vtx.role.<NanoID> of the consumer role to grant (ProvisionConsumerIdentity). Caller-resolved via pkgmgr.RoleID; validated alive before granting."},` +
				`"idpIssuer":{"type":"string","description":"Raw JWT iss claim (ProvisionConsumerIdentity, optional). Present only for an opaque-mode token (Contract #11 §3.2); written verbatim into the .idpBinding aspect."},` +
				`"idpSubject":{"type":"string","description":"Raw JWT sub claim (ProvisionConsumerIdentity, optional). Must accompany idpIssuer; written verbatim into the .idpBinding aspect."},` +
				`"linkKeyHash":{"type":"string","description":"Lowercase hex sha256 of the client-minted link secret (InitiateCredentialLink, required). Lattice stores it verbatim; the plaintext never enters Lattice."},` +
				`"linkKeyAlgo":{"type":"string","enum":["sha256"],"description":"Hash algorithm for linkKeyHash. Optional; defaults to sha256 (the only accepted value)."},` +
				`"linkKey":{"type":"string","description":"One-time-use link key plaintext (CompleteCredentialLink). Its sha256 must match the stored hash on targetIdentityKey."}}}`,
			OutputSchema: `{"type":"object","properties":` +
				`{"primaryKey":{"type":"string","description":"vtx.identity.<NanoID> of the created, claimed, or PII-recorded identity (the operation's principal key)."}}}`,
			FieldDescription: map[string]string{
				"name":              "Person's display name. Required on CreateUnclaimedIdentity. Stored as sensitive aspect.",
				"email":             "Email address. Stored lowercase-normalized. Used as a deduplication index key.",
				"phone":             "Phone number. Stored as E.164 digit string. Used as a deduplication index key.",
				"claimKeyHash":      "Lowercase hex sha256 of the client-minted claim secret. Required on CreateUnclaimedIdentity. Stored verbatim; Lattice never holds the plaintext.",
				"claimKeyAlgo":      "Hash algorithm for claimKeyHash. Optional; defaults to sha256 (the only accepted value).",
				"identityKey":       "Full vtx.identity.<NanoID> key of an existing identity vertex.",
				"newState":          "Desired state after UpdateIdentityState. State machine: unclaimed → claimed only.",
				"claimKey":          "The plaintext one-time claim key the client minted at CreateUnclaimedIdentity. Used for ClaimIdentity verification (its sha256 is compared to the stored hash).",
				"targetIdentityKey": "Full vtx.identity.<NanoID> of the unclaimed identity the calling actor wants to claim.",
				"ssn":               "Applicant SSN. Required on RecordIdentityPII. 9 digits; any hyphens are accepted and stripped; stored normalized in a sensitive vtx.identity.<NanoID>.ssn aspect.",
				"dob":               "Applicant date of birth. Required on RecordIdentityPII. ISO YYYY-MM-DD; stored in a sensitive vtx.identity.<NanoID>.dob aspect.",
				"targetActorKey":    "vtx.identity.<NanoID> ActorID to provision (ProvisionConsumerIdentity, required). Must be the exact key a verified JWT subject resolves to; rejected if not NanoID-shaped.",
				"consumerRoleKey":   "vtx.role.<NanoID> of the consumer role (ProvisionConsumerIdentity, required). Must resolve to a live role vertex; rejected otherwise.",
				"idpIssuer":         "Raw JWT iss claim (ProvisionConsumerIdentity, optional; present only for an opaque-mode token). Stored verbatim in the sensitive .idpBinding aspect.",
				"idpSubject":        "Raw JWT sub claim (ProvisionConsumerIdentity, optional; must accompany idpIssuer). Stored verbatim in the sensitive .idpBinding aspect.",
				"linkKeyHash":       "Lowercase hex sha256 of the client-minted link secret. Required on InitiateCredentialLink. Stored verbatim; Lattice never holds the plaintext.",
				"linkKeyAlgo":       "Hash algorithm for linkKeyHash. Optional; defaults to sha256 (the only accepted value).",
				"linkKey":           "The plaintext one-time link key the client minted at InitiateCredentialLink. Used for CompleteCredentialLink verification (its sha256 is compared to the stored hash).",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:    "CreateUnclaimedIdentity — new customer with email",
					Payload: map[string]any{"name": "Alice Smith", "email": "alice@example.com", "claimKeyHash": "<sha256-hex-of-client-minted-secret>"},
					ExpectedOutcome: "Creates vtx.identity.<NanoID> with class=identity, writes name/email/state/claimKey aspects " +
						"(claimKey stores the supplied hash verbatim). Returns primaryKey (the identity key). " +
						"Duplicate detection rides the IdentityCreated event's data.duplicate flag, not the reply.",
				},
				{
					Name:            "ClaimIdentity — actor claims their identity",
					Payload:         map[string]any{"targetIdentityKey": "vtx.identity.<NanoID>", "claimKey": "<plaintextKey>"},
					ExpectedOutcome: "Verifies claimKey hash, writes credentialBinding aspect, transitions state unclaimed→claimed, tombstones claimKey aspect, grants holdsRole→consumer to the claimed identity.",
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
						"wrong/spent secret, an already-bound A2, or a not-claimed U — all collapse to the same " +
						"generic ClaimKeyInvalid wire code ClaimIdentity uses (NFR-S6 anti-enumeration; the " +
						"Processor's classifier reclassifies any \"ClaimKeyInvalid: <outcome>\" fail() message the " +
						"same way regardless of which op raised it — specific outcomes surface only via Health KV).",
				},
				{
					Name:    "RecordIdentityPII — capture applicant SSN/DOB",
					Payload: map[string]any{"identityKey": "vtx.identity.<NanoID>", "ssn": "123-45-6789", "dob": "1990-01-15"},
					ExpectedOutcome: "Validates formats, writes sensitive vtx.identity.<NanoID>.ssn (normalized to 123456789) and " +
						".dob aspects onto the existing identity; the identity vertex root data is not mutated. " +
						"A sensitive ssn/dob aspect on any non-identity vertex is rejected by the step-6 sensitiveAspectScope rule.",
				},
				{
					Name:    "ProvisionConsumerIdentity — Gateway first-touch auto-provisioning",
					Payload: map[string]any{"targetActorKey": "vtx.identity.<NanoID>", "consumerRoleKey": "vtx.role.<NanoID>"},
					ExpectedOutcome: "Fresh actor: creates the identity vertex + a .state=claimed aspect + a holdsRole link to " +
						"consumerRoleKey, emits identity.provisioned, returns primaryKey=targetActorKey. Already-provisioned " +
						"actor: no-op (empty mutations/events, no response) — safe to call on every request.",
				},
				{
					Name: "ProvisionConsumerIdentity — opaque-mode first touch with IdP provenance",
					Payload: map[string]any{
						"targetActorKey": "vtx.identity.<NanoID>", "consumerRoleKey": "vtx.role.<NanoID>",
						"idpIssuer": "https://accounts.google.com", "idpSubject": "110169484474386276334",
					},
					ExpectedOutcome: "Same as the fresh-actor case, plus a sensitive .idpBinding aspect recording the raw " +
						"iss/sub (Contract #11 §3.3) — the audit answer to which IdP account this identity is.",
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
				"(Contract #11 §3.3): stored as vtx.identity.<NanoID>.idpBinding, sensitive=true, " +
				"identity-anchored, the crypto-shred unit — shredding the identity's DEK severs the " +
				"IdP-account linkage. Written only by ProvisionConsumerIdentity, and only for an opaque-mode " +
				"token (Contract #11 §3.2); a nanoid-mode/dev-provisioned actor never gets this aspect. The " +
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
				"index vertex by CreateUnclaimedIdentity; repointed (tombstone + create) by " +
				"identity-hygiene's MergeIdentity; tombstoned by ShredIdentityKey. Makes merge repoint and " +
				"shred erase decrypt-free — linkage is ownership, so no plaintext lookup is needed. " +
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
				"MergeIdentity on merge, and by ShredIdentityKey on shred. permittedCommands is " +
				"intentionally empty: multi-writer, open posture.",
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
		RevocationDDL(),
		ActorRevokedEventDDL(),
		ActorUnrevokedEventDDL(),
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
// package's link-type DDLs (indexes, duplicateOf). A link-type DDL declares
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
    # dedup-over-encrypted-pii-design.md §3.5: a shredded identity's owned
    # identityindex vertices are tombstoned in-commit, so a later create for
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

def validate_state_transition(current, new):
    if current == None:
        fail("InvalidStateTransition: <missing> -> " + str(new))
    allowed = {
        "unclaimed": ["claimed"],
    }
    targets = allowed.get(current)
    if targets == None or new not in targets:
        fail("InvalidStateTransition: " + str(current) + " -> " + str(new))

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
        name = p.name if hasattr(p, "name") else None
        if name == None or type(name) != type("") or len(name.strip()) == 0:
            fail("InvalidArgument: name: required, maxLen 200")
        name = name.strip()
        if len(name) > 200:
            fail("InvalidArgument: name: required, maxLen 200")

        raw_email = p.email if hasattr(p, "email") else None
        raw_phone = p.phone if hasattr(p, "phone") else None

        email = None
        if raw_email != None and type(raw_email) == type(""):
            e = raw_email.strip().lower()
            if len(e) > 0:
                email = e

        phone = None
        if raw_phone != None and type(raw_phone) == type(""):
            stripped = ""
            for ch in raw_phone.elems():
                if ch >= "0" and ch <= "9":
                    stripped += ch
                elif ch == "+":
                    stripped += ch
            if len(stripped) > 0:
                phone = stripped

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

        normalized_name = " ".join(name.lower().split())
        name_index_key = "vtx.identityindex." + crypto.sha256NanoID("name:" + normalized_name)

        duplicate = False
        matched = {}
        if email != None:
            email_index_key = "vtx.identityindex." + crypto.sha256NanoID("email:" + email)
            email_hit = state[email_index_key] if email_index_key in state else None
            if email_hit != None and (not hasattr(email_hit, "isDeleted") or not email_hit.isDeleted):
                duplicate = True
                incumbent_key = email_hit.data["identityKey"]
                matched[incumbent_key] = (matched[incumbent_key] if incumbent_key in matched else []) + ["exact-email"]
        if phone != None:
            phone_index_key = "vtx.identityindex." + crypto.sha256NanoID("phone:" + phone)
            phone_hit = state[phone_index_key] if phone_index_key in state else None
            if phone_hit != None and (not hasattr(phone_hit, "isDeleted") or not phone_hit.isDeleted):
                duplicate = True
                incumbent_key = phone_hit.data["identityKey"]
                matched[incumbent_key] = (matched[incumbent_key] if incumbent_key in matched else []) + ["exact-phone"]
        name_hit = state[name_index_key] if name_index_key in state else None
        if name_hit != None and (not hasattr(name_hit, "isDeleted") or not name_hit.isDeleted):
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
            if email_hit == None or (hasattr(email_hit, "isDeleted") and email_hit.isDeleted):
                mutations.append(index_vertex_mutation(email_index_key, "email", identity_key, email_hit))
                mutations.append({"op": "create", "key": "lnk." + email_index_key[len("vtx."):] + ".indexes.identity." + identity_id,
                    "document": {"class": "indexes", "isDeleted": False,
                                 "sourceVertex": email_index_key, "targetVertex": identity_key,
                                 "localName": "indexes", "data": {}}})
        if phone != None:
            mutations.append({"op": "create", "key": identity_key + ".phone",
                "document": {"class": "phone", "vertexKey": identity_key, "localName": "phone",
                             "isDeleted": False, "data": {"value": phone}}})
            if phone_hit == None or (hasattr(phone_hit, "isDeleted") and phone_hit.isDeleted):
                mutations.append(index_vertex_mutation(phone_index_key, "phone", identity_key, phone_hit))
                mutations.append({"op": "create", "key": "lnk." + phone_index_key[len("vtx."):] + ".indexes.identity." + identity_id,
                    "document": {"class": "indexes", "isDeleted": False,
                                 "sourceVertex": phone_index_key, "targetVertex": identity_key,
                                 "localName": "indexes", "data": {}}})
        if name_hit == None or (hasattr(name_hit, "isDeleted") and name_hit.isDeleted):
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

        # target_actor_key legitimately may not exist yet (the fresh-actor
        # case). Unlike CreateTask, no op in this package (or anywhere) ever
        # tombstones a bare identity vertex, so there is no
        # un-tombstone-and-recreate case to self-heal here: ANY existing
        # record — live or (hypothetically) tombstoned — is
        # already-provisioned. Treating a tombstoned record as absent and
        # falling through to "create" would hit a hard RevisionConflict at
        # commit (create-only conditioning targets the NATS subject's last
        # sequence, not its logical isDeleted flag), so deliberately not
        # mirroring CreateTask's isDeleted branch here.
        # read-posture: (d) declared in contextHint.optionalReads by the
        # Gateway's provisionActorIfNeeded dispatcher (internal/gateway/gateway.go)
        existing = kv.Read(target_actor_key)
        if existing != None:
            # No "response" here: the write-path reply-constraint rejects a
            # script-named primaryKey that isn't in this op's own write
            # footprint, and an empty-mutations no-op writes nothing
            # (mirrors AssignRole / CreateTask's idempotent no-op shape).
            return {"mutations": [], "events": []}

        # Pinned to the package's OWN consumer role, not trusted from the
        # payload: the grant matrix lets identityProvisioner AND operator
        # call this op, so a caller-supplied consumerRoleKey that was merely
        # checked for "is some live role" could steer a first-touch actor
        # into ANY role (e.g. operator) instead of consumer. Equality against
        # the literal closes that — the field still exists so the caller is
        # explicit about intent, but the script is the enforcement boundary.
        consumer_role_key = p.consumerRoleKey if hasattr(p, "consumerRoleKey") else None
        if consumer_role_key != "__EXPECTED_CONSUMER_ROLE_KEY__":
            fail("InvalidArgument: consumerRoleKey: must be the identity-domain consumer role")
        # read-posture: (a) declared in contextHint.reads by the Gateway's
        # provisionActorIfNeeded dispatcher (internal/gateway/gateway.go) — a
        # pinned, always-live role vertex; absence is a wiring fault
        role_vtx = kv.Read(consumer_role_key)
        if role_vtx == None or role_vtx.isDeleted:
            fail("UnknownRole: " + consumer_role_key)
        role_id = consumer_role_key[len("vtx.role."):]

        link_key = "lnk.identity." + actor_id + ".holdsRole.role." + role_id
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

        # Optional IdP provenance (Contract #11 §3.3): present only for an
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

        claim_key_plaintext = p.claimKey if hasattr(p, "claimKey") else None
        if claim_key_plaintext == None or type(claim_key_plaintext) != type("") or len(claim_key_plaintext) == 0:
            fail_claim("invalid-key")

        target_identity_key = p.targetIdentityKey if hasattr(p, "targetIdentityKey") else None
        if target_identity_key == None or type(target_identity_key) != type("") or len(target_identity_key) == 0:
            fail_claim("no-target")
        if not target_identity_key.startswith("vtx.identity."):
            fail_claim("no-target")

        target_vtx = state[target_identity_key] if target_identity_key in state else None
        if target_vtx == None or (hasattr(target_vtx, "isDeleted") and target_vtx.isDeleted):
            fail_claim("no-target")

        state_aspect_key = target_identity_key + ".state"
        state_aspect = state[state_aspect_key] if state_aspect_key in state else None
        if state_aspect == None:
            fail_claim("no-target")
        current_state = state_aspect.data["value"] if state_aspect.data != None and "value" in state_aspect.data else None
        if current_state == None:
            fail_claim("no-target")

        if current_state == "claimed":
            fail_claim("wrong-state")
        if current_state == "flagged-for-review":
            fail_claim("flagged")
        if current_state == "merged":
            fail_claim("merged")
        if current_state != "unclaimed":
            fail_claim("wrong-state")

        actor_key = op.actor
        cred_index_key = "vtx.credentialindex." + crypto.sha256NanoID(actor_key)
        cred_index = state[cred_index_key] if cred_index_key in state else None
        if cred_index != None and not (hasattr(cred_index, "isDeleted") and cred_index.isDeleted):
            fail_claim("credential-already-bound")

        claim_key_aspect_key = target_identity_key + ".claimKey"
        claim_key_aspect = state[claim_key_aspect_key] if claim_key_aspect_key in state else None
        if claim_key_aspect == None or (hasattr(claim_key_aspect, "isDeleted") and claim_key_aspect.isDeleted):
            fail_claim("invalid-key")
        if claim_key_aspect.data == None or "hash" not in claim_key_aspect.data:
            fail_claim("invalid-key")

        submitted_hash = crypto.sha256(claim_key_plaintext)
        stored_hash = claim_key_aspect.data["hash"]
        if not crypto.constant_time_equal(submitted_hash, stored_hash):
            fail_claim("invalid-key")

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
            {"op": "create", "key": cred_index_key,
             "document": {"class": "credentialindex", "isDeleted": False,
                          "data": {"actorKey": actor_key,
                                   "identityKey": target_identity_key,
                                   "boundAt": observed_at}}},
            {"op": "create", "key": consumer_grant_key,
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

        link_key_plaintext = p.linkKey if hasattr(p, "linkKey") else None
        if link_key_plaintext == None or type(link_key_plaintext) != type("") or len(link_key_plaintext) == 0:
            fail_link("invalid-key")

        target_identity_key = p.targetIdentityKey if hasattr(p, "targetIdentityKey") else None
        if target_identity_key == None or type(target_identity_key) != type("") or len(target_identity_key) == 0:
            fail_link("no-target")
        if not target_identity_key.startswith("vtx.identity."):
            fail_link("no-target")

        target_vtx = state[target_identity_key] if target_identity_key in state else None
        if target_vtx == None or (hasattr(target_vtx, "isDeleted") and target_vtx.isDeleted):
            fail_link("no-target")

        target_state = read_state(state, target_identity_key)
        if target_state == "merged":
            fail_link("merged")
        if target_state != "claimed":
            fail_link("wrong-state")

        # The same one-credential-<=-one-identity dedup guard ClaimIdentity
        # applies (#11 §11.4): this is a declared-optionalReads guard for the
        # friendly generic error only -- the load-bearing stop is the
        # CreateOnly create of cred_index_key below, which RevisionConflicts
        # on an already-bound credential regardless of declaration (finding
        # A4, mirrored from ClaimIdentity).
        actor_key = op.actor
        cred_index_key = "vtx.credentialindex." + crypto.sha256NanoID(actor_key)
        cred_index = state[cred_index_key] if cred_index_key in state else None
        if cred_index != None and not (hasattr(cred_index, "isDeleted") and cred_index.isDeleted):
            fail_link("credential-already-bound")

        link_key_aspect_key = target_identity_key + ".linkKey"
        link_key_aspect = state[link_key_aspect_key] if link_key_aspect_key in state else None
        if link_key_aspect == None or (hasattr(link_key_aspect, "isDeleted") and link_key_aspect.isDeleted):
            fail_link("invalid-key")
        if link_key_aspect.data == None or "hash" not in link_key_aspect.data:
            fail_link("invalid-key")

        submitted_hash = crypto.sha256(link_key_plaintext)
        stored_hash = link_key_aspect.data["hash"]
        if not crypto.constant_time_equal(submitted_hash, stored_hash):
            fail_link("invalid-key")

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
            {"op": "create", "key": cred_index_key,
             "document": {"class": "credentialindex", "isDeleted": False,
                          "data": {"actorKey": actor_key,
                                   "identityKey": target_identity_key,
                                   "boundAt": observed_at}}},
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
