package capabilityauthor

// Rule-engine proof of this package's P5 read-model lenses: capabilityProposals
// (the operator review surface), capabilityAuthorContext (the installed-DDL
// self-description catalog) and capabilityAuthorPackages (the installed-manifest
// scan). Drives the lens specs through the `full` rule engine directly against
// an embedded NATS Core KV, the same harness clinic-domain / lease-signing /
// objects-base use for their lens cypher tests.
//
// What these prove that the structural TestPackage_* tests cannot:
//   - capabilityProposals is one row per capabilityproposal vertex, every
//     aspect column null-safe (a request with no artifact yet projects
//     cleanly with null downstream columns).
//   - capabilityAuthorContext's `MATCH (m:meta)` label match is by the vertex
//     key TYPE segment, not the root `class` field — a DDL meta (class
//     meta.ddl.vertexType) and a non-DDL meta (class meta.lens) BOTH appear,
//     with the non-DDL row's self-description columns null.
//   - capabilityAuthorPackages projects declaredKeys as a LIST the reader can
//     scan for ownership, and a package whose manifest aspect is absent
//     projects null columns rather than dropping out.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

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

// projectCapAuthorAnchored drives a SELF-ANCHORED lens (one that binds
// $actorKey) against a single anchor, the shape the actorAggregate projection
// kind executes with.
func projectCapAuthorAnchored(t *testing.T, adjKV, coreKV *substrate.KV, spec, actorKey string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "capability-author lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
		"actorKey":    actorKey,
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

// TestCapabilityAuthorPending_OpensOnlyForAnUnauthoredProposal pins the
// escalation-dispatch gap predicate on BOTH of its arms.
//
// The gap must open for the AI lane's write-ahead request (nothing authored
// yet, no claim minted) and must NOT open for a proposal that already carries
// an artifact. The second case is the human lane: SubmitCapabilityProposal
// mints .request + .artifact and deliberately no .claim, so a claim-only
// predicate would read it as "reasoning not yet dispatched" and fire the
// capabilityAuthor Loom pattern at it — an unrequested reasoning call whose
// RecordCapabilityProposal reply could never commit against the create-only
// aspects the submit already wrote.
func TestCapabilityAuthorPending_OpensOnlyForAnUnauthoredProposal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	// AI lane, pre-dispatch: .request only → the gap is open.
	adjKV, coreKV := lenstest.KVs(t)
	awaiting := "vtx.capabilityproposal.capPropAwaitHJKMNPQR"
	putVertex(t, coreKV, awaiting, "capabilityproposal")
	putAspect(t, coreKV, awaiting, "request", "capabilityProposalRequest", map[string]any{
		"requesterId": "vtx.identity.op1", "intent": "a lens listing active providers",
	})
	rows := projectCapAuthorAnchored(t, adjKV, coreKV, capabilityAuthorPendingSpec, awaiting)
	require.Len(t, rows, 1)
	v := rows[0].Values // the dispatch lens echoes its anchor as entityKey, not key
	require.Equal(t, awaiting, v["entityKey"])
	require.Equal(t, true, v["violating"], "no claim and no artifact → the authoring gap is open")
	require.Equal(t, true, v["missing_authoring"])

	// AI lane, dispatched: .claim present → the gap closes.
	adjKV2, coreKV2 := lenstest.KVs(t)
	claimed := "vtx.capabilityproposal.capPropDsptHJKMNPQRS"
	putVertex(t, coreKV2, claimed, "capabilityproposal")
	putAspect(t, coreKV2, claimed, "request", "capabilityProposalRequest", map[string]any{"requesterId": "vtx.identity.op1"})
	putAspect(t, coreKV2, claimed, "claim", "capabilityProposalClaim", map[string]any{"claimedAt": "2026-08-02T10:00:00Z"})
	rows2 := projectCapAuthorAnchored(t, adjKV2, coreKV2, capabilityAuthorPendingSpec, claimed)
	require.Len(t, rows2, 1)
	require.Equal(t, false, rows2[0].Values["violating"], "a minted claim closes the gap")

	// HUMAN lane: .request + .artifact, no claim → the gap must stay SHUT.
	adjKV3, coreKV3 := lenstest.KVs(t)
	submitted := "vtx.capabilityproposal.capPropHumanHJKMNPQR"
	putVertex(t, coreKV3, submitted, "capabilityproposal")
	putAspect(t, coreKV3, submitted, "request", "capabilityProposalRequest", map[string]any{"requesterId": "vtx.identity.op1"})
	putAspect(t, coreKV3, submitted, "artifact", "capabilityProposalArtifact", map[string]any{"kind": "lens", "content": "{...}"})
	putAspect(t, coreKV3, submitted, "provenance", "capabilityProposalProvenance", map[string]any{"source": "operator"})
	rows3 := projectCapAuthorAnchored(t, adjKV3, coreKV3, capabilityAuthorPendingSpec, submitted)
	require.Len(t, rows3, 1)
	v3 := rows3[0].Values
	require.Equal(t, false, v3["violating"],
		"an operator-authored proposal has no authoring gap — it must never trigger the AI dispatch")
	require.Equal(t, false, v3["missing_authoring"])
}

// TestCapabilityProposals_FullEpisodeProjects proves every aspect the capture
// pair can write surfaces on the review lens, one row per proposal.
func TestCapabilityProposals_FullEpisodeProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := lenstest.KVs(t)
	key := "vtx.capabilityproposal.capProponeHJKMNPQRST"
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
	putAspect(t, coreKV, key, "provenance", "capabilityProposalProvenance", map[string]any{"source": "ai", "model": "claude-opus-4-8", "reasonedAt": "2026-07-04T10:00:00Z"})
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
	require.Equal(t, "ai", v["source"])
	require.Equal(t, "pending", v["reviewState"])
}

// TestCapabilityProposals_OperatorSourceProjects proves the review queue can
// badge an operator-authored proposal from a DECLARED column rather than
// inferring origin from the absence of model-shaped provenance: a proposal
// SubmitCapabilityProposal wrote carries source='operator' with every model
// field empty, and the -1.0 confidence sentinel (no model scored it) projects
// as itself rather than as a plausible-looking score.
func TestCapabilityProposals_OperatorSourceProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := lenstest.KVs(t)
	key := "vtx.capabilityproposal.capPropFourHJKMNPQRS"
	putVertex(t, coreKV, key, "capabilityproposal")
	putAspect(t, coreKV, key, "request", "capabilityProposalRequest", map[string]any{
		"requesterId": "vtx.identity.op1", "intent": "a specialty cut of the on-call roster", "contextRef": "",
	})
	putAspect(t, coreKV, key, "artifact", "capabilityProposalArtifact", map[string]any{"kind": "lens", "content": "{...}"})
	putAspect(t, coreKV, key, "confidence", "capabilityProposalConfidence", map[string]any{"score": -1.0})
	putAspect(t, coreKV, key, "validation", "capabilityProposalValidation", map[string]any{"state": "valid"})
	putAspect(t, coreKV, key, "provenance", "capabilityProposalProvenance", map[string]any{
		"source": "operator", "model": "", "promptHash": "", "catalogHash": "", "reasonedAt": "",
	})
	putAspect(t, coreKV, key, "review", "capabilityProposalReview", map[string]any{"state": "pending"})

	rows := projectCapAuthor(t, adjKV, coreKV, capabilityProposalsSpec)
	require.Len(t, rows, 1)
	v := rowByCapAuthorKey(rows, key)
	require.NotNil(t, v)
	require.Equal(t, "operator", v["source"])
	require.Equal(t, "", v["model"], "no model authored this proposal")
	require.Equal(t, -1.0, v["confidence"], "the absent-confidence sentinel, not a score")
	require.Nil(t, v["claimedAt"], "a directly-submitted proposal has no authoring claim")
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
	adjKV, coreKV := lenstest.KVs(t)
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
	adjKV, coreKV := lenstest.KVs(t)

	ddlKey := "vtx.meta." + lenstest.NanoID("capability-proposal-ddl")
	putVertex(t, coreKV, ddlKey, "meta.ddl.vertexType")
	putAspect(t, coreKV, ddlKey, "canonicalName", "canonicalName", map[string]any{"value": "capabilityproposal"})
	putAspect(t, coreKV, ddlKey, "description", "description", map[string]any{"text": "AI-authored capability proposal DDL."})
	putAspect(t, coreKV, ddlKey, "permittedCommands", "permittedCommands", map[string]any{"commands": []any{"RequestCapabilityAuthoring", "RecordCapabilityProposal"}})
	putAspect(t, coreKV, ddlKey, "inputSchema", "inputSchema", map[string]any{"schema": `{"type":"object"}`})
	putAspect(t, coreKV, ddlKey, "outputSchema", "outputSchema", map[string]any{"schema": `{"type":"object"}`})
	putAspect(t, coreKV, ddlKey, "fieldDescription", "fieldDescription", map[string]any{"fieldDescriptions": map[string]any{"intent": "the plain-language request"}})
	putAspect(t, coreKV, ddlKey, "examples", "examples", map[string]any{"examples": []any{map[string]any{"name": "basic"}}})

	lensKey := "vtx.meta." + lenstest.NanoID("capability-proposals-lens")
	putVertex(t, coreKV, lensKey, "meta.lens")
	putAspect(t, coreKV, lensKey, "canonicalName", "canonicalName", map[string]any{"value": "capabilityProposals"})
	putAspect(t, coreKV, lensKey, "description", "description", map[string]any{"text": "The operator review lens."})
	putAspect(t, coreKV, lensKey, "spec", "spec", map[string]any{"cypherRule": "MATCH (p:provider) RETURN p.key AS key", "engine": "full"})

	rows := projectCapAuthor(t, adjKV, coreKV, capabilityAuthorContextSpec)
	require.Len(t, rows, 2)

	ddlRow := rowByCapAuthorKey(rows, ddlKey)
	require.NotNil(t, ddlRow)
	require.Equal(t, "meta.ddl.vertexType", ddlRow["class"])
	require.Equal(t, "capabilityproposal", ddlRow["canonicalName"])
	require.Equal(t, []any{"RequestCapabilityAuthoring", "RecordCapabilityProposal"}, ddlRow["permittedCommands"])
	require.Equal(t, `{"type":"object"}`, ddlRow["inputSchema"])
	require.Nil(t, ddlRow["spec"], "a DDL carries no .spec aspect (only lens/weaverTarget/loomPattern do) → null")

	lensRow := rowByCapAuthorKey(rows, lensKey)
	require.NotNil(t, lensRow)
	require.Equal(t, "meta.lens", lensRow["class"])
	require.Equal(t, "capabilityProposals", lensRow["canonicalName"])
	require.Nil(t, lensRow["permittedCommands"], "non-DDL meta has no permittedCommands aspect → null")
	require.Nil(t, lensRow["inputSchema"], "non-DDL meta has no inputSchema aspect → null")
	require.Equal(t, map[string]any{"cypherRule": "MATCH (p:provider) RETURN p.key AS key", "engine": "full"}, lensRow["spec"],
		"the full .spec aspect body projects verbatim (Increment 4 — the widening this test proves)")
}

// TestCapabilityAuthorPackages_ManifestProjects proves the ownership surface a
// Core-KV-denied reader resolves "which package declared this meta key" from.
//
// declaredKeys must arrive as a LIST — it is scanned for a meta key, so a
// column that flattened to a string or dropped would silently make every target
// look unowned (i.e. kernel-seeded) and refuse every edit. The manifest-less
// package pins the null-safe arm: an install caught mid-batch leaves a package
// vertex with no .manifest aspect, and that must project as a row claiming
// nothing rather than erroring the whole projection out.
func TestCapabilityAuthorPackages_ManifestProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := lenstest.KVs(t)

	targetKey := "vtx.meta." + lenstest.NanoID("weaver-target-cold-nudge")
	ownerKey := "vtx.package." + lenstest.NanoID("weaver-target-cold-nudge-pkg")
	putVertex(t, coreKV, ownerKey, "package")
	putAspect(t, coreKV, ownerKey, "manifest", "manifest", map[string]any{
		"name":         "weaver-target-coldNudge-7f2a",
		"version":      "0.1.0",
		"description":  "",
		"depends":      []any{},
		"declaredKeys": []any{targetKey, targetKey + ".spec", targetKey + ".description", ownerKey},
	})

	repoKey := "vtx.package." + lenstest.NanoID("clinic-reminders-pkg")
	putVertex(t, coreKV, repoKey, "package")
	putAspect(t, coreKV, repoKey, "manifest", "manifest", map[string]any{
		"name":         "clinic-reminders",
		"version":      "1.2.0",
		"description":  "Reminders for the clinic's appointments.",
		"depends":      []any{"orchestration-base"},
		"declaredKeys": []any{repoKey},
	})

	bareKey := "vtx.package." + lenstest.NanoID("half-installed-package")
	putVertex(t, coreKV, bareKey, "package")

	rows := projectCapAuthor(t, adjKV, coreKV, capabilityAuthorPackagesSpec)
	require.Len(t, rows, 3)

	owner := rowByCapAuthorKey(rows, ownerKey)
	require.NotNil(t, owner)
	require.Equal(t, "weaver-target-coldNudge-7f2a", owner["name"])
	require.Equal(t, "0.1.0", owner["version"])
	require.Equal(t, []any{targetKey, targetKey + ".spec", targetKey + ".description", ownerKey}, owner["declaredKeys"],
		"declaredKeys is scanned for a meta key — it must project as the list build.go wrote, not a flattened value")
	require.Equal(t, "", owner["description"])
	require.Equal(t, []any{}, owner["depends"],
		"a console-minted package records neither, which is exactly what makes it editable in place")

	// A repo package records both, and an in-place upgrade from a capability
	// proposal would blank them — a reader has to see them to refuse.
	repo := rowByCapAuthorKey(rows, repoKey)
	require.NotNil(t, repo)
	require.Equal(t, "Reminders for the clinic's appointments.", repo["description"])
	require.Equal(t, []any{"orchestration-base"}, repo["depends"],
		"depends is compared for emptiness, so it must project as the list build.go wrote")

	bare := rowByCapAuthorKey(rows, bareKey)
	require.NotNil(t, bare, "a package whose manifest aspect is absent still projects a row")
	require.Nil(t, bare["name"])
	require.Nil(t, bare["declaredKeys"], "no manifest → no declaration → the package claims no key")
	require.Nil(t, bare["description"])
	require.Nil(t, bare["depends"])
}
