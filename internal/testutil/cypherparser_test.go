package testutil

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// FullCypherParser must thread Columns through unchanged from the same parse
// full.SpecLabels itself produces — the producer-equality pin (a threaded
// value is asserted equal to its source at a producer, not merely non-empty).
func TestFullCypherParser_ColumnsEqualsFullSpecLabels(t *testing.T) {
	body := "MATCH (i:identity) RETURN i.name AS displayName, i.key"

	want, err := full.SpecLabels(body)
	require.NoError(t, err)

	got, err := FullCypherParser{}.Parse(body)
	require.NoError(t, err)

	require.Equal(t, want.Columns, got.Columns)
	require.Equal(t, []string{"displayName", "key"}, got.Columns)
}
