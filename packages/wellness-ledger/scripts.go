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

        # Consumer self-scope (scope=self grant only): step 3 authorizes via
        # authContext.target == actor (Contract #6); payload.identityKey IS
        # the identity the account is opened for, so the script closes the
        # gap with a direct field compare — no extra kv.Read, mirroring
        # wellness-domain's CreateBooking/payload.booker check. Empty for
        # the standing operator/frontOfHouse grant (scope=any never sets
        # authContext), so this check is a no-op there.
        # authcontext-target: (payload-bind) the target must equal payload.identityKey
        if op.authContextTarget != "" and op.authContextTarget != identity_key:
            fail("AuthDenied: a consumer may only open their OWN ledger account")

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

// transactionDDLScript handles WellnessDebitAccount and WellnessCreditAccount. Each mints a
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

# Self-credit balance-verification budget (post_entry's authContextTarget
# branch), mirroring clinic-ledger's identical constants: 10 pages of 50
# postedTo entries covers many years of a billing history; an account that
# exceeds it fails the self-credit closed rather than trust a partial sum.
SELF_CREDIT_PAGE_LIMIT = 50
SELF_CREDIT_MAX_PAGES = 10

def post_entry(state, op, entry_type, event_class, allow_booking_ref, allow_refund_ref):
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

        # priceBookingRef is independent of bookingRef — a WellnessDebitAccount may
        # carry either, both, or neither. Same validation shape, a DISTINCT
        # settlesClassPrice link below so the no-show and class-price
        # settlement gaps never collide in a count().
        price_booking_key = optional_string(p, "priceBookingRef")
        if price_booking_key != None:
            _, price_booking_id = parts_of(price_booking_key, "priceBookingRef", "booking")
            if not vertex_alive(state, price_booking_key):
                fail("UnknownBooking: " + price_booking_key)

    # refundRef (WellnessCreditAccount only, the mirror of bookingRef/
    # priceBookingRef above being WellnessDebitAccount only): the vertex it
    # names is a wellnessrefund marker (wellness-domain's CancelBooking,
    # ddls.go), not a booking — a cancelled booking is tombstoned by the
    # time any refund posts, so validating alive against class=booking here
    # would always UnknownBooking a genuine refund.
    refund_key = None
    refund_id = None
    if allow_refund_ref:
        refund_key = optional_string(p, "refundRef")
        if refund_key != None:
            _, refund_id = parts_of(refund_key, "refundRef", "wellnessrefund")
            if not vertex_alive(state, refund_key):
                fail("UnknownRefund: " + refund_key)

    amount_cents = require_number(p, "amountCents")
    if amount_cents <= 0:
        fail("InvalidArgument: amountCents: required positive number")
    memo = optional_string(p, "memo")

    # Member-self ownership + amount trust (WellnessCreditAccount only —
    # permissions.go grants no self-scope WellnessDebitAccount), mirroring
    # clinic-ledger's post_entry. The mere PRESENCE of authContextTarget
    # selects this branch, same idiom as ClinicCreditAccount/CreateBooking —
    # it does not change what grant actually authorized the op (a scope=any
    # operator/frontOfHouse submit never attaches a target), it only ever
    # narrows behavior.
    # authcontext-target: (selector) a branch selector, not a confinement
    # exemption -- so it reads the raw target (did the caller declare a self
    # target at all) rather than authTargetValidated. Safe because presence
    # only pushes the caller onto the STRICTER branch below (the ownership +
    # amount proofs), never grants anything a scope=any submit would not
    # already.
    if op.authContextTarget != "":
        if entry_type != "credit":
            fail("AuthDenied: a member may only credit (pay down) their own account, not charge it")
        # authcontext-target: (ownership) unlike clinic-ledger's account→
        # patient→identifiedBy chain, a wellnessaccount's heldFor link
        # targets the IDENTITY directly (accountDDLScript above), so
        # ownership is a single link compare, no follow-up identifiedBy
        # read needed. The identity is recovered from the account's OWN
        # heldFor topology, never the payload, so a forged target only
        # fails closed.
        # read-posture: (e) relation=heldFor epoch=none -- an account carries
        # exactly one heldFor link, so this is never a keyspace scan.
        held_for_page, _ = kv.Links(acct_key, "heldFor", "out")
        held_identity_key = None
        for lk in held_for_page:
            if not lk.isDeleted:
                held_identity_key = lk.targetVertex
        if held_identity_key == None:
            fail("AuthDenied: account " + acct_key + " carries no live identity")
        if held_identity_key != op.authContextTarget:
            fail("AuthDenied: a member may only pay down their own account")

        # Amount trust: nothing on this platform verifies a self-submitted
        # payment actually happened (no payment-rail integration — out of
        # scope for a reference vertical), so an unbounded self-credit would
        # let a member forgive their own debt for free. The outstanding
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
    # WellnessDebitAccount is unaffected. The wellnessNoShowSettlement lens walks
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

    # settlesRefund: the transaction (later-arriving) is the source, the
    # pre-existing wellnessrefund marker is the target (Contract #1 §1.1).
    # Only written when the caller supplied refundRef. The
    # wellnessRefundSettlement lens walks this link to converge the refund
    # gap once posted, mirroring settlesClassPrice's exact shape.
    if refund_key != None:
        settles_refund_lnk = "lnk.wellnesstransaction." + tx_id + ".settlesRefund.wellnessrefund." + refund_id
        mutations.append(make_link(settles_refund_lnk, tx_key, refund_key, "settlesRefund", "settlesRefund", {}))

    events = [{"class": event_class,
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "amountCents": amount_cents}}]
    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

def execute(state, op):
    ot = op.operationType

    if ot == "WellnessDebitAccount":
        # workplace-exempt: (ownership-bound) post_entry's own authContextTarget
        # branch fails closed for a debit (permissions.go grants no self-scope
        # WellnessDebitAccount, so authContextTarget is never legitimately set here).
        return post_entry(state, op, "debit", "account.debited", True, False)

    if ot == "WellnessCreditAccount":
        # workplace-exempt: (ownership-bound) post_entry proves ownership itself --
        # a self-scoped credit is allowed only once the account's own heldFor
        # link resolves to op.authContextTarget.
        return post_entry(state, op, "credit", "account.credited", False, True)

    fail("transaction DDL: unknown operationType: " + ot)
`
