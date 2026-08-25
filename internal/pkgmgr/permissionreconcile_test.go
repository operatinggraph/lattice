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
	legacyKey := "vtx.permission.6p5Esr7taUqEp7j61FuE"

	cases := []struct {
		name              string
		live              []LivePermission
		declared          []DeclaredPermission
		installedPackages map[string]bool
		kernelKeys        map[string]bool
		// undecodableKeys are permission keys the gatherer found occupied but
		// could not read. A kernel key in that state must not ALSO be reported
		// absent — a document does occupy it.
		undecodableKeys map[string]bool
		wantDrift       []PermissionFindingClass
		wantNotices     []PermissionFindingClass
		// wantReason, when set, is checked against the single Drift finding's
		// Reason field (the row must produce exactly one Drift finding).
		// Class/Key/Package/OperationType/Scope are identical across the
		// different undeclared arms, so only Reason can tell a disabled
		// branch's finding from the real one.
		wantReason PermissionUndeclaredReason
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
			name:            "an occupied-but-unreadable kernel permission is not ALSO reported absent",
			kernelKeys:      kernelOnly,
			undecodableKeys: map[string]bool{"vtx.permission.7LDVS9C2JGmCgTwYXMEP": true},
			// The gatherer already raised undecodable for this key outside this
			// call. kernelMissing would be a second, false diagnosis: a
			// document occupies the key, it just cannot be read.
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
			wantReason:        ReasonPackageNotInstalled,
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
			wantReason:        ReasonKeyNotDeclared,
		},
		{
			name: "an undeclared vertex reports only undeclared, never also keyMismatch",
			live: []LivePermission{
				// The legitimately declared permission, present and clean —
				// isolates the assertion to the forged entry below rather
				// than incidentally also tripping `missing` on SampleOp.
				{Key: sampleOpKey, OperationType: "SampleOp", Scope: "any", Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
				// Self-inconsistent (its own key does not derive from its
				// own claimed OperationType/Scope either) AND absent from
				// sample-pkg's declaredKeys — both undeclared and keyMismatch
				// checks would independently fire on this body if keyMismatch
				// were checked unconditionally, so this row pins that only
				// undeclared does.
				{
					Key:           "vtx.permission." + PermissionID("sample-pkg", "TotallyDifferentOp", "any"),
					OperationType: "ForgedOp", Scope: "any",
					Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg",
				},
			},
			declared:          []DeclaredPermission{{Package: "sample-pkg", Key: sampleOpKey}},
			installedPackages: map[string]bool{"sample-pkg": true},
			wantDrift:         []PermissionFindingClass{FindingUndeclared},
			wantReason:        ReasonKeyNotDeclared,
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
			name: "declared key with no document at all fires missing, even with no recoverable tuple",
			declared: []DeclaredPermission{
				// No OperationType/Scope set, Tombstoned false — the exact
				// "no document at all" shape LoadPermissionReconciliation's
				// gatherer can produce. Missing must fire on Key/Package
				// identity alone.
				{Package: "sample-pkg", Key: sampleOpKey},
			},
			installedPackages: map[string]bool{"sample-pkg": true},
			wantDrift:         []PermissionFindingClass{FindingMissing},
		},
		{
			name: "declared key backed by a tombstoned document is a respected revocation, not missing",
			declared: []DeclaredPermission{
				{Package: "sample-pkg", Key: sampleOpKey, OperationType: "SampleOp", Scope: "any", Tombstoned: true},
			},
			installedPackages: map[string]bool{"sample-pkg": true},
			wantNotices:       []PermissionFindingClass{FindingRevoked},
			// No drift: revocations are durable (TombstonePermission,
			// upgrade.go's revival-skip), so this is the correct end state,
			// not a partial install.
		},
		{
			name: "declared key marked Undecodable produces no missing drift",
			declared: []DeclaredPermission{
				// The gatherer sets Undecodable when a document exists at Key
				// but could not be turned into a usable envelope — the pure
				// function must not ALSO report missing for it (the gatherer
				// separately raises FindingUndecodable, outside this pure
				// call, over its own undecodable list).
				{Package: "sample-pkg", Key: sampleOpKey, Undecodable: true},
			},
			installedPackages: map[string]bool{"sample-pkg": true},
			// Neither drift nor notice from THIS call: FindingUndecodable is
			// raised by the gatherer, not by ReconcilePermissions itself.
		},
		{
			name: "runtime-origin permission is inventory, never drift",
			live: []LivePermission{
				{Key: "vtx.permission.zY2iRfHmEQh8DgQbEXia", OperationType: "AdHocOp", Scope: "any", Origin: PermissionOriginRuntime},
			},
			wantNotices: []PermissionFindingClass{FindingRuntimeInventory},
		},
		{
			name: "a no-origin permission whose key IS declared by an installed package is inventory, never drift (unstamped)",
			live: []LivePermission{
				{Key: legacyKey, OperationType: "LegacyOp", Scope: "any"},
			},
			declared:          []DeclaredPermission{{Package: "sample-pkg", Key: legacyKey}},
			installedPackages: map[string]bool{"sample-pkg": true},
			wantNotices:       []PermissionFindingClass{FindingUnstampedInventory},
		},
		{
			name: "a no-origin permission whose key is declared nowhere fires undeclared, not a notice (the cheapest forgery)",
			live: []LivePermission{
				{Key: legacyKey, OperationType: "LegacyOp", Scope: "any"},
			},
			// No declared entry for legacyKey anywhere: a no-origin vertex
			// whose key no installed package declares is a forgery wearing a
			// legacy shape, not a legacy install — it must fire drift.
			wantDrift:  []PermissionFindingClass{FindingUndeclared},
			wantReason: ReasonNoOriginUndeclared,
		},
		{
			name: "a non-empty, unrecognized origin is drift outright even when its key IS declared",
			live: []LivePermission{
				{Key: sampleOpKey, OperationType: "SampleOp", Scope: "any", Origin: "Package"}, // capital-P typo, not the wire value
			},
			declared:          []DeclaredPermission{{Package: "sample-pkg", Key: sampleOpKey}},
			installedPackages: map[string]bool{"sample-pkg": true},
			wantDrift:         []PermissionFindingClass{FindingUndeclared},
			wantReason:        ReasonUnrecognizedOriginValue,
		},
		{
			name: "an empty scope derives its key verbatim, never normalized",
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
			got := ReconcilePermissions(tc.live, tc.declared, tc.installedPackages, tc.kernelKeys, tc.undecodableKeys)
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
			if tc.wantReason != "" {
				if len(got.Drift) != 1 {
					t.Fatalf("wantReason %q set but len(Drift) = %d, want exactly 1: %+v", tc.wantReason, len(got.Drift), got.Drift)
				}
				if got.Drift[0].Reason != tc.wantReason {
					t.Errorf("Drift[0].Reason = %q, want %q", got.Drift[0].Reason, tc.wantReason)
				}
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

	got1 := ReconcilePermissions(nil, declared, map[string]bool{"pkg-a": true, "pkg-b": true}, kernelKeys, nil)
	got2 := ReconcilePermissions(nil, declaredReversed, map[string]bool{"pkg-a": true, "pkg-b": true}, kernelKeys, nil)

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

// TestKernelPermissionKeySet pins the fail-loud contract on plain string
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

// writeRawPermission writes an ARBITRARY raw JSON body at key, bypassing
// writeForgedPermission's typed `data map[string]any` — the only way to
// construct an envelope whose `data.declaredBy` is a non-string (A's
// evasion vector), since json.Marshal of a Go map can never itself produce
// mismatched-type JSON.
func writeRawPermission(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, rawJSON string) {
	t.Helper()
	if _, err := conn.KVPut(ctx, CoreBucket, key, []byte(rawJSON)); err != nil {
		t.Fatalf("KVPut raw permission %s: %v", key, err)
	}
}

// livePermissionClassCounts independently re-derives the classification
// LoadPermissionReconciliation's gatherer feeds ReconcilePermissions (via the
// same gatherPermissionInputs both call), so a test can assert "how many
// kernel/package/runtime/unstamped/unrecognized permissions are live" — a
// fact ReconcilePermissions itself does not surface for the silent classes
// (kernel-clean and package-clean produce no finding at all).
func livePermissionClassCounts(t *testing.T, ctx context.Context, conn *substrate.Conn) map[PermissionProvenance]int {
	t.Helper()
	in, err := gatherPermissionInputs(ctx, conn)
	if err != nil {
		t.Fatalf("gatherPermissionInputs: %v", err)
	}
	if len(in.undecodable) > 0 {
		t.Fatalf("gatherPermissionInputs reported undecodable entries in a clean fixture: %+v", in.undecodable)
	}
	anyDeclaredKey := make(map[string]bool, len(in.declared))
	for _, d := range in.declared {
		anyDeclaredKey[d.Key] = true
	}
	counts := map[PermissionProvenance]int{}
	for _, p := range in.live {
		counts[classifyLivePermission(p, in.kernelPermissionKeys, anyDeclaredKey)]++
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

	// D's registry-anchor surface: every installed package appears, with its
	// live declaredKeys' permission-prefixed entries.
	keys, ok := got.DeclaredKeysByPackage["sample-pkg"]
	if !ok {
		t.Fatalf("DeclaredKeysByPackage[%q] missing, want an entry (installed package)", "sample-pkg")
	}
	wantKey := "vtx.permission." + PermissionID("sample-pkg", "SampleOp", "any")
	if !slices.Contains(keys, wantKey) {
		t.Errorf("DeclaredKeysByPackage[%q] = %v, want it to contain %q", "sample-pkg", keys, wantKey)
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
	if f.Reason != ReasonKeyNotDeclared {
		t.Errorf("undeclared finding Reason = %q, want %q", f.Reason, ReasonKeyNotDeclared)
	}
}

// TestLoadPermissionReconciliation_DetectsUndeclared_PackageNotInstalled
// forges a permission vertex whose declaredBy names a package that was NEVER
// installed at all — the OTHER undeclared arm from
// TestLoadPermissionReconciliation_DetectsUndeclared above, which exercises
// "declaredBy IS installed but doesn't declare this key". If
// `!installedPackages[p.DeclaredBy]` is disabled, execution falls through to
// the second arm and produces an identical Class/Key/Package finding — only
// the Reason field distinguishes which check actually fired.
func TestLoadPermissionReconciliation_DetectsUndeclared_PackageNotInstalled(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	forgedKey := "vtx.permission." + PermissionID("never-installed-pkg", "GhostOp", "any")
	writeForgedPermission(t, ctx, conn, forgedKey, map[string]any{
		"operationType": "GhostOp", "scope": "any",
		"origin": PermissionOriginPackage, "declaredBy": "never-installed-pkg",
	})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingUndeclared)
	if f.Key != forgedKey {
		t.Errorf("undeclared finding Key = %q, want %q", f.Key, forgedKey)
	}
	if f.Reason != ReasonPackageNotInstalled {
		t.Errorf("undeclared finding Reason = %q, want %q", f.Reason, ReasonPackageNotInstalled)
	}
}

// TestLoadPermissionReconciliation_DetectsUnrecognizedOrigin forges a
// permission vertex with NO `origin` field at all, whose key is declared by
// no installed package — the cheapest forgery the fail-open shape treated as
// a silent, unreconciled notice. Must fire undeclared, not classify as a
// legacy pre-stamp install.
func TestLoadPermissionReconciliation_DetectsUnrecognizedOrigin(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	forgedID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	forgedKey := "vtx.permission." + forgedID
	// No "origin" key at all, and this key is in no package's declaredKeys.
	writeForgedPermission(t, ctx, conn, forgedKey, map[string]any{
		"operationType": "SilentGrant", "scope": "any",
	})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingUndeclared)
	if f.Key != forgedKey {
		t.Errorf("undeclared finding Key = %q, want %q", f.Key, forgedKey)
	}
	if f.Reason != ReasonNoOriginUndeclared {
		t.Errorf("undeclared finding Reason = %q, want %q", f.Reason, ReasonNoOriginUndeclared)
	}
	// It must NOT also appear as an unstampedInventory notice.
	for _, n := range got.Notices {
		if n.Key == forgedKey {
			t.Errorf("forged key %s appeared as a notice (%+v) as well as drift — it should be drift only", forgedKey, n)
		}
	}
}

// TestLoadPermissionReconciliation_DetectsUnrecognizedOriginValue retargets a
// real, legitimately declared permission key so its body claims an origin
// value that is neither "package" nor "runtime" (a corrupted/typo'd stamp, or
// a package upgrade's unvalidated mutations arm dropping the real one) — with
// an unrecognized, non-empty origin, this must fire drift outright even
// though the key IS declared (the shape a plain no-origin check would miss).
func TestLoadPermissionReconciliation_DetectsUnrecognizedOriginValue(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	realKey := "vtx.permission." + PermissionID("sample-pkg", "SampleOp", "any")
	writeForgedPermission(t, ctx, conn, realKey, map[string]any{
		"operationType": "SampleOp", "scope": "any", "origin": "Package", // typo, not the wire value
	})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingUndeclared)
	if f.Key != realKey {
		t.Errorf("undeclared finding Key = %q, want %q", f.Key, realKey)
	}
	if f.Reason != ReasonUnrecognizedOriginValue {
		t.Errorf("undeclared finding Reason = %q, want %q", f.Reason, ReasonUnrecognizedOriginValue)
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
	// Exactly one finding for this key, not also undeclared.
	for _, d := range got.Drift {
		if d.Key == realKey && d.Class != FindingKeyMismatch {
			t.Errorf("realKey produced an extra finding %+v — should be keyMismatch alone", d)
		}
	}
}

// TestLoadPermissionReconciliation_DetectsMissing tombstones sample-pkg's
// declared permission key via a raw manifest injection that (unlike a real
// TombstonePermission call) leaves no document at that key at all — no live
// vertex, no tombstone either, the case B/L1's key-based redesign exists to
// un-deaden: `missing` must still fire on Key/Package identity alone, with no
// tuple to enrich the finding. See _RevokedIsNotice below for the sibling
// vector where a document IS present, tombstoned — that is NOT missing.
func TestLoadPermissionReconciliation_DetectsMissing(t *testing.T) {
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

// TestLoadPermissionReconciliation_RevokedIsNotice tombstones
// sample-pkg's declared permission via installer_test.go's tombstoneKey —
// the out-of-band soft delete TombstonePermission's sanctioned, durable
// revocation would leave behind — without touching the package's own
// declaredKeys record. The declared side resolves the tuple from the
// tombstone's preserved data. This must NOT be `missing` (revocations are
// durable: a package upgrade or --force re-apply does not revive the key),
// so it must be a notice, and the reconciliation must report zero drift.
func TestLoadPermissionReconciliation_RevokedIsNotice(t *testing.T) {
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
	if got.HasDrift() {
		t.Fatalf("HasDrift() = true, want false — a tombstoned declared key is a respected revocation, not missing: %+v", got.Drift)
	}
	f := findByClass(t, got.Notices, FindingRevoked)
	if f.Key != permKey {
		t.Errorf("revoked finding Key = %q, want %q", f.Key, permKey)
	}
	if f.Package != "sample-pkg" || f.OperationType != "SampleOp" || f.Scope != "any" {
		t.Errorf("revoked finding = %+v, want Package=sample-pkg OperationType=SampleOp Scope=any (recovered from the tombstone's preserved data)", f)
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
// no-origin permission whose key IS injected into an installed package's
// declaredKeys (the genuine pre-provenance-stamp shape, C's fix), asserting
// the gatherer's own JSON decode of `data.origin` and the declaredKeys
// membership check together route both to Notices and neither to Drift.
func TestLoadPermissionReconciliation_RuntimeAndUnstampedAreNotices(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	res, err := inst.Install(ctx, sampleDef("0.1.0"))
	if err != nil {
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
	// Register unstampedKey as one of sample-pkg's own declaredKeys — the
	// fact that makes it a genuine pre-stamp legacy install rather than a
	// forgery that simply omitted `origin` (C's fix).
	manifestKey := res.PackageKey + ".manifest"
	patchDoc(t, ctx, conn, manifestKey, func(doc map[string]any) {
		data, _ := doc["data"].(map[string]any)
		keys, _ := data["declaredKeys"].([]any)
		data["declaredKeys"] = append(keys, unstampedKey)
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

// TestLoadPermissionReconciliation_DetectsUndecodable writes a permission
// envelope whose `data.declaredBy` is a JSON NUMBER rather than a string.
// `declaredBy` is read by no authorization path
// (packages/rbac-domain/lenses.go projects only
// operationType/scope/lanes/origin), so this vertex authorizes normally via
// its own GrantPermission link regardless of whether this reconciler can
// decode its envelope — a decode failure must surface as its own finding,
// never as silence.
func TestLoadPermissionReconciliation_DetectsUndecodable(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	malformedID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	malformedKey := "vtx.permission." + malformedID
	writeRawPermission(t, ctx, conn, malformedKey,
		`{"class":"permission","isDeleted":false,"data":{"operationType":"Sneaky","scope":"any","origin":"package","declaredBy":1}}`)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingUndecodable)
	if f.Key != malformedKey {
		t.Errorf("undecodable finding Key = %q, want %q", f.Key, malformedKey)
	}
}

// TestLoadPermissionReconciliation_UndecodableDeclaredKeyIsNotAlsoMissing
// overwrites a REAL, currently-declared permission key — not a fresh one —
// with a body that is not valid JSON at all: the key is occupied, not
// absent, so this must fire undecodable only, never missing (a "no live
// vertex" diagnosis would be actively false for an occupied key, and its
// printed remedy would not apply). This is the second, distinct vector from
// TestLoadPermissionReconciliation_DetectsUndecodable above, which uses a
// fresh, never-declared key.
func TestLoadPermissionReconciliation_UndecodableDeclaredKeyIsNotAlsoMissing(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	realKey := "vtx.permission." + PermissionID("sample-pkg", "SampleOp", "any")
	writeRawPermission(t, ctx, conn, realKey, `not json at all {{{`)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingUndecodable)
	if f.Key != realKey {
		t.Errorf("undecodable finding Key = %q, want %q", f.Key, realKey)
	}
	for _, d := range got.Drift {
		if d.Key == realKey && d.Class != FindingUndecodable {
			t.Errorf("realKey produced an extra drift finding %+v — should be undecodable alone; missing must not also fire for an occupied-but-unreadable key", d)
		}
	}
}

// --- the grant-edge plane ---

// grantClassesOf extracts and sorts the Class of every grant finding, so a
// test can compare "which classes fired" without caring about slice order (the
// pure function's own ordering is asserted separately, by
// TestReconcileGrantLinks_DeterministicOrder).
func grantClassesOf(findings []GrantFinding) []GrantFindingClass {
	out := make([]GrantFindingClass, len(findings))
	for i, f := range findings {
		out[i] = f.Class
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// TestReconcileGrantLinks is the grant-edge table: one row per classification
// and per drift leg, each drift class carrying both a clean (positive) row and
// a firing (negative) row.
func TestReconcileGrantLinks(t *testing.T) {
	const (
		roleOperatorID = "SWZpB4hde9askSkMWU7s"
		roleForgedID   = "2QuyoBDkFGC5kdgrYkTF"
		kernelPermID   = "7LDVS9C2JGmCgTwYXMEP"
		legacyPermID   = "6p5Esr7taUqEp7j61FuE"
		runtimePermID  = "zY2iRfHmEQh8DgQbEXia"
	)
	kernelEdge := substrate.LinkKey("permission", kernelPermID, "grantedBy", "role", roleOperatorID)
	kernelOnly := map[string]bool{kernelEdge: true}

	samplePermID := PermissionID("sample-pkg", "SampleOp", "any")
	samplePermKey := "vtx.permission." + samplePermID
	sampleEdge := substrate.LinkKey("permission", samplePermID, "grantedBy", "role", roleOperatorID)
	samplePerms := []DeclaredPermission{{Package: "sample-pkg", Key: samplePermKey}}

	// An edge onto the KERNEL's install permission, wearing sample-pkg's
	// declaration — the escalation this plane exists to catch, since it forges
	// no permission vertex at all.
	stolenEdge := substrate.LinkKey("permission", kernelPermID, "grantedBy", "role", roleForgedID)
	// kernelPerms is the permission side of the kernel's grant topology: an
	// edge onto one of these that is not kernelEdge itself is a regrant.
	kernelPerms := map[string]bool{"vtx.permission." + kernelPermID: true}
	legacyEdge := substrate.LinkKey("permission", legacyPermID, "grantedBy", "role", roleOperatorID)

	cases := []struct {
		name                string
		live                []LiveGrantLink
		declared            []DeclaredGrantLink
		declaredPermissions []DeclaredPermission
		installedPackages   map[string]bool
		kernelKeys          map[string]bool
		// kernelPermissionKeys drives the kernel-regrant check, which is decided
		// before any origin is read; undecodableKeys keeps a kernel edge that is
		// occupied-but-unreadable from ALSO being reported absent.
		kernelPermissionKeys map[string]bool
		undecodableKeys      map[string]bool
		// wantDeclaredByPackage, when set, is compared against the
		// declaredGrantLinksByPackage map the call returns.
		wantDeclaredByPackage map[string][]string
		wantDrift             []GrantFindingClass
		wantNotices           []GrantFindingClass
		// wantReason, when set, is checked against the single Drift finding's
		// Reason field (the row must produce exactly one Drift finding).
		// Class/Key/Package are identical across the undeclared arms, so only
		// Reason can tell a disabled branch's finding from the real one.
		wantReason GrantUndeclaredReason
	}{
		{
			name:       "kernel grant edge present is silent (positive vector)",
			live:       []LiveGrantLink{{Key: kernelEdge, PermissionKey: "vtx.permission." + kernelPermID, RoleKey: "vtx.role." + roleOperatorID}},
			kernelKeys: kernelOnly,
		},
		{
			name:       "kernel grant edge absent fires kernelMissing",
			kernelKeys: kernelOnly,
			wantDrift:  []GrantFindingClass{GrantFindingKernelMissing},
		},
		{
			name: "package edge on its own declared permission is clean (undeclared + keyMismatch positive vector)",
			live: []LiveGrantLink{
				{Key: sampleEdge, PermissionKey: samplePermKey, RoleKey: "vtx.role." + roleOperatorID,
					Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
			},
			declared:            []DeclaredGrantLink{{Package: "sample-pkg", Key: sampleEdge}},
			declaredPermissions: samplePerms,
			installedPackages:   map[string]bool{"sample-pkg": true},
		},
		{
			name: "package edge declaredBy an uninstalled package fires undeclared",
			live: []LiveGrantLink{
				{Key: sampleEdge, PermissionKey: samplePermKey, RoleKey: "vtx.role." + roleOperatorID,
					Origin: PermissionOriginPackage, DeclaredBy: "ghost-pkg"},
			},
			installedPackages: map[string]bool{},
			wantDrift:         []GrantFindingClass{GrantFindingUndeclared},
			wantReason:        GrantReasonPackageNotInstalled,
		},
		{
			name: "package edge whose key is not in its declaring package's declaredKeys fires undeclared",
			live: []LiveGrantLink{
				{Key: stolenEdge, PermissionKey: "vtx.permission." + kernelPermID, RoleKey: "vtx.role." + roleForgedID,
					Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
			},
			declared:            []DeclaredGrantLink{{Package: "sample-pkg", Key: sampleEdge}},
			declaredPermissions: samplePerms,
			installedPackages:   map[string]bool{"sample-pkg": true},
			// sampleEdge is declared but not live, so `missing` fires for it as
			// well as `undeclared` for the forged edge — two findings, so
			// wantReason (which demands exactly one) stays unset and the
			// dedicated keyNotDeclared reason is pinned by the row below.
			wantDrift: []GrantFindingClass{GrantFindingMissing, GrantFindingUndeclared},
		},
		{
			name: "an undeclared edge reports only undeclared, never also keyMismatch",
			live: []LiveGrantLink{
				// Both undeclared (absent from sample-pkg's declaredKeys) AND
				// pointing at a permission sample-pkg does not declare — both
				// checks would independently fire on this edge if keyMismatch
				// were checked unconditionally, so this row pins that only
				// undeclared does.
				{Key: stolenEdge, PermissionKey: "vtx.permission." + kernelPermID, RoleKey: "vtx.role." + roleForgedID,
					Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
			},
			declaredPermissions: samplePerms,
			installedPackages:   map[string]bool{"sample-pkg": true},
			wantDrift:           []GrantFindingClass{GrantFindingUndeclared},
			wantReason:          GrantReasonKeyNotDeclared,
		},
		{
			name: "a declared edge granting a permission its package does not own fires keyMismatch",
			live: []LiveGrantLink{
				// Declared — an attacker-authored manifest can say so — but the
				// permission it grants belongs to the kernel, not to sample-pkg.
				{Key: stolenEdge, PermissionKey: "vtx.permission." + kernelPermID, RoleKey: "vtx.role." + roleForgedID,
					Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
			},
			declared:            []DeclaredGrantLink{{Package: "sample-pkg", Key: stolenEdge}},
			declaredPermissions: samplePerms,
			installedPackages:   map[string]bool{"sample-pkg": true},
			wantDrift:           []GrantFindingClass{GrantFindingKeyMismatch},
		},
		{
			name: "declared edge with a live link is clean (missing positive vector)",
			live: []LiveGrantLink{
				{Key: sampleEdge, PermissionKey: samplePermKey, RoleKey: "vtx.role." + roleOperatorID,
					Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
			},
			declared:            []DeclaredGrantLink{{Package: "sample-pkg", Key: sampleEdge}},
			declaredPermissions: samplePerms,
			installedPackages:   map[string]bool{"sample-pkg": true},
		},
		{
			name:                "declared edge with no document at all fires missing",
			declared:            []DeclaredGrantLink{{Package: "sample-pkg", Key: sampleEdge}},
			declaredPermissions: samplePerms,
			installedPackages:   map[string]bool{"sample-pkg": true},
			wantDrift:           []GrantFindingClass{GrantFindingMissing},
		},
		{
			name: "declared edge backed by a tombstoned document is a respected revocation, not missing",
			declared: []DeclaredGrantLink{
				{Package: "sample-pkg", Key: sampleEdge, PermissionKey: samplePermKey,
					RoleKey: "vtx.role." + roleOperatorID, Tombstoned: true},
			},
			declaredPermissions: samplePerms,
			installedPackages:   map[string]bool{"sample-pkg": true},
			wantNotices:         []GrantFindingClass{GrantFindingRevoked},
		},
		{
			name: "declared edge marked Undecodable produces no missing drift",
			declared: []DeclaredGrantLink{
				{Package: "sample-pkg", Key: sampleEdge, Undecodable: true},
			},
			declaredPermissions: samplePerms,
			installedPackages:   map[string]bool{"sample-pkg": true},
			// Neither drift nor notice from THIS call: GrantFindingUndecodable
			// is raised by the gatherer, not by ReconcileGrantLinks itself.
		},
		{
			name: "runtime-origin edge is inventory, never drift",
			live: []LiveGrantLink{
				{Key: substrate.LinkKey("permission", runtimePermID, "grantedBy", "role", roleOperatorID),
					PermissionKey: "vtx.permission." + runtimePermID, RoleKey: "vtx.role." + roleOperatorID,
					Origin: PermissionOriginRuntime},
			},
			wantNotices: []GrantFindingClass{GrantFindingRuntimeInventory},
		},
		{
			name: "a no-origin edge declared by a package that also declares the permission it grants is inventory, never drift (unstamped)",
			live: []LiveGrantLink{
				{Key: legacyEdge, PermissionKey: "vtx.permission." + legacyPermID, RoleKey: "vtx.role." + roleOperatorID},
			},
			declared:            []DeclaredGrantLink{{Package: "sample-pkg", Key: legacyEdge}},
			declaredPermissions: []DeclaredPermission{{Package: "sample-pkg", Key: "vtx.permission." + legacyPermID}},
			installedPackages:   map[string]bool{"sample-pkg": true},
			wantNotices:         []GrantFindingClass{GrantFindingUnstampedInventory},
		},
		{
			name: "an unstamped edge granting a permission its declaring package does not own fires keyMismatch",
			live: []LiveGrantLink{
				// Declared, and carrying no origin — which under a
				// stamp-only check would be the weaker, safer-looking class.
				// The derivation check runs here exactly as it does on the
				// package class: omitting the stamp must not buy an edge a
				// pass the stamped form would not get.
				{Key: legacyEdge, PermissionKey: "vtx.permission." + legacyPermID, RoleKey: "vtx.role." + roleOperatorID},
			},
			declared:            []DeclaredGrantLink{{Package: "sample-pkg", Key: legacyEdge}},
			declaredPermissions: samplePerms,
			installedPackages:   map[string]bool{"sample-pkg": true},
			wantDrift:           []GrantFindingClass{GrantFindingKeyMismatch},
		},
		{
			name: "an edge stamping itself runtime onto a KERNEL permission is drift, not inventory",
			live: []LiveGrantLink{
				// The whole point of the class: `origin` is client-supplied,
				// so a forger picks `runtime` and lands in a notice arm that
				// reaches no other check. The kernel-regrant check is decided
				// before any origin is read, so the choice buys nothing.
				{Key: stolenEdge, PermissionKey: "vtx.permission." + kernelPermID, RoleKey: "vtx.role." + roleForgedID,
					Origin: PermissionOriginRuntime},
			},
			installedPackages:    map[string]bool{"sample-pkg": true},
			kernelKeys:           kernelOnly,
			kernelPermissionKeys: kernelPerms,
			// kernelMissing also fires: kernelOnly's edge is not in live.
			wantDrift: []GrantFindingClass{GrantFindingKernelMissing, GrantFindingKernelRegrant},
		},
		{
			name: "a DECLARED package-origin edge onto a kernel permission is still a kernel regrant",
			live: []LiveGrantLink{
				{Key: stolenEdge, PermissionKey: "vtx.permission." + kernelPermID, RoleKey: "vtx.role." + roleForgedID,
					Origin: PermissionOriginPackage, DeclaredBy: "sample-pkg"},
			},
			declared:             []DeclaredGrantLink{{Package: "sample-pkg", Key: stolenEdge}},
			declaredPermissions:  []DeclaredPermission{{Package: "sample-pkg", Key: "vtx.permission." + kernelPermID}},
			installedPackages:    map[string]bool{"sample-pkg": true},
			kernelPermissionKeys: kernelPerms,
			// Every declaredKeys-side test this edge could face passes — the
			// attacker wrote both halves. The kernel's grant topology is the
			// one fact not sourced from the manifest.
			wantDrift: []GrantFindingClass{GrantFindingKernelRegrant},
		},
		{
			name: "the kernel's own edge onto a kernel permission is silent (kernelRegrant positive vector)",
			live: []LiveGrantLink{
				{Key: kernelEdge, PermissionKey: "vtx.permission." + kernelPermID, RoleKey: "vtx.role." + roleOperatorID},
			},
			kernelKeys:           kernelOnly,
			kernelPermissionKeys: kernelPerms,
		},
		{
			name: "a key in the grant namespace whose ids are not NanoIDs fires malformedKey",
			live: []LiveGrantLink{
				{Key: "lnk.permission.short.grantedBy.role." + roleOperatorID},
			},
			installedPackages: map[string]bool{"sample-pkg": true},
			wantDrift:         []GrantFindingClass{GrantFindingMalformedKey},
		},
		{
			name:            "an occupied-but-unreadable kernel edge is not ALSO reported absent",
			kernelKeys:      kernelOnly,
			undecodableKeys: map[string]bool{kernelEdge: true},
			// The gatherer already raised undecodable for this key outside this
			// call. kernelMissing would be a second, false diagnosis: a
			// document occupies the key.
		},
		{
			name:              "an installed package declaring no grant edge is present with an empty list",
			installedPackages: map[string]bool{"sample-pkg": true, "quiet-pkg": true},
			declared:          []DeclaredGrantLink{{Package: "sample-pkg", Key: sampleEdge}},
			// sampleEdge is declared and not live, so missing fires for it —
			// this row is about the map, not the findings.
			wantDrift: []GrantFindingClass{GrantFindingMissing},
			wantDeclaredByPackage: map[string][]string{
				"sample-pkg": {sampleEdge},
				"quiet-pkg":  {},
			},
		},
		{
			name: "a no-origin edge whose key is declared nowhere fires undeclared, not a notice (the field-deletion vector)",
			live: []LiveGrantLink{
				{Key: stolenEdge, PermissionKey: "vtx.permission." + kernelPermID, RoleKey: "vtx.role." + roleForgedID},
			},
			// No declared entry for stolenEdge anywhere: an edge carrying no
			// origin that no installed package declares is a forgery wearing a
			// legacy shape, not a legacy install.
			installedPackages: map[string]bool{"sample-pkg": true},
			wantDrift:         []GrantFindingClass{GrantFindingUndeclared},
			wantReason:        GrantReasonNoOriginUndeclared,
		},
		{
			name: "a non-empty, unrecognized origin is drift outright even when its key IS declared",
			live: []LiveGrantLink{
				{Key: sampleEdge, PermissionKey: samplePermKey, RoleKey: "vtx.role." + roleOperatorID,
					Origin: "Package"}, // capital-P typo, not the wire value
			},
			declared:            []DeclaredGrantLink{{Package: "sample-pkg", Key: sampleEdge}},
			declaredPermissions: samplePerms,
			installedPackages:   map[string]bool{"sample-pkg": true},
			wantDrift:           []GrantFindingClass{GrantFindingUndeclared},
			wantReason:          GrantReasonUnrecognizedOriginValue,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drift, notices, declaredByPkg := ReconcileGrantLinks(GrantLinkReconcileInput{
				Live:                 tc.live,
				Declared:             tc.declared,
				DeclaredPermissions:  tc.declaredPermissions,
				InstalledPackages:    tc.installedPackages,
				KernelGrantLinkKeys:  tc.kernelKeys,
				KernelPermissionKeys: tc.kernelPermissionKeys,
				UndecodableKeys:      tc.undecodableKeys,
			})
			gotDrift := grantClassesOf(drift)
			gotNotices := grantClassesOf(notices)
			if !slices.Equal(gotDrift, tc.wantDrift) {
				t.Errorf("Drift classes = %v, want %v (findings: %+v)", gotDrift, tc.wantDrift, drift)
			}
			if !slices.Equal(gotNotices, tc.wantNotices) {
				t.Errorf("Notice classes = %v, want %v (findings: %+v)", gotNotices, tc.wantNotices, notices)
			}
			if tc.wantDeclaredByPackage != nil {
				if len(declaredByPkg) != len(tc.wantDeclaredByPackage) {
					t.Errorf("declaredGrantLinksByPackage = %v, want %v", declaredByPkg, tc.wantDeclaredByPackage)
				}
				for pkg, want := range tc.wantDeclaredByPackage {
					got, present := declaredByPkg[pkg]
					if !present {
						t.Errorf("declaredGrantLinksByPackage has no entry for %q — an installed package must be present even when it declares no grant edge, because the registry-anchor pass reads empty and absent differently", pkg)
						continue
					}
					if !slices.Equal(got, want) {
						t.Errorf("declaredGrantLinksByPackage[%q] = %v, want %v", pkg, got, want)
					}
				}
			}
			if tc.wantReason != "" {
				if len(drift) != 1 {
					t.Fatalf("wantReason %q set but len(drift) = %d, want exactly 1: %+v", tc.wantReason, len(drift), drift)
				}
				if drift[0].Reason != tc.wantReason {
					t.Errorf("drift[0].Reason = %q, want %q", drift[0].Reason, tc.wantReason)
				}
			}
		})
	}
}

// TestReconcileGrantLinks_DeterministicOrder asserts the drift slice is
// sorted, not emitted in map-iteration order: two calls whose declared slices
// are supplied in opposite orders must produce byte-identical output.
func TestReconcileGrantLinks_DeterministicOrder(t *testing.T) {
	kernelKeys := map[string]bool{
		substrate.LinkKey("permission", "SWZpB4hde9askSkMWU7s", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT"): true,
		substrate.LinkKey("permission", "2QuyoBDkFGC5kdgrYkTF", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT"): true,
	}
	declared := []DeclaredGrantLink{
		{Package: "pkg-b", Key: substrate.LinkKey("permission", "vRbFXBLr7MCKANjA8qu3", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT")},
		{Package: "pkg-a", Key: substrate.LinkKey("permission", "6p5Esr7taUqEp7j61FuE", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT")},
	}
	declaredReversed := []DeclaredGrantLink{declared[1], declared[0]}
	installed := map[string]bool{"pkg-a": true, "pkg-b": true}

	drift1, _, _ := ReconcileGrantLinks(GrantLinkReconcileInput{
		Declared: declared, InstalledPackages: installed, KernelGrantLinkKeys: kernelKeys,
	})
	drift2, _, _ := ReconcileGrantLinks(GrantLinkReconcileInput{
		Declared: declaredReversed, InstalledPackages: installed, KernelGrantLinkKeys: kernelKeys,
	})

	raw1, _ := json.Marshal(drift1)
	raw2, _ := json.Marshal(drift2)
	if string(raw1) != string(raw2) {
		t.Fatalf("drift order depends on input order:\n got1=%s\n got2=%s", raw1, raw2)
	}
	// 2 kernelMissing + 2 missing == 4, and the classes must sort before one
	// another alphabetically (kernelMissing < missing).
	if len(drift1) != 4 {
		t.Fatalf("len(drift) = %d, want 4: %+v", len(drift1), drift1)
	}
	for i := 0; i < 2; i++ {
		if drift1[i].Class != GrantFindingKernelMissing {
			t.Errorf("drift[%d].Class = %q, want %q (kernelMissing sorts first)", i, drift1[i].Class, GrantFindingKernelMissing)
		}
	}
	for i := 2; i < 4; i++ {
		if drift1[i].Class != GrantFindingMissing {
			t.Errorf("drift[%d].Class = %q, want %q", i, drift1[i].Class, GrantFindingMissing)
		}
	}
}

// TestKernelGrantLinkKeySet pins the fail-loud contract on plain string input,
// so it never has to touch internal/bootstrap's package-level state (see
// kernelGrantLinkKeySet's own doc comment for why mutating those globals would
// race the rest of this package's tests).
func TestKernelGrantLinkKeySet(t *testing.T) {
	resolved := []string{
		substrate.LinkKey("permission", "SWZpB4hde9askSkMWU7s", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT"),
		substrate.LinkKey("permission", "2QuyoBDkFGC5kdgrYkTF", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT"),
	}
	cases := []struct {
		name    string
		keys    []string
		wantErr bool
	}{
		{name: "all resolved", keys: resolved},
		{name: "unloaded — empty string", keys: []string{""}, wantErr: true},
		{
			// The shape bootstrap.KernelGrantLinkKeys derives from unpopulated
			// primordial IDs: the key's segments are all there, its ids are not.
			name:    "unloaded — empty id segments",
			keys:    []string{"lnk.permission..grantedBy.role."},
			wantErr: true,
		},
		{
			name:    "unloaded — only the role id is empty",
			keys:    []string{"lnk.permission.SWZpB4hde9askSkMWU7s.grantedBy.role."},
			wantErr: true,
		},
		{
			name:    "not a grant edge at all",
			keys:    []string{"lnk.permission.SWZpB4hde9askSkMWU7s.forOperation.meta.u3RxRfKybLhhhsLe3NiT"},
			wantErr: true,
		},
		{name: "one of several unresolved still refuses the whole set", keys: []string{resolved[0], ""}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := kernelGrantLinkKeySet(tc.keys)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("kernelGrantLinkKeySet(%v) = %v, nil error; want a refusal", tc.keys, got)
				}
				if !strings.Contains(err.Error(), "bootstrap.Load") {
					t.Errorf("error should name the remedy that actually resolves it (bootstrap.Load): %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("kernelGrantLinkKeySet(%v): unexpected error %v", tc.keys, err)
			}
			if len(got) != len(tc.keys) {
				t.Errorf("len(got) = %d, want %d (%v)", len(got), len(tc.keys), got)
			}
		})
	}
}

// writeForgedGrantLink writes a `grantedBy` link envelope directly into
// core-kv via KVPut, standing in for the hazard this plane exists to catch —
// an edge authored by neither an install batch nor GrantPermission. The
// document's sourceVertex/targetVertex agree with the key, exactly as every
// sanctioned writer's do, so a test's forgery is not caught by a disagreement
// no real attacker would leave behind.
func writeForgedGrantLink(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, data map[string]any) {
	t.Helper()
	permissionKey, roleKey, ok := GrantLinkKeyParts(key)
	if !ok {
		t.Fatalf("%q is not a grant-edge key", key)
	}
	doc := map[string]any{
		"class": "grantedBy", "isDeleted": false, "data": data,
		"sourceVertex": permissionKey, "targetVertex": roleKey, "localName": "grantedBy",
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal forged grant link %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, CoreBucket, key, raw); err != nil {
		t.Fatalf("KVPut forged grant link %s: %v", key, err)
	}
}

// writeRawLink writes an ARBITRARY raw body at a link key, bypassing
// writeForgedGrantLink's typed document and its grant-key check — the way to
// construct an envelope this pass cannot decode at all, and the way to put a
// non-grant `lnk.permission.*` key in its path.
func writeRawLink(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, rawBody string) {
	t.Helper()
	if _, err := conn.KVPut(ctx, CoreBucket, key, []byte(rawBody)); err != nil {
		t.Fatalf("KVPut raw link %s: %v", key, err)
	}
}

// declareKey appends key to an installed package's manifest declaredKeys — the
// attacker-authored-manifest half of the comparison this reconciler makes
// against Core KV, and the fact that turns an unrecognized edge into a
// declared one.
func declareKey(t *testing.T, ctx context.Context, conn *substrate.Conn, manifestKey, key string) {
	t.Helper()
	patchDoc(t, ctx, conn, manifestKey, func(doc map[string]any) {
		data, _ := doc["data"].(map[string]any)
		keys, _ := data["declaredKeys"].([]any)
		data["declaredKeys"] = append(keys, key)
	})
}

// liveGrantLinkClassCounts independently re-derives the classification
// LoadPermissionReconciliation's gatherer feeds ReconcileGrantLinks, so a test
// can assert how many kernel/package/runtime/unstamped/unrecognized edges are
// live — a fact the reconciler does not surface for the silent classes
// (kernel-clean and package-clean produce no finding at all).
func liveGrantLinkClassCounts(t *testing.T, ctx context.Context, conn *substrate.Conn) map[GrantProvenance]int {
	t.Helper()
	in, err := gatherPermissionInputs(ctx, conn)
	if err != nil {
		t.Fatalf("gatherPermissionInputs: %v", err)
	}
	if len(in.undecodableGrantLinks) > 0 {
		t.Fatalf("gatherPermissionInputs reported undecodable grant links in a clean fixture: %+v", in.undecodableGrantLinks)
	}
	declaringPackages := make(map[string][]string, len(in.declaredGrantLinks))
	for _, d := range in.declaredGrantLinks {
		declaringPackages[d.Key] = append(declaringPackages[d.Key], d.Package)
	}
	counts := map[GrantProvenance]int{}
	for _, l := range in.liveGrantLinks {
		counts[classifyGrantLink(l, in.kernelGrantLinkKeys, declaringPackages)]++
	}
	return counts
}

// findGrantByClass returns the single finding of the given class, failing the
// test if there is not exactly one.
func findGrantByClass(t *testing.T, findings []GrantFinding, class GrantFindingClass) GrantFinding {
	t.Helper()
	var matches []GrantFinding
	for _, f := range findings {
		if f.Class == class {
			matches = append(matches, f)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("grant findings of class %q = %d, want exactly 1 (all findings: %+v)", class, len(matches), findings)
	}
	return matches[0]
}

// TestLoadPermissionReconciliation_GrantLinks_CleanInstall installs a real
// package through the Processor and asserts the edge plane reads back with
// zero drift, bootstrap's six kernel grant edges, and one package-origin edge
// per GrantsTo entry sampleDef declares.
func TestLoadPermissionReconciliation_GrantLinks_CleanInstall(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	def := sampleDef("0.1.0")
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	if len(got.GrantDrift) != 0 {
		t.Fatalf("GrantDrift on a clean install = %+v, want none", got.GrantDrift)
	}

	counts := liveGrantLinkClassCounts(t, ctx, conn)
	if counts[GrantProvenanceKernel] != len(bootstrap.KernelGrantLinkKeys()) {
		t.Errorf("kernel class count = %d, want %d", counts[GrantProvenanceKernel], len(bootstrap.KernelGrantLinkKeys()))
	}
	wantPackage := 0
	for _, p := range def.Permissions {
		wantPackage += len(p.GrantsTo)
	}
	if counts[GrantProvenancePackage] != wantPackage {
		t.Errorf("package class count = %d, want %d (sampleDef's declared grants)", counts[GrantProvenancePackage], wantPackage)
	}

	wantEdge := substrate.LinkKey("permission", PermissionID("sample-pkg", "SampleOp", "any"), "grantedBy", "role", bootstrap.RoleOperatorID)
	keys, ok := got.DeclaredGrantLinksByPackage["sample-pkg"]
	if !ok {
		t.Fatalf("DeclaredGrantLinksByPackage[%q] missing, want an entry (installed package)", "sample-pkg")
	}
	if !slices.Contains(keys, wantEdge) {
		t.Errorf("DeclaredGrantLinksByPackage[%q] = %v, want it to contain %q", "sample-pkg", keys, wantEdge)
	}
	// The `forOperation` edges an install also writes share the
	// `lnk.permission.` prefix and must not be collected as grants.
	for _, k := range keys {
		if _, _, ok := GrantLinkKeyParts(k); !ok {
			t.Errorf("DeclaredGrantLinksByPackage[%q] carries %q, which is not a grant edge", "sample-pkg", k)
		}
	}
}

// TestLoadPermissionReconciliation_DetectsForgedGrantEdge writes a `grantedBy`
// edge onto a permission no package declares, granting it to a role of the
// attacker's choosing. No permission vertex is forged, so the vertex plane
// sees nothing at all. With no origin and no declaration the edge must be
// drift — and must NOT also surface as an unstamped notice (the field-deletion
// vector: omitting `origin` is the cheapest way to try to look like a legacy
// install). The target is deliberately not a kernel permission: that shape is
// claimed earlier by kernelRegrant, and this test is about the origin arms.
func TestLoadPermissionReconciliation_DetectsForgedGrantEdge(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	attackerRoleID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	targetPermID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	forgedEdge := substrate.LinkKey("permission", targetPermID, "grantedBy", "role", attackerRoleID)
	writeForgedGrantLink(t, ctx, conn, forgedEdge, map[string]any{})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	if got.HasDrift() != true {
		t.Fatalf("HasDrift() = false, want true — a forged grant edge is drift")
	}
	if len(got.Drift) != 0 {
		t.Errorf("the vertex plane reported %+v; a forged EDGE forges no vertex, so only the edge plane should fire", got.Drift)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingUndeclared)
	if f.Key != forgedEdge {
		t.Errorf("undeclared finding Key = %q, want %q", f.Key, forgedEdge)
	}
	if f.Reason != GrantReasonNoOriginUndeclared {
		t.Errorf("undeclared finding Reason = %q, want %q", f.Reason, GrantReasonNoOriginUndeclared)
	}
	if f.PermissionKey != "vtx.permission."+targetPermID {
		t.Errorf("undeclared finding PermissionKey = %q, want %q", f.PermissionKey, "vtx.permission."+targetPermID)
	}
	for _, n := range got.GrantNotices {
		if n.Key == forgedEdge {
			t.Errorf("forged edge %s appeared as a notice (%+v) as well as drift — it should be drift only", forgedEdge, n)
		}
	}
}

// TestLoadPermissionReconciliation_DetectsForgedGrantEdge_PackageNotInstalled
// stamps the forged edge `package` and names a package that was never
// installed — the arm that fires before declaredKeys is ever consulted. If
// `!installedPackages[l.DeclaredBy]` is disabled, execution falls through to
// the next arm and produces an identical Class/Key/Package finding; only
// Reason distinguishes which check actually fired.
func TestLoadPermissionReconciliation_DetectsForgedGrantEdge_PackageNotInstalled(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	attackerRoleID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	targetPermID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	forgedEdge := substrate.LinkKey("permission", targetPermID, "grantedBy", "role", attackerRoleID)
	writeForgedGrantLink(t, ctx, conn, forgedEdge, map[string]any{
		"origin": PermissionOriginPackage, "declaredBy": "never-installed-pkg",
	})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingUndeclared)
	if f.Key != forgedEdge {
		t.Errorf("undeclared finding Key = %q, want %q", f.Key, forgedEdge)
	}
	if f.Reason != GrantReasonPackageNotInstalled {
		t.Errorf("undeclared finding Reason = %q, want %q", f.Reason, GrantReasonPackageNotInstalled)
	}
}

// TestLoadPermissionReconciliation_DetectsForgedGrantEdge_KeyNotDeclared
// stamps the forged edge with an INSTALLED package's name, which its
// declaredKeys does not carry — the second undeclared arm.
func TestLoadPermissionReconciliation_DetectsForgedGrantEdge_KeyNotDeclared(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	attackerRoleID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	targetPermID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	forgedEdge := substrate.LinkKey("permission", targetPermID, "grantedBy", "role", attackerRoleID)
	writeForgedGrantLink(t, ctx, conn, forgedEdge, map[string]any{
		"origin": PermissionOriginPackage, "declaredBy": "sample-pkg",
	})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingUndeclared)
	if f.Key != forgedEdge {
		t.Errorf("undeclared finding Key = %q, want %q", f.Key, forgedEdge)
	}
	if f.Package != "sample-pkg" {
		t.Errorf("undeclared finding Package = %q, want %q", f.Package, "sample-pkg")
	}
	if f.Reason != GrantReasonKeyNotDeclared {
		t.Errorf("undeclared finding Reason = %q, want %q", f.Reason, GrantReasonKeyNotDeclared)
	}
}

// TestLoadPermissionReconciliation_DetectsGrantEdgeKeyMismatch goes one step
// further than _KeyNotDeclared: the attacker also writes the forged edge into
// sample-pkg's own declaredKeys, so both membership tests pass. The edge still
// grants a permission sample-pkg does not declare — the derivation check is
// the only thing left that catches it, and it is what stops a package from
// declaring an edge onto somebody else's permission.
func TestLoadPermissionReconciliation_DetectsGrantEdgeKeyMismatch(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	res, err := inst.Install(ctx, sampleDef("0.1.0"))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	attackerRoleID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	targetPermID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	forgedEdge := substrate.LinkKey("permission", targetPermID, "grantedBy", "role", attackerRoleID)
	writeForgedGrantLink(t, ctx, conn, forgedEdge, map[string]any{
		"origin": PermissionOriginPackage, "declaredBy": "sample-pkg",
	})
	declareKey(t, ctx, conn, res.PackageKey+".manifest", forgedEdge)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingKeyMismatch)
	if f.Key != forgedEdge {
		t.Errorf("keyMismatch finding Key = %q, want %q", f.Key, forgedEdge)
	}
	if f.Package != "sample-pkg" {
		t.Errorf("keyMismatch finding Package = %q, want %q", f.Package, "sample-pkg")
	}
	// Exactly one finding for this key, not also undeclared.
	for _, d := range got.GrantDrift {
		if d.Key == forgedEdge && d.Class != GrantFindingKeyMismatch {
			t.Errorf("forged edge produced an extra finding %+v — should be keyMismatch alone", d)
		}
	}
}

// TestLoadPermissionReconciliation_GrantLinks_RuntimeAndUnstampedAreNotices
// forges a runtime-origin edge (GrantPermission's ratified channel) and a
// no-origin edge whose key IS injected into an installed package's
// declaredKeys (the genuine pre-stamp shape), asserting the gatherer's decode
// of `data.origin` and the declaredKeys membership test together route both to
// notices and neither to drift. Paired with
// _DetectsForgedGrantEdge, which uses the same no-origin body WITHOUT the
// declaredKeys entry: that one difference is the whole unstamped/unrecognized
// split.
func TestLoadPermissionReconciliation_GrantLinks_RuntimeAndUnstampedAreNotices(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	res, err := inst.Install(ctx, sampleDef("0.1.0"))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	runtimePermID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	runtimeEdge := substrate.LinkKey("permission", runtimePermID, "grantedBy", "role", bootstrap.RoleOperatorID)
	writeForgedGrantLink(t, ctx, conn, runtimeEdge, map[string]any{"origin": PermissionOriginRuntime})

	// The unstamped edge grants sample-pkg's OWN permission, to a second role:
	// the derivation check runs on this class too, so an unstamped edge onto a
	// permission its declaring package does not own is drift rather than
	// inventory (_UnstampedEdgeStillRunsTheDerivationCheck pins that).
	secondRoleID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	unstampedEdge := substrate.LinkKey("permission", PermissionID("sample-pkg", "SampleOp", "any"), "grantedBy", "role", secondRoleID)
	writeForgedGrantLink(t, ctx, conn, unstampedEdge, map[string]any{})
	declareKey(t, ctx, conn, res.PackageKey+".manifest", unstampedEdge)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	if len(got.GrantDrift) != 0 {
		t.Fatalf("GrantDrift = %+v, want none — runtime/unstamped are inventory only", got.GrantDrift)
	}
	rf := findGrantByClass(t, got.GrantNotices, GrantFindingRuntimeInventory)
	if rf.Key != runtimeEdge {
		t.Errorf("runtimeInventory finding Key = %q, want %q", rf.Key, runtimeEdge)
	}
	uf := findGrantByClass(t, got.GrantNotices, GrantFindingUnstampedInventory)
	if uf.Key != unstampedEdge {
		t.Errorf("unstampedInventory finding Key = %q, want %q", uf.Key, unstampedEdge)
	}
}

// TestLoadPermissionReconciliation_TombstonedGrantEdgeIsNotLive soft-deletes
// sample-pkg's own grant edge, the state RevokePermission leaves behind — it
// tombstones rather than removing the key. A tombstoned edge is out of the
// capability walk, so it must not be read as a live grant; it is the declared
// side that reports it, as a revoked notice rather than a missing-edge drift.
func TestLoadPermissionReconciliation_TombstonedGrantEdgeIsNotLive(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	edge := substrate.LinkKey("permission", PermissionID("sample-pkg", "SampleOp", "any"), "grantedBy", "role", bootstrap.RoleOperatorID)
	tombstoneKey(t, ctx, conn, edge)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	if len(got.GrantDrift) != 0 {
		t.Fatalf("GrantDrift = %+v, want none — a tombstoned declared edge is a respected revocation, not missing", got.GrantDrift)
	}
	f := findGrantByClass(t, got.GrantNotices, GrantFindingRevoked)
	if f.Key != edge {
		t.Errorf("revoked finding Key = %q, want %q", f.Key, edge)
	}
	if f.Package != "sample-pkg" {
		t.Errorf("revoked finding Package = %q, want %q", f.Package, "sample-pkg")
	}
	counts := liveGrantLinkClassCounts(t, ctx, conn)
	if counts[GrantProvenancePackage] != 0 {
		t.Errorf("package class count = %d, want 0 — the only package edge is tombstoned, so nothing is live", counts[GrantProvenancePackage])
	}
}

// TestLoadPermissionReconciliation_DetectsGrantKernelMissing tombstones one of
// bootstrap's six kernel grant edges: a primordial permission that no longer
// reaches the operator role.
func TestLoadPermissionReconciliation_DetectsGrantKernelMissing(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	kernelEdge := bootstrap.KernelGrantLinkKeys()[0]
	tombstoneKey(t, ctx, conn, kernelEdge)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingKernelMissing)
	if f.Key != kernelEdge {
		t.Errorf("kernelMissing finding Key = %q, want %q", f.Key, kernelEdge)
	}
}

// TestLoadPermissionReconciliation_DetectsGrantEdgeMissing injects a grant-edge
// key into sample-pkg's declaredKeys that is backed by no document at all — a
// partial install, or a hard purge outside the Processor's soft-tombstone
// path. Distinct from the tombstoned case above, which IS backed by a
// document.
func TestLoadPermissionReconciliation_DetectsGrantEdgeMissing(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	res, err := inst.Install(ctx, sampleDef("0.1.0"))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	phantomPermID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	phantomEdge := substrate.LinkKey("permission", phantomPermID, "grantedBy", "role", bootstrap.RoleOperatorID)
	declareKey(t, ctx, conn, res.PackageKey+".manifest", phantomEdge)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingMissing)
	if f.Key != phantomEdge {
		t.Errorf("missing finding Key = %q, want %q", f.Key, phantomEdge)
	}
	if f.Package != "sample-pkg" {
		t.Errorf("missing finding Package = %q, want %q", f.Package, "sample-pkg")
	}
}

// TestLoadPermissionReconciliation_DetectsGrantEdgeUndecodable overwrites
// sample-pkg's real, declared grant edge with a body that is not JSON at all.
// No authorization path reads `origin` off an edge, so the edge still
// authorizes; a decode failure must surface as its own drift finding rather
// than as silence — and never ALSO as missing, since the key is occupied.
func TestLoadPermissionReconciliation_DetectsGrantEdgeUndecodable(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	edge := substrate.LinkKey("permission", PermissionID("sample-pkg", "SampleOp", "any"), "grantedBy", "role", bootstrap.RoleOperatorID)
	writeRawLink(t, ctx, conn, edge, `not json at all {{{`)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingUndecodable)
	if f.Key != edge {
		t.Errorf("undecodable finding Key = %q, want %q", f.Key, edge)
	}
	for _, d := range got.GrantDrift {
		if d.Key == edge && d.Class != GrantFindingUndecodable {
			t.Errorf("the edge produced an extra drift finding %+v — should be undecodable alone; missing must not also fire for an occupied-but-unreadable key", d)
		}
	}
}

// TestGrantLinkKeyParts pins which `lnk.permission.*` keys this plane owns.
// The prefix is shared with the `forOperation` edges an install also writes,
// which express no grant at all — reading one as a grant would report a
// legitimate install artifact as an unaccountable escalation.
func TestGrantLinkKeyParts(t *testing.T) {
	const permID = "SWZpB4hde9askSkMWU7s"
	const roleID = "u3RxRfKybLhhhsLe3NiT"
	cases := []struct {
		name           string
		key            string
		wantOK         bool
		wantPermission string
		wantRole       string
	}{
		{
			name: "a grant edge", key: substrate.LinkKey("permission", permID, "grantedBy", "role", roleID),
			wantOK: true, wantPermission: "vtx.permission." + permID, wantRole: "vtx.role." + roleID,
		},
		{name: "a forOperation edge shares the prefix and is not a grant", key: "lnk.permission." + permID + ".forOperation.meta." + roleID},
		{name: "a grant onto something that is not a role", key: "lnk.permission." + permID + ".grantedBy.identity." + roleID},
		{name: "a holdsRole edge", key: "lnk.identity." + permID + ".holdsRole.role." + roleID},
		{name: "a permission vertex", key: "vtx.permission." + permID},
		{name: "empty", key: ""},
		{name: "unresolved ids", key: "lnk.permission..grantedBy.role."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			permissionKey, roleKey, ok := GrantLinkKeyParts(tc.key)
			if ok != tc.wantOK {
				t.Fatalf("GrantLinkKeyParts(%q) ok = %v, want %v", tc.key, ok, tc.wantOK)
			}
			if permissionKey != tc.wantPermission || roleKey != tc.wantRole {
				t.Errorf("GrantLinkKeyParts(%q) = (%q, %q), want (%q, %q)", tc.key, permissionKey, roleKey, tc.wantPermission, tc.wantRole)
			}
		})
	}
}

// TestLoadPermissionReconciliation_ForOperationEdgeIsNotAGrant puts a live,
// perfectly decodable `lnk.permission.<id>.forOperation.meta.<id>` edge in the
// enumeration's path. It shares the `lnk.permission.` prefix, carries no
// origin, and is declared by nobody — everything an unaccountable grant looks
// like — but it grants nothing, so this plane must not classify it at all.
func TestLoadPermissionReconciliation_ForOperationEdgeIsNotAGrant(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	permID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	metaID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	edge := "lnk.permission." + permID + ".forOperation.meta." + metaID
	writeRawLink(t, ctx, conn, edge, `{"class":"forOperation","isDeleted":false,"data":{},`+
		`"sourceVertex":"vtx.permission.`+permID+`","targetVertex":"vtx.meta.`+metaID+`","localName":"forOperation"}`)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	for _, f := range append(append([]GrantFinding{}, got.GrantDrift...), got.GrantNotices...) {
		if f.Key == edge {
			t.Errorf("forOperation edge %s produced a grant finding %+v — it expresses no grant", edge, f)
		}
	}
	if len(got.GrantDrift) != 0 {
		t.Errorf("GrantDrift = %+v, want none", got.GrantDrift)
	}
}

// TestGrantLinkShaped pins the enumeration filter's looser grammar against
// GrantLinkKeyParts' strict one. The gap between them is deliberate: a key
// written straight into Core KV never passed the Processor's key validation,
// so enumerating on the strict grammar would drop exactly the shapes this
// plane exists to notice.
func TestGrantLinkShaped(t *testing.T) {
	const permID = "SWZpB4hde9askSkMWU7s"
	const roleID = "u3RxRfKybLhhhsLe3NiT"
	cases := []struct {
		name       string
		key        string
		wantShaped bool
		wantStrict bool
	}{
		{name: "a well-formed grant edge", key: "lnk.permission." + permID + ".grantedBy.role." + roleID, wantShaped: true, wantStrict: true},
		{name: "grant-shaped with a short permission id", key: "lnk.permission.short.grantedBy.role." + roleID, wantShaped: true},
		{name: "grant-shaped with an out-of-alphabet role id", key: "lnk.permission." + permID + ".grantedBy.role.OOOOOOOOOOOOOOOOOOOO", wantShaped: true},
		{name: "grant-shaped with empty ids (unloaded bootstrap globals)", key: "lnk.permission..grantedBy.role.", wantShaped: true},
		{name: "a forOperation edge", key: "lnk.permission." + permID + ".forOperation.meta." + roleID},
		{name: "a holdsRole edge", key: "lnk.identity." + permID + ".holdsRole.role." + roleID},
		{name: "a permission vertex", key: "vtx.permission." + permID},
		{name: "empty", key: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := grantLinkShaped(tc.key); got != tc.wantShaped {
				t.Errorf("grantLinkShaped(%q) = %v, want %v", tc.key, got, tc.wantShaped)
			}
			if _, _, got := GrantLinkKeyParts(tc.key); got != tc.wantStrict {
				t.Errorf("GrantLinkKeyParts(%q) ok = %v, want %v", tc.key, got, tc.wantStrict)
			}
		})
	}
}

// TestDecodePermissionVertices_ListedButNotReturned exercises the arm no live
// bucket can stage without racing it: a key the list returned that the batched
// read did not. That is a torn view, and the key must be reported occupied-but-
// unreadable rather than silently dropped — a dropped key opts out of every
// finding class this reconciler produces.
func TestDecodePermissionVertices_ListedButNotReturned(t *testing.T) {
	present := "vtx.permission.SWZpB4hde9askSkMWU7s"
	torn := "vtx.permission.2QuyoBDkFGC5kdgrYkTF"
	entries := map[string]*substrate.KVEntry{
		present: {Value: []byte(`{"class":"permission","isDeleted":false,"data":{"operationType":"SampleOp","scope":"any"}}`)},
	}

	live, docs, undecodableKeys, findings := decodePermissionVertices([]string{present, torn}, entries)
	if len(live) != 1 || live[0].Key != present {
		t.Errorf("live = %+v, want only %s", live, present)
	}
	if _, ok := docs[torn]; ok {
		t.Errorf("docs must not carry a key the read did not return: %v", docs)
	}
	if !undecodableKeys[torn] {
		t.Errorf("undecodableKeys = %v, want it to name %s", undecodableKeys, torn)
	}
	f := findByClass(t, findings, FindingUndecodable)
	if f.Key != torn {
		t.Errorf("undecodable finding Key = %q, want %q", f.Key, torn)
	}
	if !strings.Contains(f.Detail, "batched read did not return it") {
		t.Errorf("detail should name the torn-view cause, not a decode failure: %q", f.Detail)
	}
}

// TestDecodeGrantLinks_ListedButNotReturned is the edge plane's twin of
// TestDecodePermissionVertices_ListedButNotReturned.
func TestDecodeGrantLinks_ListedButNotReturned(t *testing.T) {
	present := substrate.LinkKey("permission", "SWZpB4hde9askSkMWU7s", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT")
	torn := substrate.LinkKey("permission", "2QuyoBDkFGC5kdgrYkTF", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT")
	entries := map[string]*substrate.KVEntry{
		present: {Value: []byte(`{"class":"grantedBy","isDeleted":false,"data":{"origin":"package","declaredBy":"sample-pkg"},` +
			`"sourceVertex":"vtx.permission.SWZpB4hde9askSkMWU7s","targetVertex":"vtx.role.u3RxRfKybLhhhsLe3NiT"}`)},
	}

	live, docs, undecodableKeys, findings := decodeGrantLinks([]string{present, torn}, entries)
	if len(live) != 1 || live[0].Key != present {
		t.Errorf("live = %+v, want only %s", live, present)
	}
	if live[0].DeclaredBy != "sample-pkg" {
		t.Errorf("live[0].DeclaredBy = %q, want %q", live[0].DeclaredBy, "sample-pkg")
	}
	if _, ok := docs[torn]; ok {
		t.Errorf("docs must not carry a key the read did not return: %v", docs)
	}
	if !undecodableKeys[torn] {
		t.Errorf("undecodableKeys = %v, want it to name %s", undecodableKeys, torn)
	}
	f := findGrantByClass(t, findings, GrantFindingUndecodable)
	if f.Key != torn {
		t.Errorf("undecodable finding Key = %q, want %q", f.Key, torn)
	}
	if !strings.Contains(f.Detail, "batched read did not return it") {
		t.Errorf("detail should name the torn-view cause, not a decode failure: %q", f.Detail)
	}
}

// TestLoadPermissionReconciliation_RuntimeStampedKernelRegrantIsDrift is the
// vector the notice arms were blind to. The forger writes no permission vertex
// and no manifest entry: one edge conferring the kernel's InstallPackage
// permission on a role of their choosing, stamped `{"origin":"runtime"}`
// because `origin` is theirs to write. Under a classifier that reads the stamp
// first this is `runtimeInventory` — a notice, invisible to HasDrift, never
// reaching any semantic check. It must be drift.
func TestLoadPermissionReconciliation_RuntimeStampedKernelRegrantIsDrift(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	attackerRoleID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	forgedEdge := substrate.LinkKey("permission", bootstrap.PermInstallPackageID, "grantedBy", "role", attackerRoleID)
	writeForgedGrantLink(t, ctx, conn, forgedEdge, map[string]any{"origin": PermissionOriginRuntime})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	if !got.HasDrift() {
		t.Fatalf("HasDrift() = false, want true — an edge granting a kernel permission is drift whatever it stamps itself")
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingKernelRegrant)
	if f.Key != forgedEdge {
		t.Errorf("kernelRegrant finding Key = %q, want %q", f.Key, forgedEdge)
	}
	if f.PermissionKey != bootstrap.PermInstallPackageKey {
		t.Errorf("kernelRegrant finding PermissionKey = %q, want %q", f.PermissionKey, bootstrap.PermInstallPackageKey)
	}
	for _, n := range got.GrantNotices {
		if n.Key == forgedEdge {
			t.Errorf("forged edge appeared as a notice (%+v) — the runtime stamp must not buy it an inventory arm", n)
		}
	}
	// One finding, not two: kernelRegrant is decided before classification, so
	// the edge never reaches the undeclared arm as well.
	for _, d := range got.GrantDrift {
		if d.Key == forgedEdge && d.Class != GrantFindingKernelRegrant {
			t.Errorf("forged edge produced an extra finding %+v — should be kernelRegrant alone", d)
		}
	}
}

// TestLoadPermissionReconciliation_DeclaredKernelRegrantIsDrift writes the
// same edge with the strongest declaration an attacker who owns a package's
// manifest can give it: origin `package`, declaredBy an installed package, the
// key in that package's declaredKeys. Every declaredKeys-side test then
// passes, because the attacker wrote both halves. The kernel's own grant
// topology is the one fact not sourced from the manifest.
func TestLoadPermissionReconciliation_DeclaredKernelRegrantIsDrift(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	res, err := inst.Install(ctx, sampleDef("0.1.0"))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	attackerRoleID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	forgedEdge := substrate.LinkKey("permission", bootstrap.PermUpgradePackageID, "grantedBy", "role", attackerRoleID)
	writeForgedGrantLink(t, ctx, conn, forgedEdge, map[string]any{
		"origin": PermissionOriginPackage, "declaredBy": "sample-pkg",
	})
	declareKey(t, ctx, conn, res.PackageKey+".manifest", forgedEdge)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingKernelRegrant)
	if f.Key != forgedEdge {
		t.Errorf("kernelRegrant finding Key = %q, want %q", f.Key, forgedEdge)
	}
}

// TestLoadPermissionReconciliation_UnstampedKernelRegrantIsDrift is the third
// stamp an attacker can choose — none at all — on the same edge.
func TestLoadPermissionReconciliation_UnstampedKernelRegrantIsDrift(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	attackerRoleID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	forgedEdge := substrate.LinkKey("permission", bootstrap.PermCreateMetaVertexID, "grantedBy", "role", attackerRoleID)
	writeForgedGrantLink(t, ctx, conn, forgedEdge, map[string]any{})

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingKernelRegrant)
	if f.Key != forgedEdge {
		t.Errorf("kernelRegrant finding Key = %q, want %q", f.Key, forgedEdge)
	}
}

// TestLoadPermissionReconciliation_UnstampedEdgeStillRunsTheDerivationCheck
// declares a forged edge into sample-pkg's declaredKeys and gives it NO
// origin, the shape that reaches the unstamped notice arm. The permission it
// grants belongs to no package, so the derivation check must still fire: an
// edge is not made trustworthy by omitting the stamp.
func TestLoadPermissionReconciliation_UnstampedEdgeStillRunsTheDerivationCheck(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	res, err := inst.Install(ctx, sampleDef("0.1.0"))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	otherPermID, err := substrate.NewNanoID()
	if err != nil {
		t.Fatalf("NewNanoID: %v", err)
	}
	forgedEdge := substrate.LinkKey("permission", otherPermID, "grantedBy", "role", bootstrap.RoleOperatorID)
	writeForgedGrantLink(t, ctx, conn, forgedEdge, map[string]any{})
	declareKey(t, ctx, conn, res.PackageKey+".manifest", forgedEdge)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingKeyMismatch)
	if f.Key != forgedEdge {
		t.Errorf("keyMismatch finding Key = %q, want %q", f.Key, forgedEdge)
	}
	if f.Package != "sample-pkg" {
		t.Errorf("keyMismatch finding Package = %q, want %q", f.Package, "sample-pkg")
	}
	for _, n := range got.GrantNotices {
		if n.Key == forgedEdge {
			t.Errorf("forged edge appeared as a notice (%+v) — omitting the stamp must not skip the derivation check", n)
		}
	}
}

// TestLoadPermissionReconciliation_MalformedGrantKeyIsReported writes a key
// that sits in the grant-edge namespace but is not a Contract #1 link key at
// all — the Processor's key validation would have refused it, and a direct KV
// write does not go through the Processor. Filtering the enumeration on the
// strict grammar would drop it silently; it must be named.
func TestLoadPermissionReconciliation_MalformedGrantKeyIsReported(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	malformed := "lnk.permission.tooshort.grantedBy.role." + bootstrap.RoleOperatorID
	writeRawLink(t, ctx, conn, malformed,
		`{"class":"grantedBy","isDeleted":false,"data":{},`+
			`"sourceVertex":"vtx.permission.tooshort","targetVertex":"vtx.role.`+bootstrap.RoleOperatorID+`","localName":"grantedBy"}`)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingMalformedKey)
	if f.Key != malformed {
		t.Errorf("malformedKey finding Key = %q, want %q", f.Key, malformed)
	}
}

// TestLoadPermissionReconciliation_UndecodableKernelEdgeIsNotAlsoMissing
// overwrites one of the six kernel grant edges with a body that does not
// decode. The key is OCCUPIED, so "absent from the live set" is false: the
// pass must report undecodable alone, not undecodable plus kernelMissing with
// two remedies for two different states.
func TestLoadPermissionReconciliation_UndecodableKernelEdgeIsNotAlsoMissing(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	kernelEdge := bootstrap.KernelGrantLinkKeys()[0]
	writeRawLink(t, ctx, conn, kernelEdge, `not json at all {{{`)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findGrantByClass(t, got.GrantDrift, GrantFindingUndecodable)
	if f.Key != kernelEdge {
		t.Errorf("undecodable finding Key = %q, want %q", f.Key, kernelEdge)
	}
	for _, d := range got.GrantDrift {
		if d.Key == kernelEdge && d.Class != GrantFindingUndecodable {
			t.Errorf("kernel edge produced an extra drift finding %+v — should be undecodable alone; a document occupies the key", d)
		}
	}
}

// TestLoadPermissionReconciliation_UndecodableKernelPermissionIsNotAlsoMissing
// is the vertex plane's twin of the test above.
func TestLoadPermissionReconciliation_UndecodableKernelPermissionIsNotAlsoMissing(t *testing.T) {
	ctx, conn, inst := newInstallerHarness(t)
	if _, err := inst.Install(ctx, sampleDef("0.1.0")); err != nil {
		t.Fatalf("Install: %v", err)
	}

	writeRawPermission(t, ctx, conn, bootstrap.PermCreateMetaVertexKey, `not json at all {{{`)

	got, err := LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		t.Fatalf("LoadPermissionReconciliation: %v", err)
	}
	f := findByClass(t, got.Drift, FindingUndecodable)
	if f.Key != bootstrap.PermCreateMetaVertexKey {
		t.Errorf("undecodable finding Key = %q, want %q", f.Key, bootstrap.PermCreateMetaVertexKey)
	}
	for _, d := range got.Drift {
		if d.Key == bootstrap.PermCreateMetaVertexKey && d.Class != FindingUndecodable {
			t.Errorf("kernel permission produced an extra drift finding %+v — should be undecodable alone", d)
		}
	}
}

// TestIntersectFindings pins the rule LoadPermissionReconciliation's two reads
// are combined by: a finding survives only if BOTH reads made it about the
// same (class, key). That is what keeps a package lifecycle op — which commits
// atomically but is READ across six unfenced moments — from being reported as
// a forgery, and it is why the gate can be trusted to fail only on findings
// that outlived a whole second observation.
func TestIntersectFindings(t *testing.T) {
	const stableKey = "vtx.permission.SWZpB4hde9askSkMWU7s"
	const transientKey = "vtx.permission.2QuyoBDkFGC5kdgrYkTF"

	earlier := []PermissionFinding{
		{Class: FindingUndeclared, Key: stableKey, Detail: "as first seen"},
		{Class: FindingMissing, Key: transientKey, Package: "half-installed-pkg"},
	}
	latest := []PermissionFinding{
		{Class: FindingUndeclared, Key: stableKey, Detail: "as seen the second time"},
		{Class: FindingKeyMismatch, Key: stableKey},
	}

	got := intersectPermissionFindings(latest, earlier)
	if len(got) != 1 {
		t.Fatalf("intersect = %+v, want exactly the one finding both reads made", got)
	}
	if got[0].Class != FindingUndeclared || got[0].Key != stableKey {
		t.Errorf("survivor = %+v, want %q on %s", got[0], FindingUndeclared, stableKey)
	}
	if got[0].Detail != "as seen the second time" {
		t.Errorf("survivor Detail = %q, want the LATEST read's — the more recent observation is the one an operator acts on", got[0].Detail)
	}

	// The same rule on the edge plane, including the class that is the whole
	// reason for the mechanism: an uninstall read mid-commit shows live edges
	// stamped with a package whose manifest already reads uninstalled.
	edge := substrate.LinkKey("permission", "SWZpB4hde9askSkMWU7s", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT")
	otherEdge := substrate.LinkKey("permission", "2QuyoBDkFGC5kdgrYkTF", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT")
	gotGrant := intersectGrantFindings(
		[]GrantFinding{
			{Class: GrantFindingUndeclared, Key: edge, Reason: GrantReasonPackageNotInstalled},
			{Class: GrantFindingKernelRegrant, Key: otherEdge},
		},
		[]GrantFinding{
			{Class: GrantFindingKernelRegrant, Key: otherEdge},
		},
	)
	if len(gotGrant) != 1 || gotGrant[0].Class != GrantFindingKernelRegrant {
		t.Fatalf("intersect = %+v, want only the kernelRegrant both reads made — the packageNotInstalled finding appeared in one read only", gotGrant)
	}
}

// TestReconcileTwice pins the read structure LoadPermissionReconciliation is
// built on: TWO reads, and a finding survives only if both produced it. The
// hazard is not hypothetical — a package lifecycle op commits atomically
// (uninstall tombstones the manifest and every declared key together) while
// this pass reads it across six unfenced moments, so a single read that
// straddles the commit sees live edges stamped with a package whose manifest
// already reads uninstalled: field for field, a forged edge naming a package
// that was never installed. A gate must not fail on that.
func TestReconcileTwice(t *testing.T) {
	const forgedKey = "vtx.permission.SWZpB4hde9askSkMWU7s"
	edge := substrate.LinkKey("permission", "2QuyoBDkFGC5kdgrYkTF", "grantedBy", "role", "u3RxRfKybLhhhsLe3NiT")

	// The first read straddles an uninstall: it reports the transient
	// undeclared/missing findings a half-committed lifecycle op produces,
	// alongside one genuine forgery. The second read, after the commit, sees
	// only the forgery.
	reads := []PermissionReconciliation{
		{
			Drift:      []PermissionFinding{{Class: FindingUndeclared, Key: forgedKey}, {Class: FindingMissing, Key: "vtx.permission.6p5Esr7taUqEp7j61FuE", Package: "going-away-pkg"}},
			GrantDrift: []GrantFinding{{Class: GrantFindingUndeclared, Key: edge, Reason: GrantReasonPackageNotInstalled}},
			Notices:    []PermissionFinding{{Class: FindingRevoked, Key: forgedKey}},
		},
		{
			Drift:                       []PermissionFinding{{Class: FindingUndeclared, Key: forgedKey}},
			GrantDrift:                  nil,
			DeclaredKeysByPackage:       map[string][]string{"sample-pkg": {forgedKey}},
			DeclaredGrantLinksByPackage: map[string][]string{"sample-pkg": {edge}},
		},
	}
	calls := 0
	got, err := reconcileTwice(func() (PermissionReconciliation, error) {
		rec := reads[calls]
		calls++
		return rec, nil
	})
	if err != nil {
		t.Fatalf("reconcileTwice: %v", err)
	}
	if calls != 2 {
		t.Fatalf("read called %d time(s), want 2 — one unsynchronized observation cannot tell a lifecycle op from a forgery", calls)
	}
	if len(got.Drift) != 1 || got.Drift[0].Key != forgedKey {
		t.Errorf("Drift = %+v, want only the finding both reads made (%s)", got.Drift, forgedKey)
	}
	if len(got.GrantDrift) != 0 {
		t.Errorf("GrantDrift = %+v, want none — the packageNotInstalled finding appeared in the straddling read only", got.GrantDrift)
	}
	if len(got.Notices) != 0 {
		t.Errorf("Notices = %+v, want none — notices are intersected on the same rule as drift", got.Notices)
	}
	if _, ok := got.DeclaredGrantLinksByPackage["sample-pkg"]; !ok {
		t.Errorf("DeclaredGrantLinksByPackage = %v, want the SECOND read's map — it is an inventory for the registry-anchor pass, not a finding", got.DeclaredGrantLinksByPackage)
	}
}
