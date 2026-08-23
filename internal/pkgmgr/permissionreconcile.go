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
//   - `grantedBy` links are not reconciled. Authorization travels
//     `lnk.permission.<id>.grantedBy.role.<id>`, not the permission vertex,
//     and GrantPermission accepts any live permission key and any live role
//     key with no manifest check — the cheapest real escalation forges no
//     permission vertex at all. Link provenance carries no stamp to
//     reconcile against; no ratified pattern exists to close this yet.
//   - A forgery that omits `origin` is caught only by the declaredKeys
//     membership test (PermissionProvenanceUnrecognized). An attacker who
//     also controls declaredKeys — the same package-plane write the first
//     residual above already assumes honest — defeats this test too.
type PermissionReconciliation struct {
	Drift   []PermissionFinding
	Notices []PermissionFinding
	// DeclaredKeysByPackage names, for every currently-installed package
	// (present even when the package declares zero permission keys), the
	// sorted `vtx.permission.*` keys its declaredKeys record carries — the
	// exact identity ReconcilePermissions computed drift over. Exposed so a
	// caller with an independent anchor for what a package SHOULD declare
	// (scripts/verify-permission-provenance.go's registry pass) can compare
	// against it without re-deriving declaredKeys itself.
	DeclaredKeysByPackage map[string][]string
}

// HasDrift reports whether a gate consuming this reconciliation must fail.
func (r PermissionReconciliation) HasDrift() bool { return len(r.Drift) > 0 }

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
// permission keys. Output is sorted (Class, Key, Package, OperationType,
// Scope) so two calls with the same logical input produce byte-identical
// output regardless of live/declared slice order — a gate that reports in
// map order is untestable.
func ReconcilePermissions(live []LivePermission, declared []DeclaredPermission, installedPackages map[string]bool, kernelKeys map[string]bool) PermissionReconciliation {
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

// kvGetMultiChunked runs KVGetMulti in abstractGuardReadChunk-sized batches
// and merges the results. vtx.permission. is the one namespace this file
// reads that grows monotonically — every CreatePermission mints a fresh
// NanoID and TombstonePermission leaves the key rather than freeing it — so
// it is the one read here that can cross KVGetMulti's 1,024-subject fast path
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
// Declared side: every installed package's manifest `declaredKeys` entries
// with a `vtx.permission.` prefix, decoded via parseManifestNameAndKeys
// (authored_dispatch_scope.go) — the one existing reader of this exact
// envelope, tombstone semantics included, rather than a second hand-rolled
// decoder. A declared key's (operationType, scope, tombstoned) is an OPTIONAL
// enrichment, resolved from the same permission-vertex snapshot the live side
// already read when available (data survives tombstoning, per permissionDoc's
// doc comment) — never required: DeclaredPermission's identity is
// Package + Key, so a declaredKeys entry backed by no document at all (a
// partial install, an interrupted upgrade, a hard purge outside the
// Processor's soft-tombstone path) still reconciles correctly as `missing`,
// just without a recovered tuple to describe it by.
//
// Kernel keys are the six named bootstrap package variables — never a
// hard-coded string — resolved (and validated as actually resolved; see
// kernelPermissionKeys) rather than read as bare zero values.
func gatherPermissionInputs(ctx context.Context, conn *substrate.Conn) (
	live []LivePermission,
	declared []DeclaredPermission,
	installedPackages map[string]bool,
	kernelKeys map[string]bool,
	undecodable []PermissionFinding,
	err error,
) {
	permKeys, err := conn.KVListKeysPrefix(ctx, CoreBucket, "vtx.permission.")
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("pkgmgr: list permission keys: %w", err)
	}
	permRoots := permissionVertexRootKeys(permKeys)
	permEntries, err := kvGetMultiChunked(ctx, conn, CoreBucket, permRoots)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("pkgmgr: read %d permission vertices: %w", len(permRoots), err)
	}

	docs := make(map[string]permissionDoc, len(permRoots))
	undecodableKeys := make(map[string]bool)
	for _, k := range permRoots {
		entry, present := permEntries[k]
		if !present {
			undecodableKeys[k] = true
			undecodable = append(undecodable, PermissionFinding{
				Class: FindingUndecodable, Key: k,
				Detail: fmt.Sprintf("%s was listed under vtx.permission. but the batched read did not return it", k),
			})
			continue
		}
		var doc permissionDoc
		if err := json.Unmarshal(entry.Value, &doc); err != nil {
			undecodableKeys[k] = true
			undecodable = append(undecodable, PermissionFinding{
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

	pkgKeys, err := conn.KVListKeysPrefix(ctx, CoreBucket, PackageVertexPrefix)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("pkgmgr: list package keys: %w", err)
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
		return nil, nil, nil, nil, nil, fmt.Errorf("pkgmgr: read %d package manifests: %w", len(manifestKeys), err)
	}

	installedPackages = map[string]bool{}
	for _, k := range manifestKeys {
		entry, present := pkgEntries[k]
		if !present {
			continue
		}
		name, declaredKeys, perr := parseManifestNameAndKeys(entry.Value)
		if perr != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("pkgmgr: parse package manifest %s: %w", k, perr)
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
				d.Tombstoned = doc.IsDeleted
			} else if undecodableKeys[dk] {
				d.Undecodable = true
			}
			declared = append(declared, d)
		}
	}

	kernelKeys, err = kernelPermissionKeys()
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return live, declared, installedPackages, kernelKeys, undecodable, nil
}

// LoadPermissionReconciliation reads Core KV (via gatherPermissionInputs) and
// calls ReconcilePermissions, then folds in any FindingUndecodable entries
// the read itself produced — those never reach ReconcilePermissions' typed
// inputs, since a key that cannot be decoded never becomes a usable
// LivePermission or DeclaredPermission (see gatherPermissionInputs's doc
// comment).
func LoadPermissionReconciliation(ctx context.Context, conn *substrate.Conn) (PermissionReconciliation, error) {
	live, declared, installedPackages, kernelKeys, undecodable, err := gatherPermissionInputs(ctx, conn)
	if err != nil {
		return PermissionReconciliation{}, err
	}
	rec := ReconcilePermissions(live, declared, installedPackages, kernelKeys)
	if len(undecodable) > 0 {
		rec.Drift = append(rec.Drift, undecodable...)
		sortPermissionFindings(rec.Drift)
	}
	return rec, nil
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
