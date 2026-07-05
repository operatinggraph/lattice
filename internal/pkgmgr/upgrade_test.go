package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// kvDoc reads a committed Core KV entry as a generic map, failing the test if
// the key is absent.
func kvDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, CoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var m map[string]any
	if err := json.Unmarshal(entry.Value, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return m
}

func TestUpgrade_NotInstalled(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	_, err := inst.Upgrade(ctx, sampleDef("0.2.0"))
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Upgrade on absent package: want ErrNotInstalled, got %v", err)
	}
}

// TestUpgrade_NoChangesSkipped installs v1 then upgrades with the identical
// definition. Every entity body is byte-equal, so the diff is empty and the
// upgrade is a reported no-op — the strongest body-equality-skip assertion.
func TestUpgrade_NoChangesSkipped(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}
	res, err := inst.Upgrade(ctx, sampleDef("0.1.0"))
	if err != nil {
		t.Fatalf("Upgrade (no-op): %v", err)
	}
	if !res.Skipped {
		t.Fatalf("identical re-upgrade: want Skipped, got %+v", res)
	}
	if res.Created != 0 || res.Updated != 0 || res.Tombstoned != 0 {
		t.Fatalf("no-op upgrade produced mutations: %+v", res)
	}
}

// TestUpgrade_VersionBumpOnlyUpdatesPackageEntities bumps only the version,
// leaving every declared entity body identical. Only the package vertex and its
// manifest aspect (which carry the version) should update; no entity aspect is
// touched and nothing is created or tombstoned — proving the body-equality skip
// leaves unchanged entities alone.
func TestUpgrade_VersionBumpOnlyUpdatesPackageEntities(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}
	res, err := inst.Upgrade(ctx, sampleDef("0.2.0"))
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if res.Skipped {
		t.Fatalf("version bump should not be skipped: %+v", res)
	}
	if res.Created != 0 || res.Tombstoned != 0 {
		t.Fatalf("version-only bump: want 0 created / 0 tombstoned, got %+v", res)
	}
	// The package vertex (data.version) + the manifest aspect (data.version)
	// carry the version, so exactly those two update.
	if res.Updated != 2 {
		t.Fatalf("version-only bump: want 2 updates (package vertex + manifest), got %d (%+v)", res.Updated, res)
	}
}

// TestUpgrade_DiffCreateUpdateTombstone exercises all three partitions in one
// upgrade: add a lens (create), change the DDL description (update, with
// createdAt carried forward), drop the permission (tombstone). It asserts the
// resulting Core KV state and that the surviving entity's creation provenance
// is preserved while its lastModified reflects the upgrade actor.
func TestUpgrade_DiffCreateUpdateTombstone(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}

	ddlKey := metaVertexPrefix + entityNanoID(v1.Name, "ddl:sampleClass")
	descKey := ddlKey + ".description"
	newLensKey := metaVertexPrefix + entityNanoID(v1.Name, "lens:sampleLens2")
	permKey := "vtx.permission." + entityNanoID(v1.Name, permTag("SampleOp", "any"))

	// Capture the install-time creation provenance of the entity we will update.
	origDesc := kvDoc(t, ctx, conn, descKey)
	origCreatedAt, _ := origDesc["createdAt"].(string)
	if origCreatedAt == "" {
		t.Fatalf("install did not stamp createdAt on %s", descKey)
	}

	// v2: add a second lens, change the DDL description, drop the permission.
	v2 := sampleDef("0.2.0")
	v2.DDLs[0].Description = "sample upgraded"
	v2.Lenses = append(v2.Lenses, LensSpec{
		CanonicalName: "sampleLens2",
		Class:         "meta.lens",
		Adapter:       "nats-kv",
		Bucket:        "sample-bucket-2",
		Engine:        "full",
		Spec:          `MATCH (n:sample2) RETURN n.key AS key`,
	})
	v2.Permissions = nil

	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if res.Skipped {
		t.Fatalf("upgrade with changes should not be skipped: %+v", res)
	}
	if res.Created == 0 || res.Updated == 0 || res.Tombstoned == 0 {
		t.Fatalf("want non-zero create/update/tombstone, got %+v", res)
	}

	// Create: the new lens vertex landed and is live.
	newLens := kvDoc(t, ctx, conn, newLensKey)
	if del, _ := newLens["isDeleted"].(bool); del {
		t.Fatalf("new lens %s should be live", newLensKey)
	}

	// Update: the DDL description body changed, createdAt preserved, and the
	// upgrade actor is recorded as lastModifiedBy.
	desc := kvDoc(t, ctx, conn, descKey)
	gotText, _ := desc["data"].(map[string]any)["text"].(string)
	if gotText != "sample upgraded" {
		t.Fatalf("description not updated: got %q", gotText)
	}
	if gotCreatedAt, _ := desc["createdAt"].(string); gotCreatedAt != origCreatedAt {
		t.Fatalf("createdAt not preserved across update: was %q now %q", origCreatedAt, gotCreatedAt)
	}
	if lmBy, _ := desc["lastModifiedBy"].(string); lmBy != bootstrap.BootstrapIdentityKey {
		t.Fatalf("lastModifiedBy not the upgrade actor: got %q", lmBy)
	}

	// Tombstone: the dropped permission is soft-deleted.
	perm := kvDoc(t, ctx, conn, permKey)
	if del, _ := perm["isDeleted"].(bool); !del {
		t.Fatalf("dropped permission %s should be tombstoned", permKey)
	}

	// The package vertex carries the new version.
	pkg := kvDoc(t, ctx, conn, PackageVertexPrefix+entityNanoID(v1.Name, "package"))
	if ver, _ := pkg["data"].(map[string]any)["version"].(string); ver != "0.2.0" {
		t.Fatalf("package version not bumped: got %q", ver)
	}
}

// TestUpgrade_DeltaCarriesExpectedRevision proves diffManifest conditions
// every update/tombstone mutation on the revision its own read observed
// (F-011 per-key OCC, Contract #8 §8.6); a create mutation carries none (it
// is already conditioned create-only).
func TestUpgrade_DeltaCarriesExpectedRevision(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}

	descKey := metaVertexPrefix + entityNanoID(v1.Name, "ddl:sampleClass") + ".description"
	permKey := "vtx.permission." + entityNanoID(v1.Name, permTag("SampleOp", "any"))
	newLensKey := metaVertexPrefix + entityNanoID(v1.Name, "lens:sampleLens2")

	v2 := sampleDef("0.2.0")
	v2.DDLs[0].Description = "sample upgraded"
	v2.Lenses = append(v2.Lenses, LensSpec{
		CanonicalName: "sampleLens2",
		Class:         "meta.lens",
		Adapter:       "nats-kv",
		Bucket:        "sample-bucket-2",
		Engine:        "full",
		Spec:          `MATCH (n:sample2) RETURN n.key AS key`,
	})
	v2.Permissions = nil

	existing, err := inst.findInstalledPackage(ctx, v1.Name)
	if err != nil || existing == nil {
		t.Fatalf("findInstalledPackage: existing=%+v err=%v", existing, err)
	}
	mutations, sum, err := inst.computeDeltaAgainst(ctx, existing, v2)
	if err != nil {
		t.Fatalf("computeDeltaAgainst: %v", err)
	}
	if sum.created == 0 || sum.updated == 0 || sum.tombstoned == 0 {
		t.Fatalf("want non-zero create/update/tombstone, got %+v", sum)
	}
	byKey := make(map[string]installMutation, len(mutations))
	for _, m := range mutations {
		byKey[m.Key] = m
	}

	descEntry, err := conn.KVGet(ctx, CoreBucket, descKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", descKey, err)
	}
	descMut, ok := byKey[descKey]
	if !ok || descMut.Op != "update" {
		t.Fatalf("expected an update mutation for %s, got %+v", descKey, descMut)
	}
	if descMut.ExpectedRevision == nil || *descMut.ExpectedRevision != descEntry.Revision {
		t.Fatalf("update ExpectedRevision = %v, want %d", descMut.ExpectedRevision, descEntry.Revision)
	}

	permEntry, err := conn.KVGet(ctx, CoreBucket, permKey)
	if err != nil {
		t.Fatalf("KVGet %s: %v", permKey, err)
	}
	permMut, ok := byKey[permKey]
	if !ok || permMut.Op != "tombstone" {
		t.Fatalf("expected a tombstone mutation for %s, got %+v", permKey, permMut)
	}
	if permMut.ExpectedRevision == nil || *permMut.ExpectedRevision != permEntry.Revision {
		t.Fatalf("tombstone ExpectedRevision = %v, want %d", permMut.ExpectedRevision, permEntry.Revision)
	}

	createMut, ok := byKey[newLensKey]
	if !ok || createMut.Op != "create" {
		t.Fatalf("expected a create mutation for %s, got %+v", newLensKey, createMut)
	}
	if createMut.ExpectedRevision != nil {
		t.Fatalf("create mutation should carry no ExpectedRevision, got %d", *createMut.ExpectedRevision)
	}
}

// TestUpgrade_RaceOnUpdatedKeyRejected proves the F-011 per-key OCC fix on the
// update path (Contract #8 §8.6): a concurrent write to a surviving key
// between diffManifest's read and the upgrade's commit is rejected
// (RevisionConflict), not silently overwritten, and the whole atomic batch
// leaves the key un-updated — no partial upgrade. Mirrors
// TestInstaller_Uninstall_RaceOnDeclaredKeyRejected's interleave
// reconstruction: capture the revision the diff would see, have a concurrent
// write bump it, then submit the exact mutation shape diffManifest builds,
// keyed on the now-stale revision.
func TestUpgrade_RaceOnUpdatedKeyRejected(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	v1 := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	descKey := metaVertexPrefix + entityNanoID(v1.Name, "ddl:sampleClass") + ".description"

	entry, err := conn.KVGet(ctx, CoreBucket, descKey)
	if err != nil {
		t.Fatalf("capture revision: %v", err)
	}
	staleRev := entry.Revision

	if _, err := conn.KVUpdate(ctx, CoreBucket, descKey, entry.Value, staleRev); err != nil {
		t.Fatalf("simulated concurrent write: %v", err)
	}

	requestID := deterministicNanoID(v1.Name, "0.1.0->0.2.0", "race-update-op")
	payload := map[string]any{
		"name":        v1.Name,
		"fromVersion": "0.1.0",
		"toVersion":   "0.2.0",
		"mutations": []map[string]any{
			{"op": "update", "key": descKey,
				"document":         map[string]any{"isDeleted": false, "data": map[string]any{"text": "sample upgraded"}},
				"expectedRevision": staleRev},
		},
	}
	reply, err := inst.submitOp(ctx, "UpgradePackage", "UpgradePackage", requestID, payload)
	if err != nil {
		t.Fatalf("submitOp: %v", err)
	}
	if reply.Status != processor.ReplyStatusRejected {
		t.Fatalf("status = %q, want rejected", reply.Status)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeRevisionConflict {
		t.Fatalf("error = %+v, want code RevisionConflict", reply.Error)
	}

	after, err := conn.KVGet(ctx, CoreBucket, descKey)
	if err != nil {
		t.Fatalf("post-conflict read: %v", err)
	}
	if strings.Contains(string(after.Value), "sample upgraded") {
		t.Fatalf("key %s was updated despite the OCC rejection", descKey)
	}
}

// TestUpgrade_RaceOnTombstonedKeyRejected mirrors the above for the tombstone
// (removed-key) side of the diff — the whole batch is rejected and the key is
// left live, not partially tombstoned.
func TestUpgrade_RaceOnTombstonedKeyRejected(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	v1 := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	permKey := "vtx.permission." + entityNanoID(v1.Name, permTag("SampleOp", "any"))

	entry, err := conn.KVGet(ctx, CoreBucket, permKey)
	if err != nil {
		t.Fatalf("capture revision: %v", err)
	}
	staleRev := entry.Revision

	if _, err := conn.KVUpdate(ctx, CoreBucket, permKey, entry.Value, staleRev); err != nil {
		t.Fatalf("simulated concurrent write: %v", err)
	}

	requestID := deterministicNanoID(v1.Name, "0.1.0->0.2.0", "race-tombstone-op")
	payload := map[string]any{
		"name":        v1.Name,
		"fromVersion": "0.1.0",
		"toVersion":   "0.2.0",
		"mutations": []map[string]any{
			{"op": "tombstone", "key": permKey,
				"document":         map[string]any{"isDeleted": true, "data": map[string]any{}},
				"expectedRevision": staleRev},
		},
	}
	reply, err := inst.submitOp(ctx, "UpgradePackage", "UpgradePackage", requestID, payload)
	if err != nil {
		t.Fatalf("submitOp: %v", err)
	}
	if reply.Status != processor.ReplyStatusRejected {
		t.Fatalf("status = %q, want rejected", reply.Status)
	}
	if reply.Error == nil || reply.Error.Code != processor.ErrCodeRevisionConflict {
		t.Fatalf("error = %+v, want code RevisionConflict", reply.Error)
	}

	after, err := conn.KVGet(ctx, CoreBucket, permKey)
	if err != nil {
		t.Fatalf("post-conflict read: %v", err)
	}
	if strings.Contains(string(after.Value), `"isDeleted":true`) {
		t.Fatalf("key %s was tombstoned despite the OCC rejection", permKey)
	}
}

// TestUpgrade_ProtectedRootRejected is the adversarial end-to-end check: a
// client-submittable UpgradePackage op whose mutation targets a protected
// kernel/auth root is rejected at the Processor's authoritative step-8 guard,
// not the script. UpgradePackage is not create-only, so this guard is the
// load-bearing safety property.
func TestUpgrade_ProtectedRootRejected(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	for _, tc := range []struct {
		name string
		op   string
	}{
		{"tombstone-protected-role", "tombstone"},
		{"update-protected-role", "update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"name":        "adversarial",
				"fromVersion": "0.1.0",
				"toVersion":   "0.2.0",
				"mutations": []map[string]any{
					{
						"op":  tc.op,
						"key": bootstrap.RoleOperatorKey,
						"document": map[string]any{
							"class":     "role",
							"isDeleted": tc.op == "tombstone",
							"data":      map[string]any{},
						},
					},
				},
			}
			reqID := deterministicNanoID("adversarial-"+tc.op, "0.1.0->0.2.0", "upgrade-op")
			reply, err := inst.submitOp(ctx, "UpgradePackage", "UpgradePackage", reqID, payload)
			if err != nil {
				t.Fatalf("submitOp: %v", err)
			}
			if reply.Status != processor.ReplyStatusRejected {
				t.Fatalf("protected-root %s: want rejected, got %s", tc.op, reply.Status)
			}
			if reply.Error == nil || reply.Error.Code != processor.ErrCodeProtectedKey {
				t.Fatalf("protected-root %s: want ProtectedKey error, got %+v", tc.op, reply.Error)
			}
		})
	}
}

func TestUpgrade_RequestIDDeterminism(t *testing.T) {
	a := deterministicNanoID("pkg", "0.1.0->0.2.0", "upgrade-op")
	b := deterministicNanoID("pkg", "0.1.0->0.2.0", "upgrade-op")
	if a != b {
		t.Fatalf("upgrade requestId not deterministic: %q != %q", a, b)
	}
	// Distinct (from,to) pairs must be independent so each upgrade dedups on
	// its own tracker.
	if c := deterministicNanoID("pkg", "0.1.0->0.3.0", "upgrade-op"); a == c {
		t.Fatalf("distinct (from,to) pairs collided: %q", a)
	}
	// The upgrade-op tag is independent of the install-op tag at the same
	// version string, so an install and a same-string upgrade never collide.
	if d := deterministicNanoID("pkg", "0.1.0->0.2.0", "install-op"); a == d {
		t.Fatalf("upgrade-op and install-op tags collided: %q", a)
	}
}

func TestLogicalDocEqual(t *testing.T) {
	// A committed entry carries provenance the rebuilt doc lacks; equality is
	// judged only over the fields the rebuilt doc declares.
	newDoc := map[string]any{
		"class":     "permission",
		"isDeleted": false,
		"data":      map[string]any{"operationType": "Op", "scope": "any"},
	}
	committedSame := map[string]any{
		"class":          "permission",
		"isDeleted":      false,
		"data":           map[string]any{"scope": "any", "operationType": "Op"}, // key order differs
		"createdAt":      "2026-01-01T00:00:00Z",
		"createdBy":      "vtx.identity.x",
		"key":            "vtx.permission.y",
		"lastModifiedAt": "2026-01-02T00:00:00Z",
	}
	if !logicalDocEqual(newDoc, committedSame) {
		t.Fatalf("logically-equal docs reported as differing")
	}
	committedChanged := map[string]any{
		"class":     "permission",
		"isDeleted": false,
		"data":      map[string]any{"operationType": "Op", "scope": "self"}, // scope differs
		"createdAt": "2026-01-01T00:00:00Z",
	}
	if logicalDocEqual(newDoc, committedChanged) {
		t.Fatalf("changed data not detected")
	}
	committedMissing := map[string]any{
		"class":     "permission",
		"isDeleted": false,
		// data absent
	}
	if logicalDocEqual(newDoc, committedMissing) {
		t.Fatalf("missing field not detected")
	}
}

func TestJSONEqual(t *testing.T) {
	// Map key order independence.
	if !jsonEqual(map[string]any{"a": 1, "b": 2}, map[string]any{"b": 2, "a": 1}) {
		t.Fatalf("key-order should not matter")
	}
	// int vs float64 (the JSON round-trip representation).
	if !jsonEqual(5, float64(5)) {
		t.Fatalf("int and float64 5 should encode equally")
	}
	if jsonEqual([]any{"x"}, []any{"y"}) {
		t.Fatalf("different slices should differ")
	}
}
