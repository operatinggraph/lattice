# Unattended fire protocol — what every scheduled fire does, regardless of role

**Authority for the three cross-cutting mechanics of a scheduled fire: the build lock, chip notification, and
commit attribution.** Every scheduled-task prompt **points here**; none of them restates any of it. Role
behaviour lives in `agents/<role>/SKILL.md` — this file is only the wrapper that makes an unattended run safe.

> **Why this file exists.** These three rules used to be pasted into all eight scheduled-task prompts. Eight
> copies drift — and the copies are invisible from the repo, so a rule fixed here stayed broken in the prompts
> (and vice versa) until someone noticed by hand. One authority, eight pointers.

---

## 1. The mutual-exclusion build lock

**Who participates.** Only the **build-heavy** roles — the ones that touch Docker, run `go build` / `go test`,
or hold a worktree:

| Task | Lock path |
|---|---|
| `steward-autonomous`, `steward-verticals`, `ci-whetstone` | `/tmp/lattice-agentic-ops-build.lock` (shared) |
| `steward-loupe` | `/tmp/lattice-loupe-build.lock` (lane-private — Andrew's 2026-07-02 dispensation; **never** touch the shared lock) |
| `lattice-designer`, `platform-surveyor`, `vertical-po-discovery`, `lamplighter-watch` | **none** — file-only, no coordination needed; run whenever the schedule fires |

Rationale, and why the split is not fleet-wide, is in [`README.md`](README.md) § *Concurrency*.

**Acquire — first action of the fire, before anything else.** Substitute your role's `LOCKDIR` from the table:

```sh
LOCKDIR=/tmp/lattice-agentic-ops-build.lock; NOW=$(date +%s); if mkdir "$LOCKDIR" 2>/dev/null; then TOKEN="fire-$$-$NOW"; echo "$NOW" > "$LOCKDIR/acquired_at"; echo "$TOKEN" > "$LOCKDIR/owner_token"; echo "LOCK-ACQUIRED token=$TOKEN"; else HELD_AT=$(cat "$LOCKDIR/acquired_at" 2>/dev/null); if [ -z "$HELD_AT" ]; then HELD_AT=$(stat -f %m "$LOCKDIR" 2>/dev/null || stat -c %Y "$LOCKDIR" 2>/dev/null || echo 0); fi; AGE=$((NOW - HELD_AT)); if [ "$AGE" -gt 5400 ]; then rm -rf "$LOCKDIR" && mkdir "$LOCKDIR" && TOKEN="fire-$$-$NOW" && echo "$NOW" > "$LOCKDIR/acquired_at" && echo "$TOKEN" > "$LOCKDIR/owner_token" && echo "LOCK-RECLAIMED-STALE token=$TOKEN"; else echo "LOCK-HELD (${AGE}s old)"; fi; fi
```

A missing/empty `acquired_at` falls back to the lock **directory's** own mtime, never to "now", so a genuinely
orphaned lock still ages out instead of reporting 0s forever.

**Read the output before doing anything else — the two outcomes are NOT symmetric.** (2026-07-28: a fire that
saw `LOCK-HELD` ran the release command anyway, deleted a concurrent fire's live lock, and left it orphaned for
4+ hours. That is why the asymmetry is spelled out.)

- **`LOCK-HELD (…)`** → you do **not** own the lock. **Stop immediately**: no other work, no commits, and **do
  not run `rm -rf` on the lock dir under any circumstance** — you never acquired it, so it is never yours to
  delete. This is normal, not an error (`steward-autonomous` runs hourly; collisions happen). End the fire.
- **`LOCK-ACQUIRED token=…`** or **`LOCK-RECLAIMED-STALE token=…`** → you own it. **Remember the exact token.**
  Proceed.

**Renew while you hold it — after every green unit you land** (right after each successful merge/commit; also
after each long gate completes, since a CI watch alone can exceed the threshold):

```sh
LOCKDIR=/tmp/lattice-agentic-ops-build.lock; if [ "$(cat "$LOCKDIR/owner_token" 2>/dev/null)" = "<TOKEN>" ]; then date +%s > "$LOCKDIR/acquired_at"; echo LOCK-RENEWED; else echo NOT-RENEWING-token-mismatch; fi
```

A progressing fire stays protected past the 90-minute stale threshold indefinitely; a wedged one stops renewing
and ages out. On **`NOT-RENEWING-token-mismatch`** the lock was reclaimed from you — **stop immediately**, no
further builds or commits, end the fire (your release will then refuse on the same mismatch, which is correct).

**Release — your last action before the fire ends for ANY reason** (done, nothing-to-do, gate-blocked, error,
early stop), and **only** if you saw one of the two ownership outcomes:

```sh
LOCKDIR=/tmp/lattice-agentic-ops-build.lock; if [ "$(cat "$LOCKDIR/owner_token" 2>/dev/null)" = "<TOKEN>" ]; then rm -rf "$LOCKDIR"; echo LOCK-RELEASED; else echo REFUSING-RELEASE-token-mismatch-not-my-lock; fi
```

A mismatch means the lock changed hands since you acquired it (e.g. reclaimed as stale) — leave it alone, never
force it. If you saw `LOCK-HELD` at the start there is **nothing to release**.

## 2. Chips need a push — nobody is watching the session

A `spawn_task` chip only surfaces if Andrew happens to open the exact session that filed it, and a scheduled
fire is unattended. So: **if you — or an inline sub-role you are following (`owner` / `fe-engineer` /
`lamplighter`) — call `spawn_task` at any point, immediately follow it with a `PushNotification`** (status:
`proactive`; one line, under 200 chars, leading with what the chip flags). Every chip, no threshold — a chip is
already the rare high-signal case, so this cannot over-notify.

Triage of out-of-scope finds is the **lead's**, not a builder's: a spawned builder *reports* the find upward and
Winston decides the board row + optional routed chip. A chip's prompt must name the **skill** the spun session
should run plus the board row — never a bare "fix X".

## 2.5 Sub-agent delegation is authorized

A session may carry an ambient "don't spawn sub-agents unless asked" default. **For a scheduled fire, this
protocol is the ask** — where your role's SKILL.md calls for delegation (the Steward's Phase-0 scout, a builder
increment, an adversarial reviewer), use the `Agent` tool as specified there, with the **explicit `model`** that
skill names. Omitting `model` silently runs a mechanical scout on the lead's tier, so the cheaper tier is only
real if the parameter is passed.

The **roles** (`owner` / `fe-engineer` / `lamplighter`) are the exception in the other direction: they are
playbooks you follow **inline**, never `subagent_type`s — spawning them fails.

## 3. Commit attribution is not automatic here

An **interactive** Claude Code session is told by its own system prompt to end commits with a `Co-Authored-By:`
trailer. A **scheduled-task** fire is not, so its commits land author-only with no model credit unless the role
says so. (Learned the hard way: removing a hardcoded `Co-Authored-By: Claude Opus 4.8` line — right, because it
lied once a different model ran the fire — silently dropped attribution for every scheduled role.)

**End every commit message with a `Co-Authored-By:` trailer naming whichever model you actually are** — check
your own system prompt for the model name (e.g. `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`).
**Never hardcode a specific model** here or in a commit: a different model may run the next fire, so always
self-report.

## 4. Adding a scheduled role

Give it a lock **only** if it actually builds or touches Docker, and default to the **shared** lock — Loupe's
lane-private dispensation does not generalize (Verticals got its own lane lock on 2026-07-12 and it was reverted
the same day: two concurrent build fires exhaust this 16GB Mac). §2 and §3 apply to every role unconditionally.
Its prompt should be a handful of lines: identity, repo, the skill(s) that are its authority, this file, and its
run parameters — nothing that duplicates a skill.
