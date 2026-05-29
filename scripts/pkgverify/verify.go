// Package pkgverify provides shared infrastructure for the verify-package-*
// scripts. Each per-package verify script imports this library for common
// Core KV traversal helpers, keeping per-script code to the package-specific
// assertions only.
//
// Usage: imported by scripts/verify-package-*.go via:
//
//	import "github.com/asolgan/lattice/scripts/pkgverify"
//
// The library requires a live jetstream.KeyValue handle and a pre-populated
// allKeys map (snapshot of all keys). Callers construct those in main() and
// pass them to the helpers.
package pkgverify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nats-io/nats.go/jetstream"
)

// ListAllKeys returns a set (map[string]struct{}) of all live (non-tombstone)
// keys in the KV bucket.
func ListAllKeys(ctx context.Context, kv jetstream.KeyValue) (map[string]struct{}, error) {
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

// GetEnvelope fetches a single key and unmarshals it as a JSON map.
func GetEnvelope(ctx context.Context, kv jetstream.KeyValue, key string) (map[string]any, error) {
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

// CheckAspectEnvelope validates the envelope shape of an aspect key:
//   - vertexKey field matches expectedVertexKey
//   - localName field matches expectedLocalName
//
// Returns a non-nil error describing every violation found.
// Callers typically use this alongside their data-value assertions.
func CheckAspectEnvelope(env map[string]any, key, expectedVertexKey, expectedLocalName string) error {
	var errs []string
	vk, ok := env["vertexKey"].(string)
	if !ok || vk != expectedVertexKey {
		errs = append(errs, fmt.Sprintf("vertexKey: got %q want %q", env["vertexKey"], expectedVertexKey))
	}
	ln, ok2 := env["localName"].(string)
	if !ok2 || ln != expectedLocalName {
		errs = append(errs, fmt.Sprintf("localName: got %q want %q", env["localName"], expectedLocalName))
	}
	if len(errs) > 0 {
		return fmt.Errorf("aspect envelope shape error for %s: %s", key, strings.Join(errs, "; "))
	}
	return nil
}

// FindMetaByCanonical scans vtx.meta.*.canonicalName aspects and returns the
// meta-vertex key (vtx.meta.<NanoID>) whose data.value matches wantCanonical.
func FindMetaByCanonical(ctx context.Context, kv jetstream.KeyValue, allKeys map[string]struct{}, wantCanonical string) (string, error) {
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.meta.") {
			continue
		}
		if !strings.HasSuffix(key, ".canonicalName") {
			continue
		}
		env, err := GetEnvelope(ctx, kv, key)
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

// FindPackageManifest scans vtx.package.*.manifest and returns
// (pkgVertexKey, manifestKey) for the first non-tombstoned entry whose
// data.name matches pkgName.
func FindPackageManifest(ctx context.Context, kv jetstream.KeyValue, allKeys map[string]struct{}, pkgName string) (string, string, error) {
	for key := range allKeys {
		if !strings.HasPrefix(key, "vtx.package.") {
			continue
		}
		if !strings.HasSuffix(key, ".manifest") {
			continue
		}
		env, err := GetEnvelope(ctx, kv, key)
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

// ToStringSlice converts an any (expected []any of strings) to []string.
func ToStringSlice(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ToSet converts a []string to a map[string]bool for O(1) lookup.
func ToSet(ss []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		m[s] = true
	}
	return m
}

// EnvOrDefault returns the value of the environment variable key, or def if
// the variable is unset or empty.
func EnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
