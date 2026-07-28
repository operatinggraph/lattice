package identityhygiene

// Rule-engine proof of the re-authored duplicateCandidates lens
// (dedup-over-encrypted-pii-design.md §3.3/§7) — drives the spec through the
// `full` rule engine directly (the engine selected at activation via
// engine:"full") against an embedded NATS Core/Adjacency KV, the same
// harness clinic-domain / lease-signing use for their lens cypher tests.
//
// What it proves the unit/structure tests cannot:
//   - a flagged pair (a live duplicateOf link between two identities)
//     projects with the declared IntoKey shape (primaryId/secondaryId,
//     bare NanoIDs);
//   - an unflagged identity pair (no duplicateOf link) does not project —
//     this is a link-traversal match, not a graph scan;
//   - the state filter, spelled `a.state.data.value = '…' OR …` (not the
//     `IN` form the old spec silently dropped, §1.1-3), actually filters: a
//     merged identity on either side of the pair excludes the row;
//   - the lens passes ValidateKeyColumns with the explicit IntoKey — the
//     exact activation-time gate the old spec (default `["key"]` columns)
//     died on.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type dedupFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
}

func newDedupFixture(t *testing.T) *dedupFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &dedupFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}}
}

// identity creates an identity vertex + its state aspect.
func (f *dedupFixture) identity(t *testing.T, name, state string) string {
	t.Helper()
	ctx := context.Background()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	key := "vtx.identity." + id
	body := map[string]any{"key": key, "class": "identity", "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(ctx, key, raw)
	require.NoError(t, err)
	stateKey := key + ".state"
	stateBody := map[string]any{"key": stateKey, "class": "state", "vertexKey": key, "localName": "state",
		"isDeleted": false, "data": map[string]any{"value": state}}
	stateRaw, _ := json.Marshal(stateBody)
	_, err = f.coreKV.Put(ctx, stateKey, stateRaw)
	require.NoError(t, err)
	return key
}

// duplicateOf creates the durable pair-evidence link: fromName duplicateOf toName.
func (f *dedupFixture) duplicateOf(t *testing.T, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	linkKey := "lnk.identity." + fromID + ".duplicateOf.identity." + toID
	edgeID := "duplicateOf_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: "duplicateOf", Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: "identity"}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: "duplicateOf", Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: "identity"}))
}

func (f *dedupFixture) project(t *testing.T, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "duplicateCandidates cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now": now, "projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func TestDuplicateCandidatesLens_ProjectsOnlyFlaggedPairs(t *testing.T) {
	f := newDedupFixture(t)

	f.identity(t, "incumbent", "unclaimed")
	f.identity(t, "newcomer", "unclaimed")
	f.duplicateOf(t, "newcomer", "incumbent")

	// Control: two identities with no duplicateOf link between them.
	f.identity(t, "unrelatedA", "unclaimed")
	f.identity(t, "unrelatedB", "claimed")

	rows := f.project(t, duplicateCandidatesSpec)
	require.Len(t, rows, 1, "only the flagged pair may project; got %v", rows)

	row := rows[0].Values
	require.Equal(t, lenstest.NanoID("incumbent"), row["primaryId"])
	require.Equal(t, lenstest.NanoID("newcomer"), row["secondaryId"])
	require.Equal(t, "vtx.identity."+lenstest.NanoID("incumbent"), row["primaryKey"])
	require.Equal(t, "vtx.identity."+lenstest.NanoID("newcomer"), row["secondaryKey"])
}

func TestDuplicateCandidatesLens_StateFilterExcludesMergedEitherSide(t *testing.T) {
	f := newDedupFixture(t)

	f.identity(t, "mergedIncumbent", "merged")
	f.identity(t, "newcomer1", "unclaimed")
	f.duplicateOf(t, "newcomer1", "mergedIncumbent")

	f.identity(t, "incumbent2", "claimed")
	f.identity(t, "mergedNewcomer", "merged")
	f.duplicateOf(t, "mergedNewcomer", "incumbent2")

	rows := f.project(t, duplicateCandidatesSpec)
	require.Empty(t, rows, "a merged identity on either side of the pair must exclude the row; got %v", rows)
}

func TestDuplicateCandidatesLens_ActivatesWithExplicitIntoKey(t *testing.T) {
	eng := full.New()
	compiled, err := eng.Parse(duplicateCandidatesSpec)
	require.NoError(t, err)
	cr, ok := compiled.(*full.CompiledRule)
	require.True(t, ok)
	cr.KeyColumns = []string{"primaryId", "secondaryId"}
	require.NoError(t, cr.ValidateKeyColumns(),
		"the lens must activate against its declared IntoKey — the exact gate the old default-[\"key\"] spec died on")
}
