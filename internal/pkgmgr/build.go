package pkgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// metaVertexPrefix is the Contract #1 prefix for both DDL and Lens
// meta-vertices (`vtx.meta.<NanoID>`).
const metaVertexPrefix = "vtx.meta."

// buildInstallBatch constructs the full atomic-batch op list for one
// install. Returns the ops + the flat list of every Core KV key it
// will write (mirrored into the package's manifest aspect so uninstall
// can enumerate).
func (i *Installer) buildInstallBatch(
	def Definition,
	pkgKey string,
	ddlIDs, lensIDs, permIDs []string,
	now time.Time,
) ([]substrate.BatchOp, []string, error) {
	var ops []substrate.BatchOp
	var declared []string

	createdByOp := "pkg-install:" + def.Name

	addCreate := func(key string, env []byte) {
		ops = append(ops, substrate.BatchOp{
			Bucket:     CoreBucket,
			Key:        key,
			Value:      env,
			CreateOnly: true,
		})
		declared = append(declared, key)
	}

	// DDL meta-vertices + 4 canonical aspects.
	for idx, d := range def.DDLs {
		ddlKey := metaVertexPrefix + ddlIDs[idx]
		class := d.Class
		if class == "" {
			class = "meta.ddl.vertexType"
		}
		vtxEnv, err := i.makeDocEnvelope(ddlKey, class, nil, createdByOp, now)
		if err != nil {
			return nil, nil, fmt.Errorf("pkgmgr: DDL[%d] vertex: %w", idx, err)
		}
		addCreate(ddlKey, vtxEnv)
		// canonicalName
		cn, err := i.makeAspectEnvelope(ddlKey+".canonicalName", ddlKey, "canonicalName", "canonicalName",
			map[string]any{"value": d.CanonicalName}, createdByOp, now)
		if err != nil {
			return nil, nil, err
		}
		addCreate(ddlKey+".canonicalName", cn)
		// permittedCommands
		pc, err := i.makeAspectEnvelope(ddlKey+".permittedCommands", ddlKey, "permittedCommands", "permittedCommands",
			map[string]any{"commands": d.PermittedCommands}, createdByOp, now)
		if err != nil {
			return nil, nil, err
		}
		addCreate(ddlKey+".permittedCommands", pc)
		// description
		desc, err := i.makeAspectEnvelope(ddlKey+".description", ddlKey, "description", "description",
			map[string]any{"text": d.Description}, createdByOp, now)
		if err != nil {
			return nil, nil, err
		}
		addCreate(ddlKey+".description", desc)
		// script
		sc, err := i.makeAspectEnvelope(ddlKey+".script", ddlKey, "script", "script",
			map[string]any{"source": d.Script}, createdByOp, now)
		if err != nil {
			return nil, nil, err
		}
		addCreate(ddlKey+".script", sc)
	}

	// Lens meta-vertices + canonical aspects.
	for idx, l := range def.Lenses {
		lensKey := metaVertexPrefix + lensIDs[idx]
		class := l.Class
		if class == "" {
			class = "meta.lens"
		}
		vtxEnv, err := i.makeDocEnvelope(lensKey, class, nil, createdByOp, now)
		if err != nil {
			return nil, nil, fmt.Errorf("pkgmgr: Lens[%d] vertex: %w", idx, err)
		}
		addCreate(lensKey, vtxEnv)
		aspects := map[string]map[string]any{
			"canonicalName": {"value": l.CanonicalName},
			"spec":          {"source": l.Spec},
			"adapter":       {"value": l.Adapter},
			"bucket":        {"value": l.Bucket},
			"engine":        {"value": l.Engine},
		}
		// Stable iteration order so the install op list is deterministic
		// across invocations — important for atomic-batch reproducibility.
		names := make([]string, 0, len(aspects))
		for n := range aspects {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			env, err := i.makeAspectEnvelope(lensKey+"."+name, lensKey, name, name,
				aspects[name], createdByOp, now)
			if err != nil {
				return nil, nil, err
			}
			addCreate(lensKey+"."+name, env)
		}
	}

	// Permissions + grant links.
	for idx, p := range def.Permissions {
		permID := permIDs[idx]
		permKey := "vtx.permission." + permID
		data := map[string]any{
			"operationType": p.OperationType,
			"scope":         p.Scope,
		}
		if p.Note != "" {
			data["note"] = p.Note
		}
		permEnv, err := i.makeDocEnvelope(permKey, "permission", data, createdByOp, now)
		if err != nil {
			return nil, nil, fmt.Errorf("pkgmgr: Permission[%d] vertex: %w", idx, err)
		}
		addCreate(permKey, permEnv)
		// Grant links — one per role canonical name in GrantsTo.
		for _, role := range p.GrantsTo {
			// In Phase 1 the role canonical names are translated to role IDs
			// by the caller (see cmd/lattice-pkg/main.go). Here we accept
			// either a canonical name OR a vtx.role.<NanoID>; if it doesn't
			// look like a NanoID we wrap it for traceability and the caller
			// can resolve later. Real production wiring resolves at the
			// caller layer.
			roleID := role
			if len(role) > len("vtx.role.") && role[:len("vtx.role.")] == "vtx.role." {
				roleID = role[len("vtx.role."):]
			}
			linkKey := "lnk.permission." + permID + ".grantsPermission.role." + roleID
			linkEnv, err := i.makeLinkEnvelope(linkKey, "vtx.permission."+permID, "vtx.role."+roleID,
				"grantsPermission", "grantsPermission", nil, createdByOp, now)
			if err != nil {
				return nil, nil, err
			}
			addCreate(linkKey, linkEnv)
		}
	}

	// Package vertex + manifest aspect (carries the declared-keys list
	// for uninstall enumeration).
	pkgEnv, err := i.makeDocEnvelope(pkgKey, "package",
		map[string]any{"name": def.Name, "version": def.Version}, createdByOp, now)
	if err != nil {
		return nil, nil, err
	}
	addCreate(pkgKey, pkgEnv)

	manifestData := map[string]any{
		"name":         def.Name,
		"version":      def.Version,
		"description":  def.Description,
		"depends":      def.Depends,
		"declaredKeys": append([]string{}, declared...), // snapshot before we add the manifest aspect itself
	}
	manifestKey := pkgKey + ".manifest"
	manifestEnv, err := i.makeAspectEnvelope(manifestKey, pkgKey, "manifest", "manifest",
		manifestData, createdByOp, now)
	if err != nil {
		return nil, nil, err
	}
	addCreate(manifestKey, manifestEnv)

	return ops, declared, nil
}

// buildTombstoneBatch reads the live envelope of each key and emits a
// CreateOnly=false Put with isDeleted=true. We need the current
// envelope's class + provenance fields so the tombstone is a valid
// Contract #1 envelope. Missing keys are silently skipped.
func (i *Installer) buildTombstoneBatch(ctx context.Context, keys []string) ([]substrate.BatchOp, error) {
	var ops []substrate.BatchOp
	for _, k := range keys {
		entry, err := i.Conn.KVGet(ctx, CoreBucket, k)
		if err != nil {
			if errors.Is(err, substrate.ErrKeyNotFound) {
				continue
			}
			return nil, fmt.Errorf("pkgmgr: tombstone read %s: %w", k, err)
		}
		var env map[string]any
		if err := json.Unmarshal(entry.Value, &env); err != nil {
			return nil, fmt.Errorf("pkgmgr: tombstone parse %s: %w", k, err)
		}
		env["isDeleted"] = true
		env["lastModifiedAt"] = i.Now().Format(time.RFC3339Nano)
		env["lastModifiedBy"] = i.AdminActor
		env["lastModifiedByOp"] = "pkg-uninstall"
		data, err := json.Marshal(env)
		if err != nil {
			return nil, fmt.Errorf("pkgmgr: tombstone marshal %s: %w", k, err)
		}
		// Tombstone batch uses unconditional puts (no OCC). Uninstall is
		// admin-driven and the entire batch is still atomic — partial
		// failure cannot leave a mixed state. Per-key OCC would force a
		// retry loop on benign concurrent reprojection and adds no
		// safety the atomic-batch failure mode does not already provide.
		ops = append(ops, substrate.BatchOp{
			Bucket: CoreBucket,
			Key:    k,
			Value:  data,
		})
		_ = entry.Revision // intentionally unused — see comment above
	}
	return ops, nil
}

// --- envelope helpers ---

// makeDocEnvelope mirrors bootstrap.MakeVertexEnvelope but with caller-
// supplied actor + opTracker, so installs carry the admin actor's NanoID
// rather than the bootstrap identity's.
func (i *Installer) makeDocEnvelope(key, class string, data map[string]any, createdByOp string, now time.Time) ([]byte, error) {
	env := substrate.NewDocumentEnvelopeAt(class, i.AdminActor, createdByOp, now)
	env.Key = key
	if data != nil {
		env.Data = data
	}
	return json.Marshal(env)
}

// makeAspectEnvelope mirrors bootstrap.MakeAspectEnvelope with caller-
// supplied actor + opTracker.
func (i *Installer) makeAspectEnvelope(key, vertexKey, localName, class string, data map[string]any, createdByOp string, now time.Time) ([]byte, error) {
	base := substrate.NewDocumentEnvelopeAt(class, i.AdminActor, createdByOp, now)
	base.Key = key
	if data != nil {
		base.Data = data
	}
	asp := substrate.AspectEnvelope{
		DocumentEnvelope: base,
		VertexKey:        vertexKey,
		LocalName:        localName,
	}
	return json.Marshal(asp)
}

// makeLinkEnvelope mirrors bootstrap.MakeLinkEnvelope with caller-supplied
// actor + opTracker.
func (i *Installer) makeLinkEnvelope(key, youngerVertex, olderVertex, localName, class string, data map[string]any, createdByOp string, now time.Time) ([]byte, error) {
	base := substrate.NewDocumentEnvelopeAt(class, i.AdminActor, createdByOp, now)
	base.Key = key
	if data != nil {
		base.Data = data
	}
	link := substrate.LinkEnvelope{
		DocumentEnvelope: base,
		YoungerVertex:    youngerVertex,
		OlderVertex:      olderVertex,
		LocalName:        localName,
	}
	return json.Marshal(link)
}
