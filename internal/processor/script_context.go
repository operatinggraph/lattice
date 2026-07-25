package processor

import (
	"context"
	"fmt"
)

// ScriptContext is the full input the Processor makes available to a
// Starlark script at commit step 5. It is built by step 4 (Hydrate) and
// passed verbatim into the Starlark runner.
//
// Field roles (Contract #2 §2.5 + Contract #1 §1.5 + Contract #3 §3.1):
//   - Operation: the full envelope the Processor consumed. Exposed to the
//     script as the `op` global struct.
//   - Hydrated: vertex/aspect documents pre-fetched per `contextHint.reads`
//     plus the present `contextHint.optionalReads` keys. Exposed as the
//     `state` global dict (key -> struct). Absent optionalReads keys land in
//     KnownAbsent instead (never in `state`; kv.Read serves them as None), and
//     absent `reads`/`egressReads` keys land in RequiredAbsent (never in
//     `state`; touching one faults HydrationMiss).
//   - DDLLookup: DDL meta-vertices keyed by canonicalName (e.g.,
//     "identity"). Exposed as the `ddl` global dict. Populated from the
//     DDL cache built at startup and refreshed on DDL mutations.
//   - ScriptSource: the Starlark source for the operation's class.
//     Internal to the runner, not exposed to the script.
//   - ScriptClass: the canonicalName of the class whose script will run.
//     Echoed in logs and errors for traceability.
//   - KVReader: the lazy on-demand Core KV read seam backing the script's
//     `kv.Read()` builtin (Contract #2 §2.5). Populated by step 4 (Hydrate)
//     with a live Core-KV-backed reader. Optional: when nil, a `kv.Read()` of a
//     key not already in Hydrated raises a script error (tests that exercise
//     contextHint-only paths may leave it unset).
type ScriptContext struct {
	Operation *OperationEnvelope
	Hydrated  map[string]VertexDoc
	// KnownAbsent records the `contextHint.optionalReads` keys that were NOT
	// found in Core KV at step 4 (Contract #2 §2.5 — the absence-tolerant
	// declared read, class (d)). A key here resolved to *known-absent at the
	// step-4 snapshot*: `kv.Read(key)` returns None from this record with NO
	// live GET, and a `create` mutation the script derives off that absence is
	// retry-attributable — if the CreateOnly commit conflicts and the key now
	// exists, the commit path re-hydrates and re-executes (the key lands in
	// Hydrated, the script re-branches no-op). Keys absent from BOTH Hydrated
	// and KnownAbsent were simply not declared — kv.Read falls through to the
	// lazy KVReader as before. Nil when the envelope declared no optionalReads.
	KnownAbsent map[string]struct{}
	// RequiredAbsent records the `contextHint.reads` / `contextHint.egressReads`
	// keys that were NOT found in Core KV at step 4. These are the fail-closed
	// declared reads (class (a)/(f)) — the opposite disposition from KnownAbsent:
	// touching one faults HydrationMiss rather than reading as None.
	//
	// The fault is raised where the operation DEPENDS on the key — a kv.Read, a
	// `state` membership access, or a mutation naming it — rather than during
	// hydration. Step 4 runs before any authorization the key is subject to, so
	// a fault there would answer "does this key exist?" for any actor holding
	// any operation grant, over any key, without a script running. An operation
	// that never touches the key cannot be affected by whether it exists, so its
	// outcome must not depend on that; one that touches it fails closed.
	RequiredAbsent map[string]struct{}
	// DeferredMiss records the first RequiredAbsent key this execution touched,
	// so the runner can raise the HydrationMiss that step 4 deferred. Shared
	// pointer (like SensitiveReads) so the `kv` builtins and the `state` mapping
	// report through the same channel. Never nil in production wiring.
	DeferredMiss *deferredMissTracker
	DDLLookup    map[string]MetaVertex
	ScriptSource string
	ScriptClass  string
	KVReader     ScriptKVReader
	// LinkLister backs the script's `kv.Links()` builtin (Contract #2 §2.5.1) —
	// the bounded, paged op-time enumeration of a hub vertex's canonical links.
	// Populated by step 4 (Hydrate) with a live Core-KV-backed lister. Optional:
	// when nil, a `kv.Links()` call raises a script error (tests that do not
	// exercise enumeration may leave it unset).
	LinkLister ScriptLinkLister
	// SensitiveReads is the shared tracker recording whether this execution
	// decrypted any sensitive aspect as plaintext, across both step 4's
	// hydration and the lazy kv.Read() seam (KVReader). Populated by step 4
	// (Hydrate); consulted by step 6's external-egress emission guard
	// (design sensitive-param-egress §3.6). Never nil in production wiring.
	SensitiveReads *sensitiveReadTracker
}

// ScriptKVReader performs a single on-demand Core KV read for a Starlark
// `kv.Read()` call — the Contract #2 §2.5 lazy read that fires when the key was
// not pre-fetched via `contextHint.reads`.
//
// Semantics:
//   - absent / hard-tombstoned (NATS not-found) → (nil, nil), so kv.Read yields
//     None and the script can branch on it (the idempotency-create pattern:
//     present → no-op, absent → create);
//   - a live OR logically-deleted vertex → a non-nil VertexDoc (the doc carries
//     isDeleted; the script inspects it, mirroring how `state` exposes deletes);
//   - any other error → propagated and surfaced as a ScriptError.
//
// Implementations perform a single key GET — never a prefix scan. kv.Read is the
// idempotency-read seam, not a read-model surface (read models are lenses, P5).
type ScriptKVReader interface {
	ReadVertex(ctx context.Context, key string) (*VertexDoc, error)
}

// ScriptLinkLister performs one bounded, paged enumeration of a hub vertex's
// canonical Core KV links for a Starlark `kv.Links()` call (Contract #2
// §2.5.1). keyFilter is the server-side subject filter the builtin constructs
// from (hubKey, relation, direction); cursor/limit page the result.
//
// Semantics:
//   - returns the currently-committed links matching keyFilter — live AND
//     logically-deleted (each LinkDoc carries isDeleted; the script decides),
//     mirroring kv.Read. Hard-deleted/absent keys simply do not appear.
//   - nextCursor is the opaque token to pass as cursor for the next page, or
//     "" when the filter is exhausted.
//   - it is NOT snapshot-isolated and NOT a serialization point: a constraint
//     over the returned set must additionally contend a shared OCC-guarded key
//     (Contract #2 §2.5.1). A single-key read that races a hard-delete between
//     the key-list and the value-read is skipped (treated as absent).
//   - any substrate error other than a per-key not-found propagates as a
//     ScriptError.
//
// Implementations read ONLY the Core KV canonical link keyspace — never the
// Refractor Adjacency KV, never a lens/read-model (P5).
type ScriptLinkLister interface {
	ListLinks(ctx context.Context, keyFilter, cursor string, limit int) (links []LinkDoc, nextCursor string, err error)
}

// LinkDoc is the hydrated form of a Core KV link envelope (Contract #1 §1.3),
// the set-valued sibling of VertexDoc. SourceVertex/TargetVertex are derived
// from the 6-segment link key (Contract #1 §1.1: source first), so they are
// always well-formed regardless of envelope-body drift. The script consumes a
// Starlark struct projection of this type (see starlark_runner.go).
type LinkDoc struct {
	Key          string
	Class        string
	IsDeleted    bool
	Data         map[string]interface{}
	Revision     uint64
	SourceVertex string
	TargetVertex string
}

// VertexDoc is the hydrated form of a Core KV vertex or aspect document.
// Contract #1 §1.2 / §1.3 — the canonical shape. The script consumes a
// Starlark struct projection of this type (see starlark_runner.go).
type VertexDoc struct {
	Key       string                 `json:"-"`
	Class     string                 `json:"class"`
	IsDeleted bool                   `json:"isDeleted"`
	Data      map[string]interface{} `json:"data,omitempty"`
	// Aspect-only fields. Empty for vertex documents.
	VertexKey string `json:"vertexKey,omitempty"`
	LocalName string `json:"localName,omitempty"`
	// Revision is the Core KV revision the document was read at during step 4
	// (Hydrate). It is the script's OCC handle: a script asserting a guarded
	// transition echoes it back as a mutation's expectedRevision so a
	// concurrent write to the same root surfaces as a RevisionConflict
	// (Contract #2 §2.6). Not part of the persisted document body — it is the
	// substrate entry's revision, threaded in by the Hydrator.
	Revision uint64 `json:"-"`
}

// MetaVertex is the DDL meta-vertex projection hydrated from the DDL cache.
// Fields are the minimum the executing script and the DDL Validator need.
type MetaVertex struct {
	Key               string   `json:"-"`
	CanonicalName     string   `json:"canonicalName"`
	PermittedCommands []string `json:"permittedCommands,omitempty"`
}

// MutationOp is the script-proposed state transition. Contract #3 §3.2.
// `Op` is one of "create", "update", "tombstone".
type MutationOp struct {
	Op               string                 `json:"op"`
	Key              string                 `json:"key"`
	Document         map[string]interface{} `json:"document,omitempty"`
	ExpectedRevision *uint64                `json:"expectedRevision,omitempty"`
}

// EventSpec is a business event the script asks to publish. Contract #3
// §3.4.
type EventSpec struct {
	Class string                 `json:"class"`
	Data  map[string]interface{} `json:"data,omitempty"`
}

// HydratedState is what step 4 (Hydrate) returns to the commit path. It
// is the assembled ScriptContext, ready to be handed to step 5 (Execute).
type HydratedState struct {
	Context ScriptContext
}

// ScriptResult is the parsed return value of step 5 (Execute). The
// commit path passes it forward to step 6 (Validate).
//
// PrimaryKey is the optional single principal Core KV key the script names
// via the closed `response` return dict (`{"primaryKey": <key>}`). It is a
// commit-trace identifier only: the commit path validates it is a member of
// the committed mutation set before surfacing it as OperationReply.PrimaryKey.
// The script cannot return any other `response` key (fail-closed at parse).
type ScriptResult struct {
	Mutations  []MutationOp
	Events     []EventSpec
	PrimaryKey string
}

// HydrationError is the typed step-4 failure surfaced when a contextHint
// key (or the DDL meta-vertex / script aspect for the operation's class)
// is missing from Core KV.
type HydrationError struct {
	Code               string // "HydrationMiss" | "NoScriptForClass"
	MissingKey         string
	OperationRequestID string
	Cause              error
}

func (e *HydrationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: requestId=%s missingKey=%s: %v",
			e.Code, e.OperationRequestID, e.MissingKey, e.Cause)
	}
	return fmt.Sprintf("%s: requestId=%s missingKey=%s",
		e.Code, e.OperationRequestID, e.MissingKey)
}

func (e *HydrationError) Unwrap() error { return e.Cause }

// ScriptError is the typed step-5 failure for any Starlark-side problem:
// script compile/runtime errors, sandbox violations (which manifest as
// resolve errors for unbound globals), and timeouts.
//
// Detail is a side-channel field used exclusively for ClaimKeyInvalid errors.
// The script encodes a specific outcome (e.g. "invalid-key", "wrong-state")
// in the fail() message; classifyStarlarkError parses it into this field.
// The commit path reads Detail for Health KV emission then strips it before
// the reply reaches the caller (NFR-S6 anti-enumeration: callers see only
// Code="ClaimKeyInvalid" with no detail).
type ScriptError struct {
	Code               string // "ScriptError" | "SandboxViolation" | "ScriptTimeout" | "InvalidReturnShape" | "ClaimKeyInvalid"
	Message            string
	Detail             string // Side-channel for ClaimKeyInvalid outcome; stripped before reply egress.
	Line               int
	Column             int
	OperationRequestID string
}

func (e *ScriptError) Error() string {
	loc := ""
	if e.Line > 0 {
		loc = fmt.Sprintf(" line=%d col=%d", e.Line, e.Column)
	}
	return fmt.Sprintf("%s: requestId=%s%s: %s",
		e.Code, e.OperationRequestID, loc, e.Message)
}
