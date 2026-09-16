package clinicledger

import "fmt"

// ArrearsGraceDays is the net term between a charge posting and the balance it
// opened counting overdue. The package OWNS the term, so there is exactly one
// source for it: the Starlark below reads it as a Go duration string
// (ArrearsGraceDays × 24 hours, the form time.rfc3339_add takes) and
// cmd/clinic-app's statement math (its statementGraceDays constant) must equal
// it — a Go test in that app pins the two equal. A patient's displayed due
// date and the instant the arrears reminder fires are the same fact, and two
// copies of it drift into a statement that says one thing while the
// notification says another.
//
// Days × 24h is exact here because every timestamp this ledger stores is
// canonical UTC (time.rfc3339_utc at write time), where a calendar day is
// always 24 hours — Go's AddDate(0, 0, ArrearsGraceDays) over a UTC instant
// lands on the same second.
const ArrearsGraceDays = 15

// arrearsGracePrelude binds ArrearsGraceDays into Starlark, once, as the Go
// duration string time.rfc3339_add takes. Both scripts that compute an arrears
// due date open with it, so the account DDL's Weaver-dispatched evaluation and
// the transaction DDL's episode-opening charge derive the same date from the
// same Go constant rather than from two literals that can drift apart.
//
// Prepended rather than interpolated with a format verb: transactionDDLScript's
// money formatting contains a literal '%' (dollars()), and a template would
// make every future edit to either script responsible for escaping it.
var arrearsGracePrelude = fmt.Sprintf(`
ARREARS_GRACE_DURATION = "%dh"
`, ArrearsGraceDays*24)

// accountDDLScript is the account DDL's Starlark, opened by the grace-term
// binding above.
var accountDDLScript = arrearsGracePrelude + accountDDLScriptBody

// accountDDLScriptBody handles ClinicCreateAccount. The account gets its OWN
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
//
// It ALSO handles EvaluateClinicArrears, the Weaver-dispatched arrears
// evaluation: it recomputes the account's FIFO-oldest open charge over a
// bounded replay of the postedTo history, records the resulting due date in the
// account's own .arrears aspect, and — once that date has passed and no
// reminder has gone out for it — fires the external.notification the bridge
// turns into a real message to the patient. The FIFO is deliberately NOT
// maintained incrementally by post_entry: a partial payment moves the head to a
// charge no single entry can name without replaying the account's history, so
// the head is recomputed once, here, only when it matters (post_entry keeps
// only the coarse episode state the convergence lens arms its timer on).
const accountDDLScriptBody = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_update(vtx_key, local_name, cls, data):
    # Deliberately NO expectedRevision — the same choice, for the same reason,
    # transactionDDLScript's own make_aspect_update documents at length: a bare
    # update on a key the operation DECLARED is auto-conditioned on the step-4
    # hydrated revision (Contract #3 §3.2) and marked retry-eligible, so a lost
    # race re-hydrates and re-runs instead of hard-conflicting. It is also the
    # reviving verb for a tombstoned aspect, which a create would only collide
    # with (Contract #3 §3.3).
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

# EvaluateClinicArrears' replay budget over the account's postedTo history: 10
# pages of 50 entries covers many years of billing history. The ceiling is not a
# taste judgement — it is what the Processor's production script wall (250ms)
# affords for a live paged walk plus a per-candidate follow-up read, so raising
# it does not extend the reach, it just moves the failure from this budget to
# the wall.
#
# An account that exceeds it is not aged against a truncated FIFO — a partial
# replay would name the wrong head and the reminder that went out would name a
# charge the patient had already paid. But it does not fail either: see the
# degrade branch in execute(). A refusal here is a PERMANENT silent stop, because
# the only thing that would re-drive the op is the very gap this account's row
# opens, and a rejected op never closes it — Weaver would re-dispatch a doomed
# evaluation on every window, forever, with no reminder and no operator signal.
# Instead the exhaustion is RECORDED (historyTooLong) so the row goes quiet, the
# operator can see it in the read model, and the next posted entry re-arms one
# more attempt.
ARREARS_PAGE_LIMIT = 50
ARREARS_MAX_PAGES = 10

def arrears_entries(acct_key):
    # Every live entry posted to this account, as {postedAt, key, type,
    # amountCents, reversesKey}, and whether the page budget ran out before
    # the walk did. reversesKey is the key of the charge a credit's own
    # reverses link names (None for a payment, or for a reversal whose link
    # is tombstoned) — arrears_head's netting pre-pass reads it to retire
    # that specific charge instead of aging it by plain FIFO order, the same
    # rule cmd/clinic-app/ledger.go's deriveStatement runs. An entry missing
    # any of postedAt/type/amountCents is skipped rather than guessed at,
    # exactly as transactionDDLScript's backfill_balance skips it.
    entries = []
    cursor = None
    budget_exhausted = True
    for _page in range(ARREARS_MAX_PAGES):
        # read-posture: (e) relation=postedTo epoch=none -- bounded by the page
        # budget; the caller degrades when it is exhausted.
        page, cursor = kv.Links(acct_key, "postedTo", "in", cursor, ARREARS_PAGE_LIMIT)
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
            reverses_key = None
            if tx_type == "credit":
                # read-posture: (e) relation=reverses epoch=none -- a reversing
                # credit carries exactly one reverses link, written atomically
                # by ClinicCreditAccount's reversesRef leg and never added to
                # afterward, so a limit of 1 (no cursor loop) is exact, never a
                # keyspace scan. The limit is not optional: this runs once per
                # CREDIT in the whole postedTo replay, so an unbounded page
                # here is charged at the 256 default against the script's
                # live-read budget, and a history with hundreds of credits
                # blows it with a script error instead of the intended
                # historyTooLong degrade this function's own doc comment
                # promises.
                reverses_page, _ = kv.Links(lk.sourceVertex, "reverses", "out", None, 1)
                for lk2 in reverses_page:
                    if not lk2.isDeleted:
                        reverses_key = lk2.targetVertex
            entries.append({"postedAt": tx_posted_at, "key": lk.sourceVertex,
                            "type": tx_type, "amountCents": tx_amount,
                            "reversesKey": reverses_key})
        if cursor == None:
            budget_exhausted = False
            break
    return entries, budget_exhausted

def arrears_head(entries):
    # The FIFO the patient's own statement runs, reproduced exactly
    # (cmd/clinic-app/ledger.go deriveStatement + sortLedgerRows): entries in
    # (postedAt, transactionKey) order.
    #
    # A pre-pass nets every credit that names the charge it reverses
    # (reversesKey) against that debit's own face amount — capped there,
    # accumulated across however many reversing credits name the same
    # debit — before the FIFO walk ever runs, so a reversal of a NEWER charge
    # does not pay off an OLDER, unrelated one. A debit fully retired this
    # way never opens; a partially-retired one opens for the remainder.
    #
    # Everything else still FIFOs: credits offset the OLDEST still-open
    # debit first, and a credit with no open debit to apply to (net of
    # whatever it retired above) carries its remainder forward as surplus
    # that prepays whichever debits arrive next. The survivor at the front
    # of the queue is the charge that has actually been unpaid longest —
    # not merely the most recent one — which is the whole reason a
    # reminder can name a date the patient recognises.
    #
    # The sort key is the PAIR, not postedAt alone: postedAt is whole-second
    # canonical UTC (time.rfc3339_utc), so two charges posted in the same
    # second are indistinguishable by time and the transaction key is what makes
    # the order total. Without it the op and the statement can disagree about
    # which of the two is the head, and so about the due date.
    rows = sorted(entries, key=lambda e: (e["postedAt"], e["key"]))

    debit_amount = {}
    for r in rows:
        if r["type"] == "debit":
            debit_amount[r["key"]] = r["amountCents"]
    absorbed_total = {}
    absorbed_by_credit = {}
    for r in rows:
        if r["type"] != "credit" or r["reversesKey"] == None:
            continue
        target = debit_amount.get(r["reversesKey"])
        if target == None:
            continue
        remaining = target - absorbed_total.get(r["reversesKey"], 0)
        absorbed = r["amountCents"]
        if absorbed > remaining:
            absorbed = remaining
        absorbed_by_credit[r["key"]] = absorbed
        absorbed_total[r["reversesKey"]] = absorbed_total.get(r["reversesKey"], 0) + absorbed

    open_debits = []
    surplus = 0
    balance_cents = 0
    for r in rows:
        amount = r["amountCents"]
        if r["type"] == "debit":
            balance_cents += amount
            amount -= absorbed_total.get(r["key"], 0)
            if amount <= 0:
                continue
            if surplus >= amount:
                surplus -= amount
                continue
            amount -= surplus
            surplus = 0
            open_debits.append({"postedAt": r["postedAt"], "remaining": amount})
        elif r["type"] == "credit":
            balance_cents -= amount
            remaining = amount - absorbed_by_credit.get(r["key"], 0)
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
    # which is why deriveStatement's early "balance <= 0 has nothing to age"
    # return needs no counterpart here.
    if len(open_debits) == 0:
        return None, balance_cents
    return open_debits[0]["postedAt"], balance_cents

def carry_arrears(doc):
    out = {}
    for k, v in doc.data.items():
        out[k] = v
    return out

NANOID_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

def is_clinicaccount_key(key):
    # Contract #1's whole vertex grammar for a clinicaccount, not a prefix test —
    # derive_reads returns keys the Processor validates against that grammar and
    # answers a malformed one with a DeriveReadsInvalid hydration fault raised
    # BEFORE the operation's own validation runs, which would turn this branch's
    # clean "InvalidArgument: accountKey" into an opaque hydration failure. The
    # same helper transactionDDLScript carries, for the same reason.
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

def patient_for_account(acct_key):
    # The patient this clinicaccount is held for, or None where no live heldFor
    # link resolves to a live patient -- an account whose patient has since
    # gone dead still ages its own history normally. The same heldFor walk
    # transactionDDLScript's held_for_patient_id runs, returning the whole
    # key (which is all EvaluateClinicArrears' notification needs) and
    # checking the patient is alive.
    # read-posture: (e) relation=heldFor epoch=none -- a clinicaccount carries
    # at most one heldFor link, so this is never a keyspace scan.
    page, _ = kv.Links(acct_key, "heldFor", "out", None, 1)
    patient = None
    for lk in page:
        if not lk.isDeleted:
            patient = lk.targetVertex
    if patient == None:
        return None
    # read-posture: (e) per-candidate follow-up read off the enumeration
    # above -- the patient vertex itself, data-derived and unknowable
    # client-side.
    patient_doc = kv.Read(patient)
    if patient_doc == None or patient_doc.isDeleted:
        return None
    return patient

def derive_reads(op):
    # Contract #2 §2.5 class (g), for the same reason transactionDDLScript's own
    # derive_reads exists: the .arrears write below is a bare update
    # auto-conditioned on the step-4 hydrated revision ONLY for a key the
    # operation declared (Contract #3 §3.2), and contextHint is caller-supplied
    # and never enforced. A dispatch that omitted the declaration would get a
    # live read and an UNCONDITIONED write, so two evaluations racing one
    # account (a redelivery alongside a fresh dispatch) could each decide to
    # send against the same prior state. The clinicArrearsReminders target
    # declares the same key (targets.go); that DOCUMENTS the read set, this
    # GUARANTEES it.
    #
    # optionalReads, never reads: an account carries no .arrears until
    # something opens an episode on it, and a required read's absence is a
    # HydrationMiss that would block the very first evaluation of each one.
    #
    # The account ROOT itself rides the same declaration, for a distinct
    # reason: vertex_alive(state, acct_key) below decides UnknownAccount by
    # testing acct_key not in state, which cannot tell "genuinely absent" from
    # "never declared or derived" apart. Any dispatcher that omits the root
    # from its own contextHint (Weaver's clinicArrearsReminders target and
    # every OpMetaSpec descriptor both declare it too, but neither is enforced)
    # would otherwise see a live account rejected as unknown.
    if op.operationType != "EvaluateClinicArrears":
        return {}
    acct_key = optional_string(op.payload, "accountKey")
    if not is_clinicaccount_key(acct_key):
        return {}
    return {"optionalReads": [acct_key, acct_key + ".arrears"]}

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "EvaluateClinicArrears":
        # actor-guard: (primordial) restricted to Weaver's dispatch actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind this
        # op is operator/Scope:"any", which admits every operator-role holder —
        # far wider than the one engine that dispatches the
        # clinicArrearsReminders directOp playbook. accountKey arrives off the
        # payload and the account it names is forwarded in the
        # external.notification body the bridge turns into a real message to a
        # patient, so a wider submitter set is a forged send: an arbitrary
        # operator naming any account it likes and having the platform tell that
        # patient they owe money. First statement in the branch: it also denies
        # the payload-shape, vertex-alive and history-shape oracles beneath it.
        if op.actor != primordialActor["weaver"]:
            fail("AuthDenied: EvaluateClinicArrears is restricted to Weaver's dispatch actor; got " + op.actor)

        acct_key = required_string(p, "accountKey")
        parts_of(acct_key, "accountKey", "clinicaccount")

        # Liveness guard: never mint arrears state (or a 4-segment aspect key)
        # on an absent or tombstoned account. The op hydrates [accountKey]
        # (the target's Reads), so the root is in state.
        if not vertex_alive(state, acct_key):
            fail("UnknownAccount: " + acct_key + " is absent or tombstoned; no arrears evaluated")

        # The patient this account is held for, resolved LIVE from the
        # account's own heldFor out-link -- never from the payload, so the
        # patient told they owe money cannot be forged by an arbitrary
        # submitter (the same forged-send surface the actor guard above
        # closes, one step further along). Optional: an account with no live
        # heldFor patient is still evaluated -- the arrears fact is about the
        # ACCOUNT, not the patient it happens to be held for -- and the
        # resolved patient is routed into the notification's params purely for
        # the adapter's own addressing; nothing this op decides depends on it.
        patient_key = patient_for_account(acct_key)

        # The op's own timestamp, normalized to canonical UTC so the lexical
        # compare against dueAt below is sound to the second (a raw compare
        # mis-answers for the first second after an instant, '.' sorting
        # below 'Z').
        evaluated_at = time.rfc3339_utc(op.submittedAt)

        arrears_key = acct_key + ".arrears"
        # read-posture: (d) optionalReads — derived server-side by this script's
        # own derive_reads(op) for EvaluateClinicArrears (Contract #2 §2.5 class
        # (g)), and declared statically by the clinicArrearsReminders target's
        # GapActionSpec.OptionalReads (targets.go). Two questions, not one:
        # absence decides the WRITE VERB (a create is refused against a
        # tombstone, Contract #3 §3.3, so only a genuinely absent key is
        # minted), presence-and-live decides whether there is prior episode
        # state to carry.
        arrears_doc = kv.Read(arrears_key)
        arrears_absent = arrears_doc == None
        prior = {}
        if arrears_doc != None and not arrears_doc.isDeleted:
            # The CLASS, not just the key — the doctrine post_entry applies to
            # .balance: this package is the sole writer of a .arrears aspect and
            # writes exactly that class, so a document of any other class here
            # is a fault to refuse, never state to decide a send on.
            if not hasattr(arrears_doc, "class") or getattr(arrears_doc, "class") != "clinicAccountArrears":
                fail("InvalidState: this account's arrears aspect is not a clinicAccountArrears")
            prior = carry_arrears(arrears_doc)

        entries, history_too_long = arrears_entries(acct_key)

        if history_too_long:
            # DEGRADE, never refuse. The account's history outran the replay
            # budget, so the FIFO head is unknown and no send can be justified —
            # but a rejection would be a permanent silent stop: the row's own gap
            # is the only thing that re-drives this op, and a rejected op leaves
            # it open, so Weaver would re-dispatch the same doomed evaluation
            # every window and nothing would ever be sent or seen. Recording the
            # exhaustion instead makes it OBSERVABLE and QUIET: the lens
            # suppresses both the gap and the timer on historyTooLong, so the
            # dispatch loop stops while the row stays in the weaver-targets
            # bucket for an operator to find. What was already recorded is
            # carried untouched — a reminder already sent stays recorded as sent,
            # and a due date already armed is not erased by an evaluation that
            # could not read the history. stale is dropped: it asks for a
            # recomputation this op has just attempted, and re-asking would
            # re-open the gap the degrade is closing. post_entry's own stale
            # write drops historyTooLong in turn, so the next posted entry buys
            # exactly one more attempt — bounded to one op per entry, never a
            # loop.
            data = {"evaluatedAt": evaluated_at, "historyTooLong": True}
            for carried in ["dueAt", "remindedFor", "sentAt"]:
                carried_value = prior.get(carried)
                if carried_value != None:
                    data[carried] = carried_value
            if arrears_absent:
                mutations = [make_aspect(acct_key, "arrears", "clinicAccountArrears", data)]
            else:
                mutations = [make_aspect_update(acct_key, "arrears", "clinicAccountArrears", data)]
            return {"mutations": mutations,
                    "events": [{"class": "account.arrearsEvaluated",
                                "data": {"accountKey": acct_key, "dueAt": data.get("dueAt"),
                                         "sentAt": data.get("sentAt"), "balanceCents": None,
                                         "historyTooLong": True}}],
                    "response": {"primaryKey": acct_key}}

        head_posted_at, balance_cents = arrears_head(entries)

        # evaluatedAt is written on EVERY outcome, including "owes nothing":
        # its absence is what opens the convergence gap for an account that has
        # never been evaluated (every account standing at install), so a run
        # that recorded nothing would re-dispatch forever.
        data = {"evaluatedAt": evaluated_at}
        events = []

        if head_posted_at != None:
            due_at = time.rfc3339_add(head_posted_at, ARREARS_GRACE_DURATION)
            data["dueAt"] = due_at
            reminded_for = prior.get("remindedFor")
            sent_at = prior.get("sentAt")
            if due_at <= evaluated_at:
                # The head has come due. remindedFor records THIS due date
                # whatever else happens: it is the conjunct the convergence lens
                # reads, so leaving it naming an older head would hold the gap
                # open and have Weaver re-dispatch this same evaluation forever.
                data["remindedFor"] = due_at
                # Whether anything is SENT is a different question, and its
                # answer is sentAt's absence. The unit of "one reminder" is the
                # EPISODE — the stretch from the charge that took the account
                # from square to owing, until the balance comes back to zero —
                # not the head, which a partial payment moves from one open
                # charge to the next while the patient stays continuously in
                # arrears. Keying the send off remindedFor instead would nag on
                # every part-payment: pay some, the head shifts to a charge whose
                # own term has also passed, and a second notification goes out
                # for a debt the patient is visibly paying down. sentAt is
                # carried across every write of a live episode and dropped only
                # where the episode itself ends (post_entry's zero-balance and
                # new-episode writes, and the no-open-head branch below), so its
                # absence means exactly "nothing has gone out in this episode".
                if sent_at != None:
                    data["sentAt"] = sent_at
                else:
                    data["sentAt"] = evaluated_at
                    # Keyed on (accountKey, dueAt): a redelivery of the same due
                    # episode reuses the key so the adapter dedups, while a later
                    # episode (a new head, a new due date) mints a fresh one and
                    # sends again. Fired off this op's own transactional outbox —
                    # no Loom pattern, the bridge's dispatch path is fully
                    # generic.
                    ext_ref = acct_key + ":" + due_at
                    notif_params = {"accountKey": acct_key, "reminderType": "clinicArrears",
                                    "dueAt": due_at, "balanceCents": balance_cents}
                    if patient_key != None:
                        notif_params["patientKey"] = patient_key
                    events.append({"class": "external.notification",
                                   "data": {"instanceKey": ext_ref, "adapter": "notification",
                                            "replyOp": "RecordClinicArrearsReminderNotification",
                                            "externalRef": ext_ref, "idempotencyKey": ext_ref,
                                            "params": notif_params}})
            else:
                # The head is not due yet: the timer re-arms at this dueAt, and
                # the episode's record — whichever head it was reminded for, and
                # when — is carried forward unchanged. Dropping it would re-open
                # the gate and send a second time for the same episode.
                if reminded_for != None:
                    data["remindedFor"] = reminded_for
                if sent_at != None:
                    data["sentAt"] = sent_at
        # head_posted_at == None: nothing is owed, so the episode is over and
        # {evaluatedAt} alone is written — dueAt, remindedFor, sentAt and stale
        # all go with it, which is what lets the NEXT charge open a clean
        # episode rather than inherit this one's send record.
        #
        # stale is never carried on ANY path: recomputing the head from the
        # account's own history is precisely what stale asks for, and it has
        # just happened. historyTooLong goes the same way for the same reason —
        # this evaluation read the whole history, so whatever recorded that it
        # once could not is answered, and the lens un-suppresses the row.

        if arrears_absent:
            mutations = [make_aspect(acct_key, "arrears", "clinicAccountArrears", data)]
        else:
            mutations = [make_aspect_update(acct_key, "arrears", "clinicAccountArrears", data)]

        events.append({"class": "account.arrearsEvaluated",
                       "data": {"accountKey": acct_key, "dueAt": data.get("dueAt"),
                                "sentAt": data.get("sentAt"), "balanceCents": balance_cents}})
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": acct_key}}

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
//
// The other maintained fact is the account's .arrears episode state. It is
// deliberately COARSE — post_entry records only "a new arrears episode opened
// here" (a charge against an account that owed nothing, whose own postedAt
// therefore IS the FIFO head), "nothing is owed any more", or "what was recorded
// no longer describes this account" (the stale mark). It never tries to move a
// head a payment shifted: which charge is now oldest-and-open is a function of
// the whole history, not of the entry being posted, so that recomputation
// belongs to EvaluateClinicArrears (accountDDLScript), which the stale mark asks
// for. A legacy account — one carrying no .balance, so this op has no before/
// after balance to reason with — can only ever mark existing state stale; it
// never mints arrears state off a number it does not have.
var transactionDDLScript = arrearsGracePrelude + transactionDDLScriptBody

const transactionDDLScriptBody = `
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

def carry_arrears(doc):
    # Every field of the account's recorded arrears state, copied forward, with
    # ONE exception. The marks post_entry adds are ADDITIONS to that state, never
    # a rewrite of it: the send record (remindedFor, sentAt) is what stops a
    # second notification going out for an episode already reminded for, and
    # dropping it while marking the state stale would send twice for one debt.
    #
    # historyTooLong is the exception, and only this script drops it. It records
    # that an evaluation could not read the account's history inside its replay
    # budget, and the lens holds the row QUIET while it stands — no gap, no
    # timer. Carrying it across a posted entry would make that quiet permanent
    # for the life of the account. Dropping it here is what buys exactly one more
    # attempt per entry: this write also sets stale, so the gap re-opens once,
    # the evaluation runs once, and if the history is still too long it records
    # the mark again and the row goes quiet again. One op per entry, never a
    # loop.
    out = {}
    for k, v in doc.data.items():
        if k == "historyTooLong":
            continue
        out[k] = v
    return out

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

def held_for_patient_id(acct_key):
    # The NanoID of the patient this account is held for, walked off the
    # account's OWN heldFor topology (never the payload), or None when the
    # account carries no live patient. Two callers, one predicate: the self-pay
    # ownership proof and the visitRef patient check.
    # read-posture: (e) relation=heldFor epoch=none -- an account carries
    # exactly one heldFor link, so this is never a keyspace scan.
    held_for_page, _ = kv.Links(acct_key, "heldFor", "out", None, 1)
    patient_key = None
    for lk in held_for_page:
        if not lk.isDeleted:
            patient_key = lk.targetVertex
    if patient_key == None:
        return None
    _, patient_id = parts_of(patient_key, "heldFor target", "patient")
    return patient_id

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
        # A reverses link is what disarms missing_reversal: a patient who could
        # name their own no-show fee here would mark it reversed by paying it,
        # and a later status correction would then give nothing back.
        if hasattr(p, "reversesRef") and getattr(p, "reversesRef") != None:
            fail("AuthDenied: a patient may only pay down their own account, not reverse a charge")
        # authcontext-target: (ownership) the value derives an identity whose
        # ownership of the account's own patient is then proven by the
        # identifiedBy link read below -- a forged target only fails closed.
        # The patient is recovered from the account's OWN heldFor topology,
        # never the payload, so a forged claim only fails closed.
        _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
        patient_id = held_for_patient_id(acct_key)
        if patient_id == None:
            fail("AuthDenied: account " + acct_key + " carries no live patient")
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

    # The account's .arrears episode state (class clinicAccountArrears,
    # ddls.go), read here beside .balance; which branch it takes is decided
    # below the .balance write, because every branch needs this entry's own
    # postedAt and the balance it leaves behind.
    arrears_key = acct_key + ".arrears"
    # read-posture: (d) optionalReads — derived server-side by this script's own
    # derive_reads(op) for both entry ops (Contract #2 §2.5 class (g)), and
    # declared statically by opmetas.go's OpDispatchSpec.OptionalReads +
    # targets.go's GapActionSpec.OptionalReads. Absence-tolerant: no account
    # carries .arrears until something opens an episode on it.
    arrears_doc = kv.Read(arrears_key)
    # Two questions again, the .balance split exactly: absence picks the WRITE
    # VERB (a create is refused against a tombstone, Contract #3 §3.3, so only a
    # genuinely absent key is minted and a tombstoned one is revived by the
    # update), presence-and-live decides whether there is state to carry.
    arrears_absent = arrears_doc == None
    arrears_live = arrears_doc != None and not arrears_doc.isDeleted
    if arrears_live:
        # The CLASS, not just the key — the doctrine .balance applies: this
        # package is the sole writer of a .arrears aspect and writes exactly
        # that class.
        if not hasattr(arrears_doc, "class") or getattr(arrears_doc, "class") != "clinicAccountArrears":
            fail("InvalidState: this account's arrears aspect is not a clinicAccountArrears")

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
            fail("NoBalanceToPay: this account has no outstanding balance to pay")
        if amount_cents > owed_cents:
            fail("PaymentExceedsBalance: a payment of " + dollars(amount_cents) +
                 " exceeds the outstanding balance of " + dollars(owed_cents))

    # Two ways a charge names an appointment, never both (ClinicDebitAccount
    # only -- a credit has no visit to name):
    #   appointmentRef -- this transaction IS the fee the appointment's status
    #     carries (the settles link clinicNoShowSettlement reads to converge its
    #     missing_charge / missing_reversal gaps).
    #   visitRef -- this transaction is FOR the visit (a copay, a procedure), a
    #     forVisit link no convergence lens walks; only clinicLedgerHistory
    #     projects it so the history can say which visit a line concerns.
    # A settles link is only ever written while the appointment's CURRENT
    # status carries a fee: missing_reversal reads "fee-less status AND a live
    # settles link" as a correction that owes a reversal, so a settles link
    # minted against a fee-less appointment would be credited straight back.
    # The .status read is a declared optionalRead this script's own
    # derive_reads hydrates whenever the payload carries a well-formed
    # appointment key, so absence here means no fee (CreateAppointment always
    # writes .status) -- refused, never assumed.
    appt_key = None
    appt_id = None
    visit_key = None
    visit_id = None
    has_visit_ref = hasattr(p, "visitRef") and getattr(p, "visitRef") != None
    if allow_appointment_ref:
        appt_key = optional_string(p, "appointmentRef")
        visit_key = optional_string(p, "visitRef")
        if appt_key != None and visit_key != None:
            fail("InvalidArgument: visitRef/appointmentRef: a charge is either the fee an appointment carries (appointmentRef) or for a visit (visitRef), not both")
        if appt_key != None:
            _, appt_id = parts_of(appt_key, "appointmentRef", "appointment")
            if not vertex_alive(state, appt_key):
                fail("UnknownAppointment: " + appt_key)
            # read-posture: (d) optionalReads -- derived server-side by this
            # script's own derive_reads(op) for a well-formed appointmentRef
            # (Contract #2 §2.5 class (g)), and declared statically by
            # targets.go's missing_charge GapActionSpec.OptionalReads.
            status_doc = kv.Read(appt_key + ".status")
            fee_cents = None
            if status_doc != None and not status_doc.isDeleted:
                fee_cents = status_doc.data.get("noShowFeeCents")
            if fee_cents == None or (type(fee_cents) != type(0) and type(fee_cents) != type(0.0)) or fee_cents <= 0:
                fail("NoFeeToSettle: " + appt_key + " carries no fee in its current status; a charge for a visit names it through visitRef")
        if visit_key != None:
            _, visit_id = parts_of(visit_key, "visitRef", "appointment")
            if not vertex_alive(state, visit_key):
                fail("UnknownAppointment: " + visit_key)
            # The visit must be THIS account's patient's: a charge that named a
            # stranger's appointment would put that visit on this patient's
            # statement. The patient comes from the account's own heldFor walk,
            # never the payload.
            visit_patient_id = held_for_patient_id(acct_key)
            if visit_patient_id == None:
                fail("WrongPatient: account " + acct_key + " carries no live patient")
            # read-posture: (e) per-candidate follow-up read off the heldFor
            # enumeration in held_for_patient_id -- the patient id is
            # data-derived, unknowable client-side.
            for_patient = kv.Read("lnk.appointment." + visit_id + ".forPatient.patient." + visit_patient_id)
            if for_patient == None or for_patient.isDeleted:
                fail("WrongPatient: visitRef " + visit_key + " is not this patient's visit")
    else:
        if has_visit_ref:
            fail("InvalidArgument: visitRef: only valid on a debit (charge), not a credit (payment/waiver)")
        if hasattr(p, "appointmentRef") and getattr(p, "appointmentRef") != None:
            fail("InvalidArgument: appointmentRef: only valid on a debit (charge), not a credit (payment/waiver)")

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
            # The reversed charge must be posted to THIS account: a credit on
            # one account naming a debit on another would retire that debit
            # from a statement it never paid. The postedTo link key is
            # deterministic from the two payload keys.
            # read-posture: (d) optionalReads -- derived server-side by this
            # script's own derive_reads(op) for a well-formed reversesRef
            # (Contract #2 §2.5 class (g)); the Weaver missing_reversal
            # dispatch cannot template a two-column link key, so the
            # derivation is what declares it there.
            posted_to = kv.Read("lnk.clinictransaction." + reverses_id + ".postedTo.clinicaccount." + acct_id)
            if posted_to == None or posted_to.isDeleted:
                fail("WrongAccount: reversesRef " + reverses_key + " is not posted to this account")
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
    # .entry aspect; what this DDL mutates on the ACCOUNT is its .balance cache
    # and its .arrears episode state, both appended just below.
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
    new_balance_cents = None
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

    # The .arrears episode branches, decided on the balance this entry leaves.
    arrears_data = None
    if balance_cents == None:
        # LEGACY account (no .balance, and this op did not backfill one): there
        # is no before/after balance, so it cannot tell an episode opening from
        # an episode continuing. It marks what already exists STALE — which is
        # a request for EvaluateClinicArrears to recompute the head from the
        # account's own history — and mints nothing where nothing exists, since
        # a legacy account with no arrears state is already opening the
        # never-evaluated gap.
        if arrears_live:
            arrears_data = carry_arrears(arrears_doc)
            arrears_data["stale"] = True
    elif entry_type == "debit":
        if balance_cents <= 0 and new_balance_cents > 0:
            # A NEW episode. The account owed nothing before this charge and
            # owes something after it, so this charge is itself the FIFO-oldest
            # open debit and its own postedAt starts the net term — the one case
            # a posted entry can name the head without replaying anything. Every
            # field of the finished episode (remindedFor, sentAt, stale) is
            # dropped with the rewrite, which is what lets this episode be
            # reminded for on its own merits rather than inheriting the last
            # one's send record.
            arrears_data = {"dueAt": time.rfc3339_add(posted_at, ARREARS_GRACE_DURATION),
                            "evaluatedAt": posted_at}
        # Nothing is written on either of the other two shapes:
        #
        #   - balance_cents > 0: the head is an older charge still open and this
        #     charge queues behind it. A debit that re-stamped dueAt would push
        #     the due date of a balance outstanding for weeks back to today.
        #   - new_balance_cents <= 0: the account is IN CREDIT and stays there —
        #     an over-waiver or a reversal of a paid charge left it owing less
        #     than nothing, and this charge only eats into that credit. Under
        #     the FIFO the surplus prepays this charge outright, so there is no
        #     open debit and no head at all; minting an episode here would arm a
        #     timer that reminds a patient about money they do not owe.
    elif new_balance_cents <= 0:
        # Paid off (or into credit). The episode is over: {evaluatedAt} alone,
        # dropping dueAt/remindedFor/sentAt/stale, so no timer stays armed and
        # the next charge opens a clean episode.
        arrears_data = {"evaluatedAt": posted_at}
    elif arrears_live:
        # A PARTIAL payment (or a waiver / reversal that left a balance): what
        # is recorded may no longer describe this account — the FIFO head can
        # have moved to a later charge with a later due date, which this op
        # cannot compute. Carry every field and mark it stale; the convergence
        # lens reads stale as an open gap and Weaver dispatches the
        # recomputation.
        arrears_data = carry_arrears(arrears_doc)
        arrears_data["stale"] = True

    if arrears_data != None:
        if arrears_absent:
            mutations.append(make_aspect(acct_key, "arrears", "clinicAccountArrears", arrears_data))
        else:
            mutations.append(make_aspect_update(acct_key, "arrears", "clinicAccountArrears", arrears_data))

    # settles: the transaction (later-arriving) is the source, the
    # pre-existing appointment is the target (Contract #1 §1.1). Only
    # written when the caller supplied appointmentRef — a plain
    # human-submitted ClinicDebitAccount is unaffected. The clinicNoShowSettlement
    # lens walks this link to converge the no-show-fee gap once posted.
    if appt_key != None:
        settles_lnk = "lnk.clinictransaction." + tx_id + ".settles.appointment." + appt_id
        mutations.append(make_link(settles_lnk, tx_key, appt_key, "settles", "settles", {}))

    # forVisit: the transaction (later-arriving) is the source, the
    # pre-existing appointment is the target (Contract #1 §1.1) -- "this
    # transaction is for this visit". Only written when the caller supplied
    # visitRef. clinicLedgerHistory projects it as the line's appointmentKey /
    # visitStartsAt (settlesFee false); no convergence lens reads it.
    if visit_key != None:
        for_visit_lnk = "lnk.clinictransaction." + tx_id + ".forVisit.appointment." + visit_id
        mutations.append(make_link(for_visit_lnk, tx_key, visit_key, "forVisit", "forVisit", {}))

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

def is_vertex_key_of(key, vtx_type):
    # Contract #1's whole vertex grammar for one vertex type, not a prefix test.
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
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != vtx_type:
        return False
    if len(parts[2]) != 20:
        return False
    for ch in parts[2].elems():
        if ch not in NANOID_ALPHABET:
            return False
    return True

def is_clinicaccount_key(key):
    return is_vertex_key_of(key, "clinicaccount")

def is_appointment_key(key):
    return is_vertex_key_of(key, "appointment")

def is_clinictransaction_key(key):
    return is_vertex_key_of(key, "clinictransaction")

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
    # .arrears rides the same declaration for the same two reasons: post_entry's
    # own episode write is a bare update that is only auto-conditioned because
    # the key is declared, and the aspect is absent on every account until
    # something opens an episode on it.
    #
    # The same channel carries a ClinicDebitAccount's appointmentRef .status
    # (post_entry refuses NoFeeToSettle unless that aspect carries a fee) and a
    # ClinicCreditAccount's reversesRef postedTo link (WrongAccount unless the
    # charge is on this account): guards whose reads the submitter could leave
    # undeclared would fall through to live reads the caller never
    # conditioned. optionalReads because the refusal is what absence means --
    # a HydrationMiss would say the same thing less clearly.
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
    # The account ROOT rides the same declaration: post_entry's
    # vertex_alive(state, acct_key) decides UnknownAccount by testing acct_key
    # not in state, which cannot tell "genuinely absent" from "never declared
    # or derived" apart, so an undeclared submitter would see a live account
    # refused as unknown.
    optional_reads = [acct_key, acct_key + ".balance", acct_key + ".arrears"]
    if ot == "ClinicDebitAccount":
        appt_key = optional_string(op.payload, "appointmentRef")
        if is_appointment_key(appt_key):
            optional_reads.append(appt_key + ".status")
    else:
        # The postedTo link post_entry reads to prove reversesRef is a charge on
        # THIS account (WrongAccount) -- deterministic from the two payload
        # keys, so it is declared here rather than read live.
        reverses_key = optional_string(op.payload, "reversesRef")
        if is_clinictransaction_key(reverses_key):
            optional_reads.append("lnk.clinictransaction." + reverses_key.split(".")[2] +
                                  ".postedTo.clinicaccount." + acct_key.split(".")[2])
    return {"optionalReads": optional_reads}

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
