package identityhygiene

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations. One DDL —
// `identityHygiene` — gating the `MergeIdentity` operation.
//
// `edges` is a command parameter: the caller (operator CLI) reads the
// `duplicateCandidates` Lens output, collects `secondaryInboundEdges` +
// `secondaryOutboundEdges`, and submits MergeIdentity with those link
// vertex keys. The caller declares the keys in `ContextHint.Reads`;
// Processor hydrates them as ordinary Core KV reads; this script
// validates each one before acting on it. Actors are not trusted —
// every declared edge is re-read from state and re-verified.
//
// Pre-flight rules:
//   - primary != secondary; both are vtx.identity.<NanoID>
//   - both vertices exist and are not tombstoned
//   - neither is in state `merged`
//   - secondary holds no live `assignedTo` task in status `open`
//     (Contract #10 §10.1 no-orphan tombstone guard: `IdentityHasOpenTasks`
//     — reassign/cancel the task first. Enumerated via the one sanctioned
//     bounded kv.Links primitive, Contract #2 §2.5.1, direction "in";
//     mirrors clinic-domain's assert_no_overlap idiom.)
//   - every entry in `edges` validates per the trust gate below
//   - total mutations <= 999 (`MergeBatchTooLarge` otherwise)
//
// Edge validation (the trust gate):
//   - read the hydrated link envelope from state
//   - reject `EdgeNotFound` if missing or tombstoned
//   - reject `EdgeNotALink` if the key does not have the six-segment
//     `lnk.<srcType>.<srcId>.<rel>.<tgtType>.<tgtId>` shape (envelope class
//     is NOT checked — a production link's class is its relation name, e.g.
//     `holdsRole`, never the literal string "link")
//   - reject `EdgeDoesNotTouchSecondary` if neither endpoint (derived
//     from key segments) is the secondary
//
// Edge migration (after all edges validated):
//   - For each edge: tombstone the old link envelope; if the rekeyed
//     link target key already exists alive, count as a collision and
//     drop the duplicate (idempotent merge); else create the rekeyed
//     link envelope
//   - Self-loops after rekey: tombstone only
//
// duplicateOf pair-link tombstone (dedup-over-encrypted-pii-design.md §3.4):
// both directional keys — `lnk.identity.<secondary>.duplicateOf.identity.<primary>`
// and the inverted key — are dispatch-derivable, declared as optionalReads,
// and probed via `state`; whichever is live is tombstoned. Independent of
// `edges` (the CLI excludes duplicateOf/indexes from that list — they are
// pair-evidence, not business edges).
//
// indexes-driven repoint (same design, §3.4): the secondary's inbound
// `indexes` links are enumerated (bounded kv.Links, relation "indexes",
// direction "in" — the second and last enumeration this script performs).
// For each live one: the owned identityindex vertex is repointed to primary
// (`identityKey` field), the old link is tombstoned, and a new link to
// primary is created — no decryption anywhere (linkage is ownership).
//
// Credential repoint (multi-credential-identity-linking-design.md §3.3):
// every credential that currently resolves to secondary is repointed to
// primary — closing the "a merge strands a credential on the merged-dead
// identity forever" hole. secondary.credentialBinding and
// primary.credentialBinding are declared optionalReads (a never-claimed or
// Scenario-B identity has neither). The credential set is secondary's
// `credentials` array (pre-Fire-2 fallback: the singular {actorKey,
// boundAt} fields; empty if the aspect is absent) plus the implicit
// self-credential (secondary's own key, closing the Scenario-B
// resolve-miss hole). Each credential's `vtx.credentialindex.<hash>`
// vertex is repointed to primary, the set is unioned into
// primary.credentialBinding.credentials, secondary.credentialBinding is
// tombstoned, and one identity.rebound event is emitted per credential —
// the Gateway's credential-bindings materializer folds it like
// identity.claimed.
//
// State updates:
//   - secondary.state → "merged"
//   - secondary.mergedInto → primary key
//   - optional aspectConflictResolution for {name, email, phone}
//     (secondary-wins overwrites primary aspect)
//
// Events: one IdentityMerged carrying primary, secondary, linkCount and the
// per-bucket link counts (linksMigrated, linksTombstonedOnly,
// linkCollisionsMerged), plus one identity.rebound per repointed credential.
//
// Reply: MergeIdentity is multi-key with no single principal entity, so it
// returns no primaryKey. The committed key set is the key set of
// OperationReply.Revisions; merge counts ride the IdentityMerged event.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		{
			CanonicalName:     "identityHygiene",
			Class:             "meta.ddl.vertexType",
			PermittedCommands: []string{"MergeIdentity"},
			Description: "Identity-hygiene DDL. Handles MergeIdentity — " +
				"operator-explicit merge of two identities. `edges` arrives as a " +
				"command parameter (discovered by the caller via the " +
				"duplicateCandidates Lens) and is validated against Core KV by " +
				"the script. Rejects (IdentityHasOpenTasks) if secondary still holds " +
				"a live assignedTo task in status open (Contract #10 §10.1 no-orphan " +
				"tombstone guard) — reassign/cancel it first. Repoints every credential " +
				"resolving to secondary onto primary (multi-credential-identity-linking-" +
				"design.md §3.3), emitting identity.rebound per credential. Multi-key " +
				"op: returns no primaryKey; merge counts ride the IdentityMerged event " +
				"and the committed key set is in OperationReply.Revisions.",
			Script: identityHygieneScript,
			InputSchema: `{"type":"object","required":["primary","secondary","edges"],"properties":` +
				`{"primary":{"type":"string","description":"vtx.identity.<NanoID> of the surviving identity."},` +
				`"secondary":{"type":"string","description":"vtx.identity.<NanoID> of the identity to be merged and tombstoned."},` +
				`"edges":{"type":"array","items":{"type":"string"},"description":"Link vertex keys touching secondary, obtained from duplicateCandidates Lens output."},` +
				`"aspectConflictResolution":{"type":"object","description":"Optional. Map of aspect name (name|email|phone) to 'secondary-wins' to overwrite the primary aspect with the secondary's value.","additionalProperties":{"type":"string","enum":["secondary-wins"]}}}}`,
			OutputSchema: `{"type":"object","properties":{}}`,
			FieldDescription: map[string]string{
				"primary":                  "The surviving identity. All rekeyed edges will reference this identity's NanoID after merge.",
				"secondary":                "The identity being merged. Its state is set to 'merged'; its edges are rekeyed to primary.",
				"edges":                    "Ordered list of link vertex keys (lnk.*) that touch secondary. Obtained from the duplicateCandidates Lens entry's secondaryInboundEdges + secondaryOutboundEdges fields.",
				"aspectConflictResolution": "Optional per-aspect overwrite policy. Use 'secondary-wins' to copy the secondary's aspect value onto primary (e.g. prefer secondary phone number).",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name: "MergeIdentity — merge duplicate without aspect conflict resolution",
					Payload: map[string]any{
						"primary":   "vtx.identity.<primaryNanoID>",
						"secondary": "vtx.identity.<secondaryNanoID>",
						"edges":     []any{"lnk.identity.<secondaryNanoID>.holdsRole.role.<roleNanoID>"},
					},
					ExpectedOutcome: "Tombstones secondary holdsRole link, creates rekeyed link under primary. " +
						"Sets secondary.state=merged, secondary.mergedInto=primary. Returns no primaryKey; merge counts ride the IdentityMerged event.",
				},
				{
					Name: "MergeIdentity — with secondary-wins phone overwrite",
					Payload: map[string]any{
						"primary":                  "vtx.identity.<primaryNanoID>",
						"secondary":                "vtx.identity.<secondaryNanoID>",
						"edges":                    []any{},
						"aspectConflictResolution": map[string]any{"phone": "secondary-wins"},
					},
					ExpectedOutcome: "Sets secondary.state=merged. Overwrites primary.phone with secondary.phone value.",
				},
			},
		},
	}
}

// identityHygieneScript implements MergeIdentity.
//
// Command parameters (op.payload):
//   - primary                (vtx.identity.<primaryNanoID>)
//   - secondary              (vtx.identity.<secondaryNanoID>)
//   - edges                  (list of link vertex keys touching secondary;
//     caller obtains them from the
//     duplicateCandidates Lens entry's
//     secondaryInboundEdges + secondaryOutboundEdges)
//   - aspectConflictResolution  (optional; {name|email|phone: "secondary-wins"})
//
// Caller's ContextHint.Reads MUST include:
//   - primary
//   - secondary
//   - primary.state, primary.mergedInto
//   - secondary.state, secondary.mergedInto
//   - every link vertex key in `edges`
//   - (optional) primary.{name,email,phone} +
//     secondary.{name,email,phone}  when ACR is requested
//
// Caller's ContextHint.OptionalReads MUST include (dispatch-derivable,
// absence-tolerant — dedup-over-encrypted-pii-design.md §3.4):
//   - lnk.identity.<secondaryId>.duplicateOf.identity.<primaryId>
//   - lnk.identity.<primaryId>.duplicateOf.identity.<secondaryId>
//
// Caller's ContextHint.OptionalReads MUST also include (dispatch-derivable,
// absence-tolerant — multi-credential-identity-linking-design.md §3.3, A6:
// a never-claimed or Scenario-B identity has no credentialBinding aspect,
// and a required read's absence is a hydration fault that would block
// exactly the merges this closes):
//   - secondary.credentialBinding
//   - primary.credentialBinding
//
// Caller's ContextHint.Enumerations MUST declare the secondary's inbound
// `indexes` links (Hub: secondary, Relation: "indexes", Direction: "in"), in
// addition to the existing assignedTo enumeration.
//
// The script reads the hydrated map by known key, with two sanctioned
// enumeration exceptions: the secondary-has-open-tasks guard (inbound
// assignedTo) and the indexes-driven repoint (inbound indexes), both via the
// bounded kv.Links primitive (Contract #2 §2.5.1) — the same idiom
// clinic-domain's assert_no_overlap uses. Each indexes hit's owned
// identityindex vertex and the primary's would-be new indexes link are
// read/probed via kv.Read, a per-candidate follow-up off the enumeration
// (the hash is not dispatch-known ahead of the enumeration). The script
// never scans, and never reads any lens-output bucket.
const identityHygieneScript = `
IDENTITY_TASK_PAGE_LIMIT = 256
MAX_IDENTITY_TASK_PAGES = 64

def identity_has_open_tasks(identity_key):
    # Contract #10 §10.1 no-orphan tombstone guard: an identity holding a
    # live assignedTo task is rejected from MergeIdentity (the merge/tombstone
    # equivalent for identities), not silently orphaned -- reassign/cancel the
    # task first. Enumerated via the sanctioned bounded kv.Links (Contract #2
    # §2.5.1), direction "in" -- the identity is the assignedTo link's TARGET
    # (task is source, per Contract #1 §1.1). A live LINK alone does not mean
    # an open task: CompleteTask/CancelTask never tombstone the assignedTo
    # link (orchestration-base leaves it live post-transition), so each
    # candidate's source task vertex is read and only a still-"open" task
    # blocks.
    cursor = None
    for _page in range(MAX_IDENTITY_TASK_PAGES):
        # read-posture: (e) relation=assignedTo epoch=none (read-only guard:
        # a task queued concurrently with the tombstone slips past — accepted;
        # Weaver detect+recover is the orphan-task enforcer)
        links, cursor = kv.Links(identity_key, "assignedTo", "in", cursor, IDENTITY_TASK_PAGE_LIMIT)
        for lk in links:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the
            # enumeration above (data-derived key)
            task = kv.Read(lk.sourceVertex)
            if task == None or task.isDeleted:
                continue
            if task.data.get("status") == "open":
                fail("IdentityHasOpenTasks: " + identity_key + " still has open task " + lk.sourceVertex + " assigned; reassign or cancel it first")
        if cursor == None:
            return
    fail("IdentityTaskFanoutTooLarge: " + identity_key + " has too many assignedTo links to enumerate at merge time; reassign/cancel enough to bring it under the page cap first")

INDEXES_PAGE_LIMIT = 256
MAX_INDEXES_PAGES = 64

def collect_indexes_repoints(secondary_key):
    # dedup-over-encrypted-pii-design.md §3.4: the secondary's owned
    # identityindex vertices are enumerable via their inbound "indexes"
    # links (linkage IS ownership) without knowing the plaintext the hash
    # derives from. Enumerated via the sanctioned bounded kv.Links
    # (Contract #2 §2.5.1), direction "in" -- the secondary identity is the
    # indexes link's TARGET (identityindex vertex is source, per Contract #1
    # §1.1).
    repoints = []
    cursor = None
    for _page in range(MAX_INDEXES_PAGES):
        # read-posture: (e) relation=indexes epoch=none (read-only guard: an
        # indexes link created concurrently with the tombstone slips past --
        # accepted, same posture as identity_has_open_tasks above)
        links, cursor = kv.Links(secondary_key, "indexes", "in", cursor, INDEXES_PAGE_LIMIT)
        for lk in links:
            if lk.isDeleted:
                continue
            repoints.append(lk)
        if cursor == None:
            return repoints
    fail("IdentityIndexFanoutTooLarge: " + secondary_key + " has too many indexes links to enumerate at merge time")

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot != "MergeIdentity":
        fail("identityHygiene: unknown operationType: " + ot)

    primary = p.primary if hasattr(p, "primary") else None
    secondary = p.secondary if hasattr(p, "secondary") else None
    edges_in = p.edges if hasattr(p, "edges") else []
    acr = p.aspectConflictResolution if hasattr(p, "aspectConflictResolution") else None

    if primary == None or type(primary) != type("") or not primary.startswith("vtx.identity."):
        fail("InvalidMerge: primary: required vtx.identity.<NanoID>")
    if secondary == None or type(secondary) != type("") or not secondary.startswith("vtx.identity."):
        fail("InvalidMerge: secondary: required vtx.identity.<NanoID>")
    if primary == secondary:
        fail("MergeSelfReference: " + primary)
    if type(edges_in) != type([]):
        fail("InvalidMerge: edges: required list")

    primary_id = primary[len("vtx.identity."):]
    secondary_id = secondary[len("vtx.identity."):]

    # --- Both vertices must exist and not be tombstoned ---
    pvtx = state[primary] if primary in state else None
    svtx = state[secondary] if secondary in state else None
    if pvtx == None or (hasattr(pvtx, "isDeleted") and pvtx.isDeleted):
        fail("MergeIdentityMissing: primary")
    if svtx == None or (hasattr(svtx, "isDeleted") and svtx.isDeleted):
        fail("MergeIdentityMissing: secondary")

    # --- Both not already merged ---
    def read_state(identity_key):
        akey = identity_key + ".state"
        if akey in state:
            d = state[akey]
            if d.data != None and "value" in d.data:
                return d.data["value"]
        return None

    p_state = read_state(primary)
    s_state = read_state(secondary)
    if p_state == "merged":
        fail("MergeStateRejected: primary state=merged")
    if s_state == "merged":
        fail("MergeStateRejected: secondary state=merged")

    # --- No-orphan tombstone guard (Contract #10 §10.1): secondary must not
    # still hold a live open task. Checked ahead of the edges trust gate --
    # independent of whatever edges the caller declared (an assignedTo link
    # is never a valid MergeIdentity edge to silently rekey; the operator
    # reassigns/cancels the task via the task DDL first).
    identity_has_open_tasks(secondary)

    # --- duplicateOf pair-link probe (both directions): dispatch-derivable
    # from primary+secondary, declared optionalReads, absence-tolerant.
    # The operator may pick either identity as primary, so both directional
    # keys are checked; whichever is live is tombstoned below.
    dup_probe_keys = [
        "lnk.identity." + secondary_id + ".duplicateOf.identity." + primary_id,
        "lnk.identity." + primary_id + ".duplicateOf.identity." + secondary_id,
    ]
    dup_links_to_tombstone = []
    for dk in dup_probe_keys:
        if dk in state:
            d = state[dk]
            if d != None and not (hasattr(d, "isDeleted") and d.isDeleted):
                dup_links_to_tombstone.append({"key": dk, "doc": d})

    # --- indexes-driven repoint: enumerate secondary's owned identityindex
    # vertices (via inbound indexes links) up front so the batch-size cap
    # below accounts for them.
    idx_repoints = collect_indexes_repoints(secondary)

    # --- Credential repoint (multi-credential-identity-linking-design.md
    # §3.3): every credential resolving to secondary must repoint to
    # primary, or a merged identity's login strands on the merged-dead
    # vertex forever. Reads are optionalReads -- a never-claimed or
    # Scenario-B identity has no credentialBinding aspect, and treating
    # its absence as a hydration fault would block exactly the merges
    # this closes.
    def read_credential_binding(identity_key):
        akey = identity_key + ".credentialBinding"
        if akey in state:
            d = state[akey]
            if d != None and not (hasattr(d, "isDeleted") and d.isDeleted) and d.data != None:
                return d.data
        return None

    sec_binding = read_credential_binding(secondary)
    pri_binding = read_credential_binding(primary)
    merged_at = op.submittedAt

    seen_actors = {}
    cred_set = []

    def add_credential(actor_key, bound_at):
        if actor_key == None or actor_key in seen_actors:
            return
        seen_actors[actor_key] = True
        cred_set.append({"actorKey": actor_key, "boundAt": bound_at})

    # Credential set = secondary's "credentials" array (pre-Fire-2
    # fallback: the singular {actorKey, boundAt} fields; empty if the
    # aspect is absent entirely -- a never-claimed staff-created
    # secondary folds nothing on this front), plus the implicit
    # self-credential: the secondary's own key, which closes the
    # Scenario-B resolve-miss hole and is inert-but-correct for every
    # other secondary shape.
    if sec_binding != None:
        arr = sec_binding.get("credentials")
        if arr != None and type(arr) == type([]):
            for c in arr:
                add_credential(c.get("actorKey"), c.get("boundAt"))
        elif sec_binding.get("actorKey") != None:
            add_credential(sec_binding.get("actorKey"), sec_binding.get("boundAt"))
    add_credential(secondary, merged_at)

    # Union target: primary's existing credential set (same fallback
    # shape) plus every entry in cred_set not already present.
    pri_seen = {}
    pri_unioned = []

    def add_primary_credential(actor_key, bound_at):
        if actor_key == None or actor_key in pri_seen:
            return
        pri_seen[actor_key] = True
        pri_unioned.append({"actorKey": actor_key, "boundAt": bound_at})

    if pri_binding != None:
        parr = pri_binding.get("credentials")
        if parr != None and type(parr) == type([]):
            for c in parr:
                add_primary_credential(c.get("actorKey"), c.get("boundAt"))
        elif pri_binding.get("actorKey") != None:
            add_primary_credential(pri_binding.get("actorKey"), pri_binding.get("boundAt"))
    for c in cred_set:
        add_primary_credential(c["actorKey"], c["boundAt"])

    # Singular actorKey/boundAt fields keep meaning "first-bound
    # credential" -- preserve primary's own if it already had one, else
    # default to the first unioned entry (pri_unioned is never empty:
    # cred_set always carries at least the secondary self-credential).
    if pri_binding != None and pri_binding.get("actorKey") != None:
        pri_singular_actor = pri_binding.get("actorKey")
        pri_singular_bound = pri_binding.get("boundAt")
    else:
        pri_singular_actor = pri_unioned[0]["actorKey"]
        pri_singular_bound = pri_unioned[0]["boundAt"]

    # --- Trust gate: validate every declared edge against Core KV.
    # Actors are not trusted to declare keys honestly; each must
    # re-read as a link envelope and endpoint-touch the secondary.
    seen = {}
    sec_links = []
    for lk in edges_in:
        if type(lk) != type(""):
            fail("EdgeNotALink: non-string edge entry")
        if lk == "":
            fail("EdgeNotALink: empty edge key")
        if lk in seen:
            continue
        seen[lk] = True

        # Shape check on the key itself (cheap, before reading state).
        parts = lk.split(".")
        if len(parts) != 6 or parts[0] != "lnk":
            fail("EdgeNotALink: " + lk)

        if lk not in state:
            fail("EdgeNotFound: " + lk)
        link = state[lk]
        if link == None:
            fail("EdgeNotFound: " + lk)
        if hasattr(link, "isDeleted") and link.isDeleted:
            fail("EdgeNotFound: " + lk)

        # Envelope class is NOT checked here: a production link's class is
        # its relation name (e.g. "holdsRole"), never the literal "link" --
        # the six-segment key shape above is the real link-ness test.

        # Endpoint touch: per Contract #1 §1.1 the key carries the
        # endpoints; require at least one endpoint = secondary.
        src_type = parts[1]
        src_id = parts[2]
        tgt_type = parts[4]
        tgt_id = parts[5]
        touches_secondary = (src_type == "identity" and src_id == secondary_id) or (tgt_type == "identity" and tgt_id == secondary_id)
        if not touches_secondary:
            fail("EdgeDoesNotTouchSecondary: " + lk)

        sec_links.append({"key": lk, "doc": link, "parts": parts})

    # --- Pre-flight: batch-size cap (excludes reads).
    # Each non-self-loop link: 2 ops. Self-loop: 1 op. Plus state(1) +
    # mergedInto(1) + ACR(0..3).
    link_count_full = 0
    link_count_self = 0
    for entry in sec_links:
        parts = entry["parts"]
        new_src_id = primary_id if parts[2] == secondary_id else parts[2]
        new_tgt_id = primary_id if parts[5] == secondary_id else parts[5]
        if parts[1] == parts[4] and new_src_id == new_tgt_id:
            link_count_self += 1
        else:
            link_count_full += 1
    acr_count = 0
    if acr != None and type(acr) == type({}):
        for asp in ["name", "email", "phone"]:
            if asp in acr and acr[asp] == "secondary-wins":
                acr_count += 1
    # duplicateOf tombstones: 1 mutation each. indexes repoints: up to 3
    # mutations each (tombstone old link + update index vertex + create new
    # link; the create is skipped if primary already owns the same index).
    # Credential repoint: 1 credentialindex update per credential in
    # cred_set, always exactly 1 primary.credentialBinding union write
    # (cred_set is never empty), plus 1 secondary.credentialBinding
    # tombstone iff that aspect was present.
    cred_secondary_tombstone_muts = 1 if sec_binding != None else 0
    total_muts = (link_count_full * 2 + link_count_self + 2 + acr_count +
                  len(dup_links_to_tombstone) + len(idx_repoints) * 3 +
                  len(cred_set) + 1 + cred_secondary_tombstone_muts)
    if total_muts > 999:
        fail("MergeBatchTooLarge: " + str(total_muts))

    # --- Build mutations ---
    mutations = []
    links_migrated = 0
    links_tombstoned_only = 0
    link_collisions_merged = 0
    for entry in sec_links:
        lk = entry["key"]
        link = entry["doc"]
        parts = entry["parts"]
        link_class = getattr(link, "class") if hasattr(link, "class") else ""
        link_data_in = link.data if hasattr(link, "data") and link.data != None else {}
        tomb_doc = {"class": link_class, "isDeleted": True, "data": link_data_in}
        mutations.append({"op": "update", "key": lk, "document": tomb_doc})

        # Rekey endpoints.
        new_src_type = parts[1]
        new_src_id = parts[2]
        new_tgt_type = parts[4]
        new_tgt_id = parts[5]
        if new_src_type == "identity" and new_src_id == secondary_id:
            new_src_id = primary_id
        if new_tgt_type == "identity" and new_tgt_id == secondary_id:
            new_tgt_id = primary_id
        if new_src_type == new_tgt_type and new_src_id == new_tgt_id:
            links_tombstoned_only += 1
            continue
        new_key = "lnk." + new_src_type + "." + new_src_id + "." + parts[3] + "." + new_tgt_type + "." + new_tgt_id
        existing = state[new_key] if new_key in state else None
        if existing != None and not (hasattr(existing, "isDeleted") and existing.isDeleted):
            link_collisions_merged += 1
            continue
        new_doc = {"class": link_class, "isDeleted": False, "data": link_data_in}
        mutations.append({"op": "create", "key": new_key, "document": new_doc})
        links_migrated += 1

    # --- duplicateOf pair-link tombstone (both directions probed above) ---
    for entry in dup_links_to_tombstone:
        dk = entry["key"]
        d = entry["doc"]
        dup_class = getattr(d, "class") if hasattr(d, "class") else "duplicateOf"
        dup_data = d.data if hasattr(d, "data") and d.data != None else {}
        mutations.append({"op": "update", "key": dk,
            "document": {"class": dup_class, "isDeleted": True, "data": dup_data}})

    # --- indexes-driven repoint: no decryption -- linkage is ownership. ---
    for lk in idx_repoints:
        idx_vertex_key = lk.sourceVertex
        old_link_key = lk.key
        link_data = lk.data if lk.data != None else {}
        mutations.append({"op": "update", "key": old_link_key,
            "document": {"class": "indexes", "isDeleted": True, "data": link_data}})

        # read-posture: (e) per-candidate follow-up read off the enumeration
        # above (data-derived key: the hash is not dispatch-known ahead of
        # collect_indexes_repoints)
        idx_vtx = kv.Read(idx_vertex_key)
        contact_type = None
        if idx_vtx != None and idx_vtx.data != None and "contactType" in idx_vtx.data:
            contact_type = idx_vtx.data["contactType"]
        mutations.append({"op": "update", "key": idx_vertex_key,
            "document": {"class": "identityindex", "isDeleted": False,
                         "data": {"contactType": contact_type, "identityKey": primary}}})

        new_indexes_key = "lnk." + idx_vertex_key[len("vtx."):] + ".indexes.identity." + primary_id
        # read-posture: (e) per-candidate follow-up read off the enumeration
        # above (data-derived key)
        existing_new = kv.Read(new_indexes_key)
        already_live = existing_new != None and not (hasattr(existing_new, "isDeleted") and existing_new.isDeleted)
        if not already_live:
            mutations.append({"op": "create", "key": new_indexes_key,
                "document": {"class": "indexes", "isDeleted": False,
                             "sourceVertex": idx_vertex_key, "targetVertex": primary,
                             "localName": "indexes", "data": {}}})

    # --- Credential repoint: unconditioned "update" per credential -- a
    # blind Put for the data-derived credentialindex key (not dispatch-
    # known ahead of cred_set), same idiom the claim script's own derived-
    # key writes use.
    for c in cred_set:
        idx_key = "vtx.credentialindex." + crypto.sha256NanoID(c["actorKey"])
        mutations.append({"op": "update", "key": idx_key,
            "document": {"class": "credentialindex", "isDeleted": False,
                         "data": {"actorKey": c["actorKey"], "identityKey": primary,
                                  "boundAt": c["boundAt"]}}})

    # primary.credentialBinding is declared optionalReads: CAS'd on its
    # step-4 revision when present, an unconditioned blind Put (creating
    # the aspect) when absent.
    mutations.append({"op": "update", "key": primary + ".credentialBinding",
        "document": {"class": "credentialBinding", "vertexKey": primary,
                     "localName": "credentialBinding", "isDeleted": False,
                     "data": {"actorKey": pri_singular_actor, "boundAt": pri_singular_bound,
                              "credentials": pri_unioned}}})

    if sec_binding != None:
        mutations.append({"op": "tombstone", "key": secondary + ".credentialBinding"})

    # --- Secondary state aspect: -> merged ---
    mutations.append({"op": "update", "key": secondary + ".state",
        "document": {"class": "state", "vertexKey": secondary, "localName": "state",
                     "isDeleted": False, "data": {"value": "merged"}}})

    # --- Secondary mergedInto ---
    mutations.append({"op": "update", "key": secondary + ".mergedInto",
        "document": {"class": "mergedInto", "vertexKey": secondary, "localName": "mergedInto",
                     "isDeleted": False, "data": {"value": primary}}})

    # --- Optional aspect-conflict resolution (primary-side overwrite) ---
    if acr != None and type(acr) == type({}):
        for asp in ["name", "email", "phone"]:
            if asp in acr and acr[asp] == "secondary-wins":
                sec_aspect_key = secondary + "." + asp
                sec_aspect = state[sec_aspect_key] if sec_aspect_key in state else None
                if sec_aspect != None and sec_aspect.data != None and "value" in sec_aspect.data:
                    sec_val = sec_aspect.data["value"]
                    if type(sec_val) == type("") and len(sec_val) > 0:
                        mutations.append({"op": "update", "key": primary + "." + asp,
                            "document": {"class": asp, "vertexKey": primary, "localName": asp,
                                         "isDeleted": False, "data": {"value": sec_val}}})

    # --- Events ---
    events = [{"class": "identity.merged", "data": {
        "primary": primary,
        "secondary": secondary,
        "linkCount": links_migrated + links_tombstoned_only + link_collisions_merged,
        "linksMigrated": links_migrated,
        "linksTombstonedOnly": links_tombstoned_only,
        "linkCollisionsMerged": link_collisions_merged,
        "mergedAt": op.submittedAt,
    }}]

    # One identity.rebound per repointed credential -- the existing class
    # (identity.claimed) is deliberately NOT reused: a rebind is a
    # different fact (existing binding repointed, no claim occurred) that
    # needs previousIdentityKey, and reuse would put phantom claims in the
    # audit stream (design §4.3). The Gateway's credential-bindings
    # materializer folds it like identity.claimed.
    for c in cred_set:
        events.append({"class": "identity.rebound", "data": {
            "actorKey": c["actorKey"],
            "identityKey": primary,
            "previousIdentityKey": secondary,
        }})

    # MergeIdentity is multi-key with no single principal entity, so it omits
    # primaryKey. The committed key set (rekeyed links, secondary.state,
    # secondary.mergedInto, optional aspect overwrites) is the key set of
    # OperationReply.Revisions; counts ride the IdentityMerged event.
    return {
        "mutations": mutations,
        "events": events,
    }
`
