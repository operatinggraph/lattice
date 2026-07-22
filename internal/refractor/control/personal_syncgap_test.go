package control_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/control"
)

// syncgapReq marshals a syncgap ControlRequest carrying only the cursor the
// op consults (identityId/deviceId travel for symmetry with the sibling ops).
func syncgapReq(t *testing.T, identityID string, cursor uint64) []byte {
	t.Helper()
	data, err := json.Marshal(control.ControlRequest{IdentityID: identityID, DeviceID: "D1", Cursor: cursor})
	require.NoError(t, err)
	return data
}

func TestControl_PersonalSyncGap_NotConfigured_FailsClosed(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	// SetSyncFirstSeq deliberately not called → the op must fail closed.
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	reply, err := nc.Request(control.ControlSubject("personal", "syncgap"), syncgapReq(t, "AAAAAAAAAAAAAAAAAAAA", 5), 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.Contains(t, resp.Error, "stream state not configured")
	assert.Nil(t, resp.PersonalSyncGap, "an unconfigured seam must never answer gapped=false (silent-data-loss direction)")
}

func TestControl_PersonalSyncGap_SeamError_Surfaces(t *testing.T) {
	nc, _ := startControlTestServerConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := control.NewService()
	svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	svc.SetSyncFirstSeq(func(context.Context) (uint64, error) { return 0, errors.New("stream boom") })
	require.NoError(t, svc.StartNATSListener(ctx, nc))

	reply, err := nc.Request(control.ControlSubject("personal", "syncgap"), syncgapReq(t, "AAAAAAAAAAAAAAAAAAAA", 5), 2*time.Second)
	require.NoError(t, err)
	var resp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &resp))

	assert.Contains(t, resp.Error, "read stream state")
	assert.Nil(t, resp.PersonalSyncGap)
}

// TestControl_PersonalSyncGap_Boundaries pins the cursor<firstSeq comparison at
// its three interesting points (§8): cursor 0 is always gapped (max-conservative
// → re-hydrate), cursor one below the watermark is gapped, cursor exactly at the
// watermark is fresh.
func TestControl_PersonalSyncGap_Boundaries(t *testing.T) {
	const firstSeq = uint64(42)
	cases := []struct {
		name       string
		cursor     uint64
		wantGapped bool
	}{
		{"zero cursor is always gapped", 0, true},
		{"one below the watermark is gapped", firstSeq - 1, true},
		{"exactly at the watermark is fresh", firstSeq, false},
		{"far ahead of the watermark is fresh", firstSeq + 1000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nc, _ := startControlTestServerConn(t)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			svc := control.NewService()
			svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
			svc.SetSyncFirstSeq(func(context.Context) (uint64, error) { return firstSeq, nil })
			require.NoError(t, svc.StartNATSListener(ctx, nc))

			reply, err := nc.Request(control.ControlSubject("personal", "syncgap"), syncgapReq(t, "AAAAAAAAAAAAAAAAAAAA", tc.cursor), 2*time.Second)
			require.NoError(t, err)
			var resp control.ControlResponse
			require.NoError(t, json.Unmarshal(reply.Data, &resp))

			require.Empty(t, resp.Error)
			require.NotNil(t, resp.PersonalSyncGap)
			assert.Equal(t, tc.wantGapped, resp.PersonalSyncGap.Gapped)
		})
	}
}

// TestControl_PersonalSyncGap_VerifiedActorBinding proves the §3.4 identity
// binding is applied uniformly to syncgap even though the answer is
// identity-independent: a body identityId that disagrees with the verified
// actor is rejected (the op never starts default-open for a future
// per-identity refinement), and an empty body identityId is filled from the
// verified actor.
func TestControl_PersonalSyncGap_VerifiedActorBinding(t *testing.T) {
	t.Run("mismatched body identity is rejected", func(t *testing.T) {
		nc, _ := startControlTestServerConn(t)
		av, sign := newIdentityBindingVerifier(t)

		svc := control.NewService()
		svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
		svc.SetActorVerifier(av)
		svc.SetSyncFirstSeq(func(context.Context) (uint64, error) { return 1, nil })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, svc.StartNATSListener(ctx, nc))

		msg := personalRequestMsg(t, control.ControlSubject("personal", "syncgap"), sign(identityBindingTestA),
			control.ControlRequest{IdentityID: identityBindingTestB, Cursor: 5})
		reply, err := nc.RequestMsg(msg, 2*time.Second)
		require.NoError(t, err)
		var resp control.ControlResponse
		require.NoError(t, json.Unmarshal(reply.Data, &resp))

		assert.NotEmpty(t, resp.Error, "a body identityId disagreeing with the verified actor must be rejected")
		assert.Nil(t, resp.PersonalSyncGap)
	})

	t.Run("empty body identity is filled from the verified actor", func(t *testing.T) {
		nc, _ := startControlTestServerConn(t)
		av, sign := newIdentityBindingVerifier(t)

		svc := control.NewService()
		svc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
		svc.SetActorVerifier(av)
		svc.SetSyncFirstSeq(func(context.Context) (uint64, error) { return 10, nil })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, svc.StartNATSListener(ctx, nc))

		msg := personalRequestMsg(t, control.ControlSubject("personal", "syncgap"), sign(identityBindingTestA),
			control.ControlRequest{Cursor: 3})
		reply, err := nc.RequestMsg(msg, 2*time.Second)
		require.NoError(t, err)
		var resp control.ControlResponse
		require.NoError(t, json.Unmarshal(reply.Data, &resp))

		require.Empty(t, resp.Error)
		require.NotNil(t, resp.PersonalSyncGap)
		assert.True(t, resp.PersonalSyncGap.Gapped, "cursor 3 < firstSeq 10 is gapped")
	})
}
