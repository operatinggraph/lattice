package projection

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- Output descriptor parsing / validation (AC4) ---

func validDescriptor() *lens.OutputDescriptorSpec {
	return &lens.OutputDescriptorSpec{
		AnchorType:       "identity",
		OutputKeyPattern: "cap.ephemeral.{actorSuffix}",
		BodyColumns:      []string{"ephemeralGrants"},
		EmptyBehavior:    "delete",
		RealnessFilter:   "taskKey",
		Freshness:        "auto",
	}
}

func TestParseOutputDescriptor_Valid(t *testing.T) {
	d, err := ParseOutputDescriptor(validDescriptor())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.AnchorType != "identity" || d.EmptyBehavior != EmptyDelete {
		t.Fatalf("descriptor mismatch: %+v", d)
	}
	if got := d.BuildKey("vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"); got != "cap.ephemeral.identity.Hj4kPmRtw9nbCxz5vQ2y" {
		t.Fatalf("BuildKey: got %q", got)
	}
}

func TestParseOutputDescriptor_RejectsUnknownPlaceholder(t *testing.T) {
	spec := validDescriptor()
	spec.OutputKeyPattern = "cap.ephemeral.{actorSuffix}.{lensId}"
	_, err := ParseOutputDescriptor(spec)
	if err == nil || !strings.Contains(err.Error(), "unknown placeholder") {
		t.Fatalf("expected unknown-placeholder rejection, got %v", err)
	}
}

func TestParseOutputDescriptor_RejectsMissingActorSuffix(t *testing.T) {
	spec := validDescriptor()
	spec.OutputKeyPattern = "cap.ephemeral.static"
	_, err := ParseOutputDescriptor(spec)
	if err == nil || !strings.Contains(err.Error(), "actorSuffix") {
		t.Fatalf("expected missing-actorSuffix rejection, got %v", err)
	}
}

func TestParseOutputDescriptor_RejectsBadEmptyBehavior(t *testing.T) {
	spec := validDescriptor()
	spec.EmptyBehavior = "purge"
	_, err := ParseOutputDescriptor(spec)
	if err == nil || !strings.Contains(err.Error(), "emptyBehavior") {
		t.Fatalf("expected emptyBehavior rejection, got %v", err)
	}
}

func TestParseOutputDescriptor_RejectsBadFreshness(t *testing.T) {
	spec := validDescriptor()
	spec.Freshness = "manual"
	_, err := ParseOutputDescriptor(spec)
	if err == nil || !strings.Contains(err.Error(), "freshness") {
		t.Fatalf("expected freshness rejection, got %v", err)
	}
}

func TestParseOutputDescriptor_RejectsEmptyBodyColumns(t *testing.T) {
	spec := validDescriptor()
	spec.BodyColumns = nil
	_, err := ParseOutputDescriptor(spec)
	if err == nil || !strings.Contains(err.Error(), "bodyColumns") {
		t.Fatalf("expected bodyColumns rejection, got %v", err)
	}
}

func TestParseOutputDescriptor_NilSpec(t *testing.T) {
	_, err := ParseOutputDescriptor(nil)
	if err == nil {
		t.Fatalf("expected error for nil descriptor")
	}
}

// --- keyColumn: actorKey-derived bare-NanoID key (§10.2 Option b) ---

// A keyColumn descriptor emits the anchor's BARE NanoID into the {actorSuffix}
// slot, so the projected weaver-targets row key is <targetId>.<bareNanoID> (one
// dot after the targetId) — the shape splitRowKey accepts.
func TestBuildKey_KeyColumn_EmitsBareNanoID(t *testing.T) {
	d := OutputDescriptor{
		OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}",
		KeyColumn:        "entityId",
	}
	// The anchor TYPE segment is lowercase per Contract #1 (isValidTypeSegment:
	// [a-z][a-z0-9]*); the targetId prefix in the pattern is a free key token.
	got := d.BuildKey("vtx.leaseapp.Lk2Pn6mQrtwzKbcXvP3T")
	const want = "leaseApplicationComplete.Lk2Pn6mQrtwzKbcXvP3T"
	if got != want {
		t.Fatalf("keyColumn BuildKey: got %q, want %q (bare NanoID after one dot)", got, want)
	}
	if strings.Count(got, ".") != 1 {
		t.Fatalf("keyColumn key must have exactly one dot after the targetId: %q", got)
	}
}

// The default (no keyColumn) BuildKey path is byte-for-byte the pre-14.2
// behavior: {actorSuffix} = <type>.<id> (vtx-stripped). This is the AC #2
// regression pin at the unit layer.
func TestBuildKey_DefaultSuffix_Unchanged(t *testing.T) {
	d := OutputDescriptor{
		OutputKeyPattern: "cap.ephemeral.{actorSuffix}",
	}
	got := d.BuildKey("vtx.identity.Hj4kPmRtw9nbCxz5vQ2y")
	const want = "cap.ephemeral.identity.Hj4kPmRtw9nbCxz5vQ2y"
	if got != want {
		t.Fatalf("default BuildKey changed: got %q, want %q", got, want)
	}
	// A descriptor parsed without keyColumn defaults the field empty.
	parsed, err := ParseOutputDescriptor(validDescriptor())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.KeyColumn != "" {
		t.Fatalf("KeyColumn must default empty, got %q", parsed.KeyColumn)
	}
}

// TestKeyColumn_SplitRowKeyRoundTrips closes the §4 loop: the keyColumn-projected
// key satisfies the exact predicate Weaver's splitRowKey applies
// (internal/weaver/evaluator.go:514 — IsValidNanoID on the tail after the first
// dot), while the DEFAULT {actorSuffix}=<type>.<id> key FAILS it — demonstrating
// why Option (b) is needed (the M2 defect) and that 14.2 fixes it. splitRowKey is
// unexported in internal/weaver, so this asserts the predicate directly; the
// weaver-package test TestSplitRowKey_AcceptsKeyColumnProjectedKey calls the real
// function.
func TestKeyColumn_SplitRowKeyRoundTrips(t *testing.T) {
	const actorKey = "vtx.leaseapp.Lk2Pn6mQrtwzKbcXvP3T"

	keyCol := OutputDescriptor{OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}", KeyColumn: "entityId"}
	projected := keyCol.BuildKey(actorKey)
	tail := projected[strings.IndexByte(projected, '.')+1:]
	if !substrate.IsValidNanoID(tail) {
		t.Fatalf("keyColumn key tail %q must be a bare NanoID (splitRowKey predicate)", tail)
	}

	def := OutputDescriptor{OutputKeyPattern: "leaseApplicationComplete.{actorSuffix}"}
	defaultKey := def.BuildKey(actorKey)
	defaultTail := defaultKey[strings.IndexByte(defaultKey, '.')+1:]
	if substrate.IsValidNanoID(defaultTail) {
		t.Fatalf("default <type>.<id> key tail %q must FAIL the NanoID predicate (the M2 defect)", defaultTail)
	}
}

func TestParseOutputDescriptor_KeyColumn_AcceptAndReject(t *testing.T) {
	// Accept: a non-empty keyColumn parses and is carried onto the descriptor.
	accept := validDescriptor()
	accept.KeyColumn = "entityId"
	d, err := ParseOutputDescriptor(accept)
	if err != nil {
		t.Fatalf("keyColumn must be accepted: %v", err)
	}
	if d.KeyColumn != "entityId" {
		t.Fatalf("KeyColumn not carried: got %q", d.KeyColumn)
	}

	// Reject: a whitespace-only keyColumn is fail-closed.
	reject := validDescriptor()
	reject.KeyColumn = "   "
	if _, err := ParseOutputDescriptor(reject); err == nil || !strings.Contains(err.Error(), "keyColumn") {
		t.Fatalf("expected whitespace-only keyColumn rejection, got %v", err)
	}

	// The {actorSuffix}-required rule still fires when keyColumn is set: the
	// placeholder is the substitution point keyColumn fills, not a relaxation.
	noSuffix := validDescriptor()
	noSuffix.KeyColumn = "entityId"
	noSuffix.OutputKeyPattern = "leaseApplicationComplete.static"
	if _, err := ParseOutputDescriptor(noSuffix); err == nil || !strings.Contains(err.Error(), "actorSuffix") {
		t.Fatalf("expected missing-actorSuffix rejection even with keyColumn set, got %v", err)
	}
}

// --- realness filter (AC4) ---

func TestRealnessFiltered(t *testing.T) {
	d := OutputDescriptor{RealnessFilter: "taskKey"}
	in := []any{
		map[string]any{"taskKey": "vtx.task.x", "v": 1},
		map[string]any{"taskKey": nil},    // degenerate
		map[string]any{"taskKey": ""},     // degenerate
		map[string]any{"other": "no key"}, // degenerate
	}
	out := d.RealnessFiltered(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 real entry, got %d: %v", len(out), out)
	}
}

func TestRealnessFiltered_NoFilterPassesThrough(t *testing.T) {
	d := OutputDescriptor{}
	in := []any{map[string]any{"a": 1}, map[string]any{"b": 2}}
	if got := d.RealnessFiltered(in); len(got) != 2 {
		t.Fatalf("expected pass-through, got %d", len(got))
	}
}

// A non-string value at the realness field must NOT silently zero the
// projection (over-revocation). A present non-nil value is treated as real and
// kept; only nil / missing / empty / whitespace-only strings drop the entry.
func TestRealnessFiltered_NonStringFieldKept(t *testing.T) {
	d := OutputDescriptor{RealnessFilter: "taskKey"}
	in := []any{
		map[string]any{"taskKey": float64(42)},       // non-string but present → real
		map[string]any{"taskKey": true},              // non-string but present → real
		map[string]any{"taskKey": "vtx.task.x"},      // string non-empty → real
		map[string]any{"taskKey": nil},               // degenerate → dropped
		map[string]any{"taskKey": "   "},             // whitespace string → dropped
		map[string]any{"other": "no realness field"}, // missing → dropped
	}
	out := d.RealnessFiltered(in)
	if len(out) != 3 {
		t.Fatalf("expected 3 real entries (non-string kept, degenerate dropped), got %d: %v", len(out), out)
	}
}

// --- empty behavior → action + tombstone reuse signal (AC7) ---

func TestEmptyAction_Mapping(t *testing.T) {
	cases := map[EmptyBehavior]EmptyAction{
		EmptyDelete:     ActionDelete,
		EmptySoftDelete: ActionSoftDelete,
		EmptyDoc:        ActionWriteEmptyDoc,
		EmptySkip:       ActionSkip,
	}
	for eb, want := range cases {
		got := OutputDescriptor{EmptyBehavior: eb}.EmptyAction()
		if got != want {
			t.Fatalf("emptyBehavior %q → action %v, want %v", eb, got, want)
		}
	}
}

func TestRequiresGuardedTombstone(t *testing.T) {
	if !(OutputDescriptor{EmptyBehavior: EmptySoftDelete}).RequiresGuardedTombstone() {
		t.Fatalf("softDelete must require the guarded tombstone")
	}
	if !(OutputDescriptor{EmptyBehavior: EmptyDelete}).RequiresGuardedTombstone() {
		t.Fatalf("delete on a guarded adapter reuses the same tombstone mechanism")
	}
	if (OutputDescriptor{EmptyBehavior: EmptySkip}).RequiresGuardedTombstone() {
		t.Fatalf("skip must NOT write a tombstone")
	}
}

// --- activation: Compile registers an actor-aggregate lens regardless of MATCH
// shape; fan-out is always the broad BFS enumerator (no forest/coverage gate) ---

// uncoveredAuthRule builds an actorAggregate auth-plane (capability-kv) rule
// whose MATCH uses an undirected hop.
func makeRule(t *testing.T, bucket, match string) *lens.Rule {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(match)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return &lens.Rule{
		ID:             "lens-test",
		CanonicalName:  "testLens",
		Match:          match,
		ProjectionKind: ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into:           lens.IntoConfig{Target: "nats_kv", Bucket: bucket, Key: lens.KeyField{"key"}},
		Output:         validDescriptor(),
	}
}

const coveredMatch = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (task)-[:forOperation]->(op)
RETURN identity.key AS actorKey, collect(task.key) AS tasks
`

const uncoveredMatch = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:assignedTo]-(task:task)
RETURN identity.key AS actorKey, collect(task.key) AS tasks
`

// TestCompile_AuthPlaneUncoveredMatch_StillRegisters pins the retire-simple-
// engine Fire 2 invariant: the deleted invalidation-forest coverage analyzer
// was already inert at the driver level (BFS always wins — see driver.go), so
// removing it must not start refusing an auth-plane lens whose MATCH the old
// analyzer would have called "uncovered". Compile has no forest/coverage
// concept anymore; it only validates the descriptor and classifies auth-plane.
func TestCompile_AuthPlaneUncoveredMatch_StillRegisters(t *testing.T) {
	r := makeRule(t, AuthPlaneBucket, uncoveredMatch)
	plan, err := Compile(r)
	if err != nil {
		t.Fatalf("auth-plane lens must still compile (BFS fan-out, no forest gate): %v", err)
	}
	if !plan.AuthPlane {
		t.Fatalf("expected auth-plane classification for capability-kv bucket")
	}
}

func TestCompile_NonActorAggregate_Rejected(t *testing.T) {
	r := makeRule(t, "my-tasks", coveredMatch)
	r.ProjectionKind = ""
	if _, err := Compile(r); err == nil {
		t.Fatalf("expected Compile to reject a non-actorAggregate lens")
	}
}

func TestIsAuthPlane(t *testing.T) {
	if !IsAuthPlane(&lens.Rule{Into: lens.IntoConfig{Target: "nats_kv", Bucket: AuthPlaneBucket}}) {
		t.Fatalf("capability-kv bucket must classify as auth-plane")
	}
	if IsAuthPlane(&lens.Rule{Into: lens.IntoConfig{Target: "nats_kv", Bucket: "my-tasks"}}) {
		t.Fatalf("my-tasks bucket must NOT be auth-plane")
	}
	// A grant-table lens (Contract #6 §6.14) writes actor_read_grants, the
	// read-auth source of truth every protected table's RLS policy consults —
	// its pause must alert at the same severity as a paused capability-kv lens.
	if !IsAuthPlane(&lens.Rule{Into: lens.IntoConfig{Target: "postgres", GrantTable: true}}) {
		t.Fatalf("a grant-table postgres lens must classify as auth-plane")
	}
	// An ordinary protected business lens is NOT itself the read-auth source of
	// truth — RLS enforcement is Postgres-native and independent of this lens's
	// own freshness, so it stays at the generic business-lens severity tier.
	if IsAuthPlane(&lens.Rule{Into: lens.IntoConfig{Target: "postgres", Protected: true}}) {
		t.Fatalf("an ordinary protected (non-grant-table) postgres lens must NOT be auth-plane")
	}
}

// --- contributing-source provenance widening (AC5) ---

func TestContributingSources_WidensToBoundGraphKeys(t *testing.T) {
	actor := "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
	lensDef := "vtx.meta.Lk2Pn6mQrtwzKbcXvP3T"
	rows := []map[string]any{
		{
			"actorKey": actor,
			"openTasks": []any{
				map[string]any{
					"taskKey":      "vtx.task.Rm7q3pntwzkfbcxv5p9j",
					"forOperation": "vtx.op.Qp4Nb2mPq6rTwzKxVyP7",
					"scopedTo":     "vtx.lease.Zz9q3pntwzkfbcxv5p9k",
				},
			},
		},
	}
	revs := map[string]uint64{
		actor:                            47,
		lensDef:                          12,
		"vtx.task.Rm7q3pntwzkfbcxv5p9j":  8,
		"vtx.op.Qp4Nb2mPq6rTwzKxVyP7":    3,
		"vtx.lease.Zz9q3pntwzkfbcxv5p9k": 5,
	}
	got := ContributingSources(actor, lensDef, rows, func(k string) uint64 { return revs[k] })

	// v1 must include actor + lens-def + every bound task/op/scopedTo key.
	for k := range revs {
		if _, ok := got[k]; !ok {
			t.Fatalf("contributing-source set missing bound key %q: %v", k, got)
		}
	}
	if got[actor] != 47 || got[lensDef] != 12 {
		t.Fatalf("revisions not stamped from revisionOf: %v", got)
	}
}

func TestContributingSources_OmitsAbsentRevisions(t *testing.T) {
	actor := "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
	got := ContributingSources(actor, "", nil, func(string) uint64 { return 0 })
	if len(got) != 0 {
		t.Fatalf("expected no entries when revisionOf returns 0, got %v", got)
	}
}
