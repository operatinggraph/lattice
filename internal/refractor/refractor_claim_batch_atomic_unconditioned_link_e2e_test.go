// Live-pipeline reproduction of the claim-batch capability-projection gap —
// facet-staff-worlds-design.md §13.2's checkpoint. §13.2 disproved the
// NATS-layer delivery hypothesis (TestAtomicBatch_UnconditionedMemberIsDeliveredToDurableConsumer,
// internal/substrate) and found TestRefractor_CapabilityLens_LinkFanOut_E2E
// (this package) already proves a SEQUENTIALLY-Put unconditioned holdsRole
// link fans out through the real capabilityRoles pipeline correctly. The one
// combination neither test covers is the live claim ceremony's actual write
// shape: the .state update, the .claimKey tombstone, and the unconditioned
// holdsRole link landing as SIBLING members of ONE NATS atomic batch
// (step8_commit.go:191-215), not as three independent coreKV.Put calls.
//
// This test reproduces exactly that. It is deliberately the minimal
// 3-member atomic batch a claim commits (the identity + its .state=unclaimed
// + .claimKey aspects are pre-seeded via plain writes first, mirroring the
// PRIOR, separate CreateUnclaimedIdentity commit): .state UPDATE conditioned
// on its prior revision, .claimKey TOMBSTONE conditioned on its prior
// revision, and the holdsRole link written with neither CreateOnly nor
// HasRevision — the exact "update mutation whose prior key is not found"
// shape step8_commit.go:207-214 produces for a first-ever grant link.
package refractor_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// stableClaimBatchID returns a deterministic NanoID for this test's fixtures,
// mirroring stableLinkFanID's FNV-1a-over-alphabet construction.
func stableClaimBatchID(role string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 14695981039346656037
	for _, b := range []byte("claimbatch:" + role) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

// TestRefractor_CapabilityLens_ClaimBatchAtomicUnconditionedLink_E2E drives
// the claim ceremony's exact atomic-batch mutation shape through the real,
// live-CDC-driven capabilityRoles pipeline and asserts whether cap.roles.<target>
// projects. A pass here (given the disproven NATS-layer hypothesis and the
// already-green sequential-Put sibling test) narrows the still-open live gap
// to something the live stack has that this harness doesn't; a failure
// captures the bug's minimal repro.
func TestRefractor_CapabilityLens_ClaimBatchAtomicUnconditionedLink_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping claim-batch atomic unconditioned-link e2e test in -short mode")
	}

	// --- embedded NATS ---
	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capabilityKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	// AllowAtomicPublish on Core KV's backing stream — production sets this at
	// primordial provisioning; the e2e siblings never atomic-batch so they
	// never needed it, but AtomicBatch below fails closed without it.
	js := conn.JetStream()
	streamName := "KV_" + bootstrap.CoreKVBucket
	stream, err := js.Stream(ctx, streamName)
	require.NoError(t, err)
	streamCfg := stream.CachedInfo().Config
	streamCfg.AllowAtomicPublish = true
	_, err = js.UpdateStream(ctx, streamCfg)
	require.NoError(t, err)

	// --- adjacency bootstrapper ---
	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	// --- rbac-domain capabilityRoles pipeline, wired identically to
	// TestRefractor_CapabilityLens_LinkFanOut_E2E (this package) so the ONLY
	// variable between that green test and this one is the write mechanism. ---
	rolesSpec := capabilityRolesSpecForTest(t)
	fullEngine := full.New()
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	rolesCR, err := fullEngine.Parse(rolesSpec.Spec)
	require.NoError(t, err, "capabilityRoles spec must parse")
	rolesDesc := descFromPkgSpec(t, rolesSpec)
	capAdpt, err := adapter.New(capabilityKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)

	const rolesLensID = "ClaimBatchRolesLens01"
	capP, err := pipeline.New(rolesLensID, "nats_kv",
		bootstrap.CoreKVBucket, adjKV, coreKV, capAdpt, nil)
	require.NoError(t, err)
	capP.UseFullEngine(fullEngine, rolesCR)
	capP.SetEnvelopeFn(rolesDesc.EnvelopeFn("vtx.meta."+rolesLensID, projectionRevision))
	capP.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, rolesDesc.AnchorType))
	capP.SetActorDeleteKey(rolesDesc.BuildKey)

	capP.RunOn(conn, e2eSpec(rolesLensID, bootstrap.CoreKVBucket))

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	capDone := make(chan struct{})
	go func() { defer close(capDone); capP.Run(pipelineCtx) }()
	t.Cleanup(func() {
		pipelineCancel()
		<-capDone
	})

	// --- fixture: identity, role, permission ---
	identityID := stableClaimBatchID("claimant")
	roleID := stableClaimBatchID("consumer-role")
	permID := stableClaimBatchID("consumer-perm")

	identityKey := substrate.VertexKey("identity", identityID)
	roleKey := substrate.VertexKey("role", roleID)
	permKey := substrate.VertexKey("permission", permID)
	stateKey := identityKey + ".state"
	claimKeyAspectKey := identityKey + ".claimKey"
	holdsRoleKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", roleID)

	const provenanceAt = "2026-05-15T10:00:00Z"
	writeVertex := func(key, class string, extra map[string]any) uint64 {
		body := map[string]any{
			"key":            key,
			"class":          class,
			"isDeleted":      false,
			"createdAt":      provenanceAt,
			"lastModifiedAt": provenanceAt,
			"data":           extra,
		}
		data, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		rev, perr := coreKV.Put(ctx, key, data)
		require.NoError(t, perr)
		return rev
	}

	// (a) role + permission, GRANTED long before any claim — static, installed
	// topology mirroring rbac-domain's install-time grantedBy edges (§13.2
	// hypothesis 3). Not part of the atomic batch under test.
	writeVertex(roleKey, "role", map[string]any{"canonicalName": "consumer"})
	writeVertex(permKey, "permission", map[string]any{
		"operationType": "InitiateCredentialLink",
		"scope":         "self",
	})
	grantedByLinkKey := substrate.LinkKey("permission", permID, "grantedBy", "role", roleID)
	grantedByBody, jerr := json.Marshal(map[string]any{
		"key": grantedByLinkKey, "class": "grantedBy", "isDeleted": false,
		"sourceVertex": permKey, "targetVertex": roleKey, "localName": "grantedBy",
	})
	require.NoError(t, jerr)
	_, err = coreKV.Put(ctx, grantedByLinkKey, grantedByBody)
	require.NoError(t, err)

	// (b) the identity + its .state=unclaimed + .claimKey aspects — the PRIOR,
	// separate CreateUnclaimedIdentity commit's already-durable writes. Plain
	// puts, not the batch under test; captures their revisions for the
	// claim batch's conditioned members below.
	writeVertex(identityKey, "identity", map[string]any{"name": "claimant"})
	stateUnclaimedBody, jerr := json.Marshal(map[string]any{
		"key": stateKey, "class": "state", "isDeleted": false,
		"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
		"data": map[string]any{"value": "unclaimed"},
	})
	require.NoError(t, jerr)
	stateRev, perr := coreKV.Put(ctx, stateKey, stateUnclaimedBody)
	require.NoError(t, perr)

	claimKeyBody, jerr := json.Marshal(map[string]any{
		"key": claimKeyAspectKey, "class": "claimKey", "isDeleted": false,
		"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
		"data": map[string]any{"hash": "deadbeef"},
	})
	require.NoError(t, jerr)
	claimKeyRev, perr := coreKV.Put(ctx, claimKeyAspectKey, claimKeyBody)
	require.NoError(t, perr)

	// --- Sanity: no role held yet, cap doc absent. ---
	capKey := "cap.roles.identity." + identityID
	capDocAbsent := func() bool {
		_, gErr := capabilityKV.Get(ctx, capKey)
		return errors.Is(gErr, substrate.ErrKeyNotFound)
	}
	require.Eventually(t, capDocAbsent, 20*time.Second, 100*time.Millisecond,
		"identity cap doc must stay absent before the claim batch")

	// --- THE CLAIM BATCH: exactly step8_commit.go's shape for ClaimIdentity —
	// .state UPDATE conditioned on its prior revision, .claimKey TOMBSTONE
	// conditioned on its prior revision, holdsRole link written with NEITHER
	// CreateOnly NOR HasRevision (the "update mutation whose prior key is not
	// found" shape, step8_commit.go:207-214) — all as siblings of ONE
	// substrate.Conn.AtomicBatch call. ---
	stateClaimedBody, jerr := json.Marshal(map[string]any{
		"key": stateKey, "class": "state", "isDeleted": false,
		"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
		"data": map[string]any{"value": "claimed"},
	})
	require.NoError(t, jerr)
	claimKeyTombstoneBody, jerr := json.Marshal(map[string]any{
		"key": claimKeyAspectKey, "class": "claimKey", "isDeleted": true,
		"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
		"data": map[string]any{"hash": "deadbeef"},
	})
	require.NoError(t, jerr)
	holdsRoleBody, jerr := json.Marshal(map[string]any{
		"key": holdsRoleKey, "class": "holdsRole", "isDeleted": false,
		"sourceVertex": identityKey, "targetVertex": roleKey, "localName": "holdsRole",
	})
	require.NoError(t, jerr)

	ops := []substrate.BatchOp{
		{
			Bucket: bootstrap.CoreKVBucket, Key: stateKey, Value: stateClaimedBody,
			HasRevision: true, Revision: stateRev,
		},
		{
			Bucket: bootstrap.CoreKVBucket, Key: claimKeyAspectKey, Value: claimKeyTombstoneBody,
			HasRevision: true, Revision: claimKeyRev,
		},
		{
			// Deliberately unconditioned — no CreateOnly, no HasRevision.
			Bucket: bootstrap.CoreKVBucket, Key: holdsRoleKey, Value: holdsRoleBody,
		},
	}
	ack, err := conn.AtomicBatch(ctx, ops)
	require.NoError(t, err, "claim batch AtomicBatch must commit")
	require.Equal(t, uint64(3), ack.Count, "claim batch must commit all 3 members atomically")

	// --- THE ASSERTION: does the unconditioned holdsRole link's fan-out
	// reproject the identity's cap.roles doc when it rode inside the SAME
	// atomic batch as the conditioned .state/.claimKey siblings? ---
	hasConsumerGrant := func(env map[string]any) bool {
		pp, _ := env["platformPermissions"].([]any)
		for _, e := range pp {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if m["operationType"] == "InitiateCredentialLink" && m["scope"] == "self" {
				return true
			}
		}
		return false
	}
	getEnv := func() (map[string]any, bool) {
		entry, gErr := capabilityKV.Get(ctx, capKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return nil, false
		}
		var env map[string]any
		if jerr := json.Unmarshal(entry.Value, &env); jerr != nil {
			return nil, false
		}
		return env, true
	}

	projected := false
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if env, ok := getEnv(); ok && hasConsumerGrant(env) {
			projected = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !projected {
		env, _ := getEnv()
		t.Logf("MINIMAL REPRO CAPTURED: cap.roles.%s never gained the InitiateCredentialLink/self "+
			"grant from the atomic-batch-committed unconditioned holdsRole link within 25s "+
			"(last read: %+v). The identical write, done via a plain sequential coreKV.Put "+
			"(TestRefractor_CapabilityLens_LinkFanOut_E2E, this package), projects reliably — "+
			"the defect is specific to the unconditioned member riding inside an atomic batch "+
			"alongside conditioned siblings.", identityID, env)
	} else {
		t.Logf("NOT REPRODUCED IN THIS HARNESS: cap.roles.%s projected the consumer grant from "+
			"the atomic-batch-committed unconditioned holdsRole link. The live gap §13.1 found "+
			"depends on something this harness lacks (timing/load/a second lens or consumer "+
			"interaction, or a difference in the real ClaimIdentity script's exact mutation "+
			"encoding) — instrument the live stack next instead of reading more code.", identityID)
	}
	require.True(t, projected,
		"cap.roles.<target> must project the role-derived grant from an unconditioned holdsRole "+
			"link committed inside the same atomic batch as conditioned .state/.claimKey siblings — "+
			"see the preceding t.Logf for repro details")
}
