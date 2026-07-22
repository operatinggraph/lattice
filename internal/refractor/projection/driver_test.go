package projection_test

import (
	"errors"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

func ephemeralDesc(t *testing.T) projection.OutputDescriptor {
	t.Helper()
	d, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: "cap.ephemeral.{actorSuffix}",
		BodyColumns:      []string{"ephemeralGrants"},
		EmptyBehavior:    "delete",
		RealnessFilter:   "taskKey",
		Freshness:        "auto",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

func capDesc(t *testing.T) projection.OutputDescriptor {
	t.Helper()
	d, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:         "identity",
		OutputKeyPattern:   "cap.{actorSuffix}",
		BodyColumns:        []string{"platformPermissions", "serviceAccess", "roles"},
		EmptyBehavior:      "delete",
		Freshness:          "auto",
		Lanes:              []string{"default"},
		StaticEmptyColumns: []string{"ephemeralGrants"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

func myTasksDesc(t *testing.T) projection.OutputDescriptor {
	t.Helper()
	d, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: "my-tasks.{actorSuffix}",
		BodyColumns:      []string{"openTasks"},
		EmptyBehavior:    "delete",
		RealnessFilter:   "taskKey",
		Freshness:        "auto",
		ActorField:       "assignee",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

const actor = "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"

// TestDriver_PrimaryCapability_Shape asserts the primary cap.<actor> envelope
// carries the §6.2 shape: lanes, the always-empty ephemeralGrants, the three
// body columns, and actor (not assignee). It never deletes on an empty body
// (no realness filter).
func TestDriver_PrimaryCapability_Shape(t *testing.T) {
	fn := capDesc(t).EnvelopeFn("vtx.meta.cap", func(string) uint64 { return 9 })
	row := map[string]any{
		"actorKey":            actor,
		"platformPermissions": []any{map[string]any{"operationType": "read", "scope": "any"}},
		"serviceAccess":       []any{},
		"roles":               []any{"vtx.role.r1"},
	}
	env, keys, err := fn(row, nil, map[string]any{"projectedAt": "2026-05-15T10:00:00Z"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if env["key"] != "cap.identity.Hj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("key: %v", env["key"])
	}
	if keys["key"] != env["key"] {
		t.Fatalf("keys mirror: %v", keys)
	}
	if env["actor"] != actor {
		t.Fatalf("actor: %v", env["actor"])
	}
	if env["version"] != "1.0" {
		t.Fatalf("version: %v", env["version"])
	}
	lanes, ok := env["lanes"].([]string)
	if !ok || len(lanes) != 1 || lanes[0] != "default" {
		t.Fatalf("lanes: %v", env["lanes"])
	}
	eg, ok := env["ephemeralGrants"].([]any)
	if !ok || len(eg) != 0 {
		t.Fatalf("ephemeralGrants must be an always-empty array; got %v", env["ephemeralGrants"])
	}
	if _, ok := env["platformPermissions"]; !ok {
		t.Fatalf("platformPermissions missing")
	}
	revs, ok := env["projectedFromRevisions"].(map[string]uint64)
	if !ok {
		t.Fatalf("projectedFromRevisions type: %T", env["projectedFromRevisions"])
	}
	if revs[actor] == 0 || revs["vtx.meta.cap"] == 0 {
		t.Fatalf("projectedFromRevisions must include anchor + lens-def: %v", revs)
	}
}

// TestDriver_Ephemeral_RealGrant_Projects asserts a real grant projects with the
// cap.ephemeral.<actor> key and the ephemeralGrants body, actor field, no lanes.
func TestDriver_Ephemeral_RealGrant_Projects(t *testing.T) {
	fn := ephemeralDesc(t).EnvelopeFn("vtx.meta.eph", func(string) uint64 { return 0 })
	row := map[string]any{
		"actorKey": actor,
		"ephemeralGrants": []any{
			map[string]any{"taskKey": "vtx.task.t1", "operationType": "Approve"},
		},
	}
	env, keys, err := fn(row, nil, map[string]any{"projectedAt": "t"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if env["key"] != "cap.ephemeral.identity.Hj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("key: %v", env["key"])
	}
	if keys["key"] != env["key"] {
		t.Fatalf("keys mirror")
	}
	if env["actor"] != actor {
		t.Fatalf("actor field: %v", env["actor"])
	}
	if _, hasLanes := env["lanes"]; hasLanes {
		t.Fatalf("ephemeral doc must not carry lanes")
	}
	if _, hasEph := env["ephemeralGrants"].([]any); !hasEph {
		t.Fatalf("ephemeralGrants missing")
	}
}

// TestDriver_Ephemeral_NoRealGrants_Deletes asserts a degenerate null-taskKey
// collect (no real grants) drives ErrDeleteProjection keyed at the actor's key.
func TestDriver_Ephemeral_NoRealGrants_Deletes(t *testing.T) {
	fn := ephemeralDesc(t).EnvelopeFn("vtx.meta.eph", func(string) uint64 { return 0 })
	row := map[string]any{
		"actorKey":        actor,
		"ephemeralGrants": []any{map[string]any{"taskKey": nil}},
	}
	_, keys, err := fn(row, nil, map[string]any{"projectedAt": "t"})
	if !errors.Is(err, pipeline.ErrDeleteProjection) {
		t.Fatalf("expected ErrDeleteProjection, got %v", err)
	}
	if keys["key"] != "cap.ephemeral.identity.Hj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("delete key: %v", keys["key"])
	}
}

// TestDriver_MyTasks_NullRowActor_FallsBackToParams asserts the my-tasks lens's
// last-task-closed path: a null row actorKey falls back to params["actorKey"] so
// the key is deleted (not skipped).
func TestDriver_MyTasks_NullRowActor_FallsBackToParams(t *testing.T) {
	fn := myTasksDesc(t).EnvelopeFn("vtx.meta.mt", func(string) uint64 { return 0 })
	row := map[string]any{
		"actorKey":  nil,
		"openTasks": []any{map[string]any{"taskKey": nil}},
	}
	_, keys, err := fn(row, nil, map[string]any{"actorKey": actor, "projectedAt": "t"})
	if !errors.Is(err, pipeline.ErrDeleteProjection) {
		t.Fatalf("expected ErrDeleteProjection, got %v", err)
	}
	if keys["key"] != "my-tasks.identity.Hj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("delete key: %v", keys["key"])
	}
}

// TestDriver_MyTasks_OpenTask_Projects asserts the assignee field + my-tasks key.
func TestDriver_MyTasks_OpenTask_Projects(t *testing.T) {
	fn := myTasksDesc(t).EnvelopeFn("vtx.meta.mt", func(string) uint64 { return 0 })
	row := map[string]any{
		"actorKey":  actor,
		"openTasks": []any{map[string]any{"taskKey": "vtx.task.t1"}},
	}
	env, _, err := fn(row, nil, map[string]any{"actorKey": actor, "projectedAt": "t"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if env["assignee"] != actor {
		t.Fatalf("my-tasks doc must carry assignee: %v", env)
	}
	if _, hasActor := env["actor"]; hasActor {
		t.Fatalf("my-tasks doc must not carry actor (uses assignee)")
	}
}

// TestDriver_SkipsNonIdentityAnchor asserts a non-identity anchor is declined.
func TestDriver_SkipsNonIdentityAnchor(t *testing.T) {
	fn := ephemeralDesc(t).EnvelopeFn("vtx.meta.eph", func(string) uint64 { return 0 })
	row := map[string]any{"actorKey": "vtx.role.Hj4kPmRtw9nbCxz5vQ2y", "ephemeralGrants": []any{}}
	_, _, err := fn(row, nil, map[string]any{"projectedAt": "t"})
	if !errors.Is(err, pipeline.ErrSkipProjection) {
		t.Fatalf("expected ErrSkipProjection, got %v", err)
	}
}

// --- scalar passthrough (CAR E6): a convergence lens's scalar body columns ---

const leaseActor = "vtx.leaseapp.Lk2Pn6mQrtwzKbcXvP3T"

// convergenceDesc mirrors the shipped leaseApplicationComplete descriptor: an
// actorAggregate with an explicit keyColumn, scalar body columns (the §10.2
// violating / missing_* bools + entityKey / applicant strings), and NO realness
// filter (retraction rides anchor-disappearance, not anyReal).
func convergenceDesc(t *testing.T) projection.OutputDescriptor {
	t.Helper()
	d, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       "leaseapp",
		OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}",
		BodyColumns:      []string{"violating", "missing_signature", "applicant", "entityKey"},
		EmptyBehavior:    "delete",
		KeyColumn:        "entityId",
		Freshness:        "auto",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return d
}

// TestDriver_ScalarBodyColumns_ProjectVerbatim is the core E6 assertion: a bool
// projects as a Go bool (not [true] or []), a string projects as the string, and
// a nil scalar projects as a genuine null (absent value), never as [].
func TestDriver_ScalarBodyColumns_ProjectVerbatim(t *testing.T) {
	fn := convergenceDesc(t).EnvelopeFn("vtx.meta.conv", func(string) uint64 { return 0 })
	row := map[string]any{
		"actorKey":          leaseActor,
		"violating":         true,
		"missing_signature": false,
		"applicant":         "vtx.identity.Aj4kPmRtw9nbCxz5vQ2y",
		"entityKey":         leaseActor,
	}
	env, keys, err := fn(row, nil, map[string]any{"projectedAt": "t"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	// The bare-NanoID convergence key (keyColumn / Weaver splitRowKey shape).
	if env["key"] != "leaseApplicationComplete.Lk2Pn6mQrtwzKbcXvP3T" {
		t.Fatalf("key: %v", env["key"])
	}
	if keys["key"] != env["key"] {
		t.Fatalf("keys mirror: %v", keys)
	}
	// A bool projects as a Go bool — exactly what Weaver's boolColumn reads.
	if v, ok := env["violating"].(bool); !ok || v != true {
		t.Fatalf("violating must project as the Go bool true, got %T %v", env["violating"], env["violating"])
	}
	if v, ok := env["missing_signature"].(bool); !ok || v != false {
		t.Fatalf("missing_signature must project as the Go bool false, got %T %v", env["missing_signature"], env["missing_signature"])
	}
	// A string projects verbatim — the §10.8 row.<col> param resolution reads it.
	if env["applicant"] != "vtx.identity.Aj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("applicant must project verbatim, got %T %v", env["applicant"], env["applicant"])
	}
	if env["entityKey"] != leaseActor {
		t.Fatalf("entityKey must project verbatim, got %v", env["entityKey"])
	}
	// No body column may be coerced to a list.
	for _, col := range []string{"violating", "missing_signature", "applicant", "entityKey"} {
		if _, isList := env[col].([]any); isList {
			t.Fatalf("scalar column %q must NOT project as a list", col)
		}
	}
}

// TestDriver_NilScalar_ProjectsNullNotEmptyList asserts a nil scalar RETURN value
// projects as a genuine null (present key, nil value — so a downstream bool reads
// false and a string param reads absent), never as [].
func TestDriver_NilScalar_ProjectsNullNotEmptyList(t *testing.T) {
	fn := convergenceDesc(t).EnvelopeFn("vtx.meta.conv", func(string) uint64 { return 0 })
	row := map[string]any{
		"actorKey":          leaseActor,
		"violating":         true,
		"missing_signature": true,
		"applicant":         nil, // a null scalar (e.g. no applicant bound)
		"entityKey":         leaseActor,
	}
	env, _, err := fn(row, nil, map[string]any{"projectedAt": "t"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	v, present := env["applicant"]
	if !present {
		t.Fatalf("a nil scalar must still be present as a null field")
	}
	if v != nil {
		t.Fatalf("a nil scalar must project as null, got %T %v", v, v)
	}
	if _, isList := v.([]any); isList {
		t.Fatalf("a nil scalar must NOT project as an empty list")
	}
}

// TestDriver_ListBodyColumn_StillRealnessFilters is the roster regression pin: a
// list/collect body column is still realness-filtered byte-for-byte — the
// degenerate null-key collect artifact is dropped, real entries survive.
func TestDriver_ListBodyColumn_StillRealnessFilters(t *testing.T) {
	fn := ephemeralDesc(t).EnvelopeFn("vtx.meta.eph", func(string) uint64 { return 0 })
	row := map[string]any{
		"actorKey": actor,
		"ephemeralGrants": []any{
			map[string]any{"taskKey": "vtx.task.t1", "operationType": "Approve"},
			map[string]any{"taskKey": nil},               // degenerate null-collect artifact
			map[string]any{"taskKey": ""},                // degenerate empty
			map[string]any{"other": "no realness field"}, // degenerate missing
		},
	}
	env, _, err := fn(row, nil, map[string]any{"projectedAt": "t"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	eg, ok := env["ephemeralGrants"].([]any)
	if !ok {
		t.Fatalf("ephemeralGrants must stay a list, got %T", env["ephemeralGrants"])
	}
	if len(eg) != 1 {
		t.Fatalf("realness filter must drop the 3 degenerate entries, keep 1; got %d: %v", len(eg), eg)
	}
	m, _ := eg[0].(map[string]any)
	if m["taskKey"] != "vtx.task.t1" {
		t.Fatalf("the surviving entry must be the real grant, got %v", eg[0])
	}
}

// TestDriver_MixedScalarAndList_OneEnvelope asserts a lens carrying BOTH a scalar
// gap column AND a list column projects each by its own shape in one envelope.
func TestDriver_MixedScalarAndList_OneEnvelope(t *testing.T) {
	d, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: "mixed.{actorSuffix}",
		BodyColumns:      []string{"violating", "openTasks"},
		EmptyBehavior:    "delete",
		RealnessFilter:   "taskKey",
		Freshness:        "auto",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := d.EnvelopeFn("vtx.meta.mixed", func(string) uint64 { return 0 })
	row := map[string]any{
		"actorKey":  actor,
		"violating": true, // scalar → verbatim
		"openTasks": []any{ // list → realness-filtered
			map[string]any{"taskKey": "vtx.task.t1"},
			map[string]any{"taskKey": nil}, // degenerate
		},
	}
	env, _, err := fn(row, nil, map[string]any{"projectedAt": "t"})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if v, ok := env["violating"].(bool); !ok || !v {
		t.Fatalf("scalar violating must project verbatim as a bool, got %T %v", env["violating"], env["violating"])
	}
	ot, ok := env["openTasks"].([]any)
	if !ok || len(ot) != 1 {
		t.Fatalf("list openTasks must stay a realness-filtered list of 1, got %T %v", env["openTasks"], env["openTasks"])
	}
}

// TestDriver_ScalarRealnessColumn_Absent_Deletes asserts the delete/empty path for
// a convergence-style designated SCALAR realness column: when that scalar is
// absent (anchor not alive) and emptyBehavior is delete, the row retracts
// (ErrDeleteProjection) at the convergence key. A present/real scalar does NOT
// delete.
func TestDriver_ScalarRealnessColumn_Absent_Deletes(t *testing.T) {
	d, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       "leaseapp",
		OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}",
		BodyColumns:      []string{"violating", "entityKey"},
		EmptyBehavior:    "delete",
		RealnessFilter:   "entityKey", // the designated scalar realness column
		KeyColumn:        "entityId",
		Freshness:        "auto",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := d.EnvelopeFn("vtx.meta.conv", func(string) uint64 { return 0 })

	// entityKey present + real → projects (NOT deleted).
	live := map[string]any{"actorKey": leaseActor, "violating": true, "entityKey": leaseActor}
	env, _, err := fn(live, nil, map[string]any{"projectedAt": "t"})
	if err != nil {
		t.Fatalf("a present realness scalar must project, got err %v", err)
	}
	if env["entityKey"] != leaseActor {
		t.Fatalf("entityKey must project verbatim: %v", env["entityKey"])
	}

	// entityKey absent (nil) → the anchor is not alive → delete at the conv key.
	dead := map[string]any{"actorKey": leaseActor, "violating": false, "entityKey": nil}
	_, keys, derr := fn(dead, nil, map[string]any{"projectedAt": "t"})
	if !errors.Is(derr, pipeline.ErrDeleteProjection) {
		t.Fatalf("an absent realness scalar must retract via ErrDeleteProjection, got %v", derr)
	}
	if keys["key"] != "leaseApplicationComplete.Lk2Pn6mQrtwzKbcXvP3T" {
		t.Fatalf("delete must key on the bare-NanoID convergence key, got %v", keys["key"])
	}
}
