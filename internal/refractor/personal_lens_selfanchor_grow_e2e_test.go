// Package refractor_test — end-to-end proof for the self-anchored Personal
// Lens reprojection path that edge-manifest's `edgeIdentity` (manifest.me)
// exercises: a lens whose `anchor` is the recipient identity ITSELF and whose
// body columns COLLECT over the actor's own OPTIONAL-MATCH neighbourhood
// (roles, workplaces, self-anchors). PL.2's VertexFanOut proof anchors on a
// NON-actor vertex (a lease); this one closes the untested case — an identity
// whose self-anchored row already exists later GAINING a second, independent
// link, verifying the collect() column GROWS rather than staying frozen at its
// first-projection value. It runs with the D1 read-gate ACTIVE (capKV
// non-nil, seeded with the base self-grant CapabilityReadLensDefinition emits)
// so the production read-path posture is what is proven, not the trusted
// nil-capKV posture the sibling PL.2 tests use.
package refractor_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedSelfReadGrant writes the base `cap-read.identity.<actor>.<actor>`
// per-anchor key CapabilityReadLensDefinition (internal/bootstrap/lenses.go)
// projects for every actor — its self anchor ("an actor may always read its
// own vertex"), anchorId == actorId. Threading a non-nil capKV seeded with
// this is what makes the self-anchored manifest.me row pass
// personalEnvelopeFn's D1 IsReadable gate in production; without the key the
// gate fail-closes the actor's own row.
func seedSelfReadGrant(t *testing.T, h *pl2Harness, actorID string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"isDeleted": false})
	require.NoError(t, err)
	_, err = h.capKV.Put(h.ctx, "cap-read.identity."+actorID+"."+actorID, b)
	require.NoError(t, err)
}

// roleKeysOf pulls the set of non-null role keys out of a manifest-me-shaped
// delta's `roles` collect() column. collect(DISTINCT {key: role.key}) yields a
// single degenerate {key:null} entry when the actor holds no role (dropped
// client-side per the lens's own doc), so a null key is ignored here.
func roleKeysOf(t *testing.T, env map[string]any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	data, ok := env["data"].(map[string]any)
	if !ok {
		return out
	}
	roles, ok := data["roles"].([]any)
	if !ok {
		return out
	}
	for _, r := range roles {
		entry, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if k, ok := entry["key"].(string); ok && k != "" {
			out[k] = true
		}
	}
	return out
}

// TestPersonalLens_SelfAnchoredRow_GrowsWhenActorGainsALink_E2E is the missing
// coverage the manifest.me "frozen row" investigation surfaced: prove that when
// an identity whose self-anchored Personal Lens row already exists later gains a
// second, independent link (a pure `holdsRole` link mutation, NO touch to the
// identity vertex itself), the fan-out re-executes the self-anchored cypher and
// republishes a delta whose collect() column has GROWN to include the new
// neighbour. A freeze bug anywhere in the trigger → re-exec → D1 self-gate →
// publish-revision → chain would leave the row stuck at one role.
func TestPersonalLens_SelfAnchoredRow_GrowsWhenActorGainsALink_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	recipient := pl2NanoID("selfgrow-recipient")
	identityKey := substrate.VertexKey("identity", recipient)
	role1 := pl2NanoID("selfgrow-role1")
	role2 := pl2NanoID("selfgrow-role2")
	role1Key := substrate.VertexKey("role", role1)
	role2Key := substrate.VertexKey("role", role2)

	// Self-anchored, mirroring edge-manifest edgeIdentitySpec: anchor is the
	// identity's own key, roles collect over an OPTIONAL MATCH that must widen
	// as the actor gains holdsRole links.
	cypher := `MATCH (identity:identity {key: $actorKey}) ` +
		`OPTIONAL MATCH (identity)-[:holdsRole]->(role:role) ` +
		`RETURN identity.key AS anchor, "me" AS kind, "manifest.me" AS ns, ` +
		`collect(DISTINCT {key: role.key}) AS roles`

	// Production posture: the D1 read-gate is live and the actor's base
	// self-grant is present, so the self-anchored row is readable-to-self.
	seedSelfReadGrant(t, h, recipient)
	_, _ = activatePersonalLens(t, h, pl2NanoID("selfgrow-lens"), cypher, []string{"ns"}, h.capKV)

	// First hat.
	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "sam"})
	writePL2Vertex(t, h, role1Key, "role", map[string]any{"canonicalName": "consumer"})
	writePL2Link(t, h, "identity", recipient, "holdsRole", "role", role1)

	cons, err := h.js.CreateOrUpdateConsumer(h.ctx, "SYNC", jetstream.ConsumerConfig{
		FilterSubject: "lattice.sync.user." + recipient,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	// The self-anchored row must first project through the D1 self-gate with
	// exactly the one hat.
	require.Eventually(t, func() bool {
		msg, err := cons.Next(jetstream.FetchMaxWait(2 * time.Second))
		if err != nil {
			return false
		}
		var env map[string]any
		if json.Unmarshal(msg.Data(), &env) != nil || env["op"] != "upsert" {
			return false
		}
		keys := roleKeysOf(t, env)
		return keys[role1Key] && !keys[role2Key]
	}, 20*time.Second, 200*time.Millisecond,
		"the self-anchored row must project through the D1 self-gate carrying the first hat")

	// The actor GAINS a second hat — a pure holdsRole link mutation, no touch to
	// the identity vertex. This is the exact event class the "frozen manifest.me"
	// symptom named.
	writePL2Vertex(t, h, role2Key, "role", map[string]any{"canonicalName": "frontOfHouse"})
	writePL2Link(t, h, "identity", recipient, "holdsRole", "role", role2)

	// The re-published self-anchored row must GROW to both hats — the freeze bug
	// would keep it at one.
	require.Eventually(t, func() bool {
		msg, err := cons.Next(jetstream.FetchMaxWait(2 * time.Second))
		if err != nil {
			return false
		}
		var env map[string]any
		if json.Unmarshal(msg.Data(), &env) != nil || env["op"] != "upsert" {
			return false
		}
		keys := roleKeysOf(t, env)
		return keys[role1Key] && keys[role2Key]
	}, 20*time.Second, 200*time.Millisecond,
		"gaining a second holdsRole link must fan out to the self-anchored actor and grow its collect() column")
}
