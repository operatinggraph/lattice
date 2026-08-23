package bootstrap

// Tests for ReconcilePrimordial — the pass that brings an already-seeded Core
// KV's kernel into agreement with the kernel this binary builds. They live
// in-package because buildPrimordialEntries and kvEntry are unexported.
//
// The load-bearing one is TestReconcilePrimordial_IsAFixpoint: the whole
// mechanism is safe to run on every boot only because a converged bucket
// writes nothing, which holds only while the built envelopes carry the fixed
// BootstrapTime instead of wall-clock provenance.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// newReconcileSeeder is newPerKeySeeder plus AllowAtomicPublish on Core KV,
// which ProvisionBuckets sets on every real deployment and verify-kernel
// asserts. Reconcile commits as one atomic batch — the same guarantee the seed
// it repairs relies on — so the fixture has to match production rather than the
// reconcile weakening itself to fit a bare test bucket.
func newReconcileSeeder(ctx context.Context, t *testing.T) (*Seeder, jetstream.KeyValue) {
	t.Helper()
	seeder, kv := newPerKeySeeder(ctx, t)
	require.NoError(t, seeder.enableAtomicPublish(ctx, CoreKVBucket))
	return seeder, kv
}

// upgradeScriptKey is the aspect the reported failure lived in: the stored
// UpgradePackage script that lacked the tombstone branch.
func upgradeScriptKey() string { return substrate.AspectKey(UpgradePackageDDLKey, "script") }

// storedScriptSource reads the `source` field out of a stored script aspect.
func storedScriptSource(t *testing.T, raw []byte) string {
	t.Helper()
	var env struct {
		Data struct {
			Source string `json:"source"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &env))
	return env.Data.Source
}

// TestReconcilePrimordial_CreatesMissingKernelKeys proves the create branch:
// against a bucket holding none of the kernel, reconcile writes every entry it
// builds. The bootstrap op tracker is deliberately left out — it is the
// seeding-state sentinel, not kernel content.
func TestReconcilePrimordial_CreatesMissingKernelKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	entries, err := buildPrimordialEntries()
	require.NoError(t, err)

	res, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)
	require.Equal(t, len(entries)-1, res.Created, "every kernel entry but the op tracker must be created")
	require.Equal(t, 0, res.Updated)
	require.Equal(t, 0, res.Unchanged)

	_, err = kv.Get(ctx, upgradeScriptKey())
	require.NoError(t, err, "a kernel DDL script must exist after reconcile")

	_, err = kv.Get(ctx, BootstrapOpKey)
	require.Error(t, err, "reconcile must not write the seeding sentinel")
}

// TestReconcilePrimordial_IsAFixpoint is the assertion the mechanism rests on:
// running reconcile against a converged bucket writes nothing. Boot may
// therefore reconcile unconditionally without churning Core KV or advancing
// revisions, which is what keeps re-run safety (Contract #7 §7.4) intact.
//
// This holds only while every built envelope is stamped with the fixed
// BootstrapTime. If a reconciled entry ever carried wall-clock provenance,
// the second pass below would rewrite the entire kernel — and would do so on
// every boot, forever.
func TestReconcilePrimordial_IsAFixpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	first, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)
	require.True(t, first.Changed(), "the first pass against an empty bucket must write")

	before, err := kv.Get(ctx, upgradeScriptKey())
	require.NoError(t, err)

	second, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)
	require.False(t, second.Changed(), "a converged kernel must produce no writes")
	require.Equal(t, 0, second.Created)
	require.Equal(t, 0, second.Updated)
	require.Equal(t, 0, second.Retained)

	after, err := kv.Get(ctx, upgradeScriptKey())
	require.NoError(t, err)
	require.Equal(t, before.Revision(), after.Revision(),
		"a converged key must not have its revision advanced")
}

// TestReconcilePrimordial_ReplacesStaleKernelScript is the regression for the
// reported failure. A bucket seeded by an older binary holds an UpgradePackage
// script without the tombstone branch, so a package upgrade that removes a
// declared key is rejected with "mutation requires a document dict". Reconcile
// must replace that stored script with this binary's.
func TestReconcilePrimordial_ReplacesStaleKernelScript(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	_, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	// Stand in for the older binary's script: the current one with the
	// tombstone branch cut out, which is exactly what the stuck stack holds.
	stale := strings.Replace(UpgradePackageDDLScript,
		`        if mop == "tombstone":
            out_mut = {"op": mop, "key": key}
        else:
`, "", 1)
	require.NotEqual(t, UpgradePackageDDLScript, stale, "the stale fixture must actually differ")

	key := upgradeScriptKey()
	staleVal, err := MakeAspectEnvelope(key, UpgradePackageDDLKey, "script", "script",
		map[string]any{"source": stale})
	require.NoError(t, err)
	_, err = kv.Put(ctx, key, staleVal)
	require.NoError(t, err)

	res, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Updated, "exactly the stale script must be rewritten")
	require.Equal(t, 0, res.Created)

	stored, err := kv.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, UpgradePackageDDLScript, storedScriptSource(t, stored.Value()),
		"the stored script must become this binary's definition")
	require.Contains(t, storedScriptSource(t, stored.Value()), `if mop == "tombstone":`,
		"the tombstone branch is what a key-removing package upgrade needs")
}

// TestReconcilePrimordial_RestoresDeletedKernelKey proves a kernel key deleted
// from a live bucket comes back, rather than the bucket staying short a DDL
// aspect until someone wipes it.
func TestReconcilePrimordial_RestoresDeletedKernelKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	_, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	key := upgradeScriptKey()
	require.NoError(t, kv.Purge(ctx, key))

	res, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Created)
	require.Equal(t, 0, res.Updated)

	stored, err := kv.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, UpgradePackageDDLScript, storedScriptSource(t, stored.Value()))
}

// TestReconcilePrimordial_LeavesOpTrackerAlone proves the sentinel exclusion.
// PrimordialSeeded and DecideReseed read the op tracker to decide whether a
// bucket has been seeded and whether to reopen the two-phase commit window; a
// reconcile that rewrote it would be editing the seeding state machine's own
// input.
func TestReconcilePrimordial_LeavesOpTrackerAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	divergent, err := MakeVertexEnvelope(BootstrapOpKey, "op.bootstrap",
		map[string]any{"status": "committed", "note": "written by some other run"})
	require.NoError(t, err)
	rev, err := kv.Create(ctx, BootstrapOpKey, divergent)
	require.NoError(t, err)

	_, err = seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	stored, err := kv.Get(ctx, BootstrapOpKey)
	require.NoError(t, err)
	require.Equal(t, rev, stored.Revision(), "the sentinel must not be rewritten")
	require.Equal(t, divergent, stored.Value())
}

// TestReconcilePrimordial_DoesNotResurrectARevokedGrant is the security
// property. The primordial links sit outside the Processor's protected-key
// guard on purpose — protectedRootKey returns "" for anything not
// vertex-rooted — and rbac-domain's RevokeRole tombstones exactly these key
// shapes. A tombstone is soft: the key stays, carrying isDeleted.
//
// So a revoked grant looks exactly like a stale entry to a content comparison,
// and rewriting it would restore root-equivalence to a decommissioned service
// actor with nothing but an Info log to show for it. Boot must never do that.
func TestReconcilePrimordial_DoesNotResurrectARevokedGrant(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	_, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	// What RevokeRole leaves behind: the same document, isDeleted set.
	key := LoomHoldsRoleLinkKey
	live, err := kv.Get(ctx, key)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(live.Value(), &doc))
	require.Equal(t, false, doc["isDeleted"], "fixture must start from a live grant")
	doc["isDeleted"] = true
	revoked, err := json.Marshal(doc)
	require.NoError(t, err)
	_, err = kv.Put(ctx, key, revoked)
	require.NoError(t, err)

	res, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, res.Updated, "a revoked grant must not be rewritten")
	require.Equal(t, 0, res.Created)
	require.Equal(t, 1, res.Retained, "it must be reported as deliberately retained")

	stored, err := kv.Get(ctx, key)
	require.NoError(t, err)
	var after map[string]any
	require.NoError(t, json.Unmarshal(stored.Value(), &after))
	require.Equal(t, true, after["isDeleted"], "the revocation must survive a boot")
}

// TestReconcilePrimordial_LeavesTopologyToOperations proves the scope narrowing
// beyond the tombstone case: a live (not deleted) identity/role/permission/link
// body that differs from the built one is still not reverted. Their lifecycle
// belongs to operations — rbac-domain grants and revokes them — so the seeder
// reports divergence rather than overwriting whatever an operation last wrote.
func TestReconcilePrimordial_LeavesTopologyToOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	_, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	key := RoleOperatorKey
	drifted, err := MakeVertexEnvelope(key, "role",
		map[string]any{"protected": true, "note": "an operation wrote this"})
	require.NoError(t, err)
	_, err = kv.Put(ctx, key, drifted)
	require.NoError(t, err)

	res, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, res.Updated, "topology must not be reverted by a boot")
	require.Equal(t, 1, res.Retained)

	stored, err := kv.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, drifted, stored.Value(), "the operation's write must survive")
}

// TestReconcilePrimordial_DoesNotReviveADeletedDDL applies the tombstone rule
// to the one class reconcile DOES rewrite: even a kernel definition, if
// deliberately deleted, stays deleted.
func TestReconcilePrimordial_DoesNotReviveADeletedDDL(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	_, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	key := upgradeScriptKey()
	live, err := kv.Get(ctx, key)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(live.Value(), &doc))
	doc["isDeleted"] = true
	tombstoned, err := json.Marshal(doc)
	require.NoError(t, err)
	_, err = kv.Put(ctx, key, tombstoned)
	require.NoError(t, err)

	res, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, res.Updated, "a deleted definition must not be revived")
	require.Equal(t, 1, res.Retained)
}

// TestReconcilePrimordial_DoesNotWriteAnOrphan is Inc 1's whole write-side
// claim under test: a bucket holding a retired kernel entity is a candidate
// KernelOrphans reports, but ReconcilePrimordial — report-only — must leave
// it untouched: no create, no update, and its revision must not move. This
// is also the only test that actually runs the Warn branch over
// plan.orphanedEntities/plan.orphanedAspects; deleting either loop must not
// turn this test red on its own (it asserts on ReconcileResult and the
// stored revision, not on log output), but it does prove the loop executes
// against a real orphan without panicking or writing.
func TestReconcilePrimordial_DoesNotWriteAnOrphan(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	_, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	orphan := substrate.VertexKey("meta", id)
	env, err := MakeVertexEnvelope(orphan, "meta.ddl", nil)
	require.NoError(t, err)
	rev, err := kv.Put(ctx, orphan, env)
	require.NoError(t, err)

	entities, _, err := KernelOrphans(ctx, kv)
	require.NoError(t, err)
	require.Contains(t, entities, orphan, "fixture sanity: this must actually be a reported candidate")

	res, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, res.Created)
	require.Equal(t, 0, res.Updated)
	require.False(t, res.Changed(), "an orphan report must never become a write")

	stored, err := kv.Get(ctx, orphan)
	require.NoError(t, err)
	require.Equal(t, rev, stored.Revision(), "the orphan's revision must not move")
}

// TestKernelDrift_SeesWhatPresenceChecksCannot is the proof for the
// verify-kernel freshness assertion. A stale kernel key is present, carries a
// well-formed envelope and correct provenance, and differs only in its body —
// so every presence-and-shape check passes over it. Drift must still report it,
// and must report nothing once the bucket is converged.
func TestKernelDrift_SeesWhatPresenceChecksCannot(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	_, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	missing, stale, err := KernelDrift(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, missing, "a converged bucket has nothing missing")
	require.Empty(t, stale, "a converged bucket has nothing stale")

	key := upgradeScriptKey()
	staleVal, err := MakeAspectEnvelope(key, UpgradePackageDDLKey, "script", "script",
		map[string]any{"source": "def execute(state, op):\n    fail(\"an older binary\")\n"})
	require.NoError(t, err)
	_, err = kv.Put(ctx, key, staleVal)
	require.NoError(t, err)

	missing, stale, err = KernelDrift(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, missing)
	require.Equal(t, []string{key}, stale, "a differing body must be reported as drift")

	require.NoError(t, kv.Purge(ctx, key))
	missing, stale, err = KernelDrift(ctx, kv)
	require.NoError(t, err)
	require.Equal(t, []string{key}, missing, "an absent kernel entry must be reported as missing")
	require.Empty(t, stale)
}

// putRaw marshals a raw envelope value (built with substrate.NewDocumentEnvelopeAt
// or substrate.AspectEnvelope directly, bypassing MakeVertexEnvelope/
// MakeAspectEnvelope's bootstrap provenance) and puts it at key.
func putRaw(ctx context.Context, t *testing.T, kv jetstream.KeyValue, key string, env any) {
	t.Helper()
	raw, err := json.Marshal(env)
	require.NoError(t, err)
	_, err = kv.Put(ctx, key, raw)
	require.NoError(t, err)
}

// TestKernelOrphans_DiscriminatorTable proves each row of the §3.1
// discriminator (fire brief §13 part 5's state table) in isolation:
// candidate shape × build-membership × provenance × tombstone state,
// checked against the classification KernelOrphans and ReconcilePrimordial's
// orphan scan share via planReconcile.
func TestKernelOrphans_DiscriminatorTable(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	t.Run("row1_unbuilt_vertex_bootstrap_provenance_reports_entity", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, kv := newReconcileSeeder(ctx, t)

		id, err := substrate.NewNanoID()
		require.NoError(t, err)
		root := substrate.VertexKey("meta", id)
		env, err := MakeVertexEnvelope(root, "meta.ddl", nil)
		require.NoError(t, err)
		_, err = kv.Put(ctx, root, env)
		require.NoError(t, err)

		entities, aspects, err := KernelOrphans(ctx, kv)
		require.NoError(t, err)
		require.Equal(t, []string{root}, entities)
		require.Empty(t, aspects)
	})

	t.Run("row2_foreign_createdByOp_keeps_silent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, kv := newReconcileSeeder(ctx, t)

		id, err := substrate.NewNanoID()
		require.NoError(t, err)
		root := substrate.VertexKey("meta", id)
		foreignOpID, err := substrate.NewNanoID()
		require.NoError(t, err)
		foreignOp := substrate.VertexKey("op", foreignOpID)
		env := substrate.NewDocumentEnvelopeAt("meta.ddl", BootstrapIdentityKey, foreignOp, BootstrapTime)
		env.Key = root
		putRaw(ctx, t, kv, root, env)

		entities, aspects, err := KernelOrphans(ctx, kv)
		require.NoError(t, err)
		require.Empty(t, entities, "a foreign op's meta-vertex must never be reported")
		require.Empty(t, aspects)
	})

	t.Run("row2_unparseable_envelope_keeps_silent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, kv := newReconcileSeeder(ctx, t)

		id, err := substrate.NewNanoID()
		require.NoError(t, err)
		root := substrate.VertexKey("meta", id)
		// createdByOp decodes correctly to BootstrapOpKey; isDeleted carries
		// the wrong JSON type. encoding/json still populates every
		// successfully-typed field before returning the type-mismatch error,
		// so this body reaches kernelCandidatePasses with a matching
		// createdByOp AND isDeleted at its bool zero value (false) — the
		// json.Unmarshal error check is the ONLY thing standing between this
		// candidate and a (wrong) report. A syntactically-broken body like
		// "not json" cannot prove that: encoding/json validates the whole
		// document before touching the destination at all, so it never
		// populates createdByOp either, and the very next check
		// (createdByOp != BootstrapOpKey) would keep the candidate silent
		// even with the unmarshal-error check deleted.
		body := fmt.Sprintf(`{"createdByOp":%q,"isDeleted":"not-a-bool"}`, BootstrapOpKey)
		_, err = kv.Put(ctx, root, []byte(body))
		require.NoError(t, err)

		var probe struct {
			CreatedByOp string `json:"createdByOp"`
			IsDeleted   bool   `json:"isDeleted"`
		}
		probeErr := json.Unmarshal([]byte(body), &probe)
		require.Error(t, probeErr, "fixture sanity: the body as a whole must fail to unmarshal")
		require.Equal(t, BootstrapOpKey, probe.CreatedByOp,
			"fixture sanity: createdByOp must still decode correctly despite the overall error")

		entities, aspects, err := KernelOrphans(ctx, kv)
		require.NoError(t, err)
		require.Empty(t, entities, "an envelope that fails to unmarshal must keep silent even though createdByOp itself would have matched")
		require.Empty(t, aspects)
	})

	t.Run("row2_tombstoned_bootstrap_provenance_keeps_silent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, kv := newReconcileSeeder(ctx, t)

		id, err := substrate.NewNanoID()
		require.NoError(t, err)
		root := substrate.VertexKey("meta", id)
		env := substrate.NewDocumentEnvelopeAt("meta.ddl", BootstrapIdentityKey, BootstrapOpKey, BootstrapTime)
		env.Key = root
		env.IsDeleted = true
		putRaw(ctx, t, kv, root, env)

		entities, aspects, err := KernelOrphans(ctx, kv)
		require.NoError(t, err)
		require.Empty(t, entities, "an already-tombstoned candidate must not be re-reported")
		require.Empty(t, aspects)
	})

	t.Run("row3_aspect_of_live_built_entity_reports_aspect", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		seeder, kv := newReconcileSeeder(ctx, t)

		// The bucket must actually HOLD MetaRootKey as a live root. Without
		// seeding, MetaRootKey is absent from the bucket (only from `built`),
		// so deleting scanKernelOrphans' built[root] branch would fall
		// through to the parentless default (row 5) and still report this
		// aspect — the test would pass for the wrong reason. Seeding first
		// makes presentRoots[root] true too, so only the built[root] branch
		// can route this candidate to row 3.
		_, err := seeder.ReconcilePrimordial(ctx)
		require.NoError(t, err)

		key := substrate.AspectKey(MetaRootKey, "orphanDiscriminatorTestAspect")
		env, err := MakeAspectEnvelope(key, MetaRootKey, "orphanDiscriminatorTestAspect", "description", nil)
		require.NoError(t, err)
		_, err = kv.Put(ctx, key, env)
		require.NoError(t, err)

		entities, aspects, err := KernelOrphans(ctx, kv)
		require.NoError(t, err)
		require.Empty(t, entities)
		require.Equal(t, []string{key}, aspects, "an orphaned aspect of a still-built root is reported")
	})

	t.Run("row4_aspect_of_unbuilt_root_present_defers_to_the_entity_row", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, kv := newReconcileSeeder(ctx, t)

		id, err := substrate.NewNanoID()
		require.NoError(t, err)
		root := substrate.VertexKey("meta", id)
		rootEnv, err := MakeVertexEnvelope(root, "meta.ddl", nil)
		require.NoError(t, err)
		_, err = kv.Put(ctx, root, rootEnv)
		require.NoError(t, err)

		aspectKey := substrate.AspectKey(root, "script")
		aspectEnv, err := MakeAspectEnvelope(aspectKey, root, "script", "script", nil)
		require.NoError(t, err)
		_, err = kv.Put(ctx, aspectKey, aspectEnv)
		require.NoError(t, err)

		entities, aspects, err := KernelOrphans(ctx, kv)
		require.NoError(t, err)
		require.Equal(t, []string{root}, entities, "the unbuilt root is reported as the entity")
		require.Empty(t, aspects, "its aspect must not ALSO be reported separately")
	})

	t.Run("row5_parentless_aspect_reports_aspect", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, kv := newReconcileSeeder(ctx, t)

		id, err := substrate.NewNanoID()
		require.NoError(t, err)
		root := substrate.VertexKey("meta", id)
		aspectKey := substrate.AspectKey(root, "script")
		aspectEnv, err := MakeAspectEnvelope(aspectKey, root, "script", "script", nil)
		require.NoError(t, err)
		_, err = kv.Put(ctx, aspectKey, aspectEnv)
		require.NoError(t, err)

		entities, aspects, err := KernelOrphans(ctx, kv)
		require.NoError(t, err)
		require.Empty(t, entities)
		require.Equal(t, []string{aspectKey}, aspects, "a parentless aspect has no entity row to adjudicate it")
	})

	t.Run("row6_aspect_with_failing_filters_keeps_silent", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, kv := newReconcileSeeder(ctx, t)

		id, err := substrate.NewNanoID()
		require.NoError(t, err)
		root := substrate.VertexKey("meta", id)
		aspectKey := substrate.AspectKey(root, "script")
		foreignOpID, err := substrate.NewNanoID()
		require.NoError(t, err)
		foreignOp := substrate.VertexKey("op", foreignOpID)
		base := substrate.NewDocumentEnvelopeAt("script", BootstrapIdentityKey, foreignOp, BootstrapTime)
		base.Key = aspectKey
		env := substrate.AspectEnvelope{DocumentEnvelope: base, VertexKey: root, LocalName: "script"}
		putRaw(ctx, t, kv, aspectKey, env)

		entities, aspects, err := KernelOrphans(ctx, kv)
		require.NoError(t, err)
		require.Empty(t, entities)
		require.Empty(t, aspects, "an unbuilt parentless aspect that fails its own filters keeps silent")
	})
}

// TestKernelOrphans_IgnoresPackageInstalledMeta is the load-bearing negative
// (§4): a realistic package-installed DDL — a meta-vertex root plus
// canonicalName and script aspects, all carrying the INSTALLING op's
// provenance rather than bootstrap's — must never be reported as a kernel
// orphan, even though it is the only unbuilt vtx.meta.* content the bucket
// holds. createdByOp is what separates "this binary no longer builds it"
// from "some other writer owns it"; getting this backwards would report —
// and, once retirement ships, delete — every capability package installed
// on the platform.
func TestKernelOrphans_IgnoresPackageInstalledMeta(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, kv := newReconcileSeeder(ctx, t)

	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	root := substrate.VertexKey("meta", id)
	installOpID, err := substrate.NewNanoID()
	require.NoError(t, err)
	installOp := substrate.VertexKey("op", installOpID)

	rootEnv := substrate.NewDocumentEnvelopeAt("meta.ddl", BootstrapIdentityKey, installOp, BootstrapTime)
	rootEnv.Key = root
	putRaw(ctx, t, kv, root, rootEnv)

	for _, localName := range []string{"canonicalName", "script"} {
		aspectKey := substrate.AspectKey(root, localName)
		base := substrate.NewDocumentEnvelopeAt(localName, BootstrapIdentityKey, installOp, BootstrapTime)
		base.Key = aspectKey
		env := substrate.AspectEnvelope{DocumentEnvelope: base, VertexKey: root, LocalName: localName}
		putRaw(ctx, t, kv, aspectKey, env)
	}

	entities, aspects, err := KernelOrphans(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, entities, "a package's own meta-vertex must never be reported")
	require.Empty(t, aspects, "nor its aspects")
}

// TestKernelOrphans_LiveLensAspectShrinkIsReportedNeverWritten
// regression-guards §3.4's reshape: a kernel lens migrating adapters
// (nats-kv -> postgres) keeps its root and ID but drops an aspect from the
// built set. That must surface as an orphaned ASPECT of a still-live
// entity for an operator to see — never as an entity, and never as a
// write, since steps() only ever carries missing/stale.
func TestKernelOrphans_LiveLensAspectShrinkIsReportedNeverWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	_, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	// Stand-in for the aspect a NATS-KV-adapter lens carries that a postgres
	// one drops: a live kernel lens root (CapabilityLensKey), plus an aspect
	// this binary's built set no longer includes.
	key := substrate.AspectKey(CapabilityLensKey, "targetBucketShrinkTest")
	env, err := MakeAspectEnvelope(key, CapabilityLensKey, "targetBucketShrinkTest", "targetBucket",
		map[string]any{"bucket": "capability"})
	require.NoError(t, err)
	_, err = kv.Put(ctx, key, env)
	require.NoError(t, err)

	entities, aspects, err := KernelOrphans(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, entities, "a live entity's shrunk aspect must never be reported as an entity")
	require.Equal(t, []string{key}, aspects)

	plan, err := planReconcile(ctx, kv)
	require.NoError(t, err)
	require.Empty(t, plan.steps(), "an orphaned aspect must never reach steps() — reported, never written")
}

// TestKernelOrphans_ReflectsPlanReconcileClassification pins concrete
// expected values across BOTH orphan classes from a single KernelOrphans
// call, over a bucket seeded with one of each kind at once. Comparing
// KernelOrphans' result against a SECOND, separately-computed planReconcile
// call proves only that the function is deterministic against unchanged
// state — it would still pass if KernelOrphans quietly reimplemented the
// classification instead of reading planReconcile's own fields. Pinning real
// keys is what actually exercises "the read-only twin sharing planReconcile's
// comparison."
func TestKernelOrphans_ReflectsPlanReconcileClassification(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	_, err := seeder.ReconcilePrimordial(ctx)
	require.NoError(t, err)

	entityID, err := substrate.NewNanoID()
	require.NoError(t, err)
	entityKey := substrate.VertexKey("meta", entityID)
	entityEnv, err := MakeVertexEnvelope(entityKey, "meta.ddl", nil)
	require.NoError(t, err)
	_, err = kv.Put(ctx, entityKey, entityEnv)
	require.NoError(t, err)

	aspectKey := substrate.AspectKey(CapabilityLensKey, "reflectsClassificationTestAspect")
	aspectEnv, err := MakeAspectEnvelope(aspectKey, CapabilityLensKey, "reflectsClassificationTestAspect", "description", nil)
	require.NoError(t, err)
	_, err = kv.Put(ctx, aspectKey, aspectEnv)
	require.NoError(t, err)

	entities, aspects, err := KernelOrphans(ctx, kv)
	require.NoError(t, err)
	require.Equal(t, []string{entityKey}, entities)
	require.Equal(t, []string{aspectKey}, aspects)
}

// TestSeedPrimordial_SeededBucketReconciles proves the wiring: SeedPrimordial
// against a bucket that already holds the sentinel no longer returns early. It
// is the boot path, so a stale kernel is repaired by an ordinary `make up`
// rather than only by a wipe.
func TestSeedPrimordial_SeededBucketReconciles(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seeder, kv := newReconcileSeeder(ctx, t)

	tracker, err := MakeBootstrapOpEnvelope()
	require.NoError(t, err)
	_, err = kv.Create(ctx, BootstrapOpKey, tracker)
	require.NoError(t, err)

	seeded, err := seeder.PrimordialSeeded(ctx)
	require.NoError(t, err)
	require.True(t, seeded, "the fixture must look already-seeded to the guard")

	require.NoError(t, seeder.SeedPrimordial(ctx))

	stored, err := kv.Get(ctx, upgradeScriptKey())
	require.NoError(t, err, "a seeded-but-stale bucket must be reconciled, not skipped")
	require.Equal(t, UpgradePackageDDLScript, storedScriptSource(t, stored.Value()))
}
