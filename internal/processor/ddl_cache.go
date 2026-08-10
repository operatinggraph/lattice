package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/operatinggraph/lattice/internal/substrate"
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
	// applies to aspect DDLs; a sensitive aspect's anchoring rule follows
	// CustodyKind below).
	Sensitive bool
	// Abstract is true when the DDL declares itself an abstract type — a type
	// naming no instance (dynamic-type-taxonomy-design.md §3.2). Populated
	// from the root vertex document's `data.abstract`, never derived from "a
	// vertexType with no script" (the accident-of-shape failure the marker is
	// explicit to avoid). Absent means false; a PRESENT but non-bool value
	// fails closed to true instead (loadMetaVertex logs a WARN) — false is
	// the permissive direction for the abstract write-path gates, so an
	// ambiguous marker must not resolve to it.
	Abstract bool
	// CustodyKind is the declared key-custody kind of a sensitive aspect DDL
	// (retention-class-key-custody-design.md §3.2): "identity" — the default,
	// and what an absent declaration means — or "retentionClass". It is what
	// makes step 6's identity-anchoring rule conditional rather than absolute.
	CustodyKind string
	// CustodyHolderKey is the resolved vertex key of the key holder, set only
	// for CustodyKind "retentionClass". The INSTALL resolves the declared
	// class's canonical name to this key, so the commit path performs no
	// extra read to learn whose DEK encrypts the aspect.
	CustodyHolderKey string
	// ScriptSource is the body of the .script aspect, if present. The
	// Hydrator surfaces this verbatim to the Executor; empty for DDLs
	// without an attached script.
	ScriptSource string
	// Script is ScriptSource's lazily-compiled program, shared by every
	// operation on this DDL and by both passes over it — the step-4
	// `derive_reads` pre-pass (Contract #2 §2.5 class (g)) and step 5's
	// `execute` call. Nil when the DDL declares no script.
	//
	// A pointer, so the value copy Lookup hands out still shares ONE
	// compile; rebuilt rather than mutated whenever the entry is rebuilt, so
	// an edited script can never keep serving the program its previous
	// source compiled to.
	Script *CompiledScript
}

// Key-custody kinds, as they appear in a DDL meta-vertex's `.custody` aspect
// (retention-class-key-custody-design.md §3.2). pkgmgr WRITES these strings at
// install; the Processor reads them here. They are deliberately duplicated
// rather than imported: pkgmgr already depends on this package, so the import
// would cycle — the same reason the `.sensitive` aspect's name is a literal on
// both sides. Any change to these values must land on both.
const (
	// CustodyKindIdentity custodies a sensitive aspect's DEK on the aspect's
	// own anchoring identity. The default: an ABSENT custody declaration
	// means this, so an empty CustodyKind and this constant are the same
	// case everywhere they are tested.
	CustodyKindIdentity = "identity"

	// CustodyKindRetentionClass custodies the DEK on a package-declared
	// retention-class holder, which permits any anchor type.
	CustodyKindRetentionClass = "retentionClass"

	// RetentionClassVertexType is the Contract #1 type segment of a holder
	// vertex — all lowercase, because a type segment is [a-z][a-z0-9]*. The
	// declared KIND above is camelCase; these are two different strings and
	// conflating them yields a key nothing can address.
	RetentionClassVertexType = "retentionclass"

	// CustodyKindUnresolvable marks a DDL whose custody declaration exists but
	// could not be read. It is deliberately not a legal declared kind — the
	// install admits only the two above — so every consumer's default branch
	// refuses it, which is the point: a class whose custodian is unknown must
	// reject its writes rather than guess a holder.
	CustodyKindUnresolvable = "!unresolvable"
)

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
	// byCommand maps an operationType to the single vertexType DDL's
	// canonicalName that admits it — the operationType→class reverse index
	// (Contract #2 §2.1). It lets the Hydrator resolve a dispatched op's class
	// when the envelope omits it: an engine that builds the payload knows the
	// operationType but not the DDL canonical name. Two disciplines keep it
	// integrity-safe:
	//   - vertexType-ONLY: only script-bearing vertexType DDLs are indexed.
	//     An aspectType DDL lists an op in its permittedCommands purely as a
	//     step-6 write gate (the multi-key-write pattern: an op writing a typed
	//     aspect names itself in that aspect's permittedCommands), but its
	//     script is declaration-only and never executes the op — so it is never
	//     a class-inference target. Example: RecordIdentityPII is admitted by
	//     identity (vertexType, the executing script) + ssn + dob (aspectType
	//     gates); only identity is indexed.
	//   - global ambiguity guard: if two vertexType DDLs admit the same op, the
	//     op is left OUT of the index (fall through to explicit class). Inferring
	//     a class for an ambiguous op could run the wrong script — fail closed.
	byCommand map[string]string
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
		byCommand:  map[string]string{},
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
	c.byCommand = buildByCommand(byName, c.logger)
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
		Class     string                 `json:"class"`
		IsDeleted bool                   `json:"isDeleted"`
		Data      map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rootEntry.Value, &rootDoc); err != nil {
		return ref, false, fmt.Errorf("unmarshal root %s: %w", root, err)
	}
	// A tombstoned root means the whole meta-vertex is gone. Report absent
	// before any aspect reads so Invalidate drops the entry from byName /
	// byMetaPK (and never re-inserts it) and any direct load reports absent.
	if rootDoc.IsDeleted {
		return ref, false, nil
	}
	ref.Kind = deriveDDLKind(rootDoc.Class)

	// Abstract marker (dynamic-type-taxonomy-design.md §3.2): read straight off
	// the root document's `data.abstract`, never derived from the DDL's other
	// fields. Absent means false — the overwhelming common case, every DDL
	// that declares no taxonomy membership at all. A PRESENT but non-bool
	// value is a different case: false is the PERMISSIVE direction for the
	// two step-6 write-path gates this field feeds (an unrecognized value
	// resolving to false would let an instance of a type someone tried to
	// mark abstract keep writing undetected), so an ambiguous marker fails
	// CLOSED toward true instead — the doctrine step 6.5's custody-kind
	// unmarshal failure already states (ddl_cache.go's own comment there:
	// "each alternative fails OPEN in its own direction"). Logged so the
	// drift is visible rather than silently resolved either way.
	if rootDoc.Data != nil {
		if raw, present := rootDoc.Data["abstract"]; present {
			if v, ok := raw.(bool); ok {
				ref.Abstract = v
			} else {
				c.logger.Warn("ddl cache: data.abstract is present but not a JSON bool; treating as abstract (fail-closed — false is the permissive direction for the abstract write-path gates)",
					"key", root, "value", raw)
				ref.Abstract = true
			}
		}
	}

	// Shadow-key fallback: if the root key's last segment is a canonical-name
	// string (not a NanoID), treat it as the canonical name. This covers test
	// fixtures seeded as `vtx.meta.<class>`.
	parts := strings.Split(root, ".")
	if len(parts) == 3 && !substrate.IsValidNanoID(parts[2]) {
		ref.CanonicalName = parts[2]
	}

	// Try to load the canonicalName aspect (preferred lookup name). A tombstone
	// retains the prior document, so the name must be read as ABSENT when
	// deleted — the same rule the permittedCommands, custody and script readers
	// below state, and the one that makes a registration REMOVABLE at all. Read
	// a tombstoned name as live and the entry keeps serving under it forever:
	// nothing else in the meta-vertex carries the lookup name, so there is no
	// second write that could retire it, and every gate keyed off DDLs.Lookup
	// keeps answering for a name its owner has withdrawn.
	if cnEntry, err := c.conn.KVGet(ctx, c.coreBucket, root+".canonicalName"); err == nil {
		var asp struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				Value string `json:"value"`
			} `json:"data"`
		}
		if err := json.Unmarshal(cnEntry.Value, &asp); err == nil && !asp.IsDeleted {
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

	// permittedCommands aspect. A tombstone retains the prior document, so the
	// declaration must be read as ABSENT when deleted — the same rule the
	// custody reader below states, and it binds here for a sharper reason. An
	// in-place upgrade keeps a DDL's meta-vertex NanoID (Contract #8 §8.1), so
	// a package that stops emitting this aspect gets it TOMBSTONED, never
	// removed (pkgmgr diffManifest → step8_commit's tombstone arm, which
	// copies the prior document whole and only flips isDeleted). Reading a
	// tombstone as live would leave a type that declares no commands admitting
	// every command it used to — and for a type upgraded to ABSTRACT that
	// inverts the one invariant the abstract marker exists to assert.
	if pcEntry, err := c.conn.KVGet(ctx, c.coreBucket, root+".permittedCommands"); err == nil {
		var asp struct {
			IsDeleted bool                   `json:"isDeleted"`
			Data      map[string]interface{} `json:"data"`
		}
		if err := json.Unmarshal(pcEntry.Value, &asp); err == nil && asp.Data != nil && !asp.IsDeleted {
			ref.PermittedCommands = extractStringSlice(asp.Data["commands"])
		}
	} else if !errors.Is(err, substrate.ErrKeyNotFound) {
		return ref, false, fmt.Errorf("read permittedCommands %s: %w", root, err)
	}
	// Fallback: root document data.permittedCommands (used by test fixtures).
	if len(ref.PermittedCommands) == 0 && rootDoc.Data != nil {
		ref.PermittedCommands = extractStringSlice(rootDoc.Data["permittedCommands"])
	}

	// sensitive aspect. Unlike the permittedCommands, custody and script
	// readers above, a tombstoned sensitive aspect is read as LIVE, not
	// absent. That is a stated posture, not the missing filter it looks like
	// next to its three siblings — verify the reasoning against the code
	// before changing it, not just against this comment.
	//
	// The write path and the read path gate on two different declarations,
	// not one. Encryption at write time is gated on this DDL's own Sensitive
	// field (step65_encrypt.go's `!ok || !ref.Sensitive` check: once a
	// mutation resolves to a sensitive class, step 6.5 encrypts it under the
	// class DEK). Decryption at read time is gated on the LENS's own
	// SecureColumns declaration (internal/refractor/pipeline/secure.go's
	// SecureDecryptor, built from pkgmgr's LensSpec.SecureColumns —
	// definition.go:1087-1093) — an independent per-lens declaration that
	// says nothing about this DDL and is never derived from it.
	//
	// Because of that split, honoring a withdrawal here would not retire
	// anything: it would split one class's rows into ciphertext (written
	// while the aspect was live) and plaintext (written after it was
	// tombstoned), with no migration between the two, while any lens that
	// still declares the column secure keeps running its decryptor over
	// both. A meta-vertex tombstone can stop future writes from being
	// encrypted; it cannot un-encrypt what is already at rest, and reading
	// the withdrawal as effective would quietly start writing plaintext
	// alongside ciphertext under the same column. That is a decision about
	// existing ciphertext, which this reader is not positioned to make, not
	// a filter it forgot to apply.
	//
	// This is also why the posture is the safe one, not just the cautious
	// one. The permittedCommands, custody and script readers all read a
	// tombstone as ABSENT because staying live in those cases OVER-GRANTS: a
	// type that stops declaring commands would keep admitting every command
	// it used to, a class that revokes custody would keep writing to a DEK
	// it disowned, a type that withdraws its script would keep executing one
	// it no longer declares. Sensitive is the mirror image: staying live
	// here can only OVER-PROTECT, continuing to encrypt a class whose
	// package tried to stop declaring it sensitive. Same tombstone shape,
	// opposite safe direction — encryption failing open toward
	// confidentiality is the harmless side of this particular declaration.
	if sEntry, err := c.conn.KVGet(ctx, c.coreBucket, root+".sensitive"); err == nil {
		var asp struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				Value bool `json:"value"`
			} `json:"data"`
		}
		if err := json.Unmarshal(sEntry.Value, &asp); err == nil {
			if asp.IsDeleted && asp.Data.Value {
				c.logger.Warn("ddl cache: sensitive aspect tombstoned but still declares true; withdrawal not honored, class stays sensitive",
					"key", root+".sensitive")
			}
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

	// custody aspect. Absent → the identity kind. Nothing is defaulted here on
	// purpose: an empty CustodyKind IS the identity kind, and every consumer
	// reads it that way, so a DDL carrying no custody declaration needs no
	// value materialized at load time to behave correctly.
	if cEntry, err := c.conn.KVGet(ctx, c.coreBucket, root+".custody"); err == nil {
		var asp struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				Kind      string `json:"kind"`
				HolderKey string `json:"holderKey"`
			} `json:"data"`
		}
		// An unparseable custody declaration poisons the kind rather than
		// zeroing it or failing the load. Each alternative fails OPEN in its
		// own direction: zeroing degrades the DDL to identity custody, quietly
		// re-pointing a retained record's DEK at the very data subject whose
		// erasure it was declared to survive; returning an error makes Refresh
		// skip the meta-vertex entirely, so the class resolves to NO DDL, is
		// never seen as sensitive, and commits as plaintext. Poisoning keeps
		// the DDL present and sensitive with a kind no branch accepts, so
		// step 6 rejects every write to it — loudly, and with nothing at rest.
		if err := json.Unmarshal(cEntry.Value, &asp); err != nil {
			c.logger.Warn("ddl cache: unparseable custody aspect; refusing writes to this class",
				"key", root+".custody", "error", err)
			ref.CustodyKind = CustodyKindUnresolvable
			ref.CustodyHolderKey = ""
		} else if !asp.IsDeleted {
			// A tombstone retains the prior document, so the declaration must
			// be read as ABSENT when deleted — otherwise a package that
			// revokes its custody declaration keeps writing to the class DEK
			// forever, with no way to un-declare short of dropping the DDL.
			ref.CustodyKind = asp.Data.Kind
			ref.CustodyHolderKey = asp.Data.HolderKey
		}
	} else if !errors.Is(err, substrate.ErrKeyNotFound) {
		return ref, false, fmt.Errorf("read custody %s: %w", root, err)
	}
	if ref.CustodyKind == "" && rootDoc.Data != nil {
		if m, ok := rootDoc.Data["custody"].(map[string]interface{}); ok {
			ref.CustodyKind, _ = m["kind"].(string)
			ref.CustodyHolderKey, _ = m["holderKey"].(string)
		}
	}

	// script aspect. Read as ABSENT when tombstoned, for the same reason the
	// permittedCommands and custody readers do: an in-place upgrade tombstones
	// the aspects a package stops emitting rather than removing them, so a
	// retained script would keep executing for a class whose package withdrew
	// it — and on a concrete type upgraded to ABSTRACT, would keep executing
	// for a type that declares none at all.
	if scEntry, err := c.conn.KVGet(ctx, c.coreBucket, root+".script"); err == nil {
		var asp struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				Source string `json:"source"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scEntry.Value, &asp); err == nil && !asp.IsDeleted {
			ref.ScriptSource = asp.Data.Source
		}
	} else if !errors.Is(err, substrate.ErrKeyNotFound) {
		return ref, false, fmt.Errorf("read script %s: %w", root, err)
	}
	ref.Script = newCompiledScript(ref.ScriptSource)

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

// ClassForCommand resolves an operationType to the canonicalName of the single
// vertexType DDL that admits it (Contract #2 §2.1 operationType→class reverse
// index). It returns ok=false when the op is admitted by no vertexType DDL, or
// by more than one (ambiguous — never guessed; the caller falls through to the
// explicit-class requirement). It is the engine-optional `class` fallback: a
// dispatched op that omits `class` resolves its DDL here.
func (c *DDLCache) ClassForCommand(operationType string) (string, bool) {
	if operationType == "" {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	name, ok := c.byCommand[operationType]
	return name, ok
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
	// The ambiguity guard is GLOBAL (it counts how many vertexType DDLs admit
	// each op across the whole set), so a single-entry edit can change which
	// ops are unambiguous — rebuild the whole reverse index from byName.
	c.byCommand = buildByCommand(c.byName, c.logger)
	// Log a meaningful canonicalName: the freshly-loaded name when present, else
	// the prior name on the delete/tombstone path (ref.CanonicalName is empty when
	// the entry is gone, which would otherwise log a useless empty string).
	loggedName := ref.CanonicalName
	if !ok && hadPrior {
		loggedName = priorName
	}
	c.logger.Info("ddl cache: invalidated",
		"metaKey", metaRootKey, "canonicalName", loggedName, "present", ok)
	return nil
}

// Size returns the number of cached entries (for tests and metrics).
func (c *DDLCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byName)
}

// buildByCommand constructs the operationType→canonicalName reverse index from
// the canonicalName→ref map (Contract #2 §2.1). Only vertexType DDLs are
// considered (the script-bearing class owners): an aspectType DDL lists an op in
// permittedCommands only as a step-6 write gate, never as the op's executing
// script, so it must not be a class-inference target. The ambiguity guard is
// global: an op admitted by two-or-more vertexType DDLs is dropped from the
// index (left requiring an explicit class) rather than resolved to an arbitrary
// one — fail closed, never guess the wrong script.
//
// kindEmpty (shadow-keyed test fixtures with no precise meta class) is treated
// as vertexType-eligible: those fixtures ARE the script-bearing DDL in the tests
// that rely on them, and aspectType fixtures that should be excluded carry the
// explicit "aspectType" kind.
func buildByCommand(byName map[string]MetaVertexRef, logger *slog.Logger) map[string]string {
	// First pass: count how many vertexType-eligible DDLs admit each op and
	// record the (single) owner seen. A count > 1 marks the op ambiguous.
	type claim struct {
		owner string
		count int
	}
	claims := map[string]*claim{}
	for name, ref := range byName {
		if !commandIndexEligible(ref.Kind) {
			continue
		}
		// A DDL with no PermittedCommands admits no specific operationType (an
		// empty list is the unrestricted/permissive default, not a command
		// source), so it contributes nothing to an operationType->class index.
		// Skip it before the empty-Kind notice — otherwise every command-less
		// meta-vertex (lens/index/role DDLs whose Kind is not meta.ddl.*) trips
		// the notice on each cache build despite adding nothing.
		if len(ref.PermittedCommands) == 0 {
			continue
		}
		// An empty Kind is eligible only as a test affordance (shadow-keyed
		// fixtures ARE the executing DDL in the tests that seed them). In
		// production every command-owning DDL declares a precise meta class
		// (meta.ddl.vertexType etc.), so an empty Kind on a command-owning DDL
		// means a malformed / unrecognized-class meta-vertex that should not
		// silently become a class-inference target. Log a WARNING so the drift
		// is visible rather than indexing it blind.
		if ref.Kind == "" && logger != nil {
			logger.Warn("ddl cache: indexing an empty-Kind DDL for class inference (expected only for shadow-keyed test fixtures; a malformed meta-vertex in production)",
				"canonicalName", name, "permittedCommands", ref.PermittedCommands)
		}
		for _, cmd := range ref.PermittedCommands {
			if cmd == "" {
				continue
			}
			c, ok := claims[cmd]
			if !ok {
				claims[cmd] = &claim{owner: name, count: 1}
				continue
			}
			c.count++
		}
	}
	byCommand := make(map[string]string, len(claims))
	for cmd, c := range claims {
		if c.count > 1 {
			// Ambiguous across vertexType DDLs — do not index; the op must carry
			// an explicit class. Logged so an accidental collision is visible.
			if logger != nil {
				logger.Warn("ddl cache: operationType admitted by multiple vertexType DDLs; not indexing for class inference",
					"operationType", cmd, "vertexTypeDDLs", c.count)
			}
			continue
		}
		byCommand[cmd] = c.owner
	}
	return byCommand
}

// commandIndexEligible reports whether a DDL of the given Kind owns the script
// that executes its permittedCommands (and so may seed the operationType→class
// reverse index). vertexType DDLs own their op scripts; aspectType DDLs carry
// declaration-only scripts and list ops solely as step-6 write gates. An empty
// Kind (shadow-keyed fixtures) is eligible — such a fixture is the executing DDL
// in the tests that use it.
func commandIndexEligible(kind string) bool {
	return kind == "vertexType" || kind == ""
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
