# Platform control plane — `lattice.ctrl.*`

**Component reference** | Audience: component authors + operators + architects | Contract authority: **this page** (no `docs/contracts/*` section currently owns the control plane; Contract #10 owns the *data-plane* orchestration surfaces — ops, `externalTask`, schedules — not this operator surface)

---

## What it is

The control plane is the platform's **operator request/reply surface**: a live, synchronous
channel for pausing, resuming, inspecting, and retiring the three long-running orchestration
engines (Loom, Weaver, Refractor) without redeploying them. It is distinct from the data plane
(operations → Processor) and from Health KV (passive self-reporting): control-plane calls are
imperative commands an operator issues *now* and waits for an acknowledgement.

Each engine runs its control plane as a **NATS micro-service** (`github.com/nats-io/nats.go/micro`)
over **core NATS request/reply** — not JetStream, no durable state. Three responders exist:

| Responder service | Engine | Source |
|---|---|---|
| `loom-control` | Loom | `internal/loom/control` |
| `weaver-control` | Weaver | `internal/weaver/control` |
| `refractor-control` | Refractor | `internal/refractor/control` |

Each is a sibling package that imports its engine one-way (the engine never imports its control
package), registers endpoints under the shared default queue group `"q"` (so multiple engine
instances distribute load), and gets the standard `$SRV.PING` / `$SRV.STATS` / `$SRV.INFO`
introspection endpoints for free (`nats micro list`).

**Operator clients** (the only sanctioned callers):
- **Loupe** — `GET /api/control/<comp>` and `POST /api/control/<comp>/<name>/<op>`
  (`cmd/loupe/server.go` → `cmd/loupe/control.go`). Loupe forwards each engine's raw JSON reply to
  the browser and never decodes it into the engine's typed structs, so its hardcoded per-component
  allow-list (`controlComponents` in `cmd/loupe/control.go`) is the entire contract Loupe holds with
  each plane.
- **The `cmd/lattice` operator CLI** — the `loom` and `weaver` subcommands; Refractor lens ops are
  driven through Loupe. The CLI shares Loupe's operator surface via the same transport grant.

---

## Subject grammar

```
lattice.ctrl.<component>.<...>
```

Two shapes, both rooted at `lattice.ctrl.<component>`:

| Shape | Tokens | Used for | Wildcard registration |
|---|---|---|---|
| Exact | `lattice.ctrl.<comp>.<op>` | component-wide reads (`list`, `consumers`) | exact subject |
| Per-entity | `lattice.ctrl.<comp>.<id>.<op>` | one entity (instance / target / lens) | `lattice.ctrl.<comp>.*.<op>` |

The per-entity form is exactly **5 dot-separated tokens**. `<id>` is a single dot-free token —
an instance id, a Weaver target id, or a Refractor lens id. A single-token wildcard (`*`) at the
`<id>` position lets one handler serve every entity, because an engine does not know the full set of
ids at registration time. `<id>` never carries a dotted vertex key (dots are NATS token separators);
the id is a registered lens/target/instance id. A dotted or empty `<id>` builds a subject no endpoint
matches — clients reject it before publishing (`validateControlName` in `cmd/loupe/control.go`).

---

## Op vocabulary (per plane)

Each engine's op set is the authoritative `supportedOps` / registered-endpoint list in its control
package. **A client's allow-list must mirror this set** — see [Drift guard](#drift-guard).

### Loom — `internal/loom/control`

| Op | Subject | Kind | Meaning |
|---|---|---|---|
| `list` | `lattice.ctrl.loom.list` | read (exact) | list Loom instances |
| `consumers` | `lattice.ctrl.loom.consumers` | read (exact) | list managed consumers + status |
| `inspect` | `lattice.ctrl.loom.<instanceId>.inspect` | read (per-entity) | one instance's detail |
| `pause` | `lattice.ctrl.loom.<consumer>.pause` | mutate | pause a consumer (sticky across restart, health-kv backed) |
| `resume` | `lattice.ctrl.loom.<consumer>.resume` | mutate | resume a paused consumer |
| `redrive` | `lattice.ctrl.loom.<instanceId>.redrive` | mutate | resume a FAILED instance at its recorded cursor (never restarts under a fresh id — that would re-execute committed side effects) |

### Weaver — `internal/weaver/control`

| Op | Subject | Kind | Meaning |
|---|---|---|---|
| `list` | `lattice.ctrl.weaver.list` | read (exact) | list convergence targets |
| `disable` | `lattice.ctrl.weaver.<targetId>.disable` | mutate | disable a target |
| `enable` | `lattice.ctrl.weaver.<targetId>.enable` | mutate | re-enable a target |
| `revoke` | `lattice.ctrl.weaver.<targetId>.revoke` | mutate | revoke a target |

### Refractor — `internal/refractor/control`

All Refractor ops are **per-lens** (no component-wide `list`; Loupe discovers lens ids via the
Health tab).

| Op | Subject | Kind | Meaning |
|---|---|---|---|
| `health` | `lattice.ctrl.refractor.<lensId>.health` | read | the lens's Health KV entry |
| `validate` | `lattice.ctrl.refractor.<lensId>.validate` | read | best-effort field-presence report over a Core-KV sample (simple-engine lenses only) |
| `rebuild` | `lattice.ctrl.refractor.<lensId>.rebuild` | mutate (async) | rebuild the target store; `{"truncate":bool}` in the body |
| `pause` | `lattice.ctrl.refractor.<lensId>.pause` | mutate | halt the lens fetch loop |
| `resume` | `lattice.ctrl.refractor.<lensId>.resume` | mutate | unblock a paused lens |
| `delete` | `lattice.ctrl.refractor.<lensId>.delete` | mutate | stop the lens, remove its consumer + Health KV entry |

---

## Request body

Requests are **plain NATS requests with an (almost always) empty body** — the op and entity id ride
the subject, not the payload. The one exception: Refractor's `rebuild` reads `{"truncate": <bool>}`
(default `false`). Refractor also still *accepts* a legacy `{"op":..., "ruleId":...}` body for older
tooling, but **the subject path is authoritative** when both are present.

---

## Reply envelope

Every plane replies with a JSON `ControlResponse` object. The invariant across all three:

- **On error, only `error` is present** (a non-empty string). No success field is set.
- **On success, exactly one op-specific field is present**, named for the op; all others are omitted.

| Plane | Success fields (by op) | Error field |
|---|---|---|
| Loom | `instances` (list) · `consumers` (consumers) · `instance` (inspect) · `pause:{paused,note}` · `resume:{resumed}` · `redrive:{redriven}` | `error` |
| Weaver | `targets` (list) · `disable:{disabled}` · `enable:{enabled}` · `revoke:{revoked}` | `error` |
| Refractor | health.Entry fields (health) · `validate:{sampleSize,fieldReports,warnings}` · `rebuild:{started}` · `pause:{paused}` · `resume:{resumed}` · `delete:{deleted}` | `error` |

The boolean acks (`paused`, `disabled`, `started`, …) are always `true` when the field is present —
their presence *is* the acknowledgement. `rebuild` is asynchronous: `started:true` means the rebuild
was accepted and now runs in the background; failures surface only in the engine's logs, not the
reply.

---

## Auth posture

**Two layers, and the application layer is currently a stub — read this before relying on control-plane authorization.**

1. **Transport (enforced today).** The NATS account matrix (`deploy/nats-server.conf`,
   `deploy/gen-dev-nkeys`) scopes who may publish on `lattice.ctrl.*`:
   - each engine nkey may publish responses on its **own** plane only (`lattice.ctrl.loom.>`,
     `lattice.ctrl.weaver.>`; Refractor via `allowResponses`);
   - the **Loupe** and **`lattice` CLI** nkeys are granted `lattice.ctrl.>` publish — they are the
     sanctioned request issuers.
   A component with no `lattice.ctrl.>` grant cannot reach the planes by publishing to them directly.
   It is not an absolute transport bar: `lattice.ctrl.*` is an ordinary subject, so a server-published
   reply or a stream RePublish reaches it like any other (`internal/natsperm`'s `Deny` doc comment).
   Content there is server-chosen, so the reachable shape is malformed-request noise rather than a
   forged command — the responders' own request validation is what stands between that and an action.

2. **Application capability check (a stub — allow-all).** Loom and Weaver call
   `CapabilityChecker.Authorize(ctx, actor, op, targetID)` before acting, but production wires the
   **`StubCapabilityChecker`**, which **allows every request and logs it** — the real Capability-KV
   gate is **FR30** (ratified-designed, deprioritized: see the Control-plane Capability authorization
   row on the Lattice board). The `actor` argument is currently passed empty (`""`) — clients do not
   yet stamp an operator identity. **Refractor's control service has no capability check at all** in
   its dispatch path (no `CapabilityChecker` on its `Service`).

**Net:** control-plane authorization today rests entirely on the **transport grant + Loupe's
loopback, single-identity, auth-less posture** (see [loupe.md](./loupe.md)). Anyone who can publish
`lattice.ctrl.>` can invoke any op. Treat the planes as operator-trusted until FR30 lands.

---

## Failure modes

| Condition | Behavior |
|---|---|
| Unknown component / op (client side) | rejected before a subject is built (Loupe allow-list; CLI); never reaches NATS |
| Malformed subject (server side) | `{"error":"invalid control subject ..."}` |
| Unknown entity id (not registered) | `{"error":"rule/target/instance ... not registered"}` |
| Blocked engine call (KV slow/unavailable) | 5s `handlerTimeout` → error reply, not a wedged goroutine |
| Handler panic (Loom / Weaver) | `recoverHandler` converts it to a structured error reply + logs a stack; the process survives |
| Reply marshal failure | a hand-built error envelope is sent (never a client-side timeout) |

---

## Drift guard

This surface has **no compile-time coupling between the engines and their clients** — Loupe's
`controlComponents` map and the `cmd/lattice` CLIs hardcode the subjects and ops. When you add,
rename, or remove an op on an engine plane, you **must** update its client allow-list in the same
change, and reconcile this page. The reconciliation points:

- Engine ops: each control package's `supportedOps` / registered endpoints (`internal/{loom,weaver}/control/service.go`, `internal/refractor/control/service.go`).
- Loupe allow-list: `controlComponents` in `cmd/loupe/control.go`.
- Transport grants: `deploy/nats-server.conf` + `deploy/gen-dev-nkeys/main.go`.

A silent divergence (an engine op with no client entry, or a client op the engine dropped) is a
documentation *and* correctness bug — this page is where the three are kept honest.

---

## Implementation status

| Piece | Status |
|---|---|
| Loom / Weaver / Refractor control responders (subjects, ops, reply envelopes) | ✅ Built (Phase 2 / 3) |
| Transport-layer scoping of `lattice.ctrl.*` | ✅ Enforced (account matrix) |
| Application capability authorization (FR30) | 🔭 Ratified-designed, deprioritized — production runs the allow-all stub; Refractor has no gate |
| Operator actor stamping (real `actor` on `Authorize`) | ⛔ Not wired — `actor` is empty today |
