package identitydomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the identity-domain permission vertices.
//
// Grant matrix:
//
//	CreateUnclaimedIdentity       → frontOfHouse, backOfHouse, operator
//	UpdateIdentityState           → operator
//	ClaimIdentity (self)          → consumer
//	RotateClaimKey                → frontOfHouse, backOfHouse, operator
//	RecordIdentityPII             → frontOfHouse, backOfHouse, operator
//	ProvisionConsumerIdentity     → identityProvisioner, operator
//	InitiateCredentialLink (self) → consumer
//	CompleteCredentialLink (self) → consumer
//	UnlinkCredential (self)       → consumer
//
// Scope `self` for ClaimIdentity is enforced at step 3 (auth), before the
// script ever runs: an existence gate (the actor must already hold some
// role granting ClaimIdentity) and a self-match gate (authContext.target ==
// actor). The Starlark `ClaimIdentity` branch itself only ever does a
// negative dedup (an actor must not already be bound to a different
// identity, via credentialindex) — it never re-derives the scope check.
// InitiateCredentialLink/CompleteCredentialLink mirror the same self-scope
// idiom (multi-credential-identity-linking-design.md §3.2): Initiate is
// submitted through the normal resolved path (op.actor == U == target);
// Complete is submitted as the raw new credential A2 via the Gateway's
// raw-credential carve-out (op.actor == A2 == target) — the same carve-out
// class as ClaimIdentity, extended in internal/gateway/gateway.go.
// UnlinkCredential (§8) is NOT in the carve-out: it is submitted through the
// normal resolved path like Initiate (op.actor == U == target) — U is
// removing an entry from its own credentials array, not proving control of
// the credential being removed.
//
// S1 (Vertical Package Standard §2): ClaimIdentity and RecordIdentityPII carry
// full descriptors in opmetas.go. The other five user-facing ops carry a
// `[no-op-meta: …]` exemption in their Note below, each naming the specific
// reason a descriptor could not be honoured — a client-side minting ceremony,
// a submission as a different actor, or an input nothing projects. The
// operator- and system-only ops (UpdateIdentityState, ProvisionConsumerIdentity,
// and revocation.go's RevokeActor / UnrevokeActor) are outside S1 entirely: no
// human triggers them.
func Permissions() []pkgmgr.PermissionSpec {
	perms := []pkgmgr.PermissionSpec{
		{
			OperationType: "CreateUnclaimedIdentity",
			Scope:         "any",
			Note:          "Grants the right to create an unclaimed identity vertex.",
			GrantsTo:      []string{"frontOfHouse", "backOfHouse", "operator"},
		},
		{
			OperationType: "UpdateIdentityState",
			Scope:         "any",
			Note:          "Grants the right to advance an identity through its state machine.",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "ClaimIdentity",
			Scope:         "self",
			Note:          "Grants the right to claim an identity (scope=self via credentialindex).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "RotateClaimKey",
			Scope:         "any",
			Note:          "Grants staff the right to re-issue a lost claim secret for an unclaimed identity (R4 recovery — Lattice only ever stored the hash, never the plaintext).",
			GrantsTo:      []string{"frontOfHouse", "backOfHouse", "operator"},
		},
		{
			OperationType: "RecordIdentityPII",
			Scope:         "any",
			Note:          "Grants the right to record applicant PII (ssn/dob sensitive aspects) on an existing identity.",
			GrantsTo:      []string{"frontOfHouse", "backOfHouse", "operator"},
		},
		{
			OperationType: "ProvisionConsumerIdentity",
			Scope:         "any",
			Note:          "Grants the right to idempotently auto-provision a bare consumer identity on first authenticated touch (the Gateway's own system identity; scoped narrow rather than full operator).",
			GrantsTo:      []string{"identityProvisioner", "operator"},
		},
		{
			OperationType: "InitiateCredentialLink",
			Scope:         "self",
			Note:          "Grants the right to arm a link secret on your own already-claimed identity (scope=self). [no-op-meta: paired-code — the minting half is expressible (OpCeremonySpec), but the plaintext has to travel to a SECOND device and be typed back on the consuming op, which needs RevealWith/PairedCodeField. Without them a descriptor asks a person to hand-type an identity key and a 64-hex secret. Sequenced behind a real two-device consumer (client-ceremony-op-descriptors-design.md §4.4).]",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "CompleteCredentialLink",
			Scope:         "self",
			Note:          "Grants the right to bind a second credential to an identity by proving a link secret (scope=self via the raw new credential). [no-op-meta: raw-credential-actor — submitted as a DIFFERENT actor than the client authenticated as. The Gateway's raw-credential carve-out resolves op.actor to the new credential, so a descriptor's self authContext (which names the resolved business identity) denies at step 3; it needs the credential authContext value of §4.4. The credentialindex half is already closed by derive_reads.]",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "UnlinkCredential",
			Scope:         "self",
			Note:          "Grants the right to remove one of your own bound credentials (scope=self); the last remaining credential cannot be removed. [no-op-meta: lifecycle-op — its one input is the credential's vertex key, and bound credentials are served by a protected-lens read rather than projected as client-resolvable entities, so nothing can fill the field and a descriptor reduces to asking a person to hand-type a vtx.identity.<NanoID>. Closed by Inc 2's boundTo link and per-credential row (design §4.2).]",
			GrantsTo:      []string{"consumer"},
		},
	}
	return append(perms, RevocationPermissions()...)
}
