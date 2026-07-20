package pkgmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
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
}

// preservedProvenanceFields are the immutable create-provenance fields an
// upgrade carries forward onto an update mutation. The Processor's step-8
// commit rebuilds an updated entity's value from the supplied document and
// re-stamps the lastModified* triplet, but does NOT preserve createdAt/
// createdBy/createdByOp — so an in-place body change would otherwise reset a
// surviving entity's creation provenance (Contract #1 §1.3, createdAt is
// immutable). The client reads the committed entry for the body diff anyway,
// so it carries these forward at no extra read.
var preservedProvenanceFields = []string{"createdAt", "createdBy", "createdByOp"}

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
	if err := i.preflight(def); err != nil {
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
	mutations, sum, err := i.computeDeltaAgainst(ctx, existing, def)
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
	}
	if len(mutations) == 0 {
		res.Skipped = true
		res.Reason = fmt.Sprintf("package %q already matches the requested definition (no changes)", def.Name)
		return res, nil
	}

	// Step 5 — submit one UpgradePackage op.
	if err := i.submitUpgradeOp(ctx, def, existing.Version, mutations); err != nil {
		return nil, err
	}
	return res, nil
}

// preflight runs the required-field + field-level validation shared by Install,
// Upgrade, and Apply before any KV operation.
func (i *Installer) preflight(def Definition) error {
	if def.Name == "" {
		return fmt.Errorf("pkgmgr: Definition.Name is required")
	}
	if def.Version == "" {
		return fmt.Errorf("pkgmgr: Definition.Version is required")
	}
	if i.AdminActor == "" {
		return fmt.Errorf("pkgmgr: AdminActor is required")
	}
	return def.validateAll()
}

// computeDeltaAgainst rebuilds def's manifest on version-independent keys and
// diffs it against an installed base's recorded declared-key set, returning the
// create/update/tombstone mutation batch and its partition counts. Shared by
// Upgrade and Apply's in-place path; the caller already holds the installed
// base, so it is not re-resolved here.
func (i *Installer) computeDeltaAgainst(ctx context.Context, existing *installedPackage, def Definition) ([]installMutation, diffSummary, error) {
	oldKeys, err := i.readDeclaredKeys(ctx, existing.Key)
	if err != nil {
		return nil, diffSummary{}, err
	}
	newOps, _, _, err := i.buildManifestBatch(def)
	if err != nil {
		return nil, diffSummary{}, err
	}
	return i.diffManifest(ctx, oldKeys, newOps)
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
	var sum diffSummary

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
			// under the same per-key OCC every other update carries. No provenance
			// is grafted: a tombstone is written stripped (isDeleted + the
			// lastModified triplet, no createdAt), so the revived entity honestly
			// carries this install's creation stamp rather than a resurrected one.
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
		if logicalDocEqual(op.Document, committed) {
			continue // body-equality skip — no update needed
		}
		updateDoc := cloneDoc(op.Document)
		for _, f := range preservedProvenanceFields {
			if v, ok := committed[f]; ok {
				updateDoc[f] = v
			}
		}
		out = append(out, installMutation{Op: "update", Key: op.Key, Document: updateDoc, ExpectedRevision: &rev})
		sum.updated++
	}

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
	for _, k := range removed {
		committed, rev, err := i.getCommitted(ctx, k)
		if err != nil {
			return nil, diffSummary{}, err
		}
		if committed == nil {
			// Absent from KV already (a prior partial state) — nothing to
			// tombstone, nothing to condition.
			continue
		}
		out = append(out, installMutation{
			Op:               "tombstone",
			Key:              k,
			Document:         map[string]any{"isDeleted": true, "data": map[string]any{}},
			ExpectedRevision: &rev,
		})
		sum.tombstoned++
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
	declaredRaw, _ := env.Data["declaredKeys"].([]any)
	keys := make([]string, 0, len(declaredRaw)+1)
	for _, dk := range declaredRaw {
		if s, ok := dk.(string); ok && s != "" {
			keys = append(keys, s)
		}
	}
	keys = append(keys, manifestKey)
	return keys, nil
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

// cloneDoc returns a shallow copy of a logical document map so an upgrade can
// graft carried-forward provenance onto it without mutating the rebuilt batch.
func cloneDoc(d map[string]any) map[string]any {
	out := make(map[string]any, len(d)+len(preservedProvenanceFields))
	for k, v := range d {
		out[k] = v
	}
	return out
}
