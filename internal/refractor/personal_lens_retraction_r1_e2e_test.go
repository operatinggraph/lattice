// Package refractor_test — end-to-end proof for personal-lens-retraction-
// design.md's R1 (frame production): the identity-tombstone case. Reuses
// pl2Harness/activatePersonalLens/writePL2Vertex/writePL2Link/pl2NanoID
// (same package — the retraction fires close the fan-out gaps PL.2 proved).
package refractor_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestPersonalLensRetraction_R1_IdentityTombstone_PublishesEmptyFrame proves
// personal-lens-retraction-design.md §3.4's fix: tombstoning the identity a
// Personal Lens pipeline enumerates from publishes an empty keyset frame,
// not the old malformed cap-shaped Delete — natssubject.Delete rejects that
// shape ("__actor absent from keys"), and the rejection classified
// transient, so a personal pipeline (which configures no retry queue)
// redelivered it indefinitely on every such tombstone (evaluate.go
// grounding ledger row 5).
func TestPersonalLensRetraction_R1_IdentityTombstone_PublishesEmptyFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	recipient := pl2NanoID("r1-tombstone-recipient")
	identityKey := substrate.VertexKey("identity", recipient)
	leaseID := pl2NanoID("r1-tombstone-lease")
	leaseKey := substrate.VertexKey("lease", leaseID)

	lensID := pl2NanoID("r1-tombstone-lens")
	cypher := `MATCH (identity {key: $actorKey})-[:holds]->(l:lease) ` +
		`RETURN l.key AS anchor, "lease" AS kind, l.id AS entityId, l.monthlyRent AS monthlyRent`
	_, _ = activatePersonalLens(t, h, lensID, cypher, []string{"entityId"}, nil)

	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "recipient"})
	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-r1-1", "monthlyRent": 1800})
	writePL2Link(t, h, "identity", recipient, "holds", "lease", leaseID)

	cons, err := h.js.CreateOrUpdateConsumer(h.ctx, "SYNC", jetstream.ConsumerConfig{
		FilterSubject: "lattice.sync.user." + recipient,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	// The identity's own vertex event, the lease's fan-out, and the link's
	// fan-out each independently re-evaluate the (by-now fully seeded)
	// graph and publish — every re-evaluation reads CURRENT state, not a
	// point-in-time snapshot, so more than one of the three seed writes can
	// redundantly redeliver the same converged row; that duplication is
	// pre-existing fan-out behavior, orthogonal to this design. Drain to
	// quiescence and assert on the aggregate rather than on message
	// position, then isolate the tombstone's own, deterministic frame.
	seedMsgs := drainUntilQuiet(t, cons)
	require.NotEmpty(t, seedMsgs, "the seed writes must fan out at least one delta")

	var sawUpsert, sawNonEmptyFrame bool
	for _, env := range seedMsgs {
		switch env["op"] {
		case "upsert":
			data, ok := env["data"].(map[string]any)
			if ok && data["monthlyRent"] == float64(1800) && env["lens"] == lensID {
				sawUpsert = true
			}
		case "keyset":
			keys, _ := env["keys"].([]any)
			if len(keys) == 1 && keys[0] == "lease-r1-1" && env["lens"] == lensID {
				sawNonEmptyFrame = true
			}
		}
	}
	require.True(t, sawUpsert, "the seed writes must upsert the lease row")
	require.True(t, sawNonEmptyFrame, "a fan-out event over a surviving row must publish a frame naming it")

	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "recipient", "isDeleted": true})

	msg, err := cons.Next(jetstream.FetchMaxWait(15 * time.Second))
	require.NoError(t, err, "an identity tombstone must publish an empty keyset frame, never silently redeliver-loop")
	var frameEnv map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &frameEnv))
	require.Equal(t, "keyset", frameEnv["op"], "the tombstone shortcut must retract by an empty frame")
	require.Equal(t, lensID, frameEnv["lens"])
	require.Empty(t, frameEnv["keys"], "a tombstoned identity has no surviving keys")

	// No redelivery-loop residue: nothing else arrives.
	require.Never(t, func() bool {
		_, err := cons.Next(jetstream.FetchMaxWait(500 * time.Millisecond))
		return err == nil
	}, 3*time.Second, 500*time.Millisecond, "the tombstone must not redeliver-loop a malformed delete")
}

// drainUntilQuiet reads messages from cons until 1s passes with nothing new
// (allowing up to 15s for the first one, since fan-out can lag), and returns
// each as a decoded envelope.
func drainUntilQuiet(t *testing.T, cons jetstream.Consumer) []map[string]any {
	t.Helper()
	var envs []map[string]any
	wait := 15 * time.Second
	for {
		msg, err := cons.Next(jetstream.FetchMaxWait(wait))
		if err != nil {
			return envs
		}
		var env map[string]any
		require.NoError(t, json.Unmarshal(msg.Data(), &env))
		envs = append(envs, env)
		wait = time.Second
	}
}
