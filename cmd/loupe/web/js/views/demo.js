// The hosted-demo posture (F20): reads /api/demo once at boot, renders the
// visitor banner, and holds the posture for the rest of the console. The
// shaping decisions live in logic/demo.js; this module only fetches and puts
// things on the page.
//
// Affordance suppression is driven from here in two ways. The blanket case is
// declarative — a `demo-mode` class on <body> plus one CSS rule hides every
// element marked with demoHide(), so a view marks its write buttons once and
// never consults the posture. The one case that needs the value is the
// control-plane op buttons, where only the ops the server classifies as
// inspect-only stay visible (controlOpHidden).
//
// Some affordances stay visible on purpose and answer 403 — the Describe
// panel's Submit, so a visitor can walk the authoring path and see what it
// costs. refusalNotice is how those panels render that answer as the standing
// rule it is rather than as a fault.

import { $, api, el } from "../api.js";
import { demoBanner, demoPostureOn, demoControlOpHidden, demoRefusalNotice } from "../logic/demo.js";

// The last /api/demo payload. Null until init resolves, which main.js awaits
// before routing — so no view renders against an unknown posture.
let payload = null;

async function init() {
  payload = await api("/api/demo");
  // The class is what the shell's suppression rule keys off. It is decided by
  // the posture itself, not by whether the banner happens to render, so the
  // suppression mechanism does not hang off the banner's null contract.
  if (!demoPostureOn(payload)) return;
  document.body.classList.add("demo-mode");
  const banner = demoBanner(payload);
  const host = $("#demobanner");
  if (!banner || !host) return;
  host.appendChild(el("strong", null, banner.title));
  host.appendChild(el("span", null, banner.text));
  host.classList.add("visible");
}

// controlOpHidden reports whether a control-plane op button should be omitted
// under the current posture — true only in demo mode, and only for ops outside
// the server's own read-only classification.
function controlOpHidden(comp, op) {
  return demoControlOpHidden(payload, comp, op);
}

// refusalNotice shapes a 403'd write's response into {title, text} for a panel
// to render, or null when the response is not a refusal.
function refusalNotice(body) {
  return demoRefusalNotice(payload, body);
}

export { init, controlOpHidden, refusalNotice };
