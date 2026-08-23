package locationdomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// OpMetas declares descriptor-vocabulary metadata (edge-showcase-app-design.md
// §3.3, edge-manifest Fire 1) for CreateLocation — the one location op a
// staff renderer can offer today, and the fire that introduces
// `Dispatch.ClassChoices` (pkgmgr.OpDispatchSpec): unlike every op-metas.go
// precedent so far (clinic-domain, service-domain), CreateLocation is
// legitimately declared on THREE leaf DDLs — unit, building, and property
// (ddls.go locationLeafDDL) — so no single static Dispatch.Class names the
// envelope's class the way it does for an op declared on one DDL only. The
// caller picks which of `LocationTypes` (ddls.go:88) this submission targets,
// and that picked value travels as the envelope's own class field — exactly
// what internal/descriptorform/form.mjs's classChoices rendering path exists
// to collect.
//
// AuthContext "standing": CreateLocation is operator-only (permissions.go's
// mk("CreateLocation") grants only the operator role, scope=any — no
// consumer/self grant exists), the same posture clinic-domain's
// SetProviderHours/SetProviderTimeOff give their own operator-only ops.
//
// No TargetField: CreateLocation MINTS the location, so there is no
// pre-existing vertex for a client to derive the field from — the same
// free-choice-create shape as clinic-domain's CreateProvider/CreatePatient.
func OpMetas() []pkgmgr.OpMetaSpec {
	return []pkgmgr.OpMetaSpec{
		{
			OperationType: "CreateLocation",
			Presentation: &pkgmgr.OpPresentationSpec{
				Title:       "Add location",
				Description: "Add a new unit, building, or property.",
				Icon:        "map-pin",
				Tone:        "primary",
				SubmitLabel: "Add",
			},
			InputSchema: `{"type":"object","properties":` +
				`{"locationType":{"type":"string","enum":["unit","building","property"],"title":"Type","description":"The location level; sets the vtx.<locationType>.<NanoID> key prefix and the vertex class."},` +
				`"presentation":{"type":"object","title":"Display name","description":"Optional client-facing display metadata {name, description?, icon?, category?}. Absent → an undescribed location.",` +
				`"properties":{"name":{"type":"string"},"description":{"type":"string"},"icon":{"type":"string"},"category":{"type":"string"}}}},` +
				`"required":["locationType"]}`,
			FieldDescriptions: map[string]string{
				"locationType": "The location level: unit, building, or property. Determines the vtx.<locationType>.<NanoID> key prefix; the vertex class equals that same type.",
				"presentation": "Optional client-facing display metadata. When supplied, at least name is expected; absent leaves the location undescribed (renders a typed fallback, never PII).",
			},
			Dispatch: &pkgmgr.OpDispatchSpec{
				ClassChoices: LocationTypes,
				AuthContext:  "standing",
			},
		},
	}
}
