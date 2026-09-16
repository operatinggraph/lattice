package semanticcontracts

import (
	"strings"

	loftspaceledger "github.com/operatinggraph/lattice/packages/loftspace-ledger"
)

// clauseDDLScript handles CreateClause + InspectPremises + SupersedeClause +
// BackfillClauseTerm + ShortenClauseTerm. Known-key reads only (validates the
// lease/account/inspector/conditionedOn/superseded-clause vertex by the keys
// the caller lists in ContextHint.Reads; SupersedeClause reads the amended
// clause's .status the same way (required — only an active clause is
// superseded) and its .terms whenever the payload names no purpose, to
// inherit its token; BackfillClauseTerm reads the
// clause's .terms and the lease's .tenancy the same way, and its .status as
// an absence-tolerant OptionalRead; ShortenClauseTerm reads the clause's
// .terms and the lease's .notice as REQUIRED declared reads, and the
// clause's .status the same absence-tolerant way). Root data stays {} on the
// clause (D5): the prose/terms/status/inspection are aspects, the governed
// lease, charged account, assigned inspector, condition, and amended
// predecessor are links.
//
// The untermed recurring window (loftspace-ledger's RecurringChargePeriod)
// is baked in at package-init time via strings.Replace: BackfillClauseTerm
// recovers the last charge instant of an untermed clause from its recorded
// due date, which DebitAccount's untermed branch always stamps as
// postedAt + that window.
var clauseDDLScript = strings.Replace(`
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

def make_vtx_tombstone(key, cls):
    # Soft-delete a vertex (isDeleted=True). UNCONDITIONED — mirrors
    # lease-signing's withdraw tombstone: nothing else writes the clause
    # ROOT (DebitAccount/InspectPremises only ever touch its aspects), so no
    # concurrent writer races this. The clauseSatisfaction lens anchors on
    # the clause and the platform's anchor-tombstone retraction deletes its
    # row once the vertex is tombstoned (refractor.md, 679fe25) — no cypher
    # isDeleted filter needed. Root data stays {} (D5).
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": True, "data": {}}}

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

PURPOSE_FIRST_CHARS = "abcdefghijklmnopqrstuvwxyz"
PURPOSE_REST_CHARS = PURPOSE_FIRST_CHARS + "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
PURPOSE_MAX_LEN = 32

def optional_purpose(p):
    # The clause's purpose token — ^[a-z][a-zA-Z0-9]{0,31}$, spelled out
    # character by character because the sandbox ships no regex builtin: a
    # lower-case first letter, alphanumerics after it, 32 at most. Absent or
    # None means the clause carries no purpose; anything else that is not a
    # token is refused rather than dropped, since a lens reads the recorded
    # token as the fact of what the clause is FOR.
    if not hasattr(p, "purpose"):
        return None
    v = getattr(p, "purpose")
    if v == None:
        return None
    if type(v) != type(""):
        fail("InvalidArgument: purpose: required token ^[a-z][a-zA-Z0-9]{0,31}$")
    if len(v) == 0 or len(v) > PURPOSE_MAX_LEN:
        fail("InvalidArgument: purpose: required token ^[a-z][a-zA-Z0-9]{0,31}$, got " + v)
    for i, ch in enumerate(v.elems()):
        if i == 0:
            if ch not in PURPOSE_FIRST_CHARS:
                fail("InvalidArgument: purpose: required token ^[a-z][a-zA-Z0-9]{0,31}$, got " + v)
        elif ch not in PURPOSE_REST_CHARS:
            fail("InvalidArgument: purpose: required token ^[a-z][a-zA-Z0-9]{0,31}$, got " + v)
    return v

def whole_cents(v, name):
    # A ledger amount is an integer number of cents. A lens-computed figure
    # arrives as a float (leaseRentSettlement's dollars×100 conversion is
    # float64 arithmetic, so 1500.00 × 100 can land as 150000.00000000001):
    # within a millionth of an integer it IS that integer; anything further
    # off is a fractional cent, refused rather than silently rounded.
    if type(v) == type(0):
        return v
    nearest = int(v + 0.5)
    d = v - nearest
    if d < 0:
        d = -d
    if d > 0.000001:
        fail("InvalidArgument: " + name + ": must be a whole number of cents")
    return nearest

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

def period_index(valid_from, d):
    # The index k of the calendar-month period of a term beginning at
    # valid_from that contains the instant d: the LARGEST k >= 0 with
    # rfc3339_add_months(valid_from, k) <= d. Every anniversary is computed
    # from valid_from (never by stepping +1 month from the last one), so
    # Jan 31 -> Feb 28 -> Mar 31 never drifts. The year/month digits of the
    # canonical RFC3339 strings give the candidate index; day-of-month
    # clamping can leave it one step off in either direction, which one
    # correction settles. d must be >= valid_from.
    k = (int(d[0:4]) - int(valid_from[0:4])) * 12 + (int(d[5:7]) - int(valid_from[5:7]))
    if k < 0:
        k = 0
    if time.rfc3339_add_months(valid_from, k) > d:
        k = k - 1
    elif time.rfc3339_add_months(valid_from, k + 1) <= d:
        k = k + 1
    if k < 0:
        k = 0
    return k

def next_anniversary_after(valid_from, d):
    # The start of the period after the one containing d.
    return time.rfc3339_add_months(valid_from, period_index(valid_from, d) + 1)

def mint_clause(state, p, inherited_purpose):
    # Shared by CreateClause and SupersedeClause (Fire V4): builds a fresh
    # clause vertex + its aspects/links from the same payload shape. Returns
    # {"clause_key", "clause_id", "mutations", "event_data"} — the caller
    # decides the event class and whether to fold in amendment mutations.
    # inherited_purpose is the amended clause's own token, used when the
    # payload names none (SupersedeClause); None everywhere else.
    lease_key = required_string(p, "leaseAppKey")
    _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")
    prose = required_string(p, "prose")
    kind = optional_string(p, "kind")
    if kind == None:
        kind = "computational"
    if kind != "computational" and kind != "judgment":
        fail("InvalidArgument: kind: must be computational or judgment, got " + kind)

    if not vertex_alive(state, lease_key):
        fail("UnknownLeaseApplication: " + lease_key)

    # period (Fire V3): computational-only recurrence selector. A
    # prorated amount (rateCents/periodDays/daysOccupied) is one-time-only
    # — recurring proration is not a shape this fire builds.
    period = optional_string(p, "period")
    if period == None:
        period = "oneTime"
    if period != "oneTime" and period != "monthly":
        fail("InvalidArgument: period: must be oneTime or monthly, got " + period)
    if period == "monthly" and kind != "computational":
        fail("InvalidArgument: period: monthly recurrence is computational-only")

    # The term: validFrom/validUntil, both or neither, canonical UTC, a
    # monthly-only fact (a oneTime clause bills once and has no period grid
    # to lay over a term). A monthly clause minted without one keeps the
    # untermed cadence until BackfillClauseTerm stamps its term from the
    # governed lease's .tenancy.
    valid_from = optional_string(p, "validFrom")
    valid_until = optional_string(p, "validUntil")
    if (valid_from == None) != (valid_until == None):
        fail("InvalidArgument: validFrom/validUntil: both or neither")
    if valid_from != None:
        if period != "monthly":
            fail("InvalidArgument: validFrom: a term is monthly-only")
        valid_from = time.rfc3339_utc(valid_from)
        valid_until = time.rfc3339_utc(valid_until)
        if valid_until <= valid_from:
            fail("InvalidArgument: validUntil: must be after validFrom")

    cond_key = optional_string(p, "conditionedOnKey")
    cond_type = None
    cond_id = None
    if cond_key != None:
        cond_type, cond_id = parts_of(cond_key, "conditionedOnKey", "")
        if not vertex_alive(state, cond_key):
            fail("UnknownConditionVertex: " + cond_key)

    # conditioned is an explicit data flag, not inferred from link/target
    # liveness: a tombstoned conditionedOn TARGET makes the lens's cond
    # match resolve null exactly like "never conditioned" would, so only
    # this flag lets the lens tell the two apart (see lenses.go).
    terms_data = {"kind": kind, "period": period, "conditioned": (cond_key != None)}
    if valid_from != None:
        terms_data["validFrom"] = valid_from
        terms_data["validUntil"] = valid_until
    # purpose: the token a lens tells a purpose-built clause apart by — the
    # role period=monthly + conditioned<>true plays for the rent clause, made
    # explicit for a clause whose period alone is not a recognizable mark
    # (leaseRentSettlement's deposit gaps read purpose='deposit'). Recorded
    # only when supplied: a clause minted without one carries no purpose key.
    purpose = optional_purpose(p)
    if purpose == None:
        purpose = inherited_purpose
    if purpose != None:
        terms_data["purpose"] = purpose
    # The security deposit is ONE shape: a oneTime computational clause. A
    # monthly clause completes on its final period and a judgment clause
    # charges nothing, so a "deposit" of either kind could never be returned
    # for what was collected — the shape is refused here rather than left for
    # ReturnDeposit to refuse after the lens has picked it.
    if purpose == "deposit" and (period != "oneTime" or kind != "computational"):
        fail("InvalidArgument: purpose: deposit is a oneTime computational clause; got period " + period + ", kind " + kind)
    acct_key = None
    acct_id = None
    amount_cents = None
    insp_key = None
    insp_id = None

    if kind == "computational":
        acct_key = required_string(p, "accountKey")
        _, acct_id = parts_of(acct_key, "accountKey", "account")
        if not vertex_alive(state, acct_key):
            fail("UnknownAccount: " + acct_key)

        # Fire V3 proration: rateCents+periodDays+daysOccupied replace a
        # flat amountCents. int(...) forces genuine Starlark bignum
        # integers (never floats) before the multiply/floor-divide, so the
        # result is EXACT — no float64 rounding hazard (the design's §7/R2
        # money-precision rule; this is the "compute Processor-side"
        # option, done once here rather than per-debit).
        has_rate = hasattr(p, "rateCents") and getattr(p, "rateCents") != None
        if has_rate:
            if period != "oneTime":
                fail("InvalidArgument: rateCents: proration is one-time only; do not combine with a recurring period")
            rate_cents = int(require_number(p, "rateCents"))
            period_days = int(require_number(p, "periodDays"))
            days_occupied = int(require_number(p, "daysOccupied"))
            if rate_cents <= 0:
                fail("InvalidArgument: rateCents: required positive number")
            if period_days <= 0:
                fail("InvalidArgument: periodDays: required positive number")
            if days_occupied <= 0 or days_occupied > period_days:
                fail("InvalidArgument: daysOccupied: required positive number, at most periodDays")
            amount_cents = (rate_cents * days_occupied) // period_days
            if amount_cents <= 0:
                fail("InvalidArgument: rateCents/periodDays/daysOccupied: computed amountCents must be positive")
            terms_data["basis"] = "daysOccupied"
            terms_data["rateCents"] = rate_cents
            terms_data["periodDays"] = period_days
            terms_data["daysOccupied"] = days_occupied
        else:
            amount_cents = require_number(p, "amountCents")
            if amount_cents <= 0:
                fail("InvalidArgument: amountCents: required positive number")
            amount_cents = whole_cents(amount_cents, "amountCents")
        terms_data["amountCents"] = amount_cents
    else:
        insp_key = required_string(p, "inspectorKey")
        _, insp_id = parts_of(insp_key, "inspectorKey", "identity")
        if not vertex_alive(state, insp_key):
            fail("UnknownIdentity: " + insp_key)

    clause_id = nanoid.new()
    clause_key = "vtx.clause." + clause_id

    # Every link the clause writes has the clause as source: it is the
    # later-arriving vertex in each pair (Contract #1 §1.1).
    governs_lnk = "lnk.clause." + clause_id + ".governs.leaseapp." + lease_id

    mutations = [
        make_vtx(clause_key, "clause", {}),
        make_aspect(clause_key, "prose", "clauseProse", {"text": prose}),
        make_aspect(clause_key, "terms", "clauseTerms", terms_data),
        make_aspect(clause_key, "status", "clauseStatus", {"state": "active"}),
        make_link(governs_lnk, clause_key, lease_key, "governs", "governs", {}),
    ]
    event_data = {"clauseKey": clause_key, "leaseAppKey": lease_key, "kind": kind}
    if valid_from != None:
        event_data["validFrom"] = valid_from
        event_data["validUntil"] = valid_until
    if purpose != None:
        event_data["purpose"] = purpose

    if kind == "computational":
        charges_lnk = "lnk.clause." + clause_id + ".chargesTo.account." + acct_id
        mutations.append(make_link(charges_lnk, clause_key, acct_key, "chargesTo", "chargesTo", {}))
        event_data["accountKey"] = acct_key
        event_data["amountCents"] = amount_cents
    else:
        insp_lnk = "lnk.clause." + clause_id + ".requiresInspectionBy.identity." + insp_id
        mutations.append(make_link(insp_lnk, clause_key, insp_key, "requiresInspectionBy", "requiresInspectionBy", {}))
        event_data["inspectorKey"] = insp_key

    if cond_key != None:
        cond_lnk = "lnk.clause." + clause_id + ".conditionedOn." + cond_type + "." + cond_id
        mutations.append(make_link(cond_lnk, clause_key, cond_key, "conditionedOn", "conditionedOn", {}))
        event_data["conditionedOnKey"] = cond_key

    return {"clause_key": clause_key, "clause_id": clause_id,
            "mutations": mutations, "event_data": event_data}

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateClause":
        minted = mint_clause(state, p, None)
        events = [{"class": "clause.created", "data": minted["event_data"]}]
        return {"mutations": minted["mutations"], "events": events,
                "response": {"primaryKey": minted["clause_key"]}}

    if ot == "SupersedeClause":
        # Fire V4 self-amendment: mint a replacement clause (same shape as
        # CreateClause) exactly like above, then atomically tombstone the
        # amended clause and link the new one to it. "amends" reads as
        # "the new clause amends the old clause" — the new clause is the
        # later-arriving vertex, so it is the source (Contract #1 §1.1).
        old_key = required_string(p, "clauseKey")
        _, old_id = parts_of(old_key, "clauseKey", "clause")
        if not vertex_alive(state, old_key):
            # Also catches a clause already superseded (its tombstone makes
            # it not-alive), so a clause can only be amended once at a time.
            fail("UnknownClause: " + old_key)

        # Only an ACTIVE clause is superseded. A completed clause has been
        # charged (DebitAccount's one-time write) and a returned one has been
        # credited back: re-minting either as a fresh active clause would bill
        # it again through clauseSatisfaction — a second deposit collected —
        # and rewriting its status would drop the record of the charge. The
        # amended clause's .status is a REQUIRED declared read: CreateClause
        # writes it unconditionally, so its absence here means the dispatcher
        # never declared it, never that the clause is new.
        old_status_key = old_key + ".status"
        if not (old_status_key in state and vertex_alive(state, old_status_key)):
            fail("InvalidState: " + old_key + "'s .status was not hydrated; declare it in reads")
        old_status_doc = state[old_status_key]
        old_state = old_status_doc.data.get("state")
        if old_state != "active":
            fail("ClauseNotActive: " + old_key + " is " + str(old_state) + "; only an active clause can be superseded")

        # The purpose token is inherited and may not CHANGE: the amended
        # clause's own token, read from its hydrated .terms (a REQUIRED
        # declared read — mint_clause writes .terms unconditionally, so its
        # absence here means the dispatcher never declared it), is what the
        # replacement carries. A payload that names none inherits it; one
        # that names the same token is accepted; any difference — tagging an
        # untagged clause deposit, re-tagging a deposit as a fee — is refused,
        # because the token is what leaseRentSettlement reads the clause back
        # by: dropping it would mint a second deposit, adding it would make a
        # fee returnable. A clause with a different purpose is a new clause.
        old_terms_key = old_key + ".terms"
        if not vertex_alive(state, old_terms_key):
            fail("InvalidState: " + old_key + "'s .terms was not hydrated; declare it in reads")
        inherited = state[old_terms_key].data.get("purpose")
        supplied = optional_purpose(p)
        if supplied != None and supplied != inherited:
            recorded = inherited
            if recorded == None:
                recorded = "none"
            fail("InvalidArgument: purpose: a superseding clause keeps the amended clause's purpose (" + recorded + "); mint a new clause to change it")
        minted = mint_clause(state, p, inherited)
        new_key = minted["clause_key"]
        new_id = minted["clause_id"]

        amends_lnk = "lnk.clause." + new_id + ".amends.clause." + old_id
        superseded_at = time.rfc3339_utc(op.submittedAt)
        # The superseded mark is pinned to the revision the active check read:
        # a DebitAccount completing this clause between hydration and commit
        # (the charge that would make it un-amendable) must conflict with this
        # write rather than be overwritten by a status computed from the
        # pre-charge state — the ShortenClauseTerm / EndTenancy OCC shape. The
        # trade of an EXPLICIT pin: commit_path.go's applyHydratedRevisions
        # (:682-683) skips a mutation carrying its own expectedRevision, so it
        # is not in the defaulted set the §3.2 re-hydrate retry replays — a
        # conflict is a terminal rejection, not a retry. Chosen on purpose: a
        # replay would re-run the active check against the charged clause and
        # refuse anyway, and the loser must never be re-applied as a second
        # amendment; the submitter re-reads and decides.
        mutations = minted["mutations"] + [
            make_link(amends_lnk, new_key, old_key, "amends", "amends", {}),
            make_vtx_tombstone(old_key, "clause"),
            {"op": "update", "key": old_status_key, "expectedRevision": old_status_doc.revision,
             "document": {"class": "clauseStatus", "isDeleted": False,
                          "vertexKey": old_key, "localName": "status",
                          "data": {"state": "superseded", "supersededAt": superseded_at, "supersededBy": new_key}}},
        ]
        events = [
            {"class": "clause.superseded", "data": {"clauseKey": old_key, "supersededBy": new_key}},
            {"class": "clause.created", "data": minted["event_data"]},
        ]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": new_key}}

    if ot == "BackfillClauseTerm":
        # Stamps the term a monthly clause minted without one, from the
        # governed lease's own .tenancy: validFrom = leaseStart, validUntil =
        # termStart when the lease has been renewed (the clause covers the
        # ORIGINAL term only; the renewal's own clause covers the rest) else
        # leaseEnd. Dispatched by the leaseRentSettlement playbook's
        # missing_term gap.
        clause_key = required_string(p, "clauseKey")
        parts_of(clause_key, "clauseKey", "clause")
        lease_key = required_string(p, "leaseAppKey")
        _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")

        if not vertex_alive(state, clause_key):
            fail("UnknownClause: " + clause_key)
        terms_key = clause_key + ".terms"
        if not vertex_alive(state, terms_key):
            fail("InvalidState: " + clause_key + " has no .terms aspect")
        terms = state[terms_key].data
        if terms.get("period") != "monthly":
            fail("InvalidState: " + clause_key + " is not a monthly clause; a term is monthly-only")
        if terms.get("kind") != "computational":
            fail("InvalidState: " + clause_key + " is not a computational clause")
        if terms.get("validFrom") != None:
            fail("AlreadyTermed: " + clause_key + " already carries a term from " + terms.get("validFrom"))

        tenancy_key = lease_key + ".tenancy"
        if not vertex_alive(state, tenancy_key):
            fail("InvalidState: " + lease_key + " has no .tenancy aspect to term the clause from")
        tenancy = state[tenancy_key].data
        lease_start = tenancy.get("leaseStart")
        lease_end = tenancy.get("leaseEnd")
        term_start = tenancy.get("termStart")
        if lease_start == None or lease_end == None:
            fail("InvalidState: " + lease_key + "'s .tenancy is missing leaseStart/leaseEnd")
        valid_from = time.rfc3339_utc(lease_start)
        if term_start != None:
            valid_until = time.rfc3339_utc(term_start)
        else:
            valid_until = time.rfc3339_utc(lease_end)
        if valid_until <= valid_from:
            fail("InvalidState: " + lease_key + "'s term ends at " + valid_until + ", not after its start " + valid_from)

        # The .terms update preserves every key the clause already carries
        # (amountCents, conditioned, the proration audit trail) and adds the
        # two term keys. The Processor pins it to the revision the declared
        # read hydrated.
        new_terms = {}
        for k, v in terms.items():
            new_terms[k] = v
        new_terms["validFrom"] = valid_from
        new_terms["validUntil"] = valid_until

        # The recorded due date moves onto the term's anniversary grid so the
        # next charge bills the period after the one the last charge covered.
        # An untermed clause's due is always the last charge's instant plus
        # the untermed recurring window (DebitAccount's untermed branch is
        # the only writer of it), so that instant is recovered exactly, and
        # the period CONTAINING it is the one already billed. Before the term
        # starts (every prior charge was pre-term), or never charged: the
        # term's first period is due at validFrom. Otherwise the anniversary
        # after the containing period's start, capped at validUntil (a
        # charge inside the final period leaves nothing to bill). Every other
        # status key is kept. mint_clause writes .status unconditionally, so
        # its absence here can only mean the dispatcher did not declare it —
        # a due date read from nothing would rewind the clause to its first
        # period, so that fails closed.
        status_key = clause_key + ".status"
        if not (status_key in state and vertex_alive(state, status_key)):
            fail("InvalidState: " + clause_key + "'s .status was not hydrated; declare it in optionalReads")
        status_data = {}
        for k, v in state[status_key].data.items():
            status_data[k] = v
        prev_due = status_data.get("chargeValidUntil")
        if prev_due == None:
            new_due = valid_from
        else:
            last_charge = time.rfc3339_add(prev_due, "-__UNTERMED_WINDOW__")
            if last_charge < valid_from:
                new_due = valid_from
            else:
                new_due = next_anniversary_after(valid_from, last_charge)
                if new_due > valid_until:
                    new_due = valid_until
        status_data["chargeValidUntil"] = new_due
        # A due at the term's end means the final period is already billed:
        # the clause is complete, the same mark DebitAccount leaves when its
        # own charge bills the final period.
        if new_due >= valid_until:
            status_data["state"] = "completed"
            status_data["completedAt"] = time.rfc3339_utc(op.submittedAt)

        mutations = [
            {"op": "update", "key": terms_key,
             "document": {"class": "clauseTerms", "isDeleted": False,
                          "vertexKey": clause_key, "localName": "terms", "data": new_terms}},
            {"op": "update", "key": status_key,
             "document": {"class": "clauseStatus", "isDeleted": False,
                          "vertexKey": clause_key, "localName": "status", "data": status_data}},
        ]

        # The clause's governs link is re-keyed if it carries the legacy
        # target-type segment "lease" (the vertex type is "leaseapp", and the
        # adjacency index rebuilds a walk's far endpoint from that segment, so
        # a legacy key can be walked from the lease side but never from the
        # clause side): the same document is created under the Contract #1
        # key and the legacy key is tombstoned. AlreadyTermed above makes
        # this a once-only pass — the repair rides the one write that
        # visits every legacy clause.
        # read-posture: (e) relation=governs epoch=none -- a clause carries
        # exactly one governs link, written once at mint; nothing else
        # writes the relation, so there is no concurrent mutator to fence.
        page, _ = kv.Links(clause_key, "governs", "out", None, 8)
        for lk in page:
            if lk.isDeleted:
                continue
            segs = lk.key.split(".")
            if len(segs) != 6 or segs[4] != "lease":
                continue
            # The enumeration's endpoints are rebuilt from the key, so the
            # legacy link's target reads as vtx.lease.<id>; the id segment is
            # the lease's, and it must be the lease this op was told about.
            if segs[5] != lease_id:
                fail("InvalidState: " + clause_key + " governs " + segs[5] + ", not " + lease_key)
            fixed_key = "lnk.clause." + segs[2] + ".governs.leaseapp." + lease_id
            # "class" is a Starlark keyword, so the link's class is read by name.
            link_class = getattr(lk, "class")
            # Written as an upsert rather than a create: a legacy clause never
            # had the Contract #1 key, and a stray tombstone under it must not
            # block the repair.
            mutations.append({"op": "update", "key": fixed_key,
                              "document": {"class": link_class, "isDeleted": False,
                                           "sourceVertex": clause_key, "targetVertex": lease_key,
                                           "localName": "governs", "data": {}}})
            mutations.append({"op": "update", "key": lk.key, "expectedRevision": lk.revision,
                              "document": {"class": link_class, "isDeleted": True,
                                           "sourceVertex": clause_key, "targetVertex": lease_key,
                                           "localName": "governs", "data": {}}})
        events = [{"class": "clause.termed",
                   "data": {"clauseKey": clause_key, "leaseAppKey": lease_key,
                            "validFrom": valid_from, "validUntil": valid_until,
                            "chargeValidUntil": new_due}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": clause_key}}

    if ot == "ShortenClauseTerm":
        # A recorded early move-out shortens the termed rent clause's
        # validUntil to the earlier of the lease's .notice moveOutAt and the
        # clause's own term end, so billing stops at the actual move-out
        # instead of running to the original term's end. Dispatched by the
        # leaseRentSettlement playbook's missing_termShortened gap
        # (lenses.go) — one clause per pass, the missing_term idiom: several
        # overrunning clauses on one lease (the original plus a renewal, say)
        # are shortened one per pass and the gap re-opens for the next.
        clause_key = required_string(p, "clauseKey")
        parts_of(clause_key, "clauseKey", "clause")
        lease_key = required_string(p, "leaseAppKey")
        _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")

        if not vertex_alive(state, clause_key):
            fail("UnknownClause: " + clause_key)

        terms_key = clause_key + ".terms"
        if not vertex_alive(state, terms_key):
            fail("InvalidState: " + clause_key + " has no .terms aspect")
        terms_doc = state[terms_key]
        terms = terms_doc.data
        valid_from = terms.get("validFrom")
        if valid_from == None:
            fail("NotTermed: " + clause_key + " carries no term to shorten; BackfillClauseTerm terms it first")
        valid_until = terms.get("validUntil")
        if valid_until == None:
            # Every clause that carries validFrom carries validUntil too
            # (mint_clause and BackfillClauseTerm write both together, never
            # one alone) — this guards the compare below against a corrupt
            # half-term rather than trusting that invariant blindly.
            fail("NotTermed: " + clause_key + "'s term is missing validUntil")

        # The lease's .notice is a REQUIRED declared read: the gap only opens
        # once a notice is recorded, so its absence here means the
        # dispatcher never declared it, not that none exists.
        notice_key = lease_key + ".notice"
        if not vertex_alive(state, notice_key):
            fail("NoNotice: " + lease_key + " has no recorded notice")
        move_out_at = state[notice_key].data.get("moveOutAt")
        if move_out_at == None:
            fail("NoNotice: " + lease_key + " has no recorded notice")

        # The clause must govern THIS lease, off its own outbound governs
        # walk — both the Contract #1 governs.leaseapp. spelling and the
        # legacy governs.lease. one (BackfillClauseTerm's re-key may not have
        # reached this clause yet). A clause carries exactly one governs
        # link, written once at mint, so one page suffices.
        # read-posture: (e) relation=governs epoch=none -- a clause carries
        # exactly one governs link, written once at mint; nothing else
        # writes the relation, so there is no concurrent mutator to fence.
        page, _ = kv.Links(clause_key, "governs", "out", None, 8)
        governs_this_lease = False
        for lk in page:
            if lk.isDeleted:
                continue
            segs = lk.key.split(".")
            if len(segs) != 6 or (segs[4] != "leaseapp" and segs[4] != "lease"):
                continue
            if segs[5] != lease_id:
                fail("InvalidState: " + clause_key + " governs " + segs[5] + ", not " + lease_key)
            governs_this_lease = True
        if not governs_this_lease:
            fail("InvalidState: " + clause_key + " does not govern " + lease_key)

        # new_until is computed FIRST, and the no-op guard compares against
        # IT — never against the raw move_out_at. A term already collapsed to
        # validFrom carries validUntil == validFrom, which can be well AFTER
        # move_out_at (that is exactly why it collapsed there), so a guard
        # keyed on move_out_at alone would never recognize it as settled and
        # would keep rewriting .terms/.status and re-emitting the event on
        # every dispatch. Guarding on new_until instead makes a collapsed (or
        # already-capped) term a true fixed point: valid_until <= new_until
        # holds as soon as valid_until == new_until, so a repeat dispatch is a
        # clean no-op. The lens's own overrunClauseKey CASE (lenses.go)
        # separately excludes a collapsed or completed clause from candidacy
        # entirely, so this guard is belt-and-suspenders for a stale
        # in-flight dispatch, not the primary defense.
        new_until = move_out_at
        if new_until < valid_from:
            new_until = valid_from

        if valid_until <= new_until:
            # Idempotent no-op: an earlier pass already shortened the term to
            # (or inside) the move-out, or the term already ends there —
            # nothing left to shorten (the EndTenancy/SetListingStatus no-op
            # shape: empty mutations, no primaryKey).
            return {"mutations": [], "events": [], "response": {}}

        # A term collapsed to validFrom (new_until == valid_from, a renewal
        # clause whose term starts after the move-out) is the recorded fact
        # that the clause bills nothing: clauseSatisfaction's periodStart <
        # validUntil conjunct and DebitAccount's TermExhausted refusal both
        # fail closed on it. The lens excludes a collapsed clause from
        # overrunClauseKey candidacy once this lands, so it is picked here
        # exactly once.
        new_terms = {}
        for k, v in terms.items():
            new_terms[k] = v
        new_terms["validUntil"] = new_until

        status_key = clause_key + ".status"
        if not (status_key in state and vertex_alive(state, status_key)):
            fail("InvalidState: " + clause_key + "'s .status was not hydrated; declare it in optionalReads")
        status_doc = state[status_key]
        status_data = {}
        for k, v in status_doc.data.items():
            status_data[k] = v
        due = status_data.get("chargeValidUntil")
        if due == None:
            due = valid_from
        # A due at or past the new term's end means the final period is
        # already billed: the clause is complete, the same mark
        # BackfillClauseTerm and DebitAccount leave for the same fact. A
        # clause DebitAccount already completed (period=oneTime's cousin: a
        # termed monthly clause whose final charge landed before the notice)
        # keeps its ORIGINAL completedAt — this op only caps validUntil
        # further, it does not re-mark a completion that already happened.
        already_completed = status_data.get("state") == "completed"
        if due >= new_until:
            status_data["state"] = "completed"
            if not already_completed:
                status_data["completedAt"] = time.rfc3339_utc(op.submittedAt)

        # Both writes pin to the hydrated revision (the EndTenancy OCC shape,
        # not BackfillClauseTerm's unconditioned one): a concurrent
        # BackfillClauseTerm or DebitAccount landing between this op's
        # hydration and its commit must conflict rather than being silently
        # overwritten by a shortening computed from a stale term or due date.
        mutations = [
            {"op": "update", "key": terms_key, "expectedRevision": terms_doc.revision,
             "document": {"class": "clauseTerms", "isDeleted": False,
                          "vertexKey": clause_key, "localName": "terms", "data": new_terms}},
            {"op": "update", "key": status_key, "expectedRevision": status_doc.revision,
             "document": {"class": "clauseStatus", "isDeleted": False,
                          "vertexKey": clause_key, "localName": "status", "data": status_data}},
        ]
        events = [{"class": "clause.termShortened",
                   "data": {"clauseKey": clause_key, "leaseAppKey": lease_key,
                            "validUntil": new_until, "chargeValidUntil": due}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": clause_key}}

    if ot == "InspectPremises":
        clause_key = required_string(p, "clauseKey")
        parts_of(clause_key, "clauseKey", "clause")

        if not vertex_alive(state, clause_key):
            fail("UnknownClause: " + clause_key)

        # Inspect once: the .inspection aspect is written CreateOnly, so a
        # second InspectPremises with a different requestId conflicts and is
        # rejected (mirrors SignLease's AlreadySigned check).
        insp_aspect_key = clause_key + ".inspection"
        if vertex_alive(state, insp_aspect_key):
            fail("AlreadyInspected: " + clause_key)

        inspected_at = time.rfc3339_utc(op.submittedAt)
        mutations = [
            make_aspect(clause_key, "inspection", "clauseInspection",
                        {"completed": True, "completedAt": inspected_at}),
        ]
        events = [{"class": "clause.inspected", "data": {"clauseKey": clause_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": clause_key}}

    fail("clause DDL: unknown operationType: " + ot)
`, "__UNTERMED_WINDOW__", loftspaceledger.RecurringChargePeriod, 1)

// aspectDeclarationOnlyScript is the declaration-only Starlark for
// clauseProse / clauseTerms / clauseStatus / clauseInspection — written by
// CreateClause's (and, for clauseTerms and clauseStatus, BackfillClauseTerm's;
// for clauseStatus, DebitAccount's; for clauseInspection, InspectPremises's)
// own op handler, never dispatched as an operation in its own right.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`
