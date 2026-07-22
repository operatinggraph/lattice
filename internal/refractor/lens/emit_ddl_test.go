package lens_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedSpec writes a meta-lens vertex + its spec aspect into the embedded
// Core KV (the shape CoreKVSource and EmitReadPathDDL both read).
func seedSpec(ctx context.Context, t *testing.T, kv jetstream.KeyValue, id string, spec lens.LensSpec) {
	t.Helper()
	vtxKey := "vtx.meta." + id
	require.NoError(t, putJSON(ctx, kv, vtxKey, map[string]any{"id": id, "class": "meta.lens"}))
	specJSON, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NoError(t, putJSON(ctx, kv, vtxKey+".spec", specJSON))
}

func TestEmitReadPathDDL(t *testing.T) {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := test.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)

	// A grant lens (projects to the shared actor_read_grants table).
	seedSpec(ctx, t, kv, "GrantLensaaaaaaaaaaa", lens.LensSpec{
		ID:            "GrantLensaaaaaaaaaaa",
		CanonicalName: "lens.cap-read.lease",
		TargetType:    "postgres",
		CypherRule:    "MATCH (g:grant) RETURN g.actor AS actor_id, g.anchor AS anchor_id, g.src AS grant_source",
		TargetConfig:  json.RawMessage(`{"grantTable":true}`),
	})
	// A protected business read model.
	seedSpec(ctx, t, kv, "ProtLensbbbbbbbbbbbb", lens.LensSpec{
		ID:            "ProtLensbbbbbbbbbbbb",
		CanonicalName: "lens.lease-applications",
		TargetType:    "postgres",
		CypherRule:    "MATCH (a:leaseApplication) RETURN a.id AS application_id",
		TargetConfig: json.RawMessage(`{
			"table":"read_lease_applications",
			"key":["application_id"],
			"protected":true,
			"columns":[{"name":"status","type":"text"}]
		}`),
	})
	// A plain (public) postgres lens — must be skipped.
	seedSpec(ctx, t, kv, "PlainLensccccccccccc", lens.LensSpec{
		ID:            "PlainLensccccccccccc",
		CanonicalName: "lens.listings",
		TargetType:    "postgres",
		CypherRule:    "MATCH (l:listing) RETURN l.id AS listing_id",
		TargetConfig:  json.RawMessage(`{"table":"read_listings","key":["listing_id"],"public":true}`),
	})
	// A nats_kv lens — must be skipped.
	seedSpec(ctx, t, kv, "KVLensdddddddddddddd", lens.LensSpec{
		ID:            "KVLensdddddddddddddd",
		CanonicalName: "lens.contract-view",
		TargetType:    "nats_kv",
		CypherRule:    "MATCH (c:contract) RETURN c.id AS contract_id",
		TargetConfig:  json.RawMessage(`{"bucket":"contract_view","key":["contract_id"]}`),
	})

	stmts, err := lens.EmitReadPathDDL(ctx, conn, "core-kv")
	require.NoError(t, err)
	require.NotEmpty(t, stmts)

	joined := strings.Join(stmts, "\n")
	// The grant table comes first (protected policies reference it).
	require.Contains(t, joined, adapter.GrantTable)
	grantIdx := indexOfStmt(stmts, adapter.GrantTable)
	protIdx := indexOfStmt(stmts, "read_lease_applications")
	require.GreaterOrEqual(t, grantIdx, 0, "grant table DDL emitted")
	require.GreaterOrEqual(t, protIdx, 0, "protected table DDL emitted")
	require.Less(t, grantIdx, protIdx, "grant table DDL must precede the protected table DDL")

	// The protected table DDL is exactly what BuildProtectedTableDDL produces
	// (so the operator-applied table passes VerifyProtectedTable).
	want, err := adapter.BuildProtectedTableDDL("read_lease_applications", []string{"application_id"}, []adapter.ColumnDef{{Name: "status", Type: "text"}})
	require.NoError(t, err)
	for _, w := range want {
		require.Contains(t, stmts, w)
	}
	// The grant table DDL matches BuildGrantTableDDL.
	for _, w := range adapter.BuildGrantTableDDL() {
		require.Contains(t, stmts, w)
	}

	// Public + nats_kv lenses contributed no DDL.
	require.NotContains(t, joined, "read_listings")
	require.NotContains(t, joined, "contract_view")

	// FORCE ROW LEVEL SECURITY is present (the security-load-bearing posture).
	require.Contains(t, joined, "FORCE ROW LEVEL SECURITY")

	// The rendered script is semicolon-terminated and non-empty.
	script := lens.ReadPathDDLScript(stmts)
	require.True(t, strings.HasSuffix(strings.TrimSpace(script), ";"))
	require.Equal(t, len(stmts), strings.Count(script, ";\n"))
}

func TestEmitReadPathDDL_NoReadPathLenses(t *testing.T) {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := test.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)

	// Only a plain nats_kv lens installed — no protected/grant lens.
	seedSpec(ctx, t, kv, "KVLensdddddddddddddd", lens.LensSpec{
		ID:            "KVLensdddddddddddddd",
		CanonicalName: "lens.contract-view",
		TargetType:    "nats_kv",
		CypherRule:    "MATCH (c:contract) RETURN c.id AS contract_id",
		TargetConfig:  json.RawMessage(`{"bucket":"contract_view","key":["contract_id"]}`),
	})

	stmts, err := lens.EmitReadPathDDL(ctx, conn, "core-kv")
	require.NoError(t, err)
	require.Empty(t, stmts, "no grant table is emitted when no protected/grant lens is installed")
	require.Empty(t, lens.ReadPathDDLScript(stmts))
}

// TestEmitReadPathDDL_TombstonedSpec_Skipped proves a tombstoned (isDeleted)
// lens spec contributes no DDL — Core KV holds tombstoned entries as live KV
// reads (substrate.Conn.KVGet's doc comment), so EmitReadPathDDL, a raw
// consumer, must inspect the envelope's isDeleted field itself rather than
// treating every enumerated spec as active.
func TestEmitReadPathDDL_TombstonedSpec_Skipped(t *testing.T) {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := test.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)

	// A live grant lens keeps needGrantTable true so the test isolates the
	// tombstoned protected lens's own exclusion.
	seedSpec(ctx, t, kv, "GrantLensaaaaaaaaaaa", lens.LensSpec{
		ID:            "GrantLensaaaaaaaaaaa",
		CanonicalName: "lens.cap-read.lease",
		TargetType:    "postgres",
		CypherRule:    "MATCH (g:grant) RETURN g.actor AS actor_id, g.anchor AS anchor_id, g.src AS grant_source",
		TargetConfig:  json.RawMessage(`{"grantTable":true}`),
	})

	// A protected lens whose spec aspect is tombstoned (TombstoneMetaVertex's
	// shape: an envelope with isDeleted:true wrapping the spec under "data").
	spec := lens.LensSpec{
		ID:            "ProtLensbbbbbbbbbbbb",
		CanonicalName: "lens.lease-applications",
		TargetType:    "postgres",
		CypherRule:    "MATCH (a:leaseApplication) RETURN a.id AS application_id",
		TargetConfig: json.RawMessage(`{
			"table":"read_lease_applications",
			"key":["application_id"],
			"protected":true,
			"columns":[{"name":"status","type":"text"}]
		}`),
	}
	vtxKey := "vtx.meta.ProtLensbbbbbbbbbbbb"
	require.NoError(t, putJSON(ctx, kv, vtxKey, map[string]any{"id": spec.ID, "class": "meta.lens"}))
	require.NoError(t, putJSON(ctx, kv, vtxKey+".spec", map[string]any{
		"class": "meta.lens", "vertexKey": vtxKey, "localName": "spec", "isDeleted": true, "data": spec,
	}))

	stmts, err := lens.EmitReadPathDDL(ctx, conn, "core-kv")
	require.NoError(t, err)
	joined := strings.Join(stmts, "\n")
	require.Contains(t, joined, adapter.GrantTable, "the live grant lens still contributes DDL")
	require.NotContains(t, joined, "read_lease_applications", "a tombstoned protected lens must contribute no DDL")
}

// TestEmitReadPathDDL_ProtectedSoftDelete_Rejected proves EmitReadPathDDL
// rejects the same protected+deleteMode:soft combination translateSpec
// rejects at lens-activation time (validateProtectedDeleteMode, shared by
// both) — the DDL emitter must not diverge from the loader's view of "is this
// lens spec coherent" and silently provision a table for a lens that can never
// activate.
func TestEmitReadPathDDL_ProtectedSoftDelete_Rejected(t *testing.T) {
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := test.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)

	seedSpec(ctx, t, kv, "ProtLensbbbbbbbbbbbb", lens.LensSpec{
		ID:            "ProtLensbbbbbbbbbbbb",
		CanonicalName: "lens.lease-applications",
		TargetType:    "postgres",
		CypherRule:    "MATCH (a:leaseApplication) RETURN a.id AS application_id",
		TargetConfig: json.RawMessage(`{
			"table":"read_lease_applications",
			"key":["application_id"],
			"protected":true,
			"deleteMode":"soft",
			"columns":[{"name":"status","type":"text"}]
		}`),
	})

	_, err = lens.EmitReadPathDDL(ctx, conn, "core-kv")
	require.Error(t, err)
	require.Contains(t, err.Error(), "deleteMode \"soft\"")
}

// indexOfStmt returns the index of the first statement containing substr, or -1.
func indexOfStmt(stmts []string, substr string) int {
	for i, s := range stmts {
		if strings.Contains(s, substr) {
			return i
		}
	}
	return -1
}
