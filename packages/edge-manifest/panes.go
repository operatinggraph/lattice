package edgemanifest

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Panes declares the server-pane descriptors this package projects
// (facet-discovery-restoration-design.md §2.1). A pane names the Protected
// read-model sections a staff client renders — table, projected columns,
// filter, ordering, dispatch target — as DATA, so the edge client's host
// executes panes generically and a new staff workflow ships as a descriptor
// edit here, with zero app change.
//
// Column roles the renderer composes from: `id` (row key), `title` (+
// `fallback` when null), `subtitle` (joined " · "), `meta` (labeled trailing
// facts), `badge` (chip; `valueLabels` maps raw values to display words),
// `time`/`timeEnd` (a range), `target` (the row's dispatch-target vertex key —
// offered ops come from op descriptors whose dispatch.targetType matches
// `dispatch.targetType` below, gated by their own visibleWhen against this
// row), `state` (a column visibleWhen conditions read), `hidden` (fetched,
// never rendered). `kind: money` carries `unit` (dollars|cents) so the
// renderer never guesses a currency scale from a column NAME.
//
// The `columns` list is the pane's read surface, and narrowing it is a
// data-protection decision made HERE, in reviewable package data: the
// schedule section deliberately projects visit existence and timing only —
// none of `read_clinic_appointments`' clinical-content columns (`reason`,
// `documented_at`, `follow_up_requested`, `follow_up_date`, `status_note`)
// may join it outside a deliberate, reviewed widening. panes_test.go pins
// that ban, relocated from the app host where this column list used to live.
func Panes() []pkgmgr.PaneSpec {
	return []pkgmgr.PaneSpec{
		{
			CanonicalName:  "staffWorklist",
			OfferedToRoles: []string{"frontOfHouse"},
			Title:          "Worklist",
			Icon:           "doc",
			Sections:       staffWorklistSections,
		},
	}
}

// staffWorklistSections is the front-desk worklist: pending lease
// applications, today's appointment schedule, and the workplace's recurring
// visit series. Row visibility is enforced by RLS under the reader's
// workplace grants (facet-staff-worlds-design.md §3.5) — no section filters
// by workplace, because the grant IS the confinement; the filters here are
// workflow state only.
const staffWorklistSections = `[
  {
    "id": "applications",
    "title": "Applications to review",
    "emptyCopy": "No applications waiting.",
    "source": {
      "table": "read_landlord_lease_applications",
      "columns": [
        {"name": "app_id", "kind": "text", "role": "id"},
        {"name": "applicant_name", "kind": "text", "role": "title", "fallback": "Applicant"},
        {"name": "unit_address", "kind": "text", "role": "subtitle", "fallback": "Unit"},
        {"name": "unit_city", "kind": "text", "role": "subtitle"},
        {"name": "signed_at", "label": "Signed", "kind": "datetime", "role": "meta"},
        {"name": "terms_move_in_date", "label": "Move-in", "kind": "date", "role": "meta"},
        {"name": "qualified", "kind": "badge", "role": "badge",
         "valueLabels": {"true": "qualified", "false": "incomplete"}},
        {"name": "terms_requested_rent", "label": "Rent", "kind": "money", "unit": "dollars", "role": "meta"}
      ],
      "filter": {"kind": "isNull", "column": "landlord_decision"},
      "orderBy": {"column": "signed_at", "nullsLast": true},
      "limit": 200
    }
  },
  {
    "id": "schedule",
    "title": "Today's schedule",
    "emptyCopy": "Nothing scheduled today.",
    "source": {
      "table": "read_clinic_appointments",
      "columns": [
        {"name": "appointment_id", "kind": "text", "role": "id"},
        {"name": "patient_key", "kind": "text", "role": "target"},
        {"name": "starts_at", "kind": "datetime", "role": "time"},
        {"name": "ends_at", "kind": "datetime", "role": "timeEnd"},
        {"name": "status", "kind": "badge", "role": "badge"},
        {"name": "patient_name", "kind": "text", "role": "title", "fallback": "Patient"},
        {"name": "provider_name", "kind": "text", "role": "subtitle", "fallback": "Provider"},
        {"name": "provider_specialty", "kind": "text", "role": "subtitle"}
      ],
      "filter": {"kind": "utcDay", "column": "starts_at"},
      "orderBy": {"column": "starts_at"},
      "limit": 200
    },
    "dispatch": {"targetColumn": "patient_key", "targetType": "patient"}
  },
  {
    "id": "visitSeries",
    "title": "Recurring visit series",
    "emptyCopy": "No recurring series at this workplace.",
    "source": {
      "table": "read_visit_series",
      "columns": [
        {"name": "series_id", "kind": "text", "role": "id"},
        {"name": "entity_key", "kind": "text", "role": "target"},
        {"name": "patient_key", "kind": "text", "role": "hidden"},
        {"name": "patient_name", "kind": "text", "role": "title", "fallback": "Patient"},
        {"name": "provider_name", "kind": "text", "role": "subtitle"},
        {"name": "provider_specialty", "kind": "text", "role": "subtitle"},
        {"name": "interval_days", "label": "Every", "kind": "number", "role": "meta", "default": 0, "suffix": "d"},
        {"name": "next_due_at", "label": "Next due", "kind": "datetime", "role": "meta"},
        {"name": "series_status", "kind": "badge", "role": "state", "default": "ended"}
      ],
      "orderBy": {"column": "next_due_at", "nullsLast": true, "tieBreak": "entity_key"},
      "limit": 200
    },
    "dispatch": {"targetColumn": "entity_key", "targetType": "visitseries"}
  }
]`
