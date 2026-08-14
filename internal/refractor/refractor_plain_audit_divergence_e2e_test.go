// Plain-lens divergence audit e2e (lens-projection-divergence-audit-design.md
// §4.3, §7): a real substrate, a real plain lens, a row corrupted behind the
// pipeline's back, and the Refractor's own heartbeat raising
// LensProjectionDiverged — with the row STILL corrupt afterwards.
//
// That last assertion is the point of the whole file. §8.1 rejected repair on an
// unguarded, shared plain target for three compounding reasons, the decisive one
// being that the failure this mechanism exists to end was a repair path
// concealing a detection gap. A future author who "completes" the audit by
// giving it a writer passes every other assertion here and fails this one.
package refractor_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// auditE2EBucket stands in for weaver-targets: a shared, UNGUARDED plain-lens
// target — the very property that makes a repair write unsafe here and a
// read-only audit the right shape.
const auditE2EBucket = "audit-divergence-targets"

// auditE2ESpec is an ordinary plain lens: single labelled anchor, one row per
// anchor keyed by the anchor, no $now, no $projectedAt, no envelope. Exactly the
// shape §4.4's conjuncts admit.
const auditE2ESpec = `
MATCH (u:unit)
WHERE u.listing.data.status <> null
RETURN u.key AS key, u.name AS name, u.listing.data.status AS status
`

func TestRefractor_PlainLensDivergenceAudit_DetectsWithoutRepairing_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping plain-lens divergence-audit e2e test in -short mode")
	}

	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	healthKV, err := conn.OpenKV(ctx, "health-kv")
	require.NoError(t, err)
	_, err = conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: auditE2EBucket})
	require.NoError(t, err)
	targetKV, err := conn.OpenKV(ctx, auditE2EBucket)
	require.NoError(t, err)

	const ruleID = "lens-plain-audit-e2e"
	const canonicalName = "listedUnits"

	eng := full.New()
	cr, err := eng.Parse(auditE2ESpec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = []string{"key"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	adpt, err := adapter.New(targetKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	reporter := health.New(healthKV, ruleID)
	p, err := pipeline.New(ruleID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, reporter)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, cr))

	// Enrolment through the REAL gate: what is under test includes the decision
	// that this lens may be audited at all.
	enrolled, refusal := p.InstallAudit()
	require.True(t, enrolled, "an ordinary plain NATS-KV lens must enrol; refusal: %s", refusal)
	require.Equal(t, pipeline.DefaultAuditInterval, p.Auditor().Interval())
	// Compress the clock so the test exercises real passes rather than waiting
	// out the fifteen-minute background cadence. Everything else is what the
	// enrolment just derived.
	p.SetAuditPlan(pipeline.AuditPlan{
		AnchorLabel: p.Auditor().AnchorLabel(),
		Interval:    250 * time.Millisecond,
		Batch:       25,
	})
	auditor := p.Auditor()

	// The lens runs for real: the row under test is written by the pipeline's
	// own write path, so the audit compares against what the lens actually
	// produces rather than against a row the test assembled to match.
	p.RunOn(conn, e2eSpec(ruleID, bootstrap.CoreKVBucket))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	pipelineDone := make(chan struct{})
	go func() { defer close(pipelineDone); p.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-pipelineDone })

	unitID := stableNanoID("plain-audit-unit")
	unitKey := substrate.VertexKey("unit", unitID)
	const provenanceAt = "2026-08-13T10:00:00Z"
	putE2EBody(t, ctx, coreKV, unitKey+".listing", map[string]any{
		"key": unitKey + ".listing", "class": "listing", "vertexKey": unitKey,
		"localName": "listing", "isDeleted": false,
		"data": map[string]any{"status": "active"},
	})
	putE2EBody(t, ctx, coreKV, unitKey, map[string]any{
		"key": unitKey, "class": "unit", "isDeleted": false, "name": "Loft One",
		"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
		"data": map[string]any{},
	})

	projected := waitForEntry(t, ctx, targetKV, unitKey, 30*time.Second,
		"the lens did not project the anchor within 30s")
	var projectedRow map[string]any
	require.NoError(t, json.Unmarshal(projected.Value, &projectedRow))
	require.Equal(t, "Loft One", projectedRow["name"])

	// The divergence: the row is rewritten out of band — a restore, a rogue
	// writer, a partial recovery. No CDC event will ever correct it, because the
	// event that would have is in the past. This is the class the liveness plane
	// structurally cannot see: the lens is active, caught up, and wrong.
	projectedRow["name"] = "Loft One (corrupted out of band)"
	corrupted, err := json.Marshal(projectedRow)
	require.NoError(t, err)
	corruptRev, err := targetKV.Put(ctx, unitKey, corrupted)
	require.NoError(t, err)

	auditCtx, stopAudit := context.WithCancel(ctx)
	defer stopAudit()
	audited := make(chan struct{})
	go func() { defer close(audited); p.RunAudit(auditCtx) }()

	// The audit finds it on its own: nobody named the row.
	verdictDeadline := time.Now().Add(60 * time.Second)
	for auditor.Status().DivergentTotal == 0 {
		if time.Now().After(verdictDeadline) {
			t.Fatalf("the audit did not detect the corrupted row within 60s; status=%+v", auditor.Status())
		}
		time.Sleep(50 * time.Millisecond)
	}
	st := auditor.Status()
	require.Equal(t, 1, st.DivergentTotal)
	require.Equal(t, map[string]int{pipeline.AuditClassStale: 1}, st.Divergent)
	require.Equal(t, pipeline.AuditCoverageBasisKeyType, st.CoverageBasis)

	// The heartbeat says so, in the Health-KV shape the closed-loop consumer
	// (#96's Weaver auditor) will read. The provider mirrors cmd/refractor's.
	hb := health.NewLatticeHeartbeater(conn, "health-kv", "rfx-audit-e2e", 10*time.Second, logger)
	hb.LensProvider = func() []health.LensLivenessStatus {
		as := auditor.Status()
		return []health.LensLivenessStatus{{
			CanonicalName:         canonicalName,
			RuleID:                ruleID,
			Status:                "active",
			AuditEnrolled:         as.Enrolled,
			AuditRefusal:          as.Refusal,
			Audited:               as.Audited,
			DivergentRows:         as.Divergent,
			DivergentTotal:        as.DivergentTotal,
			AuditUnverified:       as.Unverified,
			AuditLastUnverified:   as.LastUnverified,
			AuditLastPassAt:       as.LastPassAt,
			AuditCycleCompletedAt: as.CycleCompletedAt,
			AuditCoverageBasis:    as.CoverageBasis,
			AuditListingSize:      as.ListingSize,
			AuditSuppression:      as.Suppression,
			AuditSuppressionAt:    as.SuppressionAt,
			AuditInterval:         auditor.Interval(),
		}}
	}
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go hb.Run(hbCtx)

	doc := waitForHealthDoc(t, ctx, healthKV, "health.refractor.rfx-audit-e2e", 30*time.Second)
	issues, _ := doc["issues"].([]any)
	var diverged map[string]any
	for _, raw := range issues {
		issue, ok := raw.(map[string]any)
		if ok && issue["code"] == "LensProjectionDiverged" {
			diverged = issue
			break
		}
	}
	require.NotNil(t, diverged, "the heartbeat must raise LensProjectionDiverged; issues=%v", issues)
	require.Equal(t, "warning", diverged["severity"],
		"a business lens's wrong read model degrades the instance, it never fails it")

	metrics, _ := doc["metrics"].(map[string]any)
	liveness, _ := metrics["lensLiveness"].(map[string]any)
	lensMetric, _ := liveness[canonicalName].(map[string]any)
	require.NotNil(t, lensMetric, "the lens must appear in metrics.lensLiveness; metrics=%v", metrics)
	require.Equal(t, float64(1), lensMetric["divergentTotal"])
	require.Equal(t, "diverged", lensMetric["alert"])
	require.Equal(t, "key-type", lensMetric["auditCoverageBasis"],
		"the coverage bound travels with the verdict, so a clean sibling number reads as the bounded claim it is")
	require.Equal(t, true, lensMetric["auditEnrolled"])

	// THE assertion. Several audit passes have run against the corrupted row by
	// now, and it must still be exactly as wrong as the test made it — same
	// bytes, and the same revision, which no rewrite of any value could satisfy.
	after, err := targetKV.Get(ctx, unitKey)
	require.NoError(t, err)
	require.Equal(t, corruptRev, after.Revision,
		"the audit must never write to the target — detection that depends on a repair is the failure this design exists to end")
	require.JSONEq(t, string(corrupted), string(after.Value),
		"the row must still be corrupt after the audit has reported it")
}

// putE2EBody marshals and stores a Core KV body.
func putE2EBody(t *testing.T, ctx context.Context, kv *substrate.KV, key string, body map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = kv.Put(ctx, key, raw)
	require.NoError(t, err)
}

// waitForEntry polls until a key carries a value, so the test synchronises on
// the condition it cares about rather than on a fixed duration.
func waitForEntry(t *testing.T, ctx context.Context, kv *substrate.KV, key string, within time.Duration, msg string) *substrate.KVEntry {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		entry, err := kv.Get(ctx, key)
		if err == nil && entry != nil && len(entry.Value) > 0 {
			return entry
		}
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForHealthDoc polls the instance heartbeat until one carries the per-lens
// liveness map — the first emit lands immediately, but the document is written
// asynchronously.
func waitForHealthDoc(t *testing.T, ctx context.Context, kv *substrate.KV, key string, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		entry, err := kv.Get(ctx, key)
		if err == nil && entry != nil && len(entry.Value) > 0 {
			var doc map[string]any
			if json.Unmarshal(entry.Value, &doc) == nil {
				if metrics, ok := doc["metrics"].(map[string]any); ok {
					if _, ok := metrics["lensLiveness"]; ok {
						return doc
					}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no heartbeat carrying metrics.lensLiveness within %s", within)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
