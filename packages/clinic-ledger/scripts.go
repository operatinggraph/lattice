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
// Root data stays {} on the account (D5): the ledgerHistory lens still derives
// the DISPLAYED balance by summing transactions. The account also carries a
// maintained .balance aspect ({balanceCents}) — an O(1) cache post_entry
// (transactionDDLScript) keeps in lockstep with every posted entry, so the
// self-credit ownership check never has to replay full history to answer "how
// much is owed" (see transactionDDLScript's own comment for the OCC shape
// that keeps it race-free under concurrent debits/credits).
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

        # Root data minimal (D5): {} on root. The .balance aspect starts at
        # zero and is the only thing post_entry mutates going forward — this
        # create is unconditioned (brand-new account, nothing to race).
        mutations = [
            make_vtx(acct_key, "clinicaccount", {}),
            make_aspect(patient_key, "ledgerAccount", "clinicLedgerAccountGuard", {"accountKey": acct_key}),
            make_aspect(acct_key, "balance", "clinicAccountBalance", {"balanceCents": 0}),
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
// account.
//
// A posted entry against an account that CARRIES a .balance aspect
// (accountDDLScript mints one at ClinicCreateAccount) ALSO moves that aspect by
// the signed amount, via a BARE update — deliberately no expectedRevision of its
// own. This script's own derive_reads(op) declares <accountKey>.balance in
// optionalReads for both ops it handles (Contract #2 §2.5 class (g)), so the key
// is hydrated — or recorded known-absent — whatever the submitter declared, and
// commit_path.go's applyHydratedRevisions (Contract #3 §3.2 (A)) conditions the
// update on the step-4 hydrated revision FOR us and marks it retry-eligible: on a
// lost race the Processor re-hydrates and retries the whole op (the bounded
// §3.2 (B) internal retry) before a terminal RevisionConflict, so two concurrent
// debits/credits against one account serialize instead of silently dropping an
// update. (An update that supplied its own expectedRevision would be treated as
// an explicit-caller compensating assertion instead — excluded from that retry —
// which is why make_aspect_update takes no revision parameter.)
//
// That cache is what lets post_entry answer "how much is owed" in O(1) — a single
// kv.Read of .balance — instead of replaying the account's full postedTo history,
// which used to blow the Starlark wall budget on any patient with a long ledger
// (a heavy self-pay account was timing out 9 of 10 self-credit submits). The
// ledgerHistory lens remains the display source of truth (it still sums
// entries independently, for the FE and for anyone auditing the maintained
// balance against the append-only log); .balance is purely this DDL's own
// fast authorization cache, never read by anything outside this package.
//
// An account minted under clinic-ledger < 0.3.0 carries no .balance at all, and
// that legacy set is CLOSED: ClinicCreateAccount mints the aspect, so no account
// opened today joins it. Only a SELF-PAY — a scope=self patient credit, the one
// leg whose cap needs the number — pays the one-time bounded replay that computes
// such an account's balance. A ClinicDebitAccount, and a staff credit or waiver,
// against a legacy account neither replay nor write .balance: the account stays
// legacy until a self-pay first touches it, and that self-pay's replay sums the
// whole history (those later charges included), so the cache is never seeded from
// a partial sum.
//
// A debit entry carries a bounded payer dimension —
// billedTo (self|insurance, default self) and, only when billedTo is
// insurance, expectedReimbursementCents (must be positive, capped at
// amountCents) — so a clinic can track what it billed insurance for vs. what
// it collected; a credit (payment) has nothing to bill and rejects both
// fields. A credit entry instead carries reason (payment|waiver, default
// payment) — a waiver forgives debt (e.g. a no-show fee) rather than
// recording cash collected; rejected on a debit, and rejected on a
// self-scoped (patient) credit, which may only pay down a balance.
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

def make_aspect_update(vtx_key, local_name, cls, data):
    # Deliberately NO expectedRevision here — leaving it unset is what makes
    # this update RETRY-eligible, not less safe. Contract #3 §3.2 (A) in
    # commit_path.go's applyHydratedRevisions auto-conditions any bare update
    # on a key the op declared in reads/optionalReads (so still safe, still
    # OCC-guarded) using the step-4 hydrated revision, and marks it
    # defaulted — the retry-eligible set. An update that supplies its OWN
    # expectedRevision instead is treated as an explicit-caller compensating
    # assertion and is EXCLUDED from that retry ("never overridden") — it
    # hard-conflicts instead of serializing, which is the opposite of what a
    # maintained counter two ops can race on needs.
    #
    # It is also the reviving verb for a TOMBSTONED aspect. A create against a
    # tombstone is refused (Contract #3 §3.3), so post_entry's .balance write
    # only mints fresh where step 4 saw the key genuinely ABSENT and comes here
    # otherwise — the auto-conditioning above then pins the tombstone's own
    # revision, so the revival races nothing.
    return {"op": "update", "key": vtx_key + "." + local_name,
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
    # clinicLedgerHistory balance sums into a non-representable total, and every
    # description of this field -- the DDL's own, the op-meta's schema, the
    # aspect's -- already says integer cents. Enforce what they all claim.
    v = require_number(p, name)
    if v != int(v):
        fail("InvalidArgument: " + name + ": required whole cents, got " + str(v))
    return int(v)

def dollars(cents):
    # Cents rendered the way the billing view and the statement show money. The
    # refusals below are toasted VERBATIM at a staffer or a patient, and
    # "exceeds 2500" reads as a different number than the $25.00 balance it is
    # talking about. The sign is carried explicitly so a negative total (an
    # account in credit after an over-waiver) never renders as "$-25.00".
    #
    # int() first: money here is whole cents (the .entry schema, the .balance
    # aspect and every field description all say integer cents), and the dollar
    # split below is integer arithmetic, so a non-integral input renders as a
    # number rather than as a malformed string.
    whole = int(cents)
    negative = whole < 0
    if negative:
        whole = -whole
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

# Legacy-account .balance backfill budget (backfill_balance below, reached only
# by a SELF-PAY against an account minted under clinic-ledger < 0.3.0): 10 pages
# of 50 postedTo entries covers many years of billing history; an account that
# exceeds it fails closed rather than seed a partial sum. The self-pay that pays
# this cost writes the aspect, so that account is O(1) forever after — this runs
# at most once per legacy account.
BALANCE_BACKFILL_PAGE_LIMIT = 50
BALANCE_BACKFILL_MAX_PAGES = 10

def backfill_balance(acct_key):
    # The starting balance of an account that carries no .balance aspect,
    # replayed once from its own postedTo history under the budget above.
    #
    # Reached ONLY from the self-pay leg, and only after the caller's standing to
    # act on this account has already been proven (post_entry runs the
    # patient-ownership proof first). That ordering is what keeps the replay from
    # being an amplification primitive: a caller who cannot post to the account
    # cannot make it walk the account's history either.
    #
    # Sign convention is the ledgerHistory lens's own: a debit is what is owed, a
    # credit (payment or waiver alike) pays it down.
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

def post_entry(state, op, entry_type, event_class, allow_appointment_ref):
    p = op.payload
    acct_key = required_string(p, "accountKey")
    _, acct_id = parts_of(acct_key, "accountKey", "clinicaccount")

    if not vertex_alive(state, acct_key):
        fail("UnknownAccount: " + acct_key)

    amount_cents = require_cents(p, "amountCents")
    if amount_cents <= 0:
        fail("InvalidArgument: amountCents: required positive number")
    memo = optional_string(p, "memo")

    # reason distinguishes a credit that is cash actually collected from one
    # that forgives debt (a waived no-show fee) -- both reduce the derived
    # balance identically (owed_cents -= amount_cents below, and the
    # ledgerHistory lens's sum(debits)-sum(credits)), but the lens projects
    # reason so a reader never mistakes forgiven debt for money received.
    # Credit-only, same bounded-dimension shape as billedTo below. Validated
    # HERE, above the authContextTarget branch, because that branch's own
    # refusal of a self-scoped waiver reads this value.
    reason = optional_string(p, "reason")
    if entry_type == "credit":
        if reason == None:
            reason = "payment"
        if reason != "payment" and reason != "waiver":
            fail("InvalidArgument: reason: must be \"payment\" or \"waiver\", got " + reason)
    elif reason != None:
        fail("InvalidArgument: reason: only valid on a credit (payment/waiver), not a debit (charge)")

    # Patient-self ownership (ClinicCreditAccount only — permissions.go grants
    # no self-scope ClinicDebitAccount), mirroring loftspace-ledger's
    # CreditAccount post_entry. Ownership is ALL this branch proves; the amount
    # cap that follows it reads the account's own balance and is written out
    # below. The mere PRESENCE of authContextTarget selects this branch, same
    # idiom as cafe-domain's Charge/Settle — it does not change what grant
    # actually authorized the op (a scope=any operator/frontOfHouse submit never
    # attaches a target), it only ever narrows behavior.
    # authcontext-target: (selector) a branch selector, not a confinement
    # exemption -- so it reads the raw target (did the caller declare a self
    # target at all) rather than authTargetValidated. Safe because presence
    # only pushes the caller onto the STRICTER branch below (the ownership
    # proof and the amount cap), never grants anything a scope=any submit would
    # not already.
    if op.authContextTarget != "":
        if entry_type != "credit":
            fail("AuthDenied: a patient may only credit (pay down) their own account, not charge it")
        if reason == "waiver":
            fail("AuthDenied: a patient may only pay down their own account, not waive a charge")
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

    # Everything above this line is the caller's standing and the payload's
    # shape; everything below it touches the ACCOUNT's own money. The order is
    # deliberate: the .balance read and — on a legacy account — the bounded
    # history replay behind it are the most expensive work this script does, so
    # they sit after the patient-ownership proof. Hoisting them above it would
    # hand anyone holding the scope=self grant a way to name a stranger's
    # account key and spend the whole replay budget before being denied.
    #
    # is_self_pay: a scope=self patient credit, proven above to be theirs. It is
    # the one leg the amount cap binds — a staff credit or waiver is a decision
    # the clinic makes, not a payment the platform has to take on trust — and
    # the one leg that ever pays for a legacy account's backfill.
    # authcontext-target: (selector) the SAME condition the ownership branch
    # above is written on, restated so the cap and the replay read one predicate
    # rather than two that could drift apart. Presence only pushes the caller
    # onto the capped branch; it exempts nothing and grants nothing.
    is_self_pay = op.authContextTarget != ""

    # .balance is a declared OPTIONALREADS key — this script's own
    # derive_reads(op) declares it for both ops it handles, so the key is
    # hydrated or recorded known-absent whatever the submitter sent, and every
    # dispatcher declares it statically besides (opmetas.go for the two
    # descriptor-driven ops, targets.go for the Weaver-dispatched charge and
    # reversal). Absence-tolerant, because an account minted under clinic-ledger
    # < 0.3.0 carries no .balance and a required read would reject every entry
    # against such an account.
    balance_key = acct_key + ".balance"
    # read-posture: (d) optionalReads — derived server-side by this script's own
    # derive_reads(op) for ClinicDebitAccount/ClinicCreditAccount (Contract #2
    # §2.5 class (g)), and declared statically by opmetas.go's
    # OpDispatchSpec.OptionalReads + targets.go's GapActionSpec.OptionalReads
    # (FE descriptor and Weaver directOp alike).
    balance_doc = kv.Read(balance_key)
    # Two questions, not one. balance_absent decides the WRITE verb: a create is
    # refused against a tombstone (Contract #3 §3.3), so only a genuinely absent
    # key may be minted and a tombstoned one is revived by the update.
    # needs_backfill decides whether there is a number to read at all.
    balance_absent = balance_doc == None
    needs_backfill = balance_absent or balance_doc.isDeleted

    # balance_cents stays None on a legacy account this op does not backfill —
    # a charge, or a staff credit or waiver. Such an account keeps NO cache
    # rather than a wrong one: seeding it from this entry alone would record a
    # total that never counted the history behind it, and every later self-pay
    # would be measured against that. The account stays legacy until a self-pay
    # first touches it, and that self-pay's replay sums the whole history, this
    # entry included.
    balance_cents = None
    if not needs_backfill:
        # The CLASS, not just the key: this script is the sole writer of a
        # .balance aspect and writes exactly that class, so a document of any
        # other class under this key is a fault to refuse, never a number to
        # spend a self-pay cap on.
        if not hasattr(balance_doc, "class") or getattr(balance_doc, "class") != "clinicAccountBalance":
            fail("InvalidState: this account's balance aspect is not a clinicAccountBalance")
        balance_cents = balance_doc.data.get("balanceCents")
        if balance_cents == None:
            balance_cents = 0
    elif is_self_pay:
        balance_cents = backfill_balance(acct_key)

    # Amount trust on the self-pay leg: nothing on this platform verifies a
    # self-submitted payment actually happened (no payment-rail integration —
    # out of scope for a reference vertical), so an unbounded self-credit would
    # let a patient forgive their own debt for free. owed_cents comes from the
    # account's own maintained .balance aspect (read above, O(1) — never the
    # payload), which post_entry itself keeps in lockstep with every posted
    # entry. A self-credit may never exceed what is owed.
    #
    # A staff credit is deliberately NOT capped here: a front-desk waiver
    # forgives debt the clinic chose to forgive, and a correction dispatched by
    # clinicNoShowSettlement's missing_reversal gap gives back a charge that may
    # already have been paid — both legitimately take the balance negative.
    #
    # Neither refusal names the account: both are toasted verbatim at the
    # patient, and a raw vtx key is not something they can act on. The amounts
    # are the actionable half, so they are spelled as money.
    if is_self_pay:
        owed_cents = balance_cents
        if owed_cents <= 0:
            fail("AuthDenied: this account has no outstanding balance to pay")
        if amount_cents > owed_cents:
            fail("AuthDenied: a payment of " + dollars(amount_cents) +
                 " exceeds the outstanding balance of " + dollars(owed_cents))

    appt_key = None
    appt_id = None
    if allow_appointment_ref:
        appt_key = optional_string(p, "appointmentRef")
        if appt_key != None:
            _, appt_id = parts_of(appt_key, "appointmentRef", "appointment")
            if not vertex_alive(state, appt_key):
                fail("UnknownAppointment: " + appt_key)

    # reversesRef (ClinicCreditAccount only): the mirror of appointmentRef
    # above, one level removed — an optional back-reference to the
    # clinictransaction this credit reverses (a no-show fee that posted
    # before a CorrectAppointmentStatus correction moved the appointment
    # off noShow), written as a reverses link (credit tx -> the reversed
    # tx). clinicNoShowSettlement's missing_reversal gap (lenses.go /
    # targets.go) is this field's only caller; a human-submitted
    # ClinicCreditAccount simply omits it and gets the plain shape.
    reverses_key = None
    reverses_id = None
    if entry_type == "credit":
        reverses_key = optional_string(p, "reversesRef")
        if reverses_key != None:
            _, reverses_id = parts_of(reverses_key, "reversesRef", "clinictransaction")
            if not vertex_alive(state, reverses_key):
                fail("UnknownTransaction: " + reverses_key)
    elif hasattr(p, "reversesRef") and getattr(p, "reversesRef") != None:
        fail("InvalidArgument: reversesRef: only valid on a credit (payment/waiver), not a debit (charge)")

    tx_id = nanoid.new()
    tx_key = "vtx.clinictransaction." + tx_id
    posted_at = time.rfc3339_utc(op.submittedAt)

    entry_data = {"type": entry_type, "amountCents": amount_cents, "postedAt": posted_at}
    if memo != None:
        entry_data["memo"] = memo
    if entry_type == "credit":
        entry_data["reason"] = reason

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
            reimb_cents = require_cents(p, "expectedReimbursementCents")
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
    # .entry aspect; the only thing this DDL ever mutates on the ACCOUNT is
    # its .balance cache, appended just below.
    mutations = [
        make_vtx(tx_key, "clinictransaction", {}),
        make_aspect(tx_key, "entry", "transactionEntry", entry_data),
        make_link(posted_to_lnk, tx_key, acct_key, "postedTo", "postedTo", {}),
    ]

    # The cache moves only where there is a cache to move: balance_cents is
    # None exactly on a legacy account this op does not backfill, and such an
    # account is left untouched rather than seeded from one entry.
    #
    # The sign convention is the ledgerHistory lens's own sum (a debit increases
    # what is owed, a credit — payment or waiver alike — decreases it). A staff
    # credit is not bounded by owed_cents (only the self-pay cap above enforces
    # that), so this legitimately goes negative on an over-waiver or on a
    # reversal of a charge already paid.
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
            mutations.append(make_aspect(acct_key, "balance", "clinicAccountBalance",
                                         {"balanceCents": new_balance_cents}))
        else:
            mutations.append(make_aspect_update(acct_key, "balance", "clinicAccountBalance",
                                                {"balanceCents": new_balance_cents}))

    # settles: the transaction (later-arriving) is the source, the
    # pre-existing appointment is the target (Contract #1 §1.1). Only
    # written when the caller supplied appointmentRef — a plain
    # human-submitted ClinicDebitAccount is unaffected. The clinicNoShowSettlement
    # lens walks this link to converge the no-show-fee gap once posted.
    if appt_key != None:
        settles_lnk = "lnk.clinictransaction." + tx_id + ".settles.appointment." + appt_id
        mutations.append(make_link(settles_lnk, tx_key, appt_key, "settles", "settles", {}))

    # reverses: the credit (later-arriving) is the source, the pre-existing
    # debit transaction is the target (Contract #1 §1.1). Only written when
    # the caller supplied reversesRef. clinicNoShowSettlement's
    # missing_reversal gap walks this link the same way missing_charge
    # walks settles: once posted, the reversal converges and stays
    # converged (idempotency-by-existence, no separate guard needed since
    # the gate is txCount=1 AND reversalCount=0).
    if reverses_key != None:
        reverses_lnk = "lnk.clinictransaction." + tx_id + ".reverses.clinictransaction." + reverses_id
        mutations.append(make_link(reverses_lnk, tx_key, reverses_key, "reverses", "reverses", {}))

    events = [{"class": event_class,
               "data": {"accountKey": acct_key, "transactionKey": tx_key, "amountCents": amount_cents}}]
    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": tx_key}}

NANOID_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

def is_clinicaccount_key(key):
    # Contract #1's whole vertex grammar for a clinicaccount, not a prefix test.
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
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != "clinicaccount":
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
    # .balance aspect is hydrated — or recorded known-absent — on EVERY dispatch
    # of these two ops, whatever the submitter happened to declare.
    #
    # That guarantee is the point, not the saved round trip. .balance is the
    # quantity the self-pay cap is measured against AND a key every posted entry
    # updates, and a bare update is auto-conditioned on the step-4 hydrated
    # revision only for a key the operation declared (Contract #3 §3.2). A
    # submitter that omitted the declaration would get a live read and an
    # UNCONDITIONED update, so K concurrent entries could each pass the cap
    # against the same balance and credit K times it. A guard a caller can
    # switch off by not mentioning it is not a guard, and contextHint is
    # caller-supplied and never enforced — hence this, the channel the platform
    # owns. The dispatchers' own static declarations (opmetas.go, targets.go)
    # stay: they document the read set, this guarantees it.
    #
    # optionalReads, never reads: an account minted under clinic-ledger < 0.3.0
    # carries no .balance, and a required read's absence is a HydrationMiss that
    # would block every entry against such an account rather than let a self-pay
    # backfill it.
    #
    # The op argument is a struct -- op.operationType, op.actor, op.payload
    # (also a struct). No kv, no nanoid: both are fail-closed stubs in this
    # pass, and a derivation that reads state is a read, not a derivation.
    ot = op.operationType
    if ot != "ClinicDebitAccount" and ot != "ClinicCreditAccount":
        return {}
    # optional_string, never required_string: a missing or malformed accountKey
    # derives nothing rather than faulting the pre-pass -- post_entry's own
    # required_string/parts_of still raise the real InvalidArgument.
    acct_key = optional_string(op.payload, "accountKey")
    if not is_clinicaccount_key(acct_key):
        return {}
    return {"optionalReads": [acct_key + ".balance"]}

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
