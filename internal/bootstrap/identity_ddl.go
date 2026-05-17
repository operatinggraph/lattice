package bootstrap

// Story 4.1 — Identity Domain DDL & State Machine.
//
// This file defines the primordial seed surface for the user-facing
// `identity` vertex class:
//   - one DDL meta-vertex `vtx.meta.<DDLIdentityID>` with the four
//     canonical aspects (canonicalName, permittedCommands, description,
//     script);
//   - five per-op permission vertices covering all Epic-4 identity
//     operations;
//   - ten grantsPermission links projecting those permissions onto the
//     consumer / frontOfHouse / backOfHouse / operator roles per the
//     AC matrix.
//
// The Starlark script enforces (a) a state machine over the `state`
// aspect and (b) an `IdentityMerged` guard that rejects any mutation
// against an identity whose state is `merged`. 4.1 ships one real
// operation — `UpdateIdentityState` — plus stub branches for the seven
// 4.2–4.5 operations that return ScriptError "NotYetImplemented".
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
			"FlagIdentityForReview",
			"ApproveIdentityMerge",
			"MergeIdentity",
			"TombstoneIdentity",
			"ScanIdentityDuplicates",
		},
		Description: "Identity domain DDL. Vertex shape: vtx.identity.<NanoID>, class=identity. " +
			"Aspects: name (sensitive, required, maxLen 200), email (sensitive, lowercase-normalized), " +
			"phone (sensitive, E.164-normalized), state (enum: unclaimed|claimed|flagged-for-review|merged), " +
			"claimKey (sensitive, one-time-use; null after claim), credentialBinding (sensitive; null pre-claim), " +
			"mergedInto (vertex-key reference, null until merged). " +
			"State machine + IdentityMerged guard enforced in .script.",
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

// IdentityPermissions returns the 5 identity-domain permission vertices.
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
		{
			Key: PermFlagIdentityForReviewKey, ID: PermFlagIdentityForReviewID,
			OperationType: "FlagIdentityForReview", Scope: "any",
			Note: "Grants the holder the right to flag any identity for review.",
		},
		{
			Key: PermApproveIdentityMergeKey, ID: PermApproveIdentityMergeID,
			OperationType: "ApproveIdentityMerge", Scope: "any",
			Note: "Grants the holder the right to approve an identity merge.",
		},
		{
			Key: PermScanIdentityDuplicatesKey, ID: PermScanIdentityDuplicatesID,
			OperationType: "ScanIdentityDuplicates", Scope: "any",
			Note: "Grants the holder the right to invoke duplicate-scan over the identity domain.",
		},
	}
}

// IdentityGrantSpec captures one grantsPermission link (permission → role).
type IdentityGrantSpec struct {
	PermID string
	RoleID string
}

// IdentityGrants returns the 10 grantsPermission link specs for the
// identity domain (Story 4.1).
//
// Grant matrix (per Story 4.1 AC table):
//
//	CreateUnclaimedIdentity → frontOfHouse, backOfHouse, operator (3)
//	ClaimIdentity           → consumer (1)
//	FlagIdentityForReview   → frontOfHouse, backOfHouse, operator (3)
//	ApproveIdentityMerge    → operator (1)
//	ScanIdentityDuplicates  → backOfHouse, operator (2)
func IdentityGrants() []IdentityGrantSpec {
	return []IdentityGrantSpec{
		{PermCreateUnclaimedIdentityID, RoleFrontOfHouseID},
		{PermCreateUnclaimedIdentityID, RoleBackOfHouseID},
		{PermCreateUnclaimedIdentityID, RoleOperatorID},
		{PermClaimIdentityID, RoleConsumerID},
		{PermFlagIdentityForReviewID, RoleFrontOfHouseID},
		{PermFlagIdentityForReviewID, RoleBackOfHouseID},
		{PermFlagIdentityForReviewID, RoleOperatorID},
		{PermApproveIdentityMergeID, RoleOperatorID},
		{PermScanIdentityDuplicatesID, RoleBackOfHouseID},
		{PermScanIdentityDuplicatesID, RoleOperatorID},
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
// State-machine transitions (per AC):
//
//	unclaimed -> claimed
//	unclaimed -> flagged-for-review
//	claimed   -> flagged-for-review
//	flagged-for-review -> claimed
//	flagged-for-review -> merged
//
// All other transitions (including same-state, e.g. unclaimed -> unclaimed)
// raise InvalidStateTransition.
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
    """Reject disallowed transitions. Same-state re-entry is rejected."""
    if current == None:
        fail("InvalidStateTransition: <missing> -> " + str(new))
    allowed = {
        "unclaimed": ["claimed", "flagged-for-review"],
        "claimed": ["flagged-for-review"],
        "flagged-for-review": ["claimed", "merged"],
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

        initial_state = "flagged-for-review" if duplicate else "unclaimed"

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
        if duplicate:
            events.append({"class": "IdentityFlaggedForReview", "data": {
                "identityKey": identity_key,
                "reason": "duplicate-contact",
            }})

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

    # Stub branches for 4.3-4.5 operations.
    if ot == "ClaimIdentity":
        fail("NotYetImplemented: Story 4.3: ClaimIdentity")
    if ot == "FlagIdentityForReview":
        fail("NotYetImplemented: Story 4.3: FlagIdentityForReview")
    if ot == "ApproveIdentityMerge":
        fail("NotYetImplemented: Story 4.5: ApproveIdentityMerge")
    if ot == "MergeIdentity":
        fail("NotYetImplemented: Story 4.5: MergeIdentity")
    if ot == "TombstoneIdentity":
        fail("NotYetImplemented: Story 4.5: TombstoneIdentity")
    if ot == "ScanIdentityDuplicates":
        fail("NotYetImplemented: Story 4.4: ScanIdentityDuplicates")

    fail("identity DDL: unknown operationType: " + ot)
`
