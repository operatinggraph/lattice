package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
)

// Tests for the read-drift ratchet (read_drift_guard.go): what the guard
// admits, what it rejects, and that the normalizer collapses runtime data
// without collapsing anything an author wrote.
//
// The guard's job is to FAIL a test, so these drive it through a collector in
// place of t.Errorf and assert on the findings. Every "admits" test carries its
// own non-vacuity control — a guard that never runs admits everything.

const (
	driftIDA = "Dr4kPmRtw9nbCxz5vQ2y"
	driftIDB = "Dv6mP3qBn4rT8wYxK7Vc"
)

// driftProbe is a guard wired to a collector, with a table the test controls.
type driftProbe struct {
	guard    *ReadDriftGuard
	findings []string
}

func newDriftProbe(reads, walks map[string]map[string]struct{}) *driftProbe {
	p := &driftProbe{}
	p.guard = &ReadDriftGuard{
		reads: reads,
		walks: walks,
		seen:  map[string]struct{}{},
	}
	p.guard.errorf = func(format string, args ...any) {
		p.findings = append(p.findings, fmt.Sprintf(format, args...))
	}
	return p
}

func (p *driftProbe) observe(op string, record processor.ScriptReadRecord, hint *processor.ContextHint) {
	p.guard.ObserveScriptReads(context.Background(), &processor.OperationEnvelope{
		RequestID: "req" + driftIDA, OperationType: op, Class: "x",
		ContextHint: hint,
	}, record)
}

func (p *driftProbe) assertNoFindings(t *testing.T, what string) {
	t.Helper()
	if len(p.findings) != 0 {
		t.Fatalf("%s: expected the guard to admit this, got:\n%s", what, strings.Join(p.findings, "\n"))
	}
}

func (p *driftProbe) assertOneFinding(t *testing.T, contains ...string) string {
	t.Helper()
	if len(p.findings) != 1 {
		t.Fatalf("expected exactly 1 finding, got %d:\n%s", len(p.findings), strings.Join(p.findings, "\n"))
	}
	for _, c := range contains {
		if !strings.Contains(p.findings[0], c) {
			t.Fatalf("finding does not mention %q:\n%s", c, p.findings[0])
		}
	}
	return p.findings[0]
}

// shapeSet is the per-op table shape the guard reads.
func shapeSet(op string, shapes ...string) map[string]map[string]struct{} {
	set := map[string]struct{}{}
	for _, s := range shapes {
		set[s] = struct{}{}
	}
	return map[string]map[string]struct{}{op: set}
}

// TestReadDriftGuard_BaselinedReadIsAdmitted is the positive vector: a live read
// whose normalized shape is already recorded as debt must NOT fail a test —
// that is the whole point of a ratchet, and a guard that reddened the existing
// corpus could not be armed at all.
//
// The second half is the non-vacuity control the processor dossier keeps
// catching the absence of: the SAME guard, handed a read that is not in the
// table, fires. Without it, "no findings" would be equally consistent with a
// guard that never ran.
func TestReadDriftGuard_BaselinedReadIsAdmitted(t *testing.T) {
	p := newDriftProbe(shapeSet("DriftOp", "vtx.thing.<id>.settings"), nil)
	p.observe("DriftOp", processor.ScriptReadRecord{
		LiveReads: []string{"vtx.thing." + driftIDA + ".settings"},
	}, nil)
	p.assertNoFindings(t, "a baselined read")

	p.observe("DriftOp", processor.ScriptReadRecord{
		LiveReads: []string{"vtx.thing." + driftIDA + ".unbaselined"},
	}, nil)
	p.assertOneFinding(t, "vtx.thing.<id>.unbaselined")
}

// TestReadDriftGuard_WalkFollowUpReadIsAdmitted — the class-(e) arm: a read of a
// key whose vertex root this execution's own enumeration surfaced is a
// follow-up on that walk, not a key from nowhere.
//
// Non-vacuity control: the identical read with nothing walked fires.
func TestReadDriftGuard_WalkFollowUpReadIsAdmitted(t *testing.T) {
	hub := "vtx.hub." + driftIDA
	target := "vtx.thing." + driftIDB
	hint := &processor.ContextHint{Enumerations: []processor.EnumerationHint{
		{Hub: hub, Relation: "linksTo", Direction: "out"},
	}}
	record := processor.ScriptReadRecord{
		LiveReads:          []string{target + ".detail"},
		Enumerations:       []processor.ScriptEnumeration{{Hub: hub, Relation: "linksTo", Direction: "out"}},
		EnumeratedVertices: []string{hub, target},
	}
	p := newDriftProbe(nil, nil)
	p.observe("DriftOp", record, hint)
	p.assertNoFindings(t, "a follow-up read on a declared walk")

	unwalked := record
	unwalked.EnumeratedVertices = nil
	q := newDriftProbe(nil, nil)
	q.observe("DriftOp", unwalked, hint)
	q.assertOneFinding(t, "vtx.thing.<id>.detail")
}

// TestReadDriftGuard_UndeclaredReadFires — the rejecting arm, plus the finding's
// contract: it must name the operation, the SHAPE (not the fixture key), and
// both remedies, so whoever trips it does not have to find this design first.
func TestReadDriftGuard_UndeclaredReadFires(t *testing.T) {
	p := newDriftProbe(nil, nil)
	p.observe("DriftOp", processor.ScriptReadRecord{
		LiveReads: []string{"vtx.thing." + driftIDA + ".secret"},
	}, nil)
	f := p.assertOneFinding(t, "DriftOp", "vtx.thing.<id>.secret", "contextHint", "optionalReads", "read_drift_baseline.txt")
	if strings.Contains(f, driftIDA) {
		t.Fatalf("finding leaks the raw fixture id instead of the shape:\n%s", f)
	}
}

// TestReadDriftGuard_UndeclaredWalkFires — an enumeration nobody declared is a
// finding in its own right. This is what stops a script laundering an
// undeclared read by walking to it first: the walk-follow-up arm admits the
// read, and this arm rejects the walk that admitted it.
func TestReadDriftGuard_UndeclaredWalkFires(t *testing.T) {
	hub := "vtx.hub." + driftIDA
	target := "vtx.thing." + driftIDB
	p := newDriftProbe(nil, nil)
	p.observe("DriftOp", processor.ScriptReadRecord{
		LiveReads:          []string{target + ".detail"},
		Enumerations:       []processor.ScriptEnumeration{{Hub: hub, Relation: "linksTo", Direction: "out"}},
		EnumeratedVertices: []string{hub, target},
	}, nil)
	p.assertOneFinding(t, "DriftOp", "vtx.hub.<id> linksTo out", "contextHint.enumerations")
}

// TestReadDriftGuard_DeclaredWalkIsAdmitted — a walk the envelope declared is
// admitted with no baseline row at all, which is the state every operation is
// meant to reach.
func TestReadDriftGuard_DeclaredWalkIsAdmitted(t *testing.T) {
	hub := "vtx.hub." + driftIDA
	walk := []processor.ScriptEnumeration{{Hub: hub, Relation: "linksTo", Direction: "out"}}
	p := newDriftProbe(nil, nil)
	p.observe("DriftOp", processor.ScriptReadRecord{Enumerations: walk},
		&processor.ContextHint{Enumerations: []processor.EnumerationHint{
			// A DIFFERENT hub id: the declaration is matched by shape, so a
			// runtime id can never make a declared walk look undeclared.
			{Hub: "vtx.hub." + driftIDB, Relation: "linksTo", Direction: "out"},
		}})
	p.assertNoFindings(t, "a declared walk")

	q := newDriftProbe(nil, nil)
	q.observe("DriftOp", processor.ScriptReadRecord{Enumerations: walk},
		&processor.ContextHint{Enumerations: []processor.EnumerationHint{
			{Hub: "vtx.hub." + driftIDB, Relation: "linksTo", Direction: "in"},
		}})
	q.assertOneFinding(t, "vtx.hub.<id> linksTo out")
}

// TestReadDriftGuard_BaselinedWalkIsAdmitted — the walk table's own arm.
func TestReadDriftGuard_BaselinedWalkIsAdmitted(t *testing.T) {
	hub := "vtx.hub." + driftIDA
	p := newDriftProbe(nil, shapeSet("DriftOp", "vtx.hub.<id> linksTo out"))
	p.observe("DriftOp", processor.ScriptReadRecord{
		Enumerations: []processor.ScriptEnumeration{{Hub: hub, Relation: "linksTo", Direction: "out"}},
	}, nil)
	p.assertNoFindings(t, "a baselined walk")

	p.observe("DriftOp", processor.ScriptReadRecord{
		Enumerations: []processor.ScriptEnumeration{{Hub: hub, Relation: "otherRel", Direction: "out"}},
	}, nil)
	p.assertOneFinding(t, "vtx.hub.<id> otherRel out")
}

// TestReadDriftGuard_BaselineIsPerOperation — a shape baselined for one
// operation does not excuse another. The table is per-dispatcher debt, not a
// global allowlist.
func TestReadDriftGuard_BaselineIsPerOperation(t *testing.T) {
	p := newDriftProbe(shapeSet("OtherOp", "vtx.thing.<id>.settings"), nil)
	p.observe("DriftOp", processor.ScriptReadRecord{
		LiveReads: []string{"vtx.thing." + driftIDA + ".settings"},
	}, nil)
	p.assertOneFinding(t, "DriftOp", "vtx.thing.<id>.settings")
}

// TestReadDriftGuard_DedupesRepeatedShape — one fixture submitting the same
// operation many times is one problem. Reporting it per execution would bury
// the finding in its own repetitions.
func TestReadDriftGuard_DedupesRepeatedShape(t *testing.T) {
	p := newDriftProbe(nil, nil)
	for _, id := range []string{driftIDA, driftIDB, driftIDA} {
		p.observe("DriftOp", processor.ScriptReadRecord{
			LiveReads: []string{"vtx.thing." + id + ".secret"},
		}, nil)
	}
	p.assertOneFinding(t, "vtx.thing.<id>.secret")
}

// TestReadDriftGuard_SilentAfterOwningTestFinishes — a pipeline can outlive its
// test; reporting then would panic the run instead of naming a drift.
func TestReadDriftGuard_SilentAfterOwningTestFinishes(t *testing.T) {
	p := newDriftProbe(nil, nil)
	p.guard.closed = true
	p.observe("DriftOp", processor.ScriptReadRecord{
		LiveReads: []string{"vtx.thing." + driftIDA + ".secret"},
	}, nil)
	p.assertNoFindings(t, "a finding raised after the owning test completed")
}

// TestReadDriftGuard_EmbeddedBaselineIsWired — the guard a CapabilityPipeline
// builds must carry the checked-in table, not an empty one. Without this the
// whole suite would pass with the baseline file deleted, and the ratchet would
// be silently ungrounded.
func TestReadDriftGuard_EmbeddedBaselineIsWired(t *testing.T) {
	g := NewReadDriftGuard(t)
	if len(g.reads) == 0 || len(g.walks) == 0 {
		t.Fatalf("guard carries an empty baseline: %d read ops, %d walk ops", len(g.reads), len(g.walks))
	}
	shapes := 0
	for _, s := range g.reads {
		shapes += len(s)
	}
	if shapes == 0 {
		t.Fatal("baseline has operations but no read shapes")
	}
}

// TestNormalizeReadKey covers both directions. The must-NOT-collapse vectors
// are the load-bearing half: a normalizer that eats a static prefix turns one
// baseline row into a wildcard over a whole keyspace, and every test above
// would still pass.
func TestNormalizeReadKey(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		// Collapses.
		{"vertex id", "vtx.thing." + driftIDA, "vtx.thing.<id>"},
		{"aspect on a vertex id", "vtx.thing." + driftIDA + ".settings", "vtx.thing.<id>.settings"},
		{"both link ids", "lnk.identity." + driftIDA + ".worksAt.building." + driftIDB, "lnk.identity.<id>.worksAt.building.<id>"},
		{"embedded id in localName", "vtx.patient." + driftIDA + ".activeVisitSeriesWith" + driftIDB, "vtx.patient.<id>.activeVisitSeriesWith<id>"},
		{"embedded instant in localName", "vtx.identity." + driftIDA + ".slot20260708t090000z", "vtx.identity.<id>.slot<t>"},
		{"instant without the trailing z", "vtx.identity." + driftIDA + ".slot20260708t090000", "vtx.identity.<id>.slot<t>"},

		// Must NOT collapse.
		{"type segment stays", "vtx.identity." + driftIDA, "vtx.identity.<id>"},
		{"relation stays", "lnk.identity." + driftIDA + ".holdsRole.role." + driftIDB, "lnk.identity.<id>.holdsRole.role.<id>"},
		{"static localName stays", "vtx.provider." + driftIDA + ".timeOff", "vtx.provider.<id>.timeOff"},
		{"trailing digit is not an instant", "vtx.session." + driftIDA + ".seat1", "vtx.session.<id>.seat1"},
		{"short digit run is not an instant", "vtx.session." + driftIDA + ".slot2026", "vtx.session.<id>.slot2026"},
		{"instant with no static prefix stays whole", "vtx.session." + driftIDA + ".20260708t090000z", "vtx.session.<id>.20260708t090000z"},
		// nanoid-alphabet: (reject) this id is 20 characters but carries an 'l',
		// so it is NOT a NanoID — and proving the normalizer leaves it literal is
		// the point of the vector. Collapsing on length alone would erase a real
		// distinction between a generated id and a hand-written fixture name.
		{"id-length segment off the alphabet stays", "vtx.augurproposal.BBaugurNoclHJKMNPQRS.gap", "vtx.augurproposal.BBaugurNoclHJKMNPQRS.gap"},
		{"short id stays", "vtx.thing.abc", "vtx.thing.abc"},
		{"unknown prefix is untouched", "health.processor.instance", "health.processor.instance"},
		{"short link key is untouched", "lnk." + driftIDA + ".rel." + driftIDB, "lnk." + driftIDA + ".rel." + driftIDB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeReadKey(tc.in); got != tc.want {
				t.Fatalf("NormalizeReadKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeEnumeration — the hub normalizes as a key; the relation and
// direction are the author's own words and must survive verbatim.
func TestNormalizeEnumeration(t *testing.T) {
	got := NormalizeEnumeration("vtx.identity."+driftIDA, "holdsRole", "out")
	if got != "vtx.identity.<id> holdsRole out" {
		t.Fatalf("NormalizeEnumeration = %q", got)
	}
	if a, b := NormalizeEnumeration("vtx.h."+driftIDA, "r", "out"), NormalizeEnumeration("vtx.h."+driftIDA, "r", "in"); a == b {
		t.Fatal("direction must not be collapsed: an in-walk and an out-walk read different keyspaces")
	}
}

// TestBaselineTable_ParsesAndRoundTrips — the embedded table is the guard's
// grounding, so a malformed row must be loud rather than silently dropped.
func TestBaselineTable_ParsesAndRoundTrips(t *testing.T) {
	sets := parseBaseline("# prose\n\nread\tOpA\tvtx.thing.<id>\nwalk\tOpB\tvtx.hub.<id> rel out\n")
	if _, ok := sets[baselineKindRead]["OpA"]["vtx.thing.<id>"]; !ok {
		t.Fatal("read row not parsed")
	}
	if _, ok := sets[baselineKindWalk]["OpB"]["vtx.hub.<id> rel out"]; !ok {
		t.Fatal("walk row not parsed")
	}
	for _, bad := range []string{"read\tOnlyTwoFields\n", "nonsense\tOpA\tshape\n"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("a malformed row must panic, not be skipped: %q", bad)
				}
			}()
			parseBaseline(bad)
		})
	}
}

// TestCapabilityPipeline_ArmsTheDriftGuard reads pipeline.go's own source. It is
// a blunt instrument, chosen because nothing else can reach the property: the
// guard is wired into processor.Deps, which CommitPath keeps private, so no
// assertion from outside can observe whether a built pipeline carries it.
//
// The property is worth a blunt test. Every other test here proves the guard
// works; only this one proves it is switched on, and a refactor that quietly
// dropped the assignment would leave the whole ratchet passing and inert.
func TestCapabilityPipeline_ArmsTheDriftGuard(t *testing.T) {
	src, err := os.ReadFile("pipeline.go")
	if err != nil {
		t.Fatalf("read pipeline.go: %v", err)
	}
	for _, want := range []string{
		"NewReadDriftGuard(t)",
		"deps.ScriptReadObserver = observers",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("CapabilityPipeline no longer arms the read-drift guard: %q is gone from pipeline.go", want)
		}
	}
}

// TestReadDriftGuard_EmptyWalkedEndpointAdmitsNothing pins the fail-closed
// direction of the class-(e) admission: a link key has no vertex root, so an
// empty root must never match a walked endpoint. Without the guard's non-empty
// check a single malformed endpoint in EnumeratedVertices would admit EVERY
// undeclared link-key read for that execution.
func TestReadDriftGuard_EmptyWalkedEndpointAdmitsNothing(t *testing.T) {
	var got []string
	g := &ReadDriftGuard{
		errorf: func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) },
		reads:  map[string]map[string]struct{}{},
		walks:  map[string]map[string]struct{}{},
		seen:   map[string]struct{}{},
	}
	g.ObserveScriptReads(context.Background(),
		&processor.OperationEnvelope{OperationType: "OpUnderTest"},
		processor.ScriptReadRecord{
			LiveReads:          []string{"lnk.identity.SecCredSingNPQRSTUVW.worksAt.building.BBcandidateHJKMNPQRS"},
			EnumeratedVertices: []string{""},
		})
	if len(got) != 1 {
		t.Fatalf("an undeclared link-key read alongside an empty walked endpoint must still fire; got %d findings: %v", len(got), got)
	}
}
