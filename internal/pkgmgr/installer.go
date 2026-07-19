package pkgmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
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
	if def.Name == "" {
		return nil, fmt.Errorf("pkgmgr: Definition.Name is required")
	}
	if def.Version == "" {
		return nil, fmt.Errorf("pkgmgr: Definition.Version is required")
	}
	if i.AdminActor == "" {
		return nil, fmt.Errorf("pkgmgr: AdminActor is required")
	}

	// Fail closed before any KV operation: e.g. a lens whose declared Bucket is
	// a reserved short alias would auto-create a phantom bucket no reader
	// consults (silent mis-targeting of the auth plane).
	if err := def.validateAll(); err != nil {
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
	if err := i.checkCanonicalNameCollision(ctx, def); err != nil {
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
	// version-independent entity keys, the full create batch). Shared with
	// Upgrade, which needs the identical new key set + bodies.
	ops, declared, pkgKey, err := i.buildManifestBatch(def)
	if err != nil {
		return nil, err
	}
	res.PackageKey = pkgKey

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
// declared-key list, and the package vertex key. Shared by Install (the fresh
// create) and Upgrade (the new side of the diff): both need the identical
// version-independent key set + bodies, so deriving them in one place keeps the
// upgrade diff aligned with what a fresh install would write. Field-level
// validation (validateLensBuckets etc.) and the install-specific idempotency /
// canonicalName-collision checks remain with the callers.
func (i *Installer) buildManifestBatch(def Definition) ([]installMutation, []string, string, error) {
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

	// Resolve any unresolved canonical names in GrantsTo via i.RoleIDs.
	def = i.resolveGrants(def)

	// Validate all GrantsTo entries resolved to valid NanoIDs. A remaining
	// canonical name (non-NanoID) means the bootstrap JSON is missing the
	// role's primordialID or the package did not declare the role in
	// Definition.Roles. A dangling grant link would be written silently and
	// cause PermissionDenied at runtime with no helpful diagnostic.
	for idx, p := range def.Permissions {
		for _, g := range p.GrantsTo {
			if !substrate.IsValidNanoID(g) {
				return nil, nil, "", fmt.Errorf("pkgmgr: Permission[%d] %q: GrantsTo entry %q is not a valid NanoID — role may not be installed or bootstrap JSON is missing the role ID", idx, p.OperationType, g)
			}
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

	ops, declared, err := i.buildInstallBatch(def, pkgKey, ddlNanoIDs, lensNanoIDs, permNanoIDs, roleNanoIDs,
		weaverTargetNanoIDs, loomPatternNanoIDs, opMetaNanoIDs)
	if err != nil {
		return nil, nil, "", err
	}
	return ops, declared, pkgKey, nil
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

// submitOp publishes an op to ops.meta and waits for the Processor reply on
// a NATS inbox, unless Submit is set (then it relays through that instead).
// Mirrors cmd/lattice/output.SubmitOp; reproduced here so internal/pkgmgr
// does not depend on a cmd/ package.
func (i *Installer) submitOp(ctx context.Context, operationType, class, requestID string, payload map[string]any) (*processor.OperationReply, error) {
	if i.Submit != nil {
		return i.Submit(ctx, operationType, class, requestID, payload)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	env := &processor.OperationEnvelope{
		RequestID:     requestID,
		Lane:          processor.LaneMeta,
		OperationType: operationType,
		Actor:         i.AdminActor,
		SubmittedAt:   i.Now().Format(time.RFC3339Nano),
		Class:         class,
		Payload:       payloadJSON,
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

// InstallResult summarises an install attempt.
type InstallResult struct {
	PackageName        string
	PackageVersion     string
	PackageKey         string
	DeclaredKeys       []string
	Skipped            bool
	Reason             string
	DependencyWarnings []string
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

// checkCanonicalNameCollision rejects an install whose declared meta-vertex
// canonicalNames (DDL + Lens + op-meta OperationType) collide with a
// canonicalName already carried by a meta-vertex in the kernel. It is a single
// KVListKeys pass plus a targeted read of only the `vtx.meta.*.canonicalName`
// aspect keys (the same shape the DDL cache reads), so it does not over-fetch.
// A tombstoned aspect is ignored — its canonicalName is no longer live.
func (i *Installer) checkCanonicalNameCollision(ctx context.Context, def Definition) error {
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
	if len(declared) == 0 {
		return nil
	}

	keys, err := i.Conn.KVListKeys(ctx, CoreBucket)
	if err != nil {
		return fmt.Errorf("pkgmgr: list keys: %w", err)
	}
	const metaPrefix = "vtx.meta."
	const cnSuffix = ".canonicalName"
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
		entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return fmt.Errorf("pkgmgr: get %s: %w", k, err)
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
		if _, collides := declared[env.Data.Value]; collides {
			return fmt.Errorf("%w: %q (declared by package %q, already on %s)",
				ErrCanonicalNameCollision, env.Data.Value, def.Name, k)
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
