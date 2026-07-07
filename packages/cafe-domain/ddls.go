package cafedomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `tab` (OpenTab,
// Charge, Settle) and the `tabStatus` aspect-type declaration (the step-6
// write gate for the .status aspect the tab vertexType DDL's own script
// writes). Mirrors the known-key discipline of location-domain /
// loftspace-domain / clinic-domain: every op reads ONLY by known key, no
// prefix scans, no adjacency lookups, no lens-output reads.
//
// A tab is a short-lived POS session against a resident lease, settled into
// cafe-ledger's append-only cafeaccount/cafetransaction ledger (Café Inc 1,
// cafe-ledger-design.md) via the cafeTabSettlement Weaver target (targets.go)
// — cafe-domain's own op scripts never write a cafeaccount/cafetransaction
// mutation directly (the step-6 gate keys PermittedCommands by (operationType,
// class); only cafe-ledger's own DDLs permit CreateAccount/DebitAccount for
// those classes).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		tabVertexTypeDDL(),
		tabStatusAspectTypeDDL(),
	}
}

func tabVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "tab",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"OpenTab", "Charge", "Settle"},
		Description: "Café house-tab session DDL. Vertex shape: vtx.tab.<NanoID>, class=tab, root data = {} " +
			"(minimal, D5 — the running total lives on the .status aspect). OpenTab{leaseAppKey} validates the lease " +
			"is alive, mints the tab, writes .status {value: open, totalCents: 0, openedAt, leaseAppKey} (leaseAppKey " +
			"denormalized onto .status so Charge/Settle never need a second declared read for the link target) and " +
			"the openFor link (tab→leaseapp). Charge{tabKey, amountCents} adds a positive amount to an OPEN tab's " +
			"running total — an OCC-conditioned upsert of .status keyed on the aspect's own current revision (the " +
			"providerSlotClaim precedent: two concurrent charges racing the same tab must not lose an update, so " +
			"totalCents is a real accumulator, not an idempotent set). Settle{tabKey} closes an OPEN tab (.status.value " +
			"→ settled, settledAt stamped, totalCents frozen), also OCC-conditioned. Settling emits tab.settled — the " +
			"cafeTabSettlement lens (lenses.go) picks up a settled tab with totalCents>0 and dispatches the resident's " +
			"café-ledger posting (opening a cafeaccount via CreateAccount on first use, then DebitAccount{tabRef}) " +
			"through Weaver, never a direct cross-package write from this script. Both Charge and Settle reject a tab " +
			"that is not currently open (TabNotOpen) — a settled tab cannot be charged again or double-settled.",
		Script: tabDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> the tab is opened for (OpenTab; required, validated alive)."},` +
			`"tabId":{"type":"string","description":"Optional bare NanoID for the new tab vertex (OpenTab); absent → minted."},` +
			`"tabKey":{"type":"string","description":"vtx.tab.<NanoID> of an existing tab (Charge/Settle; required, validated alive + open)."},` +
			`"amountCents":{"type":"number","description":"The charge amount in integer cents; required, must be > 0 (Charge)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.tab.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"leaseAppKey": "Full vtx.leaseapp.<NanoID> key of the resident lease the tab is opened for (OpenTab; required, validated alive). Denormalized onto the tab's own .status aspect so Charge/Settle need no extra declared read to recover it.",
			"tabId":       "Optional bare NanoID (no dots / key segments) for the new tab vertex (vtx.tab.<tabId>). Absent → minted with nanoid.new() (OpenTab).",
			"tabKey":      "Full vtx.tab.<NanoID> key of an existing tab (Charge/Settle; required, validated alive + class=tab + currently open).",
			"amountCents": "The charge amount in integer cents; required, must be a positive number (Charge). Added to the tab's running .status.totalCents.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "OpenTab — start a house tab for a resident",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Validates the lease is alive. Mints vtx.tab.<NanoID> (root {}) + .status " +
					"{value: open, totalCents: 0, openedAt, leaseAppKey} + the openFor link (tab→leaseapp). Returns " +
					"primaryKey (the tab key). Rejects UnknownLeaseApplication if the lease is absent.",
			},
			{
				Name:    "Charge — ring up an item on an open tab",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>", "amountCents": 850},
				ExpectedOutcome: "Validates the tab is alive + open, adds 850 to .status.totalCents (OCC-conditioned " +
					"on the aspect's current revision). Returns primaryKey. Rejects TabNotOpen if the tab is already " +
					"settled, or InvalidArgument if amountCents <= 0.",
			},
			{
				Name:    "Settle — close a tab for house-account posting",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>"},
				ExpectedOutcome: "Validates the tab is alive + open, sets .status.value to settled and stamps " +
					"settledAt (OCC-conditioned; totalCents/leaseAppKey carried over unchanged). Emits tab.settled" +
					"{tabKey, leaseAppKey, totalCents}. Returns primaryKey. Rejects TabNotOpen if already settled.",
			},
		},
	}
}

// tabStatusAspectTypeDDL declares the .status aspect (class tabStatus) — the
// step-6 write gate for OpenTab (mints)/Charge (accumulates)/Settle (closes),
// all owned by the tab vertexType DDL's own script. Declaration-only.
func tabStatusAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "tabStatus",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"OpenTab", "Charge", "Settle"},
		Description: "Tab status aspect (café). Stored as vtx.tab.<NanoID>.status (class tabStatus) = " +
			"{value: open|settled, totalCents, openedAt, leaseAppKey, settledAt?}. Non-sensitive. Written by OpenTab " +
			"(mints, value=open, totalCents=0), Charge (OCC-conditioned accumulate onto totalCents), and Settle " +
			"(OCC-conditioned close, value=settled, settledAt stamped) — all owned by the tab vertexType DDL's script. " +
			"Declaration-only: no op handler of its own.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"value":{"type":"string","enum":["open","settled"]},"totalCents":{"type":"number"},"openedAt":{"type":"string"},"leaseAppKey":{"type":"string"},"settledAt":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"value":       "open | settled.",
			"totalCents":  "The tab's running total in integer cents, accumulated by Charge.",
			"openedAt":    "When the tab was opened (RFC3339, = OpenTab's op.submittedAt).",
			"leaseAppKey": "The resident lease this tab belongs to (denormalized from OpenTab's payload).",
			"settledAt":   "When the tab was settled (RFC3339, = Settle's op.submittedAt). Absent while open.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "tab status aspect",
				Payload:         map[string]any{"value": "open", "totalCents": 850, "openedAt": "2026-07-07T12:00:00Z", "leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Stored as vtx.tab.<NanoID>.status; written by OpenTab/Charge/Settle.",
			},
		},
	}
}

// aspectDeclarationOnlyScript is the declaration-only Starlark for
// tabStatus — written by the tab vertexType DDL's own script, never
// dispatched as an operation in its own right.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

// tabDDLScript handles OpenTab, Charge, Settle. Known-key reads only: Charge
// and Settle both declare tabKey + tabKey+".status" in ContextHint.Reads so
// the current .status revision is hydrated for OCC conditioning (the
// providerSlotClaim precedent — an accumulator must not lose a concurrent
// update, unlike an idempotent status flip's unconditioned upsert).
const tabDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert_occ(vtx_key, local_name, cls, data, expected_revision):
    m = {"op": "update", "key": vtx_key + "." + local_name,
         "document": {"class": cls, "isDeleted": False,
                      "vertexKey": vtx_key, "localName": local_name, "data": data}}
    m["expectedRevision"] = expected_revision
    return m

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

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
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

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_open_status(state, tab_key):
    # Every live tab carries a .status aspect (OpenTab writes it atomically
    # with the vertex), so absence here means the caller failed to declare it
    # in ContextHint.Reads, not a legitimately-missing aspect.
    status_key = tab_key + ".status"
    if status_key not in state:
        fail("InvalidArgument: tabKey: caller must declare " + status_key + " in contextHint.reads")
    existing = state[status_key]
    if existing == None or (hasattr(existing, "isDeleted") and existing.isDeleted):
        fail("UnknownTab: " + tab_key + ": no .status aspect")
    if existing.data.get("value") != "open":
        fail("TabNotOpen: " + tab_key + " is " + str(existing.data.get("value")))
    return existing

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "OpenTab":
        lease_key = required_string(p, "leaseAppKey")
        parts_of(lease_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, lease_key):
            fail("UnknownLeaseApplication: " + lease_key)

        tab_id = bare_nanoid_or_mint(p, "tabId")
        tab_key = "vtx.tab." + tab_id
        opened_at = time.rfc3339_utc(op.submittedAt)

        # openFor: the tab (later-arriving) is the source, the pre-existing
        # lease is the target (Contract #1 §1.1). Reads as "tab openFor lease."
        open_for_lnk = "lnk.tab." + tab_id + ".openFor.leaseapp." + lease_key.split(".")[2]

        mutations = [
            make_vtx(tab_key, "tab", {}),
            make_aspect(tab_key, "status", "tabStatus",
                        {"value": "open", "totalCents": 0, "openedAt": opened_at, "leaseAppKey": lease_key}),
            make_link(open_for_lnk, tab_key, lease_key, "openFor", "openFor", {}),
        ]
        events = [{"class": "tab.opened", "data": {"tabKey": tab_key, "leaseAppKey": lease_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tab_key}}

    if ot == "Charge":
        tab_key = required_string(p, "tabKey")
        parts_of(tab_key, "tabKey", "tab")
        if not vertex_alive(state, tab_key):
            fail("UnknownTab: " + tab_key)
        if class_of(state, tab_key) != "tab":
            fail("WrongClass: tabKey: " + tab_key)
        amount_cents = require_number(p, "amountCents")
        if amount_cents <= 0:
            fail("InvalidArgument: amountCents: required positive number")

        existing = require_open_status(state, tab_key)
        new_total = existing.data.get("totalCents") + amount_cents
        status_data = {"value": "open", "totalCents": new_total,
                        "openedAt": existing.data.get("openedAt"),
                        "leaseAppKey": existing.data.get("leaseAppKey")}
        mutations = [make_aspect_upsert_occ(tab_key, "status", "tabStatus", status_data, existing.revision)]
        events = [{"class": "tab.charged", "data": {"tabKey": tab_key, "amountCents": amount_cents, "totalCents": new_total}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tab_key}}

    if ot == "Settle":
        tab_key = required_string(p, "tabKey")
        parts_of(tab_key, "tabKey", "tab")
        if not vertex_alive(state, tab_key):
            fail("UnknownTab: " + tab_key)
        if class_of(state, tab_key) != "tab":
            fail("WrongClass: tabKey: " + tab_key)

        existing = require_open_status(state, tab_key)
        settled_at = time.rfc3339_utc(op.submittedAt)
        total_cents = existing.data.get("totalCents")
        lease_key = existing.data.get("leaseAppKey")
        status_data = {"value": "settled", "totalCents": total_cents,
                        "openedAt": existing.data.get("openedAt"),
                        "leaseAppKey": lease_key, "settledAt": settled_at}
        mutations = [make_aspect_upsert_occ(tab_key, "status", "tabStatus", status_data, existing.revision)]
        events = [{"class": "tab.settled", "data": {"tabKey": tab_key, "leaseAppKey": lease_key, "totalCents": total_cents}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tab_key}}

    fail("tab DDL: unknown operationType: " + ot)
`
