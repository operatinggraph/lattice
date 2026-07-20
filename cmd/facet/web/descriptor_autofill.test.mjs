// Regression test for the descriptor form's self-anchored parameters: café
// OpenTab's leaseAppKey (edge-showcase-app-design.md §3.6) is declared
// `{me.leaseapp}` in dispatch.contextParams and resolved from the me-row's
// typed selfAnchors, so the visitor is never asked to paste a raw vertex key
// and never sees the field at all.
//
// Same harness as degraded_render.test.mjs: app.js is a plain browser script,
// so vm.runInContext puts its function declarations on the sandbox global
// (top-level const/let stay lexical). That also means `me` is resolved through
// the global object at call time, so overwriting sandbox.me injects a me-row
// into selfAnchoredKeys/selfAnchorKey/substituteTemplate without a DOM or a
// live feed.

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");

const LEASE_A = "vtx.leaseapp.AAAAAAAAAAAAAAAAAAAA";
const LEASE_C = "vtx.leaseapp.CCCCCCCCCCCCCCCCCCCC";

// loadApp evaluates app.js and points `me()` at the supplied me-row.
function loadApp(meRow) {
  const sandbox = { console, document: { addEventListener() {} } };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  sandbox.me = () => meRow;
  return sandbox;
}

test("selfAnchoredKeys indexes the me-row's declared selfAnchors by type", () => {
  const { selfAnchoredKeys } = loadApp({
    selfAnchors: [
      { type: "leaseapp", key: LEASE_A },
      { type: "appointment", key: "vtx.appointment.BBBBBBBBBBBBBBBBBBBB" },
      { type: "leaseapp", key: null },      // degenerate OPTIONAL MATCH entry
      { type: null, key: LEASE_C },         // no declared type
      { type: "op", key: "manifest.op.x" }, // not a vtx key
    ],
  });
  const idx = selfAnchoredKeys();
  assert.deepEqual([...idx.get("leaseapp")], [LEASE_A]);
  assert.ok(idx.has("appointment"));
  assert.equal(idx.has("op"), false);
});

test("selfAnchorKey resolves only an unambiguous anchor", () => {
  const one = loadApp({ selfAnchors: [{ type: "leaseapp", key: LEASE_A }] });
  assert.equal(one.selfAnchorKey("leaseapp"), LEASE_A);
  assert.equal(one.selfAnchorKey("tab"), undefined); // none of that type

  // Two leases is not a value to guess at — it degrades, never picks one.
  const two = loadApp({
    selfAnchors: [{ type: "leaseapp", key: LEASE_A }, { type: "leaseapp", key: LEASE_C }],
  });
  assert.equal(two.selfAnchorKey("leaseapp"), undefined);

  assert.equal(loadApp(null).selfAnchorKey("leaseapp"), undefined); // no me-row yet
});

test("substituteTemplate fills {me.<type>} from the declared anchor", () => {
  const { substituteTemplate } = loadApp({
    identityKey: "vtx.identity.DDDDDDDDDDDDDDDDDDDD",
    selfAnchors: [{ type: "leaseapp", key: LEASE_A }],
  });
  assert.equal(substituteTemplate("{me.leaseapp}", {}, {}), LEASE_A);
  // An unresolvable anchor yields "" — it never falls back to the actor key,
  // which is how a vtx.identity once reached the Processor as a vtx.session.
  assert.equal(substituteTemplate("{me.tab}", {}, {}), "");
  assert.equal(substituteTemplate("{actor}", {}, {}), "vtx.identity.DDDDDDDDDDDDDDDDDDDD");
});

test("unresolvableSelfAnchor names the missing type, or passes a resolvable op", () => {
  const app = loadApp({ selfAnchors: [{ type: "leaseapp", key: LEASE_A }] });
  const openTab = { dispatchContextParams: JSON.stringify({ leaseAppKey: "{me.leaseapp}" }) };
  assert.equal(app.unresolvableSelfAnchor(openTab), undefined);

  const needsTab = { dispatchContextParams: JSON.stringify({ tabKey: "{me.tab}" }) };
  assert.equal(app.unresolvableSelfAnchor(needsTab), "tab");

  // Non-{me.*} templates are somebody else's vocabulary, never a blocker.
  const booker = { dispatchContextParams: JSON.stringify({ booker: "{actor}" }) };
  assert.equal(app.unresolvableSelfAnchor(booker), undefined);
  assert.equal(app.unresolvableSelfAnchor({}), undefined);
});

test("opButton degrades an op whose {me.<type>} the identity cannot answer", () => {
  const app = loadApp({ selfAnchors: [] });
  const html = app.opButton({
    key: "vtx.meta.EEEEEEEEEEEEEEEEEEEE",
    data: {
      operationType: "OpenTab",
      title: "Open a house tab",
      dispatchClass: "tab",
      dispatchContextParams: JSON.stringify({ leaseAppKey: "{me.leaseapp}" }),
    },
  }, {});
  assert.match(html, /degraded-card/);
  assert.match(html, /Open a house tab/);
  assert.doesNotMatch(html, /data-open-op/); // never offered as a button
});

test("opButton offers the same op once the lease anchor resolves", () => {
  const app = loadApp({ selfAnchors: [{ type: "leaseapp", key: LEASE_A }] });
  const html = app.opButton({
    key: "vtx.meta.EEEEEEEEEEEEEEEEEEEE",
    data: {
      operationType: "OpenTab",
      title: "Open a house tab",
      submitLabel: "Open tab",
      dispatchClass: "tab",
      dispatchContextParams: JSON.stringify({ leaseAppKey: "{me.leaseapp}" }),
    },
  }, {});
  assert.match(html, /data-open-op="vtx\.meta\.EEEEEEEEEEEEEEEEEEEE"/);
  assert.doesNotMatch(html, /degraded-card/);
});

test("a {me.<type>} contextParam is filled into the payload, never rendered", () => {
  const app = loadApp({ selfAnchors: [{ type: "leaseapp", key: LEASE_A }] });
  const properties = { leaseAppKey: { type: "string" } };
  const contextParams = { leaseAppKey: "{me.leaseapp}" };
  // renderDescriptorForm's own field filter: a contextParam field is excluded
  // from the visible form, which is what makes "fills + hides" free here.
  const fieldNames = Object.keys(properties).filter((f) => !(f in contextParams));
  assert.deepEqual(fieldNames, []);

  const payload = {};
  for (const [field, template] of Object.entries(contextParams)) {
    payload[field] = app.substituteTemplate(template, {}, payload);
  }
  assert.equal(payload.leaseAppKey, LEASE_A);
  // dispatch.reads is substituted after contextParams, so {payload.leaseAppKey}
  // still resolves to the lease the ContextHint must declare.
  assert.equal(app.substituteTemplate("{payload.leaseAppKey}", {}, payload), LEASE_A);
});

// ---- dispatch.optionalReads + the `:id` bare-id modifier ----
// A Contract #1 link is six segments of BARE ids, so an ownership probe like
// café OpenTab's applicationFor link cannot be composed from vtx keys alone.

test("`:id` substitutes the bare Contract #1 id, not the vtx key", () => {
  const app = loadApp({
    identityKey: "vtx.identity.DDDDDDDDDDDDDDDDDDDD",
    selfAnchors: [{ type: "leaseapp", key: LEASE_A }],
  });
  assert.equal(app.substituteTemplate("{actor:id}", {}, {}), "DDDDDDDDDDDDDDDDDDDD");
  assert.equal(app.substituteTemplate("{me.leaseapp:id}", {}, {}), "AAAAAAAAAAAAAAAAAAAA");
  assert.equal(app.substituteTemplate("{payload.leaseAppKey:id}", {}, { leaseAppKey: LEASE_A }),
    "AAAAAAAAAAAAAAAAAAAA");
  // An unresolvable anchor stays empty rather than leaking the actor key.
  assert.equal(app.substituteTemplate("{me.tab:id}", {}, {}), "");
});

test("a link-shaped optionalRead composes into a whole 6-segment key", () => {
  const app = loadApp({
    identityKey: "vtx.identity.DDDDDDDDDDDDDDDDDDDD",
    selfAnchors: [{ type: "leaseapp", key: LEASE_A }],
  });
  const tmpl = "lnk.leaseapp.{payload.leaseAppKey:id}.applicationFor.identity.{actor:id}";
  assert.equal(app.substituteTemplate(tmpl, {}, { leaseAppKey: LEASE_A }),
    "lnk.leaseapp.AAAAAAAAAAAAAAAAAAAA.applicationFor.identity.DDDDDDDDDDDDDDDDDDDD");
  // The guard aspect: an ordinary 4-segment aspect key off the same payload.
  assert.equal(app.substituteTemplate("{payload.leaseAppKey}.cafeOpenTab", {}, { leaseAppKey: LEASE_A }),
    LEASE_A + ".cafeOpenTab");
});

test("an optionalRead that failed to substitute is dropped, never declared", () => {
  // submitDescriptorForm's filter: an unresolved anchor leaves a hole, and a
  // half-built key names nothing — declaring it would make the script's
  // absent-branch look deliberate when the client simply couldn't answer.
  const app = loadApp({ identityKey: "vtx.identity.DDDDDDDDDDDDDDDDDDDD", selfAnchors: [] });
  const tmpl = "lnk.leaseapp.{me.leaseapp:id}.applicationFor.identity.{actor:id}";
  const built = app.substituteTemplate(tmpl, {}, {});
  assert.equal(built, "lnk.leaseapp..applicationFor.identity.DDDDDDDDDDDDDDDDDDDD");
  const kept = [built].filter((k) => k && !k.includes("{") && !k.includes(".."));
  assert.deepEqual(kept, []);
});

// ---- {entity.<column>} — the viewed manifest.ent row as a fill source ----
// Wellness CancelBooking's `session` must be the booking's ACTUAL forSession
// target, so it is read off the booking row the visitor opened (the
// `sessionKey` column edgeEntityBookings projects), never typed.

const BOOKING = "vtx.booking.FFFFFFFFFFFFFFFFFFFF";
const SESSION = "vtx.session.GGGGGGGGGGGGGGGGGGGG";

// loadAppWithEntities points both `me()` and `entities()` at fixtures — same
// global-object override trick the harness note above describes.
function loadAppWithEntities(meRow, ents) {
  const app = loadApp(meRow);
  app.entities = () => ents;
  return app;
}

const bookingRow = {
  key: "manifest.ent." + BOOKING,
  data: { entityKey: BOOKING, entityType: "booking", title: "Vinyasa Flow", sessionKey: SESSION },
};

test("{entity.<column>} resolves a projected column of the viewed row", () => {
  const app = loadAppWithEntities({ identityKey: "vtx.identity.DDDDDDDDDDDDDDDDDDDD" }, [bookingRow]);
  const ctx = { entityKey: BOOKING };
  assert.equal(app.entityColumn(ctx, "sessionKey"), SESSION);
  assert.equal(app.substituteTemplate("{entity.sessionKey}", ctx, {}), SESSION);
  // And it composes into the ownership probe the op declares.
  const payload = { bookingKey: BOOKING, session: app.substituteTemplate("{entity.sessionKey}", ctx, {}) };
  assert.equal(
    app.substituteTemplate("lnk.booking.{payload.bookingKey:id}.forSession.session.{payload.session:id}", ctx, payload),
    "lnk.booking.FFFFFFFFFFFFFFFFFFFF.forSession.session.GGGGGGGGGGGGGGGGGGGG",
  );
});

test("{entity.<column>:id} substitutes the bare Contract #1 id", () => {
  const app = loadAppWithEntities({ identityKey: "vtx.identity.DDDDDDDDDDDDDDDDDDDD" }, [bookingRow]);
  assert.equal(app.substituteTemplate("{entity.sessionKey:id}", { entityKey: BOOKING }, {}),
    "GGGGGGGGGGGGGGGGGGGG");
});

test("a column the viewed row doesn't carry is unresolvable, never empty", () => {
  const app = loadAppWithEntities({ identityKey: "vtx.identity.DDDDDDDDDDDDDDDDDDDD" }, [
    { key: "manifest.ent." + BOOKING, data: { entityKey: BOOKING, entityType: "booking", sessionKey: null } },
  ]);
  const ctx = { entityKey: BOOKING };
  assert.equal(app.entityColumn(ctx, "sessionKey"), undefined);   // null projects as absent
  assert.equal(app.entityColumn(ctx, "nosuch"), undefined);
  assert.equal(app.entityColumn({}, "sessionKey"), undefined);    // no entity context at all

  const op = { dispatchContextParams: JSON.stringify({ session: "{entity.sessionKey}" }) };
  assert.equal(app.unresolvableEntityColumn(op, ctx), "sessionKey");
  assert.equal(app.unresolvableEntityColumn(op, { entityKey: "vtx.booking.HHHHHHHHHHHHHHHHHHHH" }), "sessionKey");
  // The `:id` modifier is stripped before the column lookup.
  const idOp = { dispatchContextParams: JSON.stringify({ session: "{entity.sessionKey:id}" }) };
  assert.equal(app.unresolvableEntityColumn(idOp, ctx), "sessionKey");
});

test("opButton degrades an op whose {entity.<column>} the row can't answer", () => {
  const op = {
    key: "vtx.meta.EEEEEEEEEEEEEEEEEEEE",
    data: {
      operationType: "CancelBooking", title: "Cancel booking", dispatchClass: "booking",
      dispatchTargetField: "bookingKey", dispatchTargetType: "booking",
      dispatchContextParams: JSON.stringify({ session: "{entity.sessionKey}" }),
    },
  };
  const meRow = { identityKey: "vtx.identity.DDDDDDDDDDDDDDDDDDDD" };

  const ok = loadAppWithEntities(meRow, [bookingRow]);
  const okHtml = ok.opButton(op, { entityKey: BOOKING });
  assert.match(okHtml, /data-open-op="vtx\.meta\.EEEEEEEEEEEEEEEEEEEE"/);
  assert.match(okHtml, /data-entity-key="vtx\.booking\.FFFFFFFFFFFFFFFFFFFF"/);
  assert.doesNotMatch(okHtml, /degraded-card/);

  const bad = loadAppWithEntities(meRow, [
    { key: "manifest.ent." + BOOKING, data: { entityKey: BOOKING, entityType: "booking" } },
  ]);
  const badHtml = bad.opButton(op, { entityKey: BOOKING });
  assert.match(badHtml, /degraded-card/);
  assert.match(badHtml, /sessionKey/);
  assert.doesNotMatch(badHtml, /data-open-op=/);
});
