package clinicledger

// accountDDLScript handles ClinicCreateAccount. The account gets its OWN
// independently-minted NanoID — vertex NanoIDs are unique identifiers across
// all of Core KV, never reused across vertex types, even deliberately (a
// prior revision minted the account under the patient's own bare NanoID;
// internal/refractor/adjacency keys strictly by bare NodeID with no type
// qualifier, so that reuse silently merged the account's and the patient's
// adjacency edges under one key and corrupted graph traversal for both — see
// adjacency-shared-nanoid-collision-design.md). "One account per patient" is
// instead enforced by a deterministic CREATE-ONLY guard aspect on the
// PRE-EXISTING patient (patientKey + ".ledgerAccount") — a second
// ClinicCreateAccount for the same patient conflicts on that already-existing
// aspect key, the same "let the key shape be the uniqueness guard" idiom, just
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

    if ot == "ClinicCreateAccount":
        patient_key = required_string(p, "patientKey")
        _, patient_id = parts_of(patient_key, "patientKey", "patient")

        # No-orphan invariant: the patient MUST be alive.
        if not vertex_alive(state, patient_key):
            fail("UnknownPatient: " + patient_key)

        # One account per patient, guarded by a deterministic aspect on the
        # PATIENT (not the account — the account's own id is independent and
        # unknown until minted below). Only meaningful when the caller declared
        # the guard key in contextHint.reads (a repeat/racing caller checking
        # before it retries); the FIRST ClinicCreateAccount for a patient declares
        # only patientKey (the guard doesn't exist yet — declaring an
        # as-yet-absent key in reads would HydrationMiss on first touch,
        # deferred past hydration), so on that path the
        # guard aspect's own create-only write is the actual uniqueness
        # enforcement: a genuine race's loser hits a raw substrate conflict
        # here rather than this clean rejection.
        guard_key = patient_key + ".ledgerAccount"
        if vertex_alive(state, guard_key):
            fail("AccountAlreadyExists: " + patient_key)

        acct_id = nanoid.new()
        acct_key = "vtx.clinicaccount." + acct_id

        # heldFor: the account (later-arriving) is the source, the pre-existing
        # patient is the target (Contract #1 §1.1). Reads as "this account is
        # held for this patient."
        held_for_lnk = "lnk.clinicaccount." + acct_id + ".heldFor.patient." + patient_id

        # Root data minimal (D5): {} on root. The balance is derived by the
        # ledgerHistory lens summing linked transactions, never stored here.
        mutations = [
            make_vtx(acct_key, "clinicaccount", {}),
            make_aspect(patient_key, "ledgerAccount", "clinicLedgerAccountGuard", {"accountKey": acct_key}),
            make_link(held_for_lnk, acct_key, patient_key, "heldFor", "heldFor", {}),
        ]
        events = [{"class": "account.created",
                   "data": {"accountKey": acct_key, "patientKey": patient_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": acct_key}}

    fail("account DDL: unknown operationType: " + ot)
`

// transactionDDLScript handles ClinicDebitAccount and ClinicCreditAccount. Each mints a
// fresh transaction vertex + a .entry aspect + the postedTo link to the
// account. The ledger is append-only: no aspect on the account is read or
// mutated here, so concurrent debits/credits against the same account never
// race a read-modify-write — the balance is derived by the ledgerHistory lens
// summing entries. A debit entry carries a bounded payer dimension —
// billedTo (self|insurance, default self) and, only when billedTo is
// insurance, expectedReimbursementCents (must be positive, capped at
// amountCents) — so a clinic can track what it billed insurance for vs. what
// it collected; a credit (payment) has nothing to bill and rejects both
// fields.
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
# branch): 10 pages of 50 postedTo entries covers many years of a billing
# history; an account that exceeds it fails the self-credit closed rather
# than trust a partial sum.
SELF_CREDIT_PAGE_LIMIT = 50
SELF_CREDIT_MAX_PAGES = 10

def post_entry(state, op, entry_type, event_class, allow_appointment_ref):
    p = op.payload
    acct_key = required_string(p, "accountKey")
    _, acct_id = parts_of(acct_key, "accountKey", "clinicaccount")

    if not vertex_alive(state, acct_key):
        fail("UnknownAccount: " + acct_key)

    appt_key = None
    appt_id = None
    if allow_appointment_ref:
        appt_key = optional_string(p, "appointmentRef")
        if appt_key != None:
            _, appt_id = parts_of(appt_key, "appointmentRef", "appointment")
            if not vertex_alive(state, appt_key):
                fail("UnknownAppointment: " + appt_key)

    amount_cents = require_number(p, "amountCents")
    if amount_cents <= 0:
        fail("InvalidArgument: amountCents: required positive number")
    memo = optional_string(p, "memo")

    # Patient-self ownership + amount trust (ClinicCreditAccount only —
    # permissions.go grants no self-scope ClinicDebitAccount), mirroring
    # loftspace-ledger's CreditAccount post_entry. The mere PRESENCE of
    # authContextTarget selects this branch, same idiom as cafe-domain's
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
            fail("AuthDenied: a patient may only credit (pay down) their own account, not charge it")
        # authcontext-target: (ownership) the value derives an identity whose
        # ownership of the account's own patient is then proven by the
        # identifiedBy link read below -- a forged target only fails closed.
        # The patient is recovered from the account's OWN heldFor topology,
        # never the payload, so a forged claim only fails closed.
        _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
        # read-posture: (e) relation=heldFor epoch=none -- an account carries
        # exactly one heldFor link, so this is never a keyspace scan.
        held_for_page, _ = kv.Links(acct_key, "heldFor", "out")
        patient_key = None
        for lk in held_for_page:
            if not lk.isDeleted:
                patient_key = lk.targetVertex
        if patient_key == None:
            fail("AuthDenied: account " + acct_key + " carries no live patient")
        _, patient_id = parts_of(patient_key, "heldFor target", "patient")
        # read-posture: (e) per-candidate follow-up read off the enumeration
        # above -- the patient id is data-derived, unknowable client-side.
        identified_by = kv.Read("lnk.patient." + patient_id + ".identifiedBy.identity." + target_identity_id)
        if identified_by == None or identified_by.isDeleted:
            fail("AuthDenied: a patient may only pay down their own account")

        # Amount trust: nothing on this platform verifies a self-submitted
        # payment actually happened (no payment-rail integration — out of
        # scope for a reference vertical), so an unbounded self-credit would
        # let a patient forgive their own debt for free. The outstanding
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
    tx_key = "vtx.clinictransaction." + tx_id
    posted_at = time.rfc3339_utc(op.submittedAt)

    entry_data = {"type": entry_type, "amountCents": amount_cents, "postedAt": posted_at}
    if memo != None:
        entry_data["memo"] = memo

    # billedTo/expectedReimbursementCents is a charge-only dimension (a
    # payment has nothing to bill) — reject either field on a credit so the
    # shape stays bounded rather than silently accepting and ignoring them.
    has_billed_to = hasattr(p, "billedTo") and getattr(p, "billedTo") != None
    has_reimb = hasattr(p, "expectedReimbursementCents") and getattr(p, "expectedReimbursementCents") != None
    if entry_type == "debit":
        billed_to = optional_string(p, "billedTo")
        if billed_to == None:
            billed_to = "self"
        if billed_to != "self" and billed_to != "insurance":
            fail("InvalidArgument: billedTo: must be \"self\" or \"insurance\", got " + billed_to)
        entry_data["billedTo"] = billed_to

        if billed_to == "insurance":
            if not has_reimb:
                fail("InvalidArgument: expectedReimbursementCents: required when billedTo is \"insurance\"")
            reimb_cents = require_number(p, "expectedReimbursementCents")
            if reimb_cents <= 0:
                fail("InvalidArgument: expectedReimbursementCents: required positive number")
            if reimb_cents > amount_cents:
                fail("InvalidArgument: expectedReimbursementCents: cannot exceed amountCents")
            entry_data["expectedReimbursementCents"] = reimb_cents
        elif has_reimb:
            fail("InvalidArgument: expectedReimbursementCents: only valid when billedTo is \"insurance\"")
    elif has_billed_to or has_reimb:
        fail("InvalidArgument: billedTo/expectedReimbursementCents: only valid on a debit (charge), not a credit (payment)")

    # postedTo: the transaction (later-arriving) is the source, the
    # pre-existing account is the target (Contract #1 §1.1). Reads as
    # "this transaction posted to this account."
    posted_to_lnk = "lnk.clinictransaction." + tx_id + ".postedTo.clinicaccount." + acct_id

    # Root data minimal (D5): {} on root. The charge/payment fact is the
    # .entry aspect; the account itself is untouched (append-only ledger).
    mutations = [
        make_vtx(tx_key, "clinictransaction", {}),
        make_aspect(tx_key, "entry", "transactionEntry", entry_data),
        make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
    ]

    # settles: the transaction (later-arriving) is the source, the
    # pre-existing appointment is the target (Contract #1 §1.1). Only
    # written when the caller supplied appointmentRef — a plain
    # human-submitted ClinicDebitAccount is unaffected. The clinicNoShowSettlement
    # lens walks this link to converge the no-show-fee gap once posted.
    if appt_key != None:
        settles_lnk = "lnk.clinictransaction." + tx_id + ".settles.appointment." + appt_id
        mutations.append(make_link(settles_lnk, tx_key, appt_key, "settles", "settles", {}))

    events = [{"class": event_class,
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "amountCents": amount_cents}}]
    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

def execute(state, op):
    ot = op.operationType

    if ot == "ClinicDebitAccount":
        # workplace-exempt: (ownership-bound) post_entry's own authContextTarget
        # branch fails closed for a debit (permissions.go grants no self-scope
        # ClinicDebitAccount) -- only ClinicCreditAccount's branch below ever
        # reaches the ownership proof.
        return post_entry(state, op, "debit", "account.debited", True)

    if ot == "ClinicCreditAccount":
        # workplace-exempt: (ownership-bound) post_entry proves ownership itself --
        # a self-scoped credit is allowed only once the account's heldFor patient's
        # identifiedBy link resolves to op.authContextTarget.
        return post_entry(state, op, "credit", "account.credited", False)

    fail("transaction DDL: unknown operationType: " + ot)
`
