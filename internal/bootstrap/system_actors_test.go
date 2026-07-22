package bootstrap_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestSystemActorKeys_DiscoversByOperatorTopology proves the
// root-designation-topology-reconverge (2026-07-03) discovery mechanism:
// SystemActorKeys finds exactly the identities holding the primordial
// `operator` role via `holdsRole` (Contract #7 §7.7) — never a
// `data.protected` bit alone, and never a revoked (tombstoned) holdsRole
// link — matching the same predicate the Capability Lens anchor cypher gates
// on (internal/bootstrap/lenses.go).
func TestSystemActorKeys_DiscoversByOperatorTopology(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)
	seedFresh(t, nc, logger)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --- baseline: the 6 primordially-seeded operator holders ---
	want := []string{
		bootstrap.BootstrapIdentityKey,
		bootstrap.LoomIdentityKey,
		bootstrap.WeaverIdentityKey,
		bootstrap.BridgeIdentityKey,
		bootstrap.ObjmgrIdentityKey,
		bootstrap.PrivacyIdentityKey,
	}
	got, err := bootstrap.SystemActorKeys(ctx, conn)
	require.NoError(t, err)
	require.ElementsMatch(t, want, got, "baseline discovery must match exactly the 6 primordial operator holders")

	// --- a protected:true identity with NO holdsRole link must NOT appear ---
	forgedID, err := substrate.NewNanoID()
	require.NoError(t, err)
	forgedKey := substrate.VertexKey("identity", forgedID)
	forgedBody, err := bootstrap.MakeVertexEnvelope(forgedKey, "identity", map[string]any{"protected": true})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bootstrap.CoreKVBucket, forgedKey, forgedBody)
	require.NoError(t, err)

	got, err = bootstrap.SystemActorKeys(ctx, conn)
	require.NoError(t, err)
	require.ElementsMatch(t, want, got,
		"a protected:true identity with no holdsRole link must not be discovered as a system actor")

	// --- a freshly-granted (non-primordial) operator holder MUST appear —
	// proves discovery is topology-driven, not a fixed enumerated set ---
	grantedID, err := substrate.NewNanoID()
	require.NoError(t, err)
	grantedKey := substrate.VertexKey("identity", grantedID)
	grantedBody, err := bootstrap.MakeVertexEnvelope(grantedKey, "identity", map[string]any{"name": "runtime-granted"})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bootstrap.CoreKVBucket, grantedKey, grantedBody)
	require.NoError(t, err)

	grantedLinkKey := substrate.LinkKey("identity", grantedID, "holdsRole", "role", bootstrap.RoleOperatorID)
	grantedLinkBody, err := bootstrap.MakeLinkEnvelope(grantedLinkKey, grantedKey, bootstrap.RoleOperatorKey, "holdsRole", "holdsRole", nil)
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bootstrap.CoreKVBucket, grantedLinkKey, grantedLinkBody)
	require.NoError(t, err)

	got, err = bootstrap.SystemActorKeys(ctx, conn)
	require.NoError(t, err)
	require.ElementsMatch(t, append(append([]string{}, want...), grantedKey), got,
		"a runtime-granted holdsRole->operator link must be discovered")

	// --- revoking (tombstoning) that same link must remove it again ---
	var env map[string]any
	require.NoError(t, json.Unmarshal(grantedLinkBody, &env))
	env["isDeleted"] = true
	revokedLinkBody, err := json.Marshal(env)
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bootstrap.CoreKVBucket, grantedLinkKey, revokedLinkBody)
	require.NoError(t, err)

	got, err = bootstrap.SystemActorKeys(ctx, conn)
	require.NoError(t, err)
	require.ElementsMatch(t, want, got,
		"a revoked (tombstoned) holdsRole->operator link must no longer be discovered")
}

// TestPrivacyActorKey_DiscoversSeeded proves PrivacyActorKey finds the
// kernel-seeded privacy-plane service actor by class, matching the identity
// primordial.go seeds under bootstrap.PrivacyIdentityKey.
func TestPrivacyActorKey_DiscoversSeeded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)
	seedFresh(t, nc, logger)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got, err := bootstrap.PrivacyActorKey(ctx, conn)
	require.NoError(t, err)
	require.Equal(t, bootstrap.PrivacyIdentityKey, got)
}

// TestPrivacyActorKey_AbsentReturnsEmpty proves the pre-version-15
// deployment case (no privacy-plane actor seeded yet): PrivacyActorKey
// returns "" with no error rather than failing the caller's startup —
// crypto-shred finalization recording is disabled, not a hard requirement.
func TestPrivacyActorKey_AbsentReturnsEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, seeder.ProvisionBuckets(ctx))

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	got, err := bootstrap.PrivacyActorKey(ctx, conn)
	require.NoError(t, err)
	require.Equal(t, "", got, "no identities seeded yet — must return empty, not error")
}
