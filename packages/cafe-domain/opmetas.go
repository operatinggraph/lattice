package cafedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3, edge-manifest Fire 1) for cafe-domain's two consumer-invocable
// (scope=self) ops — OpenTab and Settle — mirroring clinic-domain's
// (Fire 5 Inc 1) and wellness-domain's (Fire 5 Inc 1) adoption. Charge has no
// op-meta: it stays operator-only (permissions.go), so it is never
// Facet-reachable.
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
// Dispatch.OptionalReads carries the absence-tolerant half both ops need: the
// per-lease cafeOpenTabGuard dedup key (absent on a lease's first-ever tab,
// TOMBSTONED once a prior tab settled — so a required Read would fail the
// common case) and the applicationFor ownership link the self-scope check
// probes. The link key is built with the `:id` template modifier, since a
// Contract #1 link is 6 segments of bare ids rather than a vtx key.
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
