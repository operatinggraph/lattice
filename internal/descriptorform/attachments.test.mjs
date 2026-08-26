// Regression vectors for internal/descriptorform/attachments.mjs
// (staff-descriptor-rendering-design.md §22). No DOM is involved (unlike
// form.test.mjs) — attachObject/detachObject take their upload/submit I/O as
// injected functions, so these vectors exercise pure logic against fakes.

import { test } from "node:test";
import assert from "node:assert/strict";
import { deriveNanoID, objectLinkKey, attachObject, detachObject } from "./attachments.mjs";

// Golden vectors shared with internal/substrate/derive_test.go
// (TestDeriveNanoID_Golden) — the JS port must reproduce the exact same byte
// sequence as the Go implementation, since an AttachObject submitted from
// either side has to collapse on the same Contract #4 requestId.
test("deriveNanoID matches the Go substrate.DeriveNanoID golden vectors", async () => {
  assert.equal(await deriveNanoID("bridge:reply:", "req-123"), "EjraDYAJJPP3GXkv8ooM");
  assert.equal(await deriveNanoID("", ""), "5CYJnWeWpVNco5MnqAH6");
});

test("deriveNanoID is deterministic and namespace-isolated", async () => {
  const a = await deriveNanoID("object:attach:", "same-input");
  const b = await deriveNanoID("object:attach:", "same-input");
  assert.equal(a, b);
  const c = await deriveNanoID("other:namespace:", "same-input");
  assert.notEqual(a, c);
});

test("objectLinkKey builds lnk.object.<oid>.<linkName>.<type>.<id>", () => {
  assert.equal(
    objectLinkKey("obj123", "vtx.identity.abc", "photoOf"),
    "lnk.object.obj123.photoOf.identity.abc",
  );
});

test("objectLinkKey rejects a malformed targetKey", () => {
  assert.throws(() => objectLinkKey("obj123", "not-a-vtx-key", "photoOf"), /targetKey must be vtx/);
  assert.throws(() => objectLinkKey("obj123", "vtx.identity", "photoOf"), /targetKey must be vtx/);
});

const TARGET = "vtx.unit.abc";

test("attachObject assembles the envelope and returns the upload's oid", async () => {
  let submitted = null;
  const uploaded = { oid: "oid1", digest: "SHA-256=abc", size: 42, contentType: "image/png", storeName: "prov1" };
  const result = await attachObject(
    { upload: async () => uploaded, submit: async (env) => { submitted = env; return { status: "ok" }; }, namespace: "test:attach:" },
    { name: "photo.png" },
    TARGET,
    "photoOf",
  );
  assert.equal(submitted.operationType, "AttachObject");
  assert.equal(submitted.class, "object");
  assert.deepEqual(submitted.reads, [TARGET]);
  assert.deepEqual(submitted.payload, {
    digest: "SHA-256=abc",
    size: 42,
    contentType: "image/png",
    storeName: "prov1",
    targetKey: TARGET,
    linkName: "photoOf",
    filename: "photo.png",
  });
  assert.equal(typeof submitted.requestId, "string");
  assert.equal(submitted.requestId.length, 20);
  assert.deepEqual(result, { oid: "oid1", linkName: "photoOf", targetKey: TARGET, size: 42, contentType: "image/png" });
});

test("attachObject folds sensitive fields into the payload when present", async () => {
  let submitted = null;
  const uploaded = {
    oid: "oid2",
    digest: "SHA-256=def",
    size: 10,
    contentType: "application/pdf",
    storeName: "prov2",
    sensitive: true,
    governingIdentity: "vtx.identity.owner",
    encryption: { algo: "AES-256-GCM", nonce: "n", wrappedCEK: "w", keyId: "vtx.identity.owner" },
  };
  await attachObject(
    { upload: async () => uploaded, submit: async (env) => { submitted = env; return { status: "ok" }; }, namespace: "test:attach:" },
    { name: "" },
    TARGET,
    "signedLeaseOf",
    { governingIdentity: "vtx.identity.owner" },
  );
  assert.equal(submitted.payload.sensitive, true);
  assert.equal(submitted.payload.governingIdentity, "vtx.identity.owner");
  assert.deepEqual(submitted.payload.encryption, uploaded.encryption);
  assert.equal("filename" in submitted.payload, false, "an empty file.name must not add a filename field");
});

test("attachObject re-derives the same requestId for a retried attach of the same bytes+slot", async () => {
  const uploaded = { oid: "oid1", digest: "SHA-256=abc", size: 1, contentType: "image/png", storeName: "provX" };
  const seen = [];
  const opts = { upload: async () => uploaded, submit: async (env) => { seen.push(env.requestId); return { status: "ok" }; }, namespace: "test:attach:" };
  await attachObject(opts, { name: "a.png" }, TARGET, "photoOf");
  await attachObject(opts, { name: "a.png" }, TARGET, "photoOf");
  assert.equal(seen[0], seen[1]);
});

test("attachObject throws on a rejected reply", async () => {
  const uploaded = { oid: "oid1", digest: "SHA-256=abc", size: 1, contentType: "image/png", storeName: "provX" };
  await assert.rejects(
    attachObject(
      { upload: async () => uploaded, submit: async () => ({ status: "rejected", error: { code: "AuthDenied", message: "no grant" } }), namespace: "test:attach:" },
      { name: "a.png" },
      TARGET,
      "photoOf",
    ),
    /AuthDenied: no grant/,
  );
});

test("detachObject submits the link key + object key as reads and does not throw on rejection", async () => {
  let submitted = null;
  const reply = { status: "rejected", error: { code: "NotLive", message: "already detached" } };
  const result = await detachObject(async (env) => { submitted = env; return reply; }, "oid1", TARGET, "photoOf");
  assert.equal(submitted.operationType, "DetachObject");
  assert.equal(submitted.class, "object");
  assert.deepEqual(submitted.reads, ["lnk.object.oid1.photoOf.unit.abc", "vtx.object.oid1"]);
  assert.deepEqual(submitted.payload, { oid: "oid1", targetKey: TARGET, linkName: "photoOf" });
  assert.equal(result, reply, "detachObject must return the reply, not throw, on rejection");
});
