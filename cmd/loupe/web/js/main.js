// Loupe UI entry: the route table, the shell (nav highlight, breadcrumbs,
// toast), and boot. Vanilla ES modules, no framework — the Go server does all
// NATS I/O and this renders its JSON. Every view is URL-addressable via the
// hash router (native back/forward, shareable deep links).

import { $, $all, el } from "./api.js";
import { startRouter, replaceRoute } from "./router.js";
import { classifyKey } from "./logic/keys.js";
import * as shell from "./shell.js";
import * as map from "./views/map.js";
import * as graph from "./views/graph.js";
import * as tasks from "./views/tasks.js";
import * as component from "./views/component.js";
import * as packages from "./views/packages.js";
import * as files from "./views/files.js";
import * as op from "./views/op.js";

// The route table: view name (the first hash segment) → panel + module. The
// nav anchors carry the same hashes, so a tab click is just a hash change.
// Drill pages (component) highlight their parent section's tab via nav and
// crumb back to it via crumbHref.
const routes = {
  map:       { panel: "systemmap", view: map,       crumb: "System Map" },
  graph:     { panel: "graph",     view: graph,     crumb: "Graph" },
  tasks:     { panel: "tasks",     view: tasks,     crumb: "Tasks" },
  component: { panel: "component", view: component, crumb: "System Map", nav: "systemmap", crumbHref: "#/map" },
  packages:  { panel: "packages",  view: packages,  crumb: "Packages" },
  files:     { panel: "files",     view: files,     crumb: "Files" },
  op:        { panel: "op",        view: op,        crumb: "Submit Op" },
};

let current = null;

function dispatch(route) {
  if (!route.view) { replaceRoute("/map"); return; }
  // Legacy #/corekv deep links land on the Graph explorer — same arg (a key
  // selects the detail) and params (?prefix= is the raw-prefix escape hatch,
  // ?aspect= opens that row). Params re-encode via encodeURIComponent so they
  // survive parseRoute's decodeURIComponent round-trip.
  if (route.view === "corekv") {
    const parts = Object.keys(route.params).map(
      (k) => encodeURIComponent(k) + "=" + encodeURIComponent(route.params[k]),
    );
    replaceRoute("/graph" + (route.arg ? "/" + route.arg : "") + (parts.length ? "?" + parts.join("&") : ""));
    return;
  }
  // Legacy #/control deep links land on the map — the Control tab dissolved
  // into the component pages (a map node click drills into its page).
  if (route.view === "control") { replaceRoute("/map"); return; }
  // Legacy #/health deep links land on the map — the Health tab dissolved:
  // alerts → the global strip, rollup → the topbar pill + map banner,
  // component cards → the component pages, gates → the map rail.
  if (route.view === "health") { replaceRoute("/map"); return; }
  const entry = routes[route.view];
  if (!entry) {
    toast("unknown route “#/" + route.view + "” — back to the map");
    replaceRoute("/map");
    return;
  }

  if (current && current !== entry && current.view.leave) current.view.leave();

  // Activate the panel + nav link for this view (drill pages highlight their
  // parent section's tab).
  $all(".tab").forEach((a) => a.classList.toggle("active", a.dataset.tab === (entry.nav || entry.panel)));
  $all(".panel").forEach((p) => p.classList.toggle("active", p.id === "panel-" + entry.panel));

  renderCrumbs(route, entry);
  current = entry;
  entry.view.enter(route);
}

// renderCrumbs fills the breadcrumb bar on drill pages (a route with an arg):
// section › key, the key segment-decomposed with the type segment linking to
// the type-filtered list. Tab-level routes hide the bar.
function renderCrumbs(route, entry) {
  const bar = $("#breadcrumbs");
  bar.innerHTML = "";
  if (!route.arg) { bar.classList.remove("visible"); return; }
  bar.classList.add("visible");

  const section = el("a", "crumb", entry.crumb);
  section.href = entry.crumbHref || "#/" + route.view;
  bar.appendChild(section);
  bar.appendChild(el("span", "crumb-sep", "›"));

  const segs = route.arg.split(".");
  const keyBox = el("span", "crumb-key");
  segs.forEach((s, i) => {
    if (i) keyBox.appendChild(el("span", "crumb-dot", "."));
    // The type segment of a vertex root links to its type-filtered list.
    if (i === 1 && classifyKey(route.arg) !== "unknown" && route.arg.indexOf("vtx.") === 0) {
      const a = el("a", "crumb", s);
      a.href = "#/graph?type=" + encodeURIComponent(s);
      keyBox.appendChild(a);
    } else {
      keyBox.appendChild(el("span", null, s));
    }
  });
  bar.appendChild(keyBox);
}

// toast shows a small transient notice (unknown routes, copy feedback).
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

// Boot: wire the shell (topbar pill + alert strip) and each view's static
// DOM, then start routing.
shell.init();
map.init();
graph.init();
tasks.init();
component.init();
packages.init();
files.init();
op.init();
startRouter(dispatch);
