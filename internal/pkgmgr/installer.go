package pkgmgr

import (
	"context"
	"crypto/sha256"
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

	// Step 2.5 — mint deterministic NanoIDs for any roles this package
	// declares, and register them in RoleIDs so this package's own
	// GrantsTo entries (and the grant links built below) resolve to the
	// role's in-batch NanoID. The role vertices/aspects/index are created
	// in the SAME install batch (Story 1.5.5 — no substrate-direct
	// PreInstall) and captured in declaredKeys (closes F-001).
	roleNanoIDs := make([]string, len(def.Roles))
	if len(def.Roles) > 0 && i.RoleIDs == nil {
		i.RoleIDs = map[string]string{}
	}
	for idx, r := range def.Roles {
		id := deterministicNanoID(def.Name, def.Version, "role:"+r.CanonicalName)
		roleNanoIDs[idx] = id
		i.RoleIDs[r.CanonicalName] = id
	}

	// Resolve any unresolved canonical names in GrantsTo via i.RoleIDs.
	def = i.resolveGrants(def)

	// Validate all GrantsTo entries resolved to valid NanoIDs. Any
	// remaining canonical name (non-NanoID) means the bootstrap JSON is
	// missing the role's primordialID or the package did not declare the
	// role in Definition.Roles. A dangling grant link would be written
	// silently and cause PermissionDenied at runtime with no helpful
	// diagnostic.
	for idx, p := range def.Permissions {
		for _, g := range p.GrantsTo {
			if !substrate.IsValidNanoID(g) {
				return nil, fmt.Errorf("pkgmgr: Permission[%d] %q: GrantsTo entry %q is not a valid NanoID — role may not be installed or bootstrap JSON is missing the role ID", idx, p.OperationType, g)
			}
		}
	}

	// Step 3 — build the mutation manifest with DETERMINISTIC NanoIDs
	// (derived from package name+version+entity tag) so a re-install
	// produces identical keys and the create-only batch is idempotent.
	pkgKey := PackageVertexPrefix + deterministicNanoID(def.Name, def.Version, "package")
	res.PackageKey = pkgKey

	ddlNanoIDs := make([]string, len(def.DDLs))
	lensNanoIDs := make([]string, len(def.Lenses))
	permNanoIDs := make([]string, len(def.Permissions))
	for idx, d := range def.DDLs {
		ddlNanoIDs[idx] = deterministicNanoID(def.Name, def.Version, "ddl:"+d.CanonicalName)
	}
	for idx, l := range def.Lenses {
		lensNanoIDs[idx] = deterministicNanoID(def.Name, def.Version, "lens:"+l.CanonicalName)
	}
	for idx, p := range def.Permissions {
		permNanoIDs[idx] = deterministicNanoID(def.Name, def.Version,
			fmt.Sprintf("perm:%d:%s", idx, p.OperationType))
	}

	ops, declared, err := i.buildInstallBatch(def, pkgKey, ddlNanoIDs, lensNanoIDs, permNanoIDs, roleNanoIDs)
	if err != nil {
		return nil, err
	}

	// Step 4 — submit the InstallPackage op to the Processor. The op
	// carries the pre-built manifest; the kernel script enforces
	// guardrails and emits the mutations; the Processor commits them in
	// one atomic batch and invalidates the vtx.meta.* DDL cache in-commit.
	payload := map[string]any{
		"name":      def.Name,
		"version":   def.Version,
		"mutations": ops,
	}
	// Deterministic requestId from name+version so a re-submit dedup-
	// short-circuits at step 2 (idempotent install).
	requestID := deterministicNanoID(def.Name, def.Version, "install-op")
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

// deterministicNanoID derives a stable Contract #1 NanoID from the
// package name+version+entity tag. Same inputs → same ID on every run,
// so re-install is idempotent and produces identical keys.
func deterministicNanoID(name, version, tag string) string {
	sum := sha256.Sum256([]byte("lattice-pkg:" + name + ":" + version + ":" + tag))
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

// submitOp publishes an op to ops.meta and waits for the Processor reply
// on a NATS inbox. Mirrors cmd/lattice/output.SubmitOp; reproduced here
// so internal/pkgmgr does not depend on a cmd/ package.
func (i *Installer) submitOp(ctx context.Context, operationType, class, requestID string, payload map[string]any) (*processor.OperationReply, error) {
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
	keys := make([]string, 0, len(declaredRaw)+2)
	for _, dk := range declaredRaw {
		if s, ok := dk.(string); ok && s != "" {
			keys = append(keys, s)
		}
	}
	// Manifest aspect (not in declaredKeys — captured before its own key
	// was added during install) + package vertex itself, tombstoned last.
	keys = append(keys, manifestKey, ip.Key)

	// Build the UninstallPackage payload. Keys that no longer resolve
	// (already hard-deleted) are skipped — there is nothing to tombstone.
	//
	// Uninstall tombstones UNCONDITIONALLY (no per-key expectedRevision).
	// The UninstallPackage script supports per-key OCC, but the canonical
	// expectedRevision is the per-SUBJECT sequence the Committer returns in
	// OperationReply.Revisions (the install reply) — NOT the stream-level
	// revision KVGet exposes. Threading the install-time committed
	// revisions through to a later uninstall is heavier than this story
	// warrants, so OCC is deferred (CAR: per-key-revision uninstall OCC).
	// Window: a concurrent Processor write to a declared key between this
	// read and the commit is silently overwritten. The whole batch is
	// still atomic, so no partial/mixed state can result; the only loss is
	// the lost-update guarantee on a key being uninstalled — acceptable for
	// an admin-driven uninstall.
	declaredEntries := make([]map[string]any, 0, len(keys))
	tombstoned := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, err := i.Conn.KVGet(ctx, CoreBucket, k); err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("pkgmgr: uninstall read %s: %w", k, err)
		}
		declaredEntries = append(declaredEntries, map[string]any{"key": k})
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
		return nil, fmt.Errorf("pkgmgr: UninstallPackage rejected: %s", replyError(reply))
	}
}

// UninstallResult summarises an uninstall.
type UninstallResult struct {
	PackageName string
	Tombstoned  []string
	Note        string
}
