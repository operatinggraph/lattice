//go:build ignore

// verify-package-location-domain.go — assertion tool for
// `make verify-package-location-domain`.
//
// Connects to a running Lattice NATS instance and checks that the
// location-domain package has been correctly installed. Asserts:
//
//	4 DDL meta-vertices (vtx.meta.<NanoID>), all class=meta.ddl.vertexType:
//	               the ABSTRACT `location` (data.abstract=true; .script and
//	               .permittedCommands absent on a fresh install, tombstoned on
//	               a cell upgraded in place — never live) and the three CONCRETE leaves
//	               unit/building/property (data.abstract absent/false, the
//	               shared .script, .permittedCommands with all 5 ops)
//	               — each with .canonicalName, .description, .inputSchema,
//	               .outputSchema, .fieldDescription, .examples
//	3 subtypeOf links lnk.meta.<leafId>.subtypeOf.meta.<locationId>, leaf ->
//	               abstract, class=subtypeOf
//	5 permission vertices (CreateLocation, TombstoneLocation, WireContainedIn,
//	               UnwireContainedIn, SetLocationPresentation), scope any
//	5 grantedBy links (each op → operator)
//	1 package vertex (vtx.package.<NanoID>) + 1 manifest aspect (name=location-domain)
//
// Run via: go run ./scripts/verify-package-location-domain.go
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
	locationPackageName  = "location-domain"
	locationDDLCanonical = "location"
	locationCoreKVBucket = "core-kv"
)

var locationExpectedOps = []string{"CreateLocation", "TombstoneLocation", "WireContainedIn", "UnwireContainedIn", "SetLocationPresentation"}

// locationLeafCanonicals are the three CONCRETE location types, each declared
// a subtypeOf the abstract `location` above.
var locationLeafCanonicals = []string{"unit", "building", "property"}

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

	coreKV, err := js.KeyValue(ctx, locationCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", locationCoreKVBucket, err)
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

	fmt.Printf("verify-package-location-domain: scanning %d Core KV keys...\n", len(allKeys))

	// 1-5. The FOUR type metas the taxonomy declares: the abstract `location`
	// and its three concrete leaves, plus the subtypeOf edge from each leaf.
	metaKeyOf := func(canonical string) string {
		k, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, canonical)
		if err != nil || k == "" {
			fail(canonical+" DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", canonical, err))
			return ""
		}
		ok(fmt.Sprintf("%s DDL meta-vertex exists: %s", canonical, k))
		return k
	}
	// checkRoot asserts the meta root's class + liveness, and that data.abstract
	// matches what the declaration says (present and true for the abstract type,
	// absent or false for a concrete leaf — the marker is EXPLICIT, never
	// derived from the absence of a script).
	checkRoot := func(metaKey, canonical string, wantAbstract bool) {
		env, err := pkgverify.GetEnvelope(ctx, coreKV, metaKey)
		if err != nil {
			fail(metaKey+" root", fmt.Sprintf("cannot read: %v", err))
			return
		}
		if cls, _ := env["class"].(string); cls != "meta.ddl.vertexType" {
			fail(metaKey+" class", fmt.Sprintf("got %q want meta.ddl.vertexType", cls))
		} else {
			ok(metaKey + " class=meta.ddl.vertexType")
		}
		if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
			fail(metaKey+" isDeleted", "vertex is tombstoned")
		} else {
			ok(metaKey + " isDeleted=false")
		}
		data, _ := env["data"].(map[string]any)
		gotAbstract, _ := data["abstract"].(bool)
		if gotAbstract != wantAbstract {
			fail(metaKey+" data.abstract", fmt.Sprintf("%s: got %v want %v", canonical, gotAbstract, wantAbstract))
		} else {
			ok(fmt.Sprintf("%s data.abstract=%v", metaKey, wantAbstract))
		}
	}
	checkCanonicalName := func(metaKey, canonical string) {
		cnKey := metaKey + ".canonicalName"
		env, err := pkgverify.GetEnvelope(ctx, coreKV, cnKey)
		if err != nil {
			fail(cnKey, fmt.Sprintf("missing: %v", err))
			return
		}
		data, _ := env["data"].(map[string]any)
		if val, _ := data["value"].(string); val != canonical {
			fail(cnKey, fmt.Sprintf("value=%q want %q", val, canonical))
		} else {
			ok(cnKey + " value=" + canonical)
		}
		if err := pkgverify.CheckAspectEnvelope(env, cnKey, metaKey, "canonicalName"); err != nil {
			fail(cnKey+" envelope", err.Error())
		} else {
			ok(cnKey + " envelope shape OK")
		}
	}
	checkAspectsPresent := func(metaKey string, aspects []string) {
		for _, asp := range aspects {
			k := metaKey + "." + asp
			env, err := pkgverify.GetEnvelope(ctx, coreKV, k)
			if err != nil {
				fail(k, fmt.Sprintf("missing: %v", err))
				continue
			}
			if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
				fail(k, "tombstoned — a declared aspect must be live")
				continue
			}
			ok(k + " present")
			if err := pkgverify.CheckAspectEnvelope(env, k, metaKey, asp); err != nil {
				fail(k+" envelope", err.Error())
			} else {
				ok(k + " envelope shape OK")
			}
		}
	}
	// checkAspectsWithdrawn asserts an aspect an ABSTRACT type must not
	// declare. Withdrawn has TWO on-disk shapes and both are correct: the key
	// is absent (a fresh install never wrote it) or present-but-TOMBSTONED (an
	// in-place upgrade of a concrete type to an abstract one — a DDL keeps its
	// meta-vertex NanoID across versions per Contract #8 §8.1, so pkgmgr's
	// diff tombstones the aspects the new version stops declaring, and a
	// tombstone retains the prior document with only isDeleted flipped). The
	// upgrade shape is the one every already-installed cell has, so demanding
	// an absent key would fail this gate on exactly those stacks. What is NOT
	// acceptable is a LIVE aspect.
	checkAspectsWithdrawn := func(metaKey string, aspects []string) {
		for _, asp := range aspects {
			k := metaKey + "." + asp
			env, err := pkgverify.GetEnvelope(ctx, coreKV, k)
			if err != nil {
				ok(k + " absent (abstract type)")
				continue
			}
			if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
				ok(k + " tombstoned (abstract type, upgraded in place)")
				continue
			}
			fail(k, "live, but an abstract type declares neither a script nor a permittedCommands gate")
		}
	}

	// The abstract parent.
	abstractKey := metaKeyOf(locationDDLCanonical)
	if abstractKey != "" {
		checkRoot(abstractKey, locationDDLCanonical, true)
		checkCanonicalName(abstractKey, locationDDLCanonical)
		checkAspectsWithdrawn(abstractKey, []string{"permittedCommands", "script"})
		checkAspectsPresent(abstractKey, []string{"description", "inputSchema", "outputSchema", "fieldDescription", "examples"})
	}

	// The three concrete leaves, each carrying the shared script + the five ops
	// and a live subtypeOf edge to the abstract parent.
	for _, leaf := range locationLeafCanonicals {
		leafKey := metaKeyOf(leaf)
		if leafKey == "" {
			continue
		}
		checkRoot(leafKey, leaf, false)
		checkCanonicalName(leafKey, leaf)
		checkAspectsPresent(leafKey, []string{"description", "script", "inputSchema", "outputSchema", "fieldDescription", "examples"})

		pcKey := leafKey + ".permittedCommands"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := pkgverify.ToStringSlice(data["commands"])
			cmdSet := pkgverify.ToSet(cmds)
			allPresent := true
			for _, op := range locationExpectedOps {
				if !cmdSet[op] {
					fail(pcKey, fmt.Sprintf("missing command %q", op))
					allPresent = false
				}
			}
			if len(cmds) != len(locationExpectedOps) {
				fail(pcKey, fmt.Sprintf("command count=%d want %d", len(cmds), len(locationExpectedOps)))
				allPresent = false
			}
			if allPresent && len(cmds) == len(locationExpectedOps) {
				ok(fmt.Sprintf("%s contains all %d commands", pcKey, len(locationExpectedOps)))
			}
			if err := pkgverify.CheckAspectEnvelope(env, pcKey, leafKey, "permittedCommands"); err != nil {
				fail(pcKey+" envelope", err.Error())
			} else {
				ok(pcKey + " envelope shape OK")
			}
		}

		if abstractKey == "" {
			continue
		}
		// The taxonomy edge, leaf -> abstract (Contract #1 §1.1: the
		// later-arriving vertex is the source).
		linkKey := "lnk.meta." + strings.TrimPrefix(leafKey, "vtx.meta.") +
			".subtypeOf.meta." + strings.TrimPrefix(abstractKey, "vtx.meta.")
		env, err := pkgverify.GetEnvelope(ctx, coreKV, linkKey)
		if err != nil {
			fail(linkKey, fmt.Sprintf("subtypeOf edge missing: %v", err))
			continue
		}
		if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
			fail(linkKey, "subtypeOf edge is tombstoned")
			continue
		}
		if cls, _ := env["class"].(string); cls != "subtypeOf" {
			fail(linkKey, fmt.Sprintf("class=%q want subtypeOf", cls))
			continue
		}
		if src, _ := env["sourceVertex"].(string); src != leafKey {
			fail(linkKey, fmt.Sprintf("sourceVertex=%q want the leaf %q", src, leafKey))
			continue
		}
		if tgt, _ := env["targetVertex"].(string); tgt != abstractKey {
			fail(linkKey, fmt.Sprintf("targetVertex=%q want the abstract %q", tgt, abstractKey))
			continue
		}
		ok(fmt.Sprintf("%s subtypeOf %s", leaf, locationDDLCanonical))
	}

	// 6. permission vertices + scope + grantedBy-operator links.
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
		for _, expected := range locationExpectedOps {
			if opType == expected {
				permIDByOp[opType] = parts[2]
				break
			}
		}
	}

	operatorRoleID := bootstrap.RoleOperatorID
	for _, op := range locationExpectedOps {
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

	// 7. Package manifest.
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, locationPackageName)
	if err != nil || pkgKey == "" {
		fail("location-domain package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", locationPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if name, _ := data["name"].(string); name != locationPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, locationPackageName))
			} else {
				ok(pkgManifestKey + " name=location-domain")
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
		fmt.Printf("verify-package-location-domain: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-location-domain: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up && make verify-package-location-domain` to reinstall from clean state.\n")
	os.Exit(1)
}
