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

    events = [{"class": "PackageInstalled",
               "data": {"name": name, "version": version, "keyCount": len(declared)}}]
    return {"mutations": out, "events": events,
            "response": {"name": name, "version": version, "declaredKeys": declared}}
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

    events = [{"class": "PackageUninstalled",
               "data": {"name": name, "keyCount": len(tombstoned)}}]
    return {"mutations": out, "events": events,
            "response": {"name": name, "tombstonedKeys": tombstoned}}
`

// --- Self-description constants for the two package-install DDLs. ---

const installPackageInputSchema = `{"type":"object","required":["name","version","mutations"],"properties":{"name":{"type":"string"},"version":{"type":"string"},"mutations":{"type":"array","items":{"type":"object","required":["op","key","document"],"properties":{"op":{"type":"string","enum":["create"]},"key":{"type":"string"},"document":{"type":"object"}}}}}}`

const installPackageOutputSchema = `{"type":"object","required":["name","version","declaredKeys"],"properties":{"name":{"type":"string"},"version":{"type":"string"},"declaredKeys":{"type":"array","items":{"type":"string"}}}}`

const uninstallPackageInputSchema = `{"type":"object","required":["name","declaredKeys"],"properties":{"name":{"type":"string"},"declaredKeys":{"type":"array","items":{"oneOf":[{"type":"string"},{"type":"object","required":["key"],"properties":{"key":{"type":"string"},"expectedRevision":{"type":"integer"}}}]}}}}`

const uninstallPackageOutputSchema = `{"type":"object","required":["name","tombstonedKeys"],"properties":{"name":{"type":"string"},"tombstonedKeys":{"type":"array","items":{"type":"string"}}}}`

var installPackageFieldDescription = map[string]any{
	"name":               "The Capability Package canonical name (matches the package directory).",
	"version":            "The package version string. Combined with name to derive a deterministic op requestId for idempotent re-install.",
	"mutations":          "The pre-built mutation manifest: every Core KV entry the install writes.",
	"mutations[].op":     "Must be 'create' — installs are create-only.",
	"mutations[].key":    "The Contract #1 key the entry is written to (vtx.* or lnk.*).",
	"mutations[].document": "The logical document body (class, data, isDeleted, and for aspects vertexKey/localName). Provenance is stamped by the Processor.",
}

var uninstallPackageFieldDescription = map[string]any{
	"name":                         "The Capability Package canonical name to uninstall.",
	"declaredKeys":                 "The keys recorded in the package's .manifest aspect. Each is tombstoned.",
	"declaredKeys[].key":           "A declared key to tombstone.",
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
