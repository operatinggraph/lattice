package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/edge/store"
	edgesync "github.com/operatinggraph/lattice/internal/edge/sync"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

func openTestStore(t *testing.T, path string) store.Store {
	t.Helper()
	st, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestResolveDeviceID_IsStableAcrossStoreLifetimes is the whole point of
// persisting the id: the durable consumer it names resumes from its own ack
// floor, so a second reader of the same mirror must arrive at the same name
// or it orphans the first one's durable.
func TestResolveDeviceID_IsStableAcrossStoreLifetimes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirror.db")

	st1, err := store.Open(path)
	require.NoError(t, err)
	first, err := resolveDeviceID(st1)
	require.NoError(t, err)
	require.True(t, substrate.IsValidNanoID(first), "a device id is spliced into a NATS subject token; it must be a NanoID")
	require.NoError(t, st1.Close())

	st2 := openTestStore(t, path)
	second, err := resolveDeviceID(st2)
	require.NoError(t, err)
	require.Equal(t, first, second, "reopening the same mirror must yield the same device id")

	got, ok, err := readDeviceID(st2)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, first, got)
}

// TestResolveDeviceID_ReplacesAnUnusableValue — the id is a subject token on
// two independent paths (the durable's name and the auth callout's granted
// permission), so a corrupt one fails the connection outright rather than
// degrading. Minting over it is the only recovery.
func TestResolveDeviceID_ReplacesAnUnusableValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value json.RawMessage
	}{
		{"not a string", json.RawMessage(`{"id":"x"}`)},
		{"empty", json.RawMessage(`""`)},
		{"subject-reserved characters", json.RawMessage(`"has.dots.and>wildcards"`)},
		{"wrong length", json.RawMessage(`"short"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openTestStore(t, filepath.Join(t.TempDir(), "mirror.db"))
			require.NoError(t, st.PutLocal(deviceIDLocalName, tc.value))

			_, ok, err := readDeviceID(st)
			require.NoError(t, err)
			require.False(t, ok, "an unusable stored value must not be reported as a device id")

			id, err := resolveDeviceID(st)
			require.NoError(t, err)
			require.True(t, substrate.IsValidNanoID(id))

			again, err := resolveDeviceID(st)
			require.NoError(t, err)
			require.Equal(t, id, again, "the replacement must itself persist")
		})
	}
}

// TestReadDeviceID_AbsentOnAFreshMirror — a purge of an identity that never
// opened an engine on this host has no durable to reap, and must say so
// rather than minting one just to name it.
func TestReadDeviceID_AbsentOnAFreshMirror(t *testing.T) {
	st := openTestStore(t, filepath.Join(t.TempDir(), "mirror.db"))
	_, ok, err := readDeviceID(st)
	require.NoError(t, err)
	require.False(t, ok)
}

// TestEngineManager_RebuiltEngineReusesTheDeviceID is the leak itself, at the
// level it happens: every engine build used to mint a fresh device id, so
// each one created an `edge-sync-<identity>-<device>` durable nothing ever
// deleted. A rebuild must re-bind the durable the closed engine left.
func TestEngineManager_RebuiltEngineReusesTheDeviceID(t *testing.T) {
	t.Parallel()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	m := newEngineManager(ctx, engineManagerDeps{
		engineConfig: engineConfig{
			NATSURL:    url,
			GatewayURL: "http://127.0.0.1:1", // never dialed: no intents are enqueued here
			StoreDir:   t.TempDir(),
			Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		Signer: testDevSigner(t),
	})
	identity := testNanoID(t)

	eng1, err := m.Acquire(identity)
	require.NoError(t, err)
	require.True(t, substrate.IsValidNanoID(eng1.deviceID))

	// Force the rebuild path (Acquire closes a permanently-dead engine and
	// builds a replacement) — the exact moment a fresh id used to be minted.
	eng1.conn.NATS().Close()
	eng2, err := m.Acquire(identity)
	require.NoError(t, err)
	require.NotSame(t, eng1, eng2)
	require.Equal(t, eng1.deviceID, eng2.deviceID, "a rebuilt engine must reuse the device id, not orphan its durable")

	m.Release(identity)
	m.Release(identity)
	eng2.Close()
}

// TestEngineManager_PurgeReapsTheSyncDurable — a purge deletes the mirror the
// device id lives in, so the durable it names would outlive its only reader.
// Sign-out must take both.
func TestEngineManager_PurgeReapsTheSyncDurable(t *testing.T) {
	t.Parallel()
	url := testutil.StartEmbeddedNATS(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url})
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	identity := testNanoID(t)
	dir := t.TempDir()
	storePath := filepath.Join(dir, identity+".db")

	st, err := store.Open(storePath)
	require.NoError(t, err)
	deviceID, err := resolveDeviceID(st)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	durable := edgesync.DurableName(identity, deviceID)
	_, err = conn.JetStream().CreateStream(ctx, jetstream.StreamConfig{
		Name:     edgesync.DefaultStream,
		Subjects: []string{"lattice.sync.user.*"},
	})
	require.NoError(t, err)
	_, err = conn.JetStream().CreateConsumer(ctx, edgesync.DefaultStream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: "lattice.sync.user." + identity,
	})
	require.NoError(t, err)

	m := newEngineManager(ctx, engineManagerDeps{
		engineConfig: engineConfig{
			NATSURL:  url,
			StoreDir: dir,
			Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		Signer: testDevSigner(t),
	})
	require.NoError(t, m.Purge(identity))

	_, err = conn.JetStream().Consumer(ctx, edgesync.DefaultStream, durable)
	require.ErrorIs(t, err, jetstream.ErrConsumerNotFound, "the purged identity's sync durable must be gone")
}
