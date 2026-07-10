//go:build ignore

// verify-package-clinic-domain.go — assertion tool for
// `make verify-package-clinic-domain`.
//
// Connects to a running Lattice NATS instance and checks that the clinic-domain
// package has been correctly installed. Asserts:
//
//	3 vertexType DDLs: patient (CreatePatient + TombstonePatient), provider
//	  (CreateProvider + TombstoneProvider + SetProviderProfile + SetProviderHours +
//	  SetProviderTimeOff), appointment (CreateAppointment + RescheduleAppointment +
//	  SetAppointmentStatus + RecordEncounter + TombstoneAppointment), each with its
//	  self-description.
//	9 aspectType DDLs: patientDemographics, providerProfile, appointmentSchedule,
//	  appointmentStatus, providerHours, providerTimeOff, providerSlotClaim,
//	  patientSlotClaim, appointmentEncounter — their step-6 write gates.
//	The retired providerBookingGuard / patientBookingGuard DDLs are asserted ABSENT
//	(clinic-booking-write-path-slot-claims-design.md — write-path slot claims
//	replaced the scalar OCC epoch + hasBooking-link enumeration).
//	13 permission vertices: one per op (scope any, granted to operator), plus
//	  a second CreateAppointment vertex (scope self, granted to consumer —
//	  the real-actor-write-auth-e2e patient-books-their-own-appointment idiom).
//	1 package vertex + manifest aspect (name=clinic-domain).
//
// Run via: go run ./scripts/verify-package-clinic-domain.go
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
	clinicPackageName  = "clinic-domain"
	clinicCoreKVBucket = "core-kv"
)

var clinicExpectedOps = []string{
	"CreatePatient", "TombstonePatient",
	"CreateProvider", "TombstoneProvider", "SetProviderProfile", "SetProviderHours", "SetProviderTimeOff",
	"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "RecordEncounter", "TombstoneAppointment",
}

// permGrant is one expected (scope, grantee-role) pair for an operationType's
// permission vertex. Every op here carries exactly one, except
// CreateAppointment, which carries two distinct permission vertices —
// packages/clinic-domain/permissions.go grants the operator role scope=any
// AND the consumer role scope=self (the real-actor-write-auth-e2e idiom: a
// patient books their own appointment).
type permGrant struct {
	scope   string
	grantee string
}

var clinicOpGrants = map[string][]permGrant{
	"CreatePatient":         {{"any", "operator"}},
	"TombstonePatient":      {{"any", "operator"}},
	"CreateProvider":        {{"any", "operator"}},
	"TombstoneProvider":     {{"any", "operator"}},
	"SetProviderProfile":    {{"any", "operator"}},
	"SetProviderHours":      {{"any", "operator"}},
	"SetProviderTimeOff":    {{"any", "operator"}},
	"CreateAppointment":     {{"any", "operator"}, {"self", "consumer"}},
	"RescheduleAppointment": {{"any", "operator"}},
	"SetAppointmentStatus":  {{"any", "operator"}},
	"RecordEncounter":       {{"any", "operator"}},
	"TombstoneAppointment":  {{"any", "operator"}},
}

// ddlCheck describes one DDL to verify: its canonical name, its expected meta
// class, and the ops its permittedCommands must contain.
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

	coreKV, err := js.KeyValue(ctx, clinicCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", clinicCoreKVBucket, err)
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

	fmt.Printf("verify-package-clinic-domain: scanning %d Core KV keys...\n", len(allKeys))

	ddlChecks := []ddlCheck{
		{canonical: "patient", class: "meta.ddl.vertexType", ops: []string{"CreatePatient", "TombstonePatient"}},
		{canonical: "provider", class: "meta.ddl.vertexType", ops: []string{"CreateProvider", "TombstoneProvider", "SetProviderProfile", "SetProviderHours", "SetProviderTimeOff"}},
		{canonical: "appointment", class: "meta.ddl.vertexType", ops: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "RecordEncounter", "TombstoneAppointment"}},
		{canonical: "patientDemographics", class: "meta.ddl.aspectType", ops: []string{"CreatePatient"}},
		{canonical: "providerProfile", class: "meta.ddl.aspectType", ops: []string{"CreateProvider", "SetProviderProfile"}},
		{canonical: "appointmentSchedule", class: "meta.ddl.aspectType", ops: []string{"CreateAppointment", "RescheduleAppointment"}},
		{canonical: "appointmentStatus", class: "meta.ddl.aspectType", ops: []string{"CreateAppointment", "SetAppointmentStatus"}},
		{canonical: "providerHours", class: "meta.ddl.aspectType", ops: []string{"SetProviderHours"}},
		{canonical: "providerTimeOff", class: "meta.ddl.aspectType", ops: []string{"SetProviderTimeOff"}},
		{canonical: "providerSlotClaim", class: "meta.ddl.aspectType", ops: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "TombstoneAppointment"}},
		{canonical: "patientSlotClaim", class: "meta.ddl.aspectType", ops: []string{"CreateAppointment", "RescheduleAppointment", "SetAppointmentStatus", "TombstoneAppointment"}},
		{canonical: "appointmentEncounter", class: "meta.ddl.aspectType", ops: []string{"RecordEncounter"}},
	}

	// The retired scalar-epoch DDLs must NOT exist — the write-path slot-claim
	// mechanism replaced them entirely (clinic-booking-write-path-slot-claims-design.md).
	retiredCanonicals := []string{"providerBookingGuard", "patientBookingGuard"}

	for _, dc := range ddlChecks {
		ddlKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, dc.canonical)
		if err != nil || ddlKey == "" {
			fail(dc.canonical+" DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", dc.canonical, err))
			continue
		}
		ok(fmt.Sprintf("%s DDL meta-vertex exists: %s", dc.canonical, ddlKey))

		// class + alive.
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

		// permittedCommands.
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

		// remaining self-description aspects.
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

	for _, canonical := range retiredCanonicals {
		ddlKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, canonical)
		if err != nil {
			fail(canonical+" DDL absence", fmt.Sprintf("lookup error: %v", err))
		} else if ddlKey != "" {
			fail(canonical+" DDL absence", fmt.Sprintf("found %s; the write-path slot-claim mechanism must have retired this DDL", ddlKey))
		} else {
			ok(canonical + " DDL absent (retired)")
		}
	}

	// Discover role NanoIDs (operator from bootstrap; others — e.g. consumer,
	// the real-actor-write-auth-e2e grantee — by scanning vtx.role.*.canonicalName).
	operatorRoleID := bootstrap.RoleOperatorID
	roleIDByCanonical := map[string]string{}
	if operatorRoleID != "" {
		roleIDByCanonical["operator"] = operatorRoleID
	}
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.role.") || !strings.HasSuffix(key, ".canonicalName") {
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
		parts := strings.Split(key, ".")
		if len(parts) != 4 {
			continue
		}
		roleIDByCanonical[val] = parts[2]
	}

	// permission vertices + scope + grantedBy-role links. Most ops carry a
	// single permission vertex (any/operator); CreateAppointment carries two
	// (any/operator + self/consumer — packages/clinic-domain/permissions.go,
	// the real-actor-write-auth-e2e idiom). Collect ALL matching vertices per
	// op — a single permIDByOp[op] overwrite would pick whichever vertex Go's
	// unstable map iteration visited last, nondeterministically hiding the
	// other (the bug this replaces: it passed or failed at random per run).
	permIDsByOp := map[string][]string{}
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
		opType, _ := data["operationType"].(string)
		for _, expected := range clinicExpectedOps {
			if opType == expected {
				permIDsByOp[opType] = append(permIDsByOp[opType], parts[2])
				break
			}
		}
	}

	for _, op := range clinicExpectedOps {
		permIDs := permIDsByOp[op]
		if len(permIDs) == 0 {
			fail("vtx.permission.*[operationType="+op+"]", "not found in Core KV")
			continue
		}
		grants := clinicOpGrants[op]
		if len(permIDs) != len(grants) {
			fail(fmt.Sprintf("vtx.permission.*[operationType=%s]", op),
				fmt.Sprintf("found %d permission vertices, want %d (%v)", len(permIDs), len(grants), grants))
			continue
		}
		for _, grant := range grants {
			// Match this grant to the permission vertex among permIDs
			// carrying its scope (each op's grants declare distinct scopes).
			var matchedID string
			for _, permID := range permIDs {
				env, err := pkgverify.GetEnvelope(ctx, coreKV, "vtx.permission."+permID)
				if err != nil {
					continue
				}
				data, _ := env["data"].(map[string]any)
				if scope, _ := data["scope"].(string); scope == grant.scope {
					matchedID = permID
					break
				}
			}
			if matchedID == "" {
				fail(fmt.Sprintf("vtx.permission.*[operationType=%s,scope=%s]", op, grant.scope),
					"not found among discovered permission vertices")
				continue
			}
			permKey := "vtx.permission." + matchedID
			ok(fmt.Sprintf("%s operationType=%s scope=%s", permKey, op, grant.scope))

			granteeRoleID, roleFound := roleIDByCanonical[grant.grantee]
			if !roleFound {
				fail(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<%s>", matchedID, grant.grantee),
					fmt.Sprintf("role %q NanoID not found; cannot verify grant link", grant.grantee))
				continue
			}
			linkKey := "lnk.permission." + matchedID + ".grantedBy.role." + granteeRoleID
			if _, exists := allKeys[linkKey]; !exists {
				fail(linkKey, "grantedBy."+grant.grantee+" link not found")
			} else if lenv, err := pkgverify.GetEnvelope(ctx, coreKV, linkKey); err != nil {
				fail(linkKey, fmt.Sprintf("cannot read: %v", err))
			} else if isDeleted, _ := lenv["isDeleted"].(bool); isDeleted {
				fail(linkKey, "link is tombstoned")
			} else {
				ok(fmt.Sprintf("lnk.permission.%s.grantedBy.role.<%s> exists", matchedID, grant.grantee))
			}
		}
	}

	// Package manifest.
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, clinicPackageName)
	if err != nil || pkgKey == "" {
		fail("clinic-domain package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", clinicPackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if name, _ := data["name"].(string); name != clinicPackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, clinicPackageName))
			} else {
				ok(pkgManifestKey + " name=clinic-domain")
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
		fmt.Printf("verify-package-clinic-domain: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-clinic-domain: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up && make verify-package-clinic-domain` to reinstall from clean state.\n")
	os.Exit(1)
}
