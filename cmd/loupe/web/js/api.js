// Shared DOM-adjacent helpers: element selection/creation, JSON fetch, status
// lines. The Go server does all NATS I/O; every /api/* response may carry
// {"error": ...} and callers surface it inline rather than throwing.

function $(sel, root) { return (root || document).querySelector(sel); }
function $all(sel, root) { return Array.from((root || document).querySelectorAll(sel)); }

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function pretty(v) {
  try { return JSON.stringify(v, null, 2); }
  catch (_) { return String(v); }
}

// api GETs/POSTs JSON and returns the parsed body. A non-2xx with a JSON body
// is returned as-is (it carries {"error":...}); a transport failure is mapped
// to a synthetic {error} object so callers always get an object.
async function api(path, opts) {
  try {
    const res = await fetch(path, opts);
    const text = await res.text();
    let body;
    try { body = text ? JSON.parse(text) : {}; }
    catch (_) { body = { error: "non-JSON response: " + text.slice(0, 200) }; }
    return body;
  } catch (e) {
    return { error: "request failed: " + e.message };
  }
}

// toast shows a small transient notice (unknown routes, copy feedback,
// cross-view status notes like "lens deleted").
let toastTimer = null;
function toast(msg) {
  let t = $("#toast");
  if (!t) {
    t = el("div", null, "");
    t.id = "toast";
    document.body.appendChild(t);
  }
  t.textContent = msg;
  t.classList.add("visible");
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove("visible"), 3500);
}

function setStatus(id, msg, isError) {
  const e = document.getElementById(id);
  if (!e) return;
  e.textContent = msg || "";
  e.className = "muted" + (isError ? " error-text" : "");
}

export { $, $all, el, pretty, api, setStatus, toast };
