// Package starlark_spike provides a research spike validating the go.starlark.net
// library for use as the Lattice Processor's script execution layer.
//
// This is a spike deliverable — not production code. It validates three areas:
// sandbox correctness, API ergonomics, and order-of-magnitude performance.
//
// See README.md for findings.
package starlark_spike

// ---- Contract #2 types (Operation Envelope) ----

// OperationEnvelope represents the message format for operations on core-operations JetStream.
// Per Contract #2 §2.1. This is a simplified prototype for the spike.
type OperationEnvelope struct {
	RequestID     string      `json:"requestId"`
	Lane          string      `json:"lane"`
	OperationType string      `json:"operationType"`
	Actor         string      `json:"actor"`
	SubmittedAt   string      `json:"submittedAt"`
	Payload       interface{} `json:"payload"`
	ContextHint   *ContextHint `json:"contextHint,omitempty"`
	AuthContext   *AuthContext `json:"authContext,omitempty"`
}

// ContextHint carries the JIT hydration directive per Contract #2 §2.5.
type ContextHint struct {
	Reads []string `json:"reads"`
}

// AuthContext carries auth path declaration per Contract #2 §2.8.
type AuthContext struct {
	Service string `json:"service,omitempty"`
	Task    string `json:"task,omitempty"`
	Target  string `json:"target,omitempty"`
}

// ---- Contract #1 types (Vertex/Aspect/Link documents) ----

// VertexDoc represents a hydrated vertex document from Core KV.
// Per Contract #1 §1.x. Simplified for spike purposes.
type VertexDoc struct {
	Key       string                 `json:"key"`
	Class     string                 `json:"class"`
	IsDeleted bool                   `json:"isDeleted"`
	Data      map[string]interface{} `json:"data"`
}

// MetaVertex represents a DDL meta-vertex (class definition).
// Per Contract #1 §1.5.
type MetaVertex struct {
	Key             string   `json:"key"`
	CanonicalName   string   `json:"canonicalName"`
	PermittedCommands []string `json:"permittedCommands"`
	Schema          interface{} `json:"schema"`
}

// ---- Contract #3 types (MutationBatch and EventList) ----

// MutationOp represents a single intended state transition on a Core KV key.
// Per Contract #3 §3.2. Op must be one of "create", "update", "tombstone".
type MutationOp struct {
	Op               string                 `json:"op"`
	Key              string                 `json:"key"`
	Document         map[string]interface{} `json:"document,omitempty"`
	ExpectedRevision *uint64                `json:"expectedRevision,omitempty"`
}

// EventSpec represents a business event to publish to core-events JetStream.
// Per Contract #3 §3.4.
type EventSpec struct {
	Class string                 `json:"class"`
	Data  map[string]interface{} `json:"data"`
}

// ScriptResult is the parsed return value of a Starlark script execution.
// Per Contract #3 §3.1: the script returns {"mutations": [...], "events": [...]}.
type ScriptResult struct {
	Mutations []MutationOp `json:"mutations"`
	Events    []EventSpec  `json:"events"`
}

// ---- ScriptContext — the prototype API for Story 1.6 ----

// ScriptContext holds everything the Processor makes available to a Starlark script
// during commit step 5 (Execute). Story 1.6 will wire this into the Processor commit path.
//
// The Processor populates this struct after commit step 4 (JIT Hydrate) and passes
// it to RunScript, which converts it into Starlark global bindings for the script.
type ScriptContext struct {
	// Operation is the full envelope the Processor consumed from core-operations.
	// Available to the Starlark script as the `op` global.
	Operation OperationEnvelope

	// Hydrated contains vertex documents pre-fetched at commit step 4 (JIT Hydrate),
	// keyed by Core KV key (e.g., "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y").
	// Available to the Starlark script as the `state` global dict.
	// Only keys declared in op.contextHint.reads are guaranteed present;
	// any un-declared reads will be absent (returning None in Starlark).
	Hydrated map[string]VertexDoc

	// DDLLookup contains DDL meta-vertices keyed by canonicalName (e.g., "identity").
	// Available to the Starlark script as the `ddl` global dict.
	// Populated from the DDL cache and used for validation.
	DDLLookup map[string]MetaVertex
}
