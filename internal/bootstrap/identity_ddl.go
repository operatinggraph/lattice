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
    if ot == "FlagIdentityForReview":
        fail("NotYetImplemented: Story 4.3: FlagIdentityForReview")
    if ot == "ApproveIdentityMerge":
        fail("NotYetImplemented: Story 4.5: ApproveIdentityMerge")
    if ot == "MergeIdentity":
        fail("NotYetImplemented: Story 4.5: MergeIdentity")
    if ot == "TombstoneIdentity":
        fail("NotYetImplemented: Story 4.5: TombstoneIdentity")
    if ot == "ScanIdentityDuplicates":
        # -----------------------------------------------------------------------
        # ScanIdentityDuplicates — Duplicate Identity Detection (FR3, Story 4.4)
        # -----------------------------------------------------------------------
        # Three match criteria (Phase 1):
        #   1. exact-email  — both non-empty, lowercased, trimmed, equal.
        #   2. exact-phone  — both non-empty, digits+'+' stripped, equal.
        #   3. levenshtein-name — ratio(norm(a.name), norm(b.name)) >= threshold.
        # Normalization: name=lowercase+trim, email=lowercase+trim,
        #   phone=strip non-digit/non-'+'.
        # Default threshold: 0.85 (operator-overridable via payload.levenshteinThreshold).
        # Merged and tombstoned identities are excluded from the candidate pool.
        #
        # Output model: one canonical LINK per pair (symmetric; not two aspects).
        # Link key: lnk.identity.<lowID>.duplicateOf.identity.<highID>
        #   where lowID/highID are NanoIDs sorted lexicographically.
        # Link class: "duplicateOf". Link data: {criteria, confidence,
        #   scanRequestId, flaggedAt}.
        #
        # Idempotency: state.read(linkKey) — skip pair if non-tombstoned link
        # exists. Hydrator pre-loads all lnk.identity.* so this is a cheap
        # in-memory check (no round-trips). Per Decision #15 in brief, this
        # comment block + response detail constitute the canonical algorithm spec.
        # -----------------------------------------------------------------------

        # --- Input: optional threshold override ---
        threshold = 0.85
        if hasattr(p, "levenshteinThreshold"):
            t = p.levenshteinThreshold
            if type(t) == type(0) or type(t) == type(0.0):
                t = float(t)
                if t < 0.0 or t > 1.0:
                    fail("InvalidArgument: levenshteinThreshold: out of [0,1]")
                threshold = t

        # --- Enumerate identities loaded by hydrator scan-prefix ---
        # state.keys_with_prefix returns ALL keys with the prefix, including
        # aspect keys. Filter to 3-segment vertex keys only.
        all_keys = state.keys_with_prefix("vtx.identity.")
        identity_keys = []
        for k in all_keys:
            # 3-segment: vtx.identity.<id> — suffix after "vtx.identity." has no dot
            suffix = k[len("vtx.identity."):]
            if "." not in suffix:
                identity_keys.append(k)

        # --- Build normalized identity records (skip merged/tombstoned) ---
        records = []
        for ikey in identity_keys:
            vtx = state[ikey] if ikey in state else None
            if vtx == None or (hasattr(vtx, "isDeleted") and vtx.isDeleted):
                continue

            # Read pre-loaded aspects.
            st_doc = state[ikey + ".state"] if (ikey + ".state") in state else None
            current_state = None
            if st_doc != None and st_doc.data != None and "value" in st_doc.data:
                current_state = st_doc.data["value"]

            # Skip merged identities entirely (Decision #7).
            if current_state == "merged":
                continue

            name_doc = state[ikey + ".name"] if (ikey + ".name") in state else None
            name_norm = ""
            if name_doc != None and name_doc.data != None and "value" in name_doc.data:
                raw = name_doc.data["value"]
                if type(raw) == type(""):
                    name_norm = raw.strip().lower()

            email_doc = state[ikey + ".email"] if (ikey + ".email") in state else None
            email_norm = ""
            if email_doc != None and email_doc.data != None and "value" in email_doc.data:
                raw = email_doc.data["value"]
                if type(raw) == type(""):
                    email_norm = raw.strip().lower()

            phone_doc = state[ikey + ".phone"] if (ikey + ".phone") in state else None
            phone_norm = ""
            if phone_doc != None and phone_doc.data != None and "value" in phone_doc.data:
                raw = phone_doc.data["value"]
                if type(raw) == type(""):
                    stripped = ""
                    for ch in raw.elems():
                        if ch >= "0" and ch <= "9":
                            stripped += ch
                        elif ch == "+":
                            stripped += ch
                    phone_norm = stripped

            records.append({
                "key": ikey,
                "name_norm": name_norm,
                "email_norm": email_norm,
                "phone_norm": phone_norm,
                "current_state": current_state,
            })

        # --- Pairwise comparison (i < j, O(N^2) acceptable at N<=500) ---
        pairs = []  # list of {aKey, bKey, criteria, confidence}

        n = len(records)
        for i in range(n):
            for j in range(i + 1, n):
                a = records[i]
                b = records[j]
                criteria = []
                confidence = 0.0

                # Exact email match.
                if a["email_norm"] != "" and b["email_norm"] != "" and a["email_norm"] == b["email_norm"]:
                    criteria.append("exact-email")
                    confidence = 1.0

                # Exact phone match.
                if a["phone_norm"] != "" and b["phone_norm"] != "" and a["phone_norm"] == b["phone_norm"]:
                    if "exact-phone" not in criteria:
                        criteria.append("exact-phone")
                    confidence = 1.0

                # Levenshtein name match.
                if a["name_norm"] != "" and b["name_norm"] != "":
                    ratio = strings.levenshtein_ratio(a["name_norm"], b["name_norm"])
                    if ratio >= threshold:
                        if "levenshtein-name" not in criteria:
                            criteria.append("levenshtein-name")
                        if ratio > confidence:
                            confidence = ratio

                if len(criteria) > 0:
                    pairs.append({
                        "aKey": a["key"],
                        "bKey": b["key"],
                        "criteria": criteria,
                        "confidence": confidence,
                    })

        # --- Idempotency check + build mutations/events ---
        mutations = []
        events = []
        skipped_existing = 0
        skipped_flagged = 0
        cnt_email = 0
        cnt_phone = 0
        cnt_lev = 0
        scan_request_id = op.requestId
        flagged_at = op.submittedAt
        new_pairs = []

        for pair in pairs:
            a_key = pair["aKey"]
            b_key = pair["bKey"]

            # --- Canonical link key (symmetric — one link per pair) ---
            # Extract NanoID suffix from vtx.identity.<NanoID>.
            a_id = a_key[len("vtx.identity."):]
            b_id = b_key[len("vtx.identity."):]
            # Sort lexicographically to get a stable canonical key.
            if a_id < b_id:
                low_id = a_id
                high_id = b_id
            else:
                low_id = b_id
                high_id = a_id
            link_key = "lnk.identity." + low_id + ".duplicateOf.identity." + high_id

            # Idempotency: skip if non-tombstoned link already exists.
            # Hydrator pre-loaded all lnk.identity.* envelopes — cheap lookup.
            existing_link = state[link_key] if link_key in state else None
            if existing_link != None and not (hasattr(existing_link, "isDeleted") and existing_link.isDeleted):
                skipped_existing += 1
                continue

            new_pairs.append(pair)

            # Count by criterion for breakdown.
            for c in pair["criteria"]:
                if c == "exact-email":
                    cnt_email += 1
                elif c == "exact-phone":
                    cnt_phone += 1
                elif c == "levenshtein-name":
                    cnt_lev += 1

            # --- Single link mutation per pair ---
            mutations.append({"op": "create", "key": link_key,
                "document": {"class": "duplicateOf", "isDeleted": False,
                             "data": {
                                 "criteria": pair["criteria"],
                                 "confidence": pair["confidence"],
                                 "scanRequestId": scan_request_id,
                                 "flaggedAt": flagged_at,
                             }}})

            # --- State mutations: transition each member if not already flagged ---
            a_state = None
            b_state = None
            for rec in records:
                if rec["key"] == a_key:
                    a_state = rec["current_state"]
                elif rec["key"] == b_key:
                    b_state = rec["current_state"]

            if a_state != "flagged-for-review":
                mutations.append({"op": "update", "key": a_key + ".state",
                    "document": {"class": "state", "vertexKey": a_key,
                                 "localName": "state", "isDeleted": False,
                                 "data": {"value": "flagged-for-review"}}})
            else:
                skipped_flagged += 1

            if b_state != "flagged-for-review":
                mutations.append({"op": "update", "key": b_key + ".state",
                    "document": {"class": "state", "vertexKey": b_key,
                                 "localName": "state", "isDeleted": False,
                                 "data": {"value": "flagged-for-review"}}})
            else:
                skipped_flagged += 1

            # Event per flagged pair — includes linkKey per brief §4.
            events.append({"class": "IdentityDuplicateCandidateFlagged", "data": {
                "linkKey": link_key,
                "aKey": a_key,
                "bKey": b_key,
                "criteria": pair["criteria"],
                "confidence": pair["confidence"],
            }})

        # Build pairs summary for response (use new_pairs — non-skipped).
        pairs_summary = []
        for pair in new_pairs:
            pairs_summary.append({
                "aKey": pair["aKey"],
                "bKey": pair["bKey"],
                "criteria": pair["criteria"],
                "confidence": pair["confidence"],
            })

        return {
            "mutations": mutations,
            "events": events,
            "response": {
                "totalScanned": len(records),
                "candidatesFound": len(new_pairs),
                "skippedExistingPairs": skipped_existing,
                "skippedAlreadyFlagged": skipped_flagged,
                "breakdown": {
                    "exact-email": cnt_email,
                    "exact-phone": cnt_phone,
                    "levenshtein-name": cnt_lev,
                },
                "pairs": pairs_summary,
                "levenshteinThreshold": threshold,
                "scanRequestId": scan_request_id,
            },
        }

    fail("identity DDL: unknown operationType: " + ot)
`
