package full

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParse_ProjectionStarRejected pins that a `*` projection body is a parse
// error in both clauses that carry one.
//
// The grammar admits the star as a bare terminal no oC_ProjectionItem wraps, so
// it reaches the AST as nothing at all: `WITH *` becomes an empty projection
// list and every binding it was meant to carry is unbound downstream, while
// `WITH *, x AS y` becomes the single item `x` with the star silently gone. The
// star-with-items form is why the refusal reads the terminal rather than the
// item count — that list is not empty.
func TestParse_ProjectionStarRejected(t *testing.T) {
	eng := New()

	for _, spec := range []struct {
		body     string
		fragment string
	}{
		{`MATCH (u:unit) WITH * RETURN u.key AS key`, "a WITH projection may not use `*`"},
		{`MATCH (u:unit) WITH *, u.key AS key RETURN key AS key`, "a WITH projection may not use `*`"},
		{`MATCH (u:unit) RETURN *`, "a RETURN projection may not use `*`"},
		{`MATCH (u:unit) RETURN *, u.key AS key`, "a RETURN projection may not use `*`"},
	} {
		_, err := eng.Parse(spec.body)
		require.Errorf(t, err, "a star projection must not parse: %s", spec.body)
		require.Contains(t, err.Error(), spec.fragment)
	}

	// A `*` that is a multiplication operator lives inside a projection ITEM,
	// not beside one, so the refusal never sees it.
	for _, body := range []string{
		`MATCH (u:unit) RETURN u.count * 2 AS doubled`,
		`MATCH (u:unit) WITH u.count * 2 AS doubled RETURN doubled AS doubled`,
		`MATCH (u:unit)-[:manages]->(i:identity) WITH u, i RETURN u.key AS key, i.key AS ik`,
	} {
		_, err := eng.Parse(body)
		require.NoErrorf(t, err, "a named projection must still parse: %s", body)
	}
}

// TestParse_RequiredMatchWithoutNewBindingRejected pins that a required MATCH
// which introduces no new named variable is a parse error.
//
// Such a clause expands into rows the executor recognises as null expansions
// (executor.isNonNullExpansion skips anonymous and already-bound elements), and
// executor.applyMatch keeps a null expansion only for OPTIONAL — so the required
// form drops every row where its text reads as a filter. OPTIONAL is untouched:
// there the null-preserving row is the clause's own semantics.
func TestParse_RequiredMatchWithoutNewBindingRejected(t *testing.T) {
	eng := New()

	for _, body := range []string{
		// Nothing named anywhere in the query's only clause.
		`MATCH (:identity)-[:holdsRole]->(:role) RETURN 1 AS one`,
		`MATCH (:unit) RETURN 1 AS one`,
		// Every element already bound by an earlier clause.
		`MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role)
MATCH (i)-[:holdsRole]->(r)
RETURN i.key AS k, r.key AS rk`,
		// The anchor re-used, the far end anonymous.
		`MATCH (i:identity {key: $actorKey})
MATCH (i)-[:holdsRole]->(:role)
RETURN i.key AS k`,
		// A WITH carries the name on, so the clause after it still binds nothing new.
		`MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role)
WITH i, r
MATCH (i)-[:holdsRole]->(r)
RETURN i.key AS k`,
	} {
		_, err := eng.Parse(body)
		require.Errorf(t, err, "a required MATCH binding nothing new must not parse: %s", body)
		require.Contains(t, err.Error(), "a required MATCH must introduce at least one new named variable")
	}

	for _, body := range []string{
		// The named-relationship form: `r` and `role` are both new.
		`MATCH (i:identity {key: $actorKey})
MATCH (i)-[r:holdsRole]->(role:role)
RETURN i.key AS k, type(r) AS rel`,
		// The boundary: the anchor is already bound and the far end is
		// anonymous, so the relationship variable ALONE is what makes the clause
		// an expansion — a real hop is crossed and `r` is bound to the link it
		// crossed, which is a new binding and a real filter.
		`MATCH (i:identity {key: $actorKey})
MATCH (i)-[r:holdsRole]->(:role)
RETURN i.key AS k, type(r) AS rel`,
		// A first MATCH whose anchor is named and whose tail is anonymous still
		// introduces the anchor.
		`MATCH (i:identity)-[:holdsRole]->(:role) RETURN i.key AS k`,
		`MATCH (u:unit) RETURN u.key AS key`,
		// OPTIONAL binding nothing new is the shape whose null-preserving row is
		// the clause's own semantics.
		`MATCH (i:identity {key: $actorKey})
OPTIONAL MATCH (i)-[:holdsRole]->(:role)
RETURN i.key AS k`,
		`MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role)
OPTIONAL MATCH (i)-[:holdsRole]->(r)
RETURN i.key AS k, r.key AS rk`,
		// A WITH rebuilds the row from its items alone, so a name it drops is
		// unbound afterwards and the next MATCH introduces it afresh.
		`MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role)
WITH r
MATCH (i:identity)-[:holdsRole]->(r)
RETURN r.key AS rk`,
	} {
		_, err := eng.Parse(body)
		require.NoErrorf(t, err, "a MATCH binding a new variable must still parse: %s", body)
	}
}
