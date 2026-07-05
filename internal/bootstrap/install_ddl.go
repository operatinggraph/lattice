package bootstrap

// InstallPackageDDLScript and UninstallPackageDDLScript are the two
// primordial kernel DDLs that route Capability-Package install/uninstall
// through the Processor (Story 1.5.5). They are "thin scripts over a fat
// manifest": the client (internal/pkgmgr) pre-computes the full mutation
// set and ships it in the op payload; these scripts iterate that set,
// enforce guardrails, and emit it as the op's mutations. The single
// step-8 atomic commit lands all DDLs/lenses/permissions/grants at once
// and synchronously invalidates the vtx.meta.* DDL cache, so a class the
// package just declared is usable immediately (M5/B2 — no restart).
//
// Guardrails (InstallPackage is a privileged op — it must not be an
// arbitrary-write backdoor):
//   - key-shape: every key matches an allowed Contract #1 pattern
//     (vtx.<type>.<id>[.aspect], lnk.<...>); anything else is rejected;
//   - system/underscore aspect reject: no aspect localName may start with
//     an underscore (mirrors the step-6 sensitiveAspectScope convention);
//   - create-only: every mutation op must be "create" (no updates/
//     tombstones in an install).
//
// Kernel/protected-key protection is NOT enforced here. The install/uninstall
// scripts declare no ContextHint.Reads, so hydrated `state` is empty and a
// script-level data.protected check would be dead code. The AUTHORITATIVE
// protected-key backstop is the Processor commit-time guard
// (rejectProtectedMutations in internal/processor/step8_commit.go): for every
// update/tombstone it KVGets the 3-segment root and rejects the whole
// operation with ErrCodeProtectedKey when data.protected == true. That guard
// is path-independent and covers InstallPackage, UninstallPackage, meta-root,
// and any future DDL at once. InstallPackage is additionally safe by
// construction (create-only → CreateOnly conflicts on overwrite of any
// existing protected root).
//
// The script trusts the document body the client supplied (class/data/
// vertexKey/localName) but does NOT trust provenance — the Processor
// stamps createdAt/createdBy/createdByOp at step 8 from the install
// actor, so installed entities carry real provenance.

// installGuardrailHelpers is the shared Starlark prelude for both scripts:
// key-shape validation, reserved-root detection, and aspect-name checks.
const installGuardrailHelpers = `
def _key_segments(key):
    return key.split(".")

def _is_valid_key_shape(key):
    if len(key) == 0:
        return False
    parts = _key_segments(key)
    if len(parts) < 2:
        return False
    head = parts[0]
    # Allowed heads: vtx (vertex/aspect) and lnk (link).
    if head == "vtx":
        # vtx.<type>.<id> or vtx.<type>.<id>.<aspect...>
        return len(parts) >= 3
    if head == "lnk":
        # lnk.<...> — links carry many segments; require at least 3.
        return len(parts) >= 3
    return False

def _is_aspect_key(key):
    parts = _key_segments(key)
    return parts[0] == "vtx" and len(parts) >= 4

def _aspect_local_name(key):
    parts = _key_segments(key)
    return parts[len(parts) - 1]
`

// InstallPackageDDLScript iterates op.payload.mutations, enforces the
// install guardrails, and emits them as create mutations. Payload shape:
// {name, version, mutations: [{op, key, document}, ...]}.
const InstallPackageDDLScript = installGuardrailHelpers + `
def execute(state, op):
    p = op.payload
    if not hasattr(p, "name") or type(p.name) != type("") or len(p.name) == 0:
        fail("InvalidArgument: name: required non-empty string")
    if not hasattr(p, "version") or type(p.version) != type("") or len(p.version) == 0:
        fail("InvalidArgument: version: required non-empty string")
    if not hasattr(p, "mutations") or type(p.mutations) != type([]):
        fail("InvalidArgument: mutations: required list")
    name = p.name
    version = p.version

    out = []
    declared = []
    for m in p.mutations:
        if type(m) != type({}):
            fail("InvalidArgument: each mutation must be a dict")
        if "op" not in m or "key" not in m:
            fail("InvalidArgument: mutation requires op and key")
        mop = m["op"]
        key = m["key"]
        if mop != "create":
            fail("InvalidArgument: install mutations must be create-only, got " + mop)
        if type(key) != type("") or not _is_valid_key_shape(key):
            fail("InvalidArgument: illegal key shape: " + str(key))
        # NOTE: kernel/protected-key protection is enforced authoritatively by
        # the Processor commit-time guard (step 8 rejectProtectedMutations),
        # not here — installs are create-only so CreateOnly conflicts on any
        # existing protected root anyway.
        # Reject system/underscore-prefixed aspect names.
        if _is_aspect_key(key):
            local = _aspect_local_name(key)
            if len(local) > 0 and local[0] == "_":
                fail("InvalidArgument: underscore-prefixed aspect not allowed: " + key)
        if "document" not in m or type(m["document"]) != type({}):
            fail("InvalidArgument: mutation requires a document dict: " + key)
        out.append({"op": "create", "key": key, "document": m["document"]})
        declared.append(key)

    if len(out) == 0:
        fail("InvalidArgument: install produced no mutations")

    events = [{"class": "package.installed",
               "data": {"name": name, "version": version, "keyCount": len(declared)}}]
    # InstallPackage is multi-key with no single principal entity; it omits
    # primaryKey. Clients read the committed key set from OperationReply.Revisions
    # and name/version/keyCount from the PackageInstalled event.
    return {"mutations": out, "events": events}
`

// UninstallPackageDDLScript tombstones each declared key. Payload shape:
// {name, declaredKeys: [{key, expectedRevision}, ...] | [key, ...]}.
// Protected roots are rejected (defense in depth). When a key carries an
// integer expectedRevision the tombstone asserts it (OCC, closes F-011).
const UninstallPackageDDLScript = installGuardrailHelpers + `
def execute(state, op):
    p = op.payload
    if not hasattr(p, "name") or type(p.name) != type("") or len(p.name) == 0:
        fail("InvalidArgument: name: required non-empty string")
    if not hasattr(p, "declaredKeys") or type(p.declaredKeys) != type([]):
        fail("InvalidArgument: declaredKeys: required list")
    name = p.name

    out = []
    tombstoned = []
    for entry in p.declaredKeys:
        key = None
        expected = None
        if type(entry) == type(""):
            key = entry
        elif type(entry) == type({}):
            if "key" not in entry:
                fail("InvalidArgument: declaredKeys entry dict requires key")
            key = entry["key"]
            if "expectedRevision" in entry and entry["expectedRevision"] != None:
                expected = entry["expectedRevision"]
        else:
            fail("InvalidArgument: declaredKeys entry must be string or dict")

        if type(key) != type("") or not _is_valid_key_shape(key):
            fail("InvalidArgument: illegal key shape: " + str(key))
        # NOTE: protected-key (kernel/auth) protection is enforced
        # authoritatively by the Processor commit-time guard (step 8
        # rejectProtectedMutations), which KVGets each tombstone's root and
        # rejects the whole op when data.protected == true. This script does
        # NOT (and cannot, with empty hydrated state) backstop that.

        mut = {"op": "tombstone", "key": key,
               "document": {"isDeleted": True, "data": {}}}
        if expected != None:
            if type(expected) != type(0):
                fail("InvalidArgument: expectedRevision must be an integer: " + key)
            mut["expectedRevision"] = expected
        out.append(mut)
        tombstoned.append(key)

    if len(out) == 0:
        fail("InvalidArgument: uninstall produced no mutations")

    events = [{"class": "package.uninstalled",
               "data": {"name": name, "keyCount": len(tombstoned)}}]
    # UninstallPackage is multi-key with no single principal entity; it omits
    # primaryKey. Clients read the committed key set from OperationReply.Revisions
    # and name/keyCount from the PackageUninstalled event.
    return {"mutations": out, "events": events}
`

// UpgradePackageDDLScript applies a Capability-Package in-place upgrade by
// emitting a mixed create/update/tombstone mutation batch (Contract #8 §8.6).
// The client (internal/pkgmgr) reads the installed package's old declaredKeys,
// rebuilds the new manifest on version-independent keys (§8.1), diffs by key,
// and ships the delta in op.payload.mutations. Payload shape:
// {name, fromVersion, toVersion, mutations: [{op, key, document, expectedRevision?}, ...]}.
//
// Unlike InstallPackage this is NOT create-only — it carries update/tombstone
// ops, so it is not safe-by-construction. Protected kernel/auth roots are
// guarded authoritatively by the Processor commit-time guard (step 8
// rejectProtectedMutations), which KVGets each update/tombstone's root and
// rejects the whole op when data.protected == true — path-independent, the
// same backstop install/uninstall rely on. The script enforces the same
// key-shape + underscore-aspect guardrails as install (shared helpers) plus
// the op-vocabulary check (create/update/tombstone only). When an update or
// tombstone mutation carries an integer expectedRevision the mutation asserts
// it (per-key OCC, F-011/Contract #8 §8.6 — closes the sibling window to
// UninstallPackageDDLScript's).
const UpgradePackageDDLScript = installGuardrailHelpers + `
def execute(state, op):
    p = op.payload
    if not hasattr(p, "name") or type(p.name) != type("") or len(p.name) == 0:
        fail("InvalidArgument: name: required non-empty string")
    if not hasattr(p, "fromVersion") or type(p.fromVersion) != type("") or len(p.fromVersion) == 0:
        fail("InvalidArgument: fromVersion: required non-empty string")
    if not hasattr(p, "toVersion") or type(p.toVersion) != type("") or len(p.toVersion) == 0:
        fail("InvalidArgument: toVersion: required non-empty string")
    if not hasattr(p, "mutations") or type(p.mutations) != type([]):
        fail("InvalidArgument: mutations: required list")
    name = p.name

    out = []
    created = 0
    updated = 0
    tombstoned = 0
    for m in p.mutations:
        if type(m) != type({}):
            fail("InvalidArgument: each mutation must be a dict")
        if "op" not in m or "key" not in m:
            fail("InvalidArgument: mutation requires op and key")
        mop = m["op"]
        key = m["key"]
        if mop != "create" and mop != "update" and mop != "tombstone":
            fail("InvalidArgument: upgrade mutations must be create/update/tombstone, got " + str(mop))
        if type(key) != type("") or not _is_valid_key_shape(key):
            fail("InvalidArgument: illegal key shape: " + str(key))
        # NOTE: protected-key (kernel/auth) protection is enforced
        # authoritatively by the Processor commit-time guard (step 8
        # rejectProtectedMutations) for every update/tombstone, not here —
        # the upgrade script runs with empty hydrated state.
        if _is_aspect_key(key):
            local = _aspect_local_name(key)
            if len(local) > 0 and local[0] == "_":
                fail("InvalidArgument: underscore-prefixed aspect not allowed: " + key)
        if "document" not in m or type(m["document"]) != type({}):
            fail("InvalidArgument: mutation requires a document dict: " + key)
        out_mut = {"op": mop, "key": key, "document": m["document"]}
        if "expectedRevision" in m and m["expectedRevision"] != None:
            expected = m["expectedRevision"]
            if type(expected) != type(0):
                fail("InvalidArgument: expectedRevision must be an integer: " + key)
            out_mut["expectedRevision"] = expected
        out.append(out_mut)
        if mop == "create":
            created = created + 1
        elif mop == "update":
            updated = updated + 1
        else:
            tombstoned = tombstoned + 1

    if len(out) == 0:
        fail("InvalidArgument: upgrade produced no mutations")

    events = [{"class": "package.upgraded",
               "data": {"name": name, "fromVersion": p.fromVersion, "toVersion": p.toVersion,
                        "createdCount": created, "updatedCount": updated, "tombstonedCount": tombstoned}}]
    # UpgradePackage is multi-key with no single principal entity; it omits
    # primaryKey. Clients read the committed key set from OperationReply.Revisions
    # and the counts from the PackageUpgraded event.
    return {"mutations": out, "events": events}
`

// --- Self-description constants for the three package-install DDLs. ---

const installPackageInputSchema = `{"type":"object","required":["name","version","mutations"],"properties":{"name":{"type":"string"},"version":{"type":"string"},"mutations":{"type":"array","items":{"type":"object","required":["op","key","document"],"properties":{"op":{"type":"string","enum":["create"]},"key":{"type":"string"},"document":{"type":"object"}}}}}}`

// InstallPackage is multi-key with no single principal entity, so the reply
// carries no primaryKey. The committed key set is the key set of
// OperationReply.Revisions; name/version/keyCount ride the PackageInstalled event.
const installPackageOutputSchema = `{"type":"object","properties":{}}`

const uninstallPackageInputSchema = `{"type":"object","required":["name","declaredKeys"],"properties":{"name":{"type":"string"},"declaredKeys":{"type":"array","items":{"oneOf":[{"type":"string"},{"type":"object","required":["key"],"properties":{"key":{"type":"string"},"expectedRevision":{"type":"integer"}}}]}}}}`

// UninstallPackage is multi-key with no single principal entity, so the reply
// carries no primaryKey. The committed (tombstoned) key set is the key set of
// OperationReply.Revisions; name/keyCount ride the PackageUninstalled event.
const uninstallPackageOutputSchema = `{"type":"object","properties":{}}`

var installPackageFieldDescription = map[string]any{
	"name":                 "The Capability Package canonical name (matches the package directory).",
	"version":              "The package version string. Combined with name to derive a deterministic op requestId for idempotent re-install.",
	"mutations":            "The pre-built mutation manifest: every Core KV entry the install writes.",
	"mutations[].op":       "Must be 'create' — installs are create-only.",
	"mutations[].key":      "The Contract #1 key the entry is written to (vtx.* or lnk.*).",
	"mutations[].document": "The logical document body (class, data, isDeleted, and for aspects vertexKey/localName). Provenance is stamped by the Processor.",
}

var uninstallPackageFieldDescription = map[string]any{
	"name":                            "The Capability Package canonical name to uninstall.",
	"declaredKeys":                    "The keys recorded in the package's .manifest aspect. Each is tombstoned.",
	"declaredKeys[].key":              "A declared key to tombstone.",
	"declaredKeys[].expectedRevision": "Optional NATS revision for OCC — the tombstone fails if the key was modified since the client read it.",
}

var installPackageExamples = []any{
	map[string]any{
		"name": "Install a two-entry package",
		"payload": map[string]any{
			"name":    "demo-domain",
			"version": "1.0.0",
			"mutations": []any{
				map[string]any{"op": "create", "key": "vtx.meta.AbCdEfGhJkLmNpQrStUv",
					"document": map[string]any{"class": "meta.ddl.vertexType", "isDeleted": false, "data": map[string]any{}}},
			},
		},
		"expectedOutcome": "Commits all mutations in one atomic batch; the new DDL is immediately usable (DDL cache invalidated in-commit).",
	},
}

var uninstallPackageExamples = []any{
	map[string]any{
		"name": "Uninstall a package",
		"payload": map[string]any{
			"name":         "demo-domain",
			"declaredKeys": []any{"vtx.meta.AbCdEfGhJkLmNpQrStUv"},
		},
		"expectedOutcome": "Tombstones every declared key in one atomic batch.",
	},
}

const upgradePackageInputSchema = `{"type":"object","required":["name","fromVersion","toVersion","mutations"],"properties":{"name":{"type":"string"},"fromVersion":{"type":"string"},"toVersion":{"type":"string"},"mutations":{"type":"array","items":{"type":"object","required":["op","key","document"],"properties":{"op":{"type":"string","enum":["create","update","tombstone"]},"key":{"type":"string"},"document":{"type":"object"},"expectedRevision":{"type":"integer"}}}}}}`

// UpgradePackage is multi-key with no single principal entity, so the reply
// carries no primaryKey. The committed key set is the key set of
// OperationReply.Revisions; name/versions/counts ride the PackageUpgraded event.
const upgradePackageOutputSchema = `{"type":"object","properties":{}}`

var upgradePackageFieldDescription = map[string]any{
	"name":                         "The Capability Package canonical name to upgrade in place.",
	"fromVersion":                  "The currently-installed version. Combined with name+toVersion to derive a deterministic op requestId so a re-submitted upgrade dedup-short-circuits.",
	"toVersion":                    "The target version. Equal to fromVersion for a dev-mode same-version re-apply (update-only).",
	"mutations":                    "The pre-computed diff delta: create new entities, update changed bodies, tombstone removed entities — applied in one atomic batch.",
	"mutations[].op":               "One of 'create' / 'update' / 'tombstone'.",
	"mutations[].key":              "The version-independent Contract #1 key the mutation targets (vtx.* or lnk.*).",
	"mutations[].document":         "The logical document body. Provenance is stamped by the Processor; protected kernel/auth roots are rejected at commit time.",
	"mutations[].expectedRevision": "Optional NATS revision for OCC on an update/tombstone — the mutation fails if the key was modified since the diff's read.",
}

var upgradePackageExamples = []any{
	map[string]any{
		"name": "Upgrade a package with one new + one changed + one removed entity",
		"payload": map[string]any{
			"name":        "demo-domain",
			"fromVersion": "0.1.0",
			"toVersion":   "0.2.0",
			"mutations": []any{
				map[string]any{"op": "update", "key": "vtx.meta.AbCdEfGhJkLmNpQrStUv",
					"document": map[string]any{"class": "meta.lens", "isDeleted": false, "data": map[string]any{}}},
				map[string]any{"op": "create", "key": "vtx.meta.WxYz123456789AbCdEfG",
					"document": map[string]any{"class": "meta.ddl.vertexType", "isDeleted": false, "data": map[string]any{}}},
				map[string]any{"op": "tombstone", "key": "vtx.permission.MnPqRsTuVwXyZ1234567",
					"document": map[string]any{"isDeleted": true, "data": map[string]any{}}},
			},
		},
		"expectedOutcome": "Commits the mixed delta in one atomic batch; changed lens/DDL meta-vertices are re-projected and the DDL cache is invalidated in-commit (no restart).",
	},
}
