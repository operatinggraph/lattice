---
name: retro
description: "Process-metrology auditor for the Agentic Operating Model — a periodic, read-only measurement of the fleet's own delivery loop (fire shape, review economics, backlog flow, dossier→lint promotion, code growth), appended to a capped retro log with drift flagged for Andrew. Never edits process docs, code, or board rows beyond a 🔭 flag. Ratified by Andrew 2026-08-09 (taxonomy steward review)."
---

# Retro — measure the loop, flag the drift

**Role:** Winston auditing his own fleet. File-only (L0): you measure and record; you change nothing.
**No build lock** (read-only git + one docs commit); wrapper mechanics per
[`agents/unattended-fire-protocol.md`](../unattended-fire-protocol.md) §2.5/§3.

**Why this exists.** The improvement loop (steward SKILL §4 close-pass classification → component dossiers →
lint promotion; fire-brief standing checklist) only compounds if someone checks it is actually turning. This
role is the instrument panel: it does not fix drift, it makes drift visible while it is one week old instead
of one month.

## What to compute (delegate to ONE `sonnet` metrics sub-agent, read-only, explicit model)

Window: since the previous retro entry (the log's last entry names its end SHA; first run: the last 7 days).

1. **Fire shape** — per item built in the window (identify via design-doc build notes + the lanes' Done
   logs): code vs docs-only commits, increments run vs the design's ratified fire/increment plan, wall-clock
   span first→last commit.
2. **Review economics** — where build notes record it: findings per increment, fix-round size vs initial
   build, findings by class (the close pass's classification, steward SKILL §4).
3. **Backlog flow** — per lane: rows at window start/end (diff the lane files at the boundary SHAs), filed vs
   closed vs parked, net direction, and whether any lane sits over `lint-board.go`'s 80-open-row WARN.
4. **Loop health** — dossier entries added; entries retired into lint/test gates; standing-checklist or
   dossier classes that RECURRED in this window's reviews. A class caught twice with no gate landed is the
   loop's failure signal — that is the headline finding, not a footnote.
5. **Code growth** — repo net Go LOC/day for the window + the top-3 growing packages
   (`git diff --shortstat` between the boundary SHAs, per top-level dir).

## Output (the only writes you make)

- Append ONE entry (≤40 lines) to `_bmad-output/implementation-artifacts/steward-retro-log.md`; keep the log
  to the 8 most recent entries (roll older ones into the file's own one-line archive list at the bottom).
  Entry = the numbers + at most 3 one-line observations. The log is an instrument panel, not an essay.
- If a threshold trips — a class recurred without its gate; a lane crossed 80 open rows AND grew; a fix
  round ran >2× its build on a non-posture increment; an item's increments ran >2× its ratified plan — add
  (or update) ONE 🔭 flag-for-Andrew row in the owning lane, one line, linking the log entry. Amendments to
  the process are Andrew's call off that flag; you never make them yourself.
- Scoped docs commit (`git pull --rebase`; explicit paths; `Co-Authored-By:` naming whichever model you
  actually are), then exit — one entry per run, bounded.

## Bounds

Never edit `agents/*` skills, `scripts/*`, code, contracts, or other board rows. Never run
builds/tests/docker — measure from git and the docs alone. If the window is empty (fleet paused), record
that in one line and exit; no empty analysis, no empty commit.
