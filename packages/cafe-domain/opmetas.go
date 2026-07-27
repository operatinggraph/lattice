package cafedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3, edge-manifest Fire 1) for every cafe-domain op a person may trigger —
// the whole tab lifecycle: OpenTab, Charge, VoidCharge, Settle — mirroring
// clinic-domain's and wellness-domain's adoption.
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
// VoidCharge declares no ownership probe: it has no self grant at all (a POS
// correction is a staff decision even when reversing a resident's own mis-tap),
// so its only confinement is require_workplace, whose site walk is a class-(e)
// enumeration the caller cannot pre-declare.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "OpenTab",
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
				},
			},
		},
		{
			OperationType: "Charge",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Order an item",
				Description: "Add a menu item to your open tab.",
				Icon:        "cafe",
				Tone:        "primary",
				SubmitLabel: "Add to tab",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"tabKey":{"type":"string","description":"vtx.tab.<NanoID> of your open tab — auto-filled from the tab you opened."},` +
				`"menuItemKey":{"type":"string","x-entityRef":"menuitem","description":"vtx.menuitem.<NanoID> of the catalog item to order; its own price is what gets charged."}},` +
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
			},
		},
		{
			OperationType: "VoidCharge",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Void a charge",
				Description: "Correct a mis-tapped charge by taking it back off an open tab.",
				Icon:        "receipt",
				Tone:        "destructive",
				SubmitLabel: "Void charge",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"tabKey":{"type":"string","description":"vtx.tab.<NanoID> of the open tab to correct — auto-filled from the tab being viewed."},` +
				`"amountCents":{"type":"integer","minimum":1,"description":"Amount to take back off the tab, in whole cents."}},` +
				`"required":["tabKey","amountCents"]}`,
			FieldDescriptions: map[string]string{
				"tabKey":      "The tab being corrected — auto-filled by the client from the tab being viewed (dispatch.targetField), not user-entered.",
				"amountCents": "How much to subtract, in whole cents. A void larger than the running total clamps the tab to zero rather than failing.",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				Class:       "tab",
				AuthContext: "standing",
				TargetField: "tabKey",
				TargetType:  "tab",
				Reads:       []string{"{payload.tabKey}", "{payload.tabKey}.status"},
			},
		},
		{
			OperationType: "Settle",
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
				OptionalReads: []string{
					"lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}",
				},
			},
		},
	}
}
