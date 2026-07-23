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

// IdentityAnchorsBucket is the package-owned NATS-KV read model the
// identityAnchors lens projects into — an identity's residence/workplace
// anchors, keyed per actor (persona-worlds-design.md §10 Fire P1). The
// Gateway's whoami response reads it directly
// (internal/gateway/rolesanchors), the same P5-clean seam identityIndexHint
// already established for the provision-time probe.
const IdentityAnchorsBucket = "identity-anchors"

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
		{
			// identityAnchors — an actor-aggregate projection of the identity's
			// residence (residesIn) and workplace (worksAt) anchors, each entry
			// carrying the anchor's own container (persona-worlds-design.md §10
			// Fire P1: GET /v1/actor gains anchors[]). Own bucket
			// (identity-anchors), disjoint from capability-kv: Contract #6
			// §6.1/§6.2's key-class/shape are frozen for the auth surface, and
			// an anchor is a "who am I near," not a grant. The Gateway's whoami
			// response reads this directly (internal/gateway/rolesanchors), the
			// same P5-clean seam identityIndexHint established for the
			// provision-time probe. RealnessFilter mirrors capabilityRoles'
			// (rbac-domain/lenses.go): the OPTIONAL MATCH pair yields a
			// degenerate {key:null,...} entry per relation when the identity
			// has neither a residence nor a workplace, and EmptyBehavior
			// "delete" only fires once those are filtered out.
			CanonicalName:  "identityAnchors",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         IdentityAnchorsBucket,
			Engine:         "full",
			Spec:           identityAnchorsSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: "anchors.{actorSuffix}",
				BodyColumns:      []string{"anchors"},
				EmptyBehavior:    "delete",
				RealnessFilter:   "key",
				Freshness:        "auto",
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

// identityAnchorsSpec walks the identity's residence (residesIn) and
// workplace (worksAt) anchors, each stamped with its own container —
// adapted from the me-lens anchors walk (edge-manifest/lenses.go:293-313's
// edgeIdentitySpec), keeping its null-handling/name-projection idioms
// (relation stamped as a literal; `loc`/`container`/`work`/`workContainer`
// project `.presentation.data.name` for a human label) but returning
// `identity.key AS actorKey` in place of the Personal Lens's `anchor`/`ns`
// columns, since this is an actor-aggregate lens instead
// (rbac-domain/lenses.go's capabilityRolesSpec RETURN contract). The
// OPTIONAL MATCH pair yields a degenerate {key:null,...} entry per relation
// when the identity has neither a residence nor a workplace — the expected
// shape edgeIdentitySpec's own roles/anchors collects document — and the
// lens's RealnessFilter:"key" drops those before EmptyBehavior evaluates.
const identityAnchorsSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc)
OPTIONAL MATCH (loc)-[:containedIn]->(container)
OPTIONAL MATCH (identity)-[:worksAt]->(work)
OPTIONAL MATCH (work)-[:containedIn]->(workContainer)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {key: loc.key, name: loc.presentation.data.name, container: container.key, containerName: container.presentation.data.name, relation: 'residesIn'}) +
  collect(DISTINCT {key: work.key, name: work.presentation.data.name, container: workContainer.key, containerName: workContainer.presentation.data.name, relation: 'worksAt'}) AS anchors
`
