package pkgmgr

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"github.com/asolgan/lattice/internal/substrate"
)

// metaVertexPrefix is the Contract #1 prefix for both DDL and Lens
// meta-vertices (`vtx.meta.<NanoID>`).
const metaVertexPrefix = "vtx.meta."

// installMutation is one entry in the InstallPackage op payload's
// `mutations` list — a LOGICAL document (no provenance). The Processor
// stamps createdAt/createdBy/createdByOp at step 8 from the install actor.
type installMutation struct {
	Op       string         `json:"op"`
	Key      string         `json:"key"`
	Document map[string]any `json:"document"`
}

// buildInstallBatch constructs the full mutation manifest for one install
// as LOGICAL documents (Story 1.5.5). Returns the mutations + the flat
// list of every Core KV key it will write (mirrored into the package's
// manifest aspect so uninstall can enumerate). The mutations are shipped
// in the InstallPackage op payload and committed atomically by the
// Processor, which stamps provenance.
func (i *Installer) buildInstallBatch(
	def Definition,
	pkgKey string,
	ddlIDs, lensIDs, permIDs, roleIDs []string,
) ([]installMutation, []string, error) {
	var ops []installMutation
	var declared []string

	addCreate := func(key string, doc map[string]any) {
		ops = append(ops, installMutation{Op: "create", Key: key, Document: doc})
		declared = append(declared, key)
	}

	// Role vertices + aspects + canonical-name index (Story 1.5.5: folded
	// into the install batch — formerly identity-domain's substrate-direct
	// PreInstall). Deterministic NanoIDs make re-install idempotent; the
	// roleindex vertex lets cross-package canonical lookups resolve.
	for idx, r := range def.Roles {
		roleKey := "vtx.role." + roleIDs[idx]
		addCreate(roleKey, docVertex("role", nil))
		addCreate(roleKey+".canonicalName", docAspect(roleKey, "canonicalName", "canonicalName",
			map[string]any{"value": r.CanonicalName}))
		addCreate(roleKey+".description", docAspect(roleKey, "description", "description",
			map[string]any{"text": r.Description}))
		indexKey := "vtx.roleindex." + sha256NanoID("rolecanonical:"+r.CanonicalName)
		addCreate(indexKey, docVertex("roleindex",
			map[string]any{"canonicalName": r.CanonicalName, "roleId": roleIDs[idx]}))
	}

	// Fail-fast: validate all DDLSpec self-description fields before writing any entries.
	for idx, d := range def.DDLs {
		if d.InputSchema == "" {
			return nil, nil, fmt.Errorf("pkgmgr: DDL[%d] %q: InputSchema required", idx, d.CanonicalName)
		}
		if d.OutputSchema == "" {
			return nil, nil, fmt.Errorf("pkgmgr: DDL[%d] %q: OutputSchema required", idx, d.CanonicalName)
		}
		if len(d.FieldDescription) == 0 {
			return nil, nil, fmt.Errorf("pkgmgr: DDL[%d] %q: FieldDescription required", idx, d.CanonicalName)
		}
		if len(d.Examples) == 0 {
			return nil, nil, fmt.Errorf("pkgmgr: DDL[%d] %q: Examples required", idx, d.CanonicalName)
		}
	}

	// DDL meta-vertices + canonical aspects (4 structural + 4 self-description).
	for idx, d := range def.DDLs {
		ddlKey := metaVertexPrefix + ddlIDs[idx]
		class := d.Class
		if class == "" {
			class = "meta.ddl.vertexType"
		}
		addCreate(ddlKey, docVertex(class, nil))
		addCreate(ddlKey+".canonicalName", docAspect(ddlKey, "canonicalName", "canonicalName",
			map[string]any{"value": d.CanonicalName}))
		addCreate(ddlKey+".permittedCommands", docAspect(ddlKey, "permittedCommands", "permittedCommands",
			map[string]any{"commands": d.PermittedCommands}))
		addCreate(ddlKey+".description", docAspect(ddlKey, "description", "description",
			map[string]any{"text": d.Description}))
		addCreate(ddlKey+".script", docAspect(ddlKey, "script", "script",
			map[string]any{"source": d.Script}))
		// Self-description aspects: inputSchema, outputSchema, fieldDescription, examples.
		fdMap := make(map[string]any, len(d.FieldDescription))
		for k, v := range d.FieldDescription {
			fdMap[k] = v
		}
		exList := make([]any, len(d.Examples))
		for j, ex := range d.Examples {
			exList[j] = map[string]any{
				"name":            ex.Name,
				"payload":         ex.Payload,
				"expectedOutcome": ex.ExpectedOutcome,
			}
		}
		addCreate(ddlKey+".inputSchema", docAspect(ddlKey, "inputSchema", "inputSchema",
			map[string]any{"schema": d.InputSchema}))
		addCreate(ddlKey+".outputSchema", docAspect(ddlKey, "outputSchema", "outputSchema",
			map[string]any{"schema": d.OutputSchema}))
		addCreate(ddlKey+".fieldDescription", docAspect(ddlKey, "fieldDescription", "fieldDescription",
			map[string]any{"fieldDescriptions": fdMap}))
		addCreate(ddlKey+".examples", docAspect(ddlKey, "examples", "examples",
			map[string]any{"examples": exList}))
	}

	// Lens meta-vertices + canonical aspects.
	for idx, l := range def.Lenses {
		lensKey := metaVertexPrefix + lensIDs[idx]
		class := l.Class
		if class == "" {
			class = "meta.lens"
		}
		addCreate(lensKey, docVertex(class, nil))
		aspects := map[string]map[string]any{
			"canonicalName": {"value": l.CanonicalName},
			"spec":          {"source": l.Spec},
			"adapter":       {"value": l.Adapter},
			"bucket":        {"value": l.Bucket},
			"engine":        {"value": l.Engine},
		}
		// Stable iteration order so the install mutation list is deterministic
		// across invocations.
		names := make([]string, 0, len(aspects))
		for n := range aspects {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			addCreate(lensKey+"."+name, docAspect(lensKey, name, name, aspects[name]))
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
		addCreate(permKey, docVertex("permission", data))
		// Grant links — one per role canonical name in GrantsTo.
		for _, role := range p.GrantsTo {
			roleID := role
			if len(role) > len("vtx.role.") && role[:len("vtx.role.")] == "vtx.role." {
				roleID = role[len("vtx.role."):]
			}
			// Link canonical name is `grantedBy` (permission granted by role).
			linkKey := "lnk.permission." + permID + ".grantedBy.role." + roleID
			addCreate(linkKey, docLink("vtx.permission."+permID, "vtx.role."+roleID,
				"grantedBy", "grantedBy", nil))
		}
		_ = idx
	}

	// Package vertex + manifest aspect (carries the declared-keys list
	// for uninstall enumeration).
	addCreate(pkgKey, docVertex("package",
		map[string]any{"name": def.Name, "version": def.Version}))

	manifestData := map[string]any{
		"name":         def.Name,
		"version":      def.Version,
		"description":  def.Description,
		"depends":      def.Depends,
		"declaredKeys": append([]string{}, declared...), // snapshot before we add the manifest aspect itself
	}
	manifestKey := pkgKey + ".manifest"
	addCreate(manifestKey, docAspect(pkgKey, "manifest", "manifest", manifestData))

	return ops, declared, nil
}

// --- logical-document helpers (Story 1.5.5) ---
//
// These build LOGICAL documents (no provenance) for the InstallPackage op
// payload. The Processor stamps createdAt/createdBy/createdByOp at step 8
// from the install actor, so installed entities carry real provenance
// authored by the install actor.

// sha256NanoID derives a deterministic 20-char Contract #1 NanoID from an
// arbitrary string. Used as the stable canonical-name index suffix
// (vtx.roleindex.<sha256NanoID("rolecanonical:"+name)>) so re-install
// produces the same index key. Read only by Go installer code, never by
// scripts, so exact parity with the Starlark crypto.sha256NanoID builtin
// is not required.
func sha256NanoID(s string) string {
	sum := sha256.Sum256([]byte(s))
	out := make([]byte, substrate.NanoIDLength)
	for i := 0; i < substrate.NanoIDLength; i++ {
		hi := sum[(i*2)%len(sum)]
		lo := sum[((i*2)+1)%len(sum)]
		idx := (int(hi)<<8 | int(lo)) % len(substrate.Alphabet)
		out[i] = substrate.Alphabet[idx]
	}
	return string(out)
}

func docVertex(class string, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	return map[string]any{"class": class, "isDeleted": false, "data": data}
}

func docAspect(vertexKey, localName, class string, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	return map[string]any{
		"class":     class,
		"isDeleted": false,
		"data":      data,
		"vertexKey": vertexKey,
		"localName": localName,
	}
}

func docLink(youngerVertex, olderVertex, localName, class string, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	return map[string]any{
		"class":         class,
		"isDeleted":     false,
		"data":          data,
		"youngerVertex": youngerVertex,
		"olderVertex":   olderVertex,
		"localName":     localName,
	}
}
