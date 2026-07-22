package cafedomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations: `tab` (OpenTab,
// Charge, Settle), the `tabStatus` aspect-type declaration (the step-6 write
// gate for the .status aspect the tab vertexType DDL's own script writes),
// the `cafeOpenTabGuard` aspect-type declaration (the step-6 write gate
// for the per-lease open-tab dedup guard OpenTab/Settle maintain), the
// `menuItem` self-order catalog (CreateMenuItem, RetireMenuItem), and the
// `menuItemPrice` aspect-type declaration (the step-6 write gate for a menu
// item's .price aspect). Mirrors the known-key discipline of location-domain
// / loftspace-domain / clinic-domain: every op reads ONLY by known key, no
// prefix scans, no adjacency lookups, no lens-output reads.
//
// A tab is a short-lived POS session against a resident lease, settled into
// cafe-ledger's append-only cafeaccount/cafetransaction ledger (Café Inc 1,
// cafe-ledger-design.md) via the cafeTabSettlement Weaver target (targets.go)
// — cafe-domain's own op scripts never write a cafeaccount/cafetransaction
// mutation directly (the step-6 gate keys PermittedCommands by (operationType,
// class); only cafe-ledger's own DDLs permit CreateAccount/DebitAccount for
// those classes).
//
// menuItem is the catalog Charge binds a resident's self-order against: an
// operator mints priced items (CreateMenuItem) that a self-service Charge
// derives amountCents from (never trusting a caller-supplied number), the
// gap the original operator-only Charge grant existed to cover.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		tabVertexTypeDDL(),
		tabStatusAspectTypeDDL(),
		openTabGuardAspectTypeDDL(),
		menuItemVertexTypeDDL(),
		menuItemPriceAspectTypeDDL(),
	}
}

func tabVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "tab",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"OpenTab", "Charge", "Settle"},
		Description: "Café house-tab session DDL. Vertex shape: vtx.tab.<NanoID>, class=tab, root data = {} " +
			"(minimal, D5 — the running total lives on the .status aspect). OpenTab{leaseAppKey} validates the lease " +
			"is alive, rejects OpenTabAlreadyExists if the lease already has an open tab (the per-lease " +
			"cafeOpenTabGuard aspect on the leaseapp, mirroring cafe-ledger's cafeLedgerAccountGuard: a class-(d) " +
			"optionalReads dedup — create the guard fresh on a lease's first-ever tab, OCC-revive it from its prior " +
			"tombstone on a later one), mints the tab, writes .status {value: open, totalCents: 0, openedAt, " +
			"leaseAppKey} (leaseAppKey denormalized onto .status so Charge/Settle never need a second declared read " +
			"for the link target) and the openFor link (tab→leaseapp). Charge{tabKey, amountCents} (operator) adds " +
			"a positive amount to an OPEN tab's running total — an OCC-conditioned upsert of .status keyed on the " +
			"aspect's own current revision (the providerSlotClaim precedent: two concurrent charges racing the " +
			"same tab must not lose an update, so totalCents is a real accumulator, not an idempotent set). A " +
			"self-service caller instead submits Charge{tabKey, menuItemKey}: amountCents is derived from the " +
			"referenced menuItem's own .price.priceCents, never trusted from the caller (the menuItem catalog " +
			"this DDL's sibling exists to bound a self-submitted charge against). Settle{tabKey} closes an " +
			"OPEN tab (.status.value → settled, settledAt stamped, totalCents frozen), also OCC-conditioned, and " +
			"tombstones the lease's cafeOpenTabGuard so a later OpenTab can claim it again. Settling emits tab.settled " +
			"— the cafeTabSettlement lens (lenses.go) picks up a settled tab with totalCents>0 and dispatches the " +
			"resident's café-ledger posting (opening a cafeaccount via CreateAccount on first use, then " +
			"DebitAccount{tabRef}) through Weaver, never a direct cross-package write from this script. Both Charge " +
			"and Settle reject a tab that is not currently open (TabNotOpen) — a settled tab cannot be charged again " +
			"or double-settled. OpenTab, Charge, and Settle all grant scope=self to consumer: a resident may open, " +
			"self-order on, or settle a tab for their OWN lease only, verified via the lease's " +
			"applicationFor→identity link (AuthDenied otherwise).",
		Script: tabDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> the tab is opened for (OpenTab; required, validated alive)."},` +
			`"tabId":{"type":"string","description":"Optional bare NanoID for the new tab vertex (OpenTab); absent → minted."},` +
			`"tabKey":{"type":"string","description":"vtx.tab.<NanoID> of an existing tab (Charge/Settle; required, validated alive + open)."},` +
			`"amountCents":{"type":"number","description":"The charge amount in integer cents; required for an operator Charge, must be > 0."},` +
			`"menuItemKey":{"type":"string","description":"vtx.menuitem.<NanoID> of a live catalog item (self-service Charge only); amountCents is derived from it, ignoring any caller-supplied amountCents."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.tab.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"leaseAppKey": "Full vtx.leaseapp.<NanoID> key of the resident lease the tab is opened for (OpenTab; required, validated alive). Denormalized onto the tab's own .status aspect so Charge/Settle need no extra declared read to recover it.",
			"tabId":       "Optional bare NanoID (no dots / key segments) for the new tab vertex (vtx.tab.<tabId>). Absent → minted with nanoid.new() (OpenTab).",
			"tabKey":      "Full vtx.tab.<NanoID> key of an existing tab (Charge/Settle; required, validated alive + class=tab + currently open).",
			"amountCents": "The charge amount in integer cents; required for an operator Charge (must be a positive number), added to the tab's running .status.totalCents. Ignored for a self-service Charge — see menuItemKey.",
			"menuItemKey": "Full vtx.menuitem.<NanoID> key of a live catalog item (self-service Charge only; required when the caller has no operator grant). amountCents is derived from the item's own .price.priceCents, never trusted from the caller.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "OpenTab — start a house tab for a resident",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Validates the lease is alive. Mints vtx.tab.<NanoID> (root {}) + .status " +
					"{value: open, totalCents: 0, openedAt, leaseAppKey} + the openFor link (tab→leaseapp) + claims " +
					"the lease's cafeOpenTabGuard. Returns primaryKey (the tab key). Rejects UnknownLeaseApplication " +
					"if the lease is absent, or OpenTabAlreadyExists if the lease already has an open tab.",
			},
			{
				Name:    "Charge — ring up an item on an open tab (operator)",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>", "amountCents": 850},
				ExpectedOutcome: "Validates the tab is alive + open, adds 850 to .status.totalCents (OCC-conditioned " +
					"on the aspect's current revision). Returns primaryKey. Rejects TabNotOpen if the tab is already " +
					"settled, or InvalidArgument if amountCents <= 0.",
			},
			{
				Name:    "Charge — self-order against the menu catalog (resident)",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>", "menuItemKey": "vtx.menuitem.<NanoID>"},
				ExpectedOutcome: "Validates the tab is alive + open and the menu item is alive, derives the amount " +
					"from the item's .price.priceCents (any caller-supplied amountCents is ignored), adds it to " +
					".status.totalCents. Returns primaryKey. Rejects UnknownMenuItem if the item is absent or " +
					"retired, or AuthDenied if the tab's lease is not identified-by the caller.",
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

// openTabGuardAspectTypeDDL declares the .cafeOpenTab aspect (class
// cafeOpenTabGuard) OpenTab writes on the PRE-EXISTING leaseapp — the
// deterministic per-lease guard that enforces "at most one OPEN tab per
// lease at a time" (unlike cafe-ledger's cafeLedgerAccountGuard, which is a
// one-time-forever guard: a lease's café account never goes away, but its
// tab is a repeatable session, so this guard is claimed by OpenTab and
// released by Settle, over and over across the lease's life). The local
// name is vertical-prefixed (cafeOpenTab, not openTab) for the same reason
// cafeLedgerAccountGuard is: this leaseapp may carry other packages' own
// guard aspects, and a bare local name risks colliding key-for-key.
// Declaration-only: the aspect is written by OpenTab and tombstoned by
// Settle, never has its own operationType.
func openTabGuardAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "cafeOpenTabGuard",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"OpenTab", "Settle"},
		Description: "Per-lease open-tab uniqueness guard aspect. Stored as vtx.leaseapp.<NanoID>.cafeOpenTab " +
			"(class cafeOpenTabGuard) = {tabKey: <vtx.tab.<NanoID>>}. Non-sensitive. Claimed by OpenTab: a class-(d) " +
			"optionalReads dedup declared as <leaseAppKey>.cafeOpenTab — absent (the lease's first-ever tab, or any " +
			"prior tab already settled and its guard tombstoned) mints the guard fresh (create-only, the concurrent-" +
			"race backstop); present-but-tombstoned OCC-revives it keyed on its own current revision; present-and-" +
			"alive rejects the new OpenTab with OpenTabAlreadyExists. Released by Settle: an unconditioned tombstone " +
			"(mirrors clinic-domain's slot-cell release — a stale-tombstone race can only free the guard early, " +
			"never leave two tabs open) the moment the tab it names closes, so the very next OpenTab for this lease " +
			"finds it absent-or-tombstoned again.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"tabKey":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"tabKey": "The vtx.tab.<NanoID> currently holding this lease's open-tab slot.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "lease open-tab guard aspect",
				Payload:         map[string]any{"tabKey": "vtx.tab.<NanoID>"},
				ExpectedOutcome: "Stored as vtx.leaseapp.<NanoID>.cafeOpenTab; claimed by OpenTab, tombstoned by Settle.",
			},
		},
	}
}

// menuItemVertexTypeDDL declares the self-order catalog Charge binds
// against. Vertex shape: vtx.menuitem.<NanoID>, class=menuitem, root data =
// {} (D5 — name/price live on the .price aspect). Operator-only: mirrors
// service-domain's RetireServiceTemplate admin-cleanup precedent (a
// self-OCC'd tombstone), scaled down to cafe-domain's own simpler
// single-vertexType-DDL style (no envelope-class family discriminator
// needed — there is exactly one kind of menu item).
func menuItemVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "menuitem",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateMenuItem", "RetireMenuItem"},
		Description: "Café self-order menu-item catalog DDL. Vertex shape: vtx.menuitem.<NanoID>, class=menuitem, " +
			"root data = {} (D5 — name/price live on the .price aspect). CreateMenuItem{name, priceCents} " +
			"(operator-only) mints a catalog item + its .price {name, priceCents} aspect. RetireMenuItem{menuItemKey} " +
			"(operator-only) tombstones a live item, self-OCC'd on its hydrated revision (mirrors service-domain's " +
			"RetireServiceTemplate). Charge (tab vertexType DDL, above) reads a menu item's .price aspect by known " +
			"key when a self-service caller submits menuItemKey, deriving amountCents from priceCents rather than " +
			"trusting a caller-supplied number — the catalog this DDL exists to provide.",
		Script: menuItemDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"name":{"type":"string","description":"Menu item display name (CreateMenuItem; required, non-empty)."},` +
			`"priceCents":{"type":"number","description":"Price in integer cents; required, must be > 0 (CreateMenuItem)."},` +
			`"menuItemId":{"type":"string","description":"Optional bare NanoID for the new item (CreateMenuItem); absent → minted."},` +
			`"menuItemKey":{"type":"string","description":"vtx.menuitem.<NanoID> of an existing item (RetireMenuItem; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.menuitem.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"name":        "Menu item display name (CreateMenuItem; required, non-empty string), stored on the .price aspect.",
			"priceCents":  "The item's price in integer cents; required, must be a positive number (CreateMenuItem).",
			"menuItemId":  "Optional bare NanoID (no dots / key segments) for the new item (vtx.menuitem.<menuItemId>). Absent → minted with nanoid.new() (CreateMenuItem).",
			"menuItemKey": "Full vtx.menuitem.<NanoID> key of an existing item (RetireMenuItem; required, validated alive + class=menuitem).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateMenuItem — add a catalog item residents can self-order",
				Payload: map[string]any{"name": "Latte", "priceCents": 450},
				ExpectedOutcome: "Mints vtx.menuitem.<NanoID> (root {}) + .price {name: Latte, priceCents: 450}. " +
					"Returns primaryKey. Rejects InvalidArgument if name is empty or priceCents <= 0.",
			},
			{
				Name:    "RetireMenuItem — remove an item from the catalog",
				Payload: map[string]any{"menuItemKey": "vtx.menuitem.<NanoID>"},
				ExpectedOutcome: "Tombstones the item (self-OCC'd on its hydrated revision). Returns primaryKey. " +
					"Rejects UnknownMenuItem if already retired or absent.",
			},
		},
	}
}

// menuItemPriceAspectTypeDDL declares the .price aspect (class
// menuItemPrice) — the step-6 write gate for the menuItem vertexType DDL's
// own CreateMenuItem write. Declaration-only.
func menuItemPriceAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "menuItemPrice",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"CreateMenuItem"},
		Description: "Menu-item price aspect (café). Stored as vtx.menuitem.<NanoID>.price (class menuItemPrice) = " +
			"{name, priceCents}. Non-sensitive. Written once by CreateMenuItem, owned by the menuItem vertexType " +
			"DDL's own script. Declaration-only: no op handler of its own.",
		Script:       aspectDeclarationOnlyScript,
		InputSchema:  `{"type":"object","properties":{"name":{"type":"string"},"priceCents":{"type":"number"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"name":       "The item's display name.",
			"priceCents": "The item's price in integer cents.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "menu item price aspect",
				Payload:         map[string]any{"name": "Latte", "priceCents": 450},
				ExpectedOutcome: "Stored as vtx.menuitem.<NanoID>.price; written by CreateMenuItem.",
			},
		},
	}
}

// aspectDeclarationOnlyScript is the declaration-only Starlark for
// tabStatus / cafeOpenTabGuard / menuItemPrice — written by the tab and
// menuItem vertexType DDLs' own scripts, never dispatched as an operation in
// its own right.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

// tabDDLScript handles OpenTab, Charge, Settle. Known-key reads only: Charge
// and Settle both declare tabKey + tabKey+".status" in ContextHint.Reads so
// the current .status revision is hydrated for OCC conditioning (the
// providerSlotClaim precedent — an accumulator must not lose a concurrent
// update, unlike an idempotent status flip's unconditioned upsert). OpenTab
// declares <leaseAppKey>.cafeOpenTab in ContextHint.OptionalReads (Contract
// #2 §2.5 class-(d) read-before-create/dedup) so the per-lease open-tab
// guard's current state — absent, tombstoned, or alive — is hydrated
// without a live GET. A scope=self caller (OpenTab, Charge, Settle)
// additionally declares the lease's applicationFor→identity link in
// OptionalReads (also class-(d)) so the resident-self authorization check
// below can confirm the lease belongs to them without a live GET. A
// self-service Charge additionally declares menuItemKey + menuItemKey+
// ".price" in Reads (required — every live menuItem carries a .price
// aspect, so absence means an undeclared read, not a missing aspect).
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

def make_tombstone(key):
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

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

# --- workplace write confinement (facet-staff-worlds-design.md §3.5) ---------
#
# A staff actor may write only inside the location it worksAt. Three properties
# make this sound; each is a trap a simpler form falls into.
#
# 1. The exemption is ROLE-derived, never worksAt-derived. Exempting "an actor
#    with no worksAt link" would be perverse: UnwireWorksAt would WIDEN a staff
#    member's write surface from one building to everywhere. The exemption is
#    holding the primordial 'operator' role -- the same walk the kernel projects
#    its own root grant from (internal/bootstrap/lenses.go: MATCH (identity)
#    -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator'), so
#    an actor that is genuinely root necessarily has it. Everyone else is
#    confined, and an actor holding no roles at all is confined to nothing.
#
# 2. A tombstoned link is ABSENT. kv.Read returns the tombstone DOCUMENT rather
#    than None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), and
#    UnwireWorksAt tombstones rather than deletes, so the '== None' form the
#    cafe/clinic self-guards use would let a moved-on staff member keep writing.
#
# 3. The location is resolved from the TARGET's own topology, never from a
#    payload field -- a caller cannot forge which building it is writing at.
WORKPLACE_ROLE_PAGE_LIMIT = 50
WORKPLACE_PARENT_PAGE_LIMIT = 20
WORKPLACE_MAX_DEPTH = 8

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
    # roles, so this is never a keyspace scan. A role granted concurrently with
    # this write is not a race worth closing: it can only widen authority, and
    # the confined branch is the safe one.
    page, _ = kv.Links(actor_key, "holdsRole", "out", None, WORKPLACE_ROLE_PAGE_LIMIT)
    for lk in page:
        if lk.isDeleted:
            continue
        # read-posture: (e) per-candidate follow-up read off the enumeration
        # above (data-derived key -- the role is unknown until it resolves).
        cn = kv.Read(lk.targetVertex + ".canonicalName")
        if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
            return True
    return False

def worksAt_covers(actor_id, location_key):
    # Walks the location's containedIn chain upward, testing the actor's
    # deterministic worksAt link at each level. The location itself is tested
    # first, so a staff member wired to an exact unit matches too; one wired to
    # the building matches everything containedIn it.
    cur = location_key
    for _ in range(WORKPLACE_MAX_DEPTH):
        if cur == None:
            return False
        parts = cur.split(".")
        if len(parts) != 3:
            return False
        # read-posture: (e) per-candidate follow-up read off the containedIn
        # enumeration below (data-derived key -- the ancestor chain is not
        # knowable client-side, so it cannot be pre-declared).
        lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
        if lnk != None and not lnk.isDeleted:
            return True
        # read-posture: (e) relation=containedIn epoch=none -- a location has at
        # most a few parents; containment is provisioned topology, not written
        # concurrently with this op.
        page, _ = kv.Links(cur, "containedIn", "out", None, WORKPLACE_PARENT_PAGE_LIMIT)
        nxt = None
        for lk in page:
            if not lk.isDeleted:
                nxt = lk.targetVertex
        cur = nxt
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE a domain resolver
    # runs. Starlark evaluates arguments eagerly, so
    # require_workplace(resolve(x), ...) would walk the target's topology even
    # for root -- wasted reads, and worse, a malformed key anywhere in that walk
    # raises where the op previously succeeded. Call sites therefore gate on
    # this; require_workplace re-checks it anyway, so a site that forgets the
    # gate is still CORRECT, only slower.
    return op.authContextTarget != "" or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants, which
    # submit with no authContext (scope=any never sets one). A scope=self caller
    # is bound instead by its own op's ownership probe (the applicationFor /
    # identifiedBy indirection): a resident legitimately holds no worksAt link,
    # and confining them by a rule written for staff would deny every
    # self-service write. The two guards are complementary, not alternatives --
    # each binds the path the other cannot see.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
    if op.authContextTarget != "":
        return
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def leaseapp_unit(lease_key):
    # A tab's location is its lease's unit -- lease-signing's appliesToUnit link,
    # the same indirection landlordLeaseApplicationsRead anchors its building on.
    # read-posture: (e) relation=appliesToUnit epoch=none -- a leaseapp carries
    # exactly one appliesToUnit link (required at CreateLeaseApplication), so
    # this is never a keyspace scan.
    page, _ = kv.Links(lease_key, "appliesToUnit", "out")
    unit = None
    for lk in page:
        if not lk.isDeleted:
            unit = lk.targetVertex
    return unit

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

def require_menu_item_price(state, p):
    # A self-service Charge binds against the menuItem catalog rather than
    # trusting a caller-supplied amountCents (mirrors service-domain's
    # RequestService "derive, don't trust" posture: the family there comes
    # from the template's own envelope class, never a payload field — here
    # the amount comes from the item's own .price aspect). Every live
    # menuItem carries a .price aspect (CreateMenuItem writes it atomically
    # with the vertex), so an undeclared read here is a caller error, not a
    # legitimately-missing aspect.
    menu_item_key = required_string(p, "menuItemKey")
    parts_of(menu_item_key, "menuItemKey", "menuitem")
    if not vertex_alive(state, menu_item_key):
        fail("UnknownMenuItem: " + menu_item_key)
    if class_of(state, menu_item_key) != "menuitem":
        fail("WrongClass: menuItemKey: " + menu_item_key)
    price_key = menu_item_key + ".price"
    if price_key not in state:
        fail("InvalidArgument: menuItemKey: caller must declare " + price_key + " in contextHint.reads")
    price = state[price_key]
    if price == None or (hasattr(price, "isDeleted") and price.isDeleted):
        fail("UnknownMenuItem: " + menu_item_key + ": no .price aspect")
    return price.data.get("priceCents")

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "OpenTab":
        lease_key = required_string(p, "leaseAppKey")
        _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, lease_key):
            fail("UnknownLeaseApplication: " + lease_key)

        # Staff-standing confinement: a non-operator staff actor may only open a
        # tab against a lease whose unit sits inside its workplace. No-op on the
        # resident-self path, which the applicationFor probe below binds instead.
        if not workplace_exempt():
            require_workplace([leaseapp_unit(lease_key)], "cannot open a tab for lease " + lease_key)

        # Resident-self (consumer's scope=self grant only): step 3 authorizes
        # scope=self by checking authContext.target == actor (Contract #6),
        # but the op's endpoint is the LEASEAPP, not an identity — step 3
        # never sees the payload and has no notion of "this lease's
        # applicant" anyway. The script closes the gap by requiring the
        # target identity to be the lease's own applicant (lease-signing's
        # applicationFor link, the same patient/identifiedBy indirection
        # clinic-domain's CreateAppointment uses). Empty for the standing
        # operator grant (scope=any never sets authContext), so this check is
        # a no-op there — operator keeps opening tabs on behalf of any lease.
        if op.authContextTarget != "":
            _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
            application_for_lnk = "lnk.leaseapp." + lease_id + ".applicationFor.identity." + target_identity_id
            # read-posture: (d) declared in contextHint.optionalReads by the
            # self-service caller — it already knows both its own leaseAppKey
            # and its own authContext.target before submitting, so it
            # computes this key client-side and declares it.
            application_for = kv.Read(application_for_lnk)
            if application_for == None or application_for.isDeleted:
                fail("AuthDenied: a resident may only open a tab for their own lease")

        # One open tab per lease, guarded by a deterministic aspect on the
        # LEASEAPP (not the tab — the tab's own id is independent and
        # unknown until minted below). A class-(d) optionalReads dedup: the
        # caller always declares <leaseAppKey>.cafeOpenTab in
        # contextHint.optionalReads (absence-tolerant, unlike the
        # cafeLedgerAccountGuard precedent's required reads — here a repeat
        # OpenTab across the lease's life is the NORMAL flow, not just a
        # racing retry, so the guard key legitimately may or may not exist
        # yet). Absent → mint the guard fresh (create-only write is the
        # concurrent-race backstop for a genuine first-ever race). Present
        # but tombstoned (a prior tab already settled and released it) →
        # OCC-revive it keyed on its own current revision. Present and
        # alive → this lease already has an open tab, reject cleanly.
        guard_key = lease_key + ".cafeOpenTab"
        if guard_key in state:
            if vertex_alive(state, guard_key):
                fail("OpenTabAlreadyExists: " + lease_key)
            guard_revision = state[guard_key].revision
        else:
            guard_revision = None

        tab_id = bare_nanoid_or_mint(p, "tabId")
        tab_key = "vtx.tab." + tab_id
        opened_at = time.rfc3339_utc(op.submittedAt)

        # openFor: the tab (later-arriving) is the source, the pre-existing
        # lease is the target (Contract #1 §1.1). Reads as "tab openFor lease."
        open_for_lnk = "lnk.tab." + tab_id + ".openFor.leaseapp." + lease_id

        if guard_revision == None:
            guard_mut = make_aspect(lease_key, "cafeOpenTab", "cafeOpenTabGuard", {"tabKey": tab_key})
        else:
            guard_mut = make_aspect_upsert_occ(lease_key, "cafeOpenTab", "cafeOpenTabGuard",
                                                {"tabKey": tab_key}, guard_revision)

        mutations = [
            make_vtx(tab_key, "tab", {}),
            make_aspect(tab_key, "status", "tabStatus",
                        {"value": "open", "totalCents": 0, "openedAt": opened_at, "leaseAppKey": lease_key}),
            make_link(open_for_lnk, tab_key, lease_key, "openFor", "openFor", {}),
            guard_mut,
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

        is_self = op.authContextTarget != ""
        if is_self:
            # Self-order: the menuItem catalog bounds the amount — a
            # self-submitted amountCents is never read, let alone trusted.
            amount_cents = require_menu_item_price(state, p)
        else:
            amount_cents = require_number(p, "amountCents")
            if amount_cents <= 0:
                fail("InvalidArgument: amountCents: required positive number")

        existing = require_open_status(state, tab_key)

        # Staff-standing confinement: the lease comes from the tab's OWN .status
        # aspect (never the payload), so the workplace it resolves to cannot be
        # forged. Earliest point the location is derivable.
        if not workplace_exempt():
            require_workplace([leaseapp_unit(existing.data.get("leaseAppKey"))],
                              "cannot charge tab " + tab_key)

        # Resident-self ownership: same closure as Settle above — the lease
        # is recovered from the tab's OWN .status aspect, never from caller-
        # supplied payload.
        if is_self:
            _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
            lease_key = existing.data.get("leaseAppKey")
            lease_id = lease_key.split(".")[2]
            application_for_lnk = "lnk.leaseapp." + lease_id + ".applicationFor.identity." + target_identity_id
            # read-posture: (d) declared in contextHint.optionalReads by the
            # self-service caller (it knows its own tabKey + leaseAppKey +
            # authContext.target before submitting).
            application_for = kv.Read(application_for_lnk)
            if application_for == None or application_for.isDeleted:
                fail("AuthDenied: a resident may only charge their own tab")

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

        # Staff-standing confinement: same derivation as Charge — the lease comes
        # from the tab's own .status aspect, so the workplace cannot be forged.
        if not workplace_exempt():
            require_workplace([leaseapp_unit(lease_key)], "cannot settle tab " + tab_key)

        # Resident-self (consumer's scope=self grant only): same closure as
        # OpenTab above, but the lease is recovered from the tab's OWN
        # .status aspect (already declared/read for require_open_status),
        # never from caller-supplied payload — a caller declaring the wrong
        # leaseAppKey simply won't have the right composite key pre-hydrated,
        # so the read below returns None and this fails closed regardless.
        if op.authContextTarget != "":
            _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
            lease_id = lease_key.split(".")[2]
            application_for_lnk = "lnk.leaseapp." + lease_id + ".applicationFor.identity." + target_identity_id
            # read-posture: (d) declared in contextHint.optionalReads by the
            # self-service caller (it knows its own tabKey + leaseAppKey +
            # authContext.target before submitting).
            application_for = kv.Read(application_for_lnk)
            if application_for == None or application_for.isDeleted:
                fail("AuthDenied: a resident may only settle their own tab")

        status_data = {"value": "settled", "totalCents": total_cents,
                        "openedAt": existing.data.get("openedAt"),
                        "leaseAppKey": lease_key, "settledAt": settled_at}
        # Release the lease's open-tab guard so its next OpenTab can claim it
        # again — unconditioned, mirroring clinic-domain's slot-cell release
        # (a stale-tombstone race can only free the guard early, never leave
        # two tabs open; OpenTab's own OCC-revive is what actually
        # serializes a genuine race on the next claim).
        mutations = [
            make_aspect_upsert_occ(tab_key, "status", "tabStatus", status_data, existing.revision),
            make_tombstone(lease_key + ".cafeOpenTab"),
        ]
        events = [{"class": "tab.settled", "data": {"tabKey": tab_key, "leaseAppKey": lease_key, "totalCents": total_cents}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tab_key}}

    fail("tab DDL: unknown operationType: " + ot)
`

// menuItemDDLScript handles CreateMenuItem, RetireMenuItem. Known-key reads
// only: RetireMenuItem declares menuItemKey in ContextHint.Reads so the
// current revision is hydrated for the self-OCC'd tombstone (mirrors
// service-domain's RetireServiceTemplate).
const menuItemDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

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

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateMenuItem":
        name = required_string(p, "name")
        price_cents = require_number(p, "priceCents")
        if price_cents <= 0:
            fail("InvalidArgument: priceCents: required positive number")

        item_id = bare_nanoid_or_mint(p, "menuItemId")
        item_key = "vtx.menuitem." + item_id

        mutations = [
            make_vtx(item_key, "menuitem", {}),
            make_aspect(item_key, "price", "menuItemPrice", {"name": name, "priceCents": price_cents}),
        ]
        events = [{"class": "menuItem.created", "data": {"menuItemKey": item_key, "name": name, "priceCents": price_cents}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": item_key}}

    if ot == "RetireMenuItem":
        # Admin-only cleanup, mirrors service-domain's RetireServiceTemplate:
        # a tombstone mutation carries no document (the runtime writes
        # isDeleted:true + the lastModified* stamps only), self-OCC'd on the
        # hydrated revision so a concurrent mutation of the same item aborts
        # instead of racing.
        item_key = required_string(p, "menuItemKey")
        parts_of(item_key, "menuItemKey", "menuitem")
        if not vertex_alive(state, item_key):
            fail("UnknownMenuItem: " + item_key)

        mutations = [
            {"op": "tombstone", "key": item_key,
             "expectedRevision": state[item_key].revision},
        ]
        events = [{"class": "menuItem.retired", "data": {"menuItemKey": item_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": item_key}}

    fail("menuItem DDL: unknown operationType: " + ot)
`
