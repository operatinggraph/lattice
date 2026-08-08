// RegistryProbe (refractor-lens-registry-restart-integrity-design.md §4 Fire
// B step 2) — internal test package so tests can shrink graceWindow/
// tickInterval and call check directly rather than waiting out the real
// production cadence (60s grace + 10min tick).
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func newRegistryProbeTestConn(t *testing.T) (*substrate.Conn, context.Context) {
	t.Helper()
	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	js := conn.JetStream()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)

	return conn, ctx
}

func putLens(ctx context.Context, t *testing.T, conn *substrate.Conn, bucket, id string, deleted bool, targetConfig string) {
	t.Helper()
	vtxKey := "vtx.meta." + id
	vtxBody, err := json.Marshal(map[string]any{"id": id, "class": "meta.lens", "isDeleted": deleted})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bucket, vtxKey, vtxBody)
	require.NoError(t, err)
	if targetConfig == "" {
		return
	}
	specBody, err := json.Marshal(map[string]any{
		"id":            id,
		"canonicalName": "lens." + id,
		"targetType":    "nats_kv",
		"cypherRule":    "MATCH (c:contract) RETURN c.id AS contract_id",
		"targetConfig":  json.RawMessage(targetConfig),
	})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bucket, vtxKey+".spec", specBody)
	require.NoError(t, err)
}

func putEventStreamLens(ctx context.Context, t *testing.T, conn *substrate.Conn, bucket, id string) {
	t.Helper()
	vtxKey := "vtx.meta." + id
	vtxBody, err := json.Marshal(map[string]any{"id": id, "class": "meta.lens"})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bucket, vtxKey, vtxBody)
	require.NoError(t, err)
	specBody, err := json.Marshal(map[string]any{
		"id":            id,
		"canonicalName": "lens." + id,
		"source":        map[string]any{"kind": "eventStream"},
	})
	require.NoError(t, err)
	_, err = conn.KVPut(ctx, bucket, vtxKey+".spec", specBody)
	require.NoError(t, err)
}

func TestRegistryProbe_MissingDeclaredNotRegistered(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"

	putLens(ctx, t, conn, bucket, "AbCdEfGhJkMnPqRsTuVw", false, `{"bucket":"contract_view","key":["contract_id"]}`)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := NewRegistryProbe(conn, bucket, func() []string { return nil }, logger)
	p.check(ctx)

	missing := p.Missing()
	if len(missing) != 1 || missing[0] != "AbCdEfGhJkMnPqRsTuVw" {
		t.Fatalf("Missing() = %v, want [AbCdEfGhJkMnPqRsTuVw]", missing)
	}
}

func TestRegistryProbe_RegisteredIsNotMissing(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"

	putLens(ctx, t, conn, bucket, "AbCdEfGhJkMnPqRsTuVw", false, `{"bucket":"contract_view","key":["contract_id"]}`)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := NewRegistryProbe(conn, bucket, func() []string { return []string{"AbCdEfGhJkMnPqRsTuVw"} }, logger)
	p.check(ctx)

	if missing := p.Missing(); len(missing) != 0 {
		t.Fatalf("a registered lens must not be reported missing, got %v", missing)
	}
}

func TestRegistryProbe_SkipsSoftDeletedVertex(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"

	putLens(ctx, t, conn, bucket, "AbCdEfGhJkMnPqRsTuVw", true, `{"bucket":"contract_view","key":["contract_id"]}`)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := NewRegistryProbe(conn, bucket, func() []string { return nil }, logger)
	p.check(ctx)

	if missing := p.Missing(); len(missing) != 0 {
		t.Fatalf("a soft-deleted lens vertex must never be reported missing, got %v", missing)
	}
}

func TestRegistryProbe_SkipsEventStreamSpec(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"

	putEventStreamLens(ctx, t, conn, bucket, "EvStrmLensAbCdEfGhJk")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := NewRegistryProbe(conn, bucket, func() []string { return nil }, logger)
	p.check(ctx)

	if missing := p.Missing(); len(missing) != 0 {
		t.Fatalf("a Chronicler-owned eventStream spec must never be reported missing, got %v", missing)
	}
}

// TestRegistryProbe_MissingSpecStillCountsAsDeclared proves the deliberate
// fail-closed choice (§4 Fire B step 2): a vertex whose .spec fetch fails
// (never arrived, or any other error) is still counted as declared, so an
// activation that never completes still alarms rather than being quietly
// excluded.
func TestRegistryProbe_MissingSpecStillCountsAsDeclared(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"

	putLens(ctx, t, conn, bucket, "AbCdEfGhJkMnPqRsTuVw", false, "") // vertex only, no spec

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := NewRegistryProbe(conn, bucket, func() []string { return nil }, logger)
	p.check(ctx)

	missing := p.Missing()
	if len(missing) != 1 || missing[0] != "AbCdEfGhJkMnPqRsTuVw" {
		t.Fatalf("a vertex with no spec must still count as declared, got %v", missing)
	}
}

// TestRegistryProbe_TransientListFailureKeepsPriorResult proves a KV-list
// error leaves the prior Missing() snapshot in place rather than clearing it
// — a transient error must never look like "registry reconciled clean".
func TestRegistryProbe_TransientListFailureKeepsPriorResult(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"
	putLens(ctx, t, conn, bucket, "AbCdEfGhJkMnPqRsTuVw", false, `{"bucket":"contract_view","key":["contract_id"]}`)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := NewRegistryProbe(conn, bucket, func() []string { return nil }, logger)
	p.check(ctx)
	if missing := p.Missing(); len(missing) != 1 {
		t.Fatalf("setup: expected 1 missing lens, got %v", missing)
	}

	badCtx, cancel := context.WithCancel(ctx)
	cancel() // an already-cancelled ctx makes the next KVListKeysPrefix fail
	p.check(badCtx)

	if missing := p.Missing(); len(missing) != 1 {
		t.Fatalf("a failed check must preserve the prior Missing() snapshot, got %v", missing)
	}
}

func TestRegistryProbe_RunHonorsGraceWindowThenTicks(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"
	putLens(ctx, t, conn, bucket, "AbCdEfGhJkMnPqRsTuVw", false, `{"bucket":"contract_view","key":["contract_id"]}`)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := NewRegistryProbe(conn, bucket, func() []string { return nil }, logger)
	p.graceWindow = 50 * time.Millisecond
	p.tickInterval = 50 * time.Millisecond

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go p.Run(runCtx)

	require.Eventually(t, func() bool {
		return len(p.Missing()) == 1
	}, 3*time.Second, 10*time.Millisecond, "Run must reconcile after the (shrunk) grace window")
}

// ReconcileNow answers the question on demand, for a caller that cannot wait for
// a tick: the retention-class destruction consumer, which must not attest an
// erasure off an enumeration taken over a registry that has not finished
// loading. Same computation as the periodic check, returned rather than only
// stored.
func TestRegistryProbe_ReconcileNowAnswersOnDemand(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"
	putLens(ctx, t, conn, bucket, "AbCdEfGhJkMnPqRsTuVw", false, `{"bucket":"contract_view","key":["contract_id"]}`)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	registered := []string{}
	p := NewRegistryProbe(conn, bucket, func() []string { return registered }, logger)

	missing, err := p.ReconcileNow(ctx)
	if err != nil {
		t.Fatalf("ReconcileNow: %v", err)
	}
	if len(missing) != 1 || missing[0] != "AbCdEfGhJkMnPqRsTuVw" {
		t.Fatalf("a declared-but-unregistered lens must be reported, got %v", missing)
	}

	registered = []string{"AbCdEfGhJkMnPqRsTuVw"}
	missing, err = p.ReconcileNow(ctx)
	if err != nil {
		t.Fatalf("ReconcileNow after registration: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("a registered lens is not missing, got %v", missing)
	}
}

// An enumeration failure is UNKNOWN, never "reconciled clean". Folding it into
// an empty slice is exactly how a caller gating an erasure attestation on this
// would read a dead Core KV as "no lens declares that holder type".
func TestRegistryProbe_ReconcileNowFailureIsNotAnEmptyMissingSet(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"
	putLens(ctx, t, conn, bucket, "AbCdEfGhJkMnPqRsTuVw", false, `{"bucket":"contract_view","key":["contract_id"]}`)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := NewRegistryProbe(conn, bucket, func() []string { return nil }, logger)

	badCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.ReconcileNow(badCtx); err == nil {
		t.Fatal("a failed enumeration must return an error, not an empty missing set")
	}
}

// Corpus-global readiness answers a question nobody asked: any single lens that
// never registers, for any reason, would withhold every retention-class
// attestation for every holder type forever. Scoped to the DECLARING set, "not
// ready" means lenses that might be serving this holder's plaintext are missing.
func TestRegistryProbe_ReconcileNowForHolderType_IgnoresLensesThatCannotHoldIt(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"

	// Unregistered, and declares a DIFFERENT holder type: irrelevant here.
	putLens(ctx, t, conn, bucket, "AbCdEfGhJkMnPqRsTuVw", false,
		`{"bucket":"identity_view","key":["id"],"secureColumns":[{"column":"name","holderTypes":["identity"]}]}`)
	// Unregistered, and declares the one being destroyed.
	putLens(ctx, t, conn, bucket, "BbCdEfGhJkMnPqRsTuVx", false,
		`{"bucket":"note_view","key":["id"],"secureColumns":[{"column":"note","holderTypes":["retentionclass"]}]}`)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := NewRegistryProbe(conn, bucket, func() []string { return nil }, logger)

	missing, err := p.ReconcileNowForHolderType(ctx, "retentionclass")
	if err != nil {
		t.Fatalf("ReconcileNowForHolderType: %v", err)
	}
	if len(missing) != 1 || missing[0] != "BbCdEfGhJkMnPqRsTuVx" {
		t.Fatalf("only the lens declaring the destroyed holder type is relevant, got %v", missing)
	}

	// The corpus-global form still sees both, so this is a narrowing of the
	// question rather than a change to what the probe can observe.
	all, err := p.ReconcileNow(ctx)
	if err != nil {
		t.Fatalf("ReconcileNow: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("the unscoped form must still report every unregistered lens, got %v", all)
	}
}

// Fail-closed at the edge: a lens whose spec cannot be parsed has UNKNOWN holder
// types, and unknown must never resolve to "not relevant" — that is the one
// direction that silently drops the lens whose plaintext is being asked about.
func TestRegistryProbe_ReconcileNowForHolderType_KeepsALensWithAnUnreadableSpec(t *testing.T) {
	conn, ctx := newRegistryProbeTestConn(t)
	const bucket = "core-kv"
	putLens(ctx, t, conn, bucket, "CbCdEfGhJkMnPqRsTuVy", false, `{"bucket":"v","key":["id"]}`)
	if _, err := conn.KVPut(ctx, bucket, "vtx.meta.CbCdEfGhJkMnPqRsTuVy.spec", []byte("{not json")); err != nil {
		t.Fatalf("corrupt the spec: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	p := NewRegistryProbe(conn, bucket, func() []string { return nil }, logger)

	missing, err := p.ReconcileNowForHolderType(ctx, "retentionclass")
	if err != nil {
		t.Fatalf("ReconcileNowForHolderType: %v", err)
	}
	if len(missing) != 1 {
		t.Fatalf("a lens with unknown holder types must be reported, got %v", missing)
	}
}
