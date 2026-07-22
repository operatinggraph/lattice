//go:build ignore

// verify-package-rbac.go — assertion tool for `make verify-package-rbac`.
//
// Connects to a running Lattice NATS instance and checks that the
// rbac-domain package has been correctly installed. Asserts:
//
//  1 DDL meta-vertex (vtx.meta.<NanoID>) with class=meta.ddl.vertexType
//  8 DDL aspects: .canonicalName=rbac, .permittedCommands (10 ops), .description, .script,
//                 .inputSchema, .outputSchema, .fieldDescription, .examples
//    Each aspect also validated for correct vertexKey + localName envelope fields.
// 10 permission vertices (vtx.permission.<NanoID>) — one per op
// 10 grantedBy link keys (each permission → operator role)
//  2 Lens meta-vertices (vtx.meta.<NanoID>, class=meta.lens):
//      - capabilityRoles (actorAggregate; cypher walks holdsRole/grantedBy →
//        platformPermissions; projects cap.roles.<actor>)
//      - capabilityRoleIndex (operation-aggregate; the FR22 role-by-operation
//        index)
//    plus each lens's .spec + .canonicalName aspects.
//  1 package vertex (vtx.package.<NanoID>)
//  1 package manifest aspect (vtx.package.<NanoID>.manifest) with name=rbac-domain
//
// Total target: ~45 OK lines.
//
// Exit 0: all assertions pass.
// Exit 1: one or more assertions failed.
//
// Run via: go run ./scripts/verify-package-rbac.go
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
)

const (
	rbacPackageName  = "rbac-domain"
	rbacDDLCanonical = "rbac"
	coreKVBucket     = "core-kv"
)

var rbacExpectedOps = []string{
	"CreateRole", "UpdateRole", "TombstoneRole",
	"CreatePermission", "UpdatePermission", "TombstonePermission",
	"AssignRole", "RevokeRole",
	"GrantPermission", "RevokePermission",
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

	coreKV, err := js.KeyValue(ctx, coreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", coreKVBucket, err)
		os.Exit(1)
	}

	// Snapshot all keys in core-kv into a map for O(1) lookup.
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

	fmt.Printf("verify-package-rbac: scanning %d Core KV keys...\n", len(allKeys))

	// -------------------------------------------------------------------------
	// 1. Find the rbac DDL meta-vertex by scanning vtx.meta.* .canonicalName
	//    aspects and matching value=rbac.
	// -------------------------------------------------------------------------
	rbacDDLKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, rbacDDLCanonical)
	if err != nil || rbacDDLKey == "" {
		fail("rbac DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", rbacDDLCanonical, err))
	} else {
		ok(fmt.Sprintf("rbac DDL meta-vertex exists: %s", rbacDDLKey))
	}

	if rbacDDLKey != "" {
		// 2. DDL vertex class == meta.ddl.vertexType.
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, rbacDDLKey); err != nil {
			fail(rbacDDLKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			cls, _ := env["class"].(string)
			if cls != "meta.ddl.vertexType" {
				fail(rbacDDLKey+" class", fmt.Sprintf("got %q want meta.ddl.vertexType", cls))
			} else {
				ok(rbacDDLKey + " class=meta.ddl.vertexType")
			}
			isDeleted, _ := env["isDeleted"].(bool)
			if isDeleted {
				fail(rbacDDLKey+" isDeleted", "vertex is tombstoned")
			} else {
				ok(rbacDDLKey + " isDeleted=false")
			}
		}

		// 3. Aspect: .canonicalName = rbac. Also validate envelope shape.
		cnKey := rbacDDLKey + ".canonicalName"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, cnKey); err != nil {
			fail(cnKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			val, _ := data["value"].(string)
			if val != rbacDDLCanonical {
				fail(cnKey, fmt.Sprintf("canonicalName value=%q want %q", val, rbacDDLCanonical))
			} else {
				ok(cnKey + " value=rbac")
			}
			if err := pkgverify.CheckAspectEnvelope(env, cnKey, rbacDDLKey, "canonicalName"); err != nil {
				fail(cnKey+" envelope", err.Error())
			} else {
				ok(cnKey + " envelope shape OK")
			}
		}

		// 4. Aspect: .permittedCommands contains all 10 ops.
		pcKey := rbacDDLKey + ".permittedCommands"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := pkgverify.ToStringSlice(data["commands"])
			cmdSet := pkgverify.ToSet(cmds)
			allPresent := true
			for _, op := range rbacExpectedOps {
				if !cmdSet[op] {
					fail(pcKey, fmt.Sprintf("missing command %q", op))
					allPresent = false
				}
			}
			if len(cmds) != len(rbacExpectedOps) {
				fail(pcKey, fmt.Sprintf("command count=%d want %d", len(cmds), len(rbacExpectedOps)))
				allPresent = false
			}
			if allPresent && len(cmds) == len(rbacExpectedOps) {
				ok(fmt.Sprintf("%s contains all %d commands", pcKey, len(rbacExpectedOps)))
			}
			if err := pkgverify.CheckAspectEnvelope(env, pcKey, rbacDDLKey, "permittedCommands"); err != nil {
				fail(pcKey+" envelope", err.Error())
			} else {
				ok(pcKey + " envelope shape OK")
			}
		}

		// 5. Aspect: .description non-empty.
		descKey := rbacDDLKey + ".description"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, descKey); err != nil {
			fail(descKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			text, _ := data["text"].(string)
			if strings.TrimSpace(text) == "" {
				fail(descKey, "description text is empty")
			} else {
				ok(descKey + " non-empty")
			}
			if err := pkgverify.CheckAspectEnvelope(env, descKey, rbacDDLKey, "description"); err != nil {
				fail(descKey+" envelope", err.Error())
			} else {
				ok(descKey + " envelope shape OK")
			}
		}

		// 6. Aspect: .script non-empty.
		scriptKey := rbacDDLKey + ".script"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, scriptKey); err != nil {
			fail(scriptKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			src, _ := data["source"].(string)
			if strings.TrimSpace(src) == "" {
				fail(scriptKey, "script source is empty")
			} else {
				ok(scriptKey + " non-empty")
			}
			if err := pkgverify.CheckAspectEnvelope(env, scriptKey, rbacDDLKey, "script"); err != nil {
				fail(scriptKey+" envelope", err.Error())
			} else {
				ok(scriptKey + " envelope shape OK")
			}
		}

		// 6a. Self-description aspects.
		isKey := rbacDDLKey + ".inputSchema"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, isKey); err != nil {
			fail(isKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if s, ok2 := data["schema"].(string); !ok2 || strings.TrimSpace(s) == "" {
				fail(isKey, "inputSchema empty or wrong type")
			} else {
				ok(isKey + " present")
			}
			if err := pkgverify.CheckAspectEnvelope(env, isKey, rbacDDLKey, "inputSchema"); err != nil {
				fail(isKey+" envelope", err.Error())
			} else {
				ok(isKey + " envelope shape OK")
			}
		}
		osKey := rbacDDLKey + ".outputSchema"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, osKey); err != nil {
			fail(osKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if s, ok2 := data["schema"].(string); !ok2 || strings.TrimSpace(s) == "" {
				fail(osKey, "outputSchema empty or wrong type")
			} else {
				ok(osKey + " present")
			}
			if err := pkgverify.CheckAspectEnvelope(env, osKey, rbacDDLKey, "outputSchema"); err != nil {
				fail(osKey+" envelope", err.Error())
			} else {
				ok(osKey + " envelope shape OK")
			}
		}
		fdKey := rbacDDLKey + ".fieldDescription"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, fdKey); err != nil {
			fail(fdKey, fmt.Sprintf("missing: %v", err))
		} else {
			ok(fdKey + " present")
			if err := pkgverify.CheckAspectEnvelope(env, fdKey, rbacDDLKey, "fieldDescription"); err != nil {
				fail(fdKey+" envelope", err.Error())
			} else {
				ok(fdKey + " envelope shape OK")
			}
		}
		exKey := rbacDDLKey + ".examples"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, exKey); err != nil {
			fail(exKey, fmt.Sprintf("missing: %v", err))
		} else {
			ok(exKey + " present")
			if err := pkgverify.CheckAspectEnvelope(env, exKey, rbacDDLKey, "examples"); err != nil {
				fail(exKey+" envelope", err.Error())
			} else {
				ok(exKey + " envelope shape OK")
			}
		}
	}

	// -------------------------------------------------------------------------
	// 7. Find the package vertex by scanning vtx.package.*.manifest where
	//    data.name=rbac-domain.
	// -------------------------------------------------------------------------
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, rbacPackageName)
	if err != nil || pkgKey == "" {
		fail("rbac-domain package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", rbacPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}

	// Verify manifest carries the correct name.
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			name, _ := data["name"].(string)
			if name != rbacPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, rbacPackageName))
			} else {
				ok(pkgManifestKey + " name=rbac-domain")
			}
			// Manifest is an aspect of the package vertex; check envelope shape.
			if err := pkgverify.CheckAspectEnvelope(env, pkgManifestKey, pkgKey, "manifest"); err != nil {
				fail(pkgManifestKey+" envelope", err.Error())
			} else {
				ok(pkgManifestKey + " envelope shape OK")
			}
		}
	}

	// -------------------------------------------------------------------------
	// 7b. Verify rbac-domain's two Lens meta-vertices (capabilityRoles +
	// capabilityRoleIndex). rbac-domain owns the role-derived grant projection
	// (cap.roles.<actor>) and the role-by-operation index (the FR22 denial-path
	// source) — both decomposed out of the bootstrap god-cypher into the
	// package. Each lens is a vtx.meta.<NanoID> class=meta.lens carrying a spec
	// aspect whose cypherRule walks the rbac grant vocabulary.
	// -------------------------------------------------------------------------
	rbacLenses := []struct {
		canonical    string
		cypherTerms  []string
		hasProjKind  bool
		projKindWant string
	}{
		{
			canonical:    "capabilityRoles",
			cypherTerms:  []string{"holdsRole", "grantedBy", "platformPermissions"},
			hasProjKind:  true,
			projKindWant: "actorAggregate",
		},
		{
			canonical:   "capabilityRoleIndex",
			cypherTerms: []string{"grantedBy", "operationType"},
			hasProjKind: false,
		},
	}
	for _, l := range rbacLenses {
		lensKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, l.canonical)
		if err != nil || lensKey == "" {
			fail(l.canonical+" Lens meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", l.canonical, err))
			continue
		}
		ok(fmt.Sprintf("%s Lens meta-vertex exists: %s", l.canonical, lensKey))

		if env, err := pkgverify.GetEnvelope(ctx, coreKV, lensKey); err != nil {
			fail(lensKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			cls, _ := env["class"].(string)
			if cls != "meta.lens" {
				fail(lensKey+" class", fmt.Sprintf("got %q want meta.lens", cls))
			} else {
				ok(lensKey + " class=meta.lens")
			}
			if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
				fail(lensKey, "Lens vertex is tombstoned")
			}
		}

		specKey := lensKey + ".spec"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, specKey); err != nil {
			fail(specKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			src, _ := data["cypherRule"].(string)
			missing := []string{}
			for _, term := range l.cypherTerms {
				if !strings.Contains(src, term) {
					missing = append(missing, term)
				}
			}
			if len(missing) > 0 {
				fail(specKey, fmt.Sprintf("spec cypherRule missing terms: %v", missing))
			} else {
				ok(fmt.Sprintf("%s contains %v", specKey, l.cypherTerms))
			}
			if l.hasProjKind {
				pk, _ := data["projectionKind"].(string)
				if pk != l.projKindWant {
					fail(specKey+" projectionKind", fmt.Sprintf("got %q want %q", pk, l.projKindWant))
				} else {
					ok(specKey + " projectionKind=" + l.projKindWant)
				}
			}
			if err := pkgverify.CheckAspectEnvelope(env, specKey, lensKey, "spec"); err != nil {
				fail(specKey+" envelope", err.Error())
			} else {
				ok(specKey + " envelope shape OK")
			}
		}

		cnKey := lensKey + ".canonicalName"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, cnKey); err != nil {
			fail(cnKey, fmt.Sprintf("missing: %v", err))
		} else {
			if err := pkgverify.CheckAspectEnvelope(env, cnKey, lensKey, "canonicalName"); err != nil {
				fail(cnKey+" envelope", err.Error())
			} else {
				ok(cnKey + " envelope shape OK")
			}
		}
	}

	// -------------------------------------------------------------------------
	// 8. Find permission vertices for each expected op, and verify grant links.
	//
	// Strategy: scan vtx.permission.* where data.operationType matches one of
	// the 10 ops. Collect the permID for each. Then verify grantedBy link.
	// -------------------------------------------------------------------------
	operatorRoleID := bootstrap.RoleOperatorID
	if operatorRoleID == "" {
		fail("operator role ID", "bootstrap.RoleOperatorID is empty after Load; cannot verify grant links")
	}

	// Map op → permID for permission vertices found in KV.
	permIDByOp := map[string]string{}
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.permission.") {
			continue
		}
		// Skip aspect keys — only vertex tops.
		parts := strings.Split(key, ".")
		if len(parts) != 3 {
			continue
		}
		env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		isDeleted, _ := env["isDeleted"].(bool)
		if isDeleted {
			continue
		}
		data, _ := env["data"].(map[string]any)
		opType, _ := data["operationType"].(string)
		if opType == "" {
			continue
		}
		// Only record ops that belong to rbac-domain.
		for _, expected := range rbacExpectedOps {
			if opType == expected {
				permIDByOp[opType] = parts[2]
				break
			}
		}
	}

	// Assert one permission vertex per expected op.
	sortedOps := make([]string, len(rbacExpectedOps))
	copy(sortedOps, rbacExpectedOps)
	sort.Strings(sortedOps)

	for _, op := range sortedOps {
		permID, found := permIDByOp[op]
		if !found {
			fail("vtx.permission.*[operationType="+op+"]", "not found in Core KV")
			continue
		}
		permKey := "vtx.permission." + permID
		ok(fmt.Sprintf("%s operationType=%s", permKey, op))

		// Verify scope=any.
		env, err := pkgverify.GetEnvelope(ctx, coreKV, permKey)
		if err == nil {
			data, _ := env["data"].(map[string]any)
			scope, _ := data["scope"].(string)
			if scope != "any" {
				fail(permKey+" scope", fmt.Sprintf("got %q want any", scope))
			} else {
				ok(permKey + " scope=any")
			}
		}

		// Verify grantedBy link to operator role.
		if operatorRoleID != "" {
			linkKey := "lnk.permission." + permID + ".grantedBy.role." + operatorRoleID
			if _, exists := allKeys[linkKey]; !exists {
				fail(linkKey, "grantedBy link not found")
			} else {
				if env, err := pkgverify.GetEnvelope(ctx, coreKV, linkKey); err != nil {
					fail(linkKey, fmt.Sprintf("cannot read: %v", err))
				} else {
					isDeleted, _ := env["isDeleted"].(bool)
					if isDeleted {
						fail(linkKey, "link is tombstoned")
					} else {
						ok(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<operator> exists", permID))
					}
				}
			}
		}
	}

	// -------------------------------------------------------------------------
	// Final report.
	// -------------------------------------------------------------------------
	fmt.Println()
	if len(failures) == 0 {
		fmt.Printf("verify-package-rbac: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-rbac: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up && make verify-package-rbac` to reinstall from clean state.\n")
	os.Exit(1)
}
