package pkgmgr

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// classesOf extracts and sorts the Class of every finding, so a test can
// compare "which classes fired" without caring about slice order (the pure
// function's own ordering is asserted separately, by
// TestReconcilePermissions_DeterministicOrder).
func classesOf(findings []PermissionFinding) []PermissionFindingClass {
	out := make([]PermissionFindingClass, len(findings))
	for i, f := range findings {
		out[i] = f.Class
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// TestReconcilePermissions is the increment-1 table: one row per
// classification/drift class, mirroring packagename_test.go's anonymous-
// struct-row style. Every drift class carries both a clean (positive) row
// and a firing (negative) row, per the fire brief's standing checklist.
func TestReconcilePermissions(t *testing.T) {
	kernelOnly := map[string]bool{"vtx.permission.7LDVS9C2JGmCgTwYXMEP": true}
	sampleOpKey := "vtx.permission." + PermissionID("sample-pkg", "SampleOp", "any")

	cases := []struct {
		name              string
		live              []LivePermission
		declared          []DeclaredPermission
		installedPackages map[string]bool
		kernelKeys        map[string]bool
		wantDrift         []PermissionFindingClass
		wantNotices       []PermissionFindingClass
	}{
		{
			name: "kernel permission present is silent (positive vector)",
			live: []LivePermission{
				{Key: "vtx.permission.7LDVS9C2JGmCgTwYXMEP", OperationType: "CreateMetaVertex", Scope: "any"},
			},
			kernelKeys: kernelOnly,
		},
		{
			name:       "kernel permission absent fires kernelMissing",
			kernelKeys: kernelOnly,
			wantDrift:  []PermissionFindingClass{FindingKernelMissing},
		},
		{
			name: "package permission matching its declared key is clean (undeclared + keyMismatch positive vector)",
			live: []LivePermission{
				{Key: sampleOpKey, OperationType: "SampleOp", Scope: "any", Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
			},
			declared:          []DeclaredPermission{{Package: "sample-pkg", Key: sampleOpKey}},
			installedPackages: map[string]bool{"sample-pkg": true},
		},
		{
			name: "package permission declaredBy an uninstalled package fires undeclared",
			live: []LivePermission{
				{
					Key:           "vtx.permission." + PermissionID("ghost-pkg", "GhostOp", "any"),
					OperationType: "GhostOp", Scope: "any",
					Origin: PermissionOriginPackage, DeclaredBy: "ghost-pkg",
				},
			},
			installedPackages: map[string]bool{},
			wantDrift:         []PermissionFindingClass{FindingUndeclared},
		},
		{
			name: "package permission whose key is not in its declaring package's declaredKeys fires undeclared",
			live: []LivePermission{
				// The legitimately declared permission, present and clean —
				// isolates the assertion to the forged entry below rather
				// than incidentally also tripping `missing` on SampleOp.
				{Key: sampleOpKey, OperationType: "SampleOp", Scope: "any", Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
				{
					Key:           "vtx.permission." + PermissionID("sample-pkg", "ForgedOp", "any"),
					OperationType: "ForgedOp", Scope: "any",
					Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg",
				},
			},
			declared:          []DeclaredPermission{{Package: "sample-pkg", Key: sampleOpKey}},
			installedPackages: map[string]bool{"sample-pkg": true},
			wantDrift:         []PermissionFindingClass{FindingUndeclared},
		},
		{
			name: "package permission body claiming a tuple its own (declared) key does not derive fires keyMismatch",
			live: []LivePermission{
				// Sitting AT the legitimately declared key, so undeclared
				// does not also fire — isolates the assertion to the body's
				// own self-consistency, which is what keyMismatch checks.
				{Key: sampleOpKey, OperationType: "TamperedOp", Scope: "any", Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
			},
			declared:          []DeclaredPermission{{Package: "sample-pkg", Key: sampleOpKey}},
			installedPackages: map[string]bool{"sample-pkg": true},
			wantDrift:         []PermissionFindingClass{FindingKeyMismatch},
		},
		{
			name: "declared key with a live vertex is clean (missing positive vector)",
			live: []LivePermission{
				{Key: sampleOpKey, OperationType: "SampleOp", Scope: "any", Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
			},
			declared:          []DeclaredPermission{{Package: "sample-pkg", Key: sampleOpKey}},
			installedPackages: map[string]bool{"sample-pkg": true},
		},
		{
			name: "declared key with no live vertex fires missing, even with no recoverable tuple",
			declared: []DeclaredPermission{
				// No OperationType/Scope set — the exact "no document at all"
				// shape LoadPermissionReconciliation's gatherer can produce
				// (a declaredKeys entry backed by nothing readable). Missing
				// must fire on Key identity alone.
				{Package: "sample-pkg", Key: sampleOpKey},
			},
			installedPackages: map[string]bool{"sample-pkg": true},
			wantDrift:         []PermissionFindingClass{FindingMissing},
		},
		{
			name: "runtime-origin permission is inventory, never drift",
			live: []LivePermission{
				{Key: "vtx.permission.zY2iRfHmEQh8DgQbEXia", OperationType: "AdHocOp", Scope: "any", Origin: PermissionOriginRuntime},
			},
			wantNotices: []PermissionFindingClass{FindingRuntimeInventory},
		},
		{
			name: "permission with no origin and not a kernel key is inventory, never drift (unstamped)",
			live: []LivePermission{
				{Key: "vtx.permission.6p5Esr7taUqEp7j61FuE", OperationType: "LegacyOp", Scope: "any"},
			},
			wantNotices: []PermissionFindingClass{FindingUnstampedInventory},
		},
		{
			name: "an empty scope derives its key verbatim, never normalized (L2 regression guard)",
			live: []LivePermission{
				{
					Key:           "vtx.permission." + PermissionID("sample-pkg", "WeirdScopeOp", ""),
					OperationType: "WeirdScopeOp", Scope: "",
					Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg",
				},
			},
			declared: []DeclaredPermission{
				{Package: "sample-pkg", Key: "vtx.permission." + PermissionID("sample-pkg", "WeirdScopeOp", "")},
			},
			installedPackages: map[string]bool{"sample-pkg": true},
			// No findings at all: if the derivation normalized "" to "any"
			// (installer.go does not), this would spuriously fire keyMismatch.
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ReconcilePermissions(tc.live, tc.declared, tc.installedPackages, tc.kernelKeys)
			gotDrift := classesOf(got.Drift)
			gotNotices := classesOf(got.Notices)
			if len(gotDrift) != len(tc.wantDrift) || !slices.Equal(gotDrift, tc.wantDrift) {
				t.Errorf("Drift classes = %v, want %v (findings: %+v)", gotDrift, tc.wantDrift, got.Drift)
			}
			if len(gotNotices) != len(tc.wantNotices) || !slices.Equal(gotNotices, tc.wantNotices) {
				t.Errorf("Notice classes = %v, want %v (findings: %+v)", gotNotices, tc.wantNotices, got.Notices)
			}
			wantHasDrift := len(tc.wantDrift) > 0
			if got.HasDrift() != wantHasDrift {
				t.Errorf("HasDrift() = %v, want %v", got.HasDrift(), wantHasDrift)
			}
		})
	}
}

// TestReconcilePermissions_DeterministicOrder asserts the Drift slice is
// sorted, not emitted in map-iteration order: two calls whose declared/live
// slices are supplied in opposite orders must produce byte-identical output.
func TestReconcilePermissions_DeterministicOrder(t *testing.T) {
	kernelKeys := map[string]bool{
		"vtx.permission.SWZpB4hde9askSkMWU7s": true,
		"vtx.permission.2QuyoBDkFGC5kdgrYkTF": true,
	}
	declared := []DeclaredPermission{
		{Package: "pkg-b", Key: "vtx.permission.vRbFXBLr7MCKANjA8qu3"},
		{Package: "pkg-a", Key: "vtx.permission.u3RxRfKybLhhhsLe3NiT"},
	}
	declaredReversed := []DeclaredPermission{declared[1], declared[0]}

	got1 := ReconcilePermissions(nil, declared, map[string]bool{"pkg-a": true, "pkg-b": true}, kernelKeys)
	got2 := ReconcilePermissions(nil, declaredReversed, map[string]bool{"pkg-a": true, "pkg-b": true}, kernelKeys)

	raw1, _ := json.Marshal(got1.Drift)
	raw2, _ := json.Marshal(got2.Drift)
	if string(raw1) != string(raw2) {
		t.Fatalf("Drift order depends on input order:\n got1=%s\n got2=%s", raw1, raw2)
	}
	// 2 kernelMissing + 2 missing == 4, and the classes must sort before one
	// another alphabetically (kernelMissing < missing).
	if len(got1.Drift) != 4 {
		t.Fatalf("len(Drift) = %d, want 4: %+v", len(got1.Drift), got1.Drift)
	}
	for i := 0; i < 2; i++ {
		if got1.Drift[i].Class != FindingKernelMissing {
			t.Errorf("Drift[%d].Class = %q, want %q (kernelMissing sorts first)", i, got1.Drift[i].Class, FindingKernelMissing)
		}
	}
	for i := 2; i < 4; i++ {
		if got1.Drift[i].Class != FindingMissing {
			t.Errorf("Drift[%d].Class = %q, want %q", i, got1.Drift[i].Class, FindingMissing)
		}
	}
}

// TestKernelPermissionKeySet (L7) pins the fail-loud contract on plain string
// input, so it never has to touch internal/bootstrap's package-level state
// (kernelPermissionKeySet's own doc comment explains why that would race
// installer_test.go's newInstallerHarness in the same test binary).
func TestKernelPermissionKeySet(t *testing.T) {
	resolved := []string{
		"vtx.permission.SWZpB4hde9askSkMWU7s",
		"vtx.permission.2QuyoBDkFGC5kdgrYkTF",
	}
	cases := []struct {
		name    string
		keys    []string
		wantErr bool
	}{
		{name: "all resolved", keys: resolved},
		{name: "unloaded — empty string", keys: []string{""}, wantErr: true},
		{name: "unloaded — bare prefix VertexKey produces from \"\"", keys: []string{"vtx.permission."}, wantErr: true},
		{name: "one of several unresolved still refuses the whole set", keys: []string{resolved[0], ""}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := kernelPermissionKeySet(tc.keys)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("kernelPermissionKeySet(%v) = %v, nil error; want a refusal", tc.keys, got)
				}
				if !strings.Contains(err.Error(), "bootstrap.Load") {
					t.Errorf("error should name the remedy that actually resolves it (bootstrap.Load): %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("kernelPermissionKeySet(%v): unexpected error %v", tc.keys, err)
			}
			if len(got) != len(tc.keys) {
				t.Errorf("len(got) = %d, want %d (%v)", len(got), len(tc.keys), got)
			}
		})
	}
}

// --- Increment 2: the Core-KV gatherer, end to end over a real install ---

// writeForgedPermission writes a `vtx.permission.<key>` envelope directly
// into core-kv via KVPut, standing in for the hazard this reconciler exists
// to catch — a permission that was never written by an install batch or a
// sanctioned runtime mint. Mirrors installer_test.go's tombstoneKey /
// oracle_agreement_test.go's patchDoc (read-mutate-write for an existing
// key; build-from-scratch for a new one).
func writeForgedPermission(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, data map[string]any) {
	t.Helper()
	doc := map[string]any{"class": "permission", "isDeleted": false, "data": data}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal forged permission %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, key, raw); err != nil {
		t.Fatalf("KVPut forged permission %s: %v", key, err)
	}
}

// livePermissionClassCounts independently re-derives the classification
// LoadPermissionReconciliation's gatherer feeds ReconcilePermissions, so a
// test can assert "how many kernel/package/runtime/unstamped permissions are
// live" — a fact ReconcilePermissions itself does not surface for the
// silent classes (kernel-clean and package-clean produce no finding at all).
func livePermissionClassCounts(t *testing.T, ctx context.Context, conn *substrate.Conn) map[PermissionProvenance]int {
	t.Helper()
	keys, err := conn.KVListKeysPrefix(ctx, CoreBucket, "vtx.permission.")
	if err != nil {
		t.Fatalf("KVListKeysPrefix vtx.permission.: %v", err)
	}
	roots := permissionVertexRootKeys(keys)
	entries, err := conn.KVGetMulti(ctx, CoreBucket, roots)
	if err != nil {
		t.Fatalf("KVGetMulti permission roots: %v", err)
	}
	kernelKeys, err := kernelPermissionKeys()
	if err != nil {
		t.Fatalf("kernelPermissionKeys: %v", err)
	}
	counts := map[PermissionProvenance]int{}
	for _, k := range roots {
		entry, ok := entries[k]
		if !ok {
			continue
		}
		var doc permissionDoc
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			t.Fatalf("unmarshal %s: %v", k, err)
		}
		if doc.IsDeleted {
			continue
		}
		p := LivePermission{
			Key: k, OperationType: doc.Data.OperationType, Scope: doc.Data.Scope,
			Origin: doc.Data.Origin, DeclaredBy: doc.Data.DeclaredBy,
		}
		counts[classifyLivePermission(p, kernelKeys)]++
	}
	return counts
}

// findByClass returns the first finding of the given class, failing the
// test if none is present — every "detects X" test below wants exactly one.
func findByClass(t *testing.T, findings []PermissionFinding, class PermissionFindingClass) PermissionFinding {
	t.Helper()
	var matches []PermissionFinding
	for _, f := range findings {
		if f.Class == class {
			matches = append(matches, f)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("findings of class %q = %d, want exactly 1 (all findings: %+v)", class, len(matches), findings)
	}
	return matches[0]
}

// TestLoadPermissionReconciliation_CleanInstall installs a real package
// through the Processor (newInstallerHarness's real DDL script + step6
// validation + step8 atomic commit) and asserts the reconciler reads it back
// with zero drift, six live kernel permissions, and one live package
// permission per sampleDef's single PermissionSpec.
func TestLoadPermissionReconciliation_CleanInstall(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	if got.HasDrift() {
		t.Fatalf("HasDrift() = true on a clean install, want false: %+v", got.Drift)
	}

	counts := livePermissionClassCounts(t, ctx, conn)
	if counts[PermissionProvenanceKernel] != 6 {
		t.Errorf("kernel class count = %d, want 6", counts[PermissionProvenanceKernel])
	}
	if counts[PermissionProvenancePackage] != len(def.Permissions) {
		t.Errorf("package class count = %d, want %d (sampleDef's declared permission count)", counts[PermissionProvenancePackage], len(def.Permissions))
	}
}

// TestLoadPermissionReconciliation_DetectsUndeclared forges a permission
// vertex whose key IS the derivation of its own claimed
// (declaredBy, operationType, scope) — so keyMismatch does not also fire —
// but whose key sample-pkg's real declaredKeys does not contain.
func TestLoadPermissionReconciliation_DetectsUndeclared(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	forgedKey := "vtx.permission." + PermissionID("sample-pkg", "ForgedOp", "any")
	writeForgedPermission(t, ctx, conn, forgedKey, map[string]any{
		"operationType": "ForgedOp", "scope": "any",
		"origin": PermissionOriginPackage, "declaredBy": "sample-pkg",
	})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingUndeclared)
	if f.Key != forgedKey {
		t.Errorf("undeclared finding Key = %q, want %q", f.Key, forgedKey)
	}
	if f.Package != "sample-pkg" {
		t.Errorf("undeclared finding Package = %q, want %q", f.Package, "sample-pkg")
	}
}

// TestLoadPermissionReconciliation_DetectsKeyMismatch overwrites the body AT
// sample-pkg's real, legitimately-declared permission key so it claims a
// different operationType — undeclared does not fire (the key IS declared),
// isolating the assertion to the body's own self-consistency.
func TestLoadPermissionReconciliation_DetectsKeyMismatch(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	realKey := "vtx.permission." + PermissionID("sample-pkg", "SampleOp", "any")
	writeForgedPermission(t, ctx, conn, realKey, map[string]any{
		"operationType": "TamperedOp", "scope": "any",
		"origin": PermissionOriginPackage, "declaredBy": "sample-pkg",
	})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingKeyMismatch)
	if f.Key != realKey {
		t.Errorf("keyMismatch finding Key = %q, want %q", f.Key, realKey)
	}
}

// TestLoadPermissionReconciliation_DetectsMissing tombstones sample-pkg's
// declared permission out of band (installer_test.go's tombstoneKey — the
// out-of-band soft delete a non-durable revoke would leave behind) without
// touching the package's own declaredKeys record, so the declared side still
// resolves the tuple from the tombstone's preserved data while the live side
// no longer carries it. See _NoDocumentAtAll below for the sibling vector
// where no data is recoverable at all.
func TestLoadPermissionReconciliation_DetectsMissing(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	permKey := "vtx.permission." + PermissionID("sample-pkg", "SampleOp", "any")
	tombstoneKey(t, ctx, conn, permKey)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingMissing)
	if f.Key != permKey {
		t.Errorf("missing finding Key = %q, want %q", f.Key, permKey)
	}
	if f.Package != "sample-pkg" || f.OperationType != "SampleOp" || f.Scope != "any" {
		t.Errorf("missing finding = %+v, want Package=sample-pkg OperationType=SampleOp Scope=any (recovered from the tombstone's preserved data)", f)
	}
}

// TestLoadPermissionReconciliation_DetectsMissing_NoDocumentAtAll (L1/L4)
// injects a declaredKeys entry into sample-pkg's manifest that never had a
// document written at it at all — no live vertex, no tombstone either, the
// case L1's key-based redesign exists to un-deaden: `missing` must still
// fire on Key/Package identity alone, with no tuple to enrich the finding
// (this is the vector the earlier tuple-reconstruction design silently
// `continue`d past).
func TestLoadPermissionReconciliation_DetectsMissing_NoDocumentAtAll(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	res, err := inst.Install(ctx, sampleDef("0.1.0"))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	phantomID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	phantomKey := "vtx.permission." + phantomID
	manifestKey := res.PackageKey + ".manifest"
	patchDoc(t, ctx, conn, manifestKey, func(doc map[string]any) {
		data, _ := doc["data"].(map[string]any)
		keys, _ := data["declaredKeys"].([]any)
		data["declaredKeys"] = append(keys, phantomKey)
	})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingMissing)
	if f.Key != phantomKey {
		t.Errorf("missing finding Key = %q, want %q", f.Key, phantomKey)
	}
	if f.Package != "sample-pkg" {
		t.Errorf("missing finding Package = %q, want %q", f.Package, "sample-pkg")
	}
	if f.OperationType != "" || f.Scope != "" {
		t.Errorf("missing finding for a never-written key should carry no recovered tuple, got OperationType=%q Scope=%q", f.OperationType, f.Scope)
	}
}

// TestLoadPermissionReconciliation_DetectsKernelMissing tombstones one of
// bootstrap's six primordial permission keys out of band.
func TestLoadPermissionReconciliation_DetectsKernelMissing(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	tombstoneKey(t, ctx, conn, bootstrap.PermCreateMetaVertexKey)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingKernelMissing)
	if f.Key != bootstrap.PermCreateMetaVertexKey {
		t.Errorf("kernelMissing finding Key = %q, want %q", f.Key, bootstrap.PermCreateMetaVertexKey)
	}
}

// TestLoadPermissionReconciliation_RuntimeAndUnstampedAreNotices forges a
// runtime-origin permission (the ratified second grant channel) and a
// no-origin, non-kernel permission (a pre-provenance-stamp package install),
// asserting the gatherer's own JSON decode of `data.origin` correctly routes
// both to Notices and neither to Drift.
func TestLoadPermissionReconciliation_RuntimeAndUnstampedAreNotices(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	runtimeID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	runtimeKey := "vtx.permission." + runtimeID
	writeForgedPermission(t, ctx, conn, runtimeKey, map[string]any{
		"operationType": "AdHocOp", "scope": "any", "origin": PermissionOriginRuntime,
	})

	unstampedID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	unstampedKey := "vtx.permission." + unstampedID
	writeForgedPermission(t, ctx, conn, unstampedKey, map[string]any{
		"operationType": "LegacyOp", "scope": "any",
	})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	if got.HasDrift() {
		t.Fatalf("HasDrift() = true, want false — runtime/unstamped are inventory only: %+v", got.Drift)
	}
	rf := findByClass(t, got.Notices, FindingRuntimeInventory)
	if rf.Key != runtimeKey {
		t.Errorf("runtimeInventory finding Key = %q, want %q", rf.Key, runtimeKey)
	}
	uf := findByClass(t, got.Notices, FindingUnstampedInventory)
	if uf.Key != unstampedKey {
		t.Errorf("unstampedInventory finding Key = %q, want %q", uf.Key, unstampedKey)
	}
}
