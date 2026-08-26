// internal/descriptorform/attachments.mjs — the shared AttachObject/
// DetachObject client mechanism every staff vertical app may mount at
// /shared/attachments.mjs (staff-descriptor-rendering-design.md §22).
//
// AttachObject/DetachObject stay hand-built on purpose (design §2.3 — file
// upload is a ceremony no schema-driven form.mjs field can express: its
// inputs are the RESPONSE of a byte-plane upload the client has to perform
// first, not values a template can produce). What was missing was not an
// exemption for that fact but a single, reviewed, reusable implementation of
// the ceremony — loftspace-app had one (Fire 2b, #75), café/clinic/wellness
// each would otherwise have re-derived it independently. This module is that
// extraction: byte-for-byte the same logic loftspace-app carried inline,
// parameterized so any app can mount it.
//
// Each function takes the app-specific I/O it needs as its first argument
// rather than assuming a global `submitOp`/`appPost`, since those differ per
// app (auth wrapper, base path). Everything else — the NanoID derivation, the
// AttachObject/DetachObject envelope shape, the Contract #4 dedup convention —
// is identical across every caller and lives here once.
//
// The owner-anchored READ surface each app needs to list what it attached is
// NOT part of this module: loftspace-app's GET /api/objects?owner=<key>
// already serves it from the objectAttachments lens (P5), but that is a
// per-app server route, not client mechanism, and a fully descriptor-driven
// Dispatch for DetachObject still needs a lens anchored on the OWNER (not the
// object) that objects-base does not have yet — the open follow-up, checkpointed
// in design §22.

// NANOID_ALPHABET/NANOID_LENGTH mirror internal/substrate (Contract #1) exactly.
const NANOID_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789";
const NANOID_LENGTH = 20;

// deriveNanoID mirrors substrate.DeriveNanoID byte-for-byte (a SHA-256
// expansion across the canonical alphabet, re-hashing as the digest is
// exhausted), so an AttachObject submitted here collapses on the Contract #4
// requestId tracker exactly like a server-derived requestId would —
// load-bearing for object-store-manager's never-attached reconcile grace
// window (internal/objectmanager), which assumes a retried attach of the
// same bytes+slot reuses the same requestId.
export async function deriveNanoID(namespace, input) {
  const enc = new TextEncoder();
  let digest = new Uint8Array(await crypto.subtle.digest("SHA-256", enc.encode(namespace + input)));
  let di = 0;
  const out = [];
  for (let i = 0; i < NANOID_LENGTH; i++) {
    if (di >= digest.length) {
      digest = new Uint8Array(await crypto.subtle.digest("SHA-256", digest));
      di = 0;
    }
    out.push(NANOID_ALPHABET[digest[di] % NANOID_ALPHABET.length]);
    di++;
  }
  return out.join("");
}

// objectLinkKey mirrors objects-base's own key derivation:
// lnk.object.<oid>.<linkName>.<type>.<id>.
export function objectLinkKey(oid, targetKey, linkName) {
  const parts = targetKey.split(".");
  if (parts.length !== 3 || parts[0] !== "vtx") {
    throw new Error("targetKey must be vtx.<type>.<id>: " + targetKey);
  }
  return "lnk.object." + oid + "." + linkName + "." + parts[1] + "." + parts[2];
}

// attachObject uploads bytes via the caller-supplied `upload` (the app's own
// byte-plane POST — its response must carry {oid, digest, size, contentType,
// storeName} and, for a sensitive upload, {sensitive, governingIdentity,
// encryption}), then submits AttachObject through the caller-supplied
// `submit` (the app's browser-direct op path). Throws on a rejected op (or a
// transport error) so callers can count/report; the upload response's oid is
// returned so the caller doesn't need to recompute it.
//
// `namespace` scopes the Contract #4 requestId derivation — callers use a
// stable, app-specific string (e.g. "loftspace:object:attach:") so a retried
// attach of the same bytes+slot always re-derives the same requestId,
// regardless of what other apps or slots exist.
//
// sensitiveOpts (object-store-crypto-shred-design.md §9 Fire 4 Increment 2):
// pass { governingIdentity } to upload a crypto-shreddable PII document (e.g.
// an ID scan / proof-of-income) — the server seals the bytes under a per-object
// CEK and returns the encryption envelope alongside the usual byte-plane
// metadata; this function folds it into the AttachObject payload unchanged
// (the app itself never sees key material, only the already-wrapped
// envelope). Omit for an ordinary (unencrypted) attach.
export async function attachObject({ upload, submit, namespace }, file, targetKey, linkName, sensitiveOpts) {
  const uploaded = await upload(file, sensitiveOpts);
  const { oid, digest, size, contentType, storeName, sensitive, governingIdentity, encryption } = uploaded;
  const payload = { digest, size, contentType, storeName, targetKey, linkName };
  if (file.name) payload.filename = file.name;
  if (sensitive) {
    payload.sensitive = true;
    payload.governingIdentity = governingIdentity;
    payload.encryption = encryption;
  }
  const requestId = await deriveNanoID(namespace, [digest, targetKey, linkName].join("\x00"));
  const reply = await submit({
    requestId,
    operationType: "AttachObject",
    class: "object",
    reads: [targetKey],
    payload,
  });
  if (reply && reply.status === "rejected") {
    const msg = reply.error ? `${reply.error.code}: ${reply.error.message}` : "rejected";
    throw new Error(msg);
  }
  return { oid, linkName, targetKey, size, contentType };
}

// detachObject submits DetachObject through the caller-supplied `submit`,
// mirroring attachObject. Does NOT throw on a rejected reply — callers branch
// on reply.status, the same contract loftspace-app's original DELETE
// /api/objects response gave its callers.
export async function detachObject(submit, oid, targetKey, linkName) {
  const linkKey = objectLinkKey(oid, targetKey, linkName);
  const objKey = "vtx.object." + oid;
  return submit({
    operationType: "DetachObject",
    class: "object",
    reads: [linkKey, objKey],
    payload: { oid, targetKey, linkName },
  });
}
