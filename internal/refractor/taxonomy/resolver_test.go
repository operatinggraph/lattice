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
	billableID = tid("TAXbillableMeta")
)

func TestExpand_NoSnapshot_IsUnknown(t *testing.T) {
	r := New()
	_, status := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown", status)
	}
}

func TestExpand_UnknownLabel_IsUnknown(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: unitID, CanonicalName: "unit"},
	})
	_, status := r.Expand(map[string]struct{}{"nosuchtype": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown for an unresolvable label", status)
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
	exp, status := r.Expand(map[string]struct{}{"location": {}})
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
	exp, status := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["location"], "unit")
}

// TestExpand_ReflexiveClosure pins §3.4's "expanded set" row: a bare
// concrete label's own expansion is exactly itself, with no taxonomy
// declared at all.
func TestExpand_ReflexiveClosure(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: unitID, CanonicalName: "unit"},
	})
	r.SetArmed(true)
	exp, status := r.Expand(map[string]struct{}{"unit": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["unit"], "unit")
}

// TestExpand_AbstractExcludesItself pins the other half of §3.4's row: an
// abstract type contributes its concrete leaves but never itself, since it
// names no instance.
func TestExpand_AbstractExcludesItself(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: locationID, CanonicalName: "location", Abstract: true},
		{ID: unitID, CanonicalName: "unit", SubtypeOf: []string{"location"}},
		{ID: buildingID, CanonicalName: "building", SubtypeOf: []string{"location"}},
	})
	r.SetArmed(true)
	exp, status := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["location"], "unit", "building")
}

// TestExpand_ConcreteTypeWithSubtypes pins amendment A5 (commit 33e562c4): a
// CONCRETE type may itself have subtypes, and its own expansion must still
// include its own instances (reflexivity) alongside its descendants'.
func TestExpand_ConcreteTypeWithSubtypes(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{
		{ID: unitID, CanonicalName: "unit"},
		{ID: roomID, CanonicalName: "room", SubtypeOf: []string{"unit"}},
	})
	r.SetArmed(true)
	exp, status := r.Expand(map[string]struct{}{"unit": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["unit"], "unit", "room")
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
	exp, status := r.Expand(map[string]struct{}{"location": {}})
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
	exp, status := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["location"], "unit", "building", "room")

	expB, statusB := r.Expand(map[string]struct{}{"billable": {}})
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
	_, status := r.Expand(map[string]struct{}{"location": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown for a cyclic taxonomy", status)
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
	_, status := r.Expand(map[string]struct{}{"root": {}})
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
	exp, status := r.Expand(map[string]struct{}{"root": {}})
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
	exp, status := r.Expand(map[string]struct{}{"location": {}})
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
// last-write-wins winner while dropping the other registrant's subtree.
func TestInstallSnapshot_DuplicateCanonicalName_IsUnresolvable(t *testing.T) {
	r := New()
	oldID := tid("TAXdupOldMeta")
	newID := tid("TAXdupNewMeta")
	childID := tid("TAXdupChildMeta")
	r.InstallSnapshot([]TypeSnapshot{
		{ID: oldID, CanonicalName: "unit"},
		{ID: newID, CanonicalName: "unit"},
		{ID: childID, CanonicalName: "room", SubtypeOf: []string{"unit"}},
	})
	r.SetArmed(true)

	_, status := r.Expand(map[string]struct{}{"unit": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown for a duplicated canonicalName", status)
	}

	// The colliding name must not silently resolve as a SubtypeOf parent
	// either — "room" declared subtypeOf the ambiguous "unit" name, so its
	// edge must not attach to either registrant, and "room" alone (queried
	// directly, not through "unit") still resolves to just itself.
	exp, status := r.Expand(map[string]struct{}{"room": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed for the unambiguous label", status)
	}
	requireSet(t, exp["room"], "room")
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
	})
	r.SetArmed(true)

	_, status := r.Expand(map[string]struct{}{"": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown for an empty label", status)
	}
	exp, status := r.Expand(map[string]struct{}{"unit": {}})
	if status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed", status)
	}
	requireSet(t, exp["unit"], "unit")
}

// TestInstallSnapshot_ResetsArmed pins that a reload never carries a
// previous life's armed flag forward — only SetArmed may arm the resolver,
// so a consumer that dies and later re-installs a snapshot before
// re-arming must see StatusStale, not a stale StatusArmed.
func TestInstallSnapshot_ResetsArmed(t *testing.T) {
	r := New()
	r.InstallSnapshot([]TypeSnapshot{{ID: unitID, CanonicalName: "unit"}})
	r.SetArmed(true)
	if _, status := r.Expand(map[string]struct{}{"unit": {}}); status != StatusArmed {
		t.Fatalf("status = %v, want StatusArmed before reload", status)
	}

	r.InstallSnapshot([]TypeSnapshot{{ID: unitID, CanonicalName: "unit"}})
	if _, status := r.Expand(map[string]struct{}{"unit": {}}); status != StatusStale {
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

	_, status := r.Expand(map[string]struct{}{"dup": {}})
	if status != StatusUnknown {
		t.Fatalf("status = %v, want StatusUnknown querying the duplicated name directly", status)
	}

	_, status = r.Expand(map[string]struct{}{"g": {}})
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
