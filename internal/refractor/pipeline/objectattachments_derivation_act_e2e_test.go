// The ACTOR-AWARE arm's affected-anchor derivation acting on a WILDCARD hop,
// end to end over a real embedded NATS server —
// untyped-hop-anchor-derivation-design.md §10's Increment 2 e2e.
//
// The lens is the SHIPPED objectAttachments declaration, read off
// packages/objects-base rather than retyped, because the thing under test is
// whether that cypher's `OPTIONAL MATCH (o)-[r]->(owner)` — untyped, at an
// unlabeled position — now derives. A copy here would pin this file's spelling
// of the pattern instead.
//
// The adjacency index the walk reads is maintained by the pipeline's own link
// fan-out (evaluateLinkFanOut idempotently applies every link event it is handed
// before enumerating), so no separate bootstrapper is needed: the projected row
// carrying the owner IS the barrier proving the walk has an edge to step.
package pipeline_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/substrate"
	objectsbase "github.com/operatinggraph/lattice/packages/objects-base"
)

// shippedObjectsBaseSpec returns one objects-base lens's declared cypher.
func shippedObjectsBaseSpec(t *testing.T, name string) string {
	t.Helper()
	lenses := objectsbase.Lenses()
	for _, l := range lenses {
		if l.CanonicalName == name {
			return l.Spec
		}
	}
	require.FailNowf(t, "missing lens", "objects-base declares no %q lens (it declares %d)", name, len(lenses))
	return ""
}

// TestObjectAttachments_DerivationActsOnANeighbourEvent_E2E is the wildcard hop's payoff,
// asserted on the TALLY rather than on convergence alone.
//
// Convergence is asserted too, but it cannot carry the claim on its own: the
// enumerator fall-back converges the same row off the same event, so a
// convergence-only test passes identically on the pre-increment refusal. What
// separates them is which arm answered — Acted moved, FellBack did not.
//
// The trigger is an ASPECT event on the identity the object is attached to,
// which is a neighbour of the anchor over the wildcard hop and touches nothing
// the row projects. Reaching the object from it requires the walk to cross a hop
// that names no relation.
//
// The row is purged out of band first, so "was this anchor reprojected" has an
// observable answer at all: the NATS-KV adapter's unguarded upsert skips a Put
// whose row marshals to the bytes already stored, so a faithful reprojection of
// an unchanged row moves no revision.
func TestObjectAttachments_DerivationActsOnANeighbourEvent_E2E(t *testing.T) {
	env := startPipelineEnv(t)
	ruleID := "objectattachments-act"

	eng, cr := compileFullRule(t, shippedObjectsBaseSpec(t, "objectAttachments"), []string{"actorKey"})
	targetKV, adpt := newTargetKVMode(t, env, ruleID+"-target", []string{"actorKey"}, adapter.DeleteModeHard)

	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt,
		newHealthReporter(t, env, ruleID))
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, cr))

	// The three declarations InstallActorAggregate makes for every actorAggregate
	// lens, and the act-gate conjuncts other than the index itself: an enumerator
	// paired with the descriptor's AnchorType, pattern-closed output, and the
	// sweep plan whose presence IS the standing healer.
	p.SetActorEnumerator(pipeline.NewActorEnumerator(env.adjKV, env.coreKV, "object"))
	p.SetPatternClosedOutput(true)
	p.SetSweepPlan(pipeline.SweepPlan{AnchorType: "object", KeyPrefix: ruleID + "."})
	p.SetAnchorDerivationMode(pipeline.DerivationModeAct)

	// The index really does index this cypher — the precondition the whole
	// increment is about, asserted before any event so a later green cannot be
	// read as the enumerator's doing.
	ix := cr.AnchorHopIndex()
	require.Truef(t, ix.Complete,
		"the shipped objectAttachments cypher must index; %q means the wildcard hop never landed", ix.Incomplete)
	require.Len(t, ix.Hops, 1)
	require.Equal(t, "", ix.Hops[0].Rel, "the hop under test is the wildcard one")

	startPipeline(t, env, p, ruleID)

	oid, idID := narrowedID(t, "Attach1"), narrowedID(t, "Hdr1")
	objKey := substrate.VertexKey("object", oid)
	identityKey := substrate.VertexKey("identity", idID)

	putNode(t, env.coreKV, objKey, map[string]any{"key": objKey, "class": "object"})
	putNode(t, env.coreKV, objKey+".content", map[string]any{
		"key":   objKey + ".content",
		"class": "object.content",
		"data":  map[string]any{"storeName": "objects", "contentType": "image/png", "size": 11},
	})
	putNode(t, env.coreKV, identityKey, map[string]any{"key": identityKey, "class": "identity"})
	putLink(t, env.coreKV, "object", oid, "photoOf", "identity", idID)

	// A row carrying the TRAVERSED owner is the adjacency barrier: the walk this
	// test depends on has an edge to step only once the link event has been
	// applied, and only a traversed value proves it was.
	ownerOf := func() string {
		entry, err := targetKV.Get(context.Background(), objKey)
		if err != nil || entry == nil || len(entry.Value) == 0 {
			return ""
		}
		var row map[string]any
		if json.Unmarshal(entry.Value, &row) != nil {
			return ""
		}
		owners, _ := row["owners"].([]any)
		for _, o := range owners {
			m, _ := o.(map[string]any)
			if k, _ := m["ownerKey"].(string); k == identityKey {
				return k
			}
		}
		return ""
	}
	pollUntil(t, 30*time.Second, func() bool { return ownerOf() == identityKey })

	// The tally is read as a DELTA across the one event below rather than as a
	// total, so the setup's own events cannot supply the movement being claimed.
	before := p.AnchorDerivationShadow()
	require.Zero(t, before.FellBack,
		"not one setup event fell back either — the arm has been the derivation throughout")

	require.NoError(t, targetKV.Purge(context.Background(), objKey))
	require.Empty(t, ownerOf(), "the hole must be real before the reprojection question is asked")

	// An aspect of the OWNER identity, projecting into no column of this lens —
	// a pure neighbour event, reachable only across the untyped hop.
	putNode(t, env.coreKV, identityKey+".profile", map[string]any{
		"key": identityKey + ".profile", "class": "identity.profile",
		"data":           map[string]any{"displayName": "owner-renamed"},
		"lastModifiedAt": "2026-02-02T00:00:00Z",
	})

	pollUntil(t, 30*time.Second, func() bool { return ownerOf() == identityKey })

	after := p.AnchorDerivationShadow()
	require.Greater(t, after.Acted, before.Acted,
		"the neighbour event must be answered by the DERIVATION, which is what the wildcard hop buys")
	require.Zero(t, after.FellBack,
		"a fall-back here means the walk refused the wildcard hop and the enumerator converged the row instead — "+
			"the convergence above would look identical")
}
