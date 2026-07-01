//go:build ignore

// verify-package-augur.go — assertion tool for `make verify-package-augur`.
//
// Connects to a running Lattice NATS instance and checks that the augur package
// has been correctly installed (after its dependency orchestration-base). Augur
// is an opt-in installable AI package (NOT primordial) — it co-installs where the
// AI reasoning tier is wanted, matching its non-primordial dependency; Weaver's
// escalation fails closed to GapWithoutPlaybook where augur isn't installed.
//
// Asserts:
//
//	1 DDL: augurproposal (meta.ddl.vertexType) — the externalTask matched pair +
//	  the human-verdict op + the Fire 2b dispatched-flip — with permittedCommands
//	  = exactly {CreateAugurReasoningClaim, RecordProposal, ReviewProposal,
//	  RecordProposalDispatch} and its standard self-description aspects.
//	4 permission vertices: one per op above, each scope=any, grantedBy operator
//	  (Weaver, the bridge service actor, and the human reviewer are all
//	  operator-equivalent — design §3.2 / escalation-dispatch addendum §7).
//	2 meta.lens: augurProposals (the P5 read-model review surface Loupe reads)
//	  and augurDispatchPending (the Fire 2b augurDispatch weaver-target pickup).
//	1 meta.weaverTarget: augurDispatch (targetId), wired to augurDispatchPending.
//	1 package vertex + manifest aspect (name=augur).
//
// Run via: go run ./scripts/verify-package-augur.go
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
	augPackageName    = "augur"
	augCoreKVBucket   = "core-kv"
	augDDLCanonical   = "augurproposal"
	augDispatchTarget = "augurDispatch"
)

// The ops the augurproposal DDL owns — each in its permittedCommands and each a
// permission vertex granted to operator.
var augOps = []string{"CreateAugurReasoningClaim", "RecordProposal", "ReviewProposal", "RecordProposalDispatch"}

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

	coreKV, err := js.KeyValue(ctx, augCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", augCoreKVBucket, err)
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

	fmt.Printf("verify-package-augur: scanning %d Core KV keys...\n", len(allKeys))

	// The augurproposal DDL meta-vertex.
	ddlKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, augDDLCanonical)
	if err != nil || ddlKey == "" {
		fail(augDDLCanonical+" DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", augDDLCanonical, err))
	} else {
		ok(fmt.Sprintf("%s DDL meta-vertex exists: %s", augDDLCanonical, ddlKey))

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

		// permittedCommands = exactly the two ops.
		pcKey := ddlKey + ".permittedCommands"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := pkgverify.ToStringSlice(data["commands"])
			cmdSet := pkgverify.ToSet(cmds)
			allPresent := true
			for _, op := range augOps {
				if !cmdSet[op] {
					fail(pcKey, fmt.Sprintf("missing command %q", op))
					allPresent = false
				}
			}
			if len(cmds) != len(augOps) {
				fail(pcKey, fmt.Sprintf("command count=%d want %d", len(cmds), len(augOps)))
				allPresent = false
			}
			if allPresent && len(cmds) == len(augOps) {
				ok(fmt.Sprintf("%s contains exactly %v", pcKey, augOps))
			}
			if err := pkgverify.CheckAspectEnvelope(env, pcKey, ddlKey, "permittedCommands"); err != nil {
				fail(pcKey+" envelope", err.Error())
			} else {
				ok(pcKey + " envelope shape OK")
			}
		}

		// Standard DDL self-description aspects.
		for _, asp := range []string{"canonicalName", "description", "script", "inputSchema", "outputSchema", "fieldDescription", "examples"} {
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

	// The two permission vertices, each scope=any + grantedBy operator.
	operatorRoleID := bootstrap.RoleOperatorID
	for _, wantOp := range augOps {
		permID := ""
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
			if opType, _ := data["operationType"].(string); opType == wantOp {
				permID = parts[2]
				break
			}
		}
		if permID == "" {
			fail("vtx.permission.*[operationType="+wantOp+"]", "not found in Core KV")
			continue
		}
		permKey := "vtx.permission." + permID
		ok(fmt.Sprintf("%s operationType=%s", permKey, wantOp))
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
			ok(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<operator> exists (%s)", permID, wantOp))
		}
	}

	// The augurProposals read-model meta.lens (the P5 review surface). A
	// meta.lens carries its name in a .canonicalName aspect (per
	// internal/pkgmgr/build.go); classify by class, then read the identifier.
	foundLens := false
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.meta.") || strings.Count(key, ".") != 2 {
			continue
		}
		env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		if cls, _ := env["class"].(string); cls != "meta.lens" {
			continue
		}
		nameEnv, err := pkgverify.GetEnvelope(ctx, coreKV, key+".canonicalName")
		if err != nil {
			continue
		}
		nd, _ := nameEnv["data"].(map[string]any)
		if cn, _ := nd["value"].(string); cn != "augurProposals" {
			continue
		}
		foundLens = true
		ok("augurProposals meta.lens exists: " + key)
	}
	if !foundLens {
		fail("augurProposals meta.lens", "no meta.lens with canonicalName=augurProposals found")
	}

	// The augurDispatchPending weaver-target lens (Fire 2b pickup transport).
	dispLensKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, "augurDispatchPending")
	if err != nil || dispLensKey == "" {
		fail("augurDispatchPending meta.lens", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", "augurDispatchPending", err))
	} else {
		ok("augurDispatchPending meta.lens exists: " + dispLensKey)
	}

	// The augurDispatch meta.weaverTarget (the target the augurDispatchPending
	// lens feeds; its .spec aspect carries targetId + lensRef + the
	// missing_dispatch -> proposedOp gap).
	foundTarget := false
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.meta.") || !strings.HasSuffix(key, ".spec") {
			continue
		}
		env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		data, _ := env["data"].(map[string]any)
		if tid, _ := data["targetId"].(string); tid != augDispatchTarget {
			continue
		}
		foundTarget = true
		ok("augurDispatch meta.weaverTarget spec exists: " + key)
		gaps, _ := data["gaps"].(map[string]any)
		gap, _ := gaps["missing_dispatch"].(map[string]any)
		if action, _ := gap["action"].(string); action != "proposedOp" {
			fail(key+" gaps.missing_dispatch.action", fmt.Sprintf("got %q want proposedOp", action))
		} else {
			ok(key + " gaps.missing_dispatch.action=proposedOp")
		}
	}
	if !foundTarget {
		fail("augurDispatch meta.weaverTarget", "no meta.weaverTarget spec with targetId=augurDispatch found")
	}

	// Package manifest.
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, augPackageName)
	if err != nil || pkgKey == "" {
		fail("augur package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", augPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if name, _ := data["name"].(string); name != augPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, augPackageName))
			} else {
				ok(pkgManifestKey + " name=augur")
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
		fmt.Printf("verify-package-augur: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		return
	}
	fmt.Printf("verify-package-augur: %d FAILURE(S), %d OK\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Println("  " + f)
	}
	os.Exit(1)
}
