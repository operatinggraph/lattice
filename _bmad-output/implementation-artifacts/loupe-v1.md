# Loupe v1 — design + story brief (the stab)

**Status:** done (v1 stab) — built, lead-reviewed + **live-smoked against a running stack** (UI served; `/api/health` real rollup; `/api/corekv` real classified meta-vertices; `/api/packages`; control-proxy gracefully surfaces "no responders" when an engine isn't running). Gates green (build / vet / golangci-lint / `go test ./cmd/loupe/...`). Localhost-bind safety + graceful-NATS-down verified.
**Backlog item:** Loupe — view & control app (first Edge Lattice prototype). See `_bmad-output/planning-artifacts/backlog.md`.
**Type:** new internal tool (`cmd/loupe`); reuses existing vetted machinery (no new engine / contract surface).
**Review scaling:** prototype reusing vetted helpers, no engine/contract/security-plane mutation beyond the existing write path + control planes → build + a thorough lead review + a smoke test (not the full 3-layer adversarial). The one real risk (auth-less admin exposure) is designed out via the localhost-bind default below.

## Goal
A working first prototype of **Loupe**: an internal, trusted view-and-control app over a running Lattice deployment. Proves the loop end-to-end — browse Core KV, observe Health, drive all three control planes (Refractor / Weaver / Loom), submit operations, list packages — so it can grow into the Edge Lattice node.

## Architecture
A single Go binary `cmd/loupe` that:
- Connects to NATS as the **trusted admin actor** (`bootstrap.Load` → `bootstrap.BootstrapIdentityKey`), exactly like the CLI. It IS the local node / NATS client.
- Serves a **local web UI** (embedded vanilla HTML/JS/CSS via `go:embed`, no build step) + a **JSON API** over HTTP.
- The **browser is the thin view**; the Go server does all NATS I/O. (This is the trusted-tool-around-Edge-machinery shape; later the Go server grows a local VAL mirror + reconcile-by-revision.)

**Safety (BINDING).** Loupe has **no auth** and acts as admin (submits ops, pauses consumers). It MUST bind **`127.0.0.1` only** by default. A non-local bind requires an explicit `LOUPE_ADDR` opt-in and logs a loud warning at startup. A trusted-deployment tool must not silently become an auth-less network-wide admin handle.

## Scope (v1) — reuse the machinery (reuse map in the explore brief; key refs inline)
HTTP+JSON API (Go server), each reusing an existing helper:
- `GET /api/corekv?prefix=&limit=` — list Core KV keys, classify each as vertex / aspect / link / meta. Via `conn.KVListKeys(ctx, bootstrap.CoreKVBucket)` (`"core-kv"`) filtered by prefix; cap with `limit` (default e.g. 500) so a huge bucket can't hang the UI.
- `GET /api/corekv/entry?key=` — one key's envelope JSON, via `conn.KVGet`. Surface `isDeleted`.
- `GET /api/health` — Health summary: component heartbeats + freshness ("Xs ago") + issues/alerts. Reuse the classification + freshness logic shape from `cmd/lattice/health/health.go` (`bootstrap.HealthKVBucket`, keys `health.<component>.<instance>`).
- `GET /api/control/<comp>` (`comp` ∈ `refractor|weaver|loom`) — the read lists. Loom: `list` + `consumers`; Weaver: `list`; Refractor: `list`. Via `conn.NATS().RequestWithContext(ctx, "lattice.ctrl.<comp>...", nil)` → decode the component's `control.ControlResponse`.
- `POST /api/control/<comp>/<name>/<op>` — a control mutation, proxied to `lattice.ctrl.<comp>.<name>.<op>`. Loom: `inspect|pause|resume`; Weaver: `disable|enable|revoke`; Refractor: `pause|resume|rebuild|validate|inspect`. **Validate `name` dot-free + non-empty** before building the subject; validate `op` against an allow-list per component.
- `GET /api/packages` — installed packages, via `pkgmgr.NewInstaller(conn, adminActor).List(ctx)`.
- `POST /api/op` — submit an operation. Body `{operationType, lane?, class?, payload}`. Build `processor.OperationEnvelope{RequestID: substrate.NewNanoID(), Lane, OperationType, Actor: adminActor, SubmittedAt: now-rfc3339, Class, Payload}` and call `output.SubmitOp(ctx, conn, env)` (`cmd/lattice/output/submit.go`). Return the `OperationReply` (status/error/revisions/primaryKey).

Frontend (one embedded `web/` dir: `index.html` + `app.js` + `style.css`, vanilla JS `fetch`, no framework/build):
- Top nav tabs: **Core KV** (prefix filter input + key list → click a key → value pane, pretty-printed JSON), **Health** (component cards with status + freshness + any issues), **Control** (three panels — Refractor / Weaver / Loom — each rendering its list(s) with action buttons: loom pause/resume/inspect, weaver disable/enable/revoke, refractor pause/resume/rebuild; results shown inline), **Packages** (a table), **Submit Op** (form: operationType + lane select + JSON payload textarea → submit → reply pane showing accepted/rejected + revisions/error).
- Clean, minimal, readable CSS (a calm dark theme is fine). No external CDN assets (works offline / localhost).

## Non-goals (v1)
- No authN/Z, Gateway, read-path auth, Personal Lens (trusted single-identity; localhost).
- No package install/uninstall from the UI (list only — install via `lattice-pkg`). [later]
- No local VAL mirror yet (reads live). No large-file/binary (separate backlog item).
- No op-form schema autogen from DDL self-description (generic JSON payload for v1). [later]

## Build / run
- `cmd/loupe/main.go` + `cmd/loupe/web/` (embedded). Add a `loupe` build to the Makefile `build` target.
- Env: `LOUPE_ADDR` (default `127.0.0.1:7777`), `NATS_URL` (default `nats://localhost:4222`), `BOOTSTRAP_JSON_PATH` (default `./lattice.bootstrap.json`) — same conventions as the service binaries.
- Run: `make up` (brings up the stack), then `go run ./cmd/loupe`, open `http://127.0.0.1:7777`.
- **Graceful when NATS is unreachable or bootstrap file missing:** the server still starts and serves the UI; each `/api/*` call returns a clear JSON error (`{"error": "..."}`) the UI renders — never a crash. Startup must not hard-fail if NATS is down (log + serve; reconnect handles recovery).

## Verification (lead review + smoke)
- `go build ./...` · `make vet` · `golangci-lint run ./...`.
- Server starts, binds `127.0.0.1` by default (assert the default addr), serves the embedded UI at `/`, all `/api/*` routes registered. NATS-down path returns JSON errors, no panic (a unit test with no NATS, or a server-start smoke).
- Handlers: table-test the request parsing/validation (control name dot-free + op allow-list; `/api/op` envelope build) against a fake/seam where practical.
