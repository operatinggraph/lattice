//go:build ignore

// verify-kernel.go — assertion tool for `make verify-kernel`.
//
// Connects to a running Lattice NATS instance and checks that all
// kernel Core KV keys exist with correct envelopes per Contract #1 §1.3.
// The kernel set (~75 entries):
//
//	1 bootstrap op tracker
//	1 admin identity vertex
//	6 internal service-actor identity vertices (Loom + Weaver + Bridge + object-store-manager +
//	  privacy + Gateway, arch §92) — the Gateway is seeded but, unlike the other five, gets no
//	  holdsRole->operator link (narrow-role fork; see the holdsRole count below)
//	1 meta-meta-DDL vertex + 9 aspects
//	  (canonicalName/permittedCommands/description/script +
//	   inputSchema/outputSchema/fieldDescription/examples + compensation)
//	1 Lens definition: capability — the primordial-identity anchor (7 aspects:
//	  the 5 shared + projectionKind + output). The role-by-operation index is
//	  owned by the rbac-domain package (verify-package-rbac), not the kernel.
//	5 aspect-type meta-vertices × 7 aspects each
//	  (canonicalName + description + inputSchema + outputSchema +
//	   fieldDescription + examples)
//	1 operator role vertex + canonicalName + description
//	3 meta-permission vertices
//	3 grantedBy links (meta-perm → operator)
//	1 admin → operator holdsRole link
//	5 service-actor → operator holdsRole links (Loom + Weaver + Bridge + object-store-manager +
//	  privacy — not Gateway)
//
// Total ≈ 76 OK lines.
//
// Package gates (verify-package-rbac etc.) cover package-installed
// DDLs / lenses / permissions / grants separately.
//
// Exit 0: all kernel assertions pass.
// Exit 1: one or more assertions failed.
//
// Run via: go run ./scripts/verify-kernel.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

func main() {
	natsURL := envOrDefault("NATS_URL", nats.DefaultURL)
	bootstrapJSONPath := envOrDefault("BOOTSTRAP_JSON_PATH", "./lattice.bootstrap.json")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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

	// 1. Top-level kernel keys + envelope sanity.
	primordialKeys := bootstrap.PrimordialVertexKeys()
	// The enumerated set and the declared count must agree, so the kernel
	// composition cannot silently re-drift in only one of the two places.
	if len(primordialKeys) != bootstrap.PrimordialVertexKeyCount {
		failures = append(failures, fmt.Sprintf(
			"KERNEL KEY COUNT DRIFT: PrimordialVertexKeys() enumerates %d but PrimordialVertexKeyCount is %d",
			len(primordialKeys), bootstrap.PrimordialVertexKeyCount))
	}
	fmt.Printf("Checking %d kernel Core KV keys...\n", len(primordialKeys))
	for _, key := range primordialKeys {
		entry, err := coreKV.Get(ctx, key)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING key: %s (%v)", key, err))
			continue
		}
		var env map[string]any
		if err := json.Unmarshal(entry.Value(), &env); err != nil {
			failures = append(failures, fmt.Sprintf("INVALID JSON for key %s: %v", key, err))
			continue
		}
		for _, field := range []string{"key", "class", "isDeleted", "createdAt", "createdBy",
			"createdByOp", "lastModifiedAt", "lastModifiedBy", "lastModifiedByOp", "data"} {
			if _, ok := env[field]; !ok {
				failures = append(failures, fmt.Sprintf("MISSING field %q in envelope for key %s", field, key))
			}
		}
		if echoKey, ok := env["key"].(string); !ok || echoKey != key {
			failures = append(failures, fmt.Sprintf("KEY MISMATCH: envelope.key=%q but expected %q", echoKey, key))
		}
		if isDeleted, ok := env["isDeleted"].(bool); !ok || isDeleted {
			failures = append(failures, fmt.Sprintf("INVALID isDeleted for key %s", key))
		}
		if cb, ok := env["createdBy"].(string); !ok || cb != bootstrap.BootstrapIdentityKey {
			failures = append(failures, fmt.Sprintf("WRONG createdBy for key %s: got %v", key, env["createdBy"]))
		}
		fmt.Printf("  OK  %s\n", key)
	}

	// checkAspect validates an aspect envelope: JSON valid, key echo,
	// class matches expected, isDeleted=false, vertexKey matches parent.
	checkAspect := func(k, parentKey, expectedClass string) {
		entry, err := coreKV.Get(ctx, k)
		if err != nil {
			failures = append(failures, fmt.Sprintf("MISSING aspect: %s (%v)", k, err))
			return
		}
		var env map[string]any
		if err := json.Unmarshal(entry.Value(), &env); err != nil {
			failures = append(failures, fmt.Sprintf("INVALID JSON for aspect %s: %v", k, err))
			return
		}
		var aspFailures []string
		if echoKey, ok := env["key"].(string); !ok || echoKey != k {
			aspFailures = append(aspFailures, fmt.Sprintf("key echo: got %q", env["key"]))
		}
		if cls, ok := env["class"].(string); !ok || cls != expectedClass {
			aspFailures = append(aspFailures, fmt.Sprintf("class: got %q want %q", env["class"], expectedClass))
		}
		if isDeleted, ok := env["isDeleted"].(bool); !ok || isDeleted {
			aspFailures = append(aspFailures, "isDeleted is true or missing")
		}
		if vk, ok := env["vertexKey"].(string); !ok || vk != parentKey {
			aspFailures = append(aspFailures, fmt.Sprintf("vertexKey: got %q want %q", env["vertexKey"], parentKey))
		}
		if len(aspFailures) > 0 {
			for _, f := range aspFailures {
				failures = append(failures, fmt.Sprintf("ASPECT INVALID %s: %s", k, f))
			}
		} else {
			fmt.Printf("  OK  %s\n", k)
		}
	}

	// 2. Meta-meta DDL aspects (9 aspects — 4 structural + 4 self-description + 1 compensation).
	metaDDLAspects := []struct{ name, class string }{
		{"canonicalName", "canonicalName"},
		{"permittedCommands", "permittedCommands"},
		{"description", "description"},
		{"script", "script"},
		{"inputSchema", "inputSchema"},
		{"outputSchema", "outputSchema"},
		{"fieldDescription", "fieldDescription"},
		{"examples", "examples"},
		{"compensation", "compensation"},
	}
	for _, a := range metaDDLAspects {
		checkAspect(bootstrap.MetaRootKey+"."+a.name, bootstrap.MetaRootKey, a.class)
	}

	// 2b. Verify .compensation aspect data.inverseOperationType.
	{
		compKey := bootstrap.MetaRootKey + ".compensation"
		entry, err := coreKV.Get(ctx, compKey)
		if err != nil {
			failures = append(failures, fmt.Sprintf("CANNOT read compensation aspect: %s (%v)", compKey, err))
		} else {
			var compDoc struct {
				Data struct {
					InverseOperationType string `json:"inverseOperationType"`
				} `json:"data"`
			}
			if jsonErr := json.Unmarshal(entry.Value(), &compDoc); jsonErr != nil {
				failures = append(failures, fmt.Sprintf("INVALID JSON for compensation aspect %s: %v", compKey, jsonErr))
			} else if compDoc.Data.InverseOperationType != "TombstoneMetaVertex" {
				failures = append(failures, fmt.Sprintf(
					"WRONG compensation.data.inverseOperationType: got %q want %q",
					compDoc.Data.InverseOperationType, "TombstoneMetaVertex"))
			} else {
				fmt.Printf("  OK  %s.data.inverseOperationType=%q\n", compKey, compDoc.Data.InverseOperationType)
			}
		}
	}

	// 2a. Five aspect-type meta-vertices, each with 6 aspects.
	aspectTypeKeys := []struct{ key string }{
		{bootstrap.AspectTypeDescriptionKey},
		{bootstrap.AspectTypeInputSchemaKey},
		{bootstrap.AspectTypeOutputSchemaKey},
		{bootstrap.AspectTypeFieldDescriptionKey},
		{bootstrap.AspectTypeExamplesKey},
	}
	aspectTypeAspects := []struct{ name, class string }{
		{"canonicalName", "canonicalName"},
		{"description", "description"},
		{"inputSchema", "inputSchema"},
		{"outputSchema", "outputSchema"},
		{"fieldDescription", "fieldDescription"},
		{"examples", "examples"},
	}
	for _, vtx := range aspectTypeKeys {
		for _, a := range aspectTypeAspects {
			checkAspect(vtx.key+"."+a.name, vtx.key, a.class)
		}
	}

	// 3. Operator role aspects (canonicalName + description).
	for _, a := range []struct{ name, class string }{
		{"canonicalName", "canonicalName"},
		{"description", "description"},
	} {
		checkAspect(bootstrap.RoleOperatorKey+"."+a.name, bootstrap.RoleOperatorKey, a.class)
	}

	// 4. Capability Lens aspects (the primordial-identity anchor). The
	// role-by-operation index is owned by the rbac-domain package and is
	// verified by verify-package-rbac, not the kernel.
	lensAspects := []struct{ name, class string }{
		{"canonicalName", "canonicalName"},
		{"targetBucket", "targetBucket"},
		{"cypherRule", "cypherRule"},
		{"outputSchema", "outputSchema"},
		{"spec", "lensSpec"},
	}
	for _, a := range lensAspects {
		checkAspect(bootstrap.CapabilityLensKey+"."+a.name, bootstrap.CapabilityLensKey, a.class)
	}
	// The actor-aggregate capability lens additionally carries the projectionKind
	// marker and the §6.13 Output descriptor that drive its data-driven projection
	// path.
	for _, a := range []struct{ name, class string }{
		{"projectionKind", "projectionKind"},
		{"output", "output"},
	} {
		checkAspect(bootstrap.CapabilityLensKey+"."+a.name, bootstrap.CapabilityLensKey, a.class)
	}

	// 5. Health KV readiness signal.
	if _, err := healthKV.Get(ctx, bootstrap.HealthBootstrapCompleteKey); err != nil {
		failures = append(failures, fmt.Sprintf("MISSING Health KV readiness signal: %s (%v)",
			bootstrap.HealthBootstrapCompleteKey, err))
	} else {
		fmt.Printf("  OK  health.bootstrap.complete\n")
	}

	// 6. Streams + buckets.
	for _, streamName := range []string{bootstrap.CoreOpsStreamName, bootstrap.CoreEventsStreamName} {
		if _, err := js.Stream(ctx, streamName); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING JetStream stream: %s (%v)", streamName, err))
		} else {
			fmt.Printf("  OK  stream: %s\n", streamName)
		}
	}
	for _, b := range bootstrap.PlatformBuckets() {
		if _, err := js.KeyValue(ctx, b.Name); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING KV bucket: %s (%v)", b.Name, err))
		} else {
			fmt.Printf("  OK  bucket: %s\n", b.Name)
		}
	}

	// core-objects Object Store (the off-graph blob plane) — a JetStream Object
	// Store, not a KV bucket, so it is asserted separately.
	if _, err := js.ObjectStore(ctx, bootstrap.CoreObjectsBucket); err != nil {
		failures = append(failures, fmt.Sprintf("MISSING Object Store: %s (%v)", bootstrap.CoreObjectsBucket, err))
	} else {
		fmt.Printf("  OK  object store: %s\n", bootstrap.CoreObjectsBucket)
	}

	// AllowAtomicPublish must be set on the buckets whose writers use atomic
	// batches: Core KV (Processor commit) and loom-state (Loom step transition,
	// Contract #10 §10.3). Without it, Conn.AtomicBatch on the bucket is rejected.
	//
	// loom-state carries three more posture flags of the same class: every Loom
	// removal is a TTL'd purge — a subject rollup carrying a per-message TTL —
	// so a bucket that forbids rollups, forbids purges, or ignores message TTLs
	// fails every step transition. Asserting the posture here makes such a
	// bucket fail verification rather than fail Loom at runtime.
	//
	// loom-state's absent stream age limit is asserted for a different reason:
	// it is a soundness premise, not a mechanism the writers need. The server
	// writes a byte-identical MaxAge marker whether a per-message TTL or a
	// stream age limit emptied the subject, so bounding the bucket by age would
	// silently turn every standing removal marker into something the deadline
	// probe reads as an expiry.
	for _, bucket := range []string{bootstrap.CoreKVBucket, bootstrap.LoomStateBucket} {
		stream, err := js.Stream(ctx, "KV_"+bucket)
		if err != nil {
			failures = append(failures, fmt.Sprintf("CANNOT read stream KV_%s for write-posture checks: %v", bucket, err))
			continue
		}
		cfg := stream.CachedInfo().Config
		if !cfg.AllowAtomicPublish {
			failures = append(failures, fmt.Sprintf("AllowAtomicPublish NOT set on KV_%s (Conn.AtomicBatch would be rejected)", bucket))
		} else {
			fmt.Printf("  OK  AllowAtomicPublish: KV_%s\n", bucket)
		}
		if bucket != bootstrap.LoomStateBucket {
			continue
		}
		if !cfg.AllowRollup {
			failures = append(failures, fmt.Sprintf("AllowRollup NOT set on KV_%s (every Loom removal is a subject rollup)", bucket))
		}
		if cfg.DenyPurge {
			failures = append(failures, fmt.Sprintf("DenyPurge IS set on KV_%s (every Loom removal is a purge)", bucket))
		}
		if !cfg.AllowMsgTTL {
			failures = append(failures, fmt.Sprintf("AllowMsgTTL NOT set on KV_%s (a Loom removal's marker carries a TTL)", bucket))
		}
		if cfg.AllowRollup && !cfg.DenyPurge && cfg.AllowMsgTTL {
			fmt.Printf("  OK  removal posture (AllowRollup, purge allowed, AllowMsgTTL): KV_%s\n", bucket)
		}
		if cfg.MaxAge != 0 {
			failures = append(failures, fmt.Sprintf(
				"MaxAge IS set on KV_%s (%s): the deadline probe keys on the server's MaxAge marker; an age limit would mint one for every removal marker",
				bucket, cfg.MaxAge))
		} else {
			fmt.Printf("  OK  no stream age limit (the deadline probe's MaxAge marker means an expiry): KV_%s\n", bucket)
		}
	}

	// Kernel freshness + orphans, from a SINGLE plan (bootstrap.ReadKernelReport
	// — one call rather than KernelDrift then KernelOrphans separately, which
	// would walk the whole vtx.meta.* population twice). Every check above
	// asserts that a key is present and well-formed, which a bucket seeded by
	// an older binary satisfies while running superseded DDL scripts. This
	// compares stored content against what this binary builds, so a kernel
	// that boot would reconcile is reported rather than passing silently.
	fmt.Println("Checking kernel content matches this binary...")
	report, reportErr := bootstrap.ReadKernelReport(ctx, coreKV)
	switch {
	case reportErr != nil:
		failures = append(failures, fmt.Sprintf("CANNOT compare kernel content: %v", reportErr))
	case len(report.Missing) == 0 && len(report.Stale) == 0:
		fmt.Printf("  OK  kernel content matches this binary\n")
	default:
		for _, k := range report.Missing {
			failures = append(failures, fmt.Sprintf("KERNEL ENTRY MISSING: %s (run `make reseed-kernel`)", k))
		}
		for _, k := range report.Stale {
			failures = append(failures, fmt.Sprintf("KERNEL ENTRY STALE: %s holds a body this binary no longer builds (run `make reseed-kernel`)", k))
		}
	}

	// Kernel orphans (kernel-orphan-retirement-design.md §9) — report only,
	// never a failure: this pass does not retire anything, so an orphan must
	// not move the exit status a stale kernel already drives above.
	fmt.Println("Checking for kernel orphans (report only — not retired)...")
	switch {
	case reportErr != nil:
		fmt.Printf("  INFO  cannot check kernel orphans: kernel content comparison itself failed: %v\n", reportErr)
	case report.OrphanScanErr != nil:
		fmt.Printf("  INFO  cannot check kernel orphans: %v\n", report.OrphanScanErr)
	case len(report.OrphanedEntities) == 0 && len(report.OrphanedAspects) == 0:
		fmt.Printf("  OK  no kernel orphans\n")
	default:
		for _, k := range report.OrphanedEntities {
			fmt.Printf("  INFO  KERNEL ENTITY ORPHANED: %s no longer built by this binary\n", k)
		}
		for _, k := range report.OrphanedAspects {
			fmt.Printf("  INFO  KERNEL ASPECT ORPHANED: %s — its entity is still built\n", k)
		}
	}

	// Stranded operator roles
	// (primordial-epoch-stranded-authority-design.md §4). This is the one
	// surface where the class moves an exit status: bootstrap.VerifyKernel
	// reports it as a notice, because `make up` uses that command's exit code as
	// its freshness oracle and would respond to a failure by discarding
	// lattice.bootstrap.json and minting yet another epoch into the same bucket.
	// Nothing consumes this script's exit code that way.
	//
	// Severity ranks by consequence, across all three lenses that read a role's
	// holders: the two name-matching ones make ANY holder of an
	// `operator`-named role root-equivalent with no grant required, and
	// rbac-domain's cap.roles lens matches ANY held role and walks grantedBy, so
	// live grants become reachable the moment any holder exists. Only a role no
	// live identity holds is inert.
	fmt.Println("Checking for stranded operator roles (fails on live authority)...")
	switch {
	case reportErr != nil:
		fmt.Printf("  INFO  cannot check for stranded operator roles: kernel content comparison itself failed: %v\n", reportErr)
	case report.StrandedScanErr != nil:
		fmt.Printf("  INFO  cannot check for stranded operator roles: %v\n", report.StrandedScanErr)
	case len(report.StrandedOperatorEpochs) == 0:
		fmt.Printf("  OK  no stranded operator roles\n")
	default:
		for _, stranded := range report.StrandedOperatorEpochs {
			if stranded.Severity() == bootstrap.StrandedSeverityLiveAuthority {
				failures = append(failures, stranded.Report())
				continue
			}
			fmt.Printf("  INFO  %s\n", stranded.Report())
		}
	}

	// Stranded capability lenses — a SEPARATE scan, not part of the plan
	// above: its listing enumerates the whole vtx.meta.* population, not the
	// tens-sized vtx.role.* one the stranded-role scan bounds itself to
	// (bootstrap.StrandedCapabilityLenses's own doc comment), so it is never
	// wired into planReconcile/ReconcilePrimordial's boot path — only here
	// and in bootstrap.VerifyKernel, both already slower, occasional checks.
	//
	// A cypher-DIVERGED twin fails this gate — a stranded lens seeded by a
	// binary predating a since-narrowed cypher rule (e.g. c9a80312's
	// 2026-07-02 holdsRole->operator re-convergence) reads no
	// holdsRole/grantedBy edge at all and is untouched by edge revocation, so
	// it stays live authority regardless of what this deployment's current
	// operator role holds. A confirmed-identical twin is a notice, exactly
	// like the stranded role's own inert case.
	fmt.Println("Checking for stranded capability lenses (fails on a cypher-diverged twin)...")
	strandedLenses, lensErr := bootstrap.StrandedCapabilityLenses(ctx, coreKV)
	switch {
	case lensErr != nil:
		fmt.Printf("  INFO  cannot check for stranded capability lenses: %v\n", lensErr)
	case len(strandedLenses) == 0:
		fmt.Printf("  OK  no stranded capability lenses\n")
	default:
		for _, stranded := range strandedLenses {
			if stranded.Severity() == bootstrap.StrandedLensSeverityDiverged {
				failures = append(failures, stranded.Report())
				continue
			}
			fmt.Printf("  INFO  %s\n", stranded.Report())
		}
	}

	fmt.Println()
	if len(failures) == 0 {
		fmt.Printf("verify-kernel: ALL ASSERTIONS PASSED\n")
		os.Exit(0)
	}
	fmt.Printf("verify-kernel: %d FAILURE(S)\n\n", len(failures))
	for _, f := range failures {
		fmt.Printf("  - %s\n", f)
	}
	fmt.Printf("\nSuggestion: run `make down && make up` to re-bootstrap from clean state.\n")
	os.Exit(1)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
