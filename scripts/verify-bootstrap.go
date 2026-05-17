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
	// Story 3.2a adds `spec` — the LensSpec JSON body that
	// Refractor's CoreKVSource consumes to activate the lens.
	lensAspects := []string{"canonicalName", "targetBucket", "cypherRule", "outputSchema", "spec"}
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
		bootstrap.RefractorAdjacencyKV,
	}
	for _, bucket := range allBuckets {
		_, err := js.KeyValue(ctx, bucket)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING KV bucket: %s (%v)", bucket, err))
		} else {
			fmt.Printf("  OK  bucket: %s\n", bucket)
		}
	}

	// 6. Story 3.6: Assert DDL meta-vertices exist with correct aspects.
	ddlDefs := bootstrap.RoleMgmtDDLs()
	fmt.Printf("\nChecking %d role-mgmt DDL meta-vertices...\n", len(ddlDefs))
	for _, ddl := range ddlDefs {
		// Vertex itself.
		vtxEntry, err := coreKV.Get(ctx, ddl.Key)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING DDL vertex: %s (%v)", ddl.Key, err))
			continue
		}
		var vtxEnv map[string]any
		if err := json.Unmarshal(vtxEntry.Value(), &vtxEnv); err != nil {
			failures = append(failures, fmt.Sprintf("INVALID JSON for DDL vertex %s: %v", ddl.Key, err))
			continue
		}
		if cls, _ := vtxEnv["class"].(string); cls != ddl.Class {
			failures = append(failures, fmt.Sprintf("DDL vertex %s class=%q, want %q", ddl.Key, cls, ddl.Class))
		}
		fmt.Printf("  OK  %s (class=%s)\n", ddl.Key, ddl.Class)

		// Required aspects: canonicalName, permittedCommands, description, script.
		for _, aspect := range []string{"canonicalName", "permittedCommands", "description", "script"} {
			aspectKey := ddl.Key + "." + aspect
			aspEntry, err := coreKV.Get(ctx, aspectKey)
			if err != nil {
				failures = append(failures, fmt.Sprintf("MISSING DDL aspect: %s (%v)", aspectKey, err))
				continue
			}
			if aspect == "canonicalName" {
				var aspEnv map[string]any
				if err := json.Unmarshal(aspEntry.Value(), &aspEnv); err == nil {
					if data, ok := aspEnv["data"].(map[string]any); ok {
						if val, _ := data["value"].(string); val != ddl.CanonicalName {
							failures = append(failures, fmt.Sprintf("DDL aspect %s canonicalName=%q, want %q",
								aspectKey, val, ddl.CanonicalName))
							continue
						}
					}
				}
			}
			fmt.Printf("  OK  %s\n", aspectKey)
		}
	}

	// 7. Story 3.6: Assert 12 per-op permission vertices for operator grants.
	opPerms := bootstrap.RoleMgmtOperatorPermissions()
	fmt.Printf("\nChecking %d role-mgmt permission vertices...\n", len(opPerms))
	for _, perm := range opPerms {
		permEntry, err := coreKV.Get(ctx, perm.Key)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING permission vertex: %s (%v)", perm.Key, err))
			continue
		}
		var permEnv map[string]any
		if err := json.Unmarshal(permEntry.Value(), &permEnv); err != nil {
			failures = append(failures, fmt.Sprintf("INVALID JSON for permission vertex %s: %v", perm.Key, err))
			continue
		}
		if cls, _ := permEnv["class"].(string); cls != "permission" {
			failures = append(failures, fmt.Sprintf("permission vertex %s class=%q, want permission", perm.Key, cls))
		}
		if data, ok := permEnv["data"].(map[string]any); ok {
			if ot, _ := data["operationType"].(string); ot != perm.OperationType {
				failures = append(failures, fmt.Sprintf("permission vertex %s operationType=%q, want %q",
					perm.Key, ot, perm.OperationType))
			}
			if scope, _ := data["scope"].(string); scope != "any" {
				failures = append(failures, fmt.Sprintf("permission vertex %s scope=%q, want any", perm.Key, scope))
			}
		}
		fmt.Printf("  OK  %s (operationType=%s)\n", perm.Key, perm.OperationType)
	}

	// 8. Story 3.6: Assert 12 grantsPermission links to operator role.
	grantLinks := bootstrap.RoleMgmtGrantLinkKeys()
	fmt.Printf("\nChecking %d role-mgmt grant links to operator role...\n", len(grantLinks))
	for _, linkKey := range grantLinks {
		lnkEntry, err := coreKV.Get(ctx, linkKey)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING grant link: %s (%v)", linkKey, err))
			continue
		}
		var lnkEnv map[string]any
		if err := json.Unmarshal(lnkEntry.Value(), &lnkEnv); err != nil {
			failures = append(failures, fmt.Sprintf("INVALID JSON for grant link %s: %v", linkKey, err))
			continue
		}
		if cls, _ := lnkEnv["class"].(string); cls != "grantsPermission" {
			failures = append(failures, fmt.Sprintf("grant link %s class=%q, want grantsPermission", linkKey, cls))
		}
		// Verify key shape: lnk.permission.<permID>.grantsPermission.role.<operatorID>
		if !strings.HasPrefix(linkKey, "lnk.permission.") || !strings.Contains(linkKey, ".grantsPermission.role.") {
			failures = append(failures, fmt.Sprintf("grant link %s has unexpected key shape", linkKey))
		}
		// Verify it terminates with the operator role ID.
		if !strings.HasSuffix(linkKey, "."+bootstrap.RoleOperatorID) {
			failures = append(failures, fmt.Sprintf("grant link %s does not end with operator role ID", linkKey))
		}
		if isDeleted, _ := lnkEnv["isDeleted"].(bool); isDeleted {
			failures = append(failures, fmt.Sprintf("grant link %s is tombstoned (isDeleted=true)", linkKey))
		}
		fmt.Printf("  OK  %s\n", linkKey)
	}

	// 8a. Story 4.1: Assert identity DDL meta-vertex + 4 aspects.
	idDDL := bootstrap.IdentityDDL()
	fmt.Printf("\nChecking identity DDL meta-vertex (Story 4.1)...\n")
	{
		vtxEntry, err := coreKV.Get(ctx, idDDL.Key)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING identity DDL vertex: %s (%v)", idDDL.Key, err))
		} else {
			var vtxEnv map[string]any
			if err := json.Unmarshal(vtxEntry.Value(), &vtxEnv); err != nil {
				failures = append(failures, fmt.Sprintf("INVALID JSON for identity DDL vertex %s: %v", idDDL.Key, err))
			} else {
				if cls, _ := vtxEnv["class"].(string); cls != idDDL.Class {
					failures = append(failures, fmt.Sprintf("identity DDL vertex %s class=%q, want %q",
						idDDL.Key, cls, idDDL.Class))
				}
				fmt.Printf("  OK  %s (class=%s)\n", idDDL.Key, idDDL.Class)
			}
		}
		// .canonicalName aspect — must have data.value == "identity".
		cnKey := idDDL.Key + ".canonicalName"
		if aspEntry, err := coreKV.Get(ctx, cnKey); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING identity DDL aspect: %s (%v)", cnKey, err))
		} else {
			var aspEnv map[string]any
			_ = json.Unmarshal(aspEntry.Value(), &aspEnv)
			data, _ := aspEnv["data"].(map[string]any)
			if val, _ := data["value"].(string); val != idDDL.CanonicalName {
				failures = append(failures, fmt.Sprintf("identity DDL aspect %s canonicalName=%q, want %q",
					cnKey, val, idDDL.CanonicalName))
			} else {
				fmt.Printf("  OK  %s\n", cnKey)
			}
		}
		// .permittedCommands aspect — must contain all 8 op types.
		pcKey := idDDL.Key + ".permittedCommands"
		if aspEntry, err := coreKV.Get(ctx, pcKey); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING identity DDL aspect: %s (%v)", pcKey, err))
		} else {
			var aspEnv map[string]any
			_ = json.Unmarshal(aspEntry.Value(), &aspEnv)
			data, _ := aspEnv["data"].(map[string]any)
			cmds, _ := data["commands"].([]any)
			got := map[string]bool{}
			for _, c := range cmds {
				if s, ok := c.(string); ok {
					got[s] = true
				}
			}
			for _, want := range idDDL.PermittedCommands {
				if !got[want] {
					failures = append(failures, fmt.Sprintf("identity DDL %s missing permittedCommand: %s",
						pcKey, want))
				}
			}
			if len(failures) == 0 || !strings.Contains(strings.Join(failures, "\n"), pcKey) {
				fmt.Printf("  OK  %s (%d commands)\n", pcKey, len(idDDL.PermittedCommands))
			}
		}
		// .description aspect.
		descKey := idDDL.Key + ".description"
		if _, err := coreKV.Get(ctx, descKey); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING identity DDL aspect: %s (%v)", descKey, err))
		} else {
			fmt.Printf("  OK  %s\n", descKey)
		}
		// .script aspect — must have non-empty source.
		scriptKey := idDDL.Key + ".script"
		if aspEntry, err := coreKV.Get(ctx, scriptKey); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING identity DDL aspect: %s (%v)", scriptKey, err))
		} else {
			var aspEnv map[string]any
			_ = json.Unmarshal(aspEntry.Value(), &aspEnv)
			data, _ := aspEnv["data"].(map[string]any)
			src, _ := data["source"].(string)
			if strings.TrimSpace(src) == "" {
				failures = append(failures, fmt.Sprintf("identity DDL aspect %s has empty source", scriptKey))
			} else {
				fmt.Printf("  OK  %s (script len=%d)\n", scriptKey, len(src))
			}
		}
	}

	// 8b. Story 4.1: Assert 5 identity-domain permission vertices.
	idPerms := bootstrap.IdentityPermissions()
	fmt.Printf("\nChecking %d identity-domain permission vertices...\n", len(idPerms))
	for _, perm := range idPerms {
		permEntry, err := coreKV.Get(ctx, perm.Key)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING identity permission vertex: %s (%v)", perm.Key, err))
			continue
		}
		var permEnv map[string]any
		if err := json.Unmarshal(permEntry.Value(), &permEnv); err != nil {
			failures = append(failures, fmt.Sprintf("INVALID JSON for identity permission %s: %v", perm.Key, err))
			continue
		}
		if cls, _ := permEnv["class"].(string); cls != "permission" {
			failures = append(failures, fmt.Sprintf("identity permission %s class=%q, want permission",
				perm.Key, cls))
		}
		data, _ := permEnv["data"].(map[string]any)
		if ot, _ := data["operationType"].(string); ot != perm.OperationType {
			failures = append(failures, fmt.Sprintf("identity permission %s operationType=%q, want %q",
				perm.Key, ot, perm.OperationType))
		}
		if scope, _ := data["scope"].(string); scope != perm.Scope {
			failures = append(failures, fmt.Sprintf("identity permission %s scope=%q, want %q",
				perm.Key, scope, perm.Scope))
		}
		fmt.Printf("  OK  %s (operationType=%s scope=%s)\n", perm.Key, perm.OperationType, perm.Scope)
	}

	// 8c. Story 4.1: Assert 10 grantsPermission links for identity domain.
	idGrants := bootstrap.IdentityGrantLinkKeys()
	fmt.Printf("\nChecking %d identity-domain grant links...\n", len(idGrants))
	for _, linkKey := range idGrants {
		lnkEntry, err := coreKV.Get(ctx, linkKey)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING identity grant link: %s (%v)", linkKey, err))
			continue
		}
		var lnkEnv map[string]any
		if err := json.Unmarshal(lnkEntry.Value(), &lnkEnv); err != nil {
			failures = append(failures, fmt.Sprintf("INVALID JSON for identity grant link %s: %v", linkKey, err))
			continue
		}
		if cls, _ := lnkEnv["class"].(string); cls != "grantsPermission" {
			failures = append(failures, fmt.Sprintf("identity grant link %s class=%q, want grantsPermission",
				linkKey, cls))
		}
		if !strings.HasPrefix(linkKey, "lnk.permission.") || !strings.Contains(linkKey, ".grantsPermission.role.") {
			failures = append(failures, fmt.Sprintf("identity grant link %s has unexpected key shape", linkKey))
		}
		if isDeleted, _ := lnkEnv["isDeleted"].(bool); isDeleted {
			failures = append(failures, fmt.Sprintf("identity grant link %s is tombstoned", linkKey))
		}
		fmt.Printf("  OK  %s\n", linkKey)
	}

	// 9. Story 3.6: Verify canonical roles still have description aspects (Story 1.3).
	canonicalRoles := bootstrap.CanonicalRoles()
	fmt.Printf("\nChecking %d canonical role description aspects...\n", len(canonicalRoles))
	for _, role := range canonicalRoles {
		descKey := role.Key + ".description"
		if _, err := coreKV.Get(ctx, descKey); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING role description aspect: %s (%v)", descKey, err))
		} else {
			fmt.Printf("  OK  %s\n", descKey)
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
