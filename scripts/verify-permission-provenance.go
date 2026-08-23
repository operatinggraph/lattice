//go:build ignore

// verify-permission-provenance.go — assertion tool for
// `make verify-permission-provenance`.
//
// Reconciles the live `vtx.permission.*` population in Core KV against what the
// installed package manifests declare, and fails on drift. Closes the gap named
// in grant-provenance-runtime-permission-minting-design.md §1.1: the manifest
// verifier compares manifest-YAML to Go Definition and never reads Core KV, and
// each per-package `verify-package-*` script asserts only declared→live for its
// own package — so nothing anywhere sees a live permission vertex that no
// manifest declares.
//
// Every live permission vertex falls into exactly one provenance class:
//
//	kernel     one of bootstrap's six primordial permission keys
//	package    data.origin == "package" — declared by an installed manifest
//	runtime    data.origin == "runtime" — rbac-domain's CreatePermission, the
//	           ratified second grant channel (Branch A); inventory, never drift
//	unstamped  no origin and not kernel — a pre-provenance-stamp install
//
// Fails on four drift classes:
//
//	undeclared     a package-origin vertex no installed manifest declares
//	keyMismatch    a body claiming a declaration its own key does not derive
//	missing        a manifest-declared permission key with no live vertex
//	kernelMissing  one of the six primordial permission keys is absent
//
// Runtime and unstamped entries are REPORTED, never failed: the first is a
// ratified channel and the second heals on the declaring package's next
// upgrade.
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
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
)

// remedies maps each drift class to what an operator should actually do about
// it. Each one names the outcome it promises and the states it is false in —
// a remedy printed for every caller that is only true for some of them sends
// the reader down a path that returns success and changes nothing.
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
		"    A package declares this permission and nothing is live at its key. If it was\n" +
		"    revoked deliberately, note that the revoke is not durable (design §5.1): the\n" +
		"    package's next upgrade or --force re-apply revives it. If the install was\n" +
		"    interrupted, re-running the install writes the missing key.",
	pkgmgr.FindingKernelMissing: "" +
		"    A primordial permission is gone. The kernel seed is the only writer of these\n" +
		"    keys, so re-running the package install of anything will not restore it —\n" +
		"    this needs a bootstrap reconcile against the running binary.",
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

	fmt.Printf("verify-permission-provenance: reconciling live vtx.permission.* against installed manifests (%s)\n\n", natsURL)

	for _, n := range rec.Notices {
		fmt.Printf("NOTE [%s] %s\n", n.Class, n.Detail)
	}
	if len(rec.Notices) > 0 {
		fmt.Println()
	}

	if !rec.HasDrift() {
		fmt.Printf("verify-permission-provenance: PASS — no drift (%d notice(s))\n", len(rec.Notices))
		os.Exit(0)
	}

	fmt.Printf("verify-permission-provenance: %d DRIFT FINDING(S)\n\n", len(rec.Drift))
	for _, d := range rec.Drift {
		fmt.Printf("DRIFT [%s] %s\n", d.Class, d.Detail)
		if r, ok := remedies[d.Class]; ok {
			fmt.Println(r)
		}
	}
	os.Exit(1)
}
