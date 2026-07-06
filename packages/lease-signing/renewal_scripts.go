package leasesigning

import (
	"strconv"
	"strings"
)

// renewalDDLScript handles the five renewal-cycle ops (OpenRenewal /
// SetRenewalTerms / VerifyGuarantor / SignRenewal / CancelRenewal). Known-key
// reads only; SignRenewal is the one multi-vertex write (the renewal root +
// aspect, and the leaseapp's .tenancy aspect, in the same batch — the
// CreateAppointment precedent).
//
// renewalWindow (the package's build-tag-selected duration string) and its
// renewalWindowHours integer twin are baked in at package-init time for the
// two computations this script needs: SignRenewal's renewalOpensAt recompute
// (the duration-string form, `__RENEWAL_WINDOW__`) and SetRenewalTerms's
// termMonths floor (the integer form, `__RENEWAL_WINDOW_HOURS__`). Two
// strings.Replace token sites, chained — never fmt.Sprintf (leaseAppDDLScript's
// doc comment explains why: add_months' own literal '%' formatting verbs would
// collide with a Sprintf verb scan; this script has the identical add_months
// copy, so the same hazard applies here).
var renewalDDLScript = strings.Replace(strings.Replace(`
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_vtx_update(key, cls, data, expected_revision):
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data},
            "expectedRevision": expected_revision}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert(vtx_key, local_name, cls, data):
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
    # This Starlark dialect has no while loop (leaseAppDDLScript's add_months
    # hit the same constraint); pad via a bounded for-loop over width instead.
    s = str(n)
    for _ in range(width):
        if len(s) >= width:
            break
        s = "0" + s
    return s

def add_months(rfc3339_instant, months):
    # Calendar-month addition — see leaseAppDDLScript's add_months for the full
    # rationale (no calendar-aware builtin; a lease/renewal term is a calendar-
    # month count, clamped at the target month's length). Duplicated verbatim
    # here rather than shared: each DDL script is its own independent Starlark
    # module (no cross-script imports), the established per-script-helper
    # posture every DDL in this package already follows.
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

# renewalWindow expressed in whole months (ceil), the floor SetRenewalTerms
# enforces on termMonths (design §4.4: a term shorter than the renewal window
# would open the NEXT cycle the instant this one signs). renewalWindow is a
# Go duration string of whole hours (e.g. "1440h"); 730 hours/month is the
# conventional average (365.25*24/12) used only for this floor computation —
# add_months' own calendar-accurate carry is untouched, this is a ceiling
# check on an integer month count, not a date computation.
RENEWAL_WINDOW_HOURS = __RENEWAL_WINDOW_HOURS__
HOURS_PER_MONTH = 730

def renewal_window_min_months():
    months = RENEWAL_WINDOW_HOURS // HOURS_PER_MONTH
    if RENEWAL_WINDOW_HOURS % HOURS_PER_MONTH != 0:
        months = months + 1
    if months < 1:
        months = 1
    return months

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "OpenRenewal":
        # Weaver's service-actor directOp (the SetListingStatus cross-package
        # precedent): opens a renewal cycle for a leaseapp nearing its
        # renewalOpensAt horizon. Reads the leaseapp + its .tenancy aspect,
        # derives the deterministic renewal id, and CreateOnly-commits the
        # vertex + the renews link — a duplicate fire collides on the vertex
        # create and converges (no duplicate cycle for the same leaseapp+cycle).
        app_key = required_string(p, "leaseApp")
        _, app_id = parts_of(app_key, "leaseApp", "leaseapp")
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        tenancy = kv.Read(app_key + ".tenancy")
        if tenancy == None or tenancy.isDeleted:
            fail("NoTenancy: application " + app_key + " has no .tenancy aspect; a renewal cycle cannot open without one")
        cycle_end = tenancy.data.get("leaseEnd")
        if cycle_end == None:
            fail("NoTenancy: application " + app_key + "'s .tenancy aspect is missing leaseEnd")

        renewal_id = crypto.sha256NanoID("renewal:" + app_id + ":" + cycle_end)
        renewal_key = "vtx.renewal." + renewal_id
        renews_lnk = "lnk.renewal." + renewal_id + ".renews.leaseapp." + app_id

        mutations = [
            make_vtx(renewal_key, "renewal", {"cycleEnd": cycle_end, "status": "open"}),
            make_link(renews_lnk, renewal_key, app_key, "renews", "renews", {}),
        ]
        events = [{"class": "renewal.opened",
                   "data": {"renewalKey": renewal_key, "leaseAppKey": app_key, "cycleEnd": cycle_end}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": renewal_key}}

    if ot == "SetRenewalTerms":
        # Landlord rent-adjustment leg. Rejects TermsLocked once the cycle is
        # signed or not open (terms can never drift under a recorded
        # signature); otherwise an unconditioned upsert (revision before
        # signature rides the operator model — no in-chain revision path).
        renewal_key = required_string(p, "renewalKey")
        parts_of(renewal_key, "renewalKey", "renewal")
        renewal_vtx = state[renewal_key] if renewal_key in state else None
        if renewal_vtx == None or (hasattr(renewal_vtx, "isDeleted") and renewal_vtx.isDeleted):
            fail("UnknownRenewal: " + renewal_key)
        status = renewal_vtx.data.get("status")
        if status != "open":
            fail("TermsLocked: renewal " + renewal_key + " is not open (status=" + str(status) + ")")
        sig = kv.Read(renewal_key + ".renewalSignature")
        if sig != None and not sig.isDeleted:
            fail("TermsLocked: renewal " + renewal_key + " is already signed; terms cannot change under a recorded signature")

        rent_amount = require_number(p, "rentAmount")
        if rent_amount <= 0:
            fail("InvalidArgument: rentAmount: required positive number")
        term_months = require_number(p, "termMonths")
        if term_months != int(term_months):
            fail("InvalidTermMonths: termMonths must be a whole number of months; got " + str(term_months))
        min_months = renewal_window_min_months()
        if term_months < min_months:
            fail("InvalidArgument: termMonths: must be >= " + str(min_months) + " (the renewal window floor); got " + str(term_months))

        set_at = time.rfc3339_utc(op.submittedAt)
        mutations = [
            make_aspect_upsert(renewal_key, "terms", "terms",
                {"rentAmount": rent_amount, "termMonths": term_months, "setAt": set_at}),
        ]
        events = [{"class": "renewal.termsSet",
                   "data": {"renewalKey": renewal_key, "rentAmount": rent_amount, "termMonths": term_months}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": renewal_key}}

    if ot == "VerifyGuarantor":
        # Landlord guarantor-recheck leg. leaseApp + applicant are caller-
        # supplied (Starlark has no prefix scan to derive them from renewalKey
        # alone) and BOTH verified against deterministic links before use —
        # leaseApp against the renewal's OWN renews link, applicant against
        # that leaseapp's OWN applicationFor link (never trusted bare from the
        # payload, the Withdraw dual-endpoint-verification precedent) — then
        # the verified applicant's .profile. Rejects NoGuarantorToVerify when
        # there is no guarantor to verify.
        renewal_key = required_string(p, "renewalKey")
        _, renewal_id = parts_of(renewal_key, "renewalKey", "renewal")
        if not vertex_alive(state, renewal_key):
            fail("UnknownRenewal: " + renewal_key)

        app_key = required_string(p, "leaseApp")
        _, app_id = parts_of(app_key, "leaseApp", "leaseapp")
        renews_lnk = kv.Read("lnk.renewal." + renewal_id + ".renews.leaseapp." + app_id)
        if renews_lnk == None or renews_lnk.isDeleted:
            fail("LeaseAppMismatch: " + app_key + " is not the leaseapp renewal " + renewal_key + " renews")

        applicant = required_string(p, "applicant")
        _, applicant_id = parts_of(applicant, "applicant", "identity")
        app_for_lnk = kv.Read("lnk.leaseapp." + app_id + ".applicationFor.identity." + applicant_id)
        if app_for_lnk == None or app_for_lnk.isDeleted:
            fail("ApplicantMismatch: " + applicant + " is not the applicant of application " + app_key)

        # The qualification .profile aspect lives on the LEASEAPP (written by
        # SetApplicantProfile onto app_key), never on the applicant identity —
        # the applicant link above is what authenticates which leaseapp's
        # profile to read.
        profile = kv.Read(app_key + ".profile")
        has_guarantor = profile != None and not profile.isDeleted and profile.data.get("hasGuarantor") == True
        if not has_guarantor:
            fail("NoGuarantorToVerify: application " + app_key + " has no guarantor on file")

        verified_at = time.rfc3339_utc(op.submittedAt)
        method = optional_string(p, "method")
        gv_data = {"verifiedAt": verified_at}
        if method != None:
            gv_data["method"] = method
        mutations = [
            make_aspect_upsert(renewal_key, "guarantorVerification", "guarantorVerification", gv_data),
        ]
        events = [{"class": "renewal.guarantorVerified",
                   "data": {"renewalKey": renewal_key, "verifiedAt": verified_at}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": renewal_key}}

    if ot == "SignRenewal":
        # Tenant completion leg — the write-path mirror of the planner's
        # signRenewal terminal-leg pre (write-path honesty). leaseApp +
        # applicant are caller-supplied and link-verified exactly as
        # VerifyGuarantor does (the renewal's renews link, then that
        # leaseapp's applicationFor link) — a tampered payload cannot extend
        # an arbitrary leaseapp's .tenancy. On success: writes
        # .renewalSignature, flips status=complete, and — in the SAME batch —
        # extends the verified leaseapp's .tenancy (the CreateAppointment
        # multi-vertex-write precedent).
        renewal_key = required_string(p, "renewalKey")
        _, renewal_id = parts_of(renewal_key, "renewalKey", "renewal")
        renewal_vtx = state[renewal_key] if renewal_key in state else None
        if renewal_vtx == None or (hasattr(renewal_vtx, "isDeleted") and renewal_vtx.isDeleted):
            fail("UnknownRenewal: " + renewal_key)
        status = renewal_vtx.data.get("status")
        if status != "open":
            fail("RenewalNotOpen: renewal " + renewal_key + " is not open (status=" + str(status) + "); it has already been signed, completed, or cancelled")

        app_key = required_string(p, "leaseApp")
        _, app_id = parts_of(app_key, "leaseApp", "leaseapp")
        renews_lnk = kv.Read("lnk.renewal." + renewal_id + ".renews.leaseapp." + app_id)
        if renews_lnk == None or renews_lnk.isDeleted:
            fail("LeaseAppMismatch: " + app_key + " is not the leaseapp renewal " + renewal_key + " renews")

        applicant = required_string(p, "applicant")
        _, applicant_id = parts_of(applicant, "applicant", "identity")
        app_for_lnk = kv.Read("lnk.leaseapp." + app_id + ".applicationFor.identity." + applicant_id)
        if app_for_lnk == None or app_for_lnk.isDeleted:
            fail("ApplicantMismatch: " + applicant + " is not the applicant of application " + app_key)

        terms = kv.Read(renewal_key + ".terms")
        if terms == None or terms.isDeleted:
            fail("NotReadyToSign: renewal " + renewal_key + " has no .terms set yet")

        # See VerifyGuarantor: .profile lives on the LEASEAPP, not the identity.
        profile = kv.Read(app_key + ".profile")
        has_guarantor = profile != None and not profile.isDeleted and profile.data.get("hasGuarantor") == True
        if has_guarantor:
            gv = kv.Read(renewal_key + ".guarantorVerification")
            if gv == None or gv.isDeleted:
                fail("GuarantorNotVerified: renewal " + renewal_key + " requires guarantor verification before signing")

        signed_at = time.rfc3339_utc(op.submittedAt)
        term_months = terms.data.get("termMonths")

        tenancy_key = app_key + ".tenancy"
        tenancy = kv.Read(tenancy_key)
        if tenancy == None or tenancy.isDeleted:
            fail("NoTenancy: application " + app_key + " has no .tenancy aspect to extend")
        new_lease_end = add_months(tenancy.data.get("leaseEnd"), term_months)
        new_renewal_opens_at = time.rfc3339_add(new_lease_end, "-__RENEWAL_WINDOW__")

        mutations = [
            make_aspect(renewal_key, "renewalSignature", "renewalSignature", {"signedAt": signed_at}),
            make_vtx_update(renewal_key, "renewal",
                {"cycleEnd": renewal_vtx.data.get("cycleEnd"), "status": "complete"},
                renewal_vtx.revision),
            make_aspect_upsert(app_key, "tenancy", "tenancy", {
                "leaseStart": tenancy.data.get("leaseStart"),
                "leaseEnd": new_lease_end,
                "renewalOpensAt": new_renewal_opens_at,
            }),
        ]
        events = [{"class": "renewal.signed",
                   "data": {"renewalKey": renewal_key, "leaseAppKey": app_key, "leaseEnd": new_lease_end}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": renewal_key}}

    if ot == "CancelRenewal":
        # Landlord terminal decline. Rejects once signed; otherwise flips
        # status=cancelled (+ optional reason). Terminal in v1 (no revive op).
        renewal_key = required_string(p, "renewalKey")
        renewal_vtx = state[renewal_key] if renewal_key in state else None
        if renewal_vtx == None or (hasattr(renewal_vtx, "isDeleted") and renewal_vtx.isDeleted):
            fail("UnknownRenewal: " + renewal_key)
        sig = kv.Read(renewal_key + ".renewalSignature")
        if sig != None and not sig.isDeleted:
            fail("TermsLocked: renewal " + renewal_key + " is already signed; a signed cycle cannot be cancelled")

        reason = optional_string(p, "reason")
        new_data = {"cycleEnd": renewal_vtx.data.get("cycleEnd"), "status": "cancelled"}
        if reason != None:
            new_data["reason"] = reason
        mutations = [
            make_vtx_update(renewal_key, "renewal", new_data, renewal_vtx.revision),
        ]
        events = [{"class": "renewal.cancelled",
                   "data": {"renewalKey": renewal_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": renewal_key}}

    fail("renewal DDL: unknown operationType: " + ot)
`, "__RENEWAL_WINDOW__", renewalWindow, 1), "__RENEWAL_WINDOW_HOURS__", strconv.Itoa(renewalWindowHours), 1)
