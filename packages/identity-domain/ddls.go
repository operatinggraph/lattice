package identitydomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations. One DDL —
// `identity` — handles CreateUnclaimedIdentity, UpdateIdentityState,
// ClaimIdentity. State machine: unclaimed → claimed; merged is set only
// by identity-hygiene's MergeIdentity.
//
// Architectural rules: known-key reads only. The duplicate-detection
// index lookups (vtx.identityindex.*) use crypto.sha256NanoID-derived
// known keys provided by the caller in ContextHint.Reads.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		{
			CanonicalName: "identity",
			Class:         "meta.ddl.vertexType",
			PermittedCommands: []string{
				"CreateUnclaimedIdentity",
				"UpdateIdentityState",
				"ClaimIdentity",
			},
			Description: "Identity domain DDL. " +
				"Vertex shape: vtx.identity.<NanoID>, class=identity. " +
				"Aspects: name (sensitive, required, maxLen 200), email (sensitive, lowercase-normalized), " +
				"phone (sensitive, E.164-normalized), state (enum: unclaimed|claimed|merged), " +
				"claimKey (sensitive, one-time-use; null after claim), credentialBinding (sensitive; null pre-claim), " +
				"mergedInto (vertex-key reference, set only by identity-hygiene package's MergeIdentity). " +
				"State machine + IdentityMerged guard enforced in .script.",
			Script: identityDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"name":{"type":"string","maxLength":200,"description":"Person's display name. Required for CreateUnclaimedIdentity."},` +
				`"email":{"type":"string","description":"Email address, case-insensitive normalized. At least one of email/phone required."},` +
				`"phone":{"type":"string","description":"Phone number, E.164 digits only. At least one of email/phone required."},` +
				`"identityKey":{"type":"string","description":"vtx.identity.<NanoID> — target identity for UpdateIdentityState."},` +
				`"newState":{"type":"string","enum":["claimed"],"description":"Target state for UpdateIdentityState. Only unclaimed→claimed is permitted."},` +
				`"claimKey":{"type":"string","description":"One-time-use claim key plaintext (ClaimIdentity). Must match stored hash."},` +
				`"targetIdentityKey":{"type":"string","description":"vtx.identity.<NanoID> of the unclaimed identity to claim (ClaimIdentity)."}}}`,
			OutputSchema: `{"type":"object","properties":` +
				`{"identityKey":{"type":"string","description":"vtx.identity.<NanoID> of the created or claimed identity."},` +
				`"claimKey":{"type":"string","description":"Plaintext one-time claim key returned only on CreateUnclaimedIdentity."},` +
				`"possibleDuplicateFlag":{"type":"boolean","description":"True when an existing live identity with the same email or phone was found during create."}}}`,
			FieldDescription: map[string]string{
				"name":              "Person's display name. Required on CreateUnclaimedIdentity. Stored as sensitive aspect.",
				"email":             "Email address. Stored lowercase-normalized. Used as a deduplication index key.",
				"phone":             "Phone number. Stored as E.164 digit string. Used as a deduplication index key.",
				"identityKey":       "Full vtx.identity.<NanoID> key of an existing identity vertex.",
				"newState":          "Desired state after UpdateIdentityState. State machine: unclaimed → claimed only.",
				"claimKey":          "The plaintext one-time claim key issued during CreateUnclaimedIdentity. Used for ClaimIdentity verification.",
				"targetIdentityKey": "Full vtx.identity.<NanoID> of the unclaimed identity the calling actor wants to claim.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:    "CreateUnclaimedIdentity — new customer with email",
					Payload: map[string]any{"name": "Alice Smith", "email": "alice@example.com"},
					ExpectedOutcome: "Creates vtx.identity.<NanoID> with class=identity, writes name/email/state/claimKey aspects. " +
						"Returns identityKey, claimKey plaintext, possibleDuplicateFlag.",
				},
				{
					Name:    "ClaimIdentity — actor claims their identity",
					Payload: map[string]any{"targetIdentityKey": "vtx.identity.<NanoID>", "claimKey": "<plaintextKey>"},
					ExpectedOutcome: "Verifies claimKey hash, writes credentialBinding aspect, transitions state unclaimed→claimed, tombstones claimKey aspect.",
				},
			},
		},
	}
}

// identityDDLScript is the identity DDL Starlark script. State machine:
// unclaimed -> claimed. The merged state is set only by the
// identity-hygiene package's MergeIdentity script.
const identityDDLScript = `
def make_update(key, data):
    return {"op": "update", "key": key, "document": {"isDeleted": False, "data": data}}

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
        events = [{"class": "IdentityStateChanged", "data": {
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

        identity_id = nanoid.new()
        identity_key = "vtx.identity." + identity_id
        claim_key_plaintext = nanoid.new()
        claim_key_hash = crypto.sha256(claim_key_plaintext)

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
                          "isDeleted": False, "data": {"hash": claim_key_hash, "algo": "sha256"}}},
        ]
        if email != None:
            mutations.append({"op": "create", "key": identity_key + ".email",
                "document": {"class": "email", "vertexKey": identity_key, "localName": "email",
                             "isDeleted": False, "data": {"value": email}}})
            if email_index_key not in state:
                mutations.append({"op": "create", "key": email_index_key,
                    "document": {"class": "identityindex", "isDeleted": False,
                                 "data": {"contactType": "email", "identityKey": identity_key}}})
        if phone != None:
            mutations.append({"op": "create", "key": identity_key + ".phone",
                "document": {"class": "phone", "vertexKey": identity_key, "localName": "phone",
                             "isDeleted": False, "data": {"value": phone}}})
            if phone_index_key not in state:
                mutations.append({"op": "create", "key": phone_index_key,
                    "document": {"class": "identityindex", "isDeleted": False,
                                 "data": {"contactType": "phone", "identityKey": identity_key}}})

        events = [{"class": "IdentityCreated", "data": {
            "identityKey": identity_key,
            "state": initial_state,
            "duplicate": duplicate,
        }}]

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

        mutations = [
            {"op": "create", "key": target_identity_key + ".credentialBinding",
             "document": {"class": "credentialBinding", "vertexKey": target_identity_key,
                          "localName": "credentialBinding", "isDeleted": False,
                          "data": {"actorKey": actor_key, "boundAt": observed_at}}},
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
        ]

        events = [{"class": "IdentityClaimed", "data": {
            "identityKey": target_identity_key,
            "actorKey": actor_key,
        }}]

        return {
            "mutations": mutations,
            "events": events,
            "response": {"identityKey": target_identity_key},
        }
    fail("identity DDL: unknown operationType: " + ot)
`
