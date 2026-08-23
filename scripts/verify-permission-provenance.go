//go:build ignore

// verify-permission-provenance.go — assertion tool for
// `make verify-permission-provenance`.
//
// Reconciles the live `vtx.permission.*` population in Core KV against what
// installed package manifests declare (internal/pkgmgr.LoadPermissionReconciliation),
// and separately anchors every REGISTRY-known installed package's declared
// permission keys against its compiled Go Definition — the in-tree source,
// not anything Core KV holds (internal/pkgregistry; import-cycle reasons
// this lives here and not in internal/pkgmgr are on checkRegistryAnchor).
// Fails on drift from either pass. Named for the gap in
// grant-provenance-runtime-permission-minting-design.md §1.1: the manifest
// verifier compares manifest-YAML to Go Definition and never reads Core KV,
// and each per-package `verify-package-*` script asserts only declared→live
// for its own package.
//
// A PASS here narrows the gap; it does not close it. Three residuals, all
// already named as non-goals in the fire brief's §11.7:
//
//   - `grantedBy` links are not reconciled. Authorization travels
//     `lnk.permission.<id>.grantedBy.role.<id>`, not the permission vertex,
//     and GrantPermission accepts any live permission key and any live role
//     key with no manifest check. build.go writes those link keys INTO
//     declaredKeys alongside the permission's own key, and
//     LoadPermissionReconciliation reads and discards them (only
//     `vtx.permission.`-prefixed entries survive its filter) — the data is
//     there; the code just does not look at it, and there is no ratified
//     pattern yet for what "declared" would even mean for a link.
//   - The registry-anchor pass below only covers a package the compiled
//     registry KNOWS (internal/pkgregistry.Lookup). An out-of-tree install —
//     legitimate, e.g. a capability-authored package — has no Definition to
//     anchor against and is reported, never failed.
//   - A no-origin forgery is caught only by declaredKeys membership
//     (internal/pkgmgr's PermissionProvenanceUnrecognized). An attacker who
//     also controls declaredKeys defeats that test too — the registry-anchor
//     pass is what actually closes that gap, and only for a registry-known
//     package.
//
// Every live permission vertex falls into exactly one of five provenance
// classes: kernel, package, runtime, unstamped, unrecognized (see
// internal/pkgmgr's PermissionProvenance). Fails on five drift classes:
//
//	undeclared     a live vertex this reconciler cannot attribute to any
//	               installed package's declaredKeys (declaredBy names no
//	               installed package, declaredBy's declaredKeys omits this
//	               key, or the origin stamp itself is missing/unrecognized)
//	keyMismatch    a body claiming a declaration its own key does not derive
//	missing        a declaredKeys entry backed by NO document at all
//	kernelMissing  one of the six primordial permission keys is absent
//	undecodable    a vtx.permission.* root this pass could not read at all
//
// Runtime and unstamped entries are REPORTED, never failed: the first is a
// ratified channel (this gate can verify only the STAMP, never the channel
// itself — see internal/pkgmgr's PermissionProvenanceRuntime), and the
// second heals on the declaring package's next upgrade. A declared key
// backed by a TOMBSTONED document is also reported, never failed:
// TombstonePermission's revocation is durable (see the FindingMissing remedy
// below for what that means for the OTHER, still-failing shape of missing).
//
// Exit 0: no drift (notices may still be printed).
// Exit 1: drift, or the reconciliation could not be performed.
//
// Run via: go run ./scripts/verify-permission-provenance.go
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
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
		"    This vertex wears a package's provenance but no installed manifest declares it.\n" +
		"    It is NOT healed by reinstalling the named package: install writes its own\n" +
		"    declared keys and never retracts a key it did not write. Establish how it was\n" +
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

	fmt.Printf("verify-permission-provenance: reconciling live vtx.permission.* against installed manifests and the compiled registry (%s)\n\n", natsURL)

	for _, n := range rec.Notices {
		fmt.Printf("NOTE [%s] %s\n", n.Class, n.Detail)
	}
	for _, n := range regNotices {
		fmt.Printf("NOTE [registryUnknown] package=%s %s\n", n.pkg, n.detail)
	}
	totalNotices := len(rec.Notices) + len(regNotices)
	if totalNotices > 0 {
		fmt.Println()
	}

	totalDrift := len(rec.Drift) + len(regDrift)
	if totalDrift == 0 {
		fmt.Printf("verify-permission-provenance: PASS — no drift (%d notice(s))\n", totalNotices)
		os.Exit(0)
	}

	fmt.Printf("verify-permission-provenance: %d DRIFT FINDING(S)\n\n", totalDrift)
	for _, d := range rec.Drift {
		fmt.Printf("DRIFT [%s] %s\n", d.Class, d.Detail)
		if r, ok := remedies[d.Class]; ok {
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
	os.Exit(1)
}
