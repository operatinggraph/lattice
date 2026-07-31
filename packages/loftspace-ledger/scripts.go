package loftspaceledger

import "fmt"

// recurringChargePeriod is the validity span DebitAccount stamps onto a
// period="monthly" clause's .status.chargeValidUntil as
// chargeValidUntil = postedAt + recurringChargePeriod (a Go duration string,
// time.ParseDuration form) — the Fire V3 recurring-clause analog of
// lease-signing's bgcheckFreshnessWindow (freshness_window.go). Baked into
// transactionDDLScript at package-init time via fmt.Sprintf, same pattern as
// leaseServiceReplyDDLScript.
const recurringChargePeriod = "720h"

// accountDDLScript handles CreateAccount. The account gets its OWN
// independently-minted NanoID — vertex NanoIDs are unique identifiers across
// all of Core KV, never reused across vertex types, even deliberately (a
// prior revision minted the account under the lease's own bare NanoID;
// internal/refractor/adjacency keys strictly by bare NodeID with no type
// qualifier, so that reuse silently merged the account's and the lease's
// adjacency edges under one key and corrupted graph traversal for both — see
// adjacency-shared-nanoid-collision-design.md). "One account per lease" is
// instead enforced by a deterministic CREATE-ONLY guard aspect on the
// PRE-EXISTING leaseapp (leaseAppKey + ".ledgerAccount") — a second
// CreateAccount for the same lease conflicts on that already-existing aspect
// key, the same "let the key shape be the uniqueness guard" idiom, just
// anchored on the pre-existing parent instead of a freshly-minted sibling.
// Root data stays {} on the account (D5): the balance is derived by the
// ledgerHistory lens, never stored here.
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

        # One account per lease, guarded by a deterministic aspect on the
        # PRE-EXISTING leaseapp (not the account — the account's own id is
        # independent and unknown until minted below). Only meaningful when
        # the caller declared the guard key in contextHint.reads (a
        # repeat/racing caller checking before it retries); the FIRST
        # CreateAccount for a lease declares only leaseAppKey (the guard
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
// ledgerAccountGuard — written by CreateAccount's own op handler, never
// dispatched as an operation in its own right.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

// transactionDDLScript handles DebitAccount and CreditAccount. Each mints a
// fresh transaction vertex + a .entry aspect + the postedTo link to the
// account. The ledger is append-only: no aspect on the account is read or
// mutated here, so concurrent debits/credits against the same account never
// race a read-modify-write — the balance is derived by the ledgerHistory lens
// summing entries.
//
// The clauseValidUntil computation (Fire V3) is pure arithmetic on the op's
// own posted_at (time.rfc3339_add), so post_entry stays read-free for that
// leg exactly as before.
var transactionDDLScript = fmt.Sprintf(`
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

            # period (Fire V3): the clauseSatisfaction playbook always
            # templates row.period alongside clauseRef, so a Weaver-dispatched
            # charge always carries it; a hand-submitted clauseRef with no
            # period falls through to the Fire V1/V2 one-time-completion path.
            clause_period = optional_string(p, "period")

    tx_id = nanoid.new()
    tx_key = "vtx.transaction." + tx_id
    posted_at = time.rfc3339_utc(op.submittedAt)

    entry_data = {"type": entry_type, "amountCents": amount_cents, "postedAt": posted_at}
    if memo != None:
        entry_data["memo"] = memo

    # postedTo: the transaction (later-arriving) is the source, the
    # pre-existing account is the target (Contract #1 §1.1). Reads as
    # "this transaction posted to this account."
    posted_to_lnk = "lnk.transaction." + tx_id + ".postedTo.account." + acct_id

    # Root data minimal (D5): {} on root. The charge/payment fact is the
    # .entry aspect; the account itself is untouched (append-only ledger).
    mutations = [
        make_vtx(tx_key, "transaction", {}),
        make_aspect(tx_key, "entry", "transactionEntry", entry_data),
        make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
    ]
    events = [{"class": event_class,
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "amountCents": amount_cents}}]

    if clause_key != None:
        # authorizedBy: the transaction (later-arriving) is the source, the
        # pre-existing clause is the target (Contract #1 §1.1) — the "why was
        # I charged this?" chain of custody back to the authorizing clause.
        authorized_by_lnk = "lnk.transaction." + tx_id + ".authorizedBy.clause." + clause_id
        mutations.append(make_link(authorized_by_lnk, tx_key, clause_key, "authorizedBy", "authorizedBy", {}))

        # clauseValidUntil is stamped UNCONDITIONALLY, regardless of which
        # branch below fires. .terms.data.period exists but is deliberately
        # not read here (only amountCents is, above, for money provenance) —
        # clause_period stays a caller-supplied signal, not a verified one;
        # cross-checking it is out of scope for the amountCents fix and the
        # unconditional stamp below already closes the dangerous mismatch
        # direction for free (see next paragraph). A hand-submitted
        # DebitAccount (this is an ordinary operator-granted op, not
        # Weaver-exclusive) could in principle pass a period that disagrees
        # with the clause's real archetype. Always stamping chargeValidUntil
        # closes the dangerous direction of that mismatch for free: the
        # clauseSatisfaction lens's monthly gate (lenses.go) reads ONLY
        # chargeValidUntil, never the state field, so a genuinely-monthly
        # clause re-arms correctly even if clause_period was wrong/omitted
        # here — the alternative (never stamping it) would leave such a
        # clause permanently violating and Weaver re-dispatching
        # indefinitely. The mirror-image mismatch (a genuinely-oneTime
        # clause stamped as if monthly) is harmless: the oneTime gate is
        # chargeCount/authorizedBy-link-driven and never reads
        # chargeValidUntil at all.
        charge_valid_until = time.rfc3339_add(posted_at, %q)
        if clause_period == "monthly":
            # Fire V3 recurring clause: re-arm chargeValidUntil, never
            # complete. This IS the clauseSatisfaction lens's convergence gate
            # for a monthly clause (mirrors lease-signing's bgcheck-freshness
            # validUntil pattern) — unlike the one-time case below, this write
            # is load-bearing, not just audit.
            mutations.append({"op": "update", "key": clause_key + ".status",
                               "document": {"class": "clauseStatus", "isDeleted": False,
                                            "vertexKey": clause_key, "localName": "status",
                                            "data": {"state": "active", "chargeValidUntil": charge_valid_until}}})
        else:
            # Fixed/one-time clause bookkeeping: mark it completed (audit/display
            # only — the clauseSatisfaction lens's convergence gate is the
            # authorizedBy link itself, not this status, so this write is
            # UNCONDITIONED — see the design's R3). chargeValidUntil rides
            # along here too (see the note above); the lens never reads it
            # for a non-monthly clause.
            mutations.append({"op": "update", "key": clause_key + ".status",
                               "document": {"class": "clauseStatus", "isDeleted": False,
                                            "vertexKey": clause_key, "localName": "status",
                                            "data": {"state": "completed", "completedAt": posted_at,
                                                     "chargeValidUntil": charge_valid_until}}})

    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

def execute(state, op):
    ot = op.operationType

    if ot == "DebitAccount":
        return post_entry(state, op, "debit", "account.debited", True)

    if ot == "CreditAccount":
        return post_entry(state, op, "credit", "account.credited", False)

    fail("transaction DDL: unknown operationType: " + ot)
`, recurringChargePeriod)
