package cafedomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3, edge-manifest Fire 1) for cafe-domain's two consumer-invocable
// (scope=self) ops — OpenTab and Settle — mirroring clinic-domain's
// (Fire 5 Inc 1) and wellness-domain's (Fire 5 Inc 1) adoption. Charge has no
// op-meta: it stays operator-only (permissions.go), so it is never
// Facet-reachable.
//
// leaseAppKey (OpenTab) is not auto-fillable via dispatch.targetField or
// dispatch.contextParams the way clinic's provider/wellness's session are —
// a tab is a fresh vertex the op mints, there is no pre-existing "tab being
// viewed" to derive it from — so it is described in prose, the same
// treatment clinic-domain gives its own non-auto-fillable "patient" field.
// tabKey (Settle) auto-fills the ordinary way, from the tab OpenTab's own
// response returned (the client's own local record of what it just opened).
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
// UnknownLeaseApplication. This does not by itself make OpenTab fully
// descriptor-form-drivable: the per-lease cafeOpenTabGuard dedup read and
// the self-scope ownership check both need the guard aspect key and the
// applicationFor link in ContextHint.OptionalReads, and OpDispatchSpec has
// no OptionalReads-equivalent field yet — a genuine, still-open vocabulary
// gap (§7.11's residual), not something this comment papers over.
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
				Class:       "tab",
				AuthContext: "self",
				Reads:       []string{"{payload.leaseAppKey}"},
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
			},
		},
	}
}
