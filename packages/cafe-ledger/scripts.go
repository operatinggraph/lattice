package cafeledger

// accountDDLScript handles CreateAccount. The account gets its OWN
// independently-minted NanoID — vertex NanoIDs are unique identifiers across
// all of Core KV, never reused across vertex types, even deliberately (see
// adjacency-shared-nanoid-collision-design.md). "One café account per lease"
// is instead enforced by a deterministic CREATE-ONLY guard aspect on the
// PRE-EXISTING leaseapp (leaseAppKey + ".cafeLedgerAccount") — a second
// CreateAccount for the same lease conflicts on that already-existing aspect
// key. The local name is vertical-prefixed (not the bare "ledgerAccount"
// loftspace-ledger already uses on this same leaseapp) so the two ledgers'
// guard aspects never collide on one vertex. Root data stays {} on the
// account (D5): the cafeLedgerHistory lens derives the DISPLAYED balance by
// summing transactions and remains the display source of truth. The account
// also carries a maintained .balance aspect ({balanceCents}) — an O(1)
// authorization cache post_entry (transactionDDLScript) keeps in lockstep with
// every posted entry, so the payment cap never replays a house tab's whole
// history to answer "how much is owed" (see transactionDDLScript's own comment
// for the OCC shape that keeps it race-free under concurrent writers).
const accountDDLScript = `
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

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateAccount":
        lease_key = required_string(p, "leaseAppKey")
        _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")

        # No-orphan invariant: the lease MUST be alive.
        if not vertex_alive(state, lease_key):
            fail("UnknownLeaseApplication: " + lease_key)

        # One café account per lease, guarded by a deterministic aspect on
        # the LEASEAPP (not the account — the account's own id is
        # independent and unknown until minted below). The local name is
        # vertical-prefixed (cafeLedgerAccount) because this same leaseapp
        # already carries loftspace-ledger's own .ledgerAccount guard aspect
        # — a bare local name would collide key-for-key with it. Only
        # meaningful when the caller declared the guard key in
        # contextHint.reads (a repeat/racing caller checking before it
        # retries); the FIRST CreateAccount for a lease declares only
        # leaseAppKey (the guard doesn't exist yet — declaring an
        # as-yet-absent key in reads would HydrationMiss on first touch,
        # deferred past hydration), so on that path
        # the guard aspect's own create-only write is the actual uniqueness
        # enforcement: a genuine race's loser hits a raw substrate conflict
        # here rather than this clean rejection.
        guard_key = lease_key + ".cafeLedgerAccount"
        if vertex_alive(state, guard_key):
            fail("AccountAlreadyExists: " + lease_key)

        acct_id = nanoid.new()
        acct_key = "vtx.cafeaccount." + acct_id

        # heldFor: the account (later-arriving) is the source, the
        # pre-existing leaseapp is the target (Contract #1 §1.1). Reads as
        # "this café account is held for this lease."
        held_for_lnk = "lnk.cafeaccount." + acct_id + ".heldFor.leaseapp." + lease_id

        # Root data minimal (D5): {} on root. The .balance aspect starts at
        # zero and is the only thing post_entry mutates on the account from
        # creation onward — this create is unconditioned (brand-new account,
        # nothing to race). The cafeLedgerHistory lens still derives the
        # displayed balance by summing linked transactions, independently of
        # this cache.
        mutations = [
            make_vtx(acct_key, "cafeaccount", {}),
            make_aspect(lease_key, "cafeLedgerAccount", "cafeLedgerAccountGuard", {"accountKey": acct_key}),
            make_aspect(acct_key, "balance", "cafeAccountBalance", {"balanceCents": 0}),
            make_link(held_for_lnk, acct_key, lease_key, "heldFor", "heldFor", {}),
        ]
        events = [{"class": "account.created",
                   "data": {"accountKey": acct_key, "leaseAppKey": lease_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": acct_key}}

    fail("account DDL: unknown operationType: " + ot)
`

// transactionDDLScript handles DebitAccount, CreditCafeAccount and
// RefundCafeCharge. Each mints a fresh transaction vertex + a .entry aspect +
// the postedTo link to the account; a refund adds a reverses link to the charge
// it gives back, which is the only thing distinguishing it from a payment.
// A posted entry's own money fields (type, amountCents, memo, postedAt) are
// never rewritten, so the cafeLedgerHistory lens derives the displayed balance
// by summing entries independently of anything below, and remains the display
// source of truth.
//
// A posted entry against an account that CARRIES a .balance aspect
// (accountDDLScript mints one at CreateAccount) ALSO moves that aspect by the
// signed amount, via a BARE update — deliberately no expectedRevision of its
// own. This script's own derive_reads(op) declares <accountKey>.balance in
// optionalReads for every op it handles (Contract #2 §2.5 class (g)), so the
// key is hydrated — or recorded known-absent — whatever the submitter declared,
// and commit_path.go's applyHydratedRevisions (Contract #3 §3.2) conditions the
// update on the step-4 hydrated revision FOR us and marks it retry-eligible: on
// a lost race the Processor re-hydrates and retries the whole op before a
// terminal RevisionConflict, so two concurrent entries against one account
// serialize instead of silently dropping an update. (An update supplying its own
// expectedRevision would be treated as an explicit-caller compensating assertion
// instead — excluded from that retry — which is why make_aspect_update takes no
// revision parameter.) That cache is what lets a payment's cap answer "how much
// is owed" in O(1), a single read of .balance, instead of replaying the
// account's whole postedTo history on every submit.
//
// An account minted under cafe-ledger < 0.4.0 carries no .balance at all, and
// that legacy set is CLOSED: CreateAccount mints the aspect, so no account opened
// today joins it.
// Only a PAYMENT pays the one-time bounded replay that computes such an
// account's balance, because a payment is the only leg whose cap needs the
// number. DebitAccount and RefundCafeCharge against a legacy account neither
// replay nor write .balance — the account stays legacy until a payment first
// touches it, and that payment's replay sums the whole history (those later
// debits included), so the cache is never seeded from a partial sum.
//
// The other maintained tally is refundedCents on the REVERSED CHARGE's own
// .entry aspect: the refund ceiling, upserted under a CAS pinned to the
// revision that aspect was hydrated at. Two refunds racing the same charge can
// never jointly overrun it, by one of two routes. On an account carrying
// .balance, the loser's own .balance update is auto-conditioned as well, so the
// conflict re-hydrates and RE-EXECUTES the whole op, and reversed_charge then
// refuses the retry on the fresh refundedCents it re-reads. On a legacy account
// (nothing conditioned but the CAS itself) the loser's commit is simply refused
// on the stale revision. Either way exactly one refund posts, which a ceiling
// recomputed by enumerating prior reversals could never guarantee.
const transactionDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert_occ(vtx_key, local_name, cls, data, expected_revision):
    m = {"op": "update", "key": vtx_key + "." + local_name,
         "document": {"class": cls, "isDeleted": False,
                      "vertexKey": vtx_key, "localName": local_name, "data": data}}
    m["expectedRevision"] = expected_revision
    return m

def make_aspect_update(vtx_key, local_name, cls, data):
    # Deliberately NO expectedRevision here — leaving it unset is what makes
    # this update RETRY-eligible, not less safe. Contract #3 §3.2 in
    # commit_path.go's applyHydratedRevisions auto-conditions any bare update
    # on a key the op declared in reads/optionalReads (so still safe, still
    # OCC-guarded) using the step-4 hydrated revision, and marks it
    # defaulted — the retry-eligible set. An update that supplies its OWN
    # expectedRevision instead is treated as an explicit-caller compensating
    # assertion and is EXCLUDED from that retry ("never overridden") — it
    # hard-conflicts instead of serializing, which is the opposite of what a
    # maintained counter two ops can race on needs. That is the difference
    # from make_aspect_upsert_occ above, which pins a revision precisely
    # because a refund's ceiling may only be advanced from the exact tally it
    # was computed against: reapplying a total derived from a stale read to
    # whatever the charge now holds is the one outcome that ceiling forbids.
    #
    # It is also the reviving verb for a TOMBSTONED aspect. A create against a
    # tombstone is refused (Contract #3 §3.3), so post_entry's .balance write
    # only mints fresh where step 4 saw the key genuinely ABSENT and comes here
    # otherwise — the auto-conditioning above then pins the tombstone's own
    # revision, so the revival races nothing.
    return {"op": "update", "key": vtx_key + "." + local_name,
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

def require_cents(p, name):
    # Money is whole cents. A fractional amount would post an entry the
    # cafeLedgerHistory balance sums into a non-representable total, and every
    # description of this field -- the DDL's own, the op-meta's schema, the
    # aspect's -- already says integer cents. Enforce what they all claim.
    v = require_number(p, name)
    if v != int(v):
        fail("InvalidArgument: " + name + ": required whole cents, got " + str(v))
    return int(v)

def dollars(cents):
    # Cents rendered the way the counter and the statement show money. The
    # refusals below are toasted VERBATIM at a staffer or a resident, and
    # "exceeds 1425" reads as a different number than the $14.25 tab it is
    # talking about. The sign is carried explicitly so a negative total (an
    # account owed money back by a refund) never renders as "$-14.25".
    negative = cents < 0
    whole = cents
    if negative:
        whole = -cents
    minor = whole % 100
    minor_text = str(minor)
    if minor < 10:
        minor_text = "0" + minor_text
    sign = ""
    if negative:
        sign = "-"
    return sign + "$" + str(whole // 100) + "." + minor_text

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
    # The cheap half of require_workplace, callable BEFORE the account's
    # topology is resolved. Starlark evaluates arguments eagerly, so
    # require_workplace(account_unit(x), ...) would walk that topology even for
    # an operator -- wasted reads, and worse, a malformed key anywhere in that
    # walk raises where the op would otherwise succeed. The call site therefore
    # gates on this; require_workplace re-checks it anyway, so forgetting the
    # gate is still CORRECT, only slower.
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants, which
    # authorize via scope=any and so carry no target the platform has checked.
    # CreditCafeAccount's scope=self grant (permissions.go) is what makes the
    # authTargetValidated branch reachable: a resident's self-scoped credit
    # sets it (matchPlatformPermission requires target == actor for scope=self
    # to match at all), exempting them from the workplace walk below -- a
    # resident does not worksAt their own leased unit. That exemption only
    # ever says "the platform already checked the target names the caller,"
    # never "the caller owns this resource" -- post_entry's own
    # authContextTarget branch is what proves the ACCOUNT is theirs.
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

def account_unit(acct_key):
    # An account's location is its lease's unit, two platform-written hops
    # away: heldFor (written by CreateAccount) then appliesToUnit (required at
    # CreateLeaseApplication). Neither hop reads a payload field, so the
    # workplace a credit resolves to cannot be forged by the submitter.
    # read-posture: (e) relation=heldFor epoch=none -- a cafeaccount carries
    # exactly one heldFor link, guarded create-only by the lease's
    # .cafeLedgerAccount aspect, so this is never a keyspace scan.
    page, _ = kv.Links(acct_key, "heldFor", "out")
    lease = None
    for lk in page:
        if not lk.isDeleted:
            lease = lk.targetVertex
    # The lease VERTEX, not just a resolved key -- account_unit transits it to
    # reach the unit, so a dead lease must not carry the walk any further.
    if not vertex_live(lease):
        return None
    # read-posture: (e) relation=appliesToUnit epoch=none -- a leaseapp carries
    # exactly one appliesToUnit link (required at CreateLeaseApplication), so
    # this is never a keyspace scan.
    page, _ = kv.Links(lease, "appliesToUnit", "out")
    unit = None
    for lk in page:
        if not lk.isDeleted:
            unit = lk.targetVertex
    if not vertex_live(unit):
        return None
    return unit

# Legacy-account .balance backfill budget (backfill_balance below, reached
# only by a PAYMENT against an account minted under cafe-ledger < 0.4.0): 10
# pages of 50 postedTo entries covers many years of a house-tab history; an
# account that exceeds it fails closed rather than seed a partial sum. The
# payment that pays this cost writes the aspect, so that account is O(1)
# forever after — this runs at most once per legacy account.
BALANCE_BACKFILL_PAGE_LIMIT = 50
BALANCE_BACKFILL_MAX_PAGES = 10

def backfill_balance(acct_key):
    # The starting balance of an account that carries no .balance aspect,
    # replayed once from its own postedTo history under the budget above.
    #
    # Reached ONLY from the payment leg, and only after the caller's standing
    # to act on this account has already been proven (post_entry runs the
    # confinement walk and the resident-ownership proof first). That ordering
    # is what keeps the replay from being an amplification primitive: a caller
    # who cannot post to the account cannot make it walk the account's history
    # either.
    #
    # Sign convention is the cafeLedgerHistory lens's own: a debit is what is
    # owed, a credit (payment or refund alike) pays it down.
    balance_cents = 0
    cursor = None
    budget_exhausted = True
    for _page in range(BALANCE_BACKFILL_MAX_PAGES):
        # read-posture: (e) relation=postedTo epoch=none -- bounded by the
        # page budget; exhausting it below fails closed.
        page, cursor = kv.Links(acct_key, "postedTo", "in", cursor, BALANCE_BACKFILL_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the
            # enumeration above -- each transaction's own .entry aspect,
            # data-derived and unknowable client-side.
            tx_entry = kv.Read(lk.sourceVertex + ".entry")
            if tx_entry == None or tx_entry.isDeleted:
                continue
            tx_amount = tx_entry.data.get("amountCents")
            if tx_amount == None:
                continue
            if tx_entry.data.get("type") == "debit":
                balance_cents += tx_amount
            elif tx_entry.data.get("type") == "credit":
                balance_cents -= tx_amount
        if cursor == None:
            budget_exhausted = False
            break
    if budget_exhausted:
        # No account key in the text: this is toasted verbatim at whoever tried
        # to pay, and a raw vtx key tells them nothing they can act on.
        fail("AuthDenied: could not backfill this account's balance (too much transaction history for one op)")
    return balance_cents

def reversed_charge(state, p, acct_key, amount_cents):
    # Resolves payload.reversesRef into (the bare id of the charge this refund
    # reverses, the mutation that books the refund against it), refusing every
    # shape that would let a credit masquerade as a correction. All four checks
    # fail closed, and each closes a distinct hole:
    #
    #   1. The ref names a live cafetransaction (declared read — a refund
    #      against a vanished charge has nothing to correct).
    #   2. It is postedTo the SAME account being credited. Without this a
    #      staffer could point a refund on account A at a charge on account B
    #      and drain A's balance against a debit that was never A's. It is
    #      checked FIRST of the two shape tests, because the caller's standing
    #      to touch this transaction at all is what it establishes: every
    #      refusal after it describes a transaction on an account the caller
    #      already named, so none of them tells a confined staffer anything
    #      about a transaction elsewhere in the graph.
    #   3. Its .entry is a DEBIT. Reversing a credit would let one refund
    #      reverse another, compounding a payment into free money.
    #   4. The amount fits within what that debit still has un-refunded — the
    #      charge's own amountCents MINUS refundedCents, the running total this
    #      function itself maintains on the charge. The single-refund case and
    #      the cumulative case are the same arithmetic: two half-refunds of a
    #      $10 charge are fine, a third is not.
    #
    # Both numbers are read from the debit's OWN .entry aspect, never from the
    # payload, so the ceiling cannot be raised by the submitter. The new total
    # is written back to that same aspect under a CAS pinned to the revision it
    # was hydrated at, which is what makes check 4 a real cap rather than an
    # advisory one: two refunds that both read refundedCents=0 cannot both
    # commit, because the second's expectedRevision no longer matches. What the
    # loser then MEETS is not always this refusal — on an account carrying
    # .balance its own update of that aspect is auto-conditioned too, so the
    # conflict re-hydrates and re-executes the whole op, and check 4 turns it
    # away on the fresh tally it re-reads instead. The cap is the same number
    # either way; only which of the two refuses varies. Every other field of the
    # entry is carried across verbatim — the tally is an addition to the charge,
    # not a rewrite of it.
    reverses_key = required_string(p, "reversesRef")
    _, reverses_id = parts_of(reverses_key, "reversesRef", "cafetransaction")
    # An undeclared key and a tombstoned one are different faults and get
    # different words: the platform hydrates exactly what contextHint.reads
    # names, so a key absent from state was never asked for, and reporting that
    # as "unknown transaction" sends the caller looking for a charge that is
    # sitting there fine.
    if reverses_key not in state:
        fail("InvalidArgument: reversesRef: caller must declare " + reverses_key + " in contextHint.reads")
    if not vertex_alive(state, reverses_key):
        fail("UnknownTransaction: " + reverses_key)

    entry_key = reverses_key + ".entry"
    if entry_key not in state:
        fail("InvalidArgument: reversesRef: caller must declare " + entry_key + " in contextHint.reads")
    entry = state[entry_key]
    if entry == None or (hasattr(entry, "isDeleted") and entry.isDeleted):
        fail("UnknownTransaction: " + reverses_key + ": no .entry aspect")

    # read-posture: (e) relation=postedTo epoch=none -- a cafetransaction
    # carries exactly one postedTo link, written atomically by the op that
    # minted the transaction and never added to afterward, so this is never a
    # keyspace scan and nothing races it.
    posted_page, _ = kv.Links(reverses_key, "postedTo", "out")
    posted_to = None
    for lk in posted_page:
        if not lk.isDeleted:
            posted_to = lk.targetVertex
    if posted_to != acct_key:
        fail("InvalidArgument: reversesRef: " + reverses_key +
             " is not posted to account " + acct_key)

    if entry.data.get("type") != "debit":
        fail("InvalidArgument: reversesRef: only a posted charge (a debit) can be refunded; " +
             reverses_key + " is a " + str(entry.data.get("type")))

    charge_cents = entry.data.get("amountCents")
    if charge_cents == None:
        fail("InvalidArgument: reversesRef: " + reverses_key + " carries no amountCents")

    refunded_cents = entry.data.get("refundedCents", 0)
    remaining_cents = charge_cents - refunded_cents
    if amount_cents > remaining_cents:
        # No transaction key in the text: the front desk toasts this message
        # verbatim, and a staffer reading "vtx.cafetransaction.<id>" learns
        # nothing they can act on — the charge is the line they clicked.
        fail("RefundExceedsCharge: amountCents " + str(amount_cents) + " exceeds the " +
             str(remaining_cents) + " still refundable on this charge")

    tally_data = {}
    for k, v in entry.data.items():
        tally_data[k] = v
    tally_data["refundedCents"] = refunded_cents + amount_cents
    # Class "transactionEntry" restated rather than read off the hydrated
    # document: post_entry is the sole writer of a .entry aspect and writes
    # exactly that class, so the upsert asserts the shape it expects instead of
    # propagating whatever it happened to find.
    tally = make_aspect_upsert_occ(reverses_key, "entry", "transactionEntry",
                                   tally_data, entry.revision)
    return reverses_id, tally

def post_entry(state, op, entry_type, event_class, allow_tab_ref, allow_reverses_ref, confine):
    p = op.payload
    acct_key = required_string(p, "accountKey")
    _, acct_id = parts_of(acct_key, "accountKey", "cafeaccount")

    if not vertex_alive(state, acct_key):
        fail("UnknownAccount: " + acct_key)

    # Staff-standing confinement: the location comes from the ACCOUNT's own
    # heldFor lease, never from the payload, so the workplace it resolves to
    # cannot be forged. Earliest point the location is derivable -- the account
    # has to be known alive before its topology means anything.
    # workplace-exempt: (per-call-site) post_entry is itself the exemption
    # helper here -- whether confinement applies at all is the caller's
    # decision, so the discharge belongs to the execute() dispatch below.
    if confine and not workplace_exempt():
        require_workplace([account_unit(acct_key)],
                          "cannot post to account " + acct_key)

    amount_cents = require_cents(p, "amountCents")
    if amount_cents <= 0:
        fail("InvalidArgument: amountCents: required positive number")
    memo = optional_string(p, "memo")

    # Resident-self ownership (CreditCafeAccount only — permissions.go grants
    # no self-scope DebitAccount): op.authTargetValidated (workplace_exempt(),
    # already checked above) only proves the caller's target names THEMSELVES
    # (the platform's own scope=self match); it says nothing about whether the
    # ACCOUNT being credited is theirs, so that ownership proof is this
    # branch's job, mirroring loftspace-ledger's CreditAccount /
    # clinic-ledger's ClinicCreditAccount. Ownership is ALL this branch
    # proves: the amount cap is not resident-specific and binds the payment leg
    # whoever submitted it, below. The mere PRESENCE of
    # authContextTarget selects this branch, same idiom as cafe-domain's
    # Charge/Settle — it does not change what grant actually authorized the
    # op (a scope=any operator/frontOfHouse submit never attaches a target),
    # it only ever narrows behavior.
    # authcontext-target: (selector) a branch selector, not a confinement
    # exemption -- so it reads the raw target (did the caller declare a self
    # target at all) rather than authTargetValidated. Safe because presence
    # only pushes the caller onto the STRICTER branch below (the ownership
    # proof), never grants anything a scope=any submit would not already.
    if op.authContextTarget != "":
        if entry_type != "credit":
            fail("AuthDenied: a resident may only credit (pay down) their own account, not charge it")
        # authcontext-target: (ownership) the value derives an identity whose
        # ownership of the account's own lease is then proven by the
        # applicationFor link read below -- a forged target only fails closed.
        # The lease is recovered from the account's OWN heldFor topology,
        # never the payload, so a forged claim only fails closed.
        _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
        # read-posture: (e) relation=heldFor epoch=none -- a cafeaccount
        # carries exactly one heldFor link, so this is never a keyspace scan.
        held_for_page, _ = kv.Links(acct_key, "heldFor", "out")
        lease_key = None
        for lk in held_for_page:
            if not lk.isDeleted:
                lease_key = lk.targetVertex
        if lease_key == None:
            fail("AuthDenied: account " + acct_key + " carries no live lease")
        _, lease_id = parts_of(lease_key, "heldFor target", "leaseapp")
        # read-posture: (e) per-candidate follow-up read off the enumeration
        # above -- the lease id is data-derived, unknowable client-side.
        application_for = kv.Read("lnk.leaseapp." + lease_id + ".applicationFor.identity." + target_identity_id)
        if application_for == None or application_for.isDeleted:
            fail("AuthDenied: a resident may only pay down their own lease's account")

    # Everything above this line is the caller's standing and the payload's
    # shape; everything below it touches the ACCOUNT's own money. The order is
    # deliberate: the .balance read and — on a legacy account — the bounded
    # history replay behind it are the most expensive work this script does, so
    # they sit after the confinement walk and the resident-ownership proof.
    # Hoisting them above those checks would hand anyone holding the scope=self
    # grant a way to name a stranger's account key and spend the whole replay
    # budget before being denied.
    #
    # is_payment: a CreditCafeAccount that is not a refund. It is the one leg
    # the amount cap binds, and the one leg that ever pays for a legacy
    # account's backfill.
    is_payment = entry_type == "credit" and not allow_reverses_ref

    # .balance is a declared OPTIONALREADS key — this script's own
    # derive_reads(op) declares it for every op it handles, so the key is
    # hydrated or recorded known-absent whatever the submitter sent, and every
    # dispatcher declares it statically besides (opmetas.go for the two
    # descriptor-driven ops, cafe-domain's targets.go for the Weaver-dispatched
    # charge). Absence-tolerant, because an account minted under cafe-ledger
    # < 0.4.0 carries no .balance and a required read would reject every entry
    # against such an account.
    balance_key = acct_key + ".balance"
    # read-posture: (d) optionalReads — derived server-side by this script's own
    # derive_reads(op) for DebitAccount/CreditCafeAccount/RefundCafeCharge
    # (Contract #2 §2.5 class (g)), and declared statically by opmetas.go's
    # OpDispatchSpec.OptionalReads + cafe-domain's targets.go
    # GapActionSpec.OptionalReads (FE descriptor and Weaver directOp alike).
    balance_doc = kv.Read(balance_key)
    # Two questions, not one. balance_absent decides the WRITE verb: a create
    # is refused against a tombstone (Contract #3 §3.3), so only a genuinely
    # absent key may be minted and a tombstoned one is revived by the update.
    # needs_backfill decides whether there is a number to read at all.
    balance_absent = balance_doc == None
    needs_backfill = balance_absent or balance_doc.isDeleted

    # balance_cents stays None on a legacy account this op does not backfill —
    # a charge or a refund. Such an account keeps NO cache rather than a wrong
    # one: seeding it from this entry alone would record a total that never
    # counted the history behind it, and every later payment would be measured
    # against that. The account stays legacy until a payment first touches it,
    # and that payment's replay sums the whole history, this entry included.
    balance_cents = None
    if not needs_backfill:
        # The CLASS, not just the key — the same doctrine reversed_charge
        # applies to the charge's own .entry: this script is the sole writer of
        # a .balance aspect and writes exactly that class, so a document of any
        # other class under this key is a fault to refuse, never a number to
        # spend a payment cap on.
        if not hasattr(balance_doc, "class") or getattr(balance_doc, "class") != "cafeAccountBalance":
            fail("InvalidState: this account's balance aspect is not a cafeAccountBalance")
        balance_cents = balance_doc.data.get("balanceCents")
        if balance_cents == None:
            balance_cents = 0
    elif is_payment:
        balance_cents = backfill_balance(acct_key)

    # Amount trust, on EVERY CreditCafeAccount leg — the resident's own
    # scope=self submit and the operator/frontOfHouse scope=any submit alike.
    # Nothing on this platform verifies that a payment actually happened (no
    # payment-rail integration — out of scope for a reference vertical), and
    # that is as true of an amount a staffer keys at the counter as of one a
    # resident types: a mis-keyed $50 against a $14.25 tab posts an account to
    # -$35.75, off the arrears grid, with no op that undoes it. So the cap
    # binds the op, not the caller. The outstanding balance comes from the
    # account's OWN maintained .balance aspect (read above, O(1) — never the
    # payload), which post_entry itself keeps in lockstep with every posted
    # entry. A payment may never exceed what is owed.
    #
    # RefundCafeCharge is deliberately NOT capped here (allow_reverses_ref
    # selects it): its ceiling is the reversed charge's own un-refunded
    # remainder, enforced by reversed_charge under a CAS. A refund of a charge
    # the resident already paid legitimately takes the account negative — that
    # is money going back, not debt being forgiven.
    #
    # Neither refusal names the account: both are toasted verbatim, at a
    # staffer or at the resident, and a raw vtx key is not something either can
    # act on. The amounts are the actionable half, so they are spelled as
    # money.
    if is_payment:
        owed_cents = balance_cents
        if owed_cents <= 0:
            fail("AuthDenied: this account has no outstanding balance to pay")
        if amount_cents > owed_cents:
            fail("AuthDenied: a payment of " + dollars(amount_cents) +
                 " exceeds the outstanding balance of " + dollars(owed_cents))

    # tabRef (DebitAccount only — the cafe-domain Settle consumer): the tab
    # this charge settles. A tab-settlement playbook dispatch (cafe-domain's
    # cafeTabSettlement Weaver target) always declares row.tabKey in Reads, so
    # the tab is hydrated here; a plain human-submitted DebitAccount omits it
    # entirely (nothing below runs) — the loftspace-ledger clauseRef precedent.
    tab_key = None
    tab_id = None
    if allow_tab_ref:
        tab_key = optional_string(p, "tabRef")
        if tab_key != None:
            _, tab_id = parts_of(tab_key, "tabRef", "tab")
            if not vertex_alive(state, tab_key):
                fail("UnknownTab: " + tab_key)

    # reversesRef (RefundCafeCharge only): the posted charge this credit gives
    # back, REQUIRED on that op — a refund with no charge named is just a
    # payment, which is exactly the confusion the op exists to end. Every other
    # entry rejects the field outright rather than ignore it: a caller that
    # sends reversesRef to CreditCafeAccount means to record a correction, and
    # silently posting an unlinked payment would leave the statement saying the
    # resident handed money over.
    reverses_id = None
    reverses_tally = None
    if allow_reverses_ref:
        reverses_id, reverses_tally = reversed_charge(state, p, acct_key, amount_cents)
    elif hasattr(p, "reversesRef") and getattr(p, "reversesRef") != None:
        fail("InvalidArgument: reversesRef: only valid on RefundCafeCharge, not " + op.operationType)

    tx_id = nanoid.new()
    tx_key = "vtx.cafetransaction." + tx_id
    posted_at = time.rfc3339_utc(op.submittedAt)

    entry_data = {"type": entry_type, "amountCents": amount_cents, "postedAt": posted_at}
    if memo != None:
        entry_data["memo"] = memo

    # postedTo: the transaction (later-arriving) is the source, the
    # pre-existing account is the target (Contract #1 §1.1). Reads as "this
    # transaction posted to this account."
    posted_to_lnk = "lnk.cafetransaction." + tx_id + ".postedTo.cafeaccount." + acct_id

    # Root data minimal (D5): {} on root. The charge/payment fact is the
    # .entry aspect; the only thing this DDL ever mutates on the ACCOUNT is
    # its .balance cache, appended just below.
    mutations = [
        make_vtx(tx_key, "cafetransaction", {}),
        make_aspect(tx_key, "entry", "transactionEntry", entry_data),
        make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
    ]

    # The cache moves only where there is a cache to move: balance_cents is
    # None exactly on a legacy account this op does not backfill, and such an
    # account is left untouched rather than seeded from one entry.
    #
    # The sign convention is the cafeLedgerHistory lens's own sum (a debit
    # increases what is owed, a credit — payment or refund alike — decreases
    # it). A refund is not bounded by owed_cents (only the payment cap above
    # enforces that), so this legitimately goes negative when a paid-in-full
    # charge is given back.
    #
    # Which verb: a key step 4 saw genuinely ABSENT is minted by a create, and
    # because that absence was declared (optionalReads) the create carries it as
    # its assertion, so a lost two-way race on that first touch is itself
    # retry-eligible (absentConditionedCreates, commit_path.go). A key that is
    # PRESENT — live or tombstoned — takes the bare-update path instead:
    # auto-conditioned on the step-4 hydrated revision, so it serializes AND
    # retries under a concurrent writer, and revives a tombstone that a create
    # would only collide with (make_aspect_update's own comment).
    if balance_cents != None:
        if entry_type == "debit":
            new_balance_cents = balance_cents + amount_cents
        else:
            new_balance_cents = balance_cents - amount_cents
        if balance_absent:
            mutations.append(make_aspect(acct_key, "balance", "cafeAccountBalance",
                                         {"balanceCents": new_balance_cents}))
        else:
            mutations.append(make_aspect_update(acct_key, "balance", "cafeAccountBalance",
                                                {"balanceCents": new_balance_cents}))

    events = [{"class": event_class,
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "amountCents": amount_cents}}]

    if tab_key != None:
        # settles: the transaction (later-arriving) is the source, the
        # pre-existing tab is the target (Contract #1 §1.1) — the "which tab
        # did this charge settle?" chain of custody the cafeTabSettlement
        # lens's missing_charge gate reads to detect the charge is posted.
        settles_lnk = "lnk.cafetransaction." + tx_id + ".settles.tab." + tab_id
        mutations.append(make_link(settles_lnk, tx_key, tab_key, "settles", "settles", {}))

    if reverses_id != None:
        # reverses: the refund (later-arriving) is the source, the pre-existing
        # charge is the target (Contract #1 §1.1) — "this refund reverses that
        # charge". The LINK is the refund's whole identity: the entry itself
        # stays an ordinary credit, so every balance consumer sums it unchanged,
        # and the cafeLedgerHistory lens walks this hop to tell the statement
        # which line is a correction rather than a payment.
        reverses_lnk = "lnk.cafetransaction." + tx_id + ".reverses.cafetransaction." + reverses_id
        mutations.append(make_link(reverses_lnk, tx_key, "vtx.cafetransaction." + reverses_id,
                                   "reverses", "reverses", {}))
        # The charge's refundedCents tally, in the same atomic batch as the
        # credit it accounts for: the ceiling and the entry that consumes it
        # move together or neither does.
        mutations.append(reverses_tally)

    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

NANOID_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

def is_cafeaccount_key(key):
    # Contract #1's whole vertex grammar for a cafeaccount, not a prefix test.
    # derive_reads returns keys the Processor validates against that grammar,
    # answering a malformed one with a DeriveReadsInvalid hydration fault raised
    # BEFORE the operation's own validation runs. Deriving straight off an
    # unvalidated payload would therefore turn post_entry's clean
    # "InvalidArgument: accountKey" into an opaque hydration failure. A
    # derivation never fails, and never widens what the operation itself
    # rejects.
    if key == None or type(key) != type(""):
        return False
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != "cafeaccount":
        return False
    if len(parts[2]) != 20:
        return False
    for ch in parts[2].elems():
        if ch not in NANOID_ALPHABET:
            return False
    return True

def derive_reads(op):
    # Contract #2 §2.5 class (g). The Processor runs this at the head of step 4
    # and merges the result into the declared read set, so the account's
    # .balance aspect is hydrated — or recorded known-absent — on EVERY
    # dispatch of these three ops, whatever the submitter happened to declare.
    #
    # That guarantee is the point, not the saved round trip. .balance is the
    # quantity the payment cap is measured against AND a key every posted entry
    # updates, and a bare update is auto-conditioned on the step-4 hydrated
    # revision only for a key the operation declared (Contract #3 §3.2). A
    # submitter that omitted the declaration would get a live read and an
    # UNCONDITIONED update, so K concurrent payments could each pass the cap
    # against the same balance and credit K times it. A guard a caller can
    # switch off by not mentioning it is not a guard, and contextHint is
    # caller-supplied and never enforced — hence this, the channel the platform
    # owns. The dispatchers' own static declarations (opmetas.go,
    # cafe-domain's targets.go) stay: they document the read set, this
    # guarantees it.
    #
    # optionalReads, never reads: an account minted under cafe-ledger < 0.4.0
    # carries no .balance, and a required read's absence is a HydrationMiss that
    # would block every entry against such an account rather than let a payment
    # backfill it.
    #
    # The op argument is a struct -- op.operationType, op.actor, op.payload
    # (also a struct). No kv, no nanoid: both are fail-closed stubs in this
    # pass, and a derivation that reads state is a read, not a derivation.
    ot = op.operationType
    if ot != "DebitAccount" and ot != "CreditCafeAccount" and ot != "RefundCafeCharge":
        return {}
    # optional_string, never required_string: a missing or malformed accountKey
    # derives nothing rather than faulting the pre-pass -- post_entry's own
    # required_string/parts_of still raise the real InvalidArgument.
    acct_key = optional_string(op.payload, "accountKey")
    if not is_cafeaccount_key(acct_key):
        return {}
    return {"optionalReads": [acct_key + ".balance"]}

def execute(state, op):
    ot = op.operationType

    if ot == "DebitAccount":
        # A charge is posted by the cafeTabSettlement playbook dispatch, not by
        # a human at the counter, and its grant stays operator-only -- there is
        # no staff path to confine, so confinement is not attempted.
        # workplace-exempt: (no-validated-path) DebitAccount declares one
        # scope=any grant to [operator] (permissions.go) and no package mints a
        # task forOperation it, so op.authTargetValidated is never legitimately
        # true. Granting it to a staff role, or minting a task for it, makes
        # this claim false and requires confine=True here.
        return post_entry(state, op, "debit", "account.debited", True, False, False)

    if ot == "CreditCafeAccount":
        # workplace-exempt: (ownership-bound) CreditCafeAccount declares a
        # scope=self grant too (permissions.go): a resident's self-scoped
        # submit sets op.authTargetValidated (the platform's own target==actor
        # check), exempting it from the workplace walk below -- but that only
        # discharges once post_entry's own authContextTarget branch proves the
        # ACCOUNT itself is theirs (the heldFor->leaseapp->applicationFor
        # walk). An operator or frontOfHouse scope=any submit carries no
        # target, so it still clears via actor_holds_operator /
        # require_workplace as before.
        return post_entry(state, op, "credit", "account.credited", False, False, True)

    if ot == "RefundCafeCharge":
        # A refund is never self-scoped. permissions.go grants it scope=any to
        # [operator, frontOfHouse] and to NO consumer, so a submit carrying a
        # target is either a client that misread the descriptor or a caller
        # probing for a resident path that does not exist. Refusing here rather
        # than falling through matters because post_entry's own
        # authContextTarget branch is written for CreditCafeAccount: it treats a
        # credit whose target owns the account's lease as a resident paying down
        # their own tab. The amount cap that bounds an ordinary payment does not
        # bind a refund at all — a refund's ceiling is the reversed charge's
        # un-refunded remainder, and it may legitimately take the balance
        # negative — so a resident reaching that branch through THIS op would be
        # minting credits against their own charges, giving themselves money
        # back for coffee they drank.
        # authcontext-target: (selector) selects the refusal branch and only
        # that -- presence never grants anything here, it is the whole reason
        # the submission stops.
        #
        # The refusal tests the validated bit as well as the raw target,
        # because the validated bit is the one that DISCHARGES the workplace
        # walk this op relies on (workplace_exempt). The raw target is a client
        # hint any caller can set; validation is a property of the auth PATH
        # that matched -- platform scope=self, or a task's ephemeral grant
        # (internal/processor/operation_context.go). Both of those paths
        # additionally require a non-empty target, so on today's Processor the
        # second conjunct catches nothing the first does not. That subsumption
        # is the PLATFORM's invariant, not this package's, and the two
        # conjuncts fail under different edits: granting this op scope=self
        # reaches only the first, while a task minted forOperation
        # RefundCafeCharge is authorized entirely by the second. A refund that
        # reached post_entry on the task path would arrive both exempt from the
        # workplace walk and on the resident-credit branch, so the guard names
        # the bit it actually depends on.
        # workplace-exempt: (no-validated-path) permissions.go declares one
        # scope=any grant to [operator, frontOfHouse] and no package mints a
        # task forOperation RefundCafeCharge, so op.authTargetValidated is
        # never legitimately true -- and this refusal stops it regardless, so
        # the confine=True call below is reachable only by a standing grant,
        # which require_workplace binds.
        if op.authContextTarget != "" or op.authTargetValidated:
            fail("AuthDenied: RefundCafeCharge is a front-desk act, never self-scoped")
        # tabRef is DebitAccount's field and is refused here rather than
        # ignored, the mirror of post_entry's own refusal of reversesRef on
        # every op but this one. A caller that sends one means "refund the
        # charge that settled this tab" and would instead get a credit with no
        # settles link and no relation to the tab at all -- a silent drop is
        # the shape that leaves a ledger disagreeing with what was asked for.
        if hasattr(op.payload, "tabRef") and getattr(op.payload, "tabRef") != None:
            fail("InvalidArgument: tabRef: only valid on DebitAccount, not RefundCafeCharge")
        # workplace-exempt: (per-call-site) confine=True below hands the
        # discharge to post_entry's own require_workplace site — a frontOfHouse
        # staffer may refund only a charge on an account whose lease sits
        # somewhere they worksAt, exactly as CreditCafeAccount confines a
        # payment; the operator stays unconfined by the holdsRole walk.
        return post_entry(state, op, "credit", "account.credited", False, True, True)

    fail("transaction DDL: unknown operationType: " + ot)
`
