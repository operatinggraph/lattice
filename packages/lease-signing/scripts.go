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
// constant, never a runtime mutation. The script ALSO contains Starlark's own
// literal '%' formatting verbs (add_months' "%04d-%02d-%02d" date format and
// the "%" modulo operator), so this substitutes the one renewalWindow site via
// a plain strings.Replace token rather than fmt.Sprintf — a whole-script
// Sprintf would misinterpret every one of those unrelated '%' as its own verb.
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

DAYS_IN_MONTH = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]

def is_leap_year(year):
    return (year % 4 == 0 and year % 100 != 0) or (year % 400 == 0)

def days_in_month(year, month):
    if month == 2 and is_leap_year(year):
        return 29
    return DAYS_IN_MONTH[month - 1]

def zero_pad(n, width):
    # Starlark's %-format supports no field-width flag (unlike Python/Go), so a
    # fixed-width zero-padded integer (YYYY-MM-DD's month/day, always < 100,
    # year always < 10000) is built by hand: left-pad the decimal string with
    # "0" to the target width. This Starlark dialect has no while loop, so the
    # pad is a bounded for-loop over the width itself (width is always a small
    # literal — 2 or 4 — never large enough for the bound to matter).
    s = str(n)
    for _ in range(width):
        if len(s) >= width:
            break
        s = "0" + s
    return s

def add_months(rfc3339_instant, months):
    # Calendar-month addition on an RFC3339 instant (semantic-contracts' "date math
    # belongs to the op, cypher only compares" precedent): the deterministic
    # Starlark sandbox has no calendar-aware builtin (time.rfc3339_add's Go
    # duration form is hours-only — no months unit), and a lease term is a
    # calendar-month count (12 months from Jan 31 is Jan 31 of next year, not a
    # fixed hour count that would drift across leap years / month lengths), so
    # this hand-rolls the same year/month/day carry identity-domain's DOB
    # validator already parses (leap-year table above). The clock-time and zone
    # suffix are preserved verbatim (a lease term shifts the calendar date, never
    # the time of day); the day-of-month CLAMPS to the target month's length
    # (Jan 31 + 1 month = Feb 28/29, never a rollover into March) — the
    # conventional calendar-add rule, applied once (months is always a small
    # positive integer here, never large enough to need iterated clamping).
    utc = time.rfc3339_utc(rfc3339_instant)
    year = int(utc[0:4])
    month = int(utc[5:7])
    day = int(utc[8:10])
    rest = utc[10:]  # "Thh:mm:ssZ"

    total = (month - 1) + int(months)
    year = year + total // 12
    month = (total % 12) + 1

    max_day = days_in_month(year, month)
    if day > max_day:
        day = max_day

    return zero_pad(year, 4) + "-" + zero_pad(month, 2) + "-" + zero_pad(day, 2) + rest

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
            terms_data = {"moveInDate": move_in, "leaseTermMonths": require_number(p, "leaseTermMonths")}
            req_rent = optional_number(p, "requestedRent")
            if req_rent == None:
                # No rent offer from the applicant — fall back to the unit's own
                # listed rent, so leaseRentSettlementSpec (semantic-contracts) has
                # a requestedRent to gate missing_account on. Same key + idiom
                # SetApplicantProfile already reads for its income-to-rent check.
                # read-posture: (d) declared optionalReads at CreateLeaseApplication
                # dispatch — a unit with no listing yet has no rent to fall back to.
                listing = kv.Read(unit + ".listing")
                if listing != None and not listing.isDeleted:
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

        # The signature is a fact in an aspect (D5); the application root stays
        # {}. signedAt is the op's own timestamp, normalized to canonical UTC so
        # a downstream lexical compare is sound.
        signed_at = time.rfc3339_utc(op.submittedAt)
        mutations = [
            make_aspect(app_key, "signature", "signature", {"signedAt": signed_at}),
        ]
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
        # where it is absent by design).
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
                # read-posture: (e) per-candidate follow-up read off the
                # appliesToUnit enumeration leaseapp_unit() already walked
                # above — decide_unit is the resolved, live unit key, not a
                # payload placeholder that would build a malformed key when
                # absent.
                listing = kv.Read(decide_unit + ".listing")
                if listing == None or listing.isDeleted:
                    fail("NoListing: unit " + decide_unit + " has no .listing aspect; cannot compute a tenancy term")
                available_from = listing.data.get("availableFrom")
                term_months = listing.data.get("leaseTermMonths")
                if available_from == None or term_months == None:
                    fail("NoListing: unit " + decide_unit + "'s .listing is missing availableFrom/leaseTermMonths")
                lease_start = time.rfc3339_utc(available_from)
                lease_end = add_months(lease_start, term_months)
                renewal_opens_at = time.rfc3339_add(lease_end, "-__RENEWAL_WINDOW__")
                mutations.append(make_aspect(app_key, "tenancy", "tenancy",
                    {"leaseStart": lease_start, "leaseEnd": lease_end, "renewalOpensAt": renewal_opens_at}))

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
        # isDeleted → EmptyBehavior delete), and FREE the per-(applicant, unit)
        # guard link so it stops blocking a re-apply. The complement to
        # CreateLeaseApplication's guard — an applicant who applied to the wrong
        # unit can back out + re-apply (the guard revives on re-apply).
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

        # Tombstone the application. The applicationFor / appliesToUnit links are
        # left in place (non-cascading tombstone, the clinic-domain precedent) — they
        # dangle off a tombstoned anchor every reader filters.
        mutations = [make_vtx_tombstone(app_key, "leaseapp")]

        # Free the per-(applicant, unit) guard link: tombstone it so a re-apply
        # revives it. UNCONDITIONED (the withdraw is the authority the application is
        # gone; an alive guard blocks any concurrent re-apply, so no revive races
        # this). absent → nothing to free.
        guard_key = "lnk.identity." + applicant_id + ".appliedToUnit.unit." + unit_id
        # read-posture: (d) declared optionalReads at WithdrawLeaseApplication
        # dispatch — a never-guarded (legacy) application is the absent branch.
        guard = kv.Read(guard_key)
        if guard != None and not guard.isDeleted:
            mutations.append(make_link_tombstone(guard_key, applicant, unit, "appliedToUnit", "appliedToUnit"))

        events = [{"class": "leaseapp.applicationWithdrawn",
                   "data": {"leaseAppKey": app_key, "unit": unit}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

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
        # A one-time historical-data repair, operator-only and manual —
        # mirrors BackfillPatientRegistration's shape exactly (clinic-domain):
        # an application approved before CreateLeaseApplication's
        # unit-listing-rent fallback existed (0.31.14) can carry no .terms
        # aspect at all, so leaseRentSettlementSpec (semantic-contracts) —
        # gated on requestedRent present — never projects a row and the
        # lease never gets a ledger account or rent clause. This gap cannot
        # recur (every CreateLeaseApplication since 0.31.14 already falls
        # back to the unit's listed rent), so a standing auto-remediation
        # loop would be the wrong shape, same reasoning as the clinic
        # precedent's own comment.
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

    fail("leaseapp DDL: unknown operationType: " + ot)
`, "__RENEWAL_WINDOW__", renewalWindow, 1)

// leaseServiceInstanceDDLScript is the externalTask instanceOp. It mints the
// claim vertex vtx.service.<handle> (the same shape 14.1's service instance
// uses, reusing its .outcome aspect shape downstream), records the family
// discriminator + the providedTo link, and emits the external.<adapter> event
// off its own transactional outbox. Template-less (no instanceOf): the lens
// hops providedTo, not instanceOf.
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

SERVICE_FAMILIES = ["backgroundCheck", "payment"]

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
