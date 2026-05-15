package lens_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/lens"
)

// baseRule returns a minimal valid Rule for use as a diff baseline.
func baseRule() *lens.Rule {
	r, err := lens.Parse([]byte(`
id: hot-rule
team: hot-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  dsn: postgres://localhost/test
  table: old_table
  key: agreement_id
`))
	if err != nil {
		panic("baseRule: " + err.Error())
	}
	return r
}

func TestClassifyUpdate_SameMatchDifferentTable(t *testing.T) {
	old := baseRule()
	newRule, err := lens.Parse([]byte(`
id: hot-rule
team: hot-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id
into:
  target: postgres
  dsn: postgres://localhost/test
  table: new_table
  key: agreement_id
`))
	require.NoError(t, err)

	assert.Equal(t, lens.IntoOnly, lens.ClassifyUpdate(old, newRule),
		"same match + different table must be IntoOnly")
}

func TestClassifyUpdate_SameMatchDifferentKey_Composite(t *testing.T) {
	// Both rules share an identical MATCH clause that returns two columns.
	// The old rule keys on a single column; the new rule keys on both (composite).
	// ClassifyUpdate must return IntoOnly because only Into.Key changed.
	oldRule, err := lens.Parse([]byte(`
id: hot-rule
team: hot-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id, a.name AS name
into:
  target: postgres
  dsn: postgres://localhost/test
  table: old_table
  key: agreement_id
`))
	require.NoError(t, err)

	newRule, err := lens.Parse([]byte(`
id: hot-rule
team: hot-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id, a.name AS name
into:
  target: postgres
  dsn: postgres://localhost/test
  table: old_table
  key:
    - agreement_id
    - name
`))
	require.NoError(t, err)

	assert.Equal(t, lens.IntoOnly, lens.ClassifyUpdate(oldRule, newRule),
		"same match + single → composite key change must be IntoOnly")
}

func TestClassifyUpdate_MatchChanged(t *testing.T) {
	old := baseRule()
	newRule, err := lens.Parse([]byte(`
id: hot-rule
team: hot-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id, a.name AS name
into:
  target: postgres
  dsn: postgres://localhost/test
  table: old_table
  key: agreement_id
`))
	require.NoError(t, err)

	assert.Equal(t, lens.MatchChange, lens.ClassifyUpdate(old, newRule),
		"different MATCH must be MatchChange")
}

func TestClassifyUpdate_BothMatchAndIntoChanged(t *testing.T) {
	old := baseRule()
	newRule, err := lens.Parse([]byte(`
id: hot-rule
team: hot-team
match: MATCH (a:agreement) RETURN a.id AS agreement_id, a.name AS name
into:
  target: postgres
  dsn: postgres://localhost/test
  table: new_table
  key: agreement_id
`))
	require.NoError(t, err)

	assert.Equal(t, lens.MatchChange, lens.ClassifyUpdate(old, newRule),
		"when both Match and Into change, result must be MatchChange")
}

func TestClassifyUpdate_IdenticalRule(t *testing.T) {
	old := baseRule()
	same := baseRule()

	assert.Equal(t, lens.IntoOnly, lens.ClassifyUpdate(old, same),
		"identical rules must be IntoOnly (no change)")
}

func TestClassifyUpdate_NilPanics(t *testing.T) {
	r := baseRule()
	require.Panics(t, func() { lens.ClassifyUpdate(nil, r) }, "nil old must panic")
	require.Panics(t, func() { lens.ClassifyUpdate(r, nil) }, "nil new must panic")
}
