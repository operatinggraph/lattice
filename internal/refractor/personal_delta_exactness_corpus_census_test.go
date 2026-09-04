// Personal-lens delta publication — the differential exactness census (T4),
// personal-lens-delta-publication-design.md §4.1 / §10 T4.
//
// §4.1 states one invariant and the whole of Increment 2 rests on it:
//
//	If an event on vertex v changes a row's content or existence, then v is in
//	that row's provenance AS EVALUATED AFTER THE EVENT.
//
// Everything else in the increment is plumbing around that sentence. The write
// loop publishes a row iff its provenance meets the event's vertices, so a row
// whose content moved without naming the mover would be WITHHELD — the device
// would keep a stale copy, the authoritative frame would name its key, and
// nothing would ever correct it before the healer's daily content cycle. The
// design does not argue the invariant; it says "pinned by T4, not argued". This
// file is that pin.
//
// WHAT IS PINNED, exactly.
//
// For every shipped Personal Lens AS COMPOSED — its whole branch set through
// pipeline.executeBranches/mergeBranchRows, which is where a multi-walk lens's
// provenance is actually decided (mergedProvenance unions the read set of every
// branch that produced NO row for a key, and that half is what carries a
// walk-owned column's null) — and one seeded fixture graph:
//
//	for each mutation kind (vertex body, aspect, link create, link tombstone,
//	vertex tombstone) applied to each vertex / each link of the fixture:
//	  evaluate the actor before and after, diff the rows by key, and assert
//	  every row that APPEARED or whose CONTENT CHANGED is one the publication
//	  scope built from that mutation's own vertices ADMITS.
//
// A row that DISAPPEARED is never the scope's concern — the authoritative
// keyset frame carries omission (§4.1's last clause) — so this census only
// counts those and asserts the frame is unaffected by the scope.
//
// WHY THE ASSERTION IS "ADMITS" AND NOT "PROVENANCE CONTAINS THE KEY". The two
// are the same claim read at different depths, and the boundary is the one that
// matters: PublishScope.Admits is the exact predicate writeResults and
// ReprojectPersonalActor apply per row (results.go's `admits`,
// reproject_personal.go's write loop), and it is deliberately more forgiving
// than provenance-containment in one direction — a row carrying NO provenance
// at all is admitted, so that an engine or a path that records nothing
// reproduces today's publication instead of silencing a device. Asserting on
// Admits therefore pins what the device actually receives. It also means a
// census where NOTHING is ever withheld would pass vacuously, which is why
// every lens's withheld count is asserted non-zero below: the positive control
// for a table of negatives.
//
// WHY ReprojectPersonalActor AND NOT THE CDC CONSUMER. The scoped write loop
// and the scoped reprojection share the evaluation (reprojectActors →
// executeFullForActor → executeBranches → mergeBranchRows) and share the
// predicate (PublishScope.Admits). What differs is only how the scope is BUILT
// — the four CDC arms — and that is T3's subject, pinned in the pipeline
// package against each arm. Driving the reprojection lets this census control
// the mutation, the scope and the moment of evaluation exactly, which a live
// consumer racing an adjacency rebuild cannot. The consumers here run only long
// enough to seed each pipeline's ordering token and are then stopped, the same
// posture personal_lens_delta_publication_e2e_test.go's fixture takes.
//
// WHAT IS NOT PINNED HERE. Whether a lens is ELIGIBLE for a scoped publish at
// all — the clock conjunct, the label-sigil conjunct and the
// point-seeded-anchor conjunct of §4.2 — is a property of the compiled rule,
// pinned by personal_derivation_corpus_census_test.go (clock) and the pipeline
// package's TestPublishScopeRefusal (all three).
// This census is about what provenance says once the scope is granted; it
// deliberately hands the scope in rather than deriving it, so a lens that
// became scan-seeded would still be measured here and would fail its sibling
// census instead.
//
// WHEN THIS FAILS. A violation is a SOUNDNESS finding, never a reason to widen
// the assertion: it names a (lens, mutation, row) for which the shipped engine
// records a provenance set that does not include a vertex whose change moved
// that row, which is a device holding a stale row in production. The named
// vector below (TestPersonalDeltaExactness_WalkOwnedColumnSurvives) is the
// adversarial pass's own counterexample and fails first when the branch-merge
// half of §4.1 site 7 regresses.
//
// DERIVATION COMMAND (re-run this, do not trust a number in a build note):
//
//	go test ./internal/refractor/ -run TestPersonalDeltaExactness -count=1 -v
package refractor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/substrate"
	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
)

// deltaExactnessLensNames is the population this census governs, pinned as an
// exact SET rather than a count.
//
// An enumeration that silently reached nothing reads identically to a table of
// passing rows, and a NEW personal lens is precisely the event whose provenance
// nobody has yet argued about. The set is asserted equal, not contained, so a
// lens leaving the package moves this list too.
var deltaExactnessLensNames = []string{
	"edgeCatalog",
	"edgeEntityBookings",
	"edgeEntityMenuItems",
	"edgeEntityProviders",
	"edgeEntitySessions",
	"edgeEntityStudios",
	"edgeEntityTabs",
	"edgeIdentity",
	"edgeInstances",
	"edgeProviderQueue",
	"edgeProviderSchedule",
	"edgeServices",
	"edgeStaffPanes",
	"edgeStaffWorkOrders",
	"edgeTasks",
}

// deltaRecorder is the target a lens under census publishes through: it records
// what the write loop chose to send, which is the whole of what this census
// measures.
//
// It implements adapter.KeySetPublisher because ReprojectPersonalActor refuses
// a target that publishes no frame — and because the frame is half the claim
// (a withheld row must still be NAMED, or the client prunes the copy it holds).
// It deliberately implements nothing else: no PublishPipelineOpener, no
// OutcomeUpserter, so every write lands synchronously and the recording is
// complete the moment the call returns.
type deltaRecorder struct {
	mu      sync.Mutex
	rows    map[string]string
	deletes []string
	frames  [][]string
}

func newDeltaRecorder() *deltaRecorder {
	return &deltaRecorder{rows: map[string]string{}}
}

func (r *deltaRecorder) Upsert(_ context.Context, keys, row map[string]any, _ uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[deltaRowKey(keys)] = deltaCanonical(row)
	return nil
}

func (r *deltaRecorder) Delete(_ context.Context, keys map[string]any, _ uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletes = append(r.deletes, deltaRowKey(keys))
	return nil
}

func (r *deltaRecorder) Probe(context.Context) error { return nil }
func (r *deltaRecorder) Close() error                { return nil }

func (r *deltaRecorder) PublishKeySet(_ context.Context, _ string, keys []map[string]any, _ uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	named := make([]string, 0, len(keys))
	for _, k := range keys {
		named = append(named, deltaRowKey(k))
	}
	sort.Strings(named)
	r.frames = append(r.frames, named)
	return nil
}

func (r *deltaRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = map[string]string{}
	r.deletes = nil
	r.frames = nil
}

// snapshot copies out what one reprojection published.
func (r *deltaRecorder) snapshot() (rows map[string]string, deletes []string, frames [][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows = make(map[string]string, len(r.rows))
	for k, v := range r.rows {
		rows[k] = v
	}
	deletes = append([]string(nil), r.deletes...)
	sort.Strings(deletes)
	return rows, deletes, append([][]string(nil), r.frames...)
}

// deltaRowKey renders a result's key map as the stable identity this census
// diffs by — the lens's own IntoKey columns (__actor, ns, and for all but
// edgeIdentity an entityId), which is exactly the key the device stores under.
func deltaRowKey(keys map[string]any) string {
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	out := ""
	for _, n := range names {
		out += fmt.Sprintf("%s=%v|", n, keys[n])
	}
	return out
}

// deltaCanonical renders a row's VALUES as one comparable string. JSON is the
// canonicalisation because the engine's values are JSON-shaped throughout
// (scalars, lists and maps read out of Core KV documents) and Go's encoder
// sorts map keys, so two evaluations of an unchanged row render identically.
func deltaCanonical(row map[string]any) string {
	b, err := json.Marshal(row)
	if err != nil {
		return fmt.Sprintf("unmarshalable:%v", row)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// the fixture graph
// ---------------------------------------------------------------------------

// deltaFixtureVertex is one vertex of the census graph plus the two mutations
// this census applies to it.
//
// dataAlt and aspectAlt are DECLARED rather than derived (a generic "append a
// suffix to every string" mutation would flip edgeTasks' `status = "open"` and
// edgeEntityTabs' `status.value = "open"` filters and turn a content-change
// vector into a row-disappears one, which asserts nothing). Where a lens reads
// the field, the mutation moves a projected column; where none does, the
// mutation is still a real CDC event on that vertex and the census still runs
// every lens against it — which is the coverage half of the claim.
type deltaFixtureVertex struct {
	typ   string
	seed  string
	class string
	data  map[string]any
	// dataAlt is the whole replacement `data` body of the vertex-body vector.
	dataAlt map[string]any
	// aspects are written at seed time, keyed by localName.
	aspects map[string]map[string]any
	// mutAspect names which of them the aspect vector rewrites, and aspectAlt
	// is its replacement body.
	mutAspect string
	aspectAlt map[string]any
}

func (v deltaFixtureVertex) id() string { return pl2NanoID("t4delta-" + v.seed) }

func (v deltaFixtureVertex) key() string { return substrate.VertexKey(v.typ, v.id()) }

// deltaFixtureLink is one edge of the census graph. Direction follows the
// composed cypher's own pattern, which is Contract #1's rule (the
// later-arriving vertex is the source).
type deltaFixtureLink struct {
	srcType, srcSeed string
	rel              string
	dstType, dstSeed string
}

func (l deltaFixtureLink) srcID() string { return pl2NanoID("t4delta-" + l.srcSeed) }
func (l deltaFixtureLink) dstID() string { return pl2NanoID("t4delta-" + l.dstSeed) }
func (l deltaFixtureLink) key() string {
	return substrate.LinkKey(l.srcType, l.srcID(), l.rel, l.dstType, l.dstID())
}
func (l deltaFixtureLink) endpoints() []string {
	return []string{
		substrate.VertexKey(l.srcType, l.srcID()),
		substrate.VertexKey(l.dstType, l.dstID()),
	}
}

// deltaFixtureVertices is the census graph's vertex set: ONE identity wearing
// every hat the fifteen shipped lenses walk from — resident, staff role holder,
// clinic provider, wellness instructor and service provider — so a single actor
// seeds a row for each of them. The error bucket below asserts that claim
// rather than trusting it.
var deltaFixtureVertices = []deltaFixtureVertex{
	{
		typ: "identity", seed: "me", class: "identity",
		data:    map[string]any{"handle": "ada"},
		dataAlt: map[string]any{"handle": "ada-prime"},
		aspects: map[string]map[string]any{
			"name":  {"value": "Ada"},
			"state": {"value": "claimed"},
		},
		mutAspect: "name", aspectAlt: map[string]any{"value": "Ada Prime"},
	},
	{
		typ: "unit", seed: "home", class: "unit",
		data:      map[string]any{"number": "3B"},
		dataAlt:   map[string]any{"number": "4C"},
		aspects:   map[string]map[string]any{"presentation": {"name": "Unit 3B"}},
		mutAspect: "presentation", aspectAlt: map[string]any{"name": "Unit 4C"},
	},
	{
		typ: "building", seed: "bldg", class: "building",
		data:      map[string]any{"street": "12 Maple"},
		dataAlt:   map[string]any{"street": "14 Maple"},
		aspects:   map[string]map[string]any{"presentation": {"name": "Maple Court"}},
		mutAspect: "presentation", aspectAlt: map[string]any{"name": "Cedar Court"},
	},
	{
		typ: "service", seed: "tpl", class: "service",
		data:    map[string]any{"family": "backgroundCheck"},
		dataAlt: map[string]any{"family": "payment"},
		aspects: map[string]map[string]any{
			"presentation": {"name": "Laundry", "description": "Wash and fold", "icon": "basket", "category": "home"},
		},
		mutAspect: "presentation",
		aspectAlt: map[string]any{"name": "Laundry Plus", "description": "Wash, fold, press", "icon": "basket", "category": "home"},
	},
	{
		typ: "service", seed: "inst", class: "service",
		data:    map[string]any{"family": "backgroundCheck"},
		dataAlt: map[string]any{"family": "payment"},
		aspects: map[string]map[string]any{
			"outcome": {"status": "completed", "completedAt": "2026-08-01T00:00:00Z"},
		},
		mutAspect: "outcome",
		aspectAlt: map[string]any{"status": "failed", "completedAt": "2026-08-02T00:00:00Z"},
	},
	{
		typ: "meta", seed: "op", class: "meta",
		data:    map[string]any{"operationType": "RequestService"},
		dataAlt: map[string]any{"operationType": "RequestServiceUrgent"},
		aspects: map[string]map[string]any{
			"presentation": {"title": "Request a service", "shortLabel": "Request"},
		},
		mutAspect: "presentation",
		aspectAlt: map[string]any{"title": "Request a service now", "shortLabel": "Request"},
	},
	{
		typ: "meta", seed: "pane", class: "meta",
		data:    map[string]any{"paneKind": "server"},
		dataAlt: map[string]any{"paneKind": "mirror"},
		aspects: map[string]map[string]any{
			"paneDescriptor": {"paneId": "worklist", "title": "Worklist", "icon": "list", "surface": "staff", "sections": "[]"},
		},
		mutAspect: "paneDescriptor",
		aspectAlt: map[string]any{"paneId": "worklist", "title": "Work queue", "icon": "list", "surface": "staff", "sections": "[]"},
	},
	{
		typ: "role", seed: "role", class: "role",
		data:      map[string]any{"scope": "tenant"},
		dataAlt:   map[string]any{"scope": "global"},
		aspects:   map[string]map[string]any{"canonicalName": {"value": "frontOfHouse"}},
		mutAspect: "canonicalName", aspectAlt: map[string]any{"value": "frontOfHouseLead"},
	},
	{
		typ: "permission", seed: "perm", class: "permission",
		data:      map[string]any{"effect": "allow"},
		dataAlt:   map[string]any{"effect": "allowAudited"},
		aspects:   map[string]map[string]any{"presentation": {"name": "May request"}},
		mutAspect: "presentation", aspectAlt: map[string]any{"name": "May request (audited)"},
	},
	{
		typ: "task", seed: "task", class: "task",
		data:      map[string]any{"status": "open", "expiresAt": "2026-09-10T00:00:00Z"},
		dataAlt:   map[string]any{"status": "open", "expiresAt": "2026-12-31T00:00:00Z"},
		aspects:   map[string]map[string]any{"presentation": {"name": "Verify tenancy"}},
		mutAspect: "presentation", aspectAlt: map[string]any{"name": "Verify tenancy (rush)"},
	},
	{
		typ: "studio", seed: "studio", class: "studio",
		data:      map[string]any{"capacity": 20},
		dataAlt:   map[string]any{"capacity": 24},
		aspects:   map[string]map[string]any{"profile": {"name": "Studio A"}},
		mutAspect: "profile", aspectAlt: map[string]any{"name": "Studio B"},
	},
	{
		typ: "session", seed: "sess", class: "session",
		data:    map[string]any{"capacity": 12},
		dataAlt: map[string]any{"capacity": 14},
		aspects: map[string]map[string]any{
			"schedule": {"name": "Vinyasa", "startsAt": "2026-09-06T09:00:00Z"},
		},
		mutAspect: "schedule",
		aspectAlt: map[string]any{"name": "Hatha", "startsAt": "2026-09-06T10:00:00Z"},
	},
	{
		typ: "instructor", seed: "instr", class: "instructor",
		data:      map[string]any{"active": true},
		dataAlt:   map[string]any{"active": false},
		aspects:   map[string]map[string]any{"profile": {"displayName": "Iris"}},
		mutAspect: "profile", aspectAlt: map[string]any{"displayName": "Iris Bell"},
	},
	{
		typ: "provider", seed: "prov", class: "provider",
		data:      map[string]any{"specialty": "general"},
		dataAlt:   map[string]any{"specialty": "sports"},
		aspects:   map[string]map[string]any{"profile": {"fullName": "Dr Pike"}},
		mutAspect: "profile", aspectAlt: map[string]any{"fullName": "Dr Pike-Jones"},
	},
	{
		typ: "serviceprovider", seed: "sp", class: "serviceprovider",
		data:      map[string]any{"active": true},
		dataAlt:   map[string]any{"active": false},
		aspects:   map[string]map[string]any{"profile": {"displayName": "Maple Laundry"}},
		mutAspect: "profile", aspectAlt: map[string]any{"displayName": "Maple Laundry Co"},
	},
	{
		typ: "booking", seed: "bk", class: "booking",
		data:      map[string]any{"status": "confirmed"},
		dataAlt:   map[string]any{"status": "waitlisted"},
		aspects:   map[string]map[string]any{"presentation": {"name": "Vinyasa booking"}},
		mutAspect: "presentation", aspectAlt: map[string]any{"name": "Hatha booking"},
	},
	{
		typ: "leaseapp", seed: "la", class: "leaseapp",
		data:      map[string]any{"stage": "signed"},
		dataAlt:   map[string]any{"stage": "renewing"},
		aspects:   map[string]map[string]any{"presentation": {"name": "Lease 3B"}},
		mutAspect: "presentation", aspectAlt: map[string]any{"name": "Lease 3B (renewal)"},
	},
	{
		typ: "tab", seed: "tab", class: "tab",
		data:      map[string]any{"opened": "2026-09-01T00:00:00Z"},
		dataAlt:   map[string]any{"opened": "2026-09-02T00:00:00Z"},
		aspects:   map[string]map[string]any{"status": {"value": "open", "totalCents": 1200}},
		mutAspect: "status",
		// The `value` stays "open" on purpose: this vector is a CONTENT change,
		// and flipping the filter would make it a row-disappears one, which
		// asserts nothing about provenance.
		aspectAlt: map[string]any{"value": "open", "totalCents": 3400},
	},
	{
		typ: "menuitem", seed: "item", class: "menuitem",
		data:      map[string]any{"sku": "esp-1"},
		dataAlt:   map[string]any{"sku": "esp-2"},
		aspects:   map[string]map[string]any{"price": {"name": "Espresso", "priceCents": 350}},
		mutAspect: "price", aspectAlt: map[string]any{"name": "Espresso", "priceCents": 400},
	},
	{
		typ: "workorder", seed: "wo", class: "workorder",
		data:    map[string]any{"trade": "plumbing"},
		dataAlt: map[string]any{"trade": "electrical"},
		aspects: map[string]map[string]any{
			"report": {"summary": "Fix the sink", "priority": "high", "reportedAt": "2026-09-01T00:00:00Z"},
		},
		mutAspect: "report",
		aspectAlt: map[string]any{"summary": "Fix the sink and the tap", "priority": "urgent", "reportedAt": "2026-09-01T00:00:00Z"},
	},
	{
		typ: "appointment", seed: "appt", class: "appointment",
		data:    map[string]any{"channel": "clinic"},
		dataAlt: map[string]any{"channel": "telehealth"},
		aspects: map[string]map[string]any{
			"schedule": {"reason": "Check-up", "startsAt": "2026-09-07T09:00:00Z", "endsAt": "2026-09-07T09:30:00Z"},
			"status":   {"value": "booked"},
		},
		mutAspect: "schedule",
		aspectAlt: map[string]any{"reason": "Follow-up", "startsAt": "2026-09-07T11:00:00Z", "endsAt": "2026-09-07T11:30:00Z"},
	},

	// --- the SECOND row of every lens that can have one ---------------------
	//
	// A one-row actor cannot tell a scoped publish from a whole-actor one: both
	// are the same message set. Each vertex below is a second anchor for one of
	// the lenses above, so a mutation on the first row's tail has a sibling row
	// it must NOT republish — which is what the withheld column measures and
	// what the invariant is actually about. edgeIdentity is the one lens with
	// no second row by construction: `manifest.me` is one row per actor.
	{
		typ: "service", seed: "tpl2", class: "service",
		data:    map[string]any{"family": "payment"},
		dataAlt: map[string]any{"family": "backgroundCheck"},
		aspects: map[string]map[string]any{
			"presentation": {"name": "Parcel hold", "description": "Hold a parcel", "icon": "box", "category": "home"},
		},
		mutAspect: "presentation",
		aspectAlt: map[string]any{"name": "Parcel hold+", "description": "Hold a parcel longer", "icon": "box", "category": "home"},
	},
	{
		typ: "meta", seed: "op2", class: "meta",
		data:    map[string]any{"operationType": "CloseWorkOrder"},
		dataAlt: map[string]any{"operationType": "CloseWorkOrderNow"},
		aspects: map[string]map[string]any{
			"presentation": {"title": "Close a work order", "shortLabel": "Close"},
		},
		mutAspect: "presentation",
		aspectAlt: map[string]any{"title": "Close this work order", "shortLabel": "Close"},
	},
	{
		typ: "permission", seed: "perm2", class: "permission",
		data:      map[string]any{"effect": "allow"},
		dataAlt:   map[string]any{"effect": "allowAudited"},
		aspects:   map[string]map[string]any{"presentation": {"name": "May close"}},
		mutAspect: "presentation", aspectAlt: map[string]any{"name": "May close (audited)"},
	},
	{
		typ: "task", seed: "task2", class: "task",
		data:      map[string]any{"status": "open", "expiresAt": "2026-10-01T00:00:00Z"},
		dataAlt:   map[string]any{"status": "open", "expiresAt": "2026-11-01T00:00:00Z"},
		aspects:   map[string]map[string]any{"presentation": {"name": "Restock"}},
		mutAspect: "presentation", aspectAlt: map[string]any{"name": "Restock urgently"},
	},
	{
		typ: "session", seed: "sess2", class: "session",
		data:    map[string]any{"capacity": 8},
		dataAlt: map[string]any{"capacity": 10},
		aspects: map[string]map[string]any{
			"schedule": {"name": "Pilates", "startsAt": "2026-09-08T09:00:00Z"},
		},
		mutAspect: "schedule",
		aspectAlt: map[string]any{"name": "Reformer", "startsAt": "2026-09-08T10:00:00Z"},
	},
	{
		typ: "service", seed: "inst2", class: "service",
		data:      map[string]any{"family": "backgroundCheck"},
		dataAlt:   map[string]any{"family": "payment"},
		aspects:   map[string]map[string]any{"outcome": {"status": "completed", "completedAt": "2026-08-03T00:00:00Z"}},
		mutAspect: "outcome",
		aspectAlt: map[string]any{"status": "failed", "completedAt": "2026-08-04T00:00:00Z"},
	},
	{
		typ: "provider", seed: "prov2", class: "provider",
		data:      map[string]any{"specialty": "physio"},
		dataAlt:   map[string]any{"specialty": "podiatry"},
		aspects:   map[string]map[string]any{"profile": {"fullName": "Dr Osei"}},
		mutAspect: "profile", aspectAlt: map[string]any{"fullName": "Dr Osei-Boateng"},
	},
	{
		typ: "studio", seed: "studio2", class: "studio",
		data:      map[string]any{"capacity": 12},
		dataAlt:   map[string]any{"capacity": 15},
		aspects:   map[string]map[string]any{"profile": {"name": "Studio C"}},
		mutAspect: "profile", aspectAlt: map[string]any{"name": "Studio D"},
	},
	{
		typ: "menuitem", seed: "item2", class: "menuitem",
		data:      map[string]any{"sku": "cort-1"},
		dataAlt:   map[string]any{"sku": "cort-2"},
		aspects:   map[string]map[string]any{"price": {"name": "Cortado", "priceCents": 420}},
		mutAspect: "price", aspectAlt: map[string]any{"name": "Cortado", "priceCents": 450},
	},
	{
		typ: "workorder", seed: "wo2", class: "workorder",
		data:    map[string]any{"trade": "electrical"},
		dataAlt: map[string]any{"trade": "carpentry"},
		aspects: map[string]map[string]any{
			"report": {"summary": "Replace the hall light", "priority": "low", "reportedAt": "2026-09-02T00:00:00Z"},
		},
		mutAspect: "report",
		aspectAlt: map[string]any{"summary": "Replace both hall lights", "priority": "medium", "reportedAt": "2026-09-02T00:00:00Z"},
	},
	{
		typ: "booking", seed: "bk2", class: "booking",
		data:      map[string]any{"status": "confirmed"},
		dataAlt:   map[string]any{"status": "waitlisted"},
		aspects:   map[string]map[string]any{"presentation": {"name": "Pilates booking"}},
		mutAspect: "presentation", aspectAlt: map[string]any{"name": "Reformer booking"},
	},
	{
		typ: "tab", seed: "tab2", class: "tab",
		data:      map[string]any{"opened": "2026-09-03T00:00:00Z"},
		dataAlt:   map[string]any{"opened": "2026-09-04T00:00:00Z"},
		aspects:   map[string]map[string]any{"status": {"value": "open", "totalCents": 800}},
		mutAspect: "status", aspectAlt: map[string]any{"value": "open", "totalCents": 950},
	},
	{
		typ: "appointment", seed: "appt2", class: "appointment",
		data:    map[string]any{"channel": "clinic"},
		dataAlt: map[string]any{"channel": "telehealth"},
		aspects: map[string]map[string]any{
			"schedule": {"reason": "Physio", "startsAt": "2026-09-09T09:00:00Z", "endsAt": "2026-09-09T09:45:00Z"},
			"status":   {"value": "booked"},
		},
		mutAspect: "schedule",
		aspectAlt: map[string]any{"reason": "Physio review", "startsAt": "2026-09-09T14:00:00Z", "endsAt": "2026-09-09T14:45:00Z"},
	},
	{
		typ: "meta", seed: "pane2", class: "meta",
		data:    map[string]any{"paneKind": "server"},
		dataAlt: map[string]any{"paneKind": "mirror"},
		aspects: map[string]map[string]any{
			"paneDescriptor": {"paneId": "signin", "title": "Sign-in methods", "icon": "key", "surface": "staff", "sections": "[]"},
		},
		mutAspect: "paneDescriptor",
		aspectAlt: map[string]any{"paneId": "signin", "title": "Sign-in", "icon": "key", "surface": "staff", "sections": "[]"},
	},
}

// deltaFixtureLinks is the census graph's edge set, one entry per hop some
// composed branch walks. Every one of them is load-bearing for at least one
// lens, which is what makes the link vectors real: a tombstone here removes a
// hop a shipped walk depends on, not a synthetic relation nothing matches.
var deltaFixtureLinks = []deltaFixtureLink{
	{"identity", "me", "residesIn", "unit", "home"},
	{"unit", "home", "containedIn", "building", "bldg"},
	{"identity", "me", "worksAt", "building", "bldg"},
	{"identity", "me", "holdsRole", "role", "role"},
	{"service", "tpl", "availableAt", "building", "bldg"},
	{"service", "tpl", "permitsOperation", "meta", "op"},
	{"service", "tpl", "providedBy", "serviceprovider", "sp"},
	{"permission", "perm", "grantedBy", "role", "role"},
	{"permission", "perm", "forOperation", "meta", "op"},
	{"task", "task", "assignedTo", "identity", "me"},
	{"task", "task", "forOperation", "meta", "op"},
	{"task", "task", "queuedFor", "role", "role"},
	{"service", "inst", "providedTo", "identity", "me"},
	{"service", "inst", "instanceOf", "service", "tpl"},
	{"studio", "studio", "locatedAt", "building", "bldg"},
	{"session", "sess", "atStudio", "studio", "studio"},
	{"session", "sess", "ledBy", "instructor", "instr"},
	{"instructor", "instr", "identifiedBy", "identity", "me"},
	{"provider", "prov", "practicesAt", "building", "bldg"},
	{"provider", "prov", "identifiedBy", "identity", "me"},
	{"appointment", "appt", "withProvider", "provider", "prov"},
	{"booking", "bk", "bookedBy", "identity", "me"},
	{"booking", "bk", "forSession", "session", "sess"},
	{"leaseapp", "la", "applicationFor", "identity", "me"},
	{"leaseapp", "la", "appliesToUnit", "unit", "home"},
	{"tab", "tab", "openFor", "leaseapp", "la"},
	{"menuitem", "item", "servedAt", "building", "bldg"},
	{"workorder", "wo", "locatedAt", "building", "bldg"},
	{"meta", "pane", "offeredTo", "role", "role"},
	{"serviceprovider", "sp", "identifiedBy", "identity", "me"},

	// The second anchor of each lens that can hold one. op2 is reachable by the
	// STAFF walk alone, which is what makes edgeCatalog's two rows structurally
	// different: one merged across three walks, one produced by a single branch
	// with the other two contributing only a read set.
	{"service", "tpl2", "availableAt", "building", "bldg"},
	{"permission", "perm2", "grantedBy", "role", "role"},
	{"permission", "perm2", "forOperation", "meta", "op2"},
	{"task", "task2", "queuedFor", "role", "role"},
	{"session", "sess2", "atStudio", "studio", "studio"},
	{"service", "inst2", "providedTo", "identity", "me"},
	{"service", "inst2", "instanceOf", "service", "tpl"},
	{"provider", "prov2", "practicesAt", "building", "bldg"},
	{"studio", "studio2", "locatedAt", "building", "bldg"},
	{"menuitem", "item2", "servedAt", "building", "bldg"},
	{"workorder", "wo2", "locatedAt", "building", "bldg"},
	{"booking", "bk2", "bookedBy", "identity", "me"},
	{"booking", "bk2", "forSession", "session", "sess2"},
	{"tab", "tab2", "openFor", "leaseapp", "la"},
	{"appointment", "appt2", "withProvider", "provider", "prov"},
	{"meta", "pane2", "offeredTo", "role", "role"},
}

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// deltaExactnessLens is one lens under census, wired the way cmd/refractor
// wires it: the composed branch set through UseFullEngineBranches (never
// UseFullEngine, which would evaluate branch 0 alone and never reach the merge
// this census exists to cover) and projection.InstallPersonalLens.
type deltaExactnessLens struct {
	name     string
	branches int
	pipe     *pipeline.Pipeline
	rec      *deltaRecorder
}

type deltaExactnessFixture struct {
	h          *pl2Harness
	identityID string
	lenses     []*deltaExactnessLens
}

// deltaMeasurement is one lens's answer for one mutation: the complete row set
// after it, the keys the frame named, and the keys the scoped publish actually
// wrote.
type deltaMeasurement struct {
	rows     map[string]string
	framed   []string
	admitted map[string]struct{}
}

func newDeltaExactnessFixture(t *testing.T) *deltaExactnessFixture {
	t.Helper()
	h := newPL2Harness(t)
	f := &deltaExactnessFixture{h: h, identityID: pl2NanoID("t4delta-me")}

	f.lenses = installDeltaExactnessLenses(t, h)

	// The ordering token first, and DELIBERATELY before the graph exists.
	// ReprojectPersonalActor refuses at revision 0 (a frame the client would
	// discard), so each pipeline has to have applied at least one CDC event —
	// and the cheapest events to give it are the lens meta-vertices the install
	// above just wrote, which reach no identity and fan out to nothing. Seeding
	// the token after the graph would make every pipeline reproject the whole
	// actor for every fixture write first.
	stop := runDeltaConsumersUntilSeeded(t, h, f.lenses)
	stop()

	f.seedGraph(t)
	return f
}

// installDeltaExactnessLenses activates every shipped edge-manifest Personal
// Lens against a recording target, through the production path: the composed
// definition (ExpandReadGrantWalks — a declared Spec on its own is a
// presentation tail, not a runnable query), the real CoreKVSource compile, and
// projection.InstallPersonalLens.
//
// capKV is nil, which disables the D1 read-grant gate: this census is about
// provenance, and running the gate would need a cap-read slice per anchor and
// would then silently DROP rows for reasons that have nothing to do with what
// is being measured. interestKV is threaded, which is production wiring — an
// identity with no registrations is relevant to everything, so the filter is
// installed and inert.
func installDeltaExactnessLenses(t *testing.T, h *pl2Harness) []*deltaExactnessLens {
	t.Helper()
	const subjectPrefix = "lattice.sync.user"
	const syncStream = "SYNC"

	composed, err := edgemanifest.Package.ExpandReadGrantWalks()
	require.NoError(t, err, "edge-manifest's read-grant walks must compose")
	var specs []pkgmgr.LensSpec
	for _, ls := range composed.Lenses {
		if ls.Adapter == "nats-subject" && ls.Personal {
			specs = append(specs, ls)
		}
	}
	got := make([]string, 0, len(specs))
	for _, ls := range specs {
		got = append(got, ls.CanonicalName)
	}
	sort.Strings(got)
	require.Equal(t, deltaExactnessLensNames, got,
		"the personal-lens population moved — a new lens needs §4.1's invariant read for it before it is added here")

	lensIDs := make(map[string]string, len(specs))
	for _, ls := range specs {
		lensIDs[ls.CanonicalName] = pl2NanoID("t4delta-lens-" + ls.CanonicalName)
	}

	src := lens.NewCoreKVSource(h.conn, "core-kv", "test", h.logger)
	activated := make(chan *lens.Rule, len(specs)*2)
	src.SetLoadCallback(func(r *lens.Rule) {
		select {
		case activated <- r:
		default:
		}
	})
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(h.ctx))

	for _, ls := range specs {
		lensID := lensIDs[ls.CanonicalName]
		keyField := append([]string{adapter.PersonalActorKeyField}, ls.IntoKey[1:]...)
		keyJSON, err := json.Marshal(keyField)
		require.NoError(t, err)

		metaVertexKey := "vtx.meta." + lensID
		vertexJSON, err := json.Marshal(map[string]any{"class": "meta.lens", "key": metaVertexKey, "data": map[string]any{}})
		require.NoError(t, err)
		_, err = h.coreKV.Put(h.ctx, metaVertexKey, vertexJSON)
		require.NoError(t, err)

		spec := lens.LensSpec{
			ID: lensID, CanonicalName: ls.CanonicalName, TargetType: "nats_subject",
			CypherRule: ls.Spec, CypherBranches: ls.SpecBranches,
			TargetConfig: json.RawMessage(`{"subjectPrefix":"` + subjectPrefix + `","stream":"` + syncStream +
				`","personal":true,"key":` + string(keyJSON) + `}`),
		}
		specJSON, err := json.Marshal(spec)
		require.NoError(t, err)
		_, err = h.coreKV.Put(h.ctx, metaVertexKey+".spec", specJSON)
		require.NoError(t, err)
	}

	rules := map[string]*lens.Rule{}
	deadline := time.Now().Add(30 * time.Second)
	for len(rules) < len(specs) {
		remaining := time.Until(deadline)
		require.Positivef(t, remaining, "only activated %d/%d personal lenses", len(rules), len(specs))
		select {
		case r := <-activated:
			if _, want := lensIDs[r.CanonicalName]; want {
				rules[r.CanonicalName] = r
			}
		case <-time.After(remaining):
		}
	}

	out := make([]*deltaExactnessLens, 0, len(specs))
	for _, ls := range specs {
		r := rules[ls.CanonicalName]
		require.Truef(t, projection.IsPersonalLens(r), "%s must install as a personal lens", ls.CanonicalName)
		rec := newDeltaRecorder()
		p, err := pipeline.New(r.ID, "nats_subject", "core-kv", h.adjKV, h.coreKV, rec, nil)
		require.NoError(t, err)
		// The BRANCH form, not UseFullEngine. executeBranches takes the merge
		// arm on `len(rs.branches) > 1` alone, so a census wired through
		// UseFullEngine would evaluate a multi-walk lens's branch 0 by itself
		// and never exercise mergedProvenance — the half of §4.1 the
		// adversarial pass found missing.
		require.NoError(t, p.UseFullEngineBranches(fullEngineSingleton, r.CompiledRule, r.CompiledBranches))
		require.Truef(t, projection.InstallPersonalLens(p, r, h.adjKV, h.coreKV, h.interestKV, nil, false, h.logger),
			"%s must install through projection.InstallPersonalLens", ls.CanonicalName)
		out = append(out, &deltaExactnessLens{
			name: ls.CanonicalName, branches: len(r.CompiledBranches), pipe: p, rec: rec,
		})
	}
	return out
}

// runDeltaConsumersUntilSeeded runs every pipeline's real CDC consumer just
// long enough for each to hold a non-zero ordering token, then returns the
// stopper. Nothing is asserted about what they published: the recorders are
// reset before the first measurement.
func runDeltaConsumersUntilSeeded(t *testing.T, h *pl2Harness, lenses []*deltaExactnessLens) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{}, len(lenses))
	for _, l := range lenses {
		l.pipe.RunOn(h.conn, e2eSpec("t4delta-"+l.name, "core-kv"))
		go func(p *pipeline.Pipeline) { defer func() { done <- struct{}{} }(); p.Run(ctx) }(l.pipe)
	}
	require.Eventually(t, func() bool {
		for _, l := range lenses {
			if l.pipe.Progress().LastAppliedSeq == 0 {
				return false
			}
		}
		return true
	}, 60*time.Second, 50*time.Millisecond,
		"every personal pipeline must apply a CDC event before it can publish a frame at a usable revision")

	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		for range lenses {
			<-done
		}
	}
}

// seedGraph writes the census graph and waits for the adjacency index to hold
// every edge — the executor reads adjacency, so an evaluation before the
// bootstrapper has caught up would measure a graph nobody wrote.
func (f *deltaExactnessFixture) seedGraph(t *testing.T) {
	t.Helper()
	for _, v := range deltaFixtureVertices {
		emWriteVertex(t, f.h.ctx, f.h.coreKV, v.key(), v.class, v.data)
		for localName, body := range v.aspects {
			emWriteAspect(t, f.h.ctx, f.h.coreKV, v.key(), localName, v.class+"."+localName, body)
		}
	}
	for _, l := range deltaFixtureLinks {
		emWriteLink(t, f.h.ctx, f.h.coreKV, l.srcType, l.srcID(), l.rel, l.dstType, l.dstID())
	}
	for _, l := range deltaFixtureLinks {
		f.awaitLinkAdjacency(t, l, true)
	}
}

// awaitLinkAdjacency blocks until BOTH endpoints' adjacency answers agree with
// present — the deterministic barrier this census uses in place of a sleep. It
// reads through adjacency.Neighbors, which is the executor's own read, so what
// it observes is exactly what the next evaluation will see.
func (f *deltaExactnessFixture) awaitLinkAdjacency(t *testing.T, l deltaFixtureLink, present bool) {
	t.Helper()
	linkKey := l.key()
	for _, nodeID := range []string{l.srcID(), l.dstID()} {
		node := nodeID
		require.Eventuallyf(t, func() bool {
			edges, _, err := adjacency.Neighbors(f.h.ctx, f.h.adjKV, f.h.coreKV, node)
			if err != nil {
				return false
			}
			for _, e := range edges {
				if e.EdgeID == linkKey {
					return present
				}
			}
			return !present
		}, 30*time.Second, 20*time.Millisecond,
			"adjacency for node %s must report link %s present=%v", node, linkKey, present)
	}
}

// writeLinkState writes a link envelope live or tombstoned and waits for the
// index to agree.
func (f *deltaExactnessFixture) writeLinkState(t *testing.T, l deltaFixtureLink, live bool) {
	t.Helper()
	body := map[string]any{
		"key": l.key(), "class": l.rel, "isDeleted": !live,
		"sourceVertex": substrate.VertexKey(l.srcType, l.srcID()),
		"targetVertex": substrate.VertexKey(l.dstType, l.dstID()),
		"localName":    l.rel,
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = f.h.coreKV.Put(f.h.ctx, l.key(), b)
	require.NoError(t, err)
	f.awaitLinkAdjacency(t, l, live)
}

// writeVertexState writes a vertex root body live or tombstoned. A vertex event
// carries no adjacency payload (the bootstrapper acks and skips it), so the
// write is visible to the next fetchNode the moment Put returns.
func (f *deltaExactnessFixture) writeVertexState(t *testing.T, v deltaFixtureVertex, data map[string]any, deleted bool) {
	t.Helper()
	body := map[string]any{
		"key": v.key(), "class": v.class, "isDeleted": deleted,
		"createdAt": "2026-09-01T00:00:00Z", "lastModifiedAt": "2026-09-04T00:00:00Z",
		"data": data,
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = f.h.coreKV.Put(f.h.ctx, v.key(), b)
	require.NoError(t, err)
}

// measure evaluates every lens twice for the actor: unscoped, which is the
// complete post-mutation row set and the authoritative frame; and under the
// scope the CDC arm builds for this mutation's own vertices, which is what the
// write loop would publish.
//
// Two evaluations of one graph state, not one — the row VALUES and the scope
// DECISION are different questions and PublishScope answers only the second.
// The graph does not move between them (nothing else writes Core KV at this
// point), so the pair is a coherent before/after of the same instant.
func (f *deltaExactnessFixture) measure(t *testing.T, scopeVertices []string) map[string]deltaMeasurement {
	t.Helper()
	scope := pipeline.ScopeVertices(scopeVertices)
	require.Equalf(t, pipeline.ScopeKindVertices, scope.Kind(),
		"the census must measure under a VERTEX scope; %v widened to %s", scopeVertices, scope)

	out := make(map[string]deltaMeasurement, len(f.lenses))
	for _, l := range f.lenses {
		l.rec.reset()
		require.NoErrorf(t, l.pipe.ReprojectPersonalActor(f.h.ctx, f.identityID, pipeline.ScopeAll()),
			"%s: unscoped reprojection", l.name)
		rows, deletes, frames := l.rec.snapshot()
		require.Lenf(t, frames, 1, "%s: one authoritative frame per reprojection", l.name)

		l.rec.reset()
		require.NoErrorf(t, l.pipe.ReprojectPersonalActor(f.h.ctx, f.identityID, scope),
			"%s: scoped reprojection", l.name)
		scopedRows, scopedDeletes, scopedFrames := l.rec.snapshot()
		require.Lenf(t, scopedFrames, 1, "%s: one authoritative frame per reprojection", l.name)
		assert.Equalf(t, frames[0], scopedFrames[0],
			"%s: the scope must not change the FRAME — a withheld row that goes unnamed is one the client prunes", l.name)
		assert.Equalf(t, deletes, scopedDeletes,
			"%s: a Delete is never scoped — a retraction is not a content change (§4.2)", l.name)

		admitted := make(map[string]struct{}, len(scopedRows))
		for k := range scopedRows {
			admitted[k] = struct{}{}
		}
		out[l.name] = deltaMeasurement{rows: rows, framed: frames[0], admitted: admitted}
	}
	return out
}

// deltaCensusRow is one lens's line of the report.
type deltaCensusRow struct {
	branches   int
	seeded     int
	mutations  int
	withheld   int
	violations int
}

// deltaViolation is one counterexample to §4.1's invariant.
type deltaViolation struct {
	lens, mutation, rowKey string
	appeared               bool
}

// ---------------------------------------------------------------------------
// the census
// ---------------------------------------------------------------------------

// TestPersonalDeltaExactness_CorpusCensus is T4: the differential exactness pin
// that gates Increment 2 (personal-lens-delta-publication-design.md §10, §11
// row 2).
func TestPersonalDeltaExactness_CorpusCensus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the delta-exactness corpus census in -short mode")
	}
	f := newDeltaExactnessFixture(t)

	// The baseline, measured under a scope naming a vertex the graph does not
	// contain: it is the unscoped half of this call that matters (the row set
	// every mutation is diffed against), and a scope that admits nothing proves
	// the second half of the pair is really deciding rather than passing
	// everything through.
	absent := substrate.VertexKey("identity", pl2NanoID("t4delta-nobody"))
	baselineRun := f.measure(t, []string{absent})
	baseline := map[string]map[string]string{}
	report := map[string]*deltaCensusRow{}
	for _, l := range f.lenses {
		baseline[l.name] = baselineRun[l.name].rows
		report[l.name] = &deltaCensusRow{branches: l.branches, seeded: len(baselineRun[l.name].rows)}
	}

	// The error bucket, enumerated and asserted EMPTY: a lens the fixture
	// cannot seed is a lens this census says nothing about, and a table of
	// fifteen passing rows where three of them measured nothing reads exactly
	// like a table of fifteen real ones.
	var unseeded []string
	for _, l := range f.lenses {
		if len(baselineRun[l.name].rows) == 0 {
			unseeded = append(unseeded, l.name)
		}
	}
	require.Emptyf(t, unseeded,
		"the fixture graph seeds no row for %v — every shipped personal lens must be measured, "+
			"so extend deltaFixtureVertices/deltaFixtureLinks rather than accepting the gap", unseeded)
	require.GreaterOrEqualf(t, len(f.lenses), len(deltaExactnessLensNames),
		"the census floor: %d lenses measured", len(f.lenses))

	// The second floor, and the one that gives the census its teeth: on a
	// ONE-row actor a scoped publish and a whole-actor publish are the same
	// message set, so a lens seeded with a single row can be measured but
	// proves nothing about scoping. Every lens but edgeIdentity — whose
	// `manifest.me` row is one per actor by construction — must hold a sibling
	// row the mutation is entitled to leave alone.
	for _, l := range f.lenses {
		if l.name == "edgeIdentity" {
			require.Lenf(t, baseline[l.name], 1, "%s projects exactly one row per actor", l.name)
			continue
		}
		require.Greaterf(t, len(baseline[l.name]), 1,
			"%s seeds only %d row(s) — extend the fixture, or this lens's withheld count measures nothing",
			l.name, len(baseline[l.name]))
	}

	var violations []deltaViolation

	// check diffs one mutation's after-state against `before` and records every
	// row that appeared or changed without being admitted.
	check := func(label string, before map[string]map[string]string, m map[string]deltaMeasurement) {
		for _, l := range f.lenses {
			row := report[l.name]
			row.mutations++
			got := m[l.name]
			for key, after := range got.rows {
				if _, admitted := got.admitted[key]; !admitted {
					row.withheld++
				}
				prev, existed := before[l.name][key]
				if existed && prev == after {
					continue
				}
				if _, admitted := got.admitted[key]; admitted {
					continue
				}
				violations = append(violations, deltaViolation{
					lens: l.name, mutation: label, rowKey: key, appeared: !existed,
				})
				row.violations++
			}
		}
	}
	after := func(m map[string]deltaMeasurement) map[string]map[string]string {
		out := make(map[string]map[string]string, len(m))
		for name, meas := range m {
			out[name] = meas.rows
		}
		return out
	}

	// --- vertex-keyed mutations: body, aspect, tombstone ---------------------
	for _, v := range deltaFixtureVertices {
		scope := []string{v.key()}

		f.writeVertexState(t, v, v.dataAlt, false)
		check("vertex-body:"+v.seed, baseline, f.measure(t, scope))
		f.writeVertexState(t, v, v.data, false)

		emWriteAspect(t, f.h.ctx, f.h.coreKV, v.key(), v.mutAspect, v.class+"."+v.mutAspect, v.aspectAlt)
		check("aspect:"+v.seed+"."+v.mutAspect, baseline, f.measure(t, scope))
		emWriteAspect(t, f.h.ctx, f.h.coreKV, v.key(), v.mutAspect, v.class+"."+v.mutAspect, v.aspects[v.mutAspect])

		f.writeVertexState(t, v, v.data, true)
		check("vertex-tombstone:"+v.seed, baseline, f.measure(t, scope))
		f.writeVertexState(t, v, v.data, false)

		// Fixture integrity, once per vertex: every mutation above is diffed
		// against the SAME baseline, so a revert that did not take would show
		// up as a stream of spurious violations on later vertices rather than
		// as the fixture bug it is.
		restored := f.measure(t, []string{absent})
		for _, l := range f.lenses {
			require.Equalf(t, baseline[l.name], restored[l.name].rows,
				"%s: reverting %s's mutations must restore the baseline row set", l.name, v.seed)
		}
	}

	// --- link-keyed mutations: tombstone, then create ------------------------
	//
	// Run as a pair on the fixture's own edges rather than on a synthetic
	// relation: a tombstone here removes a hop a shipped walk depends on, and
	// the re-create that restores it IS the link-create vector. The create's
	// own after-state is asserted equal to the baseline, which gives the
	// integrity check for free.
	for _, l := range deltaFixtureLinks {
		scope := l.endpoints()

		f.writeLinkState(t, l, false)
		tomb := f.measure(t, scope)
		check("link-tombstone:"+l.rel+":"+l.srcSeed+"->"+l.dstSeed, baseline, tomb)

		f.writeLinkState(t, l, true)
		created := f.measure(t, scope)
		check("link-create:"+l.rel+":"+l.srcSeed+"->"+l.dstSeed, after(tomb), created)
		for _, lens := range f.lenses {
			require.Equalf(t, baseline[lens.name], created[lens.name].rows,
				"%s: re-creating link %s must restore the baseline row set", lens.name, l.key())
		}
	}

	// --- the report ---------------------------------------------------------
	names := make([]string, 0, len(report))
	for name := range report {
		names = append(names, name)
	}
	sort.Strings(names)
	t.Logf("T4 differential exactness census — %d lenses, %d fixture vertices, %d fixture links",
		len(f.lenses), len(deltaFixtureVertices), len(deltaFixtureLinks))
	t.Logf("%-22s %8s %6s %10s %9s %11s", "lens", "branches", "rows", "mutations", "withheld", "violations")
	for _, name := range names {
		r := report[name]
		t.Logf("%-22s %8d %6d %10d %9d %11d", name, r.branches, r.seeded, r.mutations, r.withheld, r.violations)
	}

	// The positive control. Every assertion above is a negative — "no row was
	// wrongly withheld" — and a scope that admitted everything would satisfy
	// all of them while publishing exactly what the design exists to stop
	// publishing. A lens must therefore have WITHHELD something somewhere: its
	// rows' provenance must actually be narrower than the whole graph.
	for _, name := range names {
		assert.Positivef(t, report[name].withheld,
			"%s never withheld a single row across %d mutations — its rows either carry no provenance at all "+
				"(PublishScope.Admits fails open on an empty set) or every row reads every vertex, and in "+
				"either case this lens's rows are not being scoped in production",
			name, report[name].mutations)
	}

	for _, v := range violations {
		kind := "content changed"
		if v.appeared {
			kind = "row appeared"
		}
		t.Errorf("SOUNDNESS: %s withheld a row it had to publish — mutation %s, row %s (%s). "+
			"personal-lens-delta-publication-design.md §4.1's invariant does not hold for this lens: "+
			"the device keeps a stale copy until the healer's daily content cycle.",
			v.lens, v.mutation, v.rowKey, kind)
	}
}

// TestPersonalDeltaExactness_WalkOwnedColumnSurvives is §4.1's fourth
// invariant case, the adversarial pass's own counterexample, as its own vector.
//
// edgeCatalog reaches one op meta by three walks. The staff walk
// (identity)-[:holdsRole]->(role)<-[:grantedBy]-(perm)-[:forOperation]->(op)
// is the only one that binds `role`, so `viaRole`/`viaRoleName` are WALK-OWNED
// columns on the merged row. Tombstone `grantedBy` and the staff branch yields
// no row for that op at all: the base and task branches still produce it, with
// viaRole null. Nothing that branch BOUND is in any surviving row's own
// provenance — the vertex that made it stop matching is one it read and did not
// bind — so without mergedProvenance's "a branch that produced NO row for the
// key contributes its whole read set" half, this row looks untouched and the
// device keeps a role name that was revoked.
//
// Asserted on the value AND on the publication: the column must actually go
// null (otherwise the vector proves nothing about walk-owned merging), and the
// surviving row must be admitted by the scope the link arm builds.
func TestPersonalDeltaExactness_WalkOwnedColumnSurvives(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the delta-exactness walk-owned vector in -short mode")
	}
	f := newDeltaExactnessFixture(t)

	var catalog *deltaExactnessLens
	for _, l := range f.lenses {
		if l.name == "edgeCatalog" {
			catalog = l
		}
	}
	require.NotNil(t, catalog, "edgeCatalog must be under census")
	require.Equal(t, 3, catalog.branches, "edgeCatalog must compose to its three walks")

	grantedBy := deltaFixtureLink{"permission", "perm", "grantedBy", "role", "role"}
	opKey := deltaFixtureVertex{typ: "meta", seed: "op"}.key()

	absent := substrate.VertexKey("identity", pl2NanoID("t4delta-nobody"))
	before := f.measure(t, []string{absent})[catalog.name]
	require.Greater(t, len(before.rows), 1,
		"the fixture must reach more than one op meta, or a scoped publish and a whole-actor one are the same message set")

	// The row under test is the one op ALL THREE walks reach. Its sibling (op2)
	// is reached by the staff walk alone, which is what makes the fixture's two
	// edgeCatalog rows structurally different rather than merely two of a kind.
	var rowKey string
	for k, v := range before.rows {
		if strings.Contains(v, `"opMetaKey":"`+opKey+`"`) {
			rowKey = k
		}
	}
	require.NotEmptyf(t, rowKey, "the merged row for %s must be projected", opKey)
	require.Containsf(t, before.rows[rowKey], `"viaRole":"`+substrate.VertexKey("role", deltaFixtureVertex{typ: "role", seed: "role"}.id())+`"`,
		"the staff walk must own a non-null viaRole before the tombstone, or this vector tests nothing")

	f.writeLinkState(t, grantedBy, false)
	after := f.measure(t, grantedBy.endpoints())[catalog.name]

	require.Contains(t, after.rows, rowKey,
		"the base and task walks still reach %s, so the merged row survives", opKey)
	require.Contains(t, after.rows[rowKey], `"viaRole":null`,
		"the staff walk stopped producing this key, so its walk-owned column must null out")
	require.Contains(t, after.admitted, rowKey,
		"the surviving row's content CHANGED, so a publish scoped to the tombstoned link's endpoints "+
			"must admit it — mergedProvenance's no-row-for-this-key branch is what carries the "+
			"vertices the staff walk read and did not bind")
}
