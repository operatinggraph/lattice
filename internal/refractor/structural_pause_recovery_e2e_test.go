// Package refractor_test — the end-to-end proof for
// structural-pause-recovery-design.md §4.2: a structurally paused PROTECTED
// Refractor lens re-runs its own Probe and resumes with no operator, bounded by
// the relapse latch that hands an unfixable cause back to a human.
//
// Every other §4.2 test drives a stub probe and a stub classifier. This is the
// only place the real pieces are composed:
//
//	embedded NATS (natsfixture)  →  Core KV CDC  →  the real Pipeline
//	  →  the real adapter.ProtectedAdapter over real Postgres
//	  →  the real adapter.VerifyProtectedTable as ConsumerSpec.Probe
//	  →  the real failure.Classify / classifyForSupervisor over a real pgconn.PgError
//	  →  the real substrate.ConsumerSupervisor pause machine
//
// Isolation: every Postgres object these tests create lives in a per-test
// scratch SCHEMA that the pool's search_path points at exclusively, dropped
// CASCADE in t.Cleanup. Nothing here can see — let alone ALTER — the shared
// actor_read_grants or any pre-existing table, which matters because the dev
// DSN is the live database a running Refractor writes to and CI runs packages
// concurrently against one Postgres.
package refractor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	// structuralPauseProbeInterval is the spec's ProbeInterval, shrunk from the
	// 10 s production default so a recovery that the design bounds at "~2 probe
	// intervals" is bounded in wall-clock seconds here.
	structuralPauseProbeInterval = 500 * time.Millisecond

	// structuralPauseAckWait mirrors cmd/refractor's lensAckWait. It is set
	// deliberately long: a structural failure leaves its message un-acked, so
	// WITHOUT §4.2(b)'s Nak-with-delay the redelivery that re-tests the fix
	// cannot arrive for five minutes. Every recovery deadline below is a small
	// multiple of the probe interval, which is what makes the Nak — not the ack
	// timeout — the only thing that can be producing the observed cadence.
	structuralPauseAckWait = 5 * time.Minute

	// structuralPauseTable is the protected read-model table. It is unqualified
	// on purpose: it resolves through the scratch schema on the pool's
	// search_path, exactly as VerifyProtectedTable's own to_regclass lookups do.
	structuralPauseTable = "widget_read"

	structuralPauseCoreBucket   = "core-kv"
	structuralPauseAdjBucket    = "refractor-adjacency"
	structuralPauseHealthBucket = "health-kv"
)

// structuralPauseLatchPrefix is the operator-facing marker the pump writes onto
// a structural pause it has stopped trying to heal (substrate's latchedCause).
const structuralPauseLatchPrefix = "structural pause latched after 3 self-heal attempts:"

// skipIfNoPostgresE2E mirrors internal/refractor/adapter's skipIfNoPostgres,
// which is unexported in a different package. POSTGRES_TEST_DSN is exported by
// every CI unit shard, so these are real gates, not local-only tests.
func skipIfNoPostgresE2E(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping Postgres-backed e2e test in short mode")
	}
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("skipping: POSTGRES_TEST_DSN not set")
	}
	return dsn
}

// structuralPauseScratchPool opens a pool whose connections carry a private,
// freshly created schema as their sole search_path. Every table these tests
// create, ALTER and drop is therefore this test's own — including the
// actor_read_grants the protected SELECT policy's membership subquery must
// resolve at CREATE POLICY time, which would otherwise be the shared,
// live-Refractor-written one. pg_catalog stays implicitly searchable, so
// to_regclass / pg_class / pg_attribute / pg_policy / pg_index all still
// resolve. The schema is dropped CASCADE first (cleanups run LIFO, before the
// pool closes).
func structuralPauseScratchPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("t_structpause_%d", time.Now().UnixNano())

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cerr := pool.Exec(context.Background(), fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema))
		require.NoError(t, cerr, "drop this test's own scratch schema")
	})

	// The grant table the generated §6.14 policy references. Shape mirrors what
	// VerifyGrantTable requires, so the scratch copy is the real thing rather
	// than a stand-in the policy merely parses against.
	_, err = pool.Exec(ctx, `CREATE TABLE "actor_read_grants" (
		actor_id text NOT NULL,
		anchor_id text NOT NULL,
		grant_source text NOT NULL,
		projection_seq bigint NOT NULL DEFAULT 0,
		is_deleted boolean NOT NULL DEFAULT false,
		PRIMARY KEY (actor_id, anchor_id, grant_source)
	)`)
	require.NoError(t, err)
	return pool
}

// structuralPauseLens is one running protected lens: its Postgres table, the
// Core KV it consumes, and the health reporter that carries the pause verdict.
type structuralPauseLens struct {
	pool     *pgxpool.Pool
	coreKV   *substrate.KV
	reporter *health.Reporter
	pipe     *pipeline.Pipeline
}

// startStructuralPauseLens provisions a protected table, wires the real
// pipeline over it, and runs it until the test ends.
//
// declaredBody is what the lens DECLARES — the column set ProtectedAdapter.Probe
// (VerifyProtectedTable) verifies. undeclaredCols are columns the table carries
// and the RETURN emits but the lens never declared: G6b's residual probe
// incompleteness, which is the only shape that can produce a probe pass over a
// still-failing write, and therefore the only shape that can drive the relapse
// latch.
func startStructuralPauseLens(
	t *testing.T,
	ctx context.Context,
	cypher string,
	declaredBody []adapter.ColumnDef,
	undeclaredCols []adapter.ColumnDef,
) *structuralPauseLens {
	t.Helper()

	dsn := skipIfNoPostgresE2E(t)
	pool := structuralPauseScratchPool(t, dsn)

	stmts, err := adapter.BuildProtectedTableDDL(structuralPauseTable, []string{"id"}, declaredBody)
	require.NoError(t, err)
	for _, s := range stmts {
		_, err = pool.Exec(ctx, s)
		require.NoError(t, err, "provision protected table: %s", s)
	}
	for _, c := range undeclaredCols {
		_, err = pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE %q ADD COLUMN %q %s`,
			structuralPauseTable, c.Name, c.Type))
		require.NoError(t, err)
	}

	srv := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, srv.ClientURL())
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	js := conn.JetStream()
	for _, b := range []string{structuralPauseCoreBucket, structuralPauseAdjBucket, structuralPauseHealthBucket} {
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: b})
		require.NoError(t, err)
	}
	coreKV, err := conn.OpenKV(ctx, structuralPauseCoreBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, structuralPauseAdjBucket)
	require.NoError(t, err)
	healthKV, err := conn.OpenKV(ctx, structuralPauseHealthBucket)
	require.NoError(t, err)

	boots := consumer.NewBootstrapper(conn, structuralPauseCoreBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	eng := full.New()
	cr, err := eng.Parse(cypher)
	require.NoError(t, err)
	fullCR, ok := cr.(*full.CompiledRule)
	require.True(t, ok)
	fullCR.KeyColumns = []string{"id"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	base, err := adapter.NewPostgresAdapter(pool, structuralPauseTable, []string{"id"}, 10*time.Second, adapter.DeleteModeHard)
	require.NoError(t, err)
	// The real read-path adapter: its Probe IS adapter.VerifyProtectedTable over
	// the declared body, which is the whole point of the opt-in in §4.2(d).
	adpt, err := adapter.NewProtectedAdapter(base, nil, declaredBody)
	require.NoError(t, err)

	lensID := stableNanoID("structural-pause-" + t.Name())
	reporter := health.New(healthKV, lensID)
	p, err := pipeline.New(lensID, "postgres", structuralPauseCoreBucket, adjKV, coreKV, adpt, reporter)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)

	// cmd/refractor's own spec, plus the one field this increment adds. The
	// pipeline fills Handler / Classify / Probe / Health in Run, so the
	// classifier under test is the real classifyForSupervisor and the probe is
	// the real adapter Probe — neither is substitutable from here.
	spec := e2eSpec(lensID, structuralPauseCoreBucket)
	spec.ProbeInterval = structuralPauseProbeInterval
	spec.AckWait = structuralPauseAckWait
	spec.StructuralProbe = true
	p.RunOn(conn, spec)

	pipeCtx, pipeCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); p.Run(pipeCtx) }()
	t.Cleanup(func() { pipeCancel(); <-done })

	return &structuralPauseLens{pool: pool, coreKV: coreKV, reporter: reporter, pipe: p}
}

// putWidget writes one widget vertex to Core KV, which the CDC stream carries
// into the lens. Every widget carries both projectable fields; the lens's
// RETURN decides which of them reach the table.
func (h *structuralPauseLens) putWidget(t *testing.T, ctx context.Context, id, status, tier string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	body, err := json.Marshal(map[string]any{
		"id":             id,
		"status":         status,
		"tier":           tier,
		"isDeleted":      false,
		"createdAt":      now,
		"lastModifiedAt": now,
	})
	require.NoError(t, err)
	_, err = h.coreKV.Put(ctx, "vtx.widget."+stableNanoID("structural-pause-widget-"+id), body)
	require.NoError(t, err)
}

// rowStatus reports whether the projected row exists and what it carries. The
// pool's role owns the table and is not RLS-constrained, so this reads the row
// as the projector wrote it; the policy's read-side behaviour has its own tests
// in internal/refractor/adapter.
func (h *structuralPauseLens) rowStatus(ctx context.Context, id string) (string, bool) {
	var status *string
	err := h.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT status FROM %q WHERE id = $1`, structuralPauseTable), id).Scan(&status)
	if err != nil {
		return "", false
	}
	if status == nil {
		return "", true
	}
	return *status, true
}

func (h *structuralPauseLens) entry(t *testing.T, ctx context.Context) health.Entry {
	t.Helper()
	e, err := h.reporter.GetStatus(ctx)
	require.NoError(t, err)
	return e
}

// exec runs one out-of-band schema change — the operator act (or the drift)
// this design is about.
func (h *structuralPauseLens) exec(t *testing.T, ctx context.Context, sql string) {
	t.Helper()
	_, err := h.pool.Exec(ctx, sql)
	require.NoError(t, err)
}

// pollUntil waits for an observable EFFECT and returns how long it took.
// Condition polling, never a fixed sleep as a synchronisation device: a settled
// consumer has not necessarily finished its handler (NumPending drops on
// prefetch, not on the write landing), so every barrier in this file is the row
// itself or the health entry itself.
func pollUntil(t *testing.T, timeout, tick time.Duration, msg string, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		if cond() {
			return time.Since(start)
		}
		if time.Since(start) > timeout {
			t.Fatalf("%s (waited %s)", msg, time.Since(start))
		}
		time.Sleep(tick)
	}
}

// pollWhile asserts a condition holds for a whole window — the shape a "did NOT
// self-heal" claim needs, since absence has no edge to wait on.
func pollWhile(t *testing.T, window, tick time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatal(msg)
		}
		time.Sleep(tick)
	}
}

func structuralPausedEntry(t *testing.T, h *structuralPauseLens, ctx context.Context) health.Entry {
	t.Helper()
	var e health.Entry
	pollUntil(t, 30*time.Second, 20*time.Millisecond,
		"lens never reached a structural pause", func() bool {
			e = h.entry(t, ctx)
			return e.Status == health.StatusPaused &&
				e.PauseReason != nil && *e.PauseReason == health.PauseReasonStructural
		})
	return e
}

// TestStructuralPauseRecovery_ProtectedLensResumesWithoutOperator is §4.2's
// stated green bar, end to end over the real pieces.
//
// It pins, in order:
//   - the positive vector — the lens actually projects a row. Without this the
//     rest could pass with the whole mechanism disabled.
//   - a DROPped DECLARED body column makes the next event's write fail 42703,
//     which the real failure.Classify calls structural and the real supervisor
//     turns into a structural pause carrying a NAMED cause (Inc 1's guarantee:
//     the column and the table, not a bare "structural").
//   - restoring the column resumes the lens with NO operator action, and the
//     pending row lands within a small multiple of the probe interval — orders
//     of magnitude inside the 5-minute AckWait the spec sets, which is what
//     makes the deadline an assertion about §4.2(b)'s Nak rather than about
//     JetStream's redelivery timer.
func TestStructuralPauseRecovery_ProtectedLensResumesWithoutOperator(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	h := startStructuralPauseLens(t, ctx,
		"MATCH (w:widget {key: $actorKey}) RETURN w.id AS id, w.status AS status",
		[]adapter.ColumnDef{{Name: "status", Type: "text"}}, nil)

	// 1. Positive vector: the lens projects.
	h.putWidget(t, ctx, "alpha", "open", "gold")
	pollUntil(t, 30*time.Second, 20*time.Millisecond,
		"the protected lens never projected its first row — nothing below would mean anything",
		func() bool {
			s, ok := h.rowStatus(ctx, "alpha")
			return ok && s == "open"
		})
	require.Equal(t, health.StatusActive, h.entry(t, ctx).Status,
		"a lens that has projected must read active")

	// 2. The structural fault: a declared body column disappears out from under
	// the write path — the package-upgrade-without-provision-readpath drift the
	// design was filed against.
	h.exec(t, ctx, fmt.Sprintf(`ALTER TABLE %q DROP COLUMN status`, structuralPauseTable))

	// 3. The next event's write fails 42703 → structural pause with a cause.
	h.putWidget(t, ctx, "bravo", "queued", "silver")
	paused := structuralPausedEntry(t, h, ctx)
	require.NotNil(t, paused.LastError,
		"a structural pause with a null lastError is the exact defect Inc 1 closed")
	assert.Contains(t, *paused.LastError, "42703",
		"the cause must carry the SQLSTATE the tier was decided on")
	assert.Contains(t, *paused.LastError, "status",
		"the cause must name the column the write path lost")
	assert.Contains(t, *paused.LastError, structuralPauseTable,
		"the cause must name the table")
	assert.NotContains(t, *paused.LastError, structuralPauseLatchPrefix,
		"a first structural pause has not exhausted any self-heal attempt")
	_, present := h.rowStatus(ctx, "bravo")
	require.False(t, present, "the paused event must not have projected")

	// 4. The operator's out-of-band fix — and NOTHING else. No Resume, no
	// restart, no CLI, no console.
	h.exec(t, ctx, fmt.Sprintf(`ALTER TABLE %q ADD COLUMN status text`, structuralPauseTable))
	fixedAt := time.Now()

	// 5. The lens re-probes, adjudicates its own condition, resumes, and the
	// message that failed is redelivered and lands.
	recovered := pollUntil(t, 30*time.Second, 20*time.Millisecond,
		"the structurally paused lens never self-healed after its own probe's condition was restored",
		func() bool {
			s, ok := h.rowStatus(ctx, "bravo")
			return ok && s == "queued"
		})
	t.Logf("structural pause self-healed and the pending row landed in %s (probe interval %s, AckWait %s)",
		recovered, structuralPauseProbeInterval, structuralPauseAckWait)
	// 15x the probe interval, not a tight multiple: everything between the ALTER and a readable
	// row — the in-flight probe, a fresh VerifyProtectedTable's catalog round trips, the consumer
	// reopen, redelivery, a full rule-engine pass and the write — competes with three other
	// packages under CI's `go test -p 4` alongside a Postgres service. The bound only has to
	// separate probe-paced recovery from AckWait-paced recovery, and AckWait here is 5 minutes,
	// so it can be generous and still be the same assertion. The tight, contention-free version
	// of this proof lives in internal/substrate's Off test, which measures the redelivery gap
	// against a 2s AckWait deterministically.
	assert.Less(t, recovered, 15*structuralPauseProbeInterval,
		"§4.2(b): the failing message is Nak'd one probe interval out, so recovery is probe-paced — "+
			"a recovery paced by AckWait (%s) instead would mean the Nak is not happening",
		structuralPauseAckWait)
	require.Less(t, time.Since(fixedAt), structuralPauseAckWait,
		"sanity: the recovery deadline must be far inside AckWait for the assertion above to be about the Nak")

	pollUntil(t, 10*time.Second, 20*time.Millisecond,
		"the recovered lens never returned to active", func() bool {
			return h.entry(t, ctx).Status == health.StatusActive
		})
}

// TestStructuralPauseRecovery_RelapseLatchHandsBackToOperator is §4.2(c): the
// backstop for the probe's residual incompleteness (G6b).
//
// The fault is deliberately one VerifyProtectedTable cannot see — a column the
// evaluator emits that the lens never DECLARED. That is the only shape that can
// make the probe pass while the write still fails, and therefore the only shape
// that can drive a relapse at all. (A dropped DECLARED column, as in the test
// above, makes the probe FAIL; the pump then simply keeps probing and never
// spends a self-heal attempt — correct, but it can never latch.)
//
// It pins:
//   - the positive vector, twice over: the row projects, WITH the undeclared
//     column, so the drop below is known to break the write and not merely to
//     be ignored.
//   - after three probe-driven resumes that did not hold, the pause is latched:
//     still paused/structural, and its persisted cause carries the latch prefix
//     so the operator reads both the diagnosis and the fact that the platform
//     tried.
//   - the latch is real and not a stuck fixture: with the condition genuinely
//     restored the lens still does NOT self-heal, and an operator Resume — the
//     hand-back the latch exists to perform — clears it and the pending row
//     lands.
func TestStructuralPauseRecovery_RelapseLatchHandsBackToOperator(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	h := startStructuralPauseLens(t, ctx,
		"MATCH (w:widget {key: $actorKey}) RETURN w.id AS id, w.status AS status, w.tier AS tier",
		[]adapter.ColumnDef{{Name: "status", Type: "text"}},
		[]adapter.ColumnDef{{Name: "tier", Type: "text"}})

	// 1. Positive vector: the lens projects, and the UNDECLARED column really is
	// part of the write. Without this the drop below would be a no-op fault.
	h.putWidget(t, ctx, "alpha", "open", "gold")
	pollUntil(t, 30*time.Second, 20*time.Millisecond,
		"the protected lens never projected its first row", func() bool {
			s, ok := h.rowStatus(ctx, "alpha")
			return ok && s == "open"
		})
	var tier string
	require.NoError(t, h.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT tier FROM %q WHERE id = $1`, structuralPauseTable), "alpha").Scan(&tier))
	require.Equal(t, "gold", tier,
		"the undeclared column must actually be written, or dropping it proves nothing")

	// 2. The fault the probe cannot adjudicate.
	h.exec(t, ctx, fmt.Sprintf(`ALTER TABLE %q DROP COLUMN tier`, structuralPauseTable))
	h.putWidget(t, ctx, "bravo", "queued", "silver")

	// 3. Probe passes → resume → the write fails again. Three times, then latch.
	var latched health.Entry
	pollUntil(t, 60*time.Second, 20*time.Millisecond,
		"the lens never latched — a probe that passes over a still-failing write must not self-heal forever",
		func() bool {
			e := h.entry(t, ctx)
			if e.Status != health.StatusPaused || e.LastError == nil {
				return false
			}
			if !strings.HasPrefix(*e.LastError, structuralPauseLatchPrefix) {
				return false
			}
			latched = e
			return true
		})
	require.NotNil(t, latched.PauseReason)
	assert.Equal(t, health.PauseReasonStructural, *latched.PauseReason,
		"a latched pause is still a structural pause — the latch changes who clears it, not what it is")
	assert.Contains(t, *latched.LastError, "42703",
		"the latch prefix must carry the cause, not replace it")
	assert.Contains(t, *latched.LastError, "tier",
		"the latched cause must still name the column")

	// 4. The latch is what is holding it, not a broken fixture: restore the
	// column and prove the lens does NOT come back on its own.
	h.exec(t, ctx, fmt.Sprintf(`ALTER TABLE %q ADD COLUMN tier text`, structuralPauseTable))
	pollWhile(t, 8*structuralPauseProbeInterval, 25*time.Millisecond,
		"a latched lens self-healed — the latch is not holding, so nothing bounds a probe that keeps being wrong",
		func() bool {
			_, present := h.rowStatus(ctx, "bravo")
			if present {
				return false
			}
			return h.entry(t, ctx).Status == health.StatusPaused
		})

	// 5. The hand-back completes: an operator Resume clears the latch, and the
	// pending row lands — which is also what proves step 4's "stayed paused" was
	// the latch and not a lens that could no longer project at all.
	h.pipe.Resume(ctx)
	pollUntil(t, 30*time.Second, 20*time.Millisecond,
		"an operator Resume did not clear the latch — a latched lens would then be unrecoverable short of a restart",
		func() bool {
			s, ok := h.rowStatus(ctx, "bravo")
			return ok && s == "queued"
		})
	pollUntil(t, 10*time.Second, 20*time.Millisecond,
		"the resumed lens never returned to active", func() bool {
			return h.entry(t, ctx).Status == health.StatusActive
		})
}
