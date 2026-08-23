// Contract #6 §6.14 byte-shape conformance test for the base read-path
// authorization lens (cap-read.<actor>.<anchorId>, D1, per-anchor keys —
// cap-read-per-anchor-grant-keys-design.md §3.1/§6).
//
// Mirrors TestCapabilityLens_ContractConformance: it runs the LITERAL
// CapabilityReadLensDefinition().CypherRule from the bootstrap package against a
// deterministically seeded graph and wraps the RETURN row through the lens's
// §6.13 Output descriptor's per-entry envelope, so the assertion targets the
// on-wire cap-read.<actor>.<anchorId> key — catching schema drift if the
// cypher or the §6.14 contract shape changes without the other.
//
// Unlike the write-path base capability lens (which projects only protected
// kernel identities), the read base projects the SELF anchor for EVERY actor —
// self-read is the universal, package-independent primordial grant. The fixture
// therefore seeds an ORDINARY (non-protected) identity and asserts it still
// gets a cap-read key.
package full_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// capabilityReadDescriptor builds the compiled §6.13 Output descriptor for the
// base cap-read lens, so the contract test wraps each RETURN row through the
// same data-driven envelope the live pipeline uses.
func capabilityReadDescriptor(t *testing.T) projection.OutputDescriptor {
	t.Helper()
	o := bootstrap.CapabilityReadLensDefinition().Output
	require.NotNil(t, o, "cap-read lens must declare an Output descriptor")
	desc, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:         o.AnchorType,
		OutputKeyPattern:   o.OutputKeyPattern,
		BodyColumns:        o.BodyColumns,
		EmptyBehavior:      o.EmptyBehavior,
		RealnessFilter:     o.RealnessFilter,
		Freshness:          o.Freshness,
		ActorField:         o.ActorField,
		Lanes:              o.Lanes,
		EntryKeyColumn:     o.EntryKeyColumn,
		StaticEmptyColumns: o.StaticEmptyColumns,
	})
	require.NoError(t, err)
	return desc
}

func TestCapabilityReadLens_ContractConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	adjKV, coreKV := contractStartKVs(t)

	// An ORDINARY (non-protected) identity — self-read must apply to every
	// actor, not just root system actors.
	aliceKey := contractPutVertex(t, coreKV, "identity", "alice",
		map[string]any{"name": "alice"})

	// --- run the LITERAL bootstrap cypher ---
	body := bootstrap.CapabilityReadLensDefinition().CypherRule
	eng := full.New()
	cr, err := eng.Parse(body)
	require.NoError(t, err, "literal cap-read cypher must parse")

	now := time.Now().Unix()
	projectedAt := time.Now().UTC().Format(time.RFC3339)
	params := map[string]any{
		"actorKey":    aliceKey,
		"now":         float64(now),
		"projectedAt": projectedAt,
	}
	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: params}, adjKV, coreKV)
	require.NoError(t, err, "literal cap-read cypher must execute")
	require.Len(t, out, 1, "cap-read query should produce exactly one row")
	row := out[0].Values

	// --- wrap through the production per-entry envelope (§3.3/§4.1) ---
	desc := capabilityReadDescriptor(t)
	require.Equal(t, "anchorId", desc.EntryKeyColumn, "the base lens must set entryKeyColumn (§6 flip)")
	entries, entryErr := desc.EntryEnvelopeFn()(row, nil, params)
	require.NoError(t, entryErr, "per-entry envelope wrapping must succeed")
	require.Lenf(t, entries, 1, "base lens projects exactly one per-anchor key (the self anchor); got %v", entries)
	envRow := entries[0].Row
	envKeys := entries[0].Keys

	aliceNanoID := strings.TrimPrefix(aliceKey, "vtx.identity.")
	require.NotEqual(t, aliceKey, aliceNanoID, "fixture sanity: actor key must carry the vtx.identity. prefix")

	// --- §3.1/§3.2 field-by-field assertions ---

	// `key`: "cap-read.identity.<actorNanoID>.<anchorId>" — the self anchor's
	// anchorId equals the actor's own bare NanoID, so it appears twice.
	wantKey := "cap-read.identity." + aliceNanoID + "." + aliceNanoID
	keyVal, ok := envRow["key"].(string)
	require.True(t, ok, "entry.key must be a string")
	require.Equal(t, wantKey, keyVal, "entry.key must be cap-read.identity.<actorNanoID>.<anchorId>")
	require.Equal(t, keyVal, envKeys["key"], "Keys map must mirror entry.key")

	// `actor`: the full actor vertex key passed in $actorKey.
	require.Equalf(t, aliceKey, envRow["actor"],
		"entry.actor must equal $actorKey; got %v", envRow["actor"])

	// `version`: "1.0" (Phase 1 pin, read-path mirror of §6.3).
	require.Equal(t, "1.0", envRow["version"], "entry.version must be '1.0'")

	// `projectedAt`: the params projectedAt (RFC3339 string).
	pa, ok := envRow["projectedAt"].(string)
	require.True(t, ok, "entry.projectedAt must be a string")
	require.Equalf(t, projectedAt, pa, "entry.projectedAt must equal params.projectedAt")

	// §3.2: no projectedFromRevisions (aggregate-only provenance) and no
	// readableAnchors wrapper — the entry's own fields (anchorType, via) land
	// at the body's top level instead.
	require.NotContains(t, envRow, "projectedFromRevisions", "a per-anchor entry must not carry aggregate-only provenance")
	require.NotContains(t, envRow, "readableAnchors", "a per-anchor entry's own fields land at the top level, not nested")

	// `anchorType`/`via`: the self anchor —
	// {anchorType:'identity', anchorId:<actor bare NanoID>, via:['self']} (§6.14).
	// anchorId is the §6.14 opaque-match-token rep: the bare NanoID extracted from
	// the actor's vertex key by the auth-plane engine's nanoIdFromKey function; it
	// lives in the key here, not the body (§3.2).
	require.Equal(t, "identity", envRow["anchorType"], "self anchor anchorType must be 'identity'")
	via, ok := envRow["via"].([]any)
	require.Truef(t, ok, "self anchor via must be a list; got %T", envRow["via"])
	require.Len(t, via, 1, "self anchor via must be ['self']")
	require.Equal(t, "self", via[0], "self anchor via must be ['self']")
}
