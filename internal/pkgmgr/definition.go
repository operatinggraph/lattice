// Package pkgmgr defines the Capability Package format and provides the
// install / uninstall / list machinery used by `cmd/lattice-pkg`.
//
// See `docs/components/_packages.md` for the canonical spec.
//
// Shape:
//   - Manifest is YAML (`manifest.yaml`); package definitions are Go
//     (each package exports a `Package = pkgmgr.Definition{...}`).
//   - One atomic batch per install against the `core-kv` bucket.
//   - Operator credential is the admin actor NanoID read from
//     `lattice.bootstrap.json`.
//   - Install writes directly to core-kv (substrate-direct).
package pkgmgr

// Definition is the static, install-time bundle for one package. Package
// authors construct one of these in their package's top-level Go file and
// export it as `var Package = pkgmgr.Definition{...}`.
type Definition struct {
	// Name is the package's canonical name (matches the directory name
	// and the `name` field in manifest.yaml).
	Name string

	// Version is the simple string version (Phase 1: equality compared
	// against the installed package vertex's manifest aspect).
	Version string

	// Description is a one-line human-facing summary mirroring the
	// manifest field.
	Description string

	// Depends lists prerequisite package names. The installer logs a
	// warning and proceeds when a dependency is not verified.
	Depends []string

	// DDLs lists the DDL meta-vertices this package declares.
	DDLs []DDLSpec

	// Lenses lists the Lens meta-vertices this package declares.
	Lenses []LensSpec

	// Permissions lists the permission vertices + grants this package
	// declares.
	Permissions []PermissionSpec

	// Roles lists the user-facing roles this package declares. They are
	// created in the SAME install batch as everything else (Story 1.5.5 —
	// no substrate-direct PreInstall), with deterministic NanoIDs, and are
	// captured in the manifest's declaredKeys so uninstall reclaims them
	// (closes F-001 orphans). A package's own Permissions may reference a
	// declared role by canonical name in GrantsTo; the installer resolves
	// it to the role's deterministic NanoID.
	Roles []RoleSpec

	// WeaverTargets lists the meta.weaverTarget meta-vertices this package
	// declares. Each binds a violation Lens's weaver-targets row prefix
	// (TargetID) to a gap → remediation playbook (Contract #10 §10.8). The
	// installer emits a vtx.meta.<NanoID> vertex + a `.spec` aspect the
	// Weaver registry CDC source loads.
	WeaverTargets []WeaverTargetSpec

	// LoomPatterns lists the meta.loomPattern meta-vertices this package
	// declares (Contract #10 §10.5). Each is a linear orchestration flow over
	// one subjectType. The installer emits a vtx.meta.<NanoID> vertex + a
	// `.spec` aspect the Loom pattern source CDC-loads.
	LoomPatterns []LoomPatternSpec

	// OpMetas lists the op-meta vertices this package declares. Each carries a
	// single OperationType on the vertex `data`, making that op discoverable
	// by both engines' op-meta index — the contract that lets a Weaver
	// `assignTask`/Loom `userTask` step resolve `forOperation` to the op's
	// meta-vertex. A package declaring an op as the target of `assignTask`
	// (or a `userTask` step) must declare a matching OpMetaSpec.
	OpMetas []OpMetaSpec
}

// WeaverTargetSpec is one meta.weaverTarget meta-vertex a package declares
// (Contract #10 §10.8). The installer emits its body so the Weaver registry
// deserializes it into a runtime Target.
type WeaverTargetSpec struct {
	// TargetID is the weaver-targets row prefix (the <targetId>.<entityId>
	// key) and a durable-name segment, so it must be a single KV-key token.
	TargetID string

	// LensRef names the violation Lens whose rows this target dispatches over.
	// A package author writes the lens's CanonicalName; the installer resolves
	// it to that lens's in-batch NanoID (a literal NanoID is passed through to
	// support a lens in an already-installed package). It is surfaced on
	// Weaver's control API; lane-1 dispatch watches weaver-targets directly,
	// not via LensRef.
	LensRef string

	// Gaps maps each `missing_<gap>` violation column to the remediation
	// action the engine runs when that column is set.
	Gaps map[string]GapActionSpec
}

// GapActionSpec mirrors the engine's GapAction (Contract #10 §10.8 action
// table) field-for-field so the emitted body deserializes cleanly into the
// runtime target. Action selects the contract; the remaining fields carry the
// per-action params, each a literal or a `row.<column>` template token.
// `Pattern` (triggerLoom) and `Operation` (assignTask/nudge/directOp) are
// shipped verbatim and resolve live in the engine registry — the installer
// does not rewrite them to NanoIDs.
type GapActionSpec struct {
	Action    string
	Pattern   string
	Subject   string
	Adapter   string
	Operation string
	Assignee  string
	Target    string
	Params    map[string]string
}

// LoomPatternSpec is one meta.loomPattern meta-vertex a package declares
// (Contract #10 §10.5). The installer emits its body so the Loom pattern
// source deserializes it into a runtime Pattern.
type LoomPatternSpec struct {
	// PatternID is the pattern's canonical id; a playbook references a pattern
	// by this string (resolved live at dispatch).
	PatternID string

	// SubjectType is the vertex type an instance of this pattern runs for.
	SubjectType string

	// CompletionDomains is the explicit set of event domains the engine
	// reconciles completion consumers for. Empty defaults to {SubjectType}.
	CompletionDomains []string

	// Steps is the linear list of pattern steps.
	Steps []StepSpec
}

// StepSpec is one entry in a pattern's linear step list (Contract #10 §10.5).
// systemOp/userTask carry `{kind, operation, guard?}`; externalTask carries
// `{kind, adapter, params, replyOp, instanceOp, guard?}` and leaves Operation
// unused.
type StepSpec struct {
	// Kind is `systemOp` (submit the bound op directly), `userTask` (CreateTask
	// and wait for the user to perform the bound op), or `externalTask` (submit
	// the instanceOp and wait for the bridge's replyOp).
	Kind string

	// Operation names the bound op for a systemOp/userTask step (unused by
	// externalTask).
	Operation string

	// Guard is the §10.5 declarative predicate the step is gated on. Carried
	// as a Go map so authors write a map literal; marshaled into the step's
	// `guard` field and omitted when nil.
	Guard map[string]any

	// Adapter is the external adapter name an externalTask dispatches to
	// (required for externalTask, unused otherwise).
	Adapter string

	// Params are an externalTask's adapter parameters — author-friendly map,
	// emitted into the step's `params` field and omitted when nil. Opaque to the
	// engine (passed through to the instanceOp payload).
	Params map[string]any

	// ReplyOp is the result-op type the bridge posts back for an externalTask
	// (required for externalTask, unused otherwise).
	ReplyOp string

	// InstanceOp is the op an externalTask step submits — its DDL mints the claim
	// vertex and emits the external.<adapter> event (required for externalTask,
	// unused otherwise).
	InstanceOp string
}

// OpMetaSpec is one op-meta vertex a package declares so an op is discoverable
// by `forOperation` resolution. The installer emits a vtx.meta.<NanoID> vertex
// carrying `data.operationType`; both engines index it identically.
//
// A future ergonomic could auto-emit one of these per DDL PermittedCommand so
// authors never hand-list them; the explicit field keeps the author in control
// of exactly which ops are resolvable.
type OpMetaSpec struct {
	// OperationType is the op this vertex makes `forOperation`-resolvable.
	OperationType string
}

// RoleSpec is one user-facing role a package declares. The installer
// creates a role vertex (`vtx.role.<id>`), its canonicalName +
// description aspects, and a canonical-name index vertex
// (`vtx.roleindex.<sha256(canonical)>` → roleId) — all in the install
// batch with deterministic NanoIDs.
type RoleSpec struct {
	// CanonicalName is the role's canonical name (e.g. "consumer").
	CanonicalName string

	// Description is the role's plain-language description aspect.
	Description string
}

// DDLSpec is one DDL meta-vertex declaration.
type DDLSpec struct {
	// CanonicalName is the DDL's canonical name (used by the Processor's
	// DDL cache for class lookup).
	CanonicalName string

	// Class is the meta-vertex class — typically `meta.ddl.vertexType`.
	Class string

	// PermittedCommands is the list of operationTypes the DDL admits.
	// The Starlark script in Script handles each of these.
	PermittedCommands []string

	// Description is a plain-language description aspect.
	Description string

	// Script is the Starlark source. Each permittedCommand should have
	// a branch; the runner returns ScriptError for unrecognized ops.
	Script string

	// Self-description aspects. Required for all DDL classes.

	// InputSchema is the JSON Schema string for this DDL's operation payload.
	InputSchema string

	// OutputSchema is the JSON Schema string for this DDL's operation response.
	OutputSchema string

	// FieldDescription maps payload field paths to plain-language descriptions.
	FieldDescription map[string]string

	// Examples is an ordered list of named usage examples for this DDL.
	Examples []ExampleSpec
}

// ExampleSpec is a single named usage example for a DDL operation.
type ExampleSpec struct {
	// Name is a short descriptive label for this example.
	Name string

	// Payload is the example operation payload sent by the client.
	Payload map[string]any

	// ExpectedOutcome is plain English describing what the platform does.
	ExpectedOutcome string
}

// LensSpec is one Lens meta-vertex declaration.
type LensSpec struct {
	// CanonicalName is the lens's canonical name (e.g. `duplicateCandidates`).
	CanonicalName string

	// Class is typically `meta.lens`.
	Class string

	// Spec is the cypher source for the lens body.
	Spec string

	// Adapter is the projection-output adapter — `"nats-kv"` or `"postgres"`.
	Adapter string

	// Bucket is the target NATS KV bucket name (nats-kv adapter only). The
	// Refractor's nats-kv adapter auto-creates-or-opens the bucket on first
	// projection. Must not be a reserved short alias (see validateLensBuckets).
	Bucket string

	// DSN is the Postgres connection string (postgres adapter only).
	DSN string

	// Table is the Postgres table name (postgres adapter only). The table must
	// already exist — the installer and Refractor never issue table DDL.
	Table string

	// QueryTimeout is the per-query deadline for the postgres adapter, e.g.
	// "5s". Empty defaults to 30s in Refractor. Ignored by nats-kv.
	QueryTimeout string

	// Engine selects the cypher engine — `full` for the standard rule set.
	Engine string

	// ProjectionKind opts the lens into the declarative actor-aggregate
	// projection plan ("actorAggregate"); empty for a plain projection lens.
	ProjectionKind string

	// Output is the §6.13 Output descriptor for an actor-aggregate lens. It is
	// emitted into the lens spec body so Refractor compiles a ProjectionPlan
	// from it. Nil for a non-actor-aggregate lens.
	Output *OutputDescriptorSpec

	// IntoKey is the lens's primary output-key column list — the RETURN
	// column(s) the adapter keys each projected record under. Empty defaults to
	// ["key"] (the per-row envelope key produced by an actor-aggregate lens).
	// An operation-aggregate index keys by its aggregation column instead (e.g.
	// ["operationType"] for the role-by-operation index).
	IntoKey []string
}

// OutputDescriptorSpec mirrors the on-wire §6.13 Output descriptor a package
// actor-aggregate lens declares. Field shape matches the Refractor-side
// lens.OutputDescriptorSpec.
type OutputDescriptorSpec struct {
	AnchorType         string   `json:"anchorType"`
	OutputKeyPattern   string   `json:"outputKeyPattern"`
	BodyColumns        []string `json:"bodyColumns"`
	EmptyBehavior      string   `json:"emptyBehavior"`
	RealnessFilter     string   `json:"realnessFilter,omitempty"`
	Freshness          string   `json:"freshness,omitempty"`
	KeyColumn          string   `json:"keyColumn,omitempty"`
	ActorField         string   `json:"actorField,omitempty"`
	Lanes              []string `json:"lanes,omitempty"`
	StaticEmptyColumns []string `json:"staticEmptyColumns,omitempty"`
}

// PermissionSpec is one permission vertex + grant set.
type PermissionSpec struct {
	// OperationType is the operationType this permission gates.
	OperationType string

	// Scope is `any` or `self` per Contract #6.
	Scope string

	// GrantsTo lists the role canonical names that receive this
	// permission via a `grantedBy` link at install time.
	GrantsTo []string

	// Note is an optional human-readable note stored in the permission
	// vertex's data.
	Note string
}
