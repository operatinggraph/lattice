# The fire brief — just-in-time story compilation (Phase 0 of every build fire)

**Why this exists.** Design docs are outcome-level *on purpose* — file-level detail written at design time
rots before build time. But a builder handed only an outcome re-discovers its scope inside the session that
edits code, which produces the three observed failure modes: scope rediscovery every fire, sideways drift
(scope negotiated mid-build with no reviewed gate), and residuals filed mid-build that a scoping pass would
have caught before the first edit. The fire brief is the old `create-story` artifact reborn **just-in-time**:
compiled when the fire is *selected*, minutes before building, so the builder executes instead of discovers.

**When.** Mandatory for **M+ fires and any multi-package or security/capability-plane fire regardless of
size**. An XS/S single-file fire may compress the brief to an in-context checklist (proportionality — don't
bureaucratize a coverage fix), but the **scope-diff gate applies at every size**.

**Who produces it.** The activator (Winston / the Steward) fans out **one or more READ-ONLY scouts** —
generic sub-agents (Read/Grep/Glob + read-only git only; **no make, no docker, no builds/tests, no writes**)
— over the code the fire touches, then compiles their reports into the brief and runs the gate itself.
Scouts are *not* roles: the owning roles (`owner`, `fe-engineer`, …) remain skills followed inline.

## Shape (seven parts)

1. **Scope sentence** — the fire's ratified scope + green bar, VERBATIM from the design doc / board row.
2. **Verified touch-list** — every file to edit or create, with `file:line` anchors **checked live now**.
   A design doc's citations are leads, not truth: re-verify each; note any that rotted.
3. **Precedents to mirror** — for each edit site, the specific shipped pattern (`file:line`) it copies.
   "Greenfield" requires one sentence on why no precedent exists (the mirror-don't-greenfield check).
4. **Increment order** — sequenced increments, each with its own green check; the fire's green bar turned
   into **runnable commands** (test invocations, curls), not prose.
5. **In-scope gotchas** — the CLAUDE.md / memory / design-doc obligations THIS fire trips (lockstep rules,
   package version bumps, `provision-readpath`, hot-reload vs restart, full-suite triggers, …).
6. **Adjacent finds** — everything discovered that is out of scope: **file each as a board row NOW**, before
   the first edit (or state why one is deliberately not filed). Pre-build filing is the healthy half of
   residual discovery; mid-build filings should become rare.
7. **Non-goals** — what the fire deliberately does not touch (the drift fence).

## The scope-diff gate (before the first edit)

Diff parts 2–4 against part 1, **item-by-item**: every touch must trace to the scope sentence. The brief may
**narrow** to the ratified scope; it may never widen it or **substitute an adjacent mechanism** (the
claim≠login lesson). Also re-verify declared dependencies **both ways**: a listed dependency that is not
load-bearing for *this* green bar is noted and dropped; an unlisted one that is → stop and resequence.
Divergence you cannot resolve by narrowing → route per the Steward's §0 (decide-don't-defer), never build
through it silently.

## Placement + lifecycle

Append the brief to the owning design doc as `### <fire> fire brief (build note, <date>)` and **commit it
(docs-only, in `main`) before opening the worktree** — it must survive session death. A small item with no
design doc carries its brief in the commit-message body instead. During the build the brief is the
checklist; any deviation gets one appended line in the build note (what changed, why) — that is the drift
record the admit review reads.

## Builder economics

With a complete brief the builder is **executing, not discovering**: mechanical increments may run as
builder sub-agents on a **cheaper model tier**; judgment-heavy increments (naming, security posture, UX
taste) stay with the lead. A brief whose builder repeatedly stalls mid-increment is a **brief-quality
defect** — fix the compilation, don't silently widen the build.
