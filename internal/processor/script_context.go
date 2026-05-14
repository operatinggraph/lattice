package processor

import (
	"fmt"
)

// ScriptContext is the full input the Processor makes available to a
// Starlark script at commit step 5. It is built by step 4 (Hydrate) and
// passed verbatim into the Starlark runner.
//
// Field roles (Contract #2 §2.5 + Contract #1 §1.5 + Contract #3 §3.1):
//   - Operation: the full envelope the Processor consumed. Exposed to the
//     script as the `op` global struct.
//   - Hydrated: vertex/aspect documents pre-fetched per `contextHint.reads`.
//     Exposed as the `state` global dict (key -> struct).
//   - DDLLookup: DDL meta-vertices keyed by canonicalName (e.g.,
//     "identity"). Exposed as the `ddl` global dict. Story 1.6 populates
//     only the operation's class; Story 1.10 hardens with a startup
//     cache.
//   - ScriptSource: the Starlark source for the operation's class.
//     Internal to the runner, not exposed to the script.
//   - ScriptClass: the canonicalName of the class whose script will run.
//     Echoed in logs and errors for traceability.
type ScriptContext struct {
	Operation    *OperationEnvelope
	Hydrated     map[string]VertexDoc
	DDLLookup    map[string]MetaVertex
	ScriptSource string
	ScriptClass  string
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
}

// MetaVertex is the DDL meta-vertex projection used by Story 1.6's lookup.
// Story 1.10 will expand this when the DDL cache lands; the fields exposed
// here are the minimum the executing script and Story 1.7's validator
// need.
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
// Story 1.5 carried this as an empty struct; Story 1.6 expands it.
type HydratedState struct {
	Context ScriptContext
}

// ScriptResult is the parsed return value of step 5 (Execute). The
// commit path passes it forward to step 6 (Validate) — which is still
// stubbed in Story 1.6.
type ScriptResult struct {
	Mutations []MutationOp
	Events    []EventSpec
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
type ScriptError struct {
	Code               string // "ScriptError" | "SandboxViolation" | "ScriptTimeout" | "InvalidReturnShape"
	Message            string
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
