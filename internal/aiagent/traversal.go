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
// Cold-start traversal algorithm (FR19):
//  1. Agent reads cap.identity.<actorId> from Capability KV.
//  2. Agent picks an operationType from platformPermissions[].
//  3. Agent calls DiscoverDDL to find the matching vtx.meta.<NanoID> by
//     enumerating vtx.meta.* keys and reading .canonicalName aspects.
//  4. Agent calls ReadDDLAspects for the five self-description aspects
//     seeded by Story 5.1 (description, inputSchema, outputSchema,
//     fieldDescription, examples).
//  5. Agent constructs payload from inputSchema and submits to ops.default
//     (or appropriate lane) — same Processor commit path as any human op.
package aiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// ErrDDLNotFound is returned by DiscoverDDL when no DDL meta-vertex with
// the requested canonicalName is found.
var ErrDDLNotFound = errors.New("aiagent: DDL not found for operation type")

// ErrAspectMissing is returned by ReadDDLAspects when a required
// self-description aspect is absent from the DDL meta-vertex.
var ErrAspectMissing = errors.New("aiagent: required DDL aspect missing")

// ExampleEntry represents one named usage example from a DDL's .examples
// aspect (Story 5.1 shape: {"examples": [{"name":...,"payload":...,"expectedOutcome":...}]}).
type ExampleEntry struct {
	Name            string         `json:"name"`
	Payload         map[string]any `json:"payload"`
	ExpectedOutcome string         `json:"expectedOutcome"`
}

// DDLAspects holds the five self-description aspects of a DDL meta-vertex
// seeded by Story 5.1. These give an AI agent (or any traverser) full
// knowledge of an operation's purpose, input contract, output contract,
// per-field guidance, and usage examples.
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
}

// Traverser is the cold-start traversal client for a Lattice-aware AI agent.
// It wraps a substrate.Conn and exposes the three-step FR19 discovery
// algorithm: ReadCapability → DiscoverDDL → ReadDDLAspects.
type Traverser struct {
	conn       *substrate.Conn
	coreBucket string
	capBucket  string
}

// NewTraverser constructs a Traverser. coreBucket is typically "core-kv";
// capBucket is typically "capability-kv". Both names must match the
// deployment's bucket provisioning.
func NewTraverser(conn *substrate.Conn, coreBucket, capBucket string) *Traverser {
	return &Traverser{
		conn:       conn,
		coreBucket: coreBucket,
		capBucket:  capBucket,
	}
}

// ReadCapability fetches the actor's full resolved capability set from
// Capability KV. The key format is cap.identity.<actorId> per Contract #6
// §6.2. This is the agent's first read — no prior deployment knowledge
// required beyond the actor ID.
//
// Returns ErrKeyNotFound (wrapped) when no capability entry exists for the
// actor. Callers should treat this as "agent has no capabilities yet".
func (t *Traverser) ReadCapability(ctx context.Context, actorID string) (*processor.CapabilityDoc, error) {
	key := "cap.identity." + actorID
	entry, err := t.conn.KVGet(ctx, t.capBucket, key)
	if err != nil {
		return nil, fmt.Errorf("aiagent: read capability for %s: %w", actorID, err)
	}
	doc, err := processor.ParseCapabilityDoc(entry.Value)
	if err != nil {
		return nil, fmt.Errorf("aiagent: parse capability doc for %s: %w", actorID, err)
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
func (t *Traverser) DiscoverDDL(ctx context.Context, operationType string) (string, error) {
	keys, err := t.conn.KVListKeys(ctx, t.coreBucket)
	if err != nil {
		return "", fmt.Errorf("aiagent: list Core KV keys: %w", err)
	}

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
			Data      struct{ Value string `json:"value"` } `json:"data"`
			IsDeleted bool                                  `json:"isDeleted"`
		}
		if err := json.Unmarshal(entry.Value, &aspDoc); err != nil {
			continue
		}
		if aspDoc.IsDeleted {
			continue
		}
		if aspDoc.Data.Value == operationType {
			return key, nil
		}
	}

	return "", fmt.Errorf("%w: %s", ErrDDLNotFound, operationType)
}

// ReadDDLAspects reads the five self-description aspects seeded by Story 5.1
// from a DDL meta-vertex. All five aspects are required; missing any returns
// ErrAspectMissing (wrapped with the aspect name).
//
// This is step 4 of the cold-start traversal algorithm: after DiscoverDDL
// returns the meta-vertex key, ReadDDLAspects gives the agent full schema
// knowledge to construct a valid payload.
func (t *Traverser) ReadDDLAspects(ctx context.Context, ddlKey string) (*DDLAspects, error) {
	aspects := &DDLAspects{}

	// description aspect: data.text
	descEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".description")
	if err != nil {
		return nil, fmt.Errorf("%w: description at %s: %v", ErrAspectMissing, ddlKey, err)
	}
	var descDoc struct {
		Data struct{ Text string `json:"text"` } `json:"data"`
	}
	if err := json.Unmarshal(descEntry.Value, &descDoc); err != nil {
		return nil, fmt.Errorf("aiagent: parse description aspect at %s: %w", ddlKey, err)
	}
	aspects.Description = descDoc.Data.Text

	// inputSchema aspect: data.schema
	isEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".inputSchema")
	if err != nil {
		return nil, fmt.Errorf("%w: inputSchema at %s: %v", ErrAspectMissing, ddlKey, err)
	}
	var isDoc struct {
		Data struct{ Schema string `json:"schema"` } `json:"data"`
	}
	if err := json.Unmarshal(isEntry.Value, &isDoc); err != nil {
		return nil, fmt.Errorf("aiagent: parse inputSchema aspect at %s: %w", ddlKey, err)
	}
	aspects.InputSchema = isDoc.Data.Schema

	// outputSchema aspect: data.schema
	osEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".outputSchema")
	if err != nil {
		return nil, fmt.Errorf("%w: outputSchema at %s: %v", ErrAspectMissing, ddlKey, err)
	}
	var osDoc struct {
		Data struct{ Schema string `json:"schema"` } `json:"data"`
	}
	if err := json.Unmarshal(osEntry.Value, &osDoc); err != nil {
		return nil, fmt.Errorf("aiagent: parse outputSchema aspect at %s: %w", ddlKey, err)
	}
	aspects.OutputSchema = osDoc.Data.Schema

	// fieldDescription aspect: data.fieldDescriptions (map[string]string)
	fdEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".fieldDescription")
	if err != nil {
		return nil, fmt.Errorf("%w: fieldDescription at %s: %v", ErrAspectMissing, ddlKey, err)
	}
	var fdDoc struct {
		Data struct {
			FieldDescriptions map[string]string `json:"fieldDescriptions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(fdEntry.Value, &fdDoc); err != nil {
		return nil, fmt.Errorf("aiagent: parse fieldDescription aspect at %s: %w", ddlKey, err)
	}
	aspects.FieldDescriptions = fdDoc.Data.FieldDescriptions

	// examples aspect: data.examples ([]ExampleEntry)
	exEntry, err := t.conn.KVGet(ctx, t.coreBucket, ddlKey+".examples")
	if err != nil {
		return nil, fmt.Errorf("%w: examples at %s: %v", ErrAspectMissing, ddlKey, err)
	}
	var exDoc struct {
		Data struct {
			Examples []ExampleEntry `json:"examples"`
		} `json:"data"`
	}
	if err := json.Unmarshal(exEntry.Value, &exDoc); err != nil {
		return nil, fmt.Errorf("aiagent: parse examples aspect at %s: %w", ddlKey, err)
	}
	aspects.Examples = exDoc.Data.Examples

	return aspects, nil
}
