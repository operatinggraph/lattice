package pkgmgr

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/asolgan/lattice/internal/substrate"
)

// Envelope classes for the orchestration meta-vertices this installer emits.
// They match exactly what the Weaver registry / Loom pattern source route on:
// a meta.weaverTarget / meta.loomPattern vertex carries its body in a sibling
// `.spec` aspect; an op-meta vertex carries operationType on the vertex `data`
// itself and is classed meta.ddl.vertexType (a non-routed meta class the op
// index probes).
const (
	weaverTargetClass     = "meta.weaverTarget"
	weaverTargetSpecClass = "weaverTargetSpec"
	loomPatternClass      = "meta.loomPattern"
	loomPatternSpecClass  = "loomPatternSpec"
	opMetaClass           = "meta.ddl.vertexType"
)

// metaVertexPrefix is the Contract #1 prefix for both DDL and Lens
// meta-vertices (`vtx.meta.<NanoID>`).
const metaVertexPrefix = "vtx.meta."

// installMutation is one entry in the InstallPackage op payload's
// `mutations` list — a LOGICAL document (no provenance). The Processor
// stamps createdAt/createdBy/createdByOp at step 8 from the install actor.
// ExpectedRevision is the per-key OCC token (F-011, Contract #8 §8.6): an
// upgrade's update/tombstone mutation carries the revision its diff read
// observed, so a concurrent write racing the upgrade fails the whole batch
// instead of being silently overwritten. Unset on create (already
// conditioned create-only) and on every install mutation.
type installMutation struct {
	Op               string         `json:"op"`
	Key              string         `json:"key"`
	Document         map[string]any `json:"document"`
	ExpectedRevision *uint64        `json:"expectedRevision,omitempty"`
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
	weaverTargetIDs, loomPatternIDs, opMetaIDs []string,
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
		// Sensitivity marker (lattice-architecture Item 6). Emitted only for
		// a sensitive aspect-type DDL; absent → the DDL cache treats the
		// aspect type as non-sensitive (ddl_cache loadMetaVertex reads
		// <root>.sensitive). Conditional keeps the common non-sensitive DDL's
		// install batch byte-for-byte unchanged.
		if d.Sensitive {
			addCreate(ddlKey+".sensitive", docAspect(ddlKey, "sensitive", "sensitive",
				map[string]any{"value": true}))
		}
	}

	// Lens meta-vertices + canonical aspects.
	for idx, l := range def.Lenses {
		lensKey := metaVertexPrefix + lensIDs[idx]
		lensID := lensIDs[idx]
		class := l.Class
		if class == "" {
			class = "meta.lens"
		}
		addCreate(lensKey, docVertex(class, nil))
		// The `spec` aspect carries the full LensSpec body Refractor's
		// CoreKVSource activates the lens from (cypherRule + targetConfig +
		// engine + projectionKind + the §6.13 Output descriptor). The sibling
		// canonicalName/adapter/bucket/engine aspects remain an operator-facing
		// documentation surface.
		aspects := map[string]map[string]any{
			"canonicalName": {"value": l.CanonicalName},
			"spec":          lensSpecBody(lensID, l),
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

	// canonicalName → in-batch lens NanoID, so a WeaverTarget's LensRef
	// authored as a lens canonicalName resolves to the id the engine's control
	// surface expects.
	lensByCanonical := make(map[string]string, len(def.Lenses))
	for idx, l := range def.Lenses {
		lensByCanonical[l.CanonicalName] = lensIDs[idx]
	}

	// WeaverTarget meta-vertices + spec aspect. The vertex carries empty data;
	// the `.spec` aspect carries the target body the Weaver registry CDC source
	// unwraps from `.data` and deserializes into a runtime Target.
	for idx, t := range def.WeaverTargets {
		targetKey := metaVertexPrefix + weaverTargetIDs[idx]
		lensRef, err := resolveLensRef(t.LensRef, lensByCanonical)
		if err != nil {
			return nil, nil, fmt.Errorf("pkgmgr: WeaverTarget[%d] %q: %w", idx, t.TargetID, err)
		}
		addCreate(targetKey, docVertex(weaverTargetClass, nil))
		addCreate(targetKey+".spec", docAspect(targetKey, "spec", weaverTargetSpecClass,
			weaverTargetSpecBody(t, lensRef)))
	}

	// LoomPattern meta-vertices + spec aspect. Same envelope as the Lens spec;
	// the Loom pattern source unwraps the pattern body from `.data`.
	for idx, p := range def.LoomPatterns {
		patternKey := metaVertexPrefix + loomPatternIDs[idx]
		addCreate(patternKey, docVertex(loomPatternClass, nil))
		addCreate(patternKey+".spec", docAspect(patternKey, "spec", loomPatternSpecClass,
			loomPatternSpecBody(p)))
	}

	// effectsByOp flattens every DDL's Effects map (keyed by operationType) so
	// the op-meta loop below can attach each op's declared guards regardless of
	// which DDL happens to implement it. validateEffects already enforces one
	// declaration per operationType per package (an Effects key must be exactly
	// one DDL's PermittedCommands entry), so no entry is ever overwritten here.
	effectsByOp := make(map[string][]json.RawMessage)
	for _, d := range def.DDLs {
		for op, guards := range d.Effects {
			effectsByOp[op] = guards
		}
	}

	// Op-meta vertices: a non-routed meta-vertex carrying operationType on its
	// own `data`, indexed by both engines so a forOperation reference resolves.
	// No spec aspect — operationType lives on the vertex envelope. An op whose
	// operationType carries declared Effects (§10.8 Planner extension) gets a
	// sibling `.effects` aspect — the runtime catalog the Weaver planner's goal
	// regression (Fire 6) reads at dispatch time; an op with no Effects emits
	// nothing extra (byte-identical to every install before this fire).
	for idx, o := range def.OpMetas {
		opMetaKey := metaVertexPrefix + opMetaIDs[idx]
		addCreate(opMetaKey, docVertex(opMetaClass,
			map[string]any{"operationType": o.OperationType}))
		if guards := effectsByOp[o.OperationType]; len(guards) > 0 {
			addCreate(opMetaKey+".effects", docAspect(opMetaKey, "effects", "effects",
				map[string]any{"guards": guards}))
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

// lensSpecBody builds the LensSpec body stored as the `spec` aspect's data.
// Refractor's CoreKVSource unwraps the aspect's `data` to this object and
// activates the lens from it (cypherRule + targetConfig + engine +
// projectionKind + the §6.13 Output descriptor).
func lensSpecBody(lensID string, l LensSpec) map[string]any {
	var targetType string
	var targetConfig map[string]any
	switch l.Adapter {
	case "postgres":
		targetType = "postgres"
		targetConfig = map[string]any{
			"dsn":   l.DSN,
			"table": l.Table,
		}
		// A GrantTable lens with no declared key omits it so Refractor applies
		// the platform grant composite (actor_id, anchor_id, grant_source);
		// every other postgres lens defaults to ["key"].
		if len(l.IntoKey) > 0 {
			targetConfig["key"] = l.IntoKey
		} else if !l.GrantTable {
			targetConfig["key"] = []string{"key"}
		}
		if l.QueryTimeout != "" {
			targetConfig["queryTimeout"] = l.QueryTimeout
		}
		if l.Protected {
			targetConfig["protected"] = true
		}
		if l.Public {
			targetConfig["public"] = true
		}
		if l.GrantTable {
			targetConfig["grantTable"] = true
		}
		if l.DiffRetraction {
			targetConfig["diffRetraction"] = true
		}
		if len(l.Columns) > 0 {
			cols := make([]map[string]any, len(l.Columns))
			for i, c := range l.Columns {
				cols[i] = map[string]any{"name": c.Name, "type": c.Type}
			}
			targetConfig["columns"] = cols
		}
		if len(l.SecureColumns) > 0 {
			secure := make([]map[string]any, len(l.SecureColumns))
			for i, c := range l.SecureColumns {
				entry := map[string]any{
					"column":            c.Column,
					"identityKeyColumn": c.IdentityKeyColumn,
				}
				if c.Field != "" {
					entry["field"] = c.Field
				}
				secure[i] = entry
			}
			targetConfig["secureColumns"] = secure
		}
	default: // "nats-kv" or empty
		targetType = "nats_kv"
		keyField := l.IntoKey
		if len(keyField) == 0 {
			keyField = []string{"key"}
		}
		targetConfig = map[string]any{
			"bucket": l.Bucket,
			"key":    keyField,
		}
	}

	spec := map[string]any{
		"id":            lensID,
		"canonicalName": l.CanonicalName,
		"targetType":    targetType,
		"targetConfig":  targetConfig,
		"cypherRule":    l.Spec,
		"engine":        l.Engine,
	}
	if l.ProjectionKind != "" {
		spec["projectionKind"] = l.ProjectionKind
	}
	if l.Output != nil {
		spec["output"] = l.Output
	}
	if l.Source != nil {
		spec["source"] = l.Source
	}
	return spec
}

// resolveLensRef maps a WeaverTarget's authored LensRef to the id the engine's
// control surface expects. A LensRef matching a declared lens canonicalName
// resolves to that lens's in-batch NanoID; a LensRef already shaped as a valid
// NanoID is passed through verbatim (it names a lens in an already-installed
// package). Anything else is a fail-closed install error — a dangling
// control-surface reference is a config bug. An empty LensRef passes through
// (the target declares no violation lens binding).
func resolveLensRef(lensRef string, lensByCanonical map[string]string) (string, error) {
	if lensRef == "" {
		return "", nil
	}
	if id, ok := lensByCanonical[lensRef]; ok {
		return id, nil
	}
	if substrate.IsValidNanoID(lensRef) {
		return lensRef, nil
	}
	return "", fmt.Errorf("LensRef %q matches no declared lens canonicalName and is not a valid NanoID", lensRef)
}

// weaverTargetSpecBody builds the meta.weaverTarget body stored as the `spec`
// aspect's data — the §10.8 `{targetId, lensRef, gaps}` shape the Weaver
// registry deserializes into a runtime Target. Optional gap-action fields are
// omitted when empty so the emitted body matches the engine's minimal shape.
// The `pattern` (triggerLoom) and `operation` (assignTask/directOp) refs
// are shipped verbatim; the engine registry resolves them live at dispatch
// (patternMetaKey / opMetaKey).
func weaverTargetSpecBody(t WeaverTargetSpec, lensRef string) map[string]any {
	gaps := make(map[string]any, len(t.Gaps))
	for col, ga := range t.Gaps {
		gaps[col] = gapActionBody(ga)
	}
	body := map[string]any{
		"targetId": t.TargetID,
		"lensRef":  lensRef,
		"gaps":     gaps,
	}
	if t.Augur != nil {
		body["augur"] = augurBody(t.Augur)
	}
	return body
}

// augurBody emits the optional §10.8 `augur` escalation block, including only
// the fields the engine parses and omitting empty optionals so the emitted body
// matches the engine's AugurPolicy shape. Op/Adapter/ReplyOp are shipped verbatim
// (the engine defaults them at dispatch when omitted). Emitted only when
// t.Augur != nil, so a target without escalation round-trips to the
// frozen-contract shape.
func augurBody(a *AugurSpec) map[string]any {
	body := map[string]any{}
	if len(a.Escalate) > 0 {
		escalate := make([]any, len(a.Escalate))
		for i, e := range a.Escalate {
			escalate[i] = e
		}
		body["escalate"] = escalate
	}
	if a.Op != "" {
		body["op"] = a.Op
	}
	if a.Adapter != "" {
		body["adapter"] = a.Adapter
	}
	if a.ReplyOp != "" {
		body["replyOp"] = a.ReplyOp
	}
	if a.Model != "" {
		body["model"] = a.Model
	}
	if a.AutoApply != nil {
		auto := map[string]any{}
		if len(a.AutoApply.Actions) > 0 {
			actions := make([]any, len(a.AutoApply.Actions))
			for i, act := range a.AutoApply.Actions {
				actions[i] = act
			}
			auto["actions"] = actions
		}
		if a.AutoApply.MinConfidence != 0 {
			auto["minConfidence"] = a.AutoApply.MinConfidence
		}
		body["autoApply"] = auto
	}
	return body
}

// gapActionBody emits one playbook entry, including only the fields the engine
// parses and omitting empty optionals so the body matches the fixture shape.
func gapActionBody(ga GapActionSpec) map[string]any {
	body := map[string]any{"action": ga.Action}
	if ga.Pattern != "" {
		body["pattern"] = ga.Pattern
	}
	if ga.Subject != "" {
		body["subject"] = ga.Subject
	}
	if ga.Adapter != "" {
		body["adapter"] = ga.Adapter
	}
	if ga.Operation != "" {
		body["operation"] = ga.Operation
	}
	if ga.Assignee != "" {
		body["assignee"] = ga.Assignee
	}
	if ga.Target != "" {
		body["target"] = ga.Target
	}
	if len(ga.Params) > 0 {
		params := make(map[string]any, len(ga.Params))
		for k, v := range ga.Params {
			params[k] = v
		}
		body["params"] = params
	}
	if len(ga.Reads) > 0 {
		reads := make([]any, len(ga.Reads))
		for i, r := range ga.Reads {
			reads[i] = r
		}
		body["reads"] = reads
	}
	if ga.IssueCode != "" {
		body["issueCode"] = ga.IssueCode
	}
	if ga.IssueSeverity != "" {
		body["issueSeverity"] = ga.IssueSeverity
	}
	return body
}

// loomPatternSpecBody builds the meta.loomPattern body stored as the `spec`
// aspect's data — the §10.5 `{patternId, subjectType, completionDomains?,
// steps}` shape the Loom pattern source deserializes into a runtime Pattern.
// completionDomains is omitted when empty (it defaults to {subjectType}); a
// step's guard is omitted when nil. systemOp/userTask emit `operation`;
// externalTask emits `adapter`/`params`/`replyOp`/`instanceOp` — each field is
// emitted only when set, so the round-tripped step matches the engine Step
// shape the validate() admits.
func loomPatternSpecBody(p LoomPatternSpec) map[string]any {
	steps := make([]any, len(p.Steps))
	for i, s := range p.Steps {
		step := map[string]any{"kind": s.Kind}
		if s.Operation != "" {
			step["operation"] = s.Operation
		}
		if s.Adapter != "" {
			step["adapter"] = s.Adapter
		}
		if len(s.Params) > 0 {
			step["params"] = s.Params
		}
		if s.ReplyOp != "" {
			step["replyOp"] = s.ReplyOp
		}
		if s.InstanceOp != "" {
			step["instanceOp"] = s.InstanceOp
		}
		if len(s.Guard) > 0 {
			step["guard"] = s.Guard
		}
		steps[i] = step
	}
	body := map[string]any{
		"patternId":   p.PatternID,
		"subjectType": p.SubjectType,
		"steps":       steps,
	}
	if len(p.CompletionDomains) > 0 {
		body["completionDomains"] = p.CompletionDomains
	}
	return body
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

func docLink(sourceVertex, targetVertex, localName, class string, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	return map[string]any{
		"class":        class,
		"isDeleted":    false,
		"data":         data,
		"sourceVertex": sourceVertex,
		"targetVertex": targetVertex,
		"localName":    localName,
	}
}
