// Package refractor_test — end-to-end proof for personal-secure-lens-design.md
// Fire 4 (PL.4): the "personal.hydrate" control RPC cold-bulk-projects one
// identity's slice and terminates with a hydrationComplete marker, so a
// device that missed the retention window (or is starting cold) can catch up
// without replaying the whole SYNC stream. Reuses pl2Harness/
// activatePersonalLens/writePL2Vertex/writePL2Link/pl2NanoID (same package).
package refractor_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestPersonalLens_PL4_E2E_HydrateBulkProjectsThenCompletes proves the cold
// path: an identity with an existing authorized lease gets a bulk upsert +
// terminal hydrationComplete marker from a single "hydrate" control RPC call,
// with no CDC event needed to trigger it.
func TestPersonalLens_PL4_E2E_HydrateBulkProjectsThenCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	recipient := pl2NanoID("pl4-hydrate-recipient")
	identityKey := substrate.VertexKey("identity", recipient)
	leaseID := pl2NanoID("pl4-hydrate-lease")
	leaseKey := substrate.VertexKey("lease", leaseID)

	lensID := pl2NanoID("pl4-hydrate-lens")
	cypher := `MATCH (identity {key: $actorKey})-[:holds]->(l:lease) ` +
		`RETURN l.key AS anchor, "lease" AS kind, l.id AS entityId, l.monthlyRent AS monthlyRent`
	p, _ := activatePersonalLens(t, h, lensID, cypher, []string{"entityId"}, nil)

	// Seed the identity's slice BEFORE registering any control listener or
	// hydrate call — hydrate must find the CURRENT state via reprojection,
	// not rely on having observed the CDC events that created it.
	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "recipient"})
	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-pl4-1", "monthlyRent": 2200})
	writePL2Link(t, h, "identity", recipient, "holds", "lease", leaseID)

	// Drain the deltas the writes above already fanned out via the live CDC
	// path, so the hydrate call's OWN bulk-publish is what the test observes
	// next on a fresh consumer.
	drainCons, err := h.js.CreateOrUpdateConsumer(h.ctx, "SYNC", jetstream.ConsumerConfig{
		FilterSubject: "lattice.sync.user." + recipient,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err := drainCons.Next(jetstream.FetchMaxWait(500 * time.Millisecond))
		return err != nil
	}, 20*time.Second, 200*time.Millisecond, "CDC fan-out from the seed writes must settle before hydrate")

	ctrlSvc := control.NewService()
	// Allow-all stub: this e2e drives the personal-lens hydrate path, not
	// capability enforcement (a nil/unconfigured checker fails closed).
	ctrlSvc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	ctrlSvc.RegisterPersonalHydrator(lensID, p)
	ctrlSvc.SetPersonalInterestKV(h.interestKV)
	ctrlCtx, ctrlCancel := context.WithCancel(h.ctx)
	t.Cleanup(ctrlCancel)
	require.NoError(t, ctrlSvc.StartNATSListener(ctrlCtx, h.conn.NATS()))

	hydrateData, err := json.Marshal(control.ControlRequest{IdentityID: recipient, DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err := h.conn.NATS().Request(control.ControlSubject("personal", "hydrate"), hydrateData, 10*time.Second)
	require.NoError(t, err)
	var hydrateResp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &hydrateResp))
	require.Empty(t, hydrateResp.Error)
	require.NotNil(t, hydrateResp.PersonalHydrate)
	require.True(t, hydrateResp.PersonalHydrate.Hydrated)
	require.Equal(t, []string{lensID}, hydrateResp.PersonalHydrate.Lenses,
		"the response must name every registered personal hydrator that ran (§3.4's dead-lens prune set)")
	revision := hydrateResp.PersonalHydrate.Revision

	// The bulk upsert for the identity's one lease.
	msg, err := drainCons.Next(jetstream.FetchMaxWait(10 * time.Second))
	require.NoError(t, err, "hydrate must bulk-publish the identity's current slice")
	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	require.Equal(t, "upsert", env["op"])
	data, ok := env["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(2200), data["monthlyRent"])

	// The keyset frame at the same revision — the authoritative set this
	// bulk projection just published (personal-lens-retraction-design.md
	// §3.4), published before the terminal marker.
	msg, err = drainCons.Next(jetstream.FetchMaxWait(10 * time.Second))
	require.NoError(t, err, "hydrate must publish a keyset frame")
	var frameEnv map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &frameEnv))
	require.Equal(t, "keyset", frameEnv["op"])
	require.Equal(t, float64(revision), frameEnv["revision"])
	frameKeys, ok := frameEnv["keys"].([]any)
	require.True(t, ok)
	require.Len(t, frameKeys, 1, "the frame names the identity's one surviving lease row")

	// The terminal hydrationComplete marker, carrying the same revision the
	// control response returned.
	msg, err = drainCons.Next(jetstream.FetchMaxWait(10 * time.Second))
	require.NoError(t, err, "hydrate must terminate with a hydrationComplete marker")
	var markerEnv map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &markerEnv))
	require.Equal(t, "hydrationComplete", markerEnv["op"])
	require.Equal(t, float64(revision), markerEnv["revision"])

	// PL.4's reserved bookkeeping: the device's Interest Set doc records the
	// revision cursor without disturbing its (absent, here) filter.
	key, err := personalinterest.Key(recipient, "deviceX")
	require.NoError(t, err)
	entry, err := h.interestKV.Get(h.ctx, key)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	require.Equal(t, float64(revision), doc["revisionCursor"])
}

// TestPersonalLens_PL4_E2E_HydrateNoLease_PublishesEmptyFrameThenMarker
// proves the zero-anchor case is a clean no-op-then-complete: an identity
// with no leases gets no upsert row — only an empty keyset frame (the
// last-row-retraction signal, personal-lens-retraction-design.md §3.4) and
// the terminal marker.
func TestPersonalLens_PL4_E2E_HydrateNoLease_PublishesEmptyFrameThenMarker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	recipient := pl2NanoID("pl4-empty-recipient")
	identityKey := substrate.VertexKey("identity", recipient)

	lensID := pl2NanoID("pl4-empty-lens")
	cypher := `MATCH (identity {key: $actorKey})-[:holds]->(l:lease) ` +
		`RETURN l.key AS anchor, "lease" AS kind, l.id AS entityId, l.monthlyRent AS monthlyRent`
	p, _ := activatePersonalLens(t, h, lensID, cypher, []string{"entityId"}, nil)

	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "recipient"})

	drainCons, err := h.js.CreateOrUpdateConsumer(h.ctx, "SYNC", jetstream.ConsumerConfig{
		FilterSubject: "lattice.sync.user." + recipient,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	ctrlSvc := control.NewService()
	// Allow-all stub: this e2e drives the personal-lens hydrate path, not
	// capability enforcement (a nil/unconfigured checker fails closed).
	ctrlSvc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	ctrlSvc.RegisterPersonalHydrator(lensID, p)
	ctrlCtx, ctrlCancel := context.WithCancel(h.ctx)
	t.Cleanup(ctrlCancel)
	require.NoError(t, ctrlSvc.StartNATSListener(ctrlCtx, h.conn.NATS()))

	hydrateData, err := json.Marshal(control.ControlRequest{IdentityID: recipient})
	require.NoError(t, err)
	reply, err := h.conn.NATS().Request(control.ControlSubject("personal", "hydrate"), hydrateData, 10*time.Second)
	require.NoError(t, err)
	var hydrateResp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &hydrateResp))
	require.Empty(t, hydrateResp.Error)
	require.True(t, hydrateResp.PersonalHydrate.Hydrated)

	msg, err := drainCons.Next(jetstream.FetchMaxWait(10 * time.Second))
	require.NoError(t, err, "an identity with no leases must still get an empty keyset frame")
	var frameEnv map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &frameEnv))
	require.Equal(t, "keyset", frameEnv["op"])
	require.Empty(t, frameEnv["keys"], "no lease anchor means no surviving key")

	msg, err = drainCons.Next(jetstream.FetchMaxWait(10 * time.Second))
	require.NoError(t, err, "an identity with no leases must still get the terminal marker")
	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	require.Equal(t, "hydrationComplete", env["op"])

	require.Never(t, func() bool {
		_, err := drainCons.Next(jetstream.FetchMaxWait(500 * time.Millisecond))
		return err == nil
	}, 3*time.Second, 500*time.Millisecond, "no lease anchor means no upsert row, only the empty frame and the marker")
}
