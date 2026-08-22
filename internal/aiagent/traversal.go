// Package aiagent provides the client-side cold-start traversal helpers
// for Lattice-aware AI agents (FR19, FR34).
//
// Design principles:
//   - Client-side only: this package reads from NATS KV buckets directly.
//     It never extends the Processor's read surface (no new ContextHint
//     fields, no adjacency reads, no lens-output reads beyond Capability KV
//     which is the one contract-defined exception).
//   - NFR-S10: AI agents use the same Capability KV and Processor commit
//     path as human actors. Nothing in this package bypasses that contract.
//   - Zero Processor changes: the story is entirely additive.
//
// Prerequisite, before step 1: the caller loads the primordial identifier
// table (bootstrap.Load — SystemActorKeys' predicate is keyed on the
// roleOperator NanoID it carries), resolves the kernel-seeded system-actor set
// (bootstrap.SystemActorKeys — one full core-kv listing), and hands that set to
// NewTraverser. Not a detail — step 1's keys are chosen by the actor's CLASS,
// and a traverser given no set treats every actor as ordinary. Do both once at
// start-up, the way examples/hello-lattice/ai-agent.go does; this package
// neither loads that file nor lists core-kv on its own behalf.
//
// Cold-start traversal algorithm (FR19):
//  1. Agent reads its capability doc from Capability KV — the keys are
//     resolved by actor class through internal/capabilitykv (a kernel-seeded
//     system actor reads its cap.<type>.<id> anchor unioned with
//     cap.roles.<type>.<id>; every other actor reads cap.roles.<type>.<id>
//     alone).
//  2. Agent picks an operationType from platformPermissions[].
//  3. Agent calls DiscoverDDL to find the matching vtx.meta.<NanoID> by
//     enumerating vtx.meta.* keys and reading .canonicalName aspects.
//  4. Agent calls ReadDDLAspects for the seven self-description aspects
//     (description, inputSchema, outputSchema, fieldDescription, examples,
//     script, permittedCommands).
//  5. Agent constructs payload from inputSchema and submits to ops.default
//     (or appropriate lane) — same Processor commit path as any human op.
package aiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/operatinggraph/lattice/internal/capabilitykv"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// ErrDDLNotFound is returned by DiscoverDDL when no DDL meta-vertex with
// the requested canonicalName is found.
var ErrDDLNotFound = errors.New("aiagent: DDL not found for operation type")

// ErrAspectMissing is returned by ReadDDLAspects when a required
// self-description aspect is absent from the DDL meta-vertex.
//
// When the absence is caused by a missing KV key, the error also wraps
// substrate.ErrKeyNotFound; callers can use errors.Is to check for either
// sentinel independently. When caused by a tombstoned aspect envelope
// (isDeleted: true), only ErrAspectMissing is wrapped.
var ErrAspectMissing = errors.New("aiagent: required DDL aspect missing")

// ExampleEntry represents one named usage example from a DDL's .examples
// aspect (shape: {"examples": [{"name":...,"payload":...,"expectedOutcome":...}]}).
type ExampleEntry struct {
	Name            string         `json:"name"`
	Payload         map[string]any `json:"payload"`
	ExpectedOutcome string         `json:"expectedOutcome"`
}

// DDLAspects holds the seven self-description aspects of a DDL meta-vertex.
// These give an AI agent (or any traverser) full knowledge of an
// operation's purpose, input contract, output contract, per-field
// guidance, usage examples, execution script, and permitted command names.
type DDLAspects struct {
	// Description is the markdown plain-language description of the DDL.
	// Source: vtx.meta.<id>.description → data.text
	Description string

	// InputSchema is the JSON Schema string for the operation's input payload.
	// Source: vtx.meta.<id>.inputSchema → data.schema
	InputSchema string

	// OutputSchema is the JSON Schema string for the operation's response.
	// Source: vtx.meta.<id>.outputSchema → data.schema
	OutputSchema string

	// FieldDescriptions maps payload field paths to plain-language descriptions.
	// Source: vtx.meta.<id>.fieldDescription → data.fieldDescriptions
	FieldDescriptions map[string]string

	// Examples is the list of named usage examples for this DDL.
	// Source: vtx.meta.<id>.examples → data.examples
	Examples []ExampleEntry

	// Script is the Starlark source of the DDL's execute(state, op) function.
	// Source: vtx.meta.<id>.script → data.source
	Script string

	// PermittedCommands lists the operationType strings that trigger this DDL.
	// An agent constructing an operation envelope must set Class to one of
	// these values.
	// Source: vtx.meta.<id>.permittedCommands → data.commands
	PermittedCommands []string

	// Abstract reports whether this DDL declares a type that names no
	// instance (dynamic-type-taxonomy-design.md §3.2) — a taxonomy parent a
	// `:<name>*` lens label expands against. An abstract type declares
	// neither a script nor a permittedCommands gate, so Script is "" and
	// PermittedCommands is nil for one, and an agent must NOT try to submit
	// against it: it picks a concrete subtype instead.
	// Source: vtx.meta.<id> → data.abstract
	Abstract bool
}

// Traverser is the cold-start traversal client for a Lattice-aware AI agent.
// It wraps a substrate.Conn and exposes the three-step FR19 discovery
// algorithm: ReadCapability → DiscoverDDL → ReadDDLAspects.
type Traverser struct {
	conn            *substrate.Conn
	coreBucket      string
	capBucket       string
	systemActorKeys []string
}

// NewTraverser constructs a Traverser. coreBucket is typically "core-kv";
// capBucket is typically "capability-kv". Both names must match the
// deployment's bucket provisioning.
//
// systemActorKeys is the kernel-seeded system-actor set (from
// bootstrap.SystemActorKeys) that decides an actor's Capability-KV key routing
// — see ReadCapability. It is taken here, not per read, because discovering it
// costs a full core-kv listing: the platform binaries resolve it once at
// start-up and hold it for the process (cmd/processor/main.go:142), and a
// traverser is constructed the same way. An empty set routes every actor as
// ordinary, which is the correct deny-closed reading for a caller that has not
// discovered the set.
//
// Panics if conn is nil or either bucket name is empty — these are
// programming errors, not runtime conditions.
func NewTraverser(conn *substrate.Conn, coreBucket, capBucket string, systemActorKeys []string) *Traverser {
	if conn == nil {
		panic("aiagent: NewTraverser: conn must not be nil")
	}
	if coreBucket == "" || capBucket == "" {
		panic("aiagent: NewTraverser: bucket names must not be empty")
	}
	return &Traverser{
		conn:            conn,
		coreBucket:      coreBucket,
		capBucket:       capBucket,
		systemActorKeys: append([]string(nil), systemActorKeys...),
	}
}

// ReadCapability fetches the actor's resolved capability set from Capability
// KV through capabilitykv.ReadPlatformDoc — in an rbac-active deployment, the
// key routing and merge the Processor's step-3 platform read applies, so the
// agent discovers the grant set that will authorize the operations it goes on
// to submit and no more. (Step 3 routes differently in the rbac-absent
// bootstrap window; capabilitykv.ReadPlatformDoc states that precondition.)
// actor is the full `vtx.<type>.<id>` key; the routing derives the
// Capability-KV keys from it, so a non-`identity` actor is keyed correctly.
//
// Which keys that is depends on the actor's class (Contract #6 §6.1):
//
//   - A system actor (in the set given to NewTraverser) reads the UNION of
//     cap.<type>.<id> — core's kernel-literal root-grant anchor, carrying
//     kernel-only ops (CreateMetaVertex, InstallPackage, …) that are backed by
//     no rbac permission and so appear in no roles projection — and
//     cap.roles.<type>.<id>, rbac-domain's capabilityRoles projection walking
//     identity->holdsRole->role<-grantedBy-permission, which carries every
//     permission granted to a role the actor holds (including ones granted to
//     `operator` after the primordial seed, which the kernel literal does not
//     re-derive). Neither is a subset of the other, so preferring one drops
//     the other's grants.
//   - Every other actor reads cap.roles.<type>.<id> alone. An anchor doc left
//     behind for such an actor is not part of its grant set: the Processor
//     would not honor it, so neither does the agent.
//
// Duplicate {operationType, scope} entries are NOT collapsed. Two entries
// differing only in `origin` (Contract #6 §6.1 provenance) are two different
// grants — a reserved op held at runtime origin is refused at step 3 while the
// same op at package origin is not — so folding them would hide the
// distinction that decides the refusal.
//
// Returns ErrKeyNotFound (wrapped) when NO key for the actor has an entry.
// Callers should treat this as "agent has no capabilities yet".
//
// Staleness note: the returned doc reflects Refractor projections that may
// have been written some time ago. ProjectedAt is deterministic provenance
// ("as-of input state"), not a freshness ceiling — the Processor does not
// reject operations on projection age (NFR-S7). Callers must NOT rely on
// the Processor denying stale projections.
func (t *Traverser) ReadCapability(ctx context.Context, actor string) (*processor.CapabilityDoc, error) {
	doc, _, err := capabilitykv.ReadPlatformDoc(ctx, t.conn, t.capBucket, t.systemActorKeys, actor)
	if err != nil {
		return nil, fmt.Errorf("aiagent: read capability for %s: %w", actor, err)
	}
	if doc == nil {
		return nil, fmt.Errorf("aiagent: read capability for %s: %w", actor, substrate.ErrKeyNotFound)
	}
	return doc, nil
}

// DiscoverDDL resolves an operationType string to the DDL meta-vertex key
// by enumerating all vtx.meta.* keys in Core KV and reading .canonicalName
// aspects until a match is found.
//
// Per Contract #1 §1.5, DDL meta-vertices are NOT addressable by deterministic
// key — discovery by class + canonicalName is the correct mechanism.
//
// Returns (metaVertexKey, nil) on success where metaVertexKey is a valid
// vtx.meta.<NanoID> key. Returns ("", ErrDDLNotFound) when no DDL with the
// requested operationType as its canonicalName is found.
//
// Returns an error (not ErrDDLNotFound) if more than one live meta-vertex
// shares the same canonicalName — this indicates an inconsistent cell state
// (e.g. two concurrent CreateMetaVertex ops committed without conflict
// detection).
//
// Performance note: DiscoverDDL scans all Core KV keys on every call,
// filtering to 3-segment vtx.meta.* candidates client-side before issuing
// KVGet calls. KVListKeys is O(all keys in the bucket), which becomes
// expensive as Core KV grows to thousands of non-meta keys. A
// KVListKeysByPrefix("vtx.meta.") substrate method plus optional
// per-Traverser DDL caching is tracked as contracts-hardening work.
func (t *Traverser) DiscoverDDL(ctx context.Context, operationType string) (string, error) {
	keys, err := t.conn.KVListKeys(ctx, t.coreBucket)
	if err != nil {
		return "", fmt.Errorf("aiagent: list Core KV keys: %w", err)
	}

	var matches []string

	for _, key := range keys {
		// Only consider 3-segment vtx.meta.* vertex keys.
		parts := strings.Split(key, ".")
		if len(parts) != 3 || parts[0] != "vtx" || parts[1] != "meta" {
			continue
		}

		cnKey := key + ".canonicalName"
		entry, err := t.conn.KVGet(ctx, t.coreBucket, cnKey)
		if err != nil {
			// This meta-vertex has no canonicalName aspect — skip.
			continue
		}

		// canonicalName aspect shape: {"data": {"value": "<name>"}, "isDeleted": false, ...}
		var aspDoc struct {
			Data struct {
				Value string `json:"value"`
			} `json:"data"`
			IsDeleted bool `json:"isDeleted"`
		}
		if err := json.Unmarshal(entry.Value, &aspDoc); err != nil {
			continue
		}
		if aspDoc.IsDeleted {
			continue
		}
		if aspDoc.Data.Value == operationType {
			// Guard against tombstoned meta-vertices.
			// TombstoneMetaVertex only tombstones the vertex key itself, not
			// the .canonicalName aspect — so the aspect may still be readable
			// even after the vertex is tombstoned. Read the 3-segment vertex
			// key and skip if isDeleted: true.
			vtxEntry, vtxErr := t.conn.KVGet(ctx, t.coreBucket, key)
			if vtxErr != nil {
				// Can't confirm liveness — skip conservatively.
				continue
			}
			var vtxDoc struct {
				IsDeleted bool `json:"isDeleted"`
			}
			if jsonErr := json.Unmarshal(vtxEntry.Value, &vtxDoc); jsonErr != nil || vtxDoc.IsDeleted {
				continue
			}
			matches = append(matches, key)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%w: %s", ErrDDLNotFound, operationType)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("aiagent: multiple live DDLs with canonicalName %q; cell is in inconsistent state", operationType)
	}
}

// ErrCompensationAspectMissing is returned by ReadCompensation when the
// .compensation aspect is absent from the DDL meta-vertex.
//
// When the absence is caused by a missing KV key, the error also wraps
// substrate.ErrKeyNotFound; callers can use errors.Is to check for either
// sentinel independently. When caused by a tombstoned aspect envelope
// (isDeleted: true), only ErrCompensationAspectMissing is wrapped.
var ErrCompensationAspectMissing = errors.New("aiagent: .compensation aspect missing")

// ReadCompensation reads the .compensation aspect from a DDL meta-vertex.
// Returns the aspect data map as-is; caller is responsible for template
// substitution from their commit response values.
//
// The compensation contract lives in the DDL meta-vertex as a sixth
// self-description aspect. The Processor never reads or interprets
// .compensation aspects (Guardrail 2 — no new Processor read surface);
// this method is the sole client-side read path.
func (t *Traverser) ReadCompensation(ctx context.Context, metaKey string) (map[string]any, error) {
	key := metaKey + ".compensation"
	entry, err := t.conn.KVGet(ctx, t.coreBucket, key)
	if err != nil {
		return nil, fmt.Errorf("%w: at %s: %w", ErrCompensationAspectMissing, metaKey, err)
	}

	// Aspect envelope shape:
	//   {"class": "compensation", "isDeleted": false, "data": {...}}
	var aspDoc struct {
		IsDeleted bool           `json:"isDeleted"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &aspDoc); err != nil {
		return nil, fmt.Errorf("aiagent: parse .compensation aspect at %s: %w", metaKey, err)
	}
	if aspDoc.IsDeleted {
		return nil, fmt.Errorf("%w: tombstoned at %s", ErrCompensationAspectMissing, metaKey)
	}
	if aspDoc.Data == nil {
		return nil, fmt.Errorf("aiagent: .compensation aspect at %s has nil data", metaKey)
	}
	return aspDoc.Data, nil
}

// aspectEnvelope is a decode helper for aspect envelope documents.
// All aspect keys in Core KV share the outer shape:
//
//	{"class": "<aspectClass>", "isDeleted": <bool>, "data": {...}}
//
// The Data field is left as json.RawMessage so each aspect can decode
// its own data shape separately.
type aspectEnvelope struct {
	IsDeleted bool            `json:"isDeleted"`
	Data      json.RawMessage `json:"data"`
}

// ReadDDLAspects reads the seven self-description aspects from a DDL
// meta-vertex. Missing one returns ErrAspectMissing (wrapped with the aspect
// name), with a single exception that is a property of the type, not a
// tolerance for sloppiness: an ABSTRACT DDL (data.abstract on the root,
// dynamic-type-taxonomy-design.md §3.2) declares neither `.script` nor
// `.permittedCommands`, because it names no instance and no operation is ever
// submitted against it. For that type alone those two absences are the correct
// shape and are reported as Abstract plus a zero Script / nil
// PermittedCommands. A CONCRETE DDL missing either still errors — which is the
// whole point of keying the exemption on the marker rather than on the
// absence.
//
// This is step 4 of the cold-start traversal algorithm: after DiscoverDDL
// returns the meta-vertex key, ReadDDLAspects gives the agent full schema
// knowledge to construct a valid payload.
//
// A liveness pre-check is performed first: if the vertex at ddlKey is
// tombstoned or unreadable, an error is returned before any aspect reads.
// This closes the TOCTOU window between DiscoverDDL and ReadDDLAspects
// and makes ReadDDLAspects safe to call as a standalone API.
func (t *Traverser) ReadDDLAspects(ctx context.Context, ddlKey string) (*DDLAspects, error) {
	// Liveness pre-check: verify the DDL vertex itself is live before reading aspects.
	vtxEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey)
	if err != nil {
		return nil, fmt.Errorf("%w: vertex at %s: %w", ErrAspectMissing, ddlKey, err)
	}
	var vtxDoc struct {
		IsDeleted bool `json:"isDeleted"`
		Data      struct {
			Abstract bool `json:"abstract"`
		} `json:"data"`
	}
	if err := json.Unmarshal(vtxEntry.Value, &vtxDoc); err != nil || vtxDoc.IsDeleted {
		return nil, fmt.Errorf("aiagent: DDL %s is tombstoned", ddlKey)
	}

	aspects := &DDLAspects{Abstract: vtxDoc.Data.Abstract}

	// description aspect: data.text
	descEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".description")
	if err != nil {
		return nil, fmt.Errorf("%w: description at %s: %w", ErrAspectMissing, ddlKey, err)
	}
	var descEnv aspectEnvelope
	if err := json.Unmarshal(descEntry.Value, &descEnv); err != nil {
		return nil, fmt.Errorf("aiagent: parse description aspect at %s: %w", ddlKey, err)
	}
	if descEnv.IsDeleted {
		return nil, fmt.Errorf("%w: description at %s: aspect is tombstoned", ErrAspectMissing, ddlKey)
	}
	var descData struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(descEnv.Data, &descData); err != nil {
		return nil, fmt.Errorf("aiagent: parse description data at %s: %w", ddlKey, err)
	}
	aspects.Description = descData.Text

	// inputSchema aspect: data.schema
	isEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".inputSchema")
	if err != nil {
		return nil, fmt.Errorf("%w: inputSchema at %s: %w", ErrAspectMissing, ddlKey, err)
	}
	var isEnv aspectEnvelope
	if err := json.Unmarshal(isEntry.Value, &isEnv); err != nil {
		return nil, fmt.Errorf("aiagent: parse inputSchema aspect at %s: %w", ddlKey, err)
	}
	if isEnv.IsDeleted {
		return nil, fmt.Errorf("%w: inputSchema at %s: aspect is tombstoned", ErrAspectMissing, ddlKey)
	}
	var isData struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(isEnv.Data, &isData); err != nil {
		return nil, fmt.Errorf("aiagent: parse inputSchema data at %s: %w", ddlKey, err)
	}
	aspects.InputSchema = isData.Schema

	// outputSchema aspect: data.schema
	osEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".outputSchema")
	if err != nil {
		return nil, fmt.Errorf("%w: outputSchema at %s: %w", ErrAspectMissing, ddlKey, err)
	}
	var osEnv aspectEnvelope
	if err := json.Unmarshal(osEntry.Value, &osEnv); err != nil {
		return nil, fmt.Errorf("aiagent: parse outputSchema aspect at %s: %w", ddlKey, err)
	}
	if osEnv.IsDeleted {
		return nil, fmt.Errorf("%w: outputSchema at %s: aspect is tombstoned", ErrAspectMissing, ddlKey)
	}
	var osData struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(osEnv.Data, &osData); err != nil {
		return nil, fmt.Errorf("aiagent: parse outputSchema data at %s: %w", ddlKey, err)
	}
	aspects.OutputSchema = osData.Schema

	// fieldDescription aspect: data.fieldDescriptions (map[string]string)
	fdEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".fieldDescription")
	if err != nil {
		return nil, fmt.Errorf("%w: fieldDescription at %s: %w", ErrAspectMissing, ddlKey, err)
	}
	var fdEnv aspectEnvelope
	if err := json.Unmarshal(fdEntry.Value, &fdEnv); err != nil {
		return nil, fmt.Errorf("aiagent: parse fieldDescription aspect at %s: %w", ddlKey, err)
	}
	if fdEnv.IsDeleted {
		return nil, fmt.Errorf("%w: fieldDescription at %s: aspect is tombstoned", ErrAspectMissing, ddlKey)
	}
	var fdData struct {
		FieldDescriptions map[string]string `json:"fieldDescriptions"`
	}
	if err := json.Unmarshal(fdEnv.Data, &fdData); err != nil {
		return nil, fmt.Errorf("aiagent: parse fieldDescription data at %s: %w", ddlKey, err)
	}
	aspects.FieldDescriptions = fdData.FieldDescriptions

	// examples aspect: data.examples ([]ExampleEntry)
	exEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".examples")
	if err != nil {
		return nil, fmt.Errorf("%w: examples at %s: %w", ErrAspectMissing, ddlKey, err)
	}
	var exEnv aspectEnvelope
	if err := json.Unmarshal(exEntry.Value, &exEnv); err != nil {
		return nil, fmt.Errorf("aiagent: parse examples aspect at %s: %w", ddlKey, err)
	}
	if exEnv.IsDeleted {
		return nil, fmt.Errorf("%w: examples at %s: aspect is tombstoned", ErrAspectMissing, ddlKey)
	}
	var exData struct {
		Examples []ExampleEntry `json:"examples"`
	}
	if err := json.Unmarshal(exEnv.Data, &exData); err != nil {
		return nil, fmt.Errorf("aiagent: parse examples data at %s: %w", ddlKey, err)
	}
	aspects.Examples = exData.Examples

	// script aspect: data.source. An abstract type declares none — it names no
	// instance, so there is nothing to execute.
	//
	// "Declares none" has TWO on-disk shapes and only one of them is a missing
	// key. A fresh install never writes the aspect. An IN-PLACE UPGRADE of a
	// concrete type to an abstract one TOMBSTONES it instead: a DDL keeps its
	// meta-vertex NanoID across versions (Contract #8 §8.1), so pkgmgr's diff
	// emits a tombstone for the aspect the new version stops declaring, and a
	// tombstone retains the prior document with only isDeleted flipped
	// (processor step8_commit). The upgrade shape is the one every already-
	// installed cell takes, so treating only the absent shape as legitimate
	// would fail cold-start traversal on exactly the stacks that have one.
	scriptEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".script")
	if err != nil {
		if aspects.Abstract && errors.Is(err, substrate.ErrKeyNotFound) {
			return t.finishAbstractAspects(ctx, ddlKey, aspects)
		}
		return nil, fmt.Errorf("%w: script at %s: %w", ErrAspectMissing, ddlKey, err)
	}
	var scriptEnv aspectEnvelope
	if err := json.Unmarshal(scriptEntry.Value, &scriptEnv); err != nil {
		return nil, fmt.Errorf("aiagent: parse script aspect at %s: %w", ddlKey, err)
	}
	if scriptEnv.IsDeleted {
		if aspects.Abstract {
			return t.finishAbstractAspects(ctx, ddlKey, aspects)
		}
		return nil, fmt.Errorf("%w: script at %s: aspect is tombstoned", ErrAspectMissing, ddlKey)
	}
	var scriptData struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(scriptEnv.Data, &scriptData); err != nil {
		return nil, fmt.Errorf("aiagent: parse script data at %s: %w", ddlKey, err)
	}
	aspects.Script = scriptData.Source

	// permittedCommands aspect: data.commands ([]string). An abstract type
	// declares none, in either of the two shapes described above.
	pcEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".permittedCommands")
	if err != nil {
		if aspects.Abstract && errors.Is(err, substrate.ErrKeyNotFound) {
			return aspects, nil
		}
		return nil, fmt.Errorf("%w: permittedCommands at %s: %w", ErrAspectMissing, ddlKey, err)
	}
	var pcEnv aspectEnvelope
	if err := json.Unmarshal(pcEntry.Value, &pcEnv); err != nil {
		return nil, fmt.Errorf("aiagent: parse permittedCommands aspect at %s: %w", ddlKey, err)
	}
	if pcEnv.IsDeleted {
		if aspects.Abstract {
			return aspects, nil
		}
		return nil, fmt.Errorf("%w: permittedCommands at %s: aspect is tombstoned", ErrAspectMissing, ddlKey)
	}
	var pcData struct {
		Commands []string `json:"commands"`
	}
	if err := json.Unmarshal(pcEnv.Data, &pcData); err != nil {
		return nil, fmt.Errorf("aiagent: parse permittedCommands data at %s: %w", ddlKey, err)
	}
	aspects.PermittedCommands = pcData.Commands

	return aspects, nil
}

// finishAbstractAspects completes a read for an abstract DDL whose `.script`
// is withdrawn by design: it still has to answer the permittedCommands
// question, which for an abstract type is "also withdrawn, also by design".
// Withdrawn means the key is absent (a fresh install) or present-but-
// tombstoned (an in-place upgrade); both are accepted.
//
// A LIVE permittedCommands aspect on an abstract meta is neither, and is
// reported rather than swallowed: the installer emits none for an abstract
// DDL, so a live one is a malformed declaration, and letting it through would
// widen the exemption from "an abstract type declares neither" into "an
// abstract type may declare anything".
func (t *Traverser) finishAbstractAspects(ctx context.Context, ddlKey string, aspects *DDLAspects) (*DDLAspects, error) {
	entry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".permittedCommands")
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return aspects, nil
		}
		return nil, fmt.Errorf("%w: permittedCommands at %s: %w", ErrAspectMissing, ddlKey, err)
	}
	var pcEnv aspectEnvelope
	if err := json.Unmarshal(entry.Value, &pcEnv); err != nil {
		return nil, fmt.Errorf("aiagent: parse permittedCommands aspect at %s: %w", ddlKey, err)
	}
	if pcEnv.IsDeleted {
		return aspects, nil
	}
	return nil, fmt.Errorf("aiagent: abstract DDL %s declares a live permittedCommands aspect — an abstract type names no instance and admits no operation", ddlKey)
}
