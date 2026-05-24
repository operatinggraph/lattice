package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/asolgan/lattice/internal/substrate"
)

// VerifyKernel checks that all post-Story-5.3 kernel Core KV keys and
// infrastructure elements exist with correct envelopes. It returns a
// (possibly empty) slice of failure descriptions; an empty slice means
// all assertions passed.
//
// This is the callable equivalent of scripts/verify-kernel.go so that
// cmd/lattice/bootstrap and any future tooling can reuse the same
// assertions without drift.
func VerifyKernel(ctx context.Context, conn *substrate.Conn) []string {
	js := conn.JetStream()
	var failures []string

	// Open Core KV.
	coreKV, err := js.KeyValue(ctx, CoreKVBucket)
	if err != nil {
		return []string{fmt.Sprintf("cannot open Core KV bucket %q: %v", CoreKVBucket, err)}
	}

	// Open Health KV.
	healthKV, err := js.KeyValue(ctx, HealthKVBucket)
	if err != nil {
		return []string{fmt.Sprintf("cannot open Health KV bucket %q: %v", HealthKVBucket, err)}
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

	// 2. Meta-meta DDL aspects.
	for _, aspect := range []string{
		"canonicalName", "permittedCommands", "description", "script",
		"inputSchema", "outputSchema", "fieldDescription", "examples",
		"compensation",
	} {
		k := MetaRootKey + "." + aspect
		if _, err := coreKV.Get(ctx, k); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING meta-DDL aspect: %s (%v)", k, err))
		}
	}

	// 2a. Aspect-type meta-vertex aspects.
	aspectTypeKeys := []string{
		AspectTypeDescriptionKey,
		AspectTypeInputSchemaKey,
		AspectTypeOutputSchemaKey,
		AspectTypeFieldDescriptionKey,
		AspectTypeExamplesKey,
	}
	for _, vtxKey := range aspectTypeKeys {
		for _, asp := range []string{"canonicalName", "description", "inputSchema", "outputSchema", "fieldDescription", "examples"} {
			k := vtxKey + "." + asp
			if _, err := coreKV.Get(ctx, k); err != nil {
				failures = append(failures, fmt.Sprintf("MISSING aspect-type aspect: %s (%v)", k, err))
			}
		}
	}

	// 3. Operator role aspects.
	for _, aspect := range []string{"canonicalName", "description"} {
		k := RoleOperatorKey + "." + aspect
		if _, err := coreKV.Get(ctx, k); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING operator role aspect: %s (%v)", k, err))
		}
	}

	// 4. Capability Lens aspects.
	lensAspects := []string{"canonicalName", "targetBucket", "cypherRule", "outputSchema", "spec"}
	for _, aspect := range lensAspects {
		k := CapabilityLensKey + "." + aspect
		if _, err := coreKV.Get(ctx, k); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING Capability Lens aspect: %s (%v)", k, err))
		}
	}
	for _, aspect := range lensAspects {
		k := CapabilityRoleIndexLensKey + "." + aspect
		if _, err := coreKV.Get(ctx, k); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING CapabilityRoleIndex Lens aspect: %s (%v)", k, err))
		}
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

	// 7. KV buckets.
	for _, bucket := range []string{
		CoreKVBucket, HealthKVBucket, CapabilityKVBucket,
		WeaverStateBucket, WeaverClaimsBucket, RefractorAdjacencyKV,
	} {
		if _, err := js.KeyValue(ctx, bucket); err != nil {
			failures = append(failures, fmt.Sprintf("MISSING KV bucket: %s (%v)", bucket, err))
		}
	}

	return failures
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

