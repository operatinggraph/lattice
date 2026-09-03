package personalinterest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/personalinterest"
)

// seedDevice is one registration a relevance case seeds, in Register's
// argument order.
type seedDevice struct {
	deviceID string
	types    []string
	anchors  []string
}

// TestRelevantIn_MatchesIsRelevant pins the factoring: for every shape of
// registration set the relevance filter recognises, the pure predicate and
// the read-then-decide entry point answer the same thing. IsRelevant is the
// shipped behaviour, so the property that matters is agreement — a caller
// switching to one read per evaluation must not change what any device
// receives.
func TestRelevantIn_MatchesIsRelevant(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	cases := []struct {
		name       string
		devices    []seedDevice
		anchorType string
		anchorID   string
		want       bool
	}{
		{
			name:       "no registration admits",
			anchorType: "lease", anchorID: "lease.1", want: true,
		},
		{
			name:       "empty filter admits",
			devices:    []seedDevice{{"deviceX", nil, nil}},
			anchorType: "lease", anchorID: "lease.1", want: true,
		},
		{
			name:       "type match admits",
			devices:    []seedDevice{{"deviceX", []string{"lease"}, nil}},
			anchorType: "lease", anchorID: "lease.1", want: true,
		},
		{
			name:       "type miss denies",
			devices:    []seedDevice{{"deviceX", []string{"lease"}, nil}},
			anchorType: "payment", anchorID: "payment.9", want: false,
		},
		{
			name:       "anchor match admits",
			devices:    []seedDevice{{"deviceX", nil, []string{"lease.1"}}},
			anchorType: "lease", anchorID: "lease.1", want: true,
		},
		{
			name:       "anchor miss denies",
			devices:    []seedDevice{{"deviceX", nil, []string{"lease.1"}}},
			anchorType: "lease", anchorID: "lease.2", want: false,
		},
		{
			name:       "anchor match admits on a type the device never declared",
			devices:    []seedDevice{{"deviceX", []string{"payment"}, []string{"lease.1"}}},
			anchorType: "lease", anchorID: "lease.1", want: true,
		},
		{
			name: "any of several devices matching admits",
			devices: []seedDevice{
				{"deviceX", []string{"payment"}, nil},
				{"deviceY", []string{"lease"}, nil},
			},
			anchorType: "lease", anchorID: "lease.1", want: true,
		},
		{
			name: "no device matching denies",
			devices: []seedDevice{
				{"deviceX", []string{"payment"}, nil},
				{"deviceY", nil, []string{"lease.9"}},
			},
			anchorType: "lease", anchorID: "lease.1", want: false,
		},
		{
			name: "one unfiltered device among filtered ones admits",
			devices: []seedDevice{
				{"deviceX", []string{"payment"}, nil},
				{"deviceY", nil, nil},
			},
			anchorType: "lease", anchorID: "lease.1", want: true,
		},
		{
			name:       "an empty anchor type never matches a declared type",
			devices:    []seedDevice{{"deviceX", []string{""}, nil}},
			anchorType: "", anchorID: "lease.1", want: false,
		},
		{
			name:       "an empty anchor id never matches a declared anchor",
			devices:    []seedDevice{{"deviceX", nil, []string{""}}},
			anchorType: "lease", anchorID: "", want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kv := newTestKV(t)
			ctx := context.Background()
			registeredAt := time.Now().UTC().Format(time.RFC3339)
			for _, d := range tc.devices {
				require.NoError(t, personalinterest.Register(ctx, kv, "identityA", d.deviceID, d.types, d.anchors, registeredAt))
			}

			live, err := personalinterest.IsRelevant(ctx, kv, "identityA", tc.anchorType, tc.anchorID)
			require.NoError(t, err)

			regs, err := personalinterest.Registrations(ctx, kv, "identityA")
			require.NoError(t, err)
			require.Len(t, regs, len(tc.devices), "every seeded device must come back as one registration")

			require.Equal(t, live, personalinterest.RelevantIn(regs, tc.anchorType, tc.anchorID),
				"the pure predicate must answer exactly what the live read answers")
			require.Equal(t, tc.want, live)
		})
	}
}

// TestRegistrations_ScopedToIdentityPrefix mirrors
// TestIsRelevant_ScopedToIdentityPrefix for the batched read: a registration
// belonging to a DIFFERENT identity that happens to share a device-id suffix
// must never appear in this identity's set — the whole set is what the
// relevance decision is then made against, so a leak here widens what one
// identity's devices receive on another's behalf.
func TestRegistrations_ScopedToIdentityPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	kv := newTestKV(t)
	ctx := context.Background()
	registeredAt := time.Now().UTC().Format(time.RFC3339)

	require.NoError(t, personalinterest.Register(ctx, kv, "identityA", "deviceX", []string{"lease"}, nil, registeredAt))
	require.NoError(t, personalinterest.Register(ctx, kv, "identityAB", "deviceX", []string{"payment"}, nil, registeredAt))
	require.NoError(t, personalinterest.Register(ctx, kv, "identityB", "deviceX", nil, nil, registeredAt))

	regs, err := personalinterest.Registrations(ctx, kv, "identityA")
	require.NoError(t, err)
	require.Len(t, regs, 1, "only identityA's own device may be read; a longer identity id sharing the prefix is a different subject")
	require.Equal(t, []string{"lease"}, regs[0].Types)

	require.True(t, personalinterest.RelevantIn(regs, "lease", "lease.1"))
	require.False(t, personalinterest.RelevantIn(regs, "payment", "payment.1"),
		"identityAB's declared type must not widen identityA's filter")
	require.False(t, personalinterest.RelevantIn(regs, "anything", "anything.1"),
		"identityB's unfiltered registration must not read as identityA's own admit-everything")

	unregistered, err := personalinterest.Registrations(ctx, kv, "identityNone")
	require.NoError(t, err)
	require.Empty(t, unregistered, "an identity with no device must read as an empty set, not an error")
	require.True(t, personalinterest.RelevantIn(unregistered, "lease", "lease.1"),
		"no registration admits everything")
}
