package loftspaceledger

import "fmt"

// RecurringChargePeriod is the validity span DebitAccount stamps onto an
// UNTERMED period="monthly" clause's .status.chargeValidUntil as
// chargeValidUntil = postedAt + RecurringChargePeriod (a Go duration string,
// time.ParseDuration form) — the recurring-clause analog of lease-signing's
// bgcheckFreshnessWindow (freshness_window.go). A clause whose .terms carry
// validFrom/validUntil never uses it: its due dates walk the calendar-month
// anniversary grid from validFrom (see the script). Baked into
// transactionDDLScript at package-init time via fmt.Sprintf, same pattern as
// leaseServiceReplyDDLScript.
const RecurringChargePeriod = "720h"

// ArrearsGraceDays is the customary grace between a rent charge falling due
// and the arrears reminder going out. Rent is due ON its recorded date and
// "N days overdue" counts from that date; the reminder waits out the grace, so
// a charge that posts on its own due date does not nag the same morning. The
// package OWNS the grace, so there is exactly one source for it: the Starlark
// below reads it as a Go duration string (ArrearsGraceDays × 24 hours, the
// form time.rfc3339_add takes) and cmd/loftspace-app reads the constant
// directly. The instant a tenant's statement says the reminder was armed for
// and the instant the notification fires are the same fact, and two copies of
// it drift into a statement that says one thing while the notification says
// another.
//
// Days × 24h is exact here because every timestamp this ledger stores is
// canonical UTC (time.rfc3339_utc at write time), where a calendar day is
// always 24 hours — Go's AddDate(0, 0, ArrearsGraceDays) over a UTC instant
// lands on the same second.
const ArrearsGraceDays = 5

// ArrearsPageLimit is the number of postedTo entries ONE dispatch of
// EvaluateLoftspaceArrears consumes — one kv.Links page — and ArrearsMaxPages
// the number of such pages an account's history may run to before the op
// records historyTooLong instead. The replay is RESUMABLE: a history longer
// than one page is folded page by page across successive dispatches, each
// recording its running aggregate and the cursor to resume from on the
// account's own .arrears.replay checkpoint, and Weaver chaining the
// dispatches through the lens's two phase gaps (lenses.go). The page, not the
// history, is what one execution pays for — the same round-trip arithmetic
// against the Processor's production script wall that sizes
// clinic-ledger's ArrearsPageLimit / ArrearsMaxPages (scripts.go there);
// this ledger runs no per-credit netting walk, so the same page limit carries
// more headroom here, not less.
const (
	ArrearsPageLimit = 30
	ArrearsMaxPages  = 20
)

// ArrearsPhaseA / ArrearsPhaseB are the two values of the replay checkpoint's
// phase, which flips on every page. The lens projects one continuation gap
// per value (lenses.go), so the page written under one phase closes the gap
// that dispatched it and opens the other; the op, the lens's cypher and the
// aspect DDL's schema all read the literal from here.
const (
	ArrearsPhaseA = "a"
	ArrearsPhaseB = "b"
)

// arrearsGracePrelude binds ArrearsGraceDays into Starlark, once, as the Go
// duration string time.rfc3339_add takes, together with the replay's page
// constants and phase literals. Only the account DDL computes an arrears
// reminder instant (EvaluateLoftspaceArrears — this ledger stores no balance,
// so post_entry never names a head and never derives a date), so only that
// script opens with it.
//
// Prepended rather than interpolated with a format verb, so no future edit to
// the script body is responsible for escaping a literal '%'.
var arrearsGracePrelude = fmt.Sprintf(`
ARREARS_GRACE_DURATION = "%dh"
ARREARS_PAGE_LIMIT = %d
ARREARS_MAX_PAGES = %d
ARREARS_PHASE_A = %q
ARREARS_PHASE_B = %q
`, ArrearsGraceDays*24, ArrearsPageLimit, ArrearsMaxPages, ArrearsPhaseA, ArrearsPhaseB)

// accountDDLScript is the account DDL's Starlark, opened by the grace binding
// above.
var accountDDLScript = arrearsGracePrelude + accountDDLScriptBody

// accountDDLScriptBody handles LoftspaceCreateAccount. The account gets its OWN
// independently-minted NanoID — vertex NanoIDs are unique identifiers across
// all of Core KV, never reused across vertex types, even deliberately (a
// prior revision minted the account under the lease's own bare NanoID;
// internal/refractor/adjacency keys strictly by bare NodeID with no type
// qualifier, so that reuse silently merged the account's and the lease's
// adjacency edges under one key and corrupted graph traversal for both — see
// adjacency-shared-nanoid-collision-design.md). "One account per lease" is
// instead enforced by a deterministic CREATE-ONLY guard aspect on the
// PRE-EXISTING leaseapp (leaseAppKey + ".ledgerAccount") — a second
// LoftspaceCreateAccount for the same lease conflicts on that already-existing aspect
// key, the same "let the key shape be the uniqueness guard" idiom, just
// anchored on the pre-existing parent instead of a freshly-minted sibling.
// Root data stays {} on the account (D5): the balance is derived by the
// ledgerHistory lens, never stored here. Granted to operator + frontOfHouse
// (permissions.go) — frontOfHouse is confined to the lease's own building via
// the workplace-confinement guard below (a leaseapp sits at a unit, unlike
// clinic-ledger/wellness-ledger's practice-wide patient/member, so this
// create op cannot go unconfined the way theirs did).
//
// It ALSO handles EvaluateLoftspaceArrears, the Weaver-dispatched arrears
// evaluation (the wellness-ledger EvaluateWellnessArrears mechanism applied
// to this ledger, which likewise stores no balance): it recomputes the
// account's FIFO-oldest open charge over a resumable, page-per-dispatch
// replay of the postedTo history (a history longer than one page records its
// running aggregate as a checkpoint on .arrears.replay and Weaver dispatches
// the next page), records that charge's own recorded due date and the
// reminder instant the grace puts after it in the account's own .arrears
// aspect, and — once the reminder instant has passed and no reminder has gone
// out in this episode — fires the external.notification the bridge turns
// into a real message to the tenant. The FIFO is NOT maintained incrementally by
// post_entry: this ledger stores no balance, so a posted entry cannot even
// tell an episode opening from one continuing, let alone name the head a
// partial payment moved to. The head is recomputed once, here, only when it
// matters — post_entry keeps only the coarse "what is recorded no longer
// describes this account" mark (stale) the convergence lens reads as a
// request for this recomputation, and the never-evaluated gap (evaluatedAt
// absent) is what first evaluates every account.
const accountDDLScriptBody = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_update(vtx_key, local_name, cls, data):
    # Deliberately NO expectedRevision — a bare update on a key the operation
    # DECLARED is auto-conditioned on the step-4 hydrated revision (Contract
    # #3 §3.2) and marked retry-eligible, so a lost race re-hydrates and
    # re-runs instead of hard-conflicting; this script's own derive_reads is
    # what guarantees the declaration. It is also the reviving verb for a
    # tombstoned aspect, which a create would only collide with (Contract #3
    # §3.3).
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
# A staff actor may write only inside the location it worksAt. Mirrors
# lease-signing's DecideLeaseApplication / cafe-ledger's CreditCafeAccount
# guard verbatim (Starlark has no cross-package import, so each package that
# grants a staff role a confined op carries its own copy). Three properties
# make this sound; each is a trap a simpler form falls into.
#
# 1. The exemption is ROLE-derived, never worksAt-derived. Exempting "an actor
#    with no worksAt link" would be perverse: UnwireWorksAt would WIDEN a staff
#    member's write surface from one building to everywhere. The exemption is
#    holding the primordial 'operator' role, so an actor that is genuinely
#    root necessarily has it. Everyone else is confined, and an actor holding
#    no roles at all is confined to nothing.
#
# 2. A tombstoned link is ABSENT. kv.Read returns the tombstone DOCUMENT
#    rather than None, and UnwireWorksAt tombstones rather than deletes, so a
#    plain '== None' test would let a moved-on staff member keep writing.
#
# 3. The location is resolved from the TARGET's own topology, never from a
#    payload field — a caller cannot forge which building it is writing at.
ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4
WORKPLACE_PARENT_PAGE_LIMIT = 20
MAX_PARENT_PAGES = 4
WORKPLACE_MAX_DEPTH = 8
WORKPLACE_MAX_NODES = 64
# A page of one is not enough for a REPOINTED single-valued relation: ListLinks
# returns tombstoned links in the page too, keys sort by target id, and a
# repoint tombstones the old key and writes a new one -- so the live link can
# sort behind its own tombstoned predecessor. lease-signing's
# ReassignLeaseUnit repoints appliesToUnit; every reader of it pages until it
# finds the live one.
LIVE_LINK_PAGE_LIMIT = 8
MAX_LIVE_LINK_PAGES = 4

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not a compile-time constant (see lease-signing's
    # identical resolver for why). Paginated and fail-closed: exhausting the
    # page budget still denies rather than assuming the role is absent.
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds
        # few roles, so this is never a keyspace scan.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the
            # enumeration above (data-derived key -- the role is unknown
            # until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def worksAt_covers(actor_id, location_key):
    # Answers "does this actor worksAt this location, or any LIVE location
    # that contains it?" — a breadth-first walk up containedIn, tested on
    # EVERY node (the starting one included) so a dead ancestor confers
    # nothing but a dead starting location confers everything. Bounded three
    # ways (depth/page/node budget) so an op-time guard cannot fan out;
    # exhausting a bound falls through to the final denial, never an escape.
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
                continue
            # read-posture: (e) per-candidate follow-up read off the
            # containedIn enumeration below -- the location VERTEX, so a
            # tombstoned one neither confers a match nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the same
            # enumeration (data-derived key -- the ancestor chain is not
            # knowable client-side, so it cannot be pre-declared).
            lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
            if lnk != None and not lnk.isDeleted:
                return True
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a
                # location has at most a few parents; containment is
                # provisioned topology, not written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE lease_unit runs --
    # require_workplace(lease_unit(x), ...) would walk lease topology even for
    # root. Call sites gate on this; require_workplace re-checks it anyway, so
    # forgetting the gate is still correct, only slower.
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only (operator/frontOfHouse via scope=any). The
    # validated scope=self path -- a landlord opening the account of a lease
    # on a unit they manage (permissions.go) -- returns early here and is
    # bound by require_manages instead, the guard that can see it.
    if op.authTargetValidated:
        return
    enforce_workplace(location_keys, what)

def enforce_workplace(location_keys, what):
    # location_keys is a LIST -- covering ANY ONE of them authorizes the
    # write. An empty/all-None list denies anyone but an operator, so an
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
    # Is this vertex present AND not tombstoned? Distinct from
    # vertex_alive(state, key): lease_unit's hop is DATA-derived (resolved
    # from a link mid-walk), so unknowable client-side and undeclarable --
    # only a live read can see it.
    if key == None:
        return False
    # read-posture: (e) one bounded read; the key is data-derived, resolved
    # from a kv.Links enumeration mid-walk.
    node = kv.Read(key)
    return node != None and not node.isDeleted

def lease_unit(lease_key):
    # A lease's location is its unit, one platform-written hop away
    # (appliesToUnit, required at CreateLeaseApplication) — unlike
    # cafe-ledger's account_unit this resolves straight from the payload's
    # OWN leaseAppKey (already validated alive above), not via an
    # account-side heldFor hop, since at LoftspaceCreateAccount time the
    # account does not exist yet to hold one.
    # A leaseapp carries exactly one LIVE appliesToUnit link, but
    # ReassignLeaseUnit (lease-signing) repoints it (tombstone old, create
    # new), and a page can hold the tombstone before the live link, so this
    # pages until it finds one rather than trusting the first page.
    cursor = None
    unit = None
    for _page in range(MAX_LIVE_LINK_PAGES):
        # read-posture: (e) relation=appliesToUnit epoch=none -- bounded,
        # never a keyspace scan.
        page, cursor = kv.Links(lease_key, "appliesToUnit", "out", cursor, LIVE_LINK_PAGE_LIMIT)
        for lk in page:
            if not lk.isDeleted:
                unit = lk.targetVertex
        if unit != None or cursor == None:
            break
    if not vertex_live(unit):
        return None
    return unit

def require_manages(unit_key, what):
    # The landlord ownership probe -- the scope=self counterpart to
    # require_workplace above, binding the path that guard deliberately cannot
    # see. A signed-in landlord holds no worksAt link and authorizes via a
    # scope=self grant, so what confines them is their own management link to
    # the unit the lease under the write applies to. Mirrors lease-signing's
    # DecideLeaseApplication probe.
    #
    # It binds the platform-VALIDATED self path and only that path, which is why
    # it keys on authTargetValidated rather than on the raw target's presence:
    # a scope=any holder (operator/frontOfHouse) whose client happens to send
    # its own key is authorized by step 3 on the standing grant WITHOUT
    # inspecting the target, so narrowing it here would confine an unconfined
    # actor to whatever it happens to manage; a task grant also validates a
    # target, but its target is the task's resource, not an identity, so the
    # equality with op.actor excludes it. A scope=self caller cannot escape:
    # step 3 denies scope=self outright when the target is absent and when
    # target != actor, so reaching this op on that grant means both hold.
    #
    # authcontext-target: (ownership) the target is used only as op.actor's own
    # key, on a path the platform already proved equal to the actor, and the
    # authority it buys is then proven by the manages LINK read below.
    if not op.authTargetValidated or op.authContextTarget != op.actor:
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    if unit_key == None:
        fail("AuthDenied: no unit resolves for this lease, so no management link can authorize it; " + what)
    _, unit_id = parts_of(unit_key, "unit", "unit")
    # read-posture: (e) per-candidate follow-up read off the appliesToUnit
    # enumeration in lease_unit (data-derived key -- the unit is not knowable
    # until the lease's own link resolves, so it cannot be pre-declared).
    lnk = kv.Read("lnk.identity." + actor_id + ".manages.unit." + unit_id)
    if lnk == None or lnk.isDeleted:
        # The unit key is deliberately NOT named: the caller reached here with a
        # lease key it already holds, and echoing the unit that lease sits on
        # would turn a denial into a lookup for a resource it does not manage.
        fail("AuthDenied: " + op.actor + " does not manage the unit this lease is on; " + what)

# EvaluateLoftspaceArrears replays the account's postedTo history ONE PAGE PER
# DISPATCH: arrears_entries consumes a single kv.Links page of
# ARREARS_PAGE_LIMIT entries (bound from Go's ArrearsPageLimit via the prelude,
# where the round-trip arithmetic against the Processor's 250 ms script wall
# lives) and folds it into a running aggregate. A history that fits one page
# finalizes in the same execution; a longer one records the aggregate and the
# cursor to resume from on the account's .arrears.replay checkpoint, and the
# next dispatch — Weaver's, through the lens's phase gaps — continues from
# there. A page pays the same whatever the history's length, so any history up
# to ARREARS_PAGE_LIMIT × ARREARS_MAX_PAGES entries is reached exactly. This
# budget is independent of the self-credit balance-verification replay in
# transactionDDLScript (SELF_CREDIT_PAGE_LIMIT / SELF_CREDIT_MAX_PAGES) — that
# walk answers a different question (the amount owed, inside one op's own
# script wall) and is unaffected by this one's constants.
#
# The aggregate carries every entry EXACTLY, not a sum: unlike clinic-ledger's
# checkpoint (which nets per-credit reversals against specific debits and so
# collapses every plain credit into one running total, sortable anywhere ahead
# of the debits it offsets), this ledger's arrears_head also derives the
# EPISODE START — the postedAt of the debit that arrived when the open queue
# was last empty — a quantity that depends on the REAL chronological
# interleaving of every debit and credit, not only on their totals. Collapsing
# the credits into one total and resorting it first would still name the right
# head and balance (a credit total offsets the oldest open debits first
# whichever page it was read on), but it can retire a debit with a credit that
# in reality posted AFTER it, which erases the very debit whose postedAt was
# the episode's start. So every entry — debit or credit — is folded into the
# aggregate under its own transaction ID and its own postedAt, and arrears_rows
# hands arrears_head the exact same rows a single, whole-history execution
# would have: no netting, no algebraic shortcut, no reverses relation to walk
# (this ledger has none — there is no refund verb, README).
# No entry in this ledger names another it reverses, so the fold has nothing
# else to compute.
#
# Keyed by the transaction's bare ID, never its full vtx key: the checkpoint
# carries an IDENTITY to re-derive from, not a relationship to stand in for
# one (CLAUDE.md's no-key-list-index rule — the same reason clinic-ledger's
# own checkpoint keys its debits/reversed maps by id, design doc §2). A page
# reads the id off its own live kv.Links enumeration (parts_of), and
# arrears_rows reconstructs the full vtx.transaction.<id> key the finalize
# walk needs for its (postedAt, key) tie-break; the checkpoint itself is never
# read as a substitute for enumerating postedTo.
#
# An account whose history runs past ARREARS_MAX_PAGES pages is not aged
# against a truncated FIFO — a partial replay would name the wrong head and
# the reminder that went out would name a charge the tenant had already paid.
# But it does not fail either: see the degrade branch in execute(). A refusal
# there is a PERMANENT silent stop, because the only thing that re-drives the
# op is the very gap this account's row opens, and a rejected op never closes
# it — Weaver would re-dispatch a doomed evaluation on every window, forever,
# with no reminder and no operator signal. Instead the exhaustion is RECORDED
# (historyTooLong, with the entry budget it exhausted as historyBudget) so the
# row goes quiet, the operator can see it in the read model, the next posted
# entry re-arms one more attempt, and a raised budget reaches the accounts the
# smaller one parked, once.
def arrears_entries(acct_key, cursor, agg):
    # One page of the account's live postedTo entries, folded into agg =
    # {"entries": {txId: {postedAt, type, amountCents, dueAt}}}: every debit
    # and every credit recorded under its own transaction ID (never its full
    # vtx key — the checkpoint carries identity, not a key-list index; see the
    # comment above this function). dueAt is the entry's OWN recorded due date
    # (DebitAccount stamps it on a clause-authorized recurring charge from the
    # clause's anniversary grid; a payment or a landlord one-off records none)
    # and carries no meaning on a credit — it is kept here (unlike the rest of
    # the record) because arrears_head reads it off every DEBIT row to name
    # the head's own recorded due date; dropping it would force arrears_rows
    # to re-read it live, one round trip per debit, on every finalize. Returns
    # (agg, next_cursor); next_cursor is None once the enumeration is
    # exhausted. Keyed by id, not by postedAt: identity is what makes the fold
    # exact across pages when two entries share a second. An entry missing any
    # of postedAt/type/amountCents is skipped rather than guessed at, exactly
    # as the self-credit replay skips it.
    #
    # read-posture: (e) relation=postedTo epoch=none -- one page per dispatch;
    # the cursor is carried on the account's .arrears.replay checkpoint and
    # the caller degrades past ARREARS_MAX_PAGES pages.
    page, next_cursor = kv.Links(acct_key, "postedTo", "in", cursor, ARREARS_PAGE_LIMIT)
    for lk in page:
        if lk.isDeleted:
            continue
        # read-posture: (e) per-candidate follow-up read off the enumeration
        # above -- each transaction's own .entry aspect, data-derived and
        # unknowable client-side.
        tx_entry = kv.Read(lk.sourceVertex + ".entry")
        if tx_entry == None or tx_entry.isDeleted:
            continue
        tx_amount = tx_entry.data.get("amountCents")
        tx_posted_at = tx_entry.data.get("postedAt")
        tx_type = tx_entry.data.get("type")
        if tx_amount == None or tx_posted_at == None or tx_type == None:
            continue
        # A deduction moves custody (what comes back), never what is OWED —
        # it is neither a debit nor a credit, so the FIFO this replay feeds
        # must not see it at all: capturing it here would have arrears_head
        # age a "charge" nothing ever billed, or retire an open debit nothing
        # ever paid.
        if tx_type != "debit" and tx_type != "credit":
            continue
        _, tx_id = parts_of(lk.sourceVertex, "postedTo source", "transaction")
        agg["entries"][tx_id] = {"postedAt": tx_posted_at, "type": tx_type,
                                 "amountCents": tx_amount, "dueAt": tx_entry.data.get("dueAt")}
    return agg, next_cursor

def arrears_checkpoint(prior):
    # The replay checkpoint the recorded .arrears carries, or None when it
    # carries none — or one this op cannot resume. A checkpoint is resumable
    # only when every field the fold needs has the shape the fold wrote: a
    # non-empty cursor string, a phase the lens projects a gap for, a page
    # count of at least one, and a dict aggregate. Anything else is treated as
    # ABSENT — the evaluation starts again at page 1 over a fresh aggregate —
    # rather than resumed or refused: resuming would fold new pages onto a
    # corrupt aggregate and name a wrong head, and a refusal would leave
    # whichever gap dispatched this op open for Weaver to re-dispatch the same
    # doomed read until its retry budget parked the row. Restarting costs one
    # replay and records a well-formed checkpoint in its place.
    replay = prior.get("replay")
    if replay == None or type(replay) != type({}):
        return None
    cursor = replay.get("cursor")
    phase = replay.get("phase")
    pages = replay.get("pages")
    entries = replay.get("entries")
    if type(cursor) != type("") or cursor == "":
        return None
    if phase != ARREARS_PHASE_A and phase != ARREARS_PHASE_B:
        return None
    if type(pages) != type(0) or pages < 1:
        return None
    if type(entries) != type({}):
        return None
    return {"cursor": cursor, "phase": phase, "pages": pages, "entries": entries}

def arrears_rows(agg):
    # arrears_head's input, rebuilt from the finished aggregate: one row per
    # entry, its full vtx.transaction.<id> key reconstructed from the
    # checkpoint's bare id (the (postedAt, key) tie-break arrears_head sorts
    # on needs the same key shape a live kv.Links page would have handed it),
    # carrying its own postedAt — the exact rows a single, whole-history
    # execution would have handed arrears_head, folded page by page instead of
    # all at once. arrears_head's own FIFO walk, including its episode-start
    # tracking, runs exactly as it does over a single whole-history read.
    rows = []
    for tx_id, e in agg["entries"].items():
        rows.append({"postedAt": e["postedAt"], "key": "vtx.transaction." + tx_id, "type": e["type"],
                     "amountCents": e["amountCents"], "dueAt": e.get("dueAt")})
    return rows

def arrears_head(entries):
    # The FIFO the tenant's own statement runs, reproduced exactly
    # (cmd/loftspace-app's rent-arrears derivation): entries in (postedAt,
    # transactionKey) order. Credits offset the OLDEST still-open debit first,
    # and a credit with no open debit to apply to carries its remainder
    # forward as surplus that prepays whichever debits arrive next. The
    # survivor at the front of the queue is the charge that has actually been
    # unpaid longest — not merely the most recent one — which is the whole
    # reason a reminder can name a date the tenant recognises.
    #
    # The sort key is the PAIR, not postedAt alone: postedAt is whole-second
    # canonical UTC (time.rfc3339_utc), so two charges posted in the same
    # second are indistinguishable by time and the transaction key is what
    # makes the order total. Without it the op and the statement can disagree
    # about which of the two is the head, and so about the due date.
    rows = sorted(entries, key=lambda e: (e["postedAt"], e["key"]))

    open_debits = []
    surplus = 0
    balance_cents = 0
    episode_start = None
    for r in rows:
        amount = r["amountCents"]
        if r["type"] == "debit":
            balance_cents += amount
            if surplus >= amount:
                surplus -= amount
                continue
            amount -= surplus
            surplus = 0
            if len(open_debits) == 0:
                episode_start = r["postedAt"]
            open_debits.append({"postedAt": r["postedAt"], "dueAt": r["dueAt"], "key": r["key"], "remaining": amount})
        elif r["type"] == "credit":
            balance_cents -= amount
            remaining = amount
            # Starlark has no while: each pass either zeroes the remainder or
            # retires one open debit, so len+1 passes is an exact bound, not a
            # budget that can run out mid-walk.
            for _i in range(len(open_debits) + 1):
                if remaining <= 0 or len(open_debits) == 0:
                    break
                if open_debits[0]["remaining"] > remaining:
                    open_debits[0]["remaining"] -= remaining
                    remaining = 0
                else:
                    remaining -= open_debits[0]["remaining"]
                    open_debits = open_debits[1:]
            surplus += remaining
    # An empty queue and a non-positive balance are the same condition under
    # FIFO consumption (credits >= debits leaves nothing open, and vice versa),
    # which is why a "balance <= 0 has nothing to age" early return needs no
    # counterpart here.
    #
    # The walk ALSO tracks the EPISODE START, and execute() reads it as the
    # boundary a recorded send is judged against. An episode is the stretch
    # from the charge that took the account from square (nothing open) to
    # owing, until the queue is next empty; formally, the episode start is
    # the postedAt of the debit that was appended onto an EMPTY open queue —
    # a debit the surplus could not prepay outright, since a prepaid debit
    # never opens. The head is NOT necessarily that opener: a partial payment
    # that exactly retires the opener moves the head to the next open charge
    # while the account stays continuously in arrears, and that charge posted
    # after the opener — so a boundary read off the head's own postedAt would
    # mistake a moved head for a new episode and send a second reminder for
    # a debt the tenant is visibly paying down. Every debit processed before
    # the opener was retired (the queue was empty when it arrived), so the
    # opener is the oldest charge of the current episode and its postedAt is
    # the instant the episode began; a payment to zero empties the queue and
    # the next debit that opens it starts the next episode, resetting the
    # instant. No second walk over the history is needed to find it.
    #
    # The head's dueAt is the FACT the charge recorded about itself, never a
    # term added to its posting: a clause-authorized rent charge carries the
    # due date its anniversary grid gave it, and a charge that recorded none
    # (a landlord one-off) is due on receipt — its own postedAt. The head's
    # own key rides along: it is what makes the notification's episode key
    # unique, since recorded due dates repeat across charges on one lease.
    if len(open_debits) == 0:
        return None, balance_cents
    head = open_debits[0]
    due_at = head["dueAt"]
    if due_at == None:
        due_at = head["postedAt"]
    return {"postedAt": head["postedAt"], "dueAt": due_at, "key": head["key"],
            "episodeStart": episode_start}, balance_cents

def carry_arrears(doc):
    out = {}
    for k, v in doc.data.items():
        out[k] = v
    return out

NANOID_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

def is_account_key(key):
    # Contract #1's whole vertex grammar for an account, not a prefix test —
    # derive_reads returns keys the Processor validates against that grammar
    # and answers a malformed one with a DeriveReadsInvalid hydration fault
    # raised BEFORE the operation's own validation runs, which would turn this
    # branch's clean "InvalidArgument: accountKey" into an opaque hydration
    # failure. The same helper transactionDDLScript carries, for the same
    # reason.
    if key == None or type(key) != type(""):
        return False
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != "account":
        return False
    if len(parts[2]) != 20:
        return False
    for ch in parts[2].elems():
        if ch not in NANOID_ALPHABET:
            return False
    return True

def tenant_for_account(acct_key):
    # The lease this account is held for and the tenant behind it, as
    # (leaseAppKey, identityKey), each None where the hop does not resolve
    # LIVE -- an account whose lease was withdrawn, or whose applicant has
    # since gone dead, still ages its own history normally. Two hops, unlike
    # the wellness ledger's one: an account is heldFor a LEASE, and the lease
    # is applicationFor the tenant's identity. Mirrors transactionDDLScript's
    # own self-credit ownership walk hop for hop.
    # read-posture: (e) relation=heldFor epoch=none -- an account carries
    # exactly one heldFor link, so this is never a keyspace scan. A page of
    # one is exact here, unlike the LIVE_LINK paging applicationFor needs
    # below: heldFor is written once at LoftspaceCreateAccount and never
    # repointed or tombstoned, so no tombstone can sort ahead of it.
    held_for_page, _ = kv.Links(acct_key, "heldFor", "out", None, 1)
    lease_key = None
    for lk in held_for_page:
        if not lk.isDeleted:
            lease_key = lk.targetVertex
    if lease_key == None:
        return None, None
    # The lease VERTEX itself: WithdrawLeaseApplication tombstones the leaseapp
    # without cascading to its links, so applicationFor dangles live off a
    # dead lease -- a withdrawn lease names no tenant to remind.
    # read-posture: (e) per-candidate follow-up read off the heldFor
    # enumeration above (data-derived key, via vertex_live).
    if not vertex_live(lease_key):
        return None, None
    identity = None
    cursor = None
    for _page in range(MAX_LIVE_LINK_PAGES):
        # read-posture: (e) relation=applicationFor epoch=none -- a leaseapp
        # carries exactly one LIVE applicationFor link, but a page can hold a
        # tombstoned predecessor before the live one, so this pages until it
        # finds one rather than trusting the first page (lease-signing's
        # live_link_target, the same walk every reader of the relation runs).
        page, cursor = kv.Links(lease_key, "applicationFor", "out", cursor, LIVE_LINK_PAGE_LIMIT)
        for lk in page:
            if not lk.isDeleted:
                identity = lk.targetVertex
        if identity != None or cursor == None:
            break
    if identity == None:
        return lease_key, None
    # read-posture: (e) per-candidate follow-up read off the enumeration
    # above -- the identity ROOT (never a sensitive aspect: the root carries
    # no PII, so a shredded holder answers here like any other), data-derived
    # and unknowable client-side.
    identity_doc = kv.Read(identity)
    if identity_doc == None or identity_doc.isDeleted:
        return lease_key, None
    return lease_key, identity

def derive_reads(op):
    # Contract #2 §2.5 class (g), for the same reason transactionDDLScript's
    # own derive_reads exists: the .arrears write below is a bare update
    # auto-conditioned on the step-4 hydrated revision ONLY for a key the
    # operation declared (Contract #3 §3.2), and contextHint is
    # caller-supplied and never enforced. A dispatch that omitted the
    # declaration would get a live read and an UNCONDITIONED write, so two
    # evaluations racing one account (a redelivery alongside a fresh
    # dispatch) could each decide to send against the same prior state. The
    # loftspaceArrearsReminders target declares the same key (targets.go);
    # that DOCUMENTS the read set, this GUARANTEES it.
    #
    # optionalReads, never reads: every account alive today carries no
    # .arrears at all, and a required read's absence is a HydrationMiss that
    # would block the very first evaluation of each one.
    #
    # The account ROOT itself rides the same declaration, for a distinct
    # reason: vertex_alive(state, acct_key) below decides UnknownAccount by
    # testing acct_key not in state, which cannot tell "genuinely absent"
    # from "never declared or derived" apart. Any dispatcher that omits the
    # root from its own contextHint would otherwise see a live account
    # rejected as unknown.
    if op.operationType != "EvaluateLoftspaceArrears":
        return {}
    acct_key = optional_string(op.payload, "accountKey")
    if not is_account_key(acct_key):
        return {}
    return {"optionalReads": [acct_key, acct_key + ".arrears"]}

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "EvaluateLoftspaceArrears":
        # actor-guard: (primordial) restricted to Weaver's dispatch actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind this
        # op is operator/Scope:"any", which admits every operator-role holder —
        # far wider than the one engine that dispatches the
        # loftspaceArrearsReminders directOp playbook. accountKey arrives off
        # the payload and the account it names is forwarded in the
        # external.notification body the bridge turns into a real message to a
        # tenant, so a wider submitter set is a forged send: an arbitrary
        # operator naming any account it likes and having the platform tell
        # that tenant they owe rent. First statement in the branch: it also
        # denies the payload-shape, vertex-alive and history-shape oracles
        # beneath it.
        if op.actor != primordialActor["weaver"]:
            fail("AuthDenied: EvaluateLoftspaceArrears is restricted to Weaver's dispatch actor; got " + op.actor)

        acct_key = required_string(p, "accountKey")
        parts_of(acct_key, "accountKey", "account")

        # Liveness guard: never mint arrears state (or a 4-segment aspect key)
        # on an absent or tombstoned account. The root is hydrated whatever the
        # dispatcher declared (derive_reads above).
        if not vertex_alive(state, acct_key):
            fail("UnknownAccount: " + acct_key + " is absent or tombstoned; no arrears evaluated")

        # The lease and the tenant this account is held for, resolved LIVE
        # from the account's own heldFor out-link and the lease's own
        # applicationFor out-link -- never from the payload, so the tenant a
        # reminder is addressed to cannot be forged by an arbitrary submitter
        # (the same forged-send surface the actor guard above closes, one
        # step further along). Both optional: an account whose lease was
        # withdrawn is still evaluated -- the arrears fact is about the
        # ACCOUNT -- and the resolved keys are routed into the notification's
        # params purely for the adapter's own addressing; nothing this op
        # decides depends on them. Neither is ever a Params hop on the Weaver
        # row: the strategist refuses to dispatch a row whose Params reference
        # a null column, which would starve exactly the accounts most worth
        # aging.
        lease_key, identity_key = tenant_for_account(acct_key)

        # The op's own timestamp, normalized to canonical UTC so the lexical
        # compare against remindAt below is sound to the second (a raw compare
        # mis-answers for the first second after an instant, '.' sorting
        # below 'Z').
        evaluated_at = time.rfc3339_utc(op.submittedAt)

        arrears_key = acct_key + ".arrears"
        # read-posture: (d) optionalReads — derived server-side by this script's
        # own derive_reads(op) for EvaluateLoftspaceArrears (Contract #2 §2.5
        # class (g)), and declared statically by the loftspaceArrearsReminders
        # target's GapActionSpec.OptionalReads (targets.go). Two questions,
        # not one: absence decides the WRITE VERB (a create is refused against
        # a tombstone, Contract #3 §3.3, so only a genuinely absent key is
        # minted), presence-and-live decides whether there is prior episode
        # state to carry.
        arrears_doc = kv.Read(arrears_key)
        arrears_absent = arrears_doc == None
        prior = {}
        if arrears_doc != None and not arrears_doc.isDeleted:
            # The CLASS, not just the key: this package is the sole writer of
            # a .arrears aspect and writes exactly that class, so a document
            # of any other class here is a fault to refuse, never state to
            # decide a send on.
            if not hasattr(arrears_doc, "class") or getattr(arrears_doc, "class") != "loftspaceAccountArrears":
                fail("InvalidState: this account's arrears aspect is not a loftspaceAccountArrears")
            prior = carry_arrears(arrears_doc)

        # The replay checkpoint, if a previous dispatch left one: the pages
        # consumed so far, the cursor to resume from and the running
        # aggregate. Absent, this dispatch starts at page 1 over a fresh
        # aggregate. A recorded value is read as the fact it records — a
        # checkpoint is progress through an enumeration, not an evaluation, so
        # nothing here decides a send.
        replay = arrears_checkpoint(prior)
        cursor = None
        agg = {"entries": {}}
        pages = 1
        if replay != None:
            cursor = replay["cursor"]
            agg = {"entries": replay["entries"]}
            pages = replay["pages"] + 1

        if pages > ARREARS_MAX_PAGES:
            # DEGRADE, never refuse. The account's history outran the replay
            # budget, so the FIFO head is unknown and no send can be justified —
            # but a rejection would be a permanent silent stop: the row's own
            # gap is the only thing that re-drives this op, and a rejected op
            # leaves it open, so Weaver would re-dispatch the same doomed
            # evaluation every window and nothing would ever be sent or seen.
            # Recording the exhaustion instead makes it OBSERVABLE and QUIET:
            # the lens suppresses both the gap and the timer on
            # historyTooLong, so the dispatch loop stops while the row stays
            # in the weaver-targets bucket for an operator to find. The budget
            # exhausted is recorded beside the flag (historyBudget, in
            # entries), so the lens can tell an account parked under a smaller
            # budget from one parked under this one and re-arm the former
            # exactly once. What was already recorded is carried untouched — a
            # reminder already sent stays recorded as sent, and a due date
            # already armed is not erased by an evaluation that could not read
            # the history. stale is dropped: it asks for a recomputation this
            # op has just attempted, and re-asking would re-open the gap the
            # degrade is closing. The checkpoint is dropped with it: a degrade
            # ends the replay, and a surviving checkpoint would keep a phase
            # gap open under the flag. post_entry's own stale write drops
            # historyTooLong in turn, so the next posted entry buys exactly
            # one more attempt — bounded to one replay per entry, never a
            # loop.
            data = {"evaluatedAt": evaluated_at, "historyTooLong": True,
                    "historyBudget": ARREARS_PAGE_LIMIT * ARREARS_MAX_PAGES}
            for carried in ["dueAt", "remindAt", "remindedFor", "sentAt"]:
                carried_value = prior.get(carried)
                if carried_value != None:
                    data[carried] = carried_value
            if arrears_absent:
                mutations = [make_aspect(acct_key, "arrears", "loftspaceAccountArrears", data)]
            else:
                mutations = [make_aspect_update(acct_key, "arrears", "loftspaceAccountArrears", data)]
            return {"mutations": mutations,
                    "events": [{"class": "account.arrearsEvaluated",
                                "data": {"accountKey": acct_key, "dueAt": data.get("dueAt"),
                                         "sentAt": data.get("sentAt"), "balanceCents": None,
                                         "historyTooLong": True}}],
                    "response": {"primaryKey": acct_key}}

        agg, next_cursor = arrears_entries(acct_key, cursor, agg)

        if next_cursor != None:
            # MID-REPLAY: the enumeration has more pages. Every recorded field
            # of the episode is carried verbatim — dueAt, remindAt,
            # remindedFor, sentAt, stale, evaluatedAt all still describe the
            # account exactly as the last COMPLETED evaluation or posted entry
            # left them; this dispatch has evaluated nothing, so evaluatedAt is
            # not re-stamped and nothing is sent — and the checkpoint is
            # rewritten whole: the advanced cursor, the page count, the
            # aggregate so far, and a phase that flips on every page. The
            # phase is Weaver's continuation trigger: the lens projects one
            # gap per phase, so the page that closes the gap that dispatched
            # it opens the other, and each gap episode is exactly one dispatch
            # (lenses.go). A redelivered dispatch reads the advanced
            # checkpoint and consumes the NEXT page — progress, never
            # repetition — and two dispatches racing one checkpoint serialize
            # on the .arrears revision this write is conditioned on
            # (make_aspect_update).
            phase = ARREARS_PHASE_A
            if replay != None and replay["phase"] == ARREARS_PHASE_A:
                phase = ARREARS_PHASE_B
            data = {}
            for k, v in prior.items():
                if k != "replay":
                    data[k] = v
            data["replay"] = {"phase": phase, "cursor": next_cursor, "pages": pages,
                              "entries": agg["entries"]}
            if arrears_absent:
                mutations = [make_aspect(acct_key, "arrears", "loftspaceAccountArrears", data)]
            else:
                mutations = [make_aspect_update(acct_key, "arrears", "loftspaceAccountArrears", data)]
            return {"mutations": mutations,
                    "events": [{"class": "account.arrearsReplayPaged",
                                "data": {"accountKey": acct_key, "pages": pages}}],
                    "response": {"primaryKey": acct_key}}

        # FINALIZE: the enumeration is exhausted, so the aggregate is the whole
        # history and the head is computed over it. Nothing below carries the
        # checkpoint — a finished evaluation has no replay in progress.
        head, balance_cents = arrears_head(arrears_rows(agg))

        # evaluatedAt is written on EVERY outcome, including "owes nothing":
        # its absence is what opens the convergence gap for an account that
        # has never been evaluated, so a run that recorded nothing would
        # re-dispatch forever.
        data = {"evaluatedAt": evaluated_at}
        events = []

        if head != None:
            # dueAt is the head's own recorded due date (arrears_head), and
            # remindAt is where the reminder is armed: the grace after it.
            # Both are written on every owed evaluation — dueAt is what the
            # statement counts "N days overdue" from, remindAt is what the
            # convergence lens arms its timer at and compares the recorded
            # lapse against.
            due_at = head["dueAt"]
            remind_at = time.rfc3339_add(due_at, ARREARS_GRACE_DURATION)
            data["dueAt"] = due_at
            data["remindAt"] = remind_at
            reminded_for = prior.get("remindedFor")
            sent_at = prior.get("sentAt")
            # The EPISODE BOUNDARY. This ledger stores no balance, so nothing
            # ends an episode at the entry that pays it off: a payment to
            # zero and a fresh charge can both post before any evaluation
            # runs, and the recorded send record then belongs to an episode
            # that is already over. arrears_head's episodeStart is the
            # postedAt of the charge that OPENED the current episode (not the
            # head, which a partial payment may have moved past the opener
            # while the tenant stayed in arrears), and a send for an earlier
            # episode necessarily happened while that episode's head was
            # still open — before the payment that squared the account, and
            # so before the charge that opened this episode — so a send
            # stamped BEFORE the current episode's opener is read as an
            # earlier episode's and dropped here (remindedFor with it: the
            # two are written together and mean nothing apart). A send AT or
            # AFTER the opener is this episode's own and is carried, whether
            # the head has since moved or not. The tie belongs to THIS
            # episode: a send is stamped by an evaluation that had already
            # seen the opener posted, so sentAt >= episodeStart always holds
            # for this episode's own send, and it lands in the opener's very
            # second when a charge posted five or more days after its
            # recorded due date is evaluated in the second it posts (the
            # never-evaluated or stale gap); an earlier episode's send at
            # the identical second would need the send, the clearing payment
            # and the new charge all inside one second. The compare is on the
            # canonical whole-second UTC instants both values are written as.
            episode_start = head["episodeStart"]
            if sent_at != None and sent_at < episode_start:
                sent_at = None
                reminded_for = None
            if remind_at <= evaluated_at:
                # The head's grace has run out. remindedFor records THIS due
                # date whatever else happens: it is the conjunct the
                # convergence lens reads, so leaving it naming an older head
                # would hold the gap open and have Weaver re-dispatch this
                # same evaluation forever.
                data["remindedFor"] = due_at
                # Whether anything is SENT is a different question, and its
                # answer is sentAt's absence. The unit of "one reminder" is the
                # EPISODE — the stretch from the charge that took the account
                # from square to owing, until the balance comes back to zero —
                # not the head, which a partial payment moves from one open
                # charge to the next while the tenant stays continuously in
                # arrears. Keying the send off remindedFor instead would nag
                # on every part-payment: pay some, the head shifts to a charge
                # whose own grace has also run out, and a second notification
                # goes out for a debt the tenant is visibly paying down.
                # sentAt is carried across every write of a live episode and
                # dropped only where the episode itself ends (the no-open-head
                # branch below, and the episode-boundary check above when a
                # newer episode has already opened — post_entry never ends
                # one, since it has no balance to see zero on), so its
                # absence means exactly "nothing has gone out in this
                # episode". sentAt records the SEND INTENT — stamped on the
                # commit that emits the outbox event; the adapter's delivery
                # outcome lands on .arrearsNotification (notifications.go).
                if sent_at != None:
                    data["sentAt"] = sent_at
                else:
                    data["sentAt"] = evaluated_at
                    # Keyed on (accountKey, dueAt, headKey): a redelivery of
                    # the same due episode reuses the key so the adapter
                    # dedups, while a later episode mints a fresh one and
                    # sends again. The head's own key is what makes the
                    # episode key unique: recorded due dates REPEAT across
                    # charges on one lease (a second clause on the same
                    # anniversary grid, a backfilled first period), so a key
                    # of the account and the date alone would have the
                    # adapter dedup a later episode's reminder away while
                    # sentAt recorded it as sent. Fired off this op's own
                    # transactional outbox — no Loom pattern, the bridge's
                    # dispatch path is fully generic.
                    ext_ref = acct_key + ":" + due_at + ":" + head["key"]
                    notif_params = {"accountKey": acct_key, "reminderType": "loftspaceRentArrears",
                                    "dueAt": due_at, "balanceCents": balance_cents}
                    if lease_key != None:
                        notif_params["leaseAppKey"] = lease_key
                    if identity_key != None:
                        notif_params["identityKey"] = identity_key
                    events.append({"class": "external.notification",
                                   "data": {"instanceKey": ext_ref, "adapter": "notification",
                                            "replyOp": "RecordLoftspaceArrearsReminderNotification",
                                            "externalRef": ext_ref, "idempotencyKey": ext_ref,
                                            "params": notif_params}})
            else:
                # The head's grace has not run out yet: the timer re-arms at
                # this remindAt, and the episode's record — whichever head it
                # was reminded for, and when — is carried forward unchanged.
                # Dropping it would re-open the gate and send a second time
                # for the same episode.
                if reminded_for != None:
                    data["remindedFor"] = reminded_for
                if sent_at != None:
                    data["sentAt"] = sent_at
        # head == None: nothing is owed, so the episode is over and
        # {evaluatedAt} alone is written — dueAt, remindAt, remindedFor, sentAt
        # and stale all go with it, which is what lets the NEXT charge open a
        # clean episode rather than inherit this one's send record. An episode
        # ends ONLY in this op: post_entry has no balance to reason from, so a
        # payment to zero marks the state stale and this recomputation is
        # what clears it — here when the history nets to nothing owed, and
        # in the boundary check above when a fresh charge had already opened
        # the next episode before this evaluation ran.
        #
        # stale is never carried on ANY path: recomputing the head from the
        # account's own history is precisely what stale asks for, and it has
        # just happened. historyTooLong and historyBudget go the same way for
        # the same reason — this evaluation read the whole history, so
        # whatever recorded that it once could not is answered, and the lens
        # un-suppresses the row. The replay checkpoint is not carried either:
        # the enumeration it recorded progress through is finished.

        if arrears_absent:
            mutations = [make_aspect(acct_key, "arrears", "loftspaceAccountArrears", data)]
        else:
            mutations = [make_aspect_update(acct_key, "arrears", "loftspaceAccountArrears", data)]

        events.append({"class": "account.arrearsEvaluated",
                       "data": {"accountKey": acct_key, "dueAt": data.get("dueAt"),
                                "sentAt": data.get("sentAt"), "balanceCents": balance_cents}})
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": acct_key}}

    if ot == "LoftspaceCreateAccount":
        lease_key = required_string(p, "leaseAppKey")
        _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")

        # No-orphan invariant: the lease MUST be alive.
        if not vertex_alive(state, lease_key):
            fail("UnknownLeaseApplication: " + lease_key)

        # Confinement: whichever path authorized this write, it is bound to
        # the LEASE's own appliesToUnit topology, never a payload field, so
        # the unit cannot be forged. Staff on the standing path must worksAt
        # a location covering it (mirrors DecideLeaseApplication /
        # CreditCafeAccount -- a leaseapp sits at a building, unlike
        # clinic/wellness's practice-wide patient/member, so this create op
        # cannot be granted unconfined); a landlord on the validated
        # scope=self path must manage it. The walk is unconditional because
        # both guards consume it and each binds the path the other cannot
        # see; an operator pays one bounded link enumeration.
        account_unit = lease_unit(lease_key)
        # workplace-exempt: (ownership-bound) require_manages IS the ownership
        # proof for the scope=self grant (permissions.go): it requires the
        # acting landlord to manage the unit the lease's own link names, so
        # the validated self path never reaches the write unconfined. A task
        # grant would also set authTargetValidated and pass require_manages's
        # self-action early return; no package mints a task forOperation this
        # op -- add a resource bind here before any does.
        require_manages(account_unit, "cannot open ledger account for lease " + lease_key)
        # workplace-exempt: (ownership-bound) require_manages above binds the
        # scope=self path to this same unit; re-stated because the
        # intervening statement puts the pre-gate out of annotation range.
        if not workplace_exempt():
            # workplace-exempt: (ownership-bound) same discharge as the
            # pre-gate above.
            require_workplace([account_unit],
                               "cannot open ledger account for lease " + lease_key)

        # One account per lease, guarded by a deterministic aspect on the
        # PRE-EXISTING leaseapp (not the account — the account's own id is
        # independent and unknown until minted below). Only meaningful when
        # the caller declared the guard key in contextHint.reads (a
        # repeat/racing caller checking before it retries); the FIRST
        # LoftspaceCreateAccount for a lease declares only leaseAppKey (the guard
        # doesn't exist yet — declaring an as-yet-absent key in reads would
        # HydrationMiss on first touch, deferred past hydration), so on that
        # path the guard aspect's own
        # create-only write is the actual uniqueness enforcement: a genuine
        # race's loser hits a raw substrate conflict here rather than this
        # clean rejection.
        guard_key = lease_key + ".ledgerAccount"
        if vertex_alive(state, guard_key):
            fail("AccountAlreadyExists: " + lease_key)

        acct_id = nanoid.new()
        acct_key = "vtx.account." + acct_id

        # heldFor: the account (later-arriving) is the source, the pre-existing
        # lease is the target (Contract #1 §1.1). Reads as "this account is
        # held for this lease."
        held_for_lnk = "lnk.account." + acct_id + ".heldFor.leaseapp." + lease_id

        # Root data minimal (D5): {} on root. The balance is derived by the
        # ledgerHistory lens summing linked transactions, never stored here.
        mutations = [
            make_vtx(acct_key, "account", {}),
            make_aspect(lease_key, "ledgerAccount", "ledgerAccountGuard", {"accountKey": acct_key}),
            make_link(held_for_lnk, acct_key, lease_key, "heldFor", "heldFor", {}),
        ]
        events = [{"class": "account.created",
                   "data": {"accountKey": acct_key, "leaseAppKey": lease_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": acct_key}}

    fail("account DDL: unknown operationType: " + ot)
`

// aspectDeclarationOnlyScript is the declaration-only Starlark for
// ledgerAccountGuard — written by LoftspaceCreateAccount's own op handler, never
// dispatched as an operation in its own right.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

// transactionDDLScript handles DebitAccount, LoftspaceRecordCharge,
// CreditAccount and ReturnDeposit. Each mints a fresh transaction vertex + a
// .entry aspect + the postedTo link to the account. The ledger is append-only:
// no aspect on the account is read or mutated here, so concurrent
// debits/credits against the same account never race a read-modify-write —
// the balance is derived by the ledgerHistory lens summing entries.
//
// ReturnDeposit (the LoftSpace "a lease takes a security deposit" design) is
// the one entry that is not post_entry's: it credits a charged deposit clause's
// amount back once the lease's tenancy has ended, links the credit
// authorizedBy the clause exactly as DebitAccount's charge was, and marks the
// clause's .status returned under OCC — this package's second writer of that
// aspect after DebitAccount, beside semantic-contracts' own four (the
// clauseStatus DDL there lists every one). Every key it reads is hydrated:
// the lease, the account, the clause, its .terms and .status, the lease's
// .tenancy and the two deterministic custody links, all supplied by this
// script's own derive_reads whatever the submitter declared, so a live read
// never decides a refusal.
//
// A self-scoped submit (authContext.target present) is bound to the account's
// own heldFor topology along one of two paths, resident first: the lease's
// applicationFor holder may credit only, capped at the outstanding balance;
// otherwise the holder of a manages link to the lease's appliesToUnit unit
// (the landlord) may debit and credit, uncapped. lease_unit / vertex_live are
// this program's own copies of the account script's walk (Starlark has no
// cross-program import; vertex_live is S10-pinned byte-identical).
//
// The clauseValidUntil computation (Fire V3) is pure arithmetic on the op's
// own posted_at (time.rfc3339_add), so post_entry stays read-free for that
// leg.
//
// The one aspect on the ACCOUNT a posted entry does touch is its .arrears
// episode state (class loftspaceAccountArrears, ddls.go), and only ever to
// mark it: this ledger stores no balance, so an entry has no before/after
// number to tell an episode opening from one continuing, or a payment that
// clears the balance from one that leaves some. It therefore records exactly
// one thing — "what is recorded no longer describes this account" (stale),
// carrying every other field — and mints nothing where nothing exists, since
// an account with no arrears state is already opening the never-evaluated
// gap. Which charge is now oldest-and-open, whether the account is square,
// and whether a reminder is due are all functions of the whole history, and
// that recomputation belongs to EvaluateLoftspaceArrears (accountDDLScript),
// which the stale mark asks for. The write is a BARE update auto-conditioned
// on the revision the key hydrated at (Contract #3 §3.2), and this script's
// own derive_reads is what guarantees the key is hydrated whatever the
// submitter declared — so concurrent entries against one account serialize
// on it and retry rather than dropping a mark.
var transactionDDLScript = fmt.Sprintf(`
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_vtx_update(key, cls, data):
    # The make_aspect_update idiom applied to a VERTEX ROOT (no vertexKey/
    # localName on a root document, unlike an aspect's). Deliberately NO
    # expectedRevision — a bare update on a key the op declared is
    # auto-conditioned on the step-4 hydrated revision (Contract #3 §3.2)
    # and retry-eligible, so a lost race re-hydrates and re-executes against
    # the winner's now-committed write instead of silently coexisting with
    # it. Callers pass the root's OWN data unchanged: this is a pure
    # CAS/idempotency anchor between two ops that would otherwise share no
    # written key, never a real content change.
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

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
    # maintained mark two ops can race on needs.
    #
    # It is also the reviving verb for a TOMBSTONED aspect. A create against a
    # tombstone is refused (Contract #3 §3.3), so post_entry's .arrears write
    # comes here for any present key and the auto-conditioning above pins the
    # document's own revision, so the write races nothing.
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

def carry_arrears(doc):
    # Every field of the account's recorded arrears state, copied forward,
    # with ONE exception. The mark post_entry adds is an ADDITION to that
    # state, never a rewrite of it: the send record (remindedFor, sentAt) is
    # what stops a second notification going out for an episode already
    # reminded for, and dropping it while marking the state stale would send
    # twice for one debt.
    #
    # historyTooLong (with the historyBudget recorded beside it) is the first
    # exception, and only this script drops it. It records that an evaluation
    # could not read the account's history inside its replay budget, and the
    # lens holds the row QUIET while it stands — no gap, no timer. Carrying it
    # across a posted entry would make that quiet permanent for the life of
    # the account. Dropping it here is what buys exactly one more attempt per
    # entry: this write also sets stale, so the gap re-opens once, the
    # evaluation runs once, and if the history is still too long it records
    # the mark again and the row goes quiet again. One op per entry, never a
    # loop.
    #
    # replay is the second: the checkpoint of an evaluation part-way through
    # the account's postedTo history — the pages consumed, the cursor, the
    # running aggregate. This entry changes the enumerated set under that
    # cursor, so the checkpoint no longer describes a prefix of the history
    # and is dropped; the stale mark this write sets re-opens the evaluation
    # gap, and the next evaluation starts again at page 1.
    out = {}
    for k, v in doc.data.items():
        if k == "historyTooLong" or k == "historyBudget" or k == "replay":
            continue
        out[k] = v
    return out

NANOID_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

def is_account_key(key):
    # Contract #1's whole vertex grammar for an account, not a prefix test —
    # derive_reads returns keys the Processor validates against that grammar
    # and answers a malformed one with a DeriveReadsInvalid hydration fault
    # raised BEFORE the operation's own validation runs, which would turn
    # post_entry's clean "InvalidArgument: accountKey" into an opaque
    # hydration failure. The same helper accountDDLScript carries, for the
    # same reason.
    if key == None or type(key) != type(""):
        return False
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != "account":
        return False
    if len(parts[2]) != 20:
        return False
    for ch in parts[2].elems():
        if ch not in NANOID_ALPHABET:
            return False
    return True

def is_vertex_key(key, want_type):
    # is_account_key's grammar for any vertex type: derive_reads derives the
    # clause and lease keys ReturnDeposit reads by the same rule, so a
    # malformed payload key derives nothing and the handler's own parts_of
    # raises the clean InvalidArgument.
    if key == None or type(key) != type(""):
        return False
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != want_type:
        return False
    if len(parts[2]) != 20:
        return False
    for ch in parts[2].elems():
        if ch not in NANOID_ALPHABET:
            return False
    return True

def arrears_stale_mark(acct_key):
    # The account's .arrears episode state (class loftspaceAccountArrears,
    # ddls.go). This ledger stores no balance, so a posted entry cannot tell an
    # episode opening from one continuing, or a clearing payment from a
    # partial one. It does the one thing it can: mark what already EXISTS
    # stale — a request for EvaluateLoftspaceArrears to recompute the head
    # (and whether there still is one) from the account's own history — and
    # mint nothing where nothing exists, since an account with no arrears
    # state is already opening the never-evaluated gap. Every debit and every
    # credit lands here alike, whichever op posted it: a charge behind an
    # older head changes nothing the head reads, but this op cannot know it
    # is behind one. Returns the mutation list to append — one update, or
    # none.
    arrears_key = acct_key + ".arrears"
    # read-posture: (d) optionalReads — derived server-side by this script's own
    # derive_reads(op) for every entry op (Contract #2 §2.5 class (g)), and
    # declared statically by opmetas.go's OpDispatchSpec.OptionalReads.
    # Absence-tolerant: no account carries .arrears until an evaluation has run
    # on it.
    arrears_doc = kv.Read(arrears_key)
    if arrears_doc == None or arrears_doc.isDeleted:
        return []
    # The CLASS, not just the key: this package is the sole writer of a
    # .arrears aspect and writes exactly that class, so a document of any
    # other class here is a fault to refuse, never state to carry.
    if not hasattr(arrears_doc, "class") or getattr(arrears_doc, "class") != "loftspaceAccountArrears":
        fail("InvalidState: this account's arrears aspect is not a loftspaceAccountArrears")
    arrears_data = carry_arrears(arrears_doc)
    arrears_data["stale"] = True
    return [make_aspect_update(acct_key, "arrears", "loftspaceAccountArrears", arrears_data)]

# Self-credit balance-verification budget (post_entry's resident branch):
# 10 pages of 50 postedTo entries covers many years of a monthly rent
# history; an account that exceeds it fails the self-credit closed rather
# than trust a partial sum.
SELF_CREDIT_PAGE_LIMIT = 50
SELF_CREDIT_MAX_PAGES = 10
# A page of one is not enough for a REPOINTED single-valued relation: ListLinks
# returns tombstoned links in the page too, keys sort by target id, and a
# repoint tombstones the old key and writes a new one -- so the live link can
# sort behind its own tombstoned predecessor. lease-signing's
# ReassignLeaseUnit repoints appliesToUnit; every reader of it pages until it
# finds the live one.
LIVE_LINK_PAGE_LIMIT = 8
MAX_LIVE_LINK_PAGES = 4

def vertex_live(key):
    # Is this vertex present AND not tombstoned? Distinct from
    # vertex_alive(state, key): lease_unit's hop is DATA-derived (resolved
    # from a link mid-walk), so unknowable client-side and undeclarable --
    # only a live read can see it.
    if key == None:
        return False
    # read-posture: (e) one bounded read; the key is data-derived, resolved
    # from a kv.Links enumeration mid-walk.
    node = kv.Read(key)
    return node != None and not node.isDeleted

def lease_unit(lease_key):
    # A lease's location is its unit, one platform-written hop away
    # (appliesToUnit, required at CreateLeaseApplication). Here the lease
    # itself was resolved from the account's OWN heldFor link (post_entry),
    # so the whole chain account -> lease -> unit is data-derived and none of
    # it can be pre-declared or forged from the payload.
    # A leaseapp carries exactly one LIVE appliesToUnit link, but
    # ReassignLeaseUnit (lease-signing) repoints it (tombstone old, create
    # new), and a page can hold the tombstone before the live link, so this
    # pages until it finds one rather than trusting the first page.
    cursor = None
    unit = None
    for _page in range(MAX_LIVE_LINK_PAGES):
        # read-posture: (e) relation=appliesToUnit epoch=none -- bounded,
        # never a keyspace scan.
        page, cursor = kv.Links(lease_key, "appliesToUnit", "out", cursor, LIVE_LINK_PAGE_LIMIT)
        for lk in page:
            if not lk.isDeleted:
                unit = lk.targetVertex
        if unit != None or cursor == None:
            break
    if not vertex_live(unit):
        return None
    return unit

def period_index(valid_from, due):
    # The index k of the calendar-month period of a termed clause that a
    # recorded due date belongs to: the LARGEST k >= 0 with
    # rfc3339_add_months(valid_from, k) <= due. Anniversaries are always
    # computed from valid_from (never by iterating +1 month), so Jan 31 ->
    # Feb 28 -> Mar 31 never drifts. year/month arithmetic on the canonical
    # RFC3339 strings gives the candidate; day-of-month clamping can put the
    # candidate one step off in either direction, so it is corrected once.
    if due == None or due < valid_from:
        return 0
    k = (int(due[0:4]) - int(valid_from[0:4])) * 12 + (int(due[5:7]) - int(valid_from[5:7]))
    if k < 0:
        k = 0
    if time.rfc3339_add_months(valid_from, k) > due:
        k = k - 1
    elif time.rfc3339_add_months(valid_from, k + 1) <= due:
        k = k + 1
    if k < 0:
        k = 0
    return k

def self_scope_standing(op, acct_key):
    # The self-scope ownership proof shared by post_entry (DebitAccount is
    # never self-scoped, so only the two person-facing entry ops actually
    # reach it), RecordDepositDeduction and PayOutBalance: which of the two
    # populations a self-scoped submit's authContextTarget stands behind,
    # off the account's OWN heldFor->leaseapp topology, never the payload.
    # Returns (lease_key, standing) with standing in {"resident","landlord"},
    # or (None, None) when authContextTarget is absent (a scope=any
    # operator submit — no self-scope claim at all, so no proof runs). Every
    # caller narrows what the claimed standing may then DO differently (a
    # resident may only credit and only up to what is owed; a landlord's
    # RecordDepositDeduction/PayOutBalance refuse a resident outright) — but
    # the proof itself, and its resident-first order, is identical for all
    # three, since each binds the SAME claimed identity to the SAME
    # account-owned lease.
    #
    # authcontext-target: (selector) a branch selector, not a confinement
    # exemption -- so it reads the raw target (did the caller declare a self
    # target at all) rather than authTargetValidated. Safe because presence
    # only pushes the caller onto the STRICTER branch below (the ownership
    # proofs), never grants anything a scope=any submit would not already.
    if op.authContextTarget == "":
        return None, None
    # authcontext-target: (ownership) the value derives an identity whose
    # standing behind the account's own lease is then proven by a link
    # read below -- applicationFor (resident) or manages on the lease's
    # unit (landlord); a forged target only fails closed. The lease is
    # recovered from the account's OWN heldFor topology, never the
    # payload, so a forged claim only fails closed.
    _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
    # read-posture: (e) relation=heldFor epoch=none -- an account carries
    # exactly one heldFor link, so this is never a keyspace scan. A page of
    # one is exact here, unlike the LIVE_LINK paging appliesToUnit needs:
    # heldFor is written once at LoftspaceCreateAccount and never
    # repointed or tombstoned, so no tombstone can sort ahead of it.
    held_for_page, _ = kv.Links(acct_key, "heldFor", "out", None, 1)
    lease_key = None
    for lk in held_for_page:
        if not lk.isDeleted:
            lease_key = lk.targetVertex
    if lease_key == None:
        fail("AuthDenied: account " + acct_key + " carries no live lease")
    # The lease VERTEX itself: WithdrawLeaseApplication tombstones the
    # leaseapp without cascading to its links, so applicationFor and
    # appliesToUnit dangle live off a dead lease -- neither proof below
    # may transit one (lease-signing's leaseapp_unit live-checks the
    # application first for the same reason).
    # read-posture: (e) per-candidate follow-up read off the heldFor
    # enumeration above (data-derived key, via vertex_live).
    if not vertex_live(lease_key):
        fail("AuthDenied: account " + acct_key + " carries no live lease")
    _, lease_id = parts_of(lease_key, "heldFor target", "leaseapp")
    # read-posture: (e) per-candidate follow-up read off the enumeration
    # above -- the lease id is data-derived, unknowable client-side.
    application_for = kv.Read("lnk.leaseapp." + lease_id + ".applicationFor.identity." + target_identity_id)
    if application_for != None and not application_for.isDeleted:
        # RESIDENT.
        return lease_key, "resident"
    # LANDLORD: the lease's unit resolves from the lease's own
    # appliesToUnit link (paged, live-checked), and the caller must
    # manage it.
    unit_key = lease_unit(lease_key)
    manages = None
    if unit_key != None:
        _, unit_id = parts_of(unit_key, "unit", "unit")
        # read-posture: (e) per-candidate follow-up read off the
        # appliesToUnit enumeration in lease_unit (data-derived key --
        # the unit is not knowable until the lease's own link
        # resolves, so it cannot be pre-declared).
        manages = kv.Read("lnk.identity." + target_identity_id + ".manages.unit." + unit_id)
    if manages == None or manages.isDeleted:
        # The unit key is deliberately NOT named: the caller reached
        # here with an account key it already holds, and echoing the
        # unit that account's lease sits on would turn a denial into
        # a lookup for a resource it does not own.
        fail("AuthDenied: " + op.actor + " neither holds nor manages the lease this account is held for; a self-scoped entry is the resident's payment or the landlord's charge or payment on a unit they manage")
    return lease_key, "landlord"

def account_balance_cents(acct_key):
    # The account's own outstanding balance (positive = owed, negative = a
    # credit balance), recomputed from its OWN postedTo transaction history —
    # never trusted from the payload — paginated + bounded exactly like the
    # workplace-confinement walks in this file's account DDL. Shared by
    # post_entry's resident self-credit cap and PayOutBalance's payout
    # figure: both need the SAME trustworthy sum, deductions excluded (a
    # deduction is neither a debit nor a credit — see the type test below —
    # so it moves custody without moving what is owed). Returns
    # (owed_cents, budget_exhausted); an exhausted budget is the caller's own
    # fail-closed signal, never a partial sum.
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
    return owed_cents, budget_exhausted

def post_entry(state, op, entry_type, event_class, allow_clause_ref):
    p = op.payload
    acct_key = required_string(p, "accountKey")
    _, acct_id = parts_of(acct_key, "accountKey", "account")

    if not vertex_alive(state, acct_key):
        fail("UnknownAccount: " + acct_key)

    amount_cents = require_number(p, "amountCents")
    if amount_cents <= 0:
        fail("InvalidArgument: amountCents: required positive number")
    memo = optional_string(p, "memo")

    # Self-scoped ownership + amount trust: the mere PRESENCE of
    # authContextTarget selects this branch, same idiom as cafe-domain's
    # Charge/Settle -- it does not change what grant actually authorized the
    # op, it only ever narrows behavior: a scope=any holder (operator) whose
    # client sends a target is pushed onto the proofs below and confined by
    # them to the leases it holds or manages -- narrower, never wider -- and
    # the shipped FE (loftspace-app's landlordSubmit) attaches one only from
    # the landlord console. Two populations hold the consumer scope=self grants
    # (permissions.go) and are told apart from the account's OWN topology,
    # never the payload: the RESIDENT (the lease's applicationFor link) may
    # credit only, capped at what is owed; the LANDLORD (a manages link to
    # the unit the lease appliesToUnit) may debit and credit, uncapped --
    # the landlord is the creditor, so the amount is their own receivable.
    # The resident proof answers first: a landlord who tenants their own
    # unit is a resident.
    # authcontext-target: (selector) a branch selector, not a confinement
    # exemption -- so it reads the raw target (did the caller declare a self
    # target at all) rather than authTargetValidated. Safe because presence
    # only pushes the caller onto the STRICTER branch below (the ownership
    # proofs, and the resident's amount proof), never grants anything a
    # scope=any submit would not already.
    # workplace-exempt: (ownership-bound) self_scope_standing IS the
    # ownership proof for both consumer scope=self grants (permissions.go):
    # it resolves the account's own heldFor lease and requires the claimed
    # target to hold either the lease's applicationFor link (resident) or a
    # manages link on the lease's own appliesToUnit unit (landlord), so the
    # validated self path never reaches a write unconfined. DebitAccount
    # declares no self grant and no task is ever minted forOperation it, so
    # only the two person-facing entry ops (LoftspaceRecordCharge,
    # CreditAccount) can ever carry a validated target here.
    _, standing = self_scope_standing(op, acct_key)
    if standing == "resident":
        # RESIDENT: credit only, never a charge on their own lease.
        if entry_type != "credit":
            fail("AuthDenied: a resident may only credit (pay down) their own account, not charge it")

        # Amount trust: nothing on this platform verifies a self-submitted
        # payment actually happened (no payment-rail integration -- out of
        # scope for a reference vertical, package doc), so an unbounded
        # self-credit would let a resident forgive their own debt for free.
        # The outstanding balance is recomputed from the account's OWN
        # postedTo transaction history (never trusted from the payload) via
        # account_balance_cents (paginated + bounded exactly like the
        # workplace-confinement walks in this file's account DDL,
        # worksAt_covers): an account whose history exhausts the page budget
        # fails closed (denies) rather than trusts a partial sum. A
        # self-credit may never exceed what is actually owed.
        owed_cents, budget_exhausted = account_balance_cents(acct_key)
        if budget_exhausted:
            fail("AuthDenied: could not verify account " + acct_key + "'s balance (too much transaction history)")
        if owed_cents <= 0:
            fail("NoBalanceToPay: account " + acct_key + " has no outstanding balance to pay")
        if amount_cents > owed_cents:
            fail("PaymentExceedsBalance: amountCents exceeds account " + acct_key + "'s outstanding balance of " + str(owed_cents))
    # LANDLORD standing needs no further proof here: self_scope_standing
    # already confirmed the manages link before returning it. Both
    # directions are allowed and neither is capped -- the landlord is the
    # creditor.

    # clauseRef (DebitAccount only — the semantic-contracts Executable Paper
    # consumer, Contract #10 §10.8): the clause this charge is authorized by.
    # A clause playbook dispatch always declares row.clauseKey in Reads, so
    # the clause is hydrated here; a plain human-submitted DebitAccount omits
    # it entirely (nothing below runs).
    clause_key = None
    clause_id = None
    clause_period = None
    if allow_clause_ref:
        clause_key = optional_string(p, "clauseRef")
        if clause_key != None:
            _, clause_id = parts_of(clause_key, "clauseRef", "clause")
            if not vertex_alive(state, clause_key):
                fail("UnknownClause: " + clause_key)

            # amountCents provenance: a clause-authorized charge is money
            # that never self-heals once posted (append-only ledger), so it
            # must be DERIVED from the clause's own .terms — never trusted
            # verbatim from a caller/Weaver-copied number, which could
            # reflect a stale or torn read of whatever projection dispatched
            # it. The clauseSatisfaction playbook (semantic-contracts
            # targets.go) declares clauseRef + ".terms" in Reads, so it is
            # already hydrated here alongside the clause root.
            terms_key = clause_key + ".terms"
            if not vertex_alive(state, terms_key):
                fail("InvalidState: clause " + clause_key + " has no live .terms aspect")
            clause_amount = state[terms_key].data.get("amountCents")
            if clause_amount == None:
                fail("InvalidArgument: clauseRef: clause " + clause_key + " carries no fixed amountCents (a judgment clause has none)")
            if clause_amount != amount_cents:
                fail("AmountMismatch: payload amountCents disagrees with clause " + clause_key + "'s authoritative amountCents")
            amount_cents = clause_amount

            # period: the clauseSatisfaction playbook always templates
            # row.period alongside clauseRef, so a Weaver-dispatched charge
            # always carries it; a hand-submitted clauseRef with no period
            # falls through to the one-time-completion path.
            clause_period = optional_string(p, "period")

            # The clause's term (validFrom/validUntil on .terms, both or
            # neither) and its recorded due date (.status.chargeValidUntil).
            # Read from the clause's own record, like amountCents above,
            # never from the payload. CreateClause writes .status
            # unconditionally, so a termed clause whose .status is not in
            # state was dispatched without declaring it (the playbook lists
            # it as an OptionalRead): a due date read from nothing would
            # rewind the clause to its first period and re-bill every period
            # since, so that fails closed. An untermed clause never reads
            # the due date and keeps accepting the bare declaration.
            clause_valid_from = state[terms_key].data.get("validFrom")
            clause_valid_until = state[terms_key].data.get("validUntil")
            status_key = clause_key + ".status"
            clause_due = None
            if status_key in state and vertex_alive(state, status_key):
                clause_due = state[status_key].data.get("chargeValidUntil")
            elif clause_valid_from != None:
                fail("InvalidState: clause " + clause_key + "'s .status was not hydrated; a termed clause's charge must declare it in optionalReads")

    tx_id = nanoid.new()
    tx_key = "vtx.transaction." + tx_id
    posted_at = time.rfc3339_utc(op.submittedAt)

    # The period this charge bills, and the clause's next due date. Which
    # instant is stamped depends on whether the clause carries a term.
    # UNTERMED: postedAt + the recurring window, the legacy cadence. TERMED:
    # the due dates walk the calendar-month anniversary grid from validFrom —
    # this charge bills the period whose start is the recorded due date (the
    # lens opened the gap because a recorded lapse reached it), so the next
    # due is the anniversary after it, computed from validFrom each time so
    # the day-of-month never drifts. A first charge (no recorded due, or one
    # before validFrom) bills period 0. A lapse whose period would start at
    # or after validUntil bills nothing: the lens never opens that gap once
    # the due date lies on the grid, so this is the fail-closed backstop.
    # The billed period's end is capped at validUntil — the term is
    # exclusive there, so the final period is [its anniversary, validUntil).
    period_start = None
    period_end = None
    charge_valid_until = None
    term_exhausted = False
    if clause_key != None:
        if clause_valid_from != None:
            k = period_index(clause_valid_from, clause_due)
            period_start = time.rfc3339_add_months(clause_valid_from, k)
            if clause_valid_until != None and period_start >= clause_valid_until:
                fail("TermExhausted: clause " + clause_key + "'s next period starts at " + period_start + ", at or after its validUntil " + clause_valid_until)
            charge_valid_until = time.rfc3339_add_months(clause_valid_from, k + 1)
            period_end = charge_valid_until
            if clause_valid_until != None and charge_valid_until >= clause_valid_until:
                term_exhausted = True
                period_end = clause_valid_until
        else:
            charge_valid_until = time.rfc3339_add(posted_at, %q)
            if clause_period == "monthly":
                period_start = posted_at
                period_end = charge_valid_until

    entry_data = {"type": entry_type, "amountCents": amount_cents, "postedAt": posted_at}
    if memo != None:
        entry_data["memo"] = memo
    # A recurring charge records the period it covers and the date it fell
    # due ON THE ENTRY — the statement states "rent for Sep 6 – Oct 6, due
    # Sep 6" from the row itself, never by re-deriving the grid from the
    # clause at read time. The due date is the period's own start: a termed
    # clause's validFrom is its first period's due date and every later
    # period falls due on its anniversary; an untermed monthly clause is due
    # at its recorded lapse, or when it posts if none is recorded. A
    # one-time charge covers no period and stamps nothing.
    if period_start != None:
        entry_data["periodStart"] = period_start
        entry_data["periodEnd"] = period_end
        # An untermed clause's recorded due (the lapse the lens opened the gap
        # at) is the date it fell due when it is hydrated and already reached;
        # its period still runs from the posting, the legacy cadence.
        due_at = period_start
        if clause_valid_from == None and clause_due != None and clause_due <= posted_at:
            due_at = clause_due
        entry_data["dueAt"] = due_at

    # postedTo: the transaction (later-arriving) is the source, the
    # pre-existing account is the target (Contract #1 §1.1). Reads as
    # "this transaction posted to this account."
    posted_to_lnk = "lnk.transaction." + tx_id + ".postedTo.account." + acct_id

    # Root data minimal (D5): {} on root. The charge/payment fact is the
    # .entry aspect; the account root is otherwise untouched (append-only
    # ledger) — EXCEPT on the resident's own capped self-credit, where a bare
    # update of the account root's own unchanged data is the CAS anchor two
    # concurrent self-credits need: each mints an independent transaction
    # (a fresh nanoid, never colliding with the other), so without a shared
    # conditioned key both could compute the SAME owed_cents from the same
    # stale postedTo read and both land, each individually capped but jointly
    # exceeding what is owed — the exact race PayOutBalance later makes
    # cashable. The bare update (make_vtx_update) is auto-conditioned on the
    # revision this op's own declared read of acct_key hydrated (Contract #3
    # §3.2), so the loser re-hydrates, re-executes account_balance_cents
    # against the winner's now-posted credit, and is capped or refused on the
    # fresh total instead of landing independently. The landlord/operator
    # paths are uncapped and stay untouched — nothing they compute depends on
    # a stale read racing another writer the same way.
    mutations = [
        make_vtx(tx_key, "transaction", {}),
        make_aspect(tx_key, "entry", "transactionEntry", entry_data),
        make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
    ]
    if standing == "resident":
        mutations.append(make_vtx_update(acct_key, "account", state[acct_key].data))
    events = [{"class": event_class,
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "amountCents": amount_cents}}]

    if clause_key != None:
        # authorizedBy: the transaction (later-arriving) is the source, the
        # pre-existing clause is the target (Contract #1 §1.1) — the "why was
        # I charged this?" chain of custody back to the authorizing clause.
        authorized_by_lnk = "lnk.transaction." + tx_id + ".authorizedBy.clause." + clause_id
        mutations.append(make_link(authorized_by_lnk, tx_key, clause_key, "authorizedBy", "authorizedBy", {}))

        # chargeValidUntil is stamped UNCONDITIONALLY, regardless of which
        # branch below fires. .terms.data.period exists but is deliberately
        # not read here (only amountCents and the term are, above, for
        # provenance) — clause_period stays a caller-supplied signal, not a
        # verified one; the unconditional stamp below already closes the
        # dangerous mismatch direction for free (see next paragraph). A
        # hand-submitted DebitAccount (this is an ordinary operator-granted
        # op, not Weaver-exclusive) could in principle pass a period that
        # disagrees with the clause's real archetype. Always stamping
        # chargeValidUntil closes the dangerous direction of that mismatch:
        # the clauseSatisfaction lens's monthly gate (lenses.go) reads ONLY
        # chargeValidUntil, never the state field, so a genuinely-monthly
        # clause re-arms correctly even if clause_period was wrong/omitted
        # here — the alternative (never stamping it) would leave such a
        # clause permanently violating and Weaver re-dispatching
        # indefinitely. The mirror-image mismatch (a genuinely-oneTime
        # clause stamped as if monthly) is harmless: the oneTime gate is
        # chargeCount/authorizedBy-link-driven and never reads
        # chargeValidUntil at all.
        #
        # Which instant is stamped (charge_valid_until, computed above with
        # the billed period) depends on whether the clause carries a term.
        if clause_period == "monthly" and not term_exhausted:
            # Recurring clause: re-arm chargeValidUntil, never complete. This
            # IS the clauseSatisfaction lens's convergence gate for a monthly
            # clause (mirrors lease-signing's bgcheck-freshness validUntil
            # pattern) — unlike the one-time case below, this write is
            # load-bearing, not just audit.
            mutations.append({"op": "update", "key": clause_key + ".status",
                               "document": {"class": "clauseStatus", "isDeleted": False,
                                            "vertexKey": clause_key, "localName": "status",
                                            "data": {"state": "active", "chargeValidUntil": charge_valid_until}}})
        else:
            # Fixed/one-time clause bookkeeping, and a termed monthly clause
            # whose final period this charge bills: mark it completed
            # (audit/display only — the clauseSatisfaction lens's convergence
            # gate is the authorizedBy link for a oneTime clause and the
            # due-date-vs-validUntil comparison for a termed one, never this
            # status — see the design's R3). chargeValidUntil rides along
            # here too (see the note above): for the termed case it is the
            # instant the term ends, which the lens reads as "no period left".
            mutations.append({"op": "update", "key": clause_key + ".status",
                               "document": {"class": "clauseStatus", "isDeleted": False,
                                            "vertexKey": clause_key, "localName": "status",
                                            "data": {"state": "completed", "completedAt": posted_at,
                                                     "chargeValidUntil": charge_valid_until}}})

    mutations += arrears_stale_mark(acct_key)

    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

def return_deposit(state, op):
    # The security deposit comes back as a credit on the lease's account once
    # the tenancy has ended: an ordinary credit that nets against whatever the
    # tenant still owes, whose remainder the statement reads as the refund
    # owed. Dispatched by leaseRentSettlement's missing_depositReturn
    # (packages/semantic-contracts, targets.go) with the lease, the charged
    # deposit clause the lens selected, and the lease's account. Every fact
    # below is read from the graph's own record, never trusted from the
    # payload: the amount and the purpose from the clause's .terms, whether
    # it was charged from its .status, the end from the lease's .tenancy,
    # and the clause's custody from its own deterministic links.
    p = op.payload
    lease_key = required_string(p, "leaseAppKey")
    _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")
    clause_key = required_string(p, "clauseKey")
    _, clause_id = parts_of(clause_key, "clauseKey", "clause")
    acct_key = required_string(p, "accountKey")
    _, acct_id = parts_of(acct_key, "accountKey", "account")

    if not vertex_alive(state, acct_key):
        fail("UnknownAccount: " + acct_key)
    if not vertex_alive(state, clause_key):
        fail("UnknownClause: " + clause_key)
    if not vertex_alive(state, lease_key):
        fail("UnknownLeaseApplication: " + lease_key)

    # Only a deposit is returned, and a deposit is ONE shape: the purpose
    # token CreateClause recorded (the same mark the dispatching lens selected
    # it by — a clause without it is refused whatever its prose says) on a
    # oneTime computational clause. A monthly clause reaches completed too,
    # on its final period after N charges, and a judgment clause charges
    # nothing: neither could be returned for what was collected. The amount
    # is the clause's own, as DebitAccount charged it.
    terms_key = clause_key + ".terms"
    if not vertex_alive(state, terms_key):
        fail("UnknownClause: " + clause_key + " has no live .terms aspect")
    terms = state[terms_key].data
    if terms.get("purpose") != "deposit":
        fail("NotADeposit: " + clause_key + " carries no purpose=deposit; only a security deposit clause is returned")
    if terms.get("period") != "oneTime" or terms.get("kind") != "computational":
        fail("NotADeposit: " + clause_key + " is not a oneTime computational clause; a deposit is charged once and returned once")
    amount_cents = terms.get("amountCents")
    if amount_cents == None or amount_cents <= 0:
        fail("NotADeposit: " + clause_key + " carries no positive amountCents to return")

    # CreateClause writes .status unconditionally, so its absence here is a
    # missing hydration or a torn clause, never a fresh one — fail closed
    # rather than treat "no state" as "not yet charged".
    status_key = clause_key + ".status"
    if not (status_key in state and vertex_alive(state, status_key)):
        fail("InvalidState: " + clause_key + " has no live .status aspect")
    status_doc = state[status_key]
    status_state = status_doc.data.get("state")

    # The recorded end of the tenancy (EndTenancy's endedAt) is what a return
    # rides — never the notice's intention or the term's scheduled end.
    tenancy_key = lease_key + ".tenancy"
    ended_at = None
    if tenancy_key in state and vertex_alive(state, tenancy_key):
        ended_at = state[tenancy_key].data.get("endedAt")
    if ended_at == None:
        fail("TenancyNotEnded: " + lease_key + " records no endedAt; the deposit is returned once the tenancy has ended")

    # Custody: the clause must charge THIS account and govern THIS lease, off
    # its own deterministic link keys (mint_clause writes both once, with the
    # clause as source). Both are hydrated by derive_reads below; an absent
    # or tombstoned link is a mismatch between the payload and the clause's
    # record, so the credit lands nowhere it does not belong.
    charges_lnk = "lnk.clause." + clause_id + ".chargesTo.account." + acct_id
    if not vertex_alive(state, charges_lnk):
        fail("ClauseAccountMismatch: " + clause_key + " does not charge " + acct_key)
    governs_lnk = "lnk.clause." + clause_id + ".governs.leaseapp." + lease_id
    if not vertex_alive(state, governs_lnk):
        fail("ClauseLeaseMismatch: " + clause_key + " does not govern " + lease_key)

    # Only a well-addressed submit reaches the state checks: a return that
    # names the wrong lease or account is refused above even when the clause
    # is already returned — a mis-addressed hand submit must never read as
    # "done".
    if status_state == "returned":
        # Idempotent no-op (the EndTenancy shape: empty mutations, no event,
        # no primaryKey): the deposit is already returned. The lens's
        # missing_depositReturn drops a returned clause from candidacy, so a
        # dispatch that still names it is a race with an earlier return, not
        # a second refund.
        return {"mutations": [], "events": [], "response": {}}
    if status_state != "completed":
        # completed is the state DebitAccount's one-time charge leaves; an
        # active clause is minted but not yet billed, and returning an
        # uncharged deposit would credit money never collected.
        fail("DepositNotCharged: " + clause_key + " is " + str(status_state) + ", not completed; a deposit is returned only once DebitAccount has charged it")

    # The NET returned to the tenant: the clause's own full amount, less
    # whatever RecordDepositDeduction has already taken off it — the running
    # total on THIS package's own .deductions aspect (ddls.go's
    # depositDeductions; absent = never deducted). Zero net (fully deducted)
    # posts no transaction at all: a zero-amount entry is not a transaction,
    # so the clause still moves to returned but nothing is minted or linked
    # (Decision 4).
    deducted_cents = 0
    deductions_key = clause_key + ".deductions"
    deductions_doc = None
    if deductions_key in state and vertex_alive(state, deductions_key):
        deductions_doc = state[deductions_key]
        deducted_cents = deductions_doc.data.get("totalCents")
        if deducted_cents == None:
            deducted_cents = 0
    net_cents = amount_cents - deducted_cents
    if net_cents < 0:
        # A recorded deduction total exceeding the clause's own amount is a
        # torn record (RecordDepositDeduction's own cap refuses this at
        # write time) — fail closed rather than credit a negative amount.
        fail("InvalidState: " + clause_key + "'s recorded deductions (" + str(deducted_cents) + ") exceed its own amountCents (" + str(amount_cents) + ")")

    posted_at = time.rfc3339_utc(op.submittedAt)

    # The clause's .status moves to returned, keeping every field DebitAccount
    # left (completedAt, chargeValidUntil) and pinned to the revision the
    # dispatch hydrated: a concurrent writer of .status must conflict rather
    # than be overwritten by a return computed from a stale state, and a
    # second return racing this one conflicts here instead of crediting
    # twice. The EXPLICIT pin keeps this write itself out of the defaulted
    # set the §3.2 re-hydrate retry replays (commit_path.go's
    # applyHydratedRevisions skips a mutation that carries its own
    # expectedRevision) — a loser is never replayed as a second credit. The
    # .deductions write below IS retry-eligible, so a return-vs-return loser
    # re-hydrates, re-executes, and lands on the returned no-op branch above;
    # a loser whose only conflict is this pin is rejected and Weaver
    # re-evaluates the row.
    status_data = {}
    for k, v in status_doc.data.items():
        status_data[k] = v
    status_data["state"] = "returned"
    status_data["returnedAt"] = posted_at

    # .deductions is the SERIALIZATION ANCHOR against RecordDepositDeduction:
    # that op reads .status and writes .deductions, this one reads
    # .deductions and writes .status, and two writers conditioned on keys
    # the OTHER never touches never serialize — a boundary-time deduction
    # would land on an already-returned clause, or a return would compute
    # its net from a total a concurrent deduction was about to change.
    # Writing .deductions here too — a BARE update carrying the
    # hydrated data unchanged when present (Contract #3 §3.2: auto-
    # conditioned on the revision this op's own derive_reads hydrated, so a
    # deduction that lands first conflicts this write and the platform
    # re-hydrates/re-executes/re-commits with the fresh total), or a CREATE
    # of {totalCents: 0, count: 0, lastRecordedAt: posted_at} when absent
    # (Contract #2 §2.5's absentConditionedCreates: a create on a key step 4
    # observed as known-absent is retry-eligible the same way, so a
    # concurrent first deduction's own create conflicts and re-hydrates
    # rather than silently coexisting) — gives the two ops a shared key
    # every race between them must serialize through.
    if deductions_doc != None:
        deductions_mutation = make_aspect_update(clause_key, "deductions", "depositDeductions", deductions_doc.data)
    else:
        deductions_mutation = make_aspect(clause_key, "deductions", "depositDeductions",
                                           {"totalCents": 0, "count": 0, "lastRecordedAt": posted_at})

    mutations = [
        {"op": "update", "key": status_key, "expectedRevision": status_doc.revision,
         "document": {"class": "clauseStatus", "isDeleted": False,
                      "vertexKey": clause_key, "localName": "status", "data": status_data}},
        deductions_mutation,
    ]
    response = {}
    tx_key = None
    if net_cents > 0:
        tx_id = nanoid.new()
        tx_key = "vtx.transaction." + tx_id
        entry_data = {"type": "credit", "amountCents": net_cents, "postedAt": posted_at,
                      "memo": "Security deposit returned"}

        # postedTo / authorizedBy: the transaction (later-arriving) is the
        # source of both (Contract #1 §1.1) — the same chain of custody
        # DebitAccount recorded for the charge, so the statement tells the
        # return from a payment by the clause it names.
        posted_to_lnk = "lnk.transaction." + tx_id + ".postedTo.account." + acct_id
        authorized_by_lnk = "lnk.transaction." + tx_id + ".authorizedBy.clause." + clause_id

        mutations += [
            make_vtx(tx_key, "transaction", {}),
            make_aspect(tx_key, "entry", "transactionEntry", entry_data),
            make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
            make_link(authorized_by_lnk, tx_key, clause_key, "authorizedBy", "authorizedBy", {}),
        ]
        # A credit moves the FIFO the arrears evaluation ages, exactly as
        # every post_entry credit does. A zero-net return posts no
        # transaction, so it moves no FIFO and needs no mark.
        mutations += arrears_stale_mark(acct_key)
        response = {"primaryKey": tx_key}

    event_data = {"accountKey": acct_key, "clauseKey": clause_key,
                  "leaseAppKey": lease_key, "amountCents": net_cents}
    if tx_key != None:
        event_data["transactionKey"] = tx_key
    events = [{"class": "loftspace.depositReturned", "data": event_data}]
    return {"mutations": mutations, "events": events, "response": response}

def record_deposit_deduction(state, op):
    # A landlord deduction taken from a charged, still-held security
    # deposit. It MOVES CUSTODY — what eventually comes back to the tenant —
    # never what the tenant OWES: the entry's own type ("deduction") is
    # neither "debit" nor "credit", so every balance reader that tests the
    # type explicitly (the arrears FIFO's arrears_entries, the resident
    # self-credit walk / account_balance_cents above) ignores it outright —
    # a damage deduction never ages into arrears and never blocks a
    # self-credit. Custody on the statement is charged − deducted − returned
    # (cmd/loftspace-app's computeDepositSummary). The running deducted total
    # lives on THIS PACKAGE'S OWN .deductions aspect on the clause (class
    # depositDeductions, ddls.go) — never on the clause's .status, which
    # semantic-contracts owns: PermittedCommands is a REAL commit-time gate
    # (step6_validate.go), not documentation, so writing a foreign package's
    # aspect class would need this op admitted there, reaching a script that
    # was never meant to authorize it (the S9 hazard). This op still READS
    # .status (the completed gate), it just never writes it.
    p = op.payload
    acct_key = required_string(p, "accountKey")
    _, acct_id = parts_of(acct_key, "accountKey", "account")
    clause_key = required_string(p, "clauseKey")
    _, clause_id = parts_of(clause_key, "clauseKey", "clause")
    amount_cents = require_number(p, "amountCents")
    if amount_cents <= 0:
        fail("InvalidArgument: amountCents: required positive number")
    reason = required_string(p, "reason")
    if len(reason) > 200:
        fail("InvalidArgument: reason: must be 200 characters or fewer")

    if not vertex_alive(state, acct_key):
        fail("UnknownAccount: " + acct_key)
    if not vertex_alive(state, clause_key):
        fail("UnknownClause: " + clause_key)

    # Who: the landlord's self-scope path (the account's own heldFor lease,
    # its appliesToUnit unit, the caller's manages link) or the operator with
    # no target. self_scope_standing's resident branch answers first and is
    # refused here — a deduction is the landlord's own act, never the
    # tenant's.
    # workplace-exempt: (ownership-bound) self_scope_standing IS the
    # ownership proof for this op's consumer scope=self grant
    # (permissions.go): a resident standing is refused outright below, and a
    # landlord standing already required the manages link on the lease's own
    # unit before returning here.
    _, standing = self_scope_standing(op, acct_key)
    if standing == "resident":
        fail("AuthDenied: a resident may not deduct from their own deposit")

    # Custody: the clause must charge THIS account, off its own deterministic
    # link key (mint_clause writes it once, with the clause as source).
    charges_lnk = "lnk.clause." + clause_id + ".chargesTo.account." + acct_id
    if not vertex_alive(state, charges_lnk):
        fail("ClauseAccountMismatch: " + clause_key + " does not charge " + acct_key)

    terms_key = clause_key + ".terms"
    if not vertex_alive(state, terms_key):
        fail("UnknownClause: " + clause_key + " has no live .terms aspect")
    terms = state[terms_key].data
    if terms.get("purpose") != "deposit" or terms.get("period") != "oneTime" or terms.get("kind") != "computational":
        fail("NotADeposit: " + clause_key + " is not a oneTime computational purpose=deposit clause")
    deposit_amount = terms.get("amountCents")
    if deposit_amount == None or deposit_amount <= 0:
        fail("NotADeposit: " + clause_key + " carries no positive amountCents to deduct from")

    status_key = clause_key + ".status"
    if not (status_key in state and vertex_alive(state, status_key)):
        fail("InvalidState: " + clause_key + " has no live .status aspect")
    status_doc = state[status_key]
    status_state = status_doc.data.get("state")
    # completed = charged and still held; active = not charged yet (nothing
    # to deduct from); returned = the deposit is gone.
    if status_state != "completed":
        fail("DepositNotHeld: " + clause_key + " is " + str(status_state) + ", not completed; a deduction is only taken from a charged, still-held deposit")

    # The running total lives on THIS package's own .deductions aspect
    # (depositDeductions, ddls.go) — absent on a clause never deducted from.
    deductions_key = clause_key + ".deductions"
    deductions_doc = None
    if deductions_key in state and vertex_alive(state, deductions_key):
        deductions_doc = state[deductions_key]
    total_so_far = 0
    count_so_far = 0
    if deductions_doc != None:
        total_so_far = deductions_doc.data.get("totalCents")
        if total_so_far == None:
            total_so_far = 0
        count_so_far = deductions_doc.data.get("count")
        if count_so_far == None:
            count_so_far = 0
    if total_so_far + amount_cents > deposit_amount:
        fail("DeductionExceedsDeposit: " + clause_key + "'s deposit of " + str(deposit_amount) +
             " already carries " + str(total_so_far) + " deducted; " + str(amount_cents) + " more would exceed it")

    tx_id = nanoid.new()
    tx_key = "vtx.transaction." + tx_id
    posted_at = time.rfc3339_utc(op.submittedAt)
    entry_data = {"type": "deduction", "amountCents": amount_cents, "postedAt": posted_at, "memo": reason}

    # postedTo / authorizedBy: the transaction (later-arriving) is the source
    # of both (Contract #1 §1.1) — the same chain of custody DebitAccount and
    # ReturnDeposit record.
    posted_to_lnk = "lnk.transaction." + tx_id + ".postedTo.account." + acct_id
    authorized_by_lnk = "lnk.transaction." + tx_id + ".authorizedBy.clause." + clause_id

    deductions_data = {"totalCents": total_so_far + amount_cents, "count": count_so_far + 1, "lastRecordedAt": posted_at}
    if deductions_doc == None:
        # First deduction on this clause: CREATE. make_aspect's own create-only
        # write conflicts a genuine race instead of silently overwriting it.
        deductions_mutation = make_aspect(clause_key, "deductions", "depositDeductions", deductions_data)
    else:
        # A later deduction: a BARE update (make_aspect_update), auto-
        # conditioned on the revision this op's own derive_reads hydrated
        # (.deductions is declared optionalReads, never expectedRevision-
        # pinned): the Contract #3 §3.2 re-hydrate retry replays it against
        # the latest revision on a conflict, so two concurrent deductions
        # both land and the running total stays exact.
        deductions_mutation = make_aspect_update(clause_key, "deductions", "depositDeductions", deductions_data)

    mutations = [
        make_vtx(tx_key, "transaction", {}),
        make_aspect(tx_key, "entry", "transactionEntry", entry_data),
        make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
        make_link(authorized_by_lnk, tx_key, clause_key, "authorizedBy", "authorizedBy", {}),
        deductions_mutation,
    ]
    # No .arrears stale mark: a deduction moves no FIFO at all (it is neither
    # a debit nor a credit — see arrears_entries' own type test), so the
    # replay checkpoint's "every debit and credit" aggregate stays exact
    # without one.
    events = [{"class": "loftspace.depositDeducted",
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "clauseKey": clause_key, "amountCents": amount_cents}}]
    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

def pay_out_balance(state, op):
    # Pays the account's WHOLE credit balance out to the tenant once the
    # tenancy has ended — the landlord's own act (or the operator's), never
    # the resident's, computed op-side from the account's own postedTo
    # history exactly as account_balance_cents computes what a resident
    # owes: never trusted from the payload, so there is nothing here to cap
    # or under/over-trust. A negative recomputed balance is a credit; paying
    # it out debits the account back to zero.
    p = op.payload
    acct_key = required_string(p, "accountKey")
    _, acct_id = parts_of(acct_key, "accountKey", "account")
    lease_key = required_string(p, "leaseAppKey")
    _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")

    if not vertex_alive(state, acct_key):
        fail("UnknownAccount: " + acct_key)
    if not vertex_alive(state, lease_key):
        fail("UnknownLeaseApplication: " + lease_key)

    # Who: the landlord's self-scope path or the operator with no target —
    # the same standing self_scope_standing proves for RecordDepositDeduction.
    # workplace-exempt: (ownership-bound) self_scope_standing IS the
    # ownership proof for this op's consumer scope=self grant
    # (permissions.go): a resident standing is refused outright below, and a
    # landlord standing already required the manages link on the lease's own
    # unit before returning here.
    _, standing = self_scope_standing(op, acct_key)
    if standing == "resident":
        fail("AuthDenied: a resident may not pay out their own account's balance")

    # Custody: the account must actually be held for THIS lease, off the
    # account's own deterministic heldFor link (the account is the source —
    # Contract #1 §1.1).
    held_for_lnk = "lnk.account." + acct_id + ".heldFor.leaseapp." + lease_id
    if not vertex_alive(state, held_for_lnk):
        fail("AccountLeaseMismatch: " + acct_key + " is not held for " + lease_key)

    tenancy_key = lease_key + ".tenancy"
    ended_at = None
    if tenancy_key in state and vertex_alive(state, tenancy_key):
        ended_at = state[tenancy_key].data.get("endedAt")
    if ended_at == None:
        fail("TenancyNotEnded: " + lease_key + " records no endedAt; a balance is paid out once the tenancy has ended")

    owed_cents, budget_exhausted = account_balance_cents(acct_key)
    if budget_exhausted:
        fail("HistoryTooLong: could not verify account " + acct_key + "'s balance (too much transaction history)")
    if owed_cents >= 0:
        fail("NoCreditBalance: account " + acct_key + " carries no credit balance to pay out")
    payout_cents = -owed_cents

    tx_id = nanoid.new()
    tx_key = "vtx.transaction." + tx_id
    posted_at = time.rfc3339_utc(op.submittedAt)
    # kind records this debit's PROVENANCE — no clause authorizes it, so the
    # statement labels it by this recorded fact, never by its memo (memo is
    # free text an operator could edit the wording of in a client; kind is
    # not).
    entry_data = {"type": "debit", "kind": "payout", "amountCents": payout_cents,
                  "postedAt": posted_at, "memo": "Balance paid out to the tenant"}
    posted_to_lnk = "lnk.transaction." + tx_id + ".postedTo.account." + acct_id

    # The account root's own bare update (unchanged data) is the CAS anchor:
    # two concurrent PayOutBalance submissions on a never-evaluated account
    # (no .arrears aspect to serialize through — arrears_stale_mark mints
    # nothing where absent) each mint an independent transaction and would
    # otherwise both land, each paying out the SAME computed owed_cents. The
    # bare update (make_vtx_update) is auto-conditioned on the revision this
    # op's own declared read of acct_key hydrated (Contract #3 §3.2), so the
    # loser re-hydrates, re-executes account_balance_cents against the
    # winner's now-posted payout debit, and finds NoCreditBalance instead of
    # paying the same credit out twice.
    mutations = [
        make_vtx(tx_key, "transaction", {}),
        make_aspect(tx_key, "entry", "transactionEntry", entry_data),
        make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
        make_vtx_update(acct_key, "account", state[acct_key].data),
    ]
    # A debit moves the FIFO the arrears evaluation ages, exactly as every
    # post_entry debit does.
    mutations += arrears_stale_mark(acct_key)

    events = [{"class": "loftspace.balancePaidOut",
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "leaseAppKey": lease_key, "amountCents": payout_cents}}]
    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

def derive_reads(op):
    # Contract #2 §2.5 class (g): the keys post_entry's .arrears write depends
    # on, returned server-side for EVERY dispatch of the three entry ops,
    # whatever the submitter declared — and ReturnDeposit's whole read set,
    # below. The write is a bare update that is
    # only auto-conditioned on the step-4 hydrated revision (Contract #3
    # §3.2) for a key that WAS hydrated — a submitter that omitted the
    # declaration would get a live read and an UNCONDITIONED update, so two
    # concurrent entries against one account could each carry the same prior
    # state and one mark would be lost. A guard a caller can switch off by
    # not mentioning it is not a guard, and contextHint is caller-supplied
    # and never enforced — hence this, the channel the platform owns. The
    # dispatchers' own static declarations (opmetas.go) stay: they document
    # the read set, this guarantees it.
    #
    # optionalReads, never reads: no account carries .arrears until an
    # evaluation has run on it, and a required read's absence is a
    # HydrationMiss that would block every entry against such an account.
    #
    # The account ROOT itself rides the same declaration too, for a distinct
    # reason: post_entry's vertex_alive(state, acct_key) decides
    # UnknownAccount by testing acct_key not in state, which cannot tell
    # "genuinely absent" from "never declared or derived" apart. Every
    # dispatcher's own static declaration already names the root too, but
    # that is a hint a caller may ignore, not an enforcement.
    #
    # The op argument is a struct -- op.operationType, op.actor, op.payload
    # (also a struct). No kv, no nanoid: both are fail-closed stubs in this
    # pass, and a derivation that reads state is a read, not a derivation.
    ot = op.operationType
    if ot == "ReturnDeposit":
        # ReturnDeposit's whole read set, derived from the three payload keys:
        # the account root and its .arrears (the same two as every entry op),
        # the clause root, its .terms, .status and .deductions (this
        # package's own running-total aspect, ddls.go), the lease root and
        # its .tenancy, and the two deterministic custody links — which no dispatcher can
        # template (a link key spans two payload fields), so this is the one
        # channel that hydrates them. All optionalReads, so the handler's own
        # refusals (UnknownAccount, UnknownClause, UnknownLeaseApplication,
        # TenancyNotEnded, ClauseAccountMismatch, ClauseLeaseMismatch) name
        # what is absent instead of an opaque hydration miss. The merge rule
        # (Contract #2 §2.5 class (g), internal/processor/derive_reads.go
        # mergeDerivedReads) is that a derivation never HARDENS the envelope's
        # disposition: a key the envelope already declared keeps the
        # envelope's own reads / optionalReads placement, so Weaver's required
        # declarations stay required and an undeclared submitter gets these
        # as optional.
        keys = []
        acct_key = optional_string(op.payload, "accountKey")
        clause_key = optional_string(op.payload, "clauseKey")
        lease_key = optional_string(op.payload, "leaseAppKey")
        has_acct = is_account_key(acct_key)
        has_clause = is_vertex_key(clause_key, "clause")
        has_lease = is_vertex_key(lease_key, "leaseapp")
        if has_acct:
            keys += [acct_key, acct_key + ".arrears"]
        if has_clause:
            keys += [clause_key, clause_key + ".terms", clause_key + ".status", clause_key + ".deductions"]
            clause_id = clause_key.split(".")[2]
            if has_acct:
                keys.append("lnk.clause." + clause_id + ".chargesTo.account." + acct_key.split(".")[2])
            if has_lease:
                keys.append("lnk.clause." + clause_id + ".governs.leaseapp." + lease_key.split(".")[2])
        if has_lease:
            keys += [lease_key, lease_key + ".tenancy"]
        if len(keys) == 0:
            return {}
        return {"optionalReads": keys}
    if ot == "RecordDepositDeduction":
        # The account root, the clause root, its .terms, .status and
        # .deductions (this package's own running-total aspect, ddls.go),
        # and the chargesTo custody link — the same shape as ReturnDeposit's
        # branch, minus the lease (this op never reads .tenancy).
        keys = []
        acct_key = optional_string(op.payload, "accountKey")
        clause_key = optional_string(op.payload, "clauseKey")
        has_acct = is_account_key(acct_key)
        has_clause = is_vertex_key(clause_key, "clause")
        if has_acct:
            keys.append(acct_key)
        if has_clause:
            keys += [clause_key, clause_key + ".terms", clause_key + ".status", clause_key + ".deductions"]
            clause_id = clause_key.split(".")[2]
            if has_acct:
                keys.append("lnk.clause." + clause_id + ".chargesTo.account." + acct_key.split(".")[2])
        if len(keys) == 0:
            return {}
        return {"optionalReads": keys}
    if ot == "PayOutBalance":
        # The account root and its .arrears (the payout is an ordinary
        # debit), the lease root and its .tenancy, and the deterministic
        # heldFor custody link — spanning both payload keys, so no
        # dispatcher can template it.
        keys = []
        acct_key = optional_string(op.payload, "accountKey")
        lease_key = optional_string(op.payload, "leaseAppKey")
        has_acct = is_account_key(acct_key)
        has_lease = is_vertex_key(lease_key, "leaseapp")
        if has_acct:
            keys += [acct_key, acct_key + ".arrears"]
        if has_lease:
            keys += [lease_key, lease_key + ".tenancy"]
        if has_acct and has_lease:
            keys.append("lnk.account." + acct_key.split(".")[2] + ".heldFor.leaseapp." + lease_key.split(".")[2])
        if len(keys) == 0:
            return {}
        return {"optionalReads": keys}
    if ot != "DebitAccount" and ot != "LoftspaceRecordCharge" and ot != "CreditAccount":
        return {}
    # optional_string, never required_string: a missing or malformed
    # accountKey derives nothing rather than faulting the pre-pass --
    # post_entry's own required_string/parts_of still raise the real
    # InvalidArgument.
    acct_key = optional_string(op.payload, "accountKey")
    if not is_account_key(acct_key):
        return {}
    return {"optionalReads": [acct_key, acct_key + ".arrears"]}

def execute(state, op):
    ot = op.operationType

    if ot == "DebitAccount":
        # workplace-exempt: (ownership-bound) DebitAccount declares one
        # scope=any grant, to [operator] (permissions.go) -- the orchestrated,
        # clause-authorized charge -- so post_entry's self-scoped branch is
        # unreachable on the grant side (step 3 denies scope=self outright);
        # were a target ever to reach it, that branch still fails closed for
        # a resident and demands the landlord's manages link otherwise.
        return post_entry(state, op, "debit", "account.debited", True)

    if ot == "LoftspaceRecordCharge":
        # workplace-exempt: (ownership-bound) post_entry proves ownership itself --
        # a self-scoped charge is allowed only on the landlord path, once the
        # account's heldFor lease's appliesToUnit unit carries a manages link
        # from op.authContextTarget; the resident path (applicationFor) refuses
        # a charge outright. Never clause-authorized (allow_clause_ref False).
        return post_entry(state, op, "debit", "account.debited", False)

    if ot == "CreditAccount":
        # workplace-exempt: (ownership-bound) post_entry proves ownership itself --
        # a self-scoped credit is allowed only once the account's heldFor lease's
        # applicationFor link resolves to op.authContextTarget (resident, capped
        # at the balance) or its appliesToUnit unit carries a manages link from
        # it (landlord, uncapped).
        return post_entry(state, op, "credit", "account.credited", False)

    if ot == "ReturnDeposit":
        # Operator-only (permissions.go: one scope=any grant, Weaver's
        # dispatch actor), no self grant, no task minted for it — so no
        # authContext target ever reaches this branch; the custody it proves
        # is the clause's own (chargesTo / governs), off the graph's record.
        return return_deposit(state, op)

    if ot == "RecordDepositDeduction":
        # workplace-exempt: (per-call-site) record_deposit_deduction's own
        # self_scope_standing call carries the discharge.
        return record_deposit_deduction(state, op)

    if ot == "PayOutBalance":
        # workplace-exempt: (per-call-site) pay_out_balance's own
        # self_scope_standing call carries the discharge.
        return pay_out_balance(state, op)

    fail("transaction DDL: unknown operationType: " + ot)
`, RecurringChargePeriod)
