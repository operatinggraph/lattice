package taxonomy

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// tid pads base into a valid Contract #1 20-char NanoID. base is sanitized
// against keys.Alphabet's excluded I/l/O/0 first (a descriptive base string
// like "location" spells out a lowercase 'l', which the real alphabet
// forbids) — every generated ID is then verified against keys.IsValidNanoID
// so a future alphabet change fails this helper loudly instead of quietly
// seeding an invalid fixture.
func tid(base string) string {
	repl := strings.NewReplacer("I", "J", "l", "L", "O", "Q", "0", "9")
	id := repl.Replace(base)
	for len(id) < 20 {
		id += "A"
	}
	if len(id) > 20 {
		id = id[:20]
	}
	if !keys.IsValidNanoID(id) {
		panic("taxonomy test: tid produced an invalid NanoID: " + id)
	}
	return id
}

var (
	locationID = tid("TAXlocationMeta")
	unitID     = tid("TAXunitMeta")
	buildingID = tid("TAXbuildingMeta")
	roomID     = tid("TAXroomMeta")
	closetID   = tid("TAXclosetMeta")
	billableID = tid("TAXbillableMeta")
)

func TestExpand_NoSnapshot_IsUnknown(t *testing.T) {
	r := New()
	_, _, status, reason := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown", status)
	}
	if reason == "" {
		t.Fatal("expected a non-empty reason for an unloaded resolver")
	}
}

func TestExpand_UnknownLabel_IsUnknown(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: unitID, CanonicalName: "unit"},
	})
	_, _, status, reason := r.Expand(map[string]struct{}{"nosuchtype": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown for an unresolvable label", status)
	}
	if !strings.Contains(reason, "nosuchtype") {
		t.Errorf("reason should name the offending label; got %q", reason)
	}
}

func TestExpand_SnapshotWithoutLiveConsumer_IsStaleNotArmed(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: locationID, CanonicalName: "location", Abstract: true},
		{ID: unitID, CanonicalName: "unit", SubtypeOf: []string{"location"}},
	})
	// SetArmed is never called: the resolver ships disarmed until item 4's
	// CDC consumer marks it live.
	exp, _, status, _ := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusStale {
		t.Fatalf("status = %v, want StatusStale", status)
	}
	requireSet(t, exp["location"], "unit")
}

func TestExpand_Armed_ReportsArmed(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: locationID, CanonicalName: "location", Abstract: true},
		{ID: unitID, CanonicalName: "unit", SubtypeOf: []string{"location"}},
	})
	r.SetArmed(true)
	exp, _, status, _ := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["location"], "unit")
}

// TestExpand_ConcreteChildless_ResolvesToItselfAndIsInert pins the
// activation/re-derivation split (dynamic-type-taxonomy-design.md §14 Fire A
// item 5, amendment A3): Expand itself never refuses a concrete childless
// `*` label — its closure is exactly {itself}, a perfectly valid answer —
// it only FLAGS the label as inert via the returned inert set. What a
// caller does with that flag is the caller's decision; TestUseFullEngine_
// ConcreteChildlessSigil_RefusesAtActivationOnly (pipeline package) pins the
// two opposite decisions the two production callers make.
func TestExpand_ConcreteChildless_ResolvesToItselfAndIsInert(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: unitID, CanonicalName: "unit"},
	})
	r.SetArmed(true)
	exp, inert, status, reason := r.Expand(map[string]struct{}{"unit": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed — a childless concrete label is a resolvable answer, not a fault", status)
	}
	if reason != "" {
		t.Errorf("expected no reason on a successful resolution, got %q", reason)
	}
	requireSet(t, exp["unit"], "unit")
	if _, ok := inert["unit"]; !ok {
		t.Errorf("expected %q in the inert set, got %v", "unit", inert)
	}
}

// TestExpand_ConcreteWithOnlyAbstractLeaflessChild_IsInert pins that
// inertness is decided from the COMPUTED CLOSURE, not a direct-child count:
// "unit" has one direct child, "unitgroup", but unitgroup is abstract and
// has no concrete descendants of its own. unit's closure is therefore still
// exactly {"unit"} — structurally identical to the childless case above —
// so it must be flagged inert too, even though len(direct children) == 1.
func TestExpand_ConcreteWithOnlyAbstractLeaflessChild_IsInert(t *testing.T) {
	r := New()
	groupID := tid("TAXunitgroupMeta")
	r.InstallSnapshot([]TypeSnapshot{
		{ID: unitID, CanonicalName: "unit"},
		{ID: groupID, CanonicalName: "unitgroup", Abstract: true, SubtypeOf: []string{"unit"}},
	})
	r.SetArmed(true)
	exp, inert, status, _ := r.Expand(map[string]struct{}{"unit": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["unit"], "unit")
	if _, ok := inert["unit"]; !ok {
		t.Errorf("a concrete type whose only child is abstract-and-leafless must be inert (closure == {itself}); inert = %v", inert)
	}
}

// TestExpand_ReflexiveClosure pins §3.4's "expanded set" row: a concrete
// type with at least one CONCRETE (or otherwise leaf-bearing) subtypeOf
// descendant expands reflexively to itself plus its descendants, and is NOT
// flagged inert — the `*` sigil bought something real.
func TestExpand_ReflexiveClosure(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: unitID, CanonicalName: "unit"},
		{ID: roomID, CanonicalName: "room", SubtypeOf: []string{"unit"}},
	})
	r.SetArmed(true)
	exp, inert, status, _ := r.Expand(map[string]struct{}{"unit": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["unit"], "unit", "room")
	if _, ok := inert["unit"]; ok {
		t.Errorf("unit's closure gained \"room\" — it must not be flagged inert")
	}
}

// TestExpand_AbstractExcludesItself pins the other half of §3.4's row: an
// abstract type contributes its concrete leaves but never itself, since it
// names no instance. Never flagged inert — inertness applies only to a
// CONCRETE label (see Expand's doc).
func TestExpand_AbstractExcludesItself(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: locationID, CanonicalName: "location", Abstract: true},
		{ID: unitID, CanonicalName: "unit", SubtypeOf: []string{"location"}},
		{ID: buildingID, CanonicalName: "building", SubtypeOf: []string{"location"}},
	})
	r.SetArmed(true)
	exp, inert, status, _ := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["location"], "unit", "building")
	if _, ok := inert["location"]; ok {
		t.Errorf("an abstract label must never be flagged inert, got %v", inert)
	}
}

// TestExpand_AbstractWithNoChildrenAtAll_IsArmedWithEmptySet pins §4.2's
// second tier at the boundary a later fire could otherwise silently break:
// an abstract type with ZERO subtypeOf children (not merely an abstract
// mid-type with no concrete descendants) still resolves to a KNOWN, empty,
// non-inert set — never StatusUnknown, and never flagged inert (inertness
// is a CONCRETE-only concept). Kept distinct from
// TestExpand_AbstractWithNoConcreteDescendants_IsArmedWithEmptySet below,
// which exercises an abstract WITH a (leafless) child — the "zero direct
// children" and "children exist but none are concrete" shapes are both
// required to stay green independently.
func TestExpand_AbstractWithNoChildrenAtAll_IsArmedWithEmptySet(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: locationID, CanonicalName: "location", Abstract: true},
	})
	r.SetArmed(true)
	exp, inert, status, _ := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	if got := exp["location"]; len(got) != 0 {
		t.Fatalf("got %v, want an empty (but present) set", got)
	}
	if _, ok := inert["location"]; ok {
		t.Errorf("an abstract label must never be flagged inert, got %v", inert)
	}
}

// TestExpand_ConcreteTypeWithSubtypes pins amendment A5 (commit 33e562c4): a
// CONCRETE type may itself have subtypes, and its own expansion must still
// include its own instances (reflexivity) alongside its descendants', and
// must not be flagged inert.
func TestExpand_ConcreteTypeWithSubtypes(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: unitID, CanonicalName: "unit"},
		{ID: roomID, CanonicalName: "room", SubtypeOf: []string{"unit"}},
	})
	r.SetArmed(true)
	exp, inert, status, _ := r.Expand(map[string]struct{}{"unit": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["unit"], "unit", "room")
	if _, ok := inert["unit"]; ok {
		t.Errorf("unit's closure gained \"room\" — it must not be flagged inert")
	}
}

// TestExpand_MultiLevelChain pins §3.4's transitivity rule: location <-
// unit <- room must expose room three hops down from location.
func TestExpand_MultiLevelChain(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: locationID, CanonicalName: "location", Abstract: true},
		{ID: unitID, CanonicalName: "unit", SubtypeOf: []string{"location"}},
		{ID: roomID, CanonicalName: "room", SubtypeOf: []string{"unit"}},
	})
	r.SetArmed(true)
	exp, _, status, _ := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["location"], "unit", "room")
}

// TestExpand_Diamond pins that a type reachable by two independent
// subtypeOf paths appears exactly once — the diamond §3.4's "multiple
// parents" row explicitly allows.
func TestExpand_Diamond(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: locationID, CanonicalName: "location", Abstract: true},
		{ID: billableID, CanonicalName: "billable", Abstract: true},
		{ID: unitID, CanonicalName: "unit", SubtypeOf: []string{"location"}},
		{ID: buildingID, CanonicalName: "building", SubtypeOf: []string{"location"}},
		{ID: roomID, CanonicalName: "room", SubtypeOf: []string{"unit", "billable"}},
	})
	r.SetArmed(true)
	exp, _, status, _ := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["location"], "unit", "building", "room")

	expB, _, statusB, _ := r.Expand(map[string]struct{}{"billable": {}})
	if statusB != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", statusB)
	}
	requireSet(t, expB["billable"], "room")
}

// TestExpand_Cycle_IsUnknown pins resolver-time cycle detection as the
// authority (§14 Fire A item 3): a cycle a resolver observes — however it
// got there — must never be trusted, regardless of what pkgmgr's
// install-time check would have concluded about the same edges.
func TestExpand_Cycle_IsUnknown(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: locationID, CanonicalName: "location", SubtypeOf: []string{"unit"}},
		{ID: unitID, CanonicalName: "unit", SubtypeOf: []string{"location"}},
	})
	r.SetArmed(true)
	_, _, status, reason := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown for a cyclic taxonomy", status)
	}
	if !strings.Contains(reason, "location") || !strings.Contains(reason, "cycle") {
		t.Errorf("reason should name the queried label and the cycle cause; got %q", reason)
	}
}

// TestExpand_ReasonTruncatesLongCanonicalName pins that a reason string
// built from a raw canonicalName (unbounded — validated only on the
// pkgmgr-mediated install path, never on a raw core-operations submit) is
// always bounded, never interpolated verbatim. A cycle is the vehicle: the
// cycle-detection reason interpolates the OTHER node's canonicalName
// (collectDownLocked), which here is deliberately far longer than
// maxReasonNameLen.
func TestExpand_ReasonTruncatesLongCanonicalName(t *testing.T) {
	r := New()
	longName := strings.Repeat("x", maxReasonNameLen+50)
	longID := tid("TAXlongCycleMeta")
	shortID := tid("TAXshortCycleMeta")
	r.InstallSnapshot([]TypeSnapshot{
		{ID: longID, CanonicalName: longName, SubtypeOf: []string{"shortloop"}},
		{ID: shortID, CanonicalName: "shortloop", SubtypeOf: []string{longName}},
	})
	r.SetArmed(true)
	_, _, status, reason := r.Expand(map[string]struct{}{"shortloop": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown for a cyclic taxonomy", status)
	}
	if strings.Contains(reason, longName) {
		t.Errorf("reason must not interpolate the raw, untruncated canonicalName (%d bytes); got %q", len(longName), reason)
	}
	if len(reason) > 4*maxReasonNameLen {
		t.Errorf("reason is not bounded: %d bytes: %q", len(reason), reason)
	}
}

// TestExpand_OverDepth_IsUnknown pins the maxDepth bound: a chain longer
// than maxDepth degrades to StatusUnknown rather than a silently truncated
// set.
func TestExpand_OverDepth_IsUnknown(t *testing.T) {
	// A chain of maxDepth+2 links from the root: root -> unit -> room ->
	// hallway -> building -> property, six nodes, five hops — deeper than
	// maxDepth (4).
	root := tid("TAXdeepRoot")
	n1 := tid("TAXdeepN1")
	n2 := tid("TAXdeepN2")
	n3 := tid("TAXdeepN3")
	n4 := tid("TAXdeepN4")
	n5 := tid("TAXdeepN5")
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: root, CanonicalName: "root", Abstract: true},
		{ID: n1, CanonicalName: "n1", SubtypeOf: []string{"root"}},
		{ID: n2, CanonicalName: "n2", SubtypeOf: []string{"n1"}},
		{ID: n3, CanonicalName: "n3", SubtypeOf: []string{"n2"}},
		{ID: n4, CanonicalName: "n4", SubtypeOf: []string{"n3"}},
		{ID: n5, CanonicalName: "n5", SubtypeOf: []string{"n4"}},
	})
	r.SetArmed(true)
	_, _, status, _ := r.Expand(map[string]struct{}{"root": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown for a chain deeper than maxDepth", status)
	}
}

// TestExpand_WithinDepthBound_Resolves pins the boundary from the other
// side: a chain exactly at maxDepth must still resolve.
func TestExpand_WithinDepthBound_Resolves(t *testing.T) {
	root := tid("TAXokRoot")
	n1 := tid("TAXokN1")
	n2 := tid("TAXokN2")
	n3 := tid("TAXokN3")
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: root, CanonicalName: "root", Abstract: true},
		{ID: n1, CanonicalName: "n1", SubtypeOf: []string{"root"}},
		{ID: n2, CanonicalName: "n2", SubtypeOf: []string{"n1"}},
		{ID: n3, CanonicalName: "n3", SubtypeOf: []string{"n2"}},
	})
	r.SetArmed(true)
	exp, _, status, _ := r.Expand(map[string]struct{}{"root": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed for a chain within maxDepth", status)
	}
	requireSet(t, exp["root"], "n1", "n2", "n3")
}

// TestExpand_AbstractWithNoConcreteDescendants_IsArmedWithEmptySet pins §3.4's
// expanded-set row on the resolver's own boundary: an abstract type whose
// only descendant is itself abstract resolves to a KNOWN, empty set, not
// StatusUnknown — "genuinely zero leaves" is a real, resolvable answer. The
// caller (pipeline.useFullEngineBranches) is what degrades this to a broad
// filter; the resolver's job is only to report the truth.
func TestExpand_AbstractWithNoConcreteDescendants_IsArmedWithEmptySet(t *testing.T) {
	r := New()
	midID := tid("TAXemptyMidMeta")
	r.InstallSnapshot([]TypeSnapshot{
		{ID: locationID, CanonicalName: "location", Abstract: true},
		{ID: midID, CanonicalName: "mid", Abstract: true, SubtypeOf: []string{"location"}},
	})
	r.SetArmed(true)
	exp, _, status, _ := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	if got := exp["location"]; len(got) != 0 {
		t.Fatalf("got %v, want an empty (but present) set", got)
	}
}

// TestInstallSnapshot_DuplicateCanonicalName_IsUnresolvable pins the
// behavior for a CanonicalName shared by two snapshot entries (e.g. a
// rename's create-new + tombstone-old both live in one boot-replay window):
// the name must become UNRESOLVABLE, never silently resolve to an arbitrary
// last-write-wins winner while dropping the other registrant's subtree. A
// DIRECT query of the ambiguous name must report the AMBIGUITY as its
// cause, not a generic "unknown label" — the whole point of a reason string
// is naming the real cause, and during a rename's create-new/tombstone-old
// window this is a collision, not a typo.
func TestInstallSnapshot_DuplicateCanonicalName_IsUnresolvable(t *testing.T) {
	r := New()
	oldID := tid("TAXdupOldMeta")
	newID := tid("TAXdupNewMeta")
	childID := tid("TAXdupChildMeta")
	r.InstallSnapshot([]TypeSnapshot{
		{ID: oldID, CanonicalName: "unit"},
		{ID: newID, CanonicalName: "unit"},
		{ID: childID, CanonicalName: "room", SubtypeOf: []string{"unit"}},
		{ID: closetID, CanonicalName: "closet", SubtypeOf: []string{"room"}},
	})
	r.SetArmed(true)

	_, _, status, reason := r.Expand(map[string]struct{}{"unit": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown for a duplicated canonicalName", status)
	}
	if !strings.Contains(reason, "unit") || !strings.Contains(reason, "ambiguous") {
		t.Errorf("a direct query of a collided name should report AMBIGUITY as the cause, not a generic unknown; got %q", reason)
	}

	// The colliding name must not silently resolve as a SubtypeOf parent
	// either — "room" declared subtypeOf the ambiguous "unit" name, so its
	// edge must not attach to either registrant, and "room" alone (queried
	// directly, not through "unit") still resolves to itself plus its own
	// child "closet" — proving the collision on "unit" left "room" itself
	// perfectly resolvable.
	exp, _, status, _ := r.Expand(map[string]struct{}{"room": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed for the unambiguous label", status)
	}
	requireSet(t, exp["room"], "room", "closet")
}

// TestInstallSnapshot_EmptyCanonicalName_NeverResolvable pins that a
// snapshot entry with no name (a malformed/partial record) can never make
// the empty string a resolvable label, and does not otherwise disturb an
// unrelated, well-formed entry.
func TestInstallSnapshot_EmptyCanonicalName_NeverResolvable(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: tid("TAXblankMeta"), CanonicalName: ""},
		{ID: unitID, CanonicalName: "unit"},
		{ID: roomID, CanonicalName: "room", SubtypeOf: []string{"unit"}},
	})
	r.SetArmed(true)

	_, _, status, _ := r.Expand(map[string]struct{}{"": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown for an empty label", status)
	}
	exp, _, status, _ := r.Expand(map[string]struct{}{"unit": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["unit"], "unit", "room")
}

// TestInstallSnapshot_ResetsArmed pins that a reload never carries a
// previous life's armed flag forward — only SetArmed may arm the resolver,
// so a consumer that dies and later re-installs a snapshot before
// re-arming must see StatusStale, not a stale StatusArmed.
func TestInstallSnapshot_ResetsArmed(t *testing.T) {
	snap := []TypeSnapshot{
		{ID: unitID, CanonicalName: "unit"},
		{ID: roomID, CanonicalName: "room", SubtypeOf: []string{"unit"}},
	}
	r := New()
	r.InstallSnapshot(snap)
	r.SetArmed(true)
	if _, _, status, _ := r.Expand(map[string]struct{}{"unit": {}}); status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed before reload", status)
	}

	r.InstallSnapshot(snap)
	if _, _, status, _ := r.Expand(map[string]struct{}{"unit": {}}); status != StatusStale {
		t.Fatalf("status = %v, want StatusStale immediately after a reload — armed must not survive InstallSnapshot", status)
	}
}

// TestExpand_DuplicateNameReachedAsAncestorDescendant_IsUnknown pins the
// asymmetry a bare byName deletion does not close: G(abstract) has two
// children named "dup" (a collision) and one child "e"; one of the "dup"
// entries in turn has a concrete child "c". Querying "g" must not silently
// return {"e"} while dropping "c" — walking through either poisoned "dup"
// entry must take the WHOLE "g" query to StatusUnknown, exactly like
// querying "dup" directly does.
func TestExpand_DuplicateNameReachedAsAncestorDescendant_IsUnknown(t *testing.T) {
	r := New()
	gID := tid("TAXpoisonGMeta")
	dup1ID := tid("TAXpoisonDup1Meta")
	dup2ID := tid("TAXpoisonDup2Meta")
	cID := tid("TAXpoisonCMeta")
	eID := tid("TAXpoisonEMeta")
	r.InstallSnapshot([]TypeSnapshot{
		{ID: gID, CanonicalName: "g", Abstract: true},
		{ID: dup1ID, CanonicalName: "dup", Abstract: true, SubtypeOf: []string{"g"}},
		{ID: dup2ID, CanonicalName: "dup", Abstract: true, SubtypeOf: []string{"g"}},
		{ID: cID, CanonicalName: "c", SubtypeOf: []string{"dup"}},
		{ID: eID, CanonicalName: "e", SubtypeOf: []string{"g"}},
	})
	r.SetArmed(true)

	_, _, status, reason := r.Expand(map[string]struct{}{"dup": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown querying the duplicated name directly", status)
	}
	if !strings.Contains(reason, "ambiguous") {
		t.Errorf("a direct query of a collided name should report ambiguity as the cause; got %q", reason)
	}

	_, _, status, _ = r.Expand(map[string]struct{}{"g": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown — reaching a poisoned descendant must refuse the whole "+
			"query, never silently return {\"e\"} while \"c\" (unreachable because \"dup\" could not resolve "+
			"as a parent) goes missing", status)
	}
}

func requireSet(t *testing.T, got map[string]struct{}, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d entries %v, want %d entries %v", len(got), got, len(want), want)
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Fatalf("got %v, missing %q", got, w)
		}
	}
}
