//go:build ignore

// verify-package-identity.go — assertion tool for `make verify-package-identity`.
//
// Connects to a running Lattice NATS instance and checks that the
// identity-domain package has been correctly installed. Asserts:
//
//  1 DDL meta-vertex (vtx.meta.<NanoID>) with class=meta.ddl.vertexType
//  8 DDL aspects: .canonicalName=identity, .permittedCommands (3 ops),
//                 .description, .script,
//                 .inputSchema, .outputSchema, .fieldDescription, .examples (Story 5.1)
//  3 permission vertices (vtx.permission.<NanoID>) — CreateUnclaimedIdentity,
//    UpdateIdentityState, ClaimIdentity
//  5 grantedBy link keys:
//    CreateUnclaimedIdentity → operator, frontOfHouse, backOfHouse
//    UpdateIdentityState     → operator
//    ClaimIdentity           → consumer
//  3 user-facing role vertices (consumer, frontOfHouse, backOfHouse)
//    seeded by PreInstall hook (vtx.role.<NanoID>)
//  1 package vertex (vtx.package.<NanoID>)
//  1 package manifest aspect with name=identity-domain
//
// Total target: ~30 OK lines.
//
// Exit 0: all assertions pass.
// Exit 1: one or more assertions failed.
//
// Run via: go run ./scripts/verify-package-identity.go
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

const (
	identityPackageName  = "identity-domain"
	identityDDLCanonical = "identity"
	identityCoreKVBucket = "core-kv"
)

// grantTarget maps operationType → expected grantee canonical names.
var identityGrantTargets = map[string][]string{
	"CreateUnclaimedIdentity": {"operator", "frontOfHouse", "backOfHouse"},
	"UpdateIdentityState":     {"operator"},
	"ClaimIdentity":           {"consumer"},
}

var identityExpectedOps = []string{
	"CreateUnclaimedIdentity",
	"UpdateIdentityState",
	"ClaimIdentity",
}

var identityOpScopes = map[string]string{
	"CreateUnclaimedIdentity": "any",
	"UpdateIdentityState":     "any",
	"ClaimIdentity":           "self",
}

// userFacingRoles are seeded by identity-domain's PreInstall hook.
var identityUserFacingRoles = []string{"consumer", "frontOfHouse", "backOfHouse"}

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

	coreKV, err := js.KeyValue(ctx, identityCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", identityCoreKVBucket, err)
		os.Exit(1)
	}

	allKeys, err := identityListAllKeys(ctx, coreKV)
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

	fmt.Printf("verify-package-identity: scanning %d Core KV keys...\n", len(allKeys))

	// -------------------------------------------------------------------------
	// 1. Find the identity DDL meta-vertex.
	// -------------------------------------------------------------------------
	identityDDLKey, err := identityFindMetaByCanonical(ctx, coreKV, allKeys, identityDDLCanonical)
	if err != nil || identityDDLKey == "" {
		fail("identity DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", identityDDLCanonical, err))
	} else {
		ok(fmt.Sprintf("identity DDL meta-vertex exists: %s", identityDDLKey))
	}

	if identityDDLKey != "" {
		// 2. DDL vertex class == meta.ddl.vertexType.
		if env, err := identityGetEnvelope(ctx, coreKV, identityDDLKey); err != nil {
			fail(identityDDLKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			cls, _ := env["class"].(string)
			if cls != "meta.ddl.vertexType" {
				fail(identityDDLKey+" class", fmt.Sprintf("got %q want meta.ddl.vertexType", cls))
			} else {
				ok(identityDDLKey + " class=meta.ddl.vertexType")
			}
			isDeleted, _ := env["isDeleted"].(bool)
			if isDeleted {
				fail(identityDDLKey+" isDeleted", "vertex is tombstoned")
			} else {
				ok(identityDDLKey + " isDeleted=false")
			}
		}

		// 3. Aspect: .canonicalName = identity.
		cnKey := identityDDLKey + ".canonicalName"
		if env, err := identityGetEnvelope(ctx, coreKV, cnKey); err != nil {
			fail(cnKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			val, _ := data["value"].(string)
			if val != identityDDLCanonical {
				fail(cnKey, fmt.Sprintf("value=%q want %q", val, identityDDLCanonical))
			} else {
				ok(cnKey + " value=identity")
			}
		}

		// 4. Aspect: .permittedCommands = [CreateUnclaimedIdentity, UpdateIdentityState, ClaimIdentity].
		pcKey := identityDDLKey + ".permittedCommands"
		if env, err := identityGetEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := identityToStringSlice(data["commands"])
			cmdSet := identityToSet(cmds)
			allPresent := true
			for _, op := range identityExpectedOps {
				if !cmdSet[op] {
					fail(pcKey, fmt.Sprintf("missing command %q", op))
					allPresent = false
				}
			}
			if len(cmds) != len(identityExpectedOps) {
				fail(pcKey, fmt.Sprintf("command count=%d want %d", len(cmds), len(identityExpectedOps)))
				allPresent = false
			}
			if allPresent && len(cmds) == len(identityExpectedOps) {
				ok(fmt.Sprintf("%s contains all 3 commands", pcKey))
			}
		}

		// 5. Aspect: .description non-empty.
		descKey := identityDDLKey + ".description"
		if env, err := identityGetEnvelope(ctx, coreKV, descKey); err != nil {
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
		scriptKey := identityDDLKey + ".script"
		if env, err := identityGetEnvelope(ctx, coreKV, scriptKey); err != nil {
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
		for _, asp := range []string{"inputSchema", "outputSchema", "fieldDescription", "examples"} {
			k := identityDDLKey + "." + asp
			if _, err := identityGetEnvelope(ctx, coreKV, k); err != nil {
				fail(k, fmt.Sprintf("missing: %v", err))
			} else {
				ok(k + " present")
			}
		}
	}

	// -------------------------------------------------------------------------
	// 7. Discover role NanoIDs (operator from bootstrap; others by scanning
	//    vtx.role.*.canonicalName aspects).
	// -------------------------------------------------------------------------
	// Build canonical-name → roleID map by scanning vtx.role.*.canonicalName.
	roleIDByCanonical := map[string]string{}
	// Seed operator from bootstrap.
	if bootstrap.RoleOperatorID != "" {
		roleIDByCanonical["operator"] = bootstrap.RoleOperatorID
	}
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.role.") {
			continue
		}
		if !strings.HasSuffix(key, ".canonicalName") {
			continue
		}
		env, err := identityGetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		data, _ := env["data"].(map[string]any)
		val, _ := data["value"].(string)
		if val == "" {
			continue
		}
		// Extract NanoID from vtx.role.<NanoID>.canonicalName.
		parts := strings.Split(key, ".")
		if len(parts) != 4 {
			continue
		}
		roleIDByCanonical[val] = parts[2]
	}

	// -------------------------------------------------------------------------
	// 8. Assert 3 user-facing role vertices seeded by PreInstall.
	// -------------------------------------------------------------------------
	for _, roleName := range identityUserFacingRoles {
		roleID, found := roleIDByCanonical[roleName]
		if !found {
			fail("vtx.role.*[canonicalName="+roleName+"]", "not found in Core KV")
			continue
		}
		roleKey := "vtx.role." + roleID
		env, err := identityGetEnvelope(ctx, coreKV, roleKey)
		if err != nil {
			fail(roleKey, fmt.Sprintf("cannot read: %v", err))
			continue
		}
		isDeleted, _ := env["isDeleted"].(bool)
		if isDeleted {
			fail(roleKey, "role is tombstoned")
		} else {
			ok(fmt.Sprintf("vtx.role.%s canonicalName=%s exists", roleID, roleName))
		}
	}

	// -------------------------------------------------------------------------
	// 9. Find permission vertices + verify scope + grant links.
	// -------------------------------------------------------------------------
	permIDByOp := map[string]string{}
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.permission.") {
			continue
		}
		parts := strings.Split(key, ".")
		if len(parts) != 3 {
			continue
		}
		env, err := identityGetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		isDeleted, _ := env["isDeleted"].(bool)
		if isDeleted {
			continue
		}
		data, _ := env["data"].(map[string]any)
		opType, _ := data["operationType"].(string)
		for _, expected := range identityExpectedOps {
			if opType == expected {
				permIDByOp[opType] = parts[2]
				break
			}
		}
	}

	for _, op := range identityExpectedOps {
		permID, found := permIDByOp[op]
		if !found {
			fail("vtx.permission.*[operationType="+op+"]", "not found in Core KV")
			continue
		}
		permKey := "vtx.permission." + permID
		ok(fmt.Sprintf("%s operationType=%s", permKey, op))

		// Verify scope.
		wantScope := identityOpScopes[op]
		env, err := identityGetEnvelope(ctx, coreKV, permKey)
		if err == nil {
			data, _ := env["data"].(map[string]any)
			scope, _ := data["scope"].(string)
			if scope != wantScope {
				fail(permKey+" scope", fmt.Sprintf("got %q want %q", scope, wantScope))
			} else {
				ok(fmt.Sprintf("%s scope=%s", permKey, wantScope))
			}
		}

		// Verify grantedBy links for each expected grantee.
		grantees := identityGrantTargets[op]
		for _, granteeName := range grantees {
			granteeRoleID, roleFound := roleIDByCanonical[granteeName]
			if !roleFound {
				fail(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<"+granteeName+">", permID),
					fmt.Sprintf("role %q NanoID not found; cannot verify grant link", granteeName))
				continue
			}
			linkKey := "lnk.permission." + permID + ".grantedBy.role." + granteeRoleID
			if _, exists := allKeys[linkKey]; !exists {
				fail(linkKey, "grantedBy link not found")
			} else {
				if lenv, err := identityGetEnvelope(ctx, coreKV, linkKey); err != nil {
					fail(linkKey, fmt.Sprintf("cannot read: %v", err))
				} else {
					isDeleted, _ := lenv["isDeleted"].(bool)
					if isDeleted {
						fail(linkKey, "link is tombstoned")
					} else {
						ok(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<%s> exists", permID, granteeName))
					}
				}
			}
		}
	}

	// -------------------------------------------------------------------------
	// 10. Package manifest.
	// -------------------------------------------------------------------------
	pkgKey, pkgManifestKey, err := identityFindPackageManifest(ctx, coreKV, allKeys, identityPackageName)
	if err != nil || pkgKey == "" {
		fail("identity-domain package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", identityPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := identityGetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			name, _ := data["name"].(string)
			if name != identityPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, identityPackageName))
			} else {
				ok(pkgManifestKey + " name=identity-domain")
			}
		}
	}

	// -------------------------------------------------------------------------
	// Final report.
	// -------------------------------------------------------------------------
	fmt.Println()
	if len(failures) == 0 {
		fmt.Printf("verify-package-identity: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-identity: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up && make verify-package-rbac && make verify-package-identity` to reinstall from clean state.\n")
	os.Exit(1)
}

func identityListAllKeys(ctx context.Context, kv jetstream.KeyValue) (map[string]struct{}, error) {
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

func identityGetEnvelope(ctx context.Context, kv jetstream.KeyValue, key string) (map[string]any, error) {
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

func identityFindMetaByCanonical(ctx context.Context, kv jetstream.KeyValue, allKeys map[string]struct{}, wantCanonical string) (string, error) {
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.meta.") {
			continue
		}
		if !strings.HasSuffix(key, ".canonicalName") {
			continue
		}
		env, err := identityGetEnvelope(ctx, kv, key)
		if err != nil {
			continue
		}
		data, _ := env["data"].(map[string]any)
		val, _ := data["value"].(string)
		if val == wantCanonical {
			return strings.TrimSuffix(key, ".canonicalName"), nil
		}
	}
	return "", nil
}

func identityFindPackageManifest(ctx context.Context, kv jetstream.KeyValue, allKeys map[string]struct{}, pkgName string) (string, string, error) {
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.package.") {
			continue
		}
		if !strings.HasSuffix(key, ".manifest") {
			continue
		}
		env, err := identityGetEnvelope(ctx, kv, key)
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

func identityToStringSlice(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func identityToSet(ss []string) map[string]bool {
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
