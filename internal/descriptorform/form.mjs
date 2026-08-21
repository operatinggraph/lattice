// internal/descriptorform/form.mjs — the shared op-form renderer every staff
// vertical app mounts at /shared/form.mjs (staff-descriptor-rendering-design.md
// §13). One implementation of the op-catalog descriptor vocabulary — schema-
// to-field-kind detection, template substitution, and authContext assembly —
// so a staff app's server stops re-declaring op shapes the owning package
// already declares (design §2.3).
//
// renderOpForm(catalogRow, context, mount) mounts the op's fields into `mount`
// and returns { descriptor, submit() }, or null when the op cannot be offered
// from this context at all — never a form that renders but cannot submit.
//
// `catalogRow` is the raw `/api/op-catalog` row shape: `operationType`,
// `presentation`, `inputSchema` (a JSON string), `fieldDescriptions`,
// `dispatch.{class,authContext,targetField,targetType,reads,optionalReads,
// visibleWhen}`, `sensitive`.
//
// `context` is `{ target, me, taskKey, workplace, row, prefill, selfVoice }`
// — `target` is the resolved subject key the caller already knows (a task's
// `scopedTo`, or an explicit entity key for a non-task surface); `taskKey`
// names a task-voice submission; `row`/`prefill` back `{context.<field>}`
// reads and pre-filled values; `me` is the signed-in identity key — never a
// target fallback (see the anti-fallback rule below), but whenever it is
// set, `submit()` auto-pushes it onto `reads` (mirroring Facet's own
// renderer) since a script gating on the caller's own hub commonly needs it
// in state regardless of authContext kind. `selfVoice` gates WHETHER a
// `self`-authContext op actually sends `{target: me}` at all (see
// buildAuthContext) — it is not itself the value sent, and a caller in no
// self-voiced surface at all simply never sets it (undefined is falsy, so
// this defaults closed).
//
// An op whose dispatch carries no `targetField` (a free-choice create, or an
// op with no single pre-existing "entity in view" to derive a subject from —
// e.g. minting a brand-new vertex, or naming two independent existing ones
// as plain fields) needs no `context.target` at all: every schema property
// renders as an ordinary editable field, nothing is auto-filled or excluded,
// and `submit()` writes no target into the payload. `context.target` is
// simply ignored for such an op.
//
// ANTI-FALLBACK RULE (normative): target resolution is `context.target`
// alone — never `context.me` or any other context key substituted for an
// unresolved target. This module's callers resolve `target` themselves
// before calling `renderOpForm`, so its own fallback surface collapses to
// "did the caller give me a target": no `context.target` ⇒ this function
// returns `null` rather than rendering a form that can't submit (the same
// early refusal loftspace's own `openComplete` applies before ever calling
// into this module). This rule has a filed defect behind it, not taste
// (vertical-package-standard.md §8, "the client's identity targetType
// fallback"): a client that substitutes the submitter's own identity for an
// unresolvable identity-typed target, for an operator, writes a walk-in's
// PII onto the operator's own vertex — a create-only mistake with no undo.

// fieldKind detects the rendered control for one schema property. The
// conditions mirror cmd/facet/web/app.js's renderField line-for-line in
// LOGIC (not copy-paste — a fresh implementation) so the three-way drift
// gate (scripts/lint-facet-renderer-drift.go) can find equivalent markers in
// both renderers. entity-ref renders as a plain text input in this module —
// staff apps have no entity-ref picker yet, so the marker only needs to
// exist for drift-gate parity; the resolution source is out of scope here.
function fieldKind(name, schema) {
  if (schema.type === "boolean") return "boolean";
  if (schema.enum) return "enum";
  if ((schema.type === "integer" || schema.type === "number") &&
      (schema["x-format"] === "money" || /Cents$/.test(name))) return "money";
  if (schema.type === "integer" || schema.type === "number") return "number";
  if (schema.type === "string" && schema.format === "date") return "date";
  if (schema.type === "string" && schema.format === "date-time") return "date-time";
  if (schema.type === "string" && schema["x-entityRef"]) return "entity-ref";
  return "text";
}

// prettifyFieldName is the label floor for a schema property with no
// declared title: camelCase/snake_case identifiers read as words with a
// leading capital ("startsAt" → "Starts at").
function prettifyFieldName(name) {
  if (!name) return "";
  const spaced = String(name)
    .replace(/_/g, " ")
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .toLowerCase()
    .trim();
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

// titleCase labels an enum option that declares no enumLabels entry.
function titleCase(s) {
  return String(s)
    .replace(/[_-]+/g, " ")
    .replace(/\w\S*/g, (w) => w.charAt(0).toUpperCase() + w.slice(1).toLowerCase());
}

// dateTimeToRFC3339 converts a <input type=datetime-local> value (no
// timezone) to RFC3339. An unparsable value is returned unchanged — the
// script's own schema validation is the enforcer, not this client.
function dateTimeToRFC3339(v) {
  if (typeof v !== "string" || !v) return v;
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? v : d.toISOString().replace(/\.\d{3}Z$/, "Z");
}

// fieldIdSeq/nextFieldID give every rendered control a page-unique id so its
// label's `for` pairs with it (accessibility) — a monotonic per-module-load
// counter rather than any one app's own DOM-naming convention, since this
// module is mounted by four different apps with no shared id scheme to
// borrow (each app's own hand-built modal, where one exists, uses its own
// prefix tied to its own fields container).
let fieldIdSeq = 0;
function nextFieldID() {
  fieldIdSeq += 1;
  return "descriptorform-field-" + fieldIdSeq;
}

// buildField renders one schema property into a labeled control appended
// under a wrapper `<div>`, and returns `{ name, label, required, container,
// read() }` — `read()` answers the control's current value (`undefined` when
// empty), evaluated lazily at submit time rather than snapshotted at render
// time, so a value the person is still editing is read fresh.
//
// Bounds come from the schema and ONLY from the schema (minimum/maximum/
// integer step) — a hardcoded floor here could only agree with the owning
// script's real guard by luck.
//
// `sensitive` masks a plain text/entity-ref control as a password input —
// never a date or a number, which are unenterable masked and are not a
// secret from the person typing them (identity-domain descriptors' own note
// on fields like `dob`).
function buildField(name, schema, isRequired, help, prefill, sensitive) {
  const kind = fieldKind(name, schema);
  const label = schema.title || prettifyFieldName(name);
  // fieldDescriptions[name] (the owning package's explicit help copy) wins;
  // the schema's own JSON Schema `description` is the floor under it — every
  // catalog field gets SOME help text when the package supplied either.
  const helpText = help || schema.description || "";
  const prefillVal = prefill ? prefill[name] : undefined;
  const id = nextFieldID();

  const container = document.createElement("div");
  container.className = "field";
  const labelEl = document.createElement("label");
  labelEl.setAttribute("for", id);
  labelEl.textContent = label + (isRequired ? " *" : "");
  container.appendChild(labelEl);

  let control;
  let read;

  if (kind === "boolean") {
    control = document.createElement("input");
    control.type = "checkbox";
    control.checked = prefillVal === true;
    read = () => control.checked;
  } else if (kind === "enum") {
    control = document.createElement("select");
    if (!isRequired) {
      const blank = document.createElement("option");
      blank.value = "";
      blank.textContent = "Choose…";
      control.appendChild(blank);
    }
    for (const opt of schema.enum) {
      const optionEl = document.createElement("option");
      optionEl.value = opt;
      optionEl.textContent = (schema.enumLabels && schema.enumLabels[opt]) || titleCase(opt);
      if (opt === prefillVal) optionEl.selected = true;
      control.appendChild(optionEl);
    }
    read = () => (control.value === "" ? undefined : control.value);
  } else if (kind === "money") {
    control = document.createElement("input");
    control.type = "number";
    control.step = "0.01";
    if (prefillVal !== undefined && prefillVal !== null) control.value = String(prefillVal / 100);
    read = () => {
      if (control.value === "" || control.value === undefined || control.value === null) return undefined;
      const dollars = Number(control.value);
      if (Number.isNaN(dollars)) throw new Error(label + " must be a valid number.");
      return Math.round(dollars * 100);
    };
  } else if (kind === "number") {
    control = document.createElement("input");
    control.type = "number";
    if (schema.minimum !== undefined) control.min = String(schema.minimum);
    if (schema.maximum !== undefined) control.max = String(schema.maximum);
    if (schema.type === "integer") control.step = "1";
    if (prefillVal !== undefined && prefillVal !== null) control.value = String(prefillVal);
    read = () => {
      if (control.value === "" || control.value === undefined || control.value === null) return undefined;
      const n = schema.type === "integer" ? parseInt(control.value, 10) : Number(control.value);
      if (Number.isNaN(n)) throw new Error(label + " must be a valid number.");
      return n;
    };
  } else if (kind === "date") {
    control = document.createElement("input");
    control.type = "date";
    if (prefillVal) control.value = prefillVal;
    read = () => (control.value === "" ? undefined : control.value);
  } else if (kind === "date-time") {
    control = document.createElement("input");
    control.type = "datetime-local";
    if (prefillVal) control.value = prefillVal;
    read = () => (control.value === "" ? undefined : dateTimeToRFC3339(control.value));
  } else {
    control = document.createElement("input");
    control.type = sensitive ? "password" : "text";
    if (control.type === "password") control.autocomplete = "off";
    // entity-ref carries its declared vertex TYPE onto the element even
    // though it renders identically to a plain text input in this
    // increment (no picker yet) — the attribute is what a later picker
    // hooks onto, and what tells the two kinds apart at all (Facet's own
    // `data-entity-ref-type` convention, app.js:2512).
    if (kind === "entity-ref") control.setAttribute("data-entity-ref-type", String(schema["x-entityRef"]));
    if (prefillVal !== undefined && prefillVal !== null) control.value = String(prefillVal);
    // Trimmed: a free-typed field's whitespace-only value is not a value —
    // required-field validation must see it as empty, not as a string that
    // happens to satisfy "non-empty" on a technicality.
    read = () => {
      const v = control.value.trim();
      return v === "" ? undefined : v;
    };
  }
  control.id = id;
  control.name = name;
  control.required = isRequired;
  container.appendChild(control);

  if (helpText) {
    const helpEl = document.createElement("div");
    helpEl.className = "field-help";
    helpEl.textContent = helpText;
    container.appendChild(helpEl);
  }

  return { name, label, required: isRequired, container, read };
}

// normalizeCatalogRow turns one raw `/api/op-catalog` row into the shape this
// module renders from, refusing (returning `null`) rather than
// half-rendering: no inputSchema means there is nothing to render, no
// dispatch class means the envelope could not be assembled even if a form
// appeared (`targetField` itself is optional — see the free-choice/no-target
// note above), a declared `visibleWhen` this module ships no evaluator for
// is treated as unmet (no state, no offer — the honest answer to a condition
// a client cannot decide, loftspace `catalogDescriptor`'s own rule), and
// `authContext:"service"` has no source in this module's context shape
// (`{ target, me, taskKey, workplace, row, prefill, selfVoice }` carries no
// service key) — refusing it here means a caller never gets a handle back
// for it, rather than one whose submit() always fails at the Processor.
// canRender (below) is this same refusal, exposed for a caller deciding
// whether to enable an offer before it has a mount to render into.
function normalizeCatalogRow(row) {
  if (!row || !row.inputSchema || !row.dispatch) return null;
  const dispatch = row.dispatch;
  if (!dispatch.class) return null;
  if (dispatch.visibleWhen) return null;
  if (dispatch.authContext === "service") return null;
  let schema;
  try {
    schema = JSON.parse(row.inputSchema);
  } catch (_e) {
    return null;
  }
  if (!schema || typeof schema !== "object") return null;
  return {
    operationType: row.operationType,
    schema,
    dispatch,
    presentation: row.presentation || {},
    fieldDescriptions: row.fieldDescriptions || {},
  };
}

// canRender reports whether a raw `/api/op-catalog` row has enough of the
// descriptor vocabulary for this module to ever offer a form for it — the
// same structural refusal `renderOpForm` applies internally, exposed
// standalone so a caller can decide whether to enable a "Complete"-style
// button before it has resolved the specific context (target, task) a click
// would render against. It cannot check `dispatch.targetType` against a
// resolved target from here — `renderOpForm` still refuses that case once a
// target is known.
export function canRender(catalogRow) {
  return !!normalizeCatalogRow(catalogRow);
}

// shortKeyID reads the trailing dot-segment out of a key — the bare Contract
// #1 NanoID a 6-segment link key needs out of an ordinary `vtx.<type>.<id>`
// value (loftspace `app.js`'s own `shortKey`; this module has no access to
// that function and needs the identical convention for its `:id` modifier
// below, so it carries its own copy of the same one-line rule).
function shortKeyID(key) {
  if (typeof key !== "string" || key === "") return "";
  const i = key.lastIndexOf(".");
  return i >= 0 ? key.slice(i + 1) : key;
}

// keyType reads the Contract #1 vertex type out of a `vtx.<type>.<id>` key,
// or undefined for anything that isn't one (Facet `app.js`'s own `keyType`,
// re-declared here for the same reason shortKeyID is: no access to that
// module's copy).
function keyType(key) {
  return typeof key === "string" && key.startsWith("vtx.") ? key.split(".")[1] : undefined;
}

// substituteTemplate resolves one read template against the assembled
// payload and the caller-supplied context. Five forms: `{me}` / `{actor}`
// (aliases — both read straight off `context.me`) / `{taskKey}` (bare
// tokens), `{payload.<field>}` (the payload just assembled), and
// `{context.<field>}` (a column of the caller's companion row — the staff
// analog of Facet's `{entity.<column>}`, the seam loftspace's hand-built
// SignRenewal/VerifyGuarantor completions need for their composite link-key
// reads, ready for a caller that supplies `context.row`). Any of the five
// may carry a trailing `:id` modifier (`{payload.renewalKey:id}`) to
// substitute the bare NanoID instead of the full key — what makes a
// 6-segment link key expressible as a declared read.
//
// A placeholder this function does not recognize at all — a typo, or a
// vocabulary form this module has not adopted — throws rather than
// resolving to "" and being silently dropped by wholeKey: an unresolved
// VALUE is the normal, tolerated shape of an optional read, but an
// unresolvable TEMPLATE is a descriptor this module cannot honor at all, and
// failing loud here is the only way that surfaces instead of quietly
// declaring fewer reads than the op actually needs.
function substituteTemplate(str, context, payload) {
  if (typeof str !== "string") return str;
  return str.replace(/\{([^}]+)\}/g, (whole, rawExpr) => {
    const bare = rawExpr.endsWith(":id");
    const expr = bare ? rawExpr.slice(0, -3) : rawExpr;
    let value;
    if (expr === "me" || expr === "actor") {
      value = context.me;
    } else if (expr === "taskKey") {
      value = context.taskKey;
    } else if (expr.startsWith("payload.")) {
      value = payload[expr.slice("payload.".length)];
    } else if (expr.startsWith("context.")) {
      value = context.row ? context.row[expr.slice("context.".length)] : undefined;
    } else {
      throw new Error("descriptorform: unrecognized read template " + whole);
    }
    if (value === undefined || value === null || value === "") return "";
    return bare ? shortKeyID(String(value)) : String(value);
  });
}

// wholeKey accepts a substituted read declaration only when every
// dot-separated segment survived. A placeholder that resolved to "" leaves a
// hole — a leading ".someAspect", a trailing "vtx.identity.ABC.", an
// interior ".." — and the remainder is not a shorter key, it is a different
// and invalid one that NATS rejects with ErrInvalidKey rather than
// ErrKeyNotFound. Declaring nothing is always the safe reading of "this key
// did not resolve".
function wholeKey(k) {
  return typeof k === "string" && k !== "" && !k.includes("{") && !k.split(".").includes("");
}

// substituteTemplates maps a declared read-template list through
// substituteTemplate and drops anything that isn't a whole key, deduping the
// survivors.
function substituteTemplates(templates, context, payload) {
  const out = [];
  for (const template of templates || []) {
    const key = substituteTemplate(template, context, payload);
    if (wholeKey(key) && !out.includes(key)) out.push(key);
  }
  return out;
}

// buildAuthContext assembles the envelope's authContext per the op's
// declared dispatch.authContext — the exact leg a Contract #10 grant path
// checks: "task" rides the task's own ephemeral grant (throws when
// context.taskKey is missing — a task-voice op reached without a real task
// has no grant to submit under, the loftspace `desc.taskLeg && !task.taskKey`
// refusal, now enforced inside this module since it owns authContext
// assembly), "self" rides a scope=self grant checked as authContext.target
// == actor, and "standing"/anything else sends no authContext object at all
// (a standing role grant needs none — spelled out rather than left to an
// undefined fallthrough, because sending nothing IS the correct submission).
//
// "self" additionally requires context.selfVoice: a caller that never opted
// into self-voice submits with NO authContext, exactly as if the op were
// standing — mirroring loftspace's pre-migration `landlordSubmit()`, which
// sent `{authContext:{target: state.applicant}}` only when `isLandlord()`
// and `undefined` otherwise. This is NOT redundant with the platform's own
// step3 matcher: an actor's PlatformPermissions doc can legitimately carry
// BOTH a scope=any row (a broad, target-independent grant) and a scope=self
// row for the same operationType, and the matcher authorizes on the FIRST
// row that succeeds, in doc order — so sending a target that trivially
// equals the actor (context.me is always the signed-in caller's own key)
// would make the scope=self row succeed and WIN over a later scope=any row,
// which then routes the op through the platform-VALIDATED self path and
// switches on any per-op ownership guard keyed to it (e.g. lease-signing's
// `require_manages`, gated on exactly `op.authTargetValidated &&
// op.authContextTarget == op.actor`) — a stricter check than the caller's
// actual (broader) grant would have required. Only a caller that KNOWS it is
// submitting in a self-voiced surface (e.g. this app's landlord hat) should
// ever cause that switch to flip.
function buildAuthContext(kind, context) {
  if (kind === "task") {
    if (!context.taskKey) throw new Error("This action can only be taken from its task.");
    return { task: context.taskKey, target: context.target };
  }
  if (kind === "self") return context.selfVoice ? { target: context.me } : undefined;
  return undefined;
}

export function renderOpForm(catalogRow, context, mount) {
  const normalized = normalizeCatalogRow(catalogRow);
  if (!normalized) return null;

  const { schema, dispatch, presentation, fieldDescriptions, operationType } = normalized;
  const targetField = dispatch.targetField;

  // A targetField-less op (free-choice create, or an op naming two
  // independent existing entities as plain fields) has no subject to
  // resolve at all, so context.target is never required and the type check
  // below has nothing to check against. Every other op needs a resolved,
  // correctly-typed target before it can render.
  if (targetField) {
    if (!context || !context.target) return null;
    // A wrong-typed target is a real hazard, not a formality: a caller could
    // hand this a key it resolved for a DIFFERENT purpose (a task's own key
    // where the op wants its subject's, say), and submitting it would name
    // the wrong vertex under real authority. Facet's own resolveTargetKey
    // applies exactly this check for exactly this reason.
    if (dispatch.targetType && keyType(context.target) !== dispatch.targetType) return null;
  }
  // A targetField-less op may be called with no context at all (nothing to
  // resolve) — default it so the prefill/reads/authContext reads below never
  // dereference a null.
  context = context || {};

  const props = schema.properties || {};
  const required = new Set(schema.required || []);
  // The target field stays out of the form: it is filled from `context.target`,
  // never typed. Filtering by `undefined` (no targetField) excludes nothing —
  // every schema property renders.
  const fieldNames = Object.keys(props).filter((name) => name !== targetField);

  const fields = fieldNames.map((name) =>
    buildField(name, props[name] || {}, required.has(name), fieldDescriptions[name], context.prefill, !!catalogRow.sensitive));

  mount.innerHTML = "";
  for (const f of fields) mount.appendChild(f.container);

  return {
    descriptor: {
      title: presentation.title || operationType,
      submitLabel: presentation.submitLabel || "Submit",
      sensitive: !!catalogRow.sensitive,
      targetField,
      targetType: dispatch.targetType || "",
    },
    submit() {
      const payload = {};
      // Written FIRST, before any read template is substituted: a declared
      // read of the form `{payload.<targetField>}.suffix` can only resolve
      // once the target is in the payload it names (mirrors Facet
      // app.js:2748-2754 / loftspace app.js:2313). Skipped entirely for a
      // targetField-less op — there is no key to write it under, and every
      // field below is a plain typed value instead.
      if (targetField) payload[targetField] = context.target;

      for (const f of fields) {
        const value = f.read();
        if (value === undefined) {
          if (f.required) throw new Error(f.label + " is required.");
          continue;
        }
        payload[f.name] = value;
      }

      const reads = substituteTemplates(dispatch.reads, context, payload);
      const optionalReads = substituteTemplates(dispatch.optionalReads, context, payload);

      // Two Facet-side fallbacks this module mirrors (design §2.2), pushed
      // AFTER template substitution so they land alongside whatever the
      // descriptor already declared rather than replacing it: a script that
      // gates on its own target (vertex_alive/class_of on `state[target]`)
      // needs that key in state even when the owning package's own Dispatch
      // forgot to declare it as a Read — Facet's own renderer has always
      // auto-pushed it (app.js:2790-2797) and never depended on every
      // package getting the declaration right; a targetField-less op has no
      // such key to push. The signed-in caller's own identity is pushed the
      // same way (app.js:2798-2804) — a standing-guard script that checks
      // `op.actor` against a link touching the caller's own hub (e.g. an
      // own-binding probe) commonly needs the actor's key in state too, and
      // `context.me` is cheap to have on hand whenever it's set. Both are
      // idempotent against a template that already produced the same key.
      if (targetField && payload[targetField] && !reads.includes(payload[targetField])) {
        reads.push(payload[targetField]);
      }
      if (context.me && !reads.includes(context.me)) reads.push(context.me);

      return {
        operationType,
        class: dispatch.class,
        payload,
        reads,
        optionalReads,
        authContext: buildAuthContext(dispatch.authContext, context),
      };
    },
  };
}
