# Loupe

**Component reference** | Audience: operators + implementers

> Loupe is an **application** (`cmd/loupe`), not a platform engine — it has no frozen interface
> contract of its own. Its framing of record is the *Active initiative — Loupe* section of
> `_bmad-output/planning-artifacts/backlog/lattice.md` and the experience-layer row of
> `_bmad-output/planning-artifacts/lattice-architecture.md`. Update this page in the same commit as
> the code; drift between page and code is a documentation bug.

---

## Overview

Loupe is the **internal view-and-control client** for a running Lattice deployment — a jeweler's
loupe onto the lattice. It connects to NATS as the **primordial admin actor** and serves a local web
UI plus a JSON API: browse Core KV (vertices / aspects / links), observe Health, drive the Refractor /
Weaver / Loom control planes, list and install / uninstall capability packages, submit operations, and
view / upload large binaries. The browser is a thin view; the Go server (`cmd/loupe`) does **all** NATS
I/O — the front-end is served from `cmd/loupe/web` (go:embed'd assets) and only ever calls `/api/*`.

**The "a-ha" framing.** Loupe is an internal, trusted-operator tool, but built *around the Edge-node
local-first machinery* — the same substrate + reconcile-by-revision a real Edge Lattice node would use
— so it doubles as the **first prototype of Edge Lattice** without taking on the Edge security layer. It
is a stepping stone: prove the local-first view/control loop now; grow into the per-user sovereign node
later, once the deferred security pieces (Gateway, read-path auth, Personal Lens) land.

---

## Capabilities (v1)

Routes are registered in `cmd/loupe/server.go` (`registerRoutes`); each handler lives in its own file.

| Surface | Routes | What it does |
|---------|--------|--------------|
| **Core KV browse** | `/api/corekv`, `/api/corekv/entry`, `/api/vertices`, `/api/vertex` | List + inspect Core-KV entries; assemble a vertex's root + aspects + links into a single view (`corekv.go`, `vertex.go`). |
| **Health dashboard** | `/api/health`, `/api/systemmap` | Read the Health-KV plane (`health.<component>.<instance>`) and compute a per-component status roll-up + a live system map (`health.go`, `systemmap.go`). |
| **Tasks** | `/api/tasks` | List open orchestration tasks (`tasks.go`). |
| **Flows** | `/api/flows` | List the Chronicler's `orchestration-history` P5 read model — every Loom instance's lifecycle (pattern, timestamps, failure reason), badging a "running" row live vs orphaned against the live `lattice.ctrl.loom.list` control read (`flows.go`). Unlike Core KV browse, this reads a lens target bucket, not Core KV. |
| **Control proxy** | `/api/control/…` | Proxy the `lattice.ctrl.<component>.*` micro-services — Refractor / Weaver / Loom: list / inspect / pause / resume / fail an instance or consumer (`server.go`, `control.go`). |
| **Packages** | `/api/packages` | List installed capability packages; install / uninstall (`server.go`). |
| **Operation submission** | `/api/ops`, `/api/op` | Enumerate submittable operations (`ops.go`) and submit one (`server.go`). Forms are driven by **DDL self-description** (`inputSchema` / `fieldDescription` / `examples`) — no Loupe-side per-op knowledge. |
| **Large binaries** | `/api/objects`, `/api/objects/…` | Upload / view / detach blobs (profile photos, lease PDFs) via the substrate Object Store; the graph holds a pointer-aspect, the store holds the content-addressed bytes (`objects.go`). |

The server starts even when NATS is unreachable or the bootstrap file is missing: the UI is served and
each `/api/*` call returns a JSON error the UI renders, never a crash (`requireConn` 503-guards the
not-connected path).

---

## Read / write paths — the P5 inspector exception

- **Reads.** Loupe reads **Core KV directly** (vertices / aspects / links) — it is the **only**
  `cmd/<app>` permitted to do so. P5 (`lattice-architecture.md`) says vertical applications read **lens
  projections**, never Core KV; the `lint-conventions` P5 gate fails any other `cmd/<app>` that
  references `core-kv` / `CoreKVBucket`. Loupe is the sanctioned exception *because it is the
  admin/console inspector* — its whole purpose is to show the raw graph an operator needs to debug the
  platform, exactly the view a lens projection would hide. (This mirrors the platform-binary exception:
  bootstrap / processor / refractor / weaver / loom / bridge may read Core KV directly; Loupe is the one
  *application* that joins them.)

- **Writes.** Loupe holds **no** write privilege over Core KV — like every other actor, it mutates state
  only by **submitting operations** to the Processor (P2: the Processor is the sole writer). Object
  uploads go to the substrate Object Store; the linking pointer-aspect is written by submitting an op.

---

## Security posture

Loupe runs as a **single trusted / privileged identity** (the primordial admin actor) with **no
authentication** and **no per-user authorization** — by design. It explicitly does **not** build
per-user authN / authZ, the Gateway, read-path authorization (D1), or a Personal Lens; it reads the
**full** graph as a trusted client. Per-user scoping is a later Edge evolution.

Because it is an auth-less admin handle, the **loopback bind is load-bearing**:

- Loupe binds **`127.0.0.1`** by default (`LOUPE_ADDR`, default `127.0.0.1:7777`).
- A non-loopback `LOUPE_ADDR` is an explicit opt-in and emits a **loud startup WARN**
  (`warnIfNonLoopback` / `isLoopbackHost`, `main.go`) — a bare `:7777`, `0.0.0.0`, a LAN/public IP, or
  an unparseable addr all trip the warning, so an auth-less network-wide admin bind is never silent.
- Uploaded blobs are served with an **anti-XSS disposition guard** (`objectDisposition`): only a
  raster-image allow-list renders inline; every other type — including active documents (svg / html /
  pdf) — is forced to a neutral `octet-stream` attachment so an uploaded document can never execute as
  same-origin script.

---

## Implementation status

**Built (Phase 3).** The view-and-control web app (`cmd/loupe`): Core-KV browse, the Health / system-map
dashboard, the tasks view, the Chronicler's Flows view (durable Loom-flow history — a P5 lens-target
read, not Core KV), the Refractor / Weaver / Loom control proxy, package list / install /
uninstall, DDL-driven operation submission, and large-binary upload / view / detach over the substrate
Object Store. Trusted single-identity, loopback-bound, no auth. Run it with `make run-loupe` (UI alone)
or `make up-full` (full stack + Loupe at http://127.0.0.1:7777).

**Testing.** The Go server is handler-tested via `httptest` (every `*_test.go` drives the real route
mux). The browser UI is ES modules under `web/js/` with a pure `logic/` tier covered by the **goja**
harness (`web_logic_test.go`): each shipped `logic/*.js` is loaded from the same `go:embed` FS the
server serves and table-tested inside `go test ./cmd/loupe/...` — no Node, no build step. `logic/`
files are declarations + one trailing `export { … }` line in ES6-conservative syntax (goja parse
failure is the loud gate); DOM/render code is verified by the fe-engineer's in-browser pass.

**Deferred (Phase 3+ — the Edge evolution).** Per-user authN / authZ, the Gateway, read-path
authorization (D1), and the Personal Lens. These are the pieces that turn the trusted inspector into a
per-user sovereign Edge node; until they land, Loupe stays a single-identity loopback tool. All four are
now 🔭 Designed (ratified 2026-06-27, build-pending): read-path auth (D1) is the foundation (Postgres-RLS
+ a JWT read-actor seam), the Gateway's read-actor seam is D1 increment 1, and the Personal Lens (the
Edge fan-out path) is sequenced behind D1 + a concrete Edge consumer.
