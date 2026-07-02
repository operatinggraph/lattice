package bespokecontracts

// clauseDDLScript handles CreateClause. Known-key reads only (validates the
// lease + account by the keys the caller lists in ContextHint.Reads). Root
// data stays {} on the clause (D5): the prose/terms/status are aspects, the
// governed lease and charged account are links.
const clauseDDLScript = `
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

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateClause":
        lease_key = required_string(p, "leaseAppKey")
        _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")
        acct_key = required_string(p, "accountKey")
        _, acct_id = parts_of(acct_key, "accountKey", "account")
        prose = required_string(p, "prose")
        amount_cents = require_number(p, "amountCents")
        if amount_cents <= 0:
            fail("InvalidArgument: amountCents: required positive number")

        # No-orphan invariant: both the governed lease and the charged
        # account must be live.
        if not vertex_alive(state, lease_key):
            fail("UnknownLeaseApplication: " + lease_key)
        if not vertex_alive(state, acct_key):
            fail("UnknownAccount: " + acct_key)

        clause_id = nanoid.new()
        clause_key = "vtx.clause." + clause_id

        # governs / chargesTo: the clause (later-arriving) is the source, the
        # pre-existing lease / account is the target (Contract #1 §1.1).
        governs_lnk = "lnk.clause." + clause_id + ".governs.lease." + lease_id
        charges_lnk = "lnk.clause." + clause_id + ".chargesTo.account." + acct_id

        mutations = [
            make_vtx(clause_key, "clause", {}),
            make_aspect(clause_key, "prose", "clauseProse", {"text": prose}),
            make_aspect(clause_key, "terms", "clauseTerms",
                        {"kind": "computational", "amountCents": amount_cents, "period": "oneTime"}),
            make_aspect(clause_key, "status", "clauseStatus", {"state": "active"}),
            make_link(governs_lnk, clause_key, lease_key, "governs", "governs", {}),
            make_link(charges_lnk, clause_key, acct_key, "chargesTo", "chargesTo", {}),
        ]
        events = [{"class": "clause.created",
                   "data": {"clauseKey": clause_key, "leaseAppKey": lease_key,
                            "accountKey": acct_key, "amountCents": amount_cents}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": clause_key}}

    fail("clause DDL: unknown operationType: " + ot)
`

// aspectDeclarationOnlyScript is the declaration-only Starlark for
// clauseProse / clauseTerms / clauseStatus — written by CreateClause's (and,
// for clauseStatus, DebitAccount's) own op handler, never dispatched as an
// operation in its own right.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`
