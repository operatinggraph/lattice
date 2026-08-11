# The fire brief — just-in-time story compilation (Phase 0 of every build fire)

**Why this exists.** Design docs are outcome-level *on purpose* — file-level detail written at design time
rots before build time. But a builder handed only an outcome re-discovers its scope inside the session that
edits code, which produces the three observed failure modes: scope rediscovery every fire, sideways drift
(scope negotiated mid-build with no reviewed gate), and residuals filed mid-build that a scoping pass would
have caught before the first edit. The fire brief is the old `create-story` artifact reborn **just-in-time**:
compiled when the fire is *selected*, minutes before building, so the builder executes instead of discovers.

**When.** Mandatory for **M+ fires and any multi-package or security/capability-plane fire regardless of
size**. An XS/S single-file fire may compress the brief to an in-context checklist (proportionality — don't
bureaucratize a coverage fix), but the **scope-diff gate applies at every size**. **One brief per ITEM,
compiled when the item is first selected — never per resume (Andrew, 2026-08-08):** resuming an in-flight 🏗️
item runs a **delta-scout** (re-verify the checkpoint's next increment's touch-list live) whose findings ride
the increment's own commit; recompiling a full committed brief per resume is the old per-story ceremony
creeping back.

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
   package version bumps, `provision-readpath`, hot-reload vs restart, full-suite triggers, …) — **plus,
   copied in verbatim: the touched components' "Review keeps catching" dossier entries**
   (`docs/components/<c>.md`, the section at the end) **and the standing checklist below**. The dossier is
   how a prior review's findings reach this builder; a brief that skips it re-purchases them.
6. **Adjacent finds** — everything discovered that is out of the FIRE's scope. Each one is either
   **absorbed into this run's batch as its own unit** (the default: the run fixes what it finds; the fire's
   scope stays narrow while the batch grows), or filed ONLY under one of the two outs — **needs Andrew**, or
   **needs a designer pass** — with the out stated on the row. There is no third path and no "file for
   later" (`agents/steward/SKILL.md` §4). Listing the finds here is scoping, not filing.
7. **Non-goals** — what the fire deliberately does not touch (the drift fence).

## The standing checklist (copied into every brief's part 5)

The cross-component failure classes review keeps re-finding — **capped at six lines**: an entry retires when
a lint/test gate mechanizes it, and a new one must displace an old one past six (a checklist that grows
stops being walked). The builder walks it before the first edit; the reviewers walk it after.

1. **New state needs a LIFETIME, not a data structure** — before building any registry / cache / latch /
   watch / accumulated set, write its state table: created / reset / carried / ordered at every boundary
   (crash, replay, reconnect, tombstone, upgrade). "Track it as we go" names a mechanism where a rule belongs.
2. **Every census is a premise** — re-run any stated count live before relying on it, and write predicates
   over the enumerated state table, never one clause over a multi-shape set.
3. **A negative test needs its positive vector proven first**, and every fix is proven by reverting it and
   watching its test fail — a test that passes with the mechanism disabled pins nothing.
4. **Removal needs a transport AND an observer, and a demoted mechanism needs EVERY obligation
   enumerated** — read what each consumer actually tests, at what granularity; an upsert-only writer
   retracts nothing whose key drops out. When a mechanism is *replaced* rather than deleted, list
   everything it was silently doing and account for each: finding one obligation and moving on is the
   same defect wearing a smaller hat (cold-sign-in Fire 2 — the design found the server ack floor's
   poison-disposal job, missed that it also held the resume position behind un-acked holes, and shipped a
   permanent-skip path that only cold review caught).
5. **One deterministic key, one writer** — a create-only writer bricks the second; a second writer needs an
   explicit arbitration or a single owner, decided before it is added.
6. **Precedent may carry debt** — verify a mirrored pattern against the rule it claims to follow before
   copying it; "the neighbor does it" is not grounding.

## The scope-diff gate (before the first edit)

Diff parts 2–4 against part 1, **item-by-item**: every touch must trace to the scope sentence. The brief may
**narrow** to the ratified scope; it may never widen it or **substitute an adjacent mechanism** (the
claim≠login lesson). Also re-verify declared dependencies **both ways**: a listed dependency that is not
load-bearing for *this* green bar is noted and dropped; an unlisted one that is → stop and resequence.
Divergence you cannot resolve by narrowing → route per the Steward's §0 (decide-don't-defer), never build
through it silently. **Every census/count the design states is a PREMISE:** re-run the design's own
executable census (its command + expected count — the designer skill requires the pair) live and pin the
number; a mismatch is resolved here, before the first edit, never left for the admit review to trip over
(trialed 2026-08-09: a ratified "four label-equality sites" was six — all three cold reviewers found the
same two misses independently, both failing toward silent over-grant).

## Placement + lifecycle

Append the brief to the owning design doc as `### <fire> fire brief (build note, <date>)` and **commit it
(docs-only, in `main`) before opening the worktree** — it must survive session death. A small item with no
design doc carries its brief in the commit-message body instead. During the build the brief is the
checklist; any deviation gets one appended line in the build note (what changed, why) — that is the drift
record the admit review reads. On a multi-fire item this commit-before-code applies to the item's **first**
brief only; resume delta-scout notes and checkpoint amendments ride the increment's own commit.

## Builder economics

With a complete brief the builder is **executing, not discovering**: mechanical increments may run as
builder sub-agents on a **cheaper model tier**; judgment-heavy increments (naming, security posture, UX
taste) stay with the lead. A brief whose builder repeatedly stalls mid-increment is a **brief-quality
defect** — fix the compilation, don't silently widen the build.

**Builder conduct — finds go up, not out.** A builder that hits an out-of-scope defect **reports it in its
final report** for Winston to triage; it does **not** `spawn_task` a user-facing chip, edit the board, or
widen its own scope. Winston files the board row (canonical demand) and, only if a one-click convenience is
wanted, spawns a chip whose prompt **names the skill to run** (`/steward <stream>` + the row). Dispatched
builder prompts should state this explicitly. See [[feedback_chip_prompts_name_the_skill]].
