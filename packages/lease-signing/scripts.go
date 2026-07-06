package leasesigning

import (
	"fmt"
	"strings"
)

// leaseAppDDLScript handles the leaseapp lifecycle ops CreateLeaseApplication
// and SignLease. Known-key reads only (validates every link/aspect endpoint by
// the keys the caller lists in ContextHint.Reads). Root data stays {} on every
// op (D5): the applicant is a link, the signature is an aspect.
//
// renewalWindow (a Go duration string, time.ParseDuration form) is baked into
// DecideLeaseApplication's .tenancy stamping at package-init time — the same
// "the policy lives in the script" convention bgcheckFreshnessWindow uses — so
// renewalOpensAt = leaseEnd - renewalWindow is a compile-time-selected
// constant, never a runtime mutation. The script ALSO contains Starlark's own
// literal '%' formatting verbs (add_months' "%04d-%02d-%02d" date format and
// the "%" modulo operator), so this substitutes the one renewalWindow site via
// a plain strings.Replace token rather than fmt.Sprintf — a whole-script
// Sprintf would misinterpret every one of those unrelated '%' as its own verb.
var leaseAppDDLScript = strings.Replace(`
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

def make_aspect_upsert(vtx_key, local_name, cls, data):
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_vtx_tombstone(key, cls):
    # Soft-delete a vertex (isDeleted=True). UNCONDITIONED — a concurrent withdraw
    # tombstones to the same state (idempotent), and nothing else writes the
    # leaseapp ROOT (SignLease writes the .signature aspect, a different key). The
    # convergence lens anchors on the leaseapp and filters isDeleted, so the row
    # deletes (EmptyBehavior). Root data stays {} (D5).
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": True, "data": {}}}

def make_link_revive_occ(key, source, target, cls, local_name, expected_revision):
    # Revive a soft-deleted guard link (isDeleted=True → False), CAS-guarded on its
    # tombstone revision. A blind make_link (op:create) would COLLIDE with the
    # existing tombstone key, so a re-apply after a withdraw must revive, not create
    # (the userTask-self-heal / object-GC-re-link precedent). The CAS serializes two
    # concurrent re-applies: both snapshot the same revision, both update, the second
    # RevisionConflicts (fail closed, never a silent duplicate).
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": {}},
            "expectedRevision": expected_revision}

def make_link_tombstone(key, source, target, cls, local_name):
    # Soft-delete a guard link (isDeleted=True). UNCONDITIONED — a withdraw is the
    # authority that the application (and so the guard) is gone; a live application
    # (alive guard) blocks any concurrent re-apply at CreateLeaseApplication, so no
    # revive races this tombstone. Frees the (applicant, unit) pair for re-apply.
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": True,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": {}}}

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

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

def optional_number(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        return None
    return v

def optional_bool(p, name):
    # An optional boolean flag (hasCoApplicant / hasGuarantor). Absent / null /
    # non-bool degrades to False — a flag the applicant did not set is "no".
    if not hasattr(p, name):
        return False
    v = getattr(p, name)
    if v == None or type(v) != type(True):
        return False
    return v

def string_list(p, name):
    # An optional list of non-empty trimmed strings (references). Absent / null /
    # non-list → []. Non-string / blank entries are dropped (a clean list, never
    # a fail — the count is what the landlord reads).
    if not hasattr(p, name):
        return []
    v = getattr(p, name)
    if v == None or type(v) != type([]):
        return []
    out = []
    for item in v:
        if type(item) == type("") and len(item.strip()) > 0:
            out.append(item.strip())
    return out

# Standard rental qualification: gross MONTHLY income must be at least this
# multiple of the monthly rent (the conventional 3x-rent rule). The op computes
# the derived incomeToRentMet boolean here (the lens engine has no arithmetic),
# so only the boolean — never the raw income — reaches the read model.
INCOME_TO_RENT_RATIO = 3.0

# The employmentStatus enum SetApplicantProfile admits. employed / self-employed
# are the active-income states that derive employmentVerified=True; the rest are
# captured honestly but read as unverified income.
EMPLOYMENT_STATUSES = ["employed", "self-employed", "unemployed", "student", "retired"]

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
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

DAYS_IN_MONTH = [31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]

def is_leap_year(year):
    return (year % 4 == 0 and year % 100 != 0) or (year % 400 == 0)

def days_in_month(year, month):
    if month == 2 and is_leap_year(year):
        return 29
    return DAYS_IN_MONTH[month - 1]

def zero_pad(n, width):
    # Starlark's %-format supports no field-width flag (unlike Python/Go), so a
    # fixed-width zero-padded integer (YYYY-MM-DD's month/day, always < 100,
    # year always < 10000) is built by hand: left-pad the decimal string with
    # "0" to the target width. This Starlark dialect has no while loop, so the
    # pad is a bounded for-loop over the width itself (width is always a small
    # literal — 2 or 4 — never large enough for the bound to matter).
    s = str(n)
    for _ in range(width):
        if len(s) >= width:
            break
        s = "0" + s
    return s

def add_months(rfc3339_instant, months):
    # Calendar-month addition on an RFC3339 instant (bespoke-contracts' "date math
    # belongs to the op, cypher only compares" precedent): the deterministic
    # Starlark sandbox has no calendar-aware builtin (time.rfc3339_add's Go
    # duration form is hours-only — no months unit), and a lease term is a
    # calendar-month count (12 months from Jan 31 is Jan 31 of next year, not a
    # fixed hour count that would drift across leap years / month lengths), so
    # this hand-rolls the same year/month/day carry identity-domain's DOB
    # validator already parses (leap-year table above). The clock-time and zone
    # suffix are preserved verbatim (a lease term shifts the calendar date, never
    # the time of day); the day-of-month CLAMPS to the target month's length
    # (Jan 31 + 1 month = Feb 28/29, never a rollover into March) — the
    # conventional calendar-add rule, applied once (months is always a small
    # positive integer here, never large enough to need iterated clamping).
    utc = time.rfc3339_utc(rfc3339_instant)
    year = int(utc[0:4])
    month = int(utc[5:7])
    day = int(utc[8:10])
    rest = utc[10:]  # "Thh:mm:ssZ"

    total = (month - 1) + int(months)
    year = year + total // 12
    month = (total % 12) + 1

    max_day = days_in_month(year, month)
    if day > max_day:
        day = max_day

    return zero_pad(year, 4) + "-" + zero_pad(month, 2) + "-" + zero_pad(day, 2) + rest

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateLeaseApplication":
        applicant = required_string(p, "applicant")
        _, applicant_id = parts_of(applicant, "applicant", "identity")

        # No-orphan invariant (FR29 / P4): the applicant identity MUST be alive.
        # An application pointing at a non-existent applicant is never committed.
        if not vertex_alive(state, applicant):
            fail("UnknownApplicant: " + applicant)

        # The application MUST name the unit it applies to (§7 Q2): a unit-less
        # application can never exist, so there is no unactuatable missing_unit
        # gap to wedge Weaver — the convergence lens reads the unit's listing /
        # address as informational columns ("what am I applying to lease"). The
        # unit is a location-domain vtx.unit.<NanoID>, alive-checked here (so the
        # caller must list it in ContextHint.Reads).
        unit = required_string(p, "unit")
        _, unit_id = parts_of(unit, "unit", "unit")
        if not vertex_alive(state, unit):
            fail("UnknownUnit: " + unit)

        # leaseAppId is a caller-supplied write-ahead seam (mirrors
        # service-domain's instanceId). Absent → mint internally. CreateOnly
        # semantics make a crash-retry with the same id collapse on the
        # Contract #4 tracker.
        app_id = bare_nanoid_or_mint(p, "leaseAppId")
        app_key = "vtx.leaseapp." + app_id

        # applicationFor: the leaseapp (later-arriving) is the source, the
        # pre-existing identity is the target (Contract #1 §1.1). Reads as
        # "this application is for this applicant."
        app_for_lnk = "lnk.leaseapp." + app_id + ".applicationFor.identity." + applicant_id

        # appliesToUnit: the leaseapp (later-arriving) is the source, the
        # pre-existing unit is the target (Contract #1 §1.1). Reads as
        # "this application applies to this unit." The convergence lens walks it.
        applies_to_lnk = "lnk.leaseapp." + app_id + ".appliesToUnit.unit." + unit_id

        # Per-(applicant, unit) live-application guard (Capability-KV §06 — the
        # operation's own Starlark logic; no platform scan, no frozen contract). The
        # constraint is pure existence-uniqueness — at most ONE live application per
        # applicant+unit (a unit accepts many DIFFERENT applicants: normal leasing,
        # the landlord chooses) — so it needs no list: a DETERMINISTIC guard LINK
        # keyed on the pair IS the constraint (relationships are links, never keys in
        # an aspect — Contract #1). lnk.identity.<a>.appliedToUnit.unit.<u> reads as
        # "this applicant applied to this unit" (§1.1: the link is the later-arriving
        # fact; source = the applicant, target = the unit). Read it ON DEMAND (kv.Read,
        # §2.5; the unit may have no guard yet, so a declared read would HydrationMiss):
        #   - alive  → DuplicateApplication (the applicant already has a live one).
        #   - absent → make_link (op:create) is the guard: two concurrent first-applies
        #              both create, the second RevisionConflicts on the key (fail closed).
        #   - tombstoned (a prior withdraw freed it) → REVIVE via CAS (a blind create
        #              would collide with the tombstone — revive-on-create), CAS-guarded
        #              so two concurrent re-applies fail closed.
        guard_key = "lnk.identity." + applicant_id + ".appliedToUnit.unit." + unit_id
        guard = kv.Read(guard_key)
        if guard != None and not guard.isDeleted:
            fail("DuplicateApplication: applicant " + applicant + " already has a live application for unit " + unit)
        if guard != None:
            guard_mut = make_link_revive_occ(guard_key, applicant, unit, "appliedToUnit", "appliedToUnit", guard.revision)
        else:
            guard_mut = make_link(guard_key, applicant, unit, "appliedToUnit", "appliedToUnit", {})

        # Root data minimal (D5): {} on root. The applicant + unit are links; the
        # status/gaps are lens-computed, never stored.
        mutations = [
            make_vtx(app_key, "leaseapp", {}),
            make_link(app_for_lnk, app_key, applicant, "applicationFor", "applicationFor", {}),
            make_link(applies_to_lnk, app_key, unit, "appliesToUnit", "appliesToUnit", {}),
            # The per-(applicant, unit) uniqueness guard link — created, or revived
            # from a prior withdraw's tombstone (CAS). See the guard logic above.
            guard_mut,
        ]

        # .terms (D3): the applicant's requested lease terms — additive
        # application detail for the applicant FE / operator (the convergence lens
        # does NOT read it). Written only when moveInDate is supplied, so a bare
        # applicant+unit application stays valid; moveInDate present ⇒
        # leaseTermMonths required (a half-specified terms block is rejected);
        # requestedRent is optional.
        move_in = optional_string(p, "moveInDate")
        if move_in != None:
            terms_data = {"moveInDate": move_in, "leaseTermMonths": require_number(p, "leaseTermMonths")}
            req_rent = optional_number(p, "requestedRent")
            if req_rent != None:
                terms_data["requestedRent"] = req_rent
            mutations.append(make_aspect(app_key, "terms", "terms", terms_data))

        events = [{"class": "leaseapp.applicationCreated",
                   "data": {"leaseAppKey": app_key, "applicant": applicant, "unit": unit}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "SignLease":
        app_key = required_string(p, "leaseAppKey")
        parts_of(app_key, "leaseAppKey", "leaseapp")

        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # Sign once: the .signature aspect is written CreateOnly, so a second
        # SignLease with a different requestId conflicts and is rejected. When
        # the caller lists the now-existing .signature key in ContextHint.Reads,
        # the state is hydrated and this explicit check fires first, upgrading
        # the rejection to a structured AlreadySigned ScriptError.
        sig_key = app_key + ".signature"
        if vertex_alive(state, sig_key):
            fail("AlreadySigned: " + app_key)

        # The signature is a fact in an aspect (D5); the application root stays
        # {}. signedAt is the op's own timestamp, normalized to canonical UTC so
        # a downstream lexical compare is sound.
        signed_at = time.rfc3339_utc(op.submittedAt)
        mutations = [
            make_aspect(app_key, "signature", "signature", {"signedAt": signed_at}),
        ]
        events = [{"class": "leaseapp.leaseSigned",
                   "data": {"leaseAppKey": app_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "DecideLeaseApplication":
        # The landlord's leasing decision — the human gate the listing-flip waits
        # behind. Validates the application is a live leaseapp, validates the decision
        # enum, enforces the decision lifecycle guards (below), and writes a .decision
        # aspect {value, decidedAt} on the leaseapp. The convergence lens reads
        # app.decision.data.value: approved opens missing_listingLeased (→ the unit
        # leases); declined is a terminal disposition (declined OR'd in the lens).
        app_key = required_string(p, "leaseAppKey")
        _, app_id = parts_of(app_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        decision = required_string(p, "decision")
        if decision != "approved" and decision != "declined":
            fail("BadDecision: " + decision)

        # Terminal-decision guard: a recorded decision is FINAL. Re-submitting the SAME
        # decision stays accepted (idempotent / re-run-safe under at-least-once);
        # changing a recorded decision to a DIFFERENT value is rejected — an approved or
        # declined application must not silently flip or oscillate (the verified live
        # bug: approved→declined committed freely). The prior decision is read lazily
        # (kv.Read, §2.5 — the link-check idiom already used in this script), matching
        # the op's unconditioned single-op semantics. Reconsidering a recorded decision
        # is a future explicit re-open op, not a silent overwrite.
        prior = kv.Read(app_key + ".decision")
        if prior != None and not prior.isDeleted:
            prior_val = prior.data.get("value")
            if prior_val != None and prior_val != decision:
                fail("DecisionFinal: application " + app_key + " is already " + str(prior_val) + "; a recorded decision is terminal and cannot be changed to " + decision)

        # Approve-readiness floor: a landlord must not APPROVE an application the
        # applicant has not yet SIGNED (the verified live bug: a profileSubmitted=false
        # application could be approved, producing a misleading "Approved" the
        # convergence lens can never lease). Signing is the applicant's final
        # commitment step; an unsigned application is not ready for an approval. This is
        # a cheap, SOUND floor — deliberately NOT the full applicantApproved gate
        # (.ssn + a fresh completed bgcheck + a completed payment + the signature),
        # which is a lens-derived signal spanning the identity + its providedTo service
        # instances with freshness windows; reproducing that cross-vertex computation in
        # this write-path op would duplicate read-model logic and risk op↔lens
        # divergence. The convergence lens still enforces the FULL gate before the unit
        # actually leases (missing_listingLeased), so an approve here can never lease an
        # unqualified applicant. A DECLINE carries no readiness floor — a landlord may
        # decline at any point.
        if decision == "approved":
            sig = kv.Read(app_key + ".signature")
            if sig == None or sig.isDeleted:
                fail("NotReadyToApprove: application " + app_key + " has not been signed by the applicant; cannot approve an unsigned application")

        # decidedAt is the op's own timestamp, normalized to canonical UTC (read-free,
        # mirroring SignLease's signedAt) so a downstream lexical compare is sound.
        decided_at = time.rfc3339_utc(op.submittedAt)
        # reason is optional free-text the landlord supplies with a decline (applicant
        # feedback + a fair-housing record). It is stored on the .decision aspect only
        # when supplied; an approve or a reasonless decline carries none. A same-value
        # re-submission (idempotent) can attach / update the reason on the already-
        # recorded decision. The convergence lens projects it as declineReason.
        decision_data = {"value": decision, "decidedAt": decided_at}
        reason = optional_string(p, "reason")
        if reason != None:
            decision_data["reason"] = reason
        mutations = [
            make_aspect_upsert(app_key, "decision", "decision", decision_data),
        ]

        # .tenancy: the tenancy-term fact stamped exactly once, on the FIRST
        # approve — CREATE-ONLY (a re-approve of an already-terminal decision is
        # idempotent at the DecisionFinal guard above, but even a same-value
        # re-submission must never re-derive .tenancy and silently truncate a
        # SignRenewal-extended leaseEnd back to the original term, design §4.1).
        # Read the unit via the leaseapp's OWN appliesToUnit link (never the
        # payload — the caller cannot forge which unit's listing feeds the term
        # math) and its .listing economics; both are on-demand kv.Read (like
        # SetApplicantProfile's rent lookup) so the caller need only list the
        # unit key in ContextHint.Reads, mirroring the existing unit-verification
        # idiom in this script.
        if decision == "approved":
            existing_tenancy = kv.Read(app_key + ".tenancy")
            if existing_tenancy == None or existing_tenancy.isDeleted:
                # appliesToUnit is required at CreateLeaseApplication (no unit-less
                # application, §3 D5), so a live application always names exactly
                # one unit. Starlark has no prefix scan, so the caller supplies the
                # unit key explicitly and it is verified against the leaseapp's own
                # deterministic appliesToUnit link — the same unit-verification
                # idiom WithdrawLeaseApplication / SetApplicantProfile already use —
                # so a wrong / fabricated unit can never feed the term math.
                unit = required_string(p, "unit")
                _, unit_id = parts_of(unit, "unit", "unit")
                applies_to_lnk = "lnk.leaseapp." + app_id + ".appliesToUnit.unit." + unit_id
                ulink = kv.Read(applies_to_lnk)
                if ulink == None or ulink.isDeleted:
                    fail("UnitMismatch: " + unit + " is not the unit application " + app_key + " applies to")
                listing = kv.Read(unit + ".listing")
                if listing == None or listing.isDeleted:
                    fail("NoListing: unit " + unit + " has no .listing aspect; cannot compute a tenancy term")
                available_from = listing.data.get("availableFrom")
                term_months = listing.data.get("leaseTermMonths")
                if available_from == None or term_months == None:
                    fail("NoListing: unit " + unit + "'s .listing is missing availableFrom/leaseTermMonths")
                lease_start = time.rfc3339_utc(available_from)
                lease_end = add_months(lease_start, term_months)
                renewal_opens_at = time.rfc3339_add(lease_end, "-__RENEWAL_WINDOW__")
                mutations.append(make_aspect(app_key, "tenancy", "tenancy",
                    {"leaseStart": lease_start, "leaseEnd": lease_end, "renewalOpensAt": renewal_opens_at}))

        events = [{"class": "leaseapp.applicationDecided",
                   "data": {"leaseAppKey": app_key, "decision": decision}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "WithdrawLeaseApplication":
        # Withdraw / cancel an application: soft-delete the leaseapp so it drops
        # from My Applications (the convergence lens anchors on it + filters
        # isDeleted → EmptyBehavior delete), and FREE the per-(applicant, unit)
        # guard link so it stops blocking a re-apply. The complement to
        # CreateLeaseApplication's guard — an applicant who applied to the wrong
        # unit can back out + re-apply (the guard revives on re-apply).
        app_key = required_string(p, "leaseAppKey")
        _, app_id = parts_of(app_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # The unit + applicant the application is for (the FE carries both on the
        # row). Verify each is genuinely THIS application's endpoint via its
        # deterministic leaseapp-anchored link (kv.Read) — mirroring clinic's
        # withProvider check — so a wrong / fabricated unit or applicant can't be
        # used to free a different pair's guard. The (applicant, unit) pair then
        # reconstructs the guard-link key deterministically.
        unit = required_string(p, "unit")
        _, unit_id = parts_of(unit, "unit", "unit")
        applies_to_lnk = "lnk.leaseapp." + app_id + ".appliesToUnit.unit." + unit_id
        ulink = kv.Read(applies_to_lnk)
        if ulink == None or ulink.isDeleted:
            fail("UnitMismatch: " + unit + " is not the unit application " + app_key + " applies to")

        applicant = required_string(p, "applicant")
        _, applicant_id = parts_of(applicant, "applicant", "identity")
        app_for_lnk = "lnk.leaseapp." + app_id + ".applicationFor.identity." + applicant_id
        alink = kv.Read(app_for_lnk)
        if alink == None or alink.isDeleted:
            fail("ApplicantMismatch: " + applicant + " is not the applicant of application " + app_key)

        # Tombstone the application. The applicationFor / appliesToUnit links are
        # left in place (non-cascading tombstone, the clinic-domain precedent) — they
        # dangle off a tombstoned anchor every reader filters.
        mutations = [make_vtx_tombstone(app_key, "leaseapp")]

        # Free the per-(applicant, unit) guard link: tombstone it so a re-apply
        # revives it. UNCONDITIONED (the withdraw is the authority the application is
        # gone; an alive guard blocks any concurrent re-apply, so no revive races
        # this). Read it first so a never-guarded application (legacy data) writes no
        # phantom key — absent → nothing to free.
        guard_key = "lnk.identity." + applicant_id + ".appliedToUnit.unit." + unit_id
        guard = kv.Read(guard_key)
        if guard != None and not guard.isDeleted:
            mutations.append(make_link_tombstone(guard_key, applicant, unit, "appliedToUnit", "appliedToUnit"))

        events = [{"class": "leaseapp.applicationWithdrawn",
                   "data": {"leaseAppKey": app_key, "unit": unit}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "SetApplicantProfile":
        # The applicant's qualification profile — the data a landlord decides on.
        # It captures income / employment / references / co-applicant / guarantor
        # on the application as a .profile aspect, and derives the landlord-facing
        # qualification SIGNALS the lens projects. The RAW financials (annualIncome,
        # employerName) live in the Core-KV aspect plaintext-for-now (the same
        # discipline as identity .ssn / .demographics — the deferred Vault plane owns
        # their encryption + a raw-financial display later) and are NEVER projected;
        # only the DERIVED booleans/counts reach the read model, so the landlord sees
        # "income meets 3x rent / employed / N references" without the raw figures.
        # Re-submittable: an UNCONDITIONED upsert overwrites the prior profile.
        app_key = required_string(p, "leaseAppKey")
        _, app_id = parts_of(app_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # The unit the application applies to — needed to read its listing rent for
        # the income-to-rent derivation. Verify it is genuinely THIS application's
        # unit via the deterministic appliesToUnit link (kv.Read, the Withdraw /
        # clinic withProvider precedent) so a wrong / fabricated unit can't be used.
        unit = required_string(p, "unit")
        _, unit_id = parts_of(unit, "unit", "unit")
        applies_to_lnk = "lnk.leaseapp." + app_id + ".appliesToUnit.unit." + unit_id
        link = kv.Read(applies_to_lnk)
        if link == None or link.isDeleted:
            fail("UnitMismatch: " + unit + " is not the unit application " + app_key + " applies to")

        annual_income = require_number(p, "annualIncome")
        if annual_income <= 0:
            fail("InvalidArgument: annualIncome: required positive number")
        employment = required_string(p, "employmentStatus")
        if employment not in EMPLOYMENT_STATUSES:
            fail("InvalidArgument: employmentStatus: must be one of employed, self-employed, unemployed, student, retired; got " + employment)
        employer = optional_string(p, "employerName")
        refs = string_list(p, "references")
        has_co = optional_bool(p, "hasCoApplicant")
        has_guarantor = optional_bool(p, "hasGuarantor")

        # Derived qualification signals (the lens has no arithmetic / len, so they
        # are computed here). employmentVerified = an active income source;
        # referenceCount = how many references were supplied.
        employment_verified = employment == "employed" or employment == "self-employed"
        ref_count = len(refs)

        # The unit's monthly listing rent, read ON DEMAND (kv.Read §2.5, non-OCC
        # config read — mirroring clinic's enforce_hours). None when the unit has no
        # listing / no positive rent (an income-to-rent signal is then genuinely
        # unknown, not false). Read at submit time against the rent then-current; a
        # later rent change is reflected on the next SetApplicantProfile. The applicant
        # AND the guarantor income-to-rent checks both derive from it.
        rent = None
        listing = kv.Read(unit + ".listing")
        if listing != None and not listing.isDeleted:
            r = listing.data.get("rentAmount")
            if r != None and (type(r) == type(0) or type(r) == type(0.0)) and r > 0:
                rent = r

        # income-to-rent: gross MONTHLY income ≥ 3× rent (the conventional rule).
        income_to_rent_met = None
        if rent != None:
            income_to_rent_met = (annual_income * 1.0) / 12.0 >= INCOME_TO_RENT_RATIO * rent

        # .profile (D3): raw fields (NOT projected) + derived signals (projected).
        profile_data = {
            "annualIncome":       annual_income,
            "employmentStatus":   employment,
            "references":         refs,
            "hasCoApplicant":     has_co,
            "hasGuarantor":       has_guarantor,
            "employmentVerified": employment_verified,
            "referenceCount":     ref_count,
            "submittedAt":        time.rfc3339_utc(op.submittedAt),
        }
        if employer != None:
            profile_data["employerName"] = employer
        if income_to_rent_met != None:
            profile_data["incomeToRentMet"] = income_to_rent_met

        # Guarantor / co-applicant DETAIL (RAW — like the applicant's own financials,
        # stored plaintext-for-now and NEVER projected; the deferred Vault plane owns
        # their display). Captured only when the applicant set the corresponding flag,
        # so clearing the flag drops the detail on the next (unconditioned) re-submit.
        # The ONE derived, projectable signal is guarantorIncomeToRentMet — does the
        # guarantor's OWN income cover 3× the rent (the standard reason a guarantor
        # backs a thin-income application), derived from the same rent read above so a
        # landlord can lean on "guarantor covers 3× rent" rather than a bare ✓ on a
        # below-income applicant. None when no guarantor income / no listing rent.
        guarantor_income_to_rent_met = None
        if has_guarantor:
            g_name = optional_string(p, "guarantorName")
            g_rel = optional_string(p, "guarantorRelationship")
            g_income = optional_number(p, "guarantorAnnualIncome")
            if g_name != None:
                profile_data["guarantorName"] = g_name
            if g_rel != None:
                profile_data["guarantorRelationship"] = g_rel
            if g_income != None and g_income > 0:
                profile_data["guarantorAnnualIncome"] = g_income
                if rent != None:
                    guarantor_income_to_rent_met = (g_income * 1.0) / 12.0 >= INCOME_TO_RENT_RATIO * rent
        if has_co:
            c_name = optional_string(p, "coApplicantName")
            c_contact = optional_string(p, "coApplicantContact")
            if c_name != None:
                profile_data["coApplicantName"] = c_name
            if c_contact != None:
                profile_data["coApplicantContact"] = c_contact
        if guarantor_income_to_rent_met != None:
            profile_data["guarantorIncomeToRentMet"] = guarantor_income_to_rent_met

        mutations = [make_aspect_upsert(app_key, "profile", "profile", profile_data)]
        events = [{"class": "leaseapp.profileSubmitted",
                   "data": {"leaseAppKey": app_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    fail("leaseapp DDL: unknown operationType: " + ot)
`, "__RENEWAL_WINDOW__", renewalWindow, 1)

// leaseServiceInstanceDDLScript is the externalTask instanceOp. It mints the
// claim vertex vtx.service.<handle> (the same shape 14.1's service instance
// uses, reusing its .outcome aspect shape downstream), records the family
// discriminator + the providedTo link, and emits the external.<adapter> event
// off its own transactional outbox. Template-less (no instanceOf): the lens
// hops providedTo, not instanceOf.
const leaseServiceInstanceDDLScript = `
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

def required_bare_handle(p, name):
    # The bare instance handle Loom minted: type-free, must carry no key
    # delimiters so "vtx.service." + handle is a single well-formed vertex key.
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
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

SERVICE_FAMILIES = ["backgroundCheck", "payment"]

def family_of(p):
    # The family is opaque pass-through from the Loom step's params.family.
    # A nested payload object is exposed to Starlark as a dict (not a struct),
    # so it is read by key, not by attribute.
    if not hasattr(p, "params") or p.params == None:
        fail("InvalidArgument: params.family: required (backgroundCheck|payment)")
    params = p.params
    if type(params) != type({}) or "family" not in params:
        fail("InvalidArgument: params.family: required (backgroundCheck|payment)")
    fam = params["family"]
    if fam == None or type(fam) != type("") or len(fam.strip()) == 0:
        fail("InvalidArgument: params.family: required non-empty string")
    fam = fam.strip()
    if fam not in SERVICE_FAMILIES:
        fail("InvalidArgument: params.family: must be one of backgroundCheck, payment; got " + fam)
    return fam

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateLeaseServiceInstance":
        handle = required_bare_handle(p, "instanceKey")
        subject_key = required_string(p, "subjectKey")
        adapter = required_string(p, "adapter")
        reply_op = required_string(p, "replyOp")
        fam = family_of(p)
        _, subject_id = parts_of(subject_key, "subjectKey", "identity")

        # No-orphan invariant (FR29 / P4): the applicant identity MUST be alive.
        if not vertex_alive(state, subject_key):
            fail("UnknownApplicant: " + subject_key)

        # Prepend the package-chosen claim-vertex type. The engine never names a
        # type; the replyOp re-prepends the SAME type — a matched pair.
        inst_key = "vtx.service." + handle

        # The type/subtype discriminator lives on the vertex ENVELOPE class (P7) —
        # service.<family>.instance — NOT a .class/.family shadow aspect. That
        # fine-grained class misses the exact class→DDL lookup, so the step-6
        # write-gate resolver walks this instance's instanceOf link to its type
        # authority (Contract #1 §1.5 instanceOf terminal): the leaseServiceInstance
        # DDL's meta-vertex, surfaced to the script as ddl[...].metaKey. The lens
        # discriminates bgcheck/payment by reading inst.class directly (no .family).
        inst_class = "service." + fam + ".instance"
        meta_key = ddl["leaseServiceInstance"].metaKey
        _, meta_id = parts_of(meta_key, "typeAuthority", "meta")
        instance_of_lnk = "lnk.service." + handle + ".instanceOf.meta." + meta_id

        # providedTo: the service instance (later-arriving) is the source, the
        # pre-existing identity is the target (Contract #1 §1.1). This is the
        # convergence link the lens fans out across to read the outcome aspect.
        provided_to_lnk = "lnk.service." + handle + ".providedTo.identity." + subject_id

        # Root data minimal (D5): {} on root. The vertex KEY type is 'service'
        # (vtx.service.<handle>) so the lens anchors via the key segment; the
        # envelope CLASS carries the fine-grained discriminator. NO outcome aspect
        # yet — absence = not-yet-complete. The instanceOf link is the source of the
        # write-gate authority; providedTo is the convergence link.
        mutations = [
            make_vtx(inst_key, inst_class, {}),
            make_link(instance_of_lnk, inst_key, meta_key, "instanceOf", "instanceOf", {}),
            make_link(provided_to_lnk, inst_key, subject_key, "providedTo", "providedTo", {}),
        ]

        # Emit the external.<adapter> event off this op's transactional outbox.
        # The body shape matches the bridge's externalEvent reader: the bare
        # handle is the opaque correlation token (instanceKey == externalRef ==
        # idempotencyKey by construction). dispatchOp is the package-local op the
        # bridge posts if its adapter returns Pending (it records the .dispatch
        # marker); it is the matched pair of replyOp, which the bridge posts on a
        # terminal outcome.
        event_data = {
            "instanceKey":    handle,
            "adapter":        adapter,
            "replyOp":        reply_op,
            "dispatchOp":     "RecordServiceDispatch",
            "externalRef":    handle,
            "idempotencyKey": handle,
            "params":         {"family": fam},
        }
        events = [{"class": "external." + adapter, "data": event_data}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    fail("leaseServiceInstance DDL: unknown operationType: " + ot)
`

// leaseServiceReplyDDLScript is the externalTask replyOp the bridge submits.
// The bridge posts {externalRef, status, result}; this op reconstructs the claim
// vertex key, takes the adapter's terminal status (completed | failed), derives
// completedAt + validUntil, writes the .outcome aspect, and emits
// orchestration.externalTaskCompleted{externalRef} — the completion signal Loom
// correlates on. Without that event the externalTask never completes.
//
// The bridge submits this op with no ContextHint.Reads (internal/bridge's
// actuator builds an envelope with no Reads field), so the op reads NOTHING
// from state: the reconstructed vtx.service.<handle> vertex, its .class aspect,
// and its root revision are all unhydrated on the live path. The once-only
// guarantee is therefore the create-only .outcome write itself — a redelivered
// reply conflicts on the existing .outcome key and the batch is rejected (the
// bridge's deterministic deriveReplyRequestID already collapses most
// redeliveries at the Contract #4 tracker). The instance root, already minted
// {data:{}} by the instanceOp, is left untouched (D5).
//
// validUntil is pure arithmetic on the op's own completedAt
// (time.rfc3339_add), so the op stays read-free.
var leaseServiceReplyDDLScript = fmt.Sprintf(`
def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
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
    return v

def required_bare_handle(p, name):
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

# The terminal outcome values RecordLeaseServiceOutcome admits (mirrors
# service-domain). completed = the external call succeeded with a satisfying
# result; failed = a definitive business rejection (a declined charge, a failed
# background check). The bridge supplies it verbatim from the adapter's
# Result.Status — it is required, with no default.
OUTCOME_STATUSES = ["completed", "failed"]

def required_status(p):
    st = required_string(p, "status")
    if st not in OUTCOME_STATUSES:
        fail("InvalidArgument: status: must be one of completed, failed; got " + st)
    return st

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordLeaseServiceOutcome":
        handle = required_bare_handle(p, "externalRef")
        # Reconstruct the claim-vertex key from the bare handle: the instanceOp
        # chose 'service' as the type, so the replyOp re-prepends the same type.
        # The bare-handle format validation needs no state read.
        inst_key = "vtx.service." + handle

        # The bridge supplies only a free-form result string. It is NOT written
        # to the projection-plane .outcome aspect (it can carry PII / payment
        # data in production and the lens reads only status / completedAt /
        # validUntil); it rides the service.outcomeRecorded provenance event body
        # instead.
        result = optional_string(p, "result")

        # The terminal status is the adapter's verdict, supplied verbatim by the
        # bridge (completed | failed) and required — an adapter error is
        # Nak+retry (never a reply), so every reply carries a definitive business
        # outcome. completedAt is the op's own timestamp (the bridge supplies
        # none), normalized to canonical UTC for a sound lexical compare.
        status = required_status(p)
        completed_at = time.rfc3339_utc(op.submittedAt)

        # Stamp validUntil = completedAt + the freshness window. This op is
        # read-free and cannot tell bgcheck from payment, so it stamps validUntil
        # on EVERY outcome (family-agnostic). The lens applies the freshness
        # policy to bgcheck only — it counts a completed bgcheck toward
        # convergence solely while validUntil > $now, re-opening the gap once it
        # lapses; payment ignores validUntil (ever-completed). So validUntil on a
        # payment outcome is harmless and unused: the freshness rule lives in the
        # cypher (Contract #10 §10.2). The add is pure arithmetic on completed_at
        # — no clock read, so the op stays read-free and deterministic.
        valid_until = time.rfc3339_add(completed_at, %q)

        # Write the .outcome aspect {status, completedAt, validUntil} as a
        # create-only mutation. This create-only IS the once-only guarantee: a
        # redelivered reply conflicts on the existing key and the batch is
        # rejected (FR58 at the DDL layer, atop the bridge's deterministic
        # requestId collapse). The instance root, already {data:{}}, is not
        # touched (D5).
        mutations = [
            make_aspect(inst_key, "outcome", "leaseServiceOutcome", {"status": status, "completedAt": completed_at, "validUntil": valid_until}),
        ]

        # Emit the completion signal Loom correlates on (the BARE handle as
        # externalRef — Loom parks on token.<handle>) PLUS a provenance event
        # (which carries the free-form result, kept off the aspect). The
        # completion event is load-bearing: without it the externalTask never
        # completes (the creation-deadline disarmed on instanceOp commit; the
        # bridge reply carried no completion signal).
        provenance = {"serviceKey": inst_key, "status": status, "completedAt": completed_at, "validUntil": valid_until}
        if result != None:
            provenance["result"] = result
        events = [
            {"class": "orchestration.externalTaskCompleted",
             "data": {"externalRef": handle}},
            {"class": "service.outcomeRecorded",
             "data": provenance},
        ]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    fail("leaseServiceReply DDL: unknown operationType: " + ot)
`, bgcheckFreshnessWindow)

// leaseServiceDispatchDDLScript is the externalTask dispatchOp the bridge submits
// when its adapter returns Pending (the external call was submitted but has not
// resolved yet). The bridge posts {externalRef, vendorRef, adapter, replyOp,
// nextPollAt, deadline}; this op reconstructs the claim vertex key from the bare
// handle and writes a create-only .dispatch aspect
// {vendorRef, adapter, replyOp, submittedAt, nextPollAt, deadline} on it — the
// pending marker. The bridge's poll/timeout schedules carry the routing (adapter /
// replyOp / vendorRef) on their payload, so the fired handler reads it from there —
// NOT from this marker; the marker records the same routing for the lens / Weaver
// read-model (pending-suppression, a later increment). It does NOT write the create-only .outcome
// aspect and does NOT emit orchestration.externalTaskCompleted: the externalTask
// is NOT done, so Loom's token stays parked. The .dispatch and .outcome aspects
// are deliberately separate (.outcome is the FR58 once-only terminal guard;
// "pending" is a distinct state the lens/Weaver can read without colliding with
// it).
//
// Like the replyOp the bridge submits this with no ContextHint.Reads, so the op
// reads NOTHING from state: the reconstructed vtx.service.<handle> vertex is
// unhydrated on the live path. The once-only guarantee is the create-only
// .dispatch write itself — a redelivered Pending conflicts on the existing
// .dispatch key and the batch is rejected (atop the bridge's deterministic
// deriveDispatchRequestID, which already collapses most redeliveries at the
// Contract #4 tracker). submittedAt is the op's own timestamp, normalized to
// canonical UTC; nextPollAt and deadline are the bridge-supplied schedule
// instants, normalized to canonical UTC for a sound lexical compare (no clock
// read — read-free, deterministic).
const leaseServiceDispatchDDLScript = `
def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def required_instant(p, name):
    # An RFC3339 instant the bridge computed (nextPollAt / deadline), normalized
    # to canonical UTC so the marker compares lexically with the schedule headers.
    v = required_string(p, name)
    return time.rfc3339_utc(v)

def required_bare_handle(p, name):
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordServiceDispatch":
        handle = required_bare_handle(p, "externalRef")
        # Reconstruct the claim-vertex key from the bare handle (the matched-pair
        # type the instanceOp chose). The bare-handle validation needs no state read.
        inst_key = "vtx.service." + handle

        # The vendor's opaque pending reference (the poll/webhook key the bridge
        # got back from the adapter). Required — a Pending with no ref is meaningless.
        vendor_ref = required_string(p, "vendorRef")

        # The routing recorded for the lens / Weaver read-model: which adapter to
        # Poll on a poll firing, and which replyOp to post when the poll resolves or
        # the call times out. The fired handler reads these from the schedule
        # payload, not the marker; both are required here for the read-model record.
        adapter = required_string(p, "adapter")
        reply_op = required_string(p, "replyOp")

        # The bridge-supplied schedule instants: when the next poll is due and when
        # the call gives up. Recorded for the lens / Weaver read-model; the timeout
        # itself fires from the armed schedule, not this marker.
        next_poll_at = required_instant(p, "nextPollAt")
        deadline = required_instant(p, "deadline")

        # submittedAt is the op's own timestamp, normalized to canonical UTC. The
        # bridge supplies no timestamp; this is the dispatch instant.
        submitted_at = time.rfc3339_utc(op.submittedAt)

        # Write the .dispatch aspect {vendorRef, adapter, replyOp, submittedAt,
        # nextPollAt, deadline} as a create-only mutation. This create-only IS the
        # once-only guarantee: a redelivered Pending conflicts on the existing key
        # and the batch is rejected (atop the bridge's deterministic dispatch
        # requestId collapse). NO .outcome is written and NO
        # orchestration.externalTaskCompleted is emitted — the task is not done,
        # the token stays parked. The instance root, already {}, is untouched (D5).
        mutations = [
            make_aspect(inst_key, "dispatch", "leaseServiceDispatchMarker",
                        {"vendorRef": vendor_ref, "adapter": adapter, "replyOp": reply_op,
                         "submittedAt": submitted_at, "nextPollAt": next_poll_at, "deadline": deadline}),
        ]

        # A provenance event marks the submit for the audit join (NOT a completion
        # signal — Loom must NOT close the token on a dispatch).
        events = [
            {"class": "service.dispatchRecorded",
             "data": {"serviceKey": inst_key, "vendorRef": vendor_ref, "submittedAt": submitted_at}},
        ]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    fail("leaseServiceDispatch DDL: unknown operationType: " + ot)
`
