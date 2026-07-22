package identitydomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// actorRevocationDDL is the canonical name of the DDL handling the two
// event-only Gateway kill-switch ops (gateway-token-revocation-activation-
// design.md §2.1).
const actorRevocationDDL = "actorRevocation"

// RevocationDDL returns the DDL meta-vertex declaration for RevokeActor /
// UnrevokeActor. Each op is EVENT-ONLY: it produces NO business mutation —
// revocation is operational security state, not graph state (modelling it as
// an identity aspect would need a projector and a retraction path on
// unrevoke, the over-grant risk a dropped composite key carries). The script
// returns an empty `mutations` list and a single `events` entry; the
// Processor commits a tracker-only atomic batch and the outbox publishes the
// event, which the Gateway's own materializer consumes into its local
// token-revocation KV bucket (internal/gateway.StartRevocationMaterializer) —
// the same event-only-op → outbox → component-materializes-its-own-state
// loop the Loom lifecycle ops already run (packages/orchestration-base).
func RevocationDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     actorRevocationDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"RevokeActor", "UnrevokeActor"},
		Description: "Event-only Gateway token-revocation kill-switch ops. RevokeActor{actor, reason?} emits " +
			"gateway.actorRevoked{actor,at,by,reason}; UnrevokeActor{actor} emits " +
			"gateway.actorUnrevoked{actor,at,by}. Neither writes Core KV — the Gateway's own durable " +
			"events.gateway.> consumer materializes the token-revocation KV bucket the read-path " +
			"revocation.Checker consults per request. `by` is the submitting actor (op.actor); `at` is the " +
			"commit timestamp — together they make the kill-switch auditable (who revoked whom, when).",
		Script: actorRevocationScript,
		InputSchema: `{"type":"object","properties":` +
			`{"actor":{"type":"string","description":"vtx.identity.<NanoID> of the actor to revoke/unrevoke."},` +
			`"reason":{"type":"string","description":"Optional human-readable reason (RevokeActor only)."}},` +
			`"required":["actor"],"additionalProperties":false}`,
		OutputSchema: `{"type":"object","properties":{}}`,
		FieldDescription: map[string]string{
			"actor":  "Full vtx.identity.<NanoID> key of the actor to revoke or unrevoke.",
			"reason": "Optional human-readable reason the actor was revoked (RevokeActor only).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "RevokeActor — cut off a compromised actor",
				Payload: map[string]any{"actor": "vtx.identity.<NanoID>", "reason": "reported-compromised"},
				ExpectedOutcome: "Commits a tracker-only atomic batch (no mutation) and emits " +
					"events.gateway.actorRevoked{actor,at,by,reason}. The Gateway's materializer puts the " +
					"actor key into token-revocation; the next request bearing that actor's token is refused (403).",
			},
			{
				Name:    "UnrevokeActor — reverse a revocation",
				Payload: map[string]any{"actor": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Emits events.gateway.actorUnrevoked{actor,at,by}. The Gateway's materializer " +
					"deletes the actor key from token-revocation; the actor's (still-unexpired) token is accepted again.",
			},
		},
	}
}

// actorRevocationScript handles the two event-only kill-switch ops. Each
// branch returns an empty mutations list and a single event.
const actorRevocationScript = `
# NANOID_ALPHABET mirrors internal/substrate/nanoid.go's Alphabet constant —
# Starlark has no cross-language import, so the 58-char set (A-Z, a-z, 0-9
# minus the visually ambiguous I, l, O, 0) is duplicated here deliberately.
NANOID_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

def required_actor(p):
    if not hasattr(p, "actor"):
        fail("InvalidArgument: actor: required")
    v = p.actor
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: actor: required non-empty string")
    v = v.strip()
    if not v.startswith("vtx.identity."):
        fail("InvalidArgument: actor: must be a vtx.identity.<NanoID> key")
    id_part = v[len("vtx.identity."):]
    if len(id_part) != 20:
        fail("InvalidArgument: actor: must be a vtx.identity.<NanoID> key (20-char id)")
    for ch in id_part.elems():
        if ch not in NANOID_ALPHABET:
            fail("InvalidArgument: actor: must be a vtx.identity.<NanoID> key (invalid character in id)")
    return v

def optional_reason(p):
    if not hasattr(p, "reason"):
        return ""
    v = p.reason
    if v == None or type(v) != type(""):
        return ""
    return v.strip()

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RevokeActor":
        actor = required_actor(p)
        reason = optional_reason(p)
        events = [{"class": "gateway.actorRevoked",
                   "data": {"actor": actor, "at": op.submittedAt, "by": op.actor, "reason": reason}}]
        return {"mutations": [], "events": events}

    if ot == "UnrevokeActor":
        actor = required_actor(p)
        events = [{"class": "gateway.actorUnrevoked",
                   "data": {"actor": actor, "at": op.submittedAt, "by": op.actor}}]
        return {"mutations": [], "events": events}

    fail("actorRevocation DDL: unknown operationType: " + ot)
`

// ActorRevokedEventDDL registers the gateway.actorRevoked event type
// (Contract #3 §3.4). Declaration is not required for emission (the §3.4/§3.8
// validator is currently a no-op, tracked separately as
// step6-batch-internal-consistency-decision) but is zero-cost and
// self-documents the events.gateway.> family for the Gateway's materializer
// and any future Chronicler history lens.
func ActorRevokedEventDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "gateway.actorRevoked",
		Class:         "meta.ddl.eventType",
		Description: "Emitted by RevokeActor once the revocation op has committed. Consumed by the Gateway's " +
			"own events.gateway.> durable consumer, which puts the actor key into its local token-revocation " +
			"KV bucket — the per-request kill-switch revocation.Checker reads.",
		Script: revocationEventDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"actor":{"type":"string"},"at":{"type":"string"},"by":{"type":"string"},"reason":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"actor":  "The revoked actor's vtx.identity.<NanoID> key.",
			"at":     "Commit timestamp.",
			"by":     "The submitting actor (op.actor) — who revoked.",
			"reason": "Optional human-readable reason.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "gateway.actorRevoked",
				Payload:         map[string]any{"actor": "vtx.identity.<NanoID>", "at": "2026-07-03T00:00:00Z", "by": "vtx.identity.<operatorNanoID>", "reason": "reported-compromised"},
				ExpectedOutcome: "Folded by the Gateway's materializer into token-revocation; the actor's next request is refused (403).",
			},
		},
	}
}

// ActorUnrevokedEventDDL registers the gateway.actorUnrevoked event type.
func ActorUnrevokedEventDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName: "gateway.actorUnrevoked",
		Class:         "meta.ddl.eventType",
		Description: "Emitted by UnrevokeActor once the reversal op has committed. Consumed by the Gateway's " +
			"own events.gateway.> durable consumer, which deletes the actor key from its local " +
			"token-revocation KV bucket.",
		Script: revocationEventDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"actor":{"type":"string"},"at":{"type":"string"},"by":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"actor": "The un-revoked actor's vtx.identity.<NanoID> key.",
			"at":    "Commit timestamp.",
			"by":    "The submitting actor (op.actor) — who reversed the revocation.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "gateway.actorUnrevoked",
				Payload:         map[string]any{"actor": "vtx.identity.<NanoID>", "at": "2026-07-03T00:00:00Z", "by": "vtx.identity.<operatorNanoID>"},
				ExpectedOutcome: "Folded by the Gateway's materializer, which deletes the actor's token-revocation key.",
			},
		},
	}
}

// revocationEventDDLScript is the declaration-only Starlark shared by both
// event-type DDLs — mirrors sensitiveAspectDDLScript's fail-closed stub.
const revocationEventDDLScript = `
def execute(state, op):
    fail("event-type DDL: not an operation handler: " + op.operationType)
`

// RevocationPermissions grants RevokeActor/UnrevokeActor to the operator role
// at scope:any — a platform kill-switch is not self-scoped. This covers
// Loupe's operator actor (the F11 revoke surface) and any admin operator.
func RevocationPermissions() []pkgmgr.PermissionSpec {
	return []pkgmgr.PermissionSpec{
		{
			OperationType: "RevokeActor",
			Scope:         "any",
			Note:          "Grants the right to revoke an actor's tokens via the Gateway kill-switch (security-critical; not self-scoped).",
			GrantsTo:      []string{"operator"},
		},
		{
			OperationType: "UnrevokeActor",
			Scope:         "any",
			Note:          "Grants the right to reverse a token revocation.",
			GrantsTo:      []string{"operator"},
		},
	}
}
