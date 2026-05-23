//go:build ignore

// verify-package-rbac.go — assertion tool for `make verify-package-rbac`.
//
// Connects to a running Lattice NATS instance and checks that the
// rbac-domain package has been correctly installed. Asserts:
//
//  1 DDL meta-vertex (vtx.meta.<NanoID>) with class=meta.ddl.vertexType
//  8 DDL aspects: .canonicalName=rbac, .permittedCommands (10 ops), .description, .script,
//                 .inputSchema, .outputSchema, .fieldDescription, .examples (Story 5.1)
// 10 permission vertices (vtx.permission.<NanoID>) — one per op
// 10 grantedBy link keys (each permission → operator role)
//  1 package vertex (vtx.package.<NanoID>)
//  1 package manifest aspect (vtx.package.<NanoID>.manifest) with name=rbac-domain
//
// Total target: ~34 OK lines.
//
// Exit 0: all assertions pass.
// Exit 1: one or more assertions failed.
//
// Run via: go run ./scripts/verify-package-rbac.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
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
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	coreKV, err := js.KeyValue(ctx, coreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", coreKVBucket, err)
		os.Exit(1)
	}

	// Snapshot all keys in core-kv into a map for O(1) lookup.
	allKeys, err := listAllKeys(ctx, coreKV)
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
	rbacDDLKey, err := findMetaVertexByCanonicalName(ctx, coreKV, allKeys, rbacDDLCanonical)
	if err != nil || rbacDDLKey == "" {
		fail("rbac DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", rbacDDLCanonical, err))
	} else {
		ok(fmt.Sprintf("rbac DDL meta-vertex exists: %s", rbacDDLKey))
	}

	if rbacDDLKey != "" {
		// 2. DDL vertex class == meta.ddl.vertexType.
		if env, err := getEnvelope(ctx, coreKV, rbacDDLKey); err != nil {
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

		// 3. Aspect: .canonicalName = rbac.
		cnKey := rbacDDLKey + ".canonicalName"
		if env, err := getEnvelope(ctx, coreKV, cnKey); err != nil {
			fail(cnKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			val, _ := data["value"].(string)
			if val != rbacDDLCanonical {
				fail(cnKey, fmt.Sprintf("canonicalName value=%q want %q", val, rbacDDLCanonical))
			} else {
				ok(cnKey + " value=rbac")
			}
		}

		// 4. Aspect: .permittedCommands contains all 10 ops.
		pcKey := rbacDDLKey + ".permittedCommands"
		if env, err := getEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := toStringSlice(data["commands"])
			cmdSet := toSet(cmds)
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
		}

		// 5. Aspect: .description non-empty.
		descKey := rbacDDLKey + ".description"
		if env, err := getEnvelope(ctx, coreKV, descKey); err != nil {
			fail(descKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			text, _ := data["text"].(string)
			if strings.TrimSpace(text) == "" {
				fail(descKey, "description text is empty")
			} else {
				ok(descKey + " non-empty")
			}
		}

		// 6. Aspect: .script non-empty.
		scriptKey := rbacDDLKey + ".script"
		if env, err := getEnvelope(ctx, coreKV, scriptKey); err != nil {
			fail(scriptKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			src, _ := data["source"].(string)
			if strings.TrimSpace(src) == "" {
				fail(scriptKey, "script source is empty")
			} else {
				ok(scriptKey + " non-empty")
			}
		}

		// 6a. Story 5.1: self-description aspects.
		isKey := rbacDDLKey + ".inputSchema"
		if env, err := getEnvelope(ctx, coreKV, isKey); err != nil {
			fail(isKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if strings.TrimSpace(data["schema"].(string)) == "" {
				fail(isKey, "inputSchema empty")
			} else {
				ok(isKey + " present")
			}
		}
		osKey := rbacDDLKey + ".outputSchema"
		if env, err := getEnvelope(ctx, coreKV, osKey); err != nil {
			fail(osKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if strings.TrimSpace(data["schema"].(string)) == "" {
				fail(osKey, "outputSchema empty")
			} else {
				ok(osKey + " present")
			}
		}
		fdKey := rbacDDLKey + ".fieldDescription"
		if _, err := getEnvelope(ctx, coreKV, fdKey); err != nil {
			fail(fdKey, fmt.Sprintf("missing: %v", err))
		} else {
			ok(fdKey + " present")
		}
		exKey := rbacDDLKey + ".examples"
		if _, err := getEnvelope(ctx, coreKV, exKey); err != nil {
			fail(exKey, fmt.Sprintf("missing: %v", err))
		} else {
			ok(exKey + " present")
		}
	}

	// -------------------------------------------------------------------------
	// 7. Find the package vertex by scanning vtx.package.*.manifest where
	//    data.name=rbac-domain.
	// -------------------------------------------------------------------------
	pkgKey, pkgManifestKey, err := findPackageManifest(ctx, coreKV, allKeys, rbacPackageName)
	if err != nil || pkgKey == "" {
		fail("rbac-domain package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", rbacPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}

	// Verify manifest carries the correct name.
	if pkgManifestKey != "" {
		if env, err := getEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			name, _ := data["name"].(string)
			if name != rbacPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, rbacPackageName))
			} else {
				ok(pkgManifestKey + " name=rbac-domain")
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
		env, err := getEnvelope(ctx, coreKV, key)
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
		env, err := getEnvelope(ctx, coreKV, permKey)
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
				if env, err := getEnvelope(ctx, coreKV, linkKey); err != nil {
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

// listAllKeys returns a set (map[string]struct{}) of all keys in the KV bucket.
func listAllKeys(ctx context.Context, kv jetstream.KeyValue) (map[string]struct{}, error) {
	lister, err := kv.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	defer lister.Stop()
	result := map[string]struct{}{}
	for k := range lister.Keys() {
		result[k] = struct{}{}
	}
	return result, nil
}

// getEnvelope fetches a single key and unmarshals it as a map.
func getEnvelope(ctx context.Context, kv jetstream.KeyValue, key string) (map[string]any, error) {
	entry, err := kv.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	var env map[string]any
	if err := json.Unmarshal(entry.Value(), &env); err != nil {
		return nil, fmt.Errorf("invalid JSON for %s: %w", key, err)
	}
	return env, nil
}

// findMetaVertexByCanonicalName scans vtx.meta.*.canonicalName aspects
// and returns the vertex key (vtx.meta.<NanoID>) whose canonicalName data.value
// matches wantCanonical.
func findMetaVertexByCanonicalName(ctx context.Context, kv jetstream.KeyValue, allKeys map[string]struct{}, wantCanonical string) (string, error) {
	for key := range allKeys {
		// Match vtx.meta.<id>.canonicalName
		if !strings.HasPrefix(key, "vtx.meta.") {
			continue
		}
		if !strings.HasSuffix(key, ".canonicalName") {
			continue
		}
		env, err := getEnvelope(ctx, kv, key)
		if err != nil {
			continue
		}
		data, _ := env["data"].(map[string]any)
		val, _ := data["value"].(string)
		if val == wantCanonical {
			// Strip the .canonicalName suffix.
			return strings.TrimSuffix(key, ".canonicalName"), nil
		}
	}
	return "", nil
}

// findPackageManifest scans vtx.package.*.manifest and returns (pkgVertexKey, manifestKey)
// for the first entry whose data.name matches pkgName.
func findPackageManifest(ctx context.Context, kv jetstream.KeyValue, allKeys map[string]struct{}, pkgName string) (string, string, error) {
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.package.") {
			continue
		}
		if !strings.HasSuffix(key, ".manifest") {
			continue
		}
		env, err := getEnvelope(ctx, kv, key)
		if err != nil {
			continue
		}
		isDeleted, _ := env["isDeleted"].(bool)
		if isDeleted {
			continue
		}
		data, _ := env["data"].(map[string]any)
		name, _ := data["name"].(string)
		if name == pkgName {
			vtxKey := strings.TrimSuffix(key, ".manifest")
			return vtxKey, key, nil
		}
	}
	return "", "", nil
}

// toStringSlice converts an any (expected []any of strings) to []string.
func toStringSlice(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// toSet converts a []string to a map[string]bool for O(1) lookup.
func toSet(ss []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
