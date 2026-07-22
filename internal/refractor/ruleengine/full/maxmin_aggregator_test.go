package full

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// reduceExtreme is the pure max()/min() fold. It must drop nulls, fold empty /
// all-null input to null, order numbers numerically and strings lexicographically
// (so canonical-UTC RFC3339 timestamps reduce chronologically).
func TestReduceExtreme(t *testing.T) {
	type tc struct {
		name   string
		op     string
		inputs []any
		want   any
	}
	cases := []tc{
		{"max strings rfc3339", ">", []any{"2026-06-01T00:00:00Z", "2026-06-09T00:00:00Z", "2026-06-03T00:00:00Z"}, "2026-06-09T00:00:00Z"},
		{"min strings rfc3339", "<", []any{"2026-06-01T00:00:00Z", "2026-06-09T00:00:00Z", "2026-06-03T00:00:00Z"}, "2026-06-01T00:00:00Z"},
		{"max numbers", ">", []any{int64(3), int64(7), int64(1)}, int64(7)},
		{"min numbers mixed int/float", "<", []any{int64(3), 1.5, int64(7)}, 1.5},
		{"nulls dropped", ">", []any{nil, "2026-06-02T00:00:00Z", nil, "2026-06-05T00:00:00Z"}, "2026-06-05T00:00:00Z"},
		{"single value", ">", []any{"2026-06-02T00:00:00Z"}, "2026-06-02T00:00:00Z"},
		{"all null folds to null", ">", []any{nil, nil}, nil},
		{"empty folds to null", "<", nil, nil},
		// Incomparable types (compareAny can't order string vs number) → no swap,
		// the first non-null wins. Documents the intentional fold behavior; the
		// lease lens never feeds mixed types (validUntil is always a string).
		{"incomparable keeps first", ">", []any{"2026-06-02T00:00:00Z", int64(5)}, "2026-06-02T00:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := reduceExtreme(c.op, c.inputs)
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
}

// TestExec_WithMaxMinAggregator drives max()/min() through the real WITH-clause
// grouping path (projectItems → finalizeAggregator), mirroring the lease-signing
// freshUntil reduction: one anchor with several neighbours folds to a single row
// carrying the chronological extreme, never one row per neighbour.
func TestExec_WithMaxMinAggregator(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)
	// Three completed checks "providedTo" alice with distinct validUntil stamps —
	// the multi-bgcheck shape that previously row-expanded the anchor.
	putVertex(t, reg, coreKV, "c1", "service", map[string]any{"validUntil": "2026-06-01T00:00:00Z"})
	putVertex(t, reg, coreKV, "c2", "service", map[string]any{"validUntil": "2026-06-09T00:00:00Z"})
	putVertex(t, reg, coreKV, "c3", "service", map[string]any{"validUntil": "2026-06-03T00:00:00Z"})
	putEdge(t, reg, adjKV, "providedTo", "c1", "alice")
	putEdge(t, reg, adjKV, "providedTo", "c2", "alice")
	putEdge(t, reg, adjKV, "providedTo", "c3", "alice")

	results := parseExec(t,
		`MATCH (i:identity {key: $k})<-[:providedTo]-(s:service)
		 WITH i.key AS who,
		      max(s.validUntil) AS latest,
		      min(s.validUntil) AS earliest,
		      count(DISTINCT s.key) AS n
		 RETURN who, latest, earliest, n`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1, "max/min must aggregate to one row per anchor, not one per neighbour")
	require.Equal(t, "2026-06-09T00:00:00Z", results[0].Values["latest"])
	require.Equal(t, "2026-06-01T00:00:00Z", results[0].Values["earliest"])
	require.Equal(t, int64(3), results[0].Values["n"])
}

// TestExec_MaxMinArityError confirms a multi-arg max()/min() is a loud query
// error, not a silent "use the first arg" (max/min are unary aggregators).
func TestExec_MaxMinArityError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})

	eng := New()
	cr, err := eng.Parse(`MATCH (i:identity {key: $k}) RETURN max(i.name, i.key) AS bad`)
	require.NoError(t, err)
	_, err = eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV)
	require.Error(t, err, "max with 2 args must error, not silently drop the second")
	require.Contains(t, err.Error(), "takes exactly 1 argument")
}

// TestExec_MaxNoMatchIsNull confirms max() over zero matched neighbours folds to
// a genuine null (the "no fresh bgcheck → freshUntil null" path that lets Weaver
// clear a standing @at), with the anchor preserved via OPTIONAL MATCH.
func TestExec_MaxNoMatchIsNull(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)

	results := parseExec(t,
		`MATCH (i:identity {key: $k})
		 OPTIONAL MATCH (i)<-[:providedTo]-(s:service)
		 WITH i.key AS who, max(s.validUntil) AS latest
		 RETURN who, latest`,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV,
	)
	require.Len(t, results, 1)
	require.Equal(t, vtxKey(reg, "alice"), results[0].Values["who"])
	require.Nil(t, results[0].Values["latest"])
}

// TestExec_RequiredMatchZeroRowsWithAggregateProjectsZeroRows confirms that an
// unanchored REQUIRED MATCH binding zero rows (a class-scan finding no
// vertex, e.g. a fresh stack with no leaseapp yet) projects zero output rows
// through an aggregating WITH — not a synthetic all-null row. Unlike
// TestExec_MaxNoMatchIsNull (an OPTIONAL MATCH neighbor set empty for an
// anchor that DID match, correctly folding to one row with a null aggregate),
// here the anchor itself never matched: there is nothing to attach an
// aggregate to. The fixture only seeds an unrelated identity vertex, so the
// `unit` class scan is genuinely empty.
func TestExec_RequiredMatchZeroRowsWithAggregateProjectsZeroRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)

	results := parseExec(t,
		`MATCH (u:unit)
		 OPTIONAL MATCH (u)<-[:providedTo]-(s:service)
		 WITH u.key AS unitKey, max(s.validUntil) AS latest
		 RETURN unitKey, latest`,
		ruleengine.EventContext{Parameters: map[string]any{}},
		adjKV, coreKV,
	)
	require.Empty(t, results, "a required MATCH binding zero rows must project zero rows, not a phantom null row")
}

// TestExec_RequiredMatchZeroRowsWithAggregateInReturnProjectsZeroRows is the
// applyReturn call site of the same fix: an aggregate directly in RETURN
// (no intermediate WITH) over a zero-row required MATCH must also project
// zero rows.
func TestExec_RequiredMatchZeroRowsWithAggregateInReturnProjectsZeroRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", nil)

	results := parseExec(t,
		`MATCH (u:unit) RETURN u.key AS unitKey, count(u.key) AS n`,
		ruleengine.EventContext{Parameters: map[string]any{}},
		adjKV, coreKV,
	)
	require.Empty(t, results, "a required MATCH binding zero rows must project zero rows via RETURN too")
}
