package full

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The wellnessBookingChangeNotices shape (2026-09-18): a name walked off an
// OPTIONAL hop, templated by a Weaver target as a Params value, must reach
// the row non-null — coalesced, or read off the anchor, or guarded by the
// gap's own `<> null` conjunct.
const optionalHopSpec = `MATCH (b:booking {key: $actorKey})
OPTIONAL MATCH (b)-[:forSession]->(se:session)
OPTIONAL MATCH (se)-[:ledBy]->(i:instructor)
RETURN
  b.key AS entityKey,
  b.status.data.bookedAt AS bookedAt,
  se.key AS sessionKey,
  se.schedule.data.startsAt AS startsAt,
  i.profile.data.displayName AS instructorName,
  coalesce(i.profile.data.displayName, '') AS instructorNameSafe,
  coalesce(i.profile.data.displayName, null) AS instructorNameUnsafe,
  'fixed' AS literalCol,
  ((se.schedule.data.startsAt <> null) AND (b.status.data.value = 'booked')) AS missing_guarded,
  ((b.status.data.value = 'booked') OR (se.schedule.data.startsAt <> null)) AS missing_or,
  (NOT (se.key = null) AND (b.status.data.value = 'booked')) AS missing_not_eq,
  ((b.freshnessExpiry.data.byTarget.x >= se.schedule.data.remindAt) AND (b.status.data.value = 'booked')) AS missing_compared`

func TestOptionalHopColumns(t *testing.T) {
	cr, err := New().Parse(optionalHopSpec)
	require.NoError(t, err)
	full := cr.(*CompiledRule)
	cols, exhaustive := full.OptionalHopColumns()
	require.True(t, exhaustive)
	byAlias := map[string]OptionalHopColumn{}
	for _, c := range cols {
		byAlias[c.Alias] = c
	}
	require.Empty(t, byAlias["entityKey"].OptionalVars, "the anchor is required")
	require.Empty(t, byAlias["bookedAt"].OptionalVars)
	require.Equal(t, []string{"se"}, byAlias["sessionKey"].OptionalVars)
	require.Equal(t, []string{"se"}, byAlias["startsAt"].OptionalVars)
	require.Equal(t, []string{"i"}, byAlias["instructorName"].OptionalVars)
	require.False(t, byAlias["instructorName"].NullSafe)
	require.True(t, byAlias["instructorNameSafe"].NullSafe, "coalesce to a literal bounds the column")
	require.False(t, byAlias["instructorNameUnsafe"].NullSafe, "coalesce to null bounds nothing")
	require.True(t, byAlias["literalCol"].NullSafe)
	require.Empty(t, byAlias["literalCol"].OptionalVars)
}

func TestReturnColumnGuardsNonNull(t *testing.T) {
	cr, err := New().Parse(optionalHopSpec)
	require.NoError(t, err)
	full := cr.(*CompiledRule)
	require.True(t, full.ReturnColumnGuardsNonNull("missing_guarded", "startsAt"), "a top-level `<> null` conjunct over the column's exact expression")
	require.True(t, full.ReturnColumnGuardsNonNull("missing_guarded", "sessionKey"), "a non-null chain rooted at se binds se, so se.key is non-null")
	require.True(t, full.ReturnColumnGuardsNonNull("missing_compared", "sessionKey"), "an ordering comparison makes both operands non-null, binding se")
	require.False(t, full.ReturnColumnGuardsNonNull("missing_compared", "startsAt"), "remindAt non-null says nothing about startsAt")
	require.False(t, full.ReturnColumnGuardsNonNull("missing_or", "startsAt"), "an OR branch is not a guard")
	require.True(t, full.ReturnColumnGuardsNonNull("missing_not_eq", "sessionKey"), "NOT (x = null) is the same test")
	require.False(t, full.ReturnColumnGuardsNonNull("missing_guarded", "instructorName"))
	require.False(t, full.ReturnColumnGuardsNonNull("absent", "startsAt"))
}
