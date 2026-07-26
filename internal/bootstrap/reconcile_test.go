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
