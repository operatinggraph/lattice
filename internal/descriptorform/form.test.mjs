// Regression vectors for internal/descriptorform/form.mjs
// (staff-descriptor-rendering-design.md §13 Inc 2). form.mjs is a real ES
// module (it `export`s renderOpForm), so this harness diverges from the
// cmd/facet/web `.test.mjs` idiom (`vm.runInContext` over a plain script):
// Node's test runner loads ESM natively, so a dynamic `import()` after
// installing a minimal fake `document` on the global is simpler than
// `vm.SourceTextModule` and needs no `--experimental-vm-modules` flag. The
// fake DOM below implements only what form.mjs actually calls
// (createElement/appendChild/removeChild/the handful of element properties it
// sets, plus a `document.body` for showCeremonyReveal to mount into) — there
// is no DOM library in this repo (no package.json, no jsdom) to reach for
// instead.
//
// WebCrypto is NOT faked: Node's own `crypto`/`TextEncoder` globals are the
// same WebCrypto surface a browser gives form.mjs, so the ceremony vectors
// mint and hash for real and the sha256(plaintext) == submitted-hash
// invariant is checked against node:crypto rather than against a stub that
// could agree with a wrong implementation. The unsupported-runtime vector
// deletes the global for the length of one assertion instead.

import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";

class FakeElement {
  constructor(tag) {
    this.tagName = tag;
    this.children = [];
    this.value = "";
    this.checked = false;
    this.name = "";
    this.required = false;
    this.textContent = "";
    this.className = "";
    this.attrs = {};
    this.hidden = false;
    this.style = {};
    this.parentNode = null;
    this._listeners = {};
  }
  appendChild(child) {
    this.children.push(child);
    child.parentNode = this;
    return child;
  }
  removeChild(child) {
    this.children = this.children.filter((c) => c !== child);
    child.parentNode = null;
    return child;
  }
  setAttribute(k, v) {
    this.attrs[k] = v;
  }
  // addEventListener/fire: the fake DOM's stand-in for a real dispatched
  // event — form.mjs only ever adds a listener and expects it to run on the
  // next input/change, so a plain callback list plus a synchronous `fire`
  // the tests call directly is enough; there is no real event loop to
  // simulate.
  addEventListener(type, fn) {
    (this._listeners[type] = this._listeners[type] || []).push(fn);
  }
  fire(type) {
    for (const fn of this._listeners[type] || []) fn();
  }
  set innerHTML(v) {
    this._innerHTML = v;
    this.children = [];
  }
  get innerHTML() {
    return this._innerHTML || "";
  }
}

globalThis.document = {
  createElement(tag) {
    return new FakeElement(tag);
  },
  body: new FakeElement("body"),
};

const { renderOpForm, canRender, showCeremonyReveal, revealCeremonySecret } =
  await import("./form.mjs");

// controlByName finds the rendered control for one field name inside a mount
// — each field is a wrapper <div> whose children are [label, control, help?].
function controlByName(mount, name) {
  for (const wrapper of mount.children) {
    const found = wrapper.children.find((c) => c.name === name);
    if (found) return found;
  }
  return undefined;
}

const TARGET = "vtx.renewal.AAAAAAAAAAAAAAAAAAAA";

function baseRow(overrides) {
  return Object.assign(
    {
      operationType: "TestOp",
      presentation: { title: "Test Op", submitLabel: "Go" },
      fieldDescriptions: {},
      dispatch: {
        class: "testclass",
        authContext: "standing",
        targetField: "renewalKey",
        targetType: "renewal",
        reads: [],
        optionalReads: [],
      },
      sensitive: false,
    },
    overrides,
  );
}

// ---- schema -> field-kind table (6 kinds) ----

test("renderOpForm renders each schema shape as its own control kind", async () => {
  const schema = {
    type: "object",
    properties: {
      renewalKey: { type: "string" }, // the dispatch target: excluded from the form
      isActive: { type: "boolean" },
      status: { type: "string", enum: ["draft", "signed"] },
      amountCents: { type: "integer" },
      startsOn: { type: "string", format: "date" },
      startsAt: { type: "string", format: "date-time" },
      providerKey: { type: "string", "x-entityRef": "provider" },
    },
    required: [],
  };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, { target: TARGET }, mount);
  assert.ok(handle, "a valid catalog row + resolved target must render");

  assert.equal(controlByName(mount, "renewalKey"), undefined, "the target field is never rendered");

  const isActive = controlByName(mount, "isActive");
  assert.equal(isActive.tagName, "input");
  assert.equal(isActive.type, "checkbox");

  const status = controlByName(mount, "status");
  assert.equal(status.tagName, "select");

  const amount = controlByName(mount, "amountCents");
  assert.equal(amount.tagName, "input");
  assert.equal(amount.type, "number");
  assert.equal(amount.step, "0.01", "a *Cents field renders as money, not a bare integer");

  const startsOn = controlByName(mount, "startsOn");
  assert.equal(startsOn.type, "date");

  const startsAt = controlByName(mount, "startsAt");
  assert.equal(startsAt.type, "datetime-local");

  const provider = controlByName(mount, "providerKey");
  assert.equal(provider.type, "text", "entity-ref has no picker yet — a plain text input");
  assert.equal(provider.attrs["data-entity-ref-type"], "provider",
    "entity-ref is still DETECTED and carries its declared vertex type, even without a picker yet — " +
    "deleting the entity-ref branch entirely must fail this, not just the .type assertion above");
});

// A string over the 120-char maxLength threshold renders as a textarea
// (mirrors cmd/facet/web/app.js:2515-2516); at or under it, or with no
// maxLength at all, it stays a plain text input.
test("a string field's maxLength decides textarea vs plain text", async () => {
  const schema = {
    type: "object",
    properties: {
      summary: { type: "string", maxLength: 4000 },
      note: { type: "string", maxLength: 120 },
      untyped: { type: "string" },
    },
    required: [],
  };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { class: "testclass", authContext: "standing", reads: [], optionalReads: [] },
  });
  const mount = new FakeElement("div");
  renderOpForm(row, {}, mount);

  const summary = controlByName(mount, "summary");
  assert.equal(summary.tagName, "textarea", "over 120 renders as textarea");
  assert.equal(summary.maxLength, 4000);

  assert.equal(controlByName(mount, "note").tagName, "input", "exactly 120 stays a plain input");
  assert.equal(controlByName(mount, "untyped").tagName, "input", "no maxLength stays a plain input");
});

test("a textarea's whitespace-only value reads as undefined, same as a text field", async () => {
  const schema = { type: "object", properties: { notes: { type: "string", maxLength: 4000 } }, required: [] };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { class: "testclass", authContext: "standing", reads: [], optionalReads: [] },
  });
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, {}, mount);
  controlByName(mount, "notes").value = "   ";
  assert.equal((await handle.submit()).envelope.payload.notes, undefined);
});

// ---- per-field conditional visibility (x-visibleWhen) ----

function visibleWhenRow(overrides) {
  const schema = {
    type: "object",
    properties: {
      followUpRequested: { type: "boolean" },
      followUpDate: {
        type: "string",
        format: "date",
        "x-visibleWhen": { field: "followUpRequested", equals: true },
      },
    },
    required: [],
  };
  return baseRow(Object.assign(
    {
      inputSchema: JSON.stringify(schema),
      dispatch: { class: "testclass", authContext: "standing", reads: [], optionalReads: [] },
    },
    overrides,
  ));
}

test("x-visibleWhen starts hidden/shown to match the sibling's initial value, and tracks it live", async () => {
  const mount = new FakeElement("div");
  renderOpForm(visibleWhenRow(), {}, mount);
  const toggle = controlByName(mount, "followUpRequested");
  // The date control's wrapper <div class="field"> is the mount's direct
  // child carrying it — find it the same way controlByName walks children.
  const wrapper = mount.children.find((w) => w.children.some((c) => c.name === "followUpDate"));

  assert.equal(wrapper.hidden, true, "unchecked at render: the dependent field starts hidden");

  toggle.checked = true;
  toggle.fire("change");
  assert.equal(wrapper.hidden, false, "checking the sibling reveals the dependent field");

  toggle.checked = false;
  toggle.fire("change");
  assert.equal(wrapper.hidden, true, "unchecking hides it again");
});

test("submit() drops a hidden x-visibleWhen field and never fails its required check while hidden", async () => {
  const schema = {
    type: "object",
    properties: {
      followUpRequested: { type: "boolean" },
      followUpDate: {
        type: "string",
        format: "date",
        "x-visibleWhen": { field: "followUpRequested", equals: true },
      },
    },
    required: ["followUpDate"], // required, but exempt while its visibleWhen is unmet
  };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { class: "testclass", authContext: "standing", reads: [], optionalReads: [] },
  });
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, {}, mount);

  const envelope = (await handle.submit()).envelope;
  assert.equal("followUpDate" in envelope.payload, false, "a hidden field contributes nothing to the payload");

  const toggle = controlByName(mount, "followUpRequested");
  toggle.checked = true;
  toggle.fire("change");
  await assert.rejects(() => handle.submit(), /Follow up date is required/,
    "revealed and still required — now it must fail its own required check like any other field");

  controlByName(mount, "followUpDate").value = "2026-09-01";
  assert.equal((await handle.submit()).envelope.payload.followUpDate, "2026-09-01");
});

test("normalizeCatalogRow refuses a dangling x-visibleWhen sibling reference", async () => {
  const row = visibleWhenRow();
  const schema = JSON.parse(row.inputSchema);
  schema.properties.followUpDate["x-visibleWhen"].field = "noSuchField";
  row.inputSchema = JSON.stringify(schema);
  assert.equal(renderOpForm(row, {}, new FakeElement("div")), null);
  assert.equal(canRender(row), false);
});

test("normalizeCatalogRow refuses chained x-visibleWhen (a conditional field naming another conditional field)", async () => {
  const schema = {
    type: "object",
    properties: {
      a: { type: "boolean" },
      b: { type: "boolean", "x-visibleWhen": { field: "a", equals: true } },
      c: { type: "string", "x-visibleWhen": { field: "b", equals: true } },
    },
    required: [],
  };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { class: "testclass", authContext: "standing", reads: [], optionalReads: [] },
  });
  assert.equal(renderOpForm(row, {}, new FakeElement("div")), null, "b is itself conditional — c chaining off it is refused");
  assert.equal(canRender(row), false);
});

test("rendered fields carry the .field/.field-help CSS hooks and a paired label[for]/input[id]", async () => {
  const schema = {
    type: "object",
    properties: { renewalKey: { type: "string" }, method: { type: "string" } },
    required: [],
  };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    fieldDescriptions: { method: "how the guarantor was verified" },
  });
  const mount = new FakeElement("div");
  renderOpForm(row, { target: TARGET }, mount);

  assert.equal(mount.children.length, 1);
  const wrapper = mount.children[0];
  assert.equal(wrapper.className, "field");
  const [labelEl, control, helpEl] = wrapper.children;
  assert.equal(labelEl.tagName, "label");
  assert.equal(helpEl.className, "field-help");
  assert.equal(helpEl.textContent, "how the guarantor was verified");
  assert.ok(control.id, "the control needs an id for its label to pair with");
  assert.equal(labelEl.attrs.for, control.id, "label[for] must name the control's own id, not a fixed string");
});

// ---- unresolvable-target refusal ----

test("renderOpForm refuses to render without a resolved context.target", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  assert.equal(renderOpForm(row, {}, new FakeElement("div")), null);
  assert.equal(renderOpForm(row, { target: "" }, new FakeElement("div")), null);
  assert.equal(renderOpForm(row, undefined, new FakeElement("div")), null);
  // The sharp anti-fallback vector: `me` IS present, but target is not — a
  // module willing to fall back to context.me here is exactly the old
  // client-identity-fallback defect (vertical-package-standard.md §8) this
  // rule exists to forbid. `me` being populated must not matter at all.
  assert.equal(
    renderOpForm(row, { me: "vtx.identity.EEEEEEEEEEEEEEEEEEEE", target: undefined }, new FakeElement("div")),
    null,
    "context.me must never substitute for a missing target",
  );
});

// A target that resolves to a DIFFERENT vertex type than the op declares is
// not a value to submit under real authority — the caller may have handed
// this a key it resolved for some other purpose. Mirrors Facet's own
// resolveTargetKey type check.
test("renderOpForm refuses a target whose vertex type doesn't match dispatch.targetType", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) }); // dispatch.targetType: "renewal"
  const WRONG_TYPE = "vtx.identity.EEEEEEEEEEEEEEEEEEEE";
  assert.equal(renderOpForm(row, { target: WRONG_TYPE }, new FakeElement("div")), null);
  assert.ok(renderOpForm(row, { target: TARGET }, new FakeElement("div")), "the matching type still renders");
});

// ---- normalization refusals (item 1's catalogDescriptor-equivalent) ----

test("renderOpForm refuses a row with no inputSchema, no dispatch class, or an authContext:service voice", async () => {
  const schema = { type: "object", properties: {}, required: [] };
  const ctx = { target: TARGET };

  assert.equal(renderOpForm(baseRow({ inputSchema: "" }), ctx, new FakeElement("div")), null);

  const noClass = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { targetField: "renewalKey" },
  });
  assert.equal(renderOpForm(noClass, ctx, new FakeElement("div")), null);

  const serviceVoice = baseRow({ inputSchema: JSON.stringify(schema) });
  serviceVoice.dispatch.authContext = "service";
  assert.equal(renderOpForm(serviceVoice, ctx, new FakeElement("div")), null,
    "this module's context shape carries no service key to build authContext:service from");
});

// ---- dispatch.visibleWhen (item 5) — evaluated against context.row ----

function visibleWhenOpRow() {
  const schema = { type: "object", properties: {}, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.visibleWhen = { field: "series_status", equals: "active" };
  return row;
}

test("renderOpForm offers a visibleWhen-gated op when context.row's column matches", async () => {
  const handle = renderOpForm(visibleWhenOpRow(), { target: TARGET, row: { series_status: "active" } }, new FakeElement("div"));
  assert.ok(handle, "the row's column matches equals, so the op renders");
});

test("renderOpForm refuses a visibleWhen-gated op when context.row's column doesn't match, is absent, or the row itself is absent", async () => {
  const mismatched = renderOpForm(visibleWhenOpRow(), { target: TARGET, row: { series_status: "paused" } }, new FakeElement("div"));
  assert.equal(mismatched, null, "wrong value ⇒ no offer");

  const noColumn = renderOpForm(visibleWhenOpRow(), { target: TARGET, row: {} }, new FakeElement("div"));
  assert.equal(noColumn, null, "row exists but lacks the named column ⇒ no state, no offer");

  const noRow = renderOpForm(visibleWhenOpRow(), { target: TARGET }, new FakeElement("div"));
  assert.equal(noRow, null, "no row at all ⇒ nothing to evaluate against, fails closed");
});

test("renderOpForm's visibleWhen check uses strict equality, not truthiness", async () => {
  const row = visibleWhenOpRow();
  row.dispatch.visibleWhen = { field: "count", equals: 1 };
  const handle = renderOpForm(row, { target: TARGET, row: { count: "1" } }, new FakeElement("div"));
  assert.equal(handle, null, "a string \"1\" must not satisfy equals: 1");
});

// A "type":"array" property has no fieldKind case: left unguarded it would
// fall through to a plain text input and submit a STRING where the script
// expects a list — a silent wrong-shaped write the person submitting it has
// no way to notice. Refusing the whole row is the honest answer until a real
// array kind ships (fieldKind has no way to omit just the one bad field and
// still assemble a submittable envelope).
test("renderOpForm refuses a row with an array-typed schema property", async () => {
  const schema = {
    type: "object",
    properties: { renewalKey: { type: "string" }, windows: { type: "array", items: { type: "object" } } },
    required: [],
  };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  assert.equal(renderOpForm(row, { target: TARGET }, new FakeElement("div")), null);
  assert.equal(canRender(row), false);
});

// ---- canRender: the same structural refusal, before there is a mount ----

test("canRender agrees with renderOpForm's own refusal, without needing a context or a mount", async () => {
  const schema = { type: "object", properties: {}, required: [] };

  assert.equal(canRender(baseRow({ inputSchema: JSON.stringify(schema) })), true);
  assert.equal(canRender(baseRow({ inputSchema: "" })), false, "no schema to render");

  const noClass = baseRow({ inputSchema: JSON.stringify(schema), dispatch: { targetField: "renewalKey" } });
  assert.equal(canRender(noClass), false, "no dispatch class ⇒ no envelope could ever be assembled");

  // Same split as dispatch.targetType (already untestable here for the same
  // reason): canRender has no context.row to evaluate visibleWhen against,
  // so — like targetType — it defers, and renderOpForm applies the real,
  // fail-closed check once a row is known (see the visibleWhen tests above).
  const gated = baseRow({ inputSchema: JSON.stringify(schema) });
  gated.dispatch.visibleWhen = { field: "active", equals: true };
  assert.equal(canRender(gated), true, "visibleWhen is deferred to renderOpForm, which has context.row");

  const serviceVoice = baseRow({ inputSchema: JSON.stringify(schema) });
  serviceVoice.dispatch.authContext = "service";
  assert.equal(canRender(serviceVoice), false);
});

// ---- envelope assembly per authContext kind ----

test("submit() assembles authContext:task and throws when the task leg has no taskKey", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.authContext = "task";

  const withTask = renderOpForm(row, { target: TARGET, taskKey: "manifest.task.x" }, new FakeElement("div"));
  const envelope = (await withTask.submit()).envelope;
  assert.deepEqual(envelope.authContext, { task: "manifest.task.x", target: TARGET });

  const withoutTask = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  await assert.rejects(() => withoutTask.submit(), /task/);
});

test("submit() assembles authContext:self from context.me when selfVoice is set, never from context.target", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.authContext = "self";
  const ME = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";

  const handle = renderOpForm(row, { target: TARGET, me: ME, selfVoice: true }, new FakeElement("div"));
  assert.deepEqual((await handle.submit()).envelope.authContext, { target: ME });
});

// A caller that never opts into self-voice gets NO authContext at all for a
// self-authContext op — the actor's platform grants may carry a scope=any
// row alongside a scope=self one for the same operationType, and the
// Processor authorizes on the FIRST matching row (Contract #6's platform
// matcher). Sending {target: me} unconditionally would make the scope=self
// row succeed trivially (context.me always equals the actor) and WIN,
// routing the op through the platform-validated self path and switching on
// any per-op ownership guard keyed to it — stricter than the caller's actual
// (broader) grant required. Mirrors loftspace's pre-migration
// landlordSubmit(), which sent the self authContext only when isLandlord().
test("submit() sends no authContext for a self-authContext op when the caller never opted into self-voice", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.authContext = "self";
  const ME = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";

  const noFlag = renderOpForm(row, { target: TARGET, me: ME }, new FakeElement("div"));
  assert.equal((await noFlag.submit()).envelope.authContext, undefined, "selfVoice absent ⇒ no authContext, exactly like standing");

  const flaggedFalse = renderOpForm(row, { target: TARGET, me: ME, selfVoice: false }, new FakeElement("div"));
  assert.equal((await flaggedFalse.submit()).envelope.authContext, undefined);
});

test("submit() sends no authContext at all for a standing-authContext op", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) }); // authContext: "standing"
  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  assert.equal((await handle.submit()).envelope.authContext, undefined);
});

// ---- wholeKey drop + write-before-substitute order ----

test("submit() drops a read whose template left an unresolved segment, and resolves one built off the just-written target", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{payload.renewalKey}.terms", "{payload.missingField}.suffix"];

  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  const envelope = (await handle.submit()).envelope;
  // TARGET itself trails the declared read: the targetField fallback (below)
  // pushes it too, and it is a distinct string from TARGET + ".terms".
  assert.deepEqual(envelope.reads, [TARGET + ".terms", TARGET],
    "the target written into payload BEFORE substitution makes {payload.<targetField>}.<suffix> resolve, " +
    "and the unresolvable template is dropped rather than sent as a hole");
});

test("submit() drops an optionalRead the same way", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.optionalReads = ["{payload.missingField}.guarantorVerification"];

  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  assert.deepEqual((await handle.submit()).envelope.optionalReads, []);
});

// ---- dispatch.enumerations -> envelope.enumerations ----

test("submit() resolves a declared enumeration's {actor} hub and passes relation/direction through verbatim", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.enumerations = [{ hub: "{actor}", relation: "holdsRole", direction: "out" }];
  const ME = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";

  const handle = renderOpForm(row, { target: TARGET, me: ME }, new FakeElement("div"));
  const envelope = (await handle.submit()).envelope;
  assert.deepEqual(envelope.enumerations, [{ hub: ME, relation: "holdsRole", direction: "out" }]);
});

test("submit() drops an enumeration whose hub template does not resolve to a whole key", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.enumerations = [{ hub: "{payload.missingField}", relation: "holdsRole", direction: "out" }];

  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  const envelope = (await handle.submit()).envelope;
  assert.equal("enumerations" in envelope, false, "an unresolvable hub drops the entry, leaving no enumerations on the envelope");
});

test("submit() sends one enumeration when two declarations resolve to the same walk", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.enumerations = [
    { hub: "{actor}", relation: "holdsRole", direction: "out" },
    { hub: "{me}", relation: "holdsRole", direction: "out" },
  ];
  const ME = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";

  const handle = renderOpForm(row, { target: TARGET, me: ME }, new FakeElement("div"));
  const envelope = (await handle.submit()).envelope;
  assert.deepEqual(envelope.enumerations, [{ hub: ME, relation: "holdsRole", direction: "out" }],
    "two spellings of one hub resolve to one walk, and one walk is sent once");
});

test("submit() drops an enumeration missing relation or direction", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.enumerations = [
    { hub: "{actor}", direction: "out" },
    { hub: "{actor}", relation: "holdsRole" },
  ];
  const ME = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";

  const handle = renderOpForm(row, { target: TARGET, me: ME }, new FakeElement("div"));
  const envelope = (await handle.submit()).envelope;
  assert.equal("enumerations" in envelope, false, "an entry missing relation or direction is never sent on the wire");
});

test("submit() sends no enumerations field when the descriptor declares none", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) }); // dispatch.enumerations unset

  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  const envelope = (await handle.submit()).envelope;
  assert.equal("enumerations" in envelope, false);
});

// ---- required-field validation ----

test("submit() throws for a missing required field and never for an unset optional one", async () => {
  const schema = {
    type: "object",
    properties: { renewalKey: { type: "string" }, note: { type: "string" }, method: { type: "string" } },
    required: ["note"],
  };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, { target: TARGET }, mount);
  await assert.rejects(() => handle.submit(), /Note is required/);

  controlByName(mount, "note").value = "   ";
  await assert.rejects(() => handle.submit(), /Note is required/, "whitespace-only is not a value");

  controlByName(mount, "note").value = "renewed by phone";
  const envelope = (await handle.submit()).envelope;
  assert.equal(envelope.payload.note, "renewed by phone");
  assert.equal("method" in envelope.payload, false, "an empty optional field is omitted, not sent as \"\"");
});

// ---- {context.<field>} — the staff analog of Facet's {entity.<column>} ----

test("a {context.<field>} read resolves against the caller's companion row", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{context.leaseApp}"];

  const handle = renderOpForm(row, { target: TARGET, row: { leaseApp: "vtx.leaseapp.CCCCCCCCCCCCCCCCCCCC" } }, new FakeElement("div"));
  // TARGET trails: the targetField fallback pushes it too, since it never
  // appeared in the declared {context.leaseApp} read.
  assert.deepEqual((await handle.submit()).envelope.reads, ["vtx.leaseapp.CCCCCCCCCCCCCCCCCCCC", TARGET]);
});

test("{entity.<field>} is accepted as an alias for {context.<field>} against the same companion row", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{entity.leaseApp}"];

  const handle = renderOpForm(row, { target: TARGET, row: { leaseApp: "vtx.leaseapp.CCCCCCCCCCCCCCCCCCCC" } }, new FakeElement("div"));
  assert.deepEqual((await handle.submit()).envelope.reads, ["vtx.leaseapp.CCCCCCCCCCCCCCCCCCCC", TARGET]);
});

// ---- dispatch.contextParams — a field the CLIENT fills and never renders ----

const LEASE_APP = "vtx.leaseapp.CCCCCCCCCCCCCCCCCCCC";
const TENANT = "vtx.identity.DDDDDDDDDDDDDDDDDDDD";

// lease-signing's SignRenewal, whole: renewalKey is the task's subject,
// leaseApp/applicant are the renewal row's own facts, and the person sees a
// single confirm button.
function signRenewalRow() {
  const schema = {
    type: "object",
    properties: { renewalKey: { type: "string" } },
    required: ["renewalKey"],
  };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.authContext = "task";
  row.dispatch.contextParams = { leaseApp: "{context.leaseApp}", applicant: "{context.tenant}" };
  row.dispatch.reads = [
    "{payload.renewalKey}",
    "lnk.renewal.{payload.renewalKey:id}.renews.leaseapp.{payload.leaseApp:id}",
    "lnk.leaseapp.{payload.leaseApp:id}.applicationFor.identity.{payload.applicant:id}",
  ];
  return row;
}

test("a contextParams field is filled from context and never rendered", async () => {
  const schema = {
    type: "object",
    properties: { renewalKey: { type: "string" }, leaseApp: { type: "string" }, note: { type: "string" } },
    required: ["renewalKey", "leaseApp"],
  };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.contextParams = { leaseApp: "{context.leaseApp}" };

  const mount = new FakeElement("div");
  const handle = renderOpForm(row, { target: TARGET, row: { leaseApp: LEASE_APP } }, mount);
  assert.equal(controlByName(mount, "leaseApp"), undefined,
    "a contextParams field is excluded from the form the same way the target field is");
  assert.ok(controlByName(mount, "note"), "an ordinary field still renders");

  const envelope = (await handle.submit()).envelope;
  assert.equal(envelope.payload.leaseApp, LEASE_APP,
    "the descriptor said where the value comes from, so submit() fills it");
});

test("contextParams are filled BEFORE the read templates that name them", async () => {
  const handle = renderOpForm(signRenewalRow(), {
    target: TARGET,
    taskKey: "manifest.task.x",
    row: { leaseApp: LEASE_APP, tenant: TENANT },
  }, new FakeElement("div"));
  const envelope = (await handle.submit()).envelope;

  assert.deepEqual(envelope.payload, { renewalKey: TARGET, leaseApp: LEASE_APP, applicant: TENANT });
  assert.deepEqual(envelope.reads, [
    TARGET,
    "lnk.renewal.AAAAAAAAAAAAAAAAAAAA.renews.leaseapp.CCCCCCCCCCCCCCCCCCCC",
    "lnk.leaseapp.CCCCCCCCCCCCCCCCCCCC.applicationFor.identity.DDDDDDDDDDDDDDDDDDDD",
  ], "both 6-segment link reads resolve, which they only can if the params landed in the payload first");
  assert.deepEqual(envelope.authContext, { task: "manifest.task.x", target: TARGET });
});

// A contextParams field has no control to fail required-field validation on —
// the person never saw it — so an unresolvable template has to refuse here or
// the op reaches the Processor with an empty key it can only reject, with
// nothing in the UI to explain why.
test("submit() refuses when a contextParams template resolves to nothing", async () => {
  const handle = renderOpForm(signRenewalRow(), {
    target: TARGET,
    taskKey: "manifest.task.x",
    row: { leaseApp: LEASE_APP }, // no tenant column
  }, new FakeElement("div"));
  await assert.rejects(() => handle.submit(), /applicant/);
});

test("submit() refuses a contextParams template with no companion row at all", async () => {
  const handle = renderOpForm(signRenewalRow(), {
    target: TARGET,
    taskKey: "manifest.task.x",
    row: null,
  }, new FakeElement("div"));
  await assert.rejects(() => handle.submit(), /leaseApp/);
});

// The `?` OPTIONAL marker (definition.go's OpDispatchSpec doc) is real
// descriptor vocabulary this module adopts: an optional contextParams
// template that does not resolve is silently OMITTED from the payload — no
// throw, no rendered field — and the op still submits. Contrast with the
// required-template refusal test above (no `?`): same missing companion-row
// column, opposite outcome, which is the whole point of the marker.
test("an optional `?` contextParams template that doesn't resolve is silently omitted, not refused", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.contextParams = { leaseApp: "{context.leaseApp?}" };

  const handle = renderOpForm(row, { target: TARGET, row: {} }, new FakeElement("div"));
  const envelope = (await handle.submit()).envelope;
  assert.equal("leaseApp" in envelope.payload, false, "the field is absent, not sent as \"\"");
});

// ---- {me.<type>} — the typed self-anchor (definition.go's OpDispatchSpec
// doc, cmd/facet/web/app.js's selfAnchorKey) ----

const LEASE_APP_ANCHOR = "vtx.leaseapp.EEEEEEEEEEEEEEEEEEEE";

test("{me.<type>} resolves to the single matching context.selfAnchors entry and fills a contextParams field", async () => {
  const schema = {
    type: "object",
    properties: { renewalKey: { type: "string" }, leaseAppKey: { type: "string" } },
    required: ["renewalKey"],
  };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.contextParams = { leaseAppKey: "{me.leaseapp}" };

  const mount = new FakeElement("div");
  const handle = renderOpForm(row, {
    target: TARGET,
    selfAnchors: [{ type: "leaseapp", key: LEASE_APP_ANCHOR }],
  }, mount);
  assert.equal(controlByName(mount, "leaseAppKey"), undefined,
    "a contextParams field is excluded from the form the same way any other one is");

  const envelope = (await handle.submit()).envelope;
  assert.equal(envelope.payload.leaseAppKey, LEASE_APP_ANCHOR);
});

test("a required {me.<type>} throws the could-not-fill error when zero or several anchors match", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" }, leaseAppKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.contextParams = { leaseAppKey: "{me.leaseapp}" };

  const zero = renderOpForm(row, { target: TARGET, selfAnchors: [] }, new FakeElement("div"));
  await assert.rejects(() => zero.submit(), /leaseAppKey/, "no matching anchor is not a value to guess at");

  const ambiguous = renderOpForm(row, {
    target: TARGET,
    selfAnchors: [
      { type: "leaseapp", key: LEASE_APP_ANCHOR },
      { type: "leaseapp", key: "vtx.leaseapp.FFFFFFFFFFFFFFFFFFFF" },
    ],
  }, new FakeElement("div"));
  await assert.rejects(() => ambiguous.submit(), /leaseAppKey/, "two matches is exactly as ungoverned as zero");
});

test("an optional {me.<type>?} silently omits the field on zero or several matches, and fills it on exactly one", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" }, leaseAppKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.contextParams = { leaseAppKey: "{me.leaseapp?}" };

  const zero = renderOpForm(row, { target: TARGET, selfAnchors: [] }, new FakeElement("div"));
  const zeroEnvelope = (await zero.submit()).envelope;
  assert.equal("leaseAppKey" in zeroEnvelope.payload, false, "no match ⇒ silently omitted, never thrown");

  const several = renderOpForm(row, {
    target: TARGET,
    selfAnchors: [
      { type: "leaseapp", key: LEASE_APP_ANCHOR },
      { type: "leaseapp", key: "vtx.leaseapp.FFFFFFFFFFFFFFFFFFFF" },
    ],
  }, new FakeElement("div"));
  const severalEnvelope = (await several.submit()).envelope;
  assert.equal("leaseAppKey" in severalEnvelope.payload, false, "ambiguous ⇒ silently omitted, never thrown");

  const one = renderOpForm(row, {
    target: TARGET,
    selfAnchors: [{ type: "leaseapp", key: LEASE_APP_ANCHOR }],
  }, new FakeElement("div"));
  const oneEnvelope = (await one.submit()).envelope;
  assert.equal(oneEnvelope.payload.leaseAppKey, LEASE_APP_ANCHOR, "exactly one match still fills the field normally");
});

test("{me.<type>:id} composes: resolves the self-anchor's key, then substitutes the bare NanoID", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.contextParams = { leaseAppId: "{me.leaseapp:id}" };

  const handle = renderOpForm(row, {
    target: TARGET,
    selfAnchors: [{ type: "leaseapp", key: LEASE_APP_ANCHOR }],
  }, new FakeElement("div"));
  const envelope = (await handle.submit()).envelope;
  assert.equal(envelope.payload.leaseAppId, "EEEEEEEEEEEEEEEEEEEE",
    "the bare id, not the full vtx.leaseapp.<id> key");
});

test("an op declaring no contextParams renders and submits exactly as before", async () => {
  const schema = {
    type: "object",
    properties: { renewalKey: { type: "string" }, note: { type: "string" } },
    required: [],
  };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, { target: TARGET }, mount);
  assert.ok(controlByName(mount, "note"));
  assert.deepEqual((await handle.submit()).envelope.payload, { renewalKey: TARGET });
});

// ---- {actor} alias, the `:id` bare-NanoID modifier, and unrecognized
// templates — the exact composite-link-key shape loftspace's SignRenewal/
// VerifyGuarantor completions declare (VerifyGuarantor's real read is
// `lnk.renewal.{payload.renewalKey:id}.renews.leaseapp.{payload.leaseApp:id}`). ----

test("{actor} is an alias for {me}", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{actor}"];
  const ME = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";

  const handle = renderOpForm(row, { target: TARGET, me: ME }, new FakeElement("div"));
  // TARGET trails: the targetField fallback pushes it (not yet in reads);
  // the context.me fallback then no-ops since ME is already present via
  // {actor}'s own resolution.
  assert.deepEqual((await handle.submit()).envelope.reads, [ME, TARGET]);
});

test("the :id modifier substitutes the bare NanoID, composing into a 6-segment link key", async () => {
  const schema = {
    type: "object",
    properties: { renewalKey: { type: "string" }, leaseApp: { type: "string" } },
    required: [],
  };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["lnk.renewal.{payload.renewalKey:id}.renews.leaseapp.{payload.leaseApp:id}"];

  const mount = new FakeElement("div");
  const handle = renderOpForm(row, { target: TARGET }, mount);
  controlByName(mount, "leaseApp").value = "vtx.leaseapp.CCCCCCCCCCCCCCCCCCCC";
  // TARGET trails: the targetField fallback pushes it too, since the
  // composite link key above never resolves to the bare TARGET string.
  assert.deepEqual((await handle.submit()).envelope.reads,
    ["lnk.renewal.AAAAAAAAAAAAAAAAAAAA.renews.leaseapp.CCCCCCCCCCCCCCCCCCCC", TARGET]);
});

test("{me:id} and {taskKey:id} also take the modifier", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{me:id}", "{taskKey:id}"];

  const handle = renderOpForm(row, {
    target: TARGET,
    me: "vtx.identity.BBBBBBBBBBBBBBBBBBBB",
    taskKey: "manifest.task.DDDDDDDDDDDDDDDDDDDD",
  }, new FakeElement("div"));
  // TARGET and the full context.me key both trail: the :id modifier's bare
  // NanoIDs above are distinct strings from the FULL keys the two fallbacks
  // push, so both land, in fallback order (targetField, then context.me).
  assert.deepEqual((await handle.submit()).envelope.reads,
    ["BBBBBBBBBBBBBBBBBBBB", "DDDDDDDDDDDDDDDDDDDD", TARGET, "vtx.identity.BBBBBBBBBBBBBBBBBBBB"]);
});

test("an unrecognized read template throws instead of silently dropping", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{scopedTo}"]; // not a form this module recognizes

  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  await assert.rejects(() => handle.submit(), /unrecognized read template/);
});

// ---- targetField-less ops (free-choice create / no single subject) ----

test("a row with no dispatch.targetField renders every schema property as a field, with no context.target required", async () => {
  const schema = {
    type: "object",
    properties: { fullName: { type: "string" }, specialty: { type: "string" } },
    required: ["fullName"],
  };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { class: "provider", authContext: "standing", reads: [], optionalReads: [] },
  });
  const mount = new FakeElement("div");

  assert.equal(canRender(row), true, "targetField is no longer required to exist, only dispatch.class is");

  const handle = renderOpForm(row, {}, mount);
  assert.ok(handle, "no context.target needed when the op declares no targetField");
  assert.ok(controlByName(mount, "fullName"), "every schema property renders — nothing is auto-filled or excluded");
  assert.ok(controlByName(mount, "specialty"));

  const handleNoContext = renderOpForm(row, undefined, new FakeElement("div"));
  assert.ok(handleNoContext, "a targetField-less op renders even with no context object at all");
});

test("submit() on a targetField-less op never writes an undefined-keyed entry into the payload", async () => {
  const schema = {
    type: "object",
    properties: { fullName: { type: "string" } },
    required: ["fullName"],
  };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { class: "provider", authContext: "standing", reads: [], optionalReads: [] },
  });
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, {}, mount);
  controlByName(mount, "fullName").value = "Dr. Sam Okafor";
  const envelope = (await handle.submit()).envelope;
  assert.deepEqual(envelope.payload, { fullName: "Dr. Sam Okafor" });
  assert.equal("undefined" in envelope.payload, false);
});

// Regression guard: a targetField-BEARING row must still refuse without a
// resolved context.target — this must NOT have changed by loosening the
// targetField-less case above.
test("a targetField-bearing row still refuses to render without a resolved context.target", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) }); // dispatch.targetField: "renewalKey"
  assert.equal(renderOpForm(row, {}, new FakeElement("div")), null);
  assert.equal(renderOpForm(row, undefined, new FakeElement("div")), null);
});

// ---- numeric coercion ----

test("a money field submits cents, and a non-numeric value throws rather than serializing NaN", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" }, rentCents: { type: "integer" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, { target: TARGET }, mount);

  controlByName(mount, "rentCents").value = "12.50";
  assert.equal((await handle.submit()).envelope.payload.rentCents, 1250);

  controlByName(mount, "rentCents").value = "not-a-number";
  await assert.rejects(() => handle.submit(), /valid number/);
});

// ---- dispatch.classChoices — no single static class (Fire: CreateLocation) ----

test("a row with classChoices and no class renders the choice select and is not refused", async () => {
  const schema = { type: "object", properties: { name: { type: "string" } }, required: [] };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: {
      classChoices: ["unit", "building", "property"],
      authContext: "standing",
      reads: [],
      optionalReads: [],
    },
  });
  const mount = new FakeElement("div");

  assert.equal(canRender(row), true, "classChoices alone satisfies the class-resolution requirement");

  const handle = renderOpForm(row, {}, mount);
  assert.ok(handle, "classChoices with no dispatch.class must still render");

  const choiceControl = controlByName(mount, "__classChoice");
  assert.ok(choiceControl, "the synthetic class-choice select must render");
  assert.equal(choiceControl.tagName, "select");
  assert.equal(mount.children[0].children[1], choiceControl,
    "the class-choice field renders AHEAD of the schema-driven fields");
  assert.ok(controlByName(mount, "name"), "the ordinary schema field still renders alongside it");
});

test("submit() sends the picked classChoice as the envelope's class, and never renders it into payload", async () => {
  const schema = { type: "object", properties: { name: { type: "string" } }, required: [] };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: {
      classChoices: ["unit", "building", "property"],
      authContext: "standing",
      reads: [],
      optionalReads: [],
    },
  });
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, {}, mount);

  controlByName(mount, "__classChoice").value = "building";
  controlByName(mount, "name").value = "West Tower";
  const envelope = (await handle.submit()).envelope;
  assert.equal(envelope.class, "building");
  assert.equal("__classChoice" in envelope.payload, false,
    "the synthetic class-choice control is never a schema field, so it never lands in payload");
  assert.equal(envelope.payload.name, "West Tower");
});

test("submit() throws when no classChoice was picked, exactly like any other required field", async () => {
  const schema = { type: "object", properties: {}, required: [] };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: {
      classChoices: ["unit", "building", "property"],
      authContext: "standing",
      reads: [],
      optionalReads: [],
    },
  });
  const handle = renderOpForm(row, {}, new FakeElement("div"));
  await assert.rejects(() => handle.submit(), /Type is required/);
});

// Regression pin for the normalizeCatalogRow refusal change: a row with
// NEITHER class NOR classChoices must still be refused exactly as before —
// loosening the line-253 check to accommodate classChoices must not also
// loosen it for the genuinely classless case.
test("a row with neither dispatch.class nor dispatch.classChoices is still refused", async () => {
  const schema = { type: "object", properties: {}, required: [] };
  const ctx = {};

  const neitherNoKey = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { authContext: "standing", reads: [], optionalReads: [] },
  });
  assert.equal(renderOpForm(neitherNoKey, ctx, new FakeElement("div")), null);
  assert.equal(canRender(neitherNoKey), false);

  const emptyChoices = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { classChoices: [], authContext: "standing", reads: [], optionalReads: [] },
  });
  assert.equal(renderOpForm(emptyChoices, ctx, new FakeElement("div")), null,
    "an empty classChoices array is not a real choice set");
  assert.equal(canRender(emptyChoices), false);
});

// ---- targetField / context.me read fallbacks (mirrors cmd/facet/web/app.js
// :2790-2804 — a script gating on its own target or the caller's own hub can
// need either key in `state` even when the owning package's Dispatch forgot
// to declare it; this module must not depend on every package getting that
// declaration right, the same way Facet's own renderer never has) ----

test("submit() auto-pushes the resolved targetField value onto reads when the descriptor didn't declare it", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) }); // dispatch.reads: [] — nothing declared
  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  assert.deepEqual((await handle.submit()).envelope.reads, [TARGET],
    "a script's vertex_alive/class_of check on its own target needs the target key in state " +
    "regardless of whether the descriptor declared it");
});

test("submit() does not duplicate the targetField push when a declared read already resolved to the same key", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{payload.renewalKey}"]; // already declares it
  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  assert.deepEqual((await handle.submit()).envelope.reads, [TARGET], "no duplicate entry for the same key");
});

test("submit() auto-pushes context.me onto reads when set and not already declared", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = []; // no targetField push either would occur without a real targetField...
  row.dispatch.targetField = undefined; // ...isolate the context.me push specifically
  row.dispatch.targetType = undefined;
  const me = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";
  const handle = renderOpForm(row, { me }, new FakeElement("div"));
  assert.deepEqual((await handle.submit()).envelope.reads, [me]);
});

test("submit() does not duplicate context.me when it's already the resolved target", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  const handle = renderOpForm(row, { target: TARGET, me: TARGET }, new FakeElement("div"));
  assert.deepEqual((await handle.submit()).envelope.reads, [TARGET], "the targetField push and the context.me push must not both add the same key");
});

test("submit() pushes neither fallback when targetField is absent and context.me is unset", async () => {
  const schema = { type: "object", properties: { fullName: { type: "string" } }, required: [] };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { class: "provider", authContext: "standing", reads: [], optionalReads: [] },
  });
  const handle = renderOpForm(row, {}, new FakeElement("div"));
  assert.deepEqual((await handle.submit()).envelope.reads, []);
});

// ---- the mint-and-reveal ceremony (pkgmgr.OpCeremonySpec) ----
//
// Two ops declare one today (identity-domain's CreateUnclaimedIdentity and
// RotateClaimKey): the client mints a secret Lattice must never learn,
// submits only its hash, and shows the plaintext exactly once, only after
// the write lands.

const CEREMONY_SCHEMA = {
  type: "object",
  properties: {
    name: { type: "string" },
    claimKeyHash: { type: "string" },
  },
  required: ["name", "claimKeyHash"],
};

// ceremonyRow is a catalog row shaped like CreateUnclaimedIdentity's — no
// targetField (a create mints its own subject), and a ceremony naming the
// schema's hash field.
function ceremonyRow() {
  return baseRow({
    inputSchema: JSON.stringify(CEREMONY_SCHEMA),
    dispatch: { class: "identity", authContext: "standing", reads: [], optionalReads: [] },
    ceremony: {
      mintedSecretHashField: "claimKeyHash",
      revealTitle: "Claim key",
      revealHelp: "Hand this to them — it is never shown again.",
    },
  });
}

// withoutWebCrypto runs fn with globalThis.crypto removed, restoring it
// however fn ends. It is the only way to reach the runtime this module has to
// refuse on — an insecure origin, where crypto.subtle simply does not exist —
// since Node's own global is real and cannot be un-supported any other way.
function withoutWebCrypto(fn) {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, "crypto");
  delete globalThis.crypto;
  try {
    return fn();
  } finally {
    Object.defineProperty(globalThis, "crypto", descriptor);
  }
}

test("an op declaring a ceremony is refused outright when the runtime cannot perform one", () => {
  const row = ceremonyRow();
  // The positive vector runs FIRST, so the refusal below is provably about
  // WebCrypto's absence and not about some unrelated defect in the row.
  assert.equal(canRender(row), true, "the same row is offerable when WebCrypto is present");
  assert.ok(renderOpForm(row, {}, new FakeElement("div")));

  withoutWebCrypto(() => {
    assert.equal(canRender(row), false,
      "no ceremony support ⇒ the op is not offered at all, never offered with the hash field rendered");
    assert.equal(renderOpForm(row, {}, new FakeElement("div")), null);
  });
});

test("the ceremony's hash field is never rendered as a field", () => {
  const mount = new FakeElement("div");
  const handle = renderOpForm(ceremonyRow(), {}, mount);
  assert.equal(controlByName(mount, "claimKeyHash"), undefined,
    "rendering it would ask a person to type a digest whose preimage nobody holds");
  assert.ok(controlByName(mount, "name"), "every other schema property still renders");
});

test("submit() fills the hash field with the sha256 of the plaintext it reveals", async () => {
  const mount = new FakeElement("div");
  const handle = renderOpForm(ceremonyRow(), {}, mount);
  controlByName(mount, "name").value = "Ada Okonkwo";

  const { envelope, reveal } = await handle.submit();
  assert.equal(envelope.payload.name, "Ada Okonkwo");
  assert.ok(reveal, "a ceremony op hands its plaintext back for the caller to reveal");
  assert.equal(reveal.title, "Claim key");
  assert.equal(reveal.help, "Hand this to them — it is never shown again.");
  assert.match(reveal.plaintext, /^[0-9a-f]{64}$/, "32 CSPRNG bytes, hex encoded");

  // THE invariant. The hash submitted must be the hash OF the secret handed
  // to the person: nothing downstream can ever detect a mismatch, since
  // Lattice only ever sees the hash — the write would simply land, arming a
  // target with a secret no one can present.
  assert.equal(envelope.payload.claimKeyHash,
    createHash("sha256").update(reveal.plaintext).digest("hex"));
});

test("each submission of the same form mints its own secret", async () => {
  const mount = new FakeElement("div");
  const handle = renderOpForm(ceremonyRow(), {}, mount);
  controlByName(mount, "name").value = "Ada Okonkwo";
  const first = await handle.submit();
  const second = await handle.submit();
  assert.notEqual(first.reveal.plaintext, second.reveal.plaintext,
    "a re-submitted form must never re-arm the target with a secret already handed out");
  assert.notEqual(first.envelope.payload.claimKeyHash, second.envelope.payload.claimKeyHash);
});

test("submit() answers reveal: null for an op that declares no ceremony", async () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const handle = renderOpForm(baseRow({ inputSchema: JSON.stringify(schema) }), { target: TARGET }, new FakeElement("div"));
  const { envelope, reveal } = await handle.submit();
  assert.equal(reveal, null,
    "one return shape for every op, so no caller has to branch on which kind it holds");
  assert.equal(envelope.operationType, "TestOp");
});

// The projection omits the whole ceremony object unless it names a hash field
// (each app's op_catalog.go), so this row cannot arrive over the wire. The
// vector pins which way the module reads one anyway: a ceremony with no field
// to fill describes nothing to perform, so it is no ceremony — never a
// half-performed one that withholds a field it has no value for.
test("a ceremony naming no hash field describes nothing to perform and is not one", async () => {
  const row = ceremonyRow();
  row.ceremony = { revealTitle: "Claim key" };
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, {}, mount);
  assert.ok(handle);
  assert.ok(controlByName(mount, "claimKeyHash"), "nothing is excluded, because nothing was declared");
  controlByName(mount, "name").value = "Ada Okonkwo";
  controlByName(mount, "claimKeyHash").value = "deadbeef";
  assert.equal((await handle.submit()).reveal, null);
});

test("showCeremonyReveal mounts the plaintext as text, never as markup, and dismisses on click", () => {
  const before = document.body.children.length;
  const overlay = showCeremonyReveal("Claim key", "<b>hand it over</b>", "abc123");
  assert.equal(document.body.children.length, before + 1);

  const panel = overlay.children[0];
  const texts = panel.children.map((c) => c.textContent);
  assert.ok(texts.includes("Claim key"));
  assert.ok(texts.includes("<b>hand it over</b>"),
    "copy goes in through textContent, so markup inside it is shown, never parsed");
  assert.ok(texts.includes("abc123"));

  const dismiss = panel.children[panel.children.length - 1];
  dismiss.fire("click");
  assert.equal(document.body.children.length, before, "dismissing removes it from the DOM");
});

test("showCeremonyReveal omits the help paragraph when the descriptor declared none", () => {
  const overlay = showCeremonyReveal("Claim key", "", "abc123");
  const panel = overlay.children[0];
  assert.deepEqual(panel.children.map((c) => c.tagName), ["h2", "pre", "button"]);
  panel.children[2].fire("click"); // leave document.body as this test found it
});

// ---- ceremony rule 3: reveal only on a CONFIRMED write ----
//
// The invariant these vectors exist for is an ordering one, and the way to get
// it wrong is a NEGATIVE check: `reply.status !== "rejected"` reads as "the
// write was fine" and is not, because two reply shapes are neither accepted
// nor rejected. Contract #2 §2.4's `duplicate` says an earlier submission
// already claimed this requestId — so the envelope carrying THIS secret's hash
// was never applied — and the Gateway answers a Processor reply timeout with a
// status-less HTTP 202 `{requestId}`, which means the write may still commit
// and may not. Revealing on either hands a person a plaintext for a write that
// did not land, which is what the whole ceremony vocabulary exists to prevent.
//
// The gate is exercised through the exported function rather than pinned by
// reading the four staff apps' sources: they each call it and narrate the
// answer, so this is the one place the decision is made for all of them.

// revealed captures the plaintexts a call put on screen, and leaves
// document.body exactly as it found it — every mounted overlay is dismissed
// through its own button, so a withholding vector's assertion that NOTHING
// mounted cannot pass merely because an earlier vector left the body dirty.
function revealed(reveal, reply) {
  const before = document.body.children.length;
  const outcome = revealCeremonySecret(reveal, reply);
  const mounted = document.body.children.slice(before);
  const plaintexts = mounted.map((o) => {
    const panel = o.children[0];
    return panel.children.find((c) => c.tagName === "pre").textContent;
  });
  for (const o of mounted) o.children[0].children[o.children[0].children.length - 1].fire("click");
  assert.equal(document.body.children.length, before);
  return { outcome, plaintexts };
}

const aReveal = { title: "Claim key", help: "Hand it over.", plaintext: "s3cr3t" };

test("a confirmed write is the only reply that reveals the plaintext", () => {
  const { outcome, plaintexts } = revealed(aReveal, { status: "accepted", requestId: "r1" });
  assert.equal(outcome, "shown");
  assert.deepEqual(plaintexts, ["s3cr3t"]);
});

// The table is the point: every entry is a reply an app can actually receive,
// and every one of them EXCEPT "accepted" must withhold. A gate written as
// "not rejected" passes the last vector and fails the first four.
for (const [name, reply] of [
  ["a status-less HTTP 202, the Gateway's reply-timeout fallback", { requestId: "r1" }],
  ["a duplicate — some earlier submission claimed this requestId", { status: "duplicate", requestId: "r1" }],
  ["a reply with an unrecognized status", { status: "pending", requestId: "r1" }],
  ["no reply at all", undefined],
  ["a null reply", null],
  ["a rejection", { status: "rejected", error: { code: "AuthDenied", message: "no" } }],
]) {
  test(`${name} withholds the plaintext`, () => {
    const { outcome, plaintexts } = revealed(aReveal, reply);
    assert.equal(outcome, "withheld",
      "not a confirmed commit ⇒ the secret is dropped, and the caller is told so it can say a fresh one is needed");
    assert.deepEqual(plaintexts, [], "nothing reached the screen");
  });
}

test("an op that minted nothing reveals nothing and says nothing, on any reply", () => {
  for (const reply of [{ status: "accepted" }, { requestId: "r1" }, undefined]) {
    const { outcome, plaintexts } = revealed(null, reply);
    assert.equal(outcome, "none", "no ceremony ⇒ there is no secret to withhold and nothing to narrate");
    assert.deepEqual(plaintexts, []);
  }
});

// End to end over the two halves: submit() mints and hands back, the gate
// decides. A ceremony op whose submission timed out must leave no trace of its
// plaintext on screen even though the hash it derived is already on the wire.
test("a ceremony submission that only got a 202 back shows nothing, hash on the wire or not", async () => {
  const mount = new FakeElement("div");
  const handle = renderOpForm(ceremonyRow(), {}, mount);
  controlByName(mount, "name").value = "Ada Okonkwo";
  const { envelope, reveal } = await handle.submit();

  assert.match(envelope.payload.claimKeyHash, /^[0-9a-f]{64}$/,
    "the hash was assembled and would have been submitted");
  const { outcome, plaintexts } = revealed(reveal, { requestId: "r1" });
  assert.equal(outcome, "withheld");
  assert.deepEqual(plaintexts, []);
});
