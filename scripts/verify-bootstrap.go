//go:build ignore

// verify-bootstrap.go — assertion tool for `make verify-bootstrap`.
//
// Connects to a running Lattice NATS instance and checks that all primordial
// Core KV keys exist with correct envelopes per Contract #1 §1.3.
//
// Exit 0: all assertions pass.
// Exit 1: one or more assertions failed — prints a diff-style report.
//
// Run via: go run ./scripts/verify-bootstrap.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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

	// Load this deployment's primordial IDs from lattice.bootstrap.json
	// (written by cmd/bootstrap on first `make up`). The IDs are
	// runtime-generated per deployment, so we cannot reference compile-time
	// constants — we must read the JSON.
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

	// 1. Assert all primordial Core KV keys exist and have valid envelopes.
	primordialKeys := bootstrap.PrimordialVertexKeys()
	fmt.Printf("Checking %d primordial Core KV keys...\n", len(primordialKeys))

	for _, key := range primordialKeys {
		entry, err := coreKV.Get(ctx, key)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING key: %s (%v)", key, err))
			continue
		}

		// Validate the envelope JSON.
		var env map[string]any
		if err := json.Unmarshal(entry.Value(), &env); err != nil {
			failures = append(failures, fmt.Sprintf("INVALID JSON for key %s: %v", key, err))
			continue
		}

		// Check required envelope fields per Contract #1 §1.3.
		requiredFields := []string{"key", "class", "isDeleted", "createdAt", "createdBy",
			"createdByOp", "lastModifiedAt", "lastModifiedBy", "lastModifiedByOp", "data"}
		for _, field := range requiredFields {
			if _, ok := env[field]; !ok {
				failures = append(failures, fmt.Sprintf("MISSING field %q in envelope for key %s", field, key))
			}
		}

		// Check that the 'key' field echoes the KV key.
		if echoKey, ok := env["key"].(string); !ok || echoKey != key {
			failures = append(failures, fmt.Sprintf("KEY MISMATCH: envelope.key=%q but expected %q", echoKey, key))
		}

		// Check isDeleted is false.
		if isDeleted, ok := env["isDeleted"].(bool); !ok || isDeleted {
			failures = append(failures, fmt.Sprintf("INVALID isDeleted for key %s: expected false", key))
		}

		// Check createdBy points to bootstrap identity.
		if createdBy, ok := env["createdBy"].(string); !ok || createdBy != bootstrap.BootstrapIdentityKey {
			failures = append(failures, fmt.Sprintf("WRONG createdBy for key %s: got %q, want %q",
				key, env["createdBy"], bootstrap.BootstrapIdentityKey))
		}

		// Check createdByOp points to bootstrap op.
		if createdByOp, ok := env["createdByOp"].(string); !ok || createdByOp != bootstrap.BootstrapOpKey {
			failures = append(failures, fmt.Sprintf("WRONG createdByOp for key %s: got %q, want %q",
				key, env["createdByOp"], bootstrap.BootstrapOpKey))
		}

		// Check class is non-empty.
		if class, ok := env["class"].(string); !ok || class == "" {
			failures = append(failures, fmt.Sprintf("MISSING or empty class for key %s", key))
		}

		fmt.Printf("  OK  %s\n", key)
	}

	// 2. Check that the bootstrap op tracker has self-referential provenance
	//    (createdByOp == its own key, per Contract #4 §4.1).
	opEntry, err := coreKV.Get(ctx, bootstrap.BootstrapOpKey)
	if err == nil {
		var env map[string]any
		if jsonErr := json.Unmarshal(opEntry.Value(), &env); jsonErr == nil {
			if cbop, _ := env["createdByOp"].(string); cbop != bootstrap.BootstrapOpKey {
				failures = append(failures, fmt.Sprintf("OP TRACKER self-reference broken: createdByOp=%q, want %q",
					cbop, bootstrap.BootstrapOpKey))
			} else {
				fmt.Printf("  OK  op tracker self-reference\n")
			}
		}
	}

	// 3. Check Capability Lens aspects exist.
	lensAspects := []string{"canonicalName", "targetBucket", "cypherRule", "outputSchema"}
	for _, aspect := range lensAspects {
		aspectKey := bootstrap.CapabilityLensKey + "." + aspect
		_, err := coreKV.Get(ctx, aspectKey)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING Capability Lens aspect: %s (%v)", aspectKey, err))
		} else {
			fmt.Printf("  OK  %s\n", aspectKey)
		}
	}
	for _, aspect := range lensAspects {
		aspectKey := bootstrap.CapabilityRoleIndexLensKey + "." + aspect
		_, err := coreKV.Get(ctx, aspectKey)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING CapabilityRoleIndex Lens aspect: %s (%v)", aspectKey, err))
		} else {
			fmt.Printf("  OK  %s\n", aspectKey)
		}
	}

	// 4. Check Health KV readiness signal.
	_, err = healthKV.Get(ctx, bootstrap.HealthBootstrapCompleteKey)
	if err != nil {
		failures = append(failures, fmt.Sprintf("MISSING Health KV readiness signal: %s (%v)",
			bootstrap.HealthBootstrapCompleteKey, err))
	} else {
		fmt.Printf("  OK  health.bootstrap.complete\n")
	}

	// 5a. Check JetStream streams exist (Story 1.8 adds core-events for
	// the Processor's step-9 event fan-out).
	allStreams := []string{
		bootstrap.CoreOpsStreamName,
		bootstrap.CoreEventsStreamName,
	}
	for _, streamName := range allStreams {
		stream, err := js.Stream(ctx, streamName)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING JetStream stream: %s (%v)", streamName, err))
			continue
		}
		info, err := stream.Info(ctx)
		if err != nil {
			failures = append(failures, fmt.Sprintf("STREAM info failed: %s (%v)", streamName, err))
			continue
		}
		if streamName == bootstrap.CoreEventsStreamName {
			// Assert core-events accepts events.> and has the expected
			// retention shape.
			foundSubject := false
			for _, s := range info.Config.Subjects {
				if s == bootstrap.EventsWildcardSubject {
					foundSubject = true
					break
				}
			}
			if !foundSubject {
				failures = append(failures, fmt.Sprintf("STREAM %s missing subject %s (got %v)",
					streamName, bootstrap.EventsWildcardSubject, info.Config.Subjects))
			}
			if info.Config.MaxAge == 0 {
				failures = append(failures, fmt.Sprintf("STREAM %s has no MaxAge — events should expire (Phase 1 default 7d)", streamName))
			}
		}
		fmt.Printf("  OK  stream: %s\n", streamName)
	}

	// 5. Check KV buckets exist.
	allBuckets := []string{
		bootstrap.CoreKVBucket,
		bootstrap.HealthKVBucket,
		bootstrap.CapabilityKVBucket,
		bootstrap.WeaverStateBucket,
		bootstrap.WeaverClaimsBucket,
	}
	for _, bucket := range allBuckets {
		_, err := js.KeyValue(ctx, bucket)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING KV bucket: %s (%v)", bucket, err))
		} else {
			fmt.Printf("  OK  bucket: %s\n", bucket)
		}
	}

	// Report.
	fmt.Printf("\n")
	if len(failures) == 0 {
		fmt.Printf("verify-bootstrap: ALL ASSERTIONS PASSED ✓\n")
		os.Exit(0)
	}

	fmt.Printf("verify-bootstrap: %d FAILURE(S)\n\n", len(failures))
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nDiff (expected vs actual):\n")
	fmt.Printf("  Expected: all %d primordial keys present with correct Contract #1 envelopes\n", len(primordialKeys))
	fmt.Printf("  Actual:   %d failures found — see list above\n", len(failures))
	fmt.Printf("\nSuggestion: run `make down && make up` to re-bootstrap from clean state.\n")
	os.Exit(1)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// _ is a compile-time check that we import strings (used in some assertions).
var _ = strings.Contains
