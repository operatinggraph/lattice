//go:build ignore

// verify-package-clinic-reminders.go — assertion tool for
// `make verify-package-clinic-reminders`.
//
// Connects to a running Lattice NATS instance and checks that the clinic-reminders
// package has been correctly installed (after its deps orchestration-base +
// clinic-domain). Asserts:
//
//	2 DDLs: appointmentReminderOp (vertexType, RecordAppointmentReminder — the op
//	  handler) + appointmentReminder (aspectType, RecordAppointmentReminder — the
//	  step-6 .reminder write gate), each with its self-description aspects.
//	1 permission vertex: RecordAppointmentReminder, scope any, grantedBy operator.
//	1 meta.lens: appointmentReminders (the weaver-target convergence lens).
//	1 meta.weaverTarget: appointmentReminders (the §10.8 playbook).
//	1 package vertex + manifest aspect (name=clinic-reminders).
//
// Run via: go run ./scripts/verify-package-clinic-reminders.go
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
	remPackageName  = "clinic-reminders"
	remCoreKVBucket = "core-kv"
	remOp           = "RecordAppointmentReminder"
)

type ddlCheck struct {
	canonical string
	class     string
	ops       []string
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

	coreKV, err := js.KeyValue(ctx, remCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", remCoreKVBucket, err)
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

	fmt.Printf("verify-package-clinic-reminders: scanning %d Core KV keys...\n", len(allKeys))

	ddlChecks := []ddlCheck{
		{canonical: "appointmentReminderOp", class: "meta.ddl.vertexType", ops: []string{remOp}},
		{canonical: "appointmentReminder", class: "meta.ddl.aspectType", ops: []string{remOp}},
	}

	for _, dc := range ddlChecks {
		ddlKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, dc.canonical)
		if err != nil || ddlKey == "" {
			fail(dc.canonical+" DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", dc.canonical, err))
			continue
		}
		ok(fmt.Sprintf("%s DDL meta-vertex exists: %s", dc.canonical, ddlKey))

		if env, err := pkgverify.GetEnvelope(ctx, coreKV, ddlKey); err != nil {
			fail(ddlKey+" class", fmt.Sprintf("cannot read: %v", err))
		} else {
			if cls, _ := env["class"].(string); cls != dc.class {
				fail(ddlKey+" class", fmt.Sprintf("got %q want %q", cls, dc.class))
			} else {
				ok(ddlKey + " class=" + dc.class)
			}
			if isDeleted, _ := env["isDeleted"].(bool); isDeleted {
				fail(ddlKey+" isDeleted", "vertex is tombstoned")
			} else {
				ok(ddlKey + " isDeleted=false")
			}
		}

		pcKey := ddlKey + ".permittedCommands"
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pcKey); err != nil {
			fail(pcKey, fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			cmds := pkgverify.ToStringSlice(data["commands"])
			cmdSet := pkgverify.ToSet(cmds)
			allPresent := true
			for _, op := range dc.ops {
				if !cmdSet[op] {
					fail(pcKey, fmt.Sprintf("missing command %q", op))
					allPresent = false
				}
			}
			if len(cmds) != len(dc.ops) {
				fail(pcKey, fmt.Sprintf("command count=%d want %d", len(cmds), len(dc.ops)))
				allPresent = false
			}
			if allPresent && len(cmds) == len(dc.ops) {
				ok(fmt.Sprintf("%s contains exactly %v", pcKey, dc.ops))
			}
			if err := pkgverify.CheckAspectEnvelope(env, pcKey, ddlKey, "permittedCommands"); err != nil {
				fail(pcKey+" envelope", err.Error())
			} else {
				ok(pcKey + " envelope shape OK")
			}
		}

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

	// The RecordAppointmentReminder permission vertex + scope + grantedBy-operator.
	operatorRoleID := bootstrap.RoleOperatorID
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
		if opType, _ := data["operationType"].(string); opType == remOp {
			permID = parts[2]
			break
		}
	}
	if permID == "" {
		fail("vtx.permission.*[operationType="+remOp+"]", "not found in Core KV")
	} else {
		permKey := "vtx.permission." + permID
		ok(fmt.Sprintf("%s operationType=%s", permKey, remOp))
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

	// The appointmentReminders meta.lens + meta.weaverTarget (the §10.2↔§10.8
	// binding). The two meta-vertices identify themselves DIFFERENTLY (per
	// internal/pkgmgr/build.go): a meta.lens carries its name in a .canonicalName
	// aspect, while a meta.weaverTarget carries no .canonicalName — its identity is
	// the targetId in its .spec aspect. Classify by the vertex class, then read the
	// identifier from the aspect that class actually writes.
	foundLens, foundTarget := false, false
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.meta.") || strings.Count(key, ".") != 2 {
			continue
		}
		env, err := pkgverify.GetEnvelope(ctx, coreKV, key)
		if err != nil {
			continue
		}
		switch cls, _ := env["class"].(string); cls {
		case "meta.lens":
			nameEnv, err := pkgverify.GetEnvelope(ctx, coreKV, key+".canonicalName")
			if err != nil {
				continue
			}
			nd, _ := nameEnv["data"].(map[string]any)
			if cn, _ := nd["value"].(string); cn != "appointmentReminders" {
				continue
			}
			foundLens = true
			ok("appointmentReminders meta.lens exists: " + key)
		case "meta.weaverTarget":
			specEnv, err := pkgverify.GetEnvelope(ctx, coreKV, key+".spec")
			if err != nil {
				continue
			}
			sd, _ := specEnv["data"].(map[string]any)
			if tid, _ := sd["targetId"].(string); tid != "appointmentReminders" {
				continue
			}
			foundTarget = true
			ok("appointmentReminders meta.weaverTarget exists (targetId in .spec): " + key)
		}
	}
	if !foundLens {
		fail("appointmentReminders meta.lens", "no meta.lens with canonicalName=appointmentReminders found")
	}
	if !foundTarget {
		fail("appointmentReminders meta.weaverTarget", "no meta.weaverTarget with canonicalName=appointmentReminders found")
	}

	// Package manifest.
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, remPackageName)
	if err != nil || pkgKey == "" {
		fail("clinic-reminders package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", remPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if name, _ := data["name"].(string); name != remPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, remPackageName))
			} else {
				ok(pkgManifestKey + " name=clinic-reminders")
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
		fmt.Printf("verify-package-clinic-reminders: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		return
	}
	fmt.Printf("verify-package-clinic-reminders: %d FAILURE(S), %d OK\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Println("  " + f)
	}
	os.Exit(1)
}
