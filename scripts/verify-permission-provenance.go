//go:build ignore

// verify-permission-provenance.go — assertion tool for
// `make verify-permission-provenance`.
//
// Reconciles two live populations in Core KV against what installed package
// manifests declare (internal/pkgmgr.LoadPermissionReconciliation): the
// `vtx.permission.*` permission vertices, and the
// `lnk.permission.<id>.grantedBy.role.<id>` grant edges that actually confer
// them (the capabilityRoles lens walks the edge, not the vertex — a
// permission no role points at confers nothing). It then separately anchors
// every REGISTRY-known installed package's declared keys on BOTH planes
// against its compiled Go Definition — the in-tree source, not anything Core
// KV holds (internal/pkgregistry; import-cycle reasons this lives here and
// not in internal/pkgmgr are on checkRegistryAnchor). Fails on drift from any
// of the four passes (vertex reconcile, edge reconcile, vertex registry
// anchor, edge registry anchor). Named for the gap in
// grant-provenance-runtime-permission-minting-design.md §1.1: the manifest
// verifier compares manifest-YAML to Go Definition and never reads Core KV,
// and each per-package `verify-package-*` script asserts only declared→live
// for its own package.
//
// A PASS here narrows the gap; it does not close it. Two residuals remain on
// each plane, named as non-goals in grant-edge-provenance-design.md's fire
// brief (item 7) and grant-provenance-runtime-permission-minting-design.md's
// own fire brief before it:
//
//   - The declared side of both reconcile passes is Core KV again — a
//     package's own declaredKeys record, not repo source. A package-plane
//     actor writes both halves of that comparison in one operation, so an
//     attacker-authored manifest declaring an attacker-authored permission
//     (or an attacker-authored grant edge pointing at a legitimate one)
//     reconciles perfectly clean against the reconcile pass alone. The
//     registry-anchor passes below close this for every package the compiled
//     registry knows, on both planes.
//   - On the vertex plane, a forgery that omits `origin` is caught only by
//     declaredKeys membership (internal/pkgmgr's
//     PermissionProvenanceUnrecognized); an attacker who also controls
//     declaredKeys defeats that test too, and only the registry-anchor pass
//     closes it, for a registry-known package. The edge plane carries the
//     identical gap (GrantProvenanceUnrecognized) plus one the vertex plane
//     does not have: the ROLE side of a declared grant edge is NOT pinnable
//     from repo source at all. A PermissionSpec names its grant target by
//     canonical role name (`packages/rbac-domain/permissions.go:31`,
//     `GrantsTo: []string{"operator"}`), which cmd/lattice-pkg resolves to a
//     role id only at install time (installer.go's resolveGrants) — the
//     compiled Definition never holds the id a live edge points at. So the
//     edge registry-anchor pass below verifies WHICH permission a declared
//     edge may grant and HOW MANY edges may exist for it, and accepts
//     whichever role the declared key names; it does not and cannot check
//     that the role is the one GrantsTo actually intended. Resolving role ids
//     from Core KV to close that would put the writer on both sides of the
//     comparison again — the same gap this pass exists to close, not a fix
//     for it.
//   - The registry-anchor passes only cover a package the compiled registry
//     KNOWS (internal/pkgregistry.Lookup). An out-of-tree install —
//     legitimate, e.g. a capability-authored package — has no Definition to
//     anchor against and is reported, never failed, on either plane.
//
// Every live permission vertex falls into exactly one of five provenance
// classes (kernel, package, runtime, unstamped, unrecognized — see
// internal/pkgmgr's PermissionProvenance), and every live grant edge falls
// into the same five classes applied to the edge instead of the vertex
// (GrantProvenance). Both planes fail on the same five drift classes, named
// identically because each means the same thing on the other object:
//
//	undeclared     a live vertex/edge this reconciler cannot attribute to any
//	               installed package's declaredKeys (declaredBy names no
//	               installed package, declaredBy's declaredKeys omits this
//	               key, or the origin stamp itself is missing/unrecognized)
//	keyMismatch    a vertex body claiming a declaration its own key does not
//	               derive (vertex plane), or a declared edge granting a
//	               permission its declaring package does not itself declare
//	               (edge plane — an edge may only point at a permission its
//	               own package owns)
//	missing        a declaredKeys entry backed by NO document at all
//	kernelMissing  one of the six primordial permission keys (vertex plane) or
//	               grant edges (edge plane) is absent
//	undecodable    a vtx.permission.* or lnk.permission.*.grantedBy.role.* root
//	               this pass could not read at all
//
// Runtime and unstamped entries are REPORTED, never failed, on both planes:
// the first is a ratified channel (this gate can verify only the STAMP,
// never the channel itself — see internal/pkgmgr's PermissionProvenanceRuntime
// / GrantProvenanceRuntime), and the second heals on the declaring package's
// next upgrade. A declared key backed by a TOMBSTONED document is also
// reported, never failed, on both planes: revocation (TombstonePermission /
// RevokePermission) is durable (see the FindingMissing remedy below for what
// that means for the OTHER, still-failing shape of missing).
//
// Exit 0: no drift on either plane (notices may still be printed).
// Exit 1: drift on either plane or any pass, or the reconciliation could not
// be performed.
//
// Run via: go run ./scripts/verify-permission-provenance.go
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
)

// remedies maps each drift class to what an operator should actually do
// about it. Each one names the outcome it promises and the states it is
// false in — a remedy printed for every caller that is only true for some of
// them sends the reader down a path that returns success and changes
// nothing. The FindingMissing and FindingKernelMissing entries are pinned
// against behavior verified by executing them on a real stack, not merely
// inferred from a read of the surrounding code.
var remedies = map[pkgmgr.PermissionFindingClass]string{
	pkgmgr.FindingUndeclared: "" +
		"    No installed package's declaredKeys accounts for this vertex. The line above says\n" +
		"    which way: it names a package that is not installed, a package whose declaredKeys\n" +
		"    omits it, or it carries no provenance this pass recognises at all.\n" +
		"    Where a package IS named, reinstalling it does not heal this: install writes its\n" +
		"    own declared keys and never retracts a key it did not write. Establish how it was\n" +
		"    authored (the op log for its createdByOp) before tombstoning it — a live grant\n" +
		"    projected into cap.roles.* is authorizing somebody right now.",
	pkgmgr.FindingKeyMismatch: "" +
		"    The body and the key disagree about which declaration this is. Authorization\n" +
		"    reads the BODY (step 3 matches data.operationType), while uninstall and upgrade\n" +
		"    address the KEY — so this vertex is reachable by one and invisible to the other.\n" +
		"    Treat it as undeclared: establish authorship before touching it.",
	pkgmgr.FindingMissing: "" +
		"    A package declares this permission key and no document exists there at ALL —\n" +
		"    strictly a partial or interrupted install, or a hard purge; a deliberate revoke\n" +
		"    would show up as a NOTE (tombstoned documents are excluded from this class, and\n" +
		"    revocation is durable — a package upgrade does not revive it). Re-running a plain\n" +
		"    install does NOT write it: apply.go skips a same-version install outright once the\n" +
		"    package is already recorded as installed. Only `--force` (or the explicit `upgrade`\n" +
		"    command) reaches the re-create path, and only for a key that is genuinely absent\n" +
		"    from KV (upgrade.go's committed == nil arm) — which this class is, by definition.\n" +
		"    A plain re-install is a confirmed no-op here; --force is the actual remedy.",
	pkgmgr.FindingKernelMissing: "" +
		"    A primordial permission is gone from the live set. `make verify-kernel`'s bootstrap\n" +
		"    reconcile does NOT restore it: reconcile.go's retain path leaves any key that is\n" +
		"    tombstoned or not vtx.meta.* untouched, and a primordial permission key is both — it\n" +
		"    only re-creates a key that is HARD-missing (jetstream.ErrKeyNotFound), a state the\n" +
		"    Processor's own soft-tombstone convention never produces. There is no sanctioned\n" +
		"    automated remedy for this today: investigate how the tombstone happened (the op log\n" +
		"    for its createdByOp / lastModifiedByOp) before deciding a manual fix. Confirmed by\n" +
		"    running the reconcile against a tombstoned kernel key: it reports retained=1 and\n" +
		"    leaves kernelMissing=1 unchanged.",
	pkgmgr.FindingUndecodable: "" +
		"    A document exists at this key — it is occupied, NOT absent — but this pass could\n" +
		"    not turn it into a usable envelope (or the batched read did not return a key that\n" +
		"    was listed a moment earlier — a torn view worth re-running once before assuming the\n" +
		"    former). Every classification field this reconciler reads (declaredBy, origin,\n" +
		"    operationType, scope) is read by NO authorization path\n" +
		"    (packages/rbac-domain/lenses.go projects only operationType/scope/lanes/origin), so\n" +
		"    this vertex is authorizing normally right now regardless of why this pass cannot\n" +
		"    read it. Read it directly (KVGet the key) before deciding anything.",
}

// grantRemedies is remedies' analogue for the `grantedBy` edge plane —
// GrantFindingClass, not PermissionFindingClass, so it cannot share the map
// above. The advice mirrors each vertex-plane remedy on the object
// authorization actually travels: a live edge onto an existing permission, not
// the permission vertex itself (grant-edge-provenance-design.md §1).
var grantRemedies = map[pkgmgr.GrantFindingClass]string{
	pkgmgr.GrantFindingUndeclared: "" +
		"    No installed package's declaredKeys accounts for this edge. The line above says\n" +
		"    which way: it names a package that is not installed, a package whose declaredKeys\n" +
		"    omits it, or it carries no provenance this pass recognises at all — the last being\n" +
		"    the shape a forged edge takes, since minting one requires no permission vertex.\n" +
		"    Where a package IS named, reinstalling it does not heal this: install writes its\n" +
		"    own declared keys and never retracts a key it did not write. Establish how it was\n" +
		"    authored (the op log for its createdByOp) before revoking it — a live edge onto\n" +
		"    an existing permission is authorizing somebody right now, same as a permission\n" +
		"    vertex would.",
	pkgmgr.GrantFindingKeyMismatch: "" +
		"    The edge is declared, but the permission it grants is not one its own declaring\n" +
		"    package declares for itself — a declared edge pointing at somebody else's\n" +
		"    permission. Authorization reads the capabilityRoles walk over this edge exactly\n" +
		"    as it stands. Treat it as undeclared: establish authorship before touching it.",
	pkgmgr.GrantFindingMissing: "" +
		"    A package declares this grant-edge key and no document exists there at ALL —\n" +
		"    strictly a partial or interrupted install, or a hard purge; a deliberate revoke\n" +
		"    would show up as a NOTE (tombstoned edges are excluded from this class, and\n" +
		"    revocation is durable — a package upgrade does not revive it). Re-running a plain\n" +
		"    install does NOT write it, for the same reason it does not on the vertex plane\n" +
		"    (apply.go skips a same-version install once the package is already recorded);\n" +
		"    --force (or the explicit `upgrade` command) is the actual remedy.",
	pkgmgr.GrantFindingKernelMissing: "" +
		"    One of the six primordial grant edges (bootstrap.KernelGrantLinkKeys) is gone —\n" +
		"    the operator role no longer receives a kernel permission. `make verify-kernel`'s\n" +
		"    bootstrap reconcile does NOT restore it: reconcile.go's plan only ever examines\n" +
		"    vtx.meta.* candidates (isKernelDefinition), and a grant edge is a lnk.* key —\n" +
		"    outside that namespace entirely, so it is never even a candidate. There is no\n" +
		"    sanctioned automated remedy today: investigate how the tombstone happened (the\n" +
		"    op log for its createdByOp / lastModifiedByOp) before deciding a manual fix.",
	pkgmgr.GrantFindingUndecodable: "" +
		"    A document exists at this key — it is occupied, NOT absent — but this pass could\n" +
		"    not turn it into a usable envelope (or the batched read did not return a key that\n" +
		"    was listed a moment earlier — worth one re-run before assuming the former). No\n" +
		"    authorization path reads `origin` or `declaredBy` off a grant edge, so it is\n" +
		"    authorizing normally right now regardless of why this pass cannot read it. Read\n" +
		"    it directly (KVGet the key) before deciding anything.",
}

// registryFinding is the registry-anchor pass's own local finding shape. It
// is deliberately NOT a pkgmgr.PermissionFinding: this pass compares Core KV
// against internal/pkgregistry's compiled Definitions, and internal/pkgmgr
// cannot import pkgregistry — every packages/* imports pkgmgr, so pkgmgr
// importing pkgregistry back would be a cycle. That is also why this whole
// pass lives in this script (package main, free to import both) rather than
// in LoadPermissionReconciliation.
type registryFinding struct {
	pkg    string
	key    string
	detail string
}

// checkRegistryAnchor closes the residual PermissionReconciliation's own doc
// comment names: rec's declared side is Core KV — a package's own
// declaredKeys record — not repo source, and a package-plane actor writes
// BOTH halves of that comparison in one operation, so an attacker-authored
// manifest declaring an attacker-authored permission reconciles perfectly
// clean against rec alone. For every installed package the compiled registry
// actually KNOWS, this derives the expected permission key set from that
// package's real Go Definition.Permissions (via pkgmgr.PermissionID — the
// same derivation installer.go uses, so a legitimate install always matches)
// and diffs it against rec.DeclaredKeysByPackage, independent of whatever
// Core KV itself claims.
//
// A package the registry does not know is reported, never failed: an
// out-of-tree install (a capability-authored package, Loupe's runtime
// install path) is legitimate and simply has nothing in the binary to anchor
// against.
func checkRegistryAnchor(rec pkgmgr.PermissionReconciliation) (drift, notices []registryFinding) {
	names := make([]string, 0, len(rec.DeclaredKeysByPackage))
	for name := range rec.DeclaredKeysByPackage {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			notices = append(notices, registryFinding{
				pkg: name,
				detail: fmt.Sprintf("package %q is installed but not known to the compiled registry — "+
					"a legitimate out-of-tree install, not reconciled against repo source", name),
			})
			continue
		}

		expected := make(map[string]bool, len(def.Permissions))
		for _, p := range def.Permissions {
			expected["vtx.permission."+pkgmgr.PermissionID(name, p.OperationType, p.Scope)] = true
		}
		actual := make(map[string]bool, len(rec.DeclaredKeysByPackage[name]))
		for _, k := range rec.DeclaredKeysByPackage[name] {
			actual[k] = true
		}

		var missingFromLive, extraInLive []string
		for k := range expected {
			if !actual[k] {
				missingFromLive = append(missingFromLive, k)
			}
		}
		for k := range actual {
			if !expected[k] {
				extraInLive = append(extraInLive, k)
			}
		}
		sort.Strings(missingFromLive)
		sort.Strings(extraInLive)

		for _, k := range missingFromLive {
			drift = append(drift, registryFinding{
				pkg: name, key: k,
				detail: fmt.Sprintf("package %q's compiled Definition declares permission key %s, "+
					"which its live declaredKeys does not carry — the installed manifest does not match in-tree source", name, k),
			})
		}
		for _, k := range extraInLive {
			drift = append(drift, registryFinding{
				pkg: name, key: k,
				detail: fmt.Sprintf("package %q's live declaredKeys carries permission key %s, "+
					"which its compiled Definition does not declare — this key has no source in the repo; "+
					"an attacker-authored manifest declaring an attacker-authored permission reconciles clean "+
					"against Core KV alone, which is exactly the shape this pass exists to catch", name, k),
			})
		}
	}
	return drift, notices
}

// grantLinkPermissionKey extracts the permission-vertex key
// (`vtx.permission.<permID>`) a `lnk.permission.<permID>.grantedBy.role.<roleID>`
// key addresses, reporting false for anything not exactly that 6-segment
// shape. Mirrors internal/pkgmgr's unexported grantLinkKeyParts byte-for-byte
// — this script cannot import it (it is a private helper, not part of
// pkgmgr's exported surface) — so a change to that shape must be carried to
// both copies.
func grantLinkPermissionKey(key string) (string, bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 6 {
		return "", false
	}
	if parts[0] != "lnk" || parts[1] != "permission" || parts[3] != "grantedBy" || parts[4] != "role" {
		return "", false
	}
	if parts[2] == "" || parts[5] == "" {
		return "", false
	}
	return "vtx.permission." + parts[2], true
}

// checkGrantLinkRegistryAnchor is checkRegistryAnchor's analogue for the
// `grantedBy` edge plane: for every installed package the compiled registry
// actually knows, it derives — from that package's real Go
// Definition.Permissions — how many grant edges should exist for each
// permission the package declares (len(PermissionSpec.GrantsTo) per spec,
// summed onto that permission's key), and diffs that COUNT against how many
// grant-edge keys rec.DeclaredGrantLinksByPackage actually carries naming the
// same permission. Independent of whatever Core KV itself claims, same as the
// vertex pass.
//
// This is a count check keyed on the permission side only, deliberately not a
// full key match: a PermissionSpec names its grant target by canonical role
// name (`GrantsTo: []string{"operator"}`), which cmd/lattice-pkg resolves to a
// role id only at install time (installer.go's resolveGrants) — the compiled
// Definition never holds the role id a live edge points at. So this pass
// verifies WHICH permission a declared edge may grant and HOW MANY edges may
// exist for it, and accepts whichever role the declared key names. Do not
// extend this to a role check: there is no source-of-truth role id to anchor
// against without reading Core KV for it, which would put the writer on both
// sides of the comparison again — exactly the gap this pass exists to close,
// not a shortcut around it.
//
// A package the registry does not know is skipped here, not double-reported:
// checkRegistryAnchor above already emits one registryUnknown notice per such
// package, and rec.DeclaredKeysByPackage (which that loop ranges over) names
// every installed package, grant links included, even one that declares zero
// of either.
func checkGrantLinkRegistryAnchor(rec pkgmgr.PermissionReconciliation) (drift []registryFinding) {
	names := make([]string, 0, len(rec.DeclaredGrantLinksByPackage))
	for name := range rec.DeclaredGrantLinksByPackage {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			continue
		}

		expected := make(map[string]int, len(def.Permissions))
		for _, p := range def.Permissions {
			expected["vtx.permission."+pkgmgr.PermissionID(name, p.OperationType, p.Scope)] += len(p.GrantsTo)
		}

		actual := make(map[string]int, len(rec.DeclaredGrantLinksByPackage[name]))
		for _, k := range rec.DeclaredGrantLinksByPackage[name] {
			permKey, ok := grantLinkPermissionKey(k)
			if !ok {
				// rec.DeclaredGrantLinksByPackage is populated only from keys
				// internal/pkgmgr's own grantLinkKeyParts already accepted
				// (gatherPermissionInputs) — unreachable in practice. Skip
				// rather than trust a shape this pass did not itself validate.
				continue
			}
			actual[permKey]++
		}

		permKeys := make(map[string]bool, len(expected)+len(actual))
		for k := range expected {
			permKeys[k] = true
		}
		for k := range actual {
			permKeys[k] = true
		}
		sortedPermKeys := make([]string, 0, len(permKeys))
		for k := range permKeys {
			sortedPermKeys = append(sortedPermKeys, k)
		}
		sort.Strings(sortedPermKeys)

		for _, permKey := range sortedPermKeys {
			exp, act := expected[permKey], actual[permKey]
			if exp == act {
				continue
			}
			switch {
			case act == 0:
				drift = append(drift, registryFinding{
					pkg: name, key: permKey,
					detail: fmt.Sprintf("package %q's compiled Definition grants permission %s to %d role(s), "+
						"but its live declaredKeys carries no grant edge for it at all", name, permKey, exp),
				})
			case exp == 0:
				drift = append(drift, registryFinding{
					pkg: name, key: permKey,
					detail: fmt.Sprintf("package %q's live declaredKeys carries %d grant edge(s) naming permission %s, "+
						"which its compiled Definition does not grant to any role — this edge has no source in the repo; "+
						"an escalation needs no new permission vertex, only an edge onto an existing one, which is exactly "+
						"the shape this pass exists to catch", name, act, permKey),
				})
			default:
				drift = append(drift, registryFinding{
					pkg: name, key: permKey,
					detail: fmt.Sprintf("package %q's compiled Definition grants permission %s to %d role(s), "+
						"but its live declaredKeys carries %d grant edge(s) naming it — a count mismatch; this pass checks "+
						"WHICH permission and HOW MANY edges only, never WHICH role (see the file's header comment)", name, exp, act),
				})
			}
		}
	}
	return drift
}

func main() {
	natsURL := pkgverify.EnvOrDefault("NATS_URL", "nats://localhost:4222")
	bootstrapJSONPath := pkgverify.EnvOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The six kernel permission keys are package variables Load populates from
	// lattice.bootstrap.json, not compile-time constants — without this the
	// reconciler cannot resolve the kernel class at all.
	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot load primordial IDs from %s: %v\n", bootstrapJSONPath, err)
		fmt.Fprintln(os.Stderr, "Suggestion: ensure `make up` has completed; lattice.bootstrap.json must exist.")
		os.Exit(1)
	}

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL:          natsURL,
		Name:         "verify-permission-provenance",
		NKeySeedFile: os.Getenv("NATS_NKEY"),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot connect to NATS at %s: %v\n", natsURL, err)
		os.Exit(1)
	}
	defer conn.Close()

	rec, err := pkgmgr.LoadPermissionReconciliation(ctx, conn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: reconcile permission vertices: %v\n", err)
		os.Exit(1)
	}
	regDrift, regNotices := checkRegistryAnchor(rec)
	regGrantDrift := checkGrantLinkRegistryAnchor(rec)

	fmt.Printf("verify-permission-provenance: reconciling live vtx.permission.* vertices and lnk.permission.*.grantedBy.role.* edges against installed manifests and the compiled registry (%s)\n\n", natsURL)

	for _, n := range rec.Notices {
		fmt.Printf("NOTE [%s] %s\n", n.Class, n.Detail)
	}
	for _, n := range rec.GrantNotices {
		fmt.Printf("NOTE [%s] %s\n", n.Class, n.Detail)
	}
	for _, n := range regNotices {
		fmt.Printf("NOTE [registryUnknown] package=%s %s\n", n.pkg, n.detail)
	}
	totalNotices := len(rec.Notices) + len(rec.GrantNotices) + len(regNotices)
	if totalNotices > 0 {
		fmt.Println()
	}

	totalDrift := len(rec.Drift) + len(rec.GrantDrift) + len(regDrift) + len(regGrantDrift)
	if !rec.HasDrift() && len(regDrift) == 0 && len(regGrantDrift) == 0 {
		fmt.Printf("verify-permission-provenance: PASS — no drift on either plane, vertex or grant edge (%d notice(s))\n", totalNotices)
		os.Exit(0)
	}

	fmt.Printf("verify-permission-provenance: %d DRIFT FINDING(S)\n\n", totalDrift)
	for _, d := range rec.Drift {
		fmt.Printf("DRIFT [%s] %s\n", d.Class, d.Detail)
		if r, ok := remedies[d.Class]; ok {
			fmt.Println(r)
		}
	}
	for _, d := range rec.GrantDrift {
		fmt.Printf("DRIFT [%s] %s\n", d.Class, d.Detail)
		if r, ok := grantRemedies[d.Class]; ok {
			fmt.Println(r)
		}
	}
	for _, d := range regDrift {
		fmt.Printf("DRIFT [registryMismatch] package=%s %s\n", d.pkg, d.detail)
		fmt.Println("" +
			"    Do not trust this key's own declaredBy/origin claims to establish authorship —\n" +
			"    those live in the same Core KV this pass already found disagreeing with the\n" +
			"    compiled registry. Compare the installed manifest.yaml against the package's\n" +
			"    Go Definition directly (VerifyAgainstDefinition) before acting.")
	}
	for _, d := range regGrantDrift {
		fmt.Printf("DRIFT [registryGrantMismatch] package=%s %s\n", d.pkg, d.detail)
		fmt.Println("" +
			"    Do not trust this edge's own declaredBy/origin claims to establish authorship —\n" +
			"    those live in the same Core KV this pass already found disagreeing with the\n" +
			"    compiled registry. This pass pins WHICH permission and HOW MANY edges only — it\n" +
			"    does not and cannot pin WHICH role (see the file's header comment). Establish\n" +
			"    authorship (the op log for the edge's createdByOp) before acting.")
	}
	os.Exit(1)
}
