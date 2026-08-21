package full

// Required MATCH bindings — a required MATCH has to introduce something.
//
// A MATCH clause is executed as an EXPANSION of the rows it is handed: for each
// incoming row the executor walks the pattern, and keeps the expanded row when
// the walk bound at least one variable the incoming row did not already carry
// (executor.isNonNullExpansion, which skips anonymous elements and elements
// already bound). A clause whose every pattern element is anonymous or already
// bound therefore has nothing for that test to find, on any row — so
// executor.applyMatch sees a null-preserving expansion and keeps the row only
// `if m.Optional`.
//
// For OPTIONAL that is right: the clause is a left join, and a row with no new
// binding survives with nothing added. For the required form it is the opposite
// of what the text says. `MATCH (i:identity)-[:holdsRole]->(:role)` reads as
// "keep the identities that hold a role", and a required MATCH is meant to
// FILTER — but with no new variable to expand into, the clause drops EVERY row
// instead, and the lens projects the empty set with no diagnostic anywhere.
//
// So the shape is refused at parse, where the author is still looking at the
// query. The two readings the engine could take of it — filter, or drop — are
// both defensible, and neither is what the query does today, which is the same
// standard the sigil and alternation refusals apply.
//
// Scope follows the executor's. A MATCH adds every variable its patterns name.
// A WITH rebuilds each row from its projection items alone, so the names bound
// after it are exactly the ones it projects — a name it lets go of is unbound,
// and a later MATCH naming that name introduces it afresh.

import (
	"fmt"
	"sort"
	"strings"
)

// requiredMatchReject returns the reason to refuse q, or "" when every required
// MATCH in it introduces at least one new named variable.
func requiredMatchReject(q *Query) string {
	if q == nil {
		return ""
	}
	bound := map[string]struct{}{}
	ordinal := 0
	for _, c := range q.Clauses {
		switch cl := c.(type) {
		case *Match:
			ordinal++
			introduced, reused := matchIntroductions(cl, bound)
			if !cl.Optional && introduced == 0 {
				return requiredMatchNoBindingReject(ordinal, reused)
			}
			for _, p := range cl.Patterns {
				for _, name := range patternVariables(p) {
					bound[name] = struct{}{}
				}
			}
		case *With:
			bound = withOutputNames(cl)
		}
	}
	return ""
}

// matchIntroductions counts the pattern variables m names that bound does not
// already carry, and returns the ones it does carry, sorted and deduplicated —
// the detail that tells an author whether the clause named nothing at all or
// only renamed what it already had.
func matchIntroductions(m *Match, bound map[string]struct{}) (int, []string) {
	introduced := 0
	seen := map[string]struct{}{}
	for _, p := range m.Patterns {
		for _, name := range patternVariables(p) {
			if _, had := bound[name]; !had {
				introduced++
				continue
			}
			seen[name] = struct{}{}
		}
	}
	reused := make([]string, 0, len(seen))
	for name := range seen {
		reused = append(reused, name)
	}
	sort.Strings(reused)
	return introduced, reused
}

func requiredMatchNoBindingReject(ordinal int, reused []string) string {
	detail := "names no variable at all — every element of its pattern is anonymous"
	if len(reused) > 0 {
		quoted := make([]string, 0, len(reused))
		for _, name := range reused {
			quoted = append(quoted, "`"+name+"`")
		}
		detail = "names only " + strings.Join(quoted, ", ") + ", which the query already bound"
	}
	return fmt.Sprintf(
		"a required MATCH must introduce at least one new named variable — MATCH clause %d %s, so "+
			"the expansion carries nothing to test and the clause DROPS every row where it reads as a "+
			"filter; name the element the pattern reaches (`-[:holdsRole]->(role:role)`), or move the "+
			"existence test into a WHERE",
		ordinal, detail)
}
