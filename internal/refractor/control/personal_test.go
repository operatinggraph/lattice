package control_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
)

func TestControl_PersonalRegister_NoKVConfigured_FailsClosed(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA", DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "register"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error)
	assert.Nil(t, resp.PersonalRegister)
}

func TestControl_PersonalRegister_MissingFields_Errors(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-missing")
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalInterestKV(kv)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "register"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.NotEmpty(t, resp.Error, "identityId is required")
	assert.Nil(t, resp.PersonalRegister)
}

func TestControl_PersonalRegister_Then_IsRelevant(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-register")
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalInterestKV(kv)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	data, err := json.Marshal(control.ControlRequest{
		IdentityID: "identityA", DeviceID: "deviceX", Types: []string{"lease"},
	})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "register"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.PersonalRegister)
	assert.True(t, resp.PersonalRegister.Registered)

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	assert.True(t, relevant)

	relevant, err = personalinterest.IsRelevant(ctx, kv, "identityA", "payment", "payment.1")
	require.NoError(t, err)
	assert.False(t, relevant, "registered filter must exclude a non-matching type")
}

func TestControl_PersonalDeregister_RemovesRegistration(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-deregister")
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalInterestKV(kv)
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"payment"}, nil, "2026-07-04T00:00:00Z"))

	data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA", DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err := nc.Request(control.ControlSubject("personal", "deregister"), data, 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	require.Empty(t, resp.Error)
	require.NotNil(t, resp.PersonalDeregister)
	assert.True(t, resp.PersonalDeregister.Deregistered)

	relevant, err := personalinterest.IsRelevant(ctx, kv, "identityA", "lease", "lease.1")
	require.NoError(t, err)
	assert.True(t, relevant, "deregistering the only device must revert to admit-all")
}

// TestControl_PersonalRegisterDeregister_AnnounceOnTheInterestChangeEdge is
// Increment 1b's writer half (personal-lens-derivation-licence-design.md §4.2).
//
// A personal row is decided against the Interest Set, read live at evaluation
// time. Both control-plane writers of that projection must announce, or a
// device that narrows what it wants keeps receiving the excluded keys until the
// convergence sweep next comes round.
//
// The refused-request half is what makes it a claim about ordering rather than
// about the call existing: an announcement for a registration that never landed
// would re-drive an identity against interest nothing changed.
func TestControl_PersonalRegisterDeregister_AnnounceOnTheInterestChangeEdge(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-edge")
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalInterestKV(kv)

	var announced []string
	svc.SetInterestChangeSink(func(identityID string) { announced = append(announced, identityID) })
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	call := func(verb string, body control.ControlRequest) control.ControlResponse {
		t.Helper()
		data, err := json.Marshal(body)
		require.NoError(t, err)
		reply, err := nc.Request(control.ControlSubject("personal", verb), data, 2*time.Second)
		require.NoError(t, err)
		var resp control.ControlResponse
		require.NoError(t, json.Unmarshal(reply.Data, &resp))
		return resp
	}

	require.Empty(t, call("register", control.ControlRequest{
		IdentityID: "identityA", DeviceID: "deviceX", Types: []string{"lease"},
	}).Error)
	assert.Equal(t, []string{"identityA"}, announced,
		"a registration both narrows and widens what IsRelevant admits, so it must announce")

	require.Empty(t, call("deregister", control.ControlRequest{
		IdentityID: "identityA", DeviceID: "deviceX",
	}).Error)
	assert.Equal(t, []string{"identityA", "identityA"}, announced,
		"removing the last device widens IsRelevant to admit everything, so it must announce too")

	require.NotEmpty(t, call("register", control.ControlRequest{DeviceID: "deviceX"}).Error)
	assert.Len(t, announced, 2,
		"a request refused before the write must announce nothing — the edge names a change that happened")
}

// TestControl_PersonalHydrate_AnnouncesOnlyWhenItCreatesTheRegistration pins
// the Interest Set's FOURTH writer (personal-lens-derivation-licence-design.md
// §4.2's edge, found by review).
//
// personal.hydrate records a revision cursor as bookkeeping, and for an
// unregistered device that write CREATES the registration through
// SetRevisionCursor's kv.Create arm — carrying no types and no anchors, which
// IsRelevant reads as admit-everything. So the hydrate of an unregistered
// device WIDENS what the identity's personal lenses publish, exactly as a
// filterless register would, and owes the same announcement.
//
// The update half is what makes it a claim about the mechanism: hydrating a
// device that is already registered touches no filter, so announcing there
// would drive a reprojection of an identity whose interest did not move — and
// a hydrate already republishes every row itself.
func TestControl_PersonalHydrate_AnnouncesOnlyWhenItCreatesTheRegistration(t *testing.T) {
	nc, js := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kv := makeKV(t, nc, js, "refractor-test-personal-interest-hydrate-edge")
	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetPersonalInterestKV(kv)
	svc.RegisterPersonalHydrator("lens-a", &fakeHydrator{revision: 42})

	var announced []string
	svc.SetInterestChangeSink(func(identityID string) { announced = append(announced, identityID) })
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	hydrate := func(deviceID string) control.ControlResponse {
		t.Helper()
		data, err := json.Marshal(control.ControlRequest{IdentityID: "identityA", DeviceID: deviceID})
		require.NoError(t, err)
		reply, err := nc.Request(control.ControlSubject("personal", "hydrate"), data, 5*time.Second)
		require.NoError(t, err)
		var resp control.ControlResponse
		require.NoError(t, json.Unmarshal(reply.Data, &resp))
		return resp
	}

	require.Empty(t, hydrate("deviceNew").Error)
	assert.Equal(t, []string{"identityA"}, announced,
		"hydrating an UNREGISTERED device creates a filterless registration, which widens IsRelevant to admit everything")

	require.Empty(t, hydrate("deviceNew").Error)
	assert.Equal(t, []string{"identityA"}, announced,
		"hydrating the same device again only updates its cursor — no filter moved, so nothing may be announced")
}
