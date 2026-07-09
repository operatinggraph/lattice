// CAR E6 proof — the shipped lease-signing leaseApplicationComplete convergence
// lens projects SCALAR body columns (the §10.2 violating / missing_* bools +
// entityKey / applicant strings) through the LIVE Refractor actorAggregate
// pipeline as genuine scalars, not coerced to [].
//
// The lens declaration is taken VERBATIM from packages/lease-signing
// (leasesigning.Lenses()) — proving the package needs ZERO change for E6 (the
// lens is pre-shaped: keyColumn set, scalar body columns named). It is installed
// via the real InstallPackage op path (pkgmgr.Installer → meta-lane Processor →
// atomic commit), activated by the live lens.CoreKVSource watch, and wired
// through the production projection.InstallActorAggregate — the exact function
// cmd/refractor calls. The fixture is written straight into Core KV (leaseapp +
// applicant identity + two service instances across the applicationFor/providedTo
// links), the multi-instance fan-out case §0.C guards.
//
// Before E6 the scalar columns projected as {"violating":[],...}; Weaver's
// boolColumn read [] (not a bool) and never dispatched. This test asserts the
// projected weaver-targets row now carries Go bool / string scalars Weaver reads
// directly — the proof E6 unblocks Story 14.4.
package refractor_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/projection"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
	leasesigning "github.com/asolgan/lattice/packages/lease-signing"
)

// leaseSigningLensPackage wraps the REAL leaseApplicationComplete lens spec
// (pulled verbatim from leasesigning.Lenses()) in a minimal installable package.
// Using the shipped declaration unchanged is the "package needs ZERO change for
// E6" proof. The full lease-signing package's DDLs/ops are not needed here — the
// fixture writes the leaseapp/identity/service vertices straight into Core KV, so
// only the lens has to install + activate.
func leaseSigningLensPackage(t *testing.T) (pkgmgr.Definition, pkgmgr.LensSpec) {
	t.Helper()
	var convLens pkgmgr.LensSpec
	for _, l := range leasesigning.Lenses() {
		if l.CanonicalName == "leaseApplicationComplete" {
			convLens = l
		}
	}
	require.NotEmpty(t, convLens.CanonicalName, "lease-signing must declare leaseApplicationComplete")
	require.NotNil(t, convLens.Output, "the convergence lens must carry an Output descriptor")
	require.Equal(t, "actorAggregate", convLens.ProjectionKind, "the convergence lens must be an actorAggregate")
	require.Equal(t, "entityId", convLens.Output.KeyColumn, "the convergence lens must declare the §10.2 keyColumn")

	return pkgmgr.Definition{
		Name:        "lease-signing-lens-only",
		Version:     "1.0.0",
		Description: "E6 proof wrapper: the real leaseApplicationComplete convergence lens, installed unchanged.",
		Lenses:      []pkgmgr.LensSpec{convLens},
	}, convLens
}

func TestRefractor_LeaseSigningConvergence_ProjectsScalarColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping lease-signing scalar convergence proof e2e in -short mode")
	}

	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	js := conn.JetStream()

	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err = bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	// The real §10.2 convergence bucket (primordial — provisioned by the seeder).
	convKV, err := conn.OpenKV(ctx, "weaver-targets")
	require.NoError(t, err)

	// --- adjacency bootstrapper ---
	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	// --- meta-lane Processor pipeline so the InstallPackage op commits ---
	cp, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "lease-conv-meta")
	require.NoError(t, err)
	metaCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName:     "core-operations",
		Durable:        "lease-conv-meta",
		FilterSubjects: []string{"ops.meta"},
		AckWait:        5 * time.Second,
	}, logger)
	require.NoError(t, err)
	metaCtx, metaCancel := context.WithCancel(ctx)
	defer metaCancel()
	metaCC, err := metaCons.Consume(func(m jetstream.Msg) { cp.HandleMessage(metaCtx, m) })
	require.NoError(t, err)
	defer metaCC.Stop()

	// --- install the REAL lease-signing convergence lens via InstallPackage ---
	pkg, _ := leaseSigningLensPackage(t)
	installer := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	res, err := installer.Install(ctx, pkg)
	require.NoError(t, err, "InstallPackage of the lease-signing convergence lens must succeed")
	require.NotNil(t, res)

	// --- live activation: CoreKVSource discovers the installed lens; the generic
	// actor-aggregate path wires it (no canonical-name, no type knowledge) ---
	fullEngine := full.New()
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, logger)
	loaded := make(chan *lens.Rule, 8)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	var convRule *lens.Rule
	deadline := time.Now().Add(20 * time.Second)
	for convRule == nil {
		if time.Now().After(deadline) {
			t.Fatal("did not activate the leaseApplicationComplete lens within 20s")
		}
		select {
		case r := <-loaded:
			if r.CanonicalName == "leaseApplicationComplete" {
				convRule = r
			}
		case <-time.After(200 * time.Millisecond):
		}
	}

	require.Equal(t, "actorAggregate", convRule.ProjectionKind)
	require.NotNil(t, convRule.Output)
	require.Equal(t, "entityId", convRule.Output.KeyColumn,
		"keyColumn must survive the package-spec → InstallPackage → spec aspect → CoreKVSource round-trip")

	convAdpt, err := adapter.New(convKV, convRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New(convRule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, convAdpt, nil)
	require.NoError(t, err)
	require.NotNil(t, convRule.CompiledRule, "actor-aggregate lens must resolve a compiled rule")
	p.UseFullEngine(fullEngine, convRule.CompiledRule)
	require.True(t, projection.InstallActorAggregate(p, convAdpt, convRule, projectionRevision, adjKV, coreKV, logger),
		"leaseApplicationComplete lens must install through projection.InstallActorAggregate")

	p.RunOn(conn, e2eSpec(convRule.ID, bootstrap.CoreKVBucket))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); p.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-doneCh })

	// --- fixture: leaseapp + applicant identity + 2 service instances (bgcheck,
	// payment), all gaps open. The multi-instance fan-out the §0.C guard covers. ---
	appID := stableNanoID("lease-conv-app")
	idID := stableNanoID("lease-conv-applicant")
	bgID := stableNanoID("lease-conv-bg")
	payID := stableNanoID("lease-conv-pay")
	appKey := substrate.VertexKey("leaseapp", appID)
	idKey := substrate.VertexKey("identity", idID)

	const provenanceAt = "2026-05-15T10:00:00Z"
	writeVertex := func(key, class string, data map[string]any) {
		body := map[string]any{
			"key": key, "class": class, "isDeleted": false,
			"createdAt": provenanceAt, "lastModifiedAt": provenanceAt, "data": data,
		}
		raw, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, key, raw)
		require.NoError(t, perr)
	}
	writeAspect := func(ownerKey, local, class string, data map[string]any) {
		key := ownerKey + "." + local
		body := map[string]any{
			"key": key, "class": class, "vertexKey": ownerKey, "localName": local,
			"isDeleted": false, "createdAt": provenanceAt, "lastModifiedAt": provenanceAt, "data": data,
		}
		raw, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, key, raw)
		require.NoError(t, perr)
	}
	buildEdge := func(name, fromType, fromID, toType, toID string) {
		linkKey := substrate.LinkKey(fromType, fromID, name, toType, toID)
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

	writeVertex(idKey, "identity", map[string]any{"name": "applicant"})
	// The family discriminator is the vertex ENVELOPE class (P7 —
	// service.<family>.instance), not a .family shadow aspect; the key-type stays
	// `service` so the lens MATCH (inst:service) still binds, and the lens reads
	// inst.class to bucket the family.
	writeVertex(substrate.VertexKey("service", bgID), "service.backgroundCheck.instance", map[string]any{})
	writeVertex(substrate.VertexKey("service", payID), "service.payment.instance", map[string]any{})
	buildEdge("providedTo", "service", bgID, "identity", idID)
	buildEdge("providedTo", "service", payID, "identity", idID)
	buildEdge("applicationFor", "leaseapp", appID, "identity", idID)
	// Write the anchor LAST so its CDC event reprojects with links + neighbors in place.
	writeVertex(appKey, "leaseapp", map[string]any{})

	// The §10.2 Option (b) convergence key: <targetId>.<bareNanoID>.
	convKey := "leaseApplicationComplete." + appID

	// --- PROJECT (all gaps open): assert the row carries SCALAR columns, not []. ---
	var openEnv map[string]any
	require.Eventually(t, func() bool {
		entry, gErr := convKV.Get(ctx, convKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false
		}
		var env map[string]any
		if json.Unmarshal(entry.Value, &env) != nil {
			return false
		}
		// Wait until violating is the Go bool true (the all-gaps-open steady state),
		// proving the scalar landed (not [] and not a transient partial projection).
		v, isBool := env["violating"].(bool)
		if !isBool || !v {
			return false
		}
		openEnv = env
		return true
	}, 30*time.Second, 100*time.Millisecond, "the convergence lens did not project a scalar violating:true row")

	// The bare-NanoID key shape Weaver's splitRowKey accepts.
	tail := convKey[strings.IndexByte(convKey, '.')+1:]
	require.True(t, substrate.IsValidNanoID(tail), "convergence key tail %q must be a bare NanoID", tail)
	require.Equal(t, 1, strings.Count(convKey, "."), "convergence key must have exactly one dot")

	// Every bool gap column must be a Go bool (Weaver's boolColumn reads a bool;
	// a [] would be a RowDataError and never actionable). All gaps open → all true.
	for _, col := range []string{"violating", "missing_onboarding", "missing_bgcheck", "missing_payment", "missing_signature"} {
		v, isBool := openEnv[col].(bool)
		require.Truef(t, isBool, "column %q must project as a Go bool, got %T %v (E6: scalars must not coerce to [])", col, openEnv[col], openEnv[col])
		require.Truef(t, v, "column %q must be true with all gaps open", col)
		_, isList := openEnv[col].([]any)
		require.Falsef(t, isList, "column %q must NOT project as a list", col)
	}
	// The §10.8 param columns must be the full vertex-key STRINGS the playbook
	// templates resolve (row.entityKey / row.applicant), not lists.
	require.Equal(t, appKey, openEnv["entityKey"], "entityKey must be the full leaseapp key string")
	require.Equal(t, idKey, openEnv["applicant"], "applicant must be the full identity key string")
	if _, isList := openEnv["entityKey"].([]any); isList {
		t.Fatal("entityKey must not project as a list")
	}

	// --- ROUND-TRIP THE OTHER DIRECTION: close every gap → violating flips to the
	// Go bool false (not [], not absent). Writing .ssn (onboarding), both .outcome
	// aspects (bgcheck + payment), .signature, and the landlord .decision closes all
	// gaps (the four applicant gaps + the landlord-decision gate). ---
	writeAspect(idKey, "ssn", "ssn", map[string]any{"value": "123456789"})
	// The bgcheck closes its gap only when completed AND fresh: validUntil must be
	// in the future relative to the projection-supplied $now (time.Now() on the
	// live path), so a far-future instant keeps it fresh regardless of run time.
	// Payment is ever-completed (no validUntil needed).
	writeAspect(substrate.VertexKey("service", bgID), "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-01T00:00:00Z", "validUntil": "2099-01-01T00:00:00Z"})
	writeAspect(substrate.VertexKey("service", payID), "outcome", "outcome", map[string]any{"status": "completed", "completedAt": "2026-06-02T00:00:00Z"})
	writeAspect(appKey, "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	// The landlord's decision is the human gate the convergence waits behind: a
	// qualified application without it stays violating (missing_decision). There is no
	// unit here, so an approval closes missing_decision and leaves no listing flip.
	writeAspect(appKey, "decision", "decision", map[string]any{"value": "approved", "decidedAt": "2026-06-26T10:00:00Z"})
	// The executed-lease document chain: a SIGNED application converges only once
	// the document is produced (a docGen claim providedTo the APP with a completed
	// pointer-carrying .outcome) and anchored (the signedLease object link) --
	// missing_leaseDoc / missing_leaseDocAttach both fold into violating. Edges
	// first (adjacency), then the CDC-triggering writes.
	dgID := stableNanoID("lease-conv-docgen")
	docObjID := stableNanoID("lease-conv-leasedoc")
	buildEdge("providedTo", "service", dgID, "leaseapp", appID)
	buildEdge("signedLease", "object", docObjID, "leaseapp", appID)
	writeVertex(substrate.VertexKey("service", dgID), "service.docGen.instance", map[string]any{})
	writeAspect(substrate.VertexKey("service", dgID), "outcome", "leaseDocOutcome", map[string]any{
		"status": "completed", "completedAt": "2026-06-10T00:00:05Z",
		"digest": "SHA-256=abc123", "size": 1264, "contentType": "text/plain; charset=utf-8",
		"storeName": "dgStoreNanoXyz", "filename": "signed-lease-leaseapp.test.txt",
	})
	writeVertex(substrate.VertexKey("object", docObjID), "object", map[string]any{})

	require.Eventually(t, func() bool {
		entry, gErr := convKV.Get(ctx, convKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false
		}
		var env map[string]any
		if json.Unmarshal(entry.Value, &env) != nil {
			return false
		}
		v, isBool := env["violating"].(bool)
		if !isBool || v {
			return false // wait for the Go bool false
		}
		// All gap bools must now read false as Go bools (the convergent state).
		for _, col := range []string{"missing_onboarding", "missing_bgcheck", "missing_payment", "missing_signature"} {
			b, ok := env[col].(bool)
			if !ok || b {
				return false
			}
		}
		// The param strings survive the convergent projection too.
		return env["entityKey"] == appKey && env["applicant"] == idKey
	}, 30*time.Second, 100*time.Millisecond, "closing all gaps did not flip violating to the Go bool false with scalar columns intact")
}
