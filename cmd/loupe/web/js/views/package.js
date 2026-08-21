// Package detail page (#/package/<key>, design §9.2–9.3): header + MANIFEST
// panel + CONTENTS (the graph-resolved declared entities, every item a
// keyLink; lenses also link their #/lens page) + LIFECYCLE (upgrade/refresh
// behind a dry-run preview, uninstall behind a typed confirm). Also exports
// openApplyModal — the shared install/upgrade upload flow the Packages list's
// toolbar Install action reuses.

import { $, el, demoHide, api, setStatus, toast } from "../api.js";
import { navigate, replaceRoute } from "../router.js";
import {
  manifestCandidate, applySummaryLine, uninstallSummary, uninstallResultLines,
  uninstallAttestationPrompts, attestationSubjectLine, uninstallRetryBody,
} from "../logic/pkg.js";
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
//
// Two steps, because the server has two answers. The first POST carries the
// name alone. If the package holds a Secure Lens whose key-custody record this
// uninstall would erase from the destruction-readiness oracle's view, the
// server refuses with 409 and names every such lens in
// `unattestedSecureLenses`; the modal then asks for one required note per lens
// — the operator's attestation that the ciphertext those columns encrypted is
// safe to stop tracking — and re-submits. Which lenses to ask about and when
// the answers are complete are both decided in logic/pkg.js
// (uninstallAttestationPrompts / uninstallRetryBody), goja-tested; this view
// is the DOM around them.
function openUninstallModal(pkg) {
  const token = pkg.name || pkg.key;
  let inFlight = false;
  // The operator's typed notes, keyed by the lens spec key they answer, and
  // OUTSIDE renderAttestation: a second refusal (or a substrate blip on the
  // re-submit) re-renders the step, and retyping an attestation the operator
  // already wrote is both an invitation to write a shorter one and a way to
  // lose the considered wording of the first.
  const noteByKey = {};
  let activePrompts = [];
  let noteInputs = [];
  let attestBtn = null;
  // Filled by the attestation step; modalShell calls focusables() per Tab, so
  // fields that appear mid-flow join the focus trap without rebuilding it.
  let attestFocusables = [];
  const { modal, close } = modalShell("Uninstall package",
    () => [input, cancel, confirm].concat(attestFocusables).filter((x) => x && x.isConnected),
    () => inFlight);

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
  // Two children, never one: transient status ("uninstalling…", a failure) has
  // its own node, so writing it can never clobber the attestation fields — or
  // the submit button — the operator is in the middle of filling in.
  const msg = el("div", "small");
  const statusLine = el("div", "muted small");
  const outBox = el("div");
  msg.appendChild(statusLine);
  msg.appendChild(outBox);
  modal.appendChild(msg);
  input.focus();

  const setModalStatus = (text, cls) => {
    statusLine.className = cls;
    statusLine.textContent = text;
  };
  const notesInPromptOrder = () => activePrompts.map((p) => noteByKey[p.key] || "");
  // setBusy drives only controls that are still in the DOM, and never enables
  // a submit its own readiness rule says is not ready.
  const setBusy = (busy) => {
    cancel.disabled = busy;
    if (input.isConnected) input.disabled = busy;
    if (confirm.isConnected) confirm.disabled = busy || !deleteConfirmReady(input.value, token);
    noteInputs.forEach((n) => { n.disabled = busy; });
    if (attestBtn) {
      attestBtn.disabled = busy || !uninstallRetryBody(pkg.name, activePrompts, notesInPromptOrder());
    }
  };

  cancel.addEventListener("click", () => { if (!inFlight) close(); });
  input.addEventListener("input", () => {
    confirm.disabled = !deleteConfirmReady(input.value, token);
  });

  // renderDone renders a committed uninstall — the tombstoned key list,
  // linkified (dimmed; they are soft-deleted now) — before the operator closes
  // out. The classification of every other line (which population is benign,
  // which is escalate-this, what the attestation retired) lives in
  // uninstallResultLines — pure, and goja-tested.
  const renderDone = (body) => {
    const keys = body.tombstoned || [];
    setModalStatus("", "muted small");
    outBox.innerHTML = "";
    outBox.appendChild(el("div", null, "uninstalled — " + keys.length + " key(s) tombstoned" +
      (body.note ? " (" + body.note + ")" : "")));
    uninstallResultLines(body).forEach((line) => {
      outBox.appendChild(el("div", line.warn ? "warn-text" : null, line.text));
    });
    const keyBox = el("div", "pkg-modal-out pkg-item-deleted");
    keys.forEach((k) => {
      const line = el("div", "pkg-delta small");
      line.appendChild(el("span", "muted", "tombstone "));
      line.appendChild(keyLinkEl(k, "small"));
      keyBox.appendChild(line);
    });
    outBox.appendChild(keyBox);
    input.remove();
    noteInputs = [];
    attestBtn = null;
    attestFocusables = [];
    cancel.disabled = false;
    cancel.textContent = "Close";
    cancel.addEventListener("click", () => {
      toast("package " + body.packageName + " uninstalled");
      navigate("#/packages");
    });
  };

  // renderAttestation is the second step: one note field per lens the server
  // named, each showing the columns and holder types the operator is attesting
  // ABOUT — an attestation nobody can see the subject of is not one. A lens
  // whose canonicalName the server could not read gets the CLI remedy instead
  // of a field, since the attestation is keyed by that name. Fields are seeded
  // from noteByKey, so a re-render keeps whatever was already typed.
  const renderAttestation = (prompts, reason) => {
    activePrompts = prompts;
    noteInputs = [];
    setModalStatus("uninstall refused: " + reason, "error-text small");
    outBox.innerHTML = "";
    input.remove();
    confirm.remove();
    attestBtn = demoHide(el("button", "danger-btn", "Attest & uninstall"));
    attestFocusables = [];
    prompts.forEach((p) => {
      const box = el("div", "pkg-modal-out");
      box.appendChild(el("div", null, p.lens || p.key));
      box.appendChild(el("div", "muted small", attestationSubjectLine(p)));
      box.appendChild(el("div", "muted small", p.key));
      if (p.resolvable) {
        const note = el("input");
        note.type = "text";
        note.placeholder = "why this history is safe to stop carrying (required)";
        note.value = noteByKey[p.key] || "";
        note.addEventListener("input", () => { noteByKey[p.key] = note.value; setBusy(false); });
        box.appendChild(note);
        noteInputs.push(note);
        attestFocusables.push(note);
      } else {
        box.appendChild(el("div", "warn-text small",
          "this lens's canonicalName could not be read from Core KV — attest it from the CLI, " +
          "which takes the name the installed package declares"));
      }
      outBox.appendChild(box);
    });
    attestFocusables.push(attestBtn);
    const attestActions = el("div", "modal-actions");
    attestActions.appendChild(attestBtn);
    outBox.appendChild(attestActions);
    attestBtn.addEventListener("click", () => {
      const retry = uninstallRetryBody(pkg.name, activePrompts, notesInPromptOrder());
      if (retry) attempt(retry);
    });
    setBusy(false);
    // Focus the first field there is something to type in. With every lens
    // unresolvable the submit can never arm, and focusing a permanently
    // disabled button strands the operator on a control that does nothing.
    if (noteInputs.length) noteInputs[0].focus();
    else cancel.focus();
  };

  // attempt posts one uninstall payload and routes the three outcomes: a
  // refusal that names lenses to attest, any other failure, or a commit.
  const attempt = async (payload) => {
    inFlight = true;
    setBusy(true);
    setModalStatus("uninstalling…", "muted small");
    const body = await api("/api/packages/uninstall", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    });
    inFlight = false;
    if (body.error) {
      const prompts = uninstallAttestationPrompts(body);
      if (prompts.length) { renderAttestation(prompts, body.error); return; }
      setModalStatus("uninstall failed: " + body.error, "error-text small");
      setBusy(false);
      return;
    }
    renderDone(body);
  };

  confirm.addEventListener("click", () => { attempt({ name: pkg.name }); });
}

function init() {}

export { init, enter, leave, closeModal, openApplyModal };
