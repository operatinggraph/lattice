package cafedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

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
// menuItem is the catalog a Charge binds against: an operator mints priced
// items (CreateMenuItem) that a self-service OR a staff POS Charge can derive
// amountCents from (never trusting a caller-supplied number) by naming
// menuItemKey; a staff Charge with no menuItemKey still hand-keys amountCents
// for whatever the catalog does not cover.
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
		PermittedCommands: []string{"OpenTab", "Charge", "VoidCharge", "Settle", "SettleStaleTab", "BackfillTabStaleAt"},
		Description: "Café house-tab session DDL. Vertex shape: vtx.tab.<NanoID>, class=tab, root data = {} " +
			"(minimal, D5 — the running total lives on the .status aspect). OpenTab{leaseAppKey} validates the lease " +
			"is alive, rejects OpenTabAlreadyExists if the lease already has an open tab (the per-lease " +
			"cafeOpenTabGuard aspect on the leaseapp, mirroring cafe-ledger's cafeLedgerAccountGuard: a class-(d) " +
			"optionalReads dedup — create the guard fresh on a lease's first-ever tab, OCC-revive it from its prior " +
			"tombstone on a later one), mints the tab, writes .status {value: open, totalCents: 0, openedAt, " +
			"leaseAppKey} (leaseAppKey denormalized onto .status so Charge/Settle never need a second declared read " +
			"for the link target) plus staleAt = openedAt + 24h (precomputed — the cypher engine has no date-arithmetic " +
			"builtin, so the auto-settle deadline below must be derived at write time, not read time) and BOTH " +
			"tab→leaseapp links: chargedTo (permanent — where the money lands, the " +
			"anchor cafeTabSettlement walks to reach the lease's ledger-account guard) and openFor (transient — " +
			"that the tab is open). Charge{tabKey, amountCents} (operator, no menuItemKey — an off-menu charge the " +
			"catalog does not cover) adds " +
			"a positive amount to an OPEN tab's running total — an OCC-conditioned upsert of .status keyed on the " +
			"aspect's own current revision (the providerSlotClaim precedent: two concurrent charges racing the " +
			"same tab must not lose an update, so totalCents is a real accumulator, not an idempotent set). A " +
			"self-service OR an operator caller may instead submit Charge{tabKey, menuItemKey}: amountCents is " +
			"derived from the referenced menuItem's own .price.priceCents, never trusted from the caller (the " +
			"menuItem catalog this DDL's sibling exists to bound a charge against), and the item must be served at " +
			"the tab's own building (location_covers against the item's servedAt link) whichever caller names it. " +
			"Every Charge also appends the charged item's name (the menu item's own .price.name, or the caller's " +
			"optional description for an off-menu charge, defaulting to \"Off-menu charge\") to .status.itemsMemo, a " +
			"comma-joined running line so a tab (open or settled) shows what was actually rung up, not just the " +
			"total; a repeated identical name in the memo is exactly what a duplicate tap looks like. Every Charge " +
			"also appends a structured entry {id, description, amountCents, voided: false, orderedBy} to .status.lines " +
			"(id = \"line-\" + the 1-based position of the new entry, deterministic and collision-free within one " +
			"tab; orderedBy = op.actor, the identity that submitted THIS Charge — the resident on a self-order, the " +
			"staffer on a POS ring-up, distinguishing the two on the itemized receipt where itemsMemo cannot), " +
			"the itemized breakdown a receipt renders instead of the flat memo string; a tab whose .status predates " +
			"this field (no lines key at all, or a line predating orderedBy) is treated as lines=[] / orderedBy " +
			"absent respectively and simply accrues no itemized entries until its next Charge, itemsMemo staying " +
			"the only record of what it already carried. " +
			"VoidCharge (operator/frontOfHouse only — no self-service grant, a POS correction is a staff decision " +
			"even when reversing a resident's own self-order mis-tap) has two forms. VoidCharge{tabKey, lineId} " +
			"voids one specific .status.lines entry by its id: rejects UnknownChargeLine if no live (non-voided) " +
			"line with that id exists on the tab, otherwise derives the amount from the line itself (never " +
			"trusting a caller-supplied amountCents, the same \"derive, don't trust\" posture Charge's own " +
			"menuItemKey branch uses) and marks that line voided:true in place. VoidCharge{tabKey, amountCents} " +
			"(legacy/off-menu form, no lineId — e.g. correcting a tab whose charge predates itemized lines) " +
			"subtracts the given positive amount without touching .status.lines at all. Either form then subtracts " +
			"the resolved amount from the OPEN tab's running total, same OCC-conditioned upsert as Charge, clamped " +
			"at 0 rather than rejected when the void exceeds the current total (an over-void is a caller mistake " +
			"worth correcting cleanly, not a hard failure), and appends \"Void correction\" to itemsMemo when it " +
			"actually reduced the total (a void against an already-0 tab appends nothing — there was no charge to " +
			"correct). " +
			"Settle{tabKey} closes an " +
			"OPEN tab (.status.value → settled, settledAt stamped, totalCents AND itemsMemo frozen), also OCC-conditioned, and " +
			"tombstones both the lease's cafeOpenTabGuard (so a later OpenTab can claim it again) and the tab's own " +
			"openFor link (so the tab leaves every resident's edgeEntityTabs read grant, which walks that hop and " +
			"cannot see the .status aspect the lens tail filters on). chargedTo is deliberately left standing — the " +
			"settlement convergence below anchors on it and runs AFTER this. Settling emits tab.settled " +
			"— the cafeTabSettlement lens (lenses.go) picks up a settled tab with totalCents>0 and dispatches the " +
			"resident's café-ledger posting (opening a cafeaccount via CreateAccount on first use, then " +
			"DebitAccount{tabRef}) through Weaver, never a direct cross-package write from this script. Charge, " +
			"VoidCharge, and Settle all reject a tab that is not currently open (TabNotOpen) — a settled tab's " +
			"total is frozen and already dispatched to the ledger, so it cannot be charged, voided, or settled again. " +
			"OpenTab, Charge, and Settle all grant scope=self to consumer: a resident may open, " +
			"self-order on, or settle a tab for their OWN lease only, verified via the lease's " +
			"applicationFor→identity link (AuthDenied otherwise). " +
			"SettleStaleTab{tabKey} (operator only, orchestration-internal — no human dispatches it) is the " +
			"auto-settle twin of Settle, dispatched by cafeStaleTabSettlement (lenses.go) once an OPEN tab's own " +
			"staleAt deadline passes with no staff Settle; it no-ops cleanly (rather than rejecting TabNotOpen) if a " +
			"staff Settle already won the race. A dedicated operationType rather than a directOp against Settle " +
			"itself, because Settle's chargedTo-backfill branch reads a LINK key a Weaver GapActionSpec's Reads " +
			"cannot template (row.<column> only) — SettleStaleTab confirms chargedTo via a bounded kv.Links read " +
			"instead of a declared one. " +
			"BackfillTabStaleAt{tabKey} (operator only, orchestration-internal) is the auto-remediation twin of " +
			"OpenTab's staleAt write, dispatched by cafeStaleTabSettlement's missing_staleat gap for an OPEN tab " +
			"whose .status carries no staleAt at all — every tab opened before the staleAt feature shipped, which " +
			"missing_settle alone can never see (a null staleAt compares false against both '>' and '<=', so such a " +
			"tab was previously invisible to the whole convergence). Computes the same openedAt + 24h OpenTab would " +
			"have written and backfills it via an OCC-conditioned upsert; no-ops cleanly if the tab is already " +
			"settled or staleAt is already present (a race with another dispatch, or a redelivery).",
		Script: tabDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> the tab is opened for (OpenTab; required, validated alive)."},` +
			`"tabId":{"type":"string","description":"Optional bare NanoID for the new tab vertex (OpenTab); absent → minted."},` +
			`"tabKey":{"type":"string","description":"vtx.tab.<NanoID> of an existing tab (Charge/VoidCharge/Settle/SettleStaleTab/BackfillTabStaleAt; required, validated alive + open)."},` +
			`"amountCents":{"type":"number","description":"The amount in integer cents; required for an off-menu Charge (no menuItemKey — added to the total) or a lineId-less (legacy) VoidCharge (subtracted, clamped at 0), must be > 0. Ignored by a VoidCharge that names lineId — the amount is derived from the line itself."},` +
			`"menuItemKey":{"type":"string","description":"vtx.menuitem.<NanoID> of a live catalog item; amountCents is derived from it, ignoring any caller-supplied amountCents. Required for a self-service Charge; optional for a staff Charge (its absence means an off-menu, hand-keyed amountCents charge)."},` +
			`"description":{"type":"string","description":"Optional free-text name for an off-menu Charge's line in .status.itemsMemo/.status.lines (no menuItemKey — a catalog item's own name is used instead). Defaults to \"Off-menu charge\" when omitted."},` +
			`"lineId":{"type":"string","description":"VoidCharge only: the .status.lines entry id to void (e.g. \"line-2\"). When present, amountCents is ignored and the void amount is derived from the named line instead; rejects UnknownChargeLine if no live (non-voided) line with that id exists. Absent → the legacy amountCents-only void, which does not touch .status.lines."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.tab.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"leaseAppKey": "Full vtx.leaseapp.<NanoID> key of the resident lease the tab is opened for (OpenTab; required, validated alive). Denormalized onto the tab's own .status aspect so Charge/Settle need no extra declared read to recover it.",
			"tabId":       "Optional bare NanoID (no dots / key segments) for the new tab vertex (vtx.tab.<tabId>). Absent → minted with nanoid.new() (OpenTab).",
			"tabKey":      "Full vtx.tab.<NanoID> key of an existing tab (Charge/VoidCharge/Settle/SettleStaleTab/BackfillTabStaleAt; required, validated alive + class=tab + currently open).",
			"amountCents": "The amount in integer cents; required for an off-menu Charge (must be a positive number, added to the tab's running .status.totalCents) or a lineId-less VoidCharge (must be a positive number, subtracted from .status.totalCents and clamped at 0). Ignored whenever menuItemKey is present (Charge) or lineId is present (VoidCharge).",
			"menuItemKey": "Full vtx.menuitem.<NanoID> key of a live catalog item, served at the tab's own building. Required for a self-service Charge; optional for a staff Charge (present → catalog-priced like self-order; absent → hand-keyed amountCents). amountCents is derived from the item's own .price.priceCents, never trusted from the caller, whichever caller names it.",
			"description": "Optional free-text line name for an off-menu Charge (no menuItemKey) — appended to .status.itemsMemo and recorded as the new .status.lines entry's description, instead of a catalog item's own name. Defaults to \"Off-menu charge\" when omitted. Ignored whenever menuItemKey is present (the item's own name is used).",
			"lineId":      "VoidCharge only: the id of a .status.lines entry (e.g. \"line-2\") to void by reference. The void amount is derived from the line itself, never trusted from a caller-supplied amountCents. Rejects UnknownChargeLine if no live (non-voided) line with that id exists on the tab. Absent → the legacy amountCents-only void.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "OpenTab — start a house tab for a resident",
				Payload: map[string]any{"leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Validates the lease is alive. Mints vtx.tab.<NanoID> (root {}) + .status " +
					"{value: open, totalCents: 0, itemsMemo: \"\", lines: [], openedAt, leaseAppKey} + the chargedTo and openFor links " +
					"(both tab→leaseapp) + claims " +
					"the lease's cafeOpenTabGuard. Returns primaryKey (the tab key). Rejects UnknownLeaseApplication " +
					"if the lease is absent, or OpenTabAlreadyExists if the lease already has an open tab.",
			},
			{
				Name:    "Charge — ring up an off-menu item on an open tab (operator)",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>", "amountCents": 850, "description": "Late checkout fee"},
				ExpectedOutcome: "Validates the tab is alive + open, adds 850 to .status.totalCents (OCC-conditioned " +
					"on the aspect's current revision), appends \"Late checkout fee\" to .status.itemsMemo (or " +
					"\"Off-menu charge\" if description is omitted) and a matching {id, description, amountCents: 850, " +
					"voided: false, orderedBy: op.actor} entry to .status.lines. Returns primaryKey. Rejects TabNotOpen if the tab " +
					"is already settled, or InvalidArgument if amountCents <= 0.",
			},
			{
				Name:    "Charge — ring up a catalog item at the POS (staff)",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>", "menuItemKey": "vtx.menuitem.<NanoID>"},
				ExpectedOutcome: "Validates the tab is alive + open and the menu item is alive and served at the " +
					"tab's own building, derives the amount from the item's .price.priceCents (any caller-supplied " +
					"amountCents is ignored), adds it to .status.totalCents and appends the item's own name to " +
					".status.itemsMemo + a matching .status.lines entry. Returns primaryKey. Rejects " +
					"UnknownMenuItem if the item is absent or retired, or AuthDenied if the item is served at a " +
					"different building.",
			},
			{
				Name:    "Charge — self-order against the menu catalog (resident)",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>", "menuItemKey": "vtx.menuitem.<NanoID>"},
				ExpectedOutcome: "Validates the tab is alive + open and the menu item is alive, derives the amount " +
					"from the item's .price.priceCents (any caller-supplied amountCents is ignored), adds it to " +
					".status.totalCents and appends the item's own name to .status.itemsMemo + a matching " +
					".status.lines entry. Returns primaryKey. " +
					"Rejects UnknownMenuItem if the item is absent or " +
					"retired, or AuthDenied if the tab's lease is not identified-by the caller.",
			},
			{
				Name:    "VoidCharge — void one specific line by reference (operator/frontOfHouse only)",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>", "lineId": "line-2"},
				ExpectedOutcome: "Validates the tab is alive + open and line-2 exists and is not already voided on " +
					".status.lines, derives the void amount from that line's own amountCents (never trusting a " +
					"caller-supplied amountCents), subtracts it from .status.totalCents (OCC-conditioned, clamped " +
					"at 0), marks the line voided:true in place, and appends \"Void correction\" to .status.itemsMemo " +
					"when the total actually decreased. Returns primaryKey. Rejects TabNotOpen if the tab is already " +
					"settled, or UnknownChargeLine if line-2 is absent or already voided.",
			},
			{
				Name:    "VoidCharge — legacy amount-only correction, no line reference (operator/frontOfHouse only)",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>", "amountCents": 450},
				ExpectedOutcome: "Validates the tab is alive + open, subtracts 450 from .status.totalCents " +
					"(OCC-conditioned on the aspect's current revision), clamped at 0 rather than going negative, " +
					"leaves .status.lines untouched, and appends \"Void correction\" to .status.itemsMemo when the " +
					"total actually decreased. " +
					"Returns primaryKey. Rejects TabNotOpen if the tab is already settled, or InvalidArgument if " +
					"amountCents <= 0.",
			},
			{
				Name:    "Settle — close a tab for house-account posting",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>"},
				ExpectedOutcome: "Validates the tab is alive + open, sets .status.value to settled and stamps " +
					"settledAt (OCC-conditioned; totalCents/itemsMemo/lines/leaseAppKey carried over unchanged), and tombstones the " +
					"lease's cafeOpenTabGuard + the tab's openFor link (chargedTo stays — settlement anchors on it). " +
					"Emits tab.settled" +
					"{tabKey, leaseAppKey, totalCents}. Returns primaryKey. Rejects TabNotOpen if already settled.",
			},
			{
				Name:    "SettleStaleTab — auto-settle a tab nobody closed (orchestration-internal)",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>"},
				ExpectedOutcome: "Validates the tab is alive, closes it exactly like Settle (settled, settledAt stamped, " +
					"the lease's cafeOpenTabGuard + openFor released), confirming chargedTo via a bounded live link check " +
					"rather than a declared read. Returns primaryKey. No-ops cleanly (empty mutations/events) if the tab " +
					"is already settled — a staff Settle raced this dispatch and won.",
			},
			{
				Name:    "BackfillTabStaleAt — backfill a legacy tab's missing staleAt (orchestration-internal)",
				Payload: map[string]any{"tabKey": "vtx.tab.<NanoID>"},
				ExpectedOutcome: "Validates the tab is alive + open, computes staleAt = openedAt + 24h (the value " +
					"OpenTab would have written), and upserts it onto .status (OCC-conditioned). Returns primaryKey. " +
					"No-ops cleanly (empty mutations/events) if the tab is already settled or already carries a " +
					"staleAt.",
			},
		},
	}
}

// tabStatusAspectTypeDDL declares the .status aspect (class tabStatus) — the
// step-6 write gate for OpenTab (mints)/Charge (accumulates)/VoidCharge
// (decrements)/Settle (closes), all owned by the tab vertexType DDL's own
// script. Declaration-only.
func tabStatusAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "tabStatus",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"OpenTab", "Charge", "VoidCharge", "Settle", "SettleStaleTab", "BackfillTabStaleAt"},
		Description: "Tab status aspect (café). Stored as vtx.tab.<NanoID>.status (class tabStatus) = " +
			"{value: open|settled, totalCents, itemsMemo, lines, openedAt, staleAt, leaseAppKey, settledAt?}. Non-sensitive. Written by OpenTab " +
			"(mints, value=open, totalCents=0, itemsMemo=\"\", lines=[], staleAt=openedAt+24h), Charge (OCC-conditioned accumulate onto totalCents, " +
			"appends the charged item's name to itemsMemo and a matching {id, description, amountCents, voided: false, orderedBy: op.actor} entry to lines, " +
			"carries staleAt forward unchanged), VoidCharge " +
			"(OCC-conditioned decrement of totalCents, clamped at 0, appends \"Void correction\" to itemsMemo when the " +
			"total actually decreased; a lineId-targeted void additionally marks that lines entry voided:true in place, " +
			"a legacy amountCents-only void leaves lines untouched; carries staleAt forward unchanged), Settle/SettleStaleTab " +
			"(OCC-conditioned close, value=settled, settledAt stamped, totalCents/itemsMemo/lines carried over frozen, staleAt dropped — " +
			"no longer meaningful once settled), and BackfillTabStaleAt (OCC-conditioned backfill of a missing staleAt on a tab opened " +
			"before that field shipped, computed the same way OpenTab computes it; a no-op once staleAt is already present) — " +
			"all owned by the tab vertexType DDL's script. " +
			"Declaration-only: no op handler of its own.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"value":{"type":"string","enum":["open","settled"]},"totalCents":{"type":"number"},"itemsMemo":{"type":"string"},` +
			`"lines":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"description":{"type":"string"},"amountCents":{"type":"number"},"voided":{"type":"boolean"},"orderedBy":{"type":"string"}}}},` +
			`"openedAt":{"type":"string"},"staleAt":{"type":"string"},"leaseAppKey":{"type":"string"},"settledAt":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"value":       "open | settled.",
			"totalCents":  "The tab's running total in integer cents, accumulated by Charge.",
			"itemsMemo":   "A comma-joined, running line of what was charged — each Charge appends the item's own name (or an off-menu charge's caller-supplied/default description), each qualifying VoidCharge appends \"Void correction\". Empty string on a fresh tab. Frozen by Settle (never rewritten after).",
			"lines":       "The itemized breakdown a receipt renders instead of the flat itemsMemo string: a list of {id, description, amountCents, voided, orderedBy}, one entry per Charge, in charge order. id is \"line-\" + the entry's 1-based position (deterministic, unique within one tab). orderedBy is op.actor from the Charge that created the line — the resident's own identity on a self-order, the staffer's on a POS ring-up — so a shared house tab's receipt can tell the two apart; a line predating this field carries no orderedBy key at all, read as unknown. A lineId-targeted VoidCharge marks the matching entry voided:true rather than removing it, so a voided line still shows on the receipt struck through. A tab whose .status predates this field carries no lines key at all — read it as []. Empty list on a fresh tab. Frozen by Settle (never rewritten after).",
			"openedAt":    "When the tab was opened (RFC3339, = OpenTab's op.submittedAt).",
			"staleAt":     "RFC3339, = openedAt + 24h (OpenTab). The cafeStaleTabSettlement convergence lens (lenses.go) auto-dispatches SettleStaleTab once this passes with the tab still open, or BackfillTabStaleAt if it is absent entirely (a tab opened before this field shipped). Carried forward unchanged by Charge/VoidCharge; dropped by Settle/SettleStaleTab once settled.",
			"leaseAppKey": "The resident lease this tab belongs to (denormalized from OpenTab's payload).",
			"settledAt":   "When the tab was settled (RFC3339, = Settle's op.submittedAt). Absent while open.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "tab status aspect",
				Payload: map[string]any{"value": "open", "totalCents": 850, "itemsMemo": "Latte, Croissant", "lines": []any{
					map[string]any{"id": "line-1", "description": "Latte", "amountCents": 450, "voided": false},
					map[string]any{"id": "line-2", "description": "Croissant", "amountCents": 400, "voided": false},
				}, "openedAt": "2026-07-07T12:00:00Z", "staleAt": "2026-07-08T12:00:00Z", "leaseAppKey": "vtx.leaseapp.<NanoID>"},
				ExpectedOutcome: "Stored as vtx.tab.<NanoID>.status; written by OpenTab/Charge/VoidCharge/Settle/SettleStaleTab.",
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
//
// A catalog item is anchored to the place that serves it by a `servedAt` link
// (menuitem → location). The relation is what makes the item REACHABLE: an
// edge-manifest browse lens walks a resident's residence chain down to the
// items served where they live, and that walk is a linear pattern over named
// relations (internal/pkgmgr/anchorwalk.go), so an item with no edge is an item
// no walk can offer. It is also the honest shape — a menu belongs to a café,
// not to the deployment.
func menuItemVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "menuitem",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateMenuItem", "RetireMenuItem"},
		Description: "Café self-order menu-item catalog DDL. Vertex shape: vtx.menuitem.<NanoID>, class=menuitem, " +
			"root data = {} (D5 — name/price live on the .price aspect). CreateMenuItem{name, priceCents, locationKey} " +
			"(operator-only) mints a catalog item + its .price {name, priceCents} aspect + the servedAt link " +
			"(menuitem→location, the item being the later-arriving vertex), rejecting UnknownLocation / NotALocation " +
			"if locationKey is absent, tombstoned, or not a location. That link is the item's only reachability: an " +
			"edge-manifest browse lens walks a resident's residence chain down to the items served where they live, " +
			"so an unlinked item is one no client can offer. RetireMenuItem{menuItemKey} " +
			"(operator-only) tombstones a live item, self-OCC'd on its hydrated revision (mirrors service-domain's " +
			"RetireServiceTemplate). Charge (tab vertexType DDL, above) reads a menu item's .price aspect by known " +
			"key when a self-service caller submits menuItemKey, deriving amountCents from priceCents rather than " +
			"trusting a caller-supplied number — the catalog this DDL exists to provide.",
		Script: menuItemDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"name":{"type":"string","description":"Menu item display name (CreateMenuItem; required, non-empty)."},` +
			`"priceCents":{"type":"number","description":"Price in integer cents; required, must be > 0 (CreateMenuItem)."},` +
			`"locationKey":{"type":"string","description":"vtx.<locationType>.<NanoID> of the place that serves this item (CreateMenuItem; required, validated alive + an admitted location type segment)."},` +
			`"menuItemId":{"type":"string","description":"Optional bare NanoID for the new item (CreateMenuItem); absent → minted."},` +
			`"menuItemKey":{"type":"string","description":"vtx.menuitem.<NanoID> of an existing item (RetireMenuItem; required, validated alive)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.menuitem.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"name":        "Menu item display name (CreateMenuItem; required, non-empty string), stored on the .price aspect.",
			"priceCents":  "The item's price in integer cents; required, must be a positive number (CreateMenuItem).",
			"locationKey": "Full vtx.<locationType>.<NanoID> key (unit|building|property — the class equals the key type) of the place that serves this item (CreateMenuItem; required, validated alive + an admitted location type segment). Becomes the servedAt link, which is what makes the item reachable from a resident of that place.",
			"menuItemId":  "Optional bare NanoID (no dots / key segments) for the new item (vtx.menuitem.<menuItemId>). Absent → minted with nanoid.new() (CreateMenuItem).",
			"menuItemKey": "Full vtx.menuitem.<NanoID> key of an existing item (RetireMenuItem; required, validated alive + class=menuitem).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "CreateMenuItem — add a catalog item residents can self-order",
				Payload: map[string]any{"name": "Latte", "priceCents": 450, "locationKey": "vtx.unit.<NanoID>"},
				ExpectedOutcome: "Mints vtx.menuitem.<NanoID> (root {}) + .price {name: Latte, priceCents: 450} + " +
					"lnk.menuitem.<NanoID>.servedAt.<locationType>.<NanoID>. " +
					"Returns primaryKey. Rejects InvalidArgument if name is empty or priceCents <= 0, " +
					"UnknownLocation / NotALocation if locationKey is absent, tombstoned, or not a location.",
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

// tabDDLScript handles OpenTab, Charge, VoidCharge, Settle. Known-key reads
// only: Charge, VoidCharge, and Settle all declare tabKey + tabKey+".status"
// in ContextHint.Reads so the current .status revision is hydrated for OCC
// conditioning (the providerSlotClaim precedent — an accumulator must not
// lose a concurrent update, unlike an idempotent status flip's unconditioned
// upsert). VoidCharge carries no self-scope grant (permissions.go) — a POS
// correction is a staff decision even when it reverses a resident's own
// self-order mis-tap, so the script has no ownership-check branch for it
// (the capability plane denies a scope=self caller before this script ever
// runs). OpenTab
// declares <leaseAppKey>.cafeOpenTab in ContextHint.OptionalReads (Contract
// #2 §2.5 class-(d) read-before-create/dedup) so the per-lease open-tab
// guard's current state — absent, tombstoned, or alive — is hydrated
// without a live GET. A scope=self caller (OpenTab, Charge, Settle)
// additionally declares the lease's applicationFor→identity link in
// OptionalReads (also class-(d)) so the resident-self authorization check
// below can confirm the lease belongs to them without a live GET. A
// A self-service OR a staff catalog Charge additionally declares menuItemKey
// + menuItemKey + ".price" in Reads (required — every live menuItem carries
// a .price aspect, so absence means an undeclared read, not a missing
// aspect); a staff Charge with no menuItemKey hand-keys amountCents instead.
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
    return {"op": "tombstone", "key": key}

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
ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4
WORKPLACE_PARENT_PAGE_LIMIT = 20
MAX_PARENT_PAGES = 4
WORKPLACE_MAX_DEPTH = 8
WORKPLACE_MAX_NODES = 64

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # Paginated: a role beyond page 1 must not read as "not held" -- the walk
    # follows the cursor up to MAX_ROLE_PAGES pages before giving up, and
    # giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan. A role granted concurrently with
        # this write is not a race worth closing: it can only widen authority, and
        # the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def worksAt_covers(actor_id, location_key):
    # Answers "does this actor worksAt this location, or any LIVE location that
    # contains it?" -- a BREADTH-first walk up the containedIn topology, testing
    # the actor's deterministic worksAt link at every node. The location itself
    # is tested first, so a staff member wired to an exact unit matches too; one
    # wired to any containing building matches everything containedIn it.
    #
    # A tombstoned link OR VERTEX is absent. kv.Read returns the tombstone
    # document rather than None (step4_hydrate routes only ErrKeyNotFound to
    # knownAbsent), and UnwireWorksAt / TombstoneLocation tombstone rather than
    # delete, so isDeleted is tested explicitly in three places: the worksAt
    # link, each containedIn link, and every location VERTEX the walk stands on.
    # The vertex test is what stops a DECOMMISSIONED location from still
    # conferring authority -- TombstoneLocation does not cascade to containedIn
    # links (location-domain), so those links stay live and only the vertex's own
    # isDeleted marks it gone, while the read side stops dead there (the full
    # engine's fetchNode yields nothing for a soft-deleted node). Transiting one
    # would grant a write the reader would never show.
    #
    # It is tested on EVERY node, the caller-supplied one included, not just on
    # ancestors: a guard where a dead ancestor confers nothing but a dead
    # starting location confers everything would be exactly the kind of
    # inconsistency the next reader copies wrongly.
    #
    # EVERY parent is followed, not one per level: containment is a DAG. A walk
    # that kept a single parent would deny a staffer wired to whichever branch it
    # happened to discard, while a read-side lens projecting a covering set
    # unions every branch of [:containedIn*0..7] (cafe-domain's and
    # wellness-domain's coveringLocations are the two that do).
    #
    # Bounded three ways so an op-time guard cannot fan out: WORKPLACE_MAX_DEPTH
    # levels (0..7, the read side's hop range), WORKPLACE_PARENT_PAGE_LIMIT
    # parents per node, and WORKPLACE_MAX_NODES distinct nodes overall, a node
    # never being enqueued twice. Exhausting a bound falls through to the final
    # 'return False' -- a DENIAL, never an escape. The node budget is the one
    # bound the read side does not share (its walk caps hops, not nodes), so a
    # containment tree wide enough to exhaust it denies a write the reader would
    # show; it is set far above any real topology, and it fails closed.
    if location_key == None:
        return False
    frontier = [location_key]
    seen = [location_key]
    for _ in range(WORKPLACE_MAX_DEPTH):
        if len(frontier) == 0:
            return False
        parents = []
        for cur in frontier:
            parts = cur.split(".")
            if len(parts) != 3:
                # Not walkable. Stops its OWN branch rather than aborting the
                # walk, so one malformed ancestor cannot deny a sibling branch
                # that would have matched. A malformed location_key still
                # denies: nothing else is queued, so the frontier empties.
                continue
            # read-posture: (e) per-candidate follow-up read off the containedIn
            # enumeration below -- the location VERTEX, so a tombstoned one
            # neither confers a match nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the same
            # enumeration (data-derived key -- the ancestor chain is not
            # knowable client-side, so it cannot be pre-declared).
            lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
            if lnk != None and not lnk.isDeleted:
                return True
            # Paginated: a parent beyond page 1 must not read as "no more
            # parents" -- the walk follows the cursor up to MAX_PARENT_PAGES
            # pages before moving on, same as actor_holds_operator's role walk.
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a location has
                # at most a few parents; containment is provisioned topology, not
                # written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    # Charged to the budget at ENQUEUE, so the node count bounds the
                    # walk's reads exactly rather than to within a page, and an
                    # ancestor reachable from several branches is visited once.
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def location_covers(candidate_key, target_key):
    # Answers "is candidate_key target_key itself, or an ancestor of it in the
    # containedIn topology?" -- the same bounded breadth-first walk as
    # worksAt_covers, testing node EQUALITY at each step instead of an actor's
    # worksAt link. This is what confines a self-order Charge to the menu
    # item's own building: candidate_key is the item's servedAt place,
    # target_key is the tab's own leaseapp_unit, and the item must be offered
    # at that unit or somewhere containing it (a building-level café serving
    # every unit inside it).
    #
    # A tombstoned link OR VERTEX is absent, tested the same three ways
    # worksAt_covers does, for the same reason: TombstoneLocation does not
    # cascade to containedIn links, so only the vertex's own isDeleted stops a
    # decommissioned location from still matching.
    #
    # Unresolvable input is a DENIAL, never an escape: candidate_key == None
    # (item minted with no servedAt, impossible today but not provable so from
    # here) and target_key == None (the tab's lease carries no appliesToUnit)
    # both fail closed rather than falling open.
    if candidate_key == None or target_key == None:
        return False
    frontier = [target_key]
    seen = [target_key]
    for _ in range(WORKPLACE_MAX_DEPTH):
        if len(frontier) == 0:
            return False
        parents = []
        for cur in frontier:
            parts = cur.split(".")
            if len(parts) != 3:
                continue
            # read-posture: (e) per-candidate follow-up read off the
            # containedIn enumeration below -- the location VERTEX, so a
            # tombstoned one neither matches nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            if cur == candidate_key:
                return True
            # Paginated: a parent beyond page 1 must not read as "no more
            # parents" -- the walk follows the cursor up to MAX_PARENT_PAGES
            # pages before moving on, same as worksAt_covers' walk above.
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a location
                # has at most a few parents; containment is provisioned topology,
                # not written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE a domain resolver
    # runs. Starlark evaluates arguments eagerly, so
    # require_workplace(resolve(x), ...) would walk the target's topology even
    # for root -- wasted reads, and worse, a malformed key anywhere in that walk
    # raises where the op previously succeeded. Call sites therefore gate on
    # this; require_workplace re-checks it anyway, so a site that forgets the
    # gate is still CORRECT, only slower.
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants, which
    # authorize via scope=any and so carry no target the platform has checked.
    # A scope=self caller is bound instead by its own op's ownership probe (the
    # applicationFor / identifiedBy indirection): a resident legitimately holds
    # no worksAt link, and confining them by a rule written for staff would deny
    # every self-service write. The two guards are complementary, not
    # alternatives -- each binds the path the other cannot see.
    #
    # The exemption keys on authTargetValidated, NOT on authContextTarget being
    # non-empty: the raw target is a client-supplied hint that any scope=any
    # holder can set, so exempting on its presence would let any staff member
    # opt out of confinement.
    if op.authTargetValidated:
        return
    enforce_workplace(location_keys, what)

def enforce_workplace(location_keys, what):
    # require_workplace minus the validated-target exemption, for a
    # resource-scoped op that has already checked for itself that the validated
    # target names the resource being acted on. Past that check the caller is an
    # ordinary staff member and must clear the worksAt walk like any other.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def vertex_live(key):
    # Is this vertex present AND not tombstoned? The standalone form of the
    # vertex test worksAt_covers performs inline at every node of its bounded
    # walk, for the resolvers that walk THROUGH a vertex to produce that walk's
    # input -- a provider, a studio, a lease. Those hops are invisible to
    # worksAt_covers: by the time it runs the dead vertex has already been
    # transited and only its live locations remain, so the confinement it
    # computes is the dead entity's ex-topology.
    #
    # A tombstone is a DOCUMENT, not an absence. kv.Read returns it rather than
    # None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), so the
    # '== None' test alone reads a tombstoned vertex as live. Both halves are
    # required, and a None key answers False so a caller that resolved nothing
    # takes the same denying branch as one that resolved something dead.
    #
    # Distinct from vertex_alive(state, key), which answers the same question
    # from the operation's DECLARED contextHint.reads. The keys here are
    # data-derived -- resolved from a link mid-walk, so unknowable client-side
    # and undeclarable -- and only a live read can see them.
    #
    if key == None:
        return False
    # read-posture: (e) one bounded read per candidate. At the sites this exists
    # for, the key is data-derived -- resolved from a kv.Links enumeration
    # mid-walk, so unknowable client-side and undeclarable. A resolver cannot
    # see which caller it has, and some callers reach it with a payload key a
    # declared read has already proved live; there this is a redundant re-proof,
    # not a second class of access. Screening at the resolver rather than per
    # call site is what keeps the rule uniform.
    node = kv.Read(key)
    return node != None and not node.isDeleted

def leaseapp_unit(lease_key):
    # A tab's location is its lease's unit -- lease-signing's appliesToUnit link,
    # the same indirection landlordLeaseApplicationsRead anchors its building on.
    # The leaseapp VERTEX this walk transits. WithdrawLeaseApplication
    # soft-deletes it without cascading to its links, so a withdrawn
    # application must not carry the walk any further. A broken chain already
    # answered None here, so this adds an input to that branch, not a new
    # answer a caller can distinguish.
    if not vertex_live(lease_key):
        return None
    # read-posture: (e) relation=appliesToUnit epoch=none -- a leaseapp carries
    # exactly one appliesToUnit link (required at CreateLeaseApplication), so
    # this is never a keyspace scan.
    page, _ = kv.Links(lease_key, "appliesToUnit", "out")
    unit = None
    for lk in page:
        if not lk.isDeleted:
            unit = lk.targetVertex
    # The unit VERTEX. require_workplace's own walk re-reads it, so this is
    # belt-and-braces here -- but the resolvers are what the next author copies,
    # and lease-signing's copy feeds require_manages, which does not.
    if not vertex_live(unit):
        return None
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

def menu_item_served_at(menu_item_key):
    # A menu item's home location -- CreateMenuItem's servedAt link
    # (menuItemVertexTypeDDL). This is the second topology chain a self-order
    # Charge walks, alongside leaseapp_unit's appliesToUnit: the write is
    # confined to a tab whose own building the item is actually served at.
    #
    # read-posture: (e) relation=servedAt epoch=none -- a menu item carries
    # exactly one servedAt link (required at CreateMenuItem, never rewired).
    page, _ = kv.Links(menu_item_key, "servedAt", "out", None, 1)
    for lk in page:
        if not lk.isDeleted:
            return lk.targetVertex
    return None

def require_menu_item_price(state, p):
    # A self-service Charge binds against the menuItem catalog rather than
    # trusting a caller-supplied amountCents (mirrors service-domain's
    # RequestService "derive, don't trust" posture: the family there comes
    # from the template's own envelope class, never a payload field — here
    # the amount comes from the item's own .price aspect). Every live
    # menuItem carries a .price aspect (CreateMenuItem writes it atomically
    # with the vertex), so an undeclared read here is a caller error, not a
    # legitimately-missing aspect.
    #
    # The locality bound lives in the Charge branch below (location_covers
    # against menu_item_served_at), not here — this function only derives the
    # amount (+ the item's own name, for itemsMemo); it does not decide
    # whether the item may be charged at all.
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
    return price.data.get("priceCents"), price.data.get("name")

def append_items_memo(existing_memo, line):
    # The running itemsMemo accumulator Charge/VoidCharge both append to —
    # comma-joined, empty-string-safe (a fresh tab's itemsMemo is "").
    if existing_memo == None or existing_memo == "":
        return line
    return existing_memo + ", " + line

def void_line_by_id(lines, line_id):
    # Marks the .status.lines entry matching line_id voided:true in place
    # (rather than removing it, so a voided line still renders on the
    # receipt struck through) and returns (new_lines, its own amountCents) —
    # the "derive, don't trust" posture require_menu_item_price already uses
    # for Charge, applied to VoidCharge: the caller names WHICH line, never
    # HOW MUCH. Returns (lines, None) unchanged if no live (non-voided) line
    # with that id exists, the caller's signal to reject UnknownChargeLine.
    found_amount = None
    new_lines = []
    for line in lines:
        if found_amount == None and line.get("id") == line_id and not line.get("voided", False):
            found_amount = line.get("amountCents")
            new_lines.append({"id": line.get("id"), "description": line.get("description"),
                               "amountCents": line.get("amountCents"), "voided": True,
                               "orderedBy": line.get("orderedBy")})
        else:
            new_lines.append(line)
    return new_lines, found_amount

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
        # workplace-exempt: (ownership-bound) the applicationFor probe below
        # requires the target to be this lease's own applicant.
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
        # authcontext-target: (ownership) the target must be the lease's own
        # applicant (applicationFor), so a forged one only fails closed.
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
        # Precomputed at write time, not derived at read time — the cypher
        # engine has no date-arithmetic builtin (clinic-reminders/
        # visitseries.go's own finding), so this is the tab's own version of
        # every other deadline column in this codebase (remindAt,
        # followUpDate). cafeStaleTabSettlement (lenses.go) arms a one-shot
        # @at at this instant and auto-dispatches SettleStaleTab once it
        # passes with the tab still open.
        stale_at = time.rfc3339_add(opened_at, "24h")

        # Two links, because a tab holds two facts about its lease that expire
        # at different times. Both put the later-arriving tab in the source
        # position (Contract #1 §1.1) and read as sentences.
        #
        #   chargedTo — PERMANENT. Where this tab's money lands. The anchor
        #     cafeTabSettlement (lenses.go) walks to reach the lease's
        #     cafeLedgerAccount guard, so it must outlive the tab's closing:
        #     settlement converges precisely AFTER Settle runs. Mirrors the
        #     account heldFor lease anchor the ledgers mint.
        #   openFor — TRANSIENT. That this tab is open, released by Settle
        #     below. It is the only hop edge-manifest's edgeEntityTabs walk
        #     traverses, so retracting it is what keeps a resident's read
        #     grant bounded by their open tabs rather than by every tab the
        #     lease has ever held (a walk chain is node patterns only and
        #     cannot see the .status aspect the presentation tail filters on).
        charged_to_lnk = "lnk.tab." + tab_id + ".chargedTo.leaseapp." + lease_id
        open_for_lnk = "lnk.tab." + tab_id + ".openFor.leaseapp." + lease_id

        if guard_revision == None:
            guard_mut = make_aspect(lease_key, "cafeOpenTab", "cafeOpenTabGuard", {"tabKey": tab_key})
        else:
            guard_mut = make_aspect_upsert_occ(lease_key, "cafeOpenTab", "cafeOpenTabGuard",
                                                {"tabKey": tab_key}, guard_revision)

        mutations = [
            make_vtx(tab_key, "tab", {}),
            make_aspect(tab_key, "status", "tabStatus",
                        {"value": "open", "totalCents": 0, "itemsMemo": "", "lines": [], "openedAt": opened_at, "staleAt": stale_at, "leaseAppKey": lease_key}),
            make_link(charged_to_lnk, tab_key, lease_key, "chargedTo", "chargedTo", {}),
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

        # A branch SELECTOR, not a confinement exemption -- so it reads the raw
        # target ("did the caller declare a self target at all") rather than
        # authTargetValidated. Keying it on presence is safe: a caller who sets
        # a target is pushed onto the STRICTER branch (catalog price, plus the
        # ownership proof below), and workplace confinement still binds them
        # independently.
        # authcontext-target: (selector) picks the amount SOURCE (catalog vs
        # caller), and the stricter branch is the one presence selects.
        is_self = op.authContextTarget != ""
        # A staff Charge accepts EITHER a menuItemKey (catalog-priced, same
        # binding as self-order) or a hand-keyed amountCents (an off-menu
        # charge the catalog does not cover) — the catalog is a curated
        # subset of what a café tab may carry, not every possible charge, so
        # the free-amount path stays available to staff.
        menu_item_key = optional_string(p, "menuItemKey")
        if is_self:
            # Self-order: the menuItem catalog bounds the amount — a
            # self-submitted amountCents is never read, let alone trusted.
            amount_cents, item_name = require_menu_item_price(state, p)
        elif menu_item_key != None:
            amount_cents, item_name = require_menu_item_price(state, p)
        else:
            amount_cents = require_number(p, "amountCents")
            if amount_cents <= 0:
                fail("InvalidArgument: amountCents: required positive number")
            item_name = optional_string(p, "description")
            if item_name == None or item_name == "":
                item_name = "Off-menu charge"

        existing = require_open_status(state, tab_key)

        # Staff-standing confinement: the lease comes from the tab's OWN .status
        # aspect (never the payload), so the workplace it resolves to cannot be
        # forged. Earliest point the location is derivable.
        # workplace-exempt: (ownership-bound) the applicationFor probe below
        # requires the target to be the applicant on this tab's own lease.
        if not workplace_exempt():
            require_workplace([leaseapp_unit(existing.data.get("leaseAppKey"))],
                              "cannot charge tab " + tab_key)

        # Resident-self ownership: same closure as Settle above — the lease
        # is recovered from the tab's OWN .status aspect, never from caller-
        # supplied payload.
        # authcontext-target: (ownership) the target must be the applicant on
        # the tab's own lease, so a forged one only fails closed.
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

        # Locality bound: a catalog item -- self-ordered or staff-picked
        # alike -- may only be charged against a tab whose own building it is
        # served at. servedAt bounds what a browse walk OFFERS, but nothing
        # bounded what Charge would ACCEPT until this check. Both locations
        # are resolved from topology the caller cannot forge (the item's own
        # servedAt link, the tab's own lease's appliesToUnit), never from a
        # payload field. A hand-keyed amountCents (no menuItemKey) names no
        # item, so it carries nothing to bind.
        if menu_item_key != None:
            item_location = menu_item_served_at(menu_item_key)
            tab_location = leaseapp_unit(existing.data.get("leaseAppKey"))
            if not location_covers(item_location, tab_location):
                fail("AuthDenied: menuItemKey " + menu_item_key +
                     " is not served at tab " + tab_key + "'s building")

        new_total = existing.data.get("totalCents") + amount_cents
        new_memo = append_items_memo(existing.data.get("itemsMemo", ""), item_name)
        existing_lines = existing.data.get("lines", [])
        new_line_id = "line-" + str(len(existing_lines) + 1)
        new_lines = existing_lines + [{"id": new_line_id, "description": item_name,
                                        "amountCents": amount_cents, "voided": False,
                                        "orderedBy": op.actor}]
        status_data = {"value": "open", "totalCents": new_total, "itemsMemo": new_memo, "lines": new_lines,
                        "openedAt": existing.data.get("openedAt"),
                        "staleAt": existing.data.get("staleAt"),
                        "leaseAppKey": existing.data.get("leaseAppKey")}
        mutations = [make_aspect_upsert_occ(tab_key, "status", "tabStatus", status_data, existing.revision)]
        events = [{"class": "tab.charged", "data": {"tabKey": tab_key, "amountCents": amount_cents, "totalCents": new_total}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tab_key}}

    if ot == "VoidCharge":
        # Operator/frontOfHouse-only correction (permissions.go grants no
        # scope=self): a POS void is a staff decision, even when reversing a
        # resident's own self-order mis-tap -- letting a resident void their
        # own charge would let them un-charge an item after it was ordered,
        # unlike Charge/Settle where self-scope is safe because the resident
        # only ever acts on their own tab in the forward direction.
        tab_key = required_string(p, "tabKey")
        parts_of(tab_key, "tabKey", "tab")
        if not vertex_alive(state, tab_key):
            fail("UnknownTab: " + tab_key)
        if class_of(state, tab_key) != "tab":
            fail("WrongClass: tabKey: " + tab_key)

        # Two forms: a lineId-targeted void derives its own amount from the
        # named .status.lines entry (never trusting a caller-supplied
        # amountCents, mirroring Charge's own "derive, don't trust" posture
        # for a catalog menuItemKey); the legacy amountCents-only form (no
        # lineId — e.g. correcting a tab whose charge predates itemized
        # lines) is unchanged and touches no lines entry.
        line_id = optional_string(p, "lineId")
        existing = require_open_status(state, tab_key)
        existing_lines = existing.data.get("lines", [])
        if line_id != None and line_id != "":
            new_lines, line_amount = void_line_by_id(existing_lines, line_id)
            if line_amount == None:
                fail("UnknownChargeLine: " + line_id)
            amount_cents = line_amount
        else:
            amount_cents = require_number(p, "amountCents")
            if amount_cents <= 0:
                fail("InvalidArgument: amountCents: required positive number")
            new_lines = existing_lines

        # Staff-standing confinement: the lease comes from the tab's OWN
        # .status aspect (never the payload), same derivation as Charge/Settle.
        # workplace-exempt: (no-validated-path) VoidCharge is granted scope=any
        # to operator + frontOfHouse only (permissions.go) and no task mints it,
        # so nothing but the operator escape reaches the exemption.
        if not workplace_exempt():
            require_workplace([leaseapp_unit(existing.data.get("leaseAppKey"))],
                              "cannot void a charge on tab " + tab_key)

        # Clamped, not rejected: an over-void (voiding more than the tab's
        # current running total) is a caller mistake worth correcting
        # cleanly to 0, not a hard failure that leaves the wrong total
        # standing.
        old_total = existing.data.get("totalCents")
        new_total = old_total - amount_cents
        if new_total < 0:
            new_total = 0
        # Only a void that actually reduced the total gets an itemsMemo line —
        # an over-void against an already-0 tab corrected nothing, so there is
        # no correction to name.
        new_memo = existing.data.get("itemsMemo", "")
        if new_total < old_total:
            new_memo = append_items_memo(new_memo, "Void correction")
        status_data = {"value": "open", "totalCents": new_total, "itemsMemo": new_memo, "lines": new_lines,
                        "openedAt": existing.data.get("openedAt"),
                        "staleAt": existing.data.get("staleAt"),
                        "leaseAppKey": existing.data.get("leaseAppKey")}
        mutations = [make_aspect_upsert_occ(tab_key, "status", "tabStatus", status_data, existing.revision)]
        events = [{"class": "tab.chargeVoided", "data": {"tabKey": tab_key, "amountCents": amount_cents, "totalCents": new_total}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tab_key}}

    if ot == "Settle":
        tab_key = required_string(p, "tabKey")
        _, tab_id = parts_of(tab_key, "tabKey", "tab")
        if not vertex_alive(state, tab_key):
            fail("UnknownTab: " + tab_key)
        if class_of(state, tab_key) != "tab":
            fail("WrongClass: tabKey: " + tab_key)

        existing = require_open_status(state, tab_key)
        settled_at = time.rfc3339_utc(op.submittedAt)
        total_cents = existing.data.get("totalCents")
        lease_key = existing.data.get("leaseAppKey")
        _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")

        # Staff-standing confinement: same derivation as Charge — the lease comes
        # from the tab's own .status aspect, so the workplace cannot be forged.
        # workplace-exempt: (ownership-bound) the applicationFor probe below
        # requires the target to be the applicant on this tab's own lease.
        if not workplace_exempt():
            require_workplace([leaseapp_unit(lease_key)], "cannot settle tab " + tab_key)

        # Resident-self (consumer's scope=self grant only): same closure as
        # OpenTab above, but the lease is recovered from the tab's OWN
        # .status aspect (already declared/read for require_open_status),
        # never from caller-supplied payload — a caller declaring the wrong
        # leaseAppKey simply won't have the right composite key pre-hydrated,
        # so the read below returns None and this fails closed regardless.
        # authcontext-target: (ownership) the target must be the applicant on
        # the tab's own lease, so a forged one only fails closed.
        if op.authContextTarget != "":
            _, target_identity_id = parts_of(op.authContextTarget, "authContextTarget", "identity")
            application_for_lnk = "lnk.leaseapp." + lease_id + ".applicationFor.identity." + target_identity_id
            # read-posture: (d) declared in contextHint.optionalReads by the
            # self-service caller (it knows its own tabKey + leaseAppKey +
            # authContext.target before submitting).
            application_for = kv.Read(application_for_lnk)
            if application_for == None or application_for.isDeleted:
                fail("AuthDenied: a resident may only settle their own tab")

        status_data = {"value": "settled", "totalCents": total_cents, "itemsMemo": existing.data.get("itemsMemo", ""),
                        "lines": existing.data.get("lines", []),
                        "openedAt": existing.data.get("openedAt"),
                        "leaseAppKey": lease_key, "settledAt": settled_at}
        # Two releases, both unconditioned, mirroring clinic-domain's slot-cell
        # release (a stale-tombstone race can only free them early; OpenTab's
        # own OCC-revive is what actually serializes a genuine race on the next
        # claim). The tab's chargedTo link is deliberately NOT among them —
        # cafeTabSettlement anchors on it and converges after this runs.
        #
        #   .cafeOpenTab — the lease's open-tab guard, so its next OpenTab can
        #     claim it again.
        #   openFor — the "this tab is open" hop. Retracting it narrows every
        #     resident's edgeEntityTabs read grant to their currently-open tabs;
        #     the lens tail's status filter then re-states the same bound
        #     rather than being the only one enforcing it.
        mutations = [
            make_aspect_upsert_occ(tab_key, "status", "tabStatus", status_data, existing.revision),
            make_tombstone(lease_key + ".cafeOpenTab"),
            make_tombstone("lnk.tab." + tab_id + ".openFor.leaseapp." + lease_id),
        ]

        # Settle GUARANTEES the tab carries a chargedTo link before it closes —
        # a class-(d) read-before-create dedup (Contract #2 §2.5) declared in
        # ContextHint.OptionalReads, same idiom as the .cafeOpenTab guard
        # above. OpenTab always writes chargedTo today, so this is a no-op for
        # every normally-opened tab; it only fires for a tab whose chargedTo
        # link is absent for any reason (a schema gap the write path once had,
        # or any other drift), which cafeTabSettlement's required MATCH on
        # chargedTo would otherwise strand invisibly and unsettleably forever
        # (lenses.go — no lens row means no tabKey any surface can act on).
        # Backfilling here, rather than at read time, keeps the money-gap
        # anchor a real, permanent write, not a read-side workaround.
        charged_to_lnk = "lnk.tab." + tab_id + ".chargedTo.leaseapp." + lease_id
        if not vertex_alive(state, charged_to_lnk):
            mutations.append(make_link(charged_to_lnk, tab_key, lease_key, "chargedTo", "chargedTo", {}))
        events = [{"class": "tab.settled", "data": {"tabKey": tab_key, "leaseAppKey": lease_key, "totalCents": total_cents}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tab_key}}

    if ot == "SettleStaleTab":
        # Orchestration-internal auto-settle: cafeStaleTabSettlement (lenses.go)
        # dispatches this once an OPEN tab's own staleAt deadline passes with no
        # staff Settle. A DEDICATED operationType, not a directOp against Settle
        # itself, for the same mechanical reason clinic-domain's
        # MarkPastDueNoShow exists: Settle's chargedTo-backfill branch reads a
        # LINK key (a composite of the tab's own id + its lease's id) via
        # ContextHint.OptionalReads, and a Weaver GapActionSpec's Reads can only
        # template a single row.<column>[.<literalSuffix>] — it cannot express a
        # link key. A Weaver-dispatched Settle would therefore see chargedTo as
        # unconditionally absent (never in the undeclared read's snapshot) and
        # try to recreate an already-live link on every normally-opened tab,
        # failing every single dispatch. This op confirms chargedTo via a
        # bounded kv.Links read instead (read-posture (e), the
        # menu_item_served_at idiom, above), never a declared one.
        tab_key = required_string(p, "tabKey")
        _, tab_id = parts_of(tab_key, "tabKey", "tab")
        if not vertex_alive(state, tab_key):
            fail("UnknownTab: " + tab_key)
        if class_of(state, tab_key) != "tab":
            fail("WrongClass: tabKey: " + tab_key)

        status_key = tab_key + ".status"
        if status_key not in state:
            fail("InvalidArgument: tabKey: caller must declare " + status_key + " in contextHint.reads")
        existing = state[status_key]
        if existing == None or (hasattr(existing, "isDeleted") and existing.isDeleted):
            fail("UnknownTab: " + tab_key + ": no .status aspect")
        if existing.data.get("value") != "open":
            # A staff Settle raced this dispatch and won — no-op cleanly
            # rather than reject (MarkPastDueNoShow's own defensive re-check
            # against this exact race, clinic-domain).
            return {"mutations": [], "events": [], "response": {}}

        settled_at = time.rfc3339_utc(op.submittedAt)
        total_cents = existing.data.get("totalCents")
        lease_key = existing.data.get("leaseAppKey")
        _, lease_id = parts_of(lease_key, "leaseAppKey", "leaseapp")

        status_data = {"value": "settled", "totalCents": total_cents, "itemsMemo": existing.data.get("itemsMemo", ""),
                        "lines": existing.data.get("lines", []),
                        "openedAt": existing.data.get("openedAt"),
                        "leaseAppKey": lease_key, "settledAt": settled_at}
        mutations = [
            make_aspect_upsert_occ(tab_key, "status", "tabStatus", status_data, existing.revision),
            make_tombstone(lease_key + ".cafeOpenTab"),
            make_tombstone("lnk.tab." + tab_id + ".openFor.leaseapp." + lease_id),
        ]

        # A normally-opened tab always carries chargedTo (OpenTab writes it
        # unconditionally), so this only ever fires for a schema-gap tab that
        # predates that guarantee — the same rare case Settle's own backfill
        # exists for.
        charged_to_lnk = "lnk.tab." + tab_id + ".chargedTo.leaseapp." + lease_id
        has_charged_to = False
        # read-posture: (e) relation=chargedTo epoch=none -- mirrors
        # menu_item_served_at above; a tab carries at most one chargedTo link.
        page, _ = kv.Links(tab_key, "chargedTo", "out", None, 1)
        for lk in page:
            if not lk.isDeleted:
                has_charged_to = True
        if not has_charged_to:
            mutations.append(make_link(charged_to_lnk, tab_key, lease_key, "chargedTo", "chargedTo", {}))

        events = [{"class": "tab.settled", "data": {"tabKey": tab_key, "leaseAppKey": lease_key, "totalCents": total_cents, "reason": "stale"}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tab_key}}

    if ot == "BackfillTabStaleAt":
        # Orchestration-internal: cafeStaleTabSettlement's missing_staleat gap
        # (lenses.go) dispatches this for an OPEN tab whose .status carries no
        # staleAt at all — every tab opened before the staleAt feature shipped
        # (af451062), invisible to missing_settle until this backfills the
        # SAME value OpenTab would have written. Mirrors Settle's own
        # chargedTo backfill (above): fix the missing field at the write
        # path, never a read-side workaround.
        tab_key = required_string(p, "tabKey")
        if not vertex_alive(state, tab_key):
            fail("UnknownTab: " + tab_key)
        if class_of(state, tab_key) != "tab":
            fail("WrongClass: tabKey: " + tab_key)

        status_key = tab_key + ".status"
        if status_key not in state:
            fail("InvalidArgument: tabKey: caller must declare " + status_key + " in contextHint.reads")
        existing = state[status_key]
        if existing == None or (hasattr(existing, "isDeleted") and existing.isDeleted):
            fail("UnknownTab: " + tab_key + ": no .status aspect")
        if existing.data.get("value") != "open" or existing.data.get("staleAt") != None:
            # Already settled by staff, or staleAt already present (a race
            # with another dispatch, or a legitimate redelivery) — no-op
            # cleanly rather than reject, the SettleStaleTab idiom above.
            return {"mutations": [], "events": [], "response": {}}

        opened_at = existing.data.get("openedAt")
        stale_at = time.rfc3339_add(opened_at, "24h")
        status_data = {"value": "open", "totalCents": existing.data.get("totalCents"),
                        "itemsMemo": existing.data.get("itemsMemo", ""),
                        "lines": existing.data.get("lines", []),
                        "openedAt": opened_at, "staleAt": stale_at,
                        "leaseAppKey": existing.data.get("leaseAppKey")}
        mutations = [make_aspect_upsert_occ(tab_key, "status", "tabStatus", status_data, existing.revision)]
        events = [{"class": "tab.staleAtBackfilled", "data": {"tabKey": tab_key, "staleAt": stale_at}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": tab_key}}

    fail("tab DDL: unknown operationType: " + ot)
`

// menuItemDDLScript handles CreateMenuItem, RetireMenuItem. Known-key reads
// only: RetireMenuItem declares menuItemKey in ContextHint.Reads so the
// current revision is hydrated for the self-OCC'd tombstone (mirrors
// service-domain's RetireServiceTemplate). Both ops carry no scope=self grant
// (permissions.go) — a staff caller that cannot prove root is workplace-
// confined the same way VoidCharge is in tabDDLScript: the confinement
// helpers below are that script's require_workplace idiom, mirrored here
// rather than shared, since each op's Script is its own standalone Starlark
// unit. CreateMenuItem confines against its own payload locationKey (already
// a declared read); RetireMenuItem has no payload location, so it resolves
// the item's own servedAt link instead (menu_item_served_at, the same
// data-derived kv.Links follow-up tabDDLScript's self-service Charge branch
// already uses for the identical resolution).
const menuItemDDLScript = `
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

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

# The concrete location levels a servedAt link's target may carry.
# location-domain owns the vertices; this scheme references them by KEY TYPE,
# the way service-location's wiring ops do — a location vertex's class IS its
# own key type, so no single class value names the family.
LOCATION_TYPES = ["unit", "building", "property"]

# The class a location vertex minted before the taxonomy landed carries: one
# shared discriminator across all three levels. Nothing rewrites those
# documents, so both class shapes are live at once and this guard admits
# either.
LEGACY_LOCATION_CLASS = "location"

# The full set of classes a live location vertex may carry: its own key type
# (the class every newly-minted location gets) or the shared legacy
# discriminator.
LOCATION_CLASSES = LOCATION_TYPES + [LEGACY_LOCATION_CLASS]

def key_type_of(key):
    # The type segment of a 3-segment vtx.<type>.<NanoID> key, or None for any
    # other shape (an aspect key, a link key, a malformed string).
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        return None
    return parts[1]

def require_live_location(state, key, name):
    # Alive, keyed vtx.<locationType>.<NanoID> at an admitted location level,
    # AND carrying a location class.
    # BOTH the key and the class are checked, and each catches what the other
    # cannot. The KEY's type segment is the type authority — it is what a lens
    # label resolves against, and it is the only thing that can say "any
    # location" across the three levels, since a location's class is its own
    # key type. The CLASS is what proves location-domain minted the vertex: a
    # foreign package writing vtx.unit.<id> with a class of its own passes the
    # key check and must still be refused.
    if not vertex_alive(state, key):
        fail("UnknownLocation: " + name + ": " + key + " is absent or tombstoned")
    lt = key_type_of(key)
    if lt not in LOCATION_TYPES:
        fail("NotALocation: " + name + ": " + key + " has type segment " + str(lt) + ", required one of unit, building, property")
    cls = class_of(state, key)
    if cls not in LOCATION_CLASSES:
        fail("NotALocation: " + name + ": " + key + " has class " + str(cls) + ", required its own location type or " + LEGACY_LOCATION_CLASS)

# --- workplace write confinement (facet-staff-worlds-design.md §3.5) ---------
# Mirrors tabDDLScript's identically-named helpers verbatim — see that
# script's comment block for the full correctness argument (role-derived
# exemption, tombstone-as-document, target-topology-only resolution). Each
# op's Script is its own standalone Starlark unit, so the helpers are
# duplicated here rather than shared.
ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4
WORKPLACE_PARENT_PAGE_LIMIT = 20
MAX_PARENT_PAGES = 4
WORKPLACE_MAX_DEPTH = 8
WORKPLACE_MAX_NODES = 64

def actor_holds_operator(actor_key):
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def worksAt_covers(actor_id, location_key):
    if location_key == None:
        return False
    frontier = [location_key]
    seen = [location_key]
    for _ in range(WORKPLACE_MAX_DEPTH):
        if len(frontier) == 0:
            return False
        parents = []
        for cur in frontier:
            parts = cur.split(".")
            if len(parts) != 3:
                continue
            # read-posture: (e) per-candidate follow-up read off the containedIn
            # enumeration below -- the location VERTEX, so a tombstoned one
            # neither confers a match nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the same
            # enumeration (data-derived key -- the ancestor chain is not
            # knowable client-side, so it cannot be pre-declared).
            lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
            if lnk != None and not lnk.isDeleted:
                return True
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a location has
                # at most a few parents; containment is provisioned topology, not
                # written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def workplace_exempt():
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    if op.authTargetValidated:
        return
    enforce_workplace(location_keys, what)

def enforce_workplace(location_keys, what):
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def menu_item_served_at(menu_item_key):
    # A menu item's home location -- CreateMenuItem's servedAt link. Mirrors
    # tabDDLScript's identically-named resolver, used there by the
    # self-service Charge branch for the same lookup.
    #
    # read-posture: (e) relation=servedAt epoch=none -- a menu item carries
    # exactly one servedAt link (required at CreateMenuItem, never rewired).
    page, _ = kv.Links(menu_item_key, "servedAt", "out", None, 1)
    for lk in page:
        if not lk.isDeleted:
            return lk.targetVertex
    return None

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateMenuItem":
        name = required_string(p, "name")
        price_cents = require_number(p, "priceCents")
        if price_cents <= 0:
            fail("InvalidArgument: priceCents: required positive number")

        # The place that serves the item. Declared-read (Contract #2 §2.5): the
        # submitter lists locationKey in contextHint.reads, so an absent entry
        # reads as a tombstoned vertex here and fails closed rather than minting
        # an item linked to nothing.
        location_key = required_string(p, "locationKey")
        loc_type, loc_id = parts_of(location_key, "locationKey", "")
        require_live_location(state, location_key, "locationKey")

        # Staff-standing confinement: a non-operator staff actor may only add
        # a catalog item at a location it worksAt.
        # workplace-exempt: (no-validated-path) CreateMenuItem is granted
        # scope=any to operator + frontOfHouse only (permissions.go) and no
        # task mints it, so op.authTargetValidated is never legitimately true
        # and only the operator escape reaches the exemption.
        if not workplace_exempt():
            require_workplace([location_key], "cannot create a menu item at " + location_key)

        item_id = bare_nanoid_or_mint(p, "menuItemId")
        item_key = "vtx.menuitem." + item_id

        # servedAt: the item (later-arriving) is the source, the pre-existing
        # place is the target (Contract #1 §1.1). Reads as "menu item servedAt
        # place."
        served_at_lnk = "lnk.menuitem." + item_id + ".servedAt." + loc_type + "." + loc_id

        mutations = [
            make_vtx(item_key, "menuitem", {}),
            make_aspect(item_key, "price", "menuItemPrice", {"name": name, "priceCents": price_cents}),
            make_link(served_at_lnk, item_key, location_key, "servedAt", "servedAt", {}),
        ]
        events = [{"class": "menuItem.created", "data": {"menuItemKey": item_key, "name": name, "priceCents": price_cents, "locationKey": location_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": item_key}}

    if ot == "RetireMenuItem":
        # Staff/operator cleanup, mirrors service-domain's
        # RetireServiceTemplate: a tombstone mutation carries no document (the
        # runtime writes isDeleted:true + the lastModified* stamps only),
        # self-OCC'd on the hydrated revision so a concurrent mutation of the
        # same item aborts instead of racing.
        item_key = required_string(p, "menuItemKey")
        parts_of(item_key, "menuItemKey", "menuitem")
        if not vertex_alive(state, item_key):
            fail("UnknownMenuItem: " + item_key)

        # Staff-standing confinement: the location comes from the item's OWN
        # servedAt link (never the payload, which carries none), so the
        # workplace cannot be forged.
        # workplace-exempt: (no-validated-path) RetireMenuItem is granted
        # scope=any to operator + frontOfHouse only (permissions.go) and no
        # task mints it, so op.authTargetValidated is never legitimately true
        # and only the operator escape reaches the exemption.
        if not workplace_exempt():
            require_workplace([menu_item_served_at(item_key)], "cannot retire menu item " + item_key)

        mutations = [
            {"op": "tombstone", "key": item_key,
             "expectedRevision": state[item_key].revision},
        ]
        events = [{"class": "menuItem.retired", "data": {"menuItemKey": item_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": item_key}}

    fail("menuItem DDL: unknown operationType: " + ot)
`
