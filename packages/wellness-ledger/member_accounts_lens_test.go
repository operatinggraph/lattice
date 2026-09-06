package wellnessledger

// Rule-engine proof of wellnessMemberAccounts (memberAccountsSpec), driven
// through the `full` engine against an embedded NATS Core/Adjacency KV — the
// same harness lens_cypher_test.go uses.
//
// The lens's own contract is in its name: one row per identity that has ever
// booked, whatever their booking count, plus a null accountKey for a member who
// has not opened a ledger account yet. `WITH DISTINCT id` is what collapses the
// per-booking rows; each assertion below is paired with the count of bookings
// that fed it, so a member with three bookings and a member with one are both
// exercised.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectMemberAccounts runs the unanchored memberAccountsSpec over the whole
// corpus, the way its nats-kv lens does.
func (f *wlFixture) projectMemberAccounts(t *testing.T) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(memberAccountsSpec)
	require.NoError(t, err, "wellnessMemberAccounts cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// TestWellnessMemberAccounts_OneRowPerMember pins the row COUNT the lens's own
// comment promises. Six bookings across three members project three rows — one
// per member — and the member who never booked projects none.
func TestWellnessMemberAccounts_OneRowPerMember(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)

	members := map[string]int{"alice": 3, "bob": 2, "carol": 1}
	for member, bookings := range members {
		f.vtx(t, member, "identity")
		for i := 0; i < bookings; i++ {
			bk := member + "_bk" + string(rune('0'+i))
			f.vtx(t, bk, "booking")
			f.edge(t, "bookedBy", bk, member)
		}
	}
	// A member of the platform who never touched Wellness projects nothing:
	// the lens walks bookings, not the identity bucket.
	f.vtx(t, "neverbooked", "identity")

	// alice has opened an account; bob and carol have not.
	f.vtx(t, "aliceacct", "wellnessaccount")
	f.edge(t, "heldFor", "aliceacct", "alice")

	rows := f.projectMemberAccounts(t)
	require.Len(t, rows, len(members),
		"one row per member that has ever booked, not one per booking")

	byIdentity := map[string]map[string]any{}
	for _, r := range rows {
		key, _ := r.Values["identityKey"].(string)
		require.NotEmpty(t, key)
		require.NotContains(t, byIdentity, key, "a member must not project twice")
		byIdentity[key] = r.Values
	}

	for member := range members {
		v, present := byIdentity["vtx.identity."+f.ids[member]]
		require.Truef(t, present, "%s booked, so %s must project a row", member, member)
		require.Equal(t, v["identityKey"], v["key"], "the row is keyed on the member's own vertex key")
	}
	require.NotContains(t, byIdentity, "vtx.identity."+f.ids["neverbooked"],
		"an identity with no booking is not a member of this read model")

	// The row CONTENT is what it always was — de-duplication removes copies of
	// a row, never a column of it.
	require.Equal(t, "vtx.wellnessaccount."+f.ids["aliceacct"],
		byIdentity["vtx.identity."+f.ids["alice"]]["accountKey"],
		"a member with an account carries it, after three bookings collapsed to one row")
	require.Nil(t, byIdentity["vtx.identity."+f.ids["bob"]]["accountKey"],
		"a member with no account yet still gets a row, with a null accountKey")
	require.Nil(t, byIdentity["vtx.identity."+f.ids["carol"]]["accountKey"])
}

// TestWellnessMemberAccounts_EveryMemberKeyStillWritten is the retraction-side
// pin. Collapsing duplicates changes how many times a KEY is written, never
// WHICH keys are written: the lens's key set before and after must be equal, or
// a live target row would be dropped by a lens that no longer emits its key.
//
// It holds in BOTH worlds by design — it is the invariant across the change,
// not a pin on the de-duplication itself (its siblings above are that), so it
// passing with the keyword unhonoured is the correct outcome, not a false pass.
func TestWellnessMemberAccounts_EveryMemberKeyStillWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newWlFixture(t)
	for _, member := range []string{"alice", "bob"} {
		f.vtx(t, member, "identity")
		for i := 0; i < 3; i++ {
			bk := member + "_bk" + string(rune('0'+i))
			f.vtx(t, bk, "booking")
			f.edge(t, "bookedBy", bk, member)
		}
	}

	distinctKeys := map[string]bool{}
	for _, r := range f.projectMemberAccounts(t) {
		distinctKeys[r.Key["key"].(string)] = true
	}

	// The same corpus through the same spec with the keyword removed — the row
	// set the lens produced before DISTINCT was honoured.
	duplicating := memberAccountsSpecWithoutDistinct(t)
	eng := full.New()
	cr, err := eng.Parse(duplicating)
	require.NoError(t, err)
	before, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, before, 6, "six bookings used to project six rows")

	duplicateKeys := map[string]bool{}
	for _, r := range before {
		duplicateKeys[r.Key["key"].(string)] = true
	}
	require.Equal(t, duplicateKeys, distinctKeys,
		"the set of keys the lens writes is unchanged — only the number of writes per key fell")
}

// memberAccountsSpecWithoutDistinct is the shipped spec with the keyword
// removed, so a test can compare the row set against the one the lens produced
// while the keyword was a no-op.
func memberAccountsSpecWithoutDistinct(t *testing.T) string {
	t.Helper()
	const distinct, plain = "WITH DISTINCT id", "WITH id"
	require.Contains(t, memberAccountsSpec, distinct,
		"memberAccountsSpec no longer carries the clause this comparison is about")
	return strings.Replace(memberAccountsSpec, distinct, plain, 1)
}

// memberAccountsSpecAnchoredOnBooking is memberAccountsSpec with the walk
// direction reversed, so the anchor pattern (the first MATCH clause's first
// node, full.CompiledRule.AnchorLabel) is `bk:booking` instead of
// `id:identity` — the shape anchor-partitioned-plain-lens-retraction-design.md
// §8 row 2 names as unable to partition, since the row's key (id.key) is then
// a neighbour's key relative to the anchor, not the anchor's own. It exists
// only so TestWellnessMemberAccounts_AnchorsOnIdentity's assertions
// discriminate against a real counter-example rather than holding vacuously
// for any spec.
func memberAccountsSpecAnchoredOnBooking(t *testing.T) string {
	t.Helper()
	const reanchored, original = "MATCH (id:identity)<-[:bookedBy]-(bk:booking)", "MATCH (bk:booking)-[:bookedBy]->(id:identity)"
	require.Contains(t, memberAccountsSpec, reanchored,
		"memberAccountsSpec no longer carries the anchor pattern this comparison is about")
	return strings.Replace(memberAccountsSpec, reanchored, original, 1)
}

// TestWellnessMemberAccounts_AnchorsOnIdentity is the anchor-side pin
// (anchor-partitioned-plain-lens-retraction-design.md §8 row 2): the lens's
// rows must PARTITION by its anchor, so Refractor can seed and retract this
// lens per identity instead of rescanning the whole corpus + diffing the
// whole target bucket on every event.
//
// A per-anchor SEEDED evaluation (EventContext.Parameters["actorKey"] pinning
// a `{key: $actorKey}` position in the pattern, the shape projectAt/
// projectClassPriceAt/projectRefundAt use above) is not exercisable for this
// spec: memberAccountsSpec is a PLAIN lens with no such literal position — it
// is Refractor's own per-anchor partition-seeding machinery
// (internal/refractor/pipeline, internal/refractor/ruleengine/full's
// PartitionsByAnchor/ProjectsOneRowPerAnchor) that turns an unparameterized
// pattern like this one into a per-anchor evaluation, and that machinery is
// exercised in internal/refractor's own corpus census tests
// (plain_partition_census_test.go, plain_scanroot_corpus_census_test.go),
// which this package cannot import without a cycle. What this package CAN
// pin is the parsed spec's own structural verdict — the same one those
// census tests read off the identical predicates — the anchor is the
// identity, and the row's key is that anchor's own key.
func TestWellnessMemberAccounts_AnchorsOnIdentity(t *testing.T) {
	eng := full.New()

	cr, err := eng.Parse(memberAccountsSpec)
	require.NoError(t, err, "memberAccountsSpec must parse on the full engine")
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull, "memberAccountsSpec must compile to the full engine")

	label, ok := fullCR.AnchorLabel()
	require.True(t, ok, "the anchor pattern must carry a label")
	require.Equal(t, "identity", label,
		"the anchor is the first MATCH clause's first node — must be the identity, not the booking")

	require.True(t, fullCR.ProjectsOneRowPerAnchor(),
		"id.key is now the anchor's OWN key, so the lens's rows partition by anchor — "+
			"Refractor can seed and retract per identity with no whole-bucket diff")

	// A lens anchored on the booking instead must fail both assertions above:
	// the label is the wrong vertex type, and id.key is a NEIGHBOUR's key
	// relative to that anchor, not the anchor's own, so the partition licence
	// is refused — proving the two assertions above discriminate rather than
	// holding for any spec.
	onBooking := memberAccountsSpecAnchoredOnBooking(t)
	crOnBooking, err := eng.Parse(onBooking)
	require.NoError(t, err, "the booking-anchored spec must still parse")
	fullCROnBooking, isFull := crOnBooking.(*full.CompiledRule)
	require.True(t, isFull)

	labelOnBooking, ok := fullCROnBooking.AnchorLabel()
	require.True(t, ok)
	require.Equal(t, "booking", labelOnBooking,
		"sanity: the alternative spec really does anchor on the booking")
	require.False(t, fullCROnBooking.ProjectsOneRowPerAnchor(),
		"a lens anchored on the booking does not partition by anchor — the defect the design's §8 row 2 names")
}
