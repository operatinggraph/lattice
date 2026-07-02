// Loupe UI entry: the route table, the shell (nav highlight, breadcrumbs,
// toast), and boot. Vanilla ES modules, no framework — the Go server does all
// NATS I/O and this renders its JSON. Every view is URL-addressable via the
// hash router (native back/forward, shareable deep links).

import { $, $all, el } from "./api.js";
import { startRouter, replaceRoute } from "./router.js";
import { classifyKey } from "./logic/keys.js";
import * as map from "./views/map.js";
import * as graph from "./views/graph.js";
import * as health from "./views/health.js";
import * as tasks from "./views/tasks.js";
import * as control from "./views/control.js";
import * as packages from "./views/packages.js";
import * as files from "./views/files.js";
import * as op from "./views/op.js";

// The route table: view name (the first hash segment) → panel + module. The
// nav anchors carry the same hashes, so a tab click is just a hash change.
const routes = {
  map:      { panel: "systemmap", view: map,      crumb: "System Map" },
  graph:    { panel: "graph",     view: graph,    crumb: "Graph" },
  health:   { panel: "health",    view: health,   crumb: "Health" },
  tasks:    { panel: "tasks",     view: tasks,    crumb: "Tasks" },
  control:  { panel: "control",   view: control,  crumb: "Control" },
  packages: { panel: "packages",  view: packages, crumb: "Packages" },
  files:    { panel: "files",     view: files,    crumb: "Files" },
  op:       { panel: "op",        view: op,       crumb: "Submit Op" },
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
  const entry = routes[route.view];
  if (!entry) {
    toast("unknown route “#/" + route.view + "” — back to the map");
    replaceRoute("/map");
    return;
  }

  if (current && current !== entry && current.view.leave) current.view.leave();

  // Activate the panel + nav link for this view.
  $all(".tab").forEach((a) => a.classList.toggle("active", a.dataset.tab === entry.panel));
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
  section.href = "#/" + route.view;
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

// Boot: wire each view's static DOM, then start routing.
map.init();
graph.init();
health.init();
tasks.init();
control.init();
packages.init();
files.init();
op.init();
startRouter(dispatch);
