package bootstrap

// Story 4.1 — Identity Domain DDL & State Machine.
//
// Story 4.6 walk-back trimmed this surface dramatically. The remaining
// in-core identity DDL is the minimal kernel:
//   - permittedCommands: [CreateUnclaimedIdentity, UpdateIdentityState,
//     ClaimIdentity]
//   - 3-state machine: unclaimed → claimed → merged
//   - flagged-for-review state DELETED entirely
//   - MergeIdentity / ScanIdentityDuplicates / ApproveIdentityMerge /
//     FlagIdentityForReview / TombstoneIdentity all DELETED from the
//     DDL's permittedCommands and their Starlark branches removed
//
// The deleted ops are reborn as the `identity-hygiene` Capability
// Package (packages/identity-hygiene/), which provides `MergeIdentity`
// + the `duplicateCandidates` Lens.
//
// The `merged` state remains in core because `enforce_not_merged` is a
// core data-integrity invariant. The hygiene package's MergeIdentity
// script is the only writer that transitions to merged; without the
// package installed nothing can reach that state, but if something
// somehow did, the guard correctly rejects further ops.
//
// System identities (`identity.system.bootstrap`, `identity.system.platform`)
// and AI actors (`identity.ai`) are NOT governed by this DDL — they live
// outside the consumer state-machine envelope.

// IdentityDDLEntry mirrors RoleMgmtDDLEntry's shape for the identity DDL
// meta-vertex.
type IdentityDDLEntry struct {
	Key               string
	CanonicalName     string
	Class             string
	Kind              string
	PermittedCommands []string
	Description       string
	Script            string
}

// IdentityDDL returns the single identity DDL meta-vertex definition.
func IdentityDDL() IdentityDDLEntry {
	return IdentityDDLEntry{
		Key:           DDLIdentityKey,
		CanonicalName: "identity",
		Class:         "meta.ddl.vertexType",
		Kind:          "vertexType",
		PermittedCommands: []string{
			"CreateUnclaimedIdentity",
			"UpdateIdentityState",
			"ClaimIdentity",
		},
		Description: "Identity domain DDL (Story 4.6 trimmed). Vertex shape: vtx.identity.<NanoID>, class=identity. " +
			"Aspects: name (sensitive, required, maxLen 200), email (sensitive, lowercase-normalized), " +
			"phone (sensitive, E.164-normalized), state (enum: unclaimed|claimed|merged), " +
			"claimKey (sensitive, one-time-use; null after claim), credentialBinding (sensitive; null pre-claim), " +
			"mergedInto (vertex-key reference, set only by identity-hygiene package's MergeIdentity). " +
			"State machine + IdentityMerged guard enforced in .script. " +
			"MergeIdentity + duplicate-detection lens ship in the identity-hygiene Capability Package.",
		Script: identityDDLScript,
	}
}

// IdentityPermEntry mirrors RoleMgmtPermEntry — one per-op permission
// vertex seeded at bootstrap for the identity domain.
type IdentityPermEntry struct {
	Key           string
	ID            string
	OperationType string
	Scope         string
	Note          string
}

// IdentityPermissions returns the identity-domain permission vertices.
//
// Story 4.6 walk-back: the 4.1 surface (5 perms covering Create, Claim,
// Flag, ApproveIdentityMerge, ScanIdentityDuplicates) is trimmed to 2
// — Create + Claim. MergeIdentity ships as part of the identity-hygiene
// Capability Package (which seeds its own permission + grant link).
//
// Note on `scope: "self"` for ClaimIdentity: Phase 1 platformPermissions[]
// match is exact-operationType only; scope enforcement happens at the
// Starlark layer of the claim op itself (Story 4.3), not in 4.1.
func IdentityPermissions() []IdentityPermEntry {
	return []IdentityPermEntry{
		{
			Key: PermCreateUnclaimedIdentityKey, ID: PermCreateUnclaimedIdentityID,
			OperationType: "CreateUnclaimedIdentity", Scope: "any",
			Note: "Grants the holder the right to create an unclaimed identity vertex.",
		},
		{
			Key: PermClaimIdentityKey, ID: PermClaimIdentityID,
			OperationType: "ClaimIdentity", Scope: "self",
			Note: "Grants the holder the right to claim an identity. Scope=self is enforced " +
				"in the Story 4.3 ClaimIdentity script branch (actor == target check).",
		},
	}
}

// IdentityGrantSpec captures one grantsPermission link (permission → role).
type IdentityGrantSpec struct {
	PermID string
	RoleID string
}

// IdentityGrants returns the grantsPermission link specs for the
// identity domain.
//
// Story 4.6 walk-back: the 4.1 surface (10 grants) is trimmed to 4 — the
// Create grants (front/back/operator) and the Claim grant (consumer).
// MergeIdentity's grant ships in the identity-hygiene Capability Package.
//
// Grant matrix:
//
//	CreateUnclaimedIdentity → frontOfHouse, backOfHouse, operator (3)
//	ClaimIdentity           → consumer (1)
func IdentityGrants() []IdentityGrantSpec {
	return []IdentityGrantSpec{
		{PermCreateUnclaimedIdentityID, RoleFrontOfHouseID},
		{PermCreateUnclaimedIdentityID, RoleBackOfHouseID},
		{PermCreateUnclaimedIdentityID, RoleOperatorID},
		{PermClaimIdentityID, RoleConsumerID},
	}
}

// --- Starlark script ---

// identityDDLScript implements:
//   - state-machine validation for UpdateIdentityState
//   - IdentityMerged guard (any mutation against state=="merged" is rejected)
//   - 7 stub branches for 4.2-4.5 operations returning NotYetImplemented
//
// Sandbox: no I/O, no time, no os, no load; globals: state, op, ddl, nanoid.
//
// Error encoding: the Starlark sandbox only exposes `fail()`. The Processor
// surfaces fail() messages as ScriptError{Code: "ScriptError", Message: <text>}.
// Stories 4.x callers and tests inspect the Message for the structured prefix
// (e.g. "InvalidStateTransition: unclaimed -> merged"). The first colon-
// separated token IS the semantic error code.
//
// State-machine transitions (Story 4.6 walk-back — 3 states, 1 core
// transition via UpdateIdentityState):
//
//	unclaimed -> claimed       (via UpdateIdentityState)
//
// The `merged` state is set only by the identity-hygiene Capability
// Package's MergeIdentity script — UpdateIdentityState rejects any
// transition targeting `merged`. All other transitions (including
// same-state) raise InvalidStateTransition.
const identityDDLScript = `
def make_update(key, data):
    return {"op": "update", "key": key, "document": {"isDeleted": False, "data": data}}

def read_state(state, identity_key):
    """Return current state aspect value or None if not hydrated."""
    aspect_key = identity_key + ".state"
    if aspect_key in state:
        doc = state[aspect_key]
        if doc.data != None and "value" in doc.data:
            return doc.data["value"]
    return None

def read_merged_into(state, identity_key):
    """Return mergedInto aspect value or None if not hydrated/null."""
    aspect_key = identity_key + ".mergedInto"
    if aspect_key in state:
        doc = state[aspect_key]
        if doc.data != None and "value" in doc.data:
            return doc.data["value"]
    return None

def enforce_not_merged(current_state, merged_into):
    """Reject mutations against a merged identity. Per Decision #3:
    short-circuits on None so system/AI classes (which lack a state
    aspect) are not blocked."""
    if current_state == "merged":
        fail("IdentityMerged: mergedInto=" + (merged_into if merged_into != None else "<unknown>"))

def validate_state_transition(current, new):
    """Reject disallowed transitions. Same-state re-entry is rejected.
    Story 4.6 walk-back: only unclaimed->claimed is allowed via this
    op. The merged state is set by the identity-hygiene package's
    MergeIdentity script (which does NOT call this helper)."""
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
        events = [{"class": "IdentityStateChanged", "data": {
            "identityKey": identity_key,
            "oldState": current,
            "newState": new_state,
        }}]
        return {"mutations": mutations, "events": events}

    if ot == "CreateUnclaimedIdentity":
        # --- Input validation ---
        name = p.name if hasattr(p, "name") else None
        if name == None or type(name) != type("") or len(name.strip()) == 0:
            fail("InvalidArgument: name: required, maxLen 200")
        name = name.strip()
        if len(name) > 200:
            fail("InvalidArgument: name: required, maxLen 200")

        raw_email = p.email if hasattr(p, "email") else None
        raw_phone = p.phone if hasattr(p, "phone") else None

        # Normalize email: trim + lowercase.
        email = None
        if raw_email != None and type(raw_email) == type(""):
            e = raw_email.strip().lower()
            if len(e) > 0:
                email = e

        # Normalize phone: strip non-digit / non-+.
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

        # --- Duplicate detection via index vertices ---
        # Index keys use crypto.sha256NanoID to produce valid Contract #1
        # NanoID-alphabet keys (substrate.ClassifyKey requires this).
        # Contact-type prefix ("email:" / "phone:") prevents cross-type collision.
        # The caller pre-computes these keys in contextHint.reads (Decision #6).
        # If caller omitted them, state lookup returns None → no duplicate flag
        # (best-effort Phase 1; Story 4.4 batch is the safety net).
        duplicate = False
        if email != None:
            email_index_key = "vtx.identityindex." + crypto.sha256NanoID("email:" + email)
            email_hit = state[email_index_key] if email_index_key in state else None
            if email_hit != None and (not hasattr(email_hit, "isDeleted") or not email_hit.isDeleted):
                duplicate = True
        if phone != None:
            phone_index_key = "vtx.identityindex." + crypto.sha256NanoID("phone:" + phone)
            phone_hit = state[phone_index_key] if phone_index_key in state else None
            if phone_hit != None and (not hasattr(phone_hit, "isDeleted") or not phone_hit.isDeleted):
                duplicate = True

        # --- Generate identity key + claim key (call order matters: counter advances) ---
        # First nanoid.new() → identity_id; second → claim_key_plaintext.
        identity_id = nanoid.new()
        identity_key = "vtx.identity." + identity_id
        claim_key_plaintext = nanoid.new()
        claim_key_hash = crypto.sha256(claim_key_plaintext)

        # Story 4.6 walk-back: flagged-for-review state retired. Initial
        # state is always "unclaimed"; the identity-hygiene package's
        # duplicateCandidates Lens projects candidate pairs regardless of
        # state, so no per-vertex flag is needed.
        initial_state = "unclaimed"

        # --- Build MutationBatch ---
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
                          "isDeleted": False, "data": {"hash": claim_key_hash, "algo": "sha256"}}},
        ]
        if email != None:
            mutations.append({"op": "create", "key": identity_key + ".email",
                "document": {"class": "email", "vertexKey": identity_key, "localName": "email",
                             "isDeleted": False, "data": {"value": email}}})
            # Only create the index vertex if it doesn't already exist.
            # If it exists (duplicate detected via state read), skip creation to
            # avoid AtomicBatch CreateOnly conflict on the already-existing index entry.
            if email_index_key not in state:
                mutations.append({"op": "create", "key": email_index_key,
                    "document": {"class": "identityindex", "isDeleted": False,
                                 "data": {"contactType": "email", "identityKey": identity_key}}})
        if phone != None:
            mutations.append({"op": "create", "key": identity_key + ".phone",
                "document": {"class": "phone", "vertexKey": identity_key, "localName": "phone",
                             "isDeleted": False, "data": {"value": phone}}})
            # Only create if not already existing.
            if phone_index_key not in state:
                mutations.append({"op": "create", "key": phone_index_key,
                    "document": {"class": "identityindex", "isDeleted": False,
                                 "data": {"contactType": "phone", "identityKey": identity_key}}})

        # --- EventList ---
        events = [{"class": "IdentityCreated", "data": {
            "identityKey": identity_key,
            "state": initial_state,
            "duplicate": duplicate,
        }}]
        # Story 4.6 walk-back: IdentityFlaggedForReview event retired.
        # Duplicate detection is now lens-projected; consumers read the
        # duplicate-candidates KV bucket directly.

        # --- Response (plaintext claim key delivered to caller out-of-band) ---
        # NFR-S6: claimKey plaintext appears ONLY here in the response.
        # The .claimKey aspect stores the SHA-256 hash only.
        return {
            "mutations": mutations,
            "events": events,
            "response": {
                "identityKey": identity_key,
                "claimKey": claim_key_plaintext,
                "possibleDuplicateFlag": duplicate,
            },
        }

    if ot == "ClaimIdentity":
        # --- Story 4.3: Two-Phase Identity Claim (FR2, FR5) ---
        #
        # NFR-S6 anti-enumeration: every failure returns the same generic
        # "ClaimKeyInvalid: <outcome>" message. The <outcome> is parsed by
        # classifyStarlarkError in Go and emitted to Health KV only;
        # callers see ErrCodeClaimKeyInvalid with no detail.
        #
        # Decision #10: scope=self for ClaimIdentity is realized as
        # one-credential-one-identity (credentialindex) not actor==target.
        # Decision #11: enforce_not_merged is NOT used; merged state is
        # conflated into the generic error path to avoid leaking mergedInto.

        def fail_claim(outcome):
            fail("ClaimKeyInvalid: " + outcome)

        # --- Input validation ---
        claim_key_plaintext = p.claimKey if hasattr(p, "claimKey") else None
        if claim_key_plaintext == None or type(claim_key_plaintext) != type("") or len(claim_key_plaintext) == 0:
            fail_claim("invalid-key")

        target_identity_key = p.targetIdentityKey if hasattr(p, "targetIdentityKey") else None
        if target_identity_key == None or type(target_identity_key) != type("") or len(target_identity_key) == 0:
            fail_claim("no-target")
        if not target_identity_key.startswith("vtx.identity."):
            fail_claim("no-target")

        # --- Read target identity vertex ---
        target_vtx = state[target_identity_key] if target_identity_key in state else None
        if target_vtx == None or (hasattr(target_vtx, "isDeleted") and target_vtx.isDeleted):
            fail_claim("no-target")

        # --- Read target identity state aspect ---
        state_aspect_key = target_identity_key + ".state"
        state_aspect = state[state_aspect_key] if state_aspect_key in state else None
        if state_aspect == None:
            fail_claim("no-target")
        current_state = state_aspect.data["value"] if state_aspect.data != None and "value" in state_aspect.data else None
        if current_state == None:
            fail_claim("no-target")

        # State check before claimKey check (Decision #8: re-attempt on claimed
        # identity yields wrong-state, not invalid-key — both are generic to caller).
        if current_state == "claimed":
            fail_claim("wrong-state")
        if current_state == "flagged-for-review":
            fail_claim("flagged")
        if current_state == "merged":
            # Do NOT call enforce_not_merged (would leak mergedInto — NFR-S6).
            fail_claim("merged")
        # Any other non-unclaimed state is also wrong-state.
        if current_state != "unclaimed":
            fail_claim("wrong-state")

        # --- Check credentialindex (scope=self: one credential per identity) ---
        actor_key = op.actor
        cred_index_key = "vtx.credentialindex." + crypto.sha256NanoID(actor_key)
        cred_index = state[cred_index_key] if cred_index_key in state else None
        if cred_index != None and not (hasattr(cred_index, "isDeleted") and cred_index.isDeleted):
            fail_claim("credential-already-bound")

        # --- Validate claim key ---
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

        # --- Build MutationBatch (success path) ---
        observed_at = op.submittedAt

        mutations = [
            # credentialBinding aspect
            {"op": "create", "key": target_identity_key + ".credentialBinding",
             "document": {"class": "credentialBinding", "vertexKey": target_identity_key,
                          "localName": "credentialBinding", "isDeleted": False,
                          "data": {"actorKey": actor_key, "boundAt": observed_at}}},
            # state transition: unclaimed → claimed
            {"op": "update", "key": target_identity_key + ".state",
             "document": {"class": "state", "vertexKey": target_identity_key,
                          "localName": "state", "isDeleted": False,
                          "data": {"value": "claimed"}}},
            # claimKey tombstone (one-time-use via isDeleted: true)
            {"op": "tombstone", "key": target_identity_key + ".claimKey"},
            # credentialindex vertex (one-credential-one-identity enforcement)
            {"op": "create", "key": cred_index_key,
             "document": {"class": "credentialindex", "isDeleted": False,
                          "data": {"actorKey": actor_key,
                                   "identityKey": target_identity_key,
                                   "boundAt": observed_at}}},
        ]

        # --- EventList ---
        events = [{"class": "IdentityClaimed", "data": {
            "identityKey": target_identity_key,
            "actorKey": actor_key,
            # NFR-S7: do NOT include claimKey plaintext in events.
        }}]

        # --- Response (identityKey only; no sensitive tokens) ---
        return {
            "mutations": mutations,
            "events": events,
            "response": {"identityKey": target_identity_key},
        }
    fail("identity DDL: unknown operationType: " + ot)
`
