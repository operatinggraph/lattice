// Package refractor_test — end-to-end proof for personal-secure-lens-design.md
// Fire 3 (PL.3): wires D1's read-path Capability KV (Contract #6 §6.14,
// internal/refractor/capabilityread) as the Personal Lens's security gate,
// replacing the allow-all stub PL.1/PL.2 ran under. These cover 3 of the
// design's §5 "Gate-3" read-bypass vectors: security wins over relevance
// (vector 1), absence fails closed (vector 2), and a revoke stops the stream
// (vector 3). Reuses pl2Harness/activatePersonalLens/writePL2Vertex/
// writePL2Link/pl2NanoID (same package — PL.3 is the security layer over the
// identical fan-out fixtures PL.2 proved).
//
// Vector 4 (a stale lower-projectionSeq replay must not resurrect a revoked
// grant) is D1's PRODUCER-side write-ordering guarantee (§6.2/§6.8's
// projectionSeq monotonic guard on the write into "cap-read.*"), not
// something this fire's CONSUMER code (a plain KV Get per call, no local
// ordering state) can introduce risk into or usefully re-verify — it is
// covered by D1's own write-path tests. Vector 5 (shredded identity) is
// explicitly deferred to PL.5 (gated on Vault Phase A, §3.6/§7).
package refractor_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// pl3ReadableAnchor / pl3ReadDoc mirror the on-wire shape Contract #6 §6.14
// producers write to "cap-read.<source>.<actor>" — seeded directly here since
// no D1 read-grant lens runs in this ephemeral-NATS fixture (the object under
// test is the Personal Lens's CONSUMPTION of that KV, not D1's production of
// it, which has its own contract-conformance coverage).
type pl3ReadableAnchor struct {
	AnchorType string `json:"anchorType"`
	AnchorID   string `json:"anchorId"`
}

type pl3ReadDoc struct {
	IsDeleted       bool                `json:"isDeleted"`
	ReadableAnchors []pl3ReadableAnchor `json:"readableAnchors"`
}

func putPL3ReadDoc(t *testing.T, h *pl2Harness, key string, isDeleted bool, anchors ...pl3ReadableAnchor) {
	t.Helper()
	raw, err := json.Marshal(pl3ReadDoc{IsDeleted: isDeleted, ReadableAnchors: anchors})
	require.NoError(t, err)
	_, err = h.capKV.Put(h.ctx, key, raw)
	require.NoError(t, err)
}

func pl3LeaseCypher() string {
	return `MATCH (identity {key: $actorKey})-[:holds]->(l:lease) ` +
		`RETURN l.key AS anchor, "lease" AS kind, l.id AS entityId, l.monthlyRent AS monthlyRent`
}

func pl3Consumer(t *testing.T, h *pl2Harness, recipient string) jetstream.Consumer {
	t.Helper()
	cons, err := h.js.CreateOrUpdateConsumer(h.ctx, "SYNC", jetstream.ConsumerConfig{
		FilterSubject: "lattice.sync.user." + recipient,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)
	return cons
}

// TestPersonalLens_PL3_E2E_NoCapReadEntry_DeniesFailClosed is Gate-3 vector
// (2): an actor with no "cap-read.*" entry at all must get an empty stream —
// D1's "no entry = no access" (§6.8) carried through to the fan-out.
func TestPersonalLens_PL3_E2E_NoCapReadEntry_DeniesFailClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	recipient := pl2NanoID("pl3-noentry-recipient")
	identityKey := substrate.VertexKey("identity", recipient)
	leaseID := pl2NanoID("pl3-noentry-lease")
	leaseKey := substrate.VertexKey("lease", leaseID)

	_, _ = activatePersonalLens(t, h, pl2NanoID("pl3-noentry-lens"), pl3LeaseCypher(), []string{"entityId"}, h.capKV)

	// Deliberately NO cap-read.* seeded for this actor.
	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "recipient"})
	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-pl3-1", "monthlyRent": 2000})
	writePL2Link(t, h, "identity", recipient, "holds", "lease", leaseID)

	cons := pl3Consumer(t, h, recipient)
	msg, err := cons.Next(jetstream.FetchMaxWait(3 * time.Second))
	require.NoError(t, err, "an actor with no cap-read entry at all still receives an empty keyset frame")
	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	require.Equal(t, "keyset", env["op"], "denial retracts by an empty frame, never publishes the row")
	require.Empty(t, env["keys"], "no cap-read entry means no readable anchor")
}

// TestPersonalLens_PL3_E2E_SecurityWinsOverRelevance is Gate-3 vector (1): a
// device EXPLICITLY registers an Interest Set that declares this lease's
// anchor type relevant (so personalinterest.IsRelevant would admit it), but
// the actor's D1 read-grants don't cover this lease's anchor — the delta must
// still be denied, proving the security filter is checked first and wins
// over an affirmatively-relevant Interest Set, not just over the "no
// registration" default-admit case (which TestPersonalLens_PL3_E2E_
// NoCapReadEntry_DeniesFailClosed already covers on its own).
func TestPersonalLens_PL3_E2E_SecurityWinsOverRelevance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	recipient := pl2NanoID("pl3-secwins-recipient")
	identityKey := substrate.VertexKey("identity", recipient)
	leaseID := pl2NanoID("pl3-secwins-lease")
	leaseKey := substrate.VertexKey("lease", leaseID)

	_, _ = activatePersonalLens(t, h, pl2NanoID("pl3-secwins-lens"), pl3LeaseCypher(), []string{"entityId"}, h.capKV)

	// cap-read grants a DIFFERENT anchor only — this lease is NOT among it.
	putPL3ReadDoc(t, h, "cap-read.identity."+recipient, false,
		pl3ReadableAnchor{AnchorType: "unit", AnchorID: pl2NanoID("pl3-secwins-other-anchor")})

	// The device's Interest Set explicitly declares "lease" relevant — the
	// relevance filter alone would admit this delta.
	ctrlSvc := control.NewService()
	// Allow-all stub: this e2e drives the personal-lens projection path, not
	// capability enforcement (a nil/unconfigured checker fails closed).
	ctrlSvc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	ctrlSvc.SetPersonalInterestKV(h.interestKV)
	ctrlCtx, ctrlCancel := context.WithCancel(h.ctx)
	t.Cleanup(ctrlCancel)
	require.NoError(t, ctrlSvc.StartNATSListener(ctrlCtx, h.conn.NATS()))

	registerData, err := json.Marshal(control.ControlRequest{
		IdentityID: recipient, DeviceID: "deviceX", Types: []string{"lease"},
	})
	require.NoError(t, err)
	reply, err := h.conn.NATS().Request(control.ControlSubject("personal", "register"), registerData, 5*time.Second)
	require.NoError(t, err)
	var regResp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &regResp))
	require.Empty(t, regResp.Error)
	require.True(t, regResp.PersonalRegister.Registered)

	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "recipient"})
	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-pl3-2", "monthlyRent": 2500})
	writePL2Link(t, h, "identity", recipient, "holds", "lease", leaseID)

	cons := pl3Consumer(t, h, recipient)
	msg, err := cons.Next(jetstream.FetchMaxWait(3 * time.Second))
	require.NoError(t, err, "a delta outside the actor's readableAnchors must be denied — even when the Interest Set explicitly declares it relevant — via an empty keyset frame")
	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	require.Equal(t, "keyset", env["op"], "denial retracts by an empty frame, never publishes the row")
	require.Empty(t, env["keys"], "the ungranted anchor must not appear in the frame")
}

// TestPersonalLens_PL3_E2E_GrantedAnchor_StreamsThenRevokeStops is Gate-3
// vector (3): a delta streams once the actor's cap-read grant covers the
// anchor; after the grant is revoked (soft-tombstoned, §6.8), a subsequent
// mutation on the same anchor must NOT stream.
func TestPersonalLens_PL3_E2E_GrantedAnchor_StreamsThenRevokeStops(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	recipient := pl2NanoID("pl3-revoke-recipient")
	identityKey := substrate.VertexKey("identity", recipient)
	leaseID := pl2NanoID("pl3-revoke-lease")
	leaseKey := substrate.VertexKey("lease", leaseID)
	_, leaseBareID, ok := substrate.ParseVertexKey(leaseKey)
	require.True(t, ok)

	_, _ = activatePersonalLens(t, h, pl2NanoID("pl3-revoke-lens"), pl3LeaseCypher(), []string{"entityId"}, h.capKV)

	grantKey := "cap-read.identity." + recipient
	putPL3ReadDoc(t, h, grantKey, false, pl3ReadableAnchor{AnchorType: "lease", AnchorID: leaseBareID})

	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "recipient"})
	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-pl3-3", "monthlyRent": 3000})
	writePL2Link(t, h, "identity", recipient, "holds", "lease", leaseID)

	cons := pl3Consumer(t, h, recipient)

	msg, err := cons.Next(jetstream.FetchMaxWait(15 * time.Second))
	require.NoError(t, err, "a granted anchor must stream")
	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	data, ok := env["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(3000), data["monthlyRent"])

	// Revoke: soft-tombstone the grant (§6.8 — the retained watermark
	// convention; the Personal Lens's read side treats isDeleted:true as
	// absent regardless of the surviving projectionSeq).
	putPL3ReadDoc(t, h, grantKey, true)

	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-pl3-3", "monthlyRent": 3100})

	require.Never(t, func() bool {
		msg, err := cons.Next(jetstream.FetchMaxWait(1 * time.Second))
		if err != nil {
			return false
		}
		var env map[string]any
		if json.Unmarshal(msg.Data(), &env) != nil {
			return false
		}
		data, ok := env["data"].(map[string]any)
		return ok && data["monthlyRent"] == float64(3100)
	}, 5*time.Second, 500*time.Millisecond,
		"after the grant is revoked, a further mutation on the same anchor must not stream")
}
