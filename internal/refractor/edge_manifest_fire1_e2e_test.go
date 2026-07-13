// Package refractor_test — end-to-end proof for edge-showcase-app-design.md
// Fire 1 (the manifest package + vocabulary, G9's seeded-topology half):
// a demo tenant's five edge-manifest Personal Lenses (edgeIdentity/
// edgeServices/edgeCatalog/edgeTasks/edgeInstances) project real graph
// topology onto the tenant's `lattice.sync.user.<id>` SYNC subject, and
// RequestService — submitted under authContext.service through the REAL
// Processor (real CapabilityAuthorizer, Hydrator, Starlark executor,
// Validator, Committer; only the auth-plane cap.svc doc is pre-seeded,
// per testutil.SeedCapDoc's documented "tests short-circuit the Refractor
// projection" convention) — produces a service instance that arrives as a
// sixth delta on the same subject.
//
// Self-contained: one embedded NATS (jsstore.Dir(t) StoreDir, no Docker, no
// shared dev stack), mirroring personal_lens_pl2_e2e_test.go's pl2Harness
// (reused verbatim, same package — activateEdgeManifestLenses below batches
// its activatePersonalLens pattern for all five lenses at once, since that
// helper's detector binds the single shared "refractor-lens-source" durable)
// and refractor_package_actoraggregate_proof_e2e_test.go's bootstrap-seeder +
// meta-lane-install pattern.
//
// Deliberate simplifications (edge-showcase-app-design.md Fire 1 scope, not
// gaps this test owns to close):
//   - service-domain's `family` enum is closed to {backgroundCheck, payment}
//     (service_instance_test.go's FamilyOutOfEnum_Rejected proves it); the
//     demo template uses the real `backgroundCheck` family, branded "Maple
//     Laundry" via its `.presentation` aspect (presentation is decoupled
//     from family — §3.3) rather than widening service-domain's schema.
//   - The building/unit/tenant/task/links are written directly to Core KV
//     (mirroring the aspectfanout/actoraggregate proof tests' writeVertex/
//     writeAspect/writeLink convention) rather than driven through
//     location-domain/identity-domain/orchestration-base's own ops — those
//     packages' op-level behavior is proved by their own package tests; this
//     test's job is the manifest projection + the RequestService write path,
//     not re-proving every upstream DDL. Only service-domain (RequestService
//     itself) is installed and driven through the real Processor.
//   - The cap.svc.<tenant> availability grant is pre-seeded via
//     testutil.SeedCapDoc rather than derived by running service-location's
//     own capabilityServiceAccess lens — that lens's correctness is
//     service-location's own package_test.go/integration_test.go's job.
package refractor_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
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
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/projection"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	edgemanifest "github.com/asolgan/lattice/packages/edge-manifest"
	servicedomain "github.com/asolgan/lattice/packages/service-domain"
	"github.com/asolgan/lattice/scripts/pkgverify"
)

// emWriteVertex writes a vertex with business data nested under `.data`
// (the shape every real DDL script's make_vtx produces, and the shape
// edge-manifest's cyphers address as node.data.<field>) — mirroring
// refractor_capability_aspectfanout_e2e_test.go's writeVertex closure.
func emWriteVertex(t *testing.T, ctx context.Context, coreKV *substrate.KV, key, class string, data map[string]any) {
	t.Helper()
	body := map[string]any{
		"key": key, "class": class, "isDeleted": false,
		"createdAt": "2026-07-12T00:00:00Z", "lastModifiedAt": "2026-07-12T00:00:00Z",
		"data": data,
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, key, b)
	require.NoError(t, err)
}

// emWriteAspect writes a 4-segment aspect key with business data nested
// under `.data` (make_aspect's real shape — edgeIdentitySpec/edgeServicesSpec
// address it as node.<localName>.data.<field>).
func emWriteAspect(t *testing.T, ctx context.Context, coreKV *substrate.KV, vtxKey, localName, class string, data map[string]any) {
	t.Helper()
	aspectKey := vtxKey + "." + localName
	body := map[string]any{
		"class": class, "isDeleted": false,
		"vertexKey": vtxKey, "localName": localName, "data": data,
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, aspectKey, b)
	require.NoError(t, err)
}

// emWriteLink writes a 6-segment link key (Contract #1 §1.1: the
// later-arriving vertex is the source), mirroring the same helper's
// convention across every e2e in this package.
func emWriteLink(t *testing.T, ctx context.Context, coreKV *substrate.KV, srcType, srcID, name, dstType, dstID string) {
	t.Helper()
	linkKey := substrate.LinkKey(srcType, srcID, name, dstType, dstID)
	body := map[string]any{
		"key": linkKey, "class": name, "isDeleted": false,
		"sourceVertex": substrate.VertexKey(srcType, srcID),
		"targetVertex": substrate.VertexKey(dstType, dstID),
		"localName":    name,
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = coreKV.Put(ctx, linkKey, b)
	require.NoError(t, err)
}

// activateEdgeManifestLenses activates all five REAL edge-manifest Personal
// Lenses (packages/edge-manifest.Lenses() — the shipped cyphers, not
// hand-copies) in one batch against h. It reproduces
// personal_lens_pl2_e2e_test.go's activatePersonalLens wiring (meta-vertex +
// spec write, CoreKVSource detection, full-engine compile,
// projection.InstallPersonalLens, RunOn+Run), but batched: rather than five
// separate activatePersonalLens calls (each its own CoreKVSource, matching
// only its own lensID), this writes all five specs first, then runs ONE
// CoreKVSource pass that discovers and wires all five, matched by
// CanonicalName — simpler than reconciling five independent load channels.
func activateEdgeManifestLenses(t *testing.T, h *pl2Harness) {
	t.Helper()
	const subjectPrefix = "lattice.sync.user"
	const syncStream = "SYNC"

	specs := edgemanifest.Lenses()
	lensIDs := make(map[string]string, len(specs))
	for _, ls := range specs {
		require.GreaterOrEqual(t, len(ls.IntoKey), 1, "%s IntoKey must include __actor", ls.CanonicalName)
		lensIDs[ls.CanonicalName] = pl2NanoID("em-fire1-lens-" + ls.CanonicalName)
	}

	src := lens.NewCoreKVSource(h.conn, "core-kv", "test", h.logger)
	activated := make(chan *lens.Rule, len(specs)*2)
	src.SetLoadCallback(func(r *lens.Rule) {
		select {
		case activated <- r:
		default:
		}
	})
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(h.ctx))

	for _, ls := range specs {
		lensID := lensIDs[ls.CanonicalName]
		keyField := append([]string{adapter.PersonalActorKeyField}, ls.IntoKey[1:]...)
		keyJSON, err := json.Marshal(keyField)
		require.NoError(t, err)

		metaVertexKey := "vtx.meta." + lensID
		specKey := metaVertexKey + ".spec"
		vertexJSON, err := json.Marshal(map[string]any{"class": "meta.lens", "key": metaVertexKey, "data": map[string]any{}})
		require.NoError(t, err)
		_, err = h.coreKV.Put(h.ctx, metaVertexKey, vertexJSON)
		require.NoError(t, err)

		spec := lens.LensSpec{
			ID: lensID, CanonicalName: ls.CanonicalName, TargetType: "nats_subject", CypherRule: ls.Spec,
			TargetConfig: json.RawMessage(`{"subjectPrefix":"` + subjectPrefix + `","stream":"` + syncStream +
				`","personal":true,"key":` + string(keyJSON) + `}`),
		}
		specJSON, err := json.Marshal(spec)
		require.NoError(t, err)
		_, err = h.coreKV.Put(h.ctx, specKey, specJSON)
		require.NoError(t, err)
	}

	got := map[string]*lens.Rule{}
	deadline := time.Now().Add(20 * time.Second)
	for len(got) < len(specs) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("only activated %d/%d edge-manifest lenses within deadline (have: %v)", len(got), len(specs), got)
		}
		select {
		case r := <-activated:
			if _, want := lensIDs[r.CanonicalName]; want {
				got[r.CanonicalName] = r
			}
		case <-time.After(remaining):
		}
	}

	for _, ls := range specs {
		r := got[ls.CanonicalName]
		require.True(t, projection.IsPersonalLens(r), "%s must be recognized as a personal lens", ls.CanonicalName)
		require.True(t, ruleUsesFullEngine(t, r), "%s must compile under the full engine", ls.CanonicalName)

		adpt, err := adapter.NewNatsSubjectAdapter(h.ctx, h.conn, subjectPrefix, syncStream,
			append([]string{adapter.PersonalActorKeyField}, ls.IntoKey[1:]...))
		require.NoError(t, err)
		p, err := pipeline.New(r.ID, "nats_subject", "core-kv", h.adjKV, h.coreKV, adpt, nil)
		require.NoError(t, err)
		p.UseFullEngine(fullEngineSingleton, r.CompiledRule)
		require.True(t, projection.InstallPersonalLens(p, r, h.adjKV, h.coreKV, h.interestKV, h.capKV, h.logger),
			"%s must install through projection.InstallPersonalLens", ls.CanonicalName)

		p.RunOn(h.conn, e2eSpec(r.ID, "core-kv"))
		pipelineCtx, pipelineCancel := context.WithCancel(h.ctx)
		go p.Run(pipelineCtx)
		t.Cleanup(pipelineCancel)
	}
}

// TestEdgeManifest_Fire1_E2E_FiveRowKindsAndRequestService is the Fire 1
// green bar (edge-showcase-app-design.md §7): "a seeded tenant receives all
// five row kinds over SYNC; RequestService submits under
// authContext.service and the instance row arrives."
func TestEdgeManifest_Fire1_E2E_FiveRowKindsAndRequestService(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping edge-manifest Fire 1 e2e in -short mode")
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	js := conn.JetStream()

	// --- bootstrap: platform buckets (core-kv/health-kv/capability-kv/
	// refractor-adjacency/personal-lens-interest) + core-operations stream +
	// the primordial admin identity + operator role, mirroring the
	// actoraggregate proof test's setup. ---
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
	interestKV, err := conn.OpenKV(ctx, bootstrap.PersonalLensInterestKV)
	require.NoError(t, err)

	// --- adjacency bootstrapper (personal-lens fan-out needs the live
	// adjacency index) ---
	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	// --- install service-domain (RequestService's DDL + its op-meta) via
	// the REAL InstallPackage path, meta-lane, stub-auth (mirrors the
	// actoraggregate proof test's package-install step) ---
	metaCP, _, err := processor.MakeStubPipeline(conn, bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, processor.AuthModeStub, logger, "em-fire1-meta")
	require.NoError(t, err)
	metaCons, err := processor.EnsureConsumer(ctx, js, processor.ConsumerConfig{
		StreamName: "core-operations", Durable: "em-fire1-meta",
		FilterSubjects: []string{"ops.meta"}, AckWait: 5 * time.Second,
	}, logger)
	require.NoError(t, err)
	metaCtx, metaCancel := context.WithCancel(ctx)
	defer metaCancel()
	metaCC, err := metaCons.Consume(func(m jetstream.Msg) { metaCP.HandleMessage(metaCtx, m) })
	require.NoError(t, err)
	defer metaCC.Stop()

	installer := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	installer.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
	_, err = installer.Install(ctx, servicedomain.Package)
	require.NoError(t, err, "installing service-domain (RequestService's DDL) must succeed")

	// --- find the RequestService op-meta the install just minted. Unlike a
	// DDL/Lens/Role, an op-meta carries NO canonicalName aspect
	// (internal/pkgmgr/build.go's op-meta loop writes only the vertex root
	// {operationType} + the optional presentation/inputSchema/
	// fieldDescriptions/dispatch/sensitive/effects aspects) — so it is found
	// by scanning vtx.meta.<id> root vertices for data.operationType, not by
	// pkgverify.FindMetaByCanonical (that helper is for DDL/Lens canonical
	// names only). ---
	rawCoreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	allKeys, err := pkgverify.ListAllKeys(ctx, rawCoreKV)
	require.NoError(t, err)
	opMetaKey, err := pkgverify.FindOpMetaByOperationType(ctx, rawCoreKV, allKeys, "RequestService")
	require.NoError(t, err)
	require.NotEmpty(t, opMetaKey, "RequestService op-meta must exist after installing service-domain")

	// --- activate the five REAL edge-manifest Personal Lenses (the actual
	// production package.Lenses(), not hand-copied cyphers) via
	// activateEdgeManifestLenses: all five specs are written up front and a
	// single CoreKVSource activates all five in one pass, matched by
	// CanonicalName — simpler than reconciling activatePersonalLens's
	// (PL2's helper) five independent load channels one lens at a time. ---
	h := &pl2Harness{ctx: ctx, conn: conn, js: js, coreKV: coreKV, adjKV: adjKV, interestKV: interestKV, capKV: nil, logger: logger}
	activateEdgeManifestLenses(t, h)

	// --- demo topology: a building containing a unit, a tenant residing in
	// the unit, a service template ("Maple Laundry") availableAt the
	// building with RequestService permitted, and an open task assigned to
	// the tenant (edge-showcase-app-design.md §3.2's worked example). ---
	tenantID := pl2NanoID("em-fire1-tenant")
	tenantKey := substrate.VertexKey("identity", tenantID)
	buildingID := pl2NanoID("em-fire1-building")
	buildingKey := substrate.VertexKey("building", buildingID)
	unitID := pl2NanoID("em-fire1-unit")
	unitKey := substrate.VertexKey("unit", unitID)
	templateID := pl2NanoID("em-fire1-template")
	templateKey := substrate.VertexKey("service", templateID)
	roleID := pl2NanoID("em-fire1-role")
	roleKey := substrate.VertexKey("role", roleID)
	taskID := pl2NanoID("em-fire1-task")
	taskKey := substrate.VertexKey("task", taskID)

	emWriteVertex(t, ctx, coreKV, buildingKey, "location", map[string]any{})
	emWriteVertex(t, ctx, coreKV, unitKey, "location", map[string]any{})
	emWriteLink(t, ctx, coreKV, "unit", unitID, "containedIn", "building", buildingID)

	emWriteVertex(t, ctx, coreKV, tenantKey, "identity", map[string]any{})
	emWriteAspect(t, ctx, coreKV, tenantKey, "name", "name", map[string]any{"value": "Demo Tenant"})
	emWriteVertex(t, ctx, coreKV, roleKey, "role", map[string]any{})
	emWriteAspect(t, ctx, coreKV, roleKey, "canonicalName", "canonicalName", map[string]any{"value": "consumer"})
	emWriteLink(t, ctx, coreKV, "identity", tenantID, "holdsRole", "role", roleID)
	emWriteLink(t, ctx, coreKV, "identity", tenantID, "residesIn", "unit", unitID)

	emWriteVertex(t, ctx, coreKV, templateKey, "service.backgroundCheck.template", map[string]any{})
	emWriteAspect(t, ctx, coreKV, templateKey, "presentation", "presentation", map[string]any{
		"name": "Maple Laundry", "description": "Wash-and-fold, 24h turnaround", "icon": "laundry", "category": "home",
	})
	emWriteLink(t, ctx, coreKV, "service", templateID, "availableAt", "building", buildingID)
	emWriteLink(t, ctx, coreKV, "service", templateID, "permitsOperation", "meta", opMetaKey[len("vtx.meta."):])

	futureExpiry := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	emWriteVertex(t, ctx, coreKV, taskKey, "task", map[string]any{"status": "open", "expiresAt": futureExpiry})
	emWriteLink(t, ctx, coreKV, "task", taskID, "assignedTo", "identity", tenantID)
	emWriteLink(t, ctx, coreKV, "task", taskID, "forOperation", "meta", opMetaKey[len("vtx.meta."):])
	emWriteLink(t, ctx, coreKV, "task", taskID, "scopedTo", "service", templateID)

	// --- subscribe to the tenant's SYNC subject and drain deltas, asserting
	// all four graph-derived row kinds arrive with real content. ---
	drainCons, err := js.CreateOrUpdateConsumer(ctx, "SYNC", jetstream.ConsumerConfig{
		FilterSubject: "lattice.sync.user." + tenantID,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	seen := map[string]map[string]any{}
	drainUntil := func(deadline time.Duration, want func() bool) {
		end := time.Now().Add(deadline)
		for time.Now().Before(end) {
			msg, ferr := drainCons.Next(jetstream.FetchMaxWait(2 * time.Second))
			if ferr != nil {
				if want() {
					return
				}
				continue
			}
			var env map[string]any
			if json.Unmarshal(msg.Data(), &env) != nil {
				continue
			}
			key, _ := env["key"].(string)
			if key == "" {
				continue
			}
			data, _ := env["data"].(map[string]any)
			seen[key] = data
			if want() {
				return
			}
		}
	}

	hasPrefix := func(prefix string) bool {
		for k := range seen {
			if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
				return true
			}
		}
		return false
	}

	drainUntil(45*time.Second, func() bool {
		return seen["manifest.me"] != nil &&
			hasPrefix("manifest.svc.") &&
			hasPrefix("manifest.op.") &&
			hasPrefix("manifest.task.")
	})

	require.NotNil(t, seen["manifest.me"], "manifest.me must arrive")
	require.Equal(t, tenantKey, seen["manifest.me"]["identityKey"])
	require.Equal(t, "Demo Tenant", seen["manifest.me"]["displayName"])

	var svcData map[string]any
	for k, d := range seen {
		if len(k) > len("manifest.svc.") && k[:len("manifest.svc.")] == "manifest.svc." {
			svcData = d
		}
	}
	require.NotNil(t, svcData, "manifest.svc.<tplId> must arrive")
	require.Equal(t, templateKey, svcData["serviceKey"])
	require.Equal(t, "Maple Laundry", svcData["name"])

	var opData map[string]any
	for k, d := range seen {
		if len(k) > len("manifest.op.") && k[:len("manifest.op.")] == "manifest.op." {
			opData = d
		}
	}
	require.NotNil(t, opData, "manifest.op.<opMetaId> must arrive")
	require.Equal(t, "RequestService", opData["operationType"])
	require.Equal(t, "Request service", opData["title"])

	var taskData map[string]any
	for k, d := range seen {
		if len(k) > len("manifest.task.") && k[:len("manifest.task.")] == "manifest.task." {
			taskData = d
		}
	}
	require.NotNil(t, taskData, "manifest.task.<taskId> must arrive")
	require.Equal(t, taskKey, taskData["taskKey"])
	require.Equal(t, "RequestService", taskData["operationType"])

	// --- seed the cap.svc availability grant (bypassing the Refractor's own
	// service-location lens per this file's documented scope) and submit
	// RequestService through the REAL Processor (real CapabilityAuthorizer,
	// Hydrator, Starlark executor, Validator, Committer). ---
	testutil.SeedCapDoc(t, ctx, conn, &processor.CapabilityDoc{
		Key: "cap.svc.identity." + tenantID,
		ServiceAccess: []processor.ServiceAccessEntry{
			{Service: templateKey, ResolvedVia: []string{buildingKey},
				AllowedOperations: []processor.AllowedOperation{{OperationType: "RequestService"}}},
		},
	})

	cp, opsCons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{Durable: "em-fire1-ops"})
	payload, err := json.Marshal(map[string]any{"service": templateKey})
	require.NoError(t, err)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("emFire1RequestSvc"),
		Lane:          processor.LaneDefault,
		OperationType: "RequestService",
		Actor:         tenantKey,
		Class:         "service",
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Payload:       payload,
		ContextHint:   &processor.ContextHint{Reads: []string{templateKey, tenantKey}},
		AuthContext:   &processor.AuthContext{Service: templateKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, opsCons, processor.OutcomeAccepted)
	require.Equal(t, processor.OutcomeAccepted, outcome, "RequestService must be accepted under authContext.service")

	// --- the resulting service instance must arrive as a sixth delta,
	// providedTo the tenant, on the same SYNC subject. ---
	drainUntil(30*time.Second, func() bool { return hasPrefix("manifest.inst.") })

	var instData map[string]any
	for k, d := range seen {
		if len(k) > len("manifest.inst.") && k[:len("manifest.inst.")] == "manifest.inst." {
			instData = d
		}
	}
	require.NotNil(t, instData, "manifest.inst.<instId> must arrive after RequestService commits")
	require.Equal(t, templateKey, instData["templateKey"])
	require.Equal(t, "Maple Laundry", instData["templateName"])
	require.Equal(t, "open", instData["status"])
}
