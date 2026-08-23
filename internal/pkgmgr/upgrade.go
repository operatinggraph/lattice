package pkgmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/refractor/reloadpin"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// ErrNotInstalled is returned by Upgrade when no base install of the package
// is present. Upgrade is an in-place diff against the installed manifest, so it
// requires an existing package vertex to upgrade from.
var ErrNotInstalled = errors.New("pkgmgr: package not installed — install before upgrading")

// ErrUpgradeConflict is returned by Upgrade when a surviving key's diff-read
// revision was modified concurrently before the upgrade committed (F-011
// per-key OCC, Contract #8 §8.6 — the upgrade sibling of ErrUninstallConflict).
// The atomic batch rejects the whole delta, so the package is left on its
// prior version — re-run the upgrade to retry against the current state.
var ErrUpgradeConflict = errors.New("pkgmgr: upgrade conflict — a declared key changed concurrently")

// UpgradeResult summarises an in-place package upgrade.
type UpgradeResult struct {
	PackageName string
	FromVersion string
	ToVersion   string
	Created     int
	Updated     int
	Tombstoned  int
	Skipped     bool
	Reason      string
	// ReactivationRequired names lens spec edits this upgrade committed that a
	// running Refractor cannot hot-reload. The upgrade succeeded; these lenses
	// keep serving their activated spec until re-activated, and saying so is the
	// difference between an upgrade that applied and one that only landed.
	ReactivationRequired []string

	// RevocationsRespected counts surviving declared GRANT/ROLE topology keys
	// (vtx.permission.*, vtx.role.*, lnk.permission.*.grantedBy.role.*, …—
	// never vtx.meta.* definitions, see diffManifest) this upgrade left
	// tombstoned because an out-of-band op (RevokePermission/
	// TombstonePermission/TombstoneRole) had already revoked them. The
	// upgrade succeeded; these grants stay revoked rather than being silently
	// un-tombstoned by the body-diff update path.
	RevocationsRespected int

	// RetentionHoldersPreserved counts LIVE vtx.retentionclass.* keys this
	// upgrade left live-but-undeclared instead of tombstoning them off the
	// removal path, because only ShredRetentionClassKey may destroy a class's
	// DEK and it refuses a tombstoned holder forever. The upgrade succeeded;
	// these holders remain shreddable on the controller's retention schedule.
	RetentionHoldersPreserved int

	// RetentionHoldersAlreadyStranded counts vtx.retentionclass.* keys the
	// removal path found ALREADY tombstoned. They are not preserved in any
	// useful sense: ShredRetentionClassKey's vertex_alive guard refuses them,
	// so the DEK each custodies can no longer be destroyed by any path. This
	// upgrade did not cause that — it reports it, because an operator holding
	// a retention obligation on such a class needs to escalate rather than
	// read a "preserved" count and be reassured.
	RetentionHoldersAlreadyStranded int

	// SecureColumnsWidened counts lens secure columns whose declared
	// holderTypes this upgrade refused to narrow, writing the union with the
	// committed spec instead (retention-class-key-custody-design.md §24.6). The
	// upgrade succeeded; the package's narrowed declaration did not take effect,
	// because ciphertext already written under a dropped holder type would
	// otherwise become invisible to every destruction-readiness reader.
	SecureColumnsWidened int

	// SecureColumnsRetired counts the secure COLUMNS whose committed
	// targetConfig.secureColumns entry this upgrade erased because the package
	// declared the retirement in Definition.RetiredSecureColumns — the same
	// unit SecureColumnsWidened counts, so a removed lens that took twenty
	// secure columns with it reports twenty, not one. Each is an author
	// attestation that the ciphertext those holder types encrypted is safe to
	// stop tracking; the platform verified nothing. Reported so an operator
	// sees custody history leaving the system rather than only its arrival.
	SecureColumnsRetired int

	// SecureColumnRetirementsUnused labels every
	// Definition.RetiredSecureColumns entry this upgrade matched to no actual
	// erasure, as "<lens> / <column selector>". An unused entry excused
	// nothing here, but it sits in the package file looking load-bearing, and
	// a retirement that has outlived the edit it was written for is exactly
	// what a later author would otherwise mistake for coverage of theirs.
	SecureColumnRetirementsUnused []string

	// LeafBudgetWarnings names every subtypeOf target (dynamic-type-taxonomy-
	// design.md §10.2) whose resolved leaf count this upgrade pushed past its
	// declared LeafBudget. Advisory only — the upgrade still succeeded.
	LeafBudgetWarnings []string
}

// Upgrade applies an in-place version upgrade of an already-installed package
// (Contract #8 §8.6). It rebuilds the package manifest on version-independent
// keys (§8.1), diffs the new key set + bodies against the installed package's
// recorded declaredKeys, and submits one UpgradePackage op carrying the
// create / update / tombstone delta — committed atomically by the Processor.
//
// Steps:
//  1. Validate the Definition (mirrors Install's field-level checks).
//  2. Find the installed package + read its old declaredKeys (the old key set).
//     Absent → ErrNotInstalled (upgrade requires a base).
//  3. Rebuild the new manifest with the shared buildManifestBatch machinery.
//  4. Diff by key: new\old → create; old\new → tombstone;
//     new∩old → update iff the new logical body differs from the committed one.
//  5. Submit one UpgradePackage op (no mutations → a no-op, reported Skipped).
//
// P2-clean: it submits an op; it never writes Core KV directly. Protected
// kernel/auth roots cannot be touched — the Processor's step-8 guard rejects
// any update/tombstone of a protected root, path-independently.
func (i *Installer) Upgrade(ctx context.Context, def Definition) (*UpgradeResult, error) {
	def, err := i.preflight(def)
	if err != nil {
		return nil, err
	}
	if err := i.checkCoreBucketExists(ctx); err != nil {
		return nil, err
	}

	// Step 2 — the installed base. Upgrade requires one (unlike Apply, which
	// falls back to a fresh install when the package is absent).
	existing, err := i.findInstalledPackage(ctx, def.Name)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("%w: %q", ErrNotInstalled, def.Name)
	}

	// Steps 3–4 — rebuild the new manifest + diff into the create/update/
	// tombstone delta.
	mutations, sum, leafBudgetWarnings, err := i.computeDeltaAgainst(ctx, existing, def)
	if err != nil {
		return nil, err
	}

	res := &UpgradeResult{
		PackageName: def.Name,
		FromVersion: existing.Version,
		ToVersion:   def.Version,
		Created:     sum.created + sum.revived,
		Updated:     sum.updated,
		Tombstoned:  sum.tombstoned,

		ReactivationRequired:            sum.reactivation,
		RevocationsRespected:            sum.revocationsRespected,
		RetentionHoldersPreserved:       sum.retentionHoldersPreserved,
		RetentionHoldersAlreadyStranded: sum.retentionHoldersAlreadyStranded,
		SecureColumnsWidened:            sum.secureColumnsWidened,
		LeafBudgetWarnings:              leafBudgetWarnings,
	}

	// The Secure-Lens key-custody retirement guard: refuse an undeclared
	// erasure of a committed targetConfig.secureColumns entry (retention-class-
	// key-custody-design.md §30). It runs BEFORE the empty-delta return below,
	// unlike the op-meta guard further down: no erasure can actually reach an
	// empty delta (a dropped column changes the spec body, and a removed lens
	// emits its own tombstone), but the check is pure and side-effect-free, so
	// placing it first also validates the declarations themselves on every run
	// rather than only on the runs that happen to carry a mutation.
	retired, unusedRetirements, err := enforceSecureColumnRetirement(def, sum.droppedSecureColumns)
	if err != nil {
		return nil, err
	}
	res.SecureColumnsRetired = retired
	res.SecureColumnRetirementsUnused = unusedRetirements

	if len(mutations) == 0 {
		res.Skipped = true
		res.Reason = noChangesReason(def.Name, sum.revocationsRespected, sum.secureColumnsWidened)
		return res, nil
	}

	// The op-meta retirement guard: refuse an undeclared op-meta drop, and
	// cancel a RetireCancelsOpenTasks-declared drop's open referents before
	// the tombstone below ever lands (opmeta-retirement-open-task-guard-
	// design.md §2).
	if len(sum.tombstonedOpMetas) > 0 {
		if err := i.enforceOpMetaDisposition(ctx, def, sum.tombstonedOpMetas); err != nil {
			return nil, err
		}
	}

	// Step 5 — submit one UpgradePackage op.
	if err := i.submitUpgradeOp(ctx, def, existing.Version, mutations); err != nil {
		return nil, err
	}
	return res, nil
}

// preflight runs the required-field + field-level validation shared by Install,
// Upgrade, and Apply before any KV operation, and returns the COMPOSED
// Definition every downstream step must build from: read-grant walks are
// compiled here, so the generated cap-read producers travel with the data
// lenses they grant for through manifest-batch, diff, and submit alike.
func (i *Installer) preflight(def Definition) (Definition, error) {
	if def.Name == "" {
		return Definition{}, fmt.Errorf("pkgmgr: Definition.Name is required")
	}
	if def.Version == "" {
		return Definition{}, fmt.Errorf("pkgmgr: Definition.Version is required")
	}
	if i.AdminActor == "" {
		return Definition{}, fmt.Errorf("pkgmgr: AdminActor is required")
	}
	def, err := def.ExpandReadGrantWalks()
	if err != nil {
		return Definition{}, err
	}
	if err := def.validateAll(); err != nil {
		return Definition{}, err
	}
	return def, nil
}

// computeDeltaAgainst rebuilds def's manifest on version-independent keys and
// diffs it against an installed base's recorded declared-key set, returning the
// create/update/tombstone mutation batch, its partition counts, and any
// LeafBudget warnings (dynamic-type-taxonomy-design.md §10.2) the rebuild
// surfaced. Shared by Upgrade and Apply's in-place path; the caller already
// holds the installed base, so it is not re-resolved here.
func (i *Installer) computeDeltaAgainst(ctx context.Context, existing *installedPackage, def Definition) ([]installMutation, diffSummary, []string, error) {
	oldKeys, declErr := i.readDeclaredKeys(ctx, existing.Key)
	if declErr != nil && !errors.Is(declErr, ErrMalformedDeclaredKeys) {
		return nil, diffSummary{}, nil, declErr
	}
	newOps, _, _, leafBudgetWarnings, err := i.buildManifestBatch(ctx, def, metaScanResult{})
	if err != nil {
		return nil, diffSummary{}, nil, err
	}
	mutations, sum, err := i.diffManifest(ctx, oldKeys, newOps)
	if errors.Is(declErr, ErrMalformedDeclaredKeys) {
		sum.declarationMalformed = declErr
	}
	return mutations, sum, leafBudgetWarnings, err
}

// submitUpgradeOp submits one UpgradePackage op carrying the upgrade delta.
// Deterministic requestId from name+from+to+content so a re-submit of the same
// delta dedup-short-circuits while distinct (from,to) pairs — and distinct
// same-version edits — stay independent (Contract #8 §8.2 pattern).
func (i *Installer) submitUpgradeOp(ctx context.Context, def Definition, fromVersion string, mutations []installMutation) error {
	payload := map[string]any{
		"name":        def.Name,
		"fromVersion": fromVersion,
		"toVersion":   def.Version,
		"mutations":   mutations,
	}
	requestID, err := contentRequestID(def.Name, fromVersion+"->"+def.Version, "upgrade-op", mutations)
	if err != nil {
		return err
	}
	reply, err := i.submitOp(ctx, "UpgradePackage", "UpgradePackage", requestID, payload)
	if err != nil {
		return fmt.Errorf("pkgmgr: submit UpgradePackage: %w", err)
	}
	switch reply.Status {
	case processor.ReplyStatusAccepted, processor.ReplyStatusDuplicate:
		return nil
	default:
		if reply.Error != nil && reply.Error.Code == processor.ErrCodeRevisionConflict {
			return fmt.Errorf("%w: %s (a concurrent write raced this upgrade — re-run)",
				ErrUpgradeConflict, replyError(reply))
		}
		return fmt.Errorf("pkgmgr: UpgradePackage rejected: %s", replyError(reply))
	}
}

// noChangesReason renders the Reason string for an upgrade/apply whose
// mutation batch came back empty. A plain "no changes" is only true when
// nothing was skipped for a reason worth naming: if revocationsRespected > 0,
// a declared key is deliberately dead, and saying so is what makes the
// respected-revocation outcome visible even on a run that otherwise emits no
// mutation at all (Force same-version with nothing else changed, e.g.).
//
// The retention-holder exclusion has no branch here: a holder can only be
// excluded once it has left the manifest's declaredKeys, and declaredKeys is a
// field of the .manifest aspect body — so any run that excludes one also
// rewrites that aspect, which is itself an update mutation. An empty mutation
// batch and a nonzero holder count cannot co-occur.
//
// A refused secure-column narrowing DOES branch here, and is the case that most
// needs to: when the narrowing is the only edit to its lens spec, widening it
// back makes the body match what is already committed, the key is skipped, and
// the whole upgrade can come out empty. Reporting that as "no changes" would
// tell the author their edit was already in place when in fact it was declined.
func noChangesReason(pkgName string, revocationsRespected, secureColumnsWidened int) string {
	switch {
	case revocationsRespected > 0 && secureColumnsWidened > 0:
		return fmt.Sprintf("package %q body matches; %d previously-revoked grant(s) intentionally left revoked; %d secure column(s) kept their committed holderTypes — the narrowed declaration was NOT applied (ciphertext already written under a dropped holder type would become invisible to key destruction)", pkgName, revocationsRespected, secureColumnsWidened)
	case revocationsRespected > 0:
		return fmt.Sprintf("package %q body matches; %d previously-revoked grant(s) intentionally left revoked", pkgName, revocationsRespected)
	case secureColumnsWidened > 0:
		return fmt.Sprintf("package %q: no mutations, because the only edit was a narrowed secure-column holderTypes declaration on %d column(s), which is refused — ciphertext already written under a dropped holder type would become invisible to key destruction", pkgName, secureColumnsWidened)
	}
	return fmt.Sprintf("package %q already matches the requested definition (no changes)", pkgName)
}

// diffSummary counts the three partitions an upgrade produces.
type diffSummary struct {
	created int
	// revived counts entities the package re-adds onto a key a prior removal
	// tombstoned. They commit as updates (a create cannot land on a subject with
	// history) but are new entities from the package's point of view, so they
	// report as Created; the separate counter keeps the path assertable.
	revived    int
	updated    int
	tombstoned int
	// revocationsRespected counts surviving grant/role topology keys (declared
	// by both the old and new manifest, never vtx.meta.* definitions) whose
	// committed doc was already tombstoned by an out-of-band op
	// (RevokePermission/TombstonePermission/TombstoneRole). The upgrade leaves
	// them tombstoned rather than reviving them, and this counter is what makes
	// that silent-by-default outcome visible to the operator.
	revocationsRespected int
	// retentionHoldersPreserved counts LIVE vtx.retentionclass.* keys (the
	// holder root + its .retentionPolicy aspect) this upgrade left alone — live
	// but no longer declared — rather than auto-tombstoning via the old\new
	// removal path, because ShredRetentionClassKey's vertex_alive guard
	// (packages/privacy-base/shred_retention_class_key.go) refuses a tombstoned
	// holder forever: an auto-tombstone here would permanently block the only
	// path that can destroy that class's DEK. retention-class-key-custody-
	// design.md §3.1, §4.3.
	retentionHoldersPreserved int
	// retentionHoldersAlreadyStranded counts removal-path vtx.retentionclass.*
	// keys whose committed doc is ALREADY tombstoned. Skipping them is the same
	// non-action, but calling them "preserved" would be false: the vertex_alive
	// guard refuses them, so their DEK is past every destruction path there is.
	// Counted separately so the operator sees stranded custody as stranded.
	retentionHoldersAlreadyStranded int
	// reactivation names the lens spec edits this upgrade commits that a running
	// Refractor cannot adopt without re-activating the lens. The upgrade is
	// correct and lands; what would otherwise be wrong is reporting it as
	// success with nothing said, while the lens keeps serving its old spec.
	reactivation []string
	// secureColumnsWidened counts lens secure columns whose persisted
	// targetConfig.secureColumns[].holderTypes this diff refused to narrow: the
	// new definition declared fewer holder types than the committed spec already
	// carries, and the union was written instead. Without the counter the
	// refusal is the one outcome an operator cannot see — a narrowing that is
	// the whole diff for its key collapses back to a no-op update, so the
	// package author's declared edit would otherwise vanish behind a bare "no
	// changes". Same reason revocationsRespected and retentionHoldersPreserved
	// exist.
	secureColumnsWidened int
	// tombstonedOpMetas names every op-meta this delta drops (recognized by
	// the removed key's committed doc: class meta.ddl.vertexType +
	// data.operationType) — the population the op-meta retirement guard
	// (opmeta-retirement-open-task-guard-design.md §2) checks against the
	// Definition's declared disposition before the mutations are submitted.
	tombstonedOpMetas []tombstonedOpMeta
	// droppedSecureColumns names every committed targetConfig.secureColumns
	// entry this delta erases — the column dropped from a lens the package
	// still declares, or the whole spec of a lens it removed or renamed. This
	// is the population the secure-column retirement guard
	// (retention-class-key-custody-design.md §30) checks against the
	// Definition's declared retirements before the mutations are submitted.
	// The widen (secureColumnsWidened) protects a column the new spec still
	// names; nothing protected one it stopped naming.
	droppedSecureColumns []droppedSecureColumn
	// oldKeyCount is the size of the installed manifest's declaredKeys SET this
	// delta diffed against — every key the package currently owns, the package
	// root vertex and its `.manifest` aspect included, so it is a KEY count and
	// not a count of lenses/roles/permissions. It is the deduplicated size: a
	// manifest whose recorded declaredKeys names a key twice owns it once, and
	// a count that said otherwise would put an off-by-one in the one number an
	// operator uses to judge coverage.
	//
	// It exists because it is one of the two numbers the coverage refusal
	// (ApplyWouldRemoveError) quantifies: "the package declares N keys, this
	// Definition describes M" is the whole diagnosis for an author who
	// submitted a partial description of a package, and neither number is
	// recoverable from a mutation list that only carries what changed.
	oldKeyCount int
	// newKeyCount is the size of the rebuilt create-batch's key set — every key
	// the submitted Definition describes, again deduplicated and again
	// including the package root and the `.manifest` aspect, so the two counts
	// are comparable and a covering Definition reports the same number on both
	// sides. It is the second number the coverage refusal quantifies (see
	// oldKeyCount).
	newKeyCount int
	// undescribedKeys is old \ new: every key the installed manifest declares
	// that the submitted Definition does not describe, deduplicated and sorted.
	//
	// This is deliberately the raw set difference and NOT the emitted tombstone
	// list, which is a strict subset of it. The removal arm drops two classes of
	// key without emitting anything — a retention-class holder it preserves, and
	// a key already absent from KV — and for a caller converging the WHOLE
	// package that is the right non-action, because the manifest it writes still
	// describes the package. For a caller whose Definition is partial it is not:
	// the same in-place branch rewrites declaredKeys, depends, description and
	// version from that Definition (build.go's manifest body), so a key silently
	// dropped from the declaration is undeclared afterwards whether or not
	// anything was tombstoned — a preserved retention holder loses the custody
	// declaration that made it findable. Coverage is therefore the property the
	// refusal has to test, and emission is only its loudest symptom.
	undescribedKeys []string
	// declarationMalformed is non-nil when the installed manifest's declaredKeys
	// could not be read whole (readDeclaredKeys' ErrMalformedDeclaredKeys). The
	// diff still runs on the keys that WERE readable, because for a caller
	// converging a package on its own source that run is the repair. It is
	// carried rather than returned so that the one caller whose safety depends
	// on `old` being complete — the RefuseRemovals coverage guard, which reads a
	// short `old` as "this Definition covers everything" — can refuse instead.
	declarationMalformed error
	// resurrectedKeys names every SURVIVING key whose committed document is
	// already tombstoned and which this delta would un-tombstone by updating it
	// — the definition keys (vtx.meta.*) the respect-the-revocation branch
	// deliberately excludes, so that a package can revive its own definition by
	// re-declaring it.
	//
	// Recorded at the point an update is actually emitted, not where the
	// exclusion is taken: a tombstoned key whose body is byte-equal skips the
	// update and is revived by nothing. Source-authored convergence keeps this
	// revival; it is the caller submitting a PARTIAL Definition that has no
	// authority to perform it, having described nothing about the operator's
	// decision to kill the key.
	resurrectedKeys []string
}

// droppedSecureColumn names one erasure of a lens's committed key-custody
// history that an upgrade must not make silently.
type droppedSecureColumn struct {
	// Key is the lens `.spec` aspect key whose committed
	// targetConfig.secureColumns this delta erases.
	Key string
	// Lens is the lens's canonicalName as the COMMITTED package declared it —
	// the value a RetiredSecureColumn must name, read off the sibling
	// `.canonicalName` aspect because a lens NanoID is a salted hash and does
	// not invert. Empty when that aspect could not be read.
	Lens string
	// Column is the retirement SELECTOR this erasure needs: the dropped
	// column's name, or "" when the whole spec goes (a removed or renamed
	// lens), which only a Column:"" declaration excuses.
	Column string
	// Erased names every committed secure column this erasure takes with it —
	// one entry for a dropped column, all of them for a dropped spec — and
	// Holders the holder types they declared. Both exist so a refusal can tell
	// an author what history is at stake rather than only that something was
	// refused.
	Erased  []string
	Holders []string
}

// tombstonedOpMeta names one op-meta a diff drops: its vertex key (so the
// retirement guard can enumerate `forOperation` referents by op-meta id) and
// its operationType (so it can be matched against a declared disposition).
type tombstonedOpMeta struct {
	Key           string
	OperationType string
}

// opMetaOperationType reports the operationType a committed doc's op-meta
// vertex carries, per build.go's recognizer: a non-routed meta-vertex classed
// meta.ddl.vertexType with operationType on its own `data` (build.go:222-232).
// Returns ("", false) for any other vertex shape.
func opMetaOperationType(committed map[string]any) (string, bool) {
	if committed == nil {
		return "", false
	}
	if cls, _ := committed["class"].(string); cls != opMetaClass {
		return "", false
	}
	data, _ := committed["data"].(map[string]any)
	ot, _ := data["operationType"].(string)
	if ot == "" {
		return "", false
	}
	return ot, true
}

// diffManifest partitions the new create-batch against the old key set into the
// upgrade delta: a key only in the new set → create, unless KV already holds it
// as a tombstone, in which case it is REVIVED by update; a surviving key whose
// committed body differs from the rebuilt one → update (with createdAt/createdBy
// carried forward); a key only in the old set → tombstone. Surviving keys with
// a byte-equal logical body are omitted (the body-equality skip). Output order
// is deterministic: the new-batch order for create/update, sorted keys for
// tombstones.
//
// The revive case is what makes re-adding a removed entity possible at all.
// Package entity keys are deterministic in (package, kind, canonicalName), so a
// lens/role/permission that is dropped from a package and later added back
// lands on the EXACT key its removal tombstoned. A create asserts revision 0
// and the tombstone's subject history defeats that assertion, so the whole
// upgrade batch is rejected — permanently, since re-running is just as
// deterministic. The old manifest cannot see this: the key is absent from it
// precisely because the entity was removed, which is why the check has to be a
// KV read rather than a set difference.
//
// Every update/tombstone mutation carries the revision this diff's own read
// just observed as ExpectedRevision (per-key OCC, F-011/Contract #8 §8.6): a
// concurrent write to a surviving key between this read and the upgrade's
// commit now fails the whole atomic batch (ErrUpgradeConflict) instead of
// being silently overwritten. Create mutations are already conditioned
// create-only and carry no ExpectedRevision.
func (i *Installer) diffManifest(ctx context.Context, oldKeys []string, newOps []installMutation) ([]installMutation, diffSummary, error) {
	oldSet := make(map[string]struct{}, len(oldKeys))
	for _, k := range oldKeys {
		oldSet[k] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(newOps))
	for _, op := range newOps {
		newSet[op.Key] = struct{}{}
	}

	var out []installMutation
	sum := diffSummary{oldKeyCount: len(oldSet), newKeyCount: len(newSet)}

	for _, op := range newOps {
		_, survives := oldSet[op.Key]
		committed, rev, err := i.getCommitted(ctx, op.Key)
		if err != nil {
			return nil, diffSummary{}, err
		}
		if !survives {
			if committed == nil {
				out = append(out, installMutation{Op: "create", Key: op.Key, Document: op.Document})
				sum.created++
				continue
			}
			// Re-adding an entity whose key a prior removal tombstoned. Revive it
			// under the same per-key OCC every other update carries. The revived
			// entity keeps its original creation provenance: an entity's identity
			// is its key, so the same key coming back is the same entity, and
			// Contract #1 §1.3 makes createdAt immutable for its whole life. The
			// carry is the Processor's to make — step 8 sources the creation
			// triplet from the stored document for every update and tombstone,
			// and drops whatever the mutation supplied — so this branch neither
			// needs to graft it nor could override it.
			sum.droppedSecureColumns = append(sum.droppedSecureColumns,
				i.droppedSecureColumnsForUpdate(ctx, op.Key, committed, op.Document)...)
			sum.secureColumnsWidened += widenSecureColumnsForUpdate(op.Key, committed, op.Document)
			out = append(out, installMutation{Op: "update", Key: op.Key, Document: op.Document, ExpectedRevision: &rev})
			sum.revived++
			continue
		}
		if committed == nil {
			// Recorded in the old manifest but absent from KV (a prior partial
			// state). Re-create it — CreateOnly succeeds on an absent key.
			out = append(out, installMutation{Op: "create", Key: op.Key, Document: op.Document})
			sum.created++
			continue
		}
		del, _ := committed["isDeleted"].(bool)
		if del && !strings.HasPrefix(op.Key, metaVertexPrefix) {
			// A surviving key (present in both old and new sets) that is already
			// tombstoned can only have been tombstoned out-of-band: this diff's
			// own tombstone emission (below) touches exclusively old \ new keys,
			// never one the package still declares. Respect the deliberate
			// revocation instead of reviving it via the update path.
			//
			// Scoped to non-definition keys only (excludes vtx.meta.* — DDL/lens
			// meta-vertices and their aspects). Grant/role topology —
			// vtx.permission.*, vtx.role.*, lnk.permission.*.grantedBy.role.* —
			// is exactly what RevokePermission/TombstonePermission/TombstoneRole
			// target, and is this design's ratified scope
			// (grant-provenance-runtime-permission-minting-design.md §5.2, §12).
			// vtx.meta.* definitions are deliberately left on the pre-existing
			// body-diff revive path below: that behavior is unanalyzed by this
			// design (reactivation semantics and the opMeta retirement guard
			// both assume a definition stays revivable via reapply), and
			// extending the guard to it would be undesigned scope
			// creep — internal/bootstrap/reconcile.go:76-97 draws an analogous
			// definition-vs-topology boundary in a sibling mechanism (though
			// there every tombstone is respected unconditionally, definitions
			// included; it is cited here only for the shape of the split, not as
			// a claim that it also revives tombstoned definitions).
			sum.revocationsRespected++
			continue
		}
		sum.droppedSecureColumns = append(sum.droppedSecureColumns,
			i.droppedSecureColumnsForUpdate(ctx, op.Key, committed, op.Document)...)
		sum.secureColumnsWidened += widenSecureColumnsForUpdate(op.Key, committed, op.Document)
		if logicalDocEqual(op.Document, committed) {
			continue // body-equality skip — no update needed
		}
		if note := reactivationNote(op.Key, committed, op.Document); note != "" {
			sum.reactivation = append(sum.reactivation, note)
		}
		out = append(out, installMutation{Op: "update", Key: op.Key, Document: op.Document, ExpectedRevision: &rev})
		sum.updated++
		if del {
			// This update lands a live body over a tombstoned one — the
			// definition-key revival the branch above deliberately leaves open.
			// Recorded here rather than there because a byte-equal body skips
			// the update and revives nothing.
			sum.resurrectedKeys = append(sum.resurrectedKeys, op.Key)
		}
	}

	// Sorted so the refusal that reads it names the same keys in the same order
	// on every run — an operator comparing two runs is comparing lists, and the
	// field's own contract says sorted.
	sort.Strings(sum.resurrectedKeys)

	// Removed keys (old \ new) → tombstone, in deterministic sorted order.
	seen := make(map[string]struct{}, len(oldKeys))
	var removed []string
	for _, k := range oldKeys {
		if _, stillThere := newSet[k]; stillThere {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		removed = append(removed, k)
	}
	sort.Strings(removed)
	sum.undescribedKeys = removed
	for _, k := range removed {
		committed, rev, err := i.getCommitted(ctx, k)
		if err != nil {
			return nil, diffSummary{}, err
		}
		if committed == nil {
			// Absent from KV already (a prior partial state) — nothing to
			// tombstone, nothing to condition. A retention-class holder that is
			// absent is likewise nothing to preserve, so it is not counted.
			continue
		}
		if strings.HasPrefix(k, "vtx."+RetentionClassVertexType+".") {
			// A retention-class holder (the vtx.retentionclass.<id> root or its
			// .retentionPolicy aspect) that the package renamed or dropped. Its
			// DEK may only be destroyed by ShredRetentionClassKey, whose
			// vertex_alive guard refuses a tombstoned holder — so tombstoning it
			// here would permanently strand the class key it custodies, the
			// opposite of what a retention obligation promises. Leave the key
			// entirely alone: live but undeclared, still shreddable whenever the
			// controller's retention schedule says to destroy it. Destruction
			// belongs to the explicit verb, never to a package diff.
			//
			// A holder that is ALREADY tombstoned gets the same non-action but a
			// different count: the guard refuses it, so "preserved — still
			// shreddable" would be the one claim an operator must not be given.
			if del, _ := committed["isDeleted"].(bool); del {
				sum.retentionHoldersAlreadyStranded++
			} else {
				sum.retentionHoldersPreserved++
			}
			continue
		}
		sum.droppedSecureColumns = append(sum.droppedSecureColumns,
			i.droppedSecureColumnsForRemoval(ctx, k, committed)...)
		out = append(out, installMutation{
			Op:               "tombstone",
			Key:              k,
			ExpectedRevision: &rev,
		})
		sum.tombstoned++
		if ot, ok := opMetaOperationType(committed); ok {
			sum.tombstonedOpMetas = append(sum.tombstonedOpMetas, tombstonedOpMeta{Key: k, OperationType: ot})
		}
	}

	return out, sum, nil
}

// readDeclaredKeys returns the full set of Core KV keys a package install
// wrote: the manifest's recorded declaredKeys plus the manifest aspect itself
// (which is not in the list — its snapshot precedes its own key). The package
// vertex is already in declaredKeys.
func (i *Installer) readDeclaredKeys(ctx context.Context, pkgKey string) ([]string, error) {
	manifestKey := pkgKey + ".manifest"
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
	declaredRaw, declaredPresent := env.Data["declaredKeys"].([]any)
	keys := make([]string, 0, len(declaredRaw)+1)
	malformed := !declaredPresent
	for _, dk := range declaredRaw {
		if s, ok := dk.(string); ok && s != "" {
			keys = append(keys, s)
			continue
		}
		malformed = true
	}
	keys = append(keys, manifestKey)
	if malformed {
		// The declaration this diff is about to reason over could not be read
		// whole: `declaredKeys` is absent, is not a list, or carries an entry
		// that is not a non-empty string.
		//
		// Every caller gets the keys that WERE readable, because a convergence
		// run over a partially-readable manifest is still the operation that
		// repairs it — dropping the bad entries and re-declaring from source is
		// the outcome an operator wants. What none of them may do is treat the
		// shortened list as the truth: a manifest that lost half its
		// declaredKeys yields a short `old` set, hence few or no removals, and a
		// coverage guard reading it would admit a partial apply as fully
		// covering. So the shortfall is reported alongside the keys and the
		// RefuseRemovals path in Apply refuses on it, while Uninstall and
		// source-authored upgrades proceed as before. The sibling precedent is
		// loadProtectedDispatchSets, which fails closed on the same class.
		return keys, errMalformedDeclaredKeys(manifestKey)
	}
	return keys, nil
}

// ErrMalformedDeclaredKeys reports a package manifest whose declaredKeys could
// not be read as a complete list of keys. It is returned ALONGSIDE the keys
// that were readable, so a caller chooses between repairing and refusing; only
// a caller that has declared its Definition partial (ApplyOptions.RefuseRemovals)
// must refuse, because only that caller's safety rests on `old` being complete.
var ErrMalformedDeclaredKeys = errors.New("pkgmgr: package manifest declaredKeys is absent or malformed")

func errMalformedDeclaredKeys(manifestKey string) error {
	return fmt.Errorf("%w: %s", ErrMalformedDeclaredKeys, manifestKey)
}

// reactivationNote reports, for one updated key, whether the edit is a lens spec
// change a running Refractor will refuse to hot-reload — in which case the
// upgrade lands in Core KV and the lens keeps serving its old spec until it is
// re-activated.
//
// The refusal itself belongs to Refractor and stays there; this only predicts
// it, so that the operator who caused the edit hears about it at the moment they
// caused it rather than in a log of a process they are not watching. A
// prediction that is wrong in the quiet direction costs a missing warning; the
// upgrade is committed either way, because the stored spec is the truth and the
// running pipeline is what is stale.
func reactivationNote(key string, oldDoc, newDoc map[string]any) string {
	if !strings.HasSuffix(key, ".spec") {
		return ""
	}
	oldSpec, ok := specBodyOf(oldDoc)
	if !ok {
		return ""
	}
	newSpec, ok := specBodyOf(newDoc)
	if !ok {
		return ""
	}
	reason := reloadpin.RefusedChange(oldSpec, newSpec)
	if reason == "" {
		return ""
	}
	return fmt.Sprintf("%s: %s — the lens keeps serving its activated spec until it is re-activated (restart Refractor, or delete and re-create the lens definition)", key, reason)
}

// specBodyOf pulls the lens spec out of a stored aspect document, mirroring how
// Refractor's own loader unwraps it: a bare spec carries cypherRule at the top
// level, an envelope-wrapped one carries it under `data`. Anything else is not a
// lens spec and is left alone — the meta keyspace holds DDLs and descriptors too.
func specBodyOf(doc map[string]any) ([]byte, bool) {
	if doc == nil {
		return nil, false
	}
	if _, isLens := doc["cypherRule"]; isLens {
		raw, err := json.Marshal(doc)
		return raw, err == nil
	}
	data, ok := doc["data"].(map[string]any)
	if !ok {
		return nil, false
	}
	if _, isLens := data["cypherRule"]; !isLens {
		return nil, false
	}
	raw, err := json.Marshal(data)
	return raw, err == nil
}

// widenSecureColumnsForUpdate keeps a lens's persisted
// targetConfig.secureColumns[].holderTypes monotonically non-shrinking across an
// upgrade: for every secure column the new spec and the committed spec both
// declare, the new document's holderTypes becomes the union of the two, matched
// by column NAME (never by slice position — a package author may legitimately
// reorder the list, and a positional match would graft one column's history onto
// another).
//
// The destruction-readiness oracle and the rebuild target lister both answer
// "which lenses hold ciphertext for this holder type?" from the CURRENT spec
// (internal/refractor/health/registry_probe.go, cmd/refractor/main.go). Rows
// encrypted under a holder type the package later stops declaring are still
// physically ciphertext in the target store, but a narrowed declaration makes
// them invisible to both — the target set goes empty and the system attests as
// covered over data it can no longer see. Refusing the narrowing at the one site
// that writes the spec keeps both readers correct by construction, with no
// second historical shape for either of them to learn.
//
// It applies on the revive path as well as the ordinary body-diff path. A
// package entity key is deterministic in (package, kind, canonicalName), so the
// key a removal tombstoned is the EXACT key the same lens lands on when the
// package re-adds it — diffManifest's own contract, and why the revive branch
// carries the entity's original creation provenance forward. The committed spec
// a revive reads is therefore this lens's own previous incarnation, not some
// unrelated lens sharing a slot, and the ciphertext its earlier holder types
// encrypted is still sitting in the target store that the removal never touched.
// Skipping the widen there would reopen exactly the hazard the rest of this
// function closes.
//
// A column the committed spec does not declare (genuinely new) and a column the
// new spec drops entirely are both left alone — dropping a whole column is a
// distinct hazard (the ciphertext's physical schema, not just its key custody)
// this does not claim to close. There is deliberately no way to shed a holder
// type here: shedding one safely requires a sweep that re-keys or destroys the
// affected rows first, and that verb does not exist yet.
//
// When the union's SET matches what is already committed, the committed slice is
// written back verbatim, order included. Holder-type order carries no meaning to
// the decryptor, but any secureColumns edit at all is hot-reload-refused
// (reloadpin.PinnedFields), so re-ordering a set that did not actually change
// would emit a pointless update and tell an operator to re-activate a lens whose
// behavior is identical. That case is likewise not counted as a widening.
//
// Returns the number of columns whose declared holder types were actually
// widened — the count that keeps a narrowing-only upgrade from reporting as a
// silent no-op.
//
// Mutates newDoc in place. The two documents carry the same field in two Go
// shapes — a freshly built document holds []map[string]any with []string
// holderTypes, a committed one has round-tripped through json.Unmarshal into
// []any of map[string]any with []any holderTypes — so both are normalized before
// comparison; asserting only one shape would silently never widen anything.
func widenSecureColumnsForUpdate(key string, committed, newDoc map[string]any) int {
	if !strings.HasSuffix(key, ".spec") {
		return 0
	}
	committedEntries := secureColumnsOf(committed)
	if len(committedEntries) == 0 {
		return 0
	}
	history := make(map[string][]string, len(committedEntries))
	for _, entry := range committedEntries {
		column, _ := entry["column"].(string)
		if column == "" {
			continue
		}
		// Union, not assign: a column can appear at both targetConfig levels, and
		// the committed custody history is everything either level recorded. A
		// plain assignment is last-wins, which drops one level's holder types on
		// the floor — the same shape of loss the widen exists to prevent.
		history[column] = unionStrings(history[column], stringsOf(entry["holderTypes"]))
	}
	widened := 0
	for _, entry := range secureColumnsOf(newDoc) {
		column, _ := entry["column"].(string)
		if column == "" {
			continue
		}
		held, ok := history[column]
		if !ok {
			continue
		}
		declared := unionStrings(stringsOf(entry["holderTypes"]), nil)
		union := unionStrings(declared, held)
		// union is a superset of both operands, so an equal length is an equal
		// set.
		if len(union) == len(held) {
			entry["holderTypes"] = held
		} else {
			entry["holderTypes"] = union
		}
		if len(union) > len(declared) {
			widened++
		}
	}
	return widened
}

// droppedSecureColumnsForUpdate names the committed secure columns an
// update (or revive) of a lens `.spec` key erases: every entry the committed
// spec declares whose column the new document no longer names at all.
//
// This is the gap the widen above cannot close. widenSecureColumnsForUpdate
// unions holder types for a column BOTH specs declare; a column the new spec
// stops declaring is skipped there, and the persisted spec simply loses it.
// The destruction-readiness oracle reads a decoded targetConfig with no
// matching secure column as a genuine "this lens holds no ciphertext for that
// holder type" (internal/refractor/health/registry_probe.go), so the erasure
// is indistinguishable from never having encrypted anything — while the
// ciphertext is still physically in the target store, which no package diff
// ever touches. The author has to say so out loud instead.
//
// A new document with no targetConfig at all (the adapter changed away from
// postgres, say) drops every committed secure column, and is detected by the
// same walk rather than by a separate adapter check.
//
// Detection keys on the committed entry's EXISTENCE, never on its holder
// types being non-empty: the oracle reads an empty holderTypes list as
// UNKNOWN and answers "may hold", so an entry with nothing in it is exactly
// the entry whose disappearance loses the most coverage.
func (i *Installer) droppedSecureColumnsForUpdate(ctx context.Context, key string, committed, newDoc map[string]any) []droppedSecureColumn {
	if !strings.HasSuffix(key, ".spec") {
		return nil
	}
	committedEntries := secureColumnsOf(committed)
	if len(committedEntries) == 0 {
		return nil
	}
	stillDeclared := make(map[string]struct{}, len(committedEntries))
	for _, entry := range secureColumnsOf(newDoc) {
		if column, _ := entry["column"].(string); column != "" {
			stillDeclared[column] = struct{}{}
		}
	}
	var out []droppedSecureColumn
	lens := ""
	for _, entry := range committedEntries {
		column, _ := entry["column"].(string)
		if column == "" {
			// Unnamed and therefore unnameable by any retirement declaration.
			// bucketguard refuses an empty column name at install, so a
			// committed entry cannot carry one; the skip exists so a
			// hand-written or hand-corrupted document cannot make the guard
			// demand a declaration no author can write.
			continue
		}
		if _, ok := stillDeclared[column]; ok {
			continue
		}
		if lens == "" {
			lens = i.lensCanonicalNameOf(ctx, key)
		}
		out = append(out, droppedSecureColumn{
			Key:     key,
			Lens:    lens,
			Column:  column,
			Erased:  []string{column},
			Holders: stringsOf(entry["holderTypes"]),
		})
	}
	return out
}

// droppedSecureColumnsForRemoval names the erasure a REMOVED lens `.spec` key
// commits: the whole committed targetConfig.secureColumns goes with the
// tombstone, because a lens NanoID is salted by the canonicalName, so a lens
// the package renamed lands on a wholly new key and the old key is tombstoned
// exactly as an outright removal would be. Either way the oracle skips a
// tombstoned lens outright (registry_probe.go's IsDeleted check), while the
// target store still holds every row the retired columns encrypted.
//
// One erasure covers the whole spec, selected by a Column:"" retirement: a
// per-column declaration attests one column's history, never the lens's.
//
// A spec whose committed doc is ALREADY tombstoned is reported the same way.
// The guard reasons about what the committed spec declares, not about which
// upgrade first erased it — and a retirement declaration is available for that
// state too, which is the honest way to record custody that is already gone.
func (i *Installer) droppedSecureColumnsForRemoval(ctx context.Context, key string, committed map[string]any) []droppedSecureColumn {
	if !strings.HasSuffix(key, ".spec") {
		return nil
	}
	entries := secureColumnsOf(committed)
	if len(entries) == 0 {
		return nil
	}
	drop := droppedSecureColumn{Key: key, Lens: i.lensCanonicalNameOf(ctx, key)}
	for _, entry := range entries {
		if column, _ := entry["column"].(string); column != "" {
			drop.Erased = append(drop.Erased, column)
		}
		drop.Holders = unionStrings(drop.Holders, stringsOf(entry["holderTypes"]))
	}
	return []droppedSecureColumn{drop}
}

// lensCanonicalNameOf reads the canonicalName a lens `.spec` key's vertex
// carries, off the sibling `.canonicalName` aspect. A lens NanoID is a salted
// hash of (package, canonicalName) and does not invert, so this read is the
// only way a refusal can hand the author the exact Lens value their
// RetiredSecureColumn entry needs — and a refusal whose remedy cannot be
// written is a refusal that gets worked around by deleting the lens.
//
// Presentational only: a read failure yields the empty string and the refusal
// falls back to naming the key, rather than replacing a precise fail-closed
// message with a KV error about a different key.
func (i *Installer) lensCanonicalNameOf(ctx context.Context, specKey string) string {
	doc, _, err := i.getCommitted(ctx, strings.TrimSuffix(specKey, ".spec")+".canonicalName")
	if err != nil || doc == nil {
		return ""
	}
	data, _ := doc["data"].(map[string]any)
	name, _ := data["value"].(string)
	return name
}

// secureColumnsOf returns a spec document's secureColumns entries as mutable
// maps, accepting either the freshly-built []map[string]any or the
// json.Unmarshal-round-tripped []any. Nil-safe; a document that is not a lens
// spec, or a lens spec with no secure columns, yields nothing.
//
// BOTH targetConfig levels are read — the bare-body one and the
// stored-envelope (`data`) one — never one in preference to the other, because
// the destruction-readiness oracle's own mayHoldHolderType loops both for the
// same reason. Reading only the level a single-level lookup happens to pick
// leaves a document carrying a decoy top-level targetConfig beside a real
// `data.targetConfig.secureColumns` looking like a lens with no secure columns
// at all: no erasure is constructed for it, so no guard is ever consulted, and
// the custody record leaves silently.
//
// A column NAME appearing at both levels yields ONE entry carrying the union of
// both levels' holderTypes, because it is one column and one custody record:
// emitting it twice would make every consumer that counts entries — the
// retirement counters, the refusal's own printed bill — report one erasure as
// two. Entries with no column name are never merged: unnameable is not an
// identity, and two of them are two records.
//
// The returned maps are the document's OWN, so the write path can widen
// holderTypes in place. The one exception is a merged entry, which is a fresh
// map: writing to it would not reach the document. That costs nothing because
// only a hand-corrupted document can carry a column at both levels at all —
// make_aspect writes the envelope only — and the write path's input is always a
// document this process just built.
func secureColumnsOf(doc map[string]any) []map[string]any {
	var out []map[string]any
	byColumn := make(map[string]int)
	for _, cfg := range targetConfigsOf(doc) {
		for _, entry := range secureColumnEntriesOf(cfg) {
			column, _ := entry["column"].(string)
			if column == "" {
				out = append(out, entry)
				continue
			}
			idx, seen := byColumn[column]
			if !seen {
				byColumn[column] = len(out)
				out = append(out, entry)
				continue
			}
			merged := make(map[string]any, len(out[idx]))
			for k, v := range out[idx] {
				merged[k] = v
			}
			merged["holderTypes"] = unionStrings(stringsOf(out[idx]["holderTypes"]), stringsOf(entry["holderTypes"]))
			out[idx] = merged
		}
	}
	return out
}

// secureColumnEntriesOf normalizes one targetConfig's secureColumns list into
// mutable maps, tolerating both in-memory shapes the field takes.
func secureColumnEntriesOf(cfg map[string]any) []map[string]any {
	switch raw := cfg["secureColumns"].(type) {
	case []map[string]any:
		return raw
	case []any:
		out := make([]map[string]any, 0, len(raw))
		for _, e := range raw {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

// targetConfigsOf returns every targetConfig a lens spec document carries — the
// bare-body one and the stored-envelope (`data`) one — in that order, skipping
// absent levels.
//
// Both shapes are accepted because a spec reaches its readers either way: the
// stored aspect envelope ({class, isDeleted, data, vertexKey, localName} —
// docAspect's shape, which both a freshly built and a KV-round-tripped document
// carry) and the bare spec body. specBodyOf above takes the same two, for the
// same reason Refractor's own loader does — a spec that arrives unwrapped must
// not be mistaken for a spec with no targetConfig.
func targetConfigsOf(doc map[string]any) []map[string]any {
	if doc == nil {
		return nil
	}
	var out []map[string]any
	if cfg, ok := doc["targetConfig"].(map[string]any); ok {
		out = append(out, cfg)
	}
	if data, ok := doc["data"].(map[string]any); ok {
		if cfg, ok := data["targetConfig"].(map[string]any); ok {
			out = append(out, cfg)
		}
	}
	return out
}

// stringsOf normalizes a JSON string list held either as a Go []string (freshly
// built) or a []any of string (round-tripped through json.Unmarshal).
func stringsOf(v any) []string {
	switch raw := v.(type) {
	case []string:
		return raw
	case []any:
		out := make([]string, 0, len(raw))
		for _, e := range raw {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// unionStrings appends every element of extra that base does not already carry,
// preserving base's order first so the spec reads as the package author declared
// it, with the carried-forward history trailing.
func unionStrings(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, s := range base {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range extra {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// getCommitted reads a key's committed value as a generic map plus the
// read-time revision (the per-subject OCC token diffManifest conditions its
// update/tombstone mutations on — F-011/Contract #8 §8.6). A missing key
// returns (nil, 0, nil) so callers can treat it as "absent" rather than an error.
func (i *Installer) getCommitted(ctx context.Context, key string) (map[string]any, uint64, error) {
	entry, err := i.Conn.KVGet(ctx, CoreBucket, key)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("pkgmgr: read %s: %w", key, err)
	}
	var m map[string]any
	if err := json.Unmarshal(entry.Value, &m); err != nil {
		return nil, 0, fmt.Errorf("pkgmgr: parse %s: %w", key, err)
	}
	return m, entry.Revision, nil
}

// logicalDocEqual reports whether the committed entry already carries every
// logical field of the rebuilt document with an identical value. It compares
// only the fields the new (provenance-free) document declares — class, data,
// isDeleted, and the structural vertexKey/localName (aspect) or sourceVertex/
// targetVertex/localName (link) — so the committed entry's provenance
// (createdAt/lastModified*/key) never forces a spurious update. A mismatch on
// any declared field, or a field absent from the committed entry, means the
// body changed.
func logicalDocEqual(newDoc, committed map[string]any) bool {
	for field, nv := range newDoc {
		cv, ok := committed[field]
		if !ok {
			return false
		}
		if !jsonEqual(nv, cv) {
			return false
		}
	}
	return true
}

// jsonEqual compares two values by their canonical JSON encoding. Go's
// json.Marshal sorts map keys, so two logically-equal documents (one freshly
// built, one round-tripped through KV) encode identically regardless of map
// iteration order or int/float representation.
func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}
