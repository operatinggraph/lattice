// Unit vectors for the display-name floor rule (display-name-convention-
// design.md §2, N2): a bare NanoID is never a primary label. prettify is the
// last rung of the ladder ("<Type> · <short-id>"); anchorLabel composes the
// N1-projected location + container names; identityLabel renders the typed
// fallback instead of "Unnamed" until N3's sealed self-name arrives. Type
// labels themselves are DATA: an observed manifest.ent row's typeLabel wins,
// and titleCase of the raw segment is the only hardcoded floor.
//
// Same harness as descriptor_autofill.test.mjs: app.js is a plain browser
// script, so vm.runInContext hoists its function declarations onto the sandbox.
// The accessors resolve off the global at call time, so overwriting entities
// injects observed rows without a DOM or a feed (identityLabel's no-key branch
// falls through to shortIdentityLabel, which reads the module-scoped
// whoamiIdentityID — "" at load, so the neutral "You").

import { test } from "node:test";
import assert from "node:assert/strict";
import vm from "node:vm";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const appSrc = fs.readFileSync(path.join(__dirname, "app.js"), "utf8");

function loadApp() {
  const sandbox = { console, document: { addEventListener() {} } };
  vm.createContext(sandbox);
  vm.runInContext(appSrc, sandbox, { filename: "app.js" });
  return sandbox;
}

test("typeLabel prefers an observed ent row's projected typeLabel", () => {
  const app = loadApp();
  app.entities = () => [
    { key: "manifest.ent.x", data: { entityType: "leaseapp", typeLabel: "Lease application" } },
    { key: "manifest.ent.y", data: { entityType: "session" } }, // no label projected
  ];
  assert.equal(app.typeLabel("leaseapp"), "Lease application");
  // a type observed without a projected label still titleCases its segment
  assert.equal(app.typeLabel("session"), "Session");
  // a type never observed at all does too
  assert.equal(app.typeLabel("widget"), "Widget");
});

test("typeLabel titleCases the raw segment when nothing projected a label", () => {
  const { typeLabel } = loadApp(); // no ent rows in state at all
  assert.equal(typeLabel("leaseapp"), "Leaseapp");
  assert.equal(typeLabel("workorder"), "Workorder");
  assert.equal(typeLabel(""), "Item");
  assert.equal(typeLabel(undefined), "Item");
});

test("prettify renders a typed label with a short id, never a bare NanoID", () => {
  const app = loadApp();
  // with no projected labels, every type titleCases its own segment
  assert.equal(app.prettify("vtx.leaseapp.AAAAAAAAAAAAAAAAAAAA"), "Leaseapp · AAAAAA");
  assert.equal(app.prettify("vtx.building.BBBBBBBBBBBBBBBBBBBB"), "Building · BBBBBB");
  assert.equal(app.prettify("vtx.widget.CCCCCCCCCCCCCCCCCCCC"), "Widget · CCCCCC");
  // an observed typeLabel flows through to the composed floor label
  app.entities = () => [
    { key: "manifest.ent.x", data: { entityType: "leaseapp", typeLabel: "Lease application" } },
  ];
  assert.equal(app.prettify("vtx.leaseapp.AAAAAAAAAAAAAAAAAAAA"), "Lease application · AAAAAA");
  // too-short to be a Contract #1 key: passthrough, no crash
  assert.equal(app.prettify("manifest.me"), "manifest.me");
  assert.equal(app.prettify(""), "Unknown");
});

test("anchorLabel composes N1 location + container names, floors to typed", () => {
  const { anchorLabel } = loadApp();
  const key = "vtx.unit.UUUUUUUUUUUUUUUUUUUU";
  assert.equal(anchorLabel({ key, name: "Unit 1", containerName: "Riverside Building" }), "Unit 1 · Riverside Building");
  assert.equal(anchorLabel({ key, name: "Unit 1" }), "Unit 1");
  assert.equal(anchorLabel({ key, containerName: "Riverside Building" }), "Riverside Building");
  // no projected name yet: typed floor, never the raw key
  assert.equal(anchorLabel({ key }), "Unit · UUUUUU");
});

test("scopedLabel renders the projected label verbatim — no client suffixing", () => {
  const { scopedLabel } = loadApp();
  const scoped = "vtx.leaseapp.LLLLLLLLLLLLLLLLLLLL";
  // whatever phrase the lens projected IS the label; the client adds nothing
  assert.equal(scopedLabel(scoped, "Unit 1 lease"), "Unit 1 lease");
  assert.equal(scopedLabel(scoped, "Unit 1"), "Unit 1");
  assert.equal(scopedLabel("vtx.booking.BBBBBBBBBBBBBBBBBBBB", "Yoga Flow"), "Yoga Flow");
  // no projected name: typed floor, never a bare NanoID
  assert.equal(scopedLabel(scoped, null), "Leaseapp · LLLLLL");
  assert.equal(scopedLabel("", "Unit 1"), "");
});

test("identityLabel prefers the sealed self-name, else a typed fallback (never Unnamed)", () => {
  const { identityLabel } = loadApp();
  assert.equal(identityLabel({ displayName: "Sam Okafor" }), "Sam Okafor");
  assert.equal(identityLabel({ identityKey: "vtx.identity.IIIIIIIIIIIIIIIIIIII" }), "Identity · IIIIII");
  // no name, no key: the neutral "You", not "Unnamed" and not a domain word
  assert.equal(identityLabel({}), "You");
});

// ---- scopedTo-self vectors (facet-discovery-restoration follow-on) ----
// A task scoped to the signed-in identity labels as the viewer's own
// decrypted display name — the one identity whose name never needs a lens to
// project it. Another identity's key still floors to the typed label (its
// name is sealed, by design).

test("a self-scoped target labels as the viewer's own name, never a short id", () => {
  const sandbox = loadApp();
  vm.runInContext(
    `state.rows.set("manifest.me", { data: { identityKey: "vtx.identity.EEEEEEEEEEEEEEEEEEEE", displayName: "Sam Okafor" }, pending: false })`,
    sandbox);
  assert.equal(sandbox.scopedLabel("vtx.identity.EEEEEEEEEEEEEEEEEEEE", null), "Sam Okafor");
  assert.equal(sandbox.scopedLabel("vtx.identity.FFFFFFFFFFFFFFFFFFFF", null), sandbox.prettify("vtx.identity.FFFFFFFFFFFFFFFFFFFF"),
    "another identity still floors to the typed label");
});

test("a self-scoped target labels You before the display name arrives", () => {
  const sandbox = loadApp();
  vm.runInContext(
    `state.rows.set("manifest.me", { data: { identityKey: "vtx.identity.EEEEEEEEEEEEEEEEEEEE" }, pending: false })`,
    sandbox);
  assert.equal(sandbox.scopedLabel("vtx.identity.EEEEEEEEEEEEEEEEEEEE", null), "You");
});

test("a projected scoped name always wins over the self rule", () => {
  const sandbox = loadApp();
  vm.runInContext(
    `state.rows.set("manifest.me", { data: { identityKey: "vtx.identity.EEEEEEEEEEEEEEEEEEEE", displayName: "Sam Okafor" }, pending: false })`,
    sandbox);
  assert.equal(sandbox.scopedLabel("vtx.identity.EEEEEEEEEEEEEEEEEEEE", "Unit 2 lease"), "Unit 2 lease");
});
