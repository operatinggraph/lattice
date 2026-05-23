//go:build ignore

// verify-kernel.go — assertion tool for `make verify-kernel`.
//
// Connects to a running Lattice NATS instance and checks that all
// post-Story-5.1 KERNEL Core KV keys exist with correct envelopes per
// Contract #1 §1.3. The kernel set after Story 5.1 (~68 entries):
//
//	 1 bootstrap op tracker
//	 1 admin identity vertex
//	 1 meta-meta-DDL vertex + 8 aspects
//	   (canonicalName/permittedCommands/description/script +
//	    inputSchema/outputSchema/fieldDescription/examples)
//	 2 Lens definitions × 5 aspects each
//	 5 aspect-type meta-vertices × 7 aspects each
//	   (canonicalName + description + inputSchema + outputSchema +
//	    fieldDescription + examples)
//	 1 operator role vertex + canonicalName + description
//	 3 meta-permission vertices
//	 3 grantedBy links (meta-perm → operator)
//	 1 admin → operator holdsRole link
//
// Total ≈ 68 OK lines.
//
// Package gates (verify-package-rbac etc.) cover package-installed
// DDLs / lenses / permissions / grants separately.
//
// Exit 0: all kernel assertions pass.
// Exit 1: one or more assertions failed.
//
// Run via: go run ./scripts/verify-kernel.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
)

func main() {
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := bootstrap.Load(bootstrapJSONPath); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot load primordial IDs from %s: %v\n", bootstrapJSONPath, err)
		fmt.Fprintln(os.Stderr, "Suggestion: ensure `make up` has completed; lattice.bootstrap.json must exist.")
		os.Exit(1)
	}

	nc, err := nats.Connect(natsURL)
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

	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", bootstrap.CoreKVBucket, err)
		os.Exit(1)
	}

	healthKV, err := js.KeyValue(ctx, bootstrap.HealthKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Health KV bucket %q: %v\n", bootstrap.HealthKVBucket, err)
		os.Exit(1)
	}

	var failures []string

	// 1. Top-level kernel keys + envelope sanity.
	primordialKeys := bootstrap.PrimordialVertexKeys()
	fmt.Printf("Checking %d kernel Core KV keys...\n", len(primordialKeys))
	for _, key := range primordialKeys {
		entry, err := coreKV.Get(ctx, key)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING key: %s (%v)", key, err))
			continue
		}
		var env map[string]any
		if err := json.Unmarshal(entry.Value(), &env); err != nil {
			failures = append(failures, fmt.Sprintf("INVALID JSON for key %s: %v", key, err))
			continue
		}
		for _, field := range []string{"key", "class", "isDeleted", "createdAt", "createdBy",
			"createdByOp", "lastModifiedAt", "lastModifiedBy", "lastModifiedByOp", "data"} {
			if _, ok := env[field]; !ok {
				failures = append(failures, fmt.Sprintf("MISSING field %q in envelope for key %s", field, key))
			}
		}
		if echoKey, ok := env["key"].(string); !ok || echoKey != key {
			failures = append(failures, fmt.Sprintf("KEY MISMATCH: envelope.key=%q but expected %q", echoKey, key))
		}
		if isDeleted, ok := env["isDeleted"].(bool); !ok || isDeleted {
			failures = append(failures, fmt.Sprintf("INVALID isDeleted for key %s", key))
		}
		if cb, ok := env["createdBy"].(string); !ok || cb != bootstrap.BootstrapIdentityKey {
			failures = append(failures, fmt.Sprintf("WRONG createdBy for key %s: got %v", key, env["createdBy"]))
		}
		fmt.Printf("  OK  %s\n", key)
	}

	// 2. Meta-meta DDL aspects (8 aspects — 4 structural + 4 self-description).
	for _, aspect := range []string{
		"canonicalName", "permittedCommands", "description", "script",
		"inputSchema", "outputSchema", "fieldDescription", "examples",
	} {
		k := bootstrap.MetaRootKey + "." + aspect
		if _, err := coreKV.Get(ctx, k); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING meta-DDL aspect: %s (%v)", k, err))
		} else {
			fmt.Printf("  OK  %s\n", k)
		}
	}

	// 2a. Story 5.1: five aspect-type meta-vertices, each with 6 aspects.
	aspectTypeKeys := []string{
		bootstrap.AspectTypeDescriptionKey,
		bootstrap.AspectTypeInputSchemaKey,
		bootstrap.AspectTypeOutputSchemaKey,
		bootstrap.AspectTypeFieldDescriptionKey,
		bootstrap.AspectTypeExamplesKey,
	}
	for _, vtxKey := range aspectTypeKeys {
		for _, asp := range []string{"canonicalName", "description", "inputSchema", "outputSchema", "fieldDescription", "examples"} {
			k := vtxKey + "." + asp
			if _, err := coreKV.Get(ctx, k); err != nil {
				failures = append(failures, fmt.Sprintf("MISSING aspect-type meta-vertex aspect: %s (%v)", k, err))
			} else {
				fmt.Printf("  OK  %s\n", k)
			}
		}
	}

	// 3. Operator role aspects (canonicalName + description).
	for _, aspect := range []string{"canonicalName", "description"} {
		k := bootstrap.RoleOperatorKey + "." + aspect
		if _, err := coreKV.Get(ctx, k); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING operator role aspect: %s (%v)", k, err))
		} else {
			fmt.Printf("  OK  %s\n", k)
		}
	}

	// 4. Capability Lens + Capability-Role-Index Lens aspects.
	lensAspects := []string{"canonicalName", "targetBucket", "cypherRule", "outputSchema", "spec"}
	for _, aspect := range lensAspects {
		k := bootstrap.CapabilityLensKey + "." + aspect
		if _, err := coreKV.Get(ctx, k); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING Capability Lens aspect: %s (%v)", k, err))
		} else {
			fmt.Printf("  OK  %s\n", k)
		}
	}
	for _, aspect := range lensAspects {
		k := bootstrap.CapabilityRoleIndexLensKey + "." + aspect
		if _, err := coreKV.Get(ctx, k); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING CapabilityRoleIndex Lens aspect: %s (%v)", k, err))
		} else {
			fmt.Printf("  OK  %s\n", k)
		}
	}

	// 5. Health KV readiness signal.
	if _, err := healthKV.Get(ctx, bootstrap.HealthBootstrapCompleteKey); err != nil {
		failures = append(failures, fmt.Sprintf("MISSING Health KV readiness signal: %s (%v)",
			bootstrap.HealthBootstrapCompleteKey, err))
	} else {
		fmt.Printf("  OK  health.bootstrap.complete\n")
	}

	// 6. Streams + buckets.
	for _, streamName := range []string{bootstrap.CoreOpsStreamName, bootstrap.CoreEventsStreamName} {
		if _, err := js.Stream(ctx, streamName); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING JetStream stream: %s (%v)", streamName, err))
		} else {
			fmt.Printf("  OK  stream: %s\n", streamName)
		}
	}
	for _, bucket := range []string{
		bootstrap.CoreKVBucket, bootstrap.HealthKVBucket, bootstrap.CapabilityKVBucket,
		bootstrap.WeaverStateBucket, bootstrap.WeaverClaimsBucket, bootstrap.RefractorAdjacencyKV,
	} {
		if _, err := js.KeyValue(ctx, bucket); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING KV bucket: %s (%v)", bucket, err))
		} else {
			fmt.Printf("  OK  bucket: %s\n", bucket)
		}
	}

	fmt.Println()
	if len(failures) == 0 {
		fmt.Printf("verify-kernel: ALL ASSERTIONS PASSED\n")
		os.Exit(0)
	}
	fmt.Printf("verify-kernel: %d FAILURE(S)\n\n", len(failures))
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up` to re-bootstrap from clean state.\n")
	os.Exit(1)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
