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
// account (D5): the balance is derived by the cafeLedgerHistory lens, never
// stored here.
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

        # Root data minimal (D5): {} on root. The balance is derived by the
        # cafeLedgerHistory lens summing linked transactions, never stored
        # here.
        mutations = [
            make_vtx(acct_key, "cafeaccount", {}),
            make_aspect(lease_key, "cafeLedgerAccount", "cafeLedgerAccountGuard", {"accountKey": acct_key}),
            make_link(held_for_lnk, acct_key, lease_key, "heldFor", "heldFor", {}),
        ]
        events = [{"class": "account.created",
                   "data": {"accountKey": acct_key, "leaseAppKey": lease_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": acct_key}}

    fail("account DDL: unknown operationType: " + ot)
`

// transactionDDLScript handles DebitAccount and CreditCafeAccount. Each mints a
// fresh transaction vertex + a .entry aspect + the postedTo link to the
// account. The ledger is append-only: no aspect on the account is read or
// mutated here, so concurrent debits/credits against the same account never
// race a read-modify-write — the balance is derived by the
// cafeLedgerHistory lens summing entries.
const transactionDDLScript = `
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

# Self-credit balance-verification budget (post_entry's authContextTarget
# branch): 10 pages of 50 postedTo entries covers many years of a house-tab
# history; an account that exceeds it fails the self-credit closed rather
# than trust a partial sum.
SELF_CREDIT_PAGE_LIMIT = 50
SELF_CREDIT_MAX_PAGES = 10

def post_entry(state, op, entry_type, event_class, allow_tab_ref, confine):
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

    # Resident-self ownership + amount trust (CreditCafeAccount only —
    # permissions.go grants no self-scope DebitAccount): op.authTargetValidated
    # (workplace_exempt(), already checked above) only proves the caller's
    # target names THEMSELVES (the platform's own scope=self match); it says
    # nothing about whether the ACCOUNT being credited is theirs, so that
    # ownership proof is this branch's job, mirroring loftspace-ledger's
    # CreditAccount / clinic-ledger's ClinicCreditAccount. The mere PRESENCE
    # of authContextTarget selects this branch, same idiom as cafe-domain's
    # Charge/Settle — it does not change what grant actually authorized the
    # op (a scope=any operator/frontOfHouse submit never attaches a target),
    # it only ever narrows behavior.
    # authcontext-target: (selector) a branch selector, not a confinement
    # exemption -- so it reads the raw target (did the caller declare a self
    # target at all) rather than authTargetValidated. Safe because presence
    # only pushes the caller onto the STRICTER branch below (the ownership +
    # amount proofs), never grants anything a scope=any submit would not
    # already.
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

        # Amount trust: nothing on this platform verifies a self-submitted
        # payment actually happened (no payment-rail integration — out of
        # scope for a reference vertical), so an unbounded self-credit would
        # let a resident forgive their own debt for free. The outstanding
        # balance is recomputed from the account's OWN postedTo transaction
        # history (never trusted from the payload), paginated + bounded: an
        # account whose history exhausts the page budget fails closed
        # (denies) rather than trusts a partial sum. A self-credit may never
        # exceed what is actually owed.
        owed_cents = 0
        cursor = None
        budget_exhausted = True
        for _page in range(SELF_CREDIT_MAX_PAGES):
            # read-posture: (e) relation=postedTo epoch=none -- bounded by the
            # page budget; exhausting it below fails closed.
            page, cursor = kv.Links(acct_key, "postedTo", "in", cursor, SELF_CREDIT_PAGE_LIMIT)
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
                    owed_cents += tx_amount
                elif tx_entry.data.get("type") == "credit":
                    owed_cents -= tx_amount
            if cursor == None:
                budget_exhausted = False
                break
        if budget_exhausted:
            fail("AuthDenied: could not verify account " + acct_key + "'s balance (too much transaction history)")
        if owed_cents <= 0:
            fail("AuthDenied: account " + acct_key + " has no outstanding balance to pay")
        if amount_cents > owed_cents:
            fail("AuthDenied: amountCents exceeds account " + acct_key + "'s outstanding balance of " + str(owed_cents))

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
    # .entry aspect; the account itself is untouched (append-only ledger).
    mutations = [
        make_vtx(tx_key, "cafetransaction", {}),
        make_aspect(tx_key, "entry", "transactionEntry", entry_data),
        make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
    ]
    events = [{"class": event_class,
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "amountCents": amount_cents}}]

    if tab_key != None:
        # settles: the transaction (later-arriving) is the source, the
        # pre-existing tab is the target (Contract #1 §1.1) — the "which tab
        # did this charge settle?" chain of custody the cafeTabSettlement
        # lens's missing_charge gate reads to detect the charge is posted.
        settles_lnk = "lnk.cafetransaction." + tx_id + ".settles.tab." + tab_id
        mutations.append(make_link(settles_lnk, tx_key, tab_key, "settles", "settles", {}))

    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

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
        return post_entry(state, op, "debit", "account.debited", True, False)

    if ot == "CreditCafeAccount":
        # workplace-exempt: (ownership-bound) CreditCafeAccount now declares a
        # scope=self grant too (permissions.go): a resident's self-scoped
        # submit sets op.authTargetValidated (the platform's own target==actor
        # check), exempting it from the workplace walk below -- but that only
        # discharges once post_entry's own authContextTarget branch proves the
        # ACCOUNT itself is theirs (the heldFor->leaseapp->applicationFor
        # walk). An operator or frontOfHouse scope=any submit carries no
        # target, so it still clears via actor_holds_operator /
        # require_workplace as before.
        return post_entry(state, op, "credit", "account.credited", False, True)

    fail("transaction DDL: unknown operationType: " + ot)
`
