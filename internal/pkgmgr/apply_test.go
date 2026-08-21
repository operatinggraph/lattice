package pkgmgr

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// applyV2 returns a v2 of sampleDef that exercises all three diff partitions:
// a changed DDL description (update), an added second lens (create), and the
// dropped permission (tombstone). Mirrors TestUpgrade_DiffCreateUpdateTombstone
// so the Apply path is proven over the same shape the Upgrade path is.
func applyV2(version string) Definition {
	v2 := sampleDef(version)
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
	return v2
}

// TestApply_FreshInstall: an absent package + default options installs it.
func TestApply_FreshInstall(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	res, err := inst.Apply(ctx, sampleDef("0.1.0"), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply (fresh): %v", err)
	}
	if res.Action != "install" || res.Skipped || res.DryRun {
		t.Fatalf("fresh install: want action=install !skipped !dryrun, got %+v", res)
	}
	if res.Created == 0 {
		t.Fatalf("fresh install reported 0 created: %+v", res)
	}
	// The package vertex landed and carries the version.
	pkg := kvDoc(t, ctx, conn, PackageVertexPrefix+entityNanoID("sample-pkg", "package"))
	if ver, _ := pkg["data"].(map[string]any)["version"].(string); ver != "0.1.0" {
		t.Fatalf("package version not recorded: got %q", ver)
	}
}

// TestApply_PermissionLanesWrittenToVertexData proves PermissionSpec.Lanes
// (scoped-privileged-lane-grants-design.md Fire 1) round-trips onto the
// permission vertex's data.
func TestApply_PermissionLanesWrittenToVertexData(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	def := sampleDef("0.1.0")
	def.Permissions[0].Lanes = []string{"meta"}
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	permKey := "vtx.permission." + entityNanoID(def.Name, permTag("SampleOp", "any"))
	perm := kvDoc(t, ctx, conn, permKey)
	data, _ := perm["data"].(map[string]any)
	lanes, ok := data["lanes"].([]any)
	if !ok || len(lanes) != 1 || lanes[0] != "meta" {
		t.Fatalf("expected data.lanes=[meta]; got %+v", data["lanes"])
	}
}

// TestApply_PermissionLanesOmittedWhenUnset proves a PermissionSpec with no
// Lanes writes no "lanes" key at all (absent, not an empty array) — today's
// default for every existing package, and what the per-op-lanes-absent
// fallback in step3_auth_capability.go's platformLaneGate depends on.
func TestApply_PermissionLanesOmittedWhenUnset(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	def := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	permKey := "vtx.permission." + entityNanoID(def.Name, permTag("SampleOp", "any"))
	perm := kvDoc(t, ctx, conn, permKey)
	data, _ := perm["data"].(map[string]any)
	if _, present := data["lanes"]; present {
		t.Fatalf("expected no lanes key when Lanes is unset; got %+v", data)
	}
}

// TestApply_SameVersionSkips: install v1, Apply v1 with no force → skip,
// preserving today's install idempotency.
func TestApply_SameVersionSkips(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}
	res, err := inst.Apply(ctx, sampleDef("0.1.0"), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply (same version): %v", err)
	}
	if !res.Skipped || res.Action != "skip" {
		t.Fatalf("same-version no-force: want skip, got %+v", res)
	}
	if res.Created != 0 || res.Updated != 0 || res.Tombstoned != 0 {
		t.Fatalf("skip produced mutations: %+v", res)
	}
}

// TestApply_SameVersionForceUpdatesInPlace: install v1, edit a DDL body, then
// Apply the SAME version with Force → an in-place update lands the edited body
// at the same key (the dev-refresh path).
func TestApply_SameVersionForceUpdatesInPlace(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	edited := sampleDef("0.1.0") // SAME version, changed body.
	edited.DDLs[0].Description = "force refreshed"

	res, err := inst.Apply(ctx, edited, ApplyOptions{Force: true})
	if err != nil {
		t.Fatalf("Apply (force same-version): %v", err)
	}
	if res.Skipped || res.Action != "upgrade" {
		t.Fatalf("force same-version: want in-place upgrade, got %+v", res)
	}
	if res.Updated == 0 {
		t.Fatalf("force same-version: want >0 updated, got %+v", res)
	}
	if res.Created != 0 || res.Tombstoned != 0 {
		t.Fatalf("force same-version body edit: want only updates, got %+v", res)
	}
	descKey := metaVertexPrefix + entityNanoID("sample-pkg", "ddl:sampleClass") + ".description"
	desc := kvDoc(t, ctx, conn, descKey)
	if txt, _ := desc["data"].(map[string]any)["text"].(string); txt != "force refreshed" {
		t.Fatalf("force did not apply the edited body in place: got %q", txt)
	}
}

// TestApply_DifferentVersionAutoUpgrades: install v1, Apply v2 (no upgrade verb,
// no flags) → the version change auto-upgrades, diff-applying create/update/
// tombstone in place and bumping the package version.
func TestApply_DifferentVersionAutoUpgrades(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}

	newLensKey := metaVertexPrefix + entityNanoID(v1.Name, "lens:sampleLens2")
	permKey := "vtx.permission." + entityNanoID(v1.Name, permTag("SampleOp", "any"))

	res, err := inst.Apply(ctx, applyV2("0.2.0"), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply (auto-upgrade): %v", err)
	}
	if res.Action != "upgrade" || res.Skipped || res.DryRun {
		t.Fatalf("auto-upgrade: want action=upgrade, got %+v", res)
	}
	if res.Created == 0 || res.Updated == 0 || res.Tombstoned == 0 {
		t.Fatalf("auto-upgrade: want non-zero create/update/tombstone, got %+v", res)
	}

	// Create landed live; tombstone soft-deleted; version bumped in place.
	newLens := kvDoc(t, ctx, conn, newLensKey)
	if del, _ := newLens["isDeleted"].(bool); del {
		t.Fatalf("new lens %s should be live", newLensKey)
	}
	perm := kvDoc(t, ctx, conn, permKey)
	if del, _ := perm["isDeleted"].(bool); !del {
		t.Fatalf("dropped permission %s should be tombstoned", permKey)
	}
	pkg := kvDoc(t, ctx, conn, PackageVertexPrefix+entityNanoID(v1.Name, "package"))
	if ver, _ := pkg["data"].(map[string]any)["version"].(string); ver != "0.2.0" {
		t.Fatalf("package version not bumped: got %q", ver)
	}
}

// TestApply_DryRunDoesNotSubmit: a dry-run on a real version change reports the
// full delta + affected keys but submits nothing — Core KV is unchanged (the
// version stays v1 and the new lens key is absent).
func TestApply_DryRunDoesNotSubmit(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	newLensKey := metaVertexPrefix + entityNanoID(v1.Name, "lens:sampleLens2")

	res, err := inst.Apply(ctx, applyV2("0.2.0"), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply (dry-run): %v", err)
	}
	if !res.DryRun || res.Action != "upgrade" {
		t.Fatalf("dry-run: want dryRun upgrade preview, got %+v", res)
	}
	if res.Created == 0 || res.Updated == 0 || res.Tombstoned == 0 {
		t.Fatalf("dry-run: want a non-empty previewed delta, got %+v", res)
	}
	if len(res.CreatedKeys) != res.Created || len(res.UpdatedKeys) != res.Updated || len(res.TombstonedKeys) != res.Tombstoned {
		t.Fatalf("dry-run key lists must match the counts: %+v", res)
	}

	// Nothing was submitted: the new lens key is absent and the version is still v1.
	if _, err := conn.KVGet(ctx, CoreBucket, newLensKey); err == nil {
		t.Fatalf("dry-run wrote the new lens %s — it must not submit", newLensKey)
	}
	pkg := kvDoc(t, ctx, conn, PackageVertexPrefix+entityNanoID(v1.Name, "package"))
	if ver, _ := pkg["data"].(map[string]any)["version"].(string); ver != "0.1.0" {
		t.Fatalf("dry-run mutated the version: got %q, want 0.1.0", ver)
	}
}

// TestApply_ForceRespectsOutOfBandRevocation exercises Apply's Force
// same-version in-place branch — the actual `make reinstall-package` /
// `refresh-<vertical>` trigger the design names — against an out-of-band
// tombstone on a surviving permission. Mirrors
// TestApply_SameVersionForceUpdatesInPlace's harness shape but asserts the
// revocation is respected rather than a body edit landing.
func TestApply_ForceRespectsOutOfBandRevocation(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	v1 := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	permKey := "vtx.permission." + entityNanoID(v1.Name, permTag("SampleOp", "any"))
	tombstoneOutOfBand(t, ctx, conn, permKey)

	// Same version, Force, same declared permission — the reinstall/refresh
	// path with nothing else to change.
	res, err := inst.Apply(ctx, sampleDef("0.1.0"), ApplyOptions{Force: true})
	if err != nil {
		t.Fatalf("Apply (force same-version): %v", err)
	}
	if res.RevocationsRespected != 1 {
		t.Fatalf("RevocationsRespected = %d, want 1: %+v", res.RevocationsRespected, res)
	}
	if !res.Skipped || res.Reason == "" {
		t.Fatalf("a run with nothing else to change must report skipped with a non-empty reason naming the respected revocation: %+v", res)
	}

	after := kvDoc(t, ctx, conn, permKey)
	if del, _ := after["isDeleted"].(bool); !del {
		t.Fatalf("%s should stay tombstoned across the force refresh: %+v", permKey, after)
	}
}

// TestApply_DryRunFreshInstall: a dry-run of an absent package previews the
// full create batch without installing it.
func TestApply_DryRunFreshInstall(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	res, err := inst.Apply(ctx, sampleDef("0.1.0"), ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Apply (dry-run fresh): %v", err)
	}
	if !res.DryRun || res.Action != "install" {
		t.Fatalf("dry-run fresh: want dryRun install preview, got %+v", res)
	}
	if res.Created == 0 || len(res.CreatedKeys) != res.Created {
		t.Fatalf("dry-run fresh: want a previewed create batch, got %+v", res)
	}
	// The package is NOT installed.
	got, err := inst.findInstalledPackage(ctx, "sample-pkg")
	if err != nil {
		t.Fatalf("findInstalledPackage: %v", err)
	}
	if got != nil {
		t.Fatalf("dry-run fresh install actually installed the package: %+v", got)
	}
}

// TestApply_OverUninstalledPackageRefuses covers both fresh-install branches
// Apply reaches when no LIVE manifest carries the name but the package's own
// keys are still occupied by its uninstall's tombstones.
//
// The dry-run branch never calls Install, so it needs the gate of its own: left
// ungated it previews "install, N keys created" for a batch that cannot create
// one of them. The real branch (including `--force`, which routes here too
// because the dispatch is on `existing == nil`, not on Force) inherits the gate
// through Install.
func TestApply_OverUninstalledPackageRefuses(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")

	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := inst.Uninstall(ctx, def.Name); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	t.Run("DryRun", func(t *testing.T) {
		res, err := inst.Apply(ctx, def, ApplyOptions{DryRun: true})
		if err == nil {
			t.Fatalf("dry-run over an uninstalled package must refuse, got a preview: %+v", res)
		}
		if !errors.Is(err, ErrDeclaredKeysOccupied) {
			t.Fatalf("want ErrDeclaredKeysOccupied, got %v", err)
		}
		// No ApplyResult at all: a caller reading Created / CreatedKeys must have
		// nothing there to read as a clean preview.
		if res != nil {
			t.Fatalf("a refused preview must return no ApplyResult, got %+v", res)
		}
	})

	t.Run("Force", func(t *testing.T) {
		res, err := inst.Apply(ctx, def, ApplyOptions{Force: true})
		if err == nil {
			t.Fatalf("a forced apply over an uninstalled package must refuse, got: %+v", res)
		}
		if !errors.Is(err, ErrDeclaredKeysOccupied) {
			t.Fatalf("want ErrDeclaredKeysOccupied, got %v", err)
		}
	})
}

// TestApply_DryRunOverLiveOccupantRefuses is the preview path's other bucket: a
// declared key held by a LIVE document, with nothing tombstoned anywhere. The
// preview's gate has to consult both buckets — a gate reading only the
// tombstoned one previews a create batch whose very first CreateOnly assertion
// the commit rejects.
func TestApply_DryRunOverLiveOccupantRefuses(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")

	perm := def.Permissions[0]
	occupied := "vtx.permission." + entityNanoID(def.Name, permTag(perm.OperationType, perm.Scope))
	raw, err := json.Marshal(map[string]any{
		"class":     "permission",
		"isDeleted": false,
		"data":      map[string]any{"operationType": perm.OperationType, "scope": perm.Scope, "origin": "runtime"},
	})
	if err != nil {
		t.Fatalf("marshal live occupant: %v", err)
	}
	if _, err := conn.KVCreate(ctx, CoreBucket, occupied, raw); err != nil {
		t.Fatalf("seed live occupant %s: %v", occupied, err)
	}

	res, err := inst.Apply(ctx, def, ApplyOptions{DryRun: true})
	if err == nil {
		t.Fatalf("dry-run over a live occupant must refuse, got a preview: %+v", res)
	}
	if res != nil {
		t.Fatalf("a refused preview must return no ApplyResult, got %+v", res)
	}
	var occ *DeclaredKeysOccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("want *DeclaredKeysOccupiedError, got %T (%v)", err, err)
	}
	if !slices.Equal(occ.Live, []string{occupied}) {
		t.Errorf("Live = %v, want exactly [%s]", occ.Live, occupied)
	}
	if len(occ.Tombstoned) != 0 {
		t.Errorf("Tombstoned = %v, want empty — nothing here was ever uninstalled", occ.Tombstoned)
	}
}

// TestApply_RequireInstalledOnAbsent: the explicit `upgrade` command semantics
// (RequireInstalled) error on an absent base rather than creating it.
func TestApply_RequireInstalledOnAbsent(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	_, err := inst.Apply(ctx, sampleDef("0.1.0"), ApplyOptions{RequireInstalled: true})
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("RequireInstalled on absent: want ErrNotInstalled, got %v", err)
	}
}

// TestApply_ForceNoBodyChangeSkips: a same-version force with no body edits
// collapses to skip via the body-equality diff (nothing to re-apply).
func TestApply_ForceNoBodyChangeSkips(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// Same version, force, but no body edits → the diff is empty → skip.
	res, err := inst.Apply(ctx, sampleDef("0.1.0"), ApplyOptions{Force: true})
	if err != nil {
		t.Fatalf("Apply (force, no change): %v", err)
	}
	if !res.Skipped || res.Action != "skip" {
		t.Fatalf("force with no body change: want skip via body-equality, got %+v", res)
	}
}

// TestApply_UndeclaredSecureColumnDropRefused pins the SECOND enforcement
// point. Apply and Upgrade share computeDeltaAgainst, so a guard wired only
// into Upgrade would be bypassable through everything that reaches the
// in-place path here — `lattice-pkg install`/`upgrade` and Loupe's
// POST /api/packages/apply among them.
//
// The dry run is refused too: the guard is pure, so a preview can say the real
// apply would refuse instead of previewing a delta that cannot commit.
func TestApply_UndeclaredSecureColumnDropRefused(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithTwoSecureColumns("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	specKey := secureLensSpecKey(v1)
	before := kvDoc(t, ctx, conn, specKey)

	v2 := defWithOneSecureColumn("0.2.0")
	if _, err := inst.Apply(ctx, v2, ApplyOptions{DryRun: true}); err == nil {
		t.Fatalf("a dry-run apply must report the refusal, not preview a delta that cannot commit")
	}
	if _, err := inst.Apply(ctx, v2, ApplyOptions{}); err == nil {
		t.Fatalf("Apply must refuse an undeclared secure-column erasure, same as Upgrade")
	}
	if after := kvDoc(t, ctx, conn, specKey); !reflect.DeepEqual(before, after) {
		t.Fatalf("a refused apply must commit nothing:\nbefore %+v\nafter  %+v", before, after)
	}

	v2.RetiredSecureColumns = []RetiredSecureColumn{{
		Lens:   "sampleSecureLens",
		Column: "applicant_email",
		Note:   "the column's rows were shredded before this release",
	}}
	res, err := inst.Apply(ctx, v2, ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply (declared retirement): %v", err)
	}
	if res.SecureColumnsRetired != 1 {
		t.Fatalf("SecureColumnsRetired = %d, want 1 (%+v)", res.SecureColumnsRetired, res)
	}
	if got := committedSecureColumnNames(t, kvDoc(t, ctx, conn, specKey)); !slices.Equal(got, []string{"applicant_name"}) {
		t.Fatalf("committed secure columns = %v, want the declared retirement to have landed", got)
	}
}
