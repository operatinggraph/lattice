# Loupe agent-activity console — design proposal

> **Status:** ✅ **Winston-ratified — build-ready** (2026-06-25). All four open questions (§9) were
> implementation / design calls — **no frozen-contract change, no architectural fork** — so Winston ratified
> them per *decide-don't-defer* (`agentic-ops-design.md` §6.1.1) rather than parking them on Andrew. The
> earlier "📐 awaiting Andrew ratification" status was the timidity bug in the flesh: the loop did the hard
> part (ground → design → adversarial review) then handed the *easy* part (deciding) upward. Decisions are
> recorded in §9; **Increment 1 (data layer) is L2-buildable now.** Authored by Winston (Steward fire,
> 2026-06-25).
> Backlog item: *Loupe agent-activity console* (★★★, M) — Refinements & ops table + the *Now / experience
> layer* section. Layers onto the shipped live system-map landing view.

---

## 1. What this is

The **ops layer** that sits on top of Loupe's live system-map landing page. The system map answers *"is the
**platform** healthy right now?"*; the agent-activity console answers *"what are the **autonomous agents**
doing, and what needs **me (Andrew)**?"* — in 30 seconds, per `agentic-ops-design.md` §7:

> what shipped overnight · what's waiting on his contract-review · what's stuck · what the Steward is about to do.

It is the operator surface for the agentic operating model and, critically, **Andrew's L3 touchpoint** — the
one place the contract-review queue is rendered as *what changed / why / which consumers it affects*, not a
raw pile of uncommitted diffs. Per §7: *"Good L3 UX is where last-inch autonomy is won or lost."*

It renders into the **marked** `#sysmap-console` right-rail slot (system-map UX §6). *Marked*, not pre-built:
verified against the code, the slot today is **a comment** (`index.html:38`) inside the `#sysmap-main`
wrapper, and `style.css:374` hardcodes `grid-template-columns: 1fr` (no second column). So Increment 2 must
**create** the `<aside id="sysmap-console">`, add the `1fr 320px` grid column, and add the responsive
collapse — a small, contained FE change, not a zero-line drop-in. What *is* genuinely pre-built and reusable:
the single `refreshSystemMap()` 10s clock (`app.js:554`, confirmed) the console extends rather than racing,
and the `kind`/`status` lookup-table rendering (relevant mostly to the agent *chips*, since panes #2–#4 are a
stacked rail list, not map nodes).

## 2. Grounding — what already exists (do not redesign)

- **Live system map** — `GET /api/systemmap` + `computeSystemMap` (`cmd/loupe/systemmap.go`) and the FE
  landing view (`cmd/loupe/web/app.js`, `#panel-systemmap`). Self-truthing from Health KV + Core KV.
- **Reserved console slot** — `#sysmap-main` holds the stage; `#sysmap-console` is the empty reserved
  `<aside>` that becomes the second grid column (`1fr 320px`). Node rendering is `kind`-agnostic
  (`node.kind` + `node.status` lookup tables), so a `kind:"agent"` node is a new lookup row, not new code.
  A single `refreshSystemMap()` clock (10s) is meant to be *extended* to also pull the console's queues.
- **Health-KV read path** — `handleHealth` / `handleSystemMap` (`cmd/loupe/server.go`) list the
  `health-kv` bucket via `conn.KVListKeys` + `conn.KVGet` and decode JSON docs. `classifyHealthKey`
  (`cmd/loupe/health.go`) already classifies `health.<comp>.<inst>` (no further dots) as a component
  heartbeat — **so `health.agent.steward` already classifies as a component named `agent`, instance
  `steward`, today, with zero classifier changes.** Components stamp `heartbeatAt`; staleness is a yellow
  flag past `staleThreshold`.
- **Dogfooding decision (ratified #8)** — "the ops agents emit Health KV like any component, so Loupe
  watching the platform watches the agents for free."
- **Canonical Health schema** — `docs/observability/health-kv-schema.md`. Any new emission shape must
  co-update it in the same change (gate, §3/§5 of the agentic-ops design).
- **The board + proposals + contract edits are markdown/git artifacts** — the Progress board lives in
  `_bmad-output/planning-artifacts/backlog.md`; design proposals carry a `📐 awaiting-Andrew-ratification`
  marker (this doc is one); in-flight contract changes live **uncommitted in the working tree** (house
  convention: "contract changes = modify in-place UNCOMMITTED until ratified"). **There is no
  machine-readable status stream for any of this today.**
- **The dependency map (agentic-ops §8 item #5) is NOT built.** The L3 "affected-consumers" view is
  *designed* to be driven by a code-derived `consumer → producers` map. This console depends on it for the
  rich affected-consumers rendering — see §7.

## 3. The four data classes the console shows

| # | Pane | Question it answers | Natural source |
|---|---|---|---|
| 1 | **Per-agent health** | Is the Steward / Lamplighter / PO alive? When did it last fire? Is it stuck mid-fire? | **Health KV** — *the only real-time pane* |
| 2 | **Board state** | What is 🏗️ Active, 📋 next, ✅ recently done (as last **committed** to `main`) | **The board** (`backlog.md`, committed) |
| 3 | **L3 contract-review queue** *(Andrew's touchpoint)* | What's waiting on a ratify decision, what changed, why, who it affects | **📐-flagged proposals** + **uncommitted contract edits in `main`** |
| 4 | **Recent activity** | What shipped overnight (last N commits, by agent) | **git log** (`main`) |

The split is the crux: **#1 is runtime state with no file home → Health KV. #2–#4 are canonical markdown/git
artifacts.** Mirroring #2–#4 into KV would create a *dual-write* — the exact drift the self-truthing
principle (decision #5) warns against. That drives §4.

**A consequence to be honest about (it reshapes the priorities):** owners and ops routines do their work in
**isolated worktrees** (agentic-ops §3), and the board is committed to `main` by Winston/the Steward — not
written from inside a parallel worktree. So the repo seam (panes #2–#4) sees **committed `main` state only**;
*live, in-a-worktree-right-now* work is invisible to it by design. The single exception is uncommitted
**contract edits**, which the house rule keeps uncommitted *in `main`* — so pane #3's contract-edit signal
works, but pane #2 shows queued/committed board state, **not** "what an owner is doing this second." That
real-time "what's the Steward doing / is anything stuck" signal — the thing §7 says the console is *for* —
comes **only from pane #1 (Health KV)**. Pane #1 is therefore the load-bearing real-time pane, not a
nice-to-have liveness chip; §5.1 designs it accordingly.

## 4. The decision that needs ratification — where #2–#4 come from

Loupe is, today, a **pure NATS client**: every `/api/*` handler reads Core KV / Health KV and nothing else.
The console's #2–#4 data has no NATS home. Three ways to give it one:

### Option A — everything through KV (agents push board + review state into a bucket)
Agents write the board snapshot, review queue, and their health into `health-kv` (or a new `agentic-ops`
bucket) every fire. Loupe stays pure-NATS; the existing read path extends trivially.
- ➖ **Dual-write drift.** `backlog.md` stays the source of truth the agents *actually* maintain (the house
  rule tracks progress in the board + git, *never* in a side channel). A KV mirror is a second copy that
  goes stale the moment a fire updates the board but dies before pushing — precisely the failure the
  self-truthing map was built to avoid.
- ➖ Heaviest agent-side work; couples every role-skill to a KV-emit step for *board* state, not just health.

### Option B — Loupe gains a local repo read-seam (filesystem + git)
Loupe reads `backlog.md`, scans `_bmad-output/implementation-artifacts/*.md` for the `📐` marker, and shells
`git log` / `git diff` for uncommitted contract edits + recent commits. Agent health still via Health KV.
- ➕ **Single source of truth** — the board markdown the agents already maintain *is* the data; no mirror,
  no drift.
- ➕ Zero new agent-side work for #2–#4.
- ➖ Breaks Loupe's pure-NATS-client property; couples it to repo paths + assumes Loupe runs *in the repo*.
- ◑ **But that assumption already holds**: Loupe is an explicitly *local, trusted, single-identity dev
  tool* that binds `127.0.0.1`, reads a local `lattice.bootstrap.json`, and is launched from the repo
  (`make run-loupe`). A local repo read-seam is consistent with its trust + host model.

### Option C — hybrid *(recommended)*
- **#1 per-agent health → Health KV.** Agents emit a `health.agent.<role>` heartbeat at fire boundaries
  (start + end). Dogfoods decision #8, flows through the existing classifier unchanged, and gives the
  *runtime liveness/stuck* signal a file can't (a board snapshot can't tell you the Steward died mid-fire).
- **#2–#4 board / review-queue / activity → a Loupe local repo read-seam (Option B).** The canonical
  markdown + git stay the single source of truth; Loupe reads them directly.

**Recommendation: C.** It puts each data class where its truth already lives — runtime liveness in Health KV
(self-truthing, dogfoods the watch), and the board/review/activity in the markdown+git the agents already
maintain (no dual-write drift). The only thing that genuinely needs Andrew's blessing is **introducing a
filesystem/git read-seam into Loupe** — a deliberate softening of its pure-NATS-client property, justified
by its local-trusted-tool nature but worth an explicit yes.

**Guardrails on the read-seam (part of the proposal):**
- Gated behind `LOUPE_REPO_ROOT`; when unset, resolved via `git rev-parse --show-toplevel` (not raw CWD —
  see §5.4). **Unresolvable / not-the-Lattice-repo / unreadable / parsed-wrong → the console's #2–#4 panes
  degrade to a clean "repo data unavailable | parse-error" state**, exactly like a NATS-down `/api/*` returns
  a JSON error rather than crashing (main.go's stated robustness contract).
- **Read-only.** The seam never writes. `git` is invoked read-only (`log`, `diff --stat`, `status
  --porcelain`); no working-tree mutation. Bounded output (capped commit count, capped diff size).
- The seam is **its own package** (`cmd/loupe/repo` or a `repoSource` interface on `server`) so it's
  unit-testable against a fixture dir and trivially stubbable (mirrors how `computeHealth` takes injected
  `readEntry` / `resolveLens` rather than reaching for NATS directly).

## 5. Concrete shape (Option C)

### 5.1 Agent Health-KV emission + liveness model (#1) — the part that needs design care

Agents are **Claude scheduled-task fires**, not daemons: an agent is an LLM turn that runs every few hours
(hourly `ScheduleWakeup` hops; the PO staggered ~3h), holds **no persistent NATS connection**, and shells out
to do I/O. The daemon liveness model (a 60s heartbeat; silence past `staleThreshold` = stale) **does not
fit** — naively reused it would paint every agent permanently yellow (verified: `staleThreshold = 60s`,
`server.go:26`, shared by `computeHealth` *and* `computeSystemMap`). So pane #1 needs its own freshness rule.

**Doc shape — `health.agent.<role>`** (a *superset* of the Contract-#5 component shape, with interval fields):

```json
{
  "component":   "steward",
  "instance":    "steward",
  "kind":        "agent",
  "heartbeatAt": "2026-06-25T07:30:00Z",
  "status":      "idle | firing",
  "phase":       "design fire — no build item ready",
  "lastFireStartedAt":  "2026-06-25T07:02:00Z",
  "lastFireEndedAt":    "2026-06-25T07:30:00Z",
  "nextFireExpectedAt": "2026-06-25T10:30:00Z",
  "currentItem": "Loupe agent-activity console (design)"
}
```

**Freshness rule (agent-specific — the design decision):** an agent is
- **green / idle** when `status:"idle"` and `now < nextFireExpectedAt` (it ran, finished cleanly, isn't due
  yet — *idle is healthy for an interval worker*),
- **green / firing** when `status:"firing"` and the fire started recently (within an expected-fire-duration
  budget),
- **yellow / overdue** when `now > nextFireExpectedAt` by more than a grace margin (missed its expected next
  fire — the real "is it still scheduled?" signal),
- **yellow / stuck** when `status:"firing"` *and* `now − lastFireStartedAt` exceeds a max-fire-duration
  budget (a fire that started and never wrote its end heartbeat),
- **grey / absent** when the key is missing entirely — see the honest-semantics note below.

This means **the classifier needs no change for *grouping*** (`health.agent.steward` already → component
`agent`/`steward`), but pane #1 needs a **separate freshness path** keyed on `kind:"agent"` /
`nextFireExpectedAt` rather than the shared 60s `staleThreshold`. The earlier "freshness logic just works"
claim was wrong; this is the correction. Implement it as a `computeAgentHealth` that reads the interval
fields, *not* by calling `computeHealth` — keep the daemon path untouched.

**Emission mechanism — `lattice health emit-agent <role> --status … --next-fire …`** (a CLI verb; preferred
over a raw `nats kv put` convention — discoverable, one obvious place, unit-testable). The role-skills call
it at **fire start** (`status:"firing"`) and **fire end** (`status:"idle"`, stamping `nextFireExpectedAt`).
Hard requirements that make this safe for a scheduled fire:
- **Never fail the fire.** If NATS is unreachable (the `lattice` binary not on PATH, or `make up-full` not
  running — the *common* dev case for an overnight fire), the verb logs and exits 0. Health emission is
  best-effort telemetry, never a gate on the agent's actual work.
- **Honest `absent` semantics.** A missing `health.agent.<role>` key means *either* "never emitted" *or*
  "the stack was down when it last fired" — Loupe renders **grey "no recent signal"**, explicitly **not red
  "dead."** The console must not cry wolf at 3am when the stack is simply down (the normal state between
  demos). This is the key correction over the first draft, which would have shown agents as failed exactly
  when no one is watching.
- **Acknowledged limit (not hidden):** "stuck" only catches a fire that died *between* start-emit and
  end-emit. A fire that crashes *before* the start-emit (during sense/select) is indistinguishable from
  "didn't fire" → it shows as `absent`/`overdue`, not `stuck`. We accept this; tighter liveness would need a
  watchdog process the scheduled-task model doesn't have.
- **Co-update `docs/observability/health-kv-schema.md`** with the `health.agent.*` shape + the agent
  freshness rule in the same change (the Health-emission gate). The `health.agent.*` keys are a non-breaking
  addition under the existing key convention.

### 5.2 Repo read-seam (#2–#4)
A `repoSource` with three read methods, each independently degradable. **Parsing is brittle by nature — the
board is hand-edited prose, not a schema — so a parse that doesn't match the expected shape degrades to
`repo:"parse-error"` (see §5.3) rather than rendering garbage rows.** "Degrade" covers both *unreadable* and
*parsed-wrong*; the console never shows confidently-wrong board state.
- `Board()` → parses the `backlog.md` Progress board table into `{item, status, ref}` rows + the themed
  tables' 📋 items. Best-effort markdown-table parse, tolerant of extra/reordered columns; a header-row
  mismatch → `parse-error`, not silent misalignment.
- `ReviewQueue()` → `{title, summary, kind: "proposal"|"contract-edit", path, affectedConsumers[]}`:
  - **proposals:** scan `_bmad-output/implementation-artifacts/*.md` for the **pinned marker string
    `📐 awaiting-ratification`** (the canonical form already used by `agentic-ops-design.md` — *not* the
    `awaiting-Andrew-ratification` variant; this doc's header is corrected to match). Pull the title + the
    first "what / why" lines via the light convention in §6. **Honest scope:** title/why/affects are only
    populated for docs that follow that convention; existing proposals not retrofitted show title-only.
    The what/why is fully present *for docs written to the convention* — not retroactively for all.
  - **contract-edits:** `git status --porcelain` ∩ `docs/contracts/**` → uncommitted contract files;
    `git diff --stat` for the changed-section summary.
- `Activity()` → `git log -n <cap> --format=...` for the last N commits (subject + author + relative time);
  flag the ones co-authored by the agent (the `Co-Authored-By: Claude` trailer).

### 5.3 API
`GET /api/agents` (single call, mirrors `/api/health` / `/api/systemmap`), assembled server-side:
```json
{
  "agents":      [ { "role":"steward","status":"idle","freshness":"12m ago","currentItem":"…","issues":[] } ],
  "board":       { "active":[…], "next":[…], "recentlyDone":[…] },
  "reviewQueue": [ { "title":"…","summary":"…","kind":"proposal","path":"…","affectedConsumers":[…] } ],
  "activity":    [ { "subject":"feat(loupe): …","author":"asolgan","when":"6m ago","byAgent":true } ],
  "sources":     { "healthKV":"ok","repo":"ok | unavailable | parse-error" }
}
```
`sources` lets the FE show per-pane degradation honestly (KV up but repo unavailable, or board parse-error
while activity is fine) instead of an all-or-nothing error — consistent with the system map's per-node
absent/stale vocabulary.

### 5.4 Server wiring + the repo-poll cost
`server` gains an optional `repo repoSource` field (nil when the repo root can't be resolved → `repo` panes
report `unavailable`). `handleAgents` reads Health KV for #1 (reusing the `KVListKeys`/`KVGet` path, filtered
to `health.agent.*`, run through the new `computeAgentHealth`) and `s.repo` for #2–#4. Pure assembly
functions take injected sources → fully unit-testable with no NATS and a fixture repo dir, exactly like
`computeHealth` / `computeSystemMap`.

- **Cache the repo reads behind a short TTL.** The repo seam shells `git log` / `git diff --stat` /
  `git status --porcelain` + parses markdown. At the shared 10s refresh clock that is three subprocess spawns
  every 10s, contending with whatever Winston/owners are doing in the same checkout. The board + commits do
  not change every 10s, so `repoSource` memoizes its last read for a TTL (~30–60s) and serves cached on the
  fast path. This decouples the repo poll cost from the Health-KV clock without a second interval in the FE.
- **Resolve the repo root via git-toplevel, not raw CWD.** `LOUPE_REPO_ROOT` (if set) wins; otherwise
  discover via `git rev-parse --show-toplevel` from the process CWD rather than trusting CWD literally — so
  launching Loupe from a subdir (or accidentally from a worktree) doesn't silently read the wrong tree. If
  discovery fails or the resolved root isn't the Lattice repo, the seam stays nil → panes degrade cleanly.

## 6. UX shape (sketch — Sally owns the real spec at build time)

The reserved `#sysmap-console` `<aside>` (320px right rail) becomes visible and holds three stacked sections,
reusing the existing `.card` / `.rollup` / `.state-tag` / status-dot visual family (no new `:root` tokens):

1. **Agents** — one chip per role: name · status-dot (green idle · yellow firing/stale · red stuck) ·
   freshness · `currentItem` on hover. Kind-agnostic renderer extended with a `kind:"agent"` status row.
2. **Needs you — L3 review** *(the headline pane)* — one card per review-queue item: **what** (title),
   **why** (summary), **affected consumers** (chips), a link to open the proposal/diff path. *Not* raw
   diffs. This is Andrew's primary interaction; it sits top-right, visually weighted.
3. **Board & activity** — compact: 🏗️ Active + 📋 next (board), then the last N commits (activity), agent
   commits badged.

One clock: extend `refreshSystemMap()` to also `fetch('/api/agents')` and repaint the rail — one heartbeat,
not two competing intervals (system-map UX §6). v1 desktop-first; the rail collapses below the map under
~900px (reuse the existing responsive breakpoint).

**Proposal front-matter convention (so `ReviewQueue()` can parse what/why without NLP):** a design doc's
first block-quote carries the **pinned marker `📐 awaiting-ratification`** (the `> **Status:** 📐 …` line
this doc uses, now corrected to the canonical string); an optional `**What:** / **Why:** / **Affects:**` trio
immediately under it gives the card fields. Cheap and human-first — but note it is **a convention this
proposal introduces**: existing 📐 docs (e.g. `agentic-ops-design.md`) carry the marker but not the trio, so
they render **title-only** until retrofitted. Pane #3 is honest about that (no fabricated why/affects), and
retrofitting is a one-line-per-doc follow-up, not a blocker.

## 7. Dependency on the (unbuilt) dependency map — scoped honestly

The rich **affected-consumers** view is *designed* (agentic-ops §7/§8 #5) to come from a **code-derived**
`consumer → producers` map (imports + control-plane subjects + substrate surface). **That map is not built.**
Rather than block this console on it, v1 degrades:
- **v1 affected-consumers** = for a contract-edit, the changed **contract file/section** names (from
  `git diff`) + any consumers a 📐 proposal *explicitly lists* in its front-matter. Honest, no fabrication.
- **v1.1** swaps in the derived dependency map when it ships — same card, richer chips, no FE rewrite.

This is called out so the console isn't oversold: the "what changed / why" is fully there in v1; the
"which consumers" is partial until the dependency map lands. Flagging, not hiding (no-silent-caps).

**Sequencing note (the lean path — surfaced by review).** Pane #1 (agent health via Health KV) is the
*genuinely new* capability — real-time liveness that exists nowhere else. Panes #2–#4 are, at v1, styled
renderings of `git log` + the board markdown + `git status`, whose richest feature (affected-consumers) is
itself gated on the unbuilt dependency map (§7). A defensible leaner ordering is to ship **pane #1 first as a
standalone v0** (the agent emission verb + `health.agent.*` rendered on the existing system map, *no
repo-seam*), and only then add the repo-seam panes. This also lets the read-seam decision (§4) and the
dependency map mature before Loupe takes on a filesystem dependency. The increments below assume the full
console is wanted; **if you prefer the lean path, ratify §4/§5.1 for pane #1 only and defer the seam** — a
valid answer to §9 Q1.

1. **Increment 1 — data layer (L2-buildable once §4 + §5.1 are ratified).** `GET /api/agents` + the
   `repoSource` read-seam (+TTL cache) + `health.agent.*` emission verb + the agent freshness rule
   (`computeAgentHealth`) + schema-doc co-update + unit tests (fixture repo dir, injected Health KV). No FE.
   Ships green behind gates; the API is inspectable via curl. *Lead review + gates (backend Go, S–M); 3-layer
   if it grows.* (Under the lean path, Increment 1 is just the `health.agent.*` half.)
2. **Increment 2 — UX + FE (UX-then-FE).** Sally specs the `#sysmap-console` rail; FE Engineer **creates**
   the `<aside>` + grid column + breakpoint (it does not pre-exist — §1), builds the three panes on the
   vanilla stack, and **verifies in-browser** against `make up-full` (+ a couple of agent heartbeats and a
   📐 proposal present). 3-layer review for the FE per the M+ rule.
3. **v1.1 (later).** Wire the code-derived dependency map into affected-consumers (§7) once it exists.

## 9. Decisions — Winston-ratified (2026-06-25)

**None of these is a frozen-contract change or an architectural fork** — `docs/observability/health-kv-schema.md`
is a doc (not a frozen contract); the `health.agent.*` keys are a *non-breaking addition* under the existing
key convention; the read-seam is internal to a local, trusted, single-identity dev tool. So per
*decide-don't-defer* (`agentic-ops-design.md` §6.1.1), **Winston ratifies them — none needed Andrew:**

- **Q1 (read-seam) → Option C (hybrid).** Agent health via Health KV; board / review-queue / activity via a
  gated, read-only local repo seam. Each data class lives where its truth already is; no dual-write drift.
- **Q2 (agent-liveness) → yes.** Agent-specific freshness (idle-is-healthy + `nextFireExpectedAt`-based
  overdue / stuck, a separate path from the 60s daemon `staleThreshold`); best-effort emission that never
  fails a fire; **grey-`absent`** (not red) when the stack was down.
- **Q3 (emission mechanism) → a `lattice health emit-agent` CLI verb.** Discoverable, testable, one place.
- **Q4 (v1 agent scope) → live emitters only** (Steward + Lamplighter + Vertical PO); absent = not shown.

**Build order:** Increment 1 (data layer — `GET /api/agents` + the `repoSource` read-seam + the
`health.agent.*` emission verb + `computeAgentHealth` + schema-doc co-update + unit tests) is L2-buildable now;
Increment 2 (UX-then-FE rail) follows. The detailed rationale for each call is retained below (originally
framed as questions; kept as the decision record).

1. **§4 — the read-seam.** Bless Option C (hybrid: agent health via Health KV; board/review/activity via a
   gated, read-only local repo seam)? Or Option A (everything through KV, accepting the dual-write) to keep
   Loupe a pure NATS client? **Or the lean path** (§8 sequencing note): ship pane #1 (agent health via Health
   KV) only, *defer the repo seam* until the dependency map matures. *Recommendation: C, but the lean path is
   a clean fallback if you'd rather not give Loupe a filesystem dependency yet.*
2. **§5.1 — the agent-liveness model.** Bless the agent-specific freshness rule (idle-is-healthy +
   `nextFireExpectedAt`-based overdue/stuck, a separate path from the 60s daemon `staleThreshold`) and the
   best-effort, never-fail-the-fire emission with **grey-`absent`** (not red) when the stack was down? This
   is the substantive design content; the first draft naively reused the daemon model and would have shown
   interval agents permanently yellow and "dead" overnight. *Recommendation: yes.*
3. **Agent emission mechanism.** A new `lattice health emit-agent` CLI verb vs. a documented `nats kv put`
   convention in the role-skills. *Recommendation: a CLI verb — discoverable, testable, one obvious place.*
4. **Scope of "agents" in v1.** Steward + Lamplighter + the Vertical PO loop are the agents that actually
   fire today. Warden/Scribe/Archivist are deferred/not-running — show only live emitters (absent = not
   shown), or pre-seed greyed placeholders? *Recommendation: show only live emitters.*

## 10. What this is NOT (guardrails honored)

- No frozen-contract edit. (`docs/observability/health-kv-schema.md` is a doc, not a frozen contract; the
  `health.agent.*` shape is a *non-breaking addition* — new keys under the existing convention.)
- No new auth / no per-user scoping — Loupe stays the single trusted admin tool.
- No agent *control* from the console (pause/kill a fire) in v1 — read-only observation, matching the
  "Lamplighter surfaces, never silently fixes" stance. Control is a later, separately-scoped decision.
- The read-seam never writes the repo and never runs a mutating git command.
