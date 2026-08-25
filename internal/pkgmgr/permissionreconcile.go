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
// entered Core KV. Package-vs-runtime (Contract #6 §6.1) is two of five: the
// six primordial permissions bootstrap seeds (internal/bootstrap/nanoid.go)
// carry `protected: true` and no `origin` at all, and a live vertex with no
// recognized origin splits in two depending on whether its key is declared
// ANYWHERE — a genuine pre-stamp legacy install versus a forgery that simply
// omitted the field: without that split, a forged vertex's cheapest move is
// deleting one JSON field and reconciling as a legitimate legacy install.
// Every live vertex is classified into exactly one of the five.
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
	// never reconciled against a manifest. The gate can verify only the
	// STAMP, never the channel itself — see PermissionReconciliation's doc
	// comment on that residual.
	PermissionProvenanceRuntime PermissionProvenance = "runtime"
	// PermissionProvenanceUnstamped is a live permission carrying no
	// `origin` WHOSE KEY IS declared by some installed package's
	// declaredKeys — the genuine pre-provenance-stamp install
	// (`step8_commit.go:765`), healable by upgrading the declaring package.
	// The declaredKeys check is load-bearing: without it, omitting `origin`
	// entirely would be the cheapest way to reconcile a forged vertex clean.
	PermissionProvenanceUnstamped PermissionProvenance = "unstamped"
	// PermissionProvenanceUnrecognized is everything else that is not
	// kernel/package/runtime/unstamped: no origin and an undeclared key, or
	// a non-empty origin that is neither "package" nor "runtime" (a typo, a
	// future value, or an unrecognized-outright forgery — that shape is
	// never treated as a legacy install regardless of whether its key
	// happens to be declared). Always drift; see ReconcilePermissions.
	PermissionProvenanceUnrecognized PermissionProvenance = "unrecognized"
)

// LivePermission is one `vtx.permission.<id>` vertex read from Core KV,
// decoded down to the fields the reconciler classifies on.
type LivePermission struct {
	Key           string // vtx.permission.<id>
	OperationType string
	Scope         string
	Origin        string // data.origin verbatim: "" | "package" | "runtime" | anything else
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
	// Tombstoned reports whether Key currently holds a document with
	// `isDeleted: true` — TombstonePermission's respected, durable
	// revocation (design §12, `0bb6daea`: `upgrade.go`'s revival-skip
	// refuses to revive a surviving tombstoned non-`vtx.meta.*` key and
	// names `vtx.permission.*` as ratified scope, and `apply.go` shares the
	// same delta computation, so `--force` does not revive it either).
	// ReconcilePermissions reports a tombstoned declared key as
	// FindingRevoked — a NOTICE, the durable end state of a deliberate
	// narrowing — never FindingMissing, which is reserved for a declared key
	// backed by NO document at all. Conflating the two would make `missing`
	// fire permanently on every sanctioned revoke, with a printed remedy
	// that is never true for that state.
	Tombstoned bool
	// Undecodable reports whether Key WAS found (listed, or present in the
	// batched read) but could not be turned into a usable document — the
	// gatherer already reports this key as FindingUndecodable in its own
	// right (a drift finding), so ReconcilePermissions must not ALSO report
	// FindingMissing for it: "missing" means no document exists, never "a
	// document exists that I could not read". The key is occupied, not
	// absent, so reporting missing for it would be an actively wrong
	// diagnosis carrying a remedy that does not apply to this state.
	Undecodable bool
}

// PermissionFindingClass is the machine-readable class of one reconciliation
// finding. Never a formatted message — a caller (a CI gate, a test) asserts
// on the class, not on Detail's prose.
type PermissionFindingClass string

const (
	// FindingUndeclared is drift: a live permission vertex this reconciler
	// cannot attribute to any installed package's declaredKeys. Three shapes
	// reach it: (1) origin == "package" whose declaredBy names no installed
	// package, (2) origin == "package" whose declaredBy IS installed but
	// whose declaredKeys does not include this vertex's own key, (3) an
	// origin that is empty-and-undeclared or non-empty-and-unrecognized —
	// PermissionProvenanceUnrecognized. The row's original ask, generalized:
	// a permission minted outside the package plane wearing (or omitting)
	// package provenance.
	FindingUndeclared PermissionFindingClass = "undeclared"
	// FindingKeyMismatch is drift: a package-origin vertex, already
	// confirmed declared, whose own key does not equal
	// PermissionID(declaredBy, operationType, scope) — a body claiming a
	// declaration its key does not derive. Checked only once a vertex has
	// cleared the undeclared question (see ReconcilePermissions), so one
	// forged vertex reports exactly one finding.
	FindingKeyMismatch PermissionFindingClass = "keyMismatch"
	// FindingMissing is drift: a declared permission key (from an installed
	// package's declaredKeys) backed by NO document at all — a partial or
	// interrupted install, or a hard purge outside the Processor's
	// soft-tombstone path. Two other states that might look like "no live
	// vertex" are deliberately NOT this class: a declared key backed by a
	// TOMBSTONED document is FindingRevoked (revocations are durable — see
	// DeclaredPermission.Tombstoned's doc comment), and a declared key backed
	// by a document that exists but does not decode is FindingUndecodable
	// only, never also this (see DeclaredPermission.Undecodable's doc
	// comment) — "missing" means no document exists, never "a document
	// exists that I could not read".
	FindingMissing PermissionFindingClass = "missing"
	// FindingKernelMissing is drift: one of bootstrap's six primordial
	// permission keys is absent from the live set.
	FindingKernelMissing PermissionFindingClass = "kernelMissing"
	// FindingUndecodable is drift: a `vtx.permission.*` root this pass could
	// not account for — its envelope failed to decode, or it was listed but
	// not returned by the batched read. Raised only by
	// LoadPermissionReconciliation (the gatherer), never by
	// ReconcilePermissions itself: a key in this state produces neither a
	// LivePermission nor a usable DeclaredPermission enrichment, so the pure
	// function never sees it at all. Reported as drift, never silently
	// skipped: `declaredBy` (and every other classification field) is read by
	// no authorization path (packages/rbac-domain/lenses.go projects only
	// operationType/scope/lanes/origin), so a vertex with one malformed field
	// still authorizes normally, and silently skipping it here would let it
	// opt itself out of every finding class this reconciler produces.
	FindingUndecodable PermissionFindingClass = "undecodable"
	// FindingRuntimeInventory is a notice, never drift: a live runtime-origin
	// permission — Branch A's ratified second grant channel.
	FindingRuntimeInventory PermissionFindingClass = "runtimeInventory"
	// FindingUnstampedInventory is a notice, never drift: a live permission
	// with no origin whose key IS declared by an installed package — a
	// pre-stamp package install, remedied by upgrading the declaring
	// package. (A no-origin vertex whose key is NOT declared anywhere is
	// FindingUndeclared instead — see PermissionProvenanceUnrecognized.)
	FindingUnstampedInventory PermissionFindingClass = "unstampedInventory"
	// FindingRevoked is a notice, never drift: a declared permission key
	// backed by a tombstoned document — TombstonePermission's respected,
	// durable revocation, the correct end state of an operator narrowing an
	// over-broad grant. See DeclaredPermission.Tombstoned's doc comment.
	FindingRevoked PermissionFindingClass = "revoked"
)

// PermissionUndeclaredReason sub-classifies a FindingUndeclared finding by
// WHICH check failed. The four undeclared-producing branches in
// ReconcilePermissions share an identical Class/Key/Package/OperationType/
// Scope and differ only in Detail's free-text prose, which a test must never
// parse to prove which branch actually fired — a distinction the code makes
// has to be carried in a field and asserted from that field, or a branch can
// be disabled entirely (e.g. `case !installedPackages[p.DeclaredBy]:`
// mutated to never match) with every existing assertion still passing,
// because a sibling branch produces an indistinguishable finding. Empty on
// every finding whose Class is not FindingUndeclared.
type PermissionUndeclaredReason string

const (
	// ReasonPackageNotInstalled: a package-origin vertex's declaredBy names
	// no currently-installed package.
	ReasonPackageNotInstalled PermissionUndeclaredReason = "packageNotInstalled"
	// ReasonKeyNotDeclared: a package-origin vertex's declaredBy IS
	// installed, but that package's declaredKeys does not include this
	// vertex's own key.
	ReasonKeyNotDeclared PermissionUndeclaredReason = "keyNotDeclared"
	// ReasonNoOriginUndeclared: no `origin` at all, and this key is not
	// declared by any installed package — not a legacy pre-stamp install.
	ReasonNoOriginUndeclared PermissionUndeclaredReason = "noOriginUndeclared"
	// ReasonUnrecognizedOriginValue: a non-empty `origin` that is neither
	// "package" nor "runtime" — never treated as a legacy shape regardless
	// of declaredKeys membership.
	ReasonUnrecognizedOriginValue PermissionUndeclaredReason = "unrecognizedOriginValue"
)

// PermissionFinding is one classified fact the reconciler reports. Class is
// the assertable surface; Key names the permission key the finding concerns
// (the live vertex for every class except FindingMissing/FindingRevoked,
// which have no live vertex — there Key is the declared key in question);
// Package/OperationType/Scope name the declaration the finding concerns, when
// known (see DeclaredPermission's doc comment on when a tuple is and is not
// recoverable). Reason sub-classifies a FindingUndeclared finding by which
// check failed (see PermissionUndeclaredReason) and is empty on every other
// class. Detail is prose for a human reading the gate's output, never
// something another program parses — and every attacker-controlled field
// folded into it (operationType, scope, origin — declaredBy already did) is
// rendered with %q, not %s: an operationType containing a newline or ANSI
// escape must not be able to forge the gate's own printed output.
type PermissionFinding struct {
	Class         PermissionFindingClass
	Key           string
	Package       string
	OperationType string
	Scope         string
	Reason        PermissionUndeclaredReason
	Detail        string
}

// PermissionReconciliation is ReconcilePermissions's full answer. Drift and
// Notices are two separate slices — never one list a caller filters by class
// — so "is there drift?" never needs string matching.
//
// What a clean PASS here does NOT prove — narrowing the property, not a
// spec violation; §11.7 of the fire brief names all three as non-goals:
//
//   - The declared side is Core KV — a package's own declaredKeys record —
//     not repo source. A package-plane actor writes both halves of this
//     comparison in one operation, so an attacker-authored manifest
//     declaring an attacker-authored permission reconciles perfectly clean.
//     scripts/verify-permission-provenance.go's registry-anchor pass (over
//     internal/pkgregistry, script-only: internal/pkgmgr cannot import it —
//     every packages/* imports pkgmgr, so that is a cycle) closes this for
//     every package the compiled registry knows.
//   - A forgery that omits `origin` is caught only by the declaredKeys
//     membership test (PermissionProvenanceUnrecognized). An attacker who
//     also controls declaredKeys — the same package-plane write the first
//     residual above already assumes honest — defeats this test too.
//
// GrantDrift/GrantNotices carry the same reconciliation over the `grantedBy`
// edge (ReconcileGrantLinks), the object authorization actually travels: an
// edge onto an existing permission confers everything that permission names,
// so a live edge whose origin cannot be accounted for is drift. Two residuals
// are specific to that plane:
//
//   - The declared side is again the package's own declaredKeys record, so an
//     attacker who controls a package's manifest writes both halves of the
//     comparison for edges too — the first residual above, unchanged, on one
//     more object.
//   - The ROLE a declared edge points at is pinned by nothing. A
//     PermissionSpec names its grant target by canonical name
//     (`GrantsTo: []string{"operator"}`), which cmd/lattice-pkg resolves to a
//     role id at install time, so the compiled Definition alone does not name
//     the target role. The derivation check (GrantFindingKeyMismatch) compares
//     only the PERMISSION side of an edge against what its declaring package
//     owns, and accepts whichever role the key names.
//   - `origin` is client-supplied at every authoring channel — the classifier's
//     own input is chosen by whoever wrote the edge. `package` is the only
//     class that leads anywhere: it is reconciled against declaredKeys and the
//     derivation check. `runtime` is asserted by the edge's own body and
//     reconciled against nothing, so an edge that stamps itself `runtime` is
//     inventory whatever it grants; `unstamped` is checked against declaredKeys
//     and the derivation check but not against any stamp. Adding a field is a
//     cheaper laundering than deleting one. The one thing that holds whatever
//     the body claims is the kernel's own grant topology
//     (GrantFindingKernelRegrant): a primordial permission has exactly one
//     legitimate edge, so a second one is drift in every class.
type PermissionReconciliation struct {
	Drift   []PermissionFinding
	Notices []PermissionFinding
	// GrantDrift and GrantNotices are ReconcileGrantLinks' findings over the
	// live `lnk.permission.<id>.grantedBy.role.<id>` population — two separate
	// slices for the same reason Drift and Notices are, and separate from them
	// because the two planes classify different objects and a caller acts on
	// each differently.
	GrantDrift   []GrantFinding
	GrantNotices []GrantFinding
	// DeclaredKeysByPackage names, for every currently-installed package
	// (present even when the package declares zero permission keys), the
	// sorted `vtx.permission.*` keys its declaredKeys record carries — the
	// exact identity ReconcilePermissions computed drift over. Exposed so a
	// caller with an independent anchor for what a package SHOULD declare
	// (scripts/verify-permission-provenance.go's registry pass) can compare
	// against it without re-deriving declaredKeys itself.
	DeclaredKeysByPackage map[string][]string
	// DeclaredGrantLinksByPackage is DeclaredKeysByPackage for the edge plane:
	// for every currently-installed package (present even when it declares no
	// grant edge), the sorted `lnk.permission.<id>.grantedBy.role.<id>` keys
	// its declaredKeys record carries. Exposed for the same registry-anchor
	// reason — a package's compiled Definition names which permissions it
	// grants, so the set of grant edges that may exist under its name is
	// derivable from source.
	DeclaredGrantLinksByPackage map[string][]string
}

// HasDrift reports whether a gate consuming this reconciliation must fail —
// drift on either plane, the permission vertices or the grant edges.
func (r PermissionReconciliation) HasDrift() bool {
	return len(r.Drift) > 0 || len(r.GrantDrift) > 0
}

// classifyLivePermission sorts one live vertex into exactly one of the five
// PermissionProvenance classes: key membership in kernelKeys takes priority
// over the vertex's own claimed origin, because kernel status is a property
// of WHICH key this is, not of what the document at that key says.
// anyDeclaredKey is the union of every installed package's declaredKeys
// (permission-prefixed entries only) — the one piece of data that lets a
// no-origin vertex be told apart from a forgery that simply omitted the
// field: a genuine pre-stamp install's key was ALWAYS written by some
// package's own install batch, so it is always a member.
func classifyLivePermission(p LivePermission, kernelKeys map[string]bool, anyDeclaredKey map[string]bool) PermissionProvenance {
	if kernelKeys[p.Key] {
		return PermissionProvenanceKernel
	}
	switch p.Origin {
	case PermissionOriginPackage:
		return PermissionProvenancePackage
	case PermissionOriginRuntime:
		return PermissionProvenanceRuntime
	case "":
		if anyDeclaredKey[p.Key] {
			return PermissionProvenanceUnstamped
		}
		return PermissionProvenanceUnrecognized
	default:
		// A non-empty origin that is neither "package" nor "runtime" is
		// never treated as a legacy shape, regardless of declaredKeys
		// membership — an unrecognized value is not ambiguous the way an
		// absent one is.
		return PermissionProvenanceUnrecognized
	}
}

// ReconcilePermissions classifies every live permission vertex and every
// declared permission key against each other. Pure — no I/O;
// LoadPermissionReconciliation is the Core-KV gatherer that feeds it (and the
// one place FindingUndecodable is ever raised — see its doc comment).
//
// The declared identity is DeclaredPermission.Key — exactly the granularity
// a package's own declaredKeys record carries (no (operationType, scope)
// tuple is reconstructed or re-derived from it): `undeclared` and `missing`
// are both plain key-membership tests, so neither depends on a tuple ever
// having been recoverable. `keyMismatch` is the one check that still derives
// a key, from the LIVE vertex's own claimed fields, and only once the vertex
// has already cleared the "is this even declared" question: checking it
// unconditionally would double-count one forged vertex as two findings
// carrying two remedies that say the same thing. Scope is used byte-for-byte,
// exactly as installer.go's own key derivation uses it: no default is
// applied here, because none is applied there.
//
// installedPackages names every currently-installed (non-tombstoned)
// package by its manifest name; kernelKeys is bootstrap's six primordial
// permission keys; undecodableKeys are the permission keys the gatherer found
// OCCUPIED but could not read, which the gatherer has already reported as
// FindingUndecodable — a kernel key in that state is not reported absent as
// well, because a document does occupy it. Output is sorted (Class, Key,
// Package, OperationType, Scope, Reason, Detail) — a total order over every
// field a finding carries, so two calls with the same logical input produce
// byte-identical output regardless of live/declared slice order. A gate that
// reports in map order is untestable, and a sort that ties on two distinct
// findings leaves their order to sort.Slice's unstable pivot.
func ReconcilePermissions(live []LivePermission, declared []DeclaredPermission, installedPackages, kernelKeys, undecodableKeys map[string]bool) PermissionReconciliation {
	declaredKeysByPkg := make(map[string]map[string]bool, len(declared))
	anyDeclaredKey := make(map[string]bool, len(declared))
	for _, d := range declared {
		set, ok := declaredKeysByPkg[d.Package]
		if !ok {
			set = map[string]bool{}
			declaredKeysByPkg[d.Package] = set
		}
		set[d.Key] = true
		anyDeclaredKey[d.Key] = true
	}

	liveKeys := make(map[string]bool, len(live))
	remainingKernel := make(map[string]bool, len(kernelKeys))
	for k := range kernelKeys {
		remainingKernel[k] = true
	}

	var drift, notices []PermissionFinding

	for _, p := range live {
		liveKeys[p.Key] = true
		switch classifyLivePermission(p, kernelKeys, anyDeclaredKey) {
		case PermissionProvenanceKernel:
			delete(remainingKernel, p.Key)

		case PermissionProvenanceRuntime:
			notices = append(notices, PermissionFinding{
				Class: FindingRuntimeInventory, Key: p.Key,
				OperationType: p.OperationType, Scope: p.Scope,
				Detail: fmt.Sprintf("%s is runtime-origin (%q:%q) — the ratified second grant channel, inventory only", p.Key, p.OperationType, p.Scope),
			})

		case PermissionProvenanceUnstamped:
			notices = append(notices, PermissionFinding{
				Class: FindingUnstampedInventory, Key: p.Key,
				OperationType: p.OperationType, Scope: p.Scope,
				Detail: fmt.Sprintf("%s (%q:%q) carries no origin but its key is declared by an installed package — a pre-provenance-stamp install, OR a permission a package upgrade retargeted without preserving origin/declaredBy. This vertex's own body does not name its declaring package (that is exactly what is missing); cross-reference DeclaredKeysByPackage to find which installed package's declaredKeys names this key, and upgrade THAT one", p.Key, p.OperationType, p.Scope),
			})

		case PermissionProvenanceUnrecognized:
			reason := ReasonNoOriginUndeclared
			detail := fmt.Sprintf("%s carries no origin and its key is not declared by any installed package — not a recognized pre-stamp legacy shape", p.Key)
			if p.Origin != "" {
				reason = ReasonUnrecognizedOriginValue
				detail = fmt.Sprintf("%s carries origin %q, which is neither %q nor %q — not a recognized provenance stamp", p.Key, p.Origin, PermissionOriginPackage, PermissionOriginRuntime)
			}
			drift = append(drift, PermissionFinding{
				Class: FindingUndeclared, Key: p.Key, Reason: reason,
				OperationType: p.OperationType, Scope: p.Scope,
				Detail: detail,
			})

		case PermissionProvenancePackage:
			switch {
			case !installedPackages[p.DeclaredBy]:
				drift = append(drift, PermissionFinding{
					Class: FindingUndeclared, Key: p.Key, Package: p.DeclaredBy, Reason: ReasonPackageNotInstalled,
					OperationType: p.OperationType, Scope: p.Scope,
					Detail: fmt.Sprintf("%s declaredBy %q, which is not an installed package", p.Key, p.DeclaredBy),
				})
			case !declaredKeysByPkg[p.DeclaredBy][p.Key]:
				drift = append(drift, PermissionFinding{
					Class: FindingUndeclared, Key: p.Key, Package: p.DeclaredBy, Reason: ReasonKeyNotDeclared,
					OperationType: p.OperationType, Scope: p.Scope,
					Detail: fmt.Sprintf("%s declaredBy %q, whose declaredKeys does not include this key", p.Key, p.DeclaredBy),
				})
			default:
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
	}

	for k := range remainingKernel {
		if undecodableKeys[k] {
			// A document DOES occupy this key — the gatherer already reported
			// it as FindingUndecodable. "Absent from the live set" would be a
			// second, actively wrong diagnosis of the same key, carrying a
			// remedy for a state it is not in.
			continue
		}
		drift = append(drift, PermissionFinding{
			Class: FindingKernelMissing, Key: k,
			Detail: fmt.Sprintf("kernel permission %s is absent from the live set", k),
		})
	}

	for _, d := range declared {
		if liveKeys[d.Key] {
			continue
		}
		if d.Tombstoned {
			notices = append(notices, PermissionFinding{
				Class: FindingRevoked, Key: d.Key, Package: d.Package,
				OperationType: d.OperationType, Scope: d.Scope,
				Detail: fmt.Sprintf("package %q's declared permission %s is tombstoned — a respected, durable revocation; a package upgrade or --force re-apply does not revive it", d.Package, d.Key),
			})
			continue
		}
		if d.Undecodable {
			// The gatherer already reported this exact key as
			// FindingUndecodable — a document DOES exist there, just not one
			// this pass could read. Reporting `missing` as well would be a
			// double, actively WRONG diagnosis: "no live vertex" is false
			// when a vertex is occupying the key.
			continue
		}
		detail := fmt.Sprintf("package %q declares permission key %s, which has no live vertex", d.Package, d.Key)
		if d.OperationType != "" {
			detail = fmt.Sprintf("package %q declares permission (%q, %q) at %s, which has no live vertex", d.Package, d.OperationType, d.Scope, d.Key)
		}
		drift = append(drift, PermissionFinding{
			Class: FindingMissing, Key: d.Key, Package: d.Package,
			OperationType: d.OperationType, Scope: d.Scope,
			Detail: detail,
		})
	}

	sortPermissionFindings(drift)
	sortPermissionFindings(notices)

	declaredKeysByPackage := make(map[string][]string, len(installedPackages))
	for pkg := range installedPackages {
		keys := declaredKeysByPkg[pkg]
		list := make([]string, 0, len(keys))
		for k := range keys {
			list = append(list, k)
		}
		sort.Strings(list)
		declaredKeysByPackage[pkg] = list
	}

	return PermissionReconciliation{Drift: drift, Notices: notices, DeclaredKeysByPackage: declaredKeysByPackage}
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
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		// Reason and Detail are the last tiebreakers, not decoration: without
		// them two findings that agree on every earlier field compare equal,
		// and sort.Slice is not stable — their relative order would then be
		// whichever way the pivot fell, which is exactly the map-order
		// non-determinism this sort exists to remove.
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.Detail < b.Detail
	})
}

// GrantProvenance classifies a live `lnk.permission.<id>.grantedBy.role.<id>`
// edge by how it entered Core KV — PermissionProvenance's five classes applied
// to the object authorization actually travels. The capabilityRoles lens walks
// (identity)-[:holdsRole]->(role)<-[:grantedBy]-(permission), so a permission
// vertex no role points at confers nothing, while an edge onto an existing
// high-value permission confers everything that permission names. Two
// authoring channels stamp `data.origin` (a package's install batch,
// rbac-domain's GrantPermission); the kernel's six seeded edges carry no stamp
// and are recognized by key; and an edge with no origin splits on declaredKeys
// membership exactly as the vertex plane splits, for the same reason — without
// that split, deleting one JSON field is the cheapest way for a forged edge to
// reconcile as a legacy install. Every live edge is classified into exactly
// one of the five.
type GrantProvenance string

const (
	// GrantProvenanceKernel is one of the six edges bootstrap seeds
	// (bootstrap.KernelGrantLinkKeys — each primordial permission granted to
	// the operator role). Declared by no manifest, legitimate, reconciled
	// against that derived key set rather than any package.
	GrantProvenanceKernel GrantProvenance = "kernel"
	// GrantProvenancePackage is `origin == "package"` — an edge a package's
	// install batch wrote alongside the permission it grants.
	GrantProvenancePackage GrantProvenance = "package"
	// GrantProvenanceRuntime is `origin == "runtime"` — the stamp
	// rbac-domain's GrantPermission writes, the ratified second grant channel,
	// reconciled against nothing. This class is asserted by the edge's own
	// body and believed: `origin` is client-supplied at every authoring
	// channel, so ANY writer of an edge — not only an actor holding
	// GrantPermission — selects this class for itself simply by setting the
	// field, and the edge is then inventory whatever it grants. The one thing
	// that still holds is GrantFindingKernelRegrant, which is decided before
	// any origin is read.
	GrantProvenanceRuntime GrantProvenance = "runtime"
	// GrantProvenanceUnstamped is a live edge carrying no `origin` WHOSE KEY
	// IS declared by some installed package's declaredKeys — an install
	// predating the stamp, healable by upgrading the declaring package. The
	// declaredKeys check is what stops a bare omission being the cheapest
	// laundering, and the derivation check runs on this class too (an
	// unstamped edge must still grant a permission its declaring package
	// owns). Neither makes the class safe on its own: an attacker who also
	// writes the key into a package's declaredKeys, for a permission that
	// package does declare, reconciles here as a notice. Only the
	// registry-anchor pass over the compiled Definition closes that.
	GrantProvenanceUnstamped GrantProvenance = "unstamped"
	// GrantProvenanceUnrecognized is everything else: no origin and an
	// undeclared key, or a non-empty origin that is neither "package" nor
	// "runtime". Always drift — and an unrecognized VALUE is never read as a
	// legacy shape regardless of declaredKeys membership, because an absent
	// stamp is ambiguous where a wrong one is not.
	GrantProvenanceUnrecognized GrantProvenance = "unrecognized"
)

// LiveGrantLink is one live `lnk.permission.<id>.grantedBy.role.<id>` edge read
// from Core KV, decoded down to the fields the reconciler classifies on.
// PermissionKey/RoleKey are the link document's own `sourceVertex`/
// `targetVertex` — what the body CLAIMS the edge joins, reported on findings
// so an auditor can see the grant without a second read. Classification
// derives the permission from Key instead (grantLinkKeyParts): the key is what
// the capability walk resolves, so it is the half a forgery cannot restate.
type LiveGrantLink struct {
	Key           string // lnk.permission.<id>.grantedBy.role.<id>
	PermissionKey string // sourceVertex — vtx.permission.<id>
	RoleKey       string // targetVertex — vtx.role.<id>
	Origin        string // data.origin verbatim: "" | "package" | "runtime" | anything else
	DeclaredBy    string // data.declaredBy — set on package-origin edges only
}

// DeclaredGrantLink is one grant-edge key an installed package's manifest
// declaredKeys names — the declared side's identity, exactly as
// DeclaredPermission is on the vertex plane: Package + Key identify it fully,
// and PermissionKey/RoleKey are optional enrichment resolved from a link
// snapshot when one is available (live or tombstoned; a tombstone mutation
// carries the prior document forward whole).
type DeclaredGrantLink struct {
	Package       string
	Key           string // lnk.permission.<id>.grantedBy.role.<id>
	PermissionKey string
	RoleKey       string
	// Tombstoned reports whether Key currently holds a document with
	// `isDeleted: true` — the state RevokePermission leaves behind, which
	// removes the edge from the capability walk without freeing its key.
	// Reported as GrantFindingRevoked (a notice, the durable end state of a
	// deliberate revocation), never GrantFindingMissing, which is reserved for
	// a declared key backed by NO document at all.
	Tombstoned bool
	// Undecodable reports whether Key WAS found but could not be turned into a
	// usable link document — the gatherer already reports it as
	// GrantFindingUndecodable, so ReconcileGrantLinks must not ALSO report it
	// missing: the key is occupied, not absent.
	Undecodable bool
}

// GrantFindingClass is the machine-readable class of one grant-edge finding —
// the edge plane's own vocabulary, kept separate from PermissionFindingClass
// so a caller reporting both planes cannot silently mix a vertex finding into
// an edge slice, or read a class name as covering an object it does not.
type GrantFindingClass string

const (
	// GrantFindingUndeclared is drift: a live grant edge this reconciler
	// cannot attribute to any installed package's declaredKeys. Three shapes
	// reach it, distinguished by Reason: origin == "package" whose declaredBy
	// names no installed package; origin == "package" whose declaredBy IS
	// installed but whose declaredKeys omits this edge's key; and
	// GrantProvenanceUnrecognized (no origin and undeclared, or an origin
	// value that is neither wire value).
	GrantFindingUndeclared GrantFindingClass = "undeclared"
	// GrantFindingKeyMismatch is drift: an edge that IS declared by an
	// installed package, whose key grants a permission that package does not
	// itself declare — a declared edge pointing at somebody else's permission.
	// This is the derivation check the `package` and `unstamped` classes both
	// run; the `runtime` class reaches no check at all (see
	// GrantProvenanceRuntime), so this is not a property of every live edge.
	// Checked only once an edge has cleared the undeclared question, so one
	// forged edge reports one finding.
	GrantFindingKeyMismatch GrantFindingClass = "keyMismatch"
	// GrantFindingKernelRegrant is drift, in every provenance class and
	// whatever the edge's body claims: an edge granting one of bootstrap's six
	// primordial permissions that is not itself one of the six kernel grant
	// edges. A kernel permission has exactly one legitimate grant topology —
	// the seeder's own edge to the operator role — and no package declares a
	// key it cannot address (kernel permission ids are per-deployment NanoIDs)
	// while a runtime grant onto one is an escalation by construction. This is
	// the only check decided before `origin` is read, and so the only one an
	// attacker cannot select their way out of by choosing a class.
	GrantFindingKernelRegrant GrantFindingClass = "kernelRegrant"
	// GrantFindingMalformedKey is drift: a key occupying the grant-edge
	// namespace (`lnk.permission.*.grantedBy.role.*` by segment) whose ids are
	// not valid NanoIDs, so it is not a Contract #1 link key at all
	// (substrate.ParseLinkKey rejects it). No writer the Processor validated
	// can produce one; a direct Core-KV write can. Reported rather than
	// skipped: dropping it from the enumeration would let the malformed shape
	// opt out of every other class.
	GrantFindingMalformedKey GrantFindingClass = "malformedKey"
	// GrantFindingMissing is drift: a declared grant-edge key backed by NO
	// document at all — a partial or interrupted install, or a hard purge
	// outside the Processor's soft-tombstone path. A declared key backed by a
	// TOMBSTONED document is GrantFindingRevoked instead, and one backed by an
	// unreadable document is GrantFindingUndecodable only.
	GrantFindingMissing GrantFindingClass = "missing"
	// GrantFindingKernelMissing is drift: one of the six kernel grant edges
	// (bootstrap.KernelGrantLinkKeys) is absent from the live set — the
	// operator role has lost a primordial grant.
	GrantFindingKernelMissing GrantFindingClass = "kernelMissing"
	// GrantFindingUndecodable is drift: a grant-edge key this pass could not
	// account for — its envelope failed to decode, or it was listed but not
	// returned by the batched read. Raised only by the gatherer
	// (LoadPermissionReconciliation), never by ReconcileGrantLinks. Reported
	// rather than skipped: no authorization path reads `origin` or
	// `declaredBy` off an edge, so an edge with one malformed field still
	// authorizes normally, and skipping it here would let it opt out of every
	// class this reconciler produces.
	GrantFindingUndecodable GrantFindingClass = "undecodable"
	// GrantFindingRuntimeInventory is a notice, never drift: a live
	// runtime-origin edge — GrantPermission's ratified channel.
	GrantFindingRuntimeInventory GrantFindingClass = "runtimeInventory"
	// GrantFindingUnstampedInventory is a notice, never drift: a live edge
	// with no origin whose key IS declared by an installed package — a
	// pre-stamp package install, remedied by upgrading that package.
	GrantFindingUnstampedInventory GrantFindingClass = "unstampedInventory"
	// GrantFindingRevoked is a notice, never drift: a declared grant-edge key
	// backed by a tombstoned document — RevokePermission's respected
	// revocation, the correct end state of an operator withdrawing a grant.
	GrantFindingRevoked GrantFindingClass = "revoked"
)

// GrantUndeclaredReason sub-classifies a GrantFindingUndeclared finding by
// WHICH check failed. The undeclared-producing branches share an identical
// Class/Key/Package and differ only in Detail's prose, which a test must never
// parse to prove which branch fired — a distinction the code makes has to be
// carried in a field, or one branch can be disabled outright with every
// assertion still passing because a sibling produces an indistinguishable
// finding. Empty on every finding whose Class is not GrantFindingUndeclared.
type GrantUndeclaredReason string

const (
	// GrantReasonPackageNotInstalled: a package-origin edge's declaredBy names
	// no currently-installed package.
	GrantReasonPackageNotInstalled GrantUndeclaredReason = "packageNotInstalled"
	// GrantReasonKeyNotDeclared: a package-origin edge's declaredBy IS
	// installed, but that package's declaredKeys does not include this edge's
	// own key.
	GrantReasonKeyNotDeclared GrantUndeclaredReason = "keyNotDeclared"
	// GrantReasonNoOriginUndeclared: no `origin` at all, and this key is
	// declared by no installed package — not a legacy pre-stamp install.
	GrantReasonNoOriginUndeclared GrantUndeclaredReason = "noOriginUndeclared"
	// GrantReasonUnrecognizedOriginValue: a non-empty `origin` that is neither
	// "package" nor "runtime".
	GrantReasonUnrecognizedOriginValue GrantUndeclaredReason = "unrecognizedOriginValue"
)

// GrantFinding is one classified fact the grant-edge reconciler reports. Class
// is the assertable surface; Key names the edge the finding concerns (the live
// edge for every class except GrantFindingMissing/GrantFindingRevoked, which
// have no live edge — there Key is the declared key in question);
// PermissionKey/RoleKey name the grant it expresses when known, and Package
// the declaration it claims. Reason sub-classifies GrantFindingUndeclared and
// is empty on every other class. Detail is prose for a human reading a gate's
// output, never something another program parses — and every
// attacker-controlled field folded into it is rendered with %q so an origin or
// declaredBy containing a newline or ANSI escape cannot forge that output.
type GrantFinding struct {
	Class         GrantFindingClass
	Key           string
	PermissionKey string
	RoleKey       string
	Package       string
	Reason        GrantUndeclaredReason
	Detail        string
}

// grantRelation is the link-key relation segment that expresses a grant.
// `lnk.permission.<id>.forOperation.meta.<id>` shares the `lnk.permission.`
// prefix and is not one.
const grantRelation = "grantedBy"

// GrantLinkKeyParts splits a `lnk.permission.<permID>.grantedBy.role.<roleID>`
// key into the permission and role vertex keys it addresses, reporting false
// for anything else. The key grammar is delegated to substrate.ParseLinkKey —
// Contract #1's own parser, NanoID validation included — so "what does a grant
// edge key address" has exactly one definition, the one the refractor pipeline
// projects through. Exported because the provenance gate's registry-anchor
// pass needs the permission side of a declared edge key and must not carry a
// second copy of this grammar (scripts/verify-permission-provenance.go).
func GrantLinkKeyParts(key string) (permissionKey, roleKey string, ok bool) {
	type1, id1, relation, type2, id2, ok := substrate.ParseLinkKey(key)
	if !ok || type1 != "permission" || relation != grantRelation || type2 != "role" {
		return "", "", false
	}
	return "vtx.permission." + id1, "vtx.role." + id2, true
}

// grantLinkShaped reports whether key OCCUPIES the grant-edge namespace —
// `lnk.permission.<a>.grantedBy.role.<b>` by segment position — without
// requiring the id segments to be valid NanoIDs. Deliberately looser than
// GrantLinkKeyParts, and used only to decide what to enumerate and what to
// collect off a declaredKeys record: the writer this whole plane exists to
// catch puts bytes straight into Core KV and never passed the Processor's key
// validation, so filtering the enumeration on the strict grammar would let a
// malformed grant key opt out of every finding class. Such a key is
// enumerated and reported as GrantFindingMalformedKey instead of vanishing.
func grantLinkShaped(key string) bool {
	parts := strings.Split(key, ".")
	return len(parts) == 6 &&
		parts[0] == "lnk" && parts[1] == "permission" &&
		parts[3] == grantRelation && parts[4] == "role"
}

// classifyGrantLink sorts one live edge into exactly one of the five
// GrantProvenance classes: key membership in kernelKeys takes priority over
// the edge's own claimed origin, because kernel status is a property of WHICH
// key this is, not of what the document at that key says. declaringPackages
// maps an edge key to the installed packages whose declaredKeys name it — the
// piece of data that tells a genuine pre-stamp install apart from a forgery
// that simply omitted the field, since a pre-stamp edge's key was always
// written by some package's own install batch. It carries the package names
// rather than bare membership so the unstamped arm can run its derivation
// check against the same packages this classification consulted, instead of
// against a second set that could drift from it.
func classifyGrantLink(l LiveGrantLink, kernelKeys map[string]bool, declaringPackages map[string][]string) GrantProvenance {
	if kernelKeys[l.Key] {
		return GrantProvenanceKernel
	}
	switch l.Origin {
	case PermissionOriginPackage:
		return GrantProvenancePackage
	case PermissionOriginRuntime:
		return GrantProvenanceRuntime
	case "":
		if len(declaringPackages[l.Key]) > 0 {
			return GrantProvenanceUnstamped
		}
		return GrantProvenanceUnrecognized
	default:
		// A non-empty origin that is neither wire value is never treated as a
		// legacy shape, regardless of declaredKeys membership — an
		// unrecognized value is not ambiguous the way an absent one is.
		return GrantProvenanceUnrecognized
	}
}

// GrantLinkReconcileInput is ReconcileGrantLinks' full input. A struct rather
// than a positional argument list: the edge plane reconciles one live
// population against four independent facts — the declared edges, the
// declaring packages' own permissions, the kernel's edges and the kernel's
// permissions — plus the keys the read could not decode, and a call site that
// wide cannot be read.
type GrantLinkReconcileInput struct {
	Live     []LiveGrantLink
	Declared []DeclaredGrantLink
	// DeclaredPermissions is the vertex plane's declared side, consulted for
	// one question: does the package that declares an edge also declare the
	// permission that edge grants (GrantFindingKeyMismatch).
	DeclaredPermissions []DeclaredPermission
	// InstalledPackages names every currently-installed (non-tombstoned)
	// package by its manifest name.
	InstalledPackages map[string]bool
	// KernelGrantLinkKeys is bootstrap's six seeded edges — the kernel class.
	KernelGrantLinkKeys map[string]bool
	// KernelPermissionKeys is bootstrap's six primordial permission keys. An
	// edge granting one of them that is not itself a kernel edge is drift
	// whatever its body claims (GrantFindingKernelRegrant).
	KernelPermissionKeys map[string]bool
	// UndecodableKeys are edge keys the gatherer found OCCUPIED but could not
	// read, already reported as GrantFindingUndecodable. Named here so a
	// kernel edge in that state is not ALSO reported absent.
	UndecodableKeys map[string]bool
}

// ReconcileGrantLinks classifies every live grant edge and every declared
// grant-edge key against each other — ReconcilePermissions for the object
// authorization travels. Pure; LoadPermissionReconciliation is the Core-KV
// gatherer that feeds it (and the only place GrantFindingUndecodable is
// raised).
//
// Two checks are decided BEFORE the edge's own `origin` is read, because
// `origin` is client-supplied and a class it selects for itself cannot gate
// anything: a key that is not a Contract #1 link key at all is
// GrantFindingMalformedKey, and an edge granting a primordial permission that
// is not one of the kernel's own six edges is GrantFindingKernelRegrant. Every
// other check hangs off the classification.
//
// The declared identity is DeclaredGrantLink.Key, the granularity a package's
// declaredKeys record carries, so `undeclared` and `missing` are plain
// key-membership tests. The derivation check is the edge plane's own:
// DeclaredPermissions supplies, per package, the `vtx.permission.*` keys that
// package declares, and a DECLARED edge whose key grants a permission outside
// that set is keyMismatch — an edge may only point at a permission its
// declaring package owns. It runs on the `package` and `unstamped` classes
// alike (an edge is not made trustworthy by omitting the stamp), and only
// after the edge has cleared the "is this even declared" question, so one
// forged edge produces one finding with one remedy.
//
// Output is sorted (Class, Key, Package, PermissionKey, RoleKey, Reason,
// Detail) — a total order over every field a finding carries, so two calls
// with the same logical input produce byte-identical output regardless of
// live/declared slice order.
func ReconcileGrantLinks(in GrantLinkReconcileInput) (drift, notices []GrantFinding, declaredGrantLinksByPackage map[string][]string) {
	declaredKeysByPkg := make(map[string]map[string]bool, len(in.Declared))
	declaringPackages := make(map[string][]string, len(in.Declared))
	for _, d := range in.Declared {
		set, ok := declaredKeysByPkg[d.Package]
		if !ok {
			set = map[string]bool{}
			declaredKeysByPkg[d.Package] = set
		}
		if !set[d.Key] {
			set[d.Key] = true
			declaringPackages[d.Key] = append(declaringPackages[d.Key], d.Package)
		}
	}
	// Sorted so the package a finding names when several declare the same key
	// is a property of the input, not of its order.
	for k := range declaringPackages {
		sort.Strings(declaringPackages[k])
	}

	declaredPermsByPkg := make(map[string]map[string]bool, len(in.DeclaredPermissions))
	for _, d := range in.DeclaredPermissions {
		set, ok := declaredPermsByPkg[d.Package]
		if !ok {
			set = map[string]bool{}
			declaredPermsByPkg[d.Package] = set
		}
		set[d.Key] = true
	}

	liveKeys := make(map[string]bool, len(in.Live))
	remainingKernel := make(map[string]bool, len(in.KernelGrantLinkKeys))
	for k := range in.KernelGrantLinkKeys {
		remainingKernel[k] = true
	}

	for _, l := range in.Live {
		liveKeys[l.Key] = true

		permissionKey, _, parsed := GrantLinkKeyParts(l.Key)
		if !parsed {
			drift = append(drift, GrantFinding{
				Class: GrantFindingMalformedKey, Key: l.Key,
				PermissionKey: l.PermissionKey, RoleKey: l.RoleKey,
				Detail: fmt.Sprintf("%q occupies the grant-edge namespace but is not a well-formed lnk.permission.<id>.grantedBy.role.<id> key — no writer the Processor validated can produce it", l.Key),
			})
			continue
		}
		if !in.KernelGrantLinkKeys[l.Key] && in.KernelPermissionKeys[permissionKey] {
			drift = append(drift, GrantFinding{
				Class: GrantFindingKernelRegrant, Key: l.Key, Package: l.DeclaredBy,
				PermissionKey: l.PermissionKey, RoleKey: l.RoleKey,
				Detail: fmt.Sprintf("%s grants primordial permission %s to a role the kernel did not, claiming origin %q — a kernel permission has exactly one legitimate grant edge and this is not it", l.Key, permissionKey, l.Origin),
			})
			continue
		}

		switch classifyGrantLink(l, in.KernelGrantLinkKeys, declaringPackages) {
		case GrantProvenanceKernel:
			delete(remainingKernel, l.Key)

		case GrantProvenanceRuntime:
			notices = append(notices, GrantFinding{
				Class: GrantFindingRuntimeInventory, Key: l.Key,
				PermissionKey: l.PermissionKey, RoleKey: l.RoleKey,
				Detail: fmt.Sprintf("%s stamps itself runtime-origin, granting %s to %q — GrantPermission's ratified channel writes that stamp, and so can any other writer of the edge; it is reconciled against nothing", l.Key, permissionKey, l.RoleKey),
			})

		case GrantProvenanceUnstamped:
			declaring := declaringPackages[l.Key]
			owner := ""
			for _, p := range declaring {
				if declaredPermsByPkg[p][permissionKey] {
					owner = p
					break
				}
			}
			if owner == "" {
				drift = append(drift, GrantFinding{
					Class: GrantFindingKeyMismatch, Key: l.Key, Package: declaring[0],
					PermissionKey: l.PermissionKey, RoleKey: l.RoleKey,
					Detail: fmt.Sprintf("%s carries no origin and package %q declares its key, but it grants %s, which no package declaring this key declares as a permission of its own", l.Key, declaring[0], permissionKey),
				})
				break
			}
			notices = append(notices, GrantFinding{
				Class: GrantFindingUnstampedInventory, Key: l.Key, Package: owner,
				PermissionKey: l.PermissionKey, RoleKey: l.RoleKey,
				Detail: fmt.Sprintf("%s carries no origin, and package %q declares both its key and the permission %s it grants. That is an install predating the stamp (upgrade %[2]q to heal it), an upgrade that retargeted the edge without preserving origin/declaredBy, or a key an attacker wrote into that package's declaredKeys to buy this notice — this pass cannot tell the three apart; the gate's registry-anchor pass over the compiled Definition can", l.Key, owner, permissionKey),
			})

		case GrantProvenanceUnrecognized:
			reason := GrantReasonNoOriginUndeclared
			detail := fmt.Sprintf("%s carries no origin and no installed package declares its key. An install-authored edge always lands in its package's declaredKeys, and a GrantPermission-authored edge carries origin %q — so this is a forged edge, or a runtime grant minted before that stamp existed", l.Key, PermissionOriginRuntime)
			if l.Origin != "" {
				reason = GrantReasonUnrecognizedOriginValue
				detail = fmt.Sprintf("%s carries origin %q, which is neither %q nor %q — not a recognized provenance stamp", l.Key, l.Origin, PermissionOriginPackage, PermissionOriginRuntime)
			}
			drift = append(drift, GrantFinding{
				Class: GrantFindingUndeclared, Key: l.Key, Reason: reason,
				PermissionKey: l.PermissionKey, RoleKey: l.RoleKey,
				Detail: detail,
			})

		case GrantProvenancePackage:
			switch {
			case !in.InstalledPackages[l.DeclaredBy]:
				drift = append(drift, GrantFinding{
					Class: GrantFindingUndeclared, Key: l.Key, Package: l.DeclaredBy, Reason: GrantReasonPackageNotInstalled,
					PermissionKey: l.PermissionKey, RoleKey: l.RoleKey,
					Detail: fmt.Sprintf("%s declaredBy %q, which is not an installed package", l.Key, l.DeclaredBy),
				})
			case !declaredKeysByPkg[l.DeclaredBy][l.Key]:
				drift = append(drift, GrantFinding{
					Class: GrantFindingUndeclared, Key: l.Key, Package: l.DeclaredBy, Reason: GrantReasonKeyNotDeclared,
					PermissionKey: l.PermissionKey, RoleKey: l.RoleKey,
					Detail: fmt.Sprintf("%s declaredBy %q, whose declaredKeys does not include this key", l.Key, l.DeclaredBy),
				})
			case !declaredPermsByPkg[l.DeclaredBy][permissionKey]:
				drift = append(drift, GrantFinding{
					Class: GrantFindingKeyMismatch, Key: l.Key, Package: l.DeclaredBy,
					PermissionKey: l.PermissionKey, RoleKey: l.RoleKey,
					Detail: fmt.Sprintf("%s claims declaredBy %q, but it grants %s, which package %q does not declare as a permission of its own", l.Key, l.DeclaredBy, permissionKey, l.DeclaredBy),
				})
			}
		}
	}

	for k := range remainingKernel {
		if in.UndecodableKeys[k] {
			// A document DOES occupy this key — the gatherer already reported
			// it as GrantFindingUndecodable. "Absent from the live set" would
			// be a second, actively wrong diagnosis of the same key.
			continue
		}
		drift = append(drift, GrantFinding{
			Class: GrantFindingKernelMissing, Key: k,
			Detail: fmt.Sprintf("kernel grant edge %s is absent from the live set — a primordial permission no longer reaches the operator role", k),
		})
	}

	for _, d := range in.Declared {
		if liveKeys[d.Key] {
			continue
		}
		if d.Tombstoned {
			notices = append(notices, GrantFinding{
				Class: GrantFindingRevoked, Key: d.Key, Package: d.Package,
				PermissionKey: d.PermissionKey, RoleKey: d.RoleKey,
				Detail: fmt.Sprintf("package %q's declared grant edge %s is tombstoned — a respected revocation; the permission it granted no longer reaches that role", d.Package, d.Key),
			})
			continue
		}
		if d.Undecodable {
			// The gatherer already reported this exact key as
			// GrantFindingUndecodable — a document DOES exist there, just not
			// one this pass could read. Reporting `missing` as well would be a
			// double, actively WRONG diagnosis.
			continue
		}
		drift = append(drift, GrantFinding{
			Class: GrantFindingMissing, Key: d.Key, Package: d.Package,
			PermissionKey: d.PermissionKey, RoleKey: d.RoleKey,
			Detail: fmt.Sprintf("package %q declares grant edge %s, which has no live link", d.Package, d.Key),
		})
	}

	sortGrantFindings(drift)
	sortGrantFindings(notices)

	declaredGrantLinksByPackage = make(map[string][]string, len(in.InstalledPackages))
	for pkg := range in.InstalledPackages {
		keys := declaredKeysByPkg[pkg]
		list := make([]string, 0, len(keys))
		for k := range keys {
			list = append(list, k)
		}
		sort.Strings(list)
		declaredGrantLinksByPackage[pkg] = list
	}

	return drift, notices, declaredGrantLinksByPackage
}

func sortGrantFindings(findings []GrantFinding) {
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
		if a.PermissionKey != b.PermissionKey {
			return a.PermissionKey < b.PermissionKey
		}
		if a.RoleKey != b.RoleKey {
			return a.RoleKey < b.RoleKey
		}
		// Reason and Detail are the last tiebreakers, not decoration: without
		// them two findings that agree on every earlier field compare equal,
		// and sort.Slice is not stable — their relative order would then be
		// whichever way the pivot fell.
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.Detail < b.Detail
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

// grantLinkDoc is the partial decode of a
// `lnk.permission.<id>.grantedBy.role.<id>` envelope. Both live and tombstoned
// bodies decode into this, for the same reason permissionDoc's do: a tombstone
// mutation carries the prior document forward whole and only flips `isDeleted`
// plus the lastModified triplet, so `data` survives a revoke.
type grantLinkDoc struct {
	IsDeleted    bool   `json:"isDeleted"`
	SourceVertex string `json:"sourceVertex"`
	TargetVertex string `json:"targetVertex"`
	Data         struct {
		Origin     string `json:"origin"`
		DeclaredBy string `json:"declaredBy"`
	} `json:"data"`
}

// grantLinkRootKeys keeps only grant edges out of an already fetched key list
// — the `lnk.permission.` prefix also enumerates `forOperation` edges onto op
// metas, which express no grant and have no declared-vs-live story on this
// plane. The filter is grantLinkShaped, not the strict grammar: a key in the
// grant namespace whose ids are malformed is kept and reported
// (GrantFindingMalformedKey) rather than dropped.
func grantLinkRootKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !grantLinkShaped(k) {
			continue
		}
		out = append(out, k)
	}
	return out
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

// kvGetMultiChunked runs KVGetMulti in abstractGuardReadChunk-sized batches
// and merges the results. Two namespaces this file reads grow monotonically
// and both go through here: vtx.permission. (every CreatePermission mints a
// fresh NanoID, TombstonePermission leaves the key rather than freeing it) and
// lnk.permission. (every GrantPermission mints an edge key, RevokePermission
// leaves it), so either read can cross KVGetMulti's 1,024-subject fast path
// (taxonomy.go's abstractGuardReadChunk / readDeclaredKeyOccupants is the
// existing idiom this reuses rather than a new constant). vtx.package. stays
// unchunked, matching findInstalledPackage's existing precedent — the
// package namespace is bounded by the install count, not by churn.
func kvGetMultiChunked(ctx context.Context, conn *substrate.Conn, bucket string, keys []string) (map[string]*substrate.KVEntry, error) {
	out := make(map[string]*substrate.KVEntry, len(keys))
	for start := 0; start < len(keys); start += abstractGuardReadChunk {
		end := start + abstractGuardReadChunk
		if end > len(keys) {
			end = len(keys)
		}
		chunk, err := conn.KVGetMulti(ctx, bucket, keys[start:end])
		if err != nil {
			return nil, fmt.Errorf("read %d keys from %s: %w", end-start, keys[start], err)
		}
		for k, v := range chunk {
			out[k] = v
		}
	}
	return out, nil
}

// permissionInputs is everything gatherPermissionInputs reads out of Core KV,
// for both reconciled planes: the permission vertices and the grant edges that
// point at them. One struct rather than a return list — the two planes carry
// four populations, two kernel key sets and two undecodable lists between
// them, and a positional signature that wide cannot be read at a call site.
type permissionInputs struct {
	live     []LivePermission
	declared []DeclaredPermission
	// liveGrantLinks excludes tombstoned edges: a revoked grant is gone from
	// the capability walk, so it is not live. It reaches the reconciler
	// through declaredGrantLinks' Tombstoned instead, which is what makes a
	// revocation a notice rather than a missing-edge drift.
	liveGrantLinks     []LiveGrantLink
	declaredGrantLinks []DeclaredGrantLink
	installedPackages  map[string]bool
	// kernelPermissionKeys and kernelGrantLinkKeys are each plane's constant
	// set, both derived from the same bootstrap globals.
	kernelPermissionKeys map[string]bool
	kernelGrantLinkKeys  map[string]bool
	// undecodable and undecodableGrantLinks are the findings for keys this pass
	// could not account for at all. They never reach either pure reconciler's
	// typed inputs — a key that does not decode becomes no live or declared
	// record — so LoadPermissionReconciliation folds them into the drift it
	// returns. undecodableKeys / undecodableGrantLinkKeys are the same keys as
	// a set: the reconcilers need them to tell "occupied but unreadable" from
	// "absent" when a KERNEL key is in that state.
	undecodable              []PermissionFinding
	undecodableGrantLinks    []GrantFinding
	undecodableKeys          map[string]bool
	undecodableGrantLinkKeys map[string]bool
}

// decodePermissionVertices turns one batched read of `vtx.permission.<id>`
// roots into the live population, the snapshot map the declared side enriches
// from, the keys this pass could not account for, and the findings that name
// them. Split out of gatherPermissionInputs so both failure arms are
// exercisable on plain in-memory input — in particular the listed-but-not-
// returned arm, a torn view between the list and the batched read that no test
// can stage against a live bucket without racing it.
//
// A root this pass cannot account for is NAMED rather than skipped: no
// authorization path reads any field this reconciler classifies on, so a
// vertex with one malformed field still authorizes normally, and a plain
// `continue` would let it opt out of every finding class. A tombstoned root
// decodes into docs (the declared side reads its preserved data) but is not
// live.
func decodePermissionVertices(roots []string, entries map[string]*substrate.KVEntry) (
	live []LivePermission, docs map[string]permissionDoc, undecodableKeys map[string]bool, findings []PermissionFinding,
) {
	docs = make(map[string]permissionDoc, len(roots))
	undecodableKeys = make(map[string]bool)
	for _, k := range roots {
		entry, present := entries[k]
		if !present {
			undecodableKeys[k] = true
			findings = append(findings, PermissionFinding{
				Class: FindingUndecodable, Key: k,
				Detail: fmt.Sprintf("%s was listed under vtx.permission. but the batched read did not return it", k),
			})
			continue
		}
		var doc permissionDoc
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			undecodableKeys[k] = true
			findings = append(findings, PermissionFinding{
				Class: FindingUndecodable, Key: k,
				Detail: fmt.Sprintf("%s does not decode as a permission envelope: %v", k, err),
			})
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
	return live, docs, undecodableKeys, findings
}

// decodeGrantLinks is decodePermissionVertices for the edge plane, with one
// difference that is the whole point of the class: a TOMBSTONED edge is not
// live. RevokePermission soft-deletes rather than removing the key, and a
// tombstoned edge is out of the capability walk, so reporting it live would
// present a completed revocation as a standing grant. It still lands in docs,
// which is how the declared side reports it as a revocation rather than a
// missing edge.
func decodeGrantLinks(roots []string, entries map[string]*substrate.KVEntry) (
	live []LiveGrantLink, docs map[string]grantLinkDoc, undecodableKeys map[string]bool, findings []GrantFinding,
) {
	docs = make(map[string]grantLinkDoc, len(roots))
	undecodableKeys = make(map[string]bool)
	for _, k := range roots {
		entry, present := entries[k]
		if !present {
			undecodableKeys[k] = true
			findings = append(findings, GrantFinding{
				Class: GrantFindingUndecodable, Key: k,
				Detail: fmt.Sprintf("%s was listed under lnk.permission. but the batched read did not return it", k),
			})
			continue
		}
		var doc grantLinkDoc
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			undecodableKeys[k] = true
			findings = append(findings, GrantFinding{
				Class: GrantFindingUndecodable, Key: k,
				Detail: fmt.Sprintf("%s does not decode as a link envelope: %v", k, err),
			})
			continue
		}
		docs[k] = doc
		if doc.IsDeleted {
			continue
		}
		live = append(live, LiveGrantLink{
			Key:           k,
			PermissionKey: doc.SourceVertex,
			RoleKey:       doc.TargetVertex,
			Origin:        doc.Data.Origin,
			DeclaredBy:    doc.Data.DeclaredBy,
		})
	}
	return live, docs, undecodableKeys, findings
}

// gatherPermissionInputs performs every Core-KV read LoadPermissionReconciliation
// needs, so a second caller (the test suite's livePermissionClassCounts, which
// needs the same live/declared population to independently re-derive
// per-class counts) has one owner for the read logic rather than a second
// hand-rolled copy.
//
// Live side: every `vtx.permission.<id>` vertex root. A root this pass cannot
// account for at all — listed but absent from the batched read, or present
// but failing to decode — is named in undecodable rather than silently
// skipped: `declaredBy` (and every other field this reconciler classifies on)
// is read by no authorization path, so a vertex with one malformed field
// still authorizes normally regardless of whether this pass can read its
// envelope — a plain `continue` here would let it opt out of every finding
// class, so undecodable reports it instead.
//
// Grant-edge live side: every `lnk.permission.<id>.grantedBy.role.<id>` key
// under the `lnk.permission.` prefix (the same prefix also enumerates
// `forOperation` edges, which grantLinkRootKeys drops). A TOMBSTONED edge is
// not live: RevokePermission soft-deletes rather than removing the key, and a
// tombstoned edge is out of the capability walk, so treating it as live would
// report a completed revocation as a standing grant. Undecodable edges are
// named, for the same reason undecodable vertices are.
//
// Declared side: every installed package's manifest `declaredKeys` entries,
// decoded via parseManifestNameAndKeys (authored_dispatch_scope.go) — the one
// existing reader of this exact envelope, tombstone semantics included, rather
// than a second hand-rolled decoder. `vtx.permission.`-prefixed entries become
// DeclaredPermissions and grant-edge keys become DeclaredGrantLinks; a package
// records both in one list, since addCreate appends every key its install
// batch wrote. Each declared key's enrichment (operationType/scope for a
// permission, sourceVertex/targetVertex for an edge, tombstoned for either) is
// OPTIONAL, resolved from the snapshot the live side already read when
// available (data survives tombstoning, per permissionDoc's doc comment) —
// never required: the declared identity is Package + Key, so a declaredKeys
// entry backed by no document at all (a partial install, an interrupted
// upgrade, a hard purge outside the Processor's soft-tombstone path) still
// reconciles correctly as `missing`, just without a recovered tuple to
// describe it by.
//
// Kernel keys for both planes are derived from the named bootstrap package
// variables — never a hard-coded string — resolved (and validated as actually
// resolved; see kernelPermissionKeys) rather than read as bare zero values.
func gatherPermissionInputs(ctx context.Context, conn *substrate.Conn) (permissionInputs, error) {
	var in permissionInputs

	permKeys, err := conn.KVListKeysPrefix(ctx, CoreBucket, "vtx.permission.")
	if err != nil {
		return permissionInputs{}, fmt.Errorf("pkgmgr: list permission keys: %w", err)
	}
	permRoots := permissionVertexRootKeys(permKeys)
	permEntries, err := kvGetMultiChunked(ctx, conn, CoreBucket, permRoots)
	if err != nil {
		return permissionInputs{}, fmt.Errorf("pkgmgr: read %d permission vertices: %w", len(permRoots), err)
	}

	live, docs, undecodableKeys, permFindings := decodePermissionVertices(permRoots, permEntries)
	in.live, in.undecodable, in.undecodableKeys = live, permFindings, undecodableKeys

	linkKeys, err := conn.KVListKeysPrefix(ctx, CoreBucket, "lnk.permission.")
	if err != nil {
		return permissionInputs{}, fmt.Errorf("pkgmgr: list grant-link keys: %w", err)
	}
	linkRoots := grantLinkRootKeys(linkKeys)
	linkEntries, err := kvGetMultiChunked(ctx, conn, CoreBucket, linkRoots)
	if err != nil {
		return permissionInputs{}, fmt.Errorf("pkgmgr: read %d grant links: %w", len(linkRoots), err)
	}

	liveLinks, linkDocs, undecodableLinkKeys, linkFindings := decodeGrantLinks(linkRoots, linkEntries)
	in.liveGrantLinks, in.undecodableGrantLinks, in.undecodableGrantLinkKeys = liveLinks, linkFindings, undecodableLinkKeys

	pkgKeys, err := conn.KVListKeysPrefix(ctx, CoreBucket, PackageVertexPrefix)
	if err != nil {
		return permissionInputs{}, fmt.Errorf("pkgmgr: list package keys: %w", err)
	}
	// Mirrors installer.go's findInstalledPackage/List manifest-key filter,
	// length guard included (a key shorter than the prefix+suffix can never
	// legitimately match, and slicing past its length would panic).
	var manifestKeys []string
	for _, k := range pkgKeys {
		if len(k) < len(PackageVertexPrefix)+len(".manifest") {
			continue
		}
		if k[len(k)-len(".manifest"):] != ".manifest" {
			continue
		}
		manifestKeys = append(manifestKeys, k)
	}
	pkgEntries, err := conn.KVGetMulti(ctx, CoreBucket, manifestKeys)
	if err != nil {
		return permissionInputs{}, fmt.Errorf("pkgmgr: read %d package manifests: %w", len(manifestKeys), err)
	}

	in.installedPackages = map[string]bool{}
	for _, k := range manifestKeys {
		entry, present := pkgEntries[k]
		if !present {
			continue
		}
		name, declaredKeys, perr := parseManifestNameAndKeys(entry.Value)
		if perr != nil {
			return permissionInputs{}, fmt.Errorf("pkgmgr: parse package manifest %s: %w", k, perr)
		}
		if name == "" {
			// Tombstoned manifest reads as an uninstalled package
			// (parseManifestNameAndKeys's own convention) — declares nothing.
			continue
		}
		in.installedPackages[name] = true
		for _, dk := range declaredKeys {
			if strings.HasPrefix(dk, "vtx.permission.") {
				d := DeclaredPermission{Package: name, Key: dk}
				if doc, ok := docs[dk]; ok {
					d.OperationType = doc.Data.OperationType
					d.Scope = doc.Data.Scope
					d.Tombstoned = doc.IsDeleted
				} else if undecodableKeys[dk] {
					d.Undecodable = true
				}
				in.declared = append(in.declared, d)
				continue
			}
			if grantLinkShaped(dk) {
				d := DeclaredGrantLink{Package: name, Key: dk}
				if doc, ok := linkDocs[dk]; ok {
					d.PermissionKey = doc.SourceVertex
					d.RoleKey = doc.TargetVertex
					d.Tombstoned = doc.IsDeleted
				} else if undecodableLinkKeys[dk] {
					d.Undecodable = true
				}
				in.declaredGrantLinks = append(in.declaredGrantLinks, d)
			}
		}
	}

	in.kernelPermissionKeys, err = kernelPermissionKeys()
	if err != nil {
		return permissionInputs{}, err
	}
	in.kernelGrantLinkKeys, err = kernelGrantLinkKeys()
	if err != nil {
		return permissionInputs{}, err
	}
	return in, nil
}

// LoadPermissionReconciliation reads Core KV and runs both pure reconcilers
// over what it found — ReconcilePermissions on the permission vertices,
// ReconcileGrantLinks on the `grantedBy` edges.
//
// It reads and reconciles TWICE and returns only the findings both passes
// produced, matched on (class, key). The reason is a correctness one, not
// belt-and-braces: gatherPermissionInputs reads at six unfenced moments (three
// list calls and three batched reads) and Core KV has no snapshot read. A
// package lifecycle op commits atomically — an uninstall tombstones the
// manifest AND the package's declared keys in one mutation — so a pass that
// straddles that commit sees a half-world: edges still live and stamped
// `declaredBy: P` while P's manifest already reads uninstalled, which is
// indistinguishable, field for field, from a forged edge naming a package that
// was never installed. An install straddling the same window is the mirror
// image (declared keys with no live document yet: `missing`). Both are FALSE
// drift, and this function's caller is a CI gate whose whole output is a
// pass/fail. One commit cannot straddle two disjoint read windows at the same
// key, so intersecting the two passes removes the entire class.
//
// What it does NOT remove: a key genuinely flapping across both windows (a
// reinstall loop) is still reported, and a real forgery that is written and
// removed between the passes is missed — both are the correct trade against
// failing a gate on a transient. The returned DeclaredKeysByPackage /
// DeclaredGrantLinksByPackage come from the SECOND pass, unintersected: they
// are an inventory the caller's registry-anchor pass compares against repo
// source, not a finding, and that pass carries the same quiescence assumption.
//
// The undecodable entries each read produced are folded into that read's own
// drift before intersection — they never reach either reconciler's typed
// inputs, since a key that cannot be decoded becomes no usable live or
// declared record (see gatherPermissionInputs's doc comment).
func LoadPermissionReconciliation(ctx context.Context, conn *substrate.Conn) (PermissionReconciliation, error) {
	return reconcileTwice(func() (PermissionReconciliation, error) {
		return reconcileOnce(ctx, conn)
	})
}

// reconcileTwice performs read twice and returns only the findings both reads
// produced, matched on (class, key) — LoadPermissionReconciliation's whole
// answer, with the Core-KV read behind a parameter so the combining rule is
// testable without staging a race against a live bucket. The maps come from
// the second read, unintersected; see LoadPermissionReconciliation's doc
// comment for what that does and does not buy.
func reconcileTwice(read func() (PermissionReconciliation, error)) (PermissionReconciliation, error) {
	first, err := read()
	if err != nil {
		return PermissionReconciliation{}, err
	}
	second, err := read()
	if err != nil {
		return PermissionReconciliation{}, err
	}

	out := second
	out.Drift = intersectPermissionFindings(second.Drift, first.Drift)
	out.Notices = intersectPermissionFindings(second.Notices, first.Notices)
	out.GrantDrift = intersectGrantFindings(second.GrantDrift, first.GrantDrift)
	out.GrantNotices = intersectGrantFindings(second.GrantNotices, first.GrantNotices)
	return out, nil
}

// reconcileOnce is one read-and-reconcile pass over both planes — the whole of
// LoadPermissionReconciliation's answer except that it is a single,
// unsynchronized observation. See that function's doc comment for why one is
// not enough.
func reconcileOnce(ctx context.Context, conn *substrate.Conn) (PermissionReconciliation, error) {
	in, err := gatherPermissionInputs(ctx, conn)
	if err != nil {
		return PermissionReconciliation{}, err
	}
	rec := ReconcilePermissions(in.live, in.declared, in.installedPackages, in.kernelPermissionKeys, in.undecodableKeys)
	if len(in.undecodable) > 0 {
		rec.Drift = append(rec.Drift, in.undecodable...)
		sortPermissionFindings(rec.Drift)
	}

	grantDrift, grantNotices, declaredGrantLinksByPackage := ReconcileGrantLinks(GrantLinkReconcileInput{
		Live:                 in.liveGrantLinks,
		Declared:             in.declaredGrantLinks,
		DeclaredPermissions:  in.declared,
		InstalledPackages:    in.installedPackages,
		KernelGrantLinkKeys:  in.kernelGrantLinkKeys,
		KernelPermissionKeys: in.kernelPermissionKeys,
		UndecodableKeys:      in.undecodableGrantLinkKeys,
	})
	if len(in.undecodableGrantLinks) > 0 {
		grantDrift = append(grantDrift, in.undecodableGrantLinks...)
		sortGrantFindings(grantDrift)
	}
	rec.GrantDrift = grantDrift
	rec.GrantNotices = grantNotices
	rec.DeclaredGrantLinksByPackage = declaredGrantLinksByPackage
	return rec, nil
}

// findingIdentity is the pair two reads must agree on for a finding to
// survive. Class and Key only: Detail carries observation-time prose (a decode
// error's text, a package name recovered from a manifest that may have moved),
// and requiring THAT to match byte-for-byte would drop a finding both passes
// genuinely made about the same key.
type findingIdentity struct {
	class string
	key   string
}

// intersectPermissionFindings keeps the findings of latest whose (class, key)
// also appears in earlier. latest supplies the returned records, so the
// reported detail describes the more recent observation.
func intersectPermissionFindings(latest, earlier []PermissionFinding) []PermissionFinding {
	seen := make(map[findingIdentity]bool, len(earlier))
	for _, f := range earlier {
		seen[findingIdentity{string(f.Class), f.Key}] = true
	}
	out := make([]PermissionFinding, 0, len(latest))
	for _, f := range latest {
		if seen[findingIdentity{string(f.Class), f.Key}] {
			out = append(out, f)
		}
	}
	return out
}

// intersectGrantFindings is intersectPermissionFindings for the edge plane.
func intersectGrantFindings(latest, earlier []GrantFinding) []GrantFinding {
	seen := make(map[findingIdentity]bool, len(earlier))
	for _, f := range earlier {
		seen[findingIdentity{string(f.Class), f.Key}] = true
	}
	out := make([]GrantFinding, 0, len(latest))
	for _, f := range latest {
		if seen[findingIdentity{string(f.Class), f.Key}] {
			out = append(out, f)
		}
	}
	return out
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
		// "" is the unloaded zero value of a key variable; "vtx.permission."
		// is the shape a caller gets by concatenating a prefix onto an
		// unloaded ID variable. Both mean the same thing: nothing was ever
		// loaded into bootstrap's globals. (substrate.VertexKey cannot
		// produce the second — it validates its id segment and panics on an
		// empty one, so a key that reaches here empty-id was built by
		// concatenation.)
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

// kernelGrantLinkKeySet validates already-resolved grant-edge keys and returns
// them as GrantProvenanceKernel's constant set. Split out from
// kernelGrantLinkKeys for the same reason kernelPermissionKeySet is split from
// kernelPermissionKeys: the keys derive from internal/bootstrap package
// VARIABLES that bootstrap.Load / LoadOrGenerate populates from
// lattice.bootstrap.json, so a process that never loaded them derives keys
// with empty id segments — and a test that mutated those globals to exercise
// the unloaded state would race every other test in this package that depends
// on them being loaded. The validation therefore lives here, over
// caller-supplied keys.
//
// A key is accepted only as a complete
// `lnk.permission.<id>.grantedBy.role.<id>`: "" and the empty-id shape a
// caller gets by concatenating around unloaded ID variables both fail it, and
// so does anything that is not a grant edge at all. (substrate.LinkKey cannot
// produce that shape — it validates its id segments and panics on an empty
// one, which is why KernelGrantLinkKeys concatenates instead.)
func kernelGrantLinkKeySet(keys []string) (map[string]bool, error) {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		if _, _, ok := GrantLinkKeyParts(k); !ok {
			return nil, fmt.Errorf("pkgmgr: kernel grant-link key %q is not a resolved lnk.permission.<id>.grantedBy.role.<id> key — bootstrap.Load(BOOTSTRAP_JSON_PATH) must run before LoadPermissionReconciliation", k)
		}
		set[k] = true
	}
	return set, nil
}

// kernelGrantLinkKeys is GrantProvenanceKernel's constant set — the six
// `permission grantedBy role` edges bootstrap seeds, taken from
// bootstrap.KernelGrantLinkKeys so the kernel's grant topology is derived in
// exactly one place and a change to bootstrap's own derivation cannot silently
// desync this reconciler from the kernel it reconciles against. Returns an
// error if bootstrap's globals have not been populated (see
// kernelGrantLinkKeySet).
func kernelGrantLinkKeys() (map[string]bool, error) {
	return kernelGrantLinkKeySet(bootstrap.KernelGrantLinkKeys())
}
