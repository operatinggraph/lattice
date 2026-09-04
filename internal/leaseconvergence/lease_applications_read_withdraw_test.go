//go:build leaseshortwindow

package leaseconvergence_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// leaseApplicationsReadTestBucket is the harness's own target for
// leaseApplicationsRead — a NATS KV bucket standing in for the postgres table
// (read_lease_applications) the lens is declared against in production. See
// runLeaseApplicationsReadToBucket's doc for what this substitution proves and
// does not prove.
const leaseApplicationsReadTestBucket = "read-lease-applications-e2e"

// leaseApplicationsReadFakeDSN only needs to be non-empty: translateSpec
// (internal/refractor/lens/corekv_source.go) resolves an unset
// targetConfig.dsn from REFRACTOR_PG_DSN at activation and fails the whole
// lens load when neither is set. This harness has no Postgres and never opens
// this DSN — the lens's real postgres storage is what
// runLeaseApplicationsReadToBucket substitutes a NATS KV bucket for.
const leaseApplicationsReadFakeDSN = "postgres://leaseconvergence-test-placeholder/db"

// TestLeaseConvergence_WithdrawRetractsReadModelRow proves that withdrawing a
// lease application retracts its row from leaseApplicationsRead
// (packages/lease-signing/lenses.go), the read model
// cmd/loftspace-app's applicant-facing views and lease-document download read.
//
// leaseApplicationsRead anchors on `MATCH (app:leaseapp)` and carries a WITH
// (`WITH app.key AS entityKey … RETURN nanoIdFromKey(entityKey) AS app_id`).
// WithdrawLeaseApplication (packages/lease-signing/ddls.go) soft-deletes the
// leaseapp root; the engine's root-tombstone shortcut
// (pipeline/evaluate.go's `entry.IsDeleted && p.actorEnumerator == nil` arm,
// calling AnchorDeleteResult) resolves the WITH alias back to `app.key` and
// derives the row's key, so the tombstone retracts the projected row rather
// than leaving it behind.
//
// leaseApplicationsRead is declared against postgres
// (targetConfig.table="read_lease_applications"), and this harness has no
// Postgres. runLeaseApplicationsReadToBucket runs the lens's REAL compiled
// cypher and REAL Into.Key through a flat KV pipeline into a NATS KV bucket
// this test provisions, with adapter.DeleteModeHard — the same
// pipeline.New/adapter.New/UseFullEngine wiring harness_test.go's
// runFlatLensPipeline uses for a nats_kv-declared plain lens. The
// root-tombstone → AnchorDeleteResult → Delete mechanism this proves is
// adapter-agnostic (it decides whether to emit a Delete before any adapter is
// consulted), so this substitution proves the RETRACTION MECHANISM fires with
// the real cypher and the real op. It does NOT prove anything about the
// postgres/RLS storage path itself (row-level security, the provisioned
// authz_anchors column, or the postgres adapter's own Delete implementation).
func TestLeaseConvergence_WithdrawRetractsReadModelRow(t *testing.T) {
	// Deliberately serial (no t.Parallel): REFRACTOR_PG_DSN is read by every
	// harness's CoreKVSource (translateSpec resolves an unset targetConfig.dsn
	// from it for each postgres-declared lens), so setting it is a
	// process-wide change the sibling stacks would observe mid-run. t.Setenv
	// refuses to combine with t.Parallel for exactly that reason; the ~10 s
	// this adds serially is the price of not mutating the environment under
	// fifteen concurrent harnesses.
	//
	// It must be set before newHarness boots: a failure in translateSpec
	// drops the lens silently (dispatchSpec logs and never calls the load
	// callback), and the wait loop below would then time out with no lens
	// ever found.
	t.Setenv("REFRACTOR_PG_DSN", leaseApplicationsReadFakeDSN)

	h := newHarness(t)

	rule := discoverLeaseApplicationsReadRule(h)
	targetKV := runLeaseApplicationsReadToBucket(h, rule)

	appKey, appID, applicantKey := h.seedApplicant()
	unitKey := h.lastUnitKey

	// (1) The row lands, keyed by the bare app_id (adapter.buildKey's
	// single-key-field shape: the key is the field value verbatim, no
	// prefix), and carries the leaseapp's own key as entity_key.
	row := awaitLeaseApplicationsRow(t, h.ctx, targetKV, appID, 60*time.Second)
	require.NotNilf(t, row, "leaseApplicationsRead row for %s never appeared", appID)
	require.Equal(t, appKey, row["entity_key"], "row's entity_key must be the leaseapp's own key")

	// (2) WithdrawLeaseApplication, exactly as
	// packages/lease-signing/lease_signing_test.go's withdraw() helper drives
	// it: class leaseapp, the two (a) required validation reads, the (d)
	// optional guard-link read, actor = bootstrap (the harness's stub-auth
	// posture — see harness_test.go's AuthModeStub comment).
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	_, applicantID, _ := substrate.ParseVertexKey(applicantKey)
	withdrawReply := h.submitOp("WithdrawLeaseApplication", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey, "unit": unitKey, "applicant": applicantKey,
	}, &processor.ContextHint{
		Reads: []string{
			appKey,
			"lnk.leaseapp." + appID + ".appliesToUnit.unit." + unitID,
			"lnk.leaseapp." + appID + ".applicationFor.identity." + applicantID,
		},
		OptionalReads: []string{
			"lnk.identity." + applicantID + ".appliedToUnit.unit." + unitID,
		},
	})
	require.Equalf(t, processor.ReplyStatusAccepted, withdrawReply.Status, "WithdrawLeaseApplication: %+v", withdrawReply.Error)

	// (3) The row leaves the read model (DeleteModeHard: the key is gone —
	// readLeaseApplicationsRow returns nil for an absent OR an
	// empty/soft-deleted entry).
	require.Eventuallyf(t, func() bool {
		return readLeaseApplicationsRow(h.ctx, targetKV, appID) == nil
	}, 60*time.Second, 150*time.Millisecond,
		"leaseApplicationsRead row for %s was never retracted after withdraw", appID)

	// (4) The root itself carries the soft-delete D5 records (empty root data,
	// the isDeleted tombstone is on the envelope the withdraw op wrote, not a
	// `data` field) — vertexRootData reads the envelope's `data` object, which
	// the design (ddls.go: "root stays {}") keeps empty; the tombstone is the
	// CDC event's own IsDeleted flag evaluate.go reads, not a stored field.
	// This assertion is the D5 sanity check the design brief's §14.1 premise 1
	// calls for, not a claim about where isDeleted itself is stored.
	data, ok := h.vertexRootData(appKey)
	require.True(t, ok, "leaseapp root must still be readable (a soft delete keeps the key, D5)")
	require.Empty(t, data, "leaseapp root data must stay empty after withdraw (D5)")
}

// discoverLeaseApplicationsReadRule activates a SECOND, independent
// CoreKVSource (mirroring activateActorAggregateLensNow's own independent
// source — each CoreKVSource boot gets its own per-boot JetStream durable, so
// this does not disturb the harness's boot-time source or any pipeline it
// already wired) and waits for leaseApplicationsRead to surface, returning its
// compiled *lens.Rule.
func discoverLeaseApplicationsReadRule(h *harness) *lens.Rule {
	h.t.Helper()
	const name = "leaseApplicationsRead"
	src := lens.NewCoreKVSource(h.conn, bootstrap.CoreKVBucket, "test-lar-e2e", h.logger)
	loaded := make(chan *lens.Rule, 4)
	src.SetLoadCallback(func(r *lens.Rule) {
		if r.CanonicalName == name {
			loaded <- r
		}
	})
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(h.t, src.Start(h.ctx))

	deadline := time.Now().Add(90 * time.Second)
	for {
		require.Truef(h.t, time.Now().Before(deadline), "lens %s was not discovered within 90s", name)
		select {
		case r := <-loaded:
			return r
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// runLeaseApplicationsReadToBucket is runFlatLensPipeline's sibling for a
// postgres-declared plain lens under test. It runs the SAME real compiled
// cypher (rule.CompiledRule), the SAME real Into.Key
// (["app_id"] — threaded onto the compiled rule exactly as cmd/refractor's
// activation path threads it for any plain, non-actorAggregate,
// non-Personal, non-operation-role-index lens: main.go's threadsKeyColumns),
// and the SAME adapter.DeleteModeHard delete semantics runFlatLensPipeline
// wires for a nats_kv-declared lens — into a NATS KV bucket standing in for
// the lens's real postgres table, because this harness has no Postgres.
// See the test's own doc comment for what that substitution proves and does
// not prove.
func runLeaseApplicationsReadToBucket(h *harness, rule *lens.Rule) *substrate.KV {
	h.t.Helper()
	require.NotNilf(h.t, rule.CompiledRule, "lens %s must compile", rule.CanonicalName)
	require.Falsef(h.t, projection.IsActorAggregate(rule), "lens %s must not be actorAggregate for this flat pipeline", rule.CanonicalName)

	fullCR, ok := rule.CompiledRule.(*full.CompiledRule)
	require.Truef(h.t, ok, "lens %s must compile to the full engine", rule.CanonicalName)
	require.NoErrorf(h.t, projection.ThreadKeyColumns(fullCR, rule.CompiledBranches, rule.Into.Key), "thread %s's key columns", rule.CanonicalName)

	ctx := h.ctx
	targetKV, err := h.conn.OpenKV(ctx, leaseApplicationsReadTestBucket)
	if err != nil {
		_, cerr := h.conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: leaseApplicationsReadTestBucket})
		require.NoErrorf(h.t, cerr, "provision lens target bucket %s", leaseApplicationsReadTestBucket)
		targetKV, err = h.conn.OpenKV(ctx, leaseApplicationsReadTestBucket)
		require.NoErrorf(h.t, err, "open lens target bucket %s after provisioning", leaseApplicationsReadTestBucket)
	}

	adpt, err := adapter.New(targetKV, rule.Into.Key, adapter.DeleteModeHard)
	require.NoError(h.t, err)
	p, err := pipeline.New(rule.ID, "postgres", bootstrap.CoreKVBucket, h.adjKV, h.coreKV, adpt, nil)
	require.NoError(h.t, err)
	p.UseFullEngine(full.New(), rule.CompiledRule)

	p.RunOn(h.conn, refractorSpec(rule.ID))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); p.Run(pipelineCtx) }()
	h.t.Cleanup(func() { pipelineCancel(); <-doneCh })

	return targetKV
}

// readLeaseApplicationsRow reads the leaseApplicationsRead row for appID out
// of the test bucket. The key IS the bare app_id: buildKey's single-key-field
// shape (internal/refractor/adapter/natskv.go) concatenates nothing when
// keyOrder has one element, it just stringifies that one value. Returns nil
// when absent or empty (DeleteModeHard leaves an absent key on retraction).
func readLeaseApplicationsRow(ctx context.Context, kv *substrate.KV, appID string) map[string]any {
	entry, err := kv.Get(ctx, appID)
	if err != nil || entry == nil || len(entry.Value) == 0 {
		return nil
	}
	var row map[string]any
	if json.Unmarshal(entry.Value, &row) != nil {
		return nil
	}
	return row
}

// awaitLeaseApplicationsRow polls readLeaseApplicationsRow on a 150ms
// interval, under a deadline, until it returns a row or the deadline passes —
// the interval sleep paces the condition check, it never stands in for it.
func awaitLeaseApplicationsRow(t *testing.T, ctx context.Context, kv *substrate.KV, appID string, deadline time.Duration) map[string]any {
	t.Helper()
	cut := time.Now().Add(deadline)
	for time.Now().Before(cut) {
		if row := readLeaseApplicationsRow(ctx, kv, appID); row != nil {
			return row
		}
		time.Sleep(150 * time.Millisecond)
	}
	return readLeaseApplicationsRow(ctx, kv, appID)
}
