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

import (
	"context"

	"github.com/asolgan/lattice/internal/substrate"
)

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

	// PreInstall is an optional hook that runs BEFORE the atomic batch.
	// Packages whose install needs Go-side seeding (e.g. identity-domain
	// seeding its 3 user-facing roles so the subsequent atomic batch's
	// grants can reference them) provide this hook. ctx is the install
	// ctx; conn is the live substrate connection; adminActor is the admin
	// vtx.identity.<NanoID> key read from lattice.bootstrap.json.
	// The hook MUST be idempotent — install retry after a partial failure
	// re-invokes it.
	PreInstall PreInstallFn
}

// PreInstallFn is the optional install pre-step a package can supply.
// Implementations live in the package (e.g. packages/identity-domain/seed.go).
//
// Returns a map of role canonical names → NanoIDs that the seed step
// created. The installer merges this map into its GrantsTo resolution
// when building the atomic batch, so grant links can reference roles
// the seed just created. May be nil/empty for packages that don't seed
// new roles.
type PreInstallFn func(ctx context.Context, conn *substrate.Conn, adminActor string) (extraRoleIDs map[string]string, err error)

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

	// Adapter is the projection-output adapter — `nats-kv` for Phase 1.
	Adapter string

	// Bucket is the target output bucket for the lens projection. The
	// Refractor's nats-kv adapter auto-creates-or-opens the bucket on
	// first projection.
	Bucket string

	// Engine selects the cypher engine — `full` for the standard rule set.
	Engine string
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
