package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
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
	// The receipt names the commit this Apply actually produced. Asserting the
	// Contract #4 tracker key resolves is what makes it a receipt rather than a
	// non-empty string: the tracker entry is written by the same atomic batch
	// as the install, so a value that addresses one cannot have been invented
	// by the caller. Consumers gate on this field being non-empty before they
	// record provenance, so an unpopulated one disables them silently.
	if res.InstallRequestID == "" {
		t.Fatal("fresh install: InstallRequestID empty — the reply's requestId never reached ApplyResult")
	}
	kvDoc(t, ctx, conn, processor.TrackerKey(res.InstallRequestID))
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
	// A skip committed nothing, so it has no receipt to carry. Consumers read a
	// populated InstallRequestID as "an install landed and here is which one";
	// carrying one here would name a commit this call never made.
	if res.InstallRequestID != "" {
		t.Fatalf("skip carries a receipt for a commit it never made: %q", res.InstallRequestID)
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
	// Same receipt assertion as the fresh-install arm: the in-place upgrade
	// reaches the Processor by a different submit path, so it needs its own.
	if res.InstallRequestID == "" {
		t.Fatal("auto-upgrade: InstallRequestID empty — the reply's requestId never reached ApplyResult")
	}
	kvDoc(t, ctx, conn, processor.TrackerKey(res.InstallRequestID))
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
	// A dry-run submits nothing, so it has no receipt: a preview that named a
	// commit would be describing a write that never happened.
	if res.InstallRequestID != "" {
		t.Fatalf("dry-run carries a receipt for a commit it never made: %q", res.InstallRequestID)
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
	// A dry-run submits nothing, so it has no receipt: a preview that named a
	// commit would be describing a write that never happened.
	if res.InstallRequestID != "" {
		t.Fatalf("dry-run carries a receipt for a commit it never made: %q", res.InstallRequestID)
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
	if _, err := inst.Uninstall(ctx, def.Name, UninstallOptions{}); err != nil {
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

// removalFixtureDef returns the multi-entity package the RefuseRemovals tests
// shrink: sampleDef's DDL and lens plus a second lens, a declared role, and the
// permission granted by that role — so the installed declared set spans
// meta-vertices, a topology vertex, a role index and a link, and a refusal that
// only ever saw one entity kind could not pass. Built as its own definition
// rather than as an edit of sampleDef, which every other test here shares.
func removalFixtureDef(version string) Definition {
	def := sampleDef(version)
	def.Name = "removal-fixture-pkg"
	def.Roles = []RoleSpec{{
		CanonicalName: "sampleReviewer",
		Description:   "Reviews sample entities.",
	}}
	def.Permissions[0].GrantsTo = []string{"sampleReviewer"}
	def.Lenses = append(def.Lenses, LensSpec{
		CanonicalName: "sampleLens2",
		Class:         "meta.lens",
		Adapter:       "nats-kv",
		Bucket:        "sample-bucket-2",
		Engine:        "full",
		Spec:          `MATCH (n:sample2) RETURN n.key AS key`,
	})
	return def
}

// removalShrunkenDef is a PARTIAL description of removalFixtureDef's package:
// the same name at a new version, carrying only the second lens. This is the
// shape an AI-authored capability proposal submits — its own artifact and
// nothing else about the package it names — and against the convergence
// semantics of Apply's in-place branch it reads as "retire everything else".
func removalShrunkenDef(version string) Definition {
	full := removalFixtureDef(version)
	return Definition{
		Name:    full.Name,
		Version: version,
		Lenses:  []LensSpec{full.Lenses[1]},
	}
}

// coreKVSnapshot reads every Core KV key and its raw value, so a test can
// assert that a refused apply committed nothing at all rather than only that
// the one key it thought to check survived.
func coreKVSnapshot(t *testing.T, ctx context.Context, conn *substrate.Conn) map[string]string {
	t.Helper()
	keys, err := conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		t.Fatalf("KVListKeys: %v", err)
	}
	snap := make(map[string]string, len(keys))
	for _, k := range keys {
		entry, err := conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			t.Fatalf("KVGet %s: %v", k, err)
		}
		snap[k] = string(entry.Value)
	}
	return snap
}

// TestApply_RefuseRemovals_AdmitsCoveringDefinition is the positive vector: a
// Definition that covers every declared key still applies with the option set,
// so the refusals below are proven to be about coverage rather than about the
// option disabling the in-place branch outright. The edit is one lens's cypher
// body on the SAME version under Force — the smallest real in-place delta this
// package can produce.
func TestApply_RefuseRemovals_AdmitsCoveringDefinition(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := removalFixtureDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}

	covering := removalFixtureDef("0.1.0")
	covering.Lenses[0].Spec = `MATCH (n:sample) RETURN n.key AS key, n.extra AS extra`

	res, err := inst.Apply(ctx, covering, ApplyOptions{Force: true, RefuseRemovals: true})
	if err != nil {
		t.Fatalf("a covering Definition must apply with RefuseRemovals set: %v", err)
	}
	if res.Action != "upgrade" || res.Skipped {
		t.Fatalf("covering apply: want an in-place upgrade, got %+v", res)
	}
	if res.Updated != 1 || res.Tombstoned != 0 || res.Created != 0 {
		t.Fatalf("covering apply: want exactly the edited lens spec updated, got %+v", res)
	}
	specKey := metaVertexPrefix + LensID(v1.Name, "sampleLens") + ".spec"
	spec, _ := kvDoc(t, ctx, conn, specKey)["data"].(map[string]any)
	if rule, _ := spec["cypherRule"].(string); rule != covering.Lenses[0].Spec {
		t.Fatalf("the covering apply did not land the edited lens body: got %q", rule)
	}
}

// TestApply_RefuseRemovals_ZeroValueStillConverges is the second positive
// vector: ApplyOptions' zero value keeps the whole-Definition convergence
// semantics every source-authored install/upgrade depends on. The same
// shrinking Definition that the option refuses below tombstones here, and the
// dropped keys really are tombstoned in Core KV — asserted rather than assumed,
// because the option's blast radius is exactly this default.
func TestApply_RefuseRemovals_ZeroValueStillConverges(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := removalFixtureDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	permKey := "vtx.permission." + entityNanoID(v1.Name, permTag("SampleOp", "any"))
	roleKey := "vtx.role." + RoleID(v1.Name, "sampleReviewer")

	res, err := inst.Apply(ctx, removalShrunkenDef("0.2.0"), ApplyOptions{})
	if err != nil {
		t.Fatalf("Apply (zero value, shrinking): %v", err)
	}
	if res.Tombstoned == 0 {
		t.Fatalf("the zero value must still converge — a shrinking Definition tombstones: %+v", res)
	}
	for _, k := range []string{permKey, roleKey} {
		if del, _ := kvDoc(t, ctx, conn, k)["isDeleted"].(bool); !del {
			t.Fatalf("%s should be tombstoned by the default convergence path", k)
		}
	}
	survivor := metaVertexPrefix + LensID(v1.Name, "sampleLens2")
	if del, _ := kvDoc(t, ctx, conn, survivor)["isDeleted"].(bool); del {
		t.Fatalf("%s is the one entity the shrunken Definition describes; it must stay live", survivor)
	}
}

// TestApply_RefuseRemovals_RefusesShrunkenDefinition is the defect itself: a
// partial Definition applied over a package it does not describe. The
// assertion that pins it is that Core KV is byte-unchanged afterwards — an
// error string proves only that something was said, not that nothing was done.
func TestApply_RefuseRemovals_RefusesShrunkenDefinition(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := removalFixtureDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	pkgKey := PackageVertexPrefix + entityNanoID(v1.Name, "package")
	declared, err := inst.readDeclaredKeys(ctx, pkgKey)
	if err != nil {
		t.Fatalf("readDeclaredKeys: %v", err)
	}
	before := coreKVSnapshot(t, ctx, conn)

	res, err := inst.Apply(ctx, removalShrunkenDef("0.2.0"), ApplyOptions{RefuseRemovals: true})
	if err == nil {
		t.Fatalf("a shrinking Definition must be refused, got: %+v", res)
	}
	if res != nil {
		t.Fatalf("a refused apply must return no ApplyResult, got %+v", res)
	}
	if !errors.Is(err, ErrApplyWouldRemove) {
		t.Fatalf("want ErrApplyWouldRemove, got %v", err)
	}
	var refusal *ApplyWouldRemoveError
	if !errors.As(err, &refusal) {
		t.Fatalf("want *ApplyWouldRemoveError, got %T (%v)", err, err)
	}

	// Nothing committed: the whole bucket, not just the keys this test thought
	// to name.
	if after := coreKVSnapshot(t, ctx, conn); !maps.Equal(before, after) {
		t.Fatalf("a refused apply must commit nothing; %d keys before, %d after, and the values differ", len(before), len(after))
	}

	// The counts are the diagnosis, and they count KEYS (package root and
	// manifest aspect included), not entities.
	if refusal.PackageName != v1.Name {
		t.Errorf("PackageName = %q, want %q", refusal.PackageName, v1.Name)
	}
	if refusal.DeclaredKeys != len(declared) {
		t.Errorf("DeclaredKeys = %d, want %d (the installed manifest's declaredKeys)", refusal.DeclaredKeys, len(declared))
	}
	if refusal.DescribedKeys >= refusal.DeclaredKeys {
		t.Errorf("DescribedKeys = %d, DeclaredKeys = %d: a shrinking Definition describes strictly fewer",
			refusal.DescribedKeys, refusal.DeclaredKeys)
	}

	// The removed set is read from the field, never scraped from the message —
	// which names only the first few keys anyway.
	if !slices.IsSorted(refusal.UndescribedKeys) {
		t.Errorf("RemovedKeys must be sorted, got %v", refusal.UndescribedKeys)
	}
	permID := entityNanoID(v1.Name, permTag("SampleOp", "any"))
	roleID := RoleID(v1.Name, "sampleReviewer")
	for _, want := range []string{
		metaVertexPrefix + LensID(v1.Name, "sampleLens"),
		"vtx.role." + roleID,
		"vtx.permission." + permID,
		"lnk.permission." + permID + ".grantedBy.role." + roleID,
	} {
		if !slices.Contains(refusal.UndescribedKeys, want) {
			t.Errorf("RemovedKeys is missing %s: %v", want, refusal.UndescribedKeys)
		}
	}
	for _, kept := range []string{
		metaVertexPrefix + LensID(v1.Name, "sampleLens2"),
		metaVertexPrefix + LensID(v1.Name, "sampleLens2") + ".spec",
	} {
		if slices.Contains(refusal.UndescribedKeys, kept) {
			t.Errorf("%s is described by the submitted Definition and must not be a removal: %v", kept, refusal.UndescribedKeys)
		}
	}
	for _, k := range refusal.UndescribedKeys {
		if !slices.Contains(declared, k) {
			t.Errorf("RemovedKeys names %s, which the installed package never declared", k)
		}
	}
}

// TestApply_RefuseRemovals_DryRunRefusesIdentically pins the guard's placement:
// it sits before the dry-run return, so a preview whose real run would be
// refused says so rather than describing a batch that cannot commit. Same
// typed error, same removed set.
func TestApply_RefuseRemovals_DryRunRefusesIdentically(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	if _, err := inst.Install(ctx, removalFixtureDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}
	shrunken := removalShrunkenDef("0.2.0")

	res, err := inst.Apply(ctx, shrunken, ApplyOptions{RefuseRemovals: true, DryRun: true})
	if err == nil {
		t.Fatalf("a preview of a refused apply must refuse, got: %+v", res)
	}
	if res != nil {
		t.Fatalf("a refused preview must return no ApplyResult, got %+v", res)
	}
	var preview *ApplyWouldRemoveError
	if !errors.As(err, &preview) {
		t.Fatalf("want *ApplyWouldRemoveError from the preview, got %T (%v)", err, err)
	}

	real, err := inst.Apply(ctx, shrunken, ApplyOptions{RefuseRemovals: true})
	if err == nil {
		t.Fatalf("the real run must refuse too, got: %+v", real)
	}
	var committed *ApplyWouldRemoveError
	if !errors.As(err, &committed) {
		t.Fatalf("want *ApplyWouldRemoveError from the real run, got %T (%v)", err, err)
	}
	if !slices.Equal(preview.UndescribedKeys, committed.UndescribedKeys) {
		t.Fatalf("the preview must refuse identically to the real run:\npreview %v\nreal    %v",
			preview.UndescribedKeys, committed.UndescribedKeys)
	}
	if preview.Error() != committed.Error() {
		t.Fatalf("the preview's refusal must read identically:\npreview %q\nreal    %q", preview.Error(), committed.Error())
	}
}

// TestApply_RefuseRemovals_UndeclaredRetentionHolderRefuses pins the shape a
// tombstone-counting guard would wave through.
//
// A dropped retention-class holder is never tombstoned — only
// ShredRetentionClassKey may destroy its DEK, so the removal arm leaves it live
// (upgrade.go's retention exemption). Nothing is removed, and that is precisely
// why the emitted mutation list is the wrong thing to test: the same apply
// rewrites the manifest's declaredKeys from the submitted Definition, so the
// holder ends up LIVE AND UNDECLARED — a class key still sitting in Core KV
// with nothing recording that this package custodies it, and no shred verb able
// to find it by way of the package. A partial Definition has said nothing about
// that custody, so it may not end it.
//
// The positive vector below is the same drop under the zero value, which must
// still converge exactly as it always has: preserved, not tombstoned, and the
// declaration narrowed on purpose by a source-authored package that describes
// the whole thing.
func TestApply_RefuseRemovals_UndeclaredRetentionHolderRefuses(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := defWithRetentionClass("0.1.0", "sampleClass1")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	holderKey := RetentionClassKey(v1.Name, "sampleClass1")
	policyKey := holderKey + ".retentionPolicy"

	// v2 covers every declared key EXCEPT the retention class's two.
	res, err := inst.Apply(ctx, sampleDef("0.2.0"), ApplyOptions{RefuseRemovals: true})
	if err == nil {
		t.Fatalf("a Definition that stops declaring a retention-class holder must be refused, got: %+v", res)
	}
	var refusal *ApplyWouldRemoveError
	if !errors.As(err, &refusal) {
		t.Fatalf("want *ApplyWouldRemoveError, got %T (%v)", err, err)
	}
	if !slices.Equal(refusal.UndescribedKeys, []string{holderKey, policyKey}) {
		t.Fatalf("UndescribedKeys = %v, want exactly the holder root and its policy aspect — the keys that would be undeclared without ever being tombstoned",
			refusal.UndescribedKeys)
	}

	// The positive vector: the same drop under the zero value still converges,
	// preserving the holder rather than tombstoning it.
	zeroRes, err := inst.Apply(ctx, sampleDef("0.2.0"), ApplyOptions{})
	if err != nil {
		t.Fatalf("the source-authored path must be unchanged: %v", err)
	}
	if zeroRes.Tombstoned != 0 {
		t.Fatalf("dropping a retention class must emit no tombstone, got %d (%+v)", zeroRes.Tombstoned, zeroRes)
	}
	if zeroRes.RetentionHoldersPreserved != 2 {
		t.Fatalf("RetentionHoldersPreserved = %d, want 2 (holder root + .retentionPolicy) (%+v)", zeroRes.RetentionHoldersPreserved, zeroRes)
	}
	for _, k := range []string{holderKey, policyKey} {
		if del, _ := kvDoc(t, ctx, conn, k)["isDeleted"].(bool); del {
			t.Fatalf("%s must stay live so ShredRetentionClassKey can still destroy the class key", k)
		}
	}
}

// TestApply_RefuseRemovals_AlreadyTombstonedDeclaredKeyStillRefuses covers the
// state table row the clause decides least obviously: a declared key the
// submitted Definition drops that is ALREADY tombstoned in Core KV, so
// tombstoning it again would change nothing.
//
// It refuses anyway, and that is deliberate. The guard's question is what the
// submitter DESCRIBED, not what happens to be live: a Definition that omits a
// key its package declares is a partial description either way, and admitting
// this one would make the refusal depend on KV liveness — the same apply would
// be refused on Monday and admitted on Tuesday because somebody revoked a
// permission in between, and the author would learn nothing about the coverage
// problem that is still there. The removal is a no-op; the misdescription is
// not.
func TestApply_RefuseRemovals_AlreadyTombstonedDeclaredKeyStillRefuses(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := removalFixtureDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// A non-retention declared key, killed by a direct KV write the way
	// RevokePermission would — never through Apply, which would rewrite
	// declaredKeys and leave the key undeclared instead of dead-but-declared.
	permKey := "vtx.permission." + entityNanoID(v1.Name, permTag("SampleOp", "any"))
	tombstoneOutOfBand(t, ctx, conn, permKey)
	if del, _ := kvDoc(t, ctx, conn, permKey)["isDeleted"].(bool); !del {
		t.Fatalf("%s must be tombstoned before the apply for this row to mean anything", permKey)
	}

	res, err := inst.Apply(ctx, removalShrunkenDef("0.2.0"), ApplyOptions{RefuseRemovals: true})
	if err == nil {
		t.Fatalf("a Definition that omits a declared key must be refused whether or not the key is live, got: %+v", res)
	}
	var refusal *ApplyWouldRemoveError
	if !errors.As(err, &refusal) {
		t.Fatalf("want *ApplyWouldRemoveError, got %T (%v)", err, err)
	}
	if !slices.Contains(refusal.UndescribedKeys, permKey) {
		t.Fatalf("RemovedKeys must name the already-tombstoned %s — the diff still re-emits it, and the Definition still fails to describe it: %v",
			permKey, refusal.UndescribedKeys)
	}
}

// TestApply_RefuseRemovals_RevivingATombstonedDescribedKeyRefuses is the
// coverage rule read in the other direction.
//
// A key the Definition DOES describe, whose committed document an operator
// tombstoned out of band, is un-tombstoned by the diff's ordinary body-update
// path — for definition keys, deliberately, because re-declaring a definition is
// how a package brings one back. That reasoning belongs to a package converging
// on its own source. A caller describing one artifact of somebody else's package
// has said nothing about the operator's decision to kill that key, so it may not
// reverse it. Refusing the no-op re-tombstone of an undescribed key while
// silently resurrecting a described one would be the same rule applied in
// opposite directions.
func TestApply_RefuseRemovals_RevivingATombstonedDescribedKeyRefuses(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	v1 := removalFixtureDef("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// A DESCRIBED definition key — the lens the shrunken Definition keeps —
	// killed the way an operator would, and given a body edit so the update is
	// actually emitted rather than skipped as byte-equal.
	specKey := metaVertexPrefix + LensID(v1.Name, "sampleLens2") + ".spec"
	tombstoneOutOfBand(t, ctx, conn, specKey)

	covering := removalFixtureDef("0.1.0")
	covering.Lenses[1].Spec = `MATCH (n:sample2) RETURN n.key AS key, n.extra AS extra`
	before := coreKVSnapshot(t, ctx, conn)

	res, err := inst.Apply(ctx, covering, ApplyOptions{Force: true, RefuseRemovals: true})
	if err == nil {
		t.Fatalf("reviving a key an operator tombstoned must be refused, got: %+v", res)
	}
	var revival *ApplyWouldReviveError
	if !errors.As(err, &revival) {
		t.Fatalf("want *ApplyWouldReviveError, got %T (%v)", err, err)
	}
	if !slices.Equal(revival.RevivedKeys, []string{specKey}) {
		t.Fatalf("RevivedKeys = %v, want exactly [%s]", revival.RevivedKeys, specKey)
	}
	if after := coreKVSnapshot(t, ctx, conn); !maps.Equal(before, after) {
		t.Fatal("a refused apply must commit nothing")
	}

	// The positive vector: the zero value still revives, which is the
	// source-authored behaviour this refusal deliberately does not change.
	zeroRes, err := inst.Apply(ctx, covering, ApplyOptions{Force: true})
	if err != nil {
		t.Fatalf("the source-authored path must still revive: %v", err)
	}
	if zeroRes.Updated == 0 {
		t.Fatalf("want the revival to land as an update under the zero value, got %+v", zeroRes)
	}
	if del, _ := kvDoc(t, ctx, conn, specKey)["isDeleted"].(bool); del {
		t.Fatalf("%s should have been revived by the source-authored apply", specKey)
	}
}

// TestApply_RefuseRemovals_SingleUndescribedKeyRefuses pins the predicate's
// boundary at one, not at "several". Every other fixture here drops a large
// handful of keys, so a guard that refused only on two-or-more would pass all of
// them while admitting the smallest real misdescription there is — and the
// smallest is the one an author is most likely to make.
//
// The one-key drop is a weaver target's optional `.description` aspect, which
// the builder emits only when the field is non-empty. Everything else about the
// two Definitions is identical, so the set difference is exactly that key.
func TestApply_RefuseRemovals_SingleUndescribedKeyRefuses(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	described := weaverTargetDef("one-key-pkg", "okClass", "okLens", "okTarget", "0.1.0")
	described.WeaverTargets[0].Description = "the target an operator reads about"
	if _, err := inst.Install(ctx, described); err != nil {
		t.Fatalf("Install: %v", err)
	}

	dropped := weaverTargetDef("one-key-pkg", "okClass", "okLens", "okTarget", "0.2.0")
	res, err := inst.Apply(ctx, dropped, ApplyOptions{RefuseRemovals: true})
	if err == nil {
		t.Fatalf("a Definition short by ONE declared key must still be refused, got: %+v", res)
	}
	var refusal *ApplyWouldRemoveError
	if !errors.As(err, &refusal) {
		t.Fatalf("want *ApplyWouldRemoveError, got %T (%v)", err, err)
	}
	if len(refusal.UndescribedKeys) != 1 {
		t.Fatalf("this fixture must drop exactly one key or it does not pin the boundary; got %d: %v",
			len(refusal.UndescribedKeys), refusal.UndescribedKeys)
	}
	if !strings.HasSuffix(refusal.UndescribedKeys[0], ".description") {
		t.Fatalf("UndescribedKeys = %v, want the dropped weaver-target description aspect", refusal.UndescribedKeys)
	}

	// The exact-value assertion the counts otherwise never get: they are the
	// stated diagnosis, so an off-by-one in either is a wrong diagnosis.
	if refusal.DescribedKeys != refusal.DeclaredKeys-1 {
		t.Fatalf("DeclaredKeys=%d DescribedKeys=%d: dropping one key must move exactly one count by exactly one",
			refusal.DeclaredKeys, refusal.DescribedKeys)
	}
	if refusal.DeclaredKeys != len(refusal.UndescribedKeys)+refusal.DescribedKeys {
		t.Fatalf("the counts must reconcile: %d declared != %d undescribed + %d described",
			refusal.DeclaredKeys, len(refusal.UndescribedKeys), refusal.DescribedKeys)
	}
}

// TestApply_RefuseRemovals_CoveringDefinitionCountsMatch proves the claim the
// counts' own documentation makes and no refusal can demonstrate: a covering
// Definition describes exactly as many keys as the package declares. A covering
// apply never refuses, so it never produces an error carrying the pair — the
// diff summary is read directly instead, which is the only place the equality
// is observable.
func TestApply_RefuseRemovals_CoveringDefinitionCountsMatch(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	def := removalFixtureDef("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}
	existing, err := inst.findInstalledPackage(ctx, def.Name)
	if err != nil {
		t.Fatalf("findInstalledPackage: %v", err)
	}
	covering := removalFixtureDef("0.1.0")
	covering.Lenses[0].Spec = `MATCH (n:sample) RETURN n.key AS key, n.extra AS extra`
	_, sum, _, err := inst.computeDeltaAgainst(ctx, existing, covering)
	if err != nil {
		t.Fatalf("computeDeltaAgainst: %v", err)
	}
	if sum.oldKeyCount != sum.newKeyCount {
		t.Fatalf("a covering Definition must report the same key count on both sides; declared=%d described=%d",
			sum.oldKeyCount, sum.newKeyCount)
	}
	if len(sum.undescribedKeys) != 0 {
		t.Fatalf("a covering Definition leaves nothing undescribed, got %v", sum.undescribedKeys)
	}
}

// TestApply_RefuseRemovals_OutranksTheSecureColumnGuard pins the order the two
// guards run in, which decides which refusal an operator is handed.
//
// Both are fed by the same dropped-key set, so a partial Definition that drops a
// Secure Lens trips both. The secure-column guard's remedy is to declare a
// RetiredSecureColumn — an attestation that the ciphertext those columns
// encrypted is safe to stop tracking. A caller describing one artifact of
// somebody else's package cannot make that attestation and has no standing to:
// it is not their ciphertext. Answering them with it sends them to a dead end
// and, worse, invites the one author who CAN write it to write it for a removal
// nobody intended.
//
// The coverage refusal is the harder boundary — the apply must not happen at
// all, by any attestation — so it answers first. The source-authored path is
// untouched: with RefuseRemovals unset the coverage guard is a no-op and the
// retirement guard still owns the refusal, which the assertion below pins so
// that reordering cannot quietly disarm it.
func TestApply_RefuseRemovals_OutranksTheSecureColumnGuard(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	v1 := defWithTwoSecureColumns("0.1.0")
	if _, err := inst.Install(ctx, v1); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// A partial Definition: the secure lens is not described at all, so its
	// spec — and the custody record on it — is among the keys being dropped.
	partial := Definition{Name: v1.Name, Version: "0.2.0"}
	_, err := inst.Apply(ctx, partial, ApplyOptions{RefuseRemovals: true})
	if !errors.Is(err, ErrApplyWouldRemove) {
		t.Fatalf("a partial Definition must get the coverage refusal, not an attestation demand it cannot satisfy; got %v", err)
	}
	if errors.Is(err, ErrUndeclaredSecureColumnDrop) {
		t.Fatalf("the secure-column guard answered first: %v", err)
	}

	// The source-authored path still gets the retirement guard, unchanged.
	sourceAuthored := defWithOneSecureColumn("0.2.0")
	_, err = inst.Apply(ctx, sourceAuthored, ApplyOptions{})
	if !errors.Is(err, ErrUndeclaredSecureColumnDrop) {
		t.Fatalf("a source-authored erasure must still be refused by the retirement guard; got %v", err)
	}
}
