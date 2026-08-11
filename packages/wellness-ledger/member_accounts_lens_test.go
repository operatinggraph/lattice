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
