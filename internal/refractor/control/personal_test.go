package control_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/control"
	"github.com/asolgan/lattice/internal/refractor/personalinterest"
)

func TestControl_PersonalRegister_NoKVConfigured_FailsClosed(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
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
