package processor

import (
	"context"
	"errors"

	starlarklib "go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/operatinggraph/lattice/internal/starlarksandbox"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/vault"
)

// kvModule returns the Starlark `kv` global exposing a single builtin,
// `kv.Read(key)` — the Contract #2 §2.5 lazy on-demand Core KV read.
//
// §2.5 makes `contextHint.reads` a pre-fetch *optimisation, not a gate*:
//   - a key declared in contextHint.reads is hydrated at step 4 and served here
//     from the cache (state) with NO Core KV round-trip. NOTE this means a
//     declared key is read at the step-4 OCC snapshot; kv.Read CANNOT force a
//     fresher re-read of an already-hydrated key (and should not — echoing the
//     snapshot revision as expectedRevision is what makes the commit's OCC
//     check sound);
//   - a key NOT declared falls through to a single on-demand GET via sc.KVReader
//     ("incur latency", §2.5).
//
// Return value:
//   - present (live OR logically-deleted) → a struct projection identical in
//     shape to a `state` entry (key, class, isDeleted, data, revision, and the
//     aspect-only vertexKey/localName when set), so scripts read it the same way
//     they read `state[key]`. A logically-deleted vertex (isDeleted=true) is a
//     live KV envelope and reads as a PRESENT doc carrying the flag — NOT None;
//   - absent / hard-tombstoned (NATS delete/purge/TTL-expiry) → None.
//
// A script branching on existence must therefore test `v == None or
// v.isDeleted`, not just `v == None`, to treat a logically-deleted record as
// "needs (re)creating".
//
// This unlocks the read-before-create idempotency pattern that `contextHint` and
// `createIfAbsent` cannot express: a declared-but-absent contextHint key fails
// hydration *fatally* (HydrationMiss) before the script runs, so it cannot say
// "read this, tolerate absence." kv.Read tolerates absence (→ None), letting the
// script decide mutations AND events coherently in one branch.
//
// DETERMINISM: this is the ONE non-pure builtin. nanoid/crypto/time/json are all
// deterministic so a replayed (at-least-once) operation reproduces byte-identical
// output. kv.Read deliberately breaks that — it reads LIVE state, so two runs of
// the same requestId can observe different Core KV and branch differently. That
// is the POINT (Contract #10 §10.3 / design §4.3): the consumer reads current
// state to decide create-vs-no-op, and the Processor — not replay determinism —
// is the idempotency authority. The deterministic id + the CreateOnly backstop
// at commit (step 8) resolve the residual publish→commit race. Do not assume
// kv.Read is replay-stable.
//
// Latency note: each on-demand kv.Read is a NATS round-trip. The read binds
// to the execution-scoped context starlarksandbox.Execute attaches to the
// Starlark thread (starlarksandbox.ContextFromThread), which is the same
// wall-budget-bound context the script itself runs under — so a slow read
// counts against the script budget and surfaces as ScriptTimeout if it
// overruns. It is an intentional opt-in for the idempotency-read pattern —
// NOT a general scan/read-model hook (read models are lenses, P5).
func kvModule(sc ScriptContext) *starlarkstruct.Struct {
	readFn := starlarklib.NewBuiltin("Read", func(thread *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		if len(args) != 1 || len(kwargs) != 0 {
			return nil, errBuiltin("kv.Read(key) takes exactly 1 positional argument")
		}
		keyStr, ok := args[0].(starlarklib.String)
		if !ok {
			return nil, errBuiltin("kv.Read: argument must be a string, got " + args[0].Type())
		}
		key := string(keyStr)
		if key == "" {
			return nil, errBuiltin("kv.Read: key must be non-empty")
		}

		// Cache-first (§2.5). A hydrated entry is, by construction, always a
		// successful read: a contextHint.reads key that was absent in Core KV
		// fails hydration (HydrationMiss) before execution begins, and an
		// optionalReads key only lands here when it was present, so reaching
		// the script with the key in sc.Hydrated guarantees it was present.
		if doc, ok := sc.Hydrated[key]; ok {
			return vertexDocToStarlark(doc), nil
		}
		// Known-absent (§2.5 optionalReads): the key was declared
		// absence-tolerant and was NOT found at the step-4 snapshot — serve
		// None from the snapshot with NO live GET. This is what makes a
		// declared read-before-create replay-stable and OCC-coherent: the
		// script's absence branch and the commit's CreateOnly condition both
		// reflect the same step-4 observation (a racing create surfaces as
		// RevisionConflict → re-hydrate → now present → re-branch).
		if _, absent := sc.KnownAbsent[key]; absent {
			return starlarklib.None, nil
		}

		// Lazy on-demand read for a key not declared in contextHint.reads.
		if sc.KVReader == nil {
			return nil, errBuiltin("kv.Read: no Core KV reader wired for on-demand read of " + key)
		}
		doc, err := sc.KVReader.ReadVertex(starlarksandbox.ContextFromThread(thread, context.Background()), key)
		if err != nil {
			return nil, errBuiltin("kv.Read: " + err.Error())
		}
		if doc == nil {
			// Absent or hard-tombstoned — the script branches on None.
			return starlarklib.None, nil
		}
		return vertexDocToStarlark(*doc), nil
	})

	// kv.Links(hubKey, relation, direction, cursor=None, limit=N) -> (page, nextCursor)
	//
	// The ONE sanctioned relaxation of the otherwise known-key-reads-only write
	// path (Contract #2 §2.5.1): a bounded, paged, lazy enumeration of a hub
	// vertex's canonical Core KV links under `relation`, in the direction the hub
	// sits in the link. It exists so set/range guards read a vertex's neighbors
	// from links (Contract #1 §1.1) instead of denormalizing them into a key-list
	// aspect. It is NOT a serialization point — see §2.5.1: a guard enforcing a
	// constraint over the returned set must also contend a shared OCC-guarded key.
	// Like kv.Read it reads LIVE Core KV (not replay-stable, §3.5).
	linksFn := starlarklib.NewBuiltin("Links", func(thread *starlarklib.Thread, _ *starlarklib.Builtin, args starlarklib.Tuple, kwargs []starlarklib.Tuple) (starlarklib.Value, error) {
		var (
			hubKey, relation, direction string
			cursorVal                   starlarklib.Value = starlarklib.None
			limit                                         = defaultLinkPageLimit
		)
		if err := starlarklib.UnpackArgs("kv.Links", args, kwargs,
			"hubKey", &hubKey, "relation", &relation, "direction", &direction,
			"cursor?", &cursorVal, "limit?", &limit); err != nil {
			return nil, errBuiltin("kv.Links: " + err.Error())
		}

		hubType, hubID, ok := substrate.ParseVertexKey(hubKey)
		if !ok {
			return nil, errBuiltin("kv.Links: hubKey must be a 3-segment vertex key (vtx.<type>.<id>), got " + hubKey)
		}
		if !isValidLinkRelation(relation) {
			return nil, errBuiltin("kv.Links: relation must match [a-z][a-zA-Z0-9]*, got " + relation)
		}

		// Construct the server-side subject filter scoped to the hub's id in the
		// requested direction. The hub id is a fixed token either way, so the read
		// is bounded by the hub's degree in that direction, never the keyspace.
		var keyFilter string
		switch direction {
		case "out": // hub is the link SOURCE: lnk.<hubType>.<hubId>.<rel>.>
			keyFilter = substrate.LinkPrefix + "." + hubType + "." + hubID + "." + relation + ".>"
		case "in": // hub is the link TARGET: lnk.*.*.<rel>.<hubType>.<hubId>
			keyFilter = substrate.LinkPrefix + ".*.*." + relation + "." + hubType + "." + hubID
		default:
			return nil, errBuiltin(`kv.Links: direction must be "out" or "in", got ` + direction)
		}

		// cursor is optional and may be None (first page) or a non-empty string.
		cursor := ""
		switch c := cursorVal.(type) {
		case starlarklib.NoneType:
		case starlarklib.String:
			cursor = string(c)
		default:
			return nil, errBuiltin("kv.Links: cursor must be a string or None, got " + cursorVal.Type())
		}

		// Clamp the page limit: a non-positive value means "use the default"; an
		// over-large value is capped so one page can never be unbounded.
		if limit <= 0 {
			limit = defaultLinkPageLimit
		}
		if limit > maxLinkPageLimit {
			limit = maxLinkPageLimit
		}

		if sc.LinkLister == nil {
			return nil, errBuiltin("kv.Links: no Core KV link lister wired for enumeration of " + keyFilter)
		}
		links, nextCursor, err := sc.LinkLister.ListLinks(starlarksandbox.ContextFromThread(thread, context.Background()), keyFilter, cursor, limit)
		if err != nil {
			return nil, errBuiltin("kv.Links: " + err.Error())
		}

		page := starlarklib.NewList(nil)
		for _, l := range links {
			_ = page.Append(linkDocToStarlark(l))
		}
		var nextCursorValue starlarklib.Value = starlarklib.None
		if nextCursor != "" {
			nextCursorValue = starlarklib.String(nextCursor)
		}
		return starlarklib.Tuple{page, nextCursorValue}, nil
	})

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
		"Read":  readFn,
		"Links": linksFn,
	})
}

// defaultLinkPageLimit / maxLinkPageLimit bound a single kv.Links page. The
// default applies when the caller omits limit (or passes a non-positive value);
// the max caps any caller-supplied limit so one page is never unbounded. A hub
// with more matching links than the page size is enumerated across pages via
// the opaque cursor (Contract #2 §2.5.1 — paged, never silently truncated).
const (
	defaultLinkPageLimit = 256
	maxLinkPageLimit     = 1024
)

// isValidLinkRelation reports whether s is a valid link localName per Contract
// #1 §1.1 (`[a-z][a-zA-Z0-9]*`, no leading underscore/digit). Used to fail a
// kv.Links call fast before constructing a subject filter from a bad relation.
func isValidLinkRelation(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if r < 'a' || r > 'z' {
				return false
			}
			continue
		}
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// linkDocToStarlark projects a LinkDoc into the Starlark struct a script reads
// from a kv.Links page: .key, .class, .isDeleted, .data, .revision, plus the
// link-only .sourceVertex / .targetVertex (Contract #1 §1.3). The vertex-shaped
// fields mirror vertexDocToStarlark so a guard reads a link like a vertex.
func linkDocToStarlark(l LinkDoc) starlarklib.Value {
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlarklib.StringDict{
		"key":          starlarklib.String(l.Key),
		"class":        starlarklib.String(l.Class),
		"isDeleted":    starlarklib.Bool(l.IsDeleted),
		"data":         starlarksandbox.GoMapToStarlarkDict(l.Data),
		"revision":     starlarklib.MakeUint64(l.Revision),
		"sourceVertex": starlarklib.String(l.SourceVertex),
		"targetVertex": starlarklib.String(l.TargetVertex),
	})
}

// connLinkLister adapts a substrate.Conn + Core bucket to ScriptLinkLister — the
// production backing for kv.Links, wired by the Hydrator (step 4). It lists the
// hub's link keys via the server-side subject-filtered KVListKeysFilter (paged),
// then single-key-GETs each to load its envelope. SourceVertex/TargetVertex are
// derived from the link KEY (Contract #1 §1.1, source first), never trusted from
// the body. A key that races a hard-delete between list and GET is skipped
// (it left the set). Never reads the Refractor Adjacency KV or a lens — Core KV
// canonical links only (P5 / §2.5.1).
type connLinkLister struct {
	conn   *substrate.Conn
	bucket string
}

// ListLinks implements ScriptLinkLister.
func (r connLinkLister) ListLinks(ctx context.Context, keyFilter, cursor string, limit int) ([]LinkDoc, string, error) {
	keys, nextCursor, err := r.conn.KVListKeysFilter(ctx, r.bucket, keyFilter, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	links := make([]LinkDoc, 0, len(keys))
	for _, key := range keys {
		srcType, srcID, _, tgtType, tgtID, ok := substrate.ParseLinkKey(key)
		if !ok {
			// The lnk.-anchored filter should never match a non-link key; skip
			// rather than mis-parse if the keyspace ever surprises us.
			continue
		}
		entry, err := r.conn.KVGet(ctx, r.bucket, key)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				// Raced a concurrent hard-delete between the key-list and the
				// value-read — treat as absent (it is no longer in the set).
				continue
			}
			return nil, "", err
		}
		doc, err := parseLinkDoc(entry.Value, key)
		if err != nil {
			return nil, "", err
		}
		doc.Revision = entry.Revision
		doc.SourceVertex = substrate.VertexPrefix + "." + srcType + "." + srcID
		doc.TargetVertex = substrate.VertexPrefix + "." + tgtType + "." + tgtID
		links = append(links, doc)
	}
	return links, nextCursor, nil
}

// parseLinkDoc parses a Core KV link envelope into a LinkDoc. It reuses the
// vertex-envelope parser for the shared class/isDeleted/data fields; the
// caller fills SourceVertex/TargetVertex/Revision from the key + entry.
func parseLinkDoc(data []byte, key string) (LinkDoc, error) {
	vd, err := parseVertexDoc(data, key)
	if err != nil {
		return LinkDoc{}, err
	}
	return LinkDoc{Key: key, Class: vd.Class, IsDeleted: vd.IsDeleted, Data: vd.Data}, nil
}

// connKVReader adapts a substrate.Conn + Core bucket to ScriptKVReader. It is
// the production backing for kv.Read's on-demand path, wired by the Hydrator.
//
// A not-found (absent / hard-tombstoned) maps to (nil, nil) so kv.Read yields
// None; a logically-deleted vertex (isDeleted=true envelope still live, per
// Conn.KVGet) returns a non-nil doc carrying isDeleted so the script decides;
// every other error propagates. Single-key GET only — never a prefix scan.
//
// ddls/vault back decrypt-on-read (Contract #3 §3.10) for a sensitive aspect
// read lazily rather than via contextHint — the same disposition step 4
// applies to its pre-fetched keys. Both nil-safe: a reader without them
// (most test harnesses) returns the aspect's ciphertext opaque, unchanged.
//
// egressKeys/tracker carry the op's egressReads disposition to this seam too
// (§3.1 "one disposition, both read paths"): a key declared in
// contextHint.egressReads is, by construction, already served from the step-4
// Hydrated cache (kv.Read is cache-first), so this lazy path only matters for
// consistency — a key present in egressKeys but reached here anyway gets the
// same ref-if-sensitive disposition, never plaintext.
//
// requestID is the hydrating operation's request ID, threaded into an egress
// marker's MAC exactly as step 4's own egressReads seam does (design
// sensitive-ref-mac-provenance §3.2).
type connKVReader struct {
	conn       *substrate.Conn
	bucket     string
	ddls       *DDLCache
	vault      vault.Vault
	egressKeys map[string]struct{}
	tracker    *sensitiveReadTracker
	requestID  string
}

// ReadVertex implements ScriptKVReader.
func (r connKVReader) ReadVertex(ctx context.Context, key string) (*VertexDoc, error) {
	entry, err := r.conn.KVGet(ctx, r.bucket, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, err
	}
	doc, err := parseVertexDoc(entry.Value, key)
	if err != nil {
		return nil, err
	}
	doc.Revision = entry.Revision
	_, egress := r.egressKeys[key]
	if err := decryptSensitiveDoc(ctx, r.conn, r.bucket, r.ddls, r.vault, &doc, egress, r.tracker, r.requestID); err != nil {
		return nil, err
	}
	return &doc, nil
}
