//go:build ignore

// verify-package-edge-manifest.go — assertion tool for
// `make verify-package-edge-manifest`.
//
// Connects to a running Lattice NATS instance and checks that the
// edge-manifest package has been correctly installed (co-installed with its
// dependency chain). Asserts:
//
//	5 Lens meta-vertices (class=meta.lens), one per canonicalName
//	  (edgeIdentity/edgeServices/edgeCatalog/edgeTasks/edgeInstances), each
//	  with a spec aspect whose targetType is nats_subject, personal=true,
//	  subjectPrefix=lattice.sync.user, stream=SYNC, and a cypherRule
//	  containing its manifest.<ns> literal.
//	1 package vertex (vtx.package.<NanoID>) + 1 manifest aspect
//	  (name=edge-manifest)
//
// This is a structural install check (mirrors verify-package-service-
// location.go), not a live projection e2e — it does not seed an identity or
// assert a row actually lands on lattice.sync.user.<actor>; that is Fire
// 2's in-browser e2e (edge-showcase-app-design.md §7).
//
// Run via: go run ./scripts/verify-package-edge-manifest.go
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
	emPackageName  = "edge-manifest"
	emCoreKVBucket = "core-kv"
)

var emExpectedLenses = map[string]string{
	"edgeIdentity":  "manifest.me",
	"edgeServices":  "manifest.svc",
	"edgeCatalog":   "manifest.op",
	"edgeTasks":     "manifest.task",
	"edgeInstances": "manifest.inst",
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

	coreKV, err := js.KeyValue(ctx, emCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", emCoreKVBucket, err)
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

	fmt.Printf("verify-package-edge-manifest: scanning %d Core KV keys...\n", len(allKeys))

	// 1. Five Lens meta-vertices, each a nats_subject Personal Lens keyed
	// under its own manifest.<ns> namespace.
	for canonical, ns := range emExpectedLenses {
		lensKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, canonical)
		if err != nil || lensKey == "" {
			fail(canonical+" Lens meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", canonical, err))
			continue
		}
		ok(fmt.Sprintf("%s Lens meta-vertex exists: %s", canonical, lensKey))

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
		env, err := pkgverify.GetEnvelope(ctx, coreKV, specKey)
		if err != nil {
			fail(specKey, fmt.Sprintf("missing: %v", err))
			continue
		}
		data, _ := env["data"].(map[string]any)

		if tt, _ := data["targetType"].(string); tt != "nats_subject" {
			fail(specKey+" targetType", fmt.Sprintf("got %q want nats_subject", tt))
		} else {
			ok(specKey + " targetType=nats_subject")
		}

		targetConfig, _ := data["targetConfig"].(map[string]any)
		if targetConfig == nil {
			fail(specKey+" targetConfig", "missing or not an object")
		} else {
			if sp, _ := targetConfig["subjectPrefix"].(string); sp != "lattice.sync.user" {
				fail(specKey+" targetConfig.subjectPrefix", fmt.Sprintf("got %q want lattice.sync.user", sp))
			} else {
				ok(specKey + " targetConfig.subjectPrefix=lattice.sync.user")
			}
			if stream, _ := targetConfig["stream"].(string); stream != "SYNC" {
				fail(specKey+" targetConfig.stream", fmt.Sprintf("got %q want SYNC", stream))
			} else {
				ok(specKey + " targetConfig.stream=SYNC")
			}
			if personal, _ := targetConfig["personal"].(bool); !personal {
				fail(specKey+" targetConfig.personal", "want true")
			} else {
				ok(specKey + " targetConfig.personal=true")
			}
			key := pkgverify.ToStringSlice(targetConfig["key"])
			hasActor := false
			for _, k := range key {
				if k == "__actor" {
					hasActor = true
				}
			}
			if !hasActor {
				fail(specKey+" targetConfig.key", fmt.Sprintf("got %v, missing __actor", key))
			} else {
				ok(specKey + " targetConfig.key contains __actor")
			}
		}

		cypherRule, _ := data["cypherRule"].(string)
		wantLiteral := `"` + ns + `" AS ns`
		if !strings.Contains(cypherRule, wantLiteral) {
			fail(specKey+" cypherRule", fmt.Sprintf("missing %q", wantLiteral))
		} else {
			ok(fmt.Sprintf("%s cypherRule projects %s", specKey, ns))
		}

		if err := pkgverify.CheckAspectEnvelope(env, specKey, lensKey, "spec"); err != nil {
			fail(specKey+" envelope", err.Error())
		} else {
			ok(specKey + " envelope shape OK")
		}
	}

	// 2. Package manifest.
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, emPackageName)
	if err != nil || pkgKey == "" {
		fail("edge-manifest package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", emPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if name, _ := data["name"].(string); name != emPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, emPackageName))
			} else {
				ok(pkgManifestKey + " name=edge-manifest")
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
		fmt.Printf("verify-package-edge-manifest: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-edge-manifest: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up && make verify-package-edge-manifest` to reinstall from clean state.\n")
	os.Exit(1)
}
