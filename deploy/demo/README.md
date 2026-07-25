# deploy/demo — hosted live-demo deployment (Facet over the showcase world)

Runs the **stock dev-stack recipe** (docker compose NATS + Postgres, Makefile-launched host
processes) on one small VPS, with exactly one public surface: a reverse proxy (Caddy, TLS) in front
of Facet's loopback HTTP listener (`127.0.0.1:7810`). Everything else — NATS (4222/8222/9222),
Postgres (5432), Gateway (8080), Loupe (7777) — stays loopback-bound via `.env` host-IP port
bindings (`env.demo`) and is additionally expected to sit behind the provider's network firewall
(inbound 22/80/443 only). Note Docker publishes ports past host firewalls like ufw — use the
provider's network-level firewall, not ufw, as the outer wall.

Facet runs in **demo-persona posture** (`FACET_DEMO_PERSONAS`, see `cmd/facet/main.go`): the login
page offers one-tap persona cards, the dev-login minter refuses non-persona subjects, and
`/api/claim` is disabled. The world is the idempotent showcase dataset (`make seed-showcase`); a
nightly systemd timer tears the ephemeral stack down and reseeds it, rotating the persona ids.

## Files

- `demo-bootstrap.sh <host>` — one-time (idempotent) box setup: installs Docker + Go + jq + Caddy,
  writes `.env` from `env.demo`, installs the Caddyfile for `<host>`, runs `demo-up.sh`, enables the
  systemd boot service + nightly reset timer. Ubuntu 24.04, run as root from this directory.
- `demo-up.sh` — bring the full stack + Facet up against the current checkout: the `up-facet` chain
  (up-full → provisions → showcase installs → seed) with Facet started under `FACET_DEMO_PERSONAS`
  built from the seed's printed tenant ids. Safe to re-run.
- `demo-reset.sh` — the nightly reset: stop apps, `docker compose down` (the dev stack is ephemeral
  by design — no volumes — so this IS the wipe), then `demo-up.sh` again.
- `Caddyfile` — TLS + reverse proxy to Facet, SSE-safe (`flush_interval -1`). Reads `{$DEMO_HOST}`;
  the bootstrap installs it to `/etc/caddy/Caddyfile` with the host substituted, and writes the host
  to `demo-host` so `demo-up.sh` can pass Facet `FACET_PUBLIC_ORIGIN`. That declaration is required
  behind TLS termination: Facet's bind is loopback while the browser's Origin names the public host,
  so without it every write — sign-in included — is refused, and the session cookie loses `Secure`.
- `env.demo` — compose `.env` template binding every published container port to `127.0.0.1`.
- `systemd/` — `lattice-demo.service` (runs `demo-up.sh` at boot), `lattice-demo-reset.service` +
  `lattice-demo-reset.timer` (nightly 09:10 UTC reset).

## Bring-up (after DNS `<host>` → the box, port 80/443 reachable)

```sh
git clone <repo-url> /opt/lattice
cd /opt/lattice/deploy/demo
./demo-bootstrap.sh demo.example.com
```

Verify: `https://<host>/login` shows the persona cards; sign in; request the laundry service; the
outbox entry confirms (DONE). The operator inspector is deliberately not exposed — reach Loupe via
`ssh -L 7777:127.0.0.1:7777 <box>`.

## Operational notes

- **A reset world is not ready when the processes are.** `make down` destroys the Core KV stream
  and every Refractor durable with it, so the next start rebuilds them and rescans every key for
  every lens — on this box roughly 13k events at ~15/s, i.e. **ten to twenty minutes** during which
  the auth plane still answers for no new actor. The demo is up when the projections have caught
  up, not when the ports are listening. Watch it with
  `curl -s 'http://127.0.0.1:8222/jsz?consumers=1&limit=500'` (sum `num_pending` over the
  `KV_core-kv` consumers), or the Refractor's Health KV entry, whose `CapabilityLensLagging` issue
  clears on its own. `provision-demo-operator.sh` waits this out rather than failing at a fixed
  timeout.
- **A grant that never authorizes after the backlog drains is a lost projection, not lag** — the
  class described in `capability-projection-reconciliation-design.md`. Repair one actor with
  `./bin/lattice lens reproject <capabilityRoles lensId> --actor-key <vtx.identity.…>`; it
  recomputes from the graph and costs nothing when the actor is already converged.
- **Reset cadence**: `lattice-demo-reset.timer` (09:10 UTC). Manual reset: `systemctl start
  lattice-demo-reset.service`. Logs: `journalctl -u lattice-demo*` plus the stack's own `*.log`
  files in `/opt/lattice`.
- **Update**: `cd /opt/lattice && git pull && deploy/demo/demo-reset.sh` (rebuilds binaries, fresh
  world).
- **Sizing**: ~10 Go host processes + 2 containers ≈ well under 1 GB RSS; 2 vCPU / 4 GB is
  comfortable, builds included. All deps are pure Go — x86 and ARM both fine.
- **Demo blast radius**: visitors act only as the seeded consumer personas through the real Gateway
  capability authz; worst case is demo-world graffiti until the next reset. Before announcing the
  URL widely, put a rate-limiting proxy/WAF (e.g. Cloudflare) in front of `/api/*`.
