// Package detail page (#/package/<key>, design §9.2–9.3): header + MANIFEST
// panel + CONTENTS (the graph-resolved declared entities, every item a
// keyLink; lenses also link their #/lens page) + LIFECYCLE (upgrade/refresh
// behind a dry-run preview, uninstall behind a typed confirm). Also exports
// openApplyModal — the shared install/upgrade upload flow the Packages list's
// toolbar Install action reuses.

import { $, el, demoHide, api, setStatus, toast } from "../api.js";
import { navigate, replaceRoute } from "../router.js";
import { manifestCandidate, applySummaryLine, uninstallSummary } from "../logic/pkg.js";
import { deleteConfirmReady } from "../logic/lens.js";
import { renderDoc, keyLinkEl } from "../render.js";

const state = { key: null, modal: null };

function enter(route) {
  closeModal();
  if (!route.arg) { replaceRoute("/packages"); return; }
  state.key = route.arg;
  load(route.arg);
}

// leave closes a dangling modal so a route change can never leave a live
// upload or destructive confirm floating over an unrelated view.
function leave() {
  closeModal();
}

function closeModal() {
  if (state.modal) { state.modal.close(); state.modal = null; }
}

async function load(key) {
  const head = $("#package-head");
  const panels = $("#package-panels");
  head.innerHTML = "";
  panels.innerHTML = "";
  panels.appendChild(el("div", "muted small", "loading…"));
  setStatus("package-status", "loading…");
  const body = await api("/api/package?key=" + encodeURIComponent(key));
  if (key !== state.key) return; // navigated away while loading
  head.innerHTML = "";
  panels.innerHTML = "";
  if (body.error) {
    setStatus("package-status", body.error, true);
    const card = el("div", "notfound-card");
    card.appendChild(el("div", "notfound-key", key));
    card.appendChild(el("div", "muted", body.error));
    const back = el("a", "key-link", "← back to Packages");
    back.href = "#/packages";
    card.appendChild(back);
    panels.appendChild(card);
    return;
  }
  setStatus("package-status", "");
  renderHead(head, body);
  panels.appendChild(manifestPanel(body));
  panels.appendChild(contentsPanel(body));
  panels.appendChild(lifecyclePanel(body));
}

// renderHead: name · version tag · installedAt · the raw-envelope Graph link
// (the package page owns vtx.package.* chips, so this link is the explicit
// way back to provenance).
function renderHead(head, pkg) {
  head.appendChild(el("h2", "comp-title", pkg.name || pkg.key));
  if (pkg.version) head.appendChild(el("span", "state-tag", "v" + pkg.version));
  if (pkg.isDeleted) head.appendChild(el("span", "deleted-flag", "isDeleted (uninstalled)"));
  if (pkg.installedAt) head.appendChild(el("span", "muted small", "installed " + pkg.installedAt));
  const raw = el("a", "key-link small", "raw envelope in Graph →");
  raw.href = "#/graph/" + pkg.key;
  head.appendChild(raw);
  const refresh = el("button", null, "Refresh");
  refresh.addEventListener("click", () => load(pkg.key));
  head.appendChild(refresh);
}

function panel(title) {
  const box = el("section", "lens-panel");
  box.appendChild(el("h3", "comp-section", title));
  return box;
}

function manifestPanel(pkg) {
  const box = panel("Manifest");
  if (pkg.description) box.appendChild(el("div", null, pkg.description));
  const details = el("details");
  details.appendChild(el("summary", "muted small", "raw manifest document"));
  details.appendChild(renderDoc(pkg.manifest));
  box.appendChild(details);
  return box;
}

// contentsPanel renders §9.2: one section per kind, every resolved item a
// keyLink chip (lenses also link their page); an unresolvable declared item
// renders dimmed with "not found in graph" — honest, never silently dropped.
function contentsPanel(pkg) {
  const box = panel("Contents — what this package put in the graph");
  const sections = pkg.sections || [];
  if (!sections.length) {
    box.appendChild(el("div", "muted", "no declared keys recorded on the manifest aspect"));
    return box;
  }
  box.appendChild(el("div", "muted small",
    pkg.declaredCount + " declared key(s)" +
    (pkg.unresolved ? " · " + pkg.unresolved + " unresolved" : "")));
  sections.forEach((sec) => {
    box.appendChild(el("h4", "pkg-sechead", sec.label + " (" + sec.count + ")"));
    const list = el("div", "pkg-items");
    (sec.items || []).forEach((it) => {
      const row = el("div", "pkg-item" + (it.isDeleted ? " pkg-item-deleted" : ""));
      if (it.name) row.appendChild(el("span", "pkg-item-name", it.name));
      if (!it.found) {
        row.appendChild(el("span", "muted", it.key));
        row.appendChild(el("span", "muted small", "not found in graph"));
      } else {
        row.appendChild(keyLinkEl(it.key, "small"));
        if (it.isDeleted) row.appendChild(el("span", "deleted-flag", "isDeleted"));
        if (it.aspects) row.appendChild(el("span", "muted small", "+" + it.aspects + " aspect(s)"));
        if (it.lensId) {
          const lp = el("a", "key-link small", "lens page →");
          lp.href = "#/lens/" + it.lensId;
          row.appendChild(lp);
        }
      }
      list.appendChild(row);
    });
    box.appendChild(list);
  });
  return box;
}

// lifecyclePanel renders §9.3's detail-page actions: upgrade/refresh (the
// F-004 in-place diff-apply, behind a dry-run preview) and uninstall
// (destructive, typed confirm). Replies render linkified inline.
function lifecyclePanel(pkg) {
  // Every action in this panel is a write, so the whole panel is marked rather
  // than its two buttons — otherwise a demo visitor gets a "Lifecycle" heading
  // over an empty box and a stray soft-delete caption.
  const box = demoHide(panel("Lifecycle"));
  const row = el("div", "lens-ctlrow");
  const upBtn = el("button", "comp-ctlbtn", "upgrade / refresh…");
  if (pkg.isDeleted) {
    upBtn.disabled = true;
    upBtn.title = "package is uninstalled — reinstall from the Packages list";
  }
  upBtn.addEventListener("click", () => {
    openApplyModal({
      title: "Upgrade / refresh " + (pkg.name || pkg.key),
      intro: "Re-submits the package's manifest.yaml against the existing install " +
        "(in-place diff-apply). Edited and newly-added entities both activate live, " +
        "no restart — only a primordial kernel-seed change needs a fresh bootstrap.",
      endpoint: "/api/packages/upgrade",
      // No force checkbox: the explicit-upgrade endpoint diff-applies a
      // same-version target unconditionally, so force changes nothing.
      showForce: false,
      onDone: () => load(pkg.key),
    });
  });
  row.appendChild(upBtn);
  box.appendChild(row);

  const delRow = el("div", "lens-delrow");
  const delBtn = el("button", "danger-btn", "uninstall…");
  if (pkg.isDeleted) {
    delBtn.disabled = true;
    delBtn.title = "already uninstalled";
  } else if (!pkg.name) {
    // The server resolves an uninstall by manifest name; without one the
    // request can never succeed, so don't offer an unwinnable confirm.
    delBtn.disabled = true;
    delBtn.title = "package has no manifest name — uninstall via lattice-pkg";
  }
  delBtn.addEventListener("click", () => openUninstallModal(pkg));
  delRow.appendChild(delBtn);
  delRow.appendChild(el("span", "muted small",
    "soft-delete: " + uninstallSummary(pkg)));
  box.appendChild(delRow);
  return box;
}

// modalShell builds the shared overlay + focus/ESC plumbing. focusables() is
// re-evaluated per keypress so buttons that enable/disable stay in the cycle.
function modalShell(title, focusables, isBusy) {
  const overlay = el("div", "modal-overlay");
  const modal = el("div", "modal");
  modal.appendChild(el("h3", null, title));
  overlay.appendChild(modal);
  document.body.appendChild(overlay);

  const close = () => {
    document.removeEventListener("keydown", onKey);
    overlay.remove();
    if (state.modal && state.modal.el === overlay) state.modal = null;
  };
  const onKey = (e) => {
    if (e.key === "Escape" && !isBusy()) { close(); return; }
    if (e.key === "Tab") {
      const f = focusables().filter((x) => !x.disabled);
      if (!f.length) { e.preventDefault(); return; }
      const i = f.indexOf(document.activeElement);
      let next = i + (e.shiftKey ? -1 : 1);
      if (i === -1) next = 0;
      if (next < 0) next = f.length - 1;
      if (next >= f.length) next = 0;
      f[next].focus();
      e.preventDefault();
    }
  };
  document.addEventListener("keydown", onKey);
  overlay.addEventListener("click", (e) => { if (e.target === overlay && !isBusy()) close(); });
  closeModal(); // never stack two modals
  state.modal = { el: overlay, close };
  return { overlay, modal, close };
}

// renderApplyReply renders an install/upgrade reply: the summary line, the
// dry-run key delta (linkified chips), any dependency warnings, and the
// stranded-custody escalation when the diff met a holder it could not have
// saved.
function renderApplyReply(body) {
  const out = el("div");
  if (body.error) {
    out.appendChild(el("div", "error-text", body.error));
    return out;
  }
  out.appendChild(el("div", null, applySummaryLine(body)));
  (body.warnings || []).forEach((w) => out.appendChild(el("div", "warn-text small", w)));
  if (body.retentionHoldersAlreadyStrandedCount) {
    out.appendChild(el("div", "warn-text small",
      body.retentionHoldersAlreadyStrandedCount + " retention-class holder key(s) are ALREADY tombstoned from a prior run — " +
      "their class key can never be destroyed by ShredRetentionClassKey. Pre-existing platform damage, not caused by this operation; escalate."));
  }
  [["create", body.createdKeys], ["update", body.updatedKeys], ["tombstone", body.tombstonedKeys]]
    .forEach(([verb, keys]) => {
      (keys || []).forEach((k) => {
        const line = el("div", "pkg-delta small");
        line.appendChild(el("span", "muted", verb + " "));
        line.appendChild(keyLinkEl(k, "small"));
        out.appendChild(line);
      });
    });
  return out;
}

// openApplyModal drives the shared install/upgrade upload flow: pick
// manifest.yaml → Preview (a server dry-run — the confirm step shows the
// exact create/update/tombstone delta) → Apply. Exported for the Packages
// list's toolbar Install action.
function openApplyModal(opts) {
  let inFlight = false;
  const { modal, close } = modalShell(opts.title, () => [input, force, preview, apply, cancel], () => inFlight);

  if (opts.intro) modal.appendChild(el("p", "muted", opts.intro));
  const input = el("input");
  input.type = "file";
  input.multiple = true;
  modal.appendChild(input);
  const force = el("input");
  force.type = "checkbox";
  if (opts.showForce !== false) {
    const forceRow = el("label", "muted small");
    forceRow.appendChild(force);
    forceRow.appendChild(document.createTextNode(
      " force — re-apply changed bodies at the same version (dev refresh)"));
    modal.appendChild(forceRow);
  }

  const actions = el("div", "modal-actions");
  const cancel = el("button", null, "Cancel");
  const preview = demoHide(el("button", null, "Preview (dry-run)"));
  const apply = demoHide(el("button", "danger-btn", "Apply"));
  apply.disabled = true; // preview first — the delta IS the confirm
  actions.appendChild(cancel);
  actions.appendChild(preview);
  actions.appendChild(apply);
  modal.appendChild(actions);
  const out = el("div", "pkg-modal-out");
  modal.appendChild(out);

  cancel.addEventListener("click", () => { if (!inFlight) close(); });
  // Changing the files OR the force flag invalidates the previewed delta —
  // Apply disarms until the next preview (the delta IS the confirm).
  input.addEventListener("change", () => { apply.disabled = true; out.innerHTML = ""; });
  force.addEventListener("change", () => { apply.disabled = true; });

  const submit = async (dryRun) => {
    const files = Array.from(input.files || []);
    const names = files.map((f) => f.name);
    const pick = manifestCandidate(names);
    if (!pick) {
      out.innerHTML = "";
      out.appendChild(el("div", "error-text", files.length
        ? "multiple files selected but none named manifest.yaml"
        : "select the package's manifest.yaml first"));
      return null;
    }
    const fd = new FormData();
    files.forEach((f) => fd.append("files", f, f.name));
    fd.append("force", force.checked ? "true" : "false");
    fd.append("dryRun", dryRun ? "true" : "false");
    inFlight = true;
    [preview, apply, cancel, input, force].forEach((b) => { b.disabled = true; });
    out.innerHTML = "";
    out.appendChild(el("div", "muted small", dryRun ? "previewing…" : "applying…"));
    const body = await api(opts.endpoint, { method: "POST", body: fd });
    inFlight = false;
    cancel.disabled = false;
    preview.disabled = false;
    input.disabled = false;
    force.disabled = false;
    out.innerHTML = "";
    out.appendChild(renderApplyReply(body));
    return body;
  };

  preview.addEventListener("click", async () => {
    const body = await submit(true);
    // A previewed skip has nothing to apply; anything else arms the button.
    apply.disabled = !body || !!body.error || !!body.skipped;
  });
  apply.addEventListener("click", async () => {
    const body = await submit(false);
    apply.disabled = true;
    if (body && !body.error) {
      toast(applySummaryLine(body));
      if (opts.onDone) opts.onDone(body);
    }
  });
}

// openUninstallModal: destructive-styled typed confirm ("type the package
// name") + the resolved-contents summary of what will be tombstoned.
function openUninstallModal(pkg) {
  const token = pkg.name || pkg.key;
  let inFlight = false;
  const { modal, close } = modalShell("Uninstall package", () => [input, cancel, confirm], () => inFlight);

  modal.appendChild(el("p", "muted",
    "Soft-deletes this package's declared keys — " + uninstallSummary(pkg) +
    ". Vertices stay queryable for audit. Type the package name to confirm:"));
  modal.appendChild(el("div", "cid", token));
  const input = el("input");
  input.type = "text";
  input.placeholder = token;
  modal.appendChild(input);
  const actions = el("div", "modal-actions");
  const cancel = el("button", null, "Cancel");
  const confirm = demoHide(el("button", "danger-btn", "Uninstall"));
  confirm.disabled = true;
  actions.appendChild(cancel);
  actions.appendChild(confirm);
  modal.appendChild(actions);
  const msg = el("div", "small");
  modal.appendChild(msg);
  input.focus();

  cancel.addEventListener("click", () => { if (!inFlight) close(); });
  input.addEventListener("input", () => {
    confirm.disabled = !deleteConfirmReady(input.value, token);
  });
  confirm.addEventListener("click", async () => {
    inFlight = true;
    confirm.disabled = true;
    cancel.disabled = true;
    input.disabled = true;
    msg.className = "muted small";
    msg.textContent = "uninstalling…";
    const body = await api("/api/packages/uninstall", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: pkg.name }),
    });
    inFlight = false;
    if (body.error) {
      msg.className = "error-text small";
      msg.textContent = "uninstall failed: " + body.error;
      cancel.disabled = false;
      input.disabled = false;
      return;
    }
    // Render the full reply — the tombstoned key list, linkified (dimmed;
    // they are soft-deleted now) — before the operator closes out.
    const keys = body.tombstoned || [];
    const preserved = body.retentionHoldersPreserved || [];
    const stranded = body.retentionHoldersAlreadyStranded || [];
    msg.className = "small";
    msg.innerHTML = "";
    msg.appendChild(el("div", null, "uninstalled — " + keys.length + " key(s) tombstoned" +
      (body.note ? " (" + body.note + ")" : "")));
    // The keys the uninstall deliberately held back. Naming them here is the
    // only place an operator learns the package's retention custody outlived
    // the package; the tombstone list below would otherwise read as complete.
    if (preserved.length) {
      msg.appendChild(el("div", null, preserved.length + " retention-class holder key(s) preserved (not tombstoned) — " +
        "still destroyable via ShredRetentionClassKey: " + preserved.join(", ")));
    }
    if (stranded.length) {
      msg.appendChild(el("div", "warn-text", stranded.length + " retention-class holder key(s) are ALREADY tombstoned from a prior run — " +
        "their class key can never be destroyed by ShredRetentionClassKey. Pre-existing platform damage, not caused by this uninstall; escalate: " + stranded.join(", ")));
    }
    const keyBox = el("div", "pkg-modal-out pkg-item-deleted");
    keys.forEach((k) => {
      const line = el("div", "pkg-delta small");
      line.appendChild(el("span", "muted", "tombstone "));
      line.appendChild(keyLinkEl(k, "small"));
      keyBox.appendChild(line);
    });
    msg.appendChild(keyBox);
    input.remove();
    cancel.disabled = false;
    cancel.textContent = "Close";
    cancel.addEventListener("click", () => {
      toast("package " + body.packageName + " uninstalled");
      navigate("#/packages");
    });
  });
}

function init() {}

export { init, enter, leave, closeModal, openApplyModal };
