//go:build ignore

// print-role-id.go prints the deterministic vtx.role.<NanoID> key a
// package-declared role resolves to (pkgmgr.RoleID) — the same computation
// cmd/gateway/main.go's ConfigureProvisioning call and cmd/lattice-pkg's
// roleIDsFromBootstrap fallback both rely on to address a role without a KV
// round-trip. Used by Makefile provisioning targets that need to grant one of
// identity-domain's declared roles (consumer, frontOfHouse, backOfHouse,
// identityProvisioner) to a specific actor.
//
// Run via: go run ./scripts/print-role-id.go <packageName> <canonicalName>
package main

import (
	"fmt"
	"os"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: print-role-id.go <packageName> <canonicalName>")
		os.Exit(2)
	}
	fmt.Println("vtx.role." + pkgmgr.RoleID(os.Args[1], os.Args[2]))
}
