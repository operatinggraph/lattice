package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/asolgan/lattice/internal/substrate"
)

// MetaVertexRef is the cached projection of a DDL meta-vertex. Built by
// scanning `vtx.meta.>` at Processor startup and incrementally maintained
// as `vtx.meta.>` mutations commit (synchronous invalidation at step 8).
//
// Per Contract #1 §1.7, a DDL meta-vertex is keyed by NanoID with a
// `canonicalName` aspect carrying the lookup name (e.g., "identity").
// This struct flattens the fields the Validator + Hydrator need into a
// single record so consumers don't perform additional Core KV reads on
// the hot path.
type MetaVertexRef struct {
	// MetaVertexKey is the canonical 3-segment key (vtx.meta.<NanoID>).
	MetaVertexKey string
	// CanonicalName is the value of the .canonicalName aspect used as the
	// lookup key. For test fixtures keyed at `vtx.meta.<class>` the
	// canonical name is `<class>`.
	CanonicalName string
	// Kind classifies the DDL: "vertexType", "aspectType", "linkType",
	// "eventType". Derived from the meta-vertex class (e.g.,
	// `meta.ddl.vertexType` → "vertexType"). Empty for shadow-keyed
	// fixtures that don't declare a precise meta class.
	Kind string
	// PermittedCommands is the list of operationTypes allowed to write
	// instances of this DDL. Empty/nil → unrestricted (permissive default
	// per Contract #1 §1.5).
	PermittedCommands []string
	// Sensitive is true when the DDL declares `sensitive: true` (Phase-1
	// applies to aspect DDLs; sensitive aspects may only attach to
	// identity vertices per NFR-S3).
	Sensitive bool
	// ScriptSource is the body of the .script aspect, if present. The
	// Hydrator surfaces this verbatim to the Executor; empty for DDLs
	// without an attached script.
	ScriptSource string
}

// DDLCache is the Processor's in-memory map from canonicalName to
// MetaVertexRef. Built at startup via Refresh and refreshed
// incrementally on `vtx.meta.>` commits (Invalidate).
//
// Concurrency: a single sync.RWMutex protects the underlying map.
// Validator + Hydrator are read paths; Committer is the sole writer
// (synchronous invalidation after a successful meta-vertex commit).
type DDLCache struct {
	conn       *substrate.Conn
	coreBucket string
	logger     *slog.Logger

	mu       sync.RWMutex
	byName   map[string]MetaVertexRef
	byMetaPK map[string]string // metaVertexKey → canonicalName (reverse index for invalidate-by-key)
}

// NewDDLCache constructs the cache. Caller MUST invoke Refresh once
// before the cache is queried.
func NewDDLCache(conn *substrate.Conn, coreBucket string, logger *slog.Logger) *DDLCache {
	if conn == nil {
		panic("processor: NewDDLCache requires Conn")
	}
	if coreBucket == "" {
		panic("processor: NewDDLCache requires coreBucket")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DDLCache{
		conn:       conn,
		coreBucket: coreBucket,
		logger:     logger,
		byName:     map[string]MetaVertexRef{},
		byMetaPK:   map[string]string{},
	}
}

// Refresh rebuilds the cache from a full scan of Core KV's `vtx.meta.>`.
// Idempotent. Safe to call repeatedly; concurrent calls are serialized
// by the cache mutex (only one rebuild proceeds at a time).
func (c *DDLCache) Refresh(ctx context.Context) error {
	keys, err := c.conn.KVListKeys(ctx, c.coreBucket)
	if err != nil {
		return fmt.Errorf("ddl cache: list keys: %w", err)
	}

	// Group keys by meta-vertex root (3-segment key). Aspects live at
	// the 4-segment form `<root>.<localName>`.
	metaKeys := map[string][]string{} // root → aspect-key list (incl. root itself)
	for _, k := range keys {
		if !strings.HasPrefix(k, "vtx.meta.") {
			continue
		}
		parts := strings.Split(k, ".")
		switch len(parts) {
		case 3:
			metaKeys[k] = append(metaKeys[k], k)
		case 4:
			root := strings.Join(parts[:3], ".")
			metaKeys[root] = append(metaKeys[root], k)
		}
	}

	byName := map[string]MetaVertexRef{}
	byPK := map[string]string{}
	for root, members := range metaKeys {
		ref, ok, err := c.loadMetaVertex(ctx, root, members)
		if err != nil {
			c.logger.Warn("ddl cache: skipping meta vertex with load error",
				"key", root, "error", err)
			continue
		}
		if !ok {
			continue
		}
		if existing, dup := byName[ref.CanonicalName]; dup {
			c.logger.Warn("ddl cache: duplicate canonicalName; keeping first-seen",
				"canonicalName", ref.CanonicalName,
				"kept", existing.MetaVertexKey,
				"dropped", ref.MetaVertexKey)
			continue
		}
		byName[ref.CanonicalName] = ref
		byPK[ref.MetaVertexKey] = ref.CanonicalName
	}

	c.mu.Lock()
	c.byName = byName
	c.byMetaPK = byPK
	c.mu.Unlock()

	c.logger.Info("ddl cache: refreshed", "entries", len(byName))
	return nil
}

// loadMetaVertex assembles a MetaVertexRef for one meta-vertex root.
// Returns (_, false, nil) when the meta-vertex does not declare a
// canonicalName (cannot be looked up — skip silently).
func (c *DDLCache) loadMetaVertex(ctx context.Context, root string, _ []string) (MetaVertexRef, bool, error) {
	ref := MetaVertexRef{MetaVertexKey: root}

	// Read the root vertex to derive Kind.
	rootEntry, err := c.conn.KVGet(ctx, c.coreBucket, root)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return ref, false, nil
		}
		return ref, false, fmt.Errorf("read root %s: %w", root, err)
	}
	var rootDoc struct {
		Class string                 `json:"class"`
		Data  map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rootEntry.Value, &rootDoc); err != nil {
		return ref, false, fmt.Errorf("unmarshal root %s: %w", root, err)
	}
	ref.Kind = deriveDDLKind(rootDoc.Class)

	// Shadow-key fallback: if the root key's last segment is a canonical-name
	// string (not a NanoID), treat it as the canonical name. This covers test
	// fixtures seeded as `vtx.meta.<class>`.
	parts := strings.Split(root, ".")
	if len(parts) == 3 && !substrate.IsValidNanoID(parts[2]) {
		ref.CanonicalName = parts[2]
	}

	// Try to load the canonicalName aspect (preferred lookup name).
	if cnEntry, err := c.conn.KVGet(ctx, c.coreBucket, root+".canonicalName"); err == nil {
		var asp struct {
			Data struct {
				Value string `json:"value"`
			} `json:"data"`
		}
		if err := json.Unmarshal(cnEntry.Value, &asp); err == nil {
			if asp.Data.Value != "" {
				ref.CanonicalName = asp.Data.Value
			}
		}
	} else if !errors.Is(err, substrate.ErrKeyNotFound) {
		return ref, false, fmt.Errorf("read canonicalName %s: %w", root, err)
	}

	// Fallback: root.data.canonicalName may carry the name directly
	// (test fixtures use this shape when the aspect key is absent).
	if ref.CanonicalName == "" && rootDoc.Data != nil {
		if v, ok := rootDoc.Data["canonicalName"].(string); ok {
			ref.CanonicalName = v
		}
	}
	if ref.CanonicalName == "" {
		// No name → cannot look up. Skip.
		return ref, false, nil
	}

	// permittedCommands aspect.
	if pcEntry, err := c.conn.KVGet(ctx, c.coreBucket, root+".permittedCommands"); err == nil {
		var asp struct {
			Data map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(pcEntry.Value, &asp); err == nil && asp.Data != nil {
			ref.PermittedCommands = extractStringSlice(asp.Data["commands"])
		}
	} else if !errors.Is(err, substrate.ErrKeyNotFound) {
		return ref, false, fmt.Errorf("read permittedCommands %s: %w", root, err)
	}
	// Fallback: root document data.permittedCommands (used by test fixtures).
	if len(ref.PermittedCommands) == 0 && rootDoc.Data != nil {
		ref.PermittedCommands = extractStringSlice(rootDoc.Data["permittedCommands"])
	}

	// sensitive aspect.
	if sEntry, err := c.conn.KVGet(ctx, c.coreBucket, root+".sensitive"); err == nil {
		var asp struct {
			Data struct {
				Value bool `json:"value"`
			} `json:"data"`
		}
		if err := json.Unmarshal(sEntry.Value, &asp); err == nil {
			ref.Sensitive = asp.Data.Value
		}
	} else if !errors.Is(err, substrate.ErrKeyNotFound) {
		return ref, false, fmt.Errorf("read sensitive %s: %w", root, err)
	}
	if !ref.Sensitive && rootDoc.Data != nil {
		if v, ok := rootDoc.Data["sensitive"].(bool); ok {
			ref.Sensitive = v
		}
	}

	// script aspect.
	if scEntry, err := c.conn.KVGet(ctx, c.coreBucket, root+".script"); err == nil {
		var asp struct {
			Data struct {
				Source string `json:"source"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scEntry.Value, &asp); err == nil {
			ref.ScriptSource = asp.Data.Source
		}
	} else if !errors.Is(err, substrate.ErrKeyNotFound) {
		return ref, false, fmt.Errorf("read script %s: %w", root, err)
	}

	return ref, true, nil
}

// Lookup returns the MetaVertexRef for canonicalName, or false if absent.
// The permissive default (Contract #1 §1.5) means callers treat "absent"
// as "no DDL to enforce" — not as an error.
func (c *DDLCache) Lookup(canonicalName string) (MetaVertexRef, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ref, ok := c.byName[canonicalName]
	return ref, ok
}

// LookupByMetaKey returns the MetaVertexRef whose canonical meta-vertex
// key matches the supplied 3-segment key. Useful when synchronously
// invalidating after a committed meta-vertex mutation.
func (c *DDLCache) LookupByMetaKey(metaKey string) (MetaVertexRef, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	name, ok := c.byMetaPK[metaKey]
	if !ok {
		return MetaVertexRef{}, false
	}
	ref, ok := c.byName[name]
	return ref, ok
}

// Invalidate re-loads a single meta-vertex (by root key) into the cache.
// Called synchronously by the Committer after a successful step 8 batch
// that touched `vtx.meta.>` keys (DDL mutations trigger synchronous cache
// invalidation at step 8).
//
// metaRootKey is the 3-segment `vtx.meta.<id>` key. If the supplied key
// is a 4-segment aspect key, the root is derived automatically.
func (c *DDLCache) Invalidate(ctx context.Context, metaRootKey string) error {
	parts := strings.Split(metaRootKey, ".")
	if len(parts) >= 3 {
		metaRootKey = strings.Join(parts[:3], ".")
	}
	if !strings.HasPrefix(metaRootKey, "vtx.meta.") {
		return fmt.Errorf("ddl cache: invalidate: key %q is not a meta-vertex key", metaRootKey)
	}

	// Hold the write lock for the entire operation (including the KV read) to
	// eliminate the TOCTOU window where two concurrent Invalidate calls could
	// race on priorName and leave the cache indexed under a stale canonical name.
	// Lock contention is acceptable — Invalidate is a rare DDL-commit path.
	c.mu.Lock()
	defer c.mu.Unlock()
	priorName, hadPrior := c.byMetaPK[metaRootKey]

	ref, ok, err := c.loadMetaVertex(ctx, metaRootKey, nil)
	if err != nil {
		return fmt.Errorf("ddl cache: invalidate %s: %w", metaRootKey, err)
	}

	if hadPrior {
		delete(c.byName, priorName)
		delete(c.byMetaPK, metaRootKey)
	}
	if ok {
		c.byName[ref.CanonicalName] = ref
		c.byMetaPK[ref.MetaVertexKey] = ref.CanonicalName
	}
	c.logger.Info("ddl cache: invalidated",
		"metaKey", metaRootKey, "canonicalName", ref.CanonicalName, "present", ok)
	return nil
}

// Size returns the number of cached entries (for tests and metrics).
func (c *DDLCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byName)
}

// deriveDDLKind maps a meta-vertex class to a kind string.
// `meta.ddl.vertexType` → `vertexType`, etc. Returns the trailing
// segment after `meta.ddl.`, or the empty string if the class doesn't
// match the meta.ddl prefix (e.g., `meta.lens`, `meta.script`).
func deriveDDLKind(class string) string {
	const prefix = "meta.ddl."
	if strings.HasPrefix(class, prefix) {
		return strings.TrimPrefix(class, prefix)
	}
	return ""
}

// extractStringSlice handles both []string and []interface{} ([]any)
// shapes that the JSON decoder may surface depending on whether the
// raw payload was a literal list or a generic-decoded array.
func extractStringSlice(v interface{}) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []interface{}:
		out := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
