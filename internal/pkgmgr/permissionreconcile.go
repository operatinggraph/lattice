package pkgmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// PermissionOriginPackage and PermissionOriginRuntime are the two wire values
// a permission vertex's `data.origin` field carries (Contract #6 §6.1): a
// package-declared grant (build.go's permission mutation) versus a
// runtime-minted one (packages/rbac-domain/ddls.go's CreatePermission DDL —
// Starlark-side, not touched here). Named so the one Go reader that must tell
// them apart never repeats the bare literal.
const (
	PermissionOriginPackage = "package"
	PermissionOriginRuntime = "runtime"
)

// PermissionProvenance classifies a live `vtx.permission.*` vertex by how it
// entered Core KV. Package-vs-runtime (Contract #6 §6.1) is two of three: the
// six primordial permissions bootstrap seeds (internal/bootstrap/nanoid.go)
// carry `protected: true` and no `origin` at all, and a permission installed
// before Inc 3 stamped provenance carries no `origin` either without being
// kernel. Every live vertex is classified into exactly one of the four.
type PermissionProvenance string

const (
	// PermissionProvenanceKernel is one of bootstrap's six primordial
	// permission keys. Declared by no manifest, legitimate, reconciled
	// against the constant key set rather than any package.
	PermissionProvenanceKernel PermissionProvenance = "kernel"
	// PermissionProvenancePackage is `origin == "package"` — a permission a
	// package manifest declared at install.
	PermissionProvenancePackage PermissionProvenance = "package"
	// PermissionProvenanceRuntime is `origin == "runtime"` — the second
	// grant channel (rbac-domain's CreatePermission), ratified as legitimate
	// (grant-provenance-runtime-permission-minting-design.md Branch A) and
	// never reconciled against a manifest.
	PermissionProvenanceRuntime PermissionProvenance = "runtime"
	// PermissionProvenanceUnstamped is a live permission carrying no
	// `origin` that is also not one of the kernel's six — a pre-provenance-
	// stamp package install. Healable by upgrading the declaring package.
	PermissionProvenanceUnstamped PermissionProvenance = "unstamped"
)

// LivePermission is one `vtx.permission.<id>` vertex read from Core KV,
// decoded down to the fields the reconciler classifies on.
type LivePermission struct {
	Key           string // vtx.permission.<id>
	OperationType string
	Scope         string
	Origin        string // data.origin verbatim: "" | "package" | "runtime"
	DeclaredBy    string // data.declaredBy — set on package-origin vertices only
}

// DeclaredPermission is one permission key an installed package's manifest
// declaredKeys names — the declared side's actual identity (a package's
// `.manifest` aspect records exactly this: which keys it wrote, not the
// (operationType, scope) that produced them). OperationType/Scope are
// optional enrichment ONLY, populated when a permission-vertex snapshot
// happens to be available for Key (live or tombstoned — see
// LoadPermissionReconciliation): they widen Detail on a finding, never
// participate in classification. A DeclaredPermission with both blank is
// still a complete, reconcilable fact — Package + Key identify it fully.
type DeclaredPermission struct {
	Package       string
	Key           string // vtx.permission.<id>
	OperationType string
	Scope         string
}

// PermissionFindingClass is the machine-readable class of one reconciliation
// finding. Never a formatted message — a caller (a CI gate, a test) asserts
// on the class, not on Detail's prose.
type PermissionFindingClass string

const (
	// FindingUndeclared is drift: a package-origin permission whose
	// declaredBy either names no installed package, or names one whose
	// declaredKeys does not include this vertex's own key — the row's
	// original ask, a permission minted outside the package plane wearing a
	// package's provenance.
	FindingUndeclared PermissionFindingClass = "undeclared"
	// FindingKeyMismatch is drift: a package-origin vertex whose own key
	// does not equal PermissionID(declaredBy, operationType, scope) — a body
	// claiming a declaration its key does not derive.
	FindingKeyMismatch PermissionFindingClass = "keyMismatch"
	// FindingMissing is drift: a declared permission key (from an installed
	// package's declaredKeys) with no live vertex at that key — the §5.1
	// non-durable-revoke case, and also a partial/interrupted install or a
	// hard purge outside the Processor's soft-tombstone path.
	FindingMissing PermissionFindingClass = "missing"
	// FindingKernelMissing is drift: one of bootstrap's six primordial
	// permission keys is absent from the live set.
	FindingKernelMissing PermissionFindingClass = "kernelMissing"
	// FindingRuntimeInventory is a notice, never drift: a live runtime-origin
	// permission — Branch A's ratified second grant channel.
	FindingRuntimeInventory PermissionFindingClass = "runtimeInventory"
	// FindingUnstampedInventory is a notice, never drift: a live permission
	// with no origin that is not one of the kernel's six — a pre-stamp
	// package install, remedied by upgrading the declaring package.
	FindingUnstampedInventory PermissionFindingClass = "unstampedInventory"
)

// PermissionFinding is one classified fact the reconciler reports. Class is
// the assertable surface; Key names the permission key the finding concerns
// (the live vertex for every class except a pure `missing`, which has no
// live vertex — there Key is the declared key that is absent);
// Package/OperationType/Scope name the declaration the finding concerns (set
// on undeclared/keyMismatch always, and on missing when a tuple happened to
// be recoverable — see DeclaredPermission's doc comment). Detail is prose for
// a human reading the gate's output, never something another program parses.
type PermissionFinding struct {
	Class         PermissionFindingClass
	Key           string
	Package       string
	OperationType string
	Scope         string
	Detail        string
}

// PermissionReconciliation is ReconcilePermissions's full answer. Drift and
// Notices are two separate slices — never one list a caller filters by class
// — so "is there drift?" never needs string matching.
type PermissionReconciliation struct {
	Drift   []PermissionFinding
	Notices []PermissionFinding
}

// HasDrift reports whether a gate consuming this reconciliation must fail.
func (r PermissionReconciliation) HasDrift() bool { return len(r.Drift) > 0 }

// classifyLivePermission sorts one live vertex into exactly one of the four
// PermissionProvenance classes (§11.3): key membership in kernelKeys takes
// priority over the vertex's own claimed origin, because kernel status is a
// property of WHICH key this is, not of what the document at that key says.
func classifyLivePermission(p LivePermission, kernelKeys map[string]bool) PermissionProvenance {
	if kernelKeys[p.Key] {
		return PermissionProvenanceKernel
	}
	switch p.Origin {
	case PermissionOriginPackage:
		return PermissionProvenancePackage
	case PermissionOriginRuntime:
		return PermissionProvenanceRuntime
	default:
		return PermissionProvenanceUnstamped
	}
}

// ReconcilePermissions classifies every live permission vertex and every
// declared permission key against each other, per §11.3 of the fire brief:
// four provenance classes, four drift classes
// (undeclared/keyMismatch/missing/kernelMissing), two notice classes
// (runtime/unstamped). Pure — no I/O; LoadPermissionReconciliation is the
// Core-KV gatherer that feeds it.
//
// The declared identity is DeclaredPermission.Key — exactly the granularity
// a package's own declaredKeys record carries (no (operationType, scope)
// tuple is reconstructed or re-derived from it): `undeclared` and `missing`
// are both plain key-membership tests, so neither depends on a tuple ever
// having been recoverable. `keyMismatch` is the one check that still derives
// a key, from the LIVE vertex's own claimed fields — the self-consistency
// question "does this body's key match what it claims" is orthogonal to
// whether that key is a declared one. Scope is used byte-for-byte, exactly as
// installer.go's own key derivation uses it: no default is applied here,
// because none is applied there.
//
// installedPackages names every currently-installed (non-tombstoned)
// package by its manifest name; kernelKeys is bootstrap's six primordial
// permission keys. Output is sorted (Class, Key, Package, OperationType,
// Scope) so two calls with the same logical input produce byte-identical
// output regardless of live/declared slice order — a gate that reports in
// map order is untestable.
func ReconcilePermissions(live []LivePermission, declared []DeclaredPermission, installedPackages map[string]bool, kernelKeys map[string]bool) PermissionReconciliation {
	declaredKeysByPkg := make(map[string]map[string]bool, len(declared))
	for _, d := range declared {
		set, ok := declaredKeysByPkg[d.Package]
		if !ok {
			set = map[string]bool{}
			declaredKeysByPkg[d.Package] = set
		}
		set[d.Key] = true
	}

	liveKeys := make(map[string]bool, len(live))
	remainingKernel := make(map[string]bool, len(kernelKeys))
	for k := range kernelKeys {
		remainingKernel[k] = true
	}

	var drift, notices []PermissionFinding

	for _, p := range live {
		liveKeys[p.Key] = true
		switch classifyLivePermission(p, kernelKeys) {
		case PermissionProvenanceKernel:
			delete(remainingKernel, p.Key)

		case PermissionProvenanceRuntime:
			notices = append(notices, PermissionFinding{
				Class: FindingRuntimeInventory, Key: p.Key,
				OperationType: p.OperationType, Scope: p.Scope,
				Detail: fmt.Sprintf("%s is runtime-origin (%s:%s) — the ratified second grant channel, inventory only", p.Key, p.OperationType, p.Scope),
			})

		case PermissionProvenanceUnstamped:
			notices = append(notices, PermissionFinding{
				Class: FindingUnstampedInventory, Key: p.Key,
				OperationType: p.OperationType, Scope: p.Scope,
				Detail: fmt.Sprintf("%s (%s:%s) carries no origin and is not a kernel key — a pre-provenance-stamp package install, heal by upgrading its declaring package", p.Key, p.OperationType, p.Scope),
			})

		case PermissionProvenancePackage:
			switch {
			case !installedPackages[p.DeclaredBy]:
				drift = append(drift, PermissionFinding{
					Class: FindingUndeclared, Key: p.Key, Package: p.DeclaredBy,
					OperationType: p.OperationType, Scope: p.Scope,
					Detail: fmt.Sprintf("%s declaredBy %q, which is not an installed package", p.Key, p.DeclaredBy),
				})
			case !declaredKeysByPkg[p.DeclaredBy][p.Key]:
				drift = append(drift, PermissionFinding{
					Class: FindingUndeclared, Key: p.Key, Package: p.DeclaredBy,
					OperationType: p.OperationType, Scope: p.Scope,
					Detail: fmt.Sprintf("%s declaredBy %q, whose declaredKeys does not include this key", p.Key, p.DeclaredBy),
				})
			}
			expectedKey := "vtx.permission." + PermissionID(p.DeclaredBy, p.OperationType, p.Scope)
			if expectedKey != p.Key {
				drift = append(drift, PermissionFinding{
					Class: FindingKeyMismatch, Key: p.Key, Package: p.DeclaredBy,
					OperationType: p.OperationType, Scope: p.Scope,
					Detail: fmt.Sprintf("%s claims declaredBy %q operationType %q scope %q, which derives %s", p.Key, p.DeclaredBy, p.OperationType, p.Scope, expectedKey),
				})
			}
		}
	}

	for k := range remainingKernel {
		drift = append(drift, PermissionFinding{
			Class: FindingKernelMissing, Key: k,
			Detail: fmt.Sprintf("kernel permission %s is absent from the live set", k),
		})
	}

	for _, d := range declared {
		if liveKeys[d.Key] {
			continue
		}
		detail := fmt.Sprintf("package %q declares permission key %s, which has no live vertex", d.Package, d.Key)
		if d.OperationType != "" {
			detail = fmt.Sprintf("package %q declares permission (%s, %s) at %s, which has no live vertex", d.Package, d.OperationType, d.Scope, d.Key)
		}
		drift = append(drift, PermissionFinding{
			Class: FindingMissing, Key: d.Key, Package: d.Package,
			OperationType: d.OperationType, Scope: d.Scope,
			Detail: detail,
		})
	}

	sortPermissionFindings(drift)
	sortPermissionFindings(notices)

	return PermissionReconciliation{Drift: drift, Notices: notices}
}

func sortPermissionFindings(findings []PermissionFinding) {
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if a.Package != b.Package {
			return a.Package < b.Package
		}
		if a.OperationType != b.OperationType {
			return a.OperationType < b.OperationType
		}
		return a.Scope < b.Scope
	})
}

// permissionDoc is the partial decode of a `vtx.permission.<id>` envelope —
// both live and tombstoned bodies decode into this: a tombstone mutation
// carries the prior document forward whole and only flips `isDeleted` +
// the lastModified triplet (internal/processor/step8_commit.go's
// buildMutationValue), so `data` survives a soft-delete.
type permissionDoc struct {
	IsDeleted bool `json:"isDeleted"`
	Data      struct {
		OperationType string `json:"operationType"`
		Scope         string `json:"scope"`
		Origin        string `json:"origin"`
		DeclaredBy    string `json:"declaredBy"`
	} `json:"data"`
}

// permissionVertexRootKeys keeps only 3-segment `vtx.permission.<id>` roots
// out of an already-fetched key list. A permission vertex carries no aspects
// today, but if one gained an aspect (`vtx.permission.<id>.<aspect>`) it
// would share this exact prefix and must not be read as a vertex root
// (mirrors installer.go's metaRootKeys, same reason, on vtx.meta.).
// `lnk.permission.*` keys do NOT share this prefix — they start with "lnk.",
// not "vtx." — so they are never a concern here.
func permissionVertexRootKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if strings.Count(k, ".") != 2 {
			continue
		}
		out = append(out, k)
	}
	return out
}

// LoadPermissionReconciliation reads Core KV and calls ReconcilePermissions.
//
// Live side: every `vtx.permission.<id>` vertex root, skipping any whose
// envelope has `isDeleted: true` — KVListKeysPrefix does not filter soft
// tombstones (substrate/kv.go), so every candidate is KVGet'd (batched) and
// inspected.
//
// Declared side: every installed package's manifest `declaredKeys` entries
// with a `vtx.permission.` prefix, decoded via parseManifestNameAndKeys
// (authored_dispatch_scope.go) — the one existing reader of this exact
// envelope, tombstone semantics included, rather than a second hand-rolled
// decoder. A declared key's (operationType, scope) is an OPTIONAL
// enrichment, resolved from the same permission-vertex snapshot the live
// side already read when available (data survives tombstoning, per
// permissionDoc's doc comment) — never required: DeclaredPermission's
// identity is Package + Key, so a declaredKeys entry backed by no document at
// all (a partial install, an interrupted upgrade, a hard purge outside the
// Processor's soft-tombstone path) still reconciles correctly as `missing`,
// just without a recovered tuple to describe it by.
//
// Kernel keys are the six named bootstrap package variables — never a
// hard-coded string — resolved (and validated as actually resolved; see
// kernelPermissionKeys) rather than read as bare zero values.
func LoadPermissionReconciliation(ctx context.Context, conn *substrate.Conn) (PermissionReconciliation, error) {
	permKeys, err := conn.KVListKeysPrefix(ctx, CoreBucket, "vtx.permission.")
	if err != nil {
		return PermissionReconciliation{}, fmt.Errorf("pkgmgr: list permission keys: %w", err)
	}
	permRoots := permissionVertexRootKeys(permKeys)
	permEntries, err := conn.KVGetMulti(ctx, CoreBucket, permRoots)
	if err != nil {
		return PermissionReconciliation{}, fmt.Errorf("pkgmgr: read %d permission vertices: %w", len(permRoots), err)
	}

	docs := make(map[string]permissionDoc, len(permRoots))
	var live []LivePermission
	for _, k := range permRoots {
		entry, present := permEntries[k]
		if !present {
			continue
		}
		var doc permissionDoc
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			continue
		}
		docs[k] = doc
		if doc.IsDeleted {
			continue
		}
		live = append(live, LivePermission{
			Key:           k,
			OperationType: doc.Data.OperationType,
			Scope:         doc.Data.Scope,
			Origin:        doc.Data.Origin,
			DeclaredBy:    doc.Data.DeclaredBy,
		})
	}

	pkgKeys, err := conn.KVListKeysPrefix(ctx, CoreBucket, PackageVertexPrefix)
	if err != nil {
		return PermissionReconciliation{}, fmt.Errorf("pkgmgr: list package keys: %w", err)
	}
	var manifestKeys []string
	for _, k := range pkgKeys {
		if strings.HasSuffix(k, ".manifest") {
			manifestKeys = append(manifestKeys, k)
		}
	}
	pkgEntries, err := conn.KVGetMulti(ctx, CoreBucket, manifestKeys)
	if err != nil {
		return PermissionReconciliation{}, fmt.Errorf("pkgmgr: read %d package manifests: %w", len(manifestKeys), err)
	}

	installedPackages := map[string]bool{}
	var declared []DeclaredPermission
	for _, k := range manifestKeys {
		entry, present := pkgEntries[k]
		if !present {
			continue
		}
		name, declaredKeys, perr := parseManifestNameAndKeys(entry.Value)
		if perr != nil {
			return PermissionReconciliation{}, fmt.Errorf("pkgmgr: parse package manifest %s: %w", k, perr)
		}
		if name == "" {
			// Tombstoned manifest reads as an uninstalled package
			// (parseManifestNameAndKeys's own convention) — declares nothing.
			continue
		}
		installedPackages[name] = true
		for _, dk := range declaredKeys {
			if !strings.HasPrefix(dk, "vtx.permission.") {
				continue
			}
			d := DeclaredPermission{Package: name, Key: dk}
			if doc, ok := docs[dk]; ok {
				d.OperationType = doc.Data.OperationType
				d.Scope = doc.Data.Scope
			}
			declared = append(declared, d)
		}
	}

	kernelKeys, err := kernelPermissionKeys()
	if err != nil {
		return PermissionReconciliation{}, err
	}
	return ReconcilePermissions(live, declared, installedPackages, kernelKeys), nil
}

// kernelPermissionKeySet validates six already-resolved permission keys and
// returns them as the third provenance class's constant set. Split out from
// kernelPermissionKeys so the validation is unit-testable on plain string
// input, without touching internal/bootstrap's package-level state:
// PermCreateMetaVertexKey and its five siblings are package VARIABLES
// (internal/bootstrap/nanoid.go), populated by bootstrap.Load /
// LoadOrGenerate reading lattice.bootstrap.json — not constants, so a process
// that never loaded them carries every one as "". A test that mutated those
// globals directly to exercise the unloaded state would race every other
// test in this package that depends on them already being loaded (Go runs a
// package's tests in one process, and installer_test.go's
// newInstallerHarness calls bootstrap.LoadOrGenerate for exactly that
// dependency) — so the validation lives here, over caller-supplied keys,
// instead.
func kernelPermissionKeySet(keys []string) (map[string]bool, error) {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		// "" is the unloaded zero value; "vtx.permission." is what
		// substrate.VertexKey("permission", "") produces from it — both mean
		// the same thing: nothing was ever loaded into bootstrap's globals.
		if k == "" || k == "vtx.permission." {
			return nil, fmt.Errorf("pkgmgr: kernel permission keys unresolved — bootstrap.Load(BOOTSTRAP_JSON_PATH) must run before LoadPermissionReconciliation")
		}
		set[k] = true
	}
	return set, nil
}

// kernelPermissionKeys is the third provenance class's constant set — the
// six primordial permission keys bootstrap seeds (internal/bootstrap's
// exported package variables, never a hard-coded key string, so a change to
// bootstrap's own derivation cannot silently desync this reconciler from the
// kernel it reconciles against). Returns an error if bootstrap's globals have
// not been populated (kernelPermissionKeySet's doc comment).
func kernelPermissionKeys() (map[string]bool, error) {
	return kernelPermissionKeySet([]string{
		bootstrap.PermCreateMetaVertexKey,
		bootstrap.PermUpdateMetaVertexKey,
		bootstrap.PermTombstoneMetaVertexKey,
		bootstrap.PermInstallPackageKey,
		bootstrap.PermUninstallPackageKey,
		bootstrap.PermUpgradePackageKey,
	})
}
