# Lattice Phase 1 — Story 1.3 Dev Harness
# Requires: Docker, Docker Compose, Go 1.26.1+
#
# Quick reference:
#   make up              — start the kernel (NATS + Postgres, bootstrap, refractor, processor)
#   make up-full         — full stack on latest: kernel + orchestration tier + core packages + Loupe
#   make install-loftspace — add the LoftSpace lease-app vertical onto a running up-full
#   make refresh-clinic  — dev-loop: diff-apply edited clinic packages + restart clinic-app (no teardown)
#   make reinstall-package PKG=packages/<dir> — diff-apply one edited package in place
#   make run-loupe       — build + run Loupe (view/control UI) on http://127.0.0.1:7777
#   make down            — tear down everything cleanly
#   make verify-bootstrap — assert primordial state; exit 0 on success
#   make build           — compile all binaries
#   make vet             — run go vet ./...

SHELL := /bin/bash
NATS_URL ?= nats://localhost:4222
BOOTSTRAP_JSON ?= $(abspath ./lattice.bootstrap.json)
# The loftspace-app read-boundary DSN (D1.3): a NON-superuser, SELECT-only role so
# Postgres RLS is enforced (the lattice superuser would bypass it). See
# provision-loftspace-role.
LOFTSPACE_APP_PG_DSN ?= postgres://loftspace_app:loftspace_app_dev@localhost:5432/lattice?sslmode=disable

# Load .env if it exists (ignored by git).
-include .env

.PHONY: up up-full up-loftspace orchestration install-packages install-loftspace run-loupe run-loftspace-app down verify-kernel verify-package-rbac verify-package-identity verify-package-identity-hygiene verify-package-objects-base verify-package-location-domain verify-package-loftspace-domain verify-package-clinic-domain verify-package-clinic-reminders up-clinic install-clinic refresh-clinic refresh-loftspace provision-loftspace-role reinstall-package verify-package-service-location verify-conformance build vet lint-conventions install-skills test test-bypass test-capability-adversarial test-rollback test-lease-convergence test-object-gc test-cli test-hello-lattice test-health-completeness processor run-processor clean logs ps

## up — Bring up NATS + Postgres, run bootstrap binary, block until readiness gate.
up:
	@echo "==> Starting NATS + Postgres..."
	docker compose up -d --wait
	@echo "==> Containers healthy."
	@echo "==> Killing any background refractor processes (avoid warm-up duplicates)..."
	-pkill -f "bin/refractor" 2>/dev/null || true
	@echo "==> Killing any background processor processes (avoid warm-up duplicates)..."
	-pkill -f "bin/processor" 2>/dev/null || true
	@echo "==> Building bootstrap binary..."
	go build -o bin/bootstrap ./cmd/bootstrap
	@echo "==> Building refractor binary (Story 2.1)..."
	go build -o bin/refractor ./cmd/refractor
	@echo "==> Building lattice CLI..."
	go build -o bin/lattice ./cmd/lattice
	@echo "==> Running bootstrap (seed pass — readiness gate deferred until Refractor is up)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/bootstrap -skip-ready-wait
	@echo "==> Starting refractor in background..."
	NATS_URL=$(NATS_URL) REFRACTOR_PG_DSN="postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable" ./bin/refractor >refractor.log 2>&1 </dev/null &
	@echo "==> Running bootstrap (readiness gate — blocks until admin + Loom + Weaver + Bridge cap.* projections land)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/bootstrap
	@echo "==> Building processor binary..."
	go build -o bin/processor ./cmd/processor
	@echo "==> Starting processor in background..."
	NATS_URL=$(NATS_URL) PROCESSOR_FILTER=ops.default,ops.urgent,ops.system,ops.meta LATTICE_AUTH_MODE=stub ./bin/processor >processor.log 2>&1 </dev/null &
	@echo "==> Lattice ready."

## down — Tear down all containers and remove the bootstrap JSON.
## Volumes are ephemeral (not named), so container removal clears NATS + Postgres data.
down:
	@echo "==> Stopping and removing containers..."
	docker compose down --remove-orphans
	@echo "==> Removing bootstrap JSON (if present)..."
	rm -f $(BOOTSTRAP_JSON)
	@echo "==> Killing any background refractor processes..."
	-pkill -f "bin/refractor" 2>/dev/null || true
	@echo "==> Killing any background processor processes..."
	-pkill -f "bin/processor" 2>/dev/null || true
	@echo "==> Killing any background orchestration / Loupe processes..."
	-pkill -f "bin/loom" 2>/dev/null || true
	-pkill -f "bin/weaver" 2>/dev/null || true
	-pkill -f "bin/bridge" 2>/dev/null || true
	-pkill -f "bin/object-store-manager" 2>/dev/null || true
	-pkill -f "bin/loupe" 2>/dev/null || true
	-pkill -f "bin/loftspace-app" 2>/dev/null || true
	-pkill -f "bin/clinic-app" 2>/dev/null || true
	@echo "==> Down complete."

## verify-kernel — Assert post-Story-4.7 kernel keys exist with correct envelopes.
## Expected count ≈ 91 OK lines (30 top-level keys + aspects + streams/buckets).
verify-kernel:
	@echo "==> Running kernel verification..."
	NATS_URL=$(NATS_URL) go run ./scripts/verify-kernel.go

## verify-package-rbac — Install rbac-domain package and assert its KV state.
verify-package-rbac:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing rbac-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/rbac-domain
	@echo "==> Running rbac-domain package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-rbac.go

## verify-package-identity — Install identity-domain package and assert its KV state.
verify-package-identity:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing identity-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/identity-domain
	@echo "==> Running identity-domain package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-identity.go

## verify-package-identity-hygiene — Install identity-hygiene and assert its KV state.
verify-package-identity-hygiene:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing identity-hygiene..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/identity-hygiene
	@echo "==> Running identity-hygiene package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-identity-hygiene.go

## verify-package-objects-base — Install objects-base and assert its KV state.
verify-package-objects-base:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing objects-base..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/objects-base
	@echo "==> Running objects-base package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-objects-base.go

## verify-package-location-domain — Install location-domain and assert its KV state.
verify-package-location-domain:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing location-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/location-domain
	@echo "==> Running location-domain package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-location-domain.go

## verify-package-loftspace-domain — Install location-domain + loftspace-domain
## (in dependency order) and assert loftspace-domain's KV state.
verify-package-loftspace-domain:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing location-domain (dependency)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/location-domain
	@echo "==> Installing loftspace-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/loftspace-domain
	@echo "==> Running loftspace-domain package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-loftspace-domain.go

## verify-package-clinic-domain — Install clinic-domain (self-contained) and
## assert its KV state.
verify-package-clinic-domain:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing clinic-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-domain
	@echo "==> Running clinic-domain package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-clinic-domain.go

## verify-package-clinic-reminders — Co-install the clinic vertical (orchestration-
## base → clinic-domain → clinic-reminders) and assert clinic-reminders' KV state
## (the 2 DDLs, the appointmentReminders lens, the weaverTarget playbook, the grant).
verify-package-clinic-reminders:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing orchestration-base + clinic-domain + clinic-reminders..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-domain
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-reminders
	@echo "==> Running clinic-reminders package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-clinic-reminders.go

## verify-package-service-location — Co-install service-location with its
## dependencies (location-domain + service-domain, plus the deps those need:
## rbac-domain for the operator role, identity-domain + orchestration-base for
## service-domain) in dependency order, then assert service-location's KV state.
verify-package-service-location:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing dependency chain (rbac-domain, identity-domain, orchestration-base, location-domain, service-domain)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/rbac-domain
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/identity-domain
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/location-domain
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/service-domain
	@echo "==> Installing service-location..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/service-location
	@echo "==> Running service-location package assertions..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) go run ./scripts/verify-package-service-location.go

## verify-conformance — Run the contract-conformance freeze suite: the frozen
## OperationReply / envelope / contextHint shapes, Core KV key shapes, the DDL
## aspect set, the closed `response` script-return schema, and the in-code
## reply-constraint enforcement proof (a non-primaryKey response key or a
## primaryKey not in the committed mutation set is rejected fail-closed).
## Self-contained: uses embedded NATS, no Docker stack required.
verify-conformance:
	@echo "==> go test ./internal/processor -run TestConformance"
	go test ./internal/processor -run TestConformance -count=1

## build — Compile all binaries under cmd/.
build:
	@echo "==> Building all binaries..."
	go build ./...
	mkdir -p bin
	go build -o bin/bootstrap ./cmd/bootstrap
	go build -o bin/refractor ./cmd/refractor
	go build -o bin/processor ./cmd/processor
	go build -o bin/lattice ./cmd/lattice
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	go build -o bin/loom ./cmd/loom
	go build -o bin/weaver ./cmd/weaver
	go build -o bin/bridge ./cmd/bridge
	go build -o bin/object-store-manager ./cmd/object-store-manager
	go build -o bin/loupe ./cmd/loupe

## test-cli — Run the lattice CLI unit + E2E tests.
test-cli:
	go test ./cmd/lattice/... -v -p 1 -count=1

## processor — Build the Processor binary (Story 1.5).
processor:
	@echo "==> Building processor binary..."
	mkdir -p bin
	go build -o bin/processor ./cmd/processor

## run-processor — Run the Processor against the local make-up harness.
## Requires `make up` to have completed (NATS reachable, core-operations stream live).
run-processor: processor
	@echo "==> Starting processor (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) ./bin/processor

## up-full — Full local deployment on latest source: kernel (make up) +
## orchestration tier (Loom/Weaver/Bridge/object-store-manager) + core packages
## + Loupe, all in the background. When it returns, open http://127.0.0.1:7777.
## For a clean rebuild from scratch, run `make down` first.
up-full:
	@$(MAKE) up
	@$(MAKE) orchestration
	@$(MAKE) install-packages
	@echo "==> Building loupe binary..."
	go build -o bin/loupe ./cmd/loupe
	@echo "==> Killing any prior Loupe process..."
	-pkill -f "bin/loupe" 2>/dev/null || true
	@echo "==> Starting Loupe in background..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/loupe >loupe.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> Full Lattice ready. Open http://127.0.0.1:7777 (Loupe)."
	@echo "==> Logs: loupe.log loom.log weaver.log bridge.log objmgr.log refractor.log processor.log"

## up-loftspace — Full stack + the LoftSpace vertical + the applicant app on :7788.
## Runs up-full, installs the LoftSpace vertical (orchestration-base → location-domain
## → loftspace-domain → service-domain → lease-signing), and starts loftspace-app in
## the background alongside Loupe (:7777). The applicant app is the demand-side view;
## Loupe is the operator/inspector. Logs: loftspace-app.log (+ the up-full logs).
up-loftspace:
	@$(MAKE) up-full
	@$(MAKE) provision-loftspace-role
	@$(MAKE) install-loftspace
	@echo "==> Building loftspace-app binary..."
	go build -o bin/loftspace-app ./cmd/loftspace-app
	@echo "==> Killing any prior loftspace-app process..."
	-pkill -f "bin/loftspace-app" 2>/dev/null || true
	@echo "==> Starting loftspace-app in background (D1.3 read boundary: non-superuser SELECT-only role + dev-auth)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
		LOFTSPACE_APP_PG_DSN="$(LOFTSPACE_APP_PG_DSN)" LOFTSPACE_APP_DEV_AUTH=1 \
		./bin/loftspace-app >loftspace-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> LoftSpace ready. Operator/inspector: http://127.0.0.1:7777 (Loupe) · applicant app: http://127.0.0.1:7788"

## provision-loftspace-role — Create the loftspace-app's Postgres read role: a
## NON-superuser, SELECT-only role (D1.3 Fire 3). The app MUST NOT read as the
## `lattice` superuser — superusers (and BYPASSRLS roles) skip RLS entirely, so the
## protected lease-applications model would leak every actor's rows. SELECT-only
## bounds the WRITE blast radius — a compromised app cannot forge a grant row or
## mutate any read model (it can still read the dev DB's lens tables). Default
## privileges FOR the lattice (Refractor) role cover tables created at later lens
## activation; the explicit grant covers tables that already exist. Idempotent.
provision-loftspace-role:
	@echo "==> Provisioning loftspace-app non-superuser SELECT-only Postgres role..."
	docker compose exec -T postgres psql -U lattice -d lattice -v ON_ERROR_STOP=1 -c "\
		DO \$$\$$ BEGIN \
		  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='loftspace_app') THEN \
		    CREATE ROLE loftspace_app LOGIN PASSWORD 'loftspace_app_dev' NOSUPERUSER NOCREATEDB NOCREATEROLE; \
		  END IF; \
		END \$$\$$;" \
		-c "GRANT USAGE ON SCHEMA public TO loftspace_app;" \
		-c "ALTER DEFAULT PRIVILEGES FOR ROLE lattice IN SCHEMA public GRANT SELECT ON TABLES TO loftspace_app;" \
		-c "GRANT SELECT ON ALL TABLES IN SCHEMA public TO loftspace_app;"

## up-clinic — One-command Clinic vertical: up-full → install-clinic → build +
## start clinic-app (:7799) in the background alongside Loupe (:7777). The clinic
## app is the demand-side patient/booking view; Loupe is the operator/inspector.
## Logs: clinic-app.log (+ the up-full logs).
up-clinic:
	@$(MAKE) up-full
	@$(MAKE) install-clinic
	@echo "==> Building clinic-app binary..."
	go build -o bin/clinic-app ./cmd/clinic-app
	@echo "==> Killing any prior clinic-app process..."
	-pkill -f "bin/clinic-app" 2>/dev/null || true
	@echo "==> Starting clinic-app in background..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/clinic-app >clinic-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> Clinic ready. Operator/inspector: http://127.0.0.1:7777 (Loupe) · patient app: http://127.0.0.1:7799"

## orchestration — Build + start the orchestration tier (Loom, Weaver, Bridge,
## object-store-manager) in the background. Requires a running deployment
## (make up). object-store-manager needs no actor key; the rest load the admin
## actor from the bootstrap JSON. Logs: loom.log weaver.log bridge.log objmgr.log.
orchestration:
	@echo "==> Killing any prior orchestration processes..."
	-pkill -f "bin/loom" 2>/dev/null || true
	-pkill -f "bin/weaver" 2>/dev/null || true
	-pkill -f "bin/bridge" 2>/dev/null || true
	-pkill -f "bin/object-store-manager" 2>/dev/null || true
	@echo "==> Building orchestration binaries..."
	go build -o bin/loom ./cmd/loom
	go build -o bin/weaver ./cmd/weaver
	go build -o bin/bridge ./cmd/bridge
	go build -o bin/object-store-manager ./cmd/object-store-manager
	@echo "==> Starting Loom / Weaver / Bridge / object-store-manager in background..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/loom >loom.log 2>&1 </dev/null &
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/weaver >weaver.log 2>&1 </dev/null &
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/bridge >bridge.log 2>&1 </dev/null &
	NATS_URL=$(NATS_URL) ./bin/object-store-manager >objmgr.log 2>&1 </dev/null &
	@echo "==> Orchestration tier started."

## install-packages — Install the core Capability Packages into a running
## deployment, in dependency order: rbac-domain → identity-domain → objects-base.
## (lattice-pkg only warns on unmet deps; ordering is the caller's responsibility.)
install-packages:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing rbac-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/rbac-domain
	@echo "==> Installing identity-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/identity-domain
	@echo "==> Installing objects-base..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/objects-base

## install-loftspace — Install the LoftSpace lease-application vertical onto a
## running full stack (make up-full first), in dependency order:
## orchestration-base → location-domain → loftspace-domain → service-domain →
## lease-signing. up-full ships only the
## core packages; the vertical is an opt-in so demos / the PO loop can drive the
## real lease flow without hand-installing each package.
install-loftspace:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing orchestration-base..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	@echo "==> Installing location-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/location-domain
	@echo "==> Installing loftspace-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/loftspace-domain
	@echo "==> Installing service-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/service-domain
	@echo "==> Installing lease-signing..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/lease-signing
	@echo "==> LoftSpace vertical installed. Drive it via the lattice CLI or Loupe."

## install-clinic — Install the clinic vertical onto a running up-full stack, in
## dependency order: orchestration-base → clinic-domain → clinic-reminders.
## clinic-domain is the bookable domain; clinic-reminders adds the @at appointment-
## reminder orchestration (needs orchestration-base for MarkExpired + the Weaver
## tier up-full runs). Drive it via the clinic-app, the lattice CLI, or Loupe.
install-clinic:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Installing orchestration-base..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/orchestration-base
	@echo "==> Installing clinic-domain..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-domain
	@echo "==> Installing clinic-reminders..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install packages/clinic-reminders
	@echo "==> Clinic vertical installed (domain + reminders). Drive it via the clinic-app, the lattice CLI, or Loupe."

## reinstall-package — Dev-loop: diff-apply ONE edited package's DDL/lens onto the
## RUNNING stack in place, no `make down` (F-004 upgrade-aware install). PKG=<dir>,
## e.g. `make reinstall-package PKG=packages/clinic-domain`. A same-version edit
## lands via --force; a bumped version auto-upgrades. The Processor commits the
## create/update/tombstone delta in one atomic batch and the Refractor re-projects
## the changed lenses live. CAVEAT: an ADDED lens/role/op won't activate under a
## live stack (the Refractor + DDL cache load lenses at install time) — for a
## brand-new entity use `make down && up-<vertical>`. See docs/components/_packages.md.
reinstall-package:
	@if [ -z "$(PKG)" ]; then echo "usage: make reinstall-package PKG=packages/<dir>"; exit 2; fi
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Diff-applying $(PKG) in place (no teardown)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force $(PKG)

## refresh-clinic — Dev-loop refresh of the Clinic vertical onto the RUNNING stack,
## no `make down`: diff-apply the vertical's packages in place (F-004 upgrade-aware
## install) AND rebuild+restart bin/clinic-app, so an edited handler / lens / DDL
## lands in one command. Requires an already-running up-clinic (or up-full +
## install-clinic). Mirrors up-clinic minus the teardown + up-full. CAVEAT: an
## ADDED lens/role/op won't activate under a live stack — use `make down && up-clinic`.
refresh-clinic:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Diff-applying clinic packages in place..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/orchestration-base
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/clinic-domain
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/clinic-reminders
	@echo "==> Rebuilding clinic-app binary..."
	go build -o bin/clinic-app ./cmd/clinic-app
	@echo "==> Restarting clinic-app..."
	-pkill -f "bin/clinic-app" 2>/dev/null || true
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/clinic-app >clinic-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> Clinic refreshed (packages diff-applied + clinic-app restarted). Patient app: http://127.0.0.1:7799"

## refresh-loftspace — Dev-loop refresh of the LoftSpace vertical onto the RUNNING
## stack, no `make down`: diff-apply the vertical's packages in place (F-004
## upgrade-aware install) AND rebuild+restart bin/loftspace-app. Requires an
## already-running up-loftspace (or up-full + install-loftspace). Mirrors
## up-loftspace minus the teardown + up-full. CAVEAT: an ADDED lens/role/op won't
## activate under a live stack — use `make down && up-loftspace`.
refresh-loftspace:
	@echo "==> Building lattice-pkg..."
	go build -o bin/lattice-pkg ./cmd/lattice-pkg
	@echo "==> Diff-applying loftspace packages in place..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/orchestration-base
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/location-domain
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/loftspace-domain
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/service-domain
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/lattice-pkg install --force packages/lease-signing
	@$(MAKE) provision-loftspace-role
	@echo "==> Rebuilding loftspace-app binary..."
	go build -o bin/loftspace-app ./cmd/loftspace-app
	@echo "==> Restarting loftspace-app..."
	-pkill -f "bin/loftspace-app" 2>/dev/null || true
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
		LOFTSPACE_APP_PG_DSN="$(LOFTSPACE_APP_PG_DSN)" LOFTSPACE_APP_DEV_AUTH=1 \
		./bin/loftspace-app >loftspace-app.log 2>&1 </dev/null &
	@sleep 1
	@echo "==> LoftSpace refreshed (packages diff-applied + loftspace-app restarted). Applicant app: http://127.0.0.1:7788"

## run-loupe — Build + run Loupe (the view/control web app) in the FOREGROUND.
## Open http://127.0.0.1:7777. Requires a running deployment (make up / up-full).
run-loupe:
	@echo "==> Building loupe binary..."
	go build -o bin/loupe ./cmd/loupe
	@echo "==> Loupe on http://127.0.0.1:7777 (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/loupe

## run-loftspace-app — Build + run the LoftSpace applicant app in the FOREGROUND.
## Open http://127.0.0.1:7788. Requires a running deployment with the LoftSpace
## vertical (make up-full && make install-loftspace).
run-loftspace-app:
	@echo "==> Building loftspace-app binary..."
	go build -o bin/loftspace-app ./cmd/loftspace-app
	@echo "==> LoftSpace applicant app on http://127.0.0.1:7788 (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/loftspace-app

## run-clinic-app — Build + run the Clinic app in the FOREGROUND. Open
## http://127.0.0.1:7799. Requires a running deployment with the clinic vertical
## installed (make up-full + install-clinic, or make up-clinic).
run-clinic-app:
	@echo "==> Building clinic-app binary..."
	go build -o bin/clinic-app ./cmd/clinic-app
	@echo "==> Clinic app on http://127.0.0.1:7799 (Ctrl-C to stop)..."
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) ./bin/clinic-app

## test — Run all Go unit + integration tests.
## Test packages run concurrently (-p 4). Every embedded NATS/JetStream
## fixture binds a random port (Port = -1) and owns a private StoreDir
## under t.TempDir(), so concurrent packages share no JetStream file state.
test:
	@echo "==> go test ./... -p 4"
	go test ./... -p 4

## test-bypass — Run the Phase 1 Gate 2 adversarial bypass test suite.
## Requires a running Docker stack (make up). Exits 0 only when all 4 bypass
## categories are BLOCKED. Writes gate2-report.txt and the Health KV marker.
.PHONY: test-bypass
test-bypass:
	@$(MAKE) down
	@$(MAKE) up
	@$(MAKE) verify-kernel
	go test ./internal/bypass/... -v -count=1

## test-capability-adversarial — Run the Phase 1 Gate 3 Capability Lens
## adversarial test suite. Requires a running Docker stack (make up). Exits 0
## only when all 4 attack vectors are DEFENDED. Writes gate3-report.txt and the
## Health KV marker at health.gates.phase1.gate3.
##
## Per-vector tests (TestCapAdv_*) use embedded NATS and are self-contained.
## The TestGate3_Report roll-up connects to the live stack for the Health KV marker.
.PHONY: test-capability-adversarial
test-capability-adversarial:
	@$(MAKE) down
	@$(MAKE) up
	@$(MAKE) verify-kernel
	go test ./internal/bypass/... -v -run TestCapAdv -count=1
	go test ./internal/bypass/... -v -run TestGate3_Report -count=1

## test-hello-lattice — Run the Phase 1 Gate 5 Hello Lattice integration test suite.
## Requires a running Docker stack (make up) with Refractor live.
## Exits 0 when all six milestones pass and the gate5 Health KV marker is written.
.PHONY: test-hello-lattice
POSTGRES_URL ?= postgres://lattice:lattice_dev@localhost:5432/lattice?sslmode=disable

test-hello-lattice:
	NATS_URL=$(NATS_URL) BOOTSTRAP_JSON_PATH=$(BOOTSTRAP_JSON) \
	  POSTGRES_URL=$(POSTGRES_URL) \
	  go test -tags integration ./internal/hellolattice/... -v -p 1 -count=1 -timeout 30m

## test-health-completeness — Run the Health KV completeness integration test.
## Requires a running Docker stack (make up) with Processor + Refractor live.
## Asserts every non-event-driven Health KV key is present within 30s.
.PHONY: test-health-completeness
test-health-completeness:
	NATS_URL=$(NATS_URL) go test -tags integration ./internal/healthkv/... -v -timeout 90s

## test-rollback — Run the Phase 1 Gate 4 compensating-op rollback test suite.
## Self-contained: uses embedded NATS, no Docker stack required.
## Exits 0 when the full create → discover → compensate → verify cycle passes
## for both DDL vertex type and lens branches.
.PHONY: test-rollback
test-rollback:
	go test ./internal/aiagent/... -run TestGate4_CompensatingOpRollback -v -p 1 -count=1

## test-lease-convergence — Story 14.5 external-I/O idempotency + convergence gate.
## Self-contained: embedded NATS, no Docker stack. Boots Processor + Refractor +
## Loom + Weaver + the live bridge in-process, installs the real package chain,
## drives a lease application to steady-state convergence through the live bridge
## (Loom externalTask + bridge + temporal freshness + tasks), proves the external
## effect is at-most-once (FR58 end-to-end), asserts D5 (outcome in aspect, root
## data minimal), and exercises the eager bgcheck-freshness @at lapse. Compiled
## with -tags leaseshortwindow so the freshness window is short enough to watch a
## lapse in bounded wall-clock (the production window stays 5m).
.PHONY: test-lease-convergence
test-lease-convergence:
	go test -tags leaseshortwindow ./internal/leaseconvergence/... -run TestLeaseConvergence -v -p 1 -count=1 -timeout 10m

## test-object-gc — v1b object-GC Loop A+B convergence gate. Self-contained:
## embedded NATS, boots Processor + outbox + Refractor + Weaver + the
## object-store-manager in-process, installs rbac → identity → objects-base,
## attaches + detaches an object, and proves the full chain reclaims the bytes
## (the objectLiveness lens → Weaver directOp(TombstoneObject) → object.tombstoned
## → manager byte delete). Compiled with -tags objectgc.
.PHONY: test-object-gc
test-object-gc:
	go test -tags objectgc ./internal/objectgc/... -run TestObjectGC -v -p 1 -count=1 -timeout 3m

## vet — Run go vet on all packages except vendored ANTLR-generated parsers
## (which contain expected unreachable-code patterns from the generator).
##
## -unreachable=false: ANTLR-generated source uses an unreachable
## `goto errorExit` trick after `return` statements. Since 3.1b-i wires
## the `full` rule engine to actually import the cypher package, the
## unreachable-code analyzer reports on those generated files even when
## the cypher package itself is excluded from the package list (vet's
## unreachable analyzer scans files of imported packages). Disabling
## the unreachable analyzer is the targeted fix — every other vet
## analyzer remains enabled.
vet:
	@echo "==> go vet ./... (excluding vendored ANTLR parsers)"
	go vet -unreachable=false $$(go list ./... | grep -v 'internal/refractor/ruleengine/full/cypher')

## lint-conventions — Static check for CLAUDE.md code conventions (history/changelog
## comments, asp.* key prefixes). Advisory by default; STRICT=1 exits non-zero.
lint-conventions:
	@echo "==> Linting code conventions..."
	go run ./scripts/lint-conventions.go

## install-skills — Install the canonical agentic-ops role-skills from agents/
## into the (gitignored) .claude/skills/ where the harness discovers them.
install-skills:
	@mkdir -p .claude/skills
	@for d in agents/*/; do \
		name=$$(basename $$d); \
		rm -rf ".claude/skills/$$name"; \
		cp -R "$$d" ".claude/skills/$$name"; \
		echo "installed skill: $$name"; \
	done

## clean — Remove compiled binaries.
clean:
	rm -rf bin/

## logs — Show container logs.
logs:
	docker compose logs -f

## ps — Show running containers.
ps:
	docker compose ps
