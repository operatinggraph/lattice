package identitydomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions returns the identity-domain permission vertices.
//
// Grant matrix:
//
//	CreateUnclaimedIdentity       → frontOfHouse, backOfHouse, operator
//	UpdateIdentityState           → operator
//	ClaimIdentity (self)          → consumer
//	RecordIdentityPII             → frontOfHouse, backOfHouse, operator
//	ProvisionConsumerIdentity     → identityProvisioner, operator
//	InitiateCredentialLink (self) → consumer
//	CompleteCredentialLink (self) → consumer
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
			Note:          "Grants the right to arm a link secret on your own already-claimed identity (scope=self).",
			GrantsTo:      []string{"consumer"},
		},
		{
			OperationType: "CompleteCredentialLink",
			Scope:         "self",
			Note:          "Grants the right to bind a second credential to an identity by proving a link secret (scope=self via the raw new credential).",
			GrantsTo:      []string{"consumer"},
		},
	}
	return append(perms, RevocationPermissions()...)
}
