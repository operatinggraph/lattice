package pkgmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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

	// SpecParser supplies the static openCypher parse the install-time
	// narrowed-filter budget gate needs (checkLensLabelCap,
	// dynamic-type-taxonomy-design.md §10.2): it reads each declared lens's
	// label sets so the installer can price a `*` label's declared worst case
	// against the cap before the lens ever reaches Refractor.
	//
	// Optional, and unset leaves that gate silent — which is a diagnostic loss,
	// not an enforcement hole. The property is enforced unconditionally at
	// runtime by pipeline.ConsumerFilter's own cap (broad filter, a warn log,
	// and filterBroadReason "label-cap" on the lens's health entry); what an
	// unwired installer loses is the EARLY, decidable answer at the actor who
	// can fix it. Every production INSTALL entry point wires it (cmd/lattice-pkg,
	// cmd/loupe) — the one production Installer built without it, the probe
	// IsPackageInstalled constructs, never installs anything, so the gate has
	// nothing to be silent about there. It is a field rather than a NewInstaller argument because
	// pkgmgr cannot construct one itself — CypherParser's doc has the cycle.
	SpecParser CypherParser
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

	// Step 2.6 — the ONE Core KV key list this whole Install call needs.
	// buildManifestBatch (and everything it calls: the canonicalName and
	// op-meta collision gates, SubtypeOfRef resolution, the installed subtypeOf
	// graph, the live-instance guard on a newly-declared abstract type) reuses
	// it rather than re-listing. A def declaring no DDL/Lens/OpMeta name — and
	// therefore, since Abstract and SubtypeOfRef both live on DDLSpec, no
	// taxonomy mechanism either — performs zero KV I/O here.
	//
	// The collision gates themselves live in buildManifestBatch, not here, so
	// that an upgrade is held to the same rule as a fresh install; fetching the
	// scan early only spares that path a second key list.
	var scan metaScanResult
	if needsMetaCollisionScan(def) {
		scan, err = i.scanMeta(ctx)
		if err != nil {
			return nil, err
		}
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

	// dynamic-type-taxonomy-design.md §3.5/§8/§10.2: resolve every declared
	// SubtypeOfRef (batch-local first, then the installed kernel), refuse a
	// newly-declared Abstract type that still has live instances
	// (checkAbstractNoLiveInstances, taxonomy.go), and refuse a lens whose own
	// abstract labels cannot fit the narrowed-filter label cap at their declared
	// worst case (checkLensLabelCap, lenslabelcap.go). A def using none of the
	// three (needsTaxonomyScan false, and no lens carrying the `*` sigil)
	// performs zero extra reads — and when the caller (Install) already fetched
	// scan, neither does this def re-list the bucket even when it DOES use one.
	//
	// The lens parse runs FIRST and reads no KV, because whether the label-cap
	// gate needs the scan at all is a property of the parsed specs.
	lensLabels := i.lensSpecLabels(def)
	if !scan.fetched && (needsTaxonomyScan(def) || needsLensCapScan(lensLabels) || needsMetaCollisionScan(def)) {
		var err error
		scan, err = i.scanMeta(ctx)
		if err != nil {
			return nil, nil, "", nil, err
		}
	}
	// The three identity gates, all on this one builder so that a fresh install,
	// an in-place upgrade and a dry-run preview are held to the same rules.
	// Each covers a different identity in the namespace the runtime actually
	// indexes it in: canonicalName for DDLs and lenses, operationType for
	// op-metas, targetId for weaver targets.
	if err := i.checkCanonicalNameCollision(def, scan.names); err != nil {
		return nil, nil, "", nil, err
	}
	if err := i.checkOpMetaOperationTypeCollision(ctx, def, scan); err != nil {
		return nil, nil, "", nil, err
	}
	if err := i.checkWeaverTargetIDCollision(ctx, def, scan); err != nil {
		return nil, nil, "", nil, err
	}
	if err := i.checkAbstractNoLiveInstances(ctx, def, scan); err != nil {
		return nil, nil, "", nil, err
	}
	if err := i.checkLensLabelCap(ctx, def, lensLabels, scan); err != nil {
		return nil, nil, "", nil, err
	}
	subtypeAbstractIDs, leafBudgetWarnings, err := i.resolveTaxonomy(ctx, def, scan)
	if err != nil {
		return nil, nil, "", nil, err
	}
	// The second §10.2 advisory, on the same channel and aimed at the same
	// operator: an abstract type this batch declares with no LeafBudget takes
	// the whole label cap, which refuses every consuming lens that names any
	// other concrete label. Appended after resolveTaxonomy's own sorted set so
	// the combined list stays deterministic (this half is declaration-ordered).
	leafBudgetWarnings = append(leafBudgetWarnings, def.undeclaredLeafBudgetWarnings()...)

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
	return substrate.PackageEntityNanoID(name, version+":"+tag)
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
	return substrate.PackageEntityNanoID(name, versionScope+":"+tag+":"+digest), nil
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
//
// The derivation itself lives in substrate.PackageEntityNanoID: the Processor's
// package-scope guard resolves a package's own vertex key from the same name
// string, and a second implementation of the mapping would let the two readers
// disagree about which package owns a key.
func entityNanoID(name, tag string) string {
	return substrate.PackageEntityNanoID(name, tag)
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

// ErrCanonicalNameCollision is returned when a meta-vertex canonicalName the
// definition declares (a DDL, Lens, or op-meta name) is already carried by a
// meta-vertex the package does not own. Installing it would leave one of the two
// unreachable at runtime: the Processor's DDL cache serves a contested name from
// the lowest-keyed meta-vertex and drops the other from its lookup, so which
// definition survives is decided by NanoID ordering rather than by either
// package.
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

// ErrOpMetaOperationTypeCollision is returned when an op-meta the definition
// declares names an operationType a LIVE op-meta in the kernel already claims.
//
// One operationType, one descriptor: the `.dispatch` optionalReads are the
// Contract #2 §2.5 read-disposition floor the Processor applies to every
// submitter's envelope for that operationType, and two descriptors make that
// floor the union of both (internal/processor/ddl_cache.go's floorsByOpType) —
// one package quietly widening the absence-tolerance of an operation another
// package owns. An op-meta carries no canonicalName aspect, so
// ErrCanonicalNameCollision does not cover it: its identity is the
// operationType on its own root body.
var ErrOpMetaOperationTypeCollision = errors.New("pkgmgr: operationType already carries an op-meta in the kernel")

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

// gateLog is where the install-time collision gates report what they could not
// see. The gates read meta-vertex documents to learn what the kernel already
// claims; a document that will not decode is a claim they cannot see, so the
// gate passes on it — and a blind spot that produces silence is
// indistinguishable from a clean bucket.
//
// slog.Default() rather than an Installer field, chosen after checking the
// wiring: pkgmgr has no logger of its own, and neither CLI that builds an
// Installer (cmd/lattice-pkg, cmd/loupe) constructs one to hand it — so a field
// would be set by nobody, and every one of these lines would reach
// slog.Default() anyway through a longer path. The day either CLI calls
// slog.SetDefault, these follow it with no wiring to remember.
func gateLog() *slog.Logger { return slog.Default() }

// metaRootKeys filters an already-fetched Core KV key list down to 3-segment
// `vtx.meta.<id>` roots, and metaAspectKeys down to one named aspect of them.
// Both are pure over the single KVListKeys result every install already has, so
// no gate below performs a listing of its own.
func metaRootKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !strings.HasPrefix(k, metaVertexPrefix) {
			continue
		}
		id := k[len(metaVertexPrefix):]
		if id == "" || strings.Contains(id, ".") {
			continue
		}
		out = append(out, k)
	}
	return out
}

func metaAspectKeys(keys []string, localName string) []string {
	suffix := "." + localName
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		// The length check is what keeps the id expression below from slicing
		// BACKWARDS. A prefix match and a suffix match can overlap on one short
		// string — `vtx.meta.spec` satisfies both for localName "spec" — and the
		// slice would then be [9:8] and panic. These keys arrive from a listing
		// of the whole bucket, so this filter has to survive any string in it:
		// its job is to reject what is not a meta aspect, never to assume it.
		if len(k) < len(metaVertexPrefix)+len(suffix) {
			continue
		}
		if !strings.HasPrefix(k, metaVertexPrefix) || !strings.HasSuffix(k, suffix) {
			continue
		}
		id := k[len(metaVertexPrefix) : len(k)-len(suffix)]
		if id == "" || strings.Contains(id, ".") {
			continue
		}
		out = append(out, k)
	}
	return out
}

// readMetaDocs batch-reads a set of meta-vertex keys in ONE round trip.
//
// The install gates each need every meta document of one shape, which is a few
// hundred keys on a populated kernel; issued serially that is a few hundred
// round trips per install, per upgrade and per dry-run preview. KVGetMulti is
// the substrate's batched primitive for exactly this (step-4 hydration and
// personalinterest.IsRelevant read the same way).
//
// A key absent from the returned map is absent from the bucket — the batched
// equivalent of ErrKeyNotFound, and the same non-answer each gate already
// skips. A FAILED batch is never that: it returns an error, and every caller
// refuses the install on it, because a gate that cannot read the kernel has not
// found the kernel clean.
func (i *Installer) readMetaDocs(ctx context.Context, keys []string) (map[string]*substrate.KVEntry, error) {
	if len(keys) == 0 {
		return map[string]*substrate.KVEntry{}, nil
	}
	entries, err := i.Conn.KVGetMulti(ctx, CoreBucket, keys)
	if err != nil {
		return nil, fmt.Errorf("pkgmgr: read %d meta keys: %w", len(keys), err)
	}
	return entries, nil
}

// metaScanResult carries the single Core KV key list + its derived
// canonicalName index that one Install/Upgrade/Apply call threads through
// every consumer that needs either: the canonicalName and op-meta collision
// guards, cross-package SubtypeOfRef resolution, the installed subtypeOf graph
// scan, and the live-instance guard on a newly-declared abstract type
// (taxonomy.go). fetched is false for the zero value, the "nobody has scanned
// yet" state a def declaring nothing scan-worthy never leaves.
type metaScanResult struct {
	keys    []string          // raw KVListKeys(CoreBucket) result
	names   map[string]string // canonicalName -> meta-vertex id, derived from keys
	fetched bool
}

// needsMetaCollisionScan reports whether def declares any meta-vertex identity
// that could collide — a DDL or Lens canonicalName, an op-meta operationType, or
// a weaver targetId. False only for a def declaring none of the four, in which
// case all three collision guards have nothing to check and the scan is skipped
// entirely. A distinct gate from needsTaxonomyScan in taxonomy.go: this one must
// still fire for a def with DDLs but no SubtypeOfRef/Abstract at all.
func needsMetaCollisionScan(def Definition) bool {
	return len(def.DDLs) > 0 || len(def.Lenses) > 0 || len(def.OpMetas) > 0 || len(def.WeaverTargets) > 0
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
	const cnSuffix = ".canonicalName"
	aspectKeys := metaAspectKeys(keys, "canonicalName")
	entries, err := i.readMetaDocs(ctx, aspectKeys)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(aspectKeys))
	for _, k := range aspectKeys {
		entry, present := entries[k]
		if !present {
			continue
		}
		id := k[len(metaVertexPrefix) : len(k)-len(cnSuffix)]
		var env struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				Value string `json:"value"`
			} `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			// The name this aspect carries is now invisible to the collision
			// gate, so a package declaring it installs as though nothing owned
			// it. Skipping is still the only option — an unreadable aspect
			// names nothing to compare against — but it must not be the silent
			// option.
			gateLog().Warn("pkgmgr: meta canonicalName aspect will not decode; the name it carries cannot be checked for collisions",
				"key", k, "error", err)
			continue
		}
		if env.IsDeleted {
			continue
		}
		names[env.Data.Value] = id
	}
	return names, nil
}

// checkCanonicalNameCollision rejects a definition whose declared meta-vertex
// canonicalNames collide with a name a meta-vertex in the kernel already
// carries. metaNames is the canonicalNamesFromKeys result the caller already
// computed, mapping a LIVE canonicalName to the meta-vertex id holding it.
//
// What a collision costs: the Processor's DDL cache serves ONE meta-vertex per
// canonicalName (ddl_cache.go's indexByCanonicalName arbitrates a contested name
// to the lowest-keyed root) and drops the loser from both its name index and its
// by-key index. Every gate keyed off the loser's DDL — permittedCommands,
// sensitive, custody, script — then stops applying to it, and the loser is not a
// package's choice: it is whichever NanoID sorts higher.
//
// It runs on the shared batch builder, so a fresh install, an in-place upgrade
// and a dry-run preview are held to one rule. An upgrade re-declares the names
// its own already-installed meta-vertices carry, so the package's OWN roots are
// excluded by version-independent key: a declared name's id is
// entityNanoID(packageName, "<kind>:"+name) (Contract #8 §8.1), and all three
// kind tags are excluded per name, not just the kind declaring it now — a
// package moving a name from a DDL to a lens is editing its own entity, not
// contesting someone else's.
//
// COVERAGE, which is narrower than the set of things a package declares and
// must be read as such. The kernel side of this comparison is
// `vtx.meta.<id>.canonicalName` aspects, and build.go emits that aspect for
// DDLs and lenses ONLY:
//
//   - DDL and Lens names are checked, in both directions: a declared name
//     against every installed one, whichever of the two kinds holds it.
//   - An op-meta's OperationType is NOT checked here, deliberately. It is not a
//     canonicalName: build.go writes no such aspect for an op-meta, so this gate
//     could only ever see the collision from one side — refusing a package whose
//     op-meta names an installed TYPE while blessing the same pair installed in
//     the other order. Nothing about which package installed first should decide
//     that, and now that it runs on upgrade the asymmetry would refuse an
//     upgrade that introduces no new claim at all. The pair is not a runtime
//     collision either: the Processor's DDL cache indexes op-metas in
//     byOpMetaRoot, outside the canonicalName namespace, precisely so an
//     operationType and a type name cannot shadow one another. Op-meta against
//     op-meta — where a real collision lives, in the read-disposition floor — is
//     checkOpMetaOperationTypeCollision's, and it is enforced there.
//     (validateOpMetas' within-package rule still treats the two as one
//     namespace. That is an authoring convenience over a set one author owns,
//     not a claim about the kernel, and it is not weakened by this.)
//   - Panes, loom patterns, roles and retention classes are NOT checked here and
//     must not be assumed covered. A pane's meta-vertex carries `data.paneId`
//     and no canonicalName aspect; roles and retention classes are not
//     `vtx.meta.*` keys at all, so canonicalNamesFromKeys never lists them.
//     Weaver targets are covered, by their own gate on their own identity
//     (checkWeaverTargetIDCollision).
func (i *Installer) checkCanonicalNameCollision(def Definition, metaNames map[string]string) error {
	// ownIDs[name] is every meta-vertex id THIS package could be carrying that
	// name on. Keyed by name rather than by kind so a name's ownership survives
	// the kind it is declared under changing between versions.
	ownIDs := map[string]map[string]struct{}{}
	claim := func(name string) {
		if _, seen := ownIDs[name]; seen {
			return
		}
		ownIDs[name] = map[string]struct{}{
			DDLID(def.Name, name):  {},
			LensID(def.Name, name): {},
		}
	}
	for _, d := range def.DDLs {
		claim(d.CanonicalName)
	}
	for _, l := range def.Lenses {
		claim(l.CanonicalName)
	}

	// Sorted, so a definition introducing two collisions names the same one on
	// every run: an author fixing them one at a time needs the second report to
	// be a consequence of the first fix, not of map iteration order.
	for _, name := range sortedNameKeys(ownIDs) {
		id, collides := metaNames[name]
		if !collides {
			continue
		}
		if _, mine := ownIDs[name][id]; mine {
			continue
		}
		return fmt.Errorf("%w: %q (declared by package %q; already carried by vtx.meta.%s, which this package does not own). The Processor's DDL cache serves one meta-vertex per canonicalName and drops the other from its lookup entirely, so whichever of the two sorts higher stops enforcing its permittedCommands, sensitivity, custody and script. Rename this DDL/lens, or upgrade the package that already owns the name",
			ErrCanonicalNameCollision, name, def.Name, id)
	}
	return nil
}

// sortedNameKeys returns a name-keyed map's keys in ascending order, so a gate
// iterating it reports the same finding on every run.
func sortedNameKeys(m map[string]map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkOpMetaOperationTypeCollision refuses a definition whose op-meta
// operationTypes are already claimed by a live op-meta somebody else installed.
//
// It is the runtime counterpart to lint-package-standard's S11, and the reason
// the invariant needs one is that S11 iterates the compiled-in package registry
// while op-metas also reach the kernel by a path no lint can see: "opMeta" is
// an enabled capability artifact kind (capabilitymaterializer.go), so an
// APPROVED capability proposal materializes a Definition carrying a single
// OpMetaSpec and installs it under whatever package name the proposal names.
// Definition.validateOpMetas and validateCanonicalNameUniqueness are both
// per-Definition, so a one-entry Definition is trivially unique against itself
// and sees nothing else in the kernel. The lint keeps its job — catching the
// collision in the authored corpus, where a build fails before anything is
// installed; this catches the ones that never pass through it.
//
// It runs on the shared batch builder rather than beside the Install-only
// collision guards, because an op-meta enters the kernel through exactly one
// builder: a fresh install, an in-place upgrade and a dry-run preview all pass
// here, so the refusal does not depend on which command carried the definition,
// and a preview reports it before the operator commits to an apply.
//
// A definition's OWN root is excluded by KEY rather than by ownership
// bookkeeping: an op-meta's id is entityNanoID(packageName, "opMeta:"+
// operationType) (Contract #8 §8.1, version-independent), so the single root a
// re-install or upgrade of this package would rewrite is the only root it may
// legitimately share an operationType with, and every other claimant belongs to
// someone else by construction.
//
// The live-claimant test is `data.operationType` on a non-tombstoned root — the
// same discriminator the Processor's DDL cache uses to tell an op-meta from a
// DDL, deliberately, so the two cannot disagree about which roots carry a floor.
func (i *Installer) checkOpMetaOperationTypeCollision(ctx context.Context, def Definition, scan metaScanResult) error {
	if len(def.OpMetas) == 0 {
		return nil
	}
	ownRoot := make(map[string]string, len(def.OpMetas))
	for _, o := range def.OpMetas {
		ownRoot[o.OperationType] = metaVertexPrefix + entityNanoID(def.Name, "opMeta:"+o.OperationType)
	}
	roots := metaRootKeys(scan.keys)
	entries, err := i.readMetaDocs(ctx, roots)
	if err != nil {
		return err
	}
	for _, k := range roots {
		entry, present := entries[k]
		if !present {
			continue
		}
		var env struct {
			IsDeleted bool `json:"isDeleted"`
			Data      struct {
				OperationType string `json:"operationType"`
			} `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			// A root that will not decode cannot be seen to claim an
			// operationType, so this gate passes it — the same blind spot the
			// canonicalName scan reports, and reported the same way.
			gateLog().Warn("pkgmgr: meta root will not decode; any operationType it claims cannot be checked for collisions",
				"key", k, "error", err)
			continue
		}
		if env.IsDeleted || env.Data.OperationType == "" {
			continue
		}
		own, declaredHere := ownRoot[env.Data.OperationType]
		if !declaredHere || own == k {
			continue
		}
		return fmt.Errorf("%w: %q (declared by package %q, which would write %s; already carried by %s). One operationType carries one descriptor — its optionalReads are the Contract #2 §2.5 read floor the Processor applies to every envelope for this operationType, and a second descriptor makes that floor the union of both, so this package would be changing the read disposition of an operation it does not own. Give the op a vertical-unique name (the cafe-ledger CreditCafeAccount idiom) if the two really are distinct operations, or upgrade the package that already owns it",
			ErrOpMetaOperationTypeCollision, env.Data.OperationType, def.Name, own, k)
	}
	return nil
}

// checkWeaverTargetIDCollision refuses a definition whose declared weaver
// targetIds are already carried by an installed weaver-target `.spec` aspect
// (Contract #10 §10.8: targetId uniqueness is install-validated across installed
// targets). A weaver target has no canonicalName aspect, so the canonicalName
// gate cannot catch it; its identity lives on the `.spec` body's `targetId`, and
// two targets sharing one interleave their rows under the same `<targetId>.`
// prefix in the shared weaver-targets bucket.
//
// The third gate on this builder, and it is here for the reason the other two
// are: an upgrade is not a weaker install. A definition that could not be
// installed fresh must not arrive by upgrading into place, and a preview must
// report the same refusal an apply would.
//
// Self-exclusion is by version-independent KEY, as for the other two: a weaver
// target's meta-vertex id is entityNanoID(packageName, "weaverTarget:"+targetID)
// (Contract #8 §8.1), so the one spec a re-install or upgrade of this package
// rewrites is the only spec it may share a targetId with.
func (i *Installer) checkWeaverTargetIDCollision(ctx context.Context, def Definition, scan metaScanResult) error {
	if len(def.WeaverTargets) == 0 {
		return nil
	}
	const specSuffix = ".spec"
	ownSpec := make(map[string]string, len(def.WeaverTargets))
	for _, t := range def.WeaverTargets {
		ownSpec[t.TargetID] = metaVertexPrefix + entityNanoID(def.Name, "weaverTarget:"+t.TargetID) + specSuffix
	}

	specKeys := metaAspectKeys(scan.keys, "spec")
	entries, err := i.readMetaDocs(ctx, specKeys)
	if err != nil {
		return err
	}
	for _, k := range specKeys {
		entry, present := entries[k]
		if !present {
			continue
		}
		var env struct {
			Class     string `json:"class"`
			IsDeleted bool   `json:"isDeleted"`
			Data      struct {
				TargetID string `json:"targetId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			// A spec that will not decode cannot be seen to claim a targetId.
			// Same blind spot, same report as the other two gates. A lens spec
			// lives at this key shape too, which is why the class filter below
			// is the discriminator and a decode failure is not simply "not a
			// weaver target".
			gateLog().Warn("pkgmgr: meta spec aspect will not decode; any weaver targetId it claims cannot be checked for collisions",
				"key", k, "error", err)
			continue
		}
		if env.IsDeleted || env.Class != weaverTargetSpecClass {
			continue
		}
		own, declaredHere := ownSpec[env.Data.TargetID]
		if !declaredHere || own == k {
			continue
		}
		return fmt.Errorf("%w: %q (declared by package %q, which would write %s; already carried by %s). A targetId is the row prefix a weaver target writes under in the shared weaver-targets bucket, so two claimants interleave two packages' rows under one prefix. Give this target a package-unique targetId, or upgrade the package that already owns it",
			ErrWeaverTargetIDCollision, env.Data.TargetID, def.Name, own, k)
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
// the installer enumerates from there. Retention-class holder keys are the one
// exception: never tombstoned, and reported instead under
// RetentionHoldersPreserved or RetentionHoldersAlreadyStranded by whether the
// holder is still live.
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
	retentionHolders := make([]string, 0, 2)
	appendKey := func(k string) {
		if k == "" {
			return
		}
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		if strings.HasPrefix(k, "vtx."+RetentionClassVertexType+".") {
			// A retention-class holder (the vtx.retentionclass.<id> root or its
			// .retentionPolicy aspect) never enters the tombstone set: only
			// ShredRetentionClassKey may destroy a class's DEK and its
			// vertex_alive guard refuses a tombstoned holder forever, so
			// tombstoning here would strand the key it custodies beyond any
			// reach. Mirrors diffManifest's old\new exclusion (see
			// diffSummary.retentionHoldersPreserved) — an uninstall must not
			// auto-tombstone a holder any more than an upgrade or rename may.
			// The holder is left live but undeclared, still shreddable on the
			// controller's retention schedule. Which of the two reported buckets
			// it lands in depends on its committed state, resolved below.
			retentionHolders = append(retentionHolders, k)
			return
		}
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

	// Classify each excluded holder by what is actually committed under it.
	// The exclusion above is a prefix match, which says nothing about the key's
	// state, and the two states that are not "live" must not be reported as
	// preserved: an already-tombstoned holder is past ShredRetentionClassKey's
	// vertex_alive guard forever (pre-existing damage this uninstall neither
	// caused nor can undo — the operator has to be able to find it), and an
	// absent one has nothing to preserve at all.
	retentionHoldersPreserved := make([]string, 0, len(retentionHolders))
	retentionHoldersAlreadyStranded := make([]string, 0, len(retentionHolders))
	for _, k := range retentionHolders {
		entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("pkgmgr: uninstall read %s: %w", k, err)
		}
		var holder struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if err := json.Unmarshal(entry.Value, &holder); err != nil {
			return nil, fmt.Errorf("pkgmgr: parse %s: %w", k, err)
		}
		if holder.IsDeleted {
			retentionHoldersAlreadyStranded = append(retentionHoldersAlreadyStranded, k)
			continue
		}
		retentionHoldersPreserved = append(retentionHoldersPreserved, k)
	}

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
		// Every tombstonable key is already gone. "Nothing to uninstall" is only
		// the whole truth when nothing was deliberately held back either: with a
		// preserved holder in hand the package left something live on purpose,
		// and an operator reading this note is the one who has to know that.
		note := "nothing to uninstall"
		if parts := retentionHolderNotes(retentionHoldersPreserved, retentionHoldersAlreadyStranded); len(parts) > 0 {
			note = strings.Join(append([]string{"nothing to tombstone"}, parts...), "; ")
		}
		return &UninstallResult{
			PackageName:                     packageName,
			Note:                            note,
			RetentionHoldersPreserved:       retentionHoldersPreserved,
			RetentionHoldersAlreadyStranded: retentionHoldersAlreadyStranded,
		}, nil
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
		return &UninstallResult{
			PackageName:                     packageName,
			Tombstoned:                      tombstoned,
			RetentionHoldersPreserved:       retentionHoldersPreserved,
			RetentionHoldersAlreadyStranded: retentionHoldersAlreadyStranded,
		}, nil
	default:
		if reply.Error != nil && reply.Error.Code == processor.ErrCodeRevisionConflict {
			return nil, fmt.Errorf("%w: %s (a concurrent write raced this uninstall — re-run)",
				ErrUninstallConflict, replyError(reply))
		}
		return nil, fmt.Errorf("pkgmgr: UninstallPackage rejected: %s", replyError(reply))
	}
}

// retentionHolderNotes renders the operator-facing sentences for an
// uninstall's two retention-holder buckets, in the order they matter: what was
// held back live, then what was already past reach before this run started.
// Returns nothing when both buckets are empty, so a caller can compose it into
// a larger note without a trailing separator.
func retentionHolderNotes(preserved, alreadyStranded []string) []string {
	var parts []string
	if len(preserved) > 0 {
		parts = append(parts, fmt.Sprintf("%d retention-class holder key(s) left live but undeclared so ShredRetentionClassKey can still destroy the class key",
			len(preserved)))
	}
	if len(alreadyStranded) > 0 {
		parts = append(parts, fmt.Sprintf("%d retention-class holder key(s) were ALREADY tombstoned before this run — their class key can no longer be destroyed by ShredRetentionClassKey",
			len(alreadyStranded)))
	}
	return parts
}

// UninstallResult summarises an uninstall.
type UninstallResult struct {
	PackageName string
	Tombstoned  []string
	Note        string

	// RetentionHoldersPreserved names the LIVE vtx.retentionclass.* keys this
	// uninstall left live-but-undeclared instead of tombstoning, because only
	// ShredRetentionClassKey may destroy a class's DEK and it refuses a
	// tombstoned holder forever. The uninstall succeeded; these holders remain
	// shreddable on the controller's retention schedule.
	RetentionHoldersPreserved []string

	// RetentionHoldersAlreadyStranded names the declared vtx.retentionclass.*
	// keys that were ALREADY tombstoned when this uninstall read them. They are
	// excluded from the tombstone set like any holder, but they are not
	// preserved: ShredRetentionClassKey's vertex_alive guard refuses them, so
	// the class key each custodies is beyond every destruction path. Reported
	// separately so the operator can escalate real stranded custody instead of
	// reading it as a benign preservation.
	RetentionHoldersAlreadyStranded []string
}
