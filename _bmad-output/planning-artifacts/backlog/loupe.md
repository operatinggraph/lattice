# Backlog — Loupe (Stream 3): the operator console

Stream 3 = the Loupe console (`cmd/loupe`: Go handlers + `web/` UI). Pipeline: **PO review** files the
program → **Sally** (bmad-agent-ux-designer) produces the UX design → **Winston adjudicates**
(Andrew delegated design ratification for this program, 2026-07-02 — no 📐-awaiting-Andrew gate here) →
the **Loupe Steward** builds fires UX-then-FE. Index + cross-lane rules: [../backlog.md](../backlog.md);
row discipline: [lattice.md → "How this board works"](lattice.md) (lint-board covers this file).

**Lane boundaries.** Code scope is `cmd/loupe/**` (+ its tests). A needed platform primitive
(engine/op/substrate) or deploy/contract change routes per the cross-lane rules — file to
[lattice.md](lattice.md) and `🚧 blocked-on:` it (trivial established-pattern mirrors excepted).
**Concurrency:** this lane runs in PARALLEL with both other streams (Andrew, 2026-07-02) — it does NOT
take the shared build lock; Loupe fires serialize among themselves on `/tmp/lattice-loupe-build.lock`.

## Loupe 2.0 — "the map is the console" (the program)

PO review 2026-07-01 (Andrew session); UX design **adjudicated 2026-07-02** (Winston, Andrew-delegated):
[loupe-2-ux-design.md](../../implementation-artifacts/loupe-2-ux-design.md) — build fires per its §14;
one FE fire at a time; each fire retires a tab only in the same fire as its replacement.

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **F1 — Console shell** | Hash router + route table, ES-module `logic/` split (strip-export convention), goja harness + dep + vendors row, `keyLink` resolver seed (link rows far-end-clickable + provenance chips), breadcrumbs. | ★★★ | M | ✅ shipped · checkpoint in [design §14](../../implementation-artifacts/loupe-2-ux-design.md) |
| **F2 — Graph explorer** | Faceted/grouped/paged `#/graph` list, linkifying doc renderer, detail re-plumb, ego-graph hood mode; retires Core KV tab. | ★★★ | L | ✅ shipped · checkpoint in [design §14](../../implementation-artifacts/loupe-2-ux-design.md) |
| **F3 — Component pages** | `#/component/<id>` ×6, plural instances (fixes LWW collapse), row-level control actions, refractor roster; retires Control tab. | ★★★ | L | ✅ shipped · checkpoint in [design §14](../../implementation-artifacts/loupe-2-ux-design.md) |
| **F4 — Health absorption + status vocabulary** | Global alert strip (verbatim `health.alerts.*` incl. stub-auth-active), gates panel + rail (preserves `#sysmap-console` slot), `renderedState` incl. `pending-readpath` (the "7 degraded" fix); retires Health tab. | ★★★ | M | ✅ shipped · checkpoint in [design §14](../../implementation-artifacts/loupe-2-ux-design.md) |
| **F5 — Lens page** | Four panels: definition (DDL) · state (+freshness slot) · control (delete behind typed confirm) · contents (nats_kv now, pg-pending state). | ★★★ | L | 🎯 📋 ready · [design §14](../../implementation-artifacts/loupe-2-ux-design.md) |
| **F6 — Live pulse** | SSE tail of core-events (deliver-new, bounded), rail feed, map edge pulse animation, topbar LED, degraded modes. | ★★ | M | 📋 ready (may float early; sequence prefers F2) |
| **F7 — Submit-Op follow-through** | Structured accepted panel (committed keys linkified), `#/op?type=` prefill, session op log, ~12s requestId-filtered follow-through. | ★★ | S | 📋 ready (full value after F2+F6) |
| **F8 — Packages first-class** | `#/package/<key>` graph-resolved contents + install/upgrade/uninstall behind typed confirms (F-004 mechanics). | ★★ | M | 🚧 seq: F1, F2 |
| **F9 — Postgres read seam (lens contents)** | Read-only PG connector (`LOUPE_PG_DSN`, SELECT-only role) lighting up the §6.4 panel for protected lenses + grant tables. Adjudicated in principle (design §15 Q6); role provisioning files to lattice lane if deploy/bootstrap-touching. | ★★ | M | 🚧 seq: F5 |

## Component maintenance

| Item | What it is | Imp | Size | State |
|---|---|---|---|---|
| **[Loupe] Static-UI serving (`go:embed web`) untested** | The embedded operator-UI mount has no coverage. | ★ | XS | ✅ done (shipped with F1) |
| **[Loupe] Operator UI has no automated coverage** | goja logic-tier harness for the pure `logic/*.js` seam. Fire 2 (chromedp browser e2e) stays 🗄️ designed-shelved. | ★★ | S | ✅ done (shipped with F1) · [design](../../implementation-artifacts/loupe-fe-test-strategy-design.md) |

## Parked

| Item | Why it's parked | Imp | Size | State |
|---|---|---|---|---|
| Loupe agent-activity console | The ops layer atop the live system map (Steward queue, L3 review queue, per-agent Health). Read-seam options rejected. The L1 map keeps its `#sysmap-console` mount reserved. | ★★★ | M | 🚧 Andrew-gated (shelved 2026-06-25; design retained, do not build) |

## PO notes (rotation memory — capped, dated one-liners)

- Cross-lane feeds: lens freshness (F5's slot) ← lattice.md "silent lens-projection stall" (📐); durable
  event history (beyond F6's live tail) ← lattice.md "Loom/Weaver control-API surfacing" (📐).
- 2026-07-01 PO review (Andrew session) — filed the program; found+fixed the control-plane lockout.
- 2026-07-02 UX design adjudicated (2 premises corrected against live stack — see design §15).
- **Next:** Loupe Steward builds F5 (lens page — F4's vocabulary now in place).

## Done log — loupe (newest first)

One line per shipped item (`date · SHA · [tag] title`). Oldest roll to `archive/` past ~25.

- 2026-07-02 · `24768e8` · [Loupe/F4] Health absorption + status vocabulary — renderedState + pending-readpath rollup exclusion, shell pill+alert strip, map rail gates panel; Health tab retired
- 2026-07-02 · `5865e0e` · [Loupe/F3] Component pages + Control dissolution — `#/component/<id>` plural instances + row-level control + lens roster; Control tab retired
- 2026-07-02 · `976a18f` · [Loupe/F2] Graph explorer — faceted/paged list + linkifying renderer + ego-graph hood mode; Core KV tab retired
- 2026-07-02 · `e6a8a46` · [Loupe/F1] Console shell — hash router + ES-module split + goja logic tier (also closes: static-UI serving test, operator-UI coverage Fire 1)
- 2026-07-02 · `4b8743f` · [Loupe/deploy] Control planes restored for operator surfaces — `lattice.ctrl.>` grant (write-restriction lockout) + natsperm positive round-trip pin
