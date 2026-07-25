//go:build ignore

// verify-package-loftspace-domain.go — assertion tool for
// `make verify-package-loftspace-domain`.
//
// Connects to a running Lattice NATS instance and checks that the
// loftspace-domain package has been correctly installed. Asserts:
//
//	1 loftspaceListing DDL meta-vertex (class=meta.ddl.vertexType) admitting
//	  SetListing + SetUnitAddress + SetListingStatus, with its self-description aspects.
//	1 listing  aspect-type DDL (class=meta.ddl.aspectType) admitting SetListing + SetListingStatus.
//	1 address  aspect-type DDL (class=meta.ddl.aspectType) admitting SetUnitAddress.
//	1 loftspaceOwnership DDL meta-vertex (class=meta.ddl.vertexType) admitting
//	  AssignUnitOwner + RemoveUnitOwner, with its self-description aspects.
//	6 permission vertices — one per (operationType, scope) pair: scope=any granted
//	  to operator for all five ops, plus SetListingStatus scope=self granted to
//	  consumer (the landlord path). See loftspaceOpGrants.
//	1 package vertex + manifest aspect (name=loftspace-domain).
//
// Run via: go run ./scripts/verify-package-loftspace-domain.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
)

const (
	loftspacePackageName  = "loftspace-domain"
	loftspaceListingDDL   = "loftspaceListing"
	loftspaceOwnershipDDL = "loftspaceOwnership"
	loftspaceCoreKVBucket = "core-kv"
)

// loftspaceListingOps are the loftspaceListing vertexType DDL's commands;
// loftspaceOwnershipOps the loftspaceOwnership vertexType DDL's. loftspaceExpectedOps
// is every op that gets a permission vertex (the union).
var (
	loftspaceListingOps   = []string{"SetListing", "SetUnitAddress", "SetListingStatus"}
	loftspaceOwnershipOps = []string{"AssignUnitOwner", "RemoveUnitOwner"}
	loftspaceExpectedOps  = append(append([]string{}, loftspaceListingOps...), loftspaceOwnershipOps...)
)

// permGrant is one expected (scope, grantee-role) pair for an operationType's
// permission vertex. A permission's identity is its (operationType, scope) pair
// (Contract #8 §8.1), so a widened grantee set lands on the SAME vertex through
// a second grantedBy link, never a second vertex — the vertex count below is
// therefore the number of DISTINCT scopes, not of grants.
//
// SetListingStatus is the one op with two: scope=any to operator, and scope=self
// to consumer — the landlord path, where the script requires the acting
// identity's `manages` link to the payload unit. The ownership ops deliberately
// have no self grant: they are what CONFERS management, so a self-scoped grant
// would let any signed-in identity make itself the landlord of any unit. These
// pins assert exactly the declared pairs; no role is added implicitly.
type permGrant struct {
	scope   string
	grantee string
}

var loftspaceOpGrants = map[string][]permGrant{
	"SetListing":       {{"any", "operator"}},
	"SetUnitAddress":   {{"any", "operator"}},
	"SetListingStatus": {{"any", "operator"}, {"self", "consumer"}},
	"AssignUnitOwner":  {{"any", "operator"}},
	"RemoveUnitOwner":  {{"any", "operator"}},
}

// ddlCheck describes one DDL to verify: its canonical name, its expected meta
// class, and the ops its permittedCommands must contain.
type ddlCheck struct {
	canonical string
	class     string
	ops       []string
}

func main() {
	natsURL := pkgverify.EnvOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := pkgverify.EnvOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot load primordial IDs from %s: %v\n", bootstrapJSONPath, err)
		fmt.Fprintln(os.Stderr, "Suggestion: ensure `make up` has completed; lattice.bootstrap.json must exist.")
		os.Exit(1)
	}

	var natsOpts []nats.Option
	if seed := os.Getenv("NATS_NKEY"); seed != "" {
		nkeyOpt, err := nats.NkeyOptionFromSeed(seed)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: load NKey seed %q: %v\n", seed, err)
			os.Exit(1)
		}
		natsOpts = append(natsOpts, nkeyOpt)
	} else if creds := os.Getenv("NATS_CREDS"); creds != "" {
		natsOpts = append(natsOpts, nats.UserCredentials(creds))
	}
	nc, err := nats.Connect(natsURL, natsOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot connect to NATS at %s: %v\n", natsURL, err)
		os.Exit(1)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: jetstream context: %v\n", err)
		os.Exit(1)
	}

	coreKV, err := js.KeyValue(ctx, loftspaceCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", loftspaceCoreKVBucket, err)
		os.Exit(1)
	}

	allKeys, err := pkgverify.ListAllKeys(ctx, coreKV)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot list Core KV keys: %v\n", err)
		os.Exit(1)
	}

	var failures []string
	okCount := 0
	ok := func(desc string) {
		fmt.Printf("  OK  %s\n", desc)
		okCount++
	}
	fail := func(desc, reason string) {
		msg := fmt.Sprintf("FAIL: %s: %s", desc, reason)
		fmt.Println(" ", msg)
		failures = append(failures, msg)
	}

	fmt.Printf("verify-package-loftspace-domain: scanning %d Core KV keys...\n", len(allKeys))

	ddlChecks := []ddlCheck{
		{canonical: loftspaceListingDDL, class: "meta.ddl.vertexType", ops: loftspaceListingOps},
		{canonical: "listing", class: "meta.ddl.aspectType", ops: []string{"SetListing", "SetListingStatus"}},
		{canonical: "address", class: "meta.ddl.aspectType", ops: []string{"SetUnitAddress"}},
		{canonical: loftspaceOwnershipDDL, class: "meta.ddl.vertexType", ops: loftspaceOwnershipOps},
	}

	for _, dc := range ddlChecks {
		ddlKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, dc.canonical)
		if err != nil || ddlKey == "" {
			fail(dc.canonical+" DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", dc.canonical, err))
			continue
		}
		ok(fmt.Sprintf("%s DDL meta-vertex exists: %s", dc.canonical, ddlKey))

		// class + alive.
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, ddlKey); err != nil {
			fail(ddlKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			if cls, _ := env["class"].(string); cls != dc.class {
				fail(ddlKey+" class", fmt.Sprintf("got %q want %q", cls, dc.class))
			} else {
				ok(ddlKey + " class=" + dc.class)
			}
			if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
				fail(ddlKey+" isDeleted", "vertex is tombstoned")
			} else {
				ok(ddlKey + " isDeleted=false")
			}
		}

		// permittedCommands.
		pcKey := ddlKey + ".permittedCommands"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := pkgverify.ToStringSlice(data["commands"])
			cmdSet := pkgverify.ToSet(cmds)
			allPresent := true
			for _, op := range dc.ops {
				if !cmdSet[op] {
					fail(pcKey, fmt.Sprintf("missing command %q", op))
					allPresent = false
				}
			}
			if len(cmds) != len(dc.ops) {
				fail(pcKey, fmt.Sprintf("command count=%d want %d", len(cmds), len(dc.ops)))
				allPresent = false
			}
			if allPresent && len(cmds) == len(dc.ops) {
				ok(fmt.Sprintf("%s contains exactly %v", pcKey, dc.ops))
			}
			if err := pkgverify.CheckAspectEnvelope(env, pcKey, ddlKey, "permittedCommands"); err != nil {
				fail(pcKey+" envelope", err.Error())
			} else {
				ok(pcKey + " envelope shape OK")
			}
		}

		// remaining self-description aspects.
		for _, asp := range []string{"canonicalName", "description", "script", "inputSchema", "outputSchema", "fieldDescription", "examples"} {
			k := ddlKey + "." + asp
			if env, err := pkgverify.GetEnvelope(ctx, coreKV, k); err != nil {
				fail(k, fmt.Sprintf("missing: %v", err))
			} else {
				ok(k + " present")
				if err := pkgverify.CheckAspectEnvelope(env, k, ddlKey, asp); err != nil {
					fail(k+" envelope", err.Error())
				} else {
					ok(k + " envelope shape OK")
				}
			}
		}
	}

	// Discover role NanoIDs (operator from bootstrap; others — consumer, the
	// landlord grantee — by scanning vtx.role.*.canonicalName).
	operatorRoleID := bootstrap.RoleOperatorID
	roleIDByCanonical := map[string]string{}
	if operatorRoleID != "" {
		roleIDByCanonical["operator"] = operatorRoleID
	}
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.role.") || !strings.HasSuffix(key, ".canonicalName") {
			continue
		}
		env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		data, _ := env["data"].(map[string]any)
		val, _ := data["value"].(string)
		if val == "" {
			continue
		}
		parts := strings.Split(key, ".")
		if len(parts) != 4 {
			continue
		}
		roleIDByCanonical[val] = parts[2]
	}

	// permission vertices + scope + grantedBy-role links. Collect ALL matching
	// vertices per op: keying a single id by operationType picks whichever
	// vertex Go's unstable map iteration visited last, so an op carrying two
	// scopes (SetListingStatus) hid one of them at random per run.
	permIDsByOp := map[string][]string{}
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.permission.") {
			continue
		}
		parts := strings.Split(key, ".")
		if len(parts) != 3 {
			continue
		}
		env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
			continue
		}
		data, _ := env["data"].(map[string]any)
		opType, _ := data["operationType"].(string)
		for _, expected := range loftspaceExpectedOps {
			if opType == expected {
				permIDsByOp[opType] = append(permIDsByOp[opType], parts[2])
				break
			}
		}
	}

	for _, op := range loftspaceExpectedOps {
		permIDs := permIDsByOp[op]
		if len(permIDs) == 0 {
			fail("vtx.permission.*[operationType="+op+"]", "not found in Core KV")
			continue
		}
		grants := loftspaceOpGrants[op]
		wantScopes := map[string]bool{}
		for _, g := range grants {
			wantScopes[g.scope] = true
		}
		if len(permIDs) != len(wantScopes) {
			fail(fmt.Sprintf("vtx.permission.*[operationType=%s]", op),
				fmt.Sprintf("found %d permission vertices, want %d distinct scope(s) (%v)", len(permIDs), len(wantScopes), grants))
			continue
		}
		for _, grant := range grants {
			// Match this grant to the permission vertex carrying its scope —
			// each op's grants declare distinct scopes.
			var matchedID string
			for _, permID := range permIDs {
				env, err := pkgverify.GetEnvelope(ctx, coreKV, "vtx.permission."+permID)
				if err != nil {
					continue
				}
				data, _ := env["data"].(map[string]any)
				if scope, _ := data["scope"].(string); scope == grant.scope {
					matchedID = permID
					break
				}
			}
			if matchedID == "" {
				fail(fmt.Sprintf("vtx.permission.*[operationType=%s,scope=%s]", op, grant.scope),
					"not found among discovered permission vertices")
				continue
			}
			permKey := "vtx.permission." + matchedID
			ok(fmt.Sprintf("%s operationType=%s scope=%s", permKey, op, grant.scope))

			granteeRoleID, roleFound := roleIDByCanonical[grant.grantee]
			if !roleFound {
				fail(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<%s>", matchedID, grant.grantee),
					fmt.Sprintf("role %q NanoID not found; cannot verify grant link", grant.grantee))
				continue
			}
			linkKey := "lnk.permission." + matchedID + ".grantedBy.role." + granteeRoleID
			if _, exists := allKeys[linkKey]; !exists {
				fail(linkKey, "grantedBy."+grant.grantee+" link not found")
			} else if lenv, err := pkgverify.GetEnvelope(ctx, coreKV, linkKey); err != nil {
				fail(linkKey, fmt.Sprintf("cannot read: %v", err))
			} else if isDeleted, _ := lenv["isDeleted"].(bool); isDeleted {
				fail(linkKey, "link is tombstoned")
			} else {
				ok(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<%s> exists", matchedID, grant.grantee))
			}
		}
	}

	// Package manifest.
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, loftspacePackageName)
	if err != nil || pkgKey == "" {
		fail("loftspace-domain package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", loftspacePackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if name, _ := data["name"].(string); name != loftspacePackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, loftspacePackageName))
			} else {
				ok(pkgManifestKey + " name=loftspace-domain")
			}
			if err := pkgverify.CheckAspectEnvelope(env, pkgManifestKey, pkgKey, "manifest"); err != nil {
				fail(pkgManifestKey+" envelope", err.Error())
			} else {
				ok(pkgManifestKey + " envelope shape OK")
			}
		}
	}

	fmt.Println()
	if len(failures) == 0 {
		fmt.Printf("verify-package-loftspace-domain: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-loftspace-domain: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: install location-domain then loftspace-domain, then re-run.\n")
	os.Exit(1)
}
