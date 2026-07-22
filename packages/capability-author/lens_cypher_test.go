package capabilityauthor

// Rule-engine proof of the two Fire-1-checkpoint P5 read-model lenses added
// alongside the escalation dispatch: capabilityProposals (the operator review
// surface) and capabilityAuthorContext (the installed-DDL self-description
// catalog). Drives the lens specs through the `full` rule engine directly
// against an embedded NATS Core KV, the same harness clinic-domain /
// lease-signing / objects-base use for their lens cypher tests.
//
// What these prove that the structural TestPackage_* tests cannot:
//   - capabilityProposals is one row per capabilityproposal vertex, every
//     aspect column null-safe (a request with no artifact yet projects
//     cleanly with null downstream columns).
//   - capabilityAuthorContext's `MATCH (m:meta)` label match is by the vertex
//     key TYPE segment, not the root `class` field — a DDL meta (class
//     meta.ddl.vertexType) and a non-DDL meta (class meta.lens) BOTH appear,
//     with the non-DDL row's self-description columns null.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func capAuthorCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	opts := &natsserver.Options{JetStream: true, StoreDir: jsstore.Dir(t), NoLog: true, NoSigs: true, Port: natsserver.RANDOM_PORT}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close(); s.Shutdown() })
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-capauth-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-capauth-cypher-test"})
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-capauth-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-capauth-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// putVertex writes a root vertex document with an explicit class (which may
// differ from the key's TYPE segment, as every meta-vertex class does).
func putVertex(t *testing.T, coreKV *substrate.KV, key, class string) {
	t.Helper()
	body := map[string]any{"key": key, "class": class, "isDeleted": false, "data": map[string]any{}}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func putAspect(t *testing.T, coreKV *substrate.KV, ownerKey, local, class string, data map[string]any) {
	t.Helper()
	key := ownerKey + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": ownerKey, "localName": local, "isDeleted": false, "data": data}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func projectCapAuthor(t *testing.T, adjKV, coreKV *substrate.KV, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "capability-author lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
	}}, adjKV, coreKV)
	require.NoError(t, err)
	return out
}

func rowByCapAuthorKey(rows []ruleengine.ProjectionResult, key string) map[string]any {
	for _, r := range rows {
		if r.Values["key"] == key {
			return r.Values
		}
	}
	return nil
}

// capAuthorNanoID returns a deterministic, Contract-#1-valid 20-char NanoID
// derived from a logical name (mirrors clinic-domain's lens_cypher_test.go
// cNanoID helper) — meta-vertex keys need a real NanoID, not an arbitrary
// string, since the full engine's seed scan classifies/parses every Core KV
// key through substrate.ParseVertexKey.
func capAuthorNanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte(name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

// TestCapabilityProposals_FullEpisodeProjects proves every aspect the capture
// pair can write surfaces on the review lens, one row per proposal.
func TestCapabilityProposals_FullEpisodeProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := capAuthorCypherKVs(t)
	key := "vtx.capabilityproposal.capPropOneHJKMNPQRST"
	putVertex(t, coreKV, key, "capabilityproposal")
	putAspect(t, coreKV, key, "request", "capabilityProposalRequest", map[string]any{
		"requesterId": "vtx.identity.op1", "intent": "a lens listing active providers", "contextRef": "ctx-1",
	})
	putAspect(t, coreKV, key, "claim", "capabilityProposalClaim", map[string]any{"claimedAt": "2026-07-04T10:00:00Z"})
	putAspect(t, coreKV, key, "artifact", "capabilityProposalArtifact", map[string]any{"kind": "lens", "content": "{...}"})
	putAspect(t, coreKV, key, "target", "capabilityProposalTarget", map[string]any{"mode": "newPackage", "packageName": "activeProvidersBySpecialty"})
	putAspect(t, coreKV, key, "rationale", "capabilityProposalRationale", map[string]any{"text": "no existing lens surfaces this"})
	putAspect(t, coreKV, key, "confidence", "capabilityProposalConfidence", map[string]any{"score": 0.86})
	putAspect(t, coreKV, key, "validation", "capabilityProposalValidation", map[string]any{"state": "valid", "checkedAt": "2026-07-04T10:00:01Z"})
	putAspect(t, coreKV, key, "provenance", "capabilityProposalProvenance", map[string]any{"model": "claude-opus-4-8", "reasonedAt": "2026-07-04T10:00:00Z"})
	putAspect(t, coreKV, key, "review", "capabilityProposalReview", map[string]any{"state": "pending"})

	rows := projectCapAuthor(t, adjKV, coreKV, capabilityProposalsSpec)
	require.Len(t, rows, 1)
	v := rowByCapAuthorKey(rows, key)
	require.NotNil(t, v)
	require.Equal(t, key, v["proposalKey"])
	require.Equal(t, "vtx.identity.op1", v["requesterId"])
	require.Equal(t, "a lens listing active providers", v["intent"])
	require.Equal(t, "lens", v["kind"])
	require.Equal(t, "activeProvidersBySpecialty", v["targetPackageName"])
	require.Equal(t, "no existing lens surfaces this", v["rationale"])
	require.Equal(t, 0.86, v["confidence"])
	require.Equal(t, "valid", v["validationState"])
	require.Equal(t, "claude-opus-4-8", v["model"])
	require.Equal(t, "pending", v["reviewState"])
}

// TestCapabilityProposals_ClaimInFlight_NullDownstreamColumns proves a
// request with only the write-ahead .request aspect (reasoning still in
// flight — no .claim/.artifact/.review yet) projects cleanly with null
// downstream columns, never erroring.
func TestCapabilityProposals_ClaimInFlight_NullDownstreamColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := capAuthorCypherKVs(t)
	key := "vtx.capabilityproposal.capPropTwoHJKMNPQRST"
	putVertex(t, coreKV, key, "capabilityproposal")
	putAspect(t, coreKV, key, "request", "capabilityProposalRequest", map[string]any{
		"requesterId": "vtx.identity.op1", "intent": "a grant for the ops role",
	})

	rows := projectCapAuthor(t, adjKV, coreKV, capabilityProposalsSpec)
	require.Len(t, rows, 1)
	v := rowByCapAuthorKey(rows, key)
	require.NotNil(t, v)
	require.Equal(t, "a grant for the ops role", v["intent"])
	require.Nil(t, v["claimedAt"], "no .claim aspect yet → null (reasoning not yet dispatched)")
	require.Nil(t, v["kind"], "no .artifact aspect yet → null (reasoning in flight)")
	require.Nil(t, v["reviewState"], "no .review aspect yet → null (never authored)")
}

// TestCapabilityAuthorContext_DDLAndNonDDLMetaBothProject proves the
// MATCH (m:meta) label match is by the vertex key TYPE segment (not the root
// class field, which varies per meta kind): a DDL meta-vertex (class
// meta.ddl.vertexType, carrying the five self-description aspects) and a
// non-DDL meta-vertex (class meta.lens, no self-description aspects) both
// appear as rows, with the non-DDL row's self-description columns null.
func TestCapabilityAuthorContext_DDLAndNonDDLMetaBothProject(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := capAuthorCypherKVs(t)

	ddlKey := "vtx.meta." + capAuthorNanoID("capability-proposal-ddl")
	putVertex(t, coreKV, ddlKey, "meta.ddl.vertexType")
	putAspect(t, coreKV, ddlKey, "canonicalName", "canonicalName", map[string]any{"value": "capabilityproposal"})
	putAspect(t, coreKV, ddlKey, "description", "description", map[string]any{"text": "AI-authored capability proposal DDL."})
	putAspect(t, coreKV, ddlKey, "permittedCommands", "permittedCommands", map[string]any{"commands": []any{"RequestCapabilityAuthoring", "RecordCapabilityProposal"}})
	putAspect(t, coreKV, ddlKey, "inputSchema", "inputSchema", map[string]any{"schema": `{"type":"object"}`})
	putAspect(t, coreKV, ddlKey, "outputSchema", "outputSchema", map[string]any{"schema": `{"type":"object"}`})
	putAspect(t, coreKV, ddlKey, "fieldDescription", "fieldDescription", map[string]any{"fieldDescriptions": map[string]any{"intent": "the plain-language request"}})
	putAspect(t, coreKV, ddlKey, "examples", "examples", map[string]any{"examples": []any{map[string]any{"name": "basic"}}})

	lensKey := "vtx.meta." + capAuthorNanoID("capability-proposals-lens")
	putVertex(t, coreKV, lensKey, "meta.lens")
	putAspect(t, coreKV, lensKey, "canonicalName", "canonicalName", map[string]any{"value": "capabilityProposals"})
	putAspect(t, coreKV, lensKey, "description", "description", map[string]any{"text": "The operator review lens."})

	rows := projectCapAuthor(t, adjKV, coreKV, capabilityAuthorContextSpec)
	require.Len(t, rows, 2)

	ddlRow := rowByCapAuthorKey(rows, ddlKey)
	require.NotNil(t, ddlRow)
	require.Equal(t, "meta.ddl.vertexType", ddlRow["class"])
	require.Equal(t, "capabilityproposal", ddlRow["canonicalName"])
	require.Equal(t, []any{"RequestCapabilityAuthoring", "RecordCapabilityProposal"}, ddlRow["permittedCommands"])
	require.Equal(t, `{"type":"object"}`, ddlRow["inputSchema"])

	lensRow := rowByCapAuthorKey(rows, lensKey)
	require.NotNil(t, lensRow)
	require.Equal(t, "meta.lens", lensRow["class"])
	require.Equal(t, "capabilityProposals", lensRow["canonicalName"])
	require.Nil(t, lensRow["permittedCommands"], "non-DDL meta has no permittedCommands aspect → null")
	require.Nil(t, lensRow["inputSchema"], "non-DDL meta has no inputSchema aspect → null")
}
