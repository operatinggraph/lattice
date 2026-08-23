// The hosted-demo posture's render decision (F20,
// loupe-f20-demo-operator-ux.md). Pure — no DOM, no fetch.
//
// The banner is a disclaimer for a visitor on a public URL. Its copy promises
// only what this console actually enforces — writes and reveals refused — and
// not that the platform's grants are narrow, which is provisioned separately
// and which nothing on this page can verify.

// demoPostureOn reads the one field that decides everything else. Anything
// other than an explicit demoMode:true is "not a demo" — an absent, failed, or
// malformed read leaves the ordinary console untouched rather than fabricating
// a posture from a missing field. Suppression is cosmetic (the server's method
// rule is the enforcement), so failing this way costs nothing but honesty.
function demoPostureOn(payload) {
  return !!payload && payload.demoMode === true;
}

// demoControlOpHidden reports whether a control-plane op button should be
// hidden under the given posture: in demo mode the server refuses every control
// op except the ones that only inspect, which it names in readOnlyControlOps.
// Reading that classification off the server response rather than restating it
// here means the buttons shown and the ops permitted cannot drift apart.
//
// An absent or malformed list hides every op for that component — the same
// omission-denies posture the server's gate takes, so a shape change degrades
// to "too little shown", never to a button that only 403s.
function demoControlOpHidden(payload, comp, op) {
  if (!demoPostureOn(payload)) return false;
  var byComp = payload.readOnlyControlOps;
  if (!byComp || typeof byComp !== "object") return true;
  var ops = byComp[comp];
  if (!Array.isArray(ops)) return true;
  return ops.indexOf(op) === -1;
}

// demoDenialLead is the phrase the server's own denial message leads with
// (demo.go's demoDenialMessage), so that message stands alone as a 403 body. It
// is how this module RECOGNIZES that message: the banner drops the lead-in it
// already renders as a title, and demoRefusalNotice reads it as the signature
// of a refusal the demo posture authored.
var demoDenialLead = /^read-only demo:\s*/i;

// demoBanner shapes /api/demo into the banner to render, or null for none.
function demoBanner(payload) {
  if (!demoPostureOn(payload)) return null;
  var notice = typeof payload.notice === "string" ? payload.notice.trim() : "";
  if (demoDenialLead.test(notice)) notice = notice.replace(demoDenialLead, "");
  return {
    title: "Read-only demo",
    // The server's own denial message is the body when it sent one, so the
    // banner and the 403 a visitor triggers say the same thing.
    text: notice ||
      "This is a live Lattice operator console, in read-only demo mode. Write actions and PII reveals are refused.",
  };
}

// demoRefusalNotice shapes a refused write's api() response into the notice a
// panel renders in place of a raw error line, or null when the response is not
// a refusal at all. An affordance the demo leaves ON SCREEN (the Describe
// panel's Submit) exists precisely so a visitor can trigger the server's
// denial, so that denial is the expected outcome of a working console, not a
// fault — rendering it as red error text says the opposite.
//
// The text is always the SERVER's own message; the console never restates the
// rule, the same way demoControlOpHidden reads the classification off /api/demo
// instead of duplicating it.
//
// The TITLE is decided from that message too, not from the posture. The posture
// cannot tell one 403 from another: the cross-origin gate refuses from inside
// requireOperator, outside demoReadOnly entirely, and the demo deployment is
// exactly the one served on a public origin — so in demo mode "every 403 is the
// demo's" is false, and it is false about a refusal an operator most needs
// named accurately. A message the demo posture did not author is titled as the
// plain refusal it is.
function demoRefusalNotice(payload, body) {
  if (!body || body.httpStatus !== 403) return null;
  var text = typeof body.error === "string" ? body.error.trim() : "";
  if (!text) return null;
  var demoAuthored = demoPostureOn(payload) && demoDenialLead.test(text);
  return { title: demoAuthored ? "Read-only demo" : "Refused", text: text };
}

export { demoBanner, demoPostureOn, demoControlOpHidden, demoRefusalNotice };
