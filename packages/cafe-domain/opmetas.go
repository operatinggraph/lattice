package cafedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3, edge-manifest Fire 1) for every cafe-domain op a person may trigger —
// the tab lifecycle (OpenTab, Charge, VoidCharge, Settle) plus the menu
// catalog (CreateMenuItem, RetireMenuItem, SetMenuItemAvailability,
// SetMenuItemLocation, UpdateMenuItem) — mirroring clinic-domain's and
// wellness-domain's adoption. The five catalog ops are staff-standing like
// VoidCharge (no self-scope grant), so each carries one AuthContext
// "standing" descriptor and no ownership probe — workplace confinement
// (ddls.go) is their only guard.
//
// Three of the four are consumer-invocable (scope=self); VoidCharge is
// staff-standing. Charge is BOTH: permissions.go grants it scope=any to
// operator + frontOfHouse and scope=self to consumer, and it carries ONE
// descriptor written in the SELF voice, per clinic-domain's dual-grant idiom
// (opmetas.go — its three staff-and-consumer ops each declare a single
// AuthContext "self" meta). A staff FE hardcodes its own dispatch; a
// descriptor-driven client cannot infer the self path, so the self path is what
// the descriptor must name. Charge's self slice therefore names menuItemKey and
// NOT amountCents: on the self branch the amount comes from the item's own
// .price aspect and a caller-supplied amountCents is never read (ddls.go
// require_menu_item_price), so describing it as an input would be a lie.
//
// menuItemKey carries `x-entityRef: "menuitem"`. The field holds a Contract #1
// vertex key but is NOT dispatch.targetField (tabKey is), so it does not arrive
// through the client's target resolution — a descriptor client would otherwise
// render a raw key input nobody can type. The annotation names the vertex TYPE,
// and the client picks from the reachability-bounded rows it already holds for
// that type (edge-manifest's edgeEntityMenuItems). Naming the type rather than
// a lens or an endpoint keeps the vocabulary declarative: the op says what kind
// of thing the field holds, and stays silent on how any given client finds one.
//
// leaseAppKey (OpenTab) is declared `{me.leaseapp}` in
// dispatch.contextParams — the submitter's own lease, which is the only
// lease the self-scope grant would accept anyway. dispatch.targetField
// cannot express it (a tab is a fresh vertex the op mints, so there is no
// "tab being viewed" to derive it from), but the value was never the
// visitor's to choose: the client resolves it from the me-row's declared
// selfAnchors and renders no field for it. tabKey (Settle) auto-fills the
// ordinary way, from the tab OpenTab's own response returned (the client's
// own local record of what it just opened).
//
// Dispatch.Class on each entry is "tab" — the tab DDL's own CanonicalName
// (tabVertexTypeDDL), the Contract #2 §2.1 envelope `class` DDL-hint (never
// the vertical name "cafe" — see clinic-domain's opmetas.go doc comment for
// the regression that mistake caused).
//
// OpenTab's Dispatch.Reads ({payload.leaseAppKey}) is this package's first
// real use of the Reads template vocabulary (OpDispatchSpec.Reads;
// definition.go — mirrors wellness-domain's opmetas.go doc comment on being
// the first real use of ContextParams): a client-driven descriptor-form
// submission must declare the lease vertex itself in ContextHint.Reads
// (required, not optional) for tabDDLScript's `state[lease_key]` liveness
// check (ddls.go) — discovered live during the Facet second-renderer spike
// (edge-showcase-app-design.md §7.11) when a hand-built envelope that
// declared only the applicationFor link in OptionalReads came back
// UnknownLeaseApplication.
//
// Dispatch.OptionalReads carries the absence-tolerant half the self-scope ops
// need: the per-lease cafeOpenTabGuard dedup key (absent on a lease's first-ever
// tab, TOMBSTONED once a prior tab settled — so a required Read would fail the
// common case) and the applicationFor ownership link the self-scope check
// probes. The link key is built with the `:id` template modifier, since a
// Contract #1 link is 6 segments of bare ids rather than a vtx key.
//
// OpenTab also declares the lease's own lease-signing `.decision` aspect
// (OptionalReads, not Reads — a lease with no landlord decision yet is the
// ordinary not-yet-approved case, mirroring lease-signing's own SignLease
// probe of the same key, scripts.go). Absent or not "approved" rejects
// LeaseNotApproved: a house tab, and the charges it accrues, must never open
// against a lease the landlord hasn't signed off on (a self-order against an
// unapproved lease posted a live $4.50 charge before this guard existed).
// The same lease's `.tenancy` aspect rides alongside it (OptionalReads — a
// lease approved before tenancies were minted carries none): once its
// recorded endedAt is set (EndTenancy, whether from an early move-out or the
// term simply running out) — or, absent that, once submittedAt reaches its
// leaseEnd — the op rejects TenancyEnded, so a moved-out resident's lease
// stops taking house tabs the moment its rent clause stops billing.
//
// VoidCharge declares no ownership probe: it has no self grant at all (a POS
// correction is a staff decision even when reversing a resident's own mis-tap),
// so its only confinement is require_workplace, whose site walk is a class-(e)
// enumeration the caller cannot pre-declare.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "OpenTab",
			// refusal-courtesy(facet): CreditHold, InvalidState, LeaseNotApproved, OpenTabAlreadyExists, TenancyEnded: none — leaseAppKey is dispatch.contextParams-resolved ({me.leaseapp}); OpenTab declares no dispatch.targetType and renders no field, so Facet offers no lease picker to hide or drop any of these against
			// refusal-courtesy(facet): TabLimitExceeded: none — the house's .cafePolicy is projected by cafeHousePolicies (lenses.go), not by any edge-manifest lens Facet reads, so the generic form cannot see a closed house; the refusal toast names the desk
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Open a house tab",
				Description: "Start a café tab billed to your lease.",
				Icon:        "cafe",
				Tone:        "primary",
				SubmitLabel: "Open tab",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"leaseAppKey":{"type":"string","description":"vtx.leaseapp.<NanoID> of your own lease application."}},` +
				`"required":["leaseAppKey"]}`,
			FieldDescriptions: map[string]string{
				"leaseAppKey": "Your own lease application — must be identified-by your identity, via the lease's applicationFor link (self-scope grant requirement).",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:         "tab",
				AuthContext:   "self",
				ContextParams: map[string]string{"leaseAppKey": "{me.leaseapp}"},
				Reads:         []string{"{payload.leaseAppKey}"},
				OptionalReads: []string{
					"{payload.leaseAppKey}.cafeOpenTab",
					"lnk.leaseapp.{payload.leaseAppKey:id}.applicationFor.identity.{actor:id}",
					"{payload.leaseAppKey}.decision",
					"{payload.leaseAppKey}.tenancy",
				},
				// The operator-role confinement probe: require_workplace's
				// workplace-exempt short-circuit walks the actor's own
				// holdsRole links to test for the operator role (ddls.go
				// actor_holds_operator).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
					// The credit-hold walk: the lease's café account, reached
					// by its heldFor in-link (ddls.go cafe_account_for_lease).
					{Hub: "{payload.leaseAppKey}", Relation: "heldFor", Direction: "in"},
				},
			},
		},
		{
			OperationType: "Charge",
			// refusal-courtesy(facet): ItemUnavailable: drop — entityRefCandidates (cmd/facet/web/app.js) drops menuitem rows edgeEntityMenuItemsTail (packages/edge-manifest/lenses.go) projects available:false
			// refusal-courtesy(facet): TabNotOpen: hide — edgeEntityTabsTail (packages/edge-manifest/lenses.go) projects only tabs whose .status.value = "open", so Facet's tab browse never lists a settled tab as a Charge target
			// refusal-courtesy(facet): TabLimitExceeded: none — the house limit (cafeHousePolicies, lenses.go) and the tab's running total are not projected by the edge-manifest lenses Facet's menu picker reads, so the generic form cannot cap the pick; the refusal toast names the limit, the tab's total and the desk
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Order an item",
				Description: "Add a menu item to your open tab.",
				Icon:        "cafe",
				Tone:        "primary",
				SubmitLabel: "Add to tab",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"tabKey":{"type":"string","description":"vtx.tab.<NanoID> of your open tab — auto-filled from the tab you opened."},` +
				`"menuItemKey":{"type":"string","title":"Item","x-entityRef":"menuitem","description":"vtx.menuitem.<NanoID> of the catalog item to order; its own price is what gets charged."}},` +
				`"required":["tabKey","menuItemKey"]}`,
			FieldDescriptions: map[string]string{
				"tabKey":      "The tab being charged — auto-filled by the client from the tab it opened (dispatch.targetField), not user-entered.",
				"menuItemKey": "The catalog item you are ordering. The amount charged is the item's own listed price — a self-service order never names its own amount.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "tab",
				AuthContext: "self",
				TargetField: "tabKey",
				TargetType:  "tab",
				// .status is REQUIRED for the same reason as Settle
				// (require_open_status carries the running total forward), and
				// the menuItem's .price is required by name: the script fails
				// with "caller must declare <key> in contextHint.reads" rather
				// than reading it live, since every live menuItem has one.
				Reads: []string{
					"{payload.tabKey}", "{payload.tabKey}.status",
					"{payload.menuItemKey}", "{payload.menuItemKey}.price",
				},
				OptionalReads: []string{
					"lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}",
				},
				// The operator-role confinement probe (ddls.go
				// actor_holds_operator, reached through require_workplace).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "VoidCharge",
			// refusal-courtesy(facet): TabNotOpen: hide — edgeEntityTabsTail (packages/edge-manifest/lenses.go) projects only tabs whose .status.value = "open", so Facet's tab browse never lists a settled tab as a VoidCharge target
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Void a charge",
				Description: "Correct a mis-tapped charge by taking it back off an open tab.",
				Icon:        "receipt",
				Tone:        "destructive",
				SubmitLabel: "Void charge",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"tabKey":{"type":"string","description":"vtx.tab.<NanoID> of the open tab to correct — auto-filled from the tab being viewed."},` +
				`"lineId":{"type":"string","description":"id of one entry in the tab's own .status.lines to void; its amount is derived from that line, never from a caller-supplied amountCents."}},` +
				`"required":["tabKey","lineId"]}`,
			FieldDescriptions: map[string]string{
				"tabKey": "The tab being corrected — auto-filled by the client from the tab being viewed (dispatch.targetField), not user-entered.",
				"lineId": "The id of the specific charge line to void, from the tab's own .status.lines — its amount is derived from that line, never from a caller-supplied amountCents.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "tab",
				AuthContext: "standing",
				TargetField: "tabKey",
				TargetType:  "tab",
				Reads:       []string{"{payload.tabKey}", "{payload.tabKey}.status"},
				// The operator-role confinement probe (ddls.go
				// actor_holds_operator, reached through require_workplace).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "MarkLineServed",
			// refusal-courtesy(facet): TabNotOpen: hide — edgeEntityTabsTail (packages/edge-manifest/lenses.go) projects only tabs whose .status.value = "open", so Facet's tab browse never lists a settled tab as a MarkLineServed target
			// refusal-courtesy(facet): LineVoided, LineAlreadyServed: none — Facet's tab browse projects no per-line column, so the generic form takes a free lineId and the script's own recorded-state refusal is the answer
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Mark an order served",
				Description: "Record that a self-ordered line on an open tab was handed over.",
				Icon:        "receipt",
				Tone:        "primary",
				SubmitLabel: "Mark served",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"tabKey":{"type":"string","description":"vtx.tab.<NanoID> of the open tab the order is on — auto-filled from the tab being viewed."},` +
				`"lineId":{"type":"string","description":"id of one entry in the tab's own .status.lines to mark served; refused if it was voided or is already served."}},` +
				`"required":["tabKey","lineId"]}`,
			FieldDescriptions: map[string]string{
				"tabKey": "The tab the order is on — auto-filled by the client from the tab being viewed (dispatch.targetField), not user-entered.",
				"lineId": "The id of the specific order line handed over, from the tab's own .status.lines — servedAt and servedBy are stamped from the submission, never from the caller.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "tab",
				AuthContext: "standing",
				TargetField: "tabKey",
				TargetType:  "tab",
				Reads:       []string{"{payload.tabKey}", "{payload.tabKey}.status"},
				// The operator-role confinement probe (ddls.go
				// actor_holds_operator, reached through require_workplace).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "Settle",
			// refusal-courtesy(facet): TabNotOpen: hide — edgeEntityTabsTail (packages/edge-manifest/lenses.go) projects only tabs whose .status.value = "open", so Facet's tab browse never lists a settled tab as a Settle target
			// refusal-courtesy(facet): PaidMismatchesTab: unreachable — the self-voice InputSchema names no paidCents, and the script raises the code only when the field is present (a resident's self leg is refused AuthDenied before the equality check regardless)
			// refusal-courtesy(facet): UnservedLines: none — edgeEntityTabsTail (packages/edge-manifest/lenses.go) projects no per-line column, so the generic form cannot see an unmade order; the refusal names each line and the desk
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Close & settle tab",
				Description: "Close your tab and post the total to your account.",
				Icon:        "receipt",
				Tone:        "primary",
				SubmitLabel: "Settle",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"tabKey":{"type":"string","description":"vtx.tab.<NanoID> of the open tab to settle — auto-filled from the tab you opened."}},` +
				`"required":["tabKey"]}`,
			// The self slice names tabKey alone, the Charge precedent above:
			// Settle's staff-only paidCents (cash the desk took at the counter,
			// ddls.go) is refused AuthDenied on the self leg this descriptor
			// describes, so describing it as an input would be a lie to the
			// resident Facet renders for. The desk's own FE hardcodes it.
			FieldDescriptions: map[string]string{
				"tabKey": "The tab being closed — auto-filled by the client from the tab it opened (dispatch.targetField), not user-entered.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "tab",
				AuthContext: "self",
				TargetField: "tabKey",
				TargetType:  "tab",
				// The tab's own .status aspect is REQUIRED, not optional:
				// require_open_status reads it for the total/openedAt/lease it
				// carries forward, so its absence is a correctness error. The
				// targetField fallback declares the tab vertex but never its
				// aspects.
				Reads: []string{"{payload.tabKey}", "{payload.tabKey}.status"},
				// The self-scope ownership probe. Settle recovers the lease
				// from the tab's OWN .status (never caller-supplied), so this
				// declares the resident's own lease anchor — a caller naming
				// someone else's tab simply won't have the matching composite
				// key hydrated, and the script's kv.Read fails it closed.
				// (The chargedTo backfill dedup (ddls.go) is confirmed via a
				// live kv.Links read, not a declared one — the tab's real
				// lease is only known once its .status is read mid-script, so
				// a staff Settle of another resident's tab has no
				// `{me.leaseapp}` to declare it by.)
				OptionalReads: []string{
					"lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}",
				},
				Enumerations: []pkgmgr.EnumerationSpec{
					// The operator-role confinement probe (ddls.go
					// actor_holds_operator, reached through require_workplace).
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
					// The chargedTo backfill's live confirmation (ddls.go) — the
					// hub is the tab being settled, known up front from the
					// payload, so this is declared here rather than baselined.
					{Hub: "{payload.tabKey}", Relation: "chargedTo", Direction: "out"},
				},
			},
		},
		{
			OperationType: "CreateMenuItem",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Add a catalog item",
				Description: "Add an item to the self-order menu catalog.",
				Icon:        "cafe",
				Tone:        "primary",
				SubmitLabel: "Add item",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"name":{"type":"string","description":"Menu item display name."},` +
				`"priceCents":{"type":"integer","minimum":1,"description":"Price in whole cents; must be positive."},` +
				`"locationKey":{"type":"string","description":"vtx.<locationType>.<NanoID> of the place that serves this item — must be a location you worksAt (or an ancestor of it)."}},` +
				`"required":["name","priceCents","locationKey"]}`,
			FieldDescriptions: map[string]string{
				"name":        "The item's display name.",
				"priceCents":  "The item's price, entered in dollars — e.g. 4.50.",
				"locationKey": "The place that serves this item. Confined to a location you worksAt (or an ancestor of it) unless you are the operator.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "menuitem",
				AuthContext: "standing",
				Reads:       []string{"{payload.locationKey}"},
				// The operator-role confinement probe (ddls.go
				// actor_holds_operator, reached through require_workplace).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "RetireMenuItem",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Retire a catalog item",
				Description: "Remove an item from the self-order menu catalog.",
				Icon:        "cafe",
				Tone:        "destructive",
				SubmitLabel: "Retire item",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"menuItemKey":{"type":"string","description":"vtx.menuitem.<NanoID> of the item to retire — auto-filled from the item being viewed."}},` +
				`"required":["menuItemKey"]}`,
			FieldDescriptions: map[string]string{
				"menuItemKey": "The catalog item being retired — auto-filled by the client from the item being viewed (dispatch.targetField), not user-entered.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "menuitem",
				AuthContext: "standing",
				TargetField: "menuItemKey",
				TargetType:  "menuitem",
				Reads:       []string{"{payload.menuItemKey}"},
				// The operator-role confinement probe (ddls.go
				// actor_holds_operator, reached through require_workplace).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "SetMenuItemAvailability",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Take an item off the menu / put it back",
				Description: "Mark a catalog item sold out for the day, or bring it back.",
				Icon:        "cafe",
				Tone:        "primary",
				SubmitLabel: "Save availability",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"menuItemKey":{"type":"string","description":"vtx.menuitem.<NanoID> of the item to toggle — auto-filled from the item being viewed."},` +
				`"available":{"type":"boolean","description":"Whether the item can currently be ordered."}},` +
				`"required":["menuItemKey","available"]}`,
			FieldDescriptions: map[string]string{
				"menuItemKey": "The catalog item being toggled — auto-filled by the client from the item being viewed (dispatch.targetField), not user-entered.",
				"available":   "false takes the item off both pickers and refuses a Charge naming it (ItemUnavailable); true puts it back.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "menuitem",
				AuthContext: "standing",
				TargetField: "menuItemKey",
				TargetType:  "menuitem",
				Reads:       []string{"{payload.menuItemKey}", "{payload.menuItemKey}.price"},
				// The operator-role confinement probe (ddls.go
				// actor_holds_operator, reached through require_workplace).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "SetCafePolicy",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Set the house tab limit",
				Description: "Record how much a resident may self-order on one house tab at this location.",
				Icon:        "cafe",
				Tone:        "primary",
				SubmitLabel: "Save house limit",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"locationKey":{"type":"string","description":"vtx.<locationType>.<NanoID> of the house whose limit this is — must be a location you worksAt (or an ancestor of it)."},` +
				`"tabLimitCents":{"type":"integer","minimum":0,"description":"The self-service tab limit in whole cents; 0 closes self-service tabs at this house."}},` +
				`"required":["locationKey","tabLimitCents"]}`,
			FieldDescriptions: map[string]string{
				"locationKey":   "The house (building or property) the limit applies to. Confined to a location you worksAt (or an ancestor of it) unless you are the operator.",
				"tabLimitCents": "The limit, entered in dollars — e.g. 50.00. A resident's own order that would take their tab past it is refused; the desk may ring past it. 0 closes self-service tabs here.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:         "menuitem",
				AuthContext:   "standing",
				Reads:         []string{"{payload.locationKey}"},
				OptionalReads: []string{"{payload.locationKey}.cafePolicy"},
				// The operator-role confinement probe (ddls.go
				// actor_holds_operator, reached through require_workplace).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "SetMenuItemLocation",
			// refusal-courtesy(facet): InvalidState: none — the already-live servedAt link race (make_link_create_or_revive, ddls.go) is a same-item/same-location double-submit fault, not state any entity lens column projects for entityRefCandidates to filter on
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Relocate a catalog item",
				Description: "Move a catalog item to a new place — the repair for one whose place was retired.",
				Icon:        "cafe",
				Tone:        "primary",
				SubmitLabel: "Relocate item",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"menuItemKey":{"type":"string","description":"vtx.menuitem.<NanoID> of the item to relocate — auto-filled from the item being viewed."},` +
				`"newLocation":{"type":"string","description":"vtx.<locationType>.<NanoID> the item should now be served at — must be a location you worksAt (or an ancestor of it)."}},` +
				`"required":["menuItemKey","newLocation"]}`,
			FieldDescriptions: map[string]string{
				"menuItemKey": "The catalog item being relocated — auto-filled by the client from the item being viewed (dispatch.targetField), not user-entered.",
				"newLocation": "The item's new place. Confined to a location you worksAt (or an ancestor of it) unless you are the operator — the item's OLD place may already be gone, so it cannot anchor this check the way RetireMenuItem's does.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "menuitem",
				AuthContext: "standing",
				TargetField: "menuItemKey",
				TargetType:  "menuitem",
				Reads:       []string{"{payload.menuItemKey}", "{payload.newLocation}"},
				// The operator-role confinement probe (ddls.go
				// actor_holds_operator, reached through require_workplace).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
		{
			OperationType: "UpdateMenuItem",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Edit a catalog item",
				Description: "Rename or reprice a self-order menu item.",
				Icon:        "cafe",
				Tone:        "primary",
				SubmitLabel: "Save item",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"menuItemKey":{"type":"string","description":"vtx.menuitem.<NanoID> of the item to edit — auto-filled from the item being viewed."},` +
				`"name":{"type":"string","description":"Menu item display name."},` +
				`"priceCents":{"type":"integer","minimum":1,"description":"Price in whole cents; must be positive."}},` +
				`"required":["menuItemKey","name","priceCents"]}`,
			FieldDescriptions: map[string]string{
				"menuItemKey": "The catalog item being edited — auto-filled by the client from the item being viewed (dispatch.targetField), not user-entered.",
				"name":        "The item's display name.",
				"priceCents":  "The item's price, entered in dollars — e.g. 4.50.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "menuitem",
				AuthContext: "standing",
				TargetField: "menuItemKey",
				TargetType:  "menuitem",
				Reads:       []string{"{payload.menuItemKey}", "{payload.menuItemKey}.price"},
				// The operator-role confinement probe (ddls.go
				// actor_holds_operator, reached through require_workplace).
				Enumerations: []pkgmgr.EnumerationSpec{
					{Hub: "{actor}", Relation: "holdsRole", Direction: "out"},
				},
			},
		},
	}
}
