package identitydomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// IdentityIndexHintBucket is the package-owned NATS-KV read model the
// identityIndexHint lens projects into — the P5-clean query surface the
// Gateway's provision-time probe reads directly (whoami `?probe=1`,
// multi-credential-identity-linking-design.md §3.4), instead of routing a
// read-derived signal through an operation's synchronous reply (Contract #2
// §2.7's closed `response` schema permits only `primaryKey` and forbids any
// other script-returned data — verified at build time; see the design doc's
// §3.4 build-note).
const IdentityIndexHintBucket = "identity-index-hint"

// Lenses returns the package's Lens declarations.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "identityIndexHint",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        IdentityIndexHintBucket,
			Engine:        "full",
			Spec:          identityIndexHintSpec,
		},
		{
			// identityCredentialsRead — the protected Postgres read model an
			// identity reads to see its own bound credentials (the "manage
			// sign-in methods" account page, multi-credential-identity-
			// linking-design.md §3/§8). credentialBinding is a SENSITIVE
			// aspect (Contract #3 §3.10), so it can only ever be read through
			// the Secure-Lens decrypt-at-projection primitive — mirroring
			// clinicPatientsRead's email/phone columns exactly, just
			// self-anchored instead of staff-wildcard-anchored: the row's
			// authz_anchors is the identity's OWN NanoID, so only the
			// identity itself (RLS via lattice.actor_id) ever sees its row.
			// The whole decrypted credentialBinding object (actorKey /
			// boundAt / credentials[]) projects into one jsonb column
			// (Field left empty — there is no single scalar field to select,
			// unlike email/phone's Field:"value") since a bound-credential
			// list is a variable-length array that only exists inside the
			// ciphertext; the app-side handler reads `credentials` (falling
			// back to the singular actorKey/boundAt per the DDL's own
			// pre-Fire-2-record note) rather than a second lens per entry.
			CanonicalName: "identityCredentialsRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_identity_credentials",
			Engine:        "full",
			Spec:          identityCredentialsReadSpec,
			Protected:     true,
			IntoKey:       []string{"identity_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "identity_key", Type: "text"},
				{Name: "binding", Type: "jsonb"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "binding", IdentityKeyColumn: "identity_key"},
			},
		},
	}
}

// identityCredentialsReadSpec is self-anchored (mirrors clinicAppointmentsReadSpec's
// per-patient anchor): authz_anchors is the identity's own NanoID, so only the
// identity — resolved via its claimed/linked credential — ever reads this row.
// An identity that never claimed (no credentialBinding aspect at all) projects no
// row, same fail-closed absence as every other REQUIRED-walk protected lens here.
const identityCredentialsReadSpec = `MATCH (u:identity)
WHERE u.credentialBinding.data <> null
RETURN
  nanoIdFromKey(u.key)   AS identity_id,
  u.key                  AS entity_key,
  u.key                  AS identity_key,
  u.credentialBinding.data AS binding,
  [nanoIdFromKey(u.key)] AS authz_anchors`

// identityIndexHintSpec projects one row per live identityindex vertex,
// keyed by its own derived-hash key (the IntoKey default, `["key"]`) — the
// same existence + identityKey the dedup scripts already read in-graph
// (packages/identity-domain/ddls.go's CreateUnclaimedIdentity dedup check),
// now available P5-clean outside a write-path declared read. No PII: the
// index key is already a one-way hash (`sha256NanoID("email:"+email)`, etc)
// and the projected row carries only the identity key it resolves to.
const identityIndexHintSpec = `MATCH (n:identityindex)
RETURN n.key AS key,
       n.data.identityKey AS identityKey,
       n.data.contactType AS contactType`
