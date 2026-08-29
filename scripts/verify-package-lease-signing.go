//go:build ignore

// verify-package-lease-signing.go — assertion tool for
// `make verify-package-lease-signing`.
//
// Connects to a running Lattice NATS instance and checks that the lease-signing
// package has been correctly installed. Asserts:
//
//	14 DDLs: leaseapp (vertexType — CreateLeaseApplication/SignLease/
//	  WithdrawLeaseApplication/DecideLeaseApplication/SetApplicantProfile);
//	  applicantProfile / underwritingParties / applicationSignals (aspectType —
//	  the three-way split SetApplicantProfile writes, up to one batch);
//	  decidedProfileSnapshot (aspectType — the fair-housing preservation record
//	  DecideLeaseApplication CREATE-ONLY-stamps on the FIRST decision of either
//	  value); the externalTask wrapper triad
//	  leaseServiceInstance/leaseServiceReply/leaseServiceDispatch (vertexType) +
//	  leaseServiceOutcome/leaseServiceDispatchMarker (aspectType); the docGen
//	  triad leaseDocInstance/leaseDocReply (vertexType) + leaseDocOutcome
//	  (aspectType); renewal (vertexType — OpenRenewal/SetRenewalTerms/
//	  VerifyGuarantor/SignRenewal/CancelRenewal).
//	The underwritingRecord retention class + the custody chain it confers on
//	.profile, .underwritingParties, and .decidedProfileSnapshot: the holder
//	vertex exists, its .retentionPolicy names the class + policy, and all
//	three sensitive aspect DDLs' .sensitive is true with .custody naming that
//	holder key — the same shape as clinic-domain's clinicalRecord chain
//	(retention-class-key-custody-design.md Fire 2 item 2). applicationSignals
//	is asserted the OPPOSITE: no .sensitive, no .custody — the split's whole
//	point (the three shipped lenses read it directly, unencrypted).
//	1 package vertex + manifest aspect (name=lease-signing).
//
// Run via: go run ./scripts/verify-package-lease-signing.go
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/scripts/pkgverify"
)

const (
	leasePackageName  = "lease-signing"
	leaseCoreKVBucket = "core-kv"
)

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

	coreKV, err := js.KeyValue(ctx, leaseCoreKVBucket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot open Core KV bucket %q: %v\n", leaseCoreKVBucket, err)
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

	fmt.Printf("verify-package-lease-signing: scanning %d Core KV keys...\n", len(allKeys))

	ddlChecks := []ddlCheck{
		{canonical: "leaseapp", class: "meta.ddl.vertexType", ops: []string{"CreateLeaseApplication", "SignLease", "WithdrawLeaseApplication", "DecideLeaseApplication", "SetApplicantProfile"}},
		{canonical: "applicantProfile", class: "meta.ddl.aspectType", ops: []string{"SetApplicantProfile"}},
		{canonical: "underwritingParties", class: "meta.ddl.aspectType", ops: []string{"SetApplicantProfile"}},
		{canonical: "applicationSignals", class: "meta.ddl.aspectType", ops: []string{"SetApplicantProfile"}},
		{canonical: "decidedProfileSnapshot", class: "meta.ddl.aspectType", ops: []string{"DecideLeaseApplication"}},
		{canonical: "leaseServiceInstance", class: "meta.ddl.vertexType", ops: []string{"CreateLeaseServiceInstance"}},
		{canonical: "leaseServiceReply", class: "meta.ddl.vertexType", ops: []string{"RecordLeaseServiceOutcome"}},
		{canonical: "leaseServiceDispatch", class: "meta.ddl.vertexType", ops: []string{"RecordServiceDispatch"}},
		{canonical: "leaseServiceOutcome", class: "meta.ddl.aspectType", ops: []string{"RecordLeaseServiceOutcome"}},
		{canonical: "leaseServiceDispatchMarker", class: "meta.ddl.aspectType", ops: []string{"RecordServiceDispatch"}},
		{canonical: "leaseDocInstance", class: "meta.ddl.vertexType", ops: []string{"CreateLeaseDocInstance"}},
		{canonical: "leaseDocReply", class: "meta.ddl.vertexType", ops: []string{"RecordLeaseDocOutcome"}},
		{canonical: "leaseDocOutcome", class: "meta.ddl.aspectType", ops: []string{"RecordLeaseDocOutcome"}},
		{canonical: "renewal", class: "meta.ddl.vertexType", ops: []string{"OpenRenewal", "SetRenewalTerms", "VerifyGuarantor", "SignRenewal", "CancelRenewal"}},
	}

	ddlKeyByCanonical := map[string]string{}
	for _, dc := range ddlChecks {
		ddlKey, err := pkgverify.FindMetaByCanonical(ctx, coreKV, allKeys, dc.canonical)
		if err != nil || ddlKey == "" {
			fail(dc.canonical+" DDL meta-vertex", fmt.Sprintf("vtx.meta.*.canonicalName=%q not found: %v", dc.canonical, err))
			continue
		}
		ddlKeyByCanonical[dc.canonical] = ddlKey
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
	}

	// The underwritingRecord retention class + the custody it confers on the
	// applicant's raw financials (.profile), the guarantor/co-applicant's own
	// identifiers (.underwritingParties), and the fair-housing preservation
	// record (.decidedProfileSnapshot). A diff-apply that installs the DDLs
	// but drops either the holder vertex or a .custody aspect leaves that DDL
	// marked Sensitive with no resolvable holder, which the Processor refuses
	// at commit — the package would install clean and every
	// SetApplicantProfile / DecideLeaseApplication would fail. Assert the
	// whole chain for ALL THREE sensitive aspects, plus that
	// applicationSignals carries neither.
	holderKey := pkgmgr.RetentionClassKey("lease-signing", "underwritingRecord")
	if env, err := pkgverify.GetEnvelope(ctx, coreKV, holderKey); err != nil {
		fail(holderKey, fmt.Sprintf("underwritingRecord retention-class holder missing: %v", err))
	} else if cls, _ := env["class"].(string); cls != pkgmgr.RetentionClassVertexType {
		fail(holderKey+" class", fmt.Sprintf("got %q want %q", cls, pkgmgr.RetentionClassVertexType))
	} else {
		ok("underwritingRecord retention-class holder exists: " + holderKey)
	}
	policyKey := holderKey + ".retentionPolicy"
	if env, err := pkgverify.GetEnvelope(ctx, coreKV, policyKey); err != nil {
		fail(policyKey, fmt.Sprintf("missing: %v", err))
	} else {
		data, _ := env["data"].(map[string]any)
		name, _ := data["canonicalName"].(string)
		policy, _ := data["policy"].(string)
		if name != "underwritingRecord" || policy != pkgmgr.RetentionPolicyEraseOnExpiry {
			fail(policyKey, fmt.Sprintf("canonicalName=%q policy=%q want %q / %q",
				name, policy, "underwritingRecord", pkgmgr.RetentionPolicyEraseOnExpiry))
		} else {
			ok(policyKey + " declares underwritingRecord / " + pkgmgr.RetentionPolicyEraseOnExpiry)
		}
	}

	for _, sensitive := range []string{"applicantProfile", "underwritingParties", "decidedProfileSnapshot"} {
		ddlKey, found := ddlKeyByCanonical[sensitive]
		if !found {
			fail(sensitive+" custody", "DDL not found above; skipping custody assertions")
			continue
		}
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, ddlKey+".sensitive"); err != nil {
			fail(ddlKey+".sensitive", fmt.Sprintf("missing — %s would commit as plaintext: %v", sensitive, err))
		} else {
			data, _ := env["data"].(map[string]any)
			if v, _ := data["value"].(bool); !v {
				fail(ddlKey+".sensitive", fmt.Sprintf("value=false — %s would commit as plaintext", sensitive))
			} else {
				ok(ddlKey + ".sensitive=true")
			}
		}
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, ddlKey+".custody"); err != nil {
			fail(ddlKey+".custody", fmt.Sprintf("missing: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			kind, _ := data["kind"].(string)
			holder, _ := data["holderKey"].(string)
			if kind != pkgmgr.CustodyKindRetentionClass || holder != holderKey {
				fail(ddlKey+".custody", fmt.Sprintf("kind=%q holderKey=%q want %q / %q",
					kind, holder, pkgmgr.CustodyKindRetentionClass, holderKey))
			} else {
				ok(ddlKey + ".custody names the underwritingRecord holder")
			}
		}
	}

	// applicationSignals is the split's non-sensitive half — a regression that
	// flips Sensitive on it would make the three shipped lenses' qualification
	// columns unreadable (they never decrypt).
	if sigKey, found := ddlKeyByCanonical["applicationSignals"]; found {
		if _, exists := allKeys[sigKey+".sensitive"]; exists {
			fail(sigKey+".sensitive", "aspect present — applicationSignals must declare no .sensitive aspect at all")
		} else {
			ok(sigKey + ".sensitive absent (non-sensitive, as declared)")
		}
		if _, exists := allKeys[sigKey+".custody"]; exists {
			fail(sigKey+".custody", "aspect present — applicationSignals must declare no custody")
		} else {
			ok(sigKey + ".custody absent (no custody, as declared)")
		}
	}

	// Package manifest.
	pkgKey, pkgManifestKey, err := pkgverify.FindPackageManifest(ctx, coreKV, allKeys, leasePackageName)
	if err != nil || pkgKey == "" {
		fail("lease-signing package manifest", fmt.Sprintf("vtx.package.*.manifest[name=%q] not found: %v", leasePackageName, err))
	} else {
		ok(fmt.Sprintf("package vertex exists: %s", pkgKey))
		ok(fmt.Sprintf("package manifest exists: %s", pkgManifestKey))
	}
	if pkgManifestKey != "" {
		if env, err := pkgverify.GetEnvelope(ctx, coreKV, pkgManifestKey); err != nil {
			fail(pkgManifestKey+" name", fmt.Sprintf("cannot read: %v", err))
		} else {
			data, _ := env["data"].(map[string]any)
			if name, _ := data["name"].(string); name != leasePackageName {
				fail(pkgManifestKey+" name", fmt.Sprintf("got %q want %q", name, leasePackageName))
			} else {
				ok(pkgManifestKey + " name=lease-signing")
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
		fmt.Printf("verify-package-lease-signing: ALL ASSERTIONS PASSED (%d OK)\n", okCount)
		os.Exit(0)
	}
	fmt.Printf("verify-package-lease-signing: %d FAILURE(S) (%d OK)\n\n", len(failures), okCount)
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up && make verify-package-lease-signing` to reinstall from clean state.\n")
	os.Exit(1)
}
