package leasesigning

import (
	"fmt"
	"strings"

	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// leaseAppDDLScript handles the leaseapp lifecycle ops CreateLeaseApplication
// and SignLease. Known-key reads only (validates every link/aspect endpoint by
// the keys the caller lists in ContextHint.Reads). Root data stays {} on every
// op (D5): the applicant is a link, the signature is an aspect.
//
// renewalWindow (a Go duration string, time.ParseDuration form) is baked into
// DecideLeaseApplication's .tenancy stamping at package-init time — the same
// "the policy lives in the script" convention bgcheckFreshnessWindow uses — so
// renewalOpensAt = leaseEnd - renewalWindow is a compile-time-selected
// constant, never a runtime mutation. The substitution is a plain
// strings.Replace token rather than fmt.Sprintf so that no literal '%' in the
// script (the "%" modulo operator) is ever read as a formatting verb.
var leaseAppDDLScript = strings.Replace(`
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def make_aspect_upsert(vtx_key, local_name, cls, data):
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_update_occ(vtx_key, local_name, cls, data, expected_revision):
    # An update PINNED to the revision the script read the aspect at. A batch is
    # atomic within itself, which says nothing about a DIFFERENT op writing the
    # same key between this script's hydration and its commit -- the .tenancy
    # rewrite below reads the aspect's CONTENT to decide what to write (every
    # field preserved, endedAt added), and SignRenewal rewrites the same aspect
    # whole, so an unconditioned update would silently swallow a term extension
    # that landed in that window. The revision comes from the DECLARED read: a
    # caller that omits the declaration gets no hydrated document and the
    # branch refuses before it can write.
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data},
            "expectedRevision": expected_revision}

def make_vtx_tombstone(key, cls):
    # Soft-delete a vertex (isDeleted=True). UNCONDITIONED — a concurrent withdraw
    # tombstones to the same state (idempotent), and nothing else writes the
    # leaseapp ROOT (SignLease writes the .signature aspect, a different key). The
    # convergence lens anchors on the leaseapp and filters isDeleted, so the row
    # deletes (EmptyBehavior). Root data stays {} (D5).
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": True, "data": {}}}

def make_link_revive_occ(key, source, target, cls, local_name, expected_revision):
    # Revive a soft-deleted guard link (isDeleted=True → False), CAS-guarded on its
    # tombstone revision. A blind make_link (op:create) would COLLIDE with the
    # existing tombstone key, so a re-apply after a withdraw must revive, not create
    # (the userTask-self-heal / object-GC-re-link precedent). The CAS serializes two
    # concurrent re-applies: both snapshot the same revision, both update, the second
    # RevisionConflicts (fail closed, never a silent duplicate).
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": {}},
            "expectedRevision": expected_revision}

def make_link_tombstone(key, source, target, cls, local_name):
    # Soft-delete a guard link (isDeleted=True). UNCONDITIONED — a withdraw is the
    # authority that the application (and so the guard) is gone; a live application
    # (alive guard) blocks any concurrent re-apply at CreateLeaseApplication, so no
    # revive races this tombstone. Frees the (applicant, unit) pair for re-apply.
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": True,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": {}}}

def make_link_tombstone_occ(key, source, target, cls, local_name, expected_revision):
    # Soft-delete a live link, CAS-guarded on its own revision. Two concurrent
    # re-points of the same source to different targets both read the same
    # live link and both try to retire it — the second RevisionConflicts
    # (fail closed) rather than leaving the source with two live links of the
    # same relation.
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": True,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": {}},
            "expectedRevision": expected_revision}

def make_link_create_or_revive(key, source, target, cls, local_name):
    # read-posture: (d) declared optionalReads at ReassignLeaseUnit dispatch —
    # an absent link (the pairing has never been used before) is the common
    # case, never a required read.
    existing = kv.Read(key)
    if existing != None and not existing.isDeleted:
        fail("InvalidState: " + key + " is already live — this should have been tombstoned first")
    if existing != None:
        return make_link_revive_occ(key, source, target, cls, local_name, existing.revision)
    return make_link(key, source, target, cls, local_name, {})

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def require_number(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        fail("InvalidArgument: " + name + ": required number")
    return v

def optional_number(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        return None
    return v

def two_decimals(v, name):
    # A dollar amount carries at most two decimals: v × 100 must sit within a
    # millionth of an integer. The ledger keeps integer cents, and a figure
    # with a fractional cent here would become a clause the reader refuses on
    # every convergence pass — so it is refused once, at the source.
    scaled = v * 100
    nearest = int(scaled + 0.5)
    d = scaled - nearest
    if d < 0:
        d = -d
    if d > 0.000001:
        fail("InvalidArgument: " + name + ": at most two decimal places")
    return v

def as_rfc3339_instant(s):
    # A bare "YYYY-MM-DD" (the FE's <input type=date> shape, and what
    # seed-showcase / seed-classic-demo write for moveInDate / availableFrom)
    # anchors to midnight UTC. time.rfc3339_utc itself rejects a bare date, so
    # every caller normalizes through this first; an already-RFC3339 value
    # passes through unchanged.
    if len(s) == 10:
        return s + "T00:00:00Z"
    return s

def required_date_instant(p, name):
    # A required DATE-ONLY fact: a bare "YYYY-MM-DD" or an RFC3339 instant,
    # returned as midnight UTC of the value's UTC calendar day in
    # time.rfc3339_utc's canonical form, so every comparison against a stored
    # .tenancy stamp is a plain string compare and the stored value renders
    # as the same date on every card. An instant with an offset is read as
    # ITS UTC calendar day ("2027-04-01T00:00:00-07:00" is 2027-04-01T07:00Z,
    # recorded as 2027-04-01T00:00:00Z); the clock part is dropped, never
    # kept — a move-out is a day, and a stray seven hours would otherwise be
    # a whole billing period on the rent clause. The shape is checked here so
    # the refusal names the FIELD ("2026-9-15" would otherwise die inside the
    # parser under a message naming only the value); a well-shaped date the
    # calendar rejects (a February 30th) is still the parser's own
    # InvalidArgument.
    v = required_string(p, name)
    s = as_rfc3339_instant(v)
    digits = s[0:4] + s[5:7] + s[8:10]
    if len(s) < 20 or s[4] != "-" or s[7] != "-" or s[10] != "T" or not digits.isdigit():
        fail("InvalidArgument: " + name + ": required YYYY-MM-DD or an RFC3339 instant; got " + v)
    return time.rfc3339_utc(s)[:10] + "T00:00:00Z"

def optional_bool(p, name):
    # An optional boolean flag (hasCoApplicant / hasGuarantor). Absent / null /
    # non-bool degrades to False — a flag the applicant did not set is "no".
    if not hasattr(p, name):
        return False
    v = getattr(p, name)
    if v == None or type(v) != type(True):
        return False
    return v

def string_list(p, name):
    # An optional list of non-empty trimmed strings (references). Absent / null /
    # non-list → []. Non-string / blank entries are dropped (a clean list, never
    # a fail — the count is what the landlord reads).
    if not hasattr(p, name):
        return []
    v = getattr(p, name)
    if v == None or type(v) != type([]):
        return []
    out = []
    for item in v:
        if type(item) == type("") and len(item.strip()) > 0:
            out.append(item.strip())
    return out

# Standard rental qualification: gross MONTHLY income must be at least this
# multiple of the monthly rent (the conventional 3x-rent rule). The op computes
# the derived incomeToRentMet boolean here (the lens engine has no arithmetic),
# so only the boolean — never the raw income — reaches the read model.
INCOME_TO_RENT_RATIO = 3.0

# The employmentStatus enum SetApplicantProfile admits. employed / self-employed
# are the active-income states that derive employmentVerified=True; the rest are
# captured honestly but read as unverified income.
EMPLOYMENT_STATUSES = ["employed", "self-employed", "unemployed", "student", "retired"]

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

# --- workplace write confinement (facet-staff-worlds-design.md §3.5) ---------
#
# A staff actor may write only inside the location it worksAt. Three properties
# make this sound; each is a trap a simpler form falls into.
#
# 1. The exemption is ROLE-derived, never worksAt-derived. Exempting "an actor
#    with no worksAt link" would be perverse: UnwireWorksAt would WIDEN a staff
#    member's write surface from one building to everywhere. The exemption is
#    holding the primordial 'operator' role -- the same walk the kernel projects
#    its own root grant from (internal/bootstrap/lenses.go: MATCH (identity)
#    -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator'), so
#    an actor that is genuinely root necessarily has it. Everyone else is
#    confined, and an actor holding no roles at all is confined to nothing.
#
# 2. A tombstoned link is ABSENT. kv.Read returns the tombstone DOCUMENT rather
#    than None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), and
#    UnwireWorksAt tombstones rather than deletes, so the '== None' form the
#    cafe/clinic self-guards use would let a moved-on staff member keep writing.
#
# 3. The location is resolved from the TARGET's own topology, never from a
#    payload field -- a caller cannot forge which building it is writing at.
ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4
WORKPLACE_PARENT_PAGE_LIMIT = 20
MAX_PARENT_PAGES = 4
WORKPLACE_MAX_DEPTH = 8
WORKPLACE_MAX_NODES = 64

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # Paginated: a role beyond page 1 must not read as "not held" -- the walk
    # follows the cursor up to MAX_ROLE_PAGES pages before giving up, and
    # giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan. A role granted concurrently with
        # this write is not a race worth closing: it can only widen authority, and
        # the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def worksAt_covers(actor_id, location_key):
    # Answers "does this actor worksAt this location, or any LIVE location that
    # contains it?" -- a BREADTH-first walk up the containedIn topology, testing
    # the actor's deterministic worksAt link at every node. The location itself
    # is tested first, so a staff member wired to an exact unit matches too; one
    # wired to any containing building matches everything containedIn it.
    #
    # A tombstoned link OR VERTEX is absent. kv.Read returns the tombstone
    # document rather than None (step4_hydrate routes only ErrKeyNotFound to
    # knownAbsent), and UnwireWorksAt / TombstoneLocation tombstone rather than
    # delete, so isDeleted is tested explicitly in three places: the worksAt
    # link, each containedIn link, and every location VERTEX the walk stands on.
    # The vertex test is what stops a DECOMMISSIONED location from still
    # conferring authority -- TombstoneLocation does not cascade to containedIn
    # links (location-domain), so those links stay live and only the vertex's own
    # isDeleted marks it gone, while the read side stops dead there (the full
    # engine's fetchNode yields nothing for a soft-deleted node). Transiting one
    # would grant a write the reader would never show.
    #
    # It is tested on EVERY node, the caller-supplied one included, not just on
    # ancestors: a guard where a dead ancestor confers nothing but a dead
    # starting location confers everything would be exactly the kind of
    # inconsistency the next reader copies wrongly.
    #
    # EVERY parent is followed, not one per level: containment is a DAG. A walk
    # that kept a single parent would deny a staffer wired to whichever branch it
    # happened to discard, while a read-side lens projecting a covering set
    # unions every branch of [:containedIn*0..7] (cafe-domain's and
    # wellness-domain's coveringLocations are the two that do).
    #
    # Bounded three ways so an op-time guard cannot fan out: WORKPLACE_MAX_DEPTH
    # levels (0..7, the read side's hop range), WORKPLACE_PARENT_PAGE_LIMIT
    # parents per node, and WORKPLACE_MAX_NODES distinct nodes overall, a node
    # never being enqueued twice. Exhausting a bound falls through to the final
    # 'return False' -- a DENIAL, never an escape. The node budget is the one
    # bound the read side does not share (its walk caps hops, not nodes), so a
    # containment tree wide enough to exhaust it denies a write the reader would
    # show; it is set far above any real topology, and it fails closed.
    if location_key == None:
        return False
    frontier = [location_key]
    seen = [location_key]
    for _ in range(WORKPLACE_MAX_DEPTH):
        if len(frontier) == 0:
            return False
        parents = []
        for cur in frontier:
            parts = cur.split(".")
            if len(parts) != 3:
                # Not walkable. Stops its OWN branch rather than aborting the
                # walk, so one malformed ancestor cannot deny a sibling branch
                # that would have matched. A malformed location_key still
                # denies: nothing else is queued, so the frontier empties.
                continue
            # read-posture: (e) per-candidate follow-up read off the containedIn
            # enumeration below -- the location VERTEX, so a tombstoned one
            # neither confers a match nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the same
            # enumeration (data-derived key -- the ancestor chain is not
            # knowable client-side, so it cannot be pre-declared).
            lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
            if lnk != None and not lnk.isDeleted:
                return True
            # Paginated: a parent beyond page 1 must not read as "no more
            # parents" -- the walk follows the cursor up to MAX_PARENT_PAGES
            # pages before moving on, same as actor_holds_operator's role walk.
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a location has
                # at most a few parents; containment is provisioned topology, not
                # written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    # Charged to the budget at ENQUEUE, so the node count bounds the
                    # walk's reads exactly rather than to within a page, and an
                    # ancestor reachable from several branches is visited once.
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE a domain resolver
    # runs. Starlark evaluates arguments eagerly, so
    # require_workplace(resolve(x), ...) would walk the target's topology even
    # for root -- wasted reads, and worse, a malformed key anywhere in that walk
    # raises where the op previously succeeded. Call sites therefore gate on
    # this; require_workplace re-checks it anyway, so a site that forgets the
    # gate is still CORRECT, only slower.
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants, which
    # authorize via scope=any and so carry no target the platform has checked.
    # A scope=self caller is bound instead by its own op's ownership probe (the
    # applicationFor / identifiedBy indirection): a resident legitimately holds
    # no worksAt link, and confining them by a rule written for staff would deny
    # every self-service write. The two guards are complementary, not
    # alternatives -- each binds the path the other cannot see.
    #
    # The exemption keys on authTargetValidated, NOT on authContextTarget being
    # non-empty: the raw target is a client-supplied hint that any scope=any
    # holder can set, so exempting on its presence would let any staff member
    # opt out of confinement.
    if op.authTargetValidated:
        return
    enforce_workplace(location_keys, what)

def enforce_workplace(location_keys, what):
    # require_workplace minus the validated-target exemption, for a
    # resource-scoped op that has already checked for itself that the validated
    # target names the resource being acted on. Past that check the caller is an
    # ordinary staff member and must clear the worksAt walk like any other.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

LEASEAPP_UNIT_PAGE_LIMIT = 10

def vertex_live(key):
    # Is this vertex present AND not tombstoned? The standalone form of the
    # vertex test worksAt_covers performs inline at every node of its bounded
    # walk, for the resolvers that walk THROUGH a vertex to produce that walk's
    # input -- a provider, a studio, a lease. Those hops are invisible to
    # worksAt_covers: by the time it runs the dead vertex has already been
    # transited and only its live locations remain, so the confinement it
    # computes is the dead entity's ex-topology.
    #
    # A tombstone is a DOCUMENT, not an absence. kv.Read returns it rather than
    # None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), so the
    # '== None' test alone reads a tombstoned vertex as live. Both halves are
    # required, and a None key answers False so a caller that resolved nothing
    # takes the same denying branch as one that resolved something dead.
    #
    # Distinct from vertex_alive(state, key), which answers the same question
    # from the operation's DECLARED contextHint.reads. The keys here are
    # data-derived -- resolved from a link mid-walk, so unknowable client-side
    # and undeclarable -- and only a live read can see them.
    #
    if key == None:
        return False
    # read-posture: (e) one bounded read per candidate. At the sites this exists
    # for, the key is data-derived -- resolved from a kv.Links enumeration
    # mid-walk, so unknowable client-side and undeclarable. A resolver cannot
    # see which caller it has, and some callers reach it with a payload key a
    # declared read has already proved live; there this is a redundant re-proof,
    # not a second class of access. Screening at the resolver rather than per
    # call site is what keeps the rule uniform.
    node = kv.Read(key)
    return node != None and not node.isDeleted

def leaseapp_unit(app_key):
    # The unit a lease application applies to, from the application's OWN link
    # -- never a payload field, so a caller cannot forge which unit's landlord
    # (or workplace) authorizes the write. Returns None when the application
    # names no live unit, which every caller treats as a denial.
    #
    # The leaseapp VERTEX this walk transits. WithdrawLeaseApplication
    # soft-deletes it without cascading to its links, so a withdrawn
    # application must not carry the walk any further. A broken chain already
    # answered None here, so this adds an input to that branch, not a new
    # answer a caller can distinguish.
    if not vertex_live(app_key):
        return None
    # read-posture: (e) relation=appliesToUnit epoch=none -- a leaseapp carries
    # exactly one appliesToUnit link (required at CreateLeaseApplication), so
    # this is never a keyspace scan.
    page, _ = kv.Links(app_key, "appliesToUnit", "out", None, LEASEAPP_UNIT_PAGE_LIMIT)
    unit = None
    for lk in page:
        if not lk.isDeleted:
            unit = lk.targetVertex
    # The unit VERTEX. This copy feeds require_manages as well as
    # require_workplace, and require_manages tests only the manages LINK -- so
    # without this a landlord keeps deciding applications on a dead unit.
    if not vertex_live(unit):
        return None
    return unit

def free_applied_to_unit_guard(app_key, unit_key):
    # The mutations that FREE the per-(applicant, unit) duplicate-application
    # guard link (lnk.identity.<a>.appliedToUnit.unit.<u>, created or revived
    # by CreateLeaseApplication) once the application reaches a TERMINAL state
    # -- declined, lost, or its tenancy ended -- so the same applicant can
    # apply for the same unit again: the re-apply revives the tombstone
    # through CreateLeaseApplication's existing CAS path. The withdraw and
    # reassign releases are the precedent (WithdrawLeaseApplication frees the
    # pair it names; ReassignLeaseUnit frees the vacated pair); this is the
    # same tombstone for the terminal states that leave the leaseapp itself
    # alive. Returns [] when there is nothing to free: no live unit (the
    # caller's own resolution answered None), no live applicant link, or a
    # guard already absent / tombstoned. Never a refusal -- a terminal
    # record is not gated on the guard's bookkeeping.
    #
    # Accepted window: the guard is per PAIR and names no owning application,
    # so a terminal op hydrated while a concurrent loss + re-apply on the same
    # pair commits (a Decide{declined} read before the revived guard landed)
    # tombstones the NEW application's guard under the OCC pin's own revision
    # -- milliseconds wide on the default lane's >1 workers, and accepted.
    if unit_key == None:
        return []
    _, unit_id = parts_of(unit_key, "unit", "unit")
    # The applicant, from the application's OWN applicationFor link -- never
    # a payload field (ReassignLeaseUnit's resolution).
    # read-posture: (e) relation=applicationFor epoch=none -- a leaseapp
    # carries exactly one applicationFor link (required at
    # CreateLeaseApplication), so this is never a keyspace scan.
    app_page, _ = kv.Links(app_key, "applicationFor", "out", None, LEASEAPP_UNIT_PAGE_LIMIT)
    applicant = None
    for lk in app_page:
        if not lk.isDeleted:
            applicant = lk.targetVertex
    if applicant == None:
        return []
    _, applicant_id = parts_of(applicant, "applicant", "identity")
    guard_key = "lnk.identity." + applicant_id + ".appliedToUnit.unit." + unit_id
    # read-posture: (e) per-candidate follow-up read off the applicationFor
    # enumeration above + the caller's appliesToUnit walk (data-derived key).
    guard = kv.Read(guard_key)
    if guard == None or guard.isDeleted:
        return []
    # CAS on the guard's own revision: a concurrent re-apply that revived it
    # between this read and the commit conflicts instead of being tombstoned
    # out from under the new application.
    return [make_link_tombstone_occ(guard_key, applicant, unit_key, "appliedToUnit", "appliedToUnit", guard.revision)]

def require_manages(unit_key, what):
    # The landlord ownership probe -- the scope=self counterpart to
    # require_workplace above, binding the path that guard deliberately cannot
    # see. A signed-in landlord holds no worksAt link and authorizes via a
    # scope=self grant, so what confines them is their own management link to
    # the unit under the write.
    #
    # It binds the platform-VALIDATED self path and only that path, which is why
    # it keys on authTargetValidated rather than on the raw target's presence.
    # Two callers would otherwise be caught wrongly:
    #   - a scope=any holder (operator) whose client happens to send its own key
    #     -- step 3 authorizes it on the standing grant WITHOUT inspecting the
    #     target, so narrowing it here would confine an unconfined actor to
    #     whatever it happens to manage, while its READ surface stays
    #     portfolio-wide through the staff wildcard anchor;
    #   - a task grant, which also validates a target -- but the target of the
    #     §10.8 renewal tasks is the RENEWAL, not an identity, so the equality
    #     with op.actor excludes it and the task's own scoping stands.
    # A scope=self caller cannot escape it: step 3 denies scope=self outright
    # when the target is absent and denies it when target != actor, so reaching
    # this op on that grant means both conditions already hold.
    #
    # authcontext-target: (ownership) the target is used only as op.actor's own
    # key, on a path the platform already proved equal to the actor, and the
    # authority it buys is then proven by the manages LINK read below.
    if not op.authTargetValidated or op.authContextTarget != op.actor:
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    if unit_key == None:
        fail("AuthDenied: no unit resolves for this write, so no management link can authorize it; " + what)
    _, unit_id = parts_of(unit_key, "unit", "unit")
    # read-posture: (e) per-candidate follow-up read off the appliesToUnit
    # enumeration above (data-derived key -- the unit is not knowable until the
    # application's own link resolves, so it cannot be pre-declared).
    lnk = kv.Read("lnk.identity." + actor_id + ".manages.unit." + unit_id)
    if lnk == None or lnk.isDeleted:
        # The unit key is deliberately NOT named: the caller reached here with a
        # resource key it already holds, and echoing the unit that resource
        # belongs to would turn a denial into a lookup for a resource it does
        # not own.
        fail("AuthDenied: " + op.actor + " does not manage the unit this write is for; " + what)

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateLeaseApplication":
        applicant = required_string(p, "applicant")
        _, applicant_id = parts_of(applicant, "applicant", "identity")

        # No-orphan invariant (FR29 / P4): the applicant identity MUST be alive.
        # An application pointing at a non-existent applicant is never committed.
        if not vertex_alive(state, applicant):
            fail("UnknownApplicant: " + applicant)

        # The application MUST name the unit it applies to (§7 Q2): a unit-less
        # application can never exist, so there is no unactuatable missing_unit
        # gap to wedge Weaver — the convergence lens reads the unit's listing /
        # address as informational columns ("what am I applying to lease"). The
        # unit is a location-domain vtx.unit.<NanoID>, alive-checked here (so the
        # caller must list it in ContextHint.Reads).
        unit = required_string(p, "unit")
        _, unit_id = parts_of(unit, "unit", "unit")
        if not vertex_alive(state, unit):
            fail("UnknownUnit: " + unit)

        # Applicant-self (consumer's scope=self grant only): step 3 authorizes
        # scope=self by checking authContext.target == actor (Contract #6),
        # but never looks at payload.applicant — a consumer could satisfy that
        # check while naming a DIFFERENT identity as the applicant. The script
        # closes that gap by requiring authContextTarget == applicant whenever
        # authContextTarget is present. It is empty for the standing operator
        # grant (scope=any, the installer/test/orchestrator path, which never
        # sets authContext), so this check is a no-op there — operator keeps
        # submitting CreateLeaseApplication on behalf of any applicant, exactly
        # as its own grant (unconstrained by scope) already allows.
        # authcontext-target: (payload-bind) the target must equal
        # payload.applicant; on a CREATE there is no owning link to probe yet,
        # so a forged target only narrows what the caller may create.
        if op.authContextTarget != "" and op.authContextTarget != applicant:
            fail("AuthDenied: an applicant may only create an application for themselves")

        # leaseAppId is a caller-supplied write-ahead seam (mirrors
        # service-domain's instanceId). Absent → mint internally. CreateOnly
        # semantics make a crash-retry with the same id collapse on the
        # Contract #4 tracker.
        app_id = bare_nanoid_or_mint(p, "leaseAppId")
        app_key = "vtx.leaseapp." + app_id

        # applicationFor: the leaseapp (later-arriving) is the source, the
        # pre-existing identity is the target (Contract #1 §1.1). Reads as
        # "this application is for this applicant."
        app_for_lnk = "lnk.leaseapp." + app_id + ".applicationFor.identity." + applicant_id

        # appliesToUnit: the leaseapp (later-arriving) is the source, the
        # pre-existing unit is the target (Contract #1 §1.1). Reads as
        # "this application applies to this unit." The convergence lens walks it.
        applies_to_lnk = "lnk.leaseapp." + app_id + ".appliesToUnit.unit." + unit_id

        # Per-(applicant, unit) live-application guard (Capability-KV §06 — the
        # operation's own Starlark logic; no platform scan, no frozen contract). The
        # constraint is pure existence-uniqueness — at most ONE live application per
        # applicant+unit (a unit accepts many DIFFERENT applicants: normal leasing,
        # the landlord chooses) — so it needs no list: a DETERMINISTIC guard LINK
        # keyed on the pair IS the constraint (relationships are links, never keys in
        # an aspect — Contract #1). lnk.identity.<a>.appliedToUnit.unit.<u> reads as
        # "this applicant applied to this unit" (§1.1: the link is the later-arriving
        # fact; source = the applicant, target = the unit):
        #   - alive  → DuplicateApplication (the applicant already has a live one).
        #   - absent → make_link (op:create) is the guard: two concurrent first-applies
        #              both create, the second RevisionConflicts on the key (fail closed).
        #   - tombstoned (a prior withdraw freed it) → REVIVE via CAS (a blind create
        #              would collide with the tombstone — revive-on-create), CAS-guarded
        #              so two concurrent re-applies fail closed.
        guard_key = "lnk.identity." + applicant_id + ".appliedToUnit.unit." + unit_id
        # read-posture: (d) declared optionalReads at CreateLeaseApplication
        # dispatch — absent is the common first-apply case.
        guard = kv.Read(guard_key)
        if guard != None and not guard.isDeleted:
            fail("DuplicateApplication: applicant " + applicant + " already has a live application for unit " + unit)
        if guard != None:
            guard_mut = make_link_revive_occ(guard_key, applicant, unit, "appliedToUnit", "appliedToUnit", guard.revision)
        else:
            guard_mut = make_link(guard_key, applicant, unit, "appliedToUnit", "appliedToUnit", {})

        # Root data minimal (D5): {} on root. The applicant + unit are links; the
        # status/gaps are lens-computed, never stored.
        mutations = [
            make_vtx(app_key, "leaseapp", {}),
            make_link(app_for_lnk, app_key, applicant, "applicationFor", "applicationFor", {}),
            make_link(applies_to_lnk, app_key, unit, "appliesToUnit", "appliesToUnit", {}),
            # The per-(applicant, unit) uniqueness guard link — created, or revived
            # from a prior withdraw's tombstone (CAS). See the guard logic above.
            guard_mut,
        ]

        # .terms (D3): the applicant's requested lease terms — additive
        # application detail for the applicant FE / operator (the convergence lens
        # does NOT read it). Written only when moveInDate is supplied, so a bare
        # applicant+unit application stays valid; moveInDate present ⇒
        # leaseTermMonths required (a half-specified terms block is rejected);
        # requestedRent is optional.
        move_in = optional_string(p, "moveInDate")
        if move_in != None:
            # The terms are load-bearing: DecideLeaseApplication's first approve
            # signs the lease on them, so a malformed term is refused where it
            # is minted. moveInDate is stored NORMALIZED — a bare YYYY-MM-DD
            # read as midnight UTC, anything else parsed as RFC3339 (a date
            # that parses as neither is refused by the parse itself) — so
            # .terms always carries the RFC3339 instant the DDL states.
            # leaseTermMonths is a whole, positive month count (a fraction
            # would be silently truncated by the approve's add_months);
            # requestedRent, when supplied, is a positive amount.
            move_in = time.rfc3339_utc(as_rfc3339_instant(move_in))
            # The unit's own .listing feeds two checks on a dated application:
            # the availability floor just below and the rent fallback further
            # down. Read once here. Same key + idiom SetApplicantProfile already
            # reads for its income-to-rent check.
            # read-posture: (d) declared optionalReads at CreateLeaseApplication
            # dispatch — a unit with no listing yet has no date to floor on and
            # no rent to fall back to.
            listing = kv.Read(unit + ".listing")
            if listing == None or listing.isDeleted:
                listing = None
            # A move-in before the unit is available is refused where the terms
            # are minted, not clamped: the terms are the applicant's reviewed
            # ask, and DecideLeaseApplication's approve commits to them verbatim
            # (its own MoveInBeforeAvailable is the same refusal at the reader).
            # The compare is by UTC CALENDAR DAY: every surface promises a day
            # (a move-in is a day; the listing's availableFrom may be a bare
            # date, a midnight instant, or a wall-clock instant a landlord's
            # datetime control produced), so both sides canonicalize to an
            # instant and their YYYY-MM-DD slices are compared — a move-in ON
            # the available day is admitted whatever the listing's time of day.
            if listing != None:
                available_from = listing.data.get("availableFrom")
                if type(available_from) == type("") and len(available_from) > 0:
                    available_at = time.rfc3339_utc(as_rfc3339_instant(available_from))
                    if move_in[:10] < available_at[:10]:
                        fail("MoveInBeforeAvailable: a move-in of " + move_in[:10] + " is before unit " + unit + " is available from " + available_at[:10] + " (UTC day)")
            term_months = require_number(p, "leaseTermMonths")
            if term_months != int(term_months) or int(term_months) < 1:
                fail("InvalidTerms: leaseTermMonths must be a whole, positive month count, got " + str(term_months))
            terms_data = {"moveInDate": move_in, "leaseTermMonths": int(term_months)}
            req_rent = None
            if hasattr(p, "requestedRent") and getattr(p, "requestedRent") != None:
                req_rent = optional_number(p, "requestedRent")
                if req_rent == None or req_rent <= 0:
                    fail("InvalidTerms: requestedRent must be a positive amount, got " + str(getattr(p, "requestedRent")))
                req_rent = two_decimals(req_rent, "requestedRent")
            if req_rent == None and listing != None:
                # No rent offer from the applicant — fall back to the unit's own
                # listed rent (the .listing read above), so leaseRentSettlementSpec
                # (semantic-contracts) has a requestedRent to gate missing_account
                # on.
                r = listing.data.get("rentAmount")
                if r != None and (type(r) == type(0) or type(r) == type(0.0)) and r > 0:
                    req_rent = r
            if req_rent != None:
                terms_data["requestedRent"] = req_rent
            mutations.append(make_aspect(app_key, "terms", "terms", terms_data))

        events = [{"class": "leaseapp.applicationCreated",
                   "data": {"leaseAppKey": app_key, "applicant": applicant, "unit": unit}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "SignLease":
        app_key = required_string(p, "leaseAppKey")
        parts_of(app_key, "leaseAppKey", "leaseapp")

        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # Sign once: the .signature aspect is written CreateOnly, so a second
        # SignLease with a different requestId conflicts and is rejected. When
        # the caller lists the now-existing .signature key in ContextHint.Reads,
        # the state is hydrated and this explicit check fires first, upgrading
        # the rejection to a structured AlreadySigned ScriptError.
        sig_key = app_key + ".signature"
        if vertex_alive(state, sig_key):
            fail("AlreadySigned: " + app_key)

        # The gap that dispatches this op (missing_signature, lenses.go) only
        # fires while the unit is still available to THIS application:
        # (unitStatus <> 'leased') OR (landlordDecision = 'approved'). Weaver
        # stops DISPATCHING once that flips false, but does not RETRACT a
        # grant already handed out, so a task dispatched before the unit
        # leased to a rival (or was tombstoned) keeps a live signable grant
        # until its own expiry. Re-verify the same condition here rather than
        # trust the grant alone -- the live bug this closes: 13 rival
        # applicants held a signable grant on a unit already leased to
        # someone else, 6 more on a since-tombstoned unit.
        #
        unit_key = leaseapp_unit(app_key)
        if unit_key == None:
            fail("UnitNoLongerAvailable: application " + app_key + " names no live unit; cannot sign")
        # read-posture: (e) per-candidate follow-up read off the appliesToUnit
        # enumeration leaseapp_unit() just walked -- mirrors
        # DecideLeaseApplication's own resolution of the unit + its .listing.
        listing = kv.Read(unit_key + ".listing")
        unit_status = None
        if listing != None and not listing.isDeleted:
            unit_status = listing.data.get("status")
        # read-posture: (d) declared optionalReads at SignLease dispatch --
        # absent is the common not-yet-decided case.
        decision = kv.Read(app_key + ".decision")
        decision_value = None
        if decision != None and not decision.isDeleted:
            decision_value = decision.data.get("value")
        if unit_status == "leased" and decision_value != "approved":
            fail("UnitNoLongerAvailable: unit " + unit_key + " is already leased to another applicant; application " + app_key + " was not the one approved")
        # A recorded loss outlives the unit's status: once the winner's
        # tenancy ends and the unit relists, the live check above reads it
        # available again, but this application lost it -- a grant dispatched
        # before the loss was recorded stays unsignable.
        if decision_value == "lost":
            fail("UnitNoLongerAvailable: application " + app_key + " lost its unit to another applicant")

        # Snapshot the tenant's name onto the lease at the moment it becomes an
        # executed contract: a signed lease is a legal document that names its
        # tenant, and Contract #3 §3.10 makes that conformant ("a contract
        # record keeps its parties' names for as long as the contract must be
        # kept") -- unlike the live .name on the applicant's own identity, this
        # copy is custodied on the executedLeaseRecord retention class, so it
        # survives the applicant's own ShredIdentityKey. The applicant, from
        # the application's OWN applicationFor link -- never a payload field.
        # read-posture: (e) relation=applicationFor epoch=none -- a leaseapp
        # carries exactly one applicationFor link (required at
        # CreateLeaseApplication), so this is never a keyspace scan.
        app_page, _ = kv.Links(app_key, "applicationFor", "out", None, LEASEAPP_UNIT_PAGE_LIMIT)
        applicant = None
        for lk in app_page:
            if not lk.isDeleted:
                applicant = lk.targetVertex
        tenant_name_mutations = []
        if applicant != None:
            # The decrypt needs the applicant identity's live, un-shredded
            # .piiKey -- probe it FIRST, because kv.Read of a SENSITIVE aspect
            # FAILS (ScriptFailed) rather than degrading when the key envelope
            # is shredded, and a shredded key envelope stays PRESENT with
            # data.shredded=true (privacy-base's ShredIdentityKey updates it
            # in place rather than deleting it) -- so presence alone is not
            # enough, the probe must also check the flag.
            # read-posture: (e) per-candidate follow-up read off the
            # applicationFor enumeration above (data-derived key).
            pii_key_node = kv.Read(applicant + ".piiKey")
            pii_key_live = pii_key_node != None and not pii_key_node.isDeleted
            shredded = pii_key_live and pii_key_node.data.get("shredded")
            if pii_key_live and not shredded:
                # read-posture: (e) per-candidate follow-up read off the
                # applicationFor enumeration above (data-derived key).
                name_node = kv.Read(applicant + ".name")
                if name_node != None and not name_node.isDeleted:
                    name_val = name_node.data.get("value")
                    if name_val != None and type(name_val) == type("") and len(name_val.strip()) > 0:
                        tenant_name_mutations = [make_aspect(app_key, "tenantName", "tenantName", {"value": name_val.strip()})]
        # Absent, tombstoned, or blank name, a crypto-shredded applicant, or no
        # applicant at all: write nothing. The executed-lease document degrades
        # to the bare applicant key.

        # The signature is a fact in an aspect (D5); the application root stays
        # {}. signedAt is the op's own timestamp, normalized to canonical UTC so
        # a downstream lexical compare is sound.
        signed_at = time.rfc3339_utc(op.submittedAt)
        mutations = [
            make_aspect(app_key, "signature", "signature", {"signedAt": signed_at}),
        ] + tenant_name_mutations
        events = [{"class": "leaseapp.leaseSigned",
                   "data": {"leaseAppKey": app_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "DecideLeaseApplication":
        # The landlord's leasing decision — the human gate the listing-flip waits
        # behind. Validates the application is a live leaseapp, validates the decision
        # enum, enforces the decision lifecycle guards (below), and writes a .decision
        # aspect {value, decidedAt} on the leaseapp. The convergence lens reads
        # app.decision.data.value: approved opens missing_listingLeased (→ the unit
        # leases); declined is a terminal disposition (declined OR'd in the lens).
        # On the FIRST decision of either value, it also CREATE-ONLY-stamps a
        # .decidedProfileSnapshot aspect preserving the qualification profile as
        # it stood at that moment — the fair-housing record of what the
        # landlord actually saw (below).
        app_key = required_string(p, "leaseAppKey")
        _, app_id = parts_of(app_key, "leaseAppKey", "leaseapp")

        # Confinement, before anything is validated or revealed: whichever path
        # authorized this write, it is bound to the application's OWN unit. Staff
        # on the standing path must worksAt a location covering it; a landlord on
        # the self path must manage it. The unit comes from the application's own
        # appliesToUnit link -- the payload 'unit' field below is verified against
        # that same link, but it is only read on the approve branch, and a
        # confinement gate must bind a DECLINE just as tightly.
        #
        # The walk is unconditional because BOTH guards below consume it, and
        # each binds the path the other cannot see -- so there is no caller for
        # whom it is dead weight except an operator, who pays one bounded link
        # enumeration. It cannot raise on a reachable input either: app_key is
        # already parsed above, and the only key-shape parse of what it returns
        # lives past require_manages's self-action early return.
        decide_unit = leaseapp_unit(app_key)
        # workplace-exempt: (ownership-bound) this IS the ownership proof -- it
        # requires the acting landlord to manage the unit the application's own
        # link names, so the validated scope=self path never reaches the write
        # unconfined. It answers ahead of the liveness check below so a caller
        # who manages nothing cannot use a denial to learn that an application
        # exists.
        require_manages(decide_unit, "cannot decide application " + app_key)
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)
        # workplace-exempt: (ownership-bound) require_manages above binds the
        # scope=self path to this same unit. NOTE the OTHER validated path:
        # workplace_exempt() keys on op.authTargetValidated, which a TASK grant
        # also sets -- and require_manages returns early there, because a task's
        # target is the task's resource, not the acting identity. This op carries
        # an op-meta, so a CreateTask forOperation it would reach the write with
        # BOTH confinements off. No playbook mints one today; add a resource bind
        # here before any does.
        if not workplace_exempt():
            # workplace-exempt: (ownership-bound) same discharge as the pre-gate
            # above -- re-stated because the intervening statement puts it out of
            # annotation range.
            require_workplace([decide_unit], "cannot decide application " + app_key)

        decision = required_string(p, "decision")
        if decision != "approved" and decision != "declined":
            fail("BadDecision: " + decision)

        # Terminal-decision guard: a recorded decision is FINAL. Re-submitting the SAME
        # decision stays accepted (idempotent / re-run-safe under at-least-once);
        # changing a recorded decision to a DIFFERENT value is rejected — an approved or
        # declined application must not silently flip or oscillate (the verified live
        # bug: approved→declined committed freely). Reconsidering a recorded decision
        # is a future explicit re-open op, not a silent overwrite.
        # read-posture: (d) declared optionalReads at DecideLeaseApplication
        # dispatch — absent is the common first-decide case.
        prior = kv.Read(app_key + ".decision")
        if prior != None and not prior.isDeleted:
            prior_val = prior.data.get("value")
            if prior_val != None and prior_val != decision:
                fail("DecisionFinal: application " + app_key + " is already " + str(prior_val) + "; a recorded decision is terminal and cannot be changed to " + decision)

        # Approve-readiness floor: a landlord must not APPROVE an application the
        # applicant has not yet SIGNED (the verified live bug: a profileSubmitted=false
        # application could be approved, producing a misleading "Approved" the
        # convergence lens can never lease). Signing is the applicant's final
        # commitment step; an unsigned application is not ready for an approval. This is
        # a cheap, SOUND floor — deliberately NOT the full applicantApproved gate
        # (.ssn + a fresh completed bgcheck + a completed payment + the signature),
        # which is a lens-derived signal spanning the identity + its providedTo service
        # instances with freshness windows; reproducing that cross-vertex computation in
        # this write-path op would duplicate read-model logic and risk op↔lens
        # divergence. The convergence lens still enforces the FULL gate before the unit
        # actually leases (missing_listingLeased), so an approve here can never lease an
        # unqualified applicant. A DECLINE carries no readiness floor — a landlord may
        # decline at any point.
        if decision == "approved":
            # read-posture: (d) declared optionalReads at DecideLeaseApplication
            # dispatch — an unsigned application is the fail branch, not absence.
            sig = kv.Read(app_key + ".signature")
            if sig == None or sig.isDeleted:
                fail("NotReadyToApprove: application " + app_key + " has not been signed by the applicant; cannot approve an unsigned application")

        # decidedAt is the op's own timestamp, normalized to canonical UTC (read-free,
        # mirroring SignLease's signedAt) so a downstream lexical compare is sound.
        decided_at = time.rfc3339_utc(op.submittedAt)
        # reason is optional free-text the landlord supplies with a decline (applicant
        # feedback + a fair-housing record). It is stored on the .decision aspect only
        # when supplied; an approve or a reasonless decline carries none. A same-value
        # re-submission (idempotent) can attach / update the reason on the already-
        # recorded decision. The convergence lens projects it as declineReason.
        decision_data = {"value": decision, "decidedAt": decided_at}
        reason = optional_string(p, "reason")
        if reason != None:
            decision_data["reason"] = reason
        mutations = [
            make_aspect_upsert(app_key, "decision", "decision", decision_data),
        ]

        # A decline is a terminal state, so the FIRST decline frees the
        # per-(applicant, unit) guard link and the applicant may apply for the
        # unit again (free_applied_to_unit_guard). Only the first: a same-value
        # re-submission after a re-apply must not tombstone the NEW
        # application's revived guard. An approve keeps the pair -- it is the
        # executed lease -- until its tenancy ends (EndTenancy frees it).
        if decision == "declined" and (prior == None or prior.isDeleted or prior.data.get("value") == None):
            mutations += free_applied_to_unit_guard(app_key, decide_unit)

        # .tenancy: the tenancy-term fact stamped exactly once, on the FIRST
        # approve — CREATE-ONLY (a re-approve of an already-terminal decision is
        # idempotent at the DecisionFinal guard above, but even a same-value
        # re-submission must never re-derive .tenancy and silently truncate a
        # SignRenewal-extended leaseEnd back to the original term, design §4.1).
        # Read the unit via decide_unit — the leaseapp's OWN appliesToUnit
        # target, already resolved above for confinement — never a payload
        # field, so a caller cannot forge which unit's listing feeds the term
        # math (Standard §readTemplateDebt: a payload-conditional unit field
        # can only ever build a malformed read key on a decline or re-approve,
        # where it is absent by design). The term itself is derived from the
        # APPLICANT'S OWN .terms first — the approval commits to what the
        # applicant asked for, verbatim — and falls back field-by-field to
        # the unit's .listing only where .terms carries nothing (a bare
        # applicant+unit application with no moveInDate). A requested move-in
        # BEFORE the listing's availableFrom is REFUSED (MoveInBeforeAvailable),
        # never clamped to it: clamping would sign a lease on terms the
        # landlord did not read, and the floor on availableFrom (the
        # tenancyEnd target's FloorListingAvailability) only ever raises the
        # date, so a start it admits is one the unit is genuinely free for.
        # The bare application takes availableFrom itself and is equal by
        # construction.
        if decision == "approved":
            # read-posture: (d) declared optionalReads at DecideLeaseApplication
            # dispatch — None is the expected, common first-approve case.
            existing_tenancy = kv.Read(app_key + ".tenancy")
            if existing_tenancy == None or existing_tenancy.isDeleted:
                # appliesToUnit is required at CreateLeaseApplication (no
                # unit-less application, §3 D5), so a live application always
                # names exactly one unit, and require_manages above already
                # failed closed (AuthDenied) when decide_unit resolved to None
                # on the scope=self path. The only caller who can reach here
                # with decide_unit == None is an operator/staff caller on an
                # application whose link somehow broke — reject the same as an
                # unlistable unit rather than crash on a missing .listing read.
                if decide_unit == None:
                    fail("NoListing: application " + app_key + " names no live unit; cannot compute a tenancy term")

                # read-posture: (d) declared optionalReads at DecideLeaseApplication
                # dispatch — absent is the bare applicant+unit application.
                terms = kv.Read(app_key + ".terms")
                terms_move_in = None
                terms_term_months = None
                terms_rent = None
                if terms != None and not terms.isDeleted:
                    terms_move_in = terms.data.get("moveInDate")
                    terms_term_months = terms.data.get("leaseTermMonths")
                    terms_rent = terms.data.get("requestedRent")

                # read-posture: (e) per-candidate follow-up read off the
                # appliesToUnit enumeration leaseapp_unit() already walked
                # above — decide_unit is the resolved, live unit key, not a
                # payload placeholder that would build a malformed key when
                # absent. Still read even when .terms supplies every field —
                # the listing is the fallback, and rentAmount's own fallback
                # (below) always needs it.
                listing = kv.Read(decide_unit + ".listing")
                if listing == None or listing.isDeleted:
                    fail("NoListing: unit " + decide_unit + " has no .listing aspect; cannot compute a tenancy term")
                available_from = listing.data.get("availableFrom")
                term_months = listing.data.get("leaseTermMonths")

                move_in = terms_move_in if terms_move_in != None else available_from
                term = terms_term_months if terms_term_months != None else term_months
                if move_in == None or term == None:
                    fail("NoListing: unit " + decide_unit + "'s .listing is missing availableFrom/leaseTermMonths")
                # The term is signed on these values, so a non-positive count
                # — whichever source supplied it — is refused rather than
                # stamped as a lease that ends before it starts.
                if type(term) != type(0) and type(term) != type(0.0):
                    fail("InvalidTerms: leaseTermMonths must be a positive month count, got " + str(term))
                if int(term) < 1:
                    fail("InvalidTerms: leaseTermMonths must be a positive month count, got " + str(term))

                # moveInDate / availableFrom may be a bare "YYYY-MM-DD" (the FE
                # normalizes to RFC3339, but seed-showcase / seed-classic-demo
                # and Priya Raman's live pending application do not) —
                # time.rfc3339_utc itself rejects a bare date.
                lease_start = time.rfc3339_utc(as_rfc3339_instant(move_in))
                # The applicant's own requested start must not precede the
                # unit's availability (the same refusal CreateLeaseApplication
                # raises when the terms are minted; re-proven here because the
                # listing's date may have been floored since). Compared by UTC
                # CALENDAR DAY, as at the writer: a start ON the available day
                # is admitted whatever the listing's time of day.
                if terms_move_in != None and type(available_from) == type("") and len(available_from) > 0:
                    available_at = time.rfc3339_utc(as_rfc3339_instant(available_from))
                    if lease_start[:10] < available_at[:10]:
                        fail("MoveInBeforeAvailable: application " + app_key + " asks to move in " + lease_start[:10] + " but unit " + decide_unit + " is available from " + available_at[:10] + " (UTC day)")
                # A lease term is a calendar-month count (12 months from Jan
                # 31 is Jan 31 of next year, never a fixed hour count), and
                # the builtin clamps the day-of-month to the target month's
                # length (Jan 31 + 1 month = Feb 28/29).
                lease_end = time.rfc3339_add_months(lease_start, int(term))
                renewal_opens_at = time.rfc3339_add(lease_end, "-__RENEWAL_WINDOW__")

                tenancy_data = {"leaseStart": lease_start, "leaseEnd": lease_end, "renewalOpensAt": renewal_opens_at}

                # rentAmount: the applicant's own offered rent first, else the
                # unit's listed rent (mirroring CreateLeaseApplication's own
                # listing-rent fallback above); omitted entirely when neither
                # exists, so leaseRentSettlementSpec's coalesce(.tenancy.rentAmount,
                # .terms.requestedRent) still resolves the same way it does today.
                # Both sources pass the same positive-number test: an offered
                # rent of zero or less (or a non-number) is not an agreed rent
                # and falls through to the listing's, never into rentAmount.
                rent = None
                if terms_rent != None and (type(terms_rent) == type(0) or type(terms_rent) == type(0.0)) and terms_rent > 0:
                    rent = terms_rent
                if rent == None:
                    r = listing.data.get("rentAmount")
                    if r != None and (type(r) == type(0) or type(r) == type(0.0)) and r > 0:
                        rent = r
                if rent != None:
                    tenancy_data["rentAmount"] = rent

                mutations.append(make_aspect(app_key, "tenancy", "tenancy", tenancy_data))

                # .deposit: the security-deposit figure the tenancy owes,
                # recorded at THIS approval event from the listing's own
                # depositAmount — CREATE-ONLY, in the same mutation batch as
                # .tenancy (one event, both or neither). It is its own
                # aspect, not a .tenancy field: SignRenewal rewrites .tenancy
                # wholesale from a fixed five-field list, and a field bolted
                # on there would vanish on the first renewal. The listing is
                # a mutable relation a landlord can edit after approval, so
                # the figure is captured here rather than read live at
                # return time — a later listing edit must not re-price a
                # signed lease's deposit. No aspect when the listing carries
                # no positive depositAmount (the unit takes no deposit).
                deposit_amount = listing.data.get("depositAmount")
                if deposit_amount != None and (type(deposit_amount) == type(0) or type(deposit_amount) == type(0.0)) and deposit_amount > 0:
                    mutations.append(make_aspect(app_key, "deposit", "leaseDeposit", {"amount": deposit_amount, "recordedAt": decided_at}))

        # .decidedProfileSnapshot: the fair-housing preservation record —
        # stamped exactly once, on the FIRST .decision write of EITHER value
        # (approve OR decline; a decline is the more fair-housing-salient
        # case, and a declined application stays just as rewritable) —
        # CREATE-ONLY, mirroring the .tenancy read-then-create-only idiom
        # above FOR REAL: the gate reads the SNAPSHOT'S OWN key
        # (existing_snapshot below), never .decision (prior, above), because
        # the commit path only lets a losing concurrent CREATE gracefully
        # retry/no-op instead of hard-rejecting the whole mutation batch
        # (including the otherwise-fine .decision write) when the create's
        # OWN key was declared optionalReads and observed absent at step 4
        # (commit_path.go's absentConditionedCreates) — gating on a read of a
        # DIFFERENT key (.decision) leaves this create unconditioned from the
        # commit path's point of view, so two concurrent first-decides (e.g.
        # a double-clicked approve/decline button) would both pass the gate
        # and the loser would hard-reject instead of harmlessly no-opping.
        # SetApplicantProfile stays a freely re-submittable upsert (a
        # landlord may ask an applicant to update details after deciding, a
        # renewal cycle re-submits it years later), so without this snapshot
        # the record of what the landlord actually saw when THEY decided is
        # lost the moment a later submission overwrites .profile /
        # .underwritingParties / .applicationSignals. A sibling if to the
        # approve-only .tenancy block above (NOT nested inside the decision
        # == "approved" branch): this must fire on BOTH approve and decline.
        # read-posture: (d) declared optionalReads at DecideLeaseApplication
        # dispatch — None is the expected, common first-decision case.
        existing_snapshot = kv.Read(app_key + ".decidedProfileSnapshot")
        if existing_snapshot == None or existing_snapshot.isDeleted:
            # read-posture: (d) declared optionalReads at DecideLeaseApplication
            # dispatch — a decision reached before any profile was ever
            # submitted is the expected absent case, not an error: a landlord
            # may decide before SetApplicantProfile is ever called, and the
            # snapshot then captures an empty/partial record rather than
            # failing the decision.
            profile = kv.Read(app_key + ".profile")
            # read-posture: (d) declared optionalReads at DecideLeaseApplication
            # dispatch — same absence tolerance as .profile above.
            underwriting_parties = kv.Read(app_key + ".underwritingParties")
            # read-posture: (d) declared optionalReads at DecideLeaseApplication
            # dispatch — same absence tolerance as .profile above.
            application_signals = kv.Read(app_key + ".applicationSignals")
            snapshot_data = {
                "profile": profile.data if profile != None and not profile.isDeleted else {},
                "underwritingParties": underwriting_parties.data if underwriting_parties != None and not underwriting_parties.isDeleted else {},
                "applicationSignals": application_signals.data if application_signals != None and not application_signals.isDeleted else {},
            }
            mutations.append(make_aspect(app_key, "decidedProfileSnapshot", "decidedProfileSnapshot", snapshot_data))

        events = [{"class": "leaseapp.applicationDecided",
                   "data": {"leaseAppKey": app_key, "decision": decision}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "WithdrawLeaseApplication":
        # Withdraw / cancel an application: soft-delete the leaseapp so it drops
        # from My Applications (the convergence lens anchors on it + filters
        # isDeleted → EmptyBehavior delete), and — when the application is
        # UNDECIDED — FREE the per-(applicant, unit) guard link so it stops
        # blocking a re-apply. The complement to CreateLeaseApplication's guard
        # — an applicant who applied to the wrong unit can back out + re-apply
        # (the guard revives on re-apply). A declined or lost application had
        # its pair freed at the terminal decision, so the guard, if alive,
        # belongs to a later application on the same pair and is left alone.
        app_key = required_string(p, "leaseAppKey")
        _, app_id = parts_of(app_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # The unit + applicant the application is for (the FE carries both on the
        # row). Each is verified as genuinely THIS application's endpoint via its
        # deterministic leaseapp-anchored link (kv.Read) — mirroring clinic's
        # withProvider check — so a wrong / fabricated unit or applicant can't be
        # used to free a different pair's guard. The (applicant, unit) pair then
        # reconstructs the guard-link key deterministically.
        #
        # ORDER IS PART OF THE GUARD. Both verifications answer differently for a
        # real endpoint than a wrong one, so each is a probe: run either ahead of
        # the applicant binding and a consumer holding nothing but their own
        # scope=self grant walks a stranger's application — naming candidate
        # units until UnitMismatch stops (learning which unit it applies to),
        # then candidate applicants until ApplicantMismatch stops (learning who
        # applied). Both spaces are small and enumerable, so the probes are a
        # directed search, not a guess. Binding the caller to the applicant they
        # NAME costs no read at all, and the applicationFor read then proves that
        # applicant is this application's — so by the time the unit probe runs,
        # the caller has proven the application is their own and the probe tells
        # them only what they already know.
        applicant = required_string(p, "applicant")
        _, applicant_id = parts_of(applicant, "applicant", "identity")

        # Applicant-self (consumer's scope=self grant only): step 3 authorizes
        # scope=self by checking authContext.target == actor (Contract #6), but
        # never looks at the payload — a consumer could satisfy that check while
        # naming a DIFFERENT identity as the applicant and free someone else's
        # guard. Requiring authContextTarget == applicant binds the acting
        # identity to the named endpoint, which the applicationFor read below
        # then proves is this application's: a consumer withdraws only their own
        # application. The operator path (no authContext, scope=any — the
        # trusted-tool / orchestrator) stays unconstrained, mirroring
        # CreateLeaseApplication's applicant-self guard.
        # authcontext-target: (ownership) the target must be the applicant the
        # payload names, verified below as this application's applicationFor
        # endpoint, so a forged one only fails closed.
        if op.authContextTarget != "" and op.authContextTarget != applicant:
            fail("AuthDenied: an applicant may only withdraw their own application")

        app_for_lnk = "lnk.leaseapp." + app_id + ".applicationFor.identity." + applicant_id
        # read-posture: (a) declared reads at WithdrawLeaseApplication dispatch
        # (validation link; absence — ApplicantMismatch — is a caller error).
        alink = kv.Read(app_for_lnk)
        if alink == None or alink.isDeleted:
            fail("ApplicantMismatch: " + applicant + " is not the applicant of application " + app_key)

        unit = required_string(p, "unit")
        _, unit_id = parts_of(unit, "unit", "unit")
        applies_to_lnk = "lnk.leaseapp." + app_id + ".appliesToUnit.unit." + unit_id
        # read-posture: (a) declared reads at WithdrawLeaseApplication dispatch
        # (validation link; absence — UnitMismatch — is a caller error).
        ulink = kv.Read(applies_to_lnk)
        if ulink == None or ulink.isDeleted:
            fail("UnitMismatch: " + unit + " is not the unit application " + app_key + " applies to")

        # An approved application is an executed lease: its account (heldFor),
        # balance and rent clause hang off it and the unit is leased, so it is
        # never withdrawn — the FE hides the button, and the API refuses the
        # same way. A declined or lost application stays withdrawable — it
        # drops from My Applications — but its pair was already freed when the
        # terminal decision was recorded (free_applied_to_unit_guard), so the
        # guard is left untouched below: alive, it is a LATER application's.
        # read-posture: (d) declared optionalReads at WithdrawLeaseApplication
        # dispatch — absent is the undecided application, the normal withdraw.
        decision = kv.Read(app_key + ".decision")
        decided = decision != None and not decision.isDeleted and decision.data.get("value") != None
        if decided and decision.data.get("value") == "approved":
            fail("AlreadyApproved: application " + app_key + " is an executed lease and cannot be withdrawn")

        # Tombstone the application. The applicationFor / appliesToUnit links are
        # left in place (non-cascading tombstone, the clinic-domain precedent) — they
        # dangle off a tombstoned anchor every reader filters.
        mutations = [make_vtx_tombstone(app_key, "leaseapp")]

        # Free the per-(applicant, unit) guard link of an UNDECIDED application:
        # tombstone it so a re-apply revives it. UNCONDITIONED (the withdraw is
        # the authority the application is gone; an alive guard blocks any
        # concurrent re-apply, so no revive races this). absent → nothing to
        # free. The guard key is deterministic per pair, so once a decided
        # application's pair has been freed and the applicant has re-applied,
        # the same key is the NEW application's live guard — the decided
        # branch never touches it, or a third application on the pair would be
        # admitted past DuplicateApplication.
        guard_key = "lnk.identity." + applicant_id + ".appliedToUnit.unit." + unit_id
        # read-posture: (d) declared optionalReads at WithdrawLeaseApplication
        # dispatch — a never-guarded (legacy) application is the absent branch.
        guard = kv.Read(guard_key)
        if not decided and guard != None and not guard.isDeleted:
            mutations.append(make_link_tombstone(guard_key, applicant, unit, "appliedToUnit", "appliedToUnit"))

        events = [{"class": "leaseapp.applicationWithdrawn",
                   "data": {"leaseAppKey": app_key, "unit": unit}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "ReassignLeaseUnit":
        # Operator repair for an application whose unit died out from under
        # it (TombstoneLocation does not cascade — the SetMenuItemLocation /
        # ReassignSession repair shape), and an ordinary move of a live
        # application to a different unit besides. Re-points appliesToUnit
        # and, for a LIVE application, re-keys the per-(applicant, unit)
        # duplicate-application guard: the guard's contract is "at most one
        # live application per (applicant, unit)", so a moved lease VACATES
        # its old pair — that guard is freed (tombstoned) alongside the new
        # pair's guard going live, or a later re-apply / return to the old
        # unit would collide with a guard nothing about that pair still
        # justifies. A TERMINAL application (declined, lost, tenancy ended)
        # is re-pointed only — it holds no pair (below).
        lease_app_key = required_string(p, "leaseAppKey")
        _, app_id = parts_of(lease_app_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, lease_app_key):
            fail("UnknownLeaseApplication: " + lease_app_key)

        new_unit = required_string(p, "newUnitKey")
        _, new_unit_id = parts_of(new_unit, "newUnitKey", "unit")
        if not vertex_alive(state, new_unit):
            fail("UnknownUnit: " + new_unit)

        # The application's CURRENT appliesToUnit link. Needs the whole link
        # struct (key + revision), not just the target, so this is inlined
        # rather than reusing leaseapp_unit().
        # read-posture: (e) relation=appliesToUnit epoch=none -- a leaseapp
        # carries exactly one appliesToUnit link (required at
        # CreateLeaseApplication), so this is never a keyspace scan.
        page, _ = kv.Links(lease_app_key, "appliesToUnit", "out", None, LEASEAPP_UNIT_PAGE_LIMIT)
        current = None
        for lk in page:
            if not lk.isDeleted:
                current = lk
        if current == None:
            fail("InvalidState: ReassignLeaseUnit: " + lease_app_key + " carries no live appliesToUnit link")

        # Already there — accept harmlessly rather than reject, the same
        # no-op posture the repair's shipped precedents use for a repeat
        # repair. An empty response omits primaryKey — the reply-constraint
        # requires primaryKey to lie within the write footprint (Contract #3),
        # and a no-op commits none (AssignUnitOwner's own already-managed
        # no-op, loftspace-domain/ownership.go).
        if current.targetVertex == new_unit:
            return {"mutations": [], "events": [], "response": {}}

        # The applicant, from the application's OWN applicationFor link —
        # never a payload field. NOT required to be alive: the repair must
        # work for any lease, including one whose applicant has since been
        # removed.
        # read-posture: (e) relation=applicationFor epoch=none -- a leaseapp
        # carries exactly one applicationFor link (required at
        # CreateLeaseApplication), so this is never a keyspace scan.
        app_page, _ = kv.Links(lease_app_key, "applicationFor", "out", None, LEASEAPP_UNIT_PAGE_LIMIT)
        applicant = None
        for lk in app_page:
            if not lk.isDeleted:
                applicant = lk.targetVertex
        if applicant == None:
            fail("InvalidState: ReassignLeaseUnit: " + lease_app_key + " carries no live applicationFor link")
        _, applicant_id = parts_of(applicant, "applicant", "identity")

        old_unit = current.targetVertex
        _, old_unit_id = parts_of(old_unit, "oldUnit", "unit")

        # The guard is per LIVE pair, and a TERMINAL application — declined
        # or lost (.decision), or with its tenancy ended (.tenancy.endedAt) —
        # holds no pair: its guard was freed when the terminal state was
        # recorded (free_applied_to_unit_guard), so an alive guard on its old
        # pair is a LATER application's on that pair, and a guard on its new
        # pair would be minted for an application nothing will ever free. A
        # terminal application is therefore re-pointed only: no vacated-pair
        # tombstone, no new-pair guard, no DuplicateApplication check. Both
        # aspects are declared OptionalReads — absent is the undecided, live
        # application, the ordinary move.
        # read-posture: (d) declared optionalReads at ReassignLeaseUnit
        # dispatch — absent is the undecided application.
        decision = kv.Read(lease_app_key + ".decision")
        # read-posture: (d) declared optionalReads at ReassignLeaseUnit
        # dispatch — absent is the never-approved application.
        tenancy = kv.Read(lease_app_key + ".tenancy")
        terminal = False
        if decision != None and not decision.isDeleted and decision.data.get("value") in ["declined", "lost"]:
            terminal = True
        if tenancy != None and not tenancy.isDeleted and tenancy.data.get("endedAt") != None:
            terminal = True

        guard_muts = []
        if not terminal:
            # Per-(applicant, new unit) duplicate-application guard — the same
            # three-way block CreateLeaseApplication runs on a first apply
            # (guard logic there).
            guard_key = "lnk.identity." + applicant_id + ".appliedToUnit.unit." + new_unit_id
            # read-posture: (e) per-candidate follow-up read off the
            # applicationFor enumeration above (data-derived key).
            guard = kv.Read(guard_key)
            if guard != None and not guard.isDeleted:
                fail("DuplicateApplication: applicant " + applicant + " already has a live application for unit " + new_unit)
            if guard != None:
                guard_muts.append(make_link_revive_occ(guard_key, applicant, new_unit, "appliedToUnit", "appliedToUnit", guard.revision))
            else:
                guard_muts.append(make_link(guard_key, applicant, new_unit, "appliedToUnit", "appliedToUnit", {}))

            # The VACATED (applicant, old unit) guard: the pair the live
            # application no longer applies to, once this commits. Freed when
            # alive — nothing about the old pair still justifies holding it
            # once the application has moved on.
            old_guard_key = "lnk.identity." + applicant_id + ".appliedToUnit.unit." + old_unit_id
            # read-posture: (e) per-candidate follow-up read off the
            # appliesToUnit + applicationFor enumerations above (data-derived key).
            old_guard = kv.Read(old_guard_key)
            if old_guard != None and not old_guard.isDeleted:
                guard_muts.append(make_link_tombstone_occ(old_guard_key, applicant, old_unit, "appliedToUnit", "appliedToUnit", old_guard.revision))

        new_applies_to_lnk = "lnk.leaseapp." + app_id + ".appliesToUnit.unit." + new_unit_id
        mutations = [
            # Revision-pinned: two concurrent re-points to different units
            # must not both land, or the leaseapp ends up with two live
            # appliesToUnit links — the second CAS fails with
            # RevisionConflicts instead.
            make_link_tombstone_occ(current.key, lease_app_key, old_unit, "appliesToUnit", "appliesToUnit", current.revision),
            make_link_create_or_revive(new_applies_to_lnk, lease_app_key, new_unit, "appliesToUnit", "appliesToUnit"),
        ] + guard_muts
        events = [{"class": "leaseapp.unitReassigned",
                   "data": {"leaseAppKey": lease_app_key, "oldUnitKey": old_unit, "newUnitKey": new_unit}}]
        # primaryKey is the NEW appliesToUnit link, not leaseAppKey: every
        # mutation here is relational (no write ever touches the leaseapp
        # vertex or one of its aspects), and the reply-constraint requires
        # primaryKey to lie within the committed write footprint (Contract
        # #3) — the same link-as-primaryKey shape AssignUnitOwner returns for
        # its own link-only mutation (loftspace-domain/ownership.go).
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": new_applies_to_lnk}}

    if ot == "SetApplicantProfile":
        # The applicant's qualification profile — the data a landlord decides on.
        # Split THREE ways along the retention-class-key-custody-design.md §9.1
        # sensitivity boundary (step 6.5 encrypts a whole aspect's data map, so a
        # non-sensitive field sharing a sensitive aspect's data goes unreadable to
        # every plain lens — co-locating raw and derived facts is not an option):
        #   .profile (SENSITIVE, underwritingRecord retention class) — the
        #     applicant's OWN raw financials (annualIncome, employmentStatus,
        #     employerName, references, guarantorRelationship, guarantorAnnualIncome).
        #   .underwritingParties (SENSITIVE, SAME class, separate aspect) — the
        #     guarantor's / co-applicant's OWN identifiers (guarantorName,
        #     coApplicantName, coApplicantContact): a third party who never applied
        #     and has no identity of their own to custody this on (§8.7).
        #   .applicationSignals (NON-sensitive) — the DERIVED booleans/counts the
        #     three shipped lenses project (incomeToRentMet, employmentVerified,
        #     referenceCount, hasCoApplicant, hasGuarantor, guarantorIncomeToRentMet,
        #     submittedAt), so the landlord sees qualification without the raw
        #     figures or the third-party identities.
        # All three are written in ONE mutation batch. Each sensitive aspect's
        # written key set is STABLE — an omitted optional string writes as "" rather
        # than being dropped, and a field is omitted only when it is structurally
        # absent (no guarantor ⇒ no guarantor fields at all) — so a future per-field
        # secure column never meets a missing key. Re-submittable: an UNCONDITIONED
        # upsert overwrites all three aspects.
        app_key = required_string(p, "leaseAppKey")
        _, app_id = parts_of(app_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # Applicant-self (consumer's scope=self grant only): step 3 authorizes
        # scope=self by checking authContext.target == actor (Contract #6), but
        # never looks at which application is being profiled — a consumer could
        # satisfy that check while naming SOMEONE ELSE's application. The script
        # closes that gap by requiring the acting identity to be THIS
        # application's applicant, verified via the deterministic applicationFor
        # link keyed on the actor's own id (no payload applicant field to forge).
        # The operator path (no authContext, scope=any — the trusted-tool)
        # stays unconstrained, mirroring CreateLeaseApplication's self guard.
        # authcontext-target: (ownership) the target must be this application's
        # applicationFor endpoint, so a forged one only fails closed.
        if op.authContextTarget != "":
            _, self_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
            self_app_lnk = "lnk.leaseapp." + app_id + ".applicationFor.identity." + self_id
            # read-posture: (a) declared reads at SetApplicantProfile dispatch on
            # the consumer path (validation link; absence — AuthDenied — means
            # the caller is not this application's applicant).
            self_link = kv.Read(self_app_lnk)
            if self_link == None or self_link.isDeleted:
                fail("AuthDenied: an applicant may only set the profile on their own application")

        # The unit the application applies to — needed to read its listing rent for
        # the income-to-rent derivation. Verify it is genuinely THIS application's
        # unit via the deterministic appliesToUnit link (kv.Read, the Withdraw /
        # clinic withProvider precedent) so a wrong / fabricated unit can't be used.
        unit = required_string(p, "unit")
        _, unit_id = parts_of(unit, "unit", "unit")
        applies_to_lnk = "lnk.leaseapp." + app_id + ".appliesToUnit.unit." + unit_id
        # read-posture: (a) declared reads at SetApplicantProfile dispatch
        # (validation link; absence — UnitMismatch — is a caller error).
        link = kv.Read(applies_to_lnk)
        if link == None or link.isDeleted:
            fail("UnitMismatch: " + unit + " is not the unit application " + app_key + " applies to")

        annual_income = require_number(p, "annualIncome")
        if annual_income <= 0:
            fail("InvalidArgument: annualIncome: required positive number")
        employment = required_string(p, "employmentStatus")
        if employment not in EMPLOYMENT_STATUSES:
            fail("InvalidArgument: employmentStatus: must be one of employed, self-employed, unemployed, student, retired; got " + employment)
        employer = optional_string(p, "employerName")
        refs = string_list(p, "references")
        has_co = optional_bool(p, "hasCoApplicant")
        has_guarantor = optional_bool(p, "hasGuarantor")

        # Derived qualification signals (the lens has no arithmetic / len, so they
        # are computed here). employmentVerified = an active income source;
        # referenceCount = how many references were supplied.
        employment_verified = employment == "employed" or employment == "self-employed"
        ref_count = len(refs)

        # The unit's monthly listing rent, read ON DEMAND (kv.Read §2.5). None
        # when the unit has no listing / no positive rent (an income-to-rent
        # signal is then genuinely unknown, not false). Read at submit time
        # against the rent then-current; a later rent change is reflected on
        # the next SetApplicantProfile. The applicant AND the guarantor
        # income-to-rent checks both derive from it.
        rent = None
        # read-posture: (d) declared optionalReads at SetApplicantProfile
        # dispatch — unlike a true (c) config read, unit.listing is a
        # per-request payload-derivable key (script-read-posture-design.md §13
        # hard case 4: DecideLeaseApplication/SetListingStatus read the SAME
        # key required; this call's absence-tolerance is its own semantics,
        # not a reason to treat the key itself as undeclarable config).
        listing = kv.Read(unit + ".listing")
        if listing != None and not listing.isDeleted:
            r = listing.data.get("rentAmount")
            if r != None and (type(r) == type(0) or type(r) == type(0.0)) and r > 0:
                rent = r

        # income-to-rent: gross MONTHLY income ≥ 3× rent (the conventional rule).
        income_to_rent_met = None
        if rent != None:
            income_to_rent_met = (annual_income * 1.0) / 12.0 >= INCOME_TO_RENT_RATIO * rent

        # .profile (SENSITIVE, underwritingRecord class): the applicant's OWN raw
        # financials. employerName is ALWAYS present (STABLE shape) — "" when not
        # supplied, never dropped.
        profile_data = {
            "annualIncome":     annual_income,
            "employmentStatus": employment,
            "employerName":     employer if employer != None else "",
        }

        # underwritingParties (SENSITIVE, SAME class, separate aspect): third-party
        # identifier data — the applicant's references (who THEY name, e.g. "Prior
        # landlord — Jane Doe", not what the applicant earns) plus the guarantor's /
        # co-applicant's OWN identifiers — a population who never applied and has no
        # identity of their own to custody this on (§8.7). references is omitted when
        # the applicant supplied none (an empty list is nothing to custody).
        underwriting_parties_data = {}
        if len(refs) > 0:
            underwriting_parties_data["references"] = refs

        # Guarantor detail. profile_data carries guarantorRelationship /
        # guarantorAnnualIncome (they describe the APPLICANT's qualification story,
        # not a third-party identity); underwriting_parties_data carries
        # guarantorName (the third party's OWN identifier). Both are STRUCTURALLY
        # ABSENT (the whole group omitted) when there is no guarantor at all, and
        # STABLE (always present, "" / 0 default) when hasGuarantor is true. The
        # ONE derived, projectable signal is guarantorIncomeToRentMet — does the
        # guarantor's OWN income cover 3× the rent (the standard reason a guarantor
        # backs a thin-income application), derived from the same rent read above so a
        # landlord can lean on "guarantor covers 3× rent" rather than a bare ✓ on a
        # below-income applicant. Omitted (not written) when no listing rent.
        guarantor_income_to_rent_met = None
        if has_guarantor:
            g_name = optional_string(p, "guarantorName")
            g_rel = optional_string(p, "guarantorRelationship")
            g_income = optional_number(p, "guarantorAnnualIncome")
            g_income_valid = g_income != None and g_income > 0
            underwriting_parties_data["guarantorName"] = g_name if g_name != None else ""
            profile_data["guarantorRelationship"] = g_rel if g_rel != None else ""
            profile_data["guarantorAnnualIncome"] = g_income if g_income_valid else 0
            if g_income_valid and rent != None:
                guarantor_income_to_rent_met = (g_income * 1.0) / 12.0 >= INCOME_TO_RENT_RATIO * rent

        # Co-applicant detail — both fields are the third party's OWN identifiers,
        # so both go to underwritingParties. Structurally absent (the whole group
        # omitted) when there is no co-applicant at all.
        if has_co:
            c_name = optional_string(p, "coApplicantName")
            c_contact = optional_string(p, "coApplicantContact")
            underwriting_parties_data["coApplicantName"] = c_name if c_name != None else ""
            underwriting_parties_data["coApplicantContact"] = c_contact if c_contact != None else ""

        # .applicationSignals (NON-sensitive): the DERIVED booleans/counts the
        # three shipped lenses project. incomeToRentMet / guarantorIncomeToRentMet
        # are omitted (not written) when the signal is genuinely unknown (no
        # listing rent / no guarantor), never written false.
        signals_data = {
            "employmentVerified": employment_verified,
            "referenceCount":     ref_count,
            "hasCoApplicant":     has_co,
            "hasGuarantor":       has_guarantor,
            "submittedAt":        time.rfc3339_utc(op.submittedAt),
        }
        if income_to_rent_met != None:
            signals_data["incomeToRentMet"] = income_to_rent_met
        if guarantor_income_to_rent_met != None:
            signals_data["guarantorIncomeToRentMet"] = guarantor_income_to_rent_met

        # Unconditioned upsert of all three aspects IN ONE BATCH (the clinic
        # RecordEncounter precedent) — a re-submit overwrites every one, and no
        # path ever writes one without the others. .underwritingParties is
        # written even when it carries no field (an unconditioned upsert of
        # {}) rather than being skipped: skipping it would leave a PRIOR
        # submission's guarantorName/coApplicant*/references stale on a
        # re-submit that drops them — TestSetApplicantProfile pins that a
        # guarantor-less re-submit clears underwritingParties.guarantorName,
        # which an omitted mutation cannot do without first reading the
        # aspect it would otherwise blindly skip.
        mutations = [make_aspect_upsert(app_key, "profile", "applicantProfile", profile_data),
                     make_aspect_upsert(app_key, "underwritingParties", "underwritingParties", underwriting_parties_data),
                     make_aspect_upsert(app_key, "applicationSignals", "applicationSignals", signals_data)]
        events = [{"class": "leaseapp.profileSubmitted",
                   "data": {"leaseAppKey": app_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "BackfillLeaseTerms":
        # Operator-granted, mirroring BackfillPatientRegistration's shape
        # (clinic-domain): an approved leaseapp with no requestedRent — a
        # .terms aspect never written, or written before
        # CreateLeaseApplication's unit-listing-rent fallback existed
        # (0.31.14) — bills nothing, since every downstream ledger step
        # (leaseRentSettlementSpec's account/clause gaps, semantic-contracts)
        # requires an agreed rent before it opens. leaseRentSettlementSpec's
        # own missing_terms gap dispatches this op automatically over every
        # such lease; an operator may also run it by hand.
        app_key = required_string(p, "leaseAppKey")
        parts_of(app_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # read-posture: (d) declared optionalReads at BackfillLeaseTerms
        # dispatch — absent is the common not-yet-backfilled case (the whole
        # reason this op exists); a .terms aspect that already carries
        # requestedRent is the already-repaired case.
        terms = kv.Read(app_key + ".terms")
        existing_move_in = None
        existing_term_months = None
        if terms != None and not terms.isDeleted:
            if terms.data.get("requestedRent") != None:
                # Already backfilled (or never needed it) — no-op cleanly
                # rather than reject, mirroring BackfillPatientRegistration's
                # own already-present no-op. No primaryKey: an empty write
                # footprint has nothing for the reply-constraint to validate
                # it against.
                return {"mutations": [], "events": [], "response": {}}
            existing_move_in = terms.data.get("moveInDate")
            existing_term_months = terms.data.get("leaseTermMonths")

        # The unit this application applies to, from the application's OWN
        # appliesToUnit link — never a payload field (leaseapp_unit's own
        # forgery-resistance rationale above). Absent/dead would be a
        # structural break of the no-orphan invariant (FR29): every leaseapp
        # is minted with a live appliesToUnit link and nothing ever tombstones
        # just the link.
        unit_key = leaseapp_unit(app_key)
        if unit_key == None:
            fail("UnitNoLongerAvailable: application " + app_key + " names no live unit; cannot backfill terms")

        # Same key + fallback idiom CreateLeaseApplication's own requestedRent
        # fallback reads above — the unit's own listed rent.
        # read-posture: (e) follow-up read off the appliesToUnit enumeration
        # leaseapp_unit() just walked.
        listing = kv.Read(unit_key + ".listing")
        rent = None
        if listing != None and not listing.isDeleted:
            r = listing.data.get("rentAmount")
            if r != None and (type(r) == type(0) or type(r) == type(0.0)) and r > 0:
                rent = r
        if rent == None:
            fail("NoRentSource: unit " + unit_key + " carries no listed rent to backfill application " + app_key + " with")

        # Rebuilt from the fields .terms can ever carry (mirrors
        # BackfillPatientRegistration's own literal reconstruction) rather
        # than copying terms.data wholesale.
        merged = {"requestedRent": rent}
        if existing_move_in != None:
            merged["moveInDate"] = existing_move_in
        if existing_term_months != None:
            merged["leaseTermMonths"] = existing_term_months
        mutations = [make_aspect_upsert(app_key, "terms", "terms", merged)]
        events = [{"class": "leaseapp.termsBackfilled",
                   "data": {"leaseAppKey": app_key, "requestedRent": rent}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "EndTenancy":
        # Weaver's service-actor directOp (the OpenRenewal / SetListingStatus
        # precedent), dispatched by the tenancyEnd target once a signed,
        # approved tenancy's term has lapsed — and runnable by an operator by
        # hand. It records that the term ended, ON ITS OWN END DATE: endedAt
        # is the term's effective end (termEnd — the recorded move-out when a
        # GiveNotice precedes leaseEnd, else leaseEnd), never the instant
        # this op ran (a recorded value is read as the fact it records — the
        # cards and the TenancyEnded refusal name the end date). The
        # write-path honesty check is its own: it does not trust the
        # dispatcher's clock, so a submission ahead of termEnd is refused
        # NotYetEnded whatever the lens said. It does NOT walk renewals — the
        # open-renewal hold is the lens's dispatch gate (tenancy_end_lenses.go);
        # an operator ending a term under an open cycle is admitted, and
        # SignRenewal then refuses TenancyEnded rather than dropping the
        # endedAt. Recording the end also frees the per-(applicant, unit)
        # guard link (the walk off the application's own links, below).
        app_key = required_string(p, "leaseAppKey")
        parts_of(app_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # The .tenancy is a REQUIRED declared read (the descriptor's
        # Dispatch.Reads and the target's row.entityKey.tenancy both list it):
        # the gap only opens on a leaseapp with a tenancy, so absence is a
        # wiring fault, and the op refuses it rather than lazily reading —
        # read from state, which holds only what the submitter declared, so
        # an empty contextHint is refused here and never served by an
        # on-demand GET. The hydrated revision is what the OCC pin below
        # rests on.
        tenancy_key = app_key + ".tenancy"
        tenancy = state[tenancy_key] if tenancy_key in state else None
        if tenancy == None or tenancy.isDeleted:
            fail("NoTenancy: application " + app_key + " has no .tenancy aspect; there is no term to end")
        lease_end = tenancy.data.get("leaseEnd")
        if lease_end == None or type(lease_end) != type(""):
            fail("NoTenancy: application " + app_key + "'s .tenancy aspect is missing leaseEnd")

        # Idempotent: an at-least-once re-dispatch of an already-ended term
        # emits NOTHING — no mutation, no event. No primaryKey: an empty
        # write footprint has nothing for the reply-constraint to validate
        # it against (the SetListingStatus no-op shape).
        if tenancy.data.get("endedAt") != None:
            return {"mutations": [], "events": [], "response": {}}

        # The term's effective end. A recorded notice (.notice, written once
        # by GiveNotice) whose moveOutAt precedes leaseEnd ends the term
        # there — the same termEnd the tenancyEnd lens arms its timer on. The
        # aspect is a declared OptionalRead (the descriptor's OptionalReads
        # and the target's row.entityKey.notice), read from state exactly as
        # .tenancy is: a submitter that never declared it gets the documented
        # fallback — the term ends at leaseEnd, and a submission before that
        # is refused NotYetEnded — rather than a lazy GET that would end the
        # term on a fact the envelope never named.
        # read-posture: (d) declared optionalReads at EndTenancy dispatch — a
        # lease with no notice is the common case.
        notice_key = app_key + ".notice"
        notice = state[notice_key] if notice_key in state else None
        end = lease_end
        if notice != None and not notice.isDeleted:
            move_out_at = notice.data.get("moveOutAt")
            if move_out_at != None and type(move_out_at) == type("") and move_out_at < lease_end:
                end = move_out_at

        # Both stamps are canonical-UTC RFC3339 (leaseEnd is rfc3339_utc /
        # rfc3339_add_months output, moveOutAt is rfc3339_utc output —
        # fixed-width and zero-padded), so the string comparison orders
        # chronologically. The refusal names the end by its UTC calendar date
        # — the same YYYY-MM-DD slice every .tenancy stamp renders by (a
        # midnight-UTC instant reads as the day before west of Greenwich, so
        # no local-zone date ever appears).
        submitted_at = time.rfc3339_utc(op.submittedAt)
        if submitted_at < end:
            fail("NotYetEnded: lease " + app_key + " runs until " + end[:10] + " (UTC)")

        # Every existing field preserved (leaseStart / renewalOpensAt, and a
        # renewed term's termStart / rentAmount), endedAt added — pinned to
        # the hydrated revision so a SignRenewal extension that committed
        # between this op's hydration and its commit conflicts instead of
        # being overwritten with an end it no longer has.
        ended = {}
        for k in tenancy.data:
            ended[k] = tenancy.data[k]
        ended["endedAt"] = end
        # An ended tenancy is the application's terminal state: the
        # per-(applicant, unit) guard is freed in the same batch so the former
        # tenant may apply for the unit again (free_applied_to_unit_guard --
        # the residence design's re-approval-on-the-same-unit case revives
        # the pair). The unit comes from the application's OWN appliesToUnit
        # link; a unit that no longer resolves (tombstoned, or the link
        # broken) means there is no pair left to free, never a refusal -- the
        # end is recorded regardless. The already-ended no-op arm above never
        # reaches this.
        mutations = [make_aspect_update_occ(app_key, "tenancy", "tenancy", ended, tenancy.revision)] + free_applied_to_unit_guard(app_key, leaseapp_unit(app_key))
        events = [{"class": "leaseapp.tenancyEnded",
                   "data": {"leaseAppKey": app_key, "leaseEnd": lease_end, "endedAt": end}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "GiveNotice":
        # A tenant (or their landlord, or an operator) records that the
        # tenancy ends EARLY, on a move-out date inside the term. The notice
        # is a recorded fact on the leaseapp — .notice = {moveOutAt, givenAt,
        # givenBy}, its own aspect rather than a .tenancy field, since
        # .tenancy already has two whole-aspect writers (DecideLeaseApplication,
        # SignRenewal) and a third would put the notice under SignRenewal's
        # rewrite. It is written ONCE (create-only; a change of date is a
        # different op): from here the term's effective end is termEnd =
        # min(moveOutAt, leaseEnd) — the tenancyEnd lens arms its timer on
        # it and EndTenancy records endedAt = termEnd; SignRenewal refuses
        # NoticeGiven; leaseExpiry opens no renewal cycle.
        #
        # Under an OPEN renewal cycle the notice is ADMITTED and walks no
        # renewals: the tenant declines by leaving. The lens-side override
        # (tenancy_end_lenses.go: missing_tenancyEnded opens on a recorded
        # notice even while openRenewalCount > 0) ends the term on the
        # move-out, SignRenewal's NoticeGiven refusal keeps the open cycle
        # from extending it, and renewalComplete's (tenancyEndedAt = null)
        # gate closes the cycle once endedAt lands.
        app_key = required_string(p, "leaseAppKey")
        _, app_id = parts_of(app_key, "leaseAppKey", "leaseapp")
        move_out_at = required_date_instant(p, "moveOutDate")

        # Who is giving notice — the probe that admits the caller names the
        # recorded givenBy, and the probes answer AHEAD of the liveness check
        # so a caller who is neither tenant nor landlord cannot use a denial
        # to learn that an application exists.
        #
        # The op carries two grants: operator at scope=any and consumer at
        # scope=self. On the platform-VALIDATED self path (step 3 proved
        # authContext.target == actor on a scope=self grant — the exact
        # predicate require_manages binds on, so the two probes cannot
        # disagree about which path they are on) the caller is the TENANT
        # when the deterministic applicationFor link keyed on the acting
        # identity is live, else the LANDLORD when require_manages proves a
        # manages link to the application's own unit — it fails AuthDenied
        # itself on that path when the link is absent. A scope=any holder
        # (the operator, the trusted-tool app) is admitted on the standing
        # grant without either link and is recorded as such, whatever
        # authContext its client happened to send.
        given_by = "operator"
        # authcontext-target: (ownership) the target is used only as
        # op.actor's own key on a path the platform already proved equal to
        # the actor; the authority it buys is then proven by the
        # applicationFor link read here or the manages link require_manages
        # reads, so a forged one only fails closed.
        if op.authTargetValidated and op.authContextTarget == op.actor:
            _, self_id = parts_of(op.actor, "actor", "identity")
            # read-posture: (d) declared optionalReads at GiveNotice dispatch
            # on the consumer path — absent means the caller is not this
            # application's tenant and the landlord probe answers next.
            tenant_lnk = kv.Read("lnk.leaseapp." + app_id + ".applicationFor.identity." + self_id)
            if tenant_lnk != None and not tenant_lnk.isDeleted:
                given_by = "tenant"
            else:
                # workplace-exempt: (ownership-bound) this IS the ownership
                # proof -- it requires the acting identity to manage the unit
                # the application's own appliesToUnit link names, so the
                # validated scope=self path never reaches the write
                # unconfined; no staff grant reaches this op at all.
                require_manages(leaseapp_unit(app_key), "cannot give notice on application " + app_key)
                given_by = "landlord"

        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # The term the notice shortens: .tenancy and .signature are REQUIRED
        # declared reads (the descriptor's Dispatch.Reads), read from state
        # exactly as EndTenancy reads .tenancy — a notice on an application
        # that is not an approved, signed lease is refused, never served by
        # an on-demand GET.
        tenancy_key = app_key + ".tenancy"
        tenancy = state[tenancy_key] if tenancy_key in state else None
        if tenancy == None or tenancy.isDeleted:
            fail("NoTenancy: application " + app_key + " has no .tenancy aspect; there is no term to give notice on")
        lease_start = tenancy.data.get("leaseStart")
        lease_end = tenancy.data.get("leaseEnd")
        if lease_end == None or type(lease_end) != type("") or lease_start == None or type(lease_start) != type(""):
            fail("NoTenancy: application " + app_key + "'s .tenancy aspect is missing leaseStart or leaseEnd")
        sig_key = app_key + ".signature"
        sig = state[sig_key] if sig_key in state else None
        if sig == None or sig.isDeleted or sig.data.get("signedAt") == None:
            fail("LeaseNotSigned: application " + app_key + " has not been signed; an unsigned lease has no term to give notice on")
        ended_at = tenancy.data.get("endedAt")
        if ended_at != None:
            fail("TenancyEnded: lease " + app_key + " ended on " + str(ended_at)[:10] + " (UTC); an ended term cannot be given notice")

        # Once only. The aspect is a declared OptionalRead, so a declared
        # absence conditions the create below CreateOnly and a second notice
        # racing this one conflicts; a submitter that declared it and finds
        # it live is refused here by name.
        # read-posture: (d) declared optionalReads at GiveNotice dispatch —
        # absent is the common first-notice case.
        notice_key = app_key + ".notice"
        prior = state[notice_key] if notice_key in state else None
        if prior != None and not prior.isDeleted:
            fail("NoticeAlreadyGiven: lease " + app_key + " already gave notice for " + str(prior.data.get("moveOutAt"))[:10] + " (UTC); a recorded notice is not changed here")

        # The date rules, every stamp canonical-UTC RFC3339 (rfc3339_utc /
        # rfc3339_add_months output), compared as strings. "Today" is the
        # UTC calendar day of the op's own submittedAt — the platform's
        # timestamp, never a client clock — so a same-day move-out is
        # admitted (the tenant who already left records today) and every
        # refusal names its date by the same YYYY-MM-DD slice the cards
        # render. The move-out must fall strictly inside the term: on or
        # before leaseStart there is no tenancy to leave, on or after
        # leaseEnd the term ends on its own date and there is nothing to
        # record.
        submitted_at = time.rfc3339_utc(op.submittedAt)
        today_start = submitted_at[:10] + "T00:00:00Z"
        if move_out_at < today_start:
            fail("MoveOutBeforeToday: move-out " + move_out_at[:10] + " (UTC) is before today " + today_start[:10] + " (UTC); a notice records a move-out from today on")
        if move_out_at <= lease_start:
            fail("MoveOutBeforeStart: move-out " + move_out_at[:10] + " (UTC) is not after the term's start " + lease_start[:10] + " (UTC)")
        if move_out_at >= lease_end:
            fail("MoveOutAfterEnd: move-out " + move_out_at[:10] + " (UTC) is not before the term's end " + lease_end[:10] + " (UTC); the term ends on its own date")

        # The .tenancy rewrite below is a SERIALIZATION POINT, not a data
        # write: it carries the aspect's own data back unchanged, pinned to
        # the revision this op hydrated. GiveNotice and SignRenewal read each
        # other's aspect (.notice / .tenancy) but each writes only its own,
        # and the commit path conditions only the keys a batch mutates — so
        # without a shared key a SignRenewal hydrated before this notice
        # commits would still land a signed, extended term on a lease under
        # notice. Pinning .tenancy makes the two conflict instead: a
        # SignRenewal that committed between this op's hydration and its
        # commit RevisionConflicts this notice (the client re-reads the new
        # leaseEnd and asks again), and a notice that committed first
        # conflicts the signing (which then re-runs and refuses NoticeGiven).
        unchanged = {}
        for k in tenancy.data:
            unchanged[k] = tenancy.data[k]
        mutations = [
            make_aspect(app_key, "notice", "tenancyNotice",
                        {"moveOutAt": move_out_at, "givenAt": submitted_at, "givenBy": given_by}),
            make_aspect_update_occ(app_key, "tenancy", "tenancy", unchanged, tenancy.revision),
        ]
        events = [{"class": "leaseapp.noticeGiven",
                   "data": {"leaseAppKey": app_key, "moveOutAt": move_out_at, "givenBy": given_by}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "RecordApplicationLoss":
        # Weaver's service-actor directOp (the EndTenancy precedent), dispatched
        # by leaseApplicationComplete's missing_lossRecorded gap once the unit
        # an undecided application applies to has leased to someone else — and
        # runnable by an operator by hand via the CLI (the primordial admin, as
        # EndTenancy). It records the loss as a THIRD
        # terminal value of the landlord's own .decision aspect, {value: lost,
        # decidedAt}, so every consumer that reads a recorded decision as
        # terminal (DecisionFinal, the applicant gaps' 'lost' conjunct,
        # lost_to_rival) reads this one the same way, and the winner's later
        # tenancy end + relist revives no rival. No reason and no
        # .decidedProfileSnapshot: nobody decided this application. The unit
        # comes from the application's OWN link, never a payload field, and
        # the premise is re-verified from state rather than trusted off the
        # dispatching row: an operator cannot mark an application lost against
        # a unit that is not leased.
        app_key = required_string(p, "leaseAppKey")
        parts_of(app_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # Any recorded decision is an idempotent no-op — no mutation, no event,
        # no primaryKey (the EndTenancy / SetListingStatus no-op shape): 'lost'
        # is the at-least-once re-dispatch, 'approved' / 'declined' mean the
        # landlord decided it and there is nothing to record. A refusal here
        # would burn Weaver's retry budget on a race the lens already closed.
        # read-posture: (d) declared optionalReads at RecordApplicationLoss
        # dispatch — absent is the gap's own premise, and the declared absence
        # conditions the write below CreateOnly.
        prior = kv.Read(app_key + ".decision")
        if prior != None and not prior.isDeleted and prior.data.get("value") != None:
            return {"mutations": [], "events": [], "response": {}}

        unit_key = leaseapp_unit(app_key)
        if unit_key == None:
            fail("NoUnit: application " + app_key + " names no live unit; there is no unit to have lost")
        # read-posture: (e) per-candidate follow-up read off the appliesToUnit
        # enumeration leaseapp_unit() just walked -- mirrors SignLease's own
        # resolution of the unit + its .listing.
        listing = kv.Read(unit_key + ".listing")
        unit_status = None
        if listing != None and not listing.isDeleted:
            unit_status = listing.data.get("status")
        if unit_status != "leased":
            fail("UnitNotLeased: application " + app_key + " applies to unit " + unit_key + " whose listing is " + str(unit_status) + "; only a leased unit records a loss")

        # decidedAt is the instant the platform recorded the loss — the same
        # stamp DecideLeaseApplication writes, normalized to canonical UTC. The
        # write is a CREATE (the SignLease .signature shape), so the step-4
        # declared absence conditions it: a landlord decision that lands
        # between this op's hydration and its commit conflicts instead of being
        # overwritten with a loss, and the re-hydrated retry reads it as
        # decided — the no-op above. An upsert would silently win that race.
        lost = {"value": "lost", "decidedAt": time.rfc3339_utc(op.submittedAt)}
        # A loss is terminal: the per-(applicant, unit) guard is freed in the
        # same batch, so the losing applicant may apply for the unit again once
        # it relists (free_applied_to_unit_guard; the no-op arm above leaves an
        # already-recorded loss's guard alone).
        mutations = [make_aspect(app_key, "decision", "decision", lost)] + free_applied_to_unit_guard(app_key, unit_key)
        events = [{"class": "leaseapp.applicationLost",
                   "data": {"leaseAppKey": app_key, "unitKey": unit_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    fail("leaseapp DDL: unknown operationType: " + ot)
`, "__RENEWAL_WINDOW__", renewalWindow, 1)

// leaseServiceInstanceDDLScript is the externalTask instanceOp. It mints the
// claim vertex vtx.service.<handle> (the same shape 14.1's service instance
// uses, reusing its .outcome aspect shape downstream), records the family
// discriminator, mints the instanceOf link to this DDL's own meta-vertex (the
// write-gate type authority, and the key TombstoneSupersededLeaseServiceInstance
// reads as its ownership proof) + the providedTo link the lens hops, and emits
// the external.<adapter> event off its own transactional outbox.
//
// event_data.params is resolve_subject_params(p.params, subject_key)
// (orchestration-base's shared helper, prepended below): the backgroundCheck
// pattern's name/dob subject.*.data.value templates resolve here — both
// identity-domain aspects are sensitive, so Loom's inferExternalTaskReads
// declared them under egressReads (not reads), and the Processor hydrated
// them as $sensitiveRef markers (never plaintext) rather than the plain
// reads/optionalReads decrypt-on-hydrate path — the resolver only recognizes
// the marker the Processor authored, so plaintext can never leak into this
// event even if a future template targets a sensitive field (design
// sensitive-param-egress §3.2/§3.3). params.family stays a literal
// (collectPayment's "payment", this pattern's "backgroundCheck") — it never
// starts with "subject." so resolve_subject_params passes it through
// unchanged.
const leaseServiceInstanceDDLScript = `
` + orchestrationbase.ResolveSubjectParamsHelper + `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    # The absence-tolerant reader derive_reads uses: a missing, null,
    # non-string or whitespace-only field yields None instead of failing, so
    # the pre-pass derives nothing and execute()'s own required_string raises
    # the real InvalidArgument (clinic-domain / objects-base precedent).
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    return v.strip()

def required_bare_handle(p, name):
    # The bare instance handle Loom minted: type-free, must carry no key
    # delimiters so "vtx.service." + handle is a single well-formed vertex key.
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def vertex_class(state, key):
    # The vertex's ENVELOPE class (service.<family>.instance) -- the P7
    # type/subtype discriminator, no .class shadow aspect. None if
    # absent/dead. Mirrors service-domain's own vertex_class exactly (same
    # vtx.service.<handle> claim-vertex shape).
    if not vertex_alive(state, key):
        return None
    doc = state[key]
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def make_tombstone(key):
    return {"op": "tombstone", "key": key}

SERVICE_FAMILIES = ["backgroundCheck", "payment"]

# Page limit for the successor-ownership instanceOf walk in
# TombstoneSupersededLeaseServiceInstance. A service instance carries exactly ONE
# instanceOf link (CreateLeaseServiceInstance mints it; nothing else adds one), so
# the walk is degree-1 by construction and one small page is the whole set --
# sized above 1 only so a stray extra link on some future minter's instance could
# not hide this DDL's own link behind a page boundary. A second page refuses
# loudly rather than reading as NotOwned (the enumeration below).
INSTANCE_OF_PAGE_LIMIT = 8

def family_of(p):
    # The family is opaque pass-through from the Loom step's params.family.
    # A nested payload object is exposed to Starlark as a dict (not a struct),
    # so it is read by key, not by attribute.
    if not hasattr(p, "params") or p.params == None:
        fail("InvalidArgument: params.family: required (backgroundCheck|payment)")
    params = p.params
    if type(params) != type({}) or "family" not in params:
        fail("InvalidArgument: params.family: required (backgroundCheck|payment)")
    fam = params["family"]
    if fam == None or type(fam) != type("") or len(fam.strip()) == 0:
        fail("InvalidArgument: params.family: required non-empty string")
    fam = fam.strip()
    if fam not in SERVICE_FAMILIES:
        fail("InvalidArgument: params.family: must be one of backgroundCheck, payment; got " + fam)
    return fam

def derive_reads(op):
    # Contract #2 §2.5 class (g) for TombstoneSupersededLeaseServiceInstance.
    # Every key below is a pure function of the {instanceKey, supersededBy,
    # subjectKey} payload -- the same parts_of derivation execute() performs --
    # so no dispatcher restates it: Weaver's supersededBackgroundChecks target
    # names one read, an operator names one key. The SEVENTH read (the
    # instanceOf ownership link) needs ddl[...].metaKey, which this pre-pass
    # cannot reach (state / ddl / kv / nanoid all fail on access here), so it
    # stays the dispatcher's declaration.
    #
    # A malformed or missing field derives nothing and lets execute() raise the
    # real InvalidArgument: the pre-pass classifies keys, it never validates
    # payloads. So it derives only from a payload whose SHAPE and TYPE are
    # already right -- 3 segments, "vtx", the type segment each key must carry.
    # A grammatically wrong key would otherwise either be refused by the
    # Processor as DeriveReadsInvalid (a hydration-class code, not the
    # InvalidArgument this op promises) or hydrate a key outside
    # vtx.service.* / vtx.identity.* before a guard rejects it. What the
    # pre-pass does NOT check is the id segment: an id that is not a valid
    # NanoID still derives, and execute()'s parts_of raises the rejection.
    if op.operationType != "TombstoneSupersededLeaseServiceInstance":
        return {}
    p = op.payload
    inst = optional_string(p, "instanceKey")
    succ = optional_string(p, "supersededBy")
    subj = optional_string(p, "subjectKey")
    if inst == None or succ == None or subj == None:
        return {}
    if inst == "" or succ == "" or subj == "":
        return {}
    ip = inst.split(".")
    sp = succ.split(".")
    jp = subj.split(".")
    if len(ip) != 3 or len(sp) != 3 or len(jp) != 3:
        return {}
    if ip[0] != "vtx" or ip[1] != "service":
        return {}
    if sp[0] != "vtx" or sp[1] != "service":
        return {}
    if jp[0] != "vtx" or jp[1] != "identity":
        return {}
    if ip[2] == "" or sp[2] == "" or jp[2] == "":
        return {}
    return {"reads": [inst, succ, inst + ".outcome", succ + ".outcome",
                      "lnk.service." + ip[2] + ".providedTo.identity." + jp[2],
                      "lnk.service." + sp[2] + ".providedTo.identity." + jp[2]]}

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateLeaseServiceInstance":
        # actor-guard: (primordial) restricted to Loom's relay actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind this
        # op is operator/Scope:"any", which admits every operator-role holder —
        # far wider than the one engine that dispatches the pattern. subjectKey
        # is payload-named and its identity's params are resolved and forwarded
        # to an external adapter below, so a wider submitter set is an arbitrary
        # subject's data reaching a vendor. First statement in the branch: it
        # also denies the payload-shape and vertex-alive oracles beneath it.
        if op.actor != primordialActor["loom"]:
            fail("AuthDenied: CreateLeaseServiceInstance is restricted to Loom's relay actor; got " + op.actor)

        handle = required_bare_handle(p, "instanceKey")
        subject_key = required_string(p, "subjectKey")
        adapter = required_string(p, "adapter")
        reply_op = required_string(p, "replyOp")
        fam = family_of(p)
        _, subject_id = parts_of(subject_key, "subjectKey", "identity")

        # No-orphan invariant (FR29 / P4): the applicant identity MUST be alive.
        if not vertex_alive(state, subject_key):
            fail("UnknownApplicant: " + subject_key)

        # Prepend the package-chosen claim-vertex type. The engine never names a
        # type; the replyOp re-prepends the SAME type — a matched pair.
        inst_key = "vtx.service." + handle

        # The type/subtype discriminator lives on the vertex ENVELOPE class (P7) —
        # service.<family>.instance — NOT a .class/.family shadow aspect. That
        # fine-grained class misses the exact class→DDL lookup, so the step-6
        # write-gate resolver walks this instance's instanceOf link to its type
        # authority (Contract #1 §1.5 instanceOf terminal): the leaseServiceInstance
        # DDL's meta-vertex, surfaced to the script as ddl[...].metaKey. The lens
        # discriminates bgcheck/payment by reading inst.class directly (no .family).
        inst_class = "service." + fam + ".instance"
        meta_key = ddl["leaseServiceInstance"].metaKey
        _, meta_id = parts_of(meta_key, "typeAuthority", "meta")
        instance_of_lnk = "lnk.service." + handle + ".instanceOf.meta." + meta_id

        # providedTo: the service instance (later-arriving) is the source, the
        # pre-existing identity is the target (Contract #1 §1.1). This is the
        # convergence link the lens fans out across to read the outcome aspect.
        provided_to_lnk = "lnk.service." + handle + ".providedTo.identity." + subject_id

        # Root data minimal (D5): {} on root. The vertex KEY type is 'service'
        # (vtx.service.<handle>) so the lens anchors via the key segment; the
        # envelope CLASS carries the fine-grained discriminator. NO outcome aspect
        # yet — absence = not-yet-complete. The instanceOf link is the source of the
        # write-gate authority; providedTo is the convergence link.
        mutations = [
            make_vtx(inst_key, inst_class, {}),
            make_link(instance_of_lnk, inst_key, meta_key, "instanceOf", "instanceOf", {}),
            make_link(provided_to_lnk, inst_key, subject_key, "providedTo", "providedTo", {}),
        ]

        # Emit the external.<adapter> event off this op's transactional outbox.
        # The body shape matches the bridge's externalEvent reader: the bare
        # handle is the opaque correlation token (instanceKey == externalRef ==
        # idempotencyKey by construction). dispatchOp is the package-local op the
        # bridge posts if its adapter returns Pending (it records the .dispatch
        # marker); it is the matched pair of replyOp, which the bridge posts on a
        # terminal outcome.
        raw_params = p.params if hasattr(p, "params") and p.params != None else {}
        resolved_params = resolve_subject_params(raw_params, subject_key)
        # family_of already validated + trimmed fam; re-pin it post-resolve so a
        # caller's untrimmed family value can never diverge from the validated
        # one (resolve_subject_params passes non-"subject."-prefixed values
        # through byte-identical, which for family is normally a no-op, but
        # trim discipline should not depend on that being true forever).
        resolved_params["family"] = fam
        event_data = {
            "instanceKey":    handle,
            "adapter":        adapter,
            "replyOp":        reply_op,
            "dispatchOp":     "RecordServiceDispatch",
            "externalRef":    handle,
            "idempotencyKey": handle,
            "params":         resolved_params,
        }
        events = [{"class": "external." + adapter, "data": event_data}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    if ot == "TombstoneSupersededLeaseServiceInstance":
        # The retirement half of the supersession rule (bgcheck-supersession-
        # convergence-rule-design.md): retires a lease service instance
        # superseded by a LATER COMPLETED one on the same subject + family, so a
        # readiness aggregate that fans out over every instance of an applicant
        # (the leaseApplicationComplete lens) stops reading retired checks. Six of
        # the seven reads below are DERIVED server-side by this script's own
        # derive_reads(op) (Contract #2 §2.5 class (g)); the predecessor's
        # ownership link is the one DECLARED key a dispatcher supplies. The
        # successor's ownership is the eighth proof, and the one thing neither a
        # derivation nor a lens column can carry: a bounded class-(e) enumeration
        # of the successor's own instanceOf relation, declared by every dispatcher
        # as an enumeration rather than a read.

        # actor-guard: the grant behind this op is operator/Scope:"any", which
        # admits every operator-role holder -- including the two platform
        # engines, which hold that role for their own ops
        # (CreateLeaseServiceInstance's own actor-guard comment above states
        # the same structural fact). Weaver IS the durable submitter here: its
        # supersededBackgroundChecks convergence target dispatches this op as a
        # directOp off a lens row that has already proven the pair, and an
        # operator or trusted tool is admitted for a hand repair. Loom is
        # refused: it MINTS instances (CreateLeaseServiceInstance is restricted
        # to exactly it) and never retires them, so Loom's actor landing here is
        # a confused or compromised caller, not a legitimate submitter.
        # Weaver's admission widens who may ASK, never what can be proven: every
        # trust-bearing key is derived from the payload + ddl[...].metaKey and
        # proven against Processor-hydrated state, never taken from the caller
        # (bgcheck-supersession-convergence-rule-design.md §6).
        # primordialActor's only two keys (cmd/processor/main.go's
        # PrimordialActors wiring) are loom and weaver.
        if op.actor == primordialActor["loom"]:
            fail("AuthDenied: TombstoneSupersededLeaseServiceInstance is submitted by Weaver's convergence target or an operator; Loom mints instances and never retires them; got " + op.actor)

        instance_key = required_string(p, "instanceKey")
        superseded_by = required_string(p, "supersededBy")
        subject_key = required_string(p, "subjectKey")

        if instance_key == superseded_by:
            fail("InvalidArgument: instanceKey and supersededBy must name different instances; got " + instance_key)

        _, inst_handle = parts_of(instance_key, "instanceKey", "service")
        _, succ_handle = parts_of(superseded_by, "supersededBy", "service")
        _, subject_id = parts_of(subject_key, "subjectKey", "identity")

        if not vertex_alive(state, instance_key):
            fail("UnknownInstance: " + instance_key)

        # OWNERSHIP (Contract #1 §1.5 type authority): instance_key's
        # instanceOf link must resolve to THIS DDL's own meta-vertex, derived
        # the EXACT same way CreateLeaseServiceInstance derives it -- nothing
        # else about instance_key is trusted before this passes. A root's
        # readable SHAPE (envelope class, a completed .outcome, a providedTo
        # link) is NOT proof of origin: another package's own instance-minting
        # mechanism can produce the identical shape (e.g. service-domain's own
        # generic service instance) while its REAL instanceOf link targets a
        # DIFFERENT type authority (lnk.service.<handle>.instanceOf.service.<
        # templateId>, not .meta.<ourId>) -- a key this derivation never
        # produces, so it is simply absent for a foreign instance, never read
        # as some other document. A declared (required) read: for a genuinely
        # foreign/never-owned instance the key is required-absent at the step-4
        # snapshot, and the first touch below faults the deferred HydrationMiss
        # (Contract #2 §2.5) instead of branching -- fail-closed, and the script
        # never sees a document for it. Reaching the check itself with the key
        # present-but-tombstoned is the residual case the script refuses.
        meta_key = ddl["leaseServiceInstance"].metaKey
        _, meta_id = parts_of(meta_key, "typeAuthority", "meta")
        instance_of_lnk = "lnk.service." + inst_handle + ".instanceOf.meta." + meta_id
        # The ownership link is read from the STEP-4 SNAPSHOT, not live: it is
        # the one key derive_reads(op) cannot compute (ddl[...].metaKey is
        # unreachable in the pre-pass), so it is the one key a dispatcher
        # declares in contextHint.reads -- Weaver as row.instanceOfLink off the
        # supersededBackgroundChecks lens, an operator as the key this DDL's
        # Description spells out. A submission that declares nothing still has
        # its six derived reads, so a lazy kv.Read here would silently serve an
        # UNDECLARED live read of the trust-bearing key (class-(b) debt no
        # annotation makes visible); refusing an undeclared ownership link keeps
        # the proof inside the OCC snapshot the mutations are conditioned on.
        # A DECLARED-but-absent ownership link is required-absent, so naming it
        # here faults the deferred HydrationMiss -- the foreign-instance vector.
        if instance_of_lnk not in state:
            fail("InvalidArgument: contextHint.reads must declare the ownership link " + instance_of_lnk + " (the one key derive_reads cannot compute)")
        inst_ownership = state[instance_of_lnk]
        if inst_ownership == None or inst_ownership.isDeleted:
            fail("NotOwned: " + instance_key + " is not a lease-signing service instance (no live instanceOf link to this DDL's type authority)")

        if not vertex_alive(state, superseded_by):
            fail("UnknownInstance: " + superseded_by)

        # OWNERSHIP of the SUCCESSOR -- the same Contract #1 §1.5 type-authority
        # proof instance_key gets above, and for the same reason: a foreign
        # instance carries the identical readable shape (envelope class, a
        # completed .outcome, a providedTo link) while its real instanceOf link
        # targets another type authority. It is proven on EVERY path here, the
        # op's own eighth proof, because neither of the two ways a declared read
        # reaches this script can carry it: derive_reads cannot compute the key
        # (its target is ddl[...].metaKey, unreachable in the pre-pass) and the
        # lens cannot project it (a relationship variable inside max() is refused
        # at parse). A bounded enumeration of the successor's OWN outbound
        # instanceOf relation resolves it instead: degree 1 by construction --
        # CreateLeaseServiceInstance mints exactly one instanceOf link per
        # instance and nothing else adds one -- so this is one page of one link,
        # far inside the op's wall budget. Every dispatcher declares the walk:
        # Weaver's supersededBackgroundChecks target as an Enumerations entry on
        # the gap (hub row.supersededBy, targets.go), an operator as
        # contextHint.enumerations.
        # read-posture: (e) relation=instanceOf epoch=none -- this op is the only mutator of instanceOf on a service vertex; a concurrent retirement of the successor between the walk and the commit leaves the supersedes link sourced at a tombstoned vertex, the accepted chain-case state; Weaver's detect+recover is the backstop.
        succ_instance_of_page, succ_instance_of_more = kv.Links(superseded_by, "instanceOf", "out", None, INSTANCE_OF_PAGE_LIMIT)
        succ_owned = False
        for lk in succ_instance_of_page:
            if not lk.isDeleted and lk.targetVertex == meta_key:
                succ_owned = True
        if not succ_owned:
            # One page is the whole set for a vertex minted with one instanceOf
            # link, so a second page means the degree-1 invariant this walk rests
            # on is broken. Say that instead of reporting the walk's silence as
            # "not owned": a link that exists past the page boundary is a
            # different fault from a link that does not exist, and only one of
            # them is the caller's.
            if succ_instance_of_more != None:
                fail("NotOwned: " + superseded_by + " carries more than " + str(INSTANCE_OF_PAGE_LIMIT) + " instanceOf links; a service instance is minted with exactly one")
            fail("NotOwned: " + superseded_by + " is not a lease-signing service instance (no live instanceOf link to this DDL's type authority)")

        # Same envelope class (P7): a successor supersedes only its OWN
        # family -- a completed payment can never supersede a background
        # check, even for the same subject. Read the class the way the lens
        # + service-domain's own vertex_class helper both do (state[key]'s
        # "class" attribute, not a shadow aspect). vertex_class returns ""
        # (never None) for a live root with no class -- vertexDocToStarlark
        # always sets the "class" attribute, empty string when the stored
        # envelope carries none -- so an explicit empty-string check is the
        # live guard; two classless roots must never compare as "the same
        # class".
        inst_class = vertex_class(state, instance_key)
        succ_class = vertex_class(state, superseded_by)
        if inst_class == "" or succ_class == "":
            fail("WrongClass: instanceKey " + instance_key + " (class " + str(inst_class) + ") and supersededBy " + superseded_by + " (class " + str(succ_class) + ") must both carry a non-empty envelope class")
        if inst_class != succ_class:
            fail("WrongClass: instanceKey " + instance_key + " (class " + str(inst_class) + ") and supersededBy " + superseded_by + " (class " + str(succ_class) + ") must carry the SAME envelope class")

        # read-posture: (a) reads — derived server-side by this script's own
        # derive_reads(op) (Contract #2 §2.5 class (g)).
        inst_outcome = kv.Read(instance_key + ".outcome")
        if inst_outcome == None or inst_outcome.isDeleted or inst_outcome.data.get("status") != "completed":
            fail("NotSuperseded: " + instance_key + " carries no completed outcome")

        # read-posture: (a) reads — derived server-side by this script's own
        # derive_reads(op) (Contract #2 §2.5 class (g)).
        succ_outcome = kv.Read(superseded_by + ".outcome")
        if succ_outcome == None or succ_outcome.isDeleted or succ_outcome.data.get("status") != "completed":
            fail("NotSuperseded: " + superseded_by + " carries no completed outcome")

        inst_completed_at = inst_outcome.data.get("completedAt")
        succ_completed_at = succ_outcome.data.get("completedAt")
        # completedAt is the RFC3339 UTC stamp RecordLeaseServiceOutcome writes
        # (time.rfc3339_utc(op.submittedAt) -- fixed-width, zero-padded,
        # Z-suffixed), so comparing the two AS PLAIN STRINGS orders them
        # identically to chronological order -- the same fact clinic-domain's
        # own starts_at/ends_at RFC3339 comparisons rely on (ddls.go) and the
        # lens's validUntil/completedAt CASE comparisons rely on (lenses.go).
        #
        # The order is LATER, or EQUAL with the greater key: rfc3339_utc formats
        # whole seconds, so two replies committing in the same second stamp an
        # equal completedAt, and a rule that demanded strict lateness would leave
        # that pair live forever with nothing to name either one the survivor.
        # Comparing the full keys breaks the tie totally and deterministically.
        # This predicate is TEXTUALLY the same rule the supersededBackgroundChecks
        # lens projects a row by ((newer.completedAt > inst.completedAt) OR
        # (equal AND newer.key > inst.key)): a row the lens emits must be a pair
        # this guard accepts, or Weaver's convergence dispatch rejects forever and
        # exhausts its budget. The pinned lens rows are the drift detector for an
        # edit that changes one side and not the other.
        if inst_completed_at == None or succ_completed_at == None:
            fail("NotSuperseded: supersededBy " + superseded_by + " completedAt (" + str(succ_completed_at) + ") and " + instance_key + "'s (" + str(inst_completed_at) + ") must both be present")
        # A form precondition, not part of the ordering predicate the lens
        # mirrors: string order equals time order only for the fixed-width
        # whole-second UTC form rfc3339_utc writes (20 chars, Z-suffixed).
        # RecordLeaseServiceOutcome is the only writer of this aspect, so this
        # never fires today; it fails closed against a later writer (a backfill,
        # an import) stamping a fractional or offset-bearing value, which would
        # otherwise mis-order silently. The TYPE test comes first, and is part of
        # the same refusal: a stamp that is not a string at all (a number, a dict)
        # has no len() and no endswith(), so testing the form first would abort the
        # script with a bare Starlark error instead of this op's own
        # NotSuperseded.
        for stamp in [inst_completed_at, succ_completed_at]:
            if type(stamp) != type("") or len(stamp) != 20 or not stamp.endswith("Z"):
                fail("NotSuperseded: " + instance_key + " / " + superseded_by + " completedAt must be a whole-second RFC3339 UTC stamp (20 characters, Z-suffixed); got " + str(inst_completed_at) + " and " + str(succ_completed_at))
        later = succ_completed_at > inst_completed_at
        tie_break = succ_completed_at == inst_completed_at and superseded_by > instance_key
        if not (later or tie_break):
            fail("NotSuperseded: supersededBy " + superseded_by + " completedAt (" + str(succ_completed_at) + ") must be later than " + instance_key + "'s (" + str(inst_completed_at) + "), or equal with the greater instance key")

        inst_provided_to = "lnk.service." + inst_handle + ".providedTo.identity." + subject_id
        # read-posture: (a) reads — derived server-side by this script's own
        # derive_reads(op) (Contract #2 §2.5 class (g)).
        # A validation link: a WRONG subjectKey's derived key never
        # exists at all under the REAL link's key shape (the target identity
        # id is baked into the key itself, not a separate field to compare),
        # so that submission's key is required-absent at the step-4 snapshot and
        # this read faults the deferred HydrationMiss instead of returning None
        # -- the same fail-closed posture the ownership check above takes. The
        # script's own SubjectMismatch below is the residual case: the key
        # present but tombstoned.
        inst_plink = kv.Read(inst_provided_to)
        if inst_plink == None or inst_plink.isDeleted:
            fail("SubjectMismatch: " + instance_key + " is not providedTo " + subject_key)

        succ_provided_to = "lnk.service." + succ_handle + ".providedTo.identity." + subject_id
        # read-posture: (a) reads — derived server-side by this script's own
        # derive_reads(op) (Contract #2 §2.5 class (g)).
        # A validation link; same required-absent-faults-on-touch posture as
        # instance_key's own providedTo read above.
        succ_plink = kv.Read(succ_provided_to)
        if succ_plink == None or succ_plink.isDeleted:
            fail("SubjectMismatch: " + superseded_by + " is not providedTo " + subject_key)

        # Tombstone the root + its two links via the bare op:tombstone form
        # (no document): the write gate reads the class STORED at each key, so
        # the root tombstone is governed by service.<family>.instance and its
        # type authority must name this op in permittedCommands
        # (leaseServiceInstance's DDL does), while the two link tombstones carry
        # relation classes no linkType DDL registers and stay permissive
        # (Contract #1 §1.5/§1.6). The storage layer carries the prior
        # class/sourceVertex/targetVertex over unchanged and flips only
        # isDeleted. Adjacency then returns no edge for either link, so the
        # readiness aggregate stops reading this instance at all (a tombstoned
        # root alone is still read before being filtered,
        # ruleengine/full/executor.go).
        #
        # The fourth mutation makes the retirement walkable at rest: the live
        # successor SOURCES a supersedes link to the predecessor being tombstoned
        # in this same batch (Contract #1 §1.1 -- the later-arriving vertex is the
        # source, and the sentence reads "new supersedes old"). Both endpoints are
        # already hydrated and validated by the guards above, so the link costs no
        # read. step6_validate.go applies no same-batch endpoint-liveness rule to
        # a link create; its one endpoint rule, firstRequiredAbsentMutation,
        # covers required-ABSENT endpoints, and both endpoints here are hydrated.
        # The write gate resolves a mutation's governing DDL by exact class first,
        # and a link create's class is its relation: no linkType DDL registers
        # the relation "supersedes", and the fallback walk needs a vertex root a link mutation
        # does not have, so it takes the permissive default (step6_resolve_ddl.go)
        # and no DDL's permittedCommands names this op for the link.
        # Live lenses cannot chain through the link -- every walk decodes a
        # tombstoned neighbour as absent -- which is exactly the "history at rest"
        # posture: Loupe's inspector, an audit, or a retention pass follows it by
        # key. A predecessor retired in its turn keeps its own outbound supersedes
        # link live, so a chain of retirements stays walkable link by link.
        supersedes_lnk = "lnk.service." + succ_handle + ".supersedes.service." + inst_handle
        mutations = [
            make_tombstone(instance_key),
            make_tombstone(instance_of_lnk),
            make_tombstone(inst_provided_to),
            make_link(supersedes_lnk, superseded_by, instance_key, "supersedes", "supersedes", {}),
        ]
        events = [{"class": "lease.serviceInstanceSuperseded",
                   "data": {"instanceKey": instance_key, "supersededBy": superseded_by, "subjectKey": subject_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": instance_key}}

    fail("leaseServiceInstance DDL: unknown operationType: " + ot)
`

// leaseServiceReplyDDLScript is the externalTask replyOp the bridge submits.
// The bridge posts {externalRef, status, result}; this op reconstructs the claim
// vertex key, takes the adapter's terminal status (completed | failed), derives
// completedAt + validUntil, writes the .outcome aspect, and emits
// orchestration.externalTaskCompleted{externalRef} — the completion signal Loom
// correlates on. Without that event the externalTask never completes.
//
// The bridge submits this op with no ContextHint.Reads (internal/bridge's
// actuator builds an envelope with no Reads field), so the op reads NOTHING
// from state: the reconstructed vtx.service.<handle> vertex, its .class aspect,
// and its root revision are all unhydrated on the live path. The once-only
// guarantee is therefore the create-only .outcome write itself — a redelivered
// reply conflicts on the existing .outcome key and the batch is rejected (the
// bridge's deterministic deriveReplyRequestID already collapses most
// redeliveries at the Contract #4 tracker). The instance root, already minted
// {data:{}} by the instanceOp, is left untouched (D5).
//
// validUntil is pure arithmetic on the op's own completedAt
// (time.rfc3339_add), so the op stays read-free.
var leaseServiceReplyDDLScript = fmt.Sprintf(`
def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    return v

def required_bare_handle(p, name):
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

# The terminal outcome values RecordLeaseServiceOutcome admits (mirrors
# service-domain). completed = the external call succeeded with a satisfying
# result; failed = a definitive business rejection (a declined charge, a failed
# background check). The bridge supplies it verbatim from the adapter's
# Result.Status — it is required, with no default.
OUTCOME_STATUSES = ["completed", "failed"]

def required_status(p):
    st = required_string(p, "status")
    if st not in OUTCOME_STATUSES:
        fail("InvalidArgument: status: must be one of completed, failed; got " + st)
    return st

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordLeaseServiceOutcome":
        handle = required_bare_handle(p, "externalRef")
        # Reconstruct the claim-vertex key from the bare handle: the instanceOp
        # chose 'service' as the type, so the replyOp re-prepends the same type.
        # The bare-handle format validation needs no state read.
        inst_key = "vtx.service." + handle

        # The bridge supplies only a free-form result string. It is NOT written
        # to the projection-plane .outcome aspect (it can carry PII / payment
        # data in production and the lens reads only status / completedAt /
        # validUntil); it rides the service.outcomeRecorded provenance event body
        # instead.
        result = optional_string(p, "result")

        # The terminal status is the adapter's verdict, supplied verbatim by the
        # bridge (completed | failed) and required — an adapter error is
        # Nak+retry (never a reply), so every reply carries a definitive business
        # outcome. completedAt is the op's own timestamp (the bridge supplies
        # none), normalized to canonical UTC for a sound lexical compare.
        status = required_status(p)
        completed_at = time.rfc3339_utc(op.submittedAt)

        # Stamp validUntil = completedAt + the freshness window. This op is
        # read-free and cannot tell bgcheck from payment, so it stamps validUntil
        # on EVERY outcome (family-agnostic). The lens applies the freshness
        # policy to bgcheck only — it counts a completed bgcheck toward
        # convergence solely while no recorded lapse reaches validUntil,
        # re-opening the gap once one does; payment ignores validUntil
        # (ever-completed). So validUntil on a
        # payment outcome is harmless and unused: the freshness rule lives in the
        # cypher (Contract #10 §10.2). The add is pure arithmetic on completed_at
        # — no clock read, so the op stays read-free and deterministic.
        valid_until = time.rfc3339_add(completed_at, %q)

        # Write the .outcome aspect {status, completedAt, validUntil} as a
        # create-only mutation. This create-only IS the once-only guarantee: a
        # redelivered reply conflicts on the existing key and the batch is
        # rejected (FR58 at the DDL layer, atop the bridge's deterministic
        # requestId collapse). The instance root, already {data:{}}, is not
        # touched (D5).
        mutations = [
            make_aspect(inst_key, "outcome", "leaseServiceOutcome", {"status": status, "completedAt": completed_at, "validUntil": valid_until}),
        ]

        # Emit the completion signal Loom correlates on (the BARE handle as
        # externalRef — Loom parks on token.<handle>) PLUS a provenance event
        # (which carries the free-form result, kept off the aspect). The
        # completion event is load-bearing: without it the externalTask never
        # completes (the creation-deadline disarmed on instanceOp commit; the
        # bridge reply carried no completion signal).
        provenance = {"serviceKey": inst_key, "status": status, "completedAt": completed_at, "validUntil": valid_until}
        if result != None:
            provenance["result"] = result
        events = [
            {"class": "orchestration.externalTaskCompleted",
             "data": {"externalRef": handle}},
            {"class": "service.outcomeRecorded",
             "data": provenance},
        ]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    fail("leaseServiceReply DDL: unknown operationType: " + ot)
`, bgcheckFreshnessWindow)

// leaseServiceDispatchDDLScript is the externalTask dispatchOp the bridge submits
// when its adapter returns Pending (the external call was submitted but has not
// resolved yet). The bridge posts {externalRef, vendorRef, adapter, replyOp,
// nextPollAt, deadline}; this op reconstructs the claim vertex key from the bare
// handle and writes a create-only .dispatch aspect
// {vendorRef, adapter, replyOp, submittedAt, nextPollAt, deadline} on it — the
// pending marker. The bridge's poll/timeout schedules carry the routing (adapter /
// replyOp / vendorRef) on their payload, so the fired handler reads it from there —
// NOT from this marker; the marker records the same routing for the lens / Weaver
// read-model (pending-suppression, a later increment). It does NOT write the create-only .outcome
// aspect and does NOT emit orchestration.externalTaskCompleted: the externalTask
// is NOT done, so Loom's token stays parked. The .dispatch and .outcome aspects
// are deliberately separate (.outcome is the FR58 once-only terminal guard;
// "pending" is a distinct state the lens/Weaver can read without colliding with
// it).
//
// Like the replyOp the bridge submits this with no ContextHint.Reads, so the op
// reads NOTHING from state: the reconstructed vtx.service.<handle> vertex is
// unhydrated on the live path. The once-only guarantee is the create-only
// .dispatch write itself — a redelivered Pending conflicts on the existing
// .dispatch key and the batch is rejected (atop the bridge's deterministic
// deriveDispatchRequestID, which already collapses most redeliveries at the
// Contract #4 tracker). submittedAt is the op's own timestamp, normalized to
// canonical UTC; nextPollAt and deadline are the bridge-supplied schedule
// instants, normalized to canonical UTC for a sound lexical compare (no clock
// read — read-free, deterministic).
const leaseServiceDispatchDDLScript = `
def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def required_instant(p, name):
    # An RFC3339 instant the bridge computed (nextPollAt / deadline), normalized
    # to canonical UTC so the marker compares lexically with the schedule headers.
    v = required_string(p, name)
    return time.rfc3339_utc(v)

def required_bare_handle(p, name):
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordServiceDispatch":
        handle = required_bare_handle(p, "externalRef")
        # Reconstruct the claim-vertex key from the bare handle (the matched-pair
        # type the instanceOp chose). The bare-handle validation needs no state read.
        inst_key = "vtx.service." + handle

        # The vendor's opaque pending reference (the poll/webhook key the bridge
        # got back from the adapter). Required — a Pending with no ref is meaningless.
        vendor_ref = required_string(p, "vendorRef")

        # The routing recorded for the lens / Weaver read-model: which adapter to
        # Poll on a poll firing, and which replyOp to post when the poll resolves or
        # the call times out. The fired handler reads these from the schedule
        # payload, not the marker; both are required here for the read-model record.
        adapter = required_string(p, "adapter")
        reply_op = required_string(p, "replyOp")

        # The bridge-supplied schedule instants: when the next poll is due and when
        # the call gives up. Recorded for the lens / Weaver read-model; the timeout
        # itself fires from the armed schedule, not this marker.
        next_poll_at = required_instant(p, "nextPollAt")
        deadline = required_instant(p, "deadline")

        # submittedAt is the op's own timestamp, normalized to canonical UTC. The
        # bridge supplies no timestamp; this is the dispatch instant.
        submitted_at = time.rfc3339_utc(op.submittedAt)

        # Write the .dispatch aspect {vendorRef, adapter, replyOp, submittedAt,
        # nextPollAt, deadline} as a create-only mutation. This create-only IS the
        # once-only guarantee: a redelivered Pending conflicts on the existing key
        # and the batch is rejected (atop the bridge's deterministic dispatch
        # requestId collapse). NO .outcome is written and NO
        # orchestration.externalTaskCompleted is emitted — the task is not done,
        # the token stays parked. The instance root, already {}, is untouched (D5).
        mutations = [
            make_aspect(inst_key, "dispatch", "leaseServiceDispatchMarker",
                        {"vendorRef": vendor_ref, "adapter": adapter, "replyOp": reply_op,
                         "submittedAt": submitted_at, "nextPollAt": next_poll_at, "deadline": deadline}),
        ]

        # A provenance event marks the submit for the audit join (NOT a completion
        # signal — Loom must NOT close the token on a dispatch).
        events = [
            {"class": "service.dispatchRecorded",
             "data": {"serviceKey": inst_key, "vendorRef": vendor_ref, "submittedAt": submitted_at}},
        ]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    fail("leaseServiceDispatch DDL: unknown operationType: " + ot)
`
