//go:build integration

// Package healthkv_test is the Phase 1 Health KV completeness integration test.
//
// It connects to a running Lattice stack (Processor + Refractor + NATS) and
// asserts that every non-event-driven Health KV key documented in
// docs/observability/health-kv-schema.md appears within 30s of the test starting.
//
// Build tags:
//
//	integration — requires a live NATS connection and running Processor + Refractor.
//
// Environment variables:
//
//	NATS_URL — NATS server URL (default: nats://localhost:4222)
//
// Run with:
//
//	go test -tags integration ./internal/healthkv/... -v -timeout 90s
//
// Or via Makefile:
//
//	make test-health-completeness
//
// Keys asserted (non-event-driven, must appear within 30s):
//   - health.processor.<any-instance>         — Processor heartbeat
//   - health.processor.<any-instance>.step3-latency — per-heartbeat latency signal
//   - health.refractor.<any-instance>         — Refractor heartbeat
//   - <any-lens-id>                           — at least one per-lens reporter entry
//   - health.bootstrap.complete               — written at bootstrap (already present)
//
// Keys explicitly excluded from assertion (event-driven, only present when event occurs):
//   - health.processor.<instance>.auth-trace.<requestId>
//   - health.processor.<instance>.malformed-operation.<requestId>
//   - health.processor.<instance>.claim-attempts.<outcome>
//   - health.alerts.security.<alertCode>
//   - health.gates.phase1.gate<N>
package healthkv_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestHealthKV_Phase1Completeness polls Health KV until all non-event-driven
// keys documented in the Phase 1 schema are present, or times out after 30s.
// Failures are collected and reported together rather than stopping on the first.
func TestHealthKV_Phase1Completeness(t *testing.T) {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL:          natsURL,
		Name:         "healthkv-completeness-test",
		NKeySeedFile: os.Getenv("NATS_NKEY"),
		CredsFile:    os.Getenv("NATS_CREDS"),
	})
	if err != nil {
		t.Fatalf("connect to NATS at %s: %v\n"+
			"Ensure 'make up' has completed and Processor + Refractor are running.", natsURL, err)
	}
	defer conn.Close()

	// Poll every 2s, up to 30s total, checking all required keys each iteration.
	// Exit early as soon as all keys are present.
	const pollInterval = 2 * time.Second
	const maxWait = 30 * time.Second

	deadline := time.Now().Add(maxWait)

	for {
		missing, description := checkRequiredKeys(t, ctx, conn)
		if len(missing) == 0 {
			t.Logf("all required Health KV keys present after %v", maxWait-time.Until(deadline))
			return
		}

		if time.Now().After(deadline) {
			// Time is up. Report all missing keys.
			for _, desc := range description {
				t.Error(desc)
			}
			t.Fatalf("health KV completeness check failed after %v: %d key(s) missing",
				maxWait, len(missing))
			return
		}

		t.Logf("waiting for %d key(s): %v", len(missing), missing)
		time.Sleep(pollInterval)
	}
}

// checkRequiredKeys reads all Health KV keys and returns the list of still-missing
// key patterns plus human-readable descriptions for error reporting.
func checkRequiredKeys(t *testing.T, ctx context.Context, conn *substrate.Conn) ([]string, []string) {
	t.Helper()

	allKeys, err := conn.KVListKeys(ctx, bootstrap.HealthKVBucket)
	if err != nil {
		return []string{"(unable to list keys)"}, []string{fmt.Sprintf("KVListKeys: %v", err)}
	}

	// Build a set for fast lookup.
	keySet := make(map[string]struct{}, len(allKeys))
	for _, k := range allKeys {
		keySet[k] = struct{}{}
	}

	// Pattern matchers for each required non-event-driven key.
	type check struct {
		name    string
		present func(keySet map[string]struct{}) bool
		desc    string
	}

	checks := []check{
		{
			name: "health.processor.<instance>",
			present: func(ks map[string]struct{}) bool {
				for k := range ks {
					if strings.HasPrefix(k, "health.processor.") {
						rest := strings.TrimPrefix(k, "health.processor.")
						if !strings.Contains(rest, ".") {
							return true
						}
					}
				}
				return false
			},
			desc: "health.processor.<instance> heartbeat key not found — Processor may not be running or heartbeat interval not elapsed",
		},
		{
			name: "health.processor.<instance>.step3-latency",
			present: func(ks map[string]struct{}) bool {
				for k := range ks {
					if strings.HasPrefix(k, "health.processor.") && strings.HasSuffix(k, ".step3-latency") {
						return true
					}
				}
				return false
			},
			desc: "health.processor.<instance>.step3-latency not found — emitted per heartbeat tick only when CapabilityAuthorizer is attached (AuthModeCapability required)",
		},
		{
			name: "health.refractor.<instance>",
			present: func(ks map[string]struct{}) bool {
				for k := range ks {
					if strings.HasPrefix(k, "health.refractor.") {
						rest := strings.TrimPrefix(k, "health.refractor.")
						if !strings.Contains(rest, ".") {
							return true
						}
					}
				}
				return false
			},
			desc: "health.refractor.<instance> heartbeat key not found — Refractor may not be running or heartbeat interval not elapsed",
		},
		{
			name: "<lensId> (per-lens reporter)",
			present: func(ks map[string]struct{}) bool {
				for k := range ks {
					// Per-lens reporter keys are bare NanoIDs: they do not
					// start with "health." and are not the bootstrap key.
					// Phase 1 assumption: only Refractor writes bare (non-"health."
					// prefixed) keys to this bucket. If the key population expands
					// (e.g. a second writer), this check will need a tighter predicate
					// (e.g. regexp.MustCompile(`^[A-Za-z0-9_-]{21}$`)).
					if !strings.HasPrefix(k, "health.") {
						return true
					}
				}
				return false
			},
			desc: "<lensId> (bare NanoID per-lens reporter key) not found — Refractor must have at least one active lens (capability lens from rbac-domain/identity-domain packages)",
		},
		{
			name: "health.bootstrap.complete",
			present: func(ks map[string]struct{}) bool {
				_, ok := ks[bootstrap.HealthBootstrapCompleteKey]
				return ok
			},
			desc: "health.bootstrap.complete not found — bootstrap binary (cmd/bootstrap) must have completed successfully",
		},
	}

	var missing []string
	var descriptions []string
	for _, c := range checks {
		if !c.present(keySet) {
			missing = append(missing, c.name)
			descriptions = append(descriptions, c.desc)
		}
	}
	return missing, descriptions
}
