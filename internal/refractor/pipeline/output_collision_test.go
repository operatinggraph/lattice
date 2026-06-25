package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/failure"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/simple"
	"github.com/asolgan/lattice/internal/substrate"
)

// writeResult builds a non-delete EvalResult carrying the given output key.
func writeResult(key string) simple.EvalResult {
	return simple.EvalResult{Keys: map[string]any{"key": key}, Row: map[string]any{"x": 1}}
}

// deleteResult builds a delete EvalResult carrying the given output key.
func deleteResultFor(key string) simple.EvalResult {
	return simple.EvalResult{Delete: true, Keys: map[string]any{"key": key}}
}

func TestDetectOutputKeyCollision(t *testing.T) {
	cases := []struct {
		name      string
		results   []simple.EvalResult
		wantFound bool
		wantKey   string
		wantCount int
	}{
		{
			name:      "single row — the common one-row-per-anchor path",
			results:   []simple.EvalResult{writeResult("roster.identity.A")},
			wantFound: false,
		},
		{
			name: "two non-delete rows sharing one anchor key — collision",
			results: []simple.EvalResult{
				writeResult("roster.identity.A"),
				writeResult("roster.identity.A"),
			},
			wantFound: true,
			wantKey:   "roster.identity.A",
			wantCount: 2,
		},
		{
			name: "three non-delete rows sharing one anchor key — count is the full total",
			results: []simple.EvalResult{
				writeResult("roster.identity.A"),
				writeResult("roster.identity.A"),
				writeResult("roster.identity.A"),
			},
			wantFound: true,
			wantKey:   "roster.identity.A",
			wantCount: 3,
		},
		{
			name: "delete + write for the same key — NOT a collision (retract then write)",
			results: []simple.EvalResult{
				deleteResultFor("roster.identity.A"),
				writeResult("roster.identity.A"),
			},
			wantFound: false,
		},
		{
			name: "rows for different actors — NOT a collision",
			results: []simple.EvalResult{
				writeResult("roster.identity.A"),
				writeResult("roster.identity.B"),
			},
			wantFound: false,
		},
		{
			name: "empty output keys are ignored, not treated as colliding",
			results: []simple.EvalResult{
				{Keys: map[string]any{}, Row: map[string]any{"x": 1}},
				{Keys: map[string]any{}, Row: map[string]any{"x": 2}},
			},
			wantFound: false,
		},
		{
			name: "two deletes for the same key — NOT a collision (idempotent retract)",
			results: []simple.EvalResult{
				deleteResultFor("roster.identity.A"),
				deleteResultFor("roster.identity.A"),
			},
			wantFound: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, count, found := detectOutputKeyCollision(tc.results)
			require.Equal(t, tc.wantFound, found)
			if tc.wantFound {
				require.Equal(t, tc.wantKey, key)
				require.Equal(t, tc.wantCount, count)
			}
		})
	}
}

// TestGuardOutputKeyCollision_RecordsHealthAndFailsClosed asserts the guard
// surfaces a real collision on the Health-KV surface (errorCount + lastError)
// AND returns a Terminal-classified error so the actor's projection fails closed
// instead of silently overwriting.
func TestGuardOutputKeyCollision_RecordsHealthAndFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	_, _, healthKV := newCollisionKVs(t)
	reporter := health.New(healthKV, "rule-collision")
	p := &Pipeline{ruleID: "rule-collision", reporter: reporter}

	const actorKey = "vtx.identity.Tcc1JdentityAaaaaaaa"
	const collKey = "roster.identity.Tcc1JdentityAaaaaaaa"
	err := p.guardOutputKeyCollision(context.Background(), actorKey,
		[]simple.EvalResult{writeResult(collKey), writeResult(collKey)})

	require.Error(t, err)
	require.Equal(t, failure.CatTerminal, failure.Classify(err),
		"a per-anchor authoring defect is permanent — it must classify as Terminal, not Transient")

	entry, gerr := reporter.GetStatus(context.Background())
	require.NoError(t, gerr)
	require.Equal(t, uint64(1), entry.ErrorCount, "the collision must increment the Health-KV error count")
	require.NotNil(t, entry.LastError)
	require.Contains(t, *entry.LastError, collKey, "the recorded issue must name the colliding output key")
	require.Contains(t, *entry.LastError, actorKey, "the recorded issue must name the actor")
}

// TestGuardOutputKeyCollision_NilReporter_StillFailsClosed asserts the guard
// still fails closed when no reporter is wired (the WARN log remains the
// observable signal); a nil reporter must not panic.
func TestGuardOutputKeyCollision_NilReporter_StillFailsClosed(t *testing.T) {
	p := &Pipeline{ruleID: "rule-collision-noreporter"}
	const collKey = "roster.identity.Tcc1JdentityAaaaaaaa"
	err := p.guardOutputKeyCollision(context.Background(), "vtx.identity.Tcc1JdentityAaaaaaaa",
		[]simple.EvalResult{writeResult(collKey), writeResult(collKey)})
	require.Error(t, err)
	require.Equal(t, failure.CatTerminal, failure.Classify(err))
}

// TestGuardOutputKeyCollision_OneRow_NoError asserts the one-row-per-anchor path
// is untouched: no error, no health write.
func TestGuardOutputKeyCollision_OneRow_NoError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	_, _, healthKV := newCollisionKVs(t)
	reporter := health.New(healthKV, "rule-ok")
	p := &Pipeline{ruleID: "rule-ok", reporter: reporter}

	err := p.guardOutputKeyCollision(context.Background(), "vtx.identity.Tcc1JdentityAaaaaaaa",
		[]simple.EvalResult{writeResult("roster.identity.Tcc1JdentityAaaaaaaa")})
	require.NoError(t, err)

	entry, gerr := reporter.GetStatus(context.Background())
	require.NoError(t, gerr)
	require.Equal(t, uint64(0), entry.ErrorCount, "the clean one-row path must not write a health issue")
}

// nonAggregatingSpec is a deliberately-mis-authored actor-aggregate cypher: it
// RETURNs one row per assigned task instead of aggregating (collect) to one row
// per anchor. With 2 tasks assigned to one identity it yields 2 non-delete rows
// — each wrapped to the same anchor-derived output key by anchorEnvelopeFn.
const nonAggregatingSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.status = 'open'
RETURN
  identity.key AS actorKey,
  task.key AS taskRef
`

// anchorEnvelopeFn reproduces the load-bearing property of the real
// actor-aggregate envelope (projection.OutputDescriptor.EnvelopeFn) without
// importing the projection package (which would form an import cycle with
// pipeline): the output key is derived from the ANCHOR, so every non-delete row
// for one actor carries the SAME key. The roster.<id> shape mirrors the proof
// lens's outputKeyPattern.
func anchorEnvelopeFn(row, _, params map[string]any) (map[string]any, map[string]any, error) {
	actorKey, _ := row["actorKey"].(string)
	if actorKey == "" {
		actorKey, _ = params["actorKey"].(string)
	}
	const vtxPrefix = "vtx."
	suffix := actorKey
	if rest, ok := strings.CutPrefix(actorKey, vtxPrefix); ok {
		suffix = rest
	}
	outKey := "roster." + suffix
	envelope := map[string]any{"key": outKey, "actor": actorKey, "taskRef": row["taskRef"]}
	return envelope, map[string]any{"key": outKey}, nil
}

// TestExecuteFullForActor_MultiRowPerAnchor_GuardFires drives the LIVE full
// engine against a real graph where one actor yields 2 non-delete rows that the
// anchor-derived envelope collapses to one key, and asserts the guard fires
// (Terminal + Health issue) rather than silently dropping the first row. Uses a
// non-service / non-leaseapp vertex type (identity + task) to keep the proof
// engine-type-agnostic.
func TestExecuteFullForActor_MultiRowPerAnchor_GuardFires(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, healthKV := newCollisionKVs(t)
	ctx := context.Background()

	const (
		identityID = "Tcc1JdentityAaaaaaaa"
		task1ID    = "Tcc1Task1aaaaaaaaaaa"
		task2ID    = "Tcc1Task2bbbbbbbbbbb"
	)
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{"name": "amelia"})
	writeCollisionVertex(t, coreKV, "vtx.task."+task1ID, "task", map[string]any{"status": "open"})
	writeCollisionVertex(t, coreKV, "vtx.task."+task2ID, "task", map[string]any{"status": "open"})
	buildCollisionEdge(t, adjKV, "assignedTo", "task", task1ID, "identity", identityID)
	buildCollisionEdge(t, adjKV, "assignedTo", "task", task2ID, "identity", identityID)

	reporter := health.New(healthKV, "rule-roster")
	eng := full.New()
	cr, err := eng.Parse(nonAggregatingSpec)
	require.NoError(t, err)

	p := &Pipeline{
		ruleID:     "rule-roster",
		coreKV:     coreKV,
		adjKV:      adjKV,
		engineKind: ruleengine.EngineFull,
		fullEngine: eng,
		fullCR:     cr,
		envelopeFn: anchorEnvelopeFn,
		reporter:   reporter,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	_, err = p.executeFullForActor(ctx, identityKey, nodeProps)
	require.Error(t, err, "two open tasks must collide on the anchor-derived key — the guard must fire")
	require.Equal(t, failure.CatTerminal, failure.Classify(err))
	require.Contains(t, err.Error(), "roster.identity."+identityID)

	entry, gerr := reporter.GetStatus(ctx)
	require.NoError(t, gerr)
	require.Equal(t, uint64(1), entry.ErrorCount)
	require.NotNil(t, entry.LastError)
	require.Contains(t, *entry.LastError, "roster.identity."+identityID)
}

// TestExecuteFullForActor_OneRowPerAnchor_NoGuard is the regression control:
// the SAME lens with a single matched task projects exactly one non-delete
// result and the guard never fires — the normal one-row-per-anchor path is
// behavior-preserving.
func TestExecuteFullForActor_OneRowPerAnchor_NoGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, healthKV := newCollisionKVs(t)
	ctx := context.Background()

	const (
		identityID = "Tcc2JdentityCccccccc"
		task1ID    = "Tcc2Task1aaaaaaaaaaa"
	)
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{"name": "solo"})
	writeCollisionVertex(t, coreKV, "vtx.task."+task1ID, "task", map[string]any{"status": "open"})
	buildCollisionEdge(t, adjKV, "assignedTo", "task", task1ID, "identity", identityID)

	reporter := health.New(healthKV, "rule-roster-ok")
	eng := full.New()
	cr, err := eng.Parse(nonAggregatingSpec)
	require.NoError(t, err)

	p := &Pipeline{
		ruleID:     "rule-roster-ok",
		coreKV:     coreKV,
		adjKV:      adjKV,
		engineKind: ruleengine.EngineFull,
		fullEngine: eng,
		fullCR:     cr,
		envelopeFn: anchorEnvelopeFn,
		reporter:   reporter,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err, "a single matched task is one row per anchor — the guard must not fire")
	require.Len(t, results, 1)
	require.Equal(t, "roster.identity."+identityID, results[0].Keys["key"])

	entry, gerr := reporter.GetStatus(ctx)
	require.NoError(t, gerr)
	require.Equal(t, uint64(0), entry.ErrorCount)
}

// newCollisionKVs stands up an in-memory NATS server with empty Core, Adj, and
// Health KV buckets for the output-key-collision tests.
func newCollisionKVs(t *testing.T) (coreKV, adjKV, healthKV *substrate.KV) {
	t.Helper()
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  jsstore.Dir(t),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second))

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() {
		nc.Close()
		s.Shutdown()
	})

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "CORE"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "ADJ"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "HEALTH"})
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "CORE")
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "ADJ")
	require.NoError(t, err)
	healthKV, err = conn.OpenKV(ctx, "HEALTH")
	require.NoError(t, err)
	return coreKV, adjKV, healthKV
}

// writeCollisionVertex stores a Contract #1 vertex body in Core KV with the
// provenance timestamp executeFullForActor needs to derive projectedAt.
func writeCollisionVertex(t *testing.T, coreKV *substrate.KV, key, class string, data map[string]any) {
	t.Helper()
	body := map[string]any{
		"key": key, "class": class, "isDeleted": false,
		"createdAt": "2026-05-15T10:00:00Z", "lastModifiedAt": "2026-05-15T10:00:00Z",
		"data": data,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// buildCollisionEdge writes both adjacency directions for a Contract #1 link so
// the full engine's traversal resolves it.
func buildCollisionEdge(t *testing.T, adjKV *substrate.KV, name, fromType, fromID, toType, toID string) {
	t.Helper()
	ctx := context.Background()
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + ":" + fromID + ":" + toID
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name,
		Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType,
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name,
		Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType,
	}))
}
