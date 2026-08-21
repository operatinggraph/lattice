// Regression vectors for internal/descriptorform/form.mjs
// (staff-descriptor-rendering-design.md §13 Inc 2). form.mjs is a real ES
// module (it `export`s renderOpForm), so this harness diverges from the
// cmd/facet/web `.test.mjs` idiom (`vm.runInContext` over a plain script):
// Node's test runner loads ESM natively, so a dynamic `import()` after
// installing a minimal fake `document` on the global is simpler than
// `vm.SourceTextModule` and needs no `--experimental-vm-modules` flag. The
// fake DOM below implements only what form.mjs actually calls
// (createElement/appendChild/the handful of element properties it sets) —
// there is no DOM library in this repo (no package.json, no jsdom) to reach
// for instead.

import { test } from "node:test";
import assert from "node:assert/strict";

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
  }
  appendChild(child) {
    this.children.push(child);
    return child;
  }
  setAttribute(k, v) {
    this.attrs[k] = v;
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
};

const { renderOpForm, canRender } = await import("./form.mjs");

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

test("renderOpForm renders each schema shape as its own control kind", () => {
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

test("rendered fields carry the .field/.field-help CSS hooks and a paired label[for]/input[id]", () => {
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

test("renderOpForm refuses to render without a resolved context.target", () => {
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
test("renderOpForm refuses a target whose vertex type doesn't match dispatch.targetType", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) }); // dispatch.targetType: "renewal"
  const WRONG_TYPE = "vtx.identity.EEEEEEEEEEEEEEEEEEEE";
  assert.equal(renderOpForm(row, { target: WRONG_TYPE }, new FakeElement("div")), null);
  assert.ok(renderOpForm(row, { target: TARGET }, new FakeElement("div")), "the matching type still renders");
});

// ---- normalization refusals (item 1's catalogDescriptor-equivalent) ----

test("renderOpForm refuses a row with no inputSchema, no dispatch class, or a visibleWhen it can't evaluate", () => {
  const schema = { type: "object", properties: {}, required: [] };
  const ctx = { target: TARGET };

  assert.equal(renderOpForm(baseRow({ inputSchema: "" }), ctx, new FakeElement("div")), null);

  const noClass = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { targetField: "renewalKey" },
  });
  assert.equal(renderOpForm(noClass, ctx, new FakeElement("div")), null);

  const gated = baseRow({ inputSchema: JSON.stringify(schema) });
  gated.dispatch.visibleWhen = { field: "active", equals: true };
  assert.equal(renderOpForm(gated, ctx, new FakeElement("div")), null, "an unevaluated visibleWhen fails closed");

  const serviceVoice = baseRow({ inputSchema: JSON.stringify(schema) });
  serviceVoice.dispatch.authContext = "service";
  assert.equal(renderOpForm(serviceVoice, ctx, new FakeElement("div")), null,
    "this module's context shape carries no service key to build authContext:service from");
});

// ---- canRender: the same structural refusal, before there is a mount ----

test("canRender agrees with renderOpForm's own refusal, without needing a context or a mount", () => {
  const schema = { type: "object", properties: {}, required: [] };

  assert.equal(canRender(baseRow({ inputSchema: JSON.stringify(schema) })), true);
  assert.equal(canRender(baseRow({ inputSchema: "" })), false, "no schema to render");

  const noClass = baseRow({ inputSchema: JSON.stringify(schema), dispatch: { targetField: "renewalKey" } });
  assert.equal(canRender(noClass), false, "no dispatch class ⇒ no envelope could ever be assembled");

  const gated = baseRow({ inputSchema: JSON.stringify(schema) });
  gated.dispatch.visibleWhen = { field: "active", equals: true };
  assert.equal(canRender(gated), false, "an unevaluated visibleWhen offers nothing, same as renderOpForm");

  const serviceVoice = baseRow({ inputSchema: JSON.stringify(schema) });
  serviceVoice.dispatch.authContext = "service";
  assert.equal(canRender(serviceVoice), false);
});

// ---- envelope assembly per authContext kind ----

test("submit() assembles authContext:task and throws when the task leg has no taskKey", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.authContext = "task";

  const withTask = renderOpForm(row, { target: TARGET, taskKey: "manifest.task.x" }, new FakeElement("div"));
  const envelope = withTask.submit();
  assert.deepEqual(envelope.authContext, { task: "manifest.task.x", target: TARGET });

  const withoutTask = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  assert.throws(() => withoutTask.submit(), /task/);
});

test("submit() assembles authContext:self from context.me when selfVoice is set, never from context.target", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.authContext = "self";
  const ME = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";

  const handle = renderOpForm(row, { target: TARGET, me: ME, selfVoice: true }, new FakeElement("div"));
  assert.deepEqual(handle.submit().authContext, { target: ME });
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
test("submit() sends no authContext for a self-authContext op when the caller never opted into self-voice", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.authContext = "self";
  const ME = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";

  const noFlag = renderOpForm(row, { target: TARGET, me: ME }, new FakeElement("div"));
  assert.equal(noFlag.submit().authContext, undefined, "selfVoice absent ⇒ no authContext, exactly like standing");

  const flaggedFalse = renderOpForm(row, { target: TARGET, me: ME, selfVoice: false }, new FakeElement("div"));
  assert.equal(flaggedFalse.submit().authContext, undefined);
});

test("submit() sends no authContext at all for a standing-authContext op", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) }); // authContext: "standing"
  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  assert.equal(handle.submit().authContext, undefined);
});

// ---- wholeKey drop + write-before-substitute order ----

test("submit() drops a read whose template left an unresolved segment, and resolves one built off the just-written target", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{payload.renewalKey}.terms", "{payload.missingField}.suffix"];

  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  const envelope = handle.submit();
  // TARGET itself trails the declared read: the targetField fallback (below)
  // pushes it too, and it is a distinct string from TARGET + ".terms".
  assert.deepEqual(envelope.reads, [TARGET + ".terms", TARGET],
    "the target written into payload BEFORE substitution makes {payload.<targetField>}.<suffix> resolve, " +
    "and the unresolvable template is dropped rather than sent as a hole");
});

test("submit() drops an optionalRead the same way", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.optionalReads = ["{payload.missingField}.guarantorVerification"];

  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  assert.deepEqual(handle.submit().optionalReads, []);
});

// ---- required-field validation ----

test("submit() throws for a missing required field and never for an unset optional one", () => {
  const schema = {
    type: "object",
    properties: { renewalKey: { type: "string" }, note: { type: "string" }, method: { type: "string" } },
    required: ["note"],
  };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, { target: TARGET }, mount);
  assert.throws(() => handle.submit(), /Note is required/);

  controlByName(mount, "note").value = "   ";
  assert.throws(() => handle.submit(), /Note is required/, "whitespace-only is not a value");

  controlByName(mount, "note").value = "renewed by phone";
  const envelope = handle.submit();
  assert.equal(envelope.payload.note, "renewed by phone");
  assert.equal("method" in envelope.payload, false, "an empty optional field is omitted, not sent as \"\"");
});

// ---- {context.<field>} — the staff analog of Facet's {entity.<column>} ----

test("a {context.<field>} read resolves against the caller's companion row", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{context.leaseApp}"];

  const handle = renderOpForm(row, { target: TARGET, row: { leaseApp: "vtx.leaseapp.CCCCCCCCCCCCCCCCCCCC" } }, new FakeElement("div"));
  // TARGET trails: the targetField fallback pushes it too, since it never
  // appeared in the declared {context.leaseApp} read.
  assert.deepEqual(handle.submit().reads, ["vtx.leaseapp.CCCCCCCCCCCCCCCCCCCC", TARGET]);
});

// ---- {actor} alias, the `:id` bare-NanoID modifier, and unrecognized
// templates — the exact composite-link-key shape loftspace's SignRenewal/
// VerifyGuarantor completions need (VerifyGuarantor's real read is
// `lnk.renewal.{payload.renewalKey:id}.renews.leaseapp.{payload.leaseApp:id}`),
// so a later increment can adopt the template form with no module change. ----

test("{actor} is an alias for {me}", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{actor}"];
  const ME = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";

  const handle = renderOpForm(row, { target: TARGET, me: ME }, new FakeElement("div"));
  // TARGET trails: the targetField fallback pushes it (not yet in reads);
  // the context.me fallback then no-ops since ME is already present via
  // {actor}'s own resolution.
  assert.deepEqual(handle.submit().reads, [ME, TARGET]);
});

test("the :id modifier substitutes the bare NanoID, composing into a 6-segment link key", () => {
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
  assert.deepEqual(handle.submit().reads,
    ["lnk.renewal.AAAAAAAAAAAAAAAAAAAA.renews.leaseapp.CCCCCCCCCCCCCCCCCCCC", TARGET]);
});

test("{me:id} and {taskKey:id} also take the modifier", () => {
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
  assert.deepEqual(handle.submit().reads,
    ["BBBBBBBBBBBBBBBBBBBB", "DDDDDDDDDDDDDDDDDDDD", TARGET, "vtx.identity.BBBBBBBBBBBBBBBBBBBB"]);
});

test("an unrecognized read template throws instead of silently dropping", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{entity.leaseApp}"]; // Facet's vocabulary, not this module's

  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  assert.throws(() => handle.submit(), /unrecognized read template/);
});

// ---- targetField-less ops (free-choice create / no single subject) ----

test("a row with no dispatch.targetField renders every schema property as a field, with no context.target required", () => {
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

test("submit() on a targetField-less op never writes an undefined-keyed entry into the payload", () => {
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
  const envelope = handle.submit();
  assert.deepEqual(envelope.payload, { fullName: "Dr. Sam Okafor" });
  assert.equal("undefined" in envelope.payload, false);
});

// Regression guard: a targetField-BEARING row must still refuse without a
// resolved context.target — this must NOT have changed by loosening the
// targetField-less case above.
test("a targetField-bearing row still refuses to render without a resolved context.target", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) }); // dispatch.targetField: "renewalKey"
  assert.equal(renderOpForm(row, {}, new FakeElement("div")), null);
  assert.equal(renderOpForm(row, undefined, new FakeElement("div")), null);
});

// ---- numeric coercion ----

test("a money field submits cents, and a non-numeric value throws rather than serializing NaN", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" }, rentCents: { type: "integer" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  const mount = new FakeElement("div");
  const handle = renderOpForm(row, { target: TARGET }, mount);

  controlByName(mount, "rentCents").value = "12.50";
  assert.equal(handle.submit().payload.rentCents, 1250);

  controlByName(mount, "rentCents").value = "not-a-number";
  assert.throws(() => handle.submit(), /valid number/);
});

// ---- targetField / context.me read fallbacks (mirrors cmd/facet/web/app.js
// :2790-2804 — a script gating on its own target or the caller's own hub can
// need either key in `state` even when the owning package's Dispatch forgot
// to declare it; this module must not depend on every package getting that
// declaration right, the same way Facet's own renderer never has) ----

test("submit() auto-pushes the resolved targetField value onto reads when the descriptor didn't declare it", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) }); // dispatch.reads: [] — nothing declared
  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  assert.deepEqual(handle.submit().reads, [TARGET],
    "a script's vertex_alive/class_of check on its own target needs the target key in state " +
    "regardless of whether the descriptor declared it");
});

test("submit() does not duplicate the targetField push when a declared read already resolved to the same key", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = ["{payload.renewalKey}"]; // already declares it
  const handle = renderOpForm(row, { target: TARGET }, new FakeElement("div"));
  assert.deepEqual(handle.submit().reads, [TARGET], "no duplicate entry for the same key");
});

test("submit() auto-pushes context.me onto reads when set and not already declared", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  row.dispatch.reads = []; // no targetField push either would occur without a real targetField...
  row.dispatch.targetField = undefined; // ...isolate the context.me push specifically
  row.dispatch.targetType = undefined;
  const me = "vtx.identity.BBBBBBBBBBBBBBBBBBBB";
  const handle = renderOpForm(row, { me }, new FakeElement("div"));
  assert.deepEqual(handle.submit().reads, [me]);
});

test("submit() does not duplicate context.me when it's already the resolved target", () => {
  const schema = { type: "object", properties: { renewalKey: { type: "string" } }, required: [] };
  const row = baseRow({ inputSchema: JSON.stringify(schema) });
  const handle = renderOpForm(row, { target: TARGET, me: TARGET }, new FakeElement("div"));
  assert.deepEqual(handle.submit().reads, [TARGET], "the targetField push and the context.me push must not both add the same key");
});

test("submit() pushes neither fallback when targetField is absent and context.me is unset", () => {
  const schema = { type: "object", properties: { fullName: { type: "string" } }, required: [] };
  const row = baseRow({
    inputSchema: JSON.stringify(schema),
    dispatch: { class: "provider", authContext: "standing", reads: [], optionalReads: [] },
  });
  const handle = renderOpForm(row, {}, new FakeElement("div"));
  assert.deepEqual(handle.submit().reads, []);
});
