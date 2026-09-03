// Package refractor_test — end-to-end proof for
// personal-lens-derivation-licence-design.md Increment 1b: the Interest Set's
// change edge. Reuses grantEdgeFixture (personal_lens_grant_change_e2e_test.go)
// and the pl2 harness.
package refractor_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
)

// interestControl starts a real control service over the fixture's own NATS
// connection, with the Interest Set change edge wired to the fixture's
// reprojector — the same two calls cmd/refractor makes.
//
// The RPC is driven rather than the unexported handler called, because the
// mechanism under test is the WRITER announcing: a test that reached past the
// control op would be asserting that the closure it installed itself calls the
// method it installed.
func interestControl(t *testing.T, f *grantEdgeFixture, sink func(string)) {
	t.Helper()
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalInterestKV(f.h.interestKV)
	if sink != nil {
		svc.SetInterestChangeSink(sink)
	}
	ctx, cancel := context.WithCancel(f.h.ctx)
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNATSListener(ctx, f.h.conn.NATS()))
}

// interestRPC issues one personal.register / personal.deregister and fails on
// any error response, so a typo in the request body reads as a test failure
// rather than as a silently-absent reprojection.
func interestRPC(t *testing.T, f *grantEdgeFixture, verb string, body control.ControlRequest) {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	reply, err := f.h.conn.NATS().Request(control.ControlSubject("personal", verb), data, 5*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))
	require.Empty(t, resp.Error, "personal.%s must succeed before its edge can be asserted", verb)
}

// TestPersonalLensInterestEdge_NarrowingPrunesTheDevicesFrame is Increment 1b's
// headline (design §4.2, §10 "Interest-edge e2e").
//
// A personal row is decided against TWO projections read live — the D1 read
// gate and the Interest Set. Without this edge the Interest Set's only coverage
// is a 60 s/5-identity round-robin plus whatever unrelated Core-KV traffic
// happens to re-drive the actor, so a device that narrows its interest keeps
// receiving the excluded keys for up to a full sweeper cycle.
//
// The NARROWING direction is the one that matters and the one asserted here:
// the device must stop receiving what it stopped asking for, and the personal
// lens's authoritative keyset frame is the only thing that prunes it.
//
// The fixture's personal consumer is stopped before the interest changes, so
// the ONLY route from "a registration lands in the interest bucket" to "a frame
// on the device's SYNC subject" is the edge under test. The barrier is the
// EFFECT — the frame itself — never a consumer's pending count.
func TestPersonalLensInterestEdge_NarrowingPrunesTheDevicesFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	f := newGrantEdgeFixture(t, "interest-edge", true)
	cons := pl3Consumer(t, f.h, f.identityID)
	interestControl(t, f, f.reprojector.InterestChanged)

	// The identity may read the lease, so the row is live on the device before
	// interest is ever narrowed — otherwise a pruned frame proves nothing.
	f.grant(t)
	f.awaitCapReadLiveness(t, true)
	f.settle(t, cons)
	f.reprojector.Drain(f.h.ctx)
	drainUntilQuiet(t, cons)

	// A registration that admits the lease type: the row keeps flowing.
	interestRPC(t, f, "register", control.ControlRequest{
		IdentityID: f.identityID, DeviceID: "device-interest-edge", Types: []string{"lease"},
	})
	require.Eventually(t, func() bool { return f.reprojector.QueueDepth() > 0 }, 15*time.Second, 50*time.Millisecond,
		"a registration must enqueue its identity for reprojection")
	f.reprojector.Drain(f.h.ctx)
	widened := drainUntilQuiet(t, cons)
	assert.True(t, frameNames(widened, f.personalLens, "lease-grant-edge"),
		"while the device declares interest in leases, its frame still names the row")

	// The narrowing: the same device re-registers wanting a type this lens does
	// not project. Nothing on the identity's own subgraph changes.
	interestRPC(t, f, "register", control.ControlRequest{
		IdentityID: f.identityID, DeviceID: "device-interest-edge", Types: []string{"invoice"},
	})
	require.Eventually(t, func() bool { return f.reprojector.QueueDepth() > 0 }, 15*time.Second, 50*time.Millisecond,
		"narrowing the Interest Set must enqueue the identity — the frame that prunes its keys is owed to this signal alone")
	f.reprojector.Drain(f.h.ctx)

	narrowed := drainUntilQuiet(t, cons)
	require.NotEmpty(t, narrowed, "the narrowing must publish a frame")
	assert.False(t, frameNames(narrowed, f.personalLens, "lease-grant-edge"),
		"the frame published after the narrowing must OMIT the excluded key, which is what prunes it on the device")
	assert.True(t, sawEmptyFrame(narrowed, f.personalLens),
		"a device that wants nothing this lens projects is framed with no keys")

	// Deregistering the last device WIDENS what IsRelevant admits — absence of
	// any registration admits everything — so the row comes back.
	interestRPC(t, f, "deregister", control.ControlRequest{
		IdentityID: f.identityID, DeviceID: "device-interest-edge",
	})
	require.Eventually(t, func() bool { return f.reprojector.QueueDepth() > 0 }, 15*time.Second, 50*time.Millisecond,
		"deregistering must enqueue the identity too")
	f.reprojector.Drain(f.h.ctx)

	rewidened := drainUntilQuiet(t, cons)
	assert.True(t, frameNames(rewidened, f.personalLens, "lease-grant-edge"),
		"removing the last registration re-admits every anchor, and the widening reprojection is what tells the device")
}

// TestPersonalLensInterestEdge_MutationSinkDisabled is the mutation control,
// kept permanently rather than run once and deleted.
//
// It is the test above's narrowing half with the change edge NOT wired. If it
// ever passes alongside a green edge test, that test is passing for some reason
// other than the mechanism it claims to prove — an unrelated Core-KV event
// re-driving the actor is exactly the accident this design removes.
func TestPersonalLensInterestEdge_MutationSinkDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	f := newGrantEdgeFixture(t, "interest-mutation", true)
	cons := pl3Consumer(t, f.h, f.identityID)
	interestControl(t, f, nil)

	f.grant(t)
	f.awaitCapReadLiveness(t, true)
	f.settle(t, cons)
	f.reprojector.Drain(f.h.ctx)
	drainUntilQuiet(t, cons)

	interestRPC(t, f, "register", control.ControlRequest{
		IdentityID: f.identityID, DeviceID: "device-interest-mutation", Types: []string{"invoice"},
	})
	f.reprojector.Drain(f.h.ctx)

	assert.Zero(t, f.reprojector.QueueDepth(),
		"with no edge wired, a real Interest Set narrowing must enqueue nothing")
	assert.Empty(t, drainBriefly(t, cons),
		"without the edge the device hears nothing about the interest it just narrowed — this is the bug the edge closes")
}
