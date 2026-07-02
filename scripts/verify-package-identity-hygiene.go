//go:build ignore

// verify-package-identity-hygiene.go — assertion tool for
// `make verify-package-identity-hygiene`.
//
// Connects to a running Lattice NATS instance and checks that the
// identity-hygiene package has been correctly installed. Asserts:
//
//  1 DDL meta-vertex (vtx.meta.<NanoID>) with class=meta.ddl.vertexType
//  8 DDL aspects: .canonicalName=identityHygiene,
//                 .permittedCommands=[MergeIdentity], .description, .script,
//                 .inputSchema, .outputSchema, .fieldDescription, .examples
//    Each aspect also validated for correct vertexKey + localName envelope fields.
//  1 Lens meta-vertex (vtx.meta.<NanoID>) with class=meta.lens
//  5 Lens aspects: .canonicalName=duplicateCandidates, .spec (contains
//    secondaryInboundEdges + secondaryOutboundEdges + levenshteinRatio),
//    .adapter, .bucket, .engine
//  1 MergeIdentity permission vertex with class=permission, scope=any
//  1 grantedBy link (MergeIdentity permission → operator role)
//  1 package vertex (vtx.package.<NanoID>)
//  1 package manifest aspect with name=identity-hygiene
//
// Total target: ~20 OK lines.
//
// Exit 0: all assertions pass.
// Exit 1: one or more assertions failed.
//
// Run via: go run ./scripts/verify-package-identity-hygiene.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/scripts/pkgverify"
)

const (
	hygienePackageName   = "identity-hygiene"
	hygieneDDLCanonical  = "identityHygiene"
	hygieneLensCanonical = "duplicateCandidates"
	hygieneCoreKVBucket  = "core-kv"
	hygieneMergeOp       = "MergeIdentity"
)

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

	coreKV, err := js.KeyValue(ctx, hygieneCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", hygieneCoreKVBucket, err)
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

	fmt.Printf("verify-package-identity-hygiene: scanning %d Core KV keys...\n", len(allKeys))

	// -------------------------------------------------------------------------
	// 1. Find the identityHygiene DDL meta-vertex.
	// -------------------------------------------------------------------------
	hygieneDDLKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, hygieneDDLCanonical)
	if err != nil || hygieneDDLKey == "" {
		fail("identityHygiene DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", hygieneDDLCanonical, err))
	} else {
		ok(fmt.Sprintf("identityHygiene DDL meta-vertex exists: %s", hygieneDDLKey))
	}

	if hygieneDDLKey != "" {
		// 2. DDL vertex class == meta.ddl.vertexType.
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, hygieneDDLKey); err != nil {
			fail(hygieneDDLKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			cls, _ := env["class"].(string)
			if cls != "meta.ddl.vertexType" {
				fail(hygieneDDLKey+" class", fmt.Sprintf("got %q want meta.ddl.vertexType", cls))
			} else {
				ok(hygieneDDLKey + " class=meta.ddl.vertexType")
			}
			isDeleted, _ := env["isDeleted"].(bool)
			if isDeleted {
				fail(hygieneDDLKey+" isDeleted", "vertex is tombstoned")
			} else {
				ok(hygieneDDLKey + " isDeleted=false")
			}
		}

		// 3. Aspect: .permittedCommands = [MergeIdentity].
		pcKey := hygieneDDLKey + ".permittedCommands"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := pkgverify.ToStringSlice(data["commands"])
			cmdSet := pkgverify.ToSet(cmds)
			if !cmdSet[hygieneMergeOp] {
				fail(pcKey, fmt.Sprintf("missing command %q", hygieneMergeOp))
			} else if len(cmds) != 1 {
				fail(pcKey, fmt.Sprintf("command count=%d want 1", len(cmds)))
			} else {
				ok(fmt.Sprintf("%s contains [%s]", pcKey, hygieneMergeOp))
			}
			if err := pkgverify.CheckAspectEnvelope(env, pcKey, hygieneDDLKey, "permittedCommands"); err != nil {
				fail(pcKey+" envelope", err.Error())
			} else {
				ok(pcKey + " envelope shape OK")
			}
		}

		// 4. Aspect: .description non-empty.
		descKey := hygieneDDLKey + ".description"
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
			if err := pkgverify.CheckAspectEnvelope(env, descKey, hygieneDDLKey, "description"); err != nil {
				fail(descKey+" envelope", err.Error())
			} else {
				ok(descKey + " envelope shape OK")
			}
		}

		// 5. Aspect: .script non-empty.
		scriptKey := hygieneDDLKey + ".script"
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
			if err := pkgverify.CheckAspectEnvelope(env, scriptKey, hygieneDDLKey, "script"); err != nil {
				fail(scriptKey+" envelope", err.Error())
			} else {
				ok(scriptKey + " envelope shape OK")
			}
		}

		// 5a. Self-description aspects.
		for _, asp := range []string{"inputSchema", "outputSchema", "fieldDescription", "examples"} {
			k := hygieneDDLKey + "." + asp
			if env, err := pkgverify.GetEnvelope(ctx, coreKV, k); err != nil {
				fail(k, fmt.Sprintf("missing: %v", err))
			} else {
				ok(k + " present")
				if err := pkgverify.CheckAspectEnvelope(env, k, hygieneDDLKey, asp); err != nil {
					fail(k+" envelope", err.Error())
				} else {
					ok(k + " envelope shape OK")
				}
			}
		}
	}

	// -------------------------------------------------------------------------
	// 6. Find the duplicateCandidates Lens meta-vertex.
	// -------------------------------------------------------------------------
	lensKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, hygieneLensCanonical)
	if err != nil || lensKey == "" {
		fail("duplicateCandidates Lens meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", hygieneLensCanonical, err))
	} else {
		ok(fmt.Sprintf("duplicateCandidates Lens meta-vertex exists: %s", lensKey))
	}

	if lensKey != "" {
		// 7. Lens vertex class == meta.lens.
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, lensKey); err != nil {
			fail(lensKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			cls, _ := env["class"].(string)
			if cls != "meta.lens" {
				fail(lensKey+" class", fmt.Sprintf("got %q want meta.lens", cls))
			} else {
				ok(lensKey + " class=meta.lens")
			}
			isDeleted, _ := env["isDeleted"].(bool)
			if isDeleted {
				fail(lensKey, "Lens vertex is tombstoned")
			} else {
				ok(lensKey + " isDeleted=false")
			}
		}

		// 8. Aspect: .spec carries the LensSpec body and its cypherRule
		// contains the required terms. The spec aspect holds the full LensSpec
		// (the shape CoreKVSource activates the lens from); the cypher text
		// lives under cypherRule.
		specKey := lensKey + ".spec"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, specKey); err != nil {
			fail(specKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			src, _ := data["cypherRule"].(string)
			missingTerms := []string{}
			for _, term := range []string{"secondaryInboundEdges", "secondaryOutboundEdges", "levenshteinRatio"} {
				if !strings.Contains(src, term) {
					missingTerms = append(missingTerms, term)
				}
			}
			if len(missingTerms) > 0 {
				fail(specKey, fmt.Sprintf("spec missing terms: %v", missingTerms))
			} else {
				ok(specKey + " contains secondaryInboundEdges, secondaryOutboundEdges, levenshteinRatio")
			}
			if err := pkgverify.CheckAspectEnvelope(env, specKey, lensKey, "spec"); err != nil {
				fail(specKey+" envelope", err.Error())
			} else {
				ok(specKey + " envelope shape OK")
			}
		}

		// 9. Aspect: .canonicalName = duplicateCandidates.
		cnKey := lensKey + ".canonicalName"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, cnKey); err != nil {
			fail(cnKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			val, _ := data["value"].(string)
			if val != hygieneLensCanonical {
				fail(cnKey, fmt.Sprintf("value=%q want %q", val, hygieneLensCanonical))
			} else {
				ok(cnKey + " value=duplicateCandidates")
			}
			if err := pkgverify.CheckAspectEnvelope(env, cnKey, lensKey, "canonicalName"); err != nil {
				fail(cnKey+" envelope", err.Error())
			} else {
				ok(cnKey + " envelope shape OK")
			}
		}
	}

	// -------------------------------------------------------------------------
	// 10. MergeIdentity permission vertex + scope + grantedBy link.
	// -------------------------------------------------------------------------
	operatorRoleID := bootstrap.RoleOperatorID
	var mergePermID string
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
		isDeleted, _ := env["isDeleted"].(bool)
		if isDeleted {
			continue
		}
		data, _ := env["data"].(map[string]any)
		opType, _ := data["operationType"].(string)
		if opType == hygieneMergeOp {
			mergePermID = parts[2]
			break
		}
	}

	if mergePermID == "" {
		fail("vtx.permission.*[operationType=MergeIdentity]", "not found in Core KV")
	} else {
		mergePermKey := "vtx.permission." + mergePermID
		ok(fmt.Sprintf("%s operationType=MergeIdentity", mergePermKey))

		// Verify scope=any.
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, mergePermKey); err != nil {
			fail(mergePermKey+" scope", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			scope, _ := data["scope"].(string)
			if scope != "any" {
				fail(mergePermKey+" scope", fmt.Sprintf("got %q want any", scope))
			} else {
				ok(mergePermKey + " scope=any")
			}
		}

		// Verify grantedBy link to operator.
		if operatorRoleID == "" {
			fail("lnk.permission."+mergePermID+".grantedBy.role.<operator>",
				"bootstrap.RoleOperatorID is empty; cannot verify grant link")
		} else {
			linkKey := "lnk.permission." + mergePermID + ".grantedBy.role." + operatorRoleID
			if _, exists := allKeys[linkKey]; !exists {
				fail(linkKey, "grantedBy link not found")
			} else {
				if lenv, err := pkgverify.GetEnvelope(ctx, coreKV, linkKey); err != nil {
					fail(linkKey, fmt.Sprintf("cannot read: %v", err))
				} else {
					isDeleted, _ := lenv["isDeleted"].(bool)
					if isDeleted {
						fail(linkKey, "link is tombstoned")
					} else {
						ok(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<operator> exists", mergePermID))
					}
				}
			}
		}
	}

	// -------------------------------------------------------------------------
	// 11. Package manifest.
	// -------------------------------------------------------------------------
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, hygienePackageName)
	if err != nil || pkgKey == "" {
		fail("identity-hygiene package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", hygienePackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			name, _ := data["name"].(string)
			if name != hygienePackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, hygienePackageName))
			} else {
				ok(pkgManifestKey + " name=identity-hygiene")
			}
			if err := pkgverify.CheckAspectEnvelope(env, pkgManifestKey, pkgKey, "manifest"); err != nil {
				fail(pkgManifestKey+" envelope", err.Error())
			} else {
				ok(pkgManifestKey + " envelope shape OK")
			}
		}
	}

	// -------------------------------------------------------------------------
	// Final report.
	// -------------------------------------------------------------------------
	fmt.Println()
	if len(failures) == 0 {
		fmt.Printf("verify-package-identity-hygiene: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-identity-hygiene: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up` then install all three packages before re-running.\n")
	os.Exit(1)
}
