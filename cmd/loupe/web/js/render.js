// The linkifying document renderer (design §7.3) + the shared key-chip
// element: JSON rendered as DOM where every string value that is a well-formed
// entity key becomes a link through the one keyTarget resolver. Used by the
// Graph explorer (documents, aspects, link documents) and any view that
// renders a raw platform reply.

import { el } from "./api.js";
import { isEntityKey, keyTarget } from "./logic/keys.js";

// keyLinkEl renders a key-shaped string as a clickable chip when it resolves,
// else plain text.
function keyLinkEl(key, cls) {
  const target = isEntityKey(key) ? keyTarget(key) : null;
  if (!target) return el("span", cls, key);
  const a = el("a", (cls ? cls + " " : "") + "key-link", key);
  a.href = target;
  return a;
}

const INDENT = "  ";

// renderDoc pretty-prints a parsed JSON value into a <pre> with lite syntax
// styling (keys dim, entity-key strings as links). Non-object values render
// as their JSON form; undefined renders a muted placeholder.
function renderDoc(value) {
  const pre = el("pre", "vtx-doc doc");
  if (value === undefined) {
    pre.appendChild(el("span", "doc-muted", "(non-JSON value)"));
    return pre;
  }
  appendValue(pre, value, "");
  return pre;
}

function appendValue(parent, v, indent) {
  if (v === null || typeof v === "number" || typeof v === "boolean") {
    parent.appendChild(el("span", "doc-lit", JSON.stringify(v)));
    return;
  }
  if (typeof v === "string") {
    if (isEntityKey(v)) {
      parent.appendChild(document.createTextNode('"'));
      parent.appendChild(keyLinkEl(v, "doc-link"));
      parent.appendChild(document.createTextNode('"'));
    } else {
      parent.appendChild(el("span", "doc-str", JSON.stringify(v)));
    }
    return;
  }
  if (Array.isArray(v)) {
    if (!v.length) { parent.appendChild(document.createTextNode("[]")); return; }
    parent.appendChild(document.createTextNode("[\n"));
    v.forEach((item, i) => {
      parent.appendChild(document.createTextNode(indent + INDENT));
      appendValue(parent, item, indent + INDENT);
      parent.appendChild(document.createTextNode((i < v.length - 1 ? "," : "") + "\n"));
    });
    parent.appendChild(document.createTextNode(indent + "]"));
    return;
  }
  if (typeof v === "object") {
    const keys = Object.keys(v);
    if (!keys.length) { parent.appendChild(document.createTextNode("{}")); return; }
    parent.appendChild(document.createTextNode("{\n"));
    keys.forEach((k, i) => {
      parent.appendChild(document.createTextNode(indent + INDENT));
      parent.appendChild(el("span", "doc-key", JSON.stringify(k)));
      parent.appendChild(document.createTextNode(": "));
      appendValue(parent, v[k], indent + INDENT);
      parent.appendChild(document.createTextNode((i < keys.length - 1 ? "," : "") + "\n"));
    });
    parent.appendChild(document.createTextNode(indent + "}"));
    return;
  }
  parent.appendChild(el("span", "doc-lit", String(v)));
}

export { renderDoc, keyLinkEl };
