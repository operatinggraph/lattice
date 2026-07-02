//go:build ignore

// verify-package-service-location.go — assertion tool for
// `make verify-package-service-location`.
//
// Connects to a running Lattice NATS instance and checks that the
// service-location package has been correctly installed (co-installed with its
// dependencies location-domain + service-domain). Asserts:
//
//	1 serviceLocation DDL meta-vertex (vtx.meta.<NanoID>) class=meta.ddl.vertexType
//	  + its 8 self-description aspects (canonicalName, permittedCommands [8 ops],
//	    description, script, inputSchema, outputSchema, fieldDescription, examples)
//	8 permission vertices (Wire/Unwire × residesIn/availableAt/unavailableAt/
//	    permitsOperation), scope any, each grantedBy → operator
//	1 capabilityServiceAccess Lens meta-vertex (class=meta.lens) whose spec
//	    cypherRule walks residesIn/availableAt/unavailableAt/permitsOperation and
//	    is projectionKind actorAggregate keyed cap.svc.{actorSuffix}
//	1 package vertex (vtx.package.<NanoID>) + 1 manifest aspect (name=service-location)
//
// Run via: go run ./scripts/verify-package-service-location.go
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
	slPackageName  = "service-location"
	slDDLCanonical = "serviceLocation"
	slLensCanon    = "capabilityServiceAccess"
	slCoreKVBucket = "core-kv"
)

var slExpectedOps = []string{
	"WireResidesIn", "UnwireResidesIn",
	"WireAvailableAt", "UnwireAvailableAt",
	"WireUnavailableAt", "UnwireUnavailableAt",
	"WirePermitsOperation", "UnwirePermitsOperation",
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

	coreKV, err := js.KeyValue(ctx, slCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", slCoreKVBucket, err)
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

	fmt.Printf("verify-package-service-location: scanning %d Core KV keys...\n", len(allKeys))

	// 1. serviceLocation DDL meta-vertex + aspects.
	ddlKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, slDDLCanonical)
	if err != nil || ddlKey == "" {
		fail("serviceLocation DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", slDDLCanonical, err))
	} else {
		ok(fmt.Sprintf("serviceLocation DDL meta-vertex exists: %s", ddlKey))
	}

	if ddlKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, ddlKey); err != nil {
			fail(ddlKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			if cls, _ := env["class"].(string); cls != "meta.ddl.vertexType" {
				fail(ddlKey+" class", fmt.Sprintf("got %q want meta.ddl.vertexType", cls))
			} else {
				ok(ddlKey + " class=meta.ddl.vertexType")
			}
			if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
				fail(ddlKey+" isDeleted", "vertex is tombstoned")
			} else {
				ok(ddlKey + " isDeleted=false")
			}
		}

		// permittedCommands — all 8 ops present.
		pcKey := ddlKey + ".permittedCommands"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := pkgverify.ToStringSlice(data["commands"])
			cmdSet := pkgverify.ToSet(cmds)
			allPresent := true
			for _, op := range slExpectedOps {
				if !cmdSet[op] {
					fail(pcKey, fmt.Sprintf("missing command %q", op))
					allPresent = false
				}
			}
			if len(cmds) != len(slExpectedOps) {
				fail(pcKey, fmt.Sprintf("command count=%d want %d", len(cmds), len(slExpectedOps)))
				allPresent = false
			}
			if allPresent && len(cmds) == len(slExpectedOps) {
				ok(fmt.Sprintf("%s contains all %d commands", pcKey, len(slExpectedOps)))
			}
			if err := pkgverify.CheckAspectEnvelope(env, pcKey, ddlKey, "permittedCommands"); err != nil {
				fail(pcKey+" envelope", err.Error())
			} else {
				ok(pcKey + " envelope shape OK")
			}
		}

		// canonicalName + the remaining self-description aspects.
		cnKey := ddlKey + ".canonicalName"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, cnKey); err != nil {
			fail(cnKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if val, _ := data["value"].(string); val != slDDLCanonical {
				fail(cnKey, fmt.Sprintf("value=%q want %q", val, slDDLCanonical))
			} else {
				ok(cnKey + " value=serviceLocation")
			}
		}
		for _, asp := range []string{"description", "script", "inputSchema", "outputSchema", "fieldDescription", "examples"} {
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

	// 2. permission vertices + scope + grantedBy-operator links.
	permIDByOp := map[string]string{}
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
		for _, expected := range slExpectedOps {
			if opType == expected {
				permIDByOp[opType] = parts[2]
				break
			}
		}
	}

	operatorRoleID := bootstrap.RoleOperatorID
	for _, op := range slExpectedOps {
		permID, found := permIDByOp[op]
		if !found {
			fail("vtx.permission.*[operationType="+op+"]", "not found in Core KV")
			continue
		}
		permKey := "vtx.permission." + permID
		ok(fmt.Sprintf("%s operationType=%s", permKey, op))

		if env, err := pkgverify.GetEnvelope(ctx, coreKV, permKey); err == nil {
			data, _ := env["data"].(map[string]any)
			if scope, _ := data["scope"].(string); scope != "any" {
				fail(permKey+" scope", fmt.Sprintf("got %q want any", scope))
			} else {
				ok(permKey + " scope=any")
			}
		}

		linkKey := "lnk.permission." + permID + ".grantedBy.role." + operatorRoleID
		if _, exists := allKeys[linkKey]; !exists {
			fail(linkKey, "grantedBy.operator link not found")
		} else if lenv, err := pkgverify.GetEnvelope(ctx, coreKV, linkKey); err != nil {
			fail(linkKey, fmt.Sprintf("cannot read: %v", err))
		} else if isDeleted, _ := lenv["isDeleted"].(bool); isDeleted {
			fail(linkKey, "link is tombstoned")
		} else {
			ok(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<operator> exists", permID))
		}
	}

	// 3. capabilityServiceAccess Lens meta-vertex.
	lensKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, slLensCanon)
	if err != nil || lensKey == "" {
		fail(slLensCanon+" Lens meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", slLensCanon, err))
	} else {
		ok(fmt.Sprintf("%s Lens meta-vertex exists: %s", slLensCanon, lensKey))
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, lensKey); err != nil {
			fail(lensKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			if cls, _ := env["class"].(string); cls != "meta.lens" {
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
			for _, term := range []string{"residesIn", "availableAt", "unavailableAt", "permitsOperation", "serviceAccess", "instanceOf"} {
				if !strings.Contains(src, term) {
					missing = append(missing, term)
				}
			}
			if len(missing) > 0 {
				fail(specKey, fmt.Sprintf("spec cypherRule missing terms: %v", missing))
			} else {
				ok(specKey + " cypherRule walks the service-location vocabulary")
			}
			if pk, _ := data["projectionKind"].(string); pk != "actorAggregate" {
				fail(specKey+" projectionKind", fmt.Sprintf("got %q want actorAggregate", pk))
			} else {
				ok(specKey + " projectionKind=actorAggregate")
			}
			if err := pkgverify.CheckAspectEnvelope(env, specKey, lensKey, "spec"); err != nil {
				fail(specKey+" envelope", err.Error())
			} else {
				ok(specKey + " envelope shape OK")
			}
		}
	}

	// 4. Package manifest.
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, slPackageName)
	if err != nil || pkgKey == "" {
		fail("service-location package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", slPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if name, _ := data["name"].(string); name != slPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, slPackageName))
			} else {
				ok(pkgManifestKey + " name=service-location")
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
		fmt.Printf("verify-package-service-location: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-service-location: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up && make verify-package-service-location` to reinstall from clean state.\n")
	os.Exit(1)
}
