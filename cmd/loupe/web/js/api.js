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

// demoHide marks an element as a platform-write or PII-reveal affordance, so
// the hosted demo's shell rule (body.demo-mode in style.css) hides it from a
// visitor whose click would 403 anyway. Returns the element, so it can wrap an
// el(...) call inline.
//
// This is POLISH, not a boundary: enforcement is the server's demoReadOnly
// method rule and the platform's capability grants. An affordance a later fire
// forgets to mark stays visible and 403s honestly — cosmetic drift, never a
// security regression, which is what makes a marker convention enough here.
function demoHide(e) {
  if (e) e.setAttribute("data-demo-hide", "");
  return e;
}

function pretty(v) {
  try { return JSON.stringify(v, null, 2); }
  catch (_) { return String(v); }
}

// api GETs/POSTs JSON and returns the parsed body. A non-2xx with a JSON body
// is returned as-is (it carries {"error":...}); a transport failure is mapped
// to a synthetic {error} object so callers always get an object. A 401 means
// the operator session ended (expiry, revocation, or never logged in) —
// every /api/* route is behind the same gate, so this is the one place that
// needs to notice and send the operator back to /login rather than let each
// caller render a stray "operator login required" error inline.
//
// A failed response also carries its status back as `httpStatus`, because some
// refusals are not the same KIND of thing as an error and a caller that only
// sees the message cannot tell them apart — the demo posture's 403 is a
// standing rule about this deployment, not a fault to render in red. It is
// stamped only on non-2xx bodies and under a name no handler emits, so no
// successful payload's own fields (several carry a `status`) are shadowed.
async function api(path, opts) {
  try {
    const res = await fetch(path, opts);
    if (res.status === 401) {
      location.replace("/login");
      return { error: "operator login required" };
    }
    const text = await res.text();
    let body;
    try { body = text ? JSON.parse(text) : {}; }
    catch (_) { body = { error: "non-JSON response: " + text.slice(0, 200) }; }
    if (!res.ok && body && typeof body === "object") body.httpStatus = res.status;
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

export { $, $all, el, demoHide, pretty, api, setStatus, toast };
