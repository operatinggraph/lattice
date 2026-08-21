package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
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

// TestUpgrade_ReAddsRemovedEntity proves a package can get an entity back after
// dropping it. Entity keys are deterministic in (package, kind, canonicalName),
// so the re-add lands on the exact key the removal tombstoned — and a create
// asserts revision 0, which that key's history defeats. Before the revive
// branch this failed with a RevisionConflict, permanently: re-running produced
// the identical batch, so the package could never regain the entity. Found live
// on 2026-07-19 re-adding service-location's staffReadGrants lens, which a
// previous fire had installed and removed.
func TestUpgrade_ReAddsRemovedEntity(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	permKey := "vtx.permission." + entityNanoID(v1.Name, permTag("SampleOp", "any"))

	// v2 drops the permission — tombstoning its key.
	v2 := sampleDef("0.2.0")
	v2.Permissions = nil
	if _, err := inst.Upgrade(ctx, v2); err != nil {
		t.Fatalf("Upgrade (drop): %v", err)
	}
	if del, _ := kvDoc(t, ctx, conn, permKey)["isDeleted"].(bool); !del {
		t.Fatalf("%s should be tombstoned after the drop", permKey)
	}

	// v3 adds it back, identically — the same deterministic key.
	v3 := sampleDef("0.3.0")
	res, err := inst.Upgrade(ctx, v3)
	if err != nil {
		t.Fatalf("Upgrade (re-add) must revive the tombstoned key, not fail: %v", err)
	}
	if res.Created == 0 {
		t.Fatalf("the re-added permission should report as created, got %+v", res)
	}

	revived := kvDoc(t, ctx, conn, permKey)
	if del, _ := revived["isDeleted"].(bool); del {
		t.Fatalf("%s should be live again after the re-add", permKey)
	}
	// The entity must come back whole — a revive that only flipped isDeleted
	// without restoring the body would leave a bodyless permission that every
	// consumer reads as malformed.
	data, ok := revived["data"].(map[string]any)
	if !ok || data["operationType"] != "SampleOp" {
		t.Fatalf("revived permission lost its body: %+v", revived)
	}
	if revived["class"] == nil {
		t.Fatalf("revived permission lost its class: %+v", revived)
	}
}

// tombstoneOutOfBand flips isDeleted:true directly in Core KV for an already
// -committed key, the way RevokePermission/TombstonePermission/TombstoneRole
// would, without going through the package's own diff.
func tombstoneOutOfBand(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) {
	t.Helper()
	entry, err := conn.KVGet(ctx, CoreBucket, key)
	if err != nil {
		t.Fatalf("capture %s entry: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s entry: %v", key, err)
	}
	doc["isDeleted"] = true
	newValue, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal tombstoned %s: %v", key, err)
	}
	if _, err := conn.KVUpdate(ctx, CoreBucket, key, newValue, entry.Revision); err != nil {
		t.Fatalf("simulated out-of-band tombstone of %s: %v", key, err)
	}
}

// TestUpgrade_RespectsOutOfBandRevocation proves an out-of-band tombstone
// (RevokePermission/TombstonePermission) on keys the package continues to
// declare survives an upgrade instead of being silently un-tombstoned by the
// surviving-key update path. Unlike TestUpgrade_ReAddsRemovedEntity, v2 here
// never drops the permission from its manifest — the keys are tombstoned
// purely by the direct KV write, simulating an operator's deliberate
// revocation. It covers both the permission vertex AND the grant link
// (lnk.permission.<id>.grantedBy.role.<id>) — the literal shape
// RevokePermission targets in practice.
func TestUpgrade_RespectsOutOfBandRevocation(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	permID := entityNanoID(v1.Name, permTag("SampleOp", "any"))
	permKey := "vtx.permission." + permID
	grantLinkKey := "lnk.permission." + permID + ".grantedBy.role." + bootstrap.RoleOperatorID

	tombstoneOutOfBand(t, ctx, conn, permKey)
	tombstoneOutOfBand(t, ctx, conn, grantLinkKey)

	// v2 still declares the SAME permission, unchanged — the "survives" case.
	v2 := sampleDef("0.2.0")
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade must not fail on a respected revocation: %v", err)
	}
	if res.RevocationsRespected != 2 {
		t.Fatalf("RevocationsRespected = %d, want 2 (permission + grant link) (%+v)", res.RevocationsRespected, res)
	}

	after := kvDoc(t, ctx, conn, permKey)
	if del, _ := after["isDeleted"].(bool); !del {
		t.Fatalf("%s should stay tombstoned across the upgrade, got %+v", permKey, after)
	}
	afterLink := kvDoc(t, ctx, conn, grantLinkKey)
	if del, _ := afterLink["isDeleted"].(bool); !del {
		t.Fatalf("%s should stay tombstoned across the upgrade, got %+v", grantLinkKey, afterLink)
	}
	// The version-only bump on package vertex + manifest aspect still applies
	// normally — the revocation guard only touches the surviving grant topology.
	if res.Updated != 2 {
		t.Fatalf("unrelated version-only changes should still apply exactly (package vertex + manifest): %+v", res)
	}
}

// TestUpgrade_RevocationGuardExcludesDefinitions proves the guard is scoped to
// grant/role topology only: a package-declared vtx.meta.* definition (a lens)
// that is tombstoned out-of-band while it still survives across versions is
// REVIVED by the next upgrade, same as before this fix. That pre-existing
// revive-via-reapply behavior for definitions is unchanged and out of this
// design's ratified scope (see diffManifest's surviving-key branch) — only
// grant/role topology (what RevokePermission/TombstonePermission/
// TombstoneRole target) respects an out-of-band tombstone.
func TestUpgrade_RevocationGuardExcludesDefinitions(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	lensKey := metaVertexPrefix + entityNanoID(v1.Name, "lens:sampleLens")
	tombstoneOutOfBand(t, ctx, conn, lensKey)

	// v2 still declares the SAME lens, unchanged.
	v2 := sampleDef("0.2.0")
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if res.RevocationsRespected != 0 {
		t.Fatalf("a vtx.meta.* definition must not count toward RevocationsRespected: %+v", res)
	}

	revived := kvDoc(t, ctx, conn, lensKey)
	if del, _ := revived["isDeleted"].(bool); del {
		t.Fatalf("%s (a definition) should be revived by the upgrade, not left tombstoned: %+v", lensKey, revived)
	}
}

// defWithRetentionClass returns the sample package plus one declared
// retention-class holder, so a test can drop the class on the next version and
// observe what the diff does with the holder's keys. Built as a copy rather
// than a change to sampleDef, which every other test in this package shares.
func defWithRetentionClass(version, canonicalName string) Definition {
	def := sampleDef(version)
	def.RetentionClasses = []RetentionClassSpec{{
		CanonicalName:   canonicalName,
		Description:     "Records whose retention obligation outlives a subject's erasure.",
		Policy:          RetentionPolicyEraseOnExpiry,
		RetentionPeriod: "P1Y",
	}}
	return def
}

// TestUpgrade_PreservesRetentionClassHolderOnRemoval proves a package that
// drops (or renames — the canonicalName salts the holder's deterministic
// NanoID, so a rename is a drop plus an add) a retention class does NOT
// tombstone the old holder's keys. Only ShredRetentionClassKey may destroy a
// class's DEK, and its vertex_alive guard refuses a holder whose root is
// already tombstoned — so an auto-tombstone here would strand that key beyond
// any reach, forever. The holder is instead left live-but-undeclared, which is
// what keeps the controller's retention schedule executable.
func TestUpgrade_PreservesRetentionClassHolderOnRemoval(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithRetentionClass("0.1.0", "sampleClass1")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	holderKey := RetentionClassKey(v1.Name, "sampleClass1")
	policyKey := holderKey + ".retentionPolicy"
	for _, k := range []string{holderKey, policyKey} {
		if del, _ := kvDoc(t, ctx, conn, k)["isDeleted"].(bool); del {
			t.Fatalf("%s should be live right after install", k)
		}
	}

	// v2 drops the retention class entirely — both holder keys fall into the
	// diff's old \ new removal partition.
	v2 := sampleDef("0.2.0")
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade (drop class): %v", err)
	}
	if res.RetentionHoldersPreserved != 2 {
		t.Fatalf("RetentionHoldersPreserved = %d, want 2 (holder root + .retentionPolicy) (%+v)", res.RetentionHoldersPreserved, res)
	}
	// The class removal is the only removal in this upgrade, so any nonzero
	// tombstone count here means the exclusion failed to hold.
	if res.Tombstoned != 0 {
		t.Fatalf("dropping a retention class must emit no tombstone at all, got %d (%+v)", res.Tombstoned, res)
	}

	for _, k := range []string{holderKey, policyKey} {
		doc := kvDoc(t, ctx, conn, k)
		if del, _ := doc["isDeleted"].(bool); del {
			t.Fatalf("%s must stay live so ShredRetentionClassKey can still destroy the class key: %+v", k, doc)
		}
	}
	// The holder keeps its policy body too: an operator deciding when to shred
	// reads the retention period off this aspect, and a holder stripped of it
	// is unauditable even though it is technically still shreddable.
	policy, ok := kvDoc(t, ctx, conn, policyKey)["data"].(map[string]any)
	if !ok || policy["canonicalName"] != "sampleClass1" || policy["retentionPeriod"] != "P1Y" {
		t.Fatalf("preserved holder lost its retention policy body: %+v", policy)
	}
}

// TestUpgrade_PreservesRetentionClassHolderOnRename covers the rename half of
// the same removal: the new canonicalName mints a fresh holder while the old
// one leaves the declared set. Both must end live — the new one because the
// package declares it, the old one because its DEK is still only destroyable
// by ShredRetentionClassKey.
func TestUpgrade_PreservesRetentionClassHolderOnRename(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithRetentionClass("0.1.0", "sampleClass1")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	oldHolder := RetentionClassKey(v1.Name, "sampleClass1")

	v2 := defWithRetentionClass("0.2.0", "sampleClass2")
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade (rename class): %v", err)
	}
	newHolder := RetentionClassKey(v2.Name, "sampleClass2")
	if newHolder == oldHolder {
		t.Fatalf("the canonicalName must salt the holder NanoID; both names minted %s", newHolder)
	}
	if res.RetentionHoldersPreserved != 2 {
		t.Fatalf("RetentionHoldersPreserved = %d, want 2 (the old holder root + its .retentionPolicy) (%+v)", res.RetentionHoldersPreserved, res)
	}
	if res.Tombstoned != 0 {
		t.Fatalf("renaming a retention class must emit no tombstone, got %d (%+v)", res.Tombstoned, res)
	}
	for _, k := range []string{oldHolder, oldHolder + ".retentionPolicy", newHolder, newHolder + ".retentionPolicy"} {
		if del, _ := kvDoc(t, ctx, conn, k)["isDeleted"].(bool); del {
			t.Fatalf("%s must be live after the rename", k)
		}
	}
}

// TestUpgrade_RetentionHolderAlreadyTombstonedReportedSeparately covers the
// state the exclusion cannot rescue: a holder whose root is ALREADY tombstoned
// when the removal path reads it. ShredRetentionClassKey's vertex_alive guard
// refuses that root forever, so the class key it custodies is past every
// destruction path — counting it as "preserved, still shreddable" would tell
// the one operator who can escalate the damage that there is nothing to
// escalate. It gets its own count, and a holder key that is absent entirely
// gets neither (there is nothing there to preserve).
func TestUpgrade_RetentionHolderAlreadyTombstonedReportedSeparately(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithRetentionClass("0.1.0", "sampleClass1")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	holderKey := RetentionClassKey(v1.Name, "sampleClass1")
	policyKey := holderKey + ".retentionPolicy"

	// Stand the holder root up as a stranded one: tombstoned in Core KV while
	// still declared. Written directly because no sanctioned op produces this
	// state any more — that is precisely what makes it pre-existing damage.
	doc := kvDoc(t, ctx, conn, holderKey)
	doc["isDeleted"] = true
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal stranded holder: %v", err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, holderKey, raw); err != nil {
		t.Fatalf("KVPut stranded holder: %v", err)
	}

	v2 := sampleDef("0.2.0") // drops the class — both holder keys hit removal
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade (drop class): %v", err)
	}
	if res.RetentionHoldersAlreadyStranded != 1 {
		t.Fatalf("RetentionHoldersAlreadyStranded = %d, want 1 (the tombstoned root) (%+v)", res.RetentionHoldersAlreadyStranded, res)
	}
	if res.RetentionHoldersPreserved != 1 {
		t.Fatalf("RetentionHoldersPreserved = %d, want 1 (the still-live .retentionPolicy aspect) (%+v)", res.RetentionHoldersPreserved, res)
	}
	if res.Tombstoned != 0 {
		t.Fatalf("a stranded holder is still excluded from the tombstone set, got %d (%+v)", res.Tombstoned, res)
	}
	if del, _ := kvDoc(t, ctx, conn, policyKey)["isDeleted"].(bool); del {
		t.Fatalf("%s must stay live — only the root was stranded", policyKey)
	}

	// An absent holder key is neither preserved nor stranded: there is nothing
	// under it to report either way. Purge the policy aspect and re-run the
	// same drop against a fresh v1 base.
	ctx2, conn2, inst2 := newInstallerHarness(t)
	if _, err := inst2.Install(ctx2, defWithRetentionClass("0.1.0", "sampleClass1")); err != nil {
		t.Fatalf("Install (absent-key case): %v", err)
	}
	if err := conn2.KVPurge(ctx2, CoreBucket, policyKey); err != nil {
		t.Fatalf("KVPurge %s: %v", policyKey, err)
	}
	res2, err := inst2.Upgrade(ctx2, sampleDef("0.2.0"))
	if err != nil {
		t.Fatalf("Upgrade (absent-key case): %v", err)
	}
	if res2.RetentionHoldersPreserved != 1 || res2.RetentionHoldersAlreadyStranded != 0 {
		t.Fatalf("a purged holder key must be counted in neither bucket: preserved=%d stranded=%d (%+v)",
			res2.RetentionHoldersPreserved, res2.RetentionHoldersAlreadyStranded, res2)
	}
}

// TestNoChangesReason pins the empty-delta Reason string. A run that left a
// grant revoked did something worth naming, and a bare "no changes" would be
// the one sentence that hides it.
func TestNoChangesReason(t *testing.T) {
	plain := noChangesReason("pkg", 0, 0)
	if !strings.Contains(plain, "no changes") {
		t.Fatalf("no revocations: want the plain no-changes reason, got %q", plain)
	}

	// A refused secure-column narrowing is the case that most needs naming: the
	// widen makes the body match what is committed, so the whole upgrade can
	// come out empty with the author's declared edit silently declined.
	widened := noChangesReason("pkg", 0, 2)
	if !strings.Contains(widened, "2 column(s)") || strings.Contains(widened, "no changes") {
		t.Fatalf("a refused secure-column narrowing must be named, not reported as a plain no-change run: %q", widened)
	}

	both := noChangesReason("pkg", 1, 2)
	if !strings.Contains(both, "previously-revoked grant(s)") || !strings.Contains(both, "secure column(s)") {
		t.Fatalf("both outcomes must survive together: %q", both)
	}

	revoked := noChangesReason("pkg", 3, 0)
	if !strings.Contains(revoked, "3 previously-revoked grant(s)") {
		t.Fatalf("revocations respected must be named: %q", revoked)
	}
	if strings.Contains(revoked, "no changes") {
		t.Fatalf("a run that respected a revocation is not a plain no-change run: %q", revoked)
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

// TestUpgrade_AddsNewAspectKeyOnSurvivingVertex proves an upgrade that adds a
// brand-new ASPECT to an already-installed vertex creates that key while the
// vertex and its sibling aspects survive untouched. Every other create case in
// this file adds a whole new vertex; this is the shape a prose backfill takes
// — a weaverTarget's root and `.spec` are byte-identical across the bump and
// only `.description` is new — so nothing else pins it.
func TestUpgrade_AddsNewAspectKeyOnSurvivingVertex(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	target := WeaverTargetSpec{
		TargetID: "sampleConvergence",
		LensRef:  "sampleLens",
		Gaps:     map[string]GapActionSpec{"missing_thing": {Action: "directOp", Operation: "SampleOp"}},
	}
	v1 := sampleDef("0.1.0")
	v1.WeaverTargets = []WeaverTargetSpec{target}
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}

	targetKey := metaVertexPrefix + entityNanoID(v1.Name, "weaverTarget:sampleConvergence")
	descKey := targetKey + ".description"
	specKey := targetKey + ".spec"
	if _, err := conn.KVGet(ctx, CoreBucket, descKey); err == nil {
		t.Fatalf("v1 declared no Description but %s exists", descKey)
	}
	origSpec := kvDoc(t, ctx, conn, specKey)
	origSpecCreatedAt, _ := origSpec["createdAt"].(string)

	const prose = "Every sample entity reaches its converged state."
	v2 := sampleDef("0.2.0")
	target.Description = prose
	v2.WeaverTargets = []WeaverTargetSpec{target}

	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if res.Skipped {
		t.Fatalf("an upgrade adding a description should not be skipped: %+v", res)
	}

	desc := kvDoc(t, ctx, conn, descKey)
	if del, _ := desc["isDeleted"].(bool); del {
		t.Fatalf("backfilled %s should be live", descKey)
	}
	if got, _ := desc["data"].(map[string]any)["text"].(string); got != prose {
		t.Fatalf("backfilled description text = %q, want %q", got, prose)
	}

	// The spec aspect is byte-equal across the bump, so the body-equality skip
	// must leave its creation provenance alone — a description backfill is not
	// a rewrite of the target the engine already runs.
	newSpec := kvDoc(t, ctx, conn, specKey)
	if del, _ := newSpec["isDeleted"].(bool); del {
		t.Fatalf("%s should still be live after the backfill", specKey)
	}
	if gotCreatedAt, _ := newSpec["createdAt"].(string); gotCreatedAt != origSpecCreatedAt {
		t.Fatalf("spec createdAt changed under a description backfill: was %q now %q", origSpecCreatedAt, gotCreatedAt)
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
	mutations, sum, _, err := inst.computeDeltaAgainst(ctx, existing, v2)
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

// TestSecureColumnsOf_MergesOneColumnAcrossLevels pins the read path's
// treatment of a document carrying the same secure column at BOTH targetConfig
// levels. Both levels must be read — that is what keeps the erasure detection
// agreeing with the destruction-readiness oracle — but the column is ONE column
// and ONE custody record, so it must be emitted once, carrying the union of
// what each level recorded. Emitting it twice makes every consumer that counts
// entries report one erasure as two and print the column twice in a refusal.
//
// An entry with no column name is never merged: unnameable is not an identity,
// and two of them are two records.
func TestSecureColumnsOf_MergesOneColumnAcrossLevels(t *testing.T) {
	doc := map[string]any{
		"targetConfig": map[string]any{"secureColumns": []any{
			map[string]any{"column": "applicant_name", "holderTypes": []any{"identity"}},
			map[string]any{"column": "", "holderTypes": []any{"retentionclass"}},
		}},
		"data": map[string]any{"targetConfig": map[string]any{"secureColumns": []any{
			map[string]any{"column": "applicant_name", "holderTypes": []any{"retentionclass"}},
			map[string]any{"column": "", "holderTypes": []any{"identity"}},
		}}},
	}
	entries := secureColumnsOf(doc)
	named := 0
	unnamed := 0
	for _, e := range entries {
		if column, _ := e["column"].(string); column == "" {
			unnamed++
			continue
		}
		named++
		holders := stringsOf(e["holderTypes"])
		slices.Sort(holders)
		if !slices.Equal(holders, []string{"identity", "retentionclass"}) {
			t.Errorf("merged holderTypes = %v, want the union of both levels", holders)
		}
	}
	if named != 1 {
		t.Errorf("secureColumnsOf emitted %d entries for one column name, want 1", named)
	}
	if unnamed != 2 {
		t.Errorf("secureColumnsOf emitted %d unnamed entries, want 2 — unnameable is not an identity", unnamed)
	}
}

// TestWidenSecureColumns_UnionsAcrossTargetConfigLevels pins the write path's
// half of the same shape: the committed custody history for a column is
// everything EITHER targetConfig level recorded, and the widen must carry all
// of it forward. Losing one level's holder types means ciphertext written under
// a holder the persisted spec no longer names — invisible to every
// destruction-readiness reader, which is the exact loss the widen exists to
// prevent.
//
// The fixture is built to be able to FAIL: the surviving declaration names only
// the second level's holder, so a history that kept just that level produces a
// spec identical to the declaration and the loss leaves no other trace.
//
// Two mechanisms hold this jointly — secureColumnsOf merges a column across
// levels, and the history build unions rather than assigns — so this asserts
// the INVARIANT, not either limb: it fails when both are broken and passes
// while either stands.
func TestWidenSecureColumns_UnionsAcrossTargetConfigLevels(t *testing.T) {
	committed := map[string]any{
		"targetConfig": map[string]any{"secureColumns": []any{
			map[string]any{"column": "applicant_name", "holderTypes": []any{"alpha"}},
		}},
		"data": map[string]any{"targetConfig": map[string]any{"secureColumns": []any{
			map[string]any{"column": "applicant_name", "holderTypes": []any{"beta"}},
		}}},
	}
	newDoc := map[string]any{
		"data": map[string]any{"targetConfig": map[string]any{"secureColumns": []map[string]any{
			{"column": "applicant_name", "holderTypes": []string{"beta"}},
		}}},
	}
	widenSecureColumnsForUpdate("vtx.meta.aaaaaaaaaaaaaaaaaaaa.spec", committed, newDoc)
	got := stringsOf(secureColumnsOf(newDoc)[0]["holderTypes"])
	slices.Sort(got)
	if !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("widened holderTypes = %v, want both levels' committed holders carried forward", got)
	}
}

// defWithSecureLens returns the sample package plus one Secure Lens whose
// single secure column declares the supplied holder types, so a test can narrow
// or widen that declaration on the next version and observe what the diff
// persists. queryTimeout is the unrelated-field lever a test uses to force an
// update on a version whose only other change is the narrowing.
func defWithSecureLens(version string, holderTypes []string, queryTimeout string) Definition {
	def := sampleDef(version)
	def.Lenses = append(def.Lenses, LensSpec{
		CanonicalName: "sampleSecureLens",
		Class:         "meta.lens",
		Adapter:       "postgres",
		Table:         "read_sample_secure",
		Engine:        "full",
		Protected:     true,
		QueryTimeout:  queryTimeout,
		Spec:          `MATCH (n:sample) RETURN n.key AS key, n.applicant_name AS applicant_name`,
		Columns: []PostgresColumn{
			{Name: "key", Type: "text"},
			{Name: "applicant_name", Type: "text"},
		},
		SecureColumns: []SecureColumn{{
			Column:      "applicant_name",
			HolderTypes: holderTypes,
			Field:       "value",
		}},
	})
	return def
}

// secureLensSpecKey is the Core KV key of defWithSecureLens's spec aspect —
// the lens NanoID is deterministic in (package name, canonicalName), so it
// survives the version bump an upgrade carries.
func secureLensSpecKey(def Definition) string {
	return metaVertexPrefix + entityNanoID(def.Name, "lens:sampleSecureLens") + ".spec"
}

// committedHolderTypes reads the persisted holderTypes of a secure lens spec's
// single secure column, through the same json.Unmarshal round trip the
// installer's own diff read sees.
func committedHolderTypes(t *testing.T, doc map[string]any, column string) []string {
	t.Helper()
	data, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("spec doc has no data envelope: %+v", doc)
	}
	cfg, ok := data["targetConfig"].(map[string]any)
	if !ok {
		t.Fatalf("spec data has no targetConfig: %+v", data)
	}
	entries, ok := cfg["secureColumns"].([]any)
	if !ok {
		t.Fatalf("targetConfig has no secureColumns list: %+v", cfg)
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if entry["column"] != column {
			continue
		}
		held, ok := entry["holderTypes"].([]any)
		if !ok {
			t.Fatalf("secure column %q has no holderTypes list: %+v", column, entry)
		}
		out := make([]string, 0, len(held))
		for _, h := range held {
			s, ok := h.(string)
			if !ok {
				t.Fatalf("secure column %q holderTypes carries a non-string %v (%T)", column, h, h)
			}
			out = append(out, s)
		}
		return out
	}
	t.Fatalf("secure column %q absent from the committed spec: %+v", column, cfg)
	return nil
}

// TestUpgrade_SecureColumnHolderTypesNeverNarrow proves an upgrade that drops a
// holder type from a secure column's declaration does not drop it from the
// persisted spec. Refractor's destruction-readiness oracle and rebuild target
// lister both answer "which lenses hold ciphertext for this holder type?" from
// the CURRENT spec, so a narrowed declaration makes pre-upgrade ciphertext
// invisible to both and the platform attests as covered over rows it can no
// longer see. The narrowing is the whole diff for this key, so once widened the
// body matches what is already committed and no update is emitted at all.
func TestUpgrade_SecureColumnHolderTypesNeverNarrow(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithSecureLens("0.1.0", []string{"identity", "retentionclass"}, "")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)
	before := kvDoc(t, ctx, conn, specKey)
	if got := committedHolderTypes(t, before, "applicant_name"); !slices.Equal(got, []string{"identity", "retentionclass"}) {
		t.Fatalf("post-install holderTypes = %v, want [identity retentionclass]", got)
	}

	v2 := defWithSecureLens("0.2.0", []string{"identity"}, "")
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade (narrow holderTypes): %v", err)
	}
	// The refusal has to reach the operator. Once widened, this key's body
	// matches what is committed and the diff skips it, so the counter is the
	// only trace the declined edit leaves.
	if res.SecureColumnsWidened != 1 {
		t.Fatalf("SecureColumnsWidened = %d, want 1 — a refused narrowing that leaves no mutation must still be reported (%+v)", res.SecureColumnsWidened, res)
	}

	after := kvDoc(t, ctx, conn, specKey)
	if got := committedHolderTypes(t, after, "applicant_name"); !slices.Equal(got, []string{"identity", "retentionclass"}) {
		t.Fatalf("holderTypes after a narrowing upgrade = %v, want the union [identity retentionclass] — ciphertext held under a dropped holder type would be invisible to the destruction oracle", got)
	}
	// Whole-document equality, not merely "retentionclass is still in there":
	// the widened body is byte-equal to what is committed, so the diff must
	// skip the key entirely. Any update would restamp provenance, which this
	// catches — as would a future regression that reorders or duplicates the
	// list.
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a widen-only narrowing must collapse to a no-op update:\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestUpgrade_SecureColumnHolderTypesWiden covers the direction that is not a
// hazard: a package ADDING a holder type persists exactly what it declared. The
// union must not double-count the holder types the committed spec already
// carried.
func TestUpgrade_SecureColumnHolderTypesWiden(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithSecureLens("0.1.0", []string{"identity"}, "")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)

	v2 := defWithSecureLens("0.2.0", []string{"identity", "retentionclass"}, "")
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade (widen holderTypes): %v", err)
	}
	// Nothing was refused here — the package got exactly what it asked for, so
	// counting it would tell an operator an edit was declined when it landed.
	if res.SecureColumnsWidened != 0 {
		t.Fatalf("SecureColumnsWidened = %d, want 0 — a package ADDING a holder type had nothing refused (%+v)", res.SecureColumnsWidened, res)
	}

	got := committedHolderTypes(t, kvDoc(t, ctx, conn, specKey), "applicant_name")
	if !slices.Equal(got, []string{"identity", "retentionclass"}) {
		t.Fatalf("holderTypes after a widening upgrade = %v, want exactly the declared [identity retentionclass]", got)
	}
}

// TestUpgrade_SecureColumnNarrowingBundledWithRealChange proves the widen is not
// merely a body-equality artifact. Here the narrowing rides along with an
// unrelated edit (queryTimeout), so the key genuinely updates — and the value it
// commits must still be the union, not the narrowed declaration.
func TestUpgrade_SecureColumnNarrowingBundledWithRealChange(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithSecureLens("0.1.0", []string{"identity", "retentionclass"}, "5s")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)
	before := kvDoc(t, ctx, conn, specKey)

	v2 := defWithSecureLens("0.2.0", []string{"identity"}, "9s")
	if _, err := inst.Upgrade(ctx, v2); err != nil {
		t.Fatalf("Upgrade (narrow + unrelated edit): %v", err)
	}

	after := kvDoc(t, ctx, conn, specKey)
	if reflect.DeepEqual(before, after) {
		t.Fatalf("the unrelated queryTimeout edit must still update %s, but the committed body is unchanged: %+v", specKey, after)
	}
	if got := committedHolderTypes(t, after, "applicant_name"); !slices.Equal(got, []string{"identity", "retentionclass"}) {
		t.Fatalf("holderTypes after a bundled narrowing = %v, want the union [identity retentionclass]", got)
	}
	cfg := after["data"].(map[string]any)["targetConfig"].(map[string]any)
	if cfg["queryTimeout"] != "9s" {
		t.Fatalf("the unrelated edit must land too: queryTimeout = %v, want 9s", cfg["queryTimeout"])
	}
}

// TestUpgrade_NewSecureColumnLandsAsDeclared covers the column the union logic
// must never touch: one the committed spec never declared. It has no history to
// carry, so it lands exactly as authored.
func TestUpgrade_NewSecureColumnLandsAsDeclared(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithSecureLens("0.1.0", []string{"identity", "retentionclass"}, "")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)

	v2 := defWithSecureLens("0.2.0", []string{"identity", "retentionclass"}, "")
	lens := &v2.Lenses[len(v2.Lenses)-1]
	lens.Columns = append(lens.Columns, PostgresColumn{Name: "applicant_email", Type: "text"})
	lens.SecureColumns = append(lens.SecureColumns, SecureColumn{
		Column:      "applicant_email",
		HolderTypes: []string{"identity"},
		Field:       "value",
	})
	if _, err := inst.Upgrade(ctx, v2); err != nil {
		t.Fatalf("Upgrade (add secure column): %v", err)
	}

	after := kvDoc(t, ctx, conn, specKey)
	if got := committedHolderTypes(t, after, "applicant_email"); !slices.Equal(got, []string{"identity"}) {
		t.Fatalf("a brand-new secure column's holderTypes = %v, want exactly the declared [identity]", got)
	}
	if got := committedHolderTypes(t, after, "applicant_name"); !slices.Equal(got, []string{"identity", "retentionclass"}) {
		t.Fatalf("the pre-existing secure column must be undisturbed, got %v", got)
	}
}

// TestUpgrade_SecureColumnHolderTypesNeverNarrowOnRevive covers the removal-and-
// re-add path, which reaches a different diffManifest branch than an ordinary
// body edit. A package entity key is deterministic in (package, kind,
// canonicalName), so the key a removal tombstones is the exact key the same lens
// returns to — the committed spec a revive reads is this lens's own previous
// incarnation. Meanwhile the removal tombstoned a Core KV definition; it did not
// go near the Postgres table holding the ciphertext those earlier holder types
// encrypted. A lens that comes back declaring fewer holder types than it left
// with therefore strands exactly the rows §24.6 is about, and the revive path
// has to refuse the narrowing for the same reason the update path does.
func TestUpgrade_SecureColumnHolderTypesNeverNarrowOnRevive(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithSecureLens("0.1.0", []string{"identity", "retentionclass"}, "")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)

	// v2 drops the Secure Lens entirely — the spec key falls into the diff's
	// old \ new removal partition and is tombstoned. Dropping the lens erases
	// its committed secure columns, so the removal itself has to be declared;
	// the widen this test is about is what happens on the way BACK.
	v2 := sampleDef("0.2.0")
	v2.RetiredSecureColumns = []RetiredSecureColumn{{
		Lens: "sampleSecureLens", Note: "test fixture: the lens is coming back in v3",
	}}
	if _, err := inst.Upgrade(ctx, v2); err != nil {
		t.Fatalf("Upgrade (drop the secure lens): %v", err)
	}
	if del, _ := kvDoc(t, ctx, conn, specKey)["isDeleted"].(bool); !del {
		t.Fatalf("%s should be tombstoned after the lens leaves the manifest", specKey)
	}

	// v3 re-adds the same lens (same canonicalName ⇒ same deterministic key),
	// declaring only the narrower holder set.
	v3 := defWithSecureLens("0.3.0", []string{"identity"}, "")
	res, err := inst.Upgrade(ctx, v3)
	if err != nil {
		t.Fatalf("Upgrade (re-add the secure lens narrowed): %v", err)
	}
	if res.SecureColumnsWidened != 1 {
		t.Fatalf("SecureColumnsWidened = %d, want 1 on the revive path (%+v)", res.SecureColumnsWidened, res)
	}

	revived := kvDoc(t, ctx, conn, specKey)
	if del, _ := revived["isDeleted"].(bool); del {
		t.Fatalf("%s should be live again after the re-add: %+v", specKey, revived)
	}
	if got := committedHolderTypes(t, revived, "applicant_name"); !slices.Equal(got, []string{"identity", "retentionclass"}) {
		t.Fatalf("holderTypes after a narrowed re-add = %v, want the union [identity retentionclass] — the removal tombstoned the definition, not the ciphertext the dropped holder type encrypted", got)
	}
}

// TestUpgrade_SecureColumnsMatchedByNameNotIndex is the regression that fails if
// old and new secure columns are ever paired positionally. v2 both reorders the
// declared list and narrows the column that moved, so an index-based pairing
// would graft one column's holder-type history onto the other and leave the
// narrowed column narrowed.
func TestUpgrade_SecureColumnsMatchedByNameNotIndex(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	// v1 declares [applicant_name, applicant_email] in that order, with
	// different holder sets, so a positional mix-up is observable.
	v1 := defWithSecureLens("0.1.0", []string{"identity", "retentionclass"}, "")
	l1 := &v1.Lenses[len(v1.Lenses)-1]
	l1.Columns = append(l1.Columns, PostgresColumn{Name: "applicant_email", Type: "text"})
	l1.SecureColumns = append(l1.SecureColumns, SecureColumn{
		Column:      "applicant_email",
		HolderTypes: []string{"identity"},
		Field:       "value",
	})
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)

	// v2 reverses the declared order AND narrows applicant_name, which now sits
	// at the index applicant_email occupied.
	v2 := defWithSecureLens("0.2.0", []string{"identity"}, "")
	l2 := &v2.Lenses[len(v2.Lenses)-1]
	l2.Columns = append(l2.Columns, PostgresColumn{Name: "applicant_email", Type: "text"})
	l2.SecureColumns = []SecureColumn{
		{Column: "applicant_email", HolderTypes: []string{"identity"}, Field: "value"},
		{Column: "applicant_name", HolderTypes: []string{"identity"}, Field: "value"},
	}
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade (reorder + narrow): %v", err)
	}
	if res.SecureColumnsWidened != 1 {
		t.Fatalf("SecureColumnsWidened = %d, want exactly 1 — only applicant_name was narrowed (%+v)", res.SecureColumnsWidened, res)
	}

	after := kvDoc(t, ctx, conn, specKey)
	if got := committedHolderTypes(t, after, "applicant_name"); !slices.Equal(got, []string{"identity", "retentionclass"}) {
		t.Fatalf("applicant_name holderTypes = %v, want the union [identity retentionclass]; a positional pairing would have matched it against applicant_email's history and left it narrowed", got)
	}
	if got := committedHolderTypes(t, after, "applicant_email"); !slices.Equal(got, []string{"identity"}) {
		t.Fatalf("applicant_email holderTypes = %v, want its own unchanged [identity]; a positional pairing would have grafted applicant_name's retentionclass onto it", got)
	}
}

// TestUpgrade_SecureColumnHolderTypeReorderIsNotAChange pins the ordering rule.
// Holder-type order means nothing to the decryptor, but reloadpin refuses to
// hot-reload ANY secureColumns edit, so writing a re-ordered copy of a set that
// did not actually change would emit a pointless update and tell an operator to
// re-activate a lens whose behavior is identical.
func TestUpgrade_SecureColumnHolderTypeReorderIsNotAChange(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithSecureLens("0.1.0", []string{"retentionclass", "identity"}, "")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)
	before := kvDoc(t, ctx, conn, specKey)

	v2 := defWithSecureLens("0.2.0", []string{"identity", "retentionclass"}, "")
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade (reorder only): %v", err)
	}
	if res.SecureColumnsWidened != 0 {
		t.Fatalf("SecureColumnsWidened = %d, want 0 — a pure reorder refuses nothing (%+v)", res.SecureColumnsWidened, res)
	}

	after := kvDoc(t, ctx, conn, specKey)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("re-ordering the same holder-type set must not rewrite the spec (it would force a needless lens re-activation):\nbefore %+v\nafter  %+v", before, after)
	}
}

// defWithTwoSecureColumns extends defWithSecureLens with a second secure
// column, so a test can drop ONE of them and observe an erasure that is not
// also the disappearance of the whole spec.
func defWithTwoSecureColumns(version string) Definition {
	def := defWithSecureLens(version, []string{"identity", "retentionclass"}, "")
	lens := &def.Lenses[len(def.Lenses)-1]
	lens.Columns = append(lens.Columns, PostgresColumn{Name: "applicant_email", Type: "text"})
	lens.SecureColumns = append(lens.SecureColumns, SecureColumn{
		Column:      "applicant_email",
		HolderTypes: []string{"identity"},
		Field:       "value",
	})
	return def
}

// defWithOneSecureColumn is defWithTwoSecureColumns minus applicant_email —
// the version whose upgrade erases that column's key-custody record.
func defWithOneSecureColumn(version string) Definition {
	def := defWithTwoSecureColumns(version)
	lens := &def.Lenses[len(def.Lenses)-1]
	lens.SecureColumns = lens.SecureColumns[:1]
	return def
}

// committedSecureColumnNames lists the columns a committed lens spec still
// declares as secure, without failing on absence — the assertion an erasure
// test needs and committedHolderTypes cannot give (it fatals on a missing
// column, which is exactly the state under test here).
func committedSecureColumnNames(t *testing.T, doc map[string]any) []string {
	t.Helper()
	data, ok := doc["data"].(map[string]any)
	if !ok {
		t.Fatalf("spec doc has no data envelope: %+v", doc)
	}
	cfg, ok := data["targetConfig"].(map[string]any)
	if !ok {
		t.Fatalf("spec data has no targetConfig: %+v", data)
	}
	entries, _ := cfg["secureColumns"].([]any)
	var out []string
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := entry["column"].(string); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// TestUpgrade_UndeclaredSecureColumnDropRefused is R2: a lens the package still
// declares, minus one of its secure columns. The widen cannot help — it unions
// holder types only for columns BOTH specs name — so the persisted spec would
// simply forget that applicant_email ever held ciphertext, while every row it
// encrypted stays in the target store. Refractor's destruction-readiness oracle
// reads the current spec and nothing else, so the erasure turns into an
// attestation of coverage the platform does not have. The upgrade must refuse,
// and refuse before anything commits.
func TestUpgrade_UndeclaredSecureColumnDropRefused(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithTwoSecureColumns("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)
	before := kvDoc(t, ctx, conn, specKey)

	_, err := inst.Upgrade(ctx, defWithOneSecureColumn("0.2.0"))
	if err == nil {
		t.Fatalf("dropping a committed secure column with no declared retirement must fail the upgrade")
	}
	if !strings.Contains(err.Error(), "applicant_email") || !strings.Contains(err.Error(), "RetiredSecureColumn") {
		t.Fatalf("the refusal must name the dropped column and the declaration to add, got: %v", err)
	}
	// The remedy an author is handed must never be the move the guard exists to
	// catch.
	if strings.Contains(err.Error(), "remove the column") || strings.Contains(err.Error(), "drop the column") {
		t.Fatalf("the refusal must not suggest dropping the column as the fix: %v", err)
	}

	after := kvDoc(t, ctx, conn, specKey)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused upgrade must commit nothing:\nbefore %+v\nafter  %+v", before, after)
	}
	if got := committedSecureColumnNames(t, after); !slices.Equal(got, []string{"applicant_name", "applicant_email"}) {
		t.Fatalf("committed secure columns = %v, want both still declared", got)
	}
}

// TestUpgrade_DeclaredSecureColumnRetirementProceeds is the positive vector for
// the refusal above: the identical edit, declared. The erasure lands (the
// platform verifies nothing about the ciphertext — the declaration is the
// author's attestation) and is COUNTED, because custody history leaving the
// system is not an outcome an operator should have to infer.
func TestUpgrade_DeclaredSecureColumnRetirementProceeds(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithTwoSecureColumns("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)

	v2 := defWithOneSecureColumn("0.2.0")
	v2.RetiredSecureColumns = []RetiredSecureColumn{{
		Lens:   "sampleSecureLens",
		Column: "applicant_email",
		Note:   "applicant_email rows were re-keyed under applicant_name's envelope in the 0.2.0 migration",
	}}
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade (declared retirement): %v", err)
	}
	if res.SecureColumnsRetired != 1 {
		t.Fatalf("SecureColumnsRetired = %d, want 1 (%+v)", res.SecureColumnsRetired, res)
	}

	after := kvDoc(t, ctx, conn, specKey)
	if got := committedSecureColumnNames(t, after); !slices.Equal(got, []string{"applicant_name"}) {
		t.Fatalf("committed secure columns = %v, want the declared retirement to have landed", got)
	}
	if got := committedHolderTypes(t, after, "applicant_name"); !slices.Equal(got, []string{"identity", "retentionclass"}) {
		t.Fatalf("the surviving column must be untouched by the retirement, got %v", got)
	}
}

// TestUpgrade_UndeclaredSecureLensRemovalRefused is R3: the lens leaves the
// manifest entirely and its whole spec is tombstoned with its secure columns
// still standing. The oracle skips a tombstoned lens outright, so this erases
// even more custody record than dropping a single column does — and the removal
// never went near the target store holding the ciphertext.
func TestUpgrade_UndeclaredSecureLensRemovalRefused(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithSecureLens("0.1.0", []string{"identity", "retentionclass"}, "")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)

	_, err := inst.Upgrade(ctx, sampleDef("0.2.0"))
	if err == nil {
		t.Fatalf("removing a lens whose committed spec carries secure columns must fail the upgrade")
	}
	if !strings.Contains(err.Error(), "sampleSecureLens") || !strings.Contains(err.Error(), "retentionclass") {
		t.Fatalf("the refusal must name the lens and the holder types at stake, got: %v", err)
	}

	doc := kvDoc(t, ctx, conn, specKey)
	if del, _ := doc["isDeleted"].(bool); del {
		t.Fatalf("a refused upgrade must not tombstone %s: %+v", specKey, doc)
	}
}

// TestUpgrade_DeclaredSecureLensRetirementProceeds is the removal's positive
// vector. Column:"" is the selector a whole-spec erasure needs, because the
// lens takes every secure column it had with it.
func TestUpgrade_DeclaredSecureLensRetirementProceeds(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithTwoSecureColumns("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)

	v2 := sampleDef("0.2.0")
	v2.RetiredSecureColumns = []RetiredSecureColumn{{
		Lens: "sampleSecureLens",
		Note: "the read_sample_secure table was dropped after its rows were shredded",
	}}
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade (declared lens retirement): %v", err)
	}
	// The count is in COLUMNS, not erasure events: this lens took both of its
	// secure columns with it, and a "1" here would be a number an operator
	// cannot compare with the SecureColumnsWidened printed beside it.
	if res.SecureColumnsRetired != 2 {
		t.Fatalf("SecureColumnsRetired = %d, want 2 — the lens took both secure columns with it (%+v)", res.SecureColumnsRetired, res)
	}
	if del, _ := kvDoc(t, ctx, conn, specKey)["isDeleted"].(bool); !del {
		t.Fatalf("%s should be tombstoned once the retirement is declared", specKey)
	}
}

// TestUpgrade_PerColumnRetirementDoesNotExcuseLensRemoval pins the asymmetry
// between the two selectors. Attesting that ONE column's history is safe to
// stop carrying says nothing about the rest of the lens's, so a per-column
// declaration must not wave through the removal of the whole spec — the guard
// would otherwise be defeated by declaring the cheapest column in the list.
func TestUpgrade_PerColumnRetirementDoesNotExcuseLensRemoval(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	if _, err := inst.Install(ctx, defWithTwoSecureColumns("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	v2 := sampleDef("0.2.0")
	v2.RetiredSecureColumns = []RetiredSecureColumn{{
		Lens:   "sampleSecureLens",
		Column: "applicant_email",
		Note:   "only this one column was swept",
	}}
	_, err := inst.Upgrade(ctx, v2)
	if err == nil {
		t.Fatalf("a per-column retirement must not excuse tombstoning the whole spec")
	}
	if !strings.Contains(err.Error(), `Column: ""`) {
		t.Fatalf("the refusal must point at the Column:\"\" selector the whole-spec erasure needs, got: %v", err)
	}
}

// TestUpgrade_RenamedSecureLensRetirementNamesTheOldLens covers the rename,
// which is the shape most likely to be declared wrong. A lens NanoID is salted
// by its canonicalName, so a rename mints a wholly new key and tombstones the
// old one — and it is the OLD name whose spec loses its secure columns. A
// declaration naming the new lens excuses nothing.
func TestUpgrade_RenamedSecureLensRetirementNamesTheOldLens(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithSecureLens("0.1.0", []string{"identity", "retentionclass"}, "")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	oldSpecKey := secureLensSpecKey(v1)

	renamed := func(version, declaredLens string) Definition {
		def := defWithSecureLens(version, []string{"identity", "retentionclass"}, "")
		def.Lenses[len(def.Lenses)-1].CanonicalName = "sampleSecureLensRenamed"
		if declaredLens != "" {
			def.RetiredSecureColumns = []RetiredSecureColumn{{
				Lens: declaredLens,
				Note: "the renamed lens projects the same rows from the same table",
			}}
		}
		return def
	}

	if _, err := inst.Upgrade(ctx, renamed("0.2.0", "sampleSecureLensRenamed")); err == nil {
		t.Fatalf("declaring the NEW lens name must not excuse the old lens's erasure")
	}
	if del, _ := kvDoc(t, ctx, conn, oldSpecKey)["isDeleted"].(bool); del {
		t.Fatalf("a refused rename must not tombstone %s", oldSpecKey)
	}

	res, err := inst.Upgrade(ctx, renamed("0.2.0", "sampleSecureLens"))
	if err != nil {
		t.Fatalf("Upgrade (rename, old name declared): %v", err)
	}
	if res.SecureColumnsRetired != 1 {
		t.Fatalf("SecureColumnsRetired = %d, want 1 (%+v)", res.SecureColumnsRetired, res)
	}
	if del, _ := kvDoc(t, ctx, conn, oldSpecKey)["isDeleted"].(bool); !del {
		t.Fatalf("%s should be tombstoned once the rename's retirement is declared", oldSpecKey)
	}
}

// TestUpgrade_SecureColumnRetirementRequiresNote proves the Note is load-bearing
// rather than decorative. Nothing reads it, but a retirement with no stated
// reason is indistinguishable from a reflex, and the next operator asking who
// decided the ciphertext was safe to stop tracking has nothing to read.
func TestUpgrade_SecureColumnRetirementRequiresNote(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	if _, err := inst.Install(ctx, defWithTwoSecureColumns("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	v2 := defWithOneSecureColumn("0.2.0")
	v2.RetiredSecureColumns = []RetiredSecureColumn{{
		Lens:   "sampleSecureLens",
		Column: "applicant_email",
	}}
	_, err := inst.Upgrade(ctx, v2)
	if err == nil {
		t.Fatalf("a retirement with an empty Note must be refused")
	}
	if !strings.Contains(err.Error(), "empty Note") {
		t.Fatalf("the refusal must say the Note is what is missing, got: %v", err)
	}
}

// TestUpgrade_HolderTypeNarrowingIsNotASecureColumnRetirement is the regression
// pin on the mechanism this guard sits beside. Narrowing a column's holderTypes
// leaves the column declared, so it is the WIDEN's case, not an erasure: the
// upgrade must still succeed, still widen, and never demand a retirement
// declaration for an edit that loses no column.
func TestUpgrade_HolderTypeNarrowingIsNotASecureColumnRetirement(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithSecureLens("0.1.0", []string{"identity", "retentionclass"}, "")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)

	res, err := inst.Upgrade(ctx, defWithSecureLens("0.2.0", []string{"identity"}, ""))
	if err != nil {
		t.Fatalf("a narrowing upgrade must still succeed (the widen handles it): %v", err)
	}
	if res.SecureColumnsWidened != 1 {
		t.Fatalf("SecureColumnsWidened = %d, want 1 (%+v)", res.SecureColumnsWidened, res)
	}
	if res.SecureColumnsRetired != 0 {
		t.Fatalf("SecureColumnsRetired = %d, want 0 — no column was erased (%+v)", res.SecureColumnsRetired, res)
	}
	if got := committedHolderTypes(t, kvDoc(t, ctx, conn, specKey), "applicant_name"); !slices.Equal(got, []string{"identity", "retentionclass"}) {
		t.Fatalf("holderTypes = %v, want the union still written by the widen", got)
	}
}

// TestUpgrade_NonLensSpecRemovalNeedsNoRetirement bounds the guard. `.spec` is
// not lens-exclusive — weaver-target and loom-pattern bodies use the same
// aspect name — but neither carries a targetConfig, so neither can hold key
// custody and neither may be made to demand a retirement declaration.
func TestUpgrade_NonLensSpecRemovalNeedsNoRetirement(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := sampleDef("0.1.0")
	v1.WeaverTargets = []WeaverTargetSpec{{
		TargetID: "sampleTarget",
		LensRef:  "sampleLens",
		Gaps:     map[string]GapActionSpec{"missing_x": {Action: "directOp", Operation: "SampleOp"}},
	}}
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	targetSpecKey := metaVertexPrefix + entityNanoID(v1.Name, "weaverTarget:sampleTarget") + ".spec"
	if del, _ := kvDoc(t, ctx, conn, targetSpecKey)["isDeleted"].(bool); del {
		t.Fatalf("%s should be live right after install", targetSpecKey)
	}

	res, err := inst.Upgrade(ctx, sampleDef("0.2.0"))
	if err != nil {
		t.Fatalf("removing a weaver target must not need a secure-column retirement: %v", err)
	}
	if res.SecureColumnsRetired != 0 {
		t.Fatalf("SecureColumnsRetired = %d, want 0 (%+v)", res.SecureColumnsRetired, res)
	}
	if del, _ := kvDoc(t, ctx, conn, targetSpecKey)["isDeleted"].(bool); !del {
		t.Fatalf("%s should be tombstoned after the target leaves the manifest", targetSpecKey)
	}
}

// TestUpgrade_BlanketRetirementDoesNotExcuseColumnDrop is the mirror of
// PerColumnRetirementDoesNotExcuseLensRemoval, and the direction that decides
// whether a retirement can outlive its edit. A Column:"" entry attests that a
// removed lens's whole spec was safe to let go. Left in the package file
// afterwards it would, under a looser rule, wave through every later erasure on
// that lens — including a column dropped from the lens once it is back — under
// a Note written about something else entirely. The selector has to match
// exactly, so a stale blanket entry goes inert instead.
func TestUpgrade_BlanketRetirementDoesNotExcuseColumnDrop(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithTwoSecureColumns("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)
	before := kvDoc(t, ctx, conn, specKey)

	v2 := defWithOneSecureColumn("0.2.0")
	v2.RetiredSecureColumns = []RetiredSecureColumn{{
		Lens: "sampleSecureLens",
		Note: "0.0.9: the old read_sample_secure table was dropped outright",
	}}
	_, err := inst.Upgrade(ctx, v2)
	if err == nil {
		t.Fatalf("a whole-spec retirement must not excuse dropping one column from a lens that survives")
	}
	// The author has a Column:"" entry in front of them, so the refusal has to
	// say why it did not apply — otherwise the guard reads as broken and the
	// next move is to delete the lens.
	if !strings.Contains(err.Error(), `Column:""`) {
		t.Fatalf("the refusal must explain why the existing whole-spec entry does not apply, got: %v", err)
	}
	if !strings.Contains(err.Error(), `Column: "applicant_email"`) {
		t.Fatalf("the refusal must name the per-column declaration to add, got: %v", err)
	}
	if after := kvDoc(t, ctx, conn, specKey); !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused upgrade must commit nothing:\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestUpgrade_SecureColumnRetirementToleratesSurroundingSpace pins that the
// validated and the MATCHED spellings of a declaration are the same one. A lens
// NanoID is salted by the raw canonicalName, so an entry trimmed for validation
// and used untrimmed for matching would validate, match nothing, and produce a
// refusal instructing the author to add a declaration identical to the one
// already in their file — a dead end with no exit that is not deleting the lens.
func TestUpgrade_SecureColumnRetirementToleratesSurroundingSpace(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithTwoSecureColumns("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)

	v2 := defWithOneSecureColumn("0.2.0")
	v2.RetiredSecureColumns = []RetiredSecureColumn{{
		Lens:   "  sampleSecureLens ",
		Column: " applicant_email\t",
		Note:   "the column's rows were shredded before this release",
	}}
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("a declaration carrying surrounding whitespace must still match its lens and column: %v", err)
	}
	if res.SecureColumnsRetired != 1 {
		t.Fatalf("SecureColumnsRetired = %d, want 1 (%+v)", res.SecureColumnsRetired, res)
	}
	if got := committedSecureColumnNames(t, kvDoc(t, ctx, conn, specKey)); !slices.Equal(got, []string{"applicant_name"}) {
		t.Fatalf("committed secure columns = %v, want the declared retirement to have landed", got)
	}
}

// TestUpgrade_UnusedSecureColumnRetirementReported covers the entry that
// excused nothing. It cannot be refused — a package may legitimately carry a
// retirement across versions after the erasure it described has landed — but
// left unreported it is indistinguishable from one still doing work, which is
// how a stale attestation ends up read as coverage for somebody else's edit.
func TestUpgrade_UnusedSecureColumnRetirementReported(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	if _, err := inst.Install(ctx, defWithTwoSecureColumns("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	v2 := defWithOneSecureColumn("0.2.0")
	v2.RetiredSecureColumns = []RetiredSecureColumn{
		{Lens: "sampleSecureLens", Column: "applicant_email", Note: "swept in 0.2.0"},
		{Lens: "sampleSecureLens", Column: "applicant_name", Note: "0.0.9: never actually written"},
		{Lens: "someOtherLens", Note: "0.0.4: that lens is long gone"},
	}
	res, err := inst.Upgrade(ctx, v2)
	if err != nil {
		t.Fatalf("Upgrade (one live retirement, two stale): %v", err)
	}
	if res.SecureColumnsRetired != 1 {
		t.Fatalf("SecureColumnsRetired = %d, want 1 — only applicant_email was erased (%+v)", res.SecureColumnsRetired, res)
	}
	want := []string{
		"sampleSecureLens / applicant_name",
		`someOtherLens / "" (the whole spec — a removed or renamed lens)`,
	}
	if !slices.Equal(res.SecureColumnRetirementsUnused, want) {
		t.Fatalf("SecureColumnRetirementsUnused = %v, want %v (declaration order, the live one absent)",
			res.SecureColumnRetirementsUnused, want)
	}
}

// TestUpgrade_SecureColumnDropOnReviveRefused covers the revive branch, which
// reaches the erasure through a different path than an ordinary body edit and
// is the branch §29 got wrong once already. A package entity key is
// deterministic in (package, kind, canonicalName), so a lens removed in one
// version and re-added in the next lands on the exact key its removal
// tombstoned — and the tombstone touched a Core KV definition, not the target
// store holding the ciphertext. A lens that comes BACK with a secure column
// missing erases that column's custody record just as surely as dropping it in
// place would, so remove-then-readd must not be a way around the guard.
func TestUpgrade_SecureColumnDropOnReviveRefused(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithTwoSecureColumns("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)

	// v2 removes the lens outright — declared, so the removal itself proceeds
	// and the spec is tombstoned with both secure columns still in its body.
	v2 := sampleDef("0.2.0")
	v2.RetiredSecureColumns = []RetiredSecureColumn{{
		Lens: "sampleSecureLens", Note: "test fixture: the lens returns in v3",
	}}
	if _, err := inst.Upgrade(ctx, v2); err != nil {
		t.Fatalf("Upgrade (remove the lens): %v", err)
	}
	tombstoned := kvDoc(t, ctx, conn, specKey)
	if del, _ := tombstoned["isDeleted"].(bool); !del {
		t.Fatalf("%s should be tombstoned after v2", specKey)
	}
	if got := committedSecureColumnNames(t, tombstoned); !slices.Equal(got, []string{"applicant_name", "applicant_email"}) {
		t.Fatalf("the tombstoned spec must still carry its secure columns (they are what the revive reads): %v", got)
	}

	// v3 re-adds the lens with applicant_email gone. The revive branch reads
	// the tombstoned spec as committed history, so this is an erasure.
	v3 := defWithOneSecureColumn("0.3.0")
	if _, err := inst.Upgrade(ctx, v3); err == nil {
		t.Fatalf("re-adding a lens without one of its committed secure columns must be refused, same as dropping it in place")
	}
	if got := committedSecureColumnNames(t, kvDoc(t, ctx, conn, specKey)); !slices.Equal(got, []string{"applicant_name", "applicant_email"}) {
		t.Fatalf("a refused revive must commit nothing, got %v", got)
	}

	// The positive vector: the same re-add, declared.
	v3.RetiredSecureColumns = []RetiredSecureColumn{{
		Lens:   "sampleSecureLens",
		Column: "applicant_email",
		Note:   "the column was shredded while the lens was out of the manifest",
	}}
	res, err := inst.Upgrade(ctx, v3)
	if err != nil {
		t.Fatalf("Upgrade (declared revive retirement): %v", err)
	}
	if res.SecureColumnsRetired != 1 {
		t.Fatalf("SecureColumnsRetired = %d, want 1 on the revive path (%+v)", res.SecureColumnsRetired, res)
	}
	revived := kvDoc(t, ctx, conn, specKey)
	if del, _ := revived["isDeleted"].(bool); del {
		t.Fatalf("%s should be live again after the re-add", specKey)
	}
	if got := committedSecureColumnNames(t, revived); !slices.Equal(got, []string{"applicant_name"}) {
		t.Fatalf("committed secure columns after the declared revive = %v, want just applicant_name", got)
	}
}

// TestDefinition_ValidateRetiredSecureColumns proves the declaration is checked
// at INSTALL, not only when some later version's erasure happens to reach it. A
// package that could ship a noteless retirement and surface it three versions
// on would be handing the diagnosis to whoever runs that upgrade rather than to
// the author who wrote it.
func TestDefinition_ValidateRetiredSecureColumns(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	for _, tc := range []struct {
		name    string
		decls   []RetiredSecureColumn
		wantErr string
	}{
		{
			name:    "no lens",
			decls:   []RetiredSecureColumn{{Column: "applicant_name", Note: "swept"}},
			wantErr: "names no Lens",
		},
		{
			name:    "no note",
			decls:   []RetiredSecureColumn{{Lens: "sampleSecureLens", Column: "applicant_name"}},
			wantErr: "empty Note",
		},
		{
			name: "duplicate pair",
			decls: []RetiredSecureColumn{
				{Lens: "sampleSecureLens", Column: "applicant_name", Note: "swept"},
				{Lens: " sampleSecureLens", Column: "applicant_name ", Note: "swept again?"},
			},
			wantErr: "twice",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := defWithSecureLens("0.1.0", []string{"identity"}, "")
			def.RetiredSecureColumns = tc.decls
			_, err := inst.Install(ctx, def)
			if err == nil {
				t.Fatalf("Install must refuse a malformed retirement declaration")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}
