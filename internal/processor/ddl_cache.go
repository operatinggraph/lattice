package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

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

// errMetaDocumentUntrusted marks a meta-vertex document the cache successfully
// read and could not parse. Both failures refuse the refresh, so this is not a
// fork in the OUTCOME; it is the line that decides what is worth retrying.
// Unparseable bytes are a durable fact about the bucket — the same bytes decode
// the same way on every attempt — so spending the read budget on them buys
// nothing but latency, while a read that simply did not answer says nothing
// about the document and is exactly what the budget is for. Callers test for it
// with errors.Is; every other error out of the meta loaders is a read failure.
var errMetaDocumentUntrusted = errors.New("meta vertex document is unparseable")

// The bounded read budget EVERY Core KV read in this file spends before an
// unanswered read is taken at its word.
//
// It exists because substrate's KV calls are bare single-shot requests with no
// retry of their own, and one Refresh makes a key listing plus one read per meta
// root: without a budget, a single dropped request decides whether the Processor
// starts. With it, a blip costs milliseconds and a genuinely unreachable Core KV
// still refuses — the correct outcome for the sole writer to that bucket.
//
// The LISTING is on the budget for the sharpest form of that reason. It is the
// first read a refresh makes and it returns before any per-root read runs, so
// one dropped request there does not cost one meta-vertex, it costs the entire
// scan — the one read the refresh has no way to survive.
//
// Three attempts and 50ms between them: small enough that a whole scan of a few
// hundred roots against a healthy bucket pays nothing (the budget is spent only
// on the error path), and large enough to cross a reconnect. The delay is not
// synchronization — nothing is being waited FOR — it is spacing, so the retry
// does not simply re-send into the same closed window.
const (
	metaReadAttempts   = 3
	metaReadRetryDelay = 50 * time.Millisecond
)

// retryMetaRead runs read until it answers, the budget is spent, or ctx ends,
// and reports how many attempts it took so the caller can say so.
//
// An absent key is an ANSWER and returns immediately: ErrKeyNotFound is how
// every meta loader learns that an optional aspect is not declared, and
// retrying it would spend the budget on the single most common outcome in the
// scan.
//
// Generic over the read's result so the listing and the per-key gets share ONE
// budget rather than one each — two loops would be two places to change the
// posture, and this file has already learned what an exempt read costs.
//
// Separated from the KV calls it wraps so the budget is testable without a
// substrate that can be made to fail on command: the loop is the mechanism, and
// a stub that fails a chosen number of times exercises exactly it.
func retryMetaRead[T any](ctx context.Context, attempts int, delay time.Duration, read func() (T, error)) (T, int, error) {
	var zero T
	var err error
	for attempt := 1; ; attempt++ {
		var got T
		got, err = read()
		if err == nil {
			return got, attempt, nil
		}
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return zero, attempt, err
		}
		// A dead context cannot be outwaited, and the next attempt would fail
		// on it rather than on the bucket.
		if attempt >= attempts || ctx.Err() != nil {
			return zero, attempt, err
		}
		select {
		case <-ctx.Done():
			return zero, attempt, err
		case <-time.After(delay):
		}
	}
}

// noteRetry reports a read that only succeeded because the budget was there.
// That is the state one step before the refresh that refuses to boot, and
// nothing else in the log would show it.
func (c *DDLCache) noteRetry(target string, attempts int, err error) {
	if attempts > 1 && err == nil {
		c.logger.Warn("ddl cache: core kv read succeeded only after a retry",
			"target", target, "attempts", attempts)
	}
}

// listCoreKeys is Refresh's opening read: the full Core KV key listing every
// meta root is derived from.
func (c *DDLCache) listCoreKeys(ctx context.Context) ([]string, error) {
	keys, attempts, err := retryMetaRead(ctx, metaReadAttempts, metaReadRetryDelay, func() ([]string, error) {
		return c.conn.KVListKeys(ctx, c.coreBucket)
	})
	c.noteRetry("key listing", attempts, err)
	return keys, err
}

// readMetaKey is the one Core KV read every meta-vertex loader in this file
// makes. Routing them all through it is what makes the budget a property of the
// CACHE rather than of whichever loader remembered to retry.
func (c *DDLCache) readMetaKey(ctx context.Context, key string) (*substrate.KVEntry, error) {
	entry, attempts, err := retryMetaRead(ctx, metaReadAttempts, metaReadRetryDelay, func() (*substrate.KVEntry, error) {
		return c.conn.KVGet(ctx, c.coreBucket, key)
	})
	c.noteRetry(key, attempts, err)
	return entry, err
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

	// invalidateMu serializes Invalidate calls with each other so the cache
	// lock never has to be held across a KV read. One writer at a time is what
	// makes "read the prior state, then mutate" safe without freezing every
	// reader for the duration of a budgeted retry.
	invalidateMu sync.Mutex

	mu sync.RWMutex
	// byRoot holds ONE entry per DDL meta-vertex root: that root's own
	// projection, exactly as loadMetaVertex read it. Both name-keyed views
	// below are DERIVED from it, and that is why it exists rather than being
	// redundant with them. A canonicalName can be claimed by two roots, so an
	// index keyed by the NAME cannot say which roots contribute to it — and a
	// per-root Invalidate over such an index can neither withdraw one
	// claimant's entry without dropping the other's, nor re-run the
	// arbitration a full Refresh performs. Keeping the per-root truth and
	// deriving the views makes Refresh and Invalidate the same computation
	// over the same input, which is the only way the two can be made to agree
	// by construction instead of by inspection.
	byRoot map[string]MetaVertexRef
	// byName is the canonicalName → DDL lookup the Validator and Hydrator
	// read. Derived from byRoot by indexByCanonicalName, which arbitrates a
	// name two roots claim; never written key-by-key.
	byName map[string]MetaVertexRef
	// byMetaPK maps a meta-vertex root key to the canonicalName it is served
	// under — byName's reverse, derived alongside it, so it names only the
	// roots byName actually answers for.
	byMetaPK map[string]string
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
	// byOpType maps an operationType to its op-meta descriptor's declared
	// optionalReads TEMPLATES (Contract #2 §2.5, "the descriptor's disposition
	// is a floor the envelope cannot raise"). Populated from the `.dispatch`
	// aspect of an op-meta vertex — a `vtx.meta.<NanoID>` whose root document
	// carries `data.operationType` (internal/pkgmgr/build.go's OpMetas loop).
	//
	// Op-metas are indexed SEPARATELY from byName rather than folded into it,
	// and that is a collision, not tidiness: an op-meta's root class is
	// `meta.ddl.vertexType` — byte-identical to a real vertexType DDL's — and
	// it carries no `.canonicalName`, which is why loadMetaVertex skips it
	// today. Giving it one would put operationTypes into the namespace DDL
	// canonical names live in, where a package naming an op after one of its
	// own types would silently shadow that type's DDL.
	//
	// A duplicate operationType is unioned rather than picked, so the index
	// holds the weakest disposition every claimant declares — see
	// floorsByOpType, which is its only writer.
	//
	// Lifetime: identical to byName and byCommand. Derived from byOpMetaRoot,
	// swapped under the write lock by Refresh and re-derived in full by
	// Invalidate. Nothing else mutates it and nothing in it outlives a
	// refresh.
	byOpType map[string][]string
	// byOpMetaRoot holds ONE entry per op-meta root: the operationType that
	// root claims and the optionalReads templates its own `.dispatch`
	// declares. byRoot's counterpart for the descriptor index, and byOpType is
	// derived from it for the sharper of byRoot's two reasons — byOpType's
	// value is a UNION over claimants, so an index holding only the union can
	// add a contributor but can never subtract one, and a withdrawn descriptor
	// would keep flooring its operation until the next process restart.
	//
	// Keying it by ROOT also answers the question a tombstone destroys: a
	// tombstoned root can no longer say which operationType it carried, and
	// this map still can.
	byOpMetaRoot map[string]opMetaDescriptor

	// degraded latches the fact that an Invalidate failed, with the moment it
	// first did, the root it failed on, and why. Invalidate is the ONLY thing
	// that carries a committed meta-vertex into this process after startup, so
	// once one has failed the indexes above are no longer known to match what
	// Core KV durably holds — and an absence in them stops being evidence that
	// the DDL does not exist.
	//
	// It is per-CACHE rather than per-root, and Invalidate never clears it,
	// because a per-root record could not answer the question consumers
	// actually ask. The failed root's canonicalName is precisely what was
	// never read, so nothing can say which class's absence that failure
	// explains; only "this cache cannot vouch for itself" is a claim the
	// failure supports. A later root's successful Invalidate proves Core KV
	// answers again — it does not load the root that was missed — so the only
	// event that re-establishes full trust is a fresh Refresh: a full rescan
	// that either loads every root cleanly (and Refresh clears this latch) or
	// fails outright (§ its own widening comment), never a partial answer.
	// Today Refresh runs only at construction, so that event is a restart; a
	// future periodic Refresh (§8.2's named revisit) inherits the same clear
	// for free.
	degraded      bool
	degradedSince time.Time
	degradedKey   string
	degradedErr   error
	// degradedStaleRoots maps a root whose Invalidate failed to the
	// canonicalName its SURVIVING entry is still served under. The latch above
	// answers "an absence may be a lie"; this answers the sharper question an
	// absence cannot raise — a failing Invalidate returns before touching
	// byRoot, so an existing root's OLD entry survives intact, and a package
	// upgrade flipping that DDL's `sensitive` to true (Contract #8 §8.1 keeps
	// the meta-vertex NanoID across upgrades, so this is an UPDATE to a root
	// already in the index) leaves Lookup answering ok=true with the stale
	// `false`.
	//
	// Keyed by ROOT although consumers ask by NAME, for the two things only the
	// root can settle. A name two roots claim is stranded by whichever of them
	// failed, so one root's later success must not clear a flag the other still
	// owns; and a root whose canonicalName CHANGED between the failed and the
	// successful load would, under name-keying, leave the old name flagged
	// forever with nothing left able to name it. Accumulated across failures
	// rather than replaced: a second failed root does not make the first one's
	// entry fresh again.
	degradedStaleRoots map[string]string
}

// opMetaDescriptor is one op-meta root's own contribution to an operation's
// read-disposition floor (Contract #2 §2.5). Per-root and never merged in
// place: the merge lives in floorsByOpType, over the whole set, so every
// rebuild sees exactly the claimants that exist at that moment.
type opMetaDescriptor struct {
	operationType string
	optionalReads []string
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
		conn:         conn,
		coreBucket:   coreBucket,
		logger:       logger,
		byRoot:       map[string]MetaVertexRef{},
		byName:       map[string]MetaVertexRef{},
		byMetaPK:     map[string]string{},
		byCommand:    map[string]string{},
		byOpType:     map[string][]string{},
		byOpMetaRoot: map[string]opMetaDescriptor{},
	}
}

// Refresh rebuilds the cache from a full scan of Core KV's `vtx.meta.>`.
// Idempotent. Safe to call repeatedly; concurrent calls are serialized
// by the cache mutex (only one rebuild proceeds at a time).
func (c *DDLCache) Refresh(ctx context.Context) error {
	keys, err := c.listCoreKeys(ctx)
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

	byRoot := map[string]MetaVertexRef{}
	byOpMetaRoot := map[string]opMetaDescriptor{}
	for root, members := range metaKeys {
		// Op-metas and DDLs share the `vtx.meta.>` keyspace and the
		// `meta.ddl.vertexType` class, so both loaders are offered every root
		// and each recognizes its own shape. An op-meta is never also a DDL:
		// loadOpMetaDispatch requires `data.operationType`, which no DDL root
		// carries.
		opType, optional, isOpMeta, err := c.loadOpMetaDispatch(ctx, root)
		if err != nil {
			// A root that did not load refuses the WHOLE refresh — a cache
			// built from a partial scan must not become the quiet state.
			//
			// Skipping the root is the tempting answer and it is the wrong one
			// for every caller Refresh has, in a direction this file already
			// names. Each of the three builds a cache it then TRUSTS, and none
			// of them re-scans:
			//
			//   - The Processor's commit path builds it once at construction,
			//     so a root dropped there stays dropped for the process
			//     lifetime unless something happens to Invalidate that exact
			//     key. And the drop is not confined to the descriptor — the
			//     read that failed is the ROOT read every meta loader starts
			//     from, so the root is lost as a DDL too. A `ssn` aspect DDL
			//     missing from the cache does not reject writes: step 4 admits
			//     the op on its vertex class, step 6.5 resolves the aspect's
			//     class, misses, and commits the aspect as PLAINTEXT.
			//     permittedCommands, custody and the abstract marker go
			//     permissive by the same route.
			//   - `lattice capability` and Loupe's review handler each build a
			//     ONE-SHOT cache to answer IsSensitiveAspect at approve time —
			//     the freshness re-check that exists because the record-time
			//     verdict may be stale. A dropped root there answers `false`
			//     for a class that IS sensitive, which is a security re-check
			//     failing open at the exact moment it is being relied on.
			//
			// So the widening is not merely tolerable for the two non-boot
			// callers, it is what they needed most: both already propagate this
			// error, and refusing to answer beats answering `not sensitive`.
			//
			// Transience is answered where transience lives — readMetaKey
			// spends a bounded retry budget on a read that did not answer — not
			// by lowering what an unanswered read means once the budget is
			// spent. A Processor that cannot read Core KV must not start: it is
			// the sole writer to it, and starting half-blind writes state no
			// later refresh can take back.
			return fmt.Errorf("ddl cache: refresh: read meta vertex %s: %w", root, err)
		}
		if isOpMeta {
			byOpMetaRoot[root] = opMetaDescriptor{operationType: opType, optionalReads: optional}
			continue
		}
		ref, ok, err := c.loadMetaVertex(ctx, root, members)
		if err != nil {
			// Same rule, same reason: this loader's aspect reads decide
			// sensitivity, custody, permittedCommands and the script, and each
			// of them fails OPEN when the meta-vertex is absent from the cache.
			// A DDL whose `.sensitive` read could not be satisfied is a class
			// that stops being encrypted, not a class that merely stops being
			// looked up.
			return fmt.Errorf("ddl cache: refresh: load meta vertex %s: %w", root, err)
		}
		if !ok {
			continue
		}
		byRoot[root] = ref
	}

	byName, byPK := indexByCanonicalName(byRoot, c.logger)
	c.mu.Lock()
	c.byRoot = byRoot
	c.byName = byName
	c.byMetaPK = byPK
	c.byCommand = buildByCommand(byName, c.logger)
	c.byOpMetaRoot = byOpMetaRoot
	c.byOpType = floorsByOpType(byOpMetaRoot, c.logger)
	// Reaching this line means every root loaded cleanly (any loader error
	// above returns first, per the widening comment) — the exact event the
	// degraded latch's own doc names as what re-establishes full trust. A
	// future periodic Refresh must not keep refusing writes for a staleness
	// this rebuild just resolved.
	c.degraded = false
	c.degradedSince = time.Time{}
	c.degradedKey = ""
	c.degradedErr = nil
	c.degradedStaleRoots = nil
	c.mu.Unlock()

	c.logger.Info("ddl cache: refreshed", "entries", len(byName), "opDescriptors", len(byOpMetaRoot))
	return nil
}

// loadOpMetaDispatch reads root as an OP-META and returns its operationType
// plus the optionalReads TEMPLATES its `.dispatch` aspect declares. Reports
// (_, _, false, nil) for any root that is not an op-meta, which is every DDL
// meta-vertex and every pane.
//
// The discriminator is `data.operationType` on the root document. It is the
// only field an op-meta root carries and no DDL root carries one
// (internal/pkgmgr/build.go: a DDL root's data is abstractDDLRootData, an
// op-meta root's is {"operationType": ...}), so the two shapes cannot be
// confused even though they share a class.
//
// A tombstoned root, or a tombstoned `.dispatch`, reads as ABSENT — the same
// rule loadMetaVertex states for canonicalName/permittedCommands, and it binds
// here for the same reason: an in-place upgrade keeps the meta-vertex's NanoID
// (Contract #8 §8.1), so a package that stops emitting `.dispatch` gets it
// TOMBSTONED rather than removed. Reading a tombstoned dispatch as live would
// keep applying a withdrawn floor forever.
//
// An op-meta with no `.dispatch`, or a dispatch with no optionalReads, is
// still reported found with an empty template list. That is a real answer —
// "this descriptor declares no optional floor" — and it is not the same as
// having no descriptor at all.
//
// Errors come in the two kinds errMetaDocumentUntrusted separates, and callers
// are expected to test for it: an unparseable document is a durable fact about
// the bucket, a failed KVGet is a fact about this attempt.
func (c *DDLCache) loadOpMetaDispatch(ctx context.Context, root string) (operationType string, optionalReads []string, ok bool, err error) {
	rootEntry, err := c.readMetaKey(ctx, root)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return "", nil, false, nil
		}
		return "", nil, false, fmt.Errorf("read op-meta root %s: %w", root, err)
	}
	var rootDoc struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			OperationType string `json:"operationType"`
		} `json:"data"`
	}
	if uerr := json.Unmarshal(rootEntry.Value, &rootDoc); uerr != nil {
		return "", nil, false, fmt.Errorf("op-meta root %s: %w: %w", root, errMetaDocumentUntrusted, uerr)
	}
	if rootDoc.IsDeleted || rootDoc.Data.OperationType == "" {
		return "", nil, false, nil
	}

	dispatchEntry, derr := c.readMetaKey(ctx, root+".dispatch")
	if derr != nil {
		if errors.Is(derr, substrate.ErrKeyNotFound) {
			return rootDoc.Data.OperationType, nil, true, nil
		}
		return "", nil, false, fmt.Errorf("read dispatch %s: %w", root, derr)
	}
	var asp struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			OptionalReads []string `json:"optionalReads"`
		} `json:"data"`
	}
	if uerr := json.Unmarshal(dispatchEntry.Value, &asp); uerr != nil {
		return "", nil, false, fmt.Errorf("dispatch %s: %w: %w", root, errMetaDocumentUntrusted, uerr)
	}
	if asp.IsDeleted {
		return rootDoc.Data.OperationType, nil, true, nil
	}
	return rootDoc.Data.OperationType, asp.Data.OptionalReads, true, nil
}

// DispatchOptionalReads returns the optionalReads templates the descriptor for
// operationType declares, and whether a descriptor was found at all. The two
// answers are distinct: (nil, true) is a descriptor declaring no optional
// floor, (nil, false) is an operation with no descriptor — most of the corpus.
func (c *DDLCache) DispatchOptionalReads(operationType string) ([]string, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	templates, ok := c.byOpType[operationType]
	return templates, ok
}

// loadMetaVertex assembles a MetaVertexRef for one meta-vertex root.
// Returns (_, false, nil) when the meta-vertex does not declare a
// canonicalName (cannot be looked up — skip silently).
func (c *DDLCache) loadMetaVertex(ctx context.Context, root string, _ []string) (MetaVertexRef, bool, error) {
	ref := MetaVertexRef{MetaVertexKey: root}

	// Read the root vertex to derive Kind.
	rootEntry, err := c.readMetaKey(ctx, root)
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
	// Classified as an untrusted DOCUMENT, not a failed read, and the two
	// decoders make that distinction load-bearing rather than cosmetic: this
	// one binds `class` as a string where loadOpMetaDispatch ignores the field
	// entirely, so a root carrying `{"class":123,…}` decodes cleanly there and
	// fails only here. Left unmarked it would be a durable defect spending the
	// read budget on every attempt and reported as though the bucket were
	// unreachable.
	if err := json.Unmarshal(rootEntry.Value, &rootDoc); err != nil {
		return ref, false, fmt.Errorf("root %s: %w: %w", root, errMetaDocumentUntrusted, err)
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
	if cnEntry, err := c.readMetaKey(ctx, root+".canonicalName"); err == nil {
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
	if pcEntry, err := c.readMetaKey(ctx, root+".permittedCommands"); err == nil {
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
	if sEntry, err := c.readMetaKey(ctx, root+".sensitive"); err == nil {
		var asp struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				// A pointer, not bool: the only legitimate writer
				// (pkgmgr.emitDDLMutations, internal/pkgmgr/build.go) never
				// creates this aspect for the false case at all — absence IS
				// how "not sensitive" is encoded, so a PRESENT aspect whose
				// value is missing/null is exactly as undecidable as one that
				// fails to parse, never a shape the honest install path
				// produces. Only an explicit JSON `false` counts as a
				// deliberate live declaration.
				Value *bool `json:"value"`
			} `json:"data"`
		}
		if err := json.Unmarshal(sEntry.Value, &asp); err == nil && asp.Data.Value != nil {
			if asp.IsDeleted && *asp.Data.Value {
				c.logger.Warn("ddl cache: sensitive aspect tombstoned but still declares true; withdrawal not honored, class stays sensitive",
					"key", root+".sensitive")
			}
			ref.Sensitive = *asp.Data.Value
		} else {
			// Unparseable OR present-but-no-explicit-value: both poison
			// Sensitive to true rather than leaving it at its zero value,
			// mirroring the custody reader's poison-on-unparseable posture
			// below (and the same asymmetry the tombstone comment above
			// states: staying sensitive can only OVER-protect, so an
			// undecidable value resolves the safe way).
			reason := "unparseable"
			if err == nil {
				reason = "missing/null data.value"
			}
			c.logger.Warn("ddl cache: undecidable sensitive aspect; failing closed toward sensitive",
				"key", root+".sensitive", "reason", reason, "error", err)
			ref.Sensitive = true
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
	if cEntry, err := c.readMetaKey(ctx, root+".custody"); err == nil {
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
	if scEntry, err := c.readMetaKey(ctx, root+".script"); err == nil {
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
		// No caller reaches this today (step 8 filters on the `vtx.meta.>`
		// prefix before invalidating), and it latches anyway: whatever key this
		// was, a commit asked to be reflected in the cache and was not, and a
		// future caller that got its filter wrong must not be able to open the
		// same silent divergence the two paths below are latched against.
		ierr := fmt.Errorf("ddl cache: invalidate: key %q is not a meta-vertex key", metaRootKey)
		c.markDegraded(metaRootKey, ierr)
		return ierr
	}

	// Serialize invalidations against each other, and hold the CACHE lock only
	// around the index mutation — the shape Refresh already has (scan
	// lock-free, swap under the lock).
	//
	// Both halves are load-bearing. Holding c.mu across the reads would put a
	// budgeted retry — up to eight reads, each able to spend the full backoff —
	// inside the write lock, and every Lookup / ClassForCommand on the step-4
	// and step-6 hot paths would block on RLock for the duration; a flapping
	// Core KV during a package's meta commits would stall the commit path for
	// most of a second. Dropping the lock without this mutex would instead
	// reopen the TOCTOU it was taken for: two concurrent Invalidates on one
	// root could interleave and let the older read's document win.
	//
	// Refresh is not held to this mutex, and does not need to be: it is a
	// construction-time full rebuild that publishes one whole cache under c.mu.
	c.invalidateMu.Lock()
	defer c.invalidateMu.Unlock()

	// Op-metas share this keyspace, so the same root may be one. Its floor is
	// re-loaded here for the reason the DDL entry is: a meta-commit that
	// retires a `.dispatch` aspect must stop the floor being applied without
	// waiting for the next full Refresh.
	opType, optional, isOpMeta, oerr := c.loadOpMetaDispatch(ctx, metaRootKey)
	if oerr != nil {
		// Both error kinds are returned here, unlike Refresh: this is a
		// single-root path with a caller (the committer, at step 8) that can
		// see and report the failure, so there is nothing to gain by
		// swallowing a read that did not answer and re-deriving the floor
		// around a root whose current shape is unknown.
		//
		// The commit this invalidation followed is already durable, so the caller
		// cannot undo it and does not retry — which is what makes the latch, not
		// the returned error, the lasting record of the divergence.
		ierr := fmt.Errorf("ddl cache: invalidate op-meta %s: %w", metaRootKey, oerr)
		c.markDegraded(metaRootKey, ierr)
		return ierr
	}

	// A TOMBSTONED root can no longer say which operationType it carried, which
	// is why the prior claim is read from the index rather than the document.
	// Safe to read here and act on below: invalidateMu makes this goroutine the
	// only writer for the whole call.
	c.mu.RLock()
	priorOp, hadPriorOp := c.byOpMetaRoot[metaRootKey]
	c.mu.RUnlock()

	// Every write below lands on a PER-ROOT map and is followed by a full
	// re-derivation of the aggregate views. That is what makes a single-root
	// invalidate land on exactly what a full Refresh would produce from the
	// same KV state, for the three aggregates that are not a function of one
	// root alone: byName (a canonicalName two roots claim), byCommand (a
	// global ambiguity count over every vertexType DDL) and byOpType (a union
	// over every claimant of an operationType). Re-deriving is O(meta roots)
	// on a path that runs once per DDL commit.
	if isOpMeta {
		c.mu.Lock()
		c.byOpMetaRoot[metaRootKey] = opMetaDescriptor{operationType: opType, optionalReads: optional}
		c.byOpType = floorsByOpType(c.byOpMetaRoot, c.logger)
		templates := len(c.byOpType[opType])
		c.mu.Unlock()
		c.logger.Info("ddl cache: op-meta descriptor invalidated",
			"key", metaRootKey, "operationType", opType, "optionalReads", templates)
		return nil
	}
	if hadPriorOp {
		// Was an op-meta, now tombstoned. Dropping the ROOT and re-deriving is
		// what withdraws exactly this claimant's templates: any peer still
		// claiming the operationType keeps its own floor, and an operationType
		// left with no claimant leaves the index entirely.
		c.mu.Lock()
		delete(c.byOpMetaRoot, metaRootKey)
		c.byOpType = floorsByOpType(c.byOpMetaRoot, c.logger)
		remaining := len(c.byOpType[priorOp.operationType])
		c.mu.Unlock()
		c.logger.Info("ddl cache: op-meta descriptor withdrawn",
			"key", metaRootKey, "operationType", priorOp.operationType,
			"remainingTemplates", remaining)
		return nil
	}

	ref, ok, err := c.loadMetaVertex(ctx, metaRootKey, nil)
	if err != nil {
		// The DDL entry this root would have contributed is the one a consumer
		// reading "no such class" is most likely to be missing, so the latch is
		// set here for the same reason as on the op-meta path above.
		ierr := fmt.Errorf("ddl cache: invalidate %s: %w", metaRootKey, err)
		c.markDegraded(metaRootKey, ierr)
		return ierr
	}

	c.mu.Lock()
	priorDDL, hadPriorDDL := c.byRoot[metaRootKey]
	if ok {
		c.byRoot[metaRootKey] = ref
	} else {
		delete(c.byRoot, metaRootKey)
	}
	// This root's entry has just been rebuilt from Core KV, so whatever an
	// earlier failure stranded here is stranded no longer — drop its flag, by
	// ROOT, which is what makes a renamed canonicalName withdraw the name that
	// was actually flagged rather than the one now being served. The process-wide
	// latch is deliberately NOT cleared: another root may still be unresolved,
	// and only a full Refresh can speak for the whole projection. Without this,
	// one transient blip would refuse this class's sensitive writes until a
	// restart even after its own reload proved the entry current.
	delete(c.degradedStaleRoots, metaRootKey)
	c.byName, c.byMetaPK = indexByCanonicalName(c.byRoot, c.logger)
	// The ambiguity guard is GLOBAL (it counts how many vertexType DDLs admit
	// each op across the whole set), so a single-entry edit can change which
	// ops are unambiguous — rebuild the whole reverse index from byName.
	c.byCommand = buildByCommand(c.byName, c.logger)
	c.mu.Unlock()
	// Log a meaningful canonicalName: the freshly-loaded name when present, else
	// the prior name on the delete/tombstone path (ref.CanonicalName is empty when
	// the entry is gone, which would otherwise log a useless empty string).
	loggedName := ref.CanonicalName
	if !ok && hadPriorDDL {
		loggedName = priorDDL.CanonicalName
	}
	c.logger.Info("ddl cache: invalidated",
		"metaKey", metaRootKey, "canonicalName", loggedName, "present", ok)
	return nil
}

// markDegraded latches an Invalidate failure. Called from Invalidate's error
// returns, where invalidateMu is held and c.mu is free — the same
// invalidateMu-then-mu order every other write in that method takes, so it adds
// no new lock ordering.
//
// The two halves of the state are recorded on different rules. The onset
// (timestamp, root, error) is FIRST-failure-wins: it answers "how long has this
// process been serving a cache that cannot vouch for itself", which only the
// first failure dates, and letting a later one overwrite it would silently
// shorten that window. The possibly-stale name set ACCUMULATES: each failure
// strands a different entry, and none of them is refreshed by the next.
//
// The stale name is read from byRoot here rather than passed in, because the
// entry that is about to go stale is by definition the one this failed load was
// replacing — the caller has nothing better to say. A root with no prior entry
// contributes no name: nothing is stale, the entry is simply missing, which is
// what the latch itself already covers.
func (c *DDLCache) markDegraded(metaRootKey string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	staleName := c.byRoot[metaRootKey].CanonicalName
	if staleName != "" {
		if c.degradedStaleRoots == nil {
			c.degradedStaleRoots = map[string]string{}
		}
		c.degradedStaleRoots[metaRootKey] = staleName
	}
	if c.degraded {
		return
	}
	c.degraded = true
	c.degradedSince = time.Now()
	c.degradedKey = metaRootKey
	c.degradedErr = err
	if c.logger != nil {
		c.logger.Error("ddl cache: degraded — an invalidation failed and the cache no longer matches committed Core KV",
			"metaKey", metaRootKey, "staleCanonicalName", staleName, "error", err)
	}
}

// Degraded reports whether an Invalidate has failed in this process and, if so,
// when it first failed, on which meta root, and why.
//
// It is what separates the two answers Lookup collapses into one. A miss from a
// cache that is NOT degraded is Contract #1 §1.6's permissive default — the
// class is genuinely ungoverned. A miss from a degraded cache carries no such
// claim: the declaration may exist and simply never have reached this process,
// which is why step 6.5 refuses to commit an unresolved class as plaintext
// while this holds. Invalidate never clears it; only a fresh Refresh does —
// see the field doc.
func (c *DDLCache) Degraded() (bool, time.Time, string, error) {
	if c == nil {
		return false, time.Time{}, "", nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.degraded, c.degradedSince, c.degradedKey, c.degradedErr
}

// PossiblyStale reports whether canonicalName is served from an entry a failed
// Invalidate may have left behind its committed document.
//
// It is the half of the degraded state a miss cannot express. Degraded says a
// LOOKUP MISS is no longer evidence of absence; this says a lookup HIT is no
// longer evidence of currency — the failed reload was of this very entry, so
// what it serves is whatever was true before the commit it never read. A hit on
// any other name is untouched by that failure and stays fully trustworthy,
// which is what keeps the fail-closed reaction narrow.
//
// The scan is over the FAILED roots, not over the cache: it is bounded by how
// many invalidations have failed in this process — normally none, and a handful
// in the state this exists for.
func (c *DDLCache) PossiblyStale(canonicalName string) bool {
	if c == nil || canonicalName == "" {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, name := range c.degradedStaleRoots {
		if name == canonicalName {
			return true
		}
	}
	return false
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

// unionTemplates merges two template lists, order-stable and duplicate-free.
// The union is what makes a duplicate operationType deterministic: map
// iteration order cannot change the answer, and no claimant's floor is lost.
func unionTemplates(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, t := range list {
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}

// floorsByOpType derives the operationType→floor index from the per-root
// descriptors. It is the ONLY writer of byOpType, and both Refresh and
// Invalidate hand it the whole root set, so the two cannot disagree: a
// withdrawal is a root leaving the input, and the answer follows.
//
// A duplicate operationType is UNIONED, never picked. Unlike buildByCommand's
// ambiguity guard — which drops a candidate and thereby fails CLOSED, because
// losing a class inference rejects the op — dropping a floor fails OPEN. The
// union is the safe direction: it demotes every key any claimant declares
// optional, which can only widen absence-tolerance, never harden a key back.
//
// Roots are visited in key order so the union's ORDER, not merely its
// membership, is a function of the root set alone. Map iteration would leave a
// two-claimant floor spelled differently on each rebuild, which is exactly the
// kind of difference that makes "did this invalidate land where a refresh
// would?" unanswerable by comparison.
//
// pkgmgr refuses a duplicate at install (Installer's op-meta operationType
// collision check) and validateOpMetas refuses one within a single package, so
// the union is a recovery from state that predates those gates or was written
// around them, not a supported way to express two descriptors for one op.
func floorsByOpType(byOpMetaRoot map[string]opMetaDescriptor, logger *slog.Logger) map[string][]string {
	out := make(map[string][]string, len(byOpMetaRoot))
	claimedBy := make(map[string]string, len(byOpMetaRoot))
	for _, rootKey := range slices.Sorted(maps.Keys(byOpMetaRoot)) {
		d := byOpMetaRoot[rootKey]
		prior, dup := out[d.operationType]
		if !dup {
			out[d.operationType] = d.optionalReads
			claimedBy[d.operationType] = rootKey
			continue
		}
		if logger != nil {
			logger.Warn("ddl cache: duplicate op-meta descriptor; unioning the floors",
				"operationType", d.operationType, "key", rootKey, "alsoClaimedBy", claimedBy[d.operationType],
				"priorTemplates", len(prior), "addedTemplates", len(d.optionalReads))
		}
		out[d.operationType] = unionTemplates(prior, d.optionalReads)
	}
	return out
}

// indexByCanonicalName derives the canonicalName-keyed views from the per-root
// DDL projections. It is the ONLY writer of byName and byMetaPK, and both
// Refresh and Invalidate hand it the whole root set, for the reason
// floorsByOpType states: a name TWO roots claim is arbitrated over the set, so
// a rebuild driven by one root cannot reach an index a full rebuild would not.
//
// The arbitration is by root key, ascending — a property of the set rather than
// of the order the roots were visited in. That is what lets a winner's
// withdrawal hand the name to the remaining claimant instead of deleting a
// still-claimed DDL, and what stops two rebuilds of one KV state disagreeing
// about which meta-vertex a class resolves to. The loser is left out of BOTH
// views, so LookupByMetaKey reports it absent rather than answering for it with
// the winner's ref.
func indexByCanonicalName(byRoot map[string]MetaVertexRef, logger *slog.Logger) (map[string]MetaVertexRef, map[string]string) {
	byName := make(map[string]MetaVertexRef, len(byRoot))
	byPK := make(map[string]string, len(byRoot))
	for _, rootKey := range slices.Sorted(maps.Keys(byRoot)) {
		ref := byRoot[rootKey]
		if existing, dup := byName[ref.CanonicalName]; dup {
			if logger != nil {
				logger.Warn("ddl cache: duplicate canonicalName; keeping the lowest-keyed meta-vertex",
					"canonicalName", ref.CanonicalName,
					"kept", existing.MetaVertexKey,
					"dropped", ref.MetaVertexKey)
			}
			continue
		}
		byName[ref.CanonicalName] = ref
		byPK[ref.MetaVertexKey] = ref.CanonicalName
	}
	return byName, byPK
}
