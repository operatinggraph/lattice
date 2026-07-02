// Pure key-shape logic: the JS mirror of the Go classifier (corekv.go
// classifyKey, Contract #1 shapes) plus the shared key→route resolver. The
// goja harness drives classifyKey with the same case table as the Go test so
// FE and server never disagree on what a key is. No DOM, no fetch.

// classifyKey labels a Core KV key by its Contract #1 shape: 3-segment
// vtx.<type>.<id> vertex roots (vtx.meta.<id> is a meta-vertex), 4-segment
// aspects, 6-segment lnk.* links; anything else is unknown.
function classifyKey(key) {
  var k = key || "";
  var segs = k.split(".");
  for (var i = 0; i < segs.length; i++) {
    if (segs[i] === "") return "unknown"; // empty segment — never a well-formed key
  }
  if (k.indexOf("lnk.") === 0) {
    return segs.length === 6 ? "link" : "unknown";
  }
  if (k.indexOf("vtx.meta.") === 0) {
    if (segs.length === 3) return "meta";
    if (segs.length === 4) return "aspect";
    return "unknown";
  }
  if (k.indexOf("vtx.") === 0) {
    if (segs.length === 3) return "vertex";
    if (segs.length === 4) return "aspect";
    return "unknown";
  }
  return "unknown";
}

// isEntityKey reports whether a rendered string value is a well-formed Core KV
// entity key — the test the linkifying renderers use to decide clickability.
function isEntityKey(s) {
  return typeof s === "string" && classifyKey(s) !== "unknown";
}

// shortId drops the "vtx.<type>." prefix, leaving the id (+ any trailing segs).
function shortId(key) {
  return key.split(".").slice(2).join(".");
}

// keyTarget resolves an entity key to its console route — the one shared
// resolver behind every rendered key (design §1.2). Vertex/meta/link keys land
// on the Graph explorer detail; an aspect lands on its parent vertex with the
// ?aspect= param so the detail view can open that row. Non-entity strings
// resolve to null (render as plain text).
function keyTarget(key) {
  var cls = classifyKey(key);
  if (cls === "vertex" || cls === "meta" || cls === "link") {
    return "#/graph/" + key;
  }
  if (cls === "aspect") {
    var segs = key.split(".");
    return "#/graph/" + segs.slice(0, 3).join(".") + "?aspect=" + segs[3];
  }
  return null;
}

export { classifyKey, isEntityKey, shortId, keyTarget };
