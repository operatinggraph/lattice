package pkgmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// CoreBucket is the bucket all Capability Package writes target. The installer
// uses single-bucket atomic batches; cross-bucket batches are not supported
// by the NATS atomic-batch protocol.
const CoreBucket = "core-kv"

// PackageVertexPrefix is the Contract #1 vertex prefix the installer
// uses to record an installed package. The full vertex key shape is
// `vtx.package.<NanoID>`; the canonical name is recorded as an aspect
// so list / uninstall can resolve canonical-name → NanoID.
const PackageVertexPrefix = "vtx.package."

// DefaultBatchTimeout is the wall budget for a single install / uninstall
// atomic-batch round-trip.
const DefaultBatchTimeout = 30 * time.Second

// Installer drives package install / uninstall / list. The caller wires
// it with a substrate connection + the admin actor key read from
// `lattice.bootstrap.json`.
type Installer struct {
	Conn       *substrate.Conn
	AdminActor string // The provenance `createdBy` for every aspect written.
	Now        func() time.Time

	// RoleIDs maps role canonical names to NanoIDs for grant-link
	// resolution. Callers (cmd/lattice-pkg) populate this from
	// lattice.bootstrap.json so packages whose `GrantsTo` references
	// primordial roles (e.g. "operator") get the right link target.
	// Roles a package declares itself (Definition.Roles) are minted with
	// deterministic NanoIDs and merged in at install time. The map may be
	// unset (nil) for tests that hard-code NanoIDs in GrantsTo.
	RoleIDs map[string]string

	// Submit, when set, replaces submitOp's default direct-NATS
	// request/reply for every op this installer sends — e.g. a caller
	// relaying through an HTTP Gateway with its own verified operator
	// credential instead of stamping AdminActor
	// (loupe-operator-auth-lift-design.md §3.2). nil (the default)
	// preserves today's direct-NATS behavior unchanged.
	Submit func(ctx context.Context, operationType, class, requestID string, payload map[string]any) (*processor.OperationReply, error)
}

// NewInstaller builds a default-configured installer.
func NewInstaller(conn *substrate.Conn, adminActor string) *Installer {
	return &Installer{
		Conn:       conn,
		AdminActor: adminActor,
		Now:        func() time.Time { return time.Now().UTC() },
	}
}

// Install applies a package Definition to Core KV.
//
// Steps:
//  1. Dependency check — Phase 1 logs/returns a warning slice (not an
//     error).
//  2. Idempotency check — read any existing package vertex with the
//     same canonical name. Same version → no-op. Different version →
//     return ErrVersionMismatch.
//  3. Construct the full op list (DDLs + aspects, Lenses + aspects,
//     Permissions + grants, package vertex + manifest aspect).
//  4. Submit one atomic batch.
//
// Returns a Result describing what happened (or what was skipped).
func (i *Installer) Install(ctx context.Context, def Definition) (*InstallResult, error) {
	// Fail closed before any KV operation: e.g. a lens whose declared Bucket is
	// a reserved short alias would auto-create a phantom bucket no reader
	// consults (silent mis-targeting of the auth plane). Returns the composed
	// Definition — the generated cap-read producers install alongside the data
	// lenses they grant for.
	def, err := i.preflight(def)
	if err != nil {
		return nil, err
	}

	res := &InstallResult{PackageName: def.Name, PackageVersion: def.Version}

	// Pre-flight: confirm core-kv bucket exists before any KV operation.
	// If bootstrap has not run, the bucket is absent and we return a clear
	// actionable error instead of a raw NATS stream-not-found message.
	if err := i.checkCoreBucketExists(ctx); err != nil {
		return nil, err
	}

	// Step 1 — dependency warnings (warn-and-proceed; install order is the
	// operator's responsibility).
	for _, dep := range def.Depends {
		if dep == "" {
			continue
		}
		res.DependencyWarnings = append(res.DependencyWarnings,
			fmt.Sprintf("declared dependency %q not verified at install time", dep))
	}

	// Step 2 — idempotency check via the package vertex aspect.
	existing, err := i.findInstalledPackage(ctx, def.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Version == def.Version {
			res.Skipped = true
			res.Reason = fmt.Sprintf("package %q version %q already installed", def.Name, def.Version)
			res.PackageKey = existing.Key
			return res, nil
		}
		return nil, fmt.Errorf("%w: installed=%s requested=%s", ErrVersionMismatch, existing.Version, def.Version)
	}

	// Step 2.6 — meta canonicalName collision against the already-installed
	// kernel. Run AFTER the idempotency check confirms this is a genuinely
	// fresh install of a not-yet-installed package name: a re-install of an
	// already-present package short-circuits above, so the scan below never
	// sees a package's own previously-written meta-vertices as a collision.
	// A collision the install introduces would otherwise silently shadow one
	// definition at runtime (the DDL cache keeps first-seen, logs a WARN).
	//
	// The scan below is the ONE Core KV key list this whole Install call
	// needs — buildManifestBatch (and everything it calls: SubtypeOfRef
	// resolution, the installed subtypeOf graph, the live-instance guard on a
	// newly-declared abstract type) reuses it rather than re-listing. A def
	// declaring no DDL/Lens/OpMeta name — and therefore, since Abstract and
	// SubtypeOfRef both live on DDLSpec, no taxonomy mechanism either —
	// performs zero KV I/O here.
	var scan metaScanResult
	if needsMetaCollisionScan(def) {
		scan, err = i.scanMeta(ctx)
		if err != nil {
			return nil, err
		}
	}
	if err := i.checkCanonicalNameCollision(def, scan.names); err != nil {
		return nil, err
	}

	// Step 2.7 — weaver targetId collision against installed targets (§10.8:
	// targetId uniqueness is install-validated across installed targets). A
	// weaver target has no canonicalName aspect, so the check above misses it;
	// this runs after the same idempotency gate so a re-install never collides
	// with its own prior targets.
	if err := i.checkWeaverTargetIDCollision(ctx, def); err != nil {
		return nil, err
	}

	// Step 3 — build the mutation manifest (role NanoIDs, grant resolution,
	// version-independent entity keys, the full create batch — including
	// dynamic-type-taxonomy-design.md §3.5's SubtypeOfRef resolution). Shared
	// with Upgrade/Apply, which need the identical new key set + bodies.
	ops, declared, pkgKey, leafBudgetWarnings, err := i.buildManifestBatch(ctx, def, scan)
	if err != nil {
		return nil, err
	}
	res.PackageKey = pkgKey
	res.LeafBudgetWarnings = leafBudgetWarnings

	// Step 4 — submit the InstallPackage op to the Processor. The op
	// carries the pre-built manifest; the kernel script enforces
	// guardrails and emits the mutations; the Processor commits them in
	// one atomic batch and invalidates the vtx.meta.* DDL cache in-commit.
	payload := map[string]any{
		"name":      def.Name,
		"version":   def.Version,
		"mutations": ops,
	}
	// Deterministic requestId from name+version+content so a re-submit of the
	// SAME manifest dedup-short-circuits at step 2 (idempotent install) while
	// a same-version edit still reaches the Processor.
	requestID, err := contentRequestID(def.Name, def.Version, "install-op", ops)
	if err != nil {
		return nil, err
	}
	reply, err := i.submitOp(ctx, "InstallPackage", "InstallPackage", requestID, payload)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: submit InstallPackage: %w", err)
	}
	switch reply.Status {
	case processor.ReplyStatusAccepted, processor.ReplyStatusDuplicate:
		res.DeclaredKeys = declared
		return res, nil
	default:
		return nil, fmt.Errorf("pkgmgr: InstallPackage rejected: %s", replyError(reply))
	}
}

// buildManifestBatch mints version-independent entity keys for def, resolves
// its grants, validates them, and builds the full create-batch manifest as
// LOGICAL documents (Contract #8 §8.1). Returns the create mutations, the flat
// declared-key list, the package vertex key, and any LeafBudget warnings
// (dynamic-type-taxonomy-design.md §10.2 — advisory only, never a rejection).
// Shared by Install (the fresh create), Upgrade, and Apply's dry-run preview:
// all three need the identical version-independent key set + bodies, so
// deriving them in one place keeps the upgrade diff aligned with what a fresh
// install would write. Field-level validation (validateLensBuckets etc.) and
// the install-specific idempotency / canonicalName-collision checks remain
// with the callers.
//
// scan is the caller's Core KV key list, already fetched when
// scan.fetched is true (Install always has one whenever needsMetaCollisionScan
// applied). When scan.fetched is false and this def actually needs one
// (needsTaxonomyScan — a SubtypeOfRef to resolve or an Abstract declaration's
// live-instance guard, taxonomy.go), buildManifestBatch fetches its own,
// exactly once. Upgrade and Apply's dry-run preview — which never run the
// Install-only canonicalName collision check and so never pre-fetch — reach
// this lazy path.
func (i *Installer) buildManifestBatch(ctx context.Context, def Definition, scan metaScanResult) ([]installMutation, []string, string, []string, error) {
	// Mint deterministic NanoIDs for any roles this package declares, and
	// register them in RoleIDs so this package's own GrantsTo entries (and the
	// grant links built below) resolve to the role's in-batch NanoID. The role
	// vertices/aspects/index are created in the SAME batch (Story 1.5.5 — no
	// substrate-direct PreInstall) and captured in declaredKeys (closes F-001).
	roleNanoIDs := make([]string, len(def.Roles))
	if len(def.Roles) > 0 && i.RoleIDs == nil {
		i.RoleIDs = map[string]string{}
	}
	for idx, r := range def.Roles {
		id := entityNanoID(def.Name, "role:"+r.CanonicalName)
		roleNanoIDs[idx] = id
		i.RoleIDs[r.CanonicalName] = id
	}

	// Resolve any unresolved canonical names in GrantsTo (and pane
	// OfferedToRoles) via i.RoleIDs.
	def = i.resolveGrants(def)
	def = i.resolvePaneRoles(def)

	// Validate all GrantsTo entries resolved to valid NanoIDs. A remaining
	// canonical name (non-NanoID) means the bootstrap JSON is missing the
	// role's primordialID or the package did not declare the role in
	// Definition.Roles. A dangling grant link would be written silently and
	// cause PermissionDenied at runtime with no helpful diagnostic.
	for idx, p := range def.Permissions {
		for _, g := range p.GrantsTo {
			if !substrate.IsValidNanoID(g) {
				return nil, nil, "", nil, fmt.Errorf("pkgmgr: Permission[%d] %q: GrantsTo entry %q is not a valid NanoID — role may not be installed or bootstrap JSON is missing the role ID", idx, p.OperationType, g)
			}
		}
	}
	for idx, p := range def.Panes {
		for _, g := range p.OfferedToRoles {
			if !substrate.IsValidNanoID(g) {
				return nil, nil, "", nil, fmt.Errorf("pkgmgr: Pane[%d] %q: OfferedToRoles entry %q is not a valid NanoID — role may not be installed or bootstrap JSON is missing the role ID", idx, p.CanonicalName, g)
			}
		}
		if !IsPaneSurface(p.Surface) {
			return nil, nil, "", nil, fmt.Errorf("pkgmgr: Pane[%d] %q: Surface %q is not a known surface (%q or %q)", idx, p.CanonicalName, p.Surface, PaneSurfaceWork, PaneSurfaceAccount)
		}
	}

	// Version-independent NanoIDs (derived from package name + entity tag,
	// Contract #8 §8.1) so a re-install produces identical keys and the same
	// logical entity keeps its key across versions (the in-place upgrade §8.6).
	pkgKey := PackageVertexPrefix + entityNanoID(def.Name, "package")

	ddlNanoIDs := make([]string, len(def.DDLs))
	lensNanoIDs := make([]string, len(def.Lenses))
	permNanoIDs := make([]string, len(def.Permissions))
	weaverTargetNanoIDs := make([]string, len(def.WeaverTargets))
	loomPatternNanoIDs := make([]string, len(def.LoomPatterns))
	opMetaNanoIDs := make([]string, len(def.OpMetas))
	for idx, d := range def.DDLs {
		ddlNanoIDs[idx] = entityNanoID(def.Name, "ddl:"+d.CanonicalName)
	}
	for idx, l := range def.Lenses {
		lensNanoIDs[idx] = entityNanoID(def.Name, "lens:"+l.CanonicalName)
	}
	for idx, p := range def.Permissions {
		permNanoIDs[idx] = entityNanoID(def.Name, permTag(p.OperationType, p.Scope))
	}
	for idx, t := range def.WeaverTargets {
		weaverTargetNanoIDs[idx] = entityNanoID(def.Name, "weaverTarget:"+t.TargetID)
	}
	for idx, p := range def.LoomPatterns {
		loomPatternNanoIDs[idx] = entityNanoID(def.Name, "loomPattern:"+p.PatternID)
	}
	for idx, o := range def.OpMetas {
		opMetaNanoIDs[idx] = entityNanoID(def.Name, "opMeta:"+o.OperationType)
	}
	paneNanoIDs := make([]string, len(def.Panes))
	for idx, p := range def.Panes {
		paneNanoIDs[idx] = entityNanoID(def.Name, "pane:"+p.CanonicalName)
	}
	retentionClassNanoIDs := make([]string, len(def.RetentionClasses))
	for idx, rc := range def.RetentionClasses {
		retentionClassNanoIDs[idx] = RetentionClassID(def.Name, rc.CanonicalName)
	}

	// dynamic-type-taxonomy-design.md §3.5/§8: resolve every declared
	// SubtypeOfRef (batch-local first, then the installed kernel), and
	// refuse a newly-declared Abstract type that still has live instances
	// (checkAbstractNoLiveInstances, taxonomy.go). A def using neither
	// mechanism (needsTaxonomyScan false) performs zero extra reads — and
	// when the caller (Install) already fetched scan, neither does this def
	// re-list the bucket even when it DOES use one.
	if !scan.fetched && needsTaxonomyScan(def) {
		var err error
		scan, err = i.scanMeta(ctx)
		if err != nil {
			return nil, nil, "", nil, err
		}
	}
	if err := i.checkAbstractNoLiveInstances(ctx, def, scan); err != nil {
		return nil, nil, "", nil, err
	}
	subtypeAbstractIDs, leafBudgetWarnings, err := i.resolveTaxonomy(ctx, def, scan)
	if err != nil {
		return nil, nil, "", nil, err
	}

	ops, declared, err := i.buildInstallBatch(def, pkgKey, ddlNanoIDs, lensNanoIDs, permNanoIDs, roleNanoIDs,
		weaverTargetNanoIDs, loomPatternNanoIDs, opMetaNanoIDs, paneNanoIDs, retentionClassNanoIDs,
		subtypeAbstractIDs)
	if err != nil {
		return nil, nil, "", nil, err
	}
	return ops, declared, pkgKey, leafBudgetWarnings, nil
}

// deterministicNanoID derives a stable Contract #1 NanoID from the
// package name+version+tag. Same inputs → same ID on every run. It is used
// for the version-scoped op requestId (so re-submitting the same install/
// upgrade dedup-short-circuits while distinct versions stay independent);
// entity keys use entityNanoID, which omits the version (Contract #8 §8.1).
func deterministicNanoID(name, version, tag string) string {
	return nanoIDFromSalt("lattice-pkg:" + name + ":" + version + ":" + tag)
}

// contentRequestID derives an op requestId from the package identity AND the
// exact mutation set the op carries.
//
// The requestId must be deterministic so that genuinely re-submitting the same
// work dedup-short-circuits at the Processor's step 2. Deriving it from
// name+version alone assumes the version identifies the content — true for a
// real version bump, false for the same-version edit that `make
// reinstall-package` exists to serve. On that path fromVersion == toVersion, so
// every run produced an identical requestId and the Processor discarded all but
// the first as a duplicate: the second and later edits to a package's DDL or
// lens spec were silently dropped while the CLI still reported "committed"
// (ReplyStatusDuplicate is treated as success). Folding the mutation digest in
// keeps the idempotency — identical content still yields an identical id — and
// makes a changed same-version edit a distinct op.
func contentRequestID(name, versionScope, tag string, mutations []installMutation) (string, error) {
	digest, err := mutationsDigest(mutations)
	if err != nil {
		return "", err
	}
	return nanoIDFromSalt("lattice-pkg:" + name + ":" + versionScope + ":" + tag + ":" + digest), nil
}

// mutationsDigest is a stable content hash of a mutation batch. encoding/json
// sorts map keys and preserves struct field order, so the same batch always
// marshals to the same bytes.
func mutationsDigest(mutations []installMutation) (string, error) {
	raw, err := json.Marshal(mutations)
	if err != nil {
		return "", fmt.Errorf("pkgmgr: digest mutations: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// entityNanoID derives a stable, version-independent Contract #1 NanoID for
// an installed entity (a DDL, lens, permission, role, op-meta, …) from the
// package name + entity tag — NOT the version (Contract #8 §8.1). The same
// logical entity therefore keeps the same vtx.meta.<id> / vtx.<type>.<id>
// key across versions, so a version upgrade is an in-place update of stable
// keys (§8.6) instead of a re-mint that would orphan vertices and break
// every NanoID cross-reference (a WeaverTarget's lensRef, a grant link).
func entityNanoID(name, tag string) string {
	return nanoIDFromSalt("lattice-pkg:" + name + ":" + tag)
}

// RoleID returns the deterministic, version-independent NanoID a package's
// declared role receives at install — the exact value entityNanoID computes
// internally for a RoleSpec. Exported so Go code outside the installer (e.g.
// the Gateway, resolving a role's key to grant it in an op payload) can
// address a package-declared role without a KV read or re-deriving the tag
// convention.
func RoleID(packageName, canonicalName string) string {
	return entityNanoID(packageName, "role:"+canonicalName)
}

// LensID returns the deterministic, version-independent NanoID a package's
// declared lens receives at install — the exact value entityNanoID computes
// internally for a LensSpec (line 252's lensNanoIDs derivation). Refractor
// keys Health KV by this NanoID (cmd/refractor/main.go's health.New(kv,
// r.ID)), not by the lens's canonical name, so a caller checking
// projectionhealth.Check for a specific lens must resolve this ID first.
func LensID(packageName, canonicalName string) string {
	return entityNanoID(packageName, "lens:"+canonicalName)
}

// DDLID returns the deterministic, version-independent NanoID a package's
// declared DDL receives at install — the exact value entityNanoID computes
// internally for a DDLSpec. Exported for the same reason as RoleID/LensID: a
// caller that must ADDRESS a DDL's meta-vertex has no other way to compute the
// key. The taxonomy makes that concrete — a `subtypeOf` edge is keyed
// `lnk.meta.<DDLID(pkg, leaf)>.subtypeOf.meta.<DDLID(pkg, parent)>`
// (dynamic-type-taxonomy-design.md §3.3), so naming one edge means resolving
// two DDL NanoIDs.
func DDLID(packageName, canonicalName string) string {
	return entityNanoID(packageName, "ddl:"+canonicalName)
}

// RetentionClassID returns the deterministic, version-independent NanoID a
// package's declared retention class receives at install — the exact value
// entityNanoID computes internally for a RetentionClassSpec. Exported for the
// same reason as RoleID/LensID: a caller that must address the holder vertex
// (the Processor resolving custody, an operator naming the class to shred)
// resolves it without a KV read or re-deriving the tag convention.
func RetentionClassID(packageName, canonicalName string) string {
	return entityNanoID(packageName, "retention:"+canonicalName)
}

// RetentionClassKey returns the full Contract #1 vertex key of a package's
// declared retention class. The type segment is all-lowercase
// (RetentionClassVertexType) because a type segment is [a-z][a-z0-9]* — the
// declared custody KIND string is camelCase, and conflating the two produces a
// key nothing can address.
func RetentionClassKey(packageName, canonicalName string) string {
	return "vtx." + RetentionClassVertexType + "." + RetentionClassID(packageName, canonicalName)
}

// permTag is the version-independent identity tag for a permission entity:
// its operationType + scope (the logical identity per Contract #6), not its
// position in the package's Permissions slice — so reordering the slice does
// not churn the permission's key. A package declaring two permissions with
// the same (operationType, scope) is a degenerate duplicate, rejected by
// validatePermissionIdentityUniqueness before any key is minted.
func permTag(operationType, scope string) string {
	return "perm:" + operationType + ":" + scope
}

// nanoIDFromSalt hashes a salt string into a Contract #1 NanoID-alphabet id
// of substrate.NanoIDLength characters. Shared by the version-scoped and
// version-independent derivations above.
func nanoIDFromSalt(salt string) string {
	sum := sha256.Sum256([]byte(salt))
	out := make([]byte, substrate.NanoIDLength)
	for i := 0; i < substrate.NanoIDLength; i++ {
		hi := sum[(i*2)%len(sum)]
		lo := sum[((i*2)+1)%len(sum)]
		idx := (int(hi)<<8 | int(lo)) % len(substrate.Alphabet)
		out[i] = substrate.Alphabet[idx]
	}
	return string(out)
}

// replyError renders a rejected reply's error for diagnostics.
func replyError(reply *processor.OperationReply) string {
	if reply.Error != nil {
		return fmt.Sprintf("%s: %s", reply.Error.Code, reply.Error.Message)
	}
	return string(reply.Status)
}

// submitOp publishes a package-lifecycle op to ops.meta and waits for the
// Processor reply on a NATS inbox, unless Submit is set (then it relays
// through that instead — cmd/loupe/gatewayrelay.go's pkgmgrSubmit, scoped to
// Install/Upgrade/UninstallPackage). Mirrors cmd/lattice/output.SubmitOp;
// reproduced here so internal/pkgmgr does not depend on a cmd/ package.
func (i *Installer) submitOp(ctx context.Context, operationType, class, requestID string, payload map[string]any) (*processor.OperationReply, error) {
	if i.Submit != nil {
		return i.Submit(ctx, operationType, class, requestID, payload)
	}
	return i.submitDirectOp(ctx, processor.LaneMeta, operationType, class, requestID, payload, nil)
}

// submitDirectOp publishes an op straight to ops.<lane> over NATS and waits
// for the Processor's reply on a fresh inbox — the direct-NATS path every
// submission uses, package-lifecycle or otherwise. Unlike submitOp it never
// consults Submit: a caller that needs an op on a lane other than the
// package-lifecycle meta lane (e.g. the op-meta retirement guard's
// CancelTask, always on the default lane) calls this directly so the
// Gateway-relay hook — scoped to Install/Upgrade/UninstallPackage — is never
// asked to carry a lane it wasn't built for.
func (i *Installer) submitDirectOp(ctx context.Context, lane processor.Lane, operationType, class, requestID string, payload map[string]any, hint *processor.ContextHint) (*processor.OperationReply, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          lane,
		OperationType: operationType,
		Actor:         i.AdminActor,
		SubmittedAt:   i.Now().Format(time.RFC3339Nano),
		Class:         class,
		Payload:       payloadJSON,
		ContextHint:   hint,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	inbox := nats.NewInbox()
	sub, err := i.Conn.NATS().SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("subscribe inbox: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	subject := "ops." + string(env.Lane)
	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header:  nats.Header{"Lattice-Reply-Inbox": []string{inbox}},
	}

	bctx, cancel := context.WithTimeout(ctx, DefaultBatchTimeout)
	defer cancel()
	if _, err := i.Conn.JetStream().PublishMsg(bctx, msg); err != nil {
		return nil, fmt.Errorf("publish to %s: %w", subject, err)
	}
	replyMsg, err := sub.NextMsgWithContext(bctx)
	if err != nil {
		return nil, fmt.Errorf("wait for reply: %w", err)
	}
	var reply processor.OperationReply
	if err := json.Unmarshal(replyMsg.Data, &reply); err != nil {
		return nil, fmt.Errorf("parse reply: %w", err)
	}
	return &reply, nil
}

// resolveGrants returns a copy of def with each PermissionSpec.GrantsTo
// entry translated through i.RoleIDs. Entries already shaped as a
// vtx.role.<NanoID> prefix or as a raw NanoID are passed through
// unchanged. Unrecognized canonical names are passed through unchanged
// so callers can choose to fail or warn downstream. Defensive against
// i.RoleIDs being nil.
func (i *Installer) resolveGrants(def Definition) Definition {
	if len(def.Permissions) == 0 {
		return def
	}
	out := def
	out.Permissions = make([]PermissionSpec, len(def.Permissions))
	for idx, p := range def.Permissions {
		newGrants := make([]string, 0, len(p.GrantsTo))
		for _, g := range p.GrantsTo {
			if len(g) > len("vtx.role.") && g[:len("vtx.role.")] == "vtx.role." {
				newGrants = append(newGrants, g[len("vtx.role."):])
				continue
			}
			if i.RoleIDs != nil {
				if id, ok := i.RoleIDs[g]; ok && id != "" {
					newGrants = append(newGrants, id)
					continue
				}
			}
			newGrants = append(newGrants, g)
		}
		p.GrantsTo = newGrants
		out.Permissions[idx] = p
	}
	return out
}

// resolvePaneRoles maps pane OfferedToRoles canonical names to role NanoIDs,
// with the same accept-a-vtx.role.-prefix / accept-a-raw-NanoID behavior
// resolveGrants applies to Permission GrantsTo.
func (i *Installer) resolvePaneRoles(def Definition) Definition {
	if len(def.Panes) == 0 {
		return def
	}
	out := def
	out.Panes = make([]PaneSpec, len(def.Panes))
	for idx, p := range def.Panes {
		resolved := make([]string, 0, len(p.OfferedToRoles))
		for _, g := range p.OfferedToRoles {
			if len(g) > len("vtx.role.") && g[:len("vtx.role.")] == "vtx.role." {
				resolved = append(resolved, g[len("vtx.role."):])
				continue
			}
			if i.RoleIDs != nil {
				if id, ok := i.RoleIDs[g]; ok && id != "" {
					resolved = append(resolved, id)
					continue
				}
			}
			resolved = append(resolved, g)
		}
		p.OfferedToRoles = resolved
		out.Panes[idx] = p
	}
	return out
}

// InstallResult summarises an install attempt.
type InstallResult struct {
	PackageName        string
	PackageVersion     string
	PackageKey         string
	DeclaredKeys       []string
	Skipped            bool
	Reason             string
	DependencyWarnings []string
	// LeafBudgetWarnings names every abstract type (dynamic-type-taxonomy-
	// design.md §10.2) whose resolved leaf count this install pushed past its
	// declared LeafBudget. Advisory only — the install still succeeded;
	// rejecting it would let one package's lens narrowing veto another
	// package's type declaration. Empty when nothing is at risk.
	LeafBudgetWarnings []string
}

// ErrVersionMismatch is returned by Install when a different version of
// the same package is already installed. Use `lattice-pkg uninstall <name>`
// followed by `lattice-pkg install` to upgrade.
var ErrVersionMismatch = errors.New("pkgmgr: installed package version differs from requested")

// ErrCanonicalNameCollision is returned by Install when a meta-vertex
// canonicalName the package declares (a DDL, Lens, or op-meta name) already
// exists on a meta-vertex in the kernel. Installing it would silently shadow
// one definition at runtime (the Processor's DDL cache keeps first-seen), so
// the install is rejected.
var ErrCanonicalNameCollision = errors.New("pkgmgr: meta canonicalName already present in the kernel")

// ErrWeaverTargetIDCollision is returned by Install when a weaver targetId the
// package declares already exists on an installed weaver-target spec (Contract
// #10 §10.8: targetId is install-validated for uniqueness across installed
// targets). A weaver target has no canonicalName aspect — its identity is the
// targetId carried on its `.spec` body — so ErrCanonicalNameCollision does not
// cover it. A collision is a genuine hazard beyond the registry's keep-first:
// the colliding package's lens still projects read-model rows under the same
// `<targetId>.` prefix into the shared weaver-targets bucket, interleaving two
// packages' rows, so the install is rejected before any row is written.
var ErrWeaverTargetIDCollision = errors.New("pkgmgr: weaver targetId already present in the kernel")

// ErrUninstallConflict is returned by Uninstall when a declared key was
// modified concurrently between this uninstall's read and its commit
// (F-011 per-key OCC, Contract #8 §8.3). The atomic batch rejects the whole
// tombstone set, so the package is left fully installed — re-run the
// uninstall to retry against the current state.
var ErrUninstallConflict = errors.New("pkgmgr: uninstall conflict — a declared key changed concurrently")

// ErrBootstrapRequired is returned when the core-kv bucket is absent,
// indicating bootstrap has not been run.
var ErrBootstrapRequired = errors.New("pkgmgr: core-kv bucket not found — run bootstrap (or make up) before installing packages")

// installedPackage is the partial deserialization of `vtx.package.<id>.manifest`.
type installedPackage struct {
	Name    string
	Version string
	Key     string // package vertex key
}

// checkCoreBucketExists probes the core-kv bucket and returns
// ErrBootstrapRequired if it is absent (bootstrap has not been run).
// The probe is a lightweight KVListKeys call that fails fast if the
// underlying NATS stream does not exist.
func (i *Installer) checkCoreBucketExists(ctx context.Context) error {
	_, err := i.Conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		// Any error opening the bucket means it doesn't exist yet.
		return fmt.Errorf("%w", ErrBootstrapRequired)
	}
	return nil
}

// IsPackageInstalled reports whether a non-tombstoned package vertex with the
// given canonical name is present in core-kv. It is the install-state probe the
// processor wiring uses to gate rbac-domain-dependent dispatch (the
// cap.roles.<actor> platform routing). A bootstrap-not-run / bucket-absent
// condition is surfaced as an error so the caller can fail loudly rather than
// silently degrading auth.
func IsPackageInstalled(ctx context.Context, conn *substrate.Conn, name string) (bool, error) {
	i := NewInstaller(conn, "")
	pkg, err := i.findInstalledPackage(ctx, name)
	if err != nil {
		return false, err
	}
	return pkg != nil, nil
}

// findInstalledPackage scans `vtx.package.>` and returns the first
// package vertex whose manifest aspect's `name` matches.
func (i *Installer) findInstalledPackage(ctx context.Context, name string) (*installedPackage, error) {
	keys, err := i.Conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: list keys: %w", err)
	}
	for _, k := range keys {
		// Match `vtx.package.<NanoID>.manifest`.
		if len(k) < len(PackageVertexPrefix)+len(".manifest") {
			continue
		}
		if k[:len(PackageVertexPrefix)] != PackageVertexPrefix {
			continue
		}
		if k[len(k)-len(".manifest"):] != ".manifest" {
			continue
		}
		entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("pkgmgr: get %s: %w", k, err)
		}
		var env struct {
			IsDeleted bool           `json:"isDeleted"`
			Data      map[string]any `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			continue
		}
		if env.IsDeleted {
			continue
		}
		gotName, _ := env.Data["name"].(string)
		if gotName != name {
			continue
		}
		gotVersion, _ := env.Data["version"].(string)
		pkgVertexKey := k[:len(k)-len(".manifest")]
		return &installedPackage{Name: gotName, Version: gotVersion, Key: pkgVertexKey}, nil
	}
	return nil, nil
}

// metaScanResult carries the single Core KV key list + its derived
// canonicalName index that one Install/Upgrade/Apply call threads through
// every consumer that needs either: the canonicalName collision guard
// (Install only), cross-package SubtypeOfRef resolution, the installed
// subtypeOf graph scan, and the live-instance guard on a newly-declared
// abstract type (taxonomy.go). fetched is false for the zero value, the
// "nobody has scanned yet" state a def declaring nothing scan-worthy never
// leaves.
type metaScanResult struct {
	keys    []string          // raw KVListKeys(CoreBucket) result
	names   map[string]string // canonicalName -> meta-vertex id, derived from keys
	fetched bool
}

// needsMetaCollisionScan reports whether def declares any meta canonicalName
// that could collide (a DDL, Lens, or op-meta) — false only for a def with
// none of the three, in which case checkCanonicalNameCollision has nothing to
// check and skips its scan entirely. A distinct gate from needsTaxonomyScan
// in taxonomy.go: this one governs an Install-only check that must still run
// for a def with DDLs but no SubtypeOfRef/Abstract at all.
func needsMetaCollisionScan(def Definition) bool {
	return len(def.DDLs) > 0 || len(def.Lenses) > 0 || len(def.OpMetas) > 0
}

// scanMeta performs the ONE KVListKeys pass a caller needs, plus the derived
// canonicalName index, and returns them together so nothing downstream ever
// re-lists the bucket. Callers only invoke this when they have already
// established (via needsMetaCollisionScan / needsTaxonomyScan) that a scan is
// actually needed — this method itself always fetches.
func (i *Installer) scanMeta(ctx context.Context) (metaScanResult, error) {
	keys, err := i.Conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		return metaScanResult{}, fmt.Errorf("pkgmgr: list keys: %w", err)
	}
	names, err := i.canonicalNamesFromKeys(ctx, keys)
	if err != nil {
		return metaScanResult{}, err
	}
	return metaScanResult{keys: keys, names: names, fetched: true}, nil
}

// canonicalNamesFromKeys filters an already-fetched Core KV key list for
// every `vtx.meta.*.canonicalName` aspect key (the same shape the DDL cache
// reads) and returns canonicalName -> meta-vertex id for every LIVE
// (non-tombstoned) one. Targeted GETs only, no KVListKeys of its own.
//
// The id is taken directly from the key's middle segment — NOT validated as
// a Contract #1 NanoID. The DDL cache's own Refresh (ddl_cache.go) accepts
// ANY `vtx.meta.<X>` root regardless of whether X is a real NanoID (the
// shadow-key convention, `vtx.meta.<canonicalName>`, used by test fixtures
// seeded directly via KVPut rather than through a real package install); a
// stricter check here would silently drop such a fixture from both the
// collision guard and cross-package SubtypeOfRef resolution.
func (i *Installer) canonicalNamesFromKeys(ctx context.Context, keys []string) (map[string]string, error) {
	const metaPrefix = "vtx.meta."
	const cnSuffix = ".canonicalName"
	names := make(map[string]string)
	for _, k := range keys {
		if len(k) < len(metaPrefix)+len(cnSuffix) {
			continue
		}
		if k[:len(metaPrefix)] != metaPrefix {
			continue
		}
		if k[len(k)-len(cnSuffix):] != cnSuffix {
			continue
		}
		id := k[len(metaPrefix) : len(k)-len(cnSuffix)]
		if id == "" || strings.Contains(id, ".") {
			continue // not a 3-segment vtx.meta.<id> root
		}
		entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("pkgmgr: get %s: %w", k, err)
		}
		var env struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				Value string `json:"value"`
			} `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			continue
		}
		if env.IsDeleted {
			continue
		}
		names[env.Data.Value] = id
	}
	return names, nil
}

// checkCanonicalNameCollision rejects an install whose declared meta-vertex
// canonicalNames (DDL + Lens + op-meta OperationType) collide with a
// canonicalName already carried by a meta-vertex in the kernel. metaNames is
// the scanMetaCanonicalNames result the caller already computed — a
// collision the install introduces would otherwise silently shadow one
// definition at runtime (the DDL cache keeps first-seen, logs a WARN).
func (i *Installer) checkCanonicalNameCollision(def Definition, metaNames map[string]string) error {
	declared := make(map[string]struct{}, len(def.DDLs)+len(def.Lenses)+len(def.OpMetas))
	for _, d := range def.DDLs {
		declared[d.CanonicalName] = struct{}{}
	}
	for _, l := range def.Lenses {
		declared[l.CanonicalName] = struct{}{}
	}
	for _, o := range def.OpMetas {
		declared[o.OperationType] = struct{}{}
	}
	for name := range declared {
		if id, collides := metaNames[name]; collides {
			return fmt.Errorf("%w: %q (declared by package %q, already on vtx.meta.%s)",
				ErrCanonicalNameCollision, name, def.Name, id)
		}
	}
	return nil
}

// checkWeaverTargetIDCollision rejects an install whose declared weaver
// targetIds collide with a targetId already carried by an installed
// weaver-target `.spec` aspect (Contract #10 §10.8: targetId uniqueness is
// install-validated across installed targets). A weaver target has no
// canonicalName aspect, so checkCanonicalNameCollision cannot catch it; its
// identity lives on the `.spec` body's `targetId`. Mirrors
// checkCanonicalNameCollision: a single KVListKeys pass plus a targeted read of
// only the `vtx.meta.*.spec` keys, filtered to the weaver-target spec class. A
// tombstoned spec is ignored — its targetId is no longer live. Runs AFTER the
// idempotency check in Install so a re-install of the same package never sees
// its own prior targets as a collision.
func (i *Installer) checkWeaverTargetIDCollision(ctx context.Context, def Definition) error {
	declared := make(map[string]struct{}, len(def.WeaverTargets))
	for _, t := range def.WeaverTargets {
		declared[t.TargetID] = struct{}{}
	}
	if len(declared) == 0 {
		return nil
	}

	keys, err := i.Conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		return fmt.Errorf("pkgmgr: list keys: %w", err)
	}
	const metaPrefix = "vtx.meta."
	const specSuffix = ".spec"
	for _, k := range keys {
		if len(k) < len(metaPrefix)+len(specSuffix) {
			continue
		}
		if k[:len(metaPrefix)] != metaPrefix {
			continue
		}
		if k[len(k)-len(specSuffix):] != specSuffix {
			continue
		}
		entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return fmt.Errorf("pkgmgr: get %s: %w", k, err)
		}
		var env struct {
			Class     string `json:"class"`
			IsDeleted bool   `json:"isDeleted"`
			Data      struct {
				TargetID string `json:"targetId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			continue
		}
		if env.IsDeleted || env.Class != weaverTargetSpecClass {
			continue
		}
		if _, collides := declared[env.Data.TargetID]; collides {
			return fmt.Errorf("%w: %q (declared by package %q, already on %s)",
				ErrWeaverTargetIDCollision, env.Data.TargetID, def.Name, k)
		}
	}
	return nil
}

// List returns every currently-installed package summary (one entry per
// non-tombstoned `vtx.package.<id>.manifest` aspect).
func (i *Installer) List(ctx context.Context) ([]*installedPackage, error) {
	keys, err := i.Conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: list keys: %w", err)
	}
	var out []*installedPackage
	for _, k := range keys {
		if len(k) < len(PackageVertexPrefix)+len(".manifest") {
			continue
		}
		if k[:len(PackageVertexPrefix)] != PackageVertexPrefix {
			continue
		}
		if k[len(k)-len(".manifest"):] != ".manifest" {
			continue
		}
		entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			continue
		}
		var env struct {
			IsDeleted bool           `json:"isDeleted"`
			Data      map[string]any `json:"data"`
		}
		if json.Unmarshal(entry.Value, &env) != nil || env.IsDeleted {
			continue
		}
		name, _ := env.Data["name"].(string)
		version, _ := env.Data["version"].(string)
		out = append(out, &installedPackage{
			Name:    name,
			Version: version,
			Key:     k[:len(k)-len(".manifest")],
		})
	}
	return out, nil
}

// PackageName returns the installed package's canonical name.
func (p *installedPackage) PackageName() string { return p.Name }

// PackageVersion returns the installed package's version.
func (p *installedPackage) PackageVersion() string { return p.Version }

// PackageKey returns the installed package's vertex key.
func (p *installedPackage) PackageKey() string { return p.Key }

// Uninstall soft-deletes every Core-KV key recorded in a package's
// manifest aspect. The aspect's `declaredKeys` field lists everything
// the install wrote (DDL + lens + permission + grant + aspect keys);
// the installer enumerates from there.
//
// Soft-delete only — vertices remain queryable for audit.
func (i *Installer) Uninstall(ctx context.Context, packageName string) (*UninstallResult, error) {
	ip, err := i.findInstalledPackage(ctx, packageName)
	if err != nil {
		return nil, err
	}
	if ip == nil {
		return nil, fmt.Errorf("pkgmgr: package %q not installed", packageName)
	}
	manifestKey := ip.Key + ".manifest"
	entry, err := i.Conn.KVGet(ctx, CoreBucket, manifestKey)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: read %s: %w", manifestKey, err)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &env); err != nil {
		return nil, fmt.Errorf("pkgmgr: parse %s: %w", manifestKey, err)
	}
	declaredRaw, _ := env.Data["declaredKeys"].([]any)
	seen := make(map[string]struct{}, len(declaredRaw)+2)
	keys := make([]string, 0, len(declaredRaw)+2)
	appendKey := func(k string) {
		if k == "" {
			return
		}
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	for _, dk := range declaredRaw {
		if s, ok := dk.(string); ok {
			appendKey(s)
		}
	}
	// The manifest aspect's own key is never in declaredKeys (captured before
	// it was added during install); the package vertex itself IS already in
	// declaredKeys (its addCreate runs before the declaredKeys snapshot) — so
	// appendKey dedupes it here rather than reading (and OCC-conditioning) the
	// same key twice in one atomic batch, which would make the batch's own
	// second tombstone race the first's revision advance and self-conflict.
	appendKey(manifestKey)
	appendKey(ip.Key)

	// Build the UninstallPackage payload. Keys that no longer resolve
	// (already hard-deleted) are skipped — there is nothing to tombstone.
	//
	// Each surviving key's tombstone is conditioned on the revision this
	// read just observed (per-key OCC, F-011/Contract #8 §8.3): a
	// concurrent write to a declared key between this read and the commit
	// now fails loudly (ErrUninstallConflict) instead of being silently
	// overwritten. The whole batch is atomic, so a conflict on any one key
	// leaves the package fully installed — never a partial/mixed state.
	declaredEntries := make([]map[string]any, 0, len(keys))
	tombstoned := make([]string, 0, len(keys))
	for _, k := range keys {
		entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("pkgmgr: uninstall read %s: %w", k, err)
		}
		declaredEntries = append(declaredEntries, map[string]any{"key": k, "expectedRevision": entry.Revision})
		tombstoned = append(tombstoned, k)
	}
	if len(declaredEntries) == 0 {
		return &UninstallResult{PackageName: packageName, Note: "nothing to uninstall"}, nil
	}

	payload := map[string]any{
		"name":         packageName,
		"declaredKeys": declaredEntries,
	}
	requestID := deterministicNanoID(packageName, ip.Version, "uninstall-op")
	reply, err := i.submitOp(ctx, "UninstallPackage", "UninstallPackage", requestID, payload)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: submit UninstallPackage: %w", err)
	}
	switch reply.Status {
	case processor.ReplyStatusAccepted, processor.ReplyStatusDuplicate:
		return &UninstallResult{PackageName: packageName, Tombstoned: tombstoned}, nil
	default:
		if reply.Error != nil && reply.Error.Code == processor.ErrCodeRevisionConflict {
			return nil, fmt.Errorf("%w: %s (a concurrent write raced this uninstall — re-run)",
				ErrUninstallConflict, replyError(reply))
		}
		return nil, fmt.Errorf("pkgmgr: UninstallPackage rejected: %s", replyError(reply))
	}
}

// UninstallResult summarises an uninstall.
type UninstallResult struct {
	PackageName string
	Tombstoned  []string
	Note        string
}
