# Story SL.1 — location-domain package

**Status:** done (CI green — commit ae3a056)
**Design:** `_bmad-output/implementation-artifacts/service-location-design.md` (rev.3 — §2, §5, §6)
**Review depth:** thorough lead review (non-security: no lens, no auth-plane) — explicit per the scale-review house rule.

## Goal

Ship `packages/location-domain/` — the spatial base domain. A new base package (mirrors `identity-domain`)
that owns the place graph: location vertex types + the `containedIn` containment link. No lens, no auth — that
is SL.2 (`service-location`). This is greenfield (no `unit`/`building`/`property` DDL exists today).

## Acceptance criteria

1. **Vertex types** — `unit`, `building`, `property` per Contract #6 §6.9 (keys `vtx.unit.<id>` /
   `vtx.building.<id>` / `vtx.property.<id>`), all class `location`. Root data minimal `{}` (D5). DDL structure
   mirrors the codebase idiom (study `service-domain`'s single-DDL-with-class-discriminator and
   `identity-domain`'s single-type DDL; pick the idiomatic fit and document the choice).
2. **`containedIn` link** — location→location, transitive (unit→building→property). Direction per Contract #1
   §1.1 (later-arriving = source: "unit containedIn building"). 6-segment key. The wire-op validates BOTH
   endpoints alive AND location-class.
3. **Ops** — Create + Tombstone for the location type(s); `WireContainedIn` / `UnwireContainedIn`.
4. **Permissions** — every op `grantsTo: [operator]`, scope `any` (mirror `rbac-domain`).
5. **Installable** — `manifest.yaml` + `package.go` + registration wherever `rbac-domain`/`service-domain`
   register; a `verify-package-location-domain` Makefile target mirroring existing `verify-package-*`.
6. **Tests** — install-flow test co-installing with `identity-domain` (proves no canonical-name collision +
   DDLs/permissions land); unit tests for the ops + the `containedIn` endpoint-class validation.
7. **Gates green** — `go build ./...`, `make vet`, `golangci-lint run ./...`,
   `go test ./packages/location-domain/...`, `make verify-package-location-domain`, `make verify-kernel`.
8. **No history/changelog comments** (house rule). Match surrounding idioms.

## Dev notes

- Templates: `packages/rbac-domain/` (closest — single DDL, permissions→operator, manifest, package.go),
  `packages/identity-domain/`, `packages/service-domain/` (the `service` DDL's class-discriminator + link
  validation pattern).
- This package is consumed by SL.2's `service-location`: its `containedIn` + location vertices are what the
  `capabilityServiceAccess` lens walks. Keep the types clean and topology-only.
- Sub-agent builds; **does not commit/push/branch**. Winston (lead) reviews + commits + watches CI.
