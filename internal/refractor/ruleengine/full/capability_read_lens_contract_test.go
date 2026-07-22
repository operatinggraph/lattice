// Contract #6 §6.14 byte-shape conformance test for the base read-path
// authorization lens (cap-read.<actor>, D1).
//
// Mirrors TestCapabilityLens_ContractConformance: it runs the LITERAL
// CapabilityReadLensDefinition().CypherRule from the bootstrap package against a
// deterministically seeded graph and wraps the RETURN row through the lens's
// §6.13 Output descriptor envelope, so the assertion targets the on-wire
// cap-read.<actor> document — catching schema drift if the cypher or the §6.14
// contract shape changes without the other.
//
// Unlike the write-path base capability lens (which projects only protected
// kernel identities), the read base projects the SELF anchor for EVERY actor —
// self-read is the universal, package-independent primordial grant. The fixture
// therefore seeds an ORDINARY (non-protected) identity and asserts it still
// gets a cap-read doc.
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
	// actor, not just kernel-seeded ones.
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
	keys := out[0].Key

	// --- wrap through the production envelope (Contract #6 §6.14) ---
	wrapper := capabilityReadDescriptor(t).EnvelopeFn("vtx.meta.test-capread-lens",
		func(k string) uint64 { return 42 })
	envRow, envKeys, envErr := wrapper(row, keys, params)
	require.NoError(t, envErr, "envelope wrapping must succeed")
	require.NotNil(t, envRow)
	require.NotNil(t, envKeys)

	// --- Contract #6 §6.14 field-by-field assertions ---

	// `key`: "cap-read.identity.<NanoID>" derived from the actor vertex key.
	keyVal, ok := envRow["key"].(string)
	require.True(t, ok, "envelope.key must be a string")
	require.Equalf(t, "cap-read.identity.", keyVal[:len("cap-read.identity.")],
		"envelope.key must start with 'cap-read.identity.'; got %q", keyVal)
	require.Truef(t, len(keyVal) > len("cap-read.identity."),
		"envelope.key must include actor NanoID; got %q", keyVal)
	require.Equal(t, keyVal, envKeys["key"], "Keys map must mirror envelope.key")

	// `actor`: the full actor vertex key passed in $actorKey.
	require.Equalf(t, aliceKey, envRow["actor"],
		"envelope.actor must equal $actorKey; got %v", envRow["actor"])

	// `version`: "1.0" (Phase 1 pin, read-path mirror of §6.3).
	require.Equal(t, "1.0", envRow["version"], "envelope.version must be '1.0'")

	// `projectedAt`: the params projectedAt (RFC3339 string).
	pa, ok := envRow["projectedAt"].(string)
	require.True(t, ok, "envelope.projectedAt must be a string")
	require.Equalf(t, projectedAt, pa, "envelope.projectedAt must equal params.projectedAt")

	// `projectedFromRevisions`: a revision map including the anchor + lens-def.
	revs, ok := envRow["projectedFromRevisions"].(map[string]uint64)
	require.True(t, ok, "envelope.projectedFromRevisions must be a map[string]uint64")
	require.Containsf(t, revs, aliceKey,
		"projectedFromRevisions must include anchor revision; got %v", revs)
	require.Containsf(t, revs, "vtx.meta.test-capread-lens",
		"projectedFromRevisions must include lens-def revision; got %v", revs)

	// `lanes`: non-empty, includes "default".
	switch lanes := envRow["lanes"].(type) {
	case []string:
		require.NotEmpty(t, lanes, "envelope.lanes must not be empty")
		require.Equal(t, "default", lanes[0])
	case []any:
		require.NotEmpty(t, lanes, "envelope.lanes must not be empty")
		require.Equal(t, "default", lanes[0])
	default:
		t.Fatalf("envelope.lanes must be a string array; got %T", envRow["lanes"])
	}

	// `readableAnchors`: exactly the self anchor for this actor —
	// {anchorType:'identity', anchorId:<actor bare NanoID>, via:['self']} (§6.14).
	// anchorId is the §6.14 opaque-match-token rep: the bare NanoID extracted from
	// the actor's vertex key by the auth-plane engine's nanoIdFromKey function.
	anchorsRaw := envRow["readableAnchors"]
	anchors, ok := anchorsRaw.([]any)
	require.Truef(t, ok, "envelope.readableAnchors must be a list; got %T", anchorsRaw)
	require.Lenf(t, anchors, 1, "base lens projects exactly the self anchor; got %v", anchors)

	anchor, ok := anchors[0].(map[string]any)
	require.True(t, ok, "readableAnchors entry must be an object")
	require.Equal(t, "identity", anchor["anchorType"], "self anchor anchorType must be 'identity'")
	aliceNanoID := strings.TrimPrefix(aliceKey, "vtx.identity.")
	require.NotEqual(t, aliceKey, aliceNanoID, "fixture sanity: actor key must carry the vtx.identity. prefix")
	require.Equalf(t, aliceNanoID, anchor["anchorId"],
		"self anchor anchorId must be the actor's bare NanoID (nanoIdFromKey); got %v", anchor["anchorId"])

	via, ok := anchor["via"].([]any)
	require.Truef(t, ok, "self anchor via must be a list; got %T", anchor["via"])
	require.Len(t, via, 1, "self anchor via must be ['self']")
	require.Equal(t, "self", via[0], "self anchor via must be ['self']")
}
