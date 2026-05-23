# Story 5.1: DDL Self-Description Aspects

Status: review

## Story

As a platform engineer,
I want every DDL meta-vertex to carry plain-language description, input/output schema, field descriptions, and example aspects,
so that any traverser (AI or human) can understand what an operation does and how to invoke it without out-of-band documentation.

## Acceptance Criteria

1. **Five aspect-type meta-vertices** exist after bootstrap (class `meta.ddl.aspectType`, each with a `canonicalName` aspect):
   - canonicalName `description` — non-sensitive; markdown text; max 10KB
   - canonicalName `inputSchema` — non-sensitive; JSON Schema string for operation payload
   - canonicalName `outputSchema` — non-sensitive; JSON Schema string for response
   - canonicalName `fieldDescription` — non-sensitive; map of fieldPath → plain-language description
   - canonicalName `examples` — non-sensitive; array of `{ name, payload, expectedOutcome }`

2. **Every bootstrap-seeded DDL meta-vertex** carries all five descriptive aspects populated — no placeholders. Includes:
   - Kernel `root` DDL (`meta.ddl.vertexType`, governs `vtx.meta.*`)
   - The five aspect-type meta-vertices themselves (`meta.ddl.aspectType`)
   - Package DDLs: `rbac`, `identity`, `identityHygiene`

3. **`CreateMetaVertex` for DDL classes requires all five** descriptive aspects in the payload. Operations missing any are rejected with `MissingSelfDescription: <aspectName>: required` (error raised inside the Starlark script).

4. **Aspects are readable without elevated grants** — Core KV has no per-key read ACL; the test verifies aspects exist and are non-empty without needing an operator capability.

5. **Integration test** covers: (a) every bootstrap+package DDL has all 5 aspects, (b) `CreateMetaVertex` without descriptive aspects rejected with `MissingSelfDescription`, (c) aspect values non-empty and valid JSON where applicable.

## Tasks / Subtasks

- [ ] Task 1 — Add 5 aspect-type NanoID variables to bootstrap (AC: 1)
  - [ ] 1.1 Add `AspectTypeDescription*`, `AspectTypeInputSchema*`, etc. vars to `internal/bootstrap/nanoid.go`
  - [ ] 1.2 Add fields to `PrimordialIDsRaw` + update `generate()`, `populate()`, `currentRaw()`
  - [ ] 1.3 Update `PrimordialVertexKeys()` + bump `PrimordialVertexKeyCount`

- [ ] Task 2 — Seed 5 aspect-type meta-vertices as primordial entries (AC: 1, 2)
  - [ ] 2.1 Add 5 aspect-type meta-vertex + their own 5 descriptive aspects each in `buildPrimordialEntries()`
  - [ ] 2.2 Add 4 missing descriptive aspects to the kernel `root` DDL in `buildPrimordialEntries()` (`.description` already exists)
  - [ ] 2.3 Update bootstrap kernel comment (count grows from ~33 → ~78 entries)

- [ ] Task 3 — Update `MetaRootDDLScript` to enforce and write all 5 aspects (AC: 3)
  - [ ] 3.1 Add `required_dict` + `required_list` helpers to the Starlark script
  - [ ] 3.2 In `is_ddl_class` branch: validate `inputSchema`, `outputSchema`, `fieldDescription`, `examples` with `MissingSelfDescription` error
  - [ ] 3.3 Emit 4 new `make_aspect` calls for each of the 5 descriptive aspects in the `CreateMetaVertex` mutation batch

- [ ] Task 4 — Extend `pkgmgr.DDLSpec` with 4 new fields + installer writes them (AC: 2, 3)
  - [ ] 4.1 Add `InputSchema`, `OutputSchema`, `FieldDescription`, `Examples` fields + `ExampleSpec` type to `internal/pkgmgr/definition.go`
  - [ ] 4.2 Update `buildInstallBatch` to write the 4 new aspects + fail-fast if any DDLSpec field is empty

- [ ] Task 5 — Populate all 5 fields in every package DDLSpec (AC: 2)
  - [ ] 5.1 `packages/rbac-domain/ddls.go` — add `InputSchema`, `OutputSchema`, `FieldDescription`, `Examples` to `rbac` spec
  - [ ] 5.2 `packages/identity-domain/ddls.go` — same for `identity` spec
  - [ ] 5.3 `packages/identity-hygiene/ddls.go` — same for `identityHygiene` spec

- [ ] Task 6 — Integration test (AC: 1–5)
  - [ ] 6.1 Create `internal/bootstrap/self_description_e2e_test.go` (package `bootstrap_test`)
  - [ ] 6.2 Test: every kernel + package DDL has all 5 non-empty aspects
  - [ ] 6.3 Test: `CreateMetaVertex` without `inputSchema` (and others) rejected with `MissingSelfDescription`
  - [ ] 6.4 Test: aspects readable via plain Core KV get (no operator cap needed)

- [ ] Task 7 — Update verify scripts (AC: 2)
  - [ ] 7.1 `scripts/verify-kernel.go` — assert 5 aspect-type meta-vertices + their aspects + 4 new root DDL aspects
  - [ ] 7.2 `scripts/verify-package-rbac.go` — assert 4 new DDL aspects
  - [ ] 7.3 `scripts/verify-package-identity.go` — assert 4 new DDL aspects
  - [ ] 7.4 `scripts/verify-package-identity-hygiene.go` — assert 4 new DDL aspects

## Dev Notes

### Overview

Story 5.1 is entirely additive — no existing behaviour is removed. The deliverable is the FR19 substrate: every DDL meta-vertex gains 4 new aspect keys (`inputSchema`, `outputSchema`, `fieldDescription`, `examples`), and the `CreateMetaVertex` Starlark script is updated to require and write them. Five new primordial aspect-type meta-vertices define the schema for these aspect classes.

The description aspect already exists on several vertices; its class `description` now has a formal `meta.ddl.aspectType` DDL that story 5.1 seeds primordially.

**No Processor code changes** — only bootstrap, pkgmgr, package DDLs, and the Starlark script embedded in `meta_ddl.go`.

---

### Aspect Data Shapes (canonical)

All five aspects are written as standard aspect envelopes per Contract #1 §1.4. The `data` field shapes are:

| Aspect class | Data shape |
|---|---|
| `description` | `{ "text": "<markdown string>" }` |
| `inputSchema` | `{ "schema": "<JSON Schema as string>" }` |
| `outputSchema` | `{ "schema": "<JSON Schema as string>" }` |
| `fieldDescription` | `{ "fieldDescriptions": { "<fieldPath>": "<description>" } }` |
| `examples` | `{ "examples": [{ "name": "...", "payload": {...}, "expectedOutcome": "..." }] }` |

The `description` shape `{"text": ...}` is already used consistently across the codebase. Do NOT use `{"value": ...}` (that is for canonicalName / simple string aspects). Do NOT use `{"source": ...}` (that is for Starlark scripts).

---

### File-by-File Implementation Guide

#### `internal/bootstrap/nanoid.go`

Add after the existing `PermTombstoneMetaVertex*` vars:

```go
// Story 5.1: five aspect-type meta-vertex NanoIDs — the self-description
// aspect DDLs (description, inputSchema, outputSchema, fieldDescription, examples).
AspectTypeDescriptionID  string
AspectTypeDescriptionKey string
AspectTypeInputSchemaID  string
AspectTypeInputSchemaKey string
AspectTypeOutputSchemaID  string
AspectTypeOutputSchemaKey string
AspectTypeFieldDescriptionID  string
AspectTypeFieldDescriptionKey string
AspectTypeExamplesID  string
AspectTypeExamplesKey string
```

Add to `PrimordialIDsRaw`:
```go
AspectTypeDescription  string `json:"aspectTypeDescription"`
AspectTypeInputSchema  string `json:"aspectTypeInputSchema"`
AspectTypeOutputSchema string `json:"aspectTypeOutputSchema"`
AspectTypeFieldDescription string `json:"aspectTypeFieldDescription"`
AspectTypeExamples     string `json:"aspectTypeExamples"`
```

Add 5 pointer slots to `generate()` targets slice.

Add to `populate()`:
```go
AspectTypeDescriptionID  = raw.AspectTypeDescription
AspectTypeDescriptionKey = "vtx.meta." + AspectTypeDescriptionID
// ... repeat for other 4 ...
```

Add to `currentRaw()`:
```go
AspectTypeDescription:     AspectTypeDescriptionID,
// ...
```

Add validation entries in the `fields` slice inside `populate()`.

Update `PrimordialVertexKeys()`: add the 5 aspect-type meta-vertex keys (not their aspect keys — those are checked separately in verify-kernel).

Update `PrimordialVertexKeyCount` from 13 → 18 (5 new top-level vertices).

---

#### `internal/bootstrap/primordial.go`

**In `buildPrimordialEntries()`**, after seeding the capability lenses and before the operator role:

Seed each of the 5 aspect-type meta-vertices. Each gets 6 entries: the vertex itself + 5 descriptive aspects.

Boilerplate to follow for each (shown for `description`):

```go
// --- Aspect-type meta-vertex: "description" ---
{
    key := AspectTypeDescriptionKey
    vtxVal, vtxErr := MakeVertexEnvelope(key, "meta.ddl.aspectType", map[string]any{})
    if err := add(key, vtxVal, vtxErr); err != nil { return nil, err }
    // canonicalName
    cnKey := key + ".canonicalName"
    cnVal, cnErr := MakeAspectEnvelope(cnKey, key, "canonicalName", "canonicalName",
        map[string]any{"value": "description"})
    if err := add(cnKey, cnVal, cnErr); err != nil { return nil, err }
    // description (of the description aspect type itself)
    dKey := key + ".description"
    dVal, dErr := MakeAspectEnvelope(dKey, key, "description", "description",
        map[string]any{"text": "Plain-language markdown description for a DDL meta-vertex, lens, role, or aspect type. Stored at vtx.meta.<X>.description. Max 10KB."})
    if err := add(dKey, dVal, dErr); err != nil { return nil, err }
    // inputSchema
    isKey := key + ".inputSchema"
    isVal, isErr := MakeAspectEnvelope(isKey, key, "inputSchema", "inputSchema",
        map[string]any{"schema": `{"type":"object","properties":{"text":{"type":"string","maxLength":10240}},"required":["text"]}`})
    if err := add(isKey, isVal, isErr); err != nil { return nil, err }
    // outputSchema
    osKey := key + ".outputSchema"
    osVal, osErr := MakeAspectEnvelope(osKey, key, "outputSchema", "outputSchema",
        map[string]any{"schema": `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`})
    if err := add(osKey, osVal, osErr); err != nil { return nil, err }
    // fieldDescription
    fdKey := key + ".fieldDescription"
    fdVal, fdErr := MakeAspectEnvelope(fdKey, key, "fieldDescription", "fieldDescription",
        map[string]any{"fieldDescriptions": map[string]any{
            "text": "The markdown-formatted description content. Used by AI agents and humans to understand the entity's purpose.",
        }})
    if err := add(fdKey, fdVal, fdErr); err != nil { return nil, err }
    // examples
    exKey := key + ".examples"
    exVal, exErr := MakeAspectEnvelope(exKey, key, "examples", "examples",
        map[string]any{"examples": []any{
            map[string]any{
                "name":            "identity DDL description",
                "payload":         map[string]any{"text": "Identity vertex. Carries name, email, phone, state, claimKey, credentialBinding, mergedInto aspects."},
                "expectedOutcome": "Stored at vtx.meta.<id>.description; readable by AI traversers.",
            },
        }})
    if err := add(exKey, exVal, exErr); err != nil { return nil, err }
}
```

Repeat for `inputSchema`, `outputSchema`, `fieldDescription`, `examples` aspect-type meta-vertices with appropriate descriptions/schemas/examples.

**Content for the 5 aspect-type meta-vertices** (use these exact values — dev should not invent alternate wording):

**`inputSchema` meta-vertex** (key = `AspectTypeInputSchemaKey`):
- `.description`: `"JSON Schema object describing the valid input payload for a DDL operation. Stored at vtx.meta.<X>.inputSchema."`
- `.inputSchema.schema`: `{"type":"object","properties":{"schema":{"type":"string"}},"required":["schema"]}`
- `.outputSchema.schema`: `{"type":"object","properties":{"schema":{"type":"string"}},"required":["schema"]}`
- `.fieldDescription.fieldDescriptions`: `{"schema": "The JSON Schema for the operation's input payload, serialized as a string."}`
- `.examples`: `[{"name":"CreateRole inputSchema","payload":{"schema":"{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\"},\"description\":{\"type\":\"string\"}},\"required\":[\"name\"]}"},"expectedOutcome":"Validates CreateRole payloads; rejects missing `name`."}]`

**`outputSchema` meta-vertex** (key = `AspectTypeOutputSchemaKey`):
- `.description`: `"JSON Schema object describing the structure of the operation's response payload. Stored at vtx.meta.<X>.outputSchema."`
- `.inputSchema.schema`: `{"type":"object","properties":{"schema":{"type":"string"}},"required":["schema"]}`
- `.outputSchema.schema`: `{"type":"object","properties":{"schema":{"type":"string"}},"required":["schema"]}`
- `.fieldDescription.fieldDescriptions`: `{"schema": "The JSON Schema for the operation's response, serialized as a string."}`
- `.examples`: `[{"name":"CreateRole outputSchema","payload":{"schema":"{\"type\":\"object\",\"properties\":{\"roleKey\":{\"type\":\"string\"}},\"required\":[\"roleKey\"]}"},"expectedOutcome":"Documents that CreateRole returns a roleKey."}]`

**`fieldDescription` meta-vertex** (key = `AspectTypeFieldDescriptionKey`):
- `.description`: `"Map of field paths to plain-language descriptions. Enables AI agents to understand each input field for a DDL operation. Stored at vtx.meta.<X>.fieldDescription."`
- `.inputSchema.schema`: `{"type":"object","properties":{"fieldDescriptions":{"type":"object","additionalProperties":{"type":"string"}}},"required":["fieldDescriptions"]}`
- `.outputSchema.schema`: `{"type":"object","properties":{"fieldDescriptions":{"type":"object","additionalProperties":{"type":"string"}}},"required":["fieldDescriptions"]}`
- `.fieldDescription.fieldDescriptions`: `{"fieldDescriptions": "A map where each key is a field path (e.g. `name`, `actorKey`) and each value is a plain-language description."}`
- `.examples`: `[{"name":"CreateRole fieldDescription","payload":{"fieldDescriptions":{"name":"The canonical name for the new role (e.g. `consumer`, `backOfHouse`).","description":"Optional human-readable description of the role's purpose."}},"expectedOutcome":"Helps AI agents understand each CreateRole parameter."}]`

**`examples` meta-vertex** (key = `AspectTypeExamplesKey`):
- `.description`: `"Array of named usage examples for a DDL operation. Each includes a sample payload and expected outcome. Stored at vtx.meta.<X>.examples."`
- `.inputSchema.schema`: `{"type":"object","properties":{"examples":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"payload":{"type":"object"},"expectedOutcome":{"type":"string"}},"required":["name","payload","expectedOutcome"]}}},"required":["examples"]}`
- `.outputSchema.schema`: same as inputSchema
- `.fieldDescription.fieldDescriptions`: `{"examples": "Array of example invocations.", "examples[].name": "Short descriptive name.", "examples[].payload": "The operation payload sent by the client.", "examples[].expectedOutcome": "Plain English description of what the platform does."}`
- `.examples`: `[{"name":"examples self-example","payload":{"examples":[{"name":"CreateRole example","payload":{"name":"barista"},"expectedOutcome":"Creates vtx.role.<NanoID> with canonicalName=barista."}]},"expectedOutcome":"This is the examples aspect for the examples aspect type — recursive but valid."}]`

**Add 4 missing aspects to the kernel `root` DDL** (after the existing `.script` entry):

The `root` DDL currently has: vertex, canonicalName, permittedCommands, description, script. Add:

```go
rootInputSchemaKey := MetaRootKey + ".inputSchema"
risa, risaErr := MakeAspectEnvelope(rootInputSchemaKey, MetaRootKey, "inputSchema", "inputSchema",
    map[string]any{"schema": `{"type":"object","required":["targetClass","canonicalName"],"properties":{"targetClass":{"type":"string","description":"One of meta.ddl.vertexType|linkType|aspectType|eventType|meta.lens"},"canonicalName":{"type":"string"},"permittedCommands":{"type":"array","items":{"type":"string"}},"description":{"type":"string"},"script":{"type":"string"},"inputSchema":{"type":"string"},"outputSchema":{"type":"string"},"fieldDescription":{"type":"object"},"examples":{"type":"array"},"spec":{"type":"string"}}}`})
if err := add(rootInputSchemaKey, risa, risaErr); err != nil { return nil, err }

rootOutputSchemaKey := MetaRootKey + ".outputSchema"
rosa, rosaErr := MakeAspectEnvelope(rootOutputSchemaKey, MetaRootKey, "outputSchema", "outputSchema",
    map[string]any{"schema": `{"type":"object","properties":{"metaKey":{"type":"string"}},"required":["metaKey"]}`})
if err := add(rootOutputSchemaKey, rosa, rosaErr); err != nil { return nil, err }

rootFDKey := MetaRootKey + ".fieldDescription"
rfd, rfdErr := MakeAspectEnvelope(rootFDKey, MetaRootKey, "fieldDescription", "fieldDescription",
    map[string]any{"fieldDescriptions": map[string]any{
        "targetClass":       "The meta-vertex class to create. DDL classes: meta.ddl.vertexType, meta.ddl.linkType, meta.ddl.aspectType, meta.ddl.eventType. Lens class: meta.lens.",
        "canonicalName":     "The unique canonical name for this DDL. Used by Processor DDL cache for class lookup.",
        "permittedCommands": "List of operationType strings that may produce mutations of this vertex type.",
        "description":       "Plain-language markdown description of this DDL's purpose and behaviour.",
        "script":            "Starlark source for the DDL's execute(state, op) function.",
        "inputSchema":       "JSON Schema string for the operation payload accepted by this DDL.",
        "outputSchema":      "JSON Schema string for the operation response produced by this DDL.",
        "fieldDescription":  "Map of fieldPath to plain-language description for each payload field.",
        "examples":          "Array of {name, payload, expectedOutcome} usage examples.",
    }})
if err := add(rootFDKey, rfd, rfdErr); err != nil { return nil, err }

rootExamplesKey := MetaRootKey + ".examples"
rex, rexErr := MakeAspectEnvelope(rootExamplesKey, MetaRootKey, "examples", "examples",
    map[string]any{"examples": []any{
        map[string]any{
            "name": "CreateMetaVertex — new DDL",
            "payload": map[string]any{
                "targetClass":      "meta.ddl.vertexType",
                "canonicalName":    "book",
                "permittedCommands": []string{"CreateBook", "UpdateBook"},
                "description":      "Book vertex DDL. Carries title, author, isbn aspects.",
                "script":           "def execute(state, op): ...",
                "inputSchema":      `{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`,
                "outputSchema":     `{"type":"object","required":["bookKey"],"properties":{"bookKey":{"type":"string"}}}`,
                "fieldDescription": map[string]any{"title": "Book title, max 500 chars."},
                "examples":         []any{},
            },
            "expectedOutcome": "Creates vtx.meta.<NanoID> with class=meta.ddl.vertexType and 9 aspect keys.",
        },
    }})
if err := add(rootExamplesKey, rex, rexErr); err != nil { return nil, err }
```

---

#### `internal/bootstrap/meta_ddl.go`

**Update `MetaRootDDLScript`** to validate + write all 5 aspects for the `is_ddl_class` branch.

Add helper functions at the top of the Starlark:

```python
def required_dict(p, name):
    if not hasattr(p, name):
        fail("MissingSelfDescription: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type({}):
        fail("MissingSelfDescription: " + name + ": required non-empty dict")
    if len(v) == 0:
        fail("MissingSelfDescription: " + name + ": required non-empty dict")
    return v

def required_list(p, name):
    if not hasattr(p, name):
        fail("MissingSelfDescription: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type([]):
        fail("MissingSelfDescription: " + name + ": required non-empty list")
    if len(v) == 0:
        fail("MissingSelfDescription: " + name + ": required non-empty list")
    return v
```

Update the error for missing `description` and `script` in `required_string` calls within the DDL branch to use `MissingSelfDescription:` prefix:

```python
description = required_string(p, "description")   # keep as is — required_string raises InvalidArgument, change prefix:
```

Actually, change the `required_string` calls to use `MissingSelfDescription` prefix for the 5 descriptive fields. The simplest approach: inline-validate each and raise `MissingSelfDescription: <name>: required`:

```python
if is_ddl_class(target_class):
    permitted = p.permittedCommands if hasattr(p, "permittedCommands") else None
    if permitted == None or type(permitted) != type([]):
        fail("InvalidArgument: permittedCommands: required list of strings")
    for c in permitted:
        if type(c) != type(""):
            fail("InvalidArgument: permittedCommands: each entry must be a string")

    # Self-description validation — all 5 required, no placeholders.
    def need_str(name):
        if not hasattr(p, name):
            fail("MissingSelfDescription: " + name + ": required")
        v = getattr(p, name)
        if v == None or type(v) != type("") or len(v.strip()) == 0:
            fail("MissingSelfDescription: " + name + ": required non-empty string")
        return v.strip()

    description  = need_str("description")
    script_src   = need_str("script")
    input_schema = need_str("inputSchema")
    output_schema = need_str("outputSchema")
    field_desc = required_dict(p, "fieldDescription")
    examples   = required_list(p, "examples")

    meta_id  = nanoid.new()
    meta_key = "vtx.meta." + meta_id
    mutations = [
        make_vtx(meta_key, target_class, {}),
        make_aspect(meta_key + ".canonicalName",      meta_key, "canonicalName",    "canonicalName",    {"value": canonical_name}),
        make_aspect(meta_key + ".permittedCommands",  meta_key, "permittedCommands","permittedCommands",{"commands": permitted}),
        make_aspect(meta_key + ".description",        meta_key, "description",      "description",      {"text": description}),
        make_aspect(meta_key + ".script",             meta_key, "script",           "script",           {"source": script_src}),
        make_aspect(meta_key + ".inputSchema",        meta_key, "inputSchema",      "inputSchema",      {"schema": input_schema}),
        make_aspect(meta_key + ".outputSchema",       meta_key, "outputSchema",     "outputSchema",     {"schema": output_schema}),
        make_aspect(meta_key + ".fieldDescription",   meta_key, "fieldDescription", "fieldDescription", {"fieldDescriptions": field_desc}),
        make_aspect(meta_key + ".examples",           meta_key, "examples",         "examples",         {"examples": examples}),
    ]
    events = [{"class": "MetaVertexCreated",
               "data": {"metaKey": meta_key, "targetClass": target_class,
                        "canonicalName": canonical_name}}]
    return {"mutations": mutations, "events": events,
            "response": {"metaKey": meta_key}}
```

The `meta.lens` branch does NOT need all 5 (lenses are not `meta.ddl.*` class; only DDL meta-vertices require them). Leave that branch unchanged.

---

#### `internal/pkgmgr/definition.go`

Add to `DDLSpec`:
```go
// InputSchema is a JSON Schema string describing the operation's input payload.
// Required — installer fails fast if empty.
InputSchema string

// OutputSchema is a JSON Schema string describing the operation's response.
// Required — installer fails fast if empty.
OutputSchema string

// FieldDescription maps payload field paths to plain-language descriptions.
// Required — installer fails fast if empty.
FieldDescription map[string]string

// Examples is a list of named usage examples for this DDL.
// Required — installer fails fast if empty.
Examples []ExampleSpec
```

Add `ExampleSpec` type:
```go
// ExampleSpec is one named usage example for a DDL operation.
type ExampleSpec struct {
    Name            string         `json:"name"`
    Payload         map[string]any `json:"payload"`
    ExpectedOutcome string         `json:"expectedOutcome"`
}
```

---

#### `internal/pkgmgr/build.go`

In `buildInstallBatch`, after writing the existing 4 aspects per DDL (`canonicalName`, `permittedCommands`, `description`, `script`), add:

```go
// Hard quality gate: all 5 self-description fields required.
if d.InputSchema == "" {
    return nil, nil, fmt.Errorf("pkgmgr: DDL[%d] %q: InputSchema required (Story 5.1 quality gate)", idx, d.CanonicalName)
}
if d.OutputSchema == "" {
    return nil, nil, fmt.Errorf("pkgmgr: DDL[%d] %q: OutputSchema required (Story 5.1 quality gate)", idx, d.CanonicalName)
}
if len(d.FieldDescription) == 0 {
    return nil, nil, fmt.Errorf("pkgmgr: DDL[%d] %q: FieldDescription required (Story 5.1 quality gate)", idx, d.CanonicalName)
}
if len(d.Examples) == 0 {
    return nil, nil, fmt.Errorf("pkgmgr: DDL[%d] %q: Examples required (Story 5.1 quality gate)", idx, d.CanonicalName)
}
// inputSchema aspect
is, err := i.makeAspectEnvelope(ddlKey+".inputSchema", ddlKey, "inputSchema", "inputSchema",
    map[string]any{"schema": d.InputSchema}, createdByOp, now)
if err != nil {
    return nil, nil, err
}
addCreate(ddlKey+".inputSchema", is)
// outputSchema aspect
os, err := i.makeAspectEnvelope(ddlKey+".outputSchema", ddlKey, "outputSchema", "outputSchema",
    map[string]any{"schema": d.OutputSchema}, createdByOp, now)
if err != nil {
    return nil, nil, err
}
addCreate(ddlKey+".outputSchema", os)
// fieldDescription aspect
fdData := make(map[string]any, len(d.FieldDescription))
for k, v := range d.FieldDescription {
    fdData[k] = v
}
fd, err := i.makeAspectEnvelope(ddlKey+".fieldDescription", ddlKey, "fieldDescription", "fieldDescription",
    map[string]any{"fieldDescriptions": fdData}, createdByOp, now)
if err != nil {
    return nil, nil, err
}
addCreate(ddlKey+".fieldDescription", fd)
// examples aspect
exSlice := make([]any, len(d.Examples))
for ei, ex := range d.Examples {
    exSlice[ei] = map[string]any{
        "name":            ex.Name,
        "payload":         ex.Payload,
        "expectedOutcome": ex.ExpectedOutcome,
    }
}
exAsp, err := i.makeAspectEnvelope(ddlKey+".examples", ddlKey, "examples", "examples",
    map[string]any{"examples": exSlice}, createdByOp, now)
if err != nil {
    return nil, nil, err
}
addCreate(ddlKey+".examples", exAsp)
```

Place the validation block BEFORE the `addCreate(ddlKey, vtxEnv)` call (fail early, before allocating any NanoIDs).

---

#### `packages/rbac-domain/ddls.go`

Add to the single `DDLSpec` in `DDLs()`:

```go
InputSchema: `{"type":"object","required":[],"oneOf":[` +
    `{"title":"CreateRole","required":["name"],"properties":{"name":{"type":"string","description":"Role canonical name"},"description":{"type":"string"}}},` +
    `{"title":"UpdateRole","required":["roleKey"],"properties":{"roleKey":{"type":"string"},"description":{"type":"string"}}},` +
    `{"title":"TombstoneRole","required":["roleKey"],"properties":{"roleKey":{"type":"string"}}},` +
    `{"title":"CreatePermission","required":["operationType"],"properties":{"operationType":{"type":"string"},"scope":{"type":"string","enum":["any","self"]},"note":{"type":"string"}}},` +
    `{"title":"AssignRole","required":["actorKey","roleKey"],"properties":{"actorKey":{"type":"string"},"roleKey":{"type":"string"}}},` +
    `{"title":"GrantPermission","required":["permKey","roleKey"],"properties":{"permKey":{"type":"string"},"roleKey":{"type":"string"}}}` +
    `]}`,
OutputSchema: `{"type":"object","properties":{"roleKey":{"type":"string"},"permissionKey":{"type":"string"},"linkKey":{"type":"string"},"alreadyAssigned":{"type":"boolean"},"alreadyGranted":{"type":"boolean"}}}`,
FieldDescription: map[string]string{
    "name":          "Canonical name for the role (e.g. consumer, backOfHouse).",
    "description":   "Optional human-readable description of the role.",
    "roleKey":       "vtx.role.<NanoID> key of the target role.",
    "permKey":       "vtx.permission.<NanoID> key of the target permission.",
    "actorKey":      "vtx.<type>.<NanoID> key of the identity or entity receiving the role.",
    "operationType": "The operation type string this permission gates (e.g. CreateRole).",
    "scope":         "any = grants for all targets; self = only self-directed ops. Defaults to any.",
},
Examples: []pkgmgr.ExampleSpec{
    {
        Name:            "CreateRole — new backOfHouse role",
        Payload:         map[string]any{"name": "backOfHouse", "description": "Back-of-house staff with kitchen access."},
        ExpectedOutcome: "Creates vtx.role.<NanoID> with canonicalName=backOfHouse and description aspects.",
    },
    {
        Name:            "AssignRole — assign barista to consumer role",
        Payload:         map[string]any{"actorKey": "vtx.identity.<NanoID>", "roleKey": "vtx.role.<NanoID>"},
        ExpectedOutcome: "Creates lnk.identity.<actorId>.holdsRole.role.<roleId>.",
    },
},
```

---

#### `packages/identity-domain/ddls.go`

Add to the `identity` DDLSpec:

```go
InputSchema: `{"type":"object","required":[],"oneOf":[` +
    `{"title":"CreateUnclaimedIdentity","required":["name"],"properties":{"name":{"type":"string","maxLength":200},"email":{"type":"string"},"phone":{"type":"string"}}},` +
    `{"title":"UpdateIdentityState","required":["identityKey","newState"],"properties":{"identityKey":{"type":"string"},"newState":{"type":"string","enum":["claimed"]}}},` +
    `{"title":"ClaimIdentity","required":["targetIdentityKey","claimKey"],"properties":{"targetIdentityKey":{"type":"string"},"claimKey":{"type":"string"}}}` +
    `]}`,
OutputSchema: `{"type":"object","properties":{"identityKey":{"type":"string"},"claimKey":{"type":"string","description":"Plaintext one-time claim key (CreateUnclaimedIdentity only)"},"possibleDuplicateFlag":{"type":"boolean"}}}`,
FieldDescription: map[string]string{
    "name":              "Full display name, max 200 chars. Required for CreateUnclaimedIdentity.",
    "email":             "Email address. Lowercased and stored. At least one of email/phone required.",
    "phone":             "Phone number. Digits + leading + only; stored in normalized form.",
    "identityKey":       "vtx.identity.<NanoID> key of the identity to update.",
    "newState":          "Target state. Currently only `claimed` is a valid transition from `unclaimed`.",
    "targetIdentityKey": "vtx.identity.<NanoID> key for ClaimIdentity.",
    "claimKey":          "One-time claim key (plaintext) returned by CreateUnclaimedIdentity.",
},
Examples: []pkgmgr.ExampleSpec{
    {
        Name:            "CreateUnclaimedIdentity — new staff member",
        Payload:         map[string]any{"name": "Jane Doe", "email": "jane@example.com"},
        ExpectedOutcome: "Creates vtx.identity.<NanoID> with state=unclaimed; returns identityKey + claimKey.",
    },
    {
        Name:            "ClaimIdentity — staff member claims their identity",
        Payload:         map[string]any{"targetIdentityKey": "vtx.identity.<NanoID>", "claimKey": "<one-time-key>"},
        ExpectedOutcome: "Transitions state unclaimed→claimed; tombstones claimKey aspect; creates credentialBinding.",
    },
},
```

---

#### `packages/identity-hygiene/ddls.go`

Add to the `identityHygiene` DDLSpec:

```go
InputSchema: `{"type":"object","required":["primary","secondary","edges"],"properties":{"primary":{"type":"string","description":"vtx.identity.<NanoID>"},"secondary":{"type":"string","description":"vtx.identity.<NanoID>"},"edges":{"type":"array","items":{"type":"string"},"description":"Link vertex keys touching secondary"},"aspectConflictResolution":{"type":"object","properties":{"name":{"type":"string","enum":["secondary-wins"]},"email":{"type":"string","enum":["secondary-wins"]},"phone":{"type":"string","enum":["secondary-wins"]}}}}}`,
OutputSchema: `{"type":"object","required":["primary","secondary","mutationCount"],"properties":{"primary":{"type":"string"},"secondary":{"type":"string"},"mutationCount":{"type":"integer"},"linksMigrated":{"type":"integer"},"linksTombstonedOnly":{"type":"integer"},"linkCollisionsMerged":{"type":"integer"},"eventCount":{"type":"integer"}}}`,
FieldDescription: map[string]string{
    "primary":                  "vtx.identity.<NanoID> key of the identity to survive the merge.",
    "secondary":                "vtx.identity.<NanoID> key of the identity to be merged (set to state=merged).",
    "edges":                    "List of link vertex keys that touch the secondary identity, discovered via the duplicateCandidates Lens.",
    "aspectConflictResolution": "Optional; if `secondary-wins` for name/email/phone, the secondary's value overwrites the primary's aspect.",
},
Examples: []pkgmgr.ExampleSpec{
    {
        Name: "MergeIdentity — merge duplicate into canonical",
        Payload: map[string]any{
            "primary":   "vtx.identity.<primaryNanoID>",
            "secondary": "vtx.identity.<secondaryNanoID>",
            "edges":     []string{"lnk.identity.<secondaryId>.holdsRole.role.<roleId>"},
        },
        ExpectedOutcome: "Migrates edge to primary; sets secondary.state=merged; secondary.mergedInto=primary. Returns commit-trace detail.",
    },
},
```

---

#### `internal/bootstrap/self_description_e2e_test.go` (new file)

Use package `bootstrap_test` (external, consistent with the pattern in packages/*).

```go
package bootstrap_test

import (
    "encoding/json"
    "strings"
    "testing"
    "time"

    "github.com/asolgan/lattice/internal/bootstrap"
    "github.com/asolgan/lattice/internal/processor"
    "github.com/asolgan/lattice/internal/testutil"
)
// Note: no substrate import needed — testutil.GenReqID handles request IDs.

// TestSelfDescription_KernelDDLsHaveAllFiveAspects verifies that every
// bootstrap-seeded DDL meta-vertex (kernel + packages) carries the 5
// descriptive aspects with non-empty content.
func TestSelfDescription_KernelDDLsHaveAllFiveAspects(t *testing.T) {
    // SetupPackageTestEnv: StartEmbeddedNATS + Connect + ProvisionHarness
    // + bootstrap.LoadOrGenerate + SeedPrimordial + InstallPhase1Packages.
    ctx, conn := testutil.SetupPackageTestEnv(t)

    // Enumerate all vtx.meta.* keys; for each DDL meta-vertex
    // (class is meta.ddl.* or has canonicalName aspect) assert 5 aspects present.
    keys, err := conn.KVListKeys(ctx, bootstrap.CoreKVBucket)
    if err != nil {
        t.Fatalf("KVListKeys: %v", err)
    }

    // Collect DDL meta-vertex keys (3-segment vtx.meta.<id>).
    ddlKeys := []string{}
    for _, k := range keys {
        parts := strings.Split(k, ".")
        if len(parts) == 3 && parts[0] == "vtx" && parts[1] == "meta" {
            env, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, k)
            if err != nil {
                continue
            }
            var doc struct {
                Class     string `json:"class"`
                IsDeleted bool   `json:"isDeleted"`
            }
            if json.Unmarshal(env.Value, &doc) != nil || doc.IsDeleted {
                continue
            }
            // Only DDL meta-vertices (meta.ddl.* class) need all 5 aspects.
            if strings.HasPrefix(doc.Class, "meta.ddl.") {
                ddlKeys = append(ddlKeys, k)
            }
        }
    }
    if len(ddlKeys) == 0 {
        t.Fatal("no DDL meta-vertices found — likely bootstrap seeding failed")
    }

    for _, vtxKey := range ddlKeys {
        for _, aspect := range []string{"description", "inputSchema", "outputSchema", "fieldDescription", "examples"} {
            ak := vtxKey + "." + aspect
            entry, err := conn.KVGet(ctx, bootstrap.CoreKVBucket, ak)
            if err != nil {
                t.Errorf("DDL %s: missing aspect %s: %v", vtxKey, aspect, err)
                continue
            }
            var doc struct {
                Data map[string]any `json:"data"`
            }
            if err := json.Unmarshal(entry.Value, &doc); err != nil {
                t.Errorf("DDL %s aspect %s: invalid JSON: %v", vtxKey, aspect, err)
                continue
            }
            if len(doc.Data) == 0 {
                t.Errorf("DDL %s aspect %s: data is empty", vtxKey, aspect)
            }
        }
    }
}

// TestSelfDescription_CreateMetaVertexRequiresAllFiveAspects verifies
// that the MetaRootDDLScript rejects CreateMetaVertex ops that omit
// any of the 5 descriptive aspects.
func TestSelfDescription_CreateMetaVertexRequiresAllFiveAspects(t *testing.T) {
    ctx, conn := testutil.SetupPackageTestEnv(t)

    cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
        Durable:  "sd-test",
        Instance: "sd-test-inst",
    })

    validPayload := map[string]any{
        "targetClass":      "meta.ddl.vertexType",
        "canonicalName":    "testDDL",
        "permittedCommands": []string{"CreateTest"},
        "description":      "Test DDL for self-description validation.",
        "script":           "def execute(state, op): return {'mutations': [], 'events': []}",
        "inputSchema":      `{"type":"object"}`,
        "outputSchema":     `{"type":"object"}`,
        "fieldDescription": map[string]any{"testField": "A test field."},
        "examples":         []any{map[string]any{"name": "test", "payload": map[string]any{}, "expectedOutcome": "ok"}},
    }

    // Missing each of the 5 descriptive aspects one at a time.
    for _, missing := range []string{"description", "inputSchema", "outputSchema", "fieldDescription", "examples"} {
        t.Run("missing_"+missing, func(t *testing.T) {
            payload := make(map[string]any, len(validPayload))
            for k, v := range validPayload {
                payload[k] = v
            }
            delete(payload, missing)
            // Use unique canonicalName per subtest to avoid idempotency cache hits.
            payload["canonicalName"] = "testDDL_missing_" + missing

            payloadBytes, _ := json.Marshal(payload)
            env := &processor.OperationEnvelope{
                RequestID:     testutil.GenReqID("miss_" + missing),
                Lane:          processor.LaneMeta,
                OperationType: "CreateMetaVertex",
                Actor:         bootstrap.BootstrapIdentityKey,
                SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
                Payload:       payloadBytes,
                Class:         "root",
            }
            testutil.PublishOp(t, conn, env)
            outcome := testutil.DriveOne(t, ctx, cp, cons, processor.MessageOutcomeRejected)
            if outcome != processor.MessageOutcomeRejected {
                t.Errorf("expected Rejected for missing %s, got %v", missing, outcome)
            }
        })
    }

    // Verify a fully-populated payload is accepted.
    t.Run("fully_populated_accepted", func(t *testing.T) {
        validPayload["canonicalName"] = "testDDL_fully_populated"
        payloadBytes, _ := json.Marshal(validPayload)
        reqID := testutil.GenReqID("fullydone")
        env := &processor.OperationEnvelope{
            RequestID:     reqID,
            Lane:          processor.LaneMeta,
            OperationType: "CreateMetaVertex",
            Actor:         bootstrap.BootstrapIdentityKey,
            SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
            Payload:       payloadBytes,
            Class:         "root",
        }
        testutil.PublishOp(t, conn, env)
        outcome := testutil.DriveOne(t, ctx, cp, cons, processor.MessageOutcomeCommitted)
        if outcome != processor.MessageOutcomeCommitted {
            t.Errorf("expected Committed for fully-populated DDL, got %v", outcome)
        }
    })
}
```

**Test helper notes:**
- `testutil.SetupPackageTestEnv(t)` — in `internal/testutil/pipeline.go:196`; handles embedded NATS + ProvisionHarness + LoadOrGenerate + SeedPrimordial + InstallPhase1Packages.
- `testutil.CapabilityPipeline(t, ctx, conn, cfg)` — real CommitPath with DDLCache + CapabilityAuthorizer.
- `testutil.PublishOp(t, conn, env)` — in `internal/testutil/pipeline.go:168`.
- `testutil.DriveOne(t, ctx, cp, cons, want)` — in `internal/testutil/embedded_nats.go:58`.
- `testutil.GenReqID(label)` — deterministic 20-char request ID from label.
- For each missing-field subtest, generate a unique `RequestID` to avoid idempotency cache collisions: use `testutil.GenReqID("miss_" + missing)`.
- The `Class: "root"` field on the envelope tells the Processor which DDL to use (transitional Phase-1 hint per Contract #2 §2.1 addendum).

---

#### `scripts/verify-kernel.go`

In the existing assertions, add after the meta-DDL aspects check:

```go
// 2a. Five aspect-type meta-vertices (Story 5.1).
for _, entry := range []struct{ key, name string }{
    {bootstrap.AspectTypeDescriptionKey,     "description"},
    {bootstrap.AspectTypeInputSchemaKey,     "inputSchema"},
    {bootstrap.AspectTypeOutputSchemaKey,    "outputSchema"},
    {bootstrap.AspectTypeFieldDescriptionKey,"fieldDescription"},
    {bootstrap.AspectTypeExamplesKey,        "examples"},
} {
    if _, err := coreKV.Get(ctx, entry.key); err != nil {
        failures = append(failures, fmt.Sprintf("MISSING aspect-type meta-vertex for %q: %v", entry.name, err))
    } else {
        fmt.Printf("  OK  aspect-type vertex: %s\n", entry.name)
    }
    for _, aspect := range []string{"canonicalName", "description", "inputSchema", "outputSchema", "fieldDescription", "examples"} {
        ak := entry.key + "." + aspect
        if _, err := coreKV.Get(ctx, ak); err != nil {
            failures = append(failures, fmt.Sprintf("MISSING aspect %s on aspect-type %q: %v", aspect, entry.name, err))
        } else {
            fmt.Printf("  OK  %s\n", ak)
        }
    }
}

// 2b. Meta-meta root DDL must have all 5 descriptive aspects.
for _, aspect := range []string{"inputSchema", "outputSchema", "fieldDescription", "examples"} {
    k := bootstrap.MetaRootKey + "." + aspect
    if _, err := coreKV.Get(ctx, k); err != nil {
        failures = append(failures, fmt.Sprintf("MISSING root DDL aspect: %s (%v)", k, err))
    } else {
        fmt.Printf("  OK  %s\n", k)
    }
}
```

Update the kernel key count comment.

---

#### `scripts/verify-package-rbac.go`, `verify-package-identity.go`, `verify-package-identity-hygiene.go`

In each, after the existing DDL aspect assertions (canonicalName/permittedCommands/description/script), add:

```go
for _, aspect := range []string{"inputSchema", "outputSchema", "fieldDescription", "examples"} {
    k := ddlKey + "." + aspect
    if _, err := coreKV.Get(ctx, k); err != nil {
        failures = append(failures, fmt.Sprintf("MISSING DDL aspect: %s (%v)", k, err))
    } else {
        fmt.Printf("  OK  %s\n", k)
    }
}
```

---

### Project Structure Notes

- No new packages or directories — all changes are in existing files.
- Bootstrap NanoID count: kernel grows from 13 top-level vertices → 18 (5 new aspect-type meta-vertices). `PrimordialVertexKeyCount` must be updated.
- Total Core KV entry count grows by ~35 entries (5 vertices × 6 aspects = 30, plus 4 new aspects on root DDL = 34). The bootstrap kernel grows from ~33 → ~67 entries.
- The `BootstrapFile.Version` stays at `"3"` — the 5.1 primordial IDs are additive and old JSON files that lack them will regenerate cleanly (the field will be absent; `populate` will get empty strings; validation will fail; fresh `make up` required for existing deployments, which is expected).

### Architecture Compliance

- No new Processor code (no commit-path steps changed).
- No new ContextHint fields — the Starlark script validates the payload only; no graph reads for self-description enforcement.
- No adjacency lookups — `required_dict` / `required_list` validate `op.payload` fields only.
- NanoIDs: use `substrate.NewNanoID()` in `generate()` — do NOT hard-code IDs.
- The 5 aspect-type meta-vertices are primordial (not package-installed) because they define the schema for aspects used by all other DDLs. Bootstrapping them via a package would create a chicken-and-egg dependency.
- For the `fieldDescription` aspect, the Starlark script receives it as a dict (Starlark `type(v) == type({})`) and the data is stored as `{"fieldDescriptions": { ... }}`. The map values must be strings.

### References

- [Source: _bmad-output/planning-artifacts/epics.md#Story 5.1]
- [Source: _bmad-output/planning-artifacts/data-contracts.md#1.5 DDL Lookup, #1.7 Meta-DDL Structure]
- [Source: internal/bootstrap/nanoid.go — NanoID pattern for new primordial IDs]
- [Source: internal/bootstrap/primordial.go#buildPrimordialEntries — seeding pattern]
- [Source: internal/bootstrap/meta_ddl.go#MetaRootDDLScript — Starlark update target]
- [Source: internal/pkgmgr/definition.go — DDLSpec extension target]
- [Source: internal/pkgmgr/build.go#buildInstallBatch — installer aspect-write target]
- [Source: packages/rbac-domain/ddls.go — package DDLSpec pattern]
- [Source: internal/testutil/pipeline.go — CapabilityPipeline + DriveOne test helpers]
- [Source: _bmad-output/implementation-artifacts/phase-1-progress.md — kernel composition after 4.7]

## Dev Agent Record

### Agent Model Used

claude-opus-4-7 (recommended; ~115K budget)

### Debug Log References

### Completion Notes List

### File List
