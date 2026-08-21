package pkgmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// injectLegacyManifest writes a bare `vtx.package.<id>.manifest` aspect
// directly to core-kv, bypassing Install and its validatePackageName guard —
// simulating a manifest that landed before that guard existed (or any other
// out-of-band write), the only way a denormalized stored name can exist
// today. Returns the package vertex key.
func injectLegacyManifest(t *testing.T, ctx context.Context, conn *substrate.Conn, id, name, version string) string {
	t.Helper()
	pkgKey := PackageVertexPrefix + id
	doc := map[string]any{
		"class":     "manifest",
		"isDeleted": false,
		"vertexKey": pkgKey,
		"localName": "manifest",
		"data":      map[string]any{"name": name, "version": version},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal legacy manifest: %v", err)
	}
	if _, err := conn.KVCreate(ctx, CoreBucket, pkgKey+".manifest", b); err != nil {
		t.Fatalf("inject legacy manifest %s: %v", pkgKey, err)
	}
	return pkgKey
}

// TestFindInstalledPackage_ExactMatchIgnoresNearMiss: a probe that
// byte-exactly matches an installed manifest resolves to it even when a
// fold-equal near-miss also exists on record — an exact hit is never
// second-guessed.
func TestFindInstalledPackage_ExactMatchIgnoresNearMiss(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("install: %v", err)
	}
	injectLegacyManifest(t, ctx, conn, "LEGACYPKGAHJKMNPQRST", "Sample-Pkg", "0.1.0")

	got, err := inst.findInstalledPackage(ctx, "sample-pkg")
	if err != nil {
		t.Fatalf("findInstalledPackage(%q): unexpected error: %v", "sample-pkg", err)
	}
	if got == nil || got.Name != "sample-pkg" {
		t.Fatalf("findInstalledPackage(%q) = %+v, want the exact-match record", "sample-pkg", got)
	}
}

// TestFindInstalledPackage_NearMissRefusesLoudly is the corrected shape: a
// probe with no exact hit but a fold-equal near-miss on record gets a loud
// error naming both spellings, never a silent "not installed" and never a
// resolved match. Proven through all four production entry points that call
// findInstalledPackage — none may swallow the error into "absent" or "fresh
// install", and none may take any destructive action on the near-miss.
func TestFindInstalledPackage_NearMissRefusesLoudly(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)

	legacyKey := injectLegacyManifest(t, ctx, conn, "LEGACYPKGAHJKMNPQRST", "Weird-Casing-Pkg", "0.4.0")
	probe := "weird-casing-pkg" // the normalized spelling; no exact hit exists.

	t.Run("findInstalledPackage", func(t *testing.T) {
		got, err := inst.findInstalledPackage(ctx, probe)
		if got != nil {
			t.Fatalf("findInstalledPackage(%q): expected no resolved match, got %+v", probe, got)
		}
		if err == nil {
			t.Fatalf("findInstalledPackage(%q): expected a near-miss error, got nil", probe)
		}
		if !strings.Contains(err.Error(), probe) || !strings.Contains(err.Error(), "Weird-Casing-Pkg") {
			t.Fatalf("findInstalledPackage(%q): error %q must name both the probe and the near-miss spelling", probe, err.Error())
		}
	})

	t.Run("IsPackageInstalled", func(t *testing.T) {
		ok, err := IsPackageInstalled(ctx, conn, probe)
		if err == nil {
			t.Fatalf("IsPackageInstalled(%q): expected a near-miss error, got nil (ok=%v)", probe, ok)
		}
		if ok {
			t.Fatalf("IsPackageInstalled(%q): must never report true on a near-miss", probe)
		}
	})

	t.Run("Install_RefusesInsteadOfDuplicating", func(t *testing.T) {
		def := sampleDef("0.1.0")
		def.Name = probe
		if _, err := inst.Install(ctx, def); err == nil {
			t.Fatalf("Install(%q): expected a near-miss error, got nil", probe)
		}
		if pkg, err := inst.findInstalledPackage(ctx, probe); err == nil && pkg != nil {
			t.Fatalf("Install must not have installed anything under the probe spelling on a near-miss refusal")
		}
	})

	t.Run("Upgrade_RefusesInsteadOfSilentNotInstalled", func(t *testing.T) {
		def := sampleDef("0.5.0")
		def.Name = probe
		_, err := inst.Upgrade(ctx, def)
		if err == nil {
			t.Fatalf("Upgrade(%q): expected a near-miss error, got nil", probe)
		}
		if strings.Contains(err.Error(), "not installed") {
			t.Fatalf("Upgrade(%q): error %q must be the loud near-miss refusal, not the ordinary not-installed error", probe, err.Error())
		}
	})

	t.Run("Apply_RefusesInsteadOfFreshInstalling", func(t *testing.T) {
		def := sampleDef("0.1.0")
		def.Name = probe
		res, err := inst.Apply(ctx, def, ApplyOptions{})
		if err == nil {
			t.Fatalf("Apply(%q): expected a near-miss error, got nil (result=%+v)", probe, res)
		}
		// Apply must not have fallen through to applyFreshInstall: no package
		// vertex exists under the probe's own spelling.
		if pkg, findErr := inst.findInstalledPackage(ctx, probe); findErr == nil && pkg != nil {
			t.Fatalf("Apply must not have fresh-installed anything under the probe spelling on a near-miss refusal")
		}
	})

	t.Run("Uninstall_RefusesAndTombstonesNothing", func(t *testing.T) {
		if _, err := inst.Uninstall(ctx, probe); err == nil {
			t.Fatalf("Uninstall(%q): expected a near-miss error, got nil", probe)
		}
		// The legacy manifest is untouched — still live, still isDeleted=false.
		entry, err := conn.KVGet(ctx, CoreBucket, legacyKey+".manifest")
		if err != nil {
			t.Fatalf("legacy manifest must still exist after a refused uninstall: %v", err)
		}
		var env struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			t.Fatalf("unmarshal legacy manifest: %v", err)
		}
		if env.IsDeleted {
			t.Fatalf("Uninstall must not have tombstoned the near-miss manifest")
		}
	})
}

// TestValidatePackageName_RefusesUnnormalizedName asserts Definition.validateAll
// (via validatePackageName) refuses a Name carrying surrounding whitespace or
// uppercase, and names both the offending value and the normalized form to
// use in the error.
func TestValidatePackageName_RefusesUnnormalizedName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{name: " sample-pkg", want: "sample-pkg"},
		{name: "sample-pkg ", want: "sample-pkg"},
		{name: "Sample-Pkg", want: "sample-pkg"},
		{name: "SAMPLE-PKG", want: "sample-pkg"},
	}
	for _, tc := range cases {
		def := sampleDef("0.1.0")
		def.Name = tc.name
		err := def.validateAll()
		if err == nil {
			t.Errorf("validateAll(): Name %q: expected an error, got nil", tc.name)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, tc.name) || !strings.Contains(msg, tc.want) {
			t.Errorf("validateAll(): Name %q: error %q must name both the offending value and %q", tc.name, msg, tc.want)
		}
	}
}

// TestValidatePackageName_AcceptsNormalizedName asserts a Name that is
// already its own normalized form passes validatePackageName (any later
// validator failure is unrelated to this check).
func TestValidatePackageName_AcceptsNormalizedName(t *testing.T) {
	def := sampleDef("0.1.0")
	if err := def.validatePackageName(); err != nil {
		t.Errorf("validatePackageName(): Name %q: unexpected error: %v", def.Name, err)
	}
}
