package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// CoreKVEmpty reports whether Core KV holds no entries at all — the state a
// stack recreated out-of-band (a new, unseeded NATS) presents. It is the
// file-independent discriminator `make up` needs after a `bootstrap verify`
// mismatch: an empty bucket is the recreated-containers case, where the
// surviving lattice.bootstrap.json must be KEPT so the bootstrap binary
// re-seeds the primordial set at its stable NanoIDs (PrimordialSeeded) and
// existing references stay valid; a populated bucket whose set the file does
// not match is a genuine stale-file mismatch, where the file must be discarded
// and a fresh set minted.
//
// A missing bucket counts as empty. Tombstones count as content — KVListKeys
// does not filter them — which is the conservative choice: a bucket that ever
// held a graph reads as populated, so the destructive discard-and-mint path is
// taken only when Core KV demonstrably holds a different set.
func CoreKVEmpty(ctx context.Context, conn *substrate.Conn) (bool, error) {
	if err := conn.KVStatus(ctx, CoreKVBucket); err != nil {
		if errors.Is(err, substrate.ErrBucketNotFound) {
			return true, nil
		}
		return false, fmt.Errorf("probe Core KV status: %w", err)
	}
	keys, err := conn.KVListKeys(ctx, CoreKVBucket)
	if err != nil {
		return false, fmt.Errorf("list Core KV keys: %w", err)
	}
	return len(keys) == 0, nil
}

// VerifyKernel checks that all kernel Core KV keys and infrastructure
// elements exist with correct envelopes. Returns a (possibly empty) slice of
// failure descriptions — an empty slice means all assertions passed — plus a
// (possibly empty) slice of informational notices that never affect
// pass/fail: today, kernel-orphaned vtx.meta.* entities and aspects
// (kernel-orphan-retirement-design.md §9) an operator may want to see but
// that this pass neither writes nor holds against the bucket.
//
// This is the callable equivalent of scripts/verify-kernel.go so that
// any tooling can reuse the same assertions without drift.
func VerifyKernel(ctx context.Context, conn *substrate.Conn) (failures, notices []string) {
	js := conn.JetStream()

	// Open Core KV.
	coreKV, err := js.KeyValue(ctx, CoreKVBucket)
	if err != nil {
		return []string{fmt.Sprintf("cannot open Core KV bucket %q: %v", CoreKVBucket, err)}, nil
	}

	// Open Health KV.
	healthKV, err := js.KeyValue(ctx, HealthKVBucket)
	if err != nil {
		return []string{fmt.Sprintf("cannot open Health KV bucket %q: %v", HealthKVBucket, err)}, nil
	}

	// 1. Top-level kernel vertex keys + envelope sanity.
	primordialKeys := PrimordialVertexKeys()
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
		if cb, ok := env["createdBy"].(string); !ok || cb != BootstrapIdentityKey {
			failures = append(failures, fmt.Sprintf("WRONG createdBy for key %s: got %v", key, env["createdBy"]))
		}
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
		if echoKey, ok := env["key"].(string); !ok || echoKey != k {
			failures = append(failures, fmt.Sprintf("KEY MISMATCH in aspect %s: envelope.key=%q", k, echoKey))
		}
		if cls, ok := env["class"].(string); !ok || cls != expectedClass {
			failures = append(failures, fmt.Sprintf("CLASS MISMATCH for aspect %s: got %q want %q", k, env["class"], expectedClass))
		}
		if isDeleted, ok := env["isDeleted"].(bool); !ok || isDeleted {
			failures = append(failures, fmt.Sprintf("INVALID isDeleted for aspect %s", k))
		}
		if vk, ok := env["vertexKey"].(string); !ok || vk != parentKey {
			failures = append(failures, fmt.Sprintf("WRONG vertexKey for aspect %s: got %q want %q", k, env["vertexKey"], parentKey))
		}
	}

	// 2. Meta-meta DDL aspects.
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
		checkAspect(MetaRootKey+"."+a.name, MetaRootKey, a.class)
	}

	// 2a. Aspect-type meta-vertex aspects.
	aspectTypeKeys := []string{
		AspectTypeDescriptionKey,
		AspectTypeInputSchemaKey,
		AspectTypeOutputSchemaKey,
		AspectTypeFieldDescriptionKey,
		AspectTypeExamplesKey,
	}
	aspectTypeAspects := []struct{ name, class string }{
		{"canonicalName", "canonicalName"},
		{"description", "description"},
		{"inputSchema", "inputSchema"},
		{"outputSchema", "outputSchema"},
		{"fieldDescription", "fieldDescription"},
		{"examples", "examples"},
	}
	for _, vtxKey := range aspectTypeKeys {
		for _, a := range aspectTypeAspects {
			checkAspect(vtxKey+"."+a.name, vtxKey, a.class)
		}
	}

	// 3. Operator role aspects.
	for _, a := range []struct{ name, class string }{
		{"canonicalName", "canonicalName"},
		{"description", "description"},
	} {
		checkAspect(RoleOperatorKey+"."+a.name, RoleOperatorKey, a.class)
	}

	// 4. Capability Lens aspects (the primordial-identity anchor). It is an
	// actor-aggregate lens, so it also carries projectionKind + the §6.13
	// output descriptor. The role-by-operation index is owned by the
	// rbac-domain package and is verified by verify-package-rbac, not here.
	lensAspects := []struct{ name, class string }{
		{"canonicalName", "canonicalName"},
		{"targetBucket", "targetBucket"},
		{"cypherRule", "cypherRule"},
		{"outputSchema", "outputSchema"},
		{"projectionKind", "projectionKind"},
		{"output", "output"},
		{"spec", "lensSpec"},
	}
	for _, a := range lensAspects {
		checkAspect(CapabilityLensKey+"."+a.name, CapabilityLensKey, a.class)
	}

	// 4b. Capability-Read Lens aspects (the base read-path authorization lens,
	// Contract #6 §6.14, D1). Also an actor-aggregate lens, same aspect set.
	// Package read-grant lenses (cap-read.roles, cap-read.residence, …) are
	// verified by their owning verify-package targets, not here.
	for _, a := range lensAspects {
		checkAspect(CapabilityReadLensKey+"."+a.name, CapabilityReadLensKey, a.class)
	}

	// 4c. Capability-Read GRANTS Lens aspects (the base read-grant producer,
	// Contract #6 §6.14, D1.3). A plain postgres GrantTable lens — no
	// projectionKind/output/outputSchema, and a targetTable doc-aspect instead
	// of targetBucket. The load-bearing spec aspect carries the postgres
	// targetConfig Refractor activates.
	grantsLensAspects := []struct{ name, class string }{
		{"canonicalName", "canonicalName"},
		{"cypherRule", "cypherRule"},
		{"targetTable", "targetTable"},
		{"spec", "lensSpec"},
	}
	for _, a := range grantsLensAspects {
		checkAspect(CapabilityReadGrantsLensKey+"."+a.name, CapabilityReadGrantsLensKey, a.class)
	}

	// 4d. Capability-Read WILDCARD Grants Lens aspects (the base all-access
	// read-grant producer, Contract #6 §6.14, D1 design §3.4 M5). Same shape
	// as 4c's GrantTable lens.
	for _, a := range grantsLensAspects {
		checkAspect(CapabilityReadWildcardGrantsLensKey+"."+a.name, CapabilityReadWildcardGrantsLensKey, a.class)
	}

	// 5. Health KV readiness signal.
	if _, err := healthKV.Get(ctx, HealthBootstrapCompleteKey); err != nil {
		failures = append(failures, fmt.Sprintf("MISSING Health KV readiness signal: %s (%v)", HealthBootstrapCompleteKey, err))
	}

	// 6. JetStream streams.
	for _, streamName := range []string{CoreOpsStreamName, CoreEventsStreamName} {
		if _, err := js.Stream(ctx, streamName); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING JetStream stream: %s (%v)", streamName, err))
		}
	}

	// 7. KV buckets — every registry row (bootstrap.PlatformBuckets) must be
	// provisioned; the registry is now ProvisionBuckets' source, so this
	// verifies parity rather than a hand-copied subset.
	for _, b := range PlatformBuckets() {
		if _, err := js.KeyValue(ctx, b.Name); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING KV bucket: %s (%v)", b.Name, err))
		}
	}

	// 7a. core-objects Object Store (the off-graph blob plane). It is a
	// JetStream Object Store, not a KV bucket, so it is a separate assertion
	// shape; the substrate helper maps a missing store to ErrBucketNotFound.
	if err := conn.ObjectStoreExists(ctx, CoreObjectsBucket); err != nil {
		failures = append(failures, fmt.Sprintf("MISSING Object Store: %s (%v)", CoreObjectsBucket, err))
	}

	// AllowAtomicPublish must be set on the buckets whose writers use atomic
	// batches: Core KV (Processor commit) and loom-state (Loom step transition,
	// Contract #10 §10.3). Without it, Conn.AtomicBatch on the bucket is rejected.
	for _, bucket := range []string{CoreKVBucket, LoomStateBucket} {
		stream, err := js.Stream(ctx, "KV_"+bucket)
		if err != nil {
			failures = append(failures, fmt.Sprintf("CANNOT read stream KV_%s for AllowAtomicPublish check: %v", bucket, err))
			continue
		}
		if !stream.CachedInfo().Config.AllowAtomicPublish {
			failures = append(failures, fmt.Sprintf("AllowAtomicPublish NOT set on KV_%s (Conn.AtomicBatch would be rejected)", bucket))
		}
	}

	// Kernel freshness + orphans, from a SINGLE plan (ReadKernelReport): every
	// check above asserts that a key is present and well-formed, which a
	// bucket seeded by an older binary satisfies while running superseded DDL
	// scripts. This compares stored content against what this binary builds,
	// so `make up`'s reuse short-circuit (which calls this function) stops
	// treating a stale kernel as fresh. Reading missing/stale and the orphan
	// report from one call rather than two (KernelDrift then KernelOrphans)
	// halves the vtx.meta.* read volume this function drives — the built-set
	// comparison and the orphan scan both walk the same bucket.
	report, err := ReadKernelReport(ctx, coreKV)
	switch {
	case err != nil:
		failures = append(failures, fmt.Sprintf("CANNOT compare kernel content: %v", err))
	default:
		for _, k := range report.Missing {
			failures = append(failures, fmt.Sprintf("KERNEL ENTRY MISSING: %s (run `make reseed-kernel`)", k))
		}
		for _, k := range report.Stale {
			failures = append(failures, fmt.Sprintf("KERNEL ENTRY STALE: %s holds a body this binary no longer builds (run `make reseed-kernel`)", k))
		}

		// Kernel orphans (kernel-orphan-retirement-design.md §9). Neither
		// class is a failure: an orphan is dead weight this pass reports,
		// never retires on its own say-so
		// (kernel-orphan-retirement-design.md §3.4's whole-entity-only rule),
		// so it belongs in notices rather than in the failures slice that
		// drives this function's own exit status.
		switch {
		case report.OrphanScanErr != nil:
			notices = append(notices, fmt.Sprintf("CANNOT check kernel orphans: %v", report.OrphanScanErr))
		default:
			for _, k := range report.OrphanedEntities {
				notices = append(notices, fmt.Sprintf("KERNEL ENTITY ORPHANED: %s no longer built by this binary", k))
			}
			for _, k := range report.OrphanedAspects {
				notices = append(notices, fmt.Sprintf("KERNEL ASPECT ORPHANED: %s — its entity is still built", k))
			}
		}

		// Stranded primordial-epoch roles
		// (primordial-epoch-stranded-authority-design.md §4). The split is on
		// live grants, not on the role and never on its holders: an `operator`
		// role from a prior epoch that still confers permissions is an
		// authority island on the trust boundary that no current-epoch identity
		// can reach and no census above can see, so it is a FAILURE — the green
		// is the defect this row is about. With every grant revoked the same
		// role is dead weight, which belongs with the orphans in notices. The
		// prior-epoch holders are reported as detail because they are part of
		// the island being described, and they move nothing.
		switch {
		case report.StrandedScanErr != nil:
			notices = append(notices, fmt.Sprintf("CANNOT check for stranded primordial-epoch roles: %v", report.StrandedScanErr))
		default:
			for _, stranded := range report.StrandedOperatorEpochs {
				if len(stranded.GrantedBy) == 0 {
					notices = append(notices, fmt.Sprintf(
						"STRANDED OPERATOR ROLE: %s is from a prior bootstrap epoch and reachable from no current-epoch identity (no live grants, %d prior-epoch holder(s))",
						stranded.RoleKey, len(stranded.Holders)))
					continue
				}
				failures = append(failures, fmt.Sprintf(
					"STRANDED OPERATOR ROLE: %s is from a prior bootstrap epoch, reachable from no current-epoch identity, and still confers %d live grant(s) (%d prior-epoch holder(s))",
					stranded.RoleKey, len(stranded.GrantedBy), len(stranded.Holders)))
			}
		}
	}

	return failures, notices
}

// InspectKernel reads and returns selected primordial entries for human display.
// Returns a slice of (key, raw-value-bytes) pairs for the top-level vertex keys.
func InspectKernel(ctx context.Context, conn *substrate.Conn) ([]KernelEntry, error) {
	js := conn.JetStream()
	coreKV, err := js.KeyValue(ctx, CoreKVBucket)
	if err != nil {
		return nil, fmt.Errorf("open Core KV: %w", err)
	}

	var entries []KernelEntry
	for _, key := range PrimordialVertexKeys() {
		entry, err := coreKV.Get(ctx, key)
		if err != nil {
			entries = append(entries, KernelEntry{Key: key, Missing: true})
			continue
		}
		var doc map[string]any
		_ = json.Unmarshal(entry.Value(), &doc)
		entries = append(entries, KernelEntry{
			Key: key,
			Doc: doc,
		})
	}
	return entries, nil
}

// KernelEntry holds one kernel vertex inspection result.
type KernelEntry struct {
	Key     string         `json:"key"`
	Missing bool           `json:"missing,omitempty"`
	Doc     map[string]any `json:"doc,omitempty"`
}
