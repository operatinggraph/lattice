package wellnessledger

// accountDDLScript handles WellnessCreateAccount. The account gets its OWN
// independently-minted NanoID — vertex NanoIDs are unique identifiers across
// all of Core KV, never reused across vertex types, even deliberately (see
// clinic-ledger/scripts.go's adjacency-shared-nanoid-collision-design.md
// note, which this mirrors exactly). "One account per member" is instead
// enforced by a deterministic CREATE-ONLY guard aspect on the PRE-EXISTING
// identity (identityKey + ".wellnessLedgerAccount") — a second WellnessCreateAccount
// for the same identity conflicts on that already-existing aspect key, the
// same "let the key shape be the uniqueness guard" idiom, just anchored on
// the pre-existing parent instead of a freshly-minted sibling. Root data
// stays {} on the account (D5): the balance is derived by the
// wellnessLedgerHistory lens, never stored here.
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

    if ot == "WellnessCreateAccount":
        identity_key = required_string(p, "identityKey")
        _, identity_id = parts_of(identity_key, "identityKey", "identity")

        # No-orphan invariant: the identity MUST be alive.
        if not vertex_alive(state, identity_key):
            fail("UnknownIdentity: " + identity_key)

        # One account per member, guarded by a deterministic aspect on the
        # IDENTITY (not the account — the account's own id is independent and
        # unknown until minted below). Only meaningful when the caller declared
        # the guard key in contextHint.reads (a repeat/racing caller checking
        # before it retries); the FIRST WellnessCreateAccount for an identity declares
        # only identityKey (the guard doesn't exist yet — declaring an
        # as-yet-absent key in reads would HydrationMiss on first touch,
        # deferred past hydration), so on that path the
        # guard aspect's own create-only write is the actual uniqueness
        # enforcement: a genuine race's loser hits a raw substrate conflict
        # here rather than this clean rejection.
        guard_key = identity_key + ".wellnessLedgerAccount"
        if vertex_alive(state, guard_key):
            fail("AccountAlreadyExists: " + identity_key)

        acct_id = nanoid.new()
        acct_key = "vtx.wellnessaccount." + acct_id

        # heldFor: the account (later-arriving) is the source, the pre-existing
        # identity is the target (Contract #1 §1.1). Reads as "this account is
        # held for this identity."
        held_for_lnk = "lnk.wellnessaccount." + acct_id + ".heldFor.identity." + identity_id

        # Root data minimal (D5): {} on root. The balance is derived by the
        # wellnessLedgerHistory lens summing linked transactions, never stored here.
        mutations = [
            make_vtx(acct_key, "wellnessaccount", {}),
            make_aspect(identity_key, "wellnessLedgerAccount", "wellnessLedgerAccountGuard", {"accountKey": acct_key}),
            make_link(held_for_lnk, acct_key, identity_key, "heldFor", "heldFor", {}),
        ]
        events = [{"class": "account.created",
                   "data": {"accountKey": acct_key, "identityKey": identity_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": acct_key}}

    fail("account DDL: unknown operationType: " + ot)
`

// transactionDDLScript handles DebitAccount and CreditAccount. Each mints a
// fresh transaction vertex + a .entry aspect + the postedTo link to the
// account. The ledger is append-only: no aspect on the account is read or
// mutated here, so concurrent debits/credits against the same account never
// race a read-modify-write — the balance is derived by the
// wellnessLedgerHistory lens summing entries. Unlike clinic-ledger's
// transaction DDL, there is no billedTo/insurance payer dimension — wellness
// billing has no insurance concept, so the entry stays the plain
// {type, amountCents, memo?, postedAt} shape.
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

def post_entry(state, op, entry_type, event_class, allow_booking_ref):
    p = op.payload
    acct_key = required_string(p, "accountKey")
    _, acct_id = parts_of(acct_key, "accountKey", "wellnessaccount")

    if not vertex_alive(state, acct_key):
        fail("UnknownAccount: " + acct_key)

    booking_key = None
    booking_id = None
    price_booking_key = None
    price_booking_id = None
    if allow_booking_ref:
        booking_key = optional_string(p, "bookingRef")
        if booking_key != None:
            _, booking_id = parts_of(booking_key, "bookingRef", "booking")
            if not vertex_alive(state, booking_key):
                fail("UnknownBooking: " + booking_key)

        # priceBookingRef is independent of bookingRef — a DebitAccount may
        # carry either, both, or neither. Same validation shape, a DISTINCT
        # settlesClassPrice link below so the no-show and class-price
        # settlement gaps never collide in a count().
        price_booking_key = optional_string(p, "priceBookingRef")
        if price_booking_key != None:
            _, price_booking_id = parts_of(price_booking_key, "priceBookingRef", "booking")
            if not vertex_alive(state, price_booking_key):
                fail("UnknownBooking: " + price_booking_key)

    amount_cents = require_number(p, "amountCents")
    if amount_cents <= 0:
        fail("InvalidArgument: amountCents: required positive number")
    memo = optional_string(p, "memo")

    tx_id = nanoid.new()
    tx_key = "vtx.wellnesstransaction." + tx_id
    posted_at = time.rfc3339_utc(op.submittedAt)

    entry_data = {"type": entry_type, "amountCents": amount_cents, "postedAt": posted_at}
    if memo != None:
        entry_data["memo"] = memo

    # postedTo: the transaction (later-arriving) is the source, the
    # pre-existing account is the target (Contract #1 §1.1). Reads as
    # "this transaction posted to this account."
    posted_to_lnk = "lnk.wellnesstransaction." + tx_id + ".postedTo.wellnessaccount." + acct_id

    # Root data minimal (D5): {} on root. The charge/payment fact is the
    # .entry aspect; the account itself is untouched (append-only ledger).
    mutations = [
        make_vtx(tx_key, "wellnesstransaction", {}),
        make_aspect(tx_key, "entry", "transactionEntry", entry_data),
        make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
    ]

    # settles: the transaction (later-arriving) is the source, the
    # pre-existing booking is the target (Contract #1 §1.1). Only written
    # when the caller supplied bookingRef — a plain human-submitted
    # DebitAccount is unaffected. The wellnessNoShowSettlement lens walks
    # this link to converge the no-show-fee gap once posted.
    if booking_key != None:
        settles_lnk = "lnk.wellnesstransaction." + tx_id + ".settles.booking." + booking_id
        mutations.append(make_link(settles_lnk, tx_key, booking_key, "settles", "settles", {}))

    # settlesClassPrice: the transaction (later-arriving) is the source, the
    # pre-existing booking is the target (Contract #1 §1.1). A DISTINCT
    # relation from settles — only written when the caller supplied
    # priceBookingRef, independent of bookingRef/settles above. The
    # wellnessClassPriceSettlement lens walks this link to converge the
    # class-price gap once posted.
    if price_booking_key != None:
        settles_price_lnk = "lnk.wellnesstransaction." + tx_id + ".settlesClassPrice.booking." + price_booking_id
        mutations.append(make_link(settles_price_lnk, tx_key, price_booking_key, "settlesClassPrice", "settlesClassPrice", {}))

    events = [{"class": event_class,
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "amountCents": amount_cents}}]
    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

def execute(state, op):
    ot = op.operationType

    if ot == "DebitAccount":
        return post_entry(state, op, "debit", "account.debited", True)

    if ot == "CreditAccount":
        return post_entry(state, op, "credit", "account.credited", False)

    fail("transaction DDL: unknown operationType: " + ot)
`
