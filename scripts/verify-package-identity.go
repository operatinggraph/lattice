//go:build ignore

// verify-package-identity.go — assertion tool for `make verify-package-identity`.
//
// Connects to a running Lattice NATS instance and checks that the
// identity-domain package has been correctly installed. Asserts:
//
//	1 identity DDL meta-vertex (vtx.meta.<NanoID>) with class=meta.ddl.vertexType
//	8 DDL aspects: .canonicalName=identity, .permittedCommands (8 ops),
//	               .description, .script,
//	               .inputSchema, .outputSchema, .fieldDescription, .examples
//	  Each aspect also validated for correct vertexKey + localName envelope fields.
//	7 sensitive aspect-type DDLs (ssn, dob, name, email, phone, claimKey,
//	  credentialBinding): class=meta.ddl.aspectType, each carrying a .sensitive
//	  aspect with value=true
//	8 permission vertices (vtx.permission.<NanoID>) — CreateUnclaimedIdentity,
//	  UpdateIdentityState, ClaimIdentity, RecordIdentityPII, ProvisionConsumerIdentity,
//	  InitiateCredentialLink, CompleteCredentialLink, UnlinkCredential
//	11 grantedBy link keys:
//	  CreateUnclaimedIdentity   → operator, frontOfHouse, backOfHouse
//	  UpdateIdentityState       → operator
//	  ClaimIdentity             → consumer
//	  RecordIdentityPII         → operator, frontOfHouse, backOfHouse
//	  ProvisionConsumerIdentity → identityProvisioner, operator
//	  InitiateCredentialLink    → consumer
//	  CompleteCredentialLink    → consumer
//	  UnlinkCredential          → consumer
//	4 role vertices (consumer, frontOfHouse, backOfHouse — user-facing;
//	  identityProvisioner — system-only) seeded by PreInstall hook (vtx.role.<NanoID>)
//	1 identityIndexHint Lens meta-vertex (vtx.meta.<NanoID>) with class=meta.lens
//	3 Lens aspects: .canonicalName=identityIndexHint, .spec (contains
//	  identityindex + identityKey), .adapter/.bucket/.engine via the envelope
//	1 package vertex (vtx.package.<NanoID>)
//	1 package manifest aspect with name=identity-domain
//
// Total target: ~86 OK lines.
//
// Exit 0: all assertions pass.
// Exit 1: one or more assertions failed.
//
// Run via: go run ./scripts/verify-package-identity.go
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
	identityPackageName   = "identity-domain"
	identityDDLCanonical  = "identity"
	identityCoreKVBucket  = "core-kv"
	identityLensCanonical = "identityIndexHint"
)

// grantTarget maps operationType → expected grantee canonical names.
var identityGrantTargets = map[string][]string{
	"CreateUnclaimedIdentity":   {"operator", "frontOfHouse", "backOfHouse"},
	"UpdateIdentityState":       {"operator"},
	"ClaimIdentity":             {"consumer"},
	"RecordIdentityPII":         {"operator", "frontOfHouse", "backOfHouse"},
	"ProvisionConsumerIdentity": {"identityProvisioner", "operator"},
	"InitiateCredentialLink":    {"consumer"},
	"CompleteCredentialLink":    {"consumer"},
	"UnlinkCredential":          {"consumer"},
}

var identityExpectedOps = []string{
	"CreateUnclaimedIdentity",
	"UpdateIdentityState",
	"ClaimIdentity",
	"RecordIdentityPII",
	"ProvisionConsumerIdentity",
	"InitiateCredentialLink",
	"CompleteCredentialLink",
	"UnlinkCredential",
}

var identityOpScopes = map[string]string{
	"CreateUnclaimedIdentity":   "any",
	"UpdateIdentityState":       "any",
	"ClaimIdentity":             "self",
	"RecordIdentityPII":         "any",
	"ProvisionConsumerIdentity": "any",
	"InitiateCredentialLink":    "self",
	"CompleteCredentialLink":    "self",
	"UnlinkCredential":          "self",
}

// userFacingRoles are seeded by identity-domain's PreInstall hook.
var identityUserFacingRoles = []string{"consumer", "frontOfHouse", "backOfHouse"}

// identitySystemRoles are declared alongside the user-facing roles but are
// not user-facing (identityProvisioner is granted only to the Gateway's own
// bootstrap identity via a one-time ops action, never to a human).
var identitySystemRoles = []string{"identityProvisioner"}

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

	coreKV, err := js.KeyValue(ctx, identityCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", identityCoreKVBucket, err)
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

	fmt.Printf("verify-package-identity: scanning %d Core KV keys...\n", len(allKeys))

	// -------------------------------------------------------------------------
	// 1. Find the identity DDL meta-vertex.
	// -------------------------------------------------------------------------
	identityDDLKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, identityDDLCanonical)
	if err != nil || identityDDLKey == "" {
		fail("identity DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", identityDDLCanonical, err))
	} else {
		ok(fmt.Sprintf("identity DDL meta-vertex exists: %s", identityDDLKey))
	}

	if identityDDLKey != "" {
		// 2. DDL vertex class == meta.ddl.vertexType.
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, identityDDLKey); err != nil {
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
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, cnKey); err != nil {
			fail(cnKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			val, _ := data["value"].(string)
			if val != identityDDLCanonical {
				fail(cnKey, fmt.Sprintf("value=%q want %q", val, identityDDLCanonical))
			} else {
				ok(cnKey + " value=identity")
			}
			if err := pkgverify.CheckAspectEnvelope(env, cnKey, identityDDLKey, "canonicalName"); err != nil {
				fail(cnKey+" envelope", err.Error())
			} else {
				ok(cnKey + " envelope shape OK")
			}
		}

		// 4. Aspect: .permittedCommands = identityExpectedOps.
		pcKey := identityDDLKey + ".permittedCommands"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := pkgverify.ToStringSlice(data["commands"])
			cmdSet := pkgverify.ToSet(cmds)
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
				ok(fmt.Sprintf("%s contains all %d commands", pcKey, len(identityExpectedOps)))
			}
			if err := pkgverify.CheckAspectEnvelope(env, pcKey, identityDDLKey, "permittedCommands"); err != nil {
				fail(pcKey+" envelope", err.Error())
			} else {
				ok(pcKey + " envelope shape OK")
			}
		}

		// 5. Aspect: .description non-empty.
		descKey := identityDDLKey + ".description"
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
			if err := pkgverify.CheckAspectEnvelope(env, descKey, identityDDLKey, "description"); err != nil {
				fail(descKey+" envelope", err.Error())
			} else {
				ok(descKey + " envelope shape OK")
			}
		}

		// 6. Aspect: .script non-empty.
		scriptKey := identityDDLKey + ".script"
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
			if err := pkgverify.CheckAspectEnvelope(env, scriptKey, identityDDLKey, "script"); err != nil {
				fail(scriptKey+" envelope", err.Error())
			} else {
				ok(scriptKey + " envelope shape OK")
			}
		}

		// 6a. Self-description aspects.
		for _, asp := range []string{"inputSchema", "outputSchema", "fieldDescription", "examples"} {
			k := identityDDLKey + "." + asp
			if env, err := pkgverify.GetEnvelope(ctx, coreKV, k); err != nil {
				fail(k, fmt.Sprintf("missing: %v", err))
			} else {
				ok(k + " present")
				if err := pkgverify.CheckAspectEnvelope(env, k, identityDDLKey, asp); err != nil {
					fail(k+" envelope", err.Error())
				} else {
					ok(k + " envelope shape OK")
				}
			}
		}
	}

	// -------------------------------------------------------------------------
	// 6b. Sensitive identity-PII aspect-type DDLs: each is a meta.ddl.aspectType
	//     meta-vertex carrying a .sensitive aspect with value=true, which is what
	//     makes the Processor's step-6 validator anchor these aspects to identity
	//     vertices (NFR-S3). ssn/dob are written only by RecordIdentityPII; the
	//     other five are written by multiple ops across packages
	//     (CreateUnclaimedIdentity, ClaimIdentity, identity-hygiene's
	//     MergeIdentity), so their DDLs carry empty permittedCommands.
	// -------------------------------------------------------------------------
	for _, aspectType := range []string{"ssn", "dob", "name", "email", "phone", "claimKey", "credentialBinding"} {
		ddlKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, aspectType)
		if err != nil || ddlKey == "" {
			fail("sensitive aspect-type DDL ["+aspectType+"]",
				fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", aspectType, err))
			continue
		}
		ok(fmt.Sprintf("sensitive aspect-type DDL exists: %s canonicalName=%s", ddlKey, aspectType))

		if env, err := pkgverify.GetEnvelope(ctx, coreKV, ddlKey); err != nil {
			fail(ddlKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else if cls, _ := env["class"].(string); cls != "meta.ddl.aspectType" {
			fail(ddlKey+" class", fmt.Sprintf("got %q want meta.ddl.aspectType", cls))
		} else {
			ok(ddlKey + " class=meta.ddl.aspectType")
		}

		sKey := ddlKey + ".sensitive"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, sKey); err != nil {
			fail(sKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			val, _ := data["value"].(bool)
			if !val {
				fail(sKey, "sensitive value is not true")
			} else {
				ok(sKey + " value=true")
			}
			if err := pkgverify.CheckAspectEnvelope(env, sKey, ddlKey, "sensitive"); err != nil {
				fail(sKey+" envelope", err.Error())
			} else {
				ok(sKey + " envelope shape OK")
			}
		}
	}

	// -------------------------------------------------------------------------
	// 7. Discover role NanoIDs (operator from bootstrap; others by scanning
	//    vtx.role.*.canonicalName aspects).
	// -------------------------------------------------------------------------
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
		env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
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
	// 8. Assert 3 user-facing role vertices seeded by PreInstall, plus the
	// identityProvisioner system role.
	// -------------------------------------------------------------------------
	for _, roleName := range append(append([]string{}, identityUserFacingRoles...), identitySystemRoles...) {
		roleID, found := roleIDByCanonical[roleName]
		if !found {
			fail("vtx.role.*[canonicalName="+roleName+"]", "not found in Core KV")
			continue
		}
		roleKey := "vtx.role." + roleID
		env, err := pkgverify.GetEnvelope(ctx, coreKV, roleKey)
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
	// 8a. Find the identityIndexHint Lens meta-vertex (the provision-time
	// probe's P5-clean read seam, multi-credential-identity-linking-
	// design.md §3.4).
	// -------------------------------------------------------------------------
	hintLensKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, identityLensCanonical)
	if err != nil || hintLensKey == "" {
		fail("identityIndexHint Lens meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", identityLensCanonical, err))
	} else {
		ok(fmt.Sprintf("identityIndexHint Lens meta-vertex exists: %s", hintLensKey))
	}

	if hintLensKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, hintLensKey); err != nil {
			fail(hintLensKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			cls, _ := env["class"].(string)
			if cls != "meta.lens" {
				fail(hintLensKey+" class", fmt.Sprintf("got %q want meta.lens", cls))
			} else {
				ok(hintLensKey + " class=meta.lens")
			}
			isDeleted, _ := env["isDeleted"].(bool)
			if isDeleted {
				fail(hintLensKey, "Lens vertex is tombstoned")
			} else {
				ok(hintLensKey + " isDeleted=false")
			}
		}

		specKey := hintLensKey + ".spec"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, specKey); err != nil {
			fail(specKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			src, _ := data["cypherRule"].(string)
			missingTerms := []string{}
			for _, term := range []string{"identityindex", "identityKey"} {
				if !strings.Contains(src, term) {
					missingTerms = append(missingTerms, term)
				}
			}
			if len(missingTerms) > 0 {
				fail(specKey, fmt.Sprintf("spec missing terms: %v", missingTerms))
			} else {
				ok(specKey + " contains identityindex, identityKey")
			}
			if err := pkgverify.CheckAspectEnvelope(env, specKey, hintLensKey, "spec"); err != nil {
				fail(specKey+" envelope", err.Error())
			} else {
				ok(specKey + " envelope shape OK")
			}
		}

		cnKey := hintLensKey + ".canonicalName"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, cnKey); err != nil {
			fail(cnKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			val, _ := data["value"].(string)
			if val != identityLensCanonical {
				fail(cnKey, fmt.Sprintf("value=%q want %q", val, identityLensCanonical))
			} else {
				ok(cnKey + " value=" + identityLensCanonical)
			}
			if err := pkgverify.CheckAspectEnvelope(env, cnKey, hintLensKey, "canonicalName"); err != nil {
				fail(cnKey+" envelope", err.Error())
			} else {
				ok(cnKey + " envelope shape OK")
			}
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
		env, err := pkgverify.GetEnvelope(ctx, coreKV, permKey)
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
				if lenv, err := pkgverify.GetEnvelope(ctx, coreKV, linkKey); err != nil {
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
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, identityPackageName)
	if err != nil || pkgKey == "" {
		fail("identity-domain package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", identityPackageName, err))
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
			if name != identityPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, identityPackageName))
			} else {
				ok(pkgManifestKey + " name=identity-domain")
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
